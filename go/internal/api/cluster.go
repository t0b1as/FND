package api

// Lastverteilung ohne Liste (R671).
//
// Die Nodes kennen sich über das P2P-Netz bereits. Statt die Empfänger von Hand
// zu pflegen, fragt der öffentliche Node seine Nachbarn direkt: Wer bist du, wo
// bist du über HTTP erreichbar, und was verkraftest du? Daraus schreibt er die
// Empfängerliste selbst.
//
// ── Die Sicherheitsfrage, die hier drinsteckt ──────────────────────────────
//
// Der Verteiler reicht ALLE Anfragen weiter, auch die Anmeldung – also E-Mail
// und Passwort im Klartext an den Empfänger. Wer Empfänger wird, kann damit
// Zugangsdaten mitlesen. Eine Erkennung, die jeden Node des Netzes annimmt,
// wäre deshalb grob fahrlässig: Es genügte, einen Node ins Netz zu stellen und
// sich als leistungsfähig auszugeben.
//
// Angenommen wird ein Nachbar deshalb nur, wenn eine von zwei Bedingungen
// erfüllt ist:
//
//   1. Er liegt im GLEICHEN Netzabschnitt wie wir (dasselbe /24). Das sind die
//      eigenen Geräte im Heimnetz – der übliche Fall, ganz ohne Einrichtung.
//   2. Er weist ein gemeinsames Geheimnis nach (FUNDUS_CLUSTER_KEY). Damit
//      lassen sich auch Pis an anderen Standorten einbinden, aber eben nur
//      solche, denen man dieses Geheimnis gegeben hat.
//
// Die Peer-Kennung selbst prüft libp2p kryptografisch, eine eigene Signatur
// braucht es dafür also nicht.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const clusterProtocol = "/fundus/cluster-info/1.0.0"

// clusterInfo ist die Selbstauskunft eines Nodes.
type clusterInfo struct {
	NodeID   string   `json:"node_id"`
	HTTPPort int      `json:"http_port"`
	Adressen []string `json:"adressen"` // eigene IP-Adressen
	Modell   string   `json:"modell"`
	Gewicht  int      `json:"gewicht"`
	Nachweis string   `json:"nachweis,omitempty"` // HMAC über die Aufgabe
	TS       int64    `json:"ts"`
}

// clusterKey: gemeinsames Geheimnis, leer = nur Heimnetz.
func (s *Server) clusterKey() string {
	return strings.TrimSpace(os.Getenv("FUNDUS_CLUSTER_KEY"))
}

// eigeneAdressen sammelt die IP-Adressen dieses Rechners (ohne Loopback).
func eigeneAdressen() []string {
	out := []string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				ip := ipn.IP.To4()
				if ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
					out = append(out, ip.String())
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// gleichesNetz: Liegen zwei Adressen im selben /24?
func gleichesNetz(a, b string) bool {
	ia, ib := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if ia == nil || ib == nil {
		return false
	}
	return ia[0] == ib[0] && ia[1] == ib[1] && ia[2] == ib[2]
}

// nachweis bildet den HMAC über eine Aufgabe – beide Seiten brauchen dasselbe
// Geheimnis, es wandert dabei nie über die Leitung.
func nachweis(key, aufgabe string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(aufgabe))
	return hex.EncodeToString(m.Sum(nil))
}

// registerClusterProtocol beantwortet die Selbstauskunft.
func (s *Server) registerClusterProtocol() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(clusterProtocol, func(peerID string, data []byte) []byte {
		var frage struct {
			Aufgabe string `json:"aufgabe"`
		}
		_ = json.Unmarshal(data, &frage)

		info := clusterInfo{
			NodeID:   s.nodeID(),
			HTTPPort: 3000,
			Adressen: eigeneAdressen(),
			Modell:   erkanntesProfil.Modell,
			Gewicht:  erkanntesProfil.BalanceAnteil,
			TS:       time.Now().Unix(),
		}
		if s.cfg != nil && s.cfg.Port > 0 {
			info.HTTPPort = s.cfg.Port
		}
		if info.Gewicht < 1 {
			info.Gewicht = 1
		}
		if k := s.clusterKey(); k != "" && frage.Aufgabe != "" {
			info.Nachweis = nachweis(k, frage.Aufgabe)
		}
		raw, _ := json.Marshal(info)
		return raw
	})
}

