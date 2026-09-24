package geo_test

import (
	"math"
	"testing"

	"github.com/fundus/node/internal/geo"
)

// round rundet auf n Nachkommastellen für Vergleiche.
func round(v float64, places int) float64 {
	shift := math.Pow10(places)
	return math.Round(v*shift) / shift
}

// =============================================================================
//  HaversineKm
// =============================================================================

func TestHaversineKm(t *testing.T) {
	tests := []struct {
		name         string
		lat1, lon1   float64
		lat2, lon2   float64
		wantKm       float64
		toleranceKm  float64
	}{
		{
			name:        "Gleicher Punkt – Distanz 0",
			lat1: 50.0, lon1: 8.0, lat2: 50.0, lon2: 8.0,
			wantKm: 0, toleranceKm: 0.001,
		},
		{
			name:        "Frankfurt–München (~305 km)",
			lat1: 50.1109, lon1: 8.6821,  // Frankfurt
			lat2: 48.1351, lon2: 11.5820, // München
			wantKm: 305, toleranceKm: 5,
		},
		{
			name:        "Berlin–Hamburg (~255 km)",
			lat1: 52.5200, lon1: 13.4050, // Berlin
			lat2: 53.5753, lon2: 10.0153, // Hamburg
			wantKm: 255, toleranceKm: 5,
		},
		{
			name:        "Köln–Dortmund (~75 km)",
			lat1: 50.9333, lon1: 6.9500,  // Köln
			lat2: 51.5136, lon2: 7.4653,  // Dortmund
			wantKm: 75, toleranceKm: 5,
		},
		{
			name:        "Äquator–Polabstand (~10000 km)",
			lat1: 0, lon1: 0, lat2: 90, lon2: 0,
			wantKm: 10008, toleranceKm: 50,
		},
		{
			name:        "Antipodenpunkte (~20000 km)",
			lat1: 0, lon1: 0, lat2: 0, lon2: 180,
			wantKm: 20015, toleranceKm: 50,
		},
		{
			name:        "Sehr kurze Distanz (100 m)",
			lat1: 50.0, lon1: 8.0,
			lat2: 50.0009, lon2: 8.0, // ~100 m nördlich
			wantKm: 0.1, toleranceKm: 0.02,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := geo.HaversineKm(tc.lat1, tc.lon1, tc.lat2, tc.lon2)
			diff := math.Abs(got - tc.wantKm)
			if diff > tc.toleranceKm {
				t.Errorf("HaversineKm(%v,%v → %v,%v) = %.2f km, want %.2f ± %.2f km",
					tc.lat1, tc.lon1, tc.lat2, tc.lon2,
					got, tc.wantKm, tc.toleranceKm)
			}
		})
	}
}

// Haversine muss symmetrisch sein: d(A,B) == d(B,A)
func TestHaversineKm_Symmetry(t *testing.T) {
	cases := [][4]float64{
		{52.52, 13.40, 48.14, 11.58},
		{0, 0, 45, 90},
		{-33.87, 151.21, 51.51, -0.13}, // Sydney–London
	}
	for _, c := range cases {
		d1 := geo.HaversineKm(c[0], c[1], c[2], c[3])
		d2 := geo.HaversineKm(c[2], c[3], c[0], c[1])
		if math.Abs(d1-d2) > 0.001 {
			t.Errorf("not symmetric: %.6f vs %.6f", d1, d2)
		}
	}
}

// =============================================================================
//  GridFeeRatio
// =============================================================================

