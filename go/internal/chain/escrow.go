package chain

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
)

func bytesCompare(a, b []byte) int { return bytes.Compare(a, b) }

// ─── Escrow / Kaufvertrag (Spec §7a, Template 0x30) ──────────────────────────
// Schlanke Variante: open → confirm (Freigabe an Verkäufer) ODER refund nach
// Fristablauf (deadline). Der 2-von-3-Schiedsspruch (dispute/arbiter) folgt als
// eigener Schritt. Nur Geld-/Zustandslogik ist on-chain; Warentexte bleiben
// off-chain und werden per ContentHash verankert.

// EscrowState ist der Lebenszyklus-Zustand eines Escrows.
type EscrowState uint8

const (
	EscrowOpen     EscrowState = 1 // Betrag gesperrt, wartet auf Lieferung/Bestätigung
	EscrowDisputed EscrowState = 2 // Streit eröffnet, Jury stimmt ab
	EscrowClosed   EscrowState = 3 // abgeschlossen (freigegeben/erstattet) → gepruned
	// Storno-/Rücksende-Flow (Spec §7a-ter, Tx 0x35-0x37).
	EscrowCancelRequested EscrowState = 4 // Käufer hat Storno beantragt, Ware muss zurück
	EscrowReturnSubmitted EscrowState = 5 // Käufer hat Rücksende-Tracking hinterlegt
)

// Vote-Werte für juror_vote / dispute-Auflösung.
type Verdict uint8

const (
	VoteNone    Verdict = 0
	VoteRelease Verdict = 1 // an Verkäufer
	VoteRefund  Verdict = 2 // an Käufer
)

// Escrow ist ein gesperrter Kaufvertrag im State (Spec §7a / §7a-bis).
type Escrow struct {
	ID          [32]byte
	Buyer       Address
	Seller      Address
	Amount      *big.Int // gesperrter Betrag in uFND (ohne Gebühr/Prämie)
	Deadline    uint64   // Blockhöhe; danach darf der Käufer refunden
	ContentHash [32]byte
	State       EscrowState

	// Absicherung (optional, Spec §7a-bis).
	Insured  bool
	Premium  *big.Int  // in den Jury-Pool gesperrt (1,8 % des Betrags)
	Jurors   []Address // benannte Juroren (später stake-gewichtet gelost)
	Votes    map[Address]Verdict // abgegebene Juror-Stimmen

	// Storno-/Rücksende-Flow (Spec §7a-ter).
	ReturnDeadline uint64   // Blockhöhe; bis dahin muss der Verkäufer den Rückerhalt bestätigen
	TrackingHash   [32]byte // SHA256 der Rücksende-Trackingnummer (Käufer-Nachweis)
}

