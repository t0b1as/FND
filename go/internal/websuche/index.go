package websuche

// Eigener Suchindex (R896) – nur Standardbibliothek, für die Pi gebaut.
//
// Dateien im Ablageordner:
//   dok.dat    Seiten (komprimiert), angehängt
//   dok.idx    je Seite 24 Byte: Lage in dok.dat, Wortzahl, Zeit, Merker
//   url.idx    je Seite 12 Byte: Prüfsumme der Adresse → Seitennummer
//   seg-*.dic  Wörterbuch eines Abschnitts, sortiert
//   seg-*.pst  Fundstellen eines Abschnitts (Seitennummer, Häufigkeit)
//
// Neue Seiten sammeln sich im Speicher (bis 400 Seiten oder 6 MB) und
// werden dann als Abschnitt geschrieben. Ab 8 Abschnitten werden sie zu
// einem zusammengelegt; gelöschte Seiten fallen dabei heraus. Im Speicher
// bleibt je Abschnitt nur jedes 64. Wort – so reichen wenige MB auch für
// Hunderttausende Seiten. Rangfolge: BM25, Titel zählt dreifach.

import (
	"bufio"
	"bytes"
	"compress/flate"
	"container/heap"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	idxBreite       = 24
	urlBreite       = 12
	merkerGeloescht = 1
	puffDokumente   = 400
	puffBytes       = 6 << 20
	maxAbschnitte   = 8
	duennAbstand    = 64
	bm25k1          = 1.2
	bm25b           = 0.75
)

// Dokument: eine Seite für den Index.
type Dokument struct {
	URL          string `json:"u"`
	Titel        string `json:"t"`
	Beschreibung string `json:"b,omitempty"`
	Text         string `json:"x,omitempty"`
	Sprache      string `json:"s,omitempty"`
	Zeit         int64  `json:"z"`
}

type posting struct {
	dok uint32
	tf  uint32
}

type duenn struct {
	wort string
	off  int64
}

type abschnitt struct {
	nr    int
	dic   *os.File
	pst   *os.File
	duenn []duenn
	dicGr int64
}

// Index: der Suchindex in einem Ordner.
type Index struct {
	mu        sync.RWMutex
	ordner    string
	dat       *os.File
	idx       *os.File
	urlDatei  *os.File
	datGr     int64
	n         uint32            // vergebene Seitennummern
	lebend    int               // nicht gelöschte Seiten
	woerterG  int64             // Wörter aller lebenden Seiten (für avgdl)
	urls      map[uint64]uint32 // Adresse → Seitennummer
	meta      []dokMeta
	abschn    []*abschnitt
	naechste  int
	puffer    map[string][]posting
	puffDoks  int
	puffBytes int
	totBytes  int64 // R898: Bytes gelöschter Seiten in dok.dat
	aufgesch  bool  // R898: Zusammenlegen wegen Platzmangel verschoben
	veraltPos uint32
}

type dokMeta struct {
	woerter uint32
	merker  uint32
	laenge  uint32 // R898: Größe in dok.dat
	zeit    uint32 // R898: zuletzt gelesen
}

func urlHash(u string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(Normieren(u)))
	return h.Sum64()
}

