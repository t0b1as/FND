package partner_test

import (
	"strings"
	"testing"

	"github.com/fundus/node/internal/partner"
)

// =============================================================================
//  DefaultSexualPreferences – Vollständigkeit und Korrektheit
// =============================================================================

func TestSexPrefs_AllHaveRequiredFields(t *testing.T) {
	for i, sp := range partner.DefaultSexualPreferences {
		if sp.Abbr == "" {
			t.Errorf("[%d] missing Abbr", i)
		}
		if sp.LabelDE == "" {
			t.Errorf("[%d] (Abbr=%s) missing LabelDE", i, sp.Abbr)
		}
		if sp.LabelEN == "" {
			t.Errorf("[%d] (Abbr=%s) missing LabelEN", i, sp.Abbr)
		}
		if sp.ExplainDE == "" {
			t.Errorf("[%d] (Abbr=%s) missing ExplainDE", i, sp.Abbr)
		}
		if sp.ExplainEN == "" {
			t.Errorf("[%d] (Abbr=%s) missing ExplainEN", i, sp.Abbr)
		}
	}
}

func TestSexPrefs_NoDuplicateAbbr(t *testing.T) {
	seen := make(map[string]bool)
	for _, sp := range partner.DefaultSexualPreferences {
		if seen[sp.Abbr] {
			t.Errorf("duplicate Abbr: %q", sp.Abbr)
		}
		seen[sp.Abbr] = true
	}
}

// =============================================================================
//  FF-Varianten
// =============================================================================

func TestSexPrefs_FF_HasFourVariants(t *testing.T) {
	required := []string{"FFva", "FFvp", "FFaa", "FFap"}
	abbrs := partner.AllSexPrefAbbrs()
	abbrSet := make(map[string]bool, len(abbrs))
	for _, a := range abbrs {
		abbrSet[a] = true
	}
	for _, want := range required {
		if !abbrSet[want] {
			t.Errorf("missing FF variant %q in DefaultSexualPreferences", want)
		}
	}
}

func TestSexPrefs_FF_NoGenericEntry(t *testing.T) {
	// Der generische "FF"-Eintrag ohne Suffix darf nicht mehr existieren
	for _, sp := range partner.DefaultSexualPreferences {
		if sp.Abbr == "FF" {
			t.Error("generic 'FF' entry should be removed (replaced by FFva/FFvp/FFaa/FFap)")
		}
	}
}

func TestSexPrefs_FFva_IsVaginalActive(t *testing.T) {
	sp := partner.SexPrefByAbbr("FFva")
	if sp == nil {
		t.Fatal("FFva not found")
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "vaginal") {
		t.Errorf("FFva LabelDE should mention vaginal, got: %q", sp.LabelDE)
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "aktiv") {
		t.Errorf("FFva LabelDE should mention aktiv, got: %q", sp.LabelDE)
	}
}

func TestSexPrefs_FFvp_IsVaginalPassive(t *testing.T) {
	sp := partner.SexPrefByAbbr("FFvp")
	if sp == nil {
		t.Fatal("FFvp not found")
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "vaginal") {
		t.Errorf("FFvp LabelDE should mention vaginal, got: %q", sp.LabelDE)
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "passiv") {
		t.Errorf("FFvp LabelDE should mention passiv, got: %q", sp.LabelDE)
	}
}

func TestSexPrefs_FFaa_IsAnalActive(t *testing.T) {
	sp := partner.SexPrefByAbbr("FFaa")
	if sp == nil {
		t.Fatal("FFaa not found")
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "anal") {
		t.Errorf("FFaa LabelDE should mention anal, got: %q", sp.LabelDE)
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "aktiv") {
		t.Errorf("FFaa LabelDE should mention aktiv, got: %q", sp.LabelDE)
	}
}

func TestSexPrefs_FFap_IsAnalPassive(t *testing.T) {
	sp := partner.SexPrefByAbbr("FFap")
	if sp == nil {
		t.Fatal("FFap not found")
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "anal") {
		t.Errorf("FFap LabelDE should mention anal, got: %q", sp.LabelDE)
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "passiv") {
		t.Errorf("FFap LabelDE should mention passiv, got: %q", sp.LabelDE)
	}
}

