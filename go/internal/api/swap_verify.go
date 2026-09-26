package api

// Prüfung der Sperre der GEGENSEITE, bevor man selbst sperrt oder einlöst.
//
// Früher genügte, dass eine Sperre existierte. Ein manipulierter Node hätte
// z.B. 1 Lamport sperren und die volle FND-Menge erhalten können, oder eine
// Sperre mit so kurzer Frist setzen, dass er zurückholt, bevor man einlöst.
// Jetzt werden Betrag, Empfänger (man selbst) und Restlaufzeit geprüft.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/gagliardetto/solana-go"

	"github.com/fundus/node/internal/chain"
)

// Mindest-Restlaufzeit der fremden Sperre, um sicher einlösen zu können (~1 h).
const (
	verifyMarginSlots  = 9000 // Solana, ~400 ms/Slot
	verifyMarginBlocks = 720  // Fundus-Chain, ~5 s/Block
)

// verifySolLock prüft das Swap-Konto (Anchor: 8 Byte Diskriminator, dann
// amount_lamports u64, expiry_slot u64, initiator, redeemer, secret_hash).
func (s *Server) verifySolLock(ctx context.Context, pda solana.PublicKey, expectLamports uint64,
	redeemer solana.PublicKey, minSlotsLeft uint64) error {
	res, err := s.swapMgr.solanaRPCCall(ctx, "getAccountInfo", []interface{}{
		pda.String(), map[string]interface{}{"encoding": "base64", "commitment": "confirmed"},
	})
	if err != nil {
		return fmt.Errorf("Swap-Konto nicht lesbar: %w", err)
	}
	var v struct {
		Value *struct {
			Data  []string `json:"data"`
			Owner string   `json:"owner"`
		} `json:"value"`
	}
	if json.Unmarshal(res, &v) != nil || v.Value == nil || len(v.Value.Data) == 0 {
		return fmt.Errorf("Swap-Konto nicht gefunden")
	}
	if v.Value.Owner != s.swapMgr.htlcProgramID {
		return fmt.Errorf("Swap-Konto gehört nicht zum HTLC-Programm")
	}
	raw, err := base64.StdEncoding.DecodeString(v.Value.Data[0])
	if err != nil || len(raw) < 120 {
		return fmt.Errorf("Swap-Konto unvollständig")
	}
	amount := binary.LittleEndian.Uint64(raw[8:16])
	expiry := binary.LittleEndian.Uint64(raw[16:24])
	if amount+1 < expectLamports { // +1: Rundung
		return fmt.Errorf("gesperrt %.9f SOL, erwartet %.9f SOL", float64(amount)/1e9, float64(expectLamports)/1e9)
	}
	if !bytes.Equal(raw[56:88], redeemer.Bytes()) {
		return fmt.Errorf("Empfänger der Sperre ist nicht diese Wallet")
	}
	slotRes, err := s.swapMgr.solanaRPCCall(ctx, "getSlot", []interface{}{map[string]interface{}{"commitment": "confirmed"}})
	if err != nil {
		return fmt.Errorf("aktueller Slot nicht lesbar: %w", err)
	}
	var slot uint64
	if json.Unmarshal(slotRes, &slot) != nil {
		return fmt.Errorf("aktueller Slot unlesbar")
	}
	if expiry < slot+minSlotsLeft {
		return fmt.Errorf("Frist der Sperre zu kurz (%d Slots übrig, mindestens %d nötig)", int64(expiry)-int64(slot), minSlotsLeft)
	}
	return nil
}

// verifyFndLock prüft eine FND-Sperre: offen, Betrag, Restlaufzeit
// (der Empfänger ist durch die Suche nach eigener Adresse schon festgelegt).
func (s *Server) verifyFndLock(idHex string, expectUFND *big.Int, minBlocksLeft uint64) error {
	id, err := hash32FromHex(strings.TrimSpace(idHex))
	if err != nil {
		return fmt.Errorf("FND-Sperr-ID ungültig")
	}
	h, ok := s.chain.GetHTLC(id)
	if !ok {
		return fmt.Errorf("FND-Sperre nicht gefunden")
	}
	if h.State != chain.HTLCLocked {
		return fmt.Errorf("FND-Sperre nicht mehr offen")
	}
	if h.Amount == nil || h.Amount.Cmp(expectUFND) < 0 {
		got := "0"
		if h.Amount != nil {
			got = uFNDToFND(h.Amount.String())
		}
		return fmt.Errorf("gesperrt %s FND, erwartet %s FND", got, uFNDToFND(expectUFND.String()))
	}
	height := s.chain.Height()
	if h.Timelock < height+minBlocksLeft {
		return fmt.Errorf("Frist der FND-Sperre zu kurz (%d Blöcke übrig, mindestens %d nötig)", int64(h.Timelock)-int64(height), minBlocksLeft)
	}
	return nil
}

// verifyCounterpartyLock: nach dem Auffinden der fremden Sperre prüfen. Wer als
// Erster gesperrt hat (Käufer), braucht eine Frist, die die eigene (Zweit-)Sperre
// des Anbieters deutlich überdauert; der Käufer braucht nur Zeit zum Einlösen.
func (s *Server) verifyCounterpartyLock(ctx context.Context, ss *swapSession, fndLockID string) error {
	if ss.giveChain == "fnd" {
		// Ich gebe FND → Gegenseite hat SOL gesperrt, Empfänger bin ich.
		initiator, err := solana.PublicKeyFromBase58(ss.counterpartySol)
		if err != nil {
			return fmt.Errorf("Solana-Adresse der Gegenseite ungültig")
		}
		client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
		if err != nil {
			return err
		}
		pda, _, err := client.deriveSwapPDA(initiator, ss.secretHash)
		if err != nil {
			return err
		}
		minSlots := uint64(verifyMarginSlots)
		if !ss.isTaker {
			minSlots += secondLockerTimelockSlots
		}
		return s.verifySolLock(ctx, pda, uint64(ss.amountSOL*1_000_000_000), ss.solKey.PublicKey(), minSlots)
	}
	// Ich gebe SOL → Gegenseite hat FND gesperrt (an meine Adresse).
	minBlocks := uint64(verifyMarginBlocks)
	if !ss.isTaker {
		minBlocks += secondLockerTimelockBlocks
	}
	return s.verifyFndLock(fndLockID, fndToUFND(ss.amountFND), minBlocks)
}
