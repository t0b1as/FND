// Package update implementiert dezentrale Software-Updates über das P2P-Netz.
//
// Ablauf:
//  1. Betreiber signiert ein Update-Manifest mit dem Fee-Collector-Private-Key
//  2. Manifest wird via GossipSub auf Topic "fundus.update" verbreitet
//  3. Jeder Node prüft die Signatur gegen die kanonische Fee-Collector-Adresse
//  4. Bei gültiger Signatur und höherer Versionsnummer → Download + Neustart
//
// Integrität:
//   - Argon2id(zip, salt="fundus-update-v1:"+version, m=64MiB, t=4, p=4)
//   - Memory-hard → Manipulation des Hashes rechnerisch teuer
//   - Signatur via Ethereum ECDSA mit Fee-Collector-Key
package update

import (
	"crypto/ecdsa"
	"os/user"
	"runtime"
	"strconv"
	"sync"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
	"golang.org/x/crypto/argon2"
	"lukechampine.com/blake3"

	"github.com/fundus/node/internal/config"
)

// =============================================================================
//  Kanonische Signatur-Adresse
// =============================================================================
// EINZIGE Quelle der Wahrheit ist config.KanonischeFeeCollector. Updates müssen
// vom selben Schlüssel signiert sein wie der Fee-Collector (bewusste Kopplung,
// gleicher Key). Kein Duplikat hier — Adresswechsel nur an EINER Stelle.

const signatureAuthority = config.KanonischeFeeCollector

// TopicUpdate ist der GossipSub-Topic für Update-Ankündigungen.
const TopicUpdate = "fundus.update"

// Argon2id-Parameter für ZIP-Integritätsprüfung.
// Identisch zu den Wallet-Derivationsparametern für Konsistenz.
const (
	a2Memory  = 64 * 1024 // 64 MiB
	a2Time    = 4
	a2Threads = 4
	a2KeyLen  = 32        // 32 Bytes = 64 Hex-Zeichen
)

// argon2idSalt erzeugt einen deterministischen Salt aus Version + Konstante.
func argon2idSalt(version string) []byte {
	return []byte("fundus-update-v1:" + strings.ToUpper(version))
}

// =============================================================================
//  Manifest
// =============================================================================

// Manifest beschreibt ein Software-Update.
type Manifest struct {
	Version     string    `json:"version"`      // z.B. "R002"
	Description string    `json:"description"`
	NodeURL     string    `json:"node_url"`     // Download-URL für fundus.zip
	NodeArgon2  string    `json:"node_argon2"`  // Argon2id-Hash des ZIPs (hex, 64 Zeichen)
	PublishedAt time.Time `json:"published_at"`
	Signature   string    `json:"signature"`    // Ethereum-Signatur des Manifest-Hash
}

// hashZIP berechnet Argon2id(zip_bytes, salt, m=64MiB, t=4, p=4) → hex.
// Memory-hard: Manipulation des Hashes kostet 64 MiB RAM pro Versuch.
func hashZIP(data []byte, version string) string {
	h := argon2.IDKey(data, argon2idSalt(version), a2Time, a2Memory, a2Threads, a2KeyLen)
	return hex.EncodeToString(h)
}

// SigningPayload gibt den zu signierenden String zurück.
// Format: "fundus-update:version:argon2hash" – deterministisch und eindeutig.
func (m *Manifest) SigningPayload() string {
	return fmt.Sprintf("fundus-update:%s:%s", m.Version, m.NodeArgon2)
}

// Verify prüft ob die Manifest-Signatur von der kanonischen Adresse stammt.
func (m *Manifest) Verify() error {
	if m.Signature == "" {
		return fmt.Errorf("update: keine Signatur im Manifest")
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(m.Signature, "0x"))
	if err != nil {
		return fmt.Errorf("update: Signatur dekodieren: %w", err)
	}
	if len(sigBytes) != 65 {
		return fmt.Errorf("update: Signatur hat %d Bytes, erwartet 65", len(sigBytes))
	}

	// Ethereum-typisches Prefix: "\x19Ethereum Signed Message:\n{len}"
	payload := m.SigningPayload()
	hash    := accounts.TextHash([]byte(payload))

	// Recovery-Byte anpassen (Ethereum: 27/28 → 0/1)
	sigBytes[64] %= 27

	pubKey, err := crypto.SigToPub(hash, sigBytes)
	if err != nil {
		return fmt.Errorf("update: Public Key aus Signatur: %w", err)
	}

	// Die Fee-Collector-Adresse ist eine FUNDUS-Adresse: BLAKE3-256(X||Y)[:20]
	// (wie fnd-wallet und chain.PubkeyToAddress sie bilden). Früher wurde hier
	// die Ethereum-Adresse (Keccak) verglichen – die passt bei demselben
	// Schlüssel nie, jedes korrekt signierte Update wäre abgelehnt worden.
	recovered := fundusAddress(pubKey)
	canonical := strings.ToLower(signatureAuthority)
	if recovered != canonical {
		return fmt.Errorf("update: Signatur ungültig. Erwartet: %s, Erhalten: %s", canonical, recovered)
	}
	return nil
}

