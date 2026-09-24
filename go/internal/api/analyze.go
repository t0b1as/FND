package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/llm"
)

// maxImageSize: 8 MB pro Bild (JPEG vom Handy ist selten größer)
const maxImageSize = 8 << 20

// registerAnalyzeRoutes fügt die LLM-Analyse-Routen hinzu.
// Wird in server.go aufgerufen wenn LLM aktiviert ist.
func (s *Server) registerAnalyzeRoutes() {
	s.router.POST("/api/v1/analyze", s.handleAnalyze)
	s.router.GET("/api/v1/analyze/status", s.handleAnalyzeStatus)
}

// handleAnalyze empfängt Multipart-Upload (Bilder + Sprach-Transkript)
// und gibt das LLM-Analyse-Ergebnis zurück.
//
// Erwartetes Multipart-Formular:
//   - images[]   : ein oder mehrere Bild-Dateien (JPEG / PNG)
//   - voice_text : Sprach-Transkript (String, optional)
//   - language   : "de" oder "en" (optional, Standard: "de")
func (s *Server) handleAnalyze(c *gin.Context) {
	if s.analyzer == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "LLM-Analyse nicht aktiviert (FUNDUS_LLM_ENABLED=false)",
		})
		return
	}

	// Multipart-Parser: max 4 Bilder × 8 MB
	if err := c.Request.ParseMultipartForm(4 * maxImageSize); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Multipart-Parse-Fehler: " + err.Error()})
		return
	}

	// Bilder einlesen
	form := c.Request.MultipartForm
	imageFiles := form.File["images[]"]
	if len(imageFiles) == 0 {
		// Fallback: einzelnes Feld ohne Array-Notation
		imageFiles = form.File["images"]
	}

	images := make([][]byte, 0, len(imageFiles))
	for _, fh := range imageFiles {
		if fh.Size > maxImageSize {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("Bild '%s' zu groß (%d MB, max 8 MB)",
					fh.Filename, fh.Size>>20),
			})
			return
		}

		// MIME-Typ prüfen
		ct := fh.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "image/") && ct != "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("Ungültiger Dateityp für '%s': %s", fh.Filename, ct),
			})
			return
		}

		f, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Datei konnte nicht geöffnet werden"})
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, maxImageSize))
		f.Close()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Lesefehler"})
			return
		}
		images = append(images, data)
	}

	voiceText := c.PostForm("voice_text")
	language  := c.DefaultPostForm("language", "de")

	if len(images) == 0 && strings.TrimSpace(voiceText) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Mindestens ein Bild oder Sprachtext erforderlich",
		})
		return
	}

	s.log.Info("Analyze request received",
		zap.Int("images", len(images)),
		zap.Int("voiceLen", len(voiceText)),
		zap.String("lang", language),
	)

	result, err := s.analyzer.Analyze(c.Request.Context(), llm.AnalyzeRequest{
		Images:    images,
		VoiceText: voiceText,
		Language:  language,
	})
	if err != nil {
		s.log.Error("LLM analysis failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Analyse fehlgeschlagen: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, result)
}

// handleAnalyzeStatus gibt den Status des LLM-Dienstes zurück.
func (s *Server) handleAnalyzeStatus(c *gin.Context) {
	if s.analyzer == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false,
			"reason":  "FUNDUS_LLM_ENABLED nicht gesetzt",
		})
		return
	}

	pingCtx, cancel := c.Request.Context(), func() {}
	_ = cancel

	if err := s.analyzer.Ping(pingCtx); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled":   true,
			"reachable": false,
			"error":     err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"enabled":   true,
		"reachable": true,
	})
}