// encode serialisiert einen Escrow kanonisch (für den State-Root). Deterministisch:
// feste Reihenfolge, Juroren sortiert, Stimmen in Juroren-Reihenfolge.
func (e *Escrow) encode() []byte {
	buf := make([]byte, 0, 160)
	buf = append(buf, e.ID[:]...)
	buf = append(buf, e.Buyer[:]...)
	buf = append(buf, e.Seller[:]...)
	putUint128(&buf, e.Amount)
	putUint64(&buf, e.Deadline)
	buf = append(buf, e.ContentHash[:]...)
	buf = append(buf, byte(e.State))
	// Absicherung
	if e.Insured {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	putUint128(&buf, e.Premium)
	// Juroren sortiert + ihre Stimmen (deterministisch)
	js := make([]Address, len(e.Jurors))
	copy(js, e.Jurors)
	sortAddresses(js)
	putUint64(&buf, uint64(len(js)))
	for _, j := range js {
		buf = append(buf, j[:]...)
		v := VoteNone
		if e.Votes != nil {
			v = e.Votes[j]
		}
		buf = append(buf, byte(v))
	}
	// Storno-/Rücksende-Flow (deterministisch am Ende angehängt).
	putUint64(&buf, e.ReturnDeadline)
	buf = append(buf, e.TrackingHash[:]...)
	return buf
}

// ── Payloads ─────────────────────────────────────────────────────────────────

// sortAddresses sortiert Adressen aufsteigend (deterministisch).
func sortAddresses(a []Address) {
	sort.Slice(a, func(i, j int) bool { return bytesCompare(a[i][:], a[j][:]) < 0 })
}

// EscrowOpenPayload — Payload für TxEscrowOpen.
// Insured + Jurors sind optional (Absicherung, Spec §7a-bis).
type EscrowOpenPayload struct {
	Seller      Address
	Amount      *big.Int
	Deadline    uint64
	ContentHash [32]byte
	Insured     bool
	Jurors      []Address // benannte Juroren (nur relevant bei Insured)
}

func (p *EscrowOpenPayload) encode() []byte {
	buf := make([]byte, 0, 20+16+8+32+1+8+len(p.Jurors)*20)
	buf = append(buf, p.Seller[:]...)
	putUint128(&buf, p.Amount)
	putUint64(&buf, p.Deadline)
	buf = append(buf, p.ContentHash[:]...)
	if p.Insured {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
	putUint64(&buf, uint64(len(p.Jurors)))
	for _, j := range p.Jurors {
		buf = append(buf, j[:]...)
	}
	return buf
}

func decodeEscrowOpenPayload(b []byte) (*EscrowOpenPayload, error) {
	// Mindestlänge: 20+16+8+32+1+8 = 85 (ohne Juroren).
	if len(b) < 85 {
		return nil, errors.New("chain: ungültige EscrowOpen-Payload-Länge")
	}
	p := &EscrowOpenPayload{Amount: new(big.Int)}
	off := 0
	copy(p.Seller[:], b[off:off+20])
	off += 20
	p.Amount.SetBytes(b[off : off+16])
	off += 16
	p.Deadline = beUint64(b[off : off+8])
	off += 8
	copy(p.ContentHash[:], b[off:off+32])
	off += 32
	p.Insured = b[off] == 1
	off++
	n := beUint64(b[off : off+8])
	off += 8
	if uint64(len(b)-off) != n*20 {
		return nil, errors.New("chain: Juroren-Länge inkonsistent")
	}
	for i := uint64(0); i < n; i++ {
		var a Address
		copy(a[:], b[off:off+20])
		off += 20
		p.Jurors = append(p.Jurors, a)
	}
	return p, nil
}

// EscrowRefPayload — Payload für confirm/refund: referenziert ein Escrow per ID.
type EscrowRefPayload struct {
	EscrowID [32]byte
}

func (p *EscrowRefPayload) encode() []byte {
	out := make([]byte, 32)
	copy(out, p.EscrowID[:])
	return out
}

func decodeEscrowRefPayload(b []byte) (*EscrowRefPayload, error) {
	if len(b) != 32 {
		return nil, errors.New("chain: ungültige Escrow-Ref-Payload-Länge")
	}
	p := &EscrowRefPayload{}
	copy(p.EscrowID[:], b)
	return p, nil
}

// EscrowReturnPayload — Payload für submit_return: Escrow-ID + Tracking-Hash.
type EscrowReturnPayload struct {
	EscrowID     [32]byte
	TrackingHash [32]byte
}

func (p *EscrowReturnPayload) encode() []byte {
	out := make([]byte, 64)
	copy(out[0:32], p.EscrowID[:])
	copy(out[32:64], p.TrackingHash[:])
	return out
}

func decodeEscrowReturnPayload(b []byte) (*EscrowReturnPayload, error) {
	if len(b) != 64 {
		return nil, errors.New("chain: ungültige Escrow-Return-Payload-Länge")
	}
	p := &EscrowReturnPayload{}
	copy(p.EscrowID[:], b[0:32])
	copy(p.TrackingHash[:], b[32:64])
	return p, nil
}

// beUint64 liest 8 Bytes big-endian (Gegenstück zu putUint64).
func beUint64(b []byte) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// ── State-Übergänge ──────────────────────────────────────────────────────────

// applyEscrowOpen: Käufer (tx.From) sperrt Amount + zahlt Gebühr. Bei Insured
// zusätzlich die Prämie (1,8 % des Betrags) in den Jury-Pool. Es entsteht ein
// Escrow im Zustand OPEN. Die Escrow-ID ist der Hash der signierten Open-Tx.
func (s *State) applyEscrowOpen(tx *Transaction, feeCollector Address, height uint64) error {
	p, err := decodeEscrowOpenPayload(tx.Payload)
	if err != nil {
		return err
	}
	if p.Amount.Sign() <= 0 {
		return errors.New("chain: Escrow-Betrag muss > 0 sein")
	}
	if !fitsUint128(p.Amount) || !fitsUint128(tx.Fee) {
		return errors.New("chain: Betrag/Gebühr überschreitet uint128")
	}
	if p.Deadline <= height {
		return errors.New("chain: Escrow-Deadline muss in der Zukunft liegen")
	}
	want := FeeForValue(p.Amount)
	if tx.Fee == nil || tx.Fee.Cmp(want) != 0 {
		return fmt.Errorf("chain: Gebühr muss %s uFND sein (1,8 %%)", want.String())
	}
	// Prämie nur bei Absicherung; dann müssen Juroren benannt sein.
	premium := new(big.Int)
	if p.Insured {
		if len(p.Jurors) == 0 {
			return errors.New("chain: Absicherung ohne benannte Juroren")
		}
		if len(p.Jurors)%2 == 0 {
			return errors.New("chain: Juroren-Anzahl muss ungerade sein (Patt vermeiden)")
		}
		premium = FeeForValue(p.Amount) // 1,8 % Prämie (zusätzlich zur Tx-Gebühr)
	}
	buyer := s.getOrCreate(tx.From)
	if buyer.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", buyer.Nonce, tx.Nonce)
	}
	// Käufer zahlt: Betrag + Tx-Gebühr + (bei Insured) Prämie.
	total := new(big.Int).Add(p.Amount, tx.Fee)
	total.Add(total, premium)
	if buyer.Balance.Cmp(total) < 0 {
		return errors.New("chain: unzureichendes Guthaben für Escrow")
	}
	id := tx.Hash()
	if _, exists := s.escrows[id]; exists {
		return errors.New("chain: Escrow existiert bereits")
	}
	buyer.Balance.Sub(buyer.Balance, total)
	buyer.Nonce++
	fc := s.getOrCreate(feeCollector)
	fc.Balance.Add(fc.Balance, tx.Fee) // Tx-Gebühr an Collector; Prämie bleibt im Escrow-Pool
	e := &Escrow{
		ID:          id,
		Buyer:       tx.From,
		Seller:      p.Seller,
		Amount:      new(big.Int).Set(p.Amount),
		Deadline:    p.Deadline,
		ContentHash: p.ContentHash,
		State:       EscrowOpen,
		Insured:     p.Insured,
		Premium:     premium,
	}
	if p.Insured {
		e.Jurors = append(e.Jurors, p.Jurors...)
		e.Votes = make(map[Address]Verdict)
	}
	s.escrows[id] = e
	return nil
}

// payoutJuryPool verteilt den Jury-Pool (Prämie) gleichmäßig auf die übergebenen
// Empfänger-Adressen (Rest durch Ganzzahl-Division geht an den Fee-Collector).
func (s *State) payoutJuryPool(pool *big.Int, recipients []Address, feeCollector Address) {
	if pool == nil || pool.Sign() == 0 || len(recipients) == 0 {
		if pool != nil && pool.Sign() > 0 {
			s.getOrCreate(feeCollector).Balance.Add(s.getOrCreate(feeCollector).Balance, pool)
		}
		return
	}
	share := new(big.Int).Quo(pool, big.NewInt(int64(len(recipients))))
	distributed := new(big.Int)
	for _, r := range recipients {
		acct := s.getOrCreate(r)
		acct.Balance.Add(acct.Balance, share)
		distributed.Add(distributed, share)
	}
	// Rest (Division) an den Fee-Collector, damit nichts verloren geht.
	rest := new(big.Int).Sub(pool, distributed)
	if rest.Sign() > 0 {
		fc := s.getOrCreate(feeCollector)
		fc.Balance.Add(fc.Balance, rest)
	}
}

// applyEscrowConfirm: NUR der Käufer bestätigt die Lieferung → Betrag an Verkäufer.
// Bei Absicherung wird der Jury-Pool (Bereitschaftsentgelt) an die Juroren verteilt.
func (s *State) applyEscrowConfirm(tx *Transaction, feeCollector Address) error {
	p, err := decodeEscrowRefPayload(tx.Payload)
	if err != nil {
		return err
	}
	e, ok := s.escrows[p.EscrowID]
	if !ok || e.State != EscrowOpen {
		return errors.New("chain: Escrow nicht offen/unbekannt")
	}
	if tx.From != e.Buyer {
		return errors.New("chain: nur der Käufer kann die Lieferung bestätigen")
	}
	buyer := s.getOrCreate(tx.From)
	if buyer.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", buyer.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: confirm trägt keine Gebühr")
	}
	buyer.Nonce++
	seller := s.getOrCreate(e.Seller)
	seller.Balance.Add(seller.Balance, e.Amount)
	// Kein Streit: Jury-Pool als Bereitschaftsentgelt an alle benannten Juroren.
	if e.Insured {
		s.payoutJuryPool(e.Premium, e.Jurors, feeCollector)
	}
	e.State = EscrowClosed
	return nil
}

