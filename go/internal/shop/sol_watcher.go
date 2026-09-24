package shop

import (
	cryptoRand "crypto/rand"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// =============================================================================
//  SolWatcher – überwacht eingehende SOL-Zahlungen
// =============================================================================

// WatcherConfig konfiguriert den SOL-Watcher.
type WatcherConfig struct {
	// Solana RPC-Endpunkt
	// Mainnet: https://api.mainnet-beta.solana.com
	// Devnet:  https://api.devnet.solana.com
	// Eigener: https://solana-rpc.net (günstiger)
	RPCURL string

	// Fundus-Empfangs-Adresse auf Solana (base58)
	ReceiveAddress string

	// Wie oft nach neuen Transaktionen gesucht wird
	PollInterval time.Duration

	// Wie lange ein Kauf-Auftrag auf Zahlung wartet
	PaymentTimeout time.Duration

	// HTTP-Timeout für Solana RPC-Calls
	HTTPTimeout time.Duration
}

// DefaultWatcherConfig liefert sinnvolle Standardwerte.
var DefaultWatcherConfig = WatcherConfig{
	RPCURL:         "https://api.mainnet-beta.solana.com",
	PollInterval:   3 * time.Second,  // Solana finalisiert in ~400ms, 3s ist sicher
	PaymentTimeout: 10 * time.Minute,
	HTTPTimeout:    5 * time.Second,
}

// =============================================================================
//  PendingOrder – offener Kauf-Auftrag
// =============================================================================

// OrderStatus beschreibt den Zustand eines Kaufauftrags.
type OrderStatus string

const (
	OrderPending   OrderStatus = "pending"   // warte auf Zahlung
	OrderReceived  OrderStatus = "received"  // Zahlung eingegangen
	OrderMinting   OrderStatus = "minting"   // FND wird geminted
	OrderComplete  OrderStatus = "complete"  // fertig
	OrderFailed    OrderStatus = "failed"    // Fehler
	OrderExpired   OrderStatus = "expired"   // Timeout
)

// PendingOrder beschreibt einen offenen Kaufauftrag.
type PendingOrder struct {
	ID             string      `json:"id"`              // zufällige UUID
	FNDAmount      float64     `json:"fnd_amount"`      // zu mintende FND
	SOLExpected    float64     `json:"sol_expected"`    // erwartete SOL (mit Puffer)
	SOLReceived    float64     `json:"sol_received"`    // tatsächlich eingegangen
	RateEUR        float64     `json:"rate_eur"`        // Kurs bei Auftragsstellung
	RecipientAddr  string      `json:"recipient_addr"`  // Gnosis-Chain-Adresse für FND
	Status         OrderStatus `json:"status"`
	CreatedAt      time.Time   `json:"created_at"`
	ExpiresAt      time.Time   `json:"expires_at"`
	TxSignature    string      `json:"tx_signature,omitempty"` // Solana TX-Signatur
	MintTxHash     string      `json:"mint_tx_hash,omitempty"` // Gnosis TX-Hash
	ErrorMsg       string      `json:"error,omitempty"`
}

// =============================================================================
//  SolWatcher
// =============================================================================

// SolWatcher überwacht eine Solana-Adresse auf eingehende Zahlungen
// und löst FND-Minting aus wenn ein passender Betrag eingeht.
type SolWatcher struct {
	cfg     WatcherConfig
	feed    *PriceFeed
	minter  Minter
	client  *http.Client
	log     *zap.Logger

	mu      sync.RWMutex
	orders  map[string]*PendingOrder // orderID → Order
	// Bereits verarbeitete TX-Signaturen (Deduplizierung)
	seenTx  map[string]bool
}

// Minter ist das Interface zum nativen FND-Minting (Fundus-Chain).
// Minter schreibt dem Empfänger FND gut, nachdem eine SOL-Zahlung bestätigt
// wurde. solTxSig ist die Solana-Tx-Signatur der Einzahlung — sie dient als
// eindeutiger Anti-Replay-Schlüssel, damit dieselbe Zahlung nicht doppelt FND
// mintet. Der native Minter bettet sie in die On-Chain-TxSolCredit ein.
type Minter interface {
	MintFND(ctx context.Context, toAddress string, fndAmount float64, solTxSig string) (txHash string, err error)
}

// NewSolWatcher erstellt einen SolWatcher.
func NewSolWatcher(cfg WatcherConfig, feed *PriceFeed, minter Minter, log *zap.Logger) *SolWatcher {
	return &SolWatcher{
		cfg:    cfg,
		feed:   feed,
		minter: minter,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
		log:    log,
		orders: make(map[string]*PendingOrder),
		seenTx: make(map[string]bool),
	}
}

// CreateOrder erstellt einen neuen Kaufauftrag.
// Gibt den Auftrag zurück – der Nutzer zahlt an cfg.ReceiveAddress.
func (w *SolWatcher) CreateOrder(ctx context.Context, fndAmount float64, recipientAddr string) (*PendingOrder, error) {
	if fndAmount <= 0 {
		return nil, fmt.Errorf("fnd_amount must be > 0")
	}
	if recipientAddr == "" {
		return nil, fmt.Errorf("recipient_addr required")
	}

	// SOL-Betrag berechnen
	solAmt, err := w.feed.SOLForEUR(ctx, fndAmount)
	if err != nil {
		return nil, fmt.Errorf("price feed: %w", err)
	}

	order := &PendingOrder{
		ID:            generateOrderID(),
		FNDAmount:     fndAmount,
		SOLExpected:   solAmt.SOL,
		RateEUR:       solAmt.Rate,
		RecipientAddr: recipientAddr,
		Status:        OrderPending,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(w.cfg.PaymentTimeout),
	}

	w.mu.Lock()
	w.orders[order.ID] = order
	w.mu.Unlock()

	w.log.Info("Order created",
		zap.String("orderID", order.ID),
		zap.Float64("fnd", fndAmount),
		zap.Float64("sol_expected", solAmt.SOL),
		zap.Float64("rate", solAmt.Rate),
	)

	return order, nil
}

// GetOrder gibt einen Auftrag zurück.
func (w *SolWatcher) GetOrder(id string) (*PendingOrder, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	o, ok := w.orders[id]
	return o, ok
}

// ReceiveAddress gibt die Solana-Empfangs-Adresse zurück.
func (w *SolWatcher) ReceiveAddress() string { return w.cfg.ReceiveAddress }

// Run startet die Polling-Schleife. Blockiert bis ctx abgebrochen wird.
func (w *SolWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	w.log.Info("SOL watcher started",
		zap.String("address", w.cfg.ReceiveAddress),
		zap.Duration("interval", w.cfg.PollInterval),
	)

	for {
		select {
		case <-ctx.Done():
			w.log.Info("SOL watcher stopped")
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// =============================================================================
//  Polling-Logik
// =============================================================================

func (w *SolWatcher) poll(ctx context.Context) {
	// Abgelaufene Orders bereinigen
	w.expireOrders()

	// IMMER die Empfangsadresse abfragen — Direktzahlungen (FND-Adresse im Memo)
	// kommen OHNE vorher angelegten Auftrag herein. Früher wurde nur bei offenen
	// Orders gepollt; dann hätte der Watcher jede memo-basierte Direktzahlung
	// verpasst. Der seenTx-Cache verhindert doppelte Verarbeitung.

	// Neueste Transaktionen der Empfangs-Adresse abrufen
	sigs, err := w.getRecentSignatures(ctx)
	if err != nil {
		w.log.Warn("solana getSignatures failed", zap.Error(err))
		return
	}

	for _, sig := range sigs {
		w.mu.RLock()
		alreadySeen := w.seenTx[sig]
		w.mu.RUnlock()
		if alreadySeen {
			continue
		}

		// Transaktion laden
		tx, err := w.getTransaction(ctx, sig)
		if err != nil {
			w.log.Warn("solana getTransaction failed",
				zap.String("sig", sig[:16]+"…"),
				zap.Error(err))
			continue
		}
		if tx == nil {
			continue
		}

		// SOL-Betrag ermitteln der an unsere Adresse geflossen ist
		receivedLamports := w.extractReceivedLamports(tx)
		if receivedLamports == 0 {
			w.mu.Lock()
			w.seenTx[sig] = true
			w.mu.Unlock()
			continue
		}

		receivedSOL := float64(receivedLamports) / 1e9
		memo := extractMemo(tx) // Order-ID aus dem Memo (leer, wenn keins)

		w.log.Info("Incoming SOL payment detected",
			zap.String("sig", sig[:16]+"…"),
			zap.Float64("sol", receivedSOL),
			zap.String("memo", memo),
		)

		// Passendem Order zuordnen — primär über die Order-ID im Memo.
		if w.matchOrder(ctx, sig, receivedSOL, memo) {
			w.mu.Lock()
			w.seenTx[sig] = true
			w.mu.Unlock()
		}
		// Bei false (z.B. Kurs gerade nicht verfügbar) bleibt die Tx ungesehen und
		// wird beim nächsten Poll erneut versucht.
	}
}

// matchOrder ordnet eine eingegangene Zahlung EINDEUTIG einem Auftrag zu —
// primär über die Order-ID im Solana-Memo, nicht über den Betrag. Das verhindert
// Fehlzuordnungen, wenn zwei Käufer zeitnah denselben Betrag einzahlen: ohne
// eindeutige Referenz würde die Betrags-Heuristik dem falschen Käufer gutschreiben
// oder einen zahlenden Käufer leer ausgehen lassen.
func (w *SolWatcher) matchOrder(ctx context.Context, txSig string, receivedSOL float64, memo string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 1. Direktzahlung: Memo ist eine FND-Adresse (0x + 40 Hex). Dann rechnen wir
	// den empfangenen SOL-Betrag zum AKTUELLEN Kurs in FND um und schreiben direkt
	// gut — ohne vorher angelegten Auftrag. Der Kurs gilt zum Eingangszeitpunkt.
	if looksLikeFNDAddress(memo) {
		w.mu.Unlock() // Kurs-Abruf nicht unter dem Lock
		rate, _, err := w.feed.SOLEUR(ctx)
		w.mu.Lock()
		if err != nil || rate <= 0 {
			w.log.Error("Direktzahlung: Kurs nicht verfügbar — wird beim nächsten Poll erneut versucht",
				zap.String("sig", safeSig(txSig)), zap.Error(err))
			return false // NICHT als gesehen markieren → nächster Poll versucht erneut
		}
		// 1 FND = 1 EUR. Empfangene SOL × (EUR pro SOL) = EUR = FND.
		fndAmount := receivedSOL * rate
		if fndAmount <= 0 {
			w.log.Warn("Direktzahlung: Betrag ergibt 0 FND", zap.Float64("sol", receivedSOL))
			return true
		}
		w.log.Info("Direktzahlung erkannt (FND-Adresse im Memo)",
			zap.String("recipient", memo),
			zap.Float64("sol", receivedSOL),
			zap.Float64("fnd", fndAmount),
			zap.Float64("rate", rate))
		// Gutschrift außerhalb des Locks (MintFND geht in den Mempool). Die Chain
		// verhindert per SolTxSig-Anti-Replay eine Doppel-Gutschrift.
		recipient := memo
		sig := txSig
		go func() {
			if _, err := w.minter.MintFND(context.Background(), recipient, fndAmount, sig); err != nil {
				w.log.Error("Direktzahlung: FND-Gutschrift fehlgeschlagen",
					zap.String("recipient", recipient), zap.Error(err))
			}
		}()
		return true
	}

	// 2. Eindeutige Zuordnung über die Order-ID im Memo (klassischer Auftrags-Weg).
	if memo != "" {
		if order, ok := w.orders[memo]; ok {
			if order.Status != OrderPending {
				return true // schon verarbeitet (z.B. Doppel-Zahlung)
			}
			if time.Now().After(order.ExpiresAt) {
				order.Status = OrderExpired
				w.log.Warn("Zahlung für abgelaufenen Auftrag", zap.String("orderID", memo))
				return true
			}
			// Betrag muss trotzdem ausreichen (Schutz gegen Unterzahlung).
			if receivedSOL < order.SOLExpected {
				w.log.Warn("Zahlung deckt Auftrag nicht",
					zap.String("orderID", order.ID),
					zap.Float64("sol_received", receivedSOL),
					zap.Float64("sol_expected", order.SOLExpected))
				return true
			}
			order.Status = OrderReceived
			order.SOLReceived = receivedSOL
			order.TxSignature = txSig
			w.log.Info("Order matched (per Memo)",
				zap.String("orderID", order.ID),
				zap.Float64("sol_received", receivedSOL))
			orderCopy := *order
			go w.executeMint(context.Background(), &orderCopy)
			return true
		}
		// Memo gesetzt, aber keine passende Order — NICHT auf Betrag zurückfallen,
		// sonst wäre die Eindeutigkeit wieder ausgehebelt.
		w.log.Warn("Zahlung mit unbekannter Order-ID im Memo",
			zap.String("memo", memo), zap.Float64("sol", receivedSOL))
		return true
	}

	// 2. Kein Memo: Das ist der unsichere Altfall. Wir schreiben NICHT automatisch
	// gut, sondern protokollieren die Zahlung zur manuellen Klärung — eine
	// Betrags-Heuristik würde bei gleichen Beträgen falsch zuordnen.
	w.log.Warn("Zahlung OHNE Order-ID im Memo — keine automatische Gutschrift, manuell prüfen",
		zap.String("sig", safeSig(txSig)),
		zap.Float64("sol", receivedSOL))
	return true
}

// safeSig kürzt eine Signatur für Logs, ohne bei kurzen Strings zu paniken.
func safeSig(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}

// looksLikeFNDAddress prüft, ob ein Memo eine Fundus-Adresse ist: "0x" gefolgt
// von genau 40 Hex-Zeichen. Nur dann wird die Direktzahlung ausgelöst.
func looksLikeFNDAddress(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 42 || s[0] != '0' || (s[1] != 'x' && s[1] != 'X') {
		return false
	}
	for _, c := range s[2:] {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// extractMemo liest die Order-ID aus dem Solana-Memo einer Transaktion. Das
// Memo-Programm schreibt eine Log-Zeile wie:
//   Program log: Memo (len 36): "a1b2c3d4-…"
// Wir ziehen den Text zwischen den ersten und letzten Anführungszeichen. Leer,
// wenn kein Memo vorhanden ist.
func extractMemo(tx *solanaTransaction) string {
	if tx == nil || tx.Meta == nil {
		return ""
	}
	for _, line := range tx.Meta.LogMessages {
		if !strings.Contains(line, "Memo") {
			continue
		}
		first := strings.Index(line, "\"")
		last := strings.LastIndex(line, "\"")
		if first >= 0 && last > first {
			return strings.TrimSpace(line[first+1 : last])
		}
	}
	return ""
}

// executeMint mintet FND nach erfolgreicher SOL-Zahlung.
func (w *SolWatcher) executeMint(ctx context.Context, order *PendingOrder) {
	w.setOrderStatus(order.ID, OrderMinting, "")

	txHash, err := w.minter.MintFND(ctx, order.RecipientAddr, order.FNDAmount, order.TxSignature)
	if err != nil {
		w.log.Error("FND mint failed",
			zap.String("orderID", order.ID),
			zap.Error(err),
		)
		w.setOrderStatus(order.ID, OrderFailed, err.Error())
		return
	}

	w.mu.Lock()
	if o, ok := w.orders[order.ID]; ok {
		o.Status     = OrderComplete
		o.MintTxHash = txHash
	}
	w.mu.Unlock()

	w.log.Info("FND minted successfully",
		zap.String("orderID", order.ID),
		zap.Float64("fnd", order.FNDAmount),
		zap.String("recipient", order.RecipientAddr),
		zap.String("txHash", txHash),
	)
}

// =============================================================================
//  Solana JSON-RPC
// =============================================================================

type rpcRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (w *SolWatcher) rpcCall(ctx context.Context, method string, params []interface{}, dst interface{}) error {
	body, _ := json.Marshal(rpcRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.RPCURL,
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("rpc decode: %w", err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return json.Unmarshal(rpcResp.Result, dst)
}

// getRecentSignatures gibt die neuesten TX-Signaturen der Empfangs-Adresse zurück.
func (w *SolWatcher) getRecentSignatures(ctx context.Context) ([]string, error) {
	var result []struct {
		Signature string `json:"signature"`
		Err       interface{} `json:"err"`
	}

	err := w.rpcCall(ctx, "getSignaturesForAddress", []interface{}{
		w.cfg.ReceiveAddress,
		map[string]interface{}{"limit": 20},
	}, &result)
	if err != nil {
		return nil, err
	}

	sigs := make([]string, 0, len(result))
	for _, r := range result {
		if r.Err == nil { // nur erfolgreiche TXs
			sigs = append(sigs, r.Signature)
		}
	}
	return sigs, nil
}

// solanaTransaction ist die minimale Struktur die wir aus dem TX brauchen.
type solanaTransaction struct {
	Meta *struct {
		PostBalances []int64 `json:"postBalances"`
		PreBalances  []int64 `json:"preBalances"`
		Err          interface{} `json:"err"`
		// LogMessages enthalten u.a. das Memo-Programm-Log. Das Solana-Memo-Programm
		// schreibt eine Zeile wie: "Program log: Memo (len N): \"<text>\"".
		// Darüber ordnen wir die Zahlung EINDEUTIG einem Auftrag zu (Order-ID),
		// statt nur über den Betrag (der bei zwei gleichen Käufen kollidiert).
		LogMessages  []string `json:"logMessages"`
	} `json:"meta"`
	Transaction struct {
		Message struct {
			AccountKeys []string `json:"accountKeys"`
		} `json:"message"`
	} `json:"transaction"`
}

func (w *SolWatcher) getTransaction(ctx context.Context, sig string) (*solanaTransaction, error) {
	var tx solanaTransaction
	err := w.rpcCall(ctx, "getTransaction", []interface{}{
		sig,
		map[string]interface{}{
			"encoding":                       "json",
			"maxSupportedTransactionVersion": 0,
		},
	}, &tx)
	if err != nil {
		return nil, err
	}
	if tx.Meta == nil || tx.Meta.Err != nil {
		return nil, nil // fehlgeschlagene TX ignorieren
	}
	return &tx, nil
}

// extractReceivedLamports gibt zurück wieviel Lamports an unsere Adresse geflossen sind.
func (w *SolWatcher) extractReceivedLamports(tx *solanaTransaction) uint64 {
	keys := tx.Transaction.Message.AccountKeys
	pre  := tx.Meta.PreBalances
	post := tx.Meta.PostBalances

	for i, key := range keys {
		if key == w.cfg.ReceiveAddress && i < len(pre) && i < len(post) {
			diff := post[i] - pre[i]
			if diff > 0 {
				return uint64(diff)
			}
		}
	}
	return 0
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func (w *SolWatcher) expireOrders() {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	for _, o := range w.orders {
		if o.Status == OrderPending && now.After(o.ExpiresAt) {
			o.Status = OrderExpired
		}
	}
}

func (w *SolWatcher) setOrderStatus(id string, status OrderStatus, errMsg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if o, ok := w.orders[id]; ok {
		o.Status   = status
		o.ErrorMsg = errMsg
	}
}

// generateOrderID erzeugt eine kryptografisch sichere Order-ID.
func generateOrderID() string {
	b := make([]byte, 8)
	if _, err := cryptoRand.Read(b); err != nil {
		// Fallback auf Timestamp (sollte nie passieren)
		return fmt.Sprintf("ord-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("ord-%x", b)
}
