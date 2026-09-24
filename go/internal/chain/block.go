package chain

import (
	"bytes"
	"errors"
	"math/big"
	"sort"
)

// BlockHeader (Spec §5). ValSetHash bleibt in Phase 1 leer (kein Konsens).
// FeeCollector ist die Gebühren-/Autoritätsadresse; im Genesis gesetzt und über
// den Header-Hash tamper-evident. So definiert der (mitgelieferte, hash-
// verifizierte) Genesis die Adresse — keine separate Code-Konstante nötig.
type BlockHeader struct {
	Height       uint64
	PrevHash     [32]byte
	Timestamp    uint64
	Proposer     Address
	FeeCollector Address
	TxRoot       [32]byte
	StateRoot    [32]byte
	ValSetHash   [32]byte
	// Round ist die PoA-Fallback-Runde (0 = primärer Proposer). Steigt, wenn der
	// primäre Proposer nicht rechtzeitig produziert und ein Fallback-Validator
	// einspringt. Wird mitsigniert und gegen den Vorgänger-Zeitstempel geprüft,
	// damit niemand sich durch Behaupten einer hohen Runde vordrängeln kann.
	Round uint64
}

// Block (Spec §5). Commit (Validator-Signaturen) bleibt in Phase 1 leer.
type Block struct {
	Header       BlockHeader
	Transactions []*Transaction
	Commit       [][]byte
	// GenesisAllocs ist nur im Genesis-Block (Höhe 0) gesetzt: die Anfangs-
	// Verteilung, damit der State bei Wiederanlauf rekonstruierbar ist.
	GenesisAllocs []GenesisAccount
}

// bytes ist die kanonische Serialisierung des Headers (feste Reihenfolge).
func (h *BlockHeader) bytes() []byte {
	buf := make([]byte, 0, 8+32+8+AddressLen+AddressLen+32+32+32+8)
	putUint64(&buf, h.Height)
	buf = append(buf, h.PrevHash[:]...)
	putUint64(&buf, h.Timestamp)
	buf = append(buf, h.Proposer[:]...)
	buf = append(buf, h.FeeCollector[:]...)
	buf = append(buf, h.TxRoot[:]...)
	buf = append(buf, h.StateRoot[:]...)
	buf = append(buf, h.ValSetHash[:]...)
	putUint64(&buf, h.Round)
	return buf
}

// Hash ist der Block-Hash = chainHash(kanonischer Header). Spec §3a (Hashkette).
func (h *BlockHeader) Hash() [32]byte { return chainHash(h.bytes()) }

// txRoot ist die Merkle-Wurzel der Transaktions-Hashes in Block-Reihenfolge.
func txRoot(txs []*Transaction) [32]byte {
	leaves := make([][32]byte, len(txs))
	for i, tx := range txs {
		th := tx.Hash()
		leaves[i] = hashLeaf(nil, th[:]) // 0x00 || txhash
	}
	return merkleRoot(leaves, "fundus-empty-txs")
}

// orderTxs ordnet Transaktionen deterministisch nach (from, nonce). Garantiert
// die Nonce-Reihenfolge je Konto und ist über alle Nodes identisch (Spec §5).
func orderTxs(txs []*Transaction) []*Transaction {
	out := make([]*Transaction, len(txs))
	copy(out, txs)
	sort.SliceStable(out, func(i, j int) bool {
		if c := bytes.Compare(out[i].From[:], out[j].From[:]); c != 0 {
			return c < 0
		}
		return out[i].Nonce < out[j].Nonce
	})
	return out
}

// isCanonicalOrder prüft, ob txs bereits in kanonischer (from,nonce)-Reihenfolge
// vorliegen (ohne Pointer-Annahmen — vergleicht die Felder).
func isCanonicalOrder(txs []*Transaction) bool {
	for i := 1; i < len(txs); i++ {
		c := bytes.Compare(txs[i-1].From[:], txs[i].From[:])
		if c > 0 {
			return false
		}
		if c == 0 && txs[i-1].Nonce > txs[i].Nonce {
			return false
		}
	}
	return true
}

// BuildBlock erzeugt den nächsten Block aus parent + Transaktionen, wendet sie
// auf state an und füllt tx_root/state_root. state wird dabei verändert.
func BuildBlock(parent *BlockHeader, proposer Address, timestamp uint64, txs []*Transaction, state *State, feeCollector Address) (*Block, error) {
	ordered := orderTxs(txs)
	height := parent.Height + 1
	// Reifung VOR den Transaktionen — identisch in ApplyBlock, sonst weicht
	// der State-Root des Produzenten von dem der Prüfer ab.
	state.MatureUnbonding(height)
	// Ungültige Txs (falsche Nonce/Guthaben/Signatur) werden ÜBERSPRUNGEN statt
	// den ganzen Block zu verwerfen — eine schlechte Tx darf die übrigen nicht
	// blockieren. ApplyTransaction ist "validate-then-mutate": schlägt es fehl,
	// bleibt der State unverändert, die Tx wird einfach ausgelassen.
	applied := make([]*Transaction, 0, len(ordered))
	rewardSeen := false
	for _, tx := range ordered {
		// Höchstens EIN Storage-Reward-Tx pro Block. Ein zweiter wird ausgelassen
		// (statt den Block zu verwerfen), damit ein versehentlich doppelt
		// eingereihter Reward die übrigen Txs nicht blockiert.
		if tx.Type == TxStorageReward {
			if rewardSeen {
				continue
			}
			rewardSeen = true
		}
		if err := state.ApplyTransaction(tx, feeCollector, proposer, height); err != nil {
			continue // ungültige Tx auslassen
		}
		applied = append(applied, tx)
	}
	hdr := BlockHeader{
		Height:       parent.Height + 1,
		PrevHash:     parent.Hash(),
		Timestamp:    timestamp,
		Proposer:     proposer,
		FeeCollector: parent.FeeCollector, // konstant über die Kette (aus Genesis)
		TxRoot:       txRoot(applied),
		StateRoot:    state.Root(),
	}
	return &Block{Header: hdr, Transactions: applied}, nil
}

