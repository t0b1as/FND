package api

// Partnerprofile verschlüsselt (R939).
//
// Bis R938 gingen Spitzname, Alter, Bio, Hobbys, Werte, sexuelle Vorlieben,
// Ausbildung, Beruf und Bild-Kennungen im Klartext an alle Nodes. Jetzt sind
// diese Angaben in jeder Anzeige versiegelt (partner/seal.go); offen stehen nur
// Geschlecht, Altersklasse, Grobstandort und Suchwunsch – genug, um gefunden zu
// werden, aber nicht, um jemanden zu erkennen.
//
// Profilschlüssel: aus der Identität abgeleitet (sha256 über das eigene
// X25519-Geheimnis und eine Epoche). Jede Sitzung desselben Kontos kommt
// damit auf denselben Schlüssel, ohne ihn verteilen zu müssen. Für die
// Neuverteilung ohne Sitzung liegt er zusätzlich im Profil (seal_key) – das
// Profil verlässt den eigenen Node nur zum eigenen Heim-Node.
//
// Freigabe: Wer ein Profil sehen will, schickt eine Anfrage – und legt dabei
// den EIGENEN Schlüssel bei (wer fragt, zeigt sich). Der Gefragte sieht so das
// Profil des Anfragenden und entscheidet: freigeben (eigener Schlüssel zurück)
// oder ablehnen. Alles läuft als Ende-zu-Ende-verschlüsselte Messenger-
// Nachricht und landet bei Abwesenheit im Postfach. Entzug wechselt den
// Schlüssel (neue Epoche) und verteilt den neuen nur an die Verbleibenden.
//
// Schlüsselbund: welche Schlüssel man von anderen hat und wie die Anfragen
// stehen – selbst-verschlüsselt je Konto, über fundus.contacts an die eigenen
// Nodes verteilt (wie die Navigation, R928).

import (
	"context"
	"crypto/sha256"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/messenger"
	"github.com/fundus/node/internal/partner"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ── Profilschlüssel ──────────────────────────────────────────────────────────

func partnerSchluesselAbleiten(sess *Session, epoche int) ([]byte, error) {
	if sess == nil || sess.identity == nil {
		return nil, errors.New("partner: keine Sitzung")
	}
	geheim, err := sess.identity.ECDHSharedSecret(sess.identity.X25519PublicKeyHex())
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte("fundus/partner-seal/v1:"))
	h.Write(geheim)
	h.Write([]byte(":" + strconv.Itoa(epoche)))
	return h.Sum(nil), nil
}

func partnerEpoche(d map[string]any) int {
	if e := int(toFloat(d["seal_epoch"])); e > 0 {
		return e
	}
	return 1
}

// partnerSiegelSetzen trägt Schlüssel und Epoche ins Profil ein (Daten-Map).
// Liefert true, wenn sich etwas geändert hat.
func partnerSiegelSetzen(sess *Session, d map[string]any, epoche int) ([]byte, bool, error) {
	k, err := partnerSchluesselAbleiten(sess, epoche)
	if err != nil {
		return nil, false, err
	}
	kh := hex.EncodeToString(k)
	alt, _ := d["seal_key"].(string)
	if alt == kh && partnerEpoche(d) == epoche {
		return k, false, nil
	}
	d["seal_key"] = kh
	d["seal_epoch"] = epoche
	return k, true, nil
}

// partnerEigenerSchluessel liefert den aktuellen Profilschlüssel. Mit Sitzung
// wird er abgeleitet (und im Profil nachgetragen), ohne aus dem Profil gelesen.
func (s *Server) partnerEigenerSchluessel(sess *Session, profRec *storage.Record) ([]byte, error) {
	if profRec == nil || profRec.Data == nil {
		return nil, errors.New("partner: kein Profil")
	}
	if sess != nil && sess.identity != nil {
		k, geaendert, err := partnerSiegelSetzen(sess, profRec.Data, partnerEpoche(profRec.Data))
		if err != nil {
			return nil, err
		}
		if geaendert {
			_ = s.store.Put(profRec)
			s.pushPartnerToHome(sess, profRec.Data)
		}
		return k, nil
	}
	kh, _ := profRec.Data["seal_key"].(string)
	k, err := hex.DecodeString(kh)
	if err != nil || len(k) != partner.SealKeyLen {
		return nil, errors.New("partner: noch kein Profilschlüssel (Besitzer seit dem Update nicht angemeldet)")
	}
	return k, nil
}

