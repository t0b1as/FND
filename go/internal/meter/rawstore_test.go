package meter

import (
	"testing"
	"time"
)

func sample(tsUnix int64, kwh float64) RawSample {
	return RawSample{Timestamp: time.Unix(tsUnix, 0), KWhBezug: kwh}
}

// TestPeriodCommitmentBasic: Append + ClosePeriod liefert korrekte Ws-Mengen.
func TestPeriodCommitmentBasic(t *testing.T) {
	ps, err := NewPeriodStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mid := "METER001"
	// 10.000 kWh → 10.001 kWh = 1 kWh geliefert = 3.600.000 Ws.
	if err := ps.Append(mid, sample(100, 10000.0)); err != nil {
		t.Fatal(err)
	}
	ps.Append(mid, sample(200, 10000.5))
	ps.Append(mid, sample(300, 10001.0))

	c, err := ps.ClosePeriod(mid)
	if err != nil {
		t.Fatalf("ClosePeriod: %v", err)
	}
	if c.AmountWs != WsPerKWh {
		t.Fatalf("AmountWs = %d, want %d", c.AmountWs, WsPerKWh)
	}
	if c.TimestampA != 100 || c.TimestampB != 300 {
		t.Fatalf("Periode falsch: %d–%d", c.TimestampA, c.TimestampB)
	}
	if c.SampleCount != 3 {
		t.Fatalf("SampleCount = %d, want 3", c.SampleCount)
	}
}

// TestPeriodHashDeterministic: gleiche Samples → gleicher Hash (reproduzierbar
// für die On-Chain-Verankerung).
func TestPeriodHashDeterministic(t *testing.T) {
	mkStore := func() *PeriodStore {
		ps, _ := NewPeriodStore(t.TempDir())
		ps.Append("M", sample(100, 5.0))
		ps.Append("M", sample(160, 6.5))
		ps.Append("M", sample(220, 8.0))
		return ps
	}
	c1, _ := mkStore().ClosePeriod("M")
	c2, _ := mkStore().ClosePeriod("M")
	if c1.RawDataHash != c2.RawDataHash {
		t.Fatal("Rohdaten-Hash nicht deterministisch")
	}
	// Andere Daten → anderer Hash.
	ps3, _ := NewPeriodStore(t.TempDir())
	ps3.Append("M", sample(100, 5.0))
	ps3.Append("M", sample(160, 7.0)) // abweichend
	c3, _ := ps3.ClosePeriod("M")
	if c1.RawDataHash == c3.RawDataHash {
		t.Fatal("verschiedene Rohdaten müssten verschiedene Hashes ergeben")
	}
}

// TestClosePeriodNeedsTwoSamples: eine Periode mit <2 Samples wird abgelehnt.
func TestClosePeriodNeedsTwoSamples(t *testing.T) {
	ps, _ := NewPeriodStore(t.TempDir())
	ps.Append("M", sample(100, 5.0))
	if _, err := ps.ClosePeriod("M"); err == nil {
		t.Fatal("Periode mit nur 1 Sample hätte abgelehnt werden müssen")
	}
}

// TestClosePeriodRejectsDecreasing: rückläufiger Endstand wird erkannt.
func TestClosePeriodRejectsDecreasing(t *testing.T) {
	ps, _ := NewPeriodStore(t.TempDir())
	ps.Append("M", sample(100, 10.0))
	ps.Append("M", sample(200, 9.0)) // niedriger
	if _, err := ps.ClosePeriod("M"); err == nil {
		t.Fatal("rückläufiger Endstand hätte abgelehnt werden müssen")
	}
}

// TestDeletePeriod: nach Settlement löschbar, danach keine Daten mehr.
func TestDeletePeriod(t *testing.T) {
	ps, _ := NewPeriodStore(t.TempDir())
	ps.Append("M", sample(100, 5.0))
	ps.Append("M", sample(200, 6.0))
	if _, err := ps.ClosePeriod("M"); err != nil {
		t.Fatal(err)
	}
	if err := ps.DeletePeriod("M"); err != nil {
		t.Fatalf("DeletePeriod: %v", err)
	}
	// Nach dem Löschen: keine Samples mehr → ClosePeriod schlägt fehl.
	if _, err := ps.ClosePeriod("M"); err == nil {
		t.Fatal("nach DeletePeriod dürften keine Rohdaten mehr da sein")
	}
	// Doppeltes Löschen ist idempotent (kein Fehler).
	if err := ps.DeletePeriod("M"); err != nil {
		t.Fatalf("DeletePeriod (idempotent): %v", err)
	}
}
