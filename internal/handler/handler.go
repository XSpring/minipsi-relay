// Package handler holds the HTTP endpoints that the orchestrator calls on this CC. A handler
// decodes the request, calls internal/service and turns its errors into HTTP status codes; it
// holds no PSI logic. The endpoints and messages are defined in internal/orcapi.
package handler

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/XSpring/minipsi/internal/orcapi"
)

// Service is what the handler needs from the service layer. Every method returns quickly:
// StartSession and RunSession start work in the background and the service reports its outcome
// to the orchestrator, and Deliver only queues the message for the session.
type Service interface {
	StartSession(ctx context.Context, req *orcapi.StartSessionRequest) error
	RunSession(ctx context.Context, sessionID string) error
	AbortSession(ctx context.Context, sessionID string, req *orcapi.AbortSessionRequest) error
	DeleteSession(ctx context.Context, sessionID string) error
	Deliver(ctx context.Context, env *orcapi.Envelope) error
}

// NewHandler returns the HTTP endpoints that the orchestrator calls on this CC.
func NewHandler(svc Service) http.Handler {
	h := &handler{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc(orcapi.PatternStartSession, h.startSession)
	mux.HandleFunc(orcapi.PatternRunSession, h.runSession)
	mux.HandleFunc(orcapi.PatternAbortSession, h.abortSession)
	mux.HandleFunc(orcapi.PatternDeleteSession, h.deleteSession)
	mux.HandleFunc(orcapi.PatternMessages, h.postMessage)
	return mux
}

type handler struct {
	svc Service
}

// startSession serves I1, POST /v1/sessions.
func (h *handler) startSession(w http.ResponseWriter, r *http.Request) {
	var req orcapi.StartSessionRequest
	if !decodeBody(w, r, orcapi.MaxControlBytes, &req) {
		return
	}
	reply(w, h.svc.StartSession(r.Context(), &req), http.StatusAccepted)
}

// runSession serves I2, POST /v1/sessions/{session_id}/run.
func (h *handler) runSession(w http.ResponseWriter, r *http.Request) {
	id, ok := sessionID(w, r)
	if !ok {
		return
	}
	reply(w, h.svc.RunSession(r.Context(), id), http.StatusAccepted)
}

// abortSession serves I3, POST /v1/sessions/{session_id}/abort.
func (h *handler) abortSession(w http.ResponseWriter, r *http.Request) {
	id, ok := sessionID(w, r)
	if !ok {
		return
	}
	var req orcapi.AbortSessionRequest
	if !decodeBody(w, r, orcapi.MaxControlBytes, &req) {
		return
	}
	reply(w, h.svc.AbortSession(r.Context(), id, &req), http.StatusAccepted)
}

// deleteSession serves I4, DELETE /v1/sessions/{session_id}.
func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := sessionID(w, r)
	if !ok {
		return
	}
	reply(w, h.svc.DeleteSession(r.Context(), id), http.StatusNoContent)
}

// postMessage serves I5, POST /v1/messages: one PSI frame from the other CC.
func (h *handler) postMessage(w http.ResponseWriter, r *http.Request) {
	var env orcapi.Envelope
	if !decodeBody(w, r, orcapi.MaxEnvelopeBytes, &env) {
		return
	}
	reply(w, h.svc.Deliver(r.Context(), &env), http.StatusAccepted)
}

// sessionID returns the session ID of the path, or writes 400 malformed.
func sessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("session_id")
	if !orcapi.ValidSessionID(id) {
		writeProblem(w, http.StatusBadRequest, orcapi.CodeMalformed)
		return "", false
	}
	return id, true
}

// decodeBody reads at most limit bytes of the body and decodes them into v, or writes 413
// too_large or 400 malformed.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v orcapi.Validator) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, http.StatusRequestEntityTooLarge, orcapi.CodeTooLarge)
		} else {
			writeProblem(w, http.StatusBadRequest, orcapi.CodeMalformed)
		}
		return false
	}
	if err := orcapi.Decode(data, v); err != nil {
		writeProblem(w, http.StatusBadRequest, orcapi.CodeMalformed)
		return false
	}
	return true
}
