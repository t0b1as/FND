package api

// Drive – Transportdienst im Fundus-Netz (R635).
//
// Jeder kann BEIDES sein: Fahrer und Kunde. Ein Fahrer hinterlegt sein Profil
// mit Preisen, ein Kunde stellt ein Fahrtgesuch ein – entweder öffentlich für
// alle Fahrer oder gezielt an einen, den er auf der Karte ausgewählt hat.
// Nimmt ein Fahrer ein Gesuch an, wird es gesperrt und verschwindet aus der
// offenen Liste.
//
// Preismodell (vom Fahrer selbst festgelegt):
//   Anfahrtspauschale + Anfahrt-km × Satz + Fahrtstrecke × Satz
// Beide Anfahrtsanteile sind freiwillig; wer nur einen km-Satz nimmt, lässt die
// Pauschale auf 0.
//
// Strecke: über einen Routendienst (OSRM), mit Rückfall auf Luftlinie × 1,25.
// Welcher Weg genutzt wurde, steht in der Antwort – ein geschätzter Preis darf
// den Kunden nicht überraschen.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/fundus/node/internal/geo"
	"github.com/fundus/node/internal/grid"
	"github.com/fundus/node/internal/storage"
	"go.uber.org/zap"
)

// ── Datenmodell ─────────────────────────────────────────────────────────────

// DriveStop ist ein Halt: Start, Zwischenhalt oder Ziel.
type DriveStop struct {
	Label string  `json:"label"`          // Adresse/Beschreibung, wie der Kunde sie eingab
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
}

// DriveGoods beschreibt Ware statt Personen.
type DriveGoods struct {
	LengthCm float64 `json:"length_cm"`
	WidthCm  float64 `json:"width_cm"`
	HeightCm float64 `json:"height_cm"`
	WeightKg float64 `json:"weight_kg"`
	Desc     string  `json:"desc"`
}

// driveDriver ist das Fahrerprofil (abgelegt als RecordDriver, ID "driver:<fid>").
type driveDriver struct {
	FID             string      `json:"fid"`
	Name            string      `json:"name"`
	Active          bool        `json:"active"`            // nimmt derzeit Aufträge an
	PerKmFND        float64     `json:"per_km_fnd"`        // Fahrtkosten je km
	ApproachFlatFND float64     `json:"approach_flat_fnd"` // An-/Abfahrtspauschale
	ApproachPerKm   float64     `json:"approach_per_km"`   // An-/Abfahrt je km
	Lat             float64     `json:"lat"`               // Standort (Startpunkt des Fahrers)
	Lon             float64     `json:"lon"`
	PLZ             string      `json:"plz"`
	RadiusKm        float64     `json:"radius_km"`         // Einsatzgebiet
	Vehicle         string      `json:"vehicle"`
	Goods           bool        `json:"goods"`             // transportiert auch Ware
	Cargo           DriveGoods  `json:"cargo"`             // maximale Maße/Gewicht
	Seats           int         `json:"seats"`
	Note            string      `json:"note"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

// driveRide ist ein Fahrtgesuch (RecordRide, ID "ride:<zeit><zufall>").
type driveRide struct {
	ID           string      `json:"id"`
	CustomerFID  string      `json:"customer_fid"`
	CustomerName string      `json:"customer_name"`
	Kind         string      `json:"kind"`          // "person" | "goods"
	Stops        []DriveStop `json:"stops"`         // mindestens Start und Ziel
	DistanceKm   float64     `json:"distance_km"`
	DurationMin  float64     `json:"duration_min"`
	RouteSource  string      `json:"route_source"`  // "osrm" | "luftlinie"
	Goods        DriveGoods  `json:"goods,omitempty"`
	Seats        int         `json:"seats,omitempty"`
	WhenText     string      `json:"when_text"`     // Wunschzeit als Freitext
	Note         string      `json:"note"`
	TargetDriver string      `json:"target_driver"` // leer = öffentlich für alle
	Status       string      `json:"status"`        // "open" | "locked" | "done" | "cancelled"
	DriverFID    string      `json:"driver_fid"`
	DriverName   string      `json:"driver_name,omitempty"` // Anzeigename des Fahrers (R637)
	PriceFND     float64     `json:"price_fnd"`     // vereinbarter Preis beim Annehmen
	// Schätzung beim Einstellen (R639): Damit am Gesuch ein Preis steht, auch
	// bevor ein Fahrer zugesagt hat. Grundlage ist der nächstgelegene Fahrer.
	EstimateFND   float64 `json:"estimate_fnd,omitempty"`
	EstimateName  string  `json:"estimate_name,omitempty"`
	EstimateCount int     `json:"estimate_count,omitempty"`
	// Bezahlung (R636)
	PaymentMode  string      `json:"payment_mode,omitempty"` // "escrow" | "cash"
	EscrowID     string      `json:"escrow_id,omitempty"`
	PaidFND      float64     `json:"paid_fnd,omitempty"`
	PaidAt       *time.Time  `json:"paid_at,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	LockedAt     *time.Time  `json:"locked_at,omitempty"`
	DoneAt       *time.Time  `json:"done_at,omitempty"`
}

