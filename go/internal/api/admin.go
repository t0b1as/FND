package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/config"
	"github.com/fundus/node/internal/grid"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
	"github.com/fundus/node/internal/update"
)

// registerAdminRoutes hängt Admin-Endpunkte ein.
// Nur auf localhost:3000 erreichbar (API-Server bindet nur 127.0.0.1).
func (s *Server) registerAdminRoutes() {
	g := s.router.Group("/api/v1/admin")
	// Auth-Middleware: blockiert Tunnel-Anfragen ohne gültige Eigentümer-Session.
	// Lokale Anfragen laufen frei durch (Betreiber am Node sperrt sich nie aus).
	g.Use(s.adminAuthMiddleware())
	{
		// Login/Logout/Status: müssen OHNE bestehende Session erreichbar sein,
		// stehen daher außerhalb der Blockade (die Middleware lässt sie durch,
		// weil sie nur schreibende Admin-Aktionen über den Tunnel sperrt).
		g.POST("/login",   s.adminLoginGone)
		g.POST("/logout",  s.adminLoginGone)
		g.GET("/auth",     s.adminAuthStatus)
		g.GET("/wallet",   s.adminGetWallet)  // aktuelle Admin-Wallet (öffentliche Adresse)
		g.POST("/wallet",  s.adminSetWallet)  // Admin-Wallet aus Seed setzen (nur lokal)
		g.GET("/version",          s.adminVersion)
		g.POST("/update",          s.adminPublishUpdate)
		g.GET("/update/status",    s.updateStatus)
		g.POST("/update/check",    s.updateCheck)
		g.POST("/update/apply",    s.updateApply)
		g.GET("/config",           s.adminCheckConfig)
		g.POST("/grid/init",       s.adminInitGridWallets)
		g.GET("/grid/wallets",     s.adminListGridWallets)
		g.POST("/storage/expand",  s.adminStorageExpand)
		g.GET("/storage/stats",    s.adminStorageStats)
		g.POST("/listings/purge-orphans", s.adminPurgeOrphans)
		g.POST("/partner/purge-stale",    s.adminPurgePartnerAds)
		g.GET("/uploads/sessions",        s.adminListUploadSessions)
		g.POST("/uploads/purge",          s.adminPurgeUploadSessions)
		g.GET("/storage/drives",          s.adminListDrives)
		g.GET("/storage/volumes",         s.adminListVolumes)
		g.POST("/storage/cleanup",        s.adminCleanup)
		g.POST("/storage/volumes",        s.adminAddVolume)
		g.DELETE("/storage/volumes",      s.adminRemoveVolume)
		g.POST("/storage/volume-offer",   s.adminSetVolumeOffer)
		g.POST("/storage/chain-move",     s.adminChainMove)
		g.GET("/location",                s.adminGetLocation)
		g.POST("/location",               s.adminSetLocation)

		// Privilegierte Operationen über den fundus-helper (root-Daemon):
		// externe Laufwerke mounten, WLAN wechseln. Alle prüfen zuerst, ob der
		// Helper überhaupt läuft, und antworten sonst mit einer klaren Meldung.
		g.GET("/system/helper",           s.adminHelperStatus)
		g.GET("/system/blockdevices",     s.adminListBlockDevices)
		g.POST("/system/mount",           s.adminMountDrive)
		g.POST("/system/unmount",         s.adminUnmountDrive)
		g.GET("/system/wifi",             s.adminListWifi)
		g.GET("/system/wifi/status",      s.adminWifiStatus)
		g.POST("/system/wifi/connect",    s.adminConnectWifi)
	}
}

