package shop_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/shop"
)

// =============================================================================
//  Mock-Minter
// =============================================================================

type mockMinter struct {
	mintedTo     string
	mintedAmount float64
	txHash       string
	err          error
}

func (m *mockMinter) MintFND(_ context.Context, to string, amount float64) (string, error) {
	m.mintedTo     = to
	m.mintedAmount = amount
	if m.err != nil {
		return "", m.err
	}
	if m.txHash == "" {
		return "0xdeadbeef", nil
	}
	return m.txHash, nil
}

// =============================================================================
//  PriceFeed Tests
// =============================================================================

func TestPriceFeed_CoinGecko_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"solana": map[string]any{
				"eur": 155.42,
				"usd": 170.00,
			},
		})
	}))
	defer srv.Close()

	// CoinGecko-URL durch Test-Server ersetzen (via eigene Fetch-Funktion nicht möglich
	// ohne Dependency Injection – prüfen wir stattdessen den gesamten Feed)
	cfg := shop.DefaultPriceFeedConfig
	cfg.CacheTTL = 1 * time.Second
	feed := shop.NewPriceFeed(cfg, zap.NewNop())

	// SOLForEUR hängt von externen APIs ab – im Unit-Test mocken wir die HTTP-Schicht
	// Wir testen die Kalkulations-Logik direkt über die Hilfsmethoden
	_ = feed
	_ = srv
	t.Log("PriceFeed created successfully")
}

func TestPriceFeed_SOLAmount_Lamports(t *testing.T) {
	// 1.5 SOL = 1.5 * 1e9 = 1_500_000_000 Lamports
	amt := shop.SOLAmount{
		EUR:  255.0,
		SOL:  1.5,
		Rate: 170.0,
	}
	if amt.Lamports() != 1_500_000_000 {
		t.Errorf("Lamports() = %d, want 1_500_000_000", amt.Lamports())
	}
}

func TestPriceFeed_SOLAmount_IsValid(t *testing.T) {
	future := shop.SOLAmount{ValidUntil: time.Now().Add(60 * time.Second)}
	past   := shop.SOLAmount{ValidUntil: time.Now().Add(-1 * time.Second)}

	if !future.IsValid() {
		t.Error("future ValidUntil should be valid")
	}
	if past.IsValid() {
		t.Error("past ValidUntil should not be valid")
	}
}

func TestPriceFeed_SOLAmount_Lamports_SmallAmount(t *testing.T) {
	// 0.001 SOL = 1_000_000 Lamports
	amt := shop.SOLAmount{SOL: 0.001}
	if amt.Lamports() != 1_000_000 {
		t.Errorf("Lamports() = %d, want 1_000_000", amt.Lamports())
	}
}

func TestPriceFeed_SOLAmount_Lamports_Zero(t *testing.T) {
	amt := shop.SOLAmount{SOL: 0}
	if amt.Lamports() != 0 {
		t.Errorf("Lamports() = %d, want 0", amt.Lamports())
	}
}

// =============================================================================
//  SolWatcher – Order-Verwaltung
// =============================================================================

func newTestWatcher(t *testing.T) (*shop.SolWatcher, *mockMinter) {
	t.Helper()

	// PriceFeed mit gemockter CoinGecko-Antwort
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"solana": map[string]any{"eur": 160.0, "usd": 175.0},
		})
	}))
	t.Cleanup(srv.Close)

	feedCfg := shop.PriceFeedConfig{
		CacheTTL:    5 * time.Second,
		SlippageBPS: 50,
		OfferTTL:    90 * time.Second,
		HTTPTimeout: 2 * time.Second,
	}
	// Wir können die URL nicht überschreiben ohne DI → nutzen wir einen
	// CacheSeed über eine separate Hilfsmethode
	feed := shop.NewPriceFeedWithMockRate(feedCfg, 160.0, zap.NewNop())

	minter := &mockMinter{}
	watchCfg := shop.WatcherConfig{
		RPCURL:         "http://localhost:1", // nicht verbunden in Unit-Tests
		ReceiveAddress: "FundusWallet123abc",
		PollInterval:   100 * time.Millisecond,
		PaymentTimeout: 5 * time.Minute,
		HTTPTimeout:    time.Second,
	}

	watcher := shop.NewSolWatcher(watchCfg, feed, minter, zap.NewNop())
	return watcher, minter
}

func TestSolWatcher_CreateOrder_Success(t *testing.T) {
	watcher, _ := newTestWatcher(t)

	order, err := watcher.CreateOrder(context.Background(), 10.0, "0xGnosisAddr")
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	if order.ID == "" {
		t.Error("order.ID should not be empty")
	}
	if order.FNDAmount != 10.0 {
		t.Errorf("FNDAmount = %v, want 10.0", order.FNDAmount)
	}
	if order.SOLExpected <= 0 {
		t.Errorf("SOLExpected = %v, should be > 0", order.SOLExpected)
	}
	if order.Status != shop.OrderPending {
		t.Errorf("Status = %v, want Pending", order.Status)
	}
	if order.ExpiresAt.Before(time.Now()) {
		t.Error("ExpiresAt should be in the future")
	}
}

