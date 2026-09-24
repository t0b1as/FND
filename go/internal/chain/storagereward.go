package chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// =============================================================================
//  Storage-Reward (Phase 3) — Mint von FND gegen Quittungen
// =============================================================================
//
// Der Block-Produzent sammelt vom Konsumenten signierte Quittungen (Receipts)
// für erbrachte Storage-/Transfer-Leistung und fügt sie als TxStorageReward in
// einen Block. Beim Anwenden ERZEUGT die Chain neue FND (Inflation, bewusst) und
// schreibt sie dem jeweiligen PROVIDER gut — proportional: 1 FND pro TB.
//
// Sicherheit:
//   - Jede Quittung wird voll verifiziert (Konsument-Signatur, Provider≠Konsument).
//   - Anti-Replay: bereits eingelöste ReceiptIDs (State.redeemedReceipts) werden
//     abgelehnt — dieselbe Quittung kann nicht zweimal minten.
//   - Kein festes Limit pro Block (User-Entscheidung): die Mint-Summe ergibt sich
//     rein proportional aus den eingelösten Bytes.
//   - Der Reward geht an Receipt.Provider (wer hostet/sendet), nicht an den
//     Einreicher — ein Angreifer kann sich also nicht fremde Quittungen gutschreiben.

// RewardRateNumerator / Denominator definieren die Vergütung: 1 FND pro TB.
// 1 TB = 1e12 Bytes; 1 FND = 1e9 uFND. Also uFND = bytes * 1e9 / 1e12 = bytes/1000.
const bytesPerMicroFNDReward = 1000 // 1000 Bytes ergeben 1 uFND (= 1 FND/TB)

// StorageRewardPayload trägt die einzulösenden Quittungen.
type StorageRewardPayload struct {
	Receipts []Receipt `json:"receipts"`
}

// Encode serialisiert die Payload (exportiert, da der filestore-Node sie baut).
func (p *StorageRewardPayload) Encode() ([]byte, error) {
	return json.Marshal(p)
}

func decodeStorageRewardPayload(b []byte) (*StorageRewardPayload, error) {
	var p StorageRewardPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("chain: ungültige Storage-Reward-Payload: %w", err)
	}
	if len(p.Receipts) == 0 {
		return nil, errors.New("chain: Storage-Reward ohne Quittungen")
	}
	return &p, nil
}

// rewardForBytes berechnet die Transfer-/Store-Vergütung in uFND für eine
// Byte-Menge (1 FND/TB, einmalig pro Transfer).
func rewardForBytes(bytes uint64) *big.Int {
	return new(big.Int).SetUint64(bytes / bytesPerMicroFNDReward)
}

// RewardForBytes ist die exportierte Variante von rewardForBytes: Transfer-/
// Store-Vergütung in uFND für eine Byte-Menge (1 FND/TB). Der Node nutzt sie, um
// den offenen (noch nicht eingelösten) FND-Gegenwert der Quittungen anzuzeigen.
func RewardForBytes(bytes uint64) *big.Int {
	return rewardForBytes(bytes)
}

// secondsPerRewardMonth ist die Bezugsdauer für die Vorhaltungs-Vergütung.
// 30 Tage = 2 592 000 Sekunden. „1 FND / TB·Monat" meint: einen ganzen Monat
// ein TB vorhalten ergibt 1 FND.
const secondsPerRewardMonth = 30 * 24 * 60 * 60 // 2_592_000

// rewardForHosting berechnet die zeitbasierte Vorhaltungs-Vergütung in uFND:
//
//	uFND = bytes × dauer_sekunden / (TB × Monat_sekunden) × 1e9
//	     = bytes × dauer / (1e12 × 2 592 000) × 1e9
//	     = bytes × dauer / (1000 × 2 592 000)
//
// Also dieselbe Byte→uFND-Basis wie beim Transfer (÷1000), zusätzlich anteilig
// nach gehaltener Zeit. Ein TB (1e12 B) für einen vollen Monat ergibt exakt
// 1e9 uFND = 1 FND. Gerechnet wird mit big.Int, um bei großen Byte×Zeit-Werten
// keinen Überlauf zu riskieren.
func rewardForHosting(bytes, durationSeconds uint64) *big.Int {
	if durationSeconds == 0 || bytes == 0 {
		return big.NewInt(0)
	}
	// uFND = bytes × dauer / (bytesPerMicroFNDReward × secondsPerRewardMonth)
	num := new(big.Int).SetUint64(bytes)
	num.Mul(num, new(big.Int).SetUint64(durationSeconds))
	den := new(big.Int).SetUint64(uint64(bytesPerMicroFNDReward) * uint64(secondsPerRewardMonth))
	return num.Div(num, den)
}

