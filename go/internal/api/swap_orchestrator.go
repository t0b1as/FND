package api

// Automatischer Swap-Orchestrator. Nach einem Match führt der Node den atomaren
// FND↔SOL-Swap selbstständig durch: sperren, den Lock der Gegenseite abwarten,
// über Kreuz einlösen. Die privaten Schlüssel liegen dafür — bewusst — bis zum
// Abschluss im RAM (nie auf Platte), an die Swap-Session gebunden, und werden
// nach Abschluss/Abbruch genullt. Bei Node-Neustart sind sie weg; dann greift
// der Timelock-Refund.
//
// SICHERHEIT — asymmetrische Timelocks:
//   Wer ZUERST sperrt (Käufer, SOL), bekommt den LÄNGEREN Timelock.
//   Die zweite Seite (Verkäufer, FND) den KÜRZEREN.
//   Sonst entsteht ein Zeitfenster, in dem eine Seite einlösen UND zurückholen
//   könnte. Der Käufer löst zuerst die FND ein (enthüllt S), erst danach kann
//   der Verkäufer die SOL einlösen — der Käufer braucht also genug Zeitpuffer.

import (
	"strconv"
	"fmt"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gagliardetto/solana-go"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
)

// Timelock-Parameter (Sicherheitsabstand zwischen den Seiten).
const (
	// SICHERHEIT — asymmetrische Timelocks, gekoppelt an die SPERR-REIHENFOLGE:
	//   Wer ZUERST sperrt (der Taker, der das Geheimnis erzeugt), braucht den
	//   LÄNGEREN Timelock. Der Maker sperrt als Zweiter mit kürzerem Timelock.
	//   Das gilt für BEIDE Kaufrichtungen: Gibt der Taker SOL, sperrt SOL zuerst
	//   (lang); gibt der Taker FND, sperrt FND zuerst (lang). Jede Kette hat
	//   daher einen "first locker"- und einen "second locker"-Wert.
	//
	//   first MUSS > second sein (real: ~48h vs ~24h), sonst Betrugsfenster.
	firstLockerTimelockSlots   = 432000 // SOL als Erst-Sperrer (~48h, Slots à ~400ms)
	secondLockerTimelockSlots  = 216000 // SOL als Zweit-Sperrer (~24h)
	firstLockerTimelockBlocks  = 34560  // FND als Erst-Sperrer (~48h, Blöcke à 5s)
	secondLockerTimelockBlocks = 17280  // FND als Zweit-Sperrer (~24h)

	// Rückwärtskompatible Aliase.
	solTimelockSlots  = firstLockerTimelockSlots
	fndTimelockBlocks = secondLockerTimelockBlocks
	// Poll-Intervall, in dem der Orchestrator die Chains auf Fortschritt prüft.
	// 2s ist reaktionsschnell genug (FND-Blockzeit 5s, Solana schneller) ohne
	// die RPCs zu überlasten.
	orchestratorPollInterval = 4 * time.Second // schont das Anfrage-Limit öffentlicher RPCs
	// Maximale Gesamtdauer, bevor ein Swap als gescheitert gilt und refundet wird.
	orchestratorMaxDuration = 50 * time.Minute
)

// swapSession hält die flüchtigen Geheimnisse eines laufenden Swaps im RAM.
type swapSession struct {
	swapID string

	// Rolle + Handelsrichtung.
	//  isTaker: true = wir nehmen die Order an, erzeugen das Geheimnis, sperren
	//           ZUERST (langer Timelock). Maker sperrt als Zweiter (kurz).
	//  giveChain: welche Kette WIR weggeben — "sol" oder "fnd". Bestimmt, auf
	//           welcher Kette wir sperren und auf welcher wir einlösen.
	isTaker   bool
	giveChain string // "sol" oder "fnd" (was diese Seite weggibt)

	// Geheimnisse — NUR im RAM, werden nach Abschluss genullt.
	secret     [32]byte // das Preimage (nur der Taker kennt es anfangs)
	secretHash [32]byte // H = blake3(secret)
	solKey     solana.PrivateKey
	fndSeed    []string // FND-Seed-Wörter

	// Gegenseiten-Adressen (auf beiden Ketten).
	counterpartySol string // Solana-Adresse der Gegenseite
	counterpartyFnd string // FND-Adresse der Gegenseite

	// Eigene Empfangsadressen (aus der eigenen Order) — nötig, um den Lock der
	// Gegenseite zu finden, wenn man auf einer Kette nur EMPFÄNGT (keinen Seed hat).
	ownSol string
	ownFnd string

	amountSOL float64
	amountFND float64

	fndHTLCID string // ID des eigenen FND-Locks (für Refund)
	orderID   string // zugehörige Order (für Aufräumen bei Abschluss)
	takerOwnOrderID string // beim Auto-Match: die eigene Order des Takers

	refundPending bool // true = ein Refund-Watcher hat die Keys übernommen

	cancel context.CancelFunc
}

