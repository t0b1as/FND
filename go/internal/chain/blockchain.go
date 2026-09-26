package chain

import (
	"math/big"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ─── Phase 2: Single-Producer-Chain (Multi-Node-Sync, kein Konsens) ──────────────────────────────────────────────
// Eine Blockchain hält den aktuellen State + die Block-Historie und persistiert
// sie auf Platte. Block-Produktion erfolgt durch genau einen Node (Proposer =
// immer er) — kein Konsens. Der BFT-Konsens (Phase 3) setzt hierauf auf.

// Blockchain verwaltet Kette, State und Persistenz (thread-safe).
type Blockchain struct {
	mu           sync.RWMutex
	state        *State
	head         *BlockHeader   // Header des letzten Blocks
	height       uint64
	feeCollector Address
	genesisHash  [32]byte       // Hash des Genesis-Headers (für Peer-Vergleich)
	dir          string         // Persistenz-Verzeichnis (Blöcke + State)
	proposer     Address        // dieser Node (PoA: einer der Validatoren)
	valSet       *ValidatorSet  // aktives Validator-Set (PoA-Konsens)
	bootstrapVal *ValidatorSet  // konfiguriertes Set (FUNDUS_VALIDATORS) als Fallback,
	//                             solange kein ausreichender On-Chain-Stake existiert
	// learnedValidators sind Adressen, die dieser Node beim Sync von einem Peer als
	// dessen aktives Validator-Set erhalten hat — dezentrale Set-Verbreitung ohne
	// lokale Config. Fließen als zusätzliche Quelle in die Set-Ableitung ein.
	learnedValidators map[Address]bool
	signKey      *ecdsa.PrivateKey // Schlüssel dieses Nodes zum Signieren eigener Blöcke
	forkCollisions uint64       // Zähler: erkannte konkurrierende Blöcke gleicher Höhe (Fork-Indikator)
	// onDoubleSign wird aufgerufen, wenn ein konkurrierender Block auf gleicher
	// Höhe VOM SELBEN Signierer wie der akzeptierte Block stammt — das ist
	// beweisbares Double-Signing. Der Server hängt hier das Einreichen einer
	// TxSlash ein. Nil = keine automatische Meldung.
	onDoubleSign func(*SlashEvidence)
	// onImportTx nimmt eine über das Netz empfangene Transaktion in den Mempool
	// auf. Der Server hängt hier mempool.Add ein. Nil = keine Tx-Übernahme.
	onImportTx func(*Transaction) error
	store        BlockStore     // Block-Persistenz (bbolt); nil → Fallback auf JSON-Dateien
}

// chainMeta wird als JSON persistiert (schlanker Wiederanlauf-Anker).
type chainMeta struct {
	Height   uint64 `json:"height"`
	HeadHash string `json:"head_hash"`
	// SchemaVersion hält die State-Schema-Version fest, mit der diese Chain
	// geschrieben wurde. Beim Laden wird sie gegen StateSchemaVersion geprüft;
	// weicht sie ab, gilt die gespeicherte Chain als inkompatibel (Formatwechsel).
	SchemaVersion byte `json:"schema_version"`
}

// NewBlockchain initialisiert die Kette: lädt eine vorhandene von Platte oder
// legt aus den Genesis-Allokationen eine neue an. dir ist das Datenverzeichnis.
// NewBlockchain initialisiert die Kette. genesis ist der MITGELIEFERTE
// Genesis-Block (Höhe 0) — er definiert über seinen Header den Fee-Collector
// und ist über seinen Hash tamper-evident. expectedGenesisHash, falls gesetzt
// (nicht Null), wird gegen den tatsächlichen Genesis-Hash geprüft: weicht der
// mitgelieferte Genesis ab, startet die Kette nicht (Schutz gegen vertauschten
// Genesis). proposer = dieser Node (Phase 2: einziger Produzent).
func NewBlockchain(dir string, proposer Address, genesis *Block, expectedGenesisHash [32]byte) (*Blockchain, error) {
	if err := os.MkdirAll(filepath.Join(dir, "blocks"), 0o755); err != nil {
		return nil, fmt.Errorf("chain: Datenverzeichnis: %w", err)
	}
	// Genesis-Integrität: falls ein erwarteter Hash übergeben wurde, muss er passen.
	genHash := genesis.Header.Hash()
	var zero [32]byte
	if expectedGenesisHash != zero && genHash != expectedGenesisHash {
		return nil, fmt.Errorf("chain: Genesis-Hash weicht ab (erwartet %x, ist %x) — vertauschter Genesis?",
			expectedGenesisHash[:6], genHash[:6])
	}
	bc := &Blockchain{
		dir:          dir,
		proposer:     proposer,
		feeCollector: genesis.Header.FeeCollector, // EINZIGE Quelle: aus dem Genesis
		genesisHash:  genHash,
	}

	// Block-Persistenz öffnen (bbolt). Die DB liegt unter dir/chain.db.
	store, err := OpenBlockStore(dir)
	if err != nil {
		return nil, err
	}
	bc.store = store

	// Migrations-Wächter (FND-002): Entscheidung anhand vorhandener Daten,
	// nicht anhand von meta.json und nicht anhand von "DB komplett leer".
	// Der alte Test (!hasAny) schwieg, sobald der Genesis in chain.db lag —
	// der Node startete dann auf Höhe 0, während die Historie als JSON
	// danebenlag. Ohne Fehlermeldung.
	mig, migErr := InspectMigration(dir, store)
	if migErr != nil {
		store.Close()
		return nil, migErr
	}
	if NeedsMigration(mig) {
		store.Close()
		return nil, fmt.Errorf(
			"chain: JSON-Historie reicht bis Höhe %d, Blockstore nur bis %d — bitte zuerst migrieren: 'fundus-admin migrate-chain --dir %s'",
			mig.JSONHeight, mig.DBHeight, dir)
	}

	// Vorhandene Kette? → von Platte rekonstruieren.
	if _, err := os.Stat(filepath.Join(dir, "meta.json")); err == nil {
		if err := bc.load(); err != nil {
			return nil, fmt.Errorf("chain: Laden fehlgeschlagen: %w", err)
		}
		// Geladener Genesis muss zum mitgelieferten passen (kein Untertausch auf Platte).
		persistedGen, err := bc.loadBlock(0)
		if err != nil {
			return nil, fmt.Errorf("chain: persistierter Genesis nicht lesbar: %w", err)
		}
		if persistedGen.Header.Hash() != genHash {
			return nil, fmt.Errorf("chain: persistierter Genesis weicht vom mitgelieferten ab")
		}
		return bc, nil
	}

	// Sonst: mitgelieferten Genesis als State übernehmen und persistieren.
	st := NewState()
	for _, a := range genesis.GenesisAllocs {
		st.Credit(a.Address, a.Balance)
	}
	if st.Root() != genesis.Header.StateRoot {
		return nil, fmt.Errorf("chain: Genesis state_root inkonsistent zu den Allocs")
	}
	bc.state = st
	bc.head = &genesis.Header
	bc.height = 0
	if err := bc.persistBlock(genesis); err != nil {
		return nil, err
	}
	if err := bc.saveMeta(); err != nil {
		return nil, err
	}
	return bc, nil
}

// Height liefert die aktuelle Kettenhöhe.
func (bc *Blockchain) Height() uint64 {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.height
}

// AmIProposerNext meldet, ob dieser Node den nächsten Block (Höhe+1, Runde 0)
// selbst produzieren darf. Ohne aktiven PoA-Konsens (Solo-Betrieb) ist das immer
// der Fall. Mit Konsens nur dann, wenn dieser Node für die nächste Höhe der
// primäre Proposer ist UND einen Signier-Schlüssel hat. Der Transfer-Handler
// nutzt das, um bei Einreichung zu entscheiden: selbst sofort produzieren, oder
// die Tx dem Mempool überlassen, damit der zuständige Proposer sie einbaut.
func (bc *Blockchain) AmIProposerNext() bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.valSet == nil {
		return true // Solo-Betrieb: dieser Node produziert selbst
	}
	if bc.signKey == nil {
		return false // kein Schlüssel → kann ohnehin nicht produzieren
	}
	return bc.valSet.IsProposerForRound(bc.proposer, bc.height+1, 0)
}

