// Package topology – capacity.go
//
// Energiefluss-Accounting (Ansatz 3, finales Design)
//
// Grundprinzip: Nachgelagerte Verrechnung auf Basis tatsächlichen Flusses
// ────────────────────────────────────────────────────────────────────────
//
// Jede Sekunde misst der Smartmeter den tatsächlichen Energiefluss durch
// den Trafo in Wattsekunden (Ws = Joule). Dieser Wert wird im
// EnergyFlowCert dokumentiert und aufgeteilt in:
//
//   actual_ws     = tatsächlich geflossene Energie (Smartmeter-Messung)
//   contracted_ws = Anteil der laufenden Trades (post-hoc zugewiesen)
//   open_ws       = actual_ws − contracted_ws (noch nicht kontrahiert)
//
// Kein Vorausreservieren – ein Trade kontrahiert rückwirkend was bereits
// geflossen ist. Das ist physikalisch korrekt: Strom fließt zuerst,
// dann wird abgerechnet.
//
// Trade-Zuordnung (sekündlich):
//   Wenn mehrere Trades gleichzeitig laufen, werden die actual_ws
//   proportional zu ihren vereinbarten Quoten aufgeteilt.
//   Fließt weniger als alle Trades zusammen beanspruchen, bekommt
//   jeder Trade seinen proportionalen Anteil – kein Trade geht leer aus.
//
// Settlement:
//   Trade A (vereinbart 1.000 Ws/s, 3.600 s Laufzeit):
//     Sekunde 1: actual=1.347, contracted_A=1.000, open=347
//     Sekunde 2: actual=  800, contracted_A=  800 (nur soviel floss!)
//     ...
//     Gesamt: Summe aller contracted_ws_A = was wirklich geliefert wurde
//
// Einheit: Ws (Wattsekunden = Joule), nicht kW (Leistung)

package topology

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
	"lukechampine.com/blake3"
)

// =============================================================================
//  Konstanten
// =============================================================================

const (
	DHTFlowCert = "/fundus/flow/cert/"    // + trafo_id + "/" + unix_ts
	TopicFlow   = "fundus.energy.flow"    // GossipSub-Topic

	CertTTL    = 30 * time.Second
	CertMaxAge = 5  * time.Second

	HeadroomSafetyFactor = 0.95 // 5 % Reserve für Messunsicherheiten
)

// =============================================================================
//  EnergyFlowCert – sekündliche Energiebilanz des Trafos
// =============================================================================

// EnergyFlowCert dokumentiert den tatsächlichen Energiefluss dieser Sekunde
// und seine buchhalterische Aufteilung auf Trades und offene Kapazität.
//
// Alle Energiemengen in Ws (Wattsekunden = Joule).
type EnergyFlowCert struct {
	TrafoPeerID string    `json:"trafo_peer_id"`
	Timestamp   time.Time `json:"timestamp"` // auf Sekunde gerundet

	// Physikalische Messung (Smartmeter)
	ActualWs    float64 `json:"actual_ws"`    // geflossene Energie diese Sekunde
	RatedWs     float64 `json:"rated_ws"`     // Nennleistung × 1s (Ws/s Kapazität)
	Direction   string  `json:"direction"`    // "import" | "export" | "balanced"
	VoltageV    float64 `json:"voltage_v,omitempty"`
	FrequencyHz float64 `json:"frequency_hz,omitempty"`

	// Accounting-Aufteilung
	ContractedWs float64 `json:"contracted_ws"` // Anteil laufender Trades
	OpenWs        float64 `json:"open_ws"`       // = actual_ws - contracted_ws
	ActiveTrades  int     `json:"active_trades"` // Anzahl laufender Trades

	// Kryptographischer Nachweis
	// Commitment = BLAKE3(peer || ts || actual_ws || rated_ws || contracted_ws)
	Commitment [32]byte `json:"commitment"`
	Signature  []byte   `json:"signature"` // Ed25519(privkey, commitment)
}