// ── Preisberechnung ─────────────────────────────────────────────────────────

// drivePrice rechnet den Fahrpreis aus. anfahrtKm ist die Strecke vom Standort
// des Fahrers zum ersten Halt; sie zählt doppelt, wenn der Fahrer auch die
// Abfahrt berechnet – deshalb der Faktor im Aufruf.
func drivePrice(d *driveDriver, strecke, anfahrtKm float64) (gesamt, fahrt, anfahrt float64) {
	fahrt = strecke * d.PerKmFND
	anfahrt = d.ApproachFlatFND + anfahrtKm*d.ApproachPerKm
	gesamt = fahrt + anfahrt
	return rund2(gesamt), rund2(fahrt), rund2(anfahrt)
}

func rund2(v float64) float64 { return math.Round(v*100) / 100 }

// ── Routenberechnung ────────────────────────────────────────────────────────

type driveRoute struct {
	DistanceKm  float64 `json:"distance_km"`
	DurationMin float64 `json:"duration_min"`
	Source      string  `json:"source"`
	Geometry    string  `json:"geometry,omitempty"` // Polyline für die Karte
}

// luftlinienRoute: Rückfall ohne Routendienst. Der Faktor 1,25 bildet ab, dass
// Straßen nicht gerade verlaufen – eine Schätzung, klar als solche gekennzeichnet.
func luftlinienRoute(stops []DriveStop) driveRoute {
	var km float64
	for i := 1; i < len(stops); i++ {
		km += geo.HaversineKm(stops[i-1].Lat, stops[i-1].Lon, stops[i].Lat, stops[i].Lon)
	}
	km *= 1.25
	return driveRoute{
		DistanceKm:  rund2(km),
		DurationMin: fahrzeitMin(km, len(stops)),
		Source:      "luftlinie",
	}
}

// fahrzeitMin schätzt die Fahrzeit realistisch (R639).
//
// Vorher wurde pauschal mit 50 km/h gerechnet. Das ist weit daneben, vor allem
// auf kurzen Strecken: 5 km durch den Ort dauern keine 6 Minuten, sondern eher
// eine Viertelstunde. Jetzt hängt die Durchschnittsgeschwindigkeit von der
// Entfernung ab – kurze Fahrten laufen über Ortsstraßen mit Ampeln, lange zum
// guten Teil über Landstraße und Autobahn. Dazu eine Grundzeit fürs Losfahren
// und Parken sowie ein Zuschlag je Zwischenhalt.
func fahrzeitMin(km float64, halte int) float64 {
	var v float64 // mittlere Geschwindigkeit in km/h
	switch {
	case km < 10:
		v = 28
	case km < 50:
		v = 40
	default:
		v = 62
	}
	min := km/v*60 + 5 // 5 Minuten Grundzeit
	if halte > 2 {
		min += float64(halte-2) * 3 // je Zwischenhalt drei Minuten
	}
	return math.Round(min)
}

