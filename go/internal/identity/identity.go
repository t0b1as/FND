// Package identity implementiert email-basierte Identitäten für Fundus.
//
// Crypto-Stack (Noise Protocol / Signal-Standard):
//   Email + Passwort → Argon2id(t=4, m=256MiB) → Ed25519 Private Key
//                                                → Ed25519 Public Key → FundusID (BLAKE3-256 [:20])
//   Für ECDH: Ed25519 Private Key → X25519 (Curve25519) → Shared Secret
//             Shared Secret → Argon2id → XChaCha20-Poly1305-Schlüssel
//
// Warum dieser Stack?
//   Ed25519: deterministische Signaturen (kein Nonce-Leck wie bei ECDSA)
//   X25519:  Constant-time ECDH, timing-resistent
//   XChaCha20-Poly1305: 192-Bit Nonce (keine Kollisionen), kein HW-AES nötig
//   Argon2id: memory-hard KDF, GPU-resistent
package identity

import (
	"crypto/ecdsa"
	"crypto/rand"
	"lukechampine.com/blake3"
	textunicode "golang.org/x/text/unicode/norm"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/argon2"

	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/text/unicode/norm"
)

// =============================================================================
//  Konstanten
// =============================================================================

const (
	// Argon2id für Identitätsableitung – starke Parameter wegen schwacher Passwörter
	a2IdentTime   uint32 = 4  // t=4 bei 256 MiB – Login-Kompromiss auf Pi 3
	a2IdentMemory uint32 = 256 * 1024 // 256 MiB – Pi-3-sicher (512 MiB trieb den Node ins OOM)
	                                  // NVL72-Angriff bei 20-Zeichen-PW weiterhin >10^24 Jahre
	a2Threads     uint8  = 4
	a2KeyLen      uint32 = 32

	// Argon2id für ECDH-Shared-Secret-Härtung
	a2ECDHTime   uint32 = 2
	a2ECDHMemory uint32 = 64 * 1024 // 64 MiB

	// Argon2id für FND-Wallet-Ableitung (Seed→Key, Email+PW→Wörter).
	// 256 MiB statt 700 MiB: der Pi 3 hat nur 1 GB RAM gesamt; 700 MiB on top
	// des laufenden Node → OOM-Kill (502). 256 MiB ist Pi-sicher und stark.
	// FINAL – fließt in die Schlüsselableitung, nie mehr ändern (sonst andere Adressen).
	a2WalletTime   uint32 = 2  // t=2 (war 4): der vereinheitlichte Login (R297) macht
	                           // MEHRERE Argon2-Durchläufe hintereinander (Seed-Wörter +
	                           // Ed25519 + Chain-Adresse). t=4 ergab ~15s auf dem Pi und
	                           // ließ den Login-fetch ins Timeout laufen → Cookie kam nie an.
	a2WalletMemory uint32 = 128 * 1024 // 128 MiB (war 256) – halbiert die Login-Zeit
	a2WalletThreads uint8 = 4

	saltIdent = "fundus-identity-v2:" // final – nie mehr ändern
	saltECDH  = "fundus-ecdh-v2:"
)

// =============================================================================
//  Identity
// =============================================================================

// Identity repräsentiert eine abgeleitete Benutzeridentität.
type Identity struct {
	// FundusID: 20-Byte-Adresse (BLAKE3-256(Ed25519 PubKey)[:20], hex, "0x"-Prefix)
	FundusID string

	// PublicKeyHex: Ed25519 Public Key (32 Bytes, hex)
	PublicKeyHex string

	// x25519Priv: von Ed25519 abgeleiteter X25519-Schlüssel für ECDH
	x25519Priv [32]byte
	ed25519Key ed25519.PrivateKey

	// Chain-Wallet (secp256k1): aus DEMSELBEN Email+Passwort abgeleitet, damit
	// Login-Identität und Wallet EINE Identität sind. chainAddrCache wird LAZY
	// berechnet (erst bei ChainAddr()), damit der Login nicht den teuren
	// Chain-Argon2-Durchlauf machen muss.
	chainAddrCache string   // gecachte secp256k1-Adresse (0x…), lazy
	seedWords      []string // 30 BIP39-Wörter (für Wallet-Export/-Anzeige)

	DerivedAt time.Time
}

// ChainAddr gibt die Chain-/Wallet-Adresse (secp256k1) zurück und berechnet sie
// beim ersten Aufruf lazy aus den Seed-Wörtern (deterministisch). So bleibt der
// Login schnell — der teure Chain-Argon2 läuft nur, wenn die Adresse gebraucht
// wird (FND-Transfer, Anzeige).
func (id *Identity) ChainAddr() string {
	if id.chainAddrCache != "" {
		return id.chainAddrCache
	}
	if len(id.seedWords) == 0 {
		return ""
	}
	if addr, err := DeriveAddressFromSeed(id.seedWords); err == nil {
		id.chainAddrCache = addr
	}
	return id.chainAddrCache
}

// SeedWords gibt die Wallet-Seed-Wörter zurück (für den bewussten Export durch
// den angemeldeten Nutzer). Bewusst eine Methode, nicht exportiertes Feld.
func (id *Identity) SeedWords() []string { return id.seedWords }

// ChainPrivateKey leitet den secp256k1-Transfer-Key aus den Seed-Wörtern ab.
func (id *Identity) ChainPrivateKey() (*ecdsa.PrivateKey, error) {
	if len(id.seedWords) == 0 {
		return nil, fmt.Errorf("identity: keine Seed-Wörter in Identität")
	}
	return DerivePrivateKeyFromSeed(id.seedWords)
}

// =============================================================================
//  Ableitung
// =============================================================================

// Derive leitet eine Identität aus Email + Passwort ab (~3-6s auf Pi 3).
func Derive(email, password string) (*Identity, error) {
	if email == "" {
		return nil, errors.New("identity: email fehlt")
	}
	if err := ValidatePasswordStrength(password); err != nil {
		return nil, err
	}
	email = normalizeEmail(email)
	if !isValidEmail(email) {
		return nil, fmt.Errorf("identity: ungültige Email %q", email)
	}
	// EINHEITLICHER Weg: email+password → deterministische Seed-Wörter →
	// deriveFromWords erzeugt BEIDE Schlüssel (Ed25519 + secp256k1). So ist die
	// Identität identisch, egal ob man sich mit email+password ODER mit den
	// Seed-Wörtern anmeldet.
	words, err := WordsFromEmailPassword(email, password)
	if err != nil {
		return nil, err
	}
	return deriveFromWords(words)
}

