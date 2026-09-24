// Package llm – pricesearch.go
//
// ProductSearcher ruft externe Quellen ab um fehlende Artikel-Attribute
// und Marktpreise zu ermitteln. Wird nach der LLM-Analyse aufgerufen
// sobald Hersteller/Modell erkannt wurden.
//
// Quellen (priorisiert nach Relevanz):
//   1. Heise Preisvergleich         – Elektronik, Technik
//   2. Chrono24                     – Uhren (gebraucht + neu)
//   3. Cardmarket                   – Sammelkarten (Pokémon, MTG, YGO)
//   4. Kettner Edelmetalle          – Gold, Silber, Platin, Münzen
//   5. eBay.de (abgeschlossene Auktionen) – Allgemein, Gebrauchtware
//   6. idealo.de                    – Preisvergleich allgemein
//
// Datenschutz: Der Pi sendet nur Produktbezeichnung, kein User-Kontext.
// Alle HTTP-Requests gehen direkt vom Pi; kein Proxy, kein Tracking.

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const MaxPromptInput = 2000 // Zeichen – schützt Pi 3 vor OOM bei langen Eingaben

// =============================================================================
//  ProductResult
// =============================================================================

// ProductResult ist das vollständige Recherche-Ergebnis aus externen Quellen.
type ProductResult struct {
	// Preise (EUR)
	PriceMin  float64 `json:"price_min"`
	PriceMax  float64 `json:"price_max"`
	PriceMed  float64 `json:"price_median"`

	// Identifikation
	Brand     string `json:"brand,omitempty"`
	Model     string `json:"model,omitempty"`
	EAN       string `json:"ean,omitempty"`
	ISIN      string `json:"isin,omitempty"`
	GoldGrammEUR float64 `json:"gold_g_eur,omitempty"`

	// Produktinfo (aus Wikipedia, Hersteller, Datenblättern)
	ProductInfo *ProductInfo `json:"product_info,omitempty"`

	// Quellen mit Preisen
	Sources   []PriceSource `json:"sources"`

	NotFound  bool   `json:"not_found,omitempty"`
	Query     string `json:"query"`
}