// StakeOf liefert den aktiv gestakten Betrag einer Adresse (uFND).
func (bc *Blockchain) StakeOf(a Address) *big.Int {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.state == nil {
		return new(big.Int)
	}
	return bc.state.Stake(a)
}

// ConsensusSelf: eigene Validator-Adresse (Node-Wallet), ob ein Signier-
// schlüssel vorhanden ist und ob die Adresse im aktiven Validator-Set steht.
// Steht sie nicht darin, baut dieser Node keine Blöcke.
func (bc *Blockchain) ConsensusSelf() (addr string, canSign bool, inSet bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	addr = bc.proposer.Hex()
	canSign = bc.signKey != nil
	inSet = bc.valSet != nil && bc.valSet.Contains(bc.proposer)
	return
}

// HeadHash liefert den Hash des letzten Blocks.
func (bc *Blockchain) HeadHash() [32]byte {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.head.Hash()
}

// Balance liefert den Saldo einer Adresse (uFND).
func (bc *Blockchain) Balance(a Address) string {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.state.Balance(a).String()
}

// SetBridgeAuthority setzt die Adresse, die als einzige SOL-Credits einreichen
// darf (SOL→FND-Brücke). Muss auf allen Nodes identisch gesetzt sein.
func (bc *Blockchain) SetBridgeAuthority(a Address) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.state.SetBridgeAuthority(a)
}

