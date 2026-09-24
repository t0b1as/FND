package topology

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// =============================================================================
//  Netzwerk-Graph
// =============================================================================

// Graph hält den bekannten Ausschnitt der Netzwerktopologie.
// Wird aus DHT-Daten und GossipSub-Ankündigungen aufgebaut.
// Thread-safe.
type Graph struct {
	mu    sync.RWMutex
	nodes map[string]*NodeProfile // peerID → Profil
}

// NewGraph erstellt einen leeren Topologie-Graphen.
func NewGraph() *Graph {
	return &Graph{nodes: make(map[string]*NodeProfile)}
}

// AddOrUpdate fügt einen Node hinzu oder aktualisiert ihn.
func (g *Graph) AddOrUpdate(p *NodeProfile) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if existing, ok := g.nodes[p.PeerID]; ok {
		// Nur aktualisieren wenn neuer Timestamp
		if p.UpdatedAt.Before(existing.UpdatedAt) {
			return
		}
	}
	g.nodes[p.PeerID] = p
}

// Get gibt ein Node-Profil zurück.
func (g *Graph) Get(peerID string) (*NodeProfile, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	p, ok := g.nodes[peerID]
	return p, ok
}

// Substations gibt alle bekannten Trafostationen zurück.
func (g *Graph) Substations() []*NodeProfile {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]*NodeProfile, 0)
	for _, p := range g.nodes {
		if p.Type == NodeTypeSubstation {
			result = append(result, p)
		}
	}
	return result
}

// NodeCount gibt die Anzahl bekannter Nodes zurück.
func (g *Graph) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

// =============================================================================
//  Pfadfindung (Dijkstra auf dem Netzgraph)
// =============================================================================

// FindRoute berechnet den optimalen Handelspfad von from nach to.
//
// Algorithmus:
//  1. Wenn beide Nodes an der GLEICHEN Trafostation → DirectLV
//  2. Wenn Trafostationen im gleichen MV-Netz → LocalMV
//  3. Sonst: Dijkstra auf dem Graph → RegionalHV oder LongDistance
//  4. Fallback wenn Graph unvollständig → PLZ-Luftlinie + Schätzgebühr
func (g *Graph) FindRoute(fromPeerID, toPeerID string, priceAmount float64) (*GridRoute, error) {
	g.mu.RLock()
	fromNode, fromOK := g.nodes[fromPeerID]
	toNode, toOK     := g.nodes[toPeerID]
	g.mu.RUnlock()

	// Fallback: Nodes unbekannt
	if !fromOK || !toOK {
		return g.fallbackRoute(fromPeerID, toPeerID, priceAmount), nil
	}

	// ── Regel 1: Gleiche Trafostation ────────────────────────────────────────
	if fromNode.ParentSubstationID != "" &&
		fromNode.ParentSubstationID == toNode.ParentSubstationID {

		sub, _ := g.Get(fromNode.ParentSubstationID)
		return g.directLVRoute(fromNode, toNode, sub, priceAmount), nil
	}

	// ── Regel 2 & 3: Dijkstra ────────────────────────────────────────────────
	path, dist := g.dijkstra(fromPeerID, toPeerID)
	if path == nil {
		return g.fallbackRoute(fromPeerID, toPeerID, priceAmount), nil
	}

	return g.buildRoute(path, dist, fromNode, toNode, priceAmount), nil
}

// directLVRoute erstellt eine Direkthandels-Route (gleiche Trafostation).
func (g *Graph) directLVRoute(from, to *NodeProfile, sub *NodeProfile, price float64) *GridRoute {
	hops := []RouteHop{
		{PeerID: from.PeerID, WalletAddress: from.WalletAddress,
			NodeType: from.Type, Voltage: from.Voltage},
	}

	var subFees []SubstationFee
	subFeePercent := 0.0
	if sub != nil {
		subFeePercent = sub.TransitFeePercent
		hops = append(hops, RouteHop{
			PeerID:        sub.PeerID,
			WalletAddress: sub.WalletAddress,
			NodeType:      NodeTypeSubstation,
			Voltage:       sub.Voltage,
			FeePercent:    sub.TransitFeePercent,
		})
		subFees = []SubstationFee{{
			PeerID:        sub.PeerID,
			WalletAddress: sub.WalletAddress,
			FeePercent:    sub.TransitFeePercent,
		}}
	}
	hops = append(hops, RouteHop{
		PeerID: to.PeerID, WalletAddress: to.WalletAddress,
		NodeType: to.Type, Voltage: to.Voltage,
	})

	totalFee := subFeePercent
	return &GridRoute{
		FromPeerID: from.PeerID,
		ToPeerID:   to.PeerID,
		Hops:       hops,
		TradeMode:  TradeModeDirectLV,
		Fees: RouteFees{
			SubstationFees:     subFees,
			TotalFeePercent:    totalFee,
			TotalFeeAmount:     price * totalFee / 100,
			NetAmount:          price * (1 - totalFee/100),
		},
	}
}

