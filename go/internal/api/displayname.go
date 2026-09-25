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
}

func nameSigData(fid, name string, ts int64) []byte {
	return []byte(fmt.Sprintf("fundus-name-v1|%s|%s|%d", strings.ToLower(fid), name, ts))
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
	return identity.Verify(nameSigData(r.FID, r.Name, r.TS), r.Sig, r.FID, r.Pub)
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
	if cur, ok := s.nameRecordOf(r.FID); ok && cur.TS >= r.TS {
		return false
	}
	return s.store.Put(&storage.Record{ID: nameRecordID(r.FID), Type: storage.RecordDisplayName,
		Data: map[string]any{"fid": r.FID, "name": r.Name, "ts": r.TS, "pub": r.Pub, "sig": r.Sig}}) == nil
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
		Name string `json:"name"`
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
	if cur, ok := s.nameRecordOf(fid); ok && cur.TS >= ts {
		ts = cur.TS + 1 // falsch gehende Uhr: trotzdem "neuer"
	}
	sig, err := sess.identity.Sign(nameSigData(fid, name, ts))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	r := nameRecord{FID: fid, Name: name, TS: ts, Pub: sess.identity.PublicKeyHex, Sig: sig}
	if !s.storeNameRecord(r) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Name konnte nicht gespeichert werden"})
		return
	}
	if sess.messenger != nil {
		sess.messenger.SetDisplayName(name, ts, sig)
		if !sess.presenceHidden.Load() {
			_ = sess.messenger.PublishPresence(c.Request.Context(), true) // sofort verbreiten
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": name})
}

// GET /api/v1/identity/names?ids=0x…,0x… – geprüfte Anzeigenamen nachschlagen.
func (s *Server) identityNames(c *gin.Context) {
	out := map[string]string{}
	for i, id := range strings.Split(c.Query("ids"), ",") {
		if i >= 200 {
			break
		}
		id = strings.ToLower(strings.TrimSpace(id))
		if len(id) != 42 {
			continue
		}
		if r, ok := s.nameRecordOf(id); ok && r.Name != "" {
			out[id] = r.Name
		}
	}
	c.JSON(http.StatusOK, gin.H{"names": out})
}

// loadOwnName setzt beim Aufbau der Sitzung den gespeicherten eigenen Namen.
func (s *Server) loadOwnName(sess *Session) {
	if sess == nil || sess.identity == nil || sess.messenger == nil {
		return
	}
	if r, ok := s.nameRecordOf(sess.identity.FundusID); ok {
		sess.messenger.SetDisplayName(r.Name, r.TS, r.Sig)
	}
}

// nameFromPresence übernimmt einen mitgeschickten Namenseintrag (Präsenz/Abruf).
func (s *Server) nameFromPresence(fid, pub, name, sig string, ts int64) {
	if sig == "" || pub == "" {
		return
	}
	s.storeNameRecord(nameRecord{FID: fid, Name: name, TS: ts, Pub: pub, Sig: sig})
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
