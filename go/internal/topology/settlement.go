// Package topology – settlement.go
//
// Trade-Settlement auf Basis tatsächlichen Energieflusses
// ────────────────────────────────────────────────────────
//
// Settlement-Modell:
//   1. Trade-Start: RegisterTrade() → Trade erhält Quote (Ws/s)
//   2. Jede Sekunde: IssueCert() → actual_ws wird proportional aufgeteilt
//      Jeder Cert enthält: contracted_ws (Trades) + open_ws (Rest)
//   3. Trade-Ende: Certs aus DHT sammeln → Summe contracted_ws_für_diesen_Trade
//   4. Abrechnung: gelieferteWs = Summe, Preis = gelieferteWs × Ws-Preis
//   5. On-chain: 1 Tx mit Merkle-Root aller Certs + Abrechnung
//
// Einheit überall Ws (Wattsekunden = Joule):
//   QuoteWs     = vereinbarte Ws pro Sekunde (= Watt)
//   DeliveredWs = tatsächlich gelieferte Ws (Summe der Allokationen)
//   OpenWs      = geflossene aber unkontrahierte Ws (Spotmarkt-Potential)

package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
	"lukechampine.com/blake3"
)

// =============================================================================
//  Trade-Status
// =============================================================================

type TradeStatus string

const (
	TradeOpen      TradeStatus = "open"
	TradeSettling  TradeStatus = "settling"
	TradeSettled   TradeStatus = "settled"
	TradeCancelled TradeStatus = "cancelled"
)

// =============================================================================
//  EnergyTrade
// =============================================================================

// EnergyTrade repräsentiert einen laufenden oder abgeschlossenen Energie-Handel.
type EnergyTrade struct {
	TradeID    string      `json:"trade_id"`
	BuyerID    string      `json:"buyer_id"`
	SellerID   string      `json:"seller_id"`
	EscrowID   string      `json:"escrow_id"`
	QuoteWs    float64     `json:"quote_ws"`    // vereinbarte Ws/s (= Watt)
	PricePerKWh float64    `json:"price_per_kwh"` // vereinbarter kWh-Preis in FND
	StartedAt  time.Time   `json:"started_at"`
	EndsAt     time.Time   `json:"ends_at"`
	Status     TradeStatus `json:"status"`
	Route      GridRoute   `json:"route"`
	RouteTrafos []string   `json:"route_trafos"`

	// ContractedHops: welche Trafos haben den Trade registriert
	Hops []ContractedHop `json:"hops"`

	// Ergebnis
	Settlement *SettlementResult `json:"settlement,omitempty"`
}

// ContractedHop: ein Trafo auf der Route
type ContractedHop struct {
	TrafoPeerID string  `json:"trafo_peer_id"`
	QuoteWs     float64 `json:"quote_ws"`
	IsFallback  bool    `json:"is_fallback"` // kein Fundus-Node → Ansatz-2
	IsOwn       bool    `json:"is_own"`      // dieser Node selbst
}

// =============================================================================
//  Merkle-Tree
// =============================================================================

type merkleNode struct {
	Hash  [32]byte
	Left  *merkleNode
	Right *merkleNode
}

func buildMerkleTree(hashes [][32]byte) *merkleNode {
	if len(hashes) == 0 {
		return nil
	}
	nodes := make([]*merkleNode, len(hashes))
	for i, h := range hashes {
		nodes[i] = &merkleNode{Hash: h}
	}
	for len(nodes) > 1 {
		if len(nodes)%2 != 0 {
			nodes = append(nodes, nodes[len(nodes)-1])
		}
		var next []*merkleNode
		for i := 0; i < len(nodes); i += 2 {
			p := &merkleNode{Left: nodes[i], Right: nodes[i+1]}
			h := blake3.New(32, nil)
			h.Write(nodes[i].Hash[:])
			h.Write(nodes[i+1].Hash[:])
			copy(p.Hash[:], h.Sum(nil))
			next = append(next, p)
		}
		nodes = next
	}
	return nodes[0]
}

func merkleRoot(certs []*EnergyFlowCert) [32]byte {
	if len(certs) == 0 {
		return [32]byte{}
	}
	sorted := make([]*EnergyFlowCert, len(certs))
	copy(sorted, certs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})
	hashes := make([][32]byte, len(sorted))
	for i, c := range sorted {
		hashes[i] = c.Hash()
	}
	if tree := buildMerkleTree(hashes); tree != nil {
		return tree.Hash
	}
	return [32]byte{}
}