// IndexOeffnen: vorhandenen Index laden oder leeren anlegen.
func IndexOeffnen(ordner string) (*Index, error) {
	if err := os.MkdirAll(ordner, 0o750); err != nil {
		return nil, err
	}
	ix := &Index{ordner: ordner, urls: map[uint64]uint32{}, puffer: map[string][]posting{}}
	verdichtenAbschliessen(ordner)
	var err error
	auf := func(n string) (*os.File, error) {
		return os.OpenFile(filepath.Join(ordner, n), os.O_RDWR|os.O_CREATE, 0o640)
	}
	if ix.dat, err = auf("dok.dat"); err != nil {
		return nil, err
	}
	if ix.idx, err = auf("dok.idx"); err != nil {
		return nil, err
	}
	if ix.urlDatei, err = auf("url.idx"); err != nil {
		return nil, err
	}
	if fi, err := ix.dat.Stat(); err == nil {
		ix.datGr = fi.Size()
	}
	// Seitenliste
	b, err := io.ReadAll(ix.idx)
	if err != nil {
		return nil, err
	}
	b = b[:len(b)/idxBreite*idxBreite] // halb geschriebener Rest nach Stromausfall
	ix.n = uint32(len(b) / idxBreite)
	ix.meta = make([]dokMeta, ix.n)
	for i := uint32(0); i < ix.n; i++ {
		e := b[int(i)*idxBreite:]
		m := dokMeta{woerter: binary.LittleEndian.Uint32(e[12:]), merker: binary.LittleEndian.Uint32(e[20:]),
			laenge: binary.LittleEndian.Uint32(e[8:]), zeit: binary.LittleEndian.Uint32(e[16:])}
		ende := int64(binary.LittleEndian.Uint64(e[0:])) + int64(m.laenge)
		if ende > ix.datGr {
			m.merker |= merkerGeloescht // Daten fehlen – nicht ausliefern
			m.laenge = 0
		}
		ix.meta[i] = m
		if m.merker&merkerGeloescht == 0 {
			ix.lebend++
			ix.woerterG += int64(m.woerter)
		} else {
			ix.totBytes += int64(m.laenge)
		}
	}
	ub, err := io.ReadAll(ix.urlDatei)
	if err != nil {
		return nil, err
	}
	if rest := len(ub) % urlBreite; rest != 0 {
		ub = ub[:len(ub)-rest]
		_ = ix.urlDatei.Truncate(int64(len(ub)))
		_, _ = ix.urlDatei.Seek(int64(len(ub)), io.SeekStart)
	}
	for i := 0; i+urlBreite <= len(ub); i += urlBreite {
		d := binary.LittleEndian.Uint32(ub[i+8:])
		if d < ix.n {
			ix.urls[binary.LittleEndian.Uint64(ub[i:])] = d
		}
	}
	// Abschnitte
	namen, _ := filepath.Glob(filepath.Join(ordner, "seg-*.dic"))
	sort.Strings(namen)
	for _, n := range namen {
		var nr int
		if _, err := fmt.Sscanf(filepath.Base(n), "seg-%06d.dic", &nr); err != nil {
			continue
		}
		a, err := abschnittOeffnen(ordner, nr)
		if err != nil {
			continue // unvollständig (Absturz beim Schreiben) – Seiten kommen beim nächsten Durchlauf wieder
		}
		ix.abschn = append(ix.abschn, a)
		if nr >= ix.naechste {
			ix.naechste = nr + 1
		}
	}
	// Seiten nach dem letzten geschriebenen Abschnitt stehen in keinem
	// Wörterbuch (Absturz vor dem Schreiben) – als gelöscht führen, dann
	// liest der Crawler sie neu.
	var st struct{ Bis uint32 }
	if b, err := os.ReadFile(filepath.Join(ordner, "stand.json")); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	for i := st.Bis; i < ix.n; i++ {
		ix.loeschenGesperrt(i)
	}
	return ix, nil
}

func (ix *Index) standSchreiben() error {
	b, _ := json.Marshal(map[string]uint32{"bis": ix.n})
	tmp := filepath.Join(ix.ordner, "stand.json.tmp")
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(ix.ordner, "stand.json"))
}

func abschnittName(ordner string, nr int, endung string) string {
	return filepath.Join(ordner, fmt.Sprintf("seg-%06d.%s", nr, endung))
}

