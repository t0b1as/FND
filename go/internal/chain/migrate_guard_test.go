package chain

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// FND_002_NeedsMigration deckt die Entscheidungstabelle des Wächters ab.
// Zeile "Genesis in DB, Historie in JSON" ist der Fehler aus R035: sie war
// vorher false und hat damit 500 Blöcke Historie stillschweigend fallen lassen.
func TestFND_002_NeedsMigration(t *testing.T) {
	cases := []struct {
		name string
		in   MigrationState
		want bool
	}{
		{"frischer Node, nichts vorhanden",
			MigrationState{}, false},
		{"nur DB, keine JSON-Historie",
			MigrationState{DBHeight: 500, DBPresent: true}, false},
		{"JSON vorhanden, DB komplett leer",
			MigrationState{JSONHeight: 500, JSONPresent: true}, true},
		{"Genesis in DB, Historie in JSON — der Bug",
			MigrationState{JSONHeight: 500, JSONPresent: true, DBHeight: 0, DBPresent: true}, true},
		{"abgebrochene Migration, DB liegt zurück",
			MigrationState{JSONHeight: 500, JSONPresent: true, DBHeight: 200, DBPresent: true}, true},
		{"vollständig migriert",
			MigrationState{JSONHeight: 500, JSONPresent: true, DBHeight: 500, DBPresent: true}, false},
		{"DB ist voraus — JSON ist Altlast",
			MigrationState{JSONHeight: 200, JSONPresent: true, DBHeight: 500, DBPresent: true}, false},
		{"nur Genesis auf beiden Seiten",
			MigrationState{JSONHeight: 0, JSONPresent: true, DBHeight: 0, DBPresent: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NeedsMigration(c.in); got != c.want {
				t.Fatalf("NeedsMigration(%+v) = %v, erwartet %v", c.in, got, c.want)
			}
		})
	}
}

// FND_002_ScanJSONBlockHeight prüft das Einlesen des blocks/-Verzeichnisses.
func TestFND_002_ScanJSONBlockHeight(t *testing.T) {
	t.Run("kein blocks-Verzeichnis", func(t *testing.T) {
		h, ok, err := ScanJSONBlockHeight(t.TempDir())
		if err != nil {
			t.Fatalf("unerwarteter Fehler: %v", err)
		}
		if ok || h != 0 {
			t.Fatalf("erwartet (0,false), bekam (%d,%v)", h, ok)
		}
	})

	t.Run("höchste Höhe, nicht Anzahl", func(t *testing.T) {
		dir := t.TempDir()
		mustBlockFiles(t, dir, 0, 1, 2, 7) // Lücke bei 3..6
		h, ok, err := ScanJSONBlockHeight(dir)
		if err != nil || !ok {
			t.Fatalf("erwartet Treffer, bekam ok=%v err=%v", ok, err)
		}
		if h != 7 {
			t.Fatalf("erwartet 7, bekam %d", h)
		}
	})

	t.Run("Fremddateien werden übergangen", func(t *testing.T) {
		dir := t.TempDir()
		mustBlockFiles(t, dir, 0, 1)
		for _, junk := range []string{"meta.json.tmp", "notizen.txt", "backup.json", ".hidden"} {
			if err := os.WriteFile(filepath.Join(dir, "blocks", junk), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		h, ok, err := ScanJSONBlockHeight(dir)
		if err != nil {
			t.Fatalf("Fremddatei darf kein Fehler sein: %v", err)
		}
		if !ok || h != 1 {
			t.Fatalf("erwartet (1,true), bekam (%d,%v)", h, ok)
		}
	})

	t.Run("nur Genesis ist eine Historie", func(t *testing.T) {
		dir := t.TempDir()
		mustBlockFiles(t, dir, 0)
		h, ok, _ := ScanJSONBlockHeight(dir)
		if !ok || h != 0 {
			t.Fatalf("Genesis allein muss als vorhanden gelten, bekam (%d,%v)", h, ok)
		}
	})
}

// FND_002_GuardCatchesGenesisOnlyDB ist der Integrationsfall: eine DB, die nur
// den Genesis enthält, während JSON-Historie danebenliegt, muss als
// migrationsbedürftig erkannt werden — ohne meta.json zu befragen.
func TestFND_002_GuardCatchesGenesisOnlyDB(t *testing.T) {
	dir := t.TempDir()
	mustBlockFiles(t, dir, 0, 1, 2, 3, 4, 5)

	st, err := InspectMigration(dir, fakeStore{height: 0, present: true})
	if err != nil {
		t.Fatalf("InspectMigration: %v", err)
	}
	if !NeedsMigration(st) {
		t.Fatalf("Genesis-only-DB neben JSON-Historie muss migriert werden, Zustand: %+v", st)
	}
}

// fakeStore erfüllt nur Highest(); die übrigen Methoden werden vom Wächter
// nicht angefasst. Falls das BlockStore-Interface wächst, schlägt der Compiler
// hier an — gewollt.
type fakeStore struct {
	height  uint64
	present bool
}

func (f fakeStore) Highest() (uint64, bool, error) { return f.height, f.present, nil }
func (f fakeStore) Put(uint64, *Block) error       { panic("nicht erwartet") }
func (f fakeStore) Get(uint64) (*Block, error)     { panic("nicht erwartet") }
func (f fakeStore) Has(uint64) (bool, error)       { panic("nicht erwartet") }
func (f fakeStore) Close() error                   { return nil }

func mustBlockFiles(t *testing.T, dir string, heights ...uint64) {
	t.Helper()
	bd := filepath.Join(dir, "blocks")
	if err := os.MkdirAll(bd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range heights {
		p := filepath.Join(bd, fmt.Sprintf("%020d.json", h))
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
