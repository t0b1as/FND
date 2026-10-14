package api

// Fahrten von einem ANDEREN Node ändern (R644).
//
// Ein Fahrtgesuch gehört dem Node, auf dem der Kunde es eingestellt hat, und
// ist von diesem signiert. Nimmt ein Fahrer auf einem anderen Node an, schrieb
// dieser die Änderung bisher lokal und signierte mit SEINEM Schlüssel – die
// Fassung trug damit die falsche Unterschrift und wurde beim Abgleich überall
// verworfen. Beim Fahrer sah es richtig aus, beim Kunden kam nie etwas an.
//
// Gleiche Lösung wie bei Angeboten (R611): Die Änderung geht an den
// Besitzer-Node, der prüft die Unterschrift des Absenders, wendet sie an,
// signiert neu und verteilt. Nur dort entsteht eine gültige Fassung.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fundus/node/internal/identity"
	"github.com/fundus/node/internal/storage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const driveActionProtocol = "/fundus/drive-action/1.0.0"

type driveActionMsg struct {
	RideID string         `json:"ride_id"`
	FID    string         `json:"fid"`
	Action string         `json:"action"`
	Data   map[string]any `json:"data,omitempty"`
	TS     int64          `json:"ts"`
	Sig    string         `json:"sig"`
	PubKey string         `json:"pubkey"`
}

func driveActionSigningBytes(m *driveActionMsg) []byte {
	raw, _ := json.Marshal(m.Data)
	return []byte(strings.ToLower(m.RideID) + "|" + strings.ToLower(m.FID) + "|" +
		m.Action + "|" + fmt.Sprint(m.TS) + "|" + string(raw))
}

// driveIsOwner sagt, ob dieser Node die Fahrt verwalten darf.
func (s *Server) driveIsOwner(rec *storage.Record) bool {
	return rec == nil || rec.OwnerID == "" || rec.OwnerID == s.nodeID()
}

// driveForward reicht eine Aktion an den Besitzer-Node weiter und beantwortet
// die HTTP-Anfrage. Rückgabe true heißt: erledigt, der Aufrufer ist fertig.
func (s *Server) driveForward(c *gin.Context, rec *storage.Record, action string, daten map[string]any) bool {
	if s.driveIsOwner(rec) {
		return false // gehört uns: normal weiterverarbeiten
	}
	sess := s.getSession(c)
	if sess == nil || sess.identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Bitte zuerst anmelden."})
		return true
	}
	msg := &driveActionMsg{
		RideID: rec.ID,
		FID:    strings.ToLower(sess.identity.FundusID),
		Action: action,
		Data:   daten,
		TS:     time.Now().Unix(),
		PubKey: sess.identity.PublicKeyHex,
	}
	sig, err := sess.identity.Sign(driveActionSigningBytes(msg))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unterschrift fehlgeschlagen"})
		return true
	}
	msg.Sig = sig
	payload, err := json.Marshal(msg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return true
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	antwort, err := s.node.SendAndReceive(ctx, rec.OwnerID, driveActionProtocol, payload)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "Der Node des Kunden ist gerade nicht erreichbar: " + err.Error()})
		return true
	}
	var res struct {
		OK    bool           `json:"ok"`
		Error string         `json:"error"`
		Ride  map[string]any `json:"ride"`
	}
	if json.Unmarshal(antwort, &res) != nil || !res.OK {
		grund := res.Error
		if grund == "" {
			grund = "abgelehnt"
		}
		c.JSON(http.StatusConflict, gin.H{"error": grund})
		return true
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "ride": res.Ride, "forwarded": true})
	return true
}

// registerDriveActionProtocol nimmt Aktionen anderer Nodes entgegen.
func (s *Server) registerDriveActionProtocol() {
	if s.node == nil {
		return
	}
	s.node.RegisterProtocol(driveActionProtocol, func(peerID string, data []byte) []byte {
		fehler := func(grund string) []byte {
			raw, _ := json.Marshal(map[string]any{"ok": false, "error": grund})
			return raw
		}
		var m driveActionMsg
		if json.Unmarshal(data, &m) != nil || m.RideID == "" || m.FID == "" || m.Sig == "" {
			return fehler("unvollständig")
		}
		if d := time.Since(time.Unix(m.TS, 0)); d > maxEditAge || d < -maxEditAge {
			return fehler("Nachricht zu alt")
		}
		// Öffentlichen Schlüssel aus dem VERZEICHNIS, nicht aus der Nachricht.
		pub := ""
		if kd, e := s.store.Get(storage.RecordKeyDir, "keydir:"+strings.ToLower(m.FID)); e == nil && kd != nil && kd.Data != nil {
			pub, _ = kd.Data["ed25519"].(string)
		}
		if pub == "" {
			pub = m.PubKey
		}
		if !identity.Verify(driveActionSigningBytes(&m), m.Sig, m.FID, pub) {
			return fehler("Unterschrift ungültig")
		}

		driveLockMu.Lock()
		defer driveLockMu.Unlock()
		rec, r, err := s.driveLoadRide(m.RideID)
		if err != nil {
			return fehler(err.Error())
		}
		if !s.driveIsOwner(rec) {
			return fehler("diese Fahrt gehört einem anderen Node")
		}
		if err := s.driveApplyAction(r, strings.ToLower(m.FID), m.Action, m.Data); err != nil {
			return fehler(err.Error())
		}
		s.driveSaveRide(rec, r)
		s.driveNotify(r, driveEreignisFuer(m.Action, r))
		if s.log != nil {
			s.log.Info("Fahrt von anderem Node geändert",
				zap.String("ride", m.RideID), zap.String("aktion", m.Action), zap.String("peer", peerID))
		}
		raw, _ := json.Marshal(map[string]any{"ok": true, "ride": toMap(r)})
		return raw
	})
}

