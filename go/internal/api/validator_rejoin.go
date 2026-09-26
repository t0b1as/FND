package api

// Automatische Rückkehr ins Validator-Set.
//
// Ein gestakter Validator wird ausgesetzt, wenn er im Aktivitätsfenster
// (ValidatorLookback Blöcke) weder gebaut noch gestakt hat – so bremsen tote
// Validatoren (Pi aus, Schlüssel verloren) die Rotation nicht dauerhaft. Früher
// musste man danach von Hand erneut staken; seit den 5-s-Blöcken war das nach
// jedem Update nötig. Jetzt stellt sich ein Node, der wieder läuft und auf
// Stand ist, selbst wieder auf: kleine Stake-Transaktion → gilt als aktiv →
// wird eingeplant → bleibt durch eigenes Bauen im Set. Reines Node-Verhalten,
// keine Konsensänderung.

import (
	"context"
	"math/big"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
)

// rejoinAmount: 0,001 FND (Gebühr ~0,000018 FND).
var rejoinAmount = big.NewInt(1_000_000)

func (s *Server) validatorRejoinLoop() {
	time.Sleep(2 * time.Minute) // Start: erst synchronisieren
	var lastSent time.Time
	for {
		if sent := s.validatorRejoinOnce(lastSent); sent {
			lastSent = time.Now()
		}
		time.Sleep(time.Minute)
	}
}

func (s *Server) validatorRejoinOnce(lastSent time.Time) bool {
	if s.chain == nil || s.mempool == nil || s.fileStore == nil || !ProducerRunning.Load() {
		return false
	}
	if !lastSent.IsZero() && time.Since(lastSent) < 5*time.Minute {
		return false // höchstens alle 5 min
	}
	_, canSign, inSet := s.chain.ConsensusSelf()
	if !canSign || inSet {
		return false
	}
	key, addrHex := s.fileStore.ConsensusSigner()
	if key == nil {
		return false
	}
	self, ok := chain.AddressFromHex(addrHex)
	if !ok || s.chain.StakeOf(self).Cmp(chain.MinValidatorStake) < 0 {
		return false // kein (ausreichender) Stake → kein Validator vorgesehen
	}
	// Auf Stand? Sonst würde der Node eingeplant, ohne bauen zu können.
	if hp, ok := interface{}(s.node).(interface {
		MaxPeerHeight(time.Duration) (uint64, bool)
	}); ok {
		if maxH, _ := hp.MaxPeerHeight(10 * time.Minute); maxH > s.chain.Height()+2 {
			return false
		}
	}
	balStr, nonce := s.chain.AccountInfo(self)
	bal, okB := new(big.Int).SetString(balStr, 10)
	need := new(big.Int).Add(rejoinAmount, chain.StakeFee(rejoinAmount))
	if !okB || bal.Cmp(need) < 0 {
		if s.log != nil {
			s.log.Warn("Validator inaktiv, automatische Rückkehr nicht möglich: Node-Wallet ohne freies Guthaben für die Rückkehr-Transaktion (~0,00102 FND)",
				zap.String("node_wallet", addrHex))
		}
		return true // nicht jede Minute warnen
	}
	tx, err := chain.BuildSignedStake(key, new(big.Int).Set(rejoinAmount), nonce)
	if err != nil {
		return false
	}
	if err := s.mempool.Add(tx); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	s.broadcastTx(ctx, tx)
	cancel()
	if s.log != nil {
		s.log.Info("Validator war inaktiv – automatische Rückkehr ins Set (Stake +0,001 FND)", zap.String("node_wallet", addrHex))
	}
	return true
}
