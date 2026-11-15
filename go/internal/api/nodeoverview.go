package api

// Node-Übersicht (R531): Jeder Node beantwortet über das P2P-Protokoll
// "/fundus/nodeinfo/1.0.0" die Frage "Wie geht es dir?" – Revision, Chain,
// Speicher, Uhrzeit, Warnungen. GET /api/v1/nodes/overview fragt alle
// verbundenen Peers parallel und liefert eine Tabelle (nur Heimnetz).
// Abweichungen (andere Revision, Chain-Rückstand, Uhr falsch, Dateispeicher
// aus) fallen so auf einen Blick auf.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

const nodeInfoProtocol = "/fundus/nodeinfo/1.0.0"

var processStart = time.Now()

type nodeInfo struct {
	PeerID         string                 `json:"peer_id"`
	Name           string                 `json:"name"`
	Revision       string                 `json:"revision"`
	SourceFP       string                 `json:"source_fp,omitempty"`
	Time           int64                  `json:"time"`         // Unix-Sekunden laut Uhr des Nodes
	UptimeSec      int64                  `json:"uptime_sec"`
	Height         uint64                 `json:"height"`
	HeadHash       string                 `json:"head_hash,omitempty"`
	Producer       bool                   `json:"producer"`     // Blockproduktion läuft
	Peers          int                    `json:"peers"`
	Storage        map[string]interface{} `json:"storage,omitempty"`
	FileStoreError string                 `json:"filestore_error,omitempty"`
	ChainError     string                 `json:"chain_error,omitempty"`
	ForkNote       string                 `json:"fork_note,omitempty"`
	ClockNote      string                 `json:"clock_note,omitempty"`
	IP             string                 `json:"ip,omitempty"`   // R906: Adresse im Heimnetz
	Disk           *nodeDisk              `json:"disk,omitempty"` // R906: Datenträger des Datenordners
}

type nodeDisk struct {
	FreiGB      float64 `json:"frei_gb"`
	GesamtGB    float64 `json:"gesamt_gb"`
	FreiProzent float64 `json:"frei_prozent"`
}

// lokaleIP (R906): die Adresse, über die der Pi ins Netz geht (Route zum
// Standard-Gateway; es wird nichts gesendet). Ersatzweise die erste private
// IPv4 einer Schnittstelle – ohne Hotspot (10.42.0.1) und Docker.
func lokaleIP() string {
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		a, _ := c.LocalAddr().(*net.UDPAddr)
		c.Close()
		if a != nil && !a.IP.IsLoopback() && a.IP.String() != hotspotGW {
			return a.IP.String()
		}
	}
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || len(i.Name) >= 6 && i.Name[:6] == "docker" {
			continue
		}
		adrs, _ := i.Addrs()
		for _, a := range adrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() && n.IP.String() != hotspotGW {
				return n.IP.String()
			}
		}
	}
	return ""
}

func datentraeger(pfad string) *nodeDisk {
	var st syscall.Statfs_t
	if pfad == "" || syscall.Statfs(pfad, &st) != nil || st.Blocks == 0 {
		return nil
	}
	gb := func(b uint64) float64 { return math.Round(float64(b)*float64(st.Bsize)/1e9*10) / 10 }
	return &nodeDisk{FreiGB: gb(st.Bavail), GesamtGB: gb(st.Blocks),
		FreiProzent: math.Round(float64(st.Bavail)/float64(st.Blocks)*1000) / 10}
}

func (s *Server) buildNodeInfo() nodeInfo {
	ni := nodeInfo{
		Revision:       NodeRevision,
		SourceFP:       SourceFingerprint,
		Time:           time.Now().Unix(),
		UptimeSec:      int64(time.Since(processStart).Seconds()),
		Producer:       ProducerRunning.Load(),
		FileStoreError: FileStoreError,
	}
	if h, err := os.Hostname(); err == nil {
		ni.Name = h
	}
	ni.IP = lokaleIP()
	if s.cfg != nil {
		ni.Disk = datentraeger(s.cfg.DataDir)
	}
	if s.node != nil {
		ni.PeerID = s.node.ID().String()
		ni.Peers = len(s.node.Peers())
		// Nicht Teil der P2PNode-Schnittstelle → per Typprüfung (wie in chain.go).
		if fn, ok := interface{}(s.node).(interface{ LastForkNote() string }); ok {
			ni.ForkNote = fn.LastForkNote()
		}
		if cn, ok := interface{}(s.node).(interface{ LastClockNote() string }); ok {
			ni.ClockNote = cn.LastClockNote()
		}
	}
	if s.chain != nil {
		ni.Height = s.chain.Height()
		head := s.chain.HeadHash()
		ni.HeadHash = fmt.Sprintf("%x", head)
	} else {
		ni.ChainError = ChainInitError
		if ni.ChainError == "" {
			ni.ChainError = "Chain nicht aktiv"
		}
	}
	if s.fileStore != nil {
		st := s.fileStore.Stats()
		ni.Storage = map[string]interface{}{"used_gb": st["used_gb"], "offer_gb": st["offer_gb"], "files": st["files"]}
	}
	return ni
}

// registerNodeInfo: Antwortseite des Protokolls anmelden.
func (s *Server) registerNodeInfo() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(nodeInfoProtocol, func(peerID string, _ []byte) []byte {
		b, _ := json.Marshal(s.buildNodeInfo())
		return b
	})
}

type nodeOverviewRow struct {
	nodeInfo
	Self      bool   `json:"self"`
	SkewSec   int64  `json:"skew_sec"`            // Uhrabweichung gegenüber diesem Node
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// GET /api/v1/nodes/overview – nur Heimnetz (zeigt Infrastruktur).
func (s *Server) nodesOverview(c *gin.Context) {
	if !isLANRequest(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "nur aus dem Heimnetz"})
		return
	}
	rows := []nodeOverviewRow{{nodeInfo: s.buildNodeInfo(), Self: true}}
	if s.node != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Second)
		defer cancel()
		peers := s.node.Peers()
		out := make([]nodeOverviewRow, len(peers))
		var wg sync.WaitGroup
		for i, p := range peers {
			wg.Add(1)
			go func(i int, pid string) {
				defer wg.Done()
				row := nodeOverviewRow{nodeInfo: nodeInfo{PeerID: pid}}
				t0 := time.Now()
				raw, err := s.node.SendAndReceive(ctx, pid, nodeInfoProtocol, []byte("{}"))
				if err != nil || len(raw) == 0 {
					row.Error = "keine Auskunft (kein Fundus-Node, nicht erreichbar oder Version vor R531)"
					out[i] = row
					return
				}
				var ni nodeInfo
				if json.Unmarshal(raw, &ni) != nil {
					row.Error = "unlesbare Antwort"
					out[i] = row
					return
				}
				rtt := time.Since(t0)
				row.nodeInfo = ni
				row.LatencyMs = rtt.Milliseconds()
				// Uhrabweichung: Zeitstempel der Antwort gegen die eigene Uhr zur
				// Mitte der Anfrage (halbe Laufzeit abgezogen).
				mid := t0.Add(rtt / 2).Unix()
				row.SkewSec = ni.Time - mid
				out[i] = row
			}(i, p.String())
		}
		wg.Wait()
		rows = append(rows, out...)
	}
	c.JSON(http.StatusOK, gin.H{"nodes": rows, "time": time.Now().Unix()})
}
