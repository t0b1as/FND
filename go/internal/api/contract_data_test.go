package api

import (
	"math/big"
	"testing"

	"github.com/fundus/node/internal/chain"
)

// TestEscrowStateString prüft das Status-Mapping für die Vertragsanzeige.
func TestEscrowStateString(t *testing.T) {
	cases := map[chain.EscrowState]string{
		chain.EscrowOpen:            "open",
		chain.EscrowDisputed:        "disputed",
		chain.EscrowClosed:          "closed",
		chain.EscrowCancelRequested: "cancel_requested",
		chain.EscrowReturnSubmitted: "return_submitted",
		chain.EscrowState(99):       "unknown",
	}
	for st, want := range cases {
		if got := escrowStateString(st); got != want {
			t.Errorf("escrowStateString(%d) = %q, want %q", st, got, want)
		}
	}
}

// TestUToFNDFloat prüft die uFND→FND-Umrechnung für die Anzeige.
func TestUToFNDFloat(t *testing.T) {
	cases := []struct {
		u    *big.Int
		want float64
	}{
		{big.NewInt(chain.UFNDPerFND), 1.0},           // 1 FND
		{big.NewInt(chain.UFNDPerFND / 2), 0.5},       // 0,5 FND
		{new(big.Int).Mul(big.NewInt(10), big.NewInt(chain.UFNDPerFND)), 10.0},
		{big.NewInt(0), 0.0},
		{nil, 0.0},
	}
	for _, c := range cases {
		if got := uToFNDFloat(c.u); got != c.want {
			t.Errorf("uToFNDFloat(%v) = %v, want %v", c.u, got, c.want)
		}
	}
}
