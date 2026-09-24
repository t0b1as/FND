package filestore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/fundus/node/internal/chain"
)

// Hilfsfunktion: signierte Quittung von consKey für providerAddr über n Bytes.
func mkReceipt(t *testing.T, providerAddr chain.Address, nonce uint64, kind chain.ReceiptKind, n uint64) chain.Receipt {
	t.Helper()
	consKey, _ := crypto.GenerateKey()
	r := chain.Receipt{
		Kind:      kind,
		Provider:  providerAddr,
		Bytes:     n,
		Timestamp: 1_700_000_000,
		Nonce:     nonce,
	}
	// ChunkHash variieren über die Nonce, damit ReceiptIDs sich unterscheiden.
	var h [32]byte
	h[0] = byte(nonce)
	r.ChunkHash = h
	if err := chain.SignReceipt(&r, consKey); err != nil {
		t.Fatalf("SignReceipt: %v", err)
	}
	return r
}

func TestReceiptStoreAddAndCount(t *testing.T) {
	dir := t.TempDir()
	rs := newReceiptStore(dir, nil)
	provKey, _ := crypto.GenerateKey()
	prov := chain.PubkeyToAddress(&provKey.PublicKey)

	r1 := mkReceipt(t, prov, 1, chain.ReceiptFetch, 1000)
	if !rs.Add(r1) {
		t.Fatal("erste Quittung sollte angenommen werden")
	}
	if rs.Count() != 1 {
		t.Fatalf("Count = %d, erwartet 1", rs.Count())
	}
	// Dieselbe Quittung erneut → Duplikat, abgelehnt.
	if rs.Add(r1) {
		t.Fatal("Duplikat sollte abgelehnt werden")
	}
	if rs.Count() != 1 {
		t.Fatalf("Count nach Duplikat = %d, erwartet 1", rs.Count())
	}
}

func TestReceiptStorePendingBytes(t *testing.T) {
	rs := newReceiptStore(t.TempDir(), nil)
	provKey, _ := crypto.GenerateKey()
	prov := chain.PubkeyToAddress(&provKey.PublicKey)

	rs.Add(mkReceipt(t, prov, 1, chain.ReceiptFetch, 1000))
	rs.Add(mkReceipt(t, prov, 2, chain.ReceiptFetch, 500))
	rs.Add(mkReceipt(t, prov, 3, chain.ReceiptStore, 2000))

	fetchB, storeB := rs.PendingBytes()
	if fetchB != 1500 {
		t.Errorf("fetchBytes = %d, erwartet 1500", fetchB)
	}
	if storeB != 2000 {
		t.Errorf("storeBytes = %d, erwartet 2000", storeB)
	}
}

func TestReceiptStoreSettle(t *testing.T) {
	rs := newReceiptStore(t.TempDir(), nil)
	provKey, _ := crypto.GenerateKey()
	prov := chain.PubkeyToAddress(&provKey.PublicKey)

	r1 := mkReceipt(t, prov, 1, chain.ReceiptFetch, 1000)
	r2 := mkReceipt(t, prov, 2, chain.ReceiptFetch, 500)
	rs.Add(r1)
	rs.Add(r2)

	rs.Settle([][32]byte{r1.ReceiptID()})
	if rs.Count() != 1 {
		t.Fatalf("Count nach Settle = %d, erwartet 1", rs.Count())
	}
	// r2 bleibt übrig.
	snap := rs.Snapshot()
	if len(snap) != 1 || snap[0].ReceiptID() != r2.ReceiptID() {
		t.Fatal("nach Settle sollte nur r2 übrig sein")
	}
}

// Persistenz: nach Neustart (neuer Store auf gleichem Verzeichnis) sind die
// Quittungen wieder da.
func TestReceiptStorePersistence(t *testing.T) {
	dir := t.TempDir()
	provKey, _ := crypto.GenerateKey()
	prov := chain.PubkeyToAddress(&provKey.PublicKey)

	rs1 := newReceiptStore(dir, nil)
	rs1.Add(mkReceipt(t, prov, 1, chain.ReceiptFetch, 1000))
	rs1.Add(mkReceipt(t, prov, 2, chain.ReceiptStore, 2000))

	// "Neustart": frischer Store auf demselben Verzeichnis.
	rs2 := newReceiptStore(dir, nil)
	if rs2.Count() != 2 {
		t.Fatalf("nach Reload Count = %d, erwartet 2", rs2.Count())
	}
	fetchB, storeB := rs2.PendingBytes()
	if fetchB != 1000 || storeB != 2000 {
		t.Errorf("nach Reload fetch=%d store=%d, erwartet 1000/2000", fetchB, storeB)
	}
}

// Manipulierte Persistenz-Datei: ungültige Quittungen werden beim Laden verworfen.
func TestReceiptStoreRejectsTamperedFile(t *testing.T) {
	dir := t.TempDir()
	// Kaputte JSON-Datei hinterlegen.
	if err := os.WriteFile(filepath.Join(dir, "receipts.json"), []byte("{nicht valide"), 0600); err != nil {
		t.Fatal(err)
	}
	rs := newReceiptStore(dir, nil) // darf nicht paniken
	if rs.Count() != 0 {
		t.Fatalf("kaputte Datei sollte zu leerem Store führen, Count=%d", rs.Count())
	}
}