// AccountInfo liefert Saldo (uFND) und Nonce einer Adresse.
func (bc *Blockchain) AccountInfo(a Address) (balance string, nonce uint64) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	acct := bc.state.GetAccount(a)
	return acct.Balance.String(), acct.Nonce
}

// ProduceBlock baut aus den übergebenen Transaktionen den nächsten Block,
// wendet ihn an und persistiert ihn. Phase 2: dieser Node ist der einzige
// Produzent (kein Konsens). Gibt den neuen Block zurück.

// Close schließt den Block-Persistenz-Store (bbolt) und gibt den Datei-Lock frei.
// MUSS aufgerufen werden, bevor dieselbe Chain-DB erneut geöffnet wird (sonst
// wartet der nächste Open auf den Lock). Der Node ruft es beim Shutdown, Tests
// beim Aufräumen.
func (bc *Blockchain) Close() error {
	if bc.store != nil {
		return bc.store.Close()
	}
	return nil
}

// ForkCollisions liefert die Anzahl erkannter konkurrierender Blöcke gleicher
// Höhe (Indikator für Fork-Situationen durch Fallback-Runden bei Partition oder
// Uhren-Drift). Steigt dieser Wert im Betrieb, sollten Uhren-Sync und
// Netz-Konnektivität der Validatoren geprüft werden.
func (bc *Blockchain) ForkCollisions() uint64 {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.forkCollisions
}

// SetDoubleSignHandler registriert einen Callback, der bei erkanntem Double-
// Signing die Evidence erhält (zum Einreichen einer TxSlash). Vom Server gesetzt.
func (bc *Blockchain) SetDoubleSignHandler(fn func(*SlashEvidence)) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.onDoubleSign = fn
}

// SetTxImporter registriert den Mempool-Aufnehmer für über das Netz empfangene
// Transaktionen. Vom Server gesetzt (mempool.Add).
func (bc *Blockchain) SetTxImporter(fn func(*Transaction) error) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.onImportTx = fn
}

// ImportTxJSON nimmt eine über GossipSub empfangene Transaktion (Wire-JSON) auf
// und legt sie über den registrierten Importer in den lokalen Mempool. So
// erreicht eine auf einem Node eingereichte Tx die anderen Nodes und kann vom
// zuständigen Proposer eingebaut werden.
func (bc *Blockchain) ImportTxJSON(data []byte) error {
	tx, err := TxFromWireJSON(data)
	if err != nil {
		return err
	}
	bc.mu.RLock()
	fn := bc.onImportTx
	bc.mu.RUnlock()
	if fn == nil {
		return nil // kein Importer gesetzt → still ignorieren
	}
	return fn(tx)
}

