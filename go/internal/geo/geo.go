// Package geo enthält geographische Berechnungen für den Fundus-Marktplatz.
package geo

import "math"

// HaversineKm berechnet die Großkreis-Distanz zwischen zwei GPS-Koordinaten
// nach der Haversine-Formel. Ergebnis in Kilometern.
func HaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// GridFeeRatio berechnet den Netzgebühren-Anteil aus der Distanz in km.
// Modell: 0,5 % pro km, Minimum 0,1 %, Maximum 15 %.
func GridFeeRatio(distKm float64) float64 {
	ratio := distKm * 0.005
	if ratio > 0.15 {
		return 0.15
	}
	if ratio < 0.001 {
		return 0.001
	}
	return ratio
}

// GridFee berechnet die Netzgebühr in kWh für eine gegebene Energiemenge
// und die GPS-Koordinaten von Erzeuger und Verbraucher.
func GridFee(kWh, genLat, genLon, conLat, conLon float64) (feeKWh, netKWh float64, distKm float64) {
	distKm = HaversineKm(genLat, genLon, conLat, conLon)
	ratio  := GridFeeRatio(distKm)
	feeKWh  = kWh * ratio
	netKWh  = kWh - feeKWh
	return
}

// =============================================================================
//  Geohash – räumliche Indizierung für skalierbare Geo-Suche
// =============================================================================
//
// Geohash kodiert (lat, lon) in einen kurzen String. Wichtigste Eigenschaft:
// benachbarte Orte teilen sich ein gemeinsames Präfix. So wird Umkreis-Suche
// zur Präfix-Suche, und Angebote können regional im DHT verteilt werden.
//
// Präzision (Länge → ungefähre Zellengröße):
//   4 Zeichen ≈ 40 km   (Stadtregion)
//   5 Zeichen ≈ 5 km    (Stadtteil)
//   6 Zeichen ≈ 1 km    (Nachbarschaft)

const geohashBase32 = "0123456789bcdefghjkmnpqrstuvwxyz"

// GeohashEncode kodiert Koordinaten in einen Geohash der gegebenen Länge.
func GeohashEncode(lat, lon float64, precision int) string {
	if precision < 1 {
		precision = 5
	}
	var (
		latMin, latMax = -90.0, 90.0
		lonMin, lonMax = -180.0, 180.0
		hash           []byte
		bit            int
		ch             int
		even           = true
	)
	for len(hash) < precision {
		if even { // Längengrad
			mid := (lonMin + lonMax) / 2
			if lon >= mid {
				ch |= 1 << (4 - bit)
				lonMin = mid
			} else {
				lonMax = mid
			}
		} else { // Breitengrad
			mid := (latMin + latMax) / 2
			if lat >= mid {
				ch |= 1 << (4 - bit)
				latMin = mid
			} else {
				latMax = mid
			}
		}
		even = !even
		if bit < 4 {
			bit++
		} else {
			hash = append(hash, geohashBase32[ch])
			bit = 0
			ch = 0
		}
	}
	return string(hash)
}

// GeohashNeighbors gibt die 8 Nachbar-Geohashes derselben Präzision zurück
// (plus die Zelle selbst → 9 Zellen). Damit deckt eine Umkreis-Suche auch
// Angebote ab, die knapp jenseits der Zellgrenze liegen.
func GeohashNeighbors(hash string) []string {
	if hash == "" {
		return nil
	}
	// Zelle dekodieren, leicht versetzte Punkte neu kodieren.
	latMin, latMax, lonMin, lonMax := geohashBounds(hash)
	latC := (latMin + latMax) / 2
	lonC := (lonMin + lonMax) / 2
	dLat := latMax - latMin
	dLon := lonMax - lonMin
	prec := len(hash)

	seen := map[string]bool{hash: true}
	out := []string{hash}
	for _, dy := range []float64{-1, 0, 1} {
		for _, dx := range []float64{-1, 0, 1} {
			if dx == 0 && dy == 0 {
				continue
			}
			nh := GeohashEncode(latC+dy*dLat, lonC+dx*dLon, prec)
			if !seen[nh] {
				seen[nh] = true
				out = append(out, nh)
			}
		}
	}
	return out
}

// geohashBounds dekodiert die Begrenzungsbox eines Geohashes.
func geohashBounds(hash string) (latMin, latMax, lonMin, lonMax float64) {
	latMin, latMax = -90.0, 90.0
	lonMin, lonMax = -180.0, 180.0
	even := true
	for i := 0; i < len(hash); i++ {
		idx := indexInBase32(hash[i])
		if idx < 0 {
			continue
		}
		for bit := 4; bit >= 0; bit-- {
			b := (idx >> bit) & 1
			if even {
				mid := (lonMin + lonMax) / 2
				if b == 1 {
					lonMin = mid
				} else {
					lonMax = mid
				}
			} else {
				mid := (latMin + latMax) / 2
				if b == 1 {
					latMin = mid
				} else {
					latMax = mid
				}
			}
			even = !even
		}
	}
	return
}

func indexInBase32(c byte) int {
	for i := 0; i < len(geohashBase32); i++ {
		if geohashBase32[i] == c {
			return i
		}
	}
	return -1
}

// PrecisionForRadiusKm wählt eine sinnvolle Geohash-Länge für einen Suchradius.
// Kürzeres Präfix = größere Zelle = größerer Radius.
func PrecisionForRadiusKm(radiusKm float64) int {
	switch {
	case radiusKm > 80:
		return 3 // ~150 km
	case radiusKm > 20:
		return 4 // ~40 km
	case radiusKm > 4:
		return 5 // ~5 km
	default:
		return 6 // ~1 km
	}
}
