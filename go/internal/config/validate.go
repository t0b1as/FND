package config

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// =============================================================================
//  Kanonische Adressen – werden bei Release eingebrannt
// =============================================================================

// KanonischeFeeCollector ist die EINZIGE Quelle der Wahrheit für die Gebühren-
// und Update-Signatur-Adresse. Abgeleitet aus dem Fundus-Seed via Argon2id
// (256 MiB) → secp256k1-Key → BLAKE3-256(pubkey)[:20] (Phase 2.4, Spec §13).
// Nodes, die eine abweichende Adresse konfigurieren, starten nicht.
//
// Diese Adresse wurde via fnd-wallet (BLAKE3-Ableitung, 256 MiB) erzeugt und ist
// damit per Seed reproduzierbar — frühere Adressen (Keccak-Altadresse
// 0x7c4B93…, sowie die mit 700 MiB erzeugte 0xd6dfC7…) sind abgelöst.
//
// ADRESSWECHSEL (z.B. beim Ausrollen nach Neu-Ableitung):
//   1. diese Konstante ändern        → wirkt auf Node-Validierung UND
//                                       update.signatureAuthority (verweist hierauf)
//   2. deploy-fundus.ps1 -FeeCollector → schreibt env beim Deploy (mit -WriteEnv)
//   3. fundus.env-Template (nur Default; der Pi-Wert kommt aus Schritt 2)
// Mehr Stellen gibt es nicht — die zwei Code-Duplikate wurden vereinheitlicht.
const KanonischeFeeCollector = "0xea5594a7cc26d2456e9a033481d01a5ba101c8f8"

// EffectiveFeeCollector liefert die zu verwendende Fee-Collector-Adresse.
//
// Die EINZIGE Pflichtquelle ist KanonischeFeeCollector. FUNDUS_FND_FEE_COLLECTOR
// in fundus.env ist nur ein optionaler Override-Versuch:
//   - leer        → kanonische Adresse (Normalfall, keine env-Pflege nötig)
//   - == kanonisch → kanonische Adresse (env darf sie spiegeln)
//   - != kanonisch → wird von Validate() als Fatal abgelehnt; hier defensiv
//                     ebenfalls die kanonische Adresse, damit niemals eine
//                     abweichende Adresse in den Genesis gelangt.
//
// Dadurch leiten Chain-Genesis, Deploy und env alle aus derselben Konstante ab,
// statt drei manuell synchron gehaltene Werte zu haben.
func (c *Config) EffectiveFeeCollector() string {
	if c.FNDFeeCollector == "" {
		return KanonischeFeeCollector
	}
	if strings.EqualFold(c.FNDFeeCollector, KanonischeFeeCollector) {
		return KanonischeFeeCollector
	}
	// Abweichung: Validate() lehnt den Start ohnehin ab. Falls dieser Pfad
	// dennoch erreicht wird, niemals die abweichende Adresse verwenden.
	return KanonischeFeeCollector
}

// MinSolanaAddrLen ist die Mindestlänge einer gültigen Solana-Adresse (Base58).
const MinSolanaAddrLen = 32

// =============================================================================
//  Validierungsergebnisse
// =============================================================================

// ValidationResult beschreibt das Ergebnis einer Konfigurationsprüfung.
type ValidationResult struct {
	OK       bool
	Errors   []string
	Warnings []string
}

func (r *ValidationResult) addError(msg string)   { r.Errors = append(r.Errors, msg) }
func (r *ValidationResult) addWarning(msg string) { r.Warnings = append(r.Warnings, msg) }

// =============================================================================
//  Validate – Hauptprüfung
// =============================================================================

