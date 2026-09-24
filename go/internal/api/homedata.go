package api

// Hybrid-Datenzugriff über Nodes hinweg.
//
// Jeder Nutzer hat einen Heim-Node (keydir-Feld "home_peer": der Node, an dem er
// sich zuerst angemeldet hat). Dort liegen seine Daten dauerhaft. Meldet er sich
// an einem ANDEREN Node an ("Gast-Node"), gilt:
//
//   - Lesen: der Gast-Node holt Partnerprofil und Messenger-Verlauf vom Heim-Node
//     und hält sie als Lese-Cache lokal vor (schnell, auch bei kurzen Ausfällen).
//   - Schreiben: Änderungen werden lokal übernommen UND signiert an den Heim-Node
//     weitergereicht; der Heim-Node prüft die Signatur und speichert.
//
// Sicherheit: Jede Anfrage ist mit dem Ed25519-Schlüssel des Nutzers signiert.
// identity.Verify prüft zusätzlich, dass der Schlüssel zur FundusID gehört – ein
// Node kann also nur im Namen eines Nutzers handeln, der bei ihm GERADE
// angemeldet ist. Zeitfenster ±5 min, Schreibzugriffe mit Wiederholungssperre.
// Der Messenger-Verlauf bleibt Ende-zu-Ende beim Nutzer verschlüsselt: der
// Heim-Node speichert nur Chiffretext-Zeilen.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/messenger"
	"github.com/fundus/node/internal/storage"
)

const HomeDataProtocol = "/fundus/homedata/1.0.0"

const (
	homeReqWindow   = 5 * time.Minute
	homeHistoryMax  = 1000 // Zeilen pro Konversation beim Abgleich
	homePullEvery   = 30 * time.Second
	homeCallTimeout = 10 * time.Second
)

var errNoRemoteHome = errors.New("kein entfernter Heim-Node")

type homeReq struct {
	Op   string          `json:"op"`
	FID  string          `json:"fid"`
	Pub  string          `json:"pub"`  // Ed25519-PubKey (hex) – muss zur FundusID gehören
	Peer string          `json:"peer,omitempty"`
	TS   int64           `json:"ts"`
	Data json.RawMessage `json:"data,omitempty"`
	Sig  string          `json:"sig"`
}

type homeResp struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

func homeSignBytes(r *homeReq) []byte {
	h := sha256.Sum256(r.Data)
	return []byte(r.Op + "|" + strings.ToLower(r.FID) + "|" + strconv.FormatInt(r.TS, 10) + "|" + r.Peer + "|" + hex.EncodeToString(h[:]))
}

var (
	homeSeenMu sync.Mutex
	homeSeen   = map[string]time.Time{} // Signatur → Zeitpunkt (Wiederholungssperre)
	homePullMu sync.Mutex
	homePulled = map[string]time.Time{} // fid|peer → letzter Abgleich
)

func (s *Server) registerHomeData() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(HomeDataProtocol, func(peerID string, data []byte) []byte {
		out, _ := json.Marshal(s.handleHomeData(peerID, data))
		return out
	})
}

// ── Heim-Node-Seite ─────────────────────────────────────────────────────────

