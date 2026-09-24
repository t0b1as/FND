package filestore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
)

// nodeSeedEncFile hält die MIT PASSWORT VERSCHLÜSSELTE Seed (XChaCha20-Poly1305).
// Existiert diese Datei, gibt es KEINE Klartext-node.seed — der Node braucht das
// Passwort beim Start (oder nachgereicht via Weboberfläche) zum Entsperren.
//
// Format der Datei (hex, zeilengetrennt):
//   Zeile 1: salt (für Argon2id-Schlüsselableitung aus dem Passwort)
//   Zeile 2: ciphertext (xchacha20: nonce||sealed(seed-wörter))
const nodeSeedEncFile = "node.seed.enc"

// Argon2id-Parameter für die Seed-Verschlüsselung: bewusst STÄRKER als der
// Anzeige-Passwortschutz, da hier echtes Wertspeicher-Material geschützt wird.
const (
	seedEncTime    = 4
	seedEncMemory  = 256 * 1024 // 256 MiB
	seedEncThreads = 4
	seedEncKeyLen  = 32
	seedEncSaltLen = 16
)

// deriveSeedEncKey leitet den XChaCha20-Schlüssel aus Passwort + Salt ab.
func deriveSeedEncKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, seedEncTime, seedEncMemory, seedEncThreads, seedEncKeyLen)
}

// nodeSeedEncPath liefert den Pfad der verschlüsselten Seed-Datei.
func (fs *FileStore) nodeSeedEncPath() (string, error) {
	if fs.keyDir == "" {
		return "", fmt.Errorf("filestore: kein KeyDir")
	}
	return filepath.Join(fs.keyDir, nodeSeedEncFile), nil
}

// SeedIsEncrypted meldet, ob eine verschlüsselte Seed vorliegt (node.seed.enc).
func (fs *FileStore) SeedIsEncrypted() bool {
	p, err := fs.nodeSeedEncPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// writeEncryptedSeed verschlüsselt die Seed-Wörter mit dem Passwort und schreibt
// node.seed.enc. Entfernt anschließend eine etwaige Klartext-node.seed, damit die
// Wörter nur noch verschlüsselt auf der Platte liegen.
func (fs *FileStore) writeEncryptedSeed(words []string, password string) error {
	if fs.keyDir == "" {
		return fmt.Errorf("filestore: kein KeyDir")
	}
	if password == "" {
		return fmt.Errorf("filestore: leeres Passwort")
	}
	salt := make([]byte, seedEncSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("filestore: Salt: %w", err)
	}
	key := deriveSeedEncKey(password, salt)
	plain := []byte(strings.Join(words, "\n"))
	ct, err := xchacha20Encrypt(plain, key)
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return fmt.Errorf("filestore: Seed verschlüsseln: %w", err)
	}
	content := hex.EncodeToString(salt) + "\n" + hex.EncodeToString(ct) + "\n"
	encPath := filepath.Join(fs.keyDir, nodeSeedEncFile)
	if err := os.WriteFile(encPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("filestore: node.seed.enc schreiben: %w", err)
	}
	// Klartext-Seed und alten Hex-Key entfernen — Wörter nur noch verschlüsselt.
	_ = os.Remove(filepath.Join(fs.keyDir, nodeSeedFile))
	_ = os.Remove(filepath.Join(fs.keyDir, nodeKeyFile))
	return nil
}

// decryptSeed entschlüsselt node.seed.enc mit dem Passwort und liefert die Wörter.
func (fs *FileStore) decryptSeed(password string) ([]string, error) {
	encPath, err := fs.nodeSeedEncPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(encPath)
	if err != nil {
		return nil, fmt.Errorf("filestore: keine verschlüsselte Seed vorhanden")
	}
	lines := strings.Fields(string(raw))
	if len(lines) != 2 {
		return nil, fmt.Errorf("filestore: node.seed.enc beschädigt")
	}
	salt, err1 := hex.DecodeString(lines[0])
	ct, err2 := hex.DecodeString(lines[1])
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("filestore: node.seed.enc unlesbar")
	}
	key := deriveSeedEncKey(password, salt)
	plain, err := xchacha20Decrypt(ct, key)
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("falsches Passwort")
	}
	words := strings.Fields(string(plain))
	if len(words) == 0 {
		return nil, fmt.Errorf("filestore: entschlüsselte Seed leer")
	}
	return words, nil
}

// UnlockEncryptedSeed entschlüsselt die Seed mit dem Passwort und aktiviert die
// Wallet zur Laufzeit (setzt signerKey/selfAddr/rewardAddr). Wird beim Start mit
// dem env-Passwort und/oder später über die Weboberfläche aufgerufen.
func (fs *FileStore) UnlockEncryptedSeed(password string) error {
	words, err := fs.decryptSeed(password)
	if err != nil {
		return err
	}
	key, err := identity.DerivePrivateKeyFromSeed(words)
	if err != nil {
		return fmt.Errorf("filestore: Schlüssel aus Seed: %w", err)
	}
	fs.signerKey = key
	fs.selfAddr = chain.PubkeyToAddress(&key.PublicKey)
	fs.rewardAddr = fs.selfAddr
	return nil
}

// WalletLocked meldet, ob eine verschlüsselte Seed vorliegt, aber noch nicht
// entsperrt wurde (kein aktiver signerKey). In diesem Zustand stellt der Node
// keine Quittungen aus, läuft aber sonst normal weiter.
func (fs *FileStore) WalletLocked() bool {
	return fs.SeedIsEncrypted() && fs.signerKey == nil
}

// EncryptExistingSeed verschlüsselt eine bereits vorhandene Klartext-Seed
// nachträglich mit einem Passwort (Migration Klartext → verschlüsselt).
func (fs *FileStore) EncryptExistingSeed(password string) error {
	words, err := fs.ReadNodeSeedWords()
	if err != nil {
		return err
	}
	return fs.writeEncryptedSeed(words, password)
}

// DecryptSeedToPlaintext entfernt die Verschlüsselung wieder (schreibt Klartext-
// node.seed zurück), nach Passwort-Prüfung. Für Nutzer, die den autonomen
// Auto-Start dem Verschlüsselungsschutz vorziehen.
func (fs *FileStore) DecryptSeedToPlaintext(password string) error {
	words, err := fs.decryptSeed(password)
	if err != nil {
		return err
	}
	if fs.keyDir == "" {
		return fmt.Errorf("filestore: kein KeyDir")
	}
	content := strings.Join(words, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(fs.keyDir, nodeSeedFile), []byte(content), 0o600); err != nil {
		return fmt.Errorf("filestore: node.seed schreiben: %w", err)
	}
	_ = os.Remove(filepath.Join(fs.keyDir, nodeSeedEncFile))
	return nil
}
