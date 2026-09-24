package chain

// Stake (FND-020 bis FND-022) — Spec §5.
//
// TxStake (0x02) und TxUnstake (0x03) waren bisher nur Konstanten: kein Zweig im
// applyTx-Switch, keine Payload, keine Ableitung des Validator-Sets. Dieses File
// füllt die Lücke.
//
// Modell — drei Zustände, nicht zwei:
//
//	Guthaben  ──TxStake──▶  gestakt  ──TxUnstake──▶  freiwerdend  ──Reifung──▶  Guthaben
//
// Der Zwischenzustand "freiwerdend" (unbonding) ist nicht Bequemlichkeit,
// sondern Voraussetzung für Slashing: ohne Sperrfrist entzieht sich ein
// Validator jeder Strafe, indem er unmittelbar nach dem Fehlverhalten
// unstaked. Gestraft werden kann nur, was noch gebunden ist. Freiwerdender
// Stake zählt NICHT mehr für das Validator-Set, bleibt aber bis zur Reifung
// angreifbar — das ist der Sinn der Frist.
//
// Die Reifung passiert nicht als Transaktion, sondern zu Beginn jedes Blocks
// (MatureUnbonding). Damit hängt sie an der Höhe, nicht an der Aktivität des
// Betroffenen — sonst könnte jemand seinen Stake beliebig lange in der Schwebe
// halten und die Set-Größe verschleiern.

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
)

// UnbondingPeriod ist die Sperrfrist in Blöcken zwischen TxUnstake und der
// Rückgabe ins Guthaben. Bei BlockTime 5s entsprechen 720 Blöcke etwa einer
// Stunde. Bewusst deutlich länger als das Beweisfenster für Slashing:
// Fehlverhalten muss entdeckt und bestraft werden können, bevor das Geld
// wieder frei ist.
const UnbondingPeriod uint64 = 720

// MinValidatorStake ist die Untergrenze, ab der eine Adresse ins Validator-Set
// aufgenommen wird. 10 FND entspricht der Bootstrap-Phase aus der Roadmap
// (Ziel: 100 FND, sobald >100 Provider aktiv sind). Ändern heißt: Validator-Set
// ändert sich → konsens-relevant, nur mit Chain-Reset oder erhöhter
// StateSchemaVersion.
var MinValidatorStake = new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))

// StakeFee bestimmt die Gebühr für Stake-/Unstake-Transaktionen.
//
// OFFEN — bewusst als eigene Funktion und nicht als direkter FeeForValue-Aufruf:
// Aktuell gilt dieselbe 1,8-%-Regel wie beim Transfer. Ob das richtig ist, ist
// eine Produktentscheidung: 1,8 % auf den Stake sind eine spürbare Hürde für
// den Beitritt, und anders als beim Transfer wechselt kein Wert den Besitzer —
// das Geld bleibt beim Absender, es wird nur gesperrt. Eine feste Grundgebühr
// wäre die naheliegende Alternative. Der Wechsel ist eine Zeile hier; die Tests
// prüfen gegen StakeFee, nicht gegen 1,8 %, und bleiben deshalb gültig.
func StakeFee(amount *big.Int) *big.Int { return FeeForValue(amount) }

// StakeRecord ist der gebundene Anteil einer Adresse.
type StakeRecord struct {
	Amount       *big.Int // aktiv gestakt — zählt fürs Validator-Set
	Unbonding    *big.Int // gekündigt, noch gesperrt — zählt NICHT mehr
	UnlockHeight uint64   // Höhe, ab der Unbonding ins Guthaben zurückfällt
}

// encode serialisiert kanonisch: amount(16) || unbonding(16) || unlockHeight(8).
func (r *StakeRecord) encode() []byte {
	buf := make([]byte, 0, 40)
	putUint128(&buf, r.Amount)
	putUint128(&buf, r.Unbonding)
	putUint64(&buf, r.UnlockHeight)
	return buf
}

// StakePayload ist die Nutzlast von TxStake und TxUnstake: nur ein Betrag.
// Das Ziel ist immer der Absender — fremden Stake zu binden ergibt keinen Sinn
// und wäre eine Angriffsfläche (jemandem ungefragt Slashing-Risiko aufdrücken).
type StakePayload struct {
	Amount *big.Int
}

func encodeStakePayload(p *StakePayload) []byte {
	buf := make([]byte, 0, 16)
	putUint128(&buf, p.Amount)
	return buf
}

