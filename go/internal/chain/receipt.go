package chain

import (
	"crypto/ecdsa"
	"encoding/binary"
	"errors"

	"github.com/ethereum/go-ethereum/crypto"
)

// =============================================================================
//  Quittungen (Receipts) — fälschungssichere Nachweise für Storage/Transfer
// =============================================================================
//
// Problem: Ein Provider-Node behauptet, N Bytes für andere gespeichert oder
// geliefert zu haben, und will dafür FND minten. Reines Selbst-Reporting wäre
// trivial fälschbar (jeder druckt sich beliebig FND). Lösung: Die GEGENSEITE
// (der Konsument, der den Dienst nutzt) signiert eine Quittung. Da der Provider
// den privaten Schlüssel des Konsumenten nicht besitzt, kann er sich keine
// Quittungen selbst ausstellen.
//
// Beim späteren Minten legt der Provider die gesammelten Quittungen vor; die
// Chain verifiziert die Signaturen (Konsument == Unterzeichner) und mintet nur
// gegen gültige, nicht-doppelt-eingereichte Nachweise.

// ReceiptKind unterscheidet die beiden vergüteten Leistungen.
type ReceiptKind uint8

const (
	ReceiptFetch ReceiptKind = 1 // Konsument hat N Bytes vom Provider EMPFANGEN (Transfer)
	ReceiptStore ReceiptKind = 2 // Konsument hat N Bytes beim Provider EINGELAGERT
	// ReceiptHosting bezeugt VORHALTUNG über Zeit: Der Konsument hat per Challenge
	// geprüft, dass der Provider einen eingelagerten Chunk noch hält, und quittiert
	// die seit der letzten Quittung vergangene Haltedauer. Basis der zeitbasierten
	// Vergütung (1 FND / TB·Monat). Anders als Fetch/Store trägt sie DurationSeconds.
	ReceiptHosting ReceiptKind = 3
)

// Receipt ist eine vom Konsumenten signierte Bestätigung einer erbrachten
// Storage-/Transfer-Leistung. Sie ist der fälschungssichere Nachweis, den der
// Provider beim Minten vorlegt.
//
// Felder gehen vollständig in den signierten Digest ein (siehe signingBytes),
// damit nichts nachträglich manipuliert werden kann.
type Receipt struct {
	Kind      ReceiptKind // Fetch oder Store
	Provider  Address     // Node, der die Leistung erbracht hat (Empfänger der Vergütung)
	Consumer  Address     // Node, der den Dienst genutzt hat (Unterzeichner)
	ChunkHash [32]byte    // Identifiziert den konkreten Chunk (gegen Doppel-Quittung)
	Bytes     uint64      // Anzahl Bytes (Basis der Vergütung)
	// DurationSeconds: nur bei ReceiptHosting gesetzt — die bezeugte Haltedauer
	// in Sekunden seit der letzten Vorhaltungs-Quittung. Bei Fetch/Store = 0.
	// Die zeitbasierte Vergütung ist Bytes × Dauer (1 FND / TB·Monat).
	DurationSeconds uint64
	Timestamp int64       // Unix-Sekunden (Gültigkeitsfenster / Replay-Schutz)
	Nonce     uint64      // Zufalls-Nonce des Konsumenten (eindeutige Quittungs-ID)
	Signature []byte      // secp256k1-Signatur des Consumers über signingBytes()
}

// signingBytes serialisiert die Quittung deterministisch für Signatur/Verify.
// Reihenfolge und Länge sind fix — jede Änderung bricht bestehende Signaturen.
func (r *Receipt) signingBytes() []byte {
	buf := make([]byte, 0, 1+AddressLen+AddressLen+32+8+8+8+8)
	buf = append(buf, byte(r.Kind))
	buf = append(buf, r.Provider[:]...)
	buf = append(buf, r.Consumer[:]...)
	buf = append(buf, r.ChunkHash[:]...)
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], r.Bytes)
	buf = append(buf, tmp[:]...)
	// DurationSeconds MUSS mitsigniert werden, sonst könnte ein Provider die
	// bezeugte Haltedauer nachträglich hochsetzen und mehr FND minten.
	binary.BigEndian.PutUint64(tmp[:], r.DurationSeconds)
	buf = append(buf, tmp[:]...)
	binary.BigEndian.PutUint64(tmp[:], uint64(r.Timestamp))
	buf = append(buf, tmp[:]...)
	binary.BigEndian.PutUint64(tmp[:], r.Nonce)
	buf = append(buf, tmp[:]...)
	return buf
}

// ReceiptID ist der eindeutige Hash einer Quittung (BLAKE3 über signingBytes).
// Dient als Schlüssel gegen doppelte Einreichung derselben Quittung.
func (r *Receipt) ReceiptID() [32]byte {
	return chainHash(r.signingBytes())
}

// SignReceipt signiert die Quittung mit dem Schlüssel des KONSUMENTEN und setzt
// Consumer (aus dem PubKey abgeleitet) sowie Signature. Der Digest ist
// BLAKE3-256(signingBytes), dieselbe Konstruktion wie bei Transaktionen.
func SignReceipt(r *Receipt, consumerKey *ecdsa.PrivateKey) error {
	if consumerKey == nil {
		return errors.New("chain: kein Konsumenten-Schlüssel")
	}
	r.Consumer = PubkeyToAddress(&consumerKey.PublicKey)
	digest := chainHash(r.signingBytes())
	sig, err := crypto.Sign(digest[:], consumerKey)
	if err != nil {
		return err
	}
	r.Signature = sig
	return nil
}

// VerifyReceipt prüft, dass die Signatur vom angegebenen Consumer stammt und
// dass Provider/Consumer plausibel sind (verschieden, nicht null). Gibt nil
// zurück, wenn die Quittung gültig ist.
func (r *Receipt) VerifyReceipt() error {
	if len(r.Signature) != 65 {
		return errors.New("chain: Quittungs-Signatur muss 65 Bytes sein")
	}
	if r.Bytes == 0 {
		return errors.New("chain: Quittung über 0 Bytes")
	}
	if r.Kind != ReceiptFetch && r.Kind != ReceiptStore && r.Kind != ReceiptHosting {
		return errors.New("chain: unbekannte Quittungs-Art")
	}
	// Vorhaltungs-Quittungen müssen eine bezeugte Dauer tragen; Transfer/Store
	// dürfen keine haben (sonst könnte man Transfer als Zeitvergütung tarnen).
	if r.Kind == ReceiptHosting {
		if r.DurationSeconds == 0 {
			return errors.New("chain: Vorhaltungs-Quittung ohne Dauer")
		}
	} else if r.DurationSeconds != 0 {
		return errors.New("chain: Transfer-/Store-Quittung darf keine Dauer tragen")
	}
	var zero Address
	if r.Provider == zero || r.Consumer == zero {
		return errors.New("chain: Provider/Consumer darf nicht null sein")
	}
	if r.Provider == r.Consumer {
		// Selbst-Quittung: ein Node kann sich nicht selbst Leistung bescheinigen.
		return errors.New("chain: Provider und Consumer sind identisch (Selbst-Quittung)")
	}
	// Signatur muss zum Consumer-Feld passen (Konsument hat unterschrieben).
	digest := chainHash(r.signingBytes())
	pub, err := crypto.SigToPub(digest[:], r.Signature)
	if err != nil {
		return err
	}
	if PubkeyToAddress(pub) != r.Consumer {
		return errors.New("chain: Signatur passt nicht zum Consumer")
	}
	return nil
}
