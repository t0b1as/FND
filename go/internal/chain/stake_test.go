package chain

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// ─── FND-020 · StakePayload ──────────────────────────────────────────────────

func TestFND_020_StakePayloadRoundtrip(t *testing.T) {
	for _, amt := range []*big.Int{
		big.NewInt(1),
		big.NewInt(10 * UFNDPerFND),
		new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1)), // uint128-Max
	} {
		enc := encodeStakePayload(&StakePayload{Amount: amt})
		if len(enc) != 16 {
			t.Fatalf("Payload muss 16 Bytes sein, war %d", len(enc))
		}
		dec, err := decodeStakePayload(enc)
		if err != nil {
			t.Fatalf("decode(%s): %v", amt, err)
		}
		if dec.Amount.Cmp(amt) != 0 {
			t.Fatalf("Roundtrip: %s → %s", amt, dec.Amount)
		}
	}
}

func TestFND_020_StakePayloadRejectsMalformed(t *testing.T) {
	for _, b := range [][]byte{nil, {}, make([]byte, 15), make([]byte, 17), make([]byte, 64)} {
		if _, err := decodeStakePayload(b); err == nil {
			t.Fatalf("Länge %d muss abgelehnt werden", len(b))
		}
	}
}

// FND_020_ApplyNeverPanics: konsens-kritischer Pfad. Ein Panic beim Anwenden
// eines fremden Blocks ist ein Remote-DoS, kein Schönheitsfehler.
func TestFND_020_ApplyNeverPanics(t *testing.T) {
	key := mustKey(t)
	for _, n := range []int{0, 1, 8, 15, 16, 17, 64, 255} {
		for _, typ := range []TxType{TxStake, TxUnstake} {
			payload := make([]byte, n)
			for i := range payload {
				payload[i] = 0xff
			}
			tx := signed(t, key, typ, 0, big.NewInt(0), payload)
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Panic bei Typ 0x%02x, Payload-Länge %d: %v", byte(typ), n, r)
					}
				}()
				st := NewState()
				_ = st.ApplyTransaction(tx, Address{}, Address{}, 1)
			}()
		}
	}
}

// ─── FND-021 · applyStake / applyUnstake ─────────────────────────────────────

func TestFND_021_StakeMovesBalanceToStake(t *testing.T) {
	key := mustKey(t)
	addr := PubkeyToAddress(&key.PublicKey)
	st := NewState()
	st.Credit(addr, big.NewInt(100*UFNDPerFND))

	amt := big.NewInt(10 * UFNDPerFND)
	tx := signed(t, key, TxStake, 0, StakeFee(amt), encodeStakePayload(&StakePayload{Amount: amt}))
	if err := st.ApplyTransaction(tx, Address{1}, Address{}, 1); err != nil {
		t.Fatalf("Stake abgelehnt: %v", err)
	}
	if got := st.Stake(addr); got.Cmp(amt) != 0 {
		t.Fatalf("Stake = %s, erwartet %s", got, amt)
	}
	wantBal := new(big.Int).Sub(big.NewInt(100*UFNDPerFND), new(big.Int).Add(amt, StakeFee(amt)))
	if got := st.Balance(addr); got.Cmp(wantBal) != 0 {
		t.Fatalf("Guthaben = %s, erwartet %s", got, wantBal)
	}
}

func TestFND_021_StakeRejections(t *testing.T) {
	amt := big.NewInt(10 * UFNDPerFND)
	cases := []struct {
		name    string
		credit  *big.Int
		amount  *big.Int
		fee     *big.Int
		nonce   uint64
		wantErr bool
	}{
		{"gültig", big.NewInt(100 * UFNDPerFND), amt, StakeFee(amt), 0, false},
		{"Betrag 0", big.NewInt(100 * UFNDPerFND), big.NewInt(0), big.NewInt(0), 0, true},
		{"über dem Guthaben", big.NewInt(5 * UFNDPerFND), amt, StakeFee(amt), 0, true},
		{"Guthaben deckt Betrag, nicht die Gebühr", amt, amt, StakeFee(amt), 0, true},
		{"Gebühr zu niedrig", big.NewInt(100 * UFNDPerFND), amt, big.NewInt(1), 0, true},
		{"Gebühr null", big.NewInt(100 * UFNDPerFND), amt, big.NewInt(0), 0, true},
		{"falsche Nonce", big.NewInt(100 * UFNDPerFND), amt, StakeFee(amt), 7, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := mustKey(t)
			addr := PubkeyToAddress(&key.PublicKey)
			st := NewState()
			st.Credit(addr, c.credit)
			tx := signed(t, key, TxStake, c.nonce, c.fee, encodeStakePayload(&StakePayload{Amount: c.amount}))
			err := st.ApplyTransaction(tx, Address{1}, Address{}, 1)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, erwartet Fehler: %v", err, c.wantErr)
			}
		})
	}
}

