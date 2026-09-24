package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fundus/node/internal/partner"
	"github.com/fundus/node/internal/storage"
)

// registerPartnerRoutes hängt die Partner-Endpunkte ein.
func (s *Server) registerPartnerRoutes() {
	g := s.router.Group("/api/v1/partner")
	{
		g.GET("/profile",    s.getPartnerProfile)
		g.PUT("/profile",    s.upsertPartnerProfile)
		g.DELETE("/profile", s.deletePartnerProfile)

		// Automatisches salt-basiertes Matching (Privacy-first)
		g.POST("/publish",     s.publishPartnerAd)
		g.GET("/ads",          s.listPartnerAds)
		g.GET("/matches",      s.getMatches)

		// Opt-in Suchindex (cleartext-Kategorien, bewusst veröffentlicht)
		g.POST("/publish/searchable", s.publishSearchableAd)
		g.GET("/search",              s.searchPartner)
		g.GET("/search/ads",          s.listSearchableAds)

		// Vordefinierte Listen
		g.GET("/preferences/lists", s.getPreferenceLists)
	}
}

// getPartnerProfile gibt das lokale Profil zurück.
// Das Profil verlässt nie den Node über öffentliche Kanäle.
// currentUserWallet liefert die Wallet-Adresse des eingeloggten Nutzers (aus der
// Session). Leer, wenn niemand eingeloggt ist. Dient als Eigentums-Schlüssel:
// jedes Profil/Inserat gehört der Wallet, unter der es gespeichert wurde.
func (s *Server) currentUserWallet(c *gin.Context) string {
	sess := s.getSession(c)
	if sess == nil {
		return ""
	}
	return strings.ToLower(sess.identity.FundusID)
}

// partnerProfileID bildet die Storage-ID für das Profil einer Wallet. Fällt auf
// "local" zurück, wenn niemand eingeloggt ist (Abwärtskompatibilität / lokaler
// Einzelbetrieb ohne Login).
func (s *Server) partnerProfileID(c *gin.Context) string {
	w := s.currentUserWallet(c)
	if w == "" {
		return "" // nicht eingeloggt → kein Profil
	}
	return "profile:" + w
}

func (s *Server) getPartnerProfile(c *gin.Context) {
	pid := s.partnerProfileID(c)
	if pid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte anmelden, um dein Partnerprofil zu laden."})
		return
	}
	rec, err := s.store.Get(storage.RecordPartnerProfile, pid)
	sess := s.getSession(c)
	// Eigenes (nicht gecachtes) Profil liegt hier, der Heim-Node ist aber ein
	// anderer: dem Heim-Node im Hintergrund eine Kopie geben, falls ihm das
	// Profil fehlt. Repariert Altbestände, bei denen der Heim-Node beim ersten
	// Login nach dem Update auf einen Node ohne Profil festgelegt wurde.
	if err == nil && rec != nil && sess != nil && s.homePeerOfSession(sess) != "" {
		if from, _ := rec.Data["cached_from"].(string); from == "" {
			go s.ensureHomeHasProfile(sess, rec.Data)
		}
	}
	// Gast-Node (Hybrid): Profil liegt beim Heim-Node → holen und lokal cachen.
	// Ein vorhandener Cache wird nach 5 Minuten aufgefrischt.
	if sess != nil && s.homePeerOfSession(sess) != "" {
		stale := err != nil || rec == nil
		if !stale {
			if from, _ := rec.Data["cached_from"].(string); from != "" && time.Since(rec.UpdatedAt) > 5*time.Minute {
				stale = true
			}
		}
		if stale && s.pullPartnerFromHome(sess, pid) {
			rec, err = s.store.Get(storage.RecordPartnerProfile, pid)
		}
	}
	// Nirgends gefunden (weder hier noch beim Heim-Node): alle verbundenen
	// Nodes fragen. Die Anfrage ist vom Nutzer signiert, gelesen wird nur sein
	// eigenes Profil. Ein Original schlägt eine gecachte Kopie.
	if (err != nil || rec == nil) && sess != nil {
		if d := s.findPartnerProfileInNetwork(sess); d != nil {
			delete(d, "cached_from")
			d["owner_wallet"] = strings.ToLower(sess.identity.FundusID)
			nr := &storage.Record{ID: pid, Type: storage.RecordPartnerProfile, Data: d}
			if s.store.Put(nr) == nil {
				rec, err = nr, nil
				if s.homePeerOfSession(sess) != "" {
					go s.ensureHomeHasProfile(sess, d)
				}
				if s.log != nil {
					s.log.Info("Partnerprofil im Netz wiedergefunden und übernommen", zap.String("profil", pid))
				}
			}
		}
	}
	if err != nil || rec == nil {
		// Migration: altes Profil lag unter dem globalen Key "local" (vor der
		// wallet-gebundenen ID). Nur übernehmen, wenn es nachweislich diesem
		// Nutzer gehört oder keinen Eigentümer trägt – sonst bekäme bei mehreren
		// Nutzern auf einem Node der Erste das Profil eines anderen.
		old, oerr := s.store.Get(storage.RecordPartnerProfile, "local")
		owner := ""
		if oerr == nil && old != nil {
			owner, _ = old.Data["owner_wallet"].(string)
		}
		if oerr == nil && old != nil && (owner == "" || strings.EqualFold(owner, strings.TrimPrefix(pid, "profile:"))) {
			old.ID = pid
			old.Data["owner_wallet"] = strings.TrimPrefix(pid, "profile:")
			_ = s.store.Put(old)
			_ = s.store.Delete(storage.RecordPartnerProfile, "local")
			rec = old
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "kein Profil vorhanden"})
			return
		}
	}
	// Bio wird zurückgegeben (nur lokal sichtbar), aber nie im PublicAd
	c.JSON(http.StatusOK, rec.Data)
}

