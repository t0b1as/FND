package partner_test

import (
	"strings"
	"testing"

	"github.com/fundus/node/internal/partner"
)

func key(b byte) []byte {
	k := make([]byte, partner.SealKeyLen)
	for i := range k {
		k[i] = b + byte(i)
	}
	return k
}

func sensibleProfile() *partner.Profile {
	p := baseProfile()
	p.Age = 29
	p.Bio = "Geheime Bio"
	p.Preferences = []string{"Ehrlichkeit"}
	p.SexualPrefs = []partner.SexualPreference{{Abbr: "BDSM", Role: "aktiv"}}
	p.Education = "master"
	p.Profession = "Ingenieurin"
	p.ImageHashes = []string{"aa11bb22"}
	return p
}

func TestSeal_PublicAd_NoSensitiveCleartext(t *testing.T) {
	ad, err := partner.MakePublicAd(sensibleProfile(), "peer", mustSalt(t))
	if err != nil {
		t.Fatal(err)
	}
	full, _ := ad.Marshal()
	if err := ad.Seal(key(1)); err != nil {
		t.Fatal(err)
	}
	b, _ := ad.Marshal()
	s := string(b)
	for _, geheim := range []string{"TestUser", "Geheime Bio", "Musik", "Ehrlichkeit", "BDSM",
		"master", "Ingenieurin", "aa11bb22", `"ao":`, `"sph":`, `"hh":`} {
		if strings.Contains(s, geheim) {
			t.Errorf("%q steht nach Seal noch im Klartext", geheim)
		}
	}
	// Zum Finden nötige Angaben bleiben offen.
	for _, offen := range []string{`"gh":`, `"ah":`, `"sgh":`, `"go":"female"`, `"aro":"26-35"`} {
		if !strings.Contains(s, offen) {
			t.Errorf("%q fehlt nach Seal", offen)
		}
	}
	// Rundreise
	var back partner.PublicAd
	_ = back.Unmarshal(b)
	if err := back.Unseal(key(1)); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if back.Nickname != "TestUser" || back.BioOpen != "Geheime Bio" || len(back.SexPrefHashes) != 1 || back.ImageHashes[0] != "aa11bb22" {
		t.Errorf("Rundreise unvollständig: %+v", back)
	}
	back.PublishedAt, back.ExpiresAt = ad.PublishedAt, ad.ExpiresAt
	nb, _ := back.Marshal()
	var a, c partner.PublicAd
	_ = a.Unmarshal(full)
	_ = c.Unmarshal(nb)
	if a.BioOpen != c.BioOpen || len(a.HobbyHashes) != len(c.HobbyHashes) {
		t.Error("entsiegelte Anzeige weicht vom Original ab")
	}
}

func TestSeal_WrongKeyFails(t *testing.T) {
	ad, _ := partner.MakePublicAd(sensibleProfile(), "peer", mustSalt(t))
	_ = ad.Seal(key(1))
	if err := ad.Unseal(key(2)); err == nil {
		t.Fatal("falscher Schlüssel muss scheitern")
	}
	if ad.Sealed == "" {
		t.Fatal("misslungenes Unseal darf die Anzeige nicht verändern")
	}
	// Ohne Schlüsselkennung (manipuliert) greift die AEAD-Prüfung.
	ad.KeyID = ""
	if err := ad.Unseal(key(2)); err == nil {
		t.Fatal("falscher Schlüssel ohne kid muss scheitern")
	}
}

func TestSeal_MatchStillWorksLocked(t *testing.T) {
	me, _ := partner.NewMatcher(baseProfile(), "me", mustSalt(t))
	ad, _ := partner.MakePublicAd(remoteProfile(), "remote", mustSalt(t))
	_ = ad.Seal(key(3))
	r, why := me.MatchWithReason(ad)
	if r == nil {
		t.Fatalf("versiegelte Anzeige muss über Geschlecht/Alter/Ort matchen (%s)", why)
	}
	if !r.Locked || r.Nickname != "" || r.AgeRange != "36-45" {
		t.Errorf("gesperrtes Match falsch: locked=%v nick=%q ar=%q", r.Locked, r.Nickname, r.AgeRange)
	}
	_ = ad.Unseal(key(3))
	r2 := me.Match(ad)
	if r2 == nil || r2.Locked || r2.Nickname != "RemoteUser" || r2.CommonInterests == 0 {
		t.Errorf("entsiegeltes Match unvollständig: %+v", r2)
	}
}

