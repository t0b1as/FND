package api

// Nachricht bearbeiten, löschen und Verlauf leeren (R662).
//
// Grundsätze, die sich aus dem Entwurf des Netzes ergeben:
//
//  1. Jeder verwaltet seinen EIGENEN Verlauf. Es gibt keine zentrale Stelle,
//     die beim Gegenüber durchgreifen könnte. Beim Empfänger verschwindet eine
//     Nachricht nur, wenn dessen Node die Mitteilung erhält und anwendet.
//  2. Geändert und gelöscht werden darf nur, was man selbst geschickt hat.
//     Fremde Nachrichten lassen sich aus dem eigenen Verlauf entfernen, aber
//     nicht beim anderen.
//  3. Eine Änderung wird als solche gekennzeichnet. Stilles Umschreiben wäre
//     in einem Chat nicht in Ordnung.
//  4. Die Mitteilung reist verschlüsselt über denselben Weg wie eine normale
//     Nachricht und landet bei Bedarf im Postfach.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fundus/node/internal/storage"
)

// peerSchluessel sucht den X25519-Schlüssel der Gegenstelle heraus.
func (s *Server) peerSchluessel(sess *Session, fid string) string {
	if sess == nil || fid == "" {
		return ""
	}
	rec, err := s.store.Get(storage.RecordKeyDir, "keydir:"+strings.ToLower(fid))
	if err != nil || rec == nil || rec.Data == nil {
		return ""
	}
	x, _ := rec.Data["x25519"].(string)
	return x
}

// POST /api/v1/messenger/edit  { peer, message_id, text }
func (s *Server) messengerEdit(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.history == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		Peer      string `json:"peer"`
		MessageID string `json:"message_id"`
		Text      string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Peer = strings.ToLower(strings.TrimSpace(req.Peer))
	req.Text = strings.TrimSpace(req.Text)
	if req.Peer == "" || req.MessageID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "peer und message_id erforderlich"})
		return
	}
	if req.Text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Leerer Text – zum Entfernen bitte löschen."})
		return
	}
	if len([]rune(req.Text)) > 4000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Text zu lang"})
		return
	}

	// Nur eigene Nachrichten ändern.
	eigen, err := s.istEigeneNachricht(sess, req.Peer, req.MessageID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if !eigen {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Nur eigene Nachrichten lassen sich bearbeiten."})
		return
	}

	jetzt := time.Now()
	ok, err := sess.history.UpdateText(req.Peer, req.MessageID, req.Text, jetzt)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Nachricht nicht im Verlauf"})
		return
	}

	// Dem Gegenüber mitteilen. Scheitert das, bleibt die eigene Änderung
	// bestehen – der andere sieht dann weiter die alte Fassung, und genau das
	// meldet die Antwort zurück, statt es zu verschweigen.
	zugestellt := true
	if pk := s.peerSchluessel(sess, req.Peer); pk != "" && sess.messenger != nil {
		if _, err := sess.messenger.SendEdit(c.Request.Context(), req.Peer, pk,
			req.MessageID, req.Text); err != nil {
			zugestellt = false
		}
	} else {
		zugestellt = false
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "edited_at": jetzt, "zugestellt": zugestellt})
}

