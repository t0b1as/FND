package messenger

import (
	"fmt"
	"testing"
	"time"
)

func testStore(t *testing.T) *HistoryStore {
	t.Helper()
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	hs, err := newHistoryStoreWithKey(t.TempDir(), "0xtestidentity", key)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return hs
}

func TestHistoryAppendAndRead(t *testing.T) {
	hs := testStore(t)
	peer := "0xpeeraaa"
	base := time.Now()
	for i := 0; i < 5; i++ {
		err := hs.Append(HistoryEntry{
			ID:        fmt.Sprintf("m%d", i),
			PeerID:    peer,
			Outgoing:  i%2 == 0,
			Type:      "text",
			Text:      fmt.Sprintf("Nachricht %d", i),
			Timestamp: base.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	page, hasMore, err := hs.Page(peer, nil, 32)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page) != 5 {
		t.Fatalf("erwartet 5, bekam %d", len(page))
	}
	if hasMore {
		t.Fatal("hasMore sollte false sein (nur 5 Einträge)")
	}
	// Chronologisch (älteste zuerst)
	for i := 0; i < 5; i++ {
		if page[i].Text != fmt.Sprintf("Nachricht %d", i) {
			t.Fatalf("Reihenfolge falsch bei %d: %q", i, page[i].Text)
		}
	}
}

func TestHistoryPaginationNewest(t *testing.T) {
	hs := testStore(t)
	peer := "0xpeerbbb"
	base := time.Now()
	for i := 0; i < 40; i++ {
		hs.Append(HistoryEntry{
			ID: fmt.Sprintf("m%d", i), PeerID: peer, Type: "text",
			Text: fmt.Sprintf("n%d", i), Timestamp: base.Add(time.Duration(i) * time.Second),
		})
	}
	// Neueste 32 (ohne Cursor)
	page, hasMore, err := hs.Page(peer, nil, 32)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page) != 32 {
		t.Fatalf("erwartet 32, bekam %d", len(page))
	}
	if !hasMore {
		t.Fatal("hasMore sollte true sein (40 > 32)")
	}
	// Die neuesten 32 sind n8..n39
	if page[0].Text != "n8" || page[31].Text != "n39" {
		t.Fatalf("falsches Fenster: %s..%s", page[0].Text, page[31].Text)
	}
}

func TestHistoryPaginationCursor(t *testing.T) {
	hs := testStore(t)
	peer := "0xpeerccc"
	base := time.Now()
	for i := 0; i < 40; i++ {
		hs.Append(HistoryEntry{
			ID: fmt.Sprintf("m%d", i), PeerID: peer, Type: "text",
			Text: fmt.Sprintf("n%d", i), Timestamp: base.Add(time.Duration(i) * time.Second),
		})
	}
	// Erste Seite (neueste 32: n8..n39), dann davor laden.
	first, _, _ := hs.Page(peer, nil, 32)
	cursor := first[0].Timestamp // ts von n8
	older, hasMore, err := hs.Page(peer, &cursor, 32)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	// Vor n8 liegen n0..n7 → 8 Einträge.
	if len(older) != 8 {
		t.Fatalf("erwartet 8 ältere, bekam %d", len(older))
	}
	if hasMore {
		t.Fatal("keine weiteren vor n0")
	}
	if older[0].Text != "n0" || older[7].Text != "n7" {
		t.Fatalf("falsches Fenster: %s..%s", older[0].Text, older[7].Text)
	}
}

func TestHistoryEncryptedAtRest(t *testing.T) {
	// Eine Datei mit FALSCHEM Schlüssel darf nicht entschlüsselbar sein.
	dir := t.TempDir()
	var k1, k2 [32]byte
	for i := range k1 {
		k1[i] = byte(i)
		k2[i] = byte(255 - i)
	}
	h1, _ := newHistoryStoreWithKey(dir, "0xabc", k1)
	h1.Append(HistoryEntry{ID: "x", PeerID: "0xpeer", Type: "text", Text: "geheim", Timestamp: time.Now()})

	// Anderer Schlüssel, gleicher Pfad → Einträge nicht lesbar (übersprungen).
	h2, _ := newHistoryStoreWithKey(dir, "0xabc", k2)
	page, _, err := h2.Page("0xpeer", nil, 32)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("mit falschem Schlüssel dürfen 0 Einträge lesbar sein, waren %d", len(page))
	}
}

func TestHistoryEmptyConversation(t *testing.T) {
	hs := testStore(t)
	page, hasMore, err := hs.Page("0xniemand", nil, 32)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page) != 0 || hasMore {
		t.Fatal("leere Konversation → keine Einträge, hasMore false")
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID("0xABC123"); got != "0xabc123" {
		t.Fatalf("sanitize: %q", got)
	}
	// Pfad-Tricks müssen entfernt werden.
	if got := sanitizeID("../../etc/passwd"); got == "" || got == "../../etc/passwd" {
		t.Fatalf("Pfad-Trick nicht entschärft: %q", got)
	}
}