// ── Schlüsselbund ────────────────────────────────────────────────────────────

type partnerEintrag struct {
	K   string    `json:"k,omitempty"`   // Schlüssel (nur in Lesen)
	Kid string    `json:"kid,omitempty"` // Kennung dazu
	S   string    `json:"s,omitempty"`   // Status
	T   time.Time `json:"t"`
	N   string    `json:"n,omitempty"`   // Spitzname
}

type partnerBund struct {
	Lesen map[string]partnerEintrag `json:"lesen"` // fid → Schlüssel, mit dem ich dessen Profil lese
	Ein   map[string]partnerEintrag `json:"ein"`   // Anfragen an mich: offen | frei | abgelehnt
	Aus   map[string]partnerEintrag `json:"aus"`   // meine Anfragen: angefragt | frei | abgelehnt | entzogen
	Frei  map[string]time.Time      `json:"frei"`  // wem ich meinen Schlüssel gegeben habe
}

func (b *partnerBund) init() {
	if b.Lesen == nil {
		b.Lesen = map[string]partnerEintrag{}
	}
	if b.Ein == nil {
		b.Ein = map[string]partnerEintrag{}
	}
	if b.Aus == nil {
		b.Aus = map[string]partnerEintrag{}
	}
	if b.Frei == nil {
		b.Frei = map[string]time.Time{}
	}
}

var partnerBundMu sync.Mutex

func partnerBundID(fid string) string { return "partnerkeys:" + fid }

func (s *Server) partnerBundLaden(sess *Session) *partnerBund {
	b := &partnerBund{}
	b.init()
	if sess == nil || sess.identity == nil || s.store == nil {
		return b
	}
	fid := strings.ToLower(sess.identity.FundusID)
	rec, err := s.store.Get(storage.RecordPartnerKeys, partnerBundID(fid))
	if err != nil || rec == nil || rec.Data == nil || rec.DeletedAt != nil {
		return b
	}
	encHex, _ := rec.Data["enc"].(string)
	enc, err := hex.DecodeString(encHex)
	if err != nil {
		return b
	}
	plain, err := sess.identity.SelfDecrypt(enc)
	if err != nil {
		return b
	}
	_ = json.Unmarshal(plain, b)
	b.init()
	return b
}