// maybeReportDoubleSign prüft, ob ein konkurrierender Block (competing) dasselbe
// signiert wurde wie der bereits akzeptierte Block gleicher Höhe. Ist der
// Signierer identisch, liegt Double-Signing vor → Evidence bauen und Handler
// aufrufen. Läuft unter bc.mu (Aufruf aus ImportBlock). Fehler werden geschluckt
// (Erkennung ist Best-Effort und darf den Import-Pfad nie stören).
func (bc *Blockchain) maybeReportDoubleSign(competing *Block) {
	if bc.onDoubleSign == nil {
		return
	}
	// Meinen akzeptierten Block gleicher Höhe laden (für dessen Signatur).
	accepted, err := bc.loadBlock(competing.Header.Height)
	if err != nil || accepted == nil {
		return
	}
	// Beide müssen signiert sein.
	if len(accepted.Commit) == 0 || len(competing.Commit) == 0 {
		return
	}
	signerAccepted, err := RecoverBlockSigner(accepted)
	if err != nil {
		return
	}
	signerCompeting, err := RecoverBlockSigner(competing)
	if err != nil {
		return
	}
	// Verschiedene Signierer → legitimer Fork, kein Double-Signing.
	if signerAccepted != signerCompeting {
		return
	}
	// Selber Signierer, verschiedene Header → Double-Signing bewiesen.
	ev := &SlashEvidence{
		Height:  competing.Header.Height,
		HeaderA: accepted.Header.bytes(),
		SigA:    accepted.Commit[0],
		HeaderB: competing.Header.bytes(),
		SigB:    competing.Commit[0],
	}
	// Gegenprobe mit derselben Logik, die auch die Chain nutzt — nur einreichen,
	// wenn der Beweis wirklich trägt.
	if _, err := VerifySlashEvidence(ev); err != nil {
		return
	}
	// Handler asynchron aufrufen, damit das Einreichen (Mempool/Signieren) den
	// Import-Pfad unter bc.mu nicht blockiert.
	go bc.onDoubleSign(ev)
}

// SetValidatorSet legt das aktive Validator-Set fest (PoA-Konsens). Muss vor der
// Block-Produktion gesetzt werden. signKey ist der Schlüssel dieses Nodes zum
// Signieren eigener Blöcke (darf nil sein, wenn dieser Node nur synchronisiert).
func (bc *Blockchain) SetConsensus(vs *ValidatorSet, signKey *ecdsa.PrivateKey) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	// vs ist das konfigurierte Bootstrap-Set (FUNDUS_VALIDATORS). Es bleibt der
	// Fallback, solange kein ausreichender On-Chain-Stake existiert.
	bc.bootstrapVal = vs
	bc.signKey = signKey
	bc.deriveValidatorSetLocked()
}

// deriveValidatorSetLocked bestimmt das aktive Validator-Set: das aus dem
// Stake-State abgeleitete Set, sobald mindestens ein Validator den Mindest-Stake
// erfüllt; sonst das konfigurierte Bootstrap-Set. So startet die Kette mit den
// konfigurierten Validatoren und geht automatisch auf stake-basierte Auswahl über,
// ohne dass jemand eine Config-Datei anfassen muss. Muss unter bc.mu laufen.
//
// KONSENS-KRITISCH: Diese Funktion wird an identischer Stelle in ProduceBlock und
// ImportBlock nach jedem Block aufgerufen. Weicht die Position ab, weicht das Set
// zwischen Produzent und Prüfer ab und die Kette forkt.
// ValidatorLookback bestimmt, wie viele der letzten Blöcke für die Ableitung der
// aktiven Produzenten herangezogen werden. Ein Validator, der innerhalb dieses
// Fensters einen Block produziert hat, gilt als aktiv. Großzügig gewählt, damit
// ein Validator nicht schon nach kurzer Pause herausfällt.
const ValidatorLookback uint64 = 100

