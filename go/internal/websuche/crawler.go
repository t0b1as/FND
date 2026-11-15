package websuche

// Crawler (R896): liest Seiten höflich und legt sie im eigenen Index ab.
//
//   - robots.txt und TDM-Vorbehalt (siehe robots.go)
//   - je Host höchstens eine Anfrage gleichzeitig, frühestens alle Sekunde
//     (oder Crawl-delay), höchstens N Seiten je Host
//   - Bandbreitengrenze (MB je Stunde), auf Wunsch nur nachts (1–6 Uhr)
//   - nur öffentliche Adressen, Port 80/443 – nichts aus dem Heimnetz
//   - nur HTML bis 2 MB; noindex/nofollow werden beachtet
//   - Startseiten von Hand, dazu angeklickte Suchtreffer (Vormerken)
//   - die Warteschlange liegt in der Ablage und übersteht Neustarts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CrawlerEinstellungen: vom Admin festgelegt.
type CrawlerEinstellungen struct {
	Aktiv        bool     `json:"aktiv"`
	Startseiten  []string `json:"startseiten,omitempty"`
	NurNachts    bool     `json:"nur_nachts"`
	MBProStunde  int      `json:"mb_pro_stunde"`
	SeitenJeHost int      `json:"seiten_je_host"`
	Tiefe        int      `json:"tiefe"`        // Linkschritte ab Startseite
	FremdeHosts  bool     `json:"fremde_hosts"` // Links auf andere Hosts folgen
	NeuNachTagen int      `json:"neu_nach_tagen"`
}

// Vorgaben: was gilt, wenn nichts eingestellt ist.
func (e CrawlerEinstellungen) Vorgaben() CrawlerEinstellungen {
	if e.MBProStunde <= 0 {
		e.MBProStunde = 200
	}
	if e.SeitenJeHost <= 0 {
		e.SeitenJeHost = 500
	}
	if e.Tiefe <= 0 {
		e.Tiefe = 3
	}
	if e.NeuNachTagen <= 0 {
		e.NeuNachTagen = 30
	}
	return e
}

const (
	maxSeitenBytes   = 2 << 20
	maxParseBytes    = 768 << 10
	maxWarteschlange = 100000
	hostAbstand      = time.Second
	robotsGueltig    = 24 * time.Hour
	gleichzeitig     = 2
)

type auftrag struct {
	url   string
	tiefe int
	start bool // Startseite oder vorgemerkt: immer lesen, wenn veraltet
}

type hostZustand struct {
	robots   *Robots
	tdm      *TDMRep
	geholt   time.Time
	frei     time.Time // nächste Anfrage frühestens
	beschaef bool
	seiten   int
}

type fehlschlag struct {
	N       int   `json:"n"`
	Erst    int64 `json:"erst"`
	Naechst int64 `json:"naechst"`
}

const (
	fehlVersuche = 3
	fehlTage     = 5
)

// CrawlerStand: für die Anzeige.
type CrawlerStand struct {
	Laeuft        bool     `json:"laeuft"`
	Pause         string   `json:"pause,omitempty"`
	Warteschlange int      `json:"warteschlange"`
	Gelesen       int      `json:"gelesen"`       // seit dem Start
	Aufgenommen   int      `json:"aufgenommen"`   // davon im Index
	Uebersprungen int      `json:"uebersprungen"` // robots, TDM, noindex, kein HTML
	Fehler        int      `json:"fehler"`
	MBStunde      float64  `json:"mb_stunde"`
	Letzte        []string `json:"letzte"`
	LetzterFehler string   `json:"letzter_fehler,omitempty"`
	Hosts         int      `json:"hosts"`
	Entfernt      int      `json:"entfernt"`     // R898: nicht mehr erreichbar
	ImIndex       int      `json:"im_index"`     // R900: Seiten im Index insgesamt (bleibt über Neustarts)
	Unerreichbar  int      `json:"unerreichbar"` // R898: gerade mit Fehlversuchen
}

