package api

// Bewertungen von Handelspartnern (R533).
//
// Anker ist ein ABGESCHLOSSENES Escrow auf der Fundus-Chain: Nur Käufer und
// Verkäufer dieses Escrows dürfen jeweils die Gegenseite bewerten (eine
// Bewertung pro Seite und Handel). Die Bewertung ist mit dem Chain-Schlüssel des
// Bewertenden signiert. JEDER Node prüft beim Anzeigen selbst – Signatur,
// Escrow-Zustand und Rollen auf der Chain –, statt dem liefernden Node zu
// vertrauen. Eine erfundene Bewertung bräuchte damit einen echten, bezahlten
// Handel auf der Chain.

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"lukechampine.com/blake3"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
)

type ratingView struct {
	EscrowID string `json:"escrow_id"`
	Rater    string `json:"rater"`
	Ratee    string `json:"ratee"`
	Role     string `json:"role"` // Rolle des Bewertenden: "buyer" | "seller"
	Stars    int    `json:"stars"`
	Comment  string `json:"comment,omitempty"`
	Time     int64  `json:"time"`
}

func ratingDigest(escrow, rater, ratee string, stars int, t int64, comment string) [32]byte {
	return blake3.Sum256([]byte(fmt.Sprintf("fundus-rating-v1|%s|%s|%s|%d|%d|%s", escrow, rater, ratee, stars, t, comment)))
}

func anyInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case int:
		return int64(x), true
	case int64:
		return x, true
	}
	return 0, false
}

// verifyRating prüft eine Bewertung vollständig gegen die Chain.
func (s *Server) verifyRating(d map[string]any) (ratingView, bool) {
	var r ratingView
	if s.chain == nil || d == nil {
		return r, false
	}
	r.EscrowID, _ = d["escrow_id"].(string)
	r.Rater, _ = d["rater"].(string)
	r.Ratee, _ = d["ratee"].(string)
	r.Comment, _ = d["comment"].(string)
	sigHex, _ := d["sig"].(string)
	st, ok1 := anyInt64(d["stars"])
	tm, ok2 := anyInt64(d["time"])
	if !ok1 || !ok2 || st < 1 || st > 5 || r.Rater == "" || r.Ratee == "" || r.Rater == r.Ratee {
		return r, false
	}
	r.Stars, r.Time = int(st), tm
	id, ok := parseEscrowID(r.EscrowID)
	if !ok {
		return r, false
	}
	esc, found := s.chain.GetEscrow(id)
	if !found || esc.State != chain.EscrowClosed {
		return r, false
	}
	buyer, seller := strings.ToLower(esc.Buyer.Hex()), strings.ToLower(esc.Seller.Hex())
	switch {
	case r.Rater == buyer && r.Ratee == seller:
		r.Role = "buyer"
	case r.Rater == seller && r.Ratee == buyer:
		r.Role = "seller"
	default:
		return r, false
	}
	sig, err := hex.DecodeString(trimHexPrefix(strings.TrimSpace(sigHex)))
	if err != nil || len(sig) != 65 {
		return r, false
	}
	dg := ratingDigest(r.EscrowID, r.Rater, r.Ratee, r.Stars, r.Time, r.Comment)
	pub, err := crypto.SigToPub(dg[:], sig)
	if err != nil || strings.ToLower(chain.PubkeyToAddress(pub).Hex()) != r.Rater {
		return r, false
	}
	return r, true
}


func (s *Server) registerRatingRoutes() {
	g := s.router.Group("/api/v1/ratings")
	g.GET("", s.ratingsFor)                 // ?addr=0x… – Bewertungen einer Adresse + Schnitt
	g.GET("/escrow/:id", s.ratingsForEscrow) // Handel: Parteien, Status, vorhandene Bewertungen
	g.POST("", s.ratingCreate)              // eigene Bewertung abgeben (angemeldet)
}

// allRatings: alle gültigen Bewertungen (jede auf diesem Node geprüft).
func (s *Server) allRatings() []ratingView {
	recs, err := s.store.List(storage.RecordRating)
	if err != nil {
		return nil
	}
	out := make([]ratingView, 0, len(recs))
	for _, rec := range recs {
		if rec == nil || rec.DeletedAt != nil {
			continue
		}
		if r, ok := s.verifyRating(rec.Data); ok {
			out = append(out, r)
		}
	}
	return out
}