func abschnittOeffnen(ordner string, nr int) (*abschnitt, error) {
	if _, err := os.Stat(abschnittName(ordner, nr, "ok")); err != nil {
		return nil, errors.New("unvollständig")
	}
	dic, err := os.Open(abschnittName(ordner, nr, "dic"))
	if err != nil {
		return nil, err
	}
	pst, err := os.Open(abschnittName(ordner, nr, "pst"))
	if err != nil {
		dic.Close()
		return nil, err
	}
	a := &abschnitt{nr: nr, dic: dic, pst: pst}
	fi, _ := dic.Stat()
	a.dicGr = fi.Size()
	r := bufio.NewReader(io.NewSectionReader(dic, 0, a.dicGr))
	var off int64
	for i := 0; ; i++ {
		w, _, _, n, err := dicLesen(r)
		if err != nil {
			break
		}
		if i%duennAbstand == 0 {
			a.duenn = append(a.duenn, duenn{w, off})
		}
		off += int64(n)
	}
	return a, nil
}

// Wörterbuch-Eintrag: Länge(1) Wort Häufigkeit(uvarint) Lage(uvarint) Länge(uvarint)
func dicLesen(r *bufio.Reader) (wort string, off, laenge uint64, n int, err error) {
	l, err := r.ReadByte()
	if err != nil {
		return
	}
	buf := make([]byte, l)
	if _, err = io.ReadFull(r, buf); err != nil {
		return
	}
	n = 1 + int(l)
	var k int
	if _, k, err = uvarint(r); err != nil { // Häufigkeit (für spätere Nutzung)
		return
	}
	n += k
	if off, k, err = uvarint(r); err != nil {
		return
	}
	n += k
	if laenge, k, err = uvarint(r); err != nil {
		return
	}
	n += k
	return string(buf), off, laenge, n, nil
}

func uvarint(r *bufio.Reader) (uint64, int, error) {
	var x uint64
	var s uint
	for i := 0; i < 10; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, i, err
		}
		if b < 0x80 {
			return x | uint64(b)<<s, i + 1, nil
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	return 0, 10, errors.New("varint zu lang")
}

func (a *abschnitt) suchen(wort string) ([]posting, error) {
	i := sort.Search(len(a.duenn), func(i int) bool { return a.duenn[i].wort > wort }) - 1
	if i < 0 {
		return nil, nil
	}
	ende := a.dicGr
	if i+1 < len(a.duenn) {
		ende = a.duenn[i+1].off
	}
	r := bufio.NewReader(io.NewSectionReader(a.dic, a.duenn[i].off, ende-a.duenn[i].off))
	for {
		w, off, laenge, _, err := dicLesen(r)
		if err != nil {
			return nil, nil
		}
		if w == wort {
			buf := make([]byte, laenge)
			if _, err := a.pst.ReadAt(buf, int64(off)); err != nil {
				return nil, err
			}
			return postingsLesen(buf), nil
		}
		if w > wort {
			return nil, nil
		}
	}
}

func postingsLesen(b []byte) []posting {
	var out []posting
	var dok uint64
	for len(b) > 0 {
		d, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		b = b[n:]
		tf, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		b = b[n:]
		dok += d
		out = append(out, posting{uint32(dok), uint32(tf)})
	}
	return out
}

func postingsSchreiben(p []posting) []byte {
	buf := make([]byte, 0, len(p)*3)
	var tmp [binary.MaxVarintLen64]byte
	var vorher uint32
	for i, x := range p {
		d := x.dok
		if i > 0 {
			d = x.dok - vorher
		}
		vorher = x.dok
		buf = append(buf, tmp[:binary.PutUvarint(tmp[:], uint64(d))]...)
		buf = append(buf, tmp[:binary.PutUvarint(tmp[:], uint64(x.tf))]...)
	}
	return buf
}

// Hat: ist die Adresse im Index, und wann wurde sie gelesen?
func (ix *Index) Hat(u string) (bool, time.Time) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	d, ok := ix.urls[urlHash(u)]
	if !ok || ix.meta[d].merker&merkerGeloescht != 0 {
		return false, time.Time{}
	}
	var e [idxBreite]byte
	if _, err := ix.idx.ReadAt(e[:], int64(d)*idxBreite); err != nil {
		return true, time.Time{}
	}
	return true, time.Unix(int64(binary.LittleEndian.Uint32(e[16:])), 0)
}

