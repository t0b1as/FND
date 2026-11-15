package api

// Verkauf auf der Hotspot-Anmeldeseite (R904): ein Produkt – voreingestellt
// FLIGHT QUAD 32 für 9,90 € – entweder als Link auf eine Produktseite oder
// mit PayPal-Sofortkauf. Beim Sofortkauf holt PayPal die Lieferadresse aus dem
// PayPal-Konto des Käufers (shipping_preference GET_FROM_FILE); der Node
// übernimmt sie aus der von PayPal BESTÄTIGTEN Zahlung, legt den Verkauf ab
// (hotspot-verkaeufe.jsonl, nur der Node) und meldet ihn dem Eigentümer.
//
// Die Anmeldeseite ist vor der Freischaltung erreichbar, PayPal ebenfalls
// (Ausnahmeliste des Wächters) – kaufen geht also auch ohne WLAN-Zugang.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type hotspotShop struct {
	Aktiv    bool    `json:"aktiv"`
	Art      string  `json:"art"` // "paypal" (Sofortkauf) | "link" (Produktseite)
	Titel    string  `json:"titel"`
	Text     string  `json:"text"`
	Preis    float64 `json:"preis"` // EUR je Stück inkl. USt und Versand
	MaxStk   int     `json:"max_stueck"`
	URL      string  `json:"url"`      // Produktseite (Art "link")
	Hinweise string  `json:"hinweise"` // Anbieter, Widerruf, Versand – auf der Seite aufklappbar
}

func hotspotShopStandard() hotspotShop {
	return hotspotShop{Art: "paypal", Titel: "FLIGHT QUAD 32", Preis: 9.90, MaxStk: 5,
		Text: "Das Flugquartett mit 32 Karten: echte Rekorde aus dem Luftraum über diesem Hotspot. Preis inklusive Versand."}
}

func (s *Server) hotspotShopLesen() hotspotShop {
	sh := hotspotShopStandard()
	if b, err := os.ReadFile(s.hotspotDatei("hotspot-shop.json")); err == nil {
		_ = json.Unmarshal(b, &sh)
	}
	if sh.Art != "link" {
		sh.Art = "paypal"
	}
	if sh.MaxStk < 1 || sh.MaxStk > 20 {
		sh.MaxStk = 5
	}
	return sh
}

func hotspotShopPruefen(sh *hotspotShop) error {
	sh.Titel = strings.TrimSpace(sh.Titel)
	sh.Text = strings.TrimSpace(sh.Text)
	sh.URL = strings.TrimSpace(sh.URL)
	sh.Hinweise = strings.TrimSpace(sh.Hinweise)
	if sh.Art != "link" {
		sh.Art = "paypal"
	}
	if sh.MaxStk < 1 || sh.MaxStk > 20 {
		sh.MaxStk = 5
	}
	sh.Preis = math.Round(sh.Preis*100) / 100
	if !sh.Aktiv {
		return nil
	}
	if sh.Titel == "" || len(sh.Titel) > 60 || len(sh.Text) > 600 || len(sh.Hinweise) > 6000 {
		return fmt.Errorf("Verkauf: Titel 1–60 Zeichen, Beschreibung bis 600, Hinweise bis 6000 Zeichen.")
	}
	if sh.Art == "paypal" && (sh.Preis < 0.5 || sh.Preis > 1000) {
		return fmt.Errorf("Verkauf: Preis 0,50 bis 1000 €.")
	}
	if sh.Art == "link" {
		u, err := url.Parse(sh.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("Verkauf: Produktseite als vollständige Adresse (https://…) angeben.")
		}
	}
	return nil
}

// Angebot für die Anmeldeseite.
func (s *Server) hotspotShopAngebot(paypalBereit bool) gin.H {
	sh := s.hotspotShopLesen()
	if !sh.Aktiv || (sh.Art == "paypal" && !paypalBereit) {
		return nil
	}
	h := gin.H{"art": sh.Art, "titel": sh.Titel, "text": sh.Text, "hinweise": sh.Hinweise}
	if sh.Art == "link" {
		h["url"] = sh.URL
		if sh.Preis > 0 {
			h["preis"] = sh.Preis
		}
	} else {
		h["preis"], h["max_stueck"] = sh.Preis, sh.MaxStk
	}
	return h
}

type shopOffeneBestellung struct {
	Betrag, Marke string
	Stueck        int
	Angelegt      time.Time
}

var shopOffen = map[string]shopOffeneBestellung{} // unter hotspotMu

