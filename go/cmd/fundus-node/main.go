package main

import (
	"sync/atomic"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/fundus/node/internal/api"
	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/config"
	"github.com/fundus/node/internal/filestore"
	"github.com/fundus/node/internal/helperproto"
	"github.com/fundus/node/internal/llm"
	"github.com/fundus/node/internal/meter"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/shop"
	"github.com/fundus/node/internal/storage"
	"github.com/fundus/node/internal/topology"
	"github.com/fundus/node/internal/update"
)

// Version wird beim Build gesetzt: go build -ldflags="-X main.Version=R001"
var Version = "R001"

func main() {
	// -------------------------------------------------------------------------
	//  Logger
	// -------------------------------------------------------------------------
	log, _ := zap.NewProduction()
	defer log.Sync()

	// Einmalige Wartungsbefehle vor dem normalen Start prüfen.
	// --migrate-chain: alte JSON-Blöcke in den binären Blockstore migrieren und
	// beenden (kein Node-Start). Bequem, weil fundus-node ohnehin auf dem Pi liegt.
	if len(os.Args) > 1 && os.Args[1] == "--migrate-chain" {
		runMigrateChain(log)
		return
	}

	log.Info("Fundus Marketplace Node starting")

	// -------------------------------------------------------------------------
	//  Konfiguration
	// -------------------------------------------------------------------------
	cfg := config.Load(log)
	cfg.MustValidate(log)
	log.Info("Config loaded",
		zap.String("dataDir", cfg.DataDir),
		zap.Int("port", cfg.Port),
		zap.Int("p2pPort", cfg.P2PPort),
	)

	// -------------------------------------------------------------------------
	//  Hauptkontext
	// -------------------------------------------------------------------------
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, os.Interrupt)
	defer cancel()

	// -------------------------------------------------------------------------
	//  Storage
	// -------------------------------------------------------------------------
	store, err := storage.New(cfg.DataDir, log)
	if err != nil {
		log.Fatal("Storage init failed", zap.Error(err))
	}
	defer store.Close()
	log.Info("Storage ready", zap.String("path", cfg.DataDir))

	// -------------------------------------------------------------------------
	//  P2P-Node
	// -------------------------------------------------------------------------
	node, err := p2p.NewNode(ctx, cfg, store, log)
	if err != nil {
		log.Fatal("P2P node init failed", zap.Error(err))
	}
	defer node.Close()

	// Signaturpruefung fuer eingehende Records aktivieren: der Store nutzt
	// die VerifySignature-Methode des Nodes (Peer-ID → Public Key → Verify).
	store.SetSignatureVerifier(node.VerifySignature)

	if err := node.Bootstrap(ctx); err != nil {
		log.Warn("Bootstrap incomplete (running standalone)", zap.Error(err))
	}
	log.Info("P2P node ready",
		zap.String("peerID", node.ID().String()),
		zap.Strings("addrs", node.Addrs()),
	)

	// -------------------------------------------------------------------------
	//  Dezentraler Update-Empfänger
	// -------------------------------------------------------------------------
	// Maßgeblich ist die Build-Revision (api.NodeRevision). main.Version war fest
	// "R001" – jede Release-Version hätte damit als "neuer" gegolten und der Node
	// hätte sich nach jedem Update erneut aktualisiert.
	currentRev := api.NodeRevision
	updater := update.New(update.Config{
		CurrentVersion: currentRev,
		InstallDir:     cfg.DataDir + "/../", // /opt/fundus
		RestartCmd:     "systemctl restart fundus-node",
	}, log)

	// applyViaHelper reicht ein (bereits signatur-geprüftes) Manifest an den
	// privilegierten fundus-helper (root) zur Ausführung. Der gehärtete Node
	// (ProtectSystem=strict, NoNewPrivileges) kann selbst weder nach /opt/fundus
	// schreiben noch den Dienst neu starten. Der Helper prüft die Signatur zur
	// Sicherheit ein zweites Mal (Tor gegen kompromittierten Node).
	applyViaHelper := func(m *update.Manifest) error {
		manifestJSON, err := json.Marshal(m)
		if err != nil {
			log.Error("Update: Manifest serialisieren", zap.Error(err))
			return err
		}
		if !helperproto.Available() {
			log.Error("Update: fundus-helper nicht verfügbar — Update kann nicht angewendet werden (Helper installieren/starten)")
			return fmt.Errorf("fundus-helper nicht verfügbar")
		}
		log.Info("Update: an fundus-helper übergeben", zap.String("version", m.Version))
		resp, err := helperproto.Do(helperproto.Request{
			Action:   helperproto.ActionApplyUpdate,
			Manifest: string(manifestJSON),
		})
		if err != nil {
			log.Error("Update: Helper-Aufruf fehlgeschlagen", zap.Error(err))
			return err
		}
		if !resp.OK {
			log.Error("Update: Helper meldet Fehler", zap.String("error", resp.Error))
			return fmt.Errorf("%s", resp.Error)
		}
		log.Info("Update: vom Helper angewendet — Node wird neu gestartet", zap.String("version", m.Version))
		return nil
	}

	// Gefundene Updates werden ANGEBOTEN (Einstellungen → Software-Update) und
	// nur mit FUNDUS_UPDATE_AUTO=true sofort installiert.
	pendingUpdate := &update.Pending{}
	offerUpdate := func(m *update.Manifest) {
		pendingUpdate.Set(m)
		if cfg.UpdateAuto {
			go func() { _ = applyViaHelper(m) }()
			return
		}
		log.Info("Update verfügbar – installieren unter Einstellungen → Software-Update",
			zap.String("version", m.Version), zap.String("aktuell", currentRev))
	}

	// P2P-empfangene Updates: Signatur ist geprüft → anbieten.
	updater.SetOnUpdate(offerUpdate)
	// Update-Nachrichten vom P2P-Netz verarbeiten
	node.SetTopicHandler(p2p.TopicUpdate, updater.HandleMessage)

	// Git-Polling: holt periodisch das signierte Manifest von der konfigurierten
	// URL (z.B. GitHub raw). Bei einem gültigen, neueren Manifest wird es (a) per
	// P2P weiterverbreitet (schnelle Ausbreitung im Netz) und (b) über den Helper
	// angewendet. So erwischt es auch Nodes, die beim Broadcast offline waren.
	var poller *update.Poller
	if cfg.UpdateManifestURL != "" {
		poller = update.NewPoller(cfg.UpdateManifestURL, currentRev, 0, log,
			func(m *update.Manifest, raw []byte) {
				// Weiterverbreiten ins P2P-Netz (Best-effort) – auch Nodes ohne
				// Internetzugang erfahren so von der neuen Version.
				if err := node.Publish(ctx, p2p.TopicUpdate, raw); err != nil {
					log.Warn("Update: P2P-Weiterverbreitung fehlgeschlagen", zap.Error(err))
				} else {
					log.Info("Update: per P2P weiterverbreitet", zap.String("version", m.Version))
				}
				offerUpdate(m)
			})
		go poller.Run(ctx)
	}

	// -------------------------------------------------------------------------
	//  LLM-Analyzer (optional)
	// -------------------------------------------------------------------------
	var analyzer *llm.Analyzer
	if cfg.LLMEnabled {
		analyzer = llm.New("http://127.0.0.1:11434", cfg.LLMModel, "llama3.2:1b", log)

		pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
		if err := analyzer.Ping(pingCtx); err != nil {
			log.Warn("Ollama not reachable – LLM disabled", zap.Error(err))
			analyzer = nil
		} else {
			log.Info("Ollama reachable", zap.String("model", cfg.LLMModel))
			go func() {
				pullCtx, pullCancel := context.WithTimeout(
					context.Background(), 30*time.Minute)
				defer pullCancel()
				if err := analyzer.EnsureModel(pullCtx); err != nil {
					log.Warn("Model pull incomplete", zap.Error(err))
				}
			}()
		}
		pingCancel()
	}

	// -------------------------------------------------------------------------
	//  Smartmeter-Reader (optional)
	// -------------------------------------------------------------------------
	var meterTokens <-chan meter.Token

	if cfg.MeterProtocol != "" {
		meterCfg := meter.Config{
			Protocol:     cfg.MeterProtocol,
			SerialPort:   cfg.MeterPort,
			BaudRate:     cfg.MeterBaudRate,
			DataBits:     cfg.MeterDataBits,
			StopBits:     cfg.MeterStopBits,
			Parity:       cfg.MeterParity,
			HTTPURL:      cfg.MeterHTTPURL,
			MeterID:      cfg.MeterID,
			Lat:          cfg.MeterLat,
			Lon:          cfg.MeterLon,
			GeneratorLat: cfg.MeterGenLat,
			GeneratorLon: cfg.MeterGenLon,
			SigningKeyHex: cfg.MeterSignKey,
			Interval:     time.Second,
		}

		reader, err := meter.NewReader(meterCfg, log)
		if err != nil {
			log.Warn("Meter reader init failed – meter disabled", zap.Error(err))
		} else {
			tokens, err := reader.Run(ctx)
			if err != nil {
				log.Warn("Meter reader start failed", zap.Error(err))
			} else {
				meterTokens = tokens
				log.Info("Meter reader started",
					zap.String("protocol",  cfg.MeterProtocol),
					zap.String("port",      cfg.MeterPort),
					zap.Int("baud",        cfg.MeterBaudRate),
					zap.String("parity",   cfg.MeterParity),
					zap.String("meterID",  cfg.MeterID),
				)

				// Token → Storage + P2P-Publish im Hintergrund
				go func() {
					for tok := range tokens {
						rec := tokenToRecord(tok, node.ID().String())
						if err := store.Put(rec); err != nil {
							log.Warn("Meter token store failed", zap.Error(err))
							continue
						}
						if data, err := rec.Marshal(); err == nil {
							_ = node.Publish(ctx, p2p.TopicEnergy, data)
						}
					}
				}()
			}
		}
	} else {
		log.Info("No meter protocol configured – meter disabled")
	}

	// -------------------------------------------------------------------------
	//  FND-Client (Blockchain, optional)
	// -------------------------------------------------------------------------
	// -------------------------------------------------------------------------
	//  FND Shop (SOL → FND, optional)
	// -------------------------------------------------------------------------
	// -------------------------------------------------------------------------
	//  Dezentrales Filesharing (optional)
	// -------------------------------------------------------------------------
	var fs *filestore.FileStore
	if cfg.StorageOfferGB > 0 {
		// Storage-Verzeichnis anlegen bevor wir den Speicher prüfen
		// (statfs schlägt sonst auf nicht-existentem Pfad fehl).
		if err := os.MkdirAll(cfg.StorageDir, 0750); err != nil {
			log.Fatal("Storage-Verzeichnis konnte nicht angelegt werden",
				zap.String("dir", cfg.StorageDir), zap.Error(err))
		}
		// Prüfen ob genug Speicher vorhanden ist bevor wir ihn versprechen
		if err := filestore.CheckDiskSpace(cfg.StorageDir, cfg.StorageOfferGB); err != nil {
			log.Error("Nicht genug Speicher für Filesharing-Angebot",
				zap.Int64("offerGB", cfg.StorageOfferGB),
				zap.Error(err),
			)
			log.Fatal("Speicherfehler – FUNDUS_STORAGE_OFFER_GB reduzieren oder Speicher freimachen")
		}
		fsCfg := filestore.Config{
			DataDir: cfg.StorageDir,
			OfferGB: cfg.StorageOfferGB,
			AllocGB: cfg.StorageAllocGB, // ≥ 5 × OfferGB (durch config.Validate erzwungen)
			PeerID:  node.ID().String(),
			// Quittungs-Identität (Phase 2b): signiert Verdienst-Nachweise.
			WalletPrivKey: cfg.WalletPrivKey,
			RewardAddr:    cfg.StorageRewardAddr,
			// Node-Schlüssel bewusst ins stabile DataDir (nicht ins Chunk-Verzeichnis),
			// damit die Wallet auch bei Storage-Umzug/Löschung erhalten bleibt.
			KeyDir: cfg.DataDir,
			SeedPassword: cfg.SeedPassword,
		}
		// filestore.P2PAdapter wird durch p2p.Node implementiert
		// (Typ-Assertion nötig da filestore eigenes Interface hat)
		fs, err = filestore.New(fsCfg, &filestoreP2PAdapter{node}, log)
		if err != nil {
			log.Warn("FileStore Init fehlgeschlagen – Filesharing deaktiviert", zap.Error(err))
			fs = nil
		} else {
			go fs.RunReplicationManager(ctx)
			// Speicher-Vergütung läuft nativ über TxStorageReward auf der Fundus-Chain
			// (kein externer Chain-Client mehr nötig).
			go func() {
				// Persönlichen Index laden – ermöglicht Wiederherstellung auf neuem Pi
				seedWords := loadSeedWords(cfg)
				if len(seedWords) > 0 {
					if err := fs.WithIndex(ctx, seedWords); err != nil {
						log.Warn("Persönlicher Index nicht ladbar", zap.Error(err))
					}
					for i := range seedWords { seedWords[i] = "" } // RAM bereinigen
				}
			}()
			go func() {
				if e := fs.PublishOffer(ctx); e != nil {
					log.Warn("FileStore Offer publish", zap.Error(e))
				}
			}()
			log.Info("FileStore bereit",
				zap.Int64("offerGB", cfg.StorageOfferGB),
				zap.String("dir",    cfg.StorageDir),
			)
		}
	} else {
		log.Info("Filesharing deaktiviert (FUNDUS_STORAGE_OFFER_GB=0)")
	}

	// -------------------------------------------------------------------------
	//  Topologie-Manager
	// -------------------------------------------------------------------------
	topoProfile := &topology.NodeProfile{
		PeerID:            node.ID().String(),
		Type:              topology.NodeType(cfg.NodeType),
		Voltage:           topology.VoltageLevel(cfg.VoltageLevel),
		Lat:               cfg.NodeLat,
		Lon:               cfg.NodeLon,
		ParentSubstationID: cfg.ParentSubstation,
		TransitFeePercent: cfg.TransitFeePercent,
	}
	topoMgr := topology.New(topoProfile, &topologyP2PAdapter{node}, cfg.GMapsAPIKey, log)
	go topoMgr.Run(ctx)

	// ── Eigene Fundus-Chain (Phase 2: Single-Producer, Multi-Node-Sync) ──────────────────────────
	var chainBC *chain.Blockchain
	var mempool *chain.Mempool
	{
		// Single Source of Truth: kanonische Adresse, env nur optionaler Spiegel.
		feeCollector, ok := chain.AddressFromHex(cfg.EffectiveFeeCollector())
		if !ok {
			log.Warn("Chain: ungültige Fee-Collector-Adresse, Chain wird NICHT gestartet",
				zap.String("fee_collector", cfg.EffectiveFeeCollector()))
		} else {
			// PoA-Konsens: Proposer ist die NODE-Adresse (nicht der Fee-Collector),
			// damit die Round-Robin-Rotation über die Validatoren funktioniert und
			// dieser Node seine eigenen Blöcke signieren kann.
			// Proposer-Identität dieses Nodes. NUR gültig, wenn ein Signier-
			// Schlüssel geladen ist (entsperrte Node-Wallet). Ohne Schlüssel kann
			// dieser Node keine Blöcke signieren und darf NICHT Proposer sein —
			// er läuft dann als reiner Sync-/Lese-Node. Früher fiel proposer hier
			// auf den Fee-Collector zurück; das war falsch, weil der Node dann
			// scheinbar als Fee-Collector produzieren sollte, aber dessen Schlüssel
			// nicht hat → die Chain blieb bei der ersten Höhe stehen.
			var proposer chain.Address
			var proposerValid bool
			var consensusKey *ecdsa.PrivateKey
			if fs != nil {
				if k, addrHex := fs.ConsensusSigner(); k != nil {
					if a, ok := chain.AddressFromHex(addrHex); ok {
						proposer = a
						consensusKey = k
						proposerValid = true
					}
				}
			} else {
				// Filesharing ist aus, es gibt keinen FileStore — aber der Node
				// braucht trotzdem eine Identität, um staken/produzieren zu können.
				// Node-Wallet UNABHÄNGIG vom FileStore laden/erzeugen (entkoppelt).
				walletDir := filepath.Join(cfg.DataDir)
				if info, err := filestore.LoadOrCreateNodeWallet(walletDir); err == nil && info.Key != nil {
					proposer = chain.PubkeyToAddress(&info.Key.PublicKey)
					consensusKey = info.Key
					proposerValid = true
					if info.Created {
						log.Info("Node-Wallet (ohne Filesharing): neu erzeugt",
							zap.String("address", info.Address),
							zap.String("hinweis", "Seed-Wörter sichern!"))
					} else {
						log.Info("Node-Wallet (ohne Filesharing): geladen",
							zap.String("address", info.Address))
					}
				} else if err != nil {
					log.Warn("Node-Wallet (ohne Filesharing) konnte nicht geladen werden", zap.Error(err))
				}
			}
			if !proposerValid {
				log.Warn("Konsens: kein Signier-Schlüssel (Node-Wallet gesperrt oder nicht geladen) — dieser Node produziert KEINE Blöcke, läuft als Sync-/Lese-Node. Wallet entsperren (FUNDUS_SEED_PASSWORD oder über die Weboberfläche), dann Node neu starten.")
			}
			chainDir := filepath.Join(cfg.DataDir, "chain")
			genesisPath := filepath.Join(chainDir, "genesis.json")

			// Ausstehende Chain-Verschiebung ausführen, BEVOR die Chain geöffnet
			// wird (DB darf nicht in Benutzung sein). Die Datei chain-move.pending
			// enthält den Ziel-Laufwerkspfad; sie wird vom Admin-Endpunkt gesetzt.
			pendingMove := filepath.Join(cfg.DataDir, "chain-move.pending")
			if target, rerr := os.ReadFile(pendingMove); rerr == nil {
				tgt := strings.TrimSpace(string(target))
				os.Remove(pendingMove) // nur einmal versuchen
				if tgt != "" {
					log.Info("Chain-Verschiebung angefordert", zap.String("ziel", tgt))
					dest, merr := chain.MoveChain(chainDir, tgt, func(f string, a ...interface{}) {
						log.Info("Chain-Move: " + fmt.Sprintf(f, a...))
					})
					if merr != nil {
						log.Error("Chain-Verschiebung fehlgeschlagen — Chain bleibt am alten Ort", zap.Error(merr))
					} else {
						log.Info("Chain erfolgreich verschoben", zap.String("ziel", dest))
					}
				}
			}

			// Genesis bevorzugt aus mitgelieferter Datei laden (der Fee-Collector
			// steckt dann IM Genesis und ist über dessen Hash verifizierbar).
			// Fehlt die Datei (Erststart/Übergang), aus der Config erzeugen und
			// als genesis.json ablegen — danach ist sie das Artefakt.
			var genesis *chain.Block
			if g, err := chain.LoadGenesisFile(genesisPath); err == nil {
				genesis = g
				log.Info("Genesis aus Datei geladen", zap.String("path", genesisPath))
			} else {
				// DETERMINISTISCH erzeugen (fester Timestamp + Supply): jeder Node
				// mit demselben Fee-Collector erhält denselben Genesis-Hash, sodass
				// sich Node 2..N auf dieselbe Chain synchronisieren können.
				g, _ := chain.CanonicalGenesis(feeCollector)
				genesis = g
				if err := os.MkdirAll(chainDir, 0o755); err == nil {
					if h, werr := chain.WriteGenesisFile(genesisPath, genesis); werr == nil {
						log.Info("Genesis deterministisch erzeugt und abgelegt",
							zap.String("path", genesisPath),
							zap.String("genesis_hash", hex.EncodeToString(h[:])))
					}
				}
			}

			// expectedGenesisHash: in einer späteren Phase aus einer extern
			// verankerten Quelle (signiert/veröffentlicht). Phase 2: leer (0) =
			// keine externe Verankerung, nur Selbst-Konsistenz.
			var expected [32]byte
			bc, err := chain.NewBlockchain(chainDir, proposer, genesis, expected)
			if err != nil {
				log.Error("Chain-Initialisierung fehlgeschlagen", zap.Error(err))
				// Automatische Selbstheilung bei Inkompatibilität: Ein State-Root-
				// oder Replay-Fehler bedeutet meist, dass gespeicherte Blöcke mit
				// einer älteren State-/Root-Version erzeugt wurden (z.B. nach einem
				// Format-Update). Statt den Node ohne Chain laufen zu lassen, die
				// Block-Historie zurücksetzen (Genesis BLEIBT) und einmalig neu
				// starten. Für ein Testnetz ohne echte Werte ist das der richtige
				// Kompromiss; produktiv käme hier eine echte Migration hin.
				if isReplayIncompatibility(err) {
					log.Warn("Chain-Historie inkompatibel — setze Chain zurück (Genesis wird deterministisch neu erzeugt), Neustart der Chain",
						zap.String("grund", err.Error()))
					// Das GESAMTE Chain-Verzeichnis entfernen — auch genesis.json,
					// da diese bei einem State-Root-Versionssprung selbst einen
					// veralteten Root enthält. Der Genesis wird anschließend
					// deterministisch aus dem Fee-Collector neu erzeugt (gleicher
					// Hash auf allen Nodes).
					_ = os.RemoveAll(chainDir)
					// Genesis frisch (deterministisch) erzeugen und ablegen.
					freshGen, _ := chain.CanonicalGenesis(feeCollector)
					if err := os.MkdirAll(chainDir, 0o755); err == nil {
						_, _ = chain.WriteGenesisFile(genesisPath, freshGen)
					}
					// Zweiter Versuch mit dem NEUEN Genesis → frischer Start ab Höhe 0.
					if bc2, err2 := chain.NewBlockchain(chainDir, proposer, freshGen, expected); err2 == nil {
						bc = bc2
						genesis = freshGen
						err = nil
						log.Info("Chain nach Reset neu initialisiert (Höhe 0, neuer Genesis)")
					} else {
						log.Error("Chain-Reset fehlgeschlagen", zap.Error(err2))
					}
				}
			}
			if err == nil && bc != nil {
				chainBC = bc
				mempool = chain.NewMempool(10000)
				gh := chain.GenesisHash(genesis)

				// ── PoA-Konsens aktivieren ───────────────────────────────────
				// Validator-Set = die tatsächlich PRODUZIERENDEN Nodes (FUNDUS_VALIDATORS).
				// Der Fee-Collector ist Signatur-AUTORITÄT (Genesis, Fee-Struktur),
				// aber KEIN Proposer — kein laufender Node hält seinen Schlüssel.
				// Stünde er in der Rotation, bekäme er Proposer-Runden zugewiesen,
				// die niemand bauen kann → die Chain bliebe bei dieser Höhe stehen
				// (genau der Fehler "nicht dein Zug, Proposer ist 0xea55…").
				// Alle Nodes müssen dieselbe Liste konfiguriert haben, damit die
				// Rundenzuordnung übereinstimmt.
				var valAddrs []chain.Address
				for _, vh := range cfg.Validators {
					if a, ok := chain.AddressFromHex(vh); ok {
						valAddrs = append(valAddrs, a)
					} else {
						log.Warn("Konsens: ungültige Validator-Adresse übersprungen", zap.String("addr", vh))
					}
				}
				// Fallback: Ist keine Validator-Liste gesetzt, ist dieser Node sein
				// eigener alleiniger Proposer (sauberer Solo-Start) — aber NUR wenn
				// er einen Signier-Schlüssel hat. NIE der Fee-Collector.
				if len(valAddrs) == 0 && proposerValid {
					valAddrs = []chain.Address{proposer}
				}
				if len(valAddrs) == 0 {
					// Weder konfigurierte Validatoren noch eigener Schlüssel →
					// Konsens kann nicht aktiviert werden. Node läuft als Sync-Node.
					log.Warn("Konsens NICHT aktiviert: kein Validator-Set und kein eigener Signier-Schlüssel. Node synchronisiert nur.")
				} else if vs, verr := chain.NewValidatorSet(valAddrs); verr == nil {
					bc.SetConsensus(vs, consensusKey)
					isValidator := consensusKey != nil && vs.Contains(proposer)
					log.Info("PoA-Konsens aktiv",
						zap.Int("validators", vs.Len()),
						zap.Bool("dieser_node_ist_validator", isValidator),
						zap.String("proposer", proposer.Hex()))
					// Automatischen Produktions-Loop nur starten, wenn dieser Node
					// überhaupt Validator ist (sonst reiner Sync-Node).
					if isValidator {
						go runBlockProductionLoop(ctx, bc, mempool, node, log)
					}
				} else {
					log.Warn("Konsens: Validator-Set ungültig — Chain läuft ohne PoA (Einzelnode)", zap.Error(verr))
				}

				log.Info("Fundus-Chain aktiv (PoA-Konsens: Multi-Node mit Round-Robin-Produktion)",
					zap.Uint64("height", bc.Height()),
					zap.String("fee_collector", feeCollector.Hex()),
					zap.String("genesis_hash", hex.EncodeToString(gh[:])))
				// Chain an P2P anbinden: Aufhol-Sync beim Peer-Connect + Live-Block-Topic.
				node.AttachChain(ctx, bc)
			}
		}
	}

	srv := api.NewServer(cfg, node, store, analyzer, meterTokens, log, Version)
	// Helper-Version im Hintergrund abfragen (nie im Anfragepfad: der Helper
	// arbeitet seriell und wäre während einer Installation minutenlang belegt).
	var helperVer atomic.Value
	helperVer.Store("")
	refreshHelperVer := func() {
		if !helperproto.Available() {
			helperVer.Store("nicht erreichbar")
			return
		}
		resp, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionPing})
		switch {
		case err != nil:
			helperVer.Store("nicht erreichbar")
		case resp.Version == "":
			helperVer.Store("alt") // Helper vor R443 meldet keine Version
		default:
			helperVer.Store(resp.Version)
		}
	}
	go func() {
		refreshHelperVer()
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshHelperVer()
			}
		}
	}()

	// Software-Update aus den Einstellungen heraus (prüfen / installieren).
	srv.WithUpdateControl(&api.UpdateControl{
		HelperVersion: func() string { v, _ := helperVer.Load().(string); return v },
		Current: currentRev,
		Source:  cfg.UpdateManifestURL,
		Auto:    cfg.UpdateAuto,
		Pending: pendingUpdate.Get,
		Check: func() {
			if poller != nil {
				poller.CheckNow(ctx)
			}
		},
		Info: func() update.PollInfo {
			if poller == nil {
				return update.PollInfo{}
			}
			return poller.Info()
		},
		Apply: func(m *update.Manifest) error {
			return applyViaHelper(m) // blockiert (Minuten); die API ruft das asynchron auf
		},
	})
	srv.WithTopology(topoMgr)
	if chainBC != nil {
		srv.WithChain(chainBC, mempool)
		// Bridge-Autorität (ÖFFENTLICHE Adresse) auf JEDEM Node setzen — auch
		// ohne eigenen Shop. Nur so erkennen alle Nodes dieselben SOL-Credits als
		// gültig und die Chain bleibt konsistent. Enthält kein Geheimnis.
		if cfg.ShopBridgeAuthority != "" {
			if addr, ok := chain.AddressFromHex(cfg.ShopBridgeAuthority); ok {
				chainBC.SetBridgeAuthority(addr)
				log.Info("SOL-Brücken-Autorität gesetzt", zap.String("address", addr.Hex()))
			} else {
				log.Warn("FUNDUS_SHOP_BRIDGE_AUTHORITY ungültig — SOL-Credits werden abgelehnt",
					zap.String("value", cfg.ShopBridgeAuthority))
			}
		}
	}
	if fs != nil {
		srv.WithFileStore(fs)
		// ShareManager mit P2P-Adapter erstellen, damit lokale Verzeichnis-
		// Freigaben per DHT an andere Nodes propagiert werden. Ohne Adapter
		// blieben Freigaben lokal unsichtbar fürs Netz.
		shareDir := filepath.Join(cfg.DataDir, "shares")
		if sm, serr := filestore.NewShareManager(shareDir, node.ID().String(), &filestoreP2PAdapter{node}, log); serr == nil {
			srv.WithShareManager(sm)
			go sm.Run(context.Background()) // startet DHT-Publish + Discovery
		} else {
			log.Warn("ShareManager Init fehlgeschlagen", zap.Error(serr))
		}
	}
	// Orderbuch (dezentrale FND/SOL-Orders) mit demselben P2P-Adapter.
	srv.WithOrderBook(&filestoreP2PAdapter{node})

	if cfg.ShopEnabled && cfg.ShopReceiveAddr != "" {
		priceFeedCfg := shop.PriceFeedConfig{
			CacheTTL:    30 * time.Second,
			SlippageBPS: cfg.ShopSlippageBPS,
			OfferTTL:    time.Duration(cfg.ShopOfferTTLSec) * time.Second,
			HTTPTimeout: 5 * time.Second,
		}
		priceFeed := shop.NewPriceFeed(priceFeedCfg, log)

		watchCfg := shop.WatcherConfig{
			RPCURL:         cfg.ShopSolanaRPC,
			ReceiveAddress: cfg.ShopReceiveAddr,
			PollInterval:   3 * time.Second,
			PaymentTimeout: 10 * time.Minute,
			HTTPTimeout:    5 * time.Second,
		}
		// Minter wählen: bevorzugt die NATIVE Chain (SOL→FND als On-Chain-
		// TxSolCredit). Der Brücken-Operator nutzt einen EIGENEN, vom Node-/
		// FeeCollector-Key GETRENNTEN Bridge-Schlüssel (ShopBridgePrivKey). Nur
		// dieser signiert Gutschriften — User bekommen ihn nie. Fällt der Bridge-
		// Der Shop nutzt ausschließlich den nativen Minter (Fundus-Chain). Ohne
		// konfigurierten Bridge-Schlüssel bleibt shopMinter nil → Shop inaktiv.
		var shopMinter shop.Minter
		if chainBC != nil && mempool != nil && cfg.ShopBridgePrivKey != "" {
			if key, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.ShopBridgePrivKey, "0x")); err == nil {
				bridgeAddr := chain.PubkeyToAddress(&key.PublicKey)
				// Konsistenzprüfung: Die Adresse des privaten Bridge-Schlüssels MUSS
				// zur öffentlich konfigurierten Bridge-Autorität passen. Sonst würde
				// der Betreiber Credits signieren, die alle anderen Nodes ablehnen.
				if cfg.ShopBridgeAuthority != "" {
					if pubAddr, ok := chain.AddressFromHex(cfg.ShopBridgeAuthority); !ok || pubAddr != bridgeAddr {
						log.Fatal("SICHERHEITSWARNUNG: Bridge-Schlüssel passt nicht zu FUNDUS_SHOP_BRIDGE_AUTHORITY",
							zap.String("key_address", bridgeAddr.Hex()),
							zap.String("configured_authority", cfg.ShopBridgeAuthority),
							zap.String("hint", "Entweder den Key oder die Autoritäts-Adresse korrigieren"))
					}
				}
				shopMinter = api.NewNativeSolMinter(chainBC, mempool, key, log)
				log.Info("SOL→FND-Brücke nutzt native Chain (eigener Bridge-Schlüssel)",
					zap.String("bridge_authority", bridgeAddr.Hex()))
			} else {
				log.Warn("Bridge-Schlüssel ungültig — SOL→FND-Brücke bleibt inaktiv", zap.Error(err))
			}
		}
		if shopMinter == nil {
			// ANZEIGE-MODUS: kein Bridge-Schlüssel → dieser Node verarbeitet nicht,
			// zeigt aber die Kaufseite (QR-Code, Preise, gemeinsame Empfangsadresse).
			// Der eine Shop-Node mit Schlüssel, der dieselbe Wallet überwacht, macht
			// die Gutschrift. Die Solana-Transaktion ist die Beauftragung.
			srv.WithShop(nil, priceFeed, cfg.ShopReceiveAddr)
			log.Info("FND shop im ANZEIGE-MODUS (kein Bridge-Schlüssel — Zahlungen verarbeitet der Shop-Node mit Schlüssel)",
				zap.String("solAddress", cfg.ShopReceiveAddr))
		} else {
			watcher := shop.NewSolWatcher(watchCfg, priceFeed, shopMinter, log)
			go watcher.Run(ctx)

			srv.WithShop(watcher, priceFeed, cfg.ShopReceiveAddr)
			log.Info("FND shop enabled (VOLL-MODUS: überwacht Wallet + schreibt FND gut)",
				zap.String("solAddress", cfg.ShopReceiveAddr),
				zap.Int("slippageBPS", cfg.ShopSlippageBPS),
			)
		}
	} else {
		log.Info("FND shop disabled (set FUNDUS_SHOP_ENABLED=true and FUNDUS_SHOP_RECEIVE_ADDR)")
	}

	// -------------------------------------------------------------------------
	//  REST-API
	// -------------------------------------------------------------------------
	go func() {
		if err := srv.Run(); err != nil {
			log.Error("API server error", zap.Error(err))
			cancel()
		}
	}()
	log.Info("API server listening", zap.Int("port", cfg.Port))

	// -------------------------------------------------------------------------
	//  Warten auf Shutdown
	// -------------------------------------------------------------------------
	<-ctx.Done()
	log.Info("Shutdown signal – cleaning up…")

	shutdownCtx, shutdownCancel := context.WithTimeout(
		context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("API shutdown error", zap.Error(err))
	}

	// Chain-Store schließen → gibt den bbolt-Datei-Lock frei, damit ein Neustart
	// die DB sofort wieder öffnen kann (sonst wartet er auf den Lock).
	if chainBC != nil {
		if err := chainBC.Close(); err != nil {
			log.Warn("Chain-Store schließen", zap.Error(err))
		}
	}

	log.Info("Fundus node stopped cleanly")
}