// timelockForRole gibt den Timelock-Wert je nachdem, ob diese Seite zuerst
// (Taker, lang) oder als Zweite (Maker, kurz) sperrt.
func (ss *swapSession) solTimelock() uint64 {
	if ss.isTaker {
		return firstLockerTimelockSlots
	}
	return secondLockerTimelockSlots
}

func (ss *swapSession) fndTimelock() uint64 {
	if ss.isTaker {
		return firstLockerTimelockBlocks
	}
	return secondLockerTimelockBlocks
}

// wipe nullt alle Geheimnisse der Session.
func (ss *swapSession) wipe() {
	for i := range ss.secret {
		ss.secret[i] = 0
	}
	for i := range ss.solKey {
		ss.solKey[i] = 0
	}
	ss.fndSeed = nil
}

// orchestrator verwaltet die laufenden Swap-Sessions.
type orchestrator struct {
	mu       sync.Mutex
	sessions map[string]*swapSession
	server   *Server
}

func newOrchestrator(s *Server) *orchestrator {
	return &orchestrator{sessions: make(map[string]*swapSession), server: s}
}

// startSwap beginnt einen automatischen Swap. Die Keys werden übernommen und
// bis zum Abschluss gehalten. Läuft in einer eigenen Goroutine.
func (o *orchestrator) startSwap(ss *swapSession) {
	ctx, cancel := context.WithTimeout(context.Background(), orchestratorMaxDuration)
	ss.cancel = cancel
	o.mu.Lock()
	o.sessions[ss.swapID] = ss
	o.mu.Unlock()
	go o.run(ctx, ss)
}

// run ist der Zustandsautomat des Swaps.
func (o *orchestrator) run(ctx context.Context, ss *swapSession) {
	defer func() {
		// Keys nur nullen, wenn KEIN Refund-Watcher sie übernommen hat.
		if !ss.refundPending {
			ss.wipe()
		}
		o.mu.Lock()
		delete(o.sessions, ss.swapID)
		o.mu.Unlock()
	}()

	s := o.server
	if ss.isTaker {
		o.runTaker(ctx, ss, s)
	} else {
		o.runMaker(ctx, ss, s)
	}
	// Nach Abschluss aufräumen: bei Erfolg (oder endgültigem Refund) die Order
	// entfernen und die hinterlegten Keys freigeben.
	o.finalizeSwap(ss)
}

// finalizeSwap räumt nach einem abgeschlossenen Swap auf: entfernt die zugehörige
// Order aus dem Orderbuch und gibt die hinterlegten Maker-Keys frei.
func (o *orchestrator) finalizeSwap(ss *swapSession) {
	if ss.orderID == "" {
		return
	}
	s := o.server
	// Auto-Match-Sperre IMMER freigeben (egal wie der Swap endete), damit ein
	// Rest der Order oder ein neuer Versuch wieder matchen kann.
	if ss.takerOwnOrderID != "" && s.swapCoord != nil {
		defer func() {
			s.swapCoord.mu.Lock()
			delete(s.swapCoord.activeMatches, ss.takerOwnOrderID)
			delete(s.swapCoord.activeSince, ss.takerOwnOrderID)
			s.swapCoord.mu.Unlock()
		}()
	}
	// Phase prüfen: nur bei finalem Zustand aufräumen.
	s.swapMgr.mu.RLock()
	sw, ok := s.swapMgr.swaps[ss.swapID]
	phase := SwapInitiated
	if ok {
		phase = sw.Phase
	}
	s.swapMgr.mu.RUnlock()
	if phase != SwapSolClaimed && phase != SwapFndClaimed && phase != SwapRefunded {
		if s.log != nil {
			s.log.Info("finalizeSwap: noch nicht final", zap.String("swap", ss.swapID), zap.String("phase", string(phase)))
		}
		return // noch nicht abgeschlossen (z.B. Refund-Watcher läuft noch)
	}
	if s.log != nil {
		s.log.Info("finalizeSwap: räume auf", zap.String("swap", ss.swapID), zap.String("phase", string(phase)), zap.String("order", ss.orderID), zap.Float64("amount", ss.amountFND))
	}
	// Nur der Maker verwaltet die Order (der Taker hat keine eigene für diesen Swap).
	if s.orderBook == nil {
		return
	}
	// Order um den geswappten Betrag reduzieren. Bleibt ein Rest, bleibt die
	// Order (und die hinterlegten Keys) für weitere Teilkäufe bestehen.
	ord, existed := s.orderBook.FindOrder(ss.orderID)
	if existed {
		remainingBefore := ord.AmountFND
		s.orderBook.reduceOrder(ss.orderID, ss.amountFND)
		// Wenn nach der Reduzierung nichts mehr übrig ist, Keys freigeben.
		if remainingBefore-ss.amountFND <= 0.00000001 && s.swapCoord != nil {
			s.swapCoord.releaseKeys(ss.orderID)
		}
	} else if s.swapCoord != nil {
		s.swapCoord.releaseKeys(ss.orderID)
	}
	// Beim Auto-Match: auch die EIGENE Order des Takers reduzieren (die fremde
	// Maker-Order reduziert der Maker-Node selbst).
	if ss.takerOwnOrderID != "" {
		if myOrd, ok := s.orderBook.FindOrder(ss.takerOwnOrderID); ok {
			remBefore := myOrd.AmountFND
			s.orderBook.reduceOrder(ss.takerOwnOrderID, ss.amountFND)
			if remBefore-ss.amountFND <= 0.00000001 && s.swapCoord != nil {
				s.swapCoord.releaseKeys(ss.takerOwnOrderID)
			}
		}
		// Auto-Match-Sperre freigeben, damit ein Rest der Order erneut matchen kann.
		if s.swapCoord != nil {
			s.swapCoord.mu.Lock()
			delete(s.swapCoord.activeMatches, ss.takerOwnOrderID)
			delete(s.swapCoord.activeSince, ss.takerOwnOrderID)
			s.swapCoord.mu.Unlock()
		}
	}
}

