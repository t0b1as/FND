package chain

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
)

// StateSchemaVersion versioniert das Serialisierungs-/Root-Format des States.
// Sie fließt als erstes Byte in State.Root() ein und MUSS erhöht werden, sobald
// sich die State-Struktur oder Root-Berechnung ändert (neuer Teilbaum, andere
// Feld-Kodierung o.Ä.). Eine gespeicherte Chain, die mit einer anderen Version
// committet wurde, wird dadurch als inkompatibel erkannt — der Node kann dann
// gezielt migrieren oder (im Testnetz) automatisch zurücksetzen, statt an einem
// kryptischen Root-Mismatch zu scheitern.
//
// Historie:
//   0x01–0x03  frühe Phasen (Konten, Escrows)
//   0x04       + Meter/Commodity-Token-Teilbäume
//   0x05       + redeemedReceipts (Storage-Reward-Anti-Replay, Phase 3)
//   0x06       Fee-Split (Transfer-Gebühr 50/50 Node/Collector) — ändert Salden
//   0x07       + redeemedSolTxs (SOL→FND-Brücke auf native Chain)
//   0x08       + stakes (Validator-Stake mit Unbonding) — R036
// 0x0b (R477): Konsens deterministisch (Gründer + Historie + Stake, keine
// lokale Liste/gelernten Adressen) – erzwingt einen Neustart der Chain auf
// allen Nodes (die alten Chains waren durch abweichende Sets zerfallen).
const StateSchemaVersion byte = 0x0b

// State ist der Weltzustand: Adresse → Account, plus offene Escrows (Spec §7a).
// Phase 1 hielt nur Konten; Escrows kommen als zweiter committeter Teilbaum dazu.
type State struct {
	accounts map[Address]*Account
	escrows  map[[32]byte]*Escrow
	htlcs    map[[32]byte]*HTLC // Hash-Timelock-Contracts (Cross-Chain-Swaps)
	// stakes: gebundener Validator-Stake je Adresse (Spec §5). Eigener
	// committeter Teilbaum, damit Account.encode() unverändert bleibt.
	stakes   map[Address]*StakeRecord
	// slashed verhindert Doppel-Bestrafung: pro Übeltäter-Adresse die Evidence-
	// Höhen, für die bereits geslasht wurde (→ Anwendungs-Höhe). Konsens-relevant.
	slashed  map[Address]map[uint64]uint64
	meters   map[[11]byte]*MeterRecord
	tokens   map[[32]byte]*CommodityToken
	// redeemedReceipts: bereits eingelöste Quittungs-IDs (Anti-Replay für
	// Storage-Reward-Mint). Verhindert, dass dieselbe Quittung mehrfach FND mintet.
	redeemedReceipts map[[32]byte]bool
	// redeemedSolTxs: bereits verarbeitete Solana-Transaktions-Signaturen
	// (Anti-Replay für die SOL→FND-Brücke). Verhindert, dass dieselbe
	// SOL-Einzahlung mehrfach FND gutgeschrieben bekommt. Schlüssel ist der
	// BLAKE3-Hash der Solana-Tx-Signatur.
	redeemedSolTxs map[[32]byte]bool
	// bridgeAuthority: die einzige Adresse, die SOL-Credits einreichen darf.
	// KONFIGURATION, kein konsens-relevanter State → fließt NICHT in den Root
	// ein (sonst würde eine Betreiber-Umstellung die Chain brechen). Muss auf
	// allen Nodes gleich gesetzt sein, damit alle dieselben SolCredits gültig
	// finden. Standard: Null-Adresse = Brücke deaktiviert.
	bridgeAuthority Address
}

// NewState erzeugt einen leeren State.
func NewState() *State {
	return &State{
		accounts: make(map[Address]*Account),
		escrows:  make(map[[32]byte]*Escrow),
		htlcs:    make(map[[32]byte]*HTLC),
		stakes:   make(map[Address]*StakeRecord),
		slashed:  make(map[Address]map[uint64]uint64),
		meters:   make(map[[11]byte]*MeterRecord),
		tokens:   make(map[[32]byte]*CommodityToken),
		redeemedReceipts: make(map[[32]byte]bool),
		redeemedSolTxs:   make(map[[32]byte]bool),
	}
}