// deriveValidatorSetLocked bestimmt das aktive Validator-Set VOLLSTÄNDIG AUS DER
// CHAIN — ohne lokale Config, damit alle Nodes bei gleichem Zustand dasselbe Set
// ableiten. Muss unter bc.mu laufen. Quellen (vereinigt):
//
//  1. Produzenten der letzten Blöcke (aus der Historie): Wer kürzlich einen Block
//     signiert hat, IST nachweislich Validator. Ein neu beigetretener Node erfährt
//     die bestehenden Validatoren so automatisch beim Synchronisieren — kein
//     Bootstrap-Set und kein Genesis-Eintrag nötig. GENAU das macht es dezentral.
//  2. On-chain gestakte Adressen (>= MinValidatorStake): so tritt ein neuer Node bei.
//  3. Bootstrap-Set (Config) NUR als Anschub, solange es noch keine Historie gibt
//     (Höhe 0) — sonst könnte die allererste Kette nie starten.
func (bc *Blockchain) deriveValidatorSetLocked() {
	seen := map[Address]bool{}
	var union []Address
	add := func(a Address) {
		if a != (Address{}) && !seen[a] {
			seen[a] = true
			union = append(union, a)
		}
	}

	// 1. Produzenten der letzten Blöcke aus der Historie. Nebenbei: wer im
	// Fenster gestakt hat (für die Aktivitätsprüfung in Punkt 2).
	recentProducer := map[Address]bool{}
	recentStaker := map[Address]bool{}
	if bc.height > 0 {
		from := uint64(1)
		if bc.height > ValidatorLookback {
			from = bc.height - ValidatorLookback + 1
		}
		for h := from; h <= bc.height; h++ {
			blk, err := bc.loadBlock(h)
			if err != nil || blk == nil {
				continue
			}
			add(blk.Header.Proposer)
			recentProducer[blk.Header.Proposer] = true
			for _, tx := range blk.Transactions {
				if tx != nil && tx.Type == TxStake {
					recentStaker[tx.From] = true
				}
			}
		}
	}

	// 2. On-chain gestakte Validatoren – aber nur AKTIVE: wer im Fenster der
	// letzten ValidatorLookback Blöcke selbst gebaut ODER gestakt hat. Früher
	// blieb jeder Staker für immer in der Rotation, auch wenn sein Schlüssel
	// längst verloren war; jede seiner Runden kostete dann eine Wartezeit, bis
	// die Ersatzrunde griff. Deterministisch (nur Blockdaten), daher auf allen
	// Nodes identisch. Wer nach längerer Pause zurückkehrt: erneut staken (oder
	// im Bootstrap-Set stehen), dann ist er sofort wieder dabei.
	if bc.state != nil {
		for _, a := range bc.state.StakedValidators() {
			if bc.height <= ValidatorLookback || recentProducer[a] || recentStaker[a] {
				add(a)
			}
		}
	}

	// 3. Gründungs-Validatoren (fest eincompiliert, siehe founders.go). Gleiches
	// Programm = gleiche Liste auf allen Nodes. FRÜHER stand hier die lokale
	// FUNDUS_VALIDATORS-Liste und (Punkt 4) von Peers gelernte Adressen – beide
	// sind pro Node verschieden, die Sets wichen ab, und die Chain zerfiel.
	for _, a := range foundingAddrs() {
		add(a)
	}

	if len(union) > 0 {
		if vs, err := NewValidatorSet(union); err == nil && vs != nil {
			bc.valSet = vs
			return
		}
	}
	// Fallback (nur wenn auch die Gründer-Liste leer/ungültig wäre).
	bc.valSet = bc.bootstrapVal
}

// ValidatorAddrs liefert die hex-Adressen des aktiven Validator-Sets (für die
// dezentrale Verbreitung an synchronisierende Peers).
func (bc *Blockchain) ValidatorAddrs() []string {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.valSet == nil {
		return nil
	}
	list := bc.valSet.List()
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Hex())
	}
	return out
}

// LearnValidators übernimmt von einem Peer empfangene Validator-Adressen und
// leitet das aktive Set neu ab. Die gelernten Adressen bleiben nur so lange
// wirksam, wie sie durch Historie oder Stake bestätigt werden — sie sind eine
// Anlaufhilfe, keine dauerhafte Autorität.
// Seit R477 OHNE Wirkung auf das Set: gelernte Adressen sind pro Node
// verschieden (wer von wem wann synchronisiert) und machten das Set
// nicht-deterministisch. Das Set ergibt sich nur noch aus Chain + Gründern.
// Die Methode bleibt für die Schnittstelle (p2p/chainsync) bestehen.
func (bc *Blockchain) LearnValidators(hexAddrs []string) {}

