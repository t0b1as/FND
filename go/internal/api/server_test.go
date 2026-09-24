package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/api"
	"github.com/fundus/node/internal/config"
	"github.com/fundus/node/internal/storage"
)

// =============================================================================
//  Test-Infrastruktur
// =============================================================================

// newTestServer erstellt einen API-Server mit echtem LevelDB-Storage
// aber ohne P2P-Node und ohne LLM (beide als nil akzeptiert durch Interface).
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	store, err := storage.New(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := &config.Config{
		Port:     3000,
		P2PPort:  4001,
		DataDir:  t.TempDir(),
		MeterID:  "test-meter",
		MeterLat: 50.0,
		MeterLon: 8.0,
	}

	// P2P-Node und Analyzer werden als nil übergeben –
	// Endpoints die sie brauchen geben 503 zurück, der Rest funktioniert.
	srv := api.NewServer(cfg, nil, store, nil, nil, zap.NewNop())
	return httptest.NewServer(srv.Handler())
}

func get(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func postJSON(t *testing.T, srv *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// =============================================================================
//  Health
// =============================================================================

func TestAPI_Health(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/health")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)
	if body["status"] != "ok" {
		t.Errorf("status = %v, want \"ok\"", body["status"])
	}
}

// =============================================================================
//  Listings – CRUD
// =============================================================================

func TestAPI_Listings_Empty(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/listings")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)
	listings, ok := body["listings"]
	if !ok {
		t.Fatal("response missing 'listings' key")
	}
	if listings == nil {
		t.Error("listings should not be nil")
	}
}

func TestAPI_Listings_CreateAndGet(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	payload := map[string]any{
		"title":       "Bosch Akkuschrauber 18V",
		"condition":   "gut",
		"price_min":   35.0,
		"price_max":   55.0,
		"description": "Sehr gut erhalten.",
		"keywords":    []string{"Akkuschrauber", "Bosch"},
	}

	createResp := postJSON(t, srv, "/api/v1/listings", payload)
	if createResp.StatusCode != http.StatusCreated {
		t.Errorf("create status = %d, want 201", createResp.StatusCode)
	}

	var created map[string]any
	decodeJSON(t, createResp, &created)
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatal("created listing has no id")
	}

	// GET einzelnes Listing
	getResp := get(t, srv, "/api/v1/listings/"+id)
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("get status = %d, want 200", getResp.StatusCode)
	}

	var got map[string]any
	decodeJSON(t, getResp, &got)
	if got["id"] != id {
		t.Errorf("id = %v, want %v", got["id"], id)
	}
}

func TestAPI_Listings_GetNonExistent_404(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/listings/doesnotexist")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAPI_Listings_CreateInvalidJSON_400(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/listings",
		"application/json", strings.NewReader("not-json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAPI_Listings_Create_SetsOwnerID(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	createResp := postJSON(t, srv, "/api/v1/listings", map[string]any{"title": "test"})
	var created map[string]any
	decodeJSON(t, createResp, &created)

	// owner_id muss gesetzt sein (auch wenn P2P-Node nil ist)
	if created["owner_id"] == nil || created["owner_id"] == "" {
		t.Error("created listing should have an owner_id")
	}
}

func TestAPI_Listings_List_ReturnsAllCreated(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	for i := 0; i < 3; i++ {
		postJSON(t, srv, "/api/v1/listings",
			map[string]any{"title": "item", "index": i})
	}

	resp := get(t, srv, "/api/v1/listings")
	var body map[string]any
	decodeJSON(t, resp, &body)

	count, _ := body["count"].(float64)
	if int(count) < 3 {
		t.Errorf("count = %.0f, want >= 3", count)
	}
}

// =============================================================================
//  Energie-Token
// =============================================================================

func TestAPI_EnergyTokens_Create(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	token := map[string]any{
		"timestamp":     time.Now().Format(time.RFC3339),
		"meter_id":      "DE001234",
		"lat":           50.11,
		"lon":           8.68,
		"kwh":           1.2345,
		"generator_lat": 50.0,
		"generator_lon": 8.0,
	}

	resp := postJSON(t, srv, "/api/v1/energy", token)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("create energy token status = %d, want 201", resp.StatusCode)
	}

	var created map[string]any
	decodeJSON(t, resp, &created)
	if created["id"] == nil {
		t.Error("created energy token has no id")
	}
}