// SetBridgeAuthority legt die Adresse fest, die als einzige SOL-Credits
// einreichen darf. Muss auf allen Nodes identisch gesetzt sein. Null-Adresse
// (Standard) = SOL-Brücke deaktiviert.
func (s *State) SetBridgeAuthority(a Address) { s.bridgeAuthority = a }

// getOrCreate liefert den (lebenden) Account-Pointer, legt ihn bei Bedarf an.
func (s *State) getOrCreate(a Address) *Account {
	acct, ok := s.accounts[a]
	if !ok {
		acct = &Account{Balance: new(big.Int)}
		s.accounts[a] = acct
	}
	return acct
}

// GetAccount liefert eine Kopie des Account-Zustands (oder Null-Account).
func (s *State) GetAccount(a Address) Account {
	acct, ok := s.accounts[a]
	if !ok {
		return Account{Balance: new(big.Int), Nonce: 0}
	}
	return Account{Balance: new(big.Int).Set(acct.Balance), Nonce: acct.Nonce}
}

// Balance liefert den Saldo (Kopie) einer Adresse in uFND.
func (s *State) Balance(a Address) *big.Int {
	return new(big.Int).Set(s.getBalance(a))
}

func (s *State) getBalance(a Address) *big.Int {
	if acct, ok := s.accounts[a]; ok {
		return acct.Balance
	}
	return new(big.Int)
}

// Credit erhöht ein Guthaben (für Genesis-Allokationen).
func (s *State) Credit(a Address, amount *big.Int) {
	acct := s.getOrCreate(a)
	acct.Balance.Add(acct.Balance, amount)
}

// ApplyTransaction validiert und wendet eine Transaktion an. feeCollector erhält
// die Gebühr (Phase 1: einfache Sammeladresse; Phase 3 verteilt 50/25/25, Spec §8).
// ApplyTransaction validiert und wendet eine Transaktion an. feeCollector erhält
// die Gebühr (Spec §8). height ist die Blockhöhe, in der die Tx angewandt wird
// (nötig für fristabhängige Vertrags-Aktionen wie Escrow-Refund).
func (s *State) ApplyTransaction(tx *Transaction, feeCollector, producer Address, height uint64) error {
	if err := tx.VerifySignature(); err != nil {
		return err
	}
	switch tx.Type {
	case TxTransfer:
		return s.applyTransfer(tx, feeCollector, producer)
	case TxStake:
		return s.applyStake(tx, feeCollector, producer)
	case TxUnstake:
		return s.applyUnstake(tx, feeCollector, producer, height)
	case TxSlash:
		return s.applySlash(tx, feeCollector, producer, height)
	case TxEscrowOpen:
		return s.applyEscrowOpen(tx, feeCollector, height)
	case TxEscrowConfirm:
		return s.applyEscrowConfirm(tx, feeCollector)
	case TxEscrowRefund:
		return s.applyEscrowRefund(tx, feeCollector, height)
	case TxEscrowDispute:
		return s.applyEscrowDispute(tx)
	case TxJurorVote:
		return s.applyJurorVote(tx, feeCollector)
	case TxEscrowCancel:
		return s.applyEscrowCancel(tx)
	case TxEscrowSubmitReturn:
		return s.applyEscrowSubmitReturn(tx, height)
	case TxEscrowConfirmReturn:
		return s.applyEscrowConfirmReturn(tx, feeCollector, height)
	case TxMeterRegister:
		return s.applyMeterRegister(tx)
	case TxCommodityCertify:
		return s.applyCommodityCertify(tx)
	case TxCommoditySettle:
		return s.applyCommoditySettle(tx)
	case TxStorageReward:
		return s.applyStorageReward(tx)
	case TxSolCredit:
		return s.applySolCredit(tx, s.bridgeAuthority)
	case TxHTLCLock:
		return s.applyHTLCLock(tx, feeCollector, height)
	case TxHTLCClaim:
		return s.applyHTLCClaim(tx, feeCollector)
	case TxHTLCRefund:
		return s.applyHTLCRefund(tx, feeCollector, height)
	default:
		return fmt.Errorf("chain: Tx-Typ 0x%02x nicht unterstützt", byte(tx.Type))
	}
}

