package filestore

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/chain"
)

// =============================================================================
//  Quittungs-Fluss (Phase 2b) — Verdienst-Nachweise im store/fetch-Pfad
// =============================================================================
//
// Ablauf:
//   1. Konsument holt (fetch) oder lagert (store) einen Chunk bei Provider P.
//   2. Provider antwortet mit ProviderAddr (seine Wallet-Adresse).
//   3. Konsument signiert eine Quittung "P hat mir N Bytes geliefert/gespeichert"
//      und schickt sie per Op=="receipt" an P zurück.
//   4. Provider verifiziert (er ist der benannte Provider, Signatur gültig) und
//      sammelt die Quittung als Verdienst-Nachweis (receiptStore).
//
// Sicherheit: Die Quittung wird vom KONSUMENTEN signiert. Ein Provider kann sie
// nicht fälschen (kein fremder Privat-Key) und nicht sich selbst ausstellen
// (Provider==Consumer wird abgelehnt). Doppeleinreichung scheitert an der
// ReceiptID-Deduplizierung im receiptStore.

// providerAddrHex liefert die Wallet-Adresse dieses Nodes als Hex (für die
// ChunkResponse), oder "" wenn kein Quittungs-Schlüssel konfiguriert ist.
// Liefert das EINNAHMEN-ZIEL: die Chain schreibt den Verdienst der Adresse im
// Provider-Feld der Quittung gut (vom Konsumenten signiert). Standard ist die
// Node-Wallet; mit gesetzter Reward-Adresse gehen die Einnahmen direkt dorthin
// (z.B. an die Nutzer-Wallet des Betreibers, 256 MiB wie fnd-wallet).
func (fs *FileStore) providerAddrHex() string {
	if fs.signerKey == nil {
		return ""
	}
	return fs.rewardAddr.Hex()
}

// acceptReceipt nimmt eine vom Konsumenten signierte Quittung entgegen. Gibt
// true zurück, wenn sie gültig war, diesen Node als Provider ausweist und neu
// gespeichert wurde.
func (fs *FileStore) acceptReceipt(raw []byte) bool {
	if fs.receipts == nil || len(raw) == 0 {
		return false
	}
	var r chain.Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		fs.log.Warn("Quittung unlesbar", zap.Error(err))
		return false
	}
	// Nur Quittungen annehmen, die DIESEN Node als Provider benennen — sonst
	// könnte jemand fremde oder erfundene Nachweise unterschieben. Ohne eigenen
	// Schlüssel kennt der Node seine Adresse nicht und lehnt grundsätzlich ab.
	// Reward-Adresse ODER Node-Adresse (Quittungen von vor einer Umstellung).
	if fs.signerKey == nil || (r.Provider != fs.rewardAddr && r.Provider != fs.selfAddr) {
		fs.log.Info("DEBUG acceptReceipt abgelehnt",
			zap.Bool("hat_key", fs.signerKey != nil),
			zap.String("quittung_provider", r.Provider.Hex()),
			zap.String("mein_self", fs.selfAddr.Hex()))
		return false
	}
	// VerifyReceipt (Signatur, Selbst-Quittung, Bytes>0 …) macht der Store.
	added := fs.receipts.Add(r)
	fs.log.Info("DEBUG acceptReceipt angenommen",
		zap.Bool("neu_gespeichert", added),
		zap.Uint64("bytes", r.Bytes))
	return added
}