// POST /api/kauf {stueck}
func (s *Server) hotspotKauf(c *gin.Context) {
	if !extRateAllow("hotspotkauf:"+c.ClientIP(), 10) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Versuche – bitte kurz warten."})
		return
	}
	var req struct{ Stueck int }
	_ = c.ShouldBindJSON(&req)
	sh, pp := s.hotspotShopLesen(), s.hotspotPayPalLesen()
	if !sh.Aktiv || sh.Art != "paypal" || pp.ClientID == "" || pp.Secret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Verkauf derzeit nicht möglich."})
		return
	}
	if req.Stueck < 1 || req.Stueck > sh.MaxStk {
		req.Stueck = 1
	}
	z := make([]byte, 8)
	_, _ = rand.Read(z)
	marke := "shop|" + hex.EncodeToString(z)
	einzel := betragText(sh.Preis)
	betrag := betragText(sh.Preis * float64(req.Stueck))
	eur := func(v string) gin.H { return gin.H{"currency_code": "EUR", "value": v} }
	d, code, err := s.paypalAnfrage(pp, "/v2/checkout/orders", gin.H{
		"intent": "CAPTURE",
		"payment_source": gin.H{"paypal": gin.H{"experience_context": gin.H{
			// Lieferadresse aus dem PayPal-Konto; der Käufer kann sie dort ändern.
			"shipping_preference": "GET_FROM_FILE",
			"user_action":         "PAY_NOW",
			"locale":              "de-DE",
		}}},
		"purchase_units": []gin.H{{
			"reference_id": "produkt",
			"custom_id":    marke,
			"description":  kiKuerzen(sh.Titel, 120),
			"amount":       gin.H{"currency_code": "EUR", "value": betrag, "breakdown": gin.H{"item_total": eur(betrag)}},
			"items": []gin.H{{"name": kiKuerzen(sh.Titel, 120), "quantity": fmt.Sprint(req.Stueck),
				"unit_amount": eur(einzel), "category": "PHYSICAL_GOODS"}},
		}},
	})
	id, _ := d["id"].(string)
	if err != nil || code/100 != 2 || id == "" {
		grund := "PayPal ist gerade nicht erreichbar."
		if err == nil {
			grund = "PayPal lehnt die Bestellung ab: " + paypalGrund(d, code)
		} else if strings.Contains(err.Error(), "PayPal-Anmeldung") {
			grund = err.Error()
		}
		hotspotFehlerMerken("Verkauf: " + grund)
		c.JSON(http.StatusBadGateway, gin.H{"error": grund})
		return
	}
	hotspotMu.Lock()
	shopOffen[id] = shopOffeneBestellung{Betrag: betrag, Marke: marke, Stueck: req.Stueck, Angelegt: time.Now()}
	hotspotMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"id": id})
}

type shopVerkauf struct {
	Zeit       int64             `json:"zeit"`
	Bestellung string            `json:"bestellung"`
	Zahlung    string            `json:"zahlung,omitempty"` // Capture-ID (für Erstattungen)
	Produkt    string            `json:"produkt"`
	Stueck     int               `json:"stueck"`
	Betrag     string            `json:"betrag_eur"`
	Modus      string            `json:"modus"`
	Name       string            `json:"name"`
	Adresse    map[string]string `json:"adresse"`
	Email      string            `json:"email,omitempty"`
	Versendet  int64             `json:"versendet,omitempty"`
}

