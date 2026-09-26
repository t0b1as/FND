package api

// Solana-Zugang (RPC): Liste von Endpunkten mit automatischem Ausweichen.
//
// Quelle (in dieser Reihenfolge): Einstellungen (Datei solana-rpc.txt im
// Datenverzeichnis) → FUNDUS_SHOP_SOLANA_RPC (fundus.env, mehrere Einträge mit
// Komma) → öffentlicher Mainnet-Endpunkt. Der erste Eintrag ist bevorzugt;
// drosselt er (429) oder fällt er aus, übernimmt der nächste gesunde. Alle
// 10 Minuten wird geprüft, ob der bevorzugte wieder geht.
//
// Das Netz (mainnet/devnet/testnet) ergibt sich aus dem Genesis-Hash, den der
// Endpunkt liefert – nicht aus dem Adresstext. Eine Liste mit gemischten Netzen
// wird nicht angenommen.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const defaultSolRPC = "https://api.mainnet-beta.solana.com"

// Genesis-Hashes der Solana-Netze.
var solGenesisNet = map[string]string{
	"5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d": "mainnet",
	"EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG": "devnet",
	"4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY": "testnet",
}

type rpcEndpoint struct {
	URL     string
	Net     string // aus dem Genesis-Hash; leer = noch unbekannt
	OK      bool
	LastErr string
	Checked time.Time
}

type solRPCPoolT struct {
	mu     sync.RWMutex
	eps    []*rpcEndpoint
	active int
	source string // "Einstellungen" | "fundus.env" | "Standard"
	path   string // Datei der Einstellungen
	envVal string
	kick   chan struct{}
}

var rpcPool = &solRPCPoolT{kick: make(chan struct{}, 1)}

// parseRPCList: Komma, Leerzeichen oder Zeilenumbruch trennen; nur http(s); ohne Dubletten.
func parseRPCList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' || r == ';' }) {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		if u, err := url.Parse(f); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// initSolRPC lädt die Liste (Einstellungen → fundus.env → Standard) und startet die Prüfschleife.
func (s *Server) initSolRPC() {
	p := rpcPool
	if s.cfg != nil {
		p.envVal = s.cfg.ShopSolanaRPC
		if s.cfg.DataDir != "" {
			p.path = filepath.Join(s.cfg.DataDir, "solana-rpc.txt")
		}
	}
	p.reload()
	go p.loop()
}

func (p *solRPCPoolT) reload() {
	list, src := []string(nil), ""
	if p.path != "" {
		if b, err := os.ReadFile(p.path); err == nil {
			if l := parseRPCList(string(b)); len(l) > 0 {
				list, src = l, "Einstellungen"
			}
		}
	}
	if list == nil {
		if l := parseRPCList(p.envVal); len(l) > 0 {
			list, src = l, "fundus.env"
		}
	}
	if list == nil {
		list, src = []string{defaultSolRPC}, "Standard"
	}
	eps := make([]*rpcEndpoint, len(list))
	for i, u := range list {
		eps[i] = &rpcEndpoint{URL: u, OK: true}
	}
	p.mu.Lock()
	p.eps, p.active, p.source = eps, 0, src
	p.mu.Unlock()
	p.kickNow()
}

// activeSolRPC: der gerade aktive Endpunkt (nie leer).
func activeSolRPC() string {
	p := rpcPool
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.eps) == 0 {
		return defaultSolRPC
	}
	return p.eps[p.active].URL
}

// activeSolNet: Netz des aktiven Endpunkts ("" = noch unbekannt).
func activeSolNet() string {
	p := rpcPool
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.eps) == 0 {
		return ""
	}
	return p.eps[p.active].Net
}

// kickSolRPC: sofortige Prüfung anstoßen (z.B. nach 429/Timeout), nicht blockierend.
func kickSolRPC() { rpcPool.kickNow() }

func (p *solRPCPoolT) kickNow() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *solRPCPoolT) loop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	lastPrefer := time.Now()
	for {
		p.checkActive(time.Since(lastPrefer) > 10*time.Minute)
		if time.Since(lastPrefer) > 10*time.Minute {
			lastPrefer = time.Now()
		}
		select {
		case <-t.C:
		case <-p.kick:
		}
	}
}

// checkActive prüft den aktiven Endpunkt; bei Fehler (oder zur Rückkehr zum
// bevorzugten) wird der erste gesunde in Listenreihenfolge gewählt.
func (p *solRPCPoolT) checkActive(tryPreferred bool) {
	p.mu.RLock()
	eps := append([]*rpcEndpoint(nil), p.eps...)
	active := p.active
	p.mu.RUnlock()
	if len(eps) == 0 {
		return
	}
	if probeEndpoint(eps[active]) && !(tryPreferred && active > 0) {
		return
	}
	for i, ep := range eps {
		if i == active && !eps[active].OK {
			continue
		}
		if i == active || probeEndpoint(ep) {
			p.mu.Lock()
			if len(p.eps) == len(eps) { // Liste inzwischen nicht ersetzt
				p.active = i
			}
			p.mu.Unlock()
			return
		}
	}
}

