package api

// Angebote von einem ANDEREN eigenen Node bearbeiten (R611).
//
// Ein Angebot gehört dem Node, auf dem es angelegt wurde: Er ist als Besitzer
// eingetragen und signiert den Datensatz. Ein fremder Node kann ihn deshalb
// nicht gültig ändern – seine Fassung würde beim Abgleich verworfen.
//
// Deshalb schickt der Node, an dem der Verkäufer gerade angemeldet ist, die
// Änderung an den Besitzer-Node. Dieser prüft die UNTERSCHRIFT des Erstellers
// (Ed25519 der Identität, öffentlicher Schlüssel aus dem Verzeichnis),
// übernimmt die Felder, signiert neu und verteilt das Angebot.
//
// Damit kann niemand fremde Angebote ändern: Ohne den privaten Schlüssel des
// Erstellers scheitert die Prüfung, und abgelaufene Nachrichten werden
// ebenfalls abgewiesen.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
	"go.uber.org/zap"
)

const listingEditProtocol = "/fundus/listing-edit/1.0.0"

// maxEditAge begrenzt, wie alt eine Änderungsnachricht sein darf.
const maxEditAge = 5 * time.Minute

type listingEditMsg struct {
	ListingID string         `json:"listing_id"`
	FID       string         `json:"fid"`
	TS        int64          `json:"ts"`
	Data      map[string]any `json:"data"`
	Sig       string         `json:"sig"`
	PubKey    string         `json:"pubkey"`
}

// editSigningBytes bildet die zu unterschreibenden Bytes – stabil sortiert,
// damit beide Seiten exakt dasselbe berechnen.
func editSigningBytes(m *listingEditMsg) []byte {
	raw, _ := json.Marshal(m.Data) // Go sortiert map-Schlüssel beim Marshal
	return []byte(strings.ToLower(m.ListingID) + "|" + strings.ToLower(m.FID) + "|" +
		fmt.Sprint(m.TS) + "|" + string(raw))
}

// forwardListingEdit schickt die Änderung an den Besitzer-Node des Angebots.
func (s *Server) forwardListingEdit(c *gin.Context, rec *storage.Record, body map[string]any) error {
	if s.node == nil {
		return fmt.Errorf("kein Netzwerk")
	}
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		return fmt.Errorf("nicht angemeldet")
	}
	stripCreatorFields(body) // Ersteller und Besitzer sind nicht änderbar
	msg := &listingEditMsg{
		ListingID: rec.ID,
		FID:       strings.ToLower(sess.identity.FundusID),
		TS:        time.Now().Unix(),
		Data:      body,
		PubKey:    sess.identity.PublicKeyHex,
	}
	sig, err := sess.identity.Sign(editSigningBytes(msg))
	if err != nil {
		return fmt.Errorf("Unterschrift fehlgeschlagen: %w", err)
	}
	msg.Sig = sig
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	resp, err := s.node.SendAndReceive(ctx, rec.OwnerID, listingEditProtocol, payload)
	if err != nil {
		return err
	}
	var antwort struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(resp, &antwort) == nil && !antwort.OK {
		if antwort.Error != "" {
			return fmt.Errorf("%s", antwort.Error)
		}
		return fmt.Errorf("abgelehnt")
	}
	return nil
}

// registerListingEditProtocol nimmt Änderungen anderer Nodes entgegen.
func (s *Server) registerListingEditProtocol() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(listingEditProtocol, func(peerID string, data []byte) []byte {
		fehler := func(grund string) []byte {
			raw, _ := json.Marshal(map[string]any{"ok": false, "error": grund})
			return raw
		}
		var m listingEditMsg
		if json.Unmarshal(data, &m) != nil || m.ListingID == "" || m.FID == "" || m.Sig == "" {
			return fehler("unvollständig")
		}
		if d := time.Since(time.Unix(m.TS, 0)); d > maxEditAge || d < -maxEditAge {
			return fehler("Nachricht zu alt")
		}
		rec, err := s.store.Get(storage.RecordListing, m.ListingID)
		if err != nil || rec == nil || rec.Data == nil {
			return fehler("Angebot nicht gefunden")
		}
		if rec.OwnerID != s.nodeID() {
			return fehler("dieses Angebot gehört einem anderen Node")
		}
		// Nur der Ersteller darf ändern.
		creator, _ := rec.Data["creator_fid"].(string)
		if strings.ToLower(strings.TrimSpace(creator)) != strings.ToLower(m.FID) {
			return fehler("nur der Ersteller darf bearbeiten")
		}
		// Öffentlichen Schlüssel bevorzugt aus dem VERZEICHNIS nehmen – nicht
		// aus der Nachricht, die ja jeder schicken könnte.
		pub := ""
		if kd, e := s.store.Get(storage.RecordKeyDir, "keydir:"+strings.ToLower(m.FID)); e == nil && kd != nil && kd.Data != nil {
			pub, _ = kd.Data["ed25519"].(string)
		}
		if pub == "" {
			pub = m.PubKey // Rückfall: Identity.Verify prüft den Schlüssel gegen die Fundus-ID
		}
		if !identity.Verify(editSigningBytes(&m), m.Sig, m.FID, pub) {
			return fehler("Unterschrift ungültig")
		}
		// Löschwunsch (R618): Der Besitzer-Node erzeugt den Löschvermerk selbst,
		// signiert ihn und verteilt ihn – nur so nehmen ihn die anderen an.
		if del, _ := m.Data["__delete__"].(bool); del {
			if e := s.store.Delete(storage.RecordListing, m.ListingID); e != nil {
				return fehler("löschen fehlgeschlagen")
			}
			// Signierten Löschvermerk verteilen – ohne ihn bliebe das Angebot
			// auf allen anderen Nodes bestehen (gleicher Weg wie beim lokalen
			// Löschen).
			now := time.Now()
			tomb := &storage.Record{
				ID: rec.ID, Type: storage.RecordListing, OwnerID: rec.OwnerID,
				CreatedAt: rec.CreatedAt, UpdatedAt: now, DeletedAt: &now, Data: rec.Data,
			}
			if sig, e := s.node.SignData(tomb.SigningBytes()); e == nil {
				tomb.Signature = sig
			}
			_ = s.store.PutSynced(tomb)
			if raw, e := json.Marshal(tomb); e == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = s.node.Publish(ctx, p2p.TopicListings, raw)
				cancel()
			}
			if s.log != nil {
				s.log.Info("Angebot von anderem Node gelöscht",
					zap.String("listing", m.ListingID), zap.String("peer", peerID))
			}
			raw, _ := json.Marshal(map[string]any{"ok": true})
			return raw
		}

		// Änderungen übernehmen (dieselben Regeln wie beim lokalen Bearbeiten).
		body := m.Data
		if body == nil {
			body = map[string]any{}
		}
		stripCreatorFields(body)
		s.applyListingUpdate(rec, body)
		rec.UpdatedAt = time.Now()
		if sig, e := s.node.SignData(rec.SigningBytes()); e == nil {
			rec.Signature = sig
		}
		if e := s.store.Put(rec); e != nil {
			return fehler("speichern fehlgeschlagen")
		}
		if raw, e := json.Marshal(rec); e == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = s.node.Publish(ctx, p2p.TopicListings, raw)
			cancel()
		}
		if s.log != nil {
			s.log.Info("Angebot von anderem Node bearbeitet",
				zap.String("listing", m.ListingID), zap.String("peer", peerID))
		}
		raw, _ := json.Marshal(map[string]any{"ok": true})
		return raw
	})
}
