package websuche

// Ablageort des eigenen Suchindex (R895, Grundlage für R896).
//
// Der Index wird groß – auf der SD-Karte der Pi 3 ist dafür kein Platz und
// sie verträgt die vielen Schreibzugriffe schlecht. Drei Orte:
//
//   lokal     <DataDir>/suchindex – nur für Pis mit ausreichend Speicher
//   laufwerk  ein eingehängtes Laufwerk unter /mnt oder /media
//             (USB-Platte, SSD); der Index liegt dort in fundus-suchindex/
//   knoten    ein anderer Pi im Fundus-Netz hält den Index; dieser Pi fragt
//             ihn über P2P ab und schreibt selbst nichts
//
// Der Crawler (R896) schreibt nur über Ablage – egal wo sie liegt.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	OrtLokal    = "lokal"
	OrtLaufwerk = "laufwerk"
	OrtKnoten   = "knoten"
	// Unterordner auf einem Laufwerk – so bleibt der Rest des Laufwerks unberührt.
	LaufwerkOrdner = "fundus-suchindex"
	// Mindestplatz, ab dem ein Ort als geeignet gilt.
	MindestFreiGB = 2.0
)

// Ort: wo der Index liegt.
type Ort struct {
	Modus  string `json:"modus"`            // lokal | laufwerk | knoten
	Pfad   string `json:"pfad,omitempty"`   // bei laufwerk: Einhängepunkt oder Ordner darin
	Knoten string `json:"knoten,omitempty"` // bei knoten: Peer-ID des haltenden Pis
}

var (
	rePeerID        = regexp.MustCompile(`^(12D3Koo|Qm)[1-9A-HJ-NP-Za-km-z]{38,60}$`)
	ErlaubteWurzeln = []string{"/mnt/", "/media/"}
)

// Pruefen: Form der Angaben (ohne Platten- oder Netzzugriff).
func (o Ort) Pruefen() error {
	switch o.Modus {
	case "", OrtLokal:
		return nil
	case OrtLaufwerk:
		p := filepath.Clean(o.Pfad)
		if !filepath.IsAbs(o.Pfad) || strings.Contains(o.Pfad, "..") || strings.ContainsAny(o.Pfad, "\x00\n") {
			return errors.New("Pfad bitte als vollständigen Pfad angeben, z. B. /mnt/usb1")
		}
		erlaubt := false
		for _, w := range ErlaubteWurzeln {
			if strings.HasPrefix(p+"/", w) && len(p) > len(w)-1 {
				erlaubt = true
			}
		}
		if !erlaubt {
			return errors.New("nur Laufwerke unter /mnt oder /media")
		}
		return nil
	case OrtKnoten:
		if !rePeerID.MatchString(o.Knoten) {
			return errors.New("ungültige Knoten-ID")
		}
		return nil
	}
	return errors.New("unbekannter Ablageort")
}

// Ordner: Verzeichnis des Index auf diesem Pi ("" bei knoten).
func (o Ort) Ordner(dataDir string) string {
	switch o.Modus {
	case OrtLaufwerk:
		p := filepath.Clean(o.Pfad)
		if filepath.Base(p) == LaufwerkOrdner {
			return p
		}
		return filepath.Join(p, LaufwerkOrdner)
	case OrtKnoten:
		return ""
	}
	return filepath.Join(dataDir, "suchindex")
}

// Platz: Zustand eines Ablageorts.
type Platz struct {
	Ordner     string  `json:"ordner"`
	FreiGB     float64 `json:"frei_gb"`
	GesamtGB   float64 `json:"gesamt_gb"`
	Schreibbar bool    `json:"schreibbar"`
	AufSystem  bool    `json:"auf_system"` // liegt auf demselben Datenträger wie / (SD-Karte)
	Fehler     string  `json:"fehler,omitempty"`
	Warnung    string  `json:"warnung,omitempty"`
}

func statfsGB(p string) (frei, gesamt float64, err error) {
	var st syscall.Statfs_t
	if err = syscall.Statfs(p, &st); err != nil {
		return
	}
	bs := float64(st.Bsize)
	return float64(st.Bavail) * bs / 1e9, float64(st.Blocks) * bs / 1e9, nil
}

