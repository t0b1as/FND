package chain

// Zustandsabbilder (R704).
//
// ── Das Problem ────────────────────────────────────────────────────────────
//
// Bisher wurde beim Start die GESAMTE Chain neu durchgerechnet: Block 1 bis zur
// aktuellen Höhe, jeder einzeln von der Platte geladen und angewendet. Da alle
// fünf Sekunden ein Block entsteht – auch ohne Transaktionen, weil die Fristen
// der FND-Sperren in Blöcken zählen – wächst die Blockzahl mit der Uhr, nicht
// mit der Nutzung. Nach einem Monat waren das über 500.000 Blöcke, nach einem
// Jahr über sechs Millionen. Der Start dauerte entsprechend Minuten bis
// Stunden, und nach einem Update wäre das Netz so lange leer.
//
// ── Die Lösung ─────────────────────────────────────────────────────────────
//
// Alle paar tausend Blöcke wird der Zustand vollständig abgelegt. Beim Start
// wird das jüngste brauchbare Abbild geladen und nur der Rest nachgerechnet.
// Die Startdauer hängt damit nicht mehr von der Laufzeit ab.
//
// ── Warum das sicher ist ───────────────────────────────────────────────────
//
// Ein Abbild wird NIE blind geglaubt. Nach dem Laden wird die Zustandswurzel
// berechnet und gegen die im Blockkopf dieser Höhe gespeicherte verglichen.
// Weichen sie ab, wird das Abbild verworfen und ganz normal durchgerechnet.
//
// Damit ist ein Fehler in dieser Datei nicht gefährlich, sondern nur
// wirkungslos: Im schlimmsten Fall startet der Node so langsam wie vorher.
// Die Blöcke bleiben die Wahrheit, das Abbild ist nur eine Abkürzung.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SnapshotInterval: Abstand zwischen zwei Abbildern in Blöcken.
// 2000 Blöcke sind bei 5 s Blockzeit knapp drei Stunden – im schlimmsten Fall
// müssen also rund 2000 Blöcke nachgerechnet werden, das sind Sekundenbruchteile.
const SnapshotInterval uint64 = 2000

// snapshotKeep: so viele Abbilder bleiben liegen. Mehrere, weil ein einzelnes
// beschädigtes sonst den Weg zur Abkürzung ganz versperren würde.
const snapshotKeep = 3

// ── Serialisierbare Abbilder der State-Einträge ────────────────────────────
//
// Eigene Typen statt der internen: Die internen Felder sind teils nicht
// exportiert, und ein Abbild muss unabhängig von internen Umbauten lesbar
// bleiben. Beträge als Text, damit große Zahlen verlustfrei durchgehen.

type snapAccount struct {
	Balance string `json:"b"`
	Nonce   uint64 `json:"n"`
}

type snapEscrow struct {
	ID             string            `json:"id"`
	Buyer          string            `json:"buyer"`
	Seller         string            `json:"seller"`
	Amount         string            `json:"amount"`
	Deadline       uint64            `json:"deadline"`
	ContentHash    string            `json:"content"`
	State          uint8             `json:"state"`
	Insured        bool              `json:"insured,omitempty"`
	Premium        string            `json:"premium,omitempty"`
	Jurors         []string          `json:"jurors,omitempty"`
	Votes          map[string]uint8  `json:"votes,omitempty"`
	ReturnDeadline uint64            `json:"ret_deadline,omitempty"`
	TrackingHash   string            `json:"tracking,omitempty"`
}

