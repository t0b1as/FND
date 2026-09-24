// Package commodity definiert alle handelbaren Rohstoff-Token des Fundus Marketplace.
//
// Design-Prinzipien:
//   - Jeder Commodity-Typ wird einmalig in der Registry registriert
//   - Neue Rohstoffe hinzufügen = eine Zeile in Register() + eine Konstante
//   - Einheiten: Strom in Ws; alle anderen Commodities in g (Gramm)
//   - Brennwert-Validierung nach DIN EN ISO 18125
package commodity

import (
	"fmt"
	"math"
	"time"
)

// =============================================================================
//  Commodity-Typ
// =============================================================================

// Type identifiziert den Rohstoff. uint8 = max. 255 Typen – ausreichend.
type Type uint8

// Vordefinierte Typen (0–9 reserviert für Energie/Fluids, 10+ für Metalle/Sonstige)
const (
	Electricity Type = 0 // Strom     – Einheit: Ws (Watt-Sekunden)
	Gas         Type = 1 // Erdgas    – Einheit: g,  Brennwert kWh/kg
	Water       Type = 2 // Wasser    – Einheit: g (Gramm)
	Oil         Type = 3 // Heizöl    – Einheit: g,  Brennwert kWh/kg

	Gold    Type = 10 // Gold   – Einheit: g
	Silver  Type = 11 // Silber – Einheit: g
	Copper  Type = 12 // Kupfer – Einheit: g
	Tin     Type = 13 // Zinn   – Einheit: g
)

// =============================================================================
//  Registry – erweiterbar durch externe Pakete
// =============================================================================

// CommodityDef beschreibt alle Eigenschaften eines Rohstoff-Typs.
type CommodityDef struct {
	Type        Type
	Name        string // Anzeigename (DE)
	NameEN      string // Anzeigename (EN)
	Unit        string // Basiseinheit für Mengenangaben
	Category    Category
	// CalorificMin/Max: Plausibilitätsgrenzen für den Brennwert (kWh/kg * 100).
	// 0 = kein Brennwert (Strom, Wasser, Metalle)
	CalorificMin uint32
	CalorificMax uint32
	// HasDensity: true wenn Volumen→Masse-Umrechnung relevant ist (Öl)
	HasDensity bool
	// CO2Factor: kg CO2 pro kWh Endenergie (0 wenn nicht anwendbar)
	CO2FactorPerKWh float64
}

// Category gruppiert Typen für UI und Filterlogik.
type Category string

const (
	CategoryElectric Category = "electric" // Strom
	CategoryFuel     Category = "fuel"     // Brennstoffe (Gas, Öl)
	CategoryWater    Category = "water"    // Wasser
	CategoryMetal    Category = "metal"    // Edelmetalle & Industriemetalle
	CategoryOther    Category = "other"
)

// registry hält alle registrierten Commodity-Definitionen.
var registry = map[Type]*CommodityDef{}

// Register fügt einen neuen Commodity-Typ zur Registry hinzu.
// Aufruf idealerweise in init() des eigenen Pakets oder in Register().
func Register(def *CommodityDef) {
	if _, exists := registry[def.Type]; exists {
		panic(fmt.Sprintf("commodity: duplicate type %d (%s)", def.Type, def.Name))
	}
	registry[def.Type] = def
}

// Def gibt die Definition für einen Typ zurück.
// Gibt nil zurück wenn nicht registriert.
func Def(t Type) *CommodityDef {
	return registry[t]
}

// All gibt alle registrierten Definitionen zurück.
func All() []*CommodityDef {
	result := make([]*CommodityDef, 0, len(registry))
	for _, d := range registry {
		result = append(result, d)
	}
	return result
}

// ByCategory gibt alle Typen einer Kategorie zurück.
func ByCategory(cat Category) []*CommodityDef {
	var result []*CommodityDef
	for _, d := range registry {
		if d.Category == cat {
			result = append(result, d)
		}
	}
	return result
}

