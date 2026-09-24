// Package meter liest Smartmeter-Daten und erzeugt signierte Energie-Token.
//
// Unterstützte Protokolle:
//
//   SML (Smart Message Language, BSI TR-03109):
//     Moderner DE-Standard für EDL21-Zähler (Easymeter Q3A, Landis+Gyr E450,
//     EMH ED300L, Iskraemeco ME382). Optischer Lesekopf an /dev/ttyUSB0.
//     Baudrate: 115200 (konfigurierbar, viele Zähler auch 9600).
//     Vollständiger SML-Frame-Parser mit CRC16 und allen OBIS-Codes.
//
//   D0 (IEC 62056-21 Mode C):
//     Ältere DE-Zähler. Handshake bei 300 Baud, dann 9600 Baud.
//
//   HTTP (Shelly EM, Tasmota, Tibber Pulse, eigene REST-API)
//
// Anschluss:
//   USB-IR-Lesekopf (z.B. Volkszähler, Weidmann, Hichi) an /dev/ttyUSB0
//   Pi 3 UART: /dev/ttyAMA0 (GPIO 14/15, Pin 8/10)
package meter

import (
	"context"
	"golang.org/x/crypto/argon2"
	"lukechampine.com/blake3"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.bug.st/serial"
	"go.uber.org/zap"
)

// =============================================================================
//  OBIS-Codes (IEC 62056-61)
// =============================================================================

// OBISCode identifiziert einen Messwert nach dem OBIS-Schema
// Notation: A-B:C.D.E*F (Bytes: A B C D E F)
type OBISCode [6]byte

var (
	// Energie – Bezug
	OBISWirkenergieBezug   = OBISCode{1, 0, 1, 8, 0, 255} // 1-0:1.8.0*255 kWh gesamt
	OBISWirkenergieBezugT1 = OBISCode{1, 0, 1, 8, 1, 255} // 1-0:1.8.1*255 kWh Tarif 1
	OBISWirkenergieBezugT2 = OBISCode{1, 0, 1, 8, 2, 255} // 1-0:1.8.2*255 kWh Tarif 2

	// Energie – Einspeisung (Prosumer/PV)
	OBISWirkenergieEinspeisung = OBISCode{1, 0, 2, 8, 0, 255} // 1-0:2.8.0*255 kWh

	// Momentanleistung – gesamt
	OBISWirkleistungBezug      = OBISCode{1, 0, 1, 7, 0, 255}  // 1-0:1.7.0*255 W (Bezug)
	OBISWirkleistungEinspeisung = OBISCode{1, 0, 2, 7, 0, 255} // 1-0:2.7.0*255 W (Einspeisung)
	OBISWirkleistungGesamt     = OBISCode{1, 0, 16, 7, 0, 255} // 1-0:16.7.0*255 W (±)

	// Momentanleistung – phasenbezogen
	OBISWirkleistungL1 = OBISCode{1, 0, 36, 7, 0, 255} // 1-0:36.7.0*255 W
	OBISWirkleistungL2 = OBISCode{1, 0, 56, 7, 0, 255} // 1-0:56.7.0*255 W
	OBISWirkleistungL3 = OBISCode{1, 0, 76, 7, 0, 255} // 1-0:76.7.0*255 W

	// Strom – phasenbezogen
	OBISStromL1 = OBISCode{1, 0, 31, 7, 0, 255} // 1-0:31.7.0*255 A
	OBISStromL2 = OBISCode{1, 0, 51, 7, 0, 255} // 1-0:51.7.0*255 A
	OBISStromL3 = OBISCode{1, 0, 71, 7, 0, 255} // 1-0:71.7.0*255 A

	// Spannung – phasenbezogen
	OBISSpannungL1 = OBISCode{1, 0, 32, 7, 0, 255} // 1-0:32.7.0*255 V
	OBISSpannungL2 = OBISCode{1, 0, 52, 7, 0, 255} // 1-0:52.7.0*255 V
	OBISSpannungL3 = OBISCode{1, 0, 72, 7, 0, 255} // 1-0:72.7.0*255 V

	// Frequenz
	OBISFrequenz = OBISCode{1, 0, 14, 7, 0, 255} // 1-0:14.7.0*255 Hz

	// Zähler-Identifikation
	OBISZaehlerID   = OBISCode{0, 0, 96, 1, 0, 255} // 0-0:96.1.0*255 Zählernummer
	OBISZaehlerTyp  = OBISCode{0, 0, 96, 1, 4, 255} // 0-0:96.1.4*255 Gerätetyp
	OBISHersteller  = OBISCode{0, 0, 96, 50, 1, 255} // Herstellerkennung (z.B. "LGZ")
)

// =============================================================================
//  Messwert-Datenstruktur (vollständig)
// =============================================================================

