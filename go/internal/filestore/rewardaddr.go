package filestore

// Einnahmen-Ziel des Nodes (Speicher-/Transfer-Verdienst).
//
// Die Node-Wallet (node.seed) wird mit 128 MiB abgeleitet und ist damit NICHT
// dieselbe Adresse, die ihre Seed-Wörter in der Nutzer-Wallet (256 MiB, wie
// fnd-wallet) ergeben. Damit der Betreiber seine Einnahmen in seiner normalen
// Wallet sieht, kann das Ziel umgestellt werden – ohne Chain-Änderung: die
// Chain schreibt der Adresse im Provider-Feld der Quittung gut.
//
// Priorität: reward_addr.txt (Einstellungen, ohne Neustart) >
//            FUNDUS_STORAGE_REWARD_ADDR > Node-Adresse.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
)

const rewardAddrFile = "reward_addr.txt"

// applyRewardAddr setzt fs.rewardAddr nach obiger Priorität.
func (fs *FileStore) applyRewardAddr() {
	fs.rewardAddr = fs.selfAddr
	cand := ""
	if fs.keyDir != "" {
		if raw, err := os.ReadFile(filepath.Join(fs.keyDir, rewardAddrFile)); err == nil {
			cand = strings.TrimSpace(string(raw))
		}
	}
	if cand == "" {
		cand = fs.cfg.RewardAddr
	}
	if cand == "" {
		return
	}
	if a, ok := chain.AddressFromHex(cand); ok {
		fs.rewardAddr = a
	} else if fs.log != nil {
		fs.log.Warn("Reward-Adresse ungültig, nutze Node-Adresse", zap.String("reward_addr", cand))
	}
}

// RewardAddress liefert das aktuelle Einnahmen-Ziel und die Node-Adresse.
func (fs *FileStore) RewardAddress() (reward, node string) {
	if fs.signerKey == nil {
		return "", ""
	}
	return fs.rewardAddr.Hex(), fs.selfAddr.Hex()
}

// SetRewardAddress stellt das Einnahmen-Ziel um (leer = zurück auf die Node-
// Adresse) und speichert es dauerhaft (reward_addr.txt).
func (fs *FileStore) SetRewardAddress(addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr != "" {
		if _, ok := chain.AddressFromHex(addr); !ok {
			return fmt.Errorf("ungültige Adresse")
		}
	}
	if fs.keyDir == "" {
		return fmt.Errorf("kein Schlüsselverzeichnis")
	}
	p := filepath.Join(fs.keyDir, rewardAddrFile)
	if addr == "" {
		_ = os.Remove(p)
	} else if err := os.WriteFile(p, []byte(addr+"\n"), 0o600); err != nil {
		return err
	}
	fs.applyRewardAddr()
	if fs.log != nil {
		fs.log.Info("Einnahmen-Ziel geändert", zap.String("reward_addr", fs.rewardAddr.Hex()))
	}
	return nil
}
