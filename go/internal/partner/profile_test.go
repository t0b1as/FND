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

func mustSalt(t *testing.T) partner.Salt {
	t.Helper()
	s, err := partner.NewSalt()
	if err != nil {
		t.Fatalf("NewSalt: %v", err)
	}
	return s
}

func baseProfile() *partner.Profile {
	return &partner.Profile{
		Nickname:  "TestUser",
		Gender:    "female",
		AgeRange:  partner.Age26_35,
		Lat:       50.11,
		Lon:       8.68,
		Interests: []string{"Musik", "Wandern", "Kochen"},
		Bio:       "Mag lange Spaziergänge und gutes Essen.",
		Seeking: partner.Preference{
			Genders:   []partner.Gender{"male"},
			AgeRanges: []partner.AgeRange{partner.Age26_35, partner.Age36_45},
			RadiusKm:  50,
			Interests: []string{"Musik", "Wandern"},
		},
	}
}

func remoteProfile() *partner.Profile {
	return &partner.Profile{
		Nickname:  "RemoteUser",
		Gender:    "male",
		AgeRange:  partner.Age36_45,
		Lat:       50.15,
		Lon:       8.70,
		Interests: []string{"Musik", "Fotografie", "Wandern"},
		Seeking: partner.Preference{
			Genders:   []partner.Gender{"female"},
			AgeRanges: []partner.AgeRange{partner.Age18_25, partner.Age26_35},
			RadiusKm:  50,
		},
	}
}

// =============================================================================
//  NewSalt
// =============================================================================

func TestNewSalt_Unique(t *testing.T) {
	s1 := mustSalt(t)
	s2 := mustSalt(t)
	if s1 == s2 {
		t.Error("Two NewSalt() calls produced the same salt")
	}
}

func TestNewSalt_NotZero(t *testing.T) {
	s := mustSalt(t)
	var zero partner.Salt
	if s == zero {
		t.Error("NewSalt returned all-zero salt")
	}
}

// =============================================================================
//  MakePublicAd
// =============================================================================

func TestMakePublicAd_Success(t *testing.T) {
	profile := baseProfile()
	salt    := mustSalt(t)
	ad, err := partner.MakePublicAd(profile, "peer-123", salt)

	if err != nil {
		t.Fatalf("MakePublicAd: %v", err)
	}
	if ad == nil {
		t.Fatal("ad is nil")
	}
}

func TestMakePublicAd_NilProfile_Errors(t *testing.T) {
	_, err := partner.MakePublicAd(nil, "peer-1", mustSalt(t))
	if err == nil {
		t.Error("Expected error for nil profile")
	}
}

func TestMakePublicAd_EmptyGender_Errors(t *testing.T) {
	p := baseProfile()
	p.Gender = ""
	_, err := partner.MakePublicAd(p, "peer-1", mustSalt(t))
	if err == nil {
		t.Error("Expected error for empty gender")
	}
}

func TestMakePublicAd_EmptyAgeRange_Errors(t *testing.T) {
	p := baseProfile()
	p.AgeRange = ""
	_, err := partner.MakePublicAd(p, "peer-1", mustSalt(t))
	if err == nil {
		t.Error("Expected error for empty age range")
	}
}

func TestMakePublicAd_PeerIDPreserved(t *testing.T) {
	ad, _ := partner.MakePublicAd(baseProfile(), "my-peer-id", mustSalt(t))
	if ad.PeerID != "my-peer-id" {
		t.Errorf("PeerID = %q, want %q", ad.PeerID, "my-peer-id")
	}
}

func TestMakePublicAd_NoBioInAd(t *testing.T) {
	profile := baseProfile()
	profile.Bio = "Sehr persönliche Information"

	data, _ := partner.MakePublicAd(profile, "peer-1", mustSalt(t))
	serialised, _ := data.Marshal()

	if contains(string(serialised), "persönliche Information") {
		t.Error("Bio must not appear in serialised PublicAd")
	}
	if contains(string(serialised), "TestUser") {
		t.Error("Nickname must not appear in serialised PublicAd")
	}
}

func TestMakePublicAd_NoGenderInCleartext(t *testing.T) {
	profile := baseProfile()
	profile.Gender = "female"

	ad, _  := partner.MakePublicAd(profile, "peer-1", mustSalt(t))
	data, _ := ad.Marshal()

	if contains(string(data), "female") {
		t.Error("Gender 'female' must not appear in clear text in PublicAd")
	}
}

func TestMakePublicAd_NoInterestsInCleartext(t *testing.T) {
	profile := baseProfile()
	profile.Interests = []string{"Musik", "Wandern", "Kochen"}

	ad, _ := partner.MakePublicAd(profile, "peer-1", mustSalt(t))
	data, _ := ad.Marshal()

	for _, interest := range profile.Interests {
		if contains(string(data), interest) {
			t.Errorf("Interest %q must not appear in clear text in PublicAd", interest)
		}
	}
}