// POST /api/v1/messenger/delete  { peer, message_id, auch_beim_anderen }
func (s *Server) messengerDelete(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.history == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		Peer      string `json:"peer"`
		MessageID string `json:"message_id"`
		BeimPeer  bool   `json:"beim_anderen"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Peer = strings.ToLower(strings.TrimSpace(req.Peer))
	if req.Peer == "" || req.MessageID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "peer und message_id erforderlich"})
		return
	}

	eigen, err := s.istEigeneNachricht(sess, req.Peer, req.MessageID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	// Fremde Nachrichten darf man aus dem EIGENEN Verlauf entfernen, aber nicht
	// beim anderen – sonst könnte man die Aussagen anderer verschwinden lassen.
	if req.BeimPeer && !eigen {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Beim anderen lassen sich nur eigene Nachrichten löschen."})
		return
	}

	ok, err := sess.history.DeleteEntry(req.Peer, req.MessageID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Nachricht nicht im Verlauf"})
		return
	}

	zugestellt := false
	if req.BeimPeer {
		if pk := s.peerSchluessel(sess, req.Peer); pk != "" && sess.messenger != nil {
			if _, err := sess.messenger.SendDelete(c.Request.Context(), req.Peer, pk,
				req.MessageID); err == nil {
				zugestellt = true
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "zugestellt": zugestellt})
}

// POST /api/v1/messenger/clear  { peer }
// Leert den EIGENEN Verlauf einer Konversation. Beim Gegenüber bleibt alles.
func (s *Server) messengerClear(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.history == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		Peer     string `json:"peer"`
		BeimPeer bool   `json:"beim_anderen"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Peer = strings.ToLower(strings.TrimSpace(req.Peer))
	if req.Peer == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "peer erforderlich"})
		return
	}
	jetzt := time.Now().UTC()
	if err := sess.history.ClearConvBis(req.Peer, jetzt); err != nil {
		s.internalError(c, err)
		return
	}
	ctx := c.Request.Context()
	// R818: Auch auf den EIGENEN anderen Nodes löschen. Der Verlauf liegt je
	// Node; bisher blieb das Gespräch dort stehen. Über das Postfach erreicht
	// die Bitte auch Nodes, auf denen man gerade nicht angemeldet ist.
	if sess.messenger != nil && sess.identity != nil {
		if m, err := sess.messenger.SendClearEigene(ctx, req.Peer); err == nil && m != nil {
			s.depositMailbox(ctx, sess.identity.FundusID, m)
		}
	}
	// Auf Wunsch das Gegenüber bitten, dasselbe zu tun (R663).
	//
	// R818: Bisher kam die Bitte nur an, wenn das Gegenüber gerade auf einem
	// ANDEREN Node erreichbar war. Auf demselben Node erreicht die direkte
	// Zustellung sich selbst nicht (wie bei Textnachrichten vor R810), und
	// ins Postfach wurde sie nie gelegt – wer offline war, bekam sie nie.
	zugestellt, nachgeholt := false, false
	if req.BeimPeer && sess.messenger != nil {
		pk := s.peerSchluessel(sess, req.Peer)
		if pk == "" {
			pk = s.keyDirHolen(ctx, req.Peer) // aktuellen Schlüssel im Netz erfragen
		}
		if pk != "" {
			if m, err := sess.messenger.SendClear(ctx, req.Peer, pk); m != nil {
				zugestellt = err == nil
				// Ins Postfach: erreicht das Gegenüber auf jedem Node, auch später.
				s.depositMailbox(ctx, req.Peer, m)
				nachgeholt = true
				// Gegenüber auf DIESEM Node angemeldet? Dann sofort einspeisen.
				sessionMu.RLock()
				empf := sessionStore[req.Peer]
				sessionMu.RUnlock()
				if empf != nil && empf.messenger != nil {
					if env, e := json.Marshal(m); e == nil {
						empf.messenger.InjectIncoming(env)
						zugestellt = true
					}
				}
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "zugestellt": zugestellt, "nachgeholt": nachgeholt})
}

// istEigeneNachricht prüft im Verlauf, ob die Nachricht von einem selbst
// stammt. Der Verlauf ist die einzige verlässliche Quelle dafür – die
// Behauptung des Browsers zählt nicht.
func (s *Server) istEigeneNachricht(sess *Session, peer, messageID string) (bool, error) {
	if sess.history == nil {
		return false, nil
	}
	var vor *time.Time
	for runde := 0; runde < 200; runde++ { // deckt sehr lange Verläufe ab
		eintraege, mehr, err := sess.history.Page(peer, vor, 100)
		if err != nil {
			return false, err
		}
		for _, e := range eintraege {
			if e.ID == messageID {
				return e.Outgoing, nil
			}
		}
		if !mehr || len(eintraege) == 0 {
			return false, nil
		}
		t := eintraege[0].Timestamp
		vor = &t
	}
	return false, nil
}
