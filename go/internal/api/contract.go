package api

// Kaufvertrag-Endpunkt
//
// GET /api/v1/contracts/:escrowId
//      → JSON mit vollständigen Vertragsdaten
//
// GET /api/v1/contracts/:escrowId/html
//      → Kaufvertrag als druckoptimiertes HTML (Browser → PDF über window.print())
//
// Der Kaufvertrag enthält:
//   - Vertragsparteien (Käufer/Verkäufer via Wallet-Adresse)
//   - Gegenstand (Listing-Daten)
//   - Kaufpreis + Gebühren
//   - Zahlungsbedingungen (14-Tage-Einfrierung)
//   - Widerrufsrecht + Rücksendepflicht
//   - Escrow-Status und Fristen
//   - Blockchain-Nachweis (Escrow-ID, Tx-Hash)

import (
	"context"
	"go.uber.org/zap"
	"sort"
	"encoding/json"
	"strconv"
	"sync"
	"strings"
	"html/template"
	"lukechampine.com/blake3"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
)

func (s *Server) registerContractRoutes() {
	g := s.router.Group("/api/v1/contracts")
	{
		g.GET("/preview",        s.contractPreview)  // Vorschau aus Listing (vor Kauf)
		g.GET("/:escrowId",      s.contractJSON)
		g.GET("/:escrowId/html", s.contractHTML)
	}
	e := s.router.Group("/api/v1/escrow")
	{
		e.POST("",                    s.escrowCreate)
		e.POST("/:id/fund",           s.escrowFund)
		e.POST("/:id/confirm",        s.escrowConfirmReceipt)
		e.POST("/:id/cancel",         s.escrowCancel)
		e.POST("/:id/return",         s.escrowSubmitReturn)
		e.POST("/:id/confirm-return", s.escrowConfirmReturn)
		e.GET("",                     s.escrowList) // offene Hinterlegungen (R572)
		e.GET("/:id",                 s.escrowGet)
	}
}

// Escrow erstellen
// escrowDefaultDeadlineBlocks: Standard-Frist einer Escrow-Eröffnung, wenn der
// Client keine explizite Höhe angibt. ~14 Tage bei ~1 Block/Minute (Richtwert;
// die echte Blockrate hängt vom Produzenten ab).
// 14 Tage bei chain.BlockTime Sekunden je Block (R561: war 14*24*60 und damit
// auf 60-Sekunden-Blöcke gerechnet – die Chain läuft mit 5 s, die Frist betrug
// real nur 28 Stunden).
const escrowDefaultDeadlineBlocks uint64 = 14 * 24 * 60 * 60 / chain.BlockTime

// blockIntervalSeconds ist die angenommene Blockzeit für die Umrechnung von
// Block-Deadlines in Anzeige-Zeitpunkte (Vertragsvorschau). Richtwert; die echte
// Rate hängt vom Produzenten ab (Phase 2: Single-Producer, on-demand).
const blockIntervalSeconds = chain.BlockTime

// fundusChainID ist die Chain-ID der eigenen Fundus-Chain (NICHT die Gnosis-ID
// 100 aus der Config, die zum alten ERC-20-Pfad gehört).
const fundusChainID = 4242

// uToFNDFloat wandelt uFND (kleinste Einheit) in einen FND-Float für die Anzeige.
// Nur für Vertragsanzeige – Konsens-/Bilanzlogik nutzt ausschließlich Ganzzahlen.
func uToFNDFloat(u *big.Int) float64 {
	if u == nil {
		return 0
	}
	per := new(big.Float).SetInt64(chain.UFNDPerFND)
	f := new(big.Float).Quo(new(big.Float).SetInt(u), per)
	v, _ := f.Float64()
	return v
}

// escrowStateString gibt den Anzeige-Status eines Escrow-Zustands zurück.
func escrowStateString(st chain.EscrowState) string {
	switch st {
	case chain.EscrowOpen:
		return "open"
	case chain.EscrowDisputed:
		return "disputed"
	case chain.EscrowClosed:
		return "closed"
	case chain.EscrowCancelRequested:
		return "cancel_requested"
	case chain.EscrowReturnSubmitted:
		return "return_submitted"
	default:
		return "unknown"
	}
}

// trimHexPrefix entfernt ein optionales "0x"/"0X"-Präfix.
func trimHexPrefix(s string) string {
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		return s[2:]
	}
	return s
}

// parseEscrowID liest eine 32-Byte-Escrow-ID aus hex (mit/ohne 0x).
func parseEscrowID(s string) ([32]byte, bool) {
	var id [32]byte
	raw, err := hex.DecodeString(trimHexPrefix(s))
	if err != nil || len(raw) != 32 {
		return id, false
	}
	copy(id[:], raw)
	return id, true
}

// submitEscrowTx kapselt das gemeinsame Muster aller Escrow-Aktionen:
// Seed-Wörter → Schlüssel ableiten (BLAKE3-Adresse, Phase 2.4) → Nonce holen →
// signierte Tx bauen → in den Mempool. Gibt Tx-Hash + Absender + Nonce zurück.
// Der Schlüssel wird nach Gebrauch nullgesetzt.
func (s *Server) submitEscrowTx(words []string, txType chain.TxType, fee *big.Int, payload []byte) (txHash string, from string, nonce uint64, err error) {
	if s.chain == nil || s.mempool == nil {
		return "", "", 0, fmt.Errorf("Chain nicht aktiv")
	}
	if len(words) == 0 {
		return "", "", 0, fmt.Errorf("Seed-Wörter fehlen")
	}
	priv, derr := identity.DerivePrivateKeyFromSeed(words)
	if derr != nil {
		return "", "", 0, fmt.Errorf("Ableitung fehlgeschlagen")
	}
	defer priv.D.SetInt64(0) // Schlüssel nach Gebrauch nullen

	addr := chain.PubkeyToAddress(&priv.PublicKey)
	_, n := s.chain.AccountInfo(addr)
	tx, berr := chain.BuildSignedTx(priv, txType, fee, payload, n)
	if berr != nil {
		return "", "", 0, berr
	}
	if merr := s.mempool.Add(tx); merr != nil {
		return "", "", 0, merr
	}
	h := tx.Hash()
	return hex.EncodeToString(h[:]), addr.Hex(), n, nil
}

