package psi

import (
	"context"
	"encoding/json"
)

// Message is one protocol message of one session, exchanged between a CC and the
// orchestrator. The Engine fills every field of an outbound message; services pass an inbound
// message to Engine.Deliver unchanged.
//
// SessionID is assigned by the orchestrator and must satisfy ValidSessionID. Seq numbers the
// messages that one party sends in a session, from 1 without gaps; the receiving Engine checks
// it, so a lost, repeated or reordered message ends the session. Kind names the payload type;
// the Engine needs it to decode Payload, so services and the orchestrator forward it unchanged.
// Kind is safe to log. Payload is JSON defined by the Engine implementation; services and the
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
