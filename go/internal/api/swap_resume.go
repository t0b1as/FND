package api

// Rückholung abgelaufener Sperren – anhand der CHAIN, nicht des Arbeitsspeichers.
//
// Bisher lief die Rückholung als Goroutine mit den Schlüsseln im RAM; nach einem
// Neustart (Update, Absturz) war sie weg. Verloren ging dabei nichts – das Recht
// zur Rückholung liegt auf der Chain und verfällt nicht –, aber niemand löste
// sie mehr aus. Jetzt prüft der Node alle 5 Minuten die gespeicherten Swaps
// gegen die tatsächlichen Sperren auf beiden Chains und holt zurück, was ihm
// gehört und abgelaufen ist. Schlüssel: hinterlegte Order-Schlüssel oder eine
// angemeldete Sitzung (ohne Argon2-Ableitung).

import (
	"encoding/json"
	"encoding/binary"
	"encoding/base64"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gagliardetto/solana-go"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
)

// ownKey: ein eigener Schlüsselsatz (FND + daraus abgeleitetes Solana).
type ownKey struct {
	fnd *ecdsa.PrivateKey
	sol solana.PrivateKey
}

// collectOwnKeys: Schlüssel aus Hinterlegungen und angemeldeten Sitzungen (Kopien).
func (s *Server) collectOwnKeys() []ownKey {
	var out []ownKey
	if s.swapCoord != nil {
		s.swapCoord.mu.Lock()
		for _, d := range s.swapCoord.deposits {
			k := ownKey{}
			if len(d.solKey) == 64 {
				k.sol = append(solana.PrivateKey(nil), d.solKey...)
			}
			if len(d.fndSeed) > 0 {
				if fk, err := fndKeyFromWords(d.fndSeed); err == nil {
					k.fnd = fk
				}
			}
			if k.fnd != nil || k.sol != nil {
				out = append(out, k)
			}
		}
		s.swapCoord.mu.Unlock()
	}
	sessionMu.RLock()
	sessions := make([]*Session, 0, len(sessionStore))
	for _, sess := range sessionStore {
		sessions = append(sessions, sess)
	}
	sessionMu.RUnlock()
	for _, sess := range sessions {
		if sess == nil || sess.identity == nil || sess.identity.ChainAddr() == "" {
			continue // nur Sitzungen mit vorhandenem Wallet-Schlüssel (keine Ableitung)
		}
		if ck, err := sess.identity.ChainPrivateKey(); err == nil {
			out = append(out, ownKey{fnd: ck, sol: solKeyFromChain(ck)})
		}
	}
	return out
}

func wipeOwnKeys(keys []ownKey) {
	for _, k := range keys {
		if k.fnd != nil {
			k.fnd.D.SetInt64(0)
		}
		for i := range k.sol {
			k.sol[i] = 0
		}
	}
}

func (s *Server) resumeRefundsLoop() {
	time.Sleep(60 * time.Second) // Chain-Sync abwarten
	for {
		s.resumeRefundsOnce()
		time.Sleep(5 * time.Minute)
	}
}

