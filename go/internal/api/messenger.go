package api

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/messenger"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
)

// wsUpgrader erlaubt WebSocket-Verbindungen vom lokalen Browser.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Nur lokale Verbindungen erlaubt (API bindet auf 127.0.0.1)
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		host := u.Hostname()
		// Localhost immer erlaubt.
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return true
		}
		// LAN-/Private-Netz-Adressen erlauben (der Browser greift über die
		// LAN-IP des Pi zu, z.B. https://10.10.11.39 → Origin ist diese IP).
		if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
			return true
		}
		return false
	},
}

// Session hält die Sitzungsdaten eines angemeldeten Nutzers im RAM.
// Wird beim Tab-Schließen / Neustart verworfen.
type Session struct {
	// 2FA
	totp         *identity.TOTPManager // nil wenn TOTP nicht eingerichtet
	totpVerified bool                  // true nach erfolgreichem TOTP-Code
	identity  *identity.Identity
	messenger *messenger.Messenger
	history   *messenger.HistoryStore // persistenter, verschlüsselter Verlauf
	wsConns   []*websocket.Conn
	wsMu      sync.Mutex
}

// broadcastWS sendet eine Nachricht an ALLE aktiven WebSockets dieser Session
// (Messenger-Seite + globale Benachrichtigung + evtl. mehrere Tabs). Tote
// Verbindungen werden entfernt. Gibt true zurück, wenn mind. eine erreicht wurde.
func (s *Session) broadcastWS(payload interface{}) bool {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	alive := s.wsConns[:0]
	sent := false
	for _, c := range s.wsConns {
		if c == nil {
			continue
		}
		if err := c.WriteJSON(payload); err == nil {
			alive = append(alive, c)
			sent = true
		} else {
			_ = c.Close()
		}
	}
	s.wsConns = alive
	return sent
}

func (s *Session) addWS(c *websocket.Conn) {
	s.wsMu.Lock()
	// Verbindungslimit: bei Seitenwechseln sammeln sich sonst WS-Verbindungen an
	// (jede mit Ping-Goroutine) → Ressourcen-Leak, Node hängt nach einer Weile.
	// Max. 3 pro Session (Messenger-Tab + globale Benachrichtigung + Reserve);
	// älteste darüber schließen.
	const maxWS = 3
	for len(s.wsConns) >= maxWS {
		old := s.wsConns[0]
		s.wsConns = s.wsConns[1:]
		if old != nil {
			_ = old.Close()
		}
	}
	s.wsConns = append(s.wsConns, c)
	s.wsMu.Unlock()
}

func (s *Session) removeWS(c *websocket.Conn) {
	s.wsMu.Lock()
	out := s.wsConns[:0]
	for _, x := range s.wsConns {
		if x != c { out = append(out, x) }
	}
	s.wsConns = out
	s.wsMu.Unlock()
}

// sessionStore verwaltet aktive Sitzungen (FundusID → Session).
var (
	sessionMu    sync.RWMutex
	sessionStore = make(map[string]*Session)
	// sessionTokens ordnet ein zufälliges Cookie-Token einer FundusID zu.
	// So bleibt man über einen HttpOnly-Cookie angemeldet, ohne dass die
	// FundusID selbst im Cookie steht (Token ist nicht zurückrechenbar).
	sessionTokens = make(map[string]string) // token → fundusID
)

