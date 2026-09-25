// Package filestore implementiert anonymes, dezentrales, fünffach-redundantes
// Filesharing für den Fundus Marketplace.
//
// Kryptographie-Stack (SHA/AES werden als kompromittiert betrachtet):
//   - Chunk-Verschlüsselung: XChaCha20-Poly1305 (kein AES; kein Timing-Angriff auf ARM)
//   - Content-Hashing:       BLAKE3-256 (kein SHA-256; kryptographisch stärker)
//   - Key-Derivation:        Argon2id (memory-hard, Brute-Force-resistent)
//   - Signaturen:            Ed25519 / Argon2id-Commitment
//
// Datenfluss Upload:
//   File → Chunks (1 MB) → XChaCha20-Encrypt (Key=Argon2id(content)) →
//   Store lokal + 4 Remote-Peers via libp2p Stream →
//   Manifest im DHT ("/fundus/file/<blake3_hash>")
//
// Datenfluss Download:
//   blake3_hash → DHT-Manifest → Chunk-Locations →
//   Fetch von beliebigem der 5 Peers → Decrypt → Reassemble
package filestore

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
	"golang.org/x/crypto/argon2"
	"lukechampine.com/blake3"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/fundus/node/internal/chain"
)

// =============================================================================
//  Konstanten
// =============================================================================

const (
	// Chunk-Größe: 16 MiB. Größere Chunks → weniger Hashes pro Datei (bei 128
	// GiB: 8192 statt 131072), weniger Overhead, schnellere Verteilung.
	// RAM-Abwägung (Pi mit 1 GB!): Pro laufendem Transfer ist EIN Chunk (16 MiB)
	// im Speicher (Upload-Buffer bzw. Download-Fetch). Sequentiell unkritisch;
	// bei sehr vielen parallelen Transfers im Auge behalten. Das eigentliche
	// 128-GiB-Limit löst NICHT die Chunk-Größe, sondern das mehrstufige Manifest
	// (manifest_multi.go) — die Chunk-Größe ist nur ein Overhead-/Tempo-Hebel.
	ChunkSize = 16 << 20 // 16 MiB

	// MaxFileSize: harte Obergrenze für einen Upload (128 GiB).
	MaxFileSize = 128 << 30 // 128 GiB

	// Ziel-Replikationsfaktor (Default, wenn der User nichts wählt)
	TargetReplicas = 5

	// Minimale Replikation bevor Upload als erfolgreich gilt. Auf 1 gesenkt,
	// damit der Nutzer im Dropdown bewusst 1x (nur lokal) oder 2x waehlen kann —
	// z.B. im Single-Node-Betrieb oder fuer unkritische Daten. Die UI warnt bei
	// niedriger Redundanz vor Datenverlust. Wer Ausfallschutz will, waehlt 3x+.
	MinReplicas = 1

	// Maximale wählbare Replikation (Obergrenze gegen Netz-Speicher-Missbrauch).
	MaxReplicas = 8

	// Prüfintervall für Replikationsintegrität
	ReplicationCheckInterval = 10 * time.Minute

	// Vergütung: FND-Einheiten pro Byte (entspricht 1 FND/TB)
	FNDperTB = 1.0
	BytesPerTB = 1 << 40 // 1 TiB in Bytes

	// DHT-Namespaces
	DHTNamespaceFile  = "/fundus/file/"  // Datei-Manifest
	DHTNamespaceChunk = "/fundus/chunk/" // Chunk-Locations
	DHTNamespaceOffer = "/fundus/offer/" // Storage-Angebote

	// libp2p-Protokoll für Chunk-Transfer
	ChunkProtocol = "/fundus/chunk/1.0.0"

	// Argon2id-Parameter für Schlüsselableitung
	a2Memory  = 64 * 1024
	a2Time    = 1  // Reduziert für schnelle Chunk-Verschlüsselung
	a2Threads = 4
	a2KeyLen  = 32
)

// =============================================================================
//  Datenstrukturen
// =============================================================================

// FileManifest beschreibt eine vollständige Datei im Netzwerk.
// Wird im DHT unter DHTNamespaceFile+ContentHash gespeichert.
// FileManifest: Datei-Metadaten die im DHT gespeichert werden.
// Integrität ist über den Inhalts-Hash gesichert (Download prüft den Gesamt-
// hash gegen den angefragten contentHash + jeden Chunk gegen seinen Hash).
// ManifestSig/OwnerFundusID sind für Autorschaft/Mutability reserviert (R002).
type FileManifest struct {
	// Signatur-Felder (C-5: DHT-Manifest-Poisoning-Schutz)
	OwnerFundusID string `json:"owner_id,omitempty"` // FundusID des Uploaders
	ManifestSig   []byte `json:"manifest_sig,omitempty"` // Ed25519(owner_privkey, BLAKE3(Manifest))

	ContentHash  string      `json:"content_hash"` // BLAKE3 des Klartexts (hex)
	Size         int64       `json:"size"`         // Gesamtgröße in Bytes
	ChunkHashes  []string    `json:"chunk_hashes"` // BLAKE3 jedes Chunks (inline, kleine Dateien)
	ManifestChunks []string  `json:"manifest_chunks,omitempty"` // bei großen Dateien: Hashes der Sub-Manifeste (statt inline ChunkHashes)
	ChunkCount   int         `json:"chunk_count"`
	ChunkSize    int64       `json:"chunk_size,omitempty"` // Chunk-Größe dieser Datei (für Resume/Offset-Rechnung)
	MimeType     string      `json:"mime_type,omitempty"`
	Plaintext    bool        `json:"plaintext,omitempty"` // true = Klartext (neu, schnell). Fehlt/false = verschlüsselt (Altbestand, XChaCha20).
	CreatedAt    time.Time   `json:"created_at"`
	ExpiresAt    *time.Time  `json:"expires_at,omitempty"` // nil = unbegrenzt
}

// ChunkLocation beschreibt wo ein Chunk gespeichert ist.
// Wird im DHT unter DHTNamespaceChunk+ChunkHash gespeichert.
type ChunkLocation struct {
	ChunkHash string    `json:"chunk_hash"`
	PeerIDs   []string  `json:"peer_ids"`   // bis zu 5 Peer-IDs
	UpdatedAt time.Time `json:"updated_at"`
}

