package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// mkHostingReceipt baut eine signierte Vorhaltungs-Quittung (Consumer bezeugt,
// dass Provider den Chunk `durationSeconds` lang gehalten hat).
func mkHostingReceipt(t *testing.T, provider Address, nonce, bytes, durationSeconds uint64) Receipt {
	t.Helper()
	consKey, _ := crypto.GenerateKey()
	r := Receipt{
		Kind:            ReceiptHosting,
		Provider:        provider,
		Bytes:           bytes,
		DurationSeconds: durationSeconds,
		Timestamp:       1_700_000_000,
		Nonce:           nonce,
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

// Ein TB, einen vollen Monat gehalten, ergibt exakt 1 FND (1e9 uFND).
func TestHostingRewardOneTBOneMonth(t *testing.T) {
	oneTB := uint64(1_000_000_000_000)
	oneMonth := uint64(secondsPerRewardMonth)
	got := rewardForHosting(oneTB, oneMonth)
	want := uint64(1_000_000_000) // 1 FND in uFND
	if got.Uint64() != want {
		t.Fatalf("1 TB·Monat = %d uFND, erwartet %d", got.Uint64(), want)
	}
}

// Halbe Zeit → halbe Vergütung.
func TestHostingRewardHalfMonth(t *testing.T) {
	oneTB := uint64(1_000_000_000_000)
	halfMonth := uint64(secondsPerRewardMonth / 2)
	got := rewardForHosting(oneTB, halfMonth)
	want := uint64(500_000_000) // 0,5 FND
	if got.Uint64() != want {
		t.Fatalf("1 TB·halber Monat = %d uFND, erwartet %d", got.Uint64(), want)
	}
}

// Dauer 0 → keine Vergütung.
func TestHostingRewardZeroDuration(t *testing.T) {
	got := rewardForHosting(1_000_000_000_000, 0)
	if got.Sign() != 0 {
		t.Fatalf("Dauer 0 sollte 0 uFND ergeben, war %d", got.Uint64())
	}
}

// Vorhaltungs-Quittung ohne Dauer wird abgelehnt.
func TestHostingReceiptRequiresDuration(t *testing.T) {
	consKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)
	r := Receipt{
		Kind:      ReceiptHosting,
		Provider:  provider,
		Bytes:     1000,
		Timestamp: 1_700_000_000,
		Nonce:     1,
		// DurationSeconds absichtlich 0
	}
	r.ChunkHash[0] = 1
	if err := SignReceipt(&r, consKey); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	if err := r.VerifyReceipt(); err == nil {
		t.Fatal("Vorhaltungs-Quittung ohne Dauer sollte abgelehnt werden")
	}
}

// Transfer-Quittung MIT Dauer wird abgelehnt (Tarnung verhindern).
func TestTransferReceiptRejectsDuration(t *testing.T) {
	consKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)
	r := Receipt{
		Kind:            ReceiptFetch,
		Provider:        provider,
		Bytes:           1000,
		DurationSeconds: 999, // unerlaubt bei Fetch
		Timestamp:       1_700_000_000,
		Nonce:           1,
	}
	r.ChunkHash[0] = 1
	if err := SignReceipt(&r, consKey); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	if err := r.VerifyReceipt(); err == nil {
		t.Fatal("Transfer-Quittung mit Dauer sollte abgelehnt werden")
	}
}

// Ende-zu-Ende: Eine Vorhaltungs-Quittung mintet zeitbasiert FND an den Provider.
func TestHostingReceiptMintsTimeBasedReward(t *testing.T) {
	st := NewState()
	prodKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	oneTB := uint64(1_000_000_000_000)
	r := mkHostingReceipt(t, provider, 1, oneTB, uint64(secondsPerRewardMonth))
	raw, _ := (&StorageRewardPayload{Receipts: []Receipt{r}}).Encode()
	tx, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 0)

	if err := st.applyStorageReward(tx); err != nil {
		t.Fatalf("applyStorageReward: %v", err)
	}
	bal := st.Balance(provider)
	if bal.Cmp(big.NewInt(1_000_000_000)) != 0 { // 1 FND
		t.Fatalf("Provider-Saldo = %s, erwartet 1000000000 (1 FND)", bal.String())
	}
}
