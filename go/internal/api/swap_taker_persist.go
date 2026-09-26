package api

// Käufer-Swaps überstehen Neustarts.
//
// Der Käufer erzeugt das Geheimnis und hielt es bisher nur im Arbeitsspeicher.
// Startete sein Pi mitten im Swap neu (z.B. für ein Update), war es verloren:
// Er konnte die Sperre des Anbieters nicht mehr einlösen, beide mussten nach
// Ablauf zurückholen. Jetzt wird es – mit den Schlüsseln zum Einlösen –
// verschlüsselt gespeichert (gleiches Verfahren wie die Anbieter-Hinterlegungen,
// Pi-eigener Schlüssel swap-deposits.key, nur für root lesbar), und eine
// Schleife setzt unterbrochene Swaps fort.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gagliardetto/solana-go"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
)

type persistedTaker struct {
	SwapID    string   `json:"id"`
	OrderID   string   `json:"o"`
	Hashlock  string   `json:"h"`
	Secret    string   `json:"x"`
	Sol       string   `json:"s,omitempty"` // Base58
	Fnd       []string `json:"f,omitempty"`
	GiveChain string   `json:"g"`
	CpSol     string   `json:"cs,omitempty"`
	CpFnd     string   `json:"cf,omitempty"`
	OwnSol    string   `json:"os,omitempty"`
	OwnFnd    string   `json:"of,omitempty"`
	AmountSOL float64  `json:"as"`
	AmountFND float64  `json:"af"`
	Created   int64    `json:"t"`
}

var (
	takerMu    sync.Mutex
	takerStore = map[string]persistedTaker{}
)

func (sc *swapCoordinator) takerPaths() (data, key string, ok bool) {
	if sc.server == nil || sc.server.cfg == nil || sc.server.cfg.DataDir == "" {
		return "", "", false
	}
	d := sc.server.cfg.DataDir
	return filepath.Join(d, "swap-taker.enc"), filepath.Join(d, "swap-deposits.key"), true
}

