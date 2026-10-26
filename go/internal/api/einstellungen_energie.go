package api

// Energie-Einstellungen über die Oberfläche (R839).
//
// Wie beim Solana-Zugang: Die Werte liegen als Datei im Datenverzeichnis
// (energie-einstellungen.json) und haben VORRANG vor der fundus.env. Ein leerer
// Wert heißt: zurück zur fundus.env. Angewandt werden sie als
// Umgebungsvariablen – Handel (marktParameter) und Home-Assistant-Quelle lesen
// ihre Werte bei jedem Durchlauf neu, die Änderung wirkt also SOFORT. Nur die
// Zählerquelle braucht einen Neustart, weil der Zählerleser beim Start
// angelegt wird.
//
// Schutz: Admin-Routen – aus dem Heimnetz frei, über den Tunnel nur für den
// Eigentümer (adminAuthMiddleware). Der Home-Assistant-Token wird nie
// zurückgegeben, nur ob einer gesetzt ist.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/config"
	"github.com/gin-gonic/gin"
)

var (
	energieMu      sync.Mutex
	energieUrsprung = map[string]*string{} // Wert aus der fundus.env vor dem ersten Überschreiben
	reEntitaet      = regexp.MustCompile(`^sensor\.[a-z0-9_]{1,120}$`)
	reZaehlerNr     = regexp.MustCompile(`^[A-Za-z0-9 ]{4,40}$`)
	reWallet        = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
)

