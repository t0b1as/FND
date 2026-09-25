package storage

import (
	"strings"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	datastore "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
	leveldb "github.com/ipfs/go-ds-leveldb"
	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
)

// RecordType klassifiziert den Inhalt eines Eintrags.
type RecordType string

const (
	RecordListing        RecordType = "listing"          // Ware oder Dienstleistung
	RecordCertificate    RecordType = "certificate"       // Zertifikat oder Token
	RecordEnergy         RecordType = "energy"            // Energie-Token (Smartmeter)
	RecordJob            RecordType = "job"               // Job-Inserat
	RecordPartnerProfile RecordType = "partner_profile"
	RecordPartnerSalt    RecordType = "partner_salt"
	RecordPartnerAd      RecordType = "partner_ad"
	RecordPartnerSearchAd RecordType = "partner_search_ad" // opt-in Suchindex (Klartext-Kategorien)
	RecordContacts       RecordType = "contacts"          // verschlüsselte Kontaktliste (an FundusID gebunden)
	RecordAddressBook    RecordType = "address_book"      // lokales Wallet-Adressbuch (NICHT im Netz geteilt)
	RecordEmailDir       RecordType = "email_dir"         // opt-in Verzeichnis: email → FundusID (signiert)
	RecordMailbox        RecordType = "mailbox"           // Offline-Nachrichten für eine FundusID (verschlüsselt, bis Abholung)
	RecordKeyDir         RecordType = "key_dir"           // FundusID → X25519-PubKey (für Verschlüsselung an beliebige Adressen)
	RecordOutbox         RecordType = "outbox"            // ausgehende Nachrichten die auf den Empfänger-PubKey warten
	RecordWalletLink     RecordType = "wallet_link"       // hinterlegte Wallet je Login (verschlüsselt, NUR lokal)
)

// Record ist der generische Datencontainer im Fundus-Netz.
type Record struct {
	ID        string         `json:"id"`
	Type      RecordType     `json:"type"`
	OwnerID   string         `json:"owner_id"`            // Peer-ID des Erstellers
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at,omitempty"` // Tombstone: gesetzt = gelöscht
	Data      map[string]any `json:"data"`                // typ-spezifische Nutzlast
	Signature []byte         `json:"signature"`           // krypto. Signatur des Owners
}

// SigningBytes erzeugt die kanonische Byte-Repraesentation eines Records die
// signiert/verifiziert wird. Die Signatur selbst ist NICHT enthalten (sonst
// zirkulaer). Aenderungen an ID, Type, OwnerID, Daten oder Loesch-Status
// invalidieren die Signatur. Stabil sortiert fuer deterministische Bytes.
func (r *Record) SigningBytes() []byte {
	payload := map[string]any{
		"id":       r.ID,
		"type":     string(r.Type),
		"owner_id": r.OwnerID,
		"data":     r.Data,
	}
	if r.DeletedAt != nil {
		payload["deleted"] = r.DeletedAt.UTC().Format(time.RFC3339Nano)
	}
	// json.Marshal sortiert map-Keys deterministisch → stabile Bytes
	b, _ := json.Marshal(payload)
	return b
}

// EnergyToken enthält die Messdaten eines Smart Meters.
// Wird als Record.Data eingebettet.
type EnergyToken struct {
	Timestamp time.Time `json:"timestamp"`
	MeterID   string    `json:"meter_id"`
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	KWh       float64   `json:"kwh"`
	// Netzgebühr: wird beim Settlement berechnet aus Distanz zwischen
	// Erzeuger-Koordinaten und Verbraucher-Koordinaten
	GeneratorLat float64 `json:"generator_lat"`
	GeneratorLon float64 `json:"generator_lon"`
}

// Marshal serialisiert den Record als JSON.
func (r *Record) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

// Store verwaltet lokale Daten und Peer-Replikate.
type Store struct {
	db   datastore.Batching
	log  *zap.Logger

	mu                 sync.RWMutex
	replicationTargets []string // Peer-IDs für die wir Daten cachen
	peerData           map[string][]string // peerID → Record-IDs

	// Kurzlebiger Cache für Stats() — entlastet die häufigen /v1/status-Calls
	// beim Seiten-Rendern (sonst 4× DB-Scan pro Seitenaufruf).
	statsCache   map[string]any
	statsCacheAt time.Time
	statsCacheMu sync.Mutex

	// verifySig prueft ob eine Record-Signatur zum OwnerID-Peer passt.
	// Wird vom P2P-Node gesetzt (SetSignatureVerifier). Nil = keine Pruefung
	// (z.B. in Tests). signierte Records von Peers werden nur akzeptiert
	// wenn die Signatur gueltig ist.
	verifySig func(ownerID string, data, signature []byte) bool
}

