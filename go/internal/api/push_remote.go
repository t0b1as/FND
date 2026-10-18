package api

// Anrufmeldung über Node-Grenzen (R689).
//
// Bei Drive gab es dasselbe Problem (R646): Push-Anmeldungen und offene
// Verbindungen liegen NUR auf dem Node, auf dem sich jemand angemeldet hat. Für
// Anrufe kam bisher nur der Weg über das Signal selbst in Frage – und das
// erreicht einen Node nur, wenn dort gerade eine Sitzung des Empfängers läuft
// und das Signal auch ankommt. Ist die Sitzung nach einem Neustart weg oder
// steht die Verbindung nicht, erfährt das Gerät nichts. Genau deshalb klingelt
// es nur, wenn der Messenger offen ist.
//
// Deshalb fragt der Anrufer-Node jetzt bei ALLEN Nodes des Empfängers direkt
// nach einer Meldung – unabhängig von Sitzung und Signalweg. Das ist derselbe
// Mechanismus wie bei Drive, nur für Anrufe.
//
// Missbrauch ist eng begrenzt: Gemeldet wird ausschließlich an jemanden, für
// den dieser Node eine Push-Anmeldung hat, höchstens alle 20 Sekunden je
// Empfänger, und der Text ist fest vorgegeben – über die Leitung kommt nur, WER
// anruft und ob mit Video.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const pushAskProtocol = "/fundus/push-ask/1.0.0"

type pushAskMsg struct {
	FID      string `json:"fid"`       // wer benachrichtigt werden soll
	Von      string `json:"von"`       // Anzeigename des Anrufers
	Video    bool   `json:"video"`
	TS       int64  `json:"ts"`
}

var (
	pushAskMu    sync.Mutex
	pushAskLetzt = map[string]time.Time{}
)

// registerPushAsk nimmt Meldewünsche entgegen und stellt sie lokal zu.
func (s *Server) registerPushAsk() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(pushAskProtocol, func(peerID string, data []byte) []byte {
		ok := func(v bool) []byte {
			raw, _ := json.Marshal(map[string]any{"ok": v})
			return raw
		}
		var m pushAskMsg
		if json.Unmarshal(data, &m) != nil || m.FID == "" {
			return ok(false)
		}
		if d := time.Since(time.Unix(m.TS, 0)); d > 60*time.Second || d < -60*time.Second {
			return ok(false) // veraltet: ein Anruf von vor einer Minute klingelt nicht mehr
		}
		fid := strings.ToLower(m.FID)

		// Nur an Geräte, die sich HIER für Meldungen angemeldet haben.
		if !s.hatPushAnmeldung(fid) {
			return ok(false)
		}
		// Nicht öfter als alle 20 Sekunden je Empfänger.
		pushAskMu.Lock()
		if letzt, da := pushAskLetzt[fid]; da && time.Since(letzt) < 20*time.Second {
			pushAskMu.Unlock()
			return ok(false)
		}
		pushAskLetzt[fid] = time.Now()
		pushAskMu.Unlock()

		name := strings.TrimSpace(m.Von)
		if name == "" {
			name = "jemandem"
		}
		titel := "📞 Anruf"
		if m.Video {
			titel = "📹 Videoanruf"
		}
		s.pushNotify(fid, titel, "Anruf von "+name, "/messenger", "call", true)
		return ok(true)
	})
}

// hatPushAnmeldung: Liegt für dieses Konto hier eine Push-Anmeldung vor?
func (s *Server) hatPushAnmeldung(fid string) bool {
	ps := s.push()
	if ps == nil {
		return false
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.subs[strings.ToLower(fid)]) > 0
}

// anrufMeldungVerteilen bittet alle Nodes des Empfängers um eine Meldung.
// Läuft im Hintergrund – der Anruf selbst soll darauf nicht warten.
func (s *Server) anrufMeldungVerteilen(empfaenger, anruferName string, video bool) {
	if s.node == nil || empfaenger == "" {
		return
	}
	fid := strings.ToLower(strings.TrimSpace(empfaenger))
	ziele := s.eigeneNodes(fid) // Heim-Node und Geräteliste des Empfängers
	if len(ziele) == 0 {
		return
	}
	payload, err := json.Marshal(pushAskMsg{
		FID: fid, Von: anruferName, Video: video, TS: time.Now().Unix(),
	})
	if err != nil {
		return
	}
	go func() {
		for _, pid := range ziele {
			func(p string) {
				defer func() { _ = recover() }()
				ctx, abbr := context.WithTimeout(context.Background(), 6*time.Second)
				defer abbr()
				if _, err := s.node.SendAndReceive(ctx, p, pushAskProtocol, payload); err != nil && s.log != nil {
					s.log.Debug("Anrufmeldung nicht zustellbar",
						zap.String("peer", p), zap.Error(err))
				}
			}(pid)
		}
	}()
}