// init registriert alle eingebauten Commodity-Typen.
// Neue Typen → Register() hier oder in einem init() des Aufrufers aufrufen.
func init() {
	Register(&CommodityDef{
		Type:   Electricity,
		Name:   "Strom", NameEN: "Electricity",
		Unit:     "Ws",
		Category: CategoryElectric,
		// Strom hat keinen Brennwert, aber einen CO2-Faktor (Strommix DE 2024)
		CO2FactorPerKWh: 0.366, // kg CO2/kWh
	})
	Register(&CommodityDef{
		Type:   Gas,
		Name:   "Erdgas", NameEN: "Natural gas",
		Unit:         "g",
		Category:     CategoryFuel,
		CalorificMin: 900,  // 9.00 kWh/kg (unterste Grenze Biogas)
		CalorificMax: 1800, // 18.00 kWh/kg (LPG-nahe Gase)
		CO2FactorPerKWh: 0.201,
	})
	Register(&CommodityDef{
		Type:   Water,
		Name:   "Wasser", NameEN: "Water",
		Unit:     "g",
		Category: CategoryWater,
	})
	Register(&CommodityDef{
		Type:   Oil,
		Name:   "Heizöl", NameEN: "Heating oil",
		Unit:         "g",
		Category:     CategoryFuel,
		CalorificMin: 1000, // 10.00 kWh/kg
		CalorificMax: 1400, // 14.00 kWh/kg
		HasDensity:   true,
		CO2FactorPerKWh: 0.267,
	})

	// Metalle (kein Brennwert, kein CO2-Faktor)
	Register(&CommodityDef{
		Type:     Gold,
		Name:     "Gold", NameEN: "Gold",
		Unit:     "g",
		Category: CategoryMetal,
	})
	Register(&CommodityDef{
		Type:     Silver,
		Name:     "Silber", NameEN: "Silver",
		Unit:     "g",
		Category: CategoryMetal,
	})
	Register(&CommodityDef{
		Type:     Copper,
		Name:     "Kupfer", NameEN: "Copper",
		Unit:     "g",
		Category: CategoryMetal,
	})
	Register(&CommodityDef{
		Type:     Tin,
		Name:     "Zinn", NameEN: "Tin",
		Unit:     "g",
		Category: CategoryMetal,
	})
}

// IsRegistered prüft ob ein Typ bekannt ist.
func IsRegistered(t Type) bool { return registry[t] != nil }

// =============================================================================
//  Token-Struktur
// =============================================================================

