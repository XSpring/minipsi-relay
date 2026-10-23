package service_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/XSpring/minipsi/internal/handler"
	"github.com/XSpring/minipsi/internal/orcapi"
	"github.com/XSpring/minipsi/internal/psi"
	"github.com/XSpring/minipsi/internal/service"
)

var _ handler.Service = (*service.PSIService)(nil)

// The stubs hold the background work until the test has the 202 of I1 (loadGo) or I2 (runGo).
// The server cancels the request context before it sends the answer, so by then that context
// is done, and work that used it fails.
type stubEngine struct{ runGo chan struct{} }

func (stubEngine) Check(psi.Role, [][]byte) error { return nil }
func (e stubEngine) OpenReceiver(string, psi.Outbox) (psi.ReceiverSession, error) {
	return stubSession(e), nil
}
func (stubEngine) OpenSender(string, psi.Outbox) (psi.SenderSession, error) { return nil, psi.ErrBusy }
func (stubEngine) Deliver(context.Context, *psi.Message) error              { return nil }

type stubSession struct{ runGo chan struct{} }

func (s stubSession) Run(ctx context.Context, items [][]byte) (*psi.ReceiverResult, error) {
	<-s.runGo
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &psi.ReceiverResult{Matches: items, Stats: psi.Stats{ProtocolSID: "ab"}}, nil
}
func (stubSession) Close() error { return nil }

type stubCatalog struct{ loadGo chan struct{} }

func (c stubCatalog) Load(ctx context.Context, _ string) ([][]byte, error) {
	<-c.loadGo
	return [][]byte{[]byte("a")}, ctx.Err()
}

// TestFlowThroughHandler drives a session through the real HTTP handler: background work
// must survive the end of each request.
func TestFlowThroughHandler(t *testing.T) {
	states := make(chan orcapi.State, 4)
	orc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rep orcapi.StatusReport
		if err := orcapi.Decode(body, &rep); err == nil {
			states <- rep.State
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer orc.Close()
	loadGo, runGo := make(chan struct{}), make(chan struct{})
	svc, err := service.New(service.Config{CCID: "cc-1", ORCURL: orc.URL,
		Engine: stubEngine{runGo}, Catalog: stubCatalog{loadGo}})
	if err != nil {
		t.Fatal(err)
	}
	cc := httptest.NewServer(handler.NewHandler(svc))
	defer cc.Close()

	call := func(method, path, body string, want int) {
		t.Helper()
		req, _ := http.NewRequest(method, cc.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d", method, path, resp.StatusCode, want)
		}
	}
	next := func(want orcapi.State) {
		t.Helper()
		select {
		case st := <-states:
			if st != want {
				t.Fatalf("state %s, want %s", st, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s report", want)
		}
	}

	call(http.MethodPost, "/v1/sessions", `{"session_id":"s-1","cc_id":"cc-1","peer_cc_id":"cc-2","role":"receiver",
		"contract_ref":"urn:c:1","dataset":"customers","prep":["trim"]}`, http.StatusAccepted)
	close(loadGo)
	next(orcapi.StateReady)
	call(http.MethodPost, "/v1/sessions/s-1/run", "", http.StatusAccepted)
	close(runGo)
	next(orcapi.StateDone)
	call(http.MethodPost, "/v1/sessions/s-1/run", "", http.StatusConflict)
	call(http.MethodDelete, "/v1/sessions/s-1", "", http.StatusNoContent)
	call(http.MethodDelete, "/v1/sessions/s-1", "", http.StatusNotFound)
}