func (s *Server) resumeRefundsOnce() {
	if s.swapMgr == nil || s.chain == nil {
		return
	}
	type cand struct {
		id, fndHTLC, hashlock, buyerSol, sellerSol string
		solOnly                                    bool // verworfen: FND-Sperre gibt es nicht mehr, SOL-Sperre evtl. schon
	}
	var list []cand
	// Nur Swaps der letzten 14 Tage (alle Fristen liegen weit darunter) –
	// alte Einträge verursachen sonst unnötige Solana-Abfragen.
	cutoff := time.Now().Add(-14 * 24 * time.Hour).Unix()
	s.swapMgr.mu.RLock()
	for _, sw := range s.swapMgr.swaps {
		if sw == nil || sw.Phase == SwapRefunded || sw.Done || (sw.CreatedAt > 0 && sw.CreatedAt < cutoff) {
			continue
		}
		list = append(list, cand{sw.ID, sw.FndHTLCID, sw.Hashlock, sw.BuyerSol, sw.SellerSol, sw.Phase == SwapDiscarded})
	}
	s.swapMgr.mu.RUnlock()
	if len(list) == 0 {
		return
	}
	keys := s.collectOwnKeys()
	defer wipeOwnKeys(keys)
	if len(keys) == 0 {
		return
	}
	height := s.chain.Height()
	for _, c := range list {
		// ── Gegenrichtung: Ich habe SOL gegeben, die Gegenseite FND. Hat sie
		// meine SOL eingelöst, steht das Geheimnis in ihrer Solana-Transaktion –
		// damit hole ich ihre FND ab (vor Ablauf ihrer Sperre, sonst hätte sie
		// beides). Früher lief das nur im Arbeitsspeicher des laufenden Swaps.
		if c.hashlock != "" && s.swapMgr.htlcProgramID != "" {
			if hash, err := hash32FromHex(c.hashlock); err == nil {
				s.tryClaimFndWithSolSecret(c.id, hash, keys)
			}
		}
		// ── FND: eigene, noch gesperrte, abgelaufene Sperre ──
		if c.fndHTLC != "" && !c.solOnly {
			if id, err := hash32FromHex(c.fndHTLC); err == nil {
				if h, ok := s.chain.GetHTLC(id); ok && h.State == chain.HTLCLocked && height >= h.Timelock {
					for _, k := range keys {
						if k.fnd == nil || chain.PubkeyToAddress(&k.fnd.PublicKey) != h.Sender {
							continue
						}
						words := []string{fndKeyPrefix + hex.EncodeToString(crypto.FromECDSA(k.fnd))}
						_, _, _, err := s.submitChainTx(words, chain.TxHTLCRefund, chain.FeeForValue(nil), chain.EncodeHTLCRefund(id))
						if err == nil {
							s.setSwapPhase(c.id, SwapRefunded, "FND zurückgeholt (Frist abgelaufen)")
							if s.log != nil {
								s.log.Info("FND-Sperre zurückgeholt", zap.String("swap", c.id))
							}
						}
						break
					}
				}
			}
		}
		// ── SOL: Swap-Konto einer eigenen Adresse existiert noch → zurückholen
		// versuchen (vor Ablauf lehnt die Vorab-Simulation ab, ohne Gebühr) ──
		if c.hashlock == "" || s.swapMgr.htlcProgramID == "" {
			continue
		}
		hash, err := hash32FromHex(c.hashlock)
		if err != nil {
			continue
		}
		// Die Adressen fehlten bis R497 in automatischen Swap-Einträgen – daher
		// jeden eigenen Schlüssel prüfen: Kontoadresse = f(Schlüssel, Hashlock).
		tried := map[string]bool{}
		for _, k := range keys {
			if k.sol == nil {
				continue
			}
			pub := k.sol.PublicKey().String()
			if tried[pub] {
				continue
			}
			tried[pub] = true
			if (c.buyerSol != "" || c.sellerSol != "") && pub != c.buyerSol && pub != c.sellerSol {
				continue // Adressen bekannt und passen nicht
			}
			s.trySolRefund(c.id, k.sol, hash)
		}
	}
}

// trySolRefund: holt eine eigene, abgelaufene SOL-Sperre zurück, falls vorhanden.
func (s *Server) trySolRefund(swapID string, key solana.PrivateKey, hash [32]byte) {
	client, err := newSolHTLCClient(s.swapMgr.rpcURL(), s.swapMgr.htlcProgramID)
	if err != nil {
		return
	}
	pda, _, err := client.deriveSwapPDA(key.PublicKey(), hash)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	expiry, ok, cerr := s.solLockExpiry(ctx, pda)
	if cerr != nil || !ok {
		return // kein offenes Swap-Konto (eingelöst, zurückgeholt oder nie gesperrt)
	}
	if slot, serr := s.solCurrentSlot(ctx); serr != nil || slot < expiry {
		return // Frist noch nicht abgelaufen – keine Transaktion versuchen
	}
	if _, rerr := client.Refund(ctx, key, hash); rerr == nil {
		s.setSwapPhase(swapID, SwapRefunded, "SOL zurückgeholt (Frist abgelaufen)")
		if s.log != nil {
			s.log.Info("SOL-Sperre zurückgeholt", zap.String("swap", swapID))
		}
	} else if !strings.Contains(rerr.Error(), "Expiry") && !strings.Contains(rerr.Error(), "expir") && s.log != nil {
		s.log.Debug("SOL-Rückholung noch nicht möglich", zap.String("swap", swapID), zap.Error(rerr))
	}
}

