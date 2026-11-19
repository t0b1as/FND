package chain

import (
	"math/big"
	"testing"
)

// TestFND_024_BootstrapUnionWithStake prüft den Kern der On-Chain-Validator-
// Ableitung (seit R477): Das aktive Set ist die VEREINIGUNG aus den fest
// eincompilierten Gründer-Validatoren (founders.go) und on-chain gestakten
// Validatoren. Ein neuer Staker kommt HINZU, verdrängt die Gründer aber NICHT.
// Die lokale Bootstrap-Liste (Config) wirkt nur noch als Fallback, wenn die
// Gründer-Liste leer wäre — sie ist pro Node verschieden und darf das Set nicht
// bestimmen (R938: Test an die R477-Regel angepasst).
func TestFND_024_BootstrapUnionWithStake(t *testing.T) {
	founders := foundingAddrs()
	if len(founders) == 0 {
		t.Skip("keine Gründer-Validatoren eincompiliert")
	}
	nf := len(founders)
	local := Address{0xaa}
	bootstrap, err := NewValidatorSet([]Address{local})
	if err != nil {
		t.Fatalf("Bootstrap-Set: %v", err)
	}

	bc := &Blockchain{
		state:        NewState(),
		bootstrapVal: bootstrap,
	}

	// Phase 1: kein Stake → nur die Gründer; die lokale Liste wirkt nicht.
	bc.deriveValidatorSetLocked()
	if bc.valSet == nil || bc.valSet.Len() != nf {
		t.Fatalf("ohne Stake erwartet: %d (Gründer), bekommen Len=%v", nf, bc.valSet)
	}
	for _, f := range founders {
		if !bc.valSet.Contains(f) {
			t.Fatal("Gründer-Adressen müssen im Set sein")
		}
	}
	if bc.valSet.Contains(local) {
		t.Fatal("lokale Bootstrap-Adresse darf das Set nicht bestimmen")
	}

	// Phase 2: ein Staker → Vereinigung = Gründer + Staker.
	staker := Address{0xcc}
	bc.state.getOrCreateStake(staker).Amount = new(big.Int).Set(MinValidatorStake)
	bc.deriveValidatorSetLocked()
	if bc.valSet == nil || bc.valSet.Len() != nf+1 {
		t.Fatalf("mit Stake erwartet: %d (Gründer + Staker), bekommen Len=%v", nf+1, bc.valSet)
	}
	for _, f := range founders {
		if !bc.valSet.Contains(f) {
			t.Fatal("Gründer dürfen durch einen Staker NICHT verdrängt werden")
		}
	}
	if !bc.valSet.Contains(staker) {
		t.Fatal("der neue Staker muss zum Set hinzukommen")
	}

	// Phase 3: Stake fällt unter das Minimum → zurück zu nur Gründern.
	bc.state.getOrCreateStake(staker).Amount = big.NewInt(1)
	bc.deriveValidatorSetLocked()
	if bc.valSet == nil || bc.valSet.Len() != nf {
		t.Fatalf("nach Stake-Verlust erwartet: %d, bekommen Len=%v", nf, bc.valSet)
	}
	if bc.valSet.Contains(staker) {
		t.Fatal("ungestakter Node darf nicht mehr im Set sein")
	}
}