// StorageOffer beschreibt den angebotenen Speicherplatz eines Nodes.
type StorageOffer struct {
	PeerID      string    `json:"peer_id"`
	TotalGB     int64     `json:"total_gb"`
	UsedGB      int64     `json:"used_gb"`
	FreeGB      int64     `json:"free_gb"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ChunkRequest ist die Anfrage beim Chunk-Transfer-Protokoll.
type ChunkRequest struct {
	Op        string `json:"op"`         // "store" | "fetch" | "delete" | "ping" | "receipt"
	ChunkHash string `json:"chunk_hash"`
	Data      []byte `json:"data,omitempty"` // Chunk-Daten bei "store"
	Receipt   []byte `json:"receipt,omitempty"` // signierte Quittung bei Op=="receipt" (Phase 2b)
}

// ChunkResponse ist die Antwort beim Chunk-Transfer-Protokoll.
type ChunkResponse struct {
	OK     bool   `json:"ok"`
	Data   []byte `json:"data,omitempty"`    // Chunk-Daten bei "fetch"
	Error  string `json:"error,omitempty"`
	FreeGB int64  `json:"free_gb,omitempty"` // freier Speicher des Peers (bei "ping")
	ProviderAddr string `json:"provider_addr,omitempty"` // Wallet-Adresse des Providers (Phase 2b)
}

// =============================================================================
//  Store-Konfiguration und Abhängigkeiten
// =============================================================================

// Config konfiguriert den FileStore.
type Config struct {
	DataDir string

	// OfferGB: dem Netz angebotene GB (andere Nodes speichern Chunks hier).
	// Vergütungs-Basis: 1 FND/TB gesendet + 1 FND/TB/Monat.
	OfferGB int64

	// AllocGB: tatsächlich auf Disk reservierte GB.
	// MINIMUM: 5 × OfferGB (eigene Chunks + 4× Spiegel).
	// Wird beim Start als Alloc-Datei auf der Disk reserviert.
	// Kann zur Laufzeit erweitert werden (Expand).
	AllocGB int64

	// PeerID
	PeerID string

	// WalletPrivKey: Hex-kodierter secp256k1-Schlüssel des Node-Betreibers.
	// Wird genutzt, um Quittungen (Receipts) zu signieren, die dieser Node als
	// KONSUMENT ausstellt (bestätigt erhaltene/eingelagerte Bytes gegenüber dem
	// Provider). Leer = keine Quittungen (Verdienst-Nachweis deaktiviert).
	WalletPrivKey string

	// RewardAddr: Betreiber-Wallet, auf die der Storage-/Transfer-Verdienst
	// gebucht wird. Leer = die aus WalletPrivKey abgeleitete Node-Adresse.
	RewardAddr string

	// KeyDir: Verzeichnis für den persistenten Node-Schlüssel (node.key).
	// BEWUSST getrennt vom Chunk-DataDir: Der Wallet-Schlüssel muss auch dann
	// erhalten bleiben, wenn die Chunk-Daten auf ein anderes Volume wandern oder
	// gelöscht werden. Leer = DataDir (Rückwärtskompatibilität).
	KeyDir string

	// SeedPassword: Passwort zum Entsperren einer verschlüsselten Node-Seed beim Start.
	SeedPassword string
}

// P2PAdapter ist das Interface das FileStore vom P2P-Layer braucht.
type P2PAdapter interface {
	ID() interface{ String() string }
	Peers() []interface{ String() string }
	DHTput(ctx context.Context, key string, value []byte) error
	DHTget(ctx context.Context, key string) ([]byte, error)
	SendToPeer(ctx context.Context, peerID string, protocol string, data []byte) error
	SendAndReceive(ctx context.Context, peerID string, protocol string, data []byte) ([]byte, error)
	RegisterProtocol(protocol string, handler func(peerID string, data []byte) []byte)
	SetTopicHandler(topic string, handler func(data []byte))
	Publish(ctx context.Context, topic string, data []byte) error
}

// =============================================================================
//  FileStore
// =============================================================================

// FileStore verwaltet das dezentrale Filesharing.
type FileStore struct {
	cfg    Config
	p2p    P2PAdapter
	log    *zap.Logger

	// peerHints: Peers, von denen gerade Inhalte angefordert werden (z.B. der
	// Besitzer eines Netzwerksuche-Treffers) → werden beim Abruf zuerst gefragt.
	// Wert: time.Time (gültig bis).
	peerHints sync.Map

	mu       sync.RWMutex
	used     int64
	// chunks: Hash → Volume-Pfad, auf dem der Chunk liegt. Bei Multi-Volume-
	// Storage kann ein Chunk auf dem Haupt-DataDir oder einem zusätzlich
	// freigegebenen Laufwerk liegen; der Pfad sagt, wo gelesen/gelöscht wird.
	chunks   map[string]string

	// volumes verwaltet die Speicherorte (Haupt-DataDir + zusätzliche Laufwerke).
	volumes  *volumeManager

	// chunkRefs zählt Owner-Referenzen pro Chunk (Reference Counting). Ein Chunk
	// wird physisch erst gelöscht, wenn die letzte Referenz entfernt wird —
	// verhindert verwaiste Chunks und schützt fremde gehostete Daten.
	chunkRefs *chunkRefIndex

	// chunkReplicas merkt sich die beim Upload gewählte Redundanz pro Chunk,
	// damit der Replikations-Manager sie respektiert (statt stur TargetReplicas).
	chunkReplicas *chunkReplicasIndex

	// nodeSeedWords hält die Seed-Wörter der Node-Wallet NUR, wenn sie in dieser
	// Sitzung neu erzeugt wurde — zur einmaligen Anzeige/Sicherung durch den
	// Betreiber. Nach dem Abruf wird das Feld geleert (nicht dauerhaft im RAM).
	nodeSeedWords []string

	// keyDir merkt sich das Verzeichnis der Node-Wallet-Dateien (node.seed), damit
	// die Seed jederzeit für Anzeige/Neuerstellung gelesen/geschrieben werden kann.
	keyDir string

	// hostingLedger (Konsumenten-Seite): merkt sich, welche eigenen Chunks bei
	// welchen fremden Providern eingelagert sind, um periodisch die Vorhaltung
	// zu challengen und Vorhaltungs-Quittungen (1 FND/TB·Monat) auszustellen.
	hostingLedger *hostingLedger

	// orphanSuspects verfolgt gehostete Chunks, die verwaist scheinen (im Netz
	// nicht mehr verankert), über mehrere GC-Durchläufe — für die Grace Period,
	// bevor ein Replikat freigegeben wird. Persistent gegen Neustarts.
	orphanSuspects *orphanTracker

	// Persönlicher verschlüsselter Index (eigene Uploads)
	index    *PersonalIndex

	// Öffentlich geteilte Dateien (netzweit auffindbar)
	shared   *SharedStore

	// Lokaler Datei-Index (immer aktiv, unabhängig von Wallet/Seed)
	localIdx *LocalIndex

	// Billing
	bytesSent   int64
	bytesStored int64

	// Begrenzung gleichzeitiger Chunk-Fetches: Schutz gegen RAM-Erschöpfung,
	// da jeder In-Flight-Chunk bis zu ChunkSize (16 MiB) im Speicher hält.
	// Auf einem 1-GB-Pi sind viele parallele Transfers sonst gefährlich.
	fetchSem chan struct{}

	// Begrenzung gleichzeitiger Replikationen (Upload→Peer). Ohne Drosselung
	// starten bei großen Dateien Dutzende Goroutinen gleichzeitig und überlasten
	// schwache Remote-Pis, sodass Repliken verloren gehen. Wenige Worker, die
	// auf Bestätigung warten, sind zuverlässiger.
	replicaSem chan struct{}

	// Fairness-Buchhaltung: kumulativ fremd belegter Speicher (Give-to-Get).
	remoteConsumed *remoteConsumed

	// Identität für Quittungen (Phase 2b). signerKey signiert Receipts, die
	// dieser Node als Konsument ausstellt; selfAddr ist seine Wallet-Adresse
	// (Provider-Feld in eingehenden Quittungen); rewardAddr ist das Ziel des
	// Verdienstes. Alle drei können null sein, wenn kein WalletPrivKey gesetzt ist.
	signerKey  *ecdsa.PrivateKey
	selfAddr   chain.Address
	rewardAddr chain.Address

	// Eingegangene, vom Konsumenten signierte Quittungen (Verdienst-Nachweise),
	// die dieser Node als Provider gesammelt hat — Basis fürs spätere Minten.
	receipts *receiptStore

	// Optionaler Fortschritts-Callback fürs Chunking (von runFinish gesetzt),
	// damit die UI den Finalisierungs-Fortschritt anzeigen kann. Geschützt durch
	// progressMu, da UploadWithRedundancy in einer Goroutine läuft.
	progressMu sync.Mutex
	progressCb func(phase string, done, total int)
}

// New erstellt einen FileStore.
func New(cfg Config, p2p P2PAdapter, log *zap.Logger) (*FileStore, error) {
	chunkDir := filepath.Join(cfg.DataDir, "chunks")
	tmpDir   := filepath.Join(cfg.DataDir, "tmp")
	for _, d := range []string{chunkDir, tmpDir} {
		if err := os.MkdirAll(d, 0750); err != nil {
			return nil, fmt.Errorf("filestore: verzeichnis erstellen %s: %w", d, err)
		}
	}

	// ── Disk-Vorab-Allokation ────────────────────────────────────────────────
	// Reserviert cfg.AllocGB auf der Disk (= 5× OfferGB minimum).
	// Verhindert dass der Node Speicher verspricht den er nicht hat.
	allocFile := filepath.Join(cfg.DataDir, "storage.alloc")
	if cfg.AllocGB > 0 {
		if err := preallocateDisk(allocFile, cfg.AllocGB); err != nil {
			return nil, fmt.Errorf("filestore: disk-allokation %d GB: %w", cfg.AllocGB, err)
		}
		log.Info("Disk-Allokation erfolgreich",
			zap.Int64("allocGB",  cfg.AllocGB),
			zap.Int64("offerGB",  cfg.OfferGB),
			zap.Int64("ratio",    cfg.AllocGB/max64(cfg.OfferGB, 1)),
			zap.String("file",    allocFile),
		)
	}

	fs := &FileStore{
		cfg:    cfg,
		p2p:    p2p,
		log:    log,
		chunks: make(map[string]string),
		volumes: newVolumeManager(cfg.DataDir, log),
		chunkRefs: newChunkRefIndex(cfg.DataDir),
		chunkReplicas: newChunkReplicasIndex(cfg.DataDir),
		hostingLedger: newHostingLedger(cfg.DataDir),
		shared: NewSharedStore(cfg.DataDir),
		localIdx: NewLocalIndex(cfg.DataDir),
		// Max. 8 gleichzeitige Chunk-Fetches → höchstens ~128 MiB Chunk-Buffer
		// im schlimmsten Fall (8 × 16 MiB), sicher unter 1 GB RAM.
		fetchSem: make(chan struct{}, 8),
		replicaSem: make(chan struct{}, 3),
		remoteConsumed: newRemoteConsumed(cfg.DataDir),
		receipts: newReceiptStore(cfg.DataDir, log),
	}

	// Identität für Quittungen ableiten (Phase 2b). Der Node braucht einen
	// eigenen Schlüssel, um Quittungen zu signieren und FND zu verdienen. Jeder
	// Node hat SEINEN EIGENEN Schlüssel (dezentral, wie eine persönliche Wallet)
	// — er wird NIEMALS mitdeployed. Priorität:
	//   1. Explizit konfigurierter WalletPrivKey (User importiert eigene Seed).
	//   2. Sonst: persistenter Node-Schlüssel aus <DataDir>/node.key — beim
	//      ersten Start automatisch erzeugt, danach geladen. So bekommt jeder
	//      User automatisch seine eigene Wallet, ohne manuelle Konfiguration.
	var nodeKey *ecdsa.PrivateKey
	if cfg.WalletPrivKey != "" {
		keyHex := strings.TrimPrefix(cfg.WalletPrivKey, "0x")
		if key, err := crypto.HexToECDSA(keyHex); err != nil {
			log.Warn("Quittungs-Schlüssel ungültig, Verdienst-Nachweis deaktiviert", zap.Error(err))
		} else {
			nodeKey = key
			log.Info("Node-Wallet: konfigurierter Schlüssel (FUNDUS_WALLET_PRIV_KEY)")
		}
	} else {
		// Auto-Generierung: persistente Node-Wallet (SEED-BASIERT, wiederherstellbar)
		// laden oder anlegen. KeyDir bevorzugt (stabil, getrennt von Chunk-Daten).
		keyDir := cfg.KeyDir
		if keyDir == "" {
			keyDir = cfg.DataDir
		}
		fs.keyDir = keyDir
		// Verschlüsselte Seed? Dann kein Klartext-Laden — Passwort nötig.
		if fs.SeedIsEncrypted() {
			envPass := cfg.SeedPassword
			if envPass != "" {
				if err := fs.UnlockEncryptedSeed(envPass); err != nil {
					log.Warn("Node-Wallet: Entsperren mit FUNDUS_SEED_PASSWORD fehlgeschlagen — Wallet gesperrt, Passwort über Weboberfläche nachreichen", zap.Error(err))
				} else {
					nodeKey = fs.signerKey
					log.Info("Node-Wallet: verschlüsselte Seed via FUNDUS_SEED_PASSWORD entsperrt",
						zap.String("address", fs.selfAddr.Hex()))
				}
			} else {
				log.Warn("Node-Wallet: verschlüsselte Seed vorhanden, aber kein Passwort — Wallet GESPERRT. Node läuft eingeschränkt (keine Quittungen), bis Passwort über die Weboberfläche eingegeben wird.")
			}
		} else {
		info, err := loadOrCreateNodeWallet(keyDir)
		if err != nil {
			log.Warn("Node-Wallet konnte nicht erzeugt/geladen werden, Verdienst-Nachweis deaktiviert", zap.Error(err))
		} else {
			nodeKey = info.Key
			fs.nodeSeedWords = info.SeedWords // nur bei Neuerzeugung gesetzt
			if info.Created {
				log.Info("Node-Wallet: neue seed-basierte Wallet erzeugt und gespeichert",
					zap.String("address", info.Address),
					zap.String("pfad", filepath.Join(keyDir, nodeSeedFile)),
					zap.String("hinweis", "Seed-Wörter über die Wallet-Seite sichern (einmalig anzeigbar)!"))
			} else if info.FromSeed {
				log.Info("Node-Wallet: seed-basierte Wallet geladen", zap.String("address", info.Address))
			} else {
				log.Info("Node-Wallet: alter Hex-Schlüssel geladen (nicht wiederherstellbar — Migration empfohlen)",
					zap.String("address", info.Address))
			}
		}
		}
	}

	if nodeKey != nil {
		fs.signerKey = nodeKey
		fs.selfAddr = chain.PubkeyToAddress(&nodeKey.PublicKey)
		// Reward-Adresse: in den Einstellungen gesetzt (reward_addr.txt) >
		// FUNDUS_STORAGE_REWARD_ADDR > Node-Adresse.
		fs.applyRewardAddr()
		log.Info("Quittungs-Identität aktiv",
			zap.String("node_addr", fs.selfAddr.Hex()),
			zap.String("reward_addr", fs.rewardAddr.Hex()))
	}

	// Vorhandene Chunks einlesen — über ALLE Volumes (Haupt-DataDir +
	// zusätzlich freigegebene Laufwerke). Jeder Chunk merkt sich, auf welchem
	// Volume er liegt (für Lesen/Löschen). Liegt ein Chunk auf mehreren Volumes
	// (Altbestand), gewinnt der erste Fund.
	var loadChunks func(volPath, dir string)
	loadChunks = func(volPath, dir string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				loadChunks(volPath, filepath.Join(dir, name))
				continue
			}
			if strings.HasSuffix(name, ".tmp") {
				continue // halbe Schreibvorgänge ignorieren
			}
			if _, seen := fs.chunks[name]; seen {
				continue // schon auf einem anderen Volume gefunden
			}
			fs.chunks[name] = volPath
			if fi, err := e.Info(); err == nil {
				fs.used += fi.Size()
			}
		}
	}
	for _, v := range fs.volumes.list() {
		loadChunks(v.Path, v.chunksDir())
	}

	// Chunk-Transfer-Protokoll registrieren
	p2p.RegisterProtocol(ChunkProtocol, fs.handleChunkRequest)

	// Einmalige Migration: Wenn der chunkRefs-Index leer ist (erstes Mal nach
	// dem Update), aber bereits Chunks/Dateien existieren, die Referenzen aus
	// den vorhandenen Manifesten rekonstruieren. So werden bestehende Chunks
	// korrekt ihren Dateien zugeordnet, statt als "verwaist" zu gelten.
	fs.migrateChunkRefsIfNeeded()

	log.Info("FileStore bereit",
		zap.String("dir",    chunkDir),
		zap.Int64("offerGB", cfg.OfferGB),
		zap.Int("chunks",   len(fs.chunks)),
	)

	return fs, nil
}

// =============================================================================
//  Upload
// =============================================================================

// Upload nimmt eine Datei entgegen und verteilt sie mit der Standard-Redundanz
// (TargetReplicas). Wrapper um UploadWithRedundancy für Rückwärtskompatibilität.
func (fs *FileStore) Upload(ctx context.Context, r io.Reader, mimeType, fileName string) (contentHash string, err error) {
	return fs.UploadWithRedundancy(ctx, r, mimeType, fileName, TargetReplicas)
}

// UploadWithRedundancy verteilt die Datei mit der gewählten Replikatzahl.
// replicas wird auf [MinReplicas, MaxReplicas] begrenzt. Lokal zählt als 1
// Replik, also werden (replicas-1) Remote-Peers angeschrieben.
func (fs *FileStore) UploadWithRedundancy(ctx context.Context, r io.Reader, mimeType, fileName string, replicas int) (contentHash string, err error) {
	if replicas < MinReplicas {
		replicas = MinReplicas
	}
	if replicas > MaxReplicas {
		replicas = MaxReplicas
	}
	remoteReplicas := replicas - 1 // lokal = 1 Replik
	// Datei in temporäre Datei schreiben und gleichzeitig BLAKE3-256 berechnen
	tmpFile, err := os.CreateTemp(filepath.Join(fs.cfg.DataDir, "tmp"), "upload-*.bin")
	if err != nil {
		return "", fmt.Errorf("filestore: tmp erstellen: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	h := blake3.New(32, nil)
	// Obergrenze beim Streamen durchsetzen: LimitReader auf MaxFileSize+1, damit
	// eine zu große Datei erkannt wird, ohne sie vollständig zu schreiben.
	limited := io.LimitReader(r, MaxFileSize+1)
	size, err := io.Copy(io.MultiWriter(tmpFile, h), limited)
	if err != nil {
		return "", fmt.Errorf("filestore: lesen: %w", err)
	}
	if size > MaxFileSize {
		return "", fmt.Errorf("filestore: Datei überschreitet Obergrenze (max %d GiB)", MaxFileSize>>30)
	}
	tmpFile.Seek(0, 0)

	contentHash = hex.EncodeToString(h.Sum(nil))
	fs.log.Info("Finalisierung: Daten gelesen, beginne Chunking",
		zap.Int64("size", size), zap.String("hash", contentHash[:16]+"…"))
	// Geschätzte Chunk-Gesamtzahl für die Fortschrittsanzeige.
	estTotal := int((size + ChunkSize - 1) / ChunkSize)
	fs.reportProgress("chunking", 0, estTotal)

	// In Chunks aufteilen und verteilen. Dateien werden im KLARTEXT gespeichert
	// (öffentlicher Marktplatz/Fileshare → Speed; Convergent Encryption bot
	// ohnehin keine Vertraulichkeit, da der Hash öffentlich propagiert). Der
	// Chunk-Hash ist BLAKE3 des Klartexts → Deduplizierung bleibt erhalten.
	manifest := &FileManifest{
		ContentHash: contentHash,
		Size:        size,
		MimeType:    mimeType,
		Plaintext:   true,
		CreatedAt:   time.Now().UTC(),
	}

	chunkNum := 0
	buf := make([]byte, ChunkSize)
	// Peers EINMAL vor der Schleife bestimmen (nicht pro Chunk neu).
	remotePeers := fs.selectPeers(remoteReplicas)
	// WaitGroup für die gedrosselte Chunk-Replikation: der Upload wartet am Ende
	// kurz auf die Spiegelung, damit ein sofortiger Remote-Download die Chunks
	// findet. Schlägt eine Replikation fehl, fängt der robuste Download + das
	// Self-Heal sie später ab.
	var repWG sync.WaitGroup
	for {
		// io.ReadFull statt tmpFile.Read: ein einzelner Read() darf WENIGER als
		// len(buf) liefern (kurzer Read), was sonst zu Chunks unterschiedlicher
		// Groesse und damit anderer Hash-Grenzen fuehren koennte. ReadFull fuellt
		// den Buffer garantiert voll, ausser am Dateiende (ErrUnexpectedEOF).
		n, readErr := io.ReadFull(tmpFile, buf)
		if n == 0 && (readErr == io.EOF || readErr == io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return "", fmt.Errorf("filestore: chunk lesen: %w", readErr)
		}

		plainChunk := buf[:n]
		// Chunk-Hash = BLAKE3 des Klartexts (Content-Addressing + Dedup)
		ch := blake3Sum256(plainChunk)
		chunkHash := hex.EncodeToString(ch[:])
		// Kopie, da buf im naechsten Durchlauf ueberschrieben wird
		chunkData := make([]byte, n)
		copy(chunkData, plainChunk)

		manifest.ChunkHashes = append(manifest.ChunkHashes, chunkHash)

		// Gewählte Redundanz für diesen Chunk merken, damit der Replikations-
		// Manager sie respektiert (statt stur auf TargetReplicas zu gehen).
		fs.chunkReplicas.set(chunkHash, replicas)
		fs.log.Info("DEBUG Upload: Redundanz gespeichert",
			zap.String("chunk", chunkHash[:16]), zap.Int("replicas", replicas))

		// Lokal speichern (zählt als 1 von 5 Repliken)
		if err := fs.storeChunkWithOwner(chunkHash, chunkData, "file:"+contentHash); err != nil {
			if isReadOnlyErr(err) {
				return "", fmt.Errorf("Speicher-Laufwerk ist schreibgeschützt (read-only). Häufig bei exFAT/NTFS-Platten von Windows: bitte 'sudo bash /opt/fundus/tools/mount-drive.sh <gerät>' ausführen (behebt das dirty-Flag und bindet die Platte stabil ein), dann erneut versuchen")
			}
			return "", fmt.Errorf("filestore: chunk %d lokal speichern: %w", chunkNum, err)
		}

		// Replikation zu Peers GEDROSSELT (max. replicaSem parallel) und per
		// WaitGroup verfolgt. So werden schwache Remote-Pis nicht von Dutzenden
		// gleichzeitigen Transfers überrollt (Hauptursache verlorener Repliken).
		peerIDs := []string{fs.cfg.PeerID}
		if len(remotePeers) > 0 {
			ch := chunkHash
			cd := chunkData // bereits eine Kopie
			peers := remotePeers
			repWG.Add(1)
			go func() {
				defer repWG.Done()
				fs.replicaSem <- struct{}{}
				defer func() { <-fs.replicaSem }()
				// Pro Chunk großzügiger Timeout (60s), da der Remote-Pi langsam
				// schreibt; lieber etwas warten als die Replik verlieren.
				rctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				for _, pid := range peers {
					_ = fs.replicateChunkToPeer(rctx, pid, ch, cd)
				}
			}()
			peerIDs = append(peerIDs, remotePeers...)
		}

		// Chunk-Location im DHT speichern (asynchron + mit Timeout)
		loc := ChunkLocation{
			ChunkHash: chunkHash,
			PeerIDs:   peerIDs,
			UpdatedAt: time.Now().UTC(),
		}
		if locData, e := json.Marshal(loc); e == nil {
			key := DHTNamespaceChunk + chunkHash
			go func() {
				dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = fs.p2p.DHTput(dctx, key, locData)
			}()
		}

		chunkNum++
		// Der SD-Karte (und damit anderen HTTP-Anfragen wie Seitennavigation)
		// regelmäßig kurz Luft geben: Beim Chunking großer Dateien sättigt der
		// Lese-/Schreibstrom sonst die Disk, sodass parallele Seitenaufrufe
		// hängen (schwarzer Bildschirm beim Wegnavigieren). Alle 4 Chunks eine
		// winzige Pause — kostet kaum Zeit, hält den Node aber bedienbar.
		if chunkNum%4 == 0 {
			time.Sleep(20 * time.Millisecond)
		}
		// Fortschritt melden (gedrosselt: jeder Chunk ist hier ohnehin grob-
		// granular, also pro Chunk ok). estTotal ist eine Schätzung; bei mehr-
		// stufigen Manifesten kann die finale Zahl leicht abweichen.
		fs.reportProgress("chunking", chunkNum, estTotal)
		fs.log.Debug("Chunk verteilt",
			zap.Int("num",      chunkNum),
			zap.Int("replicas", len(peerIDs)),
		)
	}

	manifest.ChunkCount = chunkNum
	manifest.ChunkSize = ChunkSize

	// Die gedrosselte Chunk-Replikation läuft im Hintergrund weiter. Wir
	// blockieren den Upload-Abschluss NICHT mehr darauf (das verlängerte das
	// Postprocessing spürbar). Stattdessen wartet der Download per Retry kurz,
	// falls eine Replik noch unterwegs ist. Die WaitGroup wird in einer
	// separaten Goroutine abgewartet, nur fürs Log.
	if len(remotePeers) > 0 {
		go func() {
			done := make(chan struct{})
			go func() { repWG.Wait(); close(done) }()
			select {
			case <-done:
				fs.log.Info("Replikation abgeschlossen", zap.Int("chunks", chunkNum))
			case <-time.After(10 * time.Minute):
				fs.log.Warn("Replikation-Timeout — Self-Heal holt Rest nach",
					zap.Int("chunks", chunkNum))
			}
		}()
	}
	fs.log.Info("Finalisierung: Chunks verteilt, baue Manifest",
		zap.Int("chunks", chunkNum))

	// Mehrstufiges Manifest bei großen Dateien: passt die Chunk-Hash-Liste nicht
	// mehr inline (DHT-Limit ~64 KiB), in Sub-Manifeste auslagern. Jedes Sub-
	// Manifest wird als normaler, inhaltsadressierter Chunk gespeichert; das
	// Root-Manifest trägt dann nur deren Hashes.
	if needsMultiLevel(manifest.ChunkHashes) {
		subBlocks, serr := splitIntoSubManifests(manifest.ChunkHashes)
		if serr != nil {
			return "", fmt.Errorf("filestore: submanifest split: %w", serr)
		}
		subHashes := make([]string, 0, len(subBlocks))
		for _, block := range subBlocks {
			sh := blake3Sum256(block)
			shHex := hex.EncodeToString(sh[:])
			if err := fs.storeChunkWithOwner(shHex, block, "file:"+contentHash); err != nil {
				return "", fmt.Errorf("filestore: submanifest lokal speichern: %w", err)
			}
			// Sub-Manifeste asynchron replizieren (selbstverifizierend) — darf den
			// Upload nicht blockieren (siehe Manifest-Replikation unten).
			if smPeers := fs.selectPeers(remoteReplicas); len(smPeers) > 0 {
				smKey := shHex
				smData := block
				peers := smPeers
				go func() {
					rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					for _, pid := range peers {
						_ = fs.replicateChunkToPeer(rctx, pid, smKey, smData)
					}
				}()
			}
			subHashes = append(subHashes, shHex)
		}
		manifest.ManifestChunks = subHashes
		manifest.ChunkHashes = nil // nicht mehr inline → Root bleibt klein
		fs.log.Info("Mehrstufiges Manifest erstellt",
			zap.Int("sub_manifeste", len(subHashes)),
			zap.Int("chunks_gesamt", chunkNum))
	}

	// Manifest im DHT ablegen
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("filestore: manifest marshal: %w", err)
	}
	// Manifest LOKAL speichern (als Chunk), damit der Download auch ohne
	// DHT funktioniert. Lokaler Schluessel ohne Namespace-Slashes.
	localManifestKey := "manifest_" + contentHash
	if err := fs.storeChunkWithOwner(localManifestKey, manifestData, "file:"+contentHash); err != nil {
		return "", fmt.Errorf("filestore: manifest lokal speichern: %w", err)
	}
	// Manifest auch direkt zu den verbundenen Peers replizieren, damit der
	// Remote-Pi es sofort lokal hat (kein DHT-Umweg beim ersten Oeffnen).
	// ASYNCHRON: Seit die Replikation auf eine Antwort wartet (Quittungs-Fluss,
	// Phase 2b), darf diese Schleife den Upload NICHT mehr blockieren — sonst
	// wird der lokale Index-Write unten verzoegert und die Datei erscheint nicht.
	if mremotePeers := fs.selectPeers(remoteReplicas); len(mremotePeers) > 0 {
		mkey := localManifestKey
		mdata := manifestData
		peers := mremotePeers
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, pid := range peers {
				_ = fs.replicateChunkToPeer(rctx, pid, mkey, mdata)
			}
		}()
	}
	// DHT-Put asynchron + mit Timeout (blockiert den Upload nicht mehr).
	go func() {
		dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := fs.p2p.DHTput(dctx, DHTNamespaceFile+contentHash, manifestData); err != nil {
			fs.log.Warn("Manifest-DHT-Put fehlgeschlagen (lokal vorhanden)", zap.Error(err))
		}
	}()

	// Lokalen Index IMMER aktualisieren (unabhängig von Wallet/Seed) →
	// die Dateiliste funktioniert dadurch auch ohne eingerichtete Wallet.
	fs.log.Info("Finalisierung: Manifest gespeichert, schreibe Index")
	if fs.localIdx != nil {
		fs.localIdx.Add(contentHash, fileName, size, mimeType)
	}

	// Fairness-Buchhaltung: die durch diesen Upload verursachte Remote-Last
	// (replicas-1)×Größe verbuchen, damit künftige Uploads korrekt gegen OfferGB
	// geprüft werden.
	fs.bookRemoteLoad(size, replicas)

	// Persönlichen Index aktualisieren
	if fs.index != nil {
		fs.index.Add(&IndexEntry{
			ContentHash: contentHash,
			Size:        size,
			MimeType:    mimeType,
			FileName:    fileName,
			AddedAt:     time.Now().UTC(),
		})
		// Asynchron in DHT schreiben
		go func() {
			saveCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := fs.index.Save(saveCtx, fs.p2p); err != nil {
				fs.log.Warn("Index-Speichern fehlgeschlagen", zap.Error(err))
			}
		}()
	}

	// Storage-Nutzung tracking
	fs.mu.Lock()
	fs.bytesStored += size
	fs.mu.Unlock()

	fs.log.Info("Upload abgeschlossen",
		zap.String("hash",   contentHash[:16]+"…"),
		zap.Int64("size",    size),
		zap.Int("chunks",   chunkNum),
	)

	return contentHash, nil
}

// =============================================================================
//  Download
// =============================================================================

// Download lädt eine Datei anhand ihres Content-Hashes herunter.
func (fs *FileStore) Download(ctx context.Context, contentHash string, w io.Writer) error {
	// Manifest zuerst LOKAL versuchen (schnell, funktioniert ohne DHT),
	// dann direkt von Peers, dann erst aus dem DHT.
	manifestKey := "manifest_" + contentHash
	manifestData, err := fs.loadChunkLocal(manifestKey)
	triedPeers := 0
	if err != nil || len(manifestData) == 0 {
		// Direkt verbundene Peers fragen (schnell)
		directPeers := fs.selectPeers(8)
		triedPeers = len(directPeers)
		if len(directPeers) > 0 {
			if d, e := fs.fetchChunkFromPeers(ctx, manifestKey, directPeers); e == nil && len(d) > 0 {
				manifestData = d
				_ = fs.storeChunkLocal(manifestKey, d) // cachen
			}
		}
	}
	if len(manifestData) == 0 {
		// DHT-Fallback (entfernte Peers)
		dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		manifestData, err = fs.p2p.DHTget(dctx, DHTNamespaceFile+contentHash)
		if err != nil {
			return fmt.Errorf("filestore: manifest nicht gefunden für %s (direkte Peers: %d, DHT-Fehler: %v)",
				contentHash[:16]+"…", triedPeers, err)
		}
	}

	var manifest FileManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("manifest unmarshal: %w", err)
	}

	// Mehrstufiges Manifest: Sub-Manifeste laden und die vollständige Chunk-
	// Hash-Liste rekonstruieren, bevor der eigentliche Download beginnt. Die
	// Sub-Manifeste sind selbst inhaltsadressierte Chunks (über ihren Hash
	// verifiziert beim Abruf), also vertrauenswürdig zusammensetzbar.
	if len(manifest.ManifestChunks) > 0 {
		subDatas := make([][]byte, 0, len(manifest.ManifestChunks))
		for i, subHash := range manifest.ManifestChunks {
			d, ferr := fs.fetchChunk(ctx, subHash)
			if ferr != nil {
				return fmt.Errorf("filestore: sub-manifest %d/%d (%s) nicht abrufbar: %w",
					i+1, len(manifest.ManifestChunks), subHash[:12]+"…", ferr)
			}
			subDatas = append(subDatas, d)
		}
		full, rerr := reassembleFromSubManifests(subDatas)
		if rerr != nil {
			return fmt.Errorf("filestore: sub-manifest rekonstruieren: %w", rerr)
		}
		manifest.ChunkHashes = full
	}
	// C-5 Integritätsschutz: Das Manifest selbst trägt KEINEN Vertrauensanker
	// (die Owner-Signatur ManifestSig/OwnerFundusID bleibt für R002 reserviert
	// und betrifft Autorschaft/Mutability, NICHT Integrität). Der Inhalts-
	// Integritätsschutz erfolgt vertrauensanker-frei und stärker: (1) jeder
	// Chunk wird in fetchChunkFromPeers gegen seinen Hash geprüft, (2) der
	// Gesamthash des ausgelieferten Inhalts wird unten gegen den ANGEFRAGTEN
	// contentHash verifiziert (fängt DHT-Manifest-Poisoning, auch wenn alle
	// Chunks einzeln hash-konsistent sind).

	// Schlüssel nur bei verschlüsselten (Altbestands-)Dateien ableiten.
	var encKey []byte
	if !manifest.Plaintext {
		encKey = fs.deriveEncKey(contentHash)
	}

	// C-5: Manifest-Poisoning-Schutz ohne Vertrauensanker. Wir binden den
	// gesamten ausgelieferten Inhalt an den ANGEFRAGTEN contentHash: ein
	// Angreifer kann zwar ein Manifest mit selbstkonsistenten (einzeln
	// hash-gültigen) Chunks unter unserem contentHash ablegen, aber der
	// Gesamthash des Inhalts würde dann nicht zum angefragten contentHash
	// passen. Wir hashen den Output-Stream mit und prüfen am Ende.
	// WICHTIG: derselbe Hasher wie im Upload-Pfad (blake3.New(32,nil), Z.~280),
	// sonst schlägt die Prüfung systematisch fehl.
	hasher := blake3.New(32, nil)
	verifyWriter := io.MultiWriter(w, hasher)

	// Paralleler Download: bis zu parallelFetchWindow Chunks gleichzeitig
	// vorausholen (nutzt die 5-fache Replikation über mehrere Hosts), aber
	// strikt in Reihenfolge schreiben (Datei-Integrität + Gesamthash).
	emitErr := orderedParallelFetch(ctx, manifest.ChunkHashes, parallelFetchWindow,
		fs.fetchChunk,
		func(i int, chunk []byte) error {
			// Klartext-Datei → direkt; verschlüsselte (alt) → entschlüsseln
			plain := chunk
			if !manifest.Plaintext {
				var decErr error
				plain, decErr = fs.decryptChunk(chunk, encKey, i)
				if decErr != nil {
					return fmt.Errorf("filestore: chunk %d entschlüsseln: %w", i, decErr)
				}
			}
			if _, err := verifyWriter.Write(plain); err != nil {
				return fmt.Errorf("filestore: chunk %d schreiben: %w", i, err)
			}
			fs.mu.Lock()
			fs.bytesSent += int64(len(plain))
			fs.mu.Unlock()
			return nil
		})
	if emitErr != nil {
		return emitErr
	}

	// Gesamthash gegen den angefragten contentHash prüfen (nur Klartext-Pfad,
	// wo contentHash == BLAKE3(Klartext) gilt).
	if manifest.Plaintext {
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != contentHash {
			return fmt.Errorf("filestore: Inhalts-Hash weicht ab (möglicher DHT-Manifest-Poisoning): angefragt %s, erhalten %s",
				contentHash[:12]+"…", got[:12]+"…")
		}
	}

	return nil
}

// =============================================================================
//  Replikations-Health-Check
// =============================================================================

// =============================================================================
//  Replikations-Manager
// =============================================================================
//
// Zwei parallele Loops:
//
//  A) Heal-Loop (alle 10 Min):
//     Eigene Chunks prüfen → Peers pingen → fehlende Repliken auffüllen
//
//  B) Fill-Loop (alle 5 Min):
//     Freien AllocGB-Platz mit fremden Chunks füllen die weniger als 5 Repliken haben
//     → Freiwillige Replikations-Host-Funktion
//     → Vergütung: 1 FND/TB/Monat für gehostete Chunks

const (
	HealInterval    = 10 * time.Minute
	FillInterval    = 5  * time.Minute
	PeerPingTimeout = 5  * time.Second
	// HostingRewardInterval: wie oft ein bei einem Provider eingelagerter Chunk
	// erneut per Challenge geprüft und quittiert wird. Für den Testbetrieb
	// bewusst kurz (1 h), damit man Vorhaltungs-Vergütung nicht einen Monat lang
	// abwarten muss; produktiv wäre z.B. 24 h sinnvoll. Die Vergütungshöhe hängt
	// NICHT vom Intervall ab (sie ist zeitproportional), nur die Häufigkeit der
	// Quittungen.
	HostingRewardInterval = 1 * time.Hour
	// GossipSub-Topic für Replikationsankündigungen
	TopicReplication = "fundus.replication"
)

// ReplicationAnnounce kündigt einen Chunk an der mehr Repliken braucht.
type ReplicationAnnounce struct {
	ChunkHash  string    `json:"chunk_hash"`
	ManifestKey string   `json:"manifest_key"` // DHT-Key des Datei-Manifests
	NeedPeers  int       `json:"need_peers"`   // Wie viele zusätzliche Repliken gesucht
	CurrentPeers []string `json:"current_peers"`
	Timestamp  time.Time `json:"ts"`
}

// targetReplicasFor liefert die gewünschte Replikatzahl für einen Chunk. Sie
// stammt aus der beim Upload gewählten Redundanz (chunkReplicas-Index); fehlt
// ein Eintrag (Altbestand oder fremd gehostete Chunks), gilt TargetReplicas.
func (fs *FileStore) targetReplicasFor(chunkHash string) int {
	if fs.chunkReplicas == nil {
		return TargetReplicas
	}
	return fs.chunkReplicas.get(chunkHash, TargetReplicas)
}

// RunReplicationManager startet beide Replikations-Loops.
// Blockiert bis ctx abgebrochen wird.
func (fs *FileStore) RunReplicationManager(ctx context.Context) {
	fs.log.Info("Replikations-Manager gestartet",
		zap.Duration("healInterval", HealInterval),
		zap.Duration("fillInterval", FillInterval),
		zap.Int("target", TargetReplicas),
	)

	// GossipSub-Topic für Replikationsankündigungen abonnieren
	fs.p2p.SetTopicHandler(TopicReplication, fs.handleReplicationAnnounce)

	healTicker := time.NewTicker(HealInterval)
	fillTicker  := time.NewTicker(FillInterval)
	gcTicker    := time.NewTicker(30 * time.Minute)
	defer healTicker.Stop()
	defer fillTicker.Stop()
	defer gcTicker.Stop()

	// Sofort einen ersten Durchlauf machen
	go fs.checkAndHealReplication(ctx)
	go fs.fillFreeSpace(ctx)
	// Vorhaltungs-Challenges: periodisch prüfen, ob Provider eingelagerte Chunks
	// noch halten, und Vorhaltungs-Quittungen (1 FND/TB·Monat) ausstellen. Der
	// Tick ist häufig (alle 5 min gucken), aber ein Eintrag wird erst nach
	// HostingRewardInterval erneut quittiert — so entsteht nicht bei jedem Tick
	// eine Quittung, sondern höchstens einmal pro Intervall pro Chunk.
	go fs.runHostingChallenges(ctx, 5*time.Minute, HostingRewardInterval)
	// Verwaiste unfertige Resume-Uploads beim Start aufraeumen (>24h alt)
	fs.GCResumeSessions(24 * time.Hour)
	// Verwaiste gehostete Replikate freigeben: host:-Chunks, die im Netz nicht
	// mehr verankert sind (Datei gelöscht oder Uploader dauerhaft offline).
	// Mit Grace Period, damit temporäre Ausfälle nie zu Datenverlust führen.
	go fs.runOrphanGC(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-healTicker.C:
			go fs.checkAndHealReplication(ctx)
		case <-fillTicker.C:
			go fs.fillFreeSpace(ctx)
		case <-gcTicker.C:
			fs.GCResumeSessions(24 * time.Hour)
		}
	}
}

// checkAndHealReplication prüft eigene Chunks und stellt fehlende Repliken her.
// Analog zur Tombstone-Propagation bei den Anzeigen.
func (fs *FileStore) checkAndHealReplication(ctx context.Context) {
	fs.mu.RLock()
	localChunks := make([]string, 0, len(fs.chunks))
	for h := range fs.chunks {
		localChunks = append(localChunks, h)
	}
	fs.mu.RUnlock()

	healed, lost := 0, 0
	for _, chunkHash := range localChunks {
		locData, err := fs.p2p.DHTget(ctx, DHTNamespaceChunk+chunkHash)
		if err != nil {
			continue
		}
		var loc ChunkLocation
		if err := json.Unmarshal(locData, &loc); err != nil {
			continue
		}

		// Alle bekannten Peers pingen
		alive := fs.pingPeers(ctx, loc.PeerIDs)
		dead  := difference(loc.PeerIDs, alive)

		if len(dead) > 0 {
			fs.log.Info("Ausgefallene Peers erkannt",
				zap.Int("dead",    len(dead)),
				zap.Int("alive",   len(alive)),
				zap.String("chunk", chunkHash[:16]+"…"),
			)
		}

		if len(alive) >= fs.targetReplicasFor(chunkHash) {
			// Genug Repliken – DHT ggf. bereinigen
			if len(alive) != len(loc.PeerIDs) {
				loc.PeerIDs   = alive
				loc.UpdatedAt = time.Now().UTC()
				if data, e := json.Marshal(loc); e == nil {
					_ = fs.p2p.DHTput(ctx, DHTNamespaceChunk+chunkHash, data)
				}
			}
			continue
		}

		// Zu wenige Repliken → neue Peers suchen und befüllen
		needed := fs.targetReplicasFor(chunkHash) - len(alive)
		chunk, err := fs.loadChunkLocal(chunkHash)
		if err != nil {
			// Wir haben den Chunk nicht lokal – von einem lebenden Peer holen
			chunk, err = fs.fetchChunkFromPeers(ctx, chunkHash, alive)
			if err != nil {
				fs.log.Warn("Chunk nicht abrufbar – Replikation unmöglich",
					zap.String("chunk", chunkHash[:16]+"…"),
					zap.Error(err),
				)
				lost++
				continue
			}
		}

		// Neue Peers finden die Platz haben
		candidates := fs.findPeersWithSpace(ctx, needed+2)
		candidates  = removeAll(candidates, alive)

		added := 0
		for _, pid := range candidates {
			if added >= needed { break }
			if err := fs.replicateChunkToPeer(ctx, pid, chunkHash, chunk); err != nil {
				fs.log.Debug("Replikation zu Peer fehlgeschlagen",
					zap.String("peer",  pid[:8]+"…"),
					zap.Error(err),
				)
				continue
			}
			alive = append(alive, pid)
			added++
			fs.log.Debug("Chunk repliziert",
				zap.String("peer",  pid[:8]+"…"),
				zap.String("chunk", chunkHash[:16]+"…"),
			)
		}

		if added > 0 {
			// DHT-Location aktualisieren
			loc.PeerIDs   = alive
			loc.UpdatedAt = time.Now().UTC()
			if data, e := json.Marshal(loc); e == nil {
				_ = fs.p2p.DHTput(ctx, DHTNamespaceChunk+chunkHash, data)
			}
			healed++
		}

		// Ankündigen dass dieser Chunk noch mehr Repliken braucht
		if len(alive) < fs.targetReplicasFor(chunkHash) {
			fs.log.Info("DEBUG Fill: brauche mehr Repliken",
				zap.String("chunk", chunkHash[:16]),
				zap.Int("alive", len(alive)),
				zap.Int("ziel", fs.targetReplicasFor(chunkHash)))
			fs.announceNeedReplicas(ctx, chunkHash, alive)
		}
	}

	if healed > 0 || lost > 0 {
		fs.log.Info("Replikations-Heal abgeschlossen",
			zap.Int("healed", healed),
			zap.Int("lost",   lost),
		)
	}
}

// fillFreeSpace füllt freien AllocGB-Platz mit fremden Chunks.
// Nutzt GossipSub-Ankündigungen und aktive DHT-Suche.
func (fs *FileStore) fillFreeSpace(ctx context.Context) {
	fs.mu.RLock()
	usedBytes   := fs.used
	maxBytes    := fs.cfg.AllocGB * 1024 * 1024 * 1024
	freeBytes   := maxBytes - usedBytes
	myID        := fs.cfg.PeerID
	fs.mu.RUnlock()

	if freeBytes < ChunkSize*2 {
		return // Zu wenig Platz – nichts tun
	}

	fs.log.Debug("Freien Speicher füllen",
		zap.Float64("freeGB", float64(freeBytes)/(1024*1024*1024)),
	)

	// Eigenes Storage-Angebot im DHT aktualisieren
	_ = fs.PublishOffer(ctx)

	// Peers anfragen: welche Chunks brauchen mehr Repliken?
	peers := fs.selectPeers(10)
	for _, pid := range peers {
		if freeBytes < ChunkSize { break }
		if pid == myID          { continue }

		chunksAccepted := fs.pullUnderreplicatedChunks(ctx, pid, freeBytes)
		freeBytes -= int64(chunksAccepted) * ChunkSize
	}
}

// pullUnderreplicatedChunks fragt einen Peer nach Chunks die weniger als 5 Repliken haben.
// Wir bieten uns als Replikations-Host an.
func (fs *FileStore) pullUnderreplicatedChunks(ctx context.Context, peerID string, freeBytes int64) int {
	// Anfrage: "gib mir Hashes von Chunks die du hast und die weniger als 5 Repliken haben"
	req := ChunkRequest{Op: "list_underreplicated"}
	reqData, _ := json.Marshal(req)

	respData, err := fs.sendAndReceive(ctx, peerID, reqData)
	if err != nil {
		return 0
	}

	var resp struct {
		Chunks []string `json:"chunks"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return 0
	}

	accepted := 0
	myID := fs.cfg.PeerID

	for _, chunkHash := range resp.Chunks {
		if int64(accepted+1)*ChunkSize > freeBytes {
			break
		}

		// DHT prüfen: sind wir schon drin?
		locData, err := fs.p2p.DHTget(ctx, DHTNamespaceChunk+chunkHash)
		if err != nil {
			continue
		}
		var loc ChunkLocation
		if json.Unmarshal(locData, &loc) != nil {
			continue
		}
		if contains(loc.PeerIDs, myID) {
			continue // Haben wir schon
		}
		if len(loc.PeerIDs) >= fs.targetReplicasFor(chunkHash) {
			continue // Genug Repliken vorhanden
		}

		// Chunk von Peer holen
		fetchReq  := ChunkRequest{Op: "fetch", ChunkHash: chunkHash}
		fetchData, _ := json.Marshal(fetchReq)
		chunkData, err := fs.sendAndReceive(ctx, peerID, fetchData)
		if err != nil {
			continue
		}

		var fetchResp ChunkResponse
		if json.Unmarshal(chunkData, &fetchResp) != nil || !fetchResp.OK {
			continue
		}

		// Integrität prüfen
		h := blake3Sum256(fetchResp.Data)
		if hex.EncodeToString(h[:]) != chunkHash {
			fs.log.Warn("Chunk-Hash-Mismatch beim Füllen", zap.String("peer", peerID[:8]))
			continue
		}

		// Lokal speichern
		if err := fs.storeChunkLocal(chunkHash, fetchResp.Data); err != nil {
			continue
		}

		// Uns als Replikations-Host im DHT eintragen
		loc.PeerIDs   = append(loc.PeerIDs, myID)
		loc.UpdatedAt = time.Now().UTC()
		if data, e := json.Marshal(loc); e == nil {
			_ = fs.p2p.DHTput(ctx, DHTNamespaceChunk+chunkHash, data)
		}

		accepted++
		fs.log.Debug("Chunk als Replikations-Host übernommen",
			zap.String("chunk", chunkHash[:16]+"…"),
			zap.String("from",  peerID[:8]+"…"),
			zap.Int("newReplicas", len(loc.PeerIDs)),
		)
	}

	return accepted
}

