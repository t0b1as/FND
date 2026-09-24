package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Hilfsfunktion: signierte Fetch-Quittung (Konsument signiert für Provider).
func mkRewardReceipt(t *testing.T, provider Address, nonce uint64, bytes uint64) Receipt {
	t.Helper()
	consKey, _ := crypto.GenerateKey()
	r := Receipt{
		Kind:      ReceiptFetch,
		Provider:  provider,
		Bytes:     bytes,
		Timestamp: 1_700_000_000,
		Nonce:     nonce,
	}
	var h [32]byte
	h[0] = byte(nonce)
	h[1] = byte(bytes)
	r.ChunkHash = h
	if err := SignReceipt(&r, consKey); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	return r
}

func TestStorageRewardMints(t *testing.T) {
	st := NewState()
	prodKey, _ := crypto.GenerateKey()
	producer := PubkeyToAddress(&prodKey.PublicKey)
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	// 1 TB = 1e12 Bytes → 1 FND = 1e9 uFND.
	oneTB := uint64(1_000_000_000_000)
	r := mkRewardReceipt(t, provider, 1, oneTB)

	payload := StorageRewardPayload{Receipts: []Receipt{r}}
	raw, _ := payload.Encode()
	tx, err := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 0)
	if err != nil {
		t.Fatalf("BuildSignedTx: %v", err)
	}

	if err := st.ApplyTransaction(tx, Address{}, Address{}, 1); err != nil {
		t.Fatalf("ApplyTransaction: %v", err)
	}
	// Provider sollte 1 FND (1e9 uFND) bekommen haben.
	bal := st.Balance(provider)
	if bal.Cmp(big.NewInt(1_000_000_000)) != 0 {
		t.Fatalf("Provider-Saldo = %s, erwartet 1e9 uFND (1 FND)", bal.String())
	}
	// Produzent-Nonce sollte erhöht sein.
	_ = producer
}

func TestStorageRewardReplayRejected(t *testing.T) {
	st := NewState()
	prodKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	r := mkRewardReceipt(t, provider, 1, 2_000_000_000_000) // 2 TB
	payload := StorageRewardPayload{Receipts: []Receipt{r}}
	raw, _ := payload.Encode()

	tx1, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 0)
	if err := st.ApplyTransaction(tx1, Address{}, Address{}, 1); err != nil {
		t.Fatalf("erster Mint: %v", err)
	}

	// Gleiche Quittung erneut (neue Nonce) → muss als Replay abgelehnt werden.
	tx2, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 1)
	if err := st.ApplyTransaction(tx2, Address{}, Address{}, 2); err == nil {
		t.Fatal("Replay derselben Quittung MUSS abgelehnt werden")
	}
}

func TestStorageRewardSelfReceiptRejected(t *testing.T) {
	st := NewState()
	prodKey, _ := crypto.GenerateKey()

	// Selbst-Quittung: Provider signiert sich selbst (Provider == Consumer).
	provider := PubkeyToAddress(&prodKey.PublicKey)
	r := Receipt{
		Kind: ReceiptFetch, Provider: provider, Bytes: 1_000_000_000_000,
		Timestamp: 1_700_000_000, Nonce: 1,
	}
	_ = SignReceipt(&r, prodKey) // Konsument == Provider
	payload := StorageRewardPayload{Receipts: []Receipt{r}}
	raw, _ := payload.Encode()
	tx, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 0)

	if err := st.ApplyTransaction(tx, Address{}, Address{}, 1); err == nil {
		t.Fatal("Selbst-Quittung MUSS den Mint scheitern lassen")
	}
}

func TestStorageRewardTamperedBytesRejected(t *testing.T) {
	st := NewState()
	prodKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	r := mkRewardReceipt(t, provider, 1, 1_000_000_000_000)
	r.Bytes = 999_000_000_000_000 // nachträglich manipuliert → Signatur bricht
	payload := StorageRewardPayload{Receipts: []Receipt{r}}
	raw, _ := payload.Encode()
	tx, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 0)

	if err := st.ApplyTransaction(tx, Address{}, Address{}, 1); err == nil {
		t.Fatal("manipulierte Bytes MUSS den Mint scheitern lassen")
	}
}

func TestStorageRewardMultipleProviders(t *testing.T) {
	st := NewState()
	prodKey, _ := crypto.GenerateKey()

	provKey1, _ := crypto.GenerateKey()
	prov1 := PubkeyToAddress(&provKey1.PublicKey)
	provKey2, _ := crypto.GenerateKey()
	prov2 := PubkeyToAddress(&provKey2.PublicKey)

	oneTB := uint64(1_000_000_000_000)
	receipts := []Receipt{
		mkRewardReceipt(t, prov1, 1, oneTB),     // 1 FND
		mkRewardReceipt(t, prov2, 2, oneTB*2),   // 2 FND
		mkRewardReceipt(t, prov1, 3, oneTB),     // +1 FND → prov1 = 2 FND
	}
	payload := StorageRewardPayload{Receipts: receipts}
	raw, _ := payload.Encode()
	tx, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 0)

	if err := st.ApplyTransaction(tx, Address{}, Address{}, 1); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if st.Balance(prov1).Cmp(big.NewInt(2_000_000_000)) != 0 {
		t.Errorf("prov1 = %s, erwartet 2 FND", st.Balance(prov1).String())
	}
	if st.Balance(prov2).Cmp(big.NewInt(2_000_000_000)) != 0 {
		t.Errorf("prov2 = %s, erwartet 2 FND", st.Balance(prov2).String())
	}
}

func TestStorageRewardWithFeeRejected(t *testing.T) {
	st := NewState()
	prodKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	r := mkRewardReceipt(t, provider, 1, 1_000_000_000_000)
	payload := StorageRewardPayload{Receipts: []Receipt{r}}
	raw, _ := payload.Encode()
	// Mit Gebühr → muss abgelehnt werden (Reward ist gebührenfrei).
	tx, _ := BuildSignedTx(prodKey, TxStorageReward, big.NewInt(1000), raw, 0)

	if err := st.ApplyTransaction(tx, Address{}, Address{}, 1); err == nil {
		t.Fatal("Storage-Reward mit Gebühr MUSS abgelehnt werden")
	}
}
