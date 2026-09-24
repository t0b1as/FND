package meter

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"lukechampine.com/blake3"
)

// ─── Lokale Rohdaten-Schicht für Energie-Zertifizierung (Spec §7b-bis) ───────
//
// Die hochauflösenden Zählerstände bleiben LOKAL beim Erzeuger und gehen NIE
// global on-chain (gegen Chain-Explosion). Pro Abrechnungsperiode wird daraus
// ein kompaktes Commitment gebildet:
//   • die gelieferte Menge (ganzzahlig) als Differenz der Endstände,
//   • ein deterministischer Hash ALLER Roh-Readings der Periode (Audit-Anker).
// Beides geht in eine commodity_certify-Tx → erzeugt einen unsettled Token on-chain.
// Sobald der Token on-chain settled ist, dürfen die lokalen Rohdaten gelöscht
// werden (DeletePeriod) — der Hash in der Block-Historie bleibt als Beweis.
//
// HINWEIS: Diese Datei ist die STROM-Variante (kWh→Ws). Für andere Commodities
// (Wasser→ml, Gas→g …) ist die Logik identisch, nur die Einheiten-Umrechnung
// (kWhToWs) wird durch die passende ersetzt; die On-Chain-Strukturen tragen die
// Einheit explizit (chain.MeterRecord.Unit / chain.CommodityToken.Unit).

// WsPerKWh: 1 kWh = 3.600.000 Wattsekunden (Joule). Für die ganzzahlige
// On-Chain-Menge (kein Float im Konsens).
const WsPerKWh = 3_600_000

// RawSample ist ein einzelner lokaler Roh-Messpunkt (reduziert auf das, was für
// Mengen-Nachweis + Hash nötig ist).
type RawSample struct {
	Timestamp time.Time `json:"ts"`
	KWhBezug  float64   `json:"kwh"` // kumulativer Zählerstand Bezug (kWh)
}

// PeriodStore hält die Roh-Samples einer laufenden Periode pro Zähler lokal auf
// Platte (JSON-Lines), serialisiert über einen Mutex.
type PeriodStore struct {
	mu  sync.Mutex
	dir string // z.B. /opt/fundus/meterdata
}

// NewPeriodStore öffnet/erstellt den lokalen Rohdaten-Ordner.
func NewPeriodStore(dir string) (*PeriodStore, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("meter: rawstore mkdir: %w", err)
	}
	return &PeriodStore{dir: dir}, nil
}

func (p *PeriodStore) periodPath(meterID string) string {
	return filepath.Join(p.dir, sanitizeMeterID(meterID)+".jsonl")
}

// Append hängt ein Roh-Sample an die laufende Periode eines Zählers an.
func (p *PeriodStore) Append(meterID string, s RawSample) error {
	if meterID == "" {
		return errors.New("meter: leere meterID")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := os.OpenFile(p.periodPath(meterID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// loadSamples liest alle Roh-Samples eines Zählers (chronologisch sortiert).
func (p *PeriodStore) loadSamples(meterID string) ([]RawSample, error) {
	data, err := os.ReadFile(p.periodPath(meterID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RawSample
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var s RawSample
		if err := json.Unmarshal(line, &s); err != nil {
			return nil, fmt.Errorf("meter: korruptes Sample: %w", err)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out, nil
}

// PeriodCommitment ist das Ergebnis des Periodenabschlusses: die Eingaben für
// eine commodity_certify-Tx.
type PeriodCommitment struct {
	MeterID      string
	CumulativeA  uint64   // Anfangsstand der Periode in Ws (ganzzahlig)
	CumulativeB  uint64   // Endstand der Periode in Ws
	TimestampA   int64    // Unix-Sekunden Anfang
	TimestampB   int64    // Unix-Sekunden Ende
	AmountWs     uint64   // gelieferte Menge der Periode (B − A)
	RawDataHash  [32]byte // BLAKE3 über alle Roh-Samples der Periode
	SampleCount  int
}

// ClosePeriod bildet aus den lokalen Roh-Samples das Perioden-Commitment:
// Anfangs-/Endstand in Ws (ganzzahlig) + Hash aller Samples. Erfordert ≥2
// Samples (Anfang/Ende). Die Rohdaten bleiben liegen, bis DeletePeriod gerufen
// wird (nach On-Chain-Settlement).
func (p *PeriodStore) ClosePeriod(meterID string) (*PeriodCommitment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	samples, err := p.loadSamples(meterID)
	if err != nil {
		return nil, err
	}
	if len(samples) < 2 {
		return nil, errors.New("meter: Periode braucht mindestens 2 Roh-Samples")
	}
	first, last := samples[0], samples[len(samples)-1]
	aWs := kWhToWs(first.KWhBezug)
	bWs := kWhToWs(last.KWhBezug)
	if bWs < aWs {
		return nil, errors.New("meter: Endstand < Anfangsstand (Rohdaten rückläufig)")
	}
	// Deterministischer Hash über alle Samples (kanonische Byte-Form, nicht JSON,
	// damit der Hash unabhängig von JSON-Formatierung reproduzierbar ist).
	h := blake3.New(32, nil)
	var scratch [16]byte
	for _, s := range samples {
		binary.BigEndian.PutUint64(scratch[0:8], uint64(s.Timestamp.Unix()))
		binary.BigEndian.PutUint64(scratch[8:16], kWhToWs(s.KWhBezug))
		h.Write(scratch[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))

	return &PeriodCommitment{
		MeterID:     meterID,
		CumulativeA: aWs,
		CumulativeB: bWs,
		TimestampA:  first.Timestamp.Unix(),
		TimestampB:  last.Timestamp.Unix(),
		AmountWs:    bWs - aWs,
		RawDataHash: sum,
		SampleCount: len(samples),
	}, nil
}

// DeletePeriod löscht die lokalen Rohdaten eines Zählers. Erst aufrufen, wenn der
// zugehörige Token on-chain settled ist — der RawDataHash in der Block-Historie
// bleibt als Beweisanker erhalten.
func (p *PeriodStore) DeletePeriod(meterID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := os.Remove(p.periodPath(meterID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// kWhToWs rechnet einen kWh-Zählerstand in ganzzahlige Wattsekunden um
// (gerundet). Float kommt NUR hier lokal vor; on-chain geht ausschließlich die
// Ganzzahl.
func kWhToWs(kwh float64) uint64 {
	if kwh <= 0 {
		return 0
	}
	return uint64(math.Round(kwh * WsPerKWh))
}

func sanitizeMeterID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