// announceNeedReplicas kündigt per GossipSub an dass ein Chunk mehr Repliken braucht.
// Andere Nodes mit freiem Platz können sich melden.
func (fs *FileStore) announceNeedReplicas(ctx context.Context, chunkHash string, currentPeers []string) {
	ann := ReplicationAnnounce{
		ChunkHash:    chunkHash,
		NeedPeers:    fs.targetReplicasFor(chunkHash) - len(currentPeers),
		CurrentPeers: currentPeers,
		Timestamp:    time.Now().UTC(),
	}
	data, _ := json.Marshal(ann)
	_ = fs.p2p.Publish(ctx, TopicReplication, data)
}

// handleReplicationAnnounce verarbeitet eingehende Replikations-Ankündigungen.
// Wenn wir Platz haben, übernehmen wir den Chunk freiwillig.
func (fs *FileStore) handleReplicationAnnounce(data []byte) {
	var ann ReplicationAnnounce
	if json.Unmarshal(data, &ann) != nil {
		return
	}

	fs.mu.RLock()
	usedBytes := fs.used
	maxBytes  := fs.cfg.AllocGB * 1024 * 1024 * 1024
	myID      := fs.cfg.PeerID
	_, haveIt := fs.chunks[ann.ChunkHash]
	fs.mu.RUnlock()

	// Bereits dabei oder kein Platz?
	if haveIt || contains(ann.CurrentPeers, myID) {
		return
	}
	if maxBytes-usedBytes < ChunkSize {
		return
	}

	// Von einem der vorhandenen Peers holen
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, pid := range ann.CurrentPeers {
		fetchReq  := ChunkRequest{Op: "fetch", ChunkHash: ann.ChunkHash}
		fetchData, _ := json.Marshal(fetchReq)
		respData, err := fs.sendAndReceive(ctx, pid, fetchData)
		if err != nil {
			continue
		}

		var resp ChunkResponse
		if json.Unmarshal(respData, &resp) != nil || !resp.OK {
			continue
		}

		h := blake3Sum256(resp.Data)
		if hex.EncodeToString(h[:]) != ann.ChunkHash {
			continue
		}

		if err := fs.storeChunkLocal(ann.ChunkHash, resp.Data); err != nil {
			continue
		}

		// Im DHT als neuer Host eintragen
		locData, err := fs.p2p.DHTget(ctx, DHTNamespaceChunk+ann.ChunkHash)
		if err == nil {
			var loc ChunkLocation
			if json.Unmarshal(locData, &loc) == nil {
				loc.PeerIDs   = append(loc.PeerIDs, myID)
				loc.UpdatedAt = time.Now().UTC()
				if d, e := json.Marshal(loc); e == nil {
					_ = fs.p2p.DHTput(ctx, DHTNamespaceChunk+ann.ChunkHash, d)
				}
			}
		}

		fs.log.Info("Replikations-Ankündigung beantwortet",
			zap.String("chunk", ann.ChunkHash[:16]+"…"),
			zap.String("from",  pid[:8]+"…"),
		)
		return
	}
}

