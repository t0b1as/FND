package api_test

// Partner-API-Tests.
// Hilfsfunktionen newTestServer, get, postJSON, decodeJSON sind in server_test.go definiert.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// putJSON sendet einen HTTP-PUT mit JSON-Body.
func putJSON(t *testing.T, srv *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return resp
}

// deleteReq sendet einen HTTP-DELETE.
func deleteReq(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

// validPartnerProfile liefert ein gültiges Profil für Tests.
func validPartnerProfile() map[string]any {
	return map[string]any{
		"nickname":  "TestUser",
		"gender":    "female",
		"age_range": "26-35",
		"lat":       50.11,
		"lon":       8.68,
		"interests": []string{"Musik", "Wandern", "Kochen"},
		"bio":       "Nur lokal sichtbar.",
		"seeking": map[string]any{
			"genders":    []string{"male"},
			"age_ranges": []string{"26-35", "36-45"},
			"radius_km":  50.0,
		},
	}
}

// =============================================================================
//  Profil – CRUD
// =============================================================================

func TestPartner_Profile_NotFound_Initially(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/partner/profile")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no profile yet)", resp.StatusCode)
	}
}

func TestPartner_Profile_UpsertAndGet(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putResp := putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())
	if putResp.StatusCode != http.StatusOK {
		t.Errorf("PUT status = %d, want 200", putResp.StatusCode)
	}

	getResp := get(t, srv, "/api/v1/partner/profile")
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET status = %d, want 200", getResp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, getResp, &body)
	if body["nickname"] != "TestUser" {
		t.Errorf("nickname = %v, want TestUser", body["nickname"])
	}
}

func TestPartner_Profile_BioStoredButNotInPutResponse(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putResp := putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())
	var body map[string]any
	decodeJSON(t, putResp, &body)

	// Bio darf NICHT in der Antwort des PUT-Requests erscheinen
	if _, ok := body["bio"]; ok {
		t.Error("bio must not appear in PUT /partner/profile response")
	}
}

func TestPartner_Profile_SaltNeverInResponse(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putResp := putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())
	var body map[string]any
	decodeJSON(t, putResp, &body)

	if _, ok := body["salt_hex"]; ok {
		t.Error("salt_hex must not appear in any API response")
	}
}

func TestPartner_Profile_Delete(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())

	delResp := deleteReq(t, srv, "/api/v1/partner/profile")
	if delResp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", delResp.StatusCode)
	}

	getResp := get(t, srv, "/api/v1/partner/profile")
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after DELETE status = %d, want 404", getResp.StatusCode)
	}
}

func TestPartner_Profile_Delete_Idempotent(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// Zweimaliges DELETE darf nicht crashen
	deleteReq(t, srv, "/api/v1/partner/profile")
	resp := deleteReq(t, srv, "/api/v1/partner/profile")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("second DELETE status = %d, want 204", resp.StatusCode)
	}
}

func TestPartner_Profile_InvalidJSON_400(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/partner/profile",
		strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPartner_Profile_Update_OverwritesExisting(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())

	updated := validPartnerProfile()
	updated["nickname"] = "UpdatedUser"
	putJSON(t, srv, "/api/v1/partner/profile", updated)

	var body map[string]any
	decodeJSON(t, get(t, srv, "/api/v1/partner/profile"), &body)
	if body["nickname"] != "UpdatedUser" {
		t.Errorf("nickname = %v, want UpdatedUser after update", body["nickname"])
	}
}

// =============================================================================
//  Publish
// =============================================================================

func TestPartner_Publish_WithoutProfile_400(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/v1/partner/publish", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("publish without profile: status = %d, want 400", resp.StatusCode)
	}
}

func TestPartner_Publish_Success_ReturnsPublicAd(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())

	pubResp, err := http.Post(srv.URL+"/api/v1/partner/publish", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if pubResp.StatusCode != http.StatusCreated {
		t.Errorf("publish status = %d, want 201", pubResp.StatusCode)
	}

	var ad map[string]any
	decodeJSON(t, pubResp, &ad)

	// Pflichtfelder prüfen
	for _, field := range []string{"peer_id", "gh", "ah", "ih", "pub", "exp"} {
		if ad[field] == nil {
			t.Errorf("PublicAd missing required field %q", field)
		}
	}
}

func TestPartner_Publish_NoCleartextPII(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())
	pubResp, _ := http.Post(srv.URL+"/api/v1/partner/publish", "application/json", nil)

	var ad map[string]any
	decodeJSON(t, pubResp, &ad)
	adJSON, _ := json.Marshal(ad)
	adStr := string(adJSON)

	// Keine Klartextwerte aus dem Profil
	forbidden := []string{"TestUser", "female", "Musik", "Wandern", "Kochen", "lokal sichtbar"}
	for _, f := range forbidden {
		if strings.Contains(adStr, f) {
			t.Errorf("PublicAd contains cleartext PII %q", f)
		}
	}
}

func TestPartner_Publish_CoordinatesRounded(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	profile := validPartnerProfile()
	profile["lat"] = 50.1109
	profile["lon"] = 8.6821
	putJSON(t, srv, "/api/v1/partner/profile", profile)

	pubResp, _ := http.Post(srv.URL+"/api/v1/partner/publish", "application/json", nil)
	var ad map[string]any
	decodeJSON(t, pubResp, &ad)

	latR, _ := ad["lat_r"].(float64)
	lonR, _ := ad["lon_r"].(float64)

	// Muss auf 0.05 Grad gerundet sein
	if latR == 50.1109 {
		t.Error("lat_r should be rounded, not the exact original value")
	}
	// Muss in der Nähe bleiben
	if latR < 50.05 || latR > 50.20 {
		t.Errorf("lat_r = %v out of expected range", latR)
	}
	_ = lonR
}

func TestPartner_Publish_TwiceIsSafe(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())

	for i := 0; i < 2; i++ {
		resp, _ := http.Post(srv.URL+"/api/v1/partner/publish", "application/json", nil)
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("publish #%d status = %d, want 201", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// =============================================================================
//  Ads List
// =============================================================================

func TestPartner_Ads_EmptyInitially(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/partner/ads")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	decodeJSON(t, resp, &body)
	count, _ := body["count"].(float64)
	if int(count) != 0 {
		t.Errorf("count = %.0f, want 0 initially", count)
	}
}

// =============================================================================
//  Matches
// =============================================================================

func TestPartner_Matches_WithoutProfile_400(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/partner/matches")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("matches without profile: status = %d, want 400", resp.StatusCode)
	}
}

func TestPartner_Matches_EmptyWhenNoRemoteAds(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())

	resp := get(t, srv, "/api/v1/partner/matches")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	decodeJSON(t, resp, &body)
	count, _ := body["count"].(float64)
	if int(count) != 0 {
		t.Errorf("count = %.0f, want 0 (no remote ads)", count)
	}
}

func TestPartner_Matches_ResponseShape(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())

	resp := get(t, srv, "/api/v1/partner/matches")
	var body map[string]any
	decodeJSON(t, resp, &body)

	if _, ok := body["matches"]; !ok {
		t.Error("response missing 'matches' key")
	}
	if _, ok := body["count"]; !ok {
		t.Error("response missing 'count' key")
	}
}
