package api

// Sammelbestellungen (R696).
//
// Mehrere Haushalte bestellen gemeinsam, um den Mengenpreis zu bekommen:
// Heizöl, Pellets, Brennholz, Futter, Erde. Auf dem Land ist das gelebte
// Praxis und spart schnell mehrere hundert Euro je Haushalt.
//
// ── Warum der Händler KEINE Wallet braucht ─────────────────────────────────
//
// Das Schwierige an einer Sammelbestellung ist nie die Organisation, sondern
// wer das Geld einsammelt. Niemand will Kassenwart sein und mit fremdem Geld
// in Vorleistung gehen.
//
// Deshalb läuft die Hinterlegung zwischen den TEILNEHMERN und dem ORGANISATOR,
// nicht mit dem Händler. Jeder legt seinen Anteil hinterlegt bereit; der
// Organisator sieht die Summe, kann aber nicht darauf zugreifen, bis die
// Lieferung bestätigt ist. Der Händler bekommt sein Geld wie immer – per
// Überweisung oder bar bei Lieferung – und muss von Fundus nichts wissen.
//
// Das ist wichtig, weil die Funktion sonst nutzlos wäre: Kein Heizöllieferant
// richtet sich eine Wallet ein, nur weil ein Dorf gemeinsam bestellt.
//
// Wer lieber ohne Hinterlegung arbeitet, stellt die Zahlungsart auf "bar" oder
// "ueberweisung" – dann ist Fundus nur die Organisationshilfe.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/storage"
)

// gbWare: Vorschläge für das Bestellgut. Frei überschreibbar – die Liste soll
// den Einstieg erleichtern, nicht einschränken.
var gbWaren = []string{
	"Heizöl", "Holzpellets", "Brennholz", "Hackschnitzel", "Kohle",
	"Futtermittel", "Stroh", "Heu", "Dünger", "Blumenerde", "Rindenmulch",
	"Saatgut", "Kies", "Sand", "Zement", "Baustoffe",
	"Lebensmittel (Großgebinde)", "Getränke", "Sonstiges",
}

// gbEinheiten: übliche Maße je Ware.
var gbEinheiten = []string{"Liter", "kg", "Tonnen", "Schüttraummeter",
	"Raummeter", "Festmeter", "Säcke", "Paletten", "Stück", "Kartons"}

// gbTeilnehmer ist ein Mitbesteller.
type gbTeilnehmer struct {
	FID      string     `json:"fid"`
	Name     string     `json:"name"`
	Menge    float64    `json:"menge"`
	Beitritt time.Time  `json:"beitritt"`
	Bezahlt  float64    `json:"bezahlt_fnd,omitempty"`
	EscrowID string     `json:"escrow_id,omitempty"`
	BezahltAm *time.Time `json:"bezahlt_am,omitempty"`
}

// groupBuy ist eine Sammelbestellung.
type groupBuy struct {
	ID       string `json:"id"`
	OrgFID   string `json:"org_fid"`   // Organisator
	OrgName  string `json:"org_name"`
	// R697: Wallet des Organisators. Dorthin legen die Teilnehmer ihren Anteil
	// hinterlegt bereit. Sie wird beim Anlegen festgehalten, damit sie später
	// nicht unbemerkt gegen eine andere getauscht werden kann – sonst könnte
	// der Organisator nach dem Beitritt das Ziel des Geldes ändern.
	OrgWallet string `json:"org_wallet,omitempty"`
	Titel    string `json:"titel"`
	Ware     string `json:"ware"`      // frei, Vorschläge siehe gbWaren
	Einheit  string `json:"einheit"`
	Beschreibung string `json:"beschreibung,omitempty"`

	// Händler ist reine Information – er braucht keine Wallet und kein Konto.
	Haendler string `json:"haendler,omitempty"`

	MindestMenge float64 `json:"mindest_menge"` // ab hier lohnt die Bestellung
	MaxMenge     float64 `json:"max_menge,omitempty"`
	PreisProEinheit float64 `json:"preis_pro_einheit,omitempty"` // in FND, geschätzt
	Zahlungsart  string  `json:"zahlungsart"` // escrow | bar | ueberweisung

	PLZ      string    `json:"plz,omitempty"`
	Ort      string    `json:"ort,omitempty"`
	Frist    time.Time `json:"frist"`              // Annahmeschluss
	Liefertermin string `json:"liefertermin,omitempty"` // frei, z.B. "KW 48"

	Status   string         `json:"status"` // offen | bestellt | geliefert | abgesagt
	Teilnehmer []gbTeilnehmer `json:"teilnehmer"`

	Angelegt  time.Time  `json:"angelegt"`
	Bestelltam *time.Time `json:"bestellt_am,omitempty"`
	Geliefertam *time.Time `json:"geliefert_am,omitempty"`
	Notiz     string     `json:"notiz,omitempty"`
}

