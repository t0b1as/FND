package api

// Betreiberbereich von außen freischalten (R650).
//
// Dateimanager und Einstellungen verwalten echte Pfade auf dem Pi, WLAN-Zugänge
// und Schlüssel. Deshalb sind sie standardmäßig auf das Heimnetz beschränkt.
// Wer sie von unterwegs braucht, kann sie hier freischalten – bewusst als
// EIGENE Entscheidung, mit Hinweis auf die Folgen.
//
// Umgesetzt als Datei, nicht als Einstellung im Node: Der Webserver prüft sie
// direkt, bevor überhaupt etwas an den Node geht. Das ist eine Schicht früher
// und damit die robustere Stelle.
//
// Umschalten geht NUR aus dem Heimnetz. Wäre es von außen möglich, würde ein
// übernommenes Konto genügen, um sich selbst den Zugang zu öffnen.

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (s *Server) externalAdminFlagPath() string {
	dir := "/opt/fundus/data"
	if s.cfg != nil && s.cfg.DataDir != "" {
		dir = s.cfg.DataDir
	}
	return filepath.Join(dir, "external-admin.enabled")
}

func (s *Server) externalAdminEnabled() bool {
	_, err := os.Stat(s.externalAdminFlagPath())
	return err == nil
}

// GET /api/v1/admin/external-access
func (s *Server) getExternalAdmin(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"enabled": s.externalAdminEnabled(),
		"lan":     isLANRequest(c),
	})
}

// POST /api/v1/admin/external-access  { "enabled": true|false }
func (s *Server) setExternalAdmin(c *gin.Context) {
	if !isLANRequest(c) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Das lässt sich nur aus dem Heimnetz umstellen."})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pfad := s.externalAdminFlagPath()
	if req.Enabled {
		if err := os.WriteFile(pfad, []byte("1\n"), 0o640); err != nil {
			s.internalError(c, err)
			return
		}
	} else if err := os.Remove(pfad); err != nil && !os.IsNotExist(err) {
		s.internalError(c, err)
		return
	}
	if s.log != nil {
		s.log.Warn("Betreiberbereich von außen umgestellt", zap.Bool("freigeschaltet", req.Enabled))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "enabled": req.Enabled})
}
