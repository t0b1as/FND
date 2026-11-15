package api

// https vor der Freischaltung (R910). Ruft ein Gerät, das noch nicht frei-
// geschaltet ist, eine https-Seite auf – etwa eine Suche aus der Adressleiste
// –, lässt sie sich nicht auf die Anmeldeseite umleiten: Dafür fehlt das
// Zertifikat der Seite. Bisher wurde die Verbindung abgelehnt; der Browser
// meldete einen Netzfehler (ERR_INTERNET_DISCONNECTED u. a.).
//
// Jetzt nimmt der Pi diese Verbindungen an (Port 3443, Umleitung durch den
// Wächter) und zeigt ein eigenes Zertifikat. Genau daran erkennen Chrome,
// Firefox und Edge ein Anmeldenetz: Sie prüfen nach einem Zertifikatsfehler
// selbst und bieten „Im WLAN anmelden“ an statt einer Fehlerseite. Wer das
// Zertifikat trotzdem annimmt, wird zur Anmeldeseite weitergeleitet. PayPal
// ist ausgenommen (geht echt nach draußen).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap"
)

const hotspotTLSPort = ":3443"

// hotspotZertifikat: einmal erzeugt, im Datenordner abgelegt (10 Jahre gültig).
func (s *Server) hotspotZertifikat() (tls.Certificate, error) {
	crt, key := s.hotspotDatei("hotspot-tls.crt"), s.hotspotDatei("hotspot-tls.key")
	if c, err := tls.LoadX509KeyPair(crt, key); err == nil {
		return c, nil
	}
	pk, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	sn, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	vorlage := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: "WLAN-Anmeldung", Organization: []string{"Fundus Hotspot"}},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP(hotspotGW)},
	}
	der, err := x509.CreateCertificate(rand.Reader, vorlage, vorlage, &pk.PublicKey, pk)
	if err != nil {
		return tls.Certificate{}, err
	}
	kb, err := x509.MarshalECPrivateKey(pk)
	if err != nil {
		return tls.Certificate{}, err
	}
	_ = os.WriteFile(crt, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	_ = os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

func (s *Server) hotspotTLSStarten() {
	zert, err := s.hotspotZertifikat()
	if err != nil {
		if s.log != nil {
			s.log.Warn("Hotspot: Zertifikat für https nicht erzeugbar", zap.Error(err))
		}
		return
	}
	weiter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if la == nil {
			http.NotFound(w, r)
			return
		}
		if h, _, err := net.SplitHostPort(la.String()); err != nil || h != hotspotGW {
			http.NotFound(w, r) // nur über den Hotspot
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "http://"+hotspotGW+hotspotPortal+"/", http.StatusFound)
	})
	srv := &http.Server{Addr: hotspotTLSPort, Handler: weiter, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{zert}, MinVersion: tls.VersionTLS12}}
	// Abgebrochene Handshakes (Zertifikat abgelehnt) sind hier der Normalfall –
	// nicht ins Protokoll.
	srv.ErrorLog = log.New(io.Discard, "", 0)
	for {
		if err := srv.ListenAndServeTLS("", ""); err != nil && s.log != nil {
			s.log.Warn("Hotspot-https: Port belegt", zap.Error(err))
		}
		time.Sleep(time.Minute)
	}
}
