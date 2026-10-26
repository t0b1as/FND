package api

// Zähler-Zertifikate (R827): Ablesungen aus dem Telegramm → Chain.
//
// ── Ablauf ──────────────────────────────────────────────────────────────────
// 1. Der Zählerleser unterschreibt jede Ablesung mit seinem Ed25519-
//    Geräteschlüssel (meter/geraet.go) – Zählernummer aus dem Telegramm,
//    Zählwerke als ganze Wattsekunden.
// 2. Sobald Einspeisung vorliegt, registriert der Node den Zähler auf der
//    Chain (TxMeterRegister), mit der Zählerkennung aus der Zählernummer.
// 3. Einmal je Periode (Standard 60 min, FUNDUS_ZAEHLER_PERIODE_MIN)
//    zertifiziert er die eingespeiste Menge seit dem letzten Endstand
//    (TxCommodityCertify). Die Chain prüft wie gehabt: lückenlos, monoton,
//    jede Periode nur einmal.
// 4. Die beiden signierten Ablesungen der Periode legt er als BELEG ins Netz.
//    Dessen SHA-256 steht als Beweisanker (RawDataHash) in der Zertifizierung.
//
// ── Prüfen kann jeder ───────────────────────────────────────────────────────
// Beleg holen → Hash gleich dem Anker auf der Chain? Beide Unterschriften des
// Geräts gültig? Menge auf der Chain gleich der Differenz der Zählwerke?
// Zählerkennung gleich der aus der Zählernummer abgeleiteten?
//
// ── Keine Konsensänderung ───────────────────────────────────────────────────
// Die Chain-Regeln sind unverändert; die Gerätesignatur kommt über den
// vorhandenen Beweisanker hinein. Kein gleichzeitiges Update aller Nodes nötig.
//
// ── Eigene Zähler-Wallet ────────────────────────────────────────────────────
// Der Node reicht mit seinem Hauptschlüssel bereits Speicherbelohnungen ein,
// mit der jeweils aktuellen Nonce. Zähler-Transaktionen mit demselben
// Schlüssel kämen sich damit in die Quere. Deshalb ein eigener Schlüssel,
// abgeleitet aus dem Node-Schlüssel (R830) – er ist der "Erzeuger" auf der
// Chain und lässt sich aus den Seed-Wörtern jederzeit wiederherstellen.
//
// ── Was es NICHT beweist ────────────────────────────────────────────────────
// Das Telegramm der optischen Schnittstelle trägt keine Unterschrift des
// Zählers. Bewiesen ist: DIESES Lesegerät hat diese Stände gemessen und
// unterschrieben – nicht, dass niemand zwischen Zähler und Lesegerät eingriff.

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/meter"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"lukechampine.com/blake3"
)

var (
	zaehlerMu      sync.Mutex
	zaehlerAktuell = map[string]meter.Token{} // Zählernummer → jüngste signierte Ablesung
)

// AblesungEingang nimmt eine Ablesung des Zählerlesers entgegen (main.go).
// Nur unterschriebene Ablesungen mit Zählernummer aus dem Telegramm zählen.
func AblesungEingang(t meter.Token) {
	nr := strings.TrimSpace(t.MeterID)
	if t.GeraetSig == "" || nr == "" {
		return
	}
	zaehlerMu.Lock()
	zaehlerAktuell[nr] = t
	zaehlerMu.Unlock()
}

// ── Beleg ────────────────────────────────────────────────────────────────────

type zaehlerBelegStand struct {
	Zeit          uint64 `json:"t"`
	WsBezug       string `json:"ws_bezug"`
	WsEinspeisung string `json:"ws_einspeisung"`
	Sig           string `json:"sig"` // leer = ohne Gerätesignatur (Anfangsstand nicht mehr vorhanden)
}

type zaehlerBeleg struct {
	V         int               `json:"v"`
	Zaehler   string            `json:"zaehler"`    // Zählernummer aus dem Telegramm
	ZaehlerID string            `json:"zaehler_id"` // 11 Byte hex, aus der Nummer abgeleitet
	Geraet    string            `json:"geraet"`     // Ed25519-Schlüssel des Lesegeräts (hex)
	A         zaehlerBelegStand `json:"a"`
	B         zaehlerBelegStand `json:"b"`
}