// POST /api/kauf/abschluss {id}
func (s *Server) hotspotKaufAbschluss(c *gin.Context) {
	var req struct{ ID string }
	_ = c.ShouldBindJSON(&req)
	hotspotMu.Lock()
	o, ok := shopOffen[req.ID]
	hotspotMu.Unlock()
	if !ok || !reBestellung.MatchString(req.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unbekannte Bestellung."})
		return
	}
	pp := s.hotspotPayPalLesen()
	d, code, err := s.paypalAnfrage(pp, "/v2/checkout/orders/"+req.ID+"/capture", gin.H{})
	// Nur was PayPal bestätigt, zählt: Status, Betrag, Währung, Kennung.
	var v *shopVerkauf
	if err == nil && code/100 == 2 && aeroText(d, "status") == "COMPLETED" {
		for _, pu := range anyListe(d["purchase_units"]) {
			m := mapAus(pu)
			for _, cp := range anyListe(mapAus(m["payments"])["captures"]) {
				cm := mapAus(cp)
				amt := mapAus(cm["amount"])
				if aeroText(cm, "status") == "COMPLETED" && aeroText(amt, "currency_code") == "EUR" &&
					aeroText(amt, "value") == o.Betrag && aeroText(cm, "custom_id") == o.Marke {
					v = &shopVerkauf{Zahlung: aeroText(cm, "id")}
				}
			}
			if v != nil {
				sp := mapAus(m["shipping"])
				ad := mapAus(sp["address"])
				v.Name = aeroText(mapAus(sp["name"]), "full_name")
				v.Adresse = map[string]string{}
				for _, k := range []string{"address_line_1", "address_line_2", "postal_code", "admin_area_2", "admin_area_1", "country_code"} {
					if t := aeroText(ad, k); t != "" {
						v.Adresse[k] = t
					}
				}
				break
			}
		}
	}
	if v == nil {
		if s.log != nil {
			s.log.Warn("Hotspot-Verkauf: Zahlung nicht bestätigt", zap.String("bestellung", req.ID), zap.Int("status", code), zap.Error(err))
		}
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "Zahlung nicht bestätigt. Es wurde nichts abgebucht, oder bitte erneut versuchen."})
		return
	}
	hotspotMu.Lock()
	delete(shopOffen, req.ID)
	hotspotMu.Unlock()
	sh := s.hotspotShopLesen()
	v.Zeit, v.Bestellung, v.Produkt, v.Stueck, v.Betrag, v.Modus = time.Now().Unix(), req.ID, sh.Titel, o.Stueck, o.Betrag, pp.Modus
	v.Email = aeroText(mapAus(d["payer"]), "email_address")
	if f, err := os.OpenFile(s.hotspotDatei("hotspot-verkaeufe.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		b, _ := json.Marshal(v)
		_, _ = f.Write(append(b, '\n'))
		f.Close()
	}
	ort := strings.TrimSpace(v.Adresse["postal_code"] + " " + v.Adresse["admin_area_2"])
	if s.log != nil {
		s.log.Info("Hotspot-Verkauf", zap.String("bestellung", req.ID), zap.Int("stueck", v.Stueck), zap.String("betrag", v.Betrag), zap.String("modus", v.Modus))
	}
	if fid := s.eigentuemerFID(); fid != "" {
		test := ""
		if v.Modus != "live" {
			test = " (Sandbox)"
		}
		go s.meldungVerteilen(fid, "Verkauft: "+v.Produkt, fmt.Sprintf("%d Stück · %s € · an %s, %s%s", v.Stueck,
			strings.Replace(v.Betrag, ".", ",", 1), v.Name, ort, test), "/settings#hotspot", "hotspot-verkauf-"+req.ID)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "beleg": req.ID, "betrag": v.Betrag, "stueck": v.Stueck, "name": v.Name, "ort": ort})
}

// eigentuemerFID: Fundus-ID zur Admin-Wallet (für Push-Meldungen).
func (s *Server) eigentuemerFID() string {
	w := s.allowedAdminWallet()
	if w == "" || s.store == nil {
		return ""
	}
	if rec, err := s.store.Get(storage.RecordKeyDir, "keydir:"+strings.ToLower(w)); err == nil && rec != nil {
		fid, _ := rec.Data["fundus_id"].(string)
		return fid
	}
	return ""
}

// hotspotVerkaeufe: die letzten n Verkäufe, neueste zuerst, mit Versandvermerk.
func (s *Server) hotspotVerkaeufe(n int) []shopVerkauf {
	var l []shopVerkauf
	if b, err := os.ReadFile(s.hotspotDatei("hotspot-verkaeufe.jsonl")); err == nil {
		for _, z := range strings.Split(string(b), "\n") {
			var v shopVerkauf
			if json.Unmarshal([]byte(z), &v) == nil && v.Bestellung != "" {
				l = append(l, v)
			}
		}
	}
	versand := map[string]int64{}
	if b, err := os.ReadFile(s.hotspotDatei("hotspot-versand.json")); err == nil {
		_ = json.Unmarshal(b, &versand)
	}
	out := []shopVerkauf{}
	for i := len(l) - 1; i >= 0 && len(out) < n; i-- {
		l[i].Versendet = versand[l[i].Bestellung]
		out = append(out, l[i])
	}
	return out
}

// POST /api/v1/admin/hotspot/versand {bestellung, versendet}
func (s *Server) hotspotVersandSetzen(c *gin.Context) {
	var req struct {
		Bestellung string `json:"bestellung"`
		Versendet  bool   `json:"versendet"`
	}
	if c.ShouldBindJSON(&req) != nil || !reBestellung.MatchString(req.Bestellung) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	hotspotMu.Lock()
	defer hotspotMu.Unlock()
	versand := map[string]int64{}
	if b, err := os.ReadFile(s.hotspotDatei("hotspot-versand.json")); err == nil {
		_ = json.Unmarshal(b, &versand)
	}
	if req.Versendet {
		versand[req.Bestellung] = time.Now().Unix()
	} else {
		delete(versand, req.Bestellung)
	}
	if err := jsonAtomar(s.hotspotDatei("hotspot-versand.json"), versand, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
