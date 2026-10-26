package api

// Abrechnung des Nachbarschaftshandels (R844).
//
// ── Annehmen, gebündelt zahlen, abgleichen ─────────────────────────────────
// 1. ANNEHMEN (je Viertelstunde, beim Verbraucher): die günstigsten
//    Zuteilungen bis zum Bezug – und nur so viel, wie das Budget nach allen
//    noch offenen Posten trägt. Die Annahme wird festgehalten (offene Posten)
//    und VERÖFFENTLICHT, damit der Erzeuger weiß, was ihm zusteht.
// 2. ZAHLEN (gebündelt): Offene Posten je Empfänger werden gesammelt und in
//    EINER Überweisung bezahlt – sobald der älteste eine Stunde alt ist oder
//    die Summe 1 FND erreicht. Dokumentiert bleibt jede Viertelstunde.
// 3. ABGLEICHEN (beim Erzeuger): Was angenommen wurde, gegen das, was laut
//    Transaktionsindex (R823) wirklich ankam. Wer Posten älter als zwei Stunden
//    nicht bezahlt hat, bekommt keine Zuteilungen mehr, bis er nachzahlt.
//
// ── Keine Doppelzahlung, auch nach einem Absturz ───────────────────────────
// Eine Nonce gilt auf der Chain genau einmal. Deshalb wird jede Überweisung
// fertig unterschrieben GESPEICHERT und die Posten als bezahlt markiert, BEVOR
// sie gesendet wird. Nach einem Absturz wird genau diese Transaktion erneut
// gesendet – nie eine neue. War sie schon aufgenommen, lehnt die Chain die
// Wiederholung ab. Die Chain selbst verhindert so die Doppelzahlung.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/storage"
	"go.uber.org/zap"
)

// zaehlerTxMu: EINE Nonce-Vergabe für alle Transaktionen der Zähler-Wallet
// (Zertifizierung und Zahlung). Vorher zählten beide unabhängig – liefen sie
// gleichzeitig, bekamen zwei Transaktionen dieselbe Nonce.
var zaehlerTxMu sync.Mutex

const (
	marktSaeumigNach = 2 * time.Hour
	marktMindestFND  = 1.0
)

func marktZahlIntervall() time.Duration {
	if m, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("FUNDUS_MARKT_ZAHLINTERVALL_MIN")), 64); err == nil && m >= 5 {
		return time.Duration(m * float64(time.Minute))
	}
	return time.Hour
}

func uAusText(v any) *big.Int {
	s, _ := v.(string)
	n, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok {
		return new(big.Int)
	}
	return n
}

// ── 1. Annehmen ─────────────────────────────────────────────────────────────

// marktOffeneSchulden: Summe aller noch nicht bezahlten Posten (inkl. Gebühr),
// damit eine neue Annahme das Budget nicht überzieht.
func (s *Server) marktOffeneSchulden(p marktParam) *big.Int {
	summe := new(big.Int)
	alle, _ := s.store.List(storage.RecordMarktZahlung)
	for _, r := range alle {
		if r == nil || r.Data == nil {
			continue
		}
		posten, _ := r.Data["posten"].([]any)
		for _, x := range posten {
			m, _ := x.(map[string]any)
			if m == nil {
				continue
			}
			if st, _ := m["status"].(string); st == "offen" {
				e := uAusText(m["energie_u"])
				summe.Add(summe, e).Add(summe, chain.FeeForValue(e))
				if p.NetzWallet != "" {
					n := uAusText(m["netz_u"])
					summe.Add(summe, n).Add(summe, chain.FeeForValue(n))
				}
			}
		}
	}
	return summe
}

