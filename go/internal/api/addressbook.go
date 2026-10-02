package api

// Wallet-Adressbuch: lokale Sammlung von Empfangsadressen (Wallet-Adresse +
// Beschreibung), die der Nutzer pflegt. Wird bei der Kaufabwicklung / beim
// Erstellen von Anzeigen als Empfangsadresse angeboten. Rein lokal — NICHT im
// P2P-Netz geteilt (RecordAddressBook steht nicht in PublicRecordTypes).

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
)

// walletAddrRe validiert eine Fundus-Wallet-Adresse (0x + 40 Hex-Zeichen).
var walletAddrRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// solAddrRe: Solana-Adresse (Base58, 32–44 Zeichen).
var solAddrRe = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)

// addrKind erkennt die Art einer Adresse: "fnd", "sol" oder "" (ungültig).
func addrKind(a string) string {
	switch {
	case walletAddrRe.MatchString(a):
		return "fnd"
	case solAddrRe.MatchString(a):
		return "sol"
	}
	return ""
}

// Besitzer eines Eintrags (R553): die Fundus-ID des angemeldeten Nutzers.
// Einträge ohne Besitzer gehören dem Node (im Heimnetz ohne Anmeldung angelegt).
// Im Heimnetz sieht/ändert der Betreiber alles; von außen jeder nur seine eigenen.
func (s *Server) addrBookOwner(c *gin.Context) (fid string, lan bool) {
	return strings.ToLower(s.sessionFID(c)), isLANRequest(c)
}

func addrBookVisible(r *storage.Record, fid string, lan bool) bool {
	owner, _ := r.Data["owner"].(string)
	return lan || (fid != "" && strings.EqualFold(owner, fid))
}

// listAddressBook liefert die sichtbaren Einträge des Wallet-Adressbuchs.
// GET /api/v1/addressbook
func (s *Server) listAddressBook(c *gin.Context) {
	fid, lan := s.addrBookOwner(c)
	if !lan && fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	records, err := s.store.List(storage.RecordAddressBook)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"entries": []interface{}{}})
		return
	}
	entries := make([]gin.H, 0, len(records))
	for _, r := range records {
		if !addrBookVisible(r, fid, lan) {
			continue
		}
		entries = append(entries, gin.H{
			"owner":       r.Data["owner"],
			"id":          r.ID,
			"address":     r.Data["address"],
			"kind":        addrKind(strings.TrimSpace(fmt.Sprint(r.Data["address"]))),
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
	fid, lan := s.addrBookOwner(c)
	if !lan && fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	addr := strings.TrimSpace(req.Address)
	kind := addrKind(addr)
	if kind == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Adresse (FND: 0x + 40 Hex-Zeichen, Solana: Base58)"})
		return
	}
	// Duplikat-Prüfung: dieselbe Adresse nicht zweimal beim selben Besitzer.
	if records, err := s.store.List(storage.RecordAddressBook); err == nil {
		for _, r := range records {
			owner, _ := r.Data["owner"].(string)
			if a, _ := r.Data["address"].(string); strings.EqualFold(a, addr) && strings.EqualFold(owner, fid) {
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
			"kind":        kind,
			"description": strings.TrimSpace(req.Description),
			"owner":       fid, // "" = Node (Heimnetz ohne Anmeldung)
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
	fid, lan := s.addrBookOwner(c)
	if rec, err := s.store.Get(storage.RecordAddressBook, id); err != nil || rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Eintrag nicht gefunden"})
		return
	} else if !addrBookVisible(rec, fid, lan) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Nur eigene Einträge können gelöscht werden."})
		return
	}
	if err := s.store.Delete(storage.RecordAddressBook, id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Eintrag nicht gefunden"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// updateAddressBookEntry ändert Beschreibung und/oder Adresse eines eigenen Eintrags.
// PUT /api/v1/addressbook/:id  Body: { "address"?: "...", "description"?: "..." }
func (s *Server) updateAddressBookEntry(c *gin.Context) {
	id := c.Param("id")
	fid, lan := s.addrBookOwner(c)
	rec, err := s.store.Get(storage.RecordAddressBook, id)
	if err != nil || rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Eintrag nicht gefunden"})
		return
	}
	if !addrBookVisible(rec, fid, lan) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Nur eigene Einträge können geändert werden."})
		return
	}
	var req struct {
		Address     *string `json:"address"`
		Description *string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Address != nil {
		a := strings.TrimSpace(*req.Address)
		kind := addrKind(a)
		if kind == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Adresse (FND: 0x + 40 Hex-Zeichen, Solana: Base58)"})
			return
		}
		rec.Data["address"] = a
		rec.Data["kind"] = kind
	}
	if req.Description != nil {
		rec.Data["description"] = strings.TrimSpace(*req.Description)
	}
	rec.UpdatedAt = time.Now()
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": rec.ID, "address": rec.Data["address"], "description": rec.Data["description"]})
}
