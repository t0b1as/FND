package api

// Nachbarschaftshandel – Variante A (R832).
//
// Ein Teil des Stroms wird je Viertelstunde zwischen Nachbarn verrechnet, der
// Rest läuft wie gewohnt über Lieferant und Einspeisevergütung. Rein
// finanziell: Die offizielle Abrechnung bleibt unberührt; der Handel ist ein
// Ausgleich obendrauf. Er wirkt, weil der Preis Verhalten lenkt – bei
// Überangebot wird Strom vom Nachbarn billig, flexible Lasten springen an.
//
// ── Ablauf je Viertelstunde ─────────────────────────────────────────────────
// 1. Jeder Teilnehmer veröffentlicht seine Viertelstundenwerte (R829) –
//    gerätesigniert, also für jeden prüfbar – und ein Angebot mit Position,
//    Preisgrenzen und Reichweite.
// 2. Jeder ERZEUGER teilt seine Einspeisung zu: nächster Verbraucher zuerst.
//    EIN Energiepreis je Erzeuger:
//        preis = mindest + (cmin − mindest) × knappheit
//    knappheit = Nachfrage ÷ Angebot im Umkreis (höchstens 1); cmin = was der
//    knappste zahlungsbereite Verbraucher nach Netzentgelt noch zahlen würde.
//    Überangebot → Preis nahe der Untergrenze; Knappheit → nahe cmin.
//    Wer eine hohe Obergrenze angibt, zahlt dadurch NICHT mehr.
// 3. Jeder VERBRAUCHER nimmt die günstigsten Zuteilungen an, bis sein Bezug
//    gedeckt ist, und bezahlt sie aus seiner Zähler-Wallet. Der Rest seines
//    Bezugs läuft über den Tarif.
//
// Niemand zahlt mehr, als er bezogen hat, und kein Erzeuger bekommt mehr
// bezahlt, als er eingespeist hat: Der Erzeuger teilt nur seine Einspeisung zu,
// der Verbraucher nimmt höchstens seinen Bezug an.
//
// ── Netzentgelt nach Entfernung ─────────────────────────────────────────────
// Statt der tatsächlichen Netztopologie die Luftlinie (Haversine) zwischen den
// Node-Positionen: netzentgelt = grund + je_km × km. Sie wächst mit dem
// Leitungsweg und reicht für ein Preissignal, das lokalen Verbrauch belohnt.
// Das Entgelt geht an FUNDUS_NETZENTGELT_WALLET; ohne diese Angabe wird es
// berechnet und angezeigt, aber nicht überwiesen.
//
// ── Prüfbarkeit ─────────────────────────────────────────────────────────────
// Ein Erzeuger teilt nur Viertelstunden zu, deren Geräteunterschriften stimmen
// und deren Gerät zum Angebot passt. Lücken und unvollständige Intervalle sind
// nicht handelbar (R829).

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ── Parameter ────────────────────────────────────────────────────────────────

type marktParam struct {
	Teilnahme  bool    `json:"teilnahme"`
	Mindest    float64 `json:"mindestpreis"` // FND/kWh, die der Erzeuger mindestens will
	Hoechst    float64 `json:"hoechstpreis"` // FND/kWh, die der Verbraucher höchstens zahlt (inkl. Netzentgelt)
	MaxKm      float64 `json:"max_km"`
	NetzGrund  float64 `json:"netz_grund"`  // FND/kWh
	NetzJeKm   float64 `json:"netz_je_km"`  // FND/kWh je km Luftlinie
	NetzWallet string  `json:"netz_wallet"` // Empfänger des Netzentgelts (leer = nur vormerken)
}

func envZahl(name string, standard float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64); err == nil && v >= 0 {
		return v
	}
	return standard
}

