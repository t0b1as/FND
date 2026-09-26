package p2p

import (
	"strings"
	"bytes"
	"sync"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// ─── Chain-Synchronisation über P2P (Spec §11, Multi-Node) ───────────────────
//
// Zwei Mechanismen:
//   1. Aufhol-Sync (Request/Response): Beim Peer-Connect fragt ein Node die Höhe
//      des Peers ab und zieht fehlende Blöcke der Reihe nach (ChainSyncProtocol).
//   2. Live-Propagation (GossipSub): Ein neu produzierter Block wird auf
//      TopicBlocks veröffentlicht; alle Peers spielen ihn ein.
//
// Vor jedem Sync wird der Genesis-Hash verglichen — Nodes mit unterschiedlichem
// Genesis gehören zu verschiedenen Chains und dürfen NICHT mischen.

const (
	ChainSyncProtocol = "/fundus/chainsync/1.0.0" // Höhe/Block-Abruf (Request/Response)
	TopicBlocks       = "fundus.blocks"           // Live-Propagation neuer Blöcke
	TopicTx           = "fundus.tx"               // Live-Propagation neuer Transaktionen
)

// ChainBridge entkoppelt den P2P-Node von der konkreten Blockchain-Implementierung
// (kein Import-Zyklus). Erfüllt von *chain.Blockchain.
type ChainBridge interface {
	Height() uint64
	GenesisHeaderHash() [32]byte
	ExportBlockJSON(height uint64) ([]byte, error)
	ImportBlockJSON(data []byte) error
	// ImportTxJSON nimmt eine über das Netz empfangene Transaktion (Wire-JSON)
	// in den lokalen Mempool auf, damit der zuständige Proposer sie einbaut.
	ImportTxJSON(data []byte) error
	// ValidatorAddrs liefert die hex-Adressen des aktiven Validator-Sets, damit
	// ein synchronisierender Peer sie übernehmen kann (dezentrale Set-Verbreitung
	// ohne lokale Config).
	ValidatorAddrs() []string
	// Für die Astwahl bei Abzweigungen:
	BlockHashAt(h uint64) ([32]byte, bool)
	HashOfBlockJSON(data []byte) ([32]byte, uint64, error)
	BlockRoundAt(h uint64) (uint64, bool)
	BlockInfoFromJSON(data []byte) (hash [32]byte, height, round, timestamp uint64, proposer string, err error)
	// LearnValidators übernimmt von einem Peer empfangene Validator-Adressen als
	// zusätzliche Quelle für die eigene Set-Ableitung.
	LearnValidators(hexAddrs []string)
}

// chainSyncRequest ist die Anfrage an einen Peer.
type chainSyncRequest struct {
	Kind    string `json:"kind"`              // "head" | "block" | "push"
	Genesis string `json:"genesis,omitempty"` // hex des Genesis-Hash (Vergleich)
	Height  uint64 `json:"height,omitempty"`  // bei kind=block: gewünschte Höhe
	Block   []byte `json:"block,omitempty"`   // bei kind=push: der zu importierende Block (JSON)
}

// chainSyncResponse ist die Antwort eines Peers.
type chainSyncResponse struct {
	Genesis    string          `json:"genesis"`              // hex des Genesis-Hash des Peers
	Height     uint64          `json:"height"`               // aktuelle Head-Höhe des Peers
	Block      json.RawMessage `json:"block,omitempty"`      // bei kind=block: Wire-Block
	Validators []string        `json:"validators,omitempty"` // aktives Validator-Set des Peers (hex-Adressen)
	Error      string          `json:"error,omitempty"`
}

// AttachChain hängt eine Chain an den Node: registriert den Sync-Responder und
// abonniert das Block-Topic für Live-Propagation. Muss nach NewNode aufgerufen
// werden, sobald die Chain initialisiert ist.
func (n *Node) AttachChain(ctx context.Context, bridge ChainBridge) {
	n.mu.Lock()
	n.chain = bridge
	n.mu.Unlock()

	// 1. Responder für Aufhol-Anfragen.
	n.RegisterProtocol(ChainSyncProtocol, func(peerID string, data []byte) []byte {
		return n.handleChainSyncRequest(data)
	})

	// 2. Live-Blöcke vom Topic einspielen.
	if err := n.joinTopic(ctx, TopicBlocks); err != nil {
		n.log.Warn("Chain: Block-Topic beitreten fehlgeschlagen", zap.Error(err))
	}
	n.SetTopicHandler(TopicBlocks, func(raw []byte) {
		n.mu.RLock()
		c := n.chain
		n.mu.RUnlock()
		if c == nil {
			return
		}
		checkBlockClock(c, raw, n.log)
		if err := c.ImportBlockJSON(raw); err != nil {
			n.log.Warn("Chain: Live-Block verworfen", zap.Error(err))
		}
	})

	// 3. Live-Transaktionen vom Topic in den Mempool einspielen. Ohne das landet
	// eine Tx nur im Mempool des einreichenden Nodes und erreicht den zuständigen
	// Proposer nie — im Multi-Node-Betrieb würde sie nie in einen Block kommen.
	if err := n.joinTopic(ctx, TopicTx); err != nil {
		n.log.Warn("Chain: Tx-Topic beitreten fehlgeschlagen", zap.Error(err))
	}
	n.SetTopicHandler(TopicTx, func(raw []byte) {
		n.mu.RLock()
		c := n.chain
		n.mu.RUnlock()
		if c == nil {
			return
		}
		if err := c.ImportTxJSON(raw); err != nil {
			// Häufig harmlos: Tx schon bekannt (via anderen Peer) oder bereits verbaut.
			n.log.Debug("Chain: Live-Tx verworfen", zap.Error(err))
		}
	})

	n.log.Info("Chain an P2P angebunden", zap.String("genesis",
		fmt.Sprintf("%x", bridge.GenesisHeaderHash())[:12]))
}

// handleChainSyncRequest beantwortet head-/block-Anfragen.
func (n *Node) handleChainSyncRequest(data []byte) []byte {
	n.mu.RLock()
	c := n.chain
	n.mu.RUnlock()

	resp := chainSyncResponse{}
	if c == nil {
		resp.Error = "keine Chain"
		out, _ := json.Marshal(resp)
		return out
	}
	gh := c.GenesisHeaderHash()
	resp.Genesis = fmt.Sprintf("%x", gh)
	resp.Height = c.Height()
	resp.Validators = c.ValidatorAddrs() // eigenes Set mitschicken (dezentrale Verbreitung)

	var req chainSyncRequest
	if err := json.Unmarshal(data, &req); err != nil {
		resp.Error = "ungültige Anfrage"
		out, _ := json.Marshal(resp)
		return out
	}
	if req.Kind == "block" {
		blockJSON, err := c.ExportBlockJSON(req.Height)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Block = blockJSON
		}
	} else if req.Kind == "push" && len(req.Block) > 0 {
		// Direkt gepushter Block (vom Produzenten nach ProduceBlock). Zuverlässiger
		// als der GossipSub-Broadcast, weil über einen direkten Stream zugestellt.
		// ImportBlockJSON ist idempotent (alter/gleicher Block = kein Fehler) und
		// lehnt Lücken ab — dann zieht der periodische Sync den Rest nach.
		if err := c.ImportBlockJSON(req.Block); err != nil {
			resp.Error = err.Error()
		}
	}
	out, _ := json.Marshal(resp)
	return out
}