// clampX25519 wendet den Curve25519 Clamp an (RFC 7748 §5).
func clampX25519(k *[32]byte) {
	k[0]  &= 248
	k[31] &= 127
	k[31] |= 64
}

// =============================================================================
//  Ed25519-Signaturen
// =============================================================================

// Sign signiert Daten mit Ed25519 (deterministisch, kein Nonce-Leck).
func (id *Identity) Sign(data []byte) (string, error) {
	if id.ed25519Key == nil {
		return "", errors.New("identity: kein Private Key")
	}
	sig := ed25519.Sign(id.ed25519Key, data)
	return hex.EncodeToString(sig), nil
}

// Verify prüft eine Ed25519-Signatur gegen eine FundusID.
func Verify(data []byte, sigHex, fundusID string, pubKeyHex string) bool {
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return false
	}
	pubBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return false
	}
	// FundusID aus PubKey prüfen
	h := blake3Sum256(pubBytes)
	expectedID := "0x" + hex.EncodeToString(h[:20])
	if !strings.EqualFold(expectedID, fundusID) {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pubBytes), data, sigBytes)
}

// =============================================================================
//  X25519 ECDH + Argon2id-Härtung
// =============================================================================

// ECDHSharedSecret berechnet einen gehärteten Shared Secret via X25519 + Argon2id.
//
// X25519 ist timing-resistent (Constant-time). Der Shared Secret wird dann
// nochmals mit Argon2id gehärtet um strukturelle Schwächen von ECDH zu eliminieren.
func (id *Identity) ECDHSharedSecret(theirPublicKeyHex string) ([]byte, error) {
	theirPubBytes, err := hex.DecodeString(strings.TrimPrefix(theirPublicKeyHex, "0x"))
	if err != nil || len(theirPubBytes) != 32 {
		return nil, fmt.Errorf("identity: X25519 PubKey muss 32 Bytes sein")
	}

	// X25519 ECDH: constant-time
	var theirPub [32]byte
	copy(theirPub[:], theirPubBytes)

	sharedPoint, err := curve25519.X25519(id.x25519Priv[:], theirPub[:])
	if err != nil {
		return nil, fmt.Errorf("identity: X25519: %w", err)
	}

	// Argon2id-Härtung des Shared Points.
	// Salt aus den SORTIERTEN X25519-PubKeys beider Seiten — die sind auf beiden
	// Seiten identisch verfügbar (eigener + fremder X25519-Pub) und damit
	// symmetrisch. FRÜHER: FundusIDs, aber die eine kam aus Ed25519 (eigene) und
	// die andere aus blake3(X25519) der Gegenseite → asymmetrisch → verschiedene
	// Salts auf Sender/Empfänger → "message authentication failed".
	myPubBytes, _ := curve25519.X25519(id.x25519Priv[:], curve25519.Basepoint)
	pk := []string{hex.EncodeToString(myPubBytes), hex.EncodeToString(theirPubBytes)}
	if pk[0] > pk[1] { pk[0], pk[1] = pk[1], pk[0] }
	salt := []byte(saltECDH + pk[0] + ":" + pk[1])

	hardened := argon2.IDKey(sharedPoint, salt, a2ECDHTime, a2ECDHMemory, a2Threads, a2KeyLen)
	return hardened, nil
}

// X25519PublicKey gibt den X25519 Public Key zurück (32 Bytes, hex).
// Dieser wird für ECDH verwendet, der Ed25519 Public Key für Signaturen.
func (id *Identity) X25519PublicKeyHex() string {
	pub, _ := curve25519.X25519(id.x25519Priv[:], curve25519.Basepoint)
	return hex.EncodeToString(pub)
}

func pubKeyToFundusID(pubBytes []byte) string {
	h := blake3Sum256(pubBytes)
	return "0x" + hex.EncodeToString(h[:20])
}

// =============================================================================
//  XChaCha20-Poly1305 Verschlüsselung
// =============================================================================

// Encrypt verschlüsselt Daten mit XChaCha20-Poly1305.
// 192-Bit Nonce → zufällig generierbar ohne Kollisionsrisiko.
func Encrypt(plain, key []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:32])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, nil), nil
}

// Decrypt entschlüsselt XChaCha20-Poly1305 Daten.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:32])
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < aead.NonceSize() {
		return nil, errors.New("identity: ciphertext zu kurz")
	}
	return aead.Open(nil, ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():], nil)
}

// AtRestKey leitet einen stabilen, geräte-lokalen Schlüssel zum Verschlüsseln
// persistenter Daten (z.B. des Nachrichtenverlaufs) auf der Disk ab. Domain-
// separiert vom Signatur-/ECDH-Schlüssel, damit at-rest-Daten kryptografisch
// nichts mit Netz-Nachrichten gemein haben. Wird NICHT exportiert/übertragen.
func (i *Identity) AtRestKey() [32]byte {
	// BLAKE3-256(domain + ed25519Priv-Seed) → 32 Byte. Der private Ed25519-Key
	// existiert nur im RAM, also ist der abgeleitete Schlüssel an die
	// eingeloggte Identität gebunden (kein Klartext-Verlauf ohne Login).
	seed := i.ed25519Key.Seed()
	return blake3Sum256(append([]byte("fundus-atrest-v1|"), seed...))
}

// =============================================================================
//  Datei-Zugriffssteuerung
// =============================================================================

// AccessGrant erlaubt einer FundusID den Zugriff auf eine Datei.
type AccessGrant struct {
	ContentHash   string    `json:"content_hash"`
	GranteeID     string    `json:"grantee_id"`
	GranteeX25519 string    `json:"grantee_x25519"` // X25519 Public Key des Empfängers
	EncryptedKey  string    `json:"encrypted_key"`   // XChaCha20(fileKey, ecdhSecret)
	Write         bool      `json:"write"`
	GrantedAt     time.Time `json:"granted_at"`
	GrantedBy     string    `json:"granted_by"`
	Signature     string    `json:"signature"`
}

