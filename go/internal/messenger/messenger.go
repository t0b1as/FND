// Package messenger – E2E verschlüsselter Text/Audio/Video Messenger.
//
// Crypto-Stack:
//   X25519 ECDH → Shared Secret → Argon2id → XChaCha20-Poly1305
//   Signaturen:   Ed25519 (deterministisch, kein Nonce-Leck)
//   Audio/Video:  WebRTC DTLS-SRTP (Browser-nativ) + verschlüsseltes Signaling
//
// Anonymität:
//   P2P-Topics: "fundus.msg.<FundusID>" – kein Email-Bezug
//   Keine IP-Adressen oder Echtzeit-Metadaten im Klartext
package messenger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/fundus/node/internal/identity"
	"go.uber.org/zap"
)

// =============================================================================
//  Konstanten
// =============================================================================

const (
	TopicPrefix          = "fundus.msg."
	SignalingTopicPrefix = "fundus.signal."
	MaxMessageSize       = 64 * 1024
	MessageRetention     = 7 * 24 * time.Hour
)

// =============================================================================
//  Typen
// =============================================================================

type MessageType string

const (
	TypeText     MessageType = "text"
	TypeFile     MessageType = "file"
	TypeSignal   MessageType = "signal"
	TypePresence MessageType = "presence"
	TypeReceipt  MessageType = "receipt"
)

type SignalType string

const (
	SignalOffer     SignalType = "offer"
	SignalAnswer    SignalType = "answer"
	SignalICE       SignalType = "ice"
	SignalHangup    SignalType = "hangup"
	SignalCallAudio SignalType = "call_audio"
	SignalCallVideo SignalType = "call_video"
)

// Message ist eine verschlüsselte P2P-Nachricht.
type Message struct {
	ID          string      `json:"id"`
	RecipientID string      `json:"recipient_id"`
	SenderID    string      `json:"sender_id"`
	SenderPubX  string      `json:"sender_pub_x"` // X25519 PubKey des Senders für ECDH
	Type        MessageType `json:"type"`
	Timestamp   time.Time   `json:"ts"`

	// XChaCha20-Poly1305 verschlüsselt (Schlüssel via X25519+Argon2id)
	EncryptedPayload string `json:"payload"`

	// Ed25519 Signatur (über EncryptedPayload+Timestamp)
	Signature string `json:"sig"`

	ExpiresAt *time.Time `json:"exp,omitempty"`
}

// Payload ist der entschlüsselte Nachrichteninhalt.
type Payload struct {
	Text     string     `json:"text,omitempty"`
	FileHash string     `json:"file_hash,omitempty"`
	FileName string     `json:"file_name,omitempty"`
	FileSize int64      `json:"file_size,omitempty"`
	MimeType string     `json:"mime_type,omitempty"` // für Inline-Anzeige (Bild/GIF/Video)
	Signal   *SignalMsg `json:"signal,omitempty"`

	// Zustellungs-/Lesequittung (TypeReceipt). ReceiptFor referenziert die
	// Original-Nachricht-ID; ReceiptKind ist "delivered" oder "read".
	ReceiptFor  string `json:"receipt_for,omitempty"`
	ReceiptKind string `json:"receipt_kind,omitempty"`
}

// Quittungsarten (WhatsApp-analog): delivered = beim Empfänger angekommen,
// read = vom Empfänger gelesen (Chat geöffnet).
const (
	ReceiptDelivered = "delivered"
	ReceiptRead      = "read"
)

// SignalMsg enthält WebRTC-Signaling-Daten.
type SignalMsg struct {
	Type SignalType `json:"type"`
	SDP  string    `json:"sdp,omitempty"`
	ICE  string    `json:"ice,omitempty"`
}

// =============================================================================
//  P2P-Adapter
// =============================================================================

// P2PAdapter ist das Interface zum P2P-Layer.
type P2PAdapter interface {
	Publish(ctx context.Context, topic string, data []byte) error
	SetTopicHandler(topic string, handler func(data []byte))
	DeliverMessage(ctx context.Context, peerID, topicName string, payload []byte) bool
}

// =============================================================================
//  Messenger
// =============================================================================

type MessageHandler func(msg *Message, payload *Payload)