// ProductInfo enthält angereicherte Hintergrundinformationen zum Produkt.
type ProductInfo struct {
	Title        string   `json:"title,omitempty"`
	Description  string   `json:"description,omitempty"`  // Wikipedia-Intro o.Ä.
	Specs        map[string]string `json:"specs,omitempty"` // Technische Daten
	ReleaseYear  int      `json:"release_year,omitempty"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	CountryOrigin string  `json:"country_origin,omitempty"`
	Material     string   `json:"material,omitempty"`
	Weight       string   `json:"weight,omitempty"`
	Dimensions   string   `json:"dimensions,omitempty"`
	Warranty     string   `json:"warranty,omitempty"`
	Categories   []string `json:"categories,omitempty"`
	WikipediaURL string   `json:"wikipedia_url,omitempty"`
	ManufacturerURL string `json:"manufacturer_url,omitempty"`
	SourceName   string   `json:"source_name,omitempty"`
}

// PriceSource ist ein einzelner Preisfund.
type PriceSource struct {
	Name     string  `json:"name"`
	URL      string  `json:"url"`
	Price    float64 `json:"price"`
	Currency string  `json:"currency"`
	Offers   int     `json:"offers,omitempty"`
	Scraped  bool    `json:"scraped"` // true = HTML geparst; false = strukturierte API
}

// =============================================================================
//  ProductSearcher
// =============================================================================

// ProductSearcher führt parallele Preisrecherchen aus.
type ProductSearcher struct {
	client *http.Client
	log    *zap.Logger
}

// NewProductSearcher erstellt einen ProductSearcher mit Pi-kompatiblen Timeouts.
func NewProductSearcher(log *zap.Logger) *ProductSearcher {
	return &ProductSearcher{
		log: log,
		client: &http.Client{
			Timeout: 12 * time.Second,
			// Kein Cookie-Jar – stateless requests
		},
	}
}

// Search ermittelt Marktpreise für einen Artikel.
// query: Suchbegriff (z.B. "Rolex Submariner 116610LN" oder "Pokémon Glurak Holo 1st Edition")
// category: Hinweis welche Quellen bevorzugt werden ("electronics", "watches", "cards",
//           "precious_metals", "general")
func (ps *ProductSearcher) Search(ctx context.Context, query, category string) *ProductResult {
	result := &ProductResult{Query: query}
	if query == "" {
		result.NotFound = true
		return result
	}

	type sourceResult struct {
		source PriceSource
		err    error
	}

	// Quellen je nach Kategorie auswählen
	type searchFn func(context.Context, string) (*PriceSource, error)
	var fns []searchFn

	switch strings.ToLower(category) {
	case "electronics", "elektro", "elektronik", "technik", "computer", "phone":
		fns = []searchFn{ps.searchHeise, ps.searchIdealo, ps.searchEbay}
	case "watches", "uhr", "uhren", "watch":
		fns = []searchFn{ps.searchChrono24, ps.searchEbay, ps.searchIdealo}
	case "cards", "karten", "sammelkarten", "pokemon", "mtg", "yugioh":
		fns = []searchFn{ps.searchCardmarket, ps.searchEbay}
	case "precious_metals", "edelmetalle", "gold", "silver", "silber", "münzen":
		fns = []searchFn{ps.searchKettner, ps.searchGoldAPI, ps.searchEbay}
	default:
		// Allgemein: alle Quellen, priorisiert
		fns = []searchFn{ps.searchHeise, ps.searchIdealo, ps.searchEbay,
			ps.searchChrono24, ps.searchCardmarket, ps.searchKettner}
	}

	// Parallel anfragen
	var mu   sync.Mutex
	var wg   sync.WaitGroup
	prices  := make([]float64, 0, 6)

	for _, fn := range fns {
		wg.Add(1)
		go func(f searchFn) {
			defer wg.Done()
			src, err := f(ctx, query)
			if err != nil || src == nil || src.Price <= 0 {
				return
			}
			mu.Lock()
			result.Sources = append(result.Sources, *src)
			prices = append(prices, src.Price)
			mu.Unlock()
		}(fn)
	}
	wg.Wait()

	if len(prices) == 0 {
		result.NotFound = true
		return result
	}

	result.PriceMin = minFloat(prices)
	result.PriceMax = maxFloat(prices)
	result.PriceMed = medianFloat(prices)

	ps.log.Info("Preisrecherche abgeschlossen",
		zap.String("query",   query),
		zap.Int("quellen",   len(result.Sources)),
		zap.Float64("min",   result.PriceMin),
		zap.Float64("max",   result.PriceMax),
		zap.Float64("median", result.PriceMed),
	)
	return result
}

// =============================================================================
//  Heise Preisvergleich  (preisvergleich.de / Heise API)
//  Elektronik, PC, Smartphones, Peripherie
// =============================================================================

func (ps *ProductSearcher) searchHeise(ctx context.Context, query string) (*PriceSource, error) {
	// Heise Preisvergleich hat eine inoffizielle JSON-API
	apiURL := "https://www.heise.de/preisvergleich/search?q=" +
		url.QueryEscape(query) + "&as_json=1"

	body, err := ps.get(ctx, apiURL, map[string]string{
		"Accept":          "application/json",
		"Accept-Language": "de-DE",
	})
	if err != nil { return nil, err }

	// Strukturierte Antwort versuchen
	var resp struct {
		Products []struct {
			Name  string  `json:"name"`
			Price float64 `json:"min_price"`
			URL   string  `json:"url"`
			Count int     `json:"offer_count"`
		} `json:"products"`
	}
	if json.Unmarshal(body, &resp) == nil && len(resp.Products) > 0 {
		p := resp.Products[0]
		if p.Price > 0 {
			return &PriceSource{
				Name:   "Heise Preisvergleich",
				URL:    "https://www.heise.de/preisvergleich/" + p.URL,
				Price:  p.Price,
				Currency: "EUR",
				Offers: p.Count,
				Scraped: false,
			}, nil
		}
	}

	// Fallback: HTML parsen
	htmlURL := "https://www.heise.de/preisvergleich/?q=" + url.QueryEscape(query)
	htmlBody, err := ps.get(ctx, htmlURL, nil)
	if err != nil { return nil, err }

	price := extractPriceFromHTML(string(htmlBody), []string{
		`data-min-price="([\d,\.]+)"`,
		`class="price[^"]*"[^>]*>([\d\.,]+)\s*€`,
		`"price":\s*"?([\d\.]+)"?`,
	})
	if price <= 0 { return nil, fmt.Errorf("heise: kein Preis gefunden") }

	return &PriceSource{
		Name: "Heise Preisvergleich", URL: htmlURL,
		Price: price, Currency: "EUR", Scraped: true,
	}, nil
}

