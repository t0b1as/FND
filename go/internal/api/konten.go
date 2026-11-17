package api

// Konten auf diesem Node (R930).
//
// Der Node merkt sich, welche Konten sich hier angemeldet haben: erste und
// letzte Anmeldung, Anzahl, Zustimmung zu den Nutzungsbedingungen und – sobald
// bekannt – die Wallet-Adresse. Gespeichert wird nur die öffentliche Fundus-ID
// (und Adresse), nie E-Mail oder Passwort; die kennt der Node nicht.
//
// Die Nutzungsbedingungen werden seit R930 je Konto bestätigt: nach der
// ersten Anmeldung (und nach einer neuen Fassung) – nicht mehr beim ersten
// Seitenaufruf eines Browsers.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type kontoEintrag struct {
	Erste       int64  `json:"erste"`
	Letzte      int64  `json:"letzte"`
	Anmeldungen int    `json:"anmeldungen"`
	TOSVersion  string `json:"tos_version,omitempty"`
	TOSZeit     int64  `json:"tos_zeit,omitempty"`
	Wallet      string `json:"wallet,omitempty"`
}

var (
	kontenMu    sync.Mutex
	kontenDaten map[string]*kontoEintrag
)

func (s *Server) kontenPfad() string { return filepath.Join(s.cfg.DataDir, "konten.json") }

// kontenLaden: unter kontenMu aufrufen.
func (s *Server) kontenLaden() map[string]*kontoEintrag {
	if kontenDaten != nil {
		return kontenDaten
	}
	kontenDaten = map[string]*kontoEintrag{}
	if b, err := os.ReadFile(s.kontenPfad()); err == nil {
		_ = json.Unmarshal(b, &kontenDaten)
		if kontenDaten == nil {
			kontenDaten = map[string]*kontoEintrag{}
		}
	}
	return kontenDaten
}

// kontenSchreiben: unter kontenMu aufrufen.
func (s *Server) kontenSchreiben() {
	b, err := json.MarshalIndent(kontenDaten, "", " ")
	if err != nil {
		return
	}
	tmp := s.kontenPfad() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.kontenPfad())
	}
}

// kontoAnmeldungVermerken: bei jeder Anmeldung (identityDerive).
func (s *Server) kontoAnmeldungVermerken(fid, wallet string) {
	fid = strings.ToLower(fid)
	if fid == "" {
		return
	}
	kontenMu.Lock()
	defer kontenMu.Unlock()
	k := s.kontenLaden()
	e := k[fid]
	jetzt := time.Now().Unix()
	if e == nil {
		e = &kontoEintrag{Erste: jetzt}
		k[fid] = e
	}
	e.Letzte = jetzt
	e.Anmeldungen++
	if wallet != "" {
		e.Wallet = wallet
	}
	s.kontenSchreiben()
}

func (s *Server) kontoWalletVermerken(fid, wallet string) {
	fid = strings.ToLower(fid)
	if fid == "" || wallet == "" {
		return
	}
	kontenMu.Lock()
	defer kontenMu.Unlock()
	e := s.kontenLaden()[fid]
	if e != nil && e.Wallet != wallet {
		e.Wallet = wallet
		s.kontenSchreiben()
	}
}

var tosVersionMuster = func(v string) bool {
	if v == "" || len(v) > 16 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// GET /api/v1/konto/tos?version=v1 → {angemeldet, akzeptiert, version, zeit}
func (s *Server) kontoTOSZeigen(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusOK, gin.H{"angemeldet": false, "akzeptiert": false})
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	v := c.Query("version")
	kontenMu.Lock()
	e := s.kontenLaden()[fid]
	var ver string
	var zeit int64
	if e != nil {
		ver, zeit = e.TOSVersion, e.TOSZeit
	}
	kontenMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"angemeldet": true, "akzeptiert": ver != "" && (v == "" || v == ver), "version": ver, "zeit": zeit})
}