// Validate prüft die Konfiguration auf Vollständigkeit und Korrektheit.
// Bestimmte Prüfungen (Adress-Integrität) sind erzwungen und können nicht
// durch den Betreiber umgangen werden.
func (c *Config) Validate(log *zap.Logger) ValidationResult {
	r := ValidationResult{OK: true}

	// ── 1. Erzwungene Prüfungen (Hard Constraints) ──────────────────────────
	// Diese schützen Nutzer vor fehlgeleiteten Gebühren.

	// Fee Collector muss der kanonischen Adresse entsprechen
	if c.FNDFeeCollector != "" {
		configured := strings.ToLower(c.FNDFeeCollector)
		canonical  := strings.ToLower(KanonischeFeeCollector)
		if configured != canonical {
			r.addError(fmt.Sprintf(
				"FUNDUS_FND_FEE_COLLECTOR ist ungültig.\n"+
					"  Konfiguriert: %s\n"+
					"  Erwartet:     %s\n"+
					"  Die Fee-Collector-Adresse ist festgelegt und kann nicht geändert werden.",
				c.FNDFeeCollector, KanonischeFeeCollector,
			))
			r.OK = false
		}
	}

	// Storage-Reward-Adresse (optional): wenn gesetzt, muss sie eine gültige
	// FND-Adresse sein (40 Hex-Zeichen, optional 0x-Präfix). Leer = Node-Adresse.
	if c.StorageRewardAddr != "" && !isValidFNDAddress(c.StorageRewardAddr) {
		r.addError(fmt.Sprintf(
			"FUNDUS_STORAGE_REWARD_ADDR ist ungültig: %q.\n"+
				"  Erwartet: 40 Hex-Zeichen (optional mit 0x-Präfix), z.B. 0xabc…\n"+
				"  Leer lassen, um die Node-Adresse aus FUNDUS_WALLET_PRIV_KEY zu verwenden.",
			c.StorageRewardAddr,
		))
		r.OK = false
	}

	// ── 2. Shop-Validierung ──────────────────────────────────────────────────
	if c.ShopEnabled {
		if c.ShopReceiveAddr == "" {
			r.addError("FUNDUS_SHOP_ENABLED=true aber FUNDUS_SHOP_RECEIVE_ADDR fehlt.")
			r.OK = false
		} else if len(c.ShopReceiveAddr) < MinSolanaAddrLen {
			r.addError(fmt.Sprintf(
				"FUNDUS_SHOP_RECEIVE_ADDR zu kurz (%d Zeichen, min %d). "+
					"Gültige Solana-Adresse (Base58) angeben.",
				len(c.ShopReceiveAddr), MinSolanaAddrLen,
			))
			r.OK = false
		}

		if c.ShopSlippageBPS < 0 || c.ShopSlippageBPS > 500 {
			r.addError(fmt.Sprintf(
				"FUNDUS_SHOP_SLIPPAGE_BPS=%d ungültig. Erlaubt: 0–500 (0–5%%).",
				c.ShopSlippageBPS,
			))
			r.OK = false
		}

		if c.ChainID == 0 {
			r.addWarning("FUNDUS_CHAIN_ID nicht gesetzt – nutze Standardwert 100 (Gnosis Chain).")
		}
		if c.FNDAddress == "" {
			r.addWarning("FUNDUS_FND_ADDRESS fehlt – MintFND() wird fehlschlagen.")
		}
		if c.WalletPrivKey == "" {
			r.addWarning("FUNDUS_WALLET_PRIV_KEY fehlt – keine Blockchain-Transaktionen möglich.")
		}
	}

	// ── 3. Allgemeine Prüfungen ──────────────────────────────────────────────
	if c.Port < 1 || c.Port > 65535 {
		r.addError(fmt.Sprintf("FUNDUS_PORT=%d ungültig. Erlaubt: 1–65535.", c.Port))
		r.OK = false
	}
	if c.P2PPort < 1 || c.P2PPort > 65535 {
		r.addError(fmt.Sprintf("FUNDUS_P2P_PORT=%d ungültig.", c.P2PPort))
		r.OK = false
	}
	if c.Port == c.P2PPort {
		r.addError("FUNDUS_PORT und FUNDUS_P2P_PORT dürfen nicht identisch sein.")
		r.OK = false
	}
	if c.DataDir == "" {
		r.addError("FUNDUS_DATA_DIR fehlt.")
		r.OK = false
	}

	// ── 4. Meter-Prüfungen ──────────────────────────────────────────────────
	if c.MeterProtocol != "" {
		validProtocols := map[string]bool{"sml": true, "d0": true, "http": true, "mock": true}
		if !validProtocols[c.MeterProtocol] {
			r.addError(fmt.Sprintf(
				"FUNDUS_METER_PROTOCOL=%q ungültig. Erlaubt: sml, d0, http, mock.",
				c.MeterProtocol,
			))
			r.OK = false
		}
		if c.MeterProtocol == "http" && c.MeterHTTPURL == "" {
			r.addError("FUNDUS_METER_PROTOCOL=http aber FUNDUS_METER_HTTP_URL fehlt.")
			r.OK = false
		}
	}

	// ── Filesharing / Storage-Allokation ────────────────────────────────────
	if c.StorageOfferGB > 0 {
		minAlloc := c.StorageOfferGB * 5

		if c.StorageAllocGB == 0 {
			// Automatisch auf 5× setzen
			c.StorageAllocGB = minAlloc
			r.addWarning(fmt.Sprintf(
				"FUNDUS_STORAGE_ALLOC_GB nicht gesetzt – automatisch auf %d GB (5 × %d GB Offer).",
				minAlloc, c.StorageOfferGB,
			))
		} else if c.StorageAllocGB < minAlloc {
			r.addError(fmt.Sprintf(
				"FUNDUS_STORAGE_ALLOC_GB=%d GB ist zu klein. "+
					"Minimum: 5 × FUNDUS_STORAGE_OFFER_GB = %d GB. "+
					"Grund: eigene Chunks (%d GB) + 4× Spiegel-Chunks anderer Nodes (%d GB).",
				c.StorageAllocGB, minAlloc,
				c.StorageOfferGB, c.StorageOfferGB*4,
			))
			r.OK = false
		} else if c.StorageAllocGB > minAlloc {
			// Mehr als Minimum ist OK (Puffer für Wachstum)
			r.addWarning(fmt.Sprintf(
				"FUNDUS_STORAGE_ALLOC_GB=%d GB (Offer=%d GB, Minimum=%d GB) – %d GB Puffer.",
				c.StorageAllocGB, c.StorageOfferGB, minAlloc,
				c.StorageAllocGB-minAlloc,
			))
		}

		if c.StorageDir == "" {
			r.addError("FUNDUS_STORAGE_DIR fehlt.")
			r.OK = false
		}
	}

	// ── Ausgabe ──────────────────────────────────────────────────────────────
	for _, err := range r.Errors {
		log.Error("Konfigurationsfehler", zap.String("detail", err))
	}
	for _, warn := range r.Warnings {
		log.Warn("Konfigurationswarnung", zap.String("detail", warn))
	}
	if r.OK {
		log.Info("Konfiguration geprüft – OK")
	}

	return r
}

// MustValidate prüft die Konfiguration und beendet den Prozess bei Fehlern.
func (c *Config) MustValidate(log *zap.Logger) {
	result := c.Validate(log)
	if !result.OK {
		log.Fatal("Konfiguration ungültig – Node wird nicht gestartet.",
			zap.Strings("errors", result.Errors))
	}
}

// isValidFNDAddress prüft das Format einer FND-Adresse: genau 40 Hex-Zeichen,
// optional mit 0x-Präfix. Bewusst ohne internal/chain-Import (Zyklusvermeidung)
// und ohne encoding/hex (schlanker) — reine Zeichenprüfung.
func isValidFNDAddress(s string) bool {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
	}
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}