// runBuyer: Käufer-Ablauf.
//  1. SOL sperren (Verkäufer als Redeemer, langer Timelock).
//  2. Warten bis Verkäufer FND gesperrt hat.
//  3. FND einlösen (enthüllt S).
//  4. (Verkäufer löst dann selbst die SOL ein.)
// runTaker: der Taker gibt seine Give-Chain weg, sperrt ZUERST (langer
// Timelock), wartet auf den Maker-Lock, löst dann auf der Maker-Give-Chain ein
// (enthüllt dabei das Geheimnis).
func (o *orchestrator) runTaker(ctx context.Context, ss *swapSession, s *Server) {
	// 1. Auf eigener Give-Chain sperren (zuerst).
	if !o.lockGive(ctx, ss, s) {
		return // Fehler wurde in lockGive gesetzt
	}
	// 2. Warten, bis der Maker auf SEINER Give-Chain gesperrt hat.
	otherLockID, ok := o.waitForMakerLock(ctx, ss, s)
	if !ok {
		s.setSwapPhase(ss.swapID, SwapExpired, "Maker hat nicht gesperrt — Refund eingeplant")
		o.scheduleRefund(ss)
		return
	}
	// 3. Auf der Maker-Give-Chain einlösen — enthüllt das Geheimnis.
	if !o.claimTake(ctx, ss, s, otherLockID) {
		return
	}
	// Der Maker liest S von der Chain und löst seinerseits ein.
}

// runMaker: der Maker wartet auf den Taker-Lock, sperrt dann auf SEINER
// Give-Chain (kürzerer Timelock), wartet auf die Enthüllung des Geheimnisses und
// löst damit auf der Taker-Give-Chain ein.
func (o *orchestrator) runMaker(ctx context.Context, ss *swapSession, s *Server) {
	// 1. Warten auf den Taker-Lock (auf der Taker-Give-Chain).
	if !o.waitForTakerLock(ctx, ss, s) {
		s.setSwapPhase(ss.swapID, SwapExpired, "Taker hat nicht gesperrt")
		return
	}
	// 2. Auf eigener Give-Chain sperren (als Zweiter).
	if !o.lockGive(ctx, ss, s) {
		return
	}
	// 3. Warten, bis der Taker eingelöst hat → S enthüllt.
	secret, ok := o.waitForReveal(ctx, ss, s)
	if !ok {
		s.setSwapPhase(ss.swapID, SwapFndLocked, "Taker hat nicht eingelöst — Refund eingeplant")
		o.scheduleRefund(ss)
		return
	}
	// 4. Auf der Taker-Give-Chain einlösen mit dem enthüllten Geheimnis.
	o.claimTakeWithSecret(ctx, ss, s, secret)
}

// ── Richtungsabhängige Bausteine ─────────────────────────────────────────────

