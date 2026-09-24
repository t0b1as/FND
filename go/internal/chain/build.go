package chain

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
)

// ─── Komfort-Builder für serverseitiges Signieren (Spec §13, Phase 2.5) ──────
//
// Diese exportierten Funktionen bauen eine vollständige, signierte Transaktion
// aus einem abgeleiteten Schlüssel. Sie kapseln die (unexportierte) Payload-
// Serialisierung, damit die API-Schicht keine internen Encoding-Details kennen
// muss. Die Adresse (tx.From) wird in SignTransaction aus dem PubKey gesetzt.

// BuildSignedTransfer baut eine signierte Wertüberweisung (TxTransfer).
// Die Gebühr (1,8 %) wird automatisch aus dem Betrag berechnet. nonce muss der
// aktuelle Kontostand-Nonce des Absenders sein (siehe Blockchain.AccountInfo).
func BuildSignedTransfer(priv *ecdsa.PrivateKey, to Address, amount *big.Int, nonce uint64) (*Transaction, error) {
	if priv == nil {
		return nil, errors.New("chain: kein Schlüssel")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errors.New("chain: Betrag muss > 0 sein")
	}
	if !fitsUint128(amount) {
		return nil, errors.New("chain: Betrag überschreitet uint128")
	}
	fee := FeeForValue(amount)
	payload := (&TransferPayload{To: to, Amount: new(big.Int).Set(amount)}).encode()
	tx := &Transaction{
		Type:    TxTransfer,
		Nonce:   nonce,
		Fee:     fee,
		Payload: payload,
	}
	if err := SignTransaction(tx, priv); err != nil {
		return nil, err
	}
	return tx, nil
}

// BuildSignedStake baut eine signierte TxStake: sperrt `amount` FND als
// Validator-Stake. Die Adresse mit genug Stake (>= MinValidatorStake) landet
// automatisch im Validator-Set. Gebühr wie beim Stake-Modell (StakeFee).
func BuildSignedStake(priv *ecdsa.PrivateKey, amount *big.Int, nonce uint64) (*Transaction, error) {
	if priv == nil {
		return nil, errors.New("chain: kein Schlüssel")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errors.New("chain: Stake-Betrag muss > 0 sein")
	}
	if !fitsUint128(amount) {
		return nil, errors.New("chain: Betrag überschreitet uint128")
	}
	tx := &Transaction{
		Type:    TxStake,
		Nonce:   nonce,
		Fee:     StakeFee(amount),
		Payload: encodeStakePayload(&StakePayload{Amount: new(big.Int).Set(amount)}),
	}
	if err := SignTransaction(tx, priv); err != nil {
		return nil, err
	}
	return tx, nil
}

// BuildSignedUnstake baut eine signierte TxUnstake: gibt `amount` gestakter FND
// frei (in den Unbonding-Zustand, dann nach der Sperrfrist zurück ins Guthaben).
func BuildSignedUnstake(priv *ecdsa.PrivateKey, amount *big.Int, nonce uint64) (*Transaction, error) {
	if priv == nil {
		return nil, errors.New("chain: kein Schlüssel")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errors.New("chain: Unstake-Betrag muss > 0 sein")
	}
	if !fitsUint128(amount) {
		return nil, errors.New("chain: Betrag überschreitet uint128")
	}
	tx := &Transaction{
		Type:    TxUnstake,
		Nonce:   nonce,
		Fee:     StakeFee(amount),
		Payload: encodeStakePayload(&StakePayload{Amount: new(big.Int).Set(amount)}),
	}
	if err := SignTransaction(tx, priv); err != nil {
		return nil, err
	}
	return tx, nil
}
// Melder (priv) signiert die Meldung; die Gebühr ist 0 (Melden soll sich lohnen).
func BuildSignedSlash(priv *ecdsa.PrivateKey, ev *SlashEvidence, nonce uint64) (*Transaction, error) {
	if priv == nil {
		return nil, errors.New("chain: kein Schlüssel")
	}
	if ev == nil {
		return nil, errors.New("chain: keine Evidence")
	}
	payload := encodeSlashPayload(&SlashPayload{Evidence: *ev})
	tx := &Transaction{
		Type:    TxSlash,
		Nonce:   nonce,
		Fee:     SlashFee(),
		Payload: payload,
	}
	if err := SignTransaction(tx, priv); err != nil {
		return nil, err
	}
	return tx, nil
}

// BuildSignedTx baut eine signierte Transaktion mit beliebigem Typ + vorgefertigter
// Payload (z.B. Escrow-/Energie-Tx, deren Payload der Aufrufer schon serialisiert
// hat). fee in uFND; für gebührenfreie Tx (Escrow-Folgeschritte, Energie) 0
// übergeben. Validierung des Typs erfolgt erst beim Anwenden (state.go).
func BuildSignedTx(priv *ecdsa.PrivateKey, txType TxType, fee *big.Int, payload []byte, nonce uint64) (*Transaction, error) {
	if priv == nil {
		return nil, errors.New("chain: kein Schlüssel")
	}
	if fee == nil {
		fee = new(big.Int)
	}
	if !fitsUint128(fee) {
		return nil, errors.New("chain: Gebühr überschreitet uint128")
	}
	tx := &Transaction{
		Type:    txType,
		Nonce:   nonce,
		Fee:     new(big.Int).Set(fee),
		Payload: payload,
	}
	if err := SignTransaction(tx, priv); err != nil {
		return nil, err
	}
	return tx, nil
}