// marktAnnehmenUndZahlen (R844): nimmt an und hält fest – bezahlt wird
// gebündelt in marktZahlungen.
func (s *Server) marktAnnehmenUndZahlen(w viertelWert, bezugKWh float64, p marktParam) {
	zid := w.Zaehler + "/" + strconv.FormatInt(w.Von, 10)
	if r, _ := s.store.Get(storage.RecordMarktZahlung, zid); r != nil {
		return // schon angenommen
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
		// Wallet aus dem ANGEBOT des Erzeugers – nicht aus der Zuteilung,
		// die jeder Node hätte schreiben können.
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
				Preis: f("preis"), Km: f("km"), Wallet: erzWallet}}
			if a.Km > p.MaxKm || a.Preis+netzentgelt(p, a.Km) > p.Hoechst+1e-9 || a.KWh <= 0 {
				continue
			}
			a.Netz = netzentgelt(p, a.Km)
			angebote = append(angebote, a)
		}
	}
	angenommen, _ := marktAnnehmen(bezugKWh, angebote)

	// Budget: Guthaben minus alles, was schon angenommen, aber noch offen ist.
	_, adr, err := s.zaehlerWallet()
	if err != nil {
		return
	}
	gText, _ := s.chain.AccountInfo(adr)
	frei, ok := new(big.Int).SetString(strings.TrimSpace(gText), 10)
	if !ok {
		frei = new(big.Int)
	}
	frei.Sub(frei, s.marktOffeneSchulden(p))

	var posten []map[string]any
	var p2p, summeE, summeN float64
	for i, a := range angenommen {
		uE, uN := fndZuU(a.KWh*a.Preis), fndZuU(a.KWh*a.Netz)
		bedarf := new(big.Int).Add(uE, chain.FeeForValue(uE))
		if p.NetzWallet != "" {
			bedarf.Add(bedarf, uN).Add(bedarf, chain.FeeForValue(uN))
		}
		if frei.Cmp(bedarf) < 0 {
			break // Budget erschöpft: der Rest läuft über den Tarif
		}
		frei.Sub(frei, bedarf)
		netzStatus := "offen"
		if p.NetzWallet == "" {
			netzStatus = "vorgemerkt" // ohne Empfänger nur berechnet
		}
		posten = append(posten, map[string]any{
			"nr": i, "von": a.Von, "wallet": strings.ToLower(a.Wallet), "kwh": a.KWh, "preis": a.Preis,
			"netz": a.Netz, "km": a.Km, "energie_u": uE.String(), "netz_u": uN.String(),
			"status": "offen", "netz_status": netzStatus,
		})
		p2p += a.KWh
		summeE += a.KWh * a.Preis
		summeN += a.KWh * a.Netz
	}
	daten := map[string]any{
		"zaehler": w.Zaehler, "von": w.Von, "bezug_kwh": bezugKWh, "p2p_kwh": p2p,
		"tarif_kwh": bezugKWh - p2p, "energie_fnd": summeE, "netz_fnd": summeN,
		"posten": posten, "angenommen_um": time.Now().Unix(),
	}
	// ZUERST festhalten – erst danach kann je gezahlt werden.
	if err := s.store.Put(&storage.Record{ID: zid, Type: storage.RecordMarktZahlung, Data: daten}); err != nil {
		return
	}
	if len(posten) == 0 {
		return
	}
	// Veröffentlichen: Der Erzeuger gleicht damit ab, was ihm zusteht.
	var oeffentlich []map[string]any
	for _, x := range posten {
		oeffentlich = append(oeffentlich, map[string]any{"von": x["von"], "kwh": x["kwh"],
			"preis": x["preis"], "energie_u": x["energie_u"]})
	}
	s.marktVeroeffentlichen(&storage.Record{ID: zid, Type: storage.RecordMarktAnnahme, Data: map[string]any{
		"verbraucher": w.Zaehler, "wallet": strings.ToLower(adr.Hex()), "von": w.Von, "posten": oeffentlich,
	}})
	if s.log != nil {
		s.log.Info("Nachbarschaftshandel: angenommen", zap.String("zaehler", w.Zaehler),
			zap.Float64("p2p_kwh", p2p), zap.Float64("energie_fnd", summeE))
	}
}

