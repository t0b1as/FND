package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/geo"
	"github.com/fundus/node/internal/grid"
	"github.com/fundus/node/internal/p2p"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ─── Netzweite Job-Suche (Gebote = Stellenangebote, Gesuche = Stellengesuche) ─
//
// Spiegelt den Markt-Suchpfad (search.go): lokale Treffer sofort, Netz-Treffer
// per GossipSub-Broadcast (TopicJobSearch) + direktem Rückkanal. Zusätzlich
// filterbar nach Job-Typ ("offer" = Gebot, "request" = Gesuch). Dieselben
// Ergebnis-Limits wie die Markt-Suche (searchLocalMax/RespondMax/CollectMax).

// JobSearchQuery ist eine Job-Such-Anfrage (lokal + im Netz).
type JobSearchQuery struct {
	SearchID    string  `json:"search_id"`
	RequesterID string  `json:"requester_id"`
	Q           string  `json:"q"`
	JobType     string  `json:"job_type"` // "", "offer" (Gebot), "request" (Gesuch)
	GeohashPfx  string  `json:"geohash_pfx,omitempty"`
	Lat         float64 `json:"lat,omitempty"`
	Lon         float64 `json:"lon,omitempty"`
	RadiusKm    float64 `json:"radius_km,omitempty"`
	CreatedAt   int64   `json:"created_at"`
}

// JobSearchHit ist ein Job-Treffer (Stub für die Liste).
type JobSearchHit struct {
	ID         string  `json:"id"`
	OwnerID    string  `json:"owner_id"`
	Title      string  `json:"title"`
	JobType    string  `json:"job_type"` // "offer" | "request"
	Category   string  `json:"category,omitempty"`
	Location   string  `json:"location,omitempty"`
	SalaryMin  float64 `json:"salary_min,omitempty"`
	Lat        float64 `json:"lat,omitempty"`
	Lon        float64 `json:"lon,omitempty"`
	DistanceKm float64 `json:"distance_km,omitempty"`
	FromPeer   string  `json:"from_peer,omitempty"`
}

// JobSearchResultMsg ist die Antwort eines Nodes (Rückkanal).
type JobSearchResultMsg struct {
	SearchID string         `json:"search_id"`
	Hits     []JobSearchHit `json:"hits"`
}

type jobSearchSession struct {
	hits      []JobSearchHit
	seen      map[string]bool
	createdAt time.Time
}

type jobSearchManager struct {
	mu       sync.Mutex
	sessions map[string]*jobSearchSession
}

func newJobSearchManager() *jobSearchManager {
	return &jobSearchManager{sessions: map[string]*jobSearchSession{}}
}

func (m *jobSearchManager) add(searchID string, hits []JobSearchHit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[searchID]
	if sess == nil {
		sess = &jobSearchSession{seen: map[string]bool{}, createdAt: time.Now()}
		m.sessions[searchID] = sess
	}
	for _, h := range hits {
		if len(sess.hits) >= searchCollectMax {
			break // gleiches Gesamt-Limit wie Markt-/Datei-Suche
		}
		if h.ID == "" || sess.seen[h.ID] {
			continue
		}
		sess.seen[h.ID] = true
		sess.hits = append(sess.hits, h)
	}
}

func (m *jobSearchManager) get(searchID string) []JobSearchHit {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[searchID]; sess != nil {
		out := make([]JobSearchHit, len(sess.hits))
		copy(out, sess.hits)
		return out
	}
	return nil
}

func (m *jobSearchManager) gc() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sess := range m.sessions {
		if time.Since(sess.createdAt) > 2*time.Minute {
			delete(m.sessions, id)
		}
	}
}

