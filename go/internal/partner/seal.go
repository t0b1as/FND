package partner

// Versiegelte Partner-Anzeigen (R939).
//
// Bis R938 standen Spitzname, Alter, Bio, Hobbys, Werte, sexuelle Vorlieben,
// Ausbildung, Beruf und die Bild-Kennungen im Klartext in jeder Anzeige – und
// damit auf jedem Node des Netzes. Die Match-Hashes halfen nicht: Sie nutzen
// einen netzweit festen Salt, und die Werte stammen aus festen Auswahllisten,
// lassen sich also durch Ausprobieren zurückrechnen.
//
// Jetzt trägt eine Anzeige offen nur, was zum Gefundenwerden nötig ist:
// Geschlecht, Altersklasse, Grobstandort, Suchwunsch (Geschlecht, Alter,
// Radius) und die Wallet-Kennung für die Kontaktaufnahme. Alles andere liegt
// verschlüsselt (XChaCha20-Poly1305) im Feld "sealed". Den Schlüssel bekommt
// nur, wem der Besitzer ihn per Ende-zu-Ende-Nachricht freigibt; nach einem
// Entzug wird er gewechselt.
//
// Grenze: Bilder und Videos selbst liegen weiter unverschlüsselt im
// Dateispeicher. Ohne Freigabe kennt aber niemand ihre Kennungen.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

// SealKeyLen ist die Länge eines Profilschlüssels.
const SealKeyLen = 32

// ErrSealKey: Schlüssel fehlt, ist falsch oder veraltet.
var ErrSealKey = errors.New("partner: Profilschlüssel passt nicht")

// KeyID liefert die öffentliche Kennung eines Profilschlüssels (zeigt dem
// Empfänger, ob sein Schlüssel noch aktuell ist – verrät den Schlüssel nicht).
func KeyID(key []byte) string {
	h := sha256.Sum256(append([]byte("fundus/partner-kid/v1:"), key...))
	return hex.EncodeToString(h[:8])
}

func sealBytes(plain, key []byte) (string, error) {
	if len(key) != SealKeyLen {
		return "", ErrSealKey
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, []byte("fundus/partner/v1"))), nil
}