func TestGridFeeRatio(t *testing.T) {
	tests := []struct {
		name    string
		distKm  float64
		wantMin float64
		wantMax float64
	}{
		{"0 km → Minimum", 0, 0.001, 0.001},
		{"1 km → 0.5%", 1, 0.005, 0.005},
		{"10 km → 5%", 10, 0.05, 0.05},
		{"20 km → 10%", 20, 0.10, 0.10},
		{"30 km → 15% (cap)", 30, 0.15, 0.15},
		{"100 km → 15% (cap bleibt)", 100, 0.15, 0.15},
		{"0.1 km → Minimum", 0.1, 0.001, 0.001},
		{"0.2 km → 0.1%", 0.2, 0.001, 0.001},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := geo.GridFeeRatio(tc.distKm)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("GridFeeRatio(%.1f km) = %.4f, want [%.4f, %.4f]",
					tc.distKm, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

// GridFeeRatio darf nie negativ sein und nie über 0.15 liegen
func TestGridFeeRatio_Bounds(t *testing.T) {
	for _, km := range []float64{-100, -1, 0, 0.001, 1, 10, 50, 100, 1000} {
		r := geo.GridFeeRatio(km)
		if r < 0 {
			t.Errorf("GridFeeRatio(%.1f) = %.6f, must not be negative", km, r)
		}
		if r > 0.15 {
			t.Errorf("GridFeeRatio(%.1f) = %.6f, must not exceed 0.15", km, r)
		}
	}
}

// =============================================================================
//  GridFee
// =============================================================================

func TestGridFee(t *testing.T) {
	tests := []struct {
		name                         string
		kWh                          float64
		genLat, genLon, conLat, conLon float64
		wantFeeMin, wantFeeMax       float64 // Erwartete Gebühr in kWh
	}{
		{
			name:   "Gleicher Ort – Minimum-Gebühr",
			kWh:    100,
			genLat: 50.0, genLon: 8.0, conLat: 50.0, conLon: 8.0,
			// 0 km → 0.1 % → 0.1 kWh
			wantFeeMin: 0.09, wantFeeMax: 0.11,
		},
		{
			name:   "10 km – 5% Gebühr",
			kWh:    100,
			genLat: 50.0, genLon: 8.0,
			// ~10 km östlich
			conLat: 50.0, conLon: 8.1,
			wantFeeMin: 2.0, wantFeeMax: 4.0,
		},
		{
			name:   "100 km – 15% Gebühr (Cap)",
			kWh:    100,
			genLat: 50.0, genLon: 8.0,
			conLat: 51.0, conLon: 8.0, // ~111 km nördlich
			wantFeeMin: 14.5, wantFeeMax: 15.1,
		},
		{
			name:   "Kleinste Einheit – kWh bleibt positiv",
			kWh:    0.001,
			genLat: 50.0, genLon: 8.0, conLat: 50.0, conLon: 8.0,
			wantFeeMin: 0, wantFeeMax: 0.001,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fee, net, dist := geo.GridFee(tc.kWh, tc.genLat, tc.genLon, tc.conLat, tc.conLon)

			if fee < tc.wantFeeMin || fee > tc.wantFeeMax {
				t.Errorf("fee = %.4f kWh, want [%.4f, %.4f]",
					fee, tc.wantFeeMin, tc.wantFeeMax)
			}

			// Invariante: fee + net == kWh (innerhalb float64-Genauigkeit)
			if math.Abs(fee+net-tc.kWh) > 1e-9 {
				t.Errorf("fee + net = %.10f, want %.10f (kWh)", fee+net, tc.kWh)
			}

			// Distanz muss >= 0 sein
			if dist < 0 {
				t.Errorf("dist = %.4f km, must not be negative", dist)
			}
		})
	}
}

// Gebühr darf nie größer als der Gesamtbetrag sein
func TestGridFee_FeeNeverExceedsTotal(t *testing.T) {
	for _, kwh := range []float64{0.001, 1, 100, 10000} {
		fee, net, _ := geo.GridFee(kwh, 0, 0, 10, 10)
		if fee > kwh {
			t.Errorf("fee %.6f > kWh %.6f", fee, kwh)
		}
		if net < 0 {
			t.Errorf("net kWh %.6f < 0 for input %.6f", net, kwh)
		}
	}
}
