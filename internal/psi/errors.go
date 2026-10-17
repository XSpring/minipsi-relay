package psi

import (
	"errors"
	"strings"
)

// Session failure kinds. Every error that Check and Run return is an *Error whose Kind is one
// of these, so errors.Is(err, ErrTimeout) works.
var (
	// ErrInput reports that the local items were rejected: by Check, by the same check that
	// Run makes before its first message (a local limit was exceeded), or because two distinct
	// items collide when hashed.
	ErrInput = errors.New("psi: input rejected")
	// ErrMismatch reports that the parties' parameters (protocol version, suite, hash, lambda
	// or roles) differ.
	ErrMismatch = errors.New("psi: parameters do not match the peer")
	// ErrProtocol reports that the peer broke the protocol or announced sizes above the local
	// limits.
	ErrProtocol = errors.New("psi: protocol violation by the peer")
	// ErrPeerAborted reports that the peer ended the session; Error.Code says why.
	ErrPeerAborted = errors.New("psi: session aborted by the peer")
	// ErrTimeout reports that a peer message did not arrive in time, or that the deadline of
	// the Run context passed.
	ErrTimeout = errors.New("psi: timed out")
	// ErrDelivery reports that messages could not be posted to the orchestrator.
	ErrDelivery = errors.New("psi: message delivery failed")
	// ErrCanceled reports that the Run context was cancelled or the session was closed.
	ErrCanceled = errors.New("psi: session cancelled")
	// ErrInternal reports a local failure of the engine.
	ErrInternal = errors.New("psi: internal error")
)

// Registry and message errors. Engine.OpenReceiver, Engine.OpenSender and Engine.Deliver
// return them wrapped; Outbox implementations return ErrSessionClosed and ErrMalformed wrapped
// for refusals that must not be retried.
var (
	// ErrBusy reports that the session limit is reached or the session's inbound queue stayed
	// full; retry later.
	ErrBusy = errors.New("psi: busy, retry later")
	// ErrSessionExists reports that the session ID is open or finished recently.
	ErrSessionExists = errors.New("psi: session already exists")
	// ErrUnknownSession reports that no session with this ID is open yet; retry later.
	ErrUnknownSession = errors.New("psi: session not open")
	// ErrOutOfOrder reports that a message arrived ahead of its sequence; retry later.
	ErrOutOfOrder = errors.New("psi: message ahead of sequence")
	// ErrSessionClosed reports that the session has ended; do not retry.
	ErrSessionClosed = errors.New("psi: session closed")
	// ErrMalformed reports an invalid or too large message, a message that conflicts with an
	// accepted one with the same Seq, or an invalid session ID; do not retry.
	ErrMalformed = errors.New("psi: malformed message or session ID")
)

// Error describes a failed session. It never contains items, payloads or results.
type Error struct {
	// Kind is one of ErrInput, ErrMismatch, ErrProtocol, ErrPeerAborted, ErrTimeout,
	// ErrDelivery, ErrCanceled and ErrInternal; nil is treated as ErrInternal.
	Kind error
	// SessionID is the orchestrator's session ID; it is empty for Check.
	SessionID string
	// Code is the abort code exchanged with the peer, such as "ERR_TIMEOUT"; it may be empty.
	Code string
	// Remote reports that the peer sent the abort.
	Remote bool
	// Cause is the implementation's error, for logs only. Unwrap does not return it, so
	// callers cannot depend on implementation error types.
	Cause error
}

// Error returns "psi: session <id>: <kind> (<code>[, peer]): <cause>", leaving out the parts
// that are empty.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("psi: ")
	if e.SessionID != "" {
		b.WriteString("session ")
		b.WriteString(e.SessionID)
		b.WriteString(": ")
	}
	b.WriteString(strings.TrimPrefix(e.kind().Error(), "psi: "))
	if e.Code != "" || e.Remote {
		b.WriteString(" (")
		b.WriteString(e.Code)
		if e.Remote {
			if e.Code != "" {
				b.WriteString(", ")
			}
			b.WriteString("peer")
		}
		b.WriteString(")")
	}
	if e.Cause != nil {
		b.WriteString(": ")
		b.WriteString(e.Cause.Error())
	}
	return b.String()
}

// Unwrap returns Kind, so that errors.Is(err, ErrTimeout) works.
func (e *Error) Unwrap() error { return e.kind() }

func (e *Error) kind() error {
	if e.Kind == nil {
		return ErrInternal
	}
	return e.Kind
}
