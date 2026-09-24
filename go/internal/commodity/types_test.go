package commodity_test

import (
	"math"
	"testing"
	"time"

	"github.com/fundus/node/internal/commodity"
)

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func approx(a, b, tol float64) bool { return math.Abs(a-b) < tol }

func baseToken(typ commodity.Type) *commodity.Token {
	return &commodity.Token{
		Commodity: typ, Timestamp: time.Now().UTC(), MeterID: "TEST001",
		Lat: 50.11, Lon: 8.68, GeneratorLat: 50.0, GeneratorLon: 8.0,
	}
}
func electricityToken(ws float64) *commodity.Token {
	t := baseToken(commodity.Electricity); t.EnergyWs = ws; return t
}
func gasToken(massG float64, calX100 uint32) *commodity.Token {
	t := baseToken(commodity.Gas)
	t.GasG = massG; t.GasCalorificX100 = calX100; t.GasType = "H"; t.GasPressureBar = 1.013
	return t
}
func waterToken(grams float64, quality commodity.WaterQuality) *commodity.Token {
	t := baseToken(commodity.Water); t.WaterG = grams; t.WaterQuality = quality; t.WaterTempC = 12.5; return t
}
func oilToken(massG float64, calX100 uint32) *commodity.Token {
	t := baseToken(commodity.Oil)
	t.OilG = massG; t.OilCalorificX100 = calX100; t.OilDensityG_mL = 0.845; t.OilGrade = "EL"
	return t
}
func metalToken(typ commodity.Type, massG float64) *commodity.Token {
	t := baseToken(typ); t.MassG = massG; return t
}

// =============================================================================
//  Registry
// =============================================================================

func TestRegistry_BuiltinTypes(t *testing.T) {
	types := []commodity.Type{
		commodity.Electricity, commodity.Gas, commodity.Water, commodity.Oil,
		commodity.Gold, commodity.Silver, commodity.Copper, commodity.Tin,
	}
	for _, typ := range types {
		if !commodity.IsRegistered(typ) {
			t.Errorf("type %d not registered", typ)
		}
		def := commodity.Def(typ)
		if def == nil { t.Errorf("Def(%d) = nil", typ); continue }
		if def.Name == "" { t.Errorf("type %d: empty Name", typ) }
		if def.Unit == "" { t.Errorf("type %d: empty Unit", typ) }
	}
}

func TestRegistry_UnknownType(t *testing.T) {
	if commodity.IsRegistered(commodity.Type(255)) {
		t.Error("type 255 should not be registered")
	}
}

func TestRegistry_Units(t *testing.T) {
	cases := map[commodity.Type]string{
		commodity.Electricity: "Ws",
		commodity.Gas:         "g",
		commodity.Water:       "g",
		commodity.Oil:         "g",
		commodity.Gold:        "g",
		commodity.Silver:      "g",
		commodity.Copper:      "g",
		commodity.Tin:         "g",
	}
	for typ, wantUnit := range cases {
		def := commodity.Def(typ)
		if def.Unit != wantUnit {
			t.Errorf("%s: Unit = %q, want %q", def.Name, def.Unit, wantUnit)
		}
	}
}

func TestRegistry_Categories(t *testing.T) {
	if commodity.Def(commodity.Electricity).Category != commodity.CategoryElectric {
		t.Error("Electricity: want CategoryElectric")
	}
	if commodity.Def(commodity.Gas).Category != commodity.CategoryFuel {
		t.Error("Gas: want CategoryFuel")
	}
	if commodity.Def(commodity.Water).Category != commodity.CategoryWater {
		t.Error("Water: want CategoryWater")
	}
	for _, typ := range []commodity.Type{commodity.Gold, commodity.Silver, commodity.Copper, commodity.Tin} {
		if commodity.Def(typ).Category != commodity.CategoryMetal {
			t.Errorf("%d: want CategoryMetal", typ)
		}
	}
}

func TestRegistry_ByCategory_Metals_AtLeast4(t *testing.T) {
	metals := commodity.ByCategory(commodity.CategoryMetal)
	if len(metals) < 4 {
		t.Errorf("expected >= 4 metals, got %d", len(metals))
	}
}

func TestRegistry_All_NotEmpty(t *testing.T) {
	all := commodity.All()
	if len(all) < 8 {
		t.Errorf("All() returned %d, want >= 8", len(all))
	}
}

func TestRegistry_DuplicateRegistration_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate registration")
		}
	}()
	commodity.Register(&commodity.CommodityDef{Type: commodity.Gold, Name: "Dup", Unit: "g"})
}

// =============================================================================
//  Strom – Einheit Ws (Watt-Sekunden)
// =============================================================================

func TestElectricity_EnergyKWh_1kWh(t *testing.T) {
	// 1 kWh = 3.600.000 Ws
	tok := electricityToken(3_600_000)
	if !approx(tok.EnergyKWh(), 1.0, 0.0001) {
		t.Errorf("EnergyKWh(3600000 Ws) = %.6f, want 1.0", tok.EnergyKWh())
	}
}