func geraet(p string) (uint64, bool) {
	var st syscall.Stat_t
	if syscall.Stat(p, &st) != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// PlatzPruefen: existiert der Ort, ist er beschreibbar, wie viel ist frei?
// Bei einem Laufwerk muss der Einhängepunkt schon bestehen – angelegt wird
// nur der Unterordner fundus-suchindex. So landet der Index nie aus Versehen
// auf der SD-Karte, wenn die Platte gerade nicht eingehängt ist.
func PlatzPruefen(o Ort, dataDir string) Platz {
	ordner := o.Ordner(dataDir)
	pl := Platz{Ordner: ordner}
	if ordner == "" {
		pl.Fehler = "Index liegt auf einem anderen Knoten"
		return pl
	}
	if o.Modus == OrtLaufwerk {
		basis := filepath.Dir(ordner)
		fi, err := os.Stat(basis)
		if err != nil || !fi.IsDir() {
			pl.Fehler = "Laufwerk nicht gefunden: " + basis
			return pl
		}
		if d1, ok1 := geraet(basis); ok1 {
			if d2, ok2 := geraet("/"); ok2 && d1 == d2 {
				pl.AufSystem = true
				pl.Fehler = basis + " ist nicht eingehängt – der Ordner liegt auf der SD-Karte"
				return pl
			}
		}
	}
	if err := os.MkdirAll(ordner, 0o750); err != nil {
		pl.Fehler = "Ordner nicht anlegbar: " + err.Error()
		return pl
	}
	probe := filepath.Join(ordner, ".schreibprobe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		pl.Fehler = "nicht beschreibbar: " + err.Error()
		return pl
	}
	_ = os.Remove(probe)
	pl.Schreibbar = true
	pl.FreiGB, pl.GesamtGB, _ = statfsGB(ordner)
	if o.Modus != OrtLaufwerk {
		if d1, ok1 := geraet(ordner); ok1 {
			if d2, ok2 := geraet("/"); ok2 && d1 == d2 {
				pl.AufSystem = true
			}
		}
	}
	if pl.FreiGB < MindestFreiGB {
		pl.Warnung = "weniger als 2 GB frei"
	} else if pl.AufSystem {
		pl.Warnung = "liegt auf der SD-Karte – für einen großen Index ein Laufwerk wählen"
	}
	return pl
}

// Laufwerk: ein Kandidat für die Auswahl.
type Laufwerk struct {
	Pfad     string  `json:"pfad"`
	Geraet   string  `json:"geraet"`
	Typ      string  `json:"typ"`
	FreiGB   float64 `json:"frei_gb"`
	GesamtGB float64 `json:"gesamt_gb"`
}

// MountsDatei: Quelle der Einhängepunkte (Tests ersetzen sie).
var MountsDatei = "/proc/mounts"

// Laufwerke: eingehängte Datenträger unter /mnt und /media.
func Laufwerke() []Laufwerk {
	b, err := os.ReadFile(MountsDatei)
	if err != nil {
		return nil
	}
	var out []Laufwerk
	gesehen := map[string]bool{}
	for _, z := range strings.Split(string(b), "\n") {
		f := strings.Fields(z)
		if len(f) < 3 {
			continue
		}
		pfad := strings.ReplaceAll(f[1], `\040`, " ")
		ok := false
		for _, w := range ErlaubteWurzeln {
			if strings.HasPrefix(pfad+"/", w) && pfad+"/" != w {
				ok = true
			}
		}
		if !ok || gesehen[pfad] || f[2] == "davfs" || f[2] == "fuse.sshfs" || f[2] == "cifs" || f[2] == "nfs" || f[2] == "nfs4" {
			continue // Netzlaufwerke: zu langsam und zu unsicher für einen Index
		}
		gesehen[pfad] = true
		l := Laufwerk{Pfad: pfad, Geraet: f[0], Typ: f[2]}
		l.FreiGB, l.GesamtGB, _ = statfsGB(pfad)
		out = append(out, l)
	}
	return out
}

// ---------------------------------------------------------------------------
// Ablage: der Index auf der Platte. In R895 nur das Gerüst (Ordner mit
// Kennung); Dokumente und Suche kommen mit dem Crawler in R896.

const ablageFormat = 1

type ablageKopf struct {
	Format   int    `json:"format"`
	Erstellt int64  `json:"erstellt"`
	Node     string `json:"node"`
}

type Ablage struct {
	Ordner string
	kopf   ablageKopf
	Index  *Index
}

// Info: Stand des Index (auch für die Abfrage durch andere Pis).
type Info struct {
	Bereit    bool    `json:"bereit"`
	Dokumente int     `json:"dokumente"`
	GroesseMB float64 `json:"groesse_mb"`
	FreiGB    float64 `json:"frei_gb"`
	Erstellt  int64   `json:"erstellt"`
	Node      string  `json:"node"`
	Hinweis   string  `json:"hinweis,omitempty"`
}

// Oeffnen: Ablage anlegen oder vorhandene übernehmen. Eine fremde Kennung
// (anderer Node) wird übernommen – so kann eine Platte umziehen.
func Oeffnen(ordner, node string) (*Ablage, error) {
	if ordner == "" {
		return nil, errors.New("kein Ordner")
	}
	if err := os.MkdirAll(ordner, 0o750); err != nil {
		return nil, err
	}
	a := &Ablage{Ordner: ordner}
	kopfDatei := filepath.Join(ordner, "ablage.json")
	if b, err := os.ReadFile(kopfDatei); err == nil && json.Unmarshal(b, &a.kopf) == nil && a.kopf.Format > 0 {
		if a.kopf.Format > ablageFormat {
			return nil, errors.New("Index stammt von einer neueren Fundus-Version")
		}
		var err error
		a.Index, err = IndexOeffnen(ordner)
		return a, err
	}
	a.kopf = ablageKopf{Format: ablageFormat, Erstellt: time.Now().Unix(), Node: node}
	b, _ := json.MarshalIndent(a.kopf, "", "  ")
	tmp := kopfDatei + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, kopfDatei); err != nil {
		return nil, err
	}
	var err error
	a.Index, err = IndexOeffnen(ordner)
	return a, err
}