// upsertPartnerProfile speichert das eigene Profil lokal.
func (s *Server) upsertPartnerProfile(c *gin.Context) {
	var profile partner.Profile
	if err := c.ShouldBindJSON(&profile); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Standort-Fallback: Wenn das Profil keine Koordinaten mitbringt, den
	// zentralen Node-Standort aus den Einstellungen (locationStore) übernehmen.
	// Sonst läge das Profil bei 0,0 und würde von der Umkreissuche nie gefunden.
	if profile.Lat == 0 && profile.Lon == 0 && s.locationStore != nil {
		if own := s.locationStore.get(); own.hasPosition() {
			profile.Lat = own.Lat
			profile.Lon = own.Lon
		}
	}

	// Bio als Rich-Text sanitizen (nur sichere Formatierungs-Tags).
	profile.Bio = sanitizeRichText(profile.Bio)

	// Salt erstellen oder bestehenden laden
	saltHex, _ := s.loadPartnerSalt()
	if saltHex == "" {
		salt, err := partner.NewSalt()
		if err != nil {
			s.internalError(c, err)
			return
		}
		saltHex = hex.EncodeToString(salt[:])
		s.savePartnerSalt(saltHex)
	}

	rec := &storage.Record{
		ID:   s.partnerProfileID(c),
		Type: storage.RecordPartnerProfile,
		Data: map[string]any{
			"nickname":     profile.Nickname,
			"gender":       profile.Gender,
			"age_range":    profile.AgeRange,
			"birthdate":    profile.Birthdate,
			"age":          profile.Age,
			"lat":          profile.Lat,
			"lon":          profile.Lon,
			"hobbies":      profile.Hobbies,
			"preferences":  profile.Preferences,
			"dislikes":     profile.Dislikes,
			"sexual_prefs": profile.SexualPrefs,
			"education":    profile.Education,
			"profession":   profile.Profession,
			"industry":     profile.Industry,
			"bio":          profile.Bio,
			"image_hashes": profile.ImageHashes,
			"video_hashes": profile.VideoHashes,
			"seeking":      profile.Seeking,
			"salt_hex":     saltHex,
			"owner_wallet": s.currentUserWallet(c), // Eigentums-Bindung
		},
	}

	if rec.ID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte anmelden, um dein Partnerprofil zu speichern."})
		return
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	// Gast-Node (Hybrid): Änderung signiert an den Heim-Node weiterreichen.
	if sess := s.getSession(c); sess != nil {
		s.pushPartnerToHome(sess, rec.Data)
	}

	// Bio und Salt aus der Antwort entfernen
	resp := copyWithout(rec.Data, "bio", "salt_hex")
	c.JSON(http.StatusOK, resp)
}

// deletePartnerProfile löscht Profil und Salt vom Node.
func (s *Server) deletePartnerProfile(c *gin.Context) {
	_ = s.store.Delete(storage.RecordPartnerProfile, s.partnerProfileID(c))
	// Der Salt ist für alle Nutzer dieses Nodes gemeinsam – NICHT löschen.
	if w := s.currentUserWallet(c); w != "" {
		_ = s.store.Delete(storage.RecordPartnerAd, "my-ad:"+w)
		_ = s.store.Delete(storage.RecordPartnerSearchAd, "my-search-ad:"+w)
	}
	c.Status(http.StatusNoContent)
}