// fundusAddress bildet die Fundus-Adresse eines secp256k1-Schlüssels.
func fundusAddress(pub *ecdsa.PublicKey) string {
	raw := crypto.FromECDSAPub(pub) // 65 Bytes: 0x04 || X || Y
	if len(raw) != 65 {
		return ""
	}
	h := blake3.Sum256(raw[1:])
	return "0x" + hex.EncodeToString(h[:20])
}

// =============================================================================
//  Updater
// =============================================================================

// Updater empfängt und wendet Update-Manifeste an.
type Updater struct {
	currentVersion string
	installDir     string    // Zielverzeichnis (z.B. /opt/fundus)
	restartCmd     string    // z.B. "systemctl restart fundus-node"
	log            *zap.Logger
	httpClient     *http.Client
	onUpdate       func(m *Manifest) // Callback wenn Update beginnt (für Tests)
}

// Config konfiguriert den Updater.
type Config struct {
	CurrentVersion string // z.B. "R001"
	InstallDir     string // Standard: /opt/fundus
	RestartCmd     string // Standard: "sudo systemctl restart fundus-node"
	HTTPTimeout    time.Duration
}

var DefaultConfig = Config{
	InstallDir:  "/opt/fundus",
	RestartCmd:  "sudo systemctl restart fundus-node",
	HTTPTimeout: 5 * time.Minute,
}

// SetOnUpdate setzt einen Callback, der bei einem gültigen, neueren Update
// AUSGELÖST wird, statt dass der Updater selbst installiert. Damit kann der Node
// die privilegierte Ausführung an den fundus-helper (root) delegieren, statt
// selbst zu entpacken/neu zu starten (was der gehärtete Node nicht darf).
func (u *Updater) SetOnUpdate(fn func(m *Manifest)) {
	u.onUpdate = fn
}

// New erstellt einen Updater.
func New(cfg Config, log *zap.Logger) *Updater {
	timeout := cfg.HTTPTimeout
	if timeout == 0 { timeout = DefaultConfig.HTTPTimeout }
	install := cfg.InstallDir
	if install == "" { install = DefaultConfig.InstallDir }
	restart := cfg.RestartCmd
	if restart == "" { restart = DefaultConfig.RestartCmd }

	return &Updater{
		currentVersion: cfg.CurrentVersion,
		installDir:     install,
		restartCmd:     restart,
		log:            log,
		httpClient:     &http.Client{Timeout: timeout},
	}
}

// HandleMessage verarbeitet eine eingehende P2P-Nachricht auf TopicUpdate.
// Wird von p2p.Node.handleMessages() aufgerufen.
func (u *Updater) HandleMessage(data []byte) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		u.log.Warn("Update: ungültiges Manifest", zap.Error(err))
		return
	}

	u.log.Info("Update-Ankündigung empfangen",
		zap.String("version",     m.Version),
		zap.String("description", m.Description),
	)

	// 1. Signatur prüfen
	if err := m.Verify(); err != nil {
		u.log.Error("Update: Signaturprüfung fehlgeschlagen – ignoriert",
			zap.Error(err))
		return
	}
	u.log.Info("Update: Signatur gültig")

	// 2. Versionsvergleich
	if !isNewerVersion(m.Version, u.currentVersion) {
		u.log.Info("Update: keine neuere Version",
			zap.String("current", u.currentVersion),
			zap.String("manifest", m.Version))
		return
	}

	// 3. Anwenden
	if u.onUpdate != nil {
		u.onUpdate(&m)
		return
	}
	go u.applyUpdate(&m)
}