// lockGive sperrt den Betrag auf der Give-Chain dieser Seite.
func (o *orchestrator) lockGive(ctx context.Context, ss *swapSession, s *Server) bool {
	if ss.giveChain == "sol" {
		client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
		if err != nil {
			s.setSwapPhase(ss.swapID, SwapExpired, "SOL-Client-Fehler: "+err.Error())
			return false
		}
		redeemer, err := solana.PublicKeyFromBase58(ss.counterpartySol)
		if err != nil {
			s.setSwapPhase(ss.swapID, SwapExpired, "Gegenseiten-SOL-Adresse ungültig")
			return false
		}
		lamports := uint64(ss.amountSOL * 1_000_000_000)
		sig, err := client.Initiate(ctx, ss.solKey, lamports, ss.solTimelock(), redeemer, ss.secretHash)
		if err != nil {
			s.setSwapPhase(ss.swapID, SwapExpired, "SOL-Lock fehlgeschlagen ("+solNet(s)+"): "+err.Error())
			return false
		}
		// Absenden genügt nicht: prüfen, ob das Swap-Konto wirklich auf der
		// Chain angekommen ist (sonst wartet die Gegenseite ewig).
		if pda, _, perr := client.deriveSwapPDA(ss.solKey.PublicKey(), ss.secretHash); perr == nil {
			deadline := time.Now().Add(90 * time.Second)
			for {
				ok, cerr := o.solAccountCheck(ctx, s, pda.String())
				if ok {
					break
				}
				if time.Now().After(deadline) || ctx.Err() != nil {
					note := "SOL-Lock gesendet (" + solNet(s) + ", Signatur " + truncate(sig, 16) + "), aber nach 90 s nicht auf der Chain – im Explorer prüfen"
					if cerr != nil {
						note += "; RPC-Fehler: " + truncate(cerr.Error(), 100)
					}
					s.setSwapPhase(ss.swapID, SwapExpired, note)
					return false
				}
				time.Sleep(3 * time.Second)
			}
		}
		s.setSwapSolLock(ss.swapID, sig)
		s.setSwapPhase(ss.swapID, ss.currentPhase(s), "SOL gesperrt ("+solNet(s)+"), Signatur "+truncate(sig, 16)+"… – warte auf FND-Lock der Gegenseite")
		return true
	}
	// giveChain == "fnd"
	txHash, _, _, err := s.submitFndLock(ss.fndSeed, ss.counterpartyFnd, ss.amountFND, ss.secretHash, ss.fndTimelock())
	if err != nil {
		s.setSwapPhase(ss.swapID, SwapExpired, "FND-Lock fehlgeschlagen: "+err.Error())
		return false
	}
	s.setSwapFndLock(ss.swapID, txHash)
	ss.fndHTLCID = txHash
	s.setSwapPhase(ss.swapID, ss.currentPhase(s), "FND-Sperre eingereicht (Tx "+truncate(txHash, 12)+
		") – wird mit dem nächsten Block wirksam (Chain-Höhe "+strconv.FormatUint(s.chain.Height(), 10)+")")
	return true
}

// waitForMakerLock (Taker-Sicht): wartet auf den Lock des Makers auf DESSEN
// Give-Chain (die Gegen-Chain zu unserer Give-Chain). Gibt die Lock-ID/Signatur.
func (o *orchestrator) waitForMakerLock(ctx context.Context, ss *swapSession, s *Server) (string, bool) {
	if ss.giveChain == "sol" {
		// Maker gibt FND → wir warten auf FND-Lock an unsere FND-Adresse.
		return o.waitForFndLock(ctx, ss, s)
	}
	// Maker gibt SOL → wir warten auf SOL-Lock.
	if o.waitForSolLock(ctx, ss, s) {
		return "sol", true
	}
	return "", false
}

// waitForTakerLock (Maker-Sicht): wartet auf den Lock des Takers.
func (o *orchestrator) waitForTakerLock(ctx context.Context, ss *swapSession, s *Server) bool {
	if ss.giveChain == "sol" {
		// Wir geben SOL, Taker gibt FND → warten auf FND-Lock.
		_, ok := o.waitForFndLock(ctx, ss, s)
		return ok
	}
	// Wir geben FND, Taker gibt SOL → warten auf SOL-Lock.
	return o.waitForSolLock(ctx, ss, s)
}

// claimTake (Taker): löst auf der Maker-Give-Chain ein. otherLockID ist bei FND
// die HTLC-ID.
func (o *orchestrator) claimTake(ctx context.Context, ss *swapSession, s *Server, otherLockID string) bool {
	if ss.giveChain == "sol" {
		// Maker gab FND → wir lösen FND ein (enthüllt S auf der FND-Chain).
		if err := s.orchestratorFndClaim(ss.fndSeed, otherLockID, ss.secret); err != nil {
			s.setSwapPhase(ss.swapID, SwapFndLocked, "FND-Claim fehlgeschlagen: "+err.Error())
			return false
		}
		s.setSwapPhase(ss.swapID, SwapFndClaimed, "")
		return true
	}
	// Maker gab SOL → wir lösen SOL ein (enthüllt S auf Solana).
	client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
	if err != nil {
		return false
	}
	makerSol, err := solana.PublicKeyFromBase58(ss.counterpartySol)
	if err != nil {
		return false
	}
	if _, err := client.Redeem(ctx, ss.solKey, makerSol, ss.secretHash, ss.secret); err != nil {
		s.setSwapPhase(ss.swapID, SwapSolLocked, "SOL-Claim fehlgeschlagen: "+err.Error())
		return false
	}
	s.setSwapPhase(ss.swapID, SwapFndClaimed, "") // Phase generisch: "erste Einlösung erfolgt"
	return true
}

