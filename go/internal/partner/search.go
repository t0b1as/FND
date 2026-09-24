package partner

import (
	"math"
	"sort"
	"strings"
	"time"
)

// =============================================================================
//  SearchableAd – opt-in öffentliches Suchprofil
//
//  Im Unterschied zum PrivateAd (salt-gehashte Werte, kein Klartext) enthält
//  der SearchableAd bewusst grob kategorisierte Klartextwerte.
//  Der User aktiviert diese Veröffentlichung explizit ("searchable = true").
//
//  Keine freien Texte, keine genaue Position, kein Name – nur Kategorien
//  aus den vordefinierten Listen.
// =============================================================================

// SearchableAd wird im P2P-Netz auf dem Topic "fundus.partner.search" publiziert.
type SearchableAd struct {
	PeerID   string `json:"peer_id"`
	FundusID string `json:"fundus_id,omitempty"` // Wallet-Identität (für Messenger-Kontakt aus der Suche)

	// Grobe Kategorienwerte (aus den vordefinierten Listen, kein Freitext)
	GenderCategory string   `json:"gc"`    // "male" | "female" | "non-binary" | "other"
	AgeRange       string   `json:"ar"`    // aus AgeRange-Enum
	EducationGroup string   `json:"eg"`    // "school" | "vocational" | "university" | "other"
	IndustryGroup  string   `json:"ig"`    // Industrie-Gruppe (grob)

	// Opt-in Collections (nur aus vordefinierten Listen – kein Freitext)
	HobbyTags      []string `json:"ht,omitempty"`   // max. 5 Tags
	PrefTags       []string `json:"pt,omitempty"`   // max. 5 Tags
	SexPrefAbbrs   []string `json:"spa,omitempty"`  // Abkürzungen (opt-in)

	// Grobstandort: ~10 km Rasterung (größer als beim PrivateAd)
	LatCell float64 `json:"lc"`
	LonCell float64 `json:"lnc"`

	// Suchpräferenzen (was ich suche)
	SeekingGender    []string `json:"sg"`
	SeekingAgeRanges []string `json:"sar"`
	SeekingRadiusKm  float64  `json:"sr"`

	PublishedAt time.Time `json:"pub"`
	ExpiresAt   time.Time `json:"exp"`

	// ── Offene Profildaten (öffentliches Profil, Klartext) ────────────────────
	Nickname    string   `json:"nick,omitempty"`
	Age         int      `json:"age,omitempty"` // echtes Alter (aus Geburtsdatum)
	BioOpen     string   `json:"bio,omitempty"`
	ImageHashes []string `json:"imgs,omitempty"`
	VideoHashes []string `json:"vids,omitempty"`
}

// SearchResult ist ein Treffer aus einer parametrischen Suche.
type SearchResult struct {
	PeerID         string    `json:"peer_id"`
	DistanceKm     float64   `json:"distance_km"`
	Score          float64   `json:"score"`           // 0.0–1.0
	MatchedHobbies []string  `json:"matched_hobbies"` // gemeinsame Tags
	MatchedPrefs   []string  `json:"matched_prefs"`
	MatchedSex     []string  `json:"matched_sex"`
	AgeRange       string    `json:"age_range"`
	EducationGroup string    `json:"education_group"`
	IndustryGroup  string    `json:"industry_group"`
	FoundAt        time.Time `json:"found_at"`

	// Offene Profildaten (öffentliches Profil).
	FundusID    string   `json:"fundus_id,omitempty"` // für Messenger-Kontakt
	Nickname    string   `json:"nickname,omitempty"`
	Gender      string   `json:"gender,omitempty"`
	Age         int      `json:"age,omitempty"`
	BioOpen     string   `json:"bio,omitempty"`
	Hobbies     []string `json:"hobbies,omitempty"`
	Prefs       []string `json:"preferences,omitempty"`
	SexPrefs    []string `json:"sexual_prefs,omitempty"`
	ImageHashes []string `json:"image_hashes,omitempty"`
	VideoHashes []string `json:"video_hashes,omitempty"`
}

// =============================================================================
//  SearchFilter – Suchkriterien
// =============================================================================

// SortOrder bestimmt die Sortierung der Suchergebnisse.
type SortOrder string

const (
	SortByDistance SortOrder = "distance"
	SortByScore    SortOrder = "score"
	SortByHobbies  SortOrder = "hobbies"
	SortBySexMatch SortOrder = "sex"
)

