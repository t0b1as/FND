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
	feedSichtGruppe = "group" // R813: nur Mitglieder einer eigenen Gruppe
)

var (
	feedHashMuster  = regexp.MustCompile(`^[0-9A-Za-z]{16,100}$`)
	feedDrosselMu   sync.Mutex
	feedDrossel     = map[string][]time.Time{}
	feedGeheimMu    sync.Mutex
	feedGeheimnisse = map[string][]byte{} // "meineFid|ihrX25519" → ECDH-Geheimnis
	feedSchluessMu  sync.Mutex
	feedSchluessel  = map[string][]byte{} // "meineFid|beitrag" → K (nur Erfolge)
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
	VidDat string   `json:"video_hash,omitempty"` // hochgeladene Videodatei (vor R815: nur eine)
	Videos []string `json:"videos,omitempty"`     // R815: mehrere hochgeladene Videos
	Gruppe string   `json:"group_name,omitempty"` // R813: nur im verschlüsselten Inhalt
	Link   *feedLink `json:"link,omitempty"`      // R814: Vorschau des ersten Links
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
	if len(i.Videos) > 4 {
		return "Höchstens 4 Videos je Beitrag."
	}
	for _, h := range i.Videos {
		if !feedHashMuster.MatchString(h) {
			return "Ungültiger Videoverweis."
		}
	}
	if msg := i.Link.pruefen(); msg != "" {
		return msg
	}
	if i.Text == "" && len(i.Bilder) == 0 && len(i.Videos) == 0 && i.Video == "" && i.VidDat == "" && i.Link == nil {
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

// feedBeitragsschluessel: ersetzt durch feedSchluesselbund (feed_edit.go, R815).

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
	fid := strings.ToLower(sess.identity.FundusID)
	if !feedDrosseln("post", fid, 30, time.Hour) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Beiträge in kurzer Zeit – bitte später erneut."})
		return
	}
	sicht, gid, gname, kreis, fehler := s.feedReichweite(sess, req.Sicht)
	if fehler != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": fehler})
		return
	}
	req.feedInhalt.Gruppe = gname
	inhalt, _ := json.Marshal(req.feedInhalt)
	id := "post:" + feedZufall(12)
	ts := time.Now().Unix()
	daten := map[string]any{"fid": fid, "pub": sess.identity.PublicKeyHex, "ts": ts, "vis": sicht}
	var nutzlast string
	empfaenger := 0
	if sicht == feedSichtOeff {
		nutzlast = string(inhalt)
	} else {
		var keys map[string]string
		var err error
		nutzlast, keys, _, empfaenger, err = s.feedVerpacken(c, sess, inhalt, kreis)
		if err != nil {
			s.internalError(c, err)
			return
		}
		daten["keys"] = keys
		daten["xpub"] = sess.identity.X25519PublicKeyHex()
		if gid != "" {
			daten["grp"] = gid
		}
	}
	daten["payload"] = nutzlast
	sig, err := sess.identity.Sign(feedBeitragSignaturtext(id, fid, ts, sicht, nutzlast))
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
		s.log.Info("Feed-Beitrag erstellt", zap.String("id", id), zap.String("sicht", sicht),
			zap.Int("empfaenger", empfaenger))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": id, "contacts": empfaenger})
}

// ── Reaktion anlegen (Like, Kommentar, Löschmarke) ───────────────────────────

func (s *Server) feedReaktion(c *gin.Context, sess *Session, art, beitrag, ziel, nutzlast string, eindeutig bool) (*storage.Record, error) {
	return s.feedReaktionAnlegen(sess, art, beitrag, ziel, nutzlast, eindeutig, nil)
}

// feedReaktionOhneAnfrage: für Freigaben aus dem Hintergrund, je Empfänger
// und Node eindeutig.
func (s *Server) feedReaktionOhneAnfrage(sess *Session, art, beitrag, ziel, nutzlast string, extra map[string]any) (*storage.Record, error) {
	return s.feedReaktionAnlegen(sess, art, beitrag, ziel, nutzlast, true, extra)
}

