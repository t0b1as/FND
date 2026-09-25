package api

// Wallet öffnen, für den Login hinterlegen, alte Guthaben umziehen.
//
// Seit R456 leitet der Node Nutzer-Wallets wie fnd-wallet ab (Argon2id 256 MiB,
// t=4): dieselben Seed-Wörter ergeben überall dieselbe Adresse. Das dauert auf
// dem Pi ~10 s und läuft deshalb NICHT beim Login, sondern nur bei:
//   POST   /api/v1/wallet/open     Wallet ableiten (eigene Login-Wörter oder andere)
//   POST   /api/v1/wallet/link     für diesen Login hinterlegen (verschlüsselt, lokal)
//   DELETE /api/v1/wallet/link     Hinterlegung aufheben
//   POST   /api/v1/wallet/migrate  Guthaben der alten Adresse (128 MiB, vor R456)
//                                  auf die neue Adresse übertragen
//
// Die Hinterlegung ist mit einem Schlüssel verschlüsselt, den nur diese Login-
// Identität erzeugen kann (Identity.LinkKey), und wird nie ins Netz verteilt.

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/storage"
)

func walletLinkID(fid string) string { return "walletlink:" + strings.ToLower(fid) }

// loadWalletLink stellt die hinterlegte Wallet einer Identität her (beim
// Aufbau der Sitzung). Kein Argon2 – der Schlüssel liegt verschlüsselt vor.
func (s *Server) loadWalletLink(id *identity.Identity) {
	if s.store == nil || id == nil {
		return
	}
	rec, err := s.store.Get(storage.RecordWalletLink, walletLinkID(id.FundusID))
	if err != nil || rec == nil {
		return
	}
	enc, _ := rec.Data["enc"].(string)
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return
	}
	plain, err := identity.Decrypt(raw, id.LinkKey())
	if err != nil || len(plain) != 32 {
		return
	}
	k, err := crypto.ToECDSA(plain)
	for i := range plain {
		plain[i] = 0
	}
	if err == nil {
		id.SetChainKey(k)
	}
}

// walletKeyFromRequest: Schlüssel aus eingegebenen Wörtern/Email+PW, sonst aus
// den Login-Wörtern der Sitzung. derive = Ableitungsfunktion (neu/alt).
func (s *Server) walletKeyFromRequest(c *gin.Context, words []string, email, password string,
	derive func([]string) (*ecdsa.PrivateKey, error)) (*ecdsa.PrivateKey, *Session, int, string) {
	sess := s.getSession(c)
	if len(words) == 0 && email == "" {
		if sess == nil || sess.identity == nil {
			return nil, nil, http.StatusUnauthorized, "Nicht angemeldet"
		}
		words = sess.identity.SeedWords()
		if len(words) == 0 {
			return nil, sess, http.StatusBadRequest, "Keine Seed-Wörter in der Sitzung – bitte neu anmelden"
		}
	} else {
		w, err := resolveSeed(words, email, password)
		if err != nil {
			return nil, sess, http.StatusBadRequest, err.Error()
		}
		words = w
	}
	k, err := derive(words)
	if err != nil {
		return nil, sess, http.StatusInternalServerError, err.Error()
	}
	return k, sess, 0, ""
}

type walletReq struct {
	Words    []string `json:"words"`
	Email    string   `json:"email"`
	Password string   `json:"password"`
}

// POST /api/v1/wallet/open
func (s *Server) walletOpen(c *gin.Context) {
	if s.chain == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req walletReq
	_ = c.ShouldBindJSON(&req)
	own := len(req.Words) == 0 && req.Email == ""
	k, sess, code, msg := s.walletKeyFromRequest(c, req.Words, req.Email, req.Password, identity.DerivePrivateKeyFromSeed)
	if k == nil {
		c.JSON(code, gin.H{"error": msg})
		return
	}
	addr := chain.PubkeyToAddress(&k.PublicKey)
	bal, _ := s.chain.AccountInfo(addr)

	// Alte Adresse (vor R456, 128 MiB) mit Guthaben?
	oldK, _, _, _ := s.walletKeyFromRequest(c, req.Words, req.Email, req.Password, identity.DeriveNodePrivateKeyFromSeed)
	out := gin.H{"address": addr.Hex(), "fnd": uFNDToFND(bal)}
	if oldK != nil {
		oldAddr := chain.PubkeyToAddress(&oldK.PublicKey)
		oldBal, _ := s.chain.AccountInfo(oldAddr)
		oldK.D.SetInt64(0)
		if b, ok := new(big.Int).SetString(oldBal, 10); ok && b.Sign() > 0 && oldAddr != addr {
			out["old_address"] = oldAddr.Hex()
			out["old_fnd"] = uFNDToFND(oldBal)
		}
	}
	linked := ""
	if sess != nil && sess.identity != nil {
		linked = s.linkedAddress(sess.identity)
	}
	if own && sess != nil && sess.identity != nil && linked == "" {
		// Eigene Wallet für diese Sitzung merken (nur wenn keine andere
		// hinterlegt ist – die hinterlegte hat Vorrang).
		sess.identity.SetChainKey(k)
	} else {
		k.D.SetInt64(0) // nur angesehen → Schlüssel verwerfen
	}
	out["linked_address"] = linked
	out["is_linked"] = linked != "" && strings.EqualFold(linked, addr.Hex())
	out["own"] = own
	c.JSON(http.StatusOK, out)
}

