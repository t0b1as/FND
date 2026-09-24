package api

// Solana-HTLC-Client: baut, signiert und sendet die drei HTLC-Instruktionen
// (initiate/redeem/refund) an unser deploytes Programm. Die Discriminatoren und
// die Account-/Argument-Struktur stammen exakt aus der IDL (fundus_htlc.json).
//
// Signiert wird mit dem vom Nutzer eingegebenen Solana-Schlüssel (Base58 oder
// Mnemonic). Der Schlüssel wird NUR für die eine Transaktion genutzt und nicht
// gespeichert. Sicherheitshinweis: Bei einem nicht vertrauenswürdigen Node ist
// direkte Schlüssel-Eingabe unsicher — für den lokalen/eigenen Node vertretbar.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	confirm "github.com/gagliardetto/solana-go/rpc/sendAndConfirmTransaction"
	"github.com/gagliardetto/solana-go/rpc/ws"
	"github.com/mr-tron/base58"
	"lukechampine.com/blake3"
)

// IDL-Discriminatoren (die ersten 8 Byte jeder Instruktion).
var (
	discInitiate = []byte{5, 63, 123, 113, 153, 75, 148, 14}
	discRedeem   = []byte{184, 12, 86, 149, 70, 196, 97, 225}
	discRefund   = []byte{2, 96, 183, 251, 63, 208, 46, 46}
)

const swapAccountSeed = "swap_account"

// solHTLCClient kapselt RPC + Programm-ID.
type solHTLCClient struct {
	rpcURL    string
	wsURL     string
	programID solana.PublicKey
}

func newSolHTLCClient(rpcURL, programIDBase58 string) (*solHTLCClient, error) {
	pid, err := solana.PublicKeyFromBase58(programIDBase58)
	if err != nil {
		return nil, fmt.Errorf("ungültige Programm-ID: %w", err)
	}
	// WebSocket-URL aus der RPC-URL ableiten (http→ws, https→wss).
	wsURL := rpcURL
	if len(rpcURL) > 7 && rpcURL[:7] == "http://" {
		wsURL = "ws://" + rpcURL[7:]
	} else if len(rpcURL) > 8 && rpcURL[:8] == "https://" {
		wsURL = "wss://" + rpcURL[8:]
	}
	return &solHTLCClient{rpcURL: rpcURL, wsURL: wsURL, programID: pid}, nil
}

// parseSolKey wandelt einen eingegebenen Solana-Schlüssel in ein Keypair um.
// Akzeptiert Base58-Private-Key ODER eine BIP-39-Mnemonic (Leerzeichen-getrennt).
func parseSolKey(input string) (solana.PrivateKey, error) {
	pk, _, err := parseSolKeyWithPath(input, "")
	return pk, err
}

// parseSolKeyWithPath leitet den Schlüssel ab. Bei einem Mnemonic bestimmt der
// path den Ableitungspfad; ist path leer, wird der solana-keygen-Standard
// genutzt (roher Seed OHNE BIP-44-Pfad — das ist, was die CLI tut). Gibt auch
// die abgeleitete Adresse zurück (für die Diagnose).
func parseSolKeyWithPath(input, path string) (solana.PrivateKey, string, error) {
	input = strings.TrimSpace(input)

	// Format 1: solana-keygen JSON-Byte-Array [1,2,3,...] — das native CLI-Format.
	// Umgeht die Mnemonic-Ableitungs-Mehrdeutigkeit komplett (100% CLI-kompatibel).
	if strings.HasPrefix(input, "[") {
		pk, err := solana.PrivateKeyFromSolanaKeygenFileBytes([]byte(input))
		if err != nil {
			return nil, "", fmt.Errorf("Keygen-JSON ungültig: %w", err)
		}
		return pk, pk.PublicKey().String(), nil
	}

	// Format 2: Mnemonic (mehrere Wörter).
	if len(strings.Fields(input)) >= 12 {
		var pk solana.PrivateKey
		var err error
		switch path {
		case "raw":
			// Roher BIP-39-Seed ohne SLIP-0010-Ableitung.
			pk, err = solana.PrivateKeyFromMnemonic(input, "")
		case "", "cli":
			// Node-Standard für die Ausführung: m/44'/501'/0'/0' (Phantom/Backpack).
			// Das ist der Pfad, den die meisten Wallets nutzen. (Diagnose zeigt
			// alle Varianten — bei Abweichung hier anpassen.)
			pk, err = solana.PrivateKeyFromMnemonicAtPath(input, "", "m/44'/501'/0'/0'")
		default:
			pk, err = solana.PrivateKeyFromMnemonicAtPath(input, "", path)
		}
		if err != nil {
			return nil, "", fmt.Errorf("Mnemonic ungültig: %w", err)
		}
		return pk, pk.PublicKey().String(), nil
	}

	// Format 3: Base58-Private-Key.
	pk, err := solana.PrivateKeyFromBase58(input)
	if err != nil {
		return nil, "", fmt.Errorf("Solana-Schlüssel ungültig (weder Keygen-JSON, Mnemonic noch Base58): %w", err)
	}
	return pk, pk.PublicKey().String(), nil
}