// Messenger verwaltet E2E-Verschlüsselung, Senden und Empfang.
type Messenger struct {
	identity *identity.Identity
	p2p      P2PAdapter
	log      *zap.Logger

	mu       sync.RWMutex
	handlers []MessageHandler
	offline  map[string][]*Message

	// recipientPeerID löst eine FundusID zur Peer-ID auf (aus dem keydir),
	// für die gerichtete Zustellung. Wird vom API-Layer gesetzt.
	recipientPeerID func(fundusID string) string

	// seenMsgs dedupliziert eingehende Nachrichten: dieselbe Nachricht kann über
	// mehrere Wege ankommen (gerichtet + GossipSub, oder GossipSub-Mehrfach-
	// zustellung). Wir verarbeiten jede msg.ID nur einmal.
	seenMu   sync.Mutex
	seenMsgs map[string]time.Time
}

// SetPeerIDResolver setzt die Funktion, die FundusID → Peer-ID auflöst
// (für gerichtete Nachrichten-Zustellung).
func (m *Messenger) SetPeerIDResolver(fn func(fundusID string) string) {
	m.recipientPeerID = fn
}

// New erstellt einen Messenger und abonniert das eigene P2P-Topic.
func New(id *identity.Identity, p2p P2PAdapter, log *zap.Logger) *Messenger {
	m := &Messenger{
		identity: id,
		p2p:      p2p,
		log:      log,
		offline:  make(map[string][]*Message),
	}

	// Eigene Topics abonnieren
	for _, prefix := range []string{TopicPrefix, SignalingTopicPrefix} {
		topic := prefix + strings.ToLower(id.FundusID)
		p2p.SetTopicHandler(topic, m.handleIncoming)
	}

	log.Info("Messenger bereit",
		zap.String("fundusID",  id.FundusID[:10]+"…"),
		zap.String("x25519pub", id.X25519PublicKeyHex()[:16]+"…"),
	)
	return m
}

// OnMessage registriert einen eingehenden Nachrichten-Handler.
func (m *Messenger) OnMessage(h MessageHandler) {
	m.mu.Lock(); defer m.mu.Unlock()
	m.handlers = append(m.handlers, h)
}

// =============================================================================
//  Senden
// =============================================================================

func (m *Messenger) Send(ctx context.Context, recipientID, recipientX25519Hex, text string) (*Message, error) {
	return m.sendPayload(ctx, recipientID, recipientX25519Hex, TypeText, Payload{Text: text})
}

func (m *Messenger) SendFile(ctx context.Context, recipientID, recipientX25519Hex, contentHash, fileName, mimeType string, fileSize int64) (*Message, error) {
	return m.sendPayload(ctx, recipientID, recipientX25519Hex, TypeFile,
		Payload{FileHash: contentHash, FileName: fileName, FileSize: fileSize, MimeType: mimeType})
}

func (m *Messenger) SendSignal(ctx context.Context, recipientID, recipientX25519Hex string, signal SignalMsg) (*Message, error) {
	return m.sendPayload(ctx, recipientID, recipientX25519Hex, TypeSignal, Payload{Signal: &signal})
}

// SendReceipt schickt eine Zustellungs- oder Lesequittung für eine empfangene
// Nachricht zurück an deren Absender. kind ist ReceiptDelivered oder ReceiptRead.
func (m *Messenger) SendReceipt(ctx context.Context, recipientID, recipientX25519Hex, messageID, kind string) error {
	if messageID == "" || (kind != ReceiptDelivered && kind != ReceiptRead) {
		return errors.New("messenger: ungültige Quittung")
	}
	_, err := m.sendPayload(ctx, recipientID, recipientX25519Hex, TypeReceipt,
		Payload{ReceiptFor: messageID, ReceiptKind: kind})
	return err
}

