package meter_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/meter"
)

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func newKey(t *testing.T) string {
	t.Helper()
	// 32 Null-Bytes als Test-Schlüssel (reproduzierbar)
	return hex.EncodeToString(make([]byte, 32))
}

func baseConfig() meter.Config {
	return meter.Config{
		Protocol:  "mock",
		MeterID:   "TEST-METER-001",
		Lat:       50.1109,
		Lon:       8.6821,
		GeneratorLat: 50.0,
		GeneratorLon: 8.0,
		Interval:  10 * time.Millisecond, // schnell für Tests
	}
}

// =============================================================================
//  NewReader – Konfigurationsvalidierung
// =============================================================================

func TestNewReader_ValidConfig(t *testing.T) {
	cfg := baseConfig()
	cfg.SigningKeyHex = newKey(t)
	_, err := meter.NewReader(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewReader with valid config: %v", err)
	}
}

func TestNewReader_InvalidSigningKey(t *testing.T) {
	cfg := baseConfig()
	cfg.SigningKeyHex = "not-hex!!!"
	_, err := meter.NewReader(cfg, zap.NewNop())
	if err == nil {
		t.Error("Expected error for invalid signing key hex, got nil")
	}
}

func TestNewReader_ShortSigningKey(t *testing.T) {
	cfg := baseConfig()
	cfg.SigningKeyHex = hex.EncodeToString([]byte("tooshort")) // nur 8 Bytes
	_, err := meter.NewReader(cfg, zap.NewNop())
	if err == nil {
		t.Error("Expected error for too-short signing key, got nil")
	}
}

