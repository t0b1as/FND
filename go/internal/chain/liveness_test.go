package chain

import (
	"crypto/ecdsa"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Rundenschwellen mit FallbackTimeout-Puffer: Runde 0 ab BlockTime, Runde r ab
// BlockTime + r*FallbackTimeout. Der Puffer (FallbackTimeout = 2*BlockTime) gibt
// dem primären Proposer Vorsprung, bevor ein Fallback einspringen darf.
func TestRoundForElapsed(t *testing.T) {
	bt := BlockTime
	ft := FallbackTimeout
	cases := []struct {
		elapsed uint64
		want    uint64
	}{
		{0, 0},
		{bt - 1, 0},
		{bt, 0},          // primärer Proposer (Runde 0)
		{bt + ft - 1, 0}, // knapp vor Runde 1
		{bt + ft, 1},     // erster Fallback
		{bt + 2*ft, 2},   // zweiter Fallback
		{bt + 10*ft, 10},
	}
	for _, c := range cases {
		if got := RoundForElapsed(c.elapsed); got != c.want {
			t.Errorf("RoundForElapsed(%d): erwartet %d, bekam %d", c.elapsed, c.want, got)
		}
	}
}

// Round-basierte Proposer-Auswahl: Runde r verschiebt den Proposer um r
// Positionen weiter. Über eine volle Rotation (N Runden) ist man wieder beim
// primären Proposer.
func TestProposerForRound(t *testing.T) {
	var addrs []Address
	for i := 0; i < 4; i++ {
		k, _ := crypto.GenerateKey()
		addrs = append(addrs, PubkeyToAddress(&k.PublicKey))
	}
	vs, _ := NewValidatorSet(addrs)
	n := uint64(vs.Len())

	for h := uint64(1); h < 12; h++ {
		primary := vs.ProposerForRound(h, 0)
		// Runde 0 == klassisches Round-Robin.
		if primary != vs.ProposerForHeight(h) {
			t.Fatalf("Runde 0 weicht von ProposerForHeight ab (Höhe %d)", h)
		}
		// Nach einer vollen Rotation (N Runden) wieder derselbe Proposer.
		if vs.ProposerForRound(h, n) != primary {
			t.Fatalf("nach voller Rotation nicht wieder primärer Proposer (Höhe %d)", h)
		}
		// Jede Fallback-Runde bis N-1 ist ein ANDERER Validator als der primäre.
		for r := uint64(1); r < n; r++ {
			if vs.ProposerForRound(h, r) == primary {
				t.Fatalf("Fallback-Runde %d gleicht dem primären Proposer (Höhe %d)", r, h)
			}
		}
	}
}

// Der Fallback-Proposer (Runde 1) wird nur akzeptiert, wenn genug Zeit seit dem
// Vorgängerblock verstrichen ist — sonst „zu früh".
func TestFallbackRoundTiming(t *testing.T) {
	kA, _ := crypto.GenerateKey()
	kB, _ := crypto.GenerateKey()
	aA := PubkeyToAddress(&kA.PublicKey)
	aB := PubkeyToAddress(&kB.PublicKey)
	vs, _ := NewValidatorSet([]Address{aA, aB})

	// Höhe finden, für die in Runde 0 A dran ist und in Runde 1 B einspringt.
	var h uint64
	for hh := uint64(1); hh < 10; hh++ {
		if vs.IsProposerForRound(aA, hh, 0) && vs.IsProposerForRound(aB, hh, 1) {
			h = hh
			break
		}
	}
	if h == 0 {
		t.Fatal("keine passende Höhe gefunden")
	}

	prevTS := uint64(1_000_000)

	// B (Fallback, Runde 1) produziert ZU FRÜH: nur BlockTime seit Vorgänger →
	// Runde 1 noch nicht freigeschaltet (erst ab BlockTime + FallbackTimeout).
	blkEarly := &Block{Header: BlockHeader{
		Height: h, Proposer: aB, Round: 1, ValSetHash: vs.Hash(),
		Timestamp: prevTS + BlockTime,
	}}
	if err := SignBlock(blkEarly, kB); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlockConsensusAt(blkEarly, vs, prevTS); err == nil {
		t.Fatal("Fallback-Block zu früh akzeptiert — Liveness-Sicherung greift nicht!")
	}

	// B produziert RECHTZEITIG: BlockTime + FallbackTimeout seit Vorgänger →
	// Runde 1 erlaubt.
	blkOK := &Block{Header: BlockHeader{
		Height: h, Proposer: aB, Round: 1, ValSetHash: vs.Hash(),
		Timestamp: prevTS + BlockTime + FallbackTimeout,
	}}
	if err := SignBlock(blkOK, kB); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlockConsensusAt(blkOK, vs, prevTS); err != nil {
		t.Fatalf("rechtzeitiger Fallback-Block abgelehnt: %v", err)
	}
}

// Der primäre Proposer (Runde 0) wird unabhängig von der verstrichenen Zeit
// akzeptiert (Runde 0 hat keine Wartebedingung außer der Mindest-Blockzeit, die
// im Produktions-Loop sitzt, nicht in der Verifikation).
func TestPrimaryProposerNoTimingRestriction(t *testing.T) {
	kA, _ := crypto.GenerateKey()
	kB, _ := crypto.GenerateKey()
	aA := PubkeyToAddress(&kA.PublicKey)
	aB := PubkeyToAddress(&kB.PublicKey)
	vs, _ := NewValidatorSet([]Address{aA, aB})

	var h uint64
	var signer Address
	var key = kA
	for hh := uint64(1); hh < 10; hh++ {
		signer = vs.ProposerForRound(hh, 0)
		if signer == aA {
			key = kA
			h = hh
			break
		}
		if signer == aB {
			key = kB
			h = hh
			break
		}
	}

	prevTS := uint64(1_000_000)
	// Runde 0, quasi keine Zeit vergangen → trotzdem akzeptiert.
	blk := &Block{Header: BlockHeader{
		Height: h, Proposer: signer, Round: 0, ValSetHash: vs.Hash(),
		Timestamp: prevTS + 1,
	}}
	if err := SignBlock(blk, key); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlockConsensusAt(blk, vs, prevTS); err != nil {
		t.Fatalf("primärer Proposer (Runde 0) abgelehnt: %v", err)
	}
}

// Ein Fallback-Proposer, der die FALSCHE Runde behauptet (er ist zwar
// zeitlich erlaubt, aber nicht der Proposer FÜR diese Runde), wird abgelehnt.
func TestWrongFallbackProposerRejected(t *testing.T) {
	kA, _ := crypto.GenerateKey()
	kB, _ := crypto.GenerateKey()
	kC, _ := crypto.GenerateKey()
	aA := PubkeyToAddress(&kA.PublicKey)
	aB := PubkeyToAddress(&kB.PublicKey)
	aC := PubkeyToAddress(&kC.PublicKey)
	vs, _ := NewValidatorSet([]Address{aA, aB, aC})

	// Höhe, in der A primär (Runde 0) ist.
	var h uint64
	for hh := uint64(1); hh < 12; hh++ {
		if vs.IsProposerForRound(aA, hh, 0) {
			h = hh
			break
		}
	}
	prevTS := uint64(2_000_000)

	// Wer ist der KORREKTE Runde-1-Proposer? Ein anderer (weder primär noch der
	// korrekte Fallback) darf Runde 1 NICHT beanspruchen.
	correct := vs.ProposerForRound(h, 1)
	primary := vs.ProposerForRound(h, 0)
	type kp struct {
		a Address
		k *ecdsa.PrivateKey
	}
	var wrong *kp
	for _, pair := range []kp{{aA, kA}, {aB, kB}, {aC, kC}} {
		if pair.a != correct && pair.a != primary {
			p := pair
			wrong = &p
			break
		}
	}
	if wrong == nil {
		t.Skip("kein passender falscher Proposer bei dieser Konstellation")
	}

	// Zeitlich wäre Runde 1 erlaubt (BlockTime + FallbackTimeout), aber wrong.a
	// ist nicht der Runde-1-Proposer → muss abgelehnt werden.
	blk := &Block{Header: BlockHeader{
		Height: h, Proposer: wrong.a, Round: 1, ValSetHash: vs.Hash(),
		Timestamp: prevTS + BlockTime + FallbackTimeout,
	}}
	if err := SignBlock(blk, wrong.k); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlockConsensusAt(blk, vs, prevTS); err == nil {
		t.Fatal("falscher Fallback-Proposer für Runde 1 akzeptiert!")
	}
}

// FallbackTimeout ist größer als BlockTime (Puffer gegen Forks): Runde 1 wird
// erst nach BlockTime + FallbackTimeout freigeschaltet, nicht schon nach dem
// nächsten Timeout.
func TestFallbackTimeoutBuffer(t *testing.T) {
	if FallbackTimeout < BlockTime {
		t.Fatalf("FallbackTimeout (%d) sollte >= BlockTime (%d) sein — sonst zu wenig Fork-Puffer",
			FallbackTimeout, BlockTime)
	}
	// Bei genau 2*BlockTime darf Runde 1 NOCH NICHT erlaubt sein (Puffer wirkt).
	if FallbackTimeout > BlockTime {
		if r := RoundForElapsed(2 * BlockTime); r != 0 {
			t.Fatalf("bei 2*BlockTime sollte wegen Puffer noch Runde 0 gelten, bekam %d", r)
		}
	}
}
