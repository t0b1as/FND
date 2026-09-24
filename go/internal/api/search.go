package api

// Netzwerkweite Suche per Such-Ping.
//
// Modell: "Beides kombiniert"
//   1. Sucher ruft GET /api/v1/search auf.
//   2. Server durchsucht SOFORT den lokalen Bestand → direkte Treffer.
//   3. Server broadcastet die Query ins fundus.search-Topic (GossipSub),
//      optional auf eine Geohash-Region eingegrenzt.
//   4. Andere Nodes durchsuchen ihren lokalen Bestand und schicken Treffer
//      per direktem Stream (SearchResultProtocol) an den Sucher zurück.
//   5. Sucher sammelt eingehende Treffer in einem Zeitfenster; das Frontend
//      pollt GET /api/v1/search/results?id=<id> und ergaenzt die Liste live.
//
// Skalierung: Der Geohash-Praefix in der Query begrenzt, welche Nodes
// ueberhaupt antworten — so flutet nicht jede Suche das ganze Netz.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"lukechampine.com/blake3"

	"github.com/fundus/node/internal/geo"
	"github.com/fundus/node/internal/grid"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// SearchQuery ist die ueber GossipSub verteilte Suchanfrage.
// Suchergebnis-Limits: schützen Antwortgröße, Bandbreite und CPU. Greifen an
// drei Stellen, damit das Problem nicht nur verschoben wird:
//   - searchLocalMax:  max. lokale Treffer pro Node und Suche (früher Abbruch)
//   - searchRespondMax: max. Treffer, die ein Node anderen zurückschickt
//   - searchCollectMax: max. Gesamttreffer, die ein Sucher einsammelt
const (
	searchLocalMax   = 100
	searchRespondMax = 50
	searchCollectMax = 300
)

type SearchQuery struct {
	SearchID     string  `json:"search_id"`     // eindeutige ID dieser Suche
	RequesterID  string  `json:"requester_id"`  // Peer-ID des Suchers (Rueckkanal)
	Q            string  `json:"q"`             // Volltext
	Category     string  `json:"category"`      // optionale Kategorie
	GeohashPfx   string  `json:"geohash_pfx"`   // optionale Regions-Eingrenzung
	Lat          float64 `json:"lat"`           // Zentrum fuer Distanz
	Lon          float64 `json:"lon"`
	RadiusKm     float64 `json:"radius_km"`
	TTL          int     `json:"ttl"`           // Hop-Limit (aktuell informativ)
	CreatedAt    int64   `json:"created_at"`    // Unix-Sekunden (gegen alte Pings)
}

// SearchHit ist ein einzelner Treffer-Stub (klein gehalten – Details werden
// erst beim Oeffnen ueber die Listing-ID nachgeladen).
type SearchHit struct {
	ID         string  `json:"id"`
	OwnerID    string  `json:"owner_id"`
	Title      string  `json:"title"`
	Category   string  `json:"category"`
	Condition  string  `json:"condition"`
	PriceMin   float64 `json:"price_min"`
	Thumbnail  string  `json:"thumbnail,omitempty"`   // erstes Thumbnail (Base64)
	ImageHash  string  `json:"image_hash,omitempty"`  // erster Vollbild-Hash
	Lat        float64 `json:"lat,omitempty"`
	Lon        float64 `json:"lon,omitempty"`
	DistanceKm float64 `json:"distance_km,omitempty"`
	FromPeer   string  `json:"from_peer,omitempty"`
	Sold       bool    `json:"sold,omitempty"` // true = bereits verkauft (ausgegraut)
}

// SearchResultMsg ist die Antwort eines Nodes auf einen Such-Ping (Rueckkanal).
type SearchResultMsg struct {
	SearchID string      `json:"search_id"`
	Hits     []SearchHit `json:"hits"`
}

// laufende Suchen: SearchID → gesammelte Treffer
type searchSession struct {
	hits      []SearchHit
	seen      map[string]bool // dedupe per Listing-ID
	createdAt time.Time
}

type searchManager struct {
	mu       sync.Mutex
	sessions map[string]*searchSession
}

func newSearchManager() *searchManager {
	return &searchManager{sessions: map[string]*searchSession{}}
}