func (s *Server) linkedAddress(id *identity.Identity) string {
	if s.store == nil || id == nil {
		return ""
	}
	if rec, err := s.store.Get(storage.RecordWalletLink, walletLinkID(id.FundusID)); err == nil && rec != nil {
		a, _ := rec.Data["address"].(string)
		return a
	}
	return ""
}

// POST /api/v1/wallet/link – Wallet (eigene oder andere) für den Login hinterlegen.
func (s *Server) walletLink(c *gin.Context) {
	var req walletReq
	_ = c.ShouldBindJSON(&req)
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	// Eigene Wallet, schon in dieser Sitzung abgeleitet ("Wallet öffnen")? Dann
	// den vorhandenen Schlüssel nehmen – eine zweite 256-MiB-Ableitung trieb den
	// Pi in die Auslagerung. Nur wenn KEINE andere Wallet hinterlegt ist, ist der
	// Sitzungsschlüssel sicher die eigene.
	var k *ecdsa.PrivateKey
	own := len(req.Words) == 0 && req.Email == ""
	if own && s.linkedAddress(sess.identity) == "" && sess.identity.ChainAddr() != "" {
		if kk, err := sess.identity.ChainPrivateKey(); err == nil {
			k = kk
		}
	}
	if k == nil {
		var code int
		var msg string
		k, _, code, msg = s.walletKeyFromRequest(c, req.Words, req.Email, req.Password, identity.DerivePrivateKeyFromSeed)
		if k == nil {
			c.JSON(code, gin.H{"error": msg})
			return
		}
	}
	raw := crypto.FromECDSA(k)
	enc, err := identity.Encrypt(raw, sess.identity.LinkKey())
	for i := range raw {
		raw[i] = 0
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	addr := chain.PubkeyToAddress(&k.PublicKey).Hex()
	if err := s.store.Put(&storage.Record{ID: walletLinkID(sess.identity.FundusID), Type: storage.RecordWalletLink,
		Data: map[string]any{"address": addr, "enc": base64.StdEncoding.EncodeToString(enc)}}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	sess.identity.SetChainKey(k)
	s.ensureKeyDir(c, sess.identity) // Verzeichnis: Fundus-ID ↔ Wallet-Adresse
	if tok, err := c.Cookie("fundus_session"); err == nil {
		s.persistSession(tok, sess.identity) // Neustart-fest inkl. Wallet
	}
	if s.log != nil {
		s.log.Info("Wallet für Login hinterlegt", zap.String("fid", sess.identity.FundusID[:10]+"…"), zap.String("wallet", addr))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "address": addr})
}

// DELETE /api/v1/wallet/link
func (s *Server) walletUnlink(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Nicht angemeldet"})
		return
	}
	_ = s.store.Delete(storage.RecordWalletLink, walletLinkID(sess.identity.FundusID))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// POST /api/v1/wallet/migrate – Guthaben der alten Adresse (128 MiB, vor R456)
// auf die neue Adresse derselben Wörter übertragen (Gebühr wird abgezogen).
func (s *Server) walletMigrate(c *gin.Context) {
	if s.chain == nil || s.mempool == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req walletReq
	_ = c.ShouldBindJSON(&req)
	oldK, _, code, msg := s.walletKeyFromRequest(c, req.Words, req.Email, req.Password, identity.DeriveNodePrivateKeyFromSeed)
	if oldK == nil {
		c.JSON(code, gin.H{"error": msg})
		return
	}
	defer oldK.D.SetInt64(0)
	newK, _, code, msg := s.walletKeyFromRequest(c, req.Words, req.Email, req.Password, identity.DerivePrivateKeyFromSeed)
	if newK == nil {
		c.JSON(code, gin.H{"error": msg})
		return
	}
	to := chain.PubkeyToAddress(&newK.PublicKey)
	newK.D.SetInt64(0)
	from := chain.PubkeyToAddress(&oldK.PublicKey)
	if from == to {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Alte und neue Adresse sind gleich – nichts umzuziehen"})
		return
	}
	balStr, nonce := s.chain.AccountInfo(from)
	bal, ok := new(big.Int).SetString(balStr, 10)
	if !ok || bal.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Auf der alten Adresse liegt kein Guthaben"})
		return
	}
	amount := chain.NetFromGross(bal) // gesamtes Guthaben abzüglich Gebühr
	if amount.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Guthaben zu klein für die Gebühr"})
		return
	}
	tx, err := chain.BuildSignedTransfer(oldK, to, amount, nonce)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.mempool.Add(tx); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h := tx.Hash()
	s.broadcastTx(c.Request.Context(), tx)
	if _, _, perr := s.submitOrProduce(c.Request.Context()); perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Transaktion abgelehnt: " + perr.Error(), "tx_hash": hex.EncodeToString(h[:])})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "tx_hash": hex.EncodeToString(h[:]),
		"from": from.Hex(), "to": to.Hex(), "amount": uFNDToFND(amount.String())})
}
