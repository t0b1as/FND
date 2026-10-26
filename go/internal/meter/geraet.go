package meter

// Gerätesignatur für Zählerablesungen (R827).
//
// Bisher trug eine Ablesung nur einen Prüfwert, gebildet mit einem GEHEIMEN
// Schlüssel des Nodes – prüfen konnte ihn nur der Node selbst. Jetzt
// unterschreibt der Zählerleser jede Ablesung mit einem eigenen Ed25519-
// Schlüssel. Den öffentlichen Teil trägt die Ablesung mit; jeder im Netz kann
// die Unterschrift prüfen.
//
// Unterschrieben wird eine feste Textform (keine JSON-Mehrdeutigkeit):
//   fundus-zaehler-v1|<Zählernummer>|<Unix-Sekunden>|<Ws Bezug>|<Ws Einspeisung>
// Zählwerke als GANZE Wattsekunden (SI-Ganzzahl wie auf der Chain), nie Float.

import (
	"crypto/ed25519"
	"encoding/hex"
	"math"
	"math/big"
	"strconv"
	"strings"

	"lukechampine.com/blake3"
)

// KWhZuWs rechnet einen Zählerstand in ganze Wattsekunden um, auf volle
// Wattstunden gerundet – genauer zeigen die Zählwerke ohnehin nicht an.
func KWhZuWs(kwh float64) *big.Int {
	if kwh <= 0 || math.IsNaN(kwh) || math.IsInf(kwh, 0) {
		return new(big.Int)
	}
	wh := new(big.Int).SetInt64(int64(math.Round(kwh * 1000)))
	return wh.Mul(wh, big.NewInt(3600))
}

// AblesungNachricht ist der unterschriebene Text einer Ablesung.
func AblesungNachricht(zaehler string, unix int64, wsBezug, wsEinspeisung string) []byte {
	return []byte("fundus-zaehler-v1|" + strings.TrimSpace(zaehler) + "|" + strconv.FormatInt(unix, 10) +
		"|" + wsBezug + "|" + wsEinspeisung)
}

// AblesungPruefen prüft die Gerätesignatur einer Ablesung.
func AblesungPruefen(geraetHex, sigHex, zaehler string, unix int64, wsBezug, wsEinspeisung string) bool {
	pub, err := hex.DecodeString(geraetHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, AblesungNachricht(zaehler, unix, wsBezug, wsEinspeisung), sig)
}

// GeraetSchluesselAbleiten bildet den Geräteschlüssel eines Zählers aus der
// Wurzel des Nodes und der Zählernummer (R830).
//
// Die Wurzel stammt aus den gesicherten Seed-Wörtern des Nodes. Dieselben
// Wörter plus dieselbe Zählernummer ergeben auf jedem Node denselben
// Schlüssel – keine eigene Schlüsseldatei, nichts zusätzlich zu sichern. Die
// Zählernummer macht jeden Zähler eigen, ist aber NICHT das Geheimnis: Sie ist
// öffentlich (Typenschild, Telegramm, Rechnung). Das Geheimnis bleibt die Wurzel.
func GeraetSchluesselAbleiten(wurzel []byte, zaehlernummer string) ed25519.PrivateKey {
	h := blake3.New(32, nil)
	h.Write([]byte("fundus-zaehler-geraet-v1:"))
	h.Write(wurzel)
	h.Write([]byte("|"))
	h.Write([]byte(NummerNormal(zaehlernummer)))
	return ed25519.NewKeyFromSeed(h.Sum(nil))
}

// NummerNormal: Großbuchstaben, ohne Leerraum – wie chain.ZaehlerIDAusNummer.
func NummerNormal(nummer string) string {
	return strings.ToUpper(strings.Join(strings.Fields(nummer), ""))
}

// geraetFuer liefert den Schlüssel für eine Zählernummer (zwischengespeichert).
// Nil, solange der Node seine Wurzel nicht hat (verschlüsselter Seed noch
// nicht entsperrt) – dann wird nicht unterschrieben.
func (r *Reader) geraetFuer(nummer string) ed25519.PrivateKey {
	n := NummerNormal(nummer)
	r.geraetMu.Lock()
	defer r.geraetMu.Unlock()
	if k, ok := r.geraete[n]; ok {
		return k
	}
	if r.cfg.GeraetWurzel == nil {
		return nil
	}
	w := r.cfg.GeraetWurzel()
	if len(w) == 0 {
		return nil
	}
	k := GeraetSchluesselAbleiten(w, n)
	if r.geraete == nil {
		r.geraete = map[string]ed25519.PrivateKey{}
	}
	r.geraete[n] = k
	return k
}

// unterschreiben ergänzt eine Ablesung um Wattsekunden und Gerätesignatur.
// Ohne Zählernummer aus dem Telegramm wird NICHT unterschrieben: Eine
// Unterschrift ohne Zähler belegt nichts.
func (r *Reader) unterschreiben(t *Token) {
	t.WsBezug = KWhZuWs(t.KWh).String()
	t.WsEinspeisung = KWhZuWs(t.KWhFeed).String()
	if strings.TrimSpace(t.MeterID) == "" {
		return
	}
	geraet := r.geraetFuer(t.MeterID)
	if geraet == nil {
		return
	}
	t.GeraetPub = hex.EncodeToString(geraet.Public().(ed25519.PublicKey))
	t.GeraetSig = hex.EncodeToString(ed25519.Sign(geraet,
		AblesungNachricht(t.MeterID, t.Timestamp.Unix(), t.WsBezug, t.WsEinspeisung)))
}