func TestAPI_EnergyTokens_Fee_Calculation(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// Token erstellen: Erzeuger und Verbraucher ~10 km entfernt
	token := map[string]any{
		"meter_id":      "DE001234",
		"lat":           50.0,
		"lon":           8.1,   // ~8 km östlich
		"kwh":           100.0,
		"generator_lat": 50.0,
		"generator_lon": 8.0,
	}

	createResp := postJSON(t, srv, "/api/v1/energy", token)
	var created map[string]any
	decodeJSON(t, createResp, &created)
	id := created["id"].(string)

	// Gebühr berechnen
	feeResp := get(t, srv, "/api/v1/energy/"+id+"/fee")
	if feeResp.StatusCode != http.StatusOK {
		t.Errorf("fee status = %d, want 200", feeResp.StatusCode)
	}

	var fee map[string]any
	decodeJSON(t, feeResp, &fee)

	distKm, _ := fee["distance_km"].(float64)
	feeKwh, _ := fee["fee_kwh"].(float64)
	netKwh, _ := fee["net_kwh"].(float64)

	if distKm <= 0 {
		t.Errorf("distance_km = %.4f, must be > 0", distKm)
	}
	if feeKwh <= 0 {
		t.Errorf("fee_kwh = %.4f, must be > 0", feeKwh)
	}
	if netKwh <= 0 {
		t.Errorf("net_kwh = %.4f, must be > 0", netKwh)
	}

	// fee + net muss ~100 kWh ergeben
	total := feeKwh + netKwh
	if total < 99.9 || total > 100.1 {
		t.Errorf("fee_kwh + net_kwh = %.4f, want ~100.0", total)
	}

	// Gebühr muss zwischen Minimum und Maximum liegen
	feeRatio := feeKwh / 100.0
	if feeRatio < 0.001 || feeRatio > 0.15 {
		t.Errorf("fee ratio = %.4f, must be in [0.001, 0.15]", feeRatio)
	}
}

func TestAPI_EnergyTokens_Fee_SameLocation_MinimumFee(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	token := map[string]any{
		"meter_id":      "DE-SAME",
		"lat":           50.0, "lon": 8.0,
		"kwh":           100.0,
		"generator_lat": 50.0, "generator_lon": 8.0, // identisch
	}
	createResp := postJSON(t, srv, "/api/v1/energy", token)
	var created map[string]any
	decodeJSON(t, createResp, &created)

	feeResp := get(t, srv, "/api/v1/energy/"+created["id"].(string)+"/fee")
	var fee map[string]any
	decodeJSON(t, feeResp, &fee)

	// Distanz 0 → Minimum-Gebühr (0.1%)
	feeKwh, _ := fee["fee_kwh"].(float64)
	if feeKwh < 0.05 || feeKwh > 0.2 {
		t.Errorf("same-location fee_kwh = %.4f, expected ~0.1 (minimum)", feeKwh)
	}
}

func TestAPI_EnergyTokens_Fee_NotFound_404(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/energy/ghost/fee")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// =============================================================================
//  Jobs
// =============================================================================

func TestAPI_Jobs_CreateAndList(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	job := map[string]any{
		"title":       "Klempner gesucht",
		"type":        "request",
		"description": "Rohrbruch im Keller reparieren",
	}

	createResp := postJSON(t, srv, "/api/v1/jobs", job)
	if createResp.StatusCode != http.StatusCreated {
		t.Errorf("create job status = %d, want 201", createResp.StatusCode)
	}

	listResp := get(t, srv, "/api/v1/jobs")
	var body map[string]any
	decodeJSON(t, listResp, &body)
	count, _ := body["count"].(float64)
	if int(count) < 1 {
		t.Errorf("count = %.0f, want >= 1", count)
	}
}

// =============================================================================
//  LLM-Status (nil Analyzer)
// =============================================================================

func TestAPI_AnalyzeStatus_NoAnalyzer(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/analyze/status")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)
	if body["enabled"] != false {
		t.Errorf("enabled = %v, want false (no analyzer configured)", body["enabled"])
	}
}

func TestAPI_Analyze_NoAnalyzer_503(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/analyze",
		"multipart/form-data", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (no analyzer)", resp.StatusCode)
	}
}

// =============================================================================
//  Meter-Status (nil meterTokens)
// =============================================================================

func TestAPI_MeterStatus_NoMeter(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/meter/status")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)
	if body["enabled"] != false {
		t.Errorf("enabled = %v, want false (no meter configured)", body["enabled"])
	}
}

// =============================================================================
//  Concurrent access (Race-Detector)
// =============================================================================

func TestAPI_Listings_ConcurrentCreates(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	done := make(chan struct{}, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			postJSON(t, srv, "/api/v1/listings",
				map[string]any{"title": "concurrent", "index": i})
			done <- struct{}{}
		}(i)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 20; i++ {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("Concurrent creates timed out")
		}
	}

	resp := get(t, srv, "/api/v1/listings")
	var body map[string]any
	decodeJSON(t, resp, &body)
	count, _ := body["count"].(float64)
	if int(count) < 20 {
		t.Errorf("count = %.0f after 20 concurrent creates, want >= 20", count)
	}
}