// osrmRoute fragt einen Routendienst. Fehler sind kein Drama – dann greift die
// Schätzung. Die Adresse ist einstellbar, damit ein eigener Dienst genutzt
// werden kann (FUNDUS_DRIVE_OSRM).
func (s *Server) osrmRoute(ctx context.Context, stops []DriveStop) (driveRoute, error) {
	basis := strings.TrimRight(s.driveOSRMBase(), "/")
	if basis == "" {
		return driveRoute{}, fmt.Errorf("kein Routendienst eingestellt")
	}
	teile := make([]string, 0, len(stops))
	for _, st := range stops {
		teile = append(teile, fmt.Sprintf("%.6f,%.6f", st.Lon, st.Lat)) // OSRM: lon,lat
	}
	url := basis + "/route/v1/driving/" + strings.Join(teile, ";") +
		"?overview=simplified&geometries=polyline&alternatives=false&steps=false"

	rctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return driveRoute{}, err
	}
	req.Header.Set("User-Agent", "FundusNode/Drive")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return driveRoute{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return driveRoute{}, fmt.Errorf("Routendienst antwortete mit %d", resp.StatusCode)
	}
	var out struct {
		Code   string `json:"code"`
		Routes []struct {
			Distance float64 `json:"distance"` // Meter
			Duration float64 `json:"duration"` // Sekunden
			Geometry string  `json:"geometry"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return driveRoute{}, err
	}
	if out.Code != "Ok" || len(out.Routes) == 0 {
		return driveRoute{}, fmt.Errorf("keine Route gefunden")
	}
	r := out.Routes[0]
	// Der Routendienst rechnet mit freier Fahrt: keine Ampeln, kein Verkehr,
	// kein Halten. In der Praxis dauert es länger (R639).
	dauer := r.Duration/60*1.2 + 3
	if len(stops) > 2 {
		dauer += float64(len(stops)-2) * 3 // je Zwischenhalt
	}
	return driveRoute{
		DistanceKm:  rund2(r.Distance / 1000),
		DurationMin: math.Round(dauer),
		Source:      "osrm",
		Geometry:    r.Geometry,
	}, nil
}

// routeFor liefert die Route, mit Rückfall auf die Schätzung.
func (s *Server) routeFor(ctx context.Context, stops []DriveStop) driveRoute {
	if len(stops) < 2 {
		return driveRoute{Source: "luftlinie"}
	}
	if r, err := s.osrmRoute(ctx, stops); err == nil && r.DistanceKm > 0 {
		return r
	} else if err != nil && s.log != nil {
		s.log.Debug("Routendienst nicht erreichbar – Luftlinie wird geschätzt", zap.Error(err))
	}
	return luftlinienRoute(stops)
}

func (s *Server) driveOSRMBase() string {
	if s.cfg != nil && s.cfg.DriveOSRMURL != "" {
		return s.cfg.DriveOSRMURL
	}
	return "https://router.project-osrm.org"
}

// ── Hilfsfunktionen ─────────────────────────────────────────────────────────

func driverFromRecord(rec *storage.Record) *driveDriver {
	if rec == nil || rec.Data == nil {
		return nil
	}
	raw, err := json.Marshal(rec.Data)
	if err != nil {
		return nil
	}
	var d driveDriver
	if json.Unmarshal(raw, &d) != nil {
		return nil
	}
	return &d
}

func rideFromRecord(rec *storage.Record) *driveRide {
	if rec == nil || rec.Data == nil {
		return nil
	}
	raw, err := json.Marshal(rec.Data)
	if err != nil {
		return nil
	}
	var r driveRide
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	if r.ID == "" {
		r.ID = rec.ID
	}
	return &r
}

func toMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// ── Endpunkte ───────────────────────────────────────────────────────────────

func (s *Server) registerDriveRoutes() {
	g := s.router.Group("/api/v1/drive")
	{
		g.GET("/drivers", s.driveListDrivers)
		g.GET("/driver/me", s.driveGetOwnProfile)
		g.PUT("/driver/me", s.driveSaveOwnProfile)
		g.POST("/route", s.driveRoute)
		g.GET("/rides", s.driveListRides)
		g.POST("/rides", s.driveCreateRide)
		g.POST("/rides/:id/accept", s.driveAcceptRide)
		g.POST("/rides/:id/release", s.driveReleaseRide)
		g.POST("/rides/:id/done", s.driveDoneRide)
		g.POST("/rides/:id/cancel", s.driveCancelRide)
		g.POST("/rides/:id/rate",   s.driveRateDriver)
		g.POST("/rides/:id/escrow", s.driveLinkEscrow)
		g.POST("/rides/:id/cash",   s.driveCashPaid)
		g.GET("/rides/:id/payee",   s.driveDriverWallet)
	}
	// Postleitzahl → Koordinaten (offline, aus der Tabelle des Energie-Netzes).
	// Der Browser braucht das zum Umrechnen der eingegebenen Halte.
	s.router.GET("/api/v1/grid/plz/:plz", func(c *gin.Context) {
		lat, lon := plzCentroid(c.Param("plz"))
		if lat == 0 && lon == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Postleitzahl unbekannt"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"lat": lat, "lon": lon})
	})
}

// GET /drivers?lat=&lon=&radius= – Fahrer für die Karte.
func (s *Server) driveListDrivers(c *gin.Context) {
	recs, err := s.store.List(storage.RecordDriver)
	if err != nil {
		s.internalError(c, err)
		return
	}
	lat := parseQueryFloat(c, "lat")
	lon := parseQueryFloat(c, "lon")
	radius := parseQueryFloat(c, "radius")

	out := make([]gin.H, 0, len(recs))
	for _, rec := range recs {
		if rec == nil || rec.DeletedAt != nil {
			continue
		}
		d := driverFromRecord(rec)
		if d == nil || !d.Active || d.FID == "" {
			continue
		}
		eintrag := toMap(d)
		if lat != 0 || lon != 0 {
			dist := geo.HaversineKm(lat, lon, d.Lat, d.Lon)
			if radius > 0 && dist > radius {
				continue
			}
			eintrag["distance_km"] = rund2(dist)
		}
		eintrag["rating"] = s.driverRating(d.FID)
		out = append(out, eintrag)
	}
	sort.Slice(out, func(i, j int) bool {
		di, _ := out[i]["distance_km"].(float64)
		dj, _ := out[j]["distance_km"].(float64)
		return di < dj
	})
	c.JSON(http.StatusOK, gin.H{"drivers": out})
}

// driverRating fasst die Bewertungen eines Fahrers zusammen.
func (s *Server) driverRating(fid string) gin.H {
	recs, err := s.store.List(storage.RecordRating)
	if err != nil {
		return gin.H{"count": 0}
	}
	summe, anzahl := 0.0, 0
	for _, r := range recs {
		if r == nil || r.DeletedAt != nil || r.Data == nil {
			continue
		}
		if sub, _ := r.Data["subject"].(string); !strings.EqualFold(sub, fid) {
			continue
		}
		if st, ok := toFloatOK(r.Data["stars"]); ok && st >= 1 && st <= 5 {
			summe += st
			anzahl++
		}
	}
	if anzahl == 0 {
		return gin.H{"count": 0}
	}
	return gin.H{"count": anzahl, "avg": math.Round(summe/float64(anzahl)*10) / 10}
}

func (s *Server) driveGetOwnProfile(c *gin.Context) {
	fid := s.sessionFID(c)
	if fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	rec, err := s.store.Get(storage.RecordDriver, "driver:"+fid)
	if err != nil || rec == nil {
		c.JSON(http.StatusOK, gin.H{"driver": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"driver": driverFromRecord(rec)})
}

func (s *Server) driveSaveOwnProfile(c *gin.Context) {
	fid := s.sessionFID(c)
	if fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var d driveDriver
	if err := c.ShouldBindJSON(&d); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	d.FID = fid // nicht aus dem Körper übernehmbar
	// Name immer aus dem Verzeichnis (R637): Der Browser schickte ihn leer, und
	// in der Fahrerliste stand dann nur die gekürzte Kennung.
	d.Name = s.displayNameFor(fid)
	d.UpdatedAt = time.Now()
	if d.PerKmFND < 0 || d.ApproachFlatFND < 0 || d.ApproachPerKm < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Preise dürfen nicht negativ sein."})
		return
	}
	// Standort: wenn nicht gesetzt, aus der Postleitzahl ableiten.
	if d.Lat == 0 && d.Lon == 0 && d.PLZ != "" {
		d.Lat, d.Lon = plzCentroid(d.PLZ)
	}
	rec := &storage.Record{
		ID:        "driver:" + fid,
		Type:      storage.RecordDriver,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Data:      toMap(d),
	}
	if s.node != nil {
		rec.OwnerID = s.node.ID().String()
		if sig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = sig
		}
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	s.drivePublish(rec)
	c.JSON(http.StatusOK, gin.H{"ok": true, "driver": d})
}

// POST /route – Strecke für eine Liste von Halten.
func (s *Server) driveRoute(c *gin.Context) {
	var req struct {
		Stops []DriveStop `json:"stops"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Stops) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Mindestens Start und Ziel angeben."})
		return
	}
	if len(req.Stops) > 12 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Höchstens 12 Halte."})
		return
	}
	r := s.routeFor(c.Request.Context(), req.Stops)
	c.JSON(http.StatusOK, r)
}