// probeEndpoint: getHealth (+ einmalig getGenesisHash für das Netz).
func probeEndpoint(ep *rpcEndpoint) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, err := rawSolCall(ctx, ep.URL, "getHealth", nil)
	if err == nil && ep.Net == "" {
		if res, gerr := rawSolCall(ctx, ep.URL, "getGenesisHash", nil); gerr == nil {
			var h string
			if json.Unmarshal(res, &h) == nil {
				ep.Net = solGenesisNet[h]
				if ep.Net == "" {
					ep.Net = "unbekannt"
				}
			}
		}
	}
	ep.Checked = time.Now()
	ep.OK = err == nil
	ep.LastErr = ""
	if err != nil {
		ep.LastErr = err.Error()
	}
	return ep.OK
}

// rawSolCall: minimaler JSON-RPC-Aufruf gegen einen bestimmten Endpunkt.
func rawSolCall(ctx context.Context, rpcURL, method string, params []interface{}) (json.RawMessage, error) {
	body := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, solRPCErr(err, rpcURL)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("gedrosselt (429)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &out) != nil {
		return nil, fmt.Errorf("unlesbare Antwort")
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s", out.Error.Message)
	}
	return out.Result, nil
}

// publicSolRPC: öffentlicher Endpunkt für den BROWSER (Phantom-Zahlung). Nie
// die konfigurierte Adresse – die kann einen API-Schlüssel enthalten.
func publicSolRPC() string {
	switch activeSolNet() {
	case "devnet":
		return "https://api.devnet.solana.com"
	case "testnet":
		return "https://api.testnet.solana.com"
	}
	return defaultSolRPC
}

var reAPIKeyish = regexp.MustCompile(`([?&](api[-_]?key|token|key)=)[^&]+`)

// ── Einstellungen ───────────────────────────────────────────────────────────

// GET /api/v1/admin/solana/rpc – Liste (Schlüssel ausgeblendet), aktiver, Netz, Zustand.
func (s *Server) adminSolRPCGet(c *gin.Context) {
	p := rpcPool
	p.mu.RLock()
	type row struct {
		URL     string `json:"url"`
		Net     string `json:"net"`
		OK      bool   `json:"ok"`
		Active  bool   `json:"active"`
		LastErr string `json:"last_error,omitempty"`
		Checked int64  `json:"checked,omitempty"`
	}
	rows := make([]row, len(p.eps))
	for i, ep := range p.eps {
		rows[i] = row{URL: maskRPC(ep.URL), Net: ep.Net, OK: ep.OK, Active: i == p.active, LastErr: ep.LastErr}
		if !ep.Checked.IsZero() {
			rows[i].Checked = ep.Checked.Unix()
		}
	}
	src := p.source
	p.mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{"endpoints": rows, "source": src, "env_set": parseRPCList(p.envVal) != nil})
}

// POST /api/v1/admin/solana/rpc {endpoints: "…"} – speichern (leer = zurück zu fundus.env).
// Jeder Eintrag wird geprüft: erreichbar und alle im selben Netz.
func (s *Server) adminSolRPCSet(c *gin.Context) {
	var req struct {
		Endpoints string `json:"endpoints"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	p := rpcPool
	if p.path == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "kein Datenverzeichnis konfiguriert"})
		return
	}
	list := parseRPCList(req.Endpoints)
	if strings.TrimSpace(req.Endpoints) == "" {
		_ = os.Remove(p.path)
		p.reload()
		c.JSON(http.StatusOK, gin.H{"ok": true, "note": "Einstellung entfernt – es gilt wieder fundus.env bzw. der Standard"})
		return
	}
	if len(list) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "keine gültige Adresse (https://…) erkannt"})
		return
	}
	nets := map[string]bool{}
	for _, u := range list {
		ep := &rpcEndpoint{URL: u}
		if !probeEndpoint(ep) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "nicht erreichbar: " + maskRPC(u) + " – " + reAPIKeyish.ReplaceAllString(ep.LastErr, "${1}…")})
			return
		}
		nets[ep.Net] = true
	}
	if len(nets) > 1 {
		var n []string
		for k := range nets {
			n = append(n, k)
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "Endpunkte gehören zu verschiedenen Solana-Netzen (" + strings.Join(n, ", ") + ") – bitte nur ein Netz"})
		return
	}
	if err := os.WriteFile(p.path, []byte(strings.Join(list, "\n")+"\n"), 0o600); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	p.reload()
	var net string
	for k := range nets {
		net = k
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": len(list), "net": net})
}

// FirstSolRPC: erster gültiger Eintrag einer Liste (für Stellen, die beim Start
// genau eine Adresse brauchen, z.B. den Zahlungswächter des Shops).
func FirstSolRPC(list string) string {
	if l := parseRPCList(list); len(l) > 0 {
		return l[0]
	}
	return defaultSolRPC
}