// newSessionToken erzeugt ein kryptografisch zufälliges Session-Token.
func newSessionToken() string {
	b := make([]byte, 32)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// =============================================================================
//  Routen
// =============================================================================

func (s *Server) registerMessengerRoutes() {
	g := s.router.Group("/api/v1")
	{
		// Identitäts-Ableitung (Argon2id auf Server-Seite)
		g.POST("/identity/derive",  s.identityDerive)
		g.GET("/identity/me",       s.identityMe)
		g.GET("/identity/seed",     s.identitySeedWords) // Wallet-Seed anzeigen (eigene Session)
		g.POST("/identity/email-dir/publish", s.emailDirPublish) // opt-in email→FundusID
		g.GET("/identity/email-dir/lookup",   s.emailDirLookup)  // email → FundusID nachschlagen
		g.POST("/identity/keydir/publish", s.keyDirPublish) // eigenen X25519-PubKey veröffentlichen
		g.GET("/identity/keydir/lookup",   s.keyDirLookup)  // FundusID → X25519-PubKey nachschlagen
		g.POST("/identity/logout",  s.identityLogout)
		g.POST("/identity/sign",    s.identitySign)  // signiert eine Nachricht mit dem Session-Schlüssel

		// Messenger
		g.POST("/messenger/send",     s.messengerSend)
		g.POST("/messenger/read",     s.messengerMarkRead)
		g.POST("/messenger/decrypt",  s.messengerDecrypt)
		g.POST("/messenger/presence", s.messengerPresence)
		g.GET("/messenger/contacts",  s.messengerContacts)
		g.POST("/messenger/contacts/sync", s.contactsSync) // verschlüsselte Liste speichern
		g.GET("/messenger/contacts/load",  s.contactsLoad) // verschlüsselte Liste abrufen
		g.POST("/messenger/mailbox/deposit", s.mailboxDeposit) // Offline-Nachricht ablegen
		g.GET("/messenger/mailbox/fetch",    s.mailboxFetch)   // eigene wartende Nachrichten abholen
		g.GET("/messenger/whoami",    s.messengerWhoami)
		g.POST("/messenger/contacts", s.messengerAddContact)
		g.GET("/messenger/history",   s.messengerHistory)

		// WebSocket für Echtzeit-Nachrichten
		g.GET("/messenger/ws", s.messengerWebSocket)
		g.GET("/messenger/ice", s.messengerICE)
	}
}

// =============================================================================
//  Identity Endpoints
// =============================================================================

// buildSession baut die Laufzeit-Session einer Identität auf (Messenger,
// verschlüsselter Verlauf, Empfangs-Handler). Gemeinsamer Weg für den Login und
// die Wiederherstellung nach einem Neustart des Nodes.
func (s *Server) buildSession(id *identity.Identity) *Session {
	// Session anlegen
	sess := &Session{
		identity: id,
	}

	// Messenger-Instanz mit P2P-Adapter erstellen
	if s.node != nil {
		p2pAdapter := &p2pMessengerAdapter{node: s.node}
		sess.messenger = messenger.New(id, p2pAdapter, s.log)
		// Resolver für gerichtete Zustellung: FundusID → Peer-ID aus dem keydir.
		sess.messenger.SetPeerIDResolver(func(fundusID string) string {
			if rec, e := s.store.Get(storage.RecordKeyDir, "keydir:"+strings.ToLower(fundusID)); e == nil && rec != nil {
				if pid, ok := rec.Data["peer_id"].(string); ok {
					return pid
				}
			}
			return ""
		})

		// Persistenten, verschlüsselten Verlauf-Store anlegen (best effort).
		if s.cfg != nil && s.cfg.DataDir != "" {
			if hs, herr := messenger.NewHistoryStore(s.cfg.DataDir, id); herr == nil {
				sess.history = hs
				// TTL: Nachrichten älter als die Aufbewahrungsfrist entfernen
				// (geht nur mit dem Schlüssel, also beim Login des Nutzers).
				if days := s.cfg.TTLHistoryDays; days > 0 {
					go func() {
						if n, _ := hs.Prune(time.Duration(days) * 24 * time.Hour); n > 0 {
							s.log.Info("Verlauf ausgedünnt (TTL)", zap.Int("nachrichten", n))
						}
					}()
				}
			} else {
				s.log.Warn("Verlauf-Store konnte nicht angelegt werden", zap.Error(herr))
			}
		}

		// Eingehende Nachrichten an WebSocket weiterleiten + im Verlauf sichern.
		sess.messenger.OnMessage(func(msg *messenger.Message, payload *messenger.Payload) {
			// Quittungen (delivered/read) aktualisieren den Status der AUSGEHENDEN
			// Nachricht und werden als kompaktes Status-Update an den WS gereicht.
			if msg.Type == messenger.TypeReceipt && payload != nil && payload.ReceiptFor != "" {
				at := msg.Timestamp
				if sess.history != nil {
					_, _ = sess.history.UpdateReceipt(msg.SenderID, payload.ReceiptFor, payload.ReceiptKind, at)
				}
				sess.broadcastWS(gin.H{
					"type":         "receipt",
					"receipt_for":  payload.ReceiptFor,
					"receipt_kind": payload.ReceiptKind,
					"peer_id":      msg.SenderID,
					"ts":           at,
				})
				return
			}

			// Verlauf persistieren (eingehend). payload ist bereits entschlüsselt.
			// Anruf-Signale (SDP/ICE) sind keine Nachrichten → nicht in den Verlauf.
			if sess.history != nil && payload != nil && payload.Signal == nil {
				he := messenger.HistoryEntry{
					ID:        msg.ID,
					PeerID:    msg.SenderID,
					Outgoing:  false,
					Type:      string(msg.Type),
					Text:      payload.Text,
					FileHash:  payload.FileHash,
					FileName:  payload.FileName,
					FileSize:  payload.FileSize,
					Mime:      payload.MimeType,
					Timestamp: msg.Timestamp,
				}
				_ = sess.history.Append(he)
				s.forwardHistoryToHome(sess, he) // Gast-Node: auch beim Heim-Node ablegen
			}
			sess.wsMu.Lock()
			nConns := len(sess.wsConns)
			sess.wsMu.Unlock()
			if nConns > 0 {
				// msg + entschlüsselte payload zusammen senden, damit das Frontend
				// den Text direkt hat (kein zweiter decrypt-Roundtrip, der bei
				// Cookie-/Session-Problemen scheitern und leere Nachrichten erzeugen kann).
				out := gin.H{
					"id": msg.ID, "sender_id": msg.SenderID, "recipient_id": msg.RecipientID,
					"type": msg.Type, "ts": msg.Timestamp, "sender_pub_x": msg.SenderPubX,
				}
				if payload != nil {
					out["text"] = payload.Text
					out["file_hash"] = payload.FileHash
					out["file_name"] = payload.FileName
					out["file_size"] = payload.FileSize
					out["mime_type"] = payload.MimeType
					if payload.Signal != nil {
						out["signal"] = payload.Signal // WebRTC-Signal (Anruf)
					}
					out["decrypted"] = true
				}
				if sess.broadcastWS(out) {
				} else {
					s.log.Warn("WS-Zustellung fehlgeschlagen")
				}
			} else {
				s.log.Warn("Nachricht empfangen, aber kein aktiver WebSocket (Frontend nicht verbunden)")
			}
		})
	}
	return sess
}

// identityDerive leitet eine Identität aus Email + Passwort ab.
// Die Ableitung passiert server-seitig (Argon2id ~3-6s).
// Der Private Key bleibt im RAM der Session.
//
// POST /api/v1/identity/derive
// Body: { "email": "...", "password": "..." }
func (s *Server) identityDerive(c *gin.Context) {
	var req struct {
		Email    string   `json:"email"`
		Password string   `json:"password"`
		Words    []string `json:"words"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s.log.Info("Identitätsableitung startet…")
	var id *identity.Identity
	var err error
	if len(req.Words) > 0 {
		// Login direkt mit den Seed-Wörtern (ergibt dieselbe Identität wie email+pw).
		id, err = identity.DeriveFromWords(req.Words)
	} else if req.Email != "" && req.Password != "" {
		id, err = identity.Derive(req.Email, req.Password)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email+passwort ODER seed-wörter erforderlich"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Passwort und Email sofort aus Request-Struct löschen
	req.Password = ""
	req.Email    = ""

	// Session anlegen (Messenger, Verlauf, Empfangs-Handler)
	sess := s.buildSession(id)

	sessionMu.Lock()
	// Altes Cookie-Token invalidieren, falls vorhanden (sonst sammeln sich
	// mehrere gültige Sessions an → Badge und Messenger können unterschiedliche
	// Identitäten sehen). Ein Browser = eine aktive Session.
	forgetOld := ""
	if oldToken, err := c.Cookie("fundus_session"); err == nil && oldToken != "" {
		forgetOld = oldToken
		if oldFid, ok := sessionTokens[oldToken]; ok {
			delete(sessionTokens, oldToken)
			if oldFid != id.FundusID {
				delete(sessionStore, oldFid) // andere Wallet war eingeloggt → entfernen
			}
		}
	}
	sessionStore[id.FundusID] = sess
	// Sicheres Session-Token erzeugen und als HttpOnly-Cookie setzen, damit die
	// Anmeldung Reloads und Browser-Neustarts übersteht.
	token := newSessionToken()
	sessionTokens[token] = id.FundusID
	sessionMu.Unlock()
	// Über Neustarts/Updates hinweg: verschlüsselt ablegen (Schlüssel = Token).
	s.forgetSession(forgetOld)
	s.persistSession(token, id)

	// Cookie: HttpOnly, SameSite=Lax OHNE Secure. Mit selbstsigniertem Zertifikat
	// speichert/sendet Chrome Secure-Cookies unzuverlässig; Lax reicht, weil alle
	// Requests same-origin sind (Frontend und API auf demselben Host).
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("fundus_session", token, 30*24*3600, "/", "", false, true)
	s.log.Info("Identität abgeleitet", zap.String("fundusID", id.FundusID[:10]+"…"))

	// Eigenen X25519-PubKey ins Verzeichnis stellen, damit andere an diese
	// Adresse verschlüsselt senden können (Keyserver-Prinzip). ensureKeyDir
	// hält dabei den Heim-Node fest (home_peer) und überschreibt ihn nie.
	s.ensureKeyDir(c, id)

	c.JSON(http.StatusOK, gin.H{
		"fundus_id":  id.FundusID,
		"public_key": id.PublicKeyHex,
		"derived_at": id.DerivedAt,
		"note":       "Sitzung übersteht Neustarts (verschlüsselt, Schlüssel nur im Browser-Cookie)",
		"kdf":        "Argon2id v2 (512 MiB, t=4, ~11s Pi 3)",
	})
}

func (s *Server) identityMe(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	rec := sess.identity.PublicRecord()
	// Sicherstellen, dass der PubKey im Verzeichnis steht (auch bei Cookie-Login,
	// der identityDerive nicht durchläuft) — sonst kann niemand an diese Adresse
	// verschlüsselt senden.
	s.ensureKeyDir(c, sess.identity)
	// wallet_address ist die ECHTE Chain-Adresse (secp256k1), von der FND
	// gesendet/empfangen wird — abgeleitet aus demselben Login. Die FundusID
	// (Ed25519) ist die Messenger-/Kontakt-Identität.
	walletAddr := sess.identity.ChainAddr()
	if walletAddr == "" { walletAddr = rec.FundusID } // Fallback
	c.JSON(http.StatusOK, gin.H{
		"fundus_id":       rec.FundusID,
		"wallet_address":  walletAddr,
		"ed25519_pub_key": rec.Ed25519PubKey,
		"created_at":      rec.CreatedAt,
	})
}

// identitySign signiert eine Nachricht (z.B. einen Vertrags-Hash) mit dem
// Ed25519-Schlüssel der angemeldeten Identität. Setzt eine aktive Session voraus
// (der Nutzer muss vorher /identity/derive mit seinen Zugangsdaten aufrufen —
// erst dann liegt der private Schlüssel vor). Ohne Session: 401.
// identityLogout meldet ab: löscht das Session-Cookie und die Token-Zuordnung.
// POST /api/v1/identity/logout
func (s *Server) identityLogout(c *gin.Context) {
	if token, err := c.Cookie("fundus_session"); err == nil && token != "" {
		sessionMu.Lock()
		if fid, ok := sessionTokens[token]; ok {
			delete(sessionStore, fid) // Session komplett entfernen
		}
		delete(sessionTokens, token)
		sessionMu.Unlock()
		s.forgetSession(token)
	}
	// Cookie im Browser löschen (maxAge negativ).
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("fundus_session", "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) identitySign(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet — bitte zuerst mit Zugangsdaten anmelden"})
		return
	}
	var req struct {
		Message string `json:"message" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sig, err := sess.identity.Sign([]byte(req.Message))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Signatur fehlgeschlagen: " + err.Error()})
		return
	}
	rec := sess.identity.PublicRecord()
	c.JSON(http.StatusOK, gin.H{
		"signature":       sig,
		"fundus_id":       rec.FundusID,
		"wallet_address":  rec.FundusID,
		"ed25519_pub_key": rec.Ed25519PubKey,
	})
}

// =============================================================================
//  Messenger Endpoints
// =============================================================================

// messengerSend verschlüsselt und sendet eine Nachricht.
//
// POST /api/v1/messenger/send
// Body: { "recipient_id": "0x...", "recipient_pub_key": "04...", "text": "...", "type": "text" }
func (s *Server) messengerSend(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.messenger == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}

	var req struct {
		RecipientID     string              `json:"recipient_id"`
		RecipientPubKey string              `json:"recipient_pub_key"`
		Text            string              `json:"text,omitempty"`
		FileHash        string              `json:"file_hash,omitempty"`
		FileName        string              `json:"file_name,omitempty"`
		FileSize        int64               `json:"file_size,omitempty"`
		MimeType        string              `json:"mime_type,omitempty"`
		Signal          *messenger.SignalMsg `json:"signal,omitempty"`
		Type            messenger.MessageType `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Empfänger-PubKey fehlt? Im Schlüssel-Verzeichnis nachschlagen (damit man an
	// beliebige Adressen senden kann, ohne den Key vorher zu kennen).
	if req.RecipientPubKey == "" && req.RecipientID != "" {
		rid := strings.ToLower(req.RecipientID)
		// Lokalen Cache ZUERST (niedrige Latenz — kein Netzwerk-Roundtrip pro
		// Nachricht). Peer-Request nur, wenn lokal nichts da ist.
		rec, _ := s.store.Get(storage.RecordKeyDir, "keydir:"+rid)
		if rec == nil && s.node != nil {
			if raw := s.node.RequestKeyDir(c.Request.Context(), rid); len(raw) > 0 {
				var dr storage.Record
				if json.Unmarshal(raw, &dr) == nil && dr.ID != "" {
					_ = s.store.Put(&dr)
					rec = &dr
					s.log.Info("Empfänger-PubKey via Peer-Request", zap.String("to", rid[:10]+"…"))
				}
			}
		}
		if rec == nil && s.node != nil {
			if raw, derr := s.node.DHTget(c.Request.Context(), "/fundus/keydir/"+rid); derr == nil && len(raw) > 0 {
				var dr storage.Record
				if json.Unmarshal(raw, &dr) == nil && dr.ID != "" {
					_ = s.store.Put(&dr) // lokal cachen für nächstes Mal
					rec = &dr
					s.log.Info("Empfänger-PubKey via DHT abgerufen", zap.String("to", rid[:10]+"…"))
				}
			}
		}
		if rec != nil {
			if x, ok := rec.Data["x25519"].(string); ok && x != "" {
				req.RecipientPubKey = x
				if fdid, ok2 := rec.Data["fundus_id"].(string); ok2 && fdid != "" {
					req.RecipientID = strings.ToLower(fdid)
				}
			}
		}
		if req.RecipientPubKey == "" {
			// Kein PubKey verfügbar (Empfänger war nie online). Statt abzubrechen:
			// Nachricht in die lokale Ausgangs-Warteschlange legen. Ein Worker
			// stellt sie zu, sobald der Empfänger online geht und seinen PubKey
			// veröffentlicht. So gehen Nachrichten an noch-nie-online Empfänger
			// nicht verloren (dezentrales Store-and-Forward).
			var rnd [8]byte
			_, _ = crand.Read(rnd[:])
			senderFid := ""
			if sess := s.getSession(c); sess != nil && sess.identity != nil {
				senderFid = strings.ToLower(sess.identity.FundusID)
			}
			obID := "outbox:" + senderFid + ":" + strconv.FormatInt(time.Now().UnixNano(), 36) + hex.EncodeToString(rnd[:])
			obRec := &storage.Record{
				ID:   obID,
				Type: storage.RecordOutbox,
				Data: map[string]any{
					"sender": senderFid, "recipient": rid,
					"text": req.Text, "msg_type": string(req.Type),
					"file_hash": req.FileHash, "file_name": req.FileName,
					"queued_at": time.Now().Unix(),
				},
			}
			_ = s.store.Put(obRec)
			s.log.Info("Nachricht in Ausgangs-Warteschlange (Empfänger noch ohne PubKey)", zap.String("to", rid[:10]+"…"))
			c.JSON(http.StatusOK, gin.H{"ok": true, "queued": true, "note": "Empfänger noch nie online — Nachricht wird zugestellt sobald erreichbar"})
			return
		}
	}

	var (
		msg *messenger.Message
		err error
	)

	switch req.Type {
	case messenger.TypeText:
		msg, err = sess.messenger.Send(c.Request.Context(),
			req.RecipientID, req.RecipientPubKey, req.Text)

	case messenger.TypeFile:
		msg, err = sess.messenger.SendFile(c.Request.Context(),
			req.RecipientID, req.RecipientPubKey,
			req.FileHash, req.FileName, req.MimeType, req.FileSize)

	case messenger.TypeSignal:
		if req.Signal == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "signal fehlt"})
			return
		}
		msg, err = sess.messenger.SendSignal(c.Request.Context(),
			req.RecipientID, req.RecipientPubKey, *req.Signal)

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unbekannter Typ"})
		return
	}

	if err != nil {
		s.internalError(c, err)
		return
	}

	// Ausgehende Nachricht im Verlauf sichern (Klartext liegt in req vor —
	// die gesendete Version ist für den Empfänger verschlüsselt, daher hier
	// separat ablegen, damit der eigene Verlauf lesbar bleibt).
	if sess.history != nil && msg != nil {
		he := messenger.HistoryEntry{
			ID:        msg.ID,
			PeerID:    req.RecipientID,
			Outgoing:  true,
			Type:      string(req.Type),
			Text:      req.Text,
			FileHash:  req.FileHash,
			FileName:  req.FileName,
			FileSize:  req.FileSize,
			Mime:      req.MimeType,
			Timestamp: msg.Timestamp,
		}
		_ = sess.history.Append(he)
		s.forwardHistoryToHome(sess, he) // Gast-Node: auch beim Heim-Node ablegen
	}

	// Zusätzlich in die Offline-Mailbox des Empfängers legen (verschlüsselt),
	// damit die Nachricht auch ankommt, wenn der Empfänger gerade nicht online
	// ist — er holt sie beim nächsten Login (hier oder auf einem anderen Node) ab.
	if msg != nil {
		if env, e := json.Marshal(msg); e == nil {
			rid := strings.ToLower(req.RecipientID)
			var rnd [8]byte
			_, _ = crand.Read(rnd[:])
			mbID := "mailbox:" + rid + ":" + strconv.FormatInt(time.Now().UnixNano(), 36) + hex.EncodeToString(rnd[:])
			mbRec := &storage.Record{
				ID:   mbID,
				Type: storage.RecordMailbox,
				Data: map[string]any{"recipient": rid, "envelope": string(env), "ts": time.Now().Unix()},
			}
			_ = s.store.Put(mbRec)
			if s.node != nil {
				if raw, me := json.Marshal(mbRec); me == nil {
					_ = s.node.Publish(c.Request.Context(), "fundus.mailbox", raw)
				}
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "message_id": msg.ID})
}
// Nachrichten an deren Absender. Das Frontend ruft dies beim Öffnen eines Chats
// für alle noch nicht als gelesen quittierten eingehenden Nachrichten auf.
//
// POST /api/v1/messenger/read
// Body: { "peer_id": "0x...", "peer_pub_key": "...", "message_ids": ["...","..."] }
func (s *Server) messengerMarkRead(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.messenger == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		PeerID     string   `json:"peer_id"`
		PeerPubKey string   `json:"peer_pub_key"`
		MessageIDs []string `json:"message_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.PeerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "peer_id erforderlich"})
		return
	}
	// PubKey fehlt? Aus dem keydir auflösen (lokal oder per Peer-Request) — genau
	// wie beim Senden. Sonst könnte keine Lesebestätigung verschickt werden.
	if req.PeerPubKey == "" {
		rid := strings.ToLower(req.PeerID)
		var rec *storage.Record
		if s.node != nil {
			if raw := s.node.RequestKeyDir(c.Request.Context(), rid); len(raw) > 0 {
				var dr storage.Record
				if json.Unmarshal(raw, &dr) == nil { rec = &dr }
			}
		}
		if rec == nil {
			rec, _ = s.store.Get(storage.RecordKeyDir, "keydir:"+rid)
		}
		if rec != nil {
			if x, ok := rec.Data["x25519"].(string); ok && x != "" {
				req.PeerPubKey = x
				if fdid, ok2 := rec.Data["fundus_id"].(string); ok2 && fdid != "" {
					req.PeerID = strings.ToLower(fdid)
				}
			}
		}
	}
	if req.PeerPubKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Empfänger-Schlüssel für Quittung nicht gefunden"})
		return
	}
	sent := 0
	for _, mid := range req.MessageIDs {
		if mid == "" {
			continue
		}
		if err := sess.messenger.SendReceipt(c.Request.Context(),
			req.PeerID, req.PeerPubKey, mid, messenger.ReceiptRead); err == nil {
			sent++
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "sent": sent})
}

