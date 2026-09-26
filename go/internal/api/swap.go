package api

// Swap-Koordination für atomare FND↔SOL-Tauschgeschäfte über HTLC.
//
// Ablauf (vereinfacht), wenn Käufer B die Verkaufs-Order von A annimmt:
//  1. B erzeugt ein Geheimnis S, berechnet H = blake3(S). B sperrt SOL auf
//     Solana (HTLC, Empfänger A, Hashlock H, Timelock T_B) — signiert per Phantom.
//  2. A sieht H und sperrt FND auf der Fundus-Chain (TxHTLCLock, Empfänger B,
//     Hashlock H, Timelock T_A > T_B).
//  3. B löst die FND ein (TxHTLCClaim mit S) — enthüllt S auf der Fundus-Chain.
//  4. A liest S und löst die SOL auf Solana ein (HTLC-Claim mit S).
// Bricht jemand ab, bekommt nach Ablauf des jeweiligen Timelocks jeder sein
// Geld zurück. Der Node koordiniert nur; die Solana-Transaktionen signiert der
// Nutzer client-seitig mit Phantom, die Fundus-Transaktionen mit seiner Seed.
//
// HINWEIS: Diese Schicht liefert die Parameter und verfolgt den Status. Die
// konkrete Solana-HTLC-Programm-ID + Instruktions-Layout kommen aus der Config
// (FUNDUS_SWAP_HTLC_PROGRAM), NICHT hartcodiert — so ist der Code an ein real
// existierendes, deploytes Programm anbindbar, ohne auf Annahmen zu bauen.

import (
	"sort"
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gagliardetto/solana-go"
	"lukechampine.com/blake3"

	"github.com/fundus/node/internal/chain"
)

// SwapPhase beschreibt den Fortschritt eines Swaps.
type SwapPhase string

const (
	SwapInitiated    SwapPhase = "initiated"     // Swap angelegt, wartet auf SOL-Lock
	SwapSolLocked    SwapPhase = "sol_locked"    // Käufer hat SOL gesperrt
	SwapFndLocked    SwapPhase = "fnd_locked"    // Verkäufer hat FND gesperrt
	SwapFndClaimed   SwapPhase = "fnd_claimed"   // Käufer hat FND eingelöst (S enthüllt)
	SwapSolClaimed   SwapPhase = "sol_claimed"   // Verkäufer hat SOL eingelöst → fertig
	SwapRefunded     SwapPhase = "refunded"      // abgebrochen, zurückgegeben
	SwapExpired      SwapPhase = "expired"
)