// returnWindowBlocks ist die Frist (in Blöcken), innerhalb derer der Verkäufer
// nach Hinterlegung des Rücksende-Trackings den Rückerhalt bestätigen muss.
// Danach darf der Käufer den Refund selbst auslösen (Schutz vor Blockade).
const returnWindowBlocks = 20160 // ~7 Tage bei 30s-Blöcken

// applyEscrowCancel: NUR der Käufer, NUR solange offen. Der Betrag bleibt
// gesperrt; der Escrow geht in cancel_requested über (Ware muss zurück).
func (s *State) applyEscrowCancel(tx *Transaction) error {
	p, err := decodeEscrowRefPayload(tx.Payload)
	if err != nil {
		return err
	}
	e, ok := s.escrows[p.EscrowID]
	if !ok || e.State != EscrowOpen {
		return errors.New("chain: Escrow nicht offen/unbekannt")
	}
	if tx.From != e.Buyer {
		return errors.New("chain: nur der Käufer kann stornieren")
	}
	buyer := s.getOrCreate(tx.From)
	if buyer.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", buyer.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: cancel trägt keine Gebühr")
	}
	buyer.Nonce++
	e.State = EscrowCancelRequested
	return nil
}

// applyEscrowSubmitReturn: NUR der Käufer, NUR nach Storno. Hinterlegt den
// Tracking-Hash und startet die Verkäufer-Frist (returnWindowBlocks).
func (s *State) applyEscrowSubmitReturn(tx *Transaction, height uint64) error {
	p, err := decodeEscrowReturnPayload(tx.Payload)
	if err != nil {
		return err
	}
	e, ok := s.escrows[p.EscrowID]
	if !ok || e.State != EscrowCancelRequested {
		return errors.New("chain: Escrow nicht im Storno-Zustand")
	}
	if tx.From != e.Buyer {
		return errors.New("chain: nur der Käufer kann die Rücksendung hinterlegen")
	}
	if p.TrackingHash == ([32]byte{}) {
		return errors.New("chain: Tracking-Hash fehlt")
	}
	buyer := s.getOrCreate(tx.From)
	if buyer.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", buyer.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: submit_return trägt keine Gebühr")
	}
	buyer.Nonce++
	e.TrackingHash = p.TrackingHash
	e.ReturnDeadline = height + returnWindowBlocks
	e.State = EscrowReturnSubmitted
	return nil
}

