package api

// Transaktionsindex (R823): alle Buchungen aller Adressen, abfragbar je Wallet.
//
// ── Was er ist ──────────────────────────────────────────────────────────────
// Abgeleitetes Wissen, kein Konsens: Jeder Node baut ihn für sich, aus den
// Blöcken, die er ohnehin aufbewahrt. Kein Chain-Reset, kein Formatwechsel;
// der Index lässt sich jederzeit wegwerfen und neu aufbauen.
//
// ── Wie er entsteht ─────────────────────────────────────────────────────────
// Ein eigener SCHATTENZUSTAND durchläuft die Blöcke noch einmal, mit
// chain.ApplyBlockErfasst. Die Chain misst dabei, was sie tatsächlich bucht –
// Gebühren, Validator-Anteile, Belohnungen werden also nicht nachgerechnet,
// sondern gemessen. Der Zustand der Chain selbst wird nie berührt.
//
// Erster Aufbau: ab Genesis, im Hintergrund, gedrosselt. Danach beim Start ab
// dem jüngsten geprüften Zustandsabbild bis zum letzten indexierten Block –
// nicht wieder von vorn. Neue Blöcke kommen alle paar Sekunden hinzu.
//
// ── Abspaltungen ────────────────────────────────────────────────────────────
// Passt ein neuer Block nicht an den letzten indexierten (anderer Vorgänger),
// wurde die Chain umgeschrieben: Der Index verwirft alles ab einem geprüften
// Abbild und rechnet von dort neu. Er zeigt nie Buchungen eines verworfenen
// Zweigs.

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type txIndexZustand struct {
	mu        sync.Mutex
	schatten  *chain.State
	kopf      *chain.BlockHeader
	bis       uint64 // zuletzt indexierter Block
	ziel      uint64 // Höhe der Chain beim letzten Lauf
	fehler    string
	gestartet bool
}

var txIdx txIndexZustand

func txIndexAdresse(a chain.Address) string { return "0x" + hex.EncodeToString(a[:]) }

// ── Einordnung ───────────────────────────────────────────────────────────────

// txArt ordnet eine Buchung ein. Kennungen statt Texte – die Oberfläche
// übersetzt sie.
func txArt(b chain.Buchung, feeCollector chain.Address) string {
	if b.TxIndex < 0 {
		return "stake_freigabe"
	}
	if b.Adresse == b.Von {
		return txTypArt(b.TxTyp)
	}
	switch {
	case b.TxTyp == chain.TxStorageReward:
		return "belohnung_vorhaltung"
	case b.Adresse == b.Proposer:
		return "belohnung_validator" // Anteil des Block-Erzeugers an der Gebühr
	case b.Adresse == feeCollector:
		return "gebuehr_einnahme"
	}
	return txTypArt(b.TxTyp)
}

func txTypArt(t chain.TxType) string {
	switch t {
	case chain.TxTransfer:
		return "ueberweisung"
	case chain.TxStake:
		return "stake"
	case chain.TxUnstake:
		return "unstake"
	case chain.TxSlash:
		return "slash"
	case chain.TxSolCredit:
		return "sol_gutschrift"
	case chain.TxStorageReward:
		return "belohnung_vorhaltung"
	case chain.TxEscrowOpen:
		return "hinterlegung"
	case chain.TxEscrowConfirm:
		return "freigabe"
	case chain.TxEscrowRefund, chain.TxEscrowConfirmReturn:
		return "erstattung"
	case chain.TxHTLCLock, chain.TxHTLCClaim, chain.TxHTLCRefund:
		return "tausch"
	}
	return "sonstiges"
}

