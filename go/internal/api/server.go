package api

import (
	"path/filepath"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"golang.org/x/time/rate"
	"time"

	"github.com/gin-gonic/gin"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"

	"lukechampine.com/blake3"

	"github.com/fundus/node/internal/config"
	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/filestore"
	"github.com/fundus/node/internal/geo"
	"github.com/fundus/node/internal/grid"
	"github.com/fundus/node/internal/llm"
	"github.com/fundus/node/internal/meter"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/shop"
	"github.com/fundus/node/internal/storage"
	"github.com/fundus/node/internal/topology"
)

// Server ist der lokale REST-API Server für das Lua-Frontend.
type Server struct {
	updateCtl *UpdateControl // Software-Update (prüfen/installieren)
	router      *gin.Engine
	srv         *http.Server
	node        p2p.P2PNode
	store       *storage.Store
	analyzer    *llm.Analyzer
	meterTokens     <-chan meter.Token
	capacityMgr     *topology.CapacityManager
	shareMgr_        *filestore.ShareManager
	orderBook        *OrderBook
	swapMgr          *SwapManager
	orch             *orchestrator
	swapCoord        *swapCoordinator
	searchMgr        *searchManager
	fileSearchMgr    *fileSearchManager
	jobSearchMgr     *jobSearchManager
	pendingWallet    *pendingWalletState
	pendingWalletMu  sync.Mutex
	settlementEngine *topology.SettlementEngine
	cfg         *config.Config
	log         *zap.Logger
	version     string

	// Shop (optional)
	shopWatcher *shop.SolWatcher
	shopFeed    *shop.PriceFeed
	// shopReceiveAddr ist die gemeinsame Solana-Empfangsadresse. Auch im Anzeige-
	// Modus gesetzt (Node ohne Bridge-Schlüssel), damit QR-Code/Zahlungsdaten
	// angezeigt werden können, obwohl dieser Node die Gutschrift nicht selbst macht.
	shopReceiveAddr string

	// Grid-Wallets
	gridWallets map[string]*grid.OperatorWallet

	// FileStore – anonymes dezentrales Filesharing (optional)
	fileStore *filestore.FileStore
	thumbs    *thumbnailer // serverseitige Thumbnail-Erzeugung (gecacht)
	chain     *chain.Blockchain
	mempool   *chain.Mempool
	// nodeWalletAddr: Adresse des Node-Wallets (für Storage-Reward-Mint als
	// Einreicher). Nil, wenn kein WalletPrivKey konfiguriert ist.
	nodeWalletAddr *chain.Address

	// payoutMgr verwaltet die automatische Auszahlung der Node-Einnahmen an eine
	// vom Betreiber gewählte Zielwallet (Schwelle oder Intervall).
	payoutMgr *payoutManager

	// locationStore hält die eigene Node-Position (GPS), gesetzt im Admin-Bereich,
	// genutzt als Referenz für die Marktplatz-Distanz ohne Such-PLZ.
	locationStore *locationStore
	adminWallet   *adminWalletStore

	// Topology – Netzgraph und Handelspfad-Routing
	topology  *topology.Manager
}

// NewServer erstellt den API-Server und registriert alle Routen.
func NewServer(cfg *config.Config, node p2p.P2PNode, store *storage.Store, analyzer *llm.Analyzer, meterTokens <-chan meter.Token, log *zap.Logger, version string) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// SICHERHEIT: Client-IP nur von nginx (127.0.0.1) und nur aus X-Real-IP
	// übernehmen. Vorher vertraute gin JEDEM Proxy und las X-Forwarded-For –
	// ein Client im LAN konnte mit "X-Forwarded-For: 127.0.0.1" als lokaler
	// Betreiber gelten.
	_ = r.SetTrustedProxies([]string{"127.0.0.1", "::1"})
	r.RemoteIPHeaders = []string{"X-Real-IP"}
	r.Use(gin.Recovery())
	r.Use(rateLimitMiddleware())
	r.Use(requestLogger(log))

	s := &Server{
		router:      r,
		node:        node,
		store:       store,
		analyzer:    analyzer,
		meterTokens: meterTokens,
		cfg:         cfg,
		version:     version,
		log:         log,
		searchMgr:   newSearchManager(),
		fileSearchMgr: newFileSearchManager(),
		jobSearchMgr: newJobSearchManager(),
	}

	// GLOBALE Tunnel-Schutzregel: gilt für ALLE Routen. Blockiert schreibende
	// und sensible Anfragen über den P2P-Tunnel, wenn keine Admin-Session vorliegt.
	// Lokale Anfragen sind unberührt. MUSS vor der Routen-Registrierung stehen.
	r.Use(s.tunnelGuardMiddleware())
	r.Use(s.ownerGuardMiddleware())

	s.registerRoutes()
	s.registerAnalyzeRoutes()
	s.registerMeterRoutes()
	s.registerPartnerRoutes()
	s.registerAdminRoutes()
	s.registerGridRoutes()
	s.registerFileRoutes()
	s.registerMessengerRoutes()
	s.registerTopologyRoutes()
	s.registerContractRoutes()
	s.registerSearchRoutes()
	s.registerJobSearchRoutes()
	s.registerFileSearchRoutes()
	s.registerWalletRoutes()
	s.registerChainRoutes()

	// Such-Responder: auf Netzwerk-Pings antworten + Rueckkanal einsammeln
	s.registerSearchResponder()
	s.registerFileSearchResponder()
	s.registerPresence() // Messenger: wer ist online
	s.registerJobSearchResponder()

	s.srv = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", cfg.Port),
		Handler: r,
		// WriteTimeout bewusst NICHT gesetzt (0 = unbegrenzt): Er gilt für die
		// GESAMTE Antwort, nicht pro Write. Bei großen Datei-Downloads (mehrere
		// GB, minutenlang gestreamt) würde jeder feste Wert den Transfer nach
		// Ablauf hart abbrechen ("write: i/o timeout" mitten im Stream). Der
		// Schutz gegen langsame/hängende Clients läuft stattdessen über
		// ReadHeaderTimeout (Request-Header) und IdleTimeout (Keep-Alive).
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Eigene Node-Position laden (für Marktplatz-Distanz ohne Such-PLZ).
	if cfg != nil && cfg.DataDir != "" {
		s.locationStore = newLocationStore(cfg.DataDir)
		s.adminWallet = newAdminWalletStore(cfg.DataDir)
	}

	return s
}

