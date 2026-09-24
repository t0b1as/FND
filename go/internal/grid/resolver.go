package grid

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/argon2"
	"lukechampine.com/blake3"
)

// =============================================================================
//  PLZ → Bundesland
// =============================================================================
// Quelle: Deutsche Post AG / Statistisches Bundesamt
// Zuordnung der 2-stelligen PLZ-Präfixe zu Bundesland-Kürzeln

var plzToState = map[string]string{
	// Bayern
	"80": "BY", "81": "BY", "82": "BY", "83": "BY", "84": "BY",
	"85": "BY", "86": "BY", "87": "BY", "88": "BY", "89": "BY",
	"90": "BY", "91": "BY", "92": "BY", "93": "BY", "94": "BY",
	"95": "BY", "96": "BY", "97": "BY",
	// Baden-Württemberg
	"68": "BW", "69": "BW", "70": "BW", "71": "BW", "72": "BW",
	"73": "BW", "74": "BW", "75": "BW", "76": "BW", "77": "BW",
	"78": "BW", "79": "BW",
	// NRW
	"40": "NW", "41": "NW", "42": "NW", "44": "NW", "45": "NW",
	"46": "NW", "47": "NW", "48": "NW", "49": "NW", "50": "NW",
	"51": "NW", "52": "NW", "53": "NW", "57": "NW", "58": "NW", "59": "NW",
	// Hessen
	"34": "HE", "35": "HE", "36": "HE", "60": "HE", "61": "HE",
	"63": "HE", "64": "HE", "65": "HE",
	// Niedersachsen
	"26": "NI", "27": "NI", "28": "NI", "29": "NI",
	"30": "NI", "31": "NI", "37": "NI", "38": "NI",
	// Rheinland-Pfalz
	"54": "RP", "55": "RP", "56": "RP", "67": "RP",
	// Saarland
	"66": "SL",
	// Sachsen
	"01": "SN", "02": "SN", "04": "SN", "08": "SN", "09": "SN",
	// Sachsen-Anhalt
	"06": "ST", "39": "ST",
	// Thüringen
	"07": "TH", "99": "TH",
	// Brandenburg
	"03": "BB", "14": "BB", "15": "BB", "16": "BB",
	// Mecklenburg-Vorpommern
	"17": "MV", "18": "MV", "19": "MV",
	// Schleswig-Holstein
	"23": "SH", "24": "SH", "25": "SH",
	// Berlin
	"10": "BE", "12": "BE", "13": "BE",
	// Hamburg
	"20": "HH", "21": "HH", "22": "HH",
	// Sonderfälle (Grenzgebiete)
	"32": "NW", // Herford (NW)
	"33": "NW", // Paderborn (NW)
	"43": "NW", // Kirchhundem (NW, veraltet)
	"62": "HE", // Limburg
	"98": "TH", // Schmalkalden
}

// StateForPLZ gibt das Bundesland-Kürzel für eine PLZ zurück.
// Gibt "" zurück wenn unbekannt.
func StateForPLZ(plz string) string {
	if len(plz) < 2 {
		return ""
	}
	return plzToState[plz[:2]]
}

// =============================================================================
//  PLZ → Netzbetreiber Lookup
// =============================================================================

// LookupResult enthält den gefundenen Netzbetreiber und Qualitätshinweise.
type LookupResult struct {
	Operator  *Operator
	PLZ       string
	State     string
	MatchType string // "plz_exact" | "plz_prefix" | "state_fallback" | "not_found"
	Note      string // Hinweis bei Unsicherheiten
}

