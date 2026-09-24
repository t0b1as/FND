package storage

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	datastore "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/query"
)

// ─── Invertierter Trigramm-Index für skalierbare Volltextsuche ───────────────
//
// Problem: die lineare Volltextsuche (strings.Contains über alle Records) bricht
// bei vielen Treffern willkürlich ab (searchLocalMax) und skaliert nicht. Lösung:
// ein invertierter Trigramm-Index. Der durchsuchbare Text jedes Records wird in
// überlappende 3-Zeichen-Folgen (Trigramme) zerlegt; der Index bildet
// `trigramm → record_ids` ab.
//
// Suche: die Query wird ebenfalls in Trigramme zerlegt; die SCHNITTMENGE der
// ID-Mengen aller Query-Trigramme ergibt die KANDIDATEN. Das ist eine Obermenge
// der echten Substring-Treffer (Trigramme können in anderer Reihenfolge im Text
// stehen), daher MUSS der Aufrufer jeden Kandidaten mit dem echten
// strings.Contains verifizieren. Damit ist das Ergebnis identisch zur linearen
// Suche — nur werden statt aller Records bloß die Kandidaten geladen.
//
// Zusätzlich werden Kategorien als Term `cat:<kategorie>` indexiert, damit auch
// das Browsing ohne Suchbegriff (leere Query + Kategorie) den Index nutzt.
//
// Persistenz: `/inv/<type>/<hexterm>/<record_id>`. Der Term ist hex-kodiert,
// damit Sonderzeichen/Leerzeichen/Slashes den Datastore-Key nicht brechen. Eine
// Prefix-Query auf `/inv/<type>/<hexterm>/` liefert alle IDs eines Terms.

// invIndexedTypes: welche Record-Typen volltext-indexiert werden + welche
// Data-Felder in den durchsuchbaren Text einfließen (Reihenfolge wie im bisherigen
// linearen Scan, damit der verifizierende strings.Contains exakt denselben
// Heuhaufen sieht).
var invTextFields = map[RecordType][]string{
	RecordListing: {"title", "description", "listing_text", "keywords"},
	RecordJob:     {"title", "description", "company", "listing_text", "keywords"},
}

// minTrigramQuery ist die kleinste Query-Länge, die der Trigramm-Index bedienen
// kann. Kürzere Queries (<3 Zeichen) muss der Aufrufer per linearem Scan abdecken.
const minTrigramQuery = 3

// haystack baut den durchsuchbaren Lowercase-Text eines Records — exakt wie der
// bisherige lineare Scan ihn zusammensetzt.
func haystack(r *Record, fields []string) string {
	if r.Data == nil {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "keywords" {
			parts = append(parts, keywordsJoin(r.Data["keywords"]))
			continue
		}
		if v, ok := r.Data[f].(string); ok {
			parts = append(parts, v)
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// keywordsJoin macht aus einem keywords-Feld (Liste oder String) einen String.
func keywordsJoin(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		ss := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				ss = append(ss, s)
			}
		}
		return strings.Join(ss, " ")
	case []string:
		return strings.Join(t, " ")
	}
	return ""
}

// trigrams zerlegt einen (bereits lowercase) Text in die Menge seiner eindeutigen
// Trigramme. Iteriert über RUNES (nicht Bytes), damit Umlaute korrekt behandelt
// werden ("tür" → tür, nicht zerschnittene Mehrbyte-Sequenzen).
func trigrams(text string) []string {
	runes := []rune(text)
	if len(runes) < 3 {
		return nil
	}
	seen := make(map[string]struct{}, len(runes))
	out := make([]string, 0, len(runes))
	for i := 0; i+3 <= len(runes); i++ {
		tg := string(runes[i : i+3])
		if _, ok := seen[tg]; ok {
			continue
		}
		seen[tg] = struct{}{}
		out = append(out, tg)
	}
	return out
}

// invTermKey baut den Datastore-Key eines Index-Eintrags (Term hex-kodiert).
func invTermKey(rt RecordType, term, id string) datastore.Key {
	return datastore.NewKey(fmt.Sprintf("/inv/%s/%s/%s",
		string(rt), hex.EncodeToString([]byte(term)), id))
}

// invTermPrefix baut den Prefix für alle IDs eines Terms.
func invTermPrefix(rt RecordType, term string) string {
	return fmt.Sprintf("/inv/%s/%s/", string(rt), hex.EncodeToString([]byte(term)))
}

// recordTerms sammelt alle Index-Terme eines Records: die Trigramme seines
// Volltexts plus den Kategorie-Term (für Browsing ohne Suchbegriff).
func recordTerms(r *Record, fields []string) []string {
	terms := trigrams(haystack(r, fields))
	if r.Data != nil {
		if cat, ok := r.Data["category"].(string); ok {
			cat = strings.ToLower(strings.TrimSpace(cat))
			if cat != "" {
				terms = append(terms, "cat:"+cat)
			}
		}
	}
	return terms
}