// SyncChainFromPeer holt fehlende Blöcke von einem Peer. Vergleicht zuerst den
// Genesis-Hash (bei Abweichung: Abbruch — fremde Chain), dann zieht es Blöcke
// von eigener Höhe+1 bis zur Peer-Höhe der Reihe nach und spielt sie ein.
func (n *Node) SyncChainFromPeer(peerID string) {
	n.mu.RLock()
	c := n.chain
	n.mu.RUnlock()
	if c == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. Head des Peers abfragen.
	ourGenesis := fmt.Sprintf("%x", c.GenesisHeaderHash())
	headReq, _ := json.Marshal(chainSyncRequest{Kind: "head", Genesis: ourGenesis})
	raw, err := n.SendAndReceive(ctx, peerID, ChainSyncProtocol, headReq)
	if err != nil {
		n.log.Warn("Chain-Sync: head-Anfrage fehlgeschlagen", zap.String("peer", peerID), zap.Error(err))
		return
	}
	var head chainSyncResponse
	if err := json.Unmarshal(raw, &head); err != nil || head.Error != "" {
		return
	}
	// 2. Genesis-Hash MUSS übereinstimmen — sonst fremde Chain.
	if head.Genesis != ourGenesis {
		n.log.Warn("Chain-Sync: abweichender Genesis — Peer gehört zu anderer Chain",
			zap.String("peer", peerID),
			zap.String("our", ourGenesis[:12]), zap.String("their", safePrefix(head.Genesis, 12)))
		return
	}
	notePeerHead(peerID, head.Height, false)

	// 2a. Liegen wir auf demselben Ast? Auf der gemeinsamen Höhe die Blockhashes
	// vergleichen – IMMER, nicht nur wenn der Peer weiter ist. (Wachsen zwei Äste
	// gleich schnell, gab es nie "etwas nachzuholen", und die Abzweigung blieb
	// unbemerkt: jeder Validator baute seinen Ast allein weiter.)
	if ours := c.Height(); ours > 0 && head.Height > 0 {
		h := ours
		if head.Height < h {
			h = head.Height
		}
		if mine, ok := c.BlockHashAt(h); ok {
			if theirs, ok2 := n.peerBlockHash(ctx, peerID, c, h); ok2 && theirs != mine {
				notePeerHead(peerID, head.Height, true)
				go n.resolveFork(peerID, c, head.Height)
				return
			}
		}
	}

	// 2b. Validator-Set des Peers übernehmen (dezentrale Verbreitung ohne Config).
	// So erfährt ein neu beigetretener Node die aktiven Validatoren direkt vom Netz.
	if len(head.Validators) > 0 {
		c.LearnValidators(head.Validators)
	}
	// 3. Fehlende Blöcke der Reihe nach ziehen.
	imported := 0
	for h := c.Height() + 1; h <= head.Height; h++ {
		blkReq, _ := json.Marshal(chainSyncRequest{Kind: "block", Height: h})
		braw, err := n.SendAndReceive(ctx, peerID, ChainSyncProtocol, blkReq)
		if err != nil {
			n.log.Warn("Chain-Sync: Block-Abruf fehlgeschlagen", zap.Uint64("height", h), zap.Error(err))
			break
		}
		var bresp chainSyncResponse
		if err := json.Unmarshal(braw, &bresp); err != nil || bresp.Error != "" || len(bresp.Block) == 0 {
			break
		}
		if err := c.ImportBlockJSON(bresp.Block); err != nil {
			n.log.Warn("Chain-Sync: Block abgelehnt", zap.Uint64("height", h), zap.Error(err))
			// Peer liegt auf einer anderen Abzweigung: seine Höhe darf die eigene
			// Blockproduktion nicht blockieren.
			notePeerHead(peerID, head.Height, true)
			if strings.Contains(err.Error(), "prev_hash") {
				go n.resolveFork(peerID, c, head.Height)
			}
			break
		}
		imported++
	}
	if imported > 0 {
		n.log.Info("Chain-Sync abgeschlossen",
			zap.String("peer", peerID), zap.Int("blocks", imported), zap.Uint64("height", c.Height()))
	}
}

