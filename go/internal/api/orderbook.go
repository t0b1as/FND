package api

// Dezentrales Orderbuch für FND/SOL-Swaps. Orders werden per P2P propagiert
// (wie die Verzeichnis-Freigaben): jeder Node hält seine eigenen offenen Orders
// und fragt periodisch die verbundenen Peers nach deren Orders ab. Ausgeführte
// oder abgelaufene Orders verschwinden automatisch (werden nicht mehr propagiert).
//
// Die eigentliche Abwicklung eines Matches läuft später über den HTLC-Cross-
// Chain-Swap (FND↔SOL, atomar). Das Orderbuch selbst ist nur die Absichts-
// bekundung: "Ich biete X FND zum Preis Y in SOL".

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"lukechampine.com/blake3"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// OrderProtocol ist das P2P-Protokoll zur direkten Order-Abfrage.
const OrderProtocol = "/fundus/orders-query/1.0.0"

// OrderPushProtocol ist das Push-Protokoll: bei Order-Änderungen sendet der Node
// sofort ein Update an alle Peers (schneller als der Poll). Der Poll bleibt als
// Sicherheitsnetz, damit auch Nodes, die einen Push verpassen, konsistent werden.
const OrderPushProtocol = "/fundus/orders-push/1.0.0"

// OrderSide: Kauf oder Verkauf von FND (Gegenwährung SOL).
type OrderSide string

const (
	OrderBuy  OrderSide = "buy"  // will FND kaufen (zahlt SOL)
	OrderSell OrderSide = "sell" // will FND verkaufen (erhält SOL)
)

// OrderType: Limit (fester Preis) oder Markt (bestes verfügbares).
type OrderType string

const (
	OrderLimit  OrderType = "limit"
	OrderMarket OrderType = "market"
)

// Order ist eine einzelne Order im Buch.
type Order struct {
	ID         string    `json:"id"`          // eindeutig (blake3 aus Feldern)
	Maker      string    `json:"maker"`       // Wallet-Adresse des Erstellers (FND)
	MakerPeer  string    `json:"maker_peer"`  // Peer-ID (für Rückkanal/Kontakt)
	Side       OrderSide `json:"side"`        // buy | sell
	Type       OrderType `json:"type"`        // limit | market
	AmountFND  float64   `json:"amount_fnd"`  // Menge FND
	PriceSOL   float64   `json:"price_sol"`   // Preis pro FND in SOL (nur bei limit)
	CreatedAt  int64     `json:"created_at"`  // Unix-Sekunden
	ExpiresAt  int64     `json:"expires_at"`  // Unix-Sekunden; danach ungültig
	FndAddress string    `json:"fnd_address"` // FND-Adresse (Empfang bei Kauf, Zahlung bei Verkauf)
	SolAddress string    `json:"sol_address"` // Solana-Adresse (Zahlung bei Kauf, Empfang bei Verkauf)
}

// isActive prüft, ob die Order noch gültig (nicht abgelaufen) ist.
func (o *Order) isActive() bool {
	return o.ExpiresAt == 0 || time.Now().Unix() < o.ExpiresAt
}

// OrderBook verwaltet die eigenen Orders und den Cache der Netz-Orders.
type OrderBook struct {
	mu       sync.RWMutex
	myOrders map[string]*Order // eigene offene Orders (ID → Order)

	discMu     sync.RWMutex
	discovered []byte    // gecachtes JSON aller im Netz entdeckten Orders (Poll)
	discoveredOrders map[string]*Order // strukturierte Kopie (ID → Order) für FindOrder
	discAt     time.Time // Zeitpunkt der letzten Discovery

	// pushedOrders: per Push empfangene Fremd-Orders (ID → Order). Ergänzt den
	// Poll-Cache in Echtzeit. Eine gelöschte Order wird hier sofort entfernt.
	pushMu       sync.RWMutex
	pushedOrders map[string]*Order

	node p2pNode // Zugriff auf Peers + Send + RegisterProtocol

	// path: Datei für die EIGENEN Orders (übersteht Updates/Neustarts/Ausfälle).
	// Fremde Orders werden bewusst nicht gespeichert – sie kommen bei jedem Poll
	// frisch vom Ersteller; ein alter Stand könnte stornierte Orders zeigen.
	path string
	saveMu sync.Mutex
}

// OrderPush ist die Nachricht eines Push-Updates.
type OrderPush struct {
	Action string `json:"action"` // "upsert" | "remove"
	Order  *Order `json:"order,omitempty"`
	ID     string `json:"id,omitempty"` // bei remove
}