// GET /rides – offene Gesuche plus die eigenen (als Kunde oder Fahrer).
func (s *Server) driveListRides(c *gin.Context) {
	recs, err := s.store.List(storage.RecordRide)
	if err != nil {
		s.internalError(c, err)
		return
	}
	me := s.sessionFID(c)
	out := make([]gin.H, 0, len(recs))
	for _, rec := range recs {
		if rec == nil || rec.DeletedAt != nil {
			continue
		}
		r := rideFromRecord(rec)
		if r == nil {
			continue
		}
		// Sichtbar: alle offenen Gesuche (jeder kann Fahrer sein) sowie alles,
		// woran man selbst beteiligt ist.
		beteiligt := me != "" && (strings.EqualFold(r.CustomerFID, me) || strings.EqualFold(r.DriverFID, me) ||
			strings.EqualFold(r.TargetDriver, me))
		if r.Status != "open" && !beteiligt {
			continue
		}
		// Gezielte Anfragen sieht nur der angefragte Fahrer (und der Kunde).
		if r.TargetDriver != "" && !beteiligt {
			continue
		}
		eintrag := toMap(r)
		eintrag["mine"] = me != "" && strings.EqualFold(r.CustomerFID, me)
		eintrag["driving"] = me != "" && strings.EqualFold(r.DriverFID, me)
		out = append(out, eintrag)
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["created_at"]) > fmt.Sprint(out[j]["created_at"])
	})
	c.JSON(http.StatusOK, gin.H{"rides": out})
}