// messengerHistory liefert den paginierten, entschlüsselten Verlauf einer
// Konversation. 32 Nachrichten pro Seite; mit ?before=<RFC3339> werden die 32
// Nachrichten VOR diesem Zeitstempel geladen (dynamisches Nachladen beim
// Hochscrollen).
// GET /api/v1/messenger/history?peer=<id>&before=<ts>&limit=32
func (s *Server) messengerHistory(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	if sess.history == nil {
		c.JSON(http.StatusOK, gin.H{"messages": []any{}, "has_more": false})
		return
	}
	peer := c.Query("peer")
	if peer == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "peer fehlt"})
		return
	}
	limit := 32
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}
	var before *time.Time
	if b := c.Query("before"); b != "" {
		if t, err := time.Parse(time.RFC3339Nano, b); err == nil {
			before = &t
		}
	}

	// Gast-Node: Verlauf vom Heim-Node einmischen (gedrosselt, Lese-Cache).
	if before == nil && s.homePeerOfSession(sess) != "" {
		s.pullHistoryFromHome(sess, peer)
	}
	entries, hasMore, err := sess.history.Page(peer, before, limit)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"messages": entries,
		"has_more": hasMore,
	})
}

// messengerDecrypt entschlüsselt eine eingehende Nachricht.
// Wird vom Browser aufgerufen wenn eine Nachricht per WebSocket eingeht.
//
// POST /api/v1/messenger/decrypt
// Body: Message (JSON)
func (s *Server) messengerDecrypt(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}

	// Vereinfacht: Nachricht kommt als JSON, wir leiten sie intern weiter
	// Die eigentliche Entschlüsselung passiert im Messenger.handleIncoming
	c.JSON(http.StatusOK, gin.H{"note": "Entschlüsselung erfolgt intern im Messenger"})
}

