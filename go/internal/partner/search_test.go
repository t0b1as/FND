package partner_test

import (
	"math"
	"testing"
	"time"

	"github.com/fundus/node/internal/partner"
)

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func makeAd(peerID, gender, age, edu, industry string,
	lat, lon float64,
	hobbies, prefs, sexAbbrs []string,
	seekGender []string, seekRadius float64,
) *partner.SearchableAd {
	now := time.Now()
	return &partner.SearchableAd{
		PeerID:          peerID,
		GenderCategory:  gender,
		AgeRange:        age,
		EducationGroup:  edu,
		IndustryGroup:   industry,
		LatCell:         lat,
		LonCell:         lon,
		HobbyTags:       hobbies,
		PrefTags:        prefs,
		SexPrefAbbrs:    sexAbbrs,
		SeekingGender:   seekGender,
		SeekingAgeRanges: []string{},
		SeekingRadiusKm: seekRadius,
		PublishedAt:     now,
		ExpiresAt:       now.Add(24 * time.Hour),
	}
}

func baseFilter() partner.SearchFilter {
	return partner.SearchFilter{
		Lat:      50.11,
		Lon:      8.68,
		RadiusKm: 100,
		SortBy:   partner.SortByScore,
		Limit:    50,
	}
}

func makeSearcher() *partner.Searcher {
	return partner.NewSearcher()
}

// =============================================================================
//  NewSearcher
// =============================================================================

func TestNewSearcher_NotNil(t *testing.T) {
	s := partner.NewSearcher()
	if s == nil {
		t.Error("NewSearcher() returned nil")
	}
}

// =============================================================================
//  SearchFilter.Validate
// =============================================================================

func TestSearchFilter_Validate_Defaults(t *testing.T) {
	f := partner.SearchFilter{}
	f.Validate()

	if f.SortBy != partner.SortByScore {
		t.Errorf("SortBy = %q, want %q", f.SortBy, partner.SortByScore)
	}
	if f.Limit != 50 {
		t.Errorf("Limit = %d, want 50", f.Limit)
	}
}

func TestSearchFilter_Validate_CapsLimit(t *testing.T) {
	f := partner.SearchFilter{Limit: 9999}
	f.Validate()
	if f.Limit > 100 {
		t.Errorf("Limit = %d, should be capped at 100", f.Limit)
	}
}

func TestSearchFilter_Validate_ZeroRadius_NoLimit(t *testing.T) {
	f := partner.SearchFilter{RadiusKm: 0}
	f.Validate()
	if f.RadiusKm <= 0 {
		t.Error("RadiusKm 0 should be replaced with unlimited value")
	}
}

// =============================================================================
//  Searcher.Search – Grundfälle
// =============================================================================

func TestSearch_EmptyAds_ReturnsEmpty(t *testing.T) {
	s := makeSearcher()
	results, total := s.Search(nil, baseFilter())
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
}

func TestSearch_AllPassfilter_ReturnsAll(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("p1", "female", "26-35", "university", "it",    50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("p2", "male",   "36-45", "vocational", "trade", 50.2, 8.8, nil, nil, nil, nil, 0),
	}
	f := baseFilter()
	// Kein Genderfilter, kein sonstiger Filter → alle durch
	results, total := s.Search(ads, f)
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if len(results) != 2 {
		t.Errorf("len(results) = %d, want 2", len(results))
	}
}

func TestSearch_ExpiredAd_Excluded(t *testing.T) {
	s := makeSearcher()
	expired := makeAd("p-exp", "female", "26-35", "school", "it", 50.1, 8.7, nil, nil, nil, nil, 0)
	expired.ExpiresAt = time.Now().Add(-1 * time.Hour)

	results, total := s.Search([]*partner.SearchableAd{expired}, baseFilter())
	if total != 0 {
		t.Errorf("expired ad should be excluded, total = %d", total)
	}
	_ = results
}

// =============================================================================
//  Distanzfilter
// =============================================================================

