package api

// Node-zu-Node-Swap-Koordination.
//
// Ablauf im echten Betrieb:
//  1. Der Verkäufer stellt eine Verkaufs-Order ein und HINTERLEGT dabei seine
//     Schlüssel (Solana + FND-Seed). Der Node hält sie im RAM, gebunden an die
//     Order-ID, bis die Order erfüllt oder gecancelt wird.
//  2. Der Käufer klickt auf die Order → sein Node erzeugt das Geheimnis und
//     kontaktiert den Verkäufer-Node per P2P (SwapInitProtocol) mit dem
//     Hashlock und seinen Adressen.
//  3. Der Verkäufer-Node startet daraufhin seine Orchestrator-Seite (seller)
//     mit den hinterlegten Schlüsseln. Der Käufer-Node startet die buyer-Seite.
//  4. Der Orchestrator fährt den atomaren Swap wie gehabt.

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/gagliardetto/solana-go"
	"go.uber.org/zap"
	"lukechampine.com/blake3"
)

// SwapInitProtocol: Käufer → Verkäufer, "starte deine Seite für diesen Swap".
const SwapInitProtocol = "/fundus/swap-init/1.0.0"

// depositedKeys hält die vom Verkäufer hinterlegten Schlüssel je Order.
type depositedKeys struct {
	solKey  solana.PrivateKey
	fndSeed []string
	solAddr string
	fndAddr string
	amountSOL float64
	amountFND float64
}

func (d *depositedKeys) wipe() {
	for i := range d.solKey {
		d.solKey[i] = 0
	}
	d.fndSeed = nil
}

// swapCoordinator verwaltet hinterlegte Verkäufer-Keys und das P2P-Protokoll.
type swapCoordinator struct {
	mu            sync.Mutex
	deposits      map[string]*depositedKeys // orderID → Keys
	activeMatches map[string]bool           // orderID → Auto-Swap läuft
	activeSince   map[string]time.Time      // orderID → Start des Auto-Swaps (Wächter)
	backoff       map[string]time.Time      // "meine|fremde" → nicht vor diesem Zeitpunkt erneut
	server        *Server
}

func newSwapCoordinator(s *Server) *swapCoordinator {
	return &swapCoordinator{
		deposits:      make(map[string]*depositedKeys),
		activeMatches: make(map[string]bool),
		activeSince:   make(map[string]time.Time),
		backoff:       make(map[string]time.Time),
		server:        s,
	}
}

// registerProtocol hängt den Swap-Init-Handler an den p2pNode.
func (sc *swapCoordinator) registerProtocol(node p2pNode) {
	node.RegisterProtocol(SwapInitProtocol, func(peerID string, data []byte) []byte {
		return sc.handleSwapInit(peerID, data)
	})
}

// depositKeys legt die Verkäufer-Schlüssel für eine Order ab.
func (sc *swapCoordinator) depositKeys(orderID string, k *depositedKeys) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.deposits[orderID] = k
	sc.saveDepositsLocked() // übersteht Neustarts (verschlüsselt)
}

// releaseKeys entfernt und nullt die Schlüssel einer Order (Cancel/Erfüllung).
func (sc *swapCoordinator) releaseKeys(orderID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if k, ok := sc.deposits[orderID]; ok {
		k.wipe()
		delete(sc.deposits, orderID)
		sc.saveDepositsLocked()
	}
}

// swapInitMsg ist die P2P-Nachricht Taker → Maker.
type swapInitMsg struct {
	OrderID       string  `json:"order_id"`
	Hashlock      string  `json:"hashlock"`    // hex
	BuyerSol      string  `json:"buyer_sol"`   // Taker-Empfangsadresse SOL (leer wenn Taker SOL gibt)
	BuyerFnd      string  `json:"buyer_fnd"`   // Taker-Empfangsadresse FND (leer wenn Taker FND gibt)
	AmountSOL     float64 `json:"amount_sol"`
	AmountFND     float64 `json:"amount_fnd"`
	TakerGivesSol bool    `json:"taker_gives_sol"` // true = Taker gibt SOL, Maker gibt FND
}

