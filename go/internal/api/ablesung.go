package api

// Ablesungen bleiben lokal (R828).
//
// Bisher legte der Node JEDE Sekunde eine Ablesung als öffentlichen
// Datensatz ins Netz. Das Energie-Thema speichert jeder Node vollständig, und
// beim Verbinden gleichen die Nodes den Bestand ab: rund 35 MB je Zähler und
// Tag – auf JEDEM Node. Bei tausend Zählern 35 GB täglich.
//
// Jetzt:
//   - Jede Ablesung geht an den Zähler-Zertifizierer und an die Live-Anzeige.
//   - Gespeichert wird nur die ERSTE Ablesung jeder Minute, und nur lokal
//     (RecordAblesung steht nicht in PublicRecordTypes). Aufbewahrung 35 Tage.
//   - Ins Netz gehen nur die Belege der Zertifizierung.
//   - Die aufgelaufenen Sekundenwerte (RecordEnergy) werden im Hintergrund
//     schrittweise gelöscht.
//
// Nebenbei behoben: Die Live-Anzeige las bisher DENSELBEN Kanal wie main.go.
// Jede Ablesung ging nur an einen der beiden – bei offener Live-Anzeige
// fehlten dem Speicher Ablesungen. Jetzt verteilt AblesungSpeichern an alle.

