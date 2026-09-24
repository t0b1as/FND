// Package identity – totp.go
//
// TOTP (Time-based One-Time Password) nach RFC 6238.
// Kompatibel mit: Google Authenticator, Aegis, Bitwarden, 1Password, FreeOTP.
//
// Warum TOTP statt SMS/Email?
//   - Kein Server benötigt (vollständig offline/dezentral)
//   - Kein Netzwerk-Kanal kompromittierbar (SS7, SIM-Swap)
//   - Standard-Authenticator-Apps sind weit verbreitet
//   - Perfekt für dezentralen Pi-Node ohne Cloud-Abhängigkeit
//
// Sicherheitsmodell in Fundus:
//   1. Identität ableiten (Argon2id, ~6-8s) → Ed25519-Key
//   2. TOTP-Code prüfen (30s-Fenster, ±1 Toleranz)
//   3. Erst dann wird die Session angelegt
//
// TOTP-Secret-Speicherung:
//   Das Secret wird XChaCha20-Poly1305 verschlüsselt (Key = Identity-Key)
//   und im lokalen DataDir gespeichert. Nie im Klartext auf Disk.
//
// Timing-Sicherheit:
//   Code-Vergleich via subtle.ConstantTimeCompare (kein Timing-Leak).
//   Verwendete Codes werden für 90s getracked (Replay-Schutz).

package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // TOTP-Standard RFC 6238 schreibt HMAC-SHA1 vor
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"

	"golang.org/x/crypto/argon2"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// =============================================================================
//  Konstanten
// =============================================================================

const (
	totpDigits    = 6           // Standard: 6 Ziffern
	totpPeriod    = 30          // Sekunden pro Zeitschritt (RFC 6238)
	totpWindow    = 1           // Toleranz: ±1 Zeitschritt (±30s)
	totpIssuer    = "Fundus"
	totpSecretLen = 20          // 160 Bit = Standard für TOTP (RFC 4226)
)

// =============================================================================
//  TOTPState – persistenter Zustand
// =============================================================================

// TOTPState wird verschlüsselt auf Disk gespeichert.
type TOTPState struct {
	Enabled    bool      `json:"enabled"`
	SecretB32  string    `json:"secret_b32"` // Base32-encodiertes Secret
	EnabledAt  time.Time `json:"enabled_at"`
	// Replay-Schutz: bereits verwendete Codes (Timestamp → Code)
	UsedCodes  map[string]string `json:"used_codes"`
}

// =============================================================================
//  TOTPManager
// =============================================================================

// TOTPManager verwaltet TOTP-2FA für einen Identity-Inhaber.
type TOTPManager struct {
	mu       sync.Mutex
	state    *TOTPState
	filePath string     // verschlüsselte State-Datei
	encKey   []byte     // von Identity abgeleitet, 32 Bytes
}

// NewTOTPManager erstellt einen TOTPManager für eine Identität.
// encKey: XChaCha20-Key (32 Bytes), von der Identity abgeleitet.
// dataDir: Verzeichnis für die verschlüsselte State-Datei.
func NewTOTPManager(id *Identity, dataDir string) (*TOTPManager, error) {
	// Encryption-Key für TOTP-State aus Identity ableiten
	// Separater Domain-Salt → unabhängig vom Haupt-Key
	saltRaw := blake3Sum256(append(
		[]byte("fundus-totp-storage-v1:"),
		[]byte(id.FundusID)...,
	))
	encKey := argon2DeriveKey(id.ed25519Key[:32], saltRaw[:], 1, 32*1024, 4, 32)

	mgr := &TOTPManager{
		filePath: filepath.Join(dataDir, "totp.enc"),
		encKey:   encKey,
	}

	// Bestehenden State laden
	if err := mgr.load(); err != nil {
		// Kein State = TOTP noch nicht eingerichtet
		mgr.state = &TOTPState{UsedCodes: make(map[string]string)}
	}
	return mgr, nil
}

// =============================================================================
//  Setup-Flow (einmalig)
// =============================================================================