// WithShop aktiviert den FND-Token-Shop. Im VOLL-Modus (mit Watcher) überwacht
// dieser Node die Solana-Wallet und schreibt FND gut. Im ANZEIGE-Modus (watcher
// = nil, nur Adresse + Feed) zeigt der Node nur die Kaufseite/QR-Codes; die
// Gutschrift macht der eine Shop-Node mit Bridge-Schlüssel, der dieselbe Wallet
// überwacht. Die Solana-Transaktion selbst ist die "Beauftragung" — es braucht
// keine Node-zu-Node-Kommunikation.
func (s *Server) WithShop(watcher *shop.SolWatcher, feed *shop.PriceFeed, receiveAddr string) *Server {
	s.shopWatcher     = watcher
	s.shopFeed        = feed
	s.shopReceiveAddr = receiveAddr
	s.registerShopRoutes() // erst jetzt registrieren, da Feed/Adresse gesetzt sind
	return s
}

// WithFileStore aktiviert das dezentrale Filesharing.
func (s *Server) WithFileStore(fs *filestore.FileStore) *Server {
	s.fileStore = fs
	if s.cfg != nil && s.cfg.DataDir != "" {
		s.thumbs = newThumbnailer(s.cfg.DataDir)
	}
	return s
}

// WithShareManager verbindet den ShareManager (lokale Verzeichnis-Freigaben +
// DHT-Propagierung ins Netz). Wird in main.go mit dem P2P-Adapter erstellt,
// damit Freigaben an andere Nodes propagiert werden.
func (s *Server) WithShareManager(sm *filestore.ShareManager) *Server {
	s.shareMgr_ = sm
	if sm != nil && s.log != nil {
		s.log.Info("ShareManager aktiv (lokale Freigaben + Netz-Propagierung)")
	}
	return s
}

// WithOrderBook aktiviert das dezentrale FND/SOL-Orderbuch (P2P-propagierte
// Orders). Startet die Hintergrund-Discovery.
func (s *Server) WithOrderBook(node p2pNode) *Server {
	obPath := ""
	if s.cfg != nil && s.cfg.DataDir != "" {
		obPath = filepath.Join(s.cfg.DataDir, "orderbook.json")
	}
	s.orderBook = newOrderBook(node, obPath)
	go s.orderBook.Run(context.Background())
	go s.runOutboxWorker() // wartende Nachrichten zustellen sobald Empfänger online
	go s.mempoolMaintenance() // wartende Txs bereinigen + erneut verteilen (Validatoren erreichen)
	// Swap-Manager mit den Solana-Parametern aus der Config.
	solRPC, htlcProg := "", ""
	if s.cfg != nil {
		solRPC = s.cfg.ShopSolanaRPC
		htlcProg = s.cfg.SwapHTLCProgramID
	}
	s.swapMgr = newSwapManager(solRPC, htlcProg)
	if s.cfg != nil && s.cfg.DataDir != "" {
		// Laufende Swaps (HTLC-IDs, Phase) überstehen Update/Absturz – nötig, um
		// nach einem Ausfall einlösen oder zurückholen zu können.
		s.swapMgr.enablePersistence(filepath.Join(s.cfg.DataDir, "swaps.json"))
	}
	s.orch = newOrchestrator(s)
	s.swapCoord = newSwapCoordinator(s)
	s.swapCoord.loadDeposits() // hinterlegte Schlüssel nach Neustart zurückholen
	go s.resumeSolClaimsLoop() // offene SOL-Abholungen nach Neustart wieder aufnehmen
	go s.resumeRefundsLoop()   // abgelaufene eigene Sperren zurückholen (anhand der Chain)
	// Swap-Init-Protokoll am p2pNode registrieren (falls Orderbuch aktiv).
	if s.orderBook != nil && s.orderBook.Node() != nil {
		s.swapCoord.registerProtocol(s.orderBook.Node())
		// Automatisches Matching: seriell neu aufgebaut (ein Swap gleichzeitig,
		// Wächter, Backoff). Abschaltbar über FUNDUS_SWAP_AUTO=false. Der
		// manuelle Swap über das Orderbuch bleibt unabhängig davon aktiv.
		if s.cfg == nil || s.cfg.SwapAuto {
			s.swapCoord.runMatcher()
		}
	}
	if s.log != nil {
		s.log.Info("Orderbuch + Swap-Koordination aktiv (dezentrale FND/SOL-Orders)")
	}
	return s
}