// tokenToRecord wandelt einen Meter-Token in einen speicherbaren Record um.
func tokenToRecord(tok meter.Token, ownerID string) *storage.Record {
	return &storage.Record{
		ID:        fmt.Sprintf("%d", tok.Timestamp.UnixNano()),
		Type:      storage.RecordEnergy,
		OwnerID:   ownerID,
		CreatedAt: tok.Timestamp,
		Data: map[string]any{
			"timestamp":     tok.Timestamp.UTC().Format(time.RFC3339),
			"meter_id":      tok.MeterID,
			"lat":           tok.Lat,
			"lon":           tok.Lon,
			"kwh":           tok.KWh,
			"watt_now":      tok.WattNow,
			"generator_lat": tok.GeneratorLat,
			"generator_lon": tok.GeneratorLon,
			"signature":     tok.Signature,
		},
	}
}

// filestoreP2PAdapter verbindet filestore.P2PAdapter mit p2p.P2PNode.
type filestoreP2PAdapter struct{ n p2p.P2PNode }

func (a *filestoreP2PAdapter) ID() interface{ String() string }    { return a.n.ID() }
func (a *filestoreP2PAdapter) Peers() []interface{ String() string } {
	peers := a.n.Peers()
	out   := make([]interface{ String() string }, len(peers))
	for i, p := range peers { out[i] = p }
	return out
}
func (a *filestoreP2PAdapter) DHTput(ctx context.Context, k string, v []byte) error {
	return a.n.DHTput(ctx, k, v)
}
func (a *filestoreP2PAdapter) DHTget(ctx context.Context, k string) ([]byte, error) {
	return a.n.DHTget(ctx, k)
}
func (a *filestoreP2PAdapter) SendToPeer(ctx context.Context, pid, proto string, data []byte) error {
	return a.n.SendToPeer(ctx, pid, proto, data)
}
func (a *filestoreP2PAdapter) SendAndReceive(ctx context.Context, pid, proto string, data []byte) ([]byte, error) {
	return a.n.SendAndReceive(ctx, pid, proto, data)
}
func (a *filestoreP2PAdapter) RegisterProtocol(proto string, h func(string, []byte) []byte) {
	a.n.RegisterProtocol(proto, h)
}
func (a *filestoreP2PAdapter) SetTopicHandler(topic string, h func([]byte)) {
	a.n.SetTopicHandler(topic, h)
}
func (a *filestoreP2PAdapter) Publish(ctx context.Context, topic string, data []byte) error {
	return a.n.Publish(ctx, topic, data)
}