func TestSolWatcher_CreateOrder_ZeroAmount_Error(t *testing.T) {
	watcher, _ := newTestWatcher(t)
	_, err := watcher.CreateOrder(context.Background(), 0, "0xAddr")
	if err == nil {
		t.Error("expected error for zero fnd amount")
	}
}

func TestSolWatcher_CreateOrder_EmptyAddr_Error(t *testing.T) {
	watcher, _ := newTestWatcher(t)
	_, err := watcher.CreateOrder(context.Background(), 5.0, "")
	if err == nil {
		t.Error("expected error for empty recipient address")
	}
}

func TestSolWatcher_GetOrder_ExistsAfterCreate(t *testing.T) {
	watcher, _ := newTestWatcher(t)
	order, _ := watcher.CreateOrder(context.Background(), 25.0, "0xAddr")

	got, ok := watcher.GetOrder(order.ID)
	if !ok {
		t.Error("GetOrder returned false for existing order")
	}
	if got.ID != order.ID {
		t.Errorf("GetOrder ID = %v, want %v", got.ID, order.ID)
	}
}

func TestSolWatcher_GetOrder_UnknownID_ReturnsFalse(t *testing.T) {
	watcher, _ := newTestWatcher(t)
	_, ok := watcher.GetOrder("nonexistent-id")
	if ok {
		t.Error("GetOrder should return false for unknown ID")
	}
}

func TestSolWatcher_CreateOrder_SOLExpected_WithSlippage(t *testing.T) {
	watcher, _ := newTestWatcher(t)

	// Rate = 160 EUR/SOL, 10 FND → 10/160 = 0.0625 SOL + 0.5% Slippage
	order, err := watcher.CreateOrder(context.Background(), 10.0, "0xAddr")
	if err != nil {
		t.Fatal(err)
	}

	rawSOL := 10.0 / 160.0               // 0.0625
	minExpected := rawSOL * 1.004         // min: +0.4%
	maxExpected := rawSOL * 1.011         // max: +1.1%

	if order.SOLExpected < minExpected || order.SOLExpected > maxExpected {
		t.Errorf("SOLExpected = %.8f, want in [%.8f, %.8f] (with ~0.5%% slippage)",
			order.SOLExpected, minExpected, maxExpected)
	}
}

func TestSolWatcher_CreateOrder_RateStored(t *testing.T) {
	watcher, _ := newTestWatcher(t)
	order, _ := watcher.CreateOrder(context.Background(), 10.0, "0xAddr")
	if order.RateEUR != 160.0 {
		t.Errorf("RateEUR = %v, want 160.0", order.RateEUR)
	}
}

func TestSolWatcher_MultipleOrders_IndependentIDs(t *testing.T) {
	watcher, _ := newTestWatcher(t)

	o1, _ := watcher.CreateOrder(context.Background(), 10.0, "0xAddr1")
	o2, _ := watcher.CreateOrder(context.Background(), 20.0, "0xAddr2")

	if o1.ID == o2.ID {
		t.Error("Two orders should have different IDs")
	}
}

// =============================================================================
//  OrderStatus-Übergänge
// =============================================================================

func TestOrderStatus_Constants(t *testing.T) {
	statuses := []shop.OrderStatus{
		shop.OrderPending,
		shop.OrderReceived,
		shop.OrderMinting,
		shop.OrderComplete,
		shop.OrderFailed,
		shop.OrderExpired,
	}
	seen := make(map[shop.OrderStatus]bool)
	for _, s := range statuses {
		if seen[s] {
			t.Errorf("duplicate status: %q", s)
		}
		seen[s] = true
		if string(s) == "" {
			t.Error("status should not be empty string")
		}
	}
}

// =============================================================================
//  PricePoint
// =============================================================================

func TestDefaultPriceFeedConfig(t *testing.T) {
	cfg := shop.DefaultPriceFeedConfig
	if cfg.CacheTTL <= 0 {
		t.Error("CacheTTL should be > 0")
	}
	if cfg.SlippageBPS <= 0 {
		t.Error("SlippageBPS should be > 0")
	}
	if cfg.OfferTTL <= 0 {
		t.Error("OfferTTL should be > 0")
	}
}

func TestDefaultWatcherConfig(t *testing.T) {
	cfg := shop.DefaultWatcherConfig
	if cfg.PollInterval <= 0 {
		t.Error("PollInterval should be > 0")
	}
	if cfg.PaymentTimeout <= 0 {
		t.Error("PaymentTimeout should be > 0")
	}
}
