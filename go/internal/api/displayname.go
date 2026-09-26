package api

// Anzeigename je Fundus-ID.
//
// Der Name ist ein vom Inhaber SIGNIERTER Eintrag (Name, Zeitpunkt, Signatur
// über "fundus-name-v1|<id>|<name>|<ts>"). Nur wer die ID besitzt, kann ihn
// setzen; jeder Node prüft die Signatur, bevor er ihn übernimmt oder anzeigt.
// Verbreitung: mit der (ebenfalls signierten) Präsenzmeldung und dem
// Präsenz-Abruf; jeder Node speichert geprüfte Namen dauerhaft (auch wenn die
// Person später offline ist). In Nachrichten reist der Name zusätzlich
// verschlüsselt in der Nutzlast (Payload.SenderName).

import (
	"encoding/hex"
	"encoding/base64"
	"crypto/sha256"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/storage"
)

type nameRecord struct {
	FID  string `json:"fid"`
	Name string `json:"name"`
	TS   int64  `json:"ts"`
	Pub  string `json:"pub"`
	Sig  string `json:"sig"`
	// Profilbild: nur die PRÜFSUMME ist signiert; das Bild selbst (≤16 KB) reist
	// nicht mit jeder Präsenz, sondern wird bei Bedarf von einem Peer geholt und
	// gegen die Prüfsumme geprüft.
	AvatarHash string `json:"avatar_hash,omitempty"`
	Avatar     []byte `json:"-"`
}

// nameSigData: v1 (ohne Bild) bleibt gültig; mit Bild v2 inkl. Prüfsumme.
func nameSigData(fid, name string, ts int64) []byte {
	return []byte(fmt.Sprintf("fundus-name-v1|%s|%s|%d", strings.ToLower(fid), name, ts))
}

func profileSigData(fid, name string, ts int64, avatarHash string) []byte {
	if avatarHash == "" {
		return nameSigData(fid, name, ts)
	}
	return []byte(fmt.Sprintf("fundus-name-v2|%s|%s|%d|%s", strings.ToLower(fid), name, ts, avatarHash))
}

const maxAvatarBytes = 16 * 1024

// avatarHashOf: Prüfsumme eines Profilbilds (128 Bit, hex).
func avatarHashOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// avatarMime prüft das Bildformat an den ersten Bytes (nur JPEG/PNG/WebP).
func avatarMime(b []byte) string {
	switch {
	case len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) > 8 && string(b[1:4]) == "PNG":
		return "image/png"
	case len(b) > 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

func (r nameRecord) valid() bool {
	if r.FID == "" || r.Pub == "" || r.Sig == "" || r.TS <= 0 {
		return false
	}
	if r.Name != "" {
		if _, err := sanitizeDisplayName(r.Name); err != nil {
			return false
		}
	}
	if r.AvatarHash != "" && len(r.AvatarHash) != 32 {
		return false
	}
	if len(r.Avatar) > 0 && (len(r.Avatar) > maxAvatarBytes || avatarHashOf(r.Avatar) != r.AvatarHash || avatarMime(r.Avatar) == "") {
		return false
	}
	return identity.Verify(profileSigData(r.FID, r.Name, r.TS, r.AvatarHash), r.Sig, r.FID, r.Pub)
}

// sanitizeDisplayName: 1–32 Zeichen, keine Steuerzeichen, kein "0x…" (sähe
// sonst wie die Adresse einer anderen Person aus).
func sanitizeDisplayName(n string) (string, error) {
	n = strings.Join(strings.Fields(n), " ")
	if n == "" {
		return "", nil
	}
	if !utf8.ValidString(n) || utf8.RuneCountInString(n) > 32 {
		return "", errors.New("Name: höchstens 32 Zeichen")
	}
	for _, r := range n {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return "", errors.New("Name enthält unzulässige Zeichen")
		}
	}
	if strings.HasPrefix(strings.ToLower(n), "0x") {
		return "", errors.New("Name darf nicht mit „0x“ beginnen (sähe wie eine Adresse aus)")
	}
	return n, nil
}

func nameRecordID(fid string) string { return "displayname:" + strings.ToLower(fid) }