// Swap verfolgt einen laufenden Tausch.
type Swap struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"order_id"`     // die angenommene Order
	Buyer       string    `json:"buyer"`        // FND-Adresse des Käufers
	Seller      string    `json:"seller"`       // FND-Adresse des Verkäufers
	BuyerSol    string    `json:"buyer_sol"`    // Solana-Adresse Käufer
	SellerSol   string    `json:"seller_sol"`   // Solana-Adresse Verkäufer
	AmountFND   float64   `json:"amount_fnd"`
	AmountSOL   float64   `json:"amount_sol"`
	Hashlock    string    `json:"hashlock"`     // H = blake3(S), hex
	Phase       SwapPhase `json:"phase"`
	Note        string    `json:"note,omitempty"` // Fehler-/Statusnotiz (Diagnose)
	FndHTLCID   string    `json:"fnd_htlc_id,omitempty"`  // ID des Fundus-HTLC
	SolLockSig  string    `json:"sol_lock_sig,omitempty"` // Solana-Tx-Signatur des SOL-Locks
	CreatedAt   int64     `json:"created_at"`
	ExpiresAt   int64     `json:"expires_at"`
}

// SwapManager verwaltet laufende Swaps (lokal, im Speicher).
type SwapManager struct {
	mu    sync.RWMutex
	swaps map[string]*Swap
	// Persistenz (swaps.json): Swaps enthalten keine Geheimnisse (nur Adressen,
	// Beträge, Hashlock, HTLC-IDs, Phase) und dürfen daher auf die Platte.
	path     string
	lastSnap []byte
	// Solana-Parameter aus der Config (für das Frontend/Phantom).
	solRPC        string
	htlcProgramID string
}

func newSwapManager(solRPC, htlcProgramID string) *SwapManager {
	return &SwapManager{
		swaps:         make(map[string]*Swap),
		solRPC:        solRPC,
		htlcProgramID: htlcProgramID,
	}
}

// ── API-Handler ─────────────────────────────────────────────────────────────

// swapParams liefert die Solana-HTLC-Parameter, die das Frontend braucht, um
// mit Phantom eine HTLC-Transaktion zu bauen.
// GET /api/v1/swap/params
func (s *Server) swapParams(c *gin.Context) {
	if s.swapMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht aktiv"})
		return
	}
	ready := s.swapMgr.htlcProgramID != ""
	c.JSON(http.StatusOK, gin.H{
		"sol_rpc":          s.swapMgr.solRPC,
		"htlc_program_id":  s.swapMgr.htlcProgramID,
		"ready":            ready, // false, solange kein HTLC-Programm konfiguriert ist
		"note":             "Eigenes Solana-HTLC-Programm (blake3-Hashlock via nativem solana_program::blake3-Syscall, kompatibel zur Fundus-Chain) via FUNDUS_SWAP_HTLC_PROGRAM konfigurieren. Struktur-Blaupause: Garden-Finance-HTLC (initiate/redeem/refund, PDA-Vaults).",
	})
}

// swapInitiate legt einen Swap an (Käufer nimmt eine Order an). Erzeugt die
// Swap-Verfolgung; die tatsächlichen Locks passieren danach client-seitig.
// POST /api/v1/swap  Body: { order_id, buyer, seller, buyer_sol, seller_sol, amount_fnd, amount_sol, hashlock }
func (s *Server) swapInitiate(c *gin.Context) {
	if s.swapMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht aktiv"})
		return
	}
	var req struct {
		OrderID   string  `json:"order_id"`
		Buyer     string  `json:"buyer"`
		Seller    string  `json:"seller"`
		BuyerSol  string  `json:"buyer_sol"`
		SellerSol string  `json:"seller_sol"`
		AmountFND float64 `json:"amount_fnd"`
		AmountSOL float64 `json:"amount_sol"`
		Hashlock  string  `json:"hashlock"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Hashlock == "" || req.AmountFND <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hashlock und amount_fnd erforderlich"})
		return
	}
	now := time.Now().Unix()
	sw := &Swap{
		ID: orderID(&Order{Maker: req.Buyer, Side: OrderBuy, CreatedAt: now, SolAddress: req.Hashlock}),
		OrderID: req.OrderID, Buyer: req.Buyer, Seller: req.Seller,
		BuyerSol: req.BuyerSol, SellerSol: req.SellerSol,
		AmountFND: req.AmountFND, AmountSOL: req.AmountSOL,
		Hashlock: req.Hashlock, Phase: SwapInitiated,
		CreatedAt: now, ExpiresAt: now + 3600, // 1h Fenster für den Swap
	}
	s.swapMgr.mu.Lock()
	s.swapMgr.swaps[sw.ID] = sw
	s.swapMgr.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true, "swap": sw})
}

// swapGet liefert den Status eines Swaps.
// GET /api/v1/swap/:id
func (s *Server) swapGet(c *gin.Context) {
	if s.swapMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht aktiv"})
		return
	}
	s.swapMgr.mu.RLock()
	sw, ok := s.swapMgr.swaps[c.Param("id")]
	s.swapMgr.mu.RUnlock()
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Swap nicht gefunden"})
		return
	}
	c.JSON(http.StatusOK, sw)
}