func (c *EnergyFlowCert) Hash() [32]byte {
	data, _ := json.Marshal(c)
	h := blake3.New(32, nil)
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func (c *EnergyFlowCert) Verify(pubKey ed25519.PublicKey) bool {
	return ed25519.Verify(pubKey, c.Commitment[:], c.Signature)
}

func buildFlowCommitment(peerID string, ts time.Time, actualWs, ratedWs, contractedWs float64) [32]byte {
	h := blake3.New(32, nil)
	h.Write([]byte(peerID))
	tsB := make([]byte, 8)
	binary.BigEndian.PutUint64(tsB, uint64(ts.Unix()))
	h.Write(tsB)
	for _, v := range []float64{actualWs, ratedWs, contractedWs} {
		b := make([]byte, 8)
		// Ws auf mWs (Milliwattsekunden) runden für Ganzzahl-Repr.
		binary.BigEndian.PutUint64(b, uint64(math.Round(v*1000)))
		h.Write(b)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// =============================================================================
//  ActiveTrade – ein laufender Handel mit Fluss-Quote
// =============================================================================

// ActiveTrade beschreibt einen laufenden Handel.
// QuoteWs ist die vereinbarte Menge Ws pro Sekunde die dieser Trade
// bei ausreichendem Fluss beansprucht. Bei Unterdeckung wird proportional
// gekürzt.
type ActiveTrade struct {
	TradeID   string    `json:"trade_id"`
	QuoteWs   float64   `json:"quote_ws"`   // vereinbarte Ws/s (= Watt × 1s)
	BuyerID   string    `json:"buyer_id"`
	SellerID  string    `json:"seller_id"`
	StartedAt time.Time `json:"started_at"`
	EndsAt    time.Time `json:"ends_at"` // Nullzeit = unbefristet
	RouteHash [32]byte  `json:"route_hash"`
}

// =============================================================================
//  SecondAccounting – buchhalterisches Ergebnis einer Sekunde
// =============================================================================

// SecondAccounting beschreibt wie die actual_ws einer Sekunde auf die
// aktiven Trades verteilt wurden.
type SecondAccounting struct {
	Timestamp    time.Time            `json:"ts"`
	ActualWs     float64              `json:"actual_ws"`
	ContractedWs float64              `json:"contracted_ws"`
	OpenWs       float64              `json:"open_ws"`
	// Zuweisung pro Trade (tradeID → zugewiesene Ws)
	Allocations  map[string]float64   `json:"allocations"`
}

// allocate verteilt actual_ws proportional auf alle aktiven Trades.
// Wenn actual_ws < Summe aller Quoten: jeder bekommt proportionalen Anteil.
// Wenn actual_ws > Summe aller Quoten: contracted_ws = Quoten-Summe, open_ws = Rest.
func allocate(actualWs float64, trades map[string]*ActiveTrade) SecondAccounting {
	acc := SecondAccounting{
		Timestamp:   time.Now().Truncate(time.Second),
		ActualWs:    actualWs,
		Allocations: make(map[string]float64, len(trades)),
	}
	if len(trades) == 0 || actualWs <= 0 {
		acc.OpenWs = actualWs
		return acc
	}

	// Gesamtquote aller aktiven Trades
	totalQuote := 0.0
	for _, t := range trades {
		totalQuote += t.QuoteWs
	}

	if totalQuote <= 0 {
		acc.OpenWs = actualWs
		return acc
	}

	if actualWs >= totalQuote {
		// Alle Trades bekommen ihre volle Quote
		for id, t := range trades {
			acc.Allocations[id] = t.QuoteWs
			acc.ContractedWs += t.QuoteWs
		}
		acc.OpenWs = actualWs - acc.ContractedWs
	} else {
		// Unterdeckung: proportionale Kürzung
		ratio := actualWs / totalQuote
		for id, t := range trades {
			alloc := t.QuoteWs * ratio
			acc.Allocations[id] = alloc
			acc.ContractedWs += alloc
		}
		acc.OpenWs = 0
	}

	return acc
}

// =============================================================================
//  FlowStatus (API-Antwort)
// =============================================================================

type FlowStatus struct {
	TrafoPeerID  string    `json:"trafo_peer_id"`
	RatedWs      float64   `json:"rated_ws"`       // Nennkapazität pro Sekunde
	LastActualWs float64   `json:"last_actual_ws"` // letzter Messwert
	ContractedWs float64   `json:"contracted_ws"`  // aktuell kontrahiert
	OpenWs       float64   `json:"open_ws"`        // verfügbar für neue Trades
	ActiveTrades int       `json:"active_trades"`
	Direction    string    `json:"direction"`
	LatestCertTS time.Time `json:"latest_cert_ts"`
	CertAgeS     float64   `json:"cert_age_s"`
	Healthy      bool      `json:"healthy"`
	Alarm        string    `json:"alarm,omitempty"`
}

// RouteFlowCheck – Ergebnis der Route-Validierung für einen Trafo
type RouteFlowCheck struct {
	TrafoPeerID  string
	HasCert      bool
	LastActualWs float64
	ContractedWs float64
	OpenWs       float64
	CertAge      time.Duration
	Sufficient   bool   // open_ws >= requestedWs oder Fallback
	Fallback     bool   // kein Fundus-Trafo → Ansatz-2
	Error        string
}

// =============================================================================
//  FlowManager – Kernkomponente
// =============================================================================

// FlowManager verwaltet das sekündliche Energiefluss-Accounting.
type FlowManager struct {
	mu      sync.RWMutex
	p2p     P2PAdapter
	privKey ed25519.PrivateKey
	pubKey  ed25519.PublicKey
	peerID  string
	ratedWs float64 // Nennkapazität in Ws/s (= Watt)
	dbPath  string  // Backup-Pfad für aktive Trades

	activeTrades map[string]*ActiveTrade
	latestCert   *EnergyFlowCert
	remoteCerts  map[string]*EnergyFlowCert // andere Trafos

	log *zap.Logger
}

// NewFlowManager erstellt einen neuen FlowManager.
// ratedKW: Nennleistung in kW (wird intern zu Ws/s = W umgerechnet)
func NewFlowManager(
	p2p P2PAdapter,
	privKey ed25519.PrivateKey,
	peerID string,
	ratedKW float64,
	dbPath string,
	log *zap.Logger,
) *FlowManager {
	if ratedKW <= 0 {
		ratedKW = 400
	}
	return &FlowManager{
		p2p:          p2p,
		privKey:      privKey,
		pubKey:       privKey.Public().(ed25519.PublicKey),
		peerID:       peerID,
		ratedWs:      ratedKW * 1000, // kW → W = Ws/s
		dbPath:       dbPath,
		activeTrades: make(map[string]*ActiveTrade),
		remoteCerts:  make(map[string]*EnergyFlowCert),
		log:          log,
	}
}

// Run startet Hintergrundaufgaben.
func (fm *FlowManager) Run(ctx context.Context) {
	fm.p2p.SetTopicHandler(TopicFlow, fm.handleRemoteCert)
	go fm.loadFromDisk()
	go fm.runExpiredTradePruner(ctx)
	fm.log.Info("FlowManager gestartet",
		zap.String("trafo",    fm.peerID),
		zap.Float64("rated_ws", fm.ratedWs),
	)
}

// =============================================================================
//  Sekündliche Cert-Ausstellung
// =============================================================================

// IssueCert wird vom Smartmeter-Reader aufgerufen (1 Hz).
// wattNow: aktuelle Momentanleistung in Watt → entspricht Ws in dieser Sekunde.
func (fm *FlowManager) IssueCert(ctx context.Context, wattNow, voltageV, freqHz float64) (*EnergyFlowCert, error) {
	ts := time.Now().Truncate(time.Second)

	// Sekunden-Accounting: actual_ws = wattNow × 1s
	actualWs := wattNow // Watt × 1s = Ws

	fm.mu.RLock()
	trades := make(map[string]*ActiveTrade, len(fm.activeTrades))
	for id, t := range fm.activeTrades {
		trades[id] = t
	}
	fm.mu.RUnlock()

	sec := allocate(actualWs, trades)
	contracted := sec.ContractedWs
	open       := sec.OpenWs

	commitment := buildFlowCommitment(fm.peerID, ts, actualWs, fm.ratedWs, contracted)
	cert := &EnergyFlowCert{
		TrafoPeerID:  fm.peerID,
		Timestamp:    ts,
		ActualWs:     actualWs,
		RatedWs:      fm.ratedWs,
		Direction:    flowDirection(wattNow),
		VoltageV:     voltageV,
		FrequencyHz:  freqHz,
		ContractedWs: contracted,
		OpenWs:       open,
		ActiveTrades: len(trades),
		Commitment:   commitment,
		Signature:    ed25519.Sign(fm.privKey, commitment[:]),
	}

	fm.mu.Lock()
	fm.latestCert = cert
	fm.mu.Unlock()

	data, _ := json.Marshal(cert)
	dhtKey := fmt.Sprintf("%s%s/%d", DHTFlowCert, fm.peerID, ts.Unix())
	if err := fm.p2p.DHTput(ctx, dhtKey, data); err != nil {
		fm.log.Debug("DHT-FlowCert-Publish fehlgeschlagen", zap.Error(err))
	}
	fm.p2p.Publish(ctx, TopicFlow, data)

	return cert, nil
}

func flowDirection(wattNow float64) string {
	switch {
	case wattNow > 10:
		return "import"
	case wattNow < -10:
		return "export"
	default:
		return "balanced"
	}
}

// =============================================================================
//  Trade-Accounting
// =============================================================================

// RegisterTrade fügt einen Trade zum Accounting hinzu.
// quoteWs: gewünschte Ws pro Sekunde (= vereinbarte Watt-Leistung).
// Kein Prüfen ob genug "frei" ist – der Trade bekommt was fließt.
// open_ws im Cert zeigt was noch unkontrahiert ist.
func (fm *FlowManager) RegisterTrade(
	tradeID, buyerID, sellerID string,
	quoteWs float64, // gewünschte Ws/s (= Watt)
	endsAt time.Time,
	routeHash [32]byte,
) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if _, exists := fm.activeTrades[tradeID]; exists {
		return fmt.Errorf("trade %s bereits registriert", tradeID)
	}
	if quoteWs <= 0 {
		return fmt.Errorf("quoteWs muss > 0 sein")
	}

	fm.activeTrades[tradeID] = &ActiveTrade{
		TradeID:   tradeID,
		QuoteWs:   quoteWs,
		BuyerID:   buyerID,
		SellerID:  sellerID,
		StartedAt: time.Now(),
		EndsAt:    endsAt,
		RouteHash: routeHash,
	}

	fm.log.Info("Trade ins Flow-Accounting eingebucht",
		zap.String("trade_id",  tradeID),
		zap.Float64("quote_ws", quoteWs),
		zap.Int("active_trades", len(fm.activeTrades)),
	)

	go fm.saveToDisk()
	return nil
}

// UnregisterTrade entfernt einen Trade aus dem Accounting.
func (fm *FlowManager) UnregisterTrade(tradeID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	t, ok := fm.activeTrades[tradeID]
	if !ok {
		return fmt.Errorf("trade %s nicht im Accounting", tradeID)
	}
	delete(fm.activeTrades, tradeID)

	fm.log.Info("Trade aus Flow-Accounting ausgebucht",
		zap.String("trade_id",  tradeID),
		zap.Float64("quote_ws", t.QuoteWs),
		zap.Int("active_trades", len(fm.activeTrades)),
	)

	go fm.saveToDisk()
	return nil
}

// =============================================================================
//  Route-Validierung
// =============================================================================

// ValidateRoute prüft ob alle Trafos auf einer Route erreichbar sind
// und das letzte Cert nicht zu alt ist.
// Es gibt kein "Reservieren" – wir prüfen nur ob der Trafo online ist
// und ungefähr genug open_ws hat (informativer Check, nicht bindend).
func (fm *FlowManager) ValidateRoute(
	ctx context.Context,
	trafoPeerIDs []string,
	requestedWs float64,
) ([]RouteFlowCheck, error) {
	checks := make([]RouteFlowCheck, 0, len(trafoPeerIDs))

	for _, pid := range trafoPeerIDs {
		check := RouteFlowCheck{TrafoPeerID: pid}

		var cert *EnergyFlowCert
		if pid == fm.peerID {
			fm.mu.RLock()
			cert = fm.latestCert
			fm.mu.RUnlock()
		} else {
			var err error
			cert, err = fm.GetRemoteCert(ctx, pid)
			if err != nil {
				check.Error    = err.Error()
				check.Fallback = true
				check.Sufficient = true // kein Fundus-Node → nicht blockieren
				checks = append(checks, check)
				continue
			}
		}

		if cert == nil {
			check.Fallback  = true
			check.Sufficient = true
			check.Error    = "kein Cert verfügbar"
			checks = append(checks, check)
			continue
		}

		check.HasCert      = true
		check.LastActualWs = cert.ActualWs
		check.ContractedWs = cert.ContractedWs
		check.OpenWs        = cert.OpenWs
		check.CertAge       = time.Since(cert.Timestamp)
		// "Sufficient" = informativer Hinweis: open_ws im letzten Cert ≥ Quote
		// Nicht bindend – final entscheidet was tatsächlich fließt
		check.Sufficient = cert.OpenWs >= requestedWs || cert.ActualWs >= requestedWs
		checks = append(checks, check)
	}
	return checks, nil
}

// =============================================================================
//  Remote-Cert-Lookup
// =============================================================================

func (fm *FlowManager) GetRemoteCert(ctx context.Context, peerID string) (*EnergyFlowCert, error) {
	fm.mu.RLock()
	cert, ok := fm.remoteCerts[peerID]
	fm.mu.RUnlock()

	if ok && time.Since(cert.Timestamp) < CertTTL {
		return cert, nil
	}

	ts := time.Now().Truncate(time.Second)
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("%s%s/%d", DHTFlowCert, peerID, ts.Unix()-int64(i))
		data, err := fm.p2p.DHTget(ctx, key)
		if err != nil {
			continue
		}
		var c EnergyFlowCert
		if json.Unmarshal(data, &c) == nil {
			fm.mu.Lock()
			fm.remoteCerts[peerID] = &c
			fm.mu.Unlock()
			return &c, nil
		}
	}
	n := len(peerID)
	if n > 12 {
		n = 12
	}
	return nil, fmt.Errorf("kein Flow-Cert für %s… (kein Fundus-Trafo)", peerID[:n])
}

func (fm *FlowManager) handleRemoteCert(data []byte) {
	var cert EnergyFlowCert
	if json.Unmarshal(data, &cert) != nil || cert.TrafoPeerID == fm.peerID {
		return
	}
	fm.mu.Lock()
	if ex := fm.remoteCerts[cert.TrafoPeerID]; ex == nil || cert.Timestamp.After(ex.Timestamp) {
		fm.remoteCerts[cert.TrafoPeerID] = &cert
	}
	fm.mu.Unlock()
}

// =============================================================================
//  Status
// =============================================================================

func (fm *FlowManager) Status() FlowStatus {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	s := FlowStatus{
		TrafoPeerID:  fm.peerID,
		RatedWs:      fm.ratedWs,
		ActiveTrades: len(fm.activeTrades),
	}
	if fm.latestCert != nil {
		s.LastActualWs = fm.latestCert.ActualWs
		s.ContractedWs = fm.latestCert.ContractedWs
		s.OpenWs        = fm.latestCert.OpenWs
		s.Direction    = fm.latestCert.Direction
		s.LatestCertTS = fm.latestCert.Timestamp
		s.CertAgeS     = time.Since(fm.latestCert.Timestamp).Seconds()
		s.Healthy      = time.Since(fm.latestCert.Timestamp) <= CertMaxAge
		if !s.Healthy {
			s.Alarm = fmt.Sprintf(
				"FlowCert veraltet: %.0fs (max %.0fs) – Smartmeter prüfen",
				s.CertAgeS, CertMaxAge.Seconds())
		}
	}
	return s
}

// =============================================================================
//  Persistenz
// =============================================================================

type flowBackup struct {
	Trades []*ActiveTrade `json:"trades"`
}

func (fm *FlowManager) saveToDisk() {
	if fm.dbPath == "" {
		return
	}
	fm.mu.RLock()
	trades := make([]*ActiveTrade, 0, len(fm.activeTrades))
	for _, t := range fm.activeTrades {
		trades = append(trades, t)
	}
	fm.mu.RUnlock()

	data, _ := json.Marshal(flowBackup{Trades: trades})
	tmp := fm.dbPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err == nil {
		os.Rename(tmp, fm.dbPath)
	}
}

func (fm *FlowManager) loadFromDisk() {
	if fm.dbPath == "" {
		return
	}
	data, err := os.ReadFile(fm.dbPath)
	if err != nil {
		return
	}
	var bak flowBackup
	if json.Unmarshal(data, &bak) != nil {
		return
	}
	now := time.Now()
	loaded := 0
	fm.mu.Lock()
	for _, t := range bak.Trades {
		if !t.EndsAt.IsZero() && t.EndsAt.Before(now) {
			continue
		}
		fm.activeTrades[t.TradeID] = t
		loaded++
	}
	fm.mu.Unlock()
	if loaded > 0 {
		fm.log.Info("Flow-Accounting aus Disk geladen",
			zap.Int("trades", loaded))
	}
}

func (fm *FlowManager) runExpiredTradePruner(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fm.pruneExpired()
		}
	}
}

func (fm *FlowManager) pruneExpired() {
	now := time.Now()
	fm.mu.Lock()
	for id, t := range fm.activeTrades {
		if !t.EndsAt.IsZero() && t.EndsAt.Before(now) {
			delete(fm.activeTrades, id)
			fm.log.Info("Abgelaufenen Trade ausgebucht", zap.String("id", id))
		}
	}
	fm.mu.Unlock()
	go fm.saveToDisk()
}

// Rückwärtskompatibilität: CapacityManager-Alias damit bestehende API-Aufrufe
// ohne Umbenennung weiter funktionieren.
type CapacityManager = FlowManager

func NewCapacityManager(
	p2p P2PAdapter,
	privKey ed25519.PrivateKey,
	peerID string,
	ratedKW float64,
	dbPath string,
	log *zap.Logger,
) *FlowManager {
	return NewFlowManager(p2p, privKey, peerID, ratedKW, dbPath, log)
}

// CapacityStatus-Alias für Grid-API
type CapacityStatus = FlowStatus
