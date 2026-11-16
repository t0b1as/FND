package api

// Diagnose des Hotspots (R911): Was passiert, wenn ein Telefon die
// Anmeldeseite nicht oder spät zeigt? Die Einstellungsseite zeigt
//   - die letzten Aufrufe der Anmeldeseite (Gerät, Adresse, Dauer),
//   - die Messung der Namensauflösung durch den Wächter (dns.json),
//   - das Protokoll des Wächters (Starts, Fehler).
// Gespeichert wird nur im Speicher bzw. unter /run (weg nach Neustart).

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type portalAufruf struct {
	Zeit   int64  `json:"zeit"`
	Geraet string `json:"geraet"` // letzte Stelle der IP + Art des Geräts
	Host   string `json:"host"`
	Pfad   string `json:"pfad"`
	Status int    `json:"status"`
	Ms     int64  `json:"ms"`
}

var (
	portalAufrufeMu sync.Mutex
	portalAufrufe   []portalAufruf
)

func geraeteArt(ua string) string {
	switch {
	case strings.Contains(ua, "CaptiveNetworkSupport"):
		return "iOS-Prüfung"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		return "iOS"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Mac OS"):
		return "Mac"
	case ua == "":
		return "?"
	}
	return "anderes"
}

func portalAufrufMerken(c *gin.Context, d time.Duration) {
	portalAufrufEintragen(c.Request.RemoteAddr, c.Request.UserAgent(), c.Request.Host, c.Request.URL.Path, c.Writer.Status(), d)
}

func portalAufrufEintragen(remote, ua, host, pfad string, status int, d time.Duration) {
	ip, _, _ := net.SplitHostPort(remote)
	if i := strings.LastIndex(ip, "."); i >= 0 {
		ip = "…" + ip[i:]
	}
	a := portalAufruf{Zeit: time.Now().Unix(), Geraet: ip + " " + geraeteArt(ua),
		Host: kiKuerzen(host, 60), Pfad: kiKuerzen(pfad, 60), Status: status, Ms: d.Milliseconds()}
	portalAufrufeMu.Lock()
	portalAufrufe = append(portalAufrufe, a)
	if len(portalAufrufe) > 120 {
		portalAufrufe = portalAufrufe[len(portalAufrufe)-100:]
	}
	portalAufrufeMu.Unlock()
}

func hotspotDiagnose() gin.H {
	h := gin.H{}
	portalAufrufeMu.Lock()
	l := make([]portalAufruf, 0, 40)
	for i := len(portalAufrufe) - 1; i >= 0 && len(l) < 40; i-- {
		l = append(l, portalAufrufe[i])
	}
	portalAufrufeMu.Unlock()
	h["aufrufe"] = l
	if b, err := os.ReadFile("/run/fundus-hotspot/dns.json"); err == nil {
		var d map[string]any
		if json.Unmarshal(b, &d) == nil {
			h["dns"] = d
		}
	}
	if b, err := os.ReadFile("/run/fundus-hotspot/protokoll"); err == nil {
		z := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(z) > 60 {
			z = z[len(z)-60:]
		}
		h["protokoll"] = z
	}
	return h
}
