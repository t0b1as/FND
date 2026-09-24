package helperproto

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// Available meldet, ob der Helper-Socket existiert und erreichbar ist. Der Node
// kann damit prüfen, ob die Mount-/WLAN-Funktionen überhaupt angeboten werden
// sollen (der Helper ist optional — ohne ihn läuft der Node normal weiter).
func Available() bool {
	c, err := net.DialTimeout("unix", SocketPath, 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Do sendet eine Anfrage an den Helper und liefert die Antwort. Baut die
// Verbindung auf, sendet eine JSON-Zeile, liest eine JSON-Zeile zurück.
func Do(req Request) (*Response, error) {
	req.Version = ProtocolVersion

	conn, err := net.DialTimeout("unix", SocketPath, 3*time.Second)
	if err != nil {
		return nil, errors.New("fundus-helper nicht erreichbar (läuft der Dienst?)")
	}
	defer conn.Close()
	// Updates (Download, Argon2-Prüfung, Entpacken) dauern auf dem Pi Minuten.
	deadline := 60 * time.Second
	if req.Action == ActionApplyUpdate {
		deadline = 20 * time.Minute
	}
	_ = conn.SetDeadline(time.Now().Add(deadline))

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		return nil, err
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Ping prüft, ob der Helper antwortet.
func Ping() bool {
	resp, err := Do(Request{Action: ActionPing})
	return err == nil && resp.OK
}