// GrantAccess verschlüsselt den Datei-Schlüssel für einen Empfänger.
func (id *Identity) GrantAccess(contentHash string, fileKey []byte, theirX25519Hex string, write bool) (*AccessGrant, error) {
	secret, err := id.ECDHSharedSecret(theirX25519Hex)
	if err != nil {
		return nil, err
	}
	encKey, err := Encrypt(fileKey, secret)
	if err != nil {
		return nil, err
	}

	theirPubBytes, _ := hex.DecodeString(theirX25519Hex)
	theirID := pubKeyToFundusID(theirPubBytes)

	grant := &AccessGrant{
		ContentHash:   contentHash,
		GranteeID:     theirID,
		GranteeX25519: theirX25519Hex,
		EncryptedKey:  hex.EncodeToString(encKey),
		Write:         write,
		GrantedAt:     time.Now().UTC(),
		GrantedBy:     id.FundusID,
	}
	data, _ := json.Marshal(grant)
	sig, err := id.Sign(data)
	if err != nil {
		return nil, err
	}
	grant.Signature = sig
	return grant, nil
}

// DecryptFileKey entschlüsselt den Datei-Schlüssel aus einem Grant.
func (id *Identity) DecryptFileKey(grant *AccessGrant) ([]byte, error) {
	if !strings.EqualFold(grant.GranteeID, id.FundusID) {
		return nil, errors.New("identity: Grant nicht für diese Identität")
	}
	secret, err := id.ECDHSharedSecret(grant.GranteeX25519)
	if err != nil {
		return nil, err
	}
	encKey, err := hex.DecodeString(grant.EncryptedKey)
	if err != nil {
		return nil, err
	}
	return Decrypt(encKey, secret)
}

// =============================================================================
//  Öffentlicher Record
// =============================================================================

// PublicRecord ist der öffentlich teilbare Teil.
type PublicRecord struct {
	FundusID     string    `json:"fundus_id"`      // für Identifikation
	Ed25519PubKey string   `json:"ed25519_pub_key"` // für Signaturen
	X25519PubKey  string   `json:"x25519_pub_key"`  // für ECDH/Encryption
	CreatedAt    time.Time `json:"created_at"`
}

func (id *Identity) PublicRecord() PublicRecord {
	return PublicRecord{
		FundusID:      id.FundusID,
		Ed25519PubKey: id.PublicKeyHex,
		X25519PubKey:  id.X25519PublicKeyHex(),
		CreatedAt:     id.DerivedAt,
	}
}

// =============================================================================
//  Email-Normalisierung
// =============================================================================

func normalizeEmail(email string) string {
	email = strings.TrimFunc(email, unicode.IsSpace)
	email = norm.NFC.String(email)
	parts := strings.SplitN(email, "@", 2)
	if len(parts) == 2 {
		return strings.ToLower(parts[0]) + "@" + strings.ToLower(parts[1])
	}
	return strings.ToLower(email)
}

func isValidEmail(email string) bool {
	parts := strings.SplitN(email, "@", 2)
	return len(parts) == 2 && len(parts[0]) > 0 &&
		strings.Contains(parts[1], ".") && len(parts[1]) >= 3
}


// =============================================================================
//  Passwort-Stärke-Validierung
// =============================================================================

// PasswordStrength beschreibt die Stärke eines Passworts.
type PasswordStrength struct {
	Score    int    // 0–4 (0=sehr schwach, 4=sehr stark)
	Label    string // "Sehr schwach" … "Sehr stark"
	Entropy  float64 // Geschätzte Entropie in Bit
	Feedback string // Konkreter Hinweis
}

// ValidatePasswordStrength prüft ob ein Passwort die Mindestanforderungen erfüllt.
// Mindestlänge: 14 Zeichen (schützt auch gegen NVL72-Angriffe bei 256 MiB Argon2id).
func ValidatePasswordStrength(password string) error {
	if len(password) < 14 {
		return fmt.Errorf(
			"identity: passwort min. 14 Zeichen (aktuell %d) – "+
				"schützt vor GPU-Angriffen auch mit zukünftiger Hardware",
			len(password))
	}
	s := MeasurePasswordStrength(password)
	if s.Score < 2 {
		return fmt.Errorf("identity: passwort zu schwach (%s) – %s", s.Label, s.Feedback)
	}
	return nil
}

// MeasurePasswordStrength berechnet die Stärke eines Passworts (kein Netzwerk-Call).
func MeasurePasswordStrength(password string) PasswordStrength {
	var (
		hasLower  bool
		hasUpper  bool
		hasDigit  bool
		hasSymbol bool
		uniqueChars = make(map[rune]bool)
	)
	for _, ch := range password {
		uniqueChars[ch] = true
		switch {
		case ch >= 'a' && ch <= 'z': hasLower = true
		case ch >= 'A' && ch <= 'Z': hasUpper = true
		case ch >= '0' && ch <= '9': hasDigit = true
		default:                     hasSymbol = true
		}
	}

	// Zeichensatz-Größe schätzen
	charsetSize := 0
	if hasLower  { charsetSize += 26 }
	if hasUpper  { charsetSize += 26 }
	if hasDigit  { charsetSize += 10 }
	if hasSymbol { charsetSize += 32 }
	if charsetSize == 0 { charsetSize = 26 }

	// Entropie = log2(charsetSize^len)
	entropy := float64(len(password)) * math.Log2(float64(charsetSize))

	// Bonus für lange Passwörter
	if len(password) >= 20 { entropy *= 1.1 }
	if len(password) >= 30 { entropy *= 1.2 }

	// Score 0–4
	score := 0
	switch {
	case entropy >= 80: score = 4
	case entropy >= 60: score = 3
	case entropy >= 40: score = 2
	case entropy >= 25: score = 1
	default:            score = 0
	}

	// Mindestlänge zwingt Score nach oben
	if len(password) < 14 && score > 1 { score = 1 }

	labels   := []string{"Sehr schwach", "Schwach", "Mittel", "Stark", "Sehr stark"}
	feedbacks := []string{
		"Mindestens 14 Zeichen, Mix aus Groß/Klein/Ziffern/Sonderzeichen",
		"Mehr Zeichen oder gemischten Zeichensatz verwenden",
		"Gut – für kritische Wallets lieber 20+ Zeichen",
		"Stark – für maximale Sicherheit 30+ Zeichen zufällig",
		"Ausgezeichnet",
	}

	return PasswordStrength{
		Score:    score,
		Label:    labels[score],
		Entropy:  entropy,
		Feedback: feedbacks[score],
	}
}


