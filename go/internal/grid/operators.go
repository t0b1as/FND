// Package grid implementiert die Geo-Zuordnung von deutschen Netzbetreibern
// anhand der Postleitzahl (PLZ) und erzeugt deterministische Wallets pro Betreiber.
//
// Datenquellen:
//   - Strom HV (ÜNB): 4 Regelzonen, Bundesland-basierte PLZ-Zuordnung
//     Bundesnetzagentur / netztransparenz.de
//   - Strom MV/LV: BDEW 4-stellige Netzbetreibernummer, VNBdigital
//     (800+ VNBs – hier die 50 größten nach Netzgebiet)
//   - Gas HV (FNB): 16 Fernleitungsnetzbetreiber, regionale Zuordnung
//     FNB Gas e.V. / Wikipedia
//   - Gas MV/LV: DVGW 6-stellige Codenummer
//     (700+ VNBs – hier die größten)
//
// PLZ-Zuordnung: Für ÜNB/FNB auf Bundesland-Ebene (PLZ-Präfix → Bundesland → ÜNB).
// Für VNBs: direkte PLZ-Ranges für die größten, Rest über Bundesland-Fallback.
package grid

// =============================================================================
//  Sparten
// =============================================================================

type Commodity string

const (
	CommodityElectricityHV Commodity = "electricity_hv" // Höchstspannung (220/380 kV)
	CommodityElectricityMV Commodity = "electricity_mv" // Hochspannung (10–110 kV)
	CommodityElectricityLV Commodity = "electricity_lv" // Niederspannung (230/400 V)
	CommodityGasHV         Commodity = "gas_hv"         // Fernleitungsnetz (>16 bar)
	CommodityGasMV         Commodity = "gas_mv"         // Hochdruckverteilnetz
	CommodityGasLV         Commodity = "gas_lv"         // Niederdruckverteilnetz (<100 mbar)
	CommodityWater         Commodity = "water"           // Trinkwasser
)

// =============================================================================
//  Operator – Netzbetreiber-Datensatz
// =============================================================================

// Operator beschreibt einen deutschen Netzbetreiber.
type Operator struct {
	ID string

	Name      string
	Commodity Commodity
	Level     string // "hv" | "mv" | "lv"

	BDEWCode string
	DVGWCode string
	BDEWName string

	States      []string
	PLZPrefixes []string

	// Netzgebühr: Basisprozentsatz pro km Luftlinie (nur LV/MV).
	// HV-Betreiber werden in der Gebührenberechnung ignoriert.
	// Beispiel: 0.002 = 0,2% pro km → bei 50 km = 10% Netzgebühr
	// Wird vom Betreiber über die Admin-API konfiguriert.
	// Standardwert 0 = keine Gebühr (HV, Wasser, unkonfiguriert).
	FeePercentPerKM float64

	// Geografischer Mittelpunkt des Netzgebiets (für Distanzberechnung).
	// Wird aus PLZ-Präfixen automatisch berechnet.
	CentroidLat float64
	CentroidLon float64

	Website string
}

// =============================================================================
//  Strom – Übertragungsnetzbetreiber (HV, 220/380 kV)
// =============================================================================
// Quelle: netztransparenz.de, netzentwicklungsplan.de

