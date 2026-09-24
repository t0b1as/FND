package p2p

import (
	"net"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	record "github.com/libp2p/go-libp2p-record"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/config"
	"github.com/fundus/node/internal/storage"
)

const (
	FundusProtocol = protocol.ID("/fundus/1.0.0")
	SyncProtocol   = "/fundus/sync/1.0.0" // Bestands-Abgleich beim Peer-Connect
	KeyDirProtocol = "/fundus/keydir-req/1.0.0" // direkter PubKey-Abruf beim Peer
	MsgDeliverProtocol = "/fundus/msg-deliver/1.0.0" // gerichtete Nachrichten-Zustellung
	SearchResultProtocol = "/fundus/search-result/1.0.0" // Rueckkanal fuer Such-Treffer
	FileSearchResultProtocol = "/fundus/file-search-result/1.0.0" // Rueckkanal fuer Datei-Treffer
	JobSearchResultProtocol = "/fundus/job-search-result/1.0.0" // Rueckkanal fuer Job-Treffer

	TopicListings     = "fundus.listings"
	TopicEnergy       = "fundus.energy"
	TopicCertificate  = "fundus.certificate"
	TopicJobs         = "fundus.jobs"
	TopicPartner      = "fundus.partner"
	TopicPartnerSearch = "fundus.partner.search"
	TopicMailbox      = "fundus.mailbox"
	TopicContacts     = "fundus.contacts"
	TopicEmailDir     = "fundus.emaildir"
	TopicKeyDir       = "fundus.keydir"
	TopicUpdate       = "fundus.update"  // Signierte Update-Manifeste
	TopicSearch       = "fundus.search"  // Netzwerkweite Such-Pings
	TopicFileSearch   = "fundus.filesearch" // Suche nach geteilten Dateien
	TopicJobSearch    = "fundus.jobsearch"  // Netzwerkweite Job-Such-Pings (Gebote/Gesuche)

	mdnsServiceTag = "fundus-marketplace"
	rendezvous     = "fundus-marketplace-v1"
)

// Node ist der Kern des P2P-Netzwerks.
type Node struct {
	host   host.Host
	dht    *dht.IpfsDHT
	pubsub *pubsub.PubSub
	topics map[string]*pubsub.Topic
	subs   map[string]*pubsub.Subscription
	baseCtx context.Context // für spätes joinTopic (z.B. persönliche Msg-Topics)
	msgLoopsStarted bool    // true sobald die initialen handleMessages-Loops laufen

	// topicHandlers: custom handler pro Topic (ersetzt Store-Replikation)
	topicHandlers   map[string]func([]byte)
	topicHandlersMu sync.RWMutex

	store  *storage.Store
	cfg    *config.Config
	log    *zap.Logger

	chain  ChainBridge // optionale Chain-Anbindung (Multi-Node-Sync), nil bis AttachChain

	mu     sync.RWMutex
	peers  map[peer.ID]time.Time
}

// loadOrCreateIdentity lädt den persistenten libp2p-Host-Key aus
// DataDir/identity.key oder erzeugt ihn beim ersten Start. Dadurch bleibt die
// Peer-ID über Neustarts stabil — wichtig für Eigentümer-Checks und
// Signaturen (sonst gehoeren alte Angebote nach Neustart "niemandem" mehr).
func loadOrCreateIdentity(dataDir string, log *zap.Logger) (crypto.PrivKey, error) {
	keyPath := filepath.Join(dataDir, "identity.key")

	if data, err := os.ReadFile(keyPath); err == nil && len(data) > 0 {
		priv, err := crypto.UnmarshalPrivateKey(data)
		if err == nil {
			log.Info("Persistente Identität geladen", zap.String("path", keyPath))
			return priv, nil
		}
		log.Warn("identity.key unlesbar – erzeuge neue", zap.Error(err))
	}

	// Neuen Ed25519-Key erzeugen und speichern
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("key erzeugen: %w", err)
	}
	raw, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("key serialisieren: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("dataDir anlegen: %w", err)
	}
	if err := os.WriteFile(keyPath, raw, 0o600); err != nil {
		return nil, fmt.Errorf("key speichern: %w", err)
	}
	log.Info("Neue persistente Identität erzeugt", zap.String("path", keyPath))
	return priv, nil
}