// Hinzufuegen: Seite aufnehmen; eine ältere Fassung derselben Adresse wird ersetzt.
func (ix *Index) Hinzufuegen(d Dokument) error {
	if Normieren(d.URL) == "" {
		return errors.New("keine Webadresse")
	}
	if d.Zeit == 0 {
		d.Zeit = time.Now().Unix()
	}
	if r := []rune(d.Text); len(r) > 8000 {
		d.Text = string(r[:8000])
	}
	zaehl := map[string]uint32{}
	for _, w := range Woerter(d.Titel) {
		zaehl[w] += 3
	}
	for _, w := range Woerter(d.Beschreibung) {
		zaehl[w] += 2
	}
	gesamt := uint32(0)
	for _, w := range Woerter(d.Text) {
		zaehl[w]++
		gesamt++
	}
	for _, w := range Woerter(urlWoerter(d.URL)) {
		zaehl[w]++
	}
	gesamt += uint32(len(Woerter(d.Titel)))

	var roh bytes.Buffer
	fw, _ := flate.NewWriter(&roh, flate.BestSpeed)
	_ = json.NewEncoder(fw).Encode(d)
	fw.Close()

	ix.mu.Lock()
	defer ix.mu.Unlock()
	if _, err := ix.dat.WriteAt(roh.Bytes(), ix.datGr); err != nil {
		return err
	}
	nr := ix.n
	var e [idxBreite]byte
	binary.LittleEndian.PutUint64(e[0:], uint64(ix.datGr))
	binary.LittleEndian.PutUint32(e[8:], uint32(roh.Len()))
	binary.LittleEndian.PutUint32(e[12:], gesamt)
	binary.LittleEndian.PutUint32(e[16:], uint32(d.Zeit))
	if _, err := ix.idx.WriteAt(e[:], int64(nr)*idxBreite); err != nil {
		return err
	}
	ix.datGr += int64(roh.Len())
	h := urlHash(d.URL)
	if alt, ok := ix.urls[h]; ok {
		ix.loeschenGesperrt(alt)
	}
	var ue [urlBreite]byte
	binary.LittleEndian.PutUint64(ue[0:], h)
	binary.LittleEndian.PutUint32(ue[8:], nr)
	if _, err := ix.urlDatei.Write(ue[:]); err != nil {
		return err
	}
	ix.urls[h] = nr
	ix.meta = append(ix.meta, dokMeta{woerter: gesamt, laenge: uint32(roh.Len()), zeit: uint32(d.Zeit)})
	ix.n++
	ix.lebend++
	ix.woerterG += int64(gesamt)
	for w, tf := range zaehl {
		ix.puffer[w] = append(ix.puffer[w], posting{nr, tf})
		ix.puffBytes += len(w) + 8
	}
	ix.puffDoks++
	if ix.puffDoks >= puffDokumente || ix.puffBytes >= puffBytes {
		return ix.schreibenGesperrt()
	}
	return nil
}

func urlWoerter(u string) string {
	if p, err := parseURL(u); err == nil {
		return strings.NewReplacer(".", " ", "/", " ", "-", " ", "_", " ").Replace(p.Hostname() + " " + p.Path)
	}
	return ""
}

func (ix *Index) loeschenGesperrt(nr uint32) {
	if nr >= ix.n || ix.meta[nr].merker&merkerGeloescht != 0 {
		return
	}
	ix.meta[nr].merker |= merkerGeloescht
	ix.totBytes += int64(ix.meta[nr].laenge)
	ix.lebend--
	ix.woerterG -= int64(ix.meta[nr].woerter)
	var m [4]byte
	binary.LittleEndian.PutUint32(m[:], ix.meta[nr].merker)
	_, _ = ix.idx.WriteAt(m[:], int64(nr)*idxBreite+20)
}

