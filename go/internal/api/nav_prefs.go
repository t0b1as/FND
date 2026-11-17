package api

// Navigation je Konto (R928).
//
// Welche Bereiche in der Zugriffsleiste oben, im Menü und als Kachel auf der
// Startseite stehen, lag bisher im Browser (localStorage) – galt also für das
// Gerät, nicht für die Person, und eine Änderung traf jeden, der denselben
// Browser nutzt. Jetzt hängt die Einstellung am Konto: selbst-verschlüsselt
// gespeichert und wie die Feed-Gruppen über fundus.contacts an alle Nodes
// verteilt, damit sie auf jedem Node gilt, an dem man sich anmeldet.
// Ohne Anmeldung gilt die Standard-Navigation.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
)

type navSicht struct {
	Landing bool `json:"landing"`
	Burger  bool `json:"burger"`
	Quick   bool `json:"quick"`
}

var navSchluessel = regexp.MustCompile(`^[a-z][a-z0-9_]{1,23}$`)

func navRecordID(fid string) string { return "navprefs:" + fid }

func (s *Server) kontoNavLesen(sess *Session) map[string]navSicht {
	if sess == nil || sess.identity == nil {
		return nil
	}
	fid := strings.ToLower(sess.identity.FundusID)
	rec, err := s.store.Get(storage.RecordNavPrefs, navRecordID(fid))
	if err != nil || rec == nil || rec.Data == nil || rec.DeletedAt != nil {
		return nil
	}
	encHex, _ := rec.Data["enc"].(string)
	enc, err := hex.DecodeString(encHex)
	if err != nil {
		return nil
	}
	plain, err := sess.identity.SelfDecrypt(enc)
	if err != nil {
		return nil // nicht von diesem Konto verschlüsselt → Standard
	}
	var v map[string]navSicht
	if json.Unmarshal(plain, &v) != nil {
		return nil
	}
	return v
}

// GET /api/v1/konto/navigation → {angemeldet, vis|null}
func (s *Server) kontoNavZeigen(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusOK, gin.H{"angemeldet": false, "vis": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"angemeldet": true, "vis": s.kontoNavLesen(sess)})
}

// PUT /api/v1/konto/navigation {vis} – {vis: null} setzt auf Standard zurück.
func (s *Server) kontoNavSpeichern(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden – die Navigation gilt je Konto."})
		return
	}
	var req struct {
		Vis map[string]navSicht `json:"vis"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.Vis) > 60 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	for k := range req.Vis {
		if !navSchluessel.MatchString(k) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Bereich: " + k})
			return
		}
	}
	fid := strings.ToLower(sess.identity.FundusID)
	plain, _ := json.Marshal(req.Vis)
	if req.Vis == nil {
		plain = []byte("null")
	}
	enc, err := sess.identity.SelfEncrypt(plain)
	if err != nil {
		s.internalError(c, err)
		return
	}
	rec := &storage.Record{ID: navRecordID(fid), Type: storage.RecordNavPrefs,
		Data: map[string]any{"enc": hex.EncodeToString(enc), "owner": fid}}
	rec.UpdatedAt = time.Now().UTC()
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	if s.node != nil {
		if raw, e := json.Marshal(rec); e == nil {
			go func() {
				defer func() { _ = recover() }()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = s.node.Publish(ctx, "fundus.contacts", raw)
			}()
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
