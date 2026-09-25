package api

// Fundus-Chain API (Phase 2: Single-Producer + Multi-Node-Sync, on-demand Block-Produktion).
// Endpunkte zum Beobachten und Testen der eigenen Chain. Senden/Signieren von
// Transaktionen erfolgt serverseitig über die Wallet-Endpunkte (spätere Phase);
// hier zunächst Status, Konto-Abfrage und manuelle Block-Produktion.

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
)

func (s *Server) registerChainRoutes() {
	g := s.router.Group("/api/v1/chain")
	g.GET("/status", s.chainStatus)               // Höhe, Head-Hash, Mempool-Größe
	g.GET("/account", s.chainAccount)             // Saldo + Nonce einer Adresse
	g.POST("/tx", s.chainSubmitTx)                // signierte Tx in den Mempool
	g.POST("/send", s.chainSend)                  // serverseitig signieren+einreichen (aus Seed)
	g.POST("/produce", s.chainProduceBlock)       // Block aus Mempool bauen (on-demand)
}

func (s *Server) chainStatus(c *gin.Context) {
	if s.chain == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	head := s.chain.HeadHash()
	mp := 0
	if s.mempool != nil {
		mp = s.mempool.Len()
	}
	// Aktuelles Validator-Set mit ausgeben (Anzahl + Adressen), damit man den
	// Beitritt eines neuen Validators live verfolgen kann, ohne im Log zu suchen.
	valCount := 0
	valList := []string{}
	if vs := s.chain.ValidatorSet(); vs != nil {
		valCount = vs.Len()
		for _, a := range vs.List() {
			valList = append(valList, a.Hex())
		}
	}
	myAddr, canSign, inSet := s.chain.ConsensusSelf()
	out := gin.H{
		"height":     s.chain.Height(),
		"head_hash":  hex.EncodeToString(head[:]),
		"mempool":    mp,
		"validators":       valCount,
		"validator_addrs":  valList,
		// Eigene Rolle: die Validator-Adresse ist die Node-Wallet (node.seed).
		"my_validator_addr": myAddr,
		"i_am_validator":    canSign && inSet,
	}
	if !(canSign && inSet) {
		out["hint"] = "Dieser Node baut keine Blöcke. Damit er es tut: seine my_validator_addr in FUNDUS_VALIDATORS auf ALLEN Nodes identisch eintragen und neu starten."
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) chainAccount(c *gin.Context) {
	if s.chain == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	addr, ok := chain.AddressFromHex(c.Query("address"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Adresse"})
		return
	}
	bal, nonce := s.chain.AccountInfo(addr)
	// uFND → FND (1e9) für die Anzeige.
	fnd := uFNDToFND(bal)
	c.JSON(http.StatusOK, gin.H{
		"address": addr.Hex(),
		"ufnd":    bal,
		"fnd":     fnd,
		"nonce":   nonce,
	})
}

// chainSubmitTx nimmt eine bereits signierte Transaktion (Hex-Felder) entgegen
// und legt sie in den Mempool.
func (s *Server) chainSubmitTx(c *gin.Context) {
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req struct {
		Type      uint8  `json:"type"`
		From      string `json:"from"`
		Nonce     uint64 `json:"nonce"`
		Fee       string `json:"fee"`
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	from, ok := chain.AddressFromHex(req.From)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige From-Adresse"})
		return
	}
	fee, ok := new(big.Int).SetString(req.Fee, 10)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Fee"})
		return
	}
	payload, err := hex.DecodeString(trimHex(req.Payload))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Payload (hex)"})
		return
	}
	sig, err := hex.DecodeString(trimHex(req.Signature))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Signatur (hex)"})
		return
	}
	tx := &chain.Transaction{
		Type:      chain.TxType(req.Type),
		From:      from,
		Nonce:     req.Nonce,
		Fee:       fee,
		Payload:   payload,
		Signature: sig,
	}
	if err := s.mempool.Add(tx); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h := tx.Hash()
	s.broadcastTx(c.Request.Context(), tx) // an Peers verteilen (Multi-Node)
	c.JSON(http.StatusOK, gin.H{"tx_hash": hex.EncodeToString(h[:]), "mempool": s.mempool.Len()})
}

