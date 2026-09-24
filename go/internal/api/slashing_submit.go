package api

import (
	"context"

	"github.com/fundus/node/internal/chain"
	"go.uber.org/zap"
)

// submitDoubleSignEvidence wird von der Chain aufgerufen, wenn sie Double-Signing
// erkannt hat. Baut eine signierte TxSlash mit dem Node-Schlüssel und legt sie in
// den Mempool. Best-Effort: Fehler werden geloggt, nicht propagiert — die
// Erkennung darf den Node-Betrieb nie stören.
//
// Diese lokale Logik ist NICHT konsens-kritisch: sie reicht nur eine Tx ein. Die
// eigentliche Prüfung (applySlash) läuft deterministisch auf allen Nodes; eine
// fehlerhafte Meldung würde dort abgelehnt.
func (s *Server) submitDoubleSignEvidence(ev *chain.SlashEvidence) {
	if s.fileStore == nil || s.chain == nil || s.mempool == nil || ev == nil {
		return
	}
	key, addrHex := s.fileStore.ConsensusSigner()
	if key == nil {
		return // ohne Schlüssel können wir nicht signieren
	}
	self, ok := chain.AddressFromHex(addrHex)
	if !ok {
		return
	}
	_, nonce := s.chain.AccountInfo(self)

	tx, err := chain.BuildSignedSlash(key, ev, nonce)
	if err != nil {
		s.log.Warn("Double-Sign-Meldung: Tx-Bau fehlgeschlagen", zap.Error(err))
		return
	}
	if err := s.mempool.Add(tx); err != nil {
		// Häufig harmlos: dieselbe Evidence wurde bereits gemeldet.
		s.log.Debug("Double-Sign-Meldung: Mempool-Ablehnung", zap.Error(err))
		return
	}
	// Wenn dieser Node Proposer ist, sofort einbauen; sonst der zuständige.
	_, _, _ = s.submitOrProduce(context.Background())

	s.log.Warn("Double-Signing erkannt und gemeldet",
		zap.Uint64("hoehe", ev.Height))
}