// waitForReveal (Maker): wartet, bis der Taker eingelöst und S enthüllt hat.
func (o *orchestrator) waitForReveal(ctx context.Context, ss *swapSession, s *Server) ([32]byte, bool) {
	var empty [32]byte
	if ss.giveChain == "sol" {
		// Maker gibt SOL, Taker gibt FND. Der Maker hat SOL gesperrt (er ist
		// initiator der PDA). Der Taker löst diese SOL ein und enthüllt S dabei
		// auf Solana. Wir lesen S aus der redeem-Transaktion der PDA.
		client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
		if err != nil {
			return empty, false
		}
		myKey := ss.solKey.PublicKey() // Maker ist initiator
		tick := time.NewTicker(orchestratorPollInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return empty, false
			case <-tick.C:
				if secret, ok := client.ReadSecretFromClaim(ctx, s.swapMgr, myKey, ss.secretHash); ok {
					return secret, true
				}
			}
		}
	}
	// Maker gibt FND, Taker gibt SOL: Der Taker löst UNSEREN FND-Lock ein und
	// enthüllt S auf der FND-Chain. Wir lesen es aus unserem FND-HTLC.
	return o.waitForSecretReveal(ctx, ss, s, ss.fndHTLCID)
}

// claimTakeWithSecret (Maker): löst auf der Taker-Give-Chain mit S ein.
func (o *orchestrator) claimTakeWithSecret(ctx context.Context, ss *swapSession, s *Server, secret [32]byte) {
	if ss.giveChain == "sol" {
		// Taker gab FND → wir lösen FND ein.
		id, ok := s.findFndHTLCByHashlockForMe(ss.secretHash, ss.fndSeed)
		if !ok {
			s.setSwapPhase(ss.swapID, SwapFndClaimed, "FND-HTLC des Takers nicht gefunden")
			return
		}
		if err := s.orchestratorFndClaim(ss.fndSeed, id, secret); err != nil {
			s.setSwapPhase(ss.swapID, SwapFndClaimed, "FND-Claim fehlgeschlagen: "+err.Error())
			return
		}
		s.setSwapPhase(ss.swapID, SwapSolClaimed, "")
		return
	}
	// Taker gab SOL → wir lösen SOL ein – mit Wiederholung in eigener Goroutine
	// und EIGENER Schlüsselkopie (das Aufräumen nach dem Swap nullt die
	// hinterlegten Schlüssel; eine leere Solana-Wallet für die Gebühr ließ die
	// Abholung früher nach einem einzigen Versuch endgültig scheitern).
	takerSol, err := solana.PublicKeyFromBase58(ss.counterpartySol)
	if err != nil {
		s.setSwapPhase(ss.swapID, SwapFndClaimed, "SOL-Abholung: Käufer-Adresse ungültig")
		return
	}
	keyCopy := append(solana.PrivateKey(nil), ss.solKey...)
	go s.redeemSolWithRetry(ss.swapID, keyCopy, takerSol, ss.secretHash, secret, time.Now().Add(solClaimWindow))
	// Phase "SOL abgeholt" setzt redeemSolWithRetry bei Erfolg.
}

// scheduleRefund wählt den richtigen Refund je nach Give-Chain.
func (o *orchestrator) scheduleRefund(ss *swapSession) {
	if ss.giveChain == "sol" {
		o.scheduleSolRefund(ss)
	} else {
		o.scheduleFndRefund(ss)
	}
}

// ── Poll-Helfer (prüfen den Chain-Zustand) ───────────────────────────────────

// waitForSolLock pollt, bis der SOL-HTLC des Käufers auf Solana existiert.
func (o *orchestrator) waitForSolLock(ctx context.Context, ss *swapSession, s *Server) bool {
	buyerSol, err := solana.PublicKeyFromBase58(ss.counterpartySol)
	if err != nil {
		s.setSwapPhase(ss.swapID, ss.currentPhase(s), "Gegenseiten-SOL-Adresse ungültig: "+ss.counterpartySol)
		return false
	}
	client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
	if err != nil {
		return false
	}
	pda, _, err := client.deriveSwapPDA(buyerSol, ss.secretHash)
	if err != nil {
		return false
	}
	// Diagnose: welche PDA wird auf welchem Netz beobachtet.
	base := "warte auf SOL-Lock (" + solNet(s) + ") an PDA " + pda.String()[:8] + "… (Initiator " + ss.counterpartySol[:8] + "…)"
	s.setSwapPhase(ss.swapID, ss.currentPhase(s), base)
	tick := time.NewTicker(orchestratorPollInterval)
	defer tick.Stop()
	lastErr := ""
	for {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
			ok, err := o.solAccountCheck(ctx, s, pda.String())
			if ok {
				return true
			}
			// RPC-Fehler sichtbar machen statt stumm weiterzuwarten.
			e := ""
			if err != nil {
				e = err.Error()
			}
			if e != lastErr {
				lastErr = e
				if e != "" {
					s.setSwapPhase(ss.swapID, ss.currentPhase(s), base+" – "+solRPCErr(err, s.swapMgr.solRPC).Error())
				} else {
					s.setSwapPhase(ss.swapID, ss.currentPhase(s), base)
				}
			}
		}
	}
}

