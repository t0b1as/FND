package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/meter"
)

// registerMeterRoutes hängt die Smartmeter-Routen ein.
func (s *Server) registerMeterRoutes() {
	m := s.router.Group("/api/v1/meter")
	{
		m.GET("/status",  s.getMeterStatus)
		m.GET("/stream",  s.streamMeterReadings)
		m.POST("/reading", s.ingestReading)
		m.GET("/detect",  s.detectMeter) // ttyUSB Auto-Detection
	}
}

// detectMeter scannt alle seriellen Ports und gibt verfügbare Smartmeter zurück.
//
// GET /api/v1/meter/detect
// Antwort: { ports: [...], detected: { port, baud, protocol } | null }
func (s *Server) detectMeter(c *gin.Context) {
	available := meter.DetectSerialPorts()

	// Schnell-Probe: nur verfügbare Ports zurückgeben ohne langes Warten
	quickProbe := c.Query("probe") == "1"

	result := gin.H{
		"ports":     available,
		"detected":  nil,
		"hint":      "Füge ?probe=1 hinzu für vollständigen Port-Scan (dauert bis 30s)",
	}

	if quickProbe && len(available) > 0 {
		port, baud, proto := meter.AutoDetectMeter(s.log)
		if port != "" {
			result["detected"] = gin.H{
				"port":     port,
				"baud":     baud,
				"protocol": proto,
				"env_hint": "FUNDUS_METER_PORT=" + port + " FUNDUS_METER_BAUD=" + fmt.Sprintf("%d", baud) + " FUNDUS_METER_PROTOCOL=" + proto,
			}
		} else {
			result["detected"] = nil
			result["warning"] = "Kein Smartmeter erkannt. Lesekopf angeschlossen und am Zähler aktiviert?"
		}
	}

	c.JSON(http.StatusOK, result)
}

// getMeterStatus gibt Konfiguration und letztes Reading zurück.
func (s *Server) getMeterStatus(c *gin.Context) {
	if s.meterTokens == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false,
			"reason":  "Kein Smartmeter konfiguriert (FUNDUS_METER_PROTOCOL fehlt)",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":  true,
		"protocol": s.cfg.MeterProtocol,
		"meter_id": s.cfg.MeterID,
		"lat":      s.cfg.MeterLat,
		"lon":      s.cfg.MeterLon,
	})
}

// streamMeterReadings sendet Token als Server-Sent Events (SSE).
// Das Lua-Frontend kann mit EventSource("/api/v1/meter/stream") live mitlesen.
func (s *Server) streamMeterReadings(c *gin.Context) {
	if s.meterTokens == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein Meter-Stream"})
		return
	}

	c.Header("Content-Type",      "text/event-stream")
	c.Header("Cache-Control",     "no-cache")
	c.Header("Connection",        "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	clientGone := c.Request.Context().Done()
	// R828: eigenes Abo statt des Leser-Kanals. Vorher lasen Live-Anzeige und
	// main.go denselben Kanal, und jede Ablesung ging nur an einen von beiden.
	abo := ablesungAbonnieren()
	defer ablesungAbbestellen(abo)

	for {
		select {
		case <-clientGone:
			return
		case tok, ok := <-abo:
			if !ok {
				return
			}
			line := fmt.Sprintf(
				"data: {\"timestamp\":%q,\"meter_id\":%q,\"kwh\":%.4f,\"kwh_feed\":%.4f,\"watt\":%.1f}\n\n",
				tok.Timestamp.UTC().Format(time.RFC3339),
				tok.MeterID, tok.KWh, tok.KWhFeed, tok.WattNow, // R842: Einspeisung mit
			)
			c.Writer.WriteString(line)
			c.Writer.Flush()
		}
	}
}

// ingestReading nimmt ein einzelnes Reading entgegen, speichert es als
// Energie-Token und veröffentlicht es im P2P-Netz.
// Nützlich wenn ein externes Skript (z.B. Python auf dem Pi) den Zähler
// ausliest und per HTTP einspielt.
func (s *Server) ingestReading(c *gin.Context) {
	var tok meter.Token
	if err := c.ShouldBindJSON(&tok); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if tok.Timestamp.IsZero() {
		tok.Timestamp = time.Now().UTC()
	}
	if tok.MeterID == "" {
		tok.MeterID = s.cfg.MeterID
	}
	// R828: Von außen eingespielte Ablesungen bleiben lokal wie die eigenen.
	// Eine mitgeschickte Gerätesignatur wird NICHT übernommen – der Node kann
	// nicht prüfen, woher sie stammt. Solche Ablesungen werden nie zertifiziert.
	tok.GeraetPub, tok.GeraetSig = "", ""
	AblesungSpeichern(s.store, tok)
	s.log.Info("Ablesung von außen übernommen",
		zap.String("meter", tok.MeterID), zap.Float64("kwh", tok.KWh), zap.Float64("watt", tok.WattNow))
	c.JSON(http.StatusCreated, gin.H{"ok": true})
}
