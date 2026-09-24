package api

import (
	"mime"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/filestore"
)

// registerFileRoutes hängt die Filesharing-Endpunkte ein.
func (s *Server) registerFileRoutes() {
	// Routes IMMER registrieren, auch wenn fileStore (noch) nil ist.
	// Grund: NewServer ruft dies VOR WithFileStore auf. Die Handler haben
	// eigene nil-Checks und liefern dann sauberes JSON ({"enabled":false})
	// statt 404 (was die Lua-Seite als HTML missinterpretiert → "deaktiviert"
	// + "Unexpected token '<'" beim Upload).
	g := s.router.Group("/api/v1/files")
	{
		g.POST("/upload",        s.fileUpload)
		g.GET("/download/:hash", s.fileDownload)
		g.GET("/probe/:hash",    s.fileProbe)  // Codecs/Dauer (ffprobe)
		g.GET("/stream/:hash",   s.fileStream) // umverpackt als MP4 (ffmpeg)
		g.GET("/name/:hash",     s.fileName)            // Dateiname zum Hash (für Download)
		g.GET("/availability/:hash", s.fileAvailability)
		g.GET("/redundancy/:hash", s.fileRedundancy)
		g.GET("/thumb/:hash", s.fileThumbnail)
		g.GET("/info/:hash",     s.fileInfo)
		g.DELETE("/info/:hash",  s.fileDelete)
		g.GET("/stats",          s.fileStats)
		g.GET("/earnings",       s.fileEarnings)
		g.GET("/node-seed",      s.nodeSeedGet)
		g.POST("/node-seed/confirm", s.nodeSeedConfirm)
		g.POST("/node-seed/reveal",  s.nodeSeedReveal)
		g.POST("/node-seed/password", s.nodeSeedSetPassword)
		g.POST("/node-seed/recreate", s.nodeSeedRecreate)
		g.POST("/node-seed/encrypt",  s.nodeSeedEncrypt)
		g.POST("/node-seed/unlock",   s.nodeSeedUnlock)
		g.POST("/node-seed/decrypt",  s.nodeSeedDecrypt)
		g.GET("/list",           s.fileList)
		g.POST("/manifest",      s.createManifest)      // Ordner-Manifest erstellen
		g.GET("/manifest/:hash", s.getManifest)         // Manifest lesen (Navigation)
		g.GET("/browse",         s.browseLocalDir)      // lokale Ordner durchsuchen (Freigabe-Auswahl)
		// Resumefähiger Upload (grosse Dateien/Videos, disconnect-sicher)
		g.POST("/upload/begin",  s.fileUploadBegin)
		g.POST("/upload/block",  s.fileUploadBlock)
		g.POST("/upload/finish", s.fileUploadFinish)
		g.GET("/upload/status",  s.fileUploadStatus)
		g.POST("/upload/abort",  s.fileUploadAbort)
	}

	// Verzeichnis-Shares
	sh := s.router.Group("/api/v1/shares")
	{
		sh.GET("",                s.shareList)          // eigene Shares
		sh.POST("",               s.shareCreate)        // Share erstellen
		sh.GET("/:id",            s.shareGet)           // Share-Details
		sh.PUT("/:id",            s.shareUpdate)        // Berechtigungen ändern
		sh.DELETE("/:id",         s.shareDelete)        // Share löschen

		sh.GET("/:id/ls",         s.shareListDir)       // Verzeichnis listen
		sh.GET("/:id/ls/*path",   s.shareListDirPath)   // Unterverzeichnis
		sh.GET("/:id/dl/*path",   s.shareDownload)      // Datei herunterladen
		sh.POST("/:id/mirror",    s.shareMirror)        // rekursiv auf lokalen Pfad spiegeln
		sh.POST("/:id/ul/:dir",   s.shareUpload)        // Datei hochladen
		sh.GET("/:id/tree",       s.shareTree)          // Verzeichnisbaum (JSON)
		sh.GET("/:id/flat",       s.shareListFlat)      // Flat-Listing (alle FlatListing-Dirs zusammen)

		// Fremde Shares entdecken (via DHT)
		sh.GET("/discover/:peer", s.shareDiscover)
		sh.GET("/network",        s.shareDiscoverAll)   // alle Netz-Freigaben sammeln
		sh.GET("/remote/:peer/ping",          s.remoteSharePing)  // Erreichbarkeit
		sh.GET("/remote/:peer/:share/ls",     s.remoteShareLs)    // Remote-Verzeichnis
		sh.GET("/remote/:peer/:share/dl",     s.remoteShareDl)    // Remote-Download
		sh.POST("/remote/:peer/:share/mirror", s.remoteShareMirror) // Remote-Spiegelung
	}
}

// fileUpload nimmt eine Datei entgegen und verteilt sie 5-fach im Netz.
//
// POST /api/v1/files/upload
// Content-Type: multipart/form-data
// Felder: file (Binärdaten), mime_type (optional)
func (s *Server) fileUpload(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Filesharing ist auf diesem Node deaktiviert"})
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Datei fehlt: " + err.Error()})
		return
	}
	defer file.Close()

	mimeType := c.PostForm("mime_type")
	if mimeType == "" {
		mimeType = header.Header.Get("Content-Type")
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Dateigröße prüfen (max 128 GiB pro Upload; der freie Plattenplatz bleibt
	// die eigentliche Grenze — siehe Resume-Pfad mit FreeBytes()).
	const maxSize = 128 * 1024 * 1024 * 1024 // 128 GiB
	if header.Size > maxSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("Datei zu groß (max 128 GiB, ist %.1f GB)",
				float64(header.Size)/1e9),
		})
		return
	}

	// Optionale Redundanz (Marktplatz-Bilder brauchen keine 5 Replikate; in
	// kleinen Netzen scheitert die Peer-Prüfung sonst und der Upload schlägt fehl).
	replicas := filestore.TargetReplicas
	if rv := c.PostForm("redundancy"); rv != "" {
		if n, err := strconv.Atoi(rv); err == nil && n >= filestore.MinReplicas && n <= filestore.MaxReplicas {
			replicas = n
		}
	}
	contentHash, err := s.fileStore.UploadWithRedundancy(c.Request.Context(), file, mimeType, header.Filename, replicas)
	if err != nil {
		s.internalError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"content_hash": contentHash,
		"size_bytes":   header.Size,
		"mime_type":    mimeType,
		"download_url": fmt.Sprintf("/api/v1/files/download/%s", contentHash),
		"note":         "Datei wird 5-fach im P2P-Netz gespeichert. Anonymer Zugriff über den Content-Hash.",
	})
}