// SetSignatureVerifier registriert die Funktion zur Signaturpruefung
// eingehender Records (vom P2P-Node bereitgestellt).
func (s *Store) SetSignatureVerifier(fn func(ownerID string, data, signature []byte) bool) {
	s.mu.Lock()
	s.verifySig = fn
	s.mu.Unlock()
}

// checkSignature verifiziert einen eingehenden Record. Gibt true zurueck wenn
// die Signatur gueltig ist ODER (uebergangsweise) wenn kein Verifier gesetzt
// ist bzw. der Record (noch) keine Signatur traegt. So bleiben Altbestaende
// und Test-Setups funktionsfaehig, waehrend signierte Records geschuetzt sind.
func (s *Store) checkSignature(r *Record) bool {
	s.mu.RLock()
	vf := s.verifySig
	s.mu.RUnlock()
	if vf == nil {
		return true // kein Verifier (Test/Uebergang)
	}
	if len(r.Signature) == 0 {
		return true // unsigniert (Altbestand/Uebergang) - durch Owner-Check abgesichert
	}
	return vf(r.OwnerID, r.SigningBytes(), r.Signature)
}

// New öffnet oder erstellt die LevelDB-Datenbank.
func New(dataDir string, log *zap.Logger) (*Store, error) {
	dbPath := filepath.Join(dataDir, "db")
	db, err := leveldb.NewDatastore(dbPath, nil)
	if err != nil {
		return nil, fmt.Errorf("open leveldb at %s: %w", dbPath, err)
	}

	st := &Store{
		db:       db,
		log:      log,
		peerData: make(map[string][]string),
	}
	// Sekundärindizes beim Start (neu) aufbauen — deckt Altbestand ab, der vor
	// Einführung des Index angelegt wurde, und repariert evtl. Drift. Idempotent.
	for rt := range indexedFields {
		if err := st.RebuildIndex(rt); err != nil {
			log.Warn("Index-Aufbau fehlgeschlagen", zap.String("type", string(rt)), zap.Error(err))
		}
	}
	// Invertierten Volltext-Index (Trigramme + Kategorie) aufbauen.
	for rt := range invTextFields {
		if err := st.RebuildInvIndex(rt); err != nil {
			log.Warn("Volltext-Index-Aufbau fehlgeschlagen", zap.String("type", string(rt)), zap.Error(err))
		}
	}
	return st, nil
}

// Put speichert einen Record (eigener oder Replikat).
func (s *Store) Put(r *Record) error {
	if r.ID == "" {
		return fmt.Errorf("record has no ID")
	}
	r.UpdatedAt = time.Now()

	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}

	// Alten Record für die Index-Pflege laden (Wert könnte sich geändert haben).
	prev, _ := s.Get(r.Type, r.ID)

	key := datastoreKey(r.Type, r.ID)
	if err := s.db.Put(context.Background(), key, raw); err != nil {
		return err
	}
	s.updateIndex(r, prev)
	s.updateInvIndex(r, prev)
	return nil
}

// Get liest einen einzelnen Record.
func (s *Store) Get(recordType RecordType, id string) (*Record, error) {
	key := datastoreKey(recordType, id)
	raw, err := s.db.Get(context.Background(), key)
	if err == datastore.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal record: %w", err)
	}
	return &r, nil
}

// List gibt alle Records eines Typs zurück.
func (s *Store) List(recordType RecordType) ([]*Record, error) {
	prefix := fmt.Sprintf("/%s/", string(recordType))
	results, err := s.db.Query(context.Background(), query.Query{
		Prefix: prefix,
	})
	if err != nil {
		return nil, err
	}
	defer results.Close()

	var records []*Record
	for result := range results.Next() {
		if result.Error != nil {
			return nil, result.Error
		}
		var r Record
		if err := json.Unmarshal(result.Value, &r); err != nil {
			s.log.Warn("Corrupt record skipped", zap.String("key", result.Key))
			continue
		}
		// Tombstones (gelöschte Records) nicht zurückgeben
		if r.DeletedAt != nil {
			continue
		}
		records = append(records, &r)
	}
	return records, nil
}

