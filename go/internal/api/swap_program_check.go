package api

// Kein Handel ohne funktionierendes Solana-Programm.
//
// Ein Pi mit falscher FUNDUS_SWAP_HTLC_PROGRAM (z.B. die alte Test-ID) nahm
// Orders an und begann Swaps – und suchte die Sperre der Gegenseite dann an
// einer ganz anderen Kontoadresse (die Adresse hängt von der Programm-ID ab).
// Jetzt: Orders anlegen, annehmen und Swap-Anfragen nur, wenn das eingestellte
// Programm im Netz gefunden wird und ausführbar ist (Ergebnis 10 min gepuffert).

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

var (
	progCheckMu  sync.Mutex
	progCheckAt  time.Time
	progCheckErr error
)

// solProgramReady: nil, wenn das HTLC-Programm erreichbar und ausführbar ist.
func (s *Server) solProgramReady(ctx context.Context) error {
	if s.swapMgr == nil || s.swapMgr.htlcProgramID == "" {
		return fmt.Errorf("Solana-Swaps sind auf diesem Node nicht eingerichtet (FUNDUS_SWAP_HTLC_PROGRAM leer)")
	}
	progCheckMu.Lock()
	if !progCheckAt.IsZero() && time.Since(progCheckAt) < 10*time.Minute && progCheckErr == nil {
		progCheckMu.Unlock()
		return nil // gepuffert: zuletzt in Ordnung
	}
	progCheckMu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var err error
	res, rerr := s.swapMgr.solanaRPCCall(cctx, "getAccountInfo", []interface{}{
		s.swapMgr.htlcProgramID, map[string]interface{}{"encoding": "base64"},
	})
	if rerr != nil {
		err = fmt.Errorf("Solana-Programm nicht prüfbar: %s", solRPCErr(rerr, s.swapMgr.rpcURL()).Error())
	} else {
		var acc struct {
			Value *struct {
				Executable bool `json:"executable"`
			} `json:"value"`
		}
		_ = json.Unmarshal(res, &acc)
		if acc.Value == nil || !acc.Value.Executable {
			err = fmt.Errorf("Solana-Programm %s… ist im eingestellten Netz (%s) nicht vorhanden – "+
				"FUNDUS_SWAP_HTLC_PROGRAM in /etc/fundus/fundus.env prüfen", s.swapMgr.htlcProgramID[:8], s.solNetName())
		}
	}
	progCheckMu.Lock()
	progCheckAt, progCheckErr = time.Now(), err
	progCheckMu.Unlock()
	return err
}