// blake3Sum256 berechnet den Hash für Identitäts-Ableitungen (BLAKE3-256,
// vereinheitlicht mit der Chain-Adressableitung).
func blake3Sum256(data []byte) [32]byte {
	return blake3.Sum256(data)
}

// =============================================================================
//  Wallet-Generierung (Web-UI)
// =============================================================================

// WordsFromEmailPassword leitet deterministisch 30 Seed-Wörter aus Email+Passwort
// ab. Gleiche Eingabe → gleiche Wörter → gleiche Wallet. Argon2id streckt die
// Eingabe gegen Brute-Force; trotzdem ist die Sicherheit nur so gut wie das
// Passwort (daher Mindestlänge im Aufrufer erzwingen).
func WordsFromEmailPassword(email, password string) ([]string, error) {
	email = textunicode.NFC.String(strings.ToLower(strings.TrimSpace(email)))
	password = textunicode.NFC.String(password)
	if email == "" || password == "" {
		return nil, fmt.Errorf("identity: email und passwort erforderlich")
	}
	const wordCount = 30
	wordlist := bip39Wordlist()
	n := len(wordlist)

	// Argon2id über "email\x00password" → 60 Bytes (2 pro Wort)
	input := append([]byte(email), 0)
	input = append(input, []byte(password)...)
	salt := []byte("fundus-wallet-emailpw-v1")
	raw := argon2.IDKey(input, salt, a2WalletTime, a2WalletMemory, a2WalletThreads, uint32(wordCount*2))
	for i := range input {
		input[i] = 0
	}

	words := make([]string, wordCount)
	for i := 0; i < wordCount; i++ {
		v := int(raw[i*2])<<8 | int(raw[i*2+1])
		words[i] = wordlist[v%n]
	}
	for i := range raw {
		raw[i] = 0
	}
	return words, nil
}

// GenerateWallet generiert 30 zufällige Seed-Wörter und leitet die Wallet-Adresse ab.
// Gibt Wörter + Adresse zurück. Private Key wird sofort verworfen.
func GenerateWallet() (words []string, address string, err error) {
	// 30 Wörter aus 2048 = 30 × 11 Bit = 330 Bit Entropie (weit über BIP39-Standard)
	const wordCount = 30
	wordlist := bip39Wordlist() // interne englische Wortliste (2048 Wörter)

	// Rejection Sampling gegen Modulo-Bias (gleichverteilte Indizes)
	words = make([]string, wordCount)
	n := len(wordlist)
	maxValid := 65536 - (65536 % n) // größtes Vielfaches von n unter 2^16
	for i := 0; i < wordCount; i++ {
		for {
			b := make([]byte, 2)
			if _, err = rand.Read(b); err != nil {
				return nil, "", fmt.Errorf("identity: random words: %w", err)
			}
			v := int(b[0])<<8 | int(b[1])
			if v < maxValid {
				words[i] = wordlist[v%n]
				break
			}
		}
	}

	address, err = DeriveAddressFromSeed(words)
	if err != nil {
		return nil, "", err
	}
	return words, address, nil
}