// findPeersWithSpace sucht Peers die genug freien Speicher für einen Chunk haben.
func (fs *FileStore) findPeersWithSpace(ctx context.Context, count int) []string {
	peers     := fs.selectPeers(count * 3)
	result    := make([]string, 0, count)
	pingCtx, cancel := context.WithTimeout(ctx, PeerPingTimeout)
	defer cancel()

	for _, pid := range peers {
		if len(result) >= count { break }
		req, _  := json.Marshal(ChunkRequest{Op: "ping"})
		resp, err := fs.sendAndReceive(pingCtx, pid, req)
		if err != nil { continue }

		var cr ChunkResponse
		if json.Unmarshal(resp, &cr) == nil && cr.OK {
			result = append(result, pid)
		}
	}
	return result
}

// checkAvailability prüft VOR einem Upload, ob genug erreichbare Peers mit
// echtem freiem Platz existieren, um (redundancy-1) Remote-Repliken der Datei
// aufzunehmen. Fragt die Peers per ping ab (FreeGB) und summiert deren freien
// Platz, bis die geforderte Last gedeckt ist. Gibt nil zurück, wenn genug Platz
// im erreichbaren Netz vorhanden ist, sonst einen sprechenden Fehler.
//
// HINWEIS (netznah): Diese Prüfung spricht echte Peers an — Ergebnis hängt vom
// Live-Netz ab und ist nur dort verlässlich testbar.
func (fs *FileStore) checkAvailability(ctx context.Context, fileSize int64, redundancy int) error {
	remoteReplicas := redundancy - 1
	if remoteReplicas <= 0 {
		return nil // nur lokale Kopie, kein Fremd-Platz nötig
	}
	// Benötigte Last: jede der (redundancy-1) Repliken belegt fileSize.
	neededBytes := int64(remoteReplicas) * fileSize
	neededGB := neededBytes / (1024 * 1024 * 1024)
	if neededBytes%(1024*1024*1024) != 0 {
		neededGB++ // aufrunden
	}

	// Genug Peers ansprechen (mehr als remoteReplicas, da nicht alle Platz haben).
	peers := fs.selectPeers(remoteReplicas * 4)
	pingCtx, cancel := context.WithTimeout(ctx, PeerPingTimeout)
	defer cancel()

	var reachable int
	var totalFreeGB int64
	var withSpace int
	for _, pid := range peers {
		req, _ := json.Marshal(ChunkRequest{Op: "ping"})
		resp, err := fs.sendAndReceive(pingCtx, pid, req)
		if err != nil {
			continue
		}
		reachable++
		var cr ChunkResponse
		if json.Unmarshal(resp, &cr) == nil && cr.OK && cr.FreeGB > 0 {
			withSpace++
			totalFreeGB += cr.FreeGB
		}
	}

	// Fairness der Verteilung: wir brauchen MINDESTENS remoteReplicas
	// verschiedene Peers mit Platz (sonst lägen mehrere Repliken auf einem Host
	// → kein echter Ausfallschutz), UND deren freier Platz muss die Last decken.
	if withSpace < remoteReplicas {
		return fmt.Errorf(
			"Verfügbarkeit: nur %d erreichbare Peers mit Platz, benötigt werden %d (für %d Remote-Repliken). "+
				"Wähle eine geringere Redundanz oder warte auf mehr Peers",
			withSpace, remoteReplicas, remoteReplicas)
	}
	if totalFreeGB < neededGB {
		return fmt.Errorf(
			"Verfügbarkeit: freier Netz-Speicher %d GB deckt die benötigten %d GB nicht "+
				"(%d Repliken × %.2f GB). Wähle eine geringere Redundanz",
			totalFreeGB, neededGB, remoteReplicas, float64(fileSize)/1e9)
	}
	return nil
}