// Crawler liest Seiten in den Index.
type Crawler struct {
	ix     *Index
	ordner string
	e      CrawlerEinstellungen

	// ErlaubeLokal: nur für Tests (lokaler Testserver).
	ErlaubeLokal bool
	// Jetzt: Uhr (Tests ersetzen sie).
	Jetzt func() time.Time

	// Suchverbund (R897). Alle Rückrufe dürfen nicht blockieren – sie werden
	// unter der Sperre des Crawlers aufgerufen.
	// Zustaendig: liest dieser Pi den Host? nil = alles hier.
	Zustaendig func(host string) bool
	// Abgeben: Adresse gehört einem anderen Pi.
	Abgeben func(u string, tiefe int)
	// Kopie: aufgenommene Seite (für die zweite Ablage).
	Kopie func(d Dokument)
	// Entfernt: Seite fällt aus dem Index (noindex, Vorbehalt, 404).
	Entfernt func(u string)

	// Platz (R898): "" = genug Platz, sonst der Grund zum Anhalten.
	Platz         func() string
	platzGrund    string
	platzGeprueft time.Time

	// R898: nicht erreichbare Seiten – nach 3 Fehlversuchen über mindestens
	// 5 Tage fallen sie aus dem Index.
	fehl           map[string]fehlschlag
	startGelesen   time.Time
	veraltGeprueft time.Time

	mu       sync.Mutex
	schlange []auftrag
	geplant  map[uint64]bool
	hosts    map[string]*hostZustand
	bytes    []struct {
		t time.Time
		n int
	}
	stand  CrawlerStand
	weck   chan struct{} // R899: neue Arbeit weckt einen ruhenden Arbeiter
	client *http.Client
	stopp  context.CancelFunc
	fertig sync.WaitGroup
}

// NeuerCrawler: noch nicht gestartet.
func NeuerCrawler(ix *Index, ordner string, e CrawlerEinstellungen) *Crawler {
	c := &Crawler{ix: ix, ordner: ordner, e: e.Vorgaben(), geplant: map[uint64]bool{}, hosts: map[string]*hostZustand{}, Jetzt: time.Now, fehl: map[string]fehlschlag{}, weck: make(chan struct{}, 1)}
	c.client = &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext:           c.waehlen,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("zu viele Weiterleitungen")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("Weiterleitung auf anderes Protokoll")
			}
			return nil
		},
	}
	return c
}

// oeffentlich: keine Adressen aus Heimnetz, Loopback, Link-local, CGNAT.
func oeffentlich(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1]&0xc0 == 64 { // 100.64.0.0/10
			return false
		}
		if v4[0] == 0 || v4[0] >= 240 {
			return false
		}
	}
	return true
}

