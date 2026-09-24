package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// TestFND_025_SlashDoubleSign prüft, dass echtes Double-Signing erkannt und der
// Stake entzogen wird — und dass gefälschte oder identische Beweise abprallen.
func TestFND_025_SlashDoubleSign(t *testing.T) {
	// Übeltäter-Schlüssel.
	priv, _ := crypto.GenerateKey()
	offender := PubkeyToAddress(&priv.PublicKey)

	// Zwei VERSCHIEDENE Header für dieselbe Höhe, beide vom Übeltäter signiert.
	mkHeader := func(mark byte) ([]byte, []byte) {
		h := &BlockHeader{Height: 5}
		h.TxRoot[0] = mark
		blk := &Block{Header: *h}
		if err := SignBlock(blk, priv); err != nil {
			t.Fatalf("SignBlock: %v", err)
		}
		return h.bytes(), blk.Commit[0]
	}
	hdrA, sigA := mkHeader(0x01)
	hdrB, sigB := mkHeader(0x02)

	ev := &SlashEvidence{Height: 5, HeaderA: hdrA, SigA: sigA, HeaderB: hdrB, SigB: sigB}

	// 1) Echtes Double-Signing → Übeltäter wird korrekt erkannt.
	who, err := VerifySlashEvidence(ev)
	if err != nil {
		t.Fatalf("gültiges Double-Sign sollte akzeptiert werden: %v", err)
	}
	if who != offender {
		t.Fatalf("falscher Übeltäter erkannt: %x vs %x", who, offender)
	}

	// 2) Identische Header → kein Double-Signing, muss abgelehnt werden.
	evSame := &SlashEvidence{Height: 5, HeaderA: hdrA, SigA: sigA, HeaderB: hdrA, SigB: sigA}
	if _, err := VerifySlashEvidence(evSame); err == nil {
		t.Fatal("identische Header dürfen nicht als Double-Sign gelten")
	}

	// 3) Zwei Header von VERSCHIEDENEN Signierern → kein Double-Sign.
	priv2, _ := crypto.GenerateKey()
	h2 := &BlockHeader{Height: 5}
	h2.TxRoot[0] = 0x03
	blk2 := &Block{Header: *h2}
	_ = SignBlock(blk2, priv2)
	evCross := &SlashEvidence{Height: 5, HeaderA: hdrA, SigA: sigA, HeaderB: h2.bytes(), SigB: blk2.Commit[0]}
	if _, err := VerifySlashEvidence(evCross); err == nil {
		t.Fatal("verschiedene Signierer dürfen nicht als Double-Sign gelten")
	}
}

// TestFND_025_SlashAppliesAndNoDouble prüft die State-Transition: Stake wird
// entzogen, Melder belohnt, und dieselbe Evidence kann nicht doppelt slashen.
func TestFND_025_SlashAppliesAndNoDouble(t *testing.T) {
	st := NewState()

	priv, _ := crypto.GenerateKey()
	offender := PubkeyToAddress(&priv.PublicKey)

	// Übeltäter hat 100 FND gestakt.
	stake := new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND))
	st.getOrCreateStake(offender).Amount = new(big.Int).Set(stake)

	// Melder-Account.
	reporter := Address{0x99}
	st.getOrCreate(reporter).Balance = new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))

	// Double-Sign-Beweis bauen.
	mkHeader := func(mark byte) ([]byte, []byte) {
		h := &BlockHeader{Height: 7}
		h.TxRoot[0] = mark
		blk := &Block{Header: *h}
		_ = SignBlock(blk, priv)
		return h.bytes(), blk.Commit[0]
	}
	hdrA, sigA := mkHeader(0x01)
	hdrB, sigB := mkHeader(0x02)
	payload := encodeSlashPayload(&SlashPayload{Evidence: SlashEvidence{
		Height: 7, HeaderA: hdrA, SigA: sigA, HeaderB: hdrB, SigB: sigB,
	}})

	tx := &Transaction{Type: TxSlash, From: reporter, Nonce: 0, Fee: big.NewInt(0), Payload: payload}
	feeC := Address{0xfe}

	// Anwenden.
	if err := st.applySlash(tx, feeC, Address{}, 10); err != nil {
		t.Fatalf("applySlash sollte erfolgreich sein: %v", err)
	}

	// Stake des Übeltäters muss auf 0 sein (100% Slash).
	if st.Stake(offender).Sign() != 0 {
		t.Fatalf("Stake muss nach Slash 0 sein, ist %s", st.Stake(offender).String())
	}

	// Melder hat die Belohnung (5% von 100 FND = 5 FND) erhalten.
	reward := new(big.Int).Div(stake, big.NewInt(20))
	got := st.GetAccount(reporter).Balance
	want := new(big.Int).Add(new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND)), reward)
	if got.Cmp(want) != 0 {
		t.Fatalf("Melder-Belohnung falsch: bekommen %s, erwartet %s", got, want)
	}

	// Zweiter Versuch mit DERSELBEN Evidence → muss abgelehnt werden.
	tx2 := &Transaction{Type: TxSlash, From: reporter, Nonce: 1, Fee: big.NewInt(0), Payload: payload}
	if err := st.applySlash(tx2, feeC, Address{}, 11); err == nil {
		t.Fatal("dieselbe Evidence darf nicht doppelt slashen")
	}
}