func decodeStakePayload(b []byte) (*StakePayload, error) {
	if len(b) != 16 {
		return nil, fmt.Errorf("chain: ungültige Stake-Payload-Länge (%d, erwartet 16)", len(b))
	}
	return &StakePayload{Amount: new(big.Int).SetBytes(b)}, nil
}

// getOrCreateStake liefert den lebenden StakeRecord-Pointer.
func (s *State) getOrCreateStake(a Address) *StakeRecord {
	r, ok := s.stakes[a]
	if !ok {
		r = &StakeRecord{Amount: new(big.Int), Unbonding: new(big.Int)}
		s.stakes[a] = r
	}
	return r
}

// Stake liefert den aktiv gestakten Betrag einer Adresse (Kopie).
func (s *State) Stake(a Address) *big.Int {
	if r, ok := s.stakes[a]; ok {
		return new(big.Int).Set(r.Amount)
	}
	return new(big.Int)
}

// Unbonding liefert den freiwerdenden Betrag und die Freigabehöhe.
func (s *State) Unbonding(a Address) (*big.Int, uint64) {
	if r, ok := s.stakes[a]; ok {
		return new(big.Int).Set(r.Unbonding), r.UnlockHeight
	}
	return new(big.Int), 0
}

// applyStake bindet Guthaben als Stake. Gebühr wird wie beim Transfer
// aufgeteilt (Node-Anteil an den Produzenten, Rest an den Fee-Collector).
func (s *State) applyStake(tx *Transaction, feeCollector, producer Address) error {
	p, err := decodeStakePayload(tx.Payload)
	if err != nil {
		return err
	}
	if p.Amount.Sign() <= 0 {
		return errors.New("chain: Stake-Betrag muss > 0 sein")
	}
	if !fitsUint128(p.Amount) || !fitsUint128(tx.Fee) {
		return errors.New("chain: Betrag/Gebühr überschreitet uint128")
	}
	want := StakeFee(p.Amount)
	if tx.Fee == nil || tx.Fee.Cmp(want) != 0 {
		return fmt.Errorf("chain: Gebühr muss %s uFND sein", want.String())
	}
	sender := s.getOrCreate(tx.From)
	if sender.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", sender.Nonce, tx.Nonce)
	}
	total := new(big.Int).Add(p.Amount, tx.Fee)
	if sender.Balance.Cmp(total) < 0 {
		return errors.New("chain: unzureichendes Guthaben")
	}

	rec := s.getOrCreateStake(tx.From)
	if !fitsUint128(new(big.Int).Add(rec.Amount, p.Amount)) {
		return errors.New("chain: Stake-Summe überschreitet uint128")
	}

	sender.Balance.Sub(sender.Balance, total)
	sender.Nonce++
	rec.Amount.Add(rec.Amount, p.Amount)
	s.creditFee(tx.Fee, feeCollector, producer)
	return nil
}

// applyUnstake überführt gestakten Betrag in den freiwerdenden Topf. Das Geld
// ist damit sofort aus dem Validator-Set heraus, aber erst nach der Sperrfrist
// wieder verfügbar.
//
// Eine zweite Kündigung vor Ablauf der ersten setzt die Frist für den GESAMTEN
// freiwerdenden Betrag neu. Sonst ließe sich die Frist umgehen, indem man kurz
// vor Ablauf minimal nachkündigt und den alten Topf mitzieht.
func (s *State) applyUnstake(tx *Transaction, feeCollector, producer Address, height uint64) error {
	p, err := decodeStakePayload(tx.Payload)
	if err != nil {
		return err
	}
	if p.Amount.Sign() <= 0 {
		return errors.New("chain: Unstake-Betrag muss > 0 sein")
	}
	if !fitsUint128(p.Amount) || !fitsUint128(tx.Fee) {
		return errors.New("chain: Betrag/Gebühr überschreitet uint128")
	}
	want := StakeFee(p.Amount)
	if tx.Fee == nil || tx.Fee.Cmp(want) != 0 {
		return fmt.Errorf("chain: Gebühr muss %s uFND sein", want.String())
	}
	sender := s.getOrCreate(tx.From)
	if sender.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", sender.Nonce, tx.Nonce)
	}
	// Die Gebühr kommt aus dem Guthaben, nicht aus dem Stake — sonst könnte
	// man sich über wiederholtes Unstaken am eigenen gesperrten Geld bedienen.
	if sender.Balance.Cmp(tx.Fee) < 0 {
		return errors.New("chain: unzureichendes Guthaben für die Gebühr")
	}
	rec, ok := s.stakes[tx.From]
	if !ok || rec.Amount.Cmp(p.Amount) < 0 {
		return errors.New("chain: Unstake über dem gestakten Betrag")
	}

	sender.Balance.Sub(sender.Balance, tx.Fee)
	sender.Nonce++
	rec.Amount.Sub(rec.Amount, p.Amount)
	rec.Unbonding.Add(rec.Unbonding, p.Amount)
	rec.UnlockHeight = height + UnbondingPeriod
	s.creditFee(tx.Fee, feeCollector, producer)
	return nil
}