// saveTakersLocked: takerMu muss gehalten werden.
func (sc *swapCoordinator) saveTakersLocked() {
	dataPath, keyPath, ok := sc.takerPaths()
	if !ok {
		return
	}
	if len(takerStore) == 0 {
		_ = os.Remove(dataPath)
		return
	}
	plain, err := json.Marshal(takerStore)
	if err != nil {
		return
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	key, err := sc.depositKey(keyPath)
	if err != nil {
		sc.logWarn("Käufer-Swaps: Speicherschlüssel nicht verfügbar", err)
		return
	}
	enc, err := identity.Encrypt(plain, key)
	if err != nil {
		sc.logWarn("Käufer-Swaps: Verschlüsseln fehlgeschlagen", err)
		return
	}
	tmp := dataPath + ".tmp"
	if os.WriteFile(tmp, enc, 0o600) == nil {
		_ = os.Rename(tmp, dataPath)
	}
}

// loadTakers: nach dem Start die gespeicherten Käufer-Swaps laden.
func (sc *swapCoordinator) loadTakers() {
	dataPath, keyPath, ok := sc.takerPaths()
	if !ok {
		return
	}
	enc, err := os.ReadFile(dataPath)
	if err != nil {
		return
	}
	key, err := os.ReadFile(keyPath)
	if err != nil || len(key) != 32 {
		return
	}
	plain, err := identity.Decrypt(enc, key)
	if err != nil {
		sc.logWarn("Käufer-Swaps: Entschlüsseln fehlgeschlagen", err)
		return
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	m := map[string]persistedTaker{}
	if json.Unmarshal(plain, &m) != nil {
		return
	}
	takerMu.Lock()
	takerStore = m
	takerMu.Unlock()
	if sc.server.log != nil && len(m) > 0 {
		sc.server.log.Info("Käufer-Swaps nach Neustart wiederhergestellt", zap.Int("swaps", len(m)))
	}
}

// rememberTaker: Käufer-Swap mit Geheimnis und Einlöse-Schlüsseln sichern.
func (sc *swapCoordinator) rememberTaker(ss *swapSession) {
	p := persistedTaker{
		SwapID: ss.swapID, OrderID: ss.orderID, Hashlock: hex.EncodeToString(ss.secretHash[:]),
		Secret: hex.EncodeToString(ss.secret[:]), Fnd: append([]string(nil), ss.fndSeed...),
		GiveChain: ss.giveChain, CpSol: ss.counterpartySol, CpFnd: ss.counterpartyFnd,
		OwnSol: ss.ownSol, OwnFnd: ss.ownFnd, AmountSOL: ss.amountSOL, AmountFND: ss.amountFND,
		Created: time.Now().Unix(),
	}
	if len(ss.solKey) == 64 {
		p.Sol = ss.solKey.String()
	}
	takerMu.Lock()
	takerStore[ss.swapID] = p
	sc.saveTakersLocked()
	takerMu.Unlock()
}

func (sc *swapCoordinator) forgetTaker(swapID string) {
	takerMu.Lock()
	if _, ok := takerStore[swapID]; ok {
		delete(takerStore, swapID)
		sc.saveTakersLocked()
	}
	takerMu.Unlock()
}

// resumeTakersOnce: unterbrochene Käufer-Swaps fortsetzen (aus resumeSolClaimsLoop).
func (s *Server) resumeTakersOnce() {
	if s.swapCoord == nil || s.swapMgr == nil || s.chain == nil {
		return
	}
	takerMu.Lock()
	list := make([]persistedTaker, 0, len(takerStore))
	for _, p := range takerStore {
		list = append(list, p)
	}
	takerMu.Unlock()
	for _, p := range list {
		// Läuft der Swap noch im Arbeitsspeicher? Dann nichts tun.
		s.orch.mu.Lock()
		_, running := s.orch.sessions[p.SwapID]
		s.orch.mu.Unlock()
		if running {
			continue
		}
		// Abgeschlossen, zurückgeholt, verworfen oder zu alt → vergessen.
		s.swapMgr.mu.RLock()
		sw, ok := s.swapMgr.swaps[p.SwapID]
		var phase SwapPhase
		var done bool
		var note string
		if ok {
			phase, done, note = sw.Phase, sw.Done, sw.Note
		}
		s.swapMgr.mu.RUnlock()
		if !ok || done || phase == SwapRefunded || phase == SwapDiscarded || phase == SwapFndClaimed ||
			time.Since(time.Unix(p.Created, 0)) > 7*24*time.Hour {
			s.swapCoord.forgetTaker(p.SwapID)
			continue
		}
		if phase == SwapExpired && strings.Contains(note, "passt nicht") {
			continue // Sperre des Anbieters wurde abgelehnt – nur noch zurückholen (Rückhol-Schleife)
		}
		s.resumeTaker(p)
	}
}

func (s *Server) resumeTaker(p persistedTaker) {
	hash, err1 := hash32FromHex(p.Hashlock)
	sec, err2 := hash32FromHex(p.Secret)
	if err1 != nil || err2 != nil {
		return
	}
	var solKey solana.PrivateKey
	if p.Sol != "" {
		if k, err := solana.PrivateKeyFromBase58(p.Sol); err == nil {
			solKey = k
		}
	}
	ss := &swapSession{
		swapID: p.SwapID, orderID: p.OrderID, isTaker: true, giveChain: p.GiveChain,
		solKey: solKey, fndSeed: p.Fnd, secret: sec, secretHash: hash,
		counterpartySol: p.CpSol, counterpartyFnd: p.CpFnd, ownSol: p.OwnSol, ownFnd: p.OwnFnd,
		amountSOL: p.AmountSOL, amountFND: p.AmountFND,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if p.GiveChain == "sol" {
		// Ich gab SOL, Anbieter gibt FND.
		// 1. Eigene SOL-Sperre noch offen? Sonst NICHT einlösen (bereits zurückgeholt).
		if len(solKey) != 64 {
			return
		}
		client, err := newSolHTLCClient(s.swapMgr.rpcURL(), s.swapMgr.htlcProgramID)
		if err != nil {
			return
		}
		myPDA, _, err := client.deriveSwapPDA(solKey.PublicKey(), hash)
		if err != nil {
			return
		}
		if ok, cerr := (&orchestrator{}).solAccountCheck(ctx, s, myPDA.String()); cerr != nil || !ok {
			return
		}
		// 2. Sperre des Anbieters vorhanden und gültig?
		id, found := s.findFndHTLCByHashlock(hash, p.OwnFnd)
		if !found {
			return
		}
		if hid, err := hash32FromHex(id); err == nil {
			if h, ok := s.chain.GetHTLC(hid); ok && h.State == chain.HTLCClaimed {
				s.markSwapDone(p.SwapID) // bereits eingelöst
				return
			}
		}
		if err := s.verifyCounterpartyLock(ctx, ss, id); err != nil {
			s.setSwapPhase(p.SwapID, SwapExpired, "nach Neustart: Sperre des Anbieters passt nicht: "+err.Error()+" – eigene Sperre wird nach Ablauf zurückgeholt")
			return
		}
		// 3. Einlösen (enthüllt das Geheimnis; der Anbieter holt dann die SOL).
		if err := s.orchestratorFndClaim(p.Fnd, id, sec); err != nil {
			s.setSwapPhase(p.SwapID, SwapSolLocked, "nach Neustart: FND-Einlösung fehlgeschlagen: "+truncate(err.Error(), 140)+" – neuer Versuch in 2 min")
			return
		}
		s.setSwapPhase(p.SwapID, SwapFndClaimed, "FND eingelöst (nach Neustart fortgesetzt)")
		s.markSwapDone(p.SwapID)
		if s.log != nil {
			s.log.Info("Käufer-Swap nach Neustart abgeschlossen", zap.String("swap", p.SwapID))
		}
		return
	}

	// Ich gab FND, Anbieter gibt SOL.
	// 1. Eigene FND-Sperre (an den Anbieter) noch offen?
	if id, found := s.findFndHTLCByHashlock(hash, p.CpFnd); !found {
		return
	} else if hid, err := hash32FromHex(id); err != nil {
		return
	} else if h, ok := s.chain.GetHTLC(hid); !ok || h.State != chain.HTLCLocked {
		return // bereits eingelöst oder zurückgeholt
	}
	// 2. SOL-Sperre des Anbieters vorhanden und gültig?
	makerSol, err := solana.PublicKeyFromBase58(p.CpSol)
	if err != nil || len(solKey) != 64 {
		return
	}
	client, err := newSolHTLCClient(s.swapMgr.rpcURL(), s.swapMgr.htlcProgramID)
	if err != nil {
		return
	}
	pda, _, err := client.deriveSwapPDA(makerSol, hash)
	if err != nil {
		return
	}
	if ok, cerr := (&orchestrator{}).solAccountCheck(ctx, s, pda.String()); cerr != nil || !ok {
		return
	}
	if err := s.verifyCounterpartyLock(ctx, ss, "sol"); err != nil {
		s.setSwapPhase(p.SwapID, SwapExpired, "nach Neustart: Sperre des Anbieters passt nicht: "+err.Error()+" – eigene Sperre wird nach Ablauf zurückgeholt")
		return
	}
	// 3. Einlösen mit Wiederholung (eigene Goroutine; läuft höchstens einmal je Swap).
	key := append(solana.PrivateKey(nil), solKey...)
	go func() {
		if s.redeemSolRetry(p.SwapID, key, makerSol, hash, sec, time.Now().Add(20*time.Hour), SwapFndClaimed, SwapSolLocked) {
			s.markSwapDone(p.SwapID)
		}
	}()
}
