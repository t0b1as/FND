package api

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

)

// registerShopRoutes hängt die Shop-Endpunkte ein.
func (s *Server) registerShopRoutes() {
	if s.shopFeed == nil {
		return // Shop nicht konfiguriert (weder Voll- noch Anzeige-Modus)
	}

	g := s.router.Group("/api/v1/shop")
	{
		// Kurs abrufen
		g.GET("/price",    s.shopGetPrice)

		// Kauf-Auftrag erstellen
		g.POST("/order",   s.shopCreateOrder)

		// Auftragsstatus abfragen
		g.GET("/order/:id", s.shopGetOrder)

		// Empfangsadresse (für QR-Code)
		g.GET("/address",  s.shopGetAddress)
		g.GET("/pay-url",  s.shopPayURL)      // Solana-Pay-URL (für QR-Code) erzeugen
	}
}

// shopGetPrice gibt den aktuellen SOL/EUR-Kurs und ein Beispiel-Angebot zurück.
//
// Query-Parameter:
//   amount  – gewünschter FND-Betrag (Standard: 10)
func (s *Server) shopGetPrice(c *gin.Context) {
	amount := 10.0
	if a := c.Query("amount"); a != "" {
		if parsed := parseQueryFloat(c, "amount"); parsed > 0 {
			amount = parsed
		}
	}

	rate, source, err := s.shopFeed.SOLEUR(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Kurs nicht verfügbar: " + err.Error(),
		})
		return
	}

	solAmt, err := s.shopFeed.SOLForEUR(c.Request.Context(), amount)
	if err != nil {
		s.internalError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"sol_eur":      rate,
		"source":       source,
		"example": gin.H{
			"fnd":         amount,
			"sol":         solAmt.SOL,
			"sol_raw":     solAmt.RawSOL,
			"slippage_bps": solAmt.SlippageBPS,
			"valid_until": solAmt.ValidUntil,
			"lamports":    solAmt.Lamports(),
		},
	})
}

// shopCreateOrder erstellt einen neuen Kaufauftrag.
//
// Body: { "fnd_amount": 10.0, "recipient_addr": "0x..." }
func (s *Server) shopCreateOrder(c *gin.Context) {
	var req struct {
		FNDAmount     float64 `json:"fnd_amount"     binding:"required"`
		RecipientAddr string  `json:"recipient_addr" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.FNDAmount <= 0 || req.FNDAmount > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fnd_amount muss zwischen 0 und 10.000 liegen"})
		return
	}

	if s.shopWatcher == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Dieser Node zeigt den Shop nur an — Zahlung läuft direkt per QR-Code/Memo, kein Auftrag nötig."})
		return
	}
	order, err := s.shopWatcher.CreateOrder(
		c.Request.Context(), req.FNDAmount, req.RecipientAddr,
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Empfangs-Adresse mitgeben damit der Nutzer direkt zahlen kann
	c.JSON(http.StatusCreated, gin.H{
		"order":           order,
		"pay_to_address":  s.shopReceiveAddr,
		"pay_sol":         order.SOLExpected,
		"pay_lamports":    uint64(order.SOLExpected * 1e9),
		"expires_in_sec":  int(order.ExpiresAt.Sub(order.CreatedAt).Seconds()),
	})
}

// shopGetOrder gibt den aktuellen Status eines Auftrags zurück.
func (s *Server) shopGetOrder(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "order id required"})
		return
	}

	if s.shopWatcher == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Auftrags-Status nur auf dem verarbeitenden Shop-Node verfügbar."})
		return
	}
	order, ok := s.shopWatcher.GetOrder(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	c.JSON(http.StatusOK, order)
}

// shopGetAddress gibt die Solana-Empfangsadresse für QR-Codes zurück.
func (s *Server) shopGetAddress(c *gin.Context) {
	// Öffentlicher Endpunkt des Netzes – NIE die konfigurierte Adresse: die
	// kann einen API-Schlüssel enthalten und ging früher an jeden Browser.
	rpc := publicSolRPC()
	c.JSON(http.StatusOK, gin.H{
		"solana_address": s.shopReceiveAddr,
		"rpc_url":        rpc, // öffentlicher RPC (für Phantom-Zahlung im Browser)
		"note":           "Sende SOL an diese Adresse mit deiner FND-Adresse im Memo.",
	})
}

// shopPayURL erzeugt eine Solana-Pay-URL für einen QR-Code. Der Käufer scannt sie
// mit seiner Solana-Wallet (Phantom o.ä.), die Betrag, Empfänger UND die FND-
// Zieladresse (als Memo) automatisch ausfüllt — keine Kommandozeile, kein
// manuelles Memo-Tippen. Parameter: fnd_addr (FND-Zieladresse), amount_fnd.
func (s *Server) shopPayURL(c *gin.Context) {
	fndAddr := c.Query("fnd_addr")
	if fndAddr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fnd_addr fehlt (FND-Zieladresse)"})
		return
	}
	amountFND := parseQueryFloat(c, "amount_fnd")
	if amountFND <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount_fnd muss > 0 sein"})
		return
	}
	// FND-Betrag → benötigte SOL (1 FND = 1 EUR).
	solAmt, err := s.shopFeed.SOLForEUR(c.Request.Context(), amountFND)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Kurs nicht verfügbar: " + err.Error()})
		return
	}
	recipient := s.shopReceiveAddr

	// Solana-Pay-URL: solana:<empfänger>?amount=<sol>&memo=<fnd-adresse>&label=...
	// Der Betrag mit ausreichend Nachkommastellen (Lamports-genau).
	q := url.Values{}
	q.Set("amount", fmt.Sprintf("%.9f", solAmt.SOL))
	q.Set("memo", fndAddr) // die FND-Zieladresse reist als Memo mit
	q.Set("label", "Fundus FND")
	q.Set("message", fmt.Sprintf("%.2f FND an %s", amountFND, fndAddr))
	payURL := "solana:" + recipient + "?" + q.Encode()

	c.JSON(http.StatusOK, gin.H{
		"pay_url":     payURL,
		"recipient":   recipient,
		"sol_amount":  solAmt.SOL,
		"fnd_amount":  amountFND,
		"fnd_addr":    fndAddr,
		"valid_until": solAmt.ValidUntil,
	})
}
