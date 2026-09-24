package chain

import (
	"errors"
	"fmt"
	"math/big"
)

// ─── Energie-/Rohstoff-Zertifizierung (Spec §7b, Tx 0x22/0x23) ───────────────
//
// Phase 2.3a: Die Chain verbucht NUR die fälschungssichere, lückenlose Menge
// (Differenz zweier kumulativer, monoton steigender Zählerstände) als
// zertifizierte Liefermenge beim Erzeuger. SI-Basiseinheiten als Ganzzahl
// (Strom Ws, Gas/Öl g, Wasser ml) — niemals Float (Konsens-Determinismus).
//
// KEIN Geldfluss, KEIN Tarif in dieser Stufe. Der Handel der Zertifikate
// (Erzeuger-Minimum, Angebot/Nachfrage, Verbraucher zahlt Erzeuger) ist ein
// eigener zweiter Schritt (Phase 2.3b), der auf CertifiedTotal aufsetzt.

// MeterRecord ist die Chain-Registrierung eines Zählers plus dessen zuletzt
// verbuchter Endstand (lückenlose Kette gegen Doppel-Abrechnung).
type MeterRecord struct {
	Producer       Address  // Erzeuger-Account, dem dieser Zähler gehört
	Commodity      uint8    // 0=Strom 1=Gas 2=Wasser 3=Öl …
	Unit           uint8    // Mengeneinheit (UnitWs/UnitMl/UnitG …) — wie Amount zu lesen ist
	LastCumulative *big.Int // zuletzt verbuchter kumulativer Stand (SI-Ganzzahl)
	LastTimestamp  uint64   // Unix-Sekunden des letzten verbuchten Stands
	CertifiedTotal *big.Int // Lebenssumme aller zertifizierten Differenzen (auch settled)
}

// CommodityToken ist ein diskretes Rohstoff-/Energiezertifikat für EINE Abrechnungsperiode
// (Spec §7b-bis). On-Chain steht nur das Commitment: Menge + Zeitraum + ein Hash
// der LOKAL beim Erzeuger gehaltenen Rohdaten. Solange Settled=false ist der Token
// handelbar; nach dem Settlement (Verkauf/Abrechnung) wird er aus dem aktiven
// State GEPRUNED (die certify-/settle-Tx bleiben in der Block-Historie). Der
// Erzeuger darf dann seine lokalen Rohdaten löschen — der RawDataHash bleibt in
// der Historie als Beweisanker, falls die Menge je angezweifelt wird.
type CommodityToken struct {
	Producer    Address  // Erzeuger (= Zähler-Eigentümer zum Zeitpunkt der Zertifizierung)
	Commodity   uint8    // 0=Strom 1=Gas 2=Wasser 3=Öl …
	Unit        uint8    // Mengeneinheit (UnitWs/UnitMl/UnitG …) — wie Amount zu lesen ist
	Amount      *big.Int // gelieferte Menge der Periode (SI-Ganzzahl in der angegebenen Einheit)
	PeriodStart uint64   // Unix-Sekunden (= timestamp_a)
	PeriodEnd   uint64   // Unix-Sekunden (= timestamp_b)
	RawDataHash [32]byte // Hash der lokalen Rohdaten (Audit-Anker)
	Settled     bool     // true → abgerechnet, wird aus dem aktiven State gepruned
}