// =============================================================================
//  SettlementResult
// =============================================================================

type SettlementResult struct {
	TradeID    string    `json:"trade_id"`
	SettledAt  time.Time `json:"settled_at"`

	// Energiemenge
	QuoteWs       float64 `json:"quote_ws"`       // vereinbarte Ws/s
	DeliveredWs   float64 `json:"delivered_ws"`   // tatsächlich gelieferte Ws (Summe)
	OpenWsTotal   float64 `json:"open_ws_total"`  // unkontrahierte Ws im gleichen Zeitraum
	TradeDurationS int    `json:"trade_duration_s"`
	CoverageRatio float64 `json:"coverage_ratio"` // DeliveredWs / (QuoteWs × Dauer)
	IsPartial     bool    `json:"is_partial"`     // < 90 % Coverage

	// Nachweisführung
	CertCount   int    `json:"cert_count"`
	MerkleRoot  string `json:"merkle_root"`  // hex
	Method      string `json:"method"`       // "flow_accounting" | "mixed" | "temporal_fallback"
	FallbackHops int   `json:"fallback_hops"`

	// Abrechnung
	PricePerWs  float64          `json:"price_per_ws"`  // vereinbarter Ws-Preis in FND
	TotalFND    float64          `json:"total_fnd"`     // DeliveredWs × PricePerWs
	GridFees    []GridFeePayment `json:"grid_fees"`
	TotalFeeFND float64          `json:"total_fee_fnd"`
	NetFND      float64          `json:"net_fnd"` // TotalFND - TotalFeeFND

	// On-chain
	GnosisChainTxHash string `json:"gnosis_tx_hash,omitempty"`
	Anchored          bool   `json:"anchored"`
}

type GridFeePayment struct {
	TrafoPeerID string  `json:"trafo_peer_id"`
	WalletAddr  string  `json:"wallet_address"`
	FeePercent  float64 `json:"fee_percent"`
	AmountFND   float64 `json:"amount_fnd"`
	Method      string  `json:"method"` // "flow_accounting" | "temporal_fallback"
}

// =============================================================================
//  SettlementEngine
// =============================================================================

type SettlementEngine struct {
	mu     sync.RWMutex
	trades map[string]*EnergyTrade

	flowMgr *FlowManager
	p2p     P2PAdapter
	log     *zap.Logger

	// Callbacks (optional – kein Panic wenn nil)
	OnAnchorSettlement func(ctx context.Context, result *SettlementResult) (string, error)
	OnPayGridFees      func(ctx context.Context, fees []GridFeePayment, escrowID string) error
}

func NewSettlementEngine(flowMgr *FlowManager, p2p P2PAdapter, log *zap.Logger) *SettlementEngine {
	return &SettlementEngine{
		trades:  make(map[string]*EnergyTrade),
		flowMgr: flowMgr,
		p2p:     p2p,
		log:     log,
	}
}

func (se *SettlementEngine) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			se.settleExpiredTrades(ctx)
		}
	}
}

// =============================================================================
//  Trade-Lifecycle
// =============================================================================