func TestElectricity_EnergyWh_From_Ws(t *testing.T) {
	// 3600 Ws = 1 Wh
	tok := electricityToken(3600)
	if !approx(tok.EnergyWh(), 1.0, 0.0001) {
		t.Errorf("EnergyWh(3600 Ws) = %.6f, want 1.0 Wh", tok.EnergyWh())
	}
}

func TestElectricity_Quantity_In_Ws(t *testing.T) {
	tok := electricityToken(18_000)
	if !approx(tok.Quantity(), 18_000, 0.001) {
		t.Errorf("Quantity = %.1f, want 18000 Ws", tok.Quantity())
	}
}

func TestElectricity_CO2(t *testing.T) {
	// 1 kWh Strom, DE-Strommix ~0.366 kg CO2/kWh
	tok := electricityToken(3_600_000)
	co2 := tok.CO2EquivalentKg()
	if !approx(co2, 0.366, 0.05) {
		t.Errorf("CO2(1 kWh) = %.4f, want ~0.366", co2)
	}
}

func TestElectricity_1Wh_Equals_3600Ws(t *testing.T) {
	tok := electricityToken(3600)
	wh := tok.EnergyWh()
	if !approx(wh, 1.0, 0.0001) {
		t.Errorf("3600 Ws EnergyWh = %.4f, want 1.0", wh)
	}
}

func TestElectricity_SmallReading(t *testing.T) {
	// Typischer 1-Sekunden-Smartmeter-Wert bei 3 kW Last: 3000 Ws
	tok := electricityToken(3000)
	wh := tok.EnergyWh()
	if !approx(wh, 3000.0/3600, 0.0001) {
		t.Errorf("3000 Ws EnergyWh = %.6f, want %.6f", wh, 3000.0/3600)
	}
}

// =============================================================================
//  Wasser – Einheit L
// =============================================================================

func TestWater_Quantity_In_Grams(t *testing.T) {
	// 12500 g = 12.5 kg = 12.5 L (Dichte Wasser ≈ 1 g/mL = 1 kg/L)
	tok := waterToken(12500, commodity.WaterDrinking)
	if !approx(tok.Quantity(), 12500, 0.001) {
		t.Errorf("Quantity = %.4f, want 12500 g", tok.Quantity())
	}
}

func TestWater_EnergyKWh_Zero(t *testing.T) {
	if waterToken(100000, commodity.WaterDrinking).EnergyKWh() != 0 {
		t.Error("Water EnergyKWh should be 0")
	}
}

func TestWater_CO2_Zero(t *testing.T) {
	if waterToken(100000, commodity.WaterDrinking).CO2EquivalentKg() != 0 {
		t.Error("Water CO2 should be 0")
	}
}

func TestWater_Quality_Strings(t *testing.T) {
	cases := []struct {
		q commodity.WaterQuality; s string
	}{
		{commodity.WaterDrinking, "drinking"}, {commodity.WaterProcess, "process"},
		{commodity.WaterGrey, "grey"}, {commodity.WaterWaste, "waste"},
	}
	for _, c := range cases {
		if c.q.String() != c.s {
			t.Errorf("WaterQuality %d = %q, want %q", c.q, c.q.String(), c.s)
		}
	}
}

func TestWater_EncodeID_NoConversion(t *testing.T) {
	// 1500 g Wasser → on-chain uint32: 1500
	tok := waterToken(1500, commodity.WaterDrinking)
	tok.MeterID = "WAT001"; tok.Timestamp = time.Now()
	id, err := tok.EncodeID()
	if err != nil { t.Fatalf("EncodeID: %v", err) }
	var empty [32]byte
	if id == empty { t.Error("EncodeID returned zero ID") }
}

// =============================================================================
//  Erdgas
// =============================================================================

func TestGas_EnergyKWh(t *testing.T) {
	tok := gasToken(1000, 1390) // 1 kg * 13.9 kWh/kg
	if !approx(tok.EnergyKWh(), 13.9, 0.01) {
		t.Errorf("Gas EnergyKWh(1000g) = %.4f, want ~13.9", tok.EnergyKWh())
	}
}

func TestGas_MassKg(t *testing.T) {
	tok := gasToken(2500, 1390)
	if !approx(tok.MassKg(), 2.5, 0.001) {
		t.Errorf("MassKg = %.4f, want 2.5", tok.MassKg())
	}
}

func TestGas_Quantity_Grams(t *testing.T) {
	tok := gasToken(750, 1390)
	if !approx(tok.Quantity(), 750, 0.001) {
		t.Errorf("Quantity = %.4f, want 750 g", tok.Quantity())
	}
}

// =============================================================================
//  Heizöl
// =============================================================================