// BroadcastBlock veröffentlicht einen neu produzierten Block (Wire-JSON) auf dem
// Block-Topic. Aufrufer: der Block-Produzent direkt nach ProduceBlock.
// Sendet zusätzlich einen DIREKTEN Push an alle verbundenen Peers — das ist
// zuverlässiger als der reine GossipSub-Broadcast (der einen Peer verfehlen
// kann, wenn er gerade nicht im Topic-Mesh ist).
func (n *Node) BroadcastBlock(ctx context.Context, blockJSON []byte) error {
	// 1. GossipSub-Topic (erreicht auch Nicht-direkt-verbundene über das Mesh).
	pubErr := n.Publish(ctx, TopicBlocks, blockJSON)
	// 2. Direkter Push an alle aktuell verbundenen Peers (Request/Response).
	go n.PushBlockToPeers(blockJSON)
	return pubErr
}

// BroadcastTx veröffentlicht eine neu eingereichte Transaktion (Wire-JSON) auf
// dem Tx-Topic, damit alle Peers sie in ihren Mempool aufnehmen und der
// zuständige Proposer sie einbauen kann.
func (n *Node) BroadcastTx(ctx context.Context, txJSON []byte) error {
	return n.Publish(ctx, TopicTx, txJSON)
}

// PushBlockToPeers stellt einen Block per direktem Stream an jeden verbundenen
// Peer zu (kind="push"). Best-effort: Fehler einzelner Peers werden nur geloggt.
func (n *Node) PushBlockToPeers(blockJSON []byte) {
	req, err := json.Marshal(chainSyncRequest{Kind: "push", Block: blockJSON})
	if err != nil {
		return
	}
	for _, id := range n.Peers() {
		go func(peerID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := n.SendAndReceive(ctx, peerID, ChainSyncProtocol, req); err != nil {
				n.log.Debug("Block-Push fehlgeschlagen", zap.String("peer", peerID), zap.Error(err))
			}
		}(id.String())
	}
}