// swapUpdatePhase aktualisiert die Phase eines Swaps (vom Frontend gemeldet,
// nachdem ein Schritt on-chain bestätigt wurde).
// POST /api/v1/swap/:id/phase  Body: { phase, fnd_htlc_id?, sol_lock_sig? }
func (s *Server) swapUpdatePhase(c *gin.Context) {
	if s.swapMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht aktiv"})
		return
	}
	var req struct {
		Phase      SwapPhase `json:"phase"`
		FndHTLCID  string    `json:"fnd_htlc_id"`
		SolLockSig string    `json:"sol_lock_sig"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.swapMgr.mu.Lock()
	defer s.swapMgr.mu.Unlock()
	sw, ok := s.swapMgr.swaps[c.Param("id")]
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Swap nicht gefunden"})
		return
	}
	if req.Phase != "" {
		sw.Phase = req.Phase
	}
	if req.FndHTLCID != "" {
		sw.FndHTLCID = req.FndHTLCID
	}
	if req.SolLockSig != "" {
		sw.SolLockSig = req.SolLockSig
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "swap": sw})
}

// solanaRPCCall macht einen JSON-RPC-Aufruf an den konfigurierten Solana-RPC.
func (sm *SwapManager) solanaRPCCall(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sm.solRPC, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, &rpcError{out.Error.Message}
	}
	return out.Result, nil
}

type rpcError struct{ msg string }

func (e *rpcError) Error() string { return e.msg }

// swapHealth prüft LIVE, ob der Solana-RPC erreichbar ist und das HTLC-Programm
// dort existiert + ausführbar ist. Beweist die End-to-End-Verbindung
// Node → RPC → Solana-Programm.
// GET /api/v1/swap/health
func (s *Server) swapHealth(c *gin.Context) {
	if s.swapMgr == nil || s.swapMgr.htlcProgramID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht konfiguriert"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()

	// 1. RPC erreichbar? (getHealth)
	if _, err := s.swapMgr.solanaRPCCall(ctx, "getHealth", []interface{}{}); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"rpc_reachable": false,
			"error":         solRPCErr(err, s.swapMgr.solRPC).Error(),
			"rpc":           maskRPC(s.swapMgr.solRPC),
		})
		return
	}

	// 2. Programm existiert + ausführbar? (getAccountInfo)
	res, err := s.swapMgr.solanaRPCCall(ctx, "getAccountInfo", []interface{}{
		s.swapMgr.htlcProgramID,
		map[string]interface{}{"encoding": "base64"},
	})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"rpc_reachable": true, "program_found": false,
			"error": "Programm-Abfrage fehlgeschlagen: " + err.Error(),
		})
		return
	}
	var acc struct {
		Value *struct {
			Executable bool   `json:"executable"`
			Owner      string `json:"owner"`
		} `json:"value"`
	}
	_ = json.Unmarshal(res, &acc)
	programFound := acc.Value != nil
	executable := programFound && acc.Value.Executable

	c.JSON(http.StatusOK, gin.H{
		"rpc_reachable": true,
		"program_found": programFound,
		"executable":    executable,
		"program_id":    s.swapMgr.htlcProgramID,
		"rpc":           maskRPC(s.swapMgr.solRPC),
		"status":        map[bool]string{true: "✓ Solana-HTLC-Programm live und bereit", false: "Programm nicht gefunden/nicht ausführbar"}[executable],
	})
}

// ── Echte Solana-HTLC-Ausführung ─────────────────────────────────────────────

// swapSolInitiate sperrt SOL auf Solana (initiate). Der Nutzer gibt seinen
// Solana-Schlüssel ein (Base58 oder Mnemonic).
// POST /api/v1/swap/sol/initiate
// Body: { sol_key, amount_sol, expires_in_slots, redeemer, secret_hash }
func (s *Server) swapSolInitiate(c *gin.Context) {
	if s.swapMgr == nil || s.swapMgr.htlcProgramID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht konfiguriert"})
		return
	}
	var req struct {
		SolKey         string  `json:"sol_key"          binding:"required"`
		AmountSOL      float64 `json:"amount_sol"       binding:"required"`
		ExpiresInSlots uint64  `json:"expires_in_slots"`
		Redeemer       string  `json:"redeemer"         binding:"required"` // Solana-Adresse des Einlösers
		SecretHashHex  string  `json:"secret_hash"      binding:"required"` // 32-Byte hex
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	key, err := s.solKeyInput(c, req.SolKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	redeemer, err := solanaPubkeyFromString(req.Redeemer)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Redeemer-Adresse: " + err.Error()})
		return
	}
	secretHash, err := hash32FromHex(req.SecretHashHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger secret_hash: " + err.Error()})
		return
	}
	expires := req.ExpiresInSlots
	if expires == 0 {
		expires = 216000 // ~24h bei ~400ms/Slot
	}
	lamports := uint64(req.AmountSOL * 1_000_000_000)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	sig, err := client.Initiate(ctx, key, lamports, expires, redeemer, secretHash)
	// Schlüssel sofort vergessen.
	req.SolKey = ""
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "signature": sig})
}

// swapSolRedeem löst gesperrte SOL mit dem Geheimnis ein.
// POST /api/v1/swap/sol/redeem  Body: { sol_key, initiator, secret_hash, secret }
func (s *Server) swapSolRedeem(c *gin.Context) {
	if s.swapMgr == nil || s.swapMgr.htlcProgramID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht konfiguriert"})
		return
	}
	var req struct {
		SolKey        string `json:"sol_key"     binding:"required"`
		Initiator     string `json:"initiator"   binding:"required"`
		SecretHashHex string `json:"secret_hash" binding:"required"`
		SecretHex     string `json:"secret"      binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	key, err := s.solKeyInput(c, req.SolKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	initiator, err := solanaPubkeyFromString(req.Initiator)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Initiator-Adresse"})
		return
	}
	secretHash, err := hash32FromHex(req.SecretHashHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger secret_hash"})
		return
	}
	secret, err := hash32FromHex(req.SecretHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiges secret"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	sig, err := client.Redeem(ctx, key, initiator, secretHash, secret)
	req.SolKey = ""
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "signature": sig})
}

