package api

// Sekündlicher Nachbarschaftshandel (R840) – jede Sekunde handeln,
// viertelstündlich dokumentieren.
//
// ── Wer spricht mit wem ─────────────────────────────────────────────────────
// Nur ein Erzeuger, der GERADE einspeist, ergreift die Initiative. Er schickt
// jede Sekunde seinen Überschuss per Direktverbindung an die Teilnehmer in
// seiner Reichweite; jeder antwortet in derselben Verbindung mit seinem
// aktuellen Bezug. Kein Rundruf: Nodes außerhalb der Reichweite und Nodes,
// solange niemand einspeist, bekommen nichts. Andere Erzeuger im Umkreis
// erhalten die Meldung ebenfalls – so kennt jeder das gesamte Angebot und
// bestimmt die Knappheit richtig.
//
// ── Was jede Sekunde passiert ───────────────────────────────────────────────
// Der Erzeuger teilt seinen Überschuss nach denselben Regeln wie bisher zu
// (marktZuteilen: nächster zuerst, ein Preis nach Knappheit) – nur mit Watt
// statt Kilowattstunden – und summiert Wattsekunden und Preis je Verbraucher
// für die laufende Viertelstunde. In der nächsten Meldung teilt er jedem
// Verbraucher seine Zuteilung und den Preis mit: das Live-Preissignal.
//
// ── Dokumentiert wird viertelstündlich ──────────────────────────────────────
// Am Ende der Viertelstunde werden die Sekundensummen gegen die
// UNTERSCHRIEBENEN Viertelstundenwerte gedeckelt: Kein Erzeuger teilt mehr zu,
// als er nachweislich eingespeist hat; jeder Verbraucher nimmt höchstens seinen
// Bezug an (marktAnnehmenUndZahlen). Die Sekunden selbst sind nicht
// unterschrieben – sie können Energie nur VERTEILEN, nie erfinden.
//
// ── Echtheit ────────────────────────────────────────────────────────────────
// Eine Meldung zählt nur, wenn sie von dem Peer kommt, der im Angebot dieses
// Zählers steht (libp2p authentifiziert Peers). Niemand kann fremde Zähler mit
// Scheinbedarf vertreten.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const marktSekundeProtocol = "/fundus/markt-sekunde/1.0.0"

type marktSekMsg struct {
	Zaehler string  `json:"zaehler"`        // Absender: Zählernummer des Erzeugers
	TS      int64   `json:"ts"`             // Unix-Millisekunden
	W       float64 `json:"w"`              // aktueller Überschuss in W
	Preis   float64 `json:"preis"`          // Energiepreis der letzten Sekunde (FND/kWh)
	An      float64 `json:"an_w,omitempty"` // Zuteilung an DICH in der letzten Sekunde (W)
}

type marktSekAntwort struct {
	Zaehler string  `json:"zaehler"`
	BezugW  float64 `json:"bezug_w"` // aktueller Bezug in W (0 = kein Bedarf)
	EinspW  float64 `json:"einsp_w"` // aktuelle Einspeisung in W (für die Knappheit)
}

// ── Eigener Zustand je Sekunde ───────────────────────────────────────────────

type marktJetzt struct {
	mu        sync.Mutex
	zaehler   string
	bezugW    float64
	einspW    float64
	stand     time.Time
	letztEins time.Time // zuletzt wachsendes Einspeisezählwerk
	letztBez  time.Time // zuletzt wachsendes Bezugszählwerk
	kwhB      float64
	kwhE      float64
}

var mJetzt marktJetzt

// Richtung aus den Zählwerken (was wächst, fließt), Betrag aus der
// Momentanleistung – bei Zählern, die die Leistung ohne Vorzeichen melden,
// ist das die einzige verlässliche Richtung.
func (m *marktJetzt) aufnehmen(nr string, kwhB, kwhE, watt float64, t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.zaehler == nr {
		if kwhE > m.kwhE {
			m.letztEins = t
		}
		if kwhB > m.kwhB {
			m.letztBez = t
		}
	}
	m.zaehler, m.kwhB, m.kwhE, m.stand = nr, kwhB, kwhE, t
	w := math.Abs(watt)
	speistEin := watt < 0 || (m.letztEins.After(m.letztBez) && t.Sub(m.letztEins) < 30*time.Second)
	if speistEin {
		m.einspW, m.bezugW = w, 0
	} else {
		m.einspW, m.bezugW = 0, w
	}
}

