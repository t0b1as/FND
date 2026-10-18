package api

// Fehlenden Schlüssel im Netz erfragen (R687).
//
// Ein Anruf wird verschlüsselt zugestellt, dafür braucht der Anrufer den
// öffentlichen Schlüssel des Empfängers aus dem Schlüsselverzeichnis. Bisher
// wurde NUR im eigenen Bestand nachgesehen. Fehlte der Eintrag dort, scheiterte
// der Anruf – und zwar nur in eine Richtung, nämlich von dem Node aus, dem der
// Eintrag fehlt. Genau das Bild: vom 39er zum 71er geht es, andersherum nicht.
//
// Die Verzeichniseinträge werden zwar im Netz verteilt, aber ein Node, der
// später dazukommt oder einen Abgleich verpasst hat, hat sie eben nicht. Statt
// auf den nächsten Abgleich zu warten, fragt er jetzt direkt nach.
//
// Sicherheit: Der erhaltene Eintrag wird über PutSynced abgelegt, und das prüft
// die Signatur des Besitzer-Nodes. Ein Nachbar kann also keinen falschen
// Schlüssel unterschieben – er könnte nur einen vorenthalten, und dagegen hilft,
// dass mehrere Nachbarn gefragt werden.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/storage"
)

const keyDirAskProtocol = "/fundus/keydir-ask/1.0.0"

// registerKeyDirAsk beantwortet Anfragen nach einem Verzeichniseintrag.
func (s *Server) registerKeyDirAsk() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(keyDirAskProtocol, func(peerID string, data []byte) []byte {
		var frage struct {
			FundusID string `json:"fundus_id"`
		}
		leer := []byte(`{"found":false}`)
		if json.Unmarshal(data, &frage) != nil {
			return leer
		}
		fid := strings.ToLower(strings.TrimSpace(frage.FundusID))
		if fid == "" {
			return leer
		}
		rec, err := s.store.Get(storage.RecordKeyDir, "keydir:"+fid)
		if err != nil || rec == nil {
			return leer
		}
		// Den vollständigen Datensatz samt Signatur weitergeben – die
		// Gegenseite prüft sie selbst.
		raw, err := json.Marshal(map[string]any{"found": true, "record": rec})
		if err != nil {
			return leer
		}
		return raw
	})
}

// keyDirHolen fragt die Nachbarn nach dem Verzeichniseintrag und legt ihn ab.
// Gibt den X25519-Schlüssel zurück, wenn einer gefunden wurde.
func (s *Server) keyDirHolen(ctx context.Context, fundusID string) string {
	if s.node == nil || s.store == nil {
		return ""
	}
	fid := strings.ToLower(strings.TrimSpace(fundusID))
	if fid == "" {
		return ""
	}
	frage, _ := json.Marshal(map[string]string{"fundus_id": fid})

	for _, p := range s.node.Peers() {
		c2, abbr := context.WithTimeout(ctx, 3*time.Second)
		antwort, err := s.node.SendAndReceive(c2, p.String(), keyDirAskProtocol, frage)
		abbr()
		if err != nil {
			continue
		}
		var res struct {
			Found  bool             `json:"found"`
			Record *storage.Record  `json:"record"`
		}
		if json.Unmarshal(antwort, &res) != nil || !res.Found || res.Record == nil {
			continue
		}
		// PutSynced prüft die Signatur des Besitzer-Nodes und lehnt einen
		// untergeschobenen Eintrag ab.
		if err := s.store.PutSynced(res.Record); err != nil {
			if s.log != nil {
				s.log.Warn("Verzeichniseintrag abgelehnt", zap.String("von", p.String()[:12]),
					zap.Error(err))
			}
			continue
		}
		x, _ := res.Record.Data["x25519"].(string)
		if x != "" {
			if s.log != nil {
				s.log.Info("Schlüssel im Netz erfragt",
					zap.String("fuer", fid[:10]+"…"), zap.String("von", p.String()[:12]))
			}
			return x
		}
	}
	return ""
}
