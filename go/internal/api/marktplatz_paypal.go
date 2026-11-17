package api

// PayPal im Marktplatz (R922).
//
// Ein Angebot kann zusätzlich zur FND-Zahlung (Escrow) per PayPal bezahlt
// werden. 1 FND = 1 EUR. Das Geld geht DIREKT an den Verkäufer: Er trägt in
// seinem Angebot seine PayPal-Adresse (E-Mail oder Händlerkonto-ID) ein, und
// der Node erstellt die Zahlung mit genau diesem Empfänger („payee“). Die
// PayPal-Zugangsdaten (Client-ID/Secret) des Node-Betreibers dienen nur dazu,
// die Zahlung anzulegen und zu bestätigen – das Geld fließt nicht über ihn.
//
// Ablauf:
//  1. /api/v1/marktplatz/paypal/bestellung prüft die Positionen (Angebot da,
//     Bestand, ein Verkäufer je Zahlung), rechnet den Betrag selbst aus und
//     legt die PayPal-Bestellung an. Den Preis schickt der Browser NICHT mit.
//  2. Der Käufer bestätigt bei PayPal; die Lieferadresse kommt aus seinem
//     PayPal-Konto (bei Versand).
//  3. /abschluss reserviert den Bestand, bucht die Zahlung ab, prüft Betrag,
//     Währung und Kennung und legt je Position einen Kaufvermerk an – derselbe
//     Weg wie beim Escrow-Kauf: Der Verkäufer-Node zieht den Bestand ab und
//     meldet den Verkauf per Push, hier zusätzlich mit Lieferadresse.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

var (
	rePayPalMail     = regexp.MustCompile(`^[^@\s"<>]{1,64}@[a-z0-9.-]+\.[a-z]{2,}$`)
	rePayPalHaendler = regexp.MustCompile(`^[A-Z0-9]{13}$`)
)

// paypalEmpfaengerNorm: E-Mail (klein) oder Händlerkonto-ID (13 Zeichen, groß).
func paypalEmpfaengerNorm(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if strings.Contains(v, "@") {
		v = strings.ToLower(v)
		if len(v) > 127 || !rePayPalMail.MatchString(v) {
			return "", fmt.Errorf("PayPal-Empfänger: keine gültige E-Mail-Adresse")
		}
		return v, nil
	}
	v = strings.ToUpper(v)
	if !rePayPalHaendler.MatchString(v) {
		return "", fmt.Errorf("PayPal-Empfänger: E-Mail-Adresse oder Händlerkonto-ID (13 Zeichen) angeben")
	}
	return v, nil
}

// paypalFeldPruefen: das Feld „paypal“ eines Angebots vor dem Speichern.
func paypalFeldPruefen(body map[string]any) error {
	roh, da := body["paypal"]
	if !da {
		return nil
	}
	t, _ := roh.(string)
	n, err := paypalEmpfaengerNorm(t)
	if err != nil {
		return err
	}
	body["paypal"] = n
	return nil
}

func paypalPayee(e string) gin.H {
	if strings.Contains(e, "@") {
		return gin.H{"email_address": e}
	}
	return gin.H{"merchant_id": e}
}

// GET /api/v1/marktplatz/paypal – darf der Marktplatz PayPal anbieten?
func (s *Server) marktplatzPayPalInfo(c *gin.Context) {
	pp := s.hotspotPayPalLesen()
	bereit := pp.ClientID != "" && pp.Secret != ""
	h := gin.H{"bereit": bereit}
	if bereit {
		h["client_id"], h["modus"] = pp.ClientID, pp.Modus
	}
	c.JSON(http.StatusOK, h)
}

type mpPosition struct {
	ListingID string `json:"listing_id"`
	Anzahl    int    `json:"anzahl"`
	Versand   bool   `json:"versand"`
}

type mpPos struct {
	ListingID  string  `json:"listing_id"`
	Titel      string  `json:"titel"`
	Anzahl     int     `json:"anzahl"`
	Versand    bool    `json:"versand"`
	EinzelEUR  float64 `json:"einzel_eur"`
	VersandEUR float64 `json:"versand_eur"`
}