// fetchChunkFromPeers holt einen Chunk von einer Liste lebender Peers.
func (fs *FileStore) fetchChunkFromPeers(ctx context.Context, chunkHash string, peers []string) ([]byte, error) {
	req := ChunkRequest{Op: "fetch", ChunkHash: chunkHash}
	reqData, _ := json.Marshal(req)

	for _, pid := range peers {
		respData, err := fs.sendAndReceive(ctx, pid, reqData)
		if err != nil { continue }

		var resp ChunkResponse
		if json.Unmarshal(respData, &resp) != nil || !resp.OK { continue }

		// Integritäts-Check per Hash NUR für echte Chunks. Manifeste werden
		// unter dem Key "manifest_<contentHash>" abgelegt — deren Inhalt-Hash
		// entspricht NICHT dem Key, daher hier kein Hash-Vergleich.
		if !strings.HasPrefix(chunkHash, "manifest_") {
			h := blake3Sum256(resp.Data)
			if hex.EncodeToString(h[:]) != chunkHash {
				fs.log.Warn("Chunk-Hash-Mismatch", zap.String("peer", pid[:8]))
				continue
			}
		}

		// Quittung ausstellen (Phase 2b): Dem Provider den Empfang signiert
		// bestätigen. Best effort, blockiert den Transfer nicht. Nur wenn der
		// Provider seine Adresse mitgeschickt hat.
		if resp.ProviderAddr != "" {
			fs.issueReceipt(ctx, pid, resp.ProviderAddr, chunkHash, chain.ReceiptFetch, len(resp.Data))
		} else {
			fs.log.Info("DEBUG Peer-Fetch: Provider schickte KEINE Adresse mit",
				zap.String("peer", pid), zap.String("chunk", chunkHash[:16]))
		}
		return resp.Data, nil
	}
	return nil, fmt.Errorf("chunk %s von keinem Peer erhalten", chunkHash[:16])
}

