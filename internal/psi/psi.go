package psi

import (
	"context"
	"encoding/json"
	"fmt"
)

// MaxMessageBytes is the largest JSON encoding of a Message that an Engine produces or
// accepts. Inbound handlers and the orchestrator must accept bodies of this size.
const MaxMessageBytes = 8 << 20

// MaxSessionIDLen is the longest session ID that an Engine accepts (see ValidSessionID).
const MaxSessionIDLen = 128

// ValidSessionID reports whether id is a valid session ID: 1 to MaxSessionIDLen bytes, each an
// ASCII letter, digit, '-', '_', '.' or ':'. Engines refuse other values with ErrMalformed,
// which also keeps session IDs safe to write into logs.
func ValidSessionID(id string) bool {
	if len(id) == 0 || len(id) > MaxSessionIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

// Role is the part a party plays in a session.
type Role uint8

const (
	// RoleReceiver learns the intersection.
	RoleReceiver Role = 1
	// RoleSender helps compute the intersection and learns nothing about it.
	RoleSender Role = 2
)

// String returns "receiver", "sender", or "Role(n)" for any other value n.
func (r Role) String() string {
	switch r {
	case RoleReceiver:
		return "receiver"
	case RoleSender:
		return "sender"
	default:
		return fmt.Sprintf("Role(%d)", uint8(r))
	}
}

// Message is one protocol message of one session, exchanged between a CC and the
// orchestrator. The Engine fills every field of an outbound message; services pass an inbound
// message to Engine.Deliver unchanged.
//
// SessionID is assigned by the orchestrator and must satisfy ValidSessionID. Seq numbers the
// messages that one party sends in a session, from 1 without gaps; the receiving Engine checks
// it, so a lost, repeated or reordered message ends the session. Kind names the payload type,
// for logs only. Payload is JSON defined by the Engine implementation; services and the
// orchestrator treat it as opaque, forward it unchanged and never log it. An Engine does not
// modify a Message after passing it to Outbox.Post.
type Message struct {
	SessionID string          `json:"session_id"`
	Seq       uint64          `json:"seq"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

// Outbox sends the messages of a session to the orchestrator. The service implements it, for
// example with an HTTP POST, and usually creates a new Outbox for each session.
//
// The Engine never calls Post concurrently for one session; it calls it in Seq order, one
// message at a time. If one Outbox is passed to several sessions, Post may be called
// concurrently and must be safe for concurrent use. Post returns nil only after the
// orchestrator has accepted m. Any error ends the session: the Engine does not retry, and Run
// returns an *Error with Kind ErrDelivery. Post must return promptly once ctx is done.
type Outbox interface {
	Post(ctx context.Context, m *Message) error
}

// Engine runs PSI sessions. Implementations are safe for concurrent use.
type Engine interface {
	// Check validates items for role without any network activity, so that a service can
	// refuse a session that the orchestrator started before any message is sent. It applies the same
	// rules as Run: exact duplicate items are dropped first, keeping the first occurrence, and
	// the limits apply to the distinct items after padding. It does not hash the items, so the
	// receiver's Run can still fail with ErrInput when two items collide. A failure, including
	// an invalid role, is an *Error with Kind ErrInput.
	Check(role Role, items [][]byte) error

	// OpenReceiver registers sessionID, assigned by the orchestrator, for this party as the
	// receiver. From then on, Deliver buffers the peer's messages for the session until Run
	// starts; out carries this party's messages. It returns an error that wraps ErrMalformed
	// (a session ID that fails ValidSessionID), ErrSessionExists or ErrBusy.
	OpenReceiver(sessionID string, out Outbox) (ReceiverSession, error)

	// OpenSender is OpenReceiver for the sender role.
	OpenSender(sessionID string, out Outbox) (SenderSession, error)

	// Deliver hands one inbound message from the orchestrator to its session. It returns nil
	// when the message was accepted. Otherwise it returns an error that wraps:
	//   - ErrUnknownSession: no session with this ID is open (not opened yet, or already
	//     ended);
	//   - ErrMalformed: the message is invalid, too large or out of sequence. For an open
	//     session this also ends the session: Run returns an *Error with Kind ErrProtocol and
	//     the peer is told;
	//   - ctx.Err(): ctx was done before the message could be queued.
	// Deliver does not wait for the session to process the message; it may wait while the
	// session's inbound queue is full. Deliver may keep m.Payload after it returns; the caller
	// must not modify m or its Payload afterwards.
	Deliver(ctx context.Context, m *Message) error
}

// ReceiverSession is an open session of the receiver. Run may be called once.
type ReceiverSession interface {
	// Run executes the session over items and returns the matching values. Exact duplicate
	// items are dropped before the session, as Check describes. Run returns after the session
	// has ended and this party's last message has been posted, or a post has failed. Canceling
	// ctx aborts the session.
	//
	// A failure is an *Error. On a local failure, including one before the first message is
	// sent (such as ErrInput) and a canceled ctx, Run tells the peer to stop before it
	// returns (best effort, bounded wait); it does not after ErrPeerAborted or ErrDelivery.
	Run(ctx context.Context, items [][]byte) (*ReceiverResult, error)

	// Close releases the session. If Run was never called, the peer is told to stop waiting;
	// if Run is running, it is canceled. Close is idempotent.
	Close() error
}

// SenderSession is an open session of the sender. Run may be called once.
type SenderSession interface {
	// Run executes the session over items. The sender learns nothing about the intersection.
	// It behaves as ReceiverSession.Run otherwise.
	Run(ctx context.Context, items [][]byte) (*SenderResult, error)

	// Close behaves as ReceiverSession.Close.
	Close() error
}
