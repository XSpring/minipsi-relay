package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/XSpring/minipsi/internal/orcapi"
	"github.com/XSpring/minipsi/internal/psi"
	"github.com/XSpring/minipsi/internal/service"
)

// fakeService records every call and returns err.
type fakeService struct {
	err   error
	calls []string
	id    string
	start *orcapi.StartSessionRequest
	abort *orcapi.AbortSessionRequest
	env   *orcapi.Envelope
}

func (f *fakeService) StartSession(_ context.Context, req *orcapi.StartSessionRequest) error {
	f.calls, f.start = append(f.calls, "start"), req
	return f.err
}

func (f *fakeService) RunSession(_ context.Context, id string) error {
	f.calls, f.id = append(f.calls, "run"), id
	return f.err
}

func (f *fakeService) AbortSession(_ context.Context, id string, req *orcapi.AbortSessionRequest) error {
	f.calls, f.id, f.abort = append(f.calls, "abort"), id, req
	return f.err
}

func (f *fakeService) DeleteSession(_ context.Context, id string) error {
	f.calls, f.id = append(f.calls, "delete"), id
	return f.err
}

func (f *fakeService) Deliver(_ context.Context, env *orcapi.Envelope) error {
	f.calls, f.env = append(f.calls, "deliver"), env
	return f.err
}

// golden returns an example body from internal/orcapi/testdata.
func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "orcapi", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func serve(svc Service, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	NewHandler(svc).ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		call                     string
	}{
		{"start", http.MethodPost, "/v1/sessions", golden(t, "start_session"), http.StatusAccepted, "start"},
		{"run", http.MethodPost, "/v1/sessions/s-1/run", "", http.StatusAccepted, "run"},
		{"abort", http.MethodPost, "/v1/sessions/s-1/abort", golden(t, "abort_session"), http.StatusAccepted, "abort"},
		{"delete", http.MethodDelete, "/v1/sessions/s-1", "", http.StatusNoContent, "delete"},
		{"message", http.MethodPost, "/v1/messages", golden(t, "envelope"), http.StatusAccepted, "deliver"},
	} {
		svc := &fakeService{}
		rec := serve(svc, tc.method, tc.path, tc.body)
		if rec.Code != tc.status || rec.Body.Len() != 0 {
			t.Errorf("%s: status %d body %q, want %d and no body", tc.name, rec.Code, rec.Body, tc.status)
		}
		if !reflect.DeepEqual(svc.calls, []string{tc.call}) {
			t.Errorf("%s: calls %v, want [%s]", tc.name, svc.calls, tc.call)
		}
	}
}

