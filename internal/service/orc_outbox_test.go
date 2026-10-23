package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XSpring/minipsi/internal/orcapi"
	"github.com/XSpring/minipsi/internal/psi"
)

// captured is one request that a test ORC received.
type captured struct {
	method, path, contentType string
	body                      []byte
}

// captureServer answers every request with status and records it.
func captureServer(t *testing.T, status int) (*httptest.Server, func() []captured) {
	t.Helper()
	var mu sync.Mutex
	var got []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, captured{r.Method, r.URL.Path, r.Header.Get("Content-Type"), body})
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"title":"x","status":1,"code":"internal"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []captured {
		mu.Lock()
		defer mu.Unlock()
		return append([]captured(nil), got...)
	}
}

func testClient(t *testing.T, url string) *orcClient {
	t.Helper()
	c, err := newORCClient(url, &http.Client{Timeout: 5 * time.Second}, "cc-1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOutboxPostsEnvelope(t *testing.T) {
	srv, got := captureServer(t, http.StatusAccepted)
	out := &orcOutbox{c: testClient(t, srv.URL+"/api"), sessionID: "s-1", peerCCID: "cc-2"}
	m := &psi.Message{SessionID: "s-1", Seq: 7, Kind: "poly_chunk", Payload: json.RawMessage(`{"parts":[]}`)}
	if err := out.Post(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	reqs := got()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.method != http.MethodPost || r.path != "/api/v1/messages" || r.contentType != "application/json" {
		t.Errorf("request %s %s (%s)", r.method, r.path, r.contentType)
	}
	var env orcapi.Envelope
	if err := orcapi.Decode(r.body, &env); err != nil {
		t.Fatal(err)
	}
	want := orcapi.Envelope{SessionID: "s-1", SenderCCID: "cc-1", ReceiverCCID: "cc-2", Seq: 7,
		Kind: "poly_chunk", Payload: json.RawMessage(`{"parts":[]}`)}
	if env.SessionID != want.SessionID || env.SenderCCID != want.SenderCCID || env.ReceiverCCID != want.ReceiverCCID ||
		env.Seq != want.Seq || env.Kind != want.Kind || string(env.Payload) != string(want.Payload) {
		t.Errorf("envelope %+v, want %+v", env, want)
	}
}

func TestReportStatusPath(t *testing.T) {
	srv, got := captureServer(t, http.StatusNoContent)
	c := testClient(t, srv.URL)
	rep := &orcapi.StatusReport{CCID: "cc-1", State: orcapi.StateReady}
	if err := c.reportStatus(context.Background(), "s-1", rep); err != nil {
		t.Fatal(err)
	}
	r := got()[0]
	if r.path != "/v1/sessions/s-1/status" {
		t.Errorf("path %s", r.path)
	}
	var back orcapi.StatusReport
	if err := orcapi.Decode(r.body, &back); err != nil || back.State != orcapi.StateReady || back.CCID != "cc-1" {
		t.Errorf("report %+v, %v", back, err)
	}
	if err := c.reportStatus(context.Background(), "a b", rep); err == nil {
		t.Error("invalid session ID: no error")
	}
}

func TestPostRefusals(t *testing.T) {
	const marker = "SECRETMARKER"
	srv, got := captureServer(t, http.StatusServiceUnavailable)
	c := testClient(t, srv.URL)
	out := &orcOutbox{c: c, sessionID: "s-1", peerCCID: "cc-2"}
	m := &psi.Message{SessionID: "s-1", Seq: 1, Kind: "hello", Payload: json.RawMessage(`{"x":"` + marker + `"}`)}

	err := out.Post(context.Background(), m)
	if err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), marker) {
		t.Errorf("503: %v", err)
	}
	if n := len(got()); n != 1 {
		t.Fatalf("%d requests, want 1 (no retry)", n)
	}
	// Refused before sending: another session, an invalid message.
	if err := out.Post(context.Background(), &psi.Message{SessionID: "s-2", Seq: 1, Kind: "hello", Payload: m.Payload}); err == nil {
		t.Error("another session: no error")
	}
	if err := out.Post(context.Background(), &psi.Message{SessionID: "s-1", Kind: "hello", Payload: m.Payload}); err == nil ||
		strings.Contains(err.Error(), marker) {
		t.Errorf("seq 0: %v", err)
	}
	if err := out.Post(context.Background(), nil); err == nil {
		t.Error("nil message: no error")
	}
	if n := len(got()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestPostHonoursContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block); srv.Close() })
	c := testClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.reportStatus(ctx, "s-1", &orcapi.StatusReport{CCID: "cc-1", State: orcapi.StateReady})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Errorf("%v after %v", err, time.Since(start))
	}
}

func TestORCURL(t *testing.T) {
	for _, u := range []string{"", "orc:8080", "ftp://orc", "http://", "http://:8080", "http://orc?x=1", "http://orc#f", "http://user:SECRET@[::1"} {
		_, err := newORCClient(u, http.DefaultClient, "cc-1")
		if err == nil {
			t.Errorf("%q: no error", u)
		} else if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%q: error repeats the URL: %v", u, err)
		}
	}
	for _, u := range []string{"http://orc", "https://orc.example:8443/base/"} {
		if _, err := newORCClient(u, http.DefaultClient, "cc-1"); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	m := &psi.Message{SessionID: "s-1", Seq: 3, Kind: "done", Payload: json.RawMessage(`{"a":1}`)}
	env := toEnvelope(m, "cc-1", "cc-2")
	if env.SenderCCID != "cc-1" || env.ReceiverCCID != "cc-2" {
		t.Errorf("CC IDs %+v", env)
	}
	back := toMessage(env)
	if back.SessionID != m.SessionID || back.Seq != m.Seq || back.Kind != m.Kind || string(back.Payload) != string(m.Payload) {
		t.Errorf("round trip %+v, want %+v", back, m)
	}
}
