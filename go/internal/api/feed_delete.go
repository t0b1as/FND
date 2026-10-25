package api

// Echtes Löschen im Feed (R817).
//
// Bisher verbarg eine Löschmarke den Beitrag nur; Inhalt, Kommentare und
// Anhänge lagen weiter auf allen Nodes. Jetzt entfernt JEDER Node, der eine
// gültige Löschmarke des Autors sieht, seine Kopie: den Beitrag, alle
// Kommentare, Likes, Bearbeitungen und Freigaben. Nur die Löschmarke selbst
// bleibt – als Nachricht an Nodes, die noch nichts davon wissen. Sie enthält
// keinen Inhalt.
//
// ── Anhänge ─────────────────────────────────────────────────────────────────
// Der Datei-Speicher legt nach INHALT ab und kennt keinen Besitzer: Laden
// zwei Nutzer dasselbe GIF hoch, ist es dieselbe Datei. Deshalb führt jeder
// Node eine Verweisliste (feed_media, nur lokal): welche Beiträge, die auf
// IHM entstanden, welche Datei nutzen. Entfernt wird eine Datei erst, wenn
//   - kein Beitrag dieses Nodes sie mehr nutzt,
//   - kein Inserat sie nutzt,
//   - und sie im Index als Feed-Upload gekennzeichnet ist ("feed-…"). Hat
//     jemand dieselbe Datei auch unter "Dateien" abgelegt, trägt der Index
//     dort deren Namen – dann bleibt sie.
// Die Kopien auf anderen Nodes räumt die vorhandene Bereinigung verwaister
// Kopien auf (orphan_gc), wie beim Löschen eines Inserats.

import (
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/storage"
	"go.uber.org/zap"
)

// feedMedienMerken trägt die Dateien eines Beitrags in die Verweisliste ein.
func (s *Server) feedMedienMerken(beitrag string, i feedInhalt) {
	var hashes []string
	hashes = append(hashes, i.Bilder...)
	hashes = append(hashes, i.Videos...)
	if i.VidDat != "" {
		hashes = append(hashes, i.VidDat)
	}
	if i.Link != nil && i.Link.Bild != "" {
		hashes = append(hashes, i.Link.Bild)
	}
	for _, h := range hashes {
		id := "feedmedia:" + h
		rec, _ := s.store.Get(storage.RecordFeedMedia, id)
		var liste []any
		if rec != nil && rec.Data != nil {
			liste, _ = rec.Data["posts"].([]any)
		}
		schon := false
		for _, x := range liste {
			if x == beitrag {
				schon = true
				break
			}
		}
		if schon {
			continue
		}
		liste = append(liste, beitrag)
		_ = s.store.Put(&storage.Record{ID: id, Type: storage.RecordFeedMedia,
			Data: map[string]any{"posts": liste}, UpdatedAt: time.Now().UTC()})
	}
}

// feedMedienFreigeben streicht einen Beitrag aus der Verweisliste und
// entfernt Dateien, die danach niemand mehr nutzt.
func (s *Server) feedMedienFreigeben(beitrag string) int {
	alle, err := s.store.List(storage.RecordFeedMedia)
	if err != nil || len(alle) == 0 {
		return 0
	}
	var inserate map[string]bool
	entfernt := 0
	for _, rec := range alle {
		if rec == nil || rec.DeletedAt != nil || rec.Data == nil || !strings.HasPrefix(rec.ID, "feedmedia:") {
			continue
		}
		liste, _ := rec.Data["posts"].([]any)
		var rest []any
		betroffen := false
		for _, x := range liste {
			if x == beitrag {
				betroffen = true
				continue
			}
			rest = append(rest, x)
		}
		if !betroffen {
			continue
		}
		hash := strings.TrimPrefix(rec.ID, "feedmedia:")
		if len(rest) > 0 {
			rec.Data["posts"] = rest
			rec.UpdatedAt = time.Now().UTC()
			_ = s.store.Put(rec)
			continue
		}
		_ = s.store.HardDelete(storage.RecordFeedMedia, rec.ID)
		if s.fileStore == nil {
			continue
		}
		if inserate == nil {
			inserate = s.feedInseratsDateien()
		}
		if inserate[hash] {
			continue // ein Inserat nutzt dieselbe Datei
		}
		if !strings.HasPrefix(s.fileStore.NameForHash(hash), "feed-") {
			continue // unter "Dateien" abgelegt oder unbekannt – nicht anfassen
		}
		s.fileStore.RemoveFromIndex(hash)
		entfernt++
	}
	return entfernt
}

func (s *Server) feedInseratsDateien() map[string]bool {
	out := map[string]bool{}
	alle, _ := s.store.List(storage.RecordListing)
	for _, rec := range alle {
		if rec == nil || rec.DeletedAt != nil || rec.Data == nil {
			continue
		}
		for _, feld := range []string{"image_hashes", "video_hashes"} {
			if l, ok := rec.Data[feld].([]any); ok {
				for _, h := range l {
					if hs, _ := h.(string); hs != "" {
						out[hs] = true
					}
				}
			}
		}
	}
	return out
}