func marktParameter() marktParam {
	t := strings.ToLower(strings.TrimSpace(os.Getenv("FUNDUS_MARKT_TEILNAHME")))
	return marktParam{
		Teilnahme:  t == "1" || t == "ja" || t == "true",
		Mindest:    envZahl("FUNDUS_MARKT_MINDESTPREIS", 0.02),
		Hoechst:    envZahl("FUNDUS_MARKT_HOECHSTPREIS", 0.30),
		MaxKm:      envZahl("FUNDUS_MARKT_MAX_KM", 10),
		NetzGrund:  envZahl("FUNDUS_NETZ_GRUND", 0.01),
		NetzJeKm:   envZahl("FUNDUS_NETZ_JE_KM", 0.004),
		NetzWallet: strings.ToLower(strings.TrimSpace(os.Getenv("FUNDUS_NETZENTGELT_WALLET"))),
	}
}

func netzentgelt(p marktParam, km float64) float64 { return p.NetzGrund + p.NetzJeKm*km }

// haversineKm: Luftlinie zwischen zwei Punkten auf der Erde.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0088
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

// ── Reine Rechnung (in Python an Szenarien geprüft) ──────────────────────────

type marktTeilnehmer struct {
	Zaehler string
	Lat     float64
	Lon     float64
	Mindest float64
	Hoechst float64
	MaxKm   float64
	Wallet  string
	KWh     float64 // Einspeisung (Erzeuger) bzw. Bezug (Verbraucher) der Viertelstunde
}

type marktPosten struct {
	An     string  `json:"an"`
	KWh    float64 `json:"kwh"`
	Preis  float64 `json:"preis"` // Energiepreis FND/kWh (für alle Abnehmer dieses Erzeugers gleich)
	Netz   float64 `json:"netz"`  // Netzentgelt FND/kWh
	Km     float64 `json:"km"`
	Wallet string  `json:"wallet_erzeuger"`
}

// marktZuteilen: Der Erzeuger verteilt seine Einspeisung.
func marktZuteilen(erz marktTeilnehmer, erzeuger, verbraucher []marktTeilnehmer, p marktParam) ([]marktPosten, float64, float64) {
	type kand struct {
		v    marktTeilnehmer
		km   float64
		oben float64
	}
	var ks []kand
	for _, v := range verbraucher {
		if v.KWh <= 0 || v.Zaehler == erz.Zaehler {
			continue
		}
		km := haversineKm(erz.Lat, erz.Lon, v.Lat, v.Lon)
		if km > math.Min(erz.MaxKm, v.MaxKm) {
			continue
		}
		oben := v.Hoechst - netzentgelt(p, km)
		if oben < erz.Mindest {
			continue
		}
		ks = append(ks, kand{v, km, oben})
	}
	if len(ks) == 0 || erz.KWh <= 0 {
		return nil, 0, 0
	}
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].km != ks[j].km {
			return ks[i].km < ks[j].km
		}
		return ks[i].v.Zaehler < ks[j].v.Zaehler
	})
	// Knappheit im Umkreis DIESES Erzeugers: alle Erzeuger darin gegen die
	// Nachfrage der Verbraucher, die er beliefern darf.
	var angebot, nachfrage float64
	for _, e := range erzeuger {
		if haversineKm(erz.Lat, erz.Lon, e.Lat, e.Lon) <= erz.MaxKm {
			angebot += e.KWh
		}
	}
	cmin := math.Inf(1)
	for _, k := range ks {
		nachfrage += k.v.KWh
		cmin = math.Min(cmin, k.oben)
	}
	knapp := 0.0
	if angebot > 0 {
		knapp = math.Min(1, nachfrage/angebot)
	}
	preis := erz.Mindest + (cmin-erz.Mindest)*knapp
	var out []marktPosten
	rest := erz.KWh
	for _, k := range ks {
		if rest <= 1e-9 {
			break
		}
		m := math.Min(rest, k.v.KWh)
		out = append(out, marktPosten{An: k.v.Zaehler, KWh: m, Preis: preis,
			Netz: netzentgelt(p, k.km), Km: k.km, Wallet: erz.Wallet})
		rest -= m
	}
	return out, knapp, preis
}

type marktAngenommen struct {
	marktPosten
	Von string `json:"von"` // Zähler des Erzeugers
}

