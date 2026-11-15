package websuche

// Speicher im Griff halten (R898).
//
//   - Platzregel: Obergrenze für den Index und ein Rest, der auf dem
//     Datenträger frei bleiben muss. Ist eins erreicht, hält der Crawler an
//     und der Pi nimmt im Verbund keine Kopien mehr an.
//   - Verdichten: Gelöschte und ersetzte Seiten bleiben zunächst in dok.dat
//     liegen. Ist mehr als ein Drittel tot, wird die Datei ohne sie neu
//     geschrieben (nur wenn der Platz dafür da ist; absturzfest).
//   - Veraltete: Seiten, die lange nicht gelesen wurden, für das Neulesen –
//     dabei fallen nicht mehr erreichbare Seiten heraus.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Platzregel: vom Admin festgelegt; 0 = automatisch.
type Platzregel struct {
	GrenzeGB float64 `json:"grenze_gb"` // Höchstgröße des Index
	RestGB   float64 `json:"rest_gb"`   // so viel bleibt auf dem Datenträger frei
}

// PlatzStand: wie es um den Platz steht.
type PlatzStand struct {
	GroesseGB float64 `json:"groesse_gb"`
	GrenzeGB  float64 `json:"grenze_gb"`
	FreiGB    float64 `json:"frei_gb"`
	RestGB    float64 `json:"rest_gb"`
	AufSystem bool    `json:"auf_system"`
	Auto      bool    `json:"auto"`
	Voll      bool    `json:"voll"`
	Grund     string  `json:"grund,omitempty"`
	TotAnteil float64 `json:"tot_anteil"` // Anteil gelöschter Daten in dok.dat
}

// Stand: Größe des Index gegen Grenze und freien Platz.
//
// Automatisch: Auf dem Systemdatenträger (SD-Karte) höchstens 2 GB und
// höchstens die Hälfte des Platzes, 5 GB bleiben für den Node frei. Auf
// einem eigenen Laufwerk darf der Index es bis auf 1 GB füllen.
func (r Platzregel) Stand(ordner string, ix *Index) PlatzStand {
	st := PlatzStand{Auto: r.GrenzeGB <= 0}
	if ix != nil {
		st.GroesseGB = float64(ix.Groesse()) / 1e9
		st.TotAnteil = ix.TotAnteil()
	}
	st.FreiGB, _, _ = statfsGB(ordner)
	if d1, ok1 := geraet(ordner); ok1 {
		if d2, ok2 := geraet("/"); ok2 && d1 == d2 {
			st.AufSystem = true
		}
	}
	st.RestGB = r.RestGB
	if st.RestGB <= 0 {
		st.RestGB = 1
		if st.AufSystem {
			st.RestGB = 5
		}
	}
	st.GrenzeGB = r.GrenzeGB
	if st.Auto {
		verfuegbar := st.FreiGB + st.GroesseGB
		if st.AufSystem {
			st.GrenzeGB = verfuegbar / 2
			if st.GrenzeGB > 2 {
				st.GrenzeGB = 2
			}
		} else {
			st.GrenzeGB = verfuegbar - st.RestGB
		}
	}
	switch {
	case st.FreiGB < st.RestGB:
		st.Voll, st.Grund = true, fmt.Sprintf("Datenträger fast voll (%.1f GB frei, %.0f GB bleiben reserviert)", st.FreiGB, st.RestGB)
	case st.GroesseGB >= st.GrenzeGB:
		st.Voll, st.Grund = true, fmt.Sprintf("Speichergrenze des Index erreicht (%.2f von %.2f GB)", st.GroesseGB, st.GrenzeGB)
	}
	st.Grund = strings.ReplaceAll(st.Grund, ".", ",") // deutsche Zahlen
	return st
}

// Groesse: Bytes aller Dateien des Index.
func (ix *Index) Groesse() int64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var g int64
	_ = filepath.Walk(ix.ordner, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			g += fi.Size()
		}
		return nil
	})
	return g
}

// TotAnteil: Anteil gelöschter Seiten an dok.dat.
func (ix *Index) TotAnteil() float64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.datGr == 0 {
		return 0
	}
	return float64(ix.totBytes) / float64(ix.datGr)
}

// Pflegen: verschobenes Zusammenlegen nachholen, bei Bedarf verdichten.
func (ix *Index) Pflegen() error {
	ix.mu.Lock()
	if ix.aufgesch && len(ix.abschn) > maxAbschnitte {
		if err := ix.zusammenlegenGesperrt(); err != nil {
			ix.mu.Unlock()
			return err
		}
	}
	ix.mu.Unlock()
	_, err := ix.Verdichten(false)
	return err
}

const verdichtenMarke = "verdichten.ok"