func (s *Server) handleHomeData(from string, data []byte) homeResp {
	var r homeReq
	if json.Unmarshal(data, &r) != nil {
		return homeResp{Error: "ungültige Anfrage"}
	}
	fid := strings.ToLower(r.FID)
	if d := time.Since(time.Unix(r.TS, 0)); d > homeReqWindow || d < -homeReqWindow {
		return homeResp{Error: "Zeitstempel außerhalb des Fensters"}
	}
	if !identity.Verify(homeSignBytes(&r), r.Sig, fid, r.Pub) {
		return homeResp{Error: "Signatur ungültig"}
	}
	write := strings.HasSuffix(r.Op, ".put") || strings.HasSuffix(r.Op, ".append")
	if write {
		homeSeenMu.Lock()
		now := time.Now()
		for k, t := range homeSeen {
			if now.Sub(t) > 2*homeReqWindow {
				delete(homeSeen, k)
			}
		}
		_, dup := homeSeen[r.Sig]
		homeSeen[r.Sig] = now
		homeSeenMu.Unlock()
		if dup {
			return homeResp{Error: "Wiederholte Anfrage"}
		}
		if !s.isHomeFor(fid) {
			return homeResp{Error: "dieser Node ist nicht der Heim-Node"}
		}
	}
	dataDir := ""
	if s.cfg != nil {
		dataDir = s.cfg.DataDir
	}

	switch r.Op {
	case "partner.get":
		rec, err := s.store.Get(storage.RecordPartnerProfile, "profile:"+fid)
		if err != nil || rec == nil {
			return homeResp{OK: true}
		}
		raw, _ := json.Marshal(rec.Data)
		return homeResp{OK: true, Data: raw}

	case "partner.put":
		var d map[string]any
		if json.Unmarshal(r.Data, &d) != nil || d == nil {
			return homeResp{Error: "ungültige Profildaten"}
		}
		d["owner_wallet"] = fid
		delete(d, "cached_from")
		if err := s.store.Put(&storage.Record{ID: "profile:" + fid, Type: storage.RecordPartnerProfile, Data: d}); err != nil {
			return homeResp{Error: err.Error()}
		}
		return homeResp{OK: true}

	case "history.pull":
		if dataDir == "" {
			return homeResp{Error: "kein DataDir"}
		}
		convs := []string{r.Peer}
		if r.Peer == "" {
			convs = messenger.RawConversations(dataDir, fid)
		}
		out := map[string][]string{}
		for _, p := range convs {
			if lines, err := messenger.RawLines(dataDir, fid, p, homeHistoryMax); err == nil && len(lines) > 0 {
				out[p] = lines
			}
		}
		raw, _ := json.Marshal(out)
		return homeResp{OK: true, Data: raw}

	case "history.append":
		var d struct {
			Line string `json:"line"`
		}
		if json.Unmarshal(r.Data, &d) != nil || r.Peer == "" || dataDir == "" {
			return homeResp{Error: "ungültige Verlaufszeile"}
		}
		if err := messenger.AppendRawLine(dataDir, fid, r.Peer, d.Line); err != nil {
			return homeResp{Error: err.Error()}
		}
		return homeResp{OK: true}
	}
	return homeResp{Error: "unbekannte Operation"}
}

// isHomeFor: ist dieser Node der Heim-Node der FundusID?
func (s *Server) isHomeFor(fid string) bool {
	if s.node == nil {
		return false
	}
	me := s.node.ID().String()
	rec, err := s.store.Get(storage.RecordKeyDir, "keydir:"+fid)
	if err != nil || rec == nil {
		return false
	}
	if hp, _ := rec.Data["home_peer"].(string); hp != "" {
		return hp == me
	}
	pid, _ := rec.Data["peer_id"].(string) // Altbestand ohne home_peer
	return pid == me
}

// homePeerOf liefert den ENTFERNTEN Heim-Node einer FundusID ("" = lokal/unbekannt).
func (s *Server) homePeerOf(fid string) string {
	if s.node == nil || s.store == nil {
		return ""
	}
	rec, err := s.store.Get(storage.RecordKeyDir, "keydir:"+strings.ToLower(fid))
	if err != nil || rec == nil {
		return ""
	}
	hp, _ := rec.Data["home_peer"].(string)
	if hp == "" || hp == s.node.ID().String() {
		return ""
	}
	return hp
}

// ── Gast-Node-Seite ─────────────────────────────────────────────────────────