func TestSearch_DistanceFilter_ExcludesFarAway(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("near",  "female", "26-35", "other", "it", 50.15, 8.70, nil, nil, nil, nil, 0), // ~5 km
		makeAd("far",   "female", "26-35", "other", "it", 52.52, 13.40, nil, nil, nil, nil, 0), // ~500 km (Berlin)
	}
	f := baseFilter()
	f.RadiusKm = 50
	results, _ := s.Search(ads, f)

	for _, r := range results {
		if r.PeerID == "far" {
			t.Error("far peer should be excluded by radius filter")
		}
	}
	found := false
	for _, r := range results {
		if r.PeerID == "near" {
			found = true
		}
	}
	if !found {
		t.Error("near peer should be included")
	}
}

func TestSearch_NoRadius_IncludesAll(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("p1", "female", "26-35", "other", "it", 10.0,  20.0,  nil, nil, nil, nil, 0),
		makeAd("p2", "female", "26-35", "other", "it", -33.87, 151.21, nil, nil, nil, nil, 0), // Sydney
	}
	f := baseFilter()
	f.RadiusKm = 0 // kein Limit

	_, total := s.Search(ads, f)
	if total != 2 {
		t.Errorf("total = %d, want 2 (no radius limit)", total)
	}
}

func TestSearch_Result_DistanceKm_IsRounded(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("p1", "female", "26-35", "other", "it", 50.20, 8.68, nil, nil, nil, nil, 0),
	}
	results, _ := s.Search(ads, baseFilter())
	if len(results) == 0 {
		t.Fatal("expected result")
	}
	// Nachkommastellen prüfen (max 2)
	dist := results[0].DistanceKm
	if dist != math.Round(dist*100)/100 {
		t.Errorf("DistanceKm = %v not rounded to 2 decimal places", dist)
	}
}

// =============================================================================
//  Kategorienfilter
// =============================================================================