// WithChain verbindet die eigene Fundus-Chain (Phase 2: Single-Producer, Multi-Node-Sync).
func (s *Server) WithChain(bc *chain.Blockchain, mp *chain.Mempool) *Server {
	s.chain = bc
	s.mempool = mp
	// Node-Wallet-Adresse für Storage-Reward-Mint ableiten (Einreicher des
	// Reward-Tx). Ohne gültigen Key bleibt nodeWalletAddr nil → kein Mint.
	if s.cfg != nil && s.cfg.WalletPrivKey != "" {
		keyHex := strings.TrimPrefix(s.cfg.WalletPrivKey, "0x")
		if key, err := ethcrypto.HexToECDSA(keyHex); err == nil {
			addr := chain.PubkeyToAddress(&key.PublicKey)
			s.nodeWalletAddr = &addr
		}
	}
	// Auto-Payout der Node-Einnahmen initialisieren + Loop starten.
	if s.cfg != nil && s.cfg.DataDir != "" {
		s.payoutMgr = newPayoutManager(s.cfg.DataDir, s, s.log)
		go s.payoutMgr.run(context.Background())
	}
	// Partner-Ads warten: eigene regelmäßig neu verteilen, veraltete fremde löschen.
	partnerMaintOnce.Do(func() { go s.partnerMaintenance(context.Background()) })
	// Automatische Double-Sign-Meldung: erkennt die Chain Equivocation, bauen wir
	// hier eine signierte TxSlash und legen sie in den Mempool. Der zuständige
	// Proposer baut sie ein; die Verifikation (applySlash) läuft deterministisch.
	bc.SetDoubleSignHandler(s.submitDoubleSignEvidence)
	// Über das Netz empfangene Transaktionen in den lokalen Mempool aufnehmen.
	bc.SetTxImporter(func(tx *chain.Transaction) error {
		if s.mempool == nil {
			return nil
		}
		return s.mempool.Add(tx)
	})
	return s
}

// Run startet den HTTP-Server auf der konfigurierten Adresse (blockierend).
func (s *Server) Run() error {
	return s.srv.ListenAndServe()
}

// RunOnAddr startet den Server auf einer explizit angegebenen Adresse.
// Nützlich für Tests die einen freien Port selbst wählen.
func (s *Server) RunOnAddr(addr string) error {
	s.srv.Addr = addr
	return s.srv.ListenAndServe()
}

// Handler gibt den http.Handler zurück (für Tests mit httptest.NewServer).
func (s *Server) Handler() http.Handler {
	return s.router
}

// Shutdown stoppt den Server gracefully.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// =============================================================================
//  Routen
// =============================================================================

func (s *Server) registerRoutes() {
	r := s.router

	// Health & Status
	r.GET("/health",       s.getHealth)
	r.GET("/api/v1/status", s.getStatus)
	r.GET("/api/v1/nat",    s.getNATStatus)  // NAT-Traversal-Status
	r.GET("/api/v1/donate/config", s.donateConfig) // freiwillige PayPal-Spende (Shop)
	r.POST("/api/v1/connect", s.postConnectPeer) // gezielt mit Peer verbinden (Test/Diagnose)

	// Listings (Waren & Dienste)
	listings := r.Group("/api/v1/listings")
	{
		listings.GET("",       s.listListings)
		listings.POST("",      s.createListing)
		listings.GET("/:id",   s.getListing)
		listings.PUT("/:id",   s.updateListing)
		listings.DELETE("/:id", s.deleteListing)
	}

	// Wallet-Adressbuch (lokale Empfangsadressen für Kaufabwicklung)
	addrbook := r.Group("/api/v1/addressbook")
	{
		addrbook.GET("",        s.listAddressBook)
		addrbook.POST("",       s.addAddressBookEntry)
		addrbook.DELETE("/:id", s.deleteAddressBookEntry)
	}

	// Dezentrales FND/SOL-Orderbuch (P2P-propagierte Orders)
	orders := r.Group("/api/v1/orders")
	{
		orders.POST("",       s.orderCreate)
		orders.GET("/mine",   s.orderListMine)
		orders.GET("/book",   s.orderBookGet)
		orders.DELETE("/:id", s.orderCancel)
	}

	// Atomarer FND↔SOL-Swap (HTLC-Koordination)
	swap := r.Group("/api/v1/swap")
	{
		swap.GET("/params",     s.swapParams)
		swap.GET("/health",     s.swapHealth)
		swap.GET("/secret",     s.swapSecret)
		swap.GET("/htlcs",      s.swapHTLCList)
		swap.GET("/all",        s.swapListAll)
		swap.GET("/deposits",   s.swapDeposits)
		swap.POST("/derive-addrs", s.swapDeriveAddrs)
		swap.POST("/keyaddr",   s.swapKeyAddress)
		swap.POST("/sol/initiate", s.swapSolInitiate)
		swap.POST("/sol/redeem",   s.swapSolRedeem)
		swap.POST("/sol/refund",   s.swapSolRefund)
		swap.POST("/fnd/lock",     s.swapFndLock)
		swap.POST("/fnd/claim",    s.swapFndClaim)
		swap.POST("/fnd/refund",   s.swapFndRefund)
		swap.POST("/auto/start",   s.swapAutoStart)
		swap.POST("/deposit",      s.swapDepositKeys)
		swap.POST("/buy",          s.swapBuy)
		swap.POST("",           s.swapInitiate)
		swap.GET("/:id",        s.swapGet)
		swap.POST("/:id/phase", s.swapUpdatePhase)
	}

	// Energie-Token
	energy := r.Group("/api/v1/energy")
	{
		energy.GET("",           s.listEnergyTokens)
		energy.POST("",          s.createEnergyToken)
		energy.GET("/:id",       s.getEnergyToken)
		energy.GET("/:id/fee",   s.calculateGridFee)
	}

	// Zertifikate
	certs := r.Group("/api/v1/certificates")
	{
		certs.GET("",      s.listCertificates)
		certs.POST("",     s.createCertificate)
		certs.GET("/:id",  s.getCertificate)
	}

	// Jobs
	jobs := r.Group("/api/v1/jobs")
	{
		jobs.GET("",      s.listJobs)
		jobs.POST("",     s.createJob)
		jobs.GET("/:id",  s.getJob)
		jobs.POST("/:id/sign", s.signJob)  // Vertrags-Signatur (Arbeitgeber/Auftragnehmer) verankern
	}

	// P2P-Netzwerk-Info
	r.GET("/api/v1/peers",  s.getPeers)
	r.GET("/api/v1/node",   s.getNodeInfo)

	// P2P-HTTP-Tunnel: Zugriff auf andere Nodes über libp2p (auch ohne direkte IP).
	// /api/v1/proxy/<peer-id>/<pfad> → tunnelt zur Web-UI des Ziel-Nodes.
	r.Any("/api/v1/proxy/:peerid/*path", s.proxyToPeer)
	// Remote-Modus: alle Seiten/Links über den Tunnel (Cookie fundus_remote).
	r.GET("/api/v1/remote/enter/:peerid", s.remoteEnter)
	r.GET("/api/v1/remote/exit", s.remoteExit)
	r.GET("/api/v1/remote/admin", s.remoteAdmin)
	s.registerTunnelHandler() // Ziel-seitigen Handler beim Node registrieren
	s.registerPartnerPull()   // Partner-Ads aktiv abfragbar (Pull)
	s.registerHomeData()      // Hybrid-Datenzugriff (Heim-Node)
	retentionStart.Do(func() { go s.runRetention(context.Background()) })
}