// pingPeers prüft welche Peers erreichbar und als Replikations-Host nutzbar sind.
func (fs *FileStore) pingPeers(ctx context.Context, peerIDs []string) []string {
	alive := make([]string, 0, len(peerIDs))
	pingCtx, cancel := context.WithTimeout(ctx, PeerPingTimeout)
	defer cancel()

	for _, pid := range peerIDs {
		if pid == fs.cfg.PeerID {
			alive = append(alive, pid) // Wir selbst: immer alive
			continue
		}
		req, _ := json.Marshal(ChunkRequest{Op: "ping"})
		resp, err := fs.sendAndReceive(pingCtx, pid, req)
		if err != nil { continue }

		var cr ChunkResponse
		if json.Unmarshal(resp, &cr) == nil && cr.OK {
			alive = append(alive, pid)
		}
	}
	return alive
}

// SetTopicHandler verdrahtet GossipSub mit dem Replikations-Manager.
func (fs *FileStore) SetTopicHandler() {
	fs.p2p.SetTopicHandler(TopicReplication, fs.handleReplicationAnnounce)
}

// handleChunkRequest – "list_underreplicated" op ergänzen
func (fs *FileStore) handleListUnderreplicated(ctx context.Context) []string {
	fs.mu.RLock()
	localChunks := make([]string, 0, len(fs.chunks))
	for h := range fs.chunks { localChunks = append(localChunks, h) }
	fs.mu.RUnlock()

	result := make([]string, 0)
	for _, hash := range localChunks {
		locData, err := fs.p2p.DHTget(ctx, DHTNamespaceChunk+hash)
		if err != nil { continue }
		var loc ChunkLocation
		if json.Unmarshal(locData, &loc) != nil { continue }
		if len(loc.PeerIDs) < fs.targetReplicasFor(hash) {
			result = append(result, hash)
		}
	}
	return result
}

// =============================================================================
//  Chunk-Transfer-Protokoll (libp2p Stream Handler)
// =============================================================================

// handleChunkRequest verarbeitet eingehende Chunk-Anfragen von anderen Peers.
func (fs *FileStore) handleChunkRequest(peerID string, data []byte) []byte {
	var req ChunkRequest
	if err := json.Unmarshal(data, &req); err != nil {
		resp, _ := json.Marshal(ChunkResponse{OK: false, Error: "ungültige Anfrage"})
		return resp
	}

	switch req.Op {
	case "ping":
		fs.mu.RLock()
		free := (fs.cfg.AllocGB * 1024 * 1024 * 1024) - fs.used
		fs.mu.RUnlock()
		if free < ChunkSize {
			resp, _ := json.Marshal(ChunkResponse{OK: false, Error: "kein Speicher", FreeGB: 0})
			return resp
		}
		resp, _ := json.Marshal(ChunkResponse{OK: true, FreeGB: free / (1024 * 1024 * 1024)})
		return resp

	case "list_underreplicated":
		// Welche lokalen Chunks haben weniger als 5 Repliken?
		ctx2, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		hashes := fs.handleListUnderreplicated(ctx2)
		resp, _ := json.Marshal(map[string]interface{}{"ok": true, "chunks": hashes})
		return resp

	case "store":
		// Fremder Chunk fürs Netz — als gehostet markieren (host:-Owner), damit
		// die GC ihn nicht als verwaist löscht. Die Quittung (Op=="receipt")
		// liefert anschließend den echten Verdienst-Nachweis.
		if err := fs.storeChunkWithOwner(req.ChunkHash, req.Data, "host:remote"); err != nil {
			resp, _ := json.Marshal(ChunkResponse{OK: false, Error: err.Error()})
			return resp
		}

		// Telemetrie (lokal). Der ECHTE Verdienst-Nachweis läuft über Quittungen:
		// Der Konsument schickt anschließend per Op=="receipt" eine signierte
		// Store-Quittung, die dieser Node als Provider sammelt.
		fs.mu.Lock()
		fs.bytesStored += int64(len(req.Data))
		fs.mu.Unlock()

		resp, _ := json.Marshal(ChunkResponse{OK: true, ProviderAddr: fs.providerAddrHex()})
		return resp

	case "fetch":
		chunk, err := fs.loadChunkLocal(req.ChunkHash)
		if err != nil {
			resp, _ := json.Marshal(ChunkResponse{OK: false, Error: "chunk nicht gefunden"})
			return resp
		}

		// Telemetrie (lokal). Echter Nachweis: Konsument schickt danach per
		// Op=="receipt" eine signierte Fetch-Quittung.
		fs.mu.Lock()
		fs.bytesSent += int64(len(chunk))
		fs.mu.Unlock()

		resp, _ := json.Marshal(ChunkResponse{OK: true, Data: chunk, ProviderAddr: fs.providerAddrHex()})
		return resp

	case "receipt":
		// Konsument reicht eine signierte Quittung für eine zuvor erbrachte
		// store/fetch-Leistung nach. Annahme nur, wenn dieser Node der im
		// Receipt benannte Provider ist (sonst sammelte man fremde Nachweise).
		ok := fs.acceptReceipt(req.Receipt)
		resp, _ := json.Marshal(ChunkResponse{OK: ok})
		return resp

	case "delete":
		_ = fs.deleteChunkLocal(req.ChunkHash)
		resp, _ := json.Marshal(ChunkResponse{OK: true})
		return resp
	}

	resp, _ := json.Marshal(ChunkResponse{OK: false, Error: "unbekannte Operation"})
	return resp
}

// =============================================================================
//  Kryptographie
// =============================================================================

// deriveEncKey leitet den XChaCha20-Schlüssel aus dem Content-Hash ab via Argon2id.
// Gleicher Inhalt → gleicher Schlüssel → Content-Deduplication ohne SHA.
func (fs *FileStore) deriveEncKey(contentHash string) []byte {
	return argon2.IDKey(
		[]byte(contentHash),
		[]byte("fundus-file-xchacha20-v1"),
		a2Time, a2Memory, a2Threads, 32, // 256-Bit Key für XChaCha20
	)
}

// encryptChunk verschlüsselt einen Chunk mit XChaCha20-Poly1305.
// 192-Bit Nonce → keine Nonce-Kollisionen auch bei Millionen von Chunks.
func (fs *FileStore) encryptChunk(plain, key []byte, chunkNum int) (encrypted []byte, hash string, err error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, "", fmt.Errorf("xchacha20: %w", err)
	}

	// 24-Byte Nonce: 4 Bytes Chunk-Nummer + 20 Bytes Zufall
	nonce := make([]byte, aead.NonceSize()) // = 24 Bytes
	nonce[0] = byte(chunkNum >> 24)
	nonce[1] = byte(chunkNum >> 16)
	nonce[2] = byte(chunkNum >> 8)
	nonce[3] = byte(chunkNum)
	if _, err := rand.Read(nonce[4:]); err != nil {
		return nil, "", err
	}

	ciphertext := aead.Seal(nonce, nonce, plain, nil)

	// BLAKE3-256 für Content-Addressing (kein SHA-256)
	h := blake3Sum256(ciphertext)
	return ciphertext, hex.EncodeToString(h[:]), nil
}

// decryptChunk entschlüsselt einen Chunk via XChaCha20-Poly1305.
func (fs *FileStore) decryptChunk(ciphertext, key []byte, chunkNum int) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("xchacha20: %w", err)
	}

	if len(ciphertext) < aead.NonceSize() {
		return nil, fmt.Errorf("chunk zu kurz")
	}
	nonce     := ciphertext[:aead.NonceSize()]
	cipherData := ciphertext[aead.NonceSize():]

	return aead.Open(nil, nonce, cipherData, nil)
}

// =============================================================================
//  Lokaler Chunk-Store
// =============================================================================

// chunkPath liefert den Pfad eines VORHANDENEN Chunks (sucht das Volume, auf
// dem er liegt). Für neue Chunks wird das Ziel-Volume separat gewählt.
func (fs *FileStore) chunkPath(hash string) string {
	fs.mu.RLock()
	volPath, ok := fs.chunks[hash]
	fs.mu.RUnlock()
	if !ok {
		// Unbekannt → Default auf Primary-Volume (für Existenz-Checks).
		volPath = fs.cfg.DataDir
	}
	return filepath.Join(volPath, "chunks", hash[:2], hash)
}

// storeChunkLocal speichert einen Chunk ohne expliziten Owner (Default-Owner
// "cache"). Bestehende Aufrufer bleiben so kompatibel; Owner-bewusste Aufrufer
// nutzen storeChunkWithOwner.
func (fs *FileStore) storeChunkLocal(hash string, data []byte) error {
	return fs.storeChunkWithOwner(hash, data, "cache")
}

// storeChunkWithOwner speichert einen Chunk und vermerkt eine Owner-Referenz.
// Mehrfaches Speichern desselben Chunks dedupliziert die Daten und fügt nur eine
// weitere Referenz hinzu.
func (fs *FileStore) storeChunkWithOwner(hash string, data []byte, owner string) error {
	// Kapazitäts-Check: Limit ist AllocGB (tatsächlich reserviert)
	fs.mu.RLock()
	maxBytes    := fs.cfg.AllocGB * 1024 * 1024 * 1024
	enoughSpace := fs.used+int64(len(data)) <= maxBytes
	_, alreadyHave := fs.chunks[hash]
	fs.mu.RUnlock()

	if alreadyHave {
		// Daten liegen schon — nur die Owner-Referenz ergänzen (Dedup).
		if fs.chunkRefs != nil && owner != "" {
			fs.chunkRefs.addRef(hash, owner)
		}
		return nil
	}
	if !enoughSpace {
		return fmt.Errorf("filestore: Speicherkapazität erschöpft (%d GB angeboten)", fs.cfg.OfferGB)
	}

	// Ziel-Volume mit dem meisten freien Platz wählen (verteilt die Last auf
	// Haupt-DataDir + zusätzliche Laufwerke).
	vol := fs.volumes.volumeForNewChunk()
	path := vol.chunkPathFor(hash)

	// writeChunkTo versucht, den Chunk auf ein bestimmtes Volume zu schreiben.
	writeChunkTo := func(p string) error {
		if err := os.MkdirAll(filepath.Dir(p), 0750); err != nil {
			return err
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, data, 0640); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	}

	usedVol := vol
	if err := writeChunkTo(path); err != nil {
		// Bei JEDEM Schreibfehler auf einem nicht-primären Volume (z.B. exFAT-
		// Platte, die zur Laufzeit auf read-only umschaltet, oder andere I/O-
		// Fehler): automatisch auf das Primary-Volume (internes DataDir, immer
		// beschreibbar) ausweichen, statt den Upload abzubrechen.
		if !vol.Primary {
			primary := fs.volumes.primaryVolume()
			path = primary.chunkPathFor(hash)
			if err2 := writeChunkTo(path); err2 != nil {
				return err2
			}
			usedVol = primary
			if fs.log != nil {
				fs.log.Warn("Chunk auf Primary-Volume ausgewichen (Ziel nicht beschreibbar)",
					zap.String("chunk", hash[:16]), zap.Error(err))
			}
		} else {
			return err
		}
	}

	fs.mu.Lock()
	fs.chunks[hash] = usedVol.Path
	fs.used += int64(len(data))
	fs.mu.Unlock()

	if fs.chunkRefs != nil && owner != "" {
		fs.chunkRefs.addRef(hash, owner)
	}
	return nil
}

func (fs *FileStore) loadChunkLocal(hash string) ([]byte, error) {
	fs.mu.RLock()
	volPath, exists := fs.chunks[hash]
	fs.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("chunk %s nicht lokal vorhanden", hash[:16])
	}
	return os.ReadFile(filepath.Join(volPath, "chunks", hash[:2], hash))
}

