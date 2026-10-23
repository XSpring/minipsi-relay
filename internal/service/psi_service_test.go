package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XSpring/minipsi/internal/orcapi"
	"github.com/XSpring/minipsi/internal/psi"
)

// --- fakes -------------------------------------------------------------------------------

// fakeEngine records what the service asks of it. run decides how Run ends; the default
// returns the first item as the match.
type fakeEngine struct {
	mu         sync.Mutex
	checkErr   error
	openErr    error
	deliverErr error
	checked    [][]byte
	opened     []string // "receiver:<id>" or "sender:<id>"
	delivered  []*psi.Message
	closes     int
	closeGate  chan struct{} // when set, Close waits for it
	checkGate  chan struct{} // when set, Check closes checkIn, then waits for checkGate
	checkIn    chan struct{}
	run        func(ctx context.Context, items [][]byte) ([][]byte, psi.Stats, error)
}

func (e *fakeEngine) Check(_ psi.Role, items [][]byte) error {
	e.mu.Lock()
	gate, in := e.checkGate, e.checkIn
	e.mu.Unlock()
	if gate != nil {
		close(in)
		<-gate
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checked = items
	return e.checkErr
}

func (e *fakeEngine) open(kind, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.openErr != nil {
		return e.openErr
	}
	e.opened = append(e.opened, kind+":"+id)
	return nil
}

func (e *fakeEngine) OpenReceiver(id string, _ psi.Outbox) (psi.ReceiverSession, error) {
	if err := e.open("receiver", id); err != nil {
		return nil, err
	}
	return fakeReceiver{e}, nil
}

func (e *fakeEngine) OpenSender(id string, _ psi.Outbox) (psi.SenderSession, error) {
	if err := e.open("sender", id); err != nil {
		return nil, err
	}
	return fakeSender{e}, nil
}

func (e *fakeEngine) Deliver(_ context.Context, m *psi.Message) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.delivered = append(e.delivered, m)
	return e.deliverErr
}

func (e *fakeEngine) runSession(ctx context.Context, items [][]byte) ([][]byte, psi.Stats, error) {
	if e.run != nil {
		return e.run(ctx, items)
	}
	st := psi.Stats{ProtocolSID: "ab12", MessagesSent: 4, MessagesReceived: 5, Duration: 1500 * time.Millisecond}
	return items[:1], st, nil
}

func (e *fakeEngine) close() error {
	e.mu.Lock()
	gate := e.closeGate
	e.mu.Unlock()
	if gate != nil {
		<-gate
	}
	e.mu.Lock()
	e.closes++
	e.mu.Unlock()
	return nil
}

// seen returns what Check received and which sessions were opened.
func (e *fakeEngine) seen() (checked string, opened string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("%q", e.checked), fmt.Sprint(e.opened)
}

func (e *fakeEngine) closeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closes
}

type fakeReceiver struct{ e *fakeEngine }

func (r fakeReceiver) Run(ctx context.Context, items [][]byte) (*psi.ReceiverResult, error) {
	m, st, err := r.e.runSession(ctx, items)
	if err != nil {
		return nil, err
	}
	return &psi.ReceiverResult{Matches: m, Stats: st}, nil
}

func (r fakeReceiver) Close() error { return r.e.close() }

type fakeSender struct{ e *fakeEngine }

func (s fakeSender) Run(ctx context.Context, items [][]byte) (*psi.SenderResult, error) {
	_, st, err := s.e.runSession(ctx, items)
	if err != nil {
		return nil, err
	}
	return &psi.SenderResult{Stats: st}, nil
}

func (s fakeSender) Close() error { return s.e.close() }

// fakeCatalog returns the values of known datasets; load, when set, replaces it.
type fakeCatalog struct {
	data map[string][][]byte
	load func(ctx context.Context, name string) ([][]byte, error)
}

func (c *fakeCatalog) Load(ctx context.Context, name string) ([][]byte, error) {
	if c.load != nil {
		return c.load(ctx, name)
	}
	v, ok := c.data[name]
	if !ok {
		return nil, errors.New("unknown dataset")
	}
	return v, nil
}

// report is one status report that the fake ORC received, and its raw body.
type report struct {
	sessionID string
	rep       orcapi.StatusReport
	body      []byte
}

