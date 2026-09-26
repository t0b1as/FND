package api

// Deckungsprüfung beim Anlegen einer Order im FND-Shop.
//
// Eine Order wird nur angenommen, wenn die Wallet genug der jeweiligen Währung
// hat – sonst würde sie im Buch stehen, jemand nähme sie an, und der Swap
// scheiterte beim Sperren (die Gegenseite hätte umsonst gewartet).
//
//   Verkauf (FND → SOL): FND auf der FND-Adresse ≥ Menge + Sperrgebühr
//   Kauf    (SOL → FND): SOL auf der SOL-Adresse ≥ Menge × Preis + Gebührenpuffer
//
// Bereits in eigenen offenen Orders derselben Adresse gebundene Beträge werden
// abgezogen – dieselben FND/SOL lassen sich nicht mehrfach anbieten.

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"time"

	solana "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"

	"github.com/fundus/node/internal/chain"
)

// Puffer für Solana-Transaktionsgebühren und die Miete des HTLC-Kontos.
const solFeeBufferLamports = 5_000_000 // 0,005 SOL

// orderFundsError: Meldung für den Nutzer + Details für die Anzeige.
type orderFundsError struct {
	Msg    string
	Detail map[string]any
}

// solClaimFeeLamports: Wer SOL ERHÄLT, muss die Abholung auf Solana selbst
// bezahlen (Gebühr ~0,000005 SOL). Ohne jedes SOL scheitert sie mit "no record
// of a prior credit". 0,002 SOL deckt auch Wiederholungen großzügig ab.
const solClaimFeeLamports uint64 = 2_000_000

// checkSolClaimFee: hat die Solana-Wallet, die SOL erhalten soll, genug für die Abholgebühr?
func (s *Server) checkSolClaimFee(ctx context.Context, solAddr, who string) *orderFundsError {
	if solAddr == "" {
		return nil
	}
	bal, err := s.solBalanceLamports(ctx, solAddr)
	if err != nil {
		return &orderFundsError{Msg: "SOL-Guthaben für die Abholgebühr nicht prüfbar (" + err.Error() + ")"}
	}
	if bal < solClaimFeeLamports {
		return &orderFundsError{Msg: fmt.Sprintf("%s braucht etwas SOL für die Abholgebühr: mind. %.3f SOL, vorhanden %.6f SOL (Menü oben rechts → Solana-Wallet → Adresse, dorthin SOL senden)",
			who, float64(solClaimFeeLamports)/1e9, float64(bal)/1e9)}
	}
	return nil
}

func (s *Server) checkOrderFunds(ctx context.Context, side OrderSide, typ OrderType, amountFND, priceSOL float64, fndAddr, solAddr string) *orderFundsError {
	switch side {
	case OrderSell:
		if e := s.checkFNDFunds(amountFND, fndAddr); e != nil {
			return e
		}
		// Verkäufer erhält SOL → muss die Abholung bezahlen können.
		return s.checkSolClaimFee(ctx, solAddr, "Deine Solana-Wallet")
	case OrderBuy:
		return s.checkSOLFunds(ctx, typ, amountFND, priceSOL, solAddr)
	}
	return nil
}

// ── Verkauf: FND ────────────────────────────────────────────────────────────

