package chain

// HTLC (Hash-Timelock-Contract) für atomare Cross-Chain-Swaps FND↔SOL.
//
// Ablauf eines Swaps (vereinfacht): Alice will FND gegen Bobs SOL tauschen.
//  1. Alice wählt ein Geheimnis S, berechnet H = blake3(S).
//  2. Alice sperrt FND (TxHTLCLock) mit Hashlock H, Empfänger Bob, Timelock T_A.
//  3. Bob sieht H auf der FND-Chain und sperrt seine SOL auf Solana mit demselben
//     H, Empfänger Alice, Timelock T_B < T_A.
//  4. Alice löst die SOL auf Solana ein und enthüllt dabei S.
//  5. Bob liest S von Solana und löst die FND ein (TxHTLCClaim mit S).
// Bricht jemand ab, bekommt nach Ablauf des jeweiligen Timelocks jeder sein
// Geld zurück (TxHTLCRefund). Niemand kann betrügen.

import (
	"errors"
	"math/big"

	"lukechampine.com/blake3"
)

// HTLC-Zustand.
type HTLCState uint8

const (
	HTLCLocked   HTLCState = 1 // FND gesperrt, wartet auf Claim oder Refund
	HTLCClaimed  HTLCState = 2 // per Preimage eingelöst (Empfänger hat die FND)
	HTLCRefunded HTLCState = 3 // nach Timelock zurückgegeben (Sperrer hat die FND)
)

// HTLC beschreibt einen gesperrten Betrag mit Hashlock + Timelock.
type HTLC struct {
	ID        [32]byte  // = Tx-Hash des Lock
	Sender    Address   // wer die FND gesperrt hat (bekommt Refund)
	Recipient Address   // wer mit dem Preimage einlösen darf
	Amount    *big.Int  // gesperrte FND
	Hashlock  [32]byte  // H = blake3(Preimage)
	Timelock  uint64    // Blockhöhe; danach darf Sender refunden
	State     HTLCState
	Preimage  [32]byte // gesetzt nach Claim (enthüllt das Geheimnis on-chain)
}

// HTLCLockPayload — Payload für TxHTLCLock.
type HTLCLockPayload struct {
	Recipient Address
	Amount    *big.Int
	Hashlock  [32]byte
	Timelock  uint64
}

func (p *HTLCLockPayload) encode() []byte {
	buf := make([]byte, 0, 20+16+32+8)
	buf = append(buf, p.Recipient[:]...)
	putUint128(&buf, p.Amount)
	buf = append(buf, p.Hashlock[:]...)
	putUint64(&buf, p.Timelock)
	return buf
}

func decodeHTLCLockPayload(b []byte) (*HTLCLockPayload, error) {
	if len(b) != 20+16+32+8 {
		return nil, errors.New("chain: ungültige HTLCLock-Payload-Länge")
	}
	p := &HTLCLockPayload{Amount: new(big.Int)}
	off := 0
	copy(p.Recipient[:], b[off:off+20])
	off += 20
	p.Amount.SetBytes(b[off : off+16])
	off += 16
	copy(p.Hashlock[:], b[off:off+32])
	off += 32
	p.Timelock = beUint64(b[off : off+8])
	return p, nil
}

// HTLCClaimPayload — Payload für TxHTLCClaim. Enthält die HTLC-ID und das
// Preimage; der Hash des Preimage muss dem Hashlock entsprechen.
type HTLCClaimPayload struct {
	ID       [32]byte
	Preimage [32]byte
}

func (p *HTLCClaimPayload) encode() []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, p.ID[:]...)
	buf = append(buf, p.Preimage[:]...)
	return buf
}

func decodeHTLCClaimPayload(b []byte) (*HTLCClaimPayload, error) {
	if len(b) != 64 {
		return nil, errors.New("chain: ungültige HTLCClaim-Payload-Länge")
	}
	p := &HTLCClaimPayload{}
	copy(p.ID[:], b[0:32])
	copy(p.Preimage[:], b[32:64])
	return p, nil
}

// HTLCRefundPayload — Payload für TxHTLCRefund. Nur die HTLC-ID.
type HTLCRefundPayload struct {
	ID [32]byte
}