// applyEscrowConfirmReturn: Der Verkäufer bestätigt den Rückerhalt → Betrag
// zurück an den Käufer. Nach Fristablauf (ReturnDeadline) darf ALTERNATIV der
// Käufer selbst bestätigen, damit ein untätiger Verkäufer ihn nicht blockiert.
func (s *State) applyEscrowConfirmReturn(tx *Transaction, feeCollector Address, height uint64) error {
	p, err := decodeEscrowRefPayload(tx.Payload)
	if err != nil {
		return err
	}
	e, ok := s.escrows[p.EscrowID]
	if !ok || e.State != EscrowReturnSubmitted {
		return errors.New("chain: Escrow nicht im Rücksende-Zustand")
	}
	// Verkäufer darf jederzeit bestätigen; der Käufer erst nach Fristablauf.
	switch tx.From {
	case e.Seller:
		// ok
	case e.Buyer:
		if height < e.ReturnDeadline {
			return errors.New("chain: Käufer-Selbstbestätigung erst nach Fristablauf")
		}
	default:
		return errors.New("chain: nur Verkäufer oder (nach Frist) Käufer")
	}
	actor := s.getOrCreate(tx.From)
	if actor.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", actor.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: confirm_return trägt keine Gebühr")
	}
	actor.Nonce++
	// Betrag zurück an den Käufer.
	buyer := s.getOrCreate(e.Buyer)
	buyer.Balance.Add(buyer.Balance, e.Amount)
	// Jury-Pool (falls insured) als Bereitschaftsentgelt auszahlen.
	if e.Insured {
		s.payoutJuryPool(e.Premium, e.Jurors, feeCollector)
	}
	e.State = EscrowClosed
	return nil
}
// Bei Absicherung wird der Jury-Pool an die Juroren verteilt (Bereitschaftsentgelt).
func (s *State) applyEscrowRefund(tx *Transaction, feeCollector Address, height uint64) error {
	p, err := decodeEscrowRefPayload(tx.Payload)
	if err != nil {
		return err
	}
	e, ok := s.escrows[p.EscrowID]
	if !ok || e.State != EscrowOpen {
		return errors.New("chain: Escrow nicht offen/unbekannt")
	}
	if tx.From != e.Buyer {
		return errors.New("chain: nur der Käufer kann den Refund auslösen")
	}
	if height < e.Deadline {
		return fmt.Errorf("chain: Refund erst ab Blockhöhe %d möglich (jetzt %d)", e.Deadline, height)
	}
	buyer := s.getOrCreate(tx.From)
	if buyer.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", buyer.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: refund trägt keine Gebühr")
	}
	buyer.Nonce++
	buyer.Balance.Add(buyer.Balance, e.Amount)
	if e.Insured {
		s.payoutJuryPool(e.Premium, e.Jurors, feeCollector)
	}
	e.State = EscrowClosed
	return nil
}