func TestOil_EnergyKWh(t *testing.T) {
	tok := oilToken(1000, 1186) // 1 kg * 11.86 kWh/kg
	if !approx(tok.EnergyKWh(), 11.86, 0.05) {
		t.Errorf("Oil EnergyKWh(1000g) = %.4f, want ~11.86", tok.EnergyKWh())
	}
}

func TestOil_Quantity_Grams(t *testing.T) {
	tok := oilToken(845, 1186)
	if !approx(tok.Quantity(), 845, 0.001) {
		t.Errorf("Quantity = %.4f, want 845 g", tok.Quantity())
	}
}

// =============================================================================
//  Metalle
// =============================================================================

func TestGold_Quantity(t *testing.T) {
	tok := metalToken(commodity.Gold, 31.1035) // 1 Troy-Unze
	if !approx(tok.Quantity(), 31.1035, 0.001) {
		t.Errorf("Gold Quantity = %.4f, want 31.1035 g", tok.Quantity())
	}
}

func TestSilver_MassKg(t *testing.T) {
	tok := metalToken(commodity.Silver, 5000) // 5 kg
	if !approx(tok.MassKg(), 5.0, 0.001) {
		t.Errorf("Silver MassKg = %.4f, want 5.0", tok.MassKg())
	}
}

func TestCopper_EnergyKWh_Zero(t *testing.T) {
	if metalToken(commodity.Copper, 1000).EnergyKWh() != 0 {
		t.Error("Copper EnergyKWh should be 0")
	}
}

func TestTin_CO2_Zero(t *testing.T) {
	if metalToken(commodity.Tin, 500).CO2EquivalentKg() != 0 {
		t.Error("Tin CO2 should be 0")
	}
}

func TestMetals_AllUnitsGrams(t *testing.T) {
	for _, typ := range []commodity.Type{commodity.Gold, commodity.Silver, commodity.Copper, commodity.Tin} {
		def := commodity.Def(typ)
		if def.Unit != "g" {
			t.Errorf("%s Unit = %q, want 'g'", def.Name, def.Unit)
		}
	}
}

// =============================================================================
//  Konsistenz über alle Typen
// =============================================================================

func TestAllTypes_Quantity_NonNegative(t *testing.T) {
	tokens := []*commodity.Token{
		electricityToken(0), gasToken(0, 1390), waterToken(0, commodity.WaterDrinking),
		oilToken(0, 1186), metalToken(commodity.Gold, 0),
	}
	for _, tok := range tokens {
		if tok.Quantity() < 0 {
			t.Errorf("%s Quantity() < 0", commodity.Def(tok.Commodity).Name)
		}
	}
}

// =============================================================================
//  EncodeID
// =============================================================================

func TestEncodeID_AllBuiltinTypes(t *testing.T) {
	tokens := []*commodity.Token{
		electricityToken(3600), gasToken(500, 1390), waterToken(10000, commodity.WaterDrinking),
		oilToken(845, 1186), metalToken(commodity.Gold, 31.1), metalToken(commodity.Silver, 1000),
		metalToken(commodity.Copper, 5000), metalToken(commodity.Tin, 2500),
	}
	for _, tok := range tokens {
		tok.MeterID = "TEST001"; tok.Timestamp = time.Now()
		_, err := tok.EncodeID()
		name := commodity.Def(tok.Commodity).Name
		if err != nil { t.Errorf("EncodeID(%s): %v", name, err) }
	}
}

func TestEncodeID_UnknownType_Error(t *testing.T) {
	tok := &commodity.Token{Commodity: commodity.Type(99), MeterID: "X", Timestamp: time.Now()}
	if _, err := tok.EncodeID(); err == nil {
		t.Error("unknown commodity should return error")
	}
}

func TestEncodeID_Deterministic(t *testing.T) {
	tok := gasToken(1000, 1390)
	tok.Timestamp = time.Unix(1700000000, 0)
	id1, _ := tok.EncodeID()
	id2, _ := tok.EncodeID()
	if id1 != id2 { t.Error("EncodeID not deterministic") }
}

func TestEncodeID_DifferentQuantity_DifferentID(t *testing.T) {
	ts := time.Unix(1700000000, 0)
	t1 := gasToken(1000, 1390); t1.Timestamp = ts
	t2 := gasToken(2000, 1390); t2.Timestamp = ts
	id1, _ := t1.EncodeID()
	id2, _ := t2.EncodeID()
	if id1 == id2 { t.Error("different quantities should give different IDs") }
}

// =============================================================================
//  CalorificRef
// =============================================================================

func TestCalorificRef_Values(t *testing.T) {
	if !approx(commodity.CalorificRef.GasH.KWhPerKg, 13.9, 0.5) {
		t.Errorf("GasH = %.2f, want ~13.9", commodity.CalorificRef.GasH.KWhPerKg)
	}
	if commodity.CalorificRef.GasH.ToX100() != 1390 {
		t.Errorf("GasH.ToX100() = %d, want 1390", commodity.CalorificRef.GasH.ToX100())
	}
}
