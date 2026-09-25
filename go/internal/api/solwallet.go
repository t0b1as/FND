package api

// Solana-Wallet im Node.
//
// Abgeleitet aus derselben FND-Wallet (und damit aus denselben Seed-Wörtern):
//   Ed25519-Seed = BLAKE3("fundus-sol-v1:" || secp256k1-Wallet-Schlüssel)
// Dieselben Wörter ergeben auf jedem Node dieselbe Solana-Adresse; kein
// zusätzlicher Argon2-Durchlauf, keine zusätzliche Speicherung (bei
// hinterlegter Wallet sofort verfügbar). Die 30-Wörter-Seed ist KEIN
// BIP39-Mnemonic (12–24 Wörter) – für Phantom & Co. gibt es den Export.
//
//   GET  /api/v1/wallet/sol          Adresse + Guthaben
//   POST /api/v1/wallet/sol/send     {to, amount_sol}
//   POST /api/v1/wallet/sol/export   Base58-Schlüssel (Phantom-Import)
//
// In Swap/Shop-Aufrufen bedeutet sol_key = "session": diese Wallet.

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/gin-gonic/gin"
	"lukechampine.com/blake3"

	"github.com/fundus/node/internal/identity"
)

const (
	solTxFeeLamports   = 5000
	solRentMinLamports = 890880 // Mindestreserve eines neuen (leeren) Kontos
)

// solKeyFromChain leitet den Solana-Schlüssel aus dem FND-Wallet-Schlüssel ab.
func solKeyFromChain(k *ecdsa.PrivateKey) solana.PrivateKey {
	raw := crypto.FromECDSA(k)
	h := blake3.Sum256(append([]byte("fundus-sol-v1:"), raw...))
	for i := range raw {
		raw[i] = 0
	}
	return solana.PrivateKey(ed25519.NewKeyFromSeed(h[:]))
}

// sessionSolKey: Solana-Schlüssel der angemeldeten Wallet (hinterlegt → sofort,
// sonst einmalige Ableitung ~10 s).
func (s *Server) sessionSolKey(c *gin.Context) (solana.PrivateKey, error) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		return nil, fmt.Errorf("Nicht angemeldet")
	}
	k, err := sess.identity.ChainPrivateKey()
	if err != nil {
		return nil, err
	}
	defer k.D.SetInt64(0)
	return solKeyFromChain(k), nil
}

// solKeyInput: "session" = Wallet der Anmeldung, sonst Keygen-JSON/Mnemonic/Base58.
func (s *Server) solKeyInput(c *gin.Context, input string) (solana.PrivateKey, error) {
	if strings.EqualFold(strings.TrimSpace(input), "session") {
		return s.sessionSolKey(c)
	}
	return parseSolKey(input)
}

func (s *Server) solRPCURL() string {
	if s.swapMgr != nil && s.swapMgr.solRPC != "" {
		return s.swapMgr.solRPC
	}
	if s.cfg != nil && s.cfg.ShopSolanaRPC != "" {
		return s.cfg.ShopSolanaRPC
	}
	return "https://api.mainnet-beta.solana.com"
}

func (s *Server) solGetBalance(ctx context.Context, pk solana.PublicKey) (uint64, error) {
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	res, err := rpc.New(s.solRPCURL()).GetBalance(cctx, pk, rpc.CommitmentConfirmed)
	if err != nil || res == nil {
		return 0, solRPCErr(err, s.solRPCURL())
	}
	return res.Value, nil
}

// maskRPC: RPC-Adresse ohne Query (API-Schlüssel von Helius/QuickNode stehen
// oft in ?api-key=… oder im Pfad nach dem Host) – für Anzeigen/Antworten.
func maskRPC(rpcURL string) string {
	u, err := url.Parse(rpcURL)
	if err != nil || u.Host == "" {
		return "(RPC)"
	}
	out := u.Scheme + "://" + u.Host
	if u.Path != "" && u.Path != "/" {
		out += "/…" // Pfad kann ebenfalls einen Schlüssel enthalten (QuickNode)
	}
	return out
}

// solRPCErr übersetzt RPC-Fehler in eine verständliche Ursache (statt der
// Sammelmeldung "nicht erreichbar") und nennt den Endpunkt – OHNE API-Schlüssel.
func solRPCErr(err error, rpcURL string) error {
	host := maskRPC(rpcURL)
	if err == nil {
		return fmt.Errorf("Solana-RPC (%s): leere Antwort", host)
	}
	e := strings.ToLower(err.Error())
	var why string
	switch {
	case strings.Contains(e, "429") || strings.Contains(e, "too many"):
		why = "Drosselung durch den RPC-Anbieter (zu viele Anfragen) – eigenen RPC-Zugang verwenden (Helius, QuickNode …)"
	case strings.Contains(e, "x509") || strings.Contains(e, "certificate"):
		why = "Zertifikatsfehler – stimmt die Uhrzeit des Pi? (timedatectl)"
	case strings.Contains(e, "no such host") || strings.Contains(e, "server misbehaving"):
		why = "Name nicht auflösbar (DNS/Internet des Pi prüfen)"
	case strings.Contains(e, "deadline") || strings.Contains(e, "timeout"):
		why = "Zeitüberschreitung (RPC überlastet oder Verbindung langsam)"
	case strings.Contains(e, "connection refused"):
		why = "Verbindung abgewiesen (falsche Adresse/Port?)"
	case strings.Contains(e, "401") || strings.Contains(e, "403") || strings.Contains(e, "unauthorized"):
		why = "Zugriff verweigert (API-Schlüssel des RPC-Zugangs prüfen)"
	default:
		why = truncate(err.Error(), 140)
	}
	return fmt.Errorf("Solana-RPC (%s): %s", host, why)
}