// DerivePrivateKeyFromSeed leitet den secp256k1-PrivateKey aus Seed-Wörtern ab
// (gleiche Ableitung wie DeriveAddressFromSeed). Der Aufrufer MUSS den Key nach
// Gebrauch nullen (key.D.SetInt64(0)). Für ephemeres Signieren von Transaktionen.
func DerivePrivateKeyFromSeed(words []string) (*ecdsa.PrivateKey, error) {
	norm := make([]string, 0, len(words))
	for _, w := range words {
		w = textunicode.NFC.String(strings.TrimFunc(w, unicode.IsSpace))
		if w != "" {
			norm = append(norm, w)
		}
	}
	if len(norm) == 0 {
		return nil, fmt.Errorf("identity: keine Seed-Wörter")
	}
	password := []byte(strings.Join(norm, "\n"))
	salt := []byte("fundus-fnd-v2")
	keyBytes := argon2.IDKey(password, salt, a2WalletTime, a2WalletMemory, a2WalletThreads, 32)
	for i := range password {
		password[i] = 0
	}
	privKey, err := crypto.ToECDSA(keyBytes)
	for i := range keyBytes {
		keyBytes[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("identity: secp256k1: %w", err)
	}
	return privKey, nil
}

// DeriveAddressFromSeed leitet die Wallet-Adresse aus Seed-Wörtern ab.
// Gibt NUR die öffentliche Adresse zurück. Private Key wird sofort gelöscht.
func DeriveAddressFromSeed(words []string) (string, error) {
	// Argon2id v2 (256 MiB) – gleiche Parameter UND Normalisierung wie fnd-wallet
	// WICHTIG: NFC-Normalisierung muss identisch sein, sonst andere Adresse
	// bei Wörtern mit Umlauten/Akzenten (z.B. "café" vs "cafe\u0301")
	norm := make([]string, 0, len(words))
	for _, w := range words {
		w = textunicode.NFC.String(strings.TrimFunc(w, unicode.IsSpace))
		if w != "" { norm = append(norm, w) }
	}
	if len(norm) == 0 {
		return "", fmt.Errorf("identity: keine Seed-Wörter")
	}

	password := []byte(strings.Join(norm, "\n"))
	salt     := []byte("fundus-fnd-v2")
	keyBytes := argon2.IDKey(password, salt, a2WalletTime, a2WalletMemory, a2WalletThreads, 32)
	for i := range password { password[i] = 0 }

	privKey, err := crypto.ToECDSA(keyBytes)
	for i := range keyBytes { keyBytes[i] = 0 }
	if err != nil {
		return "", fmt.Errorf("identity: secp256k1: %w", err)
	}

	addr := blake3Address(&privKey.PublicKey)
	privKey.D.SetInt64(0)
	return addr, nil
}

// blake3Address leitet die Wallet-/Chain-Adresse aus einem secp256k1-PubKey ab:
// die ersten 20 Bytes von BLAKE3-256(unkomprimierter PubKey ohne 0x04-Präfix).
// MUSS identisch zu chain.PubkeyToAddress sein (Spec §3a) — ein Test in
// identity_address_test.go vergleicht beide Ableitungen byteweise.
func blake3Address(pub *ecdsa.PublicKey) string {
	raw := crypto.FromECDSAPub(pub) // 65 Bytes: 0x04 || X || Y
	if len(raw) != 65 {
		return ""
	}
	h := blake3.Sum256(raw[1:]) // X || Y
	return "0x" + hex.EncodeToString(h[:20])
}

// bip39Wordlist gibt die offizielle BIP39-Wortliste (2048 Wörter, Englisch) für
// die Seed-Generierung zurück. 30 Wörter aus 2048 ≈ 330 bit Entropie – weit über
// dem 128-bit-Sicherheitsniveau. Standard-konform (github.com/bitcoin/bips BIP39).
func bip39Wordlist() []string {
	return []string{
		"abandon","ability","able","about","above","absent","absorb","abstract",
		"absurd","abuse","access","accident","account","accuse","achieve","acid",
		"acoustic","acquire","across","act","action","actor","actress","actual",
		"adapt","add","addict","address","adjust","admit","adult","advance",
		"advice","aerobic","affair","afford","afraid","again","age","agent",
		"agree","ahead","aim","air","airport","aisle","alarm","album",
		"alcohol","alert","alien","all","alley","allow","almost","alone",
		"alpha","already","also","alter","always","amateur","amazing","among",
		"amount","amused","analyst","anchor","ancient","anger","angle","angry",
		"animal","ankle","announce","annual","another","answer","antenna","antique",
		"anxiety","any","apart","apology","appear","apple","approve","april",
		"arch","arctic","area","arena","argue","arm","armed","armor",
		"army","around","arrange","arrest","arrive","arrow","art","artefact",
		"artist","artwork","ask","aspect","assault","asset","assist","assume",
		"asthma","athlete","atom","attack","attend","attitude","attract","auction",
		"audit","august","aunt","author","auto","autumn","average","avocado",
		"avoid","awake","aware","away","awesome","awful","awkward","axis",
		"baby","bachelor","bacon","badge","bag","balance","balcony","ball",
		"bamboo","banana","banner","bar","barely","bargain","barrel","base",
		"basic","basket","battle","beach","bean","beauty","because","become",
		"beef","before","begin","behave","behind","believe","below","belt",
		"bench","benefit","best","betray","better","between","beyond","bicycle",
		"bid","bike","bind","biology","bird","birth","bitter","black",
		"blade","blame","blanket","blast","bleak","bless","blind","blood",
		"blossom","blouse","blue","blur","blush","board","boat","body",
		"boil","bomb","bone","bonus","book","boost","border","boring",
		"borrow","boss","bottom","bounce","box","boy","bracket","brain",
		"brand","brass","brave","bread","breeze","brick","bridge","brief",
		"bright","bring","brisk","broccoli","broken","bronze","broom","brother",
		"brown","brush","bubble","buddy","budget","buffalo","build","bulb",
		"bulk","bullet","bundle","bunker","burden","burger","burst","bus",
		"business","busy","butter","buyer","buzz","cabbage","cabin","cable",
		"cactus","cage","cake","call","calm","camera","camp","can",
		"canal","cancel","candy","cannon","canoe","canvas","canyon","capable",
		"capital","captain","car","carbon","card","cargo","carpet","carry",
		"cart","case","cash","casino","castle","casual","cat","catalog",
		"catch","category","cattle","caught","cause","caution","cave","ceiling",
		"celery","cement","census","century","cereal","certain","chair","chalk",
		"champion","change","chaos","chapter","charge","chase","chat","cheap",
		"check","cheese","chef","cherry","chest","chicken","chief","child",
		"chimney","choice","choose","chronic","chuckle","chunk","churn","cigar",
		"cinnamon","circle","citizen","city","civil","claim","clap","clarify",
		"claw","clay","clean","clerk","clever","click","client","cliff",
		"climb","clinic","clip","clock","clog","close","cloth","cloud",
		"clown","club","clump","cluster","clutch","coach","coast","coconut",
		"code","coffee","coil","coin","collect","color","column","combine",
		"come","comfort","comic","common","company","concert","conduct","confirm",
		"congress","connect","consider","control","convince","cook","cool","copper",
		"copy","coral","core","corn","correct","cost","cotton","couch",
		"country","couple","course","cousin","cover","coyote","crack","cradle",
		"craft","cram","crane","crash","crater","crawl","crazy","cream",
		"credit","creek","crew","cricket","crime","crisp","critic","crop",
		"cross","crouch","crowd","crucial","cruel","cruise","crumble","crunch",
		"crush","cry","crystal","cube","culture","cup","cupboard","curious",
		"current","curtain","curve","cushion","custom","cute","cycle","dad",
		"damage","damp","dance","danger","daring","dash","daughter","dawn",
		"day","deal","debate","debris","decade","december","decide","decline",
		"decorate","decrease","deer","defense","define","defy","degree","delay",
		"deliver","demand","demise","denial","dentist","deny","depart","depend",
		"deposit","depth","deputy","derive","describe","desert","design","desk",
		"despair","destroy","detail","detect","develop","device","devote","diagram",
		"dial","diamond","diary","dice","diesel","diet","differ","digital",
		"dignity","dilemma","dinner","dinosaur","direct","dirt","disagree","discover",
		"disease","dish","dismiss","disorder","display","distance","divert","divide",
		"divorce","dizzy","doctor","document","dog","doll","dolphin","domain",
		"donate","donkey","donor","door","dose","double","dove","draft",
		"dragon","drama","drastic","draw","dream","dress","drift","drill",
		"drink","drip","drive","drop","drum","dry","duck","dumb",
		"dune","during","dust","dutch","duty","dwarf","dynamic","eager",
		"eagle","early","earn","earth","easily","east","easy","echo",
		"ecology","economy","edge","edit","educate","effort","egg","eight",
		"either","elbow","elder","electric","elegant","element","elephant","elevator",
		"elite","else","embark","embody","embrace","emerge","emotion","employ",
		"empower","empty","enable","enact","end","endless","endorse","enemy",
		"energy","enforce","engage","engine","enhance","enjoy","enlist","enough",
		"enrich","enroll","ensure","enter","entire","entry","envelope","episode",
		"equal","equip","era","erase","erode","erosion","error","erupt",
		"escape","essay","essence","estate","eternal","ethics","evidence","evil",
		"evoke","evolve","exact","example","excess","exchange","excite","exclude",
		"excuse","execute","exercise","exhaust","exhibit","exile","exist","exit",
		"exotic","expand","expect","expire","explain","expose","express","extend",
		"extra","eye","eyebrow","fabric","face","faculty","fade","faint",
		"faith","fall","false","fame","family","famous","fan","fancy",
		"fantasy","farm","fashion","fat","fatal","father","fatigue","fault",
		"favorite","feature","february","federal","fee","feed","feel","female",
		"fence","festival","fetch","fever","few","fiber","fiction","field",
		"figure","file","film","filter","final","find","fine","finger",
		"finish","fire","firm","first","fiscal","fish","fit","fitness",
		"fix","flag","flame","flash","flat","flavor","flee","flight",
		"flip","float","flock","floor","flower","fluid","flush","fly",
		"foam","focus","fog","foil","fold","follow","food","foot",
		"force","forest","forget","fork","fortune","forum","forward","fossil",
		"foster","found","fox","fragile","frame","frequent","fresh","friend",
		"fringe","frog","front","frost","frown","frozen","fruit","fuel",
		"fun","funny","furnace","fury","future","gadget","gain","galaxy",
		"gallery","game","gap","garage","garbage","garden","garlic","garment",
		"gas","gasp","gate","gather","gauge","gaze","general","genius",
		"genre","gentle","genuine","gesture","ghost","giant","gift","giggle",
		"ginger","giraffe","girl","give","glad","glance","glare","glass",
		"glide","glimpse","globe","gloom","glory","glove","glow","glue",
		"goat","goddess","gold","good","goose","gorilla","gospel","gossip",
		"govern","gown","grab","grace","grain","grant","grape","grass",
		"gravity","great","green","grid","grief","grit","grocery","group",
		"grow","grunt","guard","guess","guide","guilt","guitar","gun",
		"gym","habit","hair","half","hammer","hamster","hand","happy",
		"harbor","hard","harsh","harvest","hat","have","hawk","hazard",
		"head","health","heart","heavy","hedgehog","height","hello","helmet",
		"help","hen","hero","hidden","high","hill","hint","hip",
		"hire","history","hobby","hockey","hold","hole","holiday","hollow",
		"home","honey","hood","hope","horn","horror","horse","hospital",
		"host","hotel","hour","hover","hub","huge","human","humble",
		"humor","hundred","hungry","hunt","hurdle","hurry","hurt","husband",
		"hybrid","ice","icon","idea","identify","idle","ignore","ill",
		"illegal","illness","image","imitate","immense","immune","impact","impose",
		"improve","impulse","inch","include","income","increase","index","indicate",
		"indoor","industry","infant","inflict","inform","inhale","inherit","initial",
		"inject","injury","inmate","inner","innocent","input","inquiry","insane",
		"insect","inside","inspire","install","intact","interest","into","invest",
		"invite","involve","iron","island","isolate","issue","item","ivory",
		"jacket","jaguar","jar","jazz","jealous","jeans","jelly","jewel",
		"job","join","joke","journey","joy","judge","juice","jump",
		"jungle","junior","junk","just","kangaroo","keen","keep","ketchup",
		"key","kick","kid","kidney","kind","kingdom","kiss","kit",
		"kitchen","kite","kitten","kiwi","knee","knife","knock","know",
		"lab","label","labor","ladder","lady","lake","lamp","language",
		"laptop","large","later","latin","laugh","laundry","lava","law",
		"lawn","lawsuit","layer","lazy","leader","leaf","learn","leave",
		"lecture","left","leg","legal","legend","leisure","lemon","lend",
		"length","lens","leopard","lesson","letter","level","liar","liberty",
		"library","license","life","lift","light","like","limb","limit",
		"link","lion","liquid","list","little","live","lizard","load",
		"loan","lobster","local","lock","logic","lonely","long","loop",
		"lottery","loud","lounge","love","loyal","lucky","luggage","lumber",
		"lunar","lunch","luxury","lyrics","machine","mad","magic","magnet",
		"maid","mail","main","major","make","mammal","man","manage",
		"mandate","mango","mansion","manual","maple","marble","march","margin",
		"marine","market","marriage","mask","mass","master","match","material",
		"math","matrix","matter","maximum","maze","meadow","mean","measure",
		"meat","mechanic","medal","media","melody","melt","member","memory",
		"mention","menu","mercy","merge","merit","merry","mesh","message",
		"metal","method","middle","midnight","milk","million","mimic","mind",
		"minimum","minor","minute","miracle","mirror","misery","miss","mistake",
		"mix","mixed","mixture","mobile","model","modify","mom","moment",
		"monitor","monkey","monster","month","moon","moral","more","morning",
		"mosquito","mother","motion","motor","mountain","mouse","move","movie",
		"much","muffin","mule","multiply","muscle","museum","mushroom","music",
		"must","mutual","myself","mystery","myth","naive","name","napkin",
		"narrow","nasty","nation","nature","near","neck","need","negative",
		"neglect","neither","nephew","nerve","nest","net","network","neutral",
		"never","news","next","nice","night","noble","noise","nominee",
		"noodle","normal","north","nose","notable","note","nothing","notice",
		"novel","now","nuclear","number","nurse","nut","oak","obey",
		"object","oblige","obscure","observe","obtain","obvious","occur","ocean",
		"october","odor","off","offer","office","often","oil","okay",
		"old","olive","olympic","omit","once","one","onion","online",
		"only","open","opera","opinion","oppose","option","orange","orbit",
		"orchard","order","ordinary","organ","orient","original","orphan","ostrich",
		"other","outdoor","outer","output","outside","oval","oven","over",
		"own","owner","oxygen","oyster","ozone","pact","paddle","page",
		"pair","palace","palm","panda","panel","panic","panther","paper",
		"parade","parent","park","parrot","party","pass","patch","path",
		"patient","patrol","pattern","pause","pave","payment","peace","peanut",
		"pear","peasant","pelican","pen","penalty","pencil","people","pepper",
		"perfect","permit","person","pet","phone","photo","phrase","physical",
		"piano","picnic","picture","piece","pig","pigeon","pill","pilot",
		"pink","pioneer","pipe","pistol","pitch","pizza","place","planet",
		"plastic","plate","play","please","pledge","pluck","plug","plunge",
		"poem","poet","point","polar","pole","police","pond","pony",
		"pool","popular","portion","position","possible","post","potato","pottery",
		"poverty","powder","power","practice","praise","predict","prefer","prepare",
		"present","pretty","prevent","price","pride","primary","print","priority",
		"prison","private","prize","problem","process","produce","profit","program",
		"project","promote","proof","property","prosper","protect","proud","provide",
		"public","pudding","pull","pulp","pulse","pumpkin","punch","pupil",
		"puppy","purchase","purity","purpose","purse","push","put","puzzle",
		"pyramid","quality","quantum","quarter","question","quick","quit","quiz",
		"quote","rabbit","raccoon","race","rack","radar","radio","rail",
		"rain","raise","rally","ramp","ranch","random","range","rapid",
		"rare","rate","rather","raven","raw","razor","ready","real",
		"reason","rebel","rebuild","recall","receive","recipe","record","recycle",
		"reduce","reflect","reform","refuse","region","regret","regular","reject",
		"relax","release","relief","rely","remain","remember","remind","remove",
		"render","renew","rent","reopen","repair","repeat","replace","report",
		"require","rescue","resemble","resist","resource","response","result","retire",
		"retreat","return","reunion","reveal","review","reward","rhythm","rib",
		"ribbon","rice","rich","ride","ridge","rifle","right","rigid",
		"ring","riot","ripple","risk","ritual","rival","river","road",
		"roast","robot","robust","rocket","romance","roof","rookie","room",
		"rose","rotate","rough","round","route","royal","rubber","rude",
		"rug","rule","run","runway","rural","sad","saddle","sadness",
		"safe","sail","salad","salmon","salon","salt","salute","same",
		"sample","sand","satisfy","satoshi","sauce","sausage","save","say",
		"scale","scan","scare","scatter","scene","scheme","school","science",
		"scissors","scorpion","scout","scrap","screen","script","scrub","sea",
		"search","season","seat","second","secret","section","security","seed",
		"seek","segment","select","sell","seminar","senior","sense","sentence",
		"series","service","session","settle","setup","seven","shadow","shaft",
		"shallow","share","shed","shell","sheriff","shield","shift","shine",
		"ship","shiver","shock","shoe","shoot","shop","short","shoulder",
		"shove","shrimp","shrug","shuffle","shy","sibling","sick","side",
		"siege","sight","sign","silent","silk","silly","silver","similar",
		"simple","since","sing","siren","sister","situate","six","size",
		"skate","sketch","ski","skill","skin","skirt","skull","slab",
		"slam","sleep","slender","slice","slide","slight","slim","slogan",
		"slot","slow","slush","small","smart","smile","smoke","smooth",
		"snack","snake","snap","sniff","snow","soap","soccer","social",
		"sock","soda","soft","solar","soldier","solid","solution","solve",
		"someone","song","soon","sorry","sort","soul","sound","soup",
		"source","south","space","spare","spatial","spawn","speak","special",
		"speed","spell","spend","sphere","spice","spider","spike","spin",
		"spirit","split","spoil","sponsor","spoon","sport","spot","spray",
		"spread","spring","spy","square","squeeze","squirrel","stable","stadium",
		"staff","stage","stairs","stamp","stand","start","state","stay",
		"steak","steel","stem","step","stereo","stick","still","sting",
		"stock","stomach","stone","stool","story","stove","strategy","street",
		"strike","strong","struggle","student","stuff","stumble","style","subject",
		"submit","subway","success","such","sudden","suffer","sugar","suggest",
		"suit","summer","sun","sunny","sunset","super","supply","supreme",
		"sure","surface","surge","surprise","surround","survey","suspect","sustain",
		"swallow","swamp","swap","swarm","swear","sweet","swift","swim",
		"swing","switch","sword","symbol","symptom","syrup","system","table",
		"tackle","tag","tail","talent","talk","tank","tape","target",
		"task","taste","tattoo","taxi","teach","team","tell","ten",
		"tenant","tennis","tent","term","test","text","thank","that",
		"theme","then","theory","there","they","thing","this","thought",
		"three","thrive","throw","thumb","thunder","ticket","tide","tiger",
		"tilt","timber","time","tiny","tip","tired","tissue","title",
		"toast","tobacco","today","toddler","toe","together","toilet","token",
		"tomato","tomorrow","tone","tongue","tonight","tool","tooth","top",
		"topic","topple","torch","tornado","tortoise","toss","total","tourist",
		"toward","tower","town","toy","track","trade","traffic","tragic",
		"train","transfer","trap","trash","travel","tray","treat","tree",
		"trend","trial","tribe","trick","trigger","trim","trip","trophy",
		"trouble","truck","true","truly","trumpet","trust","truth","try",
		"tube","tuition","tumble","tuna","tunnel","turkey","turn","turtle",
		"twelve","twenty","twice","twin","twist","two","type","typical",
		"ugly","umbrella","unable","unaware","uncle","uncover","under","undo",
		"unfair","unfold","unhappy","uniform","unique","unit","universe","unknown",
		"unlock","until","unusual","unveil","update","upgrade","uphold","upon",
		"upper","upset","urban","urge","usage","use","used","useful",
		"useless","usual","utility","vacant","vacuum","vague","valid","valley",
		"valve","van","vanish","vapor","various","vast","vault","vehicle",
		"velvet","vendor","venture","venue","verb","verify","version","very",
		"vessel","veteran","viable","vibrant","vicious","victory","video","view",
		"village","vintage","violin","virtual","virus","visa","visit","visual",
		"vital","vivid","vocal","voice","void","volcano","volume","vote",
		"voyage","wage","wagon","wait","walk","wall","walnut","want",
		"warfare","warm","warrior","wash","wasp","waste","water","wave",
		"way","wealth","weapon","wear","weasel","weather","web","wedding",
		"weekend","weird","welcome","west","wet","whale","what","wheat",
		"wheel","when","where","whip","whisper","wide","width","wife",
		"wild","will","win","window","wine","wing","wink","winner",
		"winter","wire","wisdom","wise","wish","witness","wolf","woman",
		"wonder","wood","wool","word","work","world","worry","worth",
		"wrap","wreck","wrestle","wrist","write","wrong","yard","year",
		"yellow","you","young","youth","zebra","zero","zone","zoo",
	}
}


// SelfEncrypt verschlüsselt Daten für den Besitzer selbst (z.B. die private
// Kontaktliste). Nutzt ECDH mit dem EIGENEN X25519-PublicKey → ein Geheimnis,
// das nur diese Identität reproduzieren kann. So können persönliche Daten
// verschlüsselt im Netz abgelegt und an jedem Node wiederhergestellt werden.
func (id *Identity) SelfEncrypt(plain []byte) ([]byte, error) {
	secret, err := id.ECDHSharedSecret(id.X25519PublicKeyHex())
	if err != nil {
		return nil, fmt.Errorf("identity: self-ecdh: %w", err)
	}
	return Encrypt(plain, secret)
}

// SelfDecrypt entschlüsselt mit SelfEncrypt verschlüsselte Daten.
func (id *Identity) SelfDecrypt(ciphertext []byte) ([]byte, error) {
	secret, err := id.ECDHSharedSecret(id.X25519PublicKeyHex())
	if err != nil {
		return nil, fmt.Errorf("identity: self-ecdh: %w", err)
	}
	return Decrypt(ciphertext, secret)
}

// deriveFromWords ist der EINE gemeinsame Ableitungsweg: aus den Seed-Wörtern
// werden BEIDE Schlüssel deterministisch erzeugt — Ed25519 (Messenger/FundusID)
// und secp256k1 (Chain-Wallet). Sowohl der email+password-Login (der intern
// zuerst die Wörter erzeugt) als auch der direkte Wörter-Login rufen dies auf,
// sodass beide Wege GARANTIERT dieselbe Identität ergeben.
func deriveFromWords(words []string) (*Identity, error) {
	norm := make([]string, 0, len(words))
	for _, w := range words {
		w = textunicode.NFC.String(strings.TrimFunc(w, unicode.IsSpace))
		if w != "" {
			norm = append(norm, strings.ToLower(w))
		}
	}
	if len(norm) < 10 {
		return nil, fmt.Errorf("identity: zu wenige Seed-Wörter (%d)", len(norm))
	}
	joined := []byte(strings.Join(norm, "\n"))

	// Ed25519-Seed aus den Wörtern (eigener Salt, getrennt vom Chain-Key).
	edSeed := argon2.IDKey(joined, []byte("fundus-ident-ed25519-v1"), a2WalletTime, a2WalletMemory, a2WalletThreads, 32)
	ed25519Priv := ed25519.NewKeyFromSeed(edSeed)
	ed25519Pub := ed25519Priv.Public().(ed25519.PublicKey)

	// X25519 für ECDH: erste 32 Bytes des Ed25519-Seeds + Curve25519-Clamp
	// (identisch zur bestehenden Derive-Methode, damit ECDH kompatibel bleibt).
	var x25519Priv [32]byte
	copy(x25519Priv[:], edSeed)
	clampX25519(&x25519Priv)
	for i := range edSeed {
		edSeed[i] = 0
	}

	// FundusID = BLAKE3-256(Ed25519 PubKey)[:20]
	h := blake3Sum256(ed25519Pub)
	fundusID := "0x" + hex.EncodeToString(h[:20])

	// Chain-Wallet (secp256k1) wird NICHT beim Login abgeleitet — das wäre ein
	// dritter, teurer Argon2-Durchlauf, der für Login/Messenger unnötig ist. Die
	// Adresse wird erst bei Bedarf (FND-Transfer, Anzeige) aus seedWords berechnet
	// (ChainAddr()-Methode), deterministisch und ohne Sicherheitsverlust.
	return &Identity{
		FundusID:     fundusID,
		PublicKeyHex: hex.EncodeToString(ed25519Pub),
		ed25519Key:   ed25519Priv,
		x25519Priv:   x25519Priv,
		seedWords:    norm,
		DerivedAt:    time.Now().UTC(),
	}, nil
}

// DeriveFromWords: öffentlicher Einstieg für den Wörter-Login.
func DeriveFromWords(words []string) (*Identity, error) {
	return deriveFromWords(words)
}

// Blake3Sum256 ist der exportierte Wrapper für BLAKE3-256 (z.B. für den
// Email-Verzeichnis-Schlüssel).
func Blake3Sum256(data []byte) [32]byte {
	return blake3Sum256(data)
}

// ── Sitzungswiederherstellung ohne erneutes Argon2 ──────────────────────────

// SessionSecret enthält, was eine angemeldete Sitzung nach einem Neustart des
// Nodes braucht: den bereits abgeleiteten Ed25519-Seed (spart den teuren
// Argon2-Durchlauf mit 512 MiB), die Seed-Wörter (Wallet-Export, ChainAddr) und
// die ggf. schon berechnete Chain-Adresse. NUR verschlüsselt speichern.
type SessionSecret struct {
	EdSeed    []byte   `json:"s"`
	Words     []string `json:"w"`
	ChainAddr string   `json:"c,omitempty"`
}

// ExportSessionSecret liefert die Geheimnisse dieser Identität für die
// verschlüsselte Sitzungsablage.
func (id *Identity) ExportSessionSecret() SessionSecret {
	return SessionSecret{
		EdSeed:    append([]byte(nil), id.ed25519Key.Seed()...),
		Words:     append([]string(nil), id.seedWords...),
		ChainAddr: id.chainAddrCache,
	}
}

// FromSessionSecret stellt eine Identität aus einem SessionSecret wieder her –
// in Millisekunden, ohne Argon2. Ergibt exakt dieselben Schlüssel wie der Login.
func FromSessionSecret(sec SessionSecret) (*Identity, error) {
	if len(sec.EdSeed) != ed25519.SeedSize {
		return nil, errors.New("identity: Sitzungsschlüssel ungültig")
	}
	priv := ed25519.NewKeyFromSeed(sec.EdSeed)
	pub := priv.Public().(ed25519.PublicKey)
	var x25519Priv [32]byte
	copy(x25519Priv[:], sec.EdSeed)
	clampX25519(&x25519Priv)
	h := blake3Sum256(pub)
	return &Identity{
		FundusID:       "0x" + hex.EncodeToString(h[:20]),
		PublicKeyHex:   hex.EncodeToString(pub),
		ed25519Key:     priv,
		x25519Priv:     x25519Priv,
		seedWords:      append([]string(nil), sec.Words...),
		chainAddrCache: sec.ChainAddr,
		DerivedAt:      time.Now().UTC(),
	}, nil
}
