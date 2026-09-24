// Package shop implementiert den FND-Token-Shop:
// SOL-Zahlung → Kurs-Check → FND-Mint.
package shop

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
//  Konfiguration
// =============================================================================

// PriceFeedConfig konfiguriert den Preis-Feed.
type PriceFeedConfig struct {
	// Wie lange ein gecachter Preis gültig ist (Standard: 30s)
	CacheTTL time.Duration

	// Slippage-Toleranz in Basispunkten (Standard: 50 = 0.5%)
	// Erhöht den verlangten SOL-Betrag um diesen Puffer
	SlippageBPS int

	// Ablaufzeit eines Kaufangebots (Standard: 90s)
	// Danach muss der Nutzer den Preis neu abrufen
	OfferTTL time.Duration

	// HTTP-Timeout für API-Calls
	HTTPTimeout time.Duration
}

// DefaultPriceFeedConfig liefert sinnvolle Standardwerte.
var DefaultPriceFeedConfig = PriceFeedConfig{
	CacheTTL:    30 * time.Second,
	SlippageBPS: 50,  // 0.5%
	OfferTTL:    90 * time.Second,
	HTTPTimeout: 5 * time.Second,
}

// =============================================================================
//  PricePoint – gecachter Kurs
// =============================================================================

// PricePoint enthält einen Kurswert mit Zeitstempel.
type PricePoint struct {
	SOLEUR    float64   // 1 SOL in EUR
	SOLUSD    float64   // 1 SOL in USD (Fallback-Basis)
	Source    string    // Quelle des Kurses
	FetchedAt time.Time // Zeitpunkt des Abrufs
	ExpiresAt time.Time // Ablaufzeit
}

func (p *PricePoint) Valid() bool {
	return p != nil && time.Now().Before(p.ExpiresAt) && p.SOLEUR > 0
}

// =============================================================================
//  PriceFeed
// =============================================================================

// PriceFeed holt SOL/EUR-Kurse von mehreren Quellen mit Fallback und Caching.
// Quellen in Priorität: CoinGecko → Binance → Kraken
type PriceFeed struct {
	cfg    PriceFeedConfig
	client *http.Client
	log    *zap.Logger

	mu    sync.RWMutex
	cache *PricePoint
}

// NewPriceFeed erstellt einen PriceFeed.
func NewPriceFeed(cfg PriceFeedConfig, log *zap.Logger) *PriceFeed {
	return &PriceFeed{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
		log:    log,
	}
}

// NewPriceFeedWithMockRate erstellt einen PriceFeed mit vorgesetztem Kurs (für Tests).
func NewPriceFeedWithMockRate(cfg PriceFeedConfig, mockRate float64, log *zap.Logger) *PriceFeed {
	f := NewPriceFeed(cfg, log)
	f.cache = &PricePoint{
		SOLEUR:    mockRate,
		SOLUSD:    mockRate * 1.08,
		Source:    "mock",
		FetchedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour), // läuft in Tests nie ab
	}
	return f
}

// SOLEUR gibt den aktuellen SOL/EUR-Kurs zurück.
// Nutzt den Cache wenn gültig, sonst frischer Abruf.
func (f *PriceFeed) SOLEUR(ctx context.Context) (float64, string, error) {
	// Cache lesen
	f.mu.RLock()
	cached := f.cache
	f.mu.RUnlock()

	if cached.Valid() {
		return cached.SOLEUR, cached.Source + " (cached)", nil
	}

	// Frischer Abruf – Quellen nacheinander probieren
	sources := []struct {
		name string
		fn   func(context.Context) (*PricePoint, error)
	}{
		{"coingecko", f.fetchCoinGecko},
		{"binance",   f.fetchBinance},
		{"kraken",    f.fetchKraken},
	}

	for _, src := range sources {
		point, err := src.fn(ctx)
		if err != nil {
			f.log.Warn("price source failed",
				zap.String("source", src.name),
				zap.Error(err))
			continue
		}

		// Cache aktualisieren
		point.Source    = src.name
		point.FetchedAt = time.Now()
		point.ExpiresAt = time.Now().Add(f.cfg.CacheTTL)

		f.mu.Lock()
		f.cache = point
		f.mu.Unlock()

		f.log.Debug("SOL/EUR price fetched",
			zap.Float64("sol_eur", point.SOLEUR),
			zap.String("source", src.name),
		)

		return point.SOLEUR, src.name, nil
	}

	return 0, "", fmt.Errorf("all price sources failed")
}