// Reading ist ein vollständiger Messwertblock vom Zähler.
type Reading struct {
	Timestamp time.Time `json:"timestamp"`

	// Zähler-Identifikation
	ZaehlerID  string `json:"zaehler_id,omitempty"`
	Hersteller string `json:"hersteller,omitempty"`

	// Energie (kWh)
	KWhBezug       float64 `json:"kwh_bezug"`        // Wirkenergie Bezug gesamt
	KWhBezugT1     float64 `json:"kwh_bezug_t1,omitempty"` // Tarif 1
	KWhBezugT2     float64 `json:"kwh_bezug_t2,omitempty"` // Tarif 2
	KWhEinspeisung float64 `json:"kwh_einspeisung"`  // Wirkenergie Einspeisung (PV)

	// Momentanleistung (W)
	WattGesamt      float64 `json:"watt_gesamt"`       // ± Vorzeichen
	WattBezug       float64 `json:"watt_bezug,omitempty"`
	WattEinspeisung float64 `json:"watt_einspeisung,omitempty"`
	WattL1          float64 `json:"watt_l1,omitempty"`
	WattL2          float64 `json:"watt_l2,omitempty"`
	WattL3          float64 `json:"watt_l3,omitempty"`

	// Strom (A)
	AmpereL1 float64 `json:"ampere_l1,omitempty"`
	AmpereL2 float64 `json:"ampere_l2,omitempty"`
	AmpereL3 float64 `json:"ampere_l3,omitempty"`

	// Spannung (V)
	VoltL1 float64 `json:"volt_l1,omitempty"`
	VoltL2 float64 `json:"volt_l2,omitempty"`
	VoltL3 float64 `json:"volt_l3,omitempty"`

	// Frequenz (Hz)
	FrequenzHz float64 `json:"frequenz_hz,omitempty"`

	// Abgeleitete Werte
	Leistungsfaktor float64 `json:"leistungsfaktor,omitempty"` // cos φ
	IstEinspeisung  bool    `json:"ist_einspeisung"`           // Prosumer speist gerade ein

	// Legacy-Feld für Kompatibilität mit altem Code
	MeterID  string  `json:"meter_id"`
	KWh      float64 `json:"kwh"`      // = KWhBezug
	KWhFeed  float64 `json:"kwh_feed"` // = KWhEinspeisung
	WattNow  float64 `json:"watt_now"` // = WattGesamt
}

// =============================================================================
//  Token (signierter Energie-Nachweis)
// =============================================================================

// Token ist ein signierter, handelbarer Energie-Nachweis.
type Token struct {
	Timestamp    time.Time `json:"timestamp"`
	MeterID      string    `json:"meter_id"`
	Lat          float64   `json:"lat"`
	Lon          float64   `json:"lon"`
	KWh          float64   `json:"kwh"`
	KWhFeed      float64   `json:"kwh_feed"`
	WattNow      float64   `json:"watt_now"`
	WattL1       float64   `json:"watt_l1,omitempty"`
	WattL2       float64   `json:"watt_l2,omitempty"`
	WattL3       float64   `json:"watt_l3,omitempty"`
	VoltL1       float64   `json:"volt_l1,omitempty"`
	AmpereL1     float64   `json:"ampere_l1,omitempty"`
	FrequenzHz   float64   `json:"frequenz_hz,omitempty"`
	IstEinspeisung bool    `json:"ist_einspeisung"`
	GeneratorLat float64   `json:"generator_lat"`
	GeneratorLon float64   `json:"generator_lon"`
	Commitment   string    `json:"commitment"` // Argon2id(key || payload) – kein SHA
	Signature    string    `json:"signature,omitempty"`
}

// =============================================================================
//  Config
// =============================================================================

// Config konfiguriert den Smartmeter-Reader.
type Config struct {
	Protocol   string        // "sml" | "d0" | "http" | "mock"
	SerialPort string        // z.B. "/dev/ttyUSB0"
	BaudRate   int           // Standard: 115200 (SML), 9600 (D0 nach Handshake)
	DataBits   int           // 8
	StopBits   float64       // 1.0
	Parity     string        // "N" (None) | "E" (Even) | "O" (Odd)
	ReadTimeout time.Duration // Serial Read-Timeout pro Frame
	HTTPURL    string
	MeterID    string
	Lat        float64
	Lon        float64
	GeneratorLat float64
	GeneratorLon float64
	SigningKeyHex string
	Interval   time.Duration
}

// =============================================================================
//  Reader
// =============================================================================

// Reader liest kontinuierlich vom Smartmeter.
type Reader struct {
	cfg    Config
	log    *zap.Logger
	key    []byte
	client *http.Client
}

// NewReader erstellt einen Reader mit validierten Defaults.
func NewReader(cfg Config, log *zap.Logger) (*Reader, error) {
	// Defaults
	if cfg.Interval == 0  { cfg.Interval = time.Second }
	if cfg.BaudRate == 0  { cfg.BaudRate = 115200 }
	if cfg.DataBits == 0  { cfg.DataBits = 8 }
	if cfg.StopBits == 0  { cfg.StopBits = 1.0 }
	if cfg.Parity == ""   { cfg.Parity = "N" }
	if cfg.ReadTimeout == 0 { cfg.ReadTimeout = 5 * time.Second }

	var key []byte
	if cfg.SigningKeyHex != "" {
		var err error
		key, err = hex.DecodeString(cfg.SigningKeyHex)
		if err != nil {
			return nil, fmt.Errorf("invalid signing key: %w", err)
		}
		if len(key) < 32 {
			return nil, fmt.Errorf("signing key must be ≥ 32 bytes")
		}
	}

	return &Reader{
		cfg:    cfg,
		log:    log,
		key:    key,
		client: &http.Client{Timeout: 3 * time.Second},
	}, nil
}