func TestSearch_GenderFilter(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("female-peer",    "female",     "26-35", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("male-peer",      "male",       "26-35", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("nonbinary-peer", "non-binary", "26-35", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
	}

	f := baseFilter()
	f.Genders = []string{"female"}
	results, _ := s.Search(ads, f)

	if len(results) != 1 || results[0].PeerID != "female-peer" {
		t.Errorf("expected only female-peer, got %v", peerIDs(results))
	}
}

func TestSearch_GenderFilter_Multiple(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("f", "female",     "26-35", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("m", "male",       "26-35", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("n", "non-binary", "26-35", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.Genders = []string{"female", "non-binary"}
	results, _ := s.Search(ads, f)

	if len(results) != 2 {
		t.Errorf("expected 2 results (female+nonbinary), got %d", len(results))
	}
}

func TestSearch_AgeRangeFilter(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("young", "female", "18-25", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("mid",   "female", "26-35", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("older", "female", "46-55", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.AgeRanges = []string{"26-35", "46-55"}
	results, _ := s.Search(ads, f)

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d: %v", len(results), peerIDs(results))
	}
}

func TestSearch_EducationFilter(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("uni", "female", "26-35", "university", "it",        50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("voc", "female", "26-35", "vocational",  "trade",    50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("sch", "female", "26-35", "school",      "gastro",   50.1, 8.7, nil, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.EducationGroups = []string{"university"}
	results, _ := s.Search(ads, f)
	if len(results) != 1 || results[0].PeerID != "uni" {
		t.Errorf("expected only uni, got %v", peerIDs(results))
	}
}

func TestSearch_IndustryFilter(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("it-peer",     "female", "26-35", "university", "it",      50.1, 8.7, nil, nil, nil, nil, 0),
		makeAd("health-peer", "female", "26-35", "university", "health",  50.1, 8.7, nil, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.IndustryGroups = []string{"it"}
	results, _ := s.Search(ads, f)
	if len(results) != 1 || results[0].PeerID != "it-peer" {
		t.Errorf("expected only it-peer, got %v", peerIDs(results))
	}
}

// =============================================================================
//  Interessen-Filter
// =============================================================================

func TestSearch_HobbyFilter_RequiresAtLeastOne(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("match",   "female", "26-35", "other", "it", 50.1, 8.7,
			[]string{"musik", "wandern"}, nil, nil, nil, 0),
		makeAd("nomatch", "female", "26-35", "other", "it", 50.1, 8.7,
			[]string{"gaming", "kochen"}, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.Hobbies = []string{"musik", "fotografie"}
	results, _ := s.Search(ads, f)

	if len(results) != 1 || results[0].PeerID != "match" {
		t.Errorf("expected only match, got %v", peerIDs(results))
	}
}

func TestSearch_HobbyFilter_MatchedHobbies_InResult(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("p1", "female", "26-35", "other", "it", 50.1, 8.7,
			[]string{"musik", "wandern", "kochen"}, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.Hobbies = []string{"musik", "kochen", "gaming"}
	results, _ := s.Search(ads, f)

	if len(results) == 0 {
		t.Fatal("expected result")
	}
	// Nur "musik" und "kochen" matchen (nicht "gaming")
	if len(results[0].MatchedHobbies) != 2 {
		t.Errorf("MatchedHobbies = %v, want [musik kochen]", results[0].MatchedHobbies)
	}
}

func TestSearch_SexPrefFilter(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("bdsm-user",  "female", "26-35", "other", "it", 50.1, 8.7,
			nil, nil, []string{"BDSM", "DOM"}, nil, 0),
		makeAd("vanilla-user", "female", "26-35", "other", "it", 50.1, 8.7,
			nil, nil, []string{"VAN"}, nil, 0),
	}
	f := baseFilter()
	f.SexPrefAbbrs = []string{"BDSM"}
	results, _ := s.Search(ads, f)

	if len(results) != 1 || results[0].PeerID != "bdsm-user" {
		t.Errorf("expected only bdsm-user, got %v", peerIDs(results))
	}
	if len(results[0].MatchedSex) == 0 {
		t.Error("MatchedSex should contain BDSM")
	}
}

func TestSearch_NoFilters_ReturnsAll(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("p1", "female", "26-35", "other", "it",    50.1, 8.7, []string{"musik"}, nil, nil, nil, 0),
		makeAd("p2", "male",   "46-55", "school", "trade", 50.1, 8.7, []string{"sport"}, nil, nil, nil, 0),
	}
	f := baseFilter()
	// Kein Hobbyfilter gesetzt
	results, total := s.Search(ads, f)
	if total != 2 {
		t.Errorf("no hobby filter: total = %d, want 2", total)
	}
	_ = results
}

// =============================================================================
//  Gegenseitigkeitsprüfung
// =============================================================================

func TestSearch_RequireMutual_FiltersOutNonMutual(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("seeks-male", "female", "26-35", "other", "it", 50.1, 8.7,
			nil, nil, nil, []string{"male"}, 100),
		makeAd("seeks-female", "female", "26-35", "other", "it", 50.1, 8.7,
			nil, nil, nil, []string{"female"}, 100),
	}
	f := baseFilter()
	f.RequireMutual = true
	f.MyGender = "male"

	results, _ := s.Search(ads, f)

	// Nur der Ad der "male" sucht soll erscheinen
	for _, r := range results {
		if r.PeerID == "seeks-female" {
			t.Error("seeks-female should be excluded (requires mutual, I'm male)")
		}
	}
}

func TestSearch_RequireMutual_False_DoesNotFilter(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("p1", "female", "26-35", "other", "it", 50.1, 8.7,
			nil, nil, nil, []string{"female"}, 100),
	}
	f := baseFilter()
	f.RequireMutual = false
	f.MyGender = "male"

	_, total := s.Search(ads, f)
	if total != 1 {
		t.Errorf("require_mutual=false: total = %d, want 1", total)
	}
}

// =============================================================================
//  Sortierung
// =============================================================================

func TestSearch_SortByDistance(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("far",  "female", "26-35", "other", "it", 50.8, 8.7, nil, nil, nil, nil, 0),  // ~77 km
		makeAd("near", "female", "26-35", "other", "it", 50.15, 8.7, nil, nil, nil, nil, 0), // ~4 km
	}
	f := baseFilter()
	f.SortBy = partner.SortByDistance
	results, _ := s.Search(ads, f)

	if len(results) < 2 {
		t.Fatal("expected 2 results")
	}
	if results[0].DistanceKm > results[1].DistanceKm {
		t.Errorf("not sorted by distance: [0]=%.1f > [1]=%.1f",
			results[0].DistanceKm, results[1].DistanceKm)
	}
}

func TestSearch_SortByScore_Default(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("faraway",  "female", "26-35", "other", "it", 50.8, 8.7, nil, nil, nil, nil, 0),
		makeAd("nearby",   "female", "26-35", "other", "it", 50.12, 8.68, nil, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.SortBy = partner.SortByScore
	results, _ := s.Search(ads, f)

	if len(results) >= 2 {
		if results[0].Score < results[1].Score {
			t.Errorf("not sorted by score descending: [0]=%.3f < [1]=%.3f",
				results[0].Score, results[1].Score)
		}
	}
}

func TestSearch_SortByHobbies(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("few",   "female", "26-35", "other", "it", 50.1, 8.7, []string{"musik"}, nil, nil, nil, 0),
		makeAd("many",  "female", "26-35", "other", "it", 50.1, 8.7, []string{"musik","wandern","kochen"}, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.Hobbies = []string{"musik", "wandern", "kochen", "gaming"}
	f.SortBy = partner.SortByHobbies
	results, _ := s.Search(ads, f)

	if len(results) >= 2 {
		if len(results[0].MatchedHobbies) < len(results[1].MatchedHobbies) {
			t.Error("not sorted by matched hobbies descending")
		}
	}
}

// =============================================================================
//  Paginierung
// =============================================================================

func TestSearch_Pagination_Limit(t *testing.T) {
	s := makeSearcher()
	ads := make([]*partner.SearchableAd, 10)
	for i := range ads {
		ads[i] = makeAd(
			"p"+string(rune('a'+i)),
			"female", "26-35", "other", "it",
			50.1, 8.7, nil, nil, nil, nil, 0)
	}
	f := baseFilter()
	f.Limit = 3
	results, total := s.Search(ads, f)

	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
	if len(results) != 3 {
		t.Errorf("len(results) = %d, want 3 (limit)", len(results))
	}
}

func TestSearch_Pagination_Offset(t *testing.T) {
	s := makeSearcher()
	ads := make([]*partner.SearchableAd, 5)
	for i := range ads {
		ads[i] = makeAd(
			"p"+string(rune('0'+i)),
			"female", "26-35", "other", "it",
			50.1+float64(i)*0.001, 8.7, nil, nil, nil, nil, 0)
	}
	f := baseFilter()
	f.Limit  = 2
	f.Offset = 3
	f.SortBy = partner.SortByDistance // stabile Sortierung für Offset-Test

	results, total := s.Search(ads, f)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(results) != 2 {
		t.Errorf("len(results) = %d, want 2 (5 - offset 3)", len(results))
	}
}

func TestSearch_Pagination_OffsetBeyondTotal(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("p1", "female", "26-35", "other", "it", 50.1, 8.7, nil, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.Offset = 100
	results, total := s.Search(ads, f)

	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0 (offset beyond total)", len(results))
	}
}

// =============================================================================
//  Score-Eigenschaften
// =============================================================================

func TestSearch_Score_InRange(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("p1", "female", "26-35", "university", "it", 50.1, 8.7,
			[]string{"musik","wandern"}, []string{"nichtraucher"}, []string{"VAN"}, nil, 0),
	}
	f := baseFilter()
	f.Hobbies     = []string{"musik","gaming"}
	f.SexPrefAbbrs = []string{"VAN","BDSM"}

	results, _ := s.Search(ads, f)
	if len(results) == 0 {
		t.Fatal("expected result")
	}
	sc := results[0].Score
	if sc < 0 || sc > 1 {
		t.Errorf("score = %.4f, must be in [0.0, 1.0]", sc)
	}
}

func TestSearch_CloserPeer_HigherDistanceScore(t *testing.T) {
	s := makeSearcher()
	ads := []*partner.SearchableAd{
		makeAd("near", "female", "26-35", "other", "it", 50.12, 8.68, nil, nil, nil, nil, 0),
		makeAd("far",  "female", "26-35", "other", "it", 50.90, 8.68, nil, nil, nil, nil, 0),
	}
	f := baseFilter()
	f.SortBy = partner.SortByScore
	results, _ := s.Search(ads, f)

	if len(results) < 2 {
		t.Fatal("expected 2 results")
	}
	if results[0].PeerID != "near" {
		t.Errorf("closer peer should score higher: got %s first", results[0].PeerID)
	}
}

// =============================================================================
//  MakeSearchableAd
// =============================================================================

func TestMakeSearchableAd_GenderNormalized(t *testing.T) {
	p := baseProfile()
	p.Gender = "weiblich"
	ad := partner.MakeSearchableAd(p, "peer-1")
	if ad.GenderCategory != "female" {
		t.Errorf("GenderCategory = %q, want 'female'", ad.GenderCategory)
	}
}

func TestMakeSearchableAd_EducationGrouped(t *testing.T) {
	tests := []struct{ edu, wantGroup string }{
		{"bachelor",      "university"},
		{"master",        "university"},
		{"doktor",        "university"},
		{"berufsausbildung", "vocational"},
		{"abitur",        "school"},
		{"selbststudium", "self-taught"},
		{"",              "other"},
	}
	for _, tc := range tests {
		p := baseProfile()
		p.Education = tc.edu
		ad := partner.MakeSearchableAd(p, "peer-1")
		if ad.EducationGroup != tc.wantGroup {
			t.Errorf("edu=%q → EducationGroup=%q, want %q", tc.edu, ad.EducationGroup, tc.wantGroup)
		}
	}
}

func TestMakeSearchableAd_HobbiesCappedAt5(t *testing.T) {
	p := baseProfile()
	p.Hobbies = []string{"a","b","c","d","e","f","g"}
	ad := partner.MakeSearchableAd(p, "peer-1")
	if len(ad.HobbyTags) > 5 {
		t.Errorf("HobbyTags len = %d, want <= 5", len(ad.HobbyTags))
	}
}

func TestMakeSearchableAd_CoordinatesRoundedTo10km(t *testing.T) {
	p := baseProfile()
	p.Lat = 50.1109
	p.Lon = 8.6821
	ad := partner.MakeSearchableAd(p, "peer-1")

	// ~10 km Rasterung (0.1 Grad)
	if ad.LatCell == p.Lat {
		t.Error("LatCell should be rounded (10 km grid), not exact")
	}
	diff := ad.LatCell - p.Lat
	if diff > 0.1 || diff < -0.1 {
		t.Errorf("LatCell diff = %.4f, should be <= 0.1 degree", diff)
	}
}

func TestMakeSearchableAd_SexAbbrsExtracted(t *testing.T) {
	p := baseProfile()
	p.SexualPrefs = []partner.SexualPreference{
		{Abbr: "BDSM", Role: partner.RoleActive},
		{Abbr: "VAN",  Role: partner.RoleSwitch},
	}
	ad := partner.MakeSearchableAd(p, "peer-1")
	if len(ad.SexPrefAbbrs) != 2 {
		t.Errorf("SexPrefAbbrs len = %d, want 2", len(ad.SexPrefAbbrs))
	}
}

func TestMakeSearchableAd_ExpiresIn24h(t *testing.T) {
	p := baseProfile()
	ad := partner.MakeSearchableAd(p, "peer-1")
	dur := ad.ExpiresAt.Sub(ad.PublishedAt)
	if dur < 23*time.Hour || dur > 25*time.Hour {
		t.Errorf("ExpiresAt - PublishedAt = %v, want ~24h", dur)
	}
}

// =============================================================================
//  Hilfsfunktion
// =============================================================================

func peerIDs(results []partner.SearchResult) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.PeerID
	}
	return ids
}