// publishPartnerAd erstellt einen PublicAd aus dem lokalen Profil
// und publiziert ihn im P2P-Netz.
func (s *Server) publishPartnerAd(c *gin.Context) {
	profRec, err := s.store.Get(storage.RecordPartnerProfile, s.partnerProfileID(c))
	if err != nil || profRec == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kein Profil – zuerst Profil anlegen"})
		return
	}

	profile, salt, err := s.loadProfileAndSalt(profRec)
	if err != nil {
		s.internalError(c, err)
		return
	}

	peerID := ""
	if s.node != nil {
		peerID = s.node.ID().String()
	}

	ad, err := partner.MakePublicAd(profile, peerID, salt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Partnerprofil erfordert Login: sonst hätte das Profil keine FundusID und
	// wäre nicht kontaktierbar (der Messenger-Kontakt-Button bräuchte sie).
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden — ein Partnerprofil braucht eine Wallet-Identität."})
		return
	}
	// Wallet-/Chain-Identität des Erstellers mitführen (aus der Session), damit
	// der Kontakt-Button im Messenger die richtige Identität adressieren kann
	// und Profile knotenübergreifend dem User zugeordnet werden.
	ad.FundusID = strings.ToLower(sess.identity.FundusID)

	// Lokal speichern (für eigene Match-Referenz)
	adData, _ := ad.Marshal()
	adRec := &storage.Record{
		ID:   "my-ad:" + strings.ToLower(sess.identity.FundusID),
		Type: storage.RecordPartnerAd,
		Data: map[string]any{"raw": string(adData)},
	}
	_ = s.store.Put(adRec)

	// Im P2P-Netz publizieren
	if s.node != nil {
		node := s.node
		go func(){ pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second); defer cancel(); _ = node.Publish(pctx, "fundus.partner", adData) }()
	}

	s.log.Info("Partner ad published",
		zap.String("peerID", peerID),
	)

	// PublicAd zurückgeben (kein Klartext-PII)
	c.JSON(http.StatusCreated, ad)
}

// listPartnerAds gibt alle gecachten PublicAds zurück (von Peers erhalten).
func (s *Server) listPartnerAds(c *gin.Context) {
	records, err := s.store.List(storage.RecordPartnerAd)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ads": records, "count": len(records)})
}

// getMatches führt lokales Matching durch und gibt sortierte Ergebnisse zurück.
func (s *Server) getMatches(c *gin.Context) {
	profRec, err := s.store.Get(storage.RecordPartnerProfile, s.partnerProfileID(c))
	if err != nil || profRec == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kein Profil vorhanden"})
		return
	}

	profile, salt, err := s.loadProfileAndSalt(profRec)
	if err != nil {
		s.internalError(c, err)
		return
	}

	peerID := ""
	if s.node != nil {
		peerID = s.node.ID().String()
	}

	matcher, err := partner.NewMatcher(profile, peerID, salt)
	if err != nil {
		s.internalError(c, err)
		return
	}

	// Alle gecachten Ads laden und lokal matchen
	go s.partnerPullFromPeers(false) // frische Ads für den nächsten Aufruf holen
	ads, _ := s.store.List(storage.RecordPartnerAd)

	// Deduplizieren: pro Ersteller (FundusID) nur das NEUESTE Ad behalten. Ohne
	// das sammeln sich veraltete Ads an (z.B. wenn ein Peer eine neue Peer-ID
	// bekommt) und Matches zeigen alte Profile. Ads ohne FundusID (vor dem Login
	// erstellt) werden übersprungen — sie sind nicht kontaktierbar.
	type adEntry struct {
		ad      *partner.PublicAd
		created time.Time
	}
	newest := map[string]adEntry{}
	myFid := strings.ToLower(s.currentUserWallet(c))
	for _, rec := range ads {
		rawStr, _ := rec.Data["raw"].(string)
		if rawStr == "" {
			continue
		}
		ad := &partner.PublicAd{}
		if err := ad.Unmarshal([]byte(rawStr)); err != nil {
			continue
		}
		fid := strings.ToLower(ad.FundusID)
		if fid == "" {
			continue // ohne Messenger-Identität → nicht kontaktierbar, überspringen
		}
		// Nur das EIGENE Profil ausschließen – nicht alle Anzeigen dieses Nodes
		// (sonst fänden sich mehrere Nutzer desselben Nodes nie).
		if fid == myFid {
			continue
		}
		if prev, ok := newest[fid]; !ok || rec.CreatedAt.After(prev.created) {
			newest[fid] = adEntry{ad: ad, created: rec.CreatedAt}
		}
	}

	var matches []*partner.MatchResult
	rejected := map[string]int{}
	for _, e := range newest {
		if result, why := matcher.MatchWithReason(e.ad); result != nil {
			matches = append(matches, result)
		} else {
			rejected[why]++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"matches":  matches,
		"count":    len(matches),
		"profiles": len(newest), // bekannte fremde Profile (mit Identität)
		"rejected": rejected,    // Gründe für verworfene Profile
	})
}