// messengerPresence veröffentlicht den Online-Status.
func (s *Server) messengerPresence(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.messenger == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		Online bool `json:"online"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_ = sess.messenger.PublishPresence(c.Request.Context(), req.Online)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) messengerContacts(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.messenger == nil {
		c.JSON(http.StatusOK, gin.H{"contacts": []string{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contacts": sess.messenger.Contacts()})
}

// messengerWhoami gibt die fundus_id + den Ed25519-PublicKey der aktiven
// Messenger-Identität zurück (ohne Seed). Die Marktplatz-Erstellseiten nutzen
// das, um den eigenen Kontakt-Schlüssel ins Inserat einzubetten, damit Käufer
// den Verkäufer direkt verschlüsselt anschreiben können. Leer, wenn keine
// Messenger-Identität aktiv ist (dann erscheint kein Kontakt-Button am Inserat).
func (s *Server) messengerWhoami(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusOK, gin.H{"active": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"active":     true,
		"fundus_id":  sess.identity.FundusID,
		"public_key": sess.identity.PublicKeyHex,
	})
}

func (s *Server) messengerAddContact(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.messenger == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		FundusID      string `json:"fundus_id"`
		Ed25519PubKey string `json:"ed25519_pub_key"`
		X25519PubKey  string `json:"x25519_pub_key"`
		Alias         string `json:"alias"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sess.messenger.AddContact(&messenger.Contact{
		FundusID:      req.FundusID,
		Ed25519PubKey: req.Ed25519PubKey,
		X25519PubKey:  req.X25519PubKey,
		Alias:         req.Alias,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// =============================================================================
//  WebSocket für Echtzeit-Nachrichten
// =============================================================================

// messengerWebSocket stellt eine WebSocket-Verbindung für eingehende Nachrichten her.
//
// GET /api/v1/messenger/ws?id=<fundus-id>
func (s *Server) messengerWebSocket(c *gin.Context) {
	// SICHERHEIT: Die Session kommt aus dem Cookie (der WebSocket-Handshake
	// sendet es mit). ?id dient nur noch als Plausibilitätsprüfung – früher
	// konnte jeder mit einer (öffentlichen) FundusID fremde, bereits
	// entschlüsselte Nachrichten mitlesen.
	cs := s.getSession(c)
	if cs == nil || cs.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "nicht angemeldet", "relogin": true})
		return
	}
	fundusID := strings.ToLower(cs.identity.FundusID)
	if q := strings.ToLower(strings.TrimSpace(c.Query("id"))); q != "" && q != fundusID {
		c.JSON(http.StatusForbidden, gin.H{"error": "id passt nicht zur Session"})
		return
	}

	sessionMu.RLock()
	sess, ok := sessionStore[fundusID]
	sessionMu.RUnlock()
	if ok {
	}

	// Nicht über die id gefunden? Über das Session-Cookie versuchen (robuster,
	// falls die id-Schreibweise abweicht oder das Frontend eine andere Adresse
	// mitgibt als die eingeloggte).
	if !ok {
		if cookieSess := s.getSession(c); cookieSess != nil {
			sess = cookieSess
			ok = true
			s.log.Info("WS-Session über Cookie aufgelöst (id-Mismatch)", zap.String("query_id", fundusID[:10]+"…"))
		}
	}

	if !ok {
		s.log.Warn("WS: Session nicht gefunden", zap.String("id", fundusID[:10]+"…"))
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Session nicht gefunden", "relogin": true})
		return
	}

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		s.log.Warn("WebSocket Upgrade fehlgeschlagen", zap.Error(err))
		return
	}

	sess.addWS(conn) // zur Liste hinzufügen (mehrere WS pro Session erlaubt)

	s.log.Info("WebSocket verbunden", zap.String("fundusID", fundusID[:10]+"…"))

	// Verbindung offen halten bis Client trennt. Ping/Pong-Deadline, damit tote
	// Verbindungen (z.B. aus einem Reconnect-Sturm) nicht ewig eine Goroutine
	// blockieren und den Node überlasten.
	// Keepalive: Server pingt regelmäßig, Pong verlängert die Deadline. Deadline
	// großzügig (3min), damit gelegentlich verspätete Pongs die Verbindung nicht
	// killen.
	conn.SetReadDeadline(time.Now().Add(180 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(180 * time.Second))
		return nil
	})
	s.safeGo("ws-ping", func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			sess.wsMu.Lock()
			active := false
			for _, c := range sess.wsConns { if c == conn { active = true; break } }
			sess.wsMu.Unlock()
			if !active {
				return // Verbindung wurde entfernt
			}
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			s.log.Info("WebSocket getrennt", zap.String("fundusID", fundusID[:10]+"…"), zap.Error(err))
			break
		}
	}

	sess.removeWS(conn)
}