func TestSexPrefs_FF_Variants_HasRoles_False(t *testing.T) {
	// Rolle ist bereits im Kürzel kodiert – kein zusätzliches Rollenfeld nötig
	for _, abbr := range []string{"FFva", "FFvp", "FFaa", "FFap"} {
		sp := partner.SexPrefByAbbr(abbr)
		if sp == nil {
			t.Fatalf("%s not found", abbr)
		}
		if sp.HasRoles {
			t.Errorf("%s: HasRoles should be false (role is encoded in the abbr)", abbr)
		}
	}
}

// =============================================================================
//  Z-Varianten (Zunge/Oral)
// =============================================================================

func TestSexPrefs_Z_HasFourVariants(t *testing.T) {
	required := []string{"Zva", "Zvp", "Zaa", "Zap"}
	abbrs := partner.AllSexPrefAbbrs()
	abbrSet := make(map[string]bool, len(abbrs))
	for _, a := range abbrs {
		abbrSet[a] = true
	}
	for _, want := range required {
		if !abbrSet[want] {
			t.Errorf("missing Z variant %q in DefaultSexualPreferences", want)
		}
	}
}

func TestSexPrefs_Zva_IsCunnilingusActive(t *testing.T) {
	sp := partner.SexPrefByAbbr("Zva")
	if sp == nil {
		t.Fatal("Zva not found")
	}
	lde := strings.ToLower(sp.ExplainDE)
	if !strings.Contains(lde, "vagina") && !strings.Contains(lde, "vulva") &&
		!strings.Contains(strings.ToLower(sp.LabelDE), "cunnilingus") {
		t.Errorf("Zva should describe vaginal oral stimulation, LabelDE=%q ExplainDE=%q",
			sp.LabelDE, sp.ExplainDE)
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "aktiv") {
		t.Errorf("Zva LabelDE should mention aktiv, got: %q", sp.LabelDE)
	}
}

func TestSexPrefs_Zvp_IsCunnilingusPassive(t *testing.T) {
	sp := partner.SexPrefByAbbr("Zvp")
	if sp == nil {
		t.Fatal("Zvp not found")
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "passiv") {
		t.Errorf("Zvp LabelDE should mention passiv, got: %q", sp.LabelDE)
	}
}

func TestSexPrefs_Zaa_IsAnalingusActive(t *testing.T) {
	sp := partner.SexPrefByAbbr("Zaa")
	if sp == nil {
		t.Fatal("Zaa not found")
	}
	lde := strings.ToLower(sp.ExplainDE + " " + sp.LabelDE)
	if !strings.Contains(lde, "anal") {
		t.Errorf("Zaa should describe anal oral stimulation, got: %q", sp.LabelDE)
	}
	if !strings.Contains(lde, "aktiv") {
		t.Errorf("Zaa should mention aktiv, got: %q", sp.LabelDE)
	}
}

func TestSexPrefs_Zap_IsAnalingusPassive(t *testing.T) {
	sp := partner.SexPrefByAbbr("Zap")
	if sp == nil {
		t.Fatal("Zap not found")
	}
	if !strings.Contains(strings.ToLower(sp.LabelDE), "passiv") {
		t.Errorf("Zap LabelDE should mention passiv, got: %q", sp.LabelDE)
	}
}

func TestSexPrefs_Z_Variants_HasRoles_False(t *testing.T) {
	for _, abbr := range []string{"Zva", "Zvp", "Zaa", "Zap"} {
		sp := partner.SexPrefByAbbr(abbr)
		if sp == nil {
			t.Fatalf("%s not found", abbr)
		}
		if sp.HasRoles {
			t.Errorf("%s: HasRoles should be false (role encoded in abbr)", abbr)
		}
	}
}