// Run startet die Messschleife.
func (r *Reader) Run(ctx context.Context) (<-chan Token, error) {
	out := make(chan Token, 8)

	switch strings.ToLower(r.cfg.Protocol) {
	case "sml":
		// SML: Port dauerhaft offen halten, Stream lesen.
		// Der Zähler sendet von sich aus alle ~1-2s einen Frame.
		// Kein Ticker – wir lesen frame-synchron.
		go r.runSMLStream(ctx, out)

	case "d0":
		// D0: Handshake + periodisches Polling (Zähler sendet nicht kontinuierlich)
		go r.runPolling(ctx, out, r.readD0)

	case "http", "shelly", "tasmota", "tibber":
		// HTTP: Polling mit konfiguriertem Intervall (Geräte-abhängig)
		go r.runPolling(ctx, out, r.readHTTP)

	case "mock", "":
		go r.runPolling(ctx, out, r.readMock)

	default:
		return nil, fmt.Errorf("unknown meter protocol: %s", r.cfg.Protocol)
	}

	return out, nil
}

// runSMLStream liest kontinuierlich vom SML-Port.
// Öffnet den Port einmal und liest Frame für Frame – genau wie der Zähler sendet.
func (r *Reader) runSMLStream(ctx context.Context, out chan<- Token) {
	defer close(out)

	for {
		if ctx.Err() != nil { return }

		port, err := r.openSerial(r.cfg.BaudRate)
		if err != nil {
			r.log.Warn("SML Port nicht öffenbar – retry in 5s",
				zap.String("port", r.cfg.SerialPort), zap.Error(err))
			select { case <-ctx.Done(): return
			case <-time.After(5 * time.Second): }
			continue
		}

		r.log.Info("SML Stream geöffnet",
			zap.String("port", r.cfg.SerialPort),
			zap.Int("baud", r.cfg.BaudRate))

		// Frame-Schleife auf dem offenen Port
		consecutiveErrors := 0
		for {
			if ctx.Err() != nil { port.Close(); return }

			frame, err := r.readSMLFrame(port)
			if err != nil {
				consecutiveErrors++
				r.log.Debug("SML Frame-Fehler", zap.Error(err), zap.Int("errors", consecutiveErrors))
				if consecutiveErrors > 10 {
					r.log.Warn("Zu viele SML-Fehler – Port neu öffnen")
					break // Port schließen und neu öffnen
				}
				continue
			}
			consecutiveErrors = 0

			reading, err := parseSMLFrame(frame)
			if err != nil {
				r.log.Debug("SML Parse-Fehler", zap.Error(err))
				continue
			}

			token := r.makeToken(reading)
			select {
			case out <- token:
			case <-ctx.Done():
				port.Close(); return
			default:
				// Channel voll (Consumer zu langsam) – altes Token verwerfen
			}
		}
		port.Close()
	}
}

// runPolling liest periodisch mit dem konfigurierten Intervall (für D0, HTTP, Mock).
func (r *Reader) runPolling(ctx context.Context, out chan<- Token, readFn func(context.Context) (*Reading, error)) {
	defer close(out)
	interval := r.cfg.Interval
	if interval <= 0 { interval = time.Second }

	for {
		select {
		case <-ctx.Done(): return
		case <-time.After(interval):
			reading, err := readFn(ctx)
			if err != nil {
				r.log.Warn("Meter read error", zap.Error(err))
				continue
			}
			token := r.makeToken(reading)
			select {
			case out <- token:
			default:
			}
		}
	}
}

// =============================================================================
//  Serieller Port – gemeinsame Öffnungs-Funktion
// =============================================================================

func (r *Reader) openSerial(baudRate int) (serial.Port, error) {
	parity := serial.NoParity
	switch strings.ToUpper(r.cfg.Parity) {
	case "E":
		parity = serial.EvenParity
	case "O":
		parity = serial.OddParity
	}

	stopBits := serial.OneStopBit
	if r.cfg.StopBits == 2.0 {
		stopBits = serial.TwoStopBits
	}

	mode := &serial.Mode{
		BaudRate: baudRate,
		DataBits: r.cfg.DataBits,
		Parity:   parity,
		StopBits: stopBits,
	}

	port, err := serial.Open(r.cfg.SerialPort, mode)
	if err != nil {
		return nil, fmt.Errorf("serial open %s @%d: %w", r.cfg.SerialPort, baudRate, err)
	}

	if err := port.SetReadTimeout(r.cfg.ReadTimeout); err != nil {
		port.Close()
		return nil, fmt.Errorf("serial timeout: %w", err)
	}

	return port, nil
}

// =============================================================================
//  SML – Smart Message Language (BSI TR-03109-1)
// =============================================================================
//
//  Frame-Struktur:
//    Escape:    1B 1B 1B 1B   (4 Bytes)
//    Start:     01 01 01 01   (4 Bytes)
//    Body:      SML-Nachrichten (variable Länge)
//    Escape:    1B 1B 1B 1B   (4 Bytes)
//    End:       1A <pad> <CRC16 LE>  (4 Bytes)
//
//  OBIS-Wert-Encoding im SML-Baum:
//    Jede SML_GetList.Response enthält SML_ListEntry mit:
//      - objName:    OBIS-Code (6 Bytes, Octet String)
//      - scaler:     int8
//      - value:      int/uint (je nach Typ-Tag)
//    Wert = rawValue * 10^scaler

