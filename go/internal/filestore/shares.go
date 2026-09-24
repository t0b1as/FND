// Package filestore – shares.go
//
// Verzeichnis-Freigaben (Directory Shares)
//
// Unterschied zu regulären Uploads:
//   Uploads:  Dateien werden in Chunks zerlegt, 5× im DHT repliziert,
//             content-addressed. Der Uploader ist anonym.
//
//   Shares:   Verzeichnisse bleiben auf dem lokalen Dateisystem des Pi.
//             Keine Redundanz-Replikation (der Pi ist der einzige Holder).
//             Zugriff via libp2p-Stream direkt vom Pi.
//             Berechtigung per Ed25519-Identität geprüft.
//
// Berechtigungsmodell:
//   ReadAccess:  public | contacts | specific:[peer_ids] | none
//   WriteAccess: public | contacts | specific:[peer_ids] | none
//
// Mehrere Verzeichnisse können zu einer Share zusammengefasst werden.
// Jedes Verzeichnis erscheint als eigener Ordner im virtuellen Share-Tree.
//
// Sicherheit:
//   - Jeder Zugriff prüft die Ed25519-Signatur des anfragenden Peers
//   - Path-Traversal-Angriffe werden durch Sandbox-Prüfung verhindert
//   - WriteAccess beschränkt sich auf Uploads in Upload-Ordner (nicht überschreiben)
//
// DHT:
//   Share-Metadaten werden unter /fundus/shares/<owner_peer_id> veröffentlicht
//   damit Berechtigte die Share entdecken können.

package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"lukechampine.com/blake3"
)

// =============================================================================
//  Berechtigungsmodell
// =============================================================================

// AccessMode beschreibt wer Zugriff hat.
type AccessMode string

const (
	AccessPublic   AccessMode = "public"   // jeder Peer (anonym erlaubt)
	AccessContacts AccessMode = "contacts" // alle Peer-IDs in der Kontaktliste
	AccessSpecific AccessMode = "specific" // nur explizit genannte Peer-IDs
	AccessNone     AccessMode = "none"     // kein Zugriff
)

// AccessRule definiert Zugriff für eine Aktion (lesen oder schreiben).
type AccessRule struct {
	Mode    AccessMode `json:"mode"`
	PeerIDs []string   `json:"peer_ids,omitempty"` // nur relevant bei "specific"
}

// Allows prüft ob ein Peer Zugriff hat.
// contacts: die aktuelle Kontaktliste des Owners (wird von ShareManager befüllt).
func (r AccessRule) Allows(peerID string, contacts []string) bool {
	switch r.Mode {
	case AccessPublic:
		return true
	case AccessNone:
		return false
	case AccessContacts:
		for _, c := range contacts {
			if c == peerID {
				return true
			}
		}
		return false
	case AccessSpecific:
		for _, p := range r.PeerIDs {
			if p == peerID {
				return true
			}
		}
		return false
	}
	return false
}

// =============================================================================
//  Share-Struktur
// =============================================================================

// MappedDir ist ein lokales Verzeichnis das Teil einer Share ist.
type MappedDir struct {
	// Name des virtuellen Ordners (wie er dem Empfänger angezeigt wird).
	// Nur relevant wenn FlatListing=false.
	VirtualName string `json:"virtual_name"`
	// Absoluter lokaler Pfad auf dem Pi
	LocalPath string `json:"local_path"`
	// Nur-Lese-Override für diesen Ordner (überschreibt Share.WriteAccess)
	ReadOnly bool `json:"read_only,omitempty"`
	// FlatListing: Inhalt dieses Verzeichnisses direkt in der Share-Root anzeigen,
	// ohne übergeordneten Ordner. Mehrere Verzeichnisse mit FlatListing=true
	// werden parallel nebeneinander gelistet.
	// Namenskollisionen werden durch Suffix _(2), _(3) etc. aufgelöst.
	FlatListing bool `json:"flat_listing,omitempty"`
}

// Share repräsentiert eine Verzeichnis-Freigabe.
// Mehrere lokale Verzeichnisse können in einer Share zusammengefasst werden.
type Share struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`         // Anzeigename
	Description string      `json:"description,omitempty"`
	Dirs        []MappedDir `json:"dirs"`         // gemappte Verzeichnisse

	ReadAccess  AccessRule `json:"read_access"`
	WriteAccess AccessRule `json:"write_access"`

	OwnerPeerID string    `json:"owner_peer_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Gecachte Statistiken (asynchron aktualisiert)
	TotalSizeBytes int64 `json:"total_size_bytes"`
	TotalFiles     int   `json:"total_files"`
}

