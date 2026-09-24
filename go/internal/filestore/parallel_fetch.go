package filestore

import (
	"context"
	"fmt"
	"sync"
)

// ─── Paralleler Chunk-Download (Sliding-Window-Prefetch) ─────────────────────
//
// Bottleneck vorher: Chunks wurden streng sequenziell geholt (Chunk N erst nach
// N-1), obwohl jeder Chunk 5-fach auf verschiedenen Hosts repliziert ist. Damit
// lag bei 5 Hosts ein Großteil der Bandbreite brach.
//
// Lösung: Bis zu `window` Chunks werden GLEICHZEITIG vorausgeholt (jeder über
// fetchChunk, das selbst den schnellsten erreichbaren Host wählt). Geschrieben
// wird aber strikt IN REIHENFOLGE — sobald der jeweils nächste erwartete Chunk
// fertig ist. So bleibt die Datei korrekt und der Gesamthash stimmt, während die
// Latenz vieler Chunks überlappt.
//
// RAM-Schutz: `window` ist klein (Default unten); zusätzlich begrenzt der
// fetchSem in fetchChunk die tatsächlich gleichzeitig im Speicher liegenden
// Chunks. Beides zusammen hält den Peak auf 1-GB-Pis sicher.

// parallelFetchWindow ist die Anzahl gleichzeitig vorausgeholter Chunks.
// Bewusst klein gewählt: window × ChunkSize (z.B. 4 × 16 MiB = 64 MiB) bleibt
// weit unter 1 GB RAM, nutzt aber die 5-fache Replikation spürbar aus.
const parallelFetchWindow = 6

// fetchResult ist das Ergebnis eines vorausgeholten Chunks (Reihenfolge via Index).
type fetchResult struct {
	index int
	data  []byte
	err   error
}

// orderedParallelFetch holt die Chunks aus `hashes` mit einem Prefetch-Fenster
// der Größe `window` und ruft `emit` für jeden Chunk STRIKT in aufsteigender
// Index-Reihenfolge auf. Gibt beim ersten Fehler (oder ctx-Abbruch) zurück.
//
// `fetch` kapselt den eigentlichen Abruf eines einzelnen Chunks (i.d.R.
// fs.fetchChunk). `emit` schreibt/verarbeitet den fertigen Chunk; gibt emit
// einen Fehler zurück, wird abgebrochen.
func orderedParallelFetch(
	ctx context.Context,
	hashes []string,
	window int,
	fetch func(ctx context.Context, hash string) ([]byte, error),
	emit func(index int, data []byte) error,
) error {
	if window < 1 {
		window = 1
	}
	n := len(hashes)
	if n == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Ergebnis-Puffer für noch nicht ausgegebene (out-of-order) Chunks.
	pending := make(map[int]([]byte))
	results := make(chan fetchResult, window)

	sem := make(chan struct{}, window)
	var wg sync.WaitGroup

	// Dispatcher: startet Fetches, aber höchstens `window` gleichzeitig.
	go func() {
		for i := 0; i < n; i++ {
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				defer func() { <-sem }()
				data, err := fetch(ctx, hashes[idx])
				select {
				case results <- fetchResult{index: idx, data: data, err: err}:
				case <-ctx.Done():
				}
			}(i)
		}
		wg.Wait()
		close(results)
	}()

	// Sammler: gibt Chunks streng in Reihenfolge aus.
	next := 0
	for next < n {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r, ok := <-results:
			if !ok {
				if next < n {
					return fmt.Errorf("paralleler Download: Stream endete bei %d/%d", next, n)
				}
				return nil
			}
			if r.err != nil {
				return fmt.Errorf("paralleler Download: chunk %d: %w", r.index, r.err)
			}
			pending[r.index] = r.data
			// Alle jetzt verfügbaren, lückenlos folgenden Chunks ausgeben.
			for {
				data, ok := pending[next]
				if !ok {
					break
				}
				if err := emit(next, data); err != nil {
					return err
				}
				delete(pending, next)
				next++
			}
		}
	}
	return nil
}
