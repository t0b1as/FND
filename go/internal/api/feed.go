package api

// Feed (R812): Beiträge mit Bildern, GIFs und Videos, öffentlich oder nur für
// Kontakte, mit Likes und Kommentaren.
//
// ── Verteilung ──────────────────────────────────────────────────────────────
// Beiträge und Reaktionen sind öffentliche Datensätze (Thema fundus.feed) und
// werden wie Inserate an alle Nodes verteilt und beim Verbinden abgeglichen.
//
// ── Echtheit ────────────────────────────────────────────────────────────────
// Jeder Beitrag, jede Reaktion trägt den öffentlichen Schlüssel des Autors und
// seine Signatur. Die Fundus-ID leitet sich aus diesem Schlüssel ab
// (identity.Verify prüft beides). Ein manipulierter Node kann also nichts im
// Namen eines anderen unterschieben – die Node-Signatur allein sagt nur, wer
// den Datensatz VERTEILT hat, nicht wer ihn GESCHRIEBEN hat.
//
// ── Nur für Kontakte ────────────────────────────────────────────────────────
// Der Inhalt wird mit einem zufälligen Beitragsschlüssel K verschlüsselt. K
// wird für jeden Kontakt einzeln verpackt: mit dem gemeinsamen Geheimnis aus
// X25519(Autor, Kontakt). Der Kontakt rechnet dasselbe Geheimnis mit seinem
// privaten und dem öffentlichen Schlüssel des Autors aus. Wer nicht in der
// Liste steht, sieht nur, DASS es einen Beitrag gibt – nicht was darin steht.
// Kommentare auf solche Beiträge werden ebenfalls mit K verschlüsselt.
// Maßgeblich sind die Kontakte zum Zeitpunkt des Beitrags.
//
// Bewusst NICHT der vorhandene identity.GrantAccess/DecryptFileKey: Dort
// rechnet die Entschlüsselung mit dem eigenen statt dem Schlüssel des
// Absenders – das kann bei einem Dritten nicht aufgehen.
//
// ── Löschen ─────────────────────────────────────────────────────────────────
// Über eine vom AUTOR signierte Löschmarke, nicht über den Grabstein des
// Nodes. Der Grabstein gilt nur, wenn ihn der Node schreibt, dem der Beitrag
// gehört; wer auf dem einen Node postet und am Handy über einen anderen
// löscht, käme damit nicht durch. Die Marke prüft jeder Node selbst, und der
// Besitzer-Node räumt den Beitrag danach auch tatsächlich ab.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	feedMaxText     = 5000
	feedMaxComment  = 1500
	feedMaxBilder   = 6
	feedSeitenGroe  = 20
	feedThema       = "fundus.feed"
	feedSichtOeff   = "public"
	feedSichtKontak = "contacts"
)

var (
	feedHashMuster  = regexp.MustCompile(`^[0-9A-Za-z]{16,100}$`)
	feedDrosselMu   sync.Mutex
	feedDrossel     = map[string][]time.Time{}
	feedGeheimMu    sync.Mutex
	feedGeheimnisse = map[string][]byte{} // "meineFid|ihrX25519" → ECDH-Geheimnis
	feedSchluessMu  sync.Mutex
	feedSchluessel  = map[string][]byte{} // "meineFid|beitrag" → K (nil = kein Zugang)
)

// feedDrosseln begrenzt, wie oft jemand schreiben darf (Spam-Schutz).
func feedDrosseln(art, fid string, max int, fenster time.Duration) bool {
	feedDrosselMu.Lock()
	defer feedDrosselMu.Unlock()
	k := art + "|" + fid
	jetzt := time.Now()
	var behalten []time.Time
	for _, t := range feedDrossel[k] {
		if jetzt.Sub(t) < fenster {
			behalten = append(behalten, t)
		}
	}
	if len(behalten) >= max {
		feedDrossel[k] = behalten
		return false
	}
	feedDrossel[k] = append(behalten, jetzt)
	return true
}

