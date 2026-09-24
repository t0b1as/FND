package filestore

import (
	"crypto/ecdsa"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/fundus/node/internal/identity"
)

// Dateinamen der persistenten Node-Wallet im DataDir.
//   - node.seed: 30 Seed-Wörter (neu, WIEDERHERSTELLBAR). Bevorzugt.
//   - node.key:  roher Hex-Schlüssel (Altbestand, NICHT wiederherstellbar).
// Beide enthalten ein GEHEIMNIS und werden mit 0600 (nur Besitzer) angelegt.
const (
	nodeSeedFile = "node.seed"
	nodeKeyFile  = "node.key"
)

// NodeWalletInfo beschreibt die Node-Wallet nach dem Laden/Erzeugen.
type NodeWalletInfo struct {
	Key       *ecdsa.PrivateKey
	Address   string   // abgeleitete Fundus-Adresse (0x…)
	SeedWords []string // nur gesetzt, wenn NEU erzeugt (zum einmaligen Anzeigen)
	Created   bool     // true = beim ersten Start neu erzeugt
	FromSeed  bool     // true = seed-basiert (wiederherstellbar)
}

// loadOrCreateNodeWallet lädt die persistente Node-Wallet oder erzeugt beim
// ersten Start eine neue. Neue Wallets sind SEED-BASIERT: 30 zufällige Wörter,
// aus denen der Schlüssel abgeleitet wird. Die Seed wird gespeichert und einmal
// zurückgegeben, damit der Betreiber sie sichern kann — so ist die Wallet (und
// der Verdienst) wiederherstellbar, falls die Node-Daten verloren gehen.
//
// Priorität beim Laden:
//  1. node.seed (seed-basiert, wiederherstellbar) — bevorzugt.
//  2. node.key (roher Hex, Altbestand) — weiter unterstützt, aber NICHT
//     wiederherstellbar. Wird NICHT automatisch migriert (der Schlüssel bleibt
//     gültig; ein Wechsel würde die Adresse und damit den Verdienst ändern).
//  3. Nichts vorhanden → neue seed-basierte Wallet erzeugen.
func loadOrCreateNodeWallet(keyDir string) (*NodeWalletInfo, error) {
	if keyDir == "" {
		return nil, fmt.Errorf("filestore: kein Verzeichnis für Node-Wallet")
	}
	seedPath := filepath.Join(keyDir, nodeSeedFile)
	keyPath := filepath.Join(keyDir, nodeKeyFile)

	// 1. Seed-basierte Wallet laden (bevorzugt).
	if raw, err := os.ReadFile(seedPath); err == nil {
		words := strings.Fields(string(raw))
		key, err := identity.DerivePrivateKeyFromSeed(words)
		if err != nil {
			return nil, fmt.Errorf("filestore: node.seed unlesbar: %w", err)
		}
		addr, _ := identity.DeriveAddressFromSeed(words)
		return &NodeWalletInfo{Key: key, Address: addr, Created: false, FromSeed: true}, nil
	}

	// 2. Alten rohen Hex-Schlüssel laden (Rückwärtskompatibilität).
	if raw, err := os.ReadFile(keyPath); err == nil {
		keyHex := strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x")
		key, err := crypto.HexToECDSA(keyHex)
		if err != nil {
			return nil, fmt.Errorf("filestore: node.key unlesbar: %w", err)
		}
		addr := crypto.PubkeyToAddress(key.PublicKey).Hex()
		return &NodeWalletInfo{Key: key, Address: addr, Created: false, FromSeed: false}, nil
	}

	// 3. Nichts vorhanden → neue seed-basierte Wallet erzeugen.
	words, address, err := identity.GenerateWallet()
	if err != nil {
		return nil, fmt.Errorf("filestore: Wallet-Erzeugung: %w", err)
	}
	key, err := identity.DerivePrivateKeyFromSeed(words)
	if err != nil {
		return nil, fmt.Errorf("filestore: Schlüssel aus Seed: %w", err)
	}

	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return nil, fmt.Errorf("filestore: Verzeichnis anlegen: %w", err)
	}
	// Seed als Zeilen-getrennte Wörter speichern, nur für den Besitzer lesbar.
	content := strings.Join(words, "\n") + "\n"
	if err := os.WriteFile(seedPath, []byte(content), 0o600); err != nil {
		return nil, fmt.Errorf("filestore: node.seed schreiben: %w", err)
	}
	_ = os.Chmod(seedPath, 0o600)

	return &NodeWalletInfo{
		Key:       key,
		Address:   address,
		SeedWords: words,
		Created:   true,
		FromSeed:  true,
	}, nil
}

// LoadOrCreateNodeWallet ist die exportierte Variante von loadOrCreateNodeWallet.
// Sie erlaubt es, die Node-Wallet UNABHÄNGIG vom FileStore zu laden/erzeugen —
// z.B. wenn ein Node keinen Speicher anbietet (Filesharing aus), aber trotzdem
// eine Identität zum Staken und für den Konsens braucht.
func LoadOrCreateNodeWallet(keyDir string) (*NodeWalletInfo, error) {
	return loadOrCreateNodeWallet(keyDir)
}
