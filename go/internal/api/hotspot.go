package api

// WLAN-Hotspot (R865): Internetzugang in drei frei definierbaren Stufen,
// gratis oder gegen Zahlung per PayPal in EUR.
//
// Arbeitsteilung: Der Node RECHNET – Stufen, Zahlungen, wer wie lange
// freigeschaltet ist – und schreibt
//   <DataDir>/hotspot.json            Einstellungen (für den Wächter lesbar)
//   <DataDir>/hotspot-zugaenge.json   freigeschaltete Geräte
//   <DataDir>/hotspot-paypal.json     PayPal-Zugangsdaten (nur der Node, 0600)
//   <DataDir>/hotspot-zahlungen.jsonl Zahlungsbelege (nur der Node, 0600)
// Der Wächter tools/fundus-hotspot.sh (Root) setzt nur um: Zugangspunkt,
// Freischaltungen, Drosselung. Der Node braucht dafür keine Root-Rechte.
//
// Die Anmeldeseite läuft auf einem EIGENEN Webserver (Port 3088) direkt im
// Node – unabhängig von nginx und der Fundus-Anmeldung – und antwortet nur
// Anfragen, die über den Hotspot (10.42.0.1) hereinkommen.
//
// PayPal: Bestellung anlegen → Gerät bezahlt im PayPal-Fenster → der Node lässt
// sich die Zahlung von PayPal BESTÄTIGEN (Betrag, Währung, Gerät müssen exakt
// passen). Was der Browser meldet, zählt nicht als Beweis.

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	hotspotGW     = "10.42.0.1"
	hotspotPortal = ":3088"
)

type hotspotStufe struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Down    float64 `json:"down"`    // Mbit/s zum Gerät
	Up      float64 `json:"up"`      // Mbit/s vom Gerät
	Stunden float64 `json:"stunden"` // Dauer eines Zugangs
	Preis   float64 `json:"preis"`   // EUR inkl. USt; 0 = gratis
}

type hotspotConfig struct {
	Aktiv       bool           `json:"aktiv"`
	Mitbenutzen bool           `json:"mitbenutzen"` // R866: eingebautes WLAN für Verbindung UND Hotspot
	SSID   string         `json:"ssid"`
	Kanal  int            `json:"kanal"`
	Stufen []hotspotStufe `json:"stufen"`
}

type hotspotPayPal struct {
	ClientID string `json:"client_id"`
	Secret   string `json:"secret"`
	Modus    string `json:"modus"` // "sandbox" | "live"
}

type hotspotZugang struct {
	MAC    string  `json:"mac"`
	IP     string  `json:"ip"`
	Stufe  string  `json:"stufe"`
	Name   string  `json:"name"`
	Down   float64 `json:"down"`
	Up     float64 `json:"up"`
	Bis    int64   `json:"bis"`
	Quelle string  `json:"quelle"` // "gratis" | "paypal"
	Ref    string  `json:"ref,omitempty"`
}

var (
	hotspotMu      sync.Mutex
	hotspotOffen   = map[string]hotspotOffeneBestellung{} // PayPal-Bestellung → erwartet
	hotspotToken   string
	hotspotTokenBis time.Time
	reMAC          = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)
	reBestellung   = regexp.MustCompile(`^[A-Z0-9]{5,40}$`)
)

type hotspotOffeneBestellung struct {
	MAC, Stufe, Betrag string
	Angelegt           time.Time
}

func hotspotStandard() hotspotConfig {
	return hotspotConfig{SSID: "Fundus WLAN", Kanal: 6, Stufen: []hotspotStufe{
		{ID: "basis", Name: "Basis", Down: 5, Up: 2, Stunden: 1, Preis: 0},
		{ID: "standard", Name: "Standard", Down: 20, Up: 10, Stunden: 24, Preis: 2},
		{ID: "premium", Name: "Premium", Down: 1000, Up: 1000, Stunden: 24, Preis: 5},
	}}
}

func (s *Server) hotspotDatei(n string) string { return filepath.Join(s.cfg.DataDir, n) }