func feedZufall(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func feedPruefsumme(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ── Inhalt ───────────────────────────────────────────────────────────────────

type feedInhalt struct {
	Text   string   `json:"text"`
	Bilder []string `json:"images,omitempty"`
	Video  string   `json:"video,omitempty"`      // Adresse (YouTube, Vimeo, Datei)
	VidDat string   `json:"video_hash,omitempty"` // hochgeladene Videodatei
}

var feedVideoMuster = regexp.MustCompile(`^https://[^\s"'<>]{4,400}$`)

func (i *feedInhalt) pruefen() string {
	i.Text = strings.TrimSpace(i.Text)
	if len([]rune(i.Text)) > feedMaxText {
		return "Text zu lang (höchstens 5000 Zeichen)."
	}
	if len(i.Bilder) > feedMaxBilder {
		return "Höchstens 6 Bilder je Beitrag."
	}
	for _, h := range i.Bilder {
		if !feedHashMuster.MatchString(h) {
			return "Ungültiger Bildverweis."
		}
	}
	i.Video = strings.TrimSpace(i.Video)
	if i.Video != "" && !feedVideoMuster.MatchString(i.Video) {
		return "Video-Adresse muss mit https:// beginnen."
	}
	if i.VidDat != "" && !feedHashMuster.MatchString(i.VidDat) {
		return "Ungültiger Videoverweis."
	}
	if i.Text == "" && len(i.Bilder) == 0 && i.Video == "" && i.VidDat == "" {
		return "Der Beitrag ist leer."
	}
	return ""
}

// ── Schlüssel ────────────────────────────────────────────────────────────────

// feedGeheimnis: X25519-Geheimnis zwischen mir und einem anderen Schlüssel.
// Zwischengespeichert, weil ECDHSharedSecret absichtlich teuer ist (Argon2id) –
// sonst kostete jeder Kontakt-Beitrag im Feed eine volle Härtung.
func feedGeheimnis(id *identity.Identity, ihrX string) ([]byte, error) {
	k := strings.ToLower(id.FundusID) + "|" + strings.ToLower(ihrX)
	feedGeheimMu.Lock()
	if g, ok := feedGeheimnisse[k]; ok {
		feedGeheimMu.Unlock()
		return g, nil
	}
	feedGeheimMu.Unlock()
	g, err := id.ECDHSharedSecret(ihrX)
	if err != nil {
		return nil, err
	}
	feedGeheimMu.Lock()
	if len(feedGeheimnisse) > 5000 {
		feedGeheimnisse = map[string][]byte{}
	}
	feedGeheimnisse[k] = g
	feedGeheimMu.Unlock()
	return g, nil
}

// feedKontakte liefert die Fundus-IDs der eigenen Kontakte – aus der
// verschlüsselten Liste DIESES Kontos. Nicht aus messenger.Contacts(): Die
// liegt prozessweit und mischt die Kontakte aller Konten auf dem Node.
func (s *Server) feedKontakte(sess *Session) []string {
	fid := strings.ToLower(sess.identity.FundusID)
	rec, err := s.store.Get(storage.RecordContacts, "contacts:"+fid)
	if err != nil || rec == nil || rec.Data == nil {
		return nil
	}
	encHex, _ := rec.Data["enc"].(string)
	enc, err := hex.DecodeString(encHex)
	if err != nil {
		return nil
	}
	plain, err := sess.identity.SelfDecrypt(enc)
	if err != nil {
		return nil
	}
	var liste []map[string]any
	if json.Unmarshal(plain, &liste) != nil {
		return nil
	}
	var out []string
	gesehen := map[string]bool{fid: true}
	for _, k := range liste {
		f, _ := k["fundusID"].(string)
		if f == "" {
			f, _ = k["fundus_id"].(string)
		}
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" && !gesehen[f] {
			gesehen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// feedX25519 holt den AKTUELLEN Schlüssel einer Fundus-ID – nicht den in der
// Kontaktliste gespeicherten, der veraltet sein kann (siehe R798).
func (s *Server) feedX25519(c *gin.Context, fid string) string {
	if rec, err := s.store.Get(storage.RecordKeyDir, "keydir:"+fid); err == nil && rec != nil {
		if x, _ := rec.Data["x25519"].(string); x != "" {
			return x
		}
	}
	return s.keyDirHolen(c.Request.Context(), fid)
}

// feedBeitragsschluessel liefert K für einen Kontakt-Beitrag, oder nil.
func feedBeitragsschluessel(id *identity.Identity, rec *storage.Record) []byte {
	me := strings.ToLower(id.FundusID)
	ck := me + "|" + rec.ID
	feedSchluessMu.Lock()
	if k, ok := feedSchluessel[ck]; ok {
		feedSchluessMu.Unlock()
		return k
	}
	feedSchluessMu.Unlock()
	var k []byte
	keys, _ := rec.Data["keys"].(map[string]any)
	if verpackt, _ := keys[me].(string); verpackt != "" {
		if xpub, _ := rec.Data["xpub"].(string); xpub != "" {
			if g, err := feedGeheimnis(id, xpub); err == nil {
				if roh, err := hex.DecodeString(verpackt); err == nil {
					if kk, err := identity.Decrypt(roh, g); err == nil && len(kk) == 32 {
						k = kk
					}
				}
			}
		}
	}
	feedSchluessMu.Lock()
	if len(feedSchluessel) > 20000 {
		feedSchluessel = map[string][]byte{}
	}
	feedSchluessel[ck] = k
	feedSchluessMu.Unlock()
	return k
}

// ── Prüfen ───────────────────────────────────────────────────────────────────

func feedBeitragSignaturtext(id, fid string, ts int64, sicht, nutzlast string) []byte {
	return []byte("fundus-post:" + id + ":" + fid + ":" + strconv.FormatInt(ts, 10) + ":" + sicht + ":" + feedPruefsumme(nutzlast))
}
func feedReaktionSignaturtext(art, beitrag, ziel, fid string, ts int64, nutzlast string) []byte {
	return []byte("fundus-feed:" + art + ":" + beitrag + ":" + ziel + ":" + fid + ":" + strconv.FormatInt(ts, 10) + ":" + feedPruefsumme(nutzlast))
}

func zahl64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

// feedBeitragEcht prüft die Autorensignatur eines Beitrags.
func feedBeitragEcht(rec *storage.Record) bool {
	d := rec.Data
	fid, _ := d["fid"].(string)
	pub, _ := d["pub"].(string)
	sig, _ := d["sig"].(string)
	sicht, _ := d["vis"].(string)
	nutzlast, _ := d["payload"].(string)
	if fid == "" || pub == "" || sig == "" {
		return false
	}
	return identity.Verify(feedBeitragSignaturtext(rec.ID, fid, zahl64(d["ts"]), sicht, nutzlast), sig, fid, pub)
}

func feedReaktionEcht(rec *storage.Record) bool {
	d := rec.Data
	art, _ := d["kind"].(string)
	beitrag, _ := d["post"].(string)
	ziel, _ := d["target"].(string)
	fid, _ := d["fid"].(string)
	pub, _ := d["pub"].(string)
	sig, _ := d["sig"].(string)
	nutzlast, _ := d["payload"].(string)
	if fid == "" || pub == "" || sig == "" {
		return false
	}
	return identity.Verify(feedReaktionSignaturtext(art, beitrag, ziel, fid, zahl64(d["ts"]), nutzlast), sig, fid, pub)
}

// ── Speichern und verteilen ──────────────────────────────────────────────────

func (s *Server) feedAblegen(rec *storage.Record) error {
	rec.CreatedAt = time.Now().UTC()
	rec.UpdatedAt = rec.CreatedAt
	if s.node != nil {
		rec.OwnerID = s.nodeID()
		if sig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = sig
		}
	}
	if err := s.store.Put(rec); err != nil {
		return err
	}
	s.feedVerteilen(rec)
	return nil
}

func (s *Server) feedVerteilen(rec *storage.Record) {
	if s.node == nil {
		return
	}
	data, err := marshalRecord(rec)
	if err != nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.node.Publish(ctx, feedThema, data)
	}()
}

func (s *Server) feedSitzung(c *gin.Context) *Session {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return nil
	}
	return sess
}

// ── Beitrag anlegen ──────────────────────────────────────────────────────────

func (s *Server) feedErstellen(c *gin.Context) {
	sess := s.feedSitzung(c)
	if sess == nil {
		return
	}
	var req struct {
		feedInhalt
		Sicht string `json:"visibility"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	if msg := req.feedInhalt.pruefen(); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	if req.Sicht != feedSichtKontak {
		req.Sicht = feedSichtOeff
	}
	fid := strings.ToLower(sess.identity.FundusID)
	if !feedDrosseln("post", fid, 30, time.Hour) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Beiträge in kurzer Zeit – bitte später erneut."})
		return
	}
	inhalt, _ := json.Marshal(req.feedInhalt)
	id := "post:" + feedZufall(12)
	ts := time.Now().Unix()
	daten := map[string]any{"fid": fid, "pub": sess.identity.PublicKeyHex, "ts": ts, "vis": req.Sicht}

	var nutzlast string
	empfaenger := 0
	if req.Sicht == feedSichtKontak {
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			s.internalError(c, err)
			return
		}
		enc, err := identity.Encrypt(inhalt, k)
		if err != nil {
			s.internalError(c, err)
			return
		}
		nutzlast = hex.EncodeToString(enc)
		meinX := sess.identity.X25519PublicKeyHex()
		keys := map[string]string{}
		// Mich selbst einschließen: Auch der Autor muss seinen Beitrag lesen.
		ziele := append([]string{fid}, s.feedKontakte(sess)...)
		for _, ziel := range ziele {
			x := meinX
			if ziel != fid {
				x = s.feedX25519(c, ziel)
			}
			if x == "" {
				continue // ohne Schlüssel kein Zugang – lieber ausgelassen als offen
			}
			g, err := feedGeheimnis(sess.identity, x)
			if err != nil {
				continue
			}
			v, err := identity.Encrypt(k, g)
			if err != nil {
				continue
			}
			keys[ziel] = hex.EncodeToString(v)
			if ziel != fid {
				empfaenger++
			}
		}
		daten["keys"] = keys
		daten["xpub"] = meinX
	} else {
		nutzlast = string(inhalt)
	}
	daten["payload"] = nutzlast
	sig, err := sess.identity.Sign(feedBeitragSignaturtext(id, fid, ts, req.Sicht, nutzlast))
	if err != nil {
		s.internalError(c, err)
		return
	}
	daten["sig"] = sig
	rec := &storage.Record{ID: id, Type: storage.RecordPost, Data: daten}
	if err := s.feedAblegen(rec); err != nil {
		s.internalError(c, err)
		return
	}
	if s.log != nil {
		s.log.Info("Feed-Beitrag erstellt", zap.String("id", id), zap.String("sicht", req.Sicht),
			zap.Int("kontakte", empfaenger))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id, "contacts": empfaenger})
}

// ── Reaktion anlegen (Like, Kommentar, Löschmarke) ───────────────────────────

func (s *Server) feedReaktion(c *gin.Context, sess *Session, art, beitrag, ziel, nutzlast string, eindeutig bool) (*storage.Record, error) {
	fid := strings.ToLower(sess.identity.FundusID)
	ts := time.Now().Unix()
	sig, err := sess.identity.Sign(feedReaktionSignaturtext(art, beitrag, ziel, fid, ts, nutzlast))
	if err != nil {
		return nil, err
	}
	// Likes und Löschmarken je Konto UND Node: Jeder Node schreibt nur seinen
	// eigenen Datensatz und gerät so nie mit dem Besitzer-Schutz aneinander.
	// Es gilt der jüngste über alle Nodes.
	id := "react:" + beitrag + ":" + art + ":" + feedZufall(8)
	if eindeutig {
		id = "react:" + beitrag + ":" + art + ":" + ziel + ":" + fid + ":" + s.nodeID()
	}
	rec := &storage.Record{ID: id, Type: storage.RecordPostReact, Data: map[string]any{
		"post": beitrag, "kind": art, "target": ziel, "fid": fid,
		"pub": sess.identity.PublicKeyHex, "ts": ts, "payload": nutzlast, "sig": sig,
	}}
	return rec, s.feedAblegen(rec)
}

func (s *Server) feedBeitragLaden(id string) *storage.Record {
	rec, err := s.store.Get(storage.RecordPost, id)
	if err != nil || rec == nil || rec.DeletedAt != nil || rec.Data == nil || !feedBeitragEcht(rec) {
		return nil
	}
	return rec
}

// feedDarfSehen: öffentlich, oder ich habe den Beitragsschlüssel.
func feedDarfSehen(sess *Session, rec *storage.Record) (bool, []byte) {
	if sicht, _ := rec.Data["vis"].(string); sicht != feedSichtKontak {
		return true, nil
	}
	if sess == nil || sess.identity == nil {
		return false, nil
	}
	k := feedBeitragsschluessel(sess.identity, rec)
	return k != nil, k
}

func (s *Server) feedLiken(c *gin.Context) {
	sess := s.feedSitzung(c)
	if sess == nil {
		return
	}
	beitrag := c.Param("id")
	rec := s.feedBeitragLaden(beitrag)
	if rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Beitrag nicht gefunden."})
		return
	}
	if ok, _ := feedDarfSehen(sess, rec); !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "Kein Zugang zu diesem Beitrag."})
		return
	}
	var req struct {
		Like bool `json:"like"`
	}
	_ = c.ShouldBindJSON(&req)
	if !feedDrosseln("like", strings.ToLower(sess.identity.FundusID), 300, time.Hour) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Likes in kurzer Zeit."})
		return
	}
	wert := "0"
	if req.Like {
		wert = "1"
	}
	if _, err := s.feedReaktion(c, sess, "like", beitrag, "", wert, true); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) feedKommentieren(c *gin.Context) {
	sess := s.feedSitzung(c)
	if sess == nil {
		return
	}
	beitrag := c.Param("id")
	rec := s.feedBeitragLaden(beitrag)
	if rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Beitrag nicht gefunden."})
		return
	}
	ok, k := feedDarfSehen(sess, rec)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "Kein Zugang zu diesem Beitrag."})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len([]rune(req.Text)) > feedMaxComment {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Kommentar leer oder zu lang (höchstens 1500 Zeichen)."})
		return
	}
	if !feedDrosseln("comment", strings.ToLower(sess.identity.FundusID), 120, time.Hour) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Kommentare in kurzer Zeit."})
		return
	}
	nutzlast := req.Text
	if k != nil { // Kontakt-Beitrag: Kommentar ebenso verschlüsseln
		enc, err := identity.Encrypt([]byte(req.Text), k)
		if err != nil {
			s.internalError(c, err)
			return
		}
		nutzlast = hex.EncodeToString(enc)
	}
	r, err := s.feedReaktion(c, sess, "comment", beitrag, "", nutzlast, false)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": r.ID})
}

// feedLoeschen: Beitrag (ziel leer) oder Kommentar (ziel = Kommentar-ID).
func (s *Server) feedLoeschen(c *gin.Context) {
	sess := s.feedSitzung(c)
	if sess == nil {
		return
	}
	beitrag := c.Param("id")
	ziel := c.Query("kommentar")
	fid := strings.ToLower(sess.identity.FundusID)
	if ziel == "" {
		rec := s.feedBeitragLaden(beitrag)
		if rec == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Beitrag nicht gefunden."})
			return
		}
		if autor, _ := rec.Data["fid"].(string); autor != fid {
			c.JSON(http.StatusForbidden, gin.H{"error": "Nur der Autor kann löschen."})
			return
		}
	} else {
		kr, err := s.store.Get(storage.RecordPostReact, ziel)
		if err != nil || kr == nil || kr.Data == nil || !feedReaktionEcht(kr) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Kommentar nicht gefunden."})
			return
		}
		if p, _ := kr.Data["post"].(string); p != beitrag {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Kommentar gehört nicht zu diesem Beitrag."})
			return
		}
		if autor, _ := kr.Data["fid"].(string); autor != fid {
			c.JSON(http.StatusForbidden, gin.H{"error": "Nur der Autor kann löschen."})
			return
		}
	}
	if _, err := s.feedReaktion(c, sess, "del", beitrag, ziel, "", true); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── Lesen ────────────────────────────────────────────────────────────────────

type feedReakt struct {
	likes     map[string]bool // fid → aktuell geliked
	likeTs    map[string]int64
	kommentar []*storage.Record
	geloescht map[string]bool // Ziel ("" = Beitrag) → vom Autor gelöscht
}

// feedReaktionenSammeln wertet alle Reaktionen eines Beitrags aus. Der Autor
// des Beitrags (bzw. des Kommentars) ist der Einzige, dessen Löschmarke zählt.
func (s *Server) feedReaktionenSammeln(beitrag *storage.Record) *feedReakt {
	r := &feedReakt{likes: map[string]bool{}, likeTs: map[string]int64{}, geloescht: map[string]bool{}}
	alle, _ := s.store.ListPrefix(storage.RecordPostReact, "react:"+beitrag.ID+":")
	autor, _ := beitrag.Data["fid"].(string)
	kommAutor := map[string]string{}
	var marken []*storage.Record
	for _, x := range alle {
		if x == nil || x.DeletedAt != nil || x.Data == nil || !feedReaktionEcht(x) {
			continue
		}
		if p, _ := x.Data["post"].(string); p != beitrag.ID {
			continue
		}
		fid, _ := x.Data["fid"].(string)
		ts := zahl64(x.Data["ts"])
		switch art, _ := x.Data["kind"].(string); art {
		case "like":
			if ts >= r.likeTs[fid] {
				r.likeTs[fid] = ts
				v, _ := x.Data["payload"].(string)
				r.likes[fid] = v == "1"
			}
		case "comment":
			r.kommentar = append(r.kommentar, x)
			kommAutor[x.ID] = fid
		case "del":
			marken = append(marken, x)
		}
	}
	for _, m := range marken {
		fid, _ := m.Data["fid"].(string)
		ziel, _ := m.Data["target"].(string)
		if ziel == "" && fid == autor {
			r.geloescht[""] = true
		} else if ziel != "" && kommAutor[ziel] == fid {
			r.geloescht[ziel] = true
		}
	}
	return r
}

func (s *Server) feedAutorInfo(fid string) gin.H {
	return gin.H{"fid": fid, "name": s.displayNameFor(fid),
		"avatar": "/api/v1/identity/avatar/" + fid}
}

func (s *Server) feedListe(c *gin.Context) {
	sess := s.getSession(c)
	me := ""
	if sess != nil && sess.identity != nil {
		me = strings.ToLower(sess.identity.FundusID)
	}
	vor, _ := strconv.ParseInt(c.Query("before"), 10, 64)
	nurVon := strings.ToLower(strings.TrimSpace(c.Query("author")))
	alle, err := s.store.List(storage.RecordPost)
	if err != nil {
		s.internalError(c, err)
		return
	}
	sort.Slice(alle, func(i, j int) bool { return zahl64(alle[i].Data["ts"]) > zahl64(alle[j].Data["ts"]) })
	var out []gin.H
	for _, rec := range alle {
		if len(out) >= feedSeitenGroe {
			break
		}
		if rec == nil || rec.DeletedAt != nil || rec.Data == nil {
			continue
		}
		ts := zahl64(rec.Data["ts"])
		if vor > 0 && ts >= vor {
			continue
		}
		fid, _ := rec.Data["fid"].(string)
		if nurVon != "" && fid != nurVon {
			continue
		}
		if !feedBeitragEcht(rec) {
			continue
		}
		ok, k := feedDarfSehen(sess, rec)
		if !ok {
			continue
		}
		r := s.feedReaktionenSammeln(rec)
		if r.geloescht[""] {
			// Der Besitzer-Node räumt einen vom Autor gelöschten Beitrag ab.
			if rec.OwnerID == s.nodeID() {
				_ = s.store.Delete(storage.RecordPost, rec.ID)
			}
			continue
		}
		nutzlast, _ := rec.Data["payload"].(string)
		var inhalt feedInhalt
		if k != nil {
			roh, err := hex.DecodeString(nutzlast)
			if err != nil {
				continue
			}
			klar, err := identity.Decrypt(roh, k)
			if err != nil || json.Unmarshal(klar, &inhalt) != nil {
				continue
			}
		} else if json.Unmarshal([]byte(nutzlast), &inhalt) != nil {
			continue
		}
		likes, kommentare := 0, 0
		for _, v := range r.likes {
			if v {
				likes++
			}
		}
		for _, kr := range r.kommentar {
			if !r.geloescht[kr.ID] {
				kommentare++
			}
		}
		sicht, _ := rec.Data["vis"].(string)
		out = append(out, gin.H{
			"id": rec.ID, "ts": ts, "visibility": sicht, "author": s.feedAutorInfo(fid),
			"text": inhalt.Text, "images": inhalt.Bilder, "video": inhalt.Video, "video_hash": inhalt.VidDat,
			"likes": likes, "liked": me != "" && r.likes[me], "comments": kommentare, "mine": me != "" && fid == me,
		})
	}
	if out == nil {
		out = []gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"posts": out, "logged_in": me != ""})
}

func (s *Server) feedKommentare(c *gin.Context) {
	sess := s.getSession(c)
	rec := s.feedBeitragLaden(c.Param("id"))
	if rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Beitrag nicht gefunden."})
		return
	}
	ok, k := feedDarfSehen(sess, rec)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "Kein Zugang zu diesem Beitrag."})
		return
	}
	me := ""
	if sess != nil && sess.identity != nil {
		me = strings.ToLower(sess.identity.FundusID)
	}
	r := s.feedReaktionenSammeln(rec)
	sort.Slice(r.kommentar, func(i, j int) bool {
		return zahl64(r.kommentar[i].Data["ts"]) < zahl64(r.kommentar[j].Data["ts"])
	})
	out := []gin.H{}
	for _, kr := range r.kommentar {
		if r.geloescht[kr.ID] {
			continue
		}
		text, _ := kr.Data["payload"].(string)
		if k != nil {
			roh, err := hex.DecodeString(text)
			if err != nil {
				continue
			}
			klar, err := identity.Decrypt(roh, k)
			if err != nil {
				continue
			}
			text = string(klar)
		}
		fid, _ := kr.Data["fid"].(string)
		out = append(out, gin.H{"id": kr.ID, "ts": zahl64(kr.Data["ts"]), "author": s.feedAutorInfo(fid),
			"text": text, "mine": me != "" && fid == me})
	}
	c.JSON(http.StatusOK, gin.H{"comments": out})
}

// feedAnzahl: sichtbare Beiträge der letzten 24 Stunden (für die Kachel).
// Ohne Entschlüsselung – ein Eintrag in der Schlüsselliste genügt als Zeichen.
func (s *Server) feedAnzahl(c *gin.Context) {
	sess := s.getSession(c)
	me := ""
	if sess != nil && sess.identity != nil {
		me = strings.ToLower(sess.identity.FundusID)
	}
	seit := time.Now().Add(-24 * time.Hour).Unix()
	alle, _ := s.store.List(storage.RecordPost)
	n := 0
	for _, rec := range alle {
		if rec == nil || rec.DeletedAt != nil || rec.Data == nil || zahl64(rec.Data["ts"]) < seit {
			continue
		}
		if sicht, _ := rec.Data["vis"].(string); sicht == feedSichtKontak {
			keys, _ := rec.Data["keys"].(map[string]any)
			if me == "" || keys[me] == nil {
				continue
			}
		}
		n++
	}
	c.JSON(http.StatusOK, gin.H{"count": n})
}

func (s *Server) registerFeedRoutes(r *gin.Engine) {
	g := r.Group("/api/v1/feed")
	{
		g.GET("", s.feedListe)
		g.GET("/count", s.feedAnzahl)
		g.POST("", s.feedErstellen)
		g.GET("/:id/comments", s.feedKommentare)
		g.POST("/:id/comments", s.feedKommentieren)
		g.POST("/:id/like", s.feedLiken)
		g.DELETE("/:id", s.feedLoeschen)
	}
}
