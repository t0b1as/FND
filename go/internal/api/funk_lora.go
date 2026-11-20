package api

// Funk über LoRa (R945): Anbindung des Funkdienstes fundus-rns.
//
// fundus-rns (tools/fundus-rns.py) betreibt Reticulum mit einem RNode-
// Funkmodul (Heltec V3 o. ä.) und spricht LXMF – das Nachrichtenformat von
// Sideband, MeshChat und Nomad Network. Der Node redet mit ihm über einen
// Unix-Socket in <DataDir>/rns/api.sock.
//
// Konten: Wer „Über Funk erreichbar“ einschaltet, bekommt eine Funkadresse.
// Der Schlüssel dazu wird aus der Wallet abgeleitet – auf jedem Node gleich –
// und liegt beim Funkdienst dieses Nodes, damit Nachrichten auch ankommen,
// wenn niemand angemeldet ist. Ausschalten löscht ihn dort wieder.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (s *Server) loraOrdner() string { return filepath.Join(s.cfg.DataDir, "rns") }
func (s *Server) loraSocket() string { return filepath.Join(s.loraOrdner(), "api.sock") }

// loraInstalliert: Läuft der Funkdienst (Socket vorhanden)?
func (s *Server) loraInstalliert() bool {
	st, err := os.Stat(s.loraSocket())
	return err == nil && st.Mode()&os.ModeSocket != 0
}

func (s *Server) loraClient(timeout time.Duration) *http.Client {
	sock := s.loraSocket()
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}}
}

var errLoraFehlt = errors.New("Der Funkdienst läuft nicht – Heltec/RNode einstecken und Fundus neu deployen (oder sudo bash /opt/fundus/tools/setup-lora.sh)")

// loraAufruf: JSON-Anfrage an den Funkdienst. Fehlertext des Dienstes kommt
// als error zurück.
func (s *Server) loraAufruf(ctx context.Context, methode, pfad string, eingabe, ausgabe any, timeout time.Duration) error {
	if !s.loraInstalliert() {
		return errLoraFehlt
	}
	var body io.Reader
	if eingabe != nil {
		b, _ := json.Marshal(eingabe)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, methode, "http://fundus-rns"+pfad, body)
	if err != nil {
		return err
	}
	if eingabe != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.loraClient(timeout).Do(req)
	if err != nil {
		return fmt.Errorf("Funkdienst nicht erreichbar: %w", err)
	}
	defer resp.Body.Close()
	roh, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		var f struct {
			Fehler string `json:"fehler"`
		}
		if json.Unmarshal(roh, &f) == nil && f.Fehler != "" {
			return errors.New(f.Fehler)
		}
		return fmt.Errorf("Funkdienst: HTTP %d", resp.StatusCode)
	}
	if ausgabe != nil {
		return json.Unmarshal(roh, ausgabe)
	}
	return nil
}

// loraSchluessel: 64 Byte Reticulum-Identität (X25519 + Ed25519) aus dem
// eigenen Wallet-Geheimnis – jede Sitzung desselben Kontos kommt auf dieselbe
// Funkadresse.
func loraSchluessel(sess *Session) (string, error) {
	if sess == nil || sess.identity == nil {
		return "", errors.New("keine Sitzung")
	}
	geheim, err := sess.identity.ECDHSharedSecret(sess.identity.X25519PublicKeyHex())
	if err != nil {
		return "", err
	}
	x := sha256.Sum256(append([]byte("fundus/rns/x25519/v1:"), geheim...))
	e := sha256.Sum256(append([]byte("fundus/rns/ed25519/v1:"), geheim...))
	return hex.EncodeToString(x[:]) + hex.EncodeToString(e[:]), nil
}

func (s *Server) loraSitzung(c *gin.Context) (*Session, string, bool) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte anmelden."})
		return nil, "", false
	}
	return sess, strings.ToLower(sess.identity.FundusID), true
}

func loraFehler(c *gin.Context, err error) {
	code := http.StatusBadGateway
	if errors.Is(err, errLoraFehlt) {
		code = http.StatusServiceUnavailable
	}
	c.JSON(code, gin.H{"error": err.Error()})
}

// ── Endpunkte (Nutzer) ───────────────────────────────────────────────────────