// RefreshValidatorSet leitet das aktive Set nach einem Block neu ab (öffentlicher
// Wrapper mit Lock). Wird nach Produktion und Import aufgerufen.
func (bc *Blockchain) refreshValidatorSetLocked() {
	bc.deriveValidatorSetLocked()
}

// ValidatorSet liefert das aktive Set (kann nil sein, wenn kein Konsens gesetzt).
func (bc *Blockchain) ValidatorSet() *ValidatorSet {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return bc.valSet
}

// IsMyTurn meldet, ob dieser Node für die NÄCHSTE Höhe der berechtigte Proposer
// ist. Nur dann sollte er produzieren.
// IsMyTurn meldet, ob dieser Node für die nächste Höhe der primäre Proposer
// (Runde 0) ist. Beibehalten für Abwärtskompatibilität; der Produktions-Loop
// nutzt TurnAt, das auch Fallback-Runden berücksichtigt.
func (bc *Blockchain) IsMyTurn() bool {
	my, round := bc.TurnAt(0)
	return my && round == 0
}

// TurnAt entscheidet anhand der seit dem letzten Block verstrichenen Zeit, ob
// dieser Node jetzt produzieren darf — als primärer Proposer (Runde 0) oder,
// falls der primäre und ggf. weitere Proposer nicht rechtzeitig geliefert haben,
// als Fallback-Proposer einer höheren Runde. Rückgabe: (dran?, Runde).
//
// nowUnix ist die aktuelle Zeit (0 → time.Now wird verwendet). Die erlaubte
// Runde ergibt sich aus der verstrichenen Zeit (RoundForElapsed). Dieser Node
// ist dran, wenn er für IRGENDEINE erlaubte Runde bis zur aktuellen der Proposer
// ist — aber nur für die NIEDRIGSTE solche Runde, damit nicht mehrere Nodes
// gleichzeitig für dieselbe Höhe produzieren.
func (bc *Blockchain) TurnAt(nowUnix uint64) (bool, uint64) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if bc.valSet == nil || bc.signKey == nil {
		return false, 0
	}
	if nowUnix == 0 {
		nowUnix = uint64(time.Now().Unix())
	}
	nextHeight := bc.height + 1

	var prevTS uint64
	if bc.head != nil {
		prevTS = bc.head.Timestamp
	}
	var elapsed uint64
	if nowUnix >= prevTS {
		elapsed = nowUnix - prevTS
	}

	// Mindest-Blockzeit: Auch der primäre Proposer (Runde 0) produziert erst,
	// wenn seit dem letzten Block mindestens BlockTime vergangen ist. Damit ist
	// die Blockrate unabhängig vom (feineren) Tick-Intervall des Loops — ohne
	// diese Sperre würde ein 1s-Tick die Kette mit BlockTime=5 überrennen.
	// Ausnahme: der allererste Block nach Genesis (prevTS==0 oder kein Head) darf
	// sofort, damit die Kette anlaufen kann.
	if bc.head != nil && prevTS > 0 && elapsed < BlockTime {
		return false, 0
	}

	maxRound := RoundForElapsed(elapsed)
	// Deckeln: Mehr Fallback-Runden als Validatoren ergeben keinen Sinn — nach
	// einer vollen Rotation ist wieder der primäre Proposer dran. Das begrenzt
	// auch den Effekt eines weit zurückliegenden Vorgänger-Zeitstempels (z.B.
	// erster Block nach Genesis), der sonst eine riesige maxRound ergäbe.
	if n := uint64(bc.valSet.Len()); n > 0 && maxRound > n-1 {
		maxRound = n - 1
	}

	// Von Runde 0 aufwärts bis zur zeitlich erlaubten maxRound suchen: die erste
	// Runde, für die DIESER Node der berechtigte Proposer ist. Das ist die
	// niedrigste Runde, in der wir legitim einspringen dürfen. Ein Fallback-Node
	// wartet dadurch automatisch, bis "seine" Runde zeitlich freigeschaltet ist,
	// und der primäre Proposer hat in Runde 0 stets Vorrang.
	for r := uint64(0); r <= maxRound; r++ {
		if bc.valSet.IsProposerForRound(bc.proposer, nextHeight, r) {
			return true, r
		}
	}
	return false, 0
}