func (c *Crawler) waehlen(ctx context.Context, netzwerk, adresse string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(adresse)
	if err != nil {
		return nil, err
	}
	if !c.ErlaubeLokal && port != "80" && port != "443" {
		return nil, errors.New("nur Port 80 und 443")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	for _, ip := range ips {
		if !c.ErlaubeLokal && !oeffentlich(ip.IP) {
			continue
		}
		return d.DialContext(ctx, netzwerk, net.JoinHostPort(ip.IP.String(), port))
	}
	return nil, errors.New("keine öffentliche Adresse")
}

// ---------------------------------------------------------------------------
// Warteschlange

func (c *Crawler) schlangeDatei() string { return filepath.Join(c.ordner, "warteschlange.txt") }

func (c *Crawler) vormerkenGesperrt(a auftrag) bool {
	if len(c.schlange) >= maxWarteschlange {
		return false
	}
	h := urlHash(a.url)
	if c.geplant[h] {
		return false
	}
	c.geplant[h] = true
	select {
	case c.weck <- struct{}{}:
	default:
	}
	if a.start {
		c.schlange = append([]auftrag{a}, c.schlange...) // vorgemerkte zuerst
	} else {
		c.schlange = append(c.schlange, a)
	}
	return true
}

// VormerkenTiefe: Link, den ein anderer Pi gefunden hat (zählt mit Tiefe).
func (c *Crawler) VormerkenTiefe(roh string, tiefe int) bool {
	u, err := url.Parse(strings.TrimSpace(roh))
	if err != nil {
		return false
	}
	l := LinkAufloesen(u, u.String())
	if l == "" || tiefe < 0 || tiefe > c.e.Tiefe {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vormerkenGesperrt(auftrag{url: l, tiefe: tiefe})
}

// Vormerken: Adresse bald lesen (angeklickter Treffer, Startseite).
func (c *Crawler) Vormerken(roh string) bool {
	u, err := url.Parse(strings.TrimSpace(roh))
	if err != nil {
		return false
	}
	l := LinkAufloesen(u, u.String())
	if l == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.vormerkenGesperrt(auftrag{url: l, start: true})
}

// Bandbreite (R900): das Stundenkonto übersteht Neustarts – sonst begänne
// nach jedem Neustart eine frische Stunde und die Grenze liefe ins Leere.
func (c *Crawler) bandbreiteDatei() string { return filepath.Join(c.ordner, "bandbreite.json") }

type bandEintrag struct {
	T int64 `json:"t"`
	N int   `json:"n"`
}

func (c *Crawler) bandbreiteSichern() {
	c.mu.Lock()
	c.bytesStundeGesperrt() // Altes verwerfen
	l := make([]bandEintrag, 0, len(c.bytes))
	for _, b := range c.bytes {
		l = append(l, bandEintrag{b.t.Unix(), b.n})
	}
	c.mu.Unlock()
	b, _ := json.Marshal(l)
	tmp := c.bandbreiteDatei() + ".tmp"
	if os.WriteFile(tmp, b, 0o640) == nil {
		_ = os.Rename(tmp, c.bandbreiteDatei())
	}
}

func (c *Crawler) bandbreiteLaden() {
	b, err := os.ReadFile(c.bandbreiteDatei())
	if err != nil {
		return
	}
	var l []bandEintrag
	if json.Unmarshal(b, &l) != nil {
		return
	}
	c.mu.Lock()
	for _, e := range l {
		c.bytes = append(c.bytes, struct {
			t time.Time
			n int
		}{time.Unix(e.T, 0), e.N})
	}
	c.bytesStundeGesperrt()
	c.mu.Unlock()
}

func (c *Crawler) fehlDatei() string { return filepath.Join(c.ordner, "fehlversuche.json") }

func (c *Crawler) fehlLaden() {
	if b, err := os.ReadFile(c.fehlDatei()); err == nil {
		m := map[string]fehlschlag{}
		if json.Unmarshal(b, &m) == nil {
			c.mu.Lock()
			c.fehl = m
			c.mu.Unlock()
		}
	}
}

func (c *Crawler) fehlSichern() {
	c.mu.Lock()
	grenze := c.Jetzt().Add(-60 * 24 * time.Hour).Unix()
	for u, f := range c.fehl {
		if f.Erst < grenze {
			delete(c.fehl, u) // längst entschieden oder vergessen
		}
	}
	b, _ := json.Marshal(c.fehl)
	c.mu.Unlock()
	tmp := c.fehlDatei() + ".tmp"
	if os.WriteFile(tmp, b, 0o640) == nil {
		_ = os.Rename(tmp, c.fehlDatei())
	}
}

// fehlschlagen: Seite war nicht erreichbar. true = sie fällt jetzt heraus.
func (c *Crawler) fehlschlagen(u string) bool {
	if hat, _ := c.ix.Hat(u); !hat {
		return false // nie aufgenommen – nichts zu entfernen
	}
	jetzt := c.Jetzt()
	c.mu.Lock()
	f := c.fehl[u]
	if f.N == 0 {
		f.Erst = jetzt.Unix()
	}
	f.N++
	// nächster Versuch: nach 2, 4, 6 … Tagen
	f.Naechst = jetzt.Add(time.Duration(2*f.N) * 24 * time.Hour).Unix()
	raus := f.N >= fehlVersuche && jetzt.Sub(time.Unix(f.Erst, 0)) >= fehlTage*24*time.Hour
	if raus {
		delete(c.fehl, u)
		c.stand.Entfernt++
	} else {
		c.fehl[u] = f
	}
	c.mu.Unlock()
	if raus {
		c.weg(u)
	}
	return raus
}

func (c *Crawler) erreicht(u string) {
	c.mu.Lock()
	delete(c.fehl, u)
	c.mu.Unlock()
}

func (c *Crawler) schlangeLaden() {
	f, err := os.Open(c.schlangeDatei())
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	c.mu.Lock()
	defer c.mu.Unlock()
	for sc.Scan() {
		u, t, _ := strings.Cut(sc.Text(), "\t")
		tiefe, _ := strconv.Atoi(t)
		start := strings.HasSuffix(t, "s")
		if start {
			tiefe, _ = strconv.Atoi(strings.TrimSuffix(t, "s"))
		}
		if strings.HasPrefix(u, "http") {
			c.vormerkenGesperrt(auftrag{url: u, tiefe: tiefe})
			if start {
				c.schlange[len(c.schlange)-1].start = true
			}
		}
	}
}

func (c *Crawler) schlangeSichern() error {
	c.mu.Lock()
	kopie := append([]auftrag(nil), c.schlange...)
	c.mu.Unlock()
	tmp := c.schlangeDatei() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, a := range kopie {
		s := ""
		if a.start {
			s = "s"
		}
		fmt.Fprintf(w, "%s\t%d%s\n", a.url, a.tiefe, s)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, c.schlangeDatei())
}

// ---------------------------------------------------------------------------
// Ablauf

// Starten: Arbeiter im Hintergrund.
func (c *Crawler) Starten() {
	c.mu.Lock()
	if c.stopp != nil {
		c.mu.Unlock()
		return
	}
	ctx, stopp := context.WithCancel(context.Background())
	c.stopp = stopp
	c.stand = CrawlerStand{Laeuft: true}
	c.mu.Unlock()
	c.schlangeLaden()
	c.fehlLaden()
	c.bandbreiteLaden()
	for _, s := range c.e.Startseiten {
		c.Vormerken(s)
	}
	c.mu.Lock()
	c.startGelesen = c.Jetzt()
	c.mu.Unlock()
	for i := 0; i < arbeiterZahl(); i++ {
		c.fertig.Add(1)
		go c.arbeiter(ctx)
	}
	c.fertig.Add(1)
	go func() { // Warteschlange regelmäßig sichern
		defer c.fertig.Done()
		// R900: alle 2 Minuten sichern – nach einem Absturz gehen höchstens
		// die Seiten der letzten 2 Minuten verloren (sie werden neu gelesen).
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		runde := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runde++
				_ = c.schlangeSichern()
				c.fehlSichern()
				c.bandbreiteSichern()
				_ = c.ix.Schreiben()
				if runde%3 == 0 {
					_ = c.ix.Pflegen() // R898: verschobenes Zusammenlegen, Verdichten
					c.neuEinplanen()
				}
			}
		}
	}()
}