// applyEscrowDispute: Käufer ODER Verkäufer eröffnet einen Streit. Nur bei
// abgesichertem, offenem Escrow. Setzt den Zustand auf DISPUTED → Jury stimmt ab.
func (s *State) applyEscrowDispute(tx *Transaction) error {
	p, err := decodeEscrowRefPayload(tx.Payload)
	if err != nil {
		return err
	}
	e, ok := s.escrows[p.EscrowID]
	if !ok || e.State != EscrowOpen {
		return errors.New("chain: Escrow nicht offen/unbekannt")
	}
	if !e.Insured {
		return errors.New("chain: nur abgesicherte Escrows können in den Streit gehen")
	}
	if tx.From != e.Buyer && tx.From != e.Seller {
		return errors.New("chain: nur Käufer oder Verkäufer kann Streit eröffnen")
	}
	party := s.getOrCreate(tx.From)
	if party.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", party.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: dispute trägt keine Gebühr")
	}
	party.Nonce++
	e.State = EscrowDisputed
	return nil
}

// applyJurorVote: ein benannter Juror stimmt für release (Verkäufer) oder refund
// (Käufer). Sobald eine absolute Mehrheit erreicht ist, wird aufgelöst: Betrag an
// die Gewinnerseite, Pool an die Juroren der Mehrheit; die Prämie wird also
// wirtschaftlich von der Verliererseite getragen.
func (s *State) applyJurorVote(tx *Transaction, feeCollector Address) error {
	p, err := decodeJurorVotePayload(tx.Payload)
	if err != nil {
		return err
	}
	e, ok := s.escrows[p.EscrowID]
	if !ok || e.State != EscrowDisputed {
		return errors.New("chain: Escrow nicht im Streit/unbekannt")
	}
	if !isJuror(e.Jurors, tx.From) {
		return errors.New("chain: Absender ist kein benannter Juror")
	}
	if _, voted := e.Votes[tx.From]; voted {
		return errors.New("chain: Juror hat bereits gestimmt")
	}
	if p.Vote != VoteRelease && p.Vote != VoteRefund {
		return errors.New("chain: ungültige Stimme")
	}
	juror := s.getOrCreate(tx.From)
	if juror.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", juror.Nonce, tx.Nonce)
	}
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: juror_vote trägt keine Gebühr")
	}
	juror.Nonce++
	e.Votes[tx.From] = p.Vote

	// Mehrheit prüfen (absolute Mehrheit der benannten Juroren).
	majority := len(e.Jurors)/2 + 1
	var nRelease, nRefund int
	for _, v := range e.Votes {
		switch v {
		case VoteRelease:
			nRelease++
		case VoteRefund:
			nRefund++
		}
	}
	var winner Verdict
	if nRelease >= majority {
		winner = VoteRelease
	} else if nRefund >= majority {
		winner = VoteRefund
	} else {
		return nil // noch keine Mehrheit, weiter sammeln
	}

	// Auflösen: Betrag an Gewinnerseite.
	if winner == VoteRelease {
		s.getOrCreate(e.Seller).Balance.Add(s.getOrCreate(e.Seller).Balance, e.Amount)
	} else {
		s.getOrCreate(e.Buyer).Balance.Add(s.getOrCreate(e.Buyer).Balance, e.Amount)
	}
	// Pool an die Juroren, die mit der Mehrheit stimmten.
	var mehrheitsJuroren []Address
	for j, v := range e.Votes {
		if v == winner {
			mehrheitsJuroren = append(mehrheitsJuroren, j)
		}
	}
	s.payoutJuryPool(e.Premium, mehrheitsJuroren, feeCollector)
	e.State = EscrowClosed
	return nil
}