// ── 2. Gebündelt zahlen ─────────────────────────────────────────────────────

type marktPostenRef struct {
	Annahme string // ID der Annahme (RecordMarktZahlung)
	Nr      int
	Netz    bool // Netzentgelt statt Energie
}

func (s *Server) marktZahlungen(p marktParam) {
	key, adr, err := s.zaehlerWallet()
	if err != nil {
		return
	}
	s.marktUeberweisungenPruefen()

	// Offene Posten je Empfänger sammeln.
	type gruppe struct {
		summe  *big.Int
		refs   []marktPostenRef
		aeltes int64
	}
	gruppen := map[string]*gruppe{}
	dazu := func(an string, u *big.Int, ref marktPostenRef, wann int64) {
		if u.Sign() <= 0 || an == "" {
			return
		}
		g := gruppen[an]
		if g == nil {
			g = &gruppe{summe: new(big.Int), aeltes: wann}
			gruppen[an] = g
		}
		g.summe.Add(g.summe, u)
		g.refs = append(g.refs, ref)
		if wann < g.aeltes {
			g.aeltes = wann
		}
	}
	annahmen, _ := s.store.List(storage.RecordMarktZahlung)
	for _, r := range annahmen {
		if r == nil || r.Data == nil {
			continue
		}
		wann, _ := toFloatOK(r.Data["angenommen_um"])
		posten, _ := r.Data["posten"].([]any)
		for _, x := range posten {
			m, _ := x.(map[string]any)
			if m == nil {
				continue
			}
			nr, _ := toFloatOK(m["nr"])
			if st, _ := m["status"].(string); st == "offen" {
				an, _ := m["wallet"].(string)
				dazu(an, uAusText(m["energie_u"]), marktPostenRef{r.ID, int(nr), false}, int64(wann))
			}
			if st, _ := m["netz_status"].(string); st == "offen" && p.NetzWallet != "" {
				dazu(p.NetzWallet, uAusText(m["netz_u"]), marktPostenRef{r.ID, int(nr), true}, int64(wann))
			}
		}
	}
	mindest := fndZuU(marktMindestFND)
	faellig := time.Now().Add(-marktZahlIntervall()).Unix()
	var empfaenger []string
	for an := range gruppen {
		empfaenger = append(empfaenger, an)
	}
	sort.Strings(empfaenger)
	for _, an := range empfaenger {
		g := gruppen[an]
		if g.aeltes > faellig && g.summe.Cmp(mindest) < 0 {
			continue // noch nicht fällig
		}
		if err := s.marktUeberweisen(key, adr, an, g.summe, g.refs); err != nil && s.log != nil {
			s.log.Warn("Nachbarschaftshandel: Zahlung nicht gesendet", zap.String("an", an), zap.Error(err))
		}
	}
}

// marktPostenSetzen setzt den Status der genannten Posten (in ihren Annahmen).
func (s *Server) marktPostenSetzen(refs []marktPostenRef, status string) {
	jeAnnahme := map[string][]marktPostenRef{}
	for _, r := range refs {
		jeAnnahme[r.Annahme] = append(jeAnnahme[r.Annahme], r)
	}
	for id, rs := range jeAnnahme {
		rec, _ := s.store.Get(storage.RecordMarktZahlung, id)
		if rec == nil || rec.Data == nil {
			continue
		}
		posten, _ := rec.Data["posten"].([]any)
		for _, x := range posten {
			m, _ := x.(map[string]any)
			if m == nil {
				continue
			}
			nr, _ := toFloatOK(m["nr"])
			for _, r := range rs {
				if int(nr) != r.Nr {
					continue
				}
				if r.Netz {
					m["netz_status"] = status
				} else {
					m["status"] = status
				}
			}
		}
		_ = s.store.Put(rec)
	}
}

