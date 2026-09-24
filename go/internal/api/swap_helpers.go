package api

// Hilfsmethoden für den Swap-Orchestrator: Status-Updates und Chain-Abfragen.

import (
	"encoding/hex"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
)

// setSwapPhase setzt die Phase eines Swaps (und optional eine Fehlernotiz).
func (s *Server) setSwapPhase(swapID string, phase SwapPhase, note string) {
	if s.swapMgr == nil {
		return
	}
	s.swapMgr.mu.Lock()
	defer s.swapMgr.mu.Unlock()
	if sw, ok := s.swapMgr.swaps[swapID]; ok {
		sw.Phase = phase
		if note != "" {
			sw.Note = note
		}
	}
}

// setSwapSolLock vermerkt die SOL-Lock-Signatur und setzt die Phase.
func (s *Server) setSwapSolLock(swapID, sig string) {
	if s.swapMgr == nil {
		return
	}
	s.swapMgr.mu.Lock()
	defer s.swapMgr.mu.Unlock()
	if sw, ok := s.swapMgr.swaps[swapID]; ok {
		sw.SolLockSig = sig
		sw.Phase = SwapSolLocked
	}
}

// setSwapFndLock vermerkt die FND-HTLC-ID und setzt die Phase.
func (s *Server) setSwapFndLock(swapID, htlcID string) {
	if s.swapMgr == nil {
		return
	}
	s.swapMgr.mu.Lock()
	defer s.swapMgr.mu.Unlock()
	if sw, ok := s.swapMgr.swaps[swapID]; ok {
		sw.FndHTLCID = htlcID
		sw.Phase = SwapFndLocked
	}
}

// submitFndLock sperrt FND (für den Orchestrator; teilt die Logik mit dem Handler).
func (s *Server) submitFndLock(seed []string, recipientHex string, amountFND float64, secretHash [32]byte, timelockBlocks uint64) (string, string, uint64, error) {
	recipient, ok := chain.AddressFromHex(recipientHex)
	if !ok {
		return "", "", 0, errBadAddress
	}
	amount := fndToUFND(amountFND)
	timelock := s.chain.Height() + timelockBlocks
	payload := chain.EncodeHTLCLock(recipient, amount, secretHash, timelock)
	return s.submitChainTx(seed, chain.TxHTLCLock, chain.FeeForValue(amount), payload)
}

// orchestratorFndClaim löst einen FND-HTLC ein (für den Orchestrator).
func (s *Server) orchestratorFndClaim(seed []string, htlcIDHex string, secret [32]byte) error {
	id, err := hash32FromHex(htlcIDHex)
	if err != nil {
		return err
	}
	payload := chain.EncodeHTLCClaim(id, secret)
	_, _, _, err = s.submitChainTx(seed, chain.TxHTLCClaim, chain.FeeForValue(nil), payload)
	return err
}

// findFndHTLCByHashlock sucht einen FND-HTLC mit gegebenem Hashlock, dessen
// Empfänger die gegebene Adresse ist. Gibt die ID als Hex zurück.
func (s *Server) findFndHTLCByHashlock(hashlock [32]byte, recipientHex string) (string, bool) {	recipient, ok := chain.AddressFromHex(recipientHex)
	if !ok {
		return "", false
	}
	id, _, found := s.chain.FindHTLCByHashlock(hashlock, recipient)
	if !found {
		return "", false
	}
	return hex.EncodeToString(id[:]), true
}

// findRevealedSecret liest das Preimage eines FND-HTLC, sobald es eingelöst
// wurde (Preimage-Feld ist dann gesetzt).
func (s *Server) findRevealedSecret(htlcIDHex string) ([32]byte, bool) {
	var empty [32]byte
	id, err := hash32FromHex(htlcIDHex)
	if err != nil {
		return empty, false
	}
	h, ok := s.chain.GetHTLC(id)
	if !ok {
		return empty, false
	}
	// Preimage ist nur nach Claim gesetzt (sonst alles Null).
	if h.Preimage == empty {
		return empty, false
	}
	return h.Preimage, true
}

var errBadAddress = &swapErr{"ungültige Adresse"}

type swapErr struct{ m string }

func (e *swapErr) Error() string { return e.m }

// fndAddressFromSeed leitet die FND-Adresse (Hex) aus Seed-Wörtern ab.
func (s *Server) fndAddressFromSeed(words []string) (string, bool) {
	if len(words) == 0 {
		return "", false
	}
	priv, err := identity.DerivePrivateKeyFromSeed(words)
	if err != nil {
		return "", false
	}
	defer priv.D.SetInt64(0)
	addr := chain.PubkeyToAddress(&priv.PublicKey)
	return addr.Hex(), true
}

// findFndHTLCByHashlockForMe sucht den FND-HTLC, dessen Empfänger die eigene
// Adresse (aus dem Seed) ist — für den Fall, dass der Taker FND gab und der
// Maker (wir) den einlösen will.
func (s *Server) findFndHTLCByHashlockForMe(hashlock [32]byte, seed []string) (string, bool) {
	myAddr, ok := s.fndAddressFromSeed(seed)
	if !ok {
		return "", false
	}
	return s.findFndHTLCByHashlock(hashlock, myAddr)
}
