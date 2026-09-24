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
	if uc.Info != nil {
		if pi := uc.Info(); !pi.CheckedAt.IsZero() {
			out["last_check"] = pi
		}
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
	if err := uc.Apply(m); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"ok":      true,
		"version": m.Version,
		"message": "Update wird installiert. Der Node startet danach neu – Seite in 2–5 Minuten neu laden.",
	})
}