func (s *State) applyTransfer(tx *Transaction, feeCollector, producer Address) error {
	p, err := decodeTransferPayload(tx.Payload)
	if err != nil {
		return err
	}
	if p.Amount.Sign() <= 0 {
		return errors.New("chain: Betrag muss > 0 sein")
	}
	if !fitsUint128(p.Amount) || !fitsUint128(tx.Fee) {
		return errors.New("chain: Betrag/Gebühr überschreitet uint128")
	}
	// Gebühr muss exakt 1,8 % betragen (Spec §8).
	want := FeeForValue(p.Amount)
	if tx.Fee == nil || tx.Fee.Cmp(want) != 0 {
		return fmt.Errorf("chain: Gebühr muss %s uFND sein (1,8 %%)", want.String())
	}
	sender := s.getOrCreate(tx.From)
	if sender.Nonce != tx.Nonce {
		return fmt.Errorf("chain: falsche Nonce (erwartet %d, war %d)", sender.Nonce, tx.Nonce)
	}
	total := new(big.Int).Add(p.Amount, tx.Fee)
	if sender.Balance.Cmp(total) < 0 {
		return errors.New("chain: unzureichendes Guthaben")
	}
	// Anwenden — Reihenfolge wichtig, falls Adressen zusammenfallen
	// (getOrCreate liefert denselben Pointer, daher bleibt die Arithmetik korrekt).
	sender.Balance.Sub(sender.Balance, total)
	sender.Nonce++
	to := s.getOrCreate(p.To)
	to.Balance.Add(to.Balance, p.Amount)
	// Gebühr aufteilen: node-Anteil an den Block-Produzenten, Rest an den
	// globalen Fee-Collector. Fällt der Produzent mit dem Collector zusammen
	// (oder ist er null, z.B. Genesis/Testfälle), erhält der Collector alles.
	nodeShare, collectorShare := SplitFee(tx.Fee)
	var zero Address
	if producer != zero && producer != feeCollector {
		prod := s.getOrCreate(producer)
		prod.Balance.Add(prod.Balance, nodeShare)
		fc := s.getOrCreate(feeCollector)
		fc.Balance.Add(fc.Balance, collectorShare)
	} else {
		// Kein separater Produzent → gesamte Gebühr an den Fee-Collector.
		fc := s.getOrCreate(feeCollector)
		fc.Balance.Add(fc.Balance, tx.Fee)
	}
	return nil
}

// Root berechnet die Merkle-Wurzel des States (Spec §3a). Sie committet ZWEI
// Teilbäume mit Domain-Separation: Konten und Escrows. Beide werden über je eine
// eigene Wurzel zusammengefasst, dann verkettet — so kann ein Escrow nie als
// Konto missdeutet werden und umgekehrt.
func (s *State) Root() [32]byte {
	accRoot := s.accountsRoot()
	escRoot := s.escrowsRoot()
	mtrRoot := s.metersRoot()
	tokRoot := s.tokensRoot()
	rdmRoot := s.redeemedRoot()
	solRoot := s.solTxRoot()
	stkRoot := s.stakesRoot()
	slsRoot := s.slashedRoot()
	htlcRoot := s.htlcsRoot()
	// Kombinierte Wurzel: hash(StateSchemaVersion || accRoot || escRoot || ...).
	// Die Version erhöht sich, sobald sich die State-Serialisierung ändert (hier:
	// htlcsRoot neu). So verändert jede Formatänderung den Root eindeutig und
	// Alt-Chains werden als inkompatibel erkannt.
	buf := make([]byte, 0, 1+224)
	buf = append(buf, StateSchemaVersion)
	buf = append(buf, accRoot[:]...)
	buf = append(buf, escRoot[:]...)
	buf = append(buf, mtrRoot[:]...)
	buf = append(buf, tokRoot[:]...)
	buf = append(buf, rdmRoot[:]...)
	buf = append(buf, solRoot[:]...)
	buf = append(buf, stkRoot[:]...)
	buf = append(buf, slsRoot[:]...)
	buf = append(buf, htlcRoot[:]...)
	return chainHash(buf)
}