func (s *Server) hotspotConfigLesen() hotspotConfig {
	c := hotspotStandard()
	if b, err := os.ReadFile(s.hotspotDatei("hotspot.json")); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func (s *Server) hotspotPayPalLesen() hotspotPayPal {
	var p hotspotPayPal
	if b, err := os.ReadFile(s.hotspotDatei("hotspot-paypal.json")); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	if p.Modus != "live" {
		p.Modus = "sandbox"
	}
	return p
}

func jsonAtomar(pfad string, v any, modus os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(pfad+".neu", b, modus); err != nil {
		return err
	}
	return os.Rename(pfad+".neu", pfad)
}

// hotspotZugaenge: auf DIESEM Knoten ausgestellte Zugänge (mit MAC, nur lokal).
// Die Datei für den Wächter (hotspot-zugaenge.json) berechnet hotspotAbgleich
// daraus und aus den Pässen des Verbunds (R866).
func (s *Server) hotspotZugaenge() []hotspotZugang {
	var l []hotspotZugang
	if b, err := os.ReadFile(s.hotspotDatei("hotspot-lokal.json")); err == nil {
		_ = json.Unmarshal(b, &l)
	}
	return l
}

// hotspotFreischalten: Zugang für ein Gerät setzen oder verlängern (gleiche Stufe).
func (s *Server) hotspotFreischalten(mac, ip string, st hotspotStufe, quelle, ref string) hotspotZugang {
	hotspotMu.Lock()
	defer hotspotMu.Unlock()
	jetzt := time.Now().Unix()
	dauer := int64(st.Stunden * 3600)
	z := hotspotZugang{MAC: mac, IP: ip, Stufe: st.ID, Name: st.Name, Down: st.Down, Up: st.Up,
		Bis: jetzt + dauer, Quelle: quelle, Ref: ref}
	var neu []hotspotZugang
	for _, alt := range s.hotspotZugaenge() {
		if alt.Bis <= jetzt {
			continue
		}
		if alt.MAC == mac {
			if alt.Stufe == st.ID && quelle == "paypal" {
				z.Bis = alt.Bis + dauer // bezahlte Verlängerung
			}
			continue
		}
		neu = append(neu, alt)
	}
	neu = append(neu, z)
	_ = jsonAtomar(s.hotspotDatei("hotspot-lokal.json"), neu, 0o600)
	s.hotspotPassAusstellen(z)
	go s.hotspotAbgleich() // sofort freischalten, nicht erst beim nächsten Takt
	return z
}

func (s *Server) hotspotAufraeumen() {
	for {
		time.Sleep(time.Minute)
		hotspotMu.Lock()
		jetzt := time.Now().Unix()
		alle := s.hotspotZugaenge()
		var neu []hotspotZugang
		for _, z := range alle {
			if z.Bis > jetzt {
				neu = append(neu, z)
			}
		}
		if len(neu) != len(alle) {
			if neu == nil {
				neu = []hotspotZugang{}
			}
			_ = jsonAtomar(s.hotspotDatei("hotspot-lokal.json"), neu, 0o600)
		}
		for id, o := range hotspotOffen {
			if time.Since(o.Angelegt) > 30*time.Minute {
				delete(hotspotOffen, id)
			}
		}
		hotspotMu.Unlock()
	}
}

// macZuIP: Gerät im Hotspot über die ARP-Tabelle des Pi.
func macZuIP(ip string) string {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		t := strings.Fields(sc.Text())
		if len(t) >= 4 && t[0] == ip {
			mac := strings.ToLower(t[3])
			if reMAC.MatchString(mac) && mac != "00:00:00:00:00:00" {
				return mac
			}
		}
	}
	return ""
}

// ── PayPal ───────────────────────────────────────────────────────────────────

func paypalBasis(modus string) string {
	if modus == "live" {
		return "https://api-m.paypal.com"
	}
	return "https://api-m.sandbox.paypal.com"
}

var paypalClient = &http.Client{Timeout: 20 * time.Second}

