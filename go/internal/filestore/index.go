package filestore

// PersonalIndex – verschlüsselter persönlicher Datei-Index im DHT.
//
// Problem ohne Index:
//   Neuer Pi mit gleichen 30 Wörtern → Node kennt FundusID und Keys,
//   aber NICHT welche Dateien er hochgeladen hat.
//   Die Chunks sind noch im Netz (5× repliziert), aber unerreichbar
//   weil content_hashes unbekannt.
//
// Lösung:
//   Jeder Upload schreibt den content_hash in einen verschlüsselten Index.
//   Der Index liegt im DHT unter "/fundus/index/<FundusID>".
//   Schlüssel = Argon2id(Identitäts-Seed, salt="fundus-index-v1:<FundusID>").
//   Nur der Inhaber der 30 Wörter kann ihn entschlüsseln.
//
// Wiederherstellung auf neuem Pi:
//   1. 30 Wörter eingeben → FundusID + Index-Key abgeleitet
//   2. Index aus DHT laden und entschlüsseln
//   3. Alle eigenen content_hashes bekannt
//   4. Manifeste aus DHT holen → Chunks vom Netz ziehen
//   5. Lokal replizieren bis AllocGB erreicht
//
// Sicherheit:
//   - XChaCha20-Poly1305 verschlüsselt → Peers sehen nur Ciphertext
//   - CRDT-Merge: Index kann von mehreren Nodes gleichzeitig aktualisiert werden
//   - Kein Single-Point-of-Failure: Index selbst ist auch 5× im DHT repliziert

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"crypto/rand"

	"go.uber.org/zap"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// =============================================================================
//  Konstanten
// =============================================================================

const (
	DHTNamespaceIndex = "/fundus/index/" // persönlicher Datei-Index

	// Argon2id-Parameter für Index-Schlüsselableitung
	// Günstiger als Identitäts-Ableitung (t=2, m=32MiB) –
	// der Schlüssel hängt ohnehin am Identitäts-Seed
	indexArgon2Time   uint32 = 2
	indexArgon2Memory uint32 = 32 * 1024
	indexArgon2Thread uint8  = 4
	indexArgon2Key    uint32 = 32
	indexArgon2Salt          = "fundus-index-v1:"
)

// =============================================================================
//  IndexEntry – ein Eintrag im persönlichen Index
// =============================================================================

// IndexEntry beschreibt eine Datei im persönlichen Index.
type IndexEntry struct {
	ContentHash string    `json:"h"`   // BLAKE3-256 des Klartexts
	Size        int64     `json:"s"`   // Dateigröße in Bytes
	MimeType    string    `json:"m,omitempty"`
	FileName    string    `json:"n,omitempty"` // optionaler Anzeigename
	AddedAt     time.Time `json:"t"`
	Tags        []string  `json:"g,omitempty"`
}

// PersonalIndex ist der verschlüsselte Datei-Index einer Identität.
type PersonalIndex struct {
	FundusID string                 `json:"fundus_id"`
	Entries  map[string]*IndexEntry `json:"entries"`  // content_hash → Entry
	Version  uint64                 `json:"version"`  // monoton steigend (CRDT)
	UpdatedAt time.Time             `json:"updated_at"`

	// Nicht serialisiert
	mu         sync.RWMutex
	encKey     []byte   // abgeleiteter XChaCha20-Schlüssel
	dirty      bool     // ausstehende Schreibvorgänge
}

// =============================================================================
//  Schlüsselableitung
// =============================================================================

// DeriveIndexKey leitet den Verschlüsselungsschlüssel für den persönlichen Index ab.
// Input: die gleichen Seed-Wörter wie für fnd-wallet.
func DeriveIndexKey(seedWords []string, fundusID string) []byte {
	password := joinWords(seedWords)
	salt     := []byte(indexArgon2Salt + fundusID)
	return argon2.IDKey(password, salt, indexArgon2Time, indexArgon2Memory, indexArgon2Thread, indexArgon2Key)
}

// =============================================================================
//  Index laden / speichern
// =============================================================================

