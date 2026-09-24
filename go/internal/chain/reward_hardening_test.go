package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// Mempool darf einen Storage-Reward-Tx NICHT annehmen (nur der Produzent erzeugt
// ihn beim Blockbau). Schutz gegen eingeschleuste Reward-Tx von außen.
func TestMempoolRejectsStorageReward(t *testing.T) {
	mp := NewMempool(100)
	prodKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	r := mkRewardReceipt(t, provider, 1, 1_000_000_000_000)
	payload := StorageRewardPayload{Receipts: []Receipt{r}}
	raw, _ := payload.Encode()
	tx, err := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 0)
	if err != nil {
		t.Fatalf("BuildSignedTx: %v", err)
	}

	if err := mp.Add(tx); err == nil {
		t.Fatal("Mempool sollte Storage-Reward-Tx ablehnen, hat ihn aber akzeptiert")
	}
	if mp.Len() != 0 {
		t.Fatalf("Mempool sollte leer sein, hat %d Txs", mp.Len())
	}
}

// Ein normaler Transfer-Tx wird vom Mempool weiterhin akzeptiert (Gegenprobe).
func TestMempoolAcceptsTransfer(t *testing.T) {
	mp := NewMempool(100)
	key, _ := crypto.GenerateKey()
	to := PubkeyToAddress(&key.PublicKey)

	tx, err := BuildSignedTransfer(key, to, big.NewInt(100), 0)
	if err != nil {
		t.Fatalf("BuildSignedTransfer: %v", err)
	}
	if err := mp.Add(tx); err != nil {
		t.Fatalf("Mempool sollte Transfer akzeptieren: %v", err)
	}
	if mp.Len() != 1 {
		t.Fatalf("Mempool sollte 1 Tx haben, hat %d", mp.Len())
	}
}

// ApplyBlock muss einen Block mit ZWEI Storage-Reward-Tx verwerfen.
func TestApplyBlockRejectsMultipleRewards(t *testing.T) {
	st := NewState()
	genesis := &BlockHeader{Height: 0}

	prodKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	// Zwei Reward-Tx (unterschiedliche Quittungen, beide für sich gültig).
	r1 := mkRewardReceipt(t, provider, 1, 1_000_000_000_000)
	r2 := mkRewardReceipt(t, provider, 2, 1_000_000_000_000)
	raw1, _ := (&StorageRewardPayload{Receipts: []Receipt{r1}}).Encode()
	raw2, _ := (&StorageRewardPayload{Receipts: []Receipt{r2}}).Encode()
	tx1, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw1, 0)
	tx2, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw2, 1)

	// Block manuell mit beiden Reward-Tx bauen (Angreifer-Szenario).
	txs := []*Transaction{tx1, tx2}
	hdr := BlockHeader{
		Height:    1,
		PrevHash:  genesis.Hash(),
		Timestamp: 1_700_000_000,
		TxRoot:    txRoot(txs),
	}
	blk := &Block{Header: hdr, Transactions: txs}

	err := ApplyBlock(genesis, blk, st, Address{})
	if err == nil {
		t.Fatal("ApplyBlock sollte Block mit zwei Storage-Reward-Tx verwerfen")
	}
}

// ApplyBlock akzeptiert einen Block mit GENAU EINEM Storage-Reward-Tx.
func TestApplyBlockAcceptsSingleReward(t *testing.T) {
	genesis := &BlockHeader{Height: 0}

	prodKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	r := mkRewardReceipt(t, provider, 1, 1_000_000_000_000)
	raw, _ := (&StorageRewardPayload{Receipts: []Receipt{r}}).Encode()
	tx, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw, 0)

	// Block über BuildBlock mit einem frischen State erzeugen (berechnet den
	// korrekten State-Root nach Anwendung).
	stBuild := NewState()
	blk, err := BuildBlock(genesis, Address{}, 1_700_000_000, []*Transaction{tx}, stBuild, Address{})
	if err != nil {
		t.Fatalf("BuildBlock: %v", err)
	}
	if len(blk.Transactions) != 1 {
		t.Fatalf("Block sollte 1 Tx enthalten, hat %d", len(blk.Transactions))
	}
	// Zweiter, frischer State zum Nachvollziehen — muss denselben State-Root
	// ergeben und den Block akzeptieren.
	stApply := NewState()
	if err := ApplyBlock(genesis, blk, stApply, Address{}); err != nil {
		t.Fatalf("ApplyBlock sollte einzelnen Reward akzeptieren: %v", err)
	}
}

// BuildBlock lässt einen zweiten Storage-Reward-Tx aus (statt den Block zu
// verwerfen) — der gebaute Block enthält höchstens einen.
func TestBuildBlockDropsSecondReward(t *testing.T) {
	st := NewState()
	genesis := &BlockHeader{Height: 0}

	prodKey, _ := crypto.GenerateKey()
	provKey, _ := crypto.GenerateKey()
	provider := PubkeyToAddress(&provKey.PublicKey)

	r1 := mkRewardReceipt(t, provider, 1, 1_000_000_000_000)
	r2 := mkRewardReceipt(t, provider, 2, 1_000_000_000_000)
	raw1, _ := (&StorageRewardPayload{Receipts: []Receipt{r1}}).Encode()
	raw2, _ := (&StorageRewardPayload{Receipts: []Receipt{r2}}).Encode()
	tx1, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw1, 0)
	tx2, _ := BuildSignedTx(prodKey, TxStorageReward, nil, raw2, 1)

	blk, err := BuildBlock(genesis, Address{}, 1_700_000_000, []*Transaction{tx1, tx2}, st, Address{})
	if err != nil {
		t.Fatalf("BuildBlock: %v", err)
	}
	rewardCount := 0
	for _, tx := range blk.Transactions {
		if tx.Type == TxStorageReward {
			rewardCount++
		}
	}
	if rewardCount != 1 {
		t.Fatalf("Block sollte genau 1 Reward-Tx enthalten, hat %d", rewardCount)
	}
}