// fakeORC receives O1 and O2. onReport runs after a report is recorded and before the ORC
// answers it, as a real ORC may call the CC back at once.
type fakeORC struct {
	srv      *httptest.Server
	reports  chan report
	mu       sync.Mutex
	status   int
	onReport func(report)
}

func (o *fakeORC) set(status int, onReport func(report)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status, o.onReport = status, onReport
}

func newFakeORC(t *testing.T) *fakeORC {
	t.Helper()
	o := &fakeORC{reports: make(chan report, 16), status: http.StatusAccepted}
	mux := http.NewServeMux()
	mux.HandleFunc(orcapi.PatternReportStatus, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var rep orcapi.StatusReport
		if err := orcapi.Decode(body, &rep); err != nil {
			t.Errorf("invalid status report: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ev := report{r.PathValue("session_id"), rep, body}
		o.mu.Lock()
		status, onReport := o.status, o.onReport
		o.mu.Unlock()
		// Recorded before the callback: a report that the callback causes comes after it.
		o.reports <- ev
		if onReport != nil {
			onReport(ev)
		}
		w.WriteHeader(status)
	})
	mux.HandleFunc(orcapi.PatternMessages, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	o.srv = httptest.NewServer(mux)
	t.Cleanup(o.srv.Close)
	return o
}

// next returns the next report, or fails the test after 5 s.
func (o *fakeORC) next(t *testing.T) report {
	t.Helper()
	select {
	case r := <-o.reports:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no status report")
		return report{}
	}
}

// none checks that no report arrives for a while.
func (o *fakeORC) none(t *testing.T) {
	t.Helper()
	select {
	case r := <-o.reports:
		t.Fatalf("unexpected report %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
}

// fakeTimers replaces time.AfterFunc: timers fire only when the test says so.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool { t.stopped = true; return true }

func (ft *fakeTimers) afterFunc(_ time.Duration, f func()) stopper {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{f: f}
	ft.timers = append(ft.timers, t)
	return t
}

// fireLast runs the newest timer, as if its TTL had passed, even if it was stopped.
func (ft *fakeTimers) fireLast() {
	ft.mu.Lock()
	t := ft.timers[len(ft.timers)-1]
	ft.mu.Unlock()
	t.f()
}

type env struct {
	svc    *PSIService
	eng    *fakeEngine
	cat    *fakeCatalog
	orc    *fakeORC
	timers *fakeTimers
	logs   *lockedWriter
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	e := &env{
		eng:    &fakeEngine{},
		cat:    &fakeCatalog{data: map[string][][]byte{"customers": values(" A@x ", "b@x", "  ")}},
		orc:    newFakeORC(t),
		timers: &fakeTimers{},
		logs:   &lockedWriter{w: &bytes.Buffer{}},
	}
	cfg.CCID, cfg.ORCURL, cfg.Engine, cfg.Catalog = "cc-1", e.orc.srv.URL, e.eng, e.cat
	cfg.Logger = slog.New(slog.NewTextHandler(e.logs, nil))
	svc, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc.afterFunc = e.timers.afterFunc
	e.svc = svc
	return e
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func startReq(id string, role orcapi.Role) *orcapi.StartSessionRequest {
	return &orcapi.StartSessionRequest{SessionID: id, CCID: "cc-1", PeerCCID: "cc-2", Role: role,
		ContractRef: "urn:c:1", Dataset: "customers", Prep: []orcapi.PrepStep{orcapi.PrepTrim, orcapi.PrepLower}}
}

func (e *env) start(t *testing.T, id string, role orcapi.Role) {
	t.Helper()
	if err := e.svc.StartSession(context.Background(), startReq(id, role)); err != nil {
		t.Fatal(err)
	}
}

func wantState(t *testing.T, r report, st orcapi.State, kind orcapi.ErrorKind) {
	t.Helper()
	if r.rep.State != st || r.rep.CCID != "cc-1" {
		t.Fatalf("report %+v, want state %s", r.rep, st)
	}
	if kind != "" && (r.rep.Error == nil || r.rep.Error.Kind != kind) {
		t.Fatalf("report %+v, want kind %s", r.rep, kind)
	}
}

func (e *env) state(id string) state {
	r := e.svc.store.get(id)
	if r == nil {
		return -1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// --- tests -------------------------------------------------------------------------------

func TestSessionReceiverFlow(t *testing.T) {
	e := newEnv(t, Config{})
	ctx := context.Background()
	e.start(t, "s-1", orcapi.RoleReceiver)
	wantState(t, e.orc.next(t), orcapi.StateReady, "")
	if checked, opened := e.eng.seen(); checked != `["a@x" "b@x"]` || opened != "[receiver:s-1]" {
		t.Errorf("checked %s (want the prepared values), opened %s", checked, opened)
	}

	if err := e.svc.RunSession(ctx, "s-1"); err != nil {
		t.Fatal(err)
	}
	r := e.orc.next(t)
	wantState(t, r, orcapi.StateDone, "")
	if s := r.rep.Stats; s.ProtocolSID != "ab12" || s.Messages.Sent != 4 || s.Messages.Recv != 5 || s.DurationMS != 1500 {
		t.Errorf("stats %+v", s)
	}
	rec := e.svc.store.get("s-1")
	rec.mu.Lock()
	matches, items := fmt.Sprintf("%q", rec.matches), rec.items
	rec.mu.Unlock()
	if matches != `["a@x"]` || items != nil {
		t.Errorf("kept matches %s and items %v", matches, items)
	}
	if e.eng.closeCount() != 1 {
		t.Errorf("Close called %d times", e.eng.closeCount())
	}

	if err := e.svc.DeleteSession(ctx, "s-1"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteSession(ctx, "s-1"); !errors.Is(err, psi.ErrUnknownSession) {
		t.Errorf("second delete: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.matches != nil {
		t.Error("matches kept after delete")
	}
}

func TestSessionSenderFlow(t *testing.T) {
	e := newEnv(t, Config{})
	e.start(t, "s-1", orcapi.RoleSender)
	wantState(t, e.orc.next(t), orcapi.StateReady, "")
	if _, opened := e.eng.seen(); opened != "[sender:s-1]" {
		t.Errorf("opened %s", opened)
	}
	if err := e.svc.RunSession(context.Background(), "s-1"); err != nil {
		t.Fatal(err)
	}
	wantState(t, e.orc.next(t), orcapi.StateDone, "")
	rec := e.svc.store.get("s-1")
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.matches != nil {
		t.Errorf("sender kept matches %q", rec.matches)
	}
}

func TestSessionRejected(t *testing.T) {
	stopAtDeadline := func(e *env, own bool) {
		e.cat.load = func(ctx context.Context, _ string) ([][]byte, error) {
			<-ctx.Done()
			if own {
				return nil, errors.New("catalog: read stopped") // does not wrap ctx.Err()
			}
			return nil, ctx.Err()
		}
	}
	cases := []struct {
		name  string
		ttl   time.Duration // only the timeout cases use a short TTL
		setup func(e *env, req *orcapi.StartSessionRequest)
		kind  orcapi.ErrorKind
	}{
		{"unknown dataset", 0, func(e *env, r *orcapi.StartSessionRequest) { r.Dataset = "other" }, orcapi.ErrorInput},
		{"prep refuses", 0, func(e *env, r *orcapi.StartSessionRequest) {
			e.cat.data["customers"] = [][]byte{{0xff}}
			r.Prep = []orcapi.PrepStep{orcapi.PrepNFC}
		}, orcapi.ErrorInput},
		{"check fails", 0, func(e *env, r *orcapi.StartSessionRequest) {
			e.eng.checkErr = &psi.Error{Kind: psi.ErrInput}
		}, orcapi.ErrorInput},
		{"open fails", 0, func(e *env, r *orcapi.StartSessionRequest) { e.eng.openErr = psi.ErrBusy }, orcapi.ErrorInternal},
		{"load times out", 50 * time.Millisecond, func(e *env, r *orcapi.StartSessionRequest) { stopAtDeadline(e, false) }, orcapi.ErrorTimeout},
		{"load times out, own error", 50 * time.Millisecond, func(e *env, r *orcapi.StartSessionRequest) { stopAtDeadline(e, true) }, orcapi.ErrorTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, Config{TTL: tc.ttl})
			req := startReq("s-1", orcapi.RoleReceiver)
			tc.setup(e, req)
			if err := e.svc.StartSession(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			wantState(t, e.orc.next(t), orcapi.StateRejected, tc.kind)
			if err := e.svc.RunSession(context.Background(), "s-1"); !errors.Is(err, ErrInvalidState) {
				t.Errorf("run after rejected: %v", err)
			}
			if err := e.svc.DeleteSession(context.Background(), "s-1"); err != nil {
				t.Errorf("delete after rejected: %v", err)
			}
		})
	}
}

func TestSessionRunFails(t *testing.T) {
	e := newEnv(t, Config{})
	e.eng.run = func(context.Context, [][]byte) ([][]byte, psi.Stats, error) {
		return nil, psi.Stats{}, &psi.Error{Kind: psi.ErrPeerAborted, Code: "ERR_TIMEOUT", Remote: true}
	}
	e.start(t, "s-1", orcapi.RoleReceiver)
	e.orc.next(t)
	if err := e.svc.RunSession(context.Background(), "s-1"); err != nil {
		t.Fatal(err)
	}
	r := e.orc.next(t)
	wantState(t, r, orcapi.StateFailed, orcapi.ErrorPeerAborted)
	if r.rep.Error.Code != "ERR_TIMEOUT" || !r.rep.Error.Remote {
		t.Errorf("error %+v", r.rep.Error)
	}
}

func TestAbortRunning(t *testing.T) {
	e := newEnv(t, Config{})
	started := make(chan struct{})
	e.eng.run = func(ctx context.Context, _ [][]byte) ([][]byte, psi.Stats, error) {
		close(started)
		<-ctx.Done()
		return nil, psi.Stats{}, &psi.Error{Kind: psi.ErrCanceled, Code: "ERR_CANCELLED"}
	}
	e.start(t, "s-1", orcapi.RoleReceiver)
	e.orc.next(t)
	if err := e.svc.RunSession(context.Background(), "s-1"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := e.svc.AbortSession(context.Background(), "s-1", &orcapi.AbortSessionRequest{Reason: orcapi.AbortDeadline}); err != nil {
		t.Fatal(err)
	}
	r := e.orc.next(t)
	wantState(t, r, orcapi.StateFailed, orcapi.ErrorCanceled)
	if r.rep.Error.Code != "ERR_CANCELLED" {
		t.Errorf("code %q", r.rep.Error.Code)
	}
	if err := e.svc.AbortSession(context.Background(), "s-1", &orcapi.AbortSessionRequest{Reason: orcapi.AbortOperator}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("abort after failed: %v", err)
	}
}

func TestAbortReady(t *testing.T) {
	e := newEnv(t, Config{})
	e.start(t, "s-1", orcapi.RoleReceiver)
	e.orc.next(t)
	gate := make(chan struct{})
	e.eng.mu.Lock()
	e.eng.closeGate = gate
	e.eng.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- e.svc.AbortSession(context.Background(), "s-1", &orcapi.AbortSessionRequest{Reason: orcapi.AbortPeerRejected})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("abort waited for Close")
	}
	e.orc.none(t)
	if st := e.state("s-1"); st != stateAborted {
		t.Errorf("state %s", st)
	}
	close(gate)
	for deadline := time.Now().Add(5 * time.Second); e.eng.closeCount() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("session never closed")
		}
	}
	if err := e.svc.RunSession(context.Background(), "s-1"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("run after abort: %v", err)
	}
	if err := e.svc.DeleteSession(context.Background(), "s-1"); err != nil {
		t.Errorf("delete after abort: %v", err)
	}
}

func TestAbortWhileLoading(t *testing.T) {
	e := newEnv(t, Config{})
	release := make(chan struct{})
	inCheck := make(chan struct{})
	e.eng.checkGate, e.eng.checkIn = release, inCheck // Check ignores ctx: the loading goes on after the abort
	e.start(t, "s-1", orcapi.RoleReceiver)
	<-inCheck
	if err := e.svc.RunSession(context.Background(), "s-1"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("run while starting: %v", err)
	}
	if err := e.svc.DeleteSession(context.Background(), "s-1"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("delete while starting: %v", err)
	}
	if err := e.svc.AbortSession(context.Background(), "s-1", &orcapi.AbortSessionRequest{Reason: orcapi.AbortPeerRejected}); err != nil {
		t.Fatal(err)
	}
	close(release)
	// The late loading opens the session, sees the abort, closes it and reports nothing.
	for deadline := time.Now().Add(5 * time.Second); e.eng.closeCount() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("late session never closed")
		}
	}
	e.orc.none(t)
	if st := e.state("s-1"); st != stateAborted {
		t.Errorf("state %s", st)
	}
}

func TestORCCallsBackDuringReport(t *testing.T) {
	e := newEnv(t, Config{})
	errs := make(chan error, 2)
	e.orc.set(http.StatusAccepted, func(r report) {
		switch r.rep.State {
		case orcapi.StateReady:
			errs <- e.svc.RunSession(context.Background(), r.sessionID)
		case orcapi.StateDone:
			errs <- e.svc.DeleteSession(context.Background(), r.sessionID)
		}
	})
	e.start(t, "s-1", orcapi.RoleReceiver)
	wantState(t, e.orc.next(t), orcapi.StateReady, "")
	wantState(t, e.orc.next(t), orcapi.StateDone, "")
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Errorf("call back %d: %v", i, err)
		}
	}
}

func TestRequestErrors(t *testing.T) {
	e := newEnv(t, Config{MaxSessions: 1})
	ctx := context.Background()
	for name, err := range map[string]error{
		"run":    e.svc.RunSession(ctx, "nope"),
		"abort":  e.svc.AbortSession(ctx, "nope", &orcapi.AbortSessionRequest{Reason: orcapi.AbortOperator}),
		"delete": e.svc.DeleteSession(ctx, "nope"),
	} {
		if !errors.Is(err, psi.ErrUnknownSession) {
			t.Errorf("%s unknown session: %v", name, err)
		}
	}
	misrouted := startReq("s-1", orcapi.RoleReceiver)
	misrouted.CCID = "cc-9"
	if err := e.svc.StartSession(ctx, misrouted); !errors.Is(err, ErrMisrouted) {
		t.Errorf("misrouted start: %v", err)
	}
	invalid := startReq("s-1", orcapi.RoleReceiver)
	invalid.Prep = nil
	if err := e.svc.StartSession(ctx, invalid); !errors.Is(err, psi.ErrMalformed) {
		t.Errorf("invalid start: %v", err)
	}
	e.start(t, "s-1", orcapi.RoleReceiver)
	if err := e.svc.StartSession(ctx, startReq("s-1", orcapi.RoleReceiver)); !errors.Is(err, psi.ErrSessionExists) {
		t.Errorf("duplicate: %v", err)
	}
	if err := e.svc.StartSession(ctx, startReq("s-2", orcapi.RoleReceiver)); !errors.Is(err, psi.ErrBusy) {
		t.Errorf("full: %v", err)
	}
	e.orc.next(t)
	if err := e.svc.DeleteSession(ctx, "s-1"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("delete while ready: %v", err)
	}
}

func TestDeliver(t *testing.T) {
	e := newEnv(t, Config{})
	env := &orcapi.Envelope{SessionID: "s-1", SenderCCID: "cc-2", ReceiverCCID: "cc-1", Seq: 2, Kind: "hello",
		Payload: []byte(`{"a":1}`)}
	e.eng.deliverErr = psi.ErrUnknownSession
	if err := e.svc.Deliver(context.Background(), env); !errors.Is(err, psi.ErrUnknownSession) {
		t.Errorf("engine error not returned: %v", err)
	}
	m := e.eng.delivered[0]
	if m.SessionID != "s-1" || m.Seq != 2 || m.Kind != "hello" || string(m.Payload) != `{"a":1}` {
		t.Errorf("delivered %+v", m)
	}
	env.ReceiverCCID = "cc-9"
	if err := e.svc.Deliver(context.Background(), env); !errors.Is(err, ErrMisrouted) {
		t.Errorf("misrouted: %v", err)
	}
	if len(e.eng.delivered) != 1 {
		t.Error("a misrouted message reached the engine")
	}
}

func TestTTL(t *testing.T) {
	t.Run("ready expires", func(t *testing.T) {
		e := newEnv(t, Config{})
		e.start(t, "s-1", orcapi.RoleReceiver)
		e.orc.next(t)
		e.timers.fireLast()
		if st := e.state("s-1"); st != stateFailed {
			t.Fatalf("state %s, want failed", st)
		}
		if e.eng.closeCount() != 1 {
			t.Errorf("Close called %d times", e.eng.closeCount())
		}
		e.orc.none(t)
		if err := e.svc.RunSession(context.Background(), "s-1"); !errors.Is(err, ErrInvalidState) {
			t.Errorf("run after expiry: %v", err)
		}
		if err := e.svc.DeleteSession(context.Background(), "s-1"); err != nil {
			t.Errorf("delete after expiry: %v", err)
		}
	})
	t.Run("ended expires", func(t *testing.T) {
		e := newEnv(t, Config{})
		e.start(t, "s-1", orcapi.RoleReceiver)
		e.orc.next(t)
		if err := e.svc.RunSession(context.Background(), "s-1"); err != nil {
			t.Fatal(err)
		}
		e.orc.next(t)
		e.timers.fireLast()
		if err := e.svc.DeleteSession(context.Background(), "s-1"); !errors.Is(err, psi.ErrUnknownSession) {
			t.Errorf("delete after expiry: %v", err)
		}
	})
	t.Run("stale timer", func(t *testing.T) {
		e := newEnv(t, Config{})
		e.start(t, "s-1", orcapi.RoleReceiver)
		e.orc.next(t)
		e.timers.mu.Lock()
		readyTimer := e.timers.timers[len(e.timers.timers)-1]
		e.timers.mu.Unlock()
		if err := e.svc.RunSession(context.Background(), "s-1"); err != nil {
			t.Fatal(err)
		}
		e.orc.next(t)
		if !readyTimer.stopped {
			t.Error("the ready timer was not stopped")
		}
		readyTimer.f() // fires late: must not touch the done session
		if st := e.state("s-1"); st != stateDone {
			t.Errorf("state %s, want done", st)
		}
	})
}

func TestReportFailureIsLogged(t *testing.T) {
	e := newEnv(t, Config{})
	e.orc.set(http.StatusInternalServerError, nil)
	e.start(t, "s-1", orcapi.RoleReceiver)
	e.orc.next(t)
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(e.logText(), "status report failed"); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("failed report not logged")
		}
	}
}