func (s *Server) driveCreateRide(c *gin.Context) {
	fid := s.sessionFID(c)
	if fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var r driveRide
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(r.Stops) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Mindestens Start und Ziel angeben."})
		return
	}
	if len(r.Stops) > 12 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Höchstens 12 Halte."})
		return
	}
	if r.Kind != "goods" {
		r.Kind = "person"
	}
	// Strecke immer SELBST berechnen – nicht aus dem Körper übernehmen.
	route := s.routeFor(c.Request.Context(), r.Stops)
	r.DistanceKm, r.DurationMin, r.RouteSource = route.DistanceKm, route.DurationMin, route.Source

	r.CustomerFID = fid
	r.CustomerName = s.displayNameFor(fid)
	r.Status = "open"
	r.DriverFID = ""
	r.PriceFND = 0
	r.CreatedAt = time.Now()
	r.ID = "ride:" + fmt.Sprintf("%d", time.Now().UnixNano())
	r.EstimateFND, r.EstimateName, r.EstimateCount = s.schaetzePreis(r.Stops[0], r.DistanceKm)

	rec := &storage.Record{
		ID:        r.ID,
		Type:      storage.RecordRide,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Data:      toMap(r),
	}
	if s.node != nil {
		rec.OwnerID = s.node.ID().String()
		if sig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = sig
		}
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	s.drivePublish(rec)
	c.JSON(http.StatusOK, gin.H{"ok": true, "ride": r})
}