// gbSumme rechnet die bestellte Gesamtmenge zusammen.
func (g *groupBuy) gbSumme() float64 {
	var s float64
	for _, t := range g.Teilnehmer {
		s += t.Menge
	}
	return s
}

func (g *groupBuy) erreicht() bool { return g.gbSumme() >= g.MindestMenge }

// GET /api/v1/groupbuy/meta – Vorschläge für Ware und Einheit.
func (s *Server) gbMeta(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"waren": gbWaren, "einheiten": gbEinheiten})
}

func gbFromRecord(rec *storage.Record) *groupBuy {
	if rec == nil || rec.Data == nil {
		return nil
	}
	// Gleicher Weg wie bei Drive (rideFromRecord): über JSON, damit die
	// Umwandlung denselben Regeln folgt wie beim Speichern.
	raw, err := json.Marshal(rec.Data)
	if err != nil {
		return nil
	}
	var g groupBuy
	if json.Unmarshal(raw, &g) != nil {
		return nil
	}
	if g.ID == "" {
		g.ID = rec.ID
	}
	return &g
}

func (s *Server) gbLoad(id string) (*storage.Record, *groupBuy, error) {
	rec, err := s.store.Get(storage.RecordGroupBuy, id)
	if err != nil || rec == nil {
		return nil, nil, fmt.Errorf("Sammelbestellung nicht gefunden")
	}
	g := gbFromRecord(rec)
	if g == nil {
		return nil, nil, fmt.Errorf("Sammelbestellung unlesbar")
	}
	return rec, g, nil
}

// gbSave schreibt zurück. Nur der Besitzer-Node darf das – wie bei Drive.
func (s *Server) gbSave(rec *storage.Record, g *groupBuy) {
	if !s.driveIsOwner(rec) {
		if s.log != nil {
			s.log.Error("Sammelbestellung gehört einem anderen Node – nicht gespeichert",
				zap.String("id", rec.ID))
		}
		return
	}
	rec.Data = toMap(g)
	rec.UpdatedAt = time.Now()
	if s.node != nil {
		if sig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = sig
		}
	}
	if err := s.store.Put(rec); err != nil {
		return
	}
	s.drivePublish(rec)
}

// walletOfSession liefert die Wallet-Adresse der angemeldeten Identität.
func (s *Server) walletOfSession(c *gin.Context) string {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		return ""
	}
	// ChainAddr() wird lazy berechnet – vor dem Öffnen der Wallet kann sie leer
	// sein. Dann muss die Adresse vom Browser mitgegeben werden.
	return strings.ToLower(sess.identity.ChainAddr())
}