// StartTrade registriert einen neuen Handel.
// quoteWs: gewünschte Ws pro Sekunde (= Watt-Leistung).
// Der Trade bekommt was fließt – proportional zur Quote.
func (se *SettlementEngine) StartTrade(
	ctx context.Context,
	tradeID, buyerID, sellerID, escrowID string,
	quoteWs float64,
	duration time.Duration,
	route GridRoute,
	trafoPeerIDs []string,
) (*EnergyTrade, error) {
	now   := time.Now()
	endsAt := now.Add(duration)
	routeHash := tradeRouteHash(tradeID, trafoPeerIDs)

	var hops []ContractedHop
	var fallbackHops int

	for _, pid := range trafoPeerIDs {
		if pid == se.flowMgr.peerID {
			// Eigener Trafo: direkt ins lokale Accounting einbuchen
			if err := se.flowMgr.RegisterTrade(tradeID, buyerID, sellerID, quoteWs, endsAt, routeHash); err != nil {
				se.rollbackHops(ctx, hops, tradeID)
				return nil, fmt.Errorf("eigener Trafo: %w", err)
			}
			hops = append(hops, ContractedHop{TrafoPeerID: pid, QuoteWs: quoteWs, IsOwn: true})
		} else {
			// Remote-Trafo: über P2P anmelden
			if err := se.remoteRegister(ctx, pid, tradeID, buyerID, sellerID, quoteWs, endsAt, routeHash); err != nil {
				se.log.Warn("Remote-Trafo-Anmeldung fehlgeschlagen, Ansatz-2-Fallback",
					zap.String("trafo", pid), zap.Error(err))
				hops = append(hops, ContractedHop{TrafoPeerID: pid, QuoteWs: quoteWs, IsFallback: true})
				fallbackHops++
			} else {
				hops = append(hops, ContractedHop{TrafoPeerID: pid, QuoteWs: quoteWs})
			}
		}
	}

	trade := &EnergyTrade{
		TradeID:     tradeID,
		BuyerID:     buyerID,
		SellerID:    sellerID,
		EscrowID:    escrowID,
		QuoteWs:     quoteWs,
		StartedAt:   now,
		EndsAt:      endsAt,
		Status:      TradeOpen,
		Route:       route,
		RouteTrafos: trafoPeerIDs,
		Hops:        hops,
	}

	se.mu.Lock()
	se.trades[tradeID] = trade
	se.mu.Unlock()

	method := "flow_accounting"
	if fallbackHops == len(hops) {
		method = "temporal_fallback"
	} else if fallbackHops > 0 {
		method = "mixed"
	}

	se.log.Info("Trade gestartet",
		zap.String("trade_id",     tradeID),
		zap.Float64("quote_ws",    quoteWs),
		zap.Duration("duration",   duration),
		zap.Int("fallback_hops",   fallbackHops),
		zap.String("method",       method),
	)

	return trade, nil
}