// fileDownload lädt eine Datei anhand ihres Content-Hashes herunter.
//
// GET /api/v1/files/download/:hash
func (s *Server) fileDownload(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	hash := strings.ToLower(c.Param("hash"))
	if len(hash) != 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Content-Hash (64 Hex-Zeichen erwartet)"})
		return
	}

	// Range-Request? (Video-Seeking, resumefähiger Download)
	rangeHeader := c.GetHeader("Range")
	if rangeHeader != "" {
		s.fileDownloadRange(c, hash, rangeHeader)
		return
	}

	// Größe + MIME für Content-Length/Type (ermöglicht Player-Seeking und
	// Fortschrittsanzeige). FileSize liest nur das Manifest (leichtgewichtig) —
	// KEIN Verfügbarkeits-Scan hier, der würde den Download-Start blockieren.
	size, mimeType, serr := s.fileStore.FileSize(c.Request.Context(), hash)
	if mimeType == "" {
		mimeType = mimeFromName(s.fileStore.NameForHash(hash))
	}
	mimeType = browserMime(mimeType)
	if serr == nil && size > 0 {
		c.Header("Content-Length", strconv.FormatInt(size, 10))
		c.Header("Accept-Ranges", "bytes")
		if mimeType != "" {
			c.Header("Content-Type", mimeType)
		}
	}

	// Dateiname aus dem lokalen Index (falls bekannt), damit der Download den
	// echten Namen + Endung bekommt statt hash.bin. Bei "inline" (Bildanzeige)
	// nutzt der Browser den Namen fürs "Speichern unter".
	name := s.fileStore.NameForHash(hash)
	if name != "" {
		// .fnde-Endung (verschlüsselt) für die Anzeige entfernen.
		dispName := strings.TrimSuffix(name, ".fnde")
		if c.Query("dl") == "1" {
			c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, dispName))
		} else {
			c.Header("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, dispName))
		}
	} else {
		c.Header("Content-Disposition", "inline")
	}
	c.Header("X-Content-Hash", hash)
	// Inhalt ist über den Content-Hash unveränderlich adressiert → aggressiv
	// cachen. Verhindert, dass Bilder/Videos (z.B. in Fundus-Love-Profilen) bei
	// jedem Ansehen neu geladen werden.
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("ETag", "\""+hash+"\"")
	if match := c.GetHeader("If-None-Match"); match == "\""+hash+"\"" {
		c.Status(http.StatusNotModified)
		return
	}

	w := c.Writer
	if err := s.fileStore.Download(c.Request.Context(), hash, w); err != nil {
		if !c.Writer.Written() {
			// Noch nichts gesendet → sauberer Fehlerstatus möglich.
			c.JSON(http.StatusNotFound, gin.H{
				"error":  "Datei nicht gefunden oder nicht abrufbar",
				"detail": err.Error(),
				"hash":   hash,
			})
			return
		}
		// Schon Bytes gesendet → Status nicht mehr änderbar. Wir loggen den
		// Abbruch; der unvollständige Stream endet, ohne dass eine scheinbar
		// vollständige Datei vorgetäuscht wird (der gesetzte Content-Length sorgt
		// dafür, dass der Client die zu kurze Antwort als Fehler erkennt).
		if s.log != nil {
			s.log.Warn("Download mitten im Stream abgebrochen",
				zap.String("hash", hash), zap.Error(err))
		}
		return
	}
}

// fileThumbnail liefert ein kleines, gecachtes Vorschaubild (256 px) für einen
// Bild-Content-Hash. Erspart der Trefferliste große Base64-Blobs und dem Client
// das Laden des Vollbilds.
// GET /api/v1/files/thumb/:hash
func (s *Server) fileThumbnail(c *gin.Context) {
	if s.fileStore == nil || s.thumbs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Thumbnails nicht verfügbar"})
		return
	}
	hash := strings.ToLower(c.Param("hash"))
	if len(hash) != 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Content-Hash"})
		return
	}

	thumb, err := s.thumbs.get(c.Request.Context(), hash, func(ctx context.Context) ([]byte, error) {
		// Vorab-Größencheck: nur kleine Originale puffern (Schutz gegen sehr
		// große Dateien, die kein Vorschau-Bild sind).
		if size, _, serr := s.fileStore.FileSize(ctx, hash); serr == nil && size > maxOriginalBytes {
			return nil, fmt.Errorf("Datei zu groß für Thumbnail")
		}
		var buf bytes.Buffer
		if derr := s.fileStore.Download(ctx, hash, &buf); derr != nil {
			return nil, derr
		}
		return buf.Bytes(), nil
	})
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Thumbnail nicht erzeugbar", "detail": err.Error()})
		return
	}

	// Aggressiv cachebar: Content-adressiert, ändert sich nie.
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Data(http.StatusOK, "image/jpeg", thumb)
}

// fileAvailability meldet, wie viele Chunks lokal verfügbar sind (für die
// Download-Bereitschaft nach Upload, während die Replikation noch läuft).
func (s *Server) fileAvailability(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	hash := strings.ToLower(c.Param("hash"))
	if len(hash) != 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Content-Hash"})
		return
	}
	have, total, ready, err := s.fileStore.DownloadAvailability(c.Request.Context(), hash)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Datei nicht gefunden", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ready": ready, "have": have, "total": total})
}

// GET /api/v1/files/redundancy/:hash — Netz-Redundanz einer Datei (Minimum der
// Replikatzahl über alle Chunks).
func (s *Server) fileRedundancy(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	hash := strings.ToLower(c.Param("hash"))
	if len(hash) != 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Content-Hash"})
		return
	}
	min, target, chunks, err := s.fileStore.FileRedundancy(c.Request.Context(), hash)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Datei nicht gefunden", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"redundancy": min, "target": target, "chunks": chunks})
}