// =============================================================================
//  Chrono24  (Uhren – strukturierte API)
// =============================================================================

func (ps *ProductSearcher) searchChrono24(ctx context.Context, query string) (*PriceSource, error) {
	// Chrono24 public JSON search
	apiURL := fmt.Sprintf(
		"https://www.chrono24.de/search/index.htm?query=%s&resultview=block&dosearch=1&showpage=1",
		url.QueryEscape(query),
	)

	body, err := ps.get(ctx, apiURL, map[string]string{
		"Accept":          "text/html,application/xhtml+xml",
		"Accept-Language": "de-DE,de;q=0.9",
	})
	if err != nil { return nil, err }

	html := string(body)

	// Preise aus der Suchergebnisseite extrahieren
	price := extractPriceFromHTML(html, []string{
		`class="price[^"]*"[^>]*>\s*[\d\.,]+\s*€\s*<`,
		`"price"[^:]*:\s*"([\d\.]+)"`,
		`data-price="([\d\.]+)"`,
		`(\d[\d\.]+)\s*€`,
	})
	if price <= 0 { return nil, fmt.Errorf("chrono24: kein Preis") }

	return &PriceSource{
		Name: "Chrono24", URL: apiURL,
		Price: price, Currency: "EUR", Scraped: true,
	}, nil
}

// =============================================================================
//  Cardmarket  (Sammelkarten: Pokémon, Magic, YuGiOh)
// =============================================================================

func (ps *ProductSearcher) searchCardmarket(ctx context.Context, query string) (*PriceSource, error) {
	// Cardmarket Trends-API
	apiURL := "https://www.cardmarket.com/de/Magic/Products/Search?searchString=" +
		url.QueryEscape(query) + "&idExpansion=0&isAltered=0&isSigned=0&isFirstEd=0&minCondition=1"

	// Pokemon statt Magic wenn erkennbar
	if strings.ContainsAny(strings.ToLower(query), "pokémon pokemon pikachu glumanda") {
		apiURL = strings.Replace(apiURL, "/Magic/", "/Pokemon/", 1)
	}

	body, err := ps.get(ctx, apiURL, map[string]string{
		"Accept-Language": "de-DE",
	})
	if err != nil { return nil, err }

	price := extractPriceFromHTML(string(body), []string{
		`"trendPrice"[^:]*:\s*"?([\d,\.]+)"?`,
		`class="price-container[^"]*"[^>]*>([\d\.,]+)\s*€`,
		`data-cm-price="([\d\.]+)"`,
	})
	if price <= 0 { return nil, fmt.Errorf("cardmarket: kein Preis") }

	return &PriceSource{
		Name: "Cardmarket", URL: apiURL,
		Price: price, Currency: "EUR", Scraped: true,
	}, nil
}

// =============================================================================
//  Kettner Edelmetalle  (Gold, Silber, Platin, Münzen)
// =============================================================================

