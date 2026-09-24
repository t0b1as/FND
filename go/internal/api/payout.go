package api

// Automatische Auszahlung der Node-Einnahmen (Storage-Rewards + Produzenten-
// Gebührenanteile) an eine vom Betreiber gewählte Zielwallet.
//
// Die Node-Wallet verdient FND, hat aber keine Seed-Wörter — der Betreiber kann
// sie nur ansehen, nicht direkt ausgeben. Damit der Verdienst nutzbar wird,
// überweist der Node ihn periodisch selbst an eine Zieladresse, die der Betreiber
// festlegt. Zwei Auslöser stehen zur Wahl:
//   - Schwelle: sobald der Saldo einen Betrag erreicht, wird (fast) alles über-
//     wiesen (eine kleine Reserve für die Gebühr bleibt).
//   - Frequenz: in festen Zeitabständen, sofern genug Saldo für eine sinnvolle
//     Überweisung da ist.
//
// Der Node signiert die Transfer-Tx mit seinem eigenen Schlüssel (derselbe, mit
// dem er Blöcke signiert) und legt sie in den Mempool — der zuständige Proposer
// baut sie ein. Der Payout läuft in einem eigenen, langsamen Loop und stört die
// Block-Produktion nicht.

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fundus/node/internal/chain"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// PayoutMode bestimmt den Auslöser der Auszahlung.
type PayoutMode string

const (
	PayoutOff       PayoutMode = ""          // deaktiviert
	PayoutThreshold PayoutMode = "threshold" // bei Saldo >= Betrag
	PayoutInterval  PayoutMode = "interval"  // in festen Zeitabständen
)

// PayoutConfig ist die vom Betreiber gesetzte Auszahlungs-Einstellung.
type PayoutConfig struct {
	Target       string     `json:"target"`         // Ziel-Adresse (0x…)
	Mode         PayoutMode `json:"mode"`           // "" | threshold | interval
	ThresholdFND float64    `json:"threshold_fnd"`  // bei mode=threshold: Auslöse-Saldo
	IntervalHrs  float64    `json:"interval_hrs"`   // bei mode=interval: Abstand in Stunden
	MinFND       float64    `json:"min_fnd"`        // Mindestbetrag, unter dem nie ausgezahlt wird
	LastPayout   int64      `json:"last_payout"`    // Unix-Zeit der letzten Auszahlung
}

// payoutManager hält die Config und den Loop-Zustand.
type payoutManager struct {
	mu   sync.RWMutex
	cfg  PayoutConfig
	path string
	srv  *Server
	log  *zap.Logger
}

func newPayoutManager(dataDir string, srv *Server, log *zap.Logger) *payoutManager {
	pm := &payoutManager{
		path: filepath.Join(dataDir, "payout.json"),
		srv:  srv,
		log:  log,
	}
	pm.load()
	return pm
}

func (pm *payoutManager) get() PayoutConfig {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.cfg
}

func (pm *payoutManager) set(cfg PayoutConfig) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.cfg = cfg
	return pm.persistLocked()
}

func (pm *payoutManager) persistLocked() error {
	data, err := json.MarshalIndent(pm.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := pm.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, pm.path)
}

func (pm *payoutManager) load() {
	data, err := os.ReadFile(pm.path)
	if err != nil {
		return
	}
	var c PayoutConfig
	if json.Unmarshal(data, &c) == nil {
		pm.cfg = c
	}
}

// run ist der Payout-Loop. Er prüft alle 60s, ob eine Auszahlung fällig ist.
func (pm *payoutManager) run(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.tick()
		}
	}
}