// Anhalten: Arbeiter beenden, Warteschlange und Index sichern.
func (c *Crawler) Anhalten() {
	c.mu.Lock()
	stopp := c.stopp
	c.stopp = nil
	c.mu.Unlock()
	if stopp == nil {
		return
	}
	stopp()
	c.fertig.Wait()
	_ = c.schlangeSichern()
	c.fehlSichern()
	c.bandbreiteSichern()
	_ = c.ix.Schreiben()
	c.mu.Lock()
	c.stand.Laeuft = false
	c.stand.Pause = ""
	c.mu.Unlock()
}

// Stand: Zustand für die Anzeige.
func (c *Crawler) Stand() CrawlerStand {
	imIndex, _ := c.ix.Stand() // vor der Sperre des Crawlers (Sperrreihenfolge)
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stand
	s.ImIndex = imIndex
	s.Warteschlange = len(c.schlange)
	s.Hosts = len(c.hosts)
	s.Unerreichbar = len(c.fehl)
	s.MBStunde = float64(c.bytesStundeGesperrt()) / 1e6
	s.Letzte = append([]string(nil), c.stand.Letzte...)
	return s
}

func (c *Crawler) bytesStundeGesperrt() int {
	grenze := c.Jetzt().Add(-time.Hour)
	k := c.bytes[:0]
	summe := 0
	for _, b := range c.bytes {
		if b.t.After(grenze) {
			k = append(k, b)
			summe += b.n
		}
	}
	c.bytes = k
	return summe
}

// neuEinplanen (R898): Startseiten einmal am Tag, veraltete Seiten zum
// Neulesen – so fallen auch Seiten auf, die es nicht mehr gibt.
func (c *Crawler) neuEinplanen() {
	jetzt := c.Jetzt()
	c.mu.Lock()
	startFaellig := jetzt.Sub(c.startGelesen) >= 24*time.Hour
	if startFaellig {
		c.startGelesen = jetzt
	}
	platzVoll := c.platzGrund != ""
	wenig := len(c.schlange) < 2000
	c.mu.Unlock()
	if startFaellig {
		for _, s := range c.e.Startseiten {
			c.Vormerken(s)
		}
	}
	if platzVoll || !wenig {
		return
	}
	alt := c.ix.Veraltete(jetzt.Add(-time.Duration(c.e.NeuNachTagen)*24*time.Hour), 300)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range alt {
		if f, ok := c.fehl[u]; ok && f.Naechst > jetzt.Unix() {
			continue // nach einem Fehlversuch erst später wieder
		}
		c.vormerkenGesperrt(auftrag{url: u, tiefe: c.e.Tiefe})
	}
}

