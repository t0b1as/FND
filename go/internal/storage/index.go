package storage

import (
	"context"
	"fmt"
	"strings"

	datastore "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
)

// ─── Sekundärindex für exakte Feld-Lookups ───────────────────────────────────
//
// Manche Zugriffe suchen EINEN Record anhand eines exakten Feldwerts (z.B. das
// Listing mit einem bestimmten content_hash für die Vertragsvorschau). Ohne
// Index ist das ein linearer Scan über alle Records des Typs (List + JSON-
// Unmarshal jedes Eintrags). Dieser Index bildet `feldwert → record_id` ab und
// macht solche Lookups O(1) statt O(n).
//
// Persistenz: Index-Einträge liegen unter `/idx/<type>/<field>/<value>` und
// werden bei jedem Put/PutSynced/Delete mitgepflegt. Volltext-/Substring-Suchen
// (searchLocal/jobsearch) sind BEWUSST nicht hier abgedeckt — die bräuchten
// einen invertierten Index und haben bereits ein CPU-Limit (searchLocalMax).

// indexedFields legt fest, welche Data-Felder pro Record-Typ exakt indexiert
// werden. Erweiterbar: hier ein Feld eintragen, dann wird es automatisch gepflegt.
var indexedFields = map[RecordType][]string{
	RecordListing: {"content_hash"},
}

// indexKey baut den Datastore-Key eines Index-Eintrags. Der Wert wird normalisiert
// (klein, ohne 0x-Präfix), damit Lookups unabhängig von Schreibweise treffen.
func indexKey(rt RecordType, field, value string) datastore.Key {
	return datastore.NewKey(fmt.Sprintf("/idx/%s/%s/%s", string(rt), field, normalizeIndexValue(value)))
}

// normalizeIndexValue vereinheitlicht Indexwerte: lowercase + ohne 0x-Präfix.
// So treffen Lookups unabhängig davon, ob der Hash mit/ohne 0x gespeichert wurde.
func normalizeIndexValue(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) >= 2 && v[0] == '0' && v[1] == 'x' {
		v = v[2:]
	}
	return v
}

// indexFieldValue liest den (String-)Wert eines indexierten Feldes aus den
// Record-Daten. Leere Werte werden nicht indexiert.
func indexFieldValue(r *Record, field string) string {
	if r.Data == nil {
		return ""
	}
	v, _ := r.Data[field].(string)
	return strings.TrimSpace(v)
}

// updateIndex pflegt die Index-Einträge eines Records. removeOld entfernt zuerst
// etwaige veraltete Einträge (vor einem Update), bevor die aktuellen geschrieben
// werden. Bei einem Tombstone (DeletedAt gesetzt) werden nur Einträge entfernt.
func (s *Store) updateIndex(r *Record, prev *Record) {
	fields, ok := indexedFields[r.Type]
	if !ok {
		return // dieser Typ wird nicht indexiert
	}
	ctx := context.Background()
	for _, field := range fields {
		// Alten Eintrag entfernen, falls sich der Wert geändert hat oder gelöscht wird.
		if prev != nil {
			if oldVal := indexFieldValue(prev, field); oldVal != "" {
				newVal := indexFieldValue(r, field)
				if r.DeletedAt != nil || oldVal != newVal {
					_ = s.db.Delete(ctx, indexKey(r.Type, field, oldVal))
				}
			}
		}
		// Neuen Eintrag schreiben (außer bei Tombstone).
		if r.DeletedAt == nil {
			if val := indexFieldValue(r, field); val != "" {
				_ = s.db.Put(ctx, indexKey(r.Type, field, val), []byte(r.ID))
			}
		}
	}
}

// FindByField sucht einen Record-ID anhand eines exakt indexierten Feldwerts in
// O(1). Gibt die Record-ID + true zurück, oder "" + false, wenn kein Eintrag
// existiert. Nur für Felder aus indexedFields nutzbar.
func (s *Store) FindByField(rt RecordType, field, value string) (string, bool) {
	raw, err := s.db.Get(context.Background(), indexKey(rt, field, value))
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return string(raw), true
}

// FindRecordByField kombiniert FindByField + Get: liefert direkt den Record (oder
// nil), und überspringt Tombstones. Praktisch für Lookups, die den vollen Record
// brauchen.
func (s *Store) FindRecordByField(rt RecordType, field, value string) (*Record, bool) {
	id, ok := s.FindByField(rt, field, value)
	if !ok {
		return nil, false
	}
	rec, err := s.Get(rt, id)
	if err != nil || rec == nil || rec.DeletedAt != nil {
		return nil, false
	}
	return rec, true
}

// RebuildIndex baut den Sekundärindex eines Typs vollständig neu auf (einmalig,
// z.B. nach einem Upgrade, das neue indexierte Felder einführt, oder zur Reparatur).
// Iteriert einmal über alle Records des Typs und schreibt die Index-Einträge.
func (s *Store) RebuildIndex(rt RecordType) error {
	fields, ok := indexedFields[rt]
	if !ok {
		return nil
	}
	ctx := context.Background()
	// Alte Index-Einträge dieses Typs entfernen.
	prefix := fmt.Sprintf("/idx/%s/", string(rt))
	res, err := s.db.Query(ctx, query.Query{Prefix: prefix, KeysOnly: true})
	if err != nil {
		return err
	}
	for r := range res.Next() {
		if r.Error == nil {
			_ = s.db.Delete(ctx, datastore.NewKey(r.Key))
		}
	}
	res.Close()
	// Neu aufbauen aus dem aktuellen Bestand.
	records, err := s.List(rt)
	if err != nil {
		return err
	}
	for _, rec := range records {
		for _, field := range fields {
			if val := indexFieldValue(rec, field); val != "" {
				_ = s.db.Put(ctx, indexKey(rt, field, val), []byte(rec.ID))
			}
		}
	}
	return nil
}