func (e *env) logText() string { return e.logs.String() }

func TestNoValuesLeak(t *testing.T) {
	const marker = "secretmarker" // lower case: prep (trim, lower) keeps it as it is
	e := newEnv(t, Config{})
	e.orc.set(http.StatusInternalServerError, nil) // every report fails and is logged
	e.cat.data["customers"] = values(marker+"-1", marker+"-2")
	e.start(t, "s-1", orcapi.RoleReceiver)
	ready := e.orc.next(t)
	if err := e.svc.RunSession(context.Background(), "s-1"); err != nil {
		t.Fatal(err)
	}
	done := e.orc.next(t) // the fake engine matches the first item
	// A rejected session too.
	e.eng.mu.Lock()
	e.eng.checkErr = &psi.Error{Kind: psi.ErrInput}
	e.eng.mu.Unlock()
	e.start(t, "s-2", orcapi.RoleSender)
	rejected := e.orc.next(t)
	for deadline := time.Now().Add(5 * time.Second); strings.Count(e.logText(), "status report failed") < 3; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the failed reports were not logged: %s", e.logText())
		}
	}
	for _, s := range []string{string(ready.body), string(done.body), string(rejected.body), e.logText()} {
		if strings.Contains(strings.ToLower(s), marker) {
			t.Errorf("a value leaked: %s", s)
		}
	}
}

func TestNew(t *testing.T) {
	good := Config{CCID: "cc-1", ORCURL: "http://orc", Engine: &fakeEngine{}, Catalog: &fakeCatalog{}}
	for name, mut := range map[string]func(*Config){
		"cc id":        func(c *Config) { c.CCID = "a b" },
		"orc url":      func(c *Config) { c.ORCURL = "orc" },
		"engine":       func(c *Config) { c.Engine = nil },
		"catalog":      func(c *Config) { c.Catalog = nil },
		"max sessions": func(c *Config) { c.MaxSessions = -1 },
		"ttl":          func(c *Config) { c.TTL = -time.Second },
	} {
		cfg := good
		mut(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	s, err := New(good)
	if err != nil {
		t.Fatal(err)
	}
	if s.ttl != defaultTTL || s.store.(*memStore).max != defaultMaxSessions || s.orc.http.Timeout != defaultHTTPTimeout {
		t.Errorf("defaults: ttl %v, max %d, timeout %v", s.ttl, s.store.(*memStore).max, s.orc.http.Timeout)
	}
}
