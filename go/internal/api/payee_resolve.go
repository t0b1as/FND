package api

// Schutz vor Fehlüberweisungen an eine Fundus-ID.
//
// Eine Fundus-ID (Messenger/Kontakt, aus dem Ed25519-Schlüssel) und eine
// Wallet-Adresse (FND auf der Chain, aus dem secp256k1-Schlüssel) haben
// DASSELBE Format: 0x + 40 Hex-Zeichen. FND an eine Fundus-ID wären
// unwiderruflich verloren – zu ihr gibt es keinen Chain-Schlüssel.
//
// resolvePayee erkennt Fundus-IDs über das Schlüsselverzeichnis und übersetzt
// sie in die Wallet-Adresse derselben Person. Ist keine Wallet-Adresse
// bekannt, wird abgelehnt statt Geld zu verbrennen.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fundus/node/internal/chain"
	"github.com/fundus/node/internal/storage"
)

// resolvePayee liefert die Chain-Adresse für eine eingegebene Empfänger-
// Adresse. note ist nicht leer, wenn eine Fundus-ID übersetzt wurde.
func (s *Server) resolvePayee(in string) (addr chain.Address, note string, err error) {
	low := strings.ToLower(strings.TrimSpace(in))
	a, ok := chain.AddressFromHex(low)
	if !ok {
		return a, "", errors.New("ungültige Adresse (0x + 40 Hex-Zeichen)")
	}
	if s.store == nil {
		return a, "", nil
	}
	rec, gerr := s.store.Get(storage.RecordKeyDir, "keydir:"+low)
	if gerr != nil || rec == nil {
		return a, "", nil // unbekannt – als Wallet-Adresse verwenden
	}
	fid, _ := rec.Data["fundus_id"].(string)
	if !strings.EqualFold(fid, low) {
		return a, "", nil // Eintrag unter einer Wallet-Adresse → ist eine Wallet-Adresse
	}
	// low ist eine Fundus-ID → Wallet-Adresse derselben Person suchen.
	wallet, _ := rec.Data["wallet_address"].(string)
	if wallet == "" {
		if recs, lerr := s.store.List(storage.RecordKeyDir); lerr == nil {
			for _, r := range recs {
				if r == nil || r.ID == "keydir:"+low {
					continue
				}
				if f, _ := r.Data["fundus_id"].(string); strings.EqualFold(f, low) {
					wallet = strings.TrimPrefix(r.ID, "keydir:")
					break
				}
			}
		}
	}
	if wa, ok := chain.AddressFromHex(strings.ToLower(wallet)); ok && wallet != "" && !strings.EqualFold(wallet, low) {
		return wa, fmt.Sprintf("Fundus-ID %s… in die Wallet-Adresse %s übersetzt", low[:10], strings.ToLower(wallet)), nil
	}
	return a, "", fmt.Errorf("%s ist eine Fundus-ID (Messenger/Kontakt), keine Wallet-Adresse – FND dorthin wären verloren. "+
		"Bitte die Wallet-Adresse des Empfängers verwenden (sie steht auf seiner Wallet-Seite)", low)
}
