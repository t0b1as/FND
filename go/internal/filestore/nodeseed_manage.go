package filestore

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
)

// nodeSeedPassFile hält den Argon2id-Hash des optionalen Anzeige-Passworts.
// WICHTIG: Dies schützt NUR die Anzeige der Wörter über die Weboberfläche, nicht
// die node.seed-Datei selbst. Wer Dateizugriff auf den Node hat, kann node.seed
// immer lesen (der Node braucht den Schlüssel im Klartext zum Signieren). Der
// Passwortschutz verhindert also nur, dass jemand mit Browser-Zugriff (aber ohne
// Server-Zugriff) die Wörter einsieht — eine bewusste, ehrliche Schutzstufe.
const nodeSeedPassFile = "node.seed.passhash"

// nodeSeedPassParams: bewusst moderate Argon2id-Parameter (Anzeige-Gate, kein
// Wertspeicher-Schlüssel). 64 MiB reichen gegen Brute-Force der Anzeige-Sperre.
const (
	seedPassTime   = 2
	seedPassMemory = 64 * 1024
	seedPassThreads = 4
	seedPassKeyLen = 32
	seedPassSaltLen = 16
)

// nodeSeedPath liefert den Pfad der node.seed-Datei (leerer keyDir → Fehler).
func (fs *FileStore) nodeSeedPath() (string, error) {
	if fs.keyDir == "" {
		return "", fmt.Errorf("filestore: kein KeyDir für Seed bekannt")
	}
	return filepath.Join(fs.keyDir, nodeSeedFile), nil
}

// ReadNodeSeedWords liest die Seed-Wörter jederzeit aus node.seed (unabhängig
// davon, ob sie in dieser Sitzung neu erzeugt wurden). So kommt der Betreiber
// auch später noch an seine Wörter, etwa wenn er die Erst-Anzeige verpasst hat.
func (fs *FileStore) ReadNodeSeedWords() ([]string, error) {
	path, err := fs.nodeSeedPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("filestore: keine seed-basierte Node-Wallet vorhanden")
	}
	words := strings.Fields(string(raw))
	if len(words) == 0 {
		return nil, fmt.Errorf("filestore: node.seed leer")
	}
	return words, nil
}

// SeedDisplayProtected meldet, ob ein Anzeige-Passwort gesetzt ist.
func (fs *FileStore) SeedDisplayProtected() bool {
	if fs.keyDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(fs.keyDir, nodeSeedPassFile))
	return err == nil
}

// SetSeedDisplayPassword setzt (oder ändert) das Anzeige-Passwort. Leeres
// Passwort entfernt den Schutz.
func (fs *FileStore) SetSeedDisplayPassword(password string) error {
	if fs.keyDir == "" {
		return fmt.Errorf("filestore: kein KeyDir")
	}
	passPath := filepath.Join(fs.keyDir, nodeSeedPassFile)
	if password == "" {
		_ = os.Remove(passPath)
		return nil
	}
	salt := make([]byte, seedPassSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("filestore: Salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, seedPassTime, seedPassMemory, seedPassThreads, seedPassKeyLen)
	// Format: hex(salt):hex(hash)
	content := hex.EncodeToString(salt) + ":" + hex.EncodeToString(hash)
	return os.WriteFile(passPath, []byte(content), 0o600)
}

// checkSeedDisplayPassword prüft ein Passwort gegen den gespeicherten Hash.
// Gibt true zurück, wenn kein Passwort gesetzt ist (dann ist Anzeige frei).
func (fs *FileStore) checkSeedDisplayPassword(password string) bool {
	if fs.keyDir == "" {
		return true
	}
	raw, err := os.ReadFile(filepath.Join(fs.keyDir, nodeSeedPassFile))
	if err != nil {
		return true // kein Passwort gesetzt → Anzeige frei
	}
	parts := strings.SplitN(strings.TrimSpace(string(raw)), ":", 2)
	if len(parts) != 2 {
		return false
	}
	salt, err1 := hex.DecodeString(parts[0])
	want, err2 := hex.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, seedPassTime, seedPassMemory, seedPassThreads, seedPassKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ReadNodeSeedWordsWithPassword liefert die Wörter nur, wenn das Anzeige-Passwort
// stimmt (oder keines gesetzt ist).
func (fs *FileStore) ReadNodeSeedWordsWithPassword(password string) ([]string, error) {
	if !fs.checkSeedDisplayPassword(password) {
		return nil, fmt.Errorf("falsches Passwort")
	}
	return fs.ReadNodeSeedWords()
}

// RecreateNodeWallet erzeugt eine KOMPLETT NEUE seed-basierte Node-Wallet und
// ersetzt die alte. ACHTUNG: Die alte Adresse (und ein etwaiges Guthaben darauf)
// ist danach nicht mehr über diesen Node erreichbar — nur über die alten
// Seed-Wörter, falls noch vorhanden. Gibt die neuen Wörter + Adresse zurück.
// Der Aufrufer muss den signerKey/selfAddr des laufenden FileStore aktualisieren.
func (fs *FileStore) RecreateNodeWallet() ([]string, string, error) {
	if fs.keyDir == "" {
		return nil, "", fmt.Errorf("filestore: kein KeyDir")
	}
	words, address, err := identity.GenerateWallet()
	if err != nil {
		return nil, "", fmt.Errorf("filestore: Wallet-Erzeugung: %w", err)
	}
	key, err := identity.DerivePrivateKeyFromSeed(words)
	if err != nil {
		return nil, "", fmt.Errorf("filestore: Schlüssel aus Seed: %w", err)
	}
	// Neue Seed schreiben (überschreibt die alte node.seed).
	seedPath := filepath.Join(fs.keyDir, nodeSeedFile)
	content := strings.Join(words, "\n") + "\n"
	if err := os.WriteFile(seedPath, []byte(content), 0o600); err != nil {
		return nil, "", fmt.Errorf("filestore: node.seed schreiben: %w", err)
	}
	// Alten Anzeige-Passwortschutz entfernen (gehört zur alten Wallet).
	_ = os.Remove(filepath.Join(fs.keyDir, nodeSeedPassFile))
	// Etwaigen alten rohen node.key entfernen, damit er nicht wieder Vorrang bekommt.
	_ = os.Remove(filepath.Join(fs.keyDir, nodeKeyFile))

	// Laufenden FileStore auf den neuen Schlüssel umstellen.
	fs.signerKey = key
	fs.selfAddr = chain.PubkeyToAddress(&key.PublicKey)
	fs.rewardAddr = fs.selfAddr
	fs.nodeSeedWords = words // für sofortige Anzeige

	return words, address, nil
}