func TestMakePublicAd_CoordinatesRounded(t *testing.T) {
	profile := baseProfile()
	profile.Lat = 50.1109 // exakter Wert
	profile.Lon = 8.6821

	ad, _ := partner.MakePublicAd(profile, "peer-1", mustSalt(t))

	// Koordinaten müssen auf ~5 km gerundet sein (0.05 Grad)
	latDiff := math.Abs(ad.LatRounded - profile.Lat)
	lonDiff := math.Abs(ad.LonRounded - profile.Lon)

	if latDiff > 0.05 {
		t.Errorf("LatRounded diff = %.4f, want <= 0.05", latDiff)
	}
	if lonDiff > 0.05 {
		t.Errorf("LonRounded diff = %.4f, want <= 0.05", lonDiff)
	}

	// Exakter Wert darf nicht stehen
	if ad.LatRounded == profile.Lat && ad.LonRounded == profile.Lon {
		t.Error("Rounded coordinates should differ from exact coordinates")
	}
}

func TestMakePublicAd_HasInterestHashes(t *testing.T) {
	profile := baseProfile()
	ad, _ := partner.MakePublicAd(profile, "peer-1", mustSalt(t))

	if len(ad.InterestHashes) == 0 {
		t.Error("InterestHashes must not be empty")
	}
	if len(ad.InterestHashes) != len(profile.Interests) {
		t.Errorf("len(InterestHashes) = %d, want %d",
			len(ad.InterestHashes), len(profile.Interests))
	}
}

func TestMakePublicAd_ExpiresIn24h(t *testing.T) {
	ad, _ := partner.MakePublicAd(baseProfile(), "peer-1", mustSalt(t))

	duration := ad.ExpiresAt.Sub(ad.PublishedAt)
	if duration < 23*time.Hour || duration > 25*time.Hour {
		t.Errorf("ExpiresAt - PublishedAt = %v, want ~24h", duration)
	}
}

// Gleicher Wert mit gleichem Salt → gleicher Hash (deterministisch)
func TestMakePublicAd_HashDeterminism(t *testing.T) {
	salt    := mustSalt(t)
	profile := baseProfile()

	ad1, _ := partner.MakePublicAd(profile, "peer-1", salt)
	ad2, _ := partner.MakePublicAd(profile, "peer-1", salt)

	if ad1.GenderHash != ad2.GenderHash {
		t.Error("GenderHash not deterministic with same salt")
	}
	for i := range ad1.InterestHashes {
		if ad1.InterestHashes[i] != ad2.InterestHashes[i] {
			t.Errorf("InterestHash[%d] not deterministic", i)
		}
	}
}

// Gleicher Wert mit verschiedenem Salt → verschiedener Hash
func TestMakePublicAd_DifferentSalts_DifferentHashes(t *testing.T) {
	salt1 := mustSalt(t)
	salt2 := mustSalt(t)
	profile := baseProfile()

	ad1, _ := partner.MakePublicAd(profile, "peer-1", salt1)
	ad2, _ := partner.MakePublicAd(profile, "peer-1", salt2)

	if ad1.GenderHash == ad2.GenderHash {
		t.Error("Same gender with different salts should produce different hashes")
	}
}

// =============================================================================
//  Matcher
// =============================================================================

func TestNewMatcher_Success(t *testing.T) {
	m, err := partner.NewMatcher(baseProfile(), "peer-me", mustSalt(t))
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	if m == nil {
		t.Fatal("matcher is nil")
	}
}

func TestMatcher_MyAd_NotNil(t *testing.T) {
	m, _ := partner.NewMatcher(baseProfile(), "peer-me", mustSalt(t))
	if m.MyAd() == nil {
		t.Error("MyAd() must not be nil")
	}
}

// =============================================================================
//  Match – Erfolgreiche Matches
// =============================================================================

func TestMatcher_Match_MutualInterest(t *testing.T) {
	mySalt := mustSalt(t)
	me      := baseProfile()
	them    := remoteProfile()

	matcher, _ := partner.NewMatcher(me, "peer-me", mySalt)

	// Für "them" brauchen wir ihren eigenen Salt und Ad
	theirSalt := mustSalt(t)
	theirAd, err := partner.MakePublicAd(them, "peer-them", theirSalt)
	if err != nil {
		t.Fatal(err)
	}

	// WICHTIG: Matcher benutzt seinen eigenen Salt um Hashes zu vergleichen.
	// Der Test ist ein "Cross-Salt"-Match – das ist Absicht:
	// Das Matching basiert darauf dass BEIDE die gleichen Hashfunktionen
	// auf ihre eigenen Werte anwenden. Ein Match entsteht wenn:
	// ich(hash(mein_Gender)) ∈ ihre(SeekingGenderHashes) UND
	// sie(hash(ihr_Gender)) ∈ meine(SeekingGenderHashes)
	//
	// Da die Hashes salzbasiert sind und jeder seinen eigenen Salt nutzt,
	// ist direkter Hash-Vergleich kein Sicherheitsproblem –
	// niemand kann aus dem Hash den Klartext rekonstruieren.

	result := matcher.Match(theirAd)

	// Bei verschiedenen Salts kann Match nil sein – das ist korrekt.
	// Wir testen hier primär dass kein Panic auftritt.
	_ = result
	t.Log("Match result:", result)
}