// NewNode erstellt und startet einen vollständigen P2P-Node.
func NewNode(ctx context.Context, cfg *config.Config, store *storage.Store, log *zap.Logger) (*Node, error) {
	// -------------------------------------------------------------------------
	//  libp2p Host aufbauen
	// -------------------------------------------------------------------------
	// Persistente Identität laden/erzeugen (stabile Peer-ID über Neustarts)
	privKey, err := loadOrCreateIdentity(cfg.DataDir, log)
	if err != nil {
		return nil, fmt.Errorf("identity laden: %w", err)
	}

	opts := []libp2p.Option{
		// Persistente Identität (sonst neue Peer-ID bei jedem Start)
		libp2p.Identity(privKey),
		// TCP + QUIC auf konfigurierbarem Port
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/%s/tcp/%d", cfg.P2PHost, cfg.P2PPort),
			fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", cfg.P2PHost, cfg.P2PPort),
			// IPv6 wenn verfügbar
			fmt.Sprintf("/ip6/::/tcp/%d", cfg.P2PPort),
			fmt.Sprintf("/ip6/::/udp/%d/quic-v1", cfg.P2PPort),
		),

		// NAT-Traversal Stack
		// 1. AutoNAT: erkennt ob wir von außen erreichbar sind
		libp2p.EnableNATService(),
		// 2. UPnP/NAT-PMP: versucht Port automatisch im Router freizuschalten
		libp2p.NATPortMap(),
		// 3. Hole Punching (DCUtR): direkte UDP-Verbindung durch NAT
		//    Funktioniert bei ~75% aller Heimrouter
		libp2p.EnableHolePunching(),
		// 4. Circuit Relay: Fallback wenn Hole Punching scheitert
		libp2p.EnableRelay(),
		libp2p.EnableAutoRelayWithStaticRelays(relaysFor(cfg)),

		// Black-Hole-Erkennung AUS: libp2p sperrt nach vielen gescheiterten
		// UDP-/IPv6-Dials (z.B. zu öffentlichen DHT-Bootstrap-Nodes ohne
		// IPv6-Route) ALLE weiteren UDP/IPv6-Dials – auch zu unseren eigenen
		// Peers ("dial refused because of black hole"). Bei Mobilfunk/CGNAT ist
		// QUIC oft der einzige Weg, deshalb darf er nicht gesperrt werden.
		libp2p.UDPBlackHoleSuccessCounter(nil),
		libp2p.IPv6BlackHoleSuccessCounter(nil),

		// Loopback-Adressen nicht an andere Peers announcen (erzeugte bei der
		// Gegenseite nur "dial to self attempted"); zusätzlich konfigurierte
		// öffentliche Adressen (FUNDUS_P2P_ANNOUNCE) anhängen.
		libp2p.AddrsFactory(announceFilter(cfg)),
	}
	if cfg.P2PRelayService {
		// Dieser Node dient anderen (z.B. hinter Mobilfunk-CGNAT) als Relay.
		// Ohne Limits: die Standard-Limits (2 min / 128 KiB) sind nur für den
		// Verbindungsaufbau gedacht und würden einen Tunnel sofort kappen.
		// Aktiv wird der Dienst nur, wenn AutoNAT den Node als öffentlich
		// erreichbar erkennt – hinter NAT schadet die Option nicht.
		opts = append(opts, libp2p.EnableRelayService(relayv2.WithInfiniteLimits()))
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	// -------------------------------------------------------------------------
	//  DHT – adaptiver Modus basierend auf Erreichbarkeit
	// -------------------------------------------------------------------------
	//
	// DHT-Server:  Node ist von außen erreichbar → nimmt aktiv am Routing teil
	// DHT-Client:  Node ist hinter Symmetric NAT → nutzt andere als Server
	//
	// AutoNAT prüft nach dem Bootstrap ob wir erreichbar sind.
	// Wir starten als Server; AutoNAT degradiert bei Bedarf automatisch.
	kadDHT, err := dht.New(ctx, h,
		dht.Mode(dht.ModeAuto), // ← AutoNAT entscheidet: Server oder Client
		dht.BootstrapPeers(dht.GetDefaultBootstrapPeerAddrInfos()...),
	)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("create DHT: %w", err)
	}

	// /fundus/-Namespace-Validator NACHTRAEGLICH ergaenzen. Direkt in dht.New
	// als Option wuerde es die eingebauten pk/ipns-Validatoren des /ipfs-
	// Prefix verdraengen ("protocol prefix /ipfs" Crash). Stattdessen das
	// bestehende Validator-Set erweitern. Ohne diesen lehnt der DHT PutValue
	// mit "invalid record keytype" ab. Fundus-Werte sind selbst signiert/
	// verschluesselt, daher akzeptiert der Validator sie ohne Pruefung.
	if nsval, ok := kadDHT.Validator.(record.NamespacedValidator); ok {
		nsval["fundus"] = fundusValidator{}
	} else {
		log.Warn("DHT-Validator nicht erweiterbar, /fundus/ PutValue evtl. blockiert")
	}

	if err := kadDHT.Bootstrap(ctx); err != nil {
		log.Warn("DHT bootstrap warning", zap.Error(err))
	}

	// -------------------------------------------------------------------------
	//  GossipSub (Pub/Sub für Marktplatz-Events)
	// -------------------------------------------------------------------------
	ps, err := pubsub.NewGossipSub(ctx, h,
		pubsub.WithMessageSigning(true),
		pubsub.WithStrictSignatureVerification(true),
	)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("create pubsub: %w", err)
	}

	node := &Node{
		host:   h,
		dht:    kadDHT,
		pubsub: ps,
		baseCtx: ctx,
		topics:        make(map[string]*pubsub.Topic),
		topicHandlers: make(map[string]func([]byte)),
		subs:   make(map[string]*pubsub.Subscription),
		store:  store,
		cfg:    cfg,
		log:    log,
		peers:  make(map[peer.ID]time.Time),
	}

	// -------------------------------------------------------------------------
	//  Alle Topics abonnieren
	// -------------------------------------------------------------------------
	for _, topicName := range []string{
		TopicListings, TopicEnergy, TopicCertificate, TopicJobs,
		TopicPartner, TopicPartnerSearch, TopicUpdate, TopicSearch,
		TopicFileSearch, TopicJobSearch,
		TopicMailbox, TopicContacts, TopicEmailDir, TopicKeyDir,
	} {
		if err := node.joinTopic(ctx, topicName); err != nil {
			log.Warn("Could not join topic", zap.String("topic", topicName), zap.Error(err))
		}
	}

	// -------------------------------------------------------------------------
	//  mDNS (automatische LAN-Discovery ohne Bootstrap nötig)
	// -------------------------------------------------------------------------
	mdnsService := mdns.NewMdnsService(h, mdnsServiceTag, &mdnsNotifee{node: node, log: log})
	if err := mdnsService.Start(); err != nil {
		log.Warn("mDNS start failed (LAN discovery disabled)", zap.Error(err))
	}

	// -------------------------------------------------------------------------
	//  Eingehende Nachrichten verarbeiten (goroutine pro Topic)
	// -------------------------------------------------------------------------
	for topicName, sub := range node.subs {
		go node.handleMessages(ctx, topicName, sub)
	}
	node.mu.Lock()
	node.msgLoopsStarted = true // ab jetzt startet joinTopic seine Loops selbst
	node.mu.Unlock()

	// -------------------------------------------------------------------------
	//  Peer-Discovery via DHT im Hintergrund
	// -------------------------------------------------------------------------
	go node.discoverPeers(ctx)
	// -------------------------------------------------------------------------
	//  Peer-Replikation: Daten für bis zu 5 Peers cachen
	// -------------------------------------------------------------------------
	go node.runReplication(ctx)

	// -------------------------------------------------------------------------
	//  Periodischer Chain-Sync: fehlende Blöcke von Peers nachziehen
	//  (fängt verlorene Live-Broadcasts ab)
	// -------------------------------------------------------------------------
	go node.runChainSync(ctx)

	// Sync-Responder: beantwortet Bestands-Anfragen neuer Peers
	node.registerSyncResponder()

	return node, nil
}

