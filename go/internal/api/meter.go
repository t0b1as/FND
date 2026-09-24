package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/meter"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
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

	for {
		select {
		case <-clientGone:
			return
		case tok, ok := <-s.meterTokens:
			if !ok {
				return
			}
			line := fmt.Sprintf(
				"data: {\"timestamp\":%q,\"meter_id\":%q,\"kwh\":%.4f,\"watt\":%.1f}\n\n",
				tok.Timestamp.UTC().Format(time.RFC3339),
				tok.MeterID, tok.KWh, tok.WattNow,
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

	record := &storage.Record{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		Type:      storage.RecordEnergy,
		OwnerID:   s.node.ID().String(),
		CreatedAt: time.Now(),
		Data: map[string]any{
			"timestamp":     tok.Timestamp,
			"meter_id":      tok.MeterID,
			"lat":           tok.Lat,
			"lon":           tok.Lon,
			"kwh":           tok.KWh,
			"watt_now":      tok.WattNow,
			"generator_lat": tok.GeneratorLat,
			"generator_lon": tok.GeneratorLon,
			"signature":     tok.Signature,
		},
	}

	if err := s.store.Put(record); err != nil {
		s.internalError(c, err)
		return
	}

	if data, err := marshalRecord(record); err == nil {
		_ = s.node.Publish(c.Request.Context(), p2p.TopicEnergy, data)
	}

	s.log.Info("Energy token ingested",
		zap.String("meter", tok.MeterID),
		zap.Float64("kwh", tok.KWh),
		zap.Float64("watt", tok.WattNow),
	)

	c.JSON(http.StatusCreated, record)
}