// =============================================================================
//  Session-Hilfsmethoden
// =============================================================================

// getSession gibt die aktive Session zurück.
// Vereinfacht: FundusID aus Query-Parameter oder erstem aktiven Eintrag.
func (s *Server) getSession(c *gin.Context) *Session {
	// SICHERHEIT: Session NUR über das HttpOnly-Session-Cookie. Früher ging es
	// auch per Header X-Fundus-ID / Query fundus_id – die FundusID ist aber
	// öffentlich (Partner-Ads, Keydir), damit konnte jeder fremde Sessions
	// übernehmen (Nachrichten lesen/senden, Profil ändern).
	// KEIN Fallback auf "erste aktive Session" — das würde ohne gültiges Cookie
	// eine fremde Session zurückgeben (Sicherheitsloch) und das Abmelden wirkungslos
	// machen. Ohne Identifikation gibt es keine Session.
	token, err := c.Cookie("fundus_session")
	if err != nil || token == "" {
		return nil
	}
	sessionMu.RLock()
	var sess *Session
	if fid := sessionTokens[token]; fid != "" {
		sess = sessionStore[fid]
	}
	sessionMu.RUnlock()
	if sess != nil {
		return sess
	}
	// Nach Neustart/Update: aus der verschlüsselten Ablage wiederherstellen.
	return s.restoreSession(c, token)
}

// =============================================================================
//  P2P-Adapter für Messenger
// =============================================================================

// p2pMessengerAdapter verbindet den Messenger mit dem P2P-Layer.
type p2pMessengerAdapter struct {
	node p2p.P2PNode
}

func (a *p2pMessengerAdapter) Publish(ctx context.Context, topic string, data []byte) error {
	return a.node.Publish(ctx, topic, data)
}

func (a *p2pMessengerAdapter) SetTopicHandler(topic string, handler func(data []byte)) {
	a.node.SetTopicHandler(topic, handler)
}

func (a *p2pMessengerAdapter) DeliverMessage(ctx context.Context, peerID, topicName string, payload []byte) bool {
	return a.node.DeliverMessage(ctx, peerID, topicName, payload)
}

// =============================================================================
//  TOTP 2FA Endpunkte
// =============================================================================

// totpStatus gibt zurück ob TOTP aktiviert ist.
// GET /api/v1/identity/2fa/status
func (s *Server) totpStatus(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "nicht angemeldet"})
		return
	}
	if sess.totp == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "configured": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":    sess.totp.IsEnabled(),
		"configured": true,
	})
}

// totpSetup startet den TOTP-Einrichtungs-Flow.
// Gibt Secret + otpauth-URL zurück (für QR-Code im Browser).
// POST /api/v1/identity/2fa/setup
func (s *Server) totpSetup(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "nicht angemeldet"})
		return
	}
	if sess.totp == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "TOTP-Manager nicht verfügbar"})
		return
	}
	setup, err := sess.totp.GenerateSetup(sess.identity.FundusID[:16] + "…")
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"secret_b32":   setup.SecretB32,
		"otp_auth_url": setup.OTPAuthURL,
		"instructions": "QR-Code in Authenticator-App scannen (Google Authenticator, Aegis, Bitwarden …), dann /2fa/enable mit erstem Code aufrufen",
	})
}

// totpEnable aktiviert TOTP nach erfolgreicher Verifikation.
// POST /api/v1/identity/2fa/enable  Body: { "code": "123456" }
func (s *Server) totpEnable(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "nicht angemeldet"})
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := sess.totp.VerifyAndEnable(req.Code); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled": true,
		"message": "2FA aktiviert – ab sofort beim Login erforderlich",
	})
}

// totpVerify prüft einen TOTP-Code (beim Login nach identityDerive).
// POST /api/v1/identity/2fa/verify  Body: { "code": "123456" }
func (s *Server) totpVerify(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "nicht angemeldet"})
		return
	}
	if sess.totp == nil || !sess.totp.IsEnabled() {
		// TOTP nicht aktiviert → kein zweiter Faktor nötig
		c.JSON(http.StatusOK, gin.H{"verified": true, "totp_required": false})
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := sess.totp.Verify(req.Code); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":        err.Error(),
			"totp_required": true,
		})
		return
	}
	sess.totpVerified = true
	c.JSON(http.StatusOK, gin.H{"verified": true, "totp_required": true})
}

// totpDisable deaktiviert TOTP (erfordert aktuellen Code).
// POST /api/v1/identity/2fa/disable  Body: { "code": "123456" }
func (s *Server) totpDisable(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "nicht angemeldet"})
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := sess.totp.Disable(req.Code); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	sess.totpVerified = false
	c.JSON(http.StatusOK, gin.H{"disabled": true})
}

// =============================================================================
//  Wallet-Generierung via Web-UI
// =============================================================================