// FindOperator gibt den wahrscheinlichsten Netzbetreiber für eine PLZ und
// Sparte zurück. Matching-Strategie:
//  1. PLZ-Präfix exakt in Operator.PLZPrefixes
//  2. Bundesland-Fallback über States
//  3. nil wenn keine Übereinstimmung
func FindOperator(plz string, commodity Commodity) LookupResult {
	if len(plz) < 4 {
		return LookupResult{MatchType: "not_found", PLZ: plz, Note: "PLZ zu kurz"}
	}

	// PLZ normalisieren (nur Ziffern, 5-stellig)
	plz = strings.TrimSpace(plz)
	if len(plz) < 5 {
		plz = fmt.Sprintf("%05s", plz)
	}

	prefix2 := plz[:2]
	prefix3 := plz[:3]
	state   := StateForPLZ(plz)

	operators := operatorsForCommodity(commodity)

	// 1. Exakter 3-stelliger Präfix-Match
	for i, op := range operators {
		for _, p := range op.PLZPrefixes {
			if p == prefix3 {
				return LookupResult{
					Operator:  &operators[i],
					PLZ:       plz,
					State:     state,
					MatchType: "plz_prefix",
				}
			}
		}
	}

	// 2. 2-stelliger Präfix-Match
	for i, op := range operators {
		for _, p := range op.PLZPrefixes {
			if p == prefix2 {
				return LookupResult{
					Operator:  &operators[i],
					PLZ:       plz,
					State:     state,
					MatchType: "plz_prefix",
				}
			}
		}
	}

	// 3. Bundesland-Fallback
	if state != "" {
		for i, op := range operators {
			for _, s := range op.States {
				if s == state {
					return LookupResult{
						Operator:  &operators[i],
						PLZ:       plz,
						State:     state,
						MatchType: "state_fallback",
						Note:      fmt.Sprintf("Bundesland-Fallback für %s; für genaue Zuordnung VNBdigital.de prüfen", state),
					}
				}
			}
		}
	}

	return LookupResult{
		MatchType: "not_found",
		PLZ:       plz,
		State:     state,
		Note:      "Kein Netzbetreiber gefunden – prüfe VNBdigital.de oder gasnetzbetreiber.de",
	}
}

// FindAllOperators gibt alle Netzbetreiber für eine PLZ über alle Sparten zurück.
// Typisches Ergebnis: 6-8 Betreiber (Strom HV+MV+LV, Gas HV+LV, Wasser)
func FindAllOperators(plz string) []LookupResult {
	commodities := []Commodity{
		CommodityElectricityHV,
		CommodityElectricityMV,
		CommodityElectricityLV,
		CommodityGasHV,
		CommodityGasMV,
		CommodityGasLV,
		CommodityWater,
	}
	results := make([]LookupResult, 0, len(commodities))
	seen := map[string]bool{}

	for _, c := range commodities {
		r := FindOperator(plz, c)
		if r.Operator == nil {
			continue
		}
		// Duplikate vermeiden (MV/LV oft gleicher Betreiber)
		if seen[r.Operator.ID] {
			continue
		}
		seen[r.Operator.ID] = true
		results = append(results, r)
	}
	return results
}

// operatorsForCommodity gibt die passende Operator-Liste zurück.
func operatorsForCommodity(c Commodity) []Operator {
	switch c {
	case CommodityElectricityHV:
		return electricityHVOperators
	case CommodityElectricityMV, CommodityElectricityLV:
		return electricityMVLVOperators
	case CommodityGasHV:
		return gasHVOperators
	case CommodityGasMV, CommodityGasLV:
		return gasMVLVOperators
	case CommodityWater:
		return waterOperators
	default:
		return nil
	}
}

// =============================================================================
//  Wallet-Ableitung pro Netzbetreiber
// =============================================================================
// Argon2id(adminSeed + operatorID, salt=BLAKE3("fundus-grid-v2:"+operatorID))
// Deterministisch: gleicher Seed + gleicher Operator = gleiche Adresse, immer.
// v2: 512 MiB statt 64 MiB – konsistent mit identity.go, NVL72-resistent.

const (
	gridArgon2SaltDomain = "fundus-grid-v2:" // finaler Wert – nie mehr ändern
	gridArgon2Memory     = 512 * 1024        // 512 MiB – ~11s pro Wallet auf Pi 3
	gridArgon2Time       = 4
	gridArgon2Thread     = 4
	gridArgon2KeyLen     = 32
)

// OperatorWallet enthält die abgeleitete Wallet-Adresse eines Netzbetreibers.
type OperatorWallet struct {
	OperatorID string
	Name       string
	Commodity  Commodity
	Address    string // Gnosis-Chain EIP-55 Adresse
	Salt       string // genutzter Argon2id-Salt (zur Verifikation)
}