// Bootstrap kontaktiert bekannte Bootstrap-Nodes aus der Konfiguration.
func (n *Node) Bootstrap(ctx context.Context) error {
	if len(n.cfg.BootstrapPeers) == 0 {
		n.log.Info("No bootstrap peers configured – starting standalone")
		return nil
	}

	var wg sync.WaitGroup
	var errs []error
	var mu sync.Mutex

	for _, addrStr := range n.cfg.BootstrapPeers {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()

			ma, err := multiaddr.NewMultiaddr(addr)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("invalid multiaddr %s: %w", addr, err))
				mu.Unlock()
				return
			}

			peerInfo, err := peer.AddrInfoFromP2pAddr(ma)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}

			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			if err := n.host.Connect(connectCtx, *peerInfo); err != nil {
				n.log.Warn("Bootstrap peer unreachable",
					zap.String("addr", addr),
					zap.Error(err))
				return
			}

			n.log.Info("Connected to bootstrap peer", zap.String("peer", peerInfo.ID.String()))
			n.host.ConnManager().Protect(peerInfo.ID, "fundus-bootstrap")
			n.trackPeer(peerInfo.ID)
		}(addrStr)
	}

	wg.Wait()

	// Bootstrap-Peers dauerhaft halten: bei Abriss (Mobilfunk, Neustart der
	// Gegenseite) alle 30 s neu verbinden. Der Node hinter CGNAT baut so die
	// ausgehende Verbindung nach Hause selbst wieder auf; über sie laufen
	// dann auch Streams in Gegenrichtung (Tunnel).
	go n.keepBootstrapPeers(ctx)

	if len(errs) > 0 && len(errs) == len(n.cfg.BootstrapPeers) {
		return fmt.Errorf("all %d bootstrap peers failed", len(errs))
	}
	return nil
}

// Publish veröffentlicht eine Nachricht auf einem Topic.
func (n *Node) Publish(ctx context.Context, topicName string, data []byte) error {
	n.mu.RLock()
	topic, ok := n.topics[topicName]
	n.mu.RUnlock()
	if !ok {
		// Topic noch nicht bekannt (z.B. das persönliche Topic eines Empfängers,
		// an den wir zum ersten Mal senden). Bei GossipSub muss man einem Topic
		// beitreten, um darauf zu publishen — aber nicht zwingend abonnieren.
		// Wir treten bei UND abonnieren (joinTopic macht beides), damit auch
		// Antworten/Zustellungen auf demselben Topic empfangen werden können.
		if err := n.joinTopic(ctx, topicName); err != nil {
			return fmt.Errorf("publish: join %s: %w", topicName, err)
		}
		n.mu.RLock()
		topic, ok = n.topics[topicName]
		n.mu.RUnlock()
		if !ok {
			return fmt.Errorf("publish: topic %s nach join nicht verfügbar", topicName)
		}
	}
	return topic.Publish(ctx, data)
}

// Peers gibt eine Liste aktuell bekannter Peer-IDs zurück.
func (n *Node) Peers() []peer.ID {
	n.mu.RLock()
	defer n.mu.RUnlock()
	ids := make([]peer.ID, 0, len(n.peers))
	for id := range n.peers {
		ids = append(ids, id)
	}
	return ids
}

// PeerDetail beschreibt einen verbundenen Peer mit seinen bekannten Adressen.
type PeerDetail struct {
	ID    string   `json:"id"`
	Addrs []string `json:"addrs"` // bekannte Multiaddrs aus dem Peerstore
	IPv4  string   `json:"ipv4"`  // erste nutzbare IPv4 (für einen HTTP-Link), sonst leer
}

// PeersDetailed liefert die verbundenen Peers samt ihrer im Peerstore bekannten
// Adressen. Die IPv4 wird für einen möglichen Web-Link herausgezogen — sie ist
// aber nur erreichbar, wenn der Peer im selben (nicht isolierten) Netz liegt.
func (n *Node) PeersDetailed() []PeerDetail {
	n.mu.RLock()
	ids := make([]peer.ID, 0, len(n.peers))
	for id := range n.peers {
		ids = append(ids, id)
	}
	n.mu.RUnlock()

	ps := n.host.Peerstore()
	out := make([]PeerDetail, 0, len(ids))
	for _, id := range ids {
		d := PeerDetail{ID: id.String()}
		for _, a := range ps.Addrs(id) {
			as := a.String()
			d.Addrs = append(d.Addrs, as)
			// IPv4 aus /ip4/<addr>/... ziehen, private wie öffentliche zulassen,
			// aber Loopback überspringen.
			if d.IPv4 == "" {
				if ip, ok := extractIPv4(as); ok && ip != "127.0.0.1" {
					d.IPv4 = ip
				}
			}
		}
		out = append(out, d)
	}
	return out
}

// extractIPv4 zieht die IPv4 aus einer Multiaddr wie "/ip4/192.168.1.5/tcp/4001".
func extractIPv4(multiaddr string) (string, bool) {
	parts := strings.Split(multiaddr, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "ip4" {
			return parts[i+1], true
		}
	}
	return "", false
}

// ID gibt die eigene Peer-ID zurück.
func (n *Node) ID() peer.ID {
	return n.host.ID()
}

