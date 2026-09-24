package main

import (
	"testing"
	"time"
)

// FND_001_TickIntervalNeverZero: time.NewTicker panict bei <= 0. Die Zusage
// lautet deshalb schlicht "immer positiv", über den ganzen Wertebereich.
func TestFND_001_TickIntervalNeverZero(t *testing.T) {
	for _, bt := range []uint64{0, 1, 5, 60, 3600, ^uint64(0)} {
		got := tickIntervalFor(bt)
		if got <= 0 {
			t.Fatalf("tickIntervalFor(%d) = %v — muss positiv sein, sonst panict NewTicker", bt, got)
		}
	}
}

// FND_001_TickerAcceptsResult schließt die Lücke zwischen "Wert ist positiv"
// und "NewTicker nimmt ihn": der Panic wird provoziert, statt über eine Zahl
// argumentiert zu werden.
func TestFND_001_TickerAcceptsResult(t *testing.T) {
	for _, bt := range []uint64{0, 1, 5, 60, ^uint64(0)} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("NewTicker panicte bei BlockTime %d: %v", bt, r)
				}
			}()
			tk := time.NewTicker(tickIntervalFor(bt))
			tk.Stop()
		}()
	}
}
