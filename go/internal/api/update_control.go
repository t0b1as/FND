package api

// Software-Update auf Wunsch des Betreibers.
//
// Der Node prüft regelmäßig das signierte Release-Manifest auf GitHub (und
// empfängt es per P2P). Eine neuere Version wird hier nur ANGEBOTEN; installiert
// wird erst auf Klick in den Einstellungen (oder automatisch mit
// FUNDUS_UPDATE_AUTO=true). Die Installation übernimmt der privilegierte
// fundus-helper: Signatur- und Hashprüfung, Austausch von Programm und
// Oberfläche mit Rollback – data/, chunks/ und fundus.env bleiben unberührt.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fundus/node/internal/update"
)

// UpdateControl verbindet die API mit Poller/Helper aus main.go.
type UpdateControl struct {
	Current string
	Source  string
	Auto    bool
	Pending func() *update.Manifest
	Check   func()
	Apply   func(m *update.Manifest) error
	Info    func() update.PollInfo // Ergebnis der letzten GitHub-Prüfung
	HelperVersion func() string    // Revision des fundus-helper (führt Installationen aus)
}

// WithUpdateControl aktiviert die Update-Endpunkte.
func (s *Server) WithUpdateControl(uc *UpdateControl) *Server {
	s.updateCtl = uc
	return s
}

// GET /api/v1/admin/update/status
func (s *Server) updateStatus(c *gin.Context) {
	uc := s.updateCtl
	if uc == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "current": NodeRevision})
		return
	}
	out := gin.H{
		"enabled": uc.Source != "",
		"current": uc.Current,
		"source":  uc.Source,
		"auto":    uc.Auto,
	}
	if uc.HelperVersion != nil {
		out["helper"] = uc.HelperVersion()
	}
	if uc.Info != nil {
		if pi := uc.Info(); !pi.CheckedAt.IsZero() {
			out["last_check"] = pi
		}
	}
	if pr := s.readUpdateProgress(); pr != nil {
		// Laufende oder gerade beendete Installation (Statusanzeige).
		if pr.Version == uc.Current && pr.State != "failed" {
			pr.State = "done" // Node läuft bereits mit der neuen Version
		}
		out["progress"] = pr
	}
	if uc.Pending != nil {
		if m := uc.Pending(); m != nil {
			out["available"] = gin.H{
				"version":      m.Version,
				"description":  m.Description,
				"published_at": m.PublishedAt,
			}
		}
	}
	c.JSON(http.StatusOK, out)
}

// POST /api/v1/admin/update/check – sofort auf GitHub nachsehen.
func (s *Server) updateCheck(c *gin.Context) {
	uc := s.updateCtl
	if uc == nil || uc.Check == nil || uc.Source == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Update-Prüfung ist deaktiviert (FUNDUS_UPDATE_MANIFEST_URL=off)"})
		return
	}
	done := make(chan struct{})
	go func() { uc.Check(); close(done) }()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
	case <-c.Request.Context().Done():
		return
	}
	s.updateStatus(c)
}

// POST /api/v1/admin/update/apply – angebotenes Update installieren.
func (s *Server) updateApply(c *gin.Context) {
	uc := s.updateCtl
	if uc == nil || uc.Pending == nil || uc.Apply == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Update-Funktion nicht aktiv"})
		return
	}
	m := uc.Pending()
	if m == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Kein Update verfügbar"})
		return
	}
	s.writeUpdateProgress(&update.ApplyProgress{Version: m.Version, State: "running", Step: 0, Steps: 6,
		Label: "Installation wird gestartet", Percent: -1, UpdatedAt: time.Now()})
	// Die Installation läuft beim Helper (Minuten) und endet mit dem Neustart des
	// Nodes. Scheitert sie, bevor der Helper selbst Status schreibt, trägt der
	// Node den Fehler ein.
	go func() {
		if err := uc.Apply(m); err != nil {
			if cur := s.readUpdateProgress(); cur == nil || cur.State != "failed" {
				s.writeUpdateProgress(&update.ApplyProgress{Version: m.Version, State: "failed", Step: 0, Steps: 6,
					Label: "Installation gestartet", Percent: -1, Error: err.Error(), UpdatedAt: time.Now()})
			}
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{
		"ok":      true,
		"version": m.Version,
		"message": "Update wird installiert. Der Node startet danach neu – Seite in 2–5 Minuten neu laden.",
	})
}

// ── Fortschritt der Installation (Datei data/update-status.json) ────────────
//
// Der Helper (root) schreibt den Fortschritt; der Node liest ihn und zeigt ihn
// in den Einstellungen an. Die Datei überdauert den Neustart des Nodes.

func (s *Server) updateStatusPath() string {
	if s.cfg == nil || s.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.DataDir, "update-status.json")
}

// readUpdateProgress liefert den letzten Fortschritt (nur wenn jünger als 2 h).
func (s *Server) readUpdateProgress() *update.ApplyProgress {
	p := s.updateStatusPath()
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var pr update.ApplyProgress
	if json.Unmarshal(data, &pr) != nil || time.Since(pr.UpdatedAt) > 2*time.Hour {
		return nil
	}
	return &pr
}

func (s *Server) writeUpdateProgress(pr *update.ApplyProgress) {
	p := s.updateStatusPath()
	if p == "" {
		return
	}
	data, err := json.Marshal(pr)
	if err != nil {
		return
	}
	tmp := p + ".node.tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, p)
	}
}

// GET /api/v1/update/info – für den Update-Knopf in der Kopfzeile (jede Seite).
// Öffentlich, daher bewusst nur: laufende Revision + ob/welche neuere vorliegt
// (keine Quelle, keine Konfiguration). Installieren bleibt passwortgeschützt.
func (s *Server) updateInfo(c *gin.Context) {
	out := gin.H{"current": NodeRevision}
	if uc := s.updateCtl; uc != nil {
		if uc.Current != "" {
			out["current"] = uc.Current
		}
		if uc.Pending != nil {
			if m := uc.Pending(); m != nil && m.Version != "" {
				out["available"] = m.Version
			}
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}
