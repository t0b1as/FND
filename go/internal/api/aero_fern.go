package api

// Flugdaten über das Fundus-Netz (R879). Aufgezeichnet wird nur auf dem Pi
// mit Empfänger. Öffnet man die Aeronautik-Seite über einen anderen Pi – die
// Lastverteilung reicht Seiten auch an Pis ohne Stick weiter –, holt dieser
// Übersicht, Quartett, Spuren und Fotos vom nächsten Empfänger, so wie er
// es beim Live-Bild schon tut.
//
// Der Empfänger führt eine weitergeleitete Anfrage durch DIESELBEN Handler
// wie eine direkte (nur ein Code-Pfad). Erlaubt sind nur die vier lesenden
// Abfragen mit streng geprüften Parametern; je Peer höchstens 30 je Minute.
// Einstellungen ändern bleibt dem Pi vorbehalten, auf dem aufgezeichnet wird.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
)

const flugProtocol = "/fundus/flugdaten/1.0.0"

type flugFernAnfrage struct {
	Art      string `json:"art"` // uebersicht | spur | quartett | typbild
	Zeitraum string `json:"zeitraum,omitempty"`
	Datum    string `json:"datum,omitempty"`
	Fotos    bool   `json:"fotos,omitempty"`
	KIBilder bool   `json:"kibilder,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Von      string `json:"von,omitempty"`
	Bis      string `json:"bis,omitempty"`
	Datei    string `json:"datei,omitempty"`
}

type flugFernAntwort struct {
	Status int    `json:"status"`
	Typ    string `json:"typ"`
	Daten  string `json:"daten"` // base64
	Node   string `json:"node"`
}

var reHHMM = regexp.MustCompile(`^[0-9]{4}$`)

// flugLokal: zeichnet DIESER Pi auf (oder hat er es früher getan)?
func (s *Server) flugLokal() bool {
	if funkHatStick() || s.aeroLokal() != nil {
		return true
	}
	l, _ := filepath.Glob(filepath.Join(s.flugOrdner("tabellen"), "*.json*"))
	return len(l) > 0
}

// pfad: Anfrage → interner Pfad; nur erlaubte Arten und geprüfte Werte.
func (a flugFernAnfrage) pfad() (string, bool) {
	switch a.Art {
	case "uebersicht":
		return "/api/v1/aeronautik/aufzeichnung", true
	case "quartett":
		if (a.Zeitraum != "tag" && a.Zeitraum != "woche" && a.Zeitraum != "monat" && a.Zeitraum != "jahr") || !reFlugTag.MatchString(a.Datum) {
			return "", false
		}
		q := url.Values{"zeitraum": {a.Zeitraum}, "datum": {a.Datum}}
		if a.Fotos {
			q.Set("fotos", "1")
		}
		if a.KIBilder {
			q.Set("kibilder", "1")
		}
		return "/api/v1/aeronautik/quartett?" + q.Encode(), true
	case "spur":
		if !reFlugTag.MatchString(a.Tag) || !reHHMM.MatchString(a.Von) || !reHHMM.MatchString(a.Bis) {
			return "", false
		}
		return "/api/v1/aeronautik/aufzeichnung/spur?" + url.Values{"tag": {a.Tag}, "von": {a.Von}, "bis": {a.Bis}}.Encode(), true
	case "typbild":
		if !reTypBildDatei.MatchString(a.Datei) {
			return "", false
		}
		return "/api/v1/aeronautik/typbild/" + a.Datei, true
	}
	return "", false
}

// flugFernBearbeiten: auf dem Empfänger – Anfrage durch den normalen Handler.
func (s *Server) flugFernBearbeiten(peerID string, data []byte) []byte {
	antwort := func(a flugFernAntwort) []byte { b, _ := json.Marshal(a); return b }
	if !extRateAllow("flugfern:"+peerID, 30) {
		return antwort(flugFernAntwort{Status: http.StatusTooManyRequests})
	}
	var a flugFernAnfrage
	if json.Unmarshal(data, &a) != nil {
		return antwort(flugFernAntwort{Status: http.StatusBadRequest})
	}
	pfad, ok := a.pfad()
	if !ok || !s.flugLokal() {
		return antwort(flugFernAntwort{Status: http.StatusNotFound})
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, pfad, nil)
	req.RemoteAddr = "127.0.0.1:0" // über das Netz bereits begrenzt (s. o.)
	c.Request = req
	switch a.Art {
	case "uebersicht":
		s.flugUebersicht(c)
	case "quartett":
		s.flugQuartett(c)
	case "spur":
		s.flugSpur(c)
	case "typbild":
		c.Params = gin.Params{{Key: "datei", Value: a.Datei}}
		s.typBildAusliefern(c)
	}
	return antwort(flugFernAntwort{Status: rec.Code, Typ: rec.Header().Get("Content-Type"),
		Daten: base64.StdEncoding.EncodeToString(rec.Body.Bytes()), Node: aeroNodeName()})
}

// flugEmpfaenger: Peers mit Empfänger, nächster zuerst.
func (s *Server) flugEmpfaenger() []string {
	lat, lon := s.marktPosition()
	type kandidat struct {
		peer string
		km   float64
	}
	var ks []kandidat
	alle, _ := s.store.List(storage.RecordAeroEmpfaenger)
	for _, r := range alle {
		if r == nil || r.Data == nil {
			continue
		}
		if z, _ := toFloatOK(r.Data["zeit"]); time.Since(time.Unix(int64(z), 0)) > 30*time.Minute {
			continue
		}
		peer, _ := r.Data["peer"].(string)
		if peer == "" || peer == s.nodeID() {
			continue
		}
		rl, _ := toFloatOK(r.Data["lat"])
		ro, _ := toFloatOK(r.Data["lon"])
		ks = append(ks, kandidat{peer, haversineKm(lat, lon, rl, ro)})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].km < ks[j].km })
	l := make([]string, 0, len(ks))
	for _, k := range ks {
		l = append(l, k.peer)
	}
	return l
}

// flugWeiterleiten: Anfrage an den nächsten Empfänger; true = beantwortet.
func (s *Server) flugWeiterleiten(c *gin.Context, a flugFernAnfrage, frist time.Duration) bool {
	if s.node == nil {
		return false
	}
	body, _ := json.Marshal(a)
	for _, peer := range s.flugEmpfaenger() {
		ctx, abbr := context.WithTimeout(c.Request.Context(), frist)
		roh, err := s.node.SendAndReceive(ctx, peer, flugProtocol, body)
		abbr()
		if err != nil {
			continue
		}
		var ant flugFernAntwort
		if json.Unmarshal(roh, &ant) != nil || ant.Status == 0 || ant.Status == http.StatusNotFound && a.Art != "spur" {
			continue // dieser Peer zeichnet nicht auf – nächsten fragen
		}
		daten, err := base64.StdEncoding.DecodeString(ant.Daten)
		if err != nil {
			continue
		}
		if a.Art == "typbild" && ant.Status == http.StatusOK {
			// Foto hier ablegen – das nächste Mal ohne Umweg.
			_ = os.MkdirAll(s.typBildOrdner(), 0o755)
			_ = os.WriteFile(filepath.Join(s.typBildOrdner(), a.Datei), daten, 0o644)
		}
		if a.Art == "uebersicht" || a.Art == "quartett" {
			// Für die Seite: woher die Daten stammen.
			var m map[string]any
			if json.Unmarshal(daten, &m) == nil {
				m["empfaenger_node"] = ant.Node
				m["fern"] = true
				daten, _ = json.Marshal(m)
			}
		}
		c.Data(ant.Status, ant.Typ, daten)
		return true
	}
	return false
}

func (s *Server) flugFernStarten() {
	if s.node != nil {
		s.node.RegisterProtocol(flugProtocol, s.flugFernBearbeiten)
	}
}