// swapSolRefund gibt gesperrte SOL nach Timelock zurück.
// POST /api/v1/swap/sol/refund  Body: { sol_key, secret_hash }
func (s *Server) swapSolRefund(c *gin.Context) {
	if s.swapMgr == nil || s.swapMgr.htlcProgramID == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht konfiguriert"})
		return
	}
	var req struct {
		SolKey        string `json:"sol_key"     binding:"required"`
		SecretHashHex string `json:"secret_hash" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	client, err := newSolHTLCClient(s.swapMgr.solRPC, s.swapMgr.htlcProgramID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	key, err := s.solKeyInput(c, req.SolKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	secretHash, err := hash32FromHex(req.SecretHashHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger secret_hash"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	sig, err := client.Refund(ctx, key, secretHash)
	req.SolKey = ""
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "signature": sig})
}

// swapSecret erzeugt ein zufälliges 32-Byte-Geheimnis (secret) und den
// passenden blake3-Hashlock (secret_hash). Für Tests + zur Swap-Vorbereitung:
// der Käufer erzeugt hier sein Geheimnis, sperrt SOL mit dem secret_hash, und
// löst später mit dem secret ein (das dabei on-chain enthüllt wird).
// GET /api/v1/swap/secret
func (s *Server) swapSecret(c *gin.Context) {
	var secret [32]byte
	if _, err := crand.Read(secret[:]); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Zufall fehlgeschlagen"})
		return
	}
	hash := blake3.Sum256(secret[:])
	c.JSON(http.StatusOK, gin.H{
		"secret":      hex.EncodeToString(secret[:]),
		"secret_hash": hex.EncodeToString(hash[:]),
		"note":        "secret geheim halten! Erst beim Einlösen (redeem) verwenden. secret_hash ist öffentlich.",
	})
}

// swapKeyAddress zeigt, welche Solana-Adresse der Node aus einem eingegebenen
// Schlüssel (Mnemonic/Base58) ableitet. Diagnose: prüfen, ob sie mit der
// erwarteten Adresse (aus solana-keygen) übereinstimmt.
// POST /api/v1/swap/keyaddr  Body: { sol_key }
func (s *Server) swapKeyAddress(c *gin.Context) {
	var req struct {
		SolKey string `json:"sol_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Bei einem Mnemonic ALLE gängigen Pfade zeigen, damit man den findet, der
	// zur solana-keygen-Adresse passt.
	addrs := derivedAddresses(req.SolKey)
	req.SolKey = ""
	if len(addrs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kein Mnemonic (oder ungültig)"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"addresses": addrs,
		"note":      "Vergleiche mit 'solana address'. Der passende Pfad ist meist 'cli (roher Seed)'. Der Node nutzt aktuell diesen für die Ausführung.",
	})
}

// ── FND-HTLC-Ausführung (Fundus-Chain-Seite des Swaps) ───────────────────────

// swapFndLock sperrt FND auf der Fundus-Chain mit Hashlock + Timelock.
// POST /api/v1/swap/fnd/lock
// Body: { seed_words, recipient, amount_fnd, secret_hash, timelock_blocks }
func (s *Server) swapFndLock(c *gin.Context) {
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req struct {
		SeedWords      string  `json:"seed_words"      binding:"required"`
		Recipient      string  `json:"recipient"       binding:"required"` // FND-Adresse des Einlösers
		AmountFND      float64 `json:"amount_fnd"      binding:"required"`
		SecretHashHex  string  `json:"secret_hash"     binding:"required"`
		TimelockBlocks uint64  `json:"timelock_blocks"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	recipient, ok := chain.AddressFromHex(req.Recipient)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Empfänger-Adresse"})
		return
	}
	secretHash, err := hash32FromHex(req.SecretHashHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger secret_hash"})
		return
	}
	amount := fndToUFND(req.AmountFND)
	timelock := req.TimelockBlocks
	if timelock == 0 {
		timelock = s.chain.Height() + 17280 // ~24h bei 5s Blockzeit
	} else {
		timelock = s.chain.Height() + timelock
	}
	payload := chain.EncodeHTLCLock(recipient, amount, secretHash, timelock)

	words := strings.Fields(req.SeedWords)
	txHash, from, nonce, err := s.submitChainTx(words, chain.TxHTLCLock, chain.FeeForValue(amount), payload)
	req.SeedWords = ""
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "htlc_id": txHash, "from": from, "nonce": nonce,
		"note": "htlc_id merken — wird zum Einlösen/Zurückholen gebraucht.",
	})
}

// swapFndClaim löst gesperrte FND mit dem Preimage ein.
// POST /api/v1/swap/fnd/claim  Body: { seed_words, htlc_id, secret }
func (s *Server) swapFndClaim(c *gin.Context) {
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req struct {
		SeedWords string `json:"seed_words" binding:"required"`
		HTLCID    string `json:"htlc_id"    binding:"required"`
		SecretHex string `json:"secret"     binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, err := hash32FromHex(req.HTLCID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige htlc_id"})
		return
	}
	secret, err := hash32FromHex(req.SecretHex)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiges secret"})
		return
	}
	payload := chain.EncodeHTLCClaim(id, secret)
	words := strings.Fields(req.SeedWords)
	txHash, from, nonce, err := s.submitChainTx(words, chain.TxHTLCClaim, chain.FeeForValue(nil), payload)
	req.SeedWords = ""
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "tx": txHash, "from": from, "nonce": nonce})
}