func (s *Server) registerLora(r *gin.RouterGroup) {
	g := r.Group("/lora")
	g.GET("/status", s.loraStatus)
	g.GET("/konto", s.loraKontoZeigen)
	g.POST("/konto", s.loraKontoSetzen)
	g.GET("/gehoert", s.loraGehoert)
	g.GET("/gespraeche", s.loraGespraeche)
	g.GET("/verlauf", s.loraVerlauf)
	g.POST("/senden", s.loraSenden)
	g.POST("/ankuendigen", s.loraAnkuendigen)
}

// GET /api/v1/lora/status – Funkmodul, Sendezeit, Empfang.
func (s *Server) loraStatus(c *gin.Context) {
	if !s.loraInstalliert() {
		c.JSON(http.StatusOK, gin.H{"installiert": false, "hinweis": errLoraFehlt.Error()})
		return
	}
	var st map[string]any
	if err := s.loraAufruf(c.Request.Context(), http.MethodGet, "/status", nil, &st, 10*time.Second); err != nil {
		c.JSON(http.StatusOK, gin.H{"installiert": true, "fehler": err.Error()})
		return
	}
	delete(st, "konten") // welche Konten erreichbar sind, geht niemanden etwas an
	st["installiert"] = true
	c.JSON(http.StatusOK, st)
}

func (s *Server) loraKontoZeigen(c *gin.Context) {
	_, fid, ok := s.loraSitzung(c)
	if !ok {
		return
	}
	var k map[string]any
	if err := s.loraAufruf(c.Request.Context(), http.MethodGet, "/konto?fid="+url.QueryEscape(fid), nil, &k, 10*time.Second); err != nil {
		loraFehler(c, err)
		return
	}
	c.JSON(http.StatusOK, k)
}