// Addrs gibt die Multiaddrs zurück, unter denen dieser Node erreichbar ist.
func (n *Node) Addrs() []string {
	addrs := make([]string, 0)
	for _, addr := range n.host.Addrs() {
		addrs = append(addrs, fmt.Sprintf("%s/p2p/%s", addr, n.host.ID()))
	}
	return addrs
}

// Close fährt den Node sauber herunter.
func (n *Node) Close() error {
	n.dht.Close()
	return n.host.Close()
}

// =============================================================================
//  Interne Hilfsmethoden
// =============================================================================

func (n *Node) joinTopic(ctx context.Context, topicName string) error {
	topic, err := n.pubsub.Join(topicName)
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		topic.Close()
		return err
	}
	n.mu.Lock()
	n.topics[topicName] = topic
	n.subs[topicName]   = sub
	started := n.msgLoopsStarted
	n.mu.Unlock()
	n.log.Info("Joined topic", zap.String("topic", topicName))
	// Wenn die initialen Message-Loops schon laufen, für dieses NEU beigetretene
	// Topic sofort einen eigenen Loop starten — sonst wird seine Subscription nie
	// gelesen und Nachrichten kommen nicht an (Bug bei spät abonnierten Topics
	// wie den persönlichen Messenger-Topics).
	if started {
		go n.handleMessages(ctx, topicName, sub)
	}
	return nil
}

// SetTopicHandler registriert einen benutzerdefinierten Message-Handler für ein Topic.
func (n *Node) SetTopicHandler(topic string, handler func([]byte)) {
	n.topicHandlersMu.Lock()
	n.topicHandlers[topic] = handler
	n.topicHandlersMu.Unlock()

	// Das Topic auch tatsächlich abonnieren, sonst liefert GossipSub nichts an
	// den Handler (ein Handler ohne Subscription empfängt keine Nachrichten).
	n.mu.RLock()
	_, already := n.subs[topic]
	n.mu.RUnlock()
	if !already && n.baseCtx != nil {
		if err := n.joinTopic(n.baseCtx, topic); err != nil {
			n.log.Warn("SetTopicHandler: joinTopic fehlgeschlagen", zap.String("topic", topic), zap.Error(err))
		}
	}
}

func (n *Node) handleMessages(ctx context.Context, topicName string, sub *pubsub.Subscription) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // Shutdown
			}
			n.log.Warn("PubSub message error", zap.String("topic", topicName), zap.Error(err))
			continue
		}

		// Eigene Nachrichten ignorieren
		if msg.ReceivedFrom == n.host.ID() {
			continue
		}

		n.trackPeer(msg.ReceivedFrom)

		n.log.Debug("Message received",
			zap.String("topic", topicName),
			zap.String("from", msg.ReceivedFrom.String()),
			zap.Int("bytes", len(msg.Data)),
		)

		// Custom Handler hat Vorrang (z.B. für Update-Topic)
		n.topicHandlersMu.RLock()
		handler, hasHandler := n.topicHandlers[topicName]
		n.topicHandlersMu.RUnlock()

		if hasHandler {
			handler(msg.Data)
			continue
		}

		// Standard: An Storage weitergeben zur Replikation
		if err := n.store.HandleIncoming(topicName, msg.ReceivedFrom.String(), msg.Data); err != nil {
			n.log.Warn("Store incoming failed", zap.Error(err))
		}
	}
}

func (n *Node) discoverPeers(ctx context.Context) {
	routingDiscovery := drouting.NewRoutingDiscovery(n.dht)
	dutil.Advertise(ctx, routingDiscovery, rendezvous)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			discStart := time.Now()
			peerChan, err := routingDiscovery.FindPeers(ctx, rendezvous)
			if err != nil {
				n.log.Warn("Peer discovery error", zap.Error(err))
				continue
			}

			// Verbindungsversuche PARALLEL, nicht seriell: sonst blockiert jeder
			// nicht erreichbare Peer die ganze Schleife um sein 5s-Timeout (bei
			// mehreren toten Peers summiert sich das auf 20-25s und blockiert den
			// Node — was die Render-Calls/Seiten hängen lässt).
			var wg sync.WaitGroup
			connCount := 0
			for p := range peerChan {
				if p.ID == n.host.ID() {
					continue
				}
				connCount++
				wg.Add(1)
				go func(pi peer.AddrInfo) {
					defer wg.Done()
					// 1s Timeout: im LAN (Ping 5-10ms) ist ein Peer, der nicht in
					// 1s antwortet, praktisch nicht erreichbar. Kürzer = Node wird
					// bei toten Peers nicht ausgebremst.
					connectCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
					defer cancel()
					if err := n.host.Connect(connectCtx, pi); err == nil {
						n.trackPeer(pi.ID)
					}
				}(p)
			}
			wg.Wait()
			if d := time.Since(discStart); d > 1*time.Second {
				n.log.Warn("Peer-Discovery langsam", zap.Duration("dauer", d), zap.Int("peers_versucht", connCount))
			}
		}
	}
}

// runChainSync gleicht periodisch die Chain-Höhe mit allen bekannten Peers ab
// und zieht fehlende Blöcke nach. Das schließt die Lücke, die entsteht, wenn ein
// Live-Block-Broadcast (GossipSub) einen Peer nicht erreicht — z.B. weil er zum
// Sendezeitpunkt noch nicht im Topic-Mesh war oder erst danach verbunden hat.
// SyncChainFromPeer prüft Genesis + ist idempotent (zieht nur head>local), daher
// ist der periodische Aufruf günstig und sicher.
func (n *Node) runChainSync(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.mu.RLock()
			hasChain := n.chain != nil
			n.mu.RUnlock()
			if !hasChain {
				continue
			}
			for _, id := range n.Peers() {
				go n.SyncChainFromPeer(id.String())
			}
		}
	}
}