// homeCall signiert eine Anfrage mit der Identität der Session und schickt sie
// an den Heim-Node des Nutzers.
func (s *Server) homeCall(sess *Session, op, peer string, data any) (json.RawMessage, error) {
	if sess == nil || sess.identity == nil || s.node == nil {
		return nil, errNoRemoteHome
	}
	fid := strings.ToLower(sess.identity.FundusID)
	home := s.homePeerOf(fid)
	if home == "" {
		return nil, errNoRemoteHome
	}
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	r := homeReq{Op: op, FID: fid, Pub: sess.identity.PublicKeyHex, Peer: peer, TS: time.Now().Unix(), Data: raw}
	sig, err := sess.identity.Sign(homeSignBytes(&r))
	if err != nil {
		return nil, err
	}
	r.Sig = sig
	body, _ := json.Marshal(r)
	ctx, cancel := context.WithTimeout(context.Background(), homeCallTimeout)
	defer cancel()
	respRaw, err := s.node.SendAndReceive(ctx, home, HomeDataProtocol, body)
	if err != nil {
		return nil, err
	}
	var resp homeResp
	if json.Unmarshal(respRaw, &resp) != nil {
		return nil, errors.New("ungültige Antwort vom Heim-Node")
	}
	if !resp.OK {
		return nil, errors.New(resp.Error)
	}
	return resp.Data, nil
}

// pullHistoryFromHome mischt den Verlauf vom Heim-Node in den lokalen Cache
// (gedrosselt: höchstens alle homePullEvery pro Konversation).
func (s *Server) pullHistoryFromHome(sess *Session, peer string) {
	if sess == nil || sess.history == nil || sess.identity == nil {
		return
	}
	key := strings.ToLower(sess.identity.FundusID) + "|" + strings.ToLower(peer)
	homePullMu.Lock()
	if t, ok := homePulled[key]; ok && time.Since(t) < homePullEvery {
		homePullMu.Unlock()
		return
	}
	homePulled[key] = time.Now()
	homePullMu.Unlock()

	raw, err := s.homeCall(sess, "history.pull", peer, nil)
	if err != nil {
		if err != errNoRemoteHome && s.log != nil {
			s.log.Debug("Heim-Verlauf nicht abrufbar", zap.Error(err))
		}
		return
	}
	var convs map[string][]string
	if json.Unmarshal(raw, &convs) != nil {
		return
	}
	for p, lines := range convs {
		if n, err := sess.history.Merge(p, lines); err == nil && n > 0 && s.log != nil {
			s.log.Info("Verlauf vom Heim-Node übernommen", zap.Int("nachrichten", n))
		}
	}
}

// forwardHistoryToHome schickt einen neuen Verlaufseintrag an den Heim-Node.
func (s *Server) forwardHistoryToHome(sess *Session, e messenger.HistoryEntry) {
	if sess == nil || sess.history == nil || s.homePeerOfSession(sess) == "" {
		return
	}
	line, err := sess.history.EncryptEntry(e)
	if err != nil {
		return
	}
	go func() {
		if _, err := s.homeCall(sess, "history.append", e.PeerID, map[string]string{"line": line}); err != nil && s.log != nil {
			s.log.Debug("Verlauf nicht an Heim-Node übertragen", zap.Error(err))
		}
	}()
}

func (s *Server) homePeerOfSession(sess *Session) string {
	if sess == nil || sess.identity == nil {
		return ""
	}
	return s.homePeerOf(sess.identity.FundusID)
}

// pullPartnerFromHome holt das Partnerprofil vom Heim-Node und legt es lokal
// als Lese-Cache ab. true, wenn ein Profil übernommen wurde.
func (s *Server) pullPartnerFromHome(sess *Session, profileID string) bool {
	raw, err := s.homeCall(sess, "partner.get", "", nil)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var d map[string]any
	if json.Unmarshal(raw, &d) != nil || d == nil {
		return false
	}
	d["cached_from"] = s.homePeerOfSession(sess)
	return s.store.Put(&storage.Record{ID: profileID, Type: storage.RecordPartnerProfile, Data: d}) == nil
}

// pushPartnerToHome überträgt ein geändertes Partnerprofil an den Heim-Node.
func (s *Server) pushPartnerToHome(sess *Session, data map[string]any) {
	if s.homePeerOfSession(sess) == "" {
		return
	}
	cp := make(map[string]any, len(data))
	for k, v := range data {
		if k != "cached_from" {
			cp[k] = v
		}
	}
	go func() {
		if _, err := s.homeCall(sess, "partner.put", "", cp); err != nil && s.log != nil {
			s.log.Warn("Partnerprofil nicht an Heim-Node übertragen", zap.Error(err))
		}
	}()
}
