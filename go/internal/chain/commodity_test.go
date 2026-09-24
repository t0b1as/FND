package chain

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// ── Test-Helfer ──────────────────────────────────────────────────────────────

// registerTestMeter registriert einen Zähler für den Erzeuger und gibt
// State, Erzeuger-Key, Adresse und meter_id zurück.
func registerTestMeter(t *testing.T, commodity uint8, initial uint64, initTime uint64) (
	st *State, pk *ecdsa.PrivateKey, producer, feeColl Address, mid [11]byte,
) {
	t.Helper()
	pk, _ = crypto.GenerateKey()
	producer = PubkeyToAddress(&pk.PublicKey)
	feeColl[0] = 0xFE
	mid = [11]byte{0x4D, 0x45, 0x54, 0x45, 0x52, 0x30, 0x30, 0x31} // "METER001"

	st = NewState()
	reg := &Transaction{Type: TxMeterRegister, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&MeterRegisterPayload{
			MeterID: mid, Commodity: commodity,
			InitialReading: new(big.Int).SetUint64(initial), InitialTime: initTime,
		}).encode()}
	if err := SignTransaction(reg, pk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(reg, feeColl, Address{}, 1); err != nil {
		t.Fatalf("register: %v", err)
	}
	return
}

// certify baut + wendet eine commodity_certify-Tx an.
func certify(t *testing.T, st *State, pk *ecdsa.PrivateKey, feeColl Address, mid [11]byte,
	nonce, tA, tB, cumA, cumB uint64, height uint64) error {
	t.Helper()
	var rawHash [32]byte
	rawHash[0] = 0x77 // beliebiger Rohdaten-Hash für Tests
	tx := &Transaction{Type: TxCommodityCertify, Nonce: nonce, Fee: big.NewInt(0),
		Payload: (&CommodityCertifyPayload{
			MeterID: mid, TimestampA: tA, CumulativeA: new(big.Int).SetUint64(cumA),
			TimestampB: tB, CumulativeB: new(big.Int).SetUint64(cumB),
			RawDataHash: rawHash,
		}).encode()}
	if err := SignTransaction(tx, pk); err != nil {
		t.Fatal(err)
	}
	return st.ApplyTransaction(tx, feeColl, Address{}, height)
}

// ── Tests ────────────────────────────────────────────────────────────────────

// Happy-Path: registrieren → zwei lückenlose Zertifizierungen → Summe stimmt.
func TestCommodityCertifyHappyPath(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)

	// Erstes Intervall: 1000 → 4600 Ws (3600 geliefert).
	if err := certify(t, st, pk, feeColl, mid, 1, 100, 200, 1000, 4600, 2); err != nil {
		t.Fatalf("certify 1: %v", err)
	}
	m, _ := st.GetMeter(mid)
	if m.CertifiedTotal.Cmp(big.NewInt(3600)) != 0 {
		t.Fatalf("CertifiedTotal nach 1 = %s, want 3600", m.CertifiedTotal)
	}
	if m.LastCumulative.Cmp(big.NewInt(4600)) != 0 || m.LastTimestamp != 200 {
		t.Fatalf("Kette nicht fortgeschrieben: cum=%s ts=%d", m.LastCumulative, m.LastTimestamp)
	}

	// Zweites Intervall knüpft lückenlos an: 4600 → 5000 (400 geliefert).
	if err := certify(t, st, pk, feeColl, mid, 2, 200, 300, 4600, 5000, 3); err != nil {
		t.Fatalf("certify 2: %v", err)
	}
	m, _ = st.GetMeter(mid)
	if m.CertifiedTotal.Cmp(big.NewInt(4000)) != 0 {
		t.Fatalf("CertifiedTotal nach 2 = %s, want 4000", m.CertifiedTotal)
	}
}

// Rückläufiger Zählerstand (Monotonie verletzt) wird abgelehnt.
func TestCommodityCertifyRejectsDecreasing(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	if err := certify(t, st, pk, feeColl, mid, 1, 100, 200, 1000, 900, 2); err == nil {
		t.Fatal("rückläufiger Stand hätte abgelehnt werden müssen")
	}
}

