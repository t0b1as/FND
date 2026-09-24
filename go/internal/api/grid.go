package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fundus/node/internal/topology"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/grid"
)

// registerGridRoutes hängt die Grid-Endpunkte ein.
func (s *Server) registerGridRoutes() {
	g := s.router.Group("/api/v1/grid")
	{
		g.GET("/operators",  s.gridOperators)
		g.GET("/wallet",     s.gridWallet)
		g.GET("/list",       s.gridList)

		// Gebühren-Berechnung (nur LV/MV, HV ignoriert)
		g.GET("/fee",        s.gridFee)

		// Admin: Netzbetreiber konfiguriert seine Gebühr
		g.POST("/fee/configure", s.gridFeeConfig)
		g.GET("/fee/rates",      s.gridFeeRates)
	}

	// Kapazitäts-Management (Ansatz 3)
	cap := s.router.Group("/api/v1/grid/capacity")
	{
		cap.GET("/status",       s.capacityStatus)      // Eigener Trafo-Status
		cap.GET("/remote/:peer", s.capacityRemote)      // Fremder Trafo-Status
		cap.GET("/route",        s.capacityRoute)       // Route-Validierung
		cap.POST("/register",    s.capacityRegister)    // Trade ins Accounting einbuchen
		cap.POST("/unregister",  s.capacityUnregister)  // Trade aus Accounting ausbuchen
	}

	// Trade-Settlement
	trade := s.router.Group("/api/v1/grid/trade")
	{
		trade.POST("",           s.tradeStart)          // Trade starten
		trade.GET("/:id",        s.tradeGet)            // Trade-Status
		trade.GET("",            s.tradeList)           // Alle Trades
		trade.POST("/:id/settle",s.tradeSettle)         // Manuell settlen
		trade.POST("/:id/cancel",s.tradeCancel)         // Stornieren
	}
}

// gridFee berechnet die Netzgebühr für eine Transaktion.
//
// GET /api/v1/grid/fee?from_plz=80333&to_plz=10115&price=100
// Antwort: Distanz, Betreiber, Gebührsatz, Gebührbetrag, Nettobetrag
func (s *Server) gridFee(c *gin.Context) {
	fromPLZ := strings.TrimSpace(c.Query("from_plz"))
	toPLZ   := strings.TrimSpace(c.Query("to_plz"))
	priceStr := c.Query("price")

	if fromPLZ == "" || toPLZ == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "from_plz und to_plz erforderlich",
		})
		return
	}

	price := 0.0
	if priceStr != "" {
		fmt.Sscanf(priceStr, "%f", &price)
	}

	result := grid.CalculateGridFee(price, fromPLZ, toPLZ)

	c.JSON(http.StatusOK, gin.H{
		"from_plz":          result.FromPLZ,
		"to_plz":            result.ToPLZ,
		"distance_km":       fmt.Sprintf("%.1f", result.DistanceKM),
		"operator_id":       result.OperatorID,
		"operator_name":     result.OperatorName,
		"fee_percent_per_km": result.FeePercentPerKM,
		"total_fee_percent":  fmt.Sprintf("%.4f", result.TotalFeePercent),
		"fee_amount_fnd":    fmt.Sprintf("%.6f", result.FeeAmount),
		"net_amount_fnd":    fmt.Sprintf("%.6f", result.NetAmount),
		"note": "Gebühr geht an LV/MV-Netzbetreiber des Käufers. " +
			"HV-Betreiber werden nicht berücksichtigt.",
	})
}