// swapFndRefund gibt gesperrte FND nach Ablauf des Timelocks zurück.
// POST /api/v1/swap/fnd/refund  Body: { seed_words, htlc_id }
func (s *Server) swapFndRefund(c *gin.Context) {
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req struct {
		SeedWords string `json:"seed_words" binding:"required"`
		HTLCID    string `json:"htlc_id"    binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	id, err := hash32FromHex(req.HTLCID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige htlc_id"})
		return
	}
	payload := chain.EncodeHTLCRefund(id)
	words := strings.Fields(req.SeedWords)
	txHash, from, nonce, err := s.submitChainTx(words, chain.TxHTLCRefund, chain.FeeForValue(nil), payload)
	req.SeedWords = ""
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "tx": txHash, "from": from, "nonce": nonce})
}

// submitChainTx baut eine signierte Chain-Tx aus Seed-Wörtern und sendet sie.
func (s *Server) submitChainTx(words []string, txType chain.TxType, fee *big.Int, payload []byte) (string, string, uint64, error) {
	if len(words) == 0 {
		return "", "", 0, fmt.Errorf("Seed-Wörter fehlen")
	}
	priv, err := fndKeyFromWords(words) // Seed-Wörter ODER fertiger Schlüssel ("session")
	if err != nil {
		return "", "", 0, fmt.Errorf("Ableitung fehlgeschlagen")
	}
	defer priv.D.SetInt64(0)
	addr := chain.PubkeyToAddress(&priv.PublicKey)
	_, n := s.chain.AccountInfo(addr)
	tx, err := chain.BuildSignedTx(priv, txType, fee, payload, n)
	if err != nil {
		return "", "", 0, err
	}
	if err := s.mempool.Add(tx); err != nil {
		return "", "", 0, err
	}
	h := tx.Hash()
	return hex.EncodeToString(h[:]), addr.Hex(), n, nil
}

// swapAutoStart startet einen automatischen Swap. Der Node übernimmt die Keys
// (im RAM bis zum Abschluss) und führt den Swap selbstständig durch.
// POST /api/v1/swap/auto/start
// Body: { role, sol_key, fnd_seed, counterparty_sol, counterparty_fnd,
//         amount_sol, amount_fnd, secret? }
//   role: "buyer" oder "seller"
//   secret: nur der Käufer liefert es (hex); der Verkäufer bekommt es on-chain.
func (s *Server) swapAutoStart(c *gin.Context) {
	if s.swapMgr == nil || s.orch == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht konfiguriert"})
		return
	}
	var req struct {
		Role            string  `json:"role"             binding:"required"`
		SolKey          string  `json:"sol_key"          binding:"required"`
		FndSeed         string  `json:"fnd_seed"         binding:"required"`
		CounterpartySol string  `json:"counterparty_sol" binding:"required"`
		CounterpartyFnd string  `json:"counterparty_fnd" binding:"required"`
		AmountSOL       float64 `json:"amount_sol"       binding:"required"`
		AmountFND       float64 `json:"amount_fnd"       binding:"required"`
		SecretHex       string  `json:"secret"`
		SecretHashHex   string  `json:"secret_hash"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	solKey, err := s.solKeyInput(c, req.SolKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SOL-Key: " + err.Error()})
		return
	}
	isBuyer := req.Role == "buyer"

	// Übergangs-Mapping: In der bisherigen Richtung (SOL kauft FND) ist der
	// Käufer der Taker, der SOL weggibt; der Verkäufer der Maker, der FND weggibt.
	giveChain := "fnd"
	if isBuyer {
		giveChain = "sol"
	}
	ss := &swapSession{
		swapID:          hex.EncodeToString(randomID()),
		isTaker:         isBuyer,
		giveChain:       giveChain,
		solKey:          solKey,
		fndSeed:         s.fndWords(c, req.FndSeed),
		counterpartySol: req.CounterpartySol,
		counterpartyFnd: req.CounterpartyFnd,
		amountSOL:       req.AmountSOL,
		amountFND:       req.AmountFND,
	}

	// Käufer: erzeugt/liefert das Geheimnis. Verkäufer: nur den Hash.
	if isBuyer {
		if req.SecretHex != "" {
			sec, err := hash32FromHex(req.SecretHex)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "secret ungültig"})
				return
			}
			ss.secret = sec
		} else {
			_, _ = crand.Read(ss.secret[:])
		}
		ss.secretHash = blake3.Sum256(ss.secret[:])
	} else {
		h, err := hash32FromHex(req.SecretHashHex)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "secret_hash ungültig"})
			return
		}
		ss.secretHash = h
	}

	// Swap-Objekt für die Statusanzeige anlegen.
	s.swapMgr.mu.Lock()
	s.swapMgr.swaps[ss.swapID] = &Swap{
		ID: ss.swapID, Phase: SwapInitiated,
		Hashlock:  hex.EncodeToString(ss.secretHash[:]),
		AmountFND: req.AmountFND, AmountSOL: req.AmountSOL,
		CreatedAt: time.Now().Unix(),
	}
	s.swapMgr.mu.Unlock()

	s.orch.startSwap(ss)

	resp := gin.H{"ok": true, "swap_id": ss.swapID, "hashlock": hex.EncodeToString(ss.secretHash[:])}
	// Der Käufer teilt dem Verkäufer den Hashlock mit (über den Rückkanal/Order).
	c.JSON(http.StatusOK, resp)
}