// marktAnnehmen: Der Verbraucher nimmt die günstigsten an, bis sein Bezug gedeckt ist.
func marktAnnehmen(bezugKWh float64, angebote []marktAngenommen) ([]marktAngenommen, float64) {
	sort.Slice(angebote, func(i, j int) bool {
		ki, kj := angebote[i].Preis+angebote[i].Netz, angebote[j].Preis+angebote[j].Netz
		if ki != kj {
			return ki < kj
		}
		if angebote[i].Km != angebote[j].Km {
			return angebote[i].Km < angebote[j].Km
		}
		return angebote[i].Von < angebote[j].Von
	})
	var out []marktAngenommen
	rest := bezugKWh
	for _, a := range angebote {
		if rest <= 1e-9 {
			break
		}
		m := math.Min(rest, a.KWh)
		a.KWh = m
		out = append(out, a)
		rest -= m
	}
	return out, math.Max(0, rest)
}

// ── Ablauf ───────────────────────────────────────────────────────────────────

const (
	marktZuteilenNach = 2 * time.Minute // Erzeuger teilt zu: 2 min nach Ende der Viertelstunde
	marktAnnehmenNach = 5 * time.Minute // Verbraucher nimmt an: nach 5 min
	marktFenster      = 2 * time.Hour   // ältere Viertelstunden werden nicht mehr gehandelt
)

var marktMu sync.Mutex

// marktPosition: der Standort des Nodes (Einstellungen → Profil oder Karte;
// beide schreiben in denselben Standortspeicher). Zähler und Node stehen am
// selben Ort – es gibt nur EINE Position (R841).
func (s *Server) marktPosition() (float64, float64) {
	if s.locationStore != nil {
		if l := s.locationStore.get(); l.Lat != 0 || l.Lon != 0 {
			return l.Lat, l.Lon
		}
	}
	if s.cfg.NodeLat != 0 || s.cfg.NodeLon != 0 {
		return s.cfg.NodeLat, s.cfg.NodeLon
	}
	return s.cfg.MeterLat, s.cfg.MeterLon
}

func (s *Server) marktVeroeffentlichen(rec *storage.Record) {
	rec.OwnerID, rec.CreatedAt = s.nodeID(), time.Now()
	if err := s.store.Put(rec); err != nil || s.node == nil {
		return
	}
	if data, err := marshalRecord(rec); err == nil {
		_ = s.node.Publish(context.Background(), p2p.TopicEnergy, data)
	}
}

// eigeneViertel: gültige, handelbare Viertelstunden dieses Nodes im Fenster.
func (s *Server) eigeneViertel() []viertelWert {
	alle, _ := s.store.List(storage.RecordViertelstunde)
	grenze := time.Now().Add(-marktFenster).Unix()
	var out []viertelWert
	for _, r := range alle {
		w, ok := viertelAusRecord(r)
		if !ok || w.Von < grenze || w.Luecke || w.Unvollstaendig {
			continue
		}
		if sig, menge, gr := pruefeViertel(w); !sig || !menge || !gr {
			continue
		}
		out = append(out, w)
	}
	return out
}

func (s *Server) marktSchleife() {
	defer func() {
		if r := recover(); r != nil && s.log != nil {
			s.log.Error("Nachbarschaftshandel abgebrochen", zap.Any("grund", r))
		}
	}()
	time.Sleep(45 * time.Second)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.marktLauf()
		<-t.C
	}
}