func safePrefix(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// ── Höhen der Peers (für die Blockproduktion) ───────────────────────────────
// Ein frisch gestarteter Validator baute sofort Blöcke – noch bevor er sich mit
// den anderen abgeglichen hatte – und eröffnete so eine eigene Chain (Fork).
// Die Produktion fragt deshalb MaxPeerHeight: Ist ein Peer weiter, wird erst
// synchronisiert. Peers auf einer fremden Abzweigung zählen nicht.

type peerHeadInfo struct {
	height uint64
	fork   bool
	at     time.Time
}

var (
	peerHeadsMu sync.Mutex
	peerHeads   = map[string]peerHeadInfo{}
)

func notePeerHead(peerID string, height uint64, fork bool) {
	peerHeadsMu.Lock()
	peerHeads[peerID] = peerHeadInfo{height: height, fork: fork, at: time.Now()}
	peerHeadsMu.Unlock()
}

// MaxPeerHeight: höchste gemeldete Höhe gleicher Chain (nicht älter als maxAge)
// und ob überhaupt ein Peer Auskunft gegeben hat.
func (n *Node) MaxPeerHeight(maxAge time.Duration) (uint64, bool) {
	peerHeadsMu.Lock()
	defer peerHeadsMu.Unlock()
	var max uint64
	known := false
	for _, h := range peerHeads {
		if time.Since(h.at) > maxAge {
			continue
		}
		known = true
		if !h.fork && h.height > max {
			max = h.height
		}
	}
	return max, known
}

// ── Astwahl bei Abzweigungen ────────────────────────────────────────────────
// Bauen zwei Validatoren auf derselben Höhe je einen Block (Verzögerung, ein
// paar Sekunden Uhrenabweichung in der Ersatzrunde), behielt bisher jeder seinen
// eigenen – die Chain lief für immer getrennt weiter. Jetzt gilt eine Regel, die
// jeder Node gleich auswertet: Es gilt der Ast, dessen ERSTER ABWEICHENDER BLOCK
// den kleineren Hash hat. Wer auf dem anderen Ast liegt, legt seine Chain
// beiseite (gesichert) und startet neu; die Sync-Sperre beim Start sorgt dafür,
// dass er erst den gültigen Ast übernimmt und dann wieder baut.

var (
	forkMu          sync.Mutex
	forkLastPeer    = map[string]time.Time{}
	forkLastSwitch  time.Time
	forkLoseHandler func(reason string)
	forkNote        string
	forkNoteAt      time.Time
)

// SetForkLoseHandler: wird aufgerufen, wenn dieser Node auf dem verlierenden Ast
// liegt (main: Chain beiseitelegen und neu starten).
func SetForkLoseHandler(f func(reason string)) {
	forkMu.Lock()
	forkLoseHandler = f
	forkMu.Unlock()
}

// LastForkNote: letzte Meldung zur Astwahl (für die Statusanzeige), sonst "".
func (n *Node) LastForkNote() string {
	forkMu.Lock()
	defer forkMu.Unlock()
	if time.Since(forkNoteAt) > 30*time.Minute {
		return ""
	}
	return forkNote
}

func setForkNote(msg string) {
	forkMu.Lock()
	forkNote, forkNoteAt = msg, time.Now()
	forkMu.Unlock()
}

// peerBlockHash: Hash des Peer-Blocks auf Höhe h.
func (n *Node) peerBlockHash(ctx context.Context, peerID string, c ChainBridge, h uint64) ([32]byte, bool) {
	req, _ := json.Marshal(chainSyncRequest{Kind: "block", Height: h})
	raw, err := n.SendAndReceive(ctx, peerID, ChainSyncProtocol, req)
	if err != nil {
		return [32]byte{}, false
	}
	var resp chainSyncResponse
	if json.Unmarshal(raw, &resp) != nil || resp.Error != "" || len(resp.Block) == 0 {
		return [32]byte{}, false
	}
	hash, bh, err := c.HashOfBlockJSON(resp.Block)
	if err != nil || bh != h {
		return [32]byte{}, false
	}
	return hash, true
}

// peerBlockRound: Hash und Runde des Peer-Blocks auf Höhe h.
func (n *Node) peerBlockRound(ctx context.Context, peerID string, c ChainBridge, h uint64) ([32]byte, uint64, bool) {
	req, _ := json.Marshal(chainSyncRequest{Kind: "block", Height: h})
	raw, err := n.SendAndReceive(ctx, peerID, ChainSyncProtocol, req)
	if err != nil {
		return [32]byte{}, 0, false
	}
	var resp chainSyncResponse
	if json.Unmarshal(raw, &resp) != nil || resp.Error != "" || len(resp.Block) == 0 {
		return [32]byte{}, 0, false
	}
	hash, bh, round, _, _, err := c.BlockInfoFromJSON(resp.Block)
	if err != nil || bh != h {
		return [32]byte{}, 0, false
	}
	return hash, round, true
}

func (n *Node) resolveFork(peerID string, c ChainBridge, peerHeight uint64) {
	forkMu.Lock()
	if t, ok := forkLastPeer[peerID]; ok && time.Since(t) < 5*time.Minute {
		forkMu.Unlock()
		return
	}
	forkLastPeer[peerID] = time.Now()
	forkMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	top := c.Height()
	if peerHeight < top {
		top = peerHeight
	}
	// Gemeinsamen Vorfahren per BINÄRSUCHE finden: Weichen zwei Äste einmal ab,
	// unterscheiden sich ALLE späteren Blöcke (jeder enthält den Hash seines
	// Vorgängers). "Gleicher Hash auf Höhe h" gilt also genau bis zum Vorfahren –
	// monoton, daher genügen ~log2(Höhe) Anfragen (1 Mio. Blöcke ≈ 20), egal wie
	// tief die Abzweigung liegt. Genesis (Höhe 0) ist per Konstruktion gleich.
	same := func(h uint64) (bool, bool) {
		if h == 0 {
			return true, true
		}
		ours, ok1 := c.BlockHashAt(h)
		theirs, ok2 := n.peerBlockHash(ctx, peerID, c, h)
		if !ok1 || !ok2 {
			return false, false // nicht abrufbar → später erneut versuchen
		}
		return ours == theirs, true
	}
	lo, hi := uint64(0), top
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		eq, ok := same(mid)
		if !ok {
			n.log.Warn("Chain: Astwahl abgebrochen – Block des Peers nicht abrufbar", zap.String("peer", shortPeer(peerID)), zap.Uint64("hoehe", mid))
			return
		}
		if eq {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	ancestor := lo
	k := ancestor + 1
	ours, ok1 := c.BlockHashAt(k)
	ourRound, ok3 := c.BlockRoundAt(k)
	theirs, theirRound, ok2 := n.peerBlockRound(ctx, peerID, c, k)
	if !ok1 || !ok2 || !ok3 {
		return // einer der Äste endet am Vorfahren – kein echter Konflikt
	}
	// Regel (auf allen Nodes gleich): der REGULÄRE Block (niedrigere Runde)
	// gewinnt vor einem Ersatzblock – so verliert der Validator, der zu früh
	// eingesprungen ist. Erst bei gleicher Runde entscheidet der kleinere Hash.
	weWin := ourRound < theirRound || (ourRound == theirRound && bytes.Compare(ours[:], theirs[:]) < 0)
	why := "kleinerer Hash"
	if ourRound != theirRound {
		why = fmt.Sprintf("Runde %d vor Runde %d", minU(ourRound, theirRound), maxU(ourRound, theirRound))
	}
	if weWin {
		msg := fmt.Sprintf("Abzweigung ab Block %d erkannt – eigener Ast gilt (%s); Peer %s wechselt", k, why, shortPeer(peerID))
		setForkNote(msg)
		n.log.Info("Chain: " + msg)
		return
	}
	// Wir liegen auf dem verlierenden Ast.
	forkMu.Lock()
	if !forkLastSwitch.IsZero() && time.Since(forkLastSwitch) < 10*time.Minute {
		forkMu.Unlock()
		return // höchstens ein Astwechsel alle 10 min
	}
	forkLastSwitch = time.Now()
	h := forkLoseHandler
	forkMu.Unlock()
	msg := fmt.Sprintf("Abzweigung ab Block %d – der Ast von Peer %s gilt (%s); wechsle: Chain wird gesichert und neu synchronisiert", k, shortPeer(peerID), why)
	setForkNote(msg)
	n.log.Warn("Chain: " + msg)
	if h != nil {
		h(msg)
	}
}

func shortPeer(p string) string {
	if len(p) > 12 {
		return p[:6] + "…" + p[len(p)-4:]
	}
	return p
}

func minU(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func maxU(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

// ── Uhrenprüfung ────────────────────────────────────────────────────────────
// Ein frischer Block mit Zeitstempel AUS DER ZUKUNFT heißt: Die Uhr seines
// Erzeugers geht vor. Dann hält er den regulär zuständigen Validator zu früh
// für überfällig, baut Ersatzblöcke auf derselben Höhe – und die Chain spaltet
// sich immer wieder. Pis haben keine Batterie-Uhr; ohne NTP driften sie.

var (
	clockMu   sync.Mutex
	clockNote string
	clockAt   time.Time
)

func checkBlockClock(c ChainBridge, raw []byte, log *zap.Logger) {
	_, _, _, ts, proposer, err := c.BlockInfoFromJSON(raw)
	if err != nil || ts == 0 {
		return
	}
	ahead := int64(ts) - time.Now().Unix()
	if ahead <= 3 {
		return
	}
	msg := fmt.Sprintf("Uhr von Validator %s geht ca. %d s vor (Block-Zeitstempel in der Zukunft) – dort NTP prüfen: timedatectl; sudo timedatectl set-ntp true", proposer, ahead)
	clockMu.Lock()
	first := time.Since(clockAt) > 10*time.Minute
	clockNote, clockAt = msg, time.Now()
	clockMu.Unlock()
	if first {
		log.Warn("Chain: " + msg)
	}
}

// LastClockNote: letzte Uhren-Warnung (30 min gültig), sonst "".
func (n *Node) LastClockNote() string {
	clockMu.Lock()
	defer clockMu.Unlock()
	if time.Since(clockAt) > 30*time.Minute {
		return ""
	}
	return clockNote
}