// TOTPSetup enthält alle Daten für die Authenticator-App-Einrichtung.
type TOTPSetup struct {
	SecretB32  string // für manuelle Eingabe in der App
	OTPAuthURL string // otpauth://totp/… URL für QR-Code
	// QRCodeSVG wäre möglich, aber für Browser-Anzeige reicht die URL
}

// GenerateSetup erstellt ein neues TOTP-Secret und gibt Setup-Daten zurück.
// TOTP wird erst nach Verify() aktiviert.
func (tm *TOTPManager) GenerateSetup(accountName string) (*TOTPSetup, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.state.Enabled {
		return nil, errors.New("totp: bereits aktiviert – zuerst deaktivieren")
	}

	// 160 Bit zufälliges Secret (RFC 4226 empfiehlt ≥160 Bit)
	secret := make([]byte, totpSecretLen)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("totp: random: %w", err)
	}
	secretB32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)

	// Temporär im State speichern (noch nicht enabled)
	tm.state.SecretB32 = secretB32

	// otpauth URL bauen (Standard für QR-Code-Scanner)
	label := url.PathEscape(totpIssuer + ":" + accountName)
	params := url.Values{}
	params.Set("secret", secretB32)
	params.Set("issuer", totpIssuer)
	params.Set("algorithm", "SHA1")
	params.Set("digits", fmt.Sprintf("%d", totpDigits))
	params.Set("period", fmt.Sprintf("%d", totpPeriod))
	otpURL := fmt.Sprintf("otpauth://totp/%s?%s", label, params.Encode())

	return &TOTPSetup{
		SecretB32:  formatSecretForDisplay(secretB32),
		OTPAuthURL: otpURL,
	}, nil
}

// VerifyAndEnable aktiviert TOTP wenn der erste Code korrekt ist.
// Muss nach GenerateSetup() aufgerufen werden.
func (tm *TOTPManager) VerifyAndEnable(code string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.state.SecretB32 == "" {
		return errors.New("totp: kein Setup aktiv – GenerateSetup() zuerst aufrufen")
	}
	if tm.state.Enabled {
		return errors.New("totp: bereits aktiviert")
	}

	if !tm.validateCode(tm.state.SecretB32, code, time.Now()) {
		return errors.New("totp: ungültiger Code – App nochmals prüfen und erneut versuchen")
	}

	tm.state.Enabled   = true
	tm.state.EnabledAt = time.Now().UTC()
	tm.markUsed(code)

	return tm.save()
}

// Disable deaktiviert TOTP. Erfordert den aktuellen TOTP-Code zur Bestätigung.
func (tm *TOTPManager) Disable(code string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if !tm.state.Enabled {
		return errors.New("totp: nicht aktiviert")
	}
	if !tm.validateCode(tm.state.SecretB32, code, time.Now()) {
		return errors.New("totp: ungültiger Code")
	}

	tm.state = &TOTPState{UsedCodes: make(map[string]string)}
	return tm.save()
}

// =============================================================================
//  Verifikation (Login-Flow)
// =============================================================================

// IsEnabled gibt zurück ob TOTP aktiviert ist.
func (tm *TOTPManager) IsEnabled() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.state.Enabled
}

// Verify prüft einen TOTP-Code. Gibt Fehler zurück wenn:
//   - Code falsch
//   - Code bereits verwendet (Replay-Angriff)
//   - TOTP nicht aktiviert
func (tm *TOTPManager) Verify(code string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if !tm.state.Enabled {
		return errors.New("totp: nicht aktiviert")
	}

	// Replay-Schutz: Code schon verwendet?
	ts := currentTimeStep()
	replayKey := fmt.Sprintf("%d:%s", ts, code)
	if _, used := tm.state.UsedCodes[replayKey]; used {
		return errors.New("totp: Code bereits verwendet (Replay-Angriff verhindert)")
	}

	if !tm.validateCode(tm.state.SecretB32, code, time.Now()) {
		return errors.New("totp: ungültiger Code")
	}

	tm.markUsed(code)
	// Veraltete Einträge bereinigen
	tm.pruneUsedCodes()

	return tm.save()
}

// =============================================================================
//  TOTP-Berechnung (RFC 6238 + RFC 4226)
// =============================================================================

