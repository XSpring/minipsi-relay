package protocol

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Code is a wire error code carried in Abort frames.
type Code uint32

const (
	CodeProtocol      Code = 1 // unexpected frame / ordering / length mismatch
	CodeParamMismatch Code = 2 // negotiation failure
	CodeBadEncoding   Code = 3 // non-canonical field element / bad lengths
	CodeBadPoint      Code = 4 // invalid K_b
	CodeLimit         Code = 5 // size limits exceeded
	CodeTimeout       Code = 6 // phase deadline exceeded
	CodeInternal      Code = 7 // local failure
	CodeCancelled     Code = 8 // operator interrupt
	CodeInput         Code = 9 // local input rejected (duplicate item / H2F collision)
)

var codeNames = map[Code]string{
	CodeProtocol: "ERR_PROTOCOL", CodeParamMismatch: "ERR_PARAM_MISMATCH",
	CodeBadEncoding: "ERR_BAD_ENCODING", CodeBadPoint: "ERR_BAD_POINT", CodeLimit: "ERR_LIMIT",
	CodeTimeout: "ERR_TIMEOUT", CodeInternal: "ERR_INTERNAL", CodeCancelled: "ERR_CANCELLED",
	CodeInput: "ERR_INPUT",
}

func (c Code) String() string {
	if s, ok := codeNames[c]; ok {
		return s
	}
	return fmt.Sprintf("ERR_UNKNOWN(%d)", uint32(c))
}

// Error is a protocol failure. Remote is set when the peer sent the Abort; otherwise the
// failure was detected locally (and an Abort with Code was sent to the peer, best effort).
type Error struct {
	Code   Code
	Msg    string
	Remote bool
	Err    error // underlying cause, if any
}

func (e *Error) Error() string {
	who := "local"
	if e.Remote {
		who = "peer"
	}
	s := fmt.Sprintf("%s abort %s: %s", who, e.Code, e.Msg)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

func errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// TransportError reports a failure of the underlying Conn (peer unreachable, connection
// closed). No Abort can be delivered in that case.
type TransportError struct {
	Op  string // "send" or "recv"
	Err error
}

func (e *TransportError) Error() string { return "transport " + e.Op + ": " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// abortFor returns the Abort to send for a local error, or nil if none should be sent
// (remote aborts and transport failures).
func abortFor(err error) *Abort {
	var te *TransportError
	if errors.As(err, &te) {
		return nil
	}
	var pe *Error
	if errors.As(err, &pe) {
		if pe.Remote {
			return nil
		}
		return &Abort{Code: uint32(pe.Code), Message: pe.Msg}
	}
	return &Abort{Code: uint32(CodeInternal), Message: "internal error"}
}

// AbortConn sends an Abort with code and msg on a connection whose session never started,
// e.g. when a daemon cannot load the requested dataset, so the peer fails promptly instead
// of waiting for its handshake timeout. Best effort, bounded wait. msg must not contain
// items or local details the peer should not see.
func AbortConn(ctx context.Context, conn Conn, code Code, msg string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return conn.Send(ctx, &Frame{Abort: &Abort{Code: uint32(code), Message: msg}})
}