// fileDownloadRange liefert einen Byte-Bereich (HTTP 206 Partial Content).
func (s *Server) fileDownloadRange(c *gin.Context, hash, rangeHeader string) {
	size, mimeType, err := s.fileStore.FileSize(c.Request.Context(), hash)
	if err != nil || size <= 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Datei nicht gefunden"})
		return
	}

	// Range parsen: "bytes=START-END" (END optional) oder "bytes=-N" (die
	// letzten N Bytes – so lesen manche Player, v.a. Safari, die Metadaten am
	// Dateiende). Bei mehreren Bereichen wird nur der erste bedient.
	var start, end int64 = 0, size - 1
	spec := strings.TrimPrefix(rangeHeader, "bytes=")
	if i := strings.IndexByte(spec, ','); i >= 0 {
		spec = spec[:i]
	}
	parts := strings.SplitN(strings.TrimSpace(spec), "-", 2)
	if len(parts) == 2 {
		switch {
		case parts[0] == "" && parts[1] != "":
			if n, err := strconv.ParseInt(parts[1], 10, 64); err == nil && n > 0 {
				if n > size {
					n = size
				}
				start = size - n
			}
		default:
			if parts[0] != "" {
				start, _ = strconv.ParseInt(parts[0], 10, 64)
			}
			if parts[1] != "" {
				end, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		}
	}
	if start < 0 {
		start = 0
	}
	if end >= size || end <= 0 {
		end = size - 1
	}
	if start > end {
		c.Header("Content-Range", fmt.Sprintf("bytes */%d", size))
		c.Status(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	if mimeType == "" {
		mimeType = mimeFromName(s.fileStore.NameForHash(hash))
	}
	mimeType = browserMime(mimeType)
	if mimeType == "" {
		// Range-Requests kommen praktisch immer von <video>/<audio>-Elementen,
		// die einen Content-Type brauchen, sonst spielt der Browser nichts ab.
		mimeType = "video/mp4"
	}
	if mimeType != "" {
		c.Header("Content-Type", mimeType)
	}
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	c.Header("Content-Length", strconv.FormatInt(end-start+1, 10))
	c.Status(http.StatusPartialContent)

	if err := s.fileStore.DownloadRange(c.Request.Context(), hash, start, end, c.Writer); err != nil {
		s.log.Warn("Range-Download fehlgeschlagen", zap.String("hash", hash[:16]), zap.Error(err))
	}
}

// fileInfo gibt Metadaten einer Datei zurück (Größe, Chunk-Count, etc.)
//
// GET /api/v1/files/info/:hash
func (s *Server) fileInfo(c *gin.Context) {
	hash := strings.ToLower(c.Param("hash"))
	if len(hash) != 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Hash"})
		return
	}

	// Manifest aus DHT
	data, err := s.node.DHTget(c.Request.Context(), "/fundus/file/"+hash)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Manifest nicht gefunden"})
		return
	}

	// Als JSON direkt zurückgeben
	c.Data(http.StatusOK, "application/json", data)
}

// fileStats gibt Statistiken des lokalen FileStore zurück.
//
// GET /api/v1/files/stats
func (s *Server) fileStats(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "offer_gb": 0,
			"used_gb": 0, "files": 0, "chunks": 0})
		return
	}
	c.JSON(http.StatusOK, s.fileStore.Stats())
}

// fileEarnings liefert die Verdienst-Übersicht des Nodes: offene (noch nicht
// geminteten) Quittungen, verdienbare Bytes nach Art, aktive Vorhaltung, und den
// aktuellen FND-Saldo der Node-Adresse (= bereits realisierte Einnahmen).
func (s *Server) fileEarnings(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false})
		return
	}
	stats := s.fileStore.ReceiptStats()
	resp := gin.H{
		"enabled":         true,
		"pending_count":   stats.PendingCount,
		"fetch_bytes":     stats.FetchBytes,
		"store_bytes":     stats.StoreBytes,
		"hosting_entries": stats.HostingEntries,
		"node_address":    stats.NodeAddress,
		"signing_active":  stats.SigningActive,
		"pending_ufnd":    stats.PendingUFND,
	}
	// Ob neu erzeugte Seed-Wörter zur Sicherung bereitstehen (Flag, NICHT die
	// Wörter selbst — die werden nur über den expliziten /node-seed-Endpunkt
	// ausgeliefert, damit ein Auto-Refresh sie nicht versehentlich konsumiert).
	resp["seed_pending"] = s.fileStore.NodeSeedAvailable()
	resp["wallet_locked"] = s.fileStore.WalletLocked()
	resp["seed_encrypted"] = s.fileStore.SeedIsEncrypted()
	// Realisierter Saldo (bereits geminteten FND) aus der Chain, falls verfügbar.
	if s.chain != nil && stats.NodeAddress != "" {
		if addr, ok := chain.AddressFromHex(stats.NodeAddress); ok {
			bal, _ := s.chain.AccountInfo(addr)
			resp["balance_ufnd"] = bal
		}
	}
	c.JSON(http.StatusOK, resp)
}

// nodeSeedGet liefert die Seed-Wörter der neu erzeugten Node-Wallet, ohne sie zu
// verbrauchen (mehrfach abrufbar, bis der Betreiber die Sicherung bestätigt).
func (s *Server) nodeSeedGet(c *gin.Context) {
	if s.fileStore == nil || !s.fileStore.NodeSeedAvailable() {
		c.JSON(http.StatusOK, gin.H{"available": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"available": true,
		"words":     s.fileStore.PeekNodeSeedWords(),
	})
}

// nodeSeedConfirm markiert die Seed-Sicherung als erledigt (entfernt die Wörter
// aus dem Speicher; sie bleiben in node.seed auf der Platte).
func (s *Server) nodeSeedConfirm(c *gin.Context) {
	if s.fileStore != nil {
		s.fileStore.ConfirmSeedBackup()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nodeSeedReveal liefert die Seed-Wörter jederzeit aus node.seed (auch später,
// nicht nur direkt nach Erstellung). Wenn ein Anzeige-Passwort gesetzt ist, muss
// es im Body mitgeschickt werden. Meldet auch, ob Passwortschutz aktiv ist.
func (s *Server) nodeSeedReveal(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{"available": false})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&body)
	words, err := s.fileStore.ReadNodeSeedWordsWithPassword(body.Password)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"available": true,
			"protected": s.fileStore.SeedDisplayProtected(),
			"error":     err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"available": true, "words": words})
}

// nodeSeedSetPassword setzt/ändert/entfernt das Anzeige-Passwort für die Seed.
func (s *Server) nodeSeedSetPassword(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein FileStore"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&body)
	if err := s.fileStore.SetSeedDisplayPassword(body.Password); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "protected": body.Password != ""})
}

// nodeSeedRecreate erzeugt eine KOMPLETT NEUE Node-Wallet. Die alte Adresse und
// ein etwaiges Guthaben darauf sind danach nicht mehr über diesen Node erreichbar.
// Erfordert die explizite Bestätigung "confirm":"NEU ERSTELLEN" im Body.
func (s *Server) nodeSeedRecreate(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein FileStore"})
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.Confirm != "NEU ERSTELLEN" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bestätigung fehlt"})
		return
	}
	words, address, err := s.fileStore.RecreateNodeWallet()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "words": words, "address": address})
}

