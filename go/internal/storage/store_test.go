package storage_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/storage"
)

// newTestStore erstellt einen Store in einem temporären Verzeichnis.
// t.TempDir() räumt nach dem Test automatisch auf.
func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.New(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testRecord(id string, rt storage.RecordType) *storage.Record {
	return &storage.Record{
		ID:        id,
		Type:      rt,
		OwnerID:   "peer-abc",
		CreatedAt: time.Now(),
		Data: map[string]any{
			"title":    "Test-Artikel",
			"price":    42.0,
			"keywords": []string{"test", "artikel"},
		},
	}
}

// =============================================================================
//  Put / Get
// =============================================================================

func TestStore_PutAndGet(t *testing.T) {
	s := newTestStore(t)
	rec := testRecord("rec-1", storage.RecordListing)

	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(storage.RecordListing, "rec-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.ID != "rec-1" {
		t.Errorf("ID = %q, want %q", got.ID, "rec-1")
	}
	if got.Type != storage.RecordListing {
		t.Errorf("Type = %q, want %q", got.Type, storage.RecordListing)
	}
	if got.Data["title"] != "Test-Artikel" {
		t.Errorf("Data[title] = %v, want %q", got.Data["title"], "Test-Artikel")
	}
}

func TestStore_Get_NotFound(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Get(storage.RecordListing, "nonexistent")
	if err != nil {
		t.Fatalf("Get on missing key should not error: %v", err)
	}
	if got != nil {
		t.Errorf("Get on missing key returned non-nil: %+v", got)
	}
}

func TestStore_Put_SetsUpdatedAt(t *testing.T) {
	s   := newTestStore(t)
	rec := testRecord("ts-test", storage.RecordListing)
	rec.UpdatedAt = time.Time{} // explizit auf Null setzen

	before := time.Now()
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	got, _ := s.Get(storage.RecordListing, "ts-test")
	if got.UpdatedAt.Before(before) || got.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt = %v, expected between %v and %v",
			got.UpdatedAt, before, after)
	}
}

func TestStore_Put_EmptyID_Errors(t *testing.T) {
	s   := newTestStore(t)
	rec := testRecord("", storage.RecordListing)
	if err := s.Put(rec); err == nil {
		t.Error("Expected error for empty ID, got nil")
	}
}

// Überschreiben eines bestehenden Records
func TestStore_Put_Overwrite(t *testing.T) {
	s := newTestStore(t)

	rec := testRecord("overwrite-me", storage.RecordListing)
	_ = s.Put(rec)

	rec.Data["title"] = "Neuer Titel"
	if err := s.Put(rec); err != nil {
		t.Fatalf("overwrite Put: %v", err)
	}

	got, _ := s.Get(storage.RecordListing, "overwrite-me")
	if got.Data["title"] != "Neuer Titel" {
		t.Errorf("title = %v after overwrite, want %q", got.Data["title"], "Neuer Titel")
	}
}

// =============================================================================
//  List
// =============================================================================

func TestStore_List(t *testing.T) {
	s := newTestStore(t)

	// 3 Listings, 2 Jobs anlegen
	for i := 0; i < 3; i++ {
		_ = s.Put(testRecord(fmt.Sprintf("listing-%d", i), storage.RecordListing))
	}
	for i := 0; i < 2; i++ {
		_ = s.Put(testRecord(fmt.Sprintf("job-%d", i), storage.RecordJob))
	}

	listings, err := s.List(storage.RecordListing)
	if err != nil {
		t.Fatalf("List listings: %v", err)
	}
	if len(listings) != 3 {
		t.Errorf("len(listings) = %d, want 3", len(listings))
	}

	jobs, err := s.List(storage.RecordJob)
	if err != nil {
		t.Fatalf("List jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Errorf("len(jobs) = %d, want 2", len(jobs))
	}
}

func TestStore_List_Empty(t *testing.T) {
	s := newTestStore(t)
	records, err := s.List(storage.RecordEnergy)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected empty slice, got %d records", len(records))
	}
}