func (s *Server) marktLauf() {
	p := marktParameter()
	if !p.Teilnahme {
		return
	}
	marktMu.Lock()
	defer marktMu.Unlock()
	_, wallet, err := s.zaehlerWallet()
	if err != nil {
		s.zaehlerWarnen("markt-wallet", "Nachbarschaftshandel: keine Zähler-Wallet", zap.Error(err))
		return
	}
	lat, lon := s.marktPosition()
	if lat == 0 && lon == 0 {
		s.zaehlerWarnen("markt-position", "Nachbarschaftshandel: Standort fehlt (Einstellungen → Karte)")
		return
	}
	jetzt := time.Now()
	eigene := s.eigeneViertel()

	// 1. Angebot und Viertelstundenwerte veröffentlichen.
	zaehler := map[string]string{} // Zählernummer → Gerät
	for _, w := range eigene {
		zaehler[w.Zaehler] = w.Geraet
		id := w.Zaehler + "/" + strconv.FormatInt(w.Von, 10)
		if r, _ := s.store.Get(storage.RecordMarktViertel, id); r == nil {
			if roh, err := jsonRoh(w); err == nil {
				s.marktVeroeffentlichen(&storage.Record{ID: id, Type: storage.RecordMarktViertel,
					Data: map[string]any{"roh": roh, "zaehler": w.Zaehler, "von": w.Von}})
			}
		}
	}
	for nr, geraet := range zaehler {
		alt, _ := s.store.Get(storage.RecordMarktAngebot, nr)
		if alt != nil && alt.Data != nil && time.Since(alt.UpdatedAt) < 30*time.Minute &&
			fmt.Sprint(alt.Data["mindest"]) == fmt.Sprint(p.Mindest) && fmt.Sprint(alt.Data["hoechst"]) == fmt.Sprint(p.Hoechst) &&
			fmt.Sprint(alt.Data["peer"]) == s.nodeID() && fmt.Sprint(alt.Data["lat"]) == fmt.Sprint(lat) {
			continue
		}
		s.marktVeroeffentlichen(&storage.Record{ID: nr, Type: storage.RecordMarktAngebot, Data: map[string]any{
			// peer (R840): Nur dieser Node darf im Sekundenhandel für den Zähler sprechen.
			"zaehler": nr, "geraet": geraet, "wallet": strings.ToLower(wallet.Hex()), "peer": s.nodeID(),
			"lat": lat, "lon": lon, "mindest": p.Mindest, "hoechst": p.Hoechst, "max_km": p.MaxKm,
		}})
	}

	// Teilnehmer und ihre Werte je Viertelstunde (aus dem Netz, geprüft).
	werte := s.marktFremdeViertel()

	for _, w := range eigene {
		ende := time.Unix(w.Bis, 0)
		ich := marktTeilnehmer{Zaehler: w.Zaehler, Lat: lat, Lon: lon, Mindest: p.Mindest,
			Hoechst: p.Hoechst, MaxKm: p.MaxKm, Wallet: strings.ToLower(wallet.Hex())}
		// 2. Erzeuger: Einspeisung zuteilen.
		if e := wsZuKWh(w.EinspeisungWs); e > 0 && jetzt.After(ende.Add(marktZuteilenNach)) {
			zid := w.Zaehler + "/" + strconv.FormatInt(w.Von, 10)
			if r, _ := s.store.Get(storage.RecordMarktZuteilung, zid); r == nil {
				ich.KWh = e
				// Der eigene veröffentlichte Wert kommt aus dem Netz zurück –
				// herausfiltern, sonst zählte das eigene Angebot doppelt.
				var erz, verb []marktTeilnehmer
				for _, t := range werte.fuer(w.Von, true) {
					if t.Zaehler != w.Zaehler {
						erz = append(erz, t)
					}
				}
				for _, t := range werte.fuer(w.Von, false) {
					if t.Zaehler != w.Zaehler {
						verb = append(verb, t)
					}
				}
				erz = append(erz, ich)
				// R840: Sekundensummen haben Vorrang – gehandelt wurde jede Sekunde,
				// hier wird nur dokumentiert (gedeckelt auf die unterschriebene
				// Einspeisung). Ohne Sekundendaten die Rechnung je Viertelstunde.
				daten := map[string]any{"erzeuger": w.Zaehler, "von": w.Von, "einspeisung_kwh": e}
				posten, preis := marktSekundenPosten(w.Von, e, p)
				if posten != nil {
					daten["sekundlich"] = true
				} else {
					var knapp float64
					posten, knapp, preis = marktZuteilen(ich, erz, verb, p)
					daten["knappheit"] = knapp
				}
				daten["preis"], daten["posten"] = preis, posten
				s.marktVeroeffentlichen(&storage.Record{ID: zid, Type: storage.RecordMarktZuteilung, Data: daten})
				if s.log != nil && len(posten) > 0 {
					s.log.Info("Nachbarschaftshandel: zugeteilt", zap.String("zaehler", w.Zaehler),
						zap.Int("abnehmer", len(posten)), zap.Float64("preis", preis))
				}
			}
		}
		// 3. Verbraucher: annehmen und bezahlen.
		if b := wsZuKWh(w.BezugWs); b > 0 && jetzt.After(ende.Add(marktAnnehmenNach)) {
			s.marktAnnehmenUndZahlen(w, b, p)
		}
	}
}