// listIncludingDeleted liefert alle Records eines Typs INKLUSIVE Tombstones
// (fuer Peer-Sync, damit Loeschungen mitpropagiert werden).
func (s *Store) listIncludingDeleted(recordType RecordType) ([]*Record, error) {
	prefix := fmt.Sprintf("/%s/", string(recordType))
	results, err := s.db.Query(context.Background(), query.Query{Prefix: prefix})
	if err != nil {
		return nil, err
	}
	defer results.Close()

	var records []*Record
	for result := range results.Next() {
		if result.Error != nil {
			return nil, result.Error
		}
		var r Record
		if err := json.Unmarshal(result.Value, &r); err != nil {
			continue
		}
		records = append(records, &r) // Tombstones bleiben drin
	}
	return records, nil
}

// Delete markiert einen Record als gelöscht (Tombstone).
// Der Tombstone bleibt im Storage damit Peers die Löschung übernehmen können.
func (s *Store) Delete(recordType RecordType, id string) error {
	existing, err := s.Get(recordType, id)
	if err != nil || existing == nil {
		// Bereits gelöscht oder nie vorhanden – kein Fehler
		return nil
	}
	now := time.Now().UTC()
	existing.DeletedAt = &now
	existing.UpdatedAt = now
	existing.Data      = nil // Nutzlast freigeben
	return s.Put(existing)
}

// HardDelete entfernt einen Record dauerhaft (nur für interne Bereinigung).
func (s *Store) HardDelete(recordType RecordType, id string) error {
	return s.db.Delete(context.Background(), datastoreKey(recordType, id))
}

