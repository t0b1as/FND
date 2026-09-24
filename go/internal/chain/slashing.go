package chain

// Slashing: Bestrafung von Validator-Fehlverhalten durch Stake-Entzug.
//
// Ohne Slashing ist der Stake nur eine Beitrittshürde, keine Sicherheit — ein
// Validator könnte sich fehlverhalten, ohne etwas zu riskieren. Slashing gibt dem
// Stake Zähne: nachgewiesenes Fehlverhalten kostet echtes Geld.
//
// DIESER SCHRITT deckt den klarsten, objektiv beweisbaren Verstoß ab:
// DOUBLE-SIGNING (Equivocation) — derselbe Validator signiert ZWEI verschiedene
// Blöcke für DIESELBE Höhe. Das ermöglicht Forks und Double-Spends und ist der
// gefährlichste Angriff auf einen PoA/BFT-Konsens.
//
// Der Beweis ist selbst-verifizierend: zwei signierte Header gleicher Höhe mit
// unterschiedlichem Hash, beide vom selben Signierer. JEDER Node kann das ohne
// Vertrauen prüfen — genau das macht Slashing sicher und dezentral tauglich.

import (
	"fmt"
	"math/big"
	"sort"
)

// SlashFractionNum/Den: Anteil des aktiven Stakes, der bei nachgewiesenem
// Double-Signing entzogen wird. 1/1 = 100% (voller Entzug), bei Equivocation
// üblich, weil es ein eindeutiger, vorsätzlicher Angriff ist. Als Bruch gehalten,
// falls später abgestuft werden soll.
var (
	SlashFractionNum = big.NewInt(1)
	SlashFractionDen = big.NewInt(1)
)

// SlasherRewardNum/Den: Anteil des geslashten Betrags, der an den Melder geht
// (Anreiz, Beweise einzureichen). Der Rest wird verbrannt (niemandem gutge-
// schrieben → reduziert den Umlauf). 1/20 = 5%.
var (
	SlasherRewardNum = big.NewInt(1)
	SlasherRewardDen = big.NewInt(20)
)

// SlashEvidence ist der Beweis für Double-Signing: zwei signierte Header gleicher
// Höhe. HeaderA/HeaderB sind die serialisierten Header (Header.bytes()), SigA/SigB
// die zugehörigen Proposer-Signaturen (wie im Commit-Feld).
type SlashEvidence struct {
	Height  uint64
	HeaderA []byte
	SigA    []byte
	HeaderB []byte
	SigB    []byte
}

// SlashPayload — Payload für TxSlash: der Double-Sign-Beweis.
type SlashPayload struct {
	Evidence SlashEvidence
}

// decodeBlockHeader kehrt BlockHeader.bytes() um (feste Feldreihenfolge). Wird
// für die Slash-Evidence gebraucht, um aus den übertragenen Header-Bytes wieder
// einen Header zu gewinnen, dessen Hash und Signierer geprüft werden können.
func decodeBlockHeader(b []byte) (*BlockHeader, error) {
	r := newReader(b)
	h := &BlockHeader{}
	h.Height = r.readUint64()
	h.PrevHash = r.read32()
	h.Timestamp = r.readUint64()
	h.Proposer = r.readAddr()
	h.FeeCollector = r.readAddr()
	h.TxRoot = r.read32()
	h.StateRoot = r.read32()
	h.ValSetHash = r.read32()
	h.Round = r.readUint64()
	if r.err != nil {
		return nil, fmt.Errorf("slash: Header-Dekodierung fehlgeschlagen: %w", r.err)
	}
	return h, nil
}

// blockFromHeaderSig rekonstruiert einen minimalen Block (nur Header + eine
// Commit-Signatur), damit RecoverBlockSigner darauf angewandt werden kann.
func blockFromHeaderSig(headerBytes, sig []byte) (*Block, error) {
	h, err := decodeBlockHeader(headerBytes)
	if err != nil {
		return nil, err
	}
	return &Block{Header: *h, Commit: [][]byte{sig}}, nil
}

// VerifySlashEvidence prüft, ob der Beweis ein echtes Double-Signing belegt:
// beide Header gleiche Höhe, VERSCHIEDENE Hashes, beide gültig signiert vom
// SELBEN Validator. Liefert die Adresse des Übeltäters oder einen Fehler.
func VerifySlashEvidence(ev *SlashEvidence) (Address, error) {
	blkA, err := blockFromHeaderSig(ev.HeaderA, ev.SigA)
	if err != nil {
		return Address{}, fmt.Errorf("slash: Header A ungültig: %w", err)
	}
	blkB, err := blockFromHeaderSig(ev.HeaderB, ev.SigB)
	if err != nil {
		return Address{}, fmt.Errorf("slash: Header B ungültig: %w", err)
	}
	if blkA.Header.Height != ev.Height || blkB.Header.Height != ev.Height {
		return Address{}, fmt.Errorf("slash: Höhen stimmen nicht überein")
	}
	hashA := blkA.Header.Hash()
	hashB := blkB.Header.Hash()
	if hashA == hashB {
		return Address{}, fmt.Errorf("slash: identische Header — kein Double-Signing")
	}
	signerA, err := RecoverBlockSigner(blkA)
	if err != nil {
		return Address{}, fmt.Errorf("slash: Signatur A ungültig: %w", err)
	}
	signerB, err := RecoverBlockSigner(blkB)
	if err != nil {
		return Address{}, fmt.Errorf("slash: Signatur B ungültig: %w", err)
	}
	if signerA != signerB {
		return Address{}, fmt.Errorf("slash: verschiedene Signierer — kein Double-Signing")
	}
	return signerA, nil
}

