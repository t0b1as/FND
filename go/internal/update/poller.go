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
		return
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.log.Warn("Update-Poll: Abruf fehlgeschlagen", zap.Error(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.log.Warn("Update-Poll: HTTP-Status", zap.Int("status", resp.StatusCode))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // max 1 MiB Manifest
	if err != nil {
		p.log.Warn("Update-Poll: Lesen", zap.Error(err))
		return
	}

	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		p.log.Warn("Update-Poll: Manifest-JSON ungültig", zap.Error(err))
		return
	}
	// Schon gesehen? Dann nichts tun (kein wiederholtes Auslösen/Broadcast).
	if m.Version == p.lastSeen {
		return
	}
	// Signatur prüfen — das Tor. Ungültige Manifeste werden ignoriert.
	if err := m.Verify(); err != nil {
		p.log.Warn("Update-Poll: Signatur ungültig — ignoriert", zap.Error(err))
		return
	}
	// Nur echte, neuere Versionen.
	if !isNewerVersion(m.Version, p.currentVersion) {
		return
	}
	p.lastSeen = m.Version
	p.log.Info("Update-Poll: neues gültiges Manifest gefunden",
		zap.String("version", m.Version))
	if p.onNewManifest != nil {
		p.onNewManifest(&m, raw)
	}
}