// HandleIncoming verarbeitet eine per P2P empfangene Nachricht.
func (s *Store) HandleIncoming(topic string, fromPeerID string, data []byte) error {
	s.mu.RLock()
	isTarget := false
	for _, id := range s.replicationTargets {
		if id == fromPeerID {
			isTarget = true
			break
		}
	}
	s.mu.RUnlock()

	// Partner-Ads werden immer gecacht (kein Replikations-Target nötig)
	isPartnerTopic := topic == "fundus.partner" || topic == "fundus.partner.search"

	// Oeffentliche Marktplatz-Topics (Listings, Zertifikate, Jobs, Energie)
	// werden von ALLEN Peers live gecacht - nicht nur von Replikations-Targets.
	// Sonst saehe ein Node nur Angebote seiner ~5 Targets statt des ganzen
	// Marktes. Zusammen mit dem Peer-Sync (Bestandsabgleich beim Connect)
	// ergibt das einen konsistenten, vollstaendigen Marktplatz.
	isPublicMarket := topic == "fundus.listings" || topic == "fundus.certificate" ||
		topic == "fundus.jobs" || topic == "fundus.energy"

	// Persönliche, verschlüsselte Records (Mailbox, Kontakte, Email-Verzeichnis)
	// werden netzweit gecacht, damit sie an jedem Node abrufbar sind, an dem
	// sich der Nutzer einloggt. Inhalt ist Ende-zu-Ende- bzw. Self-verschlüsselt
	// (bzw. signiert beim Verzeichnis), daher unkritisch zu verteilen.
	isPersonal := topic == "fundus.mailbox" || topic == "fundus.contacts" || topic == "fundus.emaildir" || topic == "fundus.keydir"
	if isPersonal {
		var r Record
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		if r.ID == "" || r.Type == "" {
			return nil
		}
		if s.log != nil {
			s.log.Info("Personal-Record via P2P empfangen", zap.String("topic", topic), zap.String("id", r.ID))
		}
		return s.PutSynced(&r)
	}

	if !isTarget && !isPartnerTopic && !isPublicMarket {
		return nil
	}

	// Partner-Ads in eigene RecordTypes speichern (nicht als generischer Record).
	// Schlüssel ist der URHEBER der Ad (FundusID, sonst dessen Peer-ID) – NICHT
	// fromPeerID: das ist nur der weiterleitende Peer. Über einen einzelnen
	// Zwischen-Node (z.B. entfernter Node hinter NAT) kamen sonst alle Ads unter
	// derselben ID an und überschrieben sich gegenseitig.
	if topic == "fundus.partner" {
		key, owner := PartnerAdKey(data, fromPeerID)
		rec := &Record{
			ID:        "peer-ad-" + key,
			Type:      RecordPartnerAd,
			OwnerID:   owner,
			CreatedAt: time.Now(),
			Data:      map[string]any{"raw": string(data)},
		}
		return s.Put(rec)
	}
	if topic == "fundus.partner.search" {
		key, owner := PartnerAdKey(data, fromPeerID)
		rec := &Record{
			ID:        "peer-search-ad-" + key,
			Type:      RecordPartnerSearchAd,
			OwnerID:   owner,
			CreatedAt: time.Now(),
			Data:      map[string]any{"raw": string(data)},
		}
		return s.Put(rec)
	}

	// Generische Records für Replikations-Targets
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("unmarshal incoming: %w", err)
	}

	// SICHERHEIT: Signatur pruefen. Ein signierter Record mit UNGUELTIGER
	// Signatur wird sofort verworfen (Manipulationsversuch).
	if !s.checkSignature(&r) {
		s.log.Warn("Eingehender Record mit ungueltiger Signatur verworfen",
			zap.String("peer", fromPeerID), zap.String("id", r.ID))
		return nil
	}

	// SICHERHEIT: Ein eingehender Record darf einen bestehenden nur dann
	// ueberschreiben/loeschen, wenn die OwnerID uebereinstimmt. Sonst koennte
	// ein boeswilliger Peer fremde Angebote manipulieren oder loeschen, indem
	// er Records mit fremder ID aber eigenem/gefaelschtem Inhalt sendet.
	// (Vollstaendiger Schutz folgt mit krypto. Signaturen ueber Record.Signature.)
	if existing, gerr := s.Get(r.Type, r.ID); gerr == nil && existing != nil {
		// Adoption: ein unsignierter Altbestand darf von einem signierten
		// Record (z.B. adoptiertem Tombstone) ueberschrieben werden.
		legacyExisting := len(existing.Signature) == 0
		if existing.OwnerID != "" && existing.OwnerID != r.OwnerID && !legacyExisting {
			s.log.Warn("Eingehender Record mit abweichender OwnerID verworfen",
				zap.String("peer", fromPeerID),
				zap.String("id", r.ID),
				zap.String("existing_owner", existing.OwnerID),
				zap.String("incoming_owner", r.OwnerID),
			)
			return nil
		}
		// LOESCH-SCHUTZ: lokaler Tombstone wird nicht durch aelteren
		// nicht-geloeschten Record wiederbelebt.
		if existing.DeletedAt != nil && r.DeletedAt == nil &&
			!r.UpdatedAt.After(existing.UpdatedAt) {
			return nil
		}
	}

	// Tombstone empfangen → lokal als Tombstone SPEICHERN (nicht hart loeschen).
	// Hart loeschen wuerde den Re-Sync wieder zulassen; ein gespeicherter
	// Tombstone blockt die Wiederbelebung dauerhaft.
	if r.DeletedAt != nil {
		_ = s.Put(&r)
		s.log.Debug("Tombstone applied from peer",
			zap.String("peer", fromPeerID),
			zap.String("id", r.ID),
		)
		return nil
	}

	if err := s.Put(&r); err != nil {
		return err
	}

	s.mu.Lock()
	s.peerData[fromPeerID] = append(s.peerData[fromPeerID], r.ID)
	s.mu.Unlock()

	s.log.Debug("Replicated record from peer",
		zap.String("peer", fromPeerID),
		zap.String("id", r.ID),
		zap.String("type", string(r.Type)),
	)
	return nil
}

// UpdateReplicationTargets wählt aus der Peer-Liste bis zu maxPeers Targets aus.
// Bevorzugt Peers mit denen wir bereits Daten geteilt haben.
func (s *Store) UpdateReplicationTargets(peers []peer.ID, maxPeers int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if maxPeers > len(peers) {
		maxPeers = len(peers)
	}

	targets := make([]string, 0, maxPeers)
	for i, p := range peers {
		if i >= maxPeers {
			break
		}
		targets = append(targets, p.String())
	}

	s.replicationTargets = targets
	s.log.Info("Replication targets updated",
		zap.Int("count", len(targets)),
		zap.Strings("peers", targets),
	)
	return nil
}

// Stats gibt eine Übersicht über den Speicherstand zurück.
// PublicRecordTypes sind die Record-Typen die zwischen Peers synchronisiert
// werden (oeffentlicher Marktplatz-Bestand). Partner-Profile/Salt bleiben privat.
var PublicRecordTypes = []RecordType{
	RecordListing,
	RecordCertificate,
	RecordEnergy,
	RecordJob,
	// Partner-Ads NICHT hier: sie liefen mit ihrer Original-ID ("my-search-ad")
	// ein und überschrieben das eigene Such-Profil. Sie haben ein eigenes
	// Pull-Protokoll (api/partner.go, PartnerPullProtocol).
}

