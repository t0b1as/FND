package chain

import (
	"bytes"
	"math/big"
	"sort"
)

// KontoStand: ein Konto auf der Chain, für Übersichten (R932). Nur lesend –
// berührt weder Konsens noch Zustandswurzel.
type KontoStand struct {
	Adresse string `json:"adresse"`
	Saldo   string `json:"saldo"` // uFND
	Stake   string `json:"stake"` // uFND, aktiv gebunden
	Nonce   uint64 `json:"nonce"` // Anzahl gesendeter Transaktionen
}

// AlleKonten listet jedes Konto mit Guthaben, Stake oder je gesendeter
// Transaktion – sortiert nach Guthaben (absteigend).
func (bc *Blockchain) AlleKonten() []KontoStand {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.state == nil {
		return nil
	}
	s := bc.state
	null := new(big.Int)
	adressen := map[Address]bool{}
	for a := range s.accounts {
		adressen[a] = true
	}
	for a := range s.stakes {
		adressen[a] = true
	}
	type zeile struct {
		a     Address
		saldo *big.Int
		stake *big.Int
		nonce uint64
	}
	liste := make([]zeile, 0, len(adressen))
	for a := range adressen {
		z := zeile{a: a, saldo: new(big.Int), stake: new(big.Int)}
		if acct, ok := s.accounts[a]; ok && acct != nil {
			if acct.Balance != nil {
				z.saldo.Set(acct.Balance)
			}
			z.nonce = acct.Nonce
		}
		if r, ok := s.stakes[a]; ok && r != nil && r.Amount != nil {
			z.stake.Set(r.Amount)
		}
		if z.saldo.Cmp(null) == 0 && z.stake.Cmp(null) == 0 && z.nonce == 0 {
			continue
		}
		liste = append(liste, z)
	}
	sort.Slice(liste, func(i, j int) bool {
		if c := liste[i].saldo.Cmp(liste[j].saldo); c != 0 {
			return c > 0
		}
		return bytes.Compare(liste[i].a[:], liste[j].a[:]) < 0
	})
	out := make([]KontoStand, len(liste))
	for i, z := range liste {
		out[i] = KontoStand{Adresse: z.a.Hex(), Saldo: z.saldo.String(), Stake: z.stake.String(), Nonce: z.nonce}
	}
	return out
}