// applySlash verarbeitet eine TxSlash: verifiziert den Beweis, entzieht dem
// Übeltäter den Stake, belohnt den Melder (den Tx-Absender) und verbrennt den
// Rest. feeCollector/producer erhalten die normale Tx-Gebühr.
//
// KONSENS-KRITISCH: läuft auf jedem Node identisch. Die Verifikation ist rein
// deterministisch (Signaturprüfung), also kommen alle Nodes zum selben Ergebnis.
func (s *State) applySlash(tx *Transaction, feeCollector, producer Address, height uint64) error {
	from := tx.From
	payload, err := decodeSlashPayload(tx.Payload)
	if err != nil {
		return err
	}
	ev := &payload.Evidence

	offender, err := VerifySlashEvidence(ev)
	if err != nil {
		return err
	}

	acc := s.getOrCreate(from)
	if tx.Nonce != acc.Nonce {
		return fmt.Errorf("slash: falsche Nonce (erwartet %d, bekommen %d)", acc.Nonce, tx.Nonce)
	}
	fee := SlashFee()
	// Slash-Meldung ist gebührenfrei (fee=0): Melden soll sich lohnen, und ein
	// gültiger kryptografischer Beweis ist Voraussetzung, also ist Spam ohnehin
	// unmöglich. tx.Fee darf 0 oder nil sein.
	if tx.Fee != nil && tx.Fee.Sign() != 0 && tx.Fee.Cmp(fee) != 0 {
		return fmt.Errorf("slash: Gebühr muss %s uFND sein", fee.String())
	}
	if fee.Sign() > 0 && acc.Balance.Cmp(fee) < 0 {
		return fmt.Errorf("slash: Guthaben deckt die Gebühr nicht")
	}

	// Schutz vor Doppel-Bestrafung: für dieselbe Höhe darf ein Validator nur
	// einmal geslasht werden. Sonst könnte derselbe Beweis den Stake mehrfach
	// entziehen oder mehrfach Belohnung auszahlen.
	if s.isSlashed(offender, ev.Height) {
		return fmt.Errorf("slash: %s wurde für Höhe %d bereits geslasht", offender.Hex(), ev.Height)
	}

	rec := s.getOrCreateStake(offender)
	slashAmount := new(big.Int).Mul(rec.Amount, SlashFractionNum)
	slashAmount.Quo(slashAmount, SlashFractionDen)

	// Gebühr abziehen und verteilen (wie bei anderen Tx).
	acc.Balance.Sub(acc.Balance, fee)
	acc.Nonce++
	s.creditFee(fee, feeCollector, producer)

	if slashAmount.Sign() > 0 {
		rec.Amount.Sub(rec.Amount, slashAmount)

		reward := new(big.Int).Mul(slashAmount, SlasherRewardNum)
		reward.Quo(reward, SlasherRewardDen)
		if reward.Sign() > 0 {
			acc.Balance.Add(acc.Balance, reward)
		}
		// Rest (slashAmount - reward) wird verbrannt: keiner Adresse gutge-
		// schrieben → reduziert den Gesamtumlauf.
	}

	s.markSlashed(offender, ev.Height, height)
	return nil
}

// SlashFee ist die Gebühr für eine Slash-Meldung. Niedrig gehalten — das Melden
// soll sich lohnen, nicht abgeschreckt werden.
func SlashFee() *big.Int {
	return FeeForValue(big.NewInt(0))
}

func (s *State) isSlashed(a Address, evidenceHeight uint64) bool {
	set, ok := s.slashed[a]
	if !ok {
		return false
	}
	_, done := set[evidenceHeight]
	return done
}

func (s *State) markSlashed(a Address, evidenceHeight, appliedHeight uint64) {
	if s.slashed[a] == nil {
		s.slashed[a] = make(map[uint64]uint64)
	}
	s.slashed[a][evidenceHeight] = appliedHeight
}

// slashedRoot bindet den Slashing-Zustand in den State-Root ein (konsens-relevant,
// muss auf allen Nodes gleich sein). Deterministisch: Adressen und Höhen sortiert.
func (s *State) slashedRoot() [32]byte {
	if len(s.slashed) == 0 {
		return [32]byte{}
	}
	addrs := make([]Address, 0, len(s.slashed))
	for a := range s.slashed {
		addrs = append(addrs, a)
	}
	sortAddresses(addrs)
	buf := make([]byte, 0, 64)
	for _, a := range addrs {
		buf = append(buf, a[:]...)
		heights := make([]uint64, 0, len(s.slashed[a]))
		for h := range s.slashed[a] {
			heights = append(heights, h)
		}
		sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
		for _, h := range heights {
			putUint64(&buf, h)
			putUint64(&buf, s.slashed[a][h])
		}
	}
	return chainHash(buf)
}

func encodeSlashPayload(p *SlashPayload) []byte {
	buf := make([]byte, 0, 256)
	ev := &p.Evidence
	putUint64(&buf, ev.Height)
	putBytes(&buf, ev.HeaderA)
	putBytes(&buf, ev.SigA)
	putBytes(&buf, ev.HeaderB)
	putBytes(&buf, ev.SigB)
	return buf
}

func decodeSlashPayload(b []byte) (*SlashPayload, error) {
	r := newReader(b)
	ev := SlashEvidence{}
	ev.Height = r.readUint64()
	ev.HeaderA = r.readBytes()
	ev.SigA = r.readBytes()
	ev.HeaderB = r.readBytes()
	ev.SigB = r.readBytes()
	if r.err != nil {
		return nil, fmt.Errorf("slash: Payload-Dekodierung fehlgeschlagen: %w", r.err)
	}
	return &SlashPayload{Evidence: ev}, nil
}