// energieSchluessel: erlaubte Einträge und ihre Prüfung (leer ist immer erlaubt).
var energieSchluessel = map[string]func(string) bool{
	"FUNDUS_METER_PROTOCOL": func(v string) bool {
		switch v {
		case "sml", "d0", "http", "homeassistant", "mock":
			return true
		}
		return false
	},
	"FUNDUS_METER_MOCK_PROFIL": func(v string) bool { return v == "erzeuger" || v == "verbraucher" },
	"FUNDUS_HA_URL": func(v string) bool {
		return (strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")) && len(v) < 300
	},
	"FUNDUS_HA_TOKEN":       func(v string) bool { return len(v) >= 20 && len(v) < 2000 && !strings.ContainsAny(v, " \n\r\t") },
	"FUNDUS_HA_BEZUG":       reEntitaet.MatchString,
	"FUNDUS_HA_EINSPEISUNG": reEntitaet.MatchString,
	"FUNDUS_HA_LEISTUNG":    reEntitaet.MatchString,
	"FUNDUS_HA_ZAEHLERNUMMER": func(v string) bool {
		return reEntitaet.MatchString(v) || reZaehlerNr.MatchString(v)
	},
	"FUNDUS_HA_INTERVALL":       zahlZwischen(1, 3600),
	"FUNDUS_NODE_LAT":           zahlZwischen(-90, 90),
	"FUNDUS_NODE_LON":           zahlZwischen(-180, 180),
	"FUNDUS_MARKT_TEILNAHME":    func(v string) bool { return v == "0" || v == "1" },
	"FUNDUS_MARKT_MINDESTPREIS": zahlZwischen(0, 1000),
	"FUNDUS_MARKT_HOECHSTPREIS": zahlZwischen(0, 1000),
	"FUNDUS_MARKT_MAX_KM":       zahlZwischen(0, 500),
	"FUNDUS_NETZ_GRUND":         zahlZwischen(0, 1000),
	"FUNDUS_NETZ_JE_KM":         zahlZwischen(0, 1000),
	"FUNDUS_NETZENTGELT_WALLET": reWallet.MatchString,
}

func zahlZwischen(min, max float64) func(string) bool {
	return func(v string) bool {
		f, err := strconv.ParseFloat(v, 64)
		return err == nil && f >= min && f <= max
	}
}

func energieDatei(dataDir string) string {
	return filepath.Join(dataDir, "energie-einstellungen.json")
}

func energieLesen(dataDir string) map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(energieDatei(dataDir)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	for k, v := range out {
		if pruef, ok := energieSchluessel[k]; !ok || v == "" || !pruef(v) {
			delete(out, k) // Unbekanntes oder Ungültiges nie anwenden
		}
	}
	return out
}

// energieAnwenden setzt gespeicherte Werte als Umgebungsvariablen; nicht
// (mehr) gespeicherte bekommen ihren Wert aus der fundus.env zurück.
func energieAnwenden(werte map[string]string) {
	energieMu.Lock()
	defer energieMu.Unlock()
	for k := range energieSchluessel {
		if _, gemerkt := energieUrsprung[k]; !gemerkt {
			if v, ok := os.LookupEnv(k); ok {
				vv := v
				energieUrsprung[k] = &vv
			} else {
				energieUrsprung[k] = nil
			}
		}
		if v, ok := werte[k]; ok {
			_ = os.Setenv(k, v)
		} else if u := energieUrsprung[k]; u != nil {
			_ = os.Setenv(k, *u)
		} else {
			_ = os.Unsetenv(k)
		}
	}
}

// EnergieEinstellungenLaden: beim Start, direkt nach dem Laden der
// Konfiguration – damit auch Zählerquelle und Position aus der Oberfläche
// gelten, bevor der Zählerleser angelegt wird.
func EnergieEinstellungenLaden(cfg *config.Config) {
	if cfg == nil || cfg.DataDir == "" {
		return
	}
	werte := energieLesen(cfg.DataDir)
	if len(werte) == 0 {
		return
	}
	energieAnwenden(werte)
	energieInKonfig(cfg)
}

func energieInKonfig(cfg *config.Config) {
	if v, ok := os.LookupEnv("FUNDUS_METER_PROTOCOL"); ok {
		cfg.MeterProtocol = v
	}
	if f, err := strconv.ParseFloat(os.Getenv("FUNDUS_NODE_LAT"), 64); err == nil {
		cfg.NodeLat = f
	}
	if f, err := strconv.ParseFloat(os.Getenv("FUNDUS_NODE_LON"), 64); err == nil {
		cfg.NodeLon = f
	}
}

// GET /api/v1/admin/energie
func (s *Server) energieEinstellungenGet(c *gin.Context) {
	gespeichert := energieLesen(s.cfg.DataDir)
	aktuell := gin.H{}
	quelle := gin.H{}
	for k := range energieSchluessel {
		if k == "FUNDUS_HA_TOKEN" {
			continue
		}
		aktuell[k] = os.Getenv(k)
		if _, ok := gespeichert[k]; ok {
			quelle[k] = "oberflaeche"
		} else if os.Getenv(k) != "" {
			quelle[k] = "fundus.env"
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"werte": aktuell, "quelle": quelle,
		"token_gesetzt":  os.Getenv("FUNDUS_HA_TOKEN") != "",
		"zaehler_aktiv":  s.cfg.MeterProtocol,
		"position":       gin.H{"lat": s.cfg.NodeLat, "lon": s.cfg.NodeLon},
	})
}

// POST /api/v1/admin/energie {werte: {SCHLÜSSEL: wert}} – leerer Wert = zurück
// zur fundus.env. Ein leerer Token heißt "unverändert lassen".
func (s *Server) energieEinstellungenSet(c *gin.Context) {
	var req struct {
		Werte         map[string]string `json:"werte"`
		TokenLoeschen bool              `json:"token_loeschen"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	if s.cfg.DataDir == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Kein Datenverzeichnis konfiguriert."})
		return
	}
	alt := energieLesen(s.cfg.DataDir)
	neu := map[string]string{}
	for k, v := range alt {
		neu[k] = v
	}
	for k, v := range req.Werte {
		pruef, ok := energieSchluessel[k]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unbekannte Einstellung: " + k})
			return
		}
		v = strings.TrimSpace(v)
		if k == "FUNDUS_HA_TOKEN" && v == "" {
			continue // leer = vorhandenen Token behalten
		}
		if v == "" {
			delete(neu, k)
			continue
		}
		if !pruef(v) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Wert für " + k + ": " + v})
			return
		}
		neu[k] = v
	}
	if req.TokenLoeschen {
		delete(neu, "FUNDUS_HA_TOKEN")
	}
	b, _ := json.MarshalIndent(neu, "", "  ")
	if err := os.WriteFile(energieDatei(s.cfg.DataDir), b, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	protokollVorher := s.cfg.MeterProtocol
	energieAnwenden(neu)
	// Position wirkt sofort; die Zählerquelle erst nach dem Neustart.
	if f, err := strconv.ParseFloat(os.Getenv("FUNDUS_NODE_LAT"), 64); err == nil {
		s.cfg.NodeLat = f
	}
	if f, err := strconv.ParseFloat(os.Getenv("FUNDUS_NODE_LON"), 64); err == nil {
		s.cfg.NodeLon = f
	}
	neustart := os.Getenv("FUNDUS_METER_PROTOCOL") != protokollVorher
	c.JSON(http.StatusOK, gin.H{"ok": true, "neustart_noetig": neustart})
}

// POST /api/v1/admin/energie/ha-test {url, token, bezug, einspeisung, zaehlernummer}
// Fragt Home Assistant mit den eingegebenen Werten ab, ohne sie zu speichern.
// Ein leerer Token nimmt den gespeicherten.
func (s *Server) energieHATest(c *gin.Context) {
	var req struct {
		URL, Token, Bezug, Einspeisung, Zaehlernummer string
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	if req.Token == "" {
		req.Token = os.Getenv("FUNDUS_HA_TOKEN")
	}
	basis := strings.TrimRight(strings.TrimSpace(req.URL), "/")
	if !energieSchluessel["FUNDUS_HA_URL"](basis) || req.Token == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "fehler": "Adresse (http://…) und Token angeben."})
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	abfragen := func(entitaet string) (string, string, string) {
		if !reEntitaet.MatchString(entitaet) {
			return "", "", "keine gültige Entität"
		}
		r, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, basis+"/api/states/"+entitaet, nil)
		if err != nil {
			return "", "", err.Error()
		}
		r.Header.Set("Authorization", "Bearer "+req.Token)
		resp, err := client.Do(r)
		if err != nil {
			return "", "", "nicht erreichbar: " + err.Error()
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return "", "", "Token abgelehnt"
		case http.StatusNotFound:
			return "", "", "Entität unbekannt"
		case http.StatusOK:
		default:
			return "", "", fmt.Sprintf("Antwort %d", resp.StatusCode)
		}
		var z struct {
			State      string         `json:"state"`
			Attributes map[string]any `json:"attributes"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&z); err != nil {
			return "", "", "unlesbare Antwort"
		}
		einheit, _ := z.Attributes["unit_of_measurement"].(string)
		return z.State, einheit, ""
	}
	ergebnis := gin.H{}
	ok := true
	for name, ent := range map[string]string{"bezug": req.Bezug, "einspeisung": req.Einspeisung, "zaehlernummer": req.Zaehlernummer} {
		if name == "zaehlernummer" && !strings.HasPrefix(ent, "sensor.") {
			ergebnis[name] = gin.H{"wert": ent, "hinweis": "als Nummer eingetragen"}
			continue
		}
		w, e, f := abfragen(ent)
		if f != "" {
			ok = false
			ergebnis[name] = gin.H{"fehler": f}
		} else {
			ergebnis[name] = gin.H{"wert": w, "einheit": e}
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": ok, "ergebnis": ergebnis})
}
