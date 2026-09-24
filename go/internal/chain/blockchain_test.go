package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestBlockchainPersistenceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	feeColl := from // im Test simpel: Proposer = FeeCollector = from

	startSupply := new(big.Int).Mul(big.NewInt(1000), big.NewInt(UFNDPerFND))
	allocs := []GenesisAccount{{Address: from, Balance: startSupply}}
	genesis, _ := NewGenesis(feeColl, allocs, 1000)
	var noHash [32]byte

	bc, err := NewBlockchain(dir, from, genesis, noHash)
	if err != nil {
		t.Fatalf("NewBlockchain: %v", err)
	}
	if bc.Height() != 0 {
		t.Fatalf("Genesis-Höhe %d != 0", bc.Height())
	}

	// Eine Transfer-Tx bauen und einen Block produzieren.
	var to Address
	to[0] = 0x99
	amount := new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))
	tx := &Transaction{Type: TxTransfer, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	if err := SignTransaction(tx, priv); err != nil {
		t.Fatal(err)
	}
	blk, err := bc.ProduceBlock([]*Transaction{tx}, 1005)
	if err != nil {
		t.Fatalf("ProduceBlock: %v", err)
	}
	if blk.Header.Height != 1 {
		t.Fatalf("Block-Höhe %d != 1", blk.Header.Height)
	}
	balBefore, _ := bc.AccountInfo(to)

	// Ersten Store schließen (gibt den bbolt-Datei-Lock frei), bevor die zweite
	// Instanz dieselbe chain.db öffnet — sonst wartet Open auf den Lock.
	if err := bc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Neu laden (simuliert Node-Neustart) → State muss identisch sein.
	bc2, err := NewBlockchain(dir, from, genesis, noHash)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	defer bc2.Close()
	if bc2.Height() != 1 {
		t.Fatalf("nach Reload Höhe %d != 1", bc2.Height())
	}
	balAfter, _ := bc2.AccountInfo(to)
	if balBefore != balAfter {
		t.Fatalf("Saldo nach Reload abweichend: %s != %s", balBefore, balAfter)
	}
	if bc.HeadHash() != bc2.HeadHash() {
		t.Fatal("Head-Hash nach Reload abweichend")
	}
}

func TestGenesisCarriesFeeCollectorAndHashCheck(t *testing.T) {
	dir := t.TempDir()
	var feeColl Address
	feeColl[0] = 0xFE
	var someone Address
	someone[0] = 0x11

	allocs := []GenesisAccount{{Address: feeColl, Balance: new(big.Int).Mul(big.NewInt(5), big.NewInt(UFNDPerFND))}}
	genesis, _ := NewGenesis(feeColl, allocs, 2000)

	// Fee-Collector steckt im Header.
	if genesis.Header.FeeCollector != feeColl {
		t.Fatal("Fee-Collector nicht im Genesis-Header")
	}
	goodHash := GenesisHash(genesis)

	// Korrekter erwarteter Hash → Chain startet.
	bc, err := NewBlockchain(dir, feeColl, genesis, goodHash)
	if err != nil {
		t.Fatalf("NewBlockchain mit korrektem Hash: %v", err)
	}
	defer bc.Close()
	// Chain bezieht den Fee-Collector aus dem Genesis (nicht aus Konstante).
	bal, _ := bc.AccountInfo(feeColl)
	if bal == "0" {
		t.Fatal("Fee-Collector-Allokation fehlt")
	}

	// Falscher erwarteter Hash → Ablehnung (vertauschter Genesis).
	dir2 := t.TempDir()
	var wrong [32]byte
	wrong[0] = 0xAB
	if _, err := NewBlockchain(dir2, feeColl, genesis, wrong); err == nil {
		t.Fatal("falscher Genesis-Hash hätte abgelehnt werden müssen")
	}

	// Ein Genesis mit anderem Fee-Collector hat einen anderen Hash.
	genesis2, _ := NewGenesis(someone, allocs, 2000)
	if GenesisHash(genesis2) == goodHash {
		t.Fatal("anderer Fee-Collector muss anderen Genesis-Hash ergeben")
	}
}

func TestMempoolDedupAndTake(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	mp := NewMempool(100)

	var to Address
	to[0] = 0x01
	amount := big.NewInt(1_000_000)
	tx := &Transaction{Type: TxTransfer, Nonce: 0, Fee: FeeForValue(amount),
		Payload: (&TransferPayload{To: to, Amount: amount}).encode()}
	if err := SignTransaction(tx, priv); err != nil {
		t.Fatal(err)
	}
	if err := mp.Add(tx); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := mp.Add(tx); err == nil {
		t.Fatal("doppelte Tx hätte abgelehnt werden müssen")
	}
	if mp.Len() != 1 {
		t.Fatalf("Mempool-Länge %d != 1", mp.Len())
	}
	taken := mp.Take(0)
	if len(taken) != 1 {
		t.Fatalf("Take lieferte %d != 1", len(taken))
	}
	if mp.Len() != 0 {
		t.Fatal("Mempool nach Take nicht leer")
	}
}