// marktUeberweisen: bauen, SPEICHERN, Posten markieren – erst dann senden.
func (s *Server) marktUeberweisen(key *ecdsa.PrivateKey, adr chain.Address, an string, betrag *big.Int, refs []marktPostenRef) error {
	ziel, ok := chain.AddressFromHex(an)
	if !ok {
		return fmt.Errorf("Empfänger ungültig: %s", an)
	}
	zaehlerTxMu.Lock()
	defer zaehlerTxMu.Unlock()
	nonce := s.zaehlerNaechsteNonce(adr)
	tx, err := chain.BuildSignedTransfer(key, ziel, betrag, nonce)
	if err != nil {
		return err
	}
	roh, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	var refText []string
	for _, r := range refs {
		refText = append(refText, fmt.Sprintf("%s#%d#%t", r.Annahme, r.Nr, r.Netz))
	}
	id := fmt.Sprintf("%020d", nonce)
	// 1. Gespeichert, BEVOR irgendetwas das Haus verlässt.
	if err := s.store.Put(&storage.Record{ID: id, Type: storage.RecordMarktUeberweisung, Data: map[string]any{
		"an": an, "betrag_u": betrag.String(), "nonce": nonce, "tx": string(roh),
		"posten": refText, "status": "vorbereitet", "erstellt": time.Now().Unix(),
	}}); err != nil {
		return err
	}
	// 2. Posten als bezahlt markieren – ab jetzt werden sie nie neu bezahlt.
	s.marktPostenSetzen(refs, "bezahlt:"+id)
	// 3. Senden.
	if err := s.mempool.Add(tx); err != nil {
		// Nicht gesendet: Posten wieder freigeben, Überweisung verwerfen.
		s.marktPostenSetzen(refs, "offen")
		_ = s.store.HardDelete(storage.RecordMarktUeberweisung, id)
		return err
	}
	s.broadcastTx(context.Background(), tx)
	s.marktUeberweisungStatus(id, "gesendet")
	if s.log != nil {
		s.log.Info("Nachbarschaftshandel: gezahlt", zap.String("an", an),
			zap.Float64("fnd", uToFNDFloat(betrag)), zap.Int("posten", len(refs)), zap.Uint64("nonce", nonce))
	}
	return nil
}

// zaehlerNaechsteNonce: Kontostand-Nonce, eigene noch nicht aufgenommene
// Transaktionen UND gespeicherte, noch offene Überweisungen. Letztere sind
// nach einem Absturz womöglich nicht im Speicher für offene Transaktionen –
// ihre Nonce darf trotzdem nie ein zweites Mal vergeben werden.
// Nur unter zaehlerTxMu aufrufen.
func (s *Server) zaehlerNaechsteNonce(adr chain.Address) uint64 {
	_, nonce := s.chain.AccountInfo(adr)
	for _, tx := range s.mempool.Pending() {
		if tx != nil && tx.From == adr && tx.Nonce >= nonce {
			nonce = tx.Nonce + 1
		}
	}
	alle, _ := s.store.List(storage.RecordMarktUeberweisung)
	for _, r := range alle {
		if r == nil || r.Data == nil {
			continue
		}
		if st, _ := r.Data["status"].(string); st != "vorbereitet" && st != "gesendet" {
			continue
		}
		if n, _ := toFloatOK(r.Data["nonce"]); uint64(n) >= nonce {
			nonce = uint64(n) + 1
		}
	}
	return nonce
}

func (s *Server) marktUeberweisungStatus(id, status string) {
	rec, _ := s.store.Get(storage.RecordMarktUeberweisung, id)
	if rec == nil || rec.Data == nil {
		return
	}
	rec.Data["status"] = status
	rec.Data["stand"] = time.Now().Unix()
	_ = s.store.Put(rec)
}