// Lücke in der Kette (reading_a != letzter Endstand) wird abgelehnt.
func TestCommodityCertifyRejectsGap(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	// reading_a = 2000, aber letzter Stand ist 1000 → Lücke.
	if err := certify(t, st, pk, feeColl, mid, 1, 100, 200, 2000, 3000, 2); err == nil {
		t.Fatal("Ketten-Lücke hätte abgelehnt werden müssen")
	}
}

// Zeitstempel nicht vorwärts (timestamp_b <= timestamp_a) wird abgelehnt.
func TestCommodityCertifyRejectsBadTime(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	if err := certify(t, st, pk, feeColl, mid, 1, 100, 100, 1000, 2000, 2); err == nil {
		t.Fatal("gleicher Zeitstempel hätte abgelehnt werden müssen")
	}
}

// Falscher timestamp_a (zeitliche Lücke) wird abgelehnt.
func TestCommodityCertifyRejectsTimeGap(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	// cumA stimmt (1000), aber timestamp_a = 150 statt 100.
	if err := certify(t, st, pk, feeColl, mid, 1, 150, 250, 1000, 2000, 2); err == nil {
		t.Fatal("zeitliche Lücke hätte abgelehnt werden müssen")
	}
}

// Nur der Erzeuger des Zählers darf zertifizieren.
func TestCommodityCertifyOnlyByProducer(t *testing.T) {
	st, _, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	otherKey, _ := crypto.GenerateKey()
	tx := &Transaction{Type: TxCommodityCertify, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&CommodityCertifyPayload{
			MeterID: mid, TimestampA: 100, CumulativeA: big.NewInt(1000),
			TimestampB: 200, CumulativeB: big.NewInt(2000),
		}).encode()}
	if err := SignTransaction(tx, otherKey); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(tx, feeColl, Address{}, 2); err == nil {
		t.Fatal("Fremd-Zertifizierung hätte abgelehnt werden müssen")
	}
}

// Zertifizierung auf einen nicht registrierten Zähler wird abgelehnt.
func TestCommodityCertifyRejectsUnregistered(t *testing.T) {
	pk, _ := crypto.GenerateKey()
	var feeColl Address
	feeColl[0] = 0xFE
	st := NewState()
	st.Credit(PubkeyToAddress(&pk.PublicKey), big.NewInt(UFNDPerFND))
	var mid [11]byte
	mid[0] = 0x99
	if err := certify(t, st, pk, feeColl, mid, 0, 100, 200, 0, 100, 1); err == nil {
		t.Fatal("unregistrierter Zähler hätte abgelehnt werden müssen")
	}
}

// Ein Zähler kann nicht doppelt registriert werden.
func TestMeterRegisterRejectsDuplicate(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	reg := &Transaction{Type: TxMeterRegister, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&MeterRegisterPayload{
			MeterID: mid, Commodity: 0,
			InitialReading: big.NewInt(0), InitialTime: 50,
		}).encode()}
	if err := SignTransaction(reg, pk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(reg, feeColl, Address{}, 2); err == nil {
		t.Fatal("Doppel-Registrierung hätte abgelehnt werden müssen")
	}
}

// Unbekannter Commodity-Code wird abgelehnt.
func TestMeterRegisterRejectsBadCommodity(t *testing.T) {
	pk, _ := crypto.GenerateKey()
	var feeColl Address
	feeColl[0] = 0xFE
	st := NewState()
	var mid [11]byte
	mid[0] = 0x01
	reg := &Transaction{Type: TxMeterRegister, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&MeterRegisterPayload{
			MeterID: mid, Commodity: 250,
			InitialReading: big.NewInt(0), InitialTime: 0,
		}).encode()}
	if err := SignTransaction(reg, pk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(reg, feeColl, Address{}, 1); err == nil {
		t.Fatal("unbekannter Commodity-Code hätte abgelehnt werden müssen")
	}
}

// Die Zertifizierung verändert den State-Root (Zähler wird committet).
func TestCommodityCertifyChangesStateRoot(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	rootBefore := st.Root()
	if err := certify(t, st, pk, feeColl, mid, 1, 100, 200, 1000, 4600, 2); err != nil {
		t.Fatalf("certify: %v", err)
	}
	if st.Root() == rootBefore {
		t.Fatal("Zertifizierung muss den State-Root ändern")
	}
}