// pauseGrund: warum gerade nichts gelesen wird ("" = weiter).
func (c *Crawler) pauseGrundGesperrt() string {
	if c.e.NurNachts {
		if h := c.Jetzt().Hour(); h < 1 || h >= 6 {
			return "wartet auf die Nacht (1–6 Uhr)"
		}
	}
	if c.bytesStundeGesperrt() >= c.e.MBProStunde*1_000_000 {
		return "Bandbreitengrenze erreicht"
	}
	if c.platzGrund != "" { // R899: geprüft in platzPruefen, außerhalb der Sperre
		return c.platzGrund
	}
	if len(c.schlange) == 0 {
		return "nichts zu tun – Startseiten eintragen oder Treffer anklicken"
	}
	return ""
}

// naechsterGesperrt: ersten Auftrag, dessen Host gerade frei ist.
func (c *Crawler) naechsterGesperrt() (auftrag, *hostZustand, bool) {
	jetzt := c.Jetzt()
	for i, a := range c.schlange {
		if i > 2000 {
			break
		}
		u, err := url.Parse(a.url)
		if err != nil {
			c.schlange = append(c.schlange[:i], c.schlange[i+1:]...)
			return auftrag{}, nil, false
		}
		if c.Zustaendig != nil && !c.Zustaendig(u.Host) {
			// gehört (inzwischen) einem anderen Pi im Verbund
			c.schlange = append(c.schlange[:i], c.schlange[i+1:]...)
			delete(c.geplant, urlHash(a.url))
			if c.Abgeben != nil {
				c.Abgeben(a.url, a.tiefe)
			}
			return auftrag{}, nil, false
		}
		h := c.hosts[u.Host]
		if h == nil {
			h = &hostZustand{}
			c.hosts[u.Host] = h
		}
		if h.beschaef || jetzt.Before(h.frei) {
			continue
		}
		c.schlange = append(c.schlange[:i], c.schlange[i+1:]...)
		delete(c.geplant, urlHash(a.url))
		if h.seiten >= c.e.SeitenJeHost && !a.start {
			c.stand.Uebersprungen++
			return auftrag{}, nil, false
		}
		h.beschaef = true
		return a, h, true
	}
	return auftrag{}, nil, false
}

// arbeiterZahl (R899): auf kleinen Pis (unter 1,5 GB RAM) nur ein Arbeiter.
func arbeiterZahl() int {
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var kb int64
		for _, z := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(z, "MemTotal:") {
				fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(z, "MemTotal:")), "%d", &kb)
			}
		}
		if kb > 0 && kb < 1_500_000 {
			return 1
		}
	}
	return gleichzeitig
}

// platzPruefen (R899): außerhalb der Sperre des Crawlers – die Prüfung
// liest Dateigrößen und wartet dabei womöglich auf den Index; die Anzeige
// des Standes soll davon nicht hängen.
func (c *Crawler) platzPruefen() {
	if c.Platz == nil {
		return
	}
	c.mu.Lock()
	faellig := c.Jetzt().Sub(c.platzGeprueft) > 30*time.Second
	if faellig {
		c.platzGeprueft = c.Jetzt()
	}
	c.mu.Unlock()
	if !faellig {
		return
	}
	g := c.Platz()
	c.mu.Lock()
	c.platzGrund = g
	c.mu.Unlock()
}

// sicherLesen (R899): ein Fehler beim Lesen einer Seite darf den Node nicht
// mitreißen – er wird gemeldet, der Crawler macht weiter.
func (c *Crawler) sicherLesen(ctx context.Context, a auftrag, h *hostZustand) (verz time.Duration) {
	defer func() {
		if r := recover(); r != nil {
			c.fehler(fmt.Sprintf("interner Fehler bei %s: %v", kuerzen(a.url, 100), r))
			verz = hostAbstand
		}
	}()
	return c.lesen(ctx, a, h)
}