// DeriveWallet leitet eine deterministische Gnosis-Chain-Wallet für einen
// Netzbetreiber ab.
//
// adminSeedWords: die 30 Fundus-Seed-Wörter (identisch zu fnd-wallet).
// Der Private Key wird nicht gespeichert – nur die öffentliche Adresse.
func DeriveWallet(op *Operator, adminSeedWords []string) (*OperatorWallet, error) {
	if op == nil {
		return nil, fmt.Errorf("grid: nil operator")
	}
	if len(adminSeedWords) == 0 {
		return nil, fmt.Errorf("grid: keine Seed-Wörter angegeben")
	}

	// Passwort: AdminSeed + Operator-ID
	password := []byte(strings.Join(adminSeedWords, "\n") + "\n" + op.ID)

	// Salt: BLAKE3(domain + operatorID) → feste 32 Bytes
	// Kein Length-Extension-Risiko, uniform unabhängig von ID-Länge
	h := blake3.New(32, nil)
	h.Write([]byte(gridArgon2SaltDomain))
	h.Write([]byte(op.ID))
	salt := h.Sum(nil)

	// Argon2id → Private Key
	keyBytes := argon2.IDKey(password, salt, gridArgon2Time, gridArgon2Memory, gridArgon2Thread, gridArgon2KeyLen)

	// secp256k1 → Ethereum-Adresse
	privKey, err := crypto.ToECDSA(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("grid: Schlüsselableitung für %s: %w", op.ID, err)
	}
	addr := crypto.PubkeyToAddress(privKey.PublicKey)

	// Private Key sofort löschen (best-effort)
	privKey.D.SetInt64(0)

	return &OperatorWallet{
		OperatorID: op.ID,
		Name:       op.Name,
		Commodity:  op.Commodity,
		Address:    addr.Hex(),
		Salt:       fmt.Sprintf("%s%s (BLAKE3→%x)", gridArgon2SaltDomain, op.ID, salt[:4]),
	}, nil
}

// DeriveAllWallets leitet Wallets für alle bekannten Netzbetreiber ab.
// Dauert ~11s pro Wallet auf dem Pi (Argon2id v2, 512 MiB).
// Bei ~100 Betreibern: ca. 18 Minuten – einmalig bei der Initialisierung.
func DeriveAllWallets(adminSeedWords []string, progress func(done, total int)) ([]*OperatorWallet, error) {
	ops := AllOperators()
	wallets := make([]*OperatorWallet, 0, len(ops))

	for i, op := range ops {
		w, err := DeriveWallet(&op, adminSeedWords)
		if err != nil {
			return nil, fmt.Errorf("grid: Wallet für %s: %w", op.ID, err)
		}
		wallets = append(wallets, w)
		if progress != nil {
			progress(i+1, len(ops))
		}
	}
	return wallets, nil
}

// WalletForPLZ kombiniert FindOperator + DeriveWallet.
// Gibt Operator + Wallet-Adresse für eine PLZ und Sparte zurück.
func WalletForPLZ(plz string, commodity Commodity, adminSeedWords []string) (*LookupResult, *OperatorWallet, error) {
	result := FindOperator(plz, commodity)
	if result.Operator == nil {
		return &result, nil, fmt.Errorf("grid: kein Betreiber für PLZ %s, Sparte %s", plz, commodity)
	}
	wallet, err := DeriveWallet(result.Operator, adminSeedWords)
	if err != nil {
		return &result, nil, err
	}
	return &result, wallet, nil
}

// OperatorIDFromBDEW erzeugt die interne ID aus einem BDEW-Code.
func OperatorIDFromBDEW(code string) string {
	return "BDEW-" + strings.TrimLeft(code, "0")
}

// OperatorIDFromDVGW erzeugt die interne ID aus einem DVGW-Code.
func OperatorIDFromDVGW(code string) string {
	return "DVGW-" + strings.TrimLeft(code, "0")
}

// WalletAddressShort gibt die Kurzform einer Wallet-Adresse zurück (0x1234…5678).
func WalletAddressShort(addr string) string {
	if len(addr) < 12 {
		return addr
	}
	return addr[:8] + "…" + addr[len(addr)-4:]
}


// =============================================================================
//  PLZ-Präfix → Geografische Zentroid-Koordinaten
// =============================================================================
// Quelle: Deutsche Post PLZ-Gebiete, Statistisches Bundesamt
// Präzision: Mittelpunkt des PLZ-Bereichs, ausreichend für Luftlinien-Schätzung