// ── Energie-Token-Lebenszyklus (certify → unsettled → settle → prune) ────────

// TestCommodityCertifyCreatesToken: eine Zertifizierung erzeugt einen unsettled
// Token mit korrekter Menge, Periode und Rohdaten-Hash.
func TestCommodityCertifyCreatesToken(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	if err := certify(t, st, pk, feeColl, mid, 1, 100, 200, 1000, 4600, 2); err != nil {
		t.Fatalf("certify: %v", err)
	}
	id := CommodityTokenID(mid, 100, 200)
	tok, ok := st.GetToken(id)
	if !ok {
		t.Fatal("Token wurde nicht angelegt")
	}
	if tok.Amount.Cmp(big.NewInt(3600)) != 0 {
		t.Fatalf("Token-Menge %s != 3600", tok.Amount)
	}
	if tok.Settled {
		t.Fatal("neuer Token darf nicht settled sein")
	}
	if tok.PeriodStart != 100 || tok.PeriodEnd != 200 {
		t.Fatalf("Periode falsch: %d–%d", tok.PeriodStart, tok.PeriodEnd)
	}
	if tok.RawDataHash[0] != 0x77 {
		t.Fatal("Rohdaten-Hash nicht gespeichert")
	}
}

// TestCommoditySettleMarksAndPrunes: settle markiert den Token als abgerechnet,
// und er fällt aus dem State-Root (Pruning).
func TestCommoditySettleMarksAndPrunes(t *testing.T) {
	st, pk, producer, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	certify(t, st, pk, feeColl, mid, 1, 100, 200, 1000, 4600, 2)
	id := CommodityTokenID(mid, 100, 200)

	rootWithToken := st.Root()

	settle := &Transaction{Type: TxCommoditySettle, Nonce: 2, Fee: big.NewInt(0),
		Payload: (&CommoditySettlePayload{TokenID: id}).encode()}
	if err := SignTransaction(settle, pk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(settle, feeColl, Address{}, 3); err != nil {
		t.Fatalf("settle: %v", err)
	}
	tok, _ := st.GetToken(id)
	if !tok.Settled {
		t.Fatal("Token sollte settled sein")
	}
	// Pruning: der State-Root muss sich gegenüber 'mit Token' geändert haben
	// (settled Token fällt aus dem Root).
	if st.Root() == rootWithToken {
		t.Fatal("settled Token müsste aus dem State-Root gepruned sein")
	}
	_ = producer
}

// TestCommoditySettleOnlyByProducer: ein Fremder kann den Token nicht abrechnen.
func TestCommoditySettleOnlyByProducer(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	certify(t, st, pk, feeColl, mid, 1, 100, 200, 1000, 4600, 2)
	id := CommodityTokenID(mid, 100, 200)

	other, _ := crypto.GenerateKey()
	st.Credit(PubkeyToAddress(&other.PublicKey), big.NewInt(UFNDPerFND))
	settle := &Transaction{Type: TxCommoditySettle, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&CommoditySettlePayload{TokenID: id}).encode()}
	if err := SignTransaction(settle, other); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(settle, feeColl, Address{}, 3); err == nil {
		t.Fatal("Fremd-Settlement hätte abgelehnt werden müssen")
	}
}

// TestCommoditySettleRejectsDouble: ein bereits abgerechneter Token kann nicht
// erneut abgerechnet werden.
func TestCommoditySettleRejectsDouble(t *testing.T) {
	st, pk, _, feeColl, mid := registerTestMeter(t, 0, 1000, 100)
	certify(t, st, pk, feeColl, mid, 1, 100, 200, 1000, 4600, 2)
	id := CommodityTokenID(mid, 100, 200)

	settle1 := &Transaction{Type: TxCommoditySettle, Nonce: 2, Fee: big.NewInt(0),
		Payload: (&CommoditySettlePayload{TokenID: id}).encode()}
	SignTransaction(settle1, pk)
	if err := st.ApplyTransaction(settle1, feeColl, Address{}, 3); err != nil {
		t.Fatalf("settle1: %v", err)
	}
	settle2 := &Transaction{Type: TxCommoditySettle, Nonce: 3, Fee: big.NewInt(0),
		Payload: (&CommoditySettlePayload{TokenID: id}).encode()}
	SignTransaction(settle2, pk)
	if err := st.ApplyTransaction(settle2, feeColl, Address{}, 4); err == nil {
		t.Fatal("Doppel-Settlement hätte abgelehnt werden müssen")
	}
}

