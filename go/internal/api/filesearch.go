package api

// Netzweite Suche nach öffentlich geteilten Dateien. Spiegelt das Muster der
// Listing-Suche (search.go): Such-Ping per GossipSub broadcasten, jeder Node
// durchsucht seine geteilten Dateien lokal und schickt Treffer per direktem
// Stream zurück. Der Sucher sammelt sie und der Client pollt die Ergebnisse.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/filestore"
	"github.com/fundus/node/internal/p2p"
)

// FileSearchQuery ist der Such-Ping.
type FileSearchQuery struct {
	SearchID    string `json:"search_id"`
	RequesterID string `json:"requester_id"`
	Q           string `json:"q"`
	CreatedAt   int64  `json:"created_at"`
}

// FileSearchHit ist ein Treffer (eine geteilte Datei auf irgendeinem Node).
type FileSearchHit struct {
	Hash      string `json:"hash"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mime_type,omitempty"`
	Encrypted bool   `json:"encrypted"`
	FromPeer  string `json:"from_peer,omitempty"`
}

// FileSearchResultMsg ist die Antwort eines Nodes (Rückkanal).
type FileSearchResultMsg struct {
	SearchID string          `json:"search_id"`
	Hits     []FileSearchHit `json:"hits"`
}

// --- Sammler für laufende Datei-Suchen ---

type fileSearchSession struct {
	hits      []FileSearchHit
	seen      map[string]bool // dedupe per Hash
	createdAt time.Time
}

type fileSearchManager struct {
	mu       sync.Mutex
	sessions map[string]*fileSearchSession
}

func newFileSearchManager() *fileSearchManager {
	return &fileSearchManager{sessions: map[string]*fileSearchSession{}}
}

func (m *fileSearchManager) add(searchID string, hits []FileSearchHit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[searchID]
	if sess == nil {
		sess = &fileSearchSession{seen: map[string]bool{}, createdAt: time.Now()}
		m.sessions[searchID] = sess
	}
	for _, h := range hits {
		if len(sess.hits) >= searchCollectMax {
			break // Gesamt-Sammlung gedeckelt (gleiches Limit wie Markt-Suche)
		}
		if h.Hash == "" || sess.seen[h.Hash] {
			continue
		}
		sess.seen[h.Hash] = true
		sess.hits = append(sess.hits, h)
	}
}

func (m *fileSearchManager) get(searchID string) []FileSearchHit {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[searchID]; sess != nil {
		out := make([]FileSearchHit, len(sess.hits))
		copy(out, sess.hits)
		return out
	}
	return nil
}

func (m *fileSearchManager) gc() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sess := range m.sessions {
		if time.Since(sess.createdAt) > 2*time.Minute {
			delete(m.sessions, id)
		}
	}
}

// fileSearchLocal durchsucht die lokal geteilten Dateien.
func (s *Server) fileSearchLocal(q string) []FileSearchHit {
	if s.fileStore == nil {
		return nil
	}
	var matches []filestore.SharedFile
	matches = s.fileStore.SearchShared(q, 50)
	hits := make([]FileSearchHit, 0, len(matches))
	for _, f := range matches {
		hits = append(hits, FileSearchHit{
			Hash: f.Hash, Name: f.Name, Size: f.Size,
			MimeType: f.MimeType, Encrypted: f.Encrypted,
		})
	}
	return hits
}