// GET /api/v1/groupbuy – offene und eigene Sammelbestellungen.
func (s *Server) gbList(c *gin.Context) {
	me := strings.ToLower(s.sessionFID(c))
	recs := s.listAll(storage.RecordGroupBuy)
	out := make([]gin.H, 0, len(recs))
	for _, rec := range recs {
		g := gbFromRecord(rec)
		if g == nil {
			continue
		}
		beteiligt := strings.EqualFold(g.OrgFID, me)
		for _, t := range g.Teilnehmer {
			if strings.EqualFold(t.FID, me) {
				beteiligt = true
				break
			}
		}
		// Abgesagte und gelieferte nur für Beteiligte.
		if (g.Status == "abgesagt" || g.Status == "geliefert") && !beteiligt {
			continue
		}
		m := toMap(g)
		m["summe"] = g.gbSumme()
		m["erreicht"] = g.erreicht()
		m["mein"] = strings.EqualFold(g.OrgFID, me)
		m["beteiligt"] = beteiligt
		// R697: Den eigenen Eintrag kennzeichnen UND die Kennungen fremder
		// Hinterlegungen entfernen. Sie gehen niemanden sonst etwas an, und mit
		// einer fremden Kennung ließe sich zumindest deren Zustand abfragen.
		if tl, ok := m["teilnehmer"].([]any); ok {
			for _, e := range tl {
				t, ok := e.(map[string]any)
				if !ok {
					continue
				}
				f, _ := t["fid"].(string)
				if strings.EqualFold(f, me) {
					t["ich"] = true
				} else {
					delete(t, "escrow_id")
				}
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["frist"].(time.Time)
		b, _ := out[j]["frist"].(time.Time)
		return a.Before(b)
	})
	c.JSON(http.StatusOK, gin.H{"bestellungen": out})
}

// POST /api/v1/groupbuy – anlegen.
func (s *Server) gbCreate(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	var g groupBuy
	if err := c.ShouldBindJSON(&g); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	g.Titel = strings.TrimSpace(g.Titel)
	g.Ware = strings.TrimSpace(g.Ware)
	g.Einheit = strings.TrimSpace(g.Einheit)
	if g.Titel == "" || g.Ware == "" || g.Einheit == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Titel, Ware und Einheit sind erforderlich."})
		return
	}
	if g.MindestMenge <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Bitte eine Mindestmenge angeben – ohne sie weiß niemand, " +
				"wann sich die Bestellung lohnt."})
		return
	}
	if g.Frist.IsZero() || g.Frist.Before(time.Now()) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bitte eine Frist in der Zukunft angeben."})
		return
	}
	switch g.Zahlungsart {
	case "escrow", "bar", "ueberweisung":
	default:
		g.Zahlungsart = "escrow"
	}
	// R697: Bei Hinterlegung muss die Wallet des Organisators feststehen –
	// sonst wüsste niemand, wohin der Anteil hinterlegt wird. Sie wird geprüft
	// und festgehalten; später lässt sie sich nicht mehr ändern.
	if g.Zahlungsart == "escrow" {
		w := strings.TrimSpace(g.OrgWallet)
		if w == "" {
			w = s.walletOfSession(c)
		}
		if _, _, err := s.resolvePayee(w); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Für die Hinterlegung wird deine Wallet-Adresse gebraucht: " + err.Error()})
			return
		}
		g.OrgWallet = strings.ToLower(w)
		if g.PreisProEinheit <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Bei Hinterlegung wird ein Preis je Einheit gebraucht – " +
					"sonst steht der zu hinterlegende Betrag nicht fest."})
			return
		}
	} else {
		g.OrgWallet = ""
	}

	g.ID = "groupbuy:" + generateID()
	g.OrgFID = strings.ToLower(sess.identity.FundusID)
	g.OrgName = s.displayNameFor(g.OrgFID)
	g.Status = "offen"
	g.Angelegt = time.Now()
	// Der Organisator wird NICHT automatisch eingetragen: Manche organisieren,
	// ohne selbst mitzubestellen. Er tritt wie alle anderen mit seiner Menge bei.
	if g.Teilnehmer == nil {
		g.Teilnehmer = []gbTeilnehmer{}
	}

	rec := &storage.Record{
		ID:        g.ID,
		Type:      storage.RecordGroupBuy,
		Data:      toMap(&g),
		UpdatedAt: time.Now(),
	}
	if s.node != nil {
		rec.OwnerID = s.nodeID()
		if sig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = sig
		}
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	s.drivePublish(rec)
	c.JSON(http.StatusOK, gin.H{"ok": true, "bestellung": toMap(&g)})
}

