package api

// Wettersatelliten (R863): Anzeige dessen, was tools/fundus-meteor.py auf dem
// Pi mit Stick plant und aufnimmt.
//   /run/fundus-funk/meteor-plan.json   nächste Überflüge
//   /run/fundus-funk/meteor.lock        Überflug läuft gerade
//   <DataDir>/meteor/<JJJJMMTT-HHMM>_<sat>/fundus.json + Bilder
//   <DataDir>/meteor-auto               "an" / "aus" (hier umschaltbar)

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	reWetterOrdner = regexp.MustCompile(`^[0-9]{8}-[0-9]{4}_[A-Za-z0-9-]{1,20}$`)
	reWetterDatei  = regexp.MustCompile(`^[A-Za-z0-9 _().,+-]+(/[A-Za-z0-9 _().,+-]+)*\.(png|jpg|jpeg)$`)
)

func (s *Server) wetterAblage() string { return filepath.Join(s.cfg.DataDir, "meteor") }

func (s *Server) wetterAutomatik() bool {
	b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "meteor-auto"))
	return err != nil || strings.TrimSpace(string(b)) != "aus"
}

func wetterSatDumpDa() bool {
	for _, p := range []string{"/usr/bin/satdump", "/usr/local/bin/satdump"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// GET /api/v1/funk/wetter
func (s *Server) wetterStatus(c *gin.Context) {
	h := gin.H{"stick": funkHatStick(), "satdump": wetterSatDumpDa(), "automatik": s.wetterAutomatik(), "node": aeroNodeName()}
	if b, err := os.ReadFile("/run/fundus-funk/meteor-plan.json"); err == nil {
		var plan map[string]any
		if json.Unmarshal(b, &plan) == nil {
			// Nur kommende Überflüge, höchstens acht.
			if l, ok := plan["ueberfluege"].([]any); ok {
				var kommend []any
				for _, x := range l {
					if m, ok := x.(map[string]any); ok {
						if los, _ := m["los"].(float64); int64(los) > time.Now().Unix() && len(kommend) < 8 {
							kommend = append(kommend, m)
						}
					}
				}
				plan["ueberfluege"] = kommend
			}
			h["plan"] = plan
		}
	}
	if b, err := os.ReadFile("/run/fundus-funk/meteor.lock"); err == nil {
		var l map[string]any
		if json.Unmarshal(b, &l) == nil {
			h["laeuft"] = l
		}
	}
	var liste []map[string]any
	ordner, _ := os.ReadDir(s.wetterAblage())
	for _, o := range ordner {
		if !o.IsDir() || !reWetterOrdner.MatchString(o.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.wetterAblage(), o.Name(), "fundus.json"))
		if err != nil {
			continue
		}
		var info map[string]any
		if json.Unmarshal(b, &info) != nil {
			continue
		}
		delete(info, "spur")
		info["ordner"] = o.Name()
		liste = append(liste, info)
	}
	sort.Slice(liste, func(i, j int) bool { return liste[i]["ordner"].(string) > liste[j]["ordner"].(string) })
	if liste == nil {
		liste = []map[string]any{}
	}
	h["ueberfluege"] = liste
	c.JSON(http.StatusOK, h)
}

// GET /api/v1/funk/wetter/bild/:ordner/*datei – streng geprüft: nur Bilder
// innerhalb eines Überflug-Ordners, keine Pfade nach draußen.
func (s *Server) wetterBild(c *gin.Context) {
	ordner := c.Param("ordner")
	datei := strings.TrimPrefix(c.Param("datei"), "/")
	if !reWetterOrdner.MatchString(ordner) || !reWetterDatei.MatchString(datei) || strings.Contains(datei, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	basis := filepath.Join(s.wetterAblage(), ordner)
	pfad := filepath.Join(basis, filepath.FromSlash(datei))
	if !strings.HasPrefix(pfad, basis+string(os.PathSeparator)) {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(pfad)
}

// POST /api/v1/admin/funk/wetter {automatik}
func (s *Server) wetterAutomatikSetzen(c *gin.Context) {
	var req struct{ Automatik bool }
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	wert := "aus"
	if req.Automatik {
		wert = "an"
	}
	if err := os.WriteFile(filepath.Join(s.cfg.DataDir, "meteor-auto"), []byte(wert+"\n"), 0o664); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "automatik": req.Automatik})
}