func (m *Messenger) sendPayload(ctx context.Context, recipientID, recipientX25519Hex string, msgType MessageType, payload Payload) (*Message, error) {
	if recipientID == "" || recipientX25519Hex == "" {
		m.log.Warn("Senden abgebrochen: Empfänger-Daten fehlen",
			zap.Bool("id_leer", recipientID == ""),
			zap.Bool("pubkey_leer", recipientX25519Hex == ""))
		return nil, errors.New("messenger: recipientID und recipientX25519Hex erforderlich")
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil { return nil, err }
	if len(payloadBytes) > MaxMessageSize {
		return nil, fmt.Errorf("messenger: max %d KB", MaxMessageSize/1024)
	}

	// 1. X25519 ECDH + Argon2id → XChaCha20-Schlüssel
	sharedSecret, err := m.identity.ECDHSharedSecret(recipientX25519Hex)
	if err != nil { return nil, fmt.Errorf("messenger: ECDH: %w", err) }

	// 2. XChaCha20-Poly1305 verschlüsseln
	encrypted, err := identity.Encrypt(payloadBytes, sharedSecret)
	if err != nil { return nil, err }

	// 3. Nachricht bauen
	now := time.Now().UTC()
	exp := now.Add(MessageRetention)
	msg := &Message{
		ID:               generateID(),
		RecipientID:      recipientID,
		SenderID:         m.identity.FundusID,
		SenderPubX:       m.identity.X25519PublicKeyHex(), // Empfänger braucht ihn für ECDH
		Type:             msgType,
		Timestamp:        now,
		EncryptedPayload: hex.EncodeToString(encrypted),
		ExpiresAt:        &exp,
	}

	// 4. Ed25519 signieren
	sigData := []byte(msg.EncryptedPayload + msg.Timestamp.Format(time.RFC3339Nano))
	sig, err := m.identity.Sign(sigData)
	if err != nil { return nil, err }
	msg.Signature = sig

	// 5. Per P2P verbreiten
	msgBytes, _ := json.Marshal(msg)
	topic := TopicPrefix + strings.ToLower(recipientID)
	if msgType == TypeSignal {
		topic = SignalingTopicPrefix + strings.ToLower(recipientID)
	}

	// GERICHTETE Zustellung zuerst: wenn wir die Peer-ID des Empfängers kennen
	// (aus dem keydir), die Nachricht direkt an diesen Peer schicken — zuverlässiger
	// als GossipSub-Broadcast bei wenigen Nodes. GossipSub bleibt als Fallback.
	delivered := false
	if m.recipientPeerID != nil {
		if pid := m.recipientPeerID(recipientID); pid != "" {
			if m.p2p.DeliverMessage(ctx, pid, topic, msgBytes) {
				delivered = true
			}
		}
	}
	if delivered {
		return msg, nil
	}

	if err := m.p2p.Publish(ctx, topic, msgBytes); err != nil {
		m.mu.Lock()
		m.offline[recipientID] = append(m.offline[recipientID], msg)
		m.mu.Unlock()
		m.log.Warn("Offline-Puffer", zap.String("to", recipientID[:10]+"…"), zap.Error(err))
	} else {
		m.log.Info("Msg publiziert", zap.String("topic", topic), zap.String("to", recipientID[:10]+"…"))
	}
	return msg, nil
}

// =============================================================================
//  Empfangen + Entschlüsseln
// =============================================================================

// InjectIncoming verarbeitet eine (z.B. aus der Offline-Mailbox geholte) rohe
// Nachricht so, als wäre sie gerade per P2P eingetroffen: entschlüsseln,
// verifizieren, in die History schreiben, Handler aufrufen.
func (m *Messenger) InjectIncoming(data []byte) {
	m.handleIncoming(data)
}

func (m *Messenger) handleIncoming(data []byte) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		m.log.Warn("Eingehende Nachricht ungültig", zap.Error(err))
		return
	}
	if msg.ExpiresAt != nil && time.Now().After(*msg.ExpiresAt) { return }

	// Deduplizierung: dieselbe Nachricht kann über mehrere Wege ankommen
	// (gerichtet + GossipSub). Jede msg.ID nur einmal verarbeiten.
	if msg.ID != "" {
		m.seenMu.Lock()
		if m.seenMsgs == nil {
			m.seenMsgs = make(map[string]time.Time)
		}
		if _, dup := m.seenMsgs[msg.ID]; dup {
			m.seenMu.Unlock()
			m.log.Info("Doppelte Nachricht ignoriert", zap.String("id", msg.ID[:8]+"…"))
			return
		}
		m.seenMsgs[msg.ID] = time.Now()
		// Alte Einträge (>5min) aufräumen, damit die Map nicht wächst.
		if len(m.seenMsgs) > 200 {
			cutoff := time.Now().Add(-5 * time.Minute)
			for id, t := range m.seenMsgs {
				if t.Before(cutoff) { delete(m.seenMsgs, id) }
			}
		}
		m.seenMu.Unlock()
	}

	// Ed25519 Signatur prüfen – Sender-PubKey ist in der Nachricht
	senderPubKey, err := m.lookupContactPubKey(msg.SenderID)
	if err != nil {
		// SenderPubX nutzen falls Kontakt noch unbekannt (First-Contact)
		senderPubKey = ""
	}

	sigData := []byte(msg.EncryptedPayload + msg.Timestamp.Format(time.RFC3339Nano))
	if senderPubKey != "" && !identity.Verify(sigData, msg.Signature, msg.SenderID, senderPubKey) {
		m.log.Warn("Ungültige Signatur", zap.String("from", msg.SenderID[:10]+"…"))
		return
	}

	// XChaCha20 entschlüsseln: SenderPubX aus der Nachricht
	if msg.SenderPubX == "" {
		m.log.Warn("SenderPubX fehlt")
		return
	}
	sharedSecret, err := m.identity.ECDHSharedSecret(msg.SenderPubX)
	if err != nil {
		m.log.Debug("ECDH fehlgeschlagen (nicht für uns)", zap.Error(err))
		return
	}

	encBytes, err := hex.DecodeString(msg.EncryptedPayload)
	if err != nil {
		m.log.Warn("hex-Decode fehlgeschlagen", zap.Error(err))
		return
	}

	plainBytes, err := identity.Decrypt(encBytes, sharedSecret)
	if err != nil {
		m.log.Warn("Entschlüsselung fehlgeschlagen (falscher Empfänger-Key?)", zap.Error(err), zap.String("from", msg.SenderID[:10]+"…"))
		return
	}

	var payload Payload
	if err := json.Unmarshal(plainBytes, &payload); err != nil { return }

	m.log.Debug("Nachricht empfangen",
		zap.String("type", string(msg.Type)),
		zap.String("from", msg.SenderID[:10]+"…"),
	)

	// Automatische Zustellungsquittung für echte Nachrichten (Text/Datei).
	// NICHT für Quittungen selbst (sonst Endlosschleife) und nicht für
	// Signal/Presence (flüchtig). Best-effort, blockiert die Zustellung nicht.
	if msg.Type == TypeText || msg.Type == TypeFile {
		go func(toID, toPubX, forID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := m.SendReceipt(ctx, toID, toPubX, forID, ReceiptDelivered); err != nil {
				m.log.Debug("Zustellungsquittung fehlgeschlagen", zap.Error(err))
			}
		}(msg.SenderID, msg.SenderPubX, msg.ID)
	}

	m.mu.RLock(); handlers := m.handlers; m.mu.RUnlock()
	for _, h := range handlers { go h(&msg, &payload) }
}