// walletGenerate generiert neue Seed-Wörter und leitet die Wallet-Adresse ab.
// Die Wörter werden nur im RAM dieser Response gehalten – nie persistiert.
// POST /api/v1/wallet/generate
func (s *Server) walletGenerate(c *gin.Context) {
	words, address, err := identity.GenerateWallet()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Adresse in Session merken (für confirm-Schritt)
	s.pendingWalletMu.Lock()
	s.pendingWallet = &pendingWalletState{Address: address, GeneratedAt: time.Now()}
	s.pendingWalletMu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"words":   words,
		"address": address,
		"note":    "Wörter JETZT aufschreiben – werden nur einmal angezeigt",
	})
}

// walletConfirm aktiviert die generierte Wallet (nach Bestätigung durch User).
// POST /api/v1/wallet/confirm
func (s *Server) walletConfirm(c *gin.Context) {
	s.pendingWalletMu.Lock()
	pw := s.pendingWallet
	s.pendingWallet = nil
	s.pendingWalletMu.Unlock()

	if pw == nil || time.Since(pw.GeneratedAt) > 10*time.Minute {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kein ausstehender Wallet-Generierungsvorgang"})
		return
	}
	// Adresse bestätigen. Die Wallet wird aus den Seed-Wörtern abgeleitet
	// (in wallet.key Datei gespeichert), nicht zur Laufzeit in die Config geschrieben.
	c.JSON(http.StatusOK, gin.H{"activated": true, "address": pw.Address})
}

// walletRestore leitet eine Wallet aus bestehenden Seed-Wörtern ab.
// Gibt nur die Adresse zurück – kein Private Key.
// POST /api/v1/wallet/restore  Body: { "words": ["word1", ...] }
func (s *Server) walletRestore(c *gin.Context) {
	var req struct {
		Words []string `json:"words" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Words) < 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mindestens 10 Seed-Wörter erforderlich"})
		return
	}
	address, err := identity.DeriveAddressFromSeed(req.Words)
	// Wörter sofort aus Slice löschen (best-effort)
	for i := range req.Words { req.Words[i] = "" }
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"address": address, "note": "Nur Adresse abgeleitet – kein Key gespeichert"})
}

type pendingWalletState struct {
	Address     string
	GeneratedAt time.Time
}

// identitySeedWords zeigt die Wallet-Seed-Wörter der angemeldeten Identität.
// Bewusste, sensible Aktion: nur für die eigene aktive Session, damit der Nutzer
// seine Wallet sichern/exportieren oder von einer externen Wallet senden kann.
// GET /api/v1/identity/seed
func (s *Server) identitySeedWords(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	words := sess.identity.SeedWords()
	if len(words) == 0 {
		c.JSON(http.StatusOK, gin.H{"error": "Keine Seed-Wörter verfügbar (ältere Session – bitte neu anmelden)"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"words":         words,
		"chain_address": sess.identity.ChainAddr(),
		"note":          "Diese Wörter sind dein Wallet-Zugang. Sicher aufbewahren, niemals teilen.",
	})
}

// contactsSync speichert die verschlüsselte Kontaktliste des angemeldeten
// Nutzers, gebunden an seine FundusID. Die Verschlüsselung passiert client-seitig
// NICHT — der Server verschlüsselt mit dem Session-Schlüssel (SelfEncrypt), damit
// nur diese Wallet die Liste lesen kann. Im P2P-Netz/Store abgelegt → an jedem
// Node wiederherstellbar.
// POST /api/v1/messenger/contacts/sync  Body: { contacts: [ {...}, ... ] }
func (s *Server) contactsSync(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		Contacts json.RawMessage `json:"contacts"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Mit dem eigenen Session-Schlüssel verschlüsseln (nur diese Wallet liest es).
	enc, err := sess.identity.SelfEncrypt([]byte(req.Contacts))
	if err != nil {
		s.internalError(c, err)
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	rec := &storage.Record{
		ID:   "contacts:" + fid,
		Type: storage.RecordContacts,
		Data: map[string]any{"enc": hex.EncodeToString(enc), "owner": fid},
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	// Ins P2P-Netz propagieren (damit an anderen Nodes abrufbar).
	if s.node != nil {
		if raw, e := json.Marshal(rec); e == nil {
			_ = s.node.Publish(c.Request.Context(), "fundus.contacts", raw)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// contactsLoad holt und entschlüsselt die Kontaktliste des angemeldeten Nutzers.
// GET /api/v1/messenger/contacts/load
func (s *Server) contactsLoad(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	rec, err := s.store.Get(storage.RecordContacts, "contacts:"+fid)
	if err != nil || rec == nil {
		c.JSON(http.StatusOK, gin.H{"contacts": []any{}}) // noch keine gespeichert
		return
	}
	encHex, _ := rec.Data["enc"].(string)
	enc, err := hex.DecodeString(encHex)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"contacts": []any{}})
		return
	}
	plain, err := sess.identity.SelfDecrypt(enc)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"contacts": []any{}, "error": "Entschlüsselung fehlgeschlagen"})
		return
	}
	c.Data(http.StatusOK, "application/json", []byte(`{"contacts":`+string(plain)+`}`))
}

// emailDirPublish veröffentlicht (opt-in) die Zuordnung email → FundusID, damit
// andere per Email an den Nutzer senden können. Die Zuordnung ist mit der
// Identität signiert, sodass niemand eine fremde Email eintragen kann. Die
// eigentliche Schlüssel-Ableitung braucht weiter das Passwort — nur die
// Zuordnung wird öffentlich (der Nutzer entscheidet bewusst).
// POST /api/v1/identity/email-dir/publish
func (s *Server) emailDirPublish(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	fid := strings.ToLower(sess.identity.FundusID)
	// Signatur über "email|fundusID" — beweist, dass der Inhaber der FundusID
	// diese Zuordnung selbst eingetragen hat.
	sig, err := sess.identity.Sign([]byte(email + "|" + fid))
	if err != nil {
		s.internalError(c, err)
		return
	}
	key := hex.EncodeToString(blake3EmailKey(email))
	rec := &storage.Record{
		ID:   "emaildir:" + key,
		Type: storage.RecordEmailDir,
		Data: map[string]any{
			"email": email, "fundus_id": fid,
			"pub_key": sess.identity.PublicKeyHex, "sig": sig,
		},
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	if s.node != nil {
		if raw, e := json.Marshal(rec); e == nil {
			_ = s.node.Publish(c.Request.Context(), "fundus.emaildir", raw)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "fundus_id": fid})
}

// emailDirLookup schlägt die FundusID + Public Key zu einer Email nach.
// GET /api/v1/identity/email-dir/lookup?email=...
func (s *Server) emailDirLookup(c *gin.Context) {
	email := strings.ToLower(strings.TrimSpace(c.Query("email")))
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email fehlt"})
		return
	}
	key := hex.EncodeToString(blake3EmailKey(email))
	rec, err := s.store.Get(storage.RecordEmailDir, "emaildir:"+key)
	if err != nil || rec == nil {
		c.JSON(http.StatusOK, gin.H{"found": false})
		return
	}
	fid, _ := rec.Data["fundus_id"].(string)
	pub, _ := rec.Data["pub_key"].(string)
	c.JSON(http.StatusOK, gin.H{"found": true, "fundus_id": fid, "pub_key": pub})
}

// blake3EmailKey erzeugt einen stabilen Schlüssel aus einer Email (damit die
// Email nicht im Klartext als DB-Key steht, aber deterministisch nachschlagbar ist).
func blake3EmailKey(email string) []byte {
	h := identity.Blake3Sum256([]byte("fundus-emaildir:" + email))
	return h[:16]
}