// storeNameRecord übernimmt einen Namenseintrag, wenn er gültig und neuer ist.
func (s *Server) storeNameRecord(r nameRecord) bool {
	if s.store == nil || !r.valid() {
		return false
	}
	r.FID = strings.ToLower(r.FID)
	if cur, ok := s.nameRecordOf(r.FID); ok {
		switch {
		case cur.TS > r.TS:
			return false
		case cur.TS == r.TS:
			// Gleicher Eintrag: nur übernehmen, wenn er das fehlende Bild nachliefert.
			if !(len(cur.Avatar) == 0 && len(r.Avatar) > 0 && cur.AvatarHash == r.AvatarHash) {
				return false
			}
		}
		if len(r.Avatar) == 0 && r.AvatarHash != "" && r.AvatarHash == cur.AvatarHash {
			r.Avatar = cur.Avatar // Bild bleibt, nur Name/Zeit neu
		}
	}
	data := map[string]any{"fid": r.FID, "name": r.Name, "ts": r.TS, "pub": r.Pub, "sig": r.Sig}
	if r.AvatarHash != "" {
		data["avatar_hash"] = r.AvatarHash
		if len(r.Avatar) > 0 {
			data["avatar"] = base64.StdEncoding.EncodeToString(r.Avatar)
		}
	}
	return s.store.Put(&storage.Record{ID: nameRecordID(r.FID), Type: storage.RecordDisplayName, Data: data}) == nil
}

// nameRecordOf lädt einen Namenseintrag und prüft ihn (manipulierte werden ignoriert).
func (s *Server) nameRecordOf(fid string) (nameRecord, bool) {
	if s.store == nil {
		return nameRecord{}, false
	}
	rec, err := s.store.Get(storage.RecordDisplayName, nameRecordID(fid))
	if err != nil || rec == nil {
		return nameRecord{}, false
	}
	r := nameRecord{}
	r.FID, _ = rec.Data["fid"].(string)
	r.Name, _ = rec.Data["name"].(string)
	r.Pub, _ = rec.Data["pub"].(string)
	r.Sig, _ = rec.Data["sig"].(string)
	r.AvatarHash, _ = rec.Data["avatar_hash"].(string)
	if av, _ := rec.Data["avatar"].(string); av != "" {
		r.Avatar, _ = base64.StdEncoding.DecodeString(av)
	}
	switch v := rec.Data["ts"].(type) {
	case float64:
		r.TS = int64(v)
	case int64:
		r.TS = v
	case int:
		r.TS = int64(v)
	}
	if !r.valid() {
		return nameRecord{}, false
	}
	return r, true
}

