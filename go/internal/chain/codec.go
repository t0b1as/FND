package chain

import (
	"encoding/binary"
	"errors"
	"math/big"

	"lukechampine.com/blake3"
)

// chainHash ist der einheitliche Hash der Chain: BLAKE3-256 (Spec §3a).
func chainHash(b []byte) [32]byte { return blake3.Sum256(b) }

// ─── Kanonische Serialisierung (Spec §3a) ────────────────────────────────────
// Deterministische Byte-Darstellung: feste Feldreihenfolge, big-endian fester
// Breite, längen-präfixierte variable Felder, keine Maps. Zwei Nodes MÜSSEN aus
// demselben logischen Objekt exakt dieselben Bytes erzeugen, sonst Chain-Split.

func putUint64(buf *[]byte, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	*buf = append(*buf, b[:]...)
}

// putUint128 schreibt v als feste 16 Byte big-endian. Panikt nie: zu große
// Werte (>uint128) werden auf die untersten 16 Byte reduziert (deterministisch);
// die Gültigkeitsprüfung (fitsUint128) verwirft solche Txs ohnehin beim Anwenden.
func putUint128(buf *[]byte, v *big.Int) {
	var b [16]byte
	if v != nil && v.Sign() > 0 {
		vb := v.Bytes()
		if len(vb) > 16 {
			vb = vb[len(vb)-16:]
		}
		copy(b[16-len(vb):], vb)
	}
	*buf = append(*buf, b[:]...)
}

func putBytes(buf *[]byte, b []byte) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(b)))
	*buf = append(*buf, l[:]...)
	*buf = append(*buf, b...)
}

// signingBytes ist die kanonische Darstellung einer Tx OHNE Signatur (das, was
// signiert/verifiziert wird).
func (tx *Transaction) signingBytes() []byte {
	buf := make([]byte, 0, 64+len(tx.Payload))
	buf = append(buf, byte(tx.Type))
	buf = append(buf, tx.From[:]...)
	putUint64(&buf, tx.Nonce)
	putUint128(&buf, tx.Fee)
	putBytes(&buf, tx.Payload)
	return buf
}

// Bytes ist die vollständige kanonische Darstellung (inkl. Signatur).
func (tx *Transaction) Bytes() []byte {
	buf := tx.signingBytes()
	putBytes(&buf, tx.Signature)
	return buf
}

// Hash ist der Transaktions-Hash (BLAKE3 über die vollständige Darstellung).
func (tx *Transaction) Hash() [32]byte { return chainHash(tx.Bytes()) }

// encode serialisiert eine TransferPayload kanonisch: to(20) || amount(16).
func (p *TransferPayload) encode() []byte {
	buf := make([]byte, 0, AddressLen+16)
	buf = append(buf, p.To[:]...)
	putUint128(&buf, p.Amount)
	return buf
}

func decodeTransferPayload(b []byte) (*TransferPayload, error) {
	if len(b) != AddressLen+16 {
		return nil, errors.New("chain: ungültige Transfer-Payload-Länge")
	}
	p := &TransferPayload{Amount: new(big.Int)}
	copy(p.To[:], b[:AddressLen])
	p.Amount.SetBytes(b[AddressLen:])
	return p, nil
}

// encode serialisiert einen Account kanonisch: balance(16) || nonce(8).
func (a *Account) encode() []byte {
	buf := make([]byte, 0, 24)
	putUint128(&buf, a.Balance)
	putUint64(&buf, a.Nonce)
	return buf
}

// ─── Merkle-Baum mit Domain-Separation (Spec §3a) ────────────────────────────
// leaf     = chainHash(0x00 || key || value)
// internal = chainHash(0x01 || left || right)
// Das Prefix verhindert, dass ein Blatt als innerer Knoten missdeutet wird.

const (
	leafPrefix     = 0x00
	internalPrefix = 0x01
)

func hashLeaf(key []byte, value []byte) [32]byte {
	buf := make([]byte, 0, 1+len(key)+len(value))
	buf = append(buf, leafPrefix)
	buf = append(buf, key...)
	buf = append(buf, value...)
	return chainHash(buf)
}

func hashInternal(l, r [32]byte) [32]byte {
	buf := make([]byte, 0, 1+64)
	buf = append(buf, internalPrefix)
	buf = append(buf, l[:]...)
	buf = append(buf, r[:]...)
	return chainHash(buf)
}

// merkleRoot baut die Wurzel über bereits geordnete Blätter. Bei ungerader
// Knotenzahl wird der letzte Knoten unverändert hochgereicht (kein Duplizieren →
// keine Malleability).
func merkleRoot(leaves [][32]byte, emptyTag string) [32]byte {
	if len(leaves) == 0 {
		return chainHash([]byte(emptyTag))
	}
	level := leaves
	for len(level) > 1 {
		next := make([][32]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 < len(level) {
				next = append(next, hashInternal(level[i], level[i+1]))
			} else {
				next = append(next, level[i])
			}
		}
		level = next
	}
	return level[0]
}