// ListAllPublic liefert alle oeffentlichen Records (fuer Peer-Sync).
// Inklusive Tombstones, damit Loeschungen mitsynchronisiert werden.
func (s *Store) ListAllPublic() ([]*Record, error) {
	var all []*Record
	for _, rt := range PublicRecordTypes {
		recs, err := s.listIncludingDeleted(rt)
		if err != nil {
			continue // ein Typ-Fehler darf den Sync nicht abbrechen
		}
		all = append(all, recs...)
	}
	return all, nil
}

// PutSynced speichert einen per Peer-Sync empfangenen Record. Anders als
// HandleIncoming ist KEIN Replication-Target noetig - der Sync ist explizit
// angefragt. Es gilt last-write-wins: ein vorhandener neuerer Record wird
// nicht ueberschrieben. Tombstones (DeletedAt) werden respektiert.
func (s *Store) PutSynced(r *Record) error {
	if r == nil || r.ID == "" || r.Type == "" {
		return fmt.Errorf("store: ungueltiger sync-record")
	}
	// Signatur pruefen (ungueltig signierte Records verwerfen)
	if !s.checkSignature(r) {
		return nil
	}
	existing, err := s.Get(r.Type, r.ID)
	if err == nil && existing != nil {
		// SICHERHEIT: abweichende OwnerID -> nicht ueberschreiben.
		// Ausnahme: existing ist ein UNSIGNIERTER Altbestand (Adoption erlaubt).
		ownerMismatch := existing.OwnerID != "" && existing.OwnerID != r.OwnerID
		legacyExisting := len(existing.Signature) == 0
		if ownerMismatch && !legacyExisting {
			return nil
		}
		// LOESCH-SCHUTZ: Ist der lokale Record bereits ein Tombstone, darf ein
		// eingehender NICHT-geloeschter Record ihn nicht "wiederbeleben",
		// solange der Tombstone nicht aelter ist. Verhindert dass geloeschte
		// Angebote durch Re-Sync vom Peer zurueckkommen.
		if existing.DeletedAt != nil && r.DeletedAt == nil {
			if !r.UpdatedAt.After(existing.UpdatedAt) {
				return nil
			}
		}
		// Vorhanden: nur uebernehmen wenn der eingehende neuer ist
		if !r.UpdatedAt.After(existing.UpdatedAt) {
			return nil
		}
	}
	return s.Put(r)
}

func (s *Store) Stats() map[string]any {
	// Kurzlebiger Cache (2s): bei häufigen Render-Calls nicht jedes Mal die DB
	// 4× scannen. Verhindert, dass Seiten bei Backend-Last hängen.
	s.statsCacheMu.Lock()
	if s.statsCache != nil && time.Since(s.statsCacheAt) < 2*time.Second {
		cached := s.statsCache
		s.statsCacheMu.Unlock()
		return cached
	}
	s.statsCacheMu.Unlock()

	s.mu.RLock()
	counts := map[string]int{}
	for _, rt := range []RecordType{RecordListing, RecordCertificate, RecordEnergy, RecordJob} {
		records, _ := s.List(rt)
		counts[string(rt)] = len(records)
	}
	result := map[string]any{
		"records":              counts,
		"replication_targets":  len(s.replicationTargets),
		"peer_data_cached_for": len(s.peerData),
	}
	s.mu.RUnlock()

	s.statsCacheMu.Lock()
	s.statsCache = result
	s.statsCacheAt = time.Now()
	s.statsCacheMu.Unlock()
	return result
}

// Close schließt die Datenbank.
func (s *Store) Close() error {
	return s.db.Close()
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func datastoreKey(rt RecordType, id string) datastore.Key {
	return datastore.NewKey(fmt.Sprintf("/%s/%s", string(rt), id))
}

// PartnerAdKey liefert den Speicher-Schlüssel und Eigentümer einer Partner-Ad
// anhand ihres Inhalts: FundusID des Urhebers, sonst dessen Peer-ID, sonst der
// übergebene Fallback (weiterleitender Peer).
func PartnerAdKey(raw []byte, fallback string) (key, owner string) {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	fid, _ := m["fundus_id"].(string)
	pid, _ := m["peer_id"].(string)
	owner = pid
	if owner == "" {
		owner = fallback
	}
	switch {
	case fid != "":
		key = strings.ToLower(fid)
	case pid != "":
		key = pid
	default:
		key = fallback
	}
	return key, owner
}