// handleSwapInit läuft auf dem VERKÄUFER-Node: startet dessen Orchestrator-Seite.
func (sc *swapCoordinator) handleSwapInit(peerID string, data []byte) []byte {
	var msg swapInitMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return []byte(`{"ok":false,"error":"ungültige Nachricht"}`)
	}
	sc.mu.Lock()
	k, ok := sc.deposits[msg.OrderID]
	sc.mu.Unlock()
	if !ok {
		return []byte(`{"ok":false,"error":"keine hinterlegten Schlüssel für diese Order"}`)
	}
	hash, err := hash32FromHex(msg.Hashlock)
	if err != nil {
		return []byte(`{"ok":false,"error":"ungültiger Hashlock"}`)
	}

	// Verkäufer-Seite starten. Der Maker gibt das Gegenteil dessen, was der
	// Taker gibt: Taker gibt SOL → Maker gibt FND, und umgekehrt.
	makerGiveChain := "fnd"
	if !msg.TakerGivesSol {
		makerGiveChain = "sol"
	}
	ss := &swapSession{
		swapID:          "seller-" + msg.OrderID,
		orderID:         msg.OrderID,
		isTaker:         false,
		giveChain:       makerGiveChain,
		solKey:          k.solKey,
		fndSeed:         k.fndSeed,
		secretHash:      hash,
		counterpartySol: msg.BuyerSol,
		counterpartyFnd: msg.BuyerFnd,
		amountSOL:       msg.AmountSOL,
		amountFND:       msg.AmountFND,
	}
	// Eigene Empfangsadressen aus der eigenen Order (der Maker empfängt auf der
	// Kette, die er NICHT weggibt, und braucht dort die Adresse zum Lock-Finden).
	if s := sc.server; s.orderBook != nil {
		if ord, ok := s.orderBook.FindOrder(msg.OrderID); ok {
			ss.ownSol = ord.SolAddress
			ss.ownFnd = ord.FndAddress
		}
	}
	// Swap-Objekt zur Statusanzeige anlegen.
	s := sc.server
	s.swapMgr.mu.Lock()
	s.swapMgr.swaps[ss.swapID] = &Swap{
		ID: ss.swapID, OrderID: msg.OrderID, Phase: SwapInitiated,
		Hashlock: msg.Hashlock, AmountFND: msg.AmountFND, AmountSOL: msg.AmountSOL,
		CreatedAt: time.Now().Unix(),
	}
	s.swapMgr.mu.Unlock()
	s.orch.startSwap(ss)

	return []byte(`{"ok":true,"swap_id":"` + ss.swapID + `"}`)
}

