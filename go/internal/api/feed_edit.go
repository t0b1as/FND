package api

// Bearbeiten von Beiträgen und Kommentaren (R815).
//
// Ein Beitrag gehört dem Node, auf dem er entstand – andere Nodes dürfen den
// Datensatz nicht überschreiben (Besitzer-Schutz). Bearbeitungen sind deshalb
// eigene, vom AUTOR signierte Datensätze; die jüngste gilt. Wie beim Löschen
// funktioniert das so von jedem Node aus.
//
// ── Reichweite ändern ───────────────────────────────────────────────────────
// Jede Bearbeitung eines nicht öffentlichen Beitrags bekommt einen NEUEN
// Schlüssel. Wer aus der Reichweite entfernt wird, hat nur die alten und kann
// den neuen Stand nicht lesen. Damit ältere Kommentare für die (neue)
// Reichweite lesbar bleiben, trägt die Bearbeitung die früheren Schlüssel mit,
// verschlüsselt mit dem neuen. Wer neu hinzukommt, kann damit grundsätzlich
// auch frühere Fassungen lesen.
//
// Welcher Schlüssel zu einem Inhalt passt, wird durch Entschlüsseln geprüft:
// XChaCha20-Poly1305 ist authentifiziert, ein falscher Schlüssel scheitert
// sicher. Das spart eine Schlüsselkennung an jedem Kommentar.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
)

// feedZustand ist der gültige Stand eines Beitrags.
type feedZustand struct {
	Sicht      string         // public | contacts | group
	Gruppe     string         // Gruppen-ID bei group
	Nutzlast   string         // Klartext-JSON oder verschlüsselt (hex)
	Keys       map[string]any // verpackte Schlüssel des Stands
	Xpub       string         // X25519 des Autors
	Prev       string         // frühere Schlüssel, verschlüsselt mit dem aktuellen
	Ts         int64          // 0 = Ursprung
	Bearbeitet bool
	Quelle     string // für den Zwischenspeicher: wechselt mit jeder Bearbeitung
}