// gridFeeConfig erlaubt einem Netzbetreiber seine Gebühr zu konfigurieren.
//
// POST /api/v1/grid/fee/configure
// Body: { "operator_id": "BDEW-9909", "fee_percent_per_km": 0.002 }
//
// Hinweis: Nur LV/MV-Betreiber werden bei der Preisfindung berücksichtigt.
// HV-Betreiber können konfiguriert werden, erhalten aber keine Gebühr.
func (s *Server) gridFeeConfig(c *gin.Context) {
	var req struct {
		OperatorID      string  `json:"operator_id"       binding:"required"`
		FeePercentPerKM float64 `json:"fee_percent_per_km" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	op := grid.ByID(req.OperatorID)
	if op == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Betreiber nicht gefunden: " + req.OperatorID,
		})
		return
	}

	// HV-Warnung (aber nicht ablehnen – Betreiber darf konfigurieren)
	warning := ""
	if grid.IsHV(op) {
		warning = "HV-Betreiber werden bei der Preisfindung nicht berücksichtigt " +
			"(nur LV/MV). Die Konfiguration wird gespeichert, hat aber keinen Effekt " +
			"auf Transaktionsgebühren."
	}

	if err := grid.SetOperatorFee(req.OperatorID, req.FeePercentPerKM); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	s.log.Info("Grid-Gebühr konfiguriert",
		zap.String("operator", req.OperatorID),
		zap.Float64("fee_per_km", req.FeePercentPerKM),
	)

	resp := gin.H{
		"ok":               true,
		"operator_id":      req.OperatorID,
		"operator_name":    op.Name,
		"level":            op.Level,
		"fee_percent_per_km": req.FeePercentPerKM,
		"example": fmt.Sprintf("Bei 50 km Luftlinie = %.2f%% Gebühr",
			req.FeePercentPerKM*50),
	}
	if warning != "" {
		resp["warning"] = warning
	}
	c.JSON(http.StatusOK, resp)
}

// gridFeeRates gibt alle konfigurierten Gebühren zurück.
//
// GET /api/v1/grid/fee/rates
func (s *Server) gridFeeRates(c *gin.Context) {
	all  := grid.AllOperators()
	fees := grid.AllOperatorFees()

	type entry struct {
		ID              string  `json:"id"`
		Name            string  `json:"name"`
		Level           string  `json:"level"`
		Commodity       string  `json:"commodity"`
		FeePercentPerKM float64 `json:"fee_percent_per_km"`
		IsHV            bool    `json:"is_hv"`
		ParticipatesInFee bool  `json:"participates_in_fee"`
	}

	result := make([]entry, 0)
	for _, op := range all {
		fee := fees[op.ID]
		if fee == 0 && op.Level == "hv" {
			continue // HV ohne Konfiguration weglassen
		}
		result = append(result, entry{
			ID:              op.ID,
			Name:            op.Name,
			Level:           op.Level,
			Commodity:       string(op.Commodity),
			FeePercentPerKM: fee,
			IsHV:            op.Level == "hv",
			ParticipatesInFee: op.Level != "hv",
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"rates": result,
		"note":  "Nur LV/MV-Betreiber nehmen an der Gebührenberechnung teil. HV-Betreiber sind zur Information gelistet.",
	})
}

// gridOperators gibt alle zuständigen Netzbetreiber für eine PLZ zurück.
//
// GET /api/v1/grid/operators?plz=80333
func (s *Server) gridOperators(c *gin.Context) {
	plz := strings.TrimSpace(c.Query("plz"))
	if len(plz) < 4 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plz fehlt oder zu kurz (min. 4 Zeichen)"})
		return
	}

	results := grid.FindAllOperators(plz)
	state   := grid.StateForPLZ(plz)

	type operatorResponse struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Commodity string `json:"commodity"`
		Level     string `json:"level"`
		BDEWCode  string `json:"bdew_code,omitempty"`
		DVGWCode  string `json:"dvgw_code,omitempty"`
		MatchType string `json:"match_type"`
		Note      string `json:"note,omitempty"`
		Website   string `json:"website,omitempty"`
	}

	ops := make([]operatorResponse, 0, len(results))
	for _, r := range results {
		if r.Operator == nil {
			continue
		}
		ops = append(ops, operatorResponse{
			ID:        r.Operator.ID,
			Name:      r.Operator.Name,
			Commodity: string(r.Operator.Commodity),
			Level:     r.Operator.Level,
			BDEWCode:  r.Operator.BDEWCode,
			DVGWCode:  r.Operator.DVGWCode,
			MatchType: r.MatchType,
			Note:      r.Note,
			Website:   r.Operator.Website,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"plz":       plz,
		"state":     state,
		"operators": ops,
		"count":     len(ops),
		"note":      "Wallet-Adressen über /api/v1/grid/wallet abrufbar (nach Admin-Initialisierung)",
	})
}

// gridWallet gibt die Wallet-Adresse für einen Netzbetreiber zurück.
// Die Wallets werden beim Node-Start gecacht – keine Seed-Wörter nötig.
//
// GET /api/v1/grid/wallet?operator_id=BDEW-9909
// GET /api/v1/grid/wallet?plz=80333&commodity=electricity_lv
func (s *Server) gridWallet(c *gin.Context) {
	// Wallet-Cache prüfen
	if s.gridWallets == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Grid-Wallets noch nicht initialisiert. Admin-Befehl: POST /api/v1/admin/grid/init",
		})
		return
	}

	// Suche nach operator_id
	operatorID := c.Query("operator_id")
	if operatorID == "" {
		// Alternativ: PLZ + Commodity
		plz       := c.Query("plz")
		commodity := grid.Commodity(c.Query("commodity"))
		if plz == "" || commodity == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "operator_id oder (plz + commodity) angeben",
			})
			return
		}
		result := grid.FindOperator(plz, commodity)
		if result.Operator == nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "Kein Betreiber für diese PLZ und Sparte gefunden",
				"note":  result.Note,
			})
			return
		}
		operatorID = result.Operator.ID
	}

	// In Cache nachschlagen
	w, ok := s.gridWallets[operatorID]
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Wallet für Betreiber " + operatorID + " nicht gefunden",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"operator_id": w.OperatorID,
		"name":        w.Name,
		"commodity":   string(w.Commodity),
		"address":     w.Address,
		"address_short": grid.WalletAddressShort(w.Address),
	})
}

// gridList gibt alle bekannten Netzbetreiber zurück.
//
// GET /api/v1/grid/list?commodity=electricity_lv
// GET /api/v1/grid/list?state=BY
func (s *Server) gridList(c *gin.Context) {
	commodityFilter := grid.Commodity(c.Query("commodity"))
	stateFilter     := strings.ToUpper(c.Query("state"))

	all := grid.AllOperators()
	type opEntry struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Commodity string   `json:"commodity"`
		Level     string   `json:"level"`
		States    []string `json:"states"`
		BDEWCode  string   `json:"bdew_code,omitempty"`
		DVGWCode  string   `json:"dvgw_code,omitempty"`
		Website   string   `json:"website,omitempty"`
		HasWallet bool     `json:"has_wallet"`
	}

	result := make([]opEntry, 0, len(all))
	for _, op := range all {
		if commodityFilter != "" && op.Commodity != commodityFilter {
			continue
		}
		if stateFilter != "" {
			found := false
			for _, s := range op.States {
				if s == stateFilter {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		_, hasWallet := s.gridWallets[op.ID]
		result = append(result, opEntry{
			ID:        op.ID,
			Name:      op.Name,
			Commodity: string(op.Commodity),
			Level:     op.Level,
			States:    op.States,
			BDEWCode:  op.BDEWCode,
			DVGWCode:  op.DVGWCode,
			Website:   op.Website,
			HasWallet: hasWallet,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"operators": result,
		"count":     len(result),
	})
}

// =============================================================================
//  Kapazitäts-Endpunkte
// =============================================================================

// capacityStatus gibt den aktuellen Stand des eigenen Kapazitäts-UTXOs zurück.
//
// GET /api/v1/grid/capacity/status
func (s *Server) capacityStatus(c *gin.Context) {
	if s.capacityMgr == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled": false,
			"reason":  "Dieser Node ist kein Trafo-Node (FUNDUS_NODE_TYPE != substation)",
		})
		return
	}
	status := s.capacityMgr.Status()
	c.JSON(http.StatusOK, gin.H{
		"enabled": true,
		"status":  status,
	})
}

// capacityRemote gibt den bekannten Status eines fremden Trafo-Nodes zurück.
//
// GET /api/v1/grid/capacity/remote/:peer
func (s *Server) capacityRemote(c *gin.Context) {
	peerID := c.Param("peer")
	if s.capacityMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein CapacityManager aktiv"})
		return
	}
	cert, err := s.capacityMgr.GetRemoteCert(c.Request.Context(), peerID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"peer_id":      peerID,
		"cert":         cert,
		"cert_age_s":   time.Since(cert.Timestamp).Seconds(),
		"open_ws":       cert.OpenWs,
		"contracted_ws": cert.ContractedWs,
		"actual_ws":     cert.ActualWs,
	})
}

// capacityRoute validiert die Kapazität aller Trafos auf einer Route.
//
// GET /api/v1/grid/capacity/route?trafos=peer1,peer2,peer3&kw=10.5
func (s *Server) capacityRoute(c *gin.Context) {
	if s.capacityMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein CapacityManager aktiv"})
		return
	}
	trafosParam := c.Query("trafos")
	kwParam := c.Query("kw")
	if trafosParam == "" || kwParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trafos und kw sind Pflichtparameter"})
		return
	}

	var requestedKW float64
	if _, err := fmt.Sscanf(kwParam, "%f", &requestedKW); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kw muss eine Zahl sein"})
		return
	}

	trafos := strings.Split(trafosParam, ",")
	checks, err := s.capacityMgr.ValidateRoute(c.Request.Context(), trafos, requestedKW)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Route insgesamt OK wenn alle Trafos entweder Kapazität haben oder Fallback
	allOK := true
	for _, ch := range checks {
		if !ch.Sufficient {
			allOK = false
			break
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"route_ok":     allOK,
		"requested_kw": requestedKW,
		"checks":       checks,
	})
}

// capacityRegister bucht einen Trade ins Kapazitäts-Accounting ein.
// Prüft available_kw im letzten Cert und erhöht contracted_kw.
// Das nächste sekündliche Cert enthält den aktualisierten Wert.
//
// POST /api/v1/grid/capacity/register
// Body: { trade_id, buyer_id, seller_id, power_kw, ends_at, route_hash }
func (s *Server) capacityRegister(c *gin.Context) {
	if s.capacityMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein CapacityManager aktiv"})
		return
	}
	var req struct {
		TradeID   string    `json:"trade_id"  binding:"required"`
		BuyerID   string    `json:"buyer_id"  binding:"required"`
		SellerID  string    `json:"seller_id" binding:"required"`
		QuoteWs   float64   `json:"quote_ws"  binding:"required"` // Ws pro Sekunde (= Watt)
		EndsAt    time.Time `json:"ends_at"`
		RouteHash string    `json:"route_hash"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var routeHash [32]byte
	fmt.Sscanf(req.RouteHash, "%x", &routeHash)

	if err := s.capacityMgr.RegisterTrade(
		req.TradeID, req.BuyerID, req.SellerID,
		req.QuoteWs, req.EndsAt, routeHash,
	); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	status := s.capacityMgr.Status()
	c.JSON(http.StatusOK, gin.H{
		"registered":    true,
		"contracted_ws": status.ContractedWs,
		"open_ws":        status.OpenWs,
	})
}

// capacityUnregister bucht einen Trade aus dem Accounting aus.
//
// POST /api/v1/grid/capacity/unregister
// Body: { trade_id }
func (s *Server) capacityUnregister(c *gin.Context) {
	if s.capacityMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kein CapacityManager aktiv"})
		return
	}
	var req struct {
		TradeID string `json:"trade_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.capacityMgr.UnregisterTrade(req.TradeID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	status := s.capacityMgr.Status()
	c.JSON(http.StatusOK, gin.H{
		"unregistered":  true,
		"contracted_ws": status.ContractedWs,
		"open_ws":        status.OpenWs,
	})
}

// =============================================================================
//  Trade-Settlement-Endpunkte
// =============================================================================

// tradeStart startet einen neuen Energie-Trade mit Kapazitätsreservierung.
//
// POST /api/v1/grid/trade
// Body: { buyer_id, seller_id, escrow_id, power_kw, duration_hours, route }
func (s *Server) tradeStart(c *gin.Context) {
	if s.settlementEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Settlement-Engine nicht aktiv"})
		return
	}

	var req struct {
		BuyerID       string   `json:"buyer_id"       binding:"required"`
		SellerID      string   `json:"seller_id"      binding:"required"`
		EscrowID      string   `json:"escrow_id"      binding:"required"`
		QuoteWs       float64  `json:"quote_ws"       binding:"required"` // Ws/s (= Watt)
		DurationHours float64  `json:"duration_hours" binding:"required"`
		RouteTrafos   []string `json:"route_trafos"`  // Peer-IDs der Trafo-Nodes
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tradeID := fmt.Sprintf("trade-%d", time.Now().UnixMilli())
	duration := time.Duration(req.DurationHours * float64(time.Hour))

	trade, err := s.settlementEngine.StartTrade(
		c.Request.Context(),
		tradeID, req.BuyerID, req.SellerID, req.EscrowID,
		req.QuoteWs, duration,
		topology.GridRoute{}, // Route wird aus Dijkstra befüllt
		req.RouteTrafos,
	)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"trade_id":  trade.TradeID,
		"status":    trade.Status,
		"quote_ws":  trade.QuoteWs,
		"ends_at":   trade.EndsAt,
		"hops":      len(trade.Hops),
	})
}

// tradeGet gibt den Status eines Trades zurück.
//
// GET /api/v1/grid/trade/:id
func (s *Server) tradeGet(c *gin.Context) {
	if s.settlementEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Settlement-Engine nicht aktiv"})
		return
	}
	trade, ok := s.settlementEngine.GetTrade(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Trade nicht gefunden"})
		return
	}
	c.JSON(http.StatusOK, trade)
}

// tradeList listet alle Trades auf.
//
// GET /api/v1/grid/trade
func (s *Server) tradeList(c *gin.Context) {
	if s.settlementEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Settlement-Engine nicht aktiv"})
		return
	}
	trades := s.settlementEngine.ListTrades()
	c.JSON(http.StatusOK, gin.H{
		"trades": trades,
		"count":  len(trades),
	})
}

// tradeSettle führt das manuelle Settlement eines Trades durch.
//
// POST /api/v1/grid/trade/:id/settle
func (s *Server) tradeSettle(c *gin.Context) {
	if s.settlementEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Settlement-Engine nicht aktiv"})
		return
	}
	result, err := s.settlementEngine.SettleTrade(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// tradeCancel storniert einen laufenden Trade.
//
// POST /api/v1/grid/trade/:id/cancel
func (s *Server) tradeCancel(c *gin.Context) {
	if s.settlementEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Settlement-Engine nicht aktiv"})
		return
	}
	if err := s.settlementEngine.CancelTrade(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"cancelled": true})
}
