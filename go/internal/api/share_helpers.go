package api

// Gemeinsame Helfer rund um Freigaben (R777).
//
// Herausgelöst aus share_stream.go, als das Umpacken entfernt wurde. Diese drei
// werden weiterhin gebraucht:
//
//   port           – der eigene HTTP-Port, für ortseigene Adressen
//   urlQuery       – Kodierung eines Pfads als Abfragewert
//   probeShareFile – Codecs einer Datei ermitteln (ffprobe)
//
// probeShareFile hat derzeit keinen Aufrufer mehr in der Oberfläche, bleibt
// aber: Es ist die einzige Stelle, die ffprobe kapselt, und wird gebraucht,
// sobald wieder etwas über Medieninhalte entschieden werden soll.

import (
	"context"
	"encoding/json"
	"net/url"
	"os/exec"
	"time"
)

// shareProbe: was in einer Mediendatei steckt (R778).
//
// Lag in der gelöschten share_stream.go und ist hier mitgewandert, weil
// probeShareFile ihn als Rückgabetyp braucht.
type shareProbe struct {
	VCodec string
	ACodec string
}

// port liefert den HTTP-Port dieses Node (R718). Gebraucht, damit ffmpeg den
// ortseigenen Fernstrom als Eingang benutzen kann.
func (s *Server) port() int {
	if s.cfg != nil && s.cfg.Port > 0 {
		return s.cfg.Port
	}
	return 3000
}

// urlQuery kodiert einen Pfad für die Verwendung als Abfragewert.
func urlQuery(v string) string { return url.QueryEscape(v) }

// R777: shareProbeFile ist mitgegangen – die Route gibt es nicht mehr, und
// browserKannVideo lag in der gelöschten share_stream.go. probeShareFile selbst
// bleibt, es kapselt ffprobe.

// probeShareFile fragt ffprobe nach den Codecs der Datei.
func probeShareFile(ctx context.Context, pfad string) shareProbe {
	var p shareProbe
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ffprobe", "-v", "error",
		"-show_entries", "stream=codec_type,codec_name",
		"-of", "json", pfad).Output()
	if err != nil {
		return p
	}
	var r struct {
		Streams []struct {
			Type  string `json:"codec_type"`
			Codec string `json:"codec_name"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &r) != nil {
		return p
	}
	for _, st := range r.Streams {
		switch st.Type {
		case "video":
			if p.VCodec == "" {
				p.VCodec = st.Codec
			}
		case "audio":
			if p.ACodec == "" {
				p.ACodec = st.Codec
			}
		}
	}
	return p
}
