package chain

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Escrow-Tests bauen die Transaktionen direkt (kein Helfer nötig).

func TestEscrowOpenAndConfirm(t *testing.T) {
	buyerKey, _ := crypto.GenerateKey()
	buyer := PubkeyToAddress(&buyerKey.PublicKey)
	var seller Address
	seller[0] = 0x5E
	var feeColl Address
	feeColl[0] = 0xFE

	st := NewState()
	st.Credit(buyer, new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND)))

	amount := new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))
	fee := FeeForValue(amount)
	open := &Transaction{Type: TxEscrowOpen, Nonce: 0, Fee: fee,
		Payload: (&EscrowOpenPayload{Seller: seller, Amount: amount, Deadline: 100}).encode()}
	if err := SignTransaction(open, buyerKey); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(open, feeColl, Address{}, 1); err != nil {
		t.Fatalf("open: %v", err)
	}
	escrowID := open.Hash()

	// Geld muss gesperrt sein: Käufer hat amount+fee weniger, Verkäufer noch nichts.
	wantBuyer := new(big.Int).Sub(new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND)),
		new(big.Int).Add(amount, fee))
	if st.Balance(buyer).Cmp(wantBuyer) != 0 {
		t.Fatalf("Käufer-Saldo falsch: %s want %s", st.Balance(buyer), wantBuyer)
	}
	if st.Balance(seller).Sign() != 0 {
		t.Fatal("Verkäufer dürfte vor confirm nichts haben")
	}
	e, ok := st.GetEscrow(escrowID)
	if !ok || e.State != EscrowOpen {
		t.Fatal("Escrow nicht offen")
	}

	// Confirm durch den Käufer → Betrag an Verkäufer.
	confirm := &Transaction{Type: TxEscrowConfirm, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: escrowID}).encode()}
	if err := SignTransaction(confirm, buyerKey); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(confirm, feeColl, Address{}, 2); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if st.Balance(seller).Cmp(amount) != 0 {
		t.Fatalf("Verkäufer-Saldo %s != %s", st.Balance(seller), amount)
	}
	e, _ = st.GetEscrow(escrowID)
	if e.State != EscrowClosed {
		t.Fatal("Escrow nach confirm nicht geschlossen")
	}
}

func TestEscrowRefundAfterDeadline(t *testing.T) {
	buyerKey, _ := crypto.GenerateKey()
	buyer := PubkeyToAddress(&buyerKey.PublicKey)
	var seller Address
	seller[0] = 0x5E
	var feeColl Address

	st := NewState()
	start := new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND))
	st.Credit(buyer, start)

	amount := new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))
	open := &Transaction{Type: TxEscrowOpen, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&EscrowOpenPayload{Seller: seller, Amount: amount, Deadline: 50}).encode()}
	_ = SignTransaction(open, buyerKey)
	if err := st.ApplyTransaction(open, feeColl, Address{}, 1); err != nil {
		t.Fatalf("open: %v", err)
	}
	escrowID := open.Hash()

	// Refund vor Deadline → abgelehnt.
	refundEarly := &Transaction{Type: TxEscrowRefund, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: escrowID}).encode()}
	_ = SignTransaction(refundEarly, buyerKey)
	if err := st.ApplyTransaction(refundEarly, feeColl, Address{}, 49); err == nil {
		t.Fatal("Refund vor Deadline hätte abgelehnt werden müssen")
	}

	// Refund ab Deadline → Betrag zurück an Käufer (Nonce noch 1, da früher abgelehnt).
	refund := &Transaction{Type: TxEscrowRefund, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: escrowID}).encode()}
	_ = SignTransaction(refund, buyerKey)
	if err := st.ApplyTransaction(refund, feeColl, Address{}, 50); err != nil {
		t.Fatalf("refund: %v", err)
	}
	// Käufer hat alles bis auf die Gebühr zurück.
	wantBuyer := new(big.Int).Sub(start, FeeForValue(amount))
	if st.Balance(buyer).Cmp(wantBuyer) != 0 {
		t.Fatalf("Käufer nach Refund %s != %s", st.Balance(buyer), wantBuyer)
	}
}

func TestEscrowConfirmOnlyByBuyer(t *testing.T) {
	buyerKey, _ := crypto.GenerateKey()
	strangerKey, _ := crypto.GenerateKey()
	buyer := PubkeyToAddress(&buyerKey.PublicKey)
	var seller Address
	seller[0] = 0x5E
	var feeColl Address

	st := NewState()
	st.Credit(buyer, new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND)))
	amount := new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))
	open := &Transaction{Type: TxEscrowOpen, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&EscrowOpenPayload{Seller: seller, Amount: amount, Deadline: 100}).encode()}
	_ = SignTransaction(open, buyerKey)
	_ = st.ApplyTransaction(open, feeColl, Address{}, 1)
	escrowID := open.Hash()

	// Fremder versucht zu bestätigen → abgelehnt.
	confirm := &Transaction{Type: TxEscrowConfirm, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: escrowID}).encode()}
	_ = SignTransaction(confirm, strangerKey)
	if err := st.ApplyTransaction(confirm, feeColl, Address{}, 2); err == nil {
		t.Fatal("confirm durch Fremden hätte abgelehnt werden müssen")
	}
}