// ShareID generiert eine deterministische ID aus Owner + Name.
func ShareID(ownerPeerID, name string) string {
	h := blake3.New(32, nil)
	h.Write([]byte(ownerPeerID))
	h.Write([]byte(name))
	return fmt.Sprintf("%x", h.Sum(nil)[:8])
}

// =============================================================================
//  Virtueller Verzeichnis-Eintrag
// =============================================================================

// DirEntry ist ein Eintrag im virtuellen Verzeichnis einer Share.
type DirEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"mod_time"`
	// Pfad relativ zum Share-Root (für Download-Anfragen)
	VirtualPath string `json:"virtual_path"`
}

// =============================================================================
//  ShareManager
// =============================================================================

// ShareManager verwaltet alle Verzeichnis-Freigaben eines Nodes.
type ShareManager struct {
	mu      sync.RWMutex
	shares  map[string]*Share // shareID → Share
	dataDir string            // Pfad für Share-Metadaten-Persistenz
	peerID  string            // eigene Peer-ID (Owner)
	p2p     P2PAdapter
	// Callback um Kontaktliste zu holen (aus Messenger/Partner)
	GetContacts func() []string
	log         *zap.Logger
	publishNow  chan struct{} // Signal für sofortiges DHT-Publish (neue Freigabe)

	// Discovery-Cache: der Node pollt selbst im Hintergrund und cacht die
	// entdeckten Netz-Freigaben. Das Frontend liest nur diesen Cache (billig),
	// statt selbst zu pollen — entlastet Frontend UND Node (ein zentraler Scan
	// statt einer pro Browser-Tab).
	discMu    sync.RWMutex
	discovered []byte    // gecachtes JSON der entdeckten Netz-Freigaben
	discAt     time.Time // Zeitpunkt der letzten Discovery
}

const (
	DHTSharesKey       = "/fundus/shares/"     // + owner_peer_id
	SharesProtocol     = "/fundus/shares-query/1.0.0" // direkte Peer-Abfrage
	ShareMetaFile      = "shares.json"
	MaxShareUploadSize = 512 << 20 // 512 MB pro Upload in eine Share
)

// NewShareManager erstellt einen neuen ShareManager.
func NewShareManager(dataDir, peerID string, p2p P2PAdapter, log *zap.Logger) (*ShareManager, error) {
	sm := &ShareManager{
		shares:  make(map[string]*Share),
		dataDir: dataDir,
		peerID:  peerID,
		p2p:     p2p,
		log:     log,
		GetContacts: func() []string { return nil }, // Default: leere Kontaktliste
		publishNow:  make(chan struct{}, 1),
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("share datadir: %w", err)
	}
	sm.load()
	return sm, nil
}

// Run startet Hintergrundaufgaben.
// publicSharesJSON liefert die öffentlichen Freigabe-Metadaten als JSON (ohne
// lokale Pfade). Für DHT-Publish UND direkte Peer-Abfrage.
func (sm *ShareManager) publicSharesJSON() []byte {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	type pubShare struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Files int    `json:"files"`
		Size  int64  `json:"size_bytes"`
	}
	var pub []pubShare
	for _, s := range sm.shares {
		if s.ReadAccess.Mode != AccessNone {
			pub = append(pub, pubShare{ID: s.ID, Name: s.Name, Files: s.TotalFiles, Size: s.TotalSizeBytes})
		}
	}
	data, _ := json.Marshal(pub)
	return data
}

func (sm *ShareManager) Run(ctx context.Context) {
	// Direktes Abfrage-Protokoll registrieren: Peers können unsere öffentlichen
	// Freigaben direkt erfragen (zuverlässiger als DHT im kleinen LAN-Netz).
	if sm.p2p != nil {
		sm.p2p.RegisterProtocol(SharesProtocol, func(peerID string, req []byte) []byte {
			// Request-Typen: leer/"q" = Share-Liste; JSON {type,share,path} = ls/dl.
			if len(req) == 0 || string(req) == "q" {
				return sm.publicSharesJSON()
			}
			var r struct {
				Type  string `json:"type"`  // "ls" | "dl"
				Share string `json:"share"` // Share-ID
				Path  string `json:"path"`  // Unterpfad
			}
			if json.Unmarshal(req, &r) != nil {
				return sm.publicSharesJSON()
			}
			switch r.Type {
			case "ls":
				entries, err := sm.ListDir(r.Share, peerID, r.Path)
				if err != nil {
					out, _ := json.Marshal(map[string]string{"error": err.Error()})
					return out
				}
				out, _ := json.Marshal(map[string]interface{}{"entries": entries})
				return out
			case "dl":
				// Datei lesen und zurückgeben (begrenzt auf sinnvolle Größe für
				// den P2P-Stream; große Dateien blockweise wäre der nächste Schritt).
				share, ok := sm.Get(r.Share)
				if !ok {
					return []byte(`{"error":"share nicht gefunden"}`)
				}
				fp, err := sm.resolveVirtualPath(share, r.Path)
				if err != nil {
					return []byte(`{"error":"pfad ungültig"}`)
				}
				data, err := osReadFileLimited(fp, 64*1024*1024)
				if err != nil {
					return []byte(`{"error":"lesen fehlgeschlagen"}`)
				}
				return data // rohe Dateidaten
			}
			return sm.publicSharesJSON()
		})
	}
	// Shares periodisch in DHT veröffentlichen
	go sm.runDHTPublish(ctx)
	// Statistiken asynchron aktualisieren
	go sm.runStatUpdater(ctx)
	// Netz-Freigaben im Hintergrund entdecken und cachen (entlastet Frontend)
	go sm.runDiscovery(ctx)
}

// runDiscovery pollt im Hintergrund alle 10s die verbundenen Peers (auch über
// den P2P-Tunnel, da Tunnel-Nodes normale libp2p-Peers sind) nach ihren
// öffentlichen Freigaben und cacht das Ergebnis. So muss das Frontend nicht
// selbst pollen — es liest nur den fertigen Cache (ein zentraler Scan statt
// einer pro Browser-Tab). Nicht antwortende Peers fallen automatisch weg.
func (sm *ShareManager) runDiscovery(ctx context.Context) {
	if sm.p2p == nil {
		return
	}
	scan := func() {
		type netShare struct {
			PeerID string          `json:"peer_id"`
			Shares json.RawMessage `json:"shares"`
		}
		out := []netShare{}
		myID := sm.p2p.ID().String()
		for _, p := range sm.p2p.Peers() {
			pid := p.String()
			if pid == myID {
				continue
			}
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			data, err := sm.p2p.SendAndReceive(sctx, pid, SharesProtocol, []byte("q"))
			cancel()
			if err != nil || len(data) == 0 || string(data) == "null" {
				continue
			}
			out = append(out, netShare{PeerID: pid, Shares: json.RawMessage(data)})
		}
		j, _ := json.Marshal(out)
		sm.discMu.Lock()
		sm.discovered = j
		sm.discAt = time.Now()
		sm.discMu.Unlock()
	}
	scan() // sofort einmal
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan()
		}
	}
}

// DiscoveredShares liefert den gecachten Discovery-Stand (JSON). Billiger Read
// fürs Frontend statt eines teuren Live-Scans pro Anfrage.
func (sm *ShareManager) DiscoveredShares() []byte {
	sm.discMu.RLock()
	defer sm.discMu.RUnlock()
	if sm.discovered == nil {
		return []byte("[]")
	}
	return sm.discovered
}

// =============================================================================
//  Share-Verwaltung (CRUD)
// =============================================================================

// Create erstellt eine neue Share.
func (sm *ShareManager) Create(name, description string, dirs []MappedDir, read, write AccessRule) (*Share, error) {
	if name == "" {
		return nil, fmt.Errorf("name darf nicht leer sein")
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("mindestens ein Verzeichnis erforderlich")
	}

	// Verzeichnisse validieren
	for _, d := range dirs {
		if d.VirtualName == "" || d.LocalPath == "" {
			return nil, fmt.Errorf("jedes Verzeichnis braucht VirtualName und LocalPath")
		}
		if strings.Contains(d.VirtualName, "..") || strings.Contains(d.VirtualName, "/") {
			return nil, fmt.Errorf("ungültiger VirtualName: %q", d.VirtualName)
		}
		linfo, err := os.Lstat(d.LocalPath)
		if err != nil {
			return nil, fmt.Errorf("verzeichnis %q nicht gefunden: %w", d.LocalPath, err)
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("verzeichnis %q ist ein symlink – nicht erlaubt", d.LocalPath)
		}
		if !linfo.IsDir() {
			return nil, fmt.Errorf("%q ist kein Verzeichnis", d.LocalPath)
		}
	}

	share := &Share{
		ID:          ShareID(sm.peerID, name),
		Name:        name,
		Description: description,
		Dirs:        dirs,
		ReadAccess:  read,
		WriteAccess: write,
		OwnerPeerID: sm.peerID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	sm.mu.Lock()
	sm.shares[share.ID] = share
	sm.mu.Unlock()

	sm.save()
	sm.log.Info("Share erstellt",
		zap.String("id",   share.ID),
		zap.String("name", share.Name),
		zap.Int("dirs",    len(dirs)),
		zap.String("read",  string(read.Mode)),
		zap.String("write", string(write.Mode)),
	)
	return share, nil
}

// Update aktualisiert eine bestehende Share.
func (sm *ShareManager) Update(shareID string, name, description string, dirs []MappedDir, read, write AccessRule) (*Share, error) {
	sm.mu.Lock()
	share, ok := sm.shares[shareID]
	if !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("share %s nicht gefunden", shareID)
	}
	if name != "" {
		share.Name = name
	}
	share.Description = description
	if len(dirs) > 0 {
		share.Dirs = dirs
	}
	share.ReadAccess  = read
	share.WriteAccess = write
	share.UpdatedAt   = time.Now()
	sm.mu.Unlock()

	sm.save()
	return share, nil
}

// Delete löscht eine Share (lokale Dateien bleiben erhalten).
func (sm *ShareManager) Delete(shareID string) error {
	sm.mu.Lock()
	_, ok := sm.shares[shareID]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("share %s nicht gefunden", shareID)
	}
	delete(sm.shares, shareID)
	sm.mu.Unlock()

	sm.save()
	sm.log.Info("Share gelöscht", zap.String("id", shareID))
	return nil
}

// Get gibt eine Share zurück.
func (sm *ShareManager) Get(shareID string) (*Share, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.shares[shareID]
	return s, ok
}

// List gibt alle eigenen Shares zurück.
func (sm *ShareManager) List() []*Share {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make([]*Share, 0, len(sm.shares))
	for _, s := range sm.shares {
		out = append(out, s)
	}
	return out
}

// =============================================================================
//  Verzeichnis-Traversal
// =============================================================================

// ListDir listet den Inhalt eines virtuellen Pfads innerhalb einer Share.
// virtualPath: "" = Share-Root (zeigt alle MappedDirs als Ordner)
//              "Fotos" = Inhalt des gemappten Ordners "Fotos"
//              "Fotos/Urlaub" = Unterordner
func (sm *ShareManager) ListDir(shareID, requesterPeerID, virtualPath string) ([]DirEntry, error) {
	share, ok := sm.Get(shareID)
	if !ok {
		return nil, fmt.Errorf("share nicht gefunden")
	}

	contacts := sm.GetContacts()
	if !share.ReadAccess.Allows(requesterPeerID, contacts) {
		return nil, fmt.Errorf("zugriff verweigert")
	}

	// Navigation in einen Unterordner bei flat_listing: Der Pfad ist relativ zum
	// flat-LocalPath (kein VirtualName-Ordner davor). Prüfen, ob ein flat-Dir
	// existiert und der Pfad darin als Verzeichnis aufgeht.
	if virtualPath != "" && virtualPath != "/" {
		for _, d := range share.Dirs {
			if !d.FlatListing {
				continue
			}
			cleanC := filepath.Clean(filepath.Join(d.LocalPath, filepath.Clean("/"+virtualPath)))
			base := filepath.Clean(d.LocalPath)
			if cleanC != base && !strings.HasPrefix(cleanC, base+string(os.PathSeparator)) {
				continue // Traversal-Schutz
			}
			if info, err := os.Stat(cleanC); err == nil && info.IsDir() {
				subEntries, err := sm.readDir(cleanC, "")
				if err != nil {
					return nil, err
				}
				out := make([]DirEntry, 0, len(subEntries))
				vp := strings.TrimPrefix(virtualPath, "/")
				for _, e := range subEntries {
					e.VirtualPath = vp + "/" + e.Name
					out = append(out, e)
				}
				return out, nil
			}
		}
	}

	if virtualPath == "" || virtualPath == "/" {
		var entries []DirEntry
		seen := make(map[string]int) // Namenskollisions-Tracking

		for _, d := range share.Dirs {
			if d.FlatListing {
				// Flat: Inhalt direkt listen, ohne übergeordneten Ordner
				subEntries, err := sm.readDir(d.LocalPath, "")
				if err != nil {
					continue
				}
				for _, e := range subEntries {
					// Namenskollision auflösen
					name := e.Name
					if n := seen[name]; n > 0 {
						ext := filepath.Ext(name)
						base := strings.TrimSuffix(name, ext)
						name = fmt.Sprintf("%s_(%d)%s", base, n+1, ext)
					}
					seen[e.Name]++
					e.Name = name
					// VirtualPath: direkt im "Root" ohne Ordner-Prefix
					e.VirtualPath = "\x00flat\x00" + d.LocalPath + "/" + e.Name
					entries = append(entries, e)
				}
			} else {
				// Normal: Ordner anzeigen
				info, err := os.Stat(d.LocalPath)
				if err != nil {
					continue
				}
				name := d.VirtualName
				if n := seen[name]; n > 0 {
					name = fmt.Sprintf("%s_(%d)", name, n+1)
				}
				seen[d.VirtualName]++
				entries = append(entries, DirEntry{
					Name:        name,
					IsDir:       true,
					ModTime:     info.ModTime(),
					VirtualPath: d.VirtualName,
				})
			}
		}
		return entries, nil
	}

	// Unterordner: MappedDir bestimmen + relativen Pfad auflösen
	localPath, err := sm.resolveVirtualPath(share, virtualPath)
	if err != nil {
		return nil, err
	}

	return sm.readDir(localPath, virtualPath)
}

// resolveVirtualPath löst einen virtuellen Pfad in einen lokalen Pfad auf.
// Verhindert Path-Traversal.
// Flat-Pfad-Marker (internes Format: "\x00flat\x00<localDir>/<filename>")
func isFlatPath(vpath string) (localDir, filename string, ok bool) {
	if !strings.HasPrefix(vpath, "\x00flat\x00") {
		return "", "", false
	}
	rest := strings.TrimPrefix(vpath, "\x00flat\x00")
	slash := strings.LastIndex(rest, "/")
	if slash < 0 {
		return rest, "", true
	}
	return rest[:slash], rest[slash+1:], true
}

func (sm *ShareManager) resolveVirtualPath(share *Share, virtualPath string) (string, error) {
	// Flat-Pfad direkt auflösen
	if localDir, filename, ok := isFlatPath(virtualPath); ok {
		// Sicherstellen dass localDir zu einer MappedDir gehört (Anti-Spoofing)
		for _, d := range share.Dirs {
			if d.FlatListing && filepath.Clean(d.LocalPath) == filepath.Clean(localDir) {
				target := filepath.Join(localDir, filename)
				clean  := filepath.Clean(target)
				base   := filepath.Clean(localDir)
				// Separator-sichere Prüfung: clean muss == base oder base+sep+... sein
				if clean != base && !strings.HasPrefix(clean, base+string(os.PathSeparator)) {
					return "", fmt.Errorf("path traversal in flat path")
				}
				return clean, nil
			}
		}
		return "", fmt.Errorf("flat path nicht in share")
	}

	// Normaler Pfad, aber die Freigabe nutzt flat_listing: Der Pfad ist relativ
	// zum flat-LocalPath (kein VirtualName-Ordner davor). Das ist der Fall beim
	// Download aus einer flat freigegebenen Platte (z.B. "datei.nzb" oder
	// "unterordner/datei.nzb").
	vpClean := filepath.Clean(strings.TrimPrefix(virtualPath, "/"))
	for _, d := range share.Dirs {
		if !d.FlatListing {
			continue
		}
		candidate := filepath.Clean(filepath.Join(d.LocalPath, vpClean))
		base := filepath.Clean(d.LocalPath)
		if candidate != base && !strings.HasPrefix(candidate, base+string(os.PathSeparator)) {
			continue // Traversal-Schutz
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil // existiert innerhalb des flat-Dirs
		}
	}

	virtualPath = filepath.Clean(strings.TrimPrefix(virtualPath, "/"))
	parts := strings.SplitN(virtualPath, string(os.PathSeparator), 2)
	topDir := parts[0]

	// MappedDir finden
	var mappedDir *MappedDir
	for i := range share.Dirs {
		if share.Dirs[i].VirtualName == topDir {
			mappedDir = &share.Dirs[i]
			break
		}
	}
	if mappedDir == nil {
		return "", fmt.Errorf("verzeichnis %q nicht in share", topDir)
	}

	localPath := mappedDir.LocalPath
	if len(parts) > 1 {
		// Unterordner anhängen und auf Traversal prüfen
		localPath = filepath.Join(localPath, parts[1])
		clean := filepath.Clean(localPath)
		base  := filepath.Clean(mappedDir.LocalPath)
		if clean != base && !strings.HasPrefix(clean, base+string(os.PathSeparator)) {
			return "", fmt.Errorf("path traversal versucht")
		}
		localPath = clean
	}
	return localPath, nil
}

func (sm *ShareManager) readDir(localPath, virtualPrefix string) ([]DirEntry, error) {
	entries, err := os.ReadDir(localPath)
	if err != nil {
		return nil, fmt.Errorf("verzeichnis lesen: %w", err)
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		// Fundus-interne Ordner/Dateien ausblenden. So kann ein Laufwerk gleichzeitig
		// Chunks hosten UND direkt freigegeben werden (hybrid), ohne dass die
		// Chunk-Ablage oder Metadaten im Share auftauchen.
		if isFundusInternal(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		vpath := filepath.Join(virtualPrefix, e.Name())
		de := DirEntry{
			Name:        e.Name(),
			IsDir:       e.IsDir(),
			ModTime:     info.ModTime(),
			VirtualPath: vpath,
		}
		if !e.IsDir() {
			de.Size = info.Size()
		}
		out = append(out, de)
	}
	return out, nil
}

// isFundusInternal erkennt Fundus-interne Einträge, die in Direktfreigaben nicht
// sichtbar sein sollen (Chunk-Ablage, Metadaten, Schreibtests).
func isFundusInternal(name string) bool {
	switch name {
	case "chunks", "volumes.json", ".fundus-write-test", "fundus.env", ".fundus":
		return true
	}
	return false
}

// =============================================================================
//  Download & Upload
// =============================================================================

// ReadFile liest eine Datei aus einer Share und schreibt sie in w.
func (sm *ShareManager) ReadFile(shareID, requesterPeerID, virtualPath string, w io.Writer) error {
	share, ok := sm.Get(shareID)
	if !ok {
		return fmt.Errorf("share nicht gefunden")
	}
	contacts := sm.GetContacts()
	if !share.ReadAccess.Allows(requesterPeerID, contacts) {
		return fmt.Errorf("lesezugriff verweigert")
	}

	localPath, err := sm.resolveVirtualPath(share, virtualPath)
	if err != nil {
		return err
	}

	// Symlink-Schutz: Lstat folgt keinem Symlink
	linfo, err := os.Lstat(localPath)
	if err != nil {
		return fmt.Errorf("datei nicht gefunden: %w", err)
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlinks sind in shares nicht erlaubt")
	}
	if linfo.IsDir() {
		return fmt.Errorf("%q ist ein verzeichnis, kein download möglich", virtualPath)
	}

	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("datei öffnen: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(w, f)
	return err
}

// WriteFile schreibt eine hochgeladene Datei in eine Share.
// Nur in MappedDirs erlaubt die nicht ReadOnly sind und WriteAccess erlaubt.
func (sm *ShareManager) WriteFile(shareID, requesterPeerID, virtualDir, filename string, r io.Reader, size int64) error {
	share, ok := sm.Get(shareID)
	if !ok {
		return fmt.Errorf("share nicht gefunden")
	}
	contacts := sm.GetContacts()
	if !share.WriteAccess.Allows(requesterPeerID, contacts) {
		return fmt.Errorf("schreibzugriff verweigert")
	}
	if size > MaxShareUploadSize {
		return fmt.Errorf("datei zu groß (max %d MB)", MaxShareUploadSize>>20)
	}

	// Ziel-MappedDir bestimmen
	var targetDir *MappedDir
	for i := range share.Dirs {
		if share.Dirs[i].VirtualName == virtualDir {
			if share.Dirs[i].ReadOnly {
				return fmt.Errorf("verzeichnis %q ist schreibgeschützt", virtualDir)
			}
			targetDir = &share.Dirs[i]
			break
		}
	}
	if targetDir == nil {
		return fmt.Errorf("zielverzeichnis %q nicht gefunden", virtualDir)
	}

	// Dateiname bereinigen
	filename = filepath.Base(filepath.Clean(filename))
	if filename == "." || filename == ".." || strings.HasPrefix(filename, ".") {
		return fmt.Errorf("ungültiger dateiname: %q", filename)
	}

	localPath := filepath.Join(targetDir.LocalPath, filename)

	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0640)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("datei %q existiert bereits", filename)
		}
		return fmt.Errorf("datei erstellen: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, io.LimitReader(r, size+1))
	if err != nil {
		os.Remove(localPath) // Rollback bei Fehler
		return fmt.Errorf("datei schreiben: %w", err)
	}
	if written > size {
		os.Remove(localPath)
		return fmt.Errorf("upload überschreitet angegebene Größe")
	}

	sm.log.Info("Datei in Share hochgeladen",
		zap.String("share",    shareID),
		zap.String("peer",     requesterPeerID[:min(12, len(requesterPeerID))]),
		zap.String("file",     filename),
		zap.Int64("bytes",     written),
	)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// =============================================================================
//  Berechtigungsprüfung (für API)
// =============================================================================

// CheckAccess prüft ob ein Peer eine bestimmte Aktion ausführen darf.
// action: "read" | "write"
func (sm *ShareManager) CheckAccess(shareID, peerID, action string) bool {
	share, ok := sm.Get(shareID)
	if !ok {
		return false
	}
	contacts := sm.GetContacts()
	switch action {
	case "read":
		return share.ReadAccess.Allows(peerID, contacts)
	case "write":
		return share.WriteAccess.Allows(peerID, contacts)
	}
	return false
}

// =============================================================================
//  Persistenz & DHT
// =============================================================================

func (sm *ShareManager) save() {
	sm.mu.RLock()
	data, _ := json.MarshalIndent(sm.shares, "", "  ")
	count := len(sm.shares)
	sm.mu.RUnlock()

	// Verzeichnis sicherstellen (könnte nach einem Redeploy fehlen).
	if err := os.MkdirAll(sm.dataDir, 0700); err != nil {
		sm.log.Error("Share-Verzeichnis anlegen fehlgeschlagen", zap.Error(err))
		return
	}
	path := filepath.Join(sm.dataDir, ShareMetaFile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		sm.log.Error("Share-Metadaten speichern fehlgeschlagen", zap.Error(err), zap.String("path", path))
	} else {
		sm.log.Info("Freigaben gespeichert", zap.Int("count", count), zap.String("path", path))
	}
	// Sofortiges DHT-Publish auslösen (non-blocking), damit neue/geänderte
	// Freigaben schnell im Netz sichtbar werden statt erst beim nächsten Ticker.
	if sm.publishNow != nil {
		select {
		case sm.publishNow <- struct{}{}:
		default:
		}
	}
}

func (sm *ShareManager) load() {
	path := filepath.Join(sm.dataDir, ShareMetaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return // Datei existiert noch nicht = OK
	}
	var shares map[string]*Share
	if json.Unmarshal(data, &shares) == nil {
		sm.mu.Lock()
		sm.shares = shares
		sm.mu.Unlock()
		sm.log.Info("Shares geladen", zap.Int("count", len(shares)))
	}
}

func (sm *ShareManager) runDHTPublish(ctx context.Context) {
	publish := func() {
		sm.mu.RLock()
		// Nur öffentliche Metadaten (keine lokalen Pfade!)
		type PublicShare struct {
			ID          string     `json:"id"`
			Name        string     `json:"name"`
			Description string     `json:"description,omitempty"`
			ReadMode    AccessMode `json:"read_mode"`
			WriteMode   AccessMode `json:"write_mode"`
			Files       int        `json:"files"`
			SizeBytes   int64      `json:"size_bytes"`
			UpdatedAt   time.Time  `json:"updated_at"`
		}
		var pub []PublicShare
		for _, s := range sm.shares {
			// Nur Shares mit mindestens Read-Zugriff veröffentlichen
			if s.ReadAccess.Mode != AccessNone {
				pub = append(pub, PublicShare{
					ID:          s.ID,
					Name:        s.Name,
					Description: s.Description,
					ReadMode:    s.ReadAccess.Mode,
					WriteMode:   s.WriteAccess.Mode,
					Files:       s.TotalFiles,
					SizeBytes:   s.TotalSizeBytes,
					UpdatedAt:   s.UpdatedAt,
				})
			}
		}
		sm.mu.RUnlock()

		if len(pub) == 0 {
			return
		}
		data, _ := json.Marshal(pub)
		sm.p2p.DHTput(ctx, DHTSharesKey+sm.peerID, data)
	}

	publish() // Sofort
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.publishNow:
			publish() // ausgelöst durch neue/geänderte Freigabe
		case <-ticker.C:
			publish()
		}
	}
}

func (sm *ShareManager) runStatUpdater(ctx context.Context) {
	update := func() {
		sm.mu.RLock()
		ids := make([]string, 0, len(sm.shares))
		for id := range sm.shares {
			ids = append(ids, id)
		}
		sm.mu.RUnlock()

		for _, id := range ids {
			sm.mu.RLock()
			share := sm.shares[id]
			sm.mu.RUnlock()
			if share == nil {
				continue
			}

			var totalSize int64
			var totalFiles int
			for _, d := range share.Dirs {
				filepath.WalkDir(d.LocalPath, func(_ string, e fs.DirEntry, err error) error {
					if err != nil || e.IsDir() {
						return nil
					}
					totalFiles++
					if info, err := e.Info(); err == nil {
						totalSize += info.Size()
					}
					return nil
				})
			}

			sm.mu.Lock()
			if s := sm.shares[id]; s != nil {
				s.TotalSizeBytes = totalSize
				s.TotalFiles     = totalFiles
			}
			sm.mu.Unlock()
		}
		sm.save()
	}

	update() // Sofort
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}

// PublicView gibt eine Share ohne lokale Dateipfade zurück (für externe Peers).
func (s *Share) PublicView() map[string]interface{} {
	dirs := make([]map[string]interface{}, len(s.Dirs))
	for i, d := range s.Dirs {
		dirs[i] = map[string]interface{}{
			"virtual_name": d.VirtualName,
			"read_only":    d.ReadOnly,
			// local_path absichtlich ausgelassen
		}
	}
	return map[string]interface{}{
		"id":               s.ID,
		"name":             s.Name,
		"description":      s.Description,
		"dirs":             dirs,
		"read_access":      s.ReadAccess,
		"write_access":     s.WriteAccess,
		"owner_peer_id":    s.OwnerPeerID,
		"total_size_bytes": s.TotalSizeBytes,
		"total_files":      s.TotalFiles,
		"updated_at":       s.UpdatedAt,
	}
}

// FileSize gibt die Größe einer Datei in einer Share zurück (für Content-Length Header).
func (sm *ShareManager) FileSize(shareID, requesterPeerID, virtualPath string) (int64, error) {
	share, ok := sm.Get(shareID)
	if !ok {
		return 0, fmt.Errorf("share nicht gefunden")
	}
	contacts := sm.GetContacts()
	if !share.ReadAccess.Allows(requesterPeerID, contacts) {
		return 0, fmt.Errorf("zugriff verweigert")
	}
	localPath, err := sm.resolveVirtualPath(share, virtualPath)
	if err != nil {
		return 0, err
	}
	info, err := os.Lstat(localPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}


// MirrorShare spiegelt eine Freigabe (oder einen Unterordner davon) rekursiv auf
// einen lokalen Zielpfad. Kopiert alle Dateien mit erhaltener Struktur. Gibt die
// Anzahl kopierter Dateien zurück. Fundus-interne Ordner (chunks/) werden dabei
// übersprungen (via isFundusInternal in readDir-Logik).
func (sm *ShareManager) MirrorShare(shareID, subPath, target string) (int, error) {
	share, ok := sm.Get(shareID)
	if !ok {
		return 0, fmt.Errorf("Freigabe nicht gefunden")
	}
	// Quell-Pfad auflösen (lokaler Pfad der Freigabe + subPath).
	srcRoot, err := sm.resolveVirtualPath(share, subPath)
	if err != nil {
		return 0, fmt.Errorf("Quellpfad: %w", err)
	}
	if target == "" || !strings.HasPrefix(target, "/") || strings.Contains(target, "..") {
		return 0, fmt.Errorf("ungültiger Zielpfad")
	}
	count := 0
	err = filepath.Walk(srcRoot, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return nil // einzelne Fehler überspringen, nicht abbrechen
		}
		// Fundus-interne Ordner überspringen.
		if info.IsDir() && isFundusInternal(info.Name()) {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		// Zielpfad = target + (path relativ zu srcRoot).
		rel, e := filepath.Rel(srcRoot, path)
		if e != nil {
			return nil
		}
		dst := filepath.Join(target, rel)
		if e := os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
			return nil
		}
		if e := copyFileContents(path, dst); e == nil {
			count++
		}
		return nil
	})
	if err != nil {
		return count, err
	}
	return count, nil
}

// copyFileContents kopiert eine Datei (einfach, streaming).
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// osReadFileLimited liest eine Datei bis zu einer Maximalgröße (Schutz gegen
// riesige Dateien im P2P-Stream).
func osReadFileLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}