// publishSearchableAd erstellt einen opt-in SearchableAd (Klartextkategorien)
// und publiziert ihn auf dem Suchtopic.
func (s *Server) publishSearchableAd(c *gin.Context) {
	profRec, err := s.store.Get(storage.RecordPartnerProfile, s.partnerProfileID(c))
	if err != nil || profRec == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kein Profil – zuerst Profil anlegen"})
		return
	}
	profile, _, err := s.loadProfileAndSalt(profRec)
	if err != nil {
		s.internalError(c, err)
		return
	}

	peerID := ""
	if s.node != nil {
		peerID = s.node.ID().String()
	}

	searchAd := partner.MakeSearchableAd(profile, peerID)
	// FundusID aus der Session mitführen, damit man den Treffer aus der Suche
	// heraus im Messenger kontaktieren kann (wie beim Match-Ad).
	if sess := s.getSession(c); sess != nil && sess.identity != nil {
		searchAd.FundusID = strings.ToLower(sess.identity.FundusID)
	}
	adData, _ := json.Marshal(searchAd)

	searchID := "my-search-ad"
	if searchAd.FundusID != "" {
		searchID = "my-search-ad:" + searchAd.FundusID // pro Nutzer (mehrere Logins je Node)
	}
	rec := &storage.Record{
		ID:        searchID,
		Type:      storage.RecordPartnerSearchAd,
		OwnerID:   peerID,
		CreatedAt: searchAd.PublishedAt,
		Data:      map[string]any{"raw": string(adData)},
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	if s.node != nil {
		node := s.node
		go func(){ pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second); defer cancel(); _ = node.Publish(pctx, "fundus.partner.search", adData) }()
	}

	s.log.Info("Searchable partner ad published", zap.String("peerID", peerID))
	c.JSON(http.StatusCreated, searchAd)
}

// searchPartner – parametrische Suche auf gecachten SearchableAds.
//
// Query-Parameter:
//   lat, lon, radius_km  – Suchzentrum (Defaults: eigener Standort aus Profil)
//   gender               – kommagetrennt: "female,non-binary"
//   age_range            – kommagetrennt: "26-35,36-45"
//   education            – "university,vocational"
//   industry             – "it,education"
//   hobby                – kommagetrennt (Hobbyname)
//   pref                 – kommagetrennt
//   sex                  – Abkürzungen: "BDSM,VAN"
//   mutual               – "true" = nur gegenseitige Matches
//   my_gender, my_age_range – eigene Werte für Gegenseitigkeitsprüfung
//   sort                 – "score"|"distance"|"hobbies"|"sex"
//   limit, offset        – Paginierung
func (s *Server) searchPartner(c *gin.Context) {
	filter := partner.SearchFilter{
		Lat:             parseQueryFloat(c, "lat"),
		Lon:             parseQueryFloat(c, "lon"),
		RadiusKm:        parseQueryFloat(c, "radius_km"),
		Genders:         splitQuery(c.Query("gender")),
		AgeRanges:       nil, // Klassenfilter ersetzt durch echten von-bis-Filter
		AgeMin:          int(parseQueryFloat(c, "age_min")),
		AgeMax:          int(parseQueryFloat(c, "age_max")),
		EducationGroups: splitQuery(c.Query("education")),
		IndustryGroups:  splitQuery(c.Query("industry")),
		Hobbies:         splitQuery(c.Query("hobby")),
		Preferences:     splitQuery(c.Query("pref")),
		SexPrefAbbrs:    splitQuery(c.Query("sex")),
		RequireMutual:   c.Query("mutual") == "true",
		MyGender:        c.Query("my_gender"),
		MyAgeRange:      c.Query("my_age_range"),
		SortBy:          partner.SortOrder(c.DefaultQuery("sort", string(partner.SortByScore))),
		Limit:           parseQueryInt(c, "limit"),
		Offset:          parseQueryInt(c, "offset"),
	}

	// Standort-Fallback: eigenes Profil
	if filter.Lat == 0 && filter.Lon == 0 {
		if profRec, err := s.store.Get(storage.RecordPartnerProfile, s.partnerProfileID(c)); err == nil && profRec != nil {
			if profile, _, err2 := s.loadProfileAndSalt(profRec); err2 == nil && profile != nil {
				filter.Lat = profile.Lat
				filter.Lon = profile.Lon
				if filter.RadiusKm == 0 {
					filter.RadiusKm = profile.Seeking.RadiusKm
				}
			}
		}
	}

	go s.partnerPullFromPeers(false) // frische Ads für den nächsten Aufruf holen
	// SearchableAds laden
	records, err := s.store.List(storage.RecordPartnerSearchAd)
	if err != nil {
		s.internalError(c, err)
		return
	}

	myPeerID := ""
	if s.node != nil {
		myPeerID = s.node.ID().String()
	}

	mySearchFid := strings.ToLower(s.currentUserWallet(c))
	ads := make([]*partner.SearchableAd, 0, len(records))
	for _, rec := range records {
		rawStr, _ := rec.Data["raw"].(string)
		if rawStr == "" {
			continue
		}
		ad := &partner.SearchableAd{}
		if err := json.Unmarshal([]byte(rawStr), ad); err != nil {
			continue
		}
		// Nur das eigene Profil ausblenden (per FundusID); Anzeigen anderer
		// Nutzer DESSELBEN Nodes bleiben sichtbar. Ohne FundusID (Altbestand)
		// gilt weiter die Node-Zuordnung.
		if f := strings.ToLower(ad.FundusID); f != "" {
			if f == mySearchFid {
				continue
			}
		} else if ad.PeerID == myPeerID {
			continue
		}
		ads = append(ads, ad)
	}

	searcher := partner.NewSearcher()
	results, total := searcher.Search(ads, filter)

	c.JSON(http.StatusOK, gin.H{
		"results": results,
		"total":   total,
		"count":   len(results),
		"offset":  filter.Offset,
		"limit":   filter.Limit,
		"sort_by": filter.SortBy,
	})
}

