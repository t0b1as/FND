package websuche

// robots.txt und TDM-Vorbehalt (R896).
//
// robots.txt: Gruppe „FundusBot“, sonst „*“; Allow/Disallow mit * und $,
// die längste passende Regel gilt; Crawl-delay wird beachtet (höchstens 60 s).
// Fehlt die Datei (4xx), ist alles erlaubt. Ist der Server gestört (5xx,
// keine Antwort), liest der Crawler von diesem Host vorerst nichts.
//
// TDM-Vorbehalt nach § 44b Abs. 3 UrhG in maschinenlesbarer Form, nach der
// W3C-Empfehlung TDMRep: /.well-known/tdmrep.json, HTTP-Kopf
// „tdm-reservation: 1“ oder <meta name="tdm-reservation" content="1">.
// Mit Vorbehalt wird die Seite nicht aufgenommen.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const BotName = "FundusBot"

type regel struct {
	erlaubt bool
	muster  string
	re      *regexp.Regexp
}

// Robots: ausgewertete robots.txt eines Hosts.
type Robots struct {
	regeln        []regel
	Verzoegerung  time.Duration
	AllesVerboten bool // Server gestört
}

func musterRegex(m string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	ende := strings.HasSuffix(m, "$")
	m = strings.TrimSuffix(m, "$")
	for i, teil := range strings.Split(m, "*") {
		if i > 0 {
			b.WriteString(".*")
		}
		b.WriteString(regexp.QuoteMeta(teil))
	}
	if ende {
		b.WriteString("$")
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

// RobotsLesen: robots.txt auswerten.
func RobotsLesen(text string) *Robots {
	type gruppe struct {
		agenten []string
		regeln  []regel
		delay   time.Duration
	}
	var gruppen []*gruppe
	var akt *gruppe
	letzteWarAgent := false
	for _, z := range strings.Split(text, "\n") {
		if i := strings.Index(z, "#"); i >= 0 {
			z = z[:i]
		}
		k, v, ok := strings.Cut(z, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "user-agent":
			if akt == nil || !letzteWarAgent {
				akt = &gruppe{}
				gruppen = append(gruppen, akt)
			}
			akt.agenten = append(akt.agenten, strings.ToLower(v))
			letzteWarAgent = true
			continue
		case "allow", "disallow":
			if akt != nil {
				if v == "" {
					if k == "disallow" { // „Disallow:“ leer = alles erlaubt
						break
					}
				} else if re := musterRegex(v); re != nil {
					akt.regeln = append(akt.regeln, regel{k == "allow", v, re})
				}
			}
		case "crawl-delay":
			if akt != nil {
				if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
					akt.delay = time.Duration(f * float64(time.Second))
				}
			}
		}
		letzteWarAgent = false
	}
	var gew *gruppe
	for _, g := range gruppen {
		for _, a := range g.agenten {
			if strings.Contains(a, strings.ToLower(BotName)) {
				gew = g
			}
		}
	}
	if gew == nil {
		for _, g := range gruppen {
			for _, a := range g.agenten {
				if a == "*" {
					gew = g
				}
			}
		}
	}
	r := &Robots{}
	if gew != nil {
		r.regeln = gew.regeln
		r.Verzoegerung = gew.delay
		if r.Verzoegerung > time.Minute {
			r.Verzoegerung = time.Minute
		}
	}
	return r
}

// Erlaubt: darf der Pfad (mit Abfrage) gelesen werden?
func (r *Robots) Erlaubt(pfad string) bool {
	if r == nil {
		return true
	}
	if r.AllesVerboten {
		return false
	}
	if pfad == "" {
		pfad = "/"
	}
	bestLaenge, erlaubt := -1, true
	for _, g := range r.regeln {
		if g.re.MatchString(pfad) {
			l := len(g.muster)
			if l > bestLaenge || (l == bestLaenge && g.erlaubt) {
				bestLaenge, erlaubt = l, g.erlaubt
			}
		}
	}
	return erlaubt
}

// TDMRep: /.well-known/tdmrep.json eines Hosts.
type TDMRep struct {
	eintraege []struct {
		re        *regexp.Regexp
		vorbehalt bool
	}
}

// TDMRepLesen: [{"location":"/*","tdm-reservation":1}, …]
func TDMRepLesen(b []byte) *TDMRep {
	var roh []map[string]any
	if json.Unmarshal(b, &roh) != nil {
		return nil
	}
	t := &TDMRep{}
	for _, e := range roh {
		loc, _ := e["location"].(string)
		if loc == "" {
			continue
		}
		vb := false
		switch v := e["tdm-reservation"].(type) {
		case float64:
			vb = v == 1
		case string:
			vb = strings.TrimSpace(v) == "1"
		case bool:
			vb = v
		}
		if re := musterRegex(loc); re != nil {
			t.eintraege = append(t.eintraege, struct {
				re        *regexp.Regexp
				vorbehalt bool
			}{re, vb})
		}
	}
	return t
}

// Vorbehalt: gilt für den Pfad ein Nutzungsvorbehalt? Der erste passende
// Eintrag entscheidet (wie in TDMRep beschrieben).
func (t *TDMRep) Vorbehalt(pfad string) bool {
	if t == nil {
		return false
	}
	for _, e := range t.eintraege {
		if e.re.MatchString(pfad) {
			return e.vorbehalt
		}
	}
	return false
}