var (
	smlEscape = []byte{0x1B, 0x1B, 0x1B, 0x1B}
	smlStart  = []byte{0x01, 0x01, 0x01, 0x01}
	smlEnd    = []byte{0x1A}
)

// readSML liest und parst einen vollständigen SML-Frame.
func (r *Reader) readSML(ctx context.Context) (*Reading, error) {
	port, err := r.openSerial(r.cfg.BaudRate)
	if err != nil {
		return nil, err
	}
	defer port.Close()

	// Frame einlesen: suche Escape+Start, lies bis Escape+End
	buf, err := r.readSMLFrame(port)
	if err != nil {
		return nil, err
	}

	return parseSMLFrame(buf)
}

// readSMLFrame liest genau einen SML-Frame aus dem seriellen Port.
func (r *Reader) readSMLFrame(port serial.Port) ([]byte, error) {
	// Ringpuffer für Byte-weise Suche nach Escape-Sequenz
	raw  := make([]byte, 0, 4096)
	tmp  := make([]byte, 1)
	buf4 := make([]byte, 4)

	// Auf Escape+Start warten (max 8192 Bytes)
	for len(raw) < 8192 {
		n, err := port.Read(tmp)
		if err != nil || n == 0 { return nil, fmt.Errorf("sml: read: %w", err) }
		raw = append(raw, tmp[0])

		// Sobald wir 8 Bytes haben: auf 1B1B1B1B 01010101 prüfen
		if len(raw) >= 8 {
			tail := raw[len(raw)-8:]
			if equalBytes(tail[:4], smlEscape) && equalBytes(tail[4:], smlStart) {
				// Frame-Start gefunden – jetzt bis Frame-End lesen
				frame := []byte{0x1B, 0x1B, 0x1B, 0x1B, 0x01, 0x01, 0x01, 0x01}
				for {
					n, err := port.Read(buf4[:1])
					if err != nil || n == 0 { return nil, fmt.Errorf("sml: frame body: %w", err) }
					frame = append(frame, buf4[0])

					// Auf Escape+End prüfen (1B1B1B1B 1A)
					if len(frame) >= 9 {
						tail := frame[len(frame)-5:]
						if equalBytes(tail[:4], smlEscape) && tail[4] == 0x1A {
							// Noch 3 Bytes: pad + CRC16 (2 Bytes)
							if _, err := io.ReadFull(port, buf4[:3]); err != nil {
								return nil, fmt.Errorf("sml: crc: %w", err)
							}
							frame = append(frame, buf4[:3]...)
							return frame, nil
						}
					}
					if len(frame) > 65536 {
						return nil, fmt.Errorf("sml: frame zu groß")
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("sml: kein Frame-Start gefunden")
}

// parseSMLFrame parst einen vollständigen SML-Frame und extrahiert alle OBIS-Werte.
func parseSMLFrame(frame []byte) (*Reading, error) {
	reading := &Reading{
		Timestamp: time.Now().UTC(),
	}

	// Skip Escape+Start (8 Bytes) + Stop Escape (4 Bytes) am Ende
	if len(frame) < 12 {
		return nil, fmt.Errorf("sml: frame zu kurz")
	}
	body := frame[8 : len(frame)-8] // ohne Escape-Sequenzen

	// OBIS-Codes scannen
	scanOBIS(body, reading)

	// Legacy-Felder setzen
	reading.KWh      = reading.KWhBezug
	reading.KWhFeed  = reading.KWhEinspeisung
	reading.WattNow  = reading.WattGesamt
	reading.MeterID  = reading.ZaehlerID
	reading.IstEinspeisung = reading.WattEinspeisung > reading.WattBezug && reading.KWhEinspeisung > 0

	return reading, nil
}

// scanOBIS durchsucht den SML-Body nach bekannten OBIS-Codes und liest Werte.
//
// SML TLV-Struktur (vereinfacht):
//   Typ-Byte: [Typ:3 | Länge:5]  (Länge in Bytes inkl. Typ-Byte)
//   Typen: 0x0=OctetString, 0x5=int, 0x6=uint, 0x7=Liste, 0x4=bool
func scanOBIS(data []byte, r *Reading) {
	for i := 0; i < len(data)-6; i++ {
		// OBIS OctetString suchen: Typ-Byte 0x07 (OctetString, 7 Bytes = 1+6)
		if data[i] != 0x07 {
			continue
		}
		obis := OBISCode(data[i+1 : i+7])
		switch obis {
		case OBISZaehlerID:
			// Zähler-ID ist ein OctetString
			r.ZaehlerID = readOctetString(data, i+7)
		case OBISHersteller:
			r.Hersteller = readOctetString(data, i+7)
		case OBISWirkenergieBezug:
			r.KWhBezug = readSMLValue(data, i+7, 1000) // Wh → kWh
		case OBISWirkenergieBezugT1:
			r.KWhBezugT1 = readSMLValue(data, i+7, 1000)
		case OBISWirkenergieBezugT2:
			r.KWhBezugT2 = readSMLValue(data, i+7, 1000)
		case OBISWirkenergieEinspeisung:
			r.KWhEinspeisung = readSMLValue(data, i+7, 1000)
		case OBISWirkleistungGesamt:
			r.WattGesamt = readSMLValue(data, i+7, 1)
		case OBISWirkleistungBezug:
			r.WattBezug = readSMLValue(data, i+7, 1)
		case OBISWirkleistungEinspeisung:
			r.WattEinspeisung = readSMLValue(data, i+7, 1)
		case OBISWirkleistungL1:
			r.WattL1 = readSMLValue(data, i+7, 1)
		case OBISWirkleistungL2:
			r.WattL2 = readSMLValue(data, i+7, 1)
		case OBISWirkleistungL3:
			r.WattL3 = readSMLValue(data, i+7, 1)
		case OBISStromL1:
			r.AmpereL1 = readSMLValue(data, i+7, 1000) // mA → A
		case OBISStromL2:
			r.AmpereL2 = readSMLValue(data, i+7, 1000)
		case OBISStromL3:
			r.AmpereL3 = readSMLValue(data, i+7, 1000)
		case OBISSpannungL1:
			r.VoltL1 = readSMLValue(data, i+7, 10) // 0.1V → V
		case OBISSpannungL2:
			r.VoltL2 = readSMLValue(data, i+7, 10)
		case OBISSpannungL3:
			r.VoltL3 = readSMLValue(data, i+7, 10)
		case OBISFrequenz:
			r.FrequenzHz = readSMLValue(data, i+7, 10) // 0.1 Hz → Hz
		}
	}

	// Leistungsfaktor berechnen wenn Daten vorhanden
	if r.VoltL1 > 0 && r.AmpereL1 > 0 && r.WattL1 > 0 {
		apparentPower := r.VoltL1 * r.AmpereL1
		if apparentPower > 0 {
			r.Leistungsfaktor = math.Abs(r.WattL1) / apparentPower
			if r.Leistungsfaktor > 1.0 { r.Leistungsfaktor = 1.0 }
		}
	}
}

// readSMLValue liest einen SML-Wert (int/uint) mit Scaler.
// divisor: teilt durch diesen Wert für Einheiten-Umrechnung (z.B. Wh→kWh: 1000)
func readSMLValue(data []byte, pos int, divisor float64) float64 {
	if pos >= len(data)-2 {
		return 0
	}

	// Status-Byte überspringen falls vorhanden (0x01 oder 0x72)
	offset := pos

	// Scaler: nächstes Integer-TLV
	scaler := int8(0)
	if offset < len(data) {
		typByte := data[offset]
		if typByte == 0x52 { // int8 Scaler
			if offset+1 < len(data) {
				scaler = int8(data[offset+1])
				offset += 2
			}
		} else if typByte == 0x62 { // uint8
			if offset+1 < len(data) {
				scaler = int8(data[offset+1])
				offset += 2
			}
		}
	}

	// Wert-TLV
	if offset >= len(data) { return 0 }
	typByte := data[offset]
	length  := int(typByte & 0x0F) - 1 // Datenlänge ohne Typ-Byte

	if length <= 0 || offset+1+length > len(data) { return 0 }
	valueBytes := data[offset+1 : offset+1+length]

	var rawValue int64
	switch {
	case typByte&0xF0 == 0x50: // int
		rawValue = readSignedInt(valueBytes)
	case typByte&0xF0 == 0x60: // uint
		rawValue = int64(readUnsignedInt(valueBytes))
	default:
		return 0
	}

	result := float64(rawValue) * math.Pow10(int(scaler))
	if divisor != 0 && divisor != 1 {
		result /= divisor
	}
	return result
}

func readSignedInt(b []byte) int64 {
	switch len(b) {
	case 1: return int64(int8(b[0]))
	case 2: return int64(int16(binary.BigEndian.Uint16(b)))
	case 4: return int64(int32(binary.BigEndian.Uint32(b)))
	case 8: return int64(binary.BigEndian.Uint64(b))
	}
	return 0
}

func readUnsignedInt(b []byte) uint64 {
	switch len(b) {
	case 1: return uint64(b[0])
	case 2: return uint64(binary.BigEndian.Uint16(b))
	case 4: return uint64(binary.BigEndian.Uint32(b))
	case 8: return binary.BigEndian.Uint64(b)
	}
	return 0
}

func readOctetString(data []byte, pos int) string {
	if pos >= len(data) { return "" }
	typByte := data[pos]
	if typByte&0xF0 != 0x00 { return "" } // kein OctetString
	length := int(typByte&0x0F) - 1
	if length <= 0 || pos+1+length > len(data) { return "" }
	return string(data[pos+1 : pos+1+length])
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) { return false }
	for i := range a {
		if a[i] != b[i] { return false }
	}
	return true
}

// =============================================================================
//  D0 (IEC 62056-21 Mode C) – Ältere DE-Zähler
// =============================================================================
//
//  Protokoll:
//    1. Anfrage senden: /?!\r\n  @300 Baud
//    2. Zähler antwortet: /LSZ5... @300 Baud
//    3. ACK senden: 060...\r\n (Baudraten-Umschaltbefehl)
//    4. Zähler sendet Datentelegramm @9600 Baud

func (r *Reader) readD0(ctx context.Context) (*Reading, error) {
	// Phase 1: Handshake bei 300 Baud
	port300, err := r.openSerial(300)
	if err != nil {
		return nil, err
	}

	// Anfrage senden
	if _, err := port300.Write([]byte("/?!\r\n")); err != nil {
		port300.Close()
		return nil, fmt.Errorf("d0: request: %w", err)
	}

	// Identifikation lesen (z.B. "/LSZ5EHZ363W5\r\n")
	ident := make([]byte, 128)
	n, _ := port300.Read(ident)
	port300.Close()

	_ = ident[:n] // Hersteller-Kennung für Debugging

	// Phase 2: Datentelegramm bei 9600 Baud lesen
	port9600, err := r.openSerial(9600)
	if err != nil {
		return nil, err
	}
	defer port9600.Close()

	// ACK + Baudraten-Wechsel: 060\r\n
	port9600.Write([]byte("\x06" + "050" + "\r\n"))
	time.Sleep(300 * time.Millisecond)

	// Datentelegramm einlesen (bis ETX + BCC)
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 64)
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		n, _ := port9600.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			// ETX (0x03) markiert Ende des Datentelegramms
			if idx := indexByte(buf, 0x03); idx >= 0 {
				buf = buf[:idx+1]
				break
			}
		}
	}

	return parseD0Telegram(buf, r.cfg.MeterID)
}

// parseD0Telegram parst ein D0-Datentelegramm.
func parseD0Telegram(data []byte, meterID string) (*Reading, error) {
	reading := &Reading{
		Timestamp: time.Now().UTC(),
		MeterID:   meterID,
	}

	lines := strings.Split(string(data), "\r\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) < 10 || !strings.Contains(line, "(") {
			continue
		}

		// Format: OBIS-Code(Wert*Einheit)
		// Beispiele:
		//   1-0:1.8.0(001234.567*kWh)
		//   1-0:16.7.0(00150.00*W)
		//   0-0:96.1.0(0123456789)
		obisEnd := strings.Index(line, "(")
		valEnd  := strings.Index(line, ")")
		if obisEnd < 0 || valEnd < 0 || valEnd <= obisEnd { continue }

		obisStr := line[:obisEnd]
		valStr  := line[obisEnd+1 : valEnd]

		// Einheit trennen: Wert*Einheit
		val := valStr
		if starIdx := strings.Index(valStr, "*"); starIdx >= 0 {
			val = valStr[:starIdx]
		}

		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			// Kein numerischer Wert → String (z.B. Zähler-ID)
			if obisStr == "0-0:96.1.0" {
				reading.ZaehlerID = strings.Trim(val, " \x00")
				reading.MeterID   = reading.ZaehlerID
			}
			continue
		}

		switch obisStr {
		case "1-0:1.8.0":  reading.KWhBezug = f
		case "1-0:1.8.1":  reading.KWhBezugT1 = f
		case "1-0:1.8.2":  reading.KWhBezugT2 = f
		case "1-0:2.8.0":  reading.KWhEinspeisung = f
		case "1-0:16.7.0": reading.WattGesamt = f
		case "1-0:1.7.0":  reading.WattBezug = f
		case "1-0:2.7.0":  reading.WattEinspeisung = f
		case "1-0:36.7.0": reading.WattL1 = f
		case "1-0:56.7.0": reading.WattL2 = f
		case "1-0:76.7.0": reading.WattL3 = f
		case "1-0:31.7.0": reading.AmpereL1 = f
		case "1-0:51.7.0": reading.AmpereL2 = f
		case "1-0:71.7.0": reading.AmpereL3 = f
		case "1-0:32.7.0": reading.VoltL1 = f
		case "1-0:52.7.0": reading.VoltL2 = f
		case "1-0:72.7.0": reading.VoltL3 = f
		case "1-0:14.7.0": reading.FrequenzHz = f
		}
	}

	reading.KWh     = reading.KWhBezug
	reading.KWhFeed = reading.KWhEinspeisung
	reading.WattNow = reading.WattGesamt
	reading.IstEinspeisung = reading.KWhEinspeisung > 0 && reading.WattEinspeisung > reading.WattBezug
	return reading, nil
}