var electricityHVOperators = []Operator{
	{
		ID: "UNB-tennet", Name: "TenneT TSO GmbH",
		Commodity: CommodityElectricityHV, Level: "hv",
		States: []string{"SH", "NI", "HB", "BY", "HE"},
		// TenneT: Schleswig-Holstein bis Bayern (N-S-Streifen durch D)
		// + Teile von NW, BW über Niederspannungsübergaben
		PLZPrefixes: []string{
			// Schleswig-Holstein
			"20", "21", "22", "23", "24", "25",
			// Niedersachsen + Bremen
			"26", "27", "28", "29", "30", "31",
			// Hessen
			"34", "35", "36", "60", "61", "63", "64", "65",
			// Bayern
			"80", "81", "82", "83", "84", "85", "86", "87",
			"88", "89", "90", "91", "92", "93", "94", "95", "96", "97",
		},
		Website: "https://www.tennet.eu",
	},
	{
		ID: "UNB-50hertz", Name: "50Hertz Transmission GmbH",
		Commodity: CommodityElectricityHV, Level: "hv",
		States: []string{"BB", "MV", "SN", "ST", "TH", "BE", "HH"},
		PLZPrefixes: []string{
			// Berlin
			"10", "12", "13", "14",
			// Brandenburg
			"03", "04", "06", "14", "15", "16", "17", "18", "19",
			// Mecklenburg-Vorpommern
			"17", "18", "19",
			// Sachsen
			"01", "02", "04", "07", "08", "09",
			// Sachsen-Anhalt
			"06", "38", "39",
			// Thüringen
			"07", "99",
			// Hamburg
			"20", "21", "22",
		},
		Website: "https://www.50hertz.com",
	},
	{
		ID: "UNB-amprion", Name: "Amprion GmbH",
		Commodity: CommodityElectricityHV, Level: "hv",
		States: []string{"NW", "RP", "SL", "HE"},
		PLZPrefixes: []string{
			// NRW
			"40", "41", "42", "44", "45", "46", "47", "48", "49",
			"50", "51", "52", "53", "57", "58", "59",
			// Rheinland-Pfalz
			"54", "55", "56", "67",
			// Saarland
			"66",
			// Niedersachsen (Teile)
			"26", "30",
		},
		Website: "https://www.amprion.net",
	},
	{
		ID: "UNB-transnetbw", Name: "TransnetBW GmbH",
		Commodity: CommodityElectricityHV, Level: "hv",
		States: []string{"BW"},
		PLZPrefixes: []string{
			"68", "69", "70", "71", "72", "73", "74", "75",
			"76", "77", "78", "79",
		},
		Website: "https://www.transnetbw.de",
	},
}

// =============================================================================
//  Strom – Verteilnetzbetreiber MV/LV (Auswahl der 50 größten)
// =============================================================================
// Quelle: BDEW VNBdigital, vnbdigital.de
// BDEW-Code: 4-stellige Netzbetreibernummer
// Vollständige Liste: 800+ VNBs – hier die mit größten Netzgebieten

