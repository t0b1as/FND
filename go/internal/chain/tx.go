package chain

import (
	"crypto/ecdsa"
	"errors"

	"github.com/ethereum/go-ethereum/crypto"
)

// PubkeyToAddress leitet die Adresse aus dem secp256k1-PublicKey ab:
// erste 20 Bytes von BLAKE3-256(unkomprimierter PubKey ohne 0x04-Präfix). Spec §3a.
//
// Hinweis: Bewusst NICHT crypto.PubkeyToAddress (das nutzt Keccak); die Chain
// nutzt einheitlich BLAKE3. Die Wallet-Ableitung (identity.DeriveAddressFromSeed
// und fnd-wallet) wurde auf dieselbe Formel angeglichen (Spec §13, Phase 2.4);
// ein Cross-Check-Test (identity_address_test.go) sichert die Gleichheit ab.
func PubkeyToAddress(pub *ecdsa.PublicKey) Address {
	raw := crypto.FromECDSAPub(pub) // 65 Bytes: 0x04 || X || Y
	var a Address
	if len(raw) == 65 {
		h := chainHash(raw[1:]) // X || Y
		copy(a[:], h[:AddressLen])
	}
	return a
}

// SignTransaction signiert tx mit priv, setzt From (aus dem PubKey abgeleitet)
// und Signature. Der Digest ist BLAKE3-256(signingBytes).
func SignTransaction(tx *Transaction, priv *ecdsa.PrivateKey) error {
	if priv == nil {
		return errors.New("chain: kein Schlüssel")
	}
	tx.From = PubkeyToAddress(&priv.PublicKey)
	digest := chainHash(tx.signingBytes())
	sig, err := crypto.Sign(digest[:], priv)
	if err != nil {
		return err
	}
	tx.Signature = sig
	return nil
}

// recoverSender stellt die Absenderadresse aus der Signatur wieder her.
func (tx *Transaction) recoverSender() (Address, error) {
	if len(tx.Signature) != 65 {
		return Address{}, errors.New("chain: Signatur muss 65 Bytes sein")
	}
	digest := chainHash(tx.signingBytes())
	pub, err := crypto.SigToPub(digest[:], tx.Signature)
	if err != nil {
		return Address{}, err
	}
	return PubkeyToAddress(pub), nil
}

// VerifySignature prüft, ob die Signatur zum angegebenen From passt.
func (tx *Transaction) VerifySignature() error {
	from, err := tx.recoverSender()
	if err != nil {
		return err
	}
	if from != tx.From {
		return errors.New("chain: Signatur passt nicht zum Absender")
	}
	return nil
}
