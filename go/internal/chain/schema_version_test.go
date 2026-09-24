package chain

import (
	"math/big"
	"testing"
)

// StateSchemaVersion muss als erstes Byte in den State-Root einfließen. Ändert
// sich die Version, ändert sich der Root — so werden Alt-Chains als inkompatibel
// erkannt. Dieser Test dokumentiert die aktuelle Version und schützt davor, sie
// versehentlich ohne Bewusstsein zu verändern.
func TestStateSchemaVersionInRoot(t *testing.T) {
	if StateSchemaVersion != 0x05 {
		t.Fatalf("StateSchemaVersion = 0x%02x, erwartet 0x05 — wurde das Format bewusst geändert? Dann diesen Test UND die Historie anpassen.", StateSchemaVersion)
	}

	// Zwei leere States müssen denselben Root haben (deterministisch).
	a := NewState()
	b := NewState()
	if a.Root() != b.Root() {
		t.Fatal("zwei leere States sollten denselben Root haben")
	}

	// Der Root eines leeren States beginnt konzeptionell mit der Schema-Version;
	// ein Guthaben verändert ihn (Sanity-Check, dass Root überhaupt reagiert).
	var addr Address
	addr[0] = 0x11
	a.Credit(addr, big.NewInt(1000))
	if a.Root() == b.Root() {
		t.Fatal("Root sollte sich nach Credit ändern")
	}
}