// waitForFndLock pollt, bis der Verkäufer einen FND-HTLC mit unserem Hashlock
// angelegt hat. Gibt die HTLC-ID zurück.
func (o *orchestrator) waitForFndLock(ctx context.Context, ss *swapSession, s *Server) (string, bool) {
	// Nach der eigenen FND-Empfangsadresse suchen (die Gegenseite hat an sie
	// gesperrt). Bevorzugt die Order-Adresse (ownFnd); wer einen FND-Seed hat,
	// leitet sie sonst daraus ab.
	myFndAddr := ss.ownFnd
	if myFndAddr == "" {
		var ok bool
		myFndAddr, ok = s.fndAddressFromSeed(ss.fndSeed)
		if !ok {
			s.setSwapPhase(ss.swapID, ss.currentPhase(s), "keine eigene FND-Empfangsadresse verfügbar")
			return "", false
		}
	}
	tick := time.NewTicker(orchestratorPollInterval)
	defer tick.Stop()
	lastH, since, warned := s.chain.Height(), time.Now(), false
	s.setSwapPhase(ss.swapID, ss.currentPhase(s), "warte auf FND-Sperre der Gegenseite (Chain-Höhe "+strconv.FormatUint(lastH, 10)+")")
	for {
		select {
		case <-ctx.Done():
			return "", false
		case <-tick.C:
			if id, ok := s.findFndHTLCByHashlock(ss.secretHash, myFndAddr); ok {
				return id, true
			}
			// Wächst die Chain nicht, kann die Sperre nie ankommen – sagen, warum.
			if h := s.chain.Height(); h != lastH {
				lastH, since, warned = h, time.Now(), false
			} else if !warned && time.Since(since) > 90*time.Second {
				warned = true
				s.setSwapPhase(ss.swapID, ss.currentPhase(s), "warte auf FND-Sperre – die Fundus-Chain wächst nicht (Höhe "+
					strconv.FormatUint(h, 10)+" seit über 90 s). Validatoren prüfen: /api/v1/chain/status (i_am_validator, hint)")
			}
		}
	}
}

// waitForSecretReveal pollt, bis das Geheimnis auf der FND-Chain enthüllt wurde
// (durch den Claim des Käufers).
func (o *orchestrator) waitForSecretReveal(ctx context.Context, ss *swapSession, s *Server, fndHTLCID string) ([32]byte, bool) {
	var empty [32]byte
	tick := time.NewTicker(orchestratorPollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return empty, false
		case <-tick.C:
			if secret, ok := s.findRevealedSecret(fndHTLCID); ok {
				return secret, true
			}
		}
	}
}

// solAccountExists prüft per RPC, ob ein Konto (der PDA) existiert.
func (o *orchestrator) solAccountExists(ctx context.Context, s *Server, addr string) bool {
	ok, _ := o.solAccountCheck(ctx, s, addr)
	return ok
}

// solAccountCheck wie solAccountExists, aber mit Fehler (für die Statusanzeige).
func (o *orchestrator) solAccountCheck(ctx context.Context, s *Server, addr string) (bool, error) {
	res, err := s.swapMgr.solanaRPCCall(ctx, "getAccountInfo", []interface{}{
		addr, map[string]interface{}{"encoding": "base64", "commitment": "confirmed"},
	})
	if err != nil {
		return false, err
	}
	if strings.Contains(string(res), "\"error\"") {
		return false, fmt.Errorf("RPC: %s", truncate(string(res), 160))
	}
	// Wenn value != null, existiert das Konto.
	return !strings.Contains(string(res), "\"value\":null"), nil
}

// solNet: lesbarer Netzname für Statusmeldungen (Abweichungen zwischen den
// Nodes fallen so sofort auf).
func solNet(s *Server) string {
	if c := s.solCluster(); c != "" {
		return c
	}
	return "mainnet"
}

func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return v[:n] + "…"
}

// ── Refund-Watcher ───────────────────────────────────────────────────────────
//
// Wenn ein Swap steckenbleibt (Gegenseite reagiert nicht), muss das gesperrte
// Geld zurück. Der Refund geht erst NACH Ablauf des Timelocks durch (das
// Programm/die Chain verweigert ihn vorher). Der Watcher versucht es periodisch,
// bis er durchgeht — und hält die Keys so lange.

const (
	refundRetryInterval = 30 * time.Second
	refundMaxWait       = 50 * time.Hour // deckt den längsten Timelock (~48h) ab
)