// =============================================================================
//  Kontakte
// =============================================================================

type Contact struct {
	FundusID      string    `json:"fundus_id"`
	Ed25519PubKey string    `json:"ed25519_pub_key"` // für Signaturen
	X25519PubKey  string    `json:"x25519_pub_key"`  // für ECDH
	Alias         string    `json:"alias,omitempty"`
	LastSeen      time.Time `json:"last_seen"`
	Online        bool      `json:"online"`
}

var (
	contactMu    sync.RWMutex
	contactStore = make(map[string]*Contact)
)

func (m *Messenger) AddContact(c *Contact) {
	contactMu.Lock(); defer contactMu.Unlock()
	contactStore[strings.ToLower(c.FundusID)] = c
}

func (m *Messenger) lookupContactPubKey(fundusID string) (string, error) {
	contactMu.RLock(); defer contactMu.RUnlock()
	c, ok := contactStore[strings.ToLower(fundusID)]
	if !ok { return "", fmt.Errorf("unbekannter Kontakt") }
	return c.Ed25519PubKey, nil
}

func (m *Messenger) Contacts() []*Contact {
	contactMu.RLock(); defer contactMu.RUnlock()
	list := make([]*Contact, 0, len(contactStore))
	for _, c := range contactStore { list = append(list, c) }
	return list
}

// =============================================================================
//  Präsenz
// =============================================================================

func (m *Messenger) PublishPresence(ctx context.Context, online bool) error {
	pub := m.identity.PublicRecord()
	status := map[string]interface{}{
		"fundus_id":     pub.FundusID,
		"ed25519_pub":   pub.Ed25519PubKey,
		"x25519_pub":    pub.X25519PubKey,
		"online":        online,
		"ts":            time.Now().UTC(),
	}
	data, _ := json.Marshal(status)
	return m.p2p.Publish(ctx, TopicPrefix+"presence", data)
}

// =============================================================================
//  Hilfsfunktionen
// =============================================================================

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