// p2pNode ist das Minimal-Interface, das der OrderBook vom Node braucht.
type p2pNode interface {
	ID() interface{ String() string }
	Peers() []interface{ String() string }
	SendAndReceive(ctx context.Context, peerID string, protocol string, data []byte) ([]byte, error)
	SendToPeer(ctx context.Context, peerID string, protocol string, data []byte) error
	RegisterProtocol(protocol string, handler func(peerID string, data []byte) []byte)
}

// newOrderBook erstellt das Orderbuch und registriert das P2P-Protokoll.
func newOrderBook(node p2pNode, path string) *OrderBook {
	ob := &OrderBook{
		myOrders:     make(map[string]*Order),
		pushedOrders: make(map[string]*Order),
		node:         node,
		path:         path,
	}
	ob.load()
	if node != nil {
		node.RegisterProtocol(OrderProtocol, func(peerID string, req []byte) []byte {
			return ob.myOrdersJSON()
		})
		// Push-Empfang: eingehende Order-Updates in den Push-Speicher übernehmen.
		node.RegisterProtocol(OrderPushProtocol, func(peerID string, data []byte) []byte {
			ob.handlePush(data)
			return []byte("ok")
		})
	}
	return ob
}

// handlePush verarbeitet ein eingehendes Push-Update (neue/geänderte oder
// gelöschte Fremd-Order).
func (ob *OrderBook) handlePush(data []byte) {
	var p OrderPush
	if json.Unmarshal(data, &p) != nil {
		return
	}
	ob.pushMu.Lock()
	switch p.Action {
	case "upsert":
		if p.Order != nil && p.Order.isActive() {
			ob.pushedOrders[p.Order.ID] = p.Order
		}
	case "remove":
		id := p.ID
		if id == "" && p.Order != nil {
			id = p.Order.ID
		}
		delete(ob.pushedOrders, id)
	}
	ob.pushMu.Unlock()

	// WICHTIG: Push-Updates auch im discovered/Poll-Cache spiegeln, sonst bleibt
	// eine entfernte/reduzierte Order dort stehen (Poll läuft nur alle 30s).
	id := p.ID
	if id == "" && p.Order != nil {
		id = p.Order.ID
	}
	ob.discMu.Lock()
	if ob.discoveredOrders != nil {
		switch p.Action {
		case "upsert":
			if p.Order != nil && p.Order.isActive() {
				ob.discoveredOrders[p.Order.ID] = p.Order
			}
		case "remove":
			delete(ob.discoveredOrders, id)
		}
	}
	ob.discMu.Unlock()
}