// applyUpdate lädt das Update herunter und startet den Service neu.
func (u *Updater) applyUpdate(m *Manifest) {
	// Updates werden ausschließlich über den privilegierten fundus-helper
	// angewendet (update.ApplyManifest). Der Node selbst darf /opt/fundus nicht
	// schreiben; der frühere In-Process-Weg hätte zudem das ganze
	// Installationsverzeichnis (inkl. data/) ersetzt.
	u.log.Warn("Update: kein Helper-Callback gesetzt – Update wird nicht angewendet",
		zap.String("version", m.Version))
}

// =============================================================================
//  Update anwenden (self-contained, auch vom fundus-helper nutzbar)
// =============================================================================

// ApplyOptions steuert ApplyManifest.
type ApplyOptions struct {
	InstallDir  string        // Zielverzeichnis (z.B. /opt/fundus)
	RestartCmd  string        // z.B. "systemctl restart fundus-node" (Helper ist root, kein sudo nötig)
	CurrentVer  string        // aktuelle Version für den Neuer-Check (leer = Check überspringen)
	HTTPTimeout time.Duration // Download-Timeout (0 = 5 Min)
	Progress    func(ApplyProgress) // optional: Fortschritt melden (Statusanzeige)
}

// ApplyProgress ist der Fortschritt einer laufenden Installation.
type ApplyProgress struct {
	Version   string    `json:"version"`
	State     string    `json:"state"` // running | restarting | failed
	Step      int       `json:"step"`
	Steps     int       `json:"steps"`
	Label     string    `json:"label"`
	Percent   int       `json:"percent"` // Fortschritt innerhalb des Schritts, -1 = unbestimmt
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Schritte der Installation (für die Anzeige).
const applySteps = 6

// ApplyManifest prüft ein signiertes Manifest EIGENSTÄNDIG (Signatur gegen die
// kanonische Autorität) und wendet das Update an: ZIP laden, Argon2id-Hash
// prüfen, entpacken (Backup + atomischer Swap), Node neu starten.
//
// Self-contained, damit der privilegierte fundus-helper sie nutzen kann: Er
// prüft die Signatur SELBST (ein kompromittierter Node kann so kein unsigniertes
// Update erzwingen) und führt die privilegierten Schritte (Schreiben nach
// /opt/fundus, systemctl restart) mit Root-Rechten aus. logf darf nil sein.
func ApplyManifest(m *Manifest, opt ApplyOptions, logf func(string, ...interface{})) error {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	var last ApplyProgress
	report := func(step int, label string, pct int, state string) {
		last = ApplyProgress{Version: m.Version, State: state, Step: step, Steps: applySteps,
			Label: label, Percent: pct, UpdatedAt: time.Now()}
		if opt.Progress != nil {
			opt.Progress(last)
		}
	}
	err := applyManifest(m, opt, logf, report)
	if err != nil && opt.Progress != nil {
		last.State = "failed"
		last.Error = err.Error()
		last.UpdatedAt = time.Now()
		opt.Progress(last)
	}
	return err
}

func applyManifest(m *Manifest, opt ApplyOptions, logf func(string, ...interface{}),
	report func(step int, label string, pct int, state string)) error {
	// 1. Signatur EIGENSTÄNDIG prüfen — das eigentliche Tor.
	report(1, "Signatur prüfen", -1, "running")
	if err := m.Verify(); err != nil {
		return fmt.Errorf("Signaturprüfung: %w", err)
	}
	// 2. Optionaler Versions-Check.
	if opt.CurrentVer != "" && !isNewerVersion(m.Version, opt.CurrentVer) {
		return fmt.Errorf("keine neuere Version (aktuell %s, Manifest %s)", opt.CurrentVer, m.Version)
	}
	timeout := opt.HTTPTimeout
	if timeout == 0 {
		timeout = 20 * time.Minute // Download + Entpacken, auch bei langsamer Leitung
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 3. ZIP laden.
	logf("Update: Download von %s", m.NodeURL)
	report(2, "Herunterladen", 0, "running")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.NodeURL, nil)
	if err != nil {
		return fmt.Errorf("HTTP-Request: %w", err)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Download HTTP-Status %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "fundus-update-*.zip")
	if err != nil {
		return fmt.Errorf("Temp-Datei: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	total := resp.ContentLength
	pr := &progressReader{r: resp.Body, total: total, fn: func(pct int) { report(2, "Herunterladen", pct, "running") }}
	if _, err := io.Copy(tmpFile, pr); err != nil {
		return fmt.Errorf("Schreiben: %w", err)
	}
	tmpFile.Close()
	report(2, "Herunterladen", 100, "running")

	// 4. Argon2id-Hash prüfen (memory-hard, schwer zu fälschen).
	logf("Update: Argon2id-Hash berechnen…")
	report(3, "Prüfsumme prüfen", -1, "running")
	zipBytes, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		return fmt.Errorf("ZIP lesen: %w", err)
	}
	got := hashZIP(zipBytes, m.Version)
	zipBytes = nil
	if !strings.EqualFold(got, m.NodeArgon2) {
		return fmt.Errorf("Argon2id-Hash Mismatch (erwartet %s, erhalten %s)", m.NodeArgon2, got)
	}
	logf("Update: Hash OK")

	// 5. Entpacken in ein Staging-Verzeichnis INNERHALB der Installation (gleiches
	//    Dateisystem → atomare Renames möglich).
	staging := filepath.Join(opt.InstallDir, ".update-staging")
	os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return fmt.Errorf("Staging anlegen: %w", err)
	}
	defer os.RemoveAll(staging)
	report(4, "Entpacken", -1, "running")
	cmd := exec.CommandContext(ctx, "unzip", "-o", "-q", tmpFile.Name(), "-d", staging)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Entpacken: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// 6. Nur Programmteile tauschen – NIE data/, chunks/, fundus.env oder
	//    Schlüssel. (Früher wurde das ganze Installationsverzeichnis ersetzt –
	//    das hätte Blockchain, Wallets und Dateien gelöscht.)
	arch := runtime.GOARCH // der Helper läuft nativ auf dem Pi: arm64 oder arm
	nodeBin := filepath.Join(staging, "bin", "fundus-node-linux-"+arch)
	helperBin := filepath.Join(staging, "bin", "fundus-helper-linux-"+arch)
	if !fileExists(nodeBin) {
		return fmt.Errorf("Update-Paket enthält kein Binary für linux/%s", arch)
	}
	report(5, "Programm und Oberfläche austauschen", -1, "running")
	tx := &swapTx{logf: logf}
	fail := func(err error) error {
		tx.rollback()
		return err
	}
	if fileExists(filepath.Join(staging, "lua")) {
		luaDir := filepath.Join(opt.InstallDir, "lua")
		if err := tx.swapDir(filepath.Join(staging, "lua"), luaDir); err != nil {
			return fail(fmt.Errorf("Oberfläche tauschen: %w", err))
		}
		// Dateien, die erst bei der Installation auf dem Pi entstehen (z.B.
		// static/sodium.js, static/fonts/), stehen nicht im Update-Paket – aus
		// dem bisherigen Stand übernehmen, sonst fehlen sie nach dem Update.
		if n, err := carryOverMissing(luaDir+".bak", luaDir); err != nil {
			return fail(fmt.Errorf("Laufzeitdateien übernehmen: %w", err))
		} else if n > 0 {
			logf("Update: %d Laufzeitdatei(en) aus dem bisherigen Stand übernommen", n)
		}
	}
	fundusGID := groupID("fundus")
	if err := tx.swapFile(nodeBin, filepath.Join(opt.InstallDir, "bin", "fundus-node"), 0o750, 0, fundusGID); err != nil {
		return fail(fmt.Errorf("Node-Binary tauschen: %w", err))
	}
	for _, f := range []string{"revision.txt", "MANUAL.md", "README.md"} {
		if src := filepath.Join(staging, f); fileExists(src) {
			if err := tx.swapFile(src, filepath.Join(opt.InstallDir, f), 0o644, 0, 0); err != nil {
				return fail(fmt.Errorf("%s: %w", f, err))
			}
		}
	}
	// Webserver-Konfiguration: nur vorhandene Dateien ersetzen, dann prüfen.
	confDir := "/usr/local/openresty/nginx/conf/conf.d"
	nginxChanged := false
	for src, dst := range map[string]string{
		"fundus-http.conf":      "00-fundus-http.conf",
		"fundus-nginx.conf":     "fundus.conf",
		"fundus-nginx-ssl.conf": "fundus-ssl.conf",
	} {
		srcPath := filepath.Join(opt.InstallDir, "lua", src) // bereits getauschte Oberfläche
		dstPath := filepath.Join(confDir, dst)
		if fileExists(srcPath) && fileExists(dstPath) {
			if err := tx.swapFile(srcPath, dstPath, 0o644, 0, 0); err != nil {
				return fail(fmt.Errorf("Webserver-Konfiguration: %w", err))
			}
			nginxChanged = true
		}
	}
	if nginxChanged {
		if out, err := exec.Command("/usr/local/openresty/bin/openresty", "-t").CombinedOutput(); err != nil {
			return fail(fmt.Errorf("neue Webserver-Konfiguration fehlerhaft – Rollback: %s", strings.TrimSpace(string(out))))
		}
	}
	// Helper zuletzt (laufender Prozess behält die alte Datei bis zum Neustart).
	if fileExists(helperBin) {
		if err := tx.swapFile(helperBin, filepath.Join(opt.InstallDir, "bin", "fundus-helper"), 0o750, 0, 0); err != nil {
			return fail(fmt.Errorf("Helper-Binary tauschen: %w", err))
		}
	}
	tx.commit()
	logf("Update: Dateien installiert (Version %s)", m.Version)
	// Ab hier startet der Node neu; die Anzeige wartet, bis er mit der neuen
	// Version antwortet.
	report(6, "Neustart", -1, "restarting")

	// 7. Webserver neu laden, Node neu starten, Helper im Hintergrund neu starten.
	if nginxChanged || fileExists(filepath.Join(opt.InstallDir, "lua")) {
		_ = exec.Command("systemctl", "reload", "openresty").Run()
	}
	if opt.RestartCmd != "" {
		logf("Update: Neustart via %q", opt.RestartCmd)
		parts := strings.Fields(opt.RestartCmd)
		if len(parts) > 0 {
			if err := exec.Command(parts[0], parts[1:]...).Run(); err != nil {
				return fmt.Errorf("Neustart: %w", err)
			}
		}
	}
	_ = exec.Command("systemctl", "restart", "--no-block", "fundus-helper").Run()
	return nil
}

// ── Austausch mit Rollback ──────────────────────────────────────────────────

type swapTx struct {
	logf  func(string, ...interface{})
	undo  []func()
	clean []string
}

// swapDir ersetzt dst durch src (gleiches Dateisystem); dst wird zu dst.bak.
func (t *swapTx) swapDir(src, dst string) error {
	bak := dst + ".bak"
	os.RemoveAll(bak)
	hadOld := fileExists(dst)
	if hadOld {
		if err := os.Rename(dst, bak); err != nil {
			return err
		}
	}
	if err := os.Rename(src, dst); err != nil {
		if hadOld {
			os.Rename(bak, dst)
		}
		return err
	}
	t.undo = append(t.undo, func() {
		os.RemoveAll(dst)
		if hadOld {
			os.Rename(bak, dst)
		}
	})
	return nil
}

// swapFile kopiert src nach dst (über dst.new + Rename), alte Datei → dst.bak.
func (t *swapTx) swapFile(src, dst string, mode os.FileMode, uid, gid int) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	_ = os.Chmod(tmp, mode)
	if uid >= 0 && gid >= 0 {
		_ = os.Chown(tmp, uid, gid)
	}
	bak := dst + ".bak"
	hadOld := fileExists(dst)
	if hadOld {
		os.Remove(bak)
		if err := os.Rename(dst, bak); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		if hadOld {
			os.Rename(bak, dst)
		}
		return err
	}
	t.undo = append(t.undo, func() {
		if hadOld {
			os.Rename(bak, dst)
		} else {
			os.Remove(dst)
		}
	})
	return nil
}