// adminInitGridWallets leitet alle Betreiber-Wallets aus den Seed-Wörtern ab.
// Body: { "words": ["Wort1", "Wort2", ...] }
// Die Wörter werden nach der Verarbeitung nicht gespeichert.
func (s *Server) adminInitGridWallets(c *gin.Context) {
	var req struct {
		Words []string `json:"words" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Words) < 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Zu wenige Seed-Wörter (min. 10)"})
		return
	}

	s.log.Info("Grid-Wallet-Ableitung gestartet",
		zap.Int("operators", len(grid.AllOperators())),
	)

	done := 0
	wallets, err := grid.DeriveAllWallets(req.Words, func(d, total int) {
		done = d
		if d%10 == 0 || d == total {
			s.log.Info("Grid-Wallets abgeleitet", zap.Int("done", d), zap.Int("total", total))
		}
	})

	// Seed-Wörter aus Request-Slice löschen (best-effort)
	for i := range req.Words {
		req.Words[i] = ""
	}

	if err != nil {
		s.internalError(c, err)
		return
	}

	// In Cache speichern
	cache := make(map[string]*grid.OperatorWallet, len(wallets))
	for _, w := range wallets {
		cache[w.OperatorID] = w
	}
	s.gridWallets = cache

	c.JSON(http.StatusOK, gin.H{
		"ok":    true,
		"count": done,
		"note":  "Grid-Wallets wurden abgeleitet und im RAM gecacht (gehen bei Neustart verloren).",
	})
}

// adminListGridWallets gibt alle gecachten Grid-Wallets zurück.
func (s *Server) adminListGridWallets(c *gin.Context) {
	if s.gridWallets == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Noch nicht initialisiert – POST /api/v1/admin/grid/init aufrufen",
		})
		return
	}

	type entry struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Commodity string `json:"commodity"`
		Address  string `json:"address"`
	}
	list := make([]entry, 0, len(s.gridWallets))
	for _, w := range s.gridWallets {
		list = append(list, entry{
			ID:       w.OperatorID,
			Name:     w.Name,
			Commodity: string(w.Commodity),
			Address:  w.Address,
		})
	}
	c.JSON(http.StatusOK, gin.H{"wallets": list, "count": len(list)})
}

// adminVersion gibt die aktuelle Version zurück.
func (s *Server) adminVersion(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version":   s.version,
		"fee_collector": config.KanonischeFeeCollector,
	})
}

// adminPublishUpdate empfängt ein signiertes Manifest und verbreitet es im P2P-Netz.
// Nur über localhost erreichbar – kein externer Zugriff.
func (s *Server) adminPublishUpdate(c *gin.Context) {
	if s.node == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "P2P-Node nicht verbunden"})
		return
	}

	var m update.Manifest
	if err := c.ShouldBindJSON(&m); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Ungültiges Manifest: " + err.Error()})
		return
	}

	// Signatur prüfen bevor verbreitet wird
	if err := m.Verify(); err != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Signaturprüfung fehlgeschlagen: " + err.Error(),
		})
		return
	}

	// Im P2P-Netz verbreiten
	data, err := json.Marshal(m)
	if err != nil {
		s.internalError(c, err)
		return
	}

	if err := s.node.Publish(c.Request.Context(), p2p.TopicUpdate, data); err != nil {
		s.internalError(c, err)
		return
	}

	s.log.Info("Update-Manifest verbreitet",
		zap.String("version", m.Version),
		zap.String("argon2",  m.NodeArgon2[:16]+"…"),
	)

	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"version": m.Version,
		"argon2":  m.NodeArgon2[:16] + "…",
		"peers":   len(s.node.Peers()),
	})
}

// adminCheckConfig prüft die aktuelle Konfiguration und gibt das Ergebnis zurück.
func (s *Server) adminCheckConfig(c *gin.Context) {
	result := s.cfg.Validate(s.log)

	status := http.StatusOK
	if !result.OK {
		status = http.StatusConflict
	}

	c.JSON(status, gin.H{
		"ok":            result.OK,
		"errors":        result.Errors,
		"warnings":      result.Warnings,
		"fee_collector": config.KanonischeFeeCollector,
		"version":       s.version,
	})
}

// adminStorageExpand erweitert die Disk-Allokation ohne Node-Neustart.
//
// POST /api/v1/admin/storage/expand
// Body: { "alloc_gb": 500, "offer_gb": 100 }
//
// Regeln:
//   alloc_gb ≥ 5 × offer_gb  (eigene + 4 Spiegel-Chunks)
//   alloc_gb > aktuell        (keine Verkleinerung)
func (s *Server) adminStorageExpand(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Filesharing nicht aktiv (FUNDUS_STORAGE_OFFER_GB=0)",
		})
		return
	}

	var req struct {
		AllocGB int64 `json:"alloc_gb" binding:"required"`
		OfferGB int64 `json:"offer_gb"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := s.fileStore.Expand(req.AllocGB, req.OfferGB)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s.log.Info("Storage-Allokation erweitert",
		zap.Int64("newAllocGB", result.NewAllocGB),
		zap.Int64("newOfferGB", result.NewOfferGB),
	)

	c.JSON(http.StatusOK, gin.H{
		"ok":           true,
		"old_alloc_gb": result.OldAllocGB,
		"new_alloc_gb": result.NewAllocGB,
		"old_offer_gb": result.OldOfferGB,
		"new_offer_gb": result.NewOfferGB,
		"alloc_file":   result.AllocFile,
		"disk_free_gb": result.DiskFreeGB,
		"ratio":        result.NewAllocGB / max64Int64(result.NewOfferGB, 1),
		"note":         "Erweiterung aktiv – kein Neustart nötig.",
	})
}

// adminStorageStats zeigt detaillierte Disk-Statistiken.
//
// GET /api/v1/admin/storage/stats
func (s *Server) adminStorageStats(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{
			"active": false,
			"note":   "Filesharing deaktiviert (FUNDUS_STORAGE_OFFER_GB=0)",
		})
		return
	}

	disk := s.fileStore.DiskStats()
	billing := s.fileStore.Stats()

	// Flache Felder fuer die UI (settings.lua/admin.lua lesen d.offer_gb etc.)
	resp := gin.H{
		"active":      true,
		"offer_gb":    billing["offer_gb"],
		"alloc_gb":    disk["alloc_total_gb"],
		"used_gb":     disk["alloc_used_gb"],
		"free_gb":     disk["alloc_free_gb"],
		"storage_dir": disk["alloc_file"],
		"chunks":      disk["chunk_count"],
		// verschachtelt fuer Detailansichten
		"disk":    disk,
		"billing": billing,
		"rules": gin.H{
			"min_alloc_ratio": 5,
			"description":     "alloc_gb muss ≥ 5 × offer_gb sein",
			"reason":          "eigene Chunks (1×) + 4 Spiegel-Chunks anderer Nodes",
		},
	}
	c.JSON(http.StatusOK, resp)
}