// POST /api/v1/groupbuy/:id/join   { menge }
func (s *Server) gbJoin(c *gin.Context) {
	s.gbAktion(c, "join")
}

// POST /api/v1/groupbuy/:id/leave
func (s *Server) gbLeave(c *gin.Context) {
	s.gbAktion(c, "leave")
}

// POST /api/v1/groupbuy/:id/ordered  – Organisator hat beim Händler bestellt.
func (s *Server) gbOrdered(c *gin.Context) {
	s.gbAktion(c, "ordered")
}

// POST /api/v1/groupbuy/:id/delivered – geliefert; gibt die Hinterlegungen frei.
func (s *Server) gbDelivered(c *gin.Context) {
	s.gbAktion(c, "delivered")
}

// POST /api/v1/groupbuy/:id/cancel
func (s *Server) gbCancel(c *gin.Context) {
	s.gbAktion(c, "cancel")
}

// gbAktion führt eine Zustandsänderung aus – und reicht sie an den
// Besitzer-Node weiter, wenn die Bestellung dort liegt (wie bei Drive, R644).
func (s *Server) gbAktion(c *gin.Context, aktion string) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	id := c.Param("id")
	var body map[string]any
	_ = c.ShouldBindJSON(&body)

	rec, g, err := s.gbLoad(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !s.driveIsOwner(rec) {
		if s.driveForward(c, rec, "gb:"+aktion, body) {
			return
		}
	}
	fid := strings.ToLower(sess.identity.FundusID)
	if msg, ok := s.gbApply(g, fid, aktion, body); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	s.gbSave(rec, g)
	m := toMap(g)
	m["summe"] = g.gbSumme()
	m["erreicht"] = g.erreicht()
	c.JSON(http.StatusOK, gin.H{"ok": true, "bestellung": m})
}