func TestFND_021_UnstakeOverStakedRejected(t *testing.T) {
	key := mustKey(t)
	addr := PubkeyToAddress(&key.PublicKey)
	st := NewState()
	st.Credit(addr, big.NewInt(100*UFNDPerFND))

	amt := big.NewInt(10 * UFNDPerFND)
	stakeTx := signed(t, key, TxStake, 0, StakeFee(amt), encodeStakePayload(&StakePayload{Amount: amt}))
	if err := st.ApplyTransaction(stakeTx, Address{1}, Address{}, 1); err != nil {
		t.Fatal(err)
	}
	tooMuch := big.NewInt(20 * UFNDPerFND)
	unTx := signed(t, key, TxUnstake, 1, StakeFee(tooMuch), encodeStakePayload(&StakePayload{Amount: tooMuch}))
	if err := st.ApplyTransaction(unTx, Address{1}, Address{}, 2); err == nil {
		t.Fatal("Unstake über dem gestakten Betrag muss abgelehnt werden")
	}
}

// FND_021_UnstakeRespectsUnbonding: das Geld darf nicht sofort zurückfließen —
// sonst entzieht sich ein Validator jeder Strafe durch schnelles Unstaken.
func TestFND_021_UnstakeRespectsUnbonding(t *testing.T) {
	key := mustKey(t)
	addr := PubkeyToAddress(&key.PublicKey)
	st := NewState()
	st.Credit(addr, big.NewInt(100*UFNDPerFND))

	amt := big.NewInt(10 * UFNDPerFND)
	if err := st.ApplyTransaction(
		signed(t, key, TxStake, 0, StakeFee(amt), encodeStakePayload(&StakePayload{Amount: amt})),
		Address{1}, Address{}, 1); err != nil {
		t.Fatal(err)
	}
	balAfterStake := st.Balance(addr)

	const unstakeHeight = 100
	if err := st.ApplyTransaction(
		signed(t, key, TxUnstake, 1, StakeFee(amt), encodeStakePayload(&StakePayload{Amount: amt})),
		Address{1}, Address{}, unstakeHeight); err != nil {
		t.Fatalf("Unstake abgelehnt: %v", err)
	}

	if got := st.Stake(addr); got.Sign() != 0 {
		t.Fatalf("aktiver Stake muss sofort 0 sein, war %s", got)
	}
	unb, unlock := st.Unbonding(addr)
	if unb.Cmp(amt) != 0 {
		t.Fatalf("freiwerdend = %s, erwartet %s", unb, amt)
	}
	if unlock != unstakeHeight+UnbondingPeriod {
		t.Fatalf("Freigabehöhe = %d, erwartet %d", unlock, unstakeHeight+UnbondingPeriod)
	}

	// Einen Block vor Ablauf: noch nichts zurück.
	st.MatureUnbonding(unlock - 1)
	if got := st.Balance(addr); got.Cmp(new(big.Int).Sub(balAfterStake, StakeFee(amt))) != 0 {
		t.Fatalf("vor Ablauf der Frist darf nichts zurückfließen, Guthaben %s", got)
	}

	// Bei Ablauf: Betrag ist zurück.
	st.MatureUnbonding(unlock)
	want := new(big.Int).Add(new(big.Int).Sub(balAfterStake, StakeFee(amt)), amt)
	if got := st.Balance(addr); got.Cmp(want) != 0 {
		t.Fatalf("nach Ablauf Guthaben %s, erwartet %s", got, want)
	}
	if unb, _ := st.Unbonding(addr); unb.Sign() != 0 {
		t.Fatalf("freiwerdender Topf muss geleert sein, war %s", unb)
	}
}