func (s *Server) feedReaktionAnlegen(sess *Session, art, beitrag, ziel, nutzlast string, eindeutig bool, extra map[string]any) (*storage.Record, error) {
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
	daten := map[string]any{
		"post": beitrag, "kind": art, "target": ziel, "fid": fid,
		"pub": sess.identity.PublicKeyHex, "ts": ts, "payload": nutzlast, "sig": sig,
	}
	// Zusatzfelder (Schlüssel einer Bearbeitung, Verschlüsselungskennung …)
	// überschreiben nie die signierten Grundfelder.
	for k, v := range extra {
		if _, belegt := daten[k]; !belegt {
			daten[k] = v
		}
	}
	rec := &storage.Record{ID: id, Type: storage.RecordPostReact, Data: daten}
	return rec, s.feedAblegen(rec)
}

func (s *Server) feedBeitragLaden(id string) *storage.Record {
	rec, err := s.store.Get(storage.RecordPost, id)
	if err != nil || rec == nil || rec.DeletedAt != nil || rec.Data == nil || !feedBeitragEcht(rec) {
		return nil
	}
	return rec
}

// feedDarfSehen: ersetzt durch feedZugang (feed_edit.go, R815).

// feedReaktionenVon liefert die gültigen Reaktionen EINES Beitrags.
func (s *Server) feedReaktionenVon(beitragID string) []*storage.Record {
	alle, _ := s.store.ListPrefix(storage.RecordPostReact, "react:"+beitragID+":")
	return feedGueltige(alle, beitragID)
}

// feedReaktionenIndex liest ALLE Reaktionen einmal und ordnet sie den
// Beiträgen zu – für die Feed-Liste günstiger als je Beitrag eine Suche.
func (s *Server) feedReaktionenIndex() map[string][]*storage.Record {
	idx := map[string][]*storage.Record{}
	alle, _ := s.store.List(storage.RecordPostReact)
	for _, x := range alle {
		if x == nil || x.DeletedAt != nil || x.Data == nil {
			continue
		}
		p, _ := x.Data["post"].(string)
		if p == "" || !feedReaktionEcht(x) {
			continue
		}
		idx[p] = append(idx[p], x)
	}
	return idx
}