// Loeschen: Adresse aus dem Index nehmen (z. B. noindex beim Wiederlesen).
func (ix *Index) Loeschen(u string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if d, ok := ix.urls[urlHash(u)]; ok {
		ix.loeschenGesperrt(d)
	}
}

// Schreiben: Puffer als Abschnitt auf die Platte (auch beim Beenden).
func (ix *Index) Schreiben() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.schreibenGesperrt()
}

func (ix *Index) schreibenGesperrt() error {
	if len(ix.puffer) == 0 {
		return nil
	}
	if err := ix.dat.Sync(); err != nil {
		return err
	}
	_ = ix.idx.Sync()
	_ = ix.urlDatei.Sync()
	woerter := make([]string, 0, len(ix.puffer))
	for w := range ix.puffer {
		woerter = append(woerter, w)
	}
	sort.Strings(woerter)
	nr := ix.naechste
	i := 0
	err := abschnittSchreiben(ix.ordner, nr, func() (string, []posting, bool) {
		if i >= len(woerter) {
			return "", nil, false
		}
		w := woerter[i]
		i++
		return w, ix.puffer[w], true
	})
	if err != nil {
		return err
	}
	a, err := abschnittOeffnen(ix.ordner, nr)
	if err != nil {
		return err
	}
	ix.abschn = append(ix.abschn, a)
	ix.naechste++
	ix.puffer = map[string][]posting{}
	ix.puffDoks, ix.puffBytes = 0, 0
	if err := ix.standSchreiben(); err != nil {
		return err
	}
	if len(ix.abschn) > maxAbschnitte {
		return ix.zusammenlegenGesperrt()
	}
	return nil
}

func abschnittSchreiben(ordner string, nr int, naechstes func() (string, []posting, bool)) error {
	dicF, err := os.Create(abschnittName(ordner, nr, "dic"))
	if err != nil {
		return err
	}
	defer dicF.Close()
	pstF, err := os.Create(abschnittName(ordner, nr, "pst"))
	if err != nil {
		return err
	}
	defer pstF.Close()
	dw, pw := bufio.NewWriterSize(dicF, 64<<10), bufio.NewWriterSize(pstF, 256<<10)
	var off uint64
	var tmp [binary.MaxVarintLen64]byte
	for {
		w, p, ok := naechstes()
		if !ok {
			break
		}
		if len(p) == 0 || len(w) > 255 {
			continue
		}
		b := postingsSchreiben(p)
		if _, err := pw.Write(b); err != nil {
			return err
		}
		dw.WriteByte(byte(len(w)))
		dw.WriteString(w)
		dw.Write(tmp[:binary.PutUvarint(tmp[:], uint64(len(p)))])
		dw.Write(tmp[:binary.PutUvarint(tmp[:], off)])
		dw.Write(tmp[:binary.PutUvarint(tmp[:], uint64(len(b)))])
		off += uint64(len(b))
	}
	if err := dw.Flush(); err != nil {
		return err
	}
	if err := pw.Flush(); err != nil {
		return err
	}
	if err := dicF.Sync(); err != nil {
		return err
	}
	if err := pstF.Sync(); err != nil {
		return err
	}
	return os.WriteFile(abschnittName(ordner, nr, "ok"), []byte("1"), 0o640)
}