// marktUeberweisungenPruefen: aufgenommen? Sonst – nach einem Absturz oder
// wenn sie verloren ging – GENAU DIESELBE Transaktion erneut senden.
func (s *Server) marktUeberweisungenPruefen() {
	_, adr, err := s.zaehlerWallet()
	if err != nil {
		return
	}
	_, kontoNonce := s.chain.AccountInfo(adr)
	alle, _ := s.store.List(storage.RecordMarktUeberweisung)
	for _, r := range alle {
		if r == nil || r.Data == nil {
			continue
		}
		st, _ := r.Data["status"].(string)
		if st != "vorbereitet" && st != "gesendet" {
			continue
		}
		n, _ := toFloatOK(r.Data["nonce"])
		if uint64(n) < kontoNonce {
			s.marktUeberweisungStatus(r.ID, "bestaetigt")
			continue
		}
		stand, _ := toFloatOK(r.Data["stand"])
		if st == "gesendet" && time.Since(time.Unix(int64(stand), 0)) < 5*time.Minute {
			continue // noch etwas Zeit lassen
		}
		roh, _ := r.Data["tx"].(string)
		var tx chain.Transaction
		if json.Unmarshal([]byte(roh), &tx) != nil {
			continue
		}
		if err := s.mempool.Add(&tx); err != nil && !s.marktImSpeicher(&tx) {
			// Endgültig gescheitert (etwa Guthaben bei Aufnahme nicht mehr da)?
			// Nach einer Stunde aufgeben: Posten wieder offen, Nonce wieder frei –
			// sonst hinge sie für immer offen, und KEINE weitere Transaktion
			// dieser Wallet würde mehr aufgenommen.
			erstellt, _ := toFloatOK(r.Data["erstellt"])
			if time.Since(time.Unix(int64(erstellt), 0)) > time.Hour && uint64(n) == kontoNonce {
				s.marktUeberweisungAufgeben(r, err)
			}
			continue
		}
		s.broadcastTx(context.Background(), &tx)
		s.marktUeberweisungStatus(r.ID, "gesendet")
		if s.log != nil {
			s.log.Info("Nachbarschaftshandel: Zahlung erneut gesendet", zap.String("nonce", r.ID))
		}
	}
}

func (s *Server) marktImSpeicher(tx *chain.Transaction) bool {
	for _, p := range s.mempool.Pending() {
		if p != nil && p.From == tx.From && p.Nonce == tx.Nonce {
			return true
		}
	}
	return false
}

// marktUeberweisungAufgeben gibt Posten und Nonce einer gescheiterten
// Überweisung frei. Die Posten werden beim nächsten Lauf neu gebündelt.
func (s *Server) marktUeberweisungAufgeben(r *storage.Record, grund error) {
	var refs []marktPostenRef
	liste, _ := r.Data["posten"].([]any)
	for _, x := range liste {
		t, _ := x.(string)
		teile := strings.Split(t, "#")
		if len(teile) != 3 {
			continue
		}
		nr, _ := strconv.Atoi(teile[1])
		refs = append(refs, marktPostenRef{Annahme: teile[0], Nr: nr, Netz: teile[2] == "true"})
	}
	s.marktPostenSetzen(refs, "offen")
	s.marktUeberweisungStatus(r.ID, "gescheitert")
	if s.log != nil {
		s.log.Warn("Nachbarschaftshandel: Zahlung aufgegeben, Posten wieder offen",
			zap.String("nonce", r.ID), zap.Error(grund))
	}
}

// ── 3. Abgleich beim Erzeuger ───────────────────────────────────────────────

type marktForderung struct {
	Verbraucher string  `json:"verbraucher"`
	Angenommen  float64 `json:"angenommen_fnd"`
	Bezahlt     float64 `json:"bezahlt_fnd"`
	Ueberfaellig float64 `json:"ueberfaellig_fnd"`
	Saeumig     bool    `json:"saeumig"`
}