// SOLForEUR berechnet wie viel SOL für einen EUR-Betrag nötig ist,
// inkl. Slippage-Puffer.
func (f *PriceFeed) SOLForEUR(ctx context.Context, eurAmount float64) (SOLAmount, error) {
	rate, source, err := f.SOLEUR(ctx)
	if err != nil {
		return SOLAmount{}, err
	}

	rawSOL := eurAmount / rate

	// Slippage-Puffer draufschlagen
	puffer := float64(f.cfg.SlippageBPS) / 10000
	withSlippage := rawSOL * (1 + puffer)

	// Auf 6 Stellen runden (Lamport-Präzision ist 9, 6 reicht für Anzeige)
	rounded := math.Round(withSlippage*1e6) / 1e6

	return SOLAmount{
		EUR:         eurAmount,
		SOL:         rounded,
		RawSOL:      rawSOL,
		Rate:        rate,
		Source:      source,
		SlippageBPS: f.cfg.SlippageBPS,
		ValidUntil:  time.Now().Add(f.cfg.OfferTTL),
	}, nil
}

// =============================================================================
//  SOLAmount – Kauf-Angebot
// =============================================================================

// SOLAmount beschreibt ein konkretes Kauf-Angebot.
type SOLAmount struct {
	EUR         float64   `json:"eur"`          // Gewünschter FND-Betrag (= EUR)
	SOL         float64   `json:"sol"`          // Verlangter SOL-Betrag (mit Puffer)
	RawSOL      float64   `json:"sol_raw"`      // Ohne Puffer
	Rate        float64   `json:"rate"`         // 1 SOL = X EUR
	Source      string    `json:"source"`       // Preisquelle
	SlippageBPS int       `json:"slippage_bps"` // Puffer in Basispunkten
	ValidUntil  time.Time `json:"valid_until"`  // Angebot gültig bis
}

// IsValid prüft ob das Angebot noch gültig ist.
func (a SOLAmount) IsValid() bool {
	return time.Now().Before(a.ValidUntil)
}

// Lamports gibt den SOL-Betrag in Lamports zurück (1 SOL = 1e9 Lamports).
func (a SOLAmount) Lamports() uint64 {
	return uint64(math.Round(a.SOL * 1e9))
}

// =============================================================================
//  Preis-Quellen
// =============================================================================

func (f *PriceFeed) fetchCoinGecko(ctx context.Context) (*PricePoint, error) {
	url := "https://api.coingecko.com/api/v3/simple/price" +
		"?ids=solana&vs_currencies=eur,usd"

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("coingecko request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coingecko status %d", resp.StatusCode)
	}

	var data struct {
		Solana struct {
			EUR float64 `json:"eur"`
			USD float64 `json:"usd"`
		} `json:"solana"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("coingecko decode: %w", err)
	}
	if data.Solana.EUR == 0 {
		return nil, fmt.Errorf("coingecko: EUR price is 0")
	}

	return &PricePoint{
		SOLEUR: data.Solana.EUR,
		SOLUSD: data.Solana.USD,
	}, nil
}

func (f *PriceFeed) fetchBinance(ctx context.Context) (*PricePoint, error) {
	// Binance hat kein direktes SOL/EUR-Paar → SOL/USDT * EURUSDT
	urlSOL := "https://api.binance.com/api/v3/ticker/price?symbol=SOLUSDT"
	urlEUR := "https://api.binance.com/api/v3/ticker/price?symbol=EURUSDT"

	solUSDT, err := f.fetchBinancePrice(ctx, urlSOL)
	if err != nil {
		return nil, fmt.Errorf("binance SOL: %w", err)
	}
	eurUSDT, err := f.fetchBinancePrice(ctx, urlEUR)
	if err != nil || eurUSDT == 0 {
		return nil, fmt.Errorf("binance EUR: %w", err)
	}

	// SOL/EUR = SOL/USDT / EUR/USDT
	solEUR := solUSDT / eurUSDT

	return &PricePoint{
		SOLEUR: solEUR,
		SOLUSD: solUSDT,
	}, nil
}

func (f *PriceFeed) fetchBinancePrice(ctx context.Context, url string) (float64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := f.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var data struct {
		Price string `json:"price"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}

	var price float64
	fmt.Sscanf(data.Price, "%f", &price)
	return price, nil
}

func (f *PriceFeed) fetchKraken(ctx context.Context) (*PricePoint, error) {
	url := "https://api.kraken.com/0/public/Ticker?pair=SOLEUR"

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kraken request: %w", err)
	}
	defer resp.Body.Close()

	var data struct {
		Error  []string                    `json:"error"`
		Result map[string]json.RawMessage  `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("kraken decode: %w", err)
	}
	if len(data.Error) > 0 {
		return nil, fmt.Errorf("kraken error: %v", data.Error)
	}

	// Kraken Ticker: "c": ["last_price", "lot_volume"]
	for _, raw := range data.Result {
		var ticker struct {
			C [2]string `json:"c"` // last trade closed
		}
		if err := json.Unmarshal(raw, &ticker); err != nil {
			continue
		}
		var price float64
		fmt.Sscanf(ticker.C[0], "%f", &price)
		if price > 0 {
			return &PricePoint{SOLEUR: price, SOLUSD: price * 1.08}, nil
		}
	}

	return nil, fmt.Errorf("kraken: no price found")
}