var (
	feedLoeschMu      sync.Mutex
	feedLoeschErledigt = map[string]bool{}
)

// feedLoeschmarkenVerarbeiten setzt alle gültigen Löschmarken um. Wiederholt
// aufrufbar: Erledigte Marken werden übersprungen, und ein bereits
// entfernter Beitrag hinterlässt nichts mehr zu tun.
func (s *Server) feedLoeschmarkenVerarbeiten(reaktIdx map[string][]*storage.Record) {
	defer func() { _ = recover() }()
	for beitragID, reakt := range reaktIdx {
		autorBeitrag := ""
		var beitrag *storage.Record
		for _, m := range reakt {
			if art, _ := m.Data["kind"].(string); art != "del" {
				continue
			}
			schluessel := m.ID + "@" + time.Unix(zahl64(m.Data["ts"]), 0).UTC().Format(time.RFC3339)
			feedLoeschMu.Lock()
			erledigt := feedLoeschErledigt[schluessel]
			feedLoeschMu.Unlock()
			if erledigt {
				continue
			}
			if beitrag == nil {
				beitrag, _ = s.store.Get(storage.RecordPost, beitragID)
				if beitrag != nil && beitrag.Data != nil {
					autorBeitrag, _ = beitrag.Data["fid"].(string)
				} else if notiz, _ := s.store.Get(storage.RecordFeedMedia, "feedgeloescht:"+beitragID); notiz != nil && notiz.Data != nil {
					// Schon entfernt: Der Autor steht in der lokalen Notiz. So
					// werden auch Kommentare entfernt, die ein Nachzügler-Node
					// erst nach der Löschung geschickt hat.
					autorBeitrag, _ = notiz.Data["fid"].(string)
				}
			}
			fid, _ := m.Data["fid"].(string)
			ziel, _ := m.Data["target"].(string)
			if ziel == "" {
				// Beitrag löschen – nur durch dessen Autor. Ohne den Beitrag
				// selbst lässt sich der Autor nicht prüfen: dann nichts tun.
				if autorBeitrag == "" || fid != autorBeitrag {
					continue
				}
				s.feedBeitragEntfernen(beitragID, autorBeitrag, reakt)
				continue // nicht als erledigt merken: Nachzügler sollen beim nächsten Lauf mit
			} else {
				// Kommentar löschen – nur durch dessen Autor.
				for _, k := range reakt {
					if k.ID != ziel {
						continue
					}
					if art, _ := k.Data["kind"].(string); art != "comment" {
						break
					}
					if kf, _ := k.Data["fid"].(string); kf != fid {
						break
					}
					_ = s.store.Delete(storage.RecordPostReact, k.ID)
					for _, e := range reakt {
						if art, _ := e.Data["kind"].(string); art == "cedit" {
							if t, _ := e.Data["target"].(string); t == ziel {
								_ = s.store.Delete(storage.RecordPostReact, e.ID)
							}
						}
					}
					break
				}
			}
			feedLoeschMu.Lock()
			if len(feedLoeschErledigt) > 50000 {
				feedLoeschErledigt = map[string]bool{}
			}
			feedLoeschErledigt[schluessel] = true
			feedLoeschMu.Unlock()
		}
	}
}

// feedBeitragEntfernen entfernt die lokale Kopie eines Beitrags vollständig.
func (s *Server) feedBeitragEntfernen(beitragID, autor string, reakt []*storage.Record) {
	erstmals := false
	if rec, err := s.store.Get(storage.RecordPost, beitragID); err == nil && rec != nil && rec.DeletedAt == nil {
		_ = s.store.Delete(storage.RecordPost, beitragID)
		erstmals = true
		_ = s.store.Put(&storage.Record{ID: "feedgeloescht:" + beitragID, Type: storage.RecordFeedMedia,
			Data: map[string]any{"fid": autor}, UpdatedAt: time.Now().UTC()})
	}
	n := 0
	for _, x := range reakt {
		if art, _ := x.Data["kind"].(string); art == "del" {
			continue // die Löschmarke bleibt – sie trägt die Löschung weiter
		}
		_ = s.store.Delete(storage.RecordPostReact, x.ID)
		n++
	}
	if !erstmals && n == 0 {
		return // nichts mehr zu tun – der übliche Fall bei jedem weiteren Lauf
	}
	dateien := 0
	if erstmals {
		dateien = s.feedMedienFreigeben(beitragID)
	}
	if s.log != nil {
		s.log.Info("Feed-Beitrag entfernt", zap.String("id", beitragID),
			zap.Int("reaktionen", n), zap.Int("dateien", dateien))
	}
}

// feedAufraeumen setzt Löschmarken auch dann um, wenn niemand den Feed öffnet.
func (s *Server) feedAufraeumen() {
	defer func() { _ = recover() }()
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.feedLoeschmarkenVerarbeiten(s.feedReaktionenIndex())
	}
}
