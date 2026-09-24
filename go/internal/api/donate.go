package api

// Freiwillige Spende per PayPal (ohne Gegenleistung).
//
// Bewusst KEINE Verknüpfung mit FND: Eine Zahlung, für die man Token im selben
// Wert erhält, wäre rechtlich ein Verkauf (MiCA/BaFin) und widerspräche den
// PayPal-Regeln für Spenden; zudem sind PayPal-Zahlungen rückbuchbar, FND-
// Überweisungen nicht. FND gibt es über den atomaren Tausch FND ⇄ SOL.

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GET /api/v1/donate/config
func (s *Server) donateConfig(c *gin.Context) {
	addr := ""
	if s.cfg != nil {
		addr = s.cfg.DonatePayPal
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":  addr != "",
		"paypal":   addr,
		"currency": "EUR",
	})
}