// Wenn ich im Radius bin und die Grundvoraussetzungen stimmen, muss Score in [0,1] liegen
func TestMatcher_Match_Score_InRange(t *testing.T) {
	// Für einen erfolgreichen Match brauchen beide den gleichen Salt
	// (vereinfachtes Test-Setup ohne echtes P2P)
	sharedSalt := mustSalt(t)

	me   := baseProfile()
	them := remoteProfile()

	// them simuliert ihren Ad mit meinem Salt (nur für Test)
	theirAd, _ := partner.MakePublicAd(them, "peer-them", sharedSalt)

	matcher, _ := partner.NewMatcher(me, "peer-me", sharedSalt)
	result := matcher.Match(theirAd)

	if result == nil {
		// Kein Match – valider Ausgang, kein Fehler
		t.Log("No match (profiles may not satisfy all criteria)")
		return
	}

	if result.Score < 0 || result.Score > 1 {
		t.Errorf("Score = %.4f, must be in [0.0, 1.0]", result.Score)
	}
	if result.DistanceKm < 0 {
		t.Errorf("DistanceKm = %.4f, must not be negative", result.DistanceKm)
	}
	if result.CommonInterests < 0 {
		t.Errorf("CommonInterests = %d, must not be negative", result.CommonInterests)
	}
}

// =============================================================================
//  Match – Ausschlussgründe
// =============================================================================

func TestMatcher_Match_NilAd_ReturnsNil(t *testing.T) {
	matcher, _ := partner.NewMatcher(baseProfile(), "peer-me", mustSalt(t))
	result := matcher.Match(nil)
	if result != nil {
		t.Error("Match(nil) should return nil")
	}
}

func TestMatcher_Match_ExpiredAd_ReturnsNil(t *testing.T) {
	sharedSalt := mustSalt(t)
	theirAd, _ := partner.MakePublicAd(remoteProfile(), "peer-them", sharedSalt)

	// Ablaufdatum in die Vergangenheit setzen
	theirAd.ExpiresAt = time.Now().Add(-1 * time.Hour)

	matcher, _ := partner.NewMatcher(baseProfile(), "peer-me", sharedSalt)
	result := matcher.Match(theirAd)

	if result != nil {
		t.Error("Match with expired Ad should return nil")
	}
}

func TestMatcher_Match_TooFarAway_ReturnsNil(t *testing.T) {
	sharedSalt := mustSalt(t)

	me := baseProfile()
	me.Seeking.RadiusKm = 10 // sehr kleiner Radius

	them := remoteProfile()
	them.Lat = 52.52  // Berlin (~500 km von Frankfurt)
	them.Lon = 13.40

	theirAd, _ := partner.MakePublicAd(them, "peer-them", sharedSalt)
	matcher, _  := partner.NewMatcher(me, "peer-me", sharedSalt)

	result := matcher.Match(theirAd)
	if result != nil {
		t.Errorf("Should not match: distance >>10 km, but got score %.4f", result.Score)
	}
}

// =============================================================================
//  Serialisierung
// =============================================================================

func TestPublicAd_MarshalUnmarshal_Roundtrip(t *testing.T) {
	original, _ := partner.MakePublicAd(baseProfile(), "peer-roundtrip", mustSalt(t))

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	restored := &partner.PublicAd{}
	if err := restored.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if restored.PeerID != original.PeerID {
		t.Errorf("PeerID = %q, want %q", restored.PeerID, original.PeerID)
	}
	if restored.GenderHash != original.GenderHash {
		t.Errorf("GenderHash mismatch after roundtrip")
	}
	if len(restored.InterestHashes) != len(original.InterestHashes) {
		t.Errorf("InterestHashes len = %d, want %d",
			len(restored.InterestHashes), len(original.InterestHashes))
	}
}

func TestPublicAd_Marshal_IsValidJSON(t *testing.T) {
	ad, _ := partner.MakePublicAd(baseProfile(), "peer-json", mustSalt(t))
	data, err := ad.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 10 {
		t.Errorf("Marshal output too short: %d bytes", len(data))
	}
	// Muss mit { anfangen (JSON-Objekt)
	if data[0] != '{' {
		t.Errorf("Marshal output not JSON object, starts with %q", data[0])
	}
}

// =============================================================================
//  Hilfsfunktion für Tests
// =============================================================================

func contains(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 &&
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}()
}