// encode serialisiert einen CommodityToken kanonisch (für den State-Root).
func (e *CommodityToken) encode() []byte {
	buf := make([]byte, 0, 20+1+1+16+8+8+32+1)
	buf = append(buf, e.Producer[:]...)
	buf = append(buf, e.Commodity)
	buf = append(buf, e.Unit)
	putUint128(&buf, e.Amount)
	putUint64(&buf, e.PeriodStart)
	putUint64(&buf, e.PeriodEnd)
	buf = append(buf, e.RawDataHash[:]...)
	if e.Settled {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	return buf
}

// CommodityTokenID leitet die deterministische Token-ID aus Zähler + Periode ab:
// hash(meter_id || period_start || period_end). Dieselbe Periode ergibt dieselbe
// ID → eine Periode kann nicht zweimal als Token existieren.
func CommodityTokenID(meterID [11]byte, periodStart, periodEnd uint64) [32]byte {
	buf := make([]byte, 0, 11+16)
	buf = append(buf, meterID[:]...)
	putUint64(&buf, periodStart)
	putUint64(&buf, periodEnd)
	return chainHash(buf)
}

// encode serialisiert einen MeterRecord kanonisch (für den State-Root).
func (m *MeterRecord) encode() []byte {
	buf := make([]byte, 0, 20+1+1+16+8+16)
	buf = append(buf, m.Producer[:]...)
	buf = append(buf, m.Commodity)
	buf = append(buf, m.Unit)
	putUint128(&buf, m.LastCumulative)
	putUint64(&buf, m.LastTimestamp)
	putUint128(&buf, m.CertifiedTotal)
	return buf
}

// ── Payloads ─────────────────────────────────────────────────────────────────

// MeterRegisterPayload — Payload für TxMeterRegister.
// Initialstand legt den Nullpunkt fest (ab dem zertifiziert wird).
type MeterRegisterPayload struct {
	MeterID       [11]byte
	Commodity     uint8
	Unit          uint8    // Mengeneinheit des Zählers (UnitWs/UnitMl/UnitG …)
	InitialReading *big.Int // kumulativer Startstand (SI-Ganzzahl)
	InitialTime    uint64   // Unix-Sekunden des Startstands
}

func (p *MeterRegisterPayload) encode() []byte {
	buf := make([]byte, 0, 11+1+1+16+8)
	buf = append(buf, p.MeterID[:]...)
	buf = append(buf, p.Commodity)
	buf = append(buf, p.Unit)
	putUint128(&buf, p.InitialReading)
	putUint64(&buf, p.InitialTime)
	return buf
}

func decodeMeterRegisterPayload(b []byte) (*MeterRegisterPayload, error) {
	if len(b) != 11+1+1+16+8 {
		return nil, errors.New("chain: ungültige MeterRegister-Payload-Länge")
	}
	p := &MeterRegisterPayload{InitialReading: new(big.Int)}
	off := 0
	copy(p.MeterID[:], b[off:off+11])
	off += 11
	p.Commodity = b[off]
	off++
	p.Unit = b[off]
	off++
	p.InitialReading.SetBytes(b[off : off+16])
	off += 16
	p.InitialTime = beUint64(b[off : off+8])
	return p, nil
}

// CommodityCertifyPayload — Payload für TxCommodityCertify. Trägt den Zähler und zwei
// kumulative Stände (Anfang/Ende). reading_a muss exakt dem zuletzt verbuchten
// Endstand entsprechen (lückenlose Kette). reading_b ist der neue Endstand.
type CommodityCertifyPayload struct {
	MeterID     [11]byte
	TimestampA  uint64
	CumulativeA *big.Int
	TimestampB  uint64
	CumulativeB *big.Int
	RawDataHash [32]byte // Hash der lokalen Rohdaten dieser Periode (Audit-Anker)
}

func (p *CommodityCertifyPayload) encode() []byte {
	buf := make([]byte, 0, 11+8+16+8+16+32)
	buf = append(buf, p.MeterID[:]...)
	putUint64(&buf, p.TimestampA)
	putUint128(&buf, p.CumulativeA)
	putUint64(&buf, p.TimestampB)
	putUint128(&buf, p.CumulativeB)
	buf = append(buf, p.RawDataHash[:]...)
	return buf
}

func decodeCommodityCertifyPayload(b []byte) (*CommodityCertifyPayload, error) {
	if len(b) != 11+8+16+8+16+32 {
		return nil, errors.New("chain: ungültige CommodityCertify-Payload-Länge")
	}
	p := &CommodityCertifyPayload{CumulativeA: new(big.Int), CumulativeB: new(big.Int)}
	off := 0
	copy(p.MeterID[:], b[off:off+11])
	off += 11
	p.TimestampA = beUint64(b[off : off+8])
	off += 8
	p.CumulativeA.SetBytes(b[off : off+16])
	off += 16
	p.TimestampB = beUint64(b[off : off+8])
	off += 8
	p.CumulativeB.SetBytes(b[off : off+16])
	off += 16
	copy(p.RawDataHash[:], b[off:off+32])
	return p, nil
}

// ── State-Übergänge ──────────────────────────────────────────────────────────

// validCommodity prüft, ob der Commodity-Code bekannt ist (0–3 + Reserve).
func validCommodity(c uint8) bool {
	return c <= 9 // 0=Strom 1=Gas 2=Wasser 3=Öl, 4–9 reserviert
}

// Mengeneinheiten (SI-Basis als Ganzzahl, niemals Float on-chain). Die Einheit
// definiert, wie die Amount-Ganzzahl eines Tokens/Zählers zu lesen ist. Bewusst
// getrennt vom Commodity: derselbe Rohstoff kann je nach Zähler in
// unterschiedlichen Einheiten erfasst werden.
const (
	UnitWs uint8 = 0 // Wattsekunde (= Joule) — Energie
	UnitMl uint8 = 1 // Milliliter — Volumen (Wasser, flüssiges Öl)
	UnitG  uint8 = 2 // Gramm — Masse (Gas, Pellets)
	UnitWh uint8 = 3 // Wattstunde — alternative Energieeinheit
	UnitL  uint8 = 4 // Liter — grobes Volumen
	// 5–15 reserviert für weitere SI-Ganzzahleinheiten.
)

// validUnit prüft, ob die Einheit bekannt ist.
func validUnit(u uint8) bool {
	return u <= 15
}

// applyMeterRegister: ein Erzeuger registriert einen Zähler. Der Einreicher
// (tx.From) wird Eigentümer. meter_id ist eindeutig (keine Neu-Registrierung).
func (s *State) applyMeterRegister(tx *Transaction) error {
	p, err := decodeMeterRegisterPayload(tx.Payload)
	if err != nil {
		return err
	}
	if !validCommodity(p.Commodity) {
		return fmt.Errorf("chain: unbekannter Commodity-Code %d", p.Commodity)
	}
	if !validUnit(p.Unit) {
		return fmt.Errorf("chain: unbekannte Einheit %d", p.Unit)
	}
	if _, exists := s.meters[p.MeterID]; exists {
		return errors.New("chain: Zähler bereits registriert")
	}
	producer := s.getOrCreate(tx.From)
	if producer.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", producer.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: meter_register trägt keine Gebühr")
	}
	producer.Nonce++
	s.meters[p.MeterID] = &MeterRecord{
		Producer:       tx.From,
		Commodity:      p.Commodity,
		Unit:           p.Unit,
		LastCumulative: new(big.Int).Set(p.InitialReading),
		LastTimestamp:  p.InitialTime,
		CertifiedTotal: new(big.Int),
	}
	return nil
}