func TestEscrowDisputeJuryRefund(t *testing.T) {
	buyerKey, _ := crypto.GenerateKey()
	sellerKey, _ := crypto.GenerateKey()
	buyer := PubkeyToAddress(&buyerKey.PublicKey)
	seller := PubkeyToAddress(&sellerKey.PublicKey)
	var feeColl Address
	feeColl[0] = 0xFE

	// 3 Juroren.
	var jurors []Address
	var jpriv []*ecdsa.PrivateKey
	for i := 0; i < 3; i++ {
		k, _ := crypto.GenerateKey()
		jpriv = append(jpriv, k)
		jurors = append(jurors, PubkeyToAddress(&k.PublicKey))
	}

	st := NewState()
	start := new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND))
	st.Credit(buyer, start)

	amount := new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))
	premium := FeeForValue(amount)
	open := &Transaction{Type: TxEscrowOpen, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&EscrowOpenPayload{Seller: seller, Amount: amount, Deadline: 100,
			Insured: true, Jurors: jurors}).encode()}
	_ = SignTransaction(open, buyerKey)
	if err := st.ApplyTransaction(open, feeColl, Address{}, 1); err != nil {
		t.Fatalf("open insured: %v", err)
	}
	escrowID := open.Hash()

	// Käufer eröffnet Streit.
	dispute := &Transaction{Type: TxEscrowDispute, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: escrowID}).encode()}
	_ = SignTransaction(dispute, buyerKey)
	if err := st.ApplyTransaction(dispute, feeColl, Address{}, 5); err != nil {
		t.Fatalf("dispute: %v", err)
	}
	e, _ := st.GetEscrow(escrowID)
	if e.State != EscrowDisputed {
		t.Fatal("Escrow nicht im Streit")
	}

	// 2 von 3 Juroren stimmen für Refund (Käufer gewinnt).
	for i := 0; i < 2; i++ {
		vote := &Transaction{Type: TxJurorVote, Nonce: 0, Fee: big.NewInt(0),
			Payload: (&JurorVotePayload{EscrowID: escrowID, Vote: VoteRefund}).encode()}
		_ = SignTransaction(vote, jpriv[i])
		if err := st.ApplyTransaction(vote, feeColl, Address{}, 6); err != nil {
			t.Fatalf("vote %d: %v", i, err)
		}
	}

	// Käufer hat Betrag zurück (minus Tx-Gebühr + Prämie, die an Juroren ging).
	e, _ = st.GetEscrow(escrowID)
	if e.State != EscrowClosed {
		t.Fatal("Escrow nach Mehrheit nicht geschlossen")
	}
	// Käufer-Saldo: start - fee - premium + amount(zurück) = start - fee - premium + amount
	// = start - FeeForValue - premium (da amount zurückkam, aber amount war ja Teil von start)
	wantBuyer := new(big.Int).Set(start)
	wantBuyer.Sub(wantBuyer, FeeForValue(amount)) // Tx-Gebühr
	wantBuyer.Sub(wantBuyer, premium)             // Prämie (an Juroren)
	if st.Balance(buyer).Cmp(wantBuyer) != 0 {
		t.Fatalf("Käufer nach Jury-Refund %s != %s", st.Balance(buyer), wantBuyer)
	}
	// Die 2 Mehrheits-Juroren teilen sich die Prämie.
	half := new(big.Int).Quo(premium, big.NewInt(2))
	if st.Balance(jurors[0]).Cmp(half) != 0 {
		t.Fatalf("Juror 0 Anteil %s != %s", st.Balance(jurors[0]), half)
	}
}

func TestNonJurorCannotVote(t *testing.T) {
	buyerKey, _ := crypto.GenerateKey()
	strangerKey, _ := crypto.GenerateKey()
	buyer := PubkeyToAddress(&buyerKey.PublicKey)
	var seller Address
	seller[0] = 0x5E
	var feeColl Address

	var jurors []Address
	var jpriv []*ecdsa.PrivateKey
	for i := 0; i < 3; i++ {
		k, _ := crypto.GenerateKey()
		jpriv = append(jpriv, k)
		jurors = append(jurors, PubkeyToAddress(&k.PublicKey))
	}

	st := NewState()
	st.Credit(buyer, new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND)))
	amount := new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))
	open := &Transaction{Type: TxEscrowOpen, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&EscrowOpenPayload{Seller: seller, Amount: amount, Deadline: 100,
			Insured: true, Jurors: jurors}).encode()}
	_ = SignTransaction(open, buyerKey)
	_ = st.ApplyTransaction(open, feeColl, Address{}, 1)
	escrowID := open.Hash()

	dispute := &Transaction{Type: TxEscrowDispute, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: escrowID}).encode()}
	_ = SignTransaction(dispute, buyerKey)
	_ = st.ApplyTransaction(dispute, feeColl, Address{}, 5)

	// Fremder versucht zu stimmen → abgelehnt.
	vote := &Transaction{Type: TxJurorVote, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&JurorVotePayload{EscrowID: escrowID, Vote: VoteRefund}).encode()}
	_ = SignTransaction(vote, strangerKey)
	if err := st.ApplyTransaction(vote, feeColl, Address{}, 6); err == nil {
		t.Fatal("Nicht-Juror konnte stimmen")
	}
}

func TestEscrowStateRootChanges(t *testing.T) {
	// Ein offener Escrow muss den State-Root verändern (er ist committet).
	buyerKey, _ := crypto.GenerateKey()
	buyer := PubkeyToAddress(&buyerKey.PublicKey)
	var seller Address
	seller[0] = 0x5E
	var feeColl Address

	st := NewState()
	st.Credit(buyer, new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND)))
	rootBefore := st.Root()

	amount := new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))
	open := &Transaction{Type: TxEscrowOpen, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&EscrowOpenPayload{Seller: seller, Amount: amount, Deadline: 100}).encode()}
	_ = SignTransaction(open, buyerKey)
	_ = st.ApplyTransaction(open, feeColl, Address{}, 1)

	if st.Root() == rootBefore {
		t.Fatal("offener Escrow muss den State-Root ändern (nicht committet?)")
	}
}