// POST /rides/:id/accept – Fahrer nimmt an; das Gesuch wird gesperrt.
func (s *Server) driveAcceptRide(c *gin.Context) {
	fid := s.sessionFID(c)
	if fid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	driveLockMu.Lock()
	defer driveLockMu.Unlock()

	rec, r, err := s.driveLoadRide(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if r.Status != "open" {
		c.JSON(http.StatusConflict, gin.H{
			"error": "Dieses Gesuch ist bereits vergeben."})
		return
	}
	if strings.EqualFold(r.CustomerFID, fid) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Das eigene Gesuch kann man nicht annehmen."})
		return
	}
	if r.TargetDriver != "" && !strings.EqualFold(r.TargetDriver, fid) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Dieses Gesuch richtet sich an einen anderen Fahrer."})
		return
	}
	// Preis aus dem Fahrerprofil festhalten – damit er später feststeht.
	if drec, derr := s.store.Get(storage.RecordDriver, "driver:"+fid); derr == nil && drec != nil {
		if d := driverFromRecord(drec); d != nil {
			anfahrt := 0.0
			if len(r.Stops) > 0 && (d.Lat != 0 || d.Lon != 0) {
				anfahrt = geo.HaversineKm(d.Lat, d.Lon, r.Stops[0].Lat, r.Stops[0].Lon)
			}
			r.PriceFND, _, _ = drivePrice(d, r.DistanceKm, anfahrt)
		}
	}
	jetzt := time.Now()
	r.Status = "locked"
	r.DriverFID = fid
	r.DriverName = s.displayNameFor(fid) // damit der Kunde sieht, wer fährt
	r.LockedAt = &jetzt
	s.driveSaveRide(rec, r)
	c.JSON(http.StatusOK, gin.H{"ok": true, "ride": r})
}

func (s *Server) driveReleaseRide(c *gin.Context) {
	s.driveChangeStatus(c, func(r *driveRide, me string) error {
		if !strings.EqualFold(r.DriverFID, me) {
			return fmt.Errorf("nur der zugeteilte Fahrer kann freigeben")
		}
		r.Status, r.DriverFID, r.DriverName, r.LockedAt, r.PriceFND = "open", "", "", nil, 0
		return nil
	})
}

func (s *Server) driveDoneRide(c *gin.Context) {
	s.driveChangeStatus(c, func(r *driveRide, me string) error {
		if !strings.EqualFold(r.CustomerFID, me) && !strings.EqualFold(r.DriverFID, me) {
			return fmt.Errorf("nur Kunde oder Fahrer können abschließen")
		}
		jetzt := time.Now()
		r.Status, r.DoneAt = "done", &jetzt
		return nil
	})
}

func (s *Server) driveCancelRide(c *gin.Context) {
	s.driveChangeStatus(c, func(r *driveRide, me string) error {
		if !strings.EqualFold(r.CustomerFID, me) {
			return fmt.Errorf("nur der Kunde kann das Gesuch zurückziehen")
		}
		r.Status = "cancelled"
		return nil
	})
}

func (s *Server) driveChangeStatus(c *gin.Context, fn func(*driveRide, string) error) {
	me := s.sessionFID(c)
	if me == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	driveLockMu.Lock()
	defer driveLockMu.Unlock()
	rec, r, err := s.driveLoadRide(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if err := fn(r, me); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	s.driveSaveRide(rec, r)
	c.JSON(http.StatusOK, gin.H{"ok": true, "ride": r})
}

func (s *Server) driveLoadRide(id string) (*storage.Record, *driveRide, error) {
	rec, err := s.store.Get(storage.RecordRide, id)
	if err != nil || rec == nil || rec.DeletedAt != nil {
		return nil, nil, fmt.Errorf("Gesuch nicht gefunden")
	}
	r := rideFromRecord(rec)
	if r == nil {
		return nil, nil, fmt.Errorf("Gesuch nicht lesbar")
	}
	return rec, r, nil
}

func (s *Server) driveSaveRide(rec *storage.Record, r *driveRide) {
	rec.Data = toMap(r)
	rec.UpdatedAt = time.Now()
	if s.node != nil {
		if sig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = sig
		}
	}
	_ = s.store.Put(rec)
	s.drivePublish(rec)
}

// drivePublish macht eine Änderung sofort im Netz bekannt.
func (s *Server) drivePublish(rec *storage.Record) {
	if s.node == nil {
		return
	}
	data, err := marshalRecord(rec)
	if err != nil {
		return
	}
	topic := "fundus.drive"
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.node.Publish(ctx, topic, data)
	}()
}

