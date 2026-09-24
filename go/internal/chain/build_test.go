package chain

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// TestBuildSignedTransferEndToEnd deckt den Roadmap-2.5-Pfad ab:
// Schlüssel → BuildSignedTransfer → Block → Saldo stimmt (Sender minus Betrag+
// Gebühr, Empfänger plus Betrag).
func TestBuildSignedTransferEndToEnd(t *testing.T) {
	dir := t.TempDir()
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)
	feeColl := from

	start := new(big.Int).Mul(big.NewInt(1000), big.NewInt(UFNDPerFND))
	allocs := []GenesisAccount{{Address: from, Balance: start}}
	genesis, _ := NewGenesis(feeColl, allocs, 1000)
	var noHash [32]byte
	bc, err := NewBlockchain(dir, from, genesis, noHash)
	if err != nil {
		t.Fatalf("NewBlockchain: %v", err)
	}
	defer bc.Close()

	var to Address
	to[0] = 0x42
	amount := new(big.Int).Mul(big.NewInt(10), big.NewInt(UFNDPerFND))

	// Nonce serverseitig ermitteln (wie der /chain/send-Endpunkt).
	_, nonce := bc.AccountInfo(from)
	tx, err := BuildSignedTransfer(priv, to, amount, nonce)
	if err != nil {
		t.Fatalf("BuildSignedTransfer: %v", err)
	}
	// Builder setzt From korrekt + signiert.
	if tx.From != from {
		t.Fatal("tx.From weicht von abgeleiteter Adresse ab")
	}
	if len(tx.Signature) == 0 {
		t.Fatal("Tx ist nicht signiert")
	}
	if tx.Fee.Cmp(FeeForValue(amount)) != 0 {
		t.Fatalf("Gebühr %s != erwartet %s", tx.Fee, FeeForValue(amount))
	}

	if _, err := bc.ProduceBlock([]*Transaction{tx}, 1005); err != nil {
		t.Fatalf("ProduceBlock: %v", err)
	}

	// Empfänger hat den Betrag.
	balTo, _ := bc.AccountInfo(to)
	if balTo != amount.String() {
		t.Fatalf("Empfänger-Saldo %s != %s", balTo, amount.String())
	}
	// Sender hat Start − (Betrag + Gebühr); die Gebühr geht an feeColl (= from),
	// daher fließt sie hier zurück. Netto: Start − Betrag (Gebühr an sich selbst).
	balFrom, _ := bc.AccountInfo(from)
	wantFrom := new(big.Int).Sub(start, amount) // Gebühr an feeColl==from zurück
	if balFrom != wantFrom.String() {
		t.Fatalf("Sender-Saldo %s != %s", balFrom, wantFrom.String())
	}
}

// TestBuildSignedTransferRejectsBadInput prüft die Eingabevalidierung.
func TestBuildSignedTransferRejectsBadInput(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	var to Address
	to[0] = 0x01

	if _, err := BuildSignedTransfer(nil, to, big.NewInt(1), 0); err == nil {
		t.Fatal("nil-Schlüssel hätte abgelehnt werden müssen")
	}
	if _, err := BuildSignedTransfer(priv, to, big.NewInt(0), 0); err == nil {
		t.Fatal("Betrag 0 hätte abgelehnt werden müssen")
	}
	if _, err := BuildSignedTransfer(priv, to, big.NewInt(-5), 0); err == nil {
		t.Fatal("negativer Betrag hätte abgelehnt werden müssen")
	}
}

// TestBuildSignedTxGeneric prüft den generischen Builder (z.B. für gebührenfreie
// Folge-Tx) — Signatur + From korrekt, Fee 0 erlaubt.
func TestBuildSignedTxGeneric(t *testing.T) {
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)

	// Beliebige Payload (hier: eine Escrow-Ref), Fee 0.
	var id [32]byte
	id[0] = 0xAB
	payload := (&EscrowRefPayload{EscrowID: id}).encode()
	tx, err := BuildSignedTx(priv, TxEscrowConfirm, nil, payload, 7)
	if err != nil {
		t.Fatalf("BuildSignedTx: %v", err)
	}
	if tx.From != from {
		t.Fatal("From falsch")
	}
	if tx.Nonce != 7 {
		t.Fatalf("Nonce %d != 7", tx.Nonce)
	}
	if tx.Fee == nil || tx.Fee.Sign() != 0 {
		t.Fatal("Fee sollte 0 sein")
	}
	if len(tx.Signature) == 0 {
		t.Fatal("nicht signiert")
	}
}

// TestNetFromGross prüft die Rückrechnung für den "Gebühr aus Betrag"-Modus:
// net + FeeForValue(net) darf den eingegebenen Gesamtbetrag nicht überschreiten.
func TestNetFromGross(t *testing.T) {
	cases := []int64{1_018_000_000, 101_800_000_000, 1_000_000_000, 50_000_000_000}
	for _, gross := range cases {
		g := big.NewInt(gross)
		net := NetFromGross(g)
		fee := FeeForValue(net)
		sum := new(big.Int).Add(net, fee)
		if sum.Cmp(g) > 0 {
			t.Fatalf("gross=%d: net(%s)+fee(%s)=%s > gross — Sender zahlt zu viel",
				gross, net, fee, sum)
		}
		// Staubfrei: net+fee muss EXAKT gross treffen (keine Reste auf dem Konto).
		diff := new(big.Int).Sub(g, sum)
		if diff.Sign() != 0 {
			t.Fatalf("gross=%d: Rest %s uFND bleibt liegen (sollte 0 sein)", gross, diff)
		}
	}
}

// TestBuildBlockSkipsInvalidTx stellt sicher, dass eine ungültige Tx (falsche
// Nonce) den Block NICHT komplett verwirft, sondern nur ausgelassen wird und
// die übrigen gültigen Txs durchgehen.
func TestBuildBlockSkipsInvalidTx(t *testing.T) {
	dir := t.TempDir()
	priv, _ := crypto.GenerateKey()
	from := PubkeyToAddress(&priv.PublicKey)

	start := new(big.Int).Mul(big.NewInt(1000), big.NewInt(UFNDPerFND))
	allocs := []GenesisAccount{{Address: from, Balance: start}}
	genesis, _ := NewGenesis(from, allocs, 1000)
	var noHash [32]byte
	bc, err := NewBlockchain(dir, from, genesis, noHash)
	if err != nil {
		t.Fatalf("NewBlockchain: %v", err)
	}
	defer bc.Close()

	// Gültige Tx (Nonce 0) + ungültige Tx (Nonce 5 — Lücke).
	var to Address
	to[0] = 0xaa
	amount := new(big.Int).Mul(big.NewInt(1), big.NewInt(UFNDPerFND))
	good, _ := BuildSignedTransfer(priv, to, amount, 0)
	bad, _ := BuildSignedTransfer(priv, to, amount, 5)

	blk, err := bc.ProduceBlock([]*Transaction{good, bad}, 1)
	if err != nil {
		t.Fatalf("ProduceBlock sollte trotz einer schlechten Tx gelingen: %v", err)
	}
	if len(blk.Transactions) != 1 {
		t.Fatalf("erwartet 1 gültige Tx im Block, war %d", len(blk.Transactions))
	}
}
