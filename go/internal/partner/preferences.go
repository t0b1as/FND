package partner

// =============================================================================
//  Vordefinierte Listen – alle erweiterbar durch den User
//
//  Jede Liste enthält bewährte Optionen als Ausgangspunkt.
//  Der User kann per API beliebige zusätzliche Werte hinzufügen.
//  Im PublicAd erscheinen nur Hashes, niemals Klartext.
// =============================================================================

// EducationLevel definiert gängige Ausbildungsgrade.
type EducationLevel string

const (
	EduSchool      EducationLevel = "hauptschule"
	EduMittel      EducationLevel = "mittlere_reife"
	EduAbitur      EducationLevel = "abitur"
	EduAusbildung  EducationLevel = "berufsausbildung"
	EduBachelor    EducationLevel = "bachelor"
	EduMaster      EducationLevel = "master"
	EduDiplom      EducationLevel = "diplom"
	EduDoktor      EducationLevel = "doktor"
	EduProfessor   EducationLevel = "professor"
	EduSelbst      EducationLevel = "selbststudium"
)

// DefaultEducationLevels ist die vordefinierte Auswahl mit Anzeigetiteln.
var DefaultEducationLevels = []LabeledValue{
	{Value: "hauptschule",      LabelDE: "Hauptschulabschluss",       LabelEN: "Secondary school"},
	{Value: "mittlere_reife",   LabelDE: "Mittlere Reife / Realschule", LabelEN: "Middle school certificate"},
	{Value: "abitur",           LabelDE: "Abitur / Fachabitur",       LabelEN: "A-levels / High school diploma"},
	{Value: "berufsausbildung", LabelDE: "Berufsausbildung",          LabelEN: "Vocational training"},
	{Value: "bachelor",         LabelDE: "Bachelor",                  LabelEN: "Bachelor's degree"},
	{Value: "master",           LabelDE: "Master",                    LabelEN: "Master's degree"},
	{Value: "diplom",           LabelDE: "Diplom / Staatsexamen",     LabelEN: "Diploma / State exam"},
	{Value: "doktor",           LabelDE: "Doktortitel (Dr.)",         LabelEN: "Doctorate (PhD)"},
	{Value: "professor",        LabelDE: "Professur (Prof.)",         LabelEN: "Professorship"},
	{Value: "selbststudium",    LabelDE: "Autodidakt / Selbststudium", LabelEN: "Self-taught"},
}

// DefaultHobbies – häufige Freizeitinteressen.
var DefaultHobbies = []LabeledValue{
	{Value: "musik",         LabelDE: "Musik hören / spielen",    LabelEN: "Music"},
	{Value: "sport",         LabelDE: "Sport / Fitness",          LabelEN: "Sports / Fitness"},
	{Value: "wandern",       LabelDE: "Wandern / Trekking",       LabelEN: "Hiking / Trekking"},
	{Value: "kochen",        LabelDE: "Kochen / Backen",          LabelEN: "Cooking / Baking"},
	{Value: "reisen",        LabelDE: "Reisen",                   LabelEN: "Travelling"},
	{Value: "lesen",         LabelDE: "Lesen",                    LabelEN: "Reading"},
	{Value: "fotografie",    LabelDE: "Fotografie",               LabelEN: "Photography"},
	{Value: "film",          LabelDE: "Film & Kino",              LabelEN: "Film & Cinema"},
	{Value: "gaming",        LabelDE: "Gaming / E-Sports",        LabelEN: "Gaming / E-Sports"},
	{Value: "kunst",         LabelDE: "Kunst / Malen / Zeichnen", LabelEN: "Art / Painting"},
	{Value: "theater",       LabelDE: "Theater / Oper",           LabelEN: "Theatre / Opera"},
	{Value: "tanzen",        LabelDE: "Tanzen",                   LabelEN: "Dancing"},
	{Value: "yoga",          LabelDE: "Yoga / Meditation",        LabelEN: "Yoga / Meditation"},
	{Value: "fahrrad",       LabelDE: "Fahrrad / Radsport",       LabelEN: "Cycling"},
	{Value: "schwimmen",     LabelDE: "Schwimmen / Wassersport",  LabelEN: "Swimming / Water sports"},
	{Value: "gartenarbeit",  LabelDE: "Gartenarbeit",             LabelEN: "Gardening"},
	{Value: "handwerk",      LabelDE: "Handwerk / DIY",           LabelEN: "Crafts / DIY"},
	{Value: "natur",         LabelDE: "Natur & Umwelt",           LabelEN: "Nature & Environment"},
	{Value: "ehrenamt",      LabelDE: "Ehrenamt / Soziales",      LabelEN: "Volunteering"},
	{Value: "spiritualitaet",LabelDE: "Spiritualität / Religion", LabelEN: "Spirituality / Religion"},
}

