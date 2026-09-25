package api

// Wallet-API: Adresse aus Seed (30 Wörter) oder Email+Passwort ableiten,
// Saldo abfragen, FND überweisen (ephemer signiert) und – nur lokal/Test –
// FND minten.
//
// Sicherheitsmodell: Der Seed wird über die (HTTPS-)Verbindung an den EIGENEN
// Node gesendet, dort EINMALIG der Key abgeleitet, die Transaktion signiert und
// der Key sofort verworfen. Seed-tragende Endpunkte werden nicht geloggt und
// nur von localhost akzeptiert.

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
)

func (s *Server) registerWalletRoutes() {
	g := s.router.Group("/api/v1/wallet")
	g.POST("/derive",   s.walletDerive)   // Seed/Email+PW → Adresse (Vorschau)
	g.GET("/balance",   s.walletBalance)   // Saldo einer Adresse
	g.POST("/transfer", s.walletTransfer)  // FND überweisen (ephemer signiert)
	g.POST("/open", s.walletOpen)          // Wallet ableiten (256 MiB, wie fnd-wallet)
	g.POST("/link", s.walletLink)          // für den Login hinterlegen (verschlüsselt, lokal)
	g.DELETE("/link", s.walletUnlink)
	g.POST("/migrate", s.walletMigrate)    // Guthaben der alten Adresse (vor R456) umziehen
	g.POST("/mint",     s.walletMint)      // FND-Auszahlung vom Fee-Collector (nativer Transfer)
	g.GET("/payout",    s.walletPayoutGet)  // Auto-Payout-Einstellung lesen
	g.POST("/payout",   s.walletPayoutSet)  // Auto-Payout-Einstellung setzen
	g.POST("/stake",    s.walletStake)       // FND als Validator-Stake sperren (Node-Wallet)
	g.POST("/unstake",  s.walletUnstake)     // gestakte FND freigeben (Node-Wallet)
}

// resolveSeed wandelt entweder 30 Wörter ODER Email+Passwort in Seed-Wörter um.
func resolveSeed(words []string, email, password string) ([]string, error) {
	if len(words) > 0 {
		return words, nil
	}
	if email != "" && password != "" {
		return identity.WordsFromEmailPassword(email, password)
	}
	return nil, fmt.Errorf("entweder 30 Wörter ODER email+passwort angeben")
}