// pushToAll sendet ein Update an alle verbundenen Peers — mehrfach mit kurzem
// Abstand, damit transiente Netzwerk-Fehler das Update nicht verschlucken. Der
// Poll bleibt das Sicherheitsnetz für Nodes, die alle Pushes verpassen.
func (ob *OrderBook) pushToAll(push OrderPush) {
	if ob.node == nil {
		return
	}
	data, _ := json.Marshal(push)
	send := func() {
		myID := ob.node.ID().String()
		for _, p := range ob.node.Peers() {
			pid := p.String()
			if pid == myID {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			_ = ob.node.SendToPeer(ctx, pid, OrderPushProtocol, data)
			cancel()
		}
	}
	// Sofort + zwei Wiederholungen in den ersten Sekunden (gegen transiente Fehler).
	go func() {
		send()
		time.Sleep(2 * time.Second)
		send()
		time.Sleep(3 * time.Second)
		send()
	}()
}

// myOrdersJSON liefert die eigenen AKTIVEN Orders als JSON (für die Peer-Abfrage).
// Abgelaufene Orders werden dabei aussortiert (verschwinden aus dem Netz).
func (ob *OrderBook) myOrdersJSON() []byte {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	list := make([]*Order, 0, len(ob.myOrders))
	for _, o := range ob.myOrders {
		if o.isActive() {
			list = append(list, o)
		}
	}
	data, _ := json.Marshal(list)
	return data
}

// addOrder fügt eine eigene Order hinzu und pusht sie sofort ins Netz.
func (ob *OrderBook) addOrder(o *Order) {
	ob.mu.Lock()
	ob.myOrders[o.ID] = o
	ob.mu.Unlock()
	ob.save()
	ob.pushToAll(OrderPush{Action: "upsert", Order: o})
}

// removeOrder entfernt eine eigene Order (Stornierung oder Ausführung) und pusht
// die Löschung sofort ins Netz — damit kein zweiter Käufer eine bereits
// ausgeführte Order sieht (das kleine Restfenster deckt die Chain ab).
func (ob *OrderBook) removeOrder(id string) bool {
	ob.mu.Lock()
	_, ok := ob.myOrders[id]
	if ok {
		delete(ob.myOrders, id)
	}
	ob.mu.Unlock()
	if ok {
		ob.save()
		ob.pushToAll(OrderPush{Action: "remove", ID: id})
	}
	return ok
}

// myOrders liefert eine Kopie der eigenen aktiven Orders.
func (ob *OrderBook) listMyOrders() []*Order {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	out := make([]*Order, 0, len(ob.myOrders))
	for _, o := range ob.myOrders {
		if o.isActive() {
			out = append(out, o)
		}
	}
	return out
}

// Run startet die Hintergrund-Discovery: alle 10s die Peers nach ihren Orders
// abfragen und cachen. Abgelaufene/gelöschte Orders fallen automatisch weg.
func (ob *OrderBook) Run(ctx context.Context) {
	if ob.node == nil {
		return
	}
	scan := func() {
		type peerOrders struct {
			PeerID string          `json:"peer_id"`
			Orders json.RawMessage `json:"orders"`
		}
		out := []peerOrders{}
		myID := ob.node.ID().String()
		for _, p := range ob.node.Peers() {
			pid := p.String()
			if pid == myID {
				continue
			}
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			data, err := ob.node.SendAndReceive(sctx, pid, OrderProtocol, []byte("q"))
			cancel()
			if err != nil || len(data) == 0 || string(data) == "null" {
				continue
			}
			out = append(out, peerOrders{PeerID: pid, Orders: json.RawMessage(data)})
		}
		j, _ := json.Marshal(out)
		ob.discMu.Lock()
		ob.discovered = j
		ob.discAt = time.Now()
		// Strukturierte Kopie für FindOrder aufbauen.
		dm := make(map[string]*Order)
		for _, po := range out {
			var orders []*Order
			if json.Unmarshal(po.Orders, &orders) == nil {
				for _, o := range orders {
					if o != nil && o.ID != "" {
						o.MakerPeer = po.PeerID // Peer-Herkunft sichern
						dm[o.ID] = o
					}
				}
			}
		}
		ob.discoveredOrders = dm
		ob.discMu.Unlock()

		// Abgelaufene EIGENE Orders entfernen und den Stand speichern.
		ob.mu.Lock()
		expired := 0
		for id, o := range ob.myOrders {
			if !o.isActive() {
				delete(ob.myOrders, id)
				expired++
			}
		}
		ob.mu.Unlock()
		if expired > 0 {
			ob.save()
		}

		// Abgelaufene Push-Orders bereinigen (das Sicherheitsnetz räumt auf).
		ob.pushMu.Lock()
		for id, o := range ob.pushedOrders {
			if !o.isActive() {
				delete(ob.pushedOrders, id)
			}
		}
		ob.pushMu.Unlock()
	}
	scan()
	// Poll als Sicherheitsnetz (Push macht die Geschwindigkeit). 30s reichen,
	// da Änderungen ohnehin sofort gepusht werden. Bei jedem Poll werden zudem
	// abgelaufene Push-Orders bereinigt.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan()
		}
	}
}

// discoveredJSON liefert den gecachten Discovery-Stand plus die per Push
// empfangenen Orders (billiger Read fürs Frontend). Push-Orders erscheinen so
// sofort, ohne auf den nächsten Poll zu warten.
func (ob *OrderBook) discoveredJSON() []byte {
	ob.discMu.RLock()
	pollData := ob.discovered
	ob.discMu.RUnlock()

	// Gepushte Orders als eigene "Peer-Gruppe" anhängen (nur aktive).
	ob.pushMu.RLock()
	pushed := make([]*Order, 0, len(ob.pushedOrders))
	for _, o := range ob.pushedOrders {
		if o.isActive() {
			pushed = append(pushed, o)
		}
	}
	ob.pushMu.RUnlock()

	type peerOrders struct {
		PeerID string          `json:"peer_id"`
		Orders json.RawMessage `json:"orders"`
	}
	var groups []peerOrders
	if len(pollData) > 0 {
		_ = json.Unmarshal(pollData, &groups)
	}
	if len(pushed) > 0 {
		pj, _ := json.Marshal(pushed)
		groups = append(groups, peerOrders{PeerID: "push", Orders: pj})
	}
	out, _ := json.Marshal(groups)
	return out
}

