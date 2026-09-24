package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// Config hält alle Node-Einstellungen.
// Werte kommen aus Umgebungsvariablen (gesetzt via /etc/fundus/fundus.env).
type Config struct {
	// Netzwerk
	Port    int    // REST-API Port (Standard: 3000)
	P2PPort int    // libp2p Port   (Standard: 4001)
	P2PHost string // Bind-Adresse  (Standard: 0.0.0.0)

	// Peers
	BootstrapPeers []string // Multiaddr-Liste bekannter Bootstrap-Nodes (werden gehalten + neu verbunden)
	P2PRelays      []string // eigene Circuit-Relays (Multiaddr inkl. /p2p/<id>), vor den öffentlichen
	P2PRelayService bool    // dieser Node bietet anderen Nodes Relay an (braucht öffentlich erreichbaren Port)
	P2PAnnounce    []string // zusätzlich announcte eigene Adressen (z.B. /ip4/<öffentl.IP>/tcp/4001)
	PeersMax       int      // Maximale Peer-Replikate (Standard: 5)

	// Storage
	DataDir string // Lokales Datenverzeichnis (Standard: /opt/fundus/data)
	LogDir  string // Log-Verzeichnis          (Standard: /var/log/fundus)

	// Feature-Flags
	LLMEnabled bool   // LLM-Analyse aktiviert
	LLMModel   string // Modell-Pfad für llama.cpp

	// Smartmeter
	MeterProtocol  string
	MeterPort      string        // /dev/ttyUSB0 oder /dev/ttyAMA0
	MeterBaudRate  int           // 115200 (Standard SML), 300→9600 (D0)
	MeterDataBits  int           // 8
	MeterStopBits  float64       // 1.0 oder 2.0
	MeterParity    string        // N (None) | E (Even) | O (Odd)
	MeterHTTPURL   string
	MeterID        string
	MeterLat       float64
	MeterLon       float64
	MeterGenLat    float64
	MeterGenLon    float64
	MeterSignKey   string

	// FND Shop (SOL → FND)
	// AdminWallet: die Wallet-Adresse (0x…) des Node-Eigentümers für den Admin-
	// Login über den P2P-Tunnel. Wer sich mit dem passenden Schlüssel/Seed anmeldet,
	// bekommt Admin-Rechte. Leer = nur lokaler Admin-Zugriff (kein Remote-Admin).
	AdminWallet        string
	ShopEnabled        bool    // Shop aktivieren
	ShopSolanaRPC      string  // Solana RPC-Endpunkt
	ShopReceiveAddr    string  // Solana-Empfangsadresse (base58)
	SwapHTLCProgramID  string  // Programm-ID des Solana-HTLC-Programms (base58) für Atomic Swaps
	SwapAuto          bool   // automatisches Order-Matching (seriell, abschaltbar)
	TurnURLs          []string // TURN-Server für Anrufe hinter NAT (turn:host:3478,…)
	TTLMailboxDays    int      // nicht abgeholte Offline-Nachrichten (0 = nie löschen)
	TTLDirectoryDays  int      // Keydir/Kontakte/E-Mail-Verzeichnis fremder Nutzer
	TTLHistoryDays    int      // Messenger-Verlauf
	TurnUser          string
	TurnPass          string
	// ShopBridgePrivKey: SEPARATER Schlüssel des Brücken-Operators (NICHT der
	// Node-/FeeCollector-Key). Nur diese Instanz darf SOL→FND-Gutschriften
	// autorisieren. Er läuft ausschließlich auf dem Shop-Node des Betreibers und
	// wird NIEMALS an User weitergegeben. Leer = keine native SOL-Brücke.
	ShopBridgePrivKey  string
	// ShopBridgeAuthority: die ÖFFENTLICHE Adresse des Brücken-Operators. Muss
	// auf ALLEN Nodes gesetzt sein (auch ohne Shop), damit alle dieselben
	// SOL-Credits als gültig anerkennen — nur so ist die Chain konsistent. Enthält
	// KEIN Geheimnis (nur die Adresse). Auf dem Shop-Node muss sie zur Adresse aus
	// ShopBridgePrivKey passen.
	ShopBridgeAuthority string
	ShopSlippageBPS    int     // Slippage-Puffer in Basispunkten (Standard: 50)
	ShopOfferTTLSec    int     // Angebots-Ablaufzeit in Sekunden (Standard: 90)

	// Blockchain (Gnosis Chain)
	ChainID            int64
	ChainRPCURL        string
	FNDAddress         string // FND Contract-Adresse
	EnergyTokenAddress string // EnergyToken Contract-Adresse
	MarketAddress      string // FundusMarket Contract-Adresse
	FileStorageAddress string // FileStorage Billing Contract
	WalletPrivKey      string // Hex-kodierter Private Key (ACHTUNG: sicher speichern!)
	SeedPassword       string // Passwort zum Entsperren einer verschlüsselten Node-Seed
	Validators         []string // PoA-Validator-Adressen (Konsens); Fee-Collector ist implizit dabei
	UpdateManifestURL  string   // URL zum signierten Update-Manifest (GitHub); "off" = Prüfung aus
	UpdateAuto         bool     // true = gefundene Updates sofort installieren (Standard: nur anbieten)
	DonatePayPal       string   // PayPal-Empfänger für freiwillige Spenden im Shop; "off" = Karte aus
	StorageRewardAddr  string // Betreiber-Wallet für Storage/Transfer-Verdienst (leer = Node-Adresse)
	FNDFeeCollector    string // Gnosis-Chain-Gebührenadresse

	// Node-Typ und Netzposition (Topologie-System)
	// Definiert die Rolle dieses Nodes im Energienetz.
	NodeType          string  // consumer | prosumer | substation | power_plant
	VoltageLevel      string  // lv | mv | hv | ehv
	NodeLat           float64 // GPS-Koordinaten dieses Nodes
	NodeLon           float64
	ParentSubstation  string  // Peer-ID der übergeordneten Trafostation (ONT)
	TransitFeePercent float64 // Durchleitungsgebühr in % (nur für substation-Nodes)
	GMapsAPIKey       string  // Google Maps Distance Matrix API Key (optional)

	// Dezentrales Filesharing
	//
	// StorageOfferGB: Wie viel GB dieser Node dem Netz anbietet.
	//   Diese Menge wird 5-fach gespiegelt → jeder Chunk liegt auf 5 Nodes.
	//   Vergütung: 1 FND/TB gesendet + 1 FND/TB/Monat.
	//
	// StorageAllocGB: Wie viel GB auf der lokalen Disk reserviert werden.
	//   MINIMUM: 5 × StorageOfferGB
	//   (eigene Dateien + 4 Spiegel-Chunks anderer Nodes)
	//   Wenn 0: wird automatisch auf 5 × OfferGB gesetzt.
	StorageOfferGB int64  // dem Netz angebotene GB (= was andere speichern können)
	StorageAllocGB int64  // tatsächlich allokierte GB auf lokaler Disk (≥ 5 × OfferGB)
	StorageDir     string // Verzeichnis für Chunk-Daten

	// SeedFile: Pfad zur Datei mit den 30 Seed-Wörtern (optional).
	// Wird beim Start einmalig gelesen und sofort verworfen.
	// Sicherer als FUNDUS_SEED_WORDS Env-Variable (nicht in ps aux sichtbar).
	SeedFile       string

	// Kapazitäts-Management (Ansatz 3 – parallel zu Temporal-Matching)
	// TrafoRatedKW: Nennleistung des Trafos in kW (nur für substation/power_plant).
	// Typische Werte: 400 (LV), 1600 (MV), 25000 (HV-Umspannwerk).
	TrafoRatedKW       float64 // aus FUNDUS_TRAFO_RATED_KW
	SettlementMethod   string  // "ansatz3" | "ansatz2" | "mixed"

	// Verzeichnis-Shares (kein DHT-Replikation, lokale Dateien)
	// ShareDataDir: Verzeichnis für Share-Metadaten (shares.json).
	// Standardmäßig: DataDir + "/shares"
	ShareDataDir       string

	// Matching
	// BloomFilterSize: Bits pro Bloom-Filter (Standard 4096).
	// Erhöhen bei vielen Interessen (>50 Tags) um False-Positives zu reduzieren.
	BloomFilterSize    uint // aus FUNDUS_BLOOM_FILTER_SIZE
}