// POST /api/v1/lora/konto {erreichbar, name}
func (s *Server) loraKontoSetzen(c *gin.Context) {
	sess, fid, ok := s.loraSitzung(c)
	if !ok {
		return
	}
	var req struct {
		Erreichbar bool   `json:"erreichbar"`
		Name       string `json:"name"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	ein := map[string]any{"fid": fid, "erreichbar": req.Erreichbar}
	if req.Erreichbar {
		key, err := loraSchluessel(sess)
		if err != nil {
			s.internalError(c, err)
			return
		}
		name := strings.TrimSpace(req.Name)
		if r := []rune(name); len(r) > 40 {
			name = string(r[:40])
		}
		ein["key"], ein["name"] = key, name
	}
	var aus map[string]any
	if err := s.loraAufruf(c.Request.Context(), http.MethodPost, "/konto", ein, &aus, 30*time.Second); err != nil {
		loraFehler(c, err)
		return
	}
	// Ausschalten: Der Router des Kontos lebt bis zum Neustart des Dienstes
	// weiter – neu starten, damit die Adresse sofort verstummt.
	if v, _ := aus["neustart_noetig"].(bool); v {
		_ = s.loraAufruf(c.Request.Context(), http.MethodPost, "/neustart", map[string]any{}, nil, 5*time.Second)
	}
	c.JSON(http.StatusOK, aus)
}

func (s *Server) loraGehoert(c *gin.Context) {
	if _, _, ok := s.loraSitzung(c); !ok {
		return
	}
	var g struct {
		Gehoert map[string]struct {
			Name string `json:"name"`
			Zeit int64  `json:"zeit"`
			Hops int    `json:"hops"`
		} `json:"gehoert"`
	}
	if err := s.loraAufruf(c.Request.Context(), http.MethodGet, "/gehoert", nil, &g, 10*time.Second); err != nil {
		loraFehler(c, err)
		return
	}
	type z struct {
		Adresse string `json:"adresse"`
		Name    string `json:"name"`
		Zeit    int64  `json:"zeit"`
		Hops    int    `json:"hops"`
	}
	liste := make([]z, 0, len(g.Gehoert))
	for a, e := range g.Gehoert {
		liste = append(liste, z{a, e.Name, e.Zeit, e.Hops})
	}
	sort.Slice(liste, func(i, j int) bool { return liste[i].Zeit > liste[j].Zeit })
	c.JSON(http.StatusOK, gin.H{"gehoert": liste})
}

type loraEintrag struct {
	ID       string `json:"id"`
	Richtung string `json:"richtung"`
	Mit      string `json:"mit"`
	Text     string `json:"text"`
	Zeit     int64  `json:"zeit"`
	Status   string `json:"status"`
	Name     string `json:"name"`
}

func (s *Server) loraVerlaufHolen(ctx context.Context, fid, mit string) ([]loraEintrag, error) {
	q := "/verlauf?fid=" + url.QueryEscape(fid)
	if mit != "" {
		q += "&mit=" + url.QueryEscape(mit)
	}
	var v struct {
		Verlauf []loraEintrag `json:"verlauf"`
	}
	err := s.loraAufruf(ctx, http.MethodGet, q, nil, &v, 10*time.Second)
	return v.Verlauf, err
}

// GET /api/v1/lora/gespraeche – eine Zeile je Gegenstelle, neueste zuerst.
func (s *Server) loraGespraeche(c *gin.Context) {
	_, fid, ok := s.loraSitzung(c)
	if !ok {
		return
	}
	v, err := s.loraVerlaufHolen(c.Request.Context(), fid, "")
	if err != nil {
		loraFehler(c, err)
		return
	}
	type gespraech struct {
		Mit    string `json:"mit"`
		Name   string `json:"name"`
		Letzte string `json:"letzte"`
		Zeit   int64  `json:"zeit"`
		Anzahl int    `json:"anzahl"`
	}
	m := map[string]*gespraech{}
	for _, e := range v {
		g := m[e.Mit]
		if g == nil {
			g = &gespraech{Mit: e.Mit}
			m[e.Mit] = g
		}
		g.Anzahl++
		if e.Zeit >= g.Zeit {
			g.Zeit, g.Letzte = e.Zeit, e.Text
		}
		if e.Name != "" {
			g.Name = e.Name
		}
	}
	liste := make([]*gespraech, 0, len(m))
	for _, g := range m {
		if r := []rune(g.Letzte); len(r) > 80 {
			g.Letzte = string(r[:80]) + "…"
		}
		liste = append(liste, g)
	}
	sort.Slice(liste, func(i, j int) bool { return liste[i].Zeit > liste[j].Zeit })
	c.JSON(http.StatusOK, gin.H{"gespraeche": liste})
}

var reLoraAdresse = regexp.MustCompile(`^[0-9a-f]{32}$`)

func loraAdresseNormal(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	a = strings.NewReplacer("<", "", ">", "", ":", "", " ", "").Replace(a)
	return a
}

func (s *Server) loraVerlauf(c *gin.Context) {
	_, fid, ok := s.loraSitzung(c)
	if !ok {
		return
	}
	mit := loraAdresseNormal(c.Query("mit"))
	if !reLoraAdresse.MatchString(mit) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Funkadresse muss 32 Hex-Zeichen haben."})
		return
	}
	v, err := s.loraVerlaufHolen(c.Request.Context(), fid, mit)
	if err != nil {
		loraFehler(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"verlauf": v})
}

// POST /api/v1/lora/senden {an, text}
func (s *Server) loraSenden(c *gin.Context) {
	_, fid, ok := s.loraSitzung(c)
	if !ok {
		return
	}
	var req struct {
		An   string `json:"an"`
		Text string `json:"text"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	an := loraAdresseNormal(req.An)
	text := strings.TrimSpace(req.Text)
	if !reLoraAdresse.MatchString(an) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Funkadresse muss 32 Hex-Zeichen haben."})
		return
	}
	if text == "" || len(text) > 2000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Text fehlt oder ist zu lang (höchstens 2000 Zeichen – über Funk lieber kurz)."})
		return
	}
	var aus map[string]any
	if err := s.loraAufruf(c.Request.Context(), http.MethodPost, "/senden", map[string]any{"fid": fid, "an": an, "text": text}, &aus, 30*time.Second); err != nil {
		loraFehler(c, err)
		return
	}
	c.JSON(http.StatusOK, aus)
}

func (s *Server) loraAnkuendigen(c *gin.Context) {
	_, fid, ok := s.loraSitzung(c)
	if !ok {
		return
	}
	var aus map[string]any
	if err := s.loraAufruf(c.Request.Context(), http.MethodPost, "/ankuendigen", map[string]any{"fid": fid}, &aus, 15*time.Second); err != nil {
		loraFehler(c, err)
		return
	}
	c.JSON(http.StatusOK, aus)
}

// ── Ereignisse → Push ────────────────────────────────────────────────────────