func belegStandAus(t meter.Token) zaehlerBelegStand {
	return zaehlerBelegStand{Zeit: uint64(t.Timestamp.Unix()), WsBezug: t.WsBezug,
		WsEinspeisung: t.WsEinspeisung, Sig: t.GeraetSig}
}

// ── Zähler-Wallet ────────────────────────────────────────────────────────────

// zaehlerWallet leitet die Zähler-Wallet aus dem Node-Schlüssel ab (R830) –
// und damit aus den gesicherten Seed-Wörtern. Keine Schlüsseldatei mehr.
//
// Eigener Schlüssel statt des Hauptschlüssels, weil der Node mit diesem schon
// Speicherbelohnungen einreicht; gemeinsame Nonces kämen sich in die Quere.
// Abgeleitet heißt: dieselben Wörter ergeben auf jedem Node dieselbe
// Zähler-Wallet, und ein Node-Ersatz übernimmt seine Zähler nahtlos.
func (s *Server) zaehlerWallet() (*ecdsa.PrivateKey, chain.Address, error) {
	if s.fileStore == nil {
		return nil, chain.Address{}, errors.New("kein Datei-Speicher – keine Node-Wurzel")
	}
	wurzel, _ := s.fileStore.ConsensusSigner()
	if wurzel == nil {
		return nil, chain.Address{}, errors.New("Node-Schlüssel nicht verfügbar (Seed noch verschlüsselt?)")
	}
	roh := ethcrypto.FromECDSA(wurzel)
	// Ableiten; im (praktisch unmöglichen) Fall eines ungültigen Skalars mit
	// Zähler weiter, damit das Ergebnis trotzdem deterministisch bleibt.
	for i := byte(0); i < 16; i++ {
		h := blake3.New(32, nil)
		h.Write([]byte("fundus-zaehler-wallet-v1:"))
		h.Write(roh)
		h.Write([]byte{i})
		if k, err := ethcrypto.ToECDSA(h.Sum(nil)); err == nil {
			return k, chain.PubkeyToAddress(&k.PublicKey), nil
		}
	}
	return nil, chain.Address{}, errors.New("Zähler-Wallet nicht ableitbar")
}

// zaehlerEinreichen signiert eine Transaktion mit der Zähler-Wallet und gibt
// sie in den Mempool. Zähler-Transaktionen tragen keine Gebühr.
func (s *Server) zaehlerEinreichen(key *ecdsa.PrivateKey, adr chain.Address, typ chain.TxType, nutzlast []byte) error {
	// R844: gemeinsame Nonce-Vergabe mit den Zahlungen des Handels – sonst
	// konnten zwei Transaktionen derselben Wallet dieselbe Nonce bekommen.
	zaehlerTxMu.Lock()
	defer zaehlerTxMu.Unlock()
	nonce := s.zaehlerNaechsteNonce(adr)
	tx, err := chain.BuildSignedTx(key, typ, nil, nutzlast, nonce)
	if err != nil {
		return err
	}
	if err := s.mempool.Add(tx); err != nil {
		return err
	}
	s.broadcastTx(context.Background(), tx)
	return nil
}

// ── Lokale Endstände ─────────────────────────────────────────────────────────
// Der Beleg einer Periode braucht die SIGNIERTE Ablesung ihres Anfangs – das
// ist der Endstand der vorigen Periode. Den merkt sich der Node lokal.

func (s *Server) zaehlerStandMerken(idHex string, t meter.Token) {
	st := belegStandAus(t)
	_ = s.store.Put(&storage.Record{ID: idHex + "-" + strconv.FormatUint(st.Zeit, 10), Type: storage.RecordZaehlerStand,
		Data: map[string]any{"t": st.Zeit, "ws_bezug": st.WsBezug, "ws_einspeisung": st.WsEinspeisung,
			"sig": st.Sig, "geraet": t.GeraetPub}})
}

func (s *Server) zaehlerStandLaden(idHex string, zeit uint64) (zaehlerBelegStand, string, bool) {
	rec, err := s.store.Get(storage.RecordZaehlerStand, idHex+"-"+strconv.FormatUint(zeit, 10))
	if err != nil || rec == nil || rec.Data == nil {
		return zaehlerBelegStand{}, "", false
	}
	wb, _ := rec.Data["ws_bezug"].(string)
	we, _ := rec.Data["ws_einspeisung"].(string)
	sig, _ := rec.Data["sig"].(string)
	g, _ := rec.Data["geraet"].(string)
	return zaehlerBelegStand{Zeit: zeit, WsBezug: wb, WsEinspeisung: we, Sig: sig}, g, true
}