// ── API-Handler ─────────────────────────────────────────────────────────────

// orderCreate legt eine eigene Order an.
// POST /api/v1/orders  Body: { side, type, amount_fnd, price_sol, sol_address, ttl_sec }
func (s *Server) orderCreate(c *gin.Context) {
	if s.orderBook == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Orderbuch nicht aktiv"})
		return
	}
	var req struct {
		Side       OrderSide `json:"side"       binding:"required"`
		Type       OrderType `json:"type"       binding:"required"`
		AmountFND  float64   `json:"amount_fnd" binding:"required"`
		PriceSOL   float64   `json:"price_sol"`
		SolAddress string    `json:"sol_address"`
		FndAddress string    `json:"fnd_address"`
		TTLSec     int64     `json:"ttl_sec"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Side != OrderBuy && req.Side != OrderSell {
		c.JSON(http.StatusBadRequest, gin.H{"error": "side muss buy oder sell sein"})
		return
	}
	if req.Type == OrderLimit && req.PriceSOL <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Limit-Order braucht einen Preis > 0"})
		return
	}
	if req.AmountFND <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Menge muss > 0 sein"})
		return
	}
	ttl := req.TTLSec
	if ttl <= 0 {
		ttl = 24 * 3600 // Standard: 24 Stunden
	}
	now := time.Now().Unix()
	maker := ""
	if s.fileStore != nil {
		maker = s.fileStore.NodeWalletAddress()
	}
	peerID := ""
	if s.node != nil {
		peerID = s.node.ID().String()
	}
	// FND-Adresse: explizit angegeben, sonst die Node-Wallet als Fallback.
	fndAddr := req.FndAddress
	if fndAddr == "" && s.fileStore != nil {
		fndAddr = s.fileStore.NodeWalletAddress()
	}
	o := &Order{
		Maker: maker, MakerPeer: peerID,
		Side: req.Side, Type: req.Type,
		AmountFND: req.AmountFND, PriceSOL: req.PriceSOL,
		CreatedAt: now, ExpiresAt: now + ttl,
		FndAddress: fndAddr,
		SolAddress: req.SolAddress,
	}
	o.ID = orderID(o)
	s.orderBook.addOrder(o)
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": o.ID, "order": o})
}

// orderCancel entfernt eine eigene Order.
// DELETE /api/v1/orders/:id
func (s *Server) orderCancel(c *gin.Context) {
	if s.orderBook == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Orderbuch nicht aktiv"})
		return
	}
	if s.orderBook.removeOrder(c.Param("id")) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "Order nicht gefunden"})
}

// orderListMine liefert die eigenen offenen Orders.
// GET /api/v1/orders/mine
func (s *Server) orderListMine(c *gin.Context) {
	if s.orderBook == nil {
		c.JSON(http.StatusOK, gin.H{"orders": []interface{}{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"orders": s.orderBook.listMyOrders()})
}

// orderBookGet liefert das gesamte Netz-Orderbuch (eigene + entdeckte).
// GET /api/v1/orders/book
func (s *Server) orderBookGet(c *gin.Context) {
	obStart := time.Now()
	defer func() {
		if d := time.Since(obStart); d > 500*time.Millisecond && s.log != nil {
			s.log.Warn("orderBookGet langsam", zap.Duration("dauer", d))
		}
	}()
	if s.orderBook == nil {
		c.JSON(http.StatusOK, gin.H{"peers": []interface{}{}, "mine": []interface{}{}})
		return
	}
	c.Data(http.StatusOK, "application/json", wrapOrderBook(
		s.orderBook.discoveredJSON(), s.orderBook.listMyOrders()))
}

// wrapOrderBook verpackt entdeckte Peer-Orders + eigene Orders in ein JSON.
func wrapOrderBook(peersJSON []byte, mine []*Order) []byte {
	out, _ := json.Marshal(map[string]interface{}{
		"peers": json.RawMessage(peersJSON),
		"mine":  mine,
	})
	return out
}

// orderID berechnet eine eindeutige ID aus den Order-Feldern (blake3).
func orderID(o *Order) string {
	s := o.Maker + "|" + string(o.Side) + "|" + string(o.Type) + "|" +
		formatFloat(o.AmountFND) + "|" + formatFloat(o.PriceSOL) + "|" +
		formatInt(o.CreatedAt) + "|" + o.SolAddress
	sum := blake3.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:32]
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'f', 8, 64) }
func formatInt(i int64) string     { return strconv.FormatInt(i, 10) }

// Node gibt den p2pNode zurück (für die Swap-Koordination).
func (ob *OrderBook) Node() p2pNode { return ob.node }

// FindOrder sucht eine Order per ID (eigene + gepushte Orders).
func (ob *OrderBook) FindOrder(id string) (*Order, bool) {
	ob.mu.RLock()
	if o, ok := ob.myOrders[id]; ok {
		ob.mu.RUnlock()
		return o, true
	}
	if o, ok := ob.pushedOrders[id]; ok {
		ob.mu.RUnlock()
		return o, true
	}
	ob.mu.RUnlock()
	// Auch den Poll-Cache durchsuchen (Orders, die nicht gepusht wurden).
	ob.discMu.RLock()
	defer ob.discMu.RUnlock()
	if o, ok := ob.discoveredOrders[id]; ok {
		return o, true
	}
	return nil, false
}

// reduceOrder verringert die Menge einer eigenen Order um amount. Bleibt nichts
// übrig, wird sie entfernt. Die Änderung wird ins Netz propagiert.
func (ob *OrderBook) reduceOrder(id string, amount float64) {
	ob.mu.Lock()
	o, ok := ob.myOrders[id]
	if !ok {
		ob.mu.Unlock()
		return
	}
	o.AmountFND -= amount
	remaining := o.AmountFND
	var updated *Order
	if remaining > 0.00000001 {
		cp := *o
		updated = &cp
	} else {
		delete(ob.myOrders, id)
	}
	ob.mu.Unlock()
	ob.save() // Teilausführung sofort festhalten

	if updated != nil {
		// Reduzierte Order neu propagieren (upsert).
		ob.pushToAll(OrderPush{Action: "upsert", ID: id, Order: updated})
	} else {
		ob.pushToAll(OrderPush{Action: "remove", ID: id})
	}
}

// SnapshotOrders gibt Kopien der eigenen und der entdeckten Orders zurück
// (für die Matching-Engine).
func (ob *OrderBook) SnapshotOrders() (mine []*Order, others []*Order) {
	ob.mu.RLock()
	for _, o := range ob.myOrders {
		cp := *o
		mine = append(mine, &cp)
	}
	seen := make(map[string]bool)
	for _, o := range ob.pushedOrders {
		cp := *o
		others = append(others, &cp)
		seen[o.ID] = true
	}
	ob.mu.RUnlock()
	ob.discMu.RLock()
	for _, o := range ob.discoveredOrders {
		if seen[o.ID] {
			continue // schon aus pushedOrders — nicht doppelt
		}
		cp := *o
		others = append(others, &cp)
	}
	ob.discMu.RUnlock()
	return mine, others
}

// =============================================================================
//  Persistenz der eigenen Orders
// =============================================================================

type orderBookFile struct {
	Version int      `json:"version"`
	Mine    []*Order `json:"mine"`
}

// save schreibt die eigenen Orders atomar auf die Platte.
func (ob *OrderBook) save() {
	if ob.path == "" {
		return
	}
	ob.mu.RLock()
	list := make([]*Order, 0, len(ob.myOrders))
	for _, o := range ob.myOrders {
		cp := *o
		list = append(list, &cp)
	}
	ob.mu.RUnlock()
	ob.saveMu.Lock()
	defer ob.saveMu.Unlock()
	_ = atomicWriteJSON(ob.path, orderBookFile{Version: 1, Mine: list})
}

// load lädt die eigenen Orders nach einem Neustart/Update. Abgelaufene fallen
// weg. Die geladenen Orders werden kurz danach erneut ins Netz gepusht
// (Peers fragen sie ohnehin per Poll ab).
func (ob *OrderBook) load() {
	if ob.path == "" {
		return
	}
	var f orderBookFile
	if err := readJSONFile(ob.path, &f); err != nil {
		return // keine Datei (erster Start) oder unlesbar
	}
	n := 0
	ob.mu.Lock()
	for _, o := range f.Mine {
		if o != nil && o.ID != "" && o.isActive() && o.AmountFND > 0 {
			ob.myOrders[o.ID] = o
			n++
		}
	}
	ob.mu.Unlock()
	if n == 0 {
		return
	}
	go func() {
		time.Sleep(20 * time.Second) // Peers verbinden lassen
		for _, o := range ob.listMyOrders() {
			ob.pushToAll(OrderPush{Action: "upsert", Order: o})
		}
	}()
}