func (c *Crawler) arbeiter(ctx context.Context) {
	defer c.fertig.Done()
	for ctx.Err() == nil {
		c.platzPruefen()
		c.mu.Lock()
		grund := c.pauseGrundGesperrt()
		c.stand.Pause = grund
		var a auftrag
		var h *hostZustand
		ok := false
		if grund == "" {
			a, h, ok = c.naechsterGesperrt()
		}
		c.mu.Unlock()
		if !ok {
			warte := 300 * time.Millisecond
			if grund != "" {
				warte = 20 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-c.weck:
			case <-time.After(warte):
			}
			continue
		}
		verz := c.sicherLesen(ctx, a, h)
		c.mu.Lock()
		h.beschaef = false
		if verz < hostAbstand {
			verz = hostAbstand
		}
		h.frei = c.Jetzt().Add(verz)
		c.mu.Unlock()
	}
}

// weg: Seite aus dem Index nehmen und dem Verbund melden.
func (c *Crawler) weg(u string) {
	c.ix.Loeschen(u)
	if c.Entfernt != nil {
		c.mu.Lock()
		c.Entfernt(u)
		c.mu.Unlock()
	}
}

func (c *Crawler) fehler(msg string) {
	c.mu.Lock()
	c.stand.Fehler++
	c.stand.LetzterFehler = msg
	c.mu.Unlock()
}

func (c *Crawler) zaehleBytes(n int) {
	c.mu.Lock()
	c.bytes = append(c.bytes, struct {
		t time.Time
		n int
	}{c.Jetzt(), n})
	c.mu.Unlock()
}

// holen: GET mit Grenze; gibt Kopf und Inhalt zurück.
func (c *Crawler) holen(ctx context.Context, adresse string, max int64) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adresse, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", BotName+"/1.0 (+Fundus-Node; beachtet robots.txt und TDMRep)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
	req.Header.Set("Accept-Language", "de,en;q=0.7")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	c.zaehleBytes(len(b) + 500)
	if err != nil {
		return resp, nil, err
	}
	if int64(len(b)) > max {
		return resp, nil, errors.New("zu groß")
	}
	return resp, b, nil
}

// hostRegeln: robots.txt und tdmrep.json, einmal am Tag.
func (c *Crawler) hostRegeln(ctx context.Context, u *url.URL, h *hostZustand) {
	if h.robots != nil && c.Jetzt().Sub(h.geholt) < robotsGueltig {
		return
	}
	basis := u.Scheme + "://" + u.Host
	resp, b, err := c.holen(ctx, basis+"/robots.txt", 512<<10)
	switch {
	case err != nil && resp == nil:
		h.robots = &Robots{AllesVerboten: true}
	case resp.StatusCode >= 500:
		h.robots = &Robots{AllesVerboten: true}
	case resp.StatusCode >= 400 || err != nil:
		h.robots = &Robots{}
	default:
		h.robots = RobotsLesen(string(b))
	}
	h.tdm = nil
	if resp, b, err := c.holen(ctx, basis+"/.well-known/tdmrep.json", 256<<10); err == nil && resp.StatusCode == http.StatusOK {
		h.tdm = TDMRepLesen(b)
	}
	h.geholt = c.Jetzt()
	if h.robots.AllesVerboten {
		h.geholt = c.Jetzt().Add(-robotsGueltig + time.Hour) // in einer Stunde neu versuchen
	}
}

