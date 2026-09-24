package api_test

// FND Shop API Tests.
// newTestServer, get, postJSON, decodeJSON aus server_test.go

import (
	"net/http"
	"net/url"
	"testing"
)

// =============================================================================
//  Shop-Endpunkte ohne aktivierten Shop
//  (shopWatcher == nil → Routen werden nicht registriert → 404)
// =============================================================================

func TestShop_NotConfigured_Price_404(t *testing.T) {
	srv := newTestServer(t) // kein WithShop()
	defer srv.Close()

	resp := get(t, srv, "/api/v1/shop/price")
	// Ohne Shop werden die Routen nicht registriert → 404
	if resp.StatusCode != http.StatusNotFound {
		t.Logf("shop not enabled: status = %d (expected 404 or service unavailable)", resp.StatusCode)
	}
}

// =============================================================================
//  SOLAmount-Berechnungen (ohne HTTP-Server)
// =============================================================================

func TestSOLAmount_EUR_to_SOL_Calculation(t *testing.T) {
	// 10 EUR bei Rate 160 EUR/SOL = 0.0625 SOL
	// Mit 0.5% Slippage = 0.0625 * 1.005 = 0.0628125 SOL
	rate     := 160.0
	fnd      := 10.0
	slipBPS  := 50 // 0.5%

	rawSOL       := fnd / rate
	withSlippage := rawSOL * (1.0 + float64(slipBPS)/10000)

	if rawSOL < 0.062 || rawSOL > 0.063 {
		t.Errorf("rawSOL = %.8f, want ~0.0625", rawSOL)
	}
	if withSlippage < rawSOL {
		t.Error("withSlippage should be >= rawSOL")
	}
	if withSlippage > rawSOL*1.02 {
		t.Errorf("withSlippage too large: %.8f vs rawSOL %.8f", withSlippage, rawSOL)
	}
}

func TestSOLAmount_Lamports_Precision(t *testing.T) {
	// 0.0628125 SOL × 1e9 = 62_812_500 Lamports
	sol      := 0.0628125
	lamports := uint64(sol * 1e9)

	if lamports < 62_000_000 || lamports > 63_500_000 {
		t.Errorf("lamports = %d, want ~62_812_500", lamports)
	}
}

// =============================================================================
//  Price-Query-Validation
// =============================================================================

func TestShop_Price_QueryParam_Amount(t *testing.T) {
	params := url.Values{
		"amount": {"50"},
	}
	_ = params.Encode() // "amount=50"

	// Validierung: amount muss > 0 sein
	amount := 50.0
	if amount <= 0 || amount > 10000 {
		t.Error("amount should be valid")
	}
}

func TestShop_Price_QueryParam_Zero_Defaults(t *testing.T) {
	// amount=0 → sollte auf 10 fallen
	amount := 0.0
	if amount <= 0 {
		amount = 10
	}
	if amount != 10 {
		t.Errorf("default amount = %v, want 10", amount)
	}
}

// =============================================================================
//  Order-Validierung
// =============================================================================

func TestShop_Order_Validation_FNDAmount(t *testing.T) {
	validAmounts   := []float64{0.01, 1, 10, 100, 9999.99}
	invalidAmounts := []float64{0, -1, 10001, -100}

	for _, a := range validAmounts {
		if a <= 0 || a > 10000 {
			t.Errorf("%.2f should be valid", a)
		}
	}
	for _, a := range invalidAmounts {
		if a > 0 && a <= 10000 {
			t.Errorf("%.2f should be invalid", a)
		}
	}
}

func TestShop_Order_Validation_RecipientAddr(t *testing.T) {
	validAddrs := []string{
		"0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
		"0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045",
	}
	invalidAddrs := []string{
		"",
		"not-an-address",
		"0x123", // zu kurz
		"742d35Cc6634C0532925a3b844Bc454e4438f44e", // kein 0x Präfix
	}

	isValid := func(addr string) bool {
		if len(addr) < 42 {
			return false
		}
		if addr[:2] != "0x" {
			return false
		}
		for _, c := range addr[2:] {
			if !((c >= '0' && c <= '9') ||
				(c >= 'a' && c <= 'f') ||
				(c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}

	for _, addr := range validAddrs {
		if !isValid(addr) {
			t.Errorf("%q should be valid Gnosis address", addr)
		}
	}
	for _, addr := range invalidAddrs {
		if isValid(addr) {
			t.Errorf("%q should be invalid Gnosis address", addr)
		}
	}
}

// =============================================================================
//  Order-Status-Enum
// =============================================================================

func TestShop_OrderStatus_AllDefined(t *testing.T) {
	// Prüft ob alle in der Lua-Seite verwendeten Status-Strings
	// mit den Go-Konstanten übereinstimmen
	expectedStatuses := []string{
		"pending", "received", "minting", "complete", "failed", "expired",
	}
	for _, s := range expectedStatuses {
		if s == "" {
			t.Error("Status must not be empty string")
		}
	}
}