// add fuegt Treffer zu einer Session hinzu (dedupe per ID, last-write je ID).
func (m *searchManager) add(searchID string, hits []SearchHit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[searchID]
	if sess == nil {
		sess = &searchSession{seen: map[string]bool{}, createdAt: time.Now()}
		m.sessions[searchID] = sess
	}
	for _, h := range hits {
		if len(sess.hits) >= searchCollectMax {
			break // Gesamt-Sammlung gedeckelt → Sucher nicht überlasten
		}
		if h.ID == "" || sess.seen[h.ID] {
			continue
		}
		sess.seen[h.ID] = true
		sess.hits = append(sess.hits, h)
	}
}

// get liefert die bisher gesammelten Treffer einer Session.
func (m *searchManager) get(searchID string) []SearchHit {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[searchID]; sess != nil {
		out := make([]SearchHit, len(sess.hits))
		copy(out, sess.hits)
		return out
	}
	return nil
}

// gc entfernt Sessions die aelter als 2 Minuten sind.
func (m *searchManager) gc() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sess := range m.sessions {
		if time.Since(sess.createdAt) > 2*time.Minute {
			delete(m.sessions, id)
		}
	}
}

// =============================================================================
//  Lokale Suche (auf dem eigenen Bestand) – von API und Ping-Responder genutzt
// =============================================================================

// searchLocal durchsucht den lokalen Listing-Bestand und gibt Treffer-Stubs
// zurueck. Wird sowohl fuer die sofortige Antwort des Suchers als auch von
// Nodes verwendet die einen eingehenden Ping beantworten.
func (s *Server) searchLocal(q SearchQuery) []SearchHit {
	qLower := strings.ToLower(strings.TrimSpace(q.Q))
	catLower := strings.ToLower(strings.TrimSpace(q.Category))

	// Kandidaten über den invertierten Index beschaffen (statt alle Records zu
	// scannen). Drei Fälle:
	//   1. Volltext-Query ≥3 Zeichen → Trigramm-Index (Kandidaten = Obermenge).
	//   2. nur Kategorie, keine/kurze Query → Kategorie-Index.
	//   3. kurze Query (<3) ohne Kategorie → linearer Fallback (selten, breit).
	var records []*storage.Record
	switch {
	case len([]rune(qLower)) >= 3:
		ids, ok := s.store.SearchCandidates(storage.RecordListing, qLower)
		if !ok {
			records = s.listAll(storage.RecordListing)
		} else {
			records = s.loadByIDs(storage.RecordListing, ids)
		}
	case catLower != "":
		ids, ok := s.store.CategoryCandidates(storage.RecordListing, catLower)
		if !ok {
			records = s.listAll(storage.RecordListing)
		} else {
			records = s.loadByIDs(storage.RecordListing, ids)
		}
	default:
		records = s.listAll(storage.RecordListing)
	}

	var hits []SearchHit
	for _, r := range records {
		d := r.Data
		if d == nil {
			continue
		}
		// Geohash-Region: wenn gesetzt, nur passende Angebote
		if q.GeohashPfx != "" {
			gh := getStr(d, "geohash")
			if gh == "" || !strings.HasPrefix(gh, q.GeohashPfx) {
				continue
			}
		}
		// Volltext-VERIFIKATION: Trigramm-Kandidaten sind eine Obermenge, daher
		// hier der echte Substring-Check — identisches Ergebnis zur linearen Suche.
		if qLower != "" {
			hay := strings.ToLower(getStr(d, "title") + " " +
				getStr(d, "description") + " " + getStr(d, "listing_text") + " " +
				keywordsToString(d["keywords"]))
			if !strings.Contains(hay, qLower) {
				continue
			}
		}
		// Kategorie
		if catLower != "" && strings.ToLower(getStr(d, "category")) != catLower {
			continue
		}

		hit := SearchHit{
			ID:        r.ID,
			OwnerID:   r.OwnerID,
			Title:     firstNonEmpty(getStr(d, "title"), getStr(d, "listing_text")),
			Category:  getStr(d, "category"),
			Condition: getStr(d, "condition"),
		}
		// Verkauft-Status: lokale Markierung ODER aus der Chain abgeleitet
		// (Escrow für den Content-Hash existiert) — so erscheint VERKAUFT auf
		// ALLEN Pis, nicht nur beim Käufer.
		if sold, ok := d["sold"].(bool); ok && sold {
			hit.Sold = true
		} else if s.isListingSoldOnChain(d) {
			hit.Sold = true
		}
		if p, ok := toFloatOK(d["price_min"]); ok {
			hit.PriceMin = p
		}
		if imgs, ok := d["images"].([]any); ok && len(imgs) > 0 {
			if s0, ok := imgs[0].(string); ok {
				hit.Thumbnail = s0
			}
		}
		if hashes, ok := d["image_hashes"].([]any); ok && len(hashes) > 0 {
			if h0, ok := hashes[0].(string); ok {
				hit.ImageHash = h0
			}
		}
		if lat, ok := toFloatOK(d["lat"]); ok {
			if lon, ok2 := toFloatOK(d["lon"]); ok2 {
				hit.Lat, hit.Lon = lat, lon
				if q.Lat != 0 || q.Lon != 0 {
					dist := geo.HaversineKm(q.Lat, q.Lon, lat, lon)
					if q.RadiusKm > 0 && dist > q.RadiusKm {
						continue
					}
					hit.DistanceKm = round1(dist)
				}
			}
		}
		hits = append(hits, hit)
	}
	// Erst ALLE Treffer sammeln (der Index lieferte die vollständige Kandidaten-
	// menge), dann sinnvoll sortieren und ERST DANN deckeln — so bekommt man bei
	// Überlauf die relevantesten Treffer statt willkürlicher. Bei Distanz-Suche
	// nach Nähe, sonst stabil nach Preis dann ID.
	sortSearchHits(hits, q.Lat != 0 || q.Lon != 0)
	if len(hits) > searchLocalMax {
		hits = hits[:searchLocalMax]
	}
	return hits
}

