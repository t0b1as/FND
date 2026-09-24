package chain

// Migrations-Wächter (FND-002).
//
// Bisheriger Fehler: Die Entscheidung "muss migriert werden?" hing an zwei
// unzuverlässigen Signalen:
//
//   1. migrate.go verglich die DB-Höhe gegen meta.json. meta.json wird aber beim
//      Start neu geschrieben — steht dort nach einem Fehlstart 0, lautet der
//      Vergleich 0 >= 0 und die Migration wird als "bereits erledigt" abgelehnt.
//      Das ist die Meldung "Blockstore enthält bereits Höhe 0".
//
//   2. blockchain.go warnte nur, wenn die DB VOLLSTÄNDIG leer war (!hasAny).
//      Sobald der Genesis einmal in chain.db lag, war hasAny true, der Wächter
//      schwieg — und der Node startete mit Höhe 0, während die echte Historie
//      unangetastet als JSON in blocks/ lag. Das ist der stille Fall, und er
//      ist der gefährlichere von beiden: kein Fehler, nur weg.
//
// Einzige verlässliche Quelle ist der Vergleich der tatsächlich vorhandenen
// Daten: höchste JSON-Blockdatei gegen höchste DB-Höhe. meta.json wird für die
// Entscheidung nicht mehr befragt.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MigrationState beschreibt, was auf der Platte tatsächlich liegt.
type MigrationState struct {
	JSONHeight  uint64 // höchste Höhe mit JSON-Blockdatei
	JSONPresent bool   // überhaupt JSON-Blöcke vorhanden?
	DBHeight    uint64 // höchste Höhe im Blockstore
	DBPresent   bool   // überhaupt Blöcke im Store?
}

// NeedsMigration entscheidet allein anhand vorhandener Daten, ob migriert
// werden muss. Rein und ohne Seiteneffekte — der gesamte Wächter ist damit
// tabellarisch testbar.
func NeedsMigration(s MigrationState) bool {
	if !s.JSONPresent {
		return false // Nichts zu migrieren.
	}
	if !s.DBPresent {
		return true // JSON da, DB leer.
	}
	// Beides da: die DB ist nur dann fertig, wenn sie mindestens so weit
	// reicht wie die JSON-Historie. Liegt sie zurück (klassisch: nur Genesis
	// bei DBHeight 0 gegen JSONHeight 500), fehlt echte Historie.
	return s.JSONHeight > s.DBHeight
}

// ScanJSONBlockHeight liefert die höchste Höhe, für die unter dir/blocks/ eine
// Blockdatei existiert. Fremddateien werden übergangen, nicht als Fehler
// gewertet. Fehlt das Verzeichnis, gibt es schlicht keine JSON-Historie.
func ScanJSONBlockHeight(dir string) (uint64, bool, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "blocks"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	var (
		max   uint64
		found bool
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		h, perr := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
		if perr != nil {
			continue // z. B. meta.json.tmp oder Handbetrieb-Reste
		}
		if !found || h > max {
			max, found = h, true
		}
	}
	return max, found, nil
}

// InspectMigration liest den Plattenzustand zusammen. store darf nil sein,
// dann gilt die DB als leer.
func InspectMigration(dir string, store BlockStore) (MigrationState, error) {
	var st MigrationState
	jh, jok, err := ScanJSONBlockHeight(dir)
	if err != nil {
		return st, err
	}
	st.JSONHeight, st.JSONPresent = jh, jok
	if store != nil {
		dh, dok, derr := store.Highest()
		if derr != nil {
			return st, derr
		}
		st.DBHeight, st.DBPresent = dh, dok
	}
	return st, nil
}