type mpOffeneBestellung struct {
	Betrag, Marke, Empfaenger string
	Pos                       []mpPos
	Angelegt                  time.Time
}

var (
	mpMu    sync.Mutex
	mpOffen = map[string]mpOffeneBestellung{}
)



// mpPositionenPruefen rechnet die Positionen aus den Angeboten selbst aus.
func (s *Server) mpPositionenPruefen(roh []mpPosition) ([]mpPos, string, error) {
	if len(roh) == 0 || len(roh) > 20 {
		return nil, "", fmt.Errorf("1 bis 20 Positionen je Zahlung")
	}
	if s.store == nil {
		return nil, "", fmt.Errorf("Marktplatz nicht bereit")
	}
	gesehen := map[string]bool{}
	var out []mpPos
	empf := ""
	for _, p := range roh {
		if p.ListingID == "" || gesehen[p.ListingID] {
			return nil, "", fmt.Errorf("Jedes Angebot nur einmal je Zahlung")
		}
		gesehen[p.ListingID] = true
		rec, err := s.store.Get(storage.RecordListing, p.ListingID)
		if err == nil && rec == nil {
			// Angebot liegt (noch) nicht auf diesem Node – bei den Nachbarn holen.
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			rec = s.listingHolen(ctx, p.ListingID)
			cancel()
		}
		if err != nil || rec == nil || rec.DeletedAt != nil || rec.Data == nil {
			return nil, "", fmt.Errorf("Ein Angebot gibt es nicht mehr")
		}
		d := rec.Data
		titel, _ := d["title"].(string)
		e, _ := d["paypal"].(string)
		e, err = paypalEmpfaengerNorm(e)
		if err != nil || e == "" {
			return nil, "", fmt.Errorf("„%s“ kann nicht per PayPal bezahlt werden", kiKuerzen(titel, 60))
		}
		if empf == "" {
			empf = e
		} else if empf != e {
			return nil, "", fmt.Errorf("Eine PayPal-Zahlung geht an einen Verkäufer – bitte getrennt bezahlen")
		}
		preis, _ := toFloatOK(d["price_min"])
		preis = rund2(preis)
		if preis <= 0 {
			return nil, "", fmt.Errorf("„%s“ hat keinen Preis", kiKuerzen(titel, 60))
		}
		n := p.Anzahl
		if n < 1 {
			n = 1
		}
		qty, sold := s.listingStock(p.ListingID, d)
		if v, _ := d["sold"].(bool); v || qty-sold < n {
			frei := qty - sold
			if frei < 0 {
				frei = 0
			}
			return nil, "", fmt.Errorf("„%s“: nur noch %d verfügbar", kiKuerzen(titel, 60), frei)
		}
		versand := p.Versand && fmt.Sprint(d["delivery"]) == "shipping"
		vk := 0.0
		if versand {
			vk, _ = toFloatOK(d["shipping_cost"])
			vk = rund2(math.Max(0, vk))
		}
		out = append(out, mpPos{ListingID: p.ListingID, Titel: titel, Anzahl: n, Versand: versand, EinzelEUR: preis, VersandEUR: vk})
	}
	return out, empf, nil
}