func (s *State) accountsRoot() [32]byte {
	addrs := make([]Address, 0, len(s.accounts))
	for a, acct := range s.accounts {
		if acct.Balance.Sign() == 0 && acct.Nonce == 0 {
			continue
		}
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return bytes.Compare(addrs[i][:], addrs[j][:]) < 0
	})
	leaves := make([][32]byte, len(addrs))
	for i, a := range addrs {
		acct := s.accounts[a]
		key := a
		leaves[i] = hashLeaf(key[:], acct.encode())
	}
	return merkleRoot(leaves, "fundus-empty-state")
}

func (s *State) escrowsRoot() [32]byte {
	ids := make([][32]byte, 0, len(s.escrows))
	for id, e := range s.escrows {
		if e.State == EscrowClosed {
			continue // geschlossene Escrows werden gepruned (Geld ist geflossen)
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	leaves := make([][32]byte, len(ids))
	for i, id := range ids {
		key := id
		leaves[i] = hashLeaf(key[:], s.escrows[id].encode())
	}
	return merkleRoot(leaves, "fundus-empty-escrows")
}

// htlcsRoot committet die offenen HTLCs (Cross-Chain-Swaps). Eingelöste oder
// zurückgegebene HTLCs werden nicht mehr committet (Geld ist geflossen), bleiben
// aber im Speicher, damit das enthüllte Preimage abfragbar bleibt.
func (s *State) htlcsRoot() [32]byte {
	ids := make([][32]byte, 0, len(s.htlcs))
	for id, h := range s.htlcs {
		if h.State != HTLCLocked {
			continue // eingelöst/zurückgegeben → nicht mehr committen
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	leaves := make([][32]byte, len(ids))
	for i, id := range ids {
		key := id
		leaves[i] = hashLeaf(key[:], s.htlcs[id].encode())
	}
	return merkleRoot(leaves, "fundus-empty-htlcs")
}

// metersRoot committet die Zähler-Registry + zertifizierte Endstände (§7b).
func (s *State) metersRoot() [32]byte {
	ids := make([][11]byte, 0, len(s.meters))
	for id := range s.meters {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	leaves := make([][32]byte, len(ids))
	for i, id := range ids {
		key := id
		leaves[i] = hashLeaf(key[:], s.meters[id].encode())
	}
	return merkleRoot(leaves, "fundus-empty-meters")
}

// tokensRoot committet die aktiven (unsettled) Energie-Tokens. Settled Tokens
// werden GEPRUNED (nicht mehr committet) — die certify-/settle-Tx bleiben in der
// Block-Historie, aber der aktive State wächst nicht unbegrenzt.
// redeemedRoot committet die eingelösten Quittungs-IDs (Anti-Replay für
// Storage-Reward). Deterministisch über sortierte IDs.
func (s *State) redeemedRoot() [32]byte {
	ids := make([][32]byte, 0, len(s.redeemedReceipts))
	for id := range s.redeemedReceipts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	leaves := make([][32]byte, len(ids))
	for i, id := range ids {
		key := id
		leaves[i] = hashLeaf(key[:], []byte{0x01}) // Wert ist konstant (Marker)
	}
	return merkleRoot(leaves, "fundus-empty-redeemed")
}

// solTxRoot committet die bereits verarbeiteten Solana-Tx-Signaturen in den
// State-Root (Anti-Replay der SOL→FND-Brücke, deterministisch sortiert).
func (s *State) solTxRoot() [32]byte {
	ids := make([][32]byte, 0, len(s.redeemedSolTxs))
	for id := range s.redeemedSolTxs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	leaves := make([][32]byte, len(ids))
	for i, id := range ids {
		key := id
		leaves[i] = hashLeaf(key[:], []byte{0x01})
	}
	return merkleRoot(leaves, "fundus-empty-soltx")
}

func (s *State) tokensRoot() [32]byte {
	ids := make([][32]byte, 0, len(s.tokens))
	for id, t := range s.tokens {
		if t.Settled {
			continue // abgerechnete Tokens werden gepruned (wie geschlossene Escrows)
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	leaves := make([][32]byte, len(ids))
	for i, id := range ids {
		key := id
		leaves[i] = hashLeaf(key[:], s.tokens[id].encode())
	}
	return merkleRoot(leaves, "fundus-empty-tokens")
}