// loadSeedWords liest die Seed-Wörter aus der Umgebungsvariable FUNDUS_SEED_WORDS
// oder aus der Datei FUNDUS_SEED_FILE.
// Die Wörter werden nur einmalig gelesen und sofort nach Verwendung aus dem RAM gelöscht.
func loadSeedWords(cfg *config.Config) []string {
	// Option 1: Datei (sicherer als Env-Variable)
	if cfg.SeedFile != "" {
		data, err := os.ReadFile(cfg.SeedFile)
		if err == nil {
			words := strings.Fields(string(data))
			// Datei-Inhalt im RAM bereinigen
			for i := range data { data[i] = 0 }
			if len(words) >= 10 {
				return words
			}
		}
	}
	// Option 2: Env-Variable (Fallback)
	raw := os.Getenv("FUNDUS_SEED_WORDS")
	if raw != "" {
		return strings.Fields(raw)
	}
	return nil
}

// topologyP2PAdapter verbindet topology.Manager mit p2p.Node.
type topologyP2PAdapter struct{ n p2p.P2PNode }

func (a *topologyP2PAdapter) ID() interface{ String() string } { return a.n.ID() }
func (a *topologyP2PAdapter) Publish(ctx context.Context, topic string, data []byte) error {
	return a.n.Publish(ctx, topic, data)
}
func (a *topologyP2PAdapter) SetTopicHandler(topic string, h func([]byte)) {
	a.n.SetTopicHandler(topic, h)
}
func (a *topologyP2PAdapter) DHTput(ctx context.Context, k string, v []byte) error {
	return a.n.DHTput(ctx, k, v)
}
func (a *topologyP2PAdapter) DHTget(ctx context.Context, k string) ([]byte, error) {
	return a.n.DHTget(ctx, k)
}