// DefaultIndustries – Branchen.
var DefaultIndustries = []LabeledValue{
	{Value: "it",            LabelDE: "IT / Software / Tech",         LabelEN: "IT / Software / Tech"},
	{Value: "gesundheit",    LabelDE: "Gesundheit / Medizin",         LabelEN: "Health / Medicine"},
	{Value: "bildung",       LabelDE: "Bildung / Wissenschaft",       LabelEN: "Education / Science"},
	{Value: "finanzen",      LabelDE: "Finanzen / Banken / Versicherung", LabelEN: "Finance / Banking"},
	{Value: "handel",        LabelDE: "Handel / Einzelhandel",        LabelEN: "Retail / Trade"},
	{Value: "gastronomie",   LabelDE: "Gastronomie / Tourismus",      LabelEN: "Hospitality / Tourism"},
	{Value: "bau",           LabelDE: "Bau / Architektur",            LabelEN: "Construction / Architecture"},
	{Value: "produktion",    LabelDE: "Produktion / Fertigung",       LabelEN: "Manufacturing"},
	{Value: "logistik",      LabelDE: "Logistik / Transport",         LabelEN: "Logistics / Transport"},
	{Value: "medien",        LabelDE: "Medien / Kommunikation",       LabelEN: "Media / Communications"},
	{Value: "kunst_kultur",  LabelDE: "Kunst / Kultur / Unterhaltung", LabelEN: "Arts / Culture / Entertainment"},
	{Value: "recht",         LabelDE: "Recht / Justiz",               LabelEN: "Law / Justice"},
	{Value: "verwaltung",    LabelDE: "Öffentliche Verwaltung",       LabelEN: "Public administration"},
	{Value: "energie",       LabelDE: "Energie / Umwelt",             LabelEN: "Energy / Environment"},
	{Value: "landwirtschaft",LabelDE: "Landwirtschaft / Forst",       LabelEN: "Agriculture / Forestry"},
	{Value: "soziales",      LabelDE: "Soziale Arbeit / NGO",         LabelEN: "Social work / NGO"},
	{Value: "sport_fitness", LabelDE: "Sport / Fitness / Wellness",   LabelEN: "Sports / Fitness / Wellness"},
	{Value: "selbststaendig",LabelDE: "Selbstständig / Freelance",    LabelEN: "Self-employed / Freelance"},
	{Value: "rente",         LabelDE: "Rentner / Ruhestand",          LabelEN: "Retired"},
	{Value: "student",       LabelDE: "Student / Auszubildender",     LabelEN: "Student / Apprentice"},
}

