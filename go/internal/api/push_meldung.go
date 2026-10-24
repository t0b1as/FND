package api

// Allgemeine Push-Meldungen über alle Nodes eines Kontos (R795).
//
// Push-Anmeldungen liegen nur auf dem Node, auf dem man sie erteilt hat. Mit
// der Lastverteilung (R671) muss das nicht der Node sein, auf dem etwas
// passiert – etwa der Node, dem ein verkauftes Angebot gehört. Ein schlichtes
// pushNotify dort fand dann keine Anmeldung und meldete niemandem.
//
// Für Anrufe gab es die Verteilung schon (R689–R691, push_remote.go), fest auf
// Anrufe zugeschnitten. Das hier ist dasselbe Muster für beliebige Meldungen –
// mit EIGENEM Protokoll statt das alte umzudeuten: Ein älterer Node hätte eine
// Verkaufsmeldung sonst als "Anruf von …" angezeigt. So antwortet er nicht.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const pushMeldungProtocol = "/fundus/push-meldung/1.0.0"

type pushMeldungMsg struct {
	FID   string `json:"fid"`
	Titel string `json:"titel"`
	Text  string `json:"text"`
	Link  string `json:"link"`
	Tag   string `json:"tag"`
	TS    int64  `json:"ts"`
}

var (
	pushMeldungMu    sync.Mutex
	pushMeldungLetzt = map[string]time.Time{}
)

// registerPushMeldung nimmt Meldewünsche entgegen und stellt sie lokal zu –
// AUSSCHLIESSLICH an Geräte, die sich hier angemeldet haben.
func (s *Server) registerPushMeldung() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(pushMeldungProtocol, func(peerID string, data []byte) []byte {
		ok := func(v bool) []byte {
			raw, _ := json.Marshal(map[string]any{"ok": v})
			return raw
		}
		var m pushMeldungMsg
		if json.Unmarshal(data, &m) != nil || m.FID == "" || m.Titel == "" {
			return ok(false)
		}
		if d := time.Since(time.Unix(m.TS, 0)); d > 5*time.Minute || d < -time.Minute {
			return ok(false) // veraltet
		}
		// Längen begrenzen: Die Meldung erscheint auf dem Sperrbildschirm.
		if len(m.Titel) > 80 || len(m.Text) > 300 || len(m.Link) > 200 || len(m.Tag) > 80 ||
			(m.Link != "" && !strings.HasPrefix(m.Link, "/")) {
			return ok(false) // nur Verweise innerhalb der eigenen Oberfläche
		}
		fid := strings.ToLower(m.FID)
		if !s.hatPushAnmeldung(fid) {
			return ok(false)
		}
		// Dieselbe Meldung (gleiches Etikett) nicht doppelt binnen 30 Sekunden –
		// sie kann über mehrere Wege eintreffen.
		schluessel := fid + "|" + m.Tag
		pushMeldungMu.Lock()
		if letzt, da := pushMeldungLetzt[schluessel]; da && time.Since(letzt) < 30*time.Second {
			pushMeldungMu.Unlock()
			return ok(false)
		}
		pushMeldungLetzt[schluessel] = time.Now()
		pushMeldungMu.Unlock()

		s.pushNotify(fid, m.Titel, m.Text, m.Link, m.Tag, true)
		if s.log != nil {
			s.log.Info("Meldung zugestellt", zap.String("fuer", fid), zap.String("titel", m.Titel))
		}
		return ok(true)
	})
}

// meldungVerteilen meldet an ALLE Nodes des Kontos, nicht nur an diesen.
func (s *Server) meldungVerteilen(fid, titel, text, link, tag string) {
	fid = strings.ToLower(strings.TrimSpace(fid))
	if fid == "" {
		return
	}
	// Zuerst hier – liegt die Anmeldung auf diesem Node, ist das der kürzeste Weg.
	if s.hatPushAnmeldung(fid) {
		pushMeldungMu.Lock()
		pushMeldungLetzt[fid+"|"+tag] = time.Now()
		pushMeldungMu.Unlock()
		s.pushNotify(fid, titel, text, link, tag, true)
	}
	if s.node == nil {
		return
	}
	ziele := s.meldeZiele(fid)
	payload, err := json.Marshal(pushMeldungMsg{
		FID: fid, Titel: titel, Text: text, Link: link, Tag: tag, TS: time.Now().Unix(),
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
				_, _ = s.node.SendAndReceive(ctx, p, pushMeldungProtocol, payload)
			}(pid)
		}
	}()
}


// meldeZiele: Geräteliste des Kontos UND alle direkten Nachbarn (R796).
//
// Bisher wurden die Nachbarn nur gefragt, wenn die Geräteliste LEER war. Die
// Liste wird aber auf dem Node gelesen, auf dem etwas passiert – und dessen
// Kopie kann veraltet sein: Wer sich gerade auf einem weiteren Node angemeldet
// hat, steht dort womöglich noch nicht drin. Die Liste ist dann nicht leer,
// aber unvollständig, und genau dieser Node wurde nie gefragt.
//
// Beide zusammen zu fragen ist unbedenklich: Zugestellt wird nur dort, wo eine
// Push-Anmeldung für GENAU dieses Konto besteht. Ein Node ohne Anmeldung
// verwirft die Bitte, ohne etwas zu tun.
func (s *Server) meldeZiele(fid string) []string {
	ziele := s.eigeneNodes(fid)
	if s.node == nil {
		return ziele
	}
	gesehen := map[string]bool{s.node.ID().String(): true}
	for _, z := range ziele {
		gesehen[z] = true
	}
	for _, p := range s.node.Peers() {
		if id := p.String(); !gesehen[id] {
			gesehen[id] = true
			ziele = append(ziele, id)
		}
	}
	return ziele
}