// ── Verallgemeinerung: Commodity + explizite Einheit ─────────────────────────

// registerMeterWithUnit registriert einen Zähler mit explizitem Commodity + Unit.
func registerMeterWithUnit(t *testing.T, commodity, unit uint8, initial, initTime uint64) (
	st *State, pk *ecdsa.PrivateKey, producer, feeColl Address, mid [11]byte,
) {
	t.Helper()
	pk, _ = crypto.GenerateKey()
	producer = PubkeyToAddress(&pk.PublicKey)
	feeColl[0] = 0xFE
	mid = [11]byte{0x57, 0x41, 0x54, 0x45, 0x52, 0x30, 0x31} // "WATER01"
	st = NewState()
	reg := &Transaction{Type: TxMeterRegister, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&MeterRegisterPayload{
			MeterID: mid, Commodity: commodity, Unit: unit,
			InitialReading: new(big.Int).SetUint64(initial), InitialTime: initTime,
		}).encode()}
	if err := SignTransaction(reg, pk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(reg, feeColl, Address{}, 1); err != nil {
		t.Fatalf("register: %v", err)
	}
	return
}

// TestMeterUnitInheritedByToken: Wasser-Zähler (Commodity=2, Unit=ml) → der Token
// erbt Commodity UND Einheit korrekt.
func TestMeterUnitInheritedByToken(t *testing.T) {
	// Wasser, gemessen in ml. Start 1000 ml.
	st, pk, _, feeColl, mid := registerMeterWithUnit(t, 2, UnitMl, 1000, 100)
	m, _ := st.GetMeter(mid)
	if m.Unit != UnitMl || m.Commodity != 2 {
		t.Fatalf("Meter Commodity/Unit falsch: %d/%d", m.Commodity, m.Unit)
	}
	// Zertifizieren: 1000 → 6000 ml (5000 ml geliefert).
	var rawHash [32]byte
	rawHash[0] = 0x77
	tx := &Transaction{Type: TxCommodityCertify, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&CommodityCertifyPayload{
			MeterID: mid, TimestampA: 100, CumulativeA: big.NewInt(1000),
			TimestampB: 200, CumulativeB: big.NewInt(6000), RawDataHash: rawHash,
		}).encode()}
	if err := SignTransaction(tx, pk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(tx, feeColl, Address{}, 2); err != nil {
		t.Fatalf("certify: %v", err)
	}
	tok, ok := st.GetToken(CommodityTokenID(mid, 100, 200))
	if !ok {
		t.Fatal("Token fehlt")
	}
	if tok.Unit != UnitMl {
		t.Fatalf("Token-Einheit %d != UnitMl", tok.Unit)
	}
	if tok.Commodity != 2 {
		t.Fatalf("Token-Commodity %d != 2 (Wasser)", tok.Commodity)
	}
	if tok.Amount.Cmp(big.NewInt(5000)) != 0 {
		t.Fatalf("Menge %s != 5000 ml", tok.Amount)
	}
}

// TestMeterRegisterRejectsBadUnit: unbekannte Einheit wird abgelehnt.
func TestMeterRegisterRejectsBadUnit(t *testing.T) {
	pk, _ := crypto.GenerateKey()
	var feeColl Address
	feeColl[0] = 0xFE
	st := NewState()
	var mid [11]byte
	mid[0] = 0x07
	reg := &Transaction{Type: TxMeterRegister, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&MeterRegisterPayload{
			MeterID: mid, Commodity: 0, Unit: 200, // ungültige Einheit
			InitialReading: big.NewInt(0), InitialTime: 0,
		}).encode()}
	if err := SignTransaction(reg, pk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(reg, feeColl, Address{}, 1); err == nil {
		t.Fatal("unbekannte Einheit hätte abgelehnt werden müssen")
	}
}
