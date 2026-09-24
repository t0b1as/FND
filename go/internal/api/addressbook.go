package api

// Wallet-Adressbuch: lokale Sammlung von Empfangsadressen (Wallet-Adresse +
// Beschreibung), die der Nutzer pflegt. Wird bei der Kaufabwicklung / beim
// Erstellen von Anzeigen als Empfangsadresse angeboten. Rein lokal — NICHT im
// P2P-Netz geteilt (RecordAddressBook steht nicht in PublicRecordTypes).

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
)

// walletAddrRe validiert eine Fundus-Wallet-Adresse (0x + 40 Hex-Zeichen).
var walletAddrRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// listAddressBook liefert alle Einträge des Wallet-Adressbuchs.
// GET /api/v1/addressbook
func (s *Server) listAddressBook(c *gin.Context) {
	records, err := s.store.List(storage.RecordAddressBook)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"entries": []interface{}{}})
		return
	}
	entries := make([]gin.H, 0, len(records))
	for _, r := range records {
		entries = append(entries, gin.H{
			"id":          r.ID,
			"address":     r.Data["address"],
			"description": r.Data["description"],
			"created_at":  r.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries, "count": len(entries)})
}

// addAddressBookEntry fügt einen Eintrag hinzu (Adresse + Beschreibung).
// POST /api/v1/addressbook  Body: { "address": "0x...", "description": "..." }
func (s *Server) addAddressBookEntry(c *gin.Context) {
	var req struct {
		Address     string `json:"address"     binding:"required"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	addr := strings.TrimSpace(req.Address)
	if !walletAddrRe.MatchString(addr) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Wallet-Adresse (erwartet 0x + 40 Hex-Zeichen)"})
		return
	}
	// Duplikat-Prüfung: dieselbe Adresse nicht zweimal.
	if records, err := s.store.List(storage.RecordAddressBook); err == nil {
		for _, r := range records {
			if a, _ := r.Data["address"].(string); strings.EqualFold(a, addr) {
				c.JSON(http.StatusConflict, gin.H{"error": "Adresse ist bereits im Adressbuch"})
				return
			}
		}
	}
	record := &storage.Record{
		ID:        generateID(),
		Type:      storage.RecordAddressBook,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Data: map[string]any{
			"address":     addr,
			"description": strings.TrimSpace(req.Description),
		},
	}
	if err := s.store.Put(record); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": record.ID, "address": addr})
}

// deleteAddressBookEntry entfernt einen Eintrag.
// DELETE /api/v1/addressbook/:id
func (s *Server) deleteAddressBookEntry(c *gin.Context) {
	id := c.Param("id")
	if err := s.store.Delete(storage.RecordAddressBook, id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Eintrag nicht gefunden"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