// isReplayIncompatibility erkennt Chain-Ladefehler, die auf einem inkompatiblen
// gespeicherten Format beruhen (z.B. State-Root-Versionssprung nach einem
// Update) — im Gegensatz zu transienten Fehlern. Nur bei solchen Fehlern setzt
// der Node die Block-Historie automatisch zurück (Genesis bleibt erhalten).
func isReplayIncompatibility(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// Typische Marker eines Format-/Versions-Mismatch beim Replay oder schon
	// beim Laden/Prüfen des Genesis (z.B. nach einem State-Root-Versionssprung).
	return strings.Contains(msg, "state_root stimmt nicht") ||
		strings.Contains(msg, "tx_root stimmt nicht") ||
		strings.Contains(msg, "Head-Hash nach Replay") ||
		strings.Contains(msg, "Storage-Reward-Tx im Block") ||
		strings.Contains(msg, "nicht kanonisch") ||
		strings.Contains(msg, "Genesis state_root inkonsistent") ||
		strings.Contains(msg, "persistierter Genesis weicht") ||
		strings.Contains(msg, "Schema-Version inkompatibel")
}

// blockBroadcaster ist die minimale Schnittstelle, die der Produktions-Loop zum
// Verteilen neuer Blöcke braucht (erfüllt von p2p.Node).
type blockBroadcaster interface {
	BroadcastBlock(ctx context.Context, blockJSON []byte) error
}