// FND_021_UnstakeRestartsClock: Nachkündigen darf die Frist des gesamten Topfes
// nicht unterlaufen.
func TestFND_021_UnstakeRestartsClock(t *testing.T) {
	key := mustKey(t)
	addr := PubkeyToAddress(&key.PublicKey)
	st := NewState()
	st.Credit(addr, big.NewInt(1000*UFNDPerFND))

	big20 := big.NewInt(20 * UFNDPerFND)
	if err := st.ApplyTransaction(
		signed(t, key, TxStake, 0, StakeFee(big20), encodeStakePayload(&StakePayload{Amount: big20})),
		Address{1}, Address{}, 1); err != nil {
		t.Fatal(err)
	}
	ten := big.NewInt(10 * UFNDPerFND)
	if err := st.ApplyTransaction(
		signed(t, key, TxUnstake, 1, StakeFee(ten), encodeStakePayload(&StakePayload{Amount: ten})),
		Address{1}, Address{}, 100); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyTransaction(
		signed(t, key, TxUnstake, 2, StakeFee(ten), encodeStakePayload(&StakePayload{Amount: ten})),
		Address{1}, Address{}, 500); err != nil {
		t.Fatal(err)
	}
	_, unlock := st.Unbonding(addr)
	if unlock != 500+UnbondingPeriod {
		t.Fatalf("Frist = %d, erwartet %d — die zweite Kündigung muss die Uhr für den ganzen Topf neu stellen",
			unlock, 500+UnbondingPeriod)
	}
	// Zur alten Frist darf noch nichts frei werden.
	st.MatureUnbonding(100 + UnbondingPeriod)
	if unb, _ := st.Unbonding(addr); unb.Cmp(big20) != 0 {
		t.Fatalf("zur alten Frist wurde freigegeben: freiwerdend %s statt %s", unb, big20)
	}
}

// FND_021_Conservation ist die Invariante über allem: Stake bewegt Geld, es
// entsteht und verschwindet keines. Guthaben + Stake + freiwerdend bleibt über
// jede Folge von Stake-/Unstake-Operationen konstant.
func TestFND_021_Conservation(t *testing.T) {
	key := mustKey(t)
	addr := PubkeyToAddress(&key.PublicKey)
	collector := Address{9}
	st := NewState()
	start := big.NewInt(1000 * UFNDPerFND)
	st.Credit(addr, start)

	total := func() *big.Int {
		sum := new(big.Int).Add(st.Balance(addr), st.Stake(addr))
		unb, _ := st.Unbonding(addr)
		sum.Add(sum, unb)
		return sum.Add(sum, st.Balance(collector))
	}
	if total().Cmp(start) != 0 {
		t.Fatalf("Startsumme stimmt nicht: %s", total())
	}

	var nonce uint64
	amounts := []int64{5, 1, 50, 3, 12, 7}
	for i, a := range amounts {
		amt := big.NewInt(a * UFNDPerFND)
		typ := TxStake
		if i%2 == 1 && st.Stake(addr).Cmp(amt) >= 0 {
			typ = TxUnstake
		}
		tx := signed(t, key, typ, nonce, StakeFee(amt), encodeStakePayload(&StakePayload{Amount: amt}))
		if err := st.ApplyTransaction(tx, collector, Address{}, uint64(i+1)); err != nil {
			continue // abgelehnte Tx darf die Summe erst recht nicht ändern
		}
		nonce++
		if got := total(); got.Cmp(start) != 0 {
			t.Fatalf("nach Schritt %d (Typ 0x%02x): Summe %s, erwartet %s", i, byte(typ), got, start)
		}
	}
	st.MatureUnbonding(1_000_000)
	if got := total(); got.Cmp(start) != 0 {
		t.Fatalf("nach Reifung: Summe %s, erwartet %s", got, start)
	}
}

// ─── FND-022 · Validator-Set aus dem Zustand ─────────────────────────────────