// triggerRemoteSwap läuft auf dem KÄUFER-Node: benachrichtigt den Verkäufer und
// startet die eigene (buyer) Seite.
func (sc *swapCoordinator) triggerRemoteSwap(ctx context.Context, node p2pNode,
	sellerPeerID string, msg swapInitMsg, buyerSolKey solana.PrivateKey,
	buyerFndSeed []string, secret [32]byte, sellerSol, sellerFnd string, takerGivesSol bool, takerOwnOrderID string) error {

	// 1. Maker benachrichtigen und dessen Antwort prüfen.
	payload, _ := json.Marshal(msg)
	resp, err := node.SendAndReceive(ctx, sellerPeerID, SwapInitProtocol, payload)
	if err != nil {
		return err
	}
	// Wenn der Maker die Anfrage ablehnt (z.B. keine Keys hinterlegt), abbrechen —
	// sonst sperrt der Taker sinnlos und muss auf den Refund warten.
	if bytes.Contains(resp, []byte(`"ok":false`)) {
		var e struct{ Error string `json:"error"` }
		_ = json.Unmarshal(resp, &e)
		if e.Error == "" {
			e.Error = "Maker hat die Swap-Anfrage abgelehnt"
		}
		return &swapErr{"Maker: " + e.Error}
	}

	// 2. Eigene (Taker) Seite starten. Give-Chain = was wir weggeben.
	hash, err := hash32FromHex(msg.Hashlock)
	if err != nil {
		return err
	}
	takerGiveChain := "sol"
	if !takerGivesSol {
		takerGiveChain = "fnd"
	}
	ss := &swapSession{
		swapID:          "buyer-" + msg.OrderID,
		orderID:         msg.OrderID,
		isTaker:         true,
		giveChain:       takerGiveChain,
		solKey:          buyerSolKey,
		fndSeed:         buyerFndSeed,
		secret:          secret,
		secretHash:      hash,
		counterpartySol: sellerSol,
		counterpartyFnd: sellerFnd,
		amountSOL:       msg.AmountSOL,
		amountFND:       msg.AmountFND,
		ownSol:          msg.BuyerSol, // eigene Empfangsadressen des Takers
		ownFnd:          msg.BuyerFnd,
		takerOwnOrderID: takerOwnOrderID,
	}
	s := sc.server
	s.swapMgr.mu.Lock()
	s.swapMgr.swaps[ss.swapID] = &Swap{
		ID: ss.swapID, OrderID: msg.OrderID, Phase: SwapInitiated,
		Hashlock: msg.Hashlock, AmountFND: msg.AmountFND, AmountSOL: msg.AmountSOL,
		CreatedAt: time.Now().Unix(),
	}
	s.swapMgr.mu.Unlock()
	s.orch.startSwap(ss)
	return nil
}

// ── Automatisches Matching ───────────────────────────────────────────────────
//
// Der Node prüft periodisch, ob eine seiner eigenen offenen Orders (für die
// Keys hinterlegt sind) zu einer fremden Order passt. Bei einem Treffer löst er
// den Swap automatisch aus — ohne Klick.
//
// Doppel-Auslösung vermeiden: Nur die NEUERE Order löst aus (sie ist der Taker,
// der die ältere Order annimmt). Bei Gleichstand entscheidet die kleinere ID.
// So startet garantiert nur eine Seite den Swap.

const matchInterval = 6 * time.Second

// Serieller Auto-Matcher (Neuaufbau nach R257): Früher startete jeder Takt
// beliebig viele Swaps parallel, wiederholte gescheiterte Paare alle 6 s und
// hielt Sperren hängender Swaps für immer – das hat Nodes aufgehängt. Jetzt:
//   - höchstens EIN automatischer Swap gleichzeitig,
//   - Wächter gibt einen Swap nach autoSwapTimeout frei,
//   - gescheiterte Paare werden autoSwapBackoff lang nicht erneut versucht,
//   - ein Panic im Matcher reißt den Node nicht mit.
const (
	autoSwapTimeout = 15 * time.Minute
	autoSwapBackoff = 5 * time.Minute
)

// runMatcher startet die periodische Matching-Schleife.
func (sc *swapCoordinator) runMatcher() {
	go func() {
		tick := time.NewTicker(matchInterval)
		defer tick.Stop()
		for range tick.C {
			sc.safeMatchOnce()
		}
	}()
}

func (sc *swapCoordinator) safeMatchOnce() {
	defer func() {
		if r := recover(); r != nil && sc.server.log != nil {
			sc.server.log.Error("Auto-Match: Panic abgefangen", zap.Any("panic", r))
		}
	}()
	sc.matchOnce()
}