// scheduleSolRefund versucht periodisch, den SOL-Lock des Käufers zurückzuholen.
func (o *orchestrator) scheduleSolRefund(ss *swapSession) {
	ss.refundPending = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), refundMaxWait)
		defer cancel()
		// Keys nach Abschluss nullen.
		defer ss.wipe()
		client, err := newSolHTLCClient(o.server.swapMgr.solRPC, o.server.swapMgr.htlcProgramID)
		if err != nil {
			return
		}
		tick := time.NewTicker(refundRetryInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				o.server.setSwapPhase(ss.swapID, SwapExpired, "SOL-Refund-Frist abgelaufen")
				return
			case <-tick.C:
				sig, err := client.Refund(ctx, ss.solKey, ss.secretHash)
				if err == nil {
					_ = sig
					o.server.setSwapPhase(ss.swapID, SwapRefunded, "SOL zurückgeholt")
					return
				}
				// Fehler (meist "RefundBeforeExpiry") → weiter warten.
			}
		}
	}()
}

// scheduleFndRefund versucht periodisch, den FND-Lock des Verkäufers zurückzuholen.
func (o *orchestrator) scheduleFndRefund(ss *swapSession) {
	ss.refundPending = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), refundMaxWait)
		defer cancel()
		defer ss.wipe()
		tick := time.NewTicker(refundRetryInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				o.server.setSwapPhase(ss.swapID, SwapExpired, "FND-Refund-Frist abgelaufen")
				return
			case <-tick.C:
				if ss.fndHTLCID == "" {
					return
				}
				id, err := hash32FromHex(ss.fndHTLCID)
				if err != nil {
					return
				}
				payload := chain.EncodeHTLCRefund(id)
				_, _, _, err = o.server.submitChainTx(ss.fndSeed, chain.TxHTLCRefund, chain.FeeForValue(nil), payload)
				if err == nil {
					o.server.setSwapPhase(ss.swapID, SwapRefunded, "FND zurückgeholt")
					return
				}
				// Fehler (meist "vor Timelock") → weiter warten.
			}
		}
	}()
}


// currentPhase liest die aktuelle Phase eines Swaps (um beim Notiz-Setzen die
// Phase nicht zu ändern).
func (ss *swapSession) currentPhase(s *Server) SwapPhase {
	s.swapMgr.mu.RLock()
	defer s.swapMgr.mu.RUnlock()
	if sw, ok := s.swapMgr.swaps[ss.swapID]; ok {
		return sw.Phase
	}
	return SwapInitiated
}

// ── SOL-Abholung mit Wiederholung + Wiederaufnahme nach Neustart ──────────────

// solClaimWindow: so lange wird die Abholung versucht (die SOL-Sperre des
// Käufers als Erst-Sperrer läuft ~48 h; danach kann er zurückholen).
const solClaimWindow = 46 * time.Hour

var (
	solClaimMu      sync.Mutex
	solClaimRunning = map[string]bool{} // Swap-ID → Abholung läuft bereits
)

// friendlySolClaimErr übersetzt häufige Abholfehler.
func friendlySolClaimErr(err error, payer solana.PublicKey) string {
	e := err.Error()
	switch {
	case strings.Contains(e, "no record of a prior credit") || strings.Contains(e, "insufficient funds for fee") ||
		strings.Contains(e, "InsufficientFundsForFee"):
		return "deine Solana-Wallet " + payer.String() + " hat kein SOL für die Gebühr – bitte mind. 0,01 SOL dorthin senden (Menü → Solana-Wallet)"
	case strings.Contains(e, "429") || strings.Contains(strings.ToLower(e), "too many"):
		return "Solana-RPC drosselt (zu viele Anfragen)"
	}
	return truncate(e, 160)
}