func TestSeal_SearchableAd(t *testing.T) {
	p := sensibleProfile()
	ad := partner.MakeSearchableAd(p, "peer")
	if err := ad.Seal(key(4)); err != nil {
		t.Fatal(err)
	}
	if ad.Nickname != "" || ad.Age != 0 || len(ad.SexPrefAbbrs) != 0 || ad.GenderCategory != "female" || ad.AgeRange != "26-35" {
		t.Fatalf("Such-Anzeige falsch versiegelt: %+v", ad)
	}
	s := partner.NewSearcher()
	// Ohne Filter auf versiegelte Angaben: gefunden, aber gesperrt.
	res, total, aus := s.SearchEx([]*partner.SearchableAd{ad}, partner.SearchFilter{AgeMin: 28, AgeMax: 30})
	if total != 1 || !res[0].Locked || aus != 0 {
		t.Fatalf("gesperrte Suche: total=%d aus=%d res=%+v", total, aus, res)
	}
	// Mit Vorlieben-Filter: ausgeblendet und gezählt.
	res, total, aus = s.SearchEx([]*partner.SearchableAd{ad}, partner.SearchFilter{SexPrefAbbrs: []string{"BDSM"}})
	if total != 0 || aus != 1 {
		t.Fatalf("Filter auf versiegelte Angaben: total=%d aus=%d", total, aus)
	}
	// Altersklasse außerhalb → nicht gefunden.
	if _, total, _ = s.SearchEx([]*partner.SearchableAd{ad}, partner.SearchFilter{AgeMin: 40}); total != 0 {
		t.Fatal("Altersklasse 26-35 darf bei ab 40 nicht passen")
	}
	// Entsiegelt: Filter greift wieder auf die echten Werte.
	if err := ad.Unseal(key(4)); err != nil {
		t.Fatal(err)
	}
	res, total, _ = s.SearchEx([]*partner.SearchableAd{ad}, partner.SearchFilter{SexPrefAbbrs: []string{"BDSM"}})
	if total != 1 || res[0].Locked || res[0].Nickname != "TestUser" || res[0].Age != 29 {
		t.Fatalf("entsiegelte Suche: total=%d res=%+v", total, res)
	}
}

// R940: Kategorie "offen für Vorlieben" und einzeln öffentlich gemachte Vorlieben.
func TestSeal_SexMatchOptIn(t *testing.T) {
	p := sensibleProfile() // BDSM aktiv, nicht öffentlich
	p.SexualPrefs = append(p.SexualPrefs, partner.SexualPreference{Abbr: "TAN", Role: "switch", Public: true})

	ad, _ := partner.MakePublicAd(p, "peer", mustSalt(t))
	_ = ad.Seal(key(5))
	b, _ := ad.Marshal()
	s := string(b)
	if strings.Contains(s, "BDSM") {
		t.Error("nicht freigeschaltete Vorliebe steht im Klartext")
	}
	if !strings.Contains(s, `"som":true`) || !strings.Contains(s, "TAN:switch") {
		t.Errorf("Kategorie/öffentliche Vorliebe fehlt: %s", s)
	}

	// Abgleich versiegelt: nur über die öffentliche Vorliebe.
	me := baseProfile()
	me.SexualPrefs = []partner.SexualPreference{{Abbr: "TAN", Role: "switch"}}
	m, _ := partner.NewMatcher(me, "me", mustSalt(t))
	remote := remoteProfile()
	remote.SexualPrefs = []partner.SexualPreference{{Abbr: "BDSM", Role: "aktiv"}, {Abbr: "TAN", Role: "switch", Public: true}}
	rad, _ := partner.MakePublicAd(remote, "remote", mustSalt(t))
	_ = rad.Seal(key(6))
	r := m.Match(rad)
	if r == nil || !r.Locked || !r.SexMatch || len(r.SexPublic) != 1 || r.CommonInterests != 1 {
		t.Fatalf("versiegelter Vorlieben-Abgleich falsch: %+v", r)
	}

	// Kategorie ausgeblendet, nichts öffentlich → gar nichts sichtbar.
	q := sensibleProfile()
	q.SexMatchHidden = true
	qad, _ := partner.MakePublicAd(q, "peer", mustSalt(t))
	_ = qad.Seal(key(7))
	qb, _ := qad.Marshal()
	if strings.Contains(string(qb), `"som"`) || strings.Contains(string(qb), `"spp"`) {
		t.Errorf("verborgene Kategorie sichtbar: %s", qb)
	}

	// Suche: öffentliche Vorliebe versiegelt findbar, nicht-öffentliche nicht.
	sad := partner.MakeSearchableAd(p, "peer")
	_ = sad.Seal(key(8))
	srch := partner.NewSearcher()
	res, total, aus := srch.SearchEx([]*partner.SearchableAd{sad}, partner.SearchFilter{SexPrefAbbrs: []string{"TAN"}})
	if total != 1 || !res[0].Locked || !res[0].SexMatch || len(res[0].SexPublic) != 1 || aus != 0 {
		t.Fatalf("Suche nach öffentlicher Vorliebe: total=%d aus=%d %+v", total, aus, res)
	}
	if _, total, aus = srch.SearchEx([]*partner.SearchableAd{sad}, partner.SearchFilter{SexPrefAbbrs: []string{"BDSM"}}); total != 0 || aus != 1 {
		t.Fatalf("nicht-öffentliche Vorliebe darf nicht findbar sein: total=%d aus=%d", total, aus)
	}
	// Nur "offen für Vorlieben"
	qs := partner.MakeSearchableAd(q, "peer2")
	_ = qs.Seal(key(9))
	if _, total, _ = srch.SearchEx([]*partner.SearchableAd{sad, qs}, partner.SearchFilter{SexMatchOnly: true}); total != 1 {
		t.Fatalf("Filter nur offen für Vorlieben: total=%d", total)
	}
}