// mailboxDeposit legt eine (bereits Ende-zu-Ende-verschlüsselte) Nachricht in
// der Offline-Mailbox des Empfängers ab. So bleibt sie auf dem Node, bis der
// Empfänger sich (hier oder auf einem anderen Node) einloggt und sie abholt.
// POST /api/v1/messenger/mailbox/deposit  Body: { recipient_id, envelope }
func (s *Server) mailboxDeposit(c *gin.Context) {
	var req struct {
		RecipientID string          `json:"recipient_id" binding:"required"`
		Envelope    json.RawMessage `json:"envelope"      binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rid := strings.ToLower(req.RecipientID)
	// Eindeutige ID pro Nachricht (Zeitstempel + Zufall), gruppiert nach Empfänger.
	var rnd [8]byte
	_, _ = crand.Read(rnd[:])
	msgID := "mailbox:" + rid + ":" + strconv.FormatInt(time.Now().UnixNano(), 36) + hex.EncodeToString(rnd[:])
	rec := &storage.Record{
		ID:   msgID,
		Type: storage.RecordMailbox,
		Data: map[string]any{"recipient": rid, "envelope": string(req.Envelope), "ts": time.Now().Unix()},
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	if s.node != nil {
		if raw, e := json.Marshal(rec); e == nil {
			_ = s.node.Publish(c.Request.Context(), "fundus.mailbox", raw)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// mailboxFetch holt alle wartenden Nachrichten der angemeldeten Identität und
// entfernt sie danach aus der Mailbox (zugestellt = weg, kein ewiges Rumliegen).
// GET /api/v1/messenger/mailbox/fetch
func (s *Server) mailboxFetch(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	prefix := "mailbox:" + fid + ":"
	all, _ := s.store.List(storage.RecordMailbox)
	count := 0
	for _, r := range all {
		if !strings.HasPrefix(r.ID, prefix) {
			continue // nur Nachrichten für diese FundusID
		}
		if env, ok := r.Data["envelope"].(string); ok && sess.messenger != nil {
			// Serverseitig verarbeiten: entschlüsseln + in die persistente History
			// schreiben (über denselben Weg wie Echtzeit-Empfang). Danach ist die
			// Nachricht dauerhaft im Verlauf → Mailbox-Eintrag kann weg.
			sess.messenger.InjectIncoming([]byte(env))
			count++
		}
		_ = s.store.Delete(storage.RecordMailbox, r.ID)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "fetched": count})
}

// keyDirPublish veröffentlicht den X25519-Public-Key der angemeldeten Identität,
// gebunden an ihre FundusID. Damit kann JEDER an diese Adresse verschlüsselt
// senden, ohne den Key vorher kennen zu müssen (Keyserver-Prinzip). Signiert,
// damit niemand einen fremden Key eintragen kann.
// POST /api/v1/identity/keydir/publish  (nutzt die Session)
func (s *Server) keyDirPublish(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	x25519 := sess.identity.X25519PublicKeyHex()
	sig, err := sess.identity.Sign([]byte(fid + "|" + x25519))
	if err != nil {
		s.internalError(c, err)
		return
	}
	rec := &storage.Record{
		ID:   "keydir:" + fid,
		Type: storage.RecordKeyDir,
		Data: map[string]any{"fundus_id": fid, "x25519": x25519, "ed25519": sess.identity.PublicKeyHex, "sig": sig},
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	if s.node != nil {
		if raw, e := json.Marshal(rec); e == nil {
			_ = s.node.Publish(c.Request.Context(), "fundus.keydir", raw)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// keyDirLookup liefert den X25519-PubKey zu einer FundusID (zum Verschlüsseln).
// GET /api/v1/identity/keydir/lookup?fundus_id=0x...
func (s *Server) keyDirLookup(c *gin.Context) {
	fid := strings.ToLower(strings.TrimSpace(c.Query("fundus_id")))
	if fid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fundus_id fehlt"})
		return
	}
	rec, err := s.store.Get(storage.RecordKeyDir, "keydir:"+fid)
	if err != nil || rec == nil {
		c.JSON(http.StatusOK, gin.H{"found": false})
		return
	}
	x, _ := rec.Data["x25519"].(string)
	ed, _ := rec.Data["ed25519"].(string)
	c.JSON(http.StatusOK, gin.H{"found": true, "x25519": x, "ed25519": ed})
}

// ensureKeyDir stellt sicher, dass der X25519-PubKey der Session im Verzeichnis
// steht. Wird bei JEDEM Session-Zugriff aufgerufen (nicht nur beim Ableiten),
// damit auch Cookie-Logins (ohne erneutes identityDerive) ihren Key publizieren.
func (s *Server) ensureKeyDir(c *gin.Context, id *identity.Identity) {
	if id == nil {
		return
	}
	fid := strings.ToLower(id.FundusID)
	chain := strings.ToLower(id.ChainAddr())
	// Beide Einträge prüfen (fundus_id UND wallet_address). Nur wenn BEIDE schon
	// da sind, ist nichts zu tun — sonst wurde z.B. bei einem alten Login nur der
	// fundus_id-Eintrag geschrieben und die wallet_address fehlt.
	// Prüfen, ob die Einträge existieren UND den AKTUELLEN X25519-Key enthalten.
	// Ein Eintrag mit veraltetem Key (z.B. nach Key-Wechsel) muss NEU geschrieben
	// werden — sonst holen Sender per Peer-Request den alten Key und die
	// Entschlüsselung scheitert ("message authentication failed").
	curX := id.X25519PublicKeyHex()
	myPeerID := ""
	if s.node != nil { myPeerID = s.node.ID().String() }
	fidOK, chainOK := false, false
	// Heim-Node: einmal festgelegt, nie überschrieben (Hybrid-Datenzugriff).
	// peer_id bleibt "wo bin ich gerade" (direkte Zustellung), home_peer ist
	// "wo liegen meine Daten".
	homePeer := ""
	if rec, e := s.store.Get(storage.RecordKeyDir, "keydir:"+fid); e == nil && rec != nil {
		hp, _ := rec.Data["home_peer"].(string)
		pid, _ := rec.Data["peer_id"].(string)
		homePeer = hp
		if homePeer == "" {
			homePeer = pid // Altbestand: bisheriger Node gilt als Heim
		}
		if stored, _ := rec.Data["x25519"].(string); stored == curX && hp != "" && pid == myPeerID {
			fidOK = true
		}
	} else if kd, ok := s.node.(interface {
		RequestKeyDir(ctx context.Context, addr string) []byte
	}); ok && s.node != nil {
		// Erster Login an diesem Node: Heim-Node im Netz nachfragen, damit ein
		// Gast-Node sich nicht selbst zum Heim macht (max. 2 s, einmalig).
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if raw := kd.RequestKeyDir(ctx, fid); len(raw) > 0 {
			var nr storage.Record
			if json.Unmarshal(raw, &nr) == nil {
				if hp, _ := nr.Data["home_peer"].(string); hp != "" {
					homePeer = hp
				} else if pid, _ := nr.Data["peer_id"].(string); pid != "" {
					homePeer = pid
				}
			}
		}
		cancel()
	}
	if homePeer == "" {
		homePeer = myPeerID // neuer Nutzer: dieser Node ist sein Heim
	}
	if chain == "" || chain == fid {
		chainOK = true // keine separate Chain-Adresse nötig
	} else if rec, e := s.store.Get(storage.RecordKeyDir, "keydir:"+chain); e == nil && rec != nil {
		if stored, _ := rec.Data["x25519"].(string); stored == curX {
			chainOK = true
		} else {
		}
	}
	if fidOK && chainOK {
		return
	}
	x25519 := id.X25519PublicKeyHex()
	sig, serr := id.Sign([]byte(fid + "|" + x25519))
	if serr != nil {
		s.log.Warn("ensureKeyDir: Signatur fehlgeschlagen", zap.Error(serr))
		return
	}
	krec := &storage.Record{
		ID:   "keydir:" + fid,
		Type: storage.RecordKeyDir,
		Data: map[string]any{"fundus_id": fid, "x25519": x25519, "ed25519": id.PublicKeyHex, "sig": sig, "peer_id": myPeerID, "home_peer": homePeer},
	}
	if perr := s.store.Put(krec); perr != nil {
		s.log.Warn("ensureKeyDir: Put fehlgeschlagen", zap.Error(perr))
		return
	}
	// Zusätzlich unter der Chain-/Wallet-Adresse ablegen, damit man den Empfänger
	// auch über die im Wallet-Badge angezeigte Adresse (wallet_address) findet —
	// nicht nur über die fundus_id. Beide sind "die Adresse" derselben Person.
	if chain != "" && chain != fid {
		crec := &storage.Record{
			ID:   "keydir:" + chain,
			Type: storage.RecordKeyDir,
			Data: map[string]any{"fundus_id": fid, "x25519": x25519, "ed25519": id.PublicKeyHex, "sig": sig, "peer_id": myPeerID, "home_peer": homePeer},
		}
		_ = s.store.Put(crec)
		if s.node != nil {
			node := s.node
			go func() {
				if raw, e := json.Marshal(crec); e == nil {
					_ = node.Publish(context.Background(), "fundus.keydir", raw)
				}
			}()
		}
	}
	s.log.Info("keydir-Eintrag sichergestellt", zap.String("fid", fid[:10]+"…"), zap.String("chain", chain))

	// DHT-Put + Broadcast ASYNCHRON (in Goroutine): DHTput blockiert bei wenigen
	// Nodes teils viele Sekunden — das darf /identity/me (das ensureKeyDir bei
	// jedem Aufruf ausführt) NICHT aufhalten, sonst hängt der ganze Login-Fluss.
	if s.node != nil {
		node := s.node
		go func() {
			if raw, e := json.Marshal(krec); e == nil {
				_ = node.DHTput(context.Background(), "/fundus/keydir/"+fid, raw)
				if chain != "" && chain != fid {
					_ = node.DHTput(context.Background(), "/fundus/keydir/"+chain, raw)
				}
			}
		}()
	}
	if s.node != nil {
		if raw, e := json.Marshal(krec); e == nil {
			_ = s.node.Publish(c.Request.Context(), "fundus.keydir", raw)
		}
	}
}

// runOutboxWorker stellt wartende Nachrichten zu, sobald der Empfänger-PubKey
// verfügbar wird (der Empfänger also erstmals online geht). Dezentrales
// Store-and-Forward: Nachrichten an noch-nie-online Empfänger gehen nicht
// verloren, sondern warten beim Sender und werden nachträglich zugestellt.
func (s *Server) runOutboxWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.processOutbox()
	}
}

func (s *Server) processOutbox() {
	if s.store == nil {
		return
	}
	all, _ := s.store.List(storage.RecordOutbox)
	for _, r := range all {
		recipient, _ := r.Data["recipient"].(string)
		senderFid, _ := r.Data["sender"].(string)
		if recipient == "" || senderFid == "" {
			_ = s.store.Delete(storage.RecordOutbox, r.ID)
			continue
		}
		// Ist der Empfänger-PubKey inzwischen da (lokal oder via DHT)?
		var pubX, fundusID string
		if krec, e := s.store.Get(storage.RecordKeyDir, "keydir:"+recipient); e == nil && krec != nil {
			pubX, _ = krec.Data["x25519"].(string)
			fundusID, _ = krec.Data["fundus_id"].(string)
		}
		if pubX == "" && s.node != nil {
			// Direkt bei Peers nachfragen (Haupt-Weg), DHT als Fallback.
			raw := s.node.RequestKeyDir(context.Background(), recipient)
			if len(raw) == 0 {
				raw, _ = s.node.DHTget(context.Background(), "/fundus/keydir/"+recipient)
			}
			if len(raw) > 0 {
				var dr storage.Record
				if json.Unmarshal(raw, &dr) == nil {
					pubX, _ = dr.Data["x25519"].(string)
					fundusID, _ = dr.Data["fundus_id"].(string)
					_ = s.store.Put(&dr)
				}
			}
		}
		if pubX == "" {
			continue // noch nicht erreichbar, später erneut versuchen
		}
		// Absender-Session finden (muss noch aktiv sein, um zu signieren/senden).
		sessionMu.RLock()
		sess := sessionStore[senderFid]
		sessionMu.RUnlock()
		if sess == nil || sess.messenger == nil {
			continue // Absender gerade nicht angemeldet — später erneut
		}
		if fundusID == "" {
			fundusID = recipient
		}
		text, _ := r.Data["text"].(string)
		msgType, _ := r.Data["msg_type"].(string)
		var err error
		if msgType == "file" {
			fh, _ := r.Data["file_hash"].(string)
			fn, _ := r.Data["file_name"].(string)
			_, err = sess.messenger.SendFile(context.Background(), strings.ToLower(fundusID), pubX, fh, fn, "", 0)
		} else {
			_, err = sess.messenger.Send(context.Background(), strings.ToLower(fundusID), pubX, text)
		}
		if err == nil {
			_ = s.store.Delete(storage.RecordOutbox, r.ID)
			s.log.Info("Wartende Nachricht zugestellt", zap.String("to", recipient[:10]+"…"))
		}
	}
}

// safeGo startet eine Goroutine mit Panic-Schutz: ein Panic in Hintergrund-
// Arbeit (DHT-Put, Publish, Ping) darf NIEMALS den ganzen Node crashen — sonst
// gehen alle Sessions verloren (RAM) und alle Nutzer sind ausgeloggt.
func (s *Server) safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("Goroutine-Panic abgefangen", zap.String("wo", name), zap.Any("panic", r))
			}
		}()
		fn()
	}()
}

// messengerICE liefert die ICE-Server für WebRTC-Anrufe: STUN (öffentlich) plus
// optional ein TURN-Server aus der Node-Konfiguration. Ohne TURN kommen Anrufe
// nicht zustande, wenn beide Seiten hinter strengem NAT sitzen (z.B. beide im
// Mobilfunk) – dann vermittelt TURN die Medienströme.
// GET /api/v1/messenger/ice
func (s *Server) messengerICE(c *gin.Context) {
	servers := []gin.H{{"urls": []string{"stun:stun.l.google.com:19302"}}}
	if s.cfg != nil && len(s.cfg.TurnURLs) > 0 {
		t := gin.H{"urls": s.cfg.TurnURLs}
		if s.cfg.TurnUser != "" {
			t["username"] = s.cfg.TurnUser
			t["credential"] = s.cfg.TurnPass
		}
		servers = append(servers, t)
	}
	c.JSON(http.StatusOK, gin.H{"iceServers": servers, "turn": s.cfg != nil && len(s.cfg.TurnURLs) > 0})
}
