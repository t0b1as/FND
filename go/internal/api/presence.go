package api

// Wer ist im Netz online? (Messenger)
//
// Jeder offene Messenger meldet sich per GossipSub auf "fundus.msg.presence"
// (alle 2 min, beim Schließen offline). Bisher wurden diese Meldungen gesendet,
// aber von niemandem ausgewertet – der Online-Punkt der Kontakte blieb grau.
//
// Eine Meldung zählt nur, wenn die Fundus-ID zum mitgeschickten Ed25519-
// Schlüssel passt (FundusID = BLAKE3(pub)[:20]) – erfundene IDs fallen heraus.
// Wer nicht erscheinen will, schaltet im Messenger "Für andere sichtbar" ab.

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"lukechampine.com/blake3"
)

const (
	presenceTopic  = "fundus.msg.presence"
	presenceOnline = 6 * time.Minute  // so lange gilt eine Online-Meldung
	presenceMaxAge = 10 * time.Minute // ältere Meldungen verwerfen
)

type presenceEntry struct {
	FundusID string    `json:"fundus_id"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen"`
}

var (
	presenceMu  sync.Mutex
	presenceMap = map[string]*presenceEntry{}
)

func (s *Server) registerPresence() {
	if s.node == nil {
		return
	}
	s.node.SetTopicHandler(presenceTopic, s.handlePresence)
}

func (s *Server) handlePresence(data []byte) {
	var m struct {
		FundusID string    `json:"fundus_id"`
		Ed25519  string    `json:"ed25519_pub"`
		Online   bool      `json:"online"`
		TS       time.Time `json:"ts"`
	}
	if json.Unmarshal(data, &m) != nil {
		return
	}
	fid := strings.ToLower(strings.TrimSpace(m.FundusID))
	pub, err := hex.DecodeString(m.Ed25519)
	if err != nil || len(pub) != 32 || len(fid) != 42 {
		return
	}
	h := blake3.Sum256(pub)
	if "0x"+hex.EncodeToString(h[:20]) != fid {
		return // Fundus-ID passt nicht zum Schlüssel
	}
	now := time.Now()
	if m.TS.IsZero() || now.Sub(m.TS) > presenceMaxAge || m.TS.After(now.Add(2*time.Minute)) {
		return
	}
	recordPresence(fid, m.Online, m.TS)
}

func recordPresence(fid string, online bool, ts time.Time) {
	presenceMu.Lock()
	defer presenceMu.Unlock()
	e := presenceMap[fid]
	if e == nil {
		e = &presenceEntry{FundusID: fid}
		presenceMap[fid] = e
	}
	if ts.Before(e.LastSeen) {
		return // ältere Meldung überholt keine neuere
	}
	e.Online, e.LastSeen = online, ts
	// Aufräumen: lange nicht Gesehene vergessen.
	for k, v := range presenceMap {
		if time.Since(v.LastSeen) > 24*time.Hour {
			delete(presenceMap, k)
		}
	}
}

// GET /api/v1/messenger/online – aktuell online gemeldete Nutzer (ohne mich).
func (s *Server) messengerOnline(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	me := strings.ToLower(sess.identity.FundusID)
	presenceMu.Lock()
	out := make([]presenceEntry, 0, len(presenceMap))
	for _, e := range presenceMap {
		if e.FundusID != me && e.Online && time.Since(e.LastSeen) < presenceOnline {
			out = append(out, *e)
		}
	}
	presenceMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	c.JSON(http.StatusOK, gin.H{"online": out})
}
