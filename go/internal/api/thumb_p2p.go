package api

// Vorschaubilder zwischen Nodes austauschen (R627).
//
// Bisher holte sich jeder Node für ein Vorschaubild das VOLLSTÄNDIGE Original
// aus dem Netz – bei Handyfotos mehrere Megabyte je Bild. Beim Öffnen des
// Marktplatzes mit 16 Angeboten waren das schnell über 50 MB, die ein Pi
// nacheinander herunterladen und decodieren musste. Sichtbar wurden deshalb nur
// die ersten paar Bilder scharf.
//
// Jetzt fragt der Node das FERTIGE Vorschaubild beim Besitzer des Angebots an:
// rund 40 KB statt mehrerer Megabyte, und die Rechenarbeit macht der Node, der
// die Datei ohnehin hat. Erst wenn das nicht klappt, wird wie bisher das
// Original geholt.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"
)

const thumbProtocol = "/fundus/thumb/1.0.0"

type thumbRequest struct {
	Hash string `json:"hash"`
}

type thumbResponse struct {
	OK    bool   `json:"ok"`
	JPEG  []byte `json:"jpeg,omitempty"`
	Error string `json:"error,omitempty"`
}

// registerThumbProtocol beantwortet Anfragen anderer Nodes nach einem
// Vorschaubild. Geliefert wird nur das verkleinerte Bild, nie das Original.
func (s *Server) registerThumbProtocol() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(thumbProtocol, func(peerID string, data []byte) []byte {
		antwort := func(r thumbResponse) []byte {
			raw, _ := json.Marshal(r)
			return raw
		}
		var req thumbRequest
		if json.Unmarshal(data, &req) != nil || len(req.Hash) != 64 {
			return antwort(thumbResponse{Error: "ungültige Anfrage"})
		}
		if s.thumbs == nil || s.fileStore == nil {
			return antwort(thumbResponse{Error: "nicht verfügbar"})
		}
		hash := strings.ToLower(req.Hash)
		// Fertiges Bild? Dann sofort ausliefern.
		if cached := s.thumbs.cached(hash); cached != nil {
			return antwort(thumbResponse{OK: true, JPEG: cached})
		}
		// Sonst erzeugen. Die Frist ist knapp gehalten: Müsste dieser Node das
		// Original selbst erst aus dem Netz holen, brechen wir lieber ab –
		// sonst entstünde eine Kette von Downloads über mehrere Nodes.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		jpeg, err := s.thumbs.get(ctx, hash, func(ctx context.Context) ([]byte, error) {
			var buf bytes.Buffer
			if derr := s.fileStore.Download(ctx, hash, &buf); derr != nil {
				return nil, derr
			}
			return buf.Bytes(), nil
		})
		if err != nil {
			return antwort(thumbResponse{Error: err.Error()})
		}
		return antwort(thumbResponse{OK: true, JPEG: jpeg})
	})
}

// fetchThumbFromPeer holt das fertige Vorschaubild bei einem anderen Node.
func (s *Server) fetchThumbFromPeer(ctx context.Context, peerID, hash string) []byte {
	if s.node == nil || peerID == "" || peerID == s.nodeID() || len(hash) != 64 {
		return nil
	}
	body, err := json.Marshal(thumbRequest{Hash: strings.ToLower(hash)})
	if err != nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := s.node.SendAndReceive(rctx, peerID, thumbProtocol, body)
	if err != nil {
		return nil
	}
	var resp thumbResponse
	if json.Unmarshal(raw, &resp) != nil || !resp.OK || len(resp.JPEG) == 0 {
		return nil
	}
	if s.log != nil {
		s.log.Debug("Vorschaubild von Peer erhalten",
			zap.String("peer", peerID), zap.Int("bytes", len(resp.JPEG)))
	}
	return resp.JPEG
}