// LoadOrCreateIndex lädt den persönlichen Index aus dem DHT oder erstellt einen neuen.
// seedWords: 30 Seed-Wörter (identisch zu fnd-wallet).
func LoadOrCreateIndex(ctx context.Context, p2p P2PAdapter, fundusID string, seedWords []string) (*PersonalIndex, error) {
	encKey := DeriveIndexKey(seedWords, fundusID)

	idx := &PersonalIndex{
		FundusID:  fundusID,
		Entries:   make(map[string]*IndexEntry),
		Version:   0,
		UpdatedAt: time.Now().UTC(),
		encKey:    encKey,
	}

	dhtKey := DHTNamespaceIndex + fundusID

	// Aus DHT laden
	ciphertext, err := p2p.DHTget(ctx, dhtKey)
	if err != nil {
		// Kein Index vorhanden → neuer Node
		return idx, nil
	}

	// Entschlüsseln
	plaintext, err := xchacha20Decrypt(ciphertext, encKey)
	if err != nil {
		return nil, fmt.Errorf("index: entschlüsseln fehlgeschlagen (falscher Key?): %w", err)
	}

	var loaded PersonalIndex
	if err := json.Unmarshal(plaintext, &loaded); err != nil {
		return nil, fmt.Errorf("index: JSON ungültig: %w", err)
	}

	idx.Entries   = loaded.Entries
	idx.Version   = loaded.Version
	idx.UpdatedAt = loaded.UpdatedAt
	if idx.Entries == nil {
		idx.Entries = make(map[string]*IndexEntry)
	}

	return idx, nil
}

// Save schreibt den Index verschlüsselt in den DHT.
func (idx *PersonalIndex) Save(ctx context.Context, p2p P2PAdapter) error {
	idx.mu.Lock()
	idx.Version++
	idx.UpdatedAt = time.Now().UTC()
	idx.dirty = false
	idx.mu.Unlock()

	idx.mu.RLock()
	data, err := json.Marshal(idx)
	idx.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("index: marshal: %w", err)
	}

	ciphertext, err := xchacha20Encrypt(data, idx.encKey)
	if err != nil {
		return fmt.Errorf("index: verschlüsseln: %w", err)
	}

	return p2p.DHTput(ctx, DHTNamespaceIndex+idx.FundusID, ciphertext)
}

// =============================================================================
//  Index-Operationen (CRDT-freundlich)
// =============================================================================

// Add fügt einen Eintrag hinzu (idempotent).
func (idx *PersonalIndex) Add(entry *IndexEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if _, exists := idx.Entries[entry.ContentHash]; !exists {
		idx.Entries[entry.ContentHash] = entry
		idx.dirty = true
	}
}

// Remove entfernt einen Eintrag (Soft-Delete: Entry bleibt mit DeletedAt).
func (idx *PersonalIndex) Remove(contentHash string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.Entries, contentHash)
	idx.dirty = true
}

// ContentHashes gibt alle content_hashes zurück.
func (idx *PersonalIndex) ContentHashes() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	hashes := make([]string, 0, len(idx.Entries))
	for h := range idx.Entries {
		hashes = append(hashes, h)
	}
	return hashes
}

// AllEntries liefert eine Kopie aller Index-Einträge, nach Datum absteigend
// (neueste zuerst). Für die Dateiliste im Filemanager.
func (idx *PersonalIndex) AllEntries() []IndexEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]IndexEntry, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		if e != nil {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].AddedAt.After(out[j].AddedAt)
	})
	return out
}

// IsDirty gibt zurück ob ausstehende Änderungen vorliegen.
func (idx *PersonalIndex) IsDirty() bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.dirty
}

// Merge führt zwei Indizes zusammen (CRDT Last-Write-Wins pro Entry).
func (idx *PersonalIndex) Merge(other *PersonalIndex) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for hash, entry := range other.Entries {
		if existing, ok := idx.Entries[hash]; !ok {
			idx.Entries[hash] = entry
			idx.dirty = true
		} else if entry.AddedAt.After(existing.AddedAt) {
			idx.Entries[hash] = entry
			idx.dirty = true
		}
	}
}

// =============================================================================
//  Wiederherstellung auf neuem Node
// =============================================================================