func (p *HTLCRefundPayload) encode() []byte {
	return append([]byte(nil), p.ID[:]...)
}

func decodeHTLCRefundPayload(b []byte) (*HTLCRefundPayload, error) {
	if len(b) != 32 {
		return nil, errors.New("chain: ungültige HTLCRefund-Payload-Länge")
	}
	p := &HTLCRefundPayload{}
	copy(p.ID[:], b[0:32])
	return p, nil
}

// VerifyPreimage prüft, ob blake3(preimage) == hashlock.
//
// Wir nutzen blake3 (konsistent mit dem Rest der Chain). Das ist kein Hindernis
// für den Cross-Chain-Swap: Solana bietet blake3 als nativen On-Chain-Syscall
// (solana_program::blake3), und unser eigenes Solana-HTLC-Programm nutzt
// denselben blake3-Hashlock. So ergibt dasselbe Preimage auf BEIDEN Ketten
// denselben Hash — der Swap ist atomar und kompatibel.
func VerifyPreimage(preimage, hashlock [32]byte) bool {
	h := blake3.Sum256(preimage[:])
	return h == hashlock
}

// applyHTLCLock: Sender sperrt Amount + zahlt Gebühr (1,8 %). Erzeugt einen HTLC.
func (s *State) applyHTLCLock(tx *Transaction, feeCollector Address, height uint64) error {
	p, err := decodeHTLCLockPayload(tx.Payload)
	if err != nil {
		return err
	}
	if p.Amount.Sign() <= 0 {
		return errors.New("chain: HTLC-Betrag muss > 0 sein")
	}
	if !fitsUint128(p.Amount) || !fitsUint128(tx.Fee) {
		return errors.New("chain: Betrag/Gebühr überschreitet uint128")
	}
	if p.Timelock <= height {
		return errors.New("chain: HTLC-Timelock muss in der Zukunft liegen")
	}
	want := FeeForValue(p.Amount)
	if tx.Fee == nil || tx.Fee.Cmp(want) != 0 {
		return errors.New("chain: HTLC-Gebühr muss 1,8 % des Betrags sein")
	}
	sender := s.getOrCreate(tx.From)
	if sender.Nonce != tx.Nonce {
		return errors.New("chain: falsche Nonce für HTLCLock")
	}
	total := new(big.Int).Add(p.Amount, tx.Fee)
	if sender.Balance.Cmp(total) < 0 {
		return errors.New("chain: unzureichendes Guthaben für HTLC")
	}
	sender.Balance.Sub(sender.Balance, total)
	sender.Nonce++
	fc := s.getOrCreate(feeCollector)
	fc.Balance.Add(fc.Balance, tx.Fee)

	id := tx.Hash()
	s.htlcs[id] = &HTLC{
		ID: id, Sender: tx.From, Recipient: p.Recipient,
		Amount: new(big.Int).Set(p.Amount), Hashlock: p.Hashlock,
		Timelock: p.Timelock, State: HTLCLocked,
	}
	return nil
}

// applyHTLCClaim: Empfänger löst mit dem Preimage ein. Prüft blake3(preimage)==
// hashlock. Bei Erfolg gehen die FND an den Empfänger; das Preimage wird
// on-chain gespeichert (enthüllt das Geheimnis für die Gegenseite).
func (s *State) applyHTLCClaim(tx *Transaction, feeCollector Address) error {
	p, err := decodeHTLCClaimPayload(tx.Payload)
	if err != nil {
		return err
	}
	h, ok := s.htlcs[p.ID]
	if !ok {
		return errors.New("chain: HTLC nicht gefunden")
	}
	if h.State != HTLCLocked {
		return errors.New("chain: HTLC nicht mehr offen")
	}
	if tx.From != h.Recipient {
		return errors.New("chain: nur der Empfänger darf einlösen")
	}
	if !VerifyPreimage(p.Preimage, h.Hashlock) {
		return errors.New("chain: falsches Preimage")
	}
	claimant := s.getOrCreate(tx.From)
	if claimant.Nonce != tx.Nonce {
		return errors.New("chain: falsche Nonce für HTLCClaim")
	}
	// Claim ist gebührenfrei (die Lock-Gebühr deckt den Swap); nur Nonce erhöhen.
	claimant.Nonce++
	claimant.Balance.Add(claimant.Balance, h.Amount)
	h.State = HTLCClaimed
	h.Preimage = p.Preimage
	return nil
}