// zusammenlegenGesperrt: alle Abschnitte zu einem; gelöschte Seiten fallen weg.
func (ix *Index) zusammenlegenGesperrt() error {
	type leser struct {
		a    *abschnitt
		r    *bufio.Reader
		wort string
		off  uint64
		len  uint64
		ok   bool
	}
	// Gestaffelt: sind die neueren Abschnitte zusammen kleiner als der
	// älteste, werden nur sie zusammengelegt – sonst alle. So wird der große
	// Bestand nicht bei jedem Mal neu geschrieben.
	auswahl := ix.abschn
	behalten := []*abschnitt{}
	if len(ix.abschn) > 1 {
		var neuere int64
		for _, a := range ix.abschn[1:] {
			neuere += a.dicGr + groesse(a.pst)
		}
		if neuere < ix.abschn[0].dicGr+groesse(ix.abschn[0].pst) {
			behalten, auswahl = ix.abschn[:1], ix.abschn[1:]
		}
	}
	var noetig int64
	for _, a := range auswahl {
		noetig += a.dicGr + groesse(a.pst)
	}
	if frei, _, err := statfsGB(ix.ordner); err == nil && frei*1e9 < float64(noetig)*1.2+50e6 {
		ix.aufgesch = true // später, wenn Platz ist (Pflegen)
		return nil
	}
	ix.aufgesch = false
	var ls []*leser
	for _, a := range auswahl {
		l := &leser{a: a, r: bufio.NewReader(io.NewSectionReader(a.dic, 0, a.dicGr))}
		l.wort, l.off, l.len, _, _ = dicLesen(l.r)
		l.ok = l.wort != ""
		ls = append(ls, l)
	}
	nr := ix.naechste
	err := abschnittSchreiben(ix.ordner, nr, func() (string, []posting, bool) {
		for {
			min := ""
			for _, l := range ls {
				if l.ok && (min == "" || l.wort < min) {
					min = l.wort
				}
			}
			if min == "" {
				return "", nil, false
			}
			var alle []posting
			for _, l := range ls { // Abschnitte sind nach Seitennummer geordnet
				if !l.ok || l.wort != min {
					continue
				}
				buf := make([]byte, l.len)
				if _, err := l.a.pst.ReadAt(buf, int64(l.off)); err == nil {
					for _, p := range postingsLesen(buf) {
						if p.dok < ix.n && ix.meta[p.dok].merker&merkerGeloescht == 0 {
							alle = append(alle, p)
						}
					}
				}
				var err error
				l.wort, l.off, l.len, _, err = dicLesen(l.r)
				l.ok = err == nil
			}
			if len(alle) > 0 {
				sort.Slice(alle, func(i, j int) bool { return alle[i].dok < alle[j].dok })
				return min, alle, true
			}
		}
	})
	if err != nil {
		os.Remove(abschnittName(ix.ordner, nr, "dic"))
		os.Remove(abschnittName(ix.ordner, nr, "pst"))
		return err
	}
	neu, err := abschnittOeffnen(ix.ordner, nr)
	if err != nil {
		return err
	}
	alt := auswahl
	ix.abschn = append(append([]*abschnitt{}, behalten...), neu)
	ix.naechste++
	for _, a := range alt {
		a.dic.Close()
		a.pst.Close()
		for _, e := range []string{"ok", "dic", "pst"} { // "ok" zuerst: nie halbe Abschnitte laden
			os.Remove(abschnittName(ix.ordner, a.nr, e))
		}
	}
	return nil
}

func groesse(f *os.File) int64 {
	if fi, err := f.Stat(); err == nil {
		return fi.Size()
	}
	return 0
}

// ---------------------------------------------------------------------------
// Suchen

type wertung struct {
	dok    uint32
	punkte float64
	worte  int
}

type wertHeap []wertung

func (h wertHeap) Len() int            { return len(h) }
func (h wertHeap) Less(i, j int) bool  { return h[i].punkte < h[j].punkte }
func (h wertHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *wertHeap) Push(x interface{}) { *h = append(*h, x.(wertung)) }
func (h *wertHeap) Pop() interface{} {
	o := *h
	x := o[len(o)-1]
	*h = o[:len(o)-1]
	return x
}