var electricityMVLVOperators = []Operator{
	// ── Gruppe: E.ON / Bayernwerk / E.DIS ────────────────────────────────────
	{
		ID: "BDEW-9909", Name: "Bayernwerk Netz GmbH",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9909",
		States: []string{"BY"},
		PLZPrefixes: []string{"80", "81", "82", "83", "84", "85", "86", "87", "88", "89", "90", "91", "92", "93", "94"},
		Website: "https://www.bayernwerk-netz.de",
	},
	{
		ID: "BDEW-9902", Name: "E.DIS Netz GmbH",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9902",
		States: []string{"BB", "MV"},
		PLZPrefixes: []string{"14", "15", "16", "17", "18", "19"},
		Website: "https://www.e-dis-netz.de",
	},
	{
		ID: "BDEW-9900", Name: "E.ON Netz GmbH (Bayernwerk MV)",
		Commodity: CommodityElectricityMV, Level: "mv", BDEWCode: "9900",
		States: []string{"BY", "HE", "TH"},
		Website: "https://www.eon.com",
	},
	// ── Gruppe: RWE / Westnetz ────────────────────────────────────────────────
	{
		ID: "BDEW-9844", Name: "Westnetz GmbH",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9844",
		States: []string{"NW", "RP"},
		PLZPrefixes: []string{"40", "41", "42", "44", "45", "46", "47", "48", "50", "51", "52", "53", "54", "56", "57", "58", "59"},
		Website: "https://www.westnetz.de",
	},
	// ── Gruppe: EnBW / Netze BW ──────────────────────────────────────────────
	{
		ID: "BDEW-9823", Name: "Netze BW GmbH",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9823",
		States: []string{"BW"},
		PLZPrefixes: []string{"70", "71", "72", "73", "74", "75", "76", "77", "78", "79"},
		Website: "https://www.netze-bw.de",
	},
	// ── Gruppe: E.ON / HanseWerk / Schleswiger Stadtwerke ────────────────────
	{
		ID: "BDEW-9920", Name: "HanseWerk Natur GmbH / Schleswig-Holstein Netz AG",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9920",
		States: []string{"SH", "HH"},
		PLZPrefixes: []string{"20", "21", "22", "23", "24", "25"},
		Website: "https://www.sh-netz.com",
	},
	// ── Gruppe: Avacon ───────────────────────────────────────────────────────
	{
		ID: "BDEW-9950", Name: "Avacon Netz GmbH",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9950",
		States: []string{"NI", "ST", "HB"},
		PLZPrefixes: []string{"38", "39", "26", "27", "28", "29", "30", "31"},
		Website: "https://www.avacon-netz.de",
	},
	// ── Gruppe: Mitteldeutsche Netzgesellschaft ───────────────────────────────
	{
		ID: "BDEW-9805", Name: "Mitteldeutsche Netzgesellschaft Strom mbH (MITNETZ)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9805",
		States: []string{"SN", "ST", "TH", "BB"},
		PLZPrefixes: []string{"01", "02", "04", "06", "07", "08", "09", "99"},
		Website: "https://www.mitnetz-strom.de",
	},
	// ── Gruppe: EWE ──────────────────────────────────────────────────────────
	{
		ID: "BDEW-9829", Name: "EWE NETZ GmbH",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9829",
		States: []string{"NI", "HB", "BB"},
		PLZPrefixes: []string{"26", "27", "28", "29"},
		Website: "https://www.ewe-netz.de",
	},
	// ── Große Stadtwerke ─────────────────────────────────────────────────────
	{
		ID: "BDEW-9000", Name: "Stadtwerke München GmbH (SWM Infrastruktur)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9000",
		States: []string{"BY"},
		PLZPrefixes: []string{"80", "81"},
		Website: "https://www.swm.de",
	},
	{
		ID: "BDEW-9001", Name: "Stadtwerke Berlin (Stromnetz Berlin GmbH)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9001",
		States: []string{"BE"},
		PLZPrefixes: []string{"10", "12", "13", "14"},
		Website: "https://www.stromnetz-berlin.de",
	},
	{
		ID: "BDEW-9002", Name: "Hamburger Energiewerke GmbH (Stromnetz Hamburg)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9002",
		States: []string{"HH"},
		PLZPrefixes: []string{"20", "21", "22"},
		Website: "https://www.stromnetz-hamburg.de",
	},
	{
		ID: "BDEW-9003", Name: "Stadtwerke Köln – rheinenergie AG (netz köln)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9003",
		States: []string{"NW"},
		PLZPrefixes: []string{"50", "51"},
		Website: "https://www.rheinenergie.com",
	},
	{
		ID: "BDEW-9004", Name: "ENNI Stadt & Service Niederrhein GmbH / SWD Düsseldorf",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9004",
		States: []string{"NW"},
		PLZPrefixes: []string{"40", "41"},
		Website: "https://www.netzgesellschaft-duesseldorf.de",
	},
	{
		ID: "BDEW-9005", Name: "infra fürth gmbh / N-ERGIE Netz GmbH (Nürnberg)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9005",
		States: []string{"BY"},
		PLZPrefixes: []string{"90", "91"},
		Website: "https://www.n-ergie-netz.de",
	},
	{
		ID: "BDEW-9006", Name: "Netze Stuttgart GmbH (EnBW)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9006",
		States: []string{"BW"},
		PLZPrefixes: []string{"70"},
		Website: "https://www.netze-stuttgart.de",
	},
	{
		ID: "BDEW-9007", Name: "SWB Netz GmbH (Bremen)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9007",
		States: []string{"HB"},
		PLZPrefixes: []string{"28"},
		Website: "https://www.swb-gruppe.de",
	},
	{
		ID: "BDEW-9008", Name: "Stadtwerke Hannover AG (enercity Netz)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9008",
		States: []string{"NI"},
		PLZPrefixes: []string{"30"},
		Website: "https://www.enercity-netz.de",
	},
	{
		ID: "BDEW-9009", Name: "Drewag-Stadtwerke Dresden GmbH / SachsenEnergie Netz",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9009",
		States: []string{"SN"},
		PLZPrefixes: []string{"01"},
		Website: "https://www.sachsenenergie.de",
	},
	{
		ID: "BDEW-9010", Name: "Stadtwerke Leipzig GmbH / LVV",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9010",
		States: []string{"SN"},
		PLZPrefixes: []string{"04"},
		Website: "https://www.stadtwerke-leipzig.de",
	},
	{
		ID: "BDEW-9011", Name: "ENVIA Mitteldeutsche Energie AG (enviaM)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9011",
		States: []string{"SN", "TH", "ST", "BB"},
		Website: "https://www.enviam-gruppe.de",
	},
	{
		ID: "BDEW-9012", Name: "Netzgesellschaft Frankfurt (Mainova)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9012",
		States: []string{"HE"},
		PLZPrefixes: []string{"60", "61"},
		Website: "https://www.mainova.de",
	},
	{
		ID: "BDEW-9013", Name: "MVV Netze GmbH (Mannheim)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9013",
		States: []string{"BW"},
		PLZPrefixes: []string{"68"},
		Website: "https://www.mvv-netze.de",
	},
	{
		ID: "BDEW-9014", Name: "Saarland Netz GmbH (VSE)",
		Commodity: CommodityElectricityLV, Level: "lv", BDEWCode: "9014",
		States: []string{"SL"},
		PLZPrefixes: []string{"66"},
		Website: "https://www.saarland-netz.de",
	},
}