// jobSearchLocal durchsucht den lokalen Job-Bestand (Gebote + Gesuche).
func (s *Server) jobSearchLocal(q JobSearchQuery) []JobSearchHit {
	qLower := strings.ToLower(strings.TrimSpace(q.Q))
	typeFilter := strings.ToLower(strings.TrimSpace(q.JobType))

	// Kandidaten über den Trigramm-Index (≥3 Zeichen), sonst linearer Fallback.
	// Der type-Filter (offer/request) bleibt Postfilter — kein eigener Index nötig,
	// da er nur zwei Werte hat.
	var records []*storage.Record
	if len([]rune(qLower)) >= 3 {
		if ids, ok := s.store.SearchCandidates(storage.RecordJob, qLower); ok {
			records = s.loadByIDs(storage.RecordJob, ids)
		} else {
			records = s.listAll(storage.RecordJob)
		}
	} else {
		records = s.listAll(storage.RecordJob)
	}

	var hits []JobSearchHit
	for _, r := range records {
		d := r.Data
		if d == nil {
			continue
		}
		// Typ-Filter: Gebot (offer) vs. Gesuch (request). Storage-Feld heißt
		// "type" (so speichert job_new), der API-Param heißt job_type.
		if typeFilter != "" && strings.ToLower(getStr(d, "type")) != typeFilter {
			continue
		}
		// Geohash-Region
		if q.GeohashPfx != "" {
			gh := getStr(d, "geohash")
			if gh == "" || !strings.HasPrefix(gh, q.GeohashPfx) {
				continue
			}
		}
		// Volltext über Titel/Beschreibung/Firma/Keywords
		if qLower != "" {
			hay := strings.ToLower(getStr(d, "title") + " " +
				getStr(d, "description") + " " + getStr(d, "company") + " " +
				getStr(d, "listing_text") + " " + keywordsToString(d["keywords"]))
			if !strings.Contains(hay, qLower) {
				continue
			}
		}

		hit := JobSearchHit{
			ID:       r.ID,
			OwnerID:  r.OwnerID,
			Title:    firstNonEmpty(getStr(d, "title"), getStr(d, "listing_text")),
			JobType:  getStr(d, "type"),
			Category: getStr(d, "category"),
			Location: getStr(d, "location"),
		}
		if sal, ok := toFloatOK(d["salary_min"]); ok {
			hit.SalaryMin = sal
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
	// Vollständige Kandidaten sammeln, stabil sortieren (deterministisch nach ID),
	// dann deckeln — kein willkürlicher Abbruch mehr.
	sort.Slice(hits, func(i, j int) bool { return hits[i].ID < hits[j].ID })
	if len(hits) > searchLocalMax {
		hits = hits[:searchLocalMax]
	}
	return hits
}

// searchJobs startet eine netzweite Job-Suche: lokale Treffer sofort, Netz-
// Treffer per Broadcast; Sammlung via /jobs/search/results pollbar.
// GET /api/v1/jobs/search?q=&job_type=&plz=&radius_km=
func (s *Server) searchJobs(c *gin.Context) {
	q := JobSearchQuery{
		SearchID:    generateID(),
		RequesterID: s.nodeID(),
		Q:           c.Query("q"),
		JobType:     c.Query("job_type"),
		RadiusKm:    parseQueryFloat(c, "radius_km"),
		CreatedAt:   time.Now().Unix(),
	}
	// PLZ → Zentrum + Geohash-Region (gleich wie Markt-Suche).
	if plz := strings.TrimSpace(c.Query("plz")); plz != "" {
		lat, lon := grid.PLZCentroid(plz)
		q.Lat, q.Lon = lat, lon
		prec := geo.PrecisionForRadiusKm(q.RadiusKm)
		q.GeohashPfx = geo.GeohashEncode(lat, lon, prec)
	}

	local := s.jobSearchLocal(q)
	// Eigene Treffer in die Sammlung legen, damit /results sie sofort enthält.
	s.jobSearchMgr.add(q.SearchID, local)

	// Ins Netz broadcasten (best effort).
	if s.node != nil {
		if data, err := json.Marshal(q); err == nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel()
			_ = s.node.Publish(ctx, p2p.TopicJobSearch, data)
		}
	}
	s.jobSearchMgr.gc()

	c.JSON(200, gin.H{
		"search_id": q.SearchID,
		"hits":      local,
		"note":      "Lokale Treffer sofort; Netz-Treffer per /jobs/search/results nachladen.",
	})
}

// jobSearchResults: aktuell gesammelte Treffer einer laufenden Job-Suche.
// GET /api/v1/jobs/search/results?id=<search_id>
func (s *Server) jobSearchResults(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		c.JSON(400, gin.H{"error": "id fehlt"})
		return
	}
	c.JSON(200, gin.H{"hits": s.jobSearchMgr.get(id)})
}

// registerJobSearchRoutes hängt die Job-Such-Endpunkte ein (eigene Top-Level-
// Group, um Routing-Konflikte mit /jobs/:id zu vermeiden).
func (s *Server) registerJobSearchRoutes() {
	g := s.router.Group("/api/v1/jobsearch")
	g.GET("",         s.searchJobs)
	g.GET("/results", s.jobSearchResults)
}

// registerJobSearchResponder: auf Job-Such-Pings antworten + Rückkanal.
func (s *Server) registerJobSearchResponder() {
	if s.node == nil {
		return
	}
	s.node.SetTopicHandler(p2p.TopicJobSearch, func(data []byte) {
		var q JobSearchQuery
		if err := json.Unmarshal(data, &q); err != nil {
			return
		}
		if q.RequesterID == s.nodeID() {
			return // eigene Pings ignorieren
		}
		if q.CreatedAt > 0 && time.Now().Unix()-q.CreatedAt > 60 {
			return // zu alt
		}
		hits := s.jobSearchLocal(q)
		if len(hits) == 0 {
			return // nichts Passendes → nicht antworten (spart Traffic)
		}
		if len(hits) > searchRespondMax {
			hits = hits[:searchRespondMax]
		}
		for i := range hits {
			hits[i].FromPeer = s.nodeID()
		}
		payload, err := json.Marshal(JobSearchResultMsg{SearchID: q.SearchID, Hits: hits})
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.node.SendAndReceive(ctx, q.RequesterID,
			p2p.JobSearchResultProtocol, payload); err != nil {
			s.log.Debug("Job-Such-Antwort fehlgeschlagen",
				zap.String("to", q.RequesterID), zap.Error(err))
		}
	})

	// Rückkanal: eingehende Treffer von anderen Nodes sammeln.
	s.node.RegisterProtocol(p2p.JobSearchResultProtocol,
		func(peerID string, data []byte) []byte {
			var msg JobSearchResultMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				return nil
			}
			s.jobSearchMgr.add(msg.SearchID, msg.Hits)
			return []byte(`{"ok":true}`)
		})
}