// =============================================================================
//  Handler: Health & Status
// =============================================================================

// NodeRevision ist die eincompilierte Build-Revision (für /health-Diagnose).
// Bei jedem Release erhöhen, damit eindeutig prüfbar ist, welche Version läuft.
const NodeRevision = "R485"

// SourceFingerprint: Prüfsumme der Go-Quellen, aus denen dieses Programm gebaut
// wurde (per -ldflags -X gesetzt von push-release.ps1 / deploy-fundus.ps1).
// Passt sie nicht zu go/SOURCE_FP des Pakets, war der Build gemischt.
var SourceFingerprint = "unbekannt"

// ChainInitError: warum die Chain nicht läuft (z.B. inkompatibles Format –
// Daten bleiben unangetastet). Von main gesetzt, in /health und chain/status.
var ChainInitError string

func (s *Server) getHealth(c *gin.Context) {
	out := gin.H{"status": "ok", "time": time.Now().Unix(), "revision": NodeRevision, "source_fp": SourceFingerprint}
	if ChainInitError != "" {
		out["chain_error"] = ChainInitError
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getStatus(c *gin.Context) {
	stStart := time.Now()
	defer func() {
		if d := time.Since(stStart); d > 300*time.Millisecond && s.log != nil {
			s.log.Warn("getStatus langsam", zap.Duration("dauer", d))
		}
	}()
	stats := s.store.Stats()
	nodeID := ""
	var addrs []string
	peerCount := 0
	if s.node != nil {
		nodeID    = s.node.ID().String()
		addrs     = s.node.Addrs()
		peerCount = len(s.node.Peers())
	}
	c.JSON(http.StatusOK, gin.H{
		"version": s.version,
		"node_id": nodeID,
		"addrs":   addrs,
		"peers":   peerCount,
		"storage": stats,
		"uptime":  time.Now().Unix(),
	})
}

// getNATStatus gibt den NAT-Traversal-Status zurück.
// Nützlich um zu prüfen ob Port-Weiterleitung nötig ist.
//
// GET /api/v1/nat
func (s *Server) getNATStatus(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht bereit"})
		return
	}

	// NATStatus wird von p2p.Node implementiert
	type natNoder interface {
		NATStatus() map[string]interface{}
	}
	if n, ok := s.node.(natNoder); ok {
		status := n.NATStatus()
		status["recommendation"] = natRecommendation(status)
		c.JSON(http.StatusOK, status)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"addrs": s.node.Addrs(),
		"peers": len(s.node.Peers()),
	})
}

