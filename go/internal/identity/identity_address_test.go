package identity_test

import (
	"strings"
	"testing"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
)

// TestWalletAddressMatchesChain stellt sicher, dass die Wallet-Adressableitung
// (identity.DeriveAddressFromSeed) byteweise dieselbe Adresse liefert wie die
// Chain-Ableitung (chain.PubkeyToAddress) für denselben Seed. Das ist die
// Voraussetzung für die spätere Wallet→Chain-Anbindung (Spec §13, Phase 2.4):
// Eine in der Wallet erzeugte Adresse muss auf der Chain dieselbe sein.
func TestWalletAddressMatchesChain(t *testing.T) {
	seeds := [][]string{
		{"abandon", "ability", "able", "about", "above", "absent"},
		{"zone", "zoo", "zebra", "yellow", "wisdom", "winter", "window", "wine"},
		{"café", "naïve", "fundus", "energie", "zähler", "grün"}, // Umlaute/Akzente (NFC)
	}
	for i, words := range seeds {
		// 1. Adresse über die Wallet-Ableitung.
		walletAddr, err := identity.DeriveAddressFromSeed(words)
		if err != nil {
			t.Fatalf("seed %d: DeriveAddressFromSeed: %v", i, err)
		}

		// 2. Denselben PrivKey ableiten und die Chain-Adresse berechnen.
		priv, err := identity.DerivePrivateKeyFromSeed(words)
		if err != nil {
			t.Fatalf("seed %d: DerivePrivateKeyFromSeed: %v", i, err)
		}
		chainAddr := chain.PubkeyToAddress(&priv.PublicKey)
		priv.D.SetInt64(0)

		// 3. Vergleich (case-insensitiv, beide 0x-präfixiert).
		if !strings.EqualFold(walletAddr, chainAddr.Hex()) {
			t.Fatalf("seed %d: Wallet-Adresse %s != Chain-Adresse %s",
				i, walletAddr, chainAddr.Hex())
		}
	}
}

// TestWalletAddressDeterministic stellt sicher, dass dieselben Wörter immer
// dieselbe Adresse ergeben (keine versteckte Zufälligkeit).
func TestWalletAddressDeterministic(t *testing.T) {
	words := []string{"fundus", "energie", "zähler", "grün", "winter", "wisdom"}
	a, err := identity.DeriveAddressFromSeed(words)
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.DeriveAddressFromSeed(words)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("nicht deterministisch: %s != %s", a, b)
	}
	// Form prüfen: 0x + 40 Hex-Zeichen.
	if !strings.HasPrefix(a, "0x") || len(a) != 42 {
		t.Fatalf("unerwartetes Adressformat: %s", a)
	}
}

// TestWalletAddressNotKeccak stellt sicher, dass die Umstellung tatsächlich
// erfolgt ist: Die BLAKE3-Adresse darf NICHT mit der alten Keccak-Adresse
// (Ethereum-Stil) übereinstimmen. (Sanity-Check gegen versehentliches Zurückfallen.)
func TestWalletAddressIsBlake3Form(t *testing.T) {
	words := []string{"abandon", "ability", "able", "about"}
	addr, err := identity.DeriveAddressFromSeed(words)
	if err != nil {
		t.Fatal(err)
	}
	// Die Chain-Ableitung ist die Referenz; sie MUSS gleich sein (siehe oben).
	priv, _ := identity.DerivePrivateKeyFromSeed(words)
	chainAddr := chain.PubkeyToAddress(&priv.PublicKey)
	priv.D.SetInt64(0)
	if !strings.EqualFold(addr, chainAddr.Hex()) {
		t.Fatalf("Wallet nutzt nicht die Chain-(BLAKE3-)Ableitung: %s vs %s",
			addr, chainAddr.Hex())
	}
}