func max64Int64(a, b int64) int64 {
	if a > b { return a }
	return b
}

// adminPurgeOrphans löscht alle verwaisten (unsignierten) Altbestand-Listings.
// Diese stammen aus der Zeit vor persistenten Identitäten/Signaturen und
// "gehören" keiner aktuellen Node-ID mehr. Jedes wird als signierter Tombstone
// markiert und ins Netz propagiert, sodass es auf allen Peers verschwindet.
//
// POST /api/v1/admin/listings/purge-orphans
func (s *Server) adminPurgeOrphans(c *gin.Context) {
	records, err := s.store.List(storage.RecordListing)
	if err != nil {
		s.internalError(c, err)
		return
	}

	purged := 0
	var ids []string
	myID := s.nodeID()
	mode := c.Query("mode") // "orphans" (default) | "all-foreign"
	for _, r := range records {
		// Verwaist = gehört nicht der aktuellen (stabilen) Node-ID.
		// Das erfasst sowohl unsignierte Altbestände als auch Angebote die
		// mit einer früheren (nicht-persistenten) Peer-ID signiert wurden.
		isOwn := (r.OwnerID != "" && r.OwnerID == myID)
		isOrphan := !isOwn

		// Im Default-Modus nur löschen wenn verwaist. "all-foreign" ebenso —
		// hier identisch, aber als expliziter Schalter dokumentiert.
		_ = mode
		if !isOrphan {
			continue
		}
		now := time.Now()
		tombstone := &storage.Record{
			ID:        r.ID,
			Type:      storage.RecordListing,
			OwnerID:   myID, // adoptieren, damit signierter Tombstone gültig ist
			CreatedAt: r.CreatedAt,
			UpdatedAt: now,
			DeletedAt: &now,
			Data:      r.Data,
		}
		if s.node != nil {
			if sig, err := s.node.SignData(tombstone.SigningBytes()); err == nil {
				tombstone.Signature = sig
			}
		}
		// Direkt lokal schreiben (Admin-Befehl, umgeht Sync-Owner-Checks)
		_ = s.store.Put(tombstone)
		if s.node != nil {
			if data, err := marshalRecord(tombstone); err == nil {
				node := s.node
				go func() {
					pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = node.Publish(pctx, p2p.TopicListings, data)
				}()
			}
		}
		purged++
		ids = append(ids, r.ID)
	}

	s.log.Info("Verwaiste Angebote gelöscht", zap.Int("count", purged))
	c.JSON(http.StatusOK, gin.H{
		"purged": purged,
		"ids":    ids,
		"note":   "Verwaiste Angebote (fremde/alte OwnerID) lokal gelöscht. Bitte auf JEDEM Pi einmal ausführen.",
	})
}

// GET /api/v1/admin/uploads/sessions – laufende Resume-Uploads anzeigen
func (s *Server) adminListUploadSessions(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{"sessions": []any{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": s.fileStore.ListResumeSessions()})
}

// POST /api/v1/admin/uploads/purge – alle unfertigen Resume-Uploads loeschen
func (s *Server) adminPurgeUploadSessions(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{"purged": 0})
		return
	}
	n := s.fileStore.PurgeResumeSessions()
	s.log.Info("Verwaiste Uploads bereinigt", zap.Int("count", n))
	c.JSON(http.StatusOK, gin.H{"purged": n})
}

// adminPurgePartnerAds löscht alle empfangenen Partner-Ads (Match- und
// Such-Index) aus dem lokalen Store. Aktive Peers propagieren ihre aktuellen
// Ads danach neu — veraltete Duplikate (alte Peer-IDs, Ads ohne FundusID)
// verschwinden dadurch. POST /api/v1/admin/partner/purge-stale
func (s *Server) adminPurgePartnerAds(c *gin.Context) {
	purged := 0
	for _, rt := range []storage.RecordType{storage.RecordPartnerAd, storage.RecordPartnerSearchAd} {
		records, err := s.store.List(rt)
		if err != nil {
			continue
		}
		for _, r := range records {
			// Eigene Ads (my-ad:*, my-search-ad) behalten — nur empfangene löschen.
			if len(r.ID) >= 7 && (r.ID[:7] == "peer-ad" || (len(r.ID) >= 12 && r.ID[:12] == "peer-search-")) {
				if s.store.Delete(rt, r.ID) == nil {
					purged++
				}
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"purged": purged, "note": "Empfangene Partner-Ads gelöscht; aktive Peers propagieren neu."})
}
