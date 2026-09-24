package api

// Hilfen für kleine, absturzsichere JSON-Zustandsdateien (Orderbuch, Swaps).
//
// atomicWriteJSON schreibt erst in eine temporäre Datei, synct sie auf die
// Platte und benennt sie dann um. Ein Stromausfall/Absturz während des
// Schreibens hinterlässt so nie eine halbe Datei – es gilt entweder der alte
// oder der neue Stand. Die Dateien liegen im DataDir und werden von Updates
// (ZIP-Entpacken) nicht berührt.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func atomicWriteJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteBytes(path, data)
}

func atomicWriteBytes(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