// registerFileSearchResponder: auf Datei-Such-Pings antworten + Rückkanal.
func (s *Server) registerFileSearchResponder() {
	if s.node == nil {
		return
	}
	s.node.SetTopicHandler(p2p.TopicFileSearch, func(data []byte) {
		var q FileSearchQuery
		if err := json.Unmarshal(data, &q); err != nil {
			return
		}
		if q.RequesterID == s.nodeID() {
			return // eigene Pings ignorieren
		}
		if q.CreatedAt > 0 && time.Now().Unix()-q.CreatedAt > 60 {
			return // zu alt
		}
		hits := s.fileSearchLocal(q.Q)
		if len(hits) == 0 {
			return
		}
		for i := range hits {
			hits[i].FromPeer = s.nodeID()
		}
		payload, err := json.Marshal(FileSearchResultMsg{SearchID: q.SearchID, Hits: hits})
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := s.node.SendAndReceive(ctx, q.RequesterID,
			p2p.FileSearchResultProtocol, payload); err != nil {
			s.log.Debug("Datei-Such-Antwort fehlgeschlagen",
				zap.String("to", q.RequesterID), zap.Error(err))
		}
	})

	s.node.RegisterProtocol(p2p.FileSearchResultProtocol,
		func(peerID string, data []byte) []byte {
			var msg FileSearchResultMsg
			if err := json.Unmarshal(data, &msg); err != nil {
				return []byte("ERR")
			}
			s.fileSearchMgr.add(msg.SearchID, msg.Hits)
			return []byte("OK")
		})
}

// --- HTTP-Routen ---

func (s *Server) registerFileSearchRoutes() {
	g := s.router.Group("/api/v1/files")
	g.POST("/share",         s.fileShare)
	g.POST("/unshare",       s.fileUnshare)
	g.GET("/shared",         s.fileShared)
	g.GET("/search",         s.fileSearchStart)
	g.GET("/search/results", s.fileSearchResults)
}

// POST /api/v1/files/share  body: {hash, name, size, mime_type, encrypted}
func (s *Server) fileShare(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	var req struct {
		Hash      string `json:"hash"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		MimeType  string `json:"mime_type"`
		Encrypted bool   `json:"encrypted"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hash fehlt"})
		return
	}
	s.fileStore.ShareFile(req.Hash, req.Name, req.Size, req.MimeType, req.Encrypted)
	c.JSON(http.StatusOK, gin.H{"shared": true, "hash": req.Hash})
}

// POST /api/v1/files/unshare  body: {hash}
func (s *Server) fileUnshare(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Filesharing deaktiviert"})
		return
	}
	var req struct{ Hash string `json:"hash"` }
	if err := c.ShouldBindJSON(&req); err != nil || req.Hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hash fehlt"})
		return
	}
	s.fileStore.UnshareFile(req.Hash)
	c.JSON(http.StatusOK, gin.H{"shared": false, "hash": req.Hash})
}

// GET /api/v1/files/shared — eigene geteilte Dateien
func (s *Server) fileShared(c *gin.Context) {
	if s.fileStore == nil {
		c.JSON(http.StatusOK, gin.H{"files": []interface{}{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": s.fileStore.ListShared()})
}

// GET /api/v1/files/search?q=... — lokale Treffer sofort + Broadcast ins Netz
func (s *Server) fileSearchStart(c *gin.Context) {
	q := FileSearchQuery{
		SearchID:  generateID(),
		Q:         c.Query("q"),
		CreatedAt: time.Now().Unix(),
	}
	if s.node != nil {
		q.RequesterID = s.nodeID()
	}
	// 1. Lokale Treffer sofort
	local := s.fileSearchLocal(q.Q)
	for i := range local {
		local[i].FromPeer = "local"
	}
	s.fileSearchMgr.add(q.SearchID, local)

	// 2. Broadcast ins Netz (Antworten kommen über den Rückkanal)
	if s.node != nil {
		if payload, err := json.Marshal(q); err == nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel()
			if err := s.node.Publish(ctx, p2p.TopicFileSearch, payload); err != nil {
				s.log.Debug("Datei-Such-Broadcast fehlgeschlagen", zap.Error(err))
			}
		}
	}
	s.fileSearchMgr.gc()

	c.JSON(http.StatusOK, gin.H{
		"search_id": q.SearchID,
		"hits":      s.fileSearchMgr.get(q.SearchID),
	})
}

// GET /api/v1/files/search/results?id=<search_id>
func (s *Server) fileSearchResults(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id fehlt"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"search_id": id,
		"hits":      s.fileSearchMgr.get(id),
	})
}
