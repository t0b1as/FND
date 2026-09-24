package chain

import (
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Validator-Set ist deterministisch sortiert: gleiche Eingabe (beliebige
// Reihenfolge) → gleiches Set und gleicher Hash.
func TestValidatorSetDeterministic(t *testing.T) {
	k1, _ := crypto.GenerateKey()
	k2, _ := crypto.GenerateKey()
	k3, _ := crypto.GenerateKey()
	a1 := PubkeyToAddress(&k1.PublicKey)
	a2 := PubkeyToAddress(&k2.PublicKey)
	a3 := PubkeyToAddress(&k3.PublicKey)

	vs1, err := NewValidatorSet([]Address{a1, a2, a3})
	if err != nil {
		t.Fatal(err)
	}
	// Andere Eingabe-Reihenfolge → muss identisches Set + Hash ergeben.
	vs2, err := NewValidatorSet([]Address{a3, a1, a2})
	if err != nil {
		t.Fatal(err)
	}
	if vs1.Hash() != vs2.Hash() {
		t.Fatal("Validator-Set-Hash hängt von Eingabereihenfolge ab — nicht deterministisch")
	}
	if vs1.Len() != 3 {
		t.Fatalf("erwartet 3 Validatoren, bekam %d", vs1.Len())
	}
}

// Duplikate werden entfernt.
func TestValidatorSetDedup(t *testing.T) {
	k1, _ := crypto.GenerateKey()
	a1 := PubkeyToAddress(&k1.PublicKey)
	vs, err := NewValidatorSet([]Address{a1, a1, a1})
	if err != nil {
		t.Fatal(err)
	}
	if vs.Len() != 1 {
		t.Fatalf("Duplikate nicht entfernt: %d", vs.Len())
	}
}

// Round-Robin: über N aufeinanderfolgende Höhen rotiert der Proposer durch ALLE
// Validatoren genau einmal, dann wiederholt es sich.
func TestProposerRoundRobin(t *testing.T) {
	var keys []Address
	for i := 0; i < 4; i++ {
		k, _ := crypto.GenerateKey()
		keys = append(keys, PubkeyToAddress(&k.PublicKey))
	}
	vs, _ := NewValidatorSet(keys)
	n := uint64(vs.Len())

	// Für jede Höhe genau ein Proposer; Höhe h und h+n haben denselben.
	for h := uint64(1); h <= n; h++ {
		p1 := vs.ProposerForHeight(h)
		p2 := vs.ProposerForHeight(h + n)
		if p1 != p2 {
			t.Fatalf("Proposer nicht periodisch: Höhe %d != %d", h, h+n)
		}
	}
	// Über eine volle Runde müssen alle Validatoren drankommen.
	seen := make(map[Address]bool)
	for h := uint64(0); h < n; h++ {
		seen[vs.ProposerForHeight(h)] = true
	}
	if len(seen) != int(n) {
		t.Fatalf("nicht alle Validatoren kamen dran: %d/%d", len(seen), n)
	}
}

// Block-Signatur: signieren und den Signierer korrekt zurückgewinnen.
func TestBlockSignAndRecover(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	addr := PubkeyToAddress(&priv.PublicKey)

	blk := &Block{Header: BlockHeader{Height: 5, Proposer: addr}}
	if err := SignBlock(blk, priv); err != nil {
		t.Fatal(err)
	}
	got, err := RecoverBlockSigner(blk)
	if err != nil {
		t.Fatal(err)
	}
	if got != addr {
		t.Fatalf("Signierer falsch: %s != %s", got.Hex(), addr.Hex())
	}
}

// VerifyBlockConsensus akzeptiert einen korrekt signierten Block vom
// berechtigten Proposer und lehnt Fremde/Unberechtigte ab.
func TestVerifyBlockConsensus(t *testing.T) {
	// Zwei Validatoren.
	kA, _ := crypto.GenerateKey()
	kB, _ := crypto.GenerateKey()
	aA := PubkeyToAddress(&kA.PublicKey)
	aB := PubkeyToAddress(&kB.PublicKey)
	vs, _ := NewValidatorSet([]Address{aA, aB})

	// Finde eine Höhe, für die A der Proposer ist.
	var hA uint64
	for h := uint64(1); h < 10; h++ {
		if vs.IsProposerForHeight(aA, h) {
			hA = h
			break
		}
	}

	// A produziert korrekt für seine Höhe.
	blk := &Block{Header: BlockHeader{Height: hA, Proposer: aA, ValSetHash: vs.Hash()}}
	if err := SignBlock(blk, kA); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlockConsensus(blk, vs); err != nil {
		t.Fatalf("gültiger Block abgelehnt: %v", err)
	}

	// B versucht, für A's Höhe zu produzieren → muss abgelehnt werden.
	blkBad := &Block{Header: BlockHeader{Height: hA, Proposer: aB, ValSetHash: vs.Hash()}}
	if err := SignBlock(blkBad, kB); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlockConsensus(blkBad, vs); err == nil {
		t.Fatal("unberechtigter Proposer wurde akzeptiert — Konsens-Lücke!")
	}

	// Fremder (nicht im Set) → abgelehnt.
	kX, _ := crypto.GenerateKey()
	aX := PubkeyToAddress(&kX.PublicKey)
	blkX := &Block{Header: BlockHeader{Height: hA, Proposer: aX, ValSetHash: vs.Hash()}}
	_ = SignBlock(blkX, kX)
	if err := VerifyBlockConsensus(blkX, vs); err == nil {
		t.Fatal("Fremder außerhalb des Validator-Sets akzeptiert!")
	}
}

// Genesis (Höhe 0) braucht keinen Konsens-Nachweis.
func TestGenesisSkipsConsensus(t *testing.T) {
	k, _ := crypto.GenerateKey()
	a := PubkeyToAddress(&k.PublicKey)
	vs, _ := NewValidatorSet([]Address{a})
	gen := &Block{Header: BlockHeader{Height: 0}}
	if err := VerifyBlockConsensus(gen, vs); err != nil {
		t.Fatalf("Genesis sollte Konsens-Prüfung überspringen: %v", err)
	}
}