// chainSend leitet serverseitig aus den Seed-Wörtern ab, ermittelt die Nonce,
// baut + signiert eine Wertüberweisung und reicht sie in den Mempool ein.
// Bequemer als /chain/tx (Client muss nicht selbst signieren). Der abgeleitete
// Private Key wird unmittelbar nach Gebrauch genullt (nur im RAM, flüchtig).
//
// POST /api/v1/chain/send
// Body: { "words": ["...", ...], "to": "0x...", "amount_fnd": "12.5" }
func (s *Server) chainSend(c *gin.Context) {
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req struct {
		Words     []string `json:"words"`
		To        string   `json:"to"`
		AmountFND string   `json:"amount_fnd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	if len(req.Words) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Seed-Wörter fehlen"})
		return
	}
	to, _, terr := s.resolvePayee(req.To)
	if terr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": terr.Error()})
		return
	}
	amount, ok := parseFNDtoU(req.AmountFND)
	if !ok || amount.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger Betrag"})
		return
	}

	// Schlüssel ableiten (BLAKE3-Adresse, identisch zur Chain seit Phase 2.4).
	priv, err := identity.DerivePrivateKeyFromSeed(req.Words)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ableitung fehlgeschlagen"})
		return
	}
	defer priv.D.SetInt64(0) // Schlüssel nach Gebrauch nullen

	from := chain.PubkeyToAddress(&priv.PublicKey)
	_, nonce := s.chain.AccountInfo(from)

	tx, err := chain.BuildSignedTransfer(priv, to, amount, nonce)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.mempool.Add(tx); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h := tx.Hash()
	s.broadcastTx(c.Request.Context(), tx) // an Peers verteilen (Multi-Node)
	// Selbst produzieren, wenn dieser Node der nächste Proposer ist; sonst bleibt
	// die Tx im Mempool und der zuständige Proposer baut sie ein (Multi-Node).
	produced, blockHeight, prodErr := s.submitOrProduce(c.Request.Context())
	if prodErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "Transaktion abgelehnt: " + prodErr.Error(),
			"tx_hash": hex.EncodeToString(h[:]),
		})
		return
	}
	var bh interface{}
	if produced {
		bh = blockHeight
	}
	c.JSON(http.StatusOK, gin.H{
		"tx_hash":      hex.EncodeToString(h[:]),
		"from":         from.Hex(),
		"nonce":        nonce,
		"block_height": bh,
	})
}

// parseFNDtoU wandelt einen FND-Betrag als Dezimalstring (z.B. "12.5") in uFND
// (Ganzzahl, 1 FND = 1e9 uFND) um. Maximal 9 Nachkommastellen.
func parseFNDtoU(s string) (*big.Int, bool) {
	if s == "" {
		return nil, false
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	intPart, fracPart := s, ""
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			intPart, fracPart = s[:i], s[i+1:]
			break
		}
	}
	if len(fracPart) > 9 {
		return nil, false // mehr Präzision als uFND erlaubt
	}
	// Auf 9 Nachkommastellen auffüllen.
	for len(fracPart) < 9 {
		fracPart += "0"
	}
	combined := intPart + fracPart
	if combined == "" {
		return nil, false
	}
	v, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return nil, false
	}
	if neg {
		v.Neg(v)
	}
	return v, true
}

// produceBlockNow baut sofort einen Block aus dem aktuellen Mempool und
// broadcastet ihn an die Peers (Single-Producer, Phase 2). Wird nach jeder
// eingereichten Tx aufgerufen, damit Transfers ohne Wartezeit bestätigt sind.
// Gibt Höhe + Hash zurück; ein leerer Mempool ist kein Fehler (height,"",nil).
func (s *Server) produceBlockNow(ctx context.Context) (height uint64, hashHex string, err error) {
	if s.chain == nil || s.mempool == nil {
		return 0, "", fmt.Errorf("Chain nicht aktiv")
	}
	txs := s.mempool.Take(0) // alle

	// Storage-Reward einfügen (Phase 3): aus den gesammelten Quittungen einen
	// TxStorageReward bauen und voranstellen. Der Produzent mintet damit FND für
	// die Provider (1 FND/TB). Quittungen werden erst nach erfolgreichem Block
	// aus dem Pending-Store entfernt.
	var rewardedIDs [][32]byte
	if s.fileStore != nil && s.nodeWalletAddr != nil {
		_, nonce := s.chain.AccountInfo(*s.nodeWalletAddr)
		if rtx, ids, rerr := s.fileStore.BuildStorageRewardTx(nonce); rerr == nil && rtx != nil {
			txs = append([]*chain.Transaction{rtx}, txs...) // Reward zuerst
			rewardedIDs = ids
		} else if rerr != nil {
			s.log.Warn("Storage-Reward-Tx bauen fehlgeschlagen", zap.Error(rerr))
		}
	}

	if len(txs) == 0 {
		return s.chain.Height(), "", nil
	}
	blk, err := s.chain.ProduceBlock(txs, uint64(time.Now().Unix()))
	if err != nil {
		// Block-Bau gescheitert (eine Tx ungültig: Nonce/Guthaben/Signatur).
		// Die entnommenen Txs NICHT verlieren — zurück in den Mempool legen,
		// damit gültige Txs beim nächsten Versuch erneut berücksichtigt werden.
		// (Der Reward-Tx ist NICHT im Mempool und wird beim nächsten Lauf neu
		// gebaut — daher hier nicht zurücklegen.)
		for _, tx := range txs {
			if tx.Type != chain.TxStorageReward {
				_ = s.mempool.Add(tx)
			}
		}
		return 0, "", err
	}
	hh := blk.Header.Hash()
	// Geminteter Reward bestätigt → Quittungen aus dem Pending-Store entfernen,
	// damit sie nicht erneut eingereicht werden.
	if len(rewardedIDs) > 0 && s.fileStore != nil {
		s.fileStore.SettleRewardedReceipts(rewardedIDs)
		s.log.Info("Storage-Reward gemintet",
			zap.Int("quittungen", len(rewardedIDs)),
			zap.Uint64("hoehe", blk.Header.Height))
	}
	// Propagation an Peers: Topic-Broadcast UND direkter Push (zuverlässiger).
	if s.node != nil {
		if blockJSON, eerr := s.chain.ExportBlockJSON(blk.Header.Height); eerr == nil {
			_ = s.node.BroadcastBlock(ctx, blockJSON)
		}
	}
	return blk.Header.Height, hex.EncodeToString(hh[:]), nil
}

// submitOrProduce entscheidet nach dem Einreichen einer Tx in den Mempool, ob
// dieser Node den Block sofort selbst baut (weil er der nächste Proposer ist
// oder Solo läuft) oder ob die Tx im Mempool bleibt, bis der zuständige Proposer
// sie über seinen automatischen Loop einbaut. Rückgabe: produced=true mit Höhe,
// wenn selbst gebaut; produced=false (pending) sonst. err nur bei echtem
// Block-Bau-Fehler des eigenen Blocks (ungültige Tx).
func (s *Server) submitOrProduce(ctx context.Context) (produced bool, height uint64, err error) {
	if s.chain != nil && !s.chain.AmIProposerNext() {
		return false, 0, nil // anderer Node ist dran → Tx bleibt im Mempool
	}
	h, _, perr := s.produceBlockNow(ctx)
	if perr != nil {
		return false, 0, perr
	}
	return true, h, nil
}

// broadcastTx verteilt eine eingereichte Transaktion an alle Peers (GossipSub),
// damit sie auch dann in einen Block kommt, wenn ein ANDERER Node der zuständige
// Proposer ist. Ohne diesen Broadcast bliebe die Tx im lokalen Mempool hängen —
// genau der Grund, warum eine Stake-Tx eines Nodes die anderen nie erreichte.
func (s *Server) broadcastTx(ctx context.Context, tx *chain.Transaction) {
	if s.node == nil || tx == nil {
		return
	}
	data, err := chain.TxToWireJSON(tx)
	if err != nil {
		return
	}
	// ASYNCHRON mit eigenem Timeout: der P2P-Broadcast darf den HTTP-Request
	// NICHT blockieren (tote Peers → hängende Requests → Node reagiert nicht mehr).
	go func() {
		bctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.node.BroadcastTx(bctx, data)
	}()
}

// chainProduceBlock baut on-demand einen Block aus dem Mempool (Phase 2).
func (s *Server) chainProduceBlock(c *gin.Context) {
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	height, hashHex, err := s.produceBlockNow(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"height": height,
		"hash":   hashHex,
	})
}

// uFNDToFND formatiert uFND (Dezimalstring) als FND (1 FND = 1e9 uFND).
func uFNDToFND(ufnd string) string {
	v, ok := new(big.Int).SetString(ufnd, 10)
	if !ok {
		return "0"
	}
	per := big.NewInt(chain.UFNDPerFND)
	whole := new(big.Int).Quo(v, per)
	frac := new(big.Int).Mod(v, per)
	if frac.Sign() == 0 {
		return whole.String()
	}
	// Nachkommastellen auf 9 Stellen auffüllen, dann trailing zeros kürzen.
	fracStr := frac.String()
	for len(fracStr) < 9 {
		fracStr = "0" + fracStr
	}
	for len(fracStr) > 1 && fracStr[len(fracStr)-1] == '0' {
		fracStr = fracStr[:len(fracStr)-1]
	}
	return whole.String() + "." + fracStr
}

func trimHex(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

// mempoolMaintenance: alle 20 s wartende Transaktionen pflegen.
//
//  1. Bereinigen: Nonce kleiner als die Konto-Nonce → bereits (in einem
//     fremden Block) eingebaut oder überholt. Ohne das behielt ein Node, der
//     selbst keine Blöcke baut, jede Transaktion für immer.
//  2. Erneut verteilen: Eine Transaktion wurde bisher nur EINMAL beim Einreichen
//     verschickt. Kam sie dabei bei keinem Validator an (Neustart, Verbindung
//     gerade weg), lag sie für immer beim Einreicher – der sie als
//     Nicht-Validator nicht einbauen kann (Swap hing bei "warte auf FND-Sperre").
//     Empfänger erkennen Doppelte am Hash.
func (s *Server) mempoolMaintenance() {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for range t.C {
		if s.mempool == nil || s.chain == nil {
			continue
		}
		pend := s.mempool.Pending()
		if len(pend) == 0 {
			continue
		}
		stale := map[[32]byte]bool{}
		live := make([]*chain.Transaction, 0, len(pend))
		for _, tx := range pend {
			if tx == nil {
				continue
			}
			if _, nonce := s.chain.AccountInfo(tx.From); tx.Nonce < nonce {
				stale[tx.Hash()] = true
				continue
			}
			live = append(live, tx)
		}
		if n := s.mempool.RemoveHashes(stale); n > 0 && s.log != nil {
			s.log.Debug("Mempool bereinigt", zap.Int("entfernt", n))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		for _, tx := range live {
			s.broadcastTx(ctx, tx)
		}
		cancel()
	}
}
