package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Happy path: Bridge-Operator schreibt FND gut.
func TestSolCreditMints(t *testing.T) {
	st := NewState()
	bridgeKey, _ := crypto.GenerateKey()
	bridge := PubkeyToAddress(&bridgeKey.PublicKey)
	st.SetBridgeAuthority(bridge)

	var recipient Address
	recipient[0] = 0xAB

	payload := SolCreditPayload{Recipient: recipient, UFND: 5_000_000_000, SolTxSig: "solsig111"}
	raw, _ := payload.Encode()
	tx, err := BuildSignedTx(bridgeKey, TxSolCredit, nil, raw, 0)
	if err != nil {
		t.Fatalf("BuildSignedTx: %v", err)
	}
	if err := st.ApplyTransaction(tx, Address{}, Address{}, 1); err != nil {
		t.Fatalf("ApplyTransaction: %v", err)
	}
	if st.Balance(recipient).Cmp(big.NewInt(5_000_000_000)) != 0 {
		t.Fatalf("Empfänger = %s, erwartet 5 FND", st.Balance(recipient))
	}
}

// Anti-Replay: dieselbe Solana-Tx darf nicht zweimal minten.
func TestSolCreditAntiReplay(t *testing.T) {
	st := NewState()
	bridgeKey, _ := crypto.GenerateKey()
	bridge := PubkeyToAddress(&bridgeKey.PublicKey)
	st.SetBridgeAuthority(bridge)

	var recipient Address
	recipient[0] = 0xAB

	mk := func(nonce uint64) *Transaction {
		payload := SolCreditPayload{Recipient: recipient, UFND: 1_000_000_000, SolTxSig: "samesig"}
		raw, _ := payload.Encode()
		tx, _ := BuildSignedTx(bridgeKey, TxSolCredit, nil, raw, nonce)
		return tx
	}

	if err := st.ApplyTransaction(mk(0), Address{}, Address{}, 1); err != nil {
		t.Fatalf("erste Gutschrift: %v", err)
	}
	// Zweiter Versuch mit gleicher SOL-Sig (neue Nonce) → muss scheitern.
	if err := st.ApplyTransaction(mk(1), Address{}, Address{}, 1); err == nil {
		t.Fatal("gleiche Solana-Tx sollte nicht zweimal minten")
	}
	// Saldo blieb bei 1 FND.
	if st.Balance(recipient).Cmp(big.NewInt(1_000_000_000)) != 0 {
		t.Fatalf("Saldo = %s, erwartet 1 FND (kein Doppel-Mint)", st.Balance(recipient))
	}
}

// Nur die Bridge-Autorität darf minten.
func TestSolCreditRejectsUnauthorized(t *testing.T) {
	st := NewState()
	bridgeKey, _ := crypto.GenerateKey()
	st.SetBridgeAuthority(PubkeyToAddress(&bridgeKey.PublicKey))

	// Ein FREMDER Schlüssel versucht zu minten.
	attackerKey, _ := crypto.GenerateKey()
	var recipient Address
	recipient[0] = 0xAB
	payload := SolCreditPayload{Recipient: recipient, UFND: 1_000_000_000, SolTxSig: "sig"}
	raw, _ := payload.Encode()
	tx, _ := BuildSignedTx(attackerKey, TxSolCredit, nil, raw, 0)

	if err := st.ApplyTransaction(tx, Address{}, Address{}, 1); err == nil {
		t.Fatal("nicht-autorisierter SOL-Credit sollte abgelehnt werden")
	}
}

// Ohne gesetzte Bridge-Autorität wird abgelehnt.
func TestSolCreditNoAuthority(t *testing.T) {
	st := NewState()
	key, _ := crypto.GenerateKey()
	var recipient Address
	recipient[0] = 0xAB
	payload := SolCreditPayload{Recipient: recipient, UFND: 1_000_000_000, SolTxSig: "sig"}
	raw, _ := payload.Encode()
	tx, _ := BuildSignedTx(key, TxSolCredit, nil, raw, 0)

	if err := st.ApplyTransaction(tx, Address{}, Address{}, 1); err == nil {
		t.Fatal("ohne Bridge-Autorität sollte abgelehnt werden")
	}
}