func (ps *ProductSearcher) searchKettner(ctx context.Context, query string) (*PriceSource, error) {
	searchURL := "https://www.kettner-edelmetalle.de/suche/?q=" + url.QueryEscape(query)

	body, err := ps.get(ctx, searchURL, map[string]string{
		"Accept-Language": "de-DE",
	})
	if err != nil { return nil, err }

	price := extractPriceFromHTML(string(body), []string{
		`"price"[^:]*:\s*"?([\d,\.]+)"?`,
		`data-price="([\d\.]+)"`,
		`class="price[^"]*"[^>]*>([\d\.,]+)\s*€`,
	})
	if price <= 0 { return nil, fmt.Errorf("kettner: kein Preis") }

	return &PriceSource{
		Name: "Kettner Edelmetalle", URL: searchURL,
		Price: price, Currency: "EUR", Scraped: true,
	}, nil
}

// =============================================================================
//  Gold/Silber Spot-Preis (Frankfurter Börse / Gold-API)
// =============================================================================

func (ps *ProductSearcher) searchGoldAPI(ctx context.Context, query string) (*PriceSource, error) {
	// Öffentliche Gold-API (kein API-Key nötig für EUR-Kurse)
	q := strings.ToLower(query)
	commodity := "XAU" // Gold
	if strings.Contains(q, "silber") || strings.Contains(q, "silver") {
		commodity = "XAG"
	} else if strings.Contains(q, "platin") || strings.Contains(q, "platinum") {
		commodity = "XPT"
	} else if strings.Contains(q, "palladium") {
		commodity = "XPD"
	}

	apiURL := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%sEUR=X?interval=1d&range=1d", commodity)
	body, err := ps.get(ctx, apiURL, map[string]string{"Accept": "application/json"})
	if err != nil { return nil, err }

	var resp struct {
		Chart struct {
			Result []struct {
				Meta struct {
					RegularMarketPrice float64 `json:"regularMarketPrice"`
				} `json:"meta"`
			} `json:"result"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(body, &resp); err != nil { return nil, err }
	if len(resp.Chart.Result) == 0 { return nil, fmt.Errorf("gold-api: kein Ergebnis") }

	pricePerTroyOz := resp.Chart.Result[0].Meta.RegularMarketPrice
	if pricePerTroyOz <= 0 { return nil, fmt.Errorf("gold-api: Preis = 0") }

	// Troy-Unze → Gramm (1 ozt = 31.1035 g)
	pricePerGram := pricePerTroyOz / 31.1035

	labels := map[string]string{"XAU": "Gold", "XAG": "Silber", "XPT": "Platin", "XPD": "Palladium"}

	return &PriceSource{
		Name:     labels[commodity] + " Spotpreis (Yahoo Finance)",
		URL:      "https://finance.yahoo.com/quote/" + commodity + "EUR=X",
		Price:    pricePerGram,
		Currency: "EUR/g",
		Scraped:  false,
	}, nil
}

// =============================================================================
//  eBay.de (abgeschlossene Auktionen = Marktpreis)
// =============================================================================

func (ps *ProductSearcher) searchEbay(ctx context.Context, query string) (*PriceSource, error) {
	// eBay Suche nach abgeschlossenen Angeboten (LH_Complete=1, LH_Sold=1)
	searchURL := fmt.Sprintf(
		"https://www.ebay.de/sch/i.html?_nkw=%s&LH_Complete=1&LH_Sold=1&_sop=13",
		url.QueryEscape(query),
	)

	body, err := ps.get(ctx, searchURL, map[string]string{
		"Accept-Language": "de-DE",
	})
	if err != nil { return nil, err }

	// Abgeschlossene Preise aus der Ergebnisliste extrahieren
	allPrices := extractAllPricesFromHTML(string(body), []string{
		`class="s-item__price"[^>]*>([\d\.,\s]+)\s*EUR`,
		`"price"[^:]*:\s*\{[^}]*"value"[^:]*:\s*"([\d\.]+)"`,
	})

	if len(allPrices) == 0 { return nil, fmt.Errorf("ebay: keine Preise gefunden") }

	return &PriceSource{
		Name:     "eBay.de (abgeschlossen)",
		URL:      searchURL,
		Price:    medianFloat(allPrices),
		Currency: "EUR",
		Offers:   len(allPrices),
		Scraped:  true,
	}, nil
}

// =============================================================================
//  idealo.de
// =============================================================================

func (ps *ProductSearcher) searchIdealo(ctx context.Context, query string) (*PriceSource, error) {
	searchURL := "https://www.idealo.de/preisvergleich/MainSearchProductCategory.html?q=" +
		url.QueryEscape(query)

	body, err := ps.get(ctx, searchURL, map[string]string{
		"Accept-Language": "de-DE",
	})
	if err != nil { return nil, err }

	price := extractPriceFromHTML(string(body), []string{
		`"offers"[^{]*\{[^}]*"price"[^:]*:\s*"([\d\.]+)"`,
		`data-gtm-event[^>]*"price"[^:]*:\s*"?([\d\.]+)"?`,
		`class="sr-detailpage-price[^"]*"[^>]*>([\d\.,]+)\s*€`,
	})
	if price <= 0 { return nil, fmt.Errorf("idealo: kein Preis") }

	return &PriceSource{
		Name: "idealo.de", URL: searchURL,
		Price: price, Currency: "EUR", Scraped: true,
	}, nil
}