// applyHTLCRefund: nach Ablauf des Timelocks gibt der Sender die FND zurück.
func (s *State) applyHTLCRefund(tx *Transaction, feeCollector Address, height uint64) error {
	p, err := decodeHTLCRefundPayload(tx.Payload)
	if err != nil {
		return err
	}
	h, ok := s.htlcs[p.ID]
	if !ok {
		return errors.New("chain: HTLC nicht gefunden")
	}
	if h.State != HTLCLocked {
		return errors.New("chain: HTLC nicht mehr offen")
	}
	if tx.From != h.Sender {
		return errors.New("chain: nur der Sender darf refunden")
	}
	if height < h.Timelock {
		return errors.New("chain: Timelock noch nicht abgelaufen")
	}
	sender := s.getOrCreate(tx.From)
	if sender.Nonce != tx.Nonce {
		return errors.New("chain: falsche Nonce für HTLCRefund")
	}
	sender.Nonce++
	sender.Balance.Add(sender.Balance, h.Amount)
	h.State = HTLCRefunded
	return nil
}

// GetHTLC liefert eine Kopie eines HTLC (für Abfragen, z.B. Preimage auslesen).
func (s *State) GetHTLC(id [32]byte) (HTLC, bool) {
	h, ok := s.htlcs[id]
	if !ok {
		return HTLC{}, false
	}
	cp := *h
	cp.Amount = new(big.Int).Set(h.Amount)
	return cp, true
}

// FindHTLCByHashlock sucht einen HTLC anhand von Hashlock + Empfänger. Nötig für
// den Swap-Orchestrator: Der Verkäufer kennt die FND-HTLC-ID des Käufers nicht
// vorab, aber den gemeinsamen Hashlock und die eigene Empfangsadresse.
// Gibt die ID und den HTLC zurück.
func (s *State) FindHTLCByHashlock(hashlock [32]byte, recipient Address) ([32]byte, HTLC, bool) {
	for id, h := range s.htlcs {
		if h.Hashlock == hashlock && h.Recipient == recipient {
			cp := *h
			cp.Amount = new(big.Int).Set(h.Amount)
			return id, cp, true
		}
	}
	return [32]byte{}, HTLC{}, false
}

// encode serialisiert einen HTLC deterministisch für den StateRoot.
func (h *HTLC) encode() []byte {
	buf := make([]byte, 0, 32+20+20+16+32+8+1+32)
	buf = append(buf, h.ID[:]...)
	buf = append(buf, h.Sender[:]...)
	buf = append(buf, h.Recipient[:]...)
	putUint128(&buf, h.Amount)
	buf = append(buf, h.Hashlock[:]...)
	putUint64(&buf, h.Timelock)
	buf = append(buf, byte(h.State))
	buf = append(buf, h.Preimage[:]...)
	return buf
}

// ── Exportierte Encode-Wrapper für die API ───────────────────────────────────

// EncodeHTLCLock baut die Payload für eine TxHTLCLock-Transaktion.
func EncodeHTLCLock(recipient Address, amount *big.Int, hashlock [32]byte, timelock uint64) []byte {
	p := &HTLCLockPayload{Recipient: recipient, Amount: amount, Hashlock: hashlock, Timelock: timelock}
	return p.encode()
}

// EncodeHTLCClaim baut die Payload für eine TxHTLCClaim-Transaktion.
func EncodeHTLCClaim(id [32]byte, preimage [32]byte) []byte {
	p := &HTLCClaimPayload{ID: id, Preimage: preimage}
	return p.encode()
}

// EncodeHTLCRefund baut die Payload für eine TxHTLCRefund-Transaktion.
func EncodeHTLCRefund(id [32]byte) []byte {
	p := &HTLCRefundPayload{ID: id}
	return p.encode()
}
