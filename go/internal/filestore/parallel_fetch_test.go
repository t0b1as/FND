package filestore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestOrderedParallelFetchOrder: Chunks kommen in zufälliger Zeit zurück, müssen
// aber strikt in Reihenfolge emittiert werden.
func TestOrderedParallelFetchOrder(t *testing.T) {
	n := 50
	hashes := make([]string, n)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i)
	}

	fetch := func(ctx context.Context, hash string) ([]byte, error) {
		// Variable Latenz: spätere Indizes teils schneller → testet Reordering.
		var idx int
		fmt.Sscanf(hash, "%064x", &idx)
		time.Sleep(time.Duration((n-idx)%7) * time.Millisecond)
		return []byte(hash), nil
	}

	var emitted []int
	err := orderedParallelFetch(context.Background(), hashes, 4, fetch,
		func(i int, data []byte) error {
			emitted = append(emitted, i)
			return nil
		})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(emitted) != n {
		t.Fatalf("emittiert %d != %d", len(emitted), n)
	}
	for i := 0; i < n; i++ {
		if emitted[i] != i {
			t.Fatalf("Reihenfolge verletzt bei %d: %v", i, emitted[:i+1])
		}
	}
}

// TestOrderedParallelFetchConcurrencyCap: nie mehr als `window` gleichzeitig.
func TestOrderedParallelFetchConcurrencyCap(t *testing.T) {
	n := 40
	window := 4
	hashes := make([]string, n)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i)
	}

	var inFlight int32
	var maxSeen int32
	fetch := func(ctx context.Context, hash string) ([]byte, error) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxSeen)
			if cur <= m || atomic.CompareAndSwapInt32(&maxSeen, m, cur) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return []byte(hash), nil
	}

	err := orderedParallelFetch(context.Background(), hashes, window, fetch,
		func(i int, data []byte) error { return nil })
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if maxSeen > int32(window) {
		t.Fatalf("Parallelität überschritt Fenster: %d > %d", maxSeen, window)
	}
}

// TestOrderedParallelFetchError: ein Fetch-Fehler bricht sauber ab.
func TestOrderedParallelFetchError(t *testing.T) {
	n := 20
	hashes := make([]string, n)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i)
	}
	sentinel := errors.New("kaputt")
	fetch := func(ctx context.Context, hash string) ([]byte, error) {
		var idx int
		fmt.Sscanf(hash, "%064x", &idx)
		if idx == 10 {
			return nil, sentinel
		}
		return []byte(hash), nil
	}
	err := orderedParallelFetch(context.Background(), hashes, 4, fetch,
		func(i int, data []byte) error { return nil })
	if err == nil {
		t.Fatal("Fehler hätte propagiert werden müssen")
	}
}

// TestOrderedParallelFetchEmitError: ein emit-Fehler bricht ab.
func TestOrderedParallelFetchEmitError(t *testing.T) {
	n := 20
	hashes := make([]string, n)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%064x", i)
	}
	fetch := func(ctx context.Context, hash string) ([]byte, error) {
		return []byte(hash), nil
	}
	var mu sync.Mutex
	count := 0
	err := orderedParallelFetch(context.Background(), hashes, 4, fetch,
		func(i int, data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			count++
			if i == 5 {
				return errors.New("emit-stop")
			}
			return nil
		})
	if err == nil {
		t.Fatal("emit-Fehler hätte abbrechen müssen")
	}
}

// TestOrderedParallelFetchEmpty: leere Liste ist ein No-Op.
func TestOrderedParallelFetchEmpty(t *testing.T) {
	err := orderedParallelFetch(context.Background(), nil, 4,
		func(ctx context.Context, h string) ([]byte, error) { return nil, nil },
		func(i int, d []byte) error { return errors.New("darf nicht aufgerufen werden") })
	if err != nil {
		t.Fatalf("leere Liste: %v", err)
	}
}