// loraEreignisseVerfolgen: hört dem Funkdienst zu und meldet eingehende
// Nachrichten per Push und an offene Seiten. Läuft dauerhaft; ohne Dienst
// wird alle 30 s neu geschaut.
func (s *Server) loraEreignisseVerfolgen(ctx context.Context) {
	seq := -1
	for ctx.Err() == nil {
		if !s.loraInstalliert() {
			seq = -1
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			continue
		}
		warten := 25
		if seq < 0 {
			warten = 0 // Erstkontakt: nur den Stand holen, Altes nicht melden
		}
		var e struct {
			Ereignisse []map[string]any `json:"ereignisse"`
			Seq        int              `json:"seq"`
		}
		err := s.loraAufruf(ctx, http.MethodGet, fmt.Sprintf("/ereignisse?nach=%d&warten=%d", max(seq, 0), warten), nil, &e, 40*time.Second)
		if err != nil {
			seq = -1 // Dienst neu gestartet? Zähler beginnt dort von vorn.
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			continue
		}
		if seq >= 0 && e.Seq < seq {
			seq = -1 // Dienst wurde neu gestartet
			continue
		}
		if seq >= 0 {
			for _, ev := range e.Ereignisse {
				s.loraEreignis(ev)
			}
		}
		seq = e.Seq
	}
}

func (s *Server) loraEreignis(ev map[string]any) {
	art, _ := ev["art"].(string)
	fid, _ := ev["fid"].(string)
	if fid == "" {
		return
	}
	sessionMu.RLock()
	sess := sessionStore[fid]
	sessionMu.RUnlock()
	if sess == nil {
		for k, v := range sessionStore {
			if strings.EqualFold(k, fid) {
				sess = v
				break
			}
		}
	}
	switch art {
	case "nachricht":
		von, _ := ev["von"].(string)
		name, _ := ev["name"].(string)
		text, _ := ev["text"].(string)
		wer := name
		if wer == "" && len(von) >= 8 {
			wer = "<" + von[:8] + "…>"
		}
		if r := []rune(text); len(r) > 140 {
			text = string(r[:140]) + "…"
		}
		s.notifyUser(fid, "messages", "📡 Funk: "+wer, text, "/funkchat?mit="+von, "funk", false)
		if s.log != nil {
			s.log.Info("Funknachricht empfangen", zap.String("von", von))
		}
	}
	if sess != nil {
		sess.broadcastWS(gin.H{"type": "funk", "art": art})
	}
}

// ── Admin: Funkeinstellungen ─────────────────────────────────────────────────

type loraFunk struct {
	Aktiv         bool    `json:"aktiv"`
	Port          string  `json:"port"`
	Frequenz      int64   `json:"frequenz"`
	Bandbreite    int64   `json:"bandbreite"`
	SF            int     `json:"sf"`
	CR            int     `json:"cr"`
	Sendeleistung int     `json:"sendeleistung"`
	AirtimeLang   float64 `json:"airtime_lang"`
	AirtimeKurz   float64 `json:"airtime_kurz"`
	TCP           string  `json:"tcp"`
}

func loraStandard() loraFunk {
	return loraFunk{Aktiv: true, Port: "auto", Frequenz: 869525000, Bandbreite: 250000, SF: 9, CR: 5,
		Sendeleistung: 14, AirtimeLang: 9, AirtimeKurz: 25}
}

// loraFunkGrenzen: erlaubte Sendezeit (%) und Leistung (dBm) für einen Kanal
// nach den SRD-Regeln (EU, ERC/REC 70-03). Der Kanal muss ganz im Teilband
// liegen.
func loraFunkGrenzen(frequenz, bandbreite int64) (dutyProzent float64, maxDBm int, band string) {
	lo, hi := frequenz-bandbreite/2, frequenz+bandbreite/2
	drin := func(a, b int64) bool { return lo >= a && hi <= b }
	switch {
	case drin(869400000, 869650000):
		return 10, 22, "869,4–869,65 MHz (10 %, bis 500 mW)" // Heltec V3: höchstens 22 dBm
	case drin(868000000, 868600000):
		return 1, 14, "868,0–868,6 MHz (1 %, 25 mW)"
	case drin(869700000, 870000000):
		return 1, 14, "869,7–870,0 MHz (1 %, 25 mW)"
	case drin(863000000, 868000000), drin(868700000, 869200000):
		return 0.1, 14, "863–868 / 868,7–869,2 MHz (0,1 %, 25 mW)"
	case drin(433050000, 434790000):
		return 10, 10, "433,05–434,79 MHz (10 %, 10 mW)"
	}
	return 0, 0, ""
}