func openBytes(sealed string, key []byte) ([]byte, error) {
	if len(key) != SealKeyLen {
		return nil, ErrSealKey
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize() {
		return nil, ErrSealKey
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte("fundus/partner/v1"))
	if err != nil {
		return nil, ErrSealKey
	}
	return plain, nil
}

// ── PublicAd ─────────────────────────────────────────────────────────────────

type publicAdGeheim struct {
	EducationHash          string   `json:"eh,omitempty"`
	IndustryHash           string   `json:"ih,omitempty"`
	HobbyHashes            []string `json:"hh,omitempty"`
	PrefHashes             []string `json:"ph,omitempty"`
	DislikeHashes          []string `json:"dh,omitempty"`
	SexPrefHashes          []string `json:"sph,omitempty"`
	SeekingEducationHashes []string `json:"seh,omitempty"`
	SeekingIndustryHashes  []string `json:"sih,omitempty"`
	SeekingSexPrefHashes   []string `json:"ssph,omitempty"`

	Nickname    string   `json:"nick,omitempty"`
	AgeOpen     string   `json:"ao,omitempty"`
	BioOpen     string   `json:"bio,omitempty"`
	HobbiesOpen []string `json:"hbo,omitempty"`
	PrefsOpen   []string `json:"pfo,omitempty"`
	SexOpen     []string `json:"spo,omitempty"`
	EduOpen     string   `json:"edo,omitempty"`
	JobOpen     string   `json:"jbo,omitempty"`
	IndOpen     string   `json:"ino,omitempty"`
	ImageHashes []string `json:"imgs,omitempty"`
	VideoHashes []string `json:"vids,omitempty"`
}

// Seal verschlüsselt alle nicht zum Finden nötigen Felder der Anzeige mit key
// und leert sie im Klartext.
func (ad *PublicAd) Seal(key []byte) error {
	g := publicAdGeheim{
		EducationHash: ad.EducationHash, IndustryHash: ad.IndustryHash,
		HobbyHashes: ad.HobbyHashes, PrefHashes: ad.PrefHashes, DislikeHashes: ad.DislikeHashes,
		SexPrefHashes: ad.SexPrefHashes, SeekingEducationHashes: ad.SeekingEducationHashes,
		SeekingIndustryHashes: ad.SeekingIndustryHashes, SeekingSexPrefHashes: ad.SeekingSexPrefHashes,
		Nickname: ad.Nickname, AgeOpen: ad.AgeOpen, BioOpen: ad.BioOpen,
		HobbiesOpen: ad.HobbiesOpen, PrefsOpen: ad.PrefsOpen, SexOpen: ad.SexOpen,
		EduOpen: ad.EduOpen, JobOpen: ad.JobOpen, IndOpen: ad.IndOpen,
		ImageHashes: ad.ImageHashes, VideoHashes: ad.VideoHashes,
	}
	plain, err := json.Marshal(g)
	if err != nil {
		return err
	}
	sealed, err := sealBytes(plain, key)
	if err != nil {
		return err
	}
	ad.EducationHash, ad.IndustryHash = "", ""
	ad.HobbyHashes, ad.PrefHashes, ad.DislikeHashes, ad.SexPrefHashes = nil, nil, nil, nil
	ad.SeekingEducationHashes, ad.SeekingIndustryHashes, ad.SeekingSexPrefHashes = nil, nil, nil
	ad.Nickname, ad.AgeOpen, ad.BioOpen = "", "", ""
	ad.HobbiesOpen, ad.PrefsOpen, ad.SexOpen = nil, nil, nil
	ad.EduOpen, ad.JobOpen, ad.IndOpen = "", "", ""
	ad.ImageHashes, ad.VideoHashes = nil, nil
	ad.Sealed, ad.KeyID = sealed, KeyID(key)
	return nil
}

// Unseal entschlüsselt eine versiegelte Anzeige in place. Unversiegelt → nil.
func (ad *PublicAd) Unseal(key []byte) error {
	if ad.Sealed == "" {
		return nil
	}
	if ad.KeyID != "" && ad.KeyID != KeyID(key) {
		return ErrSealKey
	}
	plain, err := openBytes(ad.Sealed, key)
	if err != nil {
		return err
	}
	var g publicAdGeheim
	if err := json.Unmarshal(plain, &g); err != nil {
		return err
	}
	ad.EducationHash, ad.IndustryHash = g.EducationHash, g.IndustryHash
	ad.HobbyHashes, ad.PrefHashes, ad.DislikeHashes, ad.SexPrefHashes = g.HobbyHashes, g.PrefHashes, g.DislikeHashes, g.SexPrefHashes
	ad.SeekingEducationHashes, ad.SeekingIndustryHashes, ad.SeekingSexPrefHashes = g.SeekingEducationHashes, g.SeekingIndustryHashes, g.SeekingSexPrefHashes
	ad.Nickname, ad.AgeOpen, ad.BioOpen = g.Nickname, g.AgeOpen, g.BioOpen
	ad.HobbiesOpen, ad.PrefsOpen, ad.SexOpen = g.HobbiesOpen, g.PrefsOpen, g.SexOpen
	ad.EduOpen, ad.JobOpen, ad.IndOpen = g.EduOpen, g.JobOpen, g.IndOpen
	ad.ImageHashes, ad.VideoHashes = g.ImageHashes, g.VideoHashes
	ad.Sealed = ""
	return nil
}

// ── SearchableAd ─────────────────────────────────────────────────────────────

type searchAdGeheim struct {
	EducationGroup string   `json:"eg,omitempty"`
	IndustryGroup  string   `json:"ig,omitempty"`
	HobbyTags      []string `json:"ht,omitempty"`
	PrefTags       []string `json:"pt,omitempty"`
	SexPrefAbbrs   []string `json:"spa,omitempty"`
	Nickname       string   `json:"nick,omitempty"`
	Age            int      `json:"age,omitempty"`
	BioOpen        string   `json:"bio,omitempty"`
	ImageHashes    []string `json:"imgs,omitempty"`
	VideoHashes    []string `json:"vids,omitempty"`
}

// Seal: wie PublicAd.Seal für die Such-Anzeige.
func (ad *SearchableAd) Seal(key []byte) error {
	g := searchAdGeheim{
		EducationGroup: ad.EducationGroup, IndustryGroup: ad.IndustryGroup,
		HobbyTags: ad.HobbyTags, PrefTags: ad.PrefTags, SexPrefAbbrs: ad.SexPrefAbbrs,
		Nickname: ad.Nickname, Age: ad.Age, BioOpen: ad.BioOpen,
		ImageHashes: ad.ImageHashes, VideoHashes: ad.VideoHashes,
	}
	plain, err := json.Marshal(g)
	if err != nil {
		return err
	}
	sealed, err := sealBytes(plain, key)
	if err != nil {
		return err
	}
	ad.EducationGroup, ad.IndustryGroup = "", ""
	ad.HobbyTags, ad.PrefTags, ad.SexPrefAbbrs = nil, nil, nil
	ad.Nickname, ad.Age, ad.BioOpen = "", 0, ""
	ad.ImageHashes, ad.VideoHashes = nil, nil
	ad.Sealed, ad.KeyID = sealed, KeyID(key)
	return nil
}

// Unseal: wie PublicAd.Unseal für die Such-Anzeige.
func (ad *SearchableAd) Unseal(key []byte) error {
	if ad.Sealed == "" {
		return nil
	}
	if ad.KeyID != "" && ad.KeyID != KeyID(key) {
		return ErrSealKey
	}
	plain, err := openBytes(ad.Sealed, key)
	if err != nil {
		return err
	}
	var g searchAdGeheim
	if err := json.Unmarshal(plain, &g); err != nil {
		return err
	}
	ad.EducationGroup, ad.IndustryGroup = g.EducationGroup, g.IndustryGroup
	ad.HobbyTags, ad.PrefTags, ad.SexPrefAbbrs = g.HobbyTags, g.PrefTags, g.SexPrefAbbrs
	ad.Nickname, ad.Age, ad.BioOpen = g.Nickname, g.Age, g.BioOpen
	ad.ImageHashes, ad.VideoHashes = g.ImageHashes, g.VideoHashes
	ad.Sealed = ""
	return nil
}