// escrowCreate öffnet einen Escrow auf der eigenen Chain (TxEscrowOpen 0x30).
// Die Escrow-ID ist der Hash der Eröffnungs-Tx (= tx_hash der Antwort).
func (s *Server) escrowCreate(c *gin.Context) {
	var req struct {
		Words       []string `json:"words"`
		ListingID   string   `json:"listing_id"`
		Seller      string   `json:"seller"`
		AmountFND   any      `json:"amount_fnd"` // Text oder Zahl (R571)
		Quantity    int      `json:"quantity"`
		Delivery    string   `json:"delivery"`
		DeadlineH   uint64   `json:"deadline_height"`
		ContentHash string   `json:"content_hash"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	seller, _, serr := s.resolvePayee(req.Seller)
	if serr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Verkäufer-Adresse: " + serr.Error()})
		return
	}
	amount, ok := parseFNDtoU(anyToFNDString(req.AmountFND))
	if !ok || amount.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger Betrag"})
		return
	}
	var contentHash [32]byte
	// Content-Hash: entweder direkt übergeben, oder aus dem Listing nachschlagen
	// (sicherer — das Frontend muss ihn nicht kennen/fälschen können).
	chHex := req.ContentHash
	if chHex == "" && req.ListingID != "" {
		if rec, err := s.store.Get(storage.RecordListing, req.ListingID); err == nil && rec != nil {
			if h, ok := rec.Data["content_hash"].(string); ok && h != "" {
				chHex = h
			} else {
				// Alte Anzeige ohne gespeicherten Hash: on-the-fly berechnen
				// (gleiche Formel wie createListing), damit der Vertrag nie 0 zeigt.
				d := rec.Data
				hstr := fmt.Sprintf("%v|%v|%v|%v|%v",
					d["title"], d["description"], d["category"], d["condition"], d["image_hashes"])
				sum := blake3.Sum256([]byte(hstr))
				chHex = "0x" + hex.EncodeToString(sum[:])
			}
		}
	}
	if chHex != "" {
		raw, herr := hex.DecodeString(trimHexPrefix(chHex))
		if herr != nil || len(raw) != 32 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger content_hash (32 Byte hex erwartet)"})
			return
		}
		copy(contentHash[:], raw)
	}
	// Deadline: falls nicht gesetzt, ~14 Tage in Blöcken (konservativ via aktueller Höhe).
	deadline := req.DeadlineH
	if deadline == 0 {
		deadline = s.chain.Height() + escrowDefaultDeadlineBlocks
	}
	payload := (&chain.EscrowOpenPayload{
		Seller: seller, Amount: amount, Deadline: deadline, ContentHash: contentHash,
	}).Encode()
	fee := chain.FeeForValue(amount) // 1,8 % Pflichtgebühr für Escrow-Eröffnung

	// Guthabenprüfung: der Käufer muss Betrag + Gebühr in seiner Wallet haben,
	// SONST wird der Kauf abgelehnt (statt eine ungedeckte Tx zu erzeugen).
	if s.chain != nil && len(req.Words) > 0 {
		if priv, derr := identity.DerivePrivateKeyFromSeed(req.Words); derr == nil {
			buyerAddr := chain.PubkeyToAddress(&priv.PublicKey)
			priv.D.SetInt64(0) // Schlüssel sofort nullen
			balStr := s.chain.Balance(buyerAddr)
			bal, _ := new(big.Int).SetString(balStr, 10)
			if bal == nil {
				bal = big.NewInt(0)
			}
			need := new(big.Int).Add(amount, fee)
			if bal.Cmp(need) < 0 {
				c.JSON(http.StatusBadRequest, gin.H{
					"error":       "Nicht genug FND in der Wallet",
					"balance_fnd": uToFNDFloat(bal),
					"needed_fnd":  uToFNDFloat(need),
				})
				return
			}
		}
	}

	// Bestand VOR dem Senden prüfen und reservieren (R562): Früher wurde das
	// Listing erst NACH der Transaktion als verkauft markiert und vorher nie
	// geprüft – derselbe Artikel ließ sich mehrfach kaufen.
	qty := req.Quantity
	if qty < 1 {
		qty = 1
	}
	shippingFND := 0.0
	// Betrag gegen Stückpreis × Menge prüfen (R569): Sonst ließen sich mehrere
	// Stück zum Preis von einem hinterlegen. Toleranz für Rundung im Browser.
	if req.ListingID != "" && qty > 0 {
		if rec, gerr := s.store.Get(storage.RecordListing, req.ListingID); gerr == nil && rec != nil {
			if strings.EqualFold(strings.TrimSpace(req.Delivery), "shipping") {
				if sc, sok := toFloatOK(rec.Data["shipping_cost"]); sok && sc > 0 {
					shippingFND = sc
				}
			}
			if unit, uok := toFloatOK(rec.Data["price_min"]); uok && unit > 0 {
				want := unit*float64(qty) + shippingFND
				got := uToFNDFloat(amount)
				if got < want-0.005 {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": fmt.Sprintf("Betrag passt nicht: %d × %.2f FND + %.2f FND Versand = %.2f FND", qty, unit, shippingFND, want)})
					return
				}
			}
		}
	}
	release := func() {}
	if req.ListingID != "" {
		ok, left, rerr := s.reserveListingStock(req.ListingID, qty)
		if rerr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": rerr.Error()})
			return
		}
		if !ok {
			msg := "Dieser Artikel ist nicht mehr verfügbar."
			if left > 0 {
				msg = fmt.Sprintf("Nur noch %d Stück verfügbar.", left)
			}
			c.JSON(http.StatusConflict, gin.H{"error": msg})
			return
		}
		release = func() { s.releaseListingStock(req.ListingID, qty) } // bei Fehlschlag zurückgeben
	}
	txHash, from, nonce, err := s.submitEscrowTx(req.Words, chain.TxEscrowOpen, fee, payload)
	if err != nil {
		release()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Kauf am Listing vermerken (der Kaufbeweis selbst liegt unveränderlich als
	// Escrow-Tx auf der Chain; das Listing ist nur der Marktplatz-Eintrag).

	// Kaufvermerk (R570): Menge, Übergabeart und Versandkosten gehören in den
	// Kaufvertrag, stehen aber nicht auf der Chain. Keine Adresse – die geht
	// wie bisher verschlüsselt per Messenger an den Verkäufer.
	delivery := "pickup"
	if strings.EqualFold(strings.TrimSpace(req.Delivery), "shipping") {
		delivery = "shipping"
	}
	_ = s.store.Put(&storage.Record{
		ID:        "purchase-" + strings.ToLower(trimHexPrefix(txHash)),
		Type:      storage.RecordPurchase,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Data: map[string]any{
			"listing_id": req.ListingID, "quantity": qty,
			"delivery": delivery, "shipping_fnd": shippingFND,
		},
	})
	if req.ListingID != "" {
		s.settleListingStock(req.ListingID, qty) // Kaufvermerk zählt ab jetzt
		// Verkauf SOFORT dem Verkäufer-Node melden (R578). Ohne das müsste der
		// Bestand über den Datenabgleich wandern – das dauerte Minuten.
		go s.notifySaleToOwner(req.ListingID, txHash, qty)
	}
	// Escrow-ID = Tx-Hash der Eröffnung (so erzeugt applyEscrowOpen die ID).
	c.JSON(http.StatusOK, gin.H{
		"escrow_id": txHash, "tx_hash": txHash, "listing_id": req.ListingID,
		"from": from, "nonce": nonce, "status": "open",
	})
}

// escrowConfirmReceipt: Käufer bestätigt Lieferung → Freigabe an Verkäufer
// (TxEscrowConfirm 0x31).
func (s *Server) escrowConfirmReceipt(c *gin.Context) {
	s.escrowRefAction(c, chain.TxEscrowConfirm, "released")
}

// escrowCancel: Käufer beantragt Storno (TxEscrowCancel 0x35).
func (s *Server) escrowCancel(c *gin.Context) {
	s.escrowRefAction(c, chain.TxEscrowCancel, "cancel_requested")
}

// escrowConfirmReturn: Verkäufer (oder Käufer nach Frist) bestätigt Rückerhalt →
// Refund an Käufer (TxEscrowConfirmReturn 0x37).
func (s *Server) escrowConfirmReturn(c *gin.Context) {
	s.escrowRefAction(c, chain.TxEscrowConfirmReturn, "refunded")
}

// escrowRefAction kapselt alle Aktionen, die nur eine Escrow-ID referenzieren
// (confirm/cancel/confirm-return). Erwartet {words, escrow_id} bzw. nutzt den
// URL-Parameter :id als Escrow-ID (hex der Eröffnungs-Tx).
func (s *Server) escrowRefAction(c *gin.Context, txType chain.TxType, okStatus string) {
	var req struct {
		Words    []string `json:"words"`
		EscrowID string   `json:"escrow_id"`
	}
	c.ShouldBindJSON(&req)
	idHex := req.EscrowID
	if idHex == "" {
		idHex = c.Param("id")
	}
	id, ok := parseEscrowID(idHex)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Escrow-ID"})
		return
	}
	payload := (&chain.EscrowRefPayload{EscrowID: id}).Encode()
	txHash, from, nonce, err := s.submitEscrowTx(req.Words, txType, nil, payload)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"escrow_id": idHex, "tx_hash": txHash, "from": from, "nonce": nonce, "status": okStatus,
	})
}

// escrowSubmitReturn: Käufer hinterlegt Rücksende-Tracking-Hash
// (TxEscrowSubmitReturn 0x36).
func (s *Server) escrowSubmitReturn(c *gin.Context) {
	var req struct {
		Words        []string `json:"words"`
		EscrowID     string   `json:"escrow_id"`
		TrackingHash string   `json:"tracking_hash"`
	}
	c.ShouldBindJSON(&req)
	idHex := req.EscrowID
	if idHex == "" {
		idHex = c.Param("id")
	}
	id, ok := parseEscrowID(idHex)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Escrow-ID"})
		return
	}
	var trk [32]byte
	raw, herr := hex.DecodeString(trimHexPrefix(req.TrackingHash))
	if herr != nil || len(raw) != 32 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültiger tracking_hash (32 Byte hex erwartet)"})
		return
	}
	copy(trk[:], raw)
	payload := (&chain.EscrowReturnPayload{EscrowID: id, TrackingHash: trk}).Encode()
	txHash, from, nonce, err := s.submitEscrowTx(req.Words, chain.TxEscrowSubmitReturn, nil, payload)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"escrow_id": idHex, "tx_hash": txHash, "from": from, "nonce": nonce,
		"tracking_hash": req.TrackingHash, "status": "return_submitted",
	})
}

// escrowFund ist auf der eigenen Chain KEIN separater Schritt — die Eröffnung
// (escrowCreate / TxEscrowOpen) sperrt den Betrag bereits. Der Endpunkt bleibt
// aus Kompatibilität bestehen und meldet das.
func (s *Server) escrowFund(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"escrow_id": c.Param("id"),
		"status":    "funded",
		"note":      "Auf der Fundus-Chain sperrt bereits die Eröffnung den Betrag; kein separater Fund-Schritt nötig.",
	})
}

func (s *Server) escrowGet(c *gin.Context) {
	id, ok := parseEscrowID(c.Param("id"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ungültige Escrow-ID"})
		return
	}
	if s.chain == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht aktiv"})
		return
	}
	esc, found := s.chain.GetEscrow(id)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"escrow_id": c.Param("id"), "status": "unknown"})
		return
	}
	resp := gin.H{
		"escrow_id":    c.Param("id"),
		"buyer":        esc.Buyer.Hex(),
		"seller":       esc.Seller.Hex(),
		"amount_fnd":   uToFNDFloat(esc.Amount),
		"deadline":     esc.Deadline,
		"content_hash": "0x" + hex.EncodeToString(esc.ContentHash[:]),
		"status":       escrowStateString(esc.State),
		"insured":      esc.Insured,
	}
	if esc.State == chain.EscrowReturnSubmitted || esc.State == chain.EscrowCancelRequested {
		resp["return_deadline"] = esc.ReturnDeadline
		if esc.TrackingHash != ([32]byte{}) {
			resp["return_tracking_hash"] = "0x" + hex.EncodeToString(esc.TrackingHash[:])
		}
	}
	c.JSON(http.StatusOK, resp)
}

// ContractData enthält alle Daten für den Kaufvertrag.
type ContractData struct {
	EscrowID       string    `json:"escrow_id"`
	ListingID      string    `json:"listing_id"`
	CreatedAt      time.Time `json:"created_at"`
	FreezeDeadline time.Time `json:"freeze_deadline"`
	ReturnDeadline time.Time `json:"return_deadline"`

	// Parteien
	BuyerWallet  string `json:"buyer_wallet"`
	SellerWallet string `json:"seller_wallet"`

	// Ware
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Condition   string  `json:"condition"`
	Category    string  `json:"category"`
	ContentHash string  `json:"content_hash"`

	// Preis
	PriceFND    float64 `json:"price_fnd"`
	ShippingFND float64 `json:"shipping_fnd,omitempty"` // im Kaufpreis enthaltene Versandkosten
	DeliveryTxt string  `json:"delivery_txt,omitempty"` // "Versand" oder "Selbstabholung"
	FeeFND      float64 `json:"fee_fnd"`
	NetFND      float64 `json:"net_fnd"`
	GridFeeFND  float64 `json:"grid_fee_fnd,omitempty"`

	// Status
	Status      string `json:"status"`
	CancelReason string `json:"cancel_reason,omitempty"`
	ReturnTrackingHash string `json:"return_tracking_hash,omitempty"`

	// Blockchain
	ChainID     int    `json:"chain_id"`
	ChainName   string `json:"chain_name"`
	MarketAddr  string `json:"market_contract"`
	NodeID      string `json:"node_id"`
}

// contractJSON gibt Vertragsdaten als JSON zurück.
func (s *Server) contractJSON(c *gin.Context) {
	data, err := s.buildContractData(c.Param("escrowId"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

// contractHTML generiert einen druckoptimierten Kaufvertrag als HTML.
// Der Browser kann diesen direkt als PDF drucken/speichern.
func (s *Server) contractHTML(c *gin.Context) {
	data, err := s.buildContractData(c.Param("escrowId"))
	if err != nil {
		// Direkt nach dem Kauf steht die Hinterlegung noch in keinem Block
		// (alle 5 Sekunden einer). Statt einer Fehlermeldung eine Seite, die
		// sich selbst neu lädt, sobald der Vertrag bereitsteht.
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusAccepted, contractPendingHTML)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(
		`inline; filename="kaufvertrag-%s.pdf"`, data.EscrowID,
	))

	html := buildContractHTML(data)
	// Link zur Bewertung des Handelspartners (erst nach Abschluss aktiv; die
	// Seite erklärt das). Nach dem Formatieren eingesetzt, damit die
	// Platzhalter der Vorlage unverändert bleiben.
	rate := `<p style="text-align:center;margin:18px 0"><a href="/ratings?escrow=` +
		template.HTMLEscapeString(data.EscrowID) + `" style="font-weight:600">⭐ Handelspartner bewerten</a></p>`
	html = strings.Replace(html, "<footer>", rate+"\n<footer>", 1)
	c.String(http.StatusOK, html)
}

// buildContractData lädt die Vertragsdaten aus Storage und Blockchain.
// contractPreview rendert einen Kaufvertrags-Entwurf aus einem Listing,
// BEVOR ein Escrow existiert. GET /api/v1/contracts/preview?listing=<id>
func (s *Server) contractPreview(c *gin.Context) {
	listingID := c.Query("listing")
	if listingID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "listing-Parameter fehlt"})
		return
	}
	if s.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Store nicht verfügbar"})
		return
	}
	rec, err := s.store.Get(storage.RecordListing, listingID)
	if err != nil || rec == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Angebot nicht gefunden"})
		return
	}

	d := rec.Data
	getStr := func(k string) string {
		if v, ok := d[k].(string); ok {
			return v
		}
		return "–"
	}
	getFloat := func(k string) float64 {
		if v, ok := d[k].(float64); ok {
			return v
		}
		return 0
	}

	now := time.Now().UTC()
	price := getFloat("price_min")
	fee := price * 0.018 // 1,8 % Fundus-Chain-Gebühr (Vorschau-Schätzung)
	contentHash := getStr("content_hash")
	if contentHash == "–" {
		contentHash = ""
	}

	data := &ContractData{
		EscrowID:       "VORSCHAU",
		ListingID:      listingID,
		CreatedAt:      now,
		FreezeDeadline: now.Add(14 * 24 * time.Hour),
		ReturnDeadline: now.Add(21 * 24 * time.Hour),
		BuyerWallet:    "0x… (wird beim Kauf eingetragen)",
		SellerWallet:   rec.OwnerID,
		Title:          getStr("title"),
		Description:    getStr("description"),
		Condition:      getStr("condition"),
		Category:       getStr("category"),
		ContentHash:    contentHash,
		PriceFND:       price,
		FeeFND:         fee,
		NetFND:         price - fee,
		Status:         "preview",
		ChainID:        fundusChainID,
		ChainName:      "Fundus-Chain",
		MarketAddr:     s.cfg.MarketAddress,
		NodeID:         s.nodeID(),
	}
	if data.Title == "–" {
		if lt, ok := d["listing_text"].(string); ok {
			data.Title = lt
		}
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, buildContractHTML(data))
}

