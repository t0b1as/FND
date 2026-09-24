// Package partner implementiert das datenschutzfreundliche Partner-Modul.
//
// Design-Prinzipien:
//   - Profildetails verlassen den eigenen Node NIE im Klartext
//   - Im P2P-Netz werden nur Bloom-Filter-Hashes publiziert
//   - Matching findet lokal statt: ich lade Hashes herunter, ich vergleiche
//   - Kontaktaufnahme nur über verschlüsselte Direktnachricht (libp2p stream)
//   - Kein zentraler Matchmaking-Server
package partner

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// =============================================================================
//  Typen
// =============================================================================

// Gender ist ein offenes Enum – keine vordefinierte Liste.
type Gender string

// AgeRange ist eine grobe Altersklasse (nicht das genaue Geburtsdatum).
type AgeRange string

const (
	Age18_25 AgeRange = "18-25"
	Age26_35 AgeRange = "26-35"
	Age36_45 AgeRange = "36-45"
	Age46_55 AgeRange = "46-55"
	Age56Plus AgeRange = "56+"
)

// effectiveAgeRange gibt die AgeRange des Profils zurück — direkt gesetzt oder
// aus dem (Geburtsdatum-basierten) Alter abgeleitet. So funktioniert der
// Altersfilter auch für Profile, die nur ein Geburtsdatum/Alter haben.
func effectiveAgeRange(profile *Profile) AgeRange {
	if profile == nil {
		return ""
	}
	if profile.AgeRange != "" {
		return profile.AgeRange
	}
	return ageRangeFromAge(profile.Age)
}

// ageRangeFromAge leitet die grobe Altersklasse aus einem genauen Alter ab.
func ageRangeFromAge(age int) AgeRange {
	switch {
	case age <= 0:
		return ""
	case age <= 25:
		return Age18_25
	case age <= 35:
		return Age26_35
	case age <= 45:
		return Age36_45
	case age <= 55:
		return Age46_55
	default:
		return Age56Plus
	}
}

// Preference beschreibt wen jemand sucht.
type Preference struct {
	Genders   []Gender   `json:"genders"`
	AgeRanges []AgeRange `json:"age_ranges"`
	RadiusKm  float64    `json:"radius_km"`

	// Erweiterte Filter (optional – schränken den Match-Pool ein)
	Educations  []string `json:"educations,omitempty"`  // gewünschte Ausbildungsgrade
	Industries  []string `json:"industries,omitempty"`  // gewünschte Branchen

	// Sexuelle Kompatibilitäts-Filter (optional)
	// Matching prüft ob Schnittmenge zwischen meinen Prefs und ihren Prefs existiert
	SexPrefAbbrs []string `json:"sex_pref_abbrs,omitempty"` // Abkürzungen die ich suche
}