// Load liest die Konfiguration aus der Umgebung.
func Load(log *zap.Logger) *Config {
	cfg := &Config{
		Port:       envInt("FUNDUS_PORT", 3000),
		P2PPort:    envInt("FUNDUS_P2P_PORT", 4001),
		P2PHost:    envStr("FUNDUS_P2P_HOST", "0.0.0.0"),
		PeersMax:   envInt("FUNDUS_PEERS_MAX", 5),
		AdminWallet: envStr("FUNDUS_ADMIN_WALLET", ""),
		DataDir:    envStr("FUNDUS_DATA_DIR", "/opt/fundus/data"),
		LogDir:     envStr("FUNDUS_LOG_DIR", "/var/log/fundus"),
		LLMEnabled: envBool("FUNDUS_LLM_ENABLED", false),
		LLMModel:   envStr("FUNDUS_LLM_MODEL", "moondream2"),

		MeterProtocol: envStr("FUNDUS_METER_PROTOCOL", ""),
		MeterPort:     envStr("FUNDUS_METER_PORT", "/dev/ttyUSB0"),
		MeterBaudRate: envInt("FUNDUS_METER_BAUD", 115200),
		MeterDataBits: envInt("FUNDUS_METER_DATABITS", 8),
		MeterStopBits: envFloat("FUNDUS_METER_STOPBITS", 1.0),
		MeterParity:   envStr("FUNDUS_METER_PARITY", "N"),
		MeterHTTPURL:  envStr("FUNDUS_METER_HTTP_URL", ""),
		MeterID:       envStr("FUNDUS_METER_ID", ""),
		MeterLat:      envFloat("FUNDUS_METER_LAT", 0),
		MeterLon:      envFloat("FUNDUS_METER_LON", 0),
		MeterGenLat:   envFloat("FUNDUS_METER_GEN_LAT", 0),
		MeterGenLon:   envFloat("FUNDUS_METER_GEN_LON", 0),
		MeterSignKey:  envStr("FUNDUS_METER_SIGN_KEY", ""),

		ShopEnabled:     envBool("FUNDUS_SHOP_ENABLED", false),
		ShopSolanaRPC:   envStr("FUNDUS_SHOP_SOLANA_RPC", "https://api.mainnet-beta.solana.com"),
		ShopReceiveAddr: envStr("FUNDUS_SHOP_RECEIVE_ADDR", ""),
		SwapHTLCProgramID: envStr("FUNDUS_SWAP_HTLC_PROGRAM", ""),
		SwapAuto:          envBool("FUNDUS_SWAP_AUTO", true),
		TurnURLs:          envStrList("FUNDUS_TURN_URLS", ""),
		TTLMailboxDays:    envInt("FUNDUS_TTL_MAILBOX_DAYS", 60),
		TTLDirectoryDays:  envInt("FUNDUS_TTL_DIRECTORY_DAYS", 365),
		TTLHistoryDays:    envInt("FUNDUS_TTL_HISTORY_DAYS", 365),
		TurnUser:          envStr("FUNDUS_TURN_USER", ""),
		TurnPass:          envStr("FUNDUS_TURN_PASS", ""),
		ShopBridgePrivKey: envStr("FUNDUS_SHOP_BRIDGE_PRIV_KEY", ""),
		ShopBridgeAuthority: envStr("FUNDUS_SHOP_BRIDGE_AUTHORITY", ""),
		ShopSlippageBPS: envInt("FUNDUS_SHOP_SLIPPAGE_BPS", 50),
		ShopOfferTTLSec: envInt("FUNDUS_SHOP_OFFER_TTL_SEC", 90),

		ChainID:            int64(envInt("FUNDUS_CHAIN_ID", 100)),
		ChainRPCURL:        envStr("FUNDUS_CHAIN_RPC_URL", "https://rpc.gnosischain.com"),
		FNDAddress:         envStr("FUNDUS_FND_ADDRESS", ""),
		EnergyTokenAddress: envStr("FUNDUS_ENERGY_TOKEN_ADDRESS", ""),
		MarketAddress:      envStr("FUNDUS_MARKET_ADDRESS", ""),
		FileStorageAddress: envStr("FUNDUS_FILE_STORAGE_ADDRESS", ""),
		WalletPrivKey:      envStr("FUNDUS_WALLET_PRIV_KEY", ""),
		SeedPassword:       envStr("FUNDUS_SEED_PASSWORD", ""),
		Validators:         envStrList("FUNDUS_VALIDATORS", ""),
		UpdateManifestURL:  updateURL(envStr("FUNDUS_UPDATE_MANIFEST_URL", DefaultUpdateManifestURL)),
		UpdateAuto:         envBool("FUNDUS_UPDATE_AUTO", false),
		DonatePayPal:       updateURL(envStr("FUNDUS_DONATE_PAYPAL", DefaultDonatePayPal)),
		StorageRewardAddr:  envStr("FUNDUS_STORAGE_REWARD_ADDR", ""),
		FNDFeeCollector:    envStr("FUNDUS_FND_FEE_COLLECTOR", ""),
		NodeType:          envStr("FUNDUS_NODE_TYPE", "consumer"),
		VoltageLevel:      envStr("FUNDUS_VOLTAGE_LEVEL", "lv"),
		NodeLat:           envFloat("FUNDUS_NODE_LAT", 0),
		NodeLon:           envFloat("FUNDUS_NODE_LON", 0),
		ParentSubstation:  envStr("FUNDUS_PARENT_SUBSTATION", ""),
		TransitFeePercent: envFloat("FUNDUS_TRANSIT_FEE_PERCENT", 0),
		GMapsAPIKey:       envStr("FUNDUS_GMAPS_API_KEY", ""),

		StorageOfferGB: int64(envInt("FUNDUS_STORAGE_OFFER_GB", 0)),
		StorageAllocGB: int64(envInt("FUNDUS_STORAGE_ALLOC_GB", 0)),
		StorageDir:     envStr("FUNDUS_STORAGE_DIR", "/opt/fundus/chunks"),
		SeedFile:       envStr("FUNDUS_SEED_FILE", ""),
	}

	cfg.P2PRelays = envStrList("FUNDUS_P2P_RELAYS", "")
	cfg.P2PAnnounce = envStrList("FUNDUS_P2P_ANNOUNCE", "")
	cfg.P2PRelayService = envBool("FUNDUS_P2P_RELAY_SERVICE", true)

	// Bootstrap-Peers: kommagetrennte Multiaddr-Liste
	raw := envStr("FUNDUS_BOOTSTRAP_PEERS", "")
	if raw != "" {
		for _, addr := range strings.Split(raw, ",") {
			addr = strings.TrimSpace(addr)
			if addr != "" {
				cfg.BootstrapPeers = append(cfg.BootstrapPeers, addr)
			}
		}
	}

	if err := os.MkdirAll(cfg.DataDir, 0750); err != nil {
		log.Warn("Could not create data dir", zap.String("path", cfg.DataDir), zap.Error(err))
	}

	// StorageOfferGB == -1 (Default): automatisch maximal 50% des freien
	// Speichers RESERVIEREN. Wichtig: der Filestore reserviert AllocGB = 5×
	// OfferGB auf der Disk (eigene Chunks + 4× Spiegel anderer Nodes). Die 50%
	// beziehen sich daher auf den ALLOKIERTEN Platz, NICHT auf OfferGB:
	//     AllocGB = freeGB / 2      (max. halbe Platte wird belegt)
	//     OfferGB = AllocGB / 5     (was dem Netz angeboten wird)
	// Sonst wuerde OfferGB=50% → AllocGB=250% die Platte sprengen.
	// Der Nutzer kann FUNDUS_STORAGE_OFFER_GB=0 setzen (aus) oder feste GB.
	if cfg.StorageOfferGB == -1 {
		probeDir := cfg.StorageDir
		if err := os.MkdirAll(probeDir, 0750); err != nil {
			probeDir = cfg.DataDir
		}
		// Eigene alte Reservierung entfernen BEVOR wir den freien Platz messen.
		// Sonst zaehlt die eigene storage.alloc als "belegt" und die Messung
		// faellt zu niedrig aus (Henne-Ei: jede Messung sieht die vorige
		// Reservierung). Der Filestore legt sie gleich darauf korrekt neu an.
		_ = os.Remove(filepath.Join(probeDir, "storage.alloc"))
		_ = os.Remove(filepath.Join(cfg.DataDir, "storage.alloc"))
		freeGB, ok := freeDiskGB(probeDir)
		if ok {
			allocGB := freeGB / 2  // hoechstens halbe Platte reservieren
			offerGB := allocGB / 5 // 1/5 davon dem Netz anbieten (Rest = Spiegel)
			if offerGB < 1 {
				// Zu wenig Platz fuer sinnvolles Angebot -> Filesharing aus
				log.Warn("Zu wenig freier Speicher fuer Filesharing, deaktiviert",
					zap.Int64("free_gb", freeGB))
				cfg.StorageOfferGB = 0
			} else {
				cfg.StorageOfferGB = offerGB
				cfg.StorageAllocGB = allocGB
				log.Info("Storage auto-konfiguriert (max. 50% des freien Speichers reserviert)",
					zap.String("probe_dir", probeDir),
					zap.Int64("free_gb", freeGB),
					zap.Int64("alloc_gb", allocGB),
					zap.Int64("offer_gb", offerGB))
			}
		} else {
			// Freien Speicher nicht ermittelbar: NICHT hart deaktivieren (der -1-
			// Default soll ein funktionierendes Filesharing ergeben), sondern ein
			// konservatives Mindest-Angebot setzen. So bleibt Upload/Filesharing
			// nutzbar, statt still auszufallen.
			log.Warn("Freien Speicher nicht ermittelbar — setze konservatives Mindest-Angebot (1 GB)",
				zap.String("probe_dir", probeDir))
			cfg.StorageOfferGB = 1
			cfg.StorageAllocGB = 5 // 5 × OfferGB
		}
	}

	return cfg
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envStrList liest eine komma-getrennte Liste aus einer Umgebungsvariable und
// liefert die getrimmten, nicht-leeren Einträge. Für FUNDUS_VALIDATORS.
func envStrList(key, def string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		raw = def
	}
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// envFloat64 liest eine float64-Umgebungsvariable.
func envFloat64(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// DefaultUpdateManifestURL: offizielles Release-Manifest im GitHub-Repo.
const DefaultUpdateManifestURL = "https://raw.githubusercontent.com/t0b1as/FND/main/manifest.json"

// updateURL: "off"/"none"/"0" schaltet die Update-Prüfung ab.
func updateURL(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "none", "0", "false":
		return ""
	}
	return v
}

// DefaultDonatePayPal: PayPal-Konto für freiwillige Spenden an das Projekt.
const DefaultDonatePayPal = "tobias.kornmayer@gmail.com"
