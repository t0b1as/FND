package chain

import (
	"bytes"
	"math/big"
	"testing"
)

// TestBlockBinaryRoundtrip stellt sicher, dass encode∘decode einen Block exakt
// reproduziert — inklusive Head-Hash. Diese Invariante ist kritisch: Weicht der
// dekodierte Block ab, divergiert der State beim Replay.
func TestBlockBinaryRoundtrip(t *testing.T) {
	// Ein Block mit gemischtem Inhalt: mehrere Tx-Typen, Commit-Signaturen.
	mkAddr := func(b byte) Address {
		var a Address
		for i := range a {
			a[i] = b
		}
		return a
	}
	mk32 := func(b byte) [32]byte {
		var h [32]byte
		for i := range h {
			h[i] = b
		}
		return h
	}
	blk := &Block{
		Header: BlockHeader{
			Height:       42,
			PrevHash:     mk32(0x11),
			Timestamp:    1735689600,
			Proposer:     mkAddr(0x22),
			FeeCollector: mkAddr(0x33),
			TxRoot:       mk32(0x44),
			StateRoot:    mk32(0x55),
			ValSetHash:   mk32(0x66),
			Round:        3,
		},
		Transactions: []*Transaction{
			{Type: TxTransfer, From: mkAddr(0xA1), Nonce: 7, Fee: big.NewInt(1800),
				Payload: []byte{0x01, 0x02, 0x03}, Signature: bytes.Repeat([]byte{0xCC}, 65)},
			{Type: TxStorageReward, From: mkAddr(0xB2), Nonce: 0, Fee: big.NewInt(0),
				Payload: nil, Signature: bytes.Repeat([]byte{0xDD}, 65)},
		},
		Commit: [][]byte{
			bytes.Repeat([]byte{0xEE}, 65),
			bytes.Repeat([]byte{0xFF}, 65),
		},
	}

	enc := encodeBlockBinary(blk)
	dec, err := decodeBlockBinary(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Re-encode muss byte-identisch sein.
	enc2 := encodeBlockBinary(dec)
	if !bytes.Equal(enc, enc2) {
		t.Fatal("re-encode weicht ab — Serialisierung nicht verlustfrei")
	}

	// Header-Hash muss erhalten bleiben (die eigentliche Invariante).
	if blk.Header.Hash() != dec.Header.Hash() {
		t.Fatal("Header-Hash weicht nach Round-Trip ab")
	}

	// Transaktionen einzeln prüfen.
	if len(dec.Transactions) != len(blk.Transactions) {
		t.Fatalf("Tx-Anzahl: erwartet %d, bekam %d", len(blk.Transactions), len(dec.Transactions))
	}
	for i, tx := range blk.Transactions {
		if tx.Hash() != dec.Transactions[i].Hash() {
			t.Fatalf("Tx %d Hash weicht ab", i)
		}
	}
	if len(dec.Commit) != len(blk.Commit) {
		t.Fatalf("Commit-Anzahl: erwartet %d, bekam %d", len(blk.Commit), len(dec.Commit))
	}
	for i := range blk.Commit {
		if !bytes.Equal(blk.Commit[i], dec.Commit[i]) {
			t.Fatalf("Commit %d weicht ab", i)
		}
	}
}

// TestGenesisBinaryRoundtrip prüft den Genesis-Block mit GenesisAllocs.
func TestGenesisBinaryRoundtrip(t *testing.T) {
	mkAddr := func(b byte) Address {
		var a Address
		for i := range a {
			a[i] = b
		}
		return a
	}
	gen := &Block{
		Header: BlockHeader{Height: 0, Timestamp: 1735689600},
		GenesisAllocs: []GenesisAccount{
			{Address: mkAddr(0x01), Balance: big.NewInt(1_000_000)},
			{Address: mkAddr(0x02), Balance: big.NewInt(500)},
		},
	}
	enc := encodeBlockBinary(gen)
	dec, err := decodeBlockBinary(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dec.GenesisAllocs) != 2 {
		t.Fatalf("GenesisAllocs-Anzahl: erwartet 2, bekam %d", len(dec.GenesisAllocs))
	}
	for i, a := range gen.GenesisAllocs {
		if dec.GenesisAllocs[i].Address != a.Address {
			t.Fatalf("Alloc %d Adresse weicht ab", i)
		}
		if dec.GenesisAllocs[i].Balance.Cmp(a.Balance) != 0 {
			t.Fatalf("Alloc %d Balance weicht ab", i)
		}
	}
	if gen.Header.Hash() != dec.Header.Hash() {
		t.Fatal("Genesis-Header-Hash weicht ab")
	}
}