func (s *Server) partnerBundSpeichern(sess *Session, b *partnerBund) error {
	fid := strings.ToLower(sess.identity.FundusID)
	plain, _ := json.Marshal(b)
	enc, err := sess.identity.SelfEncrypt(plain)
	if err != nil {
		return err
	}
	rec := &storage.Record{ID: partnerBundID(fid), Type: storage.RecordPartnerKeys,
		Data: map[string]any{"enc": hex.EncodeToString(enc), "owner": fid}}
	rec.UpdatedAt = time.Now().UTC()
	if err := s.store.Put(rec); err != nil {
		return err
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
	return nil
}

// partnerBundAendern: lesen – ändern – speichern unter Sperre.
func (s *Server) partnerBundAendern(sess *Session, f func(b *partnerBund)) (*partnerBund, error) {
	partnerBundMu.Lock()
	defer partnerBundMu.Unlock()
	b := s.partnerBundLaden(sess)
	f(b)
	return b, s.partnerBundSpeichern(sess, b)
}

// partnerLeseSchluessel: Schlüssel für das Profil von fid (nil, wenn keiner).
func (b *partnerBund) leseSchluessel(fid string) []byte {
	e, ok := b.Lesen[fid]
	if !ok || e.K == "" {
		return nil
	}
	k, err := hex.DecodeString(e.K)
	if err != nil || len(k) != partner.SealKeyLen {
		return nil
	}
	return k
}

// status: Stand der Freigabe gegenüber fid, aus Sicht des Lesers.
func (b *partnerBund) status(fid string, entsiegelt, schluesselAlt bool) string {
	switch {
	case entsiegelt:
		return "frei"
	case schluesselAlt:
		return "veraltet"
	}
	if e, ok := b.Ein[fid]; ok && e.S == "offen" {
		return "anfrage_offen"
	}
	if e, ok := b.Aus[fid]; ok && e.S != "" {
		return e.S
	}
	return ""
}

// ── Nachrichten ──────────────────────────────────────────────────────────────

// partnerEmpfaengerSchluessel sucht den X25519-Schlüssel eines Kontos
// (lokales Verzeichnis, Peer-Anfrage, DHT, Nachbarn – wie beim Messenger).
func (s *Server) partnerEmpfaengerSchluessel(ctx context.Context, fid string) string {
	rec, _ := s.store.Get(storage.RecordKeyDir, "keydir:"+fid)
	if rec == nil && s.node != nil {
		if raw := s.node.RequestKeyDir(ctx, fid); len(raw) > 0 {
			var dr storage.Record
			if json.Unmarshal(raw, &dr) == nil && dr.ID != "" {
				_ = s.store.Put(&dr)
				rec = &dr
			}
		}
	}
	if rec == nil && s.node != nil {
		if raw, err := s.node.DHTget(ctx, "/fundus/keydir/"+fid); err == nil && len(raw) > 0 {
			var dr storage.Record
			if json.Unmarshal(raw, &dr) == nil && dr.ID != "" {
				_ = s.store.Put(&dr)
				rec = &dr
			}
		}
	}
	if rec != nil {
		if x, _ := rec.Data["x25519"].(string); x != "" {
			return x
		}
	}
	return s.keyDirHolen(ctx, fid)
}

// partnerSenden verschickt eine Partner-Nachricht Ende-zu-Ende und legt sie
// zusätzlich ins Postfach des Empfängers (kommt auch an, wenn er offline ist).
func (s *Server) partnerSenden(ctx context.Context, sess *Session, an string, pm messenger.PartnerMsg) error {
	if sess == nil || sess.messenger == nil {
		return errors.New("Messenger nicht verfügbar")
	}
	pub := s.partnerEmpfaengerSchluessel(ctx, an)
	if pub == "" {
		return errors.New("Schlüssel des Empfängers unbekannt – er war seit Längerem nicht online")
	}
	msg, err := sess.messenger.SendPartner(ctx, an, pub, pm)
	if err != nil {
		return err
	}
	env, err := json.Marshal(msg)
	if err != nil {
		return nil
	}
	var rnd [8]byte
	_, _ = crand.Read(rnd[:])
	mbID := "mailbox:" + an + ":" + strconv.FormatInt(time.Now().UnixNano(), 36) + hex.EncodeToString(rnd[:])
	mbRec := &storage.Record{ID: mbID, Type: storage.RecordMailbox,
		Data: map[string]any{"recipient": an, "envelope": string(env), "ts": time.Now().Unix()}}
	// Empfänger auf diesem Node angemeldet? Sofort einspeisen (wie R810).
	sessionMu.RLock()
	empf := sessionStore[an]
	sessionMu.RUnlock()
	if empf != nil && empf.messenger != nil && empf != sess {
		envKopie := append([]byte(nil), env...)
		go func() {
			defer func() { _ = recover() }()
			empf.messenger.InjectIncoming(envKopie)
		}()
	}
	go func() {
		defer func() { _ = recover() }()
		_ = s.store.Put(mbRec)
		if s.node != nil {
			if raw, me := json.Marshal(mbRec); me == nil {
				c2, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_ = s.node.Publish(c2, "fundus.mailbox", raw)
				cancel()
			}
		}
	}()
	return nil
}

func gueltigerPartnerSchluessel(kh string) bool {
	k, err := hex.DecodeString(kh)
	return err == nil && len(k) == partner.SealKeyLen
}

func kurzName(n string) string {
	n = strings.TrimSpace(n)
	if r := []rune(n); len(r) > 40 {
		n = string(r[:40])
	}
	return n
}

// partnerNachricht verarbeitet eine eingehende Partner-Nachricht.
func (s *Server) partnerNachricht(sess *Session, von string, pm *messenger.PartnerMsg, ts time.Time) {
	if sess == nil || sess.identity == nil || pm == nil {
		return
	}
	von = strings.ToLower(von)
	if von == "" || von == strings.ToLower(sess.identity.FundusID) {
		return
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	name := kurzName(pm.Name)
	melden := ""
	_, err := s.partnerBundAendern(sess, func(b *partnerBund) {
		switch pm.Art {
		case "anfrage", "freigabe", "schluessel":
			if !gueltigerPartnerSchluessel(pm.Schluessel) {
				return
			}
			if pm.Art == "schluessel" {
				// Neuer Schlüssel nach einem Wechsel – nur annehmen, wenn wir
				// schon einen von diesem Konto hatten.
				if _, ok := b.Lesen[von]; !ok {
					return
				}
			}
			// Postfach-Nachrichten können nach einem Neustart erneut ankommen:
			// Eine ältere Nachricht darf weder einen neueren Schlüssel
			// überschreiben noch eine Entscheidung (Ablehnung) zurücksetzen.
			if e, ok := b.Lesen[von]; ok && !ts.After(e.T) {
				return
			}
			if pm.Art == "anfrage" {
				if e, ok := b.Ein[von]; ok && !ts.After(e.T) {
					return
				}
			}
			e := b.Lesen[von]
			e.K, e.Kid, e.T = strings.ToLower(pm.Schluessel), pm.KeyID, ts
			if name != "" {
				e.N = name
			}
			b.Lesen[von] = e
			switch pm.Art {
			case "anfrage":
				if _, schonGeteilt := b.Frei[von]; schonGeteilt {
					// Ich hatte selbst schon angefragt/freigegeben → gegenseitig.
					b.Ein[von] = partnerEintrag{S: "frei", T: ts, N: name}
					if a, ok := b.Aus[von]; ok {
						a.S = "frei"
						b.Aus[von] = a
					}
					melden = "frei"
				} else {
					b.Ein[von] = partnerEintrag{S: "offen", T: ts, N: name}
					melden = "anfrage"
				}
			case "freigabe":
				a := b.Aus[von]
				a.S, a.T = "frei", ts
				if name != "" {
					a.N = name
				}
				b.Aus[von] = a
				if e, ok := b.Ein[von]; ok {
					e.S = "frei"
					b.Ein[von] = e
				}
				melden = "frei"
			}
		case "ablehnung":
			if a, ok := b.Aus[von]; ok && ts.After(a.T) {
				a.S, a.T = "abgelehnt", ts
				b.Aus[von] = a
			}
		case "entzug":
			if e, ok := b.Lesen[von]; ok && !ts.After(e.T) {
				return
			}
			delete(b.Lesen, von)
			if a, ok := b.Aus[von]; ok {
				a.S, a.T = "entzogen", ts
				b.Aus[von] = a
			}
			if e, ok := b.Ein[von]; ok && e.S == "frei" {
				e.S = "entzogen"
				b.Ein[von] = e
			}
		}
	})
	if err != nil && s.log != nil {
		s.log.Warn("Partner-Freigabe nicht gespeichert", zap.Error(err))
		return
	}
	if s.log != nil {
		s.log.Info("Partner-Nachricht", zap.String("art", pm.Art), zap.String("von", von[:min(10, len(von))]+"…"))
	}
	wer := name
	if wer == "" {
		wer = "Jemand"
	}
	switch melden {
	case "anfrage":
		s.notifyUser(sess.identity.FundusID, "messages", "💞 Partner-Anfrage",
			wer+" möchte dein Partnerprofil sehen", "/partner?tab=freigaben", "partner", false)
	case "frei":
		s.notifyUser(sess.identity.FundusID, "messages", "💞 Profil freigegeben",
			wer+" hat dir das Partnerprofil freigegeben", "/partner?tab=matches", "partner", false)
	}
	sess.broadcastWS(gin.H{"type": "partner", "art": pm.Art, "von": von})
}

// ── Neuverteilung ────────────────────────────────────────────────────────────

// partnerAnzeigenVerteilen baut beide Anzeigen versiegelt neu und verteilt sie.
func (s *Server) partnerAnzeigenVerteilen(sess *Session) error {
	if sess == nil || sess.identity == nil {
		return errors.New("partner: keine Sitzung")
	}
	fid := strings.ToLower(sess.identity.FundusID)
	profRec, err := s.store.Get(storage.RecordPartnerProfile, "profile:"+fid)
	if err != nil || profRec == nil {
		return errors.New("partner: kein Profil")
	}
	if _, err := s.partnerEigenerSchluessel(sess, profRec); err != nil {
		return err
	}
	matchRaw, searchRaw, ok := s.buildOwnPartnerAds(fid)
	if !ok {
		return errors.New("partner: Anzeige nicht baubar")
	}
	_ = s.store.Put(&storage.Record{ID: "my-ad:" + fid, Type: storage.RecordPartnerAd, Data: map[string]any{"raw": string(matchRaw)}})
	_ = s.store.Put(&storage.Record{ID: "my-search-ad:" + fid, Type: storage.RecordPartnerSearchAd, Data: map[string]any{"raw": string(searchRaw)}})
	if s.node != nil {
		node := s.node
		go func() {
			defer func() { _ = recover() }()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = node.Publish(ctx, "fundus.partner", matchRaw)
			_ = node.Publish(ctx, "fundus.partner.search", searchRaw)
		}()
	}
	return nil
}

// partnerNachAnmeldung: nach dem Login den Schlüssel sicherstellen und, falls
// ein Profil existiert, die Anzeigen versiegelt neu verteilen. Altbestand aus
// R938 und früher (Klartext) wird so beim ersten Login ersetzt.
func (s *Server) partnerNachAnmeldung(sess *Session) {
	defer func() { _ = recover() }()
	if sess == nil || sess.identity == nil || s.store == nil {
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	if rec, err := s.store.Get(storage.RecordPartnerProfile, "profile:"+fid); err != nil || rec == nil {
		return
	}
	if err := s.partnerAnzeigenVerteilen(sess); err != nil && s.log != nil {
		s.log.Info("Partner-Anzeigen nach Anmeldung nicht verteilt", zap.Error(err))
	}
}

// ── Endpunkte ────────────────────────────────────────────────────────────────

// GET /api/v1/partner/freigaben
func (s *Server) partnerFreigabenZeigen(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte anmelden."})
		return
	}
	b := s.partnerBundLaden(sess)
	type zeile struct {
		FundusID string    `json:"fundus_id"`
		Name     string    `json:"name,omitempty"`
		Status   string    `json:"status"`
		Zeit     time.Time `json:"zeit"`
		Lesbar   bool      `json:"lesbar"`
	}
	name := func(fid, n string) string {
		if n != "" {
			return n
		}
		if e, ok := b.Lesen[fid]; ok {
			return e.N
		}
		return ""
	}
	ein, aus, frei := []zeile{}, []zeile{}, []zeile{}
	for fid, e := range b.Ein {
		ein = append(ein, zeile{FundusID: fid, Name: name(fid, e.N), Status: e.S, Zeit: e.T, Lesbar: b.leseSchluessel(fid) != nil})
	}
	for fid, e := range b.Aus {
		aus = append(aus, zeile{FundusID: fid, Name: name(fid, e.N), Status: e.S, Zeit: e.T, Lesbar: b.leseSchluessel(fid) != nil})
	}
	for fid, t := range b.Frei {
		n := ""
		if e, ok := b.Ein[fid]; ok {
			n = e.N
		} else if e, ok := b.Aus[fid]; ok {
			n = e.N
		}
		frei = append(frei, zeile{FundusID: fid, Name: name(fid, n), Status: "frei", Zeit: t, Lesbar: b.leseSchluessel(fid) != nil})
	}
	offen := 0
	for _, e := range b.Ein {
		if e.S == "offen" {
			offen++
		}
	}
	c.JSON(http.StatusOK, gin.H{"ein": ein, "aus": aus, "frei": frei, "offen": offen})
}

// POST /api/v1/partner/freigabe {fundus_id, aktion: teilen|ablehnen|entziehen}
func (s *Server) partnerFreigabeAendern(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte anmelden."})
		return
	}
	var req struct {
		FundusID string `json:"fundus_id"`
		Aktion   string `json:"aktion"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Anfrage."})
		return
	}
	an := strings.ToLower(strings.TrimSpace(req.FundusID))
	ich := strings.ToLower(sess.identity.FundusID)
	if an == "" || an == ich || len(an) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiges Konto."})
		return
	}
	profRec, err := s.store.Get(storage.RecordPartnerProfile, "profile:"+ich)
	if err != nil || profRec == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Lege zuerst dein eigenes Profil an – wer anfragt, zeigt sich auch."})
		return
	}
	meinName := kurzName(str(profRec.Data["nickname"]))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	switch req.Aktion {
	case "teilen":
		k, err := s.partnerEigenerSchluessel(sess, profRec)
		if err != nil {
			s.internalError(c, err)
			return
		}
		b := s.partnerBundLaden(sess)
		art := "anfrage"
		if e, ok := b.Ein[an]; ok && (e.S == "offen" || e.S == "abgelehnt") {
			art = "freigabe"
		}
		if err := s.partnerSenden(ctx, sess, an, messenger.PartnerMsg{
			Art: art, Schluessel: hex.EncodeToString(k), KeyID: partner.KeyID(k), Name: meinName}); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		_, err = s.partnerBundAendern(sess, func(b *partnerBund) {
			now := time.Now().UTC()
			b.Frei[an] = now
			if art == "freigabe" {
				e := b.Ein[an]
				e.S = "frei"
				b.Ein[an] = e
			} else if a, ok := b.Aus[an]; !ok || a.S != "frei" {
				b.Aus[an] = partnerEintrag{S: "angefragt", T: now, N: a.N}
			}
		})
		if err != nil {
			s.internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "art": art})

	case "ablehnen":
		_ = s.partnerSenden(ctx, sess, an, messenger.PartnerMsg{Art: "ablehnung", Name: meinName})
		_, err := s.partnerBundAendern(sess, func(b *partnerBund) {
			e := b.Ein[an]
			e.S, e.T = "abgelehnt", time.Now().UTC()
			b.Ein[an] = e
			delete(b.Lesen, an)
		})
		if err != nil {
			s.internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})

	case "entziehen":
		// 1. Aus der Freigabeliste nehmen, 2. Schlüssel wechseln, 3. Anzeigen
		// neu versiegeln, 4. neuen Schlüssel an die Verbleibenden schicken.
		var bleiben []string
		_, err := s.partnerBundAendern(sess, func(b *partnerBund) {
			delete(b.Frei, an)
			if e, ok := b.Ein[an]; ok {
				e.S = "entzogen"
				b.Ein[an] = e
			}
			for f := range b.Frei {
				bleiben = append(bleiben, f)
			}
		})
		if err != nil {
			s.internalError(c, err)
			return
		}
		k, _, err := partnerSiegelSetzen(sess, profRec.Data, partnerEpoche(profRec.Data)+1)
		if err != nil {
			s.internalError(c, err)
			return
		}
		_ = s.store.Put(profRec)
		s.pushPartnerToHome(sess, profRec.Data)
		if err := s.partnerAnzeigenVerteilen(sess); err != nil {
			s.internalError(c, err)
			return
		}
		_ = s.partnerSenden(ctx, sess, an, messenger.PartnerMsg{Art: "entzug", Name: meinName})
		fehler := 0
		for _, f := range bleiben {
			if s.partnerSenden(ctx, sess, f, messenger.PartnerMsg{Art: "schluessel",
				Schluessel: hex.EncodeToString(k), KeyID: partner.KeyID(k), Name: meinName}) != nil {
				fehler++
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "neu_verteilt": len(bleiben) - fehler, "fehler": fehler})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unbekannte Aktion."})
	}
}