func feedGueltige(alle []*storage.Record, beitragID string) []*storage.Record {
	var out []*storage.Record
	for _, x := range alle {
		if x == nil || x.DeletedAt != nil || x.Data == nil {
			continue
		}
		if p, _ := x.Data["post"].(string); p != beitragID || !feedReaktionEcht(x) {
			continue
		}
		out = append(out, x)
	}
	if out == nil {
		out = []*storage.Record{} // leer, aber vorhanden: "keine Freigaben"
	}
	return out
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
	if ok, _, _ := feedZugang(sess, rec, s.feedReaktionenVon(beitrag)); !ok {
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
	ok, z, b := feedZugang(sess, rec, s.feedReaktionenVon(beitrag))
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
	// Verschlüsselt mit dem Schlüssel des AKTUELLEN Stands. Die Kennzeichnung
	// "enc" (R815) sagt Lesern, ob sie entschlüsseln müssen – die Reichweite
	// kann sich seither geändert haben.
	nutzlast, extra := req.Text, map[string]any{"enc": "0"}
	if z.Sicht != feedSichtOeff && b.Aktuell != nil {
		enc, err := identity.Encrypt([]byte(req.Text), b.Aktuell)
		if err != nil {
			s.internalError(c, err)
			return
		}
		nutzlast, extra["enc"] = hex.EncodeToString(enc), "1"
	}
	r, err := s.feedReaktionAnlegen(sess, "comment", beitrag, "", nutzlast, false, extra)
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
func feedReaktionenSammeln(beitrag *storage.Record, alle []*storage.Record) *feedReakt {
	r := &feedReakt{likes: map[string]bool{}, likeTs: map[string]int64{}, geloescht: map[string]bool{}}
	autor, _ := beitrag.Data["fid"].(string)
	kommAutor := map[string]string{}
	var marken []*storage.Record
	for _, x := range alle {
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
	nurID := strings.TrimSpace(c.Query("id")) // R813: geteilter Link auf einen Beitrag
	alle, err := s.store.List(storage.RecordPost)
	if err != nil {
		s.internalError(c, err)
		return
	}
	reaktIdx := s.feedReaktionenIndex()
	if sess != nil && sess.identity != nil {
		go s.feedZugangErgaenzen(sess, alle, reaktIdx) // R813: neue Kontakte nachträglich freischalten
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
		if nurID != "" && rec.ID != nurID {
			continue
		}
		fid, _ := rec.Data["fid"].(string)
		if nurVon != "" && fid != nurVon {
			continue
		}
		if !feedBeitragEcht(rec) {
			continue
		}
		reakt := reaktIdx[rec.ID]
		if reakt == nil {
			reakt = []*storage.Record{}
		}
		ok, z, b := feedZugang(sess, rec, reakt)
		if !ok {
			continue
		}
		r := feedReaktionenSammeln(rec, reakt)
		if r.geloescht[""] {
			// Der Besitzer-Node räumt einen vom Autor gelöschten Beitrag ab.
			if rec.OwnerID == s.nodeID() {
				_ = s.store.Delete(storage.RecordPost, rec.ID)
			}
			continue
		}
		inhalt, lesbar := feedInhaltLesen(z, b)
		if !lesbar {
			continue
		}
		likes := 0
		for _, v := range r.likes {
			if v {
				likes++
			}
		}
		// Nur Kommentare zählen, die ich auch lesen kann (nach einer Änderung
		// der Reichweite sind ältere womöglich nicht mehr für jeden lesbar).
		basisSicht, _ := rec.Data["vis"].(string)
		bearb := feedKommentarBearbeitungen(reakt)
		kommentare := 0
		for _, kr := range r.kommentar {
			if r.geloescht[kr.ID] {
				continue
			}
			if _, _, ok := feedKommentarText(kr, bearb, basisSicht, b); ok {
				kommentare++
			}
		}
		videos := inhalt.Videos
		if inhalt.VidDat != "" {
			videos = append([]string{inhalt.VidDat}, videos...)
		}
		eintrag := gin.H{
			"id": rec.ID, "ts": ts, "visibility": z.Sicht, "author": s.feedAutorInfo(fid),
			"text": inhalt.Text, "images": inhalt.Bilder, "video": inhalt.Video, "videos": videos,
			"link": inhalt.Link, "likes": likes, "liked": me != "" && r.likes[me], "comments": kommentare,
			"mine": me != "" && fid == me, "group_name": inhalt.Gruppe, "edited": z.Bearbeitet,
		}
		if me != "" && fid == me && z.Gruppe != "" {
			eintrag["group_id"] = z.Gruppe // nur der Autor braucht die Kennung (Bearbeiten)
		}
		out = append(out, eintrag)
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
	reakt := s.feedReaktionenVon(rec.ID)
	ok, _, b := feedZugang(sess, rec, reakt)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "Kein Zugang zu diesem Beitrag."})
		return
	}
	me := ""
	if sess != nil && sess.identity != nil {
		me = strings.ToLower(sess.identity.FundusID)
	}
	r := feedReaktionenSammeln(rec, reakt)
	sort.Slice(r.kommentar, func(i, j int) bool {
		return zahl64(r.kommentar[i].Data["ts"]) < zahl64(r.kommentar[j].Data["ts"])
	})
	basisSicht, _ := rec.Data["vis"].(string)
	bearb := feedKommentarBearbeitungen(reakt)
	out := []gin.H{}
	for _, kr := range r.kommentar {
		if r.geloescht[kr.ID] {
			continue
		}
		text, bearbeitet, lesbar := feedKommentarText(kr, bearb, basisSicht, b)
		if !lesbar {
			continue
		}
		fid, _ := kr.Data["fid"].(string)
		out = append(out, gin.H{"id": kr.ID, "ts": zahl64(kr.Data["ts"]), "author": s.feedAutorInfo(fid),
			"text": text, "mine": me != "" && fid == me, "edited": bearbeitet})
	}
	c.JSON(http.StatusOK, gin.H{"comments": out})
}

func (s *Server) feedAnzahl(c *gin.Context) {
	sess := s.getSession(c)
	seit := time.Now().Add(-24 * time.Hour).Unix()
	alle, _ := s.store.List(storage.RecordPost)
	reaktIdx := s.feedReaktionenIndex()
	n := 0
	for _, rec := range alle {
		if rec == nil || rec.DeletedAt != nil || rec.Data == nil || zahl64(rec.Data["ts"]) < seit {
			continue
		}
		reakt := reaktIdx[rec.ID]
		if feedReaktionenSammeln(rec, reakt).geloescht[""] {
			continue
		}
		if ok, _, _ := feedZugang(sess, rec, reakt); ok {
			n++
		}
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
		g.POST("/preview", s.feedVorschau) // R814
		g.PUT("/:id", s.feedBearbeiten)                            // R815
		g.PUT("/:id/comments/:cid", s.feedKommentarBearbeiten)      // R815
		g.GET("/groups", s.feedGruppenListe)
		g.PUT("/groups", s.feedGruppenSpeichern)
	}
}

// ── Gruppen (R813) ───────────────────────────────────────────────────────────
//
// Eine Gruppe ist eine benannte Auswahl eigener Kontakte. Sie liegt
// selbst-verschlüsselt wie die Kontaktliste: Nur dieses Konto kann sie lesen,
// sie folgt ihm aber auf jeden Node (Thema fundus.contacts). Wer in einer
// Gruppe ist, erfährt das nicht – er sieht nur die Beiträge, und bei denen den
// Gruppennamen, der verschlüsselt im Beitrag mitreist.

type feedGruppe struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Mitglieder []string `json:"members"`
}

