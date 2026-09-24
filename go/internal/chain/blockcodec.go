package chain

// Binäre, verlustfreie Serialisierung eines ganzen Blocks — für die Persistenz
// in der Blockstore-DB (bbolt). Kompakter als JSON (rohe Bytes statt Hex, keine
// Feldnamen) und deterministisch. KRITISCH: encode∘decode muss den Block exakt
// reproduzieren, sonst weicht der Head-Hash beim Replay ab. Der Round-Trip-Test
// (blockcodec_test.go) sichert das ab.

import (
	"encoding/binary"
	"errors"
	"math/big"
)

// ── Lese-Cursor (Gegenstück zu den put*-Helfern in codec.go) ────────────────

type reader struct {
	b   []byte
	pos int
	err error
}

func newReader(b []byte) *reader { return &reader{b: b} }

func (r *reader) readByte() byte {
	if r.err != nil {
		return 0
	}
	if r.pos+1 > len(r.b) {
		r.err = errors.New("chain: binär-decode: unerwartetes Ende (byte)")
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

func (r *reader) readUint64() uint64 {
	if r.err != nil {
		return 0
	}
	if r.pos+8 > len(r.b) {
		r.err = errors.New("chain: binär-decode: unerwartetes Ende (uint64)")
		return 0
	}
	v := binary.BigEndian.Uint64(r.b[r.pos : r.pos+8])
	r.pos += 8
	return v
}

func (r *reader) readUint128() *big.Int {
	if r.err != nil {
		return new(big.Int)
	}
	if r.pos+16 > len(r.b) {
		r.err = errors.New("chain: binär-decode: unerwartetes Ende (uint128)")
		return new(big.Int)
	}
	v := new(big.Int).SetBytes(r.b[r.pos : r.pos+16])
	r.pos += 16
	return v
}

func (r *reader) readBytes() []byte {
	if r.err != nil {
		return nil
	}
	if r.pos+4 > len(r.b) {
		r.err = errors.New("chain: binär-decode: unerwartetes Ende (len)")
		return nil
	}
	n := int(binary.BigEndian.Uint32(r.b[r.pos : r.pos+4]))
	r.pos += 4
	if n < 0 || r.pos+n > len(r.b) {
		r.err = errors.New("chain: binär-decode: Länge außerhalb des Puffers")
		return nil
	}
	out := make([]byte, n)
	copy(out, r.b[r.pos:r.pos+n])
	r.pos += n
	return out
}

func (r *reader) readAddr() Address {
	var a Address
	if r.err != nil {
		return a
	}
	if r.pos+AddressLen > len(r.b) {
		r.err = errors.New("chain: binär-decode: unerwartetes Ende (addr)")
		return a
	}
	copy(a[:], r.b[r.pos:r.pos+AddressLen])
	r.pos += AddressLen
	return a
}

func (r *reader) read32() [32]byte {
	var h [32]byte
	if r.err != nil {
		return h
	}
	if r.pos+32 > len(r.b) {
		r.err = errors.New("chain: binär-decode: unerwartetes Ende (hash32)")
		return h
	}
	copy(h[:], r.b[r.pos:r.pos+32])
	r.pos += 32
	return h
}

// ── Block ⇄ Bytes ───────────────────────────────────────────────────────────
//
// Layout (alle Längen big-endian):
//   Header: Height u64 | PrevHash 32 | Timestamp u64 | Proposer 20 |
//           FeeCollector 20 | TxRoot 32 | StateRoot 32 | ValSetHash 32 | Round u64
//   Transactions: count u32 | für jede: Bytes() als len-präfixierter Block
//   Commit:       count u32 | für jede Signatur: len-präfixiert
//   GenesisAllocs: count u32 | für jede: Address 20 | Balance u128

// encodeBlockBinary serialisiert einen Block vollständig und verlustfrei.
func encodeBlockBinary(b *Block) []byte {
	buf := make([]byte, 0, 256)
	// Header (dieselbe Feldreihenfolge wie header.bytes(), damit konsistent).
	buf = append(buf, b.Header.bytes()...)

	// Transactions.
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], uint32(len(b.Transactions)))
	buf = append(buf, c[:]...)
	for _, tx := range b.Transactions {
		putBytes(&buf, tx.Bytes()) // tx.Bytes() ist vollständig (inkl. Signatur)
	}

	// Commit-Signaturen.
	binary.BigEndian.PutUint32(c[:], uint32(len(b.Commit)))
	buf = append(buf, c[:]...)
	for _, sig := range b.Commit {
		putBytes(&buf, sig)
	}

	// GenesisAllocs (nur im Genesis-Block gefüllt).
	binary.BigEndian.PutUint32(c[:], uint32(len(b.GenesisAllocs)))
	buf = append(buf, c[:]...)
	for _, a := range b.GenesisAllocs {
		buf = append(buf, a.Address[:]...)
		putUint128(&buf, a.Balance)
	}
	return buf
}

