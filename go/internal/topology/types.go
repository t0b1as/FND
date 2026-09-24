// Package topology implementiert die Netzwerkgraph-Topologie für den Fundus-Energiemarktplatz.
//
// Konzept:
//
//	Jeder Fundus-Node hat ein "Profil" das beschreibt welche Rolle er im Stromnetz spielt.
//	Trafostationen (Substations) sind eigene Nodes mit eigenem Wallet.
//	Der Handelspfad zwischen zwei Nodes folgt der physischen Netzinfrastruktur:
//
//	  LV-Consumer A ──┐                     ┌── LV-Consumer B
//	                  ├── Trafo ONT-1        Trafo ONT-2 ──┤
//	                  │   (LV→MV)            (LV→MV)       │
//	                  └── MV-Netz ─── Trafo UW ────────────┘
//	                                  (MV→HV)
//
//	Handelsregeln:
//	  - Gleiche Trafostation (ONT):  Direkthandel, minimale Gebühr
//	  - Gleiches MV-Netz:           Route über 1 Trafo, LV+MV-Gebühr
//	  - Verschiedene MV-Netze:      Route über HV, vollständige Gebührkette
//
// Google Maps Routing:
//	  Jeder Node speichert seine Verbindungen zu benachbarten Nodes mit
//	  der Straßendistanz (GMaps) als Proxy für die Kabellänge.
//	  Alternativ: Luftlinie × 1.3 Korrekturfaktor (Defaultwert).
package topology

import (
	"time"
)

// =============================================================================
//  Node-Profile-Typen
// =============================================================================

// NodeType klassifiziert einen Fundus-Node nach seiner Rolle im Energienetz.
type NodeType string

const (
	// Consumer: reiner Verbraucher (Haushalt, Gewerbe ohne Erzeugung)
	NodeTypeConsumer NodeType = "consumer"

	// Prosumer: Verbraucher MIT Eigenerzeugung (Solar, BHKW, Wärmepumpe+PV)
	NodeTypeProsumer NodeType = "prosumer"

	// Substation: Trafostation (ONT LV→MV, Umspannwerk MV→HV, ÜNB HV→EHV)
	// Hat eigenes Wallet – kassiert Durchleitungsgebühr
	NodeTypeSubstation NodeType = "substation"

	// PowerPlant: reiner Erzeuger (Windpark, PV-Großanlage, Biogas)
	NodeTypePowerPlant NodeType = "power_plant"
)

// VoltageLevel beschreibt die Spannungsebene eines Netzpunkts.
type VoltageLevel string

const (
	VoltageLV  VoltageLevel = "lv"  // Niederspannung  230/400 V
	VoltageMV  VoltageLevel = "mv"  // Mittelspannung   10-30 kV
	VoltageHV  VoltageLevel = "hv"  // Hochspannung   110-220 kV
	VoltageEHV VoltageLevel = "ehv" // Höchstspannung 380/220 kV (ÜNB)
)

// =============================================================================
//  Node-Profil
// =============================================================================

// NodeProfile beschreibt die Identität und Netzposition eines Fundus-Nodes.
// Wird per GossipSub im Netz bekannt gemacht und im DHT gespeichert.
type NodeProfile struct {
	// Eindeutige Peer-ID (libp2p)
	PeerID string `json:"peer_id"`

	// Wallet-Adresse dieses Nodes (FND-Empfang, Trafostationen haben eigene)
	WalletAddress string `json:"wallet_address"`

	// Profil-Typ
	Type    NodeType     `json:"type"`
	Voltage VoltageLevel `json:"voltage"`

	// Geografische Position
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
	PLZ string  `json:"plz,omitempty"`

	// Trafostation-Referenz: an welcher Trafo ist dieser Node angeschlossen?
	// Für Consumer/Prosumer: ID der übergeordneten Trafostation (ONT)
	// Für Substation: ID der übergeordneten Trafostation (z.B. Umspannwerk)
	// Für ÜNB-Nodes: leer
	ParentSubstationID string `json:"parent_substation_id,omitempty"`

	// Direkte Verbindungen zu anderen Nodes (Nachbarn im Netzgraph)
	Connections []Connection `json:"connections"`

	// Netzbetreiber-ID (BDEW/DVGW-Code)
	OperatorID string `json:"operator_id,omitempty"`

	// Maximale Ein-/Ausspeiseleistung in Watt (0 = unbekannt)
	MaxFeedInW  float64 `json:"max_feed_in_w,omitempty"`
	MaxConsumeW float64 `json:"max_consume_w,omitempty"`

	// Gebühren-Konfiguration (nur Substation-Nodes)
	// Basis-Gebühr in % die dieser Knoten für Durchleitung berechnet
	TransitFeePercent float64 `json:"transit_fee_percent,omitempty"`

	// Zeitstempel der letzten Aktualisierung
	UpdatedAt time.Time `json:"updated_at"`

	// Signatur des Node-Betreibers (Ed25519 über JSON-Payload ohne Signature)
	Signature string `json:"signature,omitempty"`
}

