package psi

import "time"

// ReceiverResult is the outcome of a successful receiver session.
type ReceiverResult struct {
	// Matches lists the distinct input values that are in the intersection, in the order of
	// their first occurrence in the items given to Run. The slices share memory with those
	// items. Never log them.
	Matches [][]byte
	Stats   Stats
}

// SenderResult is the outcome of a successful sender session.
type SenderResult struct {
	Stats Stats
}

// Stats describes a finished session. It contains no items, field elements, scalars or
// protocol outputs.
type Stats struct {
	// ProtocolSID is the hex session ID derived in the handshake from both parties' nonces;
	// the peer reports the same value. It is not the orchestrator's session ID.
	ProtocolSID string
	// Items is the number of items given to Run.
	Items int
	// DuplicatesRemoved is the number of exact duplicate items that Run dropped before the
	// session, for either role.
	DuplicatesRemoved int
	// MessagesSent is the number of messages accepted by the orchestrator.
	MessagesSent uint64
	// MessagesReceived is the number of messages accepted by Deliver, duplicates excluded.
	MessagesReceived uint64
	// BytesSent is the total payload size of the sent messages.
	BytesSent int64
	// BytesReceived is the total payload size of the received messages.
	BytesReceived int64
	// Duration is the wall time of Run.
	Duration time.Duration
	// Phases lists named intervals in the order they finished; they may overlap, and the last
	// one, "total", spans the whole run.
	Phases []Phase
}

// Phase is a named wall-clock interval of a session.
type Phase struct {
	Name     string
	Duration time.Duration
}
