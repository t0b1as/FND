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
	"context"
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
)

type presenceEntry struct {
	FundusID string    `json:"fundus_id"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen"`
	Name     string    `json:"name,omitempty"`
}

var (
	presenceMu       sync.Mutex
	presenceMap      = map[string]*presenceEntry{}
	presenceSenderTS = map[string]time.Time{} // letzte Absender-Zeit je Fundus-ID (nur Reihenfolge)
)

// PresencePullProtocol: Nodes fragen sich gegenseitig, wen sie online sehen.
// GossipSub erreicht Nodes hinter NAT (nur Relay-Verbindung) nicht; dieser
// Abruf läuft über SendAndReceive, das Relay-Verbindungen erlaubt – und holt
// nach einem Neustart sofort alles nach. Übertragen wird das ALTER in Sekunden,
// keine Uhrzeit (falsche Uhren verfälschen so nichts).
const PresencePullProtocol = "/fundus/presence-pull/1.0.0"

type presencePullItem struct {
	FundusID string `json:"f"`
	Online   bool   `json:"o"`
	AgeSec   int64  `json:"a"`
	// Signierter Namenseintrag (optional, wird beim Empfänger geprüft)
	Name    string `json:"n,omitempty"`
	NameTS  int64  `json:"nt,omitempty"`
	NameSig string `json:"ns,omitempty"`
	Pub     string `json:"p,omitempty"`
}

func (s *Server) registerPresence() {
	if s.node == nil {
		return
	}
	s.node.SetTopicHandler(presenceTopic, s.handlePresence)
	s.node.RegisterProtocol(PresencePullProtocol, func(peerID string, data []byte) []byte {
		items := presenceSnapshot()
		for i := range items {
			if r, ok := s.nameRecordOf(items[i].FundusID); ok {
				items[i].Name, items[i].NameTS, items[i].NameSig, items[i].Pub = r.Name, r.TS, r.Sig, r.Pub
			}
		}
		out, _ := json.Marshal(items)
		return out
	})
	go s.presencePullLoop()
	go s.presenceAnnounceLoop()
}

// Aktiv = eine API-Anfrage der Sitzung in diesem Zeitraum.
const presenceActive = 15 * time.Minute

// presenceAnnounceLoop: der Node meldet jede Minute alle aktiven, sichtbaren
// Sitzungen als online. Früher hing "online" an einem Zeitgeber im Browser –
// Handy-Browser stoppen Hintergrund-Tabs, Desktop-Browser bremsen sie, und wer
// den Tab schloss, verschwand nach 6 min, obwohl er angemeldet war. So
// erschien dieselbe Person auf manchen Nodes und auf anderen nicht.
func (s *Server) presenceAnnounceLoop() {
	time.Sleep(15 * time.Second)
	for {
		sessionMu.RLock()
		list := make([]*Session, 0, len(sessionStore))
		for _, sess := range sessionStore {
			list = append(list, sess)
		}
		sessionMu.RUnlock()
		now := time.Now()
		for _, sess := range list {
			if sess == nil || sess.identity == nil || sess.messenger == nil || sess.presenceHidden.Load() {
				continue
			}
			if now.Sub(time.Unix(sess.lastActive.Load(), 0)) > presenceActive {
				continue
			}
			recordPresence(strings.ToLower(sess.identity.FundusID), true, now)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = sess.messenger.PublishPresence(ctx, true)
			cancel()
		}
		time.Sleep(time.Minute)
	}
}

func presenceSnapshot() []presencePullItem {
	presenceMu.Lock()
	defer presenceMu.Unlock()
	out := make([]presencePullItem, 0, len(presenceMap))
	for _, e := range presenceMap {
		age := time.Since(e.LastSeen)
		if age < presenceOnline {
			out = append(out, presencePullItem{FundusID: e.FundusID, Online: e.Online, AgeSec: int64(age / time.Second)})
		}
	}
	return out
}

func (s *Server) presencePullLoop() {
	time.Sleep(20 * time.Second) // Verbindungen aufbauen lassen
	for {
		s.pullPresenceOnce()
		time.Sleep(time.Minute)
	}
}

func (s *Server) pullPresenceOnce() {
	if s.node == nil {
		return
	}
	for _, pid := range s.node.Peers() {
		go func(peerID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			resp, err := s.node.SendAndReceive(ctx, peerID, PresencePullProtocol, []byte("PULL"))
			if err != nil {
				return
			}
			var items []presencePullItem
			if json.Unmarshal(resp, &items) != nil {
				return
			}
			now := time.Now()
			for _, it := range items {
				fid := strings.ToLower(it.FundusID)
				if len(fid) != 42 || !strings.HasPrefix(fid, "0x") || it.AgeSec < 0 || it.AgeSec > int64(presenceOnline/time.Second) {
					continue
				}
				recordPresence(fid, it.Online, now.Add(-time.Duration(it.AgeSec)*time.Second))
				s.nameFromPresence(fid, it.Pub, it.Name, it.NameSig, it.NameTS)
			}
		}(pid.String())
	}
}

func (s *Server) handlePresence(data []byte) {
	var m struct {
		FundusID string    `json:"fundus_id"`
		Ed25519  string    `json:"ed25519_pub"`
		Online   bool      `json:"online"`
		TS       time.Time `json:"ts"`
		Name     string    `json:"name"`
		NameTS   int64     `json:"name_ts"`
		NameSig  string    `json:"name_sig"`
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
	// Absender-Uhr NICHT für die Frische verwenden: ein Pi ohne Zeitserver
	// (kein Internet, Handy-Hotspot) geht falsch – seine Meldungen wurden
	// verworfen bzw. er verwarf die der anderen. Frische = Empfangszeit; die
	// Absender-Uhr dient nur der Reihenfolge SEINER eigenen Meldungen.
	presenceMu.Lock()
	last, seen := presenceSenderTS[fid]
	if seen && !m.TS.IsZero() && m.TS.Before(last) {
		presenceMu.Unlock()
		return // ältere Meldung desselben Absenders (Reihenfolge vertauscht)
	}
	if !m.TS.IsZero() {
		presenceSenderTS[fid] = m.TS
	}
	presenceMu.Unlock()
	recordPresence(fid, m.Online, time.Now())
	s.nameFromPresence(fid, m.Ed25519, m.Name, m.NameSig, m.NameTS) // signierter Name
}

// recordPresence: seenAt ist IMMER eine Zeit der eigenen Uhr (Empfang bzw.
// "jetzt minus gemeldetes Alter" beim Abruf von anderen Nodes).
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
	known := len(presenceMap)
	presenceMu.Unlock()
	for i := range out {
		if r, ok := s.nameRecordOf(out[i].FundusID); ok {
			out[i].Name = r.Name
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	// Nur TATSÄCHLICH verbundene Peers zählen (Peers() enthält auch längst
	// getrennte und fremde IPFS-Knoten – die Zahl wirkte zu gut).
	peers := 0
	if ns, ok := s.node.(interface{ NATStatus() map[string]interface{} }); ok && s.node != nil {
		if v, ok := ns.NATStatus()["connected_peers"].(int); ok {
			peers = v
		}
	}
	c.JSON(http.StatusOK, gin.H{"online": out, "peers": peers, "known": known})
}