// sortSearchHits sortiert Treffer: bei Geo-Suche nach Distanz (nächste zuerst),
// sonst nach Preis aufsteigend; Gleichstand nach ID (stabil/deterministisch).
func sortSearchHits(hits []SearchHit, byDistance bool) {
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if byDistance && a.DistanceKm != b.DistanceKm {
			return a.DistanceKm < b.DistanceKm
		}
		if a.PriceMin != b.PriceMin {
			return a.PriceMin < b.PriceMin
		}
		return a.ID < b.ID
	})
}

// listAll lädt alle Records eines Typs (Fallback, wenn kein Index greift).
func (s *Server) listAll(rt storage.RecordType) []*storage.Record {
	recs, err := s.store.List(rt)
	if err != nil {
		return nil
	}
	return recs
}

// loadByIDs lädt die Records zu einer Kandidaten-ID-Menge (überspringt fehlende/
// gelöschte). Lädt nur die Kandidaten statt des gesamten Bestands.
func (s *Server) loadByIDs(rt storage.RecordType, ids map[string]struct{}) []*storage.Record {
	out := make([]*storage.Record, 0, len(ids))
	for id := range ids {
		rec, err := s.store.Get(rt, id)
		if err != nil || rec == nil || rec.DeletedAt != nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// =============================================================================
//  Ping-Responder: beantwortet eingehende Such-Broadcasts
// =============================================================================

// registerSearchResponder abonniert das Such-Topic und beantwortet Pings mit
// lokalen Treffern (per direktem Stream an den Sucher).
func (s *Server) registerSearchResponder() {
	if s.node == nil {
		return
	}
	s.node.SetTopicHandler(p2p.TopicSearch, func(data []byte) {
		var q SearchQuery
		if err := json.Unmarshal(data, &q); err != nil {
			return
		}
		// Eigene Pings ignorieren
		if q.RequesterID == s.nodeID() {
			return
		}
		// Zu alte Pings ignorieren (>60s)
		if q.CreatedAt > 0 && time.Now().Unix()-q.CreatedAt > 60 {
			return
		}
		hits := s.searchLocal(q)
		if len(hits) == 0 {
			return // nichts Passendes → nicht antworten (spart Traffic)
		}
		// Antwort deckeln: ein einzelner Node soll den Sucher nicht fluten.
		if len(hits) > searchRespondMax {
			hits = hits[:searchRespondMax]
		}
		for i := range hits {
			hits[i].FromPeer = s.nodeID()
		}
		resp := SearchResultMsg{SearchID: q.SearchID, Hits: hits}
		payload, err := json.Marshal(resp)
		if err != nil {
			return
		}
		// Treffer per direktem Stream an den Sucher zurueck
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.node.SendAndReceive(ctx, q.RequesterID,
			p2p.SearchResultProtocol, payload); err != nil {
			s.log.Debug("Such-Antwort konnte nicht gesendet werden",
				zap.String("to", q.RequesterID), zap.Error(err))
		}
	})

	// Rueckkanal: eingehende Treffer anderer Nodes einsammeln
	s.node.RegisterProtocol(p2p.SearchResultProtocol,
		func(peerID string, data []byte) []byte {
			var msg SearchResultMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				return []byte("ERR")
			}
			s.searchMgr.add(msg.SearchID, msg.Hits)
			return []byte("OK")
		})
}

// =============================================================================
//  HTTP-Routen
// =============================================================================

func (s *Server) registerSearchRoutes() {
	g := s.router.Group("/api/v1/search")
	g.GET("",         s.startSearch)
	g.GET("/results", s.searchResults)
}

// startSearch: lokale Treffer sofort + Broadcast ins Netz.
// GET /api/v1/search?q=...&category=...&plz=...&radius_km=...
func (s *Server) startSearch(c *gin.Context) {
	q := SearchQuery{
		SearchID:    generateID(),
		Q:           c.Query("q"),
		Category:    c.Query("category"),
		RadiusKm:    parseFloatDefault(c.Query("radius_km"), 0),
		TTL:         3,
		CreatedAt:   time.Now().Unix(),
	}
	if s.node != nil {
		q.RequesterID = s.nodeID()
	}
	// PLZ → Zentrum + Geohash-Region
	if plz := strings.TrimSpace(c.Query("plz")); plz != "" {
		lat, lon := grid.PLZCentroid(plz)
		q.Lat, q.Lon = lat, lon
		prec := geo.PrecisionForRadiusKm(q.RadiusKm)
		q.GeohashPfx = geo.GeohashEncode(lat, lon, prec)
	}
	// Fallback: keine Such-PLZ → die eigene, im Admin gesetzte Position als
	// Referenz für die Distanzberechnung. So erscheint die Entfernung auch ohne
	// PLZ-Eingabe in der Suchleiste.
	if q.Lat == 0 && q.Lon == 0 && s.locationStore != nil {
		if own := s.locationStore.get(); own.hasPosition() {
			q.Lat, q.Lon = own.Lat, own.Lon
		}
	}

	// 1. Lokale Treffer sofort
	local := s.searchLocal(q)
	for i := range local {
		local[i].FromPeer = "local"
	}
	s.searchMgr.add(q.SearchID, local)

	// 2. Broadcast ins Netz (asynchron, Antworten kommen ueber den Rueckkanal)
	if s.node != nil {
		if payload, err := json.Marshal(q); err == nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel()
			if err := s.node.Publish(ctx, p2p.TopicSearch, payload); err != nil {
				s.log.Debug("Such-Broadcast fehlgeschlagen", zap.Error(err))
			}
		}
	}
	s.searchMgr.gc()

	c.JSON(http.StatusOK, gin.H{
		"search_id": q.SearchID,
		"hits":      s.searchMgr.get(q.SearchID),
		"note":      "Lokale Treffer sofort; Netzwerk-Treffer per /search/results nachladen.",
	})
}