// txEintraege macht aus den Buchungen eines Blocks die Index-Einträge.
// Beim Absender wird die Gebühr als EIGENE Zeile ausgewiesen, der Rest ist
// die eigentliche Bewegung.
func txEintraege(buchungen []chain.Buchung, feeCollector chain.Address) []*storage.Record {
	var out []*storage.Record
	// Je Transaktion die Beteiligten sammeln, um die Gegenseite zu nennen.
	parteien := map[int][]chain.Address{}
	for _, b := range buchungen {
		if b.Adresse != b.Proposer && b.Adresse != feeCollector {
			parteien[b.TxIndex] = append(parteien[b.TxIndex], b.Adresse)
		}
	}
	zaehler := map[string]int{}
	neu := func(b chain.Buchung, art string, betrag *big.Int) {
		adr := txIndexAdresse(b.Adresse)
		basis := fmt.Sprintf("%s/%012d-%05d", adr, b.Hoehe, b.TxIndex+1)
		n := zaehler[basis]
		zaehler[basis]++
		gegen := ""
		if b.Adresse != b.Von && b.TxIndex >= 0 && b.Von != (chain.Address{}) {
			gegen = txIndexAdresse(b.Von)
		} else {
			for _, p := range parteien[b.TxIndex] {
				if p != b.Adresse {
					gegen = txIndexAdresse(p)
					break
				}
			}
		}
		tx := ""
		if b.TxIndex >= 0 {
			tx = "0x" + hex.EncodeToString(b.TxHash[:])
		}
		out = append(out, &storage.Record{
			ID: fmt.Sprintf("%s-%02d", basis, n), Type: storage.RecordTxIndex,
			Data: map[string]any{
				"h": b.Hoehe, "t": b.Zeit, "tx": tx, "art": art, "typ": int(b.TxTyp),
				"u": betrag.String(), "fnd": uToFNDFloat(betrag), "gegen": gegen,
			},
		})
	}
	for _, b := range buchungen {
		art := txArt(b, feeCollector)
		if b.TxIndex >= 0 && b.Adresse == b.Von && b.Gebuehr != nil && b.Gebuehr.Sign() > 0 {
			neu(b, "gebuehr", new(big.Int).Neg(b.Gebuehr))
			rest := new(big.Int).Add(b.Betrag, b.Gebuehr)
			if rest.Sign() != 0 {
				neu(b, art, rest)
			}
			continue
		}
		neu(b, art, b.Betrag)
	}
	return out
}

// ── Aufbau ───────────────────────────────────────────────────────────────────

const txIndexStandID = "stand"

func (s *Server) txIndexStandLesen() (uint64, string) {
	rec, err := s.store.Get(storage.RecordTxIndexStand, txIndexStandID)
	if err != nil || rec == nil || rec.Data == nil {
		return 0, ""
	}
	h, _ := toFloatOK(rec.Data["hoehe"])
	k, _ := rec.Data["kopf"].(string)
	return uint64(h), k
}

func (s *Server) txIndexStandSchreiben(h uint64, kopf [32]byte) {
	_ = s.store.Put(&storage.Record{ID: txIndexStandID, Type: storage.RecordTxIndexStand,
		Data: map[string]any{"hoehe": h, "kopf": hex.EncodeToString(kopf[:])}})
}

// txIndexVerwerfenAb löscht alle Einträge oberhalb von h (nach einer Abspaltung).
func (s *Server) txIndexVerwerfenAb(h uint64) int {
	alle, _ := s.store.List(storage.RecordTxIndex)
	n := 0
	for _, r := range alle {
		if r == nil || r.Data == nil {
			continue
		}
		if hh, _ := toFloatOK(r.Data["h"]); uint64(hh) > h {
			_ = s.store.HardDelete(storage.RecordTxIndex, r.ID)
			n++
		}
	}
	return n
}

func (s *Server) txIndexSchleife() {
	defer func() {
		if r := recover(); r != nil && s.log != nil {
			s.log.Error("Transaktionsindex abgebrochen", zap.Any("grund", r))
		}
	}()
	// Den Start des Nodes nicht ausbremsen.
	time.Sleep(30 * time.Second)
	for {
		if s.chain != nil {
			if err := s.txIndexLauf(); err != nil {
				txIdx.mu.Lock()
				txIdx.fehler = err.Error()
				txIdx.schatten = nil // beim nächsten Lauf neu aufsetzen
				txIdx.mu.Unlock()
				if s.log != nil {
					s.log.Warn("Transaktionsindex: Lauf gescheitert", zap.Error(err))
				}
				time.Sleep(60 * time.Second)
				continue
			}
		}
		time.Sleep(5 * time.Second)
	}
}