// Typen dürfen sich nicht gegenseitig beeinflussen
func TestStore_List_TypeIsolation(t *testing.T) {
	s := newTestStore(t)
	_ = s.Put(testRecord("l1", storage.RecordListing))
	_ = s.Put(testRecord("c1", storage.RecordCertificate))
	_ = s.Put(testRecord("e1", storage.RecordEnergy))

	for _, rt := range []storage.RecordType{
		storage.RecordListing, storage.RecordCertificate, storage.RecordEnergy,
	} {
		recs, err := s.List(rt)
		if err != nil {
			t.Fatalf("List(%s): %v", rt, err)
		}
		if len(recs) != 1 {
			t.Errorf("List(%s) = %d records, want 1", rt, len(recs))
		}
		if recs[0].Type != rt {
			t.Errorf("List(%s)[0].Type = %s", rt, recs[0].Type)
		}
	}
}

// =============================================================================
//  Delete
// =============================================================================

func TestStore_Delete(t *testing.T) {
	s := newTestStore(t)
	_ = s.Put(testRecord("del-me", storage.RecordListing))

	if err := s.Delete(storage.RecordListing, "del-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := s.Get(storage.RecordListing, "del-me")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Error("Record still exists after Delete")
	}
}

// Delete auf nicht-existierenden Key darf nicht fehlerwerfen
func TestStore_Delete_NotFound_NoError(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(storage.RecordListing, "ghost"); err != nil {
		t.Errorf("Delete non-existent: %v", err)
	}
}

// =============================================================================
//  Replication
// =============================================================================

func TestStore_UpdateReplicationTargets(t *testing.T) {
	s := newTestStore(t)

	// 10 Fake-Peer-IDs erzeugen
	peers := make([]peer.ID, 10)
	for i := range peers {
		peers[i] = peer.ID(fmt.Sprintf("peer-%02d", i))
	}

	if err := s.UpdateReplicationTargets(peers, 5); err != nil {
		t.Fatalf("UpdateReplicationTargets: %v", err)
	}

	stats := s.Stats()
	targets, ok := stats["replication_targets"].(int)
	if !ok {
		t.Fatalf("stats[replication_targets] type unexpected: %T", stats["replication_targets"])
	}
	if targets != 5 {
		t.Errorf("replication_targets = %d, want 5", targets)
	}
}

func TestStore_UpdateReplicationTargets_FewerThanMax(t *testing.T) {
	s := newTestStore(t)
	peers := []peer.ID{"peer-a", "peer-b"}

	if err := s.UpdateReplicationTargets(peers, 5); err != nil {
		t.Fatal(err)
	}

	stats := s.Stats()
	targets := stats["replication_targets"].(int)
	if targets != 2 {
		t.Errorf("replication_targets = %d, want 2 (only 2 peers available)", targets)
	}
}

func TestStore_HandleIncoming_IgnoresNonTarget(t *testing.T) {
	s := newTestStore(t)
	// Kein Replication-Target, kein Partner-Topic → nicht speichern

	rec := testRecord("incoming-1", storage.RecordListing)
	data, _ := json.Marshal(rec)

	if err := s.HandleIncoming("fundus.listings", "peer-unknown", data); err != nil {
		t.Fatalf("HandleIncoming: %v", err)
	}

	got, _ := s.Get(storage.RecordListing, "incoming-1")
	if got != nil {
		t.Error("Record should NOT be stored from non-target peer on non-partner topic")
	}
}

func TestStore_HandleIncoming_StoresFromTarget(t *testing.T) {
	s := newTestStore(t)

	targetID := peer.ID("peer-target-1")
	_ = s.UpdateReplicationTargets([]peer.ID{targetID}, 5)

	rec  := testRecord("incoming-2", storage.RecordListing)
	data, _ := json.Marshal(rec)

	if err := s.HandleIncoming("fundus.listings", string(targetID), data); err != nil {
		t.Fatalf("HandleIncoming from target: %v", err)
	}

	got, err := s.Get(storage.RecordListing, "incoming-2")
	if err != nil {
		t.Fatalf("Get after HandleIncoming: %v", err)
	}
	if got == nil {
		t.Error("Record SHOULD be stored from target peer")
	}
}