func (tm *TOTPManager) validateCode(secretB32, code string, now time.Time) bool {
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		return false
	}

	// Zeitfenster: aktuell ± totpWindow
	ts := now.Unix() / int64(totpPeriod)
	for delta := int64(-totpWindow); delta <= int64(totpWindow); delta++ {
		expected := hotpCode(secret, uint64(ts+delta))
		// Constant-time Vergleich (kein Timing-Leak)
		if subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 {
			return true
		}
	}
	return false
}

// hotpCode berechnet einen HOTP-Code (RFC 4226, Basis für TOTP).
//
//nolint:gosec // SHA1 ist vom RFC 6238 Standard vorgeschrieben
func hotpCode(secret []byte, counter uint64) string {
	// Counter als Big-Endian 8-Byte
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, counter)

	// HMAC-SHA1 (RFC 4226 §5)
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg)
	h := mac.Sum(nil)

	// Dynamic Truncation (RFC 4226 §5.3)
	offset := h[len(h)-1] & 0x0f
	code := (uint32(h[offset]&0x7f) << 24) |
		(uint32(h[offset+1]) << 16) |
		(uint32(h[offset+2]) << 8) |
		uint32(h[offset+3])

	// 6 Ziffern
	code %= uint32(math.Pow10(totpDigits))
	return fmt.Sprintf("%06d", code)
}

func currentTimeStep() int64 {
	return time.Now().Unix() / int64(totpPeriod)
}

func (tm *TOTPManager) markUsed(code string) {
	ts := currentTimeStep()
	key := fmt.Sprintf("%d:%s", ts, code)
	if tm.state.UsedCodes == nil {
		tm.state.UsedCodes = make(map[string]string)
	}
	tm.state.UsedCodes[key] = time.Now().UTC().Format(time.RFC3339)
}

func (tm *TOTPManager) pruneUsedCodes() {
	// Einträge älter als 90s (3 × Zeitschritt) löschen
	cutoff := currentTimeStep() - 3
	for key := range tm.state.UsedCodes {
		var ts int64
		fmt.Sscanf(key, "%d:", &ts)
		if ts < cutoff {
			delete(tm.state.UsedCodes, key)
		}
	}
}

// =============================================================================
//  Persistenz (XChaCha20-Poly1305 verschlüsselt)
// =============================================================================

func (tm *TOTPManager) save() error {
	data, err := json.Marshal(tm.state)
	if err != nil {
		return fmt.Errorf("totp: marshal: %w", err)
	}

	enc, err := Encrypt(data, tm.encKey)
	if err != nil {
		return fmt.Errorf("totp: encrypt: %w", err)
	}

	// Atomar schreiben
	encoded := base64.StdEncoding.EncodeToString(enc)
	tmp := tm.filePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(encoded), 0600); err != nil {
		return fmt.Errorf("totp: write: %w", err)
	}
	return os.Rename(tmp, tm.filePath)
}

func (tm *TOTPManager) load() error {
	raw, err := os.ReadFile(tm.filePath)
	if err != nil {
		return err
	}
	enc, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return fmt.Errorf("totp: base64: %w", err)
	}
	plain, err := Decrypt(enc, tm.encKey)
	if err != nil {
		return fmt.Errorf("totp: decrypt: %w", err)
	}
	state := &TOTPState{}
	if err := json.Unmarshal(plain, state); err != nil {
		return fmt.Errorf("totp: unmarshal: %w", err)
	}
	if state.UsedCodes == nil {
		state.UsedCodes = make(map[string]string)
	}
	tm.state = state
	return nil
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

// formatSecretForDisplay teilt das Secret in 4er-Gruppen auf (leichter abzutippen).
func formatSecretForDisplay(secretB32 string) string {
	var result []byte
	for i, c := range secretB32 {
		if i > 0 && i%4 == 0 {
			result = append(result, ' ')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

// argon2DeriveKey leitet einen Key via Argon2id ab (Wiederverwendung des Package-Imports).
func argon2DeriveKey(password, salt []byte, t, m uint32, p uint8, keyLen uint32) []byte {
	return argon2.IDKey(password, salt, t, m, p, keyLen)
}
