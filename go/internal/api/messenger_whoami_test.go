package api

import (
	"encoding/json"
	"testing"
)

// TestMessengerWhoamiNoSession: ohne aktive Messenger-Identität liefert whoami
// active:false (und keinen Schlüssel) — die Marktplatz-Seiten betten dann keinen
// Kontaktschlüssel ein.
func TestMessengerWhoamiNoSession(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := get(t, srv, "/api/v1/messenger/whoami")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Status %d != 200", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if active, _ := out["active"].(bool); active {
		t.Fatal("ohne Session sollte active=false sein")
	}
	if _, hasKey := out["public_key"]; hasKey {
		t.Fatal("ohne Session sollte kein public_key geliefert werden")
	}
}
