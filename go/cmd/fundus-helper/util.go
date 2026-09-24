package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"time"
)

// lookupFundusGID ermittelt die numerische GID des fundus-Users, damit der Socket
// der fundus-Gruppe zugeordnet werden kann (nur der Node darf verbinden).
func lookupFundusGID() (int, error) {
	u, err := user.Lookup(fundusUserName)
	if err != nil {
		return 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, err
	}
	return gid, nil
}

// listenUnix legt den Unix-Domain-Socket an und setzt Besitzer/Rechte so, dass
// nur root (Eigentümer) und die fundus-Gruppe zugreifen dürfen (0660). Damit
// kann ausschließlich der Node — der als fundus läuft — Anfragen stellen.
func listenUnix(path string, gid int) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Rechte 0660: rw für Eigentümer (root) und Gruppe (fundus), nichts für andere.
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("chmod Socket: %w", err)
	}
	if gid > 0 {
		if err := os.Chown(path, 0, gid); err != nil {
			ln.Close()
			return nil, fmt.Errorf("chown Socket auf fundus-Gruppe: %w", err)
		}
	}
	return ln, nil
}

// runCmd führt ein externes Kommando mit fixem Pfad und getrennten Argumenten
// aus (KEINE Shell → keine Command-Injection). Timeout verhindert Hänger.
// Gibt kombinierte stdout+stderr als String zurück.
func runCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return buf.String(), fmt.Errorf("Zeitüberschreitung")
	}
	return buf.String(), err
}

// parseLsblkLine parst eine Zeile im lsblk -P Format: KEY="value" KEY="value" …
// Werte können Leerzeichen enthalten, daher wird auf die Anführungszeichen geachtet.
func parseLsblkLine(line string) map[string]string {
	out := make(map[string]string)
	i := 0
	for i < len(line) {
		// Key bis '='
		eq := strings.IndexByte(line[i:], '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(line[i : i+eq])
		i += eq + 1
		if i >= len(line) || line[i] != '"' {
			break
		}
		i++ // öffnendes "
		// Wert bis schließendes "
		end := strings.IndexByte(line[i:], '"')
		if end < 0 {
			break
		}
		val := line[i : i+end]
		i += end + 1
		out[key] = val
		// Leerzeichen bis zum nächsten Key
		for i < len(line) && line[i] == ' ' {
			i++
		}
	}
	return out
}

// splitNmcli zerlegt eine nmcli -t Zeile an ':' und beachtet die von nmcli
// verwendete Maskierung '\:' für Doppelpunkte innerhalb von Feldern.
func splitNmcli(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) && line[i+1] == ':' {
			cur.WriteByte(':')
			i++
			continue
		}
		if c == ':' {
			fields = append(fields, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	fields = append(fields, cur.String())
	return fields
}