// POST /api/v1/marktplatz/paypal/bestellung {positionen:[{listing_id, anzahl, versand}]}
func (s *Server) marktplatzPayPalBestellung(c *gin.Context) {
	if !extRateAllow("mppaypal:"+c.ClientIP(), 10) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Versuche – bitte kurz warten."})
		return
	}
	var req struct {
		Positionen []mpPosition `json:"positionen"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	pp := s.hotspotPayPalLesen()
	if pp.ClientID == "" || pp.Secret == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PayPal ist auf diesem Node nicht eingerichtet."})
		return
	}
	pos, empf, err := s.mpPositionenPruefen(req.Positionen)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	eur := func(v float64) gin.H { return gin.H{"currency_code": "EUR", "value": betragText(v)} }
	var items []gin.H
	summe, versand, mitVersand := 0.0, 0.0, false
	for _, p := range pos {
		items = append(items, gin.H{"name": kiKuerzen(p.Titel, 120), "quantity": fmt.Sprint(p.Anzahl),
			"unit_amount": eur(p.EinzelEUR), "category": "PHYSICAL_GOODS", "sku": kiKuerzen(p.ListingID, 120)})
		summe += p.EinzelEUR * float64(p.Anzahl)
		versand += p.VersandEUR
		mitVersand = mitVersand || p.Versand
	}
	summe, versand = rund2(summe), rund2(versand)
	gesamt := betragText(summe + versand)
	z := make([]byte, 8)
	_, _ = rand.Read(z)
	marke := "mp|" + hex.EncodeToString(z)
	lieferung := "NO_SHIPPING"
	if mitVersand {
		lieferung = "GET_FROM_FILE" // Lieferadresse aus dem PayPal-Konto
	}
	d, code, err := s.paypalAnfrage(pp, "/v2/checkout/orders", gin.H{
		"intent": "CAPTURE",
		"payment_source": gin.H{"paypal": gin.H{"experience_context": gin.H{
			"shipping_preference": lieferung, "user_action": "PAY_NOW", "locale": "de-DE",
		}}},
		"purchase_units": []gin.H{{
			"reference_id": "marktplatz",
			"custom_id":    marke,
			"description":  kiKuerzen("Fundus-Marktplatz: "+pos[0].Titel, 120),
			"payee":        paypalPayee(empf),
			"amount":       gin.H{"currency_code": "EUR", "value": gesamt, "breakdown": gin.H{"item_total": eur(summe), "shipping": eur(versand)}},
			"items":        items,
		}},
	})
	id, _ := d["id"].(string)
	if err != nil || code/100 != 2 || id == "" {
		grund := "PayPal ist gerade nicht erreichbar."
		if err == nil {
			grund = "PayPal lehnt die Zahlung ab: " + paypalGrund(d, code)
		}
		if s.log != nil {
			s.log.Warn("Marktplatz-PayPal: Bestellung abgelehnt", zap.Int("status", code), zap.String("grund", grund))
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": grund})
		return
	}
	mpMu.Lock()
	for k, o := range mpOffen {
		if time.Since(o.Angelegt) > time.Hour {
			delete(mpOffen, k)
		}
	}
	mpOffen[id] = mpOffeneBestellung{Betrag: gesamt, Marke: marke, Empfaenger: empf, Pos: pos, Angelegt: time.Now()}
	mpMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"id": id, "betrag": gesamt})
}

// mpVerkauf: was der Verkäufer über einen PayPal-Kauf erfährt.
type mpVerkauf struct {
	Zeit       int64             `json:"zeit"`
	Bestellung string            `json:"bestellung"`
	Zahlung    string            `json:"zahlung,omitempty"`
	ListingID  string            `json:"listing_id"`
	Titel      string            `json:"titel"`
	Anzahl     int               `json:"anzahl"`
	BetragEUR  string            `json:"betrag_eur"`
	Versand    bool              `json:"versand"`
	Name       string            `json:"name,omitempty"`
	Adresse    map[string]string `json:"adresse,omitempty"`
	Email      string            `json:"email,omitempty"`
	Modus      string            `json:"modus"`
	Versendet  int64             `json:"versendet,omitempty"`
}

// POST /api/v1/marktplatz/paypal/abschluss {id}
func (s *Server) marktplatzPayPalAbschluss(c *gin.Context) {
	var req struct{ ID string }
	_ = c.ShouldBindJSON(&req)
	mpMu.Lock()
	o, ok := mpOffen[req.ID]
	if ok {
		delete(mpOffen, req.ID) // nur einmal abschließen
	}
	mpMu.Unlock()
	if !ok || !reBestellung.MatchString(req.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unbekannte Zahlung."})
		return
	}
	// Bestand reservieren, BEVOR abgebucht wird – ist inzwischen alles weg,
	// wird gar nicht erst gezahlt.
	var reserviert []mpPos
	freigeben := func() {
		for _, p := range reserviert {
			s.releaseListingStock(p.ListingID, p.Anzahl)
		}
	}
	for _, p := range o.Pos {
		okR, frei, err := s.reserveListingStock(p.ListingID, p.Anzahl)
		if err != nil || !okR {
			freigeben()
			if frei < 0 {
				frei = 0
			}
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("„%s“ ist inzwischen nur noch %d-mal verfügbar – es wurde nichts abgebucht.", kiKuerzen(p.Titel, 60), frei)})
			return
		}
		reserviert = append(reserviert, p)
	}
	pp := s.hotspotPayPalLesen()
	d, code, err := s.paypalAnfrage(pp, "/v2/checkout/orders/"+req.ID+"/capture", gin.H{})
	zahlung := ""
	var pu map[string]any
	if err == nil && code/100 == 2 && aeroText(d, "status") == "COMPLETED" {
		for _, x := range anyListe(d["purchase_units"]) {
			m := mapAus(x)
			for _, cp := range anyListe(mapAus(m["payments"])["captures"]) {
				cm := mapAus(cp)
				amt := mapAus(cm["amount"])
				if aeroText(cm, "status") == "COMPLETED" && aeroText(amt, "currency_code") == "EUR" &&
					aeroText(amt, "value") == o.Betrag && aeroText(cm, "custom_id") == o.Marke {
					zahlung, pu = aeroText(cm, "id"), m
				}
			}
		}
	}
	if zahlung == "" {
		freigeben()
		if s.log != nil {
			s.log.Warn("Marktplatz-PayPal: Zahlung nicht bestätigt", zap.String("bestellung", req.ID), zap.Int("status", code), zap.Error(err))
		}
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "Zahlung nicht bestätigt – bitte erneut versuchen. Ohne Bestätigung wird nichts abgebucht."})
		return
	}
	sp := mapAus(pu["shipping"])
	ad := mapAus(sp["address"])
	name := aeroText(mapAus(sp["name"]), "full_name")
	adresse := map[string]string{}
	for _, k := range []string{"address_line_1", "address_line_2", "postal_code", "admin_area_2", "admin_area_1", "country_code"} {
		if t := aeroText(ad, k); t != "" {
			adresse[k] = t
		}
	}
	payer := mapAus(d["payer"])
	if name == "" {
		pn := mapAus(payer["name"])
		name = strings.TrimSpace(aeroText(pn, "given_name") + " " + aeroText(pn, "surname"))
	}
	email := aeroText(payer, "email_address")
	for _, p := range o.Pos {
		sum := sha256.Sum256([]byte(zahlung + "|" + p.ListingID))
		tx := "pp" + hex.EncodeToString(sum[:])[:40]
		lieferung := "pickup"
		if p.Versand {
			lieferung = "shipping"
		}
		_ = s.store.Put(&storage.Record{
			ID: "purchase-" + tx, Type: storage.RecordPurchase, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			Data: map[string]any{"listing_id": p.ListingID, "quantity": p.Anzahl, "delivery": lieferung,
				"shipping_fnd": p.VersandEUR, "zahlung": "paypal"},
		})
		invalidatePurchaseIndex()
		s.settleListingStock(p.ListingID, p.Anzahl)
		v := mpVerkauf{Zeit: time.Now().Unix(), Bestellung: req.ID, Zahlung: zahlung, ListingID: p.ListingID, Titel: p.Titel,
			Anzahl: p.Anzahl, BetragEUR: betragText(p.EinzelEUR*float64(p.Anzahl) + p.VersandEUR), Versand: p.Versand, Email: email, Modus: pp.Modus}
		if p.Versand {
			v.Name, v.Adresse = name, adresse
		} else {
			v.Name = name
		}
		go s.mpVerkaufMelden(p.ListingID, tx, p.Anzahl, v)
	}
	if s.log != nil {
		s.log.Info("Marktplatz-PayPal: bezahlt", zap.String("bestellung", req.ID), zap.String("betrag", o.Betrag), zap.Int("positionen", len(o.Pos)))
	}
	ort := strings.TrimSpace(adresse["postal_code"] + " " + adresse["admin_area_2"])
	c.JSON(http.StatusOK, gin.H{"ok": true, "beleg": req.ID, "betrag": o.Betrag, "name": name, "ort": ort,
		"positionen": o.Pos, "sandbox": pp.Modus != "live"})
}

// mpVerkaufMelden: eigenes Angebot → hier vermerken; sonst an den Verkäufer-Node.
func (s *Server) mpVerkaufMelden(listingID, tx string, n int, v mpVerkauf) {
	defer func() { _ = recover() }()
	rec, err := s.store.Get(storage.RecordListing, listingID)
	if err != nil || rec == nil {
		return
	}
	if rec.OwnerID == "" || rec.OwnerID == s.nodeID() {
		s.applySale(listingID, tx, n)
		s.mpVerkaufVermerken(rec.Data, v)
		return
	}
	if s.node == nil {
		return
	}
	body, err := json.Marshal(listingSoldMsg{ListingID: listingID, Tx: tx, Quantity: n, PayPal: &v})
	if err != nil {
		return
	}
	for versuch := 0; versuch < 3; versuch++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err = s.node.SendAndReceive(ctx, rec.OwnerID, listingSoldProtocol, body)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(time.Duration(10*(versuch+1)) * time.Second)
	}
	if s.log != nil {
		s.log.Warn("Marktplatz-PayPal: Verkäufer-Node nicht erreichbar – Bestand folgt über den Abgleich, die Lieferadresse steht in seinem PayPal-Konto",
			zap.String("listing", listingID), zap.Error(err))
	}
}

var mpVerkaufMu sync.Mutex

// mpVerkaufVermerken: beim Verkäufer-Node – Liste in den Einstellungen und Push.
// Jede Bestellung je Angebot nur einmal.
func (s *Server) mpVerkaufVermerken(daten map[string]any, v mpVerkauf) {
	mpVerkaufMu.Lock()
	for _, alt := range s.mpVerkaeufe(0) {
		if alt.Bestellung == v.Bestellung && alt.ListingID == v.ListingID {
			mpVerkaufMu.Unlock()
			return
		}
	}
	if f, err := os.OpenFile(s.hotspotDatei("marktplatz-paypal.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		b, _ := json.Marshal(v)
		_, _ = f.Write(append(b, '\n'))
		f.Close()
	}
	mpVerkaufMu.Unlock()
	fid, _ := daten["creator_fid"].(string)
	if fid == "" {
		fid = s.eigentuemerFID()
	}
	if fid == "" {
		return
	}
	text := fmt.Sprintf("%d Stück · %s € per PayPal", v.Anzahl, strings.Replace(v.BetragEUR, ".", ",", 1))
	if v.Versand {
		text += " · Versand an " + v.Name + ", " + strings.TrimSpace(v.Adresse["postal_code"]+" "+v.Adresse["admin_area_2"])
	} else {
		text += " · Abholung (" + v.Name + ")"
	}
	if v.Modus != "live" {
		text += " (Sandbox)"
	}
	s.meldungVerteilen(fid, "Verkauft: "+kiKuerzen(v.Titel, 60), text, "/settings#paypal", "mp-pp-"+v.Bestellung+"-"+v.ListingID)
}

// mpVerkaeufe: die letzten n PayPal-Verkäufe eigener Angebote (n = 0: alle, älteste zuerst).
func (s *Server) mpVerkaeufe(n int) []mpVerkauf {
	var l []mpVerkauf
	if b, err := os.ReadFile(s.hotspotDatei("marktplatz-paypal.jsonl")); err == nil {
		for _, z := range strings.Split(string(b), "\n") {
			var v mpVerkauf
			if json.Unmarshal([]byte(z), &v) == nil && v.Bestellung != "" {
				l = append(l, v)
			}
		}
	}
	if n == 0 {
		return l
	}
	versand := map[string]int64{}
	if b, err := os.ReadFile(s.hotspotDatei("marktplatz-paypal-versand.json")); err == nil {
		_ = json.Unmarshal(b, &versand)
	}
	out := []mpVerkauf{}
	for i := len(l) - 1; i >= 0 && len(out) < n; i-- {
		l[i].Versendet = versand[l[i].Bestellung+"|"+l[i].ListingID]
		out = append(out, l[i])
	}
	return out
}

// ── Einstellungen: Lasche „PayPal“ ──────────────────────────────────────────

// GET /api/v1/admin/paypal
func (s *Server) paypalEinstellungen(c *gin.Context) {
	pp := s.hotspotPayPalLesen()
	c.JSON(http.StatusOK, gin.H{
		"paypal":    gin.H{"client_id": pp.ClientID, "secret_gesetzt": pp.Secret != "", "modus": pp.Modus},
		"verkaeufe": s.mpVerkaeufe(100),
	})
}

// POST /api/v1/admin/paypal {client_id, secret?, modus}
func (s *Server) paypalSpeichern(c *gin.Context) {
	var req hotspotPayPal
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	pp, err := s.paypalZugangSchreiben(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	pruefung := "ohne PayPal"
	if pp.ClientID != "" && pp.Secret != "" {
		if _, err := s.paypalToken(pp); err != nil {
			pruefung = "Anmeldung bei PayPal fehlgeschlagen: " + err.Error()
		} else {
			pruefung = "Anmeldung bei PayPal erfolgreich (" + pp.Modus + ")"
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "paypal": pruefung})
}

// paypalZugangSchreiben: leeres Secret = bisheriges behalten.
func (s *Server) paypalZugangSchreiben(neu hotspotPayPal) (hotspotPayPal, error) {
	alt := s.hotspotPayPalLesen()
	pp := hotspotPayPal{ClientID: strings.TrimSpace(neu.ClientID), Secret: alt.Secret, Modus: neu.Modus}
	if sec := strings.TrimSpace(neu.Secret); sec != "" {
		pp.Secret = sec
	}
	if pp.Modus != "live" {
		pp.Modus = "sandbox"
	}
	if err := jsonAtomar(s.hotspotDatei("hotspot-paypal.json"), pp, 0o600); err != nil {
		return pp, err
	}
	hotspotMu.Lock()
	hotspotToken = "" // neue Zugangsdaten: neu anmelden
	hotspotMu.Unlock()
	return pp, nil
}

// POST /api/v1/admin/paypal/versand {bestellung, listing_id, versendet}
func (s *Server) paypalVersandSetzen(c *gin.Context) {
	var req struct {
		Bestellung string `json:"bestellung"`
		ListingID  string `json:"listing_id"`
		Versendet  bool   `json:"versendet"`
	}
	if c.ShouldBindJSON(&req) != nil || !reBestellung.MatchString(req.Bestellung) || req.ListingID == "" || len(req.ListingID) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	mpVerkaufMu.Lock()
	defer mpVerkaufMu.Unlock()
	versand := map[string]int64{}
	if b, err := os.ReadFile(s.hotspotDatei("marktplatz-paypal-versand.json")); err == nil {
		_ = json.Unmarshal(b, &versand)
	}
	k := req.Bestellung + "|" + req.ListingID
	if req.Versendet {
		versand[k] = time.Now().Unix()
	} else {
		delete(versand, k)
	}
	if err := jsonAtomar(s.hotspotDatei("marktplatz-paypal-versand.json"), versand, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── R923: PayPal-Empfänger je Benutzerkonto ────────────────────────────────
// Statt jedes Angebot einzeln zu bearbeiten, hinterlegt der Nutzer seine
// PayPal-Adresse einmal für sein Konto (Fundus-ID). Der Node
//   - trägt sie in alle Angebote dieses Kontos ein (eigene direkt, Angebote auf
//     anderen eigenen Nodes über die signierte Weiterleitung aus R611),
//   - setzt sie bei neuen Angeboten ein, wenn das Feld leer bleibt.
// Ein im Angebot abweichend eingetragener Empfänger bleibt, außer der Nutzer
// verlangt ausdrücklich „alle“.

var kontoPayPalMu sync.Mutex

func (s *Server) kontoPayPalLesen() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(s.hotspotDatei("paypal-konten.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// kontoPayPal: hinterlegter Empfänger einer Fundus-ID ("" = keiner).
func (s *Server) kontoPayPal(fid string) string {
	if fid == "" {
		return ""
	}
	kontoPayPalMu.Lock()
	defer kontoPayPalMu.Unlock()
	return s.kontoPayPalLesen()[strings.ToLower(fid)]
}

// listingsDesKontos: alle nicht gelöschten Angebote einer Fundus-ID.
func (s *Server) listingsDesKontos(fid string) []*storage.Record {
	var out []*storage.Record
	recs, err := s.store.List(storage.RecordListing)
	if err != nil {
		return nil
	}
	for _, r := range recs {
		if r == nil || r.DeletedAt != nil || r.Data == nil {
			continue
		}
		if cr, _ := r.Data["creator_fid"].(string); strings.EqualFold(strings.TrimSpace(cr), fid) {
			out = append(out, r)
		}
	}
	return out
}

// eigenesListingFeld: ein Feld im EIGENEN Angebot setzen, neu signieren, verteilen.
func (s *Server) eigenesListingFeld(rec *storage.Record, feld string, wert any) error {
	listingStockMu.Lock()
	aktuell, err := s.store.Get(storage.RecordListing, rec.ID)
	if err != nil || aktuell == nil || aktuell.Data == nil {
		listingStockMu.Unlock()
		return fmt.Errorf("Angebot nicht gefunden")
	}
	aktuell.Data[feld] = wert
	aktuell.UpdatedAt = time.Now()
	if s.node != nil && len(aktuell.Signature) > 0 {
		if sig, err := s.node.SignData(aktuell.SigningBytes()); err == nil {
			aktuell.Signature = sig
		}
	}
	err = s.store.Put(aktuell)
	listingStockMu.Unlock()
	if err != nil {
		return err
	}
	if s.node != nil {
		if data, err := marshalRecord(aktuell); err == nil {
			go func() {
				defer func() { _ = recover() }()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = s.node.Publish(ctx, p2p.TopicListings, data)
			}()
		}
	}
	return nil
}

// GET /api/v1/konto/paypal – Empfänger des angemeldeten Kontos und Stand seiner Angebote.
func (s *Server) kontoPayPalZeigen(c *gin.Context) {
	fid := s.sessionFID(c)
	if fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte mit deinem Fundus-Konto anmelden."})
		return
	}
	empf := s.kontoPayPal(fid)
	mit, ohne, anders := 0, 0, 0
	for _, r := range s.listingsDesKontos(fid) {
		e, _ := r.Data["paypal"].(string)
		switch {
		case e == "":
			ohne++
		case empf != "" && !strings.EqualFold(e, empf):
			anders++
		default:
			mit++
		}
	}
	c.JSON(http.StatusOK, gin.H{"fid": fid, "empfaenger": empf, "angebote_mit": mit, "angebote_ohne": ohne, "angebote_anders": anders})
}

// POST /api/v1/konto/paypal {empfaenger, alle}
func (s *Server) kontoPayPalSetzen(c *gin.Context) {
	fid := s.sessionFID(c)
	if fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte mit deinem Fundus-Konto anmelden."})
		return
	}
	var req struct {
		Empfaenger string `json:"empfaenger"`
		Alle       bool   `json:"alle"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	neu, err := paypalEmpfaengerNorm(req.Empfaenger)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	kontoPayPalMu.Lock()
	m := s.kontoPayPalLesen()
	alt := m[fid]
	if neu == "" {
		delete(m, fid)
	} else {
		m[fid] = neu
	}
	err = jsonAtomar(s.hotspotDatei("paypal-konten.json"), m, 0o600)
	kontoPayPalMu.Unlock()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	// Angebote nachziehen: leere, die mit dem bisherigen Standard – oder alle.
	geaendert, weiter, fehler := 0, 0, 0
	var gruende []string
	for _, r := range s.listingsDesKontos(fid) {
		e, _ := r.Data["paypal"].(string)
		if strings.EqualFold(e, neu) {
			continue
		}
		if !(req.Alle || e == "" || (alt != "" && strings.EqualFold(e, alt))) {
			continue
		}
		if r.OwnerID == "" || r.OwnerID == s.nodeID() {
			if err := s.eigenesListingFeld(r, "paypal", neu); err != nil {
				fehler++
				gruende = append(gruende, err.Error())
				continue
			}
			geaendert++
			continue
		}
		if err := s.forwardListingEdit(c, r, map[string]any{"paypal": neu}); err != nil {
			fehler++
			if len(gruende) < 3 {
				gruende = append(gruende, kiKuerzen(fmt.Sprint(r.Data["title"]), 40)+": "+err.Error())
			}
			continue
		}
		weiter++
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "empfaenger": neu, "geaendert": geaendert, "weitergeleitet": weiter, "fehler": fehler, "gruende": gruende})
}