// =============================================================================
//  Gas – Fernleitungsnetzbetreiber HV (>16 bar)
// =============================================================================
// Quelle: FNB Gas e.V. (fnb-gas.de), Wikipedia
// 16 FNBs in Deutschland, alle im Marktgebiet Trading Hub Europe (THE)
// PLZ-Zuordnung: regional/Bundesland-basiert (FNBs betreiben Transitnetz,
// keine direkte Endkunden-PLZ wie Strom-VNBs)

var gasHVOperators = []Operator{
	{
		ID: "FNB-oge", Name: "Open Grid Europe GmbH (OGE)",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"NW", "NI", "HE", "RP", "SL", "BY", "BW"},
		// Größtes deutsches Gasfernleitungsnetz (~12.000 km)
		PLZPrefixes: []string{"40", "41", "42", "44", "45", "46", "47", "48", "50", "51", "52", "53", "60", "61", "63"},
		Website: "https://www.open-grid-europe.com",
	},
	{
		ID: "FNB-gascade", Name: "GASCADE Gastransport GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"BB", "SN", "ST", "TH", "NI"},
		// Ostnetz, hauptsächlich Transit Ost→West
		PLZPrefixes: []string{"14", "15", "16", "01", "04", "06", "07", "38", "39"},
		Website: "https://www.gascade.de",
	},
	{
		ID: "FNB-thyssengas", Name: "Thyssengas GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"NW", "NI"},
		PLZPrefixes: []string{"40", "42", "44", "45", "46", "47", "58", "59"},
		Website: "https://www.thyssengas.de",
	},
	{
		ID: "FNB-bayernets", Name: "bayernets GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"BY"},
		PLZPrefixes: []string{"80", "81", "82", "83", "84", "85", "86", "87", "88", "89", "90", "91", "92", "93", "94"},
		Website: "https://www.bayernets.de",
	},
	{
		ID: "FNB-ontras", Name: "ONTRAS Gastransport GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"BB", "MV", "SN", "ST", "TH", "BE"},
		PLZPrefixes: []string{"01", "02", "03", "04", "06", "07", "08", "09", "10", "12", "13", "14", "15", "16", "17", "18", "19", "99"},
		Website: "https://www.ontras.com",
	},
	{
		ID: "FNB-nowega", Name: "Nowega GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"NI", "NW"},
		PLZPrefixes: []string{"26", "27", "28", "30", "31", "48", "49"},
		Website: "https://www.nowega.de",
	},
	{
		ID: "FNB-gasunie", Name: "Gasunie Deutschland Transport Services GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"NI", "HB", "SH"},
		PLZPrefixes: []string{"26", "27", "28", "29"},
		Website: "https://www.gasunie.de",
	},
	{
		ID: "FNB-gtgnord", Name: "Gastransport Nord GmbH (GTG Nord, EWE)",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"NI", "HB", "SH"},
		PLZPrefixes: []string{"26", "27", "28"},
		Website: "https://www.gtg-nord.de",
	},
	{
		ID: "FNB-terranets", Name: "terranets bw GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"BW"},
		PLZPrefixes: []string{"70", "71", "72", "73", "74", "75", "76", "77", "78", "79"},
		Website: "https://www.terranets-bw.de",
	},
	{
		ID: "FNB-ferngas", Name: "Ferngas Netzgesellschaft mbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"BY"},
		PLZPrefixes: []string{"80", "85", "86"},
		Website: "https://www.ferngas.de",
	},
	{
		ID: "FNB-nel", Name: "NEL Gastransport GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"MV", "SH", "NI"},
		PLZPrefixes: []string{"17", "18", "19", "23", "24", "25"},
		Website: "https://www.nel-transport.de",
	},
	{
		ID: "FNB-grtgaz", Name: "GRTgaz Deutschland GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"SL", "RP", "BW"},
		PLZPrefixes: []string{"66", "67", "68", "76"},
		Website: "https://www.grtgaz-deutschland.de",
	},
	{
		ID: "FNB-fluxys-tenp", Name: "Fluxys TENP GmbH",
		Commodity: CommodityGasHV, Level: "hv",
		States: []string{"BW", "BY"},
		PLZPrefixes: []string{"78", "79"},
		Website: "https://www.fluxys.com",
	},
}