// redeemSolWithRetry holt die SOL mit dem Geheimnis ab und versucht es bei
// Fehlern jede Minute erneut, bis es klappt, das Konto nicht mehr existiert
// (bereits abgeholt/zurückgeholt) oder die Frist abläuft.
func (s *Server) redeemSolWithRetry(swapID string, key solana.PrivateKey, takerSol solana.PublicKey,
	hash, secret [32]byte, deadline time.Time) {
	solClaimMu.Lock()
	if solClaimRunning[swapID] {
		solClaimMu.Unlock()
		return
	}
	solClaimRunning[swapID] = true
	solClaimMu.Unlock()
	defer func() {
		solClaimMu.Lock()
		delete(solClaimRunning, swapID)
		solClaimMu.Unlock()
		for i := range key {
			key[i] = 0
		}
	}()
	client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
	if err != nil {
		s.setSwapPhase(swapID, SwapFndClaimed, "SOL-Abholung: "+err.Error())
		return
	}
	pda, _, perr := client.deriveSwapPDA(takerSol, hash)
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		if perr == nil {
			// Swap-Konto weg → bereits abgeholt oder vom Käufer zurückgeholt.
			if ok, cerr := (&orchestrator{}).solAccountCheck(ctx, s, pda.String()); cerr == nil && !ok {
				cancel()
				s.setSwapPhase(swapID, SwapFndClaimed, "SOL-Swap-Konto existiert nicht mehr (bereits abgeholt oder vom Käufer zurückgeholt)")
				return
			}
		}
		_, rerr := client.Redeem(ctx, key, takerSol, hash, secret)
		cancel()
		if rerr == nil {
			s.setSwapPhase(swapID, SwapSolClaimed, "SOL abgeholt")
			if s.log != nil {
				s.log.Info("SOL abgeholt", zap.String("swap", swapID), zap.Int("versuch", attempt))
			}
			return
		}
		if time.Now().After(deadline) {
			s.setSwapPhase(swapID, SwapFndClaimed, "SOL-Abholung aufgegeben (Frist abgelaufen): "+friendlySolClaimErr(rerr, key.PublicKey()))
			return
		}
		s.setSwapPhase(swapID, SwapFndClaimed, fmt.Sprintf("SOL-Abholung fehlgeschlagen (Versuch %d): %s – nächster Versuch in 1 min",
			attempt, friendlySolClaimErr(rerr, key.PublicKey())))
		time.Sleep(time.Minute)
	}
}

// resumeSolClaimsLoop: nimmt nach einem Neustart Verkäufer-Swaps wieder auf,
// deren FND bereits eingelöst sind (Geheimnis öffentlich), deren SOL aber noch
// nicht abgeholt wurden. Schlüssel: hinterlegte Order-Schlüssel oder – falls
// schon freigegeben – die angemeldete Sitzung, deren Solana-Adresse passt.
func (s *Server) resumeSolClaimsLoop() {
	time.Sleep(45 * time.Second) // Chain-Sync abwarten
	for {
		s.resumeSolClaimsOnce()
		time.Sleep(2 * time.Minute)
	}
}

func (s *Server) resumeSolClaimsOnce() {
	if s.swapMgr == nil || s.chain == nil || s.swapMgr.htlcProgramID == "" {
		return
	}
	type pending struct {
		id, orderID, buyerSol, sellerSol, hashlock, fndHTLC string
	}
	var list []pending
	s.swapMgr.mu.RLock()
	for _, sw := range s.swapMgr.swaps {
		if sw == nil || !strings.HasPrefix(sw.ID, "seller-") || sw.Phase != SwapFndClaimed ||
			sw.BuyerSol == "" || sw.Hashlock == "" || sw.FndHTLCID == "" {
			continue
		}
		if !strings.Contains(sw.Note, "SOL-Claim fehlgeschlagen") && !strings.Contains(sw.Note, "SOL-Abholung fehlgeschlagen") {
			continue
		}
		list = append(list, pending{sw.ID, sw.OrderID, sw.BuyerSol, sw.SellerSol, sw.Hashlock, sw.FndHTLCID})
	}
	s.swapMgr.mu.RUnlock()
	for _, p := range list {
		solClaimMu.Lock()
		running := solClaimRunning[p.id]
		solClaimMu.Unlock()
		if running {
			continue
		}
		secret, ok := s.findRevealedSecret(p.fndHTLC)
		if !ok {
			continue
		}
		hash, err := hash32FromHex(p.hashlock)
		if err != nil {
			continue
		}
		takerSol, err := solana.PublicKeyFromBase58(p.buyerSol)
		if err != nil {
			continue
		}
		key := s.findSolKeyFor(p.orderID, p.sellerSol)
		if key == nil {
			s.setSwapPhase(p.id, SwapFndClaimed, "SOL-Abholung wartet: auf diesem Node anmelden (Wallet hinterlegt), dann wird automatisch abgeholt")
			continue
		}
		go s.redeemSolWithRetry(p.id, key, takerSol, hash, secret, time.Now().Add(solClaimWindow))
	}
}

// findSolKeyFor: Solana-Schlüssel des Verkäufers – aus den hinterlegten Order-
// Schlüsseln oder aus einer angemeldeten Sitzung mit passender Adresse (nur
// Sitzungen mit bereits vorhandenem Wallet-Schlüssel, keine Argon2-Ableitung).
func (s *Server) findSolKeyFor(orderID, sellerSol string) solana.PrivateKey {
	if s.swapCoord != nil {
		s.swapCoord.mu.Lock()
		if d, ok := s.swapCoord.deposits[orderID]; ok && len(d.solKey) == 64 {
			k := append(solana.PrivateKey(nil), d.solKey...)
			s.swapCoord.mu.Unlock()
			return k
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
			continue
		}
		ck, err := sess.identity.ChainPrivateKey()
		if err != nil {
			continue
		}
		k := solKeyFromChain(ck)
		ck.D.SetInt64(0)
		if sellerSol == "" || k.PublicKey().String() == sellerSol {
			return k
		}
	}
	return nil
}
