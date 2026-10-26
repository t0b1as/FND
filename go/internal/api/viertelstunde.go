package api

// Viertelstundenwerte (R829): Bezug UND Einspeisung je 15 Minuten.
//
// ── Warum Viertelstunden ────────────────────────────────────────────────────
// Der Strommarkt rechnet in Viertelstunden ab, Smart-Meter-Gateways liefern
// Viertelstundenwerte. Feiner zu handeln, als abgerechnet wird, bringt nichts.
//
// ── Belegt durch zwei Ablesungen ────────────────────────────────────────────
// Ein Intervall braucht keine eigene Unterschrift: Es ist vollständig belegt
// durch ZWEI gerätesignierte Ablesungen (R827) – die erste ab Beginn der
// Viertelstunde (A) und die erste ab ihrem Ende (B). Beide tragen Bezug und
// Einspeisung als ganze Wattsekunden. Bezug = B.bezug − A.bezug, Einspeisung
// ebenso. Jeder kann die Unterschriften prüfen und nachrechnen.
//
// Fehlen Ablesungen (Leser aus, Neustart), spannt der nächste Wert über
// mehrere Viertelstunden und ist als LÜCKE gekennzeichnet – er lässt sich
// nicht auf einzelne Viertelstunden aufteilen und ist nicht handelbar.
//
// ── Speicher ────────────────────────────────────────────────────────────────
// Lokal je Intervall (RecordViertelstunde, 13 Monate). Einmal täglich geht je
// Zähler ein Tagesbündel mit allen Intervallen des Vortags in den verteilten
// Speicher (zwei Kopien). Dessen Prüfsumme ist der Fingerabdruck, der in
// Schritt 3 auf die Chain kommt.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/meter"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	viertelToleranz     = 60 // Sekunden, die eine Ablesung nach der Grenze liegen darf
	viertelSek          = 900
	viertelAufbewahrung = 395 * 24 * time.Hour // 13 Monate
)

type viertelStand struct {
	Zeit          int64  `json:"t"`
	WsBezug       string `json:"ws_bezug"`
	WsEinspeisung string `json:"ws_einspeisung"`
	Sig           string `json:"sig"`
}

// viertelWert ist ein Intervall – in genau dieser Form auch im Tagesbündel.
type viertelWert struct {
	Zaehler       string       `json:"zaehler"`
	Von           int64        `json:"von"` // Beginn der Viertelstunde (Unix, auf 15 min ausgerichtet)
	Bis           int64        `json:"bis"` // Ende (bei Lücke: Beginn der Viertelstunde von B)
	Geraet        string       `json:"geraet"`
	A             viertelStand `json:"a"`
	B             viertelStand `json:"b"`
	BezugWs       string       `json:"bezug_ws"`
	EinspeisungWs string       `json:"einspeisung_ws"`
	Luecke        bool         `json:"luecke,omitempty"`
	// Unvollstaendig: A oder B liegt mehr als viertelToleranz nach der Grenze
	// (etwa direkt nach einer Unterbrechung) – die Menge deckt dann nicht die
	// ganze Viertelstunde ab. Wie eine Lücke nicht handelbar.
	Unvollstaendig bool `json:"unvollstaendig,omitempty"`
}

var (
	viertelMu    sync.Mutex
	viertelStart = map[string]meter.Token{} // Zähler → erste Ablesung der laufenden Viertelstunde
)

func viertelStandAus(t meter.Token) viertelStand {
	return viertelStand{Zeit: t.Timestamp.Unix(), WsBezug: t.WsBezug, WsEinspeisung: t.WsEinspeisung, Sig: t.GeraetSig}
}

func wsDiff(b, a string) (string, bool) {
	x, ok1 := new(big.Int).SetString(b, 10)
	y, ok2 := new(big.Int).SetString(a, 10)
	if !ok1 || !ok2 {
		return "", false
	}
	d := new(big.Int).Sub(x, y)
	if d.Sign() < 0 {
		return "", false // Zählwerk rückläufig (Tausch?) – kein gültiges Intervall
	}
	return d.String(), true
}