var loraBandbreiten = map[int64]bool{7800: true, 10400: true, 15600: true, 20800: true, 31250: true,
	41700: true, 62500: true, 125000: true, 250000: true, 500000: true}
var reLoraPort = regexp.MustCompile(`^/dev/(serial/by-id/[A-Za-z0-9_.:\-]+|tty(USB|ACM)[0-9]{1,2})$`)
var reLoraTCP = regexp.MustCompile(`^(server:[0-9]{2,5}|client:[A-Za-z0-9.\-]{1,253}:[0-9]{2,5})$`)

func (f *loraFunk) pruefen() error {
	if f.Port == "" {
		f.Port = "auto"
	}
	if f.Port != "auto" && !reLoraPort.MatchString(f.Port) {
		return errors.New("Anschluss: „auto“ oder /dev/serial/by-id/… bzw. /dev/ttyUSB0")
	}
	if !loraBandbreiten[f.Bandbreite] {
		return errors.New("Bandbreite: 7,8 – 500 kHz (LoRa-Stufen)")
	}
	duty, maxDBm, _ := loraFunkGrenzen(f.Frequenz, f.Bandbreite)
	if duty == 0 {
		return errors.New("Frequenz/Bandbreite liegen nicht in einem freigegebenen SRD-Band (863–870 MHz oder 433 MHz)")
	}
	if f.SF < 7 || f.SF > 12 {
		return errors.New("Spreizfaktor: 7 bis 12")
	}
	if f.CR < 5 || f.CR > 8 {
		return errors.New("Kodierrate: 5 bis 8 (4/5 … 4/8)")
	}
	if f.Sendeleistung < 0 || f.Sendeleistung > maxDBm {
		return fmt.Errorf("Sendeleistung: 0 bis %d dBm in diesem Band", maxDBm)
	}
	// 10 % Sicherheitsabstand zur gesetzlichen Grenze.
	if grenze := duty * 0.9; f.AirtimeLang <= 0 || f.AirtimeLang > grenze {
		return fmt.Errorf("Sendezeit (15 min): 0 bis %.2f %% in diesem Band", grenze)
	}
	if f.AirtimeKurz <= 0 || f.AirtimeKurz > 100 {
		return errors.New("Sendezeit (15 s): 0 bis 100 %")
	}
	f.TCP = strings.TrimSpace(f.TCP)
	if f.TCP != "" && !reLoraTCP.MatchString(f.TCP) {
		return errors.New("TCP-Verbindung: leer, server:PORT oder client:HOST:PORT")
	}
	return nil
}

func (s *Server) loraFunkLesen() loraFunk {
	f := loraStandard()
	if b, err := os.ReadFile(filepath.Join(s.loraOrdner(), "funk.json")); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f
}

// GET /api/v1/admin/funk/lora
func (s *Server) adminLoraZeigen(c *gin.Context) {
	f := s.loraFunkLesen()
	duty, maxDBm, band := loraFunkGrenzen(f.Frequenz, f.Bandbreite)
	c.JSON(http.StatusOK, gin.H{"funk": f, "installiert": s.loraInstalliert(),
		"grenzen": gin.H{"sendezeit": duty, "max_dbm": maxDBm, "band": band}})
}

// POST /api/v1/admin/funk/lora – speichern und Funkdienst neu starten.
func (s *Server) adminLoraSetzen(c *gin.Context) {
	var f loraFunk
	if c.ShouldBindJSON(&f) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	if err := f.pruefen(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(s.loraOrdner(), 0o700); err != nil {
		s.internalError(c, err)
		return
	}
	if err := jsonAtomar(filepath.Join(s.loraOrdner(), "funk.json"), f, 0o600); err != nil {
		s.internalError(c, err)
		return
	}
	neu := false
	if s.loraInstalliert() {
		neu = s.loraAufruf(c.Request.Context(), http.MethodPost, "/neustart", map[string]any{}, nil, 5*time.Second) == nil
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "neu_gestartet": neu})
}