// autoBusy prüft (und bereinigt per Wächter), ob gerade ein Auto-Swap läuft.
func (sc *swapCoordinator) autoBusy() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	now := time.Now()
	for id := range sc.activeMatches {
		since, ok := sc.activeSince[id]
		if !ok {
			sc.activeSince[id] = now
			continue
		}
		if now.Sub(since) > autoSwapTimeout {
			delete(sc.activeMatches, id)
			delete(sc.activeSince, id)
			if sc.server.log != nil {
				sc.server.log.Warn("Auto-Match: Swap ohne Abschluss – Sperre durch Wächter freigegeben",
					zap.String("order", id))
			}
		}
	}
	for k, until := range sc.backoff {
		if now.After(until) {
			delete(sc.backoff, k)
		}
	}
	return len(sc.activeMatches) > 0
}

// matchOnce sucht EIN passendes Paar und löst genau einen Swap aus.
func (sc *swapCoordinator) matchOnce() {
	s := sc.server
	if s.orderBook == nil || sc.autoBusy() {
		return
	}
	mine, others := s.orderBook.SnapshotOrders()
	if len(mine) == 0 || len(others) == 0 {
		return
	}
	for _, my := range mine {
		// Nur Orders, für die wir Keys hinterlegt haben (sonst kein Auto-Swap).
		sc.mu.Lock()
		_, hasKeys := sc.deposits[my.ID]
		sc.mu.Unlock()
		if !hasKeys || !my.isActive() {
			continue
		}
		for _, other := range others {
			if !other.isActive() || !ordersMatch(my, other) || !weAreTaker(my, other) {
				continue
			}
			pair := my.ID + "|" + other.ID
			sc.mu.Lock()
			_, blocked := sc.backoff[pair]
			if !blocked {
				sc.activeMatches[my.ID] = true
				sc.activeSince[my.ID] = time.Now()
			}
			sc.mu.Unlock()
			if blocked {
				continue
			}
			if s.log != nil {
				s.log.Info("Auto-Match: löse Swap aus",
					zap.String("myOrder", my.ID[:8]), zap.String("otherOrder", other.ID[:8]),
					zap.Float64("myPrice", my.PriceSOL), zap.Float64("otherPrice", other.PriceSOL))
			}
			go func(my, other *Order, pair string) {
				defer func() {
					if r := recover(); r != nil {
						sc.failAuto(my.ID, pair)
						if s.log != nil {
							s.log.Error("Auto-Match: Panic im Swap abgefangen", zap.Any("panic", r))
						}
					}
				}()
				if !sc.autoTriggerSwap(my, other) {
					sc.failAuto(my.ID, pair)
				}
			}(my, other, pair)
			return // seriell: genau ein Swap pro Takt und gleichzeitig
		}
	}
}

// failAuto gibt die Sperre frei und sperrt das Paar für autoSwapBackoff.
func (sc *swapCoordinator) failAuto(orderID, pair string) {
	sc.mu.Lock()
	delete(sc.activeMatches, orderID)
	delete(sc.activeSince, orderID)
	sc.backoff[pair] = time.Now().Add(autoSwapBackoff)
	sc.mu.Unlock()
}

// ordersMatch prüft, ob zwei Orders zueinander passen (Gegenrichtung + Preis).
func ordersMatch(a, b *Order) bool {
	// Entgegengesetzte Richtung: einer will kaufen, der andere verkaufen.
	if a.Side == b.Side {
		return false
	}
	// Preis: der Käufer muss mindestens den Verkaufspreis bieten.
	var buy, sell *Order
	if a.Side == OrderBuy {
		buy, sell = a, b
	} else {
		buy, sell = b, a
	}
	// buy.PriceSOL = was der Käufer max. zahlt; sell.PriceSOL = was der Verkäufer min. will.
	return buy.PriceSOL >= sell.PriceSOL
}

// weAreTaker entscheidet, ob WIR (my) den Swap auslösen: die neuere Order ist Taker.
func weAreTaker(my, other *Order) bool {
	if my.CreatedAt != other.CreatedAt {
		return my.CreatedAt > other.CreatedAt // wir sind neuer → Taker
	}
	return my.ID > other.ID // Gleichstand: größere ID ist Taker
}

