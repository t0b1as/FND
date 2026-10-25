package chain

// Buchungen für den Transaktionsindex (R823).
//
// Statt Gebühren, Validator-Anteile und Belohnungen aus den Transaktionsarten
// NACHZURECHNEN – jeder Fehler ergäbe einen falschen Verlauf –, misst die
// Chain, was sie tatsächlich bucht: Beim ersten Zugriff auf ein Konto in einer
// Transaktion merkt sich der Zustand dessen Saldo, am Ende wird verglichen.
// Die Differenz ist exakt die Buchung.
//
// Das läuft nur auf einem eigenen Schattenzustand des Index. Der Zustand der
// Chain wird nie gemessen; ApplyBlock verhält sich unverändert.

import (
	"bytes"
	"math/big"
	"sort"
)

// Buchung ist die Saldoänderung EINER Adresse durch EINE Transaktion (oder
// durch die blockweite Stake-Freigabe, dann TxIndex = -1).
type Buchung struct {
	Hoehe    uint64
	Zeit     uint64
	TxIndex  int
	TxHash   [32]byte
	TxTyp    TxType
	Von      Address  // Absender der Transaktion (Null bei TxIndex -1)
	Gebuehr  *big.Int // Gebühr der Transaktion (nur zur Einordnung)
	Proposer Address
	Adresse  Address
	Betrag   *big.Int // Saldoänderung, vorzeichenbehaftet, in uFND
}

func (s *State) erfassungAbschliessen(blk *Block, txIndex int, tx *Transaction) []Buchung {
	if s.erfassung == nil {
		return nil
	}
	var out []Buchung
	for a, vorher := range s.erfassung {
		d := new(big.Int).Sub(s.getBalance(a), vorher)
		if d.Sign() == 0 {
			continue
		}
		b := Buchung{Hoehe: blk.Header.Height, Zeit: blk.Header.Timestamp, TxIndex: txIndex,
			Proposer: blk.Header.Proposer, Adresse: a, Betrag: d, Gebuehr: new(big.Int)}
		if tx != nil {
			b.TxHash, b.TxTyp, b.Von = tx.Hash(), tx.Type, tx.From
			if tx.Fee != nil {
				b.Gebuehr = new(big.Int).Set(tx.Fee)
			}
		}
		out = append(out, b)
	}
	// Feste Reihenfolge (Kartenreihenfolge ist in Go zufällig).
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Adresse[:], out[j].Adresse[:]) < 0 })
	s.erfassung = nil
	return out
}

// IndexStart liefert einen geprüften Ausgangszustand für den Buchungsindex:
// das jüngste Zustandsabbild bis maxHoehe (gegen die Chain geprüft, siehe
// ladeAbbild), sonst den Zustand aus der Genesis. Ein FRISCHER Zustand – der
// der Chain wird nicht berührt.
func (bc *Blockchain) IndexStart(maxHoehe uint64) (*State, *BlockHeader, error) {
	bc.mu.RLock()
	bruecke := Address{}
	if bc.state != nil {
		bruecke = bc.state.bridgeAuthority
	}
	bc.mu.RUnlock()
	if maxHoehe >= 1 {
		if st, h, err := bc.ladeAbbild(maxHoehe); err == nil && st != nil && h >= 1 {
			if blk, err := bc.loadBlock(h); err == nil {
				st.bridgeAuthority = bruecke
				return st, &blk.Header, nil
			}
		}
	}
	gen, err := bc.loadBlock(0)
	if err != nil {
		return nil, nil, err
	}
	st := NewState()
	for _, a := range gen.GenesisAllocs {
		st.Credit(a.Address, a.Balance)
	}
	// Die Brücken-Berechtigung ist Konfiguration, kein Konsenszustand – ohne
	// sie lehnte der Schattenzustand SOL-Gutschriften ab und bliebe stehen.
	st.bridgeAuthority = bruecke
	return st, &gen.Header, nil
}

// FeeCollectorAdresse: Empfänger der Gebühren, wie ihn ApplyBlock benutzt.
func (bc *Blockchain) FeeCollectorAdresse() Address {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.feeCollector
}