// gbApply ist die gemeinsame Logik – von der Schnittstelle UND vom
// P2P-Weg genutzt, damit beide Wege nie auseinanderlaufen.
func (s *Server) gbApply(g *groupBuy, fid, aktion string, body map[string]any) (string, bool) {
	istOrg := strings.EqualFold(g.OrgFID, fid)

	switch aktion {
	case "join":
		if g.Status != "offen" {
			return "Die Bestellung nimmt keine Teilnehmer mehr an.", false
		}
		if !g.Frist.IsZero() && time.Now().After(g.Frist) {
			return "Die Frist ist abgelaufen.", false
		}
		menge, _ := toFloatOK(body["menge"])
		if menge <= 0 {
			return "Bitte eine Menge größer null angeben.", false
		}
		if g.MaxMenge > 0 && g.gbSumme()+menge > g.MaxMenge {
			return fmt.Sprintf("Die Höchstmenge von %.0f %s wäre überschritten.",
				g.MaxMenge, g.Einheit), false
		}
		// Schon dabei? Dann Menge ändern statt doppelt eintragen.
		for i := range g.Teilnehmer {
			if strings.EqualFold(g.Teilnehmer[i].FID, fid) {
				// R697: Ist bereits hinterlegt, darf die Menge nicht einfach
				// geändert werden – der hinterlegte Betrag passt dann nicht mehr
				// zur Menge. Erst austreten (Erstattung), dann neu beitreten.
				if g.Teilnehmer[i].EscrowID != "" && g.Teilnehmer[i].Menge != menge {
					return "Dein Anteil ist bereits hinterlegt. Tritt erst aus – " +
						"der Betrag wird dir erstattet – und trag dich dann neu ein.", false
				}
				g.Teilnehmer[i].Menge = menge
				return "", true
			}
		}
		neu := gbTeilnehmer{
			FID: fid, Name: s.displayNameFor(fid), Menge: menge, Beitritt: time.Now(),
		}
		// R697: Hinterlegung zuordnen, falls eine angelegt wurde. Die
		// Hinterlegung selbst läuft über /api/v1/escrow – dort unterschreibt
		// der Teilnehmer mit SEINEN Schlüsselwörtern. Hier wird nur vermerkt,
		// welche Hinterlegung zu welchem Anteil gehört.
		if eid, _ := body["escrow_id"].(string); eid != "" {
			neu.EscrowID = eid
			if b, ok := toFloatOK(body["bezahlt_fnd"]); ok {
				neu.Bezahlt = b
			}
			jetzt := time.Now()
			neu.BezahltAm = &jetzt
		}
		g.Teilnehmer = append(g.Teilnehmer, neu)
		return "", true

	case "leave":
		if g.Status != "offen" {
			return "Nach der Bestellung beim Händler ist kein Austritt mehr möglich.", false
		}
		// R697: Austreten entfernt nur den Eintrag; das Geld holt sich der
		// Teilnehmer über die Hinterlegung selbst zurück (Storno), weil nur er
		// dafür unterschreiben kann.
		//
		// R776: ABER NICHT, solange eine Hinterlegung offen ist.
		//
		// Der Eintrag trägt die einzige Spur zur Hinterlegung (EscrowID). Wird
		// er gelöscht, bevor storniert wurde, weiß niemand mehr, welche
		// Hinterlegung zu dieser Bestellung gehörte – das Geld bliebe liegen.
		//
		// Die Reihenfolge (erst stornieren, dann austreten) stand bisher nur in
		// einem Kommentar. Auf eine Reihenfolge zu vertrauen, die man nirgends
		// erzwingt, ist bei Geld die falsche Entscheidung.
		for _, t := range g.Teilnehmer {
			if strings.EqualFold(t.FID, fid) && t.EscrowID != "" {
				return "Bitte zuerst die Hinterlegung stornieren – sonst geht " +
					"die Zuordnung zu deinem hinterlegten Betrag verloren. " +
					"Danach ist der Austritt möglich.", false
			}
		}
		out := g.Teilnehmer[:0]
		gefunden := false
		for _, t := range g.Teilnehmer {
			if strings.EqualFold(t.FID, fid) {
				gefunden = true
				continue
			}
			out = append(out, t)
		}
		g.Teilnehmer = out
		if !gefunden {
			return "Du bist bei dieser Bestellung nicht dabei.", false
		}
		return "", true

	case "ordered":
		if !istOrg {
			return "Nur der Organisator kann die Bestellung aufgeben.", false
		}
		if g.Status != "offen" {
			return "Die Bestellung ist bereits aufgegeben.", false
		}
		if !g.erreicht() {
			return fmt.Sprintf("Die Mindestmenge von %.0f %s ist noch nicht erreicht (%.0f).",
				g.MindestMenge, g.Einheit, g.gbSumme()), false
		}
		jetzt := time.Now()
		g.Status, g.Bestelltam = "bestellt", &jetzt
		return "", true

	case "delivered":
		if !istOrg {
			return "Nur der Organisator kann die Lieferung bestätigen.", false
		}
		if g.Status != "bestellt" {
			return "Erst bestellen, dann liefern.", false
		}
		// R697: Das gibt die Hinterlegungen NICHT frei. Freigeben kann nur
		// jeder Teilnehmer selbst, mit seinen eigenen Schlüsselwörtern – sonst
		// wäre die Hinterlegung wertlos, weil der Organisator sich das Geld
		// selbst überweisen könnte. Der Zustandswechsel sagt den Teilnehmern
		// nur, dass sie jetzt bestätigen können.
		jetzt := time.Now()
		g.Status, g.Geliefertam = "geliefert", &jetzt
		return "", true

	case "cancel":
		if !istOrg {
			return "Nur der Organisator kann absagen.", false
		}
		if g.Status == "geliefert" {
			return "Eine gelieferte Bestellung lässt sich nicht absagen.", false
		}
		g.Status = "abgesagt"
		return "", true
	}
	return "Unbekannte Aktion.", false
}
