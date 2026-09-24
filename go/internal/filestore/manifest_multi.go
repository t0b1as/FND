package filestore

import (
	"encoding/json"
	"fmt"
)

// ─── Mehrstufiges Manifest (Skalierung auf große Dateien, z.B. 128 GiB) ──────
//
// Problem: Das DHT (libp2p/Kademlia) hat ein hartes Limit pro Wert (~64 KiB).
// Eine 128-GiB-Datei hat bei 16-MiB-Chunks 8192 Chunk-Hashes → ein einziges
// Manifest wäre viel zu groß fürs DHT.
//
// Lösung: Übersteigt die Chunk-Hash-Liste eine Schwelle, wird sie selbst in
// "Manifest-Chunks" (Sub-Manifeste) aufgeteilt. Jedes Sub-Manifest ist eine
// JSON-Liste von Daten-Chunk-Hashes und wird wie ein normaler, inhalts-
// adressierter Chunk im Netz gespeichert (selbstverifizierend über seinen Hash).
// Das ROOT-Manifest enthält dann statt der vollen Liste nur die (wenigen)
// Hashes der Sub-Manifeste → es bleibt klein genug fürs DHT.
//
// Diese Datei enthält die REINE, netzunabhängige Logik (Aufteilen/Serialisieren/
// Rekonstruieren). Das Speichern/Laden der Sub-Manifest-Chunks erfolgt in
// filestore.go über die bestehenden Chunk-Mechanismen.

const (
	// Schwelle: passt die Chunk-Hash-Liste (als JSON) hierunter, bleibt sie
	// inline im Root-Manifest. Konservativ unter dem 64-KiB-DHT-Limit, damit
	// auch die übrigen Manifest-Felder Platz haben.
	manifestInlineThreshold = 32 * 1024 // 32 KiB

	// Wie viele Daten-Chunk-Hashes pro Sub-Manifest. Bei 64-hex-Hashes +
	// JSON-Overhead (~70 B/Eintrag) ergibt 400 Einträge ~28 KiB < 32 KiB.
	hashesPerSubManifest = 400
)

// subManifest ist eine serialisierbare Teilliste von Daten-Chunk-Hashes.
type subManifest struct {
	ChunkHashes []string `json:"chunk_hashes"`
}

// encodeSubManifest serialisiert eine Teilliste deterministisch.
func encodeSubManifest(hashes []string) ([]byte, error) {
	return json.Marshal(subManifest{ChunkHashes: hashes})
}

// decodeSubManifest liest eine Teilliste zurück.
func decodeSubManifest(data []byte) ([]string, error) {
	var sm subManifest
	if err := json.Unmarshal(data, &sm); err != nil {
		return nil, fmt.Errorf("submanifest decode: %w", err)
	}
	return sm.ChunkHashes, nil
}

// needsMultiLevel entscheidet, ob die Chunk-Hash-Liste zu groß für ein inline-
// Manifest ist und mehrstufig gespeichert werden muss.
func needsMultiLevel(chunkHashes []string) bool {
	data, err := json.Marshal(chunkHashes)
	if err != nil {
		return true // im Zweifel mehrstufig (sicherer als ein zu großes Manifest)
	}
	return len(data) > manifestInlineThreshold
}

// splitIntoSubManifests teilt die vollständige Daten-Chunk-Hash-Liste in
// Sub-Manifest-Blöcke fester Größe. Gibt die serialisierten Sub-Manifeste
// zurück (in Reihenfolge) — der Aufrufer speichert sie als Chunks und sammelt
// ihre Hashes für das Root-Manifest.
func splitIntoSubManifests(chunkHashes []string) ([][]byte, error) {
	if len(chunkHashes) == 0 {
		return nil, nil
	}
	var blocks [][]byte
	for i := 0; i < len(chunkHashes); i += hashesPerSubManifest {
		end := i + hashesPerSubManifest
		if end > len(chunkHashes) {
			end = len(chunkHashes)
		}
		data, err := encodeSubManifest(chunkHashes[i:end])
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, data)
	}
	return blocks, nil
}

// reassembleFromSubManifests fügt die vollständige Daten-Chunk-Hash-Liste aus
// den (in Reihenfolge geladenen) Sub-Manifest-Bytes wieder zusammen.
func reassembleFromSubManifests(subManifestDatas [][]byte) ([]string, error) {
	var all []string
	for idx, data := range subManifestDatas {
		hashes, err := decodeSubManifest(data)
		if err != nil {
			return nil, fmt.Errorf("submanifest %d: %w", idx, err)
		}
		all = append(all, hashes...)
	}
	return all, nil
}