// driveLockMu serialisiert das Annehmen: Zwei Fahrer dürfen nicht gleichzeitig
// dasselbe Gesuch sperren.
var driveLockMu sync.Mutex

// plzCentroid: Koordinaten einer Postleitzahl, offline aus der Tabelle des
// Energie-Netzes. Kein externer Dienst nötig.
func plzCentroid(plz string) (float64, float64) {
	return grid.PLZCentroid(strings.TrimSpace(plz))
}

// displayNameFor liefert den hinterlegten Anzeigenamen oder die gekürzte ID.
func (s *Server) displayNameFor(fid string) string {
	if r, ok := s.nameRecordOf(fid); ok && strings.TrimSpace(r.Name) != "" {
		return r.Name
	}
	if len(fid) > 10 {
		return fid[:10] + "…"
	}
	return fid
}

// ── Bewertung eines Fahrers (R635) ──────────────────────────────────────────
// Verankert an einer ABGESCHLOSSENEN Fahrt: Nur wer wirklich gefahren ist bzw.
// mitgefahren ist, kann bewerten – dasselbe Prinzip wie beim Marktplatz, wo die
// Bewertung an eine Hinterlegung auf der Chain gebunden ist.

func (s *Server) driveRateDriver(c *gin.Context) {
	me := s.sessionFID(c)
	if me == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		Stars   int    `json:"stars"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Stars < 1 || req.Stars > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bitte 1 bis 5 Sterne angeben."})
		return
	}
	kommentar := strings.TrimSpace(strings.ReplaceAll(req.Comment, "\r", ""))
	if len([]rune(kommentar)) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Kommentar höchstens 500 Zeichen."})
		return
	}
	_, r, err := s.driveLoadRide(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if r.Status != "done" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Erst nach Abschluss der Fahrt bewertbar."})
		return
	}
	// Wer darf wen bewerten? Kunde bewertet Fahrer, Fahrer bewertet Kunde.
	var subjekt string
	switch {
	case strings.EqualFold(r.CustomerFID, me):
		subjekt = r.DriverFID
	case strings.EqualFold(r.DriverFID, me):
		subjekt = r.CustomerFID
	default:
		c.JSON(http.StatusForbidden, gin.H{"error": "Nur die Beteiligten dieser Fahrt können bewerten."})
		return
	}
	if subjekt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Diese Fahrt hat keinen Gegenpart."})
		return
	}
	data := map[string]any{
		"kind":    "drive",
		"ride_id": r.ID,
		"subject": strings.ToLower(subjekt),
		"author":  strings.ToLower(me),
		"stars":   req.Stars,
		"comment": kommentar,
		"ts":      time.Now().Unix(),
	}
	rec := &storage.Record{
		ID:        "rating-drive-" + strings.TrimPrefix(r.ID, "ride:") + "-" + strings.ToLower(me),
		Type:      storage.RecordRating,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Data:      data,
	}
	if s.node != nil {
		rec.OwnerID = s.node.ID().String()
		if sig, err := s.node.SignData(rec.SigningBytes()); err == nil {
			rec.Signature = sig
		}
	}
	if err := s.store.Put(rec); err != nil {
		s.internalError(c, err)
		return
	}
	s.drivePublish(rec)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ── Bezahlung (R636) ────────────────────────────────────────────────────────
// Zwei Wege, der Kunde entscheidet:
//
//  1. Über die Chain: Der Betrag wird wie im Marktplatz hinterlegt und erst
//     freigegeben, wenn der Kunde die Fahrt bestätigt. Der Kunde legt die
//     Hinterlegung über den bestehenden Escrow-Endpunkt an und verknüpft sie
//     danach hier mit der Fahrt.
//  2. Bar: Der FAHRER quittiert, dass er bar bezahlt wurde. Das ist nur eine
//     Notiz an der Fahrt – Geld fließt dabei nicht über Fundus. Deshalb darf
//     auch nur der Fahrer quittieren: Er ist derjenige, der den Erhalt
//     bestätigen kann.

// POST /rides/:id/escrow – Hinterlegung mit der Fahrt verknüpfen.
func (s *Server) driveLinkEscrow(c *gin.Context) {
	me := s.sessionFID(c)
	if me == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		EscrowID string `json:"escrow_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.EscrowID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Escrow-Kennung fehlt."})
		return
	}
	driveLockMu.Lock()
	defer driveLockMu.Unlock()
	rec, r, err := s.driveLoadRide(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !strings.EqualFold(r.CustomerFID, me) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Nur der Kunde kann bezahlen."})
		return
	}
	if r.DriverFID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Erst wenn ein Fahrer zugesagt hat."})
		return
	}
	r.PaymentMode = "escrow"
	r.EscrowID = strings.ToLower(strings.TrimSpace(req.EscrowID))
	r.PaidFND = r.PriceFND // hinterlegter Betrag, wird nach der Fahrt ausgezahlt
	s.driveSaveRide(rec, r)
	c.JSON(http.StatusOK, gin.H{"ok": true, "ride": r})
}

