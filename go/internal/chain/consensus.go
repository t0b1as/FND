package chain

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/crypto"
)

// ─── Proof-of-Authority Konsens ──────────────────────────────────────────────
//
// Fundus nutzt rundenbasiertes Proof-of-Authority (PoA): Eine feste, geordnete
// Menge von Validator-Adressen (das Validator-Set) darf Blöcke produzieren.
// Für jede Blockhöhe ist über eine deterministische Rundenfunktion GENAU EIN
// Validator der berechtigte Proposer. So können nicht zwei Nodes gleichzeitig
// legitime Blöcke auf derselben Höhe bauen — das eliminiert Forks im Normalfall.
//
// Blöcke werden vom Proposer signiert (Header-Hash mit secp256k1). Andere Nodes
// verifizieren: (1) Signatur gültig, (2) Signierer == berechtigter Proposer für
// diese Höhe, (3) Proposer ist im Validator-Set. Nur dann wird der Block
// akzeptiert.
//
// BlockTime steuert das Produktions-Intervall (Sekunden pro Runde).
const BlockTime uint64 = 5

// ValidatorSet ist die geordnete Menge berechtigter Block-Produzenten.
// Die Reihenfolge ist kanonisch (sortiert), damit alle Nodes dieselbe
// Rundenzuordnung berechnen.
type ValidatorSet struct {
	validators []Address // kanonisch sortiert, dedupliziert
}

// NewValidatorSet baut ein Validator-Set aus einer Adressliste (sortiert +
// dedupliziert für Determinismus). Leere Liste ist unzulässig.
func NewValidatorSet(addrs []Address) (*ValidatorSet, error) {
	if len(addrs) == 0 {
		return nil, errors.New("chain: leeres Validator-Set")
	}
	// Deduplizieren
	seen := make(map[Address]bool, len(addrs))
	uniq := make([]Address, 0, len(addrs))
	for _, a := range addrs {
		if !seen[a] {
			seen[a] = true
			uniq = append(uniq, a)
		}
	}
	// Kanonisch sortieren (byteweise), damit jeder Node dieselbe Reihenfolge hat.
	sort.Slice(uniq, func(i, j int) bool {
		return bytes.Compare(uniq[i][:], uniq[j][:]) < 0
	})
	return &ValidatorSet{validators: uniq}, nil
}

// Len liefert die Zahl der Validatoren.
func (vs *ValidatorSet) Len() int { return len(vs.validators) }

// Contains prüft, ob eine Adresse im Set ist.
func (vs *ValidatorSet) Contains(a Address) bool {
	for _, v := range vs.validators {
		if v == a {
			return true
		}
	}
	return false
}

// List liefert eine Kopie der (sortierten) Validator-Adressen.
func (vs *ValidatorSet) List() []Address {
	out := make([]Address, len(vs.validators))
	copy(out, vs.validators)
	return out
}

// ProposerForHeight liefert den berechtigten Proposer für eine Blockhöhe:
// deterministisches Round-Robin über das sortierte Set. Jeder Node berechnet
// damit unabhängig denselben Proposer — Voraussetzung dafür, dass sich alle auf
// dieselbe Kette einigen.
func (vs *ValidatorSet) ProposerForHeight(height uint64) Address {
	return vs.ProposerForRound(height, 0)
}

// ProposerForRound liefert den berechtigten Proposer für eine Höhe und
// Fallback-Runde. Runde 0 ist der primäre Proposer (height % N); jede weitere
// Runde rotiert um einen Validator weiter ((height + round) % N). So kann bei
// Ausfall des primären Proposers der nächste einspringen, ohne die
// deterministische Reihenfolge zu verlieren.
func (vs *ValidatorSet) ProposerForRound(height, round uint64) Address {
	if len(vs.validators) == 0 {
		return Address{}
	}
	idx := (height + round) % uint64(len(vs.validators))
	return vs.validators[idx]
}