// DefaultPreferences – Persönliche Vorlieben (Werte, Lebensweise).
var DefaultPreferences = []LabeledValue{
	{Value: "nichtraucher",    LabelDE: "Nichtraucher",               LabelEN: "Non-smoker"},
	{Value: "raucher",         LabelDE: "Raucher",                    LabelEN: "Smoker"},
	{Value: "vegan",           LabelDE: "Vegan",                      LabelEN: "Vegan"},
	{Value: "vegetarisch",     LabelDE: "Vegetarisch",                LabelEN: "Vegetarian"},
	{Value: "haustiere",       LabelDE: "Mag Haustiere",              LabelEN: "Pet-friendly"},
	{Value: "kinder_ja",       LabelDE: "Kinder willkommen",          LabelEN: "Children welcome"},
	{Value: "fruehaufsteher",  LabelDE: "Frühaufsteher",              LabelEN: "Early riser"},
	{Value: "nachtmensch",     LabelDE: "Nachtmensch",                LabelEN: "Night owl"},
	{Value: "stadtleben",      LabelDE: "Stadtleben",                 LabelEN: "City life"},
	{Value: "landleben",       LabelDE: "Landleben / Natur",          LabelEN: "Rural / Nature"},
	{Value: "sport_aktiv",     LabelDE: "Sportlich aktiv",            LabelEN: "Physically active"},
	{Value: "romantisch",      LabelDE: "Romantisch",                 LabelEN: "Romantic"},
	{Value: "abenteuerlustig", LabelDE: "Abenteuerlustig",            LabelEN: "Adventurous"},
	{Value: "haeuslich",       LabelDE: "Häuslich / Gemütlich",       LabelEN: "Homebody / Cosy"},
	{Value: "reiselustig",     LabelDE: "Reisefreudig",               LabelEN: "Love to travel"},
	{Value: "spirituell",      LabelDE: "Spirituell",                 LabelEN: "Spiritual"},
	{Value: "politisch_engagiert", LabelDE: "Politisch engagiert",   LabelEN: "Politically engaged"},
	{Value: "umweltbewusst",   LabelDE: "Umweltbewusst",             LabelEN: "Environmentally conscious"},
	{Value: "sozial",          LabelDE: "Sozial / Ehrenamtlich",      LabelEN: "Social / Volunteer"},
	{Value: "familienmensch",  LabelDE: "Familienmensch",             LabelEN: "Family-oriented"},
	// Charakterliche Werte
	{Value: "ehrlichkeit",     LabelDE: "Ehrlichkeit",                LabelEN: "Honesty"},
	{Value: "kreativitaet",    LabelDE: "Kreativität",                LabelEN: "Creativity"},
	{Value: "respekt",         LabelDE: "Respekt",                    LabelEN: "Respect"},
	{Value: "achtsamkeit",     LabelDE: "Achtsamkeit",                LabelEN: "Mindfulness"},
	{Value: "ehre",            LabelDE: "Ehre",                       LabelEN: "Honour"},
	{Value: "loyalitaet",      LabelDE: "Loyalität",                  LabelEN: "Loyalty"},
	{Value: "humor",           LabelDE: "Humor",                      LabelEN: "Humour"},
	{Value: "empathie",        LabelDE: "Empathie",                   LabelEN: "Empathy"},
	{Value: "offenheit",       LabelDE: "Offenheit",                  LabelEN: "Openness"},
	{Value: "verlaesslichkeit",LabelDE: "Verlässlichkeit",            LabelEN: "Reliability"},
	{Value: "toleranz",        LabelDE: "Toleranz",                   LabelEN: "Tolerance"},
	{Value: "gerechtigkeit",   LabelDE: "Gerechtigkeit",              LabelEN: "Justice"},
}

// DefaultDislikes – Abneigungen.
var DefaultDislikes = []LabeledValue{
	{Value: "rauchen_ab",       LabelDE: "Rauchen",                   LabelEN: "Smoking"},
	{Value: "alkohol_ab",       LabelDE: "Starker Alkoholkonsum",     LabelEN: "Heavy drinking"},
	{Value: "drogen_ab",        LabelDE: "Drogenkonsum",              LabelEN: "Drug use"},
	{Value: "laerm_ab",         LabelDE: "Lärm / Krawall",            LabelEN: "Noise / Chaos"},
	{Value: "unordnung_ab",     LabelDE: "Unordnung",                 LabelEN: "Messiness"},
	{Value: "unpuenktlich_ab",  LabelDE: "Unpünktlichkeit",           LabelEN: "Tardiness"},
	{Value: "handy_ab",         LabelDE: "Ständiges Handy-Starren",   LabelEN: "Constant phone use"},
	{Value: "negativ_ab",       LabelDE: "Negativität / Pessimismus", LabelEN: "Negativity / Pessimism"},
	{Value: "eifersucht_ab",    LabelDE: "Eifersucht",                LabelEN: "Jealousy"},
	{Value: "kontrollzwang_ab", LabelDE: "Kontrollverhalten",         LabelEN: "Controlling behaviour"},
	{Value: "ungeduld_ab",      LabelDE: "Ungeduld",                  LabelEN: "Impatience"},
	{Value: "fastfood_ab",      LabelDE: "Fast Food",                 LabelEN: "Fast food"},
	{Value: "tierquaelerei_ab", LabelDE: "Tierquälerei",              LabelEN: "Animal cruelty"},
	{Value: "luegen_ab",        LabelDE: "Unehrlichkeit / Lügen",     LabelEN: "Dishonesty / Lying"},
	{Value: "drama_ab",         LabelDE: "Drama / Theatralik",        LabelEN: "Drama / Theatrics"},
}