// ── Werte anderer Teilnehmer ─────────────────────────────────────────────────

type marktWerte map[int64][]marktWertEintrag

type marktWertEintrag struct {
	t        marktTeilnehmer
	erzeuger bool
}

func (m marktWerte) fuer(von int64, erzeuger bool) []marktTeilnehmer {
	var out []marktTeilnehmer
	for _, e := range m[von] {
		if e.erzeuger == erzeuger {
			out = append(out, e.t)
		}
	}
	return out
}

// marktFremdeViertel liest veröffentlichte Viertelstunden aller Teilnehmer und
// behält nur die prüfbaren: Unterschriften gültig, Gerät wie im Angebot.
func (s *Server) marktFremdeViertel() marktWerte {
	out := marktWerte{}
	alle, _ := s.store.List(storage.RecordMarktViertel)
	grenze := time.Now().Add(-marktFenster).Unix()
	angebote := map[string]*storage.Record{}
	for _, r := range alle {
		if r == nil || r.DeletedAt != nil || r.Data == nil {
			continue
		}
		roh, _ := r.Data["roh"].(string)
		var w viertelWert
		if err := jsonAus(roh, &w); err != nil || w.Von < grenze || w.Luecke || w.Unvollstaendig {
			continue
		}
		if sig, menge, gr := pruefeViertel(w); !sig || !menge || !gr {
			continue
		}
		a, ok := angebote[w.Zaehler]
		if !ok {
			a, _ = s.store.Get(storage.RecordMarktAngebot, w.Zaehler)
			angebote[w.Zaehler] = a
		}
		if a == nil || a.Data == nil {
			continue
		}
		if g, _ := a.Data["geraet"].(string); g != w.Geraet {
			continue // Viertelstunde stammt nicht vom Gerät des Angebots
		}
		zahl := func(k string) float64 { f, _ := toFloatOK(a.Data[k]); return f }
		t := marktTeilnehmer{Zaehler: w.Zaehler, Lat: zahl("lat"), Lon: zahl("lon"),
			Mindest: zahl("mindest"), Hoechst: zahl("hoechst"), MaxKm: zahl("max_km")}
		t.Wallet, _ = a.Data["wallet"].(string)
		if e := wsZuKWh(w.EinspeisungWs); e > 0 {
			et := t
			et.KWh = e
			out[w.Von] = append(out[w.Von], marktWertEintrag{et, true})
		}
		if b := wsZuKWh(w.BezugWs); b > 0 {
			bt := t
			bt.KWh = b
			out[w.Von] = append(out[w.Von], marktWertEintrag{bt, false})
		}
	}
	return out
}

// ── Annehmen und bezahlen ────────────────────────────────────────────────────

func fndZuU(fnd float64) *big.Int {
	u, _ := new(big.Float).Mul(big.NewFloat(fnd), big.NewFloat(float64(chain.UFNDPerFND))).Int(nil)
	return u
}