import (
	"fmt"
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

const ablesungAufbewahrung = 35 * 24 * time.Hour

var (
	ablMu      sync.Mutex
	ablMinute  = map[string]int64{}               // Zählerpfad → zuletzt gespeicherte Minute
	ablZaehler = map[string]bool{}                // bekannte Zählerpfade (für Liste und Aufräumen)
	ablAbos    = map[chan meter.Token]struct{}{} // Live-Anzeigen
)

// zaehlerPfad macht aus der Zählernummer ein sicheres Pfadsegment.
func zaehlerPfad(t meter.Token) string {
	nr := strings.TrimSpace(t.MeterID)
	if nr == "" {
		return "lokal"
	}
	var b strings.Builder
	for _, r := range nr {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func ablesungDaten(t meter.Token) map[string]any {
	return map[string]any{
		"timestamp": t.Timestamp.UTC().Format(time.RFC3339), "meter_id": t.MeterID,
		"lat": t.Lat, "lon": t.Lon, "kwh": t.KWh, "kwh_feed": t.KWhFeed, "watt_now": t.WattNow,
		"ws_bezug": t.WsBezug, "ws_einspeisung": t.WsEinspeisung, "geraet": t.GeraetPub, "geraet_sig": t.GeraetSig,
	}
}

// AblesungSpeichern nimmt JEDE Ablesung des Zählerlesers entgegen (main.go)
// und von außen eingespielte (POST /api/v1/meter/reading).
func AblesungSpeichern(store *storage.Store, t meter.Token) {
	AblesungEingang(t)                // Zertifizierer (nur signierte mit Zählernummer)
	viertelstundeVerarbeiten(store, t) // R829: Viertelstunden aus signierten Ablesungen
	pfad := zaehlerPfad(t)
	minute := t.Timestamp.Unix() / 60
	ablMu.Lock()
	neu := ablMinute[pfad] != minute
	if neu {
		ablMinute[pfad] = minute
		ablZaehler[pfad] = true
	}
	abos := make([]chan meter.Token, 0, len(ablAbos))
	for ch := range ablAbos {
		abos = append(abos, ch)
	}
	ablMu.Unlock()
	for _, ch := range abos {
		select {
		case ch <- t:
		default: // langsame Anzeige: Ablesung auslassen statt den Leser aufzuhalten
		}
	}
	if neu && store != nil {
		_ = store.Put(&storage.Record{ID: fmt.Sprintf("%s/%012d", pfad, minute), Type: storage.RecordAblesung,
			CreatedAt: t.Timestamp, Data: ablesungDaten(t)})
	}
}

func ablesungAbonnieren() chan meter.Token {
	ch := make(chan meter.Token, 16)
	ablMu.Lock()
	ablAbos[ch] = struct{}{}
	ablMu.Unlock()
	return ch
}

func ablesungAbbestellen(ch chan meter.Token) {
	ablMu.Lock()
	delete(ablAbos, ch)
	ablMu.Unlock()
}

// GET /api/v1/energy – die jüngsten Minutenwerte dieses Nodes (höchstens 100).
// Bisher: ALLE Energie-Datensätze des Netzes bei jedem Aufruf.
func (s *Server) listEnergyTokens(c *gin.Context) {
	ablMu.Lock()
	pfade := make([]string, 0, len(ablZaehler))
	for p := range ablZaehler {
		pfade = append(pfade, p)
	}
	ablMu.Unlock()
	var alle []*storage.Record
	for _, p := range pfade {
		recs, err := s.store.ListUnterPfad(storage.RecordAblesung, p)
		if err != nil {
			continue
		}
		sort.Slice(recs, func(i, j int) bool { return recs[i].ID > recs[j].ID })
		if len(recs) > 100 {
			recs = recs[:100]
		}
		alle = append(alle, recs...)
	}
	sort.Slice(alle, func(i, j int) bool { return alle[i].CreatedAt.After(alle[j].CreatedAt) })
	if len(alle) > 100 {
		alle = alle[:100]
	}
	if alle == nil {
		alle = []*storage.Record{}
	}
	c.JSON(http.StatusOK, gin.H{"tokens": alle, "count": len(alle)})
}

// POST /api/v1/energy – Energie-Token von Hand gibt es nicht mehr (R828):
// eingetippte kWh belegen nichts, wie die freien Zertifikate (R826).
func (s *Server) createEnergyToken(c *gin.Context) {
	c.JSON(http.StatusGone, gin.H{"error": "Energie-Token von Hand gibt es nicht mehr. Ablesungen kommen direkt aus dem Zähler."})
}

// ── Aufräumen ────────────────────────────────────────────────────────────────

var ablAufgeraeumt time.Time

// ablesungAufraeumen löscht Minutenwerte älter als 35 Tage – einmal täglich.
func (s *Server) ablesungAufraeumen() {
	if time.Since(ablAufgeraeumt) < 24*time.Hour {
		return
	}
	ablAufgeraeumt = time.Now()
	grenze := fmt.Sprintf("%012d", time.Now().Add(-ablesungAufbewahrung).Unix()/60)
	ablMu.Lock()
	pfade := make([]string, 0, len(ablZaehler))
	for p := range ablZaehler {
		pfade = append(pfade, p)
	}
	ablMu.Unlock()
	n := 0
	for _, p := range pfade {
		recs, err := s.store.ListUnterPfad(storage.RecordAblesung, p)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if i := strings.LastIndex(r.ID, "/"); i >= 0 && r.ID[i+1:] < grenze {
				_ = s.store.HardDelete(storage.RecordAblesung, r.ID)
				n++
			}
		}
	}
	if n > 0 && s.log != nil {
		s.log.Info("Ablesungen: alte Minutenwerte gelöscht", zap.Int("anzahl", n))
	}
}

// sekundenwerteLoeschen räumt die aufgelaufenen Sekundenwerte (RecordEnergy)
// im Hintergrund ab – in Häppchen, damit der Node dabei ansprechbar bleibt.
func (s *Server) sekundenwerteLoeschen() {
	defer func() { _ = recover() }()
	time.Sleep(2 * time.Minute)
	gesamt := 0
	for {
		n, err := s.store.TypSchrittweiseLeeren(storage.RecordEnergy, 2000)
		if err != nil {
			if s.log != nil {
				s.log.Warn("Sekundenwerte: Löschen gescheitert", zap.Error(err))
			}
			return
		}
		gesamt += n
		if n == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if gesamt > 0 && s.log != nil {
		s.log.Info("Sekundenwerte aus dem Netz gelöscht", zap.Int("anzahl", gesamt))
	}
}