// deleteChunkLocal entfernt einen Chunk bedingungslos (ohne Owner-Prüfung).
// Wird intern genutzt; Owner-bewusstes Löschen läuft über releaseChunk.
func (fs *FileStore) deleteChunkLocal(hash string) error {
	fs.mu.RLock()
	volPath, exists := fs.chunks[hash]
	fs.mu.RUnlock()
	if !exists {
		return nil
	}
	path := filepath.Join(volPath, "chunks", hash[:2], hash)
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	size := info.Size()
	if err := os.Remove(path); err != nil {
		return err
	}
	fs.mu.Lock()
	delete(fs.chunks, hash)
	fs.used -= size
	fs.mu.Unlock()
	return nil
}

// releaseChunk entfernt eine Owner-Referenz und löscht den Chunk physisch nur,
// wenn danach keine Referenz mehr übrig ist. Das ist der reguläre Weg, einen
// Chunk "freizugeben" (z.B. beim Löschen einer Datei).
func (fs *FileStore) releaseChunk(hash, owner string) error {
	if fs.chunkRefs == nil {
		return fs.deleteChunkLocal(hash) // ohne Index: hart löschen
	}
	if fs.chunkRefs.removeRef(hash, owner) {
		// Letzte Referenz weg → physisch löschen.
		return fs.deleteChunkLocal(hash)
	}
	return nil // andere Owner brauchen den Chunk noch
}

// =============================================================================
//  Peer-Management und Remote-Chunk-Transfer
// =============================================================================

// selectPeers wählt n Peers für Replikation aus.
func (fs *FileStore) selectPeers(n int) []string {
	result := make([]string, 0, n+2)
	seen := map[string]bool{}
	// 1. Hinweise zuerst (Besitzer aus der Netzwerksuche): sonst hängt es vom
	//    Zufall ab, ob er unter den n gefragten Peers ist – und bei dünner DHT
	//    wird eine vorhandene Datei als "nicht gefunden" gemeldet.
	now := time.Now()
	fs.peerHints.Range(func(k, v any) bool {
		id, _ := k.(string)
		until, _ := v.(time.Time)
		if id == "" || now.After(until) {
			fs.peerHints.Delete(k)
			return true
		}
		if id != fs.cfg.PeerID && !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
		return true
	})
	// 2. Verbundene Peers (bis n zusätzlich zu den Hinweisen).
	added := 0
	for _, p := range fs.p2p.Peers() {
		if added >= n {
			break
		}
		id := p.String()
		if seen[id] {
			continue
		}
		result = append(result, id)
		seen[id] = true
		added++
	}
	return result
}

// AddPeerHint merkt einen Peer 30 Minuten lang als bevorzugte Quelle (z.B. den
// Besitzer einer in der Netzwerksuche gefundenen Datei).
func (fs *FileStore) AddPeerHint(peerID string) {
	if peerID == "" || peerID == fs.cfg.PeerID {
		return
	}
	fs.peerHints.Store(peerID, time.Now().Add(30*time.Minute))
}

// reportProgress meldet den Chunking-Fortschritt an den gesetzten Callback
// (falls vorhanden). Threadsicher.
func (fs *FileStore) reportProgress(phase string, done, total int) {
	fs.progressMu.Lock()
	cb := fs.progressCb
	fs.progressMu.Unlock()
	if cb != nil {
		cb(phase, done, total)
	}
}

// setProgressCb setzt (oder löscht mit nil) den Fortschritts-Callback.
func (fs *FileStore) setProgressCb(cb func(phase string, done, total int)) {
	fs.progressMu.Lock()
	fs.progressCb = cb
	fs.progressMu.Unlock()
}

// replicateChunkToPeer sendet einen Chunk per libp2p Stream an einen Peer.
func (fs *FileStore) replicateChunkToPeer(ctx context.Context, peerID, hash string, data []byte) error {
	req := ChunkRequest{Op: "store", ChunkHash: hash, Data: data}
	reqData, err := json.Marshal(req)
	if err != nil {
		return err
	}
	// WICHTIG: eigener kurzer Timeout, damit die Replikation den Upload NICHT
	// blockiert. Der Aufrufer-ctx kann bis zu 6h laufen (runFinish); ohne eigene
	// Frist haengt die Finalisierung an einem langsamen Peer. 20s reichen fuer
	// einen Chunk; danach gilt die Replikation an diesen Peer als gescheitert.
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	respData, err := fs.sendAndReceive(sendCtx, peerID, reqData)
	if err != nil {
		return err
	}
	var resp ChunkResponse
	if json.Unmarshal(respData, &resp) != nil || !resp.OK {
		return fmt.Errorf("store bei peer %s abgelehnt", shortPeer(peerID))
	}
	// Quittung ueber die eingelagerten Bytes ausstellen (best effort, eigener
	// Timeout in issueReceipt). Darf den Replikations-Erfolg nicht beeinflussen.
	if resp.ProviderAddr != "" {
		fs.issueReceipt(ctx, peerID, resp.ProviderAddr, hash, chain.ReceiptStore, len(data))
		// Im Hosting-Ledger vermerken: DIESER Provider hält jetzt DIESEN Chunk.
		// Grundlage für die spätere periodische Vorhaltungs-Prüfung + Quittung.
		if fs.hostingLedger != nil {
			fs.hostingLedger.record(hash, resp.ProviderAddr, peerID, len(data))
		}
	}
	return nil
}

// fetchChunk holt einen Chunk – zuerst lokal, dann von Remote-Peers.
func (fs *FileStore) fetchChunk(ctx context.Context, hash string) ([]byte, error) {
	// 1. Lokal vorhanden? (billig, kein Semaphor nötig)
	if data, err := fs.loadChunkLocal(hash); err == nil {
		return data, nil
	}

	// Ab hier Netzwerk-Fetch: Semaphor begrenzt gleichzeitige In-Flight-Chunks
	// (RAM-Schutz, jeder Chunk bis ChunkSize groß). Bei abgebrochenem Kontext
	// nicht blockieren.
	select {
	case fs.fetchSem <- struct{}{}:
		defer func() { <-fs.fetchSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Mehrere Runden versuchen: Ein Chunk kann gerade noch repliziert werden
	// (Race zwischen Upload-Replikation und sofortigem Download). Statt sofort
	// aufzugeben (→ Stream-Abbruch, "content mismatch" im Browser), versuchen
	// wir es erneut — aber mit KURZEN Pausen, damit der Stream nicht so lange
	// stockt, dass der Browser/Proxy die Verbindung abbricht.
	const maxRounds = 4
	for round := 0; round < maxRounds; round++ {
		if data, ok := fs.tryFetchChunkOnce(ctx, hash); ok {
			return data, nil
		}
		if round < maxRounds-1 {
			select {
			case <-time.After(1500 * time.Millisecond): // feste, kurze Pause
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if data, err := fs.loadChunkLocal(hash); err == nil {
				return data, nil
			}
		}
	}
	return nil, fmt.Errorf("chunk %s nach %d Versuchen nicht abrufbar", hash[:16], maxRounds)
}

// tryFetchChunkOnce versucht EINMAL, einen Chunk über alle Quellen zu holen
// (direkte Peers → DHT-Locations). Gibt (data, true) bei Erfolg zurück.
func (fs *FileStore) tryFetchChunkOnce(ctx context.Context, hash string) ([]byte, bool) {
	// 2. DIREKT alle verbundenen Peers fragen (schnell bei direkter
	//    Verbindung – umgeht den langsamen DHT-Umweg in kleinen Netzen).
	directPeers := fs.selectPeers(8)
	if len(directPeers) > 0 {
		if data, err := fs.fetchChunkFromPeers(ctx, hash, directPeers); err == nil {
			_ = fs.storeChunkLocal(hash, data) // cachen
			return data, true
		}
	}

	// 3. Chunk-Locations aus DHT holen (Fallback fuer entfernte Peers)
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	locData, err := fs.p2p.DHTget(dctx, DHTNamespaceChunk+hash)
	if err != nil {
		return nil, false
	}
	var loc ChunkLocation
	if err := json.Unmarshal(locData, &loc); err != nil {
		return nil, false
	}

	req := ChunkRequest{Op: "fetch", ChunkHash: hash}
	reqData, _ := json.Marshal(req)

	for _, peerID := range loc.PeerIDs {
		if peerID == fs.cfg.PeerID { continue } // schon lokal geprüft
		respData, err := fs.sendAndReceive(ctx, peerID, reqData)
		if err != nil { continue }

		var resp ChunkResponse
		if err := json.Unmarshal(respData, &resp); err != nil { continue }
		if !resp.OK || len(resp.Data) == 0 { continue }

		// Integrität prüfen
		h := blake3Sum256(resp.Data)
		if hex.EncodeToString(h[:]) != hash {
			fs.log.Warn("Chunk-Hash-Mismatch", zap.String("peer", peerID))
			continue
		}

		_ = fs.storeChunkLocal(hash, resp.Data) // cachen
		// Quittung ausstellen (wie im direkten Peer-Pfad): Dem Provider den
		// Empfang bestätigen, wenn er seine Adresse mitgeschickt hat.
		if resp.ProviderAddr != "" {
			fs.issueReceipt(ctx, peerID, resp.ProviderAddr, hash, chain.ReceiptFetch, len(resp.Data))
		} else {
			fs.log.Info("DEBUG DHT-Fetch: Provider ohne Adresse", zap.String("peer", peerID))
		}
		return resp.Data, true
	}
	return nil, false
}

// sendAndReceive sendet eine Anfrage und liest die Antwort via bidirektionalem libp2p-Stream.
func (fs *FileStore) sendAndReceive(ctx context.Context, peerID string, data []byte) ([]byte, error) {
	return fs.p2p.SendAndReceive(ctx, peerID, ChunkProtocol, data)
}

// =============================================================================
//  Billing / Vergütung
// =============================================================================

// Stats gibt die aktuellen Billing-Metriken zurück.
func (fs *FileStore) Stats() map[string]interface{} {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	sentTB   := float64(fs.bytesSent)   / float64(BytesPerTB)
	storedTB := float64(fs.bytesStored) / float64(BytesPerTB)
	usedGB   := float64(fs.used)        / (1024 * 1024 * 1024)
	allocFreeGB := float64(fs.cfg.AllocGB) - usedGB
	// Lokal-freie Angebotsmenge (Anzeige): OfferGB minus eigener Belegung.
	offerFreeGB := float64(fs.cfg.OfferGB) - usedGB
	// Fairness-freie Menge: OfferGB minus der durch eigene Uploads VERURSACHTEN
	// Fremd-Last (das ist die Größe, gegen die Uploads geprüft werden).
	// remoteConsumed hat ein eigenes Mutex → kein Deadlock unter fs.mu.
	fairnessFreeGB := float64(fs.cfg.OfferGB*1024*1024*1024-fs.remoteConsumed.get()) / (1024 * 1024 * 1024)
	if fairnessFreeGB < 0 {
		fairnessFreeGB = 0
	}

	// "files" = Anzahl der EIGENEN, gelisteten Dateien (lokaler Index),
	// NICHT die Chunk-Anzahl. Chunks umfassen auch Replikate fremder
	// Dateien und gecachte Downloads — die gehören nicht in den Datei-Zähler.
	ownFiles := 0
	if fs.localIdx != nil {
		ownFiles = fs.localIdx.Count()
	}

	return map[string]interface{}{
		"enabled":               true, // Stats() existiert nur wenn FileStore aktiv
		"local_chunks":          len(fs.chunks),
		"chunks":                len(fs.chunks), // echte Chunk-Anzahl (inkl. Replikate)
		"files":                 ownFiles,       // nur eigene, gelistete Dateien
		"used_gb":               usedGB,
		"offer_gb":              fs.cfg.OfferGB,
		"fairness_min_gb":       fs.FairnessMinGB(),
		"offer_free_gb":         offerFreeGB,
		"fairness_free_gb":      fairnessFreeGB, // freie Angebots-Kapazität für neue Uploads
		"remote_consumed_gb":    float64(fs.remoteConsumed.get()) / (1024 * 1024 * 1024),
		"alloc_gb":              fs.cfg.AllocGB,
		"alloc_free_gb":         allocFreeGB,
		"alloc_ratio":           fs.cfg.AllocGB / max64(fs.cfg.OfferGB, 1),
		"bytes_sent":            fs.bytesSent,
		"bytes_stored":          fs.bytesStored,
		"sent_tb":               sentTB,
		"stored_tb":             storedTB,
		"earned_fnd_bandwidth":  sentTB * FNDperTB,
		"earned_fnd_storage":    storedTB * FNDperTB,
		"earned_fnd_total":      (sentTB + storedTB) * FNDperTB,
	}
}

// PublishOffer veröffentlicht das Storage-Angebot dieses Nodes im DHT.
func (fs *FileStore) PublishOffer(ctx context.Context) error {
	fs.mu.RLock()
	usedGB := fs.used / (1024 * 1024 * 1024)
	fs.mu.RUnlock()

	offer := StorageOffer{
		PeerID:    fs.cfg.PeerID,
		TotalGB:   fs.cfg.AllocGB,
		UsedGB:    usedGB,
		FreeGB:    fs.cfg.AllocGB - usedGB,
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(offer)
	if err != nil {
		return err
	}
	return fs.p2p.DHTput(ctx, DHTNamespaceOffer+fs.cfg.PeerID, data)
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item { return true }
	}
	return false
}

// difference gibt Elemente aus a zurück die nicht in b sind (a \ b).
func difference(a, b []string) []string {
	bSet := make(map[string]bool, len(b))
	for _, s := range b { bSet[s] = true }
	result := make([]string, 0)
	for _, s := range a {
		if !bSet[s] { result = append(result, s) }
	}
	return result
}

// removeAll entfernt alle Elemente von remove aus slice.
func removeAll(slice, remove []string) []string {
	rem := make(map[string]bool, len(remove))
	for _, s := range remove { rem[s] = true }
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if !rem[s] { result = append(result, s) }
	}
	return result
}

// =============================================================================
//  Automatischer Billing-Loop
// =============================================================================


// WithIndex lädt den persönlichen verschlüsselten Datei-Index aus dem DHT
// und startet bei Bedarf die Wiederherstellung eigener Dateien.
// seedWords: die 30 Fundus-Seed-Wörter.
func (fs *FileStore) WithIndex(ctx context.Context, seedWords []string) error {
	if len(seedWords) == 0 {
		fs.log.Warn("Keine Seed-Wörter – persönlicher Index deaktiviert")
		return nil
	}

	// FundusID aus Seed ableiten (gleiche Ableitung wie fnd-wallet)
	// Vereinfacht: aus dem Peer-ID Hash (deterministisch wenn Seed gleich)
	fundusID := fs.cfg.PeerID

	idx, err := LoadOrCreateIndex(ctx, fs.p2p, fundusID, seedWords)
	if err != nil {
		return fmt.Errorf("filestore: Index laden: %w", err)
	}
	fs.index = idx

	count := len(idx.ContentHashes())
	fs.log.Info("Persönlicher Datei-Index geladen",
		zap.Int("einträge",   count),
		zap.String("fundusID", fundusID[:12]+"…"),
	)

	// Wenn Dateien im Index vorhanden aber keine lokalen Chunks → neuer Pi
	fs.mu.RLock()
	localChunks := len(fs.chunks)
	fs.mu.RUnlock()

	if count > 0 && localChunks == 0 {
		fs.log.Info("Neuer Node erkannt – starte Daten-Wiederherstellung vom Netz…",
			zap.Int("dateien", count),
		)
		go func() {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
			defer cancel()
			if err := fs.RestoreFromNetwork(restoreCtx, idx, fs.log); err != nil {
				fs.log.Error("Wiederherstellung fehlgeschlagen", zap.Error(err))
			}
		}()
	}

	// Periodisch Index sichern (alle 15 Min falls dirty)
	go fs.runIndexSync(ctx)

	return nil
}

// runIndexSync speichert den Index periodisch wenn er dirty ist.
func (fs *FileStore) runIndexSync(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Beim Shutdown einmal speichern
			if fs.index != nil && fs.index.IsDirty() {
				saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = fs.index.Save(saveCtx, fs.p2p)
				cancel()
			}
			return
		case <-ticker.C:
			if fs.index != nil && fs.index.IsDirty() {
				if err := fs.index.Save(ctx, fs.p2p); err != nil {
					fs.log.Warn("Index-Sync fehlgeschlagen", zap.Error(err))
				}
			}
		}
	}
}
// Billing-Reports an den FileStorage Smart Contract.
//
// Ablauf:
//  1. RegisterAsProvider (einmalig)
//  2. Alle 24h: ReportBilling(bytesSent, 0=bandwidth)
//  3. Alle 24h: ReportBilling(bytesStored/30, 1=storage/Monat)
//  4. Eingehende confirmBilling-Anfragen anderer Peers per GossipSub bestätigen