func (s *Server) paypalToken(p hotspotPayPal) (string, error) {
	hotspotMu.Lock()
	if hotspotToken != "" && time.Now().Before(hotspotTokenBis) {
		t := hotspotToken
		hotspotMu.Unlock()
		return t, nil
	}
	hotspotMu.Unlock()
	req, _ := http.NewRequest(http.MethodPost, paypalBasis(p.Modus)+"/v1/oauth2/token",
		strings.NewReader("grant_type=client_credentials"))
	req.SetBasicAuth(p.ClientID, p.Secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := paypalClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var d struct {
		AccessToken string  `json:"access_token"`
		ExpiresIn   float64 `json:"expires_in"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &d) != nil || d.AccessToken == "" {
		return "", fmt.Errorf("PayPal-Anmeldung abgelehnt (%d) – Client-ID, Secret und Modus prüfen", resp.StatusCode)
	}
	hotspotMu.Lock()
	hotspotToken, hotspotTokenBis = d.AccessToken, time.Now().Add(time.Duration(d.ExpiresIn*0.9)*time.Second)
	hotspotMu.Unlock()
	return d.AccessToken, nil
}

func (s *Server) paypalAnfrage(p hotspotPayPal, pfad string, rumpf any) (map[string]any, int, error) {
	token, err := s.paypalToken(p)
	if err != nil {
		return nil, 0, err
	}
	b, _ := json.Marshal(rumpf)
	req, _ := http.NewRequest(http.MethodPost, paypalBasis(p.Modus)+pfad, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := paypalClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var d map[string]any
	_ = json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&d)
	return d, resp.StatusCode, nil
}

func betragText(eur float64) string { return fmt.Sprintf("%.2f", math.Round(eur*100)/100) }

// ── Anmeldeseite (eigener Webserver, nur über den Hotspot) ───────────────────

func (s *Server) hotspotGeraet(c *gin.Context) (ip, mac string, ok bool) {
	la, _ := c.Request.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if la == nil {
		return "", "", false
	}
	if h, _, err := net.SplitHostPort(la.String()); err != nil || h != hotspotGW {
		return "", "", false // nicht über den Hotspot hereingekommen
	}
	ip, _, _ = net.SplitHostPort(c.Request.RemoteAddr)
	return ip, macZuIP(ip), true
}

func (s *Server) hotspotStufeFinden(id string) (hotspotStufe, bool) {
	for _, st := range s.hotspotConfigLesen().Stufen {
		if st.ID == id {
			return st, true
		}
	}
	return hotspotStufe{}, false
}

func (s *Server) hotspotPortalStarten() {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(func(c *gin.Context) {
		if _, _, ok := s.hotspotGeraet(c); !ok {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	r.GET("/", func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(hotspotSeite)) })
	r.GET("/api/angebot", s.hotspotAngebot)
	r.POST("/api/gratis", s.hotspotGratis)
	r.POST("/api/bestellung", s.hotspotBestellung)
	r.POST("/api/abschluss", s.hotspotAbschluss)
	r.POST("/api/fehler", s.hotspotBrowserFehler) // R875: Fehler des PayPal-Skripts im Telefon
	// Erkennungsanfragen der Telefone (generate_204, hotspot-detect …) und
	// jede andere Adresse: zur Anmeldeseite.
	r.NoRoute(func(c *gin.Context) { c.Redirect(http.StatusFound, "http://"+hotspotGW+hotspotPortal+"/") })
	srv := &http.Server{Addr: hotspotPortal, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	for {
		if err := srv.ListenAndServe(); err != nil && s.log != nil {
			s.log.Warn("Hotspot-Anmeldeseite: Port belegt", zap.Error(err))
		}
		time.Sleep(time.Minute)
	}
}

func (s *Server) hotspotAngebot(c *gin.Context) {
	ip, mac, _ := s.hotspotGeraet(c)
	cfg, pp := s.hotspotConfigLesen(), s.hotspotPayPalLesen()
	h := gin.H{"stufen": cfg.Stufen, "paypal": gin.H{"client_id": pp.ClientID, "bereit": pp.ClientID != "" && pp.Secret != ""}, "ip": ip}
	for _, z := range s.hotspotAktuell() {
		if z.MAC == mac && mac != "" && z.Bis > time.Now().Unix() {
			h["zugang"] = z
		}
	}
	if mac == "" {
		h["hinweis"] = "Gerät nicht erkannt – bitte WLAN kurz trennen und neu verbinden."
	}
	c.JSON(http.StatusOK, h)
}

func (s *Server) hotspotGratis(c *gin.Context) {
	ip, mac, _ := s.hotspotGeraet(c)
	var req struct{ Stufe string }
	_ = c.ShouldBindJSON(&req)
	st, ok := s.hotspotStufeFinden(req.Stufe)
	if !ok || st.Preis > 0 || mac == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Diese Stufe ist nicht gratis."})
		return
	}
	z := s.hotspotFreischalten(mac, ip, st, "gratis", "")
	c.JSON(http.StatusOK, gin.H{"ok": true, "zugang": z})
}

func (s *Server) hotspotBestellung(c *gin.Context) {
	_, mac, _ := s.hotspotGeraet(c)
	var req struct{ Stufe string }
	_ = c.ShouldBindJSON(&req)
	st, ok := s.hotspotStufeFinden(req.Stufe)
	pp := s.hotspotPayPalLesen()
	if !ok || st.Preis <= 0 || mac == "" || pp.ClientID == "" || pp.Secret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bezahlung derzeit nicht möglich."})
		return
	}
	betrag := betragText(st.Preis)
	d, code, err := s.paypalAnfrage(pp, "/v2/checkout/orders", gin.H{
		"intent": "CAPTURE",
		"purchase_units": []gin.H{{
			"amount":      gin.H{"currency_code": "EUR", "value": betrag},
			"description": fmt.Sprintf("WLAN %s – %g h, %g Mbit/s", st.Name, st.Stunden, st.Down),
			"custom_id":   mac + "|" + st.ID,
		}},
	})
	id, _ := d["id"].(string)
	if err != nil || code/100 != 2 || id == "" {
		// R875: PayPals eigenen Grund weitergeben, statt „nicht erreichbar“ zu
		// behaupten – lehnt PayPal inhaltlich ab, steht der Grund in der Antwort.
		grund := "PayPal ist gerade nicht erreichbar."
		if err == nil {
			grund = "PayPal lehnt die Bestellung ab: " + paypalGrund(d, code)
		} else if strings.Contains(err.Error(), "PayPal-Anmeldung") {
			grund = err.Error()
		}
		hotspotFehlerMerken("Bestellung: " + grund)
		if s.log != nil {
			s.log.Warn("Hotspot: PayPal-Bestellung fehlgeschlagen", zap.Int("status", code), zap.String("grund", grund), zap.Error(err))
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": grund})
		return
	}
	hotspotMu.Lock()
	hotspotOffen[id] = hotspotOffeneBestellung{MAC: mac, Stufe: st.ID, Betrag: betrag, Angelegt: time.Now()}
	hotspotMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"id": id})
}

func (s *Server) hotspotAbschluss(c *gin.Context) {
	ip, mac, _ := s.hotspotGeraet(c)
	var req struct{ ID string }
	_ = c.ShouldBindJSON(&req)
	hotspotMu.Lock()
	o, ok := hotspotOffen[req.ID]
	hotspotMu.Unlock()
	if !ok || o.MAC != mac || !reBestellung.MatchString(req.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unbekannte Bestellung."})
		return
	}
	pp := s.hotspotPayPalLesen()
	d, code, err := s.paypalAnfrage(pp, "/v2/checkout/orders/"+req.ID+"/capture", gin.H{})
	// Bestätigung durch PayPal: Status, Betrag, Währung und Gerät müssen passen.
	bestaetigt := false
	if err == nil && code/100 == 2 {
		if st, _ := d["status"].(string); st == "COMPLETED" {
			for _, pu := range anyListe(d["purchase_units"]) {
				for _, cap := range anyListe(mapAus(mapAus(pu)["payments"])["captures"]) {
					m := mapAus(cap)
					amt := mapAus(m["amount"])
					if aeroText(m, "status") == "COMPLETED" && aeroText(amt, "currency_code") == "EUR" &&
						aeroText(amt, "value") == o.Betrag && aeroText(m, "custom_id") == o.MAC+"|"+o.Stufe {
						bestaetigt = true
					}
				}
			}
		}
	}
	if !bestaetigt {
		if s.log != nil {
			s.log.Warn("Hotspot: Zahlung nicht bestätigt", zap.String("bestellung", req.ID), zap.Int("status", code), zap.Error(err))
		}
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "Zahlung nicht bestätigt. Es wurde nichts abgebucht, oder bitte erneut versuchen."})
		return
	}
	hotspotMu.Lock()
	delete(hotspotOffen, req.ID)
	hotspotMu.Unlock()
	st, _ := s.hotspotStufeFinden(o.Stufe)
	z := s.hotspotFreischalten(mac, ip, st, "paypal", req.ID)
	// Beleg für die Buchhaltung (nur der Node liest ihn).
	if f, err := os.OpenFile(s.hotspotDatei("hotspot-zahlungen.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		b, _ := json.Marshal(gin.H{"zeit": time.Now().Unix(), "bestellung": req.ID, "betrag_eur": o.Betrag,
			"stufe": st.Name, "stunden": st.Stunden, "modus": pp.Modus})
		_, _ = f.Write(append(b, '\n'))
		f.Close()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "zugang": z, "beleg": req.ID, "betrag": o.Betrag})
}

func anyListe(v any) []any {
	l, _ := v.([]any)
	return l
}

// ── Einstellungen (Admin) ────────────────────────────────────────────────────

// GET /api/v1/admin/hotspot
func (s *Server) hotspotEinstellungen(c *gin.Context) {
	cfg, pp := s.hotspotConfigLesen(), s.hotspotPayPalLesen()
	h := gin.H{"config": cfg, "paypal": gin.H{"client_id": pp.ClientID, "secret_gesetzt": pp.Secret != "", "modus": pp.Modus},
		"verbund": gin.H{"schluessel": s.hotspotVerbundSchluessel()}}
	if b, err := os.ReadFile("/run/fundus-hotspot/status.json"); err == nil {
		var st map[string]any
		if json.Unmarshal(b, &st) == nil {
			h["status"] = st
		}
	}
	var aktiv []hotspotZugang
	for _, z := range s.hotspotAktuell() {
		if z.Bis > time.Now().Unix() {
			aktiv = append(aktiv, z)
		}
	}
	if aktiv == nil {
		aktiv = []hotspotZugang{}
	}
	h["zugaenge"] = aktiv
	hotspotMu.Lock()
	if hotspotLetzterFehler != "" {
		h["letzter_fehler"] = gin.H{"text": hotspotLetzterFehler, "zeit": hotspotLetzterFehlerZeit.Unix()}
	}
	hotspotMu.Unlock()
	c.JSON(http.StatusOK, h)
}

// POST /api/v1/admin/hotspot {config, paypal:{client_id, secret?, modus}}
func (s *Server) hotspotSpeichern(c *gin.Context) {
	var req struct {
		Config  hotspotConfig `json:"config"`
		PayPal  hotspotPayPal `json:"paypal"`
		Verbund struct {
			Schluessel string `json:"schluessel"`
		} `json:"verbund"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	cfg := req.Config
	cfg.SSID = strings.TrimSpace(cfg.SSID)
	if cfg.SSID == "" || len(cfg.SSID) > 32 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Netzname: 1 bis 32 Zeichen."})
		return
	}
	if cfg.Kanal != 1 && cfg.Kanal != 6 && cfg.Kanal != 11 {
		cfg.Kanal = 6
	}
	if len(cfg.Stufen) != 3 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Es sind genau drei Stufen."})
		return
	}
	for i, st := range cfg.Stufen {
		st.ID = []string{"basis", "standard", "premium"}[i]
		st.Name = strings.TrimSpace(st.Name)
		if st.Name == "" || len(st.Name) > 30 || st.Down < 1 || st.Down > 1000 || st.Up < 1 || st.Up > 1000 ||
			st.Stunden <= 0 || st.Stunden > 24*31 || st.Preis < 0 || st.Preis > 1000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Stufe %d: Name, 1–1000 Mbit/s, Dauer bis 31 Tage, Preis 0–1000 €.", i+1)})
			return
		}
		cfg.Stufen[i] = st
	}
	alt := s.hotspotPayPalLesen()
	pp := hotspotPayPal{ClientID: strings.TrimSpace(req.PayPal.ClientID), Secret: alt.Secret, Modus: req.PayPal.Modus}
	if sec := strings.TrimSpace(req.PayPal.Secret); sec != "" {
		pp.Secret = sec // leer = bisheriges behalten
	}
	if pp.Modus != "live" {
		pp.Modus = "sandbox"
	}
	if err := jsonAtomar(s.hotspotDatei("hotspot.json"), cfg, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	vs := strings.TrimSpace(req.Verbund.Schluessel)
	if vs != "" && (len(vs) < 16 || len(vs) > 200) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Verbund-Schlüssel: mindestens 16 Zeichen."})
		return
	}
	if err := jsonAtomar(s.hotspotDatei("hotspot-verbund.json"), gin.H{"schluessel": vs}, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	if err := jsonAtomar(s.hotspotDatei("hotspot-paypal.json"), pp, 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	hotspotMu.Lock()
	hotspotToken = "" // neue Zugangsdaten: neu anmelden
	hotspotMu.Unlock()
	go s.hotspotAbgleich() // geänderte Geschwindigkeiten sofort an den Wächter
	// Zugangsdaten gleich prüfen, damit Fehler hier auffallen und nicht beim Kunden.
	pruefung := "ohne PayPal"
	if pp.ClientID != "" && pp.Secret != "" {
		if _, err := s.paypalToken(pp); err != nil {
			pruefung = err.Error()
		} else {
			pruefung = "PayPal-Anmeldung erfolgreich (" + pp.Modus + ")"
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "paypal": pruefung})
}

func (s *Server) registerHotspot(r *gin.Engine) {
	// Übergang von R865: dort lagen die lokalen Zugänge in hotspot-zugaenge.json.
	if _, err := os.Stat(s.hotspotDatei("hotspot-lokal.json")); os.IsNotExist(err) {
		if b, err := os.ReadFile(s.hotspotDatei("hotspot-zugaenge.json")); err == nil {
			_ = os.WriteFile(s.hotspotDatei("hotspot-lokal.json"), b, 0o600)
		}
	}
	go s.hotspotPortalStarten()
	go s.hotspotAufraeumen()
	go func() {
		for {
			s.hotspotAbgleich()
			time.Sleep(5 * time.Second)
		}
	}()
}

// ── Verbund: Zugänge auf allen eigenen Knoten (R866) ─────────────────────────
// Ein Zugang wird zum PASS: Prüfwert des Geräts, Geschwindigkeit, Ablaufzeit –
// unterschrieben mit dem Verbund-Schlüssel und im Fundus-Netz verteilt. Nur
// Knoten mit demselben Schlüssel können Pässe ausstellen (sonst könnte jeder
// Fundus-Node gefälschte Zugänge verteilen) und Geräte wiedererkennen; statt
// der Geräteadresse geht nur HMAC(Schlüssel, MAC) ins Netz. Telefone verwenden
// je Netzname eine feste Adresse – bei gleichem Netznamen auf allen Knoten also
// dieselbe. Ohne Schlüssel bleibt jeder Zugang auf seinem Knoten.

func (s *Server) hotspotVerbundSchluessel() string {
	var v struct {
		Schluessel string `json:"schluessel"`
	}
	if b, err := os.ReadFile(s.hotspotDatei("hotspot-verbund.json")); err == nil {
		_ = json.Unmarshal(b, &v)
	}
	return v.Schluessel
}

func hmacHex(schluessel, text string) string {
	m := hmac.New(sha256.New, []byte(schluessel))
	m.Write([]byte(text))
	return hex.EncodeToString(m.Sum(nil))
}

func passSignatur(schluessel, h string, down, up float64, bis int64, ref string) string {
	return hmacHex(schluessel, fmt.Sprintf("fundus-hotspot-pass-v1|%s|%g|%g|%d|%s", h, down, up, bis, ref))
}

// R876: v2 unterschreibt auch die Stufe – damit gilt auf jedem Knoten die
// AKTUELLE Geschwindigkeit dieser Stufe, und niemand kann unterwegs aus
// „basis“ ein „premium“ machen.
func passSignaturV2(schluessel, h, stufe string, down, up float64, bis int64, ref string) string {
	return hmacHex(schluessel, fmt.Sprintf("fundus-hotspot-pass-v2|%s|%s|%g|%g|%d|%s", h, stufe, down, up, bis, ref))
}

func (s *Server) hotspotPassAusstellen(z hotspotZugang) {
	k := s.hotspotVerbundSchluessel()
	if k == "" {
		return
	}
	h := hmacHex(k, "mac|"+z.MAC)
	ref := z.Ref
	if ref == "" {
		ref = z.Quelle
	}
	s.marktVeroeffentlichen(&storage.Record{ID: fmt.Sprintf("%s-%d", h[:24], z.Bis), Type: storage.RecordHotspotPass,
		Data: map[string]any{"h": h, "stufe": z.Stufe, "down": z.Down, "up": z.Up, "bis": z.Bis, "name": z.Name, "ref": ref,
			"sig": passSignaturV2(k, h, z.Stufe, z.Down, z.Up, z.Bis, ref)}})
}

// hotspotAktuell: lokale Zugänge + Pässe des Verbunds für Geräte, die gerade
// bei diesem Knoten verbunden sind (aus den Leases der Adressvergabe).
func (s *Server) hotspotAktuell() []hotspotZugang {
	jetzt := time.Now().Unix()
	// R876: maßgeblich ist die AKTUELLE Geschwindigkeit der Stufe – nicht die
	// Kopie vom Zeitpunkt der Freischaltung. Sonst wirkte eine geänderte
	// Stufe erst, wenn die bestehenden Zugänge abgelaufen waren.
	stufen := map[string]hotspotStufe{}
	for _, st := range s.hotspotConfigLesen().Stufen {
		stufen[st.ID] = st
	}
	aktuell := func(z hotspotZugang) hotspotZugang {
		if st, ok := stufen[z.Stufe]; ok {
			z.Down, z.Up, z.Name = st.Down, st.Up, st.Name
		}
		return z
	}
	jeMAC := map[string]hotspotZugang{}
	for _, z := range s.hotspotZugaenge() {
		if z.Bis > jetzt {
			jeMAC[z.MAC] = aktuell(z)
		}
	}
	k := s.hotspotVerbundSchluessel()
	if k != "" {
		pass := map[string]hotspotZugang{} // Prüfwert → längster gültiger Pass
		alle, _ := s.store.List(storage.RecordHotspotPass)
		for _, r := range alle {
			if r == nil || r.Data == nil {
				continue
			}
			h, _ := r.Data["h"].(string)
			stufe, _ := r.Data["stufe"].(string)
			ref, _ := r.Data["ref"].(string)
			sig, _ := r.Data["sig"].(string)
			name, _ := r.Data["name"].(string)
			d, _ := toFloatOK(r.Data["down"])
			u, _ := toFloatOK(r.Data["up"])
			b, _ := toFloatOK(r.Data["bis"])
			bis := int64(b)
			if bis <= jetzt || bis > jetzt+32*86400 || d < 1 || d > 1000 || u < 1 || u > 1000 {
				if bis < jetzt-86400 {
					_ = s.store.HardDelete(storage.RecordHotspotPass, r.ID) // längst abgelaufen
				}
				continue
			}
			erwartet := passSignatur(k, h, d, u, bis, ref) // v1 (R866)
			if stufe != "" {
				erwartet = passSignaturV2(k, h, stufe, d, u, bis, ref)
			}
			if !hmac.Equal([]byte(sig), []byte(erwartet)) {
				continue // nicht von einem Knoten dieses Verbunds
			}
			if alt, ok := pass[h]; !ok || bis > alt.Bis {
				pass[h] = aktuell(hotspotZugang{Stufe: stufe, Name: name, Down: d, Up: u, Bis: bis, Quelle: "verbund", Ref: ref})
			}
		}
		if len(pass) > 0 {
			if f, err := os.Open("/run/fundus-hotspot/leases"); err == nil {
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					t := strings.Fields(sc.Text())
					if len(t) < 3 {
						continue
					}
					mac := strings.ToLower(t[1])
					if p, ok := pass[hmacHex(k, "mac|"+mac)]; ok {
						if alt, ok := jeMAC[mac]; !ok || p.Bis > alt.Bis {
							p.MAC, p.IP = mac, t[2]
							if ok && alt.Quelle != "verbund" {
								p.Quelle = alt.Quelle
							}
							jeMAC[mac] = p
						}
					}
				}
				f.Close()
			}
		}
	}
	out := make([]hotspotZugang, 0, len(jeMAC))
	for _, z := range jeMAC {
		out = append(out, z)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MAC < out[j].MAC })
	return out
}