// ── Zertifizierer ────────────────────────────────────────────────────────────

var (
	zaehlerOffen     = map[string]time.Time{} // Zählerkennung → Zeit der letzten eingereichten Tx
	zaehlerGewarnt   = map[string]bool{}
	zaehlerGestartet bool
	zaehlerStartMu   sync.Mutex
)

func zaehlerPeriode() time.Duration {
	if m, err := strconv.Atoi(strings.TrimSpace(os.Getenv("FUNDUS_ZAEHLER_PERIODE_MIN"))); err == nil && m >= 5 {
		return time.Duration(m) * time.Minute
	}
	return time.Hour
}

func (s *Server) zaehlerSchleife() {
	defer func() {
		if r := recover(); r != nil && s.log != nil {
			s.log.Error("Zähler-Zertifizierer abgebrochen", zap.Any("grund", r))
		}
	}()
	time.Sleep(45 * time.Second)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		s.zaehlerLauf()
	}
}

func (s *Server) zaehlerWarnen(schluessel, text string, felder ...zap.Field) {
	if zaehlerGewarnt[schluessel] || s.log == nil {
		return
	}
	zaehlerGewarnt[schluessel] = true
	s.log.Warn(text, felder...)
}

func (s *Server) zaehlerLauf() {
	s.ablesungAufraeumen() // R828: Minutenwerte älter als 35 Tage, einmal täglich
	s.viertelAufraeumen()   // R829: Viertelstunden älter als 13 Monate, einmal täglich
	s.viertelTagesbuendel() // R829: Vortag je Zähler in den verteilten Speicher
	if s.chain == nil || s.mempool == nil {
		return
	}
	zaehlerMu.Lock()
	kopie := make(map[string]meter.Token, len(zaehlerAktuell))
	for k, v := range zaehlerAktuell {
		kopie[k] = v
	}
	zaehlerMu.Unlock()
	if len(kopie) == 0 {
		return
	}
	key, adr, err := s.zaehlerWallet()
	if err != nil {
		s.zaehlerWarnen("wallet", "Zähler: keine Zähler-Wallet", zap.Error(err))
		return
	}
	periode := zaehlerPeriode()
	for nr, tok := range kopie {
		id := chain.ZaehlerIDAusNummer(nr)
		idHex := hex.EncodeToString(id[:])
		if t, ok := zaehlerOffen[idHex]; ok && time.Since(t) < 3*time.Minute {
			continue // letzte Transaktion noch nicht aufgenommen
		}
		ts := uint64(tok.Timestamp.Unix())
		ws, ok := new(big.Int).SetString(tok.WsEinspeisung, 10)
		if !ok {
			continue
		}
		m, vorhanden := s.chain.GetMeter(id)
		if !vorhanden {
			// R838: JEDEN Zähler registrieren, auch reine Verbraucher (Einspeise-
			// zählwerk null). Gehandelt wird auch der Bezug; die Registrierung
			// bindet die Zählernummer an diese Wallet, sodass niemand sonst sie
			// belegen kann. Zertifiziert wird weiterhin nur echte Einspeisung
			// (siehe unten: keine leeren Zertifikate).
			s.zaehlerStandMerken(idHex, tok)
			if err := s.zaehlerEinreichen(key, adr, chain.TxMeterRegister,
				chain.MeterRegisterNutzlast(id, 0, chain.UnitWs, ws, ts)); err != nil {
				s.zaehlerWarnen("reg:"+idHex, "Zähler: Registrierung gescheitert", zap.String("zaehler", nr), zap.Error(err))
				continue
			}
			zaehlerOffen[idHex] = time.Now()
			if s.log != nil {
				s.log.Info("Zähler: Registrierung eingereicht", zap.String("zaehler", nr),
					zap.String("kennung", idHex), zap.String("erzeuger", adr.Hex()))
			}
			continue
		}
		if m.Producer != adr {
			// Jemand hat diese Zählernummer zuerst registriert – womöglich eine
			// fremde Wallet. Nicht still übergehen.
			s.zaehlerWarnen("fremd:"+idHex, "Zähler: auf der Chain einer ANDEREN Wallet zugeordnet – keine Zertifizierung",
				zap.String("zaehler", nr), zap.String("erzeuger_chain", m.Producer.Hex()), zap.String("eigene", adr.Hex()))
			continue
		}
		if ts <= m.LastTimestamp || time.Since(time.Unix(int64(m.LastTimestamp), 0)) < periode {
			continue
		}
		switch ws.Cmp(m.LastCumulative) {
		case -1:
			s.zaehlerWarnen("rueck:"+idHex, "Zähler: Zählwerk kleiner als letzter Endstand (Zählertausch?) – keine Zertifizierung",
				zap.String("zaehler", nr), zap.String("jetzt", ws.String()), zap.String("letzter", m.LastCumulative.String()))
			continue
		case 0:
			continue // keine neue Einspeisung – keine leeren Zertifikate
		}
		a, geraetA, ok := s.zaehlerStandLaden(idHex, m.LastTimestamp)
		if !ok || geraetA != tok.GeraetPub || a.WsEinspeisung != m.LastCumulative.String() {
			// Anfang nicht (mehr) signiert vorhanden: Stand von der Chain, ohne Signatur.
			a = zaehlerBelegStand{Zeit: m.LastTimestamp, WsEinspeisung: m.LastCumulative.String()}
		}
		beleg := zaehlerBeleg{V: 1, Zaehler: nr, ZaehlerID: idHex, Geraet: tok.GeraetPub, A: a, B: belegStandAus(tok)}
		roh, _ := json.Marshal(beleg)
		anker := sha256.Sum256(roh)
		if err := s.zaehlerEinreichen(key, adr, chain.TxCommodityCertify,
			chain.CommodityCertifyNutzlast(id, m.LastTimestamp, m.LastCumulative, ts, ws, anker)); err != nil {
			s.zaehlerWarnen("cert:"+idHex+strconv.FormatUint(ts, 10), "Zähler: Zertifizierung gescheitert",
				zap.String("zaehler", nr), zap.Error(err))
			continue
		}
		zaehlerOffen[idHex] = time.Now()
		s.zaehlerStandMerken(idHex, tok)
		rec := &storage.Record{ID: "beleg:" + idHex + ":" + strconv.FormatUint(m.LastTimestamp, 10) + "-" + strconv.FormatUint(ts, 10),
			Type: storage.RecordZaehlerBeleg, OwnerID: s.nodeID(), CreatedAt: time.Now(),
			Data: map[string]any{"roh": string(roh), "zaehler": nr, "kennung": idHex}}
		if err := s.store.Put(rec); err == nil && s.node != nil {
			if data, err := marshalRecord(rec); err == nil {
				_ = s.node.Publish(context.Background(), p2p.TopicEnergy, data)
			}
		}
		if s.log != nil {
			kwh := new(big.Float).Quo(new(big.Float).SetInt(new(big.Int).Sub(ws, m.LastCumulative)), big.NewFloat(3.6e6))
			s.log.Info("Zähler: Periode zertifiziert", zap.String("zaehler", nr), zap.String("kwh", kwh.Text('f', 3)))
		}
	}
}

