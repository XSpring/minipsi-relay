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
	// ErrDelivery reports that a message could not be posted to the orchestrator.
	ErrDelivery = errors.New("psi: message delivery failed")
	// ErrCanceled reports that the Run context was canceled or the session was closed.
	ErrCanceled = errors.New("psi: session canceled")
	// ErrInternal reports a local failure of the engine.
	ErrInternal = errors.New("psi: internal error")
)

// Registry and message errors, returned wrapped by Engine.OpenReceiver, Engine.OpenSender and
// Engine.Deliver.
var (
	// ErrBusy reports that the session limit is reached.
	ErrBusy = errors.New("psi: too many sessions")
	// ErrSessionExists reports that a session with this ID is already open.
	ErrSessionExists = errors.New("psi: session already exists")
	// ErrUnknownSession reports that no session with this ID is open.
	ErrUnknownSession = errors.New("psi: session not open")
	// ErrMalformed reports an invalid or too large message, a message out of sequence, or an
	// invalid session ID.
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