// applyCommodityCertify: zertifiziert die zwischen zwei Ständen gelieferte Menge.
// Validierung (Spec §7b): Zähler registriert; nur der Erzeuger reicht ein;
// timestamp_b > timestamp_a; cumulative_b >= cumulative_a (Monotonie);
// cumulative_a == zuletzt verbuchter Endstand (lückenlos, kein Doppel-Abrechnen);
// timestamp_a == zuletzt verbuchter Zeitstempel (Kette auch zeitlich lückenlos).
func (s *State) applyCommodityCertify(tx *Transaction) error {
	p, err := decodeCommodityCertifyPayload(tx.Payload)
	if err != nil {
		return err
	}
	m, ok := s.meters[p.MeterID]
	if !ok {
		return errors.New("chain: Zähler nicht registriert")
	}
	if tx.From != m.Producer {
		return errors.New("chain: nur der Erzeuger des Zählers kann zertifizieren")
	}
	if p.TimestampB <= p.TimestampA {
		return errors.New("chain: timestamp_b muss nach timestamp_a liegen")
	}
	if p.CumulativeB.Cmp(p.CumulativeA) < 0 {
		return errors.New("chain: Zählerstand rückläufig (Monotonie verletzt)")
	}
	// Lückenlose Kette: Anfangsstand/-zeit müssen exakt am letzten Endpunkt anknüpfen.
	if p.CumulativeA.Cmp(m.LastCumulative) != 0 {
		return fmt.Errorf("chain: reading_a (%s) != letzter Endstand (%s)",
			p.CumulativeA, m.LastCumulative)
	}
	if p.TimestampA != m.LastTimestamp {
		return fmt.Errorf("chain: timestamp_a (%d) != letzter Zeitstempel (%d)",
			p.TimestampA, m.LastTimestamp)
	}
	producer := s.getOrCreate(tx.From)
	if producer.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", producer.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: commodity_certify trägt keine Gebühr")
	}
	producer.Nonce++
	// Differenz als zertifizierte Menge verbuchen, Kette fortschreiben.
	delta := new(big.Int).Sub(p.CumulativeB, p.CumulativeA)
	m.CertifiedTotal.Add(m.CertifiedTotal, delta)
	m.LastCumulative = new(big.Int).Set(p.CumulativeB)
	m.LastTimestamp = p.TimestampB

	// Diskreten, handelbaren Rohstoff-/Energie-Token für diese Periode anlegen (unsettled).
	// Deterministische ID aus Zähler+Periode → dieselbe Periode kann nicht zweimal
	// als Token existieren.
	id := CommodityTokenID(p.MeterID, p.TimestampA, p.TimestampB)
	if _, exists := s.tokens[id]; exists {
		return errors.New("chain: Token für diese Periode existiert bereits")
	}
	s.tokens[id] = &CommodityToken{
		Producer:    tx.From,
		Commodity:   m.Commodity,
		Unit:        m.Unit,
		Amount:      delta,
		PeriodStart: p.TimestampA,
		PeriodEnd:   p.TimestampB,
		RawDataHash: p.RawDataHash,
		Settled:     false,
	}
	return nil
}