func (s *Server) checkFNDFunds(amountFND float64, fndAddr string) *orderFundsError {
	if s.chain == nil {
		return &orderFundsError{Msg: "FND-Guthaben nicht prüfbar – dieser Node hat keine Chain-Anbindung"}
	}
	if fndAddr == "" {
		return &orderFundsError{Msg: "Für einen Verkauf wird deine FND-Adresse gebraucht – bitte FND-Seed eingeben"}
	}
	addr, ok := chain.AddressFromHex(fndAddr)
	if !ok {
		return &orderFundsError{Msg: "Ungültige FND-Adresse"}
	}
	bal, _ := new(big.Int).SetString(s.chain.Balance(addr), 10)
	if bal == nil {
		bal = big.NewInt(0)
	}
	amt := fndToUFND(amountFND)
	fee := chain.FeeForValue(amt)
	committed := big.NewInt(0)
	for _, o := range s.myOpenOrders() {
		if o.Side == OrderSell && o.FndAddress == fndAddr && o.AmountFND > 0 {
			a := fndToUFND(o.AmountFND)
			committed.Add(committed, a)
			committed.Add(committed, chain.FeeForValue(a))
		}
	}
	need := new(big.Int).Add(amt, fee)
	need.Add(need, committed)
	if bal.Cmp(need) >= 0 {
		return nil
	}
	avail := new(big.Int).Sub(bal, committed)
	if avail.Sign() < 0 {
		avail.SetInt64(0)
	}
	msg := fmt.Sprintf("Nicht genug FND: verfügbar %.4f FND, benötigt %.4f FND (inkl. %.4f FND Sperrgebühr)",
		uToFNDFloat(avail), uToFNDFloat(new(big.Int).Add(amt, fee)), uToFNDFloat(fee))
	if committed.Sign() > 0 {
		msg += fmt.Sprintf(" – %.4f FND sind bereits in offenen Verkaufs-Orders gebunden", uToFNDFloat(committed))
	}
	return &orderFundsError{Msg: msg, Detail: map[string]any{
		"currency": "FND", "balance": uToFNDFloat(bal), "committed": uToFNDFloat(committed),
		"needed": uToFNDFloat(new(big.Int).Add(amt, fee)), "address": fndAddr,
	}}
}

// ── Kauf: SOL ───────────────────────────────────────────────────────────────

func (s *Server) checkSOLFunds(ctx context.Context, typ OrderType, amountFND, priceSOL float64, solAddr string) *orderFundsError {
	if solAddr == "" {
		return &orderFundsError{Msg: "Für einen Kauf wird deine SOL-Adresse gebraucht – bitte SOL-Schlüssel eingeben"}
	}
	pk, err := solana.PublicKeyFromBase58(solAddr)
	if err != nil {
		return &orderFundsError{Msg: "Ungültige SOL-Adresse"}
	}
	if s.swapMgr == nil || s.swapMgr.solRPC == "" {
		return &orderFundsError{Msg: "SOL-Guthaben nicht prüfbar – Solana ist auf diesem Node nicht eingerichtet"}
	}

	// Kosten der neuen Order in SOL.
	cost := amountFND * priceSOL
	if typ == OrderMarket {
		var filled float64
		cost, filled = s.marketBuyCost(amountFND)
		if filled <= 0 {
			return &orderFundsError{Msg: "Keine Verkaufsangebote im Buch – für einen Kauf bitte eine Limit-Order mit Preis anlegen"}
		}
	}
	// In offenen Kauf-Orders derselben Adresse gebundenes SOL.
	var committed float64
	for _, o := range s.myOpenOrders() {
		if o.Side == OrderBuy && o.SolAddress == solAddr && o.PriceSOL > 0 {
			committed += o.AmountFND * o.PriceSOL
		}
	}

	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	res, err := rpc.New(s.swapMgr.solRPC).GetBalance(cctx, pk, rpc.CommitmentConfirmed)
	if err != nil || res == nil {
		return &orderFundsError{Msg: "SOL-Guthaben nicht abrufbar – Order nicht angelegt. " + solRPCErr(err, s.swapMgr.solRPC).Error()}
	}
	balLamports := res.Value
	needLamports := uint64(math.Ceil((cost+committed)*1e9)) + solFeeBufferLamports
	if balLamports >= needLamports {
		return nil
	}
	bal := float64(balLamports) / 1e9
	avail := math.Max(0, bal-committed)
	msg := fmt.Sprintf("Nicht genug SOL: verfügbar %.6f SOL, benötigt %.6f SOL (inkl. %.3f SOL für Gebühren)",
		avail, cost+float64(solFeeBufferLamports)/1e9, float64(solFeeBufferLamports)/1e9)
	if committed > 0 {
		msg += fmt.Sprintf(" – %.6f SOL sind bereits in offenen Kauf-Orders gebunden", committed)
	}
	return &orderFundsError{Msg: msg, Detail: map[string]any{
		"currency": "SOL", "balance": bal, "committed": committed,
		"needed": cost + float64(solFeeBufferLamports)/1e9, "address": solAddr,
	}}
}