// nodeSeedEncrypt verschlüsselt eine vorhandene Klartext-Seed mit einem Passwort
// (Migration Klartext → verschlüsselt). Danach braucht der Node das Passwort beim
// Start. WARNUNG an den Nutzer: Passwort-Verlust = Wallet unwiderruflich weg.
func (s *Server) nodeSeedEncrypt(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein FileStore"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&body)
	if len(body.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Passwort zu kurz (min. 8 Zeichen)"})
		return
	}
	if err := s.fileStore.EncryptExistingSeed(body.Password); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nodeSeedUnlock entsperrt eine verschlüsselte Seed zur Laufzeit (z.B. nach einem
// Neustart, wenn der Node im gesperrten Modus gestartet ist).
func (s *Server) nodeSeedUnlock(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein FileStore"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&body)
	if err := s.fileStore.UnlockEncryptedSeed(body.Password); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// nodeSeedDecrypt entfernt die Verschlüsselung wieder (Klartext-Seed zurück),
// nach Passwort-Prüfung. Für Nutzer, die den autonomen Auto-Start bevorzugen.
func (s *Server) nodeSeedDecrypt(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein FileStore"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&body)
	if err := s.fileStore.DecryptSeedToPlaintext(body.Password); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// =============================================================================
//  Share-Handler
// =============================================================================

func (s *Server) shareMgr() *filestore.ShareManager {
	if s.fileStore == nil {
		return nil
	}
	return s.shareMgr_
}

// shareList gibt alle eigenen Shares zurück.
//
// GET /api/v1/shares
func (s *Server) shareList(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	shares := mgr.List()
	c.JSON(http.StatusOK, gin.H{"shares": shares, "count": len(shares)})
}

// shareCreate erstellt eine neue Verzeichnis-Share.
//
// POST /api/v1/shares
// Body:
//
//	{
//	  "name": "Fotos 2026",
//	  "description": "Urlaubsbilder",
//	  "dirs": [
//	    {"virtual_name": "Strand", "local_path": "/home/pi/Fotos/Strand"},
//	    {"virtual_name": "Berge",  "local_path": "/home/pi/Fotos/Berge", "read_only": true}
//	  ],
//	  "read_access":  {"mode": "contacts"},
//	  "write_access": {"mode": "specific", "peer_ids": ["12D3KooW…"]}
//	}
func (s *Server) shareCreate(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}

	var req struct {
		Name        string                   `json:"name"        binding:"required"`
		Description string                   `json:"description"`
		Dirs        []filestore.MappedDir    `json:"dirs"        binding:"required"`
		ReadAccess  filestore.AccessRule     `json:"read_access"`
		WriteAccess filestore.AccessRule     `json:"write_access"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Default: Lesen=contacts, Schreiben=none
	if req.ReadAccess.Mode == "" {
		req.ReadAccess.Mode = filestore.AccessContacts
	}
	if req.WriteAccess.Mode == "" {
		req.WriteAccess.Mode = filestore.AccessNone
	}

	share, err := mgr.Create(req.Name, req.Description, req.Dirs, req.ReadAccess, req.WriteAccess)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, share)
}

// shareGet gibt Details einer Share zurück.
func (s *Server) shareGet(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	share, ok := mgr.Get(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Share nicht gefunden"})
		return
	}
	// N-1: lokale Pfade nur für localhost-Zugriff (Owner) zurückgeben
	requester := c.GetHeader("X-Fundus-Peer-ID")
	isLocal := requester == "" || requester == "local"
	if !isLocal {
		// Öffentliche Ansicht: lokale Pfade ausblenden
		c.JSON(http.StatusOK, share.PublicView())
		return
	}
	c.JSON(http.StatusOK, share)
}

// shareUpdate aktualisiert Berechtigungen einer Share.
//
// PUT /api/v1/shares/:id
func (s *Server) shareUpdate(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	var req struct {
		Name        string                `json:"name"`
		Description string                `json:"description"`
		Dirs        []filestore.MappedDir `json:"dirs"`
		ReadAccess  filestore.AccessRule  `json:"read_access"`
		WriteAccess filestore.AccessRule  `json:"write_access"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	share, err := mgr.Update(c.Param("id"), req.Name, req.Description, req.Dirs, req.ReadAccess, req.WriteAccess)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, share)
}

// shareDelete löscht eine Share (lokale Dateien bleiben erhalten).
func (s *Server) shareDelete(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	if err := mgr.Delete(c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// shareListDir listet das Wurzelverzeichnis einer Share.
func (s *Server) shareListDir(c *gin.Context) {
	s.doShareList(c, c.Param("id"), "")
}

// shareListDirPath listet einen Unterordner.
func (s *Server) shareListDirPath(c *gin.Context) {
	s.doShareList(c, c.Param("id"), c.Param("path"))
}

func (s *Server) doShareList(c *gin.Context, shareID, path string) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	// Requester-PeerID aus Header (gesetzt vom libp2p-Gateway) oder Query
	requester := c.GetHeader("X-Fundus-Peer-ID")
	if requester == "" {
		// Nur von localhost akzeptiert ohne PeerID (Web-UI)
		host := c.Request.RemoteAddr
		if !strings.HasPrefix(host, "127.") && !strings.HasPrefix(host, "[::1]") {
			c.JSON(http.StatusForbidden, gin.H{"error": "peer authentication required"})
			return
		}
		requester = "local"
	}

	entries, err := mgr.ListDir(shareID, requester, path)
	if err != nil {
		status := http.StatusForbidden
		if strings.Contains(err.Error(), "nicht gefunden") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"share_id": shareID,
		"path":     path,
		"entries":  entries,
		"count":    len(entries),
	})
}

// shareDownload lädt eine Datei aus einer Share herunter.
//
// GET /api/v1/shares/:id/dl/*path
func (s *Server) shareDownload(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	requester := c.GetHeader("X-Fundus-Peer-ID")
	if requester == "" {
		host := c.Request.RemoteAddr
		if !strings.HasPrefix(host, "127.") && !strings.HasPrefix(host, "[::1]") {
			c.JSON(http.StatusForbidden, gin.H{"error": "peer authentication required"})
			return
		}
		requester = "local"
	}

	path := c.Param("path")
	filename := filepath.Base(path)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	// Content-Length für Fortschrittsanzeige im Browser
	if size, err := mgr.FileSize(c.Param("id"), requester, path); err == nil && size > 0 {
		c.Header("Content-Length", strconv.FormatInt(size, 10))
	}

	if err := mgr.ReadFile(c.Param("id"), requester, path, c.Writer); err != nil {
		// Header schon gesendet – nur loggen
		s.log.Warn("Share-Download fehlgeschlagen", zap.String("path", path), zap.Error(err))
	}
}

// shareUpload lädt eine Datei in eine Share hoch.
//
// POST /api/v1/shares/:id/ul/:dir
func (s *Server) shareUpload(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	requester := c.GetHeader("X-Fundus-Peer-ID")
	if requester == "" {
		requester = "local"
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "keine Datei im Request"})
		return
	}
	defer file.Close()

	if err := mgr.WriteFile(
		c.Param("id"), requester, c.Param("dir"),
		header.Filename, file, header.Size,
	); err != nil {
		status := http.StatusForbidden
		if strings.Contains(err.Error(), "nicht gefunden") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"uploaded": header.Filename,
		"size":     header.Size,
	})
}