// buildRoute erstellt eine Route aus einem Dijkstra-Pfad.
func (g *Graph) buildRoute(path []string, totalDistM float64, from, to *NodeProfile, price float64) *GridRoute {
	g.mu.RLock()
	defer g.mu.RUnlock()

	hops   := make([]RouteHop, 0, len(path))
	subFees := make([]SubstationFee, 0)
	totalSubFee := 0.0
	mode   := TradeModeUnknown

	for i, pid := range path {
		node, ok := g.nodes[pid]
		if !ok {
			hops = append(hops, RouteHop{PeerID: pid})
			continue
		}

		hop := RouteHop{
			PeerID:        node.PeerID,
			WalletAddress: node.WalletAddress,
			NodeType:      node.Type,
			Voltage:       node.Voltage,
			FeePercent:    0,
		}

		if i > 0 && node.Type == NodeTypeSubstation {
			hop.FeePercent = node.TransitFeePercent
			totalSubFee  += node.TransitFeePercent
			subFees = append(subFees, SubstationFee{
				PeerID:        node.PeerID,
				WalletAddress: node.WalletAddress,
				FeePercent:    node.TransitFeePercent,
			})

			// Trade-Mode aus höchster Spannungsebene ableiten
			switch node.Voltage {
			case VoltageEHV:
				mode = TradeModeLongDistance
			case VoltageHV:
				if mode != TradeModeLongDistance { mode = TradeModeRegionalHV }
			case VoltageMV:
				if mode == TradeModeUnknown { mode = TradeModeLocalMV }
			case VoltageLV:
				if mode == TradeModeUnknown { mode = TradeModeDirectLV }
			}
		}
		hops = append(hops, hop)
	}

	if mode == TradeModeUnknown { mode = TradeModeRegionalHV }

	totalFee := totalSubFee
	if totalFee > 25.0 { totalFee = 25.0 } // Cap

	return &GridRoute{
		FromPeerID:     from.PeerID,
		ToPeerID:       to.PeerID,
		Hops:           hops,
		TotalDistanceM: totalDistM,
		TradeMode:      mode,
		Fees: RouteFees{
			SubstationFees:  subFees,
			TotalFeePercent: totalFee,
			TotalFeeAmount:  price * totalFee / 100,
			NetAmount:       price * (1 - totalFee/100),
		},
	}
}

// =============================================================================
//  Dijkstra (Distanz in Metern als Gewicht)
// =============================================================================

type dijkstraItem struct {
	peerID string
	dist   float64
	index  int
}

type pq []*dijkstraItem

func (h pq) Len() int            { return len(h) }
func (h pq) Less(i, j int) bool  { return h[i].dist < h[j].dist }
func (h pq) Swap(i, j int)       { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *pq) Push(x interface{}) { item := x.(*dijkstraItem); item.index = len(*h); *h = append(*h, item) }
func (h *pq) Pop() interface{}   { old := *h; n := len(old); item := old[n-1]; *h = old[:n-1]; return item }

// dijkstra findet den kürzesten Pfad (nach Distanz in Metern).
func (g *Graph) dijkstra(fromID, toID string) (path []string, totalDist float64) {
	dist := map[string]float64{fromID: 0}
	prev := map[string]string{}

	h := &pq{{peerID: fromID, dist: 0}}
	heap.Init(h)

	for h.Len() > 0 {
		curr := heap.Pop(h).(*dijkstraItem)
		if curr.peerID == toID {
			break
		}
		node, ok := g.nodes[curr.peerID]
		if !ok { continue }

		for _, conn := range node.Connections {
			newDist := curr.dist + conn.DistanceM
			if d, seen := dist[conn.PeerID]; !seen || newDist < d {
				dist[conn.PeerID] = newDist
				prev[conn.PeerID] = curr.peerID
				heap.Push(h, &dijkstraItem{peerID: conn.PeerID, dist: newDist})
			}
		}
	}

	if _, reached := dist[toID]; !reached {
		return nil, 0
	}

	// Pfad rekonstruieren
	for p := toID; p != ""; p = prev[p] {
		path = append([]string{p}, path...)
	}
	return path, dist[toID]
}