func (t *swapTx) rollback() {
	for i := len(t.undo) - 1; i >= 0; i-- {
		t.undo[i]()
	}
	t.undo = nil
	if t.logf != nil {
		t.logf("Update: Rollback ausgeführt – alter Stand wiederhergestellt")
	}
}

// commit lässt die .bak-Dateien liegen (manuelles Zurückrollen möglich).
func (t *swapTx) commit() { t.undo = nil }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func groupID(name string) int {
	if g, err := user.LookupGroup(name); err == nil {
		if id, err := strconv.Atoi(g.Gid); err == nil {
			return id
		}
	}
	return 0
}

// ── Anstehendes Update (Installation auf Wunsch) ────────────────────────────

// Pending hält das zuletzt gefundene, gültig signierte neuere Manifest, bis der
// Betreiber es in den Einstellungen installiert.
type Pending struct {
	mu sync.Mutex
	m  *Manifest
}

func (p *Pending) Set(m *Manifest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil || isNewerVersion(m.Version, p.m.Version) {
		p.m = m
	}
}

func (p *Pending) Get() *Manifest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m
}

// =============================================================================
//  Manifest erstellen und signieren (Betreiber-Tool)
// =============================================================================
// CreateManifest erstellt und signiert ein Update-Manifest.
// privateKeyHex: Hex-kodierter Private Key des Fee-Collectors (ohne 0x).
func CreateManifest(version, description, nodeURL, nodeZipPath, privateKeyHex string) (*Manifest, error) {
	// ZIP-Inhalt laden und Argon2id-Hash berechnen
	// (memory-hard: 64 MiB RAM pro Versuch – schwer zu fälschen)
	zipBytes, err := os.ReadFile(nodeZipPath)
	if err != nil {
		return nil, fmt.Errorf("update: ZIP öffnen: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Berechne Argon2id-Hash (~3-5s auf Pi)…\n")
	argon2hex := hashZIP(zipBytes, version)
	zipBytes = nil // RAM freigeben

	m := &Manifest{
		Version:     version,
		Description: description,
		NodeURL:     nodeURL,
		NodeArgon2:  argon2hex,
		PublishedAt: time.Now().UTC(),
	}

	// Signieren
	keyHex := strings.TrimPrefix(privateKeyHex, "0x")
	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("update: Private Key dekodieren: %w", err)
	}
	privKey, err := crypto.ToECDSA(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("update: Private Key laden: %w", err)
	}

	payload := m.SigningPayload()
	hash    := accounts.TextHash([]byte(payload))
	sig, err := crypto.Sign(hash, privKey)
	if err != nil {
		return nil, fmt.Errorf("update: Signieren: %w", err)
	}
	sig[64] += 27 // Ethereum V-Wert

	m.Signature = "0x" + hex.EncodeToString(sig)
	return m, nil
}

