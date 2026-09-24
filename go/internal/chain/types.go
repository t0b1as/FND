package chain

import (
	"encoding/hex"
	"math/big"
)

// AddressLen: Adresslänge in Bytes. Spec §3a: erste 20 Bytes von BLAKE3(pubkey).
const AddressLen = 20

// Address ist eine 20-Byte-Kontoadresse.
type Address [AddressLen]byte

// Hex gibt die Adresse als 0x-Hex-String zurück.
func (a Address) Hex() string { return "0x" + hex.EncodeToString(a[:]) }

// IsZero meldet, ob die Adresse die Null-Adresse ist.
func (a Address) IsZero() bool {
	for _, b := range a {
		if b != 0 {
			return false
		}
	}
	return true
}

// AddressFromHex parst eine 0x-Hex-Adresse (40 Hex-Zeichen).
func AddressFromHex(s string) (Address, bool) {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
	}
	var a Address
	if len(s) != AddressLen*2 {
		return a, false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return a, false
	}
	copy(a[:], b)
	return a, true
}

// TxType — Transaktionstyp (Spec §4). Phase 1 implementiert nur TxTransfer;
// die übrigen Konstanten sind reserviert für spätere Phasen.
type TxType uint8

const (
	TxTransfer   TxType = 0x01 // Wertüberweisung from→to
	TxStake      TxType = 0x02 // FND als Validator-Stake sperren
	TxUnstake    TxType = 0x03 // Stake entsperren
	TxSlash      TxType = 0x04 // Double-Sign-Beweis: Validator-Stake entziehen
	TxSolCredit  TxType = 0x21 // FND-Gutschrift nach SOL-Einzahlung
	TxStorageReward TxType = 0x25 // Storage-/Transfer-Verdienst: mintet FND gegen Quittungen

	// Vertrags-Templates (Spec §7a). Escrow/Kaufvertrag = 0x30-Familie.
	TxEscrowOpen    TxType = 0x30 // Käufer öffnet Escrow, sperrt Betrag (optional insured)
	TxEscrowConfirm TxType = 0x31 // Käufer bestätigt Lieferung → Freigabe an Verkäufer
	TxEscrowRefund  TxType = 0x32 // Refund an Käufer nach Fristablauf (deadline)
	TxEscrowDispute TxType = 0x33 // Käufer/Verkäufer eröffnet Streit (nur insured)
	TxJurorVote     TxType = 0x34 // benannter Juror stimmt release/refund
	// Storno-/Rücksende-Flow (Spec §7a-ter).
	TxEscrowCancel        TxType = 0x35 // Käufer beantragt Storno (open → cancel_requested)
	TxEscrowSubmitReturn  TxType = 0x36 // Käufer hinterlegt Rücksende-Tracking (→ return_submitted)
	TxEscrowConfirmReturn TxType = 0x37 // Verkäufer bestätigt Rückerhalt → Refund an Käufer
	// Energie/Rohstoff-Zertifizierung (Spec §7b).
	TxMeterRegister TxType = 0x23 // Erzeuger registriert einen Zähler (meter_id → producer)
	TxCommodityCertify TxType = 0x22 // zertifiziert gelieferte Menge → unsettled Energie-Token
	TxCommoditySettle  TxType = 0x24 // rechnet einen Energie-Token ab (settled → gepruned)
	// 0x40+ HTLC (Hash-Timelock-Contract) für atomare Cross-Chain-Swaps (FND↔SOL).
	TxHTLCLock   TxType = 0x40 // sperrt FND mit Hashlock + Timelock
	TxHTLCClaim  TxType = 0x41 // löst FND ein durch Vorzeigen des Preimage (enthüllt Geheimnis)
	TxHTLCRefund TxType = 0x42 // Rückgabe an den Sperrer nach Ablauf des Timelocks
)

// Account — Kontozustand (Spec §3). Balance in uFND (uint128), Nonce als
// Replay-Schutz (uint64).
type Account struct {
	Balance *big.Int
	Nonce   uint64
}

// Transaction (Spec §4). Fee in uFND (uint128). Signature: 65 Bytes secp256k1
// (recoverable). Payload ist typ-abhängig (z.B. TransferPayload bei TxTransfer).
type Transaction struct {
	Type      TxType
	From      Address
	Nonce     uint64
	Fee       *big.Int
	Payload   []byte
	Signature []byte
}

// TransferPayload — Payload für TxTransfer: Empfänger + Betrag (uFND).
type TransferPayload struct {
	To     Address
	Amount *big.Int
}
