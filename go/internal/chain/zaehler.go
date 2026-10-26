package chain

// Hilfen für den Zählerleser (R827). KEINE Konsensregeln – die Prüfungen von
// TxMeterRegister und TxCommodityCertify bleiben unverändert (commodity.go).

import (
	"crypto/sha256"
	"math/big"
	"strings"
)

// ZaehlerIDAusNummer bildet die 11-Byte-Zählerkennung der Chain aus der
// Zählernummer im Telegramm (OBIS 0-0:96.1.0, z.B. "1EBZ0100507409").
// Die Nummer passt nicht in 11 Byte; daher SHA-256 der normalisierten Nummer
// (Großbuchstaben, ohne Leerraum), die ersten 11 Byte. Jeder kann sie aus der
// Nummer nachrechnen.
func ZaehlerIDAusNummer(nummer string) [11]byte {
	n := strings.ToUpper(strings.Join(strings.Fields(nummer), ""))
	h := sha256.Sum256([]byte("fundus-zaehler-v1:" + n))
	var id [11]byte
	copy(id[:], h[:11])
	return id
}

// MeterRegisterNutzlast kodiert die Nutzlast für TxMeterRegister.
func MeterRegisterNutzlast(id [11]byte, commodity, unit uint8, anfang *big.Int, zeit uint64) []byte {
	return (&MeterRegisterPayload{MeterID: id, Commodity: commodity, Unit: unit,
		InitialReading: anfang, InitialTime: zeit}).encode()
}

// CommodityCertifyNutzlast kodiert die Nutzlast für TxCommodityCertify.
func CommodityCertifyNutzlast(id [11]byte, ta uint64, ca *big.Int, tb uint64, cb *big.Int, roh [32]byte) []byte {
	return (&CommodityCertifyPayload{MeterID: id, TimestampA: ta, CumulativeA: ca,
		TimestampB: tb, CumulativeB: cb, RawDataHash: roh}).encode()
}

// GetMeter liefert den Chain-Eintrag eines Zählers.
func (bc *Blockchain) GetMeter(id [11]byte) (MeterRecord, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.state.GetMeter(id)
}

// GetCommodityToken liefert ein Zertifikat (Token) per ID.
func (bc *Blockchain) GetCommodityToken(id [32]byte) (CommodityToken, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.state.GetToken(id)
}