// POST /api/v1/konto/tos {version}
func (s *Server) kontoTOSAkzeptieren(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if c.ShouldBindJSON(&req) != nil || !tosVersionMuster(req.Version) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Version."})
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	kontenMu.Lock()
	k := s.kontenLaden()
	e := k[fid]
	jetzt := time.Now().Unix()
	if e == nil { // Sitzung von vor R930 (wiederhergestellt): nachtragen
		e = &kontoEintrag{Erste: jetzt, Letzte: jetzt}
		k[fid] = e
	}
	e.TOSVersion, e.TOSZeit = req.Version, jetzt
	if w := sess.identity.ChainAddr(); w != "" {
		e.Wallet = w
	}
	s.kontenSchreiben()
	kontenMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /api/v1/admin/konten – Übersicht für den Betreiber.
func (s *Server) adminKonten(c *gin.Context) {
	type zeile struct {
		FID         string `json:"fid"`
		Name        string `json:"name"`
		Erste       int64  `json:"erste"`
		Letzte      int64  `json:"letzte"`
		Anmeldungen int    `json:"anmeldungen"`
		TOSVersion  string `json:"tos_version,omitempty"`
		TOSZeit     int64  `json:"tos_zeit,omitempty"`
		Wallet      string `json:"wallet,omitempty"`
		FND         string `json:"fnd,omitempty"` // Guthaben in FND, wenn Adresse bekannt
		Angemeldet  bool   `json:"angemeldet"`    // Sitzung besteht (Cookie gültig)
		Online      bool   `json:"online"`        // Aktivität in den letzten 5 Minuten
		Aktiv       int64  `json:"aktiv,omitempty"`
		Geraete     int    `json:"geraete"` // gültige Sitzungs-Cookies
	}
	// Sitzungen
	type sitz struct {
		aktiv  int64
		wallet string
	}
	sitzungen := map[string]sitz{}
	geraete := map[string]int{}
	sessionMu.Lock()
	for fid, sess := range sessionStore {
		if sess == nil {
			continue
		}
		w := ""
		if sess.identity != nil {
			w = sess.identity.ChainAddr()
		}
		sitzungen[strings.ToLower(fid)] = sitz{aktiv: sess.lastActive.Load(), wallet: w}
	}
	for _, fid := range sessionTokens {
		geraete[strings.ToLower(fid)]++
	}
	sessionMu.Unlock()

	kontenMu.Lock()
	k := s.kontenLaden()
	geaendert := false
	for fid, si := range sitzungen { // angemeldet seit vor R930: nachtragen
		e := k[fid]
		if e == nil {
			e = &kontoEintrag{Erste: si.aktiv, Letzte: si.aktiv}
			k[fid] = e
			geaendert = true
		}
		if si.wallet != "" && e.Wallet != si.wallet {
			e.Wallet = si.wallet
			geaendert = true
		}
	}
	if geaendert {
		s.kontenSchreiben()
	}
	liste := make([]zeile, 0, len(k))
	for fid, e := range k {
		liste = append(liste, zeile{FID: fid, Erste: e.Erste, Letzte: e.Letzte, Anmeldungen: e.Anmeldungen,
			TOSVersion: e.TOSVersion, TOSZeit: e.TOSZeit, Wallet: e.Wallet})
	}
	kontenMu.Unlock()

	jetzt := time.Now().Unix()
	online := 0
	for i := range liste {
		z := &liste[i]
		z.Name = s.displayNameFor(z.FID)
		if si, ok := sitzungen[z.FID]; ok {
			z.Angemeldet = true
			z.Aktiv = si.aktiv
			z.Online = si.aktiv > 0 && jetzt-si.aktiv < 300
		}
		z.Geraete = geraete[z.FID]
		if z.Online {
			online++
		}
		if z.Wallet != "" {
			if bal, ok := s.fndBalance(z.Wallet); ok && bal != nil {
				z.FND = fndDezimal(bal.String())
			}
		}
	}
	sort.Slice(liste, func(a, b int) bool {
		if liste[a].Online != liste[b].Online {
			return liste[a].Online
		}
		return liste[a].Letzte > liste[b].Letzte
	})
	c.JSON(http.StatusOK, gin.H{"konten": liste, "gesamt": len(liste), "online": online, "angemeldet": len(sitzungen)})
}

// fndDezimal: uFND (1 FND = 1e9 uFND, chain.UFNDPerFND) → „12.5“.
func fndDezimal(roh string) string {
	roh = strings.TrimLeft(roh, "0")
	if roh == "" {
		return "0"
	}
	for len(roh) <= 9 {
		roh = "0" + roh
	}
	ganz, rest := roh[:len(roh)-9], strings.TrimRight(roh[len(roh)-9:], "0")
	if len(rest) > 2 {
		rest = strings.TrimRight(rest[:2], "0")
	}
	if rest == "" {
		return ganz
	}
	return ganz + "." + rest
}
