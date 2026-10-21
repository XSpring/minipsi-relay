package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/XSpring/minipsi/internal/orcapi"
	"github.com/XSpring/minipsi/internal/psi"
	"github.com/XSpring/minipsi/internal/service"
)

// reply writes ok when err is nil, and the problem for err otherwise.
func reply(w http.ResponseWriter, err error, ok int) {
	if err == nil {
		w.WriteHeader(ok)
		return
	}
	status, code := problemFor(err)
	writeProblem(w, status, code)
}

// problemFor maps an error of the service to an HTTP status and a problem code.
func problemFor(err error) (int, orcapi.ProblemCode) {
	switch {
	case errors.Is(err, psi.ErrMalformed):
		return http.StatusBadRequest, orcapi.CodeMalformed
	case errors.Is(err, psi.ErrUnknownSession):
		return http.StatusNotFound, orcapi.CodeUnknownSession
	case errors.Is(err, psi.ErrSessionExists), errors.Is(err, service.ErrInvalidState):
		return http.StatusConflict, orcapi.CodeInvalidState
	case errors.Is(err, service.ErrMisrouted):
		return http.StatusUnprocessableEntity, orcapi.CodeMisrouted
	case errors.Is(err, psi.ErrBusy), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, orcapi.CodeUnavailable
	default:
		return http.StatusInternalServerError, orcapi.CodeInternal
	}
}

// writeProblem writes a Problem body (RFC 9457). It holds fixed text only, never request values.
func writeProblem(w http.ResponseWriter, status int, code orcapi.ProblemCode) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(orcapi.Problem{Title: http.StatusText(status), Status: status, Code: code})
}