func TestSexPrefs_Z_And_FF_BothInAllAbbrs(t *testing.T) {
	all := partner.AllSexPrefAbbrs()
	allSet := make(map[string]bool, len(all))
	for _, a := range all {
		allSet[a] = true
	}

	expected := []string{"FFva", "FFvp", "FFaa", "FFap", "Zva", "Zvp", "Zaa", "Zap"}
	for _, abbr := range expected {
		if !allSet[abbr] {
			t.Errorf("AllSexPrefAbbrs() missing %q", abbr)
		}
	}
}

// =============================================================================
//  IsKnownSexPref
// =============================================================================

func TestIsKnownSexPref_KnownEntries(t *testing.T) {
	for _, abbr := range []string{"VAN", "ORL", "BDSM", "FFva", "FFvp", "FFaa", "FFap",
		"Zva", "Zvp", "Zaa", "Zap", "AN", "WS"} {
		if !partner.IsKnownSexPref(abbr) {
			t.Errorf("IsKnownSexPref(%q) = false, want true", abbr)
		}
	}
}

func TestIsKnownSexPref_UnknownEntries(t *testing.T) {
	for _, abbr := range []string{"FF", "ZV", "UNKNOWN", "", "xxx"} {
		if partner.IsKnownSexPref(abbr) {
			t.Errorf("IsKnownSexPref(%q) = true, want false", abbr)
		}
	}
}

func TestSexPrefByAbbr_ReturnsNilForUnknown(t *testing.T) {
	if partner.SexPrefByAbbr("FF") != nil {
		t.Error("SexPrefByAbbr('FF') should return nil (replaced by variants)")
	}
	if partner.SexPrefByAbbr("") != nil {
		t.Error("SexPrefByAbbr('') should return nil")
	}
}

// =============================================================================
//  Suchbarkeit der neuen Abkürzungen
// =============================================================================

func TestSearch_FFVariants_AreSearchable(t *testing.T) {
	s := partner.NewSearcher()

	ad := makeAd("p1", "female", "26-35", "other", "it", 50.1, 8.7,
		nil, nil, []string{"FFva", "FFaa"}, nil, 0)

	f := baseFilter()
	f.SexPrefAbbrs = []string{"FFva"}
	results, total := s.Search([]*partner.SearchableAd{ad}, f)
	if total != 1 {
		t.Errorf("FFva filter: total = %d, want 1", total)
	}
	if len(results) > 0 && len(results[0].MatchedSex) == 0 {
		t.Error("FFva should appear in MatchedSex")
	}
}

func TestSearch_ZVariants_AreSearchable(t *testing.T) {
	s := partner.NewSearcher()

	ad := makeAd("p1", "female", "26-35", "other", "it", 50.1, 8.7,
		nil, nil, []string{"Zva", "Zap"}, nil, 0)

	f := baseFilter()
	f.SexPrefAbbrs = []string{"Zva"}
	results, total := s.Search([]*partner.SearchableAd{ad}, f)
	if total != 1 {
		t.Errorf("Zva filter: total = %d, want 1", total)
	}
	_ = results
}

func TestSearch_FFVariant_NoMatchOnDifferentVariant(t *testing.T) {
	s := partner.NewSearcher()

	// Ad hat FFva (vaginal aktiv) – Suche nach FFap (anal passiv) → kein Match
	ad := makeAd("p1", "female", "26-35", "other", "it", 50.1, 8.7,
		nil, nil, []string{"FFva"}, nil, 0)

	f := baseFilter()
	f.SexPrefAbbrs = []string{"FFap"}
	_, total := s.Search([]*partner.SearchableAd{ad}, f)
	if total != 0 {
		t.Errorf("FFva ad should not match FFap filter, total = %d", total)
	}
}

func TestSearch_ZA_NoMatchOnZv(t *testing.T) {
	s := partner.NewSearcher()

	ad := makeAd("p1", "female", "26-35", "other", "it", 50.1, 8.7,
		nil, nil, []string{"Zaa", "Zap"}, nil, 0)

	f := baseFilter()
	f.SexPrefAbbrs = []string{"Zva"}
	_, total := s.Search([]*partner.SearchableAd{ad}, f)
	if total != 0 {
		t.Errorf("ZA ad should not match Zv filter, total = %d", total)
	}
}