// ── Prüfen ───────────────────────────────────────────────────────────────────

type zaehlerPruefung struct {
	Zaehler     string  `json:"zaehler"`
	Von         uint64  `json:"von"`
	Bis         uint64  `json:"bis"`
	KWh         float64 `json:"kwh"`
	Erzeuger    string  `json:"erzeuger"`
	AufChain    bool    `json:"auf_chain"`    // Zertifikat existiert, Anker passt zum Beleg
	Signaturen  bool    `json:"signaturen"`   // Anfang und Ende vom Gerät unterschrieben
	AnfangOhne  bool    `json:"anfang_ohne"`  // Anfangsstand ohne Gerätesignatur
	MengeStimmt bool    `json:"menge_stimmt"` // Menge auf der Chain = Differenz der Zählwerke
	Kennung     bool    `json:"kennung"`      // Zählerkennung passt zur Zählernummer
	Geraet      string  `json:"geraet"`
}

func pruefeBeleg(bc *chain.Blockchain, roh string) (zaehlerPruefung, bool) {
	var b zaehlerBeleg
	if json.Unmarshal([]byte(roh), &b) != nil || b.Zaehler == "" {
		return zaehlerPruefung{}, false
	}
	p := zaehlerPruefung{Zaehler: b.Zaehler, Von: b.A.Zeit, Bis: b.B.Zeit, Geraet: b.Geraet}
	id := chain.ZaehlerIDAusNummer(b.Zaehler)
	p.Kennung = hex.EncodeToString(id[:]) == b.ZaehlerID
	if tok, ok := bc.GetCommodityToken(chain.CommodityTokenID(id, b.A.Zeit, b.B.Zeit)); ok {
		p.AufChain = tok.RawDataHash == sha256.Sum256([]byte(roh))
		p.Erzeuger = tok.Producer.Hex()
		if tok.Amount != nil {
			kwh, _ := new(big.Float).Quo(new(big.Float).SetInt(tok.Amount), big.NewFloat(3.6e6)).Float64()
			p.KWh = kwh
			wa, okA := new(big.Int).SetString(b.A.WsEinspeisung, 10)
			wb, okB := new(big.Int).SetString(b.B.WsEinspeisung, 10)
			p.MengeStimmt = okA && okB && new(big.Int).Sub(wb, wa).Cmp(tok.Amount) == 0
		}
	}
	sigB := meter.AblesungPruefen(b.Geraet, b.B.Sig, b.Zaehler, int64(b.B.Zeit), b.B.WsBezug, b.B.WsEinspeisung)
	if b.A.Sig == "" {
		p.AnfangOhne = true
		p.Signaturen = sigB
	} else {
		p.Signaturen = sigB && meter.AblesungPruefen(b.Geraet, b.A.Sig, b.Zaehler, int64(b.A.Zeit), b.A.WsBezug, b.A.WsEinspeisung)
	}
	return p, true
}