// runReplication sorgt dafür dass wir für bis zu PeersMax Peers Daten cachen.
func (n *Node) runReplication(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			peers := n.Peers()
			if err := n.store.UpdateReplicationTargets(peers, n.cfg.PeersMax); err != nil {
				n.log.Warn("Replication update failed", zap.Error(err))
			}
		}
	}
}

func (n *Node) trackPeer(id peer.ID) {
	n.mu.Lock()
	_, known := n.peers[id]
	n.peers[id] = time.Now()
	n.mu.Unlock()

	// Neuer Peer? -> dessen oeffentlichen Bestand abrufen (Anti-Entropy-Sync).
	// Loest das Problem dass GossipSub nur Live-Nachrichten propagiert: ein
	// frisch verbundener Peer bekommt so auch den BESTEHENDEN Marktplatz.
	if !known && id != n.host.ID() {
		go n.syncFromPeer(id)
		// Falls eine Chain angebunden ist: auch fehlende Blöcke aufholen.
		n.mu.RLock()
		hasChain := n.chain != nil
		n.mu.RUnlock()
		if hasChain {
			go n.SyncChainFromPeer(id.String())
		}
	}
}

// syncFromPeer fragt den oeffentlichen Record-Bestand eines Peers ab und
// speichert alles Neue lokal. Wird bei jedem neu verbundenen Peer ausgeloest.
func (n *Node) syncFromPeer(id peer.ID) {
	// Kurz warten bis die Verbindung/Protokolle ausgehandelt sind
	time.Sleep(2 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := n.SendAndReceive(ctx, id.String(), SyncProtocol, []byte("SYNC"))
	if err != nil {
		n.log.Debug("Peer-Sync Anfrage fehlgeschlagen",
			zap.String("peer", id.String()), zap.Error(err))
		return
	}
	var records []*storage.Record
	if err := json.Unmarshal(resp, &records); err != nil {
		n.log.Warn("Peer-Sync: ungueltige Antwort", zap.Error(err))
		return
	}
	imported := 0
	for _, r := range records {
		if r == nil {
			continue
		}
		if err := n.store.PutSynced(r); err == nil {
			imported++
		}
	}
	n.log.Info("Peer-Sync abgeschlossen",
		zap.String("peer", id.String()),
		zap.Int("received", len(records)),
		zap.Int("imported", imported))
}

// registerSyncResponder beantwortet Sync-Anfragen anderer Peers mit dem
// eigenen oeffentlichen Bestand.
func (n *Node) registerSyncResponder() {
	n.RegisterProtocol(SyncProtocol, func(peerID string, data []byte) []byte {
		records, err := n.store.ListAllPublic()
		if err != nil {
			n.log.Warn("Peer-Sync: ListAllPublic fehlgeschlagen", zap.Error(err))
			return []byte("[]")
		}
		out, err := json.Marshal(records)
		if err != nil {
			return []byte("[]")
		}
		n.log.Info("Peer-Sync: Bestand gesendet",
			zap.String("to", peerID), zap.Int("records", len(records)))
		return out
	})

	// KeyDir-Request: ein Peer fragt nach dem PubKey-Eintrag für eine Adresse.
	// Wir antworten mit dem lokalen keydir-Record (falls vorhanden). Das ist der
	// zuverlässige, direkte Weg für den Schlüsseltausch (schneller als DHT, robust
	// bei wenigen LAN-Nodes).
	n.RegisterProtocol(KeyDirProtocol, func(peerID string, data []byte) []byte {
		addr := strings.ToLower(strings.TrimSpace(string(data)))
		if addr == "" {
			return []byte("")
		}
		rec, err := n.store.Get(storage.RecordKeyDir, "keydir:"+addr)
		if err != nil || rec == nil {
			return []byte("") // haben wir nicht
		}
		out, err := json.Marshal(rec)
		if err != nil {
			return []byte("")
		}
		n.log.Info("KeyDir-Request beantwortet", zap.String("to", peerID), zap.String("addr", addr[:10]+"…"))
		return out
	})

	// Gerichtete Nachrichten-Zustellung: ein Peer schickt uns direkt eine
	// Nachricht (statt über GossipSub-Broadcast). Wir reichen sie an denselben
	// Handler weiter wie eine getopicte Nachricht. data = topicName + "\n" + payload.
	n.RegisterProtocol(MsgDeliverProtocol, func(peerID string, data []byte) []byte {
		// Format: erste Zeile = Zieltopic, Rest = Nachrichtenbytes.
		nl := -1
		for i, b := range data {
			if b == '\n' { nl = i; break }
		}
		if nl < 0 {
			return []byte("ERR")
		}
		topicName := string(data[:nl])
		payload := data[nl+1:]
		n.topicHandlersMu.RLock()
		handler, ok := n.topicHandlers[topicName]
		n.topicHandlersMu.RUnlock()
		if ok {
			handler(payload)
			return []byte("OK")
		}
		return []byte("NO_HANDLER")
	})
}

// DeliverMessage schickt eine Nachricht GERICHTET an einen bestimmten Peer
// (statt GossipSub-Broadcast). Gibt true zurück, wenn der Peer sie angenommen
// hat. Format: topicName + "\n" + payload.
func (n *Node) DeliverMessage(ctx context.Context, peerID, topicName string, payload []byte) bool {
	if peerID == "" {
		return false
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return false
	}
	// Nur versuchen, wenn der Peer AKTUELL verbunden ist — sonst sofort false
	// (→ GossipSub-Fallback), statt in einen Dial-Timeout zu laufen.
	if n.host.Network().Connectedness(pid) != network.Connected {
		return false
	}
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data := append([]byte(topicName+"\n"), payload...)
	resp, derr := n.SendAndReceive(dctx, peerID, MsgDeliverProtocol, data)
	if derr != nil {
		n.log.Warn("DeliverMessage: Peer nicht erreichbar", zap.String("peer", peerID[:12]+"…"), zap.Error(derr))
		return false
	}
	return string(resp) == "OK"
}

// RequestKeyDir fragt alle verbundenen Peers direkt nach dem PubKey-Eintrag für
// eine Adresse. Gibt den ersten gefundenen Record-JSON zurück (oder nil).
func (n *Node) RequestKeyDir(ctx context.Context, addr string) []byte {
	addr = strings.ToLower(strings.TrimSpace(addr))
	peers := n.Peers()
	for _, pid := range peers {
		reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		resp, err := n.SendAndReceive(reqCtx, pid.String(), KeyDirProtocol, []byte(addr))
		cancel()
		if err != nil {
			n.log.Warn("RequestKeyDir: Peer antwortet nicht", zap.String("peer", pid.String()[:12]+"…"), zap.Error(err))
			continue
		}
		if len(resp) == 0 {
			continue
		}
		return resp
	}
	return nil
}

// =============================================================================
//  mDNS Notifee (LAN-Autodiscovery)
// =============================================================================

type mdnsNotifee struct {
	node *Node
	log  *zap.Logger
}

func (m *mdnsNotifee) HandlePeerFound(info peer.AddrInfo) {
	m.log.Info("mDNS: peer found", zap.String("peer", info.ID.String()))
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := m.node.host.Connect(ctx, info); err != nil {
		m.log.Warn("mDNS connect failed", zap.Error(err))
		return
	}
	m.node.trackPeer(info.ID)
}

// =============================================================================
//  DHT Key/Value – für Datei-Manifeste und Chunk-Locations
// =============================================================================

// fundusValidator akzeptiert alle Werte im /fundus/-Namespace. Die Nutzdaten
// (Manifeste, Offers, Chunk-Locations) sind selbst signiert bzw. verschluesselt,
// daher ist keine zusaetzliche DHT-seitige Validierung noetig.
type fundusValidator struct{}

func (fundusValidator) Validate(key string, value []byte) error { return nil }
func (fundusValidator) Select(key string, values [][]byte) (int, error) { return 0, nil }

// DHTput speichert einen Wert im Kademlia DHT.
// Schlüssel-Format: "/fundus/<namespace>/<id>"
func (n *Node) DHTput(ctx context.Context, key string, value []byte) error {
	return n.dht.PutValue(ctx, key, value)
}

// DHTget liest einen Wert aus dem Kademlia DHT.
func (n *Node) DHTget(ctx context.Context, key string) ([]byte, error) {
	return n.dht.GetValue(ctx, key)
}

// SignData signiert beliebige Bytes mit dem privaten Host-Key des Nodes.
// Die Signatur kann von jedem Peer gegen die Peer-ID (= OwnerID) verifiziert
// werden, da die Peer-ID aus dem oeffentlichen Schluessel abgeleitet ist.
func (n *Node) SignData(data []byte) ([]byte, error) {
	priv := n.host.Peerstore().PrivKey(n.host.ID())
	if priv == nil {
		return nil, fmt.Errorf("p2p: kein privater Host-Key verfuegbar")
	}
	return priv.Sign(data)
}

// VerifySignature prueft ob signature eine gueltige Signatur ueber data ist,
// erstellt vom Inhaber der angegebenen Peer-ID (ownerID als String).
// Gibt true zurueck wenn die Signatur zum oeffentlichen Schluessel der ID passt.
func (n *Node) VerifySignature(ownerID string, data, signature []byte) bool {
	if ownerID == "" || len(signature) == 0 {
		return false
	}
	pid, err := peer.Decode(ownerID)
	if err != nil {
		return false
	}
	pub, err := pid.ExtractPublicKey()
	if err != nil || pub == nil {
		return false
	}
	ok, err := pub.Verify(data, signature)
	return err == nil && ok
}

// =============================================================================
//  Direkte Peer-Streams – für Chunk-Transfer (libp2p Streams)
// =============================================================================

// RegisterProtocol registriert einen Handler für ein libp2p-Protokoll.
// Der Handler empfängt die Anfrage-Bytes und gibt die Antwort-Bytes zurück.
func (n *Node) RegisterProtocol(protocol string, handler func(peerID string, data []byte) []byte) {
	n.host.SetStreamHandler(libp2pprotocol.ID(protocol), func(s network.Stream) {
		defer s.Close()
		peerID := s.Conn().RemotePeer().String()

		// Anfrage lesen (max 32 MB)
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			nr, err := s.Read(tmp)
			if nr > 0 {
				buf = append(buf, tmp[:nr]...)
			}
			if err != nil {
				break
			}
			if len(buf) > 32*1024*1024 {
				break
			}
		}

		// Handler aufrufen
		response := handler(peerID, buf)
		if len(response) > 0 {
			s.Write(response)
		}
	})
}