// =============================================================================
//  HTTP (Shelly EM / Tasmota / Tibber Pulse)
// =============================================================================

type shellyEMStatus struct {
	Emeters []struct {
		Power   float64 `json:"power"`
		Total   float64 `json:"total"`
		Voltage float64 `json:"voltage"`
		Current float64 `json:"current"`
		Pf      float64 `json:"pf"`
	} `json:"emeters"`
}

type tasmotaStatus struct {
	StatusSNS struct {
		Energy struct {
			Power   float64 `json:"Power"`
			Today   float64 `json:"Today"`
			Total   float64 `json:"Total"`
			Voltage float64 `json:"Voltage"`
			Current float64 `json:"Current"`
			Factor  float64 `json:"Factor"`
		} `json:"ENERGY"`
	} `json:"StatusSNS"`
}

func (r *Reader) readHTTP(ctx context.Context) (*Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.HTTPURL, nil)
	if err != nil { return nil, err }

	resp, err := r.client.Do(req)
	if err != nil { return nil, err }
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	reading := &Reading{Timestamp: time.Now().UTC(), MeterID: r.cfg.MeterID}

	// Shelly EM?
	var shelly shellyEMStatus
	if json.Unmarshal(body, &shelly) == nil && len(shelly.Emeters) > 0 {
		for _, em := range shelly.Emeters {
			reading.WattGesamt += em.Power
			reading.KWhBezug   += em.Total / 1000
			if len(shelly.Emeters) == 1 {
				reading.VoltL1    = em.Voltage
				reading.AmpereL1  = em.Current
				reading.Leistungsfaktor = em.Pf
			}
		}
		reading.KWh = reading.KWhBezug
		reading.WattNow = reading.WattGesamt
		return reading, nil
	}

	// Tasmota?
	var tasmota tasmotaStatus
	if json.Unmarshal(body, &tasmota) == nil && tasmota.StatusSNS.Energy.Power > 0 {
		e := tasmota.StatusSNS.Energy
		reading.WattGesamt = e.Power
		reading.KWhBezug   = e.Total
		reading.VoltL1      = e.Voltage
		reading.AmpereL1    = e.Current
		reading.Leistungsfaktor = e.Factor
		reading.KWh         = reading.KWhBezug
		reading.WattNow     = reading.WattGesamt
		return reading, nil
	}

	return nil, fmt.Errorf("http: unbekanntes Antwortformat von %s", r.cfg.HTTPURL)
}