// POST /api/v1/identity/name {name} – eigenen Anzeigenamen setzen (leer = entfernen).
func (s *Server) identitySetName(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		Name   string  `json:"name"`
		Avatar *string `json:"avatar"` // fehlt = unverändert, "" = entfernen, sonst Data-URL/Base64
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	name, err := sanitizeDisplayName(req.Name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	ts := time.Now().Unix()
	cur, hasCur := s.nameRecordOf(fid)
	if hasCur && cur.TS >= ts {
		ts = cur.TS + 1 // falsch gehende Uhr: trotzdem "neuer"
	}
	var avatar []byte
	avatarHash := ""
	switch {
	case req.Avatar == nil && hasCur: // unverändert
		avatar, avatarHash = cur.Avatar, cur.AvatarHash
	case req.Avatar != nil && *req.Avatar != "":
		raw := *req.Avatar
		if i := strings.Index(raw, ","); strings.HasPrefix(raw, "data:") && i > 0 {
			raw = raw[i+1:]
		}
		b, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if derr != nil || len(b) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Profilbild nicht lesbar"})
			return
		}
		if len(b) > maxAvatarBytes {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Profilbild zu groß (%d KB, höchstens 16 KB)", len(b)/1024)})
			return
		}
		if avatarMime(b) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Profilbild: nur JPEG, PNG oder WebP"})
			return
		}
		avatar, avatarHash = b, avatarHashOf(b)
	}
	sig, err := sess.identity.Sign(profileSigData(fid, name, ts, avatarHash))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	r := nameRecord{FID: fid, Name: name, TS: ts, Pub: sess.identity.PublicKeyHex, Sig: sig, AvatarHash: avatarHash, Avatar: avatar}
	if !s.storeNameRecord(r) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Profil konnte nicht gespeichert werden"})
		return
	}
	if sess.messenger != nil {
		sess.messenger.SetDisplayName(name, ts, sig, avatarHash)
		if !sess.presenceHidden.Load() {
			_ = sess.messenger.PublishPresence(c.Request.Context(), true) // sofort verbreiten
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": name, "avatar_hash": avatarHash})
}

// GET /api/v1/identity/names?ids=0x…,0x… – geprüfte Anzeigenamen nachschlagen.
func (s *Server) identityNames(c *gin.Context) {
	out := map[string]string{}
	avatars := map[string]string{}
	for i, id := range strings.Split(c.Query("ids"), ",") {
		if i >= 200 {
			break
		}
		id = strings.ToLower(strings.TrimSpace(id))
		if len(id) != 42 {
			continue
		}
		if r, ok := s.nameRecordOf(id); ok {
			if r.Name != "" {
				out[id] = r.Name
			}
			if r.AvatarHash != "" {
				avatars[id] = r.AvatarHash
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"names": out, "avatars": avatars})
}

// loadOwnName setzt beim Aufbau der Sitzung den gespeicherten eigenen Namen.
func (s *Server) loadOwnName(sess *Session) {
	if sess == nil || sess.identity == nil || sess.messenger == nil {
		return
	}
	if r, ok := s.nameRecordOf(sess.identity.FundusID); ok {
		sess.messenger.SetDisplayName(r.Name, r.TS, r.Sig, r.AvatarHash)
	}
}

// nameFromPresence übernimmt einen mitgeschickten Namenseintrag (Präsenz/Abruf).
func (s *Server) nameFromPresence(fid, pub, name, sig string, ts int64, avatarHash string) {
	if sig == "" || pub == "" {
		return
	}
	s.storeNameRecord(nameRecord{FID: fid, Name: name, TS: ts, Pub: pub, Sig: sig, AvatarHash: avatarHash})
}

func (s *Server) ownDisplayName(sess *Session) string {
	if sess == nil || sess.identity == nil {
		return ""
	}
	if r, ok := s.nameRecordOf(sess.identity.FundusID); ok {
		return r.Name
	}
	return ""
}

func (s *Server) ownAvatarHash(sess *Session) string {
	if sess == nil || sess.identity == nil {
		return ""
	}
	if r, ok := s.nameRecordOf(sess.identity.FundusID); ok {
		return r.AvatarHash
	}
	return ""
}

// AvatarProtocol: Profilbild eines Nutzers von einem Peer holen (Anfrage =
// Fundus-ID, Antwort = Bildbytes oder leer). Geprüft wird beim Empfänger.
const AvatarProtocol = "/fundus/avatar/1.0.0"

func (s *Server) registerAvatarProtocol() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(AvatarProtocol, func(peerID string, data []byte) []byte {
		if r, ok := s.nameRecordOf(strings.ToLower(strings.TrimSpace(string(data)))); ok && len(r.Avatar) > 0 {
			return r.Avatar
		}
		return nil
	})
}

// fetchAvatar holt ein fehlendes Profilbild von verbundenen Nodes (parallel,
// erstes passendes gewinnt) und speichert es, wenn es zur Prüfsumme passt.
func (s *Server) fetchAvatar(r nameRecord) ([]byte, bool) {
	if s.node == nil || r.AvatarHash == "" {
		return nil, false
	}
	peers := s.node.Peers()
	if len(peers) > 12 {
		peers = peers[:12]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	got := make(chan []byte, len(peers))
	for _, p := range peers {
		go func(pid string) {
			b, err := s.node.SendAndReceive(ctx, pid, AvatarProtocol, []byte(r.FID))
			if err == nil && len(b) > 0 && len(b) <= maxAvatarBytes && avatarHashOf(b) == r.AvatarHash && avatarMime(b) != "" {
				got <- b
				return
			}
			got <- nil
		}(p.String())
	}
	for range peers {
		select {
		case b := <-got:
			if b != nil {
				r.Avatar = b
				s.storeNameRecord(r)
				return b, true
			}
		case <-ctx.Done():
			return nil, false
		}
	}
	return nil, false
}

// GET /api/v1/identity/avatar/:fid – Profilbild (geprüft). ?v=<Prüfsumme> in
// der URL macht langes Cachen sicher.
func (s *Server) identityAvatar(c *gin.Context) {
	r, ok := s.nameRecordOf(strings.ToLower(c.Param("fid")))
	if !ok || r.AvatarHash == "" {
		c.Status(http.StatusNotFound)
		return
	}
	img := r.Avatar
	if len(img) == 0 {
		if b, ok := s.fetchAvatar(r); ok {
			img = b
		}
	}
	if len(img) == 0 {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("ETag", `"`+r.AvatarHash+`"`)
	c.Data(http.StatusOK, avatarMime(img), img)
}