func (s *Server) feedGruppenLaden(sess *Session) []feedGruppe {
	fid := strings.ToLower(sess.identity.FundusID)
	rec, err := s.store.Get(storage.RecordFeedGroups, "feedgroups:"+fid)
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
	var g []feedGruppe
	_ = json.Unmarshal(plain, &g)
	return g
}

func (s *Server) feedGruppenListe(c *gin.Context) {
	sess := s.feedSitzung(c)
	if sess == nil {
		return
	}
	g := s.feedGruppenLaden(sess)
	if g == nil {
		g = []feedGruppe{}
	}
	c.JSON(http.StatusOK, gin.H{"groups": g})
}

var feedGruppenID = regexp.MustCompile(`^[0-9a-f]{8,32}$`)
var feedFidMuster = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

func (s *Server) feedGruppenSpeichern(c *gin.Context) {
	sess := s.feedSitzung(c)
	if sess == nil {
		return
	}
	var req struct {
		Gruppen []feedGruppe `json:"groups"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	if len(req.Gruppen) > 50 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Höchstens 50 Gruppen."})
		return
	}
	me := strings.ToLower(sess.identity.FundusID)
	for i := range req.Gruppen {
		g := &req.Gruppen[i]
		g.Name = strings.TrimSpace(g.Name)
		if g.Name == "" || len([]rune(g.Name)) > 40 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Gruppenname fehlt oder ist zu lang (höchstens 40 Zeichen)."})
			return
		}
		if !feedGruppenID.MatchString(g.ID) {
			g.ID = feedZufall(8)
		}
		var m []string
		gesehen := map[string]bool{}
		for _, f := range g.Mitglieder {
			f = strings.ToLower(strings.TrimSpace(f))
			if feedFidMuster.MatchString(f) && f != me && !gesehen[f] {
				gesehen[f] = true
				m = append(m, f)
			}
		}
		if len(m) > 500 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Höchstens 500 Mitglieder je Gruppe."})
			return
		}
		g.Mitglieder = m
	}
	plain, _ := json.Marshal(req.Gruppen)
	enc, err := sess.identity.SelfEncrypt(plain)
	if err != nil {
		s.internalError(c, err)
		return
	}
	rec := &storage.Record{ID: "feedgroups:" + me, Type: storage.RecordFeedGroups,
		Data: map[string]any{"enc": hex.EncodeToString(enc), "owner": me}}
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
	// Neue Mitglieder bekommen ältere Gruppenbeiträge – im Hintergrund.
	feedZugangMu.Lock()
	delete(feedZugangZuletzt, me)
	feedZugangMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true, "groups": req.Gruppen})
}

// ── Nachträgliche Freigaben (R813) ───────────────────────────────────────────
//
// Wer erst NACH einem Beitrag Kontakt oder Gruppenmitglied wird, hätte ihn
// nie gesehen – der Schlüssel war nur für den damaligen Kreis verpackt. Wenn
// der Autor den Feed öffnet, ergänzt sein Node für jeden neuen Berechtigten
// eine Freigabe: K, verpackt für den Neuen, vom Autor signiert.
//
// Zurücknehmen lässt sich das nicht: Wer einen Beitrag einmal lesen konnte,
// hat ihn gesehen. Wer aus der Gruppe entfernt wird, bekommt aber für KÜNFTIGE
// Beiträge keinen Schlüssel mehr.

var (
	feedZugangMu      sync.Mutex
	feedZugangZuletzt = map[string]time.Time{}
)

func (s *Server) feedZugangErgaenzen(sess *Session, beitraege []*storage.Record, reaktIdx map[string][]*storage.Record) {
	defer func() { _ = recover() }()
	me := strings.ToLower(sess.identity.FundusID)
	feedZugangMu.Lock()
	if t, ok := feedZugangZuletzt[me]; ok && time.Since(t) < 5*time.Minute {
		feedZugangMu.Unlock()
		return
	}
	feedZugangZuletzt[me] = time.Now()
	feedZugangMu.Unlock()

	var kontakte []string
	var kontakteGeladen bool
	gruppen := map[string][]string{}
	for _, g := range s.feedGruppenLaden(sess) {
		gruppen[g.ID] = g.Mitglieder
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	schluessel := map[string]string{} // fid → X25519, je Lauf einmal
	neu := 0
	for _, rec := range beitraege {
		if neu >= 200 || ctx.Err() != nil {
			break // je Lauf begrenzt; der Rest folgt beim nächsten Öffnen
		}
		if rec == nil || rec.DeletedAt != nil || rec.Data == nil {
			continue
		}
		if f, _ := rec.Data["fid"].(string); f != me {
			continue
		}
		reakt := reaktIdx[rec.ID]
		if reakt == nil {
			reakt = []*storage.Record{}
		}
		// R815: maßgeblich ist der AKTUELLE Stand – nach einer Bearbeitung
		// womöglich mit anderer Reichweite und neuem Schlüssel.
		z := feedZustandVon(rec, reakt)
		var kreis []string
		switch z.Sicht {
		case feedSichtKontak:
			if !kontakteGeladen {
				kontakte, kontakteGeladen = s.feedKontakte(sess), true
			}
			kreis = kontakte
		case feedSichtGruppe:
			kreis = gruppen[z.Gruppe]
		default:
			continue
		}
		if len(kreis) == 0 {
			continue
		}
		b := feedSchluesselbund(sess.identity, rec, z, reakt)
		if b.Aktuell == nil {
			continue
		}
		kid := feedKid(b.Aktuell)
		hat := map[string]bool{}
		for f := range z.Keys {
			hat[f] = true
		}
		for _, x := range reakt {
			if art, _ := x.Data["kind"].(string); art != "key" {
				continue
			}
			// Nur Freigaben für DIESEN Schlüssel zählen. Freigaben ohne
			// Kennung stammen aus R813 und passen nur zum Ursprung.
			xk, _ := x.Data["kid"].(string)
			if xk == kid || (xk == "" && !z.Bearbeitet) {
				zf, _ := x.Data["target"].(string)
				hat[zf] = true
			}
		}
		for _, ziel := range kreis {
			if hat[ziel] || neu >= 200 {
				continue
			}
			x, ok := schluessel[ziel]
			if !ok {
				if r, err := s.store.Get(storage.RecordKeyDir, "keydir:"+ziel); err == nil && r != nil {
					x, _ = r.Data["x25519"].(string)
				}
				if x == "" {
					x = s.keyDirHolen(ctx, ziel)
				}
				schluessel[ziel] = x
			}
			if x == "" {
				continue
			}
			g, err := feedGeheimnis(sess.identity, x)
			if err != nil {
				continue
			}
			v, err := identity.Encrypt(b.Aktuell, g)
			if err != nil {
				continue
			}
			if _, err := s.feedReaktionOhneAnfrage(sess, "key", rec.ID, ziel, hex.EncodeToString(v),
				map[string]any{"kid": kid}); err == nil {
				neu++
			}
		}
	}
	if neu > 0 && s.log != nil {
		s.log.Info("Feed: Beiträge für neue Kontakte freigegeben", zap.Int("freigaben", neu))
	}
}