// searchResults: aktuell gesammelte Treffer einer laufenden Suche.
// GET /api/v1/search/results?id=<search_id>
func (s *Server) searchResults(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id fehlt"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"search_id": id,
		"hits":      s.searchMgr.get(id),
	})
}

// --- kleine Helfer ---
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

// isListingSoldOnChain leitet den Verkauft-Status aus der Chain ab: gibt es einen
// Escrow für den Content-Hash dieses Listings? So sehen alle Nodes den Status,
// unabhängig davon, wer den Kauf ausgelöst hat.
func (s *Server) isListingSoldOnChain(d map[string]any) bool {
	if s.chain == nil {
		return false
	}
	// Content-Hash: gespeichert oder on-the-fly berechnen (gleiche Formel wie
	// createListing / escrowCreate).
	chHex, _ := d["content_hash"].(string)
	if chHex == "" {
		hstr := fmt.Sprintf("%v|%v|%v|%v|%v",
			d["title"], d["description"], d["category"], d["condition"], d["image_hashes"])
		sum := blake3.Sum256([]byte(hstr))
		chHex = "0x" + hex.EncodeToString(sum[:])
	}
	raw, err := hex.DecodeString(trimHexPrefix(chHex))
	if err != nil || len(raw) != 32 {
		return false
	}
	var ch [32]byte
	copy(ch[:], raw)
	return s.chain.HasEscrowForContent(ch)
}
