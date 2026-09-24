package p2p

import (
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