// Suchen: die n besten Seiten. Seiten mit allen Suchwörtern stehen vorn.
func (ix *Index) Suchen(ctx context.Context, anfrage string, n int) ([]Treffer, error) {
	woerter := Woerter(anfrage)
	if len(woerter) == 0 {
		return nil, nil
	}
	if len(woerter) > 8 {
		woerter = woerter[:8]
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.lebend == 0 {
		return nil, nil
	}
	avgdl := float64(ix.woerterG) / float64(ix.lebend)
	if avgdl < 1 {
		avgdl = 1
	}
	punkte := map[uint32]*wertung{}
	gesehen := map[string]bool{}
	for _, w := range woerter {
		if gesehen[w] {
			continue
		}
		gesehen[w] = true
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var alle []posting
		for _, a := range ix.abschn {
			p, err := a.suchen(w)
			if err != nil {
				return nil, err
			}
			alle = append(alle, p...)
		}
		alle = append(alle, ix.puffer[w]...)
		df := 0
		for _, p := range alle {
			if p.dok < ix.n && ix.meta[p.dok].merker&merkerGeloescht == 0 {
				df++
			}
		}
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (float64(ix.lebend)-float64(df)+0.5)/(float64(df)+0.5))
		for _, p := range alle {
			if p.dok >= ix.n || ix.meta[p.dok].merker&merkerGeloescht != 0 {
				continue
			}
			dl := float64(ix.meta[p.dok].woerter)
			tf := float64(p.tf)
			s := idf * tf * (bm25k1 + 1) / (tf + bm25k1*(1-bm25b+bm25b*dl/avgdl))
			if v := punkte[p.dok]; v != nil {
				v.punkte += s
				v.worte++
			} else {
				punkte[p.dok] = &wertung{p.dok, s, 1}
			}
		}
	}
	h := &wertHeap{}
	for _, v := range punkte {
		anteil := float64(v.worte) / float64(len(gesehen))
		w := *v
		w.punkte *= anteil * anteil
		if h.Len() < n {
			heap.Push(h, w)
		} else if w.punkte > (*h)[0].punkte {
			(*h)[0] = w
			heap.Fix(h, 0)
		}
	}
	reihe := make([]wertung, h.Len())
	for i := len(reihe) - 1; i >= 0; i-- {
		reihe[i] = heap.Pop(h).(wertung)
	}
	var out []Treffer
	for _, r := range reihe {
		d, err := ix.dokumentGesperrt(r.dok)
		if err != nil {
			continue
		}
		text := Auszug(d.Text, woerter, 300)
		if text == "" {
			text = d.Beschreibung
		}
		out = append(out, Treffer{Titel: d.Titel, URL: d.URL, Text: text, Punkte: r.punkte})
	}
	return out, nil
}

func (ix *Index) dokumentGesperrt(nr uint32) (*Dokument, error) {
	var e [idxBreite]byte
	if _, err := ix.idx.ReadAt(e[:], int64(nr)*idxBreite); err != nil {
		return nil, err
	}
	off := int64(binary.LittleEndian.Uint64(e[0:]))
	l := binary.LittleEndian.Uint32(e[8:])
	if l > 4<<20 {
		return nil, errors.New("Eintrag beschädigt")
	}
	buf := make([]byte, l)
	if _, err := ix.dat.ReadAt(buf, off); err != nil {
		return nil, err
	}
	var d Dokument
	if err := json.NewDecoder(flate.NewReader(bytes.NewReader(buf))).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Stand: Zahlen für die Anzeige.
func (ix *Index) Stand() (dokumente int, abschnitte int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.lebend, len(ix.abschn)
}

// Schliessen: Puffer schreiben, Dateien zu.
func (ix *Index) Schliessen() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	err := ix.schreibenGesperrt()
	for _, a := range ix.abschn {
		a.dic.Close()
		a.pst.Close()
	}
	ix.abschn = nil
	ix.dat.Close()
	ix.idx.Close()
	ix.urlDatei.Close()
	return err
}