func (s *Server) buildContractData(escrowIDStr string) (*ContractData, error) {
	id, ok := parseEscrowID(escrowIDStr)
	if !ok {
		return nil, fmt.Errorf("ungültige Escrow-ID")
	}
	if s.chain == nil {
		return nil, fmt.Errorf("Chain nicht aktiv")
	}
	esc, found := s.chain.GetEscrow(id)
	if !found {
		// Geschlossen+gepruned oder unbekannt: kein Live-Escrow mehr abrufbar.
		return nil, fmt.Errorf("Escrow nicht gefunden (unbekannt oder bereits abgeschlossen)")
	}

	// Block-Deadlines in geschätzte Zeitpunkte umrechnen (BlockInterval-Sekunden
	// pro Block ab jetzt). Konservative Schätzung für die Vertragsanzeige.
	height := s.chain.Height()
	blockToTime := func(target uint64) time.Time {
		now := time.Now().UTC()
		if target <= height {
			return now
		}
		return now.Add(time.Duration((target-height)*blockIntervalSeconds) * time.Second)
	}

	price := uToFNDFloat(esc.Amount)
	fee := uToFNDFloat(chain.FeeForValue(esc.Amount)) // echte 1,8 % der Fundus-Chain

	data := &ContractData{
		EscrowID:       escrowIDStr,
		ListingID:      "–",
		CreatedAt:      time.Now().UTC(), // Eröffnungszeit nicht im State; Anzeigezeit
		FreezeDeadline: blockToTime(esc.Deadline),
		BuyerWallet:    esc.Buyer.Hex(),
		SellerWallet:   esc.Seller.Hex(),
		Title:          "Artikel",
		Description:    "–",
		Condition:      "–",
		Category:       "–",
		ContentHash:    "0x" + hex.EncodeToString(esc.ContentHash[:]),
		PriceFND:       price,
		FeeFND:         fee,
		NetFND:         price - fee,
		Status:         escrowStateString(esc.State),
		ChainID:        fundusChainID,
		ChainName:      "Fundus-Chain",
		MarketAddr:     s.cfg.MarketAddress,
		NodeID:         s.nodeID(),
	}
	// Rücksende-Frist + Tracking-Hash, falls im Storno-/Rücksende-Flow.
	if esc.State == chain.EscrowReturnSubmitted || esc.State == chain.EscrowCancelRequested {
		data.ReturnDeadline = blockToTime(esc.ReturnDeadline)
		if esc.TrackingHash != ([32]byte{}) {
			data.ReturnTrackingHash = "0x" + hex.EncodeToString(esc.TrackingHash[:])
		}
	}

	// Kaufvermerk (R570): Menge, Übergabeart und Versandkosten je Kauf.
	if s.store != nil {
	if rec, err := s.store.Get(storage.RecordPurchase, "purchase-"+strings.ToLower(trimHexPrefix(escrowIDStr))); err == nil && rec != nil {
		if d, _ := rec.Data["delivery"].(string); d == "shipping" {
			data.DeliveryTxt = "Versand"
		} else {
			data.DeliveryTxt = "Selbstabholung"
		}
		if sc, ok := toFloatOK(rec.Data["shipping_fnd"]); ok {
			data.ShippingFND = sc
		}
	}
	}
	// Ware aus dem verknüpften Listing nachladen: Der ContentHash verknüpft den
	// Escrow mit dem Angebot. Wir suchen das Listing mit passendem content_hash.
	if s.store != nil && esc.ContentHash != ([32]byte{}) {
		if title, desc, cond, cat, lid := s.lookupListingByContentHash(esc.ContentHash); lid != "" {
			data.ListingID = lid
			data.Title = title
			data.Description = desc
			data.Condition = cond
			data.Category = cat
		}
	}

	return data, nil
}