// postConnectPeer verbindet gezielt mit einem Peer über seine Multiaddr.
// Für Tests/Diagnose (z.B. NAT-Traversal über getrennte Netze prüfen), um die
// DHT-Discovery zu umgehen. Body: {"multiaddr":"/ip4/.../p2p/12D3KooW..."}
//
// POST /api/v1/connect
func (s *Server) postConnectPeer(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht bereit"})
		return
	}
	var body struct {
		Multiaddr string `json:"multiaddr"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Multiaddr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "multiaddr fehlt"})
		return
	}
	type connector interface {
		ConnectToPeer(ctx context.Context, multiaddrStr string) error
	}
	n, ok := s.node.(connector)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "Connect nicht unterstützt"})
		return
	}
	if err := n.ConnectToPeer(c.Request.Context(), body.Multiaddr); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "peers": len(s.node.Peers())})
}

// natRecommendation gibt eine Handlungsempfehlung basierend auf dem NAT-Status.
func natRecommendation(status map[string]interface{}) string {
	if pub, ok := status["has_public_ip"].(bool); ok && pub {
		return "✓ Direkt erreichbar – keine Port-Weiterleitung nötig"
	}
	return "⚠ Hinter NAT – UPnP wird versucht. " +
		"Für optimale Performance: Port 4001 TCP+UDP im Router weiterleiten. " +
		"Hole Punching und Circuit Relay als Fallback aktiv."
}

// =============================================================================
//  Handler: Listings
// =============================================================================

func (s *Server) listListings(c *gin.Context) {
	records, err := s.store.List(storage.RecordListing)
	if err != nil {
		s.internalError(c, err)
		return
	}

	// --- Filter-/Such-/Sortier-Parameter ---
	q        := strings.ToLower(strings.TrimSpace(c.Query("q")))         // Volltext
	category := strings.ToLower(strings.TrimSpace(c.Query("category")))  // Kategorie
	plz      := strings.TrimSpace(c.Query("plz"))                        // Umkreis-Zentrum
	radiusKm := parseFloatDefault(c.Query("radius_km"), 0)               // 0 = kein Umkreisfilter
	sortBy   := c.Query("sort")                                          // "distance" | "price" | "newest"

	var centerLat, centerLon float64
	hasCenter := false
	if plz != "" {
		centerLat, centerLon = grid.PLZCentroid(plz)
		hasCenter = centerLat != 0 || centerLon != 0
	}
	// Fallback: keine Such-PLZ eingegeben → die eigene, im Admin gesetzte Position
	// als Referenz nehmen. So werden Distanzen auch ohne PLZ-Eingabe berechnet.
	if !hasCenter && s.locationStore != nil {
		if own := s.locationStore.get(); own.hasPosition() {
			centerLat, centerLon = own.Lat, own.Lon
			hasCenter = true
		}
	}

	type scored struct {
		rec  *storage.Record
		dist float64 // km, -1 wenn unbekannt
	}
	var out []scored

	for _, r := range records {
		d := r.Data
		// Volltext über Titel + Beschreibung + Keywords
		if q != "" {
			hay := strings.ToLower(getStr(d, "title") + " " +
				getStr(d, "description") + " " + getStr(d, "listing_text") + " " +
				keywordsToString(d["keywords"]))
			if !strings.Contains(hay, q) {
				continue
			}
		}
		// Kategorie
		if category != "" && strings.ToLower(getStr(d, "category")) != category {
			continue
		}
		// Distanz
		dist := -1.0
		if hasCenter {
			lat, okLat := toFloatOK(d["lat"])
			lon, okLon := toFloatOK(d["lon"])
			if okLat && okLon {
				dist = geo.HaversineKm(centerLat, centerLon, lat, lon)
				if radiusKm > 0 && dist > radiusKm {
					continue // außerhalb des Radius
				}
			} else if radiusKm > 0 {
				continue // kein Standort → bei Umkreisfilter ausschließen
			}
		}
		out = append(out, scored{rec: r, dist: dist})
	}

	// Sortierung
	switch sortBy {
	case "distance":
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].dist < 0 {
				return false
			}
			if out[j].dist < 0 {
				return true
			}
			return out[i].dist < out[j].dist
		})
	case "price":
		sort.SliceStable(out, func(i, j int) bool {
			pi, _ := toFloatOK(out[i].rec.Data["price_min"])
			pj, _ := toFloatOK(out[j].rec.Data["price_min"])
			return pi < pj
		})
	default: // newest
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].rec.CreatedAt.After(out[j].rec.CreatedAt)
		})
	}

	// --- Paging: nach der Sortierung auf die angeforderte Seite beschneiden. ---
	// total = Anzahl nach Filter (vor Paging), damit das Frontend die Seitenzahl kennt.
	total := len(out)
	perPage := int(parseFloatDefault(c.Query("per_page"), 24)) // Default 24 pro Seite
	if perPage < 1 {
		perPage = 24
	}
	if perPage > 100 {
		perPage = 100 // Obergrenze gegen Missbrauch
	}
	page := int(parseFloatDefault(c.Query("page"), 1))
	if page < 1 {
		page = 1
	}
	totalPages := (total + perPage - 1) / perPage
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	pageItems := out[start:end]

	// Antwort: Records + optional Distanz
	listings := make([]map[string]any, 0, len(pageItems))
	for _, s := range pageItems {
		item := map[string]any{
			"id":         s.rec.ID,
			"type":       s.rec.Type,
			"owner_id":   s.rec.OwnerID,
			"created_at": s.rec.CreatedAt,
			"updated_at": s.rec.UpdatedAt,
			"data":       s.rec.Data,
		}
		if s.dist >= 0 {
			item["distance_km"] = math.Round(s.dist*10) / 10
		}
		listings = append(listings, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"listings":    listings,
		"count":       len(listings),
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
	})
}

// --- Helfer für Filter/Sort ---
func getStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
func toFloatOK(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}
func parseFloatDefault(s string, def float64) float64 {
	if s == "" {
		return def
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return def
}
func keywordsToString(v any) string {
	switch kw := v.(type) {
	case string:
		return kw
	case []any:
		parts := make([]string, 0, len(kw))
		for _, e := range kw {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func (s *Server) createListing(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Beschreibung als Rich-Text sanitizen (nur sichere Formatierungs-Tags).
	if desc, ok := body["description"].(string); ok {
		body["description"] = sanitizeRichText(desc)
	}
	ownerID := ""
	if s.node != nil {
		ownerID = s.node.ID().String()
	}
	// Einzelpreis (neues Formular) auf price_min mappen, damit Anzeige und
	// Sortierung (die price_min nutzen) kompatibel bleiben. price_max = price_min.
	if p, ok := body["price"]; ok {
		body["price_min"] = p
		body["price_max"] = p
	}
	// Geo-Anreicherung: aus der PLZ Koordinaten + Geohash ableiten, damit
	// das Angebot per Umkreissuche gefunden werden kann.
	if plz, ok := body["plz"].(string); ok && plz != "" {
		lat, lon := grid.PLZCentroid(plz)
		if lat != 0 || lon != 0 {
			body["lat"] = lat
			body["lon"] = lon
			body["geohash"] = geo.GeohashEncode(lat, lon, 6)
		}
	}
	// Content-Hash über die Kerndaten des Inserats berechnen (bindet den späteren
	// Kaufvertrag kryptografisch an genau dieses Angebot).
	{
		hstr := fmt.Sprintf("%v|%v|%v|%v|%v",
			body["title"], body["description"], body["category"],
			body["condition"], body["image_hashes"])
		sum := blake3.Sum256([]byte(hstr))
		body["content_hash"] = "0x" + hex.EncodeToString(sum[:])
	}
	record := &storage.Record{
		ID:        generateID(),
		Type:      storage.RecordListing,
		OwnerID:   ownerID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Data:      body,
	}
	// Record signieren (Owner-Authentizitaet, gegen Manipulation im P2P-Netz)
	if s.node != nil {
		if sig, err := s.node.SignData(record.SigningBytes()); err == nil {
			record.Signature = sig
		}
	}
	if err := s.store.Put(record); err != nil {
		s.internalError(c, err)
		return
	}
	if s.node != nil {
		if data, err := marshalRecord(record); err == nil {
			_ = s.node.Publish(c.Request.Context(), p2p.TopicListings, data)
		}
	}
	c.JSON(http.StatusCreated, record)
}

func (s *Server) getListing(c *gin.Context) {
	id := c.Param("id")
	record, err := s.store.Get(storage.RecordListing, id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if record == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	// editable: lokaler Owner ODER unsignierter Altbestand (adoptierbar)
	editable := (record.OwnerID != "" && record.OwnerID == s.nodeID()) ||
		len(record.Signature) == 0
	c.JSON(http.StatusOK, gin.H{
		"id":         record.ID,
		"type":       record.Type,
		"owner_id":   record.OwnerID,
		"created_at": record.CreatedAt,
		"updated_at": record.UpdatedAt,
		"data":       record.Data,
		"editable":   editable,
	})
}

func (s *Server) updateListing(c *gin.Context) {
	id := c.Param("id")
	existing, err := s.store.Get(storage.RecordListing, id)
	if err != nil || existing == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// Eigentümer-Prüfung: nur der erstellende Node darf ändern.
	// Ausnahme: UNSIGNIERTE Altbestände (vor Einführung der Signatur/
	// persistenten Identität erstellt) duerfen lokal uebernommen werden –
	// sie werden beim Speichern mit der aktuellen Identitaet neu signiert.
	isLegacy := len(existing.Signature) == 0
	if !isLegacy && existing.OwnerID != "" && existing.OwnerID != s.nodeID() {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Nur der Ersteller darf dieses Angebot bearbeiten"})
		return
	}
	if isLegacy {
		// Adoption: Angebot der aktuellen Identitaet zuordnen
		existing.OwnerID = s.nodeID()
	}

	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Beschreibung als Rich-Text sanitizen (nur sichere Formatierungs-Tags).
	if desc, ok := body["description"].(string); ok {
		body["description"] = sanitizeRichText(desc)
	}
	// Felder MERGEN statt komplett ersetzen: nur die im Body gesendeten
	// Felder werden aktualisiert. So gehen z.B. Bilder (images/image_hashes)
	// nicht verloren, wenn das Edit-Formular sie nicht mitsendet.
	if existing.Data == nil {
		existing.Data = map[string]any{}
	}
	for k, v := range body {
		existing.Data[k] = v
	}
	// Einzelpreis auf price_min/max mappen (Kompatibilität mit der Anzeige).
	if p, ok := body["price"]; ok {
		existing.Data["price_min"] = p
		existing.Data["price_max"] = p
	}
	// Geo-Anreicherung nachziehen: Wenn eine PLZ vorhanden ist, Koordinaten +
	// Geohash (neu) ableiten. Sonst hätten bearbeitete oder alte Angebote keine
	// lat/lon und würden in der Umkreis-/Distanzsuche nie auftauchen. Wird bei
	// jedem Update gemacht, damit auch eine geänderte PLZ die Koordinaten nachführt.
	if plz, ok := existing.Data["plz"].(string); ok && plz != "" {
		lat, lon := grid.PLZCentroid(plz)
		if lat != 0 || lon != 0 {
			existing.Data["lat"] = lat
			existing.Data["lon"] = lon
			existing.Data["geohash"] = geo.GeohashEncode(lat, lon, 6)
		}
	}
	existing.UpdatedAt = time.Now()
	// Nach Aenderung neu signieren
	if s.node != nil {
		if sig, err := s.node.SignData(existing.SigningBytes()); err == nil {
			existing.Signature = sig
		}
	}
	if err := s.store.Put(existing); err != nil {
		s.internalError(c, err)
		return
	}

	// Update im P2P-Netz publizieren
	if s.node != nil {
		if data, err := marshalRecord(existing); err == nil {
			_ = s.node.Publish(c.Request.Context(), p2p.TopicListings, data)
		}
	}

	c.JSON(http.StatusOK, existing)
}

func (s *Server) deleteListing(c *gin.Context) {
	id := c.Param("id")

	// Vor dem Löschen laden für Eigentümer-Prüfung
	existing, err := s.store.Get(storage.RecordListing, id)
	if err != nil || existing == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	// Eigentümer-Prüfung: nur der Ersteller darf loeschen.
	// Unsignierte Altbestände duerfen lokal geloescht werden (Adoption).
	isLegacy := len(existing.Signature) == 0
	if !isLegacy && existing.OwnerID != "" && existing.OwnerID != s.nodeID() {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Nur der Ersteller darf dieses Angebot loeschen"})
		return
	}
	// Bei Adoption: OwnerID auf eigene Identitaet setzen, damit der signierte
	// Tombstone beim Empfaenger die Signaturpruefung besteht.
	ownerID := existing.OwnerID
	if isLegacy {
		ownerID = s.nodeID()
	}

	// Zugehörige Medien aus dem lokalen FileStore entfernen (Bilder + Video),
	// damit beim Löschen der Anzeige keine verwaisten Chunks zurückbleiben. Die
	// Verwaiste-Replikate-GC würde sie sonst erst nach Tagen aufräumen; hier ist
	// klar, dass sie nicht mehr gebraucht werden.
	if s.fileStore != nil {
		if hashes, ok := existing.Data["image_hashes"].([]any); ok {
			for _, h := range hashes {
				if hs, ok := h.(string); ok && len(hs) == 64 {
					s.fileStore.RemoveFromIndex(hs)
				}
			}
		}
		if vh, ok := existing.Data["video_hash"].(string); ok && len(vh) == 64 {
			s.fileStore.RemoveFromIndex(vh)
		}
		if vhs, ok := existing.Data["video_hashes"].([]any); ok {
			for _, h := range vhs {
				if hs, ok := h.(string); ok && len(hs) == 64 {
					s.fileStore.RemoveFromIndex(hs)
				}
			}
		}
	}

	if err := s.store.Delete(storage.RecordListing, id); err != nil {
		s.internalError(c, err)
		return
	}

	// Signierten Tombstone im P2P-Netz publizieren damit Peers (nur bei
	// gueltiger Owner-Signatur) den Record ebenfalls loeschen.
	if s.node != nil {
		now := time.Now()
		tombstone := &storage.Record{
			ID:        existing.ID,
			Type:      storage.RecordListing,
			OwnerID:   ownerID,
			CreatedAt: existing.CreatedAt,
			UpdatedAt: now,
			DeletedAt: &now,
			Data:      existing.Data,
		}
		if sig, err := s.node.SignData(tombstone.SigningBytes()); err == nil {
			tombstone.Signature = sig
		}
		// Lokalen Tombstone mit korrekter (adoptierter) OwnerID + Signatur
		// ueberschreiben, damit auch der Re-Sync ihn als gueltig akzeptiert.
		_ = s.store.PutSynced(tombstone)
		if data, err := marshalRecord(tombstone); err == nil {
			_ = s.node.Publish(c.Request.Context(), p2p.TopicListings, data)
		}
	}

	c.Status(http.StatusNoContent)
}

// =============================================================================
//  Handler: Energie-Token
// =============================================================================

func (s *Server) listEnergyTokens(c *gin.Context) {
	records, err := s.store.List(storage.RecordEnergy)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tokens": records, "count": len(records)})
}

func (s *Server) createEnergyToken(c *gin.Context) {
	var token storage.EnergyToken
	if err := c.ShouldBindJSON(&token); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if token.Timestamp.IsZero() {
		token.Timestamp = time.Now()
	}

	ownerID := ""
	if s.node != nil {
		ownerID = s.node.ID().String()
	}
	record := &storage.Record{
		ID:        generateID(),
		Type:      storage.RecordEnergy,
		OwnerID:   ownerID,
		CreatedAt: time.Now(),
		Data: map[string]any{
			"timestamp":     token.Timestamp,
			"meter_id":      token.MeterID,
			"lat":           token.Lat,
			"lon":           token.Lon,
			"kwh":           token.KWh,
			"generator_lat": token.GeneratorLat,
			"generator_lon": token.GeneratorLon,
		},
	}

	if err := s.store.Put(record); err != nil {
		s.internalError(c, err)
		return
	}

	if s.node != nil {
		if data, err := marshalRecord(record); err == nil {
			_ = s.node.Publish(c.Request.Context(), p2p.TopicEnergy, data)
		}
	}

	c.JSON(http.StatusCreated, record)
}

func (s *Server) getEnergyToken(c *gin.Context) {
	s.getRecord(c, storage.RecordEnergy)
}

// calculateGridFee berechnet den Netzbetreiber-Anteil basierend auf
// der Haversine-Distanz zwischen Erzeuger und Verbraucher.
func (s *Server) calculateGridFee(c *gin.Context) {
	id := c.Param("id")
	record, err := s.store.Get(storage.RecordEnergy, id)
	if err != nil || record == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "token not found"})
		return
	}

	data := record.Data
	genLat, _ := data["generator_lat"].(float64)
	genLon, _ := data["generator_lon"].(float64)
	conLat, _ := data["lat"].(float64)
	conLon, _ := data["lon"].(float64)
	kwh, _   := data["kwh"].(float64)

	distKm   := geo.HaversineKm(genLat, genLon, conLat, conLon)
	feeRatio := geo.GridFeeRatio(distKm)
	feeKwh   := kwh * feeRatio

	c.JSON(http.StatusOK, gin.H{
		"token_id":       id,
		"kwh":            kwh,
		"distance_km":    math.Round(distKm*100) / 100,
		"fee_ratio":      math.Round(feeRatio*10000) / 10000,
		"fee_kwh":        math.Round(feeKwh*10000) / 10000,
		"net_kwh":        math.Round((kwh-feeKwh)*10000) / 10000,
	})
}

// =============================================================================
//  Handler: Zertifikate & Jobs (vereinfacht – gleiche Struktur wie Listings)
// =============================================================================

func (s *Server) listCertificates(c *gin.Context)  { s.listRecords(c, storage.RecordCertificate) }
func (s *Server) createCertificate(c *gin.Context) { s.createRecord(c, storage.RecordCertificate, p2p.TopicCertificate) }
func (s *Server) getCertificate(c *gin.Context)    { s.getRecord(c, storage.RecordCertificate) }

func (s *Server) listJobs(c *gin.Context)   { s.listRecords(c, storage.RecordJob) }
func (s *Server) createJob(c *gin.Context)  { s.createRecord(c, storage.RecordJob, p2p.TopicJobs) }
func (s *Server) getJob(c *gin.Context)     { s.getRecord(c, storage.RecordJob) }

// signJob verankert eine Vertrags-Signatur (Arbeitgeber oder Auftragnehmer) im
// Job-Record. Erwartet {role, wallet, hash, signature}. Der Hash ist der SHA-256
// des Vertragstexts, die Signatur die Ed25519-Signatur darüber (aus /identity/sign).
func (s *Server) signJob(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		Role      string `json:"role"      binding:"required"` // "employer" | "contractor"
		Wallet    string `json:"wallet"`
		Hash      string `json:"hash"      binding:"required"`
		Signature string `json:"signature" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Role != "employer" && req.Role != "contractor" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role muss employer oder contractor sein"})
		return
	}
	rec, err := s.store.Get(storage.RecordJob, id)
	if err != nil || rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Job nicht gefunden"})
		return
	}
	// Signaturen-Map im Record anlegen/aktualisieren.
	sigs, _ := rec.Data["signatures"].(map[string]any)
	if sigs == nil {
		sigs = map[string]any{}
	}
	sigs[req.Role] = map[string]any{
		"wallet":    req.Wallet,
		"hash":      req.Hash,
		"signature": req.Signature,
		"signed_at": time.Now().Format(time.RFC3339),
	}
	rec.Data["signatures"] = sigs
	// Vollständig signiert, wenn beide Rollen da sind.
	_, hasEmp := sigs["employer"]
	_, hasCon := sigs["contractor"]
	rec.Data["fully_signed"] = hasEmp && hasCon
	rec.UpdatedAt = time.Now()

	if err := s.store.Put(rec); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":           true,
		"role":         req.Role,
		"fully_signed": hasEmp && hasCon,
	})
}

