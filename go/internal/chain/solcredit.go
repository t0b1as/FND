package chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// =============================================================================
//  SOL→FND-Brücke (native Chain)
// =============================================================================
//
// Wenn ein Nutzer SOL an die Empfangsadresse der Brücke zahlt, beobachtet der
// Bridge-Operator (SolWatcher) die Solana-Chain, bestätigt die Zahlung und
// reicht eine TxSolCredit-Transaktion ein, die dem Nutzer FND auf der NATIVEN
// Fundus-Chain gutschreibt.
//
// Sicherheit:
//   - Nur der autorisierte Bridge-Operator (BridgeAuthority) darf SolCredits
//     einreichen. Sonst könnte jeder beliebig FND minten. Die Autorität ist
//     eine feste Adresse (analog Fee-Collector), gegen die tx.From geprüft wird.
//   - Jede Solana-Tx-Signatur kann nur EINMAL eingelöst werden
//     (redeemedSolTxs-Anti-Replay). Verhindert doppelte Gutschrift derselben
//     Einzahlung.
//   - Der Mint ist damit ein nachvollziehbarer On-Chain-Vorgang (statt einer
//     „magischen" Gutschrift außerhalb der Chain).

// SolCreditPayload ist die Nutzlast einer TxSolCredit-Transaktion.
type SolCreditPayload struct {
	Recipient Address `json:"recipient"`  // Empfänger der FND-Gutschrift
	UFND      uint64  `json:"ufnd"`       // Betrag in uFND (1 FND = 1e9 uFND)
	SolTxSig  string  `json:"sol_tx_sig"` // Solana-Tx-Signatur (Anti-Replay-Schlüssel)
}

func (p *SolCreditPayload) Encode() ([]byte, error) { return json.Marshal(p) }

func decodeSolCreditPayload(raw []byte) (*SolCreditPayload, error) {
	var p SolCreditPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("chain: SolCredit-Payload unlesbar: %w", err)
	}
	return &p, nil
}

// solTxKey leitet den Anti-Replay-Schlüssel aus der Solana-Tx-Signatur ab.
func solTxKey(solTxSig string) [32]byte {
	return chainHash([]byte("fundus-sol-tx:" + solTxSig))
}

// applySolCredit verarbeitet eine TxSolCredit: schreibt dem Empfänger FND gut,
// nachdem der Bridge-Operator eine bestätigte SOL-Einzahlung gemeldet hat.
func (s *State) applySolCredit(tx *Transaction, bridgeAuthority Address) error {
	if tx.Fee != nil && tx.Fee.Sign() != 0 {
		return errors.New("chain: SOL-Credit muss gebührenfrei sein")
	}
	// Nur der autorisierte Bridge-Operator darf minten.
	var zero Address
	if bridgeAuthority == zero {
		return errors.New("chain: keine Bridge-Autorität konfiguriert")
	}
	if tx.From != bridgeAuthority {
		return errors.New("chain: SOL-Credit nur von der Bridge-Autorität erlaubt")
	}

	p, err := decodeSolCreditPayload(tx.Payload)
	if err != nil {
		return err
	}
	if p.UFND == 0 {
		return errors.New("chain: SOL-Credit über 0 uFND")
	}
	if p.SolTxSig == "" {
		return errors.New("chain: SOL-Credit ohne Solana-Tx-Signatur")
	}
	var recZero Address
	if p.Recipient == recZero {
		return errors.New("chain: SOL-Credit an Null-Adresse")
	}

	// Nonce des Bridge-Operators prüfen (Reihenfolge + Replay-Schutz auf Tx-Ebene).
	submitter := s.getOrCreate(tx.From)
	if submitter.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", submitter.Nonce, tx.Nonce)
	}

	// Anti-Replay: diese Solana-Tx darf nicht schon eingelöst sein.
	key := solTxKey(p.SolTxSig)
	if s.redeemedSolTxs[key] {
		return fmt.Errorf("chain: Solana-Tx %s bereits eingelöst", p.SolTxSig)
	}

	// Gutschrift + Buchhaltung.
	s.Credit(p.Recipient, new(big.Int).SetUint64(p.UFND))
	s.redeemedSolTxs[key] = true
	submitter.Nonce++
	return nil
}