// viertelstundeVerarbeiten wird für jede SIGNIERTE Ablesung mit Zählernummer
// aufgerufen (aus AblesungSpeichern).
func viertelstundeVerarbeiten(store *storage.Store, t meter.Token) {
	nr := strings.TrimSpace(t.MeterID)
	if store == nil || nr == "" || t.GeraetSig == "" {
		return
	}
	jetzt := t.Timestamp.Unix() / viertelSek
	viertelMu.Lock()
	a, hat := viertelStart[nr]
	if hat && a.Timestamp.Unix()/viertelSek == jetzt {
		viertelMu.Unlock()
		return // noch dieselbe Viertelstunde
	}
	viertelStart[nr] = t // erste Ablesung der neuen Viertelstunde
	viertelMu.Unlock()
	if !hat || a.GeraetPub != t.GeraetPub {
		return // erster Wert nach dem Start oder anderes Gerät: noch kein Intervall
	}
	von := (a.Timestamp.Unix() / viertelSek) * viertelSek
	bis := jetzt * viertelSek
	bezug, ok1 := wsDiff(t.WsBezug, a.WsBezug)
	einsp, ok2 := wsDiff(t.WsEinspeisung, a.WsEinspeisung)
	if !ok1 || !ok2 {
		return
	}
	w := viertelWert{Zaehler: nr, Von: von, Bis: bis, Geraet: t.GeraetPub,
		A: viertelStandAus(a), B: viertelStandAus(t), BezugWs: bezug, EinspeisungWs: einsp,
		Luecke:         bis-von != viertelSek,
		Unvollstaendig: a.Timestamp.Unix()-von > viertelToleranz || t.Timestamp.Unix()-bis > viertelToleranz}
	roh, _ := json.Marshal(w)
	var daten map[string]any
	_ = json.Unmarshal(roh, &daten)
	_ = store.Put(&storage.Record{ID: fmt.Sprintf("%s/%012d", zaehlerPfad(t), von), Type: storage.RecordViertelstunde,
		CreatedAt: time.Unix(bis, 0), Data: daten})
}

// pruefeViertel prüft ein Intervall: beide Unterschriften gültig, Mengen
// stimmen, A und B liegen an den Grenzen.
func pruefeViertel(w viertelWert) (sigOK, mengeOK, grenzenOK bool) {
	sigOK = meter.AblesungPruefen(w.Geraet, w.A.Sig, w.Zaehler, w.A.Zeit, w.A.WsBezug, w.A.WsEinspeisung) &&
		meter.AblesungPruefen(w.Geraet, w.B.Sig, w.Zaehler, w.B.Zeit, w.B.WsBezug, w.B.WsEinspeisung)
	b, ok1 := wsDiff(w.B.WsBezug, w.A.WsBezug)
	e, ok2 := wsDiff(w.B.WsEinspeisung, w.A.WsEinspeisung)
	mengeOK = ok1 && ok2 && b == w.BezugWs && e == w.EinspeisungWs
	// A gehört zur Beginn-Viertelstunde, B zur End-Viertelstunde.
	grenzenOK = w.A.Zeit/viertelSek*viertelSek == w.Von && w.B.Zeit/viertelSek*viertelSek == w.Bis && w.Bis > w.Von
	return
}

func wsZuKWh(ws string) float64 {
	x, ok := new(big.Int).SetString(ws, 10)
	if !ok {
		return 0
	}
	f, _ := new(big.Float).Quo(new(big.Float).SetInt(x), big.NewFloat(3.6e6)).Float64()
	return f
}

func viertelAusRecord(r *storage.Record) (viertelWert, bool) {
	if r == nil || r.Data == nil {
		return viertelWert{}, false
	}
	roh, err := json.Marshal(r.Data)
	if err != nil {
		return viertelWert{}, false
	}
	var w viertelWert
	return w, json.Unmarshal(roh, &w) == nil && w.Zaehler != ""
}

// GET /api/v1/viertelstunden?zaehler=<nr>&anzahl=96 – jüngste zuerst, geprüft.
func (s *Server) viertelstundenListe(c *gin.Context) {
	nr := strings.TrimSpace(c.Query("zaehler"))
	anzahl := 96
	fmt.Sscan(c.DefaultQuery("anzahl", "96"), &anzahl)
	if anzahl < 1 || anzahl > 2000 {
		anzahl = 96
	}
	var pfade []string
	if nr != "" {
		pfade = []string{zaehlerPfad(meter.Token{MeterID: nr})}
	} else {
		ablMu.Lock()
		for p := range ablZaehler {
			pfade = append(pfade, p)
		}
		ablMu.Unlock()
	}
	out := []gin.H{}
	for _, p := range pfade {
		recs, err := s.store.ListUnterPfad(storage.RecordViertelstunde, p)
		if err != nil {
			continue
		}
		sort.Slice(recs, func(i, j int) bool { return recs[i].ID > recs[j].ID })
		if len(recs) > anzahl {
			recs = recs[:anzahl]
		}
		for _, r := range recs {
			w, ok := viertelAusRecord(r)
			if !ok {
				continue
			}
			sig, menge, grenzen := pruefeViertel(w)
			out = append(out, gin.H{"zaehler": w.Zaehler, "von": w.Von, "bis": w.Bis,
				"bezug_kwh": wsZuKWh(w.BezugWs), "einspeisung_kwh": wsZuKWh(w.EinspeisungWs),
				"luecke": w.Luecke, "unvollstaendig": w.Unvollstaendig,
				"signaturen": sig, "menge_stimmt": menge, "grenzen": grenzen})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["von"].(int64) > out[j]["von"].(int64) })
	if len(out) > anzahl {
		out = out[:anzahl]
	}
	c.JSON(http.StatusOK, gin.H{"viertelstunden": out})
}

