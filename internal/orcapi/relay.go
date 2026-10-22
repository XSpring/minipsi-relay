package orcapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
)

// Envelope carries one PSI message between the CCs of a session (CC to ORC, then ORC to the
// other CC, POST /v1/messages). It is psi.Message with the same JSON names, plus the CC IDs the
// ORC needs to route it: the service's Outbox adds them, and the handler removes them before it
// passes the message to the engine.
type Envelope struct {
	SessionID    string `json:"session_id"`
	SenderCCID   string `json:"sender_cc_id"`
	ReceiverCCID string `json:"receiver_cc_id"`
	// Seq numbers the messages one CC sends in a session, from 1 without gaps.
	Seq uint64 `json:"seq"`
	// Kind names the PSI frame type; the receiving engine needs it to decode Payload.
	Kind string `json:"kind"`
	// Payload is the PSI frame as JSON. Nobody but the engines reads it, and it is never
	// logged.
	Payload json.RawMessage `json:"payload"`
}

// Validate checks the rules of the envelope. It does not look inside Payload.
func (e *Envelope) Validate() error {
	var p problems
	if !ValidSessionID(e.SessionID) {
		p.add("session_id", "invalid session ID")
	}
	if !ValidCCID(e.SenderCCID) {
		p.add("sender_cc_id", "invalid CC ID")
	}
	if !ValidCCID(e.ReceiverCCID) {
		p.add("receiver_cc_id", "invalid CC ID")
	}
	if e.Seq == 0 {
		p.add("seq", "must start at 1")
	}
	if e.Kind == "" {
		p.add("kind", "required")
	}
	if t := bytes.TrimSpace(e.Payload); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		p.add("payload", "required")
	}
	return p.err()
}

// LogValue logs the routing fields and the payload size, never the payload.
func (e Envelope) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("session_id", e.SessionID),
		slog.String("sender_cc_id", e.SenderCCID),
		slog.String("receiver_cc_id", e.ReceiverCCID),
		slog.Uint64("seq", e.Seq),
		slog.String("kind", e.Kind),
		slog.Int("payload_bytes", len(e.Payload)),
	)
}