// verdichtenAbschliessen: nach einem Absturz mitten im Verdichten. Steht
// die Marke, sind beide neuen Dateien vollständig – dann übernehmen; sonst
// die halbfertigen verwerfen.
func verdichtenAbschliessen(ordner string) {
	marke := filepath.Join(ordner, verdichtenMarke)
	if _, err := os.Stat(marke); err == nil {
		for _, n := range []string{"dok.idx", "dok.dat"} {
			if _, err := os.Stat(filepath.Join(ordner, n+".neu")); err == nil {
				_ = os.Rename(filepath.Join(ordner, n+".neu"), filepath.Join(ordner, n))
			}
		}
		_ = os.Remove(marke)
		return
	}
	_ = os.Remove(filepath.Join(ordner, "dok.idx.neu"))
	_ = os.Remove(filepath.Join(ordner, "dok.dat.neu"))
}

// Verdichten: dok.dat ohne gelöschte Seiten neu schreiben. Ohne zwingen nur,
// wenn mehr als ein Drittel tot ist. true = verdichtet.
func (ix *Index) Verdichten(zwingen bool) (bool, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.totBytes == 0 || (!zwingen && ix.totBytes*3 < ix.datGr) {
		return false, nil
	}
	lebend := ix.datGr - ix.totBytes
	if frei, _, err := statfsGB(ix.ordner); err == nil && frei*1e9 < float64(lebend)*1.2+float64(ix.n)*idxBreite+50e6 {
		return false, errors.New("zu wenig Platz zum Verdichten")
	}
	alt := make([]byte, int(ix.n)*idxBreite)
	if _, err := io.ReadFull(io.NewSectionReader(ix.idx, 0, int64(len(alt))), alt); err != nil {
		return false, err
	}
	pd := filepath.Join(ix.ordner, "dok.dat.neu")
	pi := filepath.Join(ix.ordner, "dok.idx.neu")
	nd, err := os.OpenFile(pd, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o640)
	if err != nil {
		return false, err
	}
	aufraeumen := func(e error) (bool, error) {
		nd.Close()
		os.Remove(pd)
		os.Remove(pi)
		return false, e
	}
	var off int64
	buf := make([]byte, 64<<10)
	for i := uint32(0); i < ix.n; i++ {
		e := alt[int(i)*idxBreite : int(i+1)*idxBreite]
		if ix.meta[i].merker&merkerGeloescht != 0 {
			binary.LittleEndian.PutUint64(e[0:], 0)
			binary.LittleEndian.PutUint32(e[8:], 0)
			binary.LittleEndian.PutUint32(e[20:], ix.meta[i].merker)
			continue
		}
		l := int(binary.LittleEndian.Uint32(e[8:]))
		if l > len(buf) {
			buf = make([]byte, l)
		}
		if _, err := ix.dat.ReadAt(buf[:l], int64(binary.LittleEndian.Uint64(e[0:]))); err != nil {
			return aufraeumen(err)
		}
		if _, err := nd.WriteAt(buf[:l], off); err != nil {
			return aufraeumen(err)
		}
		binary.LittleEndian.PutUint64(e[0:], uint64(off))
		off += int64(l)
	}
	if err := nd.Sync(); err != nil {
		return aufraeumen(err)
	}
	if err := os.WriteFile(pi, alt, 0o640); err != nil {
		return aufraeumen(err)
	}
	if f, err := os.Open(pi); err == nil {
		f.Sync()
		f.Close()
	}
	nd.Close()
	if err := os.WriteFile(filepath.Join(ix.ordner, verdichtenMarke), []byte("1"), 0o640); err != nil {
		return aufraeumen(err)
	}
	ix.dat.Close()
	ix.idx.Close()
	verdichtenAbschliessen(ix.ordner)
	if ix.dat, err = os.OpenFile(filepath.Join(ix.ordner, "dok.dat"), os.O_RDWR, 0o640); err != nil {
		return false, err
	}
	if ix.idx, err = os.OpenFile(filepath.Join(ix.ordner, "dok.idx"), os.O_RDWR, 0o640); err != nil {
		return false, err
	}
	ix.datGr, ix.totBytes = off, 0
	for i := range ix.meta {
		if ix.meta[i].merker&merkerGeloescht != 0 {
			ix.meta[i].laenge = 0
		}
	}
	return true, nil
}

// Veraltete: bis zu max Adressen, die vor dem Zeitpunkt zuletzt gelesen
// wurden. Reihum, damit nicht immer dieselben vorne stehen.
func (ix *Index) Veraltete(vor time.Time, max int) []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	var out []string
	if ix.n == 0 {
		return nil
	}
	grenze := uint32(vor.Unix())
	start := ix.veraltPos % ix.n
	for k := uint32(0); k < ix.n && len(out) < max; k++ {
		i := (start + k) % ix.n
		ix.veraltPos = i + 1
		m := ix.meta[i]
		if m.merker&merkerGeloescht != 0 || m.zeit >= grenze {
			continue
		}
		if d, err := ix.dokumentGesperrt(i); err == nil {
			out = append(out, d.URL)
		}
	}
	return out
}
