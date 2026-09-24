package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// TestCanonicalGenesisDeterministic stellt sicher, dass zwei unabhängig erzeugte
// Genesis-Blöcke mit demselben Fee-Collector denselben Hash haben (Voraussetzung
// für Multi-Node-Sync: jeder Node erzeugt unabhängig denselben Genesis).
func TestCanonicalGenesisDeterministic(t *testing.T) {
	var fc Address
	fc[0] = 0xAB
	g1, _ := CanonicalGenesis(fc)
	g2, _ := CanonicalGenesis(fc)
	if g1.Header.Hash() != g2.Header.Hash() {
		t.Fatal("CanonicalGenesis ist nicht deterministisch — verschiedene Hashes")
	}
	// Anderer Fee-Collector → anderer Genesis.
	var fc2 Address
	fc2[0] = 0xCD
	g3, _ := CanonicalGenesis(fc2)
	if g1.Header.Hash() == g3.Header.Hash() {
		t.Fatal("verschiedene Fee-Collector sollten verschiedene Genesis ergeben")
	}
}

// TestBlockSyncBetweenNodes simuliert zwei Nodes: Node A produziert Blöcke,
// Node B importiert sie über ExportBlockJSON/ImportBlockJSON und landet bei
// identischer Höhe + Head-Hash.
func TestBlockSyncBetweenNodes(t *testing.T) {
	var fc Address
	fc[0] = 0xAB
	_, _ = CanonicalGenesis(fc)
	var noHash [32]byte

	// Node A (Produzent) — mit Guthaben für Transfers.
	dirA := t.TempDir()
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	// Genesis mit Allocs für 'from' (sonst kein Guthaben).
	allocs := []GenesisAccount{
		{Address: fc, Balance: GenesisSupply()},
		{Address: from, Balance: new(big.Int).Mul(big.NewInt(1000), big.NewInt(UFNDPerFND))},
	}
	gA := &Block{Header: BlockHeader{
		Height: 0, Timestamp: GenesisTimestamp, FeeCollector: fc,
		TxRoot: txRoot(nil),
	}, GenesisAllocs: allocs}
	// state_root korrekt setzen.
	stA := NewState()
	for _, a := range allocs {
		stA.Credit(a.Address, a.Balance)
	}
	gA.Header.StateRoot = stA.Root()

	bcA, err := NewBlockchain(dirA, fc, gA, noHash)
	if err != nil {
		t.Fatalf("Node A: %v", err)
	}
	defer bcA.Close()
	// Node B (Sync-Empfänger) — exakt derselbe Genesis.
	dirB := t.TempDir()
	bcB, err := NewBlockchain(dirB, fc, gA, noHash)
	if err != nil {
		t.Fatalf("Node B: %v", err)
	}
	defer bcB.Close()

	// Genesis-Hashes müssen gleich sein.
	if bcA.GenesisHeaderHash() != bcB.GenesisHeaderHash() {
		t.Fatal("Genesis-Hashes der beiden Nodes weichen ab")
	}

	// Node A produziert 3 Blöcke mit je einer Transfer-Tx.
	var to Address
	to[0] = 0x42
	for i := 0; i < 3; i++ {
		amount := new(big.Int).Mul(big.NewInt(int64(i+1)), big.NewInt(UFNDPerFND))
		_, nonce := bcA.AccountInfo(from)
		tx, err := BuildSignedTransfer(priv, to, amount, nonce)
		if err != nil {
			t.Fatalf("BuildSignedTransfer %d: %v", i, err)
		}
		if _, err := bcA.ProduceBlock([]*Transaction{tx}, GenesisTimestamp+uint64(i+1)); err != nil {
			t.Fatalf("ProduceBlock %d: %v", i, err)
		}
	}
	if bcA.Height() != 3 {
		t.Fatalf("Node A Höhe %d != 3", bcA.Height())
	}

	// Node B holt die Blöcke der Reihe nach (wie SyncChainFromPeer).
	for h := uint64(1); h <= bcA.Height(); h++ {
		blockJSON, err := bcA.ExportBlockJSON(h)
		if err != nil {
			t.Fatalf("ExportBlockJSON %d: %v", h, err)
		}
		if err := bcB.ImportBlockJSON(blockJSON); err != nil {
			t.Fatalf("ImportBlockJSON %d: %v", h, err)
		}
	}

	// Beide Nodes müssen jetzt identisch sein.
	if bcA.Height() != bcB.Height() {
		t.Fatalf("Höhen weichen ab: A=%d B=%d", bcA.Height(), bcB.Height())
	}
	if bcA.HeadHash() != bcB.HeadHash() {
		t.Fatal("Head-Hashes weichen nach Sync ab")
	}
	balA, _ := bcA.AccountInfo(to)
	balB, _ := bcB.AccountInfo(to)
	if balA != balB {
		t.Fatalf("Empfänger-Saldo weicht ab: A=%s B=%s", balA, balB)
	}
}

// TestImportBlockRejectsGap stellt sicher, dass ein Block mit Lücke (nicht der
// direkte Nachfolger) abgelehnt wird.
func TestImportBlockRejectsGap(t *testing.T) {
	var fc Address
	fc[0] = 0xAB
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	allocs := []GenesisAccount{
		{Address: fc, Balance: GenesisSupply()},
		{Address: from, Balance: new(big.Int).Mul(big.NewInt(1000), big.NewInt(UFNDPerFND))},
	}
	st := NewState()
	for _, a := range allocs {
		st.Credit(a.Address, a.Balance)
	}
	g := &Block{Header: BlockHeader{
		Height: 0, Timestamp: GenesisTimestamp, FeeCollector: fc,
		TxRoot: txRoot(nil), StateRoot: st.Root(),
	}, GenesisAllocs: allocs}
	var noHash [32]byte

	bcA, _ := NewBlockchain(t.TempDir(), fc, g, noHash)
	bcB, _ := NewBlockchain(t.TempDir(), fc, g, noHash)
	defer bcA.Close()
	defer bcB.Close()

	var to Address
	to[0] = 0x42
	for i := 0; i < 2; i++ {
		amount := new(big.Int).Mul(big.NewInt(int64(i+1)), big.NewInt(UFNDPerFND))
		_, nonce := bcA.AccountInfo(from)
		tx, _ := BuildSignedTransfer(priv, to, amount, nonce)
		bcA.ProduceBlock([]*Transaction{tx}, GenesisTimestamp+uint64(i+1))
	}

	// Block 2 ohne Block 1 importieren → Lücke, muss scheitern.
	block2, _ := bcA.ExportBlockJSON(2)
	if err := bcB.ImportBlockJSON(block2); err == nil {
		t.Fatal("Block mit Lücke hätte abgelehnt werden müssen")
	}
}