func (s *Server) txIndexLauf() error {
	bc := s.chain
	stand, _ := s.txIndexStandLesen()

	txIdx.mu.Lock()
	schatten, kopf := txIdx.schatten, txIdx.kopf
	txIdx.mu.Unlock()
	if schatten == nil {
		st, k, err := bc.IndexStart(stand)
		if err != nil {
			return fmt.Errorf("Ausgangszustand: %w", err)
		}
		schatten, kopf = st, k
		if s.log != nil {
			s.log.Info("Transaktionsindex: Aufbau", zap.Uint64("ab_block", k.Height),
				zap.Uint64("indexiert_bis", stand), zap.Uint64("chain", bc.Height()))
		}
	}
	fc := bc.FeeCollectorAdresse()
	ziel := bc.Height()
	txIdx.mu.Lock()
	txIdx.ziel, txIdx.fehler = ziel, ""
	txIdx.mu.Unlock()

	for h := kopf.Height + 1; h <= ziel; h++ {
		blk, err := bc.GetBlock(h)
		if err != nil {
			return fmt.Errorf("Block %d: %w", h, err)
		}
		if blk.Header.PrevHash != kopf.Hash() {
			// Abspaltung: ab einem geprüften Abbild neu rechnen.
			st, k, err := bc.IndexStart(h - 1)
			if err != nil {
				return fmt.Errorf("nach Abspaltung: %w", err)
			}
			verworfen := s.txIndexVerwerfenAb(k.Height)
			s.txIndexStandSchreiben(k.Height, k.Hash())
			stand = k.Height
			schatten, kopf = st, k
			h = k.Height // die Schleife erhöht wieder
			if s.log != nil {
				s.log.Warn("Transaktionsindex: Abspaltung, neu ab Block", zap.Uint64("block", k.Height),
					zap.Int("verworfen", verworfen))
			}
			continue
		}
		buchungen, err := chain.ApplyBlockErfasst(kopf, blk, schatten, fc)
		if err != nil {
			return fmt.Errorf("Block %d anwenden: %w", h, err)
		}
		if h > stand {
			for _, r := range txEintraege(buchungen, fc) {
				if err := s.store.Put(r); err != nil {
					return fmt.Errorf("Eintrag schreiben: %w", err)
				}
			}
		}
		kopf = &blk.Header
		if h > stand && (h%200 == 0 || h == ziel) {
			s.txIndexStandSchreiben(h, kopf.Hash())
			stand = h
		}
		txIdx.mu.Lock()
		txIdx.schatten, txIdx.kopf, txIdx.bis = schatten, kopf, h
		txIdx.mu.Unlock()
		// Beim Aufholen drosseln, damit der Pi nebenbei noch Blöcke bauen
		// und Seiten ausliefern kann.
		if ziel-h > 50 && h%500 == 0 {
			time.Sleep(25 * time.Millisecond)
		}
	}
	txIdx.mu.Lock()
	txIdx.schatten, txIdx.kopf = schatten, kopf
	if kopf.Height > txIdx.bis {
		txIdx.bis = kopf.Height
	}
	txIdx.mu.Unlock()
	return nil
}

// ── Abfrage ──────────────────────────────────────────────────────────────────

var txAdrMuster = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// GET /api/v1/wallet/verlauf?address=0x…&vor=<Kennung>&limit=50
// Öffentliche Chain-Daten: jede Adresse darf abgefragt werden.
func (s *Server) walletVerlauf(c *gin.Context) {
	adr := strings.ToLower(strings.TrimSpace(c.Query("address")))
	if !txAdrMuster.MatchString(adr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Adresse."})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	vor := c.Query("vor")
	alle, err := s.store.ListUnterPfad(storage.RecordTxIndex, adr)
	if err != nil {
		s.internalError(c, err)
		return
	}
	sort.Slice(alle, func(i, j int) bool { return alle[i].ID > alle[j].ID }) // jüngste zuerst
	out := []gin.H{}
	weiter := ""
	for _, r := range alle {
		if vor != "" && r.ID >= vor {
			continue
		}
		if len(out) >= limit {
			weiter = out[len(out)-1]["id"].(string)
			break
		}
		e := gin.H{"id": r.ID}
		for k, v := range r.Data {
			e[k] = v
		}
		out = append(out, e)
	}
	txIdx.mu.Lock()
	bis, ziel, fehler := txIdx.bis, txIdx.ziel, txIdx.fehler
	txIdx.mu.Unlock()
	if bis == 0 {
		bis, _ = s.txIndexStandLesen()
	}
	c.JSON(http.StatusOK, gin.H{"eintraege": out, "weiter": weiter,
		"stand": gin.H{"bis": bis, "chain": ziel, "fehler": fehler}})
}

func (s *Server) registerTxIndex(r *gin.Engine) {
	txIdx.mu.Lock()
	if !txIdx.gestartet {
		txIdx.gestartet = true
		go s.txIndexSchleife()
	}
	txIdx.mu.Unlock()
	r.GET("/api/v1/wallet/verlauf", s.walletVerlauf)
}