// derivedAddresses probiert systematisch viele Ableitungspfade durch und gibt
// für jeden die Adresse zurück. Mit der Zieladresse (aus solana address) findet
// man so den passenden Pfad. Deckt CLI, Phantom, Solflare, Ledger + Indizes ab.
func derivedAddresses(mnemonic string) map[string]string {
	out := map[string]string{}
	// Feste, benannte Pfade.
	named := map[string]string{
		"roher Seed (kein Pfad)":      "raw",
		"m/44'/501'":                  "m/44'/501'",
		"m/44'/501'/0'":               "m/44'/501'/0'",
		"m/44'/501'/0'/0'":            "m/44'/501'/0'/0'",
		"m/44'/501'/0'/0'/0'":         "m/44'/501'/0'/0'/0'",
	}
	for label, p := range named {
		if _, addr, err := parseSolKeyWithPath(mnemonic, p); err == nil {
			out[label] = addr
		}
	}
	// Account-Index-Varianten m/44'/501'/i' und m/44'/501'/i'/0' (i=0..4).
	for i := 0; i < 5; i++ {
		p1 := fmt.Sprintf("m/44'/501'/%d'", i)
		p2 := fmt.Sprintf("m/44'/501'/%d'/0'", i)
		if _, addr, err := parseSolKeyWithPath(mnemonic, p1); err == nil {
			out[p1] = addr
		}
		if _, addr, err := parseSolKeyWithPath(mnemonic, p2); err == nil {
			out[p2] = addr
		}
	}
	return out
}

// deriveSwapPDA berechnet die PDA-Adresse des swap_account (seeds:
// "swap_account" + initiator + secret_hash), exakt wie im Programm.
func (c *solHTLCClient) deriveSwapPDA(initiator solana.PublicKey, secretHash [32]byte) (solana.PublicKey, uint8, error) {
	return solana.FindProgramAddress(
		[][]byte{
			[]byte(swapAccountSeed),
			initiator.Bytes(),
			secretHash[:],
		},
		c.programID,
	)
}

// Initiate baut + sendet die initiate-Instruktion: sperrt SOL im PDA-Vault.
func (c *solHTLCClient) Initiate(ctx context.Context, initiatorKey solana.PrivateKey,
	amountLamports, expiresInSlots uint64, redeemer solana.PublicKey, secretHash [32]byte) (string, error) {

	initiator := initiatorKey.PublicKey()
	swapPDA, _, err := c.deriveSwapPDA(initiator, secretHash)
	if err != nil {
		return "", err
	}

	// Instruktionsdaten: discriminator + amount_lamports(u64) + expires_in_slots(u64)
	//   + redeemer(pubkey, 32) + secret_hash([u8;32])
	data := make([]byte, 0, 8+8+8+32+32)
	data = append(data, discInitiate...)
	data = appendU64(data, amountLamports)
	data = appendU64(data, expiresInSlots)
	data = append(data, redeemer.Bytes()...)
	data = append(data, secretHash[:]...)

	accounts := solana.AccountMetaSlice{
		&solana.AccountMeta{PublicKey: initiator, IsSigner: true, IsWritable: true},
		&solana.AccountMeta{PublicKey: swapPDA, IsSigner: false, IsWritable: true},
		&solana.AccountMeta{PublicKey: solana.SystemProgramID, IsSigner: false, IsWritable: false},
	}
	return c.sendIx(ctx, initiatorKey, accounts, data)
}