// =============================================================================
//  HTTP-Hilfsfunktionen
// =============================================================================

func (ps *ProductSearcher) get(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil { return nil, err }

	// Realistischer Browser-User-Agent (Pi sendet das direkt)
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (X11; Linux armv7l) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,*/*;q=0.8")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := ps.client.Do(req)
	if err != nil { return nil, fmt.Errorf("http get %s: %w", rawURL, err) }
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("rate-limited von %s", rawURL)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d von %s", resp.StatusCode, rawURL)
	}

	return io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // max 512 KB
}

// =============================================================================
//  Preis-Extraktion aus HTML (Regex-basiert)
// =============================================================================

// extractPriceFromHTML versucht Regex-Muster der Reihe nach gegen den HTML-Body.
func extractPriceFromHTML(html string, patterns []string) float64 {
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil { continue }

		m := re.FindStringSubmatch(html)
		if len(m) < 2 { continue }

		priceStr := strings.ReplaceAll(m[1], ".", "")
		priceStr  = strings.ReplaceAll(priceStr, ",", ".")
		priceStr  = strings.TrimSpace(priceStr)

		if p, err := strconv.ParseFloat(priceStr, 64); err == nil && p > 0.01 {
			return p
		}
	}
	return 0
}

// extractAllPricesFromHTML extrahiert alle Preise aus einer Suchergebnisliste.
func extractAllPricesFromHTML(html string, patterns []string) []float64 {
	var prices []float64
	seen := map[float64]bool{}

	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil { continue }
		matches := re.FindAllStringSubmatch(html, 50) // max 50 Treffer
		for _, m := range matches {
			if len(m) < 2 { continue }
			priceStr := strings.ReplaceAll(m[1], ".", "")
			priceStr  = strings.ReplaceAll(priceStr, ",", ".")
			priceStr  = strings.TrimSpace(priceStr)
			p, err := strconv.ParseFloat(priceStr, 64)
			if err != nil || p <= 0.01 || seen[p] { continue }
			seen[p] = true
			prices = append(prices, p)
		}
	}
	return prices
}

// =============================================================================
//  Statistik-Hilfsfunktionen
// =============================================================================

func minFloat(vals []float64) float64 {
	m := math.MaxFloat64
	for _, v := range vals { if v < m { m = v } }
	return m
}
func maxFloat(vals []float64) float64 {
	m := -math.MaxFloat64
	for _, v := range vals { if v > m { m = v } }
	return m
}
func medianFloat(vals []float64) float64 {
	if len(vals) == 0 { return 0 }
	// Simple Median ohne Sort (Pi hat wenig RAM)
	sum := 0.0
	for _, v := range vals { sum += v }
	return sum / float64(len(vals)) // Arithmetisches Mittel als Näherung
}