// creditFee verbucht eine Gebühr nach derselben Regel wie applyTransfer.
func (s *State) creditFee(fee *big.Int, feeCollector, producer Address) {
	nodeShare, collectorShare := SplitFee(fee)
	var zero Address
	if producer != zero && producer != feeCollector {
		s.getOrCreate(producer).Balance.Add(s.getOrCreate(producer).Balance, nodeShare)
		s.getOrCreate(feeCollector).Balance.Add(s.getOrCreate(feeCollector).Balance, collectorShare)
		return
	}
	fc := s.getOrCreate(feeCollector)
	fc.Balance.Add(fc.Balance, fee)
}

// MatureUnbonding gibt allen freiwerdenden Stake frei, dessen Sperrfrist mit
// dieser Höhe abgelaufen ist. Zu Beginn jedes Blocks aufzurufen, VOR den
// Transaktionen — so kann eine Tx im selben Block bereits über das frei
// gewordene Guthaben verfügen.
//
// Deterministisch trotz Map: die Adressen werden sortiert abgearbeitet. Bei
// reiner Addition wäre die Reihenfolge egal, aber das gilt nur, solange hier
// nichts Reihenfolge-Abhängiges dazukommt — die Sortierung kostet nichts und
// macht die Zusage unabhängig von künftigen Änderungen.
func (s *State) MatureUnbonding(height uint64) {
	ready := make([]Address, 0)
	for a, r := range s.stakes {
		if r.Unbonding.Sign() > 0 && height >= r.UnlockHeight {
			ready = append(ready, a)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		return bytes.Compare(ready[i][:], ready[j][:]) < 0
	})
	for _, a := range ready {
		r := s.stakes[a]
		s.getOrCreate(a).Balance.Add(s.getOrCreate(a).Balance, r.Unbonding)
		r.Unbonding = new(big.Int)
		r.UnlockHeight = 0
	}
}

// StakedValidators liefert alle Adressen mit aktivem Stake >= MinValidatorStake,
// kanonisch sortiert. Grundlage für das aus dem Chain-Zustand abgeleitete
// Validator-Set: jeder Node berechnet daraus unabhängig dieselbe Liste, ohne
// dass irgendwo eine Adressliste konfiguriert sein müsste.
func (s *State) StakedValidators() []Address {
	out := make([]Address, 0, len(s.stakes))
	for a, r := range s.stakes {
		if r.Amount.Cmp(MinValidatorStake) >= 0 {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}

// ValidatorSetFromState baut das Validator-Set aus dem Chain-Zustand.
// Fehler, wenn niemand die Mindestanforderung erfüllt — dieser Fall darf NICHT
// still in ein leeres Set münden, sonst steht die Kette ohne erkennbaren Grund.
func ValidatorSetFromState(s *State) (*ValidatorSet, error) {
	addrs := s.StakedValidators()
	if len(addrs) == 0 {
		return nil, fmt.Errorf("chain: kein Validator erfüllt den Mindest-Stake von %s uFND", MinValidatorStake)
	}
	return NewValidatorSet(addrs)
}

// stakesRoot ist der Merkle-Teilbaum des Stakes. Adressen ohne jeden gebundenen
// Betrag fallen heraus, damit ein leergeräumter Record denselben Root ergibt wie
// eine nie gestakte Adresse — sonst hinge der Root an der Historie statt am
// Zustand.
func (s *State) stakesRoot() [32]byte {
	addrs := make([]Address, 0, len(s.stakes))
	for a, r := range s.stakes {
		if r.Amount.Sign() == 0 && r.Unbonding.Sign() == 0 {
			continue
		}
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return bytes.Compare(addrs[i][:], addrs[j][:]) < 0
	})
	leaves := make([][32]byte, len(addrs))
	for i, a := range addrs {
		key := a
		leaves[i] = hashLeaf(key[:], s.stakes[a].encode())
	}
	return merkleRoot(leaves, "fundus-empty-stakes")
}