// updateInvIndex pflegt den invertierten Index eines Records (Diff alt→neu). Bei
// einem Tombstone (DeletedAt gesetzt) werden alle Terme entfernt.
func (s *Store) updateInvIndex(r *Record, prev *Record) {
	fields, ok := invTextFields[r.Type]
	if !ok {
		return
	}
	ctx := context.Background()

	var oldTerms, newTerms map[string]struct{}
	if prev != nil {
		oldTerms = toSet(recordTerms(prev, fields))
	}
	if r.DeletedAt == nil {
		newTerms = toSet(recordTerms(r, fields))
	}

	// Entfernen: in alt, aber nicht in neu.
	for term := range oldTerms {
		if _, keep := newTerms[term]; !keep {
			_ = s.db.Delete(ctx, invTermKey(r.Type, term, r.ID))
		}
	}
	// Hinzufügen: in neu, aber nicht in alt.
	for term := range newTerms {
		if _, had := oldTerms[term]; !had {
			_ = s.db.Put(ctx, invTermKey(r.Type, term, r.ID), []byte{1})
		}
	}
}

// idsForTerm liefert alle Record-IDs, die einen Term enthalten (Prefix-Query).
func (s *Store) idsForTerm(rt RecordType, term string) (map[string]struct{}, error) {
	res, err := s.db.Query(context.Background(), query.Query{
		Prefix: invTermPrefix(rt, term), KeysOnly: true,
	})
	if err != nil {
		return nil, err
	}
	defer res.Close()
	out := make(map[string]struct{})
	for e := range res.Next() {
		if e.Error != nil {
			continue
		}
		// Key: /inv/<type>/<hexterm>/<id> → letztes Segment ist die ID.
		k := e.Key
		if idx := strings.LastIndex(k, "/"); idx >= 0 && idx+1 < len(k) {
			out[k[idx+1:]] = struct{}{}
		}
	}
	return out, nil
}

// SearchCandidates liefert die Kandidaten-Record-IDs für eine Volltext-Query über
// den Trigramm-Index. Der zweite Rückgabewert ist false, wenn die Query zu kurz
// für den Index ist (<3 Zeichen) — dann muss der Aufrufer linear scannen.
// WICHTIG: Kandidaten sind eine Obermenge; der Aufrufer MUSS mit strings.Contains
// verifizieren.
func (s *Store) SearchCandidates(rt RecordType, queryText string) (map[string]struct{}, bool) {
	q := strings.ToLower(strings.TrimSpace(queryText))
	if len([]rune(q)) < minTrigramQuery {
		return nil, false
	}
	tris := trigrams(q)
	if len(tris) == 0 {
		return nil, false
	}
	var candidates map[string]struct{}
	for i, tg := range tris {
		ids, err := s.idsForTerm(rt, tg)
		if err != nil {
			return nil, false
		}
		if i == 0 {
			candidates = ids
		} else {
			candidates = intersect(candidates, ids)
		}
		if len(candidates) == 0 {
			break // leere Schnittmenge → kein Treffer möglich
		}
	}
	return candidates, true
}

// CategoryCandidates liefert die Record-IDs einer Kategorie (für Browsing ohne
// Suchbegriff). false, wenn keine Kategorie angegeben.
func (s *Store) CategoryCandidates(rt RecordType, category string) (map[string]struct{}, bool) {
	cat := strings.ToLower(strings.TrimSpace(category))
	if cat == "" {
		return nil, false
	}
	ids, err := s.idsForTerm(rt, "cat:"+cat)
	if err != nil {
		return nil, false
	}
	return ids, true
}

// RebuildInvIndex baut den invertierten Index eines Typs vollständig neu auf.
func (s *Store) RebuildInvIndex(rt RecordType) error {
	fields, ok := invTextFields[rt]
	if !ok {
		return nil
	}
	ctx := context.Background()
	// Alte Einträge dieses Typs entfernen.
	prefix := fmt.Sprintf("/inv/%s/", string(rt))
	res, err := s.db.Query(ctx, query.Query{Prefix: prefix, KeysOnly: true})
	if err != nil {
		return err
	}
	for e := range res.Next() {
		if e.Error == nil {
			_ = s.db.Delete(ctx, datastore.NewKey(e.Key))
		}
	}
	res.Close()
	// Neu aufbauen.
	records, err := s.List(rt)
	if err != nil {
		return err
	}
	for _, rec := range records {
		for _, term := range recordTerms(rec, fields) {
			_ = s.db.Put(ctx, invTermKey(rt, term, rec.ID), []byte{1})
		}
	}
	return nil
}

// --- Mengen-Helfer ---

func toSet(items []string) map[string]struct{} {
	m := make(map[string]struct{}, len(items))
	for _, it := range items {
		m[it] = struct{}{}
	}
	return m
}

func intersect(a, b map[string]struct{}) map[string]struct{} {
	// Über die kleinere Menge iterieren.
	if len(b) < len(a) {
		a, b = b, a
	}
	out := make(map[string]struct{}, len(a))
	for k := range a {
		if _, ok := b[k]; ok {
			out[k] = struct{}{}
		}
	}
	return out
}