// =============================================================================
//  Gas – Verteilnetzbetreiber MV/LV (Auswahl der größten)
// =============================================================================
// DVGW-Code: 6-stellig

var gasMVLVOperators = []Operator{
	// ── Gruppe: E.ON / Avacon / Bayernwerk ───────────────────────────────────
	{
		ID: "DVGW-901023", Name: "Bayernwerk Netz GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "901023",
		States: []string{"BY"},
		PLZPrefixes: []string{"80", "81", "82", "83", "84", "85", "86", "87", "88", "89", "90", "91", "92", "93", "94"},
		Website: "https://www.bayernwerk-netz.de",
	},
	{
		ID: "DVGW-901024", Name: "Avacon Netz GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "901024",
		States: []string{"NI", "ST"},
		PLZPrefixes: []string{"38", "39", "26", "27", "28", "29", "30", "31"},
		Website: "https://www.avacon-netz.de",
	},
	// ── Gruppe: E.ON Energie Deutschland / EDN ───────────────────────────────
	{
		ID: "DVGW-901031", Name: "E.DIS Netz GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "901031",
		States: []string{"BB", "MV"},
		PLZPrefixes: []string{"14", "15", "16", "17", "18", "19"},
		Website: "https://www.e-dis-netz.de",
	},
	// ── Gruppe: RWE / Westnetz ────────────────────────────────────────────────
	{
		ID: "DVGW-901042", Name: "Westnetz GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "901042",
		States: []string{"NW", "RP"},
		PLZPrefixes: []string{"40", "41", "42", "44", "45", "46", "47", "48", "50", "51", "52", "53"},
		Website: "https://www.westnetz.de",
	},
	// ── Gruppe: EnBW ──────────────────────────────────────────────────────────
	{
		ID: "DVGW-901051", Name: "Netze BW GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "901051",
		States: []string{"BW"},
		PLZPrefixes: []string{"70", "71", "72", "73", "74", "75", "76", "77", "78", "79"},
		Website: "https://www.netze-bw.de",
	},
	// ── Gruppe: EWE ──────────────────────────────────────────────────────────
	{
		ID: "DVGW-901062", Name: "EWE NETZ GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "901062",
		States: []string{"NI", "HB"},
		PLZPrefixes: []string{"26", "27", "28", "29"},
		Website: "https://www.ewe-netz.de",
	},
	// ── Gruppe: MITNETZ GAS ──────────────────────────────────────────────────
	{
		ID: "DVGW-901071", Name: "Mitteldeutsche Netzgesellschaft Gas mbH (MITNETZ GAS)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "901071",
		States: []string{"SN", "ST", "TH", "BB"},
		PLZPrefixes: []string{"01", "02", "04", "06", "07", "08", "09", "99"},
		Website: "https://www.mitnetz-gas.de",
	},
	// ── Große Stadtwerke Gas ─────────────────────────────────────────────────
	{
		ID: "DVGW-910001", Name: "SWM Services GmbH (München, Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910001",
		States: []string{"BY"},
		PLZPrefixes: []string{"80", "81"},
		Website: "https://www.swm.de",
	},
	{
		ID: "DVGW-910002", Name: "GASAG Berlin AG",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910002",
		States: []string{"BE"},
		PLZPrefixes: []string{"10", "12", "13", "14"},
		Website: "https://www.gasag.de",
	},
	{
		ID: "DVGW-910003", Name: "Gasnetz Hamburg GmbH",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910003",
		States: []string{"HH"},
		PLZPrefixes: []string{"20", "21", "22"},
		Website: "https://www.gasnetz-hamburg.de",
	},
	{
		ID: "DVGW-910004", Name: "Rheinische NetzGesellschaft mbH (Köln, Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910004",
		States: []string{"NW"},
		PLZPrefixes: []string{"50", "51"},
		Website: "https://www.rheinenergie.com",
	},
	{
		ID: "DVGW-910005", Name: "Stadtwerke Düsseldorf AG Netz (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910005",
		States: []string{"NW"},
		PLZPrefixes: []string{"40", "41"},
		Website: "https://www.swd-netz.de",
	},
	{
		ID: "DVGW-910006", Name: "infra fürth / N-ERGIE Netz GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910006",
		States: []string{"BY"},
		PLZPrefixes: []string{"90", "91"},
		Website: "https://www.n-ergie-netz.de",
	},
	{
		ID: "DVGW-910007", Name: "Netze Stuttgart GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910007",
		States: []string{"BW"},
		PLZPrefixes: []string{"70"},
		Website: "https://www.netze-stuttgart.de",
	},
	{
		ID: "DVGW-910008", Name: "enercity Netz GmbH (Hannover, Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910008",
		States: []string{"NI"},
		PLZPrefixes: []string{"30"},
		Website: "https://www.enercity-netz.de",
	},
	{
		ID: "DVGW-910009", Name: "SachsenEnergie / Drewag Netz (Dresden, Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910009",
		States: []string{"SN"},
		PLZPrefixes: []string{"01"},
		Website: "https://www.sachsenenergie.de",
	},
	{
		ID: "DVGW-910010", Name: "Stadtwerke Leipzig GmbH (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910010",
		States: []string{"SN"},
		PLZPrefixes: []string{"04"},
		Website: "https://www.stadtwerke-leipzig.de",
	},
	{
		ID: "DVGW-910011", Name: "Mainova AG (Frankfurt, Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910011",
		States: []string{"HE"},
		PLZPrefixes: []string{"60", "61"},
		Website: "https://www.mainova.de",
	},
	{
		ID: "DVGW-910012", Name: "VSE Netz GmbH / Saarland Netz (Gas)",
		Commodity: CommodityGasLV, Level: "lv", DVGWCode: "910012",
		States: []string{"SL"},
		PLZPrefixes: []string{"66"},
		Website: "https://www.saarland-netz.de",
	},
}