func TestFND_022_SetHonorsMinimum(t *testing.T) {
	st := NewState()
	below := Address{1}
	exact := Address{2}
	above := Address{3}
	st.getOrCreateStake(below).Amount = new(big.Int).Sub(MinValidatorStake, big.NewInt(1))
	st.getOrCreateStake(exact).Amount = new(big.Int).Set(MinValidatorStake)
	st.getOrCreateStake(above).Amount = new(big.Int).Mul(MinValidatorStake, big.NewInt(3))

	got := st.StakedValidators()
	if len(got) != 2 {
		t.Fatalf("erwartet 2 Validatoren, bekam %d: %v", len(got), got)
	}
	for _, a := range got {
		if a == below {
			t.Fatal("Adresse unter dem Mindest-Stake darf nicht im Set sein")
		}
	}
}

// FND_022_SetIsDeterministic: alle Nodes müssen aus demselben Zustand dieselbe
// Reihenfolge berechnen, sonst weicht die Proposer-Zuordnung ab und die Kette
// forkt. Map-Iteration in Go ist absichtlich zufällig — genau deshalb der Test.
func TestFND_022_SetIsDeterministic(t *testing.T) {
	build := func() []Address {
		st := NewState()
		for i := byte(1); i <= 12; i++ {
			st.getOrCreateStake(Address{i}).Amount = new(big.Int).Mul(MinValidatorStake, big.NewInt(2))
		}
		return st.StakedValidators()
	}
	first := build()
	for i := 0; i < 50; i++ {
		next := build()
		if len(next) != len(first) {
			t.Fatalf("Länge schwankt: %d vs %d", len(first), len(next))
		}
		for j := range first {
			if first[j] != next[j] {
				t.Fatalf("Reihenfolge schwankt an Position %d: %x vs %x", j, first[j], next[j])
			}
		}
	}
}

func TestFND_022_EmptySetIsAnError(t *testing.T) {
	st := NewState()
	st.getOrCreateStake(Address{1}).Amount = big.NewInt(1) // unter dem Minimum
	if _, err := ValidatorSetFromState(st); err == nil {
		t.Fatal("leeres Validator-Set muss ein Fehler sein, kein stiller Leerlauf")
	}
}

// FND_022_UnbondingLeavesSet: gekündigter Stake zählt sofort nicht mehr fürs
// Set — auch wenn das Geld noch gesperrt ist.
func TestFND_022_UnbondingLeavesSet(t *testing.T) {
	key := mustKey(t)
	addr := PubkeyToAddress(&key.PublicKey)
	st := NewState()
	st.Credit(addr, big.NewInt(1000*UFNDPerFND))

	amt := new(big.Int).Mul(MinValidatorStake, big.NewInt(2))
	if err := st.ApplyTransaction(
		signed(t, key, TxStake, 0, StakeFee(amt), encodeStakePayload(&StakePayload{Amount: amt})),
		Address{9}, Address{}, 1); err != nil {
		t.Fatal(err)
	}
	if len(st.StakedValidators()) != 1 {
		t.Fatal("nach Stake muss die Adresse im Set sein")
	}
	if err := st.ApplyTransaction(
		signed(t, key, TxUnstake, 1, StakeFee(amt), encodeStakePayload(&StakePayload{Amount: amt})),
		Address{9}, Address{}, 2); err != nil {
		t.Fatal(err)
	}
	if n := len(st.StakedValidators()); n != 0 {
		t.Fatalf("nach Unstake darf die Adresse nicht mehr im Set sein, Set-Größe %d", n)
	}
}

// FND_022_StakeChangesStateRoot: der Stake muss committet sein. Wäre er es
// nicht, könnten zwei Nodes mit verschiedenen Validator-Sets denselben
// State-Root melden.
func TestFND_022_StakeChangesStateRoot(t *testing.T) {
	st := NewState()
	before := st.Root()
	st.getOrCreateStake(Address{1}).Amount = new(big.Int).Set(MinValidatorStake)
	if st.Root() == before {
		t.Fatal("Stake fließt nicht in den State-Root ein")
	}
}

// ─── Helfer ──────────────────────────────────────────────────────────────────

func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func signed(t *testing.T, key *ecdsa.PrivateKey, typ TxType, nonce uint64, fee *big.Int, payload []byte) *Transaction {
	t.Helper()
	tx := &Transaction{Type: typ, Nonce: nonce, Fee: fee, Payload: payload}
	if err := SignTransaction(tx, key); err != nil {
		t.Fatal(err)
	}
	return tx
}
