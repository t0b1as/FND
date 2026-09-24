package chain

import (
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Hilfsfunktion: erzeugt eine gültige, signierte Fetch-Quittung mit frischem
// Provider und Consumer.
func makeValidReceipt(t *testing.T) *Receipt {
	t.Helper()
	provKey, _ := crypto.GenerateKey()
	consKey, _ := crypto.GenerateKey()
	r := &Receipt{
		Kind:      ReceiptFetch,
		Provider:  PubkeyToAddress(&provKey.PublicKey),
		ChunkHash: chainHash([]byte("chunk-xyz")),
		Bytes:     1 << 20, // 1 MiB
		Timestamp: 1_700_000_000,
		Nonce:     42,
	}
	if err := SignReceipt(r, consKey); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	return r
}

func TestReceiptSignVerify(t *testing.T) {
	r := makeValidReceipt(t)
	if err := r.VerifyReceipt(); err != nil {
		t.Fatalf("gültige Quittung sollte verifizieren, bekam: %v", err)
	}
}

func TestReceiptConsumerSetFromKey(t *testing.T) {
	consKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	r := &Receipt{
		Kind:      ReceiptStore,
		Provider:  PubkeyToAddress(&provKey.PublicKey),
		ChunkHash: chainHash([]byte("c")),
		Bytes:     500,
		Timestamp: 1_700_000_000,
		Nonce:     7,
	}
	if err := SignReceipt(r, consKey); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	// Consumer muss aus dem Schlüssel abgeleitet worden sein.
	if r.Consumer != PubkeyToAddress(&consKey.PublicKey) {
		t.Fatal("Consumer wurde nicht korrekt aus dem Schlüssel gesetzt")
	}
	if err := r.VerifyReceipt(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// Angriff: Provider manipuliert die Bytes-Zahl nach der Signatur nach oben.
func TestReceiptTamperBytesFails(t *testing.T) {
	r := makeValidReceipt(t)
	r.Bytes = 1 << 40 // 1 TiB statt 1 MiB — Betrugsversuch
	if err := r.VerifyReceipt(); err == nil {
		t.Fatal("manipulierte Bytes-Zahl MUSS die Verifikation brechen")
	}
}

// Angriff: Provider ändert den Provider-Empfänger auf eine andere Adresse.
func TestReceiptTamperProviderFails(t *testing.T) {
	r := makeValidReceipt(t)
	otherKey, _ := crypto.GenerateKey()
	r.Provider = PubkeyToAddress(&otherKey.PublicKey)
	if err := r.VerifyReceipt(); err == nil {
		t.Fatal("geänderter Provider MUSS die Verifikation brechen")
	}
}

// Angriff: Selbst-Quittung — Provider signiert sich selbst eine Quittung.
func TestReceiptSelfIssuedFails(t *testing.T) {
	key, _ := crypto.GenerateKey()
	addr := PubkeyToAddress(&key.PublicKey)
	r := &Receipt{
		Kind:      ReceiptFetch,
		Provider:  addr, // Provider == Consumer
		ChunkHash: chainHash([]byte("c")),
		Bytes:     1000,
		Timestamp: 1_700_000_000,
		Nonce:     1,
	}
	if err := SignReceipt(r, key); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	if err := r.VerifyReceipt(); err == nil {
		t.Fatal("Selbst-Quittung (Provider==Consumer) MUSS abgelehnt werden")
	}
}

// Angriff: leere Signatur / 0 Bytes / unbekannte Art.
func TestReceiptInvalidFields(t *testing.T) {
	base := makeValidReceipt(t)

	// 0 Bytes
	r1 := *base
	r1.Bytes = 0
	if err := r1.VerifyReceipt(); err == nil {
		t.Error("0 Bytes sollte abgelehnt werden")
	}

	// Falsche Signaturlänge
	r2 := *base
	r2.Signature = []byte{1, 2, 3}
	if err := r2.VerifyReceipt(); err == nil {
		t.Error("zu kurze Signatur sollte abgelehnt werden")
	}

	// Unbekannte Art
	r3 := *base
	r3.Kind = ReceiptKind(99)
	if err := r3.VerifyReceipt(); err == nil {
		t.Error("unbekannte Quittungs-Art sollte abgelehnt werden")
	}
}

// ReceiptID muss eindeutig und stabil sein (gleiche Quittung → gleiche ID,
// verschiedene Nonce → verschiedene ID).
func TestReceiptIDStableAndUnique(t *testing.T) {
	r := makeValidReceipt(t)
	id1 := r.ReceiptID()
	id2 := r.ReceiptID()
	if id1 != id2 {
		t.Fatal("ReceiptID muss deterministisch sein")
	}
	r2 := *r
	r2.Nonce = r.Nonce + 1
	if r2.ReceiptID() == id1 {
		t.Fatal("verschiedene Nonce muss zu verschiedener ReceiptID führen")
	}
}