func (s *Server) marktAnnehmenUndZahlen(w viertelWert, bezugKWh float64, p marktParam) {
	zid := w.Zaehler + "/" + strconv.FormatInt(w.Von, 10)
	if r, _ := s.store.Get(storage.RecordMarktZahlung, zid); r != nil {
		return // schon erledigt
	}
	var angebote []marktAngenommen
	alle, _ := s.store.List(storage.RecordMarktZuteilung)
	for _, r := range alle {
		if r == nil || r.DeletedAt != nil || r.Data == nil {
			continue
		}
		if von, _ := toFloatOK(r.Data["von"]); int64(von) != w.Von {
			continue
		}
		erz, _ := r.Data["erzeuger"].(string)
		// Die Wallet des Erzeugers kommt aus SEINEM Angebot – nicht aus der
		// Zuteilung, die jeder Node hätte schreiben können.
		ang, _ := s.store.Get(storage.RecordMarktAngebot, erz)
		if ang == nil || ang.Data == nil {
			continue
		}
		erzWallet, _ := ang.Data["wallet"].(string)
		posten, _ := r.Data["posten"].([]any)
		for _, x := range posten {
			m, _ := x.(map[string]any)
			if m == nil {
				continue
			}
			if an, _ := m["an"].(string); an != w.Zaehler {
				continue
			}
			f := func(k string) float64 { v, _ := toFloatOK(m[k]); return v }
			a := marktAngenommen{Von: erz, marktPosten: marktPosten{An: w.Zaehler, KWh: f("kwh"),
				Preis: f("preis"), Netz: f("netz"), Km: f("km"), Wallet: erzWallet}}
			// Eigene Grenzen prüfen: nicht teurer als meine Obergrenze, nicht weiter
			// als meine Reichweite, Netzentgelt nach MEINER Rechnung.
			if a.Km > p.MaxKm || a.Preis+netzentgelt(p, a.Km) > p.Hoechst+1e-9 || a.KWh <= 0 {
				continue
			}
			a.Netz = netzentgelt(p, a.Km)
			angebote = append(angebote, a)
		}
	}
	angenommen, tarif := marktAnnehmen(bezugKWh, angebote)

	key, adr, err := s.zaehlerWallet()
	if err != nil {
		return
	}
	guthabenText, _ := s.chain.AccountInfo(adr)
	guthaben, ok := new(big.Int).SetString(strings.TrimSpace(guthabenText), 10)
	if !ok {
		guthaben = new(big.Int)
	}
	var gezahlt []map[string]any
	var summeEnergie, summeNetz float64
	for _, a := range angenommen {
		energie, netz := a.KWh*a.Preis, a.KWh*a.Netz
		uE, uN := fndZuU(energie), fndZuU(netz)
		bedarf := new(big.Int).Add(uE, uN)
		bedarf.Add(bedarf, chain.FeeForValue(uE))
		if p.NetzWallet != "" {
			bedarf.Add(bedarf, chain.FeeForValue(uN))
		}
		if guthaben.Cmp(bedarf) < 0 {
			break // Budget erschöpft: der Rest läuft über den Tarif
		}
		eintrag := map[string]any{"von": a.Von, "kwh": a.KWh, "preis": a.Preis, "netz": a.Netz, "km": a.Km}
		if uE.Sign() > 0 {
			if err := s.zaehlerUeberweisen(key, adr, a.Wallet, uE); err != nil {
				eintrag["fehler"] = err.Error()
				gezahlt = append(gezahlt, eintrag)
				continue
			}
		}
		if p.NetzWallet != "" && uN.Sign() > 0 {
			if err := s.zaehlerUeberweisen(key, adr, p.NetzWallet, uN); err != nil {
				eintrag["netz_fehler"] = err.Error()
			} else {
				eintrag["netz_ueberwiesen"] = true
			}
		}
		guthaben = new(big.Int).Sub(guthaben, bedarf)
		summeEnergie += energie
		summeNetz += netz
		gezahlt = append(gezahlt, eintrag)
	}
	var p2p float64
	for _, g := range gezahlt {
		if g["fehler"] == nil {
			kwh, _ := g["kwh"].(float64)
			p2p += kwh
		}
	}
	_ = s.store.Put(&storage.Record{ID: zid, Type: storage.RecordMarktZahlung, Data: map[string]any{
		"zaehler": w.Zaehler, "von": w.Von, "bezug_kwh": bezugKWh, "p2p_kwh": p2p,
		"tarif_kwh": bezugKWh - p2p, "energie_fnd": summeEnergie, "netz_fnd": summeNetz,
		"posten": gezahlt, "ohne_budget": tarif,
	}})
	if s.log != nil && len(gezahlt) > 0 {
		s.log.Info("Nachbarschaftshandel: bezahlt", zap.String("zaehler", w.Zaehler),
			zap.Float64("p2p_kwh", p2p), zap.Float64("energie_fnd", summeEnergie), zap.Float64("netz_fnd", summeNetz))
	}
}

