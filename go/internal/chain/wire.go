package chain

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
)

// JSON-sichere Wire-Repräsentation für die Persistenz. big.Int → Dezimalstring,
// [32]byte/Address → Hex, []byte → Hex. Die kanonische Block-Identität bleibt
// der BLAKE3-Header-Hash (nicht das JSON) — das JSON ist nur Speicherformat.

type wireHeader struct {
	Height       uint64 `json:"height"`
	PrevHash     string `json:"prev_hash"`
	Timestamp    uint64 `json:"timestamp"`
	Proposer     string `json:"proposer"`
	FeeCollector string `json:"fee_collector"`
	TxRoot       string `json:"tx_root"`
	StateRoot    string `json:"state_root"`
	ValSetHash   string `json:"val_set_hash"`
	Round        uint64 `json:"round"`
}

type wireTx struct {
	Type      uint8  `json:"type"`
	From      string `json:"from"`
	Nonce     uint64 `json:"nonce"`
	Fee       string `json:"fee"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type wireAlloc struct {
	Address string `json:"address"`
	Balance string `json:"balance"`
}

type wireBlock struct {
	Header        wireHeader  `json:"header"`
	Transactions  []wireTx    `json:"transactions"`
	Commit        []string    `json:"commit"`
	GenesisAllocs []wireAlloc `json:"genesis_allocs,omitempty"`
}

func h32(b [32]byte) string  { return hex.EncodeToString(b[:]) }
func haddr(a Address) string { return hex.EncodeToString(a[:]) }

func hexTo32(s string) [32]byte {
	var out [32]byte
	b, _ := hex.DecodeString(s)
	copy(out[:], b)
	return out
}

func hexToAddr(s string) Address {
	var out Address
	b, _ := hex.DecodeString(s)
	copy(out[:], b)
	return out
}

// BlockToJSON serialisiert einen Block ins menschenlesbare JSON-Wire-Format
// (dasselbe wie das frühere Datei-Format). Für Inspektion via 'dump-block'.
func BlockToJSON(blk *Block) ([]byte, error) {
	return json.MarshalIndent(blockToWire(blk), "", "  ")
}

func blockToWire(blk *Block) *wireBlock {
	w := &wireBlock{
		Header: wireHeader{
			Height:     blk.Header.Height,
			PrevHash:   h32(blk.Header.PrevHash),
			Timestamp:  blk.Header.Timestamp,
			Proposer:     haddr(blk.Header.Proposer),
			FeeCollector: haddr(blk.Header.FeeCollector),
			TxRoot:     h32(blk.Header.TxRoot),
			StateRoot:  h32(blk.Header.StateRoot),
			ValSetHash: h32(blk.Header.ValSetHash),
			Round:      blk.Header.Round,
		},
	}
	for _, tx := range blk.Transactions {
		fee := "0"
		if tx.Fee != nil {
			fee = tx.Fee.String()
		}
		w.Transactions = append(w.Transactions, wireTx{
			Type:      uint8(tx.Type),
			From:      haddr(tx.From),
			Nonce:     tx.Nonce,
			Fee:       fee,
			Payload:   hex.EncodeToString(tx.Payload),
			Signature: hex.EncodeToString(tx.Signature),
		})
	}
	for _, c := range blk.Commit {
		w.Commit = append(w.Commit, hex.EncodeToString(c))
	}
	for _, a := range blk.GenesisAllocs {
		bal := "0"
		if a.Balance != nil {
			bal = a.Balance.String()
		}
		w.GenesisAllocs = append(w.GenesisAllocs, wireAlloc{Address: haddr(a.Address), Balance: bal})
	}
	return w
}

func wireToBlock(w *wireBlock) (*Block, error) {
	blk := &Block{
		Header: BlockHeader{
			Height:     w.Header.Height,
			PrevHash:   hexTo32(w.Header.PrevHash),
			Timestamp:  w.Header.Timestamp,
			Proposer:     hexToAddr(w.Header.Proposer),
			FeeCollector: hexToAddr(w.Header.FeeCollector),
			TxRoot:     hexTo32(w.Header.TxRoot),
			StateRoot:  hexTo32(w.Header.StateRoot),
			ValSetHash: hexTo32(w.Header.ValSetHash),
			Round:      w.Header.Round,
		},
	}
	for _, wt := range w.Transactions {
		fee, ok := new(big.Int).SetString(wt.Fee, 10)
		if !ok {
			return nil, errors.New("chain: ungültige Fee in wireTx")
		}
		payload, err := hex.DecodeString(wt.Payload)
		if err != nil {
			return nil, err
		}
		sig, err := hex.DecodeString(wt.Signature)
		if err != nil {
			return nil, err
		}
		blk.Transactions = append(blk.Transactions, &Transaction{
			Type:      TxType(wt.Type),
			From:      hexToAddr(wt.From),
			Nonce:     wt.Nonce,
			Fee:       fee,
			Payload:   payload,
			Signature: sig,
		})
	}
	for _, c := range w.Commit {
		b, err := hex.DecodeString(c)
		if err != nil {
			return nil, err
		}
		blk.Commit = append(blk.Commit, b)
	}
	for _, wa := range w.GenesisAllocs {
		bal, ok := new(big.Int).SetString(wa.Balance, 10)
		if !ok {
			return nil, errors.New("chain: ungültige Balance in wireAlloc")
		}
		blk.GenesisAllocs = append(blk.GenesisAllocs, GenesisAccount{Address: hexToAddr(wa.Address), Balance: bal})
	}
	return blk, nil
}

// TxToWireJSON serialisiert eine einzelne Transaktion als Wire-JSON (für die
// Tx-Propagation über GossipSub).
func TxToWireJSON(tx *Transaction) ([]byte, error) {
	fee := "0"
	if tx.Fee != nil {
		fee = tx.Fee.String()
	}
	w := wireTx{
		Type:      uint8(tx.Type),
		From:      haddr(tx.From),
		Nonce:     tx.Nonce,
		Fee:       fee,
		Payload:   hex.EncodeToString(tx.Payload),
		Signature: hex.EncodeToString(tx.Signature),
	}
	return json.Marshal(w)
}

// TxFromWireJSON rekonstruiert eine Transaktion aus Wire-JSON.
func TxFromWireJSON(data []byte) (*Transaction, error) {
	var wt wireTx
	if err := json.Unmarshal(data, &wt); err != nil {
		return nil, err
	}
	fee, ok := new(big.Int).SetString(wt.Fee, 10)
	if !ok {
		return nil, errors.New("chain: ungültige Fee in wireTx")
	}
	payload, err := hex.DecodeString(wt.Payload)
	if err != nil {
		return nil, err
	}
	sig, err := hex.DecodeString(wt.Signature)
	if err != nil {
		return nil, err
	}
	return &Transaction{
		Type:      TxType(wt.Type),
		From:      hexToAddr(wt.From),
		Nonce:     wt.Nonce,
		Fee:       fee,
		Payload:   payload,
		Signature: sig,
	}, nil
}
