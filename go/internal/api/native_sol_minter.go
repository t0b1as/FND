package api

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
)

// nativeSolMinter implementiert das shop.Minter-Interface, indem es SOL→FND-
// Gutschriften als native TxSolCredit-Transaktionen auf der Fundus-Chain
// einreicht (statt als Gnosis-ERC20-Mint). Der Bridge-Operator signiert mit
// seinem Node-Schlüssel; seine Adresse muss der on-chain gesetzten
// bridgeAuthority entsprechen, sonst weist die Chain die Tx ab.
type nativeSolMinter struct {
	chain   *chain.Blockchain
	mempool *chain.Mempool
	signer  *ecdsa.PrivateKey
	from    chain.Address
	log     *zap.Logger
}

func NewNativeSolMinter(bc *chain.Blockchain, mp *chain.Mempool, signer *ecdsa.PrivateKey, log *zap.Logger) *nativeSolMinter {
	return &nativeSolMinter{
		chain:   bc,
		mempool: mp,
		signer:  signer,
		from:    chain.PubkeyToAddress(&signer.PublicKey),
		log:     log,
	}
}

// MintFND baut eine signierte TxSolCredit und legt sie in den Mempool. Der
// eigentliche Mint erfolgt, sobald der nächste Block produziert wird. solTxSig
// ist der Anti-Replay-Schlüssel (Solana-Einzahlungssignatur).
func (m *nativeSolMinter) MintFND(ctx context.Context, toAddress string, fndAmount float64, solTxSig string) (string, error) {
	if m.chain == nil || m.mempool == nil {
		return "", fmt.Errorf("sol-minter: Chain nicht verfügbar")
	}
	recipient, ok := chain.AddressFromHex(toAddress)
	if !ok {
		return "", fmt.Errorf("sol-minter: ungültige Empfängeradresse %q", toAddress)
	}
	if solTxSig == "" {
		return "", fmt.Errorf("sol-minter: fehlende Solana-Tx-Signatur (Anti-Replay)")
	}

	// FND (float, ganze FND) → uFND (uint64). 1 FND = 1e9 uFND.
	// Kaufmännisch runden, um Float-Ungenauigkeit abzufangen.
	ufndF := new(big.Float).Mul(big.NewFloat(fndAmount), big.NewFloat(float64(chain.UFNDPerFND)))
	ufndInt, _ := ufndF.Int(nil)
	if ufndInt.Sign() <= 0 {
		return "", fmt.Errorf("sol-minter: Betrag ergibt 0 uFND")
	}

	payload := chain.SolCreditPayload{
		Recipient: recipient,
		UFND:      ufndInt.Uint64(),
		SolTxSig:  solTxSig,
	}
	raw, err := payload.Encode()
	if err != nil {
		return "", fmt.Errorf("sol-minter: Payload: %w", err)
	}

	// Aktuelle Nonce der Bridge-Adresse holen.
	_, nonce := m.chain.AccountInfo(m.from)

	// Gebührenfreie TxSolCredit signieren.
	tx, err := chain.BuildSignedTx(m.signer, chain.TxSolCredit, nil, raw, nonce)
	if err != nil {
		return "", fmt.Errorf("sol-minter: Tx bauen: %w", err)
	}
	if err := m.mempool.Add(tx); err != nil {
		return "", fmt.Errorf("sol-minter: Mempool: %w", err)
	}

	txHash := fmt.Sprintf("%x", tx.Hash())
	if m.log != nil {
		m.log.Info("SOL→FND-Gutschrift eingereicht (native Chain)",
			zap.String("recipient", toAddress),
			zap.Uint64("ufnd", payload.UFND),
			zap.String("sol_tx", solTxSig),
			zap.String("tx", txHash[:16]+"…"))
	}
	return txHash, nil
}