// Profile enthält alle persönlichen Daten – bleibt lokal auf dem Node.
type Profile struct {
	// Identität
	Nickname  string    `json:"nickname"`
	Gender    Gender    `json:"gender"`
	AgeRange  AgeRange  `json:"age_range,omitempty"` // veraltet, für Rückwärtskompatibilität
	Birthdate string    `json:"birthdate,omitempty"` // YYYY-MM-DD; Alter wird daraus berechnet
	Age       int       `json:"age,omitempty"`       // aus Birthdate berechnetes Alter (öffentlich)
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`

	// Hintergrund (werden gehasht, nie im Klartext publiziert)
	Education  string   `json:"education"`   // aus DefaultEducationLevels oder eigener Wert
	Profession string   `json:"profession"`  // Berufsbezeichnung
	Industry   string   `json:"industry"`    // aus DefaultIndustries oder eigener Wert

	// Interessen & Vorlieben (werden gehasht)
	Hobbies     []string `json:"hobbies"`     // aus DefaultHobbies + eigene
	Preferences []string `json:"preferences"` // aus DefaultPreferences + eigene
	Dislikes    []string `json:"dislikes"`    // aus DefaultDislikes + eigene

	// Sexuelle Vorlieben (werden gehasht inkl. Rolle)
	// Nur gespeichert wenn der User sie explizit setzt.
	SexualPrefs []SexualPreference `json:"sexual_prefs"`

	// Freitext (NIEMALS publiziert, nur lokal)
	Bio string `json:"bio"`

	// Profilbilder — FileStore-Hashes der hochgeladenen Bilder (wie beim
	// Marktplatz). Werden NICHT anonym publiziert, nur an Matches gezeigt.
	ImageHashes []string `json:"image_hashes,omitempty"`
	VideoHashes []string `json:"video_hashes,omitempty"` // Profil-Videos (mehrere möglich)

	// Suchpräferenzen
	Seeking Preference `json:"seeking"`

	// Intern
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PublicAd ist das was im P2P-Netz publiziert wird.
// Enthält KEINE persönlichen Daten – nur Hashes und Metadaten für das Matching.
type PublicAd struct {
	PeerID      string    `json:"peer_id"`
	FundusID    string    `json:"fundus_id,omitempty"` // Wallet-/Chain-Identität des Erstellers (für Messenger-Kontakt, Cross-Node)
	LatRounded  float64   `json:"lat_r"`
	LonRounded  float64   `json:"lon_r"`

	// Hashes eigener Eigenschaften
	GenderHash    string   `json:"gh"`
	AgeRangeHash  string   `json:"ah"`
	EducationHash string   `json:"eh,omitempty"`
	IndustryHash  string   `json:"ih2,omitempty"`

	// Bloom-Filter für Collections
	HobbyHashes   []string `json:"hbh,omitempty"`
	PrefHashes    []string `json:"pfh,omitempty"`
	DislikeHashes []string `json:"dlh,omitempty"`

	// Sexuelle Vorlieben: Hash aus "ABBR:ROLE" (z.B. "BDSM:aktiv")
	// Niemand kann ohne Salt rekonstruieren was dahinter steckt
	SexPrefHashes []string `json:"sph,omitempty"`

	// Suchpräferenz-Hashes
	SeekingGenderHashes    []string `json:"sgh"`
	SeekingAgeRangeHashes  []string `json:"sah"`
	SeekingEducationHashes []string `json:"seh,omitempty"`
	SeekingIndustryHashes  []string `json:"sih,omitempty"`
	SeekingSexPrefHashes   []string `json:"ssph,omitempty"`
	SeekingRadiusKm        float64  `json:"sr"`

	PublishedAt time.Time `json:"pub"`
	ExpiresAt   time.Time `json:"exp"`

	// ── Offene Profildaten (öffentliches Dating-Profil) ───────────────────────
	// ACHTUNG: Diese Felder sind KLARTEXT und werden im P2P-Netz offen geteilt.
	// Sie landen auf fremden Nodes und sind nicht mehr zurückholbar. Bewusste
	// Design-Entscheidung: offenes Profil statt anonymer Tag-Suche.
	Nickname    string   `json:"nick,omitempty"`
	GenderOpen  string   `json:"go,omitempty"`   // Klartext-Geschlecht
	AgeOpen     string   `json:"ao,omitempty"`   // Klartext-Altersspanne
	BioOpen     string   `json:"bio,omitempty"`  // Freitext-Beschreibung
	HobbiesOpen []string `json:"hbo,omitempty"`  // Klartext-Hobbys
	PrefsOpen   []string `json:"pfo,omitempty"`  // Klartext-Werte
	SexOpen     []string `json:"spo,omitempty"`  // Klartext-Vorlieben (Abkürzungen)
	EduOpen     string   `json:"edo,omitempty"`  // Bildung
	JobOpen     string   `json:"jbo,omitempty"`  // Beruf
	IndOpen     string   `json:"ino,omitempty"`  // Branche
	ImageHashes []string `json:"imgs,omitempty"` // FileStore-Hashes der Profilbilder
	VideoHashes []string `json:"vids,omitempty"` // FileStore-Hashes der Profil-Videos
}

// MatchResult beschreibt einen lokalen Match.
type MatchResult struct {
	PeerID      string    `json:"peer_id"`
	FundusID    string    `json:"fundus_id,omitempty"` // Wallet-Identität des Matches (für Messenger-Kontakt)
	Score       float64   `json:"score"`       // 0.0–1.0
	DistanceKm  float64   `json:"distance_km"`
	CommonInterests int   `json:"common_interests"`
	ContactSent bool      `json:"contact_sent"`
	MatchedAt   time.Time `json:"matched_at"`

	// Offene Profildaten des Matches (für die Anzeige).
	Nickname    string   `json:"nickname,omitempty"`
	GenderOpen  string   `json:"gender,omitempty"`
	AgeOpen     string   `json:"age,omitempty"`
	BioOpen     string   `json:"bio,omitempty"`
	HobbiesOpen []string `json:"hobbies,omitempty"`
	PrefsOpen   []string `json:"preferences,omitempty"`
	SexOpen     []string `json:"sexual_prefs,omitempty"`
	EduOpen     string   `json:"education,omitempty"`
	JobOpen     string   `json:"profession,omitempty"`
	IndOpen     string   `json:"industry,omitempty"`
	ImageHashes []string `json:"image_hashes,omitempty"`
	VideoHashes []string `json:"video_hashes,omitempty"`
}

// =============================================================================
//  Salt-basiertes Hashing
// =============================================================================

// Salt ist ein 16-Byte Zufallswert pro Profil.
// Verhindert dass gleiche Werte in verschiedenen Profilen gleiche Hashes erzeugen.
type Salt [16]byte

// NewSalt generiert einen kryptografisch sicheren Salt.
func NewSalt() (Salt, error) {
	var s Salt
	_, err := rand.Read(s[:])
	return s, err
}

// hashValue berechnet Argon2id(value, salt) – kein HMAC-SHA256.
// Argon2id ist speicherhart und schützt Profile-Daten vor Brute-Force.
func hashValue(salt Salt, value string) string {
	normalized := []byte(strings.ToLower(strings.TrimSpace(value)))
	// Argon2id: leichtgewichtig genug für viele Werte, stark genug gegen Angriffe
	h := argon2.IDKey(normalized, salt[:], 1, 16*1024, 1, 32)
	return hex.EncodeToString(h)
}

// matchSalt ist ein NETZWERKWEIT FESTER Salt, der NUR für die Match-Kriterien
// (Gender, Altersklasse, gesuchte Kategorien) verwendet wird. Nur mit einem
// gemeinsamen Salt ergeben zwei Profile für denselben Wert denselben Hash —
// sonst wäre Cross-Profil-Matching unmöglich (pro-Profil-Salt → nie ein Match).
// Bewusste Abwägung: Diese groben Kategorien gibt man fürs Matching ohnehin
// preis; alle privaten Felder (Bio, Hobbys) nutzen weiter den pro-Profil-Salt.
var matchSalt = Salt{
	0x46, 0x75, 0x6e, 0x64, 0x75, 0x73, 0x4d, 0x61,
	0x74, 0x63, 0x68, 0x53, 0x61, 0x6c, 0x74, 0x31,
}

// hashMatchValue hasht ein Match-Kriterium mit dem gemeinsamen Netzwerk-Salt.
func hashMatchValue(value string) string {
	return hashValue(matchSalt, value)
}

// hashMatchValues hasht mehrere Match-Kriterien mit dem gemeinsamen Salt.
func hashMatchValues(values []string) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = hashValue(matchSalt, v)
	}
	return result
}

// hashValues berechnet Hashes für eine Liste von Strings.
func hashValues(salt Salt, values []string) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = hashValue(salt, v)
	}
	return result
}

// roundCoordinate rundet eine Koordinate auf ~5 km Genauigkeit.
// 0.05 Grad ≈ 5.5 km auf dem Äquator, ~3.5 km in Mitteleuropa.
func roundCoordinate(coord float64) float64 {
	return float64(int(coord/0.05)) * 0.05
}

// =============================================================================
//  PublicAd erstellen
// =============================================================================

// MakePublicAd erstellt einen PublicAd aus einem Profile.
func MakePublicAd(profile *Profile, peerID string, salt Salt) (*PublicAd, error) {
	if profile == nil {
		return nil, errors.New("profile must not be nil")
	}
	if profile.Gender == "" {
		return nil, errors.New("gender is required")
	}
	if profile.AgeRange == "" {
		// age_range aus dem (aus dem Geburtsdatum berechneten) Alter ableiten,
		// da das Formular nur noch Geburtsdatum/Alter erfasst.
		profile.AgeRange = ageRangeFromAge(profile.Age)
	}
	if profile.AgeRange == "" {
		return nil, errors.New("age_range is required")
	}

	// Sex-Pref-Hashes: "ABBR:ROLE" zusammengeführt
	sexPrefKeys := make([]string, 0, len(profile.SexualPrefs))
	for _, sp := range profile.SexualPrefs {
		key := sp.Abbr + ":" + string(sp.Role)
		sexPrefKeys = append(sexPrefKeys, key)
	}

	// Seeking-Hashes
	seekSexPrefKeys := make([]string, 0, len(profile.Seeking.SexPrefAbbrs))
	for _, abbr := range profile.Seeking.SexPrefAbbrs {
		seekSexPrefKeys = append(seekSexPrefKeys, abbr)
	}

	now := time.Now().UTC()
	return &PublicAd{
		PeerID:     peerID,
		LatRounded: roundCoordinate(profile.Lat),
		LonRounded: roundCoordinate(profile.Lon),

		GenderHash:    hashMatchValue(string(profile.Gender)),
		AgeRangeHash:  hashMatchValue(string(profile.AgeRange)),
		EducationHash: hashMatchValue(profile.Education),
		IndustryHash:  hashMatchValue(profile.Industry),

		HobbyHashes:   hashMatchValues(profile.Hobbies),
		PrefHashes:    hashMatchValues(profile.Preferences),
		DislikeHashes: hashMatchValues(profile.Dislikes),
		SexPrefHashes: hashMatchValues(sexPrefKeys),

		SeekingGenderHashes:    hashMatchValues(gendersToStrings(profile.Seeking.Genders)),
		SeekingAgeRangeHashes:  hashMatchValues(ageRangesToStrings(profile.Seeking.AgeRanges)),
		SeekingEducationHashes: hashMatchValues(profile.Seeking.Educations),
		SeekingIndustryHashes:  hashMatchValues(profile.Seeking.Industries),
		SeekingSexPrefHashes:   hashMatchValues(seekSexPrefKeys),
		SeekingRadiusKm:        profile.Seeking.RadiusKm,

		// Offene Profildaten (Klartext, öffentlich geteilt).
		Nickname:    profile.Nickname,
		GenderOpen:  string(profile.Gender),
		AgeOpen:     openAge(profile),
		BioOpen:     profile.Bio,
		HobbiesOpen: profile.Hobbies,
		PrefsOpen:   profile.Preferences,
		SexOpen:     sexAbbrList(profile.SexualPrefs),
		EduOpen:     profile.Education,
		JobOpen:     profile.Profession,
		IndOpen:     profile.Industry,
		ImageHashes: profile.ImageHashes,
		VideoHashes: profile.VideoHashes,

		PublishedAt: now,
		ExpiresAt:   now.Add(24 * time.Hour),
	}, nil
}

// =============================================================================
//  Lokales Matching
// =============================================================================

// Matcher führt lokales Matching durch ohne Profildaten zu übertragen.
type Matcher struct {
	mySalt    Salt
	myProfile *Profile
	myAd      *PublicAd
}

// NewMatcher erstellt einen Matcher für das eigene Profil.
func NewMatcher(profile *Profile, peerID string, salt Salt) (*Matcher, error) {
	ad, err := MakePublicAd(profile, peerID, salt)
	if err != nil {
		return nil, fmt.Errorf("make public ad: %w", err)
	}
	return &Matcher{
		mySalt:    salt,
		myProfile: profile,
		myAd:      ad,
	}, nil
}

// Match prüft einen fremden PublicAd gegen das eigene Profil.
// Gibt nil zurück wenn kein Match.
func (m *Matcher) Match(remote *PublicAd) *MatchResult {
	r, _ := m.MatchWithReason(remote)
	return r
}

// MatchWithReason liefert das Match oder den Grund, warum keines zustande kam
// ("abgelaufen", "entfernung", "geschlecht", "alter") – für eine verständliche
// Anzeige bei 0 Treffern.
func (m *Matcher) MatchWithReason(remote *PublicAd) (*MatchResult, string) {
	if remote == nil || remote.ExpiresAt.Before(time.Now()) {
		return nil, "abgelaufen"
	}

	// 1. Distanz – nur wenn BEIDE Standorte bekannt sind. Ohne gesetzten
	//    Node-Standort steht ein Profil auf 0/0 und läge sonst ~5.000 km weg.
	locUnknown := (m.myProfile.Lat == 0 && m.myProfile.Lon == 0) ||
		(remote.LatRounded == 0 && remote.LonRounded == 0)
	distKm := -1.0
	if !locUnknown {
		distKm = haversineKm(
			m.myProfile.Lat, m.myProfile.Lon,
			remote.LatRounded, remote.LonRounded,
		)
		if m.myProfile.Seeking.RadiusKm > 0 && distKm > m.myProfile.Seeking.RadiusKm {
			return nil, "entfernung"
		}
		if remote.SeekingRadiusKm > 0 && distKm > remote.SeekingRadiusKm {
			return nil, "entfernung"
		}
	}

	// 2. Gender-Gegenseitigkeit – kein Wunsch angegeben = alle akzeptieren.
	myGenderHash := hashMatchValue(string(m.myProfile.Gender))
	if len(m.myProfile.Seeking.Genders) > 0 &&
		!containsAny(m.hashValues(gendersToStrings(m.myProfile.Seeking.Genders)), remote.GenderHash) {
		return nil, "geschlecht"
	}
	if len(remote.SeekingGenderHashes) > 0 && !containsAny(remote.SeekingGenderHashes, myGenderHash) {
		return nil, "geschlecht_gegen" // IHR Wunsch passt nicht zu MEINEM Geschlecht
	}

	// 3. Altersklassen-Gegenseitigkeit
	myAgeHash := hashMatchValue(string(m.myProfile.AgeRange))
	if len(m.myProfile.Seeking.AgeRanges) > 0 {
		if !containsAny(m.hashValues(ageRangesToStrings(m.myProfile.Seeking.AgeRanges)), remote.AgeRangeHash) {
			return nil, "alter"
		}
	}
	if len(remote.SeekingAgeRangeHashes) > 0 {
		if !containsAny(remote.SeekingAgeRangeHashes, myAgeHash) {
			return nil, "alter_gegen"
		}
	}

	// 4. Score-Berechnung: mehrere Dimensionen
	myHobbyHashes  := m.hashValues(m.myProfile.Hobbies)
	myPrefHashes   := m.hashValues(m.myProfile.Preferences)

	// Sex-Pref-Keys
	mySexKeys := make([]string, 0, len(m.myProfile.SexualPrefs))
	for _, sp := range m.myProfile.SexualPrefs {
		mySexKeys = append(mySexKeys, sp.Abbr+":"+string(sp.Role))
	}
	mySexHashes := m.hashValues(mySexKeys)

	commonHobbies   := countCommon(myHobbyHashes,  remote.HobbyHashes)
	commonPrefs     := countCommon(myPrefHashes,    remote.PrefHashes)
	commonSexPrefs  := countCommon(mySexHashes,     remote.SexPrefHashes)

	totalItems  := max(len(myHobbyHashes)+len(myPrefHashes), len(remote.HobbyHashes)+len(remote.PrefHashes), 1)
	commonTotal := commonHobbies + commonPrefs

	interestScore  := float64(commonTotal) / float64(totalItems)
	sexCompatScore := 0.0
	if len(mySexHashes) > 0 && len(remote.SexPrefHashes) > 0 {
		maxSex := max(len(mySexHashes), len(remote.SexPrefHashes), 1)
		sexCompatScore = float64(commonSexPrefs) / float64(maxSex)
	}
	distScore := 0.5 // Standort unbekannt → neutral
	if distKm >= 0 && m.myProfile.Seeking.RadiusKm > 0 {
		distScore = 1.0 - (distKm / m.myProfile.Seeking.RadiusKm)
	}

	// Gewichtung: 40% Distanz, 35% Interessen, 25% Sex-Kompatibilität
	score := 0.40*distScore + 0.35*interestScore + 0.25*sexCompatScore

	return &MatchResult{
		PeerID:          remote.PeerID,
		FundusID:        remote.FundusID,
		Score:           clamp(score, 0, 1),
		DistanceKm:      distKm,
		CommonInterests: commonTotal + commonSexPrefs,
		MatchedAt:       time.Now(),
		// Offene Profildaten des Matches übernehmen (die der PublicAd freiwillig
		// mitträgt) — damit das Match anklickbar ist und Bilder/Bio/Hobbys zeigt.
		Nickname:    remote.Nickname,
		GenderOpen:  remote.GenderOpen,
		AgeOpen:     remote.AgeOpen,
		BioOpen:     remote.BioOpen,
		HobbiesOpen: remote.HobbiesOpen,
		PrefsOpen:   remote.PrefsOpen,
		SexOpen:     remote.SexOpen,
		EduOpen:     remote.EduOpen,
		JobOpen:     remote.JobOpen,
		IndOpen:     remote.IndOpen,
		ImageHashes: remote.ImageHashes,
		VideoHashes: remote.VideoHashes,
	}, ""
}

// MyAd gibt den eigenen PublicAd zurück (für P2P-Publikation).
func (m *Matcher) MyAd() *PublicAd { return m.myAd }

// =============================================================================
//  Serialisierung
// =============================================================================

func (ad *PublicAd) Marshal() ([]byte, error)  { return json.Marshal(ad) }
func (ad *PublicAd) Unmarshal(data []byte) error { return json.Unmarshal(data, ad) }

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func (m *Matcher) hashValues(values []string) []string {
	// Match-Vergleiche nutzen den gemeinsamen Netzwerk-Salt, damit meine Hashes
	// mit denen fremder Profile übereinstimmen (sonst nie ein Match).
	return hashMatchValues(values)
}

func containsAny(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func countCommon(a, b []string) int {
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	count := 0
	for _, s := range b {
		if _, ok := set[s]; ok {
			count++
		}
	}
	return count
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b, def int) int {
	if a > b {
		if a > def {
			return a
		}
		return def
	}
	if b > def {
		return b
	}
	return def
}

func gendersToStrings(gs []Gender) []string {
	ss := make([]string, len(gs))
	for i, g := range gs {
		ss[i] = string(g)
	}
	return ss
}

func ageRangesToStrings(ars []AgeRange) []string {
	ss := make([]string, len(ars))
	for i, a := range ars {
		ss[i] = string(a)
	}
	return ss
}

// haversineKm lokale Implementierung – nutzt math-Paket für Genauigkeit.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// openAge liefert das echte Alter als Text (aus dem Geburtsdatum berechnet),
// sonst die grobe Altersklasse.
func openAge(p *Profile) string {
	if p.Age > 0 {
		return strconv.Itoa(p.Age)
	}
	return string(p.AgeRange)
}

// sexAbbrList wandelt die gewählten Vorlieben in eine Liste ihrer Abkürzungen.
func sexAbbrList(sp []SexualPreference) []string {
	out := make([]string, 0, len(sp))
	for _, x := range sp {
		if x.Abbr != "" {
			out = append(out, strings.ToUpper(x.Abbr))
		}
	}
	return out
}