// issueReceipt wird vom KONSUMENTEN nach erfolgreichem store/fetch aufgerufen.
// Erstellt eine signierte Quittung über byteCount Bytes für providerAddrHex und
// schickt sie per Op=="receipt" an den Provider-Peer zurück. Fehler werden nur
// geloggt (best effort) — sie dürfen den Datentransfer nicht blockieren.
func (fs *FileStore) issueReceipt(ctx context.Context, peerID, providerAddrHex, chunkHash string, kind chain.ReceiptKind, byteCount int) {
	if fs.signerKey == nil || providerAddrHex == "" || byteCount <= 0 {
		fs.log.Info("DEBUG issueReceipt Ausstieg 1",
			zap.Bool("hat_key", fs.signerKey != nil),
			zap.String("provider_addr", providerAddrHex),
			zap.Int("bytes", byteCount))
		return // kein Schlüssel oder Provider stellt keinen Nachweis aus
	}
	provAddr, ok := chain.AddressFromHex(providerAddrHex)
	if !ok {
		fs.log.Info("DEBUG issueReceipt: Provider-Adresse ungültig", zap.String("addr", providerAddrHex))
		return
	}
	// Nicht sich selbst quittieren (z.B. wenn man eigene Chunks von sich lädt).
	if provAddr == fs.selfAddr {
		fs.log.Info("DEBUG issueReceipt: Selbst-Quittung übersprungen",
			zap.String("provider", providerAddrHex),
			zap.String("self", fs.selfAddr.Hex()))
		return
	}
	fs.log.Info("DEBUG issueReceipt: stelle Quittung aus",
		zap.String("provider", providerAddrHex),
		zap.Int("bytes", byteCount),
		zap.String("peer", peerID))

	var nonceBuf [8]byte
	_, _ = rand.Read(nonceBuf[:])
	r := chain.Receipt{
		Kind:      kind,
		Provider:  provAddr,
		ChunkHash: hashFromHex(chunkHash),
		Bytes:     uint64(byteCount),
		Timestamp: time.Now().Unix(),
		Nonce:     binary.BigEndian.Uint64(nonceBuf[:]),
	}
	if err := chain.SignReceipt(&r, fs.signerKey); err != nil {
		fs.log.Warn("Quittung signieren fehlgeschlagen", zap.Error(err))
		return
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return
	}
	reqData, _ := json.Marshal(ChunkRequest{Op: "receipt", ChunkHash: chunkHash, Receipt: payload})

	// Best effort: an den Provider zurücksenden. Kurzer Timeout, Fehler ignorieren.
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := fs.sendAndReceive(sendCtx, peerID, reqData); err != nil {
		fs.log.Debug("Quittung an Provider senden fehlgeschlagen",
			zap.String("peer", shortPeer(peerID)), zap.Error(err))
	}
}

// shortPeer kürzt eine Peer-ID für Logs (sicher bei kurzen IDs).
func shortPeer(pid string) string {
	if len(pid) <= 8 {
		return pid
	}
	return pid[:8] + "…"
}

// PendingReceiptCount liefert die Zahl gesammelter, noch nicht geminteter
// Quittungen (für API/Status-Anzeige).
func (fs *FileStore) PendingReceiptCount() int {
	if fs.receipts == nil {
		return 0
	}
	return fs.receipts.Count()
}

// ReceiptStatsData bündelt die Kennzahlen für die Earnings-Anzeige.
type ReceiptStatsData struct {
	PendingCount   int    `json:"pending_count"`    // offene, noch nicht geminteten Quittungen
	FetchBytes     uint64 `json:"fetch_bytes"`      // ausgelieferte Bytes (Transfer-Vergütung)
	StoreBytes     uint64 `json:"store_bytes"`      // eingelagerte Bytes
	HostingEntries int    `json:"hosting_entries"`  // aktiv gehostete fremde Chunks (Vorhaltung)
	NodeAddress    string `json:"node_address"`     // eigene Node-Adresse (Reward-Empfänger)
	SigningActive  bool   `json:"signing_active"`   // ob Quittungen überhaupt ausgestellt werden
	PendingUFND    uint64 `json:"pending_ufnd"`     // offener FND-Gegenwert (uFND) aus fetch+store Bytes
}

// ReceiptStats liefert die aktuellen Verdienst-Kennzahlen des Nodes für die
// Anzeige in der Wallet (offene Quittungen, verdienbare Bytes, Vorhaltung).
func (fs *FileStore) ReceiptStats() ReceiptStatsData {
	var d ReceiptStatsData
	d.SigningActive = fs.signerKey != nil
	if fs.signerKey != nil {
		d.NodeAddress = fs.selfAddr.Hex()
	}
	if fs.receipts != nil {
		d.PendingCount = fs.receipts.Count()
		d.FetchBytes, d.StoreBytes = fs.receipts.PendingBytes()
		// Offener FND-Gegenwert: Transfer-/Store-Anteil (1 FND/TB). Der zeit-
		// abhängige Hosting-Anteil ist erst beim Einlösen exakt und bleibt hier
		// außen vor — die Zahl ist damit eine konservative Untergrenze.
		d.PendingUFND = chain.RewardForBytes(d.FetchBytes + d.StoreBytes).Uint64()
	}
	if fs.hostingLedger != nil {
		d.HostingEntries = fs.hostingLedger.count()
	}
	return d
}