// =============================================================================
//  Sexuelle Vorlieben – basierend auf gängigen Community-Abkürzungen
//
//  Aktiv = ich biete / nehme die aktive Rolle ein
//  Passiv = ich nehme / nehme die passive Rolle ein
//  Switch = beides willkommen
// =============================================================================

// SexPrefRole beschreibt die Rolle bei einer sexuellen Präferenz.
type SexPrefRole string

const (
	RoleActive  SexPrefRole = "aktiv"   // gibt / top / dominant
	RolePassive SexPrefRole = "passiv"  // empfängt / bottom / submissiv
	RoleSwitch  SexPrefRole = "switch"  // beides
)

// SexualPreference beschreibt eine sexuelle Vorliebe mit Abkürzung und Erklärung.
type SexualPreference struct {
	// Abkürzung (wird für Hashing genutzt, eindeutiger key)
	Abbr    string `json:"abbr"`
	// Gewählte Rolle
	Role    SexPrefRole `json:"role"`
}

// SexPrefDefinition beschreibt eine Kategorie sexueller Vorlieben.
type SexPrefDefinition struct {
	Abbr      string `json:"Abbr"`
	LabelDE   string `json:"LabelDE"`
	LabelEN   string `json:"LabelEN"`
	ExplainDE string `json:"ExplainDE"`
	ExplainEN string `json:"ExplainEN"`
	HasRoles  bool   `json:"HasRoles"`
}