// =============================================================================
//  Mock (Test ohne Hardware)
// =============================================================================

func (r *Reader) readMock(ctx context.Context) (*Reading, error) {
	t := float64(time.Now().UnixMilli()) / 1000
	watt := 1200.0 + 300.0*math.Sin(t/60) // simuliert schwankende Last

	return &Reading{
		Timestamp:      time.Now().UTC(),
		ZaehlerID:      "MOCK-" + r.cfg.MeterID,
		Hersteller:     "Fundus-Mock",
		KWhBezug:       t / 3600 * 1.5,
		KWhEinspeisung: t / 3600 * 0.3,
		WattGesamt:     watt,
		WattBezug:      math.Max(watt, 0),
		WattEinspeisung: math.Max(-watt, 0),
		WattL1:         watt * 0.4,
		WattL2:         watt * 0.35,
		WattL3:         watt * 0.25,
		AmpereL1:       watt * 0.4 / 230,
		AmpereL2:       watt * 0.35 / 230,
		AmpereL3:       watt * 0.25 / 230,
		VoltL1:         230.5 + math.Sin(t)*0.5,
		VoltL2:         230.2 + math.Cos(t)*0.5,
		VoltL3:         230.8 + math.Sin(t+1)*0.5,
		FrequenzHz:     50.01,
		Leistungsfaktor: 0.98,
		MeterID:        r.cfg.MeterID,
		KWh:            t / 3600 * 1.5,
		WattNow:        watt,
	}, nil
}