// POST /api/v1/wallet/derive  body: {words:[...]} ODER {email, password}
// Gibt die Wallet-Adresse zurück (Vorschau, kein Key verlässt den Node).
func (s *Server) walletDerive(c *gin.Context) {
	var req struct {
		Words    []string `json:"words"`
		Email    string   `json:"email"`
		Password string   `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	if (req.Email != "" || req.Password != "") && len(req.Password) < 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Passwort zu kurz (min. 10 Zeichen)"})
		return
	}
	words, err := resolveSeed(req.Words, req.Email, req.Password)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	addr, err := identity.DeriveAddressFromSeed(words)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	resp := gin.H{"address": addr}
	// Bei Email+PW geben wir die Wörter zurück, damit der Nutzer sie sichern kann
	if len(req.Words) == 0 {
		resp["words"] = words
	}
	c.JSON(http.StatusOK, resp)
}

// GET /api/v1/wallet/balance?address=0x...
func (s *Server) walletBalance(c *gin.Context) {
	// FND ist die native Währung der Fundus-Chain — der Saldo liegt im
	// Chain-State, nicht in einem ERC-20-Contract. (Der frühere Gnosis-Pfad
	// über s.fndClient/FUNDUS_FND_ADDRESS ist nur noch optionaler Legacy-ERC-20.)
	if s.chain == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht verfügbar"})
		return
	}
	addr := c.Query("address")
	if !strings.HasPrefix(addr, "0x") || len(addr) != 42 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Adresse"})
		return
	}
	chainAddr, ok := chain.AddressFromHex(addr)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Adresse"})
		return
	}
	bal, nonce := s.chain.AccountInfo(chainAddr)
	c.JSON(http.StatusOK, gin.H{
		"address": addr,
		"ufnd":    bal,            // Saldo in uFND (kleinste Einheit)
		"fnd":     uFNDToFND(bal), // FND für die Anzeige (1 FND = 1e9 uFND)
		"nonce":   nonce,
	})
}

// POST /api/v1/wallet/transfer
// body: {words|email+password, to, amount}
// Leitet den Key aus dem Seed ab, signiert den FND-transfer, verwirft den Key.
func (s *Server) walletTransfer(c *gin.Context) {
	// Nativer Transfer über die Fundus-Chain: Key aus Seed ableiten, signierte
	// Transfer-Tx bauen, in den Mempool legen. (Kein Gnosis-ERC-20 mehr.)
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req struct {
		Words     []string `json:"words"`
		Email     string   `json:"email"`
		Password  string   `json:"password"`
		To        string   `json:"to"`
		Amount    float64  `json:"amount"`     // Legacy-Feld (Float)
		AmountFND string   `json:"amount_fnd"` // bevorzugt: präziser Dezimalstring
		FeeMode   string   `json:"fee_mode"`   // "added" (Standard): Gebühr ZUSÄTZLICH;
		//                                         "inclusive": Gebühr AUS dem Betrag
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	to, payeeNote, perr := s.resolvePayee(req.To)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}
	if payeeNote != "" {
		c.Header("X-Fundus-Payee-Note", "fundus-id-resolved")
	}
	// Betrag bevorzugt aus dem präzisen String; sonst aus dem Float-Legacy-Feld.
	amountStr := req.AmountFND
	if amountStr == "" && req.Amount > 0 {
		amountStr = strconv.FormatFloat(req.Amount, 'f', 9, 64)
	}
	amount, ok := parseFNDtoU(amountStr)
	if !ok || amount.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger Betrag"})
		return
	}
	// Gebühren-Modus: "inclusive" → eingegebener Betrag ist der GESAMTABZUG
	// (Empfänger bekommt weniger); "added"/Standard → Betrag = Empfängerbetrag,
	// Gebühr kommt obendrauf. BuildSignedTransfer setzt die 1,8%-Gebühr selbst,
	// daher im inclusive-Modus nur den Netto-Empfängerbetrag berechnen.
	if req.FeeMode == "inclusive" {
		amount = chain.NetFromGross(amount)
		if amount.Sign() <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Betrag zu klein für Gebührenabzug"})
			return
		}
	}
	// Key-Quelle: 1) explizite Wörter/Email+Passwort, oder 2) die aktive Session
	// (angemeldete Wallet) — dann kein erneutes Passwort nötig.
	var key *ecdsa.PrivateKey
	if len(req.Words) == 0 && req.Email == "" {
		sess := s.getSession(c)
		if sess == nil || sess.identity == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet – bitte anmelden oder Seed-Wörter angeben"})
			return
		}
		var kerr error
		key, kerr = sess.identity.ChainPrivateKey()
		if kerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": kerr.Error()})
			return
		}
	} else {
		words, werr := resolveSeed(req.Words, req.Email, req.Password)
		if werr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": werr.Error()})
			return
		}
		var kerr error
		key, kerr = identity.DerivePrivateKeyFromSeed(words)
		if kerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": kerr.Error()})
			return
		}
	}
	defer func() {
		if key.D != nil {
			key.D.SetInt64(0) // Key nach Gebrauch nullen
		}
	}()
	from := chain.PubkeyToAddress(&key.PublicKey)
	_, nonce := s.chain.AccountInfo(from)
	tx, err := chain.BuildSignedTransfer(key, to, amount, nonce)
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
	// Früher rief dieser Handler immer produceBlockNow() auf — das scheiterte im
	// Multi-Node-Betrieb mit "nicht dein Zug", obwohl die Tx gültig war.
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
		"payee_note": payeeNote, // Fundus-ID → Wallet-Adresse übersetzt?
		"tx_hash":      hex.EncodeToString(h[:]),
		"from":         from.Hex(),
		"to":           to.Hex(), // tatsächlich verwendete Wallet-Adresse
		"amount":       uFNDToFND(amount.String()),
		"nonce":        nonce,
		"block_height": bh,
		"pending":      !produced,
	})
}

// POST /api/v1/wallet/mint  body: {words|email+password, to, amount|amount_fnd}
// NUR für Tests (lokale Chain). Mintet FND an eine Adresse über den Node-Key
// (muss Minter/Owner sein). Nur von localhost.
func (s *Server) walletMint(c *gin.Context) {
	// Auf der eigenen Chain gibt es kein "Minten aus dem Nichts" — die gesamte
	// Ausgabe liegt seit Genesis beim Fee-Collector. Diese Funktion ist daher eine
	// AUSZAHLUNG: Sie leitet den Key aus dem übergebenen Seed ab (Fee-Collector
	// oder beliebige Quell-Wallet) und sendet einen nativen Transfer.
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req struct {
		Words     []string `json:"words"`
		Email     string   `json:"email"`
		Password  string   `json:"password"`
		To        string   `json:"to"`
		Amount    float64  `json:"amount"`
		AmountFND string   `json:"amount_fnd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage (to/amount)"})
		return
	}
	to, payeeNote, perr := s.resolvePayee(req.To)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}
	if payeeNote != "" {
		c.Header("X-Fundus-Payee-Note", "fundus-id-resolved")
	}
	amountStr := req.AmountFND
	if amountStr == "" && req.Amount > 0 {
		amountStr = strconv.FormatFloat(req.Amount, 'f', 9, 64)
	}
	amount, ok := parseFNDtoU(amountStr)
	if !ok || amount.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger Betrag"})
		return
	}
	words, err := resolveSeed(req.Words, req.Email, req.Password)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	key, err := identity.DerivePrivateKeyFromSeed(words)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer func() {
		if key.D != nil {
			key.D.SetInt64(0)
		}
	}()
	from := chain.PubkeyToAddress(&key.PublicKey)
	_, nonce := s.chain.AccountInfo(from)
	tx, err := chain.BuildSignedTransfer(key, to, amount, nonce)
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
	// die Mint-Tx im Mempool und der zuständige Proposer baut sie ein (Multi-Node).
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
		"payee_note": payeeNote, // Fundus-ID → Wallet-Adresse übersetzt?
		"tx_hash":      hex.EncodeToString(h[:]),
		"from":         from.Hex(),
		"to":           to.Hex(), // tatsächlich verwendete Wallet-Adresse
		"amount":       uFNDToFND(amount.String()),
		"nonce":        nonce,
		"block_height": bh,
		"pending":      !produced,
	})
}