// SendToPeer sendet Daten direkt an einen Peer (fire-and-forget).
func (n *Node) SendToPeer(ctx context.Context, peerID string, protocol string, data []byte) error {
	pid, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("p2p: ungültige Peer-ID %q: %w", peerID, err)
	}

	s, err := n.host.NewStream(network.WithAllowLimitedConn(ctx, "fundus"), pid, libp2pprotocol.ID(protocol))
	if err != nil {
		return fmt.Errorf("p2p: Stream zu %s öffnen: %w", peerID, err)
	}
	defer s.Close()

	_, err = s.Write(data)
	return err
}

// SendAndReceive sendet eine Anfrage und liest die vollständige Antwort.
// libp2p-Streams sind bidirektional: Schreiben + Lesen in derselben Verbindung.
// Der Remote-Handler (RegisterProtocol) schreibt die Antwort zurück.
func (n *Node) SendAndReceive(ctx context.Context, peerID string, protocol string, data []byte) ([]byte, error) {
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("p2p: ungültige Peer-ID %q: %w", peerID, err)
	}

	s, err := n.host.NewStream(network.WithAllowLimitedConn(ctx, "fundus"), pid, libp2pprotocol.ID(protocol))
	if err != nil {
		return nil, fmt.Errorf("p2p: Stream zu %s öffnen: %w", peerID, err)
	}
	defer s.Close()

	// Anfrage senden
	if _, err := s.Write(data); err != nil {
		return nil, fmt.Errorf("p2p: Stream write: %w", err)
	}
	// Schreib-Seite schließen → Remote-Handler weiß dass Anfrage fertig ist
	if hc, ok := s.(interface{ CloseWrite() error }); ok {
		hc.CloseWrite()
	}

	// Antwort lesen (max 32 MB für Chunk-Daten)
	const maxResp = 32 * 1024 * 1024
	// Read-Deadline setzen, damit ein nicht antwortender Peer den Aufruf nicht
	// unbegrenzt blockiert (sonst hängt der ganze Request). Aus dem ctx ableiten.
	if dl, ok := ctx.Deadline(); ok {
		_ = s.SetReadDeadline(dl)
	} else {
		_ = s.SetReadDeadline(time.Now().Add(15 * time.Second))
	}
	buf  := make([]byte, 0, 4096)
	tmp  := make([]byte, 32*1024)
	for len(buf) < maxResp {
		nr, readErr := s.Read(tmp)
		if nr > 0 {
			buf = append(buf, tmp[:nr]...)
		}
		if readErr != nil {
			break // io.EOF, Deadline oder anderer Fehler
		}
	}

	if len(buf) == 0 {
		return nil, fmt.Errorf("p2p: leere Antwort von %s", peerID[:8])
	}
	return buf, nil
}