// =============================================================================
//  Wasser – Versorger (Auswahl, kommunal)
// =============================================================================
// Ca. 6.000 Versorger – keine einheitliche nationale Registrierung.
// Hier die größten nach Versorgungsgebiet (Stadtwerke + Zweckverbände).

var waterOperators = []Operator{
	{
		ID: "WVU-muenchen", Name: "Stadtwerke München GmbH (Wasser)",
		Commodity: CommodityWater,
		States: []string{"BY"}, PLZPrefixes: []string{"80", "81"},
		Website: "https://www.swm.de",
	},
	{
		ID: "WVU-berlin", Name: "Berliner Wasserbetriebe (BWB)",
		Commodity: CommodityWater,
		States: []string{"BE"}, PLZPrefixes: []string{"10", "12", "13", "14"},
		Website: "https://www.bwb.de",
	},
	{
		ID: "WVU-hamburg", Name: "Hamburg Wasser",
		Commodity: CommodityWater,
		States: []string{"HH"}, PLZPrefixes: []string{"20", "21", "22"},
		Website: "https://www.hamburgwasser.de",
	},
	{
		ID: "WVU-koeln", Name: "RheinEnergie AG (Köln, Wasser)",
		Commodity: CommodityWater,
		States: []string{"NW"}, PLZPrefixes: []string{"50", "51"},
		Website: "https://www.rheinenergie.com",
	},
	{
		ID: "WVU-frankfurt", Name: "Mainova AG (Frankfurt, Wasser)",
		Commodity: CommodityWater,
		States: []string{"HE"}, PLZPrefixes: []string{"60", "61"},
		Website: "https://www.mainova.de",
	},
	{
		ID: "WVU-stuttgart", Name: "Netze Stuttgart GmbH (Wasser)",
		Commodity: CommodityWater,
		States: []string{"BW"}, PLZPrefixes: []string{"70"},
		Website: "https://www.netze-stuttgart.de",
	},
	{
		ID: "WVU-nuernberg", Name: "N-ERGIE AG (Nürnberg, Wasser)",
		Commodity: CommodityWater,
		States: []string{"BY"}, PLZPrefixes: []string{"90", "91"},
		Website: "https://www.n-ergie.de",
	},
	{
		ID: "WVU-bremen", Name: "hanseWasser Bremen GmbH",
		Commodity: CommodityWater,
		States: []string{"HB"}, PLZPrefixes: []string{"28"},
		Website: "https://www.hansewasser.de",
	},
	{
		ID: "WVU-duesseldorf", Name: "Stadtwerke Düsseldorf AG (Wasser)",
		Commodity: CommodityWater,
		States: []string{"NW"}, PLZPrefixes: []string{"40", "41"},
		Website: "https://www.swd-ag.de",
	},
	{
		ID: "WVU-dresden", Name: "SachsenEnergie AG (Dresden, Wasser)",
		Commodity: CommodityWater,
		States: []string{"SN"}, PLZPrefixes: []string{"01"},
		Website: "https://www.sachsenenergie.de",
	},
	{
		ID: "WVU-hannover", Name: "enercity AG (Hannover, Wasser)",
		Commodity: CommodityWater,
		States: []string{"NI"}, PLZPrefixes: []string{"30"},
		Website: "https://www.enercity.de",
	},
	{
		ID: "WVU-leipzig", Name: "Kommunale Wasserwerke Leipzig (KWL)",
		Commodity: CommodityWater,
		States: []string{"SN"}, PLZPrefixes: []string{"04"},
		Website: "https://www.wasser-leipzig.de",
	},
	// Zweckverbände
	{
		ID: "WVU-gkw-hannover", Name: "Großraum-Verkehr Hannover / Stadtwerke Hannover Wasser (OWG)",
		Commodity: CommodityWater,
		States: []string{"NI"}, PLZPrefixes: []string{"30", "31"},
		Website: "https://www.enercity.de",
	},
	{
		ID: "WVU-zvw-mannheim", Name: "Zweckverband Wasserversorgung Mannheim / MVV",
		Commodity: CommodityWater,
		States: []string{"BW"}, PLZPrefixes: []string{"68"},
		Website: "https://www.mvv.de",
	},
}

// =============================================================================
//  Kombinierter Index
// =============================================================================

// AllOperators gibt alle Betreiber zurück.
func AllOperators() []Operator {
	all := make([]Operator, 0,
		len(electricityHVOperators)+
			len(electricityMVLVOperators)+
			len(gasHVOperators)+
			len(gasMVLVOperators)+
			len(waterOperators),
	)
	all = append(all, electricityHVOperators...)
	all = append(all, electricityMVLVOperators...)
	all = append(all, gasHVOperators...)
	all = append(all, gasMVLVOperators...)
	all = append(all, waterOperators...)
	return all
}

// ByID gibt einen Betreiber anhand seiner ID zurück.
func ByID(id string) *Operator {
	for _, op := range AllOperators() {
		o := op
		if o.ID == id {
			return &o
		}
	}
	return nil
}