// lookupListingByContentHash sucht das Listing, dessen eingebetteter content_hash
// zum Escrow passt, und gibt Anzeigefelder + Listing-ID zurück. Nutzt den
// Sekundärindex (O(1)) statt eines linearen Scans über alle Listings.
func (s *Server) lookupListingByContentHash(want [32]byte) (title, desc, cond, cat, listingID string) {
	wantHex := hex.EncodeToString(want[:])
	rec, ok := s.store.FindRecordByField(storage.RecordListing, "content_hash", wantHex)
	if !ok || rec == nil {
		return "", "", "", "", ""
	}
	getStr := func(d map[string]any, k string) string {
		if v, ok := d[k].(string); ok {
			return v
		}
		return "–"
	}
	return getStr(rec.Data, "title"), getStr(rec.Data, "description"),
		getStr(rec.Data, "condition"), getStr(rec.Data, "category"), rec.ID
}

// nodeID gibt die Peer-ID des Nodes zurück.
func (s *Server) nodeID() string {
	if s.node != nil {
		return s.node.ID().String()
	}
	return "unbekannt"
}

// =============================================================================
//  HTML-Kaufvertrag
// =============================================================================

func buildContractHTML(d *ContractData) string {
	statusDE := map[string]string{
		"open":             "Angelegt – Zahlung ausstehend",
		"funded":           "Bezahlt – 14-Tage-Freeze aktiv",
		"released":         "Abgeschlossen – Zahlung freigegeben",
		"cancel_requested": "Storno beantragt",
		"return_submitted": "Rücksendebeleg eingereicht",
		"refunded":         "Erstattet",
		"disputed":         "Streitfall – Arbiter eingeschaltet",
	}[d.Status]
	if statusDE == "" {
		statusDE = d.Status
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="de">
<head>
<meta charset="utf-8">
<title>Kaufvertrag – Escrow %s</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

  body {
    font-family: "Helvetica Neue", Helvetica, Arial, sans-serif;
    font-size: 11pt;
    line-height: 1.5;
    color: #1a1a1a;
    background: #fff;
    max-width: 800px;
    margin: 0 auto;
    padding: 40px 32px;
  }

  /* Bildschirm: drucken-Button oben */
  .print-bar {
    background: #f0f4ff;
    border: 1px solid #c7d2fe;
    border-radius: 6px;
    padding: 12px 16px;
    margin-bottom: 24px;
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .print-bar button {
    background: #4f46e5;
    color: #fff;
    border: none;
    padding: 8px 20px;
    border-radius: 4px;
    font-size: 13px;
    cursor: pointer;
  }
  .print-bar .hint { font-size: 12px; color: #6366f1; }

  /* Briefkopf */
  .header {
    border-bottom: 2px solid #1a1a1a;
    padding-bottom: 16px;
    margin-bottom: 24px;
  }
  .header h1 { font-size: 20pt; letter-spacing: -0.5px; }
  .header .meta { font-size: 9pt; color: #555; margin-top: 4px; }

  /* Abschnitte */
  h2 {
    font-size: 12pt;
    margin: 24px 0 8px;
    padding-bottom: 4px;
    border-bottom: 1px solid #ddd;
    text-transform: uppercase;
    letter-spacing: 0.5px;
  }

  /* Tabellen */
  table {
    width: 100%%;
    border-collapse: collapse;
    margin: 8px 0;
    font-size: 10pt;
  }
  th {
    text-align: left;
    font-weight: 600;
    color: #444;
    padding: 4px 8px 4px 0;
    width: 40%%;
    vertical-align: top;
  }
  td { padding: 4px 0; }

  /* Preistabelle */
  .price-table { margin-top: 8px; }
  .price-table tr.total td { font-weight: 700; border-top: 1px solid #aaa; padding-top: 6px; }
  .price-table tr.fee   td { color: #666; font-size: 9.5pt; }

  /* Fristen-Box */
  .fristen-box {
    background: #fff8e1;
    border: 1px solid #f59e0b;
    border-radius: 4px;
    padding: 12px 16px;
    margin: 12px 0;
    font-size: 10pt;
  }
  .fristen-box .frist {
    display: flex;
    justify-content: space-between;
    padding: 3px 0;
    border-bottom: 1px solid #fde68a;
  }
  .fristen-box .frist:last-child { border-bottom: none; }
  .frist-label { font-weight: 600; }
  .frist-date  { font-family: monospace; }

  /* AGB */
  .agb {
    font-size: 9pt;
    color: #444;
    line-height: 1.6;
    margin-top: 8px;
  }
  .agb p { margin-bottom: 8px; }
  .agb strong { color: #1a1a1a; }

  /* Status-Badge */
  .status-badge {
    display: inline-block;
    background: #dcfce7;
    color: #166534;
    padding: 2px 10px;
    border-radius: 9999px;
    font-size: 9pt;
    font-weight: 600;
  }
  .status-badge.pending { background: #fef3c7; color: #92400e; }
  .status-badge.cancel  { background: #fee2e2; color: #991b1b; }

  /* Blockchain-Nachweis */
  .blockchain-box {
    background: #f8fafc;
    border: 1px solid #e2e8f0;
    border-radius: 4px;
    padding: 10px 14px;
    font-family: monospace;
    font-size: 8.5pt;
    color: #475569;
    word-break: break-all;
    margin: 8px 0;
  }

  /* Unterschriften */
  .signatures {
    margin-top: 32px;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 24px;
  }
  .sig-block {
    border-top: 1px solid #333;
    padding-top: 8px;
    font-size: 9pt;
    color: #555;
  }
  .sig-space { height: 40px; }

  /* Fußzeile */
  footer {
    margin-top: 40px;
    padding-top: 12px;
    border-top: 1px solid #ddd;
    font-size: 8pt;
    color: #888;
    text-align: center;
  }

  /* Druckansicht */
  @media print {
    .print-bar { display: none !important; }
    body { padding: 20px; max-width: none; }
    h2   { page-break-after: avoid; }
    .signatures { page-break-inside: avoid; }
    .fristen-box { page-break-inside: avoid; }
  }
</style>
</head>
<body>

<div class="print-bar">
  <button onclick="window.print()">🖨 Als PDF speichern</button>
  <span class="hint">Drucker → Als PDF speichern → DIN A4</span>
</div>

<!-- ================================================================
     BRIEFKOPF
     ================================================================ -->
<div class="header">
  <h1>Kaufvertrag</h1>
  <div class="meta">
    Fundus Dezentraler Marktplatz · %s ·
    Ausgestellt am %s
  </div>
</div>

<!-- ================================================================
     STATUS
     ================================================================ -->
<table>
  <tr>
    <th>Escrow-ID</th>
    <td><strong>%s</strong></td>
  </tr>
  <tr>
    <th>Status</th>
    <td><span class="status-badge pending">%s</span></td>
  </tr>
  <tr>
    <th>Vertragsschluss</th>
    <td>%s UTC</td>
  </tr>
</table>

<!-- ================================================================
     VERTRAGSPARTEIEN
     ================================================================ -->
<h2>Vertragsparteien</h2>
<table>
  <tr>
    <th>Verkäufer (Wallet)</th>
    <td class="mono">%s</td>
  </tr>
  <tr>
    <th>Käufer (Wallet)</th>
    <td class="mono">%s</td>
  </tr>
</table>

<!-- ================================================================
     KAUFGEGENSTAND
     ================================================================ -->
<h2>Kaufgegenstand</h2>
<table>
  <tr><th>Bezeichnung</th><td>%s</td></tr>
  <tr><th>Beschreibung</th><td>%s</td></tr>
  <tr><th>Zustand</th><td>%s</td></tr>
  <tr><th>Kategorie</th><td>%s</td></tr>
  <tr>
    <th>Inhalts-Hash (unveränderlich)</th>
    <td style="font-family:monospace;font-size:9pt">%s</td>
  </tr>
</table>

<!-- ================================================================
     KAUFPREIS
     ================================================================ -->
<h2>Kaufpreis</h2>
<table class="price-table">
  <tr><th>Kaufpreis</th><td>%.4f FND</td></tr>
  <tr><th>Übergabe</th><td>%s</td></tr>
  <tr class="fee"><th>Plattformgebühr (1,8%%)</th><td>- %.4f FND</td></tr>
  <tr class="fee"><th>Netzgebühr</th><td>- %.4f FND</td></tr>
  <tr class="total"><th>Auszahlung an Verkäufer</th><td>%.4f FND</td></tr>
</table>
<p style="font-size:9pt;color:#666;margin-top:6px">
  FND = Fundus Network Dollar · %s (Chain-ID %d)
</p>

<!-- ================================================================
     ZAHLUNGSBEDINGUNGEN UND FRISTEN
     ================================================================ -->
<h2>Zahlungsbedingungen</h2>

<div class="fristen-box">
  <div class="frist">
    <span class="frist-label">FND eingefroren bis</span>
    <span class="frist-date">%s UTC (14 Tage)</span>
  </div>
  <div class="frist">
    <span class="frist-label">Rücksendefrist (falls Storno)</span>
    <span class="frist-date">%s</span>
  </div>
  <div class="frist">
    <span class="frist-label">Verkäufer-Antwortfrist (nach Rücksendebeleg)</span>
    <span class="frist-date">14 Tage ab Einreichung</span>
  </div>
</div>

<!-- ================================================================
     WIDERRUFSRECHT UND RÜCKSENDEPFLICHT
     ================================================================ -->
<h2>Widerrufsrecht &amp; Rücksendung</h2>
<div class="agb">
  <p>
    <strong>§ 1 Escrow-Zahlungsschutz.</strong>
    Der Kaufpreis wird für einen Zeitraum von 14 (vierzehn) Tagen ab Zahlungseingang
    im Smart Contract gesperrt (Escrow). Der Käufer kann in diesem Zeitraum den Erhalt
    bestätigen oder Storno beantragen.
  </p>
  <p>
    <strong>§ 2 Bestätigung des Erhalts.</strong>
    Bestätigt der Käufer den ordnungsgemäßen Erhalt der Ware durch
    <em>confirmReceipt()</em>, wird der Betrag sofort an den Verkäufer freigegeben.
    Reagiert der Käufer innerhalb von 14 Tagen nicht, erfolgt die automatische
    Freigabe an den Verkäufer (<em>refundExpired()</em>).
  </p>
  <p>
    <strong>§ 3 Stornierung und Rückgabe.</strong>
    Der Käufer kann innerhalb der 14-Tage-Frist Storno beantragen, wenn die Ware
    nicht angekommen ist, defekt ist oder nicht der Beschreibung entspricht.
    Mit Einreichung des Storno-Antrags entsteht eine <strong>Rücksendepflicht</strong>:
    Auch Ware, die nach dem Storno-Antrag noch zugestellt wird, muss unverzüglich
    zurückgesendet werden. Der Käufer hinterlegt die Tracking-Nummer der Rücksendung
    als Hash on-chain (<em>submitReturnProof()</em>).
  </p>
  <p>
    <strong>§ 4 Bestätigung der Rücksendung.</strong>
    Bestätigt der Verkäufer die Rücksendung innerhalb von 7 Tagen
    (<em>confirmReturn()</em>), wird der vollständige Kaufpreis an den Käufer
    erstattet. Bestreitet der Verkäufer die Rücksendung, entscheidet der Arbiter.
    Reagiert der Verkäufer nicht innerhalb der Frist, erfolgt die automatische
    Erstattung an den Käufer.
  </p>
  <p>
    <strong>§ 5 Streitfall.</strong>
    Bei Streitfällen entscheidet der Fundus-Arbiter auf Basis der eingereichten
    Nachweise (Tracking-Hash, Fotos, Beschreibungen). Die Entscheidung des Arbiters
    ist für beide Parteien bindend und wird on-chain vollstreckt.
  </p>
  <p>
    <strong>§ 6 Blockchain-Nachweis.</strong>
    Alle Vertragshandlungen (Escrow, Bestätigungen, Storno, Rücksendebeleg) werden
    unveränderlich auf der Fundus-Chain protokolliert und sind öffentlich prüfbar.
  </p>
</div>

<!-- ================================================================
     BLOCKCHAIN-NACHWEIS
     ================================================================ -->
<h2>Blockchain-Nachweis</h2>
<div class="blockchain-box">
  Smart Contract: %s<br>
  Chain: %s (Chain-ID %d)<br>
  Escrow-ID: %s<br>
  Ausgestellt durch Node: %s
</div>

<!-- ================================================================
     UNTERSCHRIFTEN
     ================================================================ -->
<div class="signatures">
  <div class="sig-block">
    <div class="sig-space"></div>
    Verkäufer<br>
    <span style="font-family:monospace;font-size:8.5pt">%s</span>
  </div>
  <div class="sig-block">
    <div class="sig-space"></div>
    Käufer<br>
    <span style="font-family:monospace;font-size:8.5pt">%s</span>
  </div>
</div>
<p style="font-size:8pt;color:#888;margin-top:8px">
  Die Unterschriften werden durch die digitale Signatur der Blockchain-Transaktionen
  (Private Key der Wallet-Adresse) rechtsverbindlich ersetzt.
</p>

<footer>
  Fundus Marktplatz · Dezentral · Keine zentrale Instanz ·
  Vertrag-ID %s · Erstellt %s UTC
</footer>

</body>
</html>`,
		d.EscrowID,
		// Header
		d.ChainName,
		d.CreatedAt.Format("02.01.2006 15:04"),
		// Status
		d.EscrowID,
		statusDE,
		d.CreatedAt.Format("02.01.2006 15:04:05"),
		// Parteien
		d.SellerWallet,
		d.BuyerWallet,
		// Kaufgegenstand
		d.Title, d.Description, d.Condition, d.Category, d.ContentHash,
		// Preis (kommt im Template VOR der FND-Fußzeile)
		d.PriceFND, contractDeliveryText(d), d.FeeFND, d.GridFeeFND, d.NetFND,
		// FND-Fußzeile (Chain-Name + Chain-ID)
		d.ChainName, d.ChainID,
		// Fristen
		d.FreezeDeadline.Format("02.01.2006 15:04"),
		returnDeadlineText(d.ReturnDeadline),
		// Blockchain
		d.MarketAddr, d.ChainName, d.ChainID, d.EscrowID, d.NodeID,
		// Unterschriften
		d.SellerWallet, d.BuyerWallet,
		// Footer
		d.EscrowID, d.CreatedAt.Format("02.01.2006 15:04:05"),
	)
}

// returnDeadlineText: Die Rücksendefrist entsteht erst MIT einem Storno
// (14 Tage ab dann). Ohne Storno gab es hier bisher das Null-Datum
// "01.01.0001" — stattdessen den Hinweis zeigen.
func returnDeadlineText(t time.Time) string {
	if t.IsZero() || t.Year() < 2000 {
		return "– (14 Tage ab Storno)"
	}
	return t.Format("02.01.2006 15:04") + " UTC (14 Tage nach Storno)"
}

// ── Bestand eines Angebots (R562) ───────────────────────────────────────────
// quantity = angebotene Stückzahl (fehlt sie, gilt 1 – Altbestand), sold_count
// = bereits verkaufte. sold = true, sobald nichts mehr übrig ist.

var listingStockMu sync.Mutex // serialisiert Lesen+Schreiben des Bestands

func listingQuantity(d map[string]any) int {
	if d == nil {
		return 1
	}
	// 0 ist gültig: Angebot vorübergehend aus dem Verkauf genommen.
	switch v := d["quantity"].(type) {
	case float64:
		if v >= 0 {
			return int(v)
		}
	case int:
		if v >= 0 {
			return v
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			return n
		}
	}
	return 1 // fehlt die Angabe: Einzelstück (Altbestand)
}

// soldFromPurchases zählt die verkaufte Menge aus den Kaufvermerken im Netz.
// Maßgeblich, weil diese Vermerke dem jeweiligen Käufer-Node gehören und sich
// deshalb verteilen – anders als eine Änderung am Angebot des Verkäufers.
func (s *Server) soldFromPurchases(listingID string) (int, bool) {
	if s.store == nil || listingID == "" {
		return 0, false
	}
	recs, err := s.store.List(storage.RecordPurchase)
	if err != nil {
		return 0, false
	}
	total, found := 0, false
	for _, r := range recs {
		if r == nil || r.DeletedAt != nil || r.Data == nil {
			continue
		}
		if lid, _ := r.Data["listing_id"].(string); lid != listingID {
			continue
		}
		found = true
		if q, ok := toFloatOK(r.Data["quantity"]); ok && q >= 1 {
			total += int(q)
		} else {
			total++
		}
	}
	return total, found
}

// listingStock liefert angebotene und verkaufte Menge eines Angebots.
func (s *Server) listingStock(id string, d map[string]any) (qty, sold int) {
	// quantity ist die TATSÄCHLICH VERFÜGBARE Menge (vom Verkäufer gepflegt).
	// Käufe, die der Verkäufer-Node noch nicht abgezogen hat, ziehen wir für
	// die Anzeige lokal ab – sichtbar am Kaufvermerk, dessen Kennung noch
	// nicht in sold_txs steht.
	qty = listingQuantity(d)
	sold = s.unappliedPurchases(id, d)
	return qty, sold
}

// appliedTxs liefert die vom Verkäufer bereits verrechneten Kauf-Kennungen.
func appliedTxs(d map[string]any) map[string]bool {
	out := map[string]bool{}
	if d == nil {
		return out
	}
	if arr, ok := d["sold_txs"].([]any); ok {
		for _, v := range arr {
			if sv, ok := v.(string); ok {
				out[strings.ToLower(sv)] = true
			}
		}
	}
	return out
}

// unappliedPurchases zählt lokale Kaufvermerke, die noch nicht verrechnet sind.
func (s *Server) unappliedPurchases(listingID string, d map[string]any) int {
	if s.store == nil || listingID == "" {
		return 0
	}
	recs, err := s.store.List(storage.RecordPurchase)
	if err != nil {
		return 0
	}
	done := appliedTxs(d)
	n := 0
	for _, r := range recs {
		if r == nil || r.DeletedAt != nil || r.Data == nil {
			continue
		}
		if lid, _ := r.Data["listing_id"].(string); lid != listingID {
			continue
		}
		if done[strings.ToLower(strings.TrimPrefix(r.ID, "purchase-"))] {
			continue // vom Verkäufer bereits abgezogen
		}
		if q, ok := toFloatOK(r.Data["quantity"]); ok && q >= 1 {
			n += int(q)
		} else {
			n++
		}
	}
	return n
}

// applySale zieht einen Verkauf vom eigenen Angebot ab. Nur der Verkäufer-Node
// darf das – seine Fassung gewinnt beim Abgleich und verteilt den Bestand.
// Jede Kauf-Kennung wird nur einmal verrechnet.
func (s *Server) applySale(listingID, tx string, n int) bool {
	if s.store == nil || listingID == "" || n < 1 {
		return false
	}
	listingStockMu.Lock()
	defer listingStockMu.Unlock()
	rec, err := s.store.Get(storage.RecordListing, listingID)
	if err != nil || rec == nil || rec.Data == nil || rec.OwnerID != s.nodeID() {
		return false
	}
	tx = strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(tx, "purchase-"), "0x"))
	if tx == "" || appliedTxs(rec.Data)[tx] {
		return false
	}
	left := listingQuantity(rec.Data) - n
	if left < 0 {
		left = 0
	}
	rec.Data["quantity"] = left
	rec.Data["sold"] = left <= 0
	txs, _ := rec.Data["sold_txs"].([]any)
	rec.Data["sold_txs"] = append(txs, tx)
	rec.UpdatedAt = time.Now()
	if s.store.Put(rec) != nil {
		return false
	}
	if s.log != nil {
		s.log.Info("Bestand verringert", zap.String("listing", listingID), zap.Int("verkauft", n), zap.Int("verfuegbar", left))
	}
	return true
}

// reconcileOwnListings holt Käufe nach, deren Meldung den Node nicht erreicht
// hat (z.B. weil er offline war). Nur für eigene Angebote.
func (s *Server) reconcileOwnListings() {
	for {
		time.Sleep(30 * time.Second)
		if s.store == nil {
			continue
		}
		recs, err := s.store.List(storage.RecordListing)
		if err != nil {
			continue
		}
		me := s.nodeID()
		for _, rec := range recs {
			if rec == nil || rec.DeletedAt != nil || rec.Data == nil || rec.OwnerID != me {
				continue
			}
			purchases, perr := s.store.List(storage.RecordPurchase)
			if perr != nil {
				break
			}
			done := appliedTxs(rec.Data)
			for _, pr := range purchases {
				if pr == nil || pr.DeletedAt != nil || pr.Data == nil {
					continue
				}
				if lid, _ := pr.Data["listing_id"].(string); lid != rec.ID {
					continue
				}
				tx := strings.ToLower(strings.TrimPrefix(pr.ID, "purchase-"))
				if done[tx] {
					continue
				}
				n := 1
				if q, ok := toFloatOK(pr.Data["quantity"]); ok && q >= 1 {
					n = int(q)
				}
				s.applySale(rec.ID, tx, n)
				done[tx] = true
			}
		}
	}
}

func listingSoldCount(d map[string]any) int {
	if d == nil {
		return 0
	}
	switch v := d["sold_count"].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	// Altbestand ohne Zähler: ein gesetztes "sold" zählt als 1 verkauft.
	if sold, _ := d["sold"].(bool); sold {
		return 1
	}
	return 0
}

// reserveListingStock bucht ein Stück ab. ok=false → nichts mehr verfügbar.
// pendingStock: laufende Reservierungen je Angebot, nur im Arbeitsspeicher.
// Zwischen Abbuchung und fertigem Kaufvermerk liegen ein paar Sekunden – so
// lange zählt diese Zahl mit, damit nichts doppelt verkauft wird.
var pendingStock = map[string]int{}

func (s *Server) reserveListingStock(id string, n int) (ok bool, left int, err error) {
	listingStockMu.Lock()
	defer listingStockMu.Unlock()
	rec, gerr := s.store.Get(storage.RecordListing, id)
	if gerr != nil || rec == nil || rec.Data == nil {
		return false, 0, fmt.Errorf("Angebot nicht gefunden")
	}
	if n < 1 {
		n = 1
	}
	qty, sold := s.listingStock(id, rec.Data)
	sold += pendingStock[id]
	if sold+n > qty {
		return false, qty - sold, nil // left = noch verfügbar
	}
	// Das Angebot selbst wird NICHT verändert: Es gehört dem Verkäufer-Node.
	// Jede Änderung hier erzeugte eine neuere Fassung beim Käufer, die beim
	// Abgleich die Angaben des Verkäufers (u.a. die Stückzahl) überschrieb.
	// Gezählt wird stattdessen über die Kaufvermerke (soldFromPurchases).
	pendingStock[id] += n
	return true, qty - sold - n, nil
}

// settleListingStock: Der Kaufvermerk steht, die Reservierung wird frei.
func (s *Server) settleListingStock(id string, n int) {
	listingStockMu.Lock()
	defer listingStockMu.Unlock()
	if pendingStock[id] -= n; pendingStock[id] <= 0 {
		delete(pendingStock, id)
	}
}

// releaseListingStock gibt ein reserviertes Stück zurück (Transaktion scheiterte).
// releaseListingStock gibt eine Reservierung zurück (Transaktion scheiterte).
func (s *Server) releaseListingStock(id string, n int) {
	s.settleListingStock(id, n)
}

// contractDeliveryText beschreibt die Übergabe inkl. enthaltener Versandkosten.
func contractDeliveryText(d *ContractData) string {
	txt := d.DeliveryTxt
	if txt == "" {
		txt = "Selbstabholung"
	}
	if d.ShippingFND > 0 {
		return fmt.Sprintf("%s (darin %.4f FND Versandkosten)", txt, d.ShippingFND)
	}
	return txt
}

// anyToFNDString nimmt amount_fnd als Text oder Zahl entgegen. Die Oberfläche
// hat den Betrag zeitweise als Zahl geschickt (R569) – das scheiterte vorher
// an der Typprüfung beim Einlesen.
func anyToFNDString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return ""
}

// escrowList liefert die offenen Hinterlegungen des angemeldeten Nutzers –
// mit Rolle (Käufer/Verkäufer), damit die Wallet die passenden Aktionen zeigt.
// Bis R571 fragte die Wallet diesen Endpunkt an, ohne dass es ihn gab.
func (s *Server) escrowList(c *gin.Context) {
	if s.chain == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Chain nicht verfügbar"})
		return
	}
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	key, err := sess.identity.ChainPrivateKey()
	if err != nil || key == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Wallet dieser Anmeldung nicht verfügbar."})
		return
	}
	me := chain.PubkeyToAddress(&key.PublicKey)
	statusName := map[chain.EscrowState]string{
		chain.EscrowOpen: "funded", chain.EscrowDisputed: "disputed", chain.EscrowCancelRequested: "cancel_requested",
		chain.EscrowReturnSubmitted: "return_submitted", chain.EscrowClosed: "closed",
	}
	out := make([]gin.H, 0)
	for id, e := range s.chain.EscrowsOf(me) {
		role := "seller"
		if e.Buyer == me {
			role = "buyer"
		}
		st, ok := statusName[e.State]
		if !ok {
			st = "open"
		}
		out = append(out, gin.H{
			"escrow_id":  "0x" + hex.EncodeToString(id[:]),
			"amount_fnd": uToFNDFloat(e.Amount),
			"status":     st,
			"role":       role,
			"deadline":   e.Deadline,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["escrow_id"]) < fmt.Sprint(out[j]["escrow_id"])
	})
	c.JSON(http.StatusOK, gin.H{"escrows": out})
}

// contractPendingHTML: Wartehinweis, bis der Kauf in einem Block steht.
const contractPendingHTML = `<!DOCTYPE html><html lang="de"><head><meta charset="utf-8">
<title>Kaufvertrag wird erstellt</title><meta http-equiv="refresh" content="4">
<style>body{font-family:system-ui,sans-serif;background:#0f1117;color:#f2f3f8;display:flex;
align-items:center;justify-content:center;height:100vh;margin:0;text-align:center}
.box{max-width:420px;padding:28px}h1{font-size:20px;margin:0 0 10px}
p{color:#b0b4cc;line-height:1.5;margin:0}
.dot{display:inline-block;width:10px;height:10px;border-radius:50%;background:#00e676;
animation:p 1.2s ease-in-out infinite;margin-bottom:14px}
@keyframes p{0%,100%{opacity:.3}50%{opacity:1}}</style></head>
<body><div class="box"><div class="dot"></div>
<h1>Der Kaufvertrag wird gerade erstellt</h1>
<p>Dein Kauf wird in den nächsten Sekunden in einem Block der Fundus-Chain festgeschrieben.
Diese Seite lädt sich automatisch neu.</p></div></body></html>`

// ── Verkaufsmeldung an den Verkäufer-Node (R578) ────────────────────────────

const listingSoldProtocol = "/fundus/listing-sold/1.0.0"

type listingSoldMsg struct {
	ListingID string `json:"listing_id"`
	Tx        string `json:"tx"`
	Quantity  int    `json:"quantity"`
}

// notifySaleToOwner meldet den Kauf direkt an den Node, dem das Angebot gehört.
func (s *Server) notifySaleToOwner(listingID, tx string, qty int) {
	if s.node == nil || s.store == nil {
		return
	}
	rec, err := s.store.Get(storage.RecordListing, listingID)
	if err != nil || rec == nil || rec.OwnerID == "" || rec.OwnerID == s.nodeID() {
		return // eigenes Angebot: wird lokal verrechnet
	}
	body, err := json.Marshal(listingSoldMsg{ListingID: listingID, Tx: tx, Quantity: qty})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.node.SendAndReceive(ctx, rec.OwnerID, listingSoldProtocol, body); err != nil && s.log != nil {
		// Nicht schlimm: Der Kaufvermerk verteilt sich ohnehin, der Verkäufer
		// holt den Abzug beim nächsten Durchlauf nach.
		s.log.Debug("Verkaufsmeldung nicht zugestellt", zap.Error(err))
	}
}

// registerListingSoldProtocol beantwortet Verkaufsmeldungen anderer Nodes.
func (s *Server) registerListingSoldProtocol() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(listingSoldProtocol, func(peerID string, data []byte) []byte {
		var m listingSoldMsg
		if json.Unmarshal(data, &m) != nil || m.ListingID == "" || m.Tx == "" {
			return []byte(`{"ok":false}`)
		}
		n := m.Quantity
		if n < 1 {
			n = 1
		}
		// applySale prüft selbst: nur eigenes Angebot, jede Kennung nur einmal.
		s.applySale(m.ListingID, m.Tx, n)
		return []byte(`{"ok":true}`)
	})
}