// GET /api/v1/zertifikate – zertifizierte Perioden (aus Belegen im Netz, gegen
// die Chain geprüft) und die aktuellen Ablesungen dieses Nodes.
func (s *Server) zertifikateListe(c *gin.Context) {
	perioden := []zaehlerPruefung{}
	if s.chain != nil {
		alle, _ := s.store.List(storage.RecordZaehlerBeleg)
		for _, r := range alle {
			if r == nil || r.DeletedAt != nil || r.Data == nil {
				continue
			}
			roh, _ := r.Data["roh"].(string)
			if p, ok := pruefeBeleg(s.chain, roh); ok {
				perioden = append(perioden, p)
			}
		}
	}
	sort.Slice(perioden, func(i, j int) bool { return perioden[i].Bis > perioden[j].Bis })
	if len(perioden) > 200 {
		perioden = perioden[:200]
	}
	ablesungen := []gin.H{}
	zaehlerMu.Lock()
	for nr, t := range zaehlerAktuell {
		ablesungen = append(ablesungen, gin.H{
			"zaehler": nr, "zeit": t.Timestamp.Unix(), "kwh_bezug": t.KWh, "kwh_einspeisung": t.KWhFeed,
			"geraet": t.GeraetPub,
			"signatur_gueltig": meter.AblesungPruefen(t.GeraetPub, t.GeraetSig, nr, t.Timestamp.Unix(), t.WsBezug, t.WsEinspeisung),
		})
	}
	zaehlerMu.Unlock()
	erzeuger := ""
	if len(ablesungen) > 0 { // ohne Zähler keine Zähler-Wallet anlegen
		if _, adr, err := s.zaehlerWallet(); err == nil {
			erzeuger = adr.Hex()
		}
	}
	c.JSON(http.StatusOK, gin.H{"perioden": perioden, "ablesungen": ablesungen,
		"periode_min": int(zaehlerPeriode().Minutes()), "zaehler_wallet": erzeuger})
}

func (s *Server) registerZaehler(r *gin.Engine) {
	zaehlerStartMu.Lock()
	if !zaehlerGestartet {
		zaehlerGestartet = true
		go s.zaehlerSchleife()
		go s.sekundenwerteLoeschen() // R828
	}
	zaehlerStartMu.Unlock()
	r.GET("/api/v1/zertifikate", s.zertifikateListe)
	r.GET("/api/v1/viertelstunden", s.viertelstundenListe) // R829
}
