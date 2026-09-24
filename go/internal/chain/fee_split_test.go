package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// SplitFee teilt 50/50, Rest (Rundung) an den Collector.
func TestSplitFeeEven(t *testing.T) {
	node, coll := SplitFee(big.NewInt(1000))
	if node.Int64() != 500 || coll.Int64() != 500 {
		t.Fatalf("1000 → node=%s coll=%s, erwartet 500/500", node, coll)
	}
}

func TestSplitFeeOdd(t *testing.T) {
	// Ungerade: node abgerundet, Collector bekommt den Rest (kein uFND verloren).
	node, coll := SplitFee(big.NewInt(1001))
	if node.Int64() != 500 || coll.Int64() != 501 {
		t.Fatalf("1001 → node=%s coll=%s, erwartet 500/501", node, coll)
	}
	// Summe muss exakt die Gebühr sein.
	sum := new(big.Int).Add(node, coll)
	if sum.Int64() != 1001 {
		t.Fatalf("Summe %s, erwartet 1001 (kein uFND darf verloren gehen)", sum)
	}
}

func TestSplitFeeZero(t *testing.T) {
	node, coll := SplitFee(big.NewInt(0))
	if node.Sign() != 0 || coll.Sign() != 0 {
		t.Fatal("Gebühr 0 sollte 0/0 ergeben")
	}
}

// Ende-zu-Ende: Ein Transfer teilt die Gebühr zwischen Produzent und Collector.
func TestTransferSplitsFeeToProducer(t *testing.T) {
	st := NewState()
	senderKey, _ := crypto.GenerateKey()
	sender := PubkeyToAddress(&senderKey.PublicKey)

	var to, feeColl, producer Address
	to[0] = 0x11
	feeColl[0] = 0x22
	producer[0] = 0x33

	// Sender mit Guthaben ausstatten.
	amount := big.NewInt(1_000_000) // 1 000 000 uFND Transfer
	fee := FeeForValue(amount)      // 1,8 % = 18 000 uFND
	st.Credit(sender, new(big.Int).Add(amount, fee))

	tx, err := BuildSignedTransfer(senderKey, to, amount, 0)
	if err != nil {
		t.Fatalf("BuildSignedTransfer: %v", err)
	}
	if err := st.ApplyTransaction(tx, feeColl, producer, 1); err != nil {
		t.Fatalf("ApplyTransaction: %v", err)
	}

	// Empfänger hat den vollen Betrag.
	if st.Balance(to).Cmp(amount) != 0 {
		t.Fatalf("Empfänger = %s, erwartet %s", st.Balance(to), amount)
	}
	// Gebühr 50/50 geteilt.
	nodeShare, collShare := SplitFee(fee)
	if st.Balance(producer).Cmp(nodeShare) != 0 {
		t.Fatalf("Produzent = %s, erwartet %s", st.Balance(producer), nodeShare)
	}
	if st.Balance(feeColl).Cmp(collShare) != 0 {
		t.Fatalf("Collector = %s, erwartet %s", st.Balance(feeColl), collShare)
	}
	// Node + Collector zusammen = ganze Gebühr.
	sum := new(big.Int).Add(st.Balance(producer), st.Balance(feeColl))
	if sum.Cmp(fee) != 0 {
		t.Fatalf("Summe der Gebührenanteile = %s, erwartet %s", sum, fee)
	}
}

// Fällt Produzent == Collector zusammen, bekommt der Collector die ganze Gebühr.
func TestTransferProducerEqualsCollector(t *testing.T) {
	st := NewState()
	senderKey, _ := crypto.GenerateKey()
	sender := PubkeyToAddress(&senderKey.PublicKey)

	var to, feeColl Address
	to[0] = 0x11
	feeColl[0] = 0x22

	amount := big.NewInt(1_000_000)
	fee := FeeForValue(amount)
	st.Credit(sender, new(big.Int).Add(amount, fee))

	tx, _ := BuildSignedTransfer(senderKey, to, amount, 0)
	// Produzent == Collector.
	if err := st.ApplyTransaction(tx, feeColl, feeColl, 1); err != nil {
		t.Fatalf("ApplyTransaction: %v", err)
	}
	if st.Balance(feeColl).Cmp(fee) != 0 {
		t.Fatalf("Collector = %s, erwartet ganze Gebühr %s", st.Balance(feeColl), fee)
	}
}