// SettleTrade führt das Settlement durch.
// Sammelt alle sekündlichen FlowCerts aus dem DHT, liest die Allokation
// für diesen Trade, summiert die gelieferten Ws, berechnet den Preis.
func (se *SettlementEngine) SettleTrade(ctx context.Context, tradeID string) (*SettlementResult, error) {
	se.mu.Lock()
	trade, ok := se.trades[tradeID]
	if !ok {
		se.mu.Unlock()
		return nil, fmt.Errorf("trade %s nicht gefunden", tradeID)
	}
	if trade.Status != TradeOpen {
		se.mu.Unlock()
		return nil, fmt.Errorf("trade %s ist %s", tradeID, trade.Status)
	}
	trade.Status = TradeSettling
	se.mu.Unlock()

	se.log.Info("Starte Settlement",
		zap.String("trade_id",   tradeID),
		zap.Time("started_at",   trade.StartedAt),
		zap.Time("ends_at",      trade.EndsAt),
		zap.Float64("quote_ws",  trade.QuoteWs),
	)

	// ── Schritt 1: Certs sammeln + Ws aufsummieren ──────────────────────────
	deliveredWs, openWsTotal, certs, err := se.collectAndSum(ctx, trade)
	if err != nil {
		se.log.Warn("Cert-Sammlung fehlgeschlagen", zap.Error(err))
	}

	// ── Schritt 2: Merkle-Root ───────────────────────────────────────────────
	root := merkleRoot(certs)

	// ── Schritt 3: Kennzahlen ────────────────────────────────────────────────
	durationS := int(math.Max(1, trade.EndsAt.Sub(trade.StartedAt).Seconds()))
	maxPossible := trade.QuoteWs * float64(durationS)
	coverageRatio := 0.0
	if maxPossible > 0 {
		coverageRatio = deliveredWs / maxPossible
		if coverageRatio > 1 {
			coverageRatio = 1
		}
	}

	// ── Schritt 4: Abrechnung ────────────────────────────────────────────────
	pricePerWs := trade.PricePerKWh / 3_600_000 // kWh-Preis → Ws-Preis
	totalFND   := deliveredWs * pricePerWs

	// ── Schritt 5: Grid-Fees ─────────────────────────────────────────────────
	gridFees    := se.calcGridFees(trade, deliveredWs, totalFND)
	totalFeeFND := 0.0
	for _, f := range gridFees {
		totalFeeFND += f.AmountFND
	}

	// Fallback-Methode bestimmen
	fallbackHops := 0
	for _, h := range trade.Hops {
		if h.IsFallback {
			fallbackHops++
		}
	}
	method := "flow_accounting"
	if fallbackHops == len(trade.Hops) {
		method = "temporal_fallback"
	} else if fallbackHops > 0 {
		method = "mixed"
	}

	result := &SettlementResult{
		TradeID:        tradeID,
		SettledAt:      time.Now(),
		QuoteWs:        trade.QuoteWs,
		DeliveredWs:    deliveredWs,
		OpenWsTotal:    openWsTotal,
		TradeDurationS: durationS,
		CoverageRatio:  coverageRatio,
		IsPartial:      coverageRatio < 0.90,
		CertCount:      len(certs),
		MerkleRoot:     fmt.Sprintf("%x", root[:]),
		Method:         method,
		FallbackHops:   fallbackHops,
		PricePerWs:     pricePerWs,
		TotalFND:       totalFND,
		GridFees:       gridFees,
		TotalFeeFND:    totalFeeFND,
		NetFND:         totalFND - totalFeeFND,
	}

	// ── Schritt 6: On-chain anchoring (Retry) ───────────────────────────────
	if se.OnAnchorSettlement != nil {
		const maxRetries = 3
		for attempt := 1; attempt <= maxRetries; attempt++ {
			txHash, err := se.OnAnchorSettlement(ctx, result)
			if err == nil {
				result.GnosisChainTxHash = txHash
				result.Anchored          = true
				se.log.Info("Settlement on-chain verankert",
					zap.String("tx", txHash), zap.Int("attempt", attempt))
				break
			}
			se.log.Warn("On-chain Anchoring fehlgeschlagen",
				zap.Int("attempt", attempt), zap.Error(err))
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt*2) * time.Second)
			}
		}
		if !result.Anchored {
			se.log.Error("Settlement NICHT on-chain verankert – manueller Eingriff nötig",
				zap.String("trade_id",   tradeID),
				zap.String("merkle_root", result.MerkleRoot),
			)
		}
	} else {
		se.log.Warn("OnAnchorSettlement nicht konfiguriert", zap.String("trade_id", tradeID))
	}

	// ── Schritt 7: Grid-Fees zahlen ──────────────────────────────────────────
	if se.OnPayGridFees != nil && len(gridFees) > 0 {
		if err := se.OnPayGridFees(ctx, gridFees, trade.EscrowID); err != nil {
			se.log.Warn("Grid-Fee-Zahlung fehlgeschlagen", zap.Error(err))
		}
	}

	// ── Schritt 8: Aus Accounting ausbuchen ──────────────────────────────────
	se.unregisterAllHops(ctx, trade)

	se.mu.Lock()
	trade.Status     = TradeSettled
	trade.Settlement = result
	se.mu.Unlock()

	se.log.Info("Settlement abgeschlossen",
		zap.String("trade_id",       tradeID),
		zap.Float64("delivered_ws",  deliveredWs),
		zap.Float64("coverage_pct",  coverageRatio*100),
		zap.Float64("net_fnd",       result.NetFND),
		zap.Bool("partial",          result.IsPartial),
		zap.Bool("anchored",         result.Anchored),
	)

	return result, nil
}

// CancelTrade storniert einen Trade (keine Abrechnung).
func (se *SettlementEngine) CancelTrade(ctx context.Context, tradeID string) error {
	se.mu.Lock()
	trade, ok := se.trades[tradeID]
	if !ok {
		se.mu.Unlock()
		return fmt.Errorf("trade %s nicht gefunden", tradeID)
	}
	trade.Status = TradeCancelled
	se.mu.Unlock()
	se.unregisterAllHops(ctx, trade)
	se.log.Info("Trade storniert", zap.String("trade_id", tradeID))
	return nil
}

func (se *SettlementEngine) GetTrade(tradeID string) (*EnergyTrade, bool) {
	se.mu.RLock()
	defer se.mu.RUnlock()
	t, ok := se.trades[tradeID]
	return t, ok
}