// ProduceBlock baut einen Block aus den übergebenen Txs als primärer Proposer
// (Runde 0). Wrapper um ProduceBlockRound für Abwärtskompatibilität.
func (bc *Blockchain) ProduceBlock(txs []*Transaction, timestamp uint64) (*Block, error) {
	return bc.ProduceBlockRound(txs, timestamp, 0)
}

// ProduceBlockRound baut einen Block in der angegebenen Fallback-Runde. Mit
// aktivem PoA-Konsens prüft es, ob dieser Node für die nächste Höhe UND diese
// Runde berechtigt ist, verankert die Runde im Header und signiert den Block.
// Ohne Konsens (valSet==nil) verhält es sich wie ein Einzelnode.
func (bc *Blockchain) ProduceBlockRound(txs []*Transaction, timestamp, round uint64) (*Block, error) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	nextHeight := bc.height + 1

	// PoA: nur der für diese Höhe und Runde berechtigte Proposer darf produzieren.
	if bc.valSet != nil {
		if bc.signKey == nil {
			return nil, errors.New("chain: kein Signier-Schlüssel — dieser Node kann keine Blöcke produzieren")
		}
		if !bc.valSet.IsProposerForRound(bc.proposer, nextHeight, round) {
			expected := bc.valSet.ProposerForRound(nextHeight, round)
			return nil, fmt.Errorf("chain: nicht dein Zug — Proposer für Höhe %d Runde %d ist %s", nextHeight, round, expected.Hex())
		}
	}

	blk, err := BuildBlock(bc.head, bc.proposer, timestamp, txs, bc.state, bc.feeCollector)
	if err != nil {
		return nil, err
	}
	if len(blk.Transactions) == 0 && len(txs) > 0 {
		return nil, errors.New("chain: keine gültige Transaktion (alle abgelehnt — Nonce/Guthaben/Gebühr prüfen)")
	}

	// PoA: Runde + Validator-Set-Hash im Header verankern und Block signieren.
	if bc.valSet != nil {
		blk.Header.Round = round
		blk.Header.ValSetHash = bc.valSet.Hash()
		if err := SignBlock(blk, bc.signKey); err != nil {
			return nil, fmt.Errorf("chain: Block signieren: %w", err)
		}
	}

	if err := bc.persistBlock(blk); err != nil {
		return nil, err
	}
	bc.head = &blk.Header
	bc.height = blk.Header.Height
	// Validator-Set nach dem Block neu ableiten (Stake kann sich geändert haben).
	// IDENTISCHE Position wie in ImportBlock — sonst forkt die Kette.
	bc.refreshValidatorSetLocked()
	if err := bc.saveMeta(); err != nil {
		return nil, err
	}
	return blk, nil
}

// ─── Genesis als mitgeliefertes Artefakt ─────────────────────────────────────

// LoadGenesisFile liest einen Genesis-Block aus einer JSON-Datei (genesis.json).
// So wird der Genesis ausgeliefert statt bei jedem Start neu erzeugt — der
// Fee-Collector steckt im Header und ist über den Genesis-Hash verifizierbar.
func LoadGenesisFile(path string) (*Block, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w wireBlock
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	return wireToBlock(&w)
}

// WriteGenesisFile schreibt einen Genesis-Block als JSON (zum einmaligen Erzeugen
// beim Ausrollen). Gibt zusätzlich den Genesis-Hash zurück (zum Veröffentlichen).
func WriteGenesisFile(path string, genesis *Block) ([32]byte, error) {
	data, err := json.MarshalIndent(blockToWire(genesis), "", "  ")
	if err != nil {
		return [32]byte{}, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return [32]byte{}, err
	}
	return genesis.Header.Hash(), nil
}

// GenesisHash liefert den Hash eines Genesis-Blocks (zum Verankern/Vergleichen).
func GenesisHash(genesis *Block) [32]byte { return genesis.Header.Hash() }

func (bc *Blockchain) blockPath(height uint64) string {
	return filepath.Join(bc.dir, "blocks", fmt.Sprintf("%020d.json", height))
}