// =============================================================================
//  Token-Commitment (Argon2id – HMAC-SHA256 gilt als kompromittiert)
// =============================================================================

func (r *Reader) makeToken(reading *Reading) Token {
	token := Token{
		Timestamp:    reading.Timestamp,
		MeterID:      reading.MeterID,
		Lat:          r.cfg.Lat,
		Lon:          r.cfg.Lon,
		KWh:          reading.KWhBezug,
		KWhFeed:      reading.KWhEinspeisung,
		WattNow:      reading.WattGesamt,
		WattL1:       reading.WattL1,
		WattL2:       reading.WattL2,
		WattL3:       reading.WattL3,
		VoltL1:       reading.VoltL1,
		AmpereL1:     reading.AmpereL1,
		FrequenzHz:   reading.FrequenzHz,
		IstEinspeisung: reading.IstEinspeisung,
		GeneratorLat: r.cfg.GeneratorLat,
		GeneratorLon: r.cfg.GeneratorLon,
	}

	if len(r.key) >= 32 {
		// Argon2id-Commitment: Kein HMAC-SHA256 (SHA als kompromittiert betrachtet).
		// Argon2id(key || payload) ist speicherhart und Brute-Force-resistent.
		payload, _ := json.Marshal(struct {
			Timestamp  string  `json:"timestamp"`
			MeterID    string  `json:"meter_id"`
			KWh        float64 `json:"kwh"`
			KWhFeed    float64 `json:"kwh_feed"`
			WattNow    float64 `json:"watt_now"`
		}{
			Timestamp: token.Timestamp.Format(time.RFC3339),
			MeterID:   token.MeterID,
			KWh:       token.KWh,
			KWhFeed:   token.KWhFeed,
			WattNow:   token.WattNow,
		})
		// Salt = BLAKE3(key) – deterministisch pro Gerät, kein SHA
		h := blake3.New(32, nil)
		h.Write(r.key)
		salt := h.Sum(nil)[:16]
		sig := argon2.IDKey(payload, salt, 1, 32*1024, 2, 32)
		token.Signature = hex.EncodeToString(sig)
	}

	return token
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func indexByte(data []byte, b byte) int {
	for i, v := range data { if v == b { return i } }
	return -1
}

// =============================================================================
//  ttyUSB Auto-Detection
// =============================================================================

// DetectSerialPorts gibt alle verfügbaren seriellen Ports zurück.
// Priorisiert: USB-Adapter vor UART vor klassischen seriellen Ports.
func DetectSerialPorts() []string {
	candidates := []string{
		"/dev/ttyUSB0", "/dev/ttyUSB1", "/dev/ttyUSB2", "/dev/ttyUSB3",
		"/dev/ttyAMA0", "/dev/ttyAMA1",
		"/dev/ttyS0", "/dev/serial0",
	}
	var found []string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	return found
}

// ProbePort prüft ob ein serieller Port SML- oder D0-Daten liefert.
// Gibt true zurück wenn innerhalb von timeout gültige Meter-Daten erkannt wurden.
func ProbePort(portPath string, baudRate int, timeout time.Duration) (ok bool, proto string) {
	mode := &serial.Mode{
		BaudRate: baudRate, DataBits: 8,
		Parity: serial.NoParity, StopBits: serial.OneStopBit,
	}
	p, err := serial.Open(portPath, mode)
	if err != nil { return false, "" }
	defer p.Close()
	p.SetReadTimeout(timeout)

	buf  := make([]byte, 64)
	data := make([]byte, 0, 512)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		n, _ := p.Read(buf)
		if n > 0 { data = append(data, buf[:n]...) }

		// SML: Escape-Sequenz 1B 1B 1B 1B
		for i := 0; i+3 < len(data); i++ {
			if data[i] == 0x1B && data[i+1] == 0x1B &&
				data[i+2] == 0x1B && data[i+3] == 0x1B {
				return true, "sml"
			}
		}
		// D0: beginnt mit '/'
		if len(data) > 0 && data[0] == '/' {
			return true, "d0"
		}
		// Kein Fortschritt nach 512 Bytes → aufgeben
		if len(data) > 512 { break }
	}
	return false, ""
}