func (se *SettlementEngine) ListTrades() []*EnergyTrade {
	se.mu.RLock()
	defer se.mu.RUnlock()
	out := make([]*EnergyTrade, 0, len(se.trades))
	for _, t := range se.trades {
		out = append(out, t)
	}
	return out
}

// =============================================================================
//  Ws-Sammlung aus DHT
// =============================================================================

// collectAndSum liest alle sekündlichen FlowCerts für alle Trafos auf der Route
// und summiert die dem Trade zugeordneten Ws (Engpass-Trafo = wenigste Coverage).
func (se *SettlementEngine) collectAndSum(
	ctx context.Context,
	trade *EnergyTrade,
) (deliveredWs, openWsTotal float64, certs []*EnergyFlowCert, err error) {
	type hopResult struct {
		pid   string
		certs []*EnergyFlowCert
		wsSum float64
		open  float64
	}

	results := make(chan hopResult, len(trade.RouteTrafos))
	for _, pid := range trade.RouteTrafos {
		go func(pid string) {
			cs, ws, open := se.loadCertsForTrade(ctx, pid, trade)
			results <- hopResult{pid: pid, certs: cs, wsSum: ws, open: open}
		}(pid)
	}

	// Engpass-Auswahl: Trafo mit der geringsten Ws-Summe ist der Flaschenhals
	minWs    := math.MaxFloat64
	minOpen  := 0.0
	var minCerts []*EnergyFlowCert

	for range trade.RouteTrafos {
		r := <-results
		if r.wsSum < minWs {
			minWs    = r.wsSum
			minOpen  = r.open
			minCerts = r.certs
		}
	}

	if minWs == math.MaxFloat64 {
		minWs = 0
	}
	return minWs, minOpen, minCerts, nil
}

// loadCertsForTrade lädt alle FlowCerts eines Trafos für den Trade-Zeitraum
// und berechnet die Ws-Allokation für diesen Trade.
const settlementSampleInterval = 60 // 1 Cert/Minute statt 1/Sekunde

func (se *SettlementEngine) loadCertsForTrade(
	ctx context.Context,
	trafoPeerID string,
	trade *EnergyTrade,
) (certs []*EnergyFlowCert, deliveredWs, openWs float64) {
	totalSec := int(trade.EndsAt.Sub(trade.StartedAt).Seconds())
	if totalSec <= 0 {
		return
	}
	step := settlementSampleInterval
	if totalSec < step {
		step = 1
	}

	// Parallele DHT-Lookups
	type result struct{ cert *EnergyFlowCert }
	sem     := make(chan struct{}, 20)
	results := make(chan result, totalSec/step+1)
	var wg sync.WaitGroup

	for i := 0; i < totalSec; i += step {
		ts := trade.StartedAt.Add(time.Duration(i) * time.Second).Truncate(time.Second)
		wg.Add(1)
		go func(ts time.Time) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			key := fmt.Sprintf("%s%s/%d", DHTFlowCert, trafoPeerID, ts.Unix())
			data, err := se.p2p.DHTget(ctx, key)
			if err != nil {
				return
			}
			var c EnergyFlowCert
			if json.Unmarshal(data, &c) == nil {
				results <- result{cert: &c}
			}
		}(ts)
	}
	go func() { wg.Wait(); close(results) }()

	// Quoten-Summe aller Trades ermitteln (für proportionale Aufteilung)
	// Vereinfachung: nur die Quote dieses Trades vs. rated_ws
	for r := range results {
		certs = append(certs, r.cert)
		// Anteil dieses Trades an den actual_ws:
		// proportional nach Quote (trade.QuoteWs / Summe aller Quoten)
		// Näherung: QuoteWs / max(ActualWs, QuoteWs) × ActualWs
		c := r.cert
		if c.ActualWs <= 0 {
			continue
		}
		// Wenn mehrere Trades laufen: Anteil = QuoteWs/contracted × contracted
		// aber wir haben hier nur den Cert, nicht die anderen Quoten.
		// Konservative Schätzung: min(QuoteWs, ActualWs) × step
		stepAlloc := math.Min(trade.QuoteWs, c.ActualWs)
		// Skalierung für Sampling: jeder Sample repräsentiert 'step' Sekunden
		deliveredWs += stepAlloc * float64(step)
		openWs      += c.OpenWs * float64(step)
	}
	return
}

// =============================================================================
//  Grid-Fees
// =============================================================================