// =============================================================================
//  Handler: P2P-Info
// =============================================================================

func (s *Server) getPeers(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusOK, gin.H{"peers": []string{}, "count": 0})
		return
	}
	peers := s.node.Peers()
	ids := make([]string, len(peers))
	for i, p := range peers {
		ids[i] = p.String()
	}
	// Eigene Adressen (für FUNDUS_BOOTSTRAP_PEERS auf anderen Nodes).
	// Nur LAN/öffentliche Multiaddr mit /p2p/ — daraus kann ein anderer Pi
	// direkt verbinden, ohne auf DHT/mDNS angewiesen zu sein.
	c.JSON(http.StatusOK, gin.H{
		"peers":          ids,
		"peers_detailed": s.node.PeersDetailed(),
		"count":          len(ids),
		"my_id":          s.node.ID().String(),
		"my_addrs":       s.node.Addrs(),
	})
}

func (s *Server) getNodeInfo(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusOK, gin.H{"id": "", "addrs": []string{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":    s.node.ID().String(),
		"addrs": s.node.Addrs(),
	})
}

// =============================================================================
//  Generische Handler-Helfer
// =============================================================================

func (s *Server) listRecords(c *gin.Context, rt storage.RecordType) {
	records, err := s.store.List(rt)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"records": records, "count": len(records)})
}