// decodeBlockBinary rekonstruiert einen Block aus encodeBlockBinary.
func decodeBlockBinary(data []byte) (*Block, error) {
	r := newReader(data)
	blk := &Block{}
	// Header.
	blk.Header.Height = r.readUint64()
	blk.Header.PrevHash = r.read32()
	blk.Header.Timestamp = r.readUint64()
	blk.Header.Proposer = r.readAddr()
	blk.Header.FeeCollector = r.readAddr()
	blk.Header.TxRoot = r.read32()
	blk.Header.StateRoot = r.read32()
	blk.Header.ValSetHash = r.read32()
	blk.Header.Round = r.readUint64()

	// Transactions.
	if r.err != nil {
		return nil, r.err
	}
	if r.pos+4 > len(r.b) {
		return nil, errors.New("chain: binär-decode: Tx-Anzahl fehlt")
	}
	txCount := int(binary.BigEndian.Uint32(r.b[r.pos : r.pos+4]))
	r.pos += 4
	for i := 0; i < txCount; i++ {
		raw := r.readBytes()
		if r.err != nil {
			return nil, r.err
		}
		tx, err := decodeTxBinary(raw)
		if err != nil {
			return nil, err
		}
		blk.Transactions = append(blk.Transactions, tx)
	}

	// Commit.
	if r.pos+4 > len(r.b) {
		return nil, errors.New("chain: binär-decode: Commit-Anzahl fehlt")
	}
	commitCount := int(binary.BigEndian.Uint32(r.b[r.pos : r.pos+4]))
	r.pos += 4
	for i := 0; i < commitCount; i++ {
		sig := r.readBytes()
		if r.err != nil {
			return nil, r.err
		}
		blk.Commit = append(blk.Commit, sig)
	}

	// GenesisAllocs.
	if r.pos+4 > len(r.b) {
		return nil, errors.New("chain: binär-decode: Alloc-Anzahl fehlt")
	}
	allocCount := int(binary.BigEndian.Uint32(r.b[r.pos : r.pos+4]))
	r.pos += 4
	for i := 0; i < allocCount; i++ {
		addr := r.readAddr()
		bal := r.readUint128()
		if r.err != nil {
			return nil, r.err
		}
		blk.GenesisAllocs = append(blk.GenesisAllocs, GenesisAccount{Address: addr, Balance: bal})
	}
	if r.err != nil {
		return nil, r.err
	}
	return blk, nil
}

// decodeTxBinary rekonstruiert eine Transaktion aus tx.Bytes().
// Layout: Type 1 | From 20 | Nonce u64 | Fee u128 | Payload(len-präfixiert) |
//         Signature(len-präfixiert)
func decodeTxBinary(data []byte) (*Transaction, error) {
	r := newReader(data)
	tx := &Transaction{}
	tx.Type = TxType(r.readByte())
	tx.From = r.readAddr()
	tx.Nonce = r.readUint64()
	tx.Fee = r.readUint128()
	tx.Payload = r.readBytes()
	tx.Signature = r.readBytes()
	if r.err != nil {
		return nil, r.err
	}
	return tx, nil
}
