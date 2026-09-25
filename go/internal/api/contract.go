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
		e.GET("/:id",                 s.escrowGet)
	}
}

// Escrow erstellen
// escrowDefaultDeadlineBlocks: Standard-Frist einer Escrow-Eröffnung, wenn der
// Client keine explizite Höhe angibt. ~14 Tage bei ~1 Block/Minute (Richtwert;
// die echte Blockrate hängt vom Produzenten ab).
const escrowDefaultDeadlineBlocks uint64 = 14 * 24 * 60

// blockIntervalSeconds ist die angenommene Blockzeit für die Umrechnung von
// Block-Deadlines in Anzeige-Zeitpunkte (Vertragsvorschau). Richtwert; die echte
// Rate hängt vom Produzenten ab (Phase 2: Single-Producer, on-demand).
const blockIntervalSeconds = 60

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
		AmountFND   string   `json:"amount_fnd"`
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
	amount, ok := parseFNDtoU(req.AmountFND)
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

	txHash, from, nonce, err := s.submitEscrowTx(req.Words, chain.TxEscrowOpen, fee, payload)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Listing als verkauft markieren (ausgegraut, nicht mehr kaufbar). Der
	// Kaufbeweis liegt ohnehin unveränderlich als Escrow-Tx auf der Chain — das
	// Listing ist nur der Marktplatz-Eintrag und darf weiter gelöscht werden.
	if req.ListingID != "" {
		if rec, gerr := s.store.Get(storage.RecordListing, req.ListingID); gerr == nil && rec != nil {
			rec.Data["sold"] = true
			rec.Data["sold_escrow"] = txHash
			_ = s.store.Put(rec)
		}
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
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(
		`inline; filename="kaufvertrag-%s.pdf"`, data.EscrowID,
	))

	html := buildContractHTML(data)
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
		return now.Add(time.Duration(target-height) * blockIntervalSeconds * time.Second)
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
    <span class="frist-date">%s UTC (14 Tage nach Storno)</span>
  </div>
  <div class="frist">
    <span class="frist-label">Verkäufer-Antwortfrist (nach Rücksendebeleg)</span>
    <span class="frist-date">7 Tage ab Einreichung</span>
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
		d.PriceFND, d.FeeFND, d.GridFeeFND, d.NetFND,
		// FND-Fußzeile (Chain-Name + Chain-ID)
		d.ChainName, d.ChainID,
		// Fristen
		d.FreezeDeadline.Format("02.01.2006 15:04"),
		d.ReturnDeadline.Format("02.01.2006 15:04"),
		// Blockchain
		d.MarketAddr, d.ChainName, d.ChainID, d.EscrowID, d.NodeID,
		// Unterschriften
		d.SellerWallet, d.BuyerWallet,
		// Footer
		d.EscrowID, d.CreatedAt.Format("02.01.2006 15:04:05"),
	)
}
