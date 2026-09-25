package api

// Hinterlegte Swap-Schlüssel dauerhaft (verschlüsselt) speichern.
//
// Bisher lagen sie nur im Arbeitsspeicher: nach jedem Neustart (Update,
// Absturz, Stromausfall, geänderte fundus.env) stand die Order weiter im
// Orderbuch, der Node konnte aber nicht mehr liefern – der Käufer bekam
// "Maker: keine hinterlegten Schlüssel für diese Order".
//
// Datei data/swap-deposits.enc, AES-GCM (identity.Encrypt) mit einem eigenen
// Schlüssel in data/swap-deposits.key (0600, nur der Node-Dienst). Die Schlüssel
// MÜSSEN für die Automatik ohne Anmeldung nutzbar sein; der Schutz entspricht
// dem der Node-Wallet. Beim Stornieren/Erfüllen werden sie auch hier entfernt.

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/gagliardetto/solana-go"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/identity"
)

type persistedDeposit struct {
	Sol       string   `json:"s,omitempty"` // Base58
	Fnd       []string `json:"f,omitempty"`
	SolAddr   string   `json:"sa,omitempty"`
	FndAddr   string   `json:"fa,omitempty"`
	AmountSOL float64  `json:"as,omitempty"`
	AmountFND float64  `json:"af,omitempty"`
}

func (sc *swapCoordinator) depositPaths() (data, key string, ok bool) {
	if sc.server == nil || sc.server.cfg == nil || sc.server.cfg.DataDir == "" {
		return "", "", false
	}
	d := sc.server.cfg.DataDir
	return filepath.Join(d, "swap-deposits.enc"), filepath.Join(d, "swap-deposits.key"), true
}

func (sc *swapCoordinator) depositKey(keyPath string) ([]byte, error) {
	if b, err := os.ReadFile(keyPath); err == nil && len(b) == 32 {
		return b, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, k, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// saveDepositsLocked schreibt alle hinterlegten Schlüssel (sc.mu muss gehalten werden).
func (sc *swapCoordinator) saveDepositsLocked() {
	dataPath, keyPath, ok := sc.depositPaths()
	if !ok {
		return
	}
	if len(sc.deposits) == 0 {
		_ = os.Remove(dataPath)
		return
	}
	m := make(map[string]persistedDeposit, len(sc.deposits))
	for id, d := range sc.deposits {
		p := persistedDeposit{Fnd: d.fndSeed, SolAddr: d.solAddr, FndAddr: d.fndAddr,
			AmountSOL: d.amountSOL, AmountFND: d.amountFND}
		if len(d.solKey) == 64 {
			p.Sol = d.solKey.String()
		}
		m[id] = p
	}
	plain, err := json.Marshal(m)
	if err != nil {
		return
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	key, err := sc.depositKey(keyPath)
	if err != nil {
		sc.logWarn("Swap-Schlüssel: Speicherschlüssel nicht verfügbar", err)
		return
	}
	enc, err := identity.Encrypt(plain, key)
	if err != nil {
		sc.logWarn("Swap-Schlüssel: Verschlüsseln fehlgeschlagen", err)
		return
	}
	tmp := dataPath + ".tmp"
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		sc.logWarn("Swap-Schlüssel: Schreiben fehlgeschlagen", err)
		return
	}
	_ = os.Rename(tmp, dataPath)
}

// loadDeposits stellt hinterlegte Schlüssel nach einem Neustart wieder her.
func (sc *swapCoordinator) loadDeposits() {
	dataPath, keyPath, ok := sc.depositPaths()
	if !ok {
		return
	}
	enc, err := os.ReadFile(dataPath)
	if err != nil {
		return
	}
	key, err := os.ReadFile(keyPath)
	if err != nil || len(key) != 32 {
		sc.logWarn("Swap-Schlüssel: Speicherschlüssel fehlt – hinterlegte Schlüssel nicht lesbar", err)
		return
	}
	plain, err := identity.Decrypt(enc, key)
	if err != nil {
		sc.logWarn("Swap-Schlüssel: Entschlüsseln fehlgeschlagen", err)
		return
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	var m map[string]persistedDeposit
	if json.Unmarshal(plain, &m) != nil {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	n := 0
	for id, p := range m {
		d := &depositedKeys{fndSeed: p.Fnd, solAddr: p.SolAddr, fndAddr: p.FndAddr,
			amountSOL: p.AmountSOL, amountFND: p.AmountFND}
		if p.Sol != "" {
			if k, err := solana.PrivateKeyFromBase58(p.Sol); err == nil {
				d.solKey = k
			}
		}
		if _, exists := sc.deposits[id]; !exists {
			sc.deposits[id] = d
			n++
		}
	}
	if n > 0 && sc.server != nil && sc.server.log != nil {
		sc.server.log.Info("Hinterlegte Swap-Schlüssel wiederhergestellt", zap.Int("orders", n))
	}
}

func (sc *swapCoordinator) logWarn(msg string, err error) {
	if sc.server != nil && sc.server.log != nil {
		sc.server.log.Warn(msg, zap.Error(err))
	}
}