func TestNewReader_UnknownProtocol(t *testing.T) {
	cfg := baseConfig()
	cfg.Protocol = "magic-protocol"
	r, err := meter.NewReader(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = r.Run(ctx)
	if err == nil {
		t.Error("Expected error for unknown protocol on Run(), got nil")
	}
}

func TestNewReader_DefaultInterval(t *testing.T) {
	cfg := baseConfig()
	cfg.Interval = 0 // soll auf 1s defaulten
	r, err := meter.NewReader(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	// Nur prüfen dass kein Panic auftritt
	_ = r
}

// =============================================================================
//  Mock-Reader: Token-Ausgabe
// =============================================================================

func TestMockReader_ProducesTokens(t *testing.T) {
	cfg := baseConfig()
	cfg.Interval = 10 * time.Millisecond

	r, err := meter.NewReader(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ch, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var tokens []meter.Token
	for tok := range ch {
		tokens = append(tokens, tok)
	}

	if len(tokens) == 0 {
		t.Error("Expected at least one token from mock reader")
	}
}

func TestMockReader_TokenFields(t *testing.T) {
	cfg := baseConfig()
	cfg.Interval = 10 * time.Millisecond

	r, err := meter.NewReader(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	ch, _ := r.Run(ctx)
	tok := <-ch

	if tok.MeterID != "TEST-METER-001" {
		t.Errorf("MeterID = %q, want %q", tok.MeterID, "TEST-METER-001")
	}
	if tok.Lat != 50.1109 {
		t.Errorf("Lat = %v, want 50.1109", tok.Lat)
	}
	if tok.Lon != 8.6821 {
		t.Errorf("Lon = %v, want 8.6821", tok.Lon)
	}
	if tok.KWh < 0 {
		t.Errorf("KWh = %v, must not be negative", tok.KWh)
	}
	if tok.Timestamp.IsZero() {
		t.Error("Timestamp must not be zero")
	}
}

func TestMockReader_ChannelClosesOnContextCancel(t *testing.T) {
	cfg := baseConfig()
	r, _ := meter.NewReader(cfg, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := r.Run(ctx)

	cancel()

	// Channel muss nach Cancel geschlossen werden
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // Channel korrekt geschlossen
			}
		case <-timeout:
			t.Error("Channel not closed within 500ms after context cancel")
			return
		}
	}
}

// =============================================================================
//  Token-Signierung und Verifikation
// =============================================================================

func TestToken_SignAndVerify(t *testing.T) {
	cfg := baseConfig()
	cfg.SigningKeyHex = newKey(t)

	r, err := meter.NewReader(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ch, _ := r.Run(ctx)
	tok := <-ch

	if tok.Signature == "" {
		t.Fatal("Signature is empty")
	}
	if !r.Verify(tok) {
		t.Error("Verify returned false for fresh token")
	}
}

func TestToken_TamperedData_FailsVerify(t *testing.T) {
	cfg := baseConfig()
	cfg.SigningKeyHex = newKey(t)
	r, _ := meter.NewReader(cfg, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ch, _ := r.Run(ctx)
	tok := <-ch

	// Manipulieren
	tok.KWh += 100

	if r.Verify(tok) {
		t.Error("Verify should return false after tampering with KWh")
	}
}

func TestToken_TamperedMeterID_FailsVerify(t *testing.T) {
	cfg := baseConfig()
	cfg.SigningKeyHex = newKey(t)
	r, _ := meter.NewReader(cfg, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ch, _ := r.Run(ctx)
	tok := <-ch

	tok.MeterID = "MANIPULATED"

	if r.Verify(tok) {
		t.Error("Verify should return false after tampering with MeterID")
	}
}

func TestToken_NoKey_VerifyAlwaysTrue(t *testing.T) {
	cfg := baseConfig()
	// Kein SigningKey gesetzt
	r, _ := meter.NewReader(cfg, zap.NewNop())

	tok := meter.Token{
		Timestamp: time.Now(),
		MeterID:   "test",
		KWh:       1.0,
	}
	if !r.Verify(tok) {
		t.Error("Verify without key should always return true")
	}
}

// =============================================================================
//  Token-Felder: Rundung
// =============================================================================

func TestMockReader_KWhRounding(t *testing.T) {
	// kWh soll auf 4 Nachkommastellen gerundet sein
	cfg := baseConfig()
	r, _ := meter.NewReader(cfg, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ch, _ := r.Run(ctx)
	tok := <-ch

	rounded := math.Round(tok.KWh*10000) / 10000
	if tok.KWh != rounded {
		t.Errorf("KWh = %v not rounded to 4 decimal places", tok.KWh)
	}
}

func TestMockReader_WattRounding(t *testing.T) {
	cfg := baseConfig()
	r, _ := meter.NewReader(cfg, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ch, _ := r.Run(ctx)
	tok := <-ch

	rounded := math.Round(tok.WattNow*10) / 10
	if tok.WattNow != rounded {
		t.Errorf("WattNow = %v not rounded to 1 decimal place", tok.WattNow)
	}
}

// =============================================================================
//  HTTP-Protokoll: Shelly EM + Tasmota
// =============================================================================

func TestHTTPReader_ShellyEM(t *testing.T) {
	shellyResp := `{
		"emeters": [
			{"power": 350.5, "total": 12345.6, "voltage": 230.1},
			{"power": 0, "total": 0, "voltage": 0}
		]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(shellyResp))
	}))
	defer srv.Close()

	cfg := baseConfig()
	cfg.Protocol = "http"
	cfg.HTTPURL  = srv.URL
	cfg.Interval = 10 * time.Millisecond

	r, err := meter.NewReader(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ch, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	tok := <-ch
	// Total: 12345.6 Wh → 12.3456 kWh
	wantKWh := 12345.6 / 1000
	if math.Abs(tok.KWh-wantKWh) > 0.001 {
		t.Errorf("KWh = %.4f, want %.4f (Shelly total/1000)", tok.KWh, wantKWh)
	}
	if math.Abs(tok.WattNow-350.5) > 0.1 {
		t.Errorf("WattNow = %.1f, want 350.5", tok.WattNow)
	}
}

func TestHTTPReader_Tasmota(t *testing.T) {
	tasmotaResp := `{
		"StatusSNS": {
			"ENERGY": {
				"Power": 275.0,
				"Today": 1.234,
				"Total": 99.876
			}
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tasmotaResp))
	}))
	defer srv.Close()

	cfg := baseConfig()
	cfg.Protocol = "http"
	cfg.HTTPURL  = srv.URL
	cfg.Interval = 10 * time.Millisecond

	r, _ := meter.NewReader(cfg, zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ch, _ := r.Run(ctx)
	tok := <-ch

	if math.Abs(tok.KWh-99.876) > 0.001 {
		t.Errorf("KWh = %.4f, want 99.876 (Tasmota Total)", tok.KWh)
	}
	if math.Abs(tok.WattNow-275.0) > 0.1 {
		t.Errorf("WattNow = %.1f, want 275.0", tok.WattNow)
	}
}

func TestHTTPReader_GenericJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]float64{"kwh": 42.5, "watt": 500})
	}))
	defer srv.Close()

	cfg := baseConfig()
	cfg.Protocol = "http"
	cfg.HTTPURL  = srv.URL
	cfg.Interval = 10 * time.Millisecond

	r, _ := meter.NewReader(cfg, zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ch, _ := r.Run(ctx)
	tok := <-ch

	if math.Abs(tok.KWh-42.5) > 0.001 {
		t.Errorf("KWh = %.4f, want 42.5", tok.KWh)
	}
}

func TestHTTPReader_ServerError_ContinuesRunning(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 2 {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]float64{"kwh": 1.0, "watt": 100})
	}))
	defer srv.Close()

	cfg := baseConfig()
	cfg.Protocol = "http"
	cfg.HTTPURL  = srv.URL
	cfg.Interval = 10 * time.Millisecond

	r, _ := meter.NewReader(cfg, zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ch, _ := r.Run(ctx)

	var tokens []meter.Token
	for tok := range ch {
		tokens = append(tokens, tok)
	}

	// Nach 2 Fehlern sollten wir wenigstens 1 gültigen Token bekommen
	if len(tokens) == 0 {
		t.Error("Expected tokens after server errors recovered, got none")
	}
}