// Schliessen: Index sichern und Dateien schließen.
func (a *Ablage) Schliessen() error {
	if a == nil || a.Index == nil {
		return nil
	}
	return a.Index.Schliessen()
}

func (a *Ablage) Info() Info {
	i := Info{Bereit: true, Erstellt: a.kopf.Erstellt, Node: a.kopf.Node}
	if a.Index != nil {
		i.Dokumente, _ = a.Index.Stand()
	}
	var groesse int64
	_ = filepath.Walk(a.Ordner, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			groesse += fi.Size()
		}
		return nil
	})
	i.GroesseMB = float64(groesse) / 1e6
	i.FreiGB, _, _ = statfsGB(a.Ordner)
	return i
}

// Suchen im eigenen Index.
func (a *Ablage) Suchen(ctx context.Context, anfrage string, n int) ([]Treffer, error) {
	if a.Index == nil {
		return nil, errors.New("Index nicht geöffnet")
	}
	return a.Index.Suchen(ctx, anfrage, n)
}

// Umziehen: Index von einem Ordner in einen anderen kopieren. Erst wenn
// alles angekommen ist (gleiche Größen), wird der alte Ordner geleert.
// fortschritt bekommt kopierte und gesamte Bytes. Der Index darf dabei
// nicht geöffnet sein.
func Umziehen(von, nach string, fortschritt func(kopiert, gesamt int64)) error {
	if filepath.Clean(von) == filepath.Clean(nach) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(von, "ablage.json")); err != nil {
		return nil // nichts umzuziehen
	}
	if _, err := os.Stat(filepath.Join(nach, "ablage.json")); err == nil {
		return errors.New("am Ziel liegt schon ein Index – erst dort aufräumen")
	}
	eintraege, err := os.ReadDir(von)
	if err != nil {
		return err
	}
	var gesamt int64
	var dateien []os.FileInfo
	for _, e := range eintraege {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		dateien = append(dateien, fi)
		gesamt += fi.Size()
	}
	if frei, _, err := statfsGB(filepath.Dir(filepath.Clean(nach))); err == nil && frei*1e9 < float64(gesamt)*1.1 {
		return errors.New("am Ziel ist nicht genug Platz")
	}
	if err := os.MkdirAll(nach, 0o750); err != nil {
		return err
	}
	var kopiert int64
	kopieren := func(fi os.FileInfo) error {
		q, err := os.Open(filepath.Join(von, fi.Name()))
		if err != nil {
			return err
		}
		defer q.Close()
		ziel := filepath.Join(nach, fi.Name())
		z, err := os.OpenFile(ziel+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		buf := make([]byte, 1<<20)
		for {
			n, rerr := q.Read(buf)
			if n > 0 {
				if _, werr := z.Write(buf[:n]); werr != nil {
					z.Close()
					return werr
				}
				kopiert += int64(n)
				if fortschritt != nil {
					fortschritt(kopiert, gesamt)
				}
			}
			if rerr != nil {
				break
			}
		}
		if err := z.Sync(); err != nil {
			z.Close()
			return err
		}
		z.Close()
		if st, err := os.Stat(ziel + ".tmp"); err != nil || st.Size() != fi.Size() {
			return errors.New("Kopie von " + fi.Name() + " unvollständig")
		}
		return os.Rename(ziel+".tmp", ziel)
	}
	// ablage.json zuletzt: erst dann gilt der Ordner als Index.
	sort.SliceStable(dateien, func(i, j int) bool {
		return dateien[i].Name() != "ablage.json" && dateien[j].Name() == "ablage.json"
	})
	for _, fi := range dateien {
		if err := kopieren(fi); err != nil {
			os.RemoveAll(nach)
			return err
		}
	}
	for _, fi := range dateien {
		if fi.Name() != "ablage.json" {
			os.Remove(filepath.Join(von, fi.Name()))
		}
	}
	return os.Remove(filepath.Join(von, "ablage.json"))
}

// ---------------------------------------------------------------------------

// Funktion: beliebige Funktion als Quelle (eigener Index, Index auf einem
// anderen Pi).
type Funktion struct {
	N string
	G float64
	F func(ctx context.Context, anfrage, sprache string) ([]Treffer, error)
}

func (f Funktion) Name() string { return f.N }
func (f Funktion) Gewicht() float64 {
	if f.G <= 0 {
		return 1
	}
	return f.G
}
func (f Funktion) Suchen(ctx context.Context, anfrage, sprache string) ([]Treffer, error) {
	return f.F(ctx, anfrage, sprache)
}

// IndexVorhanden: liegt im Ordner schon ein Index?
func IndexVorhanden(ordner string) bool {
	if ordner == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(ordner, "ablage.json"))
	return err == nil
}
