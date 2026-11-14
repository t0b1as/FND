package api

// Druckdatei für die Druckerei (R889): Die Seite liefert die Karten als
// Einzelseiten 69 × 94 mm (63 × 88 mm + 3 mm Beschnitt), Vorder- und Rückseite
// abwechselnd. Der Node wandelt sie mit Ghostscript nach den Vorgaben um:
//   - PDF/X-1a:2001 mit Ausgabebedingung, Endformat- und Beschnittrahmen,
//   - nur CMYK, kein RGB; Transparenz wird aufgelöst,
//   - voller Unterfarbenersatz: der Grauanteil jeder Farbe geht ins Schwarz –
//     Gesamtfarbauftrag höchstens 200 % (Vorgabe: 250 %), kleine schwarze
//     Schrift wird reines K 100,
//   - Schriften in Pfade umgewandelt.
// Eine Umwandlung zur Zeit, niedrige Priorität, Zeitlimit.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var druckMu sync.Mutex

// druckProfil: CMYK-Profil von Ghostscript für die Ausgabebedingung (Verweise aufgelöst).
func druckProfil() string {
	kandidaten := []string{"/usr/share/color/icc/ghostscript/default_cmyk.icc"}
	if l, _ := filepath.Glob("/usr/share/ghostscript/*/iccprofiles/default_cmyk.icc"); l != nil {
		kandidaten = append(kandidaten, l...)
	}
	for _, k := range kandidaten {
		if p, err := filepath.EvalSymlinks(k); err == nil {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

const druckPDFX = `%%!
%% PDF/X-1a:2001 für Fundus – Ausgabebedingung mit dem CMYK-Profil von Ghostscript.
[ /GTS_PDFXVersion (PDF/X-1:2001)
  /GTS_PDFXConformance (PDF/X-1a:2001)
  /Title (%s)
  /Trapped /False
/DOCINFO pdfmark
/ICCProfile (%s) def
[/_objdef {icc_PDFX} /type /stream /OBJ pdfmark
[{icc_PDFX} << /N 4 >> /PUT pdfmark
[{icc_PDFX} ICCProfile (r) file /PUT pdfmark
[/_objdef {OutputIntent_PDFX} /type /dict /OBJ pdfmark
[{OutputIntent_PDFX} <<
  /Type /OutputIntent
  /S /GTS_PDFX
  /OutputCondition (CMYK, voller Unterfarbenersatz, Gesamtfarbauftrag hoechstens 200 Prozent)
  /OutputConditionIdentifier (Custom)
  /RegistryName (http://www.color.org)
  /Info (Fundus FLIGHT QUAD 64)
  /DestOutputProfile {icc_PDFX}
>> /PUT pdfmark
[{Catalog} <</OutputIntents [ {OutputIntent_PDFX} ]>> /PUT pdfmark
`

type druckAuftrag struct {
	Status string `json:"status"` // laeuft | fertig | fehler
	Fehler string `json:"fehler,omitempty"`
	Seiten int    `json:"seiten,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	ordner string
	ende   time.Time
}

var (
	druckAuftraege = map[string]*druckAuftrag{}
	druckAuftragMu sync.Mutex
	reDruckID      = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// POST /api/v1/admin/aeronautik/druckdatei?titel=… – Körper: die PDF der Seite.
// Antwortet sofort mit einer Auftragsnummer; die Umwandlung läuft im
// Hintergrund (nginx gibt dem Admin-Bereich nur 120 s – das reicht auf dem Pi
// nicht immer für 128 Seiten mit Fotos).
func (s *Server) quartettDruckdatei(c *gin.Context) {
	gs, err := exec.LookPath("gs")
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Ghostscript fehlt auf " + aeroNodeName() + " – das nächste Update richtet es ein; sofort: sudo apt install ghostscript"})
		return
	}
	profil := druckProfil()
	if profil == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Das CMYK-Profil von Ghostscript fehlt (default_cmyk.icc)."})
		return
	}
	if !druckMu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "Es läuft bereits eine Umwandlung – bitte warten, bis sie fertig ist."})
		return
	}
	freigeben := true
	defer func() {
		if freigeben {
			druckMu.Unlock()
		}
	}()
	druckAufraeumen()
	ordner, err := os.MkdirTemp(s.cfg.DataDir, "druck-")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ein, aus, def := filepath.Join(ordner, "ein.pdf"), filepath.Join(ordner, "aus.pdf"), filepath.Join(ordner, "pdfx.ps")
	f, err := os.Create(ein)
	if err != nil {
		os.RemoveAll(ordner)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, err := io.Copy(f, io.LimitReader(c.Request.Body, 400<<20+1))
	_ = f.Close()
	kopf := make([]byte, 5)
	if fh, e := os.Open(ein); e == nil {
		_, _ = io.ReadFull(fh, kopf)
		_ = fh.Close()
	}
	if err != nil || n == 0 || n > 400<<20 || string(kopf) != "%PDF-" {
		os.RemoveAll(ordner)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Keine PDF-Datei oder größer als 400 MB."})
		return
	}
	titel := strings.Map(func(r rune) rune {
		if r == '(' || r == ')' || r == '\\' || r < 32 || r > 126 {
			return -1
		}
		return r
	}, c.DefaultQuery("titel", "FLIGHT QUAD 64"))
	if err := os.WriteFile(def, []byte(fmt.Sprintf(druckPDFX, titel, profil)), 0o644); err != nil {
		os.RemoveAll(ordner)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	zufall := make([]byte, 8)
	_, _ = rand.Read(zufall)
	id := hex.EncodeToString(zufall) // zufällig: nicht zu erraten, nie doppelt
	auftrag := &druckAuftrag{Status: "laeuft", ordner: ordner}
	druckAuftragMu.Lock()
	druckAuftraege[id] = auftrag
	druckAuftragMu.Unlock()
	freigeben = false // die Umwandlung gibt die Sperre am Ende frei
	go func() {
		defer druckMu.Unlock()
		ctx, abbr := context.WithTimeout(context.Background(), 30*time.Minute)
		defer abbr()
		args := []string{"-n", "19", gs, "-q", "-dBATCH", "-dNOPAUSE", "-dSAFER", "--permit-file-read=" + profil,
			"-dPDFX", "-dCompatibilityLevel=1.3", "-dUseFastColor=true",
			"-sDEVICE=pdfwrite", "-sColorConversionStrategy=CMYK", "-sProcessColorModel=DeviceCMYK",
			"-dNoOutputFonts", "-dPDFXSetBleedBoxToMediaBox=true", "-dAutoRotatePages=/None",
			"-dDownsampleColorImages=false", "-dAutoFilterColorImages=false", "-dColorImageFilter=/DCTEncode", "-dJPEGQ=92",
			"-sOutputFile=" + aus,
			// Endformat- und Beschnittrahmen setzt die Seite selbst (TrimBox 3 mm
			// innen); Ghostscript übernimmt sie unverändert.
			"-f", def,
			// voller Unterfarbenersatz: Grauanteil komplett ins Schwarz
			"-c", "{} settransfer {} setblackgeneration {} setundercolorremoval",
			"-f", ein}
		cmd := exec.CommandContext(ctx, "nice", args...)
		var fehler bytes.Buffer
		cmd.Stderr, cmd.Stdout = &fehler, &fehler
		err := cmd.Run()
		_ = os.Remove(ein)
		druckAuftragMu.Lock()
		defer druckAuftragMu.Unlock()
		auftrag.ende = time.Now()
		if i, e := os.Stat(aus); err == nil && e == nil && i.Size() > 0 {
			auftrag.Status, auftrag.Bytes = "fertig", i.Size()
			return
		}
		meldung := strings.TrimSpace(fehler.String())
		if len(meldung) > 400 {
			meldung = meldung[:400]
		}
		auftrag.Status = "fehler"
		if err != nil {
			auftrag.Fehler = "Ghostscript: " + err.Error() + " " + meldung
		} else {
			auftrag.Fehler = "Ghostscript lieferte keine Datei. " + meldung
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{"id": id})
}

// druckAufraeumen: fertige Aufträge nach einer Stunde löschen.
func druckAufraeumen() {
	druckAuftragMu.Lock()
	defer druckAuftragMu.Unlock()
	for id, a := range druckAuftraege {
		if a.Status != "laeuft" && time.Since(a.ende) > time.Hour {
			os.RemoveAll(a.ordner)
			delete(druckAuftraege, id)
		}
	}
}

// GET /api/v1/admin/aeronautik/druckdatei/:id[?datei=1] – Stand oder fertige Datei.
func (s *Server) quartettDruckStand(c *gin.Context) {
	id := c.Param("id")
	druckAuftragMu.Lock()
	a := druckAuftraege[id]
	var kopie druckAuftrag
	if a != nil {
		kopie = *a
	}
	druckAuftragMu.Unlock()
	if !reDruckID.MatchString(id) || a == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Auftrag unbekannt."})
		return
	}
	if c.Query("datei") != "1" {
		c.JSON(http.StatusOK, kopie)
		return
	}
	if kopie.Status != "fertig" {
		c.JSON(http.StatusConflict, gin.H{"error": "Noch nicht fertig."})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="druckdatei.pdf"`)
	c.File(filepath.Join(kopie.ordner, "aus.pdf"))
}