// TestRequestReachesService checks that the service gets the decoded body and the path ID.
func TestRequestReachesService(t *testing.T) {
	svc := &fakeService{}
	body := golden(t, "start_session")
	serve(svc, http.MethodPost, "/v1/sessions", body)
	var want orcapi.StartSessionRequest
	if err := orcapi.Decode([]byte(body), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(svc.start, &want) {
		t.Errorf("start: got %+v, want %+v", svc.start, &want)
	}

	svc = &fakeService{}
	serve(svc, http.MethodPost, "/v1/sessions/s.1:x/abort", `{"reason":"deadline"}`)
	if svc.id != "s.1:x" || svc.abort == nil || svc.abort.Reason != orcapi.AbortDeadline {
		t.Errorf("abort: id %q request %+v", svc.id, svc.abort)
	}

	svc = &fakeService{}
	serve(svc, http.MethodPost, "/v1/messages", golden(t, "envelope"))
	if svc.env == nil || svc.env.Seq != 1 || svc.env.Kind != "hello" || len(svc.env.Payload) == 0 {
		t.Errorf("deliver: got %+v", svc.env)
	}
}

func TestBadRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		code                     orcapi.ProblemCode
	}{
		{"not json", http.MethodPost, "/v1/sessions", `{`, http.StatusBadRequest, orcapi.CodeMalformed},
		{"broken rule", http.MethodPost, "/v1/sessions", `{"session_id":"s-1"}`, http.StatusBadRequest, orcapi.CodeMalformed},
		{"unknown field", http.MethodPost, "/v1/sessions/s-1/abort", `{"reason":"deadline","x":1}`, http.StatusBadRequest, orcapi.CodeMalformed},
		{"bad envelope", http.MethodPost, "/v1/messages", `{"seq":0}`, http.StatusBadRequest, orcapi.CodeMalformed},
		{"bad session id: run", http.MethodPost, "/v1/sessions/-x/run", "", http.StatusBadRequest, orcapi.CodeMalformed},
		{"bad session id: delete", http.MethodDelete, "/v1/sessions/-x", "", http.StatusBadRequest, orcapi.CodeMalformed},
		{"bad session id: abort", http.MethodPost, "/v1/sessions/-x/abort", `{"reason":"deadline"}`, http.StatusBadRequest, orcapi.CodeMalformed},
		{"control body too large", http.MethodPost, "/v1/sessions", strings.Repeat(" ", orcapi.MaxControlBytes+1), http.StatusRequestEntityTooLarge, orcapi.CodeTooLarge},
		{"envelope too large", http.MethodPost, "/v1/messages", strings.Repeat(" ", orcapi.MaxEnvelopeBytes+1), http.StatusRequestEntityTooLarge, orcapi.CodeTooLarge},
	} {
		svc := &fakeService{}
		rec := serve(svc, tc.method, tc.path, tc.body)
		checkProblem(t, tc.name, rec, tc.status, tc.code)
		if len(svc.calls) != 0 {
			t.Errorf("%s: service called: %v", tc.name, svc.calls)
		}
	}
	// A body that fails to read (for example a dropped connection) is malformed.
	svc0 := &fakeService{}
	rec := httptest.NewRecorder()
	NewHandler(svc0).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions", iotest.ErrReader(errors.New("reset"))))
	checkProblem(t, "read error", rec, http.StatusBadRequest, orcapi.CodeMalformed)
	if len(svc0.calls) != 0 {
		t.Errorf("read error: service called: %v", svc0.calls)
	}
	// A method that no pattern accepts is refused by the ServeMux.
	svc := &fakeService{}
	if rec := serve(svc, http.MethodGet, "/v1/messages", ""); rec.Code != http.StatusMethodNotAllowed || len(svc.calls) != 0 {
		t.Errorf("GET /v1/messages: status %d, calls %v", rec.Code, svc.calls)
	}
}

func TestServiceErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   orcapi.ProblemCode
	}{
		{psi.ErrMalformed, http.StatusBadRequest, orcapi.CodeMalformed},
		{psi.ErrUnknownSession, http.StatusNotFound, orcapi.CodeUnknownSession},
		{psi.ErrSessionExists, http.StatusConflict, orcapi.CodeInvalidState},
		{service.ErrInvalidState, http.StatusConflict, orcapi.CodeInvalidState},
		{service.ErrMisrouted, http.StatusUnprocessableEntity, orcapi.CodeMisrouted},
		{psi.ErrBusy, http.StatusServiceUnavailable, orcapi.CodeUnavailable},
		{context.Canceled, http.StatusServiceUnavailable, orcapi.CodeUnavailable},
		{context.DeadlineExceeded, http.StatusServiceUnavailable, orcapi.CodeUnavailable},
		{errors.New("unexpected"), http.StatusInternalServerError, orcapi.CodeInternal},
	} {
		svc := &fakeService{err: fmt.Errorf("wrapped: %w", tc.err)}
		rec := serve(svc, http.MethodPost, "/v1/messages", golden(t, "envelope"))
		checkProblem(t, tc.err.Error(), rec, tc.status, tc.code)
	}
}

// TestProblemsDoNotLeak keeps request values and service error text out of responses.
func TestProblemsDoNotLeak(t *testing.T) {
	const marker = "SECRETMARKER"
	rec := serve(&fakeService{}, http.MethodPost, "/v1/sessions", `{"session_id":"`+marker+`/"}`)
	if strings.Contains(rec.Body.String(), marker) {
		t.Errorf("response leaks the request: %s", rec.Body)
	}
	rec = serve(&fakeService{err: errors.New(marker)}, http.MethodPost, "/v1/sessions/s-1/run", "")
	if strings.Contains(rec.Body.String(), marker) {
		t.Errorf("response leaks the service error: %s", rec.Body)
	}
}

func checkProblem(t *testing.T, name string, rec *httptest.ResponseRecorder, status int, code orcapi.ProblemCode) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("%s: status %d, want %d", name, rec.Code, status)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("%s: content type %q", name, ct)
	}
	var p orcapi.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("%s: body %q: %v", name, rec.Body, err)
	}
	want := orcapi.Problem{Title: http.StatusText(status), Status: status, Code: code}
	if p != want {
		t.Errorf("%s: problem %+v, want %+v", name, p, want)
	}
}