// Token ist der vollständige Messdatensatz für einen Rohstoff-Token.
// Mengenwerte sind in der jeweiligen Basiseinheit der CommodityDef.
type Token struct {
	// Pflichtfelder
	Commodity Type      `json:"commodity"`
	Timestamp time.Time `json:"timestamp"`
	MeterID   string    `json:"meter_id"`
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`

	// -------------------------------------------------------------------------
	//  Mengenwerte (je nach Commodity-Typ nur das relevante Feld befüllen)
	// -------------------------------------------------------------------------

	// Strom (Ws = Watt-Sekunden)
	// Umrechnung: 1 Ws = 1/3600 Wh = 1/3600000 kWh
	EnergyWs float64 `json:"energy_ws,omitempty"`
	PowerW   float64 `json:"power_w,omitempty"` // Aktuelle Leistung in W

	// Erdgas (g = Gramm)
	GasG             float64 `json:"gas_g,omitempty"`
	GasCalorificX100 uint32  `json:"gas_calorific_x100,omitempty"` // kWh/kg * 100
	GasType          string  `json:"gas_type,omitempty"`           // "H" | "L"
	GasPressureBar   float32 `json:"gas_pressure_bar,omitempty"`

	// Wasser (g = Gramm; 1 L Wasser ≈ 1000 g bei Normalbedingungen)
	WaterG float64      `json:"water_g,omitempty"`
	WaterQuality WaterQuality `json:"water_quality,omitempty"`
	WaterTempC   float32      `json:"water_temp_c,omitempty"`

	// Heizöl (g = Gramm)
	OilG             float64 `json:"oil_g,omitempty"`
	OilCalorificX100 uint32  `json:"oil_calorific_x100,omitempty"` // kWh/kg * 100
	OilDensityG_mL   float32 `json:"oil_density_g_ml,omitempty"`  // nur für Volumen-Display
	OilGrade         string  `json:"oil_grade,omitempty"`         // "EL" | "S"

	// Metalle & sonstige Feststoffe (g = Gramm)
	// Wird für Gold, Silber, Kupfer, Zinn und jeden weiteren registrierten Typ genutzt.
	MassG float64 `json:"mass_g,omitempty"`

	// Erzeuger/Quelle (für Netzgebühr-Berechnung)
	GeneratorLat float64 `json:"generator_lat"`
	GeneratorLon float64 `json:"generator_lon"`

	// Kryptografische Signatur
	Signature string `json:"signature,omitempty"`
}

// =============================================================================
//  Brennwert-Referenzwerte
// =============================================================================

type CalorificValue struct {
	KWhPerKg    float64
	MJPerKg     float64
	Description string
}

func (c CalorificValue) ToX100() uint32 {
	return uint32(math.Round(c.KWhPerKg * 100))
}

var CalorificRef = struct {
	GasH  CalorificValue
	GasL  CalorificValue
	OilEL CalorificValue
	OilS  CalorificValue
}{
	GasH:  CalorificValue{KWhPerKg: 13.9,  MJPerKg: 50.04, Description: "Erdgas H (Hs)"},
	GasL:  CalorificValue{KWhPerKg: 11.4,  MJPerKg: 41.04, Description: "Erdgas L (Hs)"},
	OilEL: CalorificValue{KWhPerKg: 11.86, MJPerKg: 42.7,  Description: "Heizöl EL (Hs)"},
	OilS:  CalorificValue{KWhPerKg: 11.63, MJPerKg: 41.87, Description: "Heizöl S (Hs)"},
}

// =============================================================================
//  Wasserqualität
// =============================================================================

type WaterQuality uint8

const (
	WaterDrinking WaterQuality = 0
	WaterProcess  WaterQuality = 1
	WaterGrey     WaterQuality = 2
	WaterWaste    WaterQuality = 3
)

func (q WaterQuality) String() string {
	switch q {
	case WaterDrinking: return "drinking"
	case WaterProcess:  return "process"
	case WaterGrey:     return "grey"
	case WaterWaste:    return "waste"
	default:            return "unknown"
	}
}

// =============================================================================
//  Berechnungsmethoden auf Token
// =============================================================================

// Quantity gibt die primäre Menge in der Basiseinheit der CommodityDef zurück.
func (t *Token) Quantity() float64 {
	switch t.Commodity {
	case Electricity: return t.EnergyWs
	case Gas:         return t.GasG
	case Water:       return t.WaterG
	case Oil:         return t.OilG
	default:
		// Metalle und sonstige Feststoffe → MassG
		if def := Def(t.Commodity); def != nil && def.Category == CategoryMetal {
			return t.MassG
		}
		return t.MassG
	}
}

// EnergyKWh gibt den Energiegehalt in kWh zurück.
func (t *Token) EnergyKWh() float64 {
	switch t.Commodity {
	case Electricity:
		// Ws → kWh: 1 Ws = 1 J = 1/3600 Wh = 1/3600000 kWh
		return t.EnergyWs / 3_600_000

	case Gas:
		if t.GasG > 0 && t.GasCalorificX100 > 0 {
			return (t.GasG / 1000) * (float64(t.GasCalorificX100) / 100)
		}

	case Oil:
		if t.OilG > 0 && t.OilCalorificX100 > 0 {
			return (t.OilG / 1000) * (float64(t.OilCalorificX100) / 100)
		}
	}
	return 0
}

// EnergyWh gibt Strom-Energie in Wh zurück (Hilfsmethode für Anzeige).
func (t *Token) EnergyWh() float64 { return t.EnergyWs / 3600 }

// CO2EquivalentKg schätzt CO2-Äquivalente in kg.
func (t *Token) CO2EquivalentKg() float64 {
	def := Def(t.Commodity)
	if def == nil || def.CO2FactorPerKWh == 0 {
		return 0
	}
	return t.EnergyKWh() * def.CO2FactorPerKWh
}

// MassKg gibt die Masse in kg zurück.
func (t *Token) MassKg() float64 {
	switch t.Commodity {
	case Gas:  return t.GasG / 1000
	case Oil:  return t.OilG / 1000
	default:   return t.MassG / 1000
	}
}

// =============================================================================
//  Validierung
// =============================================================================

func (t *Token) validate() error {
	def := Def(t.Commodity)
	if def == nil {
		return fmt.Errorf("unknown commodity type %d – register it first", t.Commodity)
	}
	if t.MeterID == "" {
		return fmt.Errorf("meter_id required")
	}
	if t.Timestamp.IsZero() {
		return fmt.Errorf("timestamp required")
	}
	if t.Lat < -90 || t.Lat > 90 {
		return fmt.Errorf("lat out of range: %.6f", t.Lat)
	}
	if t.Lon < -180 || t.Lon > 180 {
		return fmt.Errorf("lon out of range: %.6f", t.Lon)
	}

	switch t.Commodity {
	case Electricity:
		if t.EnergyWs < 0 {
			return fmt.Errorf("energy_ws must be >= 0")
		}
	case Gas:
		if t.GasG < 0 {
			return fmt.Errorf("gas_g must be >= 0")
		}
		if t.GasCalorificX100 < def.CalorificMin || t.GasCalorificX100 > def.CalorificMax {
			return fmt.Errorf("gas_calorific_x100 out of range [%d,%d]: %d",
				def.CalorificMin, def.CalorificMax, t.GasCalorificX100)
		}
	case Water:
		if t.WaterG < 0 {
			return fmt.Errorf("water_g must be >= 0")
		}
	case Oil:
		if t.OilG < 0 {
			return fmt.Errorf("oil_g must be >= 0")
		}
		if t.OilCalorificX100 < def.CalorificMin || t.OilCalorificX100 > def.CalorificMax {
			return fmt.Errorf("oil_calorific_x100 out of range [%d,%d]: %d",
				def.CalorificMin, def.CalorificMax, t.OilCalorificX100)
		}
		if t.OilDensityG_mL > 0 && (t.OilDensityG_mL < 0.7 || t.OilDensityG_mL > 1.0) {
			return fmt.Errorf("oil_density_g_ml out of range [0.7,1.0]: %.3f", t.OilDensityG_mL)
		}
	default:
		// Metalle: nur MassG
		if t.MassG < 0 {
			return fmt.Errorf("mass_g must be >= 0")
		}
	}
	return nil
}

func (t *Token) calorificX100() uint32 {
	switch t.Commodity {
	case Gas: return t.GasCalorificX100
	case Oil: return t.OilCalorificX100
	default:  return 0
	}
}

// quantityForContract gibt die Menge als uint32 für den Smart Contract zurück.
// Einheiten: Ws für Strom, g für alles andere (Gas, Öl, Wasser, Metalle).
func (t *Token) quantityForContract() uint32 {
	switch t.Commodity {
	case Electricity: return uint32(math.Round(t.EnergyWs))
	case Gas:         return uint32(math.Round(t.GasG))
	case Water:       return uint32(math.Round(t.WaterG))
	case Oil:         return uint32(math.Round(t.OilG))
	default:          return uint32(math.Round(t.MassG))
	}
}

// =============================================================================
//  Smart-Contract-Kodierung
// =============================================================================

func (t *Token) EncodeID() (tokenID [32]byte, err error) {
	if err = t.validate(); err != nil {
		return
	}

	commodity := uint8(t.Commodity)
	timestamp := uint32(t.Timestamp.Unix())
	latE4     := int32(math.Round(t.Lat * 1e4))
	lonE4     := int32(math.Round(t.Lon * 1e4))
	quantity  := t.quantityForContract()
	cal       := t.calorificX100()

	tokenID[31] = commodity
	putUint32BE(tokenID[27:31], timestamp)
	putInt32BE(tokenID[23:27],  latE4)
	putInt32BE(tokenID[19:23],  lonE4)
	putUint32BE(tokenID[15:19], quantity)
	putUint32BE(tokenID[11:15], cal)
	copy(tokenID[0:11], []byte(t.MeterID)[:min11(len(t.MeterID))])

	return tokenID, nil
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func putUint32BE(b []byte, v uint32) {
	b[0] = byte(v >> 24); b[1] = byte(v >> 16)
	b[2] = byte(v >> 8);  b[3] = byte(v)
}

func putInt32BE(b []byte, v int32) { putUint32BE(b, uint32(v)) }

func min11(n int) int {
	if n < 11 { return n }
	return 11
}