func (m *marktJetzt) lesen() (string, float64, float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	frisch := time.Since(m.stand) < 5*time.Second
	return m.zaehler, m.bezugW, m.einspW, frisch
}

// ── Summen der laufenden Viertelstunden (Erzeuger) ───────────────────────────

type marktSumme struct {
	Ws      float64 // zugeteilte Wattsekunden
	PreisWs float64 // Σ Preis × Ws (für den energiegewichteten Preis)
	Km      float64
	Wallet  string
}

var (
	mSumMu    sync.Mutex
	mSummen   = map[int64]map[string]*marktSumme{} // Viertelstunde → Verbraucher → Summe
	mLetzte   = map[string]marktPosten{}           // Verbraucher → Zuteilung der letzten Sekunde
	mLive     = gin.H{}                            // für /api/v1/markt/live
	mEmpfang  = map[string]marktSekMsg{}           // Erzeuger-Zähler → letzte Meldung an MICH
	mEmpfZeit = map[string]time.Time{}
)

func viertelVon(t time.Time) int64 { return t.Unix() / viertelSek * viertelSek }

// ── Empfang: Erzeuger meldet Überschuss → ich antworte mit meinem Bedarf ─────

func (s *Server) registerMarktSekunde() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(marktSekundeProtocol, func(peerID string, data []byte) []byte {
		var m marktSekMsg
		if json.Unmarshal(data, &m) != nil || m.Zaehler == "" || !marktParameter().Teilnahme {
			return nil
		}
		if d := time.Since(time.UnixMilli(m.TS)); d > 10*time.Second || d < -10*time.Second {
			return nil
		}
		// Nur der Peer aus dem Angebot dieses Zählers darf für ihn sprechen.
		if a, _ := s.store.Get(storage.RecordMarktAngebot, m.Zaehler); a == nil || a.Data == nil {
			return nil
		} else if p, _ := a.Data["peer"].(string); p == "" || p != peerID {
			return nil
		}
		mSumMu.Lock()
		mEmpfang[m.Zaehler], mEmpfZeit[m.Zaehler] = m, time.Now()
		mSumMu.Unlock()
		nr, bezug, einsp, frisch := mJetzt.lesen()
		if !frisch || nr == "" {
			return nil
		}
		raw, _ := json.Marshal(marktSekAntwort{Zaehler: nr, BezugW: bezug, EinspW: einsp})
		return raw
	})
}

// ── Eigene Ablesungen jede Sekunde ───────────────────────────────────────────