// plzCentroid gibt den geografischen Mittelpunkt eines 2-stelligen PLZ-Präfixes.
// Dient als Proxy für die Lage eines Netzgebiets.
var plzCentroid = map[string][2]float64{
	// Format: "XX": {Lat, Lon}
	// Bayern
	"80": {48.14, 11.58}, "81": {48.10, 11.65}, "82": {48.00, 11.30},
	"83": {47.82, 12.11}, "84": {48.25, 12.67}, "85": {48.17, 11.73},
	"86": {48.37, 10.88}, "87": {47.73, 10.32}, "88": {47.74, 9.61},
	"89": {48.40, 10.01}, "90": {49.45, 11.08}, "91": {49.42, 11.00},
	"92": {49.52, 12.15}, "93": {48.96, 12.57}, "94": {48.57, 13.46},
	"95": {50.03, 11.58}, "96": {49.90, 10.90}, "97": {49.79, 9.94},
	// Baden-Württemberg
	"68": {49.49, 8.47}, "69": {49.41, 8.69}, "70": {48.78, 9.18},
	"71": {48.83, 9.50}, "72": {48.49, 9.21}, "73": {48.80, 9.82},
	"74": {49.13, 9.22}, "75": {48.90, 8.71}, "76": {49.01, 8.40},
	"77": {48.46, 7.96}, "78": {47.96, 8.48}, "79": {47.99, 7.84},
	// NRW
	"40": {51.23, 6.79}, "41": {51.17, 6.44}, "42": {51.26, 7.15},
	"44": {51.51, 7.46}, "45": {51.46, 7.01}, "46": {51.67, 6.62},
	"47": {51.33, 6.57}, "48": {51.96, 7.63}, "49": {52.28, 8.04},
	"50": {50.94, 6.96}, "51": {51.03, 7.22}, "52": {50.77, 6.09},
	"53": {50.74, 7.10}, "57": {51.01, 8.00}, "58": {51.37, 7.46},
	"59": {51.50, 7.99},
	// Hessen
	"34": {51.32, 9.50}, "35": {50.58, 8.68}, "36": {50.57, 9.68},
	"60": {50.11, 8.68}, "61": {50.24, 8.64}, "63": {50.05, 8.96},
	"64": {49.87, 8.65}, "65": {50.08, 8.24},
	// Niedersachsen
	"26": {53.14, 8.21}, "27": {53.07, 8.80}, "28": {53.08, 8.80},
	"29": {52.87, 10.52}, "30": {52.37, 9.73}, "31": {52.11, 9.36},
	"37": {51.54, 9.93}, "38": {52.27, 10.52},
	// Rheinland-Pfalz
	"54": {49.75, 6.64}, "55": {49.99, 8.27}, "56": {50.36, 7.59},
	"67": {49.44, 8.16},
	// Saarland
	"66": {49.24, 7.00},
	// Sachsen
	"01": {51.05, 13.74}, "02": {51.05, 14.42}, "04": {51.34, 12.38},
	"08": {50.63, 12.45}, "09": {50.83, 12.92},
	// Sachsen-Anhalt
	"06": {51.48, 11.97}, "39": {52.13, 11.62},
	// Thüringen
	"07": {50.93, 11.59}, "99": {50.98, 11.03},
	// Brandenburg
	"03": {51.77, 14.33}, "14": {52.40, 12.97}, "15": {52.10, 14.28},
	"16": {52.84, 13.56},
	// Mecklenburg-Vorpommern
	"17": {53.63, 12.43}, "18": {54.09, 12.14}, "19": {53.63, 11.41},
	// Schleswig-Holstein
	"23": {54.07, 10.76}, "24": {54.32, 10.13}, "25": {53.90, 9.17},
	// Berlin
	"10": {52.52, 13.40}, "12": {52.47, 13.42}, "13": {52.56, 13.34},
	// Hamburg
	"20": {53.55, 10.00}, "21": {53.46, 10.06}, "22": {53.58, 9.97},
}

// PLZCentroid gibt die Zentroid-Koordinaten eines PLZ-Präfixes zurück.
// Falls unbekannt: Mittelpunkt Deutschlands (51.16°N, 10.45°O).
func PLZCentroid(plz string) (lat, lon float64) {
	if len(plz) < 2 {
		return 51.16, 10.45
	}
	if c, ok := plzCentroid[plz[:2]]; ok {
		return c[0], c[1]
	}
	return 51.16, 10.45
}

// =============================================================================
//  Netzgebühr-Berechnung (nur LV/MV)
// =============================================================================

// GridFeeResult enthält das Ergebnis der Netzgebühr-Berechnung.
type GridFeeResult struct {
	FromPLZ         string
	ToPLZ           string
	DistanceKM      float64
	OperatorID      string
	OperatorName    string
	FeePercentPerKM float64
	TotalFeePercent float64   // = FeePercentPerKM × DistanceKM
	FeeAmount       float64   // Gebühr in FND (bei gegebenem Preis)
	NetAmount       float64   // Nettobetrag nach Gebühr
}