// GET /api/v1/ratings?addr=0x…
func (s *Server) ratingsFor(c *gin.Context) {
	addr := strings.ToLower(strings.TrimSpace(c.Query("addr")))
	if addr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "addr fehlt"})
		return
	}
	list := make([]ratingView, 0)
	sum := 0
	for _, r := range s.allRatings() {
		if r.Ratee == addr {
			list = append(list, r)
			sum += r.Stars
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Time > list[j].Time })
	avg := 0.0
	if len(list) > 0 {
		avg = float64(sum) / float64(len(list))
	}
	c.JSON(http.StatusOK, gin.H{"addr": addr, "count": len(list), "avg": avg, "ratings": list})
}

// GET /api/v1/ratings/escrow/:id
func (s *Server) ratingsForEscrow(c *gin.Context) {
	idStr := strings.ToLower(trimHexPrefix(c.Param("id")))
	id, ok := parseEscrowID(idStr)
	if !ok || s.chain == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Escrow-ID"})
		return
	}
	esc, found := s.chain.GetEscrow(id)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Escrow nicht auf der Chain gefunden"})
		return
	}
	buyer, seller := strings.ToLower(esc.Buyer.Hex()), strings.ToLower(esc.Seller.Hex())
	list := make([]ratingView, 0, 2)
	for _, r := range s.allRatings() {
		if r.EscrowID == idStr {
			list = append(list, r)
		}
	}
	me := ""
	if sess := s.getSession(c); sess != nil && sess.identity != nil {
		if k, err := sess.identity.ChainPrivateKey(); err == nil && k != nil {
			me = strings.ToLower(chain.PubkeyToAddress(&k.PublicKey).Hex())
		}
	}
	role := ""
	if me == buyer {
		role = "buyer"
	} else if me == seller {
		role = "seller"
	}
	c.JSON(http.StatusOK, gin.H{
		"escrow_id": idStr, "buyer": buyer, "seller": seller,
		"closed": esc.State == chain.EscrowClosed, "my_role": role,
		"can_rate": role != "" && esc.State == chain.EscrowClosed,
		"ratings": list,
	})
}

// POST /api/v1/ratings {escrow_id, stars, comment}
func (s *Server) ratingCreate(c *gin.Context) {
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		EscrowID string `json:"escrow_id"`
		Stars    int    `json:"stars"`
		Comment  string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Stars < 1 || req.Stars > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bitte 1 bis 5 Sterne angeben."})
		return
	}
	comment := strings.TrimSpace(strings.ReplaceAll(req.Comment, "\r", ""))
	if utf8.RuneCountInString(comment) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Kommentar höchstens 500 Zeichen."})
		return
	}
	idStr := strings.ToLower(trimHexPrefix(req.EscrowID))
	id, ok := parseEscrowID(idStr)
	if !ok || s.chain == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Escrow-ID"})
		return
	}
	esc, found := s.chain.GetEscrow(id)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Escrow nicht auf der Chain gefunden"})
		return
	}
	if esc.State != chain.EscrowClosed {
		c.JSON(http.StatusConflict, gin.H{"error": "Bewerten erst nach Abschluss des Handels möglich."})
		return
	}
	key, err := sess.identity.ChainPrivateKey()
	if err != nil || key == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Wallet dieser Anmeldung nicht verfügbar – bitte Wallet öffnen."})
		return
	}
	me := strings.ToLower(chain.PubkeyToAddress(&key.PublicKey).Hex())
	buyer, seller := strings.ToLower(esc.Buyer.Hex()), strings.ToLower(esc.Seller.Hex())
	var ratee string
	switch me {
	case buyer:
		ratee = seller
	case seller:
		ratee = buyer
	default:
		c.JSON(http.StatusForbidden, gin.H{"error": "Nur Käufer und Verkäufer dieses Handels dürfen bewerten."})
		return
	}
	now := time.Now().Unix()
	dg := ratingDigest(idStr, me, ratee, req.Stars, now, comment)
	sig, err := crypto.Sign(dg[:], key)
	if err != nil {
		s.internalError(c, err)
		return
	}
	data := map[string]any{
		"escrow_id": idStr, "rater": me, "ratee": ratee,
		"stars": req.Stars, "comment": comment, "time": now,
		"sig": fmt.Sprintf("%x", sig),
	}
	ownerID := ""
	if s.node != nil {
		ownerID = s.node.ID().String()
	}
	rec := &storage.Record{
		ID:        "rating-" + idStr + "-" + me, // eine Bewertung pro Seite und Handel (neu = ersetzt)
		Type:      storage.RecordRating,
		OwnerID:   ownerID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Data:      data,
	}
	if s.node != nil {
		if nsig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = nsig
		}
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	if s.node != nil {
		if raw, err := marshalRecord(rec); err == nil {
			_ = s.node.Publish(c.Request.Context(), p2p.TopicRatings, raw)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "ratee": ratee})
}