// shareTree gibt den gesamten Verzeichnisbaum einer Share zurück.
//
// GET /api/v1/shares/:id/tree
func (s *Server) shareTree(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	share, ok := mgr.Get(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Share nicht gefunden"})
		return
	}

	type TreeNode struct {
		Name     string      `json:"name"`
		IsDir    bool        `json:"is_dir"`
		Size     int64       `json:"size,omitempty"`
		Children []TreeNode  `json:"children,omitempty"`
	}

	var buildTree func(path, virtualPath string) []TreeNode
	buildTree = func(localPath, virtualPath string) []TreeNode {
		entries, err := os.ReadDir(localPath)
		if err != nil {
			return nil
		}
		var nodes []TreeNode
		for _, e := range entries {
			vpath := filepath.Join(virtualPath, e.Name())
			node := TreeNode{Name: e.Name(), IsDir: e.IsDir()}
			if e.IsDir() {
				node.Children = buildTree(filepath.Join(localPath, e.Name()), vpath)
			} else if info, err := e.Info(); err == nil {
				node.Size = info.Size()
			}
			nodes = append(nodes, node)
		}
		return nodes
	}

	var root []TreeNode
	for _, d := range share.Dirs {
		node := TreeNode{
			Name:     d.VirtualName,
			IsDir:    true,
			Children: buildTree(d.LocalPath, d.VirtualName),
		}
		root = append(root, node)
	}
	c.JSON(http.StatusOK, gin.H{"share_id": share.ID, "name": share.Name, "tree": root})
}

// shareDiscover entdeckt Shares eines anderen Peers via DHT.
//
// GET /api/v1/shares/discover/:peer
func (s *Server) shareDiscover(c *gin.Context) {
	peerID := c.Param("peer")
	data, err := s.node.DHTget(c.Request.Context(), filestore.DHTSharesKey+peerID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "keine Shares gefunden"})
		return
	}
	var shares interface{}
	if json.Unmarshal(data, &shares) != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Shares-Daten fehlerhaft"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"peer_id": peerID, "shares": shares})
}