// SearchFilter enthält alle Suchparameter.
// Felder die nil/leer sind werden NICHT gefiltert (= egal).
type SearchFilter struct {
	// Standort der Suche (Zentrum)
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`

	// Pflicht: Radius. 0 = kein Limit (alles im Cache)
	RadiusKm float64 `json:"radius_km"`

	// Kategorienfilter (leer = alle akzeptieren)
	Genders        []string `json:"genders"`         // z.B. ["female", "non-binary"]
	AgeRanges      []string `json:"age_ranges"`      // z.B. ["26-35", "36-45"] (veraltet)
	AgeMin         int      `json:"age_min"`         // echtes Mindestalter (0 = egal)
	AgeMax         int      `json:"age_max"`         // echtes Höchstalter (0 = egal)
	EducationGroups []string `json:"education_groups"` // z.B. ["university"]
	IndustryGroups  []string `json:"industry_groups"`  // z.B. ["it", "education"]

	// Interessen-Filter (mindestens 1 gemeinsamer Tag)
	Hobbies      []string `json:"hobbies"`       // Tags aus DefaultHobbies
	Preferences  []string `json:"preferences"`   // Tags aus DefaultPreferences
	SexPrefAbbrs []string `json:"sex_pref_abbrs"` // Abkürzungen

	// Gegenseitigkeitsprüfung
	// true = nur Ads zurückgeben die auch mein Gender/Alter suchen
	RequireMutual bool `json:"require_mutual"`

	// Eigene Angaben für Gegenseitigkeitsprüfung (nur wenn RequireMutual=true)
	MyGender   string `json:"my_gender"`
	MyAgeRange string `json:"my_age_range"`

	// Ergebnis-Steuerung
	SortBy SortOrder `json:"sort_by"` // Standard: score
	Limit  int       `json:"limit"`   // 0 = alle (max 100)
	Offset int       `json:"offset"`
}

// Validate setzt Standardwerte und prüft auf Konsistenz.
func (f *SearchFilter) Validate() {
	if f.SortBy == "" {
		f.SortBy = SortByScore
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	if f.RadiusKm <= 0 {
		f.RadiusKm = math.MaxFloat64 // kein Limit
	}
}

// =============================================================================
//  Searcher
// =============================================================================

// Searcher führt parametrische Suchen auf einem Slice von SearchableAds aus.
// Er hat keinen eigenen Salt – arbeitet auf Klartextkategorien.
type Searcher struct{}

// NewSearcher erstellt einen Searcher.
func NewSearcher() *Searcher { return &Searcher{} }

// Search wendet den Filter auf eine Liste von SearchableAds an und gibt
// sortierte, paginierte Ergebnisse zurück.
func (s *Searcher) Search(ads []*SearchableAd, filter SearchFilter) ([]SearchResult, int) {
	filter.Validate()

	var results []SearchResult

	for _, ad := range ads {
		if ad == nil || ad.ExpiresAt.Before(time.Now()) {
			continue
		}

		// 1. Distanzfilter
		distKm := haversineKm(filter.Lat, filter.Lon, ad.LatCell, ad.LonCell)
		// Standort unbekannt (Node ohne gesetzten Standort → 0/0): nicht nach
		// Entfernung filtern, sonst läge das Profil rechnerisch ~5.000 km weg.
		locUnknown := (filter.Lat == 0 && filter.Lon == 0) || (ad.LatCell == 0 && ad.LonCell == 0)
		if locUnknown {
			distKm = 0
		}
		if !locUnknown && filter.RadiusKm < math.MaxFloat64 && distKm > filter.RadiusKm {
			continue
		}

		// 2. Kategorienfilter
		if !matchesAny(filter.Genders, ad.GenderCategory) {
			continue
		}
		if !matchesAny(filter.AgeRanges, ad.AgeRange) {
			continue
		}
		// Echter Altersfilter (von–bis). Bei aktivem Filter nur Profile mit
		// bekanntem Alter im Bereich.
		if filter.AgeMin > 0 || filter.AgeMax > 0 {
			if ad.Age <= 0 {
				continue
			}
			if filter.AgeMin > 0 && ad.Age < filter.AgeMin {
				continue
			}
			if filter.AgeMax > 0 && ad.Age > filter.AgeMax {
				continue
			}
		}
		if !matchesAny(filter.EducationGroups, ad.EducationGroup) {
			continue
		}
		if !matchesAny(filter.IndustryGroups, ad.IndustryGroup) {
			continue
		}

		// 3. Interessenfilter (mindestens 1 Übereinstimmung wenn Filter gesetzt)
		var matchedHobbies, matchedPrefs, matchedSex []string
		if len(filter.Hobbies) > 0 {
			matchedHobbies = intersect(filter.Hobbies, ad.HobbyTags)
			if len(matchedHobbies) == 0 {
				continue
			}
		} else {
			matchedHobbies = intersect(filter.Hobbies, ad.HobbyTags) // für Score
		}

		matchedPrefs = intersect(filter.Preferences, ad.PrefTags)

		if len(filter.SexPrefAbbrs) > 0 {
			matchedSex = intersect(filter.SexPrefAbbrs, ad.SexPrefAbbrs)
			if len(matchedSex) == 0 {
				continue
			}
		} else {
			matchedSex = intersect(filter.SexPrefAbbrs, ad.SexPrefAbbrs)
		}

		// 4. Gegenseitigkeitsprüfung (opt-in)
		if filter.RequireMutual && filter.MyGender != "" {
			if !matchesAny(ad.SeekingGender, filter.MyGender) {
				continue
			}
		}
		if filter.RequireMutual && filter.MyAgeRange != "" {
			if !matchesAny(ad.SeekingAgeRanges, filter.MyAgeRange) {
				continue
			}
		}

		// 5. Score berechnen
		score := calcScore(distKm, filter.RadiusKm,
			len(matchedHobbies), len(filter.Hobbies),
			len(matchedPrefs),   len(filter.Preferences),
			len(matchedSex),     len(filter.SexPrefAbbrs))

		results = append(results, SearchResult{
			PeerID:         ad.PeerID,
			DistanceKm:     math.Round(distKm*100) / 100,
			Score:          math.Round(score*1000) / 1000,
			MatchedHobbies: matchedHobbies,
			MatchedPrefs:   matchedPrefs,
			MatchedSex:     matchedSex,
			AgeRange:       ad.AgeRange,
			EducationGroup: ad.EducationGroup,
			IndustryGroup:  ad.IndustryGroup,
			FoundAt:        time.Now(),
			// Offene Profildaten durchreichen (öffentliches Profil).
			FundusID:    ad.FundusID,
			Nickname:    ad.Nickname,
			Gender:      ad.GenderCategory,
			Age:         ad.Age,
			BioOpen:     ad.BioOpen,
			Hobbies:     ad.HobbyTags,
			Prefs:       ad.PrefTags,
			SexPrefs:    ad.SexPrefAbbrs,
			ImageHashes: ad.ImageHashes,
			VideoHashes: ad.VideoHashes,
		})
	}

	total := len(results)

	// 6. Sortieren
	sort.Slice(results, func(i, j int) bool {
		switch filter.SortBy {
		case SortByDistance:
			return results[i].DistanceKm < results[j].DistanceKm
		case SortByHobbies:
			ci := len(results[i].MatchedHobbies) + len(results[i].MatchedPrefs)
			cj := len(results[j].MatchedHobbies) + len(results[j].MatchedPrefs)
			if ci != cj {
				return ci > cj
			}
			return results[i].Score > results[j].Score
		case SortBySexMatch:
			if len(results[i].MatchedSex) != len(results[j].MatchedSex) {
				return len(results[i].MatchedSex) > len(results[j].MatchedSex)
			}
			return results[i].Score > results[j].Score
		default: // SortByScore
			return results[i].Score > results[j].Score
		}
	})

	// 7. Paginieren
	start := filter.Offset
	if start >= len(results) {
		return []SearchResult{}, total
	}
	end := start + filter.Limit
	if end > len(results) {
		end = len(results)
	}
	return results[start:end], total
}

// =============================================================================
//  MakeSearchableAd – SearchableAd aus Profil erstellen
// =============================================================================

// MakeSearchableAd erstellt einen SearchableAd aus einem Profile.
// Nur die opt-in Felder werden übernommen.
// Der User muss explizit zustimmen (searchable=true im Profil).
func MakeSearchableAd(profile *Profile, peerID string) *SearchableAd {
	now := time.Now().UTC()

	// Tags auf max. 5 begrenzen und normalisieren
	hobbies := limitAndNorm(profile.Hobbies, 5)
	prefs   := limitAndNorm(profile.Preferences, 5)

	// Sex-Pref-Abkürzungen (nur Abbr, keine Rolle – für Suchbarkeit)
	sexAbbrs := make([]string, 0, len(profile.SexualPrefs))
	for _, sp := range profile.SexualPrefs {
		sexAbbrs = append(sexAbbrs, strings.ToUpper(sp.Abbr))
	}

	seekGenders := make([]string, len(profile.Seeking.Genders))
	for i, g := range profile.Seeking.Genders {
		seekGenders[i] = string(g)
	}
	seekAges := make([]string, len(profile.Seeking.AgeRanges))
	for i, a := range profile.Seeking.AgeRanges {
		seekAges[i] = string(a)
	}

	return &SearchableAd{
		PeerID:         peerID,
		GenderCategory: normalizeGender(string(profile.Gender)),
		AgeRange:       string(effectiveAgeRange(profile)),
		EducationGroup: groupEducation(profile.Education),
		IndustryGroup:  profile.Industry,
		HobbyTags:      hobbies,
		PrefTags:       prefs,
		SexPrefAbbrs:   sexAbbrs,
		LatCell:        roundCell(profile.Lat, 0.1), // ~10 km
		LonCell:        roundCell(profile.Lon, 0.1),
		SeekingGender:   seekGenders,
		SeekingAgeRanges: seekAges,
		SeekingRadiusKm:  profile.Seeking.RadiusKm,
		// Offene Profildaten (öffentliches Profil, Klartext geteilt).
		Nickname:    profile.Nickname,
		Age:         profile.Age,
		BioOpen:     profile.Bio,
		ImageHashes: profile.ImageHashes,
		VideoHashes: profile.VideoHashes,
		PublishedAt:    now,
		ExpiresAt:      now.Add(24 * time.Hour),
	}
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

// matchesAny gibt true zurück wenn filter leer ist ODER val in filter enthalten ist.
func matchesAny(filter []string, val string) bool {
	if len(filter) == 0 {
		return true
	}
	val = strings.ToLower(strings.TrimSpace(val))
	for _, f := range filter {
		if strings.ToLower(strings.TrimSpace(f)) == val {
			return true
		}
	}
	return false
}

// intersect gibt gemeinsame Elemente zweier Slices zurück.
func intersect(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[strings.ToLower(s)] = true
	}
	var result []string
	for _, s := range b {
		if set[strings.ToLower(s)] {
			result = append(result, s)
		}
	}
	return result
}

// calcScore berechnet einen Gesamt-Score aus allen Dimensionen.
// Distanz: 40%, Hobbies: 25%, Prefs: 20%, Sex: 15%
func calcScore(distKm, radiusKm float64,
	commonHobbies, totalHobbies,
	commonPrefs, totalPrefs,
	commonSex, totalSex int) float64 {

	// Distanz-Score: linear, 0 = am Rand des Radius, 1 = gleicher Ort
	distScore := 1.0
	if radiusKm > 0 && radiusKm < math.MaxFloat64 {
		distScore = 1.0 - (distKm / radiusKm)
		if distScore < 0 {
			distScore = 0
		}
	}

	hobbyScore := scoreRatio(commonHobbies, totalHobbies)
	prefScore  := scoreRatio(commonPrefs,   totalPrefs)
	sexScore   := scoreRatio(commonSex,     totalSex)

	return 0.40*distScore + 0.25*hobbyScore + 0.20*prefScore + 0.15*sexScore
}

func scoreRatio(common, total int) float64 {
	if total <= 0 {
		return 1.0 // kein Filter gesetzt = kein Malus
	}
	return float64(common) / float64(total)
}

func limitAndNorm(tags []string, max int) []string {
	result := make([]string, 0, max)
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			result = append(result, t)
		}
		if len(result) >= max {
			break
		}
	}
	return result
}

// normalizeGender ordnet freie Gender-Angaben einer groben Kategorie zu.
func normalizeGender(g string) string {
	g = strings.ToLower(strings.TrimSpace(g))
	switch g {
	case "male", "männlich", "mann", "m":
		return "male"
	case "female", "weiblich", "frau", "f":
		return "female"
	case "non-binary", "nonbinary", "nb", "divers", "x":
		return "non-binary"
	default:
		if g == "" {
			return "other"
		}
		return g // eigene Angabe bleibt erhalten
	}
}

// groupEducation ordnet Ausbildungsgrad einer groben Gruppe zu.
func groupEducation(edu string) string {
	switch edu {
	case "hauptschule", "mittlere_reife", "abitur":
		return "school"
	case "berufsausbildung":
		return "vocational"
	case "bachelor", "master", "diplom", "doktor", "professor":
		return "university"
	case "selbststudium":
		return "self-taught"
	default:
		return "other"
	}
}

// roundCell rundet eine Koordinate auf das nächste Raster.
func roundCell(coord, grid float64) float64 {
	return math.Round(coord/grid) * grid
}