// FallbackTimeout ist die Wartezeit pro Fallback-Runde. Bewusst größer als
// BlockTime (= 2×), damit ein Fallback-Proposer erst einspringt, wenn der Block
// des primären Proposers mit hoher Sicherheit propagiert ist. Das reduziert die
// Wahrscheinlichkeit konkurrierender Blöcke (Fork) drastisch: Runde 1 wird erst
// bei 3×BlockTime freigeschaltet, während der primäre Proposer bei BlockTime
// produziert — ein Vorsprung von 2×BlockTime für die Netz-Propagierung.
const FallbackTimeout uint64 = 2 * BlockTime

// RoundForElapsed bestimmt die höchste Fallback-Runde, die nach der verstrichenen
// Zeit seit dem Vorgängerblock erlaubt ist.
//
// Schwellen:
//   - elapsed < BlockTime:        niemand (Rückgabe 0; die Mindest-Blockzeit-
//                                 Sperre im Loop greift ohnehin).
//   - Runde 0 (primär):           ab elapsed >= BlockTime.
//   - Runde r (r>=1, Fallback):   ab elapsed >= BlockTime + r*FallbackTimeout.
//
// Also maxRound = floor((elapsed - BlockTime) / FallbackTimeout). Der große
// Puffer (FallbackTimeout = 2×BlockTime) gibt dem primären Proposer Zeit, seinen
// Block zu verbreiten, bevor ein Fallback überhaupt in Betracht kommt — so
// entstehen konkurrierende Blöcke nur bei echtem, längerem Ausfall/Partition,
// nicht schon bei normalem Timing-Jitter.
func RoundForElapsed(elapsedSeconds uint64) uint64 {
	if BlockTime == 0 || elapsedSeconds < BlockTime {
		return 0
	}
	if FallbackTimeout == 0 {
		return 0
	}
	return (elapsedSeconds - BlockTime) / FallbackTimeout
}

// IsProposerForHeight prüft, ob addr für die gegebene Höhe der berechtigte
// Proposer ist.
func (vs *ValidatorSet) IsProposerForHeight(addr Address, height uint64) bool {
	return vs.ProposerForHeight(height) == addr
}

// IsProposerForRound prüft, ob addr in der gegebenen Fallback-Runde produzieren
// dürfte.
func (vs *ValidatorSet) IsProposerForRound(addr Address, height, round uint64) bool {
	return vs.ProposerForRound(height, round) == addr
}

// Hash liefert einen deterministischen Hash des Validator-Sets (für den
// ValSetHash im BlockHeader — so ist das aktive Set tamper-evident verankert).
func (vs *ValidatorSet) Hash() [32]byte {
	var buf []byte
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(vs.validators)))
	for _, v := range vs.validators {
		buf = append(buf, v[:]...)
	}
	return chainHash(buf)
}

// ─── Block-Signatur (Proposer signiert den Header-Hash) ──────────────────────

// SignBlock signiert den Block-Header-Hash mit dem Proposer-Schlüssel und legt
// die Signatur als einzigen Eintrag ins Commit-Feld. (PoA: eine Proposer-
// Signatur genügt; ein späteres BFT-Upgrade kann mehrere Commit-Signaturen
// sammeln, das Feld ist bereits eine Liste.)
func SignBlock(blk *Block, priv *ecdsa.PrivateKey) error {
	digest := blk.Header.Hash()
	sig, err := crypto.Sign(digest[:], priv)
	if err != nil {
		return err
	}
	blk.Commit = [][]byte{sig}
	return nil
}

// RecoverBlockSigner rekonstruiert die Adresse, die den Block signiert hat.
func RecoverBlockSigner(blk *Block) (Address, error) {
	if len(blk.Commit) == 0 || len(blk.Commit[0]) == 0 {
		return Address{}, errors.New("chain: Block ohne Signatur")
	}
	digest := blk.Header.Hash()
	pub, err := crypto.SigToPub(digest[:], blk.Commit[0])
	if err != nil {
		return Address{}, err
	}
	return PubkeyToAddress(pub), nil
}

