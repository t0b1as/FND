package api

// Angebote bei den Nachbarn erfragen (R793).
//
// getListing las bisher nur den eigenen Speicher. Mit der Lastverteilung (R671)
// landen Anlegen, Übersicht und Detailseite aber oft auf verschiedenen Nodes im
// Heimnetz, und der Abgleich zwischen ihnen braucht Sekunden. Kam die
// Detailseite zu früh, fand ihr Node das gerade angelegte Angebot noch nicht –
// gemeldet als "404 bei einem neuen Artikel, in der Übersicht steht er aber".
//
// Dasselbe Muster wie für das Schlüsselverzeichnis (R687): lokal fehlt etwas,
// also bei den Nachbarn nachfragen. PutSynced prüft dabei die Signatur des
// Besitzer-Nodes, ein Nachbar kann also kein gefälschtes Angebot unterschieben.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/fundus/node/internal/storage"
	"go.uber.org/zap"
)

const listingAskProtocol = "/fundus/listing-ask/1.0.0"

// registerListingAsk beantwortet Anfragen nach einem Angebot.
func (s *Server) registerListingAsk() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(listingAskProtocol, func(peerID string, data []byte) []byte {
		var frage struct {
			ID string `json:"id"`
		}
		leer := []byte(`{"found":false}`)
		if json.Unmarshal(data, &frage) != nil {
			return leer
		}
		id := strings.TrimSpace(frage.ID)
		if id == "" || len(id) > 200 {
			return leer
		}
		rec, err := s.store.Get(storage.RecordListing, id)
		if err != nil || rec == nil || rec.DeletedAt != nil {
			return leer
		}
		// Vollständig samt Signatur – die Gegenseite prüft sie selbst.
		raw, err := json.Marshal(map[string]any{"found": true, "record": rec})
		if err != nil {
			return leer
		}
		return raw
	})
}

// listingHolen fragt die Nachbarn nach einem Angebot und legt es ab.
//
// Insgesamt höchstens sechs Sekunden: Die Detailseite soll nicht minutenlang
// hängen, nur weil ein Nachbar nicht antwortet.
func (s *Server) listingHolen(ctx context.Context, id string) *storage.Record {
	if s.node == nil || s.store == nil || id == "" {
		return nil
	}
	frage, _ := json.Marshal(map[string]string{"id": id})
	gesamt, abbrGesamt := context.WithTimeout(ctx, 6*time.Second)
	defer abbrGesamt()

	for _, p := range s.node.Peers() {
		if gesamt.Err() != nil {
			break
		}
		c2, abbr := context.WithTimeout(gesamt, 2*time.Second)
		antwort, err := s.node.SendAndReceive(c2, p.String(), listingAskProtocol, frage)
		abbr()
		if err != nil {
			continue
		}
		var res struct {
			Found  bool            `json:"found"`
			Record *storage.Record `json:"record"`
		}
		if json.Unmarshal(antwort, &res) != nil || !res.Found || res.Record == nil {
			continue
		}
		if res.Record.ID != id || res.Record.Type != storage.RecordListing {
			continue // nicht das, wonach gefragt wurde
		}
		// PutSynced prüft die Signatur und lehnt Untergeschobenes ab.
		if err := s.store.PutSynced(res.Record); err != nil {
			if s.log != nil {
				s.log.Warn("Angebot vom Nachbarn abgelehnt",
					zap.String("id", id), zap.Error(err))
			}
			continue
		}
		if s.log != nil {
			s.log.Info("Angebot bei Nachbarn erfragt", zap.String("id", id))
		}
		return res.Record
	}
	return nil
}