// Connection beschreibt eine physische Verbindung zu einem Nachbar-Node.
type Connection struct {
	// Peer-ID des Nachbars
	PeerID string `json:"peer_id"`

	// Distanz in Metern
	// Primär: Google Maps Routing-Distanz (Straße als Proxy für Kabel)
	// Fallback: Luftlinie × 1.3 (typischer Korrekturfaktor für Kabelverlegung)
	DistanceM float64 `json:"distance_m"`

	// Wie die Distanz ermittelt wurde
	DistanceSource string `json:"distance_source"` // "gmaps" | "haversine_130pct" | "manual"

	// Verbindungskapazität in Watt (0 = unbekannt)
	CapacityW float64 `json:"capacity_w,omitempty"`

	// Spannungsebene der Verbindung
	Voltage VoltageLevel `json:"voltage"`
}

// =============================================================================
//  Routing-Ergebnis
// =============================================================================

// GridRoute beschreibt den optimalen Handelspfad zwischen zwei Nodes.
type GridRoute struct {
	FromPeerID string `json:"from_peer_id"`
	ToPeerID   string `json:"to_peer_id"`

	// Geordnete Liste der Nodes auf dem Pfad (inkl. Start und Ziel)
	Hops []RouteHop `json:"hops"`

	// Gesamtdistanz in Metern
	TotalDistanceM float64 `json:"total_distance_m"`

	// Handelsmodus
	TradeMode TradeMode `json:"trade_mode"`

	// Gebühren
	Fees RouteFees `json:"fees"`
}

// RouteHop ist ein einzelner Knoten auf dem Handelspfad.
type RouteHop struct {
	PeerID        string       `json:"peer_id"`
	WalletAddress string       `json:"wallet_address"`
	NodeType      NodeType     `json:"node_type"`
	Voltage       VoltageLevel `json:"voltage"`
	DistFromPrevM float64      `json:"dist_from_prev_m"`
	FeePercent    float64      `json:"fee_percent"` // Gebühr dieses Knotens
}

// TradeMode beschreibt wie Energie physisch übertragen wird.
type TradeMode string

const (
	// DirectTrade: Käufer und Verkäufer hängen an der SELBEN Trafostation.
	// Minimale Gebühr, keine HV/MV-Durchleitung nötig.
	TradeModeDirectLV TradeMode = "direct_lv"

	// LocalMV: Selbes MV-Netz, Route über eine Trafo-Ebene.
	TradeModeLocalMV TradeMode = "local_mv"

	// RegionalHV: Verschiedene MV-Netze, Route über HV.
	TradeModeRegionalHV TradeMode = "regional_hv"

	// LongDistance: Über ÜNB-Netz (Höchstspannung).
	TradeModeLongDistance TradeMode = "long_distance"

	// Unknown: Topologie unbekannt, Fallback auf PLZ-Luftlinie.
	TradeModeUnknown TradeMode = "unknown"
)

// RouteFees enthält die aufgeschlüsselten Gebühren entlang des Pfads.
type RouteFees struct {
	// Gebühr pro Trafo-Hop (Durchleitungsgebühr der Trafostation-Wallets)
	SubstationFees []SubstationFee `json:"substation_fees"`

	// Netzbetreiber-Gebühr (BDEW/DVGW-Betreiber des Endkundennetzes)
	OperatorFeePercent float64 `json:"operator_fee_percent"`

	// Gesamt-Gebühr in Prozent des Transaktionswerts
	TotalFeePercent float64 `json:"total_fee_percent"`

	// Gesamt-Gebühr in FND (bei gegebenem Preis)
	TotalFeeAmount float64 `json:"total_fee_amount,omitempty"`

	// Nettobetrag nach allen Gebühren
	NetAmount float64 `json:"net_amount,omitempty"`
}

// SubstationFee ist die Gebühr einer einzelnen Trafostation.
type SubstationFee struct {
	PeerID        string  `json:"peer_id"`
	WalletAddress string  `json:"wallet_address"`
	FeePercent    float64 `json:"fee_percent"`
	DistanceM     float64 `json:"distance_m"`
}