// shareListFlat gibt alle Dateien aus FlatListing-Verzeichnissen zusammen zurück.
// Dateien verschiedener Verzeichnisse erscheinen nebeneinander ohne Ordner-Prefix.
//
// GET /api/v1/shares/:id/flat
func (s *Server) shareListFlat(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	requester := c.GetHeader("X-Fundus-Peer-ID")
	if requester == "" {
		if !strings.HasPrefix(c.Request.RemoteAddr, "127.") && !strings.HasPrefix(c.Request.RemoteAddr, "[::1]") {
			c.JSON(http.StatusForbidden, gin.H{"error": "peer authentication required"})
			return
		}
		requester = "local"
	}

	// Root-Listing aufrufen – enthält bereits Flat-Entries
	entries, err := mgr.ListDir(c.Param("id"), requester, "")
	if err != nil {
		status := http.StatusForbidden
		if strings.Contains(err.Error(), "nicht gefunden") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	// Nur Flat-Entries filtern (haben \x00flat\x00 Prefix im VirtualPath)
	var flat []filestore.DirEntry
	for _, e := range entries {
		if strings.HasPrefix(e.VirtualPath, "\x00flat\x00") {
			flat = append(flat, e)
		}
	}
	if flat == nil {
		flat = []filestore.DirEntry{}
	}

	c.JSON(http.StatusOK, gin.H{
		"share_id":    c.Param("id"),
		"flat_mode":   true,
		"entries":     flat,
		"count":       len(flat),
	})
}


// =============================================================================
//  Resumefähiger Upload (disconnect-sicher, für grosse Dateien/Videos)
// =============================================================================

// POST /api/v1/files/upload/begin
// Body: {upload_id, file_name, mime_type, total_size, block_size}
// Antwort: {upload_id, block_size, total_blocks, have_blocks:[...]}
func (s *Server) fileUploadBegin(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	var req struct {
		UploadID   string `json:"upload_id"`
		FileName   string `json:"file_name"`
		MimeType   string `json:"mime_type"`
		TotalSize  int64  `json:"total_size"`
		BlockSize  int64  `json:"block_size"`
		Redundancy int    `json:"redundancy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Anfrage: " + err.Error()})
		return
	}
	// Redundanz validieren: nie unter dem Sicherheits-Minimum (sonst droht
	// Datenverlust beim Ausfall einzelner Hosts). Default = empfohlene Stufe.
	if req.Redundancy == 0 {
		req.Redundancy = filestore.TargetReplicas
	}
	if req.Redundancy < filestore.MinReplicas {
		req.Redundancy = filestore.MinReplicas
	}
	if req.Redundancy > filestore.MaxReplicas {
		req.Redundancy = filestore.MaxReplicas
	}

	// VOR dem Upload: Fairness (OfferGB deckt die Last) + Verfügbarkeit (genug
	// erreichbare Peers mit echtem Platz). Harte Schranke — kein Upload sonst.
	if err := s.fileStore.CheckUploadAllowed(c.Request.Context(), req.TotalSize, req.Redundancy); err != nil {
		c.JSON(http.StatusInsufficientStorage, gin.H{"error": err.Error()})
		return
	}
	// Limit: 20 GB ODER der noch freie Speicher, je nachdem was kleiner ist.
	const hardCap = 128 * 1024 * 1024 * 1024 // 128 GiB
	free := s.fileStore.FreeBytes()
	limit := int64(hardCap)
	if free < limit {
		limit = free
	}
	if req.TotalSize > limit {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("Datei zu groß: %.1f GB, frei: %.1f GB (max %.0f GB)",
				float64(req.TotalSize)/1e9, float64(free)/1e9, float64(hardCap)/1e9),
		})
		return
	}
	sess, have, err := s.fileStore.BeginResumeUpload(
		req.UploadID, req.FileName, req.MimeType, req.TotalSize, req.BlockSize, req.Redundancy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"upload_id":    sess.UploadID,
		"block_size":   sess.BlockSize,
		"total_blocks": sess.TotalBlocks,
		"have_blocks":  have,
	})
}

// POST /api/v1/files/upload/block?id=<upload_id>&index=<n>
// Body: rohe Block-Bytes
func (s *Server) fileUploadBlock(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	uploadID := c.Query("id")
	index, err := strconv.Atoi(c.Query("index"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger index"})
		return
	}
	if err := s.fileStore.StoreResumeBlock(uploadID, index, c.Request.Body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "index": index})
}

// POST /api/v1/files/upload/finish?id=<upload_id>
// Startet die Finalisierung im Hintergrund und kehrt sofort zurück (202).
// Der Client fragt den Fortschritt über /upload/status ab.
func (s *Server) fileUploadFinish(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	uploadID := c.Query("id")
	if err := s.fileStore.StartFinishResumeUpload(uploadID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"state": "finalizing", "upload_id": uploadID})
}

// GET /api/v1/files/list — Dateien aus dem persönlichen Index (Filemanager)
func (s *Server) fileList(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{"files": []interface{}{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": s.fileStore.ListFiles()})
}

// DELETE /api/v1/files/info/:hash — Datei aus lokalem Index + Freigabe entfernen
func (s *Server) fileDelete(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	hash := strings.ToLower(c.Param("hash"))
	if len(hash) != 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiger Hash"})
		return
	}
	s.fileStore.RemoveFromIndex(hash)
	c.JSON(http.StatusOK, gin.H{"removed": true, "hash": hash})
}

// GET /api/v1/files/upload/status?id=<upload_id>
// Liefert den Finalisierungs-Status: uploading | finalizing | done | error
func (s *Server) fileUploadStatus(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	st, ok := s.fileStore.GetResumeStatus(c.Query("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"state": "unknown"})
		return
	}
	resp := gin.H{"state": st.State, "file_name": st.FileName}
	if st.ContentHash != "" {
		resp["content_hash"] = st.ContentHash
		resp["download_url"] = fmt.Sprintf("/api/v1/files/download/%s", st.ContentHash)
	}
	if st.Error != "" {
		resp["error"] = st.Error
	}
	// Chunking-Fortschritt (falls vorhanden) durchreichen.
	if st.ChunksTotal > 0 {
		resp["phase"] = st.Phase
		resp["chunks_done"] = st.ChunksDone
		resp["chunks_total"] = st.ChunksTotal
	}
	c.JSON(http.StatusOK, resp)
}

// POST /api/v1/files/upload/abort?id=<upload_id>
func (s *Server) fileUploadAbort(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	s.fileStore.AbortResumeUpload(c.Query("id"))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// createManifest nimmt die Liste bereits hochgeladener Dateien (Pfad+Hash+Size)
// entgegen, baut daraus ein Verzeichnis-Manifest, lädt es ins Netz und trägt den
// Ordner in den LocalIndex ein.
//
// POST /api/v1/files/manifest
// Body: { "name": "urlaub", "replicas": 5, "entries": [ {path,hash,size,mime}, ... ] }
func (s *Server) createManifest(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing ist auf diesem Node deaktiviert"})
		return
	}
	var req struct {
		Name     string                     `json:"name"     binding:"required"`
		Replicas int                        `json:"replicas"`
		Entries  []filestore.ManifestEntry `json:"entries"  binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Entries) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "keine Dateien im Ordner"})
		return
	}
	replicas := req.Replicas
	if replicas <= 0 {
		replicas = 5
	}
	hash, err := s.fileStore.CreateDirManifest(c.Request.Context(), req.Name, req.Entries, replicas)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "manifest_hash": hash, "name": req.Name, "count": len(req.Entries)})
}

// getManifest liefert den Inhalt eines Verzeichnis-Manifests (für die Baum-
// Navigation im Filemanager).
//
// GET /api/v1/files/manifest/:hash
func (s *Server) getManifest(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing ist auf diesem Node deaktiviert"})
		return
	}
	hash := c.Param("hash")
	m, err := s.fileStore.GetManifest(c.Request.Context(), hash)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, m)
}