// listSearchableAds – rohe gecachte SearchableAds (für Debugging / Admin).
func (s *Server) listSearchableAds(c *gin.Context) {
	records, err := s.store.List(storage.RecordPartnerSearchAd)
	if err != nil {
		s.internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ads": records, "count": len(records)})
}

// getPreferenceLists gibt alle vordefinierten Listen für das Frontend zurück.
func (s *Server) getPreferenceLists(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"education_levels":   partner.DefaultEducationLevels,
		"hobbies":            partner.DefaultHobbies,
		"industries":         partner.DefaultIndustries,
		"preferences":        partner.DefaultPreferences,
		"dislikes":           partner.DefaultDislikes,
		"sexual_preferences": partner.DefaultSexualPreferences,
	})
}

// =============================================================================
//  Interne Hilfsmethoden
// =============================================================================

func (s *Server) loadPartnerSalt() (string, error) {
	rec, err := s.store.Get(storage.RecordPartnerSalt, "local")
	if err != nil || rec == nil {
		return "", nil
	}
	saltHex, _ := rec.Data["salt"].(string)
	return saltHex, nil
}

func (s *Server) savePartnerSalt(saltHex string) {
	rec := &storage.Record{
		ID:   "local",
		Type: storage.RecordPartnerSalt,
		Data: map[string]any{"salt": saltHex},
	}
	_ = s.store.Put(rec)
}

func (s *Server) loadProfileAndSalt(rec *storage.Record) (*partner.Profile, partner.Salt, error) {
	d := rec.Data

	var salt partner.Salt
	saltHex, _ := d["salt_hex"].(string)
	if saltHex != "" {
		b, err := hex.DecodeString(saltHex)
		if err != nil || len(b) != 16 {
			return nil, salt, fmt.Errorf("invalid salt")
		}
		copy(salt[:], b)
	}

	// SexualPrefs deserialisieren
	sexPrefsRaw, _ := d["sexual_prefs"].([]any)
	sexPrefs := make([]partner.SexualPreference, 0, len(sexPrefsRaw))
	for _, raw := range sexPrefsRaw {
		if m, ok := raw.(map[string]any); ok {
			abbr, _ := m["abbr"].(string)
			role, _ := m["role"].(string)
			if abbr != "" {
				sexPrefs = append(sexPrefs, partner.SexualPreference{
					Abbr: abbr,
					Role: partner.SexPrefRole(role),
				})
			}
		}
	}

	seekingRaw, _ := d["seeking"].(map[string]any)
	seeking := partner.Preference{}
	if seekingRaw != nil {
		seeking = partner.Preference{
			Genders:      toGenderSlice(seekingRaw["genders"]),
			AgeRanges:    toAgeRangeSlice(seekingRaw["age_ranges"]),
			RadiusKm:     toFloat(seekingRaw["radius_km"]),
			Educations:   toStringSlice(seekingRaw["educations"]),
			Industries:   toStringSlice(seekingRaw["industries"]),
			SexPrefAbbrs: toStringSlice(seekingRaw["sex_pref_abbrs"]),
		}
	}

	profile := &partner.Profile{
		Nickname:    str(d["nickname"]),
		Gender:      partner.Gender(str(d["gender"])),
		AgeRange:    partner.AgeRange(str(d["age_range"])),
		Birthdate:   str(d["birthdate"]),
		Age:         int(toFloat(d["age"])),
		Lat:         toFloat(d["lat"]),
		Lon:         toFloat(d["lon"]),
		Education:   str(d["education"]),
		Profession:  str(d["profession"]),
		Industry:    str(d["industry"]),
		Hobbies:     toStringSlice(d["hobbies"]),
		Preferences: toStringSlice(d["preferences"]),
		Dislikes:    toStringSlice(d["dislikes"]),
		SexualPrefs: sexPrefs,
		Bio:         str(d["bio"]),
		ImageHashes: toStringSlice(d["image_hashes"]),
		VideoHashes: toStringSlice(d["video_hashes"]),
		Seeking:     seeking,
	}

	return profile, salt, nil
}

// Type-conversion helpers
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