// lesen: eine Seite holen, prüfen, aufnehmen, Links vormerken.
func (c *Crawler) lesen(ctx context.Context, a auftrag, h *hostZustand) time.Duration {
	u, err := url.Parse(a.url)
	if err != nil {
		return 0
	}
	if hat, wann := c.ix.Hat(a.url); hat && c.Jetzt().Sub(wann) < time.Duration(c.e.NeuNachTagen)*24*time.Hour {
		return 0 // noch frisch
	}
	c.hostRegeln(ctx, u, h)
	verz := h.robots.Verzoegerung
	pfad := u.EscapedPath()
	if u.RawQuery != "" {
		pfad += "?" + u.RawQuery
	}
	ueberspringen := func() time.Duration {
		c.mu.Lock()
		c.stand.Uebersprungen++
		c.mu.Unlock()
		return verz
	}
	if h.robots.AllesVerboten { // Server gestört – zählt als nicht erreichbar, nicht als Verbot
		c.fehlschlagen(a.url)
		return verz
	}
	if !h.robots.Erlaubt(pfad) || h.tdm.Vorbehalt(u.EscapedPath()) {
		c.weg(a.url)
		return ueberspringen()
	}
	resp, b, err := c.holen(ctx, a.url, maxSeitenBytes)
	c.mu.Lock()
	c.stand.Gelesen++
	h.seiten++
	c.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			c.fehler(a.url + ": " + kuerzen(err.Error(), 120))
			c.fehlschlagen(a.url) // R898
		}
		return verz
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		c.erreicht(a.url)
		c.weg(a.url) // gibt es nicht mehr – sofort heraus
		return verz
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		c.fehlschlagen(a.url)
		return 2 * time.Minute // Host bremst – Abstand vergrößern
	}
	if resp.StatusCode >= 500 {
		c.fehlschlagen(a.url)
		return verz
	}
	c.erreicht(a.url)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		c.weg(a.url) // nicht (mehr) öffentlich
		return verz
	}
	if resp.StatusCode != http.StatusOK {
		return verz
	}
	typ, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if typ != "text/html" && typ != "application/xhtml+xml" {
		return ueberspringen()
	}
	endURL := resp.Request.URL
	if strings.TrimSpace(resp.Header.Get("tdm-reservation")) == "1" {
		c.weg(a.url)
		return ueberspringen()
	}
	xr := strings.ToLower(strings.Join(resp.Header.Values("X-Robots-Tag"), ","))
	if len(b) > maxParseBytes { // R899: Speicher schonen – der Text steht fast immer vorn
		b = b[:maxParseBytes]
	}
	s := SeiteLesen(string(b), endURL)
	noindex := s.NoIndex || strings.Contains(xr, "noindex") || strings.Contains(xr, "none") || s.TDMVorbehalt
	nofollow := s.NoFollow || strings.Contains(xr, "nofollow") || strings.Contains(xr, "none")
	ziel := endURL.String()
	if Normieren(ziel) != Normieren(a.url) {
		c.weg(a.url) // R898: alte Adresse leitet weiter – nur das Ziel bleibt im Index
	}
	if s.Kanonisch != "" && Normieren(s.Kanonisch) != Normieren(ziel) {
		if k, err := url.Parse(s.Kanonisch); err == nil && k.Host == endURL.Host {
			c.mu.Lock()
			c.vormerkenGesperrt(auftrag{url: s.Kanonisch, tiefe: a.tiefe})
			c.mu.Unlock()
			noindex = true // die kanonische Fassung wird aufgenommen
		}
	}
	if noindex {
		c.weg(a.url)
		ueberspringen()
	} else if len(s.Text) >= 50 || s.Titel != "" {
		if err := c.ix.Hinzufuegen(Dokument{URL: ziel, Titel: s.Titel, Beschreibung: s.Beschreibung, Text: s.Text, Sprache: s.Sprache}); err != nil {
			c.fehler("Index: " + err.Error())
		} else {
			c.mu.Lock()
			if c.Kopie != nil {
				c.Kopie(Dokument{URL: ziel, Titel: s.Titel, Beschreibung: s.Beschreibung, Text: s.Text, Sprache: s.Sprache, Zeit: c.Jetzt().Unix()})
			}
			c.stand.Aufgenommen++
			c.stand.Letzte = append([]string{ziel}, c.stand.Letzte...)
			if len(c.stand.Letzte) > 10 {
				c.stand.Letzte = c.stand.Letzte[:10]
			}
			c.mu.Unlock()
		}
	}
	if !nofollow && a.tiefe < c.e.Tiefe {
		c.mu.Lock()
		for _, l := range s.Links {
			lu, err := url.Parse(l)
			if err != nil {
				continue
			}
			if lu.Host != endURL.Host && !c.e.FremdeHosts {
				continue
			}
			if c.Zustaendig != nil && lu.Host != endURL.Host && !c.Zustaendig(lu.Host) {
				if c.Abgeben != nil {
					c.Abgeben(l, a.tiefe+1)
				}
				continue
			}
			c.vormerkenGesperrt(auftrag{url: l, tiefe: a.tiefe + 1})
		}
		c.mu.Unlock()
	}
	return verz
}
