package filestore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"lukechampine.com/blake3"
)

// ManifestEntry ist ein Eintrag in einem Verzeichnis-Manifest: eine Datei mit
// ihrem relativen Pfad und dem Content-Hash, unter dem sie (chunked, verteilt)
// im Netz liegt.
type ManifestEntry struct {
	Path string `json:"path"` // relativer Pfad im Ordner, z.B. "urlaub/strand/bild.jpg"
	Hash string `json:"hash"` // Content-Hash der Datei (einzeln hochgeladen)
	Size int64  `json:"size"`
	Mime string `json:"mime,omitempty"`
}

// DirManifest beschreibt einen hochgeladenen Ordner mit erhaltener Struktur.
// Das Manifest selbst wird wie eine Datei ins Netz gestellt (eigener Hash);
// wer den Manifest-Hash hat, kann den ganzen Baum rekonstruieren und jede Datei
// einzeln über ihren Hash abrufen. Muster wie bei IPFS-Verzeichnissen.
type DirManifest struct {
	Type    string          `json:"type"`     // immer "fundus-dir-manifest"
	Name    string          `json:"name"`     // Name des Wurzelordners
	Entries []ManifestEntry `json:"entries"`  // alle Dateien mit relativem Pfad
	Total   int64           `json:"total"`    // Gesamtgröße aller Dateien
	Count   int             `json:"count"`    // Anzahl Dateien
}

const manifestType = "fundus-dir-manifest"

// NewDirManifest baut ein Manifest aus einer Liste von Einträgen. Sortiert die
// Einträge nach Pfad (deterministisch → gleicher Ordner ergibt gleichen Hash).
func NewDirManifest(name string, entries []ManifestEntry) *DirManifest {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	var total int64
	for _, e := range entries {
		total += e.Size
	}
	return &DirManifest{
		Type:    manifestType,
		Name:    name,
		Entries: entries,
		Total:   total,
		Count:   len(entries),
	}
}

// JSON serialisiert das Manifest deterministisch (sortierte Einträge).
func (m *DirManifest) JSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// ManifestHash berechnet den Hash des serialisierten Manifests (Vorschau, ohne
// Upload). Nützlich, um vorab die Manifest-ID zu kennen.
func (m *DirManifest) ManifestHash() (string, error) {
	data, err := m.JSON()
	if err != nil {
		return "", err
	}
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ParseDirManifest liest ein Manifest aus seinen Bytes und prüft den Typ.
func ParseDirManifest(data []byte) (*DirManifest, error) {
	var m DirManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: ungültiges JSON: %w", err)
	}
	if m.Type != manifestType {
		return nil, fmt.Errorf("manifest: falscher Typ %q (erwartet %q)", m.Type, manifestType)
	}
	return &m, nil
}

// IsManifest prüft schnell, ob Bytes ein Fundus-Verzeichnis-Manifest sein könnten
// (ohne vollständiges Parsen). Für die Erkennung beim Abruf.
func IsManifest(data []byte) bool {
	if len(data) > 1<<20 { // Manifeste sind klein; > 1 MB ist sicher keins
		return false
	}
	return strings.Contains(string(data[:min(len(data), 200)]), manifestType)
}

// UploadManifest lädt ein Verzeichnis-Manifest als Datei in den FileStore hoch
// (mit Redundanz, wie jede andere Datei). Gibt den Manifest-Hash zurück, über
// den der ganze Ordner später abrufbar ist.
func (fs *FileStore) UploadManifest(ctx context.Context, m *DirManifest, replicas int) (string, error) {
	data, err := m.JSON()
	if err != nil {
		return "", err
	}
	// Manifest als .fundir-Datei hochladen, damit es beim Abruf erkennbar ist.
	name := m.Name + ".fundir"
	return fs.UploadWithRedundancy(ctx, strings.NewReader(string(data)),
		"application/x-fundus-dir", name, replicas)
}

// CreateDirManifest baut aus bereits hochgeladenen Dateien (Pfad+Hash+Size) ein
// Manifest, lädt es hoch und trägt den Ordner in den LocalIndex ein. Gibt den
// Manifest-Hash zurück.
func (fs *FileStore) CreateDirManifest(ctx context.Context, name string, entries []ManifestEntry, replicas int) (string, error) {
	if len(entries) == 0 {
		return "", fmt.Errorf("manifest: keine Dateien")
	}
	m := NewDirManifest(name, entries)
	hash, err := fs.UploadManifest(ctx, m, replicas)
	if err != nil {
		return "", err
	}
	// Ordner in den LocalIndex (als Ordner-Eintrag mit Manifest-Hash).
	if fs.localIdx != nil {
		fs.localIdx.AddDir(hash, name, m.Total, m.Count)
	}
	return hash, nil
}

// GetManifest lädt ein Manifest über seinen Hash und parst es. Für die
// Baum-Navigation (Ordnerinhalt anzeigen).
func (fs *FileStore) GetManifest(ctx context.Context, hash string) (*DirManifest, error) {
	var buf strings.Builder
	if err := fs.Download(ctx, hash, &buf); err != nil {
		return nil, fmt.Errorf("manifest: Abruf fehlgeschlagen: %w", err)
	}
	return ParseDirManifest([]byte(buf.String()))
}