// VerifyBlockConsensus prüft die Konsens-Regeln eines Blocks gegen ein
// Validator-Set (ohne Zeit-/Rundenkontext — nimmt Runde aus dem Header, prüft
// aber nicht deren zeitliche Plausibilität). Für den vollständigen Check inkl.
// Fallback-Runden-Plausibilität siehe VerifyBlockConsensusAt.
//
// Prüfungen:
//  1. Der Block trägt eine gültige Proposer-Signatur.
//  2. Der Signierer ist im Validator-Set.
//  3. Der Signierer ist der für Höhe UND Header-Runde berechtigte Proposer.
//  4. Der im Header eingetragene Proposer stimmt mit dem Signierer überein.
//  5. Der ValSetHash im Header entspricht dem aktiven Set.
//
// Genesis (Höhe 0) ist ausgenommen — er hat keinen Proposer/keine Signatur.
func VerifyBlockConsensus(blk *Block, vs *ValidatorSet) error {
	// prevTimestamp=0 signalisiert "kein Zeitkontext" → Rundenzeit wird nicht
	// geprüft (nur die kryptografische/Rollen-Konsistenz).
	return verifyConsensus(blk, vs, 0, false)
}

// VerifyBlockConsensusAt prüft zusätzlich, dass die im Header angegebene
// Fallback-Runde zeitlich zulässig ist: Ein Fallback-Proposer (Runde > 0) darf
// erst produzieren, wenn seit dem Vorgängerblock genug Zeit verstrichen ist
// ((round+1)*BlockTime). So kann sich kein Validator durch Behaupten einer hohen
// Runde vordrängeln. prevTimestamp ist der Zeitstempel des Vorgängerblocks.
func VerifyBlockConsensusAt(blk *Block, vs *ValidatorSet, prevTimestamp uint64) error {
	return verifyConsensus(blk, vs, prevTimestamp, true)
}

func verifyConsensus(blk *Block, vs *ValidatorSet, prevTimestamp uint64, checkTime bool) error {
	if blk.Header.Height == 0 {
		return nil // Genesis braucht keinen Konsens-Nachweis
	}
	if vs == nil || vs.Len() == 0 {
		return errors.New("chain: kein Validator-Set für Konsensprüfung")
	}
	signer, err := RecoverBlockSigner(blk)
	if err != nil {
		return err
	}
	if !vs.Contains(signer) {
		return errors.New("chain: Block-Signierer nicht im Validator-Set")
	}
	round := blk.Header.Round
	// Der Signierer muss der für DIESE Höhe und DIESE Runde berechtigte Proposer
	// sein (Runde 0 = primär, höhere Runden = Fallback-Rotation).
	if !vs.IsProposerForRound(signer, blk.Header.Height, round) {
		return errors.New("chain: Signierer ist nicht der berechtigte Proposer für diese Höhe/Runde")
	}
	if blk.Header.Proposer != signer {
		return errors.New("chain: Header-Proposer weicht vom Signierer ab")
	}
	if blk.Header.ValSetHash != vs.Hash() {
		return errors.New("chain: ValSetHash weicht vom aktiven Validator-Set ab")
	}
	// Zeitliche Plausibilität der Fallback-Runde: Eine Runde r > 0 ist erst
	// erlaubt, wenn seit dem Vorgängerblock mindestens r*BlockTime Sekunden
	// vergangen sind. Der Header-Timestamp muss diese Wartezeit widerspiegeln.
	if checkTime && round > 0 {
		if blk.Header.Timestamp < prevTimestamp {
			return errors.New("chain: Block-Zeitstempel vor Vorgängerblock")
		}
		elapsed := blk.Header.Timestamp - prevTimestamp
		maxRound := RoundForElapsed(elapsed)
		// Deckeln auf Validator-Anzahl - 1 (wie in TurnAt): begrenzt auch den
		// Effekt eines weit zurückliegenden Vorgänger-Zeitstempels.
		if n := uint64(vs.Len()); n > 0 && maxRound > n-1 {
			maxRound = n - 1
		}
		if round > maxRound {
			return fmt.Errorf("chain: Fallback-Runde %d zu früh (erst Runde %d nach %ds erlaubt)",
				round, maxRound, elapsed)
		}
	}
	return nil
}
