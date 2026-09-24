package api

// API-Endpunkte, die privilegierte System-Operationen über den fundus-helper
// (root-Daemon) auslösen: externe Laufwerke mounten und WLAN wechseln. Der Node
// selbst läuft unprivilegiert und darf das nicht — er reicht die Anfrage über
// einen Unix-Domain-Socket an den Helper weiter (siehe internal/helperproto).
//
// Alle Handler prüfen zuerst, ob der Helper läuft, und geben sonst eine klare,
// nicht-fatale Meldung zurück (der Helper ist optional).

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/fundus/node/internal/helperproto"
)

// adminHelperStatus meldet, ob der privilegierte Helper erreichbar ist.
func (s *Server) adminHelperStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"available": helperproto.Available()})
}

// helperUnavailable ist die einheitliche Antwort, wenn der Helper nicht läuft.
func helperUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error":     "fundus-helper nicht verfügbar",
		"hint":      "Der privilegierte Hilfsdienst läuft nicht. Mount/WLAN-Funktionen sind deshalb deaktiviert.",
		"available": false,
	})
}

// adminListBlockDevices listet anschließbare Speichergeräte.
func (s *Server) adminListBlockDevices(c *gin.Context) {
	if !helperproto.Available() {
		helperUnavailable(c)
		return
	}
	resp, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionListBlockDevices})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if !resp.OK {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": resp.Error})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "devices": resp.BlockDevices})
}

// adminMountDrive mountet ein Gerät per UUID. Body: {"uuid":"..."}
func (s *Server) adminMountDrive(c *gin.Context) {
	if !helperproto.Available() {
		helperUnavailable(c)
		return
	}
	var body struct {
		UUID string `json:"uuid"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.UUID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "uuid fehlt"})
		return
	}
	resp, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionMountDrive, UUID: body.UUID})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": resp.OK, "error": resp.Error, "mount_point": resp.MountPoint})
}

// adminUnmountDrive hängt ein Gerät wieder aus. Body: {"uuid":"..."}
func (s *Server) adminUnmountDrive(c *gin.Context) {
	if !helperproto.Available() {
		helperUnavailable(c)
		return
	}
	var body struct {
		UUID string `json:"uuid"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.UUID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "uuid fehlt"})
		return
	}
	resp, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionUnmountDrive, UUID: body.UUID})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": resp.OK, "error": resp.Error})
}

// adminListWifi listet sichtbare WLANs.
func (s *Server) adminListWifi(c *gin.Context) {
	if !helperproto.Available() {
		helperUnavailable(c)
		return
	}
	resp, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionListWifi})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if !resp.OK {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": resp.Error})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "networks": resp.WifiNetworks})
}

// adminWifiStatus liefert den aktuellen WLAN-Verbindungsstatus.
func (s *Server) adminWifiStatus(c *gin.Context) {
	if !helperproto.Available() {
		helperUnavailable(c)
		return
	}
	resp, err := helperproto.Do(helperproto.Request{Action: helperproto.ActionWifiStatus})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": resp.OK, "error": resp.Error, "status": resp.WifiStatus})
}

// adminConnectWifi verbindet mit einem WLAN. Body: {"ssid":"...","password":"..."}
// Das Passwort wird nur an den Helper weitergereicht, nie geloggt.
func (s *Server) adminConnectWifi(c *gin.Context) {
	if !helperproto.Available() {
		helperUnavailable(c)
		return
	}
	var body struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.SSID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ssid fehlt"})
		return
	}
	resp, err := helperproto.Do(helperproto.Request{
		Action:   helperproto.ActionConnectWifi,
		SSID:     body.SSID,
		Password: body.Password,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": resp.OK, "error": resp.Error})
}
