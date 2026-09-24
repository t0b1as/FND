package chain

import (
	"math/big"
	"testing"
)

// TestFND_024_BootstrapUnionWithStake prüft den Kern der On-Chain-Validator-
// Ableitung: Das aktive Set ist die VEREINIGUNG aus Bootstrap-Validatoren (Config)
// und on-chain gestakten Validatoren. Ein neuer Staker kommt HINZU, verdrängt die
// Gründungs-Nodes aber NICHT — sonst würde er eine eigene Kette produzieren.
func TestFND_024_BootstrapUnionWithStake(t *testing.T) {
	bootA, bootB := Address{0xaa}, Address{0xbb}
	bootstrap, err := NewValidatorSet([]Address{bootA, bootB})
	if err != nil {
		t.Fatalf("Bootstrap-Set: %v", err)
	}

	bc := &Blockchain{
		state:        NewState(),
		bootstrapVal: bootstrap,
	}

	// Phase 1: kein Stake → nur das Bootstrap-Set (2 Validatoren).
	bc.deriveValidatorSetLocked()
	if bc.valSet == nil || bc.valSet.Len() != 2 {
		t.Fatalf("ohne Stake erwartet: 2 (Bootstrap), bekommen Len=%v", bc.valSet)
	}
	if !bc.valSet.Contains(bootA) || !bc.valSet.Contains(bootB) {
		t.Fatal("Bootstrap-Adressen müssen im Set sein")
	}

	// Phase 2: ein DRITTER Node staked genug → Vereinigung = 3 Validatoren.
	staker := Address{0xcc}
	bc.state.getOrCreateStake(staker).Amount = new(big.Int).Set(MinValidatorStake)
	bc.deriveValidatorSetLocked()
	if bc.valSet == nil || bc.valSet.Len() != 3 {
		t.Fatalf("mit Stake erwartet: 3 (Bootstrap + Staker), bekommen Len=%v", bc.valSet)
	}
	// KRITISCH: Die Bootstrap-Validatoren dürfen NICHT verdrängt werden.
	if !bc.valSet.Contains(bootA) || !bc.valSet.Contains(bootB) {
		t.Fatal("Bootstrap-Validatoren dürfen durch einen Staker NICHT verdrängt werden")
	}
	if !bc.valSet.Contains(staker) {
		t.Fatal("der neue Staker muss zum Set hinzukommen")
	}

	// Phase 3: Stake fällt unter das Minimum → zurück zu nur Bootstrap (2).
	bc.state.getOrCreateStake(staker).Amount = big.NewInt(1)
	bc.deriveValidatorSetLocked()
	if bc.valSet == nil || bc.valSet.Len() != 2 {
		t.Fatalf("nach Stake-Verlust erwartet: 2 (nur Bootstrap), bekommen Len=%v", bc.valSet)
	}
	if bc.valSet.Contains(staker) {
		t.Fatal("ungestakter Node darf nicht mehr im Set sein")
	}
}