// Redeem baut + sendet die redeem-Instruktion: löst mit dem Geheimnis ein.
func (c *solHTLCClient) Redeem(ctx context.Context, redeemerKey solana.PrivateKey,
	initiator solana.PublicKey, secretHash [32]byte, secret [32]byte) (string, error) {

	swapPDA, _, err := c.deriveSwapPDA(initiator, secretHash)
	if err != nil {
		return "", err
	}
	data := make([]byte, 0, 8+32)
	data = append(data, discRedeem...)
	data = append(data, secret[:]...)

	accounts := solana.AccountMetaSlice{
		&solana.AccountMeta{PublicKey: redeemerKey.PublicKey(), IsSigner: true, IsWritable: true},
		&solana.AccountMeta{PublicKey: swapPDA, IsSigner: false, IsWritable: true},
	}
	return c.sendIx(ctx, redeemerKey, accounts, data)
}

// Refund baut + sendet die refund-Instruktion: gibt nach Timelock zurück.
func (c *solHTLCClient) Refund(ctx context.Context, initiatorKey solana.PrivateKey,
	secretHash [32]byte) (string, error) {

	initiator := initiatorKey.PublicKey()
	swapPDA, _, err := c.deriveSwapPDA(initiator, secretHash)
	if err != nil {
		return "", err
	}
	data := make([]byte, 0, 8)
	data = append(data, discRefund...)

	accounts := solana.AccountMetaSlice{
		&solana.AccountMeta{PublicKey: initiator, IsSigner: true, IsWritable: true},
		&solana.AccountMeta{PublicKey: swapPDA, IsSigner: false, IsWritable: true},
	}
	return c.sendIx(ctx, initiatorKey, accounts, data)
}

// sendIx baut die Transaktion mit einer Instruktion, signiert sie mit dem
// Signer und sendet sie mit Bestätigung.
func (c *solHTLCClient) sendIx(ctx context.Context, signer solana.PrivateKey,
	accounts solana.AccountMetaSlice, data []byte) (string, error) {

	rpcClient := rpc.New(c.rpcURL)
	recent, err := rpcClient.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return "", fmt.Errorf("Blockhash holen fehlgeschlagen: %w", err)
	}

	ix := solana.NewInstruction(c.programID, accounts, data)
	tx, err := solana.NewTransaction(
		[]solana.Instruction{ix},
		recent.Value.Blockhash,
		solana.TransactionPayer(signer.PublicKey()),
	)
	if err != nil {
		return "", fmt.Errorf("Transaktion bauen fehlgeschlagen: %w", err)
	}
	_, err = tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(signer.PublicKey()) {
			return &signer
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("Signieren fehlgeschlagen: %w", err)
	}

	wsClient, err := ws.Connect(ctx, c.wsURL)
	if err != nil {
		// Ohne WS senden wir trotzdem (ohne auf Bestätigung zu warten).
		sig, serr := rpcClient.SendTransaction(ctx, tx)
		if serr != nil {
			return "", fmt.Errorf("Senden fehlgeschlagen: %w", serr)
		}
		return sig.String(), nil
	}
	defer wsClient.Close()

	sig, err := confirm.SendAndConfirmTransaction(ctx, rpcClient, wsClient, tx)
	if err != nil {
		return "", fmt.Errorf("Senden/Bestätigen fehlgeschlagen: %w", err)
	}
	return sig.String(), nil
}

// ── kleine Helfer ────────────────────────────────────────────────────────────

func appendU64(b []byte, v uint64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append(b, buf[:]...)
}