// ── Tagesbündel und Aufräumen ────────────────────────────────────────────────

var (
	viertelGebuendelt = map[string]bool{}      // "<pfad>/<datum>" in diesem Lauf schon erledigt
	viertelFehlschlag = map[string]time.Time{} // letzter gescheiterter Versuch – 30 min Pause
)

// viertelTagesbuendel legt je Zähler die Intervalle des Vortags (UTC) als ein
// Bündel in den verteilten Speicher. Einmal je Tag und Zähler.
func (s *Server) viertelTagesbuendel() {
	if s.fileStore == nil {
		return
	}
	gestern := time.Now().UTC().AddDate(0, 0, -1)
	datum := gestern.Format("2006-01-02")
	tagBeginn := time.Date(gestern.Year(), gestern.Month(), gestern.Day(), 0, 0, 0, 0, time.UTC).Unix()
	tagEnde := tagBeginn + 86400
	ablMu.Lock()
	pfade := make([]string, 0, len(ablZaehler))
	for p := range ablZaehler {
		pfade = append(pfade, p)
	}
	ablMu.Unlock()
	for _, p := range pfade {
		schluessel := p + "/" + datum
		if viertelGebuendelt[schluessel] || time.Since(viertelFehlschlag[schluessel]) < 30*time.Minute {
			continue
		}
		if r, err := s.store.Get(storage.RecordViertelTag, schluessel); err == nil && r != nil {
			viertelGebuendelt[schluessel] = true
			continue
		}
		recs, err := s.store.ListUnterPfad(storage.RecordViertelstunde, p)
		if err != nil {
			continue
		}
		var werte []viertelWert
		for _, r := range recs {
			if w, ok := viertelAusRecord(r); ok && w.Von >= tagBeginn && w.Von < tagEnde {
				werte = append(werte, w)
			}
		}
		if len(werte) == 0 {
			viertelGebuendelt[schluessel] = true
			continue
		}
		sort.Slice(werte, func(i, j int) bool { return werte[i].Von < werte[j].Von })
		roh, _ := json.Marshal(struct {
			V      int           `json:"v"`
			Datum  string        `json:"datum"`
			Werte  []viertelWert `json:"werte"`
		}{1, datum, werte})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		hash, err := s.fileStore.UploadWithRedundancy(ctx, bytes.NewReader(roh), "application/json",
			"zaehler-"+p+"-"+datum+".json", 2)
		cancel()
		if err != nil {
			viertelFehlschlag[schluessel] = time.Now()
			if s.log != nil {
				s.log.Warn("Viertelstunden: Tagesbündel nicht gespeichert, neuer Versuch in 30 min",
					zap.String("zaehler", p), zap.Error(err))
			}
			continue
		}
		_ = s.store.Put(&storage.Record{ID: schluessel, Type: storage.RecordViertelTag,
			Data: map[string]any{"datei": hash, "anzahl": len(werte), "datum": datum, "zaehler": p}})
		viertelGebuendelt[schluessel] = true
		if s.log != nil {
			s.log.Info("Viertelstunden: Tagesbündel gespeichert", zap.String("zaehler", p),
				zap.String("datum", datum), zap.Int("intervalle", len(werte)), zap.String("datei", hash))
		}
	}
}

var viertelAufgeraeumt time.Time

func (s *Server) viertelAufraeumen() {
	if time.Since(viertelAufgeraeumt) < 24*time.Hour {
		return
	}
	viertelAufgeraeumt = time.Now()
	grenze := fmt.Sprintf("%012d", time.Now().Add(-viertelAufbewahrung).Unix())
	ablMu.Lock()
	pfade := make([]string, 0, len(ablZaehler))
	for p := range ablZaehler {
		pfade = append(pfade, p)
	}
	ablMu.Unlock()
	for _, p := range pfade {
		recs, err := s.store.ListUnterPfad(storage.RecordViertelstunde, p)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if i := strings.LastIndex(r.ID, "/"); i >= 0 && r.ID[i+1:] < grenze {
				_ = s.store.HardDelete(storage.RecordViertelstunde, r.ID)
			}
		}
	}
}