// marketBuyCost schätzt die Kosten eines Market-Kaufs anhand der günstigsten
// Verkaufsangebote im Buch. Liefert (Kosten in SOL, davon erfüllbare FND).
func (s *Server) marketBuyCost(amountFND float64) (cost, filled float64) {
	if s.orderBook == nil {
		return 0, 0
	}
	_, others := s.orderBook.SnapshotOrders()
	asks := make([]*Order, 0, len(others))
	now := time.Now().Unix()
	for _, o := range others {
		if o.Side == OrderSell && o.PriceSOL > 0 && o.AmountFND > 0 && (o.ExpiresAt == 0 || o.ExpiresAt > now) {
			asks = append(asks, o)
		}
	}
	sort.Slice(asks, func(i, j int) bool { return asks[i].PriceSOL < asks[j].PriceSOL })
	rest := amountFND
	for _, o := range asks {
		if rest <= 0 {
			break
		}
		take := math.Min(rest, o.AmountFND)
		cost += take * o.PriceSOL
		filled += take
		rest -= take
	}
	return cost, filled
}

func (s *Server) myOpenOrders() []*Order {
	if s.orderBook == nil {
		return nil
	}
	return s.orderBook.listMyOrders()
}

// ── Prüfung beim ANNEHMEN einer Order ───────────────────────────────────────
//
// Beim Annehmen sperrt der Annehmende (Taker) ZUERST. Fehlt dem Anbieter
// (Maker) inzwischen das Guthaben (nach dem Anlegen abgehoben), wären die
// Mittel des Takers bis zum Ablauf der Zeitsperre (~48 h) gebunden. Deshalb
// vor dem Start BEIDE Seiten prüfen – Guthaben sind auf beiden Chains öffentlich.

func (s *Server) fndBalance(addrHex string) (*big.Int, bool) {
	if s.chain == nil {
		return nil, false
	}
	a, ok := chain.AddressFromHex(addrHex)
	if !ok {
		return nil, false
	}
	bal, _ := new(big.Int).SetString(s.chain.Balance(a), 10)
	if bal == nil {
		bal = big.NewInt(0)
	}
	return bal, true
}

