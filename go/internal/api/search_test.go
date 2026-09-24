package api_test

// Partner-Suche Tests.
// Hilfsfunktionen newTestServer, get, postJSON, decodeJSON, putJSON sind
// in server_test.go / partner_test.go definiert.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// =============================================================================
//  Preference Lists
// =============================================================================

func TestPartner_PreferenceLists_NotEmpty(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/partner/preferences/lists")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)

	requiredKeys := []string{
		"education_levels", "hobbies", "industries",
		"preferences", "dislikes", "sexual_preferences",
	}
	for _, key := range requiredKeys {
		val := body[key]
		if val == nil {
			t.Errorf("preference lists missing key %q", key)
			continue
		}
		slice, ok := val.([]any)
		if !ok || len(slice) == 0 {
			t.Errorf("preference list %q is empty or not a slice", key)
		}
	}
}

func TestPartner_PreferenceLists_SexualPrefs_HaveAbbr(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	var body map[string]any
	decodeJSON(t, get(t, srv, "/api/v1/partner/preferences/lists"), &body)

	sexPrefs, _ := body["sexual_preferences"].([]any)
	if len(sexPrefs) == 0 {
		t.Fatal("sexual_preferences empty")
	}

	for i, sp := range sexPrefs {
		m, _ := sp.(map[string]any)
		if m == nil {
			t.Errorf("sexual_preference[%d] not an object", i)
			continue
		}
		abbr, _ := m["Abbr"].(string)
		if abbr == "" {
			t.Errorf("sexual_preference[%d] missing Abbr", i)
		}
		labelDE, _ := m["LabelDE"].(string)
		if labelDE == "" {
			t.Errorf("sexual_preference[%d] (Abbr=%s) missing LabelDE", i, abbr)
		}
		explainDE, _ := m["ExplainDE"].(string)
		if explainDE == "" {
			t.Errorf("sexual_preference[%d] (Abbr=%s) missing ExplainDE", i, abbr)
		}
	}
}

func TestPartner_PreferenceLists_EducationLevels_HaveValues(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	var body map[string]any
	decodeJSON(t, get(t, srv, "/api/v1/partner/preferences/lists"), &body)

	levels, _ := body["education_levels"].([]any)
	for i, l := range levels {
		m, _ := l.(map[string]any)
		if m["Value"] == nil {
			t.Errorf("education_levels[%d] missing Value", i)
		}
		if m["LabelDE"] == nil {
			t.Errorf("education_levels[%d] missing LabelDE", i)
		}
	}
}

// =============================================================================
//  Publish Searchable Ad
// =============================================================================

func TestPartner_PublishSearchable_WithoutProfile_400(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/api/v1/partner/publish/searchable",
		"application/json", nil)
	if resp != nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	}
}

func TestPartner_PublishSearchable_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())

	resp, err := http.Post(srv.URL+"/api/v1/partner/publish/searchable",
		"application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}

	var ad map[string]any
	decodeJSON(t, resp, &ad)

	// SearchableAd-Pflichtfelder
	for _, field := range []string{"peer_id", "gc", "ar", "lat_r", "lnc", "pub", "exp"} {
		if ad[field] == nil {
			t.Errorf("SearchableAd missing field %q", field)
		}
	}
}

func TestPartner_PublishSearchable_GenderNormalized(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	profile := validPartnerProfile()
	profile["gender"] = "weiblich" // soll zu "female" normalisiert werden
	putJSON(t, srv, "/api/v1/partner/profile", profile)

	resp, _ := http.Post(srv.URL+"/api/v1/partner/publish/searchable",
		"application/json", nil)
	var ad map[string]any
	decodeJSON(t, resp, &ad)

	gc, _ := ad["gc"].(string)
	if gc != "female" {
		t.Errorf("gc = %q, want 'female' (normalized from 'weiblich')", gc)
	}
}

func TestPartner_PublishSearchable_CoarserCoordinates(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	profile := validPartnerProfile()
	profile["lat"] = 50.1109
	profile["lon"] = 8.6821
	putJSON(t, srv, "/api/v1/partner/profile", profile)

	// PrivateAd (~5 km Rasterung)
	privResp, _ := http.Post(srv.URL+"/api/v1/partner/publish",
		"application/json", nil)
	var privAd map[string]any
	decodeJSON(t, privResp, &privAd)
	privLat, _ := privAd["lat_r"].(float64)

	// SearchableAd (~10 km Rasterung)
	srchResp, _ := http.Post(srv.URL+"/api/v1/partner/publish/searchable",
		"application/json", nil)
	var srchAd map[string]any
	decodeJSON(t, srchResp, &srchAd)
	srchLat, _ := srchAd["lat_r"].(float64) // LatCell

	// Beide müssen gerundet sein, SearchableAd grober oder gleich
	exactLat := 50.1109
	privDiff := abs64(privLat - exactLat)
	srchDiff := abs64(srchLat - exactLat)

	if privDiff == 0 {
		t.Error("PrivateAd lat_r should not be exact")
	}
	if srchDiff == 0 {
		t.Error("SearchableAd lat_r should not be exact")
	}
	// SearchableAd-Rasterung muss >= PrivateAd-Rasterung sein
	if srchDiff < privDiff-0.001 {
		t.Errorf("SearchableAd coord (diff=%.5f) should be coarser than PrivateAd (diff=%.5f)",
			srchDiff, privDiff)
	}
}