// GET /api/v1/wallet/sol
func (s *Server) walletSolInfo(c *gin.Context) {
	key, err := s.sessionSolKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	pub := key.PublicKey()
	out := gin.H{"address": pub.String(), "rpc": maskRPC(s.solRPCURL()), "cluster": s.solCluster()}
	if bal, err := s.solGetBalance(c.Request.Context(), pub); err == nil {
		out["lamports"] = bal
		out["sol"] = float64(bal) / 1e9
	} else {
		out["balance_error"] = err.Error()
	}
	c.JSON(http.StatusOK, out)
}

// POST /api/v1/wallet/sol/send {to, amount_sol}
func (s *Server) walletSolSend(c *gin.Context) {
	var req struct {
		To        string  `json:"to"`
		AmountSOL float64 `json:"amount_sol"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	to, err := solana.PublicKeyFromBase58(strings.TrimSpace(req.To))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültige Solana-Adresse"})
		return
	}
	if req.AmountSOL <= 0 || req.AmountSOL > 1e7 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Betrag"})
		return
	}
	lamports := uint64(math.Round(req.AmountSOL * 1e9))
	key, err := s.sessionSolKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	from := key.PublicKey()
	if from.Equals(to) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Absender und Empfänger sind gleich"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	bal, err := s.solGetBalance(ctx, from)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if bal < lamports+solTxFeeLamports {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Nicht genug SOL: %.6f vorhanden, %.6f nötig (inkl. Gebühr)",
			float64(bal)/1e9, float64(lamports+solTxFeeLamports)/1e9)})
		return
	}
	// Neues (leeres) Empfängerkonto braucht mindestens die Mindestreserve.
	if tb, err := s.solGetBalance(ctx, to); err == nil && tb == 0 && lamports < solRentMinLamports {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf(
			"Das Empfängerkonto ist neu: Solana verlangt dafür mindestens %.6f SOL (Mindestreserve)", float64(solRentMinLamports)/1e9)})
		return
	}
	client := rpc.New(s.solRPCURL())
	recent, err := client.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Blockhash holen fehlgeschlagen: " + err.Error()})
		return
	}
	tx, err := solana.NewTransaction(
		[]solana.Instruction{system.NewTransferInstruction(lamports, from, to).Build()},
		recent.Value.Blockhash, solana.TransactionPayer(from))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := tx.Sign(func(k solana.PublicKey) *solana.PrivateKey {
		if k.Equals(from) {
			return &key
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Signieren fehlgeschlagen: " + err.Error()})
		return
	}
	sig, err := client.SendTransaction(ctx, tx)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Senden fehlgeschlagen: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "signature": sig.String(), "from": from.String(), "to": to.String(),
		"amount_sol": float64(lamports) / 1e9, "cluster": s.solCluster()})
}

// POST /api/v1/wallet/sol/export – Base58-Schlüssel für Phantom/Solflare.
func (s *Server) walletSolExport(c *gin.Context) {
	key, err := s.sessionSolKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"address": key.PublicKey().String(), "secret_base58": key.String()})
}

// ── FND-Seite im Shop: Wallet der Anmeldung statt Seed-Wörtern ────────────

// fndKeyPrefix: eine "Seed" aus EINEM Eintrag mit diesem Präfix trägt statt
// Wörtern einen fertigen Schlüssel (aus der hinterlegten Wallet). Der gesamte
// Swap-Ablauf reicht die Wortliste nur weiter; umgewandelt wird ausschließlich
// in fndKeyFromWords (Sperren/Einlösen/Rückholen/Adresse).
const fndKeyPrefix = "fundus-key:"

func fndKeyFromWords(words []string) (*ecdsa.PrivateKey, error) {
	if len(words) == 1 && strings.HasPrefix(words[0], fndKeyPrefix) {
		b, err := hex.DecodeString(strings.TrimPrefix(words[0], fndKeyPrefix))
		if err != nil {
			return nil, fmt.Errorf("FND-Schlüssel ungültig")
		}
		defer func() {
			for i := range b {
				b[i] = 0
			}
		}()
		return crypto.ToECDSA(b)
	}
	return identity.DerivePrivateKeyFromSeed(words)
}

// fndWords: "session" = Wallet der Anmeldung, sonst die eingegebenen Wörter.
func (s *Server) fndWords(c *gin.Context, input string) []string {
	if !strings.EqualFold(strings.TrimSpace(input), "session") {
		return strings.Fields(input)
	}
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		return nil
	}
	k, err := sess.identity.ChainPrivateKey()
	if err != nil {
		return nil
	}
	raw := crypto.FromECDSA(k)
	k.D.SetInt64(0)
	w := []string{fndKeyPrefix + hex.EncodeToString(raw)}
	for i := range raw {
		raw[i] = 0
	}
	return w
}

// solCluster: "devnet"/"testnet" für Explorer-Links, sonst "" (Mainnet).
func (s *Server) solCluster() string {
	u := strings.ToLower(s.solRPCURL())
	switch {
	case strings.Contains(u, "devnet"):
		return "devnet"
	case strings.Contains(u, "testnet"):
		return "testnet"
	}
	return ""
}
