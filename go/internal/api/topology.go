package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/fundus/node/internal/topology"
)

func (s *Server) registerTopologyRoutes() {
	if s.topology == nil {
		return
	}
	g := s.router.Group("/api/v1/topology")
	{
		g.GET("/profile",      s.topoProfile)       // eigenes Profil
		g.GET("/nodes",        s.topoNodes)          // alle bekannten Nodes
		g.GET("/substations",  s.topoSubstations)    // alle Trafostationen
		g.GET("/route",        s.topoRoute)          // Handelspfad berechnen
		g.POST("/connections", s.topoAddConnection)  // Verbindung hinzufügen
		g.DELETE("/connections/:peer", s.topoRemoveConnection)
		g.PUT("/fee",          s.topoSetFee)         // Trafo-Gebühr konfigurieren
	}
}

// topoProfile gibt das eigene Node-Profil zurück.
func (s *Server) topoProfile(c *gin.Context) {
	c.JSON(http.StatusOK, s.topology.OwnProfile())
}

// topoNodes gibt alle bekannten Nodes im Graphen zurück.
func (s *Server) topoNodes(c *gin.Context) {
	graph := s.topology.Graph()
	// Alle Substations + eigenes Profil zurückgeben
	subs := graph.Substations()
	c.JSON(http.StatusOK, gin.H{
		"known_nodes": s.topology.KnownNodes(),
		"substations": len(subs),
		"own":         s.topology.OwnProfile(),
	})
}

// topoSubstations listet alle bekannten Trafostationen auf.
func (s *Server) topoSubstations(c *gin.Context) {
	subs := s.topology.Graph().Substations()
	c.JSON(http.StatusOK, gin.H{"substations": subs, "count": len(subs)})
}

// topoRoute berechnet den optimalen Handelspfad zu einem anderen Node.
//
// GET /api/v1/topology/route?to=<peerID>&price=100
func (s *Server) topoRoute(c *gin.Context) {
	toPeerID := c.Query("to")
	if toPeerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "to (Peer-ID) fehlt"})
		return
	}
	var priceAmount float64
	if p := c.Query("price"); p != "" {
		parsed, err := strconv.ParseFloat(p, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "price ungültig"})
			return
		}
		priceAmount = parsed
	}

	route, err := s.topology.FindRoute(c.Request.Context(), toPeerID, priceAmount)
	if err != nil {
		s.internalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"route":      route,
		"trade_mode": route.TradeMode,
		"hops":       len(route.Hops),
		"fee_percent": route.Fees.TotalFeePercent,
	})
}

// topoAddConnection fügt eine Verbindung zu einem Nachbar-Node hinzu.
//
// POST /api/v1/topology/connections
// Body: { "peer_id": "12D3...", "lat": 48.14, "lon": 11.58,
//         "voltage": "lv", "wallet": "0x..." }
func (s *Server) topoAddConnection(c *gin.Context) {
	var req struct {
		PeerID  string                  `json:"peer_id"  binding:"required"`
		Lat     float64                 `json:"lat"`
		Lon     float64                 `json:"lon"`
		Voltage topology.VoltageLevel   `json:"voltage"`
		Wallet  string                  `json:"wallet"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Voltage == "" {
		req.Voltage = topology.VoltageLV
	}

	if err := s.topology.AddConnection(
		c.Request.Context(),
		req.PeerID, req.Lat, req.Lon, req.Voltage, req.Wallet,
	); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"peer_id": req.PeerID,
		"connections": len(s.topology.OwnProfile().Connections),
	})
}

// topoRemoveConnection entfernt eine Verbindung.
func (s *Server) topoRemoveConnection(c *gin.Context) {
	peerID := c.Param("peer")
	s.topology.RemoveConnection(c.Request.Context(), peerID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// topoSetFee konfiguriert die Durchleitungsgebühr (nur Trafostationen).
//
// PUT /api/v1/topology/fee
// Body: { "transit_fee_percent": 0.5 }
func (s *Server) topoSetFee(c *gin.Context) {
	own := s.topology.OwnProfile()
	if own.Type != topology.NodeTypeSubstation {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Gebühr nur für Trafostationen konfigurierbar (FUNDUS_NODE_TYPE=substation)",
			"current_type": string(own.Type),
		})
		return
	}

	var req struct {
		TransitFeePercent float64 `json:"transit_fee_percent" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.TransitFeePercent < 0 || req.TransitFeePercent > 10 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Gebühr muss zwischen 0% und 10% liegen",
		})
		return
	}

	s.topology.UpdateFee(req.TransitFeePercent)
	c.JSON(http.StatusOK, gin.H{
		"ok":                 true,
		"transit_fee_percent": req.TransitFeePercent,
		"wallet":             own.WalletAddress,
		"note": "Gebühr aktiv. Wird bei Transaktionen durch diese Trafostation berechnet.",
	})
}