// BuildStorageRewardTx baut einen signierten TxStorageReward aus den gesammelten
// Quittungen. nonce ist die aktuelle Account-Nonce des Node-Wallets (vom Aufrufer
// aus der Chain geholt). Gibt (nil, nil) zurück, wenn keine Quittungen vorliegen
// oder kein Signing-Key konfiguriert ist. Die Quittungen bleiben im Store, bis
// settleRewardedReceipts nach erfolgreichem Mint aufräumt.
func (fs *FileStore) BuildStorageRewardTx(nonce uint64) (*chain.Transaction, [][32]byte, error) {
	if fs.signerKey == nil || fs.receipts == nil {
		return nil, nil, nil
	}
	snap := fs.receipts.Snapshot()
	if len(snap) == 0 {
		return nil, nil, nil
	}
	// Quittungen, die zu klein für ≥1 uFND sind, würden den Mint scheitern
	// lassen (applyStorageReward lehnt sie ab). Daher hier herausfiltern.
	valid := make([]chain.Receipt, 0, len(snap))
	ids := make([][32]byte, 0, len(snap))
	for _, r := range snap {
		if r.Bytes < 1000 { // < 1 uFND Vergütung
			continue
		}
		valid = append(valid, r)
		ids = append(ids, r.ReceiptID())
	}
	if len(valid) == 0 {
		return nil, nil, nil
	}
	payload := chain.StorageRewardPayload{Receipts: valid}
	raw, err := payload.Encode()
	if err != nil {
		return nil, nil, err
	}
	tx, err := chain.BuildSignedTx(fs.signerKey, chain.TxStorageReward, nil, raw, nonce)
	if err != nil {
		return nil, nil, err
	}
	return tx, ids, nil
}

// SettleRewardedReceipts entfernt die erfolgreich geminteten Quittungen aus dem
// Pending-Store (nach Bestätigung, dass der Reward-Tx im Block ist).
func (fs *FileStore) SettleRewardedReceipts(ids [][32]byte) {
	if fs.receipts != nil {
		fs.receipts.Settle(ids)
	}
}

// hashFromHex parst einen Chunk-Hash-Hex-String in ein [32]byte. Bei Manifesten
// oder ungültigem Hex bleibt das Array null (die Quittung bleibt trotzdem
// gültig — der Hash dient nur der Doppel-Quittungs-Abgrenzung).
func hashFromHex(s string) [32]byte {
	var h [32]byte
	b, err := hex.DecodeString(s)
	if err == nil && len(b) == 32 {
		copy(h[:], b)
	}
	return h
}

// =============================================================================
//  Vorhaltungs-Challenge (Konsumenten-Seite, 1 FND/TB·Monat)
// =============================================================================