// randomID erzeugt 16 Zufalls-Bytes für eine Swap-ID.
func randomID() []byte {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return b
}

// swapHTLCList listet alle FND-HTLCs (Diagnose): Hashlock, Empfänger, Zustand.
// Damit sieht man, ob ein Lock ankommt und ob Hashlock/Empfänger stimmen.
// GET /api/v1/swap/htlcs
func (s *Server) swapHTLCList(c *gin.Context) {
	if s.chain == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	list := s.chain.ListHTLCs()
	out := make([]gin.H, 0, len(list))
	// Optional ?sender=0x… : nur Sperren dieser Adresse
	var onlySender chain.Address
	filter := false
	if q := c.Query("sender"); q != "" {
		if a, ok := chain.AddressFromHex(q); ok {
			onlySender, filter = a, true
		}
	}
	for _, h := range list {
		if filter && h.Sender != onlySender {
			continue
		}
		amt := "0"
		if h.Amount != nil {
			amt = uFNDToFND(h.Amount.String())
		}
		out = append(out, gin.H{
			"amount_fnd": amt,
			"id":        hex.EncodeToString(h.ID[:]),
			"sender":    h.Sender.Hex(),
			"recipient": h.Recipient.Hex(),
			"hashlock":  hex.EncodeToString(h.Hashlock[:]),
			"timelock":  h.Timelock,
			"state":     int(h.State),
			"preimage":  hex.EncodeToString(h.Preimage[:]),
		})
	}
	c.JSON(http.StatusOK, gin.H{"htlcs": out, "height": s.chain.Height()})
}

