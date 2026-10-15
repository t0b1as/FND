package api

// Kategoriespezifische Eigenschaften (R652).
//
// Fahrzeuge und Immobilien lassen sich nicht sinnvoll über Titel und Preis
// beschreiben: Wer ein Auto sucht, filtert nach Kilometerstand, Erstzulassung
// und Kraftstoff; wer eine Wohnung sucht, nach Fläche, Zimmern und Baujahr.
// Die Felder orientieren sich an dem, was in Deutschland üblich ist – also an
// den Angaben, die auch Fahrzeug- und Immobilienportale abfragen.
//
// EIN Schema für alles: Formular, Filter und Sortierung entstehen daraus. So
// kann keine Seite Felder kennen, die eine andere nicht kennt.

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// attrFeld beschreibt eine Eigenschaft.
type attrFeld struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`               // "number" | "select" | "text" | "bool"
	Unit     string   `json:"unit,omitempty"`     // z.B. "km", "m²", "kW"
	Options  []string `json:"options,omitempty"`  // bei "select"
	Filter   bool     `json:"filter,omitempty"`   // im Suchfilter anbieten
	Sort     bool     `json:"sort,omitempty"`     // als Sortierung anbieten
	Required bool     `json:"required,omitempty"`
	Group    string   `json:"group,omitempty"`    // Überschrift im Formular
}

// autoMarken: gängige Marken auf dem deutschen Markt (R653). Eine Auswahl
// statt Freitext, damit „VW", „Volkswagen" und „vw" nicht drei verschiedene
// Dinge sind – sonst findet kein Filter etwas. „Andere" fängt den Rest ab.
var autoMarken = []string{
	"Abarth", "Alfa Romeo", "Aprilia", "Audi", "BMW", "BYD", "Chevrolet", "Chrysler",
	"Citroën", "Cupra", "Dacia", "Daihatsu", "Ducati", "Fiat", "Ford", "Harley-Davidson",
	"Honda", "Hyundai", "Isuzu", "Iveco", "Jaguar", "Jeep", "Kawasaki", "Kia", "KTM",
	"Lada", "Lancia", "Land Rover", "Lexus", "MAN", "Mazda", "Mercedes-Benz", "MG",
	"MINI", "Mitsubishi", "Nissan", "Opel", "Peugeot", "Piaggio", "Polestar", "Porsche",
	"Renault", "Rover", "Saab", "Seat", "Škoda", "Smart", "SsangYong", "Subaru", "Suzuki",
	"Tesla", "Toyota", "Triumph", "Vespa", "Volkswagen", "Volvo", "Yamaha", "Andere",
}

// baujahre liefert die Jahre von heute rückwärts bis ab – als Auswahl, damit
// keine Tippfehler wie "20215" entstehen (R655).
func baujahre(ab int) []string {
	jetzt := time.Now().Year() + 1 // Modelljahr des Folgejahres gibt es schon
	out := make([]string, 0, jetzt-ab+1)
	for j := jetzt; j >= ab; j-- {
		out = append(out, strconv.Itoa(j))
	}
	return out
}

// listingAttrSchema: Eigenschaften je Kategorie.
var listingAttrSchema = map[string][]attrFeld{
	"Fahrzeug": {
		{Key: "vehicle_type", Label: "Fahrzeugart", Type: "select", Group: "Fahrzeug", Filter: true,
			Options: []string{"PKW", "Motorrad", "Wohnmobil", "Transporter", "Anhänger", "LKW", "Fahrrad", "E-Bike", "Roller", "Sonstiges"}},
		{Key: "make", Label: "Marke", Type: "select", Group: "Fahrzeug", Filter: true, Options: autoMarken},
		// Modell bleibt Freitext: Die Modellpalette ändert sich ständig und
		// hängt an der Marke. Eine feste Liste wäre nach einem Jahr veraltet und
		// würde neue Modelle ausschließen (R653).
		{Key: "model", Label: "Modell", Type: "text", Group: "Fahrzeug", Filter: true},
		// Erstzulassung als Monat UND Jahr in EINEM Feld (R655): zwei getrennte
		// Felder konnten durch den Zeilenumbruch auseinandergerissen werden,
		// und ein einzelnes "Monat" ohne Bezug ist nicht verständlich.
		// Gespeichert wird in first_reg (Jahr, sortierbar) und
		// first_reg_month – so bleibt beides vergleichbar.
		{Key: "first_reg", Label: "Erstzulassung", Type: "monthyear", Group: "Fahrzeug",
			Filter: true, Sort: true, Options: baujahre(1950)},
		{Key: "mileage_km", Label: "Kilometerstand", Type: "number", Unit: "km", Group: "Fahrzeug", Filter: true, Sort: true},
		{Key: "power_kw", Label: "Leistung", Type: "number", Unit: "kW", Group: "Technik", Filter: true, Sort: true},
		{Key: "displacement_ccm", Label: "Hubraum", Type: "number", Unit: "cm³", Group: "Technik"},
		{Key: "fuel", Label: "Kraftstoff", Type: "select", Group: "Technik", Filter: true,
			Options: []string{"Benzin", "Diesel", "Elektro", "Hybrid", "Plug-in-Hybrid", "Erdgas", "Autogas", "Wasserstoff", "Sonstiges"}},
		{Key: "gearbox", Label: "Getriebe", Type: "select", Group: "Technik", Filter: true,
			Options: []string{"Schaltgetriebe", "Automatik", "Halbautomatik"}},
		{Key: "drive", Label: "Antrieb", Type: "select", Group: "Technik",
			Options: []string{"Vorderrad", "Hinterrad", "Allrad"}},
		{Key: "doors", Label: "Türen", Type: "number", Group: "Technik"},
		{Key: "seats", Label: "Sitzplätze", Type: "number", Group: "Technik"},
		{Key: "color", Label: "Farbe", Type: "select", Group: "Ausstattung", Filter: true,
			Options: []string{"Schwarz", "Weiß", "Silber", "Grau", "Blau", "Rot", "Grün",
				"Gelb", "Orange", "Braun", "Beige", "Gold", "Violett", "Andere"}},
		{Key: "emission_class", Label: "Schadstoffklasse", Type: "select", Group: "Zustand",
			Options: []string{"Euro 1", "Euro 2", "Euro 3", "Euro 4", "Euro 5", "Euro 6", "Euro 6d", "Elektro"}},
		{Key: "hu_until", Label: "HU gültig bis", Type: "number", Unit: "Jahr", Group: "Zustand", Filter: true, Sort: true},
		{Key: "owners", Label: "Fahrzeughalter", Type: "number", Group: "Zustand"},
		{Key: "accident_free", Label: "Unfallfrei", Type: "bool", Group: "Zustand", Filter: true},
		{Key: "roadworthy", Label: "Fahrbereit", Type: "bool", Group: "Zustand"},
		{Key: "consumption", Label: "Verbrauch", Type: "number", Unit: "l/100 km", Group: "Technik"},
		{Key: "range_km", Label: "Reichweite (elektrisch)", Type: "number", Unit: "km", Group: "Technik"},
		{Key: "battery_kwh", Label: "Batteriekapazität", Type: "number", Unit: "kWh", Group: "Technik"},

		// Ausstattung (R656): die üblichen Merkmale als Haken, nach Bereichen
		// gegliedert. Alle filterbar – danach wird beim Autokauf gesucht.
		{Key: "eq_klimaanlage", Label: "Klimaanlage", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_klimaautomatik", Label: "Klimaautomatik", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_sitzheizung", Label: "Sitzheizung", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_lenkradheizung", Label: "Lenkradheizung", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_standheizung", Label: "Standheizung", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_elektr_fensterheber", Label: "Elektr. Fensterheber", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_elektr_sitzverstellung", Label: "Elektr. Sitzverstellung", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_massagesitze", Label: "Massagesitze", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_ledersitze", Label: "Ledersitze", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_sportsitze", Label: "Sportsitze", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_zentralverriegelung", Label: "Zentralverriegelung", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_keyless_go", Label: "Keyless Go", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_elektr_heckklappe", Label: "Elektr. Heckklappe", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_servolenkung", Label: "Servolenkung", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_schiebedach", Label: "Schiebedach", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_panoramadach", Label: "Panoramadach", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_anhaengerkupplung", Label: "Anhängerkupplung", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_dachreling", Label: "Dachreling", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_alufelgen", Label: "Alufelgen", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_isofix", Label: "Isofix", Type: "bool", Group: "Komfort", Filter: true},
		{Key: "eq_navigationssystem", Label: "Navigationssystem", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_bluetooth", Label: "Bluetooth", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_freisprecheinrichtung", Label: "Freisprecheinrichtung", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_apple_carplay", Label: "Apple CarPlay", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_android_auto", Label: "Android Auto", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_dab_radio", Label: "DAB-Radio", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_soundsystem", Label: "Soundsystem", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_head_up_display", Label: "Head-up-Display", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_usb", Label: "USB", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_induktive_ladestation", Label: "Induktive Ladestation", Type: "bool", Group: "Multimedia", Filter: true},
		{Key: "eq_tempomat", Label: "Tempomat", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_abstandstempomat", Label: "Abstandstempomat", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_einparkhilfe", Label: "Einparkhilfe", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_rueckfahrkamera", Label: "Rückfahrkamera", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_360_kamera", Label: "360°-Kamera", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_spurhalteassistent", Label: "Spurhalteassistent", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_totwinkelassistent", Label: "Totwinkelassistent", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_notbremsassistent", Label: "Notbremsassistent", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_verkehrszeichenerkennung", Label: "Verkehrszeichenerkennung", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_muedigkeitserkennung", Label: "Müdigkeitserkennung", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_berganfahrhilfe", Label: "Berganfahrhilfe", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_esp", Label: "ESP", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_abs", Label: "ABS", Type: "bool", Group: "Assistenz", Filter: true},
		{Key: "eq_led_scheinwerfer", Label: "LED-Scheinwerfer", Type: "bool", Group: "Licht und Sicht", Filter: true},
		{Key: "eq_xenon", Label: "Xenon", Type: "bool", Group: "Licht und Sicht", Filter: true},
		{Key: "eq_kurvenlicht", Label: "Kurvenlicht", Type: "bool", Group: "Licht und Sicht", Filter: true},
		{Key: "eq_nebelscheinwerfer", Label: "Nebelscheinwerfer", Type: "bool", Group: "Licht und Sicht", Filter: true},
		{Key: "eq_regensensor", Label: "Regensensor", Type: "bool", Group: "Licht und Sicht", Filter: true},
		{Key: "eq_lichtsensor", Label: "Lichtsensor", Type: "bool", Group: "Licht und Sicht", Filter: true},
		{Key: "eq_beheizbare_frontscheibe", Label: "Beheizbare Frontscheibe", Type: "bool", Group: "Licht und Sicht", Filter: true},
		{Key: "eq_getoente_scheiben", Label: "Getönte Scheiben", Type: "bool", Group: "Licht und Sicht", Filter: true},
		{Key: "eq_nichtraucherfahrzeug", Label: "Nichtraucherfahrzeug", Type: "bool", Group: "Zusatz", Filter: true},
		{Key: "eq_scheckheftgepflegt", Label: "Scheckheftgepflegt", Type: "bool", Group: "Zusatz", Filter: true},
		{Key: "eq_garantie", Label: "Garantie", Type: "bool", Group: "Zusatz", Filter: true},
		{Key: "eq_winterreifen", Label: "Winterreifen", Type: "bool", Group: "Zusatz", Filter: true},
		{Key: "eq_sommerreifen", Label: "Sommerreifen", Type: "bool", Group: "Zusatz", Filter: true},
		{Key: "eq_allwetterreifen", Label: "Allwetterreifen", Type: "bool", Group: "Zusatz", Filter: true},
		{Key: "eq_behindertengerecht", Label: "Behindertengerecht", Type: "bool", Group: "Zusatz", Filter: true},
		{Key: "eq_taxi_mietwagen", Label: "Taxi/Mietwagen", Type: "bool", Group: "Zusatz", Filter: true},
	},
	"Immobilie": {
		{Key: "property_type", Label: "Objektart", Type: "select", Group: "Objekt", Filter: true,
			Options: []string{"Wohnung", "Haus", "Grundstück", "Gewerbe", "Garage/Stellplatz", "Ferienobjekt", "Sonstiges"}},
		{Key: "deal_type", Label: "Angebotsart", Type: "select", Group: "Objekt", Filter: true,
			Options: []string{"Kauf", "Miete"}},
		{Key: "living_area_m2", Label: "Wohnfläche", Type: "number", Unit: "m²", Group: "Objekt", Filter: true, Sort: true},
		{Key: "land_area_m2", Label: "Grundstücksfläche", Type: "number", Unit: "m²", Group: "Objekt", Filter: true, Sort: true},
		{Key: "rooms", Label: "Zimmer", Type: "number", Group: "Objekt", Filter: true, Sort: true},
		{Key: "bedrooms", Label: "Schlafzimmer", Type: "number", Group: "Objekt"},
		{Key: "bathrooms", Label: "Badezimmer", Type: "number", Group: "Objekt"},
		{Key: "floor", Label: "Etage", Type: "number", Group: "Objekt"},
		{Key: "year_built", Label: "Baujahr", Type: "select", Group: "Objekt", Filter: true, Sort: true,
			Options: baujahre(1850)},
		{Key: "condition", Label: "Zustand", Type: "select", Group: "Objekt",
			Options: []string{"Neubau", "Erstbezug", "Saniert", "Gepflegt", "Renovierungsbedürftig", "Abrissobjekt"}},
		{Key: "available_from", Label: "Verfügbar ab", Type: "select", Group: "Objekt",
			Options: []string{"sofort", "nach Vereinbarung", "1 Monat", "3 Monate", "6 Monate", "später"}},
		// Kosten: bei Miete die laufenden, bei Kauf die einmaligen.
		{Key: "cold_rent_fnd", Label: "Kaltmiete", Type: "number", Unit: "FND/Monat", Group: "Kosten", Filter: true, Sort: true},
		{Key: "extra_costs_fnd", Label: "Nebenkosten", Type: "number", Unit: "FND/Monat", Group: "Kosten"},
		{Key: "deposit_fnd", Label: "Kaution", Type: "number", Unit: "FND", Group: "Kosten"},
		{Key: "commission", Label: "Provision", Type: "select", Group: "Kosten", Filter: true,
			Options: []string{"provisionsfrei", "1 Monatsmiete", "2 Monatsmieten", "3 Monatsmieten",
				"3,57 % inkl. MwSt.", "7,14 % inkl. MwSt.", "nach Vereinbarung"}},
		// Energie – in Deutschland Pflichtangaben im Exposé.
		{Key: "heating", Label: "Heizungsart", Type: "select", Group: "Energie", Filter: true,
			Options: []string{"Zentralheizung", "Etagenheizung", "Fußbodenheizung", "Fernwärme", "Wärmepumpe", "Gas", "Öl", "Pellet", "Nachtspeicher", "Ofenheizung"}},
		{Key: "energy_kwh", Label: "Energiekennwert", Type: "number", Unit: "kWh/(m²·a)", Group: "Energie", Filter: true, Sort: true},
		{Key: "energy_class", Label: "Energieeffizienzklasse", Type: "select", Group: "Energie", Filter: true,
			Options: []string{"A+", "A", "B", "C", "D", "E", "F", "G", "H"}},
		{Key: "energy_cert", Label: "Energieausweis", Type: "select", Group: "Energie",
			Options: []string{"Verbrauchsausweis", "Bedarfsausweis", "liegt nicht vor"}},
		// Ausstattung
		{Key: "balcony", Label: "Balkon/Terrasse", Type: "bool", Group: "Ausstattung", Filter: true},
		{Key: "garden", Label: "Garten", Type: "bool", Group: "Ausstattung", Filter: true},
		{Key: "cellar", Label: "Keller", Type: "bool", Group: "Ausstattung"},
		{Key: "elevator", Label: "Aufzug", Type: "bool", Group: "Ausstattung"},
		{Key: "barrier_free", Label: "Barrierefrei", Type: "bool", Group: "Ausstattung", Filter: true},
		{Key: "parking", Label: "Stellplatz", Type: "select", Group: "Ausstattung",
			Options: []string{"keiner", "Außenstellplatz", "Carport", "Garage", "Tiefgarage", "Duplex"}},
		{Key: "furnished", Label: "Möbliert", Type: "bool", Group: "Ausstattung"},
		{Key: "pets_allowed", Label: "Haustiere erlaubt", Type: "bool", Group: "Ausstattung"},
	},
}

// GET /api/v1/listings/attrs – das Schema für die Formulare und Filter.
func (s *Server) listingAttrs(c *gin.Context) {
	if kat := strings.TrimSpace(c.Query("category")); kat != "" {
		c.JSON(http.StatusOK, gin.H{"category": kat, "fields": listingAttrSchema[kat]})
		return
	}
	c.JSON(http.StatusOK, gin.H{"schema": listingAttrSchema})
}

// attrFelderFuer liefert das Schema einer Kategorie (leer, wenn keines da ist).
func attrFelderFuer(kategorie string) []attrFeld {
	return listingAttrSchema[strings.TrimSpace(kategorie)]
}

// bereinigeAttrs übernimmt nur Felder, die das Schema kennt, und bringt sie in
// die passende Form. Alles andere fliegt raus – ein Angebot soll keine
// beliebigen Daten über den Umweg der Eigenschaften transportieren.
func bereinigeAttrs(kategorie string, roh map[string]any) map[string]any {
	felder := attrFelderFuer(kategorie)
	if len(felder) == 0 || roh == nil {
		return nil
	}
	out := map[string]any{}
	for _, f := range felder {
		v, ok := roh[f.Key]
		if !ok || v == nil {
			continue
		}
		switch f.Type {
		case "monthyear":
			// Jahr als Zahl, Monat getrennt daneben (R655).
			if z, ok := toFloatOK(v); ok && z > 1800 && z < 2200 {
				out[f.Key] = z
			}
			if m, ok := roh[f.Key+"_month"]; ok {
				if mz, ok := toFloatOK(m); ok && mz >= 1 && mz <= 12 {
					out[f.Key+"_month"] = mz
				}
			}
		case "number":
			if z, ok := toFloatOK(v); ok {
				out[f.Key] = z
			}
		case "bool":
			switch t := v.(type) {
			case bool:
				out[f.Key] = t
			case string:
				out[f.Key] = t == "true" || t == "1" || t == "ja"
			}
		case "select":
			sv, _ := v.(string)
			sv = strings.TrimSpace(sv)
			for _, o := range f.Options {
				if strings.EqualFold(o, sv) {
					out[f.Key] = o
					break
				}
			}
		default: // text
			sv, _ := v.(string)
			sv = strings.TrimSpace(sv)
			if len(sv) > 80 {
				sv = sv[:80]
			}
			if sv != "" {
				out[f.Key] = sv
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// attrWert holt eine Eigenschaft aus einem Angebot.
func attrWert(d map[string]any, key string) (any, bool) {
	if d == nil {
		return nil, false
	}
	a, _ := d["attrs"].(map[string]any)
	if a == nil {
		return nil, false
	}
	v, ok := a[key]
	return v, ok
}

// attrFilterAusQuery sammelt die Eigenschaftsfilter aus der HTTP-Anfrage.
// Erwartet werden Parameter der Form a_<key>, a_<key>_min und a_<key>_max.
func attrFilterAusQuery(c *gin.Context) map[string]string {
	out := map[string]string{}
	for name, werte := range c.Request.URL.Query() {
		if !strings.HasPrefix(name, "a_") || len(werte) == 0 {
			continue
		}
		wert := strings.TrimSpace(werte[0])
		if wert == "" {
			continue
		}
		out[strings.TrimPrefix(name, "a_")] = wert
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// attrFilterPasst prüft ein Angebot gegen die Eigenschaftsfilter.
// Schlüssel sind <key>, <key>_min oder <key>_max.
func attrFilterPasst(filter map[string]string, d map[string]any) bool {
	for key, wert := range filter {
		switch {
		case strings.HasSuffix(key, "_min"), strings.HasSuffix(key, "_max"):
			istMin := strings.HasSuffix(key, "_min")
			k := strings.TrimSuffix(strings.TrimSuffix(key, "_min"), "_max")
			grenze, err := strconv.ParseFloat(strings.ReplaceAll(wert, ",", "."), 64)
			if err != nil {
				continue
			}
			v, ok := attrWert(d, k)
			if !ok {
				return false // Angabe fehlt: passt nicht zu einer Zahlenvorgabe
			}
			z, ok := toFloatOK(v)
			if !ok || (istMin && z < grenze) || (!istMin && z > grenze) {
				return false
			}
		default:
			v, ok := attrWert(d, key)
			if !ok {
				return false
			}
			switch t := v.(type) {
			case bool:
				if (wert == "true" || wert == "1") != t {
					return false
				}
			case string:
				if !strings.Contains(strings.ToLower(t), strings.ToLower(wert)) {
					return false
				}
			default:
				if !strings.EqualFold(strings.TrimSpace(toStringLoose(v)), wert) {
					return false
				}
			}
		}
	}
	return true
}

func toStringLoose(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}