var (
	mFordMu    sync.Mutex
	mFordStand time.Time
	mFord      []marktForderung
)

// marktForderungen: je Verbraucher, was er bei MIR angenommen und was er laut
// Transaktionsindex überwiesen hat. Höchstens einmal je Minute berechnet.
func (s *Server) marktForderungen() []marktForderung {
	mFordMu.Lock()
	defer mFordMu.Unlock()
	if time.Since(mFordStand) < time.Minute && mFord != nil {
		return mFord
	}
	eigene := map[string]bool{}
	for _, w := range s.eigeneViertel() {
		eigene[w.Zaehler] = true
	}
	_, wallet, err := s.zaehlerWallet()
	if err != nil || len(eigene) == 0 {
		mFord, mFordStand = []marktForderung{}, time.Now()
		return mFord
	}
	type summe struct {
		wallet      string
		angenommen  *big.Int
		ueberfaellig *big.Int
	}
	je := map[string]*summe{}
	grenze := time.Now().Add(-marktSaeumigNach).Unix()
	annahmen, _ := s.store.List(storage.RecordMarktAnnahme)
	for _, r := range annahmen {
		if r == nil || r.DeletedAt != nil || r.Data == nil {
			continue
		}
		v, _ := r.Data["verbraucher"].(string)
		vw, _ := r.Data["wallet"].(string)
		von, _ := toFloatOK(r.Data["von"])
		posten, _ := r.Data["posten"].([]any)
		for _, x := range posten {
			m, _ := x.(map[string]any)
			if m == nil {
				continue
			}
			if e, _ := m["von"].(string); !eigene[e] {
				continue
			}
			sm := je[v]
			if sm == nil {
				sm = &summe{wallet: strings.ToLower(vw), angenommen: new(big.Int), ueberfaellig: new(big.Int)}
				je[v] = sm
			}
			u := uAusText(m["energie_u"])
			sm.angenommen.Add(sm.angenommen, u)
			if int64(von) < grenze {
				sm.ueberfaellig.Add(sm.ueberfaellig, u)
			}
		}
	}
	// Eingänge aus dem Transaktionsindex: Überweisungen an MEINE Zähler-Wallet.
	bezahlt := map[string]*big.Int{}
	if eintraege, err := s.store.ListUnterPfad(storage.RecordTxIndex, strings.ToLower(wallet.Hex())); err == nil {
		for _, e := range eintraege {
			if e == nil || e.Data == nil {
				continue
			}
			if art, _ := e.Data["art"].(string); art != "ueberweisung" {
				continue
			}
			u := uAusText(e.Data["u"])
			if u.Sign() <= 0 {
				continue
			}
			g, _ := e.Data["gegen"].(string)
			g = strings.ToLower(g)
			if bezahlt[g] == nil {
				bezahlt[g] = new(big.Int)
			}
			bezahlt[g].Add(bezahlt[g], u)
		}
	}
	var out []marktForderung
	for v, sm := range je {
		b := bezahlt[sm.wallet]
		if b == nil {
			b = new(big.Int)
		}
		f := marktForderung{Verbraucher: v, Angenommen: uToFNDFloat(sm.angenommen), Bezahlt: uToFNDFloat(b)}
		if offen := new(big.Int).Sub(sm.ueberfaellig, b); offen.Sign() > 0 {
			f.Ueberfaellig, f.Saeumig = uToFNDFloat(offen), true
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Verbraucher < out[j].Verbraucher })
	if out == nil {
		out = []marktForderung{}
	}
	mFord, mFordStand = out, time.Now()
	return mFord
}

// marktSaeumige: Zählernummern, die keine Zuteilungen bekommen, bis sie nachzahlen.
func (s *Server) marktSaeumige() map[string]bool {
	out := map[string]bool{}
	for _, f := range s.marktForderungen() {
		if f.Saeumig {
			out[f.Verbraucher] = true
		}
	}
	return out
}
