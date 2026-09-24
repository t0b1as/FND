package chain

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// ── Test-Helfer für den Storno-/Rücksende-Flow ───────────────────────────────

// openTestEscrow öffnet einen Standard-Escrow (10 FND, Deadline weit in der
// Zukunft) und gibt State, Käufer-/Verkäufer-Key, Adressen, ID und Beträge zurück.
func openTestEscrow(t *testing.T) (
	st *State, bk, sk *ecdsa.PrivateKey,
	buyer, seller, feeColl Address, id [32]byte, amount, fee *big.Int,
) {
	t.Helper()
	bk, _ = crypto.GenerateKey()
	sk, _ = crypto.GenerateKey()
	buyer = PubkeyToAddress(&bk.PublicKey)
	seller = PubkeyToAddress(&sk.PublicKey)
	feeColl[0] = 0xFE

	st = NewState()
	st.Credit(buyer, new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND)))

	amount = new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))
	fee = FeeForValue(amount)
	open := &Transaction{Type: TxEscrowOpen, Nonce: 0, Fee: fee,
		Payload: (&EscrowOpenPayload{Seller: seller, Amount: amount, Deadline: 100000}).encode()}
	if err := SignTransaction(open, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(open, feeColl, Address{}, 1); err != nil {
		t.Fatalf("open: %v", err)
	}
	id = open.Hash()
	return
}