// browseLocalDir listet den Inhalt eines lokalen Verzeichnisses auf dem Node
// (nur Ordner, für die Auswahl bei der Verzeichnis-Freigabe). Beginnt ohne
// Parameter bei den gemounteten Laufwerken. Nur lesend, nur Verzeichnisse.
//
// GET /api/v1/files/browse?path=/media/tobias
func (s *Server) browseLocalDir(c *gin.Context) {
	browseStart := time.Now()
	defer func() {
		if d := time.Since(browseStart); d > 200*time.Millisecond && s.log != nil {
			s.log.Warn("browseLocalDir langsam", zap.Duration("dauer", d), zap.String("path", c.Query("path")))
		}
	}()
	path := c.Query("path")
	type dirItem struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}
	// Ohne Pfad: Einstiegspunkte = aktuell gemountete externe Laufwerke (aus
	// /proc/mounts, unabhängig von Verzeichnisrechten) + gängige Medien-Orte.
	if path == "" {
		roots := []dirItem{}
		seen := map[string]bool{}
		// 1. Gemountete externe Laufwerke aus /proc/mounts — das findet die Platte
		//    auch, wenn fundus /media selbst nicht auflisten darf (ACL). /proc/mounts
		//    hängt NIE (im Gegensatz zu ReadDir auf einem FUSE-Mount).
		if data, err := os.ReadFile("/proc/mounts"); err == nil {
			// Netzwerk-/virtuelle Dateisysteme überspringen: ein ReadDir darauf
			// kann hängen (nicht erreichbare Freigabe), und sie sollen für die
			// lokale Datei-Freigabe ohnehin nicht als Ziel erscheinen.
			netFS := map[string]bool{
				"cifs": true, "smbfs": true, "smb3": true, "nfs": true, "nfs4": true,
				"fuse.gvfsd-fuse": true, "fuse.sshfs": true, "afpfs": true, "ncpfs": true,
				"fuse.rclone": true, "davfs": true,
			}
			for _, line := range strings.Split(string(data), "\n") {
				f := strings.Fields(line)
				if len(f) < 3 {
					continue
				}
				fsType := f[2]
				if netFS[fsType] {
					continue // Netzwerk-Freigabe überspringen
				}
				mp := strings.ReplaceAll(f[1], "\\040", " ") // Leerzeichen dekodieren
				if (strings.HasPrefix(mp, "/media/") || strings.HasPrefix(mp, "/mnt/") || strings.HasPrefix(mp, "/run/media/")) && !seen[mp] {
					seen[mp] = true
					roots = append(roots, dirItem{Name: mp, Path: mp, IsDir: true})
				}
			}
		}
		// 2. Zusätzlich die Basis-Verzeichnisse durchsuchen — ABER mit Timeout, denn
		//    ein ReadDir auf einem hängenden FUSE-Mount kann bis zu 30s blockieren.
		//    Da /proc/mounts die echten Laufwerke bereits SOFORT liefert, ist der
		//    ergänzende Scan nur ein Fallback für Mounts, die (selten) nicht in
		//    /proc/mounts stehen. Kurzer Timeout, damit nichts hängt.
		type scanRes struct{ items []dirItem }
		resCh := make(chan scanRes, 1)
		go func() {
			extra := []dirItem{}
			for _, base := range []string{"/media", "/run/media"} {
				entries, err := os.ReadDir(base)
				if err != nil {
					continue
				}
				for _, e := range entries {
					// e.Type().IsDir() nutzt den DirEntry-Typ OHNE zusätzlichen
					// Stat-Syscall (im Gegensatz zu e.IsDir() bei manchen FS).
					extra = append(extra, dirItem{Name: base + "/" + e.Name(), Path: base + "/" + e.Name(), IsDir: e.Type().IsDir()})
				}
			}
			resCh <- scanRes{items: extra}
		}()
		select {
		case r := <-resCh:
			for _, it := range r.items {
				if it.IsDir && !seen[it.Path] {
					seen[it.Path] = true
					roots = append(roots, it)
				}
			}
		case <-time.After(1 * time.Second):
			// Timeout: /proc/mounts-Ergebnisse reichen (die finden die echten
			// Laufwerke sofort); den ergänzenden ReadDir-Scan nicht abwarten.
		}
		c.JSON(http.StatusOK, gin.H{"path": "", "parent": "", "items": roots})
		return
	}
	// Pfad-Absicherung: nur absolute Pfade, kein "..".
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger Pfad"})
		return
	}
	// Verzeichnis limitiert lesen: os.ReadDir liest IMMER alle Einträge (bei
	// zehntausenden Dateien auf USB-exFAT sekundenlang). os.Open + ReadDir(n)
	// liest nur die ersten Einträge und stoppt — superschnell, auch bei riesigen
	// Verzeichnissen. Für die Ordner-Auswahl reichen die ersten paar hundert.
	const maxEntries = 300
	dirf, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"path": path, "error": err.Error(), "items": []dirItem{}})
		return
	}
	items := make([]dirItem, 0, 64)
	truncated := false
	for len(items) < maxEntries {
		batch, rerr := dirf.ReadDir(200) // in Blöcken lesen
		for _, e := range batch {
			if !e.Type().IsDir() {
				continue // nur Ordner
			}
			if strings.HasPrefix(e.Name(), ".") {
				continue // versteckte überspringen
			}
			items = append(items, dirItem{Name: e.Name(), Path: filepath.Join(path, e.Name()), IsDir: true})
			if len(items) >= maxEntries {
				truncated = true
				break
			}
		}
		if rerr != nil { // io.EOF oder Fehler → fertig
			break
		}
	}
	dirf.Close()
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	resp := gin.H{"path": path, "parent": filepath.Dir(path), "items": items}
	if truncated {
		resp["truncated"] = true
	}
	c.JSON(http.StatusOK, resp)
	return
}