// runBlockProductionLoop ist der automatische PoA-Produktions-Loop. Er tickt im
// BlockTime-Intervall und produziert NUR dann einen Block, wenn dieser Node für
// die nächste Höhe der berechtigte Proposer ist (Round-Robin über das Validator-
// Set). So entsteht auf jeder Höhe genau ein legitimer Block — kein Fork.
//
// Der Loop liest die Adresse für den 'node' (Broadcast) aus dem Closure-Scope.
func runBlockProductionLoop(ctx context.Context, bc *chain.Blockchain, mp *chain.Mempool, bcaster blockBroadcaster, log *zap.Logger) {
	// Feiner Tick (1s), damit die zeitbasierte Fallback-Rotation zügig greift,
	// wenn der primäre Proposer ausfällt. Die tatsächliche Blockrate bleibt durch
	// TurnAt (Rundenfreischaltung) + Mempool geregelt — häufigeres Ticken
	// produziert NICHT mehr Blöcke, es prüft nur öfter, ob dieser Node dran ist.
	// FND-001: Intervall über tickIntervalFor (tick.go) — garantiert positiv.
	ticker := time.NewTicker(tickIntervalFor(chain.BlockTime))
	defer ticker.Stop()
	log.Info("PoA-Produktions-Loop gestartet", zap.Uint64("block_time_s", chain.BlockTime))

	for {
		select {
		case <-ctx.Done():
			log.Info("PoA-Produktions-Loop beendet")
			return
		case <-ticker.C:
			// Nur produzieren, wenn dieser Node für die nächste Höhe dran ist —
			// als primärer Proposer (Runde 0) oder als Fallback, falls der/die
			// primäre(n) Proposer nicht rechtzeitig geliefert haben (Liveness).
			my, round := bc.TurnAt(0)
			if !my {
				continue
			}
			// Txs aus dem Mempool nehmen (leerer Mempool → kein Block, um die
			// Kette nicht mit Leerblöcken zu fluten).
			txs := mp.Take(500)
			if len(txs) == 0 {
				continue
			}
			blk, err := bc.ProduceBlockRound(txs, uint64(time.Now().Unix()), round)
			if err != nil {
				// Produktion fehlgeschlagen (z.B. nicht mein Zug wegen Race, oder
				// alle Txs ungültig). Gültige Txs zurücklegen wäre ideal; hier
				// loggen wir und verwerfen den Versuch (nächster Tick probiert neu).
				log.Warn("PoA: Block-Produktion fehlgeschlagen", zap.Error(err))
				continue
			}
			log.Info("PoA: Block produziert",
				zap.Uint64("height", blk.Header.Height),
				zap.Uint64("round", round),
				zap.Int("txs", len(blk.Transactions)))
			// Block an Peers broadcasten, damit sie ihn übernehmen.
			if blockJSON, e := bc.ExportBlockJSON(blk.Header.Height); e == nil {
				if bcaster != nil {
					_ = bcaster.BroadcastBlock(ctx, blockJSON)
				}
			}
		}
	}
}