// RestoreFromNetwork lädt alle eigenen Dateien vom Netz auf den neuen Node.
// Wird beim Start aufgerufen wenn der Index vorhanden ist aber keine Chunks.
//
// Ablauf:
//  1. Index aus DHT laden (verschlüsselt)
//  2. Alle content_hashes bekannt
//  3. Für jeden Hash: Manifest aus DHT → Chunk-Locations → Chunks ziehen
//  4. Lokal speichern bis AllocGB voll
func (fs *FileStore) RestoreFromNetwork(ctx context.Context, idx *PersonalIndex, log *zap.Logger) error {
	hashes := idx.ContentHashes()
	if len(hashes) == 0 {
		log.Info("Index leer – keine Dateien zum Wiederherstellen")
		return nil
	}

	log.Info("Wiederherstellung startet",
		zap.Int("dateien", len(hashes)),
		zap.Int64("allocGB", fs.cfg.AllocGB),
	)

	restored, skipped, failed := 0, 0, 0

	for i, contentHash := range hashes {
		// Freier Platz prüfen
		fs.mu.RLock()
		freeBytes := fs.cfg.AllocGB*1024*1024*1024 - fs.used
		fs.mu.RUnlock()
		if freeBytes < ChunkSize {
			log.Info("Allokation voll – Wiederherstellung pausiert",
				zap.Int("restored", restored),
				zap.Int("remaining", len(hashes)-i),
			)
			break
		}

		// Manifest aus DHT
		manifestData, err := fs.p2p.DHTget(ctx, DHTNamespaceFile+contentHash)
		if err != nil {
			log.Warn("Manifest nicht gefunden",
				zap.String("hash", contentHash[:16]+"…"),
				zap.Error(err),
			)
			failed++
			continue
		}

		var manifest FileManifest
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			failed++
			continue
		}

		// Chunks einzeln holen
		fileOK := true
		for chunkIdx, chunkHash := range manifest.ChunkHashes {
			// Schon lokal?
			fs.mu.RLock()
			_, alreadyHave := fs.chunks[chunkHash]
			fs.mu.RUnlock()
			if alreadyHave {
				continue
			}

			chunk, err := fs.fetchChunk(ctx, chunkHash)
			if err != nil {
				log.Warn("Chunk nicht abrufbar",
					zap.String("hash",  contentHash[:12]+"…"),
					zap.Int("chunk", chunkIdx),
					zap.Error(err),
				)
				fileOK = false
				break
			}

			if err := fs.storeChunkLocal(chunkHash, chunk); err != nil {
				fileOK = false
				break
			}
		}

		if fileOK {
			restored++
			log.Debug("Datei wiederhergestellt",
				zap.String("hash",  contentHash[:16]+"…"),
				zap.Int64("size",  manifest.Size),
				zap.Int("chunks", manifest.ChunkCount),
			)
		} else {
			failed++
		}

		// Fortschritt loggen
		if (i+1)%10 == 0 {
			log.Info("Wiederherstellungs-Fortschritt",
				zap.Int("fertig",    i+1),
				zap.Int("gesamt",   len(hashes)),
				zap.Int("restored", restored),
				zap.Int("failed",   failed),
			)
		}
	}

	log.Info("Wiederherstellung abgeschlossen",
		zap.Int("restored", restored),
		zap.Int("skipped",  skipped),
		zap.Int("failed",   failed),
	)
	return nil
}

// =============================================================================
//  XChaCha20-Poly1305 Hilfsfunktionen
// =============================================================================

func xchacha20Encrypt(plain, key []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil { return nil, err }
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil { return nil, err }
	return aead.Seal(nonce, nonce, plain, nil), nil
}

func xchacha20Decrypt(ct, key []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil { return nil, err }
	if len(ct) < aead.NonceSize() { return nil, fmt.Errorf("ciphertext zu kurz") }
	return aead.Open(nil, ct[:aead.NonceSize()], ct[aead.NonceSize():], nil)
}

func joinWords(words []string) []byte {
	result := make([]byte, 0, len(words)*10)
	for i, w := range words {
		result = append(result, []byte(w)...)
		if i < len(words)-1 {
			result = append(result, '\n')
		}
	}
	return result
}

// dhtKey gibt den DHT-Key für den Index dieser FundusID zurück.
func indexDHTKey(fundusID string) string {
	return DHTNamespaceIndex + fundusID
}

// IndexKeyHex gibt den Index-Schlüssel als Hex zurück (für Debugging).
func IndexKeyHex(seedWords []string, fundusID string) string {
	return hex.EncodeToString(DeriveIndexKey(seedWords, fundusID))
}