// shareMirror spiegelt eine Freigabe (oder Unterordner) rekursiv auf einen
// lokalen Zielpfad.
//
// POST /api/v1/shares/:id/mirror  Body: { "sub_path": "...", "target": "/mnt/..." }
func (s *Server) shareMirror(c *gin.Context) {
	mgr := s.shareMgr_
	if mgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "ShareManager nicht aktiv"})
		return
	}
	var req struct {
		SubPath string `json:"sub_path"`
		Target  string `json:"target" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	count, err := mgr.MirrorShare(c.Param("id"), req.SubPath, req.Target)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "count": count})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": count, "target": req.Target})
}

// fileName liefert den bekannten Dateinamen zu einem Hash (aus dem lokalen Index).
// GET /api/v1/files/name/:hash
func (s *Server) fileName(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{"name": ""})
		return
	}
	name := s.fileStore.NameForHash(strings.ToLower(c.Param("hash")))
	c.JSON(http.StatusOK, gin.H{"name": strings.TrimSuffix(name, ".fnde")})
}

// shareDiscoverAll durchsucht alle bekannten Peers nach ihren veröffentlichten
// Verzeichnis-Freigaben (via DHT) und sammelt sie. So sieht man im Netz geteilte
// Verzeichnisse, ohne die Peer-IDs einzeln kennen zu müssen.
//
// GET /api/v1/shares/discover
func (s *Server) shareDiscoverAll(c *gin.Context) {
	if s.shareMgr_ == nil {
		c.JSON(http.StatusOK, gin.H{"peers": []interface{}{}, "count": 0})
		return
	}
	// Aus dem Hintergrund-Discovery-Cache lesen (billig) — der ShareManager
	// pollt zentral, das Frontend liest nur den fertigen Stand.
	cached := s.shareMgr_.DiscoveredShares()
	c.Data(http.StatusOK, "application/json", wrapDiscovered(cached))
}

// wrapDiscovered verpackt das gecachte peers-Array in {peers, count}.
func wrapDiscovered(peersJSON []byte) []byte {
	// peersJSON ist ein Array. Anzahl grob aus der Länge ableiten wäre unsauber;
	// wir zählen die Elemente per Unmarshal (klein, unkritisch).
	var arr []json.RawMessage
	_ = json.Unmarshal(peersJSON, &arr)
	out, _ := json.Marshal(map[string]interface{}{
		"peers": json.RawMessage(peersJSON),
		"count": len(arr),
	})
	return out
}

// remoteShareLs listet ein Verzeichnis einer Freigabe auf einem ANDEREN Node auf
// (Proxy via P2P-Stream). So kann man Netz-Freigaben durchsuchen wie lokale.
//
// GET /api/v1/shares/remote/:peer/:share/ls?path=...
func (s *Server) remoteShareLs(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	peer := c.Param("peer")
	req := map[string]string{"type": "ls", "share": c.Param("share"), "path": c.Query("path")}
	reqData, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	resp, err := s.node.SendAndReceive(ctx, peer, filestore.SharesProtocol, reqData)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Node nicht erreichbar: " + err.Error()})
		return
	}
	c.Data(http.StatusOK, "application/json", resp)
}

// remoteShareDl lädt eine Datei aus einer Freigabe auf einem ANDEREN Node.
//
// GET /api/v1/shares/remote/:peer/:share/dl?path=...
func (s *Server) remoteShareDl(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	peer := c.Param("peer")
	path := c.Query("path")
	req := map[string]string{"type": "dl", "share": c.Param("share"), "path": path}
	reqData, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	resp, err := s.node.SendAndReceive(ctx, peer, filestore.SharesProtocol, reqData)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Node nicht erreichbar: " + err.Error()})
		return
	}
	// Fehler-JSON erkennen (beginnt mit {"error")
	if len(resp) > 8 && string(resp[:8]) == `{"error"` {
		c.Data(http.StatusBadGateway, "application/json", resp)
		return
	}
	name := filepath.Base(path)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	c.Data(http.StatusOK, "application/octet-stream", resp)
}

// remoteSharePing prüft, ob ein Node erreichbar ist (für die Erreichbarkeits-
// Prüfung der Netz-Freigaben).
//
// GET /api/v1/shares/remote/:peer/ping
func (s *Server) remoteSharePing(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusOK, gin.H{"reachable": false})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	_, err := s.node.SendAndReceive(ctx, c.Param("peer"), filestore.SharesProtocol, []byte("q"))
	c.JSON(http.StatusOK, gin.H{"reachable": err == nil})
}

// remoteShareMirror spiegelt eine Freigabe von einem ANDEREN Node rekursiv auf
// einen lokalen Pfad. Listet remote auf und lädt jede Datei einzeln über den
// P2P-Stream. Für kleine bis mittlere Freigaben; bei sehr großen wäre ein
// Streaming-Ansatz nötig.
//
// POST /api/v1/shares/remote/:peer/:share/mirror  Body: { sub_path, target }
func (s *Server) remoteShareMirror(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P nicht aktiv"})
		return
	}
	var req struct {
		SubPath string `json:"sub_path"`
		Target  string `json:"target" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !strings.HasPrefix(req.Target, "/") || strings.Contains(req.Target, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger Zielpfad"})
		return
	}
	peer := c.Param("peer")
	share := c.Param("share")
	count, err := s.mirrorRemoteRecursive(c.Request.Context(), peer, share, req.SubPath, req.Target)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "count": count})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": count, "target": req.Target})
}

// mirrorRemoteRecursive listet einen Remote-Pfad auf und spiegelt Dateien +
// Unterordner rekursiv. Gibt die Anzahl kopierter Dateien zurück.
func (s *Server) mirrorRemoteRecursive(ctx context.Context, peer, share, subPath, target string) (int, error) {
	// Verzeichnis remote auflisten.
	lsReq := map[string]string{"type": "ls", "share": share, "path": subPath}
	lsData, _ := json.Marshal(lsReq)
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	resp, err := s.node.SendAndReceive(lctx, peer, filestore.SharesProtocol, lsData)
	cancel()
	if err != nil {
		return 0, fmt.Errorf("Auflisten fehlgeschlagen: %w", err)
	}
	var lsResult struct {
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
		} `json:"entries"`
		Error string `json:"error"`
	}
	if json.Unmarshal(resp, &lsResult) != nil || lsResult.Error != "" {
		return 0, fmt.Errorf("Remote-Verzeichnis nicht lesbar")
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return 0, err
	}
	count := 0
	for _, e := range lsResult.Entries {
		childVirtual := e.Name
		if subPath != "" {
			childVirtual = subPath + "/" + e.Name
		}
		if e.IsDir {
			// Rekursiv in den Unterordner.
			sub, err := s.mirrorRemoteRecursive(ctx, peer, share, childVirtual, filepath.Join(target, e.Name))
			count += sub
			if err != nil {
				continue // einzelne Fehler überspringen
			}
		} else {
			// Datei herunterladen.
			dlReq := map[string]string{"type": "dl", "share": share, "path": childVirtual}
			dlData, _ := json.Marshal(dlReq)
			dctx, dcancel := context.WithTimeout(ctx, 60*time.Second)
			fileData, ferr := s.node.SendAndReceive(dctx, peer, filestore.SharesProtocol, dlData)
			dcancel()
			if ferr != nil || (len(fileData) > 8 && string(fileData[:8]) == `{"error"`) {
				continue
			}
			if os.WriteFile(filepath.Join(target, e.Name), fileData, 0644) == nil {
				count++
			}
		}
	}
	return count, nil
}

// mimeFromName leitet den Content-Type aus der Dateiendung ab (für Dateien ohne
// gespeicherten MIME-Typ). Verschlüsselte Dateien (.fnde) bleiben unbestimmt –
// ihr Inhalt ist erst nach dem Entschlüsseln im Browser lesbar.
func mimeFromName(name string) string {
	n := strings.ToLower(name)
	if n == "" || strings.HasSuffix(n, ".fnde") {
		return ""
	}
	switch filepath.Ext(n) {
	case ".mkv":
		return "video/webm" // Matroska ≈ WebM: Chrome/Edge spielen H.264/VP9-MKV so direkt
	case ".mov":
		return "video/quicktime"
	case ".m4v", ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ogv":
		return "video/ogg"
	case ".heic":
		return "image/heic"
	case ".avif":
		return "image/avif"
	}
	return mime.TypeByExtension(filepath.Ext(n))
}

// browserMime: Typen, die der Browser als Video/Audio erkennen soll.
// video/x-matroska lehnen Browser ab – als video/webm spielen sie MKV direkt.
func browserMime(m string) string {
	switch strings.ToLower(m) {
	case "video/x-matroska", "video/mkv":
		return "video/webm"
	case "audio/x-matroska":
		return "audio/webm"
	}
	return m
}