func (s *Server) marktSekundeSchleife() {
	defer func() {
		if r := recover(); r != nil && s.log != nil {
			s.log.Error("Sekundenhandel abgebrochen", zap.Any("grund", r))
		}
	}()
	abo := ablesungAbonnieren()
	go func() {
		for t := range abo {
			if nr := strings.TrimSpace(t.MeterID); nr != "" {
				mJetzt.aufnehmen(nr, t.KWh, t.KWhFeed, t.WattNow, t.Timestamp)
			}
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		p := marktParameter()
		if !p.Teilnahme {
			continue
		}
		nr, _, einsp, frisch := mJetzt.lesen()
		if !frisch || nr == "" || einsp <= 0 {
			s.marktLiveVerbraucher()
			continue
		}
		s.marktSekundeErzeuger(nr, einsp, p)
	}
}

// marktSekundeErzeuger: eine Handelssekunde als Erzeuger.
func (s *Server) marktSekundeErzeuger(nr string, einspW float64, p marktParam) {
	lat, lon := s.marktPosition()
	_, wallet, err := s.zaehlerWallet()
	if err != nil || (lat == 0 && lon == 0) {
		return
	}
	// Teilnehmer in Reichweite aus den Angeboten.
	type ziel struct {
		t    marktTeilnehmer
		peer string
	}
	var ziele []ziel
	alle, _ := s.store.List(storage.RecordMarktAngebot)
	for _, a := range alle {
		if a == nil || a.DeletedAt != nil || a.Data == nil {
			continue
		}
		z, _ := a.Data["zaehler"].(string)
		peer, _ := a.Data["peer"].(string)
		if z == "" || z == nr || peer == "" || peer == s.nodeID() {
			continue
		}
		f := func(k string) float64 { v, _ := toFloatOK(a.Data[k]); return v }
		t := marktTeilnehmer{Zaehler: z, Lat: f("lat"), Lon: f("lon"), Mindest: f("mindest"),
			Hoechst: f("hoechst"), MaxKm: f("max_km")}
		t.Wallet, _ = a.Data["wallet"].(string)
		if haversineKm(lat, lon, t.Lat, t.Lon) > math.Min(p.MaxKm, t.MaxKm) {
			continue
		}
		ziele = append(ziele, ziel{t, peer})
	}
	if len(ziele) == 0 {
		return
	}
	// Meldung an alle in Reichweite, parallel; Antwort = deren Bedarf.
	mSumMu.Lock()
	letzte := map[string]marktPosten{}
	for k, v := range mLetzte {
		letzte[k] = v
	}
	mSumMu.Unlock()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var verbraucher, erzeuger []marktTeilnehmer
	jetzt := time.Now()
	for _, z := range ziele {
		wg.Add(1)
		go func(z ziel) {
			defer wg.Done()
			defer func() { _ = recover() }()
			msg := marktSekMsg{Zaehler: nr, TS: jetzt.UnixMilli(), W: einspW}
			if l, ok := letzte[z.t.Zaehler]; ok {
				msg.Preis, msg.An = l.Preis, l.KWh // KWh trägt hier Watt (siehe unten)
			}
			raw, _ := json.Marshal(msg)
			ctx, abbr := context.WithTimeout(context.Background(), 800*time.Millisecond)
			defer abbr()
			antw, err := s.node.SendAndReceive(ctx, z.peer, marktSekundeProtocol, raw)
			if err != nil || len(antw) == 0 {
				return
			}
			var a marktSekAntwort
			if json.Unmarshal(antw, &a) != nil || a.Zaehler != z.t.Zaehler {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if a.BezugW > 0 {
				t := z.t
				t.KWh = a.BezugW
				verbraucher = append(verbraucher, t)
			}
			if a.EinspW > 0 {
				t := z.t
				t.KWh = a.EinspW
				erzeuger = append(erzeuger, t)
			}
		}(z)
	}
	wg.Wait()
	ich := marktTeilnehmer{Zaehler: nr, Lat: lat, Lon: lon, Mindest: p.Mindest, Hoechst: p.Hoechst,
		MaxKm: p.MaxKm, Wallet: strings.ToLower(wallet.Hex()), KWh: einspW}
	erzeuger = append(erzeuger, ich)
	// Dieselbe Rechnung wie je Viertelstunde – mit Watt statt kWh. Sie ist
	// linear in der Menge, Preis und Reihenfolge bleiben gleich.
	posten, knapp, preis := marktZuteilen(ich, erzeuger, verbraucher, p)

	von := viertelVon(jetzt)
	mSumMu.Lock()
	sum := mSummen[von]
	if sum == nil {
		sum = map[string]*marktSumme{}
		mSummen[von] = sum
	}
	neueLetzte := map[string]marktPosten{}
	var zugeteilt float64
	for _, x := range posten {
		ws := x.KWh * 1.0 // Watt × 1 s
		e := sum[x.An]
		if e == nil {
			e = &marktSumme{Km: x.Km, Wallet: x.Wallet}
			sum[x.An] = e
		}
		e.Ws += ws
		e.PreisWs += x.Preis * ws
		neueLetzte[x.An] = x
		zugeteilt += x.KWh
	}
	mLetzte = neueLetzte
	for v := range mSummen { // alte Viertelstunden nach 3 h vergessen
		if v < von-3*3600 {
			delete(mSummen, v)
		}
	}
	mLive = gin.H{"rolle": "erzeuger", "einspeisung_w": einspW, "zugeteilt_w": zugeteilt,
		"preis": preis, "knappheit": knapp, "abnehmer": len(posten), "zeit": jetzt.Unix()}
	mSumMu.Unlock()
}

// marktLiveVerbraucher: bester aktueller Preis aus den Meldungen der Erzeuger.
func (s *Server) marktLiveVerbraucher() {
	p := marktParameter()
	_, bezug, _, frisch := mJetzt.lesen()
	mSumMu.Lock()
	defer mSumMu.Unlock()
	var angebote []gin.H
	bester := math.Inf(1)
	var p2p float64
	for z, m := range mEmpfang {
		if time.Since(mEmpfZeit[z]) > 3*time.Second {
			continue
		}
		km := 0.0
		if a, _ := s.store.Get(storage.RecordMarktAngebot, z); a != nil && a.Data != nil {
			la, _ := toFloatOK(a.Data["lat"])
			lo, _ := toFloatOK(a.Data["lon"])
			mlat, mlon := s.marktPosition()
			km = haversineKm(mlat, mlon, la, lo)
		}
		gesamt := m.Preis + netzentgelt(p, km)
		angebote = append(angebote, gin.H{"von": z, "ueberschuss_w": m.W, "an_mich_w": m.An,
			"preis": m.Preis, "netz": netzentgelt(p, km), "km": km})
		if m.An > 0 {
			p2p += m.An
			if m.Preis > 0 && gesamt < bester {
				bester = gesamt
			}
		}
	}
	live := gin.H{"rolle": "verbraucher", "bezug_w": bezug, "vom_nachbarn_w": math.Min(p2p, bezug),
		"angebote": angebote, "zeit": time.Now().Unix(), "aktuell": frisch}
	if !math.IsInf(bester, 1) {
		live["bester_preis"] = bester
	}
	mLive = live
}

// marktSekundenPosten: Sekundensummen einer Viertelstunde als Zuteilung,
// gedeckelt auf die UNTERSCHRIEBENE Einspeisung. Nil = keine Sekundendaten
// (etwa nach einem Neustart) – dann teilt marktLauf wie bisher je
// Viertelstunde zu.
func marktSekundenPosten(von int64, einspKWh float64, p marktParam) ([]marktPosten, float64) {
	mSumMu.Lock()
	sum := mSummen[von]
	mSumMu.Unlock()
	if len(sum) == 0 {
		return nil, 0
	}
	var gesamt float64
	for _, e := range sum {
		gesamt += e.Ws
	}
	gesamtKWh := gesamt / 3.6e6
	faktor := 1.0
	if gesamtKWh > einspKWh && gesamtKWh > 0 {
		faktor = einspKWh / gesamtKWh // nie mehr zuteilen, als nachweislich eingespeist
	}
	var out []marktPosten
	var preisSumme, mengeSumme float64
	for an, e := range sum {
		if e.Ws <= 0 {
			continue
		}
		kwh := e.Ws / 3.6e6 * faktor
		preis := e.PreisWs / e.Ws
		out = append(out, marktPosten{An: an, KWh: kwh, Preis: preis, Netz: netzentgelt(p, e.Km),
			Km: e.Km, Wallet: e.Wallet})
		preisSumme += preis * kwh
		mengeSumme += kwh
	}
	mittel := 0.0
	if mengeSumme > 0 {
		mittel = preisSumme / mengeSumme
	}
	return out, mittel
}

// GET /api/v1/markt/live – das Preissignal, etwa als REST-Sensor in Home Assistant.
func (s *Server) marktLive(c *gin.Context) {
	mSumMu.Lock()
	out := gin.H{}
	for k, v := range mLive {
		out[k] = v
	}
	mSumMu.Unlock()
	out["teilnahme"] = marktParameter().Teilnahme
	c.JSON(http.StatusOK, out)
}