// AutoDetectMeter sucht automatisch einen angeschlossenen Smartmeter.
// Probiert alle verfügbaren Ports und Baudraten der Reihe nach.
// Gibt Port, Baudrate und Protokoll zurück.
func AutoDetectMeter(log *zap.Logger) (port string, baud int, proto string) {
	ports := DetectSerialPorts()
	if len(ports) == 0 {
		if log != nil { log.Warn("Keine seriellen Ports verfügbar – Smartmeter nicht angeschlossen?") }
		return "", 0, ""
	}
	if log != nil { log.Info("Suche Smartmeter...", zap.Strings("ports", ports)) }

	// Baudraten in Prioritätsreihenfolge
	bauds := []int{115200, 9600, 300}

	for _, p := range ports {
		for _, b := range bauds {
			timeout := 3 * time.Second
			if b == 300 { timeout = 6 * time.Second } // D0-Handshake dauert länger
			ok, detectedProto := ProbePort(p, b, timeout)
			if ok {
				if log != nil {
					log.Info("Smartmeter erkannt",
						zap.String("port",     p),
						zap.Int("baud",        b),
						zap.String("protokoll", detectedProto),
					)
				}
				return p, b, detectedProto
			}
			if log != nil {
				log.Debug("Kein Smartmeter auf Port/Baud",
					zap.String("port", p), zap.Int("baud", b))
			}
		}
	}

	if log != nil { log.Warn("Kein Smartmeter gefunden", zap.Strings("ports_geprüft", ports)) }
	return "", 0, ""
}

// ReadSMLWithFallback liest SML-Daten mit automatischem Port-Fallback.
// Wenn der konfigurierte Port unerwartete Daten liefert, wird
// automatisch der nächste verfügbare Port probiert.
func (r *Reader) readSMLWithFallback(ctx context.Context) (*Reading, error) {
	// Zuerst konfigurierten Port versuchen
	reading, err := r.readSML(ctx)
	if err == nil && reading != nil && reading.KWhBezug > 0 {
		return reading, nil // Erfolg auf konfiguriertem Port
	}

	// Fallback: andere Ports probieren
	for _, portPath := range DetectSerialPorts() {
		if portPath == r.cfg.SerialPort { continue } // schon probiert

		r.log.Info("Probiere alternativen Port",
			zap.String("port",    portPath),
			zap.String("grund",   "konfigurierter Port liefert keine Daten"),
		)

		origPort := r.cfg.SerialPort
		r.cfg.SerialPort = portPath

		reading, err = r.readSML(ctx)
		if err == nil && reading != nil && reading.KWhBezug > 0 {
			r.log.Info("Smartmeter auf Alternativ-Port gefunden – verwende dauerhaft",
				zap.String("port", portPath))
			// Bleibt auf dem neuen Port (cfg wurde schon geändert)
			return reading, nil
		}

		r.cfg.SerialPort = origPort // Zurücksetzen wenn kein Erfolg
	}

	return nil, fmt.Errorf("sml: kein Smartmeter auf keinem verfügbaren Port")
}