// POST /rides/:id/cash – der Fahrer quittiert Barzahlung.
func (s *Server) driveCashPaid(c *gin.Context) {
	me := s.sessionFID(c)
	if me == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return
	}
	var req struct {
		AmountFND float64 `json:"amount_fnd"`
	}
	_ = c.ShouldBindJSON(&req)

	driveLockMu.Lock()
	defer driveLockMu.Unlock()
	rec, r, err := s.driveLoadRide(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !strings.EqualFold(r.DriverFID, me) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Nur der Fahrer kann den Erhalt des Bargelds quittieren."})
		return
	}
	jetzt := time.Now()
	r.PaymentMode = "cash"
	r.PaidAt = &jetzt
	if req.AmountFND > 0 {
		r.PaidFND = rund2(req.AmountFND)
	} else {
		r.PaidFND = r.PriceFND
	}
	if r.Status == "locked" {
		r.Status = "done"
		r.DoneAt = &jetzt
	}
	s.driveSaveRide(rec, r)
	if s.log != nil {
		s.log.Info("Barzahlung quittiert", zap.String("ride", r.ID), zap.Float64("fnd", r.PaidFND))
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "ride": r})
}

// driveDriverWallet löst die Wallet-Adresse des Fahrers auf – der Kunde braucht
// sie, um die Hinterlegung anzulegen.
func (s *Server) driveDriverWallet(c *gin.Context) {
	_, r, err := s.driveLoadRide(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if r.DriverFID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Noch kein Fahrer zugeteilt."})
		return
	}
	addr, _, aerr := s.resolvePayee(r.DriverFID)
	if aerr != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "Wallet des Fahrers nicht auffindbar: " + aerr.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"driver_fid": r.DriverFID,
		"wallet":     addr.Hex(),
		"price_fnd":  r.PriceFND,
	})
}

// schaetzePreis rechnet aus, was die Fahrt beim nächstgelegenen Fahrer kosten
// würde (R639). Zurück kommen Betrag, dessen Name und wie viele Fahrer mit
// hinterlegten Preisen es überhaupt gibt.
func (s *Server) schaetzePreis(start DriveStop, km float64) (float64, string, int) {
	recs, err := s.store.List(storage.RecordDriver)
	if err != nil {
		return 0, "", 0
	}
	type kandidat struct {
		d    *driveDriver
		dist float64
	}
	var liste []kandidat
	for _, rec := range recs {
		if rec == nil || rec.DeletedAt != nil {
			continue
		}
		d := driverFromRecord(rec)
		if d == nil || !d.Active || d.PerKmFND <= 0 {
			continue
		}
		dist := 0.0
		if (d.Lat != 0 || d.Lon != 0) && (start.Lat != 0 || start.Lon != 0) {
			dist = geo.HaversineKm(d.Lat, d.Lon, start.Lat, start.Lon)
			// Außerhalb des Einsatzgebiets kommt er ohnehin nicht.
			if d.RadiusKm > 0 && dist > d.RadiusKm {
				continue
			}
		}
		liste = append(liste, kandidat{d, dist})
	}
	if len(liste) == 0 {
		return 0, "", 0
	}
	sort.Slice(liste, func(i, j int) bool { return liste[i].dist < liste[j].dist })
	n := liste[0]
	gesamt, _, _ := drivePrice(n.d, km, n.dist)
	return gesamt, n.d.Name, len(liste)
}