// runMigrateChain führt die einmalige Migration der JSON-Blöcke in den binären
// Blockstore aus (Aufruf: fundus-node --migrate-chain). Nutzt das konfigurierte
// DataDir, migriert <DataDir>/chain und verifiziert per Head-Hash. Beendet das
// Programm mit Exit-Code 1 bei Fehler.
func runMigrateChain(log *zap.Logger) {
	cfg := config.Load(log)
	chainDir := filepath.Join(cfg.DataDir, "chain")
	fmt.Fprintf(os.Stderr, "Migriere JSON-Blöcke aus %s in den Blockstore …\n", chainDir)

	count, err := chain.MigrateJSONToStore(chainDir, func(h, total uint64) {
		if h%1000 == 0 || h == total {
			fmt.Fprintf(os.Stderr, "\r  Block %d / %d", h, total)
		}
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FEHLER: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%d Blöcke migriert. Verifiziere (Replay aus DB, Head-Hash) …\n", count)

	if err := chain.VerifyStoreAgainstMeta(chainDir); err != nil {
		fmt.Fprintf(os.Stderr, "VERIFIKATION FEHLGESCHLAGEN: %v\n", err)
		fmt.Fprintln(os.Stderr, "Die alten JSON-Dateien sind unverändert. Node NICHT starten, bis geklärt.")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "✓ Verifikation OK — Head-Hash stimmt. Migration verlustfrei.")
	fmt.Fprintln(os.Stderr, "Die alten JSON-Dateien bleiben als Backup unter chain/blocks/ erhalten.")
	fmt.Fprintln(os.Stderr, "Node kann jetzt normal gestartet werden.")
}