// cancelAndSubmit führt cancel (Nonce 1) + submit_return (Nonce 2) durch.
func cancelAndSubmit(t *testing.T, st *State, bk *ecdsa.PrivateKey, feeColl Address, id [32]byte) {
	t.Helper()
	cancel := &Transaction{Type: TxEscrowCancel, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(cancel, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(cancel, feeColl, Address{}, 2); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	var trk [32]byte
	trk[0] = 0xAB
	ret := &Transaction{Type: TxEscrowSubmitReturn, Nonce: 2, Fee: big.NewInt(0),
		Payload: (&EscrowReturnPayload{EscrowID: id, TrackingHash: trk}).encode()}
	if err := SignTransaction(ret, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(ret, feeColl, Address{}, 3); err != nil {
		t.Fatalf("submit_return: %v", err)
	}
}

// ── Tests ────────────────────────────────────────────────────────────────────

// Happy-Path: open → cancel → submit_return → confirm_return (Verkäufer)
// → Betrag zurück an Käufer, Escrow geschlossen.
func TestEscrowCancelReturnHappyPath(t *testing.T) {
	st, bk, sk, buyer, seller, feeColl, id, _, fee := openTestEscrow(t)
	start := new(big.Int).Mul(big.NewInt(100), big.NewInt(UFNDPerFND))

	cancelAndSubmit(t, st, bk, feeColl, id)

	e, _ := st.GetEscrow(id)
	if e.State != EscrowReturnSubmitted {
		t.Fatalf("State = %d, want return_submitted", e.State)
	}
	if e.ReturnDeadline != 3+returnWindowBlocks {
		t.Fatalf("ReturnDeadline = %d, want %d", e.ReturnDeadline, 3+returnWindowBlocks)
	}

	conf := &Transaction{Type: TxEscrowConfirmReturn, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(conf, sk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(conf, feeColl, Address{}, 4); err != nil {
		t.Fatalf("confirm_return: %v", err)
	}
	if e2, _ := st.GetEscrow(id); e2.State != EscrowClosed {
		t.Fatalf("State = %d, want closed", e2.State)
	}
	wantBuyer := new(big.Int).Sub(start, fee)
	if st.Balance(buyer).Cmp(wantBuyer) != 0 {
		t.Fatalf("Käufer-Saldo %s != %s", st.Balance(buyer), wantBuyer)
	}
	if st.Balance(seller).Sign() != 0 {
		t.Fatal("Verkäufer dürfte nichts erhalten haben")
	}
}

// Nur der Käufer darf stornieren.
func TestEscrowCancelOnlyByBuyer(t *testing.T) {
	st, _, sk, _, _, feeColl, id, _, _ := openTestEscrow(t)
	cancel := &Transaction{Type: TxEscrowCancel, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(cancel, sk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(cancel, feeColl, Address{}, 2); err == nil {
		t.Fatal("Verkäufer-Storno hätte abgelehnt werden müssen")
	}
}

// submit_return nur nach Storno (nicht direkt aus open).
func TestEscrowSubmitReturnRequiresCancel(t *testing.T) {
	st, bk, _, _, _, feeColl, id, _, _ := openTestEscrow(t)
	var trk [32]byte
	trk[0] = 0x01
	ret := &Transaction{Type: TxEscrowSubmitReturn, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowReturnPayload{EscrowID: id, TrackingHash: trk}).encode()}
	if err := SignTransaction(ret, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(ret, feeColl, Address{}, 2); err == nil {
		t.Fatal("submit_return ohne cancel hätte abgelehnt werden müssen")
	}
}

// submit_return ohne Tracking-Hash wird abgelehnt.
func TestEscrowSubmitReturnNeedsTracking(t *testing.T) {
	st, bk, _, _, _, feeColl, id, _, _ := openTestEscrow(t)
	cancel := &Transaction{Type: TxEscrowCancel, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(cancel, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(cancel, feeColl, Address{}, 2); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	ret := &Transaction{Type: TxEscrowSubmitReturn, Nonce: 2, Fee: big.NewInt(0),
		Payload: (&EscrowReturnPayload{EscrowID: id}).encode()} // leerer Tracking-Hash
	if err := SignTransaction(ret, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(ret, feeColl, Address{}, 3); err == nil {
		t.Fatal("submit_return ohne Tracking-Hash hätte abgelehnt werden müssen")
	}
}

// Vor Fristablauf darf der Käufer NICHT selbst die Rücksendung bestätigen.
func TestEscrowConfirmReturnBuyerBeforeDeadline(t *testing.T) {
	st, bk, _, _, _, feeColl, id, _, _ := openTestEscrow(t)
	cancelAndSubmit(t, st, bk, feeColl, id)
	conf := &Transaction{Type: TxEscrowConfirmReturn, Nonce: 3, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(conf, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(conf, feeColl, Address{}, 5); err == nil {
		t.Fatal("Käufer-Selbstbestätigung vor Frist hätte abgelehnt werden müssen")
	}
}

// Nach Fristablauf DARF der Käufer selbst bestätigen.
func TestEscrowConfirmReturnBuyerAfterDeadline(t *testing.T) {
	st, bk, _, buyer, _, feeColl, id, amount, _ := openTestEscrow(t)
	cancelAndSubmit(t, st, bk, feeColl, id)
	e, _ := st.GetEscrow(id)
	balBefore := new(big.Int).Set(st.Balance(buyer))

	conf := &Transaction{Type: TxEscrowConfirmReturn, Nonce: 3, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(conf, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(conf, feeColl, Address{}, e.ReturnDeadline); err != nil {
		t.Fatalf("confirm_return nach Frist: %v", err)
	}
	if e2, _ := st.GetEscrow(id); e2.State != EscrowClosed {
		t.Fatal("Escrow sollte geschlossen sein")
	}
	wantBal := new(big.Int).Add(balBefore, amount)
	if st.Balance(buyer).Cmp(wantBal) != 0 {
		t.Fatalf("Käufer-Saldo %s != %s", st.Balance(buyer), wantBal)
	}
}

// confirm_return durch einen Dritten wird abgelehnt.
func TestEscrowConfirmReturnByStranger(t *testing.T) {
	st, bk, _, _, _, feeColl, id, _, _ := openTestEscrow(t)
	cancelAndSubmit(t, st, bk, feeColl, id)
	strangerKey, _ := crypto.GenerateKey()
	stranger := PubkeyToAddress(&strangerKey.PublicKey)
	st.Credit(stranger, big.NewInt(UFNDPerFND))
	conf := &Transaction{Type: TxEscrowConfirmReturn, Nonce: 0, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(conf, strangerKey); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(conf, feeColl, Address{}, 5); err == nil {
		t.Fatal("confirm_return durch Fremden hätte abgelehnt werden müssen")
	}
}

// Nach Abschluss ist kein cancel mehr möglich.
func TestEscrowNoCancelAfterClosed(t *testing.T) {
	st, bk, _, _, _, feeColl, id, _, _ := openTestEscrow(t)
	confirm := &Transaction{Type: TxEscrowConfirm, Nonce: 1, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(confirm, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(confirm, feeColl, Address{}, 2); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	cancel := &Transaction{Type: TxEscrowCancel, Nonce: 2, Fee: big.NewInt(0),
		Payload: (&EscrowRefPayload{EscrowID: id}).encode()}
	if err := SignTransaction(cancel, bk); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(cancel, feeColl, Address{}, 3); err == nil {
		t.Fatal("cancel nach closed hätte abgelehnt werden müssen")
	}
}