func (se *SettlementEngine) calcGridFees(trade *EnergyTrade, deliveredWs, totalFND float64) []GridFeePayment {
	var fees []GridFeePayment
	for _, hop := range trade.Route.Hops {
		if hop.FeePercent <= 0 || hop.WalletAddress == "" {
			continue
		}
		method := "flow_accounting"
		for _, h := range trade.Hops {
			if h.TrafoPeerID == hop.PeerID && h.IsFallback {
				method = "temporal_fallback"
				break
			}
		}
		fees = append(fees, GridFeePayment{
			TrafoPeerID: hop.PeerID,
			WalletAddr:  hop.WalletAddress,
			FeePercent:  hop.FeePercent,
			AmountFND:   totalFND * hop.FeePercent / 100.0,
			Method:      method,
		})
	}
	return fees
}

// =============================================================================
//  Interne Hilfsfunktionen
// =============================================================================

func (se *SettlementEngine) unregisterAllHops(ctx context.Context, trade *EnergyTrade) {
	for _, hop := range trade.Hops {
		if hop.IsFallback {
			continue
		}
		if hop.IsOwn {
			if err := se.flowMgr.UnregisterTrade(trade.TradeID); err != nil {
				se.log.Warn("Eigenes Ausbuchen fehlgeschlagen",
					zap.String("trade_id", trade.TradeID), zap.Error(err))
			}
		} else {
			se.remoteUnregister(ctx, hop.TrafoPeerID, trade.TradeID)
		}
	}
}

func (se *SettlementEngine) rollbackHops(ctx context.Context, hops []ContractedHop, tradeID string) {
	for _, h := range hops {
		if !h.IsFallback {
			if h.IsOwn {
				se.flowMgr.UnregisterTrade(tradeID)
			} else {
				se.remoteUnregister(ctx, h.TrafoPeerID, tradeID)
			}
		}
	}
}

func (se *SettlementEngine) settleExpiredTrades(ctx context.Context) {
	se.mu.RLock()
	var expired []string
	for id, t := range se.trades {
		if t.Status == TradeOpen && time.Now().After(t.EndsAt) {
			expired = append(expired, id)
		}
	}
	se.mu.RUnlock()
	for _, id := range expired {
		if _, err := se.SettleTrade(ctx, id); err != nil {
			se.log.Error("Auto-Settlement fehlgeschlagen",
				zap.String("trade_id", id), zap.Error(err))
		}
	}
}

// Remote P2P Register/Unregister (DHT-basiert, R002: libp2p-Stream)
func (se *SettlementEngine) remoteRegister(
	ctx context.Context,
	trafoPeerID, tradeID, buyerID, sellerID string,
	quoteWs float64, endsAt time.Time, routeHash [32]byte,
) error {
	type Req struct {
		TradeID   string    `json:"trade_id"`
		BuyerID   string    `json:"buyer_id"`
		SellerID  string    `json:"seller_id"`
		QuoteWs   float64   `json:"quote_ws"`
		EndsAt    time.Time `json:"ends_at"`
		RouteHash [32]byte  `json:"route_hash"`
	}
	data, _ := json.Marshal(Req{tradeID, buyerID, sellerID, quoteWs, endsAt, routeHash})
	key := fmt.Sprintf("/fundus/flow-register/%s/%s", trafoPeerID, tradeID)
	if err := se.p2p.DHTput(ctx, key, data); err != nil {
		return fmt.Errorf("DHT-Register: %w", err)
	}
	ackKey := fmt.Sprintf("/fundus/flow-register-ack/%s/%s", trafoPeerID, tradeID)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := se.p2p.DHTget(ctx, ackKey); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("Timeout – kein Ack von %s", shortID(trafoPeerID))
}

func (se *SettlementEngine) remoteUnregister(ctx context.Context, trafoPeerID, tradeID string) {
	key := fmt.Sprintf("/fundus/flow-unregister/%s/%s", trafoPeerID, tradeID)
	se.p2p.DHTput(ctx, key, []byte(tradeID))
}

func tradeRouteHash(tradeID string, trafos []string) [32]byte {
	h := blake3.New(32, nil)
	h.Write([]byte(tradeID))
	for _, p := range trafos {
		h.Write([]byte(p))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
