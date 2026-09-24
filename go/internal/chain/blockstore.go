package chain

// BlockStore kapselt die Persistenz der Blöcke. Die Blockchain spricht nur
// dieses Interface an — das Backend (bbolt) ist austauschbar und die Chain-Logik
// bleibt davon unberührt.
//
// Zugriffsmuster: append-only (ein Block alle BlockTime), Random-Read nach Höhe
// (Sync), voller sequenzieller Durchlauf beim Start (State-Replay). Das ist ein
// klassischer KV-Workload mit fortlaufenden uint64-Keys — der Best-Case für den
// B+Tree von bbolt (Einträge landen immer rechts, kein Rebalancing).

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var blocksBucket = []byte("blocks")

// BlockStore ist die Persistenz-Schnittstelle für Blöcke.
type BlockStore interface {
	Put(height uint64, blk *Block) error
	Get(height uint64) (*Block, error)
	Has(height uint64) (bool, error)
	Highest() (uint64, bool, error) // höchste gespeicherte Höhe
	Close() error
}

// boltStore ist die bbolt-Implementierung: eine einzige memory-mapped Datei,
// kein Hintergrund-GC/Kompaktierung (schonend für RAM und SD/USB-Flash),
// reines Go (cross-kompiliert mit CGO_ENABLED=0 für ARM).
type boltStore struct {
	db *bolt.DB
}

// heightKey kodiert die Höhe als 8-Byte big-endian — so ist die
// bbolt-Iterationsreihenfolge identisch mit der numerischen Reihenfolge.
func heightKey(h uint64) []byte {
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], h)
	return k[:]
}

// OpenBlockStore öffnet (oder erstellt) die Blockstore-DB unter dir/chain.db.
func OpenBlockStore(dir string) (BlockStore, error) {
	path := filepath.Join(dir, "chain.db")
	// Timeout: bbolt lockt die Datei exklusiv. Ohne Timeout wartet Open bei einem
	// bestehenden Lock UNENDLICH (Deadlock, wenn dieselbe DB versehentlich zweimal
	// geöffnet wird). Mit Timeout gibt es stattdessen einen klaren Fehler.
	db, err := bolt.Open(path, 0o640, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("chain: Blockstore öffnen (%s): %w — läuft evtl. schon ein Prozess auf dieser DB?", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, e := tx.CreateBucketIfNotExists(blocksBucket)
		return e
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("chain: Bucket anlegen: %w", err)
	}
	return &boltStore{db: db}, nil
}

func (s *boltStore) Put(height uint64, blk *Block) error {
	data := encodeBlockBinary(blk)
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(blocksBucket).Put(heightKey(height), data)
	})
}

func (s *boltStore) Get(height uint64) (*Block, error) {
	var blk *Block
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(blocksBucket).Get(heightKey(height))
		if v == nil {
			return fmt.Errorf("chain: Block %d nicht gefunden", height)
		}
		// v ist nur innerhalb der Transaktion gültig → dekodieren kopiert.
		b, err := decodeBlockBinary(v)
		if err != nil {
			return err
		}
		blk = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return blk, nil
}

func (s *boltStore) Has(height uint64) (bool, error) {
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		found = tx.Bucket(blocksBucket).Get(heightKey(height)) != nil
		return nil
	})
	return found, err
}

func (s *boltStore) Highest() (uint64, bool, error) {
	var h uint64
	var ok bool
	err := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(blocksBucket).Cursor()
		k, _ := c.Last()
		if k == nil {
			return nil
		}
		h = binary.BigEndian.Uint64(k)
		ok = true
		return nil
	})
	return h, ok, err
}

func (s *boltStore) Close() error { return s.db.Close() }