// challengeAndRewardHosting prüft für einen fälligen Ledger-Eintrag, ob der
// Provider den Chunk NOCH hält (Challenge via fetch-Protokoll), und stellt bei
// Erfolg eine signierte ReceiptHosting-Quittung über die seit der letzten
// Quittung verstrichene Zeit aus. Der Provider bekommt nur vergütet, wenn er den
// Challenge besteht — das macht die Vorhaltungs-Vergütung fälschungssicher.
func (fs *FileStore) challengeAndRewardHosting(ctx context.Context, e hostingEntry) {
	if fs.signerKey == nil {
		return
	}
	provAddr, ok := chain.AddressFromHex(e.ProviderAddr)
	if !ok || provAddr == fs.selfAddr {
		return
	}

	// 1) Challenge: den Chunk beim Provider anfragen. Antwortet er mit den Daten,
	//    hält er den Chunk noch. Wir prüfen zusätzlich den Hash, damit ein
	//    Provider nicht mit Müll "besteht".
	reqData, _ := json.Marshal(ChunkRequest{Op: "fetch", ChunkHash: e.ChunkHash})
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	respData, err := fs.sendAndReceive(cctx, e.PeerID, reqData)
	if err != nil {
		fs.log.Debug("Vorhaltungs-Challenge: Provider nicht erreichbar",
			zap.String("peer", shortPeer(e.PeerID)), zap.Error(err))
		return
	}
	var resp ChunkResponse
	if json.Unmarshal(respData, &resp) != nil || !resp.OK || len(resp.Data) == 0 {
		return // Provider hält den Chunk nicht (mehr) → keine Quittung
	}
	// Hash-Verifikation: gelieferte Daten müssen zum Chunk-Hash passen.
	respHash := blake3Sum256(resp.Data)
	if hex.EncodeToString(respHash[:]) != e.ChunkHash {
		fs.log.Warn("Vorhaltungs-Challenge: Provider lieferte falsche Daten",
			zap.String("peer", shortPeer(e.PeerID)))
		return
	}

	// 2) Dauer seit der letzten Quittung bestimmen.
	now := time.Now().Unix()
	duration := now - e.LastRewarded
	if duration <= 0 {
		return
	}

	// 3) Signierte Vorhaltungs-Quittung ausstellen.
	var nonceBuf [8]byte
	_, _ = rand.Read(nonceBuf[:])
	r := chain.Receipt{
		Kind:            chain.ReceiptHosting,
		Provider:        provAddr,
		ChunkHash:       hashFromHex(e.ChunkHash),
		Bytes:           uint64(e.Bytes),
		DurationSeconds: uint64(duration),
		Timestamp:       now,
		Nonce:           binary.BigEndian.Uint64(nonceBuf[:]),
	}
	if err := chain.SignReceipt(&r, fs.signerKey); err != nil {
		fs.log.Warn("Vorhaltungs-Quittung signieren fehlgeschlagen", zap.Error(err))
		return
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return
	}
	reqData2, _ := json.Marshal(ChunkRequest{Op: "receipt", ChunkHash: e.ChunkHash, Receipt: payload})
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	if _, err := fs.sendAndReceive(sctx, e.PeerID, reqData2); err != nil {
		fs.log.Debug("Vorhaltungs-Quittung senden fehlgeschlagen", zap.Error(err))
		return
	}

	// 4) Zeitraum als vergütet markieren, damit er nicht doppelt quittiert wird.
	fs.hostingLedger.markRewarded(e.ChunkHash, e.ProviderAddr, now)
	fs.log.Info("Vorhaltung quittiert",
		zap.String("provider", e.ProviderAddr[:10]+"…"),
		zap.Int("bytes", e.Bytes),
		zap.Int64("dauer_s", duration))
}

// runHostingChallenges läuft periodisch und challengt alle fälligen Einträge.
// Intervall bestimmt, wie oft ein Provider erneut quittiert wird — für den
// Testbetrieb kurz, produktiv z.B. täglich.
func (fs *FileStore) runHostingChallenges(ctx context.Context, tickEvery, minInterval time.Duration) {
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if fs.hostingLedger == nil {
				continue
			}
			due := fs.hostingLedger.due(minInterval)
			for _, e := range due {
				select {
				case <-ctx.Done():
					return
				default:
				}
				fs.challengeAndRewardHosting(ctx, e)
			}
		}
	}
}

// NodeSeedAvailable meldet, ob für die Node-Wallet neu erzeugte Seed-Wörter zur
// einmaligen Sicherung bereitstehen (nur direkt nach Ersterzeugung).
func (fs *FileStore) NodeSeedAvailable() bool {
	return len(fs.nodeSeedWords) > 0
}

// ConsensusSigner liefert den Signier-Schlüssel und die Adresse dieses Nodes für
// den PoA-Konsens (identisch zur Node-Wallet, die auch Storage-Quittungen
// signiert). Gibt (nil, leer) zurück, wenn keine Wallet aktiv ist (z.B. Seed
// verschlüsselt und noch nicht entsperrt) — dann kann der Node nur synchronisieren.
func (fs *FileStore) ConsensusSigner() (*ecdsa.PrivateKey, string) {
	if fs.signerKey == nil {
		return nil, ""
	}
	return fs.signerKey, fs.selfAddr.Hex()
}

// PeekNodeSeedWords gibt die Seed-Wörter der neu erzeugten Node-Wallet zurück,
// OHNE sie zu löschen. So kann der Betreiber sie mehrfach ansehen (z.B. nach
// versehentlichem Schließen der Seite), bis er die Sicherung bestätigt. Die
// Wörter bleiben ohnehin in node.seed auf der Platte.
func (fs *FileStore) PeekNodeSeedWords() []string {
	return fs.nodeSeedWords
}

// ConfirmSeedBackup markiert die Seed-Sicherung als erledigt und entfernt die
// Wörter aus dem Speicher (sie bleiben in node.seed auf der Platte). Erst NACH
// dieser expliziten Bestätigung verschwindet die Sicherungs-Aufforderung.
func (fs *FileStore) ConfirmSeedBackup() {
	fs.nodeSeedWords = nil
}
