package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
//  GossipSub-Topics
// =============================================================================

const (
	// TopicProfiles: Nodes kündigen ihr Profil an (alle 5 Min)
	TopicProfiles = "fundus.topology.profiles"

	// DHT-Key für eigenes Profil
	DHTNamespaceProfile = "/fundus/topology/"

	// Wie oft das eigene Profil neu published wird
	ProfileBroadcastInterval = 5 * time.Minute

	// Wie lange ein Profil ohne Update als veraltet gilt
	ProfileStaleness = 30 * time.Minute
)

// =============================================================================
//  P2P-Adapter
// =============================================================================

// P2PAdapter ist das Interface zum P2P-Layer.
type P2PAdapter interface {
	ID() interface{ String() string }
	Publish(ctx context.Context, topic string, data []byte) error
	SetTopicHandler(topic string, handler func(data []byte))
	DHTput(ctx context.Context, key string, value []byte) error
	DHTget(ctx context.Context, key string) ([]byte, error)
}

// =============================================================================
//  Manager
// =============================================================================

// Manager verwaltet das eigene Profil und den Topologie-Graphen.
type Manager struct {
	own     *NodeProfile       // eigenes Profil
	graph   *Graph             // bekannte Topologie
	p2p     P2PAdapter
	dist    *DistanceCalculator
	log     *zap.Logger
}

// New erstellt einen Topologie-Manager.
func New(own *NodeProfile, p2p P2PAdapter, gmapsAPIKey string, log *zap.Logger) *Manager {
	m := &Manager{
		own:   own,
		graph: NewGraph(),
		p2p:   p2p,
		dist:  NewDistanceCalculator(gmapsAPIKey),
		log:   log,
	}
	// Eigenes Profil in den Graphen aufnehmen
	m.graph.AddOrUpdate(own)
	return m
}

// Run startet den Manager (blockiert bis ctx abgebrochen).
func (m *Manager) Run(ctx context.Context) {
	// Eingehende Profile verarbeiten
	m.p2p.SetTopicHandler(TopicProfiles, m.handleIncomingProfile)

	// Eigenes Profil sofort und dann periodisch bekannt machen
	go m.broadcastLoop(ctx)

	<-ctx.Done()
}

// broadcastLoop veröffentlicht das eigene Profil regelmäßig.
func (m *Manager) broadcastLoop(ctx context.Context) {
	m.publishOwnProfile(ctx)

	ticker := time.NewTicker(ProfileBroadcastInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.publishOwnProfile(ctx)
		}
	}
}

// publishOwnProfile sendet das eigene Profil per GossipSub + DHT.
func (m *Manager) publishOwnProfile(ctx context.Context) {
	m.own.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(m.own)
	if err != nil {
		m.log.Warn("Profil-Marshal fehlgeschlagen", zap.Error(err))
		return
	}

	if err := m.p2p.Publish(ctx, TopicProfiles, data); err != nil {
		m.log.Debug("Profil-Broadcast fehlgeschlagen", zap.Error(err))
	}

	dhtKey := DHTNamespaceProfile + m.own.PeerID
	if err := m.p2p.DHTput(ctx, dhtKey, data); err != nil {
		m.log.Debug("Profil-DHT fehlgeschlagen", zap.Error(err))
	}

	m.log.Debug("Profil published",
		zap.String("type",     string(m.own.Type)),
		zap.String("voltage",  string(m.own.Voltage)),
		zap.Int("connections", len(m.own.Connections)),
	)
}

// handleIncomingProfile verarbeitet ein eingehendes Node-Profil.
func (m *Manager) handleIncomingProfile(data []byte) {
	var p NodeProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	if p.PeerID == "" { return }

	// Veraltete Profile ignorieren
	if time.Since(p.UpdatedAt) > ProfileStaleness {
		return
	}

	m.graph.AddOrUpdate(&p)
	m.log.Debug("Profil empfangen",
		zap.String("peer",    p.PeerID[:min(8, len(p.PeerID))]+"…"),
		zap.String("type",    string(p.Type)),
		zap.String("voltage", string(p.Voltage)),
	)
}

// =============================================================================
//  Verbindungen verwalten
// =============================================================================

// AddConnection fügt eine bekannte Verbindung zum eigenen Profil hinzu.
// Berechnet die Distanz automatisch per GMaps oder Luftlinie.
func (m *Manager) AddConnection(ctx context.Context, peerID string,
	peerLat, peerLon float64, voltage VoltageLevel, peerWallet string) error {

	distM, source, err := m.dist.CalculateDistance(
		ctx,
		m.own.Lat, m.own.Lon,
		peerLat, peerLon,
	)
	if err != nil {
		return fmt.Errorf("topology: Distanzberechnung: %w", err)
	}

	conn := Connection{
		PeerID:         peerID,
		DistanceM:      distM,
		DistanceSource: source,
		Voltage:        voltage,
	}

	// Doppeleintrag verhindern
	for i, c := range m.own.Connections {
		if c.PeerID == peerID {
			m.own.Connections[i] = conn
			m.publishOwnProfile(ctx)
			return nil
		}
	}

	m.own.Connections = append(m.own.Connections, conn)
	m.publishOwnProfile(ctx)

	m.log.Info("Verbindung hinzugefügt",
		zap.String("peer",   peerID[:min(8, len(peerID))]+"…"),
		zap.Float64("distM", distM),
		zap.String("source", source),
	)
	return nil
}

// RemoveConnection entfernt eine Verbindung.
func (m *Manager) RemoveConnection(ctx context.Context, peerID string) {
	conns := make([]Connection, 0, len(m.own.Connections))
	for _, c := range m.own.Connections {
		if c.PeerID != peerID {
			conns = append(conns, c)
		}
	}
	m.own.Connections = conns
	m.publishOwnProfile(ctx)
}

// =============================================================================
//  Routing-Schnittstelle
// =============================================================================

// FindRoute berechnet den optimalen Handelspfad zu einem anderen Node.
func (m *Manager) FindRoute(ctx context.Context, toPeerID string, priceAmount float64) (*GridRoute, error) {
	// Ziel-Profil aus DHT nachladen falls unbekannt
	if _, ok := m.graph.Get(toPeerID); !ok {
		m.loadProfileFromDHT(ctx, toPeerID)
	}
	return m.graph.FindRoute(m.own.PeerID, toPeerID, priceAmount)
}

// loadProfileFromDHT versucht ein Profil aus dem DHT zu laden.
func (m *Manager) loadProfileFromDHT(ctx context.Context, peerID string) {
	data, err := m.p2p.DHTget(ctx, DHTNamespaceProfile+peerID)
	if err != nil { return }

	var p NodeProfile
	if json.Unmarshal(data, &p) == nil {
		m.graph.AddOrUpdate(&p)
	}
}

// =============================================================================
//  Getter
// =============================================================================

func (m *Manager) OwnProfile() *NodeProfile     { return m.own }
func (m *Manager) Graph()       *Graph           { return m.graph }
func (m *Manager) KnownNodes()  int              { return m.graph.NodeCount() }

// UpdateFee aktualisiert die Durchleitungsgebühr (für Trafostationen).
func (m *Manager) UpdateFee(feePercent float64) {
	m.own.TransitFeePercent = feePercent
}

func min(a, b int) int {
	if a < b { return a }
	return b
}