func driveEreignisFuer(action string, r *driveRide) string {
	switch action {
	case "accept":
		return "angenommen"
	case "release":
		return "freigegeben"
	case "done":
		return "abgeschlossen"
	case "cash", "paid":
		return "bezahlt"
	case "escrow":
		return "hinterlegt"
	case "cancel":
		return "zurückgezogen"
	case "position":
		return "position"
	}
	return action
}

// driveApplyAction wendet eine Aktion an – dieselben Regeln wie über HTTP,
// damit lokaler und entfernter Weg nicht auseinanderlaufen.
func (s *Server) driveApplyAction(r *driveRide, fid, action string, daten map[string]any) error {
	jetzt := time.Now()
	zahl := func(k string) float64 {
		if daten == nil {
			return 0
		}
		v, _ := toFloatOK(daten[k])
		return v
	}
	text := func(k string) string {
		if daten == nil {
			return ""
		}
		v, _ := daten[k].(string)
		return v
	}

	switch action {
	case "accept":
		if r.Status != "open" {
			return fmt.Errorf("Dieses Gesuch ist bereits vergeben.")
		}
		if strings.EqualFold(r.CustomerFID, fid) {
			return fmt.Errorf("Das eigene Gesuch kann man nicht annehmen.")
		}
		if r.TargetDriver != "" && !strings.EqualFold(r.TargetDriver, fid) {
			return fmt.Errorf("Dieses Gesuch richtet sich an einen anderen Fahrer.")
		}
		r.Status, r.DriverFID, r.LockedAt = "locked", fid, &jetzt
		r.DriverName = s.displayNameFor(fid)
		r.PriceFND = zahl("price_fnd")
		if m := zahl("eta_min"); m > 0 {
			r.EtaMin = m
			an := jetzt.Add(time.Duration(m) * time.Minute)
			r.EtaAt = &an
		}
	case "release":
		if !strings.EqualFold(r.DriverFID, fid) {
			return fmt.Errorf("nur der zugeteilte Fahrer kann freigeben")
		}
		r.Status, r.DriverFID, r.DriverName, r.LockedAt, r.PriceFND = "open", "", "", nil, 0
		r.EtaMin, r.EtaAt, r.DriverLat, r.DriverLon, r.DriverSeenAt = 0, nil, 0, 0, nil
	case "done":
		if !strings.EqualFold(r.CustomerFID, fid) && !strings.EqualFold(r.DriverFID, fid) {
			return fmt.Errorf("nur Kunde oder Fahrer können abschließen")
		}
		r.Status, r.DoneAt = "done", &jetzt
	case "cancel":
		if !strings.EqualFold(r.CustomerFID, fid) {
			return fmt.Errorf("nur der Kunde kann das Gesuch zurückziehen")
		}
		r.Status = "cancelled"
	case "cash":
		if !strings.EqualFold(r.DriverFID, fid) {
			return fmt.Errorf("nur der Fahrer kann Bargeld quittieren")
		}
		r.PaymentMode, r.PaidAt = "cash", &jetzt
		if b := zahl("amount_fnd"); b > 0 {
			r.PaidFND = rund2(b)
		} else {
			r.PaidFND = r.PriceFND
		}
		if r.Status == "locked" {
			r.Status, r.DoneAt = "done", &jetzt
		}
	case "paid":
		if !strings.EqualFold(r.CustomerFID, fid) {
			return fmt.Errorf("nur der Kunde kann die Zahlung vermerken")
		}
		r.PaymentMode, r.PaidAt = "direct", &jetzt
		r.PaidFND = rund2(zahl("amount_fnd"))
		if r.PaidFND <= 0 {
			r.PaidFND = r.PriceFND
		}
		r.EscrowID = text("tx")
	case "escrow":
		if !strings.EqualFold(r.CustomerFID, fid) {
			return fmt.Errorf("nur der Kunde kann bezahlen")
		}
		r.PaymentMode = "escrow"
		r.EscrowID = strings.ToLower(strings.TrimSpace(text("escrow_id")))
		r.PaidFND = r.PriceFND
	case "position":
		if !strings.EqualFold(r.DriverFID, fid) {
			return fmt.Errorf("nur der zugeteilte Fahrer kann seinen Standort melden")
		}
		if r.Status != "locked" {
			return fmt.Errorf("nur während einer laufenden Fahrt")
		}
		r.DriverLat, r.DriverLon, r.DriverSeenAt = zahl("lat"), zahl("lon"), &jetzt
		if m := zahl("eta_min"); m > 0 {
			r.EtaMin = m
			an := jetzt.Add(time.Duration(m) * time.Minute)
			r.EtaAt = &an
		}
	default:
		return fmt.Errorf("unbekannte Aktion")
	}
	return nil
}
