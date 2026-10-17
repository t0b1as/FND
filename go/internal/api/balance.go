package api

// Lastverteilung steuern (R669).
//
// Der öffentlich erreichbare Node reicht die Schnittstelle an mehrere Pis
// weiter. Die Entscheidung trifft der Webserver (lua/balance.lua), hier wird
// sie nur ein- und ausgeschaltet und die Empfängerliste gepflegt.
//
// Warum über Dateien statt über Einstellungen im Node: Der Webserver prüft sie
// direkt, eine Schicht vor dem Node. Fällt der Node aus, greift die Verteilung
// trotzdem – und genau dann wird sie gebraucht.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/platform"
)

func (s *Server) balanceDir() string {
	if s.cfg != nil && s.cfg.DataDir != "" {
		return s.cfg.DataDir
	}
	return "/opt/fundus/data"
}

func (s *Server) balanceAnPfad() string    { return filepath.Join(s.balanceDir(), "balance.enabled") }
func (s *Server) balanceDirektPfad() string {
	return filepath.Join(s.balanceDir(), "balance-direct.enabled")
}
func (s *Server) balancePeersPfad() string { return filepath.Join(s.balanceDir(), "balance-peers.txt") }

// gueltigerPeer: nur einfache Host-Port-Angaben. Wer hier etwas einträgt,
// bestimmt, wohin Anfragen samt Sitzungsdaten gehen – also eng halten.
func gueltigerPeer(z string) bool {
	// Zusätze abtrennen (R670/R672): "host:port gewicht=8 peer=12D3Koo…"
	if vor, _, hat := strings.Cut(z, " "); hat {
		z = strings.TrimSpace(vor)
	}
	host, port, ok := strings.Cut(z, ":")
	if !ok || host == "" || port == "" || len(z) > 120 {
		return false
	}
	for _, r := range host {
		if !(r == '.' || r == '-' || (r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// GET /api/v1/admin/balance
func (s *Server) balanceGet(c *gin.Context) {
	_, err := os.Stat(s.balanceAnPfad())
	peers := []string{}
	if roh, e := os.ReadFile(s.balancePeersPfad()); e == nil {
		for _, z := range strings.Split(string(roh), "\n") {
			z = strings.TrimSpace(z)
			if z != "" && !strings.HasPrefix(z, "#") {
				peers = append(peers, z)
			}
		}
	}
	_, dErr := os.Stat(s.balanceDirektPfad())
	transport := "p2p"
	if dErr == nil {
		transport = "direkt"
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":   err == nil,
		"peers":     peers,
		"self":      s.nodeID(),
		"transport": transport,
	})
}

// POST /api/v1/admin/balance   { "enabled": bool, "peers": ["10.10.11.25:3000", …] }
func (s *Server) balanceSet(c *gin.Context) {
	if !isLANRequest(c) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Das lässt sich nur aus dem Heimnetz umstellen."})
		return
	}
	var req struct {
		Enabled   bool     `json:"enabled"`
		Peers     []string `json:"peers"`
		Transport string   `json:"transport"` // "p2p" (Vorgabe) oder "direkt"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Peers != nil {
		sauber := make([]string, 0, len(req.Peers))
		for _, p := range req.Peers {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !gueltigerPeer(p) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "Ungültige Adresse: " + p + " (erwartet host:port)"})
				return
			}
			sauber = append(sauber, p)
		}
		inhalt := "# Empfänger der Lastverteilung, je Zeile host:port (R669)\n" +
			strings.Join(sauber, "\n") + "\n"
		if err := os.WriteFile(s.balancePeersPfad(), []byte(inhalt), 0o640); err != nil {
			s.internalError(c, err)
			return
		}
	}
	// Einschalten ohne Empfänger wäre wirkungslos und verwirrend.
	if req.Enabled {
		roh, _ := os.ReadFile(s.balancePeersPfad())
		if len(strings.TrimSpace(string(roh))) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Erst Empfänger eintragen, dann einschalten."})
			return
		}
		if err := os.WriteFile(s.balanceAnPfad(), []byte("1\n"), 0o640); err != nil {
			s.internalError(c, err)
			return
		}
	} else if err := os.Remove(s.balanceAnPfad()); err != nil && !os.IsNotExist(err) {
		s.internalError(c, err)
		return
	}
	// Transportweg (R672). Vorgabe ist der verschlüsselte Tunnel; "direkt"
	// muss ausdrücklich gewählt werden.
	if req.Transport == "direkt" {
		if err := os.WriteFile(s.balanceDirektPfad(), []byte("1\n"), 0o640); err != nil {
			s.internalError(c, err)
			return
		}
	} else if req.Transport == "p2p" {
		if err := os.Remove(s.balanceDirektPfad()); err != nil && !os.IsNotExist(err) {
			s.internalError(c, err)
			return
		}
	}
	if s.log != nil {
		s.log.Warn("Lastverteilung umgestellt", zap.Bool("an", req.Enabled),
			zap.String("transport", req.Transport),
			zap.Int("empfaenger", len(req.Peers)))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "enabled": req.Enabled})
}

// ── Erkanntes Gerät für die Anzeige (R670) ───────────────────────────────

var erkanntesProfil platform.Profil

// SetPlatform hinterlegt das beim Start erkannte Profil.
func SetPlatform(p platform.Profil) { erkanntesProfil = p }

// GET /api/v1/admin/platform – was wurde erkannt und welche Grenzen gelten?
// Nützlich für die Lastverteilung: Das vorgeschlagene Gewicht steht dabei, man
// muss es also nicht selbst ausrechnen.
func (s *Server) platformGet(c *gin.Context) {
	p := erkanntesProfil
	c.JSON(http.StatusOK, gin.H{
		"modell":                   p.Modell,
		"ram_mib":                  p.RAMMiB,
		"kerne":                    p.Kerne,
		"klasse":                   p.Klasse,
		"bildpixel_max":            p.DecodePixel,
		"vorschau_gleichzeitig":    p.ThumbGleich,
		"ecdh_gleichzeitig":        p.ECDHGleich,
		"anmeldungen_gleichzeitig": p.LoginGleich,
		"lastgewicht":              p.BalanceAnteil,
	})
}