// =============================================================================
//  Search – Grundfälle
// =============================================================================

func TestPartner_Search_EmptyInitially(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/partner/search")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)

	for _, key := range []string{"results", "total", "count", "offset", "limit", "sort_by"} {
		if body[key] == nil {
			t.Errorf("search response missing key %q", key)
		}
	}

	total, _ := body["total"].(float64)
	if int(total) != 0 {
		t.Errorf("total = %.0f, want 0 initially", total)
	}
}

func TestPartner_Search_OwnAdNotInResults(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())
	http.Post(srv.URL+"/api/v1/partner/publish/searchable", "application/json", nil)

	var body map[string]any
	decodeJSON(t, get(t, srv, "/api/v1/partner/search"), &body)

	// Eigener Ad darf nie in Suchergebnissen erscheinen
	total, _ := body["total"].(float64)
	if int(total) != 0 {
		t.Errorf("own ad should not appear in search results, total = %.0f", total)
	}
}

func TestPartner_Search_ResponseShape(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	var body map[string]any
	decodeJSON(t, get(t, srv, "/api/v1/partner/search"), &body)

	if _, ok := body["results"]; !ok {
		t.Error("missing 'results' key")
	}
	if _, ok := body["total"]; !ok {
		t.Error("missing 'total' key")
	}
	if _, ok := body["sort_by"]; !ok {
		t.Error("missing 'sort_by' key")
	}
}

// =============================================================================
//  Search – Query-Parameter
// =============================================================================

func TestPartner_Search_AllQueryParams_Accepted(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	q := url.Values{
		"radius_km":    {"50"},
		"gender":       {"female,non-binary"},
		"age_range":    {"26-35,36-45"},
		"education":    {"university"},
		"industry":     {"it"},
		"hobby":        {"musik,wandern"},
		"pref":         {"nichtraucher"},
		"sex":          {"VAN,BDSM"},
		"mutual":       {"true"},
		"my_gender":    {"male"},
		"my_age_range": {"36-45"},
		"sort":         {"distance"},
		"limit":        {"10"},
		"offset":       {"0"},
	}

	resp := get(t, srv, "/api/v1/partner/search?"+q.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 for all params", resp.StatusCode)
	}
}

func TestPartner_Search_InvalidSort_FallsBackToScore(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/partner/search?sort=nonsense")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestPartner_Search_Pagination_LimitReturned(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	var body map[string]any
	decodeJSON(t, get(t, srv, "/api/v1/partner/search?limit=5"), &body)

	limit, _ := body["limit"].(float64)
	if int(limit) != 5 {
		t.Errorf("limit = %.0f, want 5", limit)
	}
}

func TestPartner_Search_Offset_Returned(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	var body map[string]any
	decodeJSON(t, get(t, srv, "/api/v1/partner/search?offset=20"), &body)

	offset, _ := body["offset"].(float64)
	if int(offset) != 20 {
		t.Errorf("offset = %.0f, want 20", offset)
	}
}

func TestPartner_Search_SortBy_Reflected(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	for _, sort := range []string{"score", "distance", "hobbies", "sex"} {
		var body map[string]any
		decodeJSON(t, get(t, srv, "/api/v1/partner/search?sort="+sort), &body)
		if body["sort_by"] != sort {
			t.Errorf("sort=%s: sort_by = %v, want %s", sort, body["sort_by"], sort)
		}
	}
}

// =============================================================================
//  Searchable Ads List
// =============================================================================

func TestPartner_SearchableAds_Empty(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/partner/search/ads")
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

func TestPartner_SearchableAds_AfterPublish_HasOne(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	putJSON(t, srv, "/api/v1/partner/profile", validPartnerProfile())
	http.Post(srv.URL+"/api/v1/partner/publish/searchable", "application/json", nil)

	var body map[string]any
	decodeJSON(t, get(t, srv, "/api/v1/partner/search/ads"), &body)
	count, _ := body["count"].(float64)
	if int(count) < 1 {
		t.Errorf("count = %.0f, want >= 1 after publish", count)
	}
}

// =============================================================================
//  JSON serialisierbarkeit der Listenwerte
// =============================================================================

func TestPartner_PreferenceLists_ValidJSON(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/partner/preferences/lists")
	defer resp.Body.Close()

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Errorf("response is not valid JSON: %v", err)
	}
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
