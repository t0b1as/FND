package p2p

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
)

// P2PNode ist das Interface das der API-Server und andere Pakete
// vom P2P-Knoten erwarten. Erlaubt Test-Doubles ohne echte libp2p-Verbindung.
type P2PNode interface {
	// Identität
	ID() peer.ID
	Addrs() []string
	Peers() []peer.ID
	PeersDetailed() []PeerDetail

	// GossipSub – Broadcast-Nachrichten
	Publish(ctx context.Context, topic string, data []byte) error
	SetTopicHandler(topic string, handler func(data []byte))

	// Chain – Block an alle Peers verteilen (Topic + direkter Push)
	BroadcastBlock(ctx context.Context, blockJSON []byte) error
	BroadcastTx(ctx context.Context, txJSON []byte) error

	// DHT – Key/Value für Datei-Manifeste und Chunk-Locations
	// Schlüssel-Format: "/fundus/<namespace>/<id>"
	DHTput(ctx context.Context, key string, value []byte) error
	DHTget(ctx context.Context, key string) ([]byte, error)
	RequestKeyDir(ctx context.Context, addr string) []byte
	DeliverMessage(ctx context.Context, peerID, topicName string, payload []byte) bool

	// Direkte Peer-Streams für Chunk-Transfer (libp2p Streams)
	SendToPeer(ctx context.Context, peerID string, protocol string, data []byte) error
	SendAndReceive(ctx context.Context, peerID string, protocol string, data []byte) ([]byte, error)
	RegisterProtocol(protocol string, handler func(peerID string, data []byte) []byte)

	// Signatur – Records signieren/verifizieren mit dem Host-Key
	SignData(data []byte) ([]byte, error)
	VerifySignature(ownerID string, data, signature []byte) bool

	Bootstrap(ctx context.Context) error
	Close() error
}

// Compile-time-Prüfung: *Node muss P2PNode implementieren.
var _ P2PNode = (*Node)(nil)