// swapDepositKeys hinterlegt die Verkäufer-Schlüssel für eine Order (im RAM,
// bis erfüllt/gecancelt). Der Verkäufer ruft das beim Einstellen der Order auf.
// POST /api/v1/swap/deposit  Body: { order_id, sol_key, fnd_seed, amount_sol, amount_fnd }
func (s *Server) swapDepositKeys(c *gin.Context) {
	if s.swapCoord == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap-Koordination nicht aktiv"})
		return
	}
	var req struct {
		OrderID   string  `json:"order_id"   binding:"required"`
		SolKey    string  `json:"sol_key"`
		FndSeed   string  `json:"fnd_seed"`
		AmountSOL float64 `json:"amount_sol"`
		AmountFND float64 `json:"amount_fnd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.SolKey == "" && req.FndSeed == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mindestens ein Schlüssel (SOL oder FND) nötig"})
		return
	}
	// Nur den/die tatsächlich übergebenen Schlüssel ableiten.
	var solKey solana.PrivateKey
	if req.SolKey != "" {
		var err error
		solKey, err = s.solKeyInput(c, req.SolKey)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "SOL-Key: " + err.Error()})
			return
		}
	}
	k := &depositedKeys{
		solKey:    solKey,
		fndSeed:   s.fndWords(c, req.FndSeed),
		amountSOL: req.AmountSOL,
		amountFND: req.AmountFND,
	}
	s.swapCoord.depositKeys(req.OrderID, k)
	req.SolKey, req.FndSeed = "", ""
	c.JSON(http.StatusOK, gin.H{"ok": true, "note": "Schlüssel hinterlegt, bis Order erfüllt/gecancelt."})
}

// swapBuy löst den Kauf einer Order aus (Käufer-Seite). Erzeugt das Geheimnis,
// benachrichtigt den Verkäufer-Node per P2P und startet die eigene Seite.
// POST /api/v1/swap/buy
// Body: { order_id, sol_key, fnd_seed, buyer_sol, buyer_fnd }
func (s *Server) swapBuy(c *gin.Context) {
	if s.swapCoord == nil || s.orderBook == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap/Orderbuch nicht aktiv"})
		return
	}
	var req struct {
		OrderID   string  `json:"order_id"   binding:"required"`
		SolKey    string  `json:"sol_key"`
		FndSeed   string  `json:"fnd_seed"`
		TakerSol  string  `json:"taker_sol"`
		TakerFnd  string  `json:"taker_fnd"`
		AmountFND float64 `json:"amount_fnd"` // gewünschte Teilmenge (0 = ganze Order)
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	order, ok := s.orderBook.FindOrder(req.OrderID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Order nicht gefunden"})
		return
	}

	// Teilmenge: nicht mehr als die Order hergibt; 0 → ganze Order.
	amountFND := order.AmountFND
	if req.AmountFND > 0 && req.AmountFND < order.AmountFND {
		amountFND = req.AmountFND
	}

	// Richtung aus der Order-Side ableiten:
	//  Maker "sell" (gibt FND, will SOL) → Taker gibt SOL.
	//  Maker "buy"  (gibt SOL, will FND) → Taker gibt FND.
	takerGivesSol := order.Side == OrderSell

	// Der Taker braucht BEIDE Schlüssel: den Give-Key zum Sperren (Zahlen) und
	// den Receive-Key zum Einlösen der gekauften Werte (Claim braucht Signatur!).
	if req.SolKey == "" || req.FndSeed == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Solana-Schlüssel UND FND-Seed nötig (beide Ketten werden signiert)"})
		return
	}
	solKey, err := s.solKeyInput(c, req.SolKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SOL-Key: " + err.Error()})
		return
	}
	fndAddr, okf := s.fndAddressFromSeed(s.fndWords(c, req.FndSeed))
	if !okf {
		c.JSON(http.StatusBadRequest, gin.H{"error": "FND-Seed ungültig"})
		return
	}

	// Deckung prüfen, BEVOR irgendetwas gesperrt wird: der Annehmende sperrt
	// zuerst – fehlt dem Anbieter das Guthaben, wären seine Mittel ~48 h gebunden.
	if fe := s.checkTakeFunds(c.Request.Context(), order, amountFND, takerGivesSol,
		solKey.PublicKey().String(), fndAddr, ""); fe != nil {
		req.SolKey, req.FndSeed = "", ""
		c.JSON(http.StatusBadRequest, gin.H{"error": fe.Msg, "funds": fe.Detail})
		return
	}

	// Geheimnis erzeugen (der Taker erzeugt es immer).
	var secret [32]byte
	_, _ = crand.Read(secret[:])
	hash := blake3.Sum256(secret[:])

	amountSOL := amountFND * order.PriceSOL

	// Beide Taker-Adressen aus den Keys ableiten — garantiert konsistent mit den
	// Schlüsseln, mit denen gesperrt/eingelöst wird.
	takerSolAddr := solKey.PublicKey().String()
	takerFndAddr := fndAddr

	msg := swapInitMsg{
		OrderID:   req.OrderID,
		Hashlock:  hex.EncodeToString(hash[:]),
		BuyerSol:  takerSolAddr,
		BuyerFnd:  takerFndAddr,
		AmountSOL: amountSOL,
		AmountFND: amountFND,
		TakerGivesSol: takerGivesSol,
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	err = s.swapCoord.triggerRemoteSwap(ctx, s.orderBook.Node(), order.MakerPeer, msg,
		solKey, s.fndWords(c, req.FndSeed), secret, order.SolAddress, order.FndAddress, takerGivesSol, "")
	req.SolKey, req.FndSeed = "", ""
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Gegenseite nicht erreichbar: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "swap_id": "buyer-" + req.OrderID, "hashlock": hex.EncodeToString(hash[:])})
}

// swapListAll listet alle Swaps mit Phase und Notiz (Diagnose).
// GET /api/v1/swap/all
func (s *Server) swapListAll(c *gin.Context) {
	if s.swapMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap nicht aktiv"})
		return
	}
	s.swapMgr.mu.RLock()
	out := make([]gin.H, 0, len(s.swapMgr.swaps))
	for _, sw := range s.swapMgr.swaps {
		out = append(out, gin.H{
			"id": sw.ID, "order_id": sw.OrderID, "phase": sw.Phase,
			"note": sw.Note, "amount_fnd": sw.AmountFND, "amount_sol": sw.AmountSOL,
			"hashlock": sw.Hashlock, "created_at": sw.CreatedAt,
		})
	}
	s.swapMgr.mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{"swaps": out})
}

// swapDeposits zeigt, für welche Order-IDs Verkäufer-Keys hinterlegt sind
// (Diagnose — zeigt KEINE Schlüssel, nur die Order-IDs + Beträge).
// GET /api/v1/swap/deposits
func (s *Server) swapDeposits(c *gin.Context) {
	if s.swapCoord == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Swap-Koordination nicht aktiv"})
		return
	}
	s.swapCoord.mu.Lock()
	out := make([]gin.H, 0, len(s.swapCoord.deposits))
	for orderID, k := range s.swapCoord.deposits {
		out = append(out, gin.H{
			"order_id": orderID, "amount_sol": k.amountSOL, "amount_fnd": k.amountFND,
			"has_sol_key": len(k.solKey) > 0, "has_fnd_seed": len(k.fndSeed) > 0,
		})
	}
	s.swapCoord.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"deposits": out})
}

// swapDeriveAddrs leitet aus einem SOL-Key und/oder FND-Seed die zugehörigen
// Adressen ab. Damit füllt das Order-Formular die Adressfelder passend zu den
// hinterlegten Keys — so können Order-Adresse und Signier-Adresse nicht abweichen.
// POST /api/v1/swap/derive-addrs  Body: { sol_key?, fnd_seed? }
func (s *Server) swapDeriveAddrs(c *gin.Context) {
	var req struct {
		SolKey  string `json:"sol_key"`
		FndSeed string `json:"fnd_seed"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out := gin.H{}
	if req.SolKey != "" {
		if key, err := s.solKeyInput(c, req.SolKey); err == nil {
			out["sol_address"] = key.PublicKey().String()
		} else {
			out["sol_error"] = err.Error()
		}
	}
	if req.FndSeed != "" {
		if addr, ok := s.fndAddressFromSeed(s.fndWords(c, req.FndSeed)); ok {
			out["fnd_address"] = addr
		} else {
			out["fnd_error"] = "FND-Seed ungültig"
		}
	}
	req.SolKey, req.FndSeed = "", ""
	c.JSON(http.StatusOK, out)
}