// zaehlerUeberweisen: Überweisung aus der Zähler-Wallet, mit derselben
// Nonce-Zählung wie zaehlerEinreichen.
func (s *Server) zaehlerUeberweisen(k *ecdsa.PrivateKey, adr chain.Address, an string, betrag *big.Int) error {
	ziel, ok := chain.AddressFromHex(an)
	if !ok {
		return fmt.Errorf("Empfänger ungültig: %s", an)
	}
	_, nonce := s.chain.AccountInfo(adr)
	for _, tx := range s.mempool.Pending() {
		if tx != nil && tx.From == adr {
			nonce++
		}
	}
	tx, err := chain.BuildSignedTransfer(k, ziel, betrag, nonce)
	if err != nil {
		return err
	}
	if err := s.mempool.Add(tx); err != nil {
		return err
	}
	s.broadcastTx(context.Background(), tx)
	return nil
}

func jsonRoh(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func jsonAus(roh string, v any) error { return json.Unmarshal([]byte(roh), v) }

// ── Anzeige ──────────────────────────────────────────────────────────────────

// GET /api/v1/markt/status
func (s *Server) marktStatus(c *gin.Context) {
	p := marktParameter()
	lat, lon := s.marktPosition()
	_, wallet, _ := s.zaehlerWallet()
	var guthaben float64
	if wallet != (chain.Address{}) && s.chain != nil {
		gText, _ := s.chain.AccountInfo(wallet)
		if g, ok := new(big.Int).SetString(strings.TrimSpace(gText), 10); ok {
			guthaben = uToFNDFloat(g)
		}
	}
	zahlungen, _ := s.store.List(storage.RecordMarktZahlung)
	zuteilungen, _ := s.store.List(storage.RecordMarktZuteilung)
	eigene := map[string]bool{}
	for _, w := range s.eigeneViertel() {
		eigene[w.Zaehler] = true
	}
	var gekauft, verkauft []gin.H
	for _, r := range zahlungen {
		if r != nil && r.Data != nil {
			gekauft = append(gekauft, gin.H(r.Data))
		}
	}
	for _, r := range zuteilungen {
		if r == nil || r.Data == nil {
			continue
		}
		if e, _ := r.Data["erzeuger"].(string); eigene[e] {
			verkauft = append(verkauft, gin.H(r.Data))
		}
	}
	neu := func(l []gin.H) {
		sort.Slice(l, func(i, j int) bool {
			a, _ := toFloatOK(l[i]["von"])
			b, _ := toFloatOK(l[j]["von"])
			return a > b
		})
	}
	neu(gekauft)
	neu(verkauft)
	if len(gekauft) > 16 {
		gekauft = gekauft[:16]
	}
	if len(verkauft) > 16 {
		verkauft = verkauft[:16]
	}
	c.JSON(http.StatusOK, gin.H{"parameter": p, "lat": lat, "lon": lon,
		"wallet": strings.ToLower(wallet.Hex()), "guthaben_fnd": guthaben,
		"gekauft": gekauft, "verkauft": verkauft})
}

func (s *Server) registerMarkt(r *gin.Engine) {
	r.GET("/api/v1/markt/status", s.marktStatus)
	r.GET("/api/v1/markt/live", s.marktLive) // R840: Preissignal, z.B. für Home Assistant
	s.registerMarktSekunde()
	go s.marktSchleife()
	go s.marktSekundeSchleife()
}