var hotspotLetzteListe []byte

// hotspotAbgleich schreibt die Liste für den Wächter – nur bei Änderung.
func (s *Server) hotspotAbgleich() {
	b, err := json.Marshal(s.hotspotAktuell())
	if err != nil {
		return
	}
	hotspotMu.Lock()
	gleich := bytes.Equal(b, hotspotLetzteListe)
	if !gleich {
		hotspotLetzteListe = b
	}
	hotspotMu.Unlock()
	if gleich {
		return
	}
	pfad := s.hotspotDatei("hotspot-zugaenge.json")
	if os.WriteFile(pfad+".neu", b, 0o644) == nil {
		_ = os.Rename(pfad+".neu", pfad)
	}
}

// ── Fehlerdiagnose (R875) ────────────────────────────────────────────────────

var (
	hotspotLetzterFehler     string
	hotspotLetzterFehlerZeit time.Time
)

func hotspotFehlerMerken(t string) {
	hotspotMu.Lock()
	hotspotLetzterFehler, hotspotLetzterFehlerZeit = kiKuerzen(t, 300), time.Now()
	hotspotMu.Unlock()
}

// paypalGrund: lesbarer Grund aus einer PayPal-Fehlerantwort
// ({name, message, details:[{issue, description}]}).
func paypalGrund(d map[string]any, code int) string {
	name, _ := d["name"].(string)
	msg, _ := d["message"].(string)
	teile := []string{}
	if name != "" {
		teile = append(teile, name)
	}
	for _, x := range anyListe(d["details"]) {
		m := mapAus(x)
		if i := aeroText(m, "issue"); i != "" {
			teile = append(teile, i)
		}
		if t := aeroText(m, "description"); t != "" {
			teile = append(teile, t)
		}
		break
	}
	if len(teile) == 0 && msg != "" {
		teile = append(teile, msg)
	}
	if len(teile) == 0 {
		return fmt.Sprintf("Status %d", code)
	}
	return strings.Join(teile, " – ")
}

// POST /api/fehler {stufe, text} – vom PayPal-Skript im Telefon. Nur für
// die Diagnose in den Einstellungen; begrenzt, damit niemand das Protokoll flutet.
func (s *Server) hotspotBrowserFehler(c *gin.Context) {
	var req struct{ Stufe, Text string }
	_ = c.ShouldBindJSON(&req)
	if !extRateAllow("hotspotfehler:"+c.ClientIP(), 10) {
		c.Status(http.StatusTooManyRequests)
		return
	}
	t := "PayPal im Telefon: " + kiKuerzen(req.Text, 200)
	hotspotFehlerMerken(t)
	if s.log != nil {
		s.log.Warn("Hotspot: Fehler im PayPal-Skript", zap.String("stufe", kiKuerzen(req.Stufe, 20)), zap.String("text", kiKuerzen(req.Text, 200)))
	}
	c.Status(http.StatusNoContent)
}