// tryClaimFndWithSolSecret: offene FND-Sperre an mich + Geheimnis aus meiner
// eingelösten SOL-Sperre → FND abholen. Wiederholbar (bereits eingelöst =
// Sperre nicht mehr offen = nichts zu tun).
func (s *Server) tryClaimFndWithSolSecret(swapID string, hash [32]byte, keys []ownKey) {
	for _, k := range keys {
		if k.fnd == nil || k.sol == nil {
			continue
		}
		myFnd := chain.PubkeyToAddress(&k.fnd.PublicKey)
		id, h, found := s.chain.FindHTLCByHashlock(hash, myFnd)
		if !found || h.State != chain.HTLCLocked {
			continue // keine offene FND-Sperre für diese Adresse
		}
		client, err := newSolHTLCClient(s.swapMgr.rpcURL(), s.swapMgr.htlcProgramID)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		secret, ok := client.ReadSecretFromClaim(ctx, s.swapMgr, k.sol.PublicKey(), hash)
		cancel()
		if !ok {
			continue // Gegenseite hat meine SOL (noch) nicht eingelöst
		}
		words := []string{fndKeyPrefix + hex.EncodeToString(crypto.FromECDSA(k.fnd))}
		if err := s.orchestratorFndClaim(words, hex.EncodeToString(id[:]), secret); err != nil {
			s.setSwapPhase(swapID, SwapSolClaimed, "FND-Abholung fehlgeschlagen: "+truncate(err.Error(), 140)+" – nächster Versuch in 5 min")
			return
		}
		s.setSwapPhase(swapID, SwapSolClaimed, "FND abgeholt (Geheimnis aus der Solana-Einlösung)")
		s.markSwapDone(swapID)
		s.swapMgr.mu.RLock()
		var orderID string
		var amount float64
		if sw, ok := s.swapMgr.swaps[swapID]; ok {
			orderID, amount = sw.OrderID, sw.AmountFND
		}
		s.swapMgr.mu.RUnlock()
		s.settleOrder(swapID, orderID, amount, "")
		if s.log != nil {
			s.log.Info("FND mit Geheimnis aus Solana abgeholt", zap.String("swap", swapID))
		}
		return
	}
}

// ── Altlasten aus einer früheren Chain ─────────────────────────────────────
// Nach einem Chain-Neustart (z.B. R477) blieben Swaps der alten Chain als
// "laufend" stehen: ihre FND-Sperren gibt es auf der neuen Chain nicht. Sie
// werden als "discarded" markiert und von allen Prüfschleifen übersprungen.
// Kriterium: vor Block 1 der aktuellen Chain angelegt, nicht abgeschlossen und
// – falls eine FND-Sperre vermerkt ist – diese existiert hier nicht.
func (s *Server) discardOldChainSwapsLoop() {
	time.Sleep(90 * time.Second) // Chain-Sync abwarten
	for {
		s.discardOldChainSwaps()
		time.Sleep(24 * time.Hour)
	}
}

func (s *Server) discardOldChainSwaps() {
	if s.chain == nil || s.swapMgr == nil {
		return
	}
	first := int64(s.chain.FirstBlockTime())
	if first == 0 {
		return
	}
	n := 0
	s.swapMgr.mu.Lock()
	for _, sw := range s.swapMgr.swaps {
		if sw == nil || sw.CreatedAt == 0 || sw.CreatedAt >= first || sw.Done || sw.Settled {
			continue
		}
		switch sw.Phase {
		case SwapSolClaimed, SwapRefunded, SwapDiscarded:
			continue
		}
		if sw.FndHTLCID != "" {
			if id, err := hash32FromHex(sw.FndHTLCID); err == nil {
				if _, ok := s.chain.GetHTLC(id); ok {
					continue // Sperre existiert auf dieser Chain → keine Altlast
				}
			}
		}
		sw.Phase = SwapDiscarded
		sw.Note = "verworfen (Chain-Neustart) – gehörte zu einer früheren Chain"
		n++
	}
	s.swapMgr.mu.Unlock()
	if n > 0 && s.log != nil {
		s.log.Info("Altlasten aus früherer Chain verworfen", zap.Int("swaps", n))
	}
}

// solLockExpiry: Ablauf-Slot eines Swap-Kontos (ok=false: Konto existiert nicht).
func (s *Server) solLockExpiry(ctx context.Context, pda solana.PublicKey) (uint64, bool, error) {
	res, err := s.swapMgr.solanaRPCCall(ctx, "getAccountInfo", []interface{}{
		pda.String(), map[string]interface{}{"encoding": "base64", "commitment": "confirmed"},
	})
	if err != nil {
		return 0, false, err
	}
	var v struct {
		Value *struct {
			Data  []string `json:"data"`
			Owner string   `json:"owner"`
		} `json:"value"`
	}
	if json.Unmarshal(res, &v) != nil || v.Value == nil || len(v.Value.Data) == 0 || v.Value.Owner != s.swapMgr.htlcProgramID {
		return 0, false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(v.Value.Data[0])
	if err != nil || len(raw) < 24 {
		return 0, false, nil
	}
	return binary.LittleEndian.Uint64(raw[16:24]), true, nil
}

// solCurrentSlot: aktueller Slot (confirmed).
func (s *Server) solCurrentSlot(ctx context.Context) (uint64, error) {
	res, err := s.swapMgr.solanaRPCCall(ctx, "getSlot", []interface{}{map[string]interface{}{"commitment": "confirmed"}})
	if err != nil {
		return 0, err
	}
	var slot uint64
	if err := json.Unmarshal(res, &slot); err != nil {
		return 0, err
	}
	return slot, nil
}