func toStringSlice(v any) []string {
	if sl, ok := v.([]any); ok {
		result := make([]string, 0, len(sl))
		for _, item := range sl {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

func toGenderSlice(v any) []partner.Gender {
	ss := toStringSlice(v)
	result := make([]partner.Gender, len(ss))
	for i, s := range ss {
		result[i] = partner.Gender(s)
	}
	return result
}

func toAgeRangeSlice(v any) []partner.AgeRange {
	ss := toStringSlice(v)
	result := make([]partner.AgeRange, len(ss))
	for i, s := range ss {
		result[i] = partner.AgeRange(s)
	}
	return result
}

func copyWithout(m map[string]any, keys ...string) map[string]any {
	skip := make(map[string]bool, len(keys))
	for _, k := range keys {
		skip[k] = true
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		if !skip[k] {
			result[k] = v
		}
	}
	return result
}

// splitQuery teilt einen Query-String an Kommas.
func splitQuery(q string) []string {
	if q == "" {
		return nil
	}
	var result []string
	for _, p := range strings.Split(q, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func parseQueryFloat(c *gin.Context, key string) float64 {
	s := c.Query(key)
	if s == "" {
		return 0
	}
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func parseQueryInt(c *gin.Context, key string) int {
	s := c.Query(key)
	if s == "" {
		return 0
	}
	var i int
	fmt.Sscanf(s, "%d", &i)
	return i
}

// =============================================================================
//  Partner-Ads: automatische Pflege
// =============================================================================
//
// Empfangene Ads liegen je Peer-ID im Store und wurden bisher nie gelöscht –
// nach Peer-ID-Wechseln sammelten sich Karteileichen an (Doppelanzeigen, Ads
// ohne FundusID). Jetzt:
//   - eigene Ads (my-ad:*, my-search-ad) alle 6 h neu verteilen,
//   - fremde Ads, die seit 48 h nicht aufgefrischt wurden, löschen.
// Der manuelle Endpunkt /admin/partner/purge-stale bleibt für Sofort-Aufräumen.

const (
	partnerRepublishEvery = 6 * time.Hour
	partnerAdMaxAge       = 48 * time.Hour
)

var partnerMaintOnce sync.Once

func (s *Server) partnerMaintenance(ctx context.Context) {
	// Erster Lauf kurz nach dem Start (P2P braucht etwas bis zu den Peers).
	republish := time.NewTimer(2 * time.Minute)
	defer republish.Stop()
	pull := time.NewTicker(partnerPullEvery)
	defer pull.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-republish.C:
			s.partnerMaintainOnce()
			s.partnerPullFromPeers(true)
			republish.Reset(partnerRepublishEvery)
		case <-pull.C:
			s.partnerPullFromPeers(true)
		}
	}
}

// adTimeField liest einen Zeitstempel ("pub" / "exp") aus der Roh-Ad.
func adTimeField(m map[string]any, k string) time.Time {
	if v, ok := m[k].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// partnerAdFresh: Frische nach dem VERÖFFENTLICHUNGSZEITPUNKT des Urhebers
// (nicht nach dem lokalen Empfang) – sonst hielten sich Nodes gegenseitig
// veraltete Ads per Pull ewig am Leben.
func partnerAdFresh(raw string, fallback time.Time) bool {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return fallback.After(time.Now().Add(-partnerAdMaxAge))
	}
	if exp := adTimeField(m, "exp"); !exp.IsZero() && exp.Before(time.Now()) {
		return false
	}
	pub := adTimeField(m, "pub")
	if pub.IsZero() {
		pub = fallback
	}
	return pub.After(time.Now().Add(-partnerAdMaxAge))
}

func (s *Server) partnerMaintainOnce() {
	if s.store == nil {
		return
	}
	myPeer := ""
	if s.node != nil {
		myPeer = s.node.ID().String()
	}
	republished, purged := 0, 0
	// Eigene Anzeigen je Nutzer sammeln und NACH der Schleife aus dem aktuellen
	// Profil neu bauen. Früher wurde nur der Zeitstempel der gespeicherten
	// Anzeige erneuert – eine veraltete Anzeige (z.B. von einer älteren
	// Programmversion) lebte so endlos weiter und wirkte immer frisch.
	ownAds := map[string]map[storage.RecordType]map[string]any{}
	for _, t := range []struct {
		rt    storage.RecordType
		topic string
	}{
		{storage.RecordPartnerAd, "fundus.partner"},
		{storage.RecordPartnerSearchAd, "fundus.partner.search"},
	} {
		recs, err := s.store.List(t.rt)
		if err != nil {
			continue
		}
		for _, r := range recs {
			raw, _ := r.Data["raw"].(string)
			own := strings.HasPrefix(r.ID, "my-ad") || strings.HasPrefix(r.ID, "my-search-ad")
			if own {
				var m map[string]any
				if raw == "" || json.Unmarshal([]byte(raw), &m) != nil {
					continue
				}
				// Altlast: per früherem Bestandsabgleich importierte FREMDE Ad
				// unter "my-…"-ID → entfernen.
				if pid, _ := m["peer_id"].(string); myPeer != "" && pid != "" && pid != myPeer {
					if s.store.Delete(t.rt, r.ID) == nil {
						purged++
					}
					continue
				}
				fid := ""
				if i := strings.IndexByte(r.ID, ':'); i >= 0 {
					fid = strings.ToLower(r.ID[i+1:])
				} else {
					// Altlast "my-search-ad" ohne Nutzer (galt pro Node → mehrere
					// Logins überschrieben sich): auf den Eigentümer umziehen.
					fid, _ = m["fundus_id"].(string)
					fid = strings.ToLower(fid)
					_ = s.store.Delete(t.rt, r.ID)
				}
				if fid == "" {
					continue
				}
				if ownAds[fid] == nil {
					ownAds[fid] = map[storage.RecordType]map[string]any{}
				}
				ownAds[fid][t.rt] = m
				continue
			}
			ts := r.UpdatedAt
			if ts.IsZero() {
				ts = r.CreatedAt
			}
			if !partnerAdFresh(raw, ts) {
				if s.store.Delete(t.rt, r.ID) == nil {
					purged++
				}
			}
		}
	}
	for fid, types := range ownAds {
		matchRaw, searchRaw, ok := s.buildOwnPartnerAds(fid)
		for rt, old := range types {
			id, topic, fresh := "my-ad:"+fid, "fundus.partner", matchRaw
			if rt == storage.RecordPartnerSearchAd {
				id, topic, fresh = "my-search-ad:"+fid, "fundus.partner.search", searchRaw
			}
			if !ok || len(fresh) == 0 {
				// Kein lokales Profil (z.B. Gast-Node): gespeicherte Anzeige mit
				// erneuertem Zeitstempel weiterverteilen, wie bisher.
				now := time.Now().UTC()
				old["pub"] = now.Format(time.RFC3339Nano)
				if _, has := old["exp"]; has {
					old["exp"] = now.Add(24 * time.Hour).Format(time.RFC3339Nano)
				}
				fresh, _ = json.Marshal(old)
			}
			if len(fresh) == 0 {
				continue
			}
			_ = s.store.Put(&storage.Record{ID: id, Type: rt, Data: map[string]any{"raw": string(fresh)}})
			if s.node != nil {
				pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = s.node.Publish(pctx, topic, fresh)
				cancel()
				republished++
			}
		}
	}
	if republished > 0 || purged > 0 {
		s.log.Info("Partner-Ads gepflegt", zap.Int("neu_verteilt", republished), zap.Int("veraltet_geloescht", purged))
	}
}

// buildOwnPartnerAds baut Match- und Such-Anzeige eines Nutzers frisch aus
// seinem aktuellen Profil (ohne Session – für die Neuverteilung).
func (s *Server) buildOwnPartnerAds(fid string) (matchRaw, searchRaw []byte, ok bool) {
	fid = strings.ToLower(fid)
	profRec, err := s.store.Get(storage.RecordPartnerProfile, "profile:"+fid)
	if err != nil || profRec == nil {
		return nil, nil, false
	}
	profile, salt, err := s.loadProfileAndSalt(profRec)
	if err != nil {
		return nil, nil, false
	}
	peerID := ""
	if s.node != nil {
		peerID = s.node.ID().String()
	}
	ad, err := partner.MakePublicAd(profile, peerID, salt)
	if err != nil {
		return nil, nil, false
	}
	ad.FundusID = fid
	matchRaw, _ = ad.Marshal()
	sad := partner.MakeSearchableAd(profile, peerID)
	sad.FundusID = fid
	searchRaw, _ = json.Marshal(sad)
	return matchRaw, searchRaw, true
}

// =============================================================================
//  Partner-Pull: Ads aktiv bei den Peers abfragen (wie Marktplatz/Orderbuch)
// =============================================================================
//
// Gossip ist Push-only: wer zum Veröffentlichungszeitpunkt nicht (oder nur über
// eine eingeschränkte Relay-Verbindung) verbunden war, bekommt die Ad nie. Der
// Pull fragt jeden verbundenen Peer nach allen frischen Partner-Ads, die er
// kennt (eigene + empfangene) – so erreichen Profile aus dem Heimnetz auch
// Nodes hinter NAT, die nur über einen einzigen Peer angebunden sind.

const PartnerPullProtocol = "/fundus/partner-pull/1.0.0"

const partnerPullEvery = 5 * time.Minute

type partnerPullItem struct {
	Topic string `json:"t"`
	Raw   string `json:"r"`
}

var (
	partnerPullMu   sync.Mutex
	partnerPullLast time.Time
)

func (s *Server) registerPartnerPull() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(PartnerPullProtocol, func(peerID string, data []byte) []byte {
		out, _ := json.Marshal(s.partnerShareable())
		return out
	})
}

// partnerShareable liefert alle frischen Partner-Ads (eigene + empfangene).
func (s *Server) partnerShareable() []partnerPullItem {
	var items []partnerPullItem
	if s.store == nil {
		return items
	}
	for _, t := range []struct {
		rt    storage.RecordType
		topic string
	}{
		{storage.RecordPartnerAd, "fundus.partner"},
		{storage.RecordPartnerSearchAd, "fundus.partner.search"},
	} {
		recs, _ := s.store.List(t.rt)
		for _, r := range recs {
			raw, _ := r.Data["raw"].(string)
			if raw == "" || !partnerAdFresh(raw, r.UpdatedAt) {
				continue
			}
			items = append(items, partnerPullItem{Topic: t.topic, Raw: raw})
		}
	}
	return items
}

// partnerPullFromPeers fragt alle verbundenen Peers ab. Ohne force höchstens
// einmal pro Minute (Aufruf beim Öffnen von Suche/Matches).
func (s *Server) partnerPullFromPeers(force bool) {
	if s.node == nil || s.store == nil {
		return
	}
	partnerPullMu.Lock()
	if !force && time.Since(partnerPullLast) < time.Minute {
		partnerPullMu.Unlock()
		return
	}
	partnerPullLast = time.Now()
	partnerPullMu.Unlock()

	myPeer := s.node.ID().String()
	for _, pid := range s.node.Peers() {
		go func(peerID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			resp, err := s.node.SendAndReceive(ctx, peerID, PartnerPullProtocol, []byte("PULL"))
			if err != nil {
				return
			}
			var items []partnerPullItem
			if json.Unmarshal(resp, &items) != nil {
				return
			}
			n := 0
			for _, it := range items {
				if it.Topic != "fundus.partner" && it.Topic != "fundus.partner.search" {
					continue
				}
				if !partnerAdFresh(it.Raw, time.Now()) {
					continue
				}
				// Eigene Ads (über Umwege zurückgekommen) nicht als fremde speichern.
				if _, owner := storage.PartnerAdKey([]byte(it.Raw), peerID); owner == myPeer {
					continue
				}
				if s.store.HandleIncoming(it.Topic, peerID, []byte(it.Raw)) == nil {
					n++
				}
			}
			if n > 0 {
				s.log.Info("Partner-Pull", zap.String("peer", peerID), zap.Int("ads", n))
			}
		}(pid.String())
	}
}

// findPartnerProfileInNetwork fragt alle verbundenen Nodes parallel nach dem
// Partnerprofil des angemeldeten Nutzers (signierte Leseanfrage). Liefert das
// erste Original (ohne cached_from), sonst die erste gecachte Kopie, sonst nil.
func (s *Server) findPartnerProfileInNetwork(sess *Session) map[string]any {
	if s.node == nil || sess == nil || sess.identity == nil {
		return nil
	}
	peers := s.node.Peers()
	if len(peers) == 0 {
		return nil
	}
	type found struct {
		d        map[string]any
		original bool
	}
	ch := make(chan found, len(peers))
	for _, p := range peers {
		go func(target string) {
			raw, err := s.homeCallPeer(sess, target, "partner.get", "", nil)
			if err != nil || len(raw) == 0 || string(raw) == "null" {
				ch <- found{}
				return
			}
			var d map[string]any
			if json.Unmarshal(raw, &d) != nil || len(d) == 0 {
				ch <- found{}
				return
			}
			from, _ := d["cached_from"].(string)
			ch <- found{d: d, original: from == ""}
		}(p.String())
	}
	var best map[string]any
	deadline := time.After(8 * time.Second)
	for i := 0; i < len(peers); i++ {
		select {
		case f := <-ch:
			if f.d == nil {
				continue
			}
			if f.original {
				return f.d
			}
			if best == nil {
				best = f.d
			}
		case <-deadline:
			return best
		}
	}
	return best
}

// ensureHomeHasProfile gibt dem Heim-Node eine Kopie des Profils, falls ihm
// keins vorliegt (überschreibt nie ein vorhandenes – das könnte neuer sein).
func (s *Server) ensureHomeHasProfile(sess *Session, data map[string]any) {
	raw, err := s.homeCall(sess, "partner.get", "", nil)
	if err != nil {
		return // Heim-Node nicht erreichbar – beim nächsten Aufruf erneut
	}
	if len(raw) > 0 && string(raw) != "null" {
		var d map[string]any
		if json.Unmarshal(raw, &d) == nil && len(d) > 0 {
			return
		}
	}
	cp := make(map[string]any, len(data))
	for k, v := range data {
		cp[k] = v
	}
	delete(cp, "cached_from")
	if _, err := s.homeCall(sess, "partner.put", "", cp); err == nil && s.log != nil {
		s.log.Info("Partnerprofil an den Heim-Node übertragen (fehlte dort)")
	}
}