func isJuror(jurors []Address, a Address) bool {
	for _, j := range jurors {
		if j == a {
			return true
		}
	}
	return false
}

// JurorVotePayload — Payload für TxJurorVote: Escrow-ID + Stimme.
type JurorVotePayload struct {
	EscrowID [32]byte
	Vote     Verdict
}

func (p *JurorVotePayload) encode() []byte {
	out := make([]byte, 33)
	copy(out, p.EscrowID[:])
	out[32] = byte(p.Vote)
	return out
}

func decodeJurorVotePayload(b []byte) (*JurorVotePayload, error) {
	if len(b) != 33 {
		return nil, errors.New("chain: ungültige JurorVote-Payload-Länge")
	}
	p := &JurorVotePayload{Vote: Verdict(b[32])}
	copy(p.EscrowID[:], b[:32])
	return p, nil
}

// HasEscrowForContent prüft, ob es einen Escrow für einen bestimmten Content-Hash
// gibt. Ein existierender Escrow (in jedem Zustand) bedeutet, dass für dieses
// Listing ein Kaufprozess läuft oder lief — also verkauft/reserviert. So kann
// jeder Node den Verkauft-Status aus der Chain ableiten, unabhängig davon, wer
// den Kauf ausgelöst hat.
func (s *State) HasEscrowForContent(contentHash [32]byte) bool {
	for _, e := range s.escrows {
		if e.ContentHash == contentHash {
			return true
		}
	}
	return false
}

// GetEscrow liefert eine Kopie eines Escrows (für Abfragen).
func (s *State) GetEscrow(id [32]byte) (Escrow, bool) {
	e, ok := s.escrows[id]
	if !ok {
		return Escrow{}, false
	}
	cp := Escrow{
		ID: e.ID, Buyer: e.Buyer, Seller: e.Seller,
		Amount: new(big.Int).Set(e.Amount), Deadline: e.Deadline,
		ContentHash: e.ContentHash, State: e.State,
		Insured: e.Insured,
	}
	if e.Premium != nil {
		cp.Premium = new(big.Int).Set(e.Premium)
	}
	if len(e.Jurors) > 0 {
		cp.Jurors = append([]Address(nil), e.Jurors...)
	}
	if e.Votes != nil {
		cp.Votes = make(map[Address]Verdict, len(e.Votes))
		for k, v := range e.Votes {
			cp.Votes[k] = v
		}
	}
	return cp, true
}


// ── Exportierte Encode-Wrapper (für Tx-Bau außerhalb des chain-Pakets, z.B. API) ──

// Encode gibt die kanonische Byte-Serialisierung der Escrow-Eröffnung zurück.
func (p *EscrowOpenPayload) Encode() []byte { return p.encode() }

// Encode gibt die kanonische Byte-Serialisierung einer Escrow-ID-Referenz zurück
// (confirm/refund/cancel/confirm-return).
func (p *EscrowRefPayload) Encode() []byte { return p.encode() }

// Encode gibt die kanonische Byte-Serialisierung der Rücksende-Hinterlegung zurück.
func (p *EscrowReturnPayload) Encode() []byte { return p.encode() }