func (s *Server) createRecord(c *gin.Context, rt storage.RecordType, topic string) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ownerID := ""
	if s.node != nil {
		ownerID = s.node.ID().String()
	}
	record := &storage.Record{
		ID:        generateID(),
		Type:      rt,
		OwnerID:   ownerID,
		CreatedAt: time.Now(),
		Data:      body,
	}
	if err := s.store.Put(record); err != nil {
		s.internalError(c, err)
		return
	}
	if s.node != nil {
		if data, err := marshalRecord(record); err == nil {
			_ = s.node.Publish(c.Request.Context(), topic, data)
		}
	}
	c.JSON(http.StatusCreated, record)
}

func (s *Server) getRecord(c *gin.Context, rt storage.RecordType) {
	id := c.Param("id")
	record, err := s.store.Get(rt, id)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if record == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, record)
}

func (s *Server) internalError(c *gin.Context, err error) {
	s.log.Error("API internal error", zap.Error(err))
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func marshalRecord(r *storage.Record) ([]byte, error) {
	return json.Marshal(r)
}

func requestLogger(log *zap.Logger) gin.HandlerFunc {
	// Hochfrequente Poll-Endpunkte NICHT loggen, damit der Log nicht vollläuft
	// (z.B. das 10s-Polling der Netz-Freigaben). Nur bei Fehlern (5xx) loggen.
	quietPaths := map[string]bool{
		"/api/v1/shares/network": true,
		"/api/v1/status":         true,
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		status := c.Writer.Status()
		// Ruhige Pfade nur bei Server-Fehler loggen.
		if quietPaths[c.Request.URL.Path] && status < 500 {
			return
		}
		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", status),
			zap.Duration("duration", time.Since(start)),
		}
		// Server-Fehler (5xx) prominent als Error loggen, damit sie im Log
		// auffallen und nicht zwischen den Info-Zeilen untergehen.
		if status >= 500 {
			log.Error("API request FEHLER", fields...)
		} else {
			log.Info("API request", fields...)
		}
	}
}