// =============================================================================
//  NAT-Status und Relay-Konfiguration
// =============================================================================

// ConnectToPeer verbindet gezielt mit einem Peer über seine vollständige
// Multiaddr (inkl. /p2p/<PeerID>). Für Tests/Diagnose nützlich, um die
// DHT-Discovery zu umgehen und direkt zu prüfen, ob eine Verbindung (ggf. über
// Relay) zustande kommt. Beispiel-Multiaddr:
//   /ip4/1.2.3.4/tcp/4001/p2p/12D3KooW...
//   /p2p/<RELAY_ID>/p2p-circuit/p2p/<ZIEL_ID>   (über Relay)
func (n *Node) ConnectToPeer(ctx context.Context, multiaddrStr string) error {
	ma, err := multiaddr.NewMultiaddr(strings.TrimSpace(multiaddrStr))
	if err != nil {
		return fmt.Errorf("ungültige Multiaddr: %w", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return fmt.Errorf("keine PeerID in Multiaddr: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := n.host.Connect(cctx, *info); err != nil {
		return fmt.Errorf("Verbindung fehlgeschlagen: %w", err)
	}
	return nil
}

// NATStatus gibt den aktuellen NAT-Traversal-Status zurück.
func (n *Node) NATStatus() map[string]interface{} {
	reachability := "unbekannt"
	addrs := n.host.Addrs()

	hasPublicAddr := false
	hasRelayAddr := false
	var publicAddrs []string
	var relayAddrs []string
	for _, addr := range addrs {
		s := addr.String()
		// Relay-Adressen enthalten "/p2p-circuit" — Zeichen, dass der Node über
		// einen Relay erreichbar ist (Fallback greift, wenn Hole-Punching scheitert).
		if strings.Contains(s, "p2p-circuit") {
			hasRelayAddr = true
			relayAddrs = append(relayAddrs, s)
			continue
		}
		if !isPrivateAddr(s) {
			hasPublicAddr = true
			publicAddrs = append(publicAddrs, s)
		}
	}

	// Ehrlichere Einschätzung: Eine öffentliche Adresse in der Liste heißt NICHT
	// zwingend, dass eingehende Verbindungen klappen (z.B. CGNAT). Verbundene
	// Peers + ggf. aktive Relay-Adresse sind die belastbareren Signale.
	peerCount := len(n.host.Network().Peers())
	switch {
	case hasPublicAddr:
		reachability = "public (direkte Adresse vorhanden — bei CGNAT trotzdem evtl. nur via Relay erreichbar)"
	case hasRelayAddr:
		reachability = "nat (über Relay erreichbar)"
	default:
		reachability = "nat (noch keine externe Adresse ermittelt)"
	}

	return map[string]interface{}{
		"reachability":     reachability,
		"addrs":            n.Addrs(),
		"has_public_ip":    hasPublicAddr,
		"has_relay_addr":   hasRelayAddr,
		"public_addrs":     publicAddrs,
		"relay_addrs":      relayAddrs,
		"connected_peers":  peerCount,
		"nat_traversal":    "hole_punching + circuit_relay + upnp",
		"dht_mode":         "auto (server wenn erreichbar, client sonst)",
	}
}

// defaultRelays gibt bekannte öffentliche Relay-Nodes zurück.
// Diese dienen als Fallback wenn Hole Punching scheitert.
// In Produktion: eigene Relay-Nodes für bessere Kontrolle.
func defaultRelays() []peer.AddrInfo {
	// Libp2p öffentliche Bootstrap + Relay Nodes
	// Werden nur genutzt wenn kein direkter Pfad möglich ist
	relayAddrs := []string{
		// libp2p.io Bootstrap Nodes (öffentlich verfügbar)
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	}

	var result []peer.AddrInfo
	for _, addr := range relayAddrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			continue
		}
		result = append(result, *info)
	}
	return result
}

// isPrivateAddr prüft ob eine Multiaddr eine private IP enthält.
func isPrivateAddr(addr string) bool {
	privateRanges := []string{
		"/ip4/10.", "/ip4/172.", "/ip4/192.168.", "/ip4/127.",
		"/ip6/::1", "/ip6/fc", "/ip6/fd",
	}
	for _, r := range privateRanges {
		if len(addr) >= len(r) && addr[:len(r)] == r {
			return true
		}
	}
	return false
}