// autoTriggerSwap löst einen Swap für ein automatisches Match aus. WIR sind der
// Taker (unsere Order ist neuer), other ist die Maker-Order. Nutzt unsere
// hinterlegten Keys.
func (sc *swapCoordinator) autoTriggerSwap(my, other *Order) bool {
	s := sc.server
	// Die activeMatches-Sperre wird NICHT hier freigegeben, sondern erst wenn der
	// Swap abgeschlossen ist (in finalizeSwap) oder bei Auslöse-Fehler unten.
	// Sonst würde der nächste matchOnce-Zyklus denselben Swap erneut auslösen,
	// während er noch läuft.

	sc.mu.Lock()
	k := sc.deposits[my.ID]
	sc.mu.Unlock()
	if k == nil {
		return false
	}

	// Richtung: other (Maker) ist die anzunehmende Order. takerGivesSol, wenn
	// die Maker-Order eine Verkaufs-Order ist (Maker verkauft FND → Taker gibt SOL).
	takerGivesSol := other.Side == OrderSell

	// Menge: das Minimum beider offenen Mengen.
	amountFND := my.AmountFND
	if other.AmountFND < amountFND {
		amountFND = other.AmountFND
	}
	if amountFND <= 0 {
		return false
	}
	amountSOL := amountFND * other.PriceSOL

	// Geheimnis erzeugen.
	var secret [32]byte
	_, _ = crand.Read(secret[:])
	hash := blake3.Sum256(secret[:])

	// Taker-Adressen aus unseren hinterlegten Keys ableiten.
	takerSolAddr := ""
	if len(k.solKey) > 0 {
		takerSolAddr = k.solKey.PublicKey().String()
	}
	takerFndAddr := ""
	if len(k.fndSeed) > 0 {
		if addr, ok := s.fndAddressFromSeed(k.fndSeed); ok {
			takerFndAddr = addr
		}
	}

	// Deckung beider Seiten prüfen, bevor wir (als Erst-Sperrer) Mittel binden.
	// Die eigene Order (my) zählt dabei nicht als "gebunden" – sie wird gerade erfüllt.
	fctx, fcancel := context.WithTimeout(context.Background(), 15*time.Second)
	fe := s.checkTakeFunds(fctx, other, amountFND, takerGivesSol, takerSolAddr, takerFndAddr, my.ID)
	fcancel()
	if fe != nil {
		if s.log != nil {
			s.log.Info("Auto-Match übersprungen – nicht gedeckt",
				zap.String("myOrder", my.ID), zap.String("otherOrder", other.ID), zap.String("grund", fe.Msg))
		}
		return false
	}

	msg := swapInitMsg{
		OrderID:       other.ID, // die angenommene (Maker-)Order
		Hashlock:      hex.EncodeToString(hash[:]),
		BuyerSol:      takerSolAddr,
		BuyerFnd:      takerFndAddr,
		AmountSOL:     amountSOL,
		AmountFND:     amountFND,
		TakerGivesSol: takerGivesSol,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := s.swapCoord.triggerRemoteSwap(ctx, s.orderBook.Node(), other.MakerPeer, msg,
		k.solKey, k.fndSeed, secret, other.SolAddress, other.FndAddress, takerGivesSol, my.ID)
	if s.log != nil {
		if err != nil {
			s.log.Warn("Auto-Match: Swap-Auslösung fehlgeschlagen",
				zap.String("myOrder", my.ID), zap.String("otherOrder", other.ID),
				zap.String("peer", other.MakerPeer), zap.Error(err))
		} else {
			s.log.Info("Auto-Match: Swap ausgelöst",
				zap.String("myOrder", my.ID), zap.String("otherOrder", other.ID),
				zap.Float64("amount", amountFND))
		}
	}
	// Bei Auslöse-Fehler gibt der Aufrufer die Sperre frei (mit Backoff).
	// Bei Erfolg bleibt sie, bis der Orchestrator den Swap abschließt – oder
	// der Wächter nach autoSwapTimeout eingreift.
	return err == nil
}