func TestStore_HandleIncoming_PartnerAd_AlwaysStored(t *testing.T) {
	s := newTestStore(t)
	// Kein Replication-Target – aber Partner-Topic wird immer gecacht

	payload := []byte(`{"peer_id":"peer-ext","gh":"abc","ah":"def","sgh":[],"sah":[],"sr":50,"pub":"2024-01-01T00:00:00Z","exp":"2099-01-01T00:00:00Z"}`)

	if err := s.HandleIncoming("fundus.partner", "peer-ext", payload); err != nil {
		t.Fatalf("HandleIncoming partner: %v", err)
	}

	ads, err := s.List(storage.RecordPartnerAd)
	if err != nil {
		t.Fatal(err)
	}
	if len(ads) == 0 {
		t.Error("Partner ad should always be cached regardless of replication targets")
	}
}

func TestStore_HandleIncoming_PartnerSearchAd_AlwaysStored(t *testing.T) {
	s := newTestStore(t)

	payload := []byte(`{"peer_id":"peer-srch","gc":"female","ar":"26-35","lat_r":50.1,"lnc":8.7,"pub":"2024-01-01T00:00:00Z","exp":"2099-01-01T00:00:00Z"}`)

	if err := s.HandleIncoming("fundus.partner.search", "peer-srch", payload); err != nil {
		t.Fatalf("HandleIncoming search ad: %v", err)
	}

	ads, err := s.List(storage.RecordPartnerSearchAd)
	if err != nil {
		t.Fatal(err)
	}
	if len(ads) == 0 {
		t.Error("SearchableAd should always be cached")
	}
}

func TestStore_HandleIncoming_InvalidJSON(t *testing.T) {
	s     := newTestStore(t)
	target := peer.ID("peer-target-2")
	_ = s.UpdateReplicationTargets([]peer.ID{target}, 5)

	err := s.HandleIncoming("fundus.listings", string(target), []byte("not-json"))
	if err == nil {
		t.Error("Expected error for invalid JSON, got nil")
	}
}

// =============================================================================
//  Stats
// =============================================================================

func TestStore_Stats(t *testing.T) {
	s := newTestStore(t)
	_ = s.Put(testRecord("s1", storage.RecordListing))
	_ = s.Put(testRecord("s2", storage.RecordListing))
	_ = s.Put(testRecord("e1", storage.RecordEnergy))

	stats := s.Stats()
	records, ok := stats["records"].(map[string]int)
	if !ok {
		t.Fatalf("stats[records] type: %T", stats["records"])
	}
	if records["listing"] != 2 {
		t.Errorf("listing count = %d, want 2", records["listing"])
	}
	if records["energy"] != 1 {
		t.Errorf("energy count = %d, want 1", records["energy"])
	}
}

// =============================================================================
//  Marshal
// =============================================================================

func TestRecord_Marshal_Roundtrip(t *testing.T) {
	rec := testRecord("marshal-1", storage.RecordEnergy)
	rec.Data["kwh"] = 1.2345

	data, err := rec.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got storage.Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ID != rec.ID {
		t.Errorf("ID = %q, want %q", got.ID, rec.ID)
	}
	if got.Data["kwh"] != 1.2345 {
		t.Errorf("Data[kwh] = %v, want 1.2345", got.Data["kwh"])
	}
}

// =============================================================================
//  Persistenz: Store schließen und neu öffnen
// =============================================================================

func TestStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	log := zap.NewNop()

	// Schreiben
	s1, err := storage.New(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Put(testRecord("persist-1", storage.RecordJob))
	_ = s1.Close()

	// Neu öffnen und lesen
	s2, err := storage.New(dir, log)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	got, err := s2.Get(storage.RecordJob, "persist-1")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got == nil {
		t.Error("Record not persisted after close/reopen")
	}
}