// WithTopology aktiviert das Netzgraph-Topologie-System.
func (s *Server) WithTopology(mgr *topology.Manager) *Server {
	s.topology = mgr
	return s
}

// rateLimitMiddleware schützt die API vor Flooding vom lokalen Browser.
// 60 Requests/Sekunde (Burst 120) reichen für alle UI-Aktionen.
// Schutz gegen: accidentelle Loops, bösartige Skripte via CSRF.
func rateLimitMiddleware() gin.HandlerFunc {
	type client struct {
		limiter  *rate.Limiter
		lastSeen time.Time
	}
	var (
		mu      sync.Mutex
		clients = make(map[string]*client)
	)

	// Aufräumen alter Einträge alle 5 Minuten
	go func() {
		for range time.Tick(5 * time.Minute) {
			mu.Lock()
			for ip, c := range clients {
				if time.Since(c.lastSeen) > 10*time.Minute {
					delete(clients, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		mu.Lock()
		if _, ok := clients[ip]; !ok {
			clients[ip] = &client{limiter: rate.NewLimiter(60, 120)}
		}
		cl := clients[ip]
		cl.lastSeen = time.Now()
		mu.Unlock()

		if !cl.limiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded – max 60 req/s",
			})
			return
		}
		c.Next()
	}
}