type snapHTLC struct {
	ID        string `json:"id"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Amount    string `json:"amount"`
	Hashlock  string `json:"hashlock"`
	Timelock  uint64 `json:"timelock"`
	State     uint8  `json:"state"`
	Preimage  string `json:"preimage,omitempty"`
}

type snapStake struct {
	Amount       string `json:"amount"`
	Unbonding    string `json:"unbonding"`
	UnlockHeight uint64 `json:"unlock"`
}

type snapMeter struct {
	Producer       string `json:"producer"`
	Commodity      uint8  `json:"commodity"`
	Unit           uint8  `json:"unit"`
	LastCumulative string `json:"last_cum"`
	LastTimestamp  uint64 `json:"last_ts"`
	CertifiedTotal string `json:"cert_total"`
}

type snapToken struct {
	Producer    string `json:"producer"`
	Commodity   uint8  `json:"commodity"`
	Unit        uint8  `json:"unit"`
	Amount      string `json:"amount"`
	PeriodStart uint64 `json:"start"`
	PeriodEnd   uint64 `json:"end"`
	RawDataHash string `json:"raw"`
	Settled     bool   `json:"settled,omitempty"`
}

// stateSnapshot ist der gesamte abgelegte Zustand.
type stateSnapshot struct {
	SchemaVersion uint8  `json:"schema"`
	Height        uint64 `json:"height"`
	HeadHash      string `json:"head"`
	StateRoot     string `json:"root"` // zur Prüfung beim Laden

	Accounts map[string]snapAccount       `json:"accounts"`
	Escrows  map[string]snapEscrow        `json:"escrows,omitempty"`
	HTLCs    map[string]snapHTLC          `json:"htlcs,omitempty"`
	Stakes   map[string]snapStake         `json:"stakes,omitempty"`
	Slashed  map[string]map[string]uint64 `json:"slashed,omitempty"`
	Meters   map[string]snapMeter         `json:"meters,omitempty"`
	Tokens   map[string]snapToken         `json:"tokens,omitempty"`
	Receipts []string                     `json:"receipts,omitempty"`
	SolTxs   []string                     `json:"sol_txs,omitempty"`
}

// ── Hex-Hilfen ────────────────────────────────────────────────────────────
//
// hexTo32 gibt es bereits (wire.go), meldet aber keinen Fehler bei falscher
// Länge – es füllt still mit Nullen. Für ein Abbild ist das zu wenig: Eine
// abgeschnittene Kennung würde zu einem gültig aussehenden, aber falschen
// Eintrag. Deshalb hier Varianten, die den Fehler melden.

func hex32(v [32]byte) string { return hex.EncodeToString(v[:]) }

func hexBytes(b []byte) string { return hex.EncodeToString(b) }

func hash32FromHex(s string) ([32]byte, bool) {
	var out [32]byte
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return out, true // leeres Feld = Nullwert, das ist zulässig
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return out, false
	}
	copy(out[:], b)
	return out, true
}

func bytesFromHex(s string, laenge int) ([]byte, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != laenge {
		return nil, false
	}
	return b, true
}

func snapDir(chainDir string) string { return filepath.Join(chainDir, "snapshots") }

func snapPath(chainDir string, height uint64) string {
	return filepath.Join(snapDir(chainDir), fmt.Sprintf("state-%012d.json", height))
}

func bigToS(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

func sToBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok {
		return big.NewInt(0)
	}
	return v
}

// ── Schreiben ──────────────────────────────────────────────────────────────

// WriteSnapshot legt den aktuellen Zustand ab. Fehler sind nicht schlimm – das
// Abbild ist nur eine Abkürzung, kein Teil der Wahrheit.
func (bc *Blockchain) WriteSnapshot() error {
	bc.mu.RLock()
	st := bc.state
	head := bc.head
	height := bc.height
	bc.mu.RUnlock()
	return bc.schreibeAbbild(st, head, height)
}

// schreibeAbbildLocked wird aufgerufen, wenn die Sperre BEREITS gehalten wird
// (aus ProduceBlockRound und ImportBlock heraus).
//
// Diese Trennung ist kein Schönheitsfehler, sondern notwendig: Go-Sperren sind
// nicht wiedereintrittsfähig. Würde die gesperrte Stelle WriteSnapshot rufen,
// das die Sperre erneut anfordert, stünde der Node still. Asynchron schreiben
// wäre die andere Möglichkeit gewesen, aber dann läse das Abbild den Zustand,
// während der nächste Block ihn schon verändert – ein Datenrennen mitten im
// Konsens.
func (bc *Blockchain) schreibeAbbildLocked() error {
	return bc.schreibeAbbild(bc.state, bc.head, bc.height)
}

func (bc *Blockchain) schreibeAbbild(st *State, head *BlockHeader, height uint64) error {
	if st == nil || head == nil {
		return fmt.Errorf("kein Zustand zum Ablegen")
	}

	snap := stateSnapshot{
		SchemaVersion: StateSchemaVersion,
		Height:        height,
		HeadHash:      hex32(head.Hash()),
		StateRoot:     hex32(st.Root()),
		Accounts:      map[string]snapAccount{},
	}

	for a, acct := range st.accounts {
		if acct == nil || (acct.Balance.Sign() == 0 && acct.Nonce == 0) {
			continue // leere Konten fließen auch nicht in die Wurzel ein
		}
		snap.Accounts[a.Hex()] = snapAccount{Balance: bigToS(acct.Balance), Nonce: acct.Nonce}
	}
	if len(st.escrows) > 0 {
		snap.Escrows = map[string]snapEscrow{}
		for id, e := range st.escrows {
			if e == nil {
				continue
			}
			se := snapEscrow{
				ID: hex32(e.ID), Buyer: e.Buyer.Hex(), Seller: e.Seller.Hex(),
				Amount: bigToS(e.Amount), Deadline: e.Deadline,
				ContentHash: hex32(e.ContentHash), State: uint8(e.State),
				Insured: e.Insured, Premium: bigToS(e.Premium),
				ReturnDeadline: e.ReturnDeadline, TrackingHash: hex32(e.TrackingHash),
			}
			for _, j := range e.Jurors {
				se.Jurors = append(se.Jurors, j.Hex())
			}
			if len(e.Votes) > 0 {
				se.Votes = map[string]uint8{}
				for addr, v := range e.Votes {
					se.Votes[addr.Hex()] = uint8(v)
				}
			}
			snap.Escrows[hex32(id)] = se
		}
	}
	if len(st.htlcs) > 0 {
		snap.HTLCs = map[string]snapHTLC{}
		for id, h := range st.htlcs {
			if h == nil {
				continue
			}
			snap.HTLCs[hex32(id)] = snapHTLC{
				ID: hex32(h.ID), Sender: h.Sender.Hex(), Recipient: h.Recipient.Hex(),
				Amount: bigToS(h.Amount), Hashlock: hex32(h.Hashlock),
				Timelock: h.Timelock, State: uint8(h.State), Preimage: hex32(h.Preimage),
			}
		}
	}
	if len(st.stakes) > 0 {
		snap.Stakes = map[string]snapStake{}
		for a, s := range st.stakes {
			if s == nil {
				continue
			}
			snap.Stakes[a.Hex()] = snapStake{
				Amount: bigToS(s.Amount), Unbonding: bigToS(s.Unbonding),
				UnlockHeight: s.UnlockHeight,
			}
		}
	}
	if len(st.slashed) > 0 {
		snap.Slashed = map[string]map[string]uint64{}
		for a, m := range st.slashed {
			inner := map[string]uint64{}
			for evidence, angewandt := range m {
				inner[strconv.FormatUint(evidence, 10)] = angewandt
			}
			snap.Slashed[a.Hex()] = inner
		}
	}
	if len(st.meters) > 0 {
		snap.Meters = map[string]snapMeter{}
		for id, m := range st.meters {
			if m == nil {
				continue
			}
			snap.Meters[hexBytes(id[:])] = snapMeter{
				Producer: m.Producer.Hex(), Commodity: m.Commodity, Unit: m.Unit,
				LastCumulative: bigToS(m.LastCumulative), LastTimestamp: m.LastTimestamp,
				CertifiedTotal: bigToS(m.CertifiedTotal),
			}
		}
	}
	if len(st.tokens) > 0 {
		snap.Tokens = map[string]snapToken{}
		for id, t := range st.tokens {
			if t == nil {
				continue
			}
			snap.Tokens[hex32(id)] = snapToken{
				Producer: t.Producer.Hex(), Commodity: t.Commodity, Unit: t.Unit,
				Amount: bigToS(t.Amount), PeriodStart: t.PeriodStart,
				PeriodEnd: t.PeriodEnd, RawDataHash: hex32(t.RawDataHash),
				Settled: t.Settled,
			}
		}
	}
	for id := range st.redeemedReceipts {
		snap.Receipts = append(snap.Receipts, hex32(id))
	}
	sort.Strings(snap.Receipts)
	for id := range st.redeemedSolTxs {
		snap.SolTxs = append(snap.SolTxs, hex32(id))
	}
	sort.Strings(snap.SolTxs)

	if err := os.MkdirAll(snapDir(bc.dir), 0o750); err != nil {
		return err
	}
	raw, err := json.Marshal(&snap)
	if err != nil {
		return err
	}
	// Erst daneben schreiben, dann umbenennen: Ein abgebrochener Schreibvorgang
	// hinterlässt sonst eine halbe Datei, die beim nächsten Start als Abbild
	// gilt. Das Umbenennen ist auf einem Dateisystem unteilbar.
	pfad := snapPath(bc.dir, snap.Height)
	tmp := pfad + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, pfad); err != nil {
		os.Remove(tmp)
		return err
	}
	bc.pruneSnapshots()
	return nil
}

// pruneSnapshots lässt nur die jüngsten Abbilder liegen.
func (bc *Blockchain) pruneSnapshots() {
	eintraege, err := os.ReadDir(snapDir(bc.dir))
	if err != nil {
		return
	}
	hoehen := []uint64{}
	for _, e := range eintraege {
		if h, ok := snapHeightFromName(e.Name()); ok {
			hoehen = append(hoehen, h)
		}
	}
	if len(hoehen) <= snapshotKeep {
		return
	}
	sort.Slice(hoehen, func(i, j int) bool { return hoehen[i] > hoehen[j] })
	for _, h := range hoehen[snapshotKeep:] {
		os.Remove(snapPath(bc.dir, h))
	}
}

func snapHeightFromName(name string) (uint64, bool) {
	if !strings.HasPrefix(name, "state-") || !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	mitte := strings.TrimSuffix(strings.TrimPrefix(name, "state-"), ".json")
	h, err := strconv.ParseUint(mitte, 10, 64)
	if err != nil {
		return 0, false
	}
	return h, true
}

// ── Laden ──────────────────────────────────────────────────────────────────

// ladeAbbild sucht das jüngste brauchbare Abbild bis einschließlich maxHeight
// und gibt Zustand und Höhe zurück. Ohne brauchbares Abbild: (nil, 0, nil) –
// das ist KEIN Fehler, dann wird eben ganz durchgerechnet.
func (bc *Blockchain) ladeAbbild(maxHeight uint64) (*State, uint64, error) {
	eintraege, err := os.ReadDir(snapDir(bc.dir))
	if err != nil {
		return nil, 0, nil // kein Verzeichnis = noch keine Abbilder
	}
	hoehen := []uint64{}
	for _, e := range eintraege {
		if h, ok := snapHeightFromName(e.Name()); ok && h <= maxHeight {
			hoehen = append(hoehen, h)
		}
	}
	sort.Slice(hoehen, func(i, j int) bool { return hoehen[i] > hoehen[j] })

	for _, h := range hoehen {
		st, err := bc.leseAbbild(h)
		if err != nil {
			continue // beschädigt oder unpassend: nächstälteres versuchen
		}
		return st, h, nil
	}
	return nil, 0, nil
}

// leseAbbild liest EIN Abbild und prüft es gegen den Blockkopf dieser Höhe.
func (bc *Blockchain) leseAbbild(height uint64) (*State, error) {
	raw, err := os.ReadFile(snapPath(bc.dir, height))
	if err != nil {
		return nil, err
	}
	var snap stateSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	if snap.SchemaVersion != StateSchemaVersion {
		return nil, fmt.Errorf("Abbild hat Schema 0x%02x, erwartet 0x%02x",
			snap.SchemaVersion, StateSchemaVersion)
	}
	if snap.Height != height {
		return nil, fmt.Errorf("Abbild nennt Höhe %d, liegt aber unter %d", snap.Height, height)
	}

	st := NewState()
	for addrHex, a := range snap.Accounts {
		addr, ok := AddressFromHex(addrHex)
		if !ok {
			return nil, fmt.Errorf("ungültige Adresse im Abbild: %s", addrHex)
		}
		acct := st.getOrCreate(addr)
		acct.Balance = sToBig(a.Balance)
		acct.Nonce = a.Nonce
	}
	for idHex, e := range snap.Escrows {
		id, ok := hash32FromHex(idHex)
		if !ok {
			return nil, fmt.Errorf("ungültige Escrow-Kennung: %s", idHex)
		}
		buyer, ok1 := AddressFromHex(e.Buyer)
		seller, ok2 := AddressFromHex(e.Seller)
		ch, ok3 := hash32FromHex(e.ContentHash)
		th, _ := hash32FromHex(e.TrackingHash)
		if !ok1 || !ok2 || !ok3 {
			return nil, fmt.Errorf("ungültige Escrow-Felder: %s", idHex)
		}
		esc := &Escrow{
			ID: id, Buyer: buyer, Seller: seller, Amount: sToBig(e.Amount),
			Deadline: e.Deadline, ContentHash: ch, State: EscrowState(e.State),
			Insured: e.Insured, Premium: sToBig(e.Premium),
			ReturnDeadline: e.ReturnDeadline, TrackingHash: th,
		}
		for _, jh := range e.Jurors {
			if j, ok := AddressFromHex(jh); ok {
				esc.Jurors = append(esc.Jurors, j)
			}
		}
		if len(e.Votes) > 0 {
			esc.Votes = map[Address]Verdict{}
			for ah, v := range e.Votes {
				if a, ok := AddressFromHex(ah); ok {
					esc.Votes[a] = Verdict(v)
				}
			}
		}
		st.escrows[id] = esc
	}
	for idHex, h := range snap.HTLCs {
		id, ok := hash32FromHex(idHex)
		sender, ok1 := AddressFromHex(h.Sender)
		recip, ok2 := AddressFromHex(h.Recipient)
		hl, ok3 := hash32FromHex(h.Hashlock)
		pi, _ := hash32FromHex(h.Preimage)
		if !ok || !ok1 || !ok2 || !ok3 {
			return nil, fmt.Errorf("ungültige HTLC-Felder: %s", idHex)
		}
		st.htlcs[id] = &HTLC{
			ID: id, Sender: sender, Recipient: recip, Amount: sToBig(h.Amount),
			Hashlock: hl, Timelock: h.Timelock, State: HTLCState(h.State), Preimage: pi,
		}
	}
	for addrHex, s := range snap.Stakes {
		addr, ok := AddressFromHex(addrHex)
		if !ok {
			return nil, fmt.Errorf("ungültige Stake-Adresse: %s", addrHex)
		}
		st.stakes[addr] = &StakeRecord{
			Amount: sToBig(s.Amount), Unbonding: sToBig(s.Unbonding),
			UnlockHeight: s.UnlockHeight,
		}
	}
	for addrHex, m := range snap.Slashed {
		addr, ok := AddressFromHex(addrHex)
		if !ok {
			return nil, fmt.Errorf("ungültige Slash-Adresse: %s", addrHex)
		}
		inner := map[uint64]uint64{}
		for evHex, angewandt := range m {
			ev, err := strconv.ParseUint(evHex, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("ungültige Evidence-Höhe: %s", evHex)
			}
			inner[ev] = angewandt
		}
		st.slashed[addr] = inner
	}
	for idHex, m := range snap.Meters {
		b, ok := bytesFromHex(idHex, 11)
		prod, ok1 := AddressFromHex(m.Producer)
		if !ok || !ok1 {
			return nil, fmt.Errorf("ungültige Zähler-Kennung: %s", idHex)
		}
		var id [11]byte
		copy(id[:], b)
		st.meters[id] = &MeterRecord{
			Producer: prod, Commodity: m.Commodity, Unit: m.Unit,
			LastCumulative: sToBig(m.LastCumulative), LastTimestamp: m.LastTimestamp,
			CertifiedTotal: sToBig(m.CertifiedTotal),
		}
	}
	for idHex, t := range snap.Tokens {
		id, ok := hash32FromHex(idHex)
		prod, ok1 := AddressFromHex(t.Producer)
		raw, ok2 := hash32FromHex(t.RawDataHash)
		if !ok || !ok1 || !ok2 {
			return nil, fmt.Errorf("ungültige Token-Kennung: %s", idHex)
		}
		st.tokens[id] = &CommodityToken{
			Producer: prod, Commodity: t.Commodity, Unit: t.Unit,
			Amount: sToBig(t.Amount), PeriodStart: t.PeriodStart,
			PeriodEnd: t.PeriodEnd, RawDataHash: raw, Settled: t.Settled,
		}
	}
	for _, idHex := range snap.Receipts {
		if id, ok := hash32FromHex(idHex); ok {
			st.redeemedReceipts[id] = true
		}
	}
	for _, idHex := range snap.SolTxs {
		if id, ok := hash32FromHex(idHex); ok {
			st.redeemedSolTxs[id] = true
		}
	}

	// DIE entscheidende Prüfung: Stimmt die Wurzel des geladenen Zustands mit
	// der im Blockkopf dieser Höhe? Nur dann ist das Abbild brauchbar.
	blk, err := bc.loadBlock(height)
	if err != nil {
		return nil, fmt.Errorf("Block %d zum Abgleich fehlt: %w", height, err)
	}
	if st.Root() != blk.Header.StateRoot {
		return nil, fmt.Errorf("Wurzel des Abbilds weicht vom Block %d ab", height)
	}
	return st, nil
}
