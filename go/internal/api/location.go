package api

// Eigene Node-Position (GPS-Koordinaten). Wird im Admin-Bereich gesetzt und dient
// als Referenz für die Distanzberechnung im Marktplatz, wenn der Nutzer keine
// Such-PLZ eingibt. Persistent in location.json im DataDir.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/gin-gonic/gin"
)

// NodeLocation ist die gespeicherte eigene Position.
type NodeLocation struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
	PLZ string  `json:"plz,omitempty"`
}

type locationStore struct {
	mu   sync.RWMutex
	path string
	loc  NodeLocation
}

func newLocationStore(dataDir string) *locationStore {
	ls := &locationStore{path: filepath.Join(dataDir, "location.json")}
	ls.load()
	return ls
}

func (ls *locationStore) get() NodeLocation {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.loc
}

// set speichert die Position. Gibt true, wenn eine brauchbare Position vorliegt.
func (ls *locationStore) set(loc NodeLocation) error {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.loc = loc
	data, err := json.MarshalIndent(loc, "", "  ")
	if err != nil {
		return err
	}
	tmp := ls.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, ls.path)
}

func (ls *locationStore) load() {
	data, err := os.ReadFile(ls.path)
	if err != nil {
		return
	}
	var l NodeLocation
	if json.Unmarshal(data, &l) == nil {
		ls.loc = l
	}
}

// hasPosition meldet, ob eine brauchbare Koordinate gesetzt ist.
func (l NodeLocation) hasPosition() bool {
	return l.Lat != 0 || l.Lon != 0
}

// GET /api/v1/admin/location — aktuelle eigene Position.
func (s *Server) adminGetLocation(c *gin.Context) {
	if s.locationStore == nil {
		c.JSON(http.StatusOK, gin.H{"lat": 0, "lon": 0})
		return
	}
	loc := s.locationStore.get()
	c.JSON(http.StatusOK, gin.H{"lat": loc.Lat, "lon": loc.Lon, "plz": loc.PLZ})
}

// POST /api/v1/admin/location — eigene Position setzen {lat, lon, plz?}.
func (s *Server) adminSetLocation(c *gin.Context) {
	if s.locationStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Speicher nicht verfügbar"})
		return
	}
	var req struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
		PLZ string  `json:"plz"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	// Plausibilitätsgrenzen: Lat -90..90, Lon -180..180.
	if req.Lat < -90 || req.Lat > 90 || req.Lon < -180 || req.Lon > 180 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Koordinaten außerhalb des gültigen Bereichs"})
		return
	}
	if err := s.locationStore.set(NodeLocation{Lat: req.Lat, Lon: req.Lon, PLZ: req.PLZ}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "lat": req.Lat, "lon": req.Lon})
}