// persistBlock schreibt einen Block in den BlockStore (bbolt, binär). Ist kein
// Store gesetzt (z.B. während einer Migration oder in Tests), fällt es auf das
// alte JSON-Datei-Format zurück.
func (bc *Blockchain) persistBlock(blk *Block) error {
	if bc.store != nil {
		return bc.store.Put(blk.Header.Height, blk)
	}
	// Fallback: JSON-Datei (Altformat).
	data, err := json.Marshal(blockToWire(blk))
	if err != nil {
		return err
	}
	tmp := bc.blockPath(blk.Header.Height) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, bc.blockPath(blk.Header.Height))
}

func (bc *Blockchain) saveMeta() error {
	hh := bc.head.Hash()
	m := chainMeta{Height: bc.height, HeadHash: fmt.Sprintf("%x", hh[:]), SchemaVersion: StateSchemaVersion}
	data, _ := json.MarshalIndent(m, "", "  ")
	tmp := filepath.Join(bc.dir, "meta.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(bc.dir, "meta.json"))
}

// load rekonstruiert State + Head durch erneutes Anwenden aller Blöcke ab Genesis.
// Deterministische Wiederherstellung: derselbe Block-Verlauf → derselbe State.
func (bc *Blockchain) load() error {
	metaRaw, err := os.ReadFile(filepath.Join(bc.dir, "meta.json"))
	if err != nil {
		return err
	}
	var m chainMeta
	if err := json.Unmarshal(metaRaw, &m); err != nil {
		return err
	}
	// Schema-Version prüfen: Wurde die Chain mit einem anderen State-Format
	// geschrieben (z.B. vor einem Update, das den Root veränderte), ist sie mit
	// dem aktuellen Code nicht kompatibel. Klarer, früher Fehler statt später ein
	// kryptischer Root-Mismatch. (SchemaVersion==0 = vor Einführung des Feldes →
	// gilt ebenfalls als alt/inkompatibel.)
	if m.SchemaVersion != StateSchemaVersion {
		return fmt.Errorf("chain: State-Schema-Version inkompatibel (gespeichert 0x%02x, aktuell 0x%02x) — Formatwechsel",
			m.SchemaVersion, StateSchemaVersion)
	}

	// Genesis (Höhe 0) laden.
	genBlk, err := bc.loadBlock(0)
	if err != nil {
		return fmt.Errorf("Genesis fehlt: %w", err)
	}
	// State aus dem Genesis-Block rekonstruieren (Allokationen = empfangende Txs gibt es
	// nicht; Genesis-Salden stehen im Block als Allocs).
	st := NewState()
	for _, a := range genBlk.GenesisAllocs {
		st.Credit(a.Address, a.Balance)
	}
	head := &genBlk.Header

	// Alle Folgeblöcke 1..height anwenden.
	for h := uint64(1); h <= m.Height; h++ {
		blk, err := bc.loadBlock(h)
		if err != nil {
			return fmt.Errorf("Block %d: %w", h, err)
		}
		if err := ApplyBlock(head, blk, st, bc.feeCollector); err != nil {
			return fmt.Errorf("Block %d anwenden: %w", h, err)
		}
		head = &blk.Header
	}

	if head.Hash() != hexTo32(m.HeadHash) {
		return errors.New("chain: Head-Hash nach Replay weicht von meta.json ab")
	}
	bc.state = st
	bc.head = head
	bc.height = m.Height
	return nil
}

// loadBlock lädt einen Block aus dem BlockStore (bbolt). Ist kein Store gesetzt,
// fällt es auf das alte JSON-Datei-Format zurück.
func (bc *Blockchain) loadBlock(height uint64) (*Block, error) {
	if bc.store != nil {
		return bc.store.Get(height)
	}
	return bc.loadBlockJSON(height)
}

// loadBlockJSON liest einen Block aus einer JSON-Datei (Altformat). Wird für die
// Migration alter Chains gebraucht (migrate-chain) und als Fallback, wenn kein
// Store gesetzt ist.
func (bc *Blockchain) loadBlockJSON(height uint64) (*Block, error) {
	data, err := os.ReadFile(bc.blockPath(height))
	if err != nil {
		return nil, err
	}
	var w wireBlock
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	return wireToBlock(&w)
}