// ApplyBlock validiert einen Block gegen parent + state und wendet ihn an
// (für Nodes, die einen fremden Block nachvollziehen). state wird verändert.
func ApplyBlock(parent *BlockHeader, blk *Block, state *State, feeCollector Address) error {
	if blk.Header.Height != parent.Height+1 {
		return errors.New("chain: falsche Höhe")
	}
	if blk.Header.PrevHash != parent.Hash() {
		return errors.New("chain: prev_hash passt nicht zum Vorgänger")
	}
	if !isCanonicalOrder(blk.Transactions) {
		return errors.New("chain: Transaktionen nicht kanonisch (from,nonce) geordnet")
	}
	if blk.Header.TxRoot != txRoot(blk.Transactions) {
		return errors.New("chain: tx_root stimmt nicht")
	}
	// Höchstens EIN Storage-Reward-Tx pro Block. Ein Block mit mehreren wird
	// verworfen — ein ehrlicher Node darf einen solchen Block nicht übernehmen,
	// auch wenn jeder einzelne Reward für sich gültige Quittungen trüge.
	rewardCount := 0
	for _, tx := range blk.Transactions {
		if tx.Type == TxStorageReward {
			rewardCount++
		}
	}
	if rewardCount > 1 {
		return errors.New("chain: mehr als ein Storage-Reward-Tx im Block")
	}
	state.MatureUnbonding(blk.Header.Height) // gleiche Position wie in BuildBlock
	for _, tx := range blk.Transactions {
		if err := state.ApplyTransaction(tx, feeCollector, blk.Header.Proposer, blk.Header.Height); err != nil {
			return err
		}
	}
	if blk.Header.StateRoot != state.Root() {
		return errors.New("chain: state_root stimmt nicht nach Anwendung")
	}
	return nil
}

// GenesisAccount ist eine Anfangs-Allokation (Spec §9, Frischstart).
type GenesisAccount struct {
	Address Address
	Balance *big.Int
}

// GenesisTimestamp ist der FESTE Genesis-Zeitstempel (Unix-Sekunden).
// Bewusst konstant statt time.Now(), damit JEDER Node unabhängig denselben
// Genesis-Hash erzeugt — Voraussetzung dafür, dass sich Nodes auf dieselbe
// Chain synchronisieren (Multi-Node, Spec §11). Wert: 2025-01-01T00:00:00Z.
const GenesisTimestamp uint64 = 1735689600

// GenesisSupply ist die Startausgabe an den Fee-Collector: 1 Bio. FND in uFND
// (= INITIAL_SUPPLY aus FND.sol, Spec §9).
func GenesisSupply() *big.Int {
	return new(big.Int).Mul(big.NewInt(1_000_000_000_000), big.NewInt(UFNDPerFND))
}

// CanonicalGenesis erzeugt den DETERMINISTISCHEN Genesis-Block für einen
// gegebenen Fee-Collector: fester Timestamp + feste Startausgabe. Zwei Nodes
// mit demselben Fee-Collector erhalten damit garantiert denselben Genesis-Hash,
// ohne eine genesis.json austauschen zu müssen. Die Datei bleibt optional als
// verifizierbares Artefakt, ist aber nicht mehr die Quelle der Determiniertheit.
func CanonicalGenesis(feeCollector Address) (*Block, *State) {
	allocs := []GenesisAccount{{Address: feeCollector, Balance: GenesisSupply()}}
	return NewGenesis(feeCollector, allocs, GenesisTimestamp)
}

// NewGenesis baut den Genesis-Block (Höhe 0) und den initialen State.
// NewGenesis baut den Genesis-Block (Höhe 0). feeCollector wird im Header
// verankert (tamper-evident über den Genesis-Hash) und definiert für die ganze
// Kette die Gebühren-/Autoritätsadresse.
func NewGenesis(feeCollector Address, allocs []GenesisAccount, timestamp uint64) (*Block, *State) {
	state := NewState()
	for _, a := range allocs {
		state.Credit(a.Address, a.Balance)
	}
	hdr := BlockHeader{
		Height:       0,
		Timestamp:    timestamp,
		FeeCollector: feeCollector,
		TxRoot:       txRoot(nil),
		StateRoot:    state.Root(),
	}
	return &Block{Header: hdr, GenesisAllocs: allocs}, state
}
