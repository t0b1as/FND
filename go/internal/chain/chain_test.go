package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// fnd wandelt ganze FND in uFND (big.Int) um.
func fnd(v int64) *big.Int { return new(big.Int).Mul(big.NewInt(v), big.NewInt(UFNDPerFND)) }

func TestFeeForValue(t *testing.T) {
	cases := []struct {
		amount, want int64
	}{
		{1000, 18},          // 1,8 %
		{100_000_000, 1_800_000},
		{0, 0},
		{55, 0}, // 55*18/1000 = 0,99 → abgerundet 0
	}
	for _, c := range cases {
		got := FeeForValue(big.NewInt(c.amount))
		if got.Int64() != c.want {
			t.Fatalf("FeeForValue(%d)=%d, want %d", c.amount, got.Int64(), c.want)
		}
	}
}

func TestTransferEndToEnd(t *testing.T) {
	priv, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := PubkeyToAddress(&priv.PublicKey)
	var to Address
	to[0] = 0xAA

	st := NewState()
	st.Credit(from, fnd(1)) // 1 FND

	amount := big.NewInt(100_000_000) // 0,1 FND
	fee := FeeForValue(amount)         // 1.800.000
	tx := &Transaction{Type: TxTransfer, Nonce: 0, Fee: fee,
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	if err := SignTransaction(tx, priv); err != nil {
		t.Fatal(err)
	}

	var feeColl Address
	feeColl[0] = 0xFE
	if err := st.ApplyTransaction(tx, feeColl, Address{}, 0); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if got := st.Balance(to); got.Cmp(amount) != 0 {
		t.Fatalf("to=%s want %s", got, amount)
	}
	wantSender := new(big.Int).Sub(fnd(1), new(big.Int).Add(amount, fee))
	if got := st.Balance(from); got.Cmp(wantSender) != 0 {
		t.Fatalf("from=%s want %s", got, wantSender)
	}
	if got := st.Balance(feeColl); got.Cmp(fee) != 0 {
		t.Fatalf("feeColl=%s want %s", got, fee)
	}
	if st.GetAccount(from).Nonce != 1 {
		t.Fatalf("nonce nicht erhöht")
	}
}

func TestTransferRejectsBadNonce(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	st := NewState()
	st.Credit(from, fnd(10))

	var to Address
	to[0] = 0x01
	amount := big.NewInt(1_000_000)
	tx := &Transaction{Type: TxTransfer, Nonce: 5, Fee: FeeForValue(amount),
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	if err := SignTransaction(tx, priv); err != nil {
		t.Fatal(err)
	}
	var fc Address
	if err := st.ApplyTransaction(tx, fc, Address{}, 0); err == nil {
		t.Fatal("falsche Nonce hätte abgelehnt werden müssen")
	}
}

func TestTransferRejectsInsufficientBalance(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	st := NewState()
	st.Credit(from, big.NewInt(1000)) // viel zu wenig

	var to Address
	to[0] = 0x02
	amount := fnd(1)
	tx := &Transaction{Type: TxTransfer, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	if err := SignTransaction(tx, priv); err != nil {
		t.Fatal(err)
	}
	var fc Address
	if err := st.ApplyTransaction(tx, fc, Address{}, 0); err == nil {
		t.Fatal("zu geringes Guthaben hätte abgelehnt werden müssen")
	}
}

func TestTransferRejectsWrongFee(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	st := NewState()
	st.Credit(from, fnd(10))

	var to Address
	to[0] = 0x03
	amount := big.NewInt(1_000_000)
	tx := &Transaction{Type: TxTransfer, Nonce: 0, Fee: big.NewInt(1), // falsch
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	if err := SignTransaction(tx, priv); err != nil {
		t.Fatal(err)
	}
	var fc Address
	if err := st.ApplyTransaction(tx, fc, Address{}, 0); err == nil {
		t.Fatal("falsche Gebühr hätte abgelehnt werden müssen")
	}
}

func TestSignatureMismatchRejected(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	other, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	st := NewState()
	st.Credit(from, fnd(10))

	var to Address
	to[0] = 0x04
	amount := big.NewInt(1_000_000)
	tx := &Transaction{Type: TxTransfer, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	// Mit dem RICHTIGEN Schlüssel signieren, dann From fälschen.
	if err := SignTransaction(tx, other); err != nil {
		t.Fatal(err)
	}
	tx.From = from // passt nicht zur Signatur
	var fc Address
	if err := st.ApplyTransaction(tx, fc, Address{}, 0); err == nil {
		t.Fatal("Signatur-Mismatch hätte abgelehnt werden müssen")
	}
}

func TestStateRootDeterministic(t *testing.T) {
	// Zwei States mit denselben Accounts in unterschiedlicher Einfügereihenfolge
	// müssen denselben Root liefern.
	var a1, a2, a3 Address
	a1[0] = 0x10
	a2[0] = 0x20
	a3[0] = 0x30

	s1 := NewState()
	s1.Credit(a1, big.NewInt(100))
	s1.Credit(a2, big.NewInt(200))
	s1.Credit(a3, big.NewInt(300))

	s2 := NewState()
	s2.Credit(a3, big.NewInt(300))
	s2.Credit(a1, big.NewInt(100))
	s2.Credit(a2, big.NewInt(200))

	if s1.Root() != s2.Root() {
		t.Fatal("State-Root hängt von der Einfügereihenfolge ab – nicht deterministisch")
	}
	// Leerer State hat definierten Root.
	empty := NewState()
	if empty.Root() == (s1.Root()) {
		t.Fatal("leerer Root sollte sich vom befüllten unterscheiden")
	}
}

func TestEmptyAccountsPruned(t *testing.T) {
	var a Address
	a[0] = 0x55
	s1 := NewState()
	s1.Credit(a, big.NewInt(0)) // 0-Saldo, Nonce 0 → soll ignoriert werden
	s2 := NewState()             // völlig leer
	if s1.Root() != s2.Root() {
		t.Fatal("leerer Account verändert den Root – Pruning fehlt")
	}
}

func TestBlockBuildAndReplay(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	var feeColl Address
	feeColl[0] = 0xFE

	// Genesis mit Guthaben für den Absender.
	gen, st := NewGenesis(feeColl, []GenesisAccount{{Address: from, Balance: fnd(100)}}, 1000)

	var to Address
	to[0] = 0x77
	amount := fnd(5)
	tx := &Transaction{Type: TxTransfer, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	if err := SignTransaction(tx, priv); err != nil {
		t.Fatal(err)
	}

	blk, err := BuildBlock(&gen.Header, from, 1002, []*Transaction{tx}, st, feeColl)
	if err != nil {
		t.Fatalf("BuildBlock: %v", err)
	}
	if blk.Header.Height != 1 {
		t.Fatalf("Höhe %d != 1", blk.Header.Height)
	}

	// Replay auf frischem Genesis-State muss exakt denselben State-Root liefern.
	_, st2 := NewGenesis(feeColl, []GenesisAccount{{Address: from, Balance: fnd(100)}}, 1000)
	if err := ApplyBlock(&gen.Header, blk, st2, feeColl); err != nil {
		t.Fatalf("ApplyBlock: %v", err)
	}
	if st.Root() != st2.Root() {
		t.Fatal("State-Root nach Build != nach Replay")
	}
	if blk.Header.StateRoot != st2.Root() {
		t.Fatal("Header-StateRoot != tatsächlicher Root")
	}
}

func TestApplyBlockRejectsTamperedRoot(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	var feeColl Address
	gen, st := NewGenesis(feeColl, []GenesisAccount{{Address: from, Balance: fnd(100)}}, 1000)
	var to Address
	to[0] = 0x88
	amount := fnd(1)
	tx := &Transaction{Type: TxTransfer, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	_ = SignTransaction(tx, priv)
	blk, err := BuildBlock(&gen.Header, from, 1002, []*Transaction{tx}, st, feeColl)
	if err != nil {
		t.Fatal(err)
	}
	blk.Header.StateRoot[0] ^= 0xFF // manipulieren
	_, st2 := NewGenesis(feeColl, []GenesisAccount{{Address: from, Balance: fnd(100)}}, 1000)
	if err := ApplyBlock(&gen.Header, blk, st2, feeColl); err == nil {
		t.Fatal("manipulierter State-Root hätte abgelehnt werden müssen")
	}
}

func TestCanonicalTxSerializationStable(t *testing.T) {
	var to Address
	to[0] = 0x12
	p := (&TransferPayload{To: to, Amount: big.NewInt(123456789)}).encode()
	tx := &Transaction{Type: TxTransfer, Nonce: 7, Fee: big.NewInt(42), Payload: p}
	a := tx.signingBytes()
	b := tx.signingBytes()
	if len(a) != len(b) {
		t.Fatal("signingBytes nicht stabil (Länge)")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("signingBytes nicht stabil (Inhalt)")
		}
	}
	// Roundtrip der Payload.
	dec, err := decodeTransferPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	if dec.To != to || dec.Amount.Int64() != 123456789 {
		t.Fatal("Payload-Roundtrip fehlerhaft")
	}
}