// =============================================================================
//  Disk-Vorab-Allokation
// =============================================================================

// preallocateDisk reserviert Speicherplatz auf der Disk mittels Sparse File.
//
// Linux: nutzt fallocate(2) mit FALLOC_FL_KEEP_SIZE wenn verfügbar.
//   → Blockiert Disk-Blöcke ohne sie zu schreiben (Inodes reserviert).
//   → Datei erscheint im `df` als belegt, ist aber nicht im RAM.
//
// Fallback (SD-Karte / FAT32 / alte Kernel): seek + 1-Byte-Write
//   → Sparse file: Dateisystem reserviert nur Metadaten, nicht alle Blöcke.
//   → Hinweis: Auf FAT32 (manche Pi-Installationen) kein Sparse-File-Support.
//
// Sinn: Node kann keinen Speicher versprechen den die Disk nicht hat.
// Wenn preallocateDisk fehlschlägt, startet der FileStore nicht.
func preallocateDisk(path string, offerGB int64) error {
	totalBytes := offerGB * 1024 * 1024 * 1024

	// Prüfen ob die Alloc-Datei bereits in der richtigen Größe existiert
	if info, err := os.Stat(path); err == nil {
		if info.Size() >= totalBytes {
			return nil // bereits allokiert
		}
		// Größe hat sich geändert → neu allokieren
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("alloc-datei öffnen: %w", err)
	}
	defer f.Close()

	// 1. Versuch: fallocate (Linux, ext4/xfs/btrfs)
	// Reserviert echte Disk-Blöcke ohne Daten zu schreiben.
	if err := fallocate(f, totalBytes); err == nil {
		return nil
	}

	// 2. Fallback: Sparse File via Seek
	// Schreibt 1 Byte am Ende → Dateisystem merkt sich die Größe,
	// allokiert aber nur was tatsächlich beschrieben wird.
	if _, err := f.Seek(totalBytes-1, 0); err != nil {
		return fmt.Errorf("seek für sparse file: %w", err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		return fmt.Errorf("sparse file schreiben: %w", err)
	}

	return nil
}

// CheckDiskSpace prüft ob genug freier Speicher für die Allokation vorhanden ist.
func CheckDiskSpace(dir string, requiredGB int64) error {
	availBytes, _ := diskStatfs(dir)
	requiredBytes := requiredGB * 1024 * 1024 * 1024

	if availBytes < requiredBytes {
		return fmt.Errorf(
			"nicht genug Speicher: %d GB verfügbar, %d GB benötigt (%.1f GB fehlen)",
			availBytes/1024/1024/1024,
			requiredGB,
			float64(requiredBytes-availBytes)/1024/1024/1024,
		)
	}
	return nil
}

// DiskStats gibt den aktuellen Disk-Belegungsstand zurück.
func (fs *FileStore) DiskStats() map[string]interface{} {
	freeDisk, totalDisk := diskStatfs(fs.cfg.DataDir)
	usedDisk := totalDisk - freeDisk

	fs.mu.RLock()
	chunkCount := len(fs.chunks)
	chunkBytes := fs.used
	fs.mu.RUnlock()

	return map[string]interface{}{
		"disk_total_gb":   float64(totalDisk) / (1024 * 1024 * 1024),
		"disk_free_gb":    float64(freeDisk)  / (1024 * 1024 * 1024),
		"disk_used_gb":    float64(usedDisk)  / (1024 * 1024 * 1024),
		"alloc_offer_gb":  fs.cfg.OfferGB,
		"alloc_total_gb":  fs.cfg.AllocGB,
		"alloc_used_gb":   float64(chunkBytes) / (1024 * 1024 * 1024),
		"alloc_free_gb":   float64(fs.cfg.AllocGB*1024*1024*1024-chunkBytes) / (1024 * 1024 * 1024),
		"alloc_ratio":     fs.cfg.AllocGB / max64(fs.cfg.OfferGB, 1),
		"chunk_count":     chunkCount,
		"alloc_file":      filepath.Join(fs.cfg.DataDir, "storage.alloc"),
		"min_ratio_rule":  "alloc_gb ≥ 5 × offer_gb",
	}
}

// =============================================================================
//  Live-Expansion der Allokation
// =============================================================================

// ExpandResult beschreibt das Ergebnis einer Allokations-Erweiterung.
type ExpandResult struct {
	OldAllocGB  int64
	NewAllocGB  int64
	OldOfferGB  int64
	NewOfferGB  int64
	AllocFile   string
	DiskFreeGB  int64
}

// Expand erweitert die Disk-Allokation ohne Node-Neustart.
//
// newAllocGB:  neuer Gesamt-Allokationswert (muss > aktuell sein)
// newOfferGB:  neuer Netz-Angebot-Wert (optional, 0 = unverändert)
//
// Regeln:
//   - newAllocGB ≥ 5 × newOfferGB
//   - newAllocGB > aktuell (keine Verkleinerung)
//   - Genug freier Disk-Speicher muss vorhanden sein
func (fs *FileStore) Expand(newAllocGB int64, newOfferGB int64) (*ExpandResult, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	oldAlloc := fs.cfg.AllocGB
	oldOffer := fs.cfg.OfferGB

	if newOfferGB == 0 {
		newOfferGB = oldOffer
	}

	// Validierung
	if newAllocGB <= oldAlloc {
		return nil, fmt.Errorf(
			"filestore: Allokation kann nicht verkleinert werden (%d → %d GB). "+
				"Chunks müssen zuerst abgebaut werden.",
			oldAlloc, newAllocGB,
		)
	}

	minAlloc := newOfferGB * 5
	if newAllocGB < minAlloc {
		return nil, fmt.Errorf(
			"filestore: AllocGB=%d zu klein. Minimum: 5 × OfferGB = %d GB",
			newAllocGB, minAlloc,
		)
	}

	// Disk-Platz prüfen — über ALLE Volumes summiert (Haupt-DataDir + zusätzlich
	// freigegebene Laufwerke), da neue Chunks auf jedem Volume landen können.
	neededBytes := (newAllocGB - oldAlloc) * 1024 * 1024 * 1024
	if fs.volumes != nil {
		if total := fs.volumes.totalFreeBytes(); total < neededBytes {
			return nil, fmt.Errorf(
				"filestore: nicht genug freier Speicher über alle Volumes "+
					"(%d GB frei, %d GB benötigt). Gib ein weiteres Laufwerk frei.",
				total/(1024*1024*1024), neededBytes/(1024*1024*1024))
		}
	} else if err := CheckDiskSpace(fs.cfg.DataDir, newAllocGB-oldAlloc); err != nil {
		return nil, fmt.Errorf("filestore: nicht genug Disk-Platz für Erweiterung: %w", err)
	}

	// Alloc-Datei erweitern (fallocate unterstützt Erweiterung)
	allocFile := filepath.Join(fs.cfg.DataDir, "storage.alloc")
	if err := preallocateDisk(allocFile, newAllocGB); err != nil {
		return nil, fmt.Errorf("filestore: Disk-Erweiterung fehlgeschlagen: %w", err)
	}

	// Freien Disk-Platz nach Allokation
	freeAfter, _ := diskStatfs(fs.cfg.DataDir)
	diskFreeGB := freeAfter / (1024 * 1024 * 1024)

	// Config live aktualisieren
	fs.cfg.AllocGB = newAllocGB
	fs.cfg.OfferGB = newOfferGB

	fs.log.Info("Allokation erweitert",
		zap.Int64("oldAlloc", oldAlloc),
		zap.Int64("newAlloc", newAllocGB),
		zap.Int64("oldOffer", oldOffer),
		zap.Int64("newOffer", newOfferGB),
		zap.Int64("diskFree", diskFreeGB),
	)

	return &ExpandResult{
		OldAllocGB: oldAlloc,
		NewAllocGB: newAllocGB,
		OldOfferGB: oldOffer,
		NewOfferGB: newOfferGB,
		AllocFile:  allocFile,
		DiskFreeGB: diskFreeGB,
	}, nil
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func max64(a, b int64) int64 {
	if a > b { return a }
	return b
}

// blake3Sum256 berechnet den Content-Hash (BLAKE3-256, vereinheitlicht mit der
// Chain-Adressableitung und der Identitäts-Ableitung).
func blake3Sum256(data []byte) [32]byte {
	return blake3.Sum256(data)
}

// NodeWalletAddress liefert die Wallet-Adresse dieses Nodes (0x…) als Hex.
// Gegen diese Adresse wird der Admin-Login geprüft: nur wer sich mit dem
// passenden Wallet-Schlüssel anmeldet, ist der Eigentümer des Nodes.
func (fs *FileStore) NodeWalletAddress() string {
	if fs == nil {
		return ""
	}
	return fs.selfAddr.Hex()
}

// NameForHash liefert den bekannten Dateinamen zu einem Content-Hash (aus dem
// lokalen Index), oder leer wenn unbekannt. Für korrekte Download-Dateinamen.
func (fs *FileStore) NameForHash(hash string) string {
	if fs.localIdx == nil {
		return ""
	}
	return fs.localIdx.NameForHash(hash)
}