func feedKeys(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func feedZustandVon(rec *storage.Record, reakt []*storage.Record) feedZustand {
	d := rec.Data
	z := feedZustand{Keys: feedKeys(d["keys"]), Quelle: rec.ID}
	z.Sicht, _ = d["vis"].(string)
	z.Gruppe, _ = d["grp"].(string)
	z.Nutzlast, _ = d["payload"].(string)
	z.Xpub, _ = d["xpub"].(string)
	if z.Sicht == "" {
		z.Sicht = feedSichtOeff
	}
	autor, _ := d["fid"].(string)
	var best *storage.Record
	var bestTs int64 = -1
	for _, x := range reakt {
		if art, _ := x.Data["kind"].(string); art != "edit" {
			continue
		}
		if f, _ := x.Data["fid"].(string); f != autor {
			continue
		}
		if ts := zahl64(x.Data["ts"]); ts > bestTs {
			best, bestTs = x, ts
		}
	}
	if best != nil {
		ziel, _ := best.Data["target"].(string)
		z.Sicht, z.Gruppe = ziel, ""
		if strings.HasPrefix(ziel, "group:") {
			z.Sicht, z.Gruppe = feedSichtGruppe, strings.TrimPrefix(ziel, "group:")
		}
		z.Nutzlast, _ = best.Data["payload"].(string)
		z.Keys = feedKeys(best.Data["keys"])
		if x, _ := best.Data["xpub"].(string); x != "" {
			z.Xpub = x
		}
		z.Prev, _ = best.Data["prev"].(string)
		z.Ts, z.Bearbeitet = bestTs, true
		z.Quelle = best.ID + "@" + time.Unix(bestTs, 0).UTC().Format(time.RFC3339)
	}
	return z
}

// feedBund: alle Schlüssel, die ein Leser für einen Beitrag hat.
type feedBund struct {
	Aktuell []byte   // passt zum aktuellen Stand (nil bei öffentlich oder ohne Zugang)
	Alle    [][]byte // alle bekannten, auch frühere – für Kommentare
}

func feedKid(k []byte) string {
	h := sha256.Sum256(k)
	return hex.EncodeToString(h[:8])
}

var feedBundCache = map[string]feedBund{}

// feedSchluesselbund sammelt alle für mich verpackten Schlüssel: aus dem
// Ursprung, aus jeder Bearbeitung und aus nachträglichen Freigaben des Autors.
func feedSchluesselbund(id *identity.Identity, rec *storage.Record, z feedZustand, reakt []*storage.Record) feedBund {
	me := strings.ToLower(id.FundusID)
	ck := me + "|" + z.Quelle
	feedSchluessMu.Lock()
	if b, ok := feedBundCache[ck]; ok {
		feedSchluessMu.Unlock()
		return b
	}
	feedSchluessMu.Unlock()

	autor, _ := rec.Data["fid"].(string)
	xpub := z.Xpub
	if xpub == "" {
		xpub, _ = rec.Data["xpub"].(string)
	}
	var b feedBund
	gesehen := map[string]bool{}
	dazu := func(k []byte) {
		if len(k) == 32 && !gesehen[string(k)] {
			gesehen[string(k)] = true
			b.Alle = append(b.Alle, k)
		}
	}
	auspacken := func(verpackt string) {
		if verpackt == "" || xpub == "" {
			return
		}
		g, err := feedGeheimnis(id, xpub)
		if err != nil {
			return
		}
		roh, err := hex.DecodeString(verpackt)
		if err != nil {
			return
		}
		if k, err := identity.Decrypt(roh, g); err == nil {
			dazu(k)
		}
	}
	v, _ := feedKeys(rec.Data["keys"])[me].(string)
	auspacken(v)
	for _, x := range reakt {
		if f, _ := x.Data["fid"].(string); f != autor {
			continue // nur der Autor verteilt Schlüssel
		}
		switch art, _ := x.Data["kind"].(string); art {
		case "edit":
			v, _ := feedKeys(x.Data["keys"])[me].(string)
			auspacken(v)
		case "key":
			if ziel, _ := x.Data["target"].(string); ziel == me {
				v, _ := x.Data["payload"].(string)
				auspacken(v)
			}
		}
	}
	// Welcher passt zum aktuellen Stand?
	if z.Sicht != feedSichtOeff {
		if roh, err := hex.DecodeString(z.Nutzlast); err == nil {
			for _, k := range b.Alle {
				if _, err := identity.Decrypt(roh, k); err == nil {
					b.Aktuell = k
					break
				}
			}
		}
	}
	// Frühere Schlüssel aus der Bearbeitung (für ältere Kommentare).
	if b.Aktuell != nil && z.Prev != "" {
		if roh, err := hex.DecodeString(z.Prev); err == nil {
			if klar, err := identity.Decrypt(roh, b.Aktuell); err == nil {
				var alte []string
				if json.Unmarshal(klar, &alte) == nil {
					for _, a := range alte {
						if k, err := hex.DecodeString(a); err == nil {
							dazu(k)
						}
					}
				}
			}
		}
	}
	if z.Sicht == feedSichtOeff || b.Aktuell != nil {
		// Nur Erfolge merken – eine spätere Freigabe soll sofort wirken.
		feedSchluessMu.Lock()
		if len(feedBundCache) > 20000 {
			feedBundCache = map[string]feedBund{}
		}
		feedBundCache[ck] = b
		feedSchluessMu.Unlock()
	}
	return b
}

// feedEntschluesseln versucht alle Schlüssel des Bundes.
func feedEntschluesseln(hexText string, b feedBund) ([]byte, bool) {
	roh, err := hex.DecodeString(hexText)
	if err != nil {
		return nil, false
	}
	if b.Aktuell != nil {
		if klar, err := identity.Decrypt(roh, b.Aktuell); err == nil {
			return klar, true
		}
	}
	for _, k := range b.Alle {
		if klar, err := identity.Decrypt(roh, k); err == nil {
			return klar, true
		}
	}
	return nil, false
}

// feedZugang: darf ich den aktuellen Stand sehen, und mit welchen Schlüsseln?
func feedZugang(sess *Session, rec *storage.Record, reakt []*storage.Record) (bool, feedZustand, feedBund) {
	z := feedZustandVon(rec, reakt)
	if sess == nil || sess.identity == nil {
		return z.Sicht == feedSichtOeff, z, feedBund{}
	}
	b := feedSchluesselbund(sess.identity, rec, z, reakt)
	return z.Sicht == feedSichtOeff || b.Aktuell != nil, z, b
}

func feedInhaltLesen(z feedZustand, b feedBund) (feedInhalt, bool) {
	var inhalt feedInhalt
	if z.Sicht == feedSichtOeff {
		return inhalt, json.Unmarshal([]byte(z.Nutzlast), &inhalt) == nil
	}
	if b.Aktuell == nil {
		return inhalt, false
	}
	roh, err := hex.DecodeString(z.Nutzlast)
	if err != nil {
		return inhalt, false
	}
	klar, err := identity.Decrypt(roh, b.Aktuell)
	if err != nil {
		return inhalt, false
	}
	return inhalt, json.Unmarshal(klar, &inhalt) == nil
}

// feedKommentarText liefert den aktuellen, lesbaren Text eines Kommentars
// (nach Bearbeitung) – oder false, wenn er für mich nicht lesbar ist.
func feedKommentarText(kr *storage.Record, bearb map[string]*storage.Record, basisSicht string, b feedBund) (string, bool, bool) {
	quelle, bearbeitet := kr, false
	if e := bearb[kr.ID]; e != nil {
		quelle, bearbeitet = e, true
	}
	text, _ := quelle.Data["payload"].(string)
	verschl := false
	if f, ok := quelle.Data["enc"].(string); ok {
		verschl = f == "1"
	} else {
		// Kommentare vor R815 tragen keine Kennzeichnung: verschlüsselt, wenn der
		// Beitrag ursprünglich nicht öffentlich war.
		verschl = basisSicht != feedSichtOeff && basisSicht != ""
	}
	if !verschl {
		return text, bearbeitet, true
	}
	klar, ok := feedEntschluesseln(text, b)
	return string(klar), bearbeitet, ok
}

// feedKommentarBearbeitungen: je Kommentar die jüngste Bearbeitung seines Autors.
func feedKommentarBearbeitungen(reakt []*storage.Record) map[string]*storage.Record {
	autorVon := map[string]string{}
	for _, x := range reakt {
		if art, _ := x.Data["kind"].(string); art == "comment" {
			autorVon[x.ID], _ = x.Data["fid"].(string)
		}
	}
	out := map[string]*storage.Record{}
	for _, x := range reakt {
		if art, _ := x.Data["kind"].(string); art != "cedit" {
			continue
		}
		ziel, _ := x.Data["target"].(string)
		fid, _ := x.Data["fid"].(string)
		if autorVon[ziel] == "" || autorVon[ziel] != fid {
			continue
		}
		if alt := out[ziel]; alt == nil || zahl64(x.Data["ts"]) > zahl64(alt.Data["ts"]) {
			out[ziel] = x
		}
	}
	return out
}

// ── Verpacken für eine Reichweite ────────────────────────────────────────────

// feedVerpacken verschlüsselt den Inhalt mit einem neuen Schlüssel und verpackt
// ihn für den Kreis (plus den Autor selbst).
func (s *Server) feedVerpacken(c *gin.Context, sess *Session, inhalt []byte, kreis []string) (nutzlast string, keys map[string]string, k []byte, anzahl int, err error) {
	fid := strings.ToLower(sess.identity.FundusID)
	k = make([]byte, 32)
	if _, err = rand.Read(k); err != nil {
		return
	}
	enc, err := identity.Encrypt(inhalt, k)
	if err != nil {
		return
	}
	nutzlast = hex.EncodeToString(enc)
	meinX := sess.identity.X25519PublicKeyHex()
	keys = map[string]string{}
	for _, ziel := range append([]string{fid}, kreis...) {
		x := meinX
		if ziel != fid {
			x = s.feedX25519(c, ziel)
		}
		if x == "" {
			continue // ohne Schlüssel kein Zugang – lieber ausgelassen als offen
		}
		g, e := feedGeheimnis(sess.identity, x)
		if e != nil {
			continue
		}
		v, e := identity.Encrypt(k, g)
		if e != nil {
			continue
		}
		keys[ziel] = hex.EncodeToString(v)
		if ziel != fid {
			anzahl++
		}
	}
	return
}

// feedReichweite löst die Auswahl auf: Sicht, Gruppe, Kreis.
func (s *Server) feedReichweite(sess *Session, wahl string) (sicht, gruppeID, gruppeName string, kreis []string, fehler string) {
	if strings.HasPrefix(wahl, "group:") {
		gid := strings.TrimPrefix(wahl, "group:")
		for _, g := range s.feedGruppenLaden(sess) {
			if g.ID == gid {
				return feedSichtGruppe, g.ID, g.Name, g.Mitglieder, ""
			}
		}
		return "", "", "", nil, "Diese Gruppe gibt es nicht (mehr)."
	}
	if wahl == feedSichtKontak {
		return feedSichtKontak, "", "", s.feedKontakte(sess), ""
	}
	return feedSichtOeff, "", "", nil, ""
}

// ── Beitrag bearbeiten ───────────────────────────────────────────────────────

func (s *Server) feedBearbeiten(c *gin.Context) {
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
	fid := strings.ToLower(sess.identity.FundusID)
	if autor, _ := rec.Data["fid"].(string); autor != fid {
		c.JSON(http.StatusForbidden, gin.H{"error": "Nur der Autor kann bearbeiten."})
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
	if !feedDrosseln("edit", fid, 60, time.Hour) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Bearbeitungen in kurzer Zeit."})
		return
	}
	sicht, gid, gname, kreis, fehler := s.feedReichweite(sess, req.Sicht)
	if fehler != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": fehler})
		return
	}
	req.feedInhalt.Gruppe = gname
	inhalt, _ := json.Marshal(req.feedInhalt)

	reakt := s.feedReaktionenVon(beitrag)
	z := feedZustandVon(rec, reakt)
	alterBund := feedSchluesselbund(sess.identity, rec, z, reakt)

	ziel := sicht
	if sicht == feedSichtGruppe {
		ziel = "group:" + gid
	}
	extra := map[string]any{}
	var nutzlast string
	anzahl := 0
	if sicht == feedSichtOeff {
		nutzlast = string(inhalt)
	} else {
		var keys map[string]string
		var k []byte
		var err error
		nutzlast, keys, k, anzahl, err = s.feedVerpacken(c, sess, inhalt, kreis)
		if err != nil {
			s.internalError(c, err)
			return
		}
		extra["keys"] = keys
		extra["xpub"] = sess.identity.X25519PublicKeyHex()
		if gid != "" {
			extra["grp"] = gid
		}
		// Frühere Schlüssel mitgeben, damit ältere Kommentare lesbar bleiben.
		if len(alterBund.Alle) > 0 {
			var alte []string
			for _, a := range alterBund.Alle {
				alte = append(alte, hex.EncodeToString(a))
			}
			roh, _ := json.Marshal(alte)
			if enc, err := identity.Encrypt(roh, k); err == nil {
				extra["prev"] = hex.EncodeToString(enc)
			}
		}
	}
	if _, err := s.feedReaktionAnlegen(sess, "edit", beitrag, ziel, nutzlast, true, extra); err != nil {
		s.internalError(c, err)
		return
	}
	// Neue Mitglieder der Reichweite gleich freischalten lassen.
	feedZugangMu.Lock()
	delete(feedZugangZuletzt, fid)
	feedZugangMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true, "contacts": anzahl})
}

