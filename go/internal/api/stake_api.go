package api

// Stake-API: erlaubt einem Node, FND als Validator-Stake zu sperren (und wieder
// freizugeben). Die Node-Wallet signiert die Transaktion mit ihrem eigenen
// Schlüssel — genau wie beim Payout und der Slash-Meldung. Wer genug staked
// (>= MinValidatorStake), landet automatisch im Validator-Set (R042).
//
// Das ist der letzte fehlende Baustein für den offenen Beitritt: bisher gab es
// die Stake-VERARBEITUNG (applyStake), aber keinen Weg, eine Stake-Tx zu SENDEN.

import (
	"encoding/hex"
	"math/big"
	"net/http"

	"github.com/fundus/node/internal/chain"
	"github.com/gin-gonic/gin"
)

// stakeAmountFromRequest liest den Betrag (in FND) aus dem Request und wandelt
// ihn in uFND. Gemeinsame Logik für stake und unstake.
func stakeAmountFromRequest(c *gin.Context) (*big.Int, bool) {
	var req struct {
		AmountFND float64 `json:"amount_fnd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AmountFND <= 0 {
		return nil, false
	}
	return fndToUFND(req.AmountFND), true
}

// POST /api/v1/wallet/stake  {amount_fnd: 10}
// Sperrt FND der NODE-Wallet als Validator-Stake.
func (s *Server) walletStake(c *gin.Context) {
	s.stakeOrUnstake(c, true)
}

// POST /api/v1/wallet/unstake  {amount_fnd: 10}
func (s *Server) walletUnstake(c *gin.Context) {
	s.stakeOrUnstake(c, false)
}

func (s *Server) stakeOrUnstake(c *gin.Context, isStake bool) {
	if s.fileStore == nil || s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain/Wallet nicht aktiv"})
		return
	}
	amount, ok := stakeAmountFromRequest(c)
	if !ok || amount.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount_fnd muss > 0 sein"})
		return
	}
	// Node-Schlüssel holen (dieselbe Wallet, die auch Blöcke signiert).
	key, addrHex := s.fileStore.ConsensusSigner()
	if key == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Node-Wallet gesperrt oder nicht geladen"})
		return
	}
	self, okAddr := chain.AddressFromHex(addrHex)
	if !okAddr {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "eigene Adresse ungültig"})
		return
	}
	_, nonce := s.chain.AccountInfo(self)

	var tx *chain.Transaction
	var err error
	if isStake {
		tx, err = chain.BuildSignedStake(key, amount, nonce)
	} else {
		tx, err = chain.BuildSignedUnstake(key, amount, nonce)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.mempool.Add(tx); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Mempool: " + err.Error()})
		return
	}
	s.broadcastTx(c.Request.Context(), tx) // an Peers verteilen (Multi-Node)
	produced, height, prodErr := s.submitOrProduce(c.Request.Context())
	if prodErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "abgelehnt: " + prodErr.Error()})
		return
	}
	h := tx.Hash()
	var bh interface{}
	if produced {
		bh = height
	}
	action := "stake"
	if !isStake {
		action = "unstake"
	}
	c.JSON(http.StatusOK, gin.H{
		"action":       action,
		"from":         self.Hex(),
		"amount_fnd":   uFNDToFND(amount.String()),
		"nonce":        nonce,
		"tx_hash":      hex.EncodeToString(h[:]),
		"block_height": bh,
		"pending":      !produced,
	})
}