// relaysFor liefert die statischen Relays: eigene (FUNDUS_P2P_RELAYS) zuerst,
// danach die öffentlichen Standard-Relays als Fallback.
func relaysFor(cfg *config.Config) []peer.AddrInfo {
	var out []peer.AddrInfo
	for _, a := range cfg.P2PRelays {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		if info, err := peer.AddrInfoFromP2pAddr(ma); err == nil {
			out = append(out, *info)
		}
	}
	return append(out, defaultRelays()...)
}

// announceFilter entfernt Loopback-Adressen aus den announcten Adressen und
// hängt konfigurierte öffentliche Adressen an.
func announceFilter(cfg *config.Config) func([]multiaddr.Multiaddr) []multiaddr.Multiaddr {
	var extra []multiaddr.Multiaddr
	for _, a := range cfg.P2PAnnounce {
		if ma, err := multiaddr.NewMultiaddr(a); err == nil {
			extra = append(extra, ma)
		}
	}
	return func(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
		out := make([]multiaddr.Multiaddr, 0, len(addrs)+len(extra))
		for _, a := range addrs {
			if manet.IsIPLoopback(a) {
				continue
			}
			out = append(out, a)
		}
		return append(out, extra...)
	}
}

// keepBootstrapPeers verbindet konfigurierte Bootstrap-Peers periodisch neu.
func (n *Node) keepBootstrapPeers(ctx context.Context) {
	var infos []peer.AddrInfo
	for _, a := range n.cfg.BootstrapPeers {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			continue
		}
		if info, err := peer.AddrInfoFromP2pAddr(ma); err == nil {
			infos = append(infos, *info)
		}
	}
	if len(infos) == 0 {
		return
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, info := range infos {
			if n.host.Network().Connectedness(info.ID) == network.Connected {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := n.host.Connect(cctx, info); err == nil {
				n.host.ConnManager().Protect(info.ID, "fundus-bootstrap")
				n.trackPeer(info.ID)
				n.log.Info("Bootstrap-Peer wieder verbunden", zap.String("peer", info.ID.String()))
			}
			cancel()
		}
	}
}

// =============================================================================
//  KeepAlive: Verbindung zu einem Peer aktiv halten (Remote-Zugriff)
// =============================================================================
//
// Solange über den Tunnel auf einen Peer zugegriffen wird, schützen wir die
// Verbindung vor dem Connection-Manager und pingen alle 20 s. Das hält die
// NAT-Zuordnung (Mobilfunk/CGNAT verwirft UDP-Zuordnungen oft nach ~30 s
// Stille) offen. Nach 15 min ohne Zugriff wird der Schutz wieder aufgehoben.

var (
	keepMu    sync.Mutex
	keepUntil = map[peer.ID]time.Time{}
	keepOnce  sync.Once
)

func (n *Node) KeepAlive(peerID string) {
	pid, err := peer.Decode(peerID)
	if err != nil || pid == n.host.ID() {
		return
	}
	n.host.ConnManager().Protect(pid, "fundus-remote")
	keepMu.Lock()
	keepUntil[pid] = time.Now().Add(15 * time.Minute)
	keepMu.Unlock()
	keepOnce.Do(func() { go n.keepAliveLoop() })
}

func (n *Node) keepAliveLoop() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		keepMu.Lock()
		var active []peer.ID
		for pid, until := range keepUntil {
			if now.After(until) {
				delete(keepUntil, pid)
				n.host.ConnManager().Unprotect(pid, "fundus-remote")
				continue
			}
			active = append(active, pid)
		}
		keepMu.Unlock()
		for _, pid := range active {
			if n.host.Network().Connectedness(pid) != network.Connected {
				continue // Neuaufbau übernimmt der nächste Tunnel-Zugriff
			}
			go func(p peer.ID) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				select {
				case <-ping.Ping(network.WithAllowLimitedConn(ctx, "fundus-keepalive"), n.host, p):
				case <-ctx.Done():
				}
			}(pid)
		}
	}
}

// =============================================================================
//  Rohdaten-Streams (Streaming-Tunnel v2)
// =============================================================================
//
// Anders als RegisterProtocol/SendAndReceive (Anfrage komplett einlesen, eine
// Antwort, max. 32 MB) liefern diese Funktionen den libp2p-Stream als net.Conn.
// Darüber läuft HTTP direkt und ohne Größengrenze – inkl. WebSocket-Upgrade.

// streamConn macht einen libp2p-Stream zu einem net.Conn (Read/Write/Close,
// CloseWrite und die Deadlines kommen vom eingebetteten Stream).
type streamConn struct {
	network.Stream
}

type p2pAddr string

func (a p2pAddr) Network() string { return "libp2p" }
func (a p2pAddr) String() string  { return string(a) }

func (c *streamConn) LocalAddr() net.Addr  { return p2pAddr(c.Stream.Conn().LocalPeer().String()) }
func (c *streamConn) RemoteAddr() net.Addr { return p2pAddr(c.Stream.Conn().RemotePeer().String()) }

// HandleRawStream registriert einen Handler, der den Stream als net.Conn erhält.
func (n *Node) HandleRawStream(protocol string, h func(peerID string, c net.Conn)) {
	n.host.SetStreamHandler(libp2pprotocol.ID(protocol), func(s network.Stream) {
		h(s.Conn().RemotePeer().String(), &streamConn{Stream: s})
	})
}

// OpenRawStream öffnet einen Stream zum Peer (auch über Relay-Verbindungen).
func (n *Node) OpenRawStream(ctx context.Context, peerID, protocol string) (net.Conn, error) {
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("ungültige Peer-ID: %w", err)
	}
	s, err := n.host.NewStream(network.WithAllowLimitedConn(ctx, "fundus-tunnel"), pid, libp2pprotocol.ID(protocol))
	if err != nil {
		return nil, fmt.Errorf("p2p: Stream zu %s öffnen: %w", peerID, err)
	}
	return &streamConn{Stream: s}, nil
}