// tick prüft die Auszahlungsbedingung und löst ggf. eine Überweisung aus.
func (pm *payoutManager) tick() {
	cfg := pm.get()
	if cfg.Mode == PayoutOff || cfg.Target == "" {
		return
	}
	target, ok := chain.AddressFromHex(cfg.Target)
	if !ok {
		pm.log.Warn("Payout: ungültige Zieladresse", zap.String("target", cfg.Target))
		return
	}
	// Node-Schlüssel + Adresse holen. Ohne Schlüssel (gesperrt) kein Payout.
	if pm.srv.fileStore == nil || pm.srv.chain == nil {
		return
	}
	key, addrHex := pm.srv.fileStore.ConsensusSigner()
	if key == nil {
		return
	}
	self, ok := chain.AddressFromHex(addrHex)
	if !ok || self == target {
		return // nie an sich selbst auszahlen
	}

	balStr, nonce := pm.srv.chain.AccountInfo(self)
	balance, ok := new(big.Int).SetString(balStr, 10)
	if !ok || balance.Sign() <= 0 {
		return
	}

	// Mindestbetrag (Untergrenze, unter der sich Auszahlen nie lohnt).
	minUFND := fndToUFND(cfg.MinFND)
	if minUFND.Sign() <= 0 {
		minUFND = fndToUFND(1) // Default: nie unter 1 FND auszahlen
	}

	// Auslöser prüfen.
	switch cfg.Mode {
	case PayoutThreshold:
		thr := fndToUFND(cfg.ThresholdFND)
		if thr.Sign() <= 0 || balance.Cmp(thr) < 0 {
			return // Schwelle noch nicht erreicht
		}
	case PayoutInterval:
		if cfg.IntervalHrs <= 0 {
			return
		}
		elapsed := time.Since(time.Unix(cfg.LastPayout, 0))
		if elapsed < time.Duration(cfg.IntervalHrs*float64(time.Hour)) {
			return // Intervall noch nicht um
		}
		if balance.Cmp(minUFND) < 0 {
			return // zu wenig, um sich zu lohnen
		}
	default:
		return
	}

	// Auszuzahlender Netto-Betrag: gesamter Saldo minus Gebühr. Der Saldo ist der
	// Brutto-Topf; NetFromGross liefert den größten Betrag, für den Betrag+Gebühr
	// noch in den Saldo passt.
	net := chain.NetFromGross(balance)
	if net.Sign() <= 0 || net.Cmp(minUFND) < 0 {
		return
	}

	tx, err := chain.BuildSignedTransfer(key, target, net, nonce)
	if err != nil {
		pm.log.Warn("Payout: Tx-Bau fehlgeschlagen", zap.Error(err))
		return
	}
	if err := pm.srv.mempool.Add(tx); err != nil {
		pm.log.Warn("Payout: Mempool-Ablehnung", zap.Error(err))
		return
	}
	// Wenn dieser Node gerade Proposer ist, sofort einbauen; sonst übernimmt der
	// automatische Loop des zuständigen Proposers.
	_, _, _ = pm.srv.submitOrProduce(context.Background())

	// LastPayout merken (verhindert im Intervall-Modus Doppelauszahlung; im
	// Schwellen-Modus wird der Saldo durch die Auszahlung ohnehin klein).
	pm.mu.Lock()
	pm.cfg.LastPayout = time.Now().Unix()
	_ = pm.persistLocked()
	pm.mu.Unlock()

	h := tx.Hash()
	pm.log.Info("Node-Einnahmen ausgezahlt",
		zap.String("ziel", cfg.Target),
		zap.String("betrag_fnd", uFNDToFND(net.String())),
		zap.String("tx", hexShort(h[:])))
}

// fndToUFND wandelt einen FND-Betrag (float) in die kleinste Einheit uFND
// (*big.Int) um. Für die Payout-Schwellen genügt die Genauigkeit; gerundet auf
// ganze uFND.
func fndToUFND(fnd float64) *big.Int {
	if fnd <= 0 {
		return big.NewInt(0)
	}
	// fnd * 1e9, gerundet.
	scaled := fnd*float64(chain.UFNDPerFND) + 0.5
	bf := new(big.Float).SetFloat64(scaled)
	out, _ := bf.Int(nil)
	if out == nil {
		return big.NewInt(0)
	}
	return out
}

func hexShort(b []byte) string {
	const hexd = "0123456789abcdef"
	n := len(b)
	if n > 6 {
		n = 6
	}
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		out[i*2] = hexd[b[i]>>4]
		out[i*2+1] = hexd[b[i]&0x0f]
	}
	return string(out)
}

// GET /api/v1/wallet/payout — aktuelle Auto-Payout-Einstellung + Node-Adresse.
func (s *Server) walletPayoutGet(c *gin.Context) {
	if s.payoutMgr == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}
	cfg := s.payoutMgr.get()
	resp := gin.H{
		"enabled":       true,
		"target":        cfg.Target,
		"mode":          string(cfg.Mode),
		"threshold_fnd": cfg.ThresholdFND,
		"interval_hrs":  cfg.IntervalHrs,
		"min_fnd":       cfg.MinFND,
		"last_payout":   cfg.LastPayout,
	}
	// Node-Adresse (Quelle der Auszahlung) mitgeben, damit das UI sie zeigt.
	if s.fileStore != nil {
		if _, addrHex := s.fileStore.ConsensusSigner(); addrHex != "" {
			resp["node_address"] = addrHex
		}
	}
	c.JSON(http.StatusOK, resp)
}

// POST /api/v1/wallet/payout — Auto-Payout-Einstellung setzen.
func (s *Server) walletPayoutSet(c *gin.Context) {
	if s.payoutMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	var req struct {
		Target       string  `json:"target"`
		Mode         string  `json:"mode"`
		ThresholdFND float64 `json:"threshold_fnd"`
		IntervalHrs  float64 `json:"interval_hrs"`
		MinFND       float64 `json:"min_fnd"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage"})
		return
	}
	mode := PayoutMode(req.Mode)
	// Deaktivieren ist immer erlaubt (leerer Modus).
	if mode != PayoutOff && mode != PayoutThreshold && mode != PayoutInterval {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger Modus (off | threshold | interval)"})
		return
	}
	// Bei aktivem Payout muss die Zieladresse gültig sein.
	if mode != PayoutOff {
		if _, ok := chain.AddressFromHex(req.Target); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Zieladresse"})
			return
		}
		if mode == PayoutThreshold && req.ThresholdFND <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Schwelle muss > 0 sein"})
			return
		}
		if mode == PayoutInterval && req.IntervalHrs <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Intervall muss > 0 Stunden sein"})
			return
		}
	}
	// Bestehende LastPayout-Zeit erhalten (nicht bei jedem Speichern zurücksetzen).
	prev := s.payoutMgr.get()
	cfg := PayoutConfig{
		Target:       req.Target,
		Mode:         mode,
		ThresholdFND: req.ThresholdFND,
		IntervalHrs:  req.IntervalHrs,
		MinFND:       req.MinFND,
		LastPayout:   prev.LastPayout,
	}
	if err := s.payoutMgr.set(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Speichern fehlgeschlagen: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "mode": string(mode)})
}