// applyStorageReward wendet einen TxStorageReward an: verifiziert jede Quittung,
// prüft Anti-Replay, mintet FND an die Provider und markiert die Quittungen als
// eingelöst. Diese Tx ist gebührenfrei (Fee muss 0 sein) und braucht keine
// gültige Sender-Signatur im klassischen Sinn — die Autorität kommt aus den
// signierten Quittungen selbst, nicht vom Einreicher.
func (s *State) applyStorageReward(tx *Transaction) error {
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: Storage-Reward muss gebührenfrei sein")
	}
	p, err := decodeStorageRewardPayload(tx.Payload)
	if err != nil {
		return err
	}

	// Nonce des Einreichers (Block-Produzent) prüfen — verhindert, dass derselbe
	// Reward-Tx wiederholt eingereicht wird (zusätzlich zum ReceiptID-Replay-Schutz).
	submitter := s.getOrCreate(tx.From)
	if submitter.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", submitter.Nonce, tx.Nonce)
	}

	// Erst ALLES validieren (atomar: entweder alle gültig oder Tx scheitert),
	// dann anwenden. So kann eine einzelne ungültige Quittung nicht einen
	// Teil-Mint hinterlassen.
	type credit struct {
		provider Address
		amount   *big.Int
	}
	credits := make([]credit, 0, len(p.Receipts))
	seen := make(map[[32]byte]bool, len(p.Receipts))

	for i := range p.Receipts {
		r := p.Receipts[i]
		if err := r.VerifyReceipt(); err != nil {
			return fmt.Errorf("chain: Quittung %d ungültig: %w", i, err)
		}
		id := r.ReceiptID()
		// Doppelte Quittung im selben Tx?
		if seen[id] {
			return fmt.Errorf("chain: Quittung %d doppelt im selben Reward", i)
		}
		seen[id] = true
		// Bereits in einem früheren Block eingelöst?
		if s.redeemedReceipts[id] {
			return fmt.Errorf("chain: Quittung %d bereits eingelöst (Replay)", i)
		}
		// Vergütung je nach Quittungstyp:
		//  - Fetch/Store: einmalig pro Transfer (1 FND/TB übertragen).
		//  - Hosting:     zeitbasiert (1 FND/TB·Monat) über die bezeugte Dauer.
		var amount *big.Int
		if r.Kind == ReceiptHosting {
			amount = rewardForHosting(r.Bytes, r.DurationSeconds)
		} else {
			amount = rewardForBytes(r.Bytes)
		}
		if amount.Sign() <= 0 {
			// Zu wenige Bytes bzw. zu kurze Dauer für ≥1 uFND — ablehnen, damit
			// der Produzent solche Quittungen nicht bündelt.
			return fmt.Errorf("chain: Quittung %d ergibt 0 uFND (zu klein)", i)
		}
		credits = append(credits, credit{provider: r.Provider, amount: amount})
	}

	// Anwenden: minten + als eingelöst markieren.
	for i := range p.Receipts {
		s.redeemedReceipts[p.Receipts[i].ReceiptID()] = true
	}
	for _, c := range credits {
		s.Credit(c.provider, c.amount)
	}
	// Nonce des Einreichers erhöhen (Replay-Schutz auf Tx-Ebene).
	submitter.Nonce++
	return nil
}

// IsReceiptRedeemed erlaubt dem Node zu prüfen, ob eine Quittung schon eingelöst
// wurde (z.B. um sie aus dem lokalen Pending-Store zu entfernen).
func (s *State) IsReceiptRedeemed(id [32]byte) bool {
	return s.redeemedReceipts[id]
}