// fallbackRoute erstellt eine Schätzroute wenn Topologie unbekannt.
// Basiert auf Luftlinie × 1.3 und Standard-Gebührsätzen.
func (g *Graph) fallbackRoute(fromID, toID string, price float64) *GridRoute {
	return &GridRoute{
		FromPeerID: fromID,
		ToPeerID:   toID,
		TradeMode:  TradeModeUnknown,
		Hops:       []RouteHop{{PeerID: fromID}, {PeerID: toID}},
		Fees: RouteFees{
			TotalFeePercent: 5.0, // Standardgebühr 5% wenn unbekannt
			TotalFeeAmount:  price * 0.05,
			NetAmount:       price * 0.95,
		},
	}
}

// =============================================================================
//  Distanzberechnung: Google Maps Routing oder Luftlinie-Fallback
// =============================================================================

// DistanceCalculator berechnet Routing-Distanzen zwischen zwei Punkten.
type DistanceCalculator struct {
	gmapsAPIKey string
	httpClient  *http.Client
}

// NewDistanceCalculator erstellt einen Distanz-Rechner.
// gmapsAPIKey: Google Maps Distance Matrix API Key (leer = Luftlinie-Fallback)
func NewDistanceCalculator(gmapsAPIKey string) *DistanceCalculator {
	return &DistanceCalculator{
		gmapsAPIKey: gmapsAPIKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// CalculateDistance berechnet die Routing-Distanz in Metern.
// Primär: Google Maps Distance Matrix API (Straßenrouting).
// Fallback: Luftlinie × 1.3 (typischer Korrekturfaktor).
func (d *DistanceCalculator) CalculateDistance(
	ctx context.Context,
	fromLat, fromLon, toLat, toLon float64,
) (distanceM float64, source string, err error) {

	if d.gmapsAPIKey != "" {
		distanceM, err = d.gmapsDistance(ctx, fromLat, fromLon, toLat, toLon)
		if err == nil {
			return distanceM, "gmaps", nil
		}
		// Fallthrough zu Luftlinie bei API-Fehler
	}

	// Luftlinie × 1.3 (Kabel folgen Straßen, typisch 30% länger)
	airlineM := haversineM(fromLat, fromLon, toLat, toLon)
	return airlineM * 1.3, "haversine_130pct", nil
}

// gmapsDistance ruft die Google Maps Distance Matrix API auf.
func (d *DistanceCalculator) gmapsDistance(
	ctx context.Context,
	fromLat, fromLon, toLat, toLon float64,
) (float64, error) {

	apiURL := fmt.Sprintf(
		"https://maps.googleapis.com/maps/api/distancematrix/json"+
			"?origins=%.6f,%.6f&destinations=%.6f,%.6f&mode=driving&key=%s",
		fromLat, fromLon, toLat, toLon,
		url.QueryEscape(d.gmapsAPIKey),
	)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil { return 0, err }

	resp, err := d.httpClient.Do(req)
	if err != nil { return 0, err }
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
		Rows   []struct {
			Elements []struct {
				Status   string `json:"status"`
				Distance struct {
					Value int `json:"value"` // Meter
				} `json:"distance"`
			} `json:"elements"`
		} `json:"rows"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	if result.Status != "OK" {
		return 0, fmt.Errorf("gmaps: status %s", result.Status)
	}
	if len(result.Rows) == 0 || len(result.Rows[0].Elements) == 0 {
		return 0, fmt.Errorf("gmaps: keine Ergebnisse")
	}
	elem := result.Rows[0].Elements[0]
	if elem.Status != "OK" {
		return 0, fmt.Errorf("gmaps: element status %s", elem.Status)
	}

	return float64(elem.Distance.Value), nil
}

// haversineM berechnet die Luftliniendistanz in Metern.
func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