func (s *Server) solBalanceLamports(ctx context.Context, addr string) (uint64, error) {
	if s.swapMgr == nil || s.swapMgr.solRPC == "" {
		return 0, fmt.Errorf("Solana ist auf diesem Node nicht eingerichtet")
	}
	pk, err := solana.PublicKeyFromBase58(addr)
	if err != nil {
		return 0, fmt.Errorf("ungültige SOL-Adresse")
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	res, err := rpc.New(s.swapMgr.solRPC).GetBalance(cctx, pk, rpc.CommitmentConfirmed)
	if err != nil || res == nil {
		return 0, solRPCErr(err, s.swapMgr.solRPC)
	}
	return res.Value, nil
}

// checkTakeFunds prüft vor dem Swap-Start, ob Annehmender UND Anbieter ihren
// Teil decken können. takerGivesSol: Anbieter verkauft FND, Annehmender zahlt SOL.
func (s *Server) checkTakeFunds(ctx context.Context, order *Order, amountFND float64, takerGivesSol bool,
	takerSolAddr, takerFndAddr, excludeOrderID string) *orderFundsError {
	amountSOL := amountFND * order.PriceSOL
	amtU := fndToUFND(amountFND)
	feeU := chain.FeeForValue(amtU)
	needFNDU := new(big.Int).Add(amtU, feeU)
	needSOLLamports := uint64(math.Ceil(amountSOL*1e9)) + solFeeBufferLamports

	if takerGivesSol {
		// 1. Annehmender zahlt SOL.
		if takerSolAddr == "" {
			return &orderFundsError{Msg: "Für den Kauf wird deine SOL-Adresse gebraucht – bitte SOL-Schlüssel eingeben"}
		}
		bal, err := s.solBalanceLamports(ctx, takerSolAddr)
		if err != nil {
			return &orderFundsError{Msg: "Dein SOL-Guthaben ist nicht prüfbar (" + err.Error() + ") – Swap nicht gestartet"}
		}
		var committed float64
		for _, o := range s.myOpenOrders() {
			if o.ID != excludeOrderID && o.Side == OrderBuy && o.SolAddress == takerSolAddr && o.PriceSOL > 0 {
				committed += o.AmountFND * o.PriceSOL
			}
		}
		if bal < needSOLLamports+uint64(math.Ceil(committed*1e9)) {
			msg := fmt.Sprintf("Nicht genug SOL: verfügbar %.6f SOL, benötigt %.6f SOL (inkl. %.3f SOL für Gebühren)",
				math.Max(0, float64(bal)/1e9-committed), float64(needSOLLamports)/1e9, float64(solFeeBufferLamports)/1e9)
			if committed > 0 {
				msg += fmt.Sprintf(" – %.6f SOL sind in deinen offenen Kauf-Orders gebunden", committed)
			}
			return &orderFundsError{Msg: msg}
		}
		// 2. Anbieter liefert FND.
		mbal, ok := s.fndBalance(order.FndAddress)
		if !ok {
			return &orderFundsError{Msg: "FND-Guthaben des Anbieters nicht prüfbar – Swap nicht gestartet"}
		}
		if mbal.Cmp(needFNDU) < 0 {
			return &orderFundsError{Msg: fmt.Sprintf(
				"Der Anbieter hat nicht mehr genug FND (%.4f FND vorhanden, %.4f FND nötig) – die Order ist nicht mehr gedeckt",
				uToFNDFloat(mbal), uToFNDFloat(needFNDU)), Detail: map[string]any{"maker_uncovered": true}}
		}
		// Anbieter erhält SOL → muss die Abholung bezahlen können (sonst bliebe
		// der Swap nach dem FND-Tausch bei der SOL-Abholung hängen).
		if e := s.checkSolClaimFee(ctx, order.SolAddress, "Die Solana-Wallet des Anbieters"); e != nil {
			e.Detail = map[string]any{"maker_uncovered": true}
			return e
		}
		return nil
	}

	// Annehmender liefert FND, Anbieter zahlt SOL.
	// 1. Annehmender: FND + Sperrgebühr.
	if takerFndAddr == "" {
		return &orderFundsError{Msg: "Für den Verkauf wird deine FND-Adresse gebraucht – bitte FND-Seed eingeben"}
	}
	tbal, ok := s.fndBalance(takerFndAddr)
	if !ok {
		return &orderFundsError{Msg: "Dein FND-Guthaben ist nicht prüfbar – Swap nicht gestartet"}
	}
	committed := big.NewInt(0)
	for _, o := range s.myOpenOrders() {
		if o.ID != excludeOrderID && o.Side == OrderSell && o.FndAddress == takerFndAddr && o.AmountFND > 0 {
			a := fndToUFND(o.AmountFND)
			committed.Add(committed, a)
			committed.Add(committed, chain.FeeForValue(a))
		}
	}
	if tbal.Cmp(new(big.Int).Add(needFNDU, committed)) < 0 {
		avail := new(big.Int).Sub(tbal, committed)
		if avail.Sign() < 0 {
			avail.SetInt64(0)
		}
		msg := fmt.Sprintf("Nicht genug FND: verfügbar %.4f FND, benötigt %.4f FND (inkl. %.4f FND Sperrgebühr)",
			uToFNDFloat(avail), uToFNDFloat(needFNDU), uToFNDFloat(feeU))
		if committed.Sign() > 0 {
			msg += fmt.Sprintf(" – %.4f FND sind in deinen offenen Verkaufs-Orders gebunden", uToFNDFloat(committed))
		}
		return &orderFundsError{Msg: msg}
	}
	// Annehmender erhält SOL → Abholgebühr.
	if e := s.checkSolClaimFee(ctx, takerSolAddr, "Deine Solana-Wallet"); e != nil {
		return e
	}
	// 2. Anbieter zahlt SOL.
	mbal, err := s.solBalanceLamports(ctx, order.SolAddress)
	if err != nil {
		return &orderFundsError{Msg: "SOL-Guthaben des Anbieters nicht prüfbar (" + err.Error() + ") – Swap nicht gestartet"}
	}
	if mbal < needSOLLamports {
		return &orderFundsError{Msg: fmt.Sprintf(
			"Der Anbieter hat nicht mehr genug SOL (%.6f SOL vorhanden, %.6f SOL nötig) – die Order ist nicht mehr gedeckt",
			float64(mbal)/1e9, float64(needSOLLamports)/1e9), Detail: map[string]any{"maker_uncovered": true}}
	}
	return nil
}