// clusterSammeln fragt alle verbundenen Nachbarn und schreibt die Liste.
func (s *Server) clusterSammeln(ctx context.Context) {
	if s.node == nil {
		return
	}
	meine := eigeneAdressen()
	key := s.clusterKey()

	// Eigene Adresse immer aufnehmen: Der öffentliche Node soll seinen Teil
	// der Last mittragen, sonst verteilt er nur und tut selbst nichts.
	port := 3000
	if s.cfg != nil && s.cfg.Port > 0 {
		port = s.cfg.Port
	}
	gew := erkanntesProfil.BalanceAnteil
	if gew < 1 {
		gew = 1
	}
	zeilen := []string{fmt.Sprintf("127.0.0.1:%d gewicht=%d", port, gew)}
	gefunden, abgelehnt := 0, 0

	for _, p := range s.node.Peers() {
		pid := p.String()
		aufgabe := fmt.Sprintf("%s|%s|%d", s.nodeID(), pid, time.Now().Unix()/300)
		frage, _ := json.Marshal(map[string]string{"aufgabe": aufgabe})

		c2, abbr := context.WithTimeout(ctx, 4*time.Second)
		antwort, err := s.node.SendAndReceive(c2, pid, clusterProtocol, frage)
		abbr()
		if err != nil {
			continue // kennt das Protokoll nicht oder ist nicht erreichbar
		}
		var info clusterInfo
		if json.Unmarshal(antwort, &info) != nil || info.HTTPPort <= 0 {
			continue
		}

		// Erreichbare Adresse im eigenen Netzabschnitt suchen.
		ziel := ""
		for _, a := range info.Adressen {
			for _, m := range meine {
				if gleichesNetz(a, m) {
					ziel = a
					break
				}
			}
			if ziel != "" {
				break
			}
		}
		imNetz := ziel != ""

		// Außerhalb des eigenen Netzes nur mit nachgewiesenem Geheimnis.
		if !imNetz {
			if key == "" || info.Nachweis == "" ||
				!hmac.Equal([]byte(info.Nachweis), []byte(nachweis(key, aufgabe))) {
				abgelehnt++
				continue
			}
			if len(info.Adressen) == 0 {
				abgelehnt++
				continue
			}
			ziel = info.Adressen[0]
		}

		g := info.Gewicht
		if g < 1 {
			g = 1
		}
		if g > 64 {
			g = 64
		}
		// Peer-Kennung mitschreiben (R672): Ohne sie kann der Verteiler den
		// verschlüsselten Tunnel nicht nutzen und müsste unverschlüsselt
		// weiterreichen. Sie stammt von libp2p und ist damit geprüft.
		zeilen = append(zeilen, fmt.Sprintf("%s:%d gewicht=%d peer=%s",
			ziel, info.HTTPPort, g, pid))
		gefunden++
	}

	sort.Strings(zeilen[1:])
	inhalt := "# Von der Selbsterkennung geschrieben (R671) – NICHT von Hand ändern.\n" +
		"# Stand: " + time.Now().Format(time.RFC3339) + "\n" +
		strings.Join(zeilen, "\n") + "\n"
	pfad := filepath.Join(s.balanceDir(), "balance-peers.auto.txt")
	if err := os.WriteFile(pfad, []byte(inhalt), 0o640); err != nil {
		if s.log != nil {
			s.log.Warn("Empfängerliste konnte nicht geschrieben werden", zap.Error(err))
		}
		return
	}
	if s.log != nil {
		s.log.Info("Lastverteilung: Nachbarn erkannt",
			zap.Int("empfaenger", gefunden+1), zap.Int("abgelehnt", abgelehnt))
	}
}

// clusterLoop hält die Liste aktuell.
func (s *Server) clusterLoop(ctx context.Context) {
	s.registerClusterProtocol()
	// Kurz warten, bis die ersten P2P-Verbindungen stehen.
	select {
	case <-time.After(20 * time.Second):
	case <-ctx.Done():
		return
	}
	for {
		s.clusterSammeln(ctx)
		select {
		case <-time.After(2 * time.Minute):
		case <-ctx.Done():
			return
		}
	}
}

// GET /api/v1/admin/cluster – was die Selbsterkennung gefunden hat.
func (s *Server) clusterGet(c *gin.Context) {
	pfad := filepath.Join(s.balanceDir(), "balance-peers.auto.txt")
	roh, err := os.ReadFile(pfad)
	eintraege := []gin.H{}
	if err == nil {
		for _, z := range strings.Split(string(roh), "\n") {
			z = strings.TrimSpace(z)
			if z == "" || strings.HasPrefix(z, "#") {
				continue
			}
			adr, rest, _ := strings.Cut(z, " ")
			g := 1
			if _, nach, ok := strings.Cut(rest, "gewicht="); ok {
				fmt.Sscanf(nach, "%d", &g)
			}
			eintraege = append(eintraege, gin.H{"adresse": adr, "gewicht": g})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"peers":      eintraege,
		"geheimnis":  s.clusterKey() != "",
		"eigene_ips": eigeneAdressen(),
	})
}