// solanaPubkeyFromString parst eine Solana-Adresse (Base58).
func solanaPubkeyFromString(s string) (solana.PublicKey, error) {
	return solana.PublicKeyFromBase58(strings.TrimSpace(s))
}

// hash32FromHex parst 32 Byte aus einem Hex-String (mit oder ohne 0x).
func hash32FromHex(s string) ([32]byte, error) {
	var out [32]byte
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	raw, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	if len(raw) != 32 {
		return out, fmt.Errorf("erwarte 32 Byte, war %d", len(raw))
	}
	copy(out[:], raw)
	return out, nil
}

// ReadSecretFromClaim liest das enthüllte Geheimnis aus der redeem-Transaktion,
// die den HTLC eingelöst hat. Für die FND-kauft-SOL-Richtung: Der Maker liest S
// von Solana, nachdem der Taker die SOL eingelöst hat.
//
// Ablauf: PDA berechnen → Signaturen der PDA holen → jede Tx prüfen, ob sie
// unser Programm mit der redeem-Instruktion aufruft → secret (32 Byte nach dem
// 8-Byte-Discriminator) extrahieren und prüfen, dass blake3(secret)==hashlock.
func (c *solHTLCClient) ReadSecretFromClaim(ctx context.Context, sm *SwapManager,
	initiator solana.PublicKey, secretHash [32]byte) ([32]byte, bool) {

	var empty [32]byte
	pda, _, err := c.deriveSwapPDA(initiator, secretHash)
	if err != nil {
		return empty, false
	}
	// 1. Signaturen für die PDA holen (neueste zuerst).
	sigRes, err := sm.solanaRPCCall(ctx, "getSignaturesForAddress", []interface{}{
		pda.String(), map[string]interface{}{"limit": 20},
	})
	if err != nil {
		return empty, false
	}
	var sigs []struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(sigRes, &sigs); err != nil {
		return empty, false
	}
	// 2. Jede Transaktion prüfen.
	for _, sg := range sigs {
		txRes, err := sm.solanaRPCCall(ctx, "getTransaction", []interface{}{
			sg.Signature,
			map[string]interface{}{"encoding": "json", "maxSupportedTransactionVersion": 0},
		})
		if err != nil {
			continue
		}
		if secret, ok := extractRedeemSecret(txRes, c.programID, secretHash); ok {
			return secret, true
		}
	}
	return empty, false
}

// extractRedeemSecret sucht in einer Transaktion die redeem-Instruktion unseres
// Programms und extrahiert das secret-Argument.
func extractRedeemSecret(txRes json.RawMessage, programID solana.PublicKey, secretHash [32]byte) ([32]byte, bool) {
	var empty [32]byte
	var tx struct {
		Transaction struct {
			Message struct {
				AccountKeys  []string `json:"accountKeys"`
				Instructions []struct {
					ProgramIDIndex int    `json:"programIdIndex"`
					Data           string `json:"data"` // base58
				} `json:"instructions"`
			} `json:"message"`
		} `json:"transaction"`
	}
	if err := json.Unmarshal(txRes, &tx); err != nil {
		return empty, false
	}
	keys := tx.Transaction.Message.AccountKeys
	for _, ix := range tx.Transaction.Message.Instructions {
		if ix.ProgramIDIndex < 0 || ix.ProgramIDIndex >= len(keys) {
			continue
		}
		if keys[ix.ProgramIDIndex] != programID.String() {
			continue
		}
		// Instruktionsdaten sind base58-kodiert.
		data := base58Decode(ix.Data)
		// redeem: 8 Byte Discriminator + 32 Byte secret.
		if len(data) != 8+32 {
			continue
		}
		if !bytesEqual(data[:8], discRedeem) {
			continue
		}
		var secret [32]byte
		copy(secret[:], data[8:40])
		// Verifizieren: blake3(secret) == hashlock.
		if blake3Sum(secret) == secretHash {
			return secret, true
		}
	}
	return empty, false
}

func base58Decode(s string) []byte {
	b, err := base58.Decode(s)
	if err != nil {
		return nil
	}
	return b
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func blake3Sum(secret [32]byte) [32]byte {
	return blake3.Sum256(secret[:])
}
