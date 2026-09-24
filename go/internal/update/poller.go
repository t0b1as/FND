package update

// Git-Poller: holt periodisch ein signiertes Update-Manifest von einer festen
// URL (z.B. GitHub raw / Release-Asset). Bei einem gültig signierten, neueren
// Manifest wird ein Callback ausgelöst — der Node nutzt ihn, um das Manifest
// (a) per P2P weiterzuverbreiten und (b) über den Helper anzuwenden.
//
// Das ergänzt die P2P-Verbreitung: Nodes, die zum Broadcast-Zeitpunkt offline
// waren, holen sich den Stand beim nächsten Poll trotzdem. Quelle bleibt das
// signierte Manifest — Git ist nur der Transportweg, die Signatur ist das Tor.

import (
	"fmt"
	"sync"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// Poller fragt regelmäßig eine Manifest-URL ab.
type Poller struct {
	url            string
	currentVersion string
	interval       time.Duration
	httpClient     *http.Client
	log            *zap.Logger

	// onNewManifest wird bei einem gültig signierten, neueren Manifest aufgerufen.
	// Die Signaturprüfung ist zu diesem Zeitpunkt bereits erfolgt.
	onNewManifest func(m *Manifest, raw []byte)

	// lastSeen verhindert wiederholtes Auslösen für dieselbe Version.
	lastSeen string
	mu       sync.Mutex // serialisiert Abfragen (Takt + "Jetzt prüfen")

	// Ergebnis der letzten Prüfung (für die Anzeige in den Einstellungen).
	infoMu sync.Mutex
	info   PollInfo
}

// PollInfo beschreibt die letzte Prüfung: Zeitpunkt, gefundene Version und
// warum (nicht) angeboten wird.
type PollInfo struct {
	CheckedAt time.Time `json:"checked_at"`
	Remote    string    `json:"remote"` // Version im Manifest ("" = nicht gelesen)
	Status    string    `json:"status"` // newer | not_newer | bad_signature | unreachable | invalid
	Note      string    `json:"note"`
}

func (p *Poller) setInfo(remote, status, note string) {
	p.infoMu.Lock()
	p.info = PollInfo{CheckedAt: time.Now(), Remote: remote, Status: status, Note: note}
	p.infoMu.Unlock()
}

// Info liefert das Ergebnis der letzten Prüfung.
func (p *Poller) Info() PollInfo {
	p.infoMu.Lock()
	defer p.infoMu.Unlock()
	return p.info
}

// CheckNow fragt sofort ab (Button "Jetzt prüfen") und meldet eine gefundene
// neuere Version auch dann, wenn sie schon einmal gesehen wurde.
func (p *Poller) CheckNow(ctx context.Context) {
	p.mu.Lock()
	p.lastSeen = ""
	p.mu.Unlock()
	p.pollOnce(ctx)
}

// NewPoller erstellt einen Git-Poller. interval 0 → Standard 15 Minuten.
func NewPoller(url, currentVersion string, interval time.Duration, log *zap.Logger, onNew func(m *Manifest, raw []byte)) *Poller {
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	return &Poller{
		url:            url,
		currentVersion: currentVersion,
		interval:       interval,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		log:            log,
		onNewManifest:  onNew,
	}
}

// Run startet die Poll-Schleife bis der Context beendet wird. Blockiert — als
// Goroutine starten. Ein erster Poll erfolgt sofort, dann im Intervall.
func (p *Poller) Run(ctx context.Context) {
	if p.url == "" {
		return // Git-Polling deaktiviert
	}
	p.log.Info("Update-Git-Poller gestartet", zap.String("url", p.url), zap.Duration("intervall", p.interval))
	// Erster Poll leicht verzögert, damit Netz/DHT erst hochkommen.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.pollOnce(ctx)
			timer.Reset(p.interval)
		}
	}
}

func (p *Poller) pollOnce(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, p.url, nil)
	if err != nil {
		p.log.Warn("Update-Poll: Request", zap.Error(err))
		p.setInfo("", "invalid", err.Error())
		return
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.log.Warn("Update-Poll: Abruf fehlgeschlagen", zap.Error(err))
		p.setInfo("", "unreachable", "GitHub nicht erreichbar: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.log.Warn("Update-Poll: HTTP-Status", zap.Int("status", resp.StatusCode))
		p.setInfo("", "unreachable", fmt.Sprintf("Manifest nicht abrufbar (HTTP %d)", resp.StatusCode))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // max 1 MiB Manifest
	if err != nil {
		p.log.Warn("Update-Poll: Lesen", zap.Error(err))
		p.setInfo("", "unreachable", err.Error())
		return
	}

	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		p.log.Warn("Update-Poll: Manifest-JSON ungültig", zap.Error(err))
		p.setInfo("", "invalid", "Manifest ist kein gültiges JSON")
		return
	}
	// Schon gesehen? Dann nichts tun (kein wiederholtes Auslösen/Broadcast).
	if m.Version == p.lastSeen {
		p.setInfo(m.Version, "newer", "bereits angeboten")
		return
	}
	// Signatur prüfen — das Tor. Ungültige Manifeste werden ignoriert.
	if err := m.Verify(); err != nil {
		p.log.Warn("Update-Poll: Signatur ungültig — ignoriert", zap.Error(err))
		p.setInfo(m.Version, "bad_signature", err.Error())
		return
	}
	// Nur echte, neuere Versionen.
	if !isNewerVersion(m.Version, p.currentVersion) {
		p.setInfo(m.Version, "not_newer", "installierte Version "+p.currentVersion+" ist gleich oder neuer")
		return
	}
	p.setInfo(m.Version, "newer", "neuere, gültig signierte Version")
	p.lastSeen = m.Version
	p.log.Info("Update-Poll: neues gültiges Manifest gefunden",
		zap.String("version", m.Version))
	if p.onNewManifest != nil {
		p.onNewManifest(&m, raw)
	}
}