// =============================================================================
//  Versionsnummern-Vergleich
// =============================================================================

// isNewerVersion gibt true zurück wenn candidate > current.
// Format: "Rnnn" (z.B. "R001", "R042").
func isNewerVersion(candidate, current string) bool {
	c := parseVersion(candidate)
	v := parseVersion(current)
	return c > v
}

// parseVersion extrahiert den numerischen Teil aus "Rnnn".
func parseVersion(v string) int {
	v = strings.TrimSpace(strings.ToUpper(v))
	v = strings.TrimPrefix(v, "R")
	n := 0
	fmt.Sscanf(v, "%d", &n)
	return n
}

// progressReader meldet den Download-Fortschritt (höchstens alle 500 ms).
type progressReader struct {
	r     io.Reader
	total int64
	done  int64
	last  time.Time
	fn    func(pct int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.total > 0 && time.Since(p.last) > 500*time.Millisecond {
		p.last = time.Now()
		p.fn(int(p.done * 100 / p.total))
	}
	return n, err
}

// carryOverMissing kopiert alle Dateien aus oldDir, die in newDir fehlen
// (Modus bleibt erhalten). Liefert die Anzahl übernommener Dateien.
func carryOverMissing(oldDir, newDir string) (int, error) {
	if !fileExists(oldDir) {
		return 0, nil
	}
	n := 0
	err := filepath.Walk(oldDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // nicht lesbare Einträge überspringen
		}
		rel, rerr := filepath.Rel(oldDir, path)
		if rerr != nil || rel == "." {
			return nil
		}
		dst := filepath.Join(newDir, rel)
		if info.IsDir() {
			if !fileExists(dst) {
				return os.MkdirAll(dst, info.Mode().Perm()|0o755)
			}
			return nil
		}
		if !info.Mode().IsRegular() || fileExists(dst) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if werr := os.WriteFile(dst, data, info.Mode().Perm()); werr != nil {
			return werr
		}
		n++
		return nil
	})
	return n, err
}