// =============================================================================
//  Persistenz laufender Swaps
// =============================================================================

// swapKeepDays: abgeschlossene/alte Swaps werden nach dieser Zeit verworfen.
const swapKeepDays = 30

// enablePersistence lädt gespeicherte Swaps und sichert Änderungen alle 3 s.
// Ein Snapshot-Takt statt Speichern an jeder Phasen-Stelle: die Phasen werden
// an vielen Stellen gesetzt; so kann keine vergessen werden. Maximal 3 s
// Datenverlust bei einem Absturz; die HTLC-IDs stehen ohnehin auf den Chains.
func (m *SwapManager) enablePersistence(path string) {
	if m == nil || path == "" {
		return
	}
	m.path = path
	var saved []*Swap
	if err := readJSONFile(path, &saved); err == nil {
		cutoff := time.Now().AddDate(0, 0, -swapKeepDays).Unix()
		m.mu.Lock()
		for _, sw := range saved {
			if sw != nil && sw.ID != "" && sw.CreatedAt >= cutoff {
				if _, exists := m.swaps[sw.ID]; !exists {
					m.swaps[sw.ID] = sw
				}
			}
		}
		m.mu.Unlock()
	}
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for range t.C {
			m.snapshot()
		}
	}()
}

func (m *SwapManager) snapshot() {
	cutoff := time.Now().AddDate(0, 0, -swapKeepDays).Unix()
	m.mu.Lock()
	list := make([]*Swap, 0, len(m.swaps))
	for id, sw := range m.swaps {
		if sw.CreatedAt > 0 && sw.CreatedAt < cutoff {
			delete(m.swaps, id)
			continue
		}
		cp := *sw
		list = append(list, &cp)
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil || bytes.Equal(data, m.lastSnap) {
		return
	}
	if atomicWriteBytes(m.path, data) == nil {
		m.lastSnap = data
	}
}