// DefaultSexualPreferences – Community-Abkürzungen mit Erklärungen.
// Sortiert von verbreitet/vanilla zu spezifischer.
var DefaultSexualPreferences = []SexPrefDefinition{
	{
		Abbr: "VAN", LabelDE: "Vanilla", LabelEN: "Vanilla",
		ExplainDE: "Klassischer Sex ohne besondere Praktiken oder Rollenspiele.",
		ExplainEN: "Conventional sex without specific kinks or role-play.",
		HasRoles: false,
	},
	{
		Abbr: "ORL", LabelDE: "Oralsex (allgemein)", LabelEN: "Oral sex (general)",
		ExplainDE: "Orale Stimulation – Oberbegriff für alle Varianten.",
		ExplainEN: "Oral stimulation – umbrella term for all variants.",
		HasRoles: true,
	},
	{
		Abbr: "Zva", LabelDE: "Cunnilingus (aktiv)", LabelEN: "Cunnilingus (active)",
		ExplainDE: "Orale Stimulation der Vagina / Vulva durch die Zunge – aktive/gebende Rolle.",
		ExplainEN: "Oral stimulation of the vagina / vulva with the tongue – active/giving role.",
		HasRoles: false,
	},
	{
		Abbr: "Zvp", LabelDE: "Cunnilingus (passiv)", LabelEN: "Cunnilingus (passive)",
		ExplainDE: "Orale Stimulation der Vagina / Vulva durch die Zunge – passive/empfangende Rolle.",
		ExplainEN: "Oral stimulation of the vagina / vulva with the tongue – passive/receiving role.",
		HasRoles: false,
	},
	{
		Abbr: "Zaa", LabelDE: "Analingus (aktiv)", LabelEN: "Analingus (active)",
		ExplainDE: "Orale / zungenbetonte Stimulation der Analzone – aktive/gebende Rolle.",
		ExplainEN: "Oral / tongue stimulation of the anal area – active/giving role.",
		HasRoles: false,
	},
	{
		Abbr: "Zap", LabelDE: "Analingus (passiv)", LabelEN: "Analingus (passive)",
		ExplainDE: "Orale / zungenbetonte Stimulation der Analzone – passive/empfangende Rolle.",
		ExplainEN: "Oral / tongue stimulation of the anal area – passive/receiving role.",
		HasRoles: false,
	},
	{
		Abbr: "KS", LabelDE: "Küssen / Körperkontakt", LabelEN: "Kissing / Body contact",
		ExplainDE: "Intensives Küssen und ganzkörperlicher Kontakt im Vordergrund.",
		ExplainEN: "Intense kissing and full-body contact as a focus.",
		HasRoles: false,
	},
	{
		Abbr: "BDSM", LabelDE: "BDSM (Oberbegriff)", LabelEN: "BDSM (umbrella term)",
		ExplainDE: "Bondage & Discipline, Dominance & Submission, Sadism & Masochism – einvernehmliches Machtspiel.",
		ExplainEN: "Bondage & Discipline, Dominance & Submission, Sadism & Masochism – consensual power exchange.",
		HasRoles: true,
	},
	{
		Abbr: "BD", LabelDE: "Bondage & Discipline", LabelEN: "Bondage & Discipline",
		ExplainDE: "Fesselungen (Seil, Leder, Tape) kombiniert mit Regeln und Anweisungen.",
		ExplainEN: "Restraints (rope, leather, tape) combined with rules and commands.",
		HasRoles: true,
	},
	{
		Abbr: "DS", LabelDE: "Dominance / Submission", LabelEN: "Dominance / Submission",
		ExplainDE: "Einvernehmliches Machtgefälle: eine Person führt, die andere folgt.",
		ExplainEN: "Consensual power dynamic: one person leads, the other follows.",
		HasRoles: true,
	},
	{
		Abbr: "SM", LabelDE: "Sadismus / Masochismus", LabelEN: "Sadism / Masochism",
		ExplainDE: "Lust am Zufügen oder Empfangen von (einvernehmlichem) Schmerz/Intensität.",
		ExplainEN: "Pleasure in giving or receiving (consensual) pain/intensity.",
		HasRoles: true,
	},
	{
		Abbr: "DOM", LabelDE: "Dominant", LabelEN: "Dominant",
		ExplainDE: "Einvernehmlich dominante Rolle im Machtspiel (Dom = männlich, Domme = weiblich).",
		ExplainEN: "Consensually dominant role in power exchange (Dom = male, Domme = female).",
		HasRoles: false,
	},
	{
		Abbr: "SUB", LabelDE: "Submissiv", LabelEN: "Submissive",
		ExplainDE: "Einvernehmlich unterwürfige Rolle – übergibt Kontrolle an den dominanten Partner.",
		ExplainEN: "Consensually submissive role – cedes control to the dominant partner.",
		HasRoles: false,
	},
	{
		Abbr: "SWT", LabelDE: "Switch", LabelEN: "Switch",
		ExplainDE: "Wechselt zwischen dominanter und submissiver Rolle je nach Stimmung/Partner.",
		ExplainEN: "Switches between dominant and submissive roles depending on mood/partner.",
		HasRoles: false,
	},
	{
		Abbr: "RP", LabelDE: "Rollenspiel", LabelEN: "Role-play",
		ExplainDE: "Einvernehmliches Spielen von Rollen oder Szenarien (Lehrer/Schüler, Chef/Angestellte etc.).",
		ExplainEN: "Consensual acting out of roles or scenarios (teacher/student, boss/employee etc.).",
		HasRoles: true,
	},
	{
		Abbr: "EX", LabelDE: "Exhibitionismus", LabelEN: "Exhibitionism",
		ExplainDE: "Lust daran, sich (einvernehmlich) zu zeigen oder beobachtet zu werden.",
		ExplainEN: "Pleasure in (consensually) showing oneself or being watched.",
		HasRoles: true,
	},
	{
		Abbr: "VOY", LabelDE: "Voyeurismus", LabelEN: "Voyeurism",
		ExplainDE: "Lust daran, andere (mit deren Wissen und Einverständnis) zu beobachten.",
		ExplainEN: "Pleasure in watching others (with their knowledge and consent).",
		HasRoles: false,
	},
	{
		Abbr: "TAN", LabelDE: "Tantra / Slow Sex", LabelEN: "Tantra / Slow sex",
		ExplainDE: "Bewusste, achtsame Sexualität mit Fokus auf Verbindung und Atem statt Orgasmus.",
		ExplainEN: "Conscious, mindful sexuality focused on connection and breath rather than climax.",
		HasRoles: false,
	},
	{
		Abbr: "FFva", LabelDE: "Fisting vaginal (aktiv)", LabelEN: "Vaginal fisting (active)",
		ExplainDE: "Einführen der gesamten Hand in die Vagina – aktive/gebende Rolle.",
		ExplainEN: "Insertion of the entire hand into the vagina – active/giving role.",
		HasRoles: false,
	},
	{
		Abbr: "FFvp", LabelDE: "Fisting vaginal (passiv)", LabelEN: "Vaginal fisting (passive)",
		ExplainDE: "Einführen der gesamten Hand in die Vagina – passive/empfangende Rolle.",
		ExplainEN: "Insertion of the entire hand into the vagina – passive/receiving role.",
		HasRoles: false,
	},
	{
		Abbr: "FFaa", LabelDE: "Fisting anal (aktiv)", LabelEN: "Anal fisting (active)",
		ExplainDE: "Einführen der gesamten Hand anal – aktive/gebende Rolle.",
		ExplainEN: "Insertion of the entire hand anally – active/giving role.",
		HasRoles: false,
	},
	{
		Abbr: "FFap", LabelDE: "Fisting anal (passiv)", LabelEN: "Anal fisting (passive)",
		ExplainDE: "Einführen der gesamten Hand anal – passive/empfangende Rolle.",
		ExplainEN: "Insertion of the entire hand anally – passive/receiving role.",
		HasRoles: false,
	},
	{
		Abbr: "AN", LabelDE: "Analverkehr", LabelEN: "Anal sex",
		ExplainDE: "Anale Penetration.",
		ExplainEN: "Anal penetration.",
		HasRoles: true,
	},
	{
		Abbr: "WS", LabelDE: "Watersports / Natursekt", LabelEN: "Watersports / Golden shower",
		ExplainDE: "Sexuelle Praktiken mit Urin (einvernehmlich).",
		ExplainEN: "Sexual practices involving urine (consensual).",
		HasRoles: true,
	},
	{
		Abbr: "GS", LabelDE: "Gruppensex", LabelEN: "Group sex",
		ExplainDE: "Sex mit mehreren Personen gleichzeitig.",
		ExplainEN: "Sex involving more than two people simultaneously.",
		HasRoles: false,
	},
	{
		Abbr: "MFM", LabelDE: "Ménage-à-trois (Mann-Frau-Mann)", LabelEN: "Threesome (M-F-M)",
		ExplainDE: "Dreierkonstellation mit zwei Männern und einer Frau.",
		ExplainEN: "Threesome with two men and one woman.",
		HasRoles: false,
	},
	{
		Abbr: "MFF", LabelDE: "Ménage-à-trois (Mann-Frau-Frau)", LabelEN: "Threesome (M-F-F)",
		ExplainDE: "Dreierkonstellation mit einem Mann und zwei Frauen.",
		ExplainEN: "Threesome with one man and two women.",
		HasRoles: false,
	},
	{
		Abbr: "CK", LabelDE: "Cuckolding", LabelEN: "Cuckolding",
		ExplainDE: "Der Partner schaut (einvernehmlich) zu, während der andere mit einer dritten Person Sex hat.",
		ExplainEN: "One partner (consensually) watches while the other has sex with a third person.",
		HasRoles: true,
	},
	{
		Abbr: "SW", LabelDE: "Swingen / Partnertausch", LabelEN: "Swinging / Partner swapping",
		ExplainDE: "Einvernehmlicher Tausch oder die gemeinsame Nutzung von Partnern in einer Gruppe.",
		ExplainEN: "Consensual exchange or sharing of partners within a group.",
		HasRoles: false,
	},
	{
		Abbr: "POL", LabelDE: "Polyamorie", LabelEN: "Polyamory",
		ExplainDE: "Mehrere gleichzeitige Liebesbeziehungen mit Wissen und Einverständnis aller Beteiligten.",
		ExplainEN: "Multiple simultaneous loving relationships with the knowledge and consent of all involved.",
		HasRoles: false,
	},
	{
		Abbr: "FET", LabelDE: "Fetisch (allgemein)", LabelEN: "Fetish (general)",
		ExplainDE: "Sexuelle Erregung durch bestimmte Objekte, Materialien oder Körperteile (z.B. Schuhe, Latex, Füße).",
		ExplainEN: "Sexual arousal from specific objects, materials or body parts (e.g. shoes, latex, feet).",
		HasRoles: false,
	},
	{
		Abbr: "LAT", LabelDE: "Latex / Leder", LabelEN: "Latex / Leather",
		ExplainDE: "Fetisch für Latex- oder Lederkleidung und -accessoires.",
		ExplainEN: "Fetish for latex or leather clothing and accessories.",
		HasRoles: false,
	},
	{
		Abbr: "UNI", LabelDE: "Uniform / Kostüm", LabelEN: "Uniform / Costume",
		ExplainDE: "Sexuelle Erregung durch Uniformen, Kostüme oder spezifische Kleidung.",
		ExplainEN: "Sexual arousal from uniforms, costumes or specific clothing.",
		HasRoles: true,
	},
	{
		Abbr: "AGE", LabelDE: "Age Gap / Jung-Alt-Dynamik", LabelEN: "Age gap dynamic",
		ExplainDE: "Einvernehmliche Anziehung durch Altersunterschied (alle Beteiligten volljährig).",
		ExplainEN: "Consensual attraction involving age difference (all participants adults).",
		HasRoles: true,
	},
	{
		Abbr: "MAS", LabelDE: "Masturbation / Mutual Masturbation", LabelEN: "Masturbation / Mutual",
		ExplainDE: "Gemeinsame oder gegenseitige Selbstbefriedigung als eigene Praktik.",
		ExplainEN: "Solo or mutual masturbation as a practice in itself.",
		HasRoles: true,
	},
	{
		Abbr: "CYB", LabelDE: "Cybersex / Online", LabelEN: "Cybersex / Online",
		ExplainDE: "Sexuelle Interaktion über digitale Kanäle (Chat, Video, VR).",
		ExplainEN: "Sexual interaction via digital channels (chat, video, VR).",
		HasRoles: false,
	},
}

// =============================================================================
//  Hilfstypes und Lookup-Funktionen
// =============================================================================

// LabeledValue ist ein Wert mit Anzeigetiteln in mehreren Sprachen.
type LabeledValue struct {
	Value   string `json:"Value"`
	LabelDE string `json:"LabelDE"`
	LabelEN string `json:"LabelEN"`
}

// SexPrefByAbbr gibt die Definition für eine Abkürzung zurück.
// Gibt nil zurück wenn nicht gefunden.
func SexPrefByAbbr(abbr string) *SexPrefDefinition {
	for i := range DefaultSexualPreferences {
		if DefaultSexualPreferences[i].Abbr == abbr {
			return &DefaultSexualPreferences[i]
		}
	}
	return nil
}

// AllSexPrefAbbrs gibt alle bekannten Abkürzungen zurück.
func AllSexPrefAbbrs() []string {
	abbrs := make([]string, len(DefaultSexualPreferences))
	for i, p := range DefaultSexualPreferences {
		abbrs[i] = p.Abbr
	}
	return abbrs
}

// IsKnownSexPref prüft ob eine Abkürzung bekannt ist.
func IsKnownSexPref(abbr string) bool {
	return SexPrefByAbbr(abbr) != nil
}
