// Package chain implementiert die Fundus-eigene PoS-BFT-Blockchain.
//
// Phase 1 (dieses Paket im aktuellen Stand): Kern-Datenstrukturen (Account,
// Transaction, Block), kanonische Serialisierung, BLAKE3-Hashkette,
// Transaktions-Validierung und deterministische Block-Anwendung mit
// Merkle-State. ALLES lokal und ohne Netz/Konsens — verifizierbar per
// Unit-Tests. Der BFT-Konsens (Spec §6) folgt in einer späteren Phase.
//
// Bezug: FUNDUS-CHAIN-SPEC.md.
package chain

import "math/big"

// UFNDPerFND: 1 FND = 1e9 uFND (kleinste Einheit). Spec §3a.
// Bewusst 1e9 (nicht 1e18 wie ERC-20), damit Beträge handlich bleiben.
const UFNDPerFND = 1_000_000_000

// Gebühren-Parameter (Spec §8): feste 1,8 % des bewegten Werts = 18/1000.
const (
	feeNumerator   = 18
	feeDenominator = 1000
)

// Gebühren-AUFTEILUNG: Die 1,8 % werden zwischen dem Node (Block-Produzent, der
// die Transaktion verarbeitet) und dem globalen Fee-Collector geteilt.
// nodeFeeShareNum/Denom = Anteil des NODES; der Rest geht an den Fee-Collector.
//
// Aktuell 50/50 (0,9 % / 0,9 %). Änderbar durch Anpassen dieser Werte — ABER:
// Der Split fließt in die Salden und damit in den State-Root ein. Eine Änderung
// ist ein konsens-relevanter Breaking-Change (alte Blöcke wurden anders
// verbucht) und erfordert einen Chain-Reset bzw. eine erhöhte
// StateSchemaVersion, damit Alt-Chains als inkompatibel erkannt werden. Nicht im
// laufenden Betrieb umstellbar.
const (
	nodeFeeShareNum   = 1 // Zähler:  1/2 = 50 %
	nodeFeeShareDenom = 2 // Nenner
)

// SplitFee teilt eine Gesamtgebühr in (nodeAnteil, collectorAnteil) auf.
// nodeAnteil = fee × nodeFeeShareNum / nodeFeeShareDenom (ganzzahlig abgerundet),
// collectorAnteil = fee − nodeAnteil. So geht KEIN uFND durch Rundung verloren:
// der Rest (inkl. etwaigem Rundungs-Staub) fällt dem Fee-Collector zu.
func SplitFee(fee *big.Int) (nodeShare, collectorShare *big.Int) {
	if fee == nil || fee.Sign() <= 0 {
		return big.NewInt(0), big.NewInt(0)
	}
	nodeShare = new(big.Int).Mul(fee, big.NewInt(nodeFeeShareNum))
	nodeShare.Div(nodeShare, big.NewInt(nodeFeeShareDenom))
	collectorShare = new(big.Int).Sub(fee, nodeShare)
	return nodeShare, collectorShare
}

// maxUint128 = 2^128 - 1. Salden und Beträge sind uint128 (Spec §3).
var maxUint128 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

// FeeForValue berechnet die Gebühr: 1,8 % des Betrags, ganzzahlig abgerundet.
// Determinismus: reine Integer-Arithmetik, kein Float (Spec §3a).
func FeeForValue(amount *big.Int) *big.Int {
	if amount == nil || amount.Sign() <= 0 {
		return big.NewInt(0)
	}
	f := new(big.Int).Mul(amount, big.NewInt(feeNumerator))
	f.Div(f, big.NewInt(feeDenominator))
	return f
}

// NetFromGross rechnet aus einem Gesamtbetrag (Netto + Gebühr) den größtmöglichen
// Netto-Betrag zurück, für den net + FeeForValue(net) <= gross gilt
// ("Gebühr aus dem Betrag"-Modus). Die erste Näherung gross*1000/1018 kann durch
// die Abrundung 1-2 uFND "Staub" liegen lassen; deshalb wird net anschließend
// hochgezählt, solange die Summe gross nicht überschreitet. So bleibt kein Rest
// auf dem Konto und der Sender zahlt nie MEHR als gross.
func NetFromGross(gross *big.Int) *big.Int {
	if gross == nil || gross.Sign() <= 0 {
		return big.NewInt(0)
	}
	net := new(big.Int).Mul(gross, big.NewInt(feeDenominator))
	net.Div(net, big.NewInt(feeDenominator+feeNumerator)) // gross*1000/1018
	// Staub-Korrektur: net so weit erhöhen, dass net+fee == gross (max. wenige Schritte).
	one := big.NewInt(1)
	for {
		cand := new(big.Int).Add(net, one)
		sum := new(big.Int).Add(cand, FeeForValue(cand))
		if sum.Cmp(gross) > 0 {
			break
		}
		net = cand
	}
	return net
}

// fitsUint128 prüft 0 <= v <= 2^128-1.
func fitsUint128(v *big.Int) bool {
	return v != nil && v.Sign() >= 0 && v.Cmp(maxUint128) <= 0
}