// CalculateGridFee berechnet die Netzgebühr für eine Transaktion.
//
// Regeln:
//   - Nur LV/MV-Betreiber: HV wird explizit ignoriert
//   - Gebühr = Preis × FeePercentPerKM × DistanzKM
//   - Betreiber des KÄUFERS bestimmt die Gebühr (Endkunden-Netz)
//   - Maximale Gebühr: 25% (Sicherheitskap)
//
// priceAmount: Transaktionsbetrag in FND
// fromPLZ:     PLZ des Verkäufers
// toPLZ:       PLZ des Käufers
func CalculateGridFee(priceAmount float64, fromPLZ, toPLZ string) *GridFeeResult {
	const maxFeePercent = 25.0

	// Distanz berechnen (Haversine)
	fromLat, fromLon := PLZCentroid(fromPLZ)
	toLat, toLon     := PLZCentroid(toPLZ)

	distKM := haversineKm(fromLat, fromLon, toLat, toLon)

	// LV-Betreiber des Käufers ermitteln (Endkunden-Netz = relevant)
	result := FindOperator(toPLZ, CommodityElectricityLV)

	feePercentPerKM := 0.0
	operatorID      := "unbekannt"
	operatorName    := "kein Betreiber gefunden"

	if result.Operator != nil && result.Operator.Level != "hv" {
		feePercentPerKM = result.Operator.FeePercentPerKM
		operatorID      = result.Operator.ID
		operatorName    = result.Operator.Name
	}

	totalFeePercent := feePercentPerKM * distKM
	if totalFeePercent > maxFeePercent {
		totalFeePercent = maxFeePercent
	}

	feeAmount := priceAmount * totalFeePercent / 100.0
	netAmount := priceAmount - feeAmount

	return &GridFeeResult{
		FromPLZ:         fromPLZ,
		ToPLZ:           toPLZ,
		DistanceKM:      distKM,
		OperatorID:      operatorID,
		OperatorName:    operatorName,
		FeePercentPerKM: feePercentPerKM,
		TotalFeePercent: totalFeePercent,
		FeeAmount:       feeAmount,
		NetAmount:       netAmount,
	}
}

// haversineKm berechnet die Luftliniendistanz in Kilometern.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// =============================================================================
//  Operator-Fee-Konfiguration (Admin-Store, RAM)
// =============================================================================

// feeStore hält die konfigurierten Gebühren im RAM.
// Persistenz über DHT (wird bei Änderung published).
var (
	feeMu    sync.RWMutex
	feeStore = map[string]float64{} // operatorID → FeePercentPerKM
)

// SetOperatorFee speichert die konfigurierte Gebühr eines Betreibers.
func SetOperatorFee(operatorID string, feePercentPerKM float64) error {
	if feePercentPerKM < 0 {
		return fmt.Errorf("grid: FeePercentPerKM darf nicht negativ sein")
	}
	if feePercentPerKM > 1.0 {
		return fmt.Errorf("grid: FeePercentPerKM > 1%% pro km erscheint unrealistisch (max 1.0)")
	}
	feeMu.Lock()
	feeStore[operatorID] = feePercentPerKM
	feeMu.Unlock()

	// Live in Operator-Registry aktualisieren
	updateOperatorFee(operatorID, feePercentPerKM)
	return nil
}

// GetOperatorFee gibt die konfigurierte Gebühr zurück.
func GetOperatorFee(operatorID string) float64 {
	feeMu.RLock()
	defer feeMu.RUnlock()
	return feeStore[operatorID]
}

// updateOperatorFee aktualisiert FeePercentPerKM in allen Operator-Listen.
func updateOperatorFee(id string, fee float64) {
	update := func(ops []Operator) {
		for i := range ops {
			if ops[i].ID == id {
				ops[i].FeePercentPerKM = fee
			}
		}
	}
	update(electricityMVLVOperators)
	update(gasMVLVOperators)
	update(waterOperators)
	// HV explizit ausgelassen – keine Gebühr für HV
}

// AllOperatorFees gibt alle konfigurierten Gebühren zurück.
func AllOperatorFees() map[string]float64 {
	feeMu.RLock()
	defer feeMu.RUnlock()
	result := make(map[string]float64, len(feeStore))
	for k, v := range feeStore {
		result[k] = v
	}
	return result
}

// IsHV gibt zurück ob ein Betreiber ein Hochspannungsnetz ist (kein Fee-Empfänger).
func IsHV(op *Operator) bool {
	return op != nil && op.Level == "hv"
}
