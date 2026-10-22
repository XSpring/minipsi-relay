package core

import (
	"errors"

	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/psi"
)

// toError maps an error of the core or of the relay connection to a *psi.Error of session id.
// Code is the abort code exchanged with the peer, when there is one.
func toError(id string, err error) *psi.Error {
	e := &psi.Error{SessionID: id, Cause: err}
	var pe *protocol.Error
	var te *protocol.TransportError
	switch {
	case errors.As(err, &pe):
		e.Code = pe.Code.String()
		if pe.Remote {
			e.Kind, e.Remote = psi.ErrPeerAborted, true
		} else {
			e.Kind = kindOf(pe.Code)
		}
	case errors.As(err, &te):
		switch {
		case errors.Is(err, errDelivery):
			e.Kind = psi.ErrDelivery
		case errors.Is(err, errRefused):
			e.Kind, e.Code = psi.ErrProtocol, protocol.CodeProtocol.String()
		case errors.Is(err, errClosed):
			e.Kind = psi.ErrCanceled
		default:
			e.Kind = psi.ErrInternal
		}
	default:
		e.Kind = psi.ErrInternal
	}
	return e
}

// kindOf maps a local abort code to a session failure kind. Run checks the input before the
// core starts, so CodeLimit here means that the peer announced sizes above the local limits.
func kindOf(c protocol.Code) error {
	switch c {
	case protocol.CodeInput:
		return psi.ErrInput
	case protocol.CodeParamMismatch:
		return psi.ErrMismatch
	case protocol.CodeProtocol, protocol.CodeBadEncoding, protocol.CodeBadPoint, protocol.CodeLimit:
		return psi.ErrProtocol
	case protocol.CodeTimeout:
		return psi.ErrTimeout
	case protocol.CodeCancelled:
		return psi.ErrCanceled
	}
	return psi.ErrInternal
}
