package api

// Vorschaubilder im Voraus erzeugen (R631).
//
// Bisher entstand ein Vorschaubild erst, wenn es jemand anforderte. Beim Öffnen
// des Marktplatzes bedeutete das: 16 Angebote, 16 Bilder, die ein Pi erst
// decodieren und verkleinern muss – bei 50-MP-Fotos nacheinander, weil große
// Bilder allein laufen. Dauert das länger als die Geduld des Betrachters,
// bleiben die Kacheln unscharf.
//
// Der Node, auf dem ein Angebot liegt, hat die Datei aber ohnehin. Er erzeugt
// das Vorschaubild deshalb direkt nach dem Anlegen oder Ändern – und einmal
// nachträglich für alle eigenen Angebote, die noch keins haben. Danach ist jede
// Anfrage reines Ausliefern, auch die von anderen Nodes (siehe thumb_p2p.go).

import (
	"bytes"
	"context"
	"time"

	"github.com/fundus/node/internal/storage"
	"go.uber.org/zap"
)

// pregenQueue nimmt Bild-Hashes auf, für die ein Vorschaubild fehlt.
// Gepuffert, damit das Anlegen eines Angebots nie darauf wartet.
var pregenQueue = make(chan string, 256)

// listingImageHashes liest die Bildverweise aus einem Angebots-Datensatz.
func listingImageHashes(d map[string]any) []string {
	out := make([]string, 0, 4)
	if d == nil {
		return out
	}
	if arr, ok := d["image_hashes"].([]any); ok {
		for _, v := range arr {
			if sv, ok := v.(string); ok && len(sv) == 64 {
				out = append(out, sv)
			}
		}
	}
	return out
}

// queueThumbs trägt fehlende Vorschaubilder zur Erzeugung ein (nie blockierend).
func (s *Server) queueThumbs(hashes []string) {
	if s.thumbs == nil || s.fileStore == nil {
		return
	}
	for _, h := range hashes {
		if s.thumbs.cached(h) != nil {
			continue // liegt schon vor
		}
		select {
		case pregenQueue <- h:
		default:
			return // Warteschlange voll: der nächste Durchlauf holt es nach
		}
	}
}

// thumbPregenLoop arbeitet die Warteschlange ab – eines nach dem anderen, mit
// Pause. Das Erzeugen darf den Node nie ausbremsen; es ist Vorarbeit, keine
// Anfrage, auf die jemand wartet.
func (s *Server) thumbPregenLoop() {
	defer func() {
		if r := recover(); r != nil {
			if s.log != nil {
				s.log.Error("Vorschaubild-Vorarbeit abgebrochen", zap.Any("grund", r))
			}
			time.Sleep(time.Minute)
			go s.thumbPregenLoop()
		}
	}()
	// Erst anlaufen lassen: Beim Start hat der Node genug zu tun.
	time.Sleep(90 * time.Second)
	go s.pregenOwnListings() // einmalig alle eigenen Angebote nachziehen

	for hash := range pregenQueue {
		if s.thumbs == nil || s.fileStore == nil || s.thumbs.cached(hash) != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err := s.thumbs.get(ctx, hash, func(c context.Context) ([]byte, error) {
			var buf bytes.Buffer
			if derr := s.fileStore.Download(c, hash, &buf); derr != nil {
				return nil, derr
			}
			return buf.Bytes(), nil
		})
		cancel()
		if s.log != nil {
			if err != nil {
				s.log.Debug("Vorschaubild nicht erzeugt", zap.String("hash", hash[:12]), zap.Error(err))
			} else {
				s.log.Info("Vorschaubild im Voraus erzeugt", zap.String("hash", hash[:12]))
			}
		}
		time.Sleep(2 * time.Second) // Luft für alles andere
	}
}

// pregenOwnListings zieht die Vorschaubilder aller EIGENEN Angebote nach.
// Nur eigene: Für fremde Angebote holt sich der Node das fertige Bild beim
// Besitzer, statt dessen Original durchs Netz zu ziehen.
func (s *Server) pregenOwnListings() {
	defer func() { _ = recover() }()
	if s.store == nil || s.thumbs == nil {
		return
	}
	recs, err := s.store.List(storage.RecordListing)
	if err != nil {
		return
	}
	me := s.nodeID()
	fehlend := 0
	for _, rec := range recs {
		if rec == nil || rec.DeletedAt != nil || rec.Data == nil || rec.OwnerID != me {
			continue
		}
		for _, h := range listingImageHashes(rec.Data) {
			if s.thumbs.cached(h) == nil {
				fehlend++
				select {
				case pregenQueue <- h:
				default:
				}
			}
		}
	}
	if s.log != nil && fehlend > 0 {
		s.log.Info("Vorschaubilder eigener Angebote werden nachgezogen", zap.Int("anzahl", fehlend))
	}
}