// ── Kommentar bearbeiten ─────────────────────────────────────────────────────

func (s *Server) feedKommentarBearbeiten(c *gin.Context) {
	sess := s.feedSitzung(c)
	if sess == nil {
		return
	}
	beitrag, kid := c.Param("id"), c.Param("cid")
	rec := s.feedBeitragLaden(beitrag)
	if rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Beitrag nicht gefunden."})
		return
	}
	reakt := s.feedReaktionenVon(beitrag)
	ok, z, b := feedZugang(sess, rec, reakt)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "Kein Zugang zu diesem Beitrag."})
		return
	}
	fid := strings.ToLower(sess.identity.FundusID)
	var kr *storage.Record
	for _, x := range reakt {
		if x.ID == kid {
			kr = x
			break
		}
	}
	if kr == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Kommentar nicht gefunden."})
		return
	}
	if art, _ := kr.Data["kind"].(string); art != "comment" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Das ist kein Kommentar."})
		return
	}
	if autor, _ := kr.Data["fid"].(string); autor != fid {
		c.JSON(http.StatusForbidden, gin.H{"error": "Nur der Autor kann bearbeiten."})
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
	if !feedDrosseln("comment", fid, 120, time.Hour) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Zu viele Kommentare in kurzer Zeit."})
		return
	}
	nutzlast, extra := req.Text, map[string]any{"enc": "0"}
	if z.Sicht != feedSichtOeff && b.Aktuell != nil {
		enc, err := identity.Encrypt([]byte(req.Text), b.Aktuell)
		if err != nil {
			s.internalError(c, err)
			return
		}
		nutzlast, extra["enc"] = hex.EncodeToString(enc), "1"
	}
	if _, err := s.feedReaktionAnlegen(sess, "cedit", beitrag, kid, nutzlast, true, extra); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