// CommoditySettlePayload — Payload für TxCommoditySettle: referenziert den Token per ID.
type CommoditySettlePayload struct {
	TokenID [32]byte
}

func (p *CommoditySettlePayload) encode() []byte {
	out := make([]byte, 32)
	copy(out, p.TokenID[:])
	return out
}

func decodeCommoditySettlePayload(b []byte) (*CommoditySettlePayload, error) {
	if len(b) != 32 {
		return nil, errors.New("chain: ungültige CommoditySettle-Payload-Länge")
	}
	p := &CommoditySettlePayload{}
	copy(p.TokenID[:], b)
	return p, nil
}

// applyCommoditySettle markiert einen Token als abgerechnet. Nur der Erzeuger des
// Tokens darf das (in Phase 2.3b übernimmt das der Kaufvorgang). Ein settled
// Token wird beim nächsten State-Root nicht mehr gehalten (Pruning) — der
// Erzeuger darf dann seine lokalen Rohdaten löschen. Gebührenfrei.
func (s *State) applyCommoditySettle(tx *Transaction) error {
	p, err := decodeCommoditySettlePayload(tx.Payload)
	if err != nil {
		return err
	}
	t, ok := s.tokens[p.TokenID]
	if !ok {
		return errors.New("chain: Token unbekannt oder bereits abgerechnet")
	}
	if t.Settled {
		return errors.New("chain: Token bereits abgerechnet")
	}
	if tx.From != t.Producer {
		return errors.New("chain: nur der Erzeuger kann den Token abrechnen")
	}
	actor := s.getOrCreate(tx.From)
	if actor.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", actor.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: commodity_settle trägt keine Gebühr")
	}
	actor.Nonce++
	t.Settled = true // wird beim State-Root/Persist gepruned (siehe tokensRoot)
	return nil
}

// GetToken gibt eine Kopie eines Rohstoff-/Energie-Tokens zurück (Lesen/Tests).
func (s *State) GetToken(id [32]byte) (CommodityToken, bool) {
	t, ok := s.tokens[id]
	if !ok {
		return CommodityToken{}, false
	}
	cp := *t
	cp.Amount = new(big.Int).Set(t.Amount)
	return cp, true
}
func (s *State) GetMeter(id [11]byte) (MeterRecord, bool) {
	m, ok := s.meters[id]
	if !ok {
		return MeterRecord{}, false
	}
	cp := *m
	cp.LastCumulative = new(big.Int).Set(m.LastCumulative)
	cp.CertifiedTotal = new(big.Int).Set(m.CertifiedTotal)
	return cp, true
}
