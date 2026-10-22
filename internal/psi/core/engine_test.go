package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/psi"
)

// engineORC posts the messages of one party to the peer's Engine after a JSON round trip, as
// over HTTP. Messages for a session that the peer has already ended are dropped, as an
// orchestrator may do once one side is gone, so that tests see the peer's Abort rather than a
// delivery failure.
type engineORC struct{ to psi.Engine }

func (o engineORC) Post(ctx context.Context, m *psi.Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	var in psi.Message
	if err := json.Unmarshal(body, &in); err != nil {
		return err
	}
	err = o.to.Deliver(ctx, &in)
	if errors.Is(err, psi.ErrUnknownSession) {
		return nil
	}
	return err
}

func newEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// openPair opens session s-1 on both engines, rcv as the receiver and snd as the sender.
func openPair(t *testing.T, rcv, snd *Engine) (psi.ReceiverSession, psi.SenderSession) {
	t.Helper()
	rs, err := rcv.OpenReceiver("s-1", engineORC{snd})
	if err != nil {
		t.Fatal(err)
	}
	ss, err := snd.OpenSender("s-1", engineORC{rcv})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close(); _ = ss.Close() })
	return rs, ss
}

type receiverOut struct {
	res *psi.ReceiverResult
	err error
}

type senderOut struct {
	res *psi.SenderResult
	err error
}

func goReceiver(ctx context.Context, rs psi.ReceiverSession, items [][]byte) <-chan receiverOut {
	ch := make(chan receiverOut, 1)
	go func() {
		res, err := rs.Run(ctx, items)
		ch <- receiverOut{res, err}
	}()
	return ch
}

func goSender(ctx context.Context, ss psi.SenderSession, items [][]byte) <-chan senderOut {
	ch := make(chan senderOut, 1)
	go func() {
		res, err := ss.Run(ctx, items)
		ch <- senderOut{res, err}
	}()
	return ch
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// waitReceived waits until session s-1 of e has accepted n messages.
func waitReceived(t *testing.T, e *Engine, n uint64) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		e.mu.Lock()
		s := e.sessions["s-1"]
		e.mu.Unlock()
		if s != nil && s.conn.stats().Received >= n {
			return
		}
	}
	t.Fatalf("session did not receive %d messages", n)
}

// wantErr checks that err is a *psi.Error of session s-1 with the given kind, code and side.
func wantErr(t *testing.T, what string, err error, kind error, code string, remote bool) {
	t.Helper()
	var pe *psi.Error
	if !errors.As(err, &pe) || !errors.Is(err, kind) || pe.Code != code || pe.Remote != remote || pe.SessionID != "s-1" {
		t.Fatalf("%s: %v, want %v (code %q, remote %v)", what, err, kind, code, remote)
	}
}

func items(ss ...string) [][]byte {
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = []byte(s)
	}
	return out
}

func TestEngineRunsPSI(t *testing.T) {
	ctx := testCtx(t)
	rcv, snd := newEngine(t, Config{}), newEngine(t, Config{})
	rs, ss := openPair(t, rcv, snd)
	sch := goSender(ctx, ss, items("c", "b", "z", "c"))
	res, err := rs.Run(ctx, items("a", "b", "a", "c", "d", "b"))
	if err != nil {
		t.Fatal(err)
	}
	so := <-sch
	if so.err != nil {
		t.Fatal(so.err)
	}
	if got := fmt.Sprintf("%q", res.Matches); got != `["b" "c"]` {
		t.Errorf("matches %s, want [b c] in order of first occurrence", got)
	}
	r, s := res.Stats, so.res.Stats
	if r.Items != 6 || r.DuplicatesRemoved != 2 || s.Items != 4 || s.DuplicatesRemoved != 1 {
		t.Errorf("items and duplicates: receiver %d/%d, sender %d/%d", r.Items, r.DuplicatesRemoved, s.Items, s.DuplicatesRemoved)
	}
	if len(r.ProtocolSID) != 32 || r.ProtocolSID != s.ProtocolSID {
		t.Errorf("protocol SIDs %q and %q", r.ProtocolSID, s.ProtocolSID)
	}
	if r.MessagesSent == 0 || r.MessagesSent != s.MessagesReceived || s.MessagesSent != r.MessagesReceived ||
		r.BytesSent != s.BytesReceived || s.BytesSent != r.BytesReceived {
		t.Errorf("message counts do not match: receiver %+v, sender %+v", r, s)
	}
	if r.Duration <= 0 || len(r.Phases) == 0 || r.Phases[len(r.Phases)-1].Name != "total" {
		t.Errorf("duration %v, phases %v", r.Duration, r.Phases)
	}

	// The session has ended: Run cannot run again, messages are refused, the ID is free.
	_, err = rs.Run(ctx, items("a"))
	wantErr(t, "second Run", err, psi.ErrInternal, "", false)
	err = rcv.Deliver(ctx, &psi.Message{SessionID: "s-1", Seq: 99, Kind: kindDone, Payload: []byte(`{}`)})
	if !errors.Is(err, psi.ErrUnknownSession) {
		t.Errorf("Deliver after the session: %v, want ErrUnknownSession", err)
	}
	again, err := rcv.OpenReceiver("s-1", engineORC{snd})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = again.Close()
}

func TestEngineRunsLargerSets(t *testing.T) {
	ctx := testCtx(t)
	recv, send, want := testSets(500, 400, 120, mrand.New(mrand.NewPCG(3, 3)))
	rs, ss := openPair(t, newEngine(t, Config{}), newEngine(t, Config{}))
	sch := goSender(ctx, ss, send)
	res, err := rs.Run(ctx, recv)
	if err != nil {
		t.Fatal(err)
	}
	if so := <-sch; so.err != nil {
		t.Fatal(so.err)
	}
	if len(res.Matches) != len(want) {
		t.Fatalf("%d matches, want %d", len(res.Matches), len(want))
	}
	for i, j := range want {
		if string(res.Matches[i]) != string(recv[j]) {
			t.Fatalf("match %d differs", i)
		}
	}
}

func TestEngineCheck(t *testing.T) {
	e := newEngine(t, Config{Protocol: protocol.Config{Limits: protocol.Limits{MaxInstanceSize: 4}}})
	if err := e.Check(psi.Role(0), items("a")); !errors.Is(err, psi.ErrInput) {
		t.Errorf("invalid role: %v, want ErrInput", err)
	}
	if err := e.Check(psi.RoleSender, items("a", "b", "c", "d", "e")); !errors.Is(err, psi.ErrInput) {
		t.Errorf("above the limit: %v, want ErrInput", err)
	}
	// Duplicates are dropped before the limit applies.
	if err := e.Check(psi.RoleSender, items("a", "a", "b", "b", "c", "d", "d")); err != nil {
		t.Errorf("four distinct items: %v", err)
	}
	if err := newEngine(t, Config{}).Check(psi.RoleReceiver, items("a", "b")); err != nil {
		t.Errorf("receiver: %v", err)
	}
}

func TestEngineOpen(t *testing.T) {
	ctx := testCtx(t)
	e := newEngine(t, Config{MaxSessions: 2})
	for _, id := range []string{"", "a b", strings.Repeat("a", psi.MaxSessionIDLen+1)} {
		if _, err := e.OpenReceiver(id, &recordOutbox{}); !errors.Is(err, psi.ErrMalformed) {
			t.Errorf("open %q: %v, want ErrMalformed", id, err)
		}
	}
	out := &recordOutbox{}
	first, err := e.OpenReceiver("s-1", out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenSender("s-2", &recordOutbox{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenSender("s-1", &recordOutbox{}); !errors.Is(err, psi.ErrSessionExists) {
		t.Errorf("open twice: %v, want ErrSessionExists", err)
	}
	if _, err := e.OpenSender("s-3", &recordOutbox{}); !errors.Is(err, psi.ErrBusy) {
		t.Errorf("open above the limit: %v, want ErrBusy", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenSender("s-3", &recordOutbox{}); err != nil {
		t.Errorf("open after a close: %v", err)
	}
	// Close before Run tells the peer to stop.
	msgs := out.posted()
	if len(msgs) != 1 || msgs[0].Kind != kindAbort {
		t.Fatalf("posted %d messages, want one abort", len(msgs))
	}
	f, err := decodeFrame(msgs[0].Kind, msgs[0].Payload)
	if err != nil || protocol.Code(f.Abort.Code) != protocol.CodeCancelled {
		t.Errorf("abort %+v, %v; want ERR_CANCELLED", f, err)
	}

	for name, m := range map[string]*psi.Message{
		"nil":        nil,
		"invalid ID": {SessionID: "a b", Seq: 1},
	} {
		if err := e.Deliver(ctx, m); !errors.Is(err, psi.ErrMalformed) {
			t.Errorf("Deliver %s: %v, want ErrMalformed", name, err)
		}
	}
	if err := e.Deliver(ctx, &psi.Message{SessionID: "s-9", Seq: 1}); !errors.Is(err, psi.ErrUnknownSession) {
		t.Errorf("Deliver to an unknown session: %v, want ErrUnknownSession", err)
	}
}

func TestEngineRefusedMessageEndsSession(t *testing.T) {
	ctx := testCtx(t)
	e := newEngine(t, Config{})
	out := &recordOutbox{}
	rs, err := e.OpenReceiver("s-1", out)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	bad := &psi.Message{SessionID: "s-1", Seq: 1, Kind: kindHello, Payload: []byte(`{"role":"x","hash":"sha256"}`)}
	if err := e.Deliver(ctx, bad); !errors.Is(err, psi.ErrMalformed) {
		t.Fatalf("Deliver: %v, want ErrMalformed", err)
	}
	_, err = rs.Run(ctx, items("a"))
	wantErr(t, "Run", err, psi.ErrProtocol, "ERR_PROTOCOL", false)
	// The peer is told: the last message is an Abort with ERR_PROTOCOL.
	msgs := out.posted()
	last := msgs[len(msgs)-1]
	f, err := decodeFrame(last.Kind, last.Payload)
	if err != nil || f.Abort == nil || protocol.Code(f.Abort.Code) != protocol.CodeProtocol {
		t.Errorf("last message %s: %+v, %v; want an ERR_PROTOCOL abort", last.Kind, f, err)
	}
	if err := e.Deliver(ctx, frameMsg(t, "s-1", 2, doneFrame())); !errors.Is(err, psi.ErrUnknownSession) {
		t.Errorf("Deliver after Run: %v, want ErrUnknownSession", err)
	}
}

func TestEngineRunFailures(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		ctx := testCtx(t)
		rcv := newEngine(t, Config{})
		snd := newEngine(t, Config{Protocol: protocol.Config{Limits: protocol.Limits{MaxInstanceSize: 4}}})
		rs, ss := openPair(t, rcv, snd)
		rch := goReceiver(ctx, rs, items("a", "b"))
		waitReceived(t, snd, 2) // the receiver's Hello and sizes
		_, err := ss.Run(ctx, items("a", "b", "c", "d", "e"))
		wantErr(t, "sender", err, psi.ErrInput, "ERR_INPUT", false)
		wantErr(t, "receiver", (<-rch).err, psi.ErrPeerAborted, "ERR_INPUT", true)
	})
	t.Run("canceled", func(t *testing.T) {
		ctx := testCtx(t)
		rcv, snd := newEngine(t, Config{}), newEngine(t, Config{})
		rs, ss := openPair(t, rcv, snd)
		rctx, cancel := context.WithCancel(ctx)
		rch := goReceiver(rctx, rs, items("a", "b"))
		waitReceived(t, snd, 2)
		cancel()
		wantErr(t, "receiver", (<-rch).err, psi.ErrCanceled, "ERR_CANCELLED", false)
		_, err := ss.Run(ctx, items("a"))
		wantErr(t, "sender", err, psi.ErrPeerAborted, "ERR_CANCELLED", true)
	})
	t.Run("delivery", func(t *testing.T) {
		rs, err := newEngine(t, Config{}).OpenReceiver("s-1", &recordOutbox{err: errors.New("orchestrator unavailable")})
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		_, err = rs.Run(testCtx(t), items("a"))
		wantErr(t, "receiver", err, psi.ErrDelivery, "", false)
	})
	t.Run("timeout", func(t *testing.T) {
		rcv := newEngine(t, Config{Protocol: protocol.Config{HandshakeTimeout: 200 * time.Millisecond}})
		rs, _ := openPair(t, rcv, newEngine(t, Config{})) // the sender never runs
		_, err := rs.Run(testCtx(t), items("a"))
		wantErr(t, "receiver", err, psi.ErrTimeout, "ERR_TIMEOUT", false)
	})
	t.Run("mismatch", func(t *testing.T) {
		ctx := testCtx(t)
		rcv := newEngine(t, Config{})
		snd := newEngine(t, Config{Protocol: protocol.Config{Lambda: 48}})
		rs, ss := openPair(t, rcv, snd)
		sch := goSender(ctx, ss, items("a"))
		_, rErr := rs.Run(ctx, items("a"))
		sErr := (<-sch).err
		// Each side detects the mismatch itself, unless the peer's Abort arrives before the
		// peer's Hello; at least one side detects it.
		local := 0
		for _, err := range []error{rErr, sErr} {
			var pe *psi.Error
			if !errors.As(err, &pe) || pe.Code != "ERR_PARAM_MISMATCH" ||
				!(errors.Is(err, psi.ErrMismatch) && !pe.Remote || errors.Is(err, psi.ErrPeerAborted) && pe.Remote) {
				t.Fatalf("%v, want ErrMismatch or a peer abort with ERR_PARAM_MISMATCH", err)
			}
			if !pe.Remote {
				local++
			}
		}
		if local == 0 {
			t.Errorf("neither side detected the mismatch: %v; %v", rErr, sErr)
		}
	})
}

func TestEngineClose(t *testing.T) {
	t.Run("before run", func(t *testing.T) {
		ctx := testCtx(t)
		rcv, snd := newEngine(t, Config{}), newEngine(t, Config{})
		rs, ss := openPair(t, rcv, snd)
		if err := rs.Close(); err != nil {
			t.Fatal(err)
		}
		if err := rs.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		_, err := rs.Run(ctx, items("a"))
		wantErr(t, "Run after Close", err, psi.ErrCanceled, "", false)
		_, err = ss.Run(ctx, items("a"))
		wantErr(t, "sender", err, psi.ErrPeerAborted, "ERR_CANCELLED", true)
	})
	t.Run("during run", func(t *testing.T) {
		ctx := testCtx(t)
		rcv, snd := newEngine(t, Config{}), newEngine(t, Config{})
		rs, ss := openPair(t, rcv, snd)
		rch := goReceiver(ctx, rs, items("a", "b"))
		waitReceived(t, snd, 2)
		if err := rs.Close(); err != nil {
			t.Fatal(err)
		}
		// Close returns after Run has ended the session.
		rcv.mu.Lock()
		_, open := rcv.sessions["s-1"]
		rcv.mu.Unlock()
		if open {
			t.Error("session still open after Close")
		}
		select {
		case out := <-rch:
			wantErr(t, "receiver", out.err, psi.ErrCanceled, "ERR_CANCELLED", false)
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after Close")
		}
		_, err := ss.Run(ctx, items("a"))
		wantErr(t, "sender", err, psi.ErrPeerAborted, "ERR_CANCELLED", true)
	})
}

func TestToError(t *testing.T) {
	cases := []struct {
		err    error
		kind   error
		code   string
		remote bool
	}{
		{&protocol.Error{Code: protocol.CodeTimeout, Remote: true}, psi.ErrPeerAborted, "ERR_TIMEOUT", true},
		{&protocol.Error{Code: protocol.CodeInput}, psi.ErrInput, "ERR_INPUT", false},
		{&protocol.Error{Code: protocol.CodeParamMismatch}, psi.ErrMismatch, "ERR_PARAM_MISMATCH", false},
		{&protocol.Error{Code: protocol.CodeProtocol}, psi.ErrProtocol, "ERR_PROTOCOL", false},
		{&protocol.Error{Code: protocol.CodeBadEncoding}, psi.ErrProtocol, "ERR_BAD_ENCODING", false},
		{&protocol.Error{Code: protocol.CodeBadPoint}, psi.ErrProtocol, "ERR_BAD_POINT", false},
		{&protocol.Error{Code: protocol.CodeLimit}, psi.ErrProtocol, "ERR_LIMIT", false},
		{&protocol.Error{Code: protocol.CodeTimeout}, psi.ErrTimeout, "ERR_TIMEOUT", false},
		{&protocol.Error{Code: protocol.CodeCancelled}, psi.ErrCanceled, "ERR_CANCELLED", false},
		{&protocol.Error{Code: protocol.CodeInternal}, psi.ErrInternal, "ERR_INTERNAL", false},
		{&protocol.Error{Code: 99}, psi.ErrInternal, "ERR_UNKNOWN(99)", false},
		{&protocol.TransportError{Op: "send", Err: fmt.Errorf("%w: x", errDelivery)}, psi.ErrDelivery, "", false},
		{&protocol.TransportError{Op: "recv", Err: errRefused}, psi.ErrProtocol, "ERR_PROTOCOL", false},
		{&protocol.TransportError{Op: "recv", Err: errClosed}, psi.ErrCanceled, "", false},
		{&protocol.TransportError{Op: "recv", Err: io.EOF}, psi.ErrInternal, "", false},
		{errors.New("other"), psi.ErrInternal, "", false},
	}
	for _, tc := range cases {
		e := toError("s-1", tc.err)
		if !errors.Is(e, tc.kind) || e.Code != tc.code || e.Remote != tc.remote || e.SessionID != "s-1" || e.Cause != tc.err {
			t.Errorf("%v: got %+v, want %v (code %q, remote %v)", tc.err, e, tc.kind, tc.code, tc.remote)
		}
	}
}

func TestNew(t *testing.T) {
	for name, cfg := range map[string]Config{
		"max frame":    {Protocol: protocol.Config{MaxFrame: maxRelayFrame + 1}},
		"lambda":       {Protocol: protocol.Config{Lambda: 10}},
		"max sessions": {MaxSessions: -1},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	e, err := New(Config{Protocol: protocol.Config{MaxFrame: maxRelayFrame}})
	if err != nil {
		t.Fatal(err)
	}
	if e.maxSessions != defaultMaxSessions {
		t.Errorf("max sessions %d, want %d", e.maxSessions, defaultMaxSessions)
	}
}

func TestEngineDoesNotLeakItems(t *testing.T) {
	const marker = "SECRETMARKER"
	secret := items(marker+"-1", marker+"-2", marker+"-3", marker+"-4", marker+"-5")
	var texts []string

	limited := newEngine(t, Config{Protocol: protocol.Config{Limits: protocol.Limits{MaxInstanceSize: 4}}})
	texts = append(texts, limited.Check(psi.RoleSender, secret).Error())

	ctx := testCtx(t)
	rs, ss := openPair(t, newEngine(t, Config{}), newEngine(t, Config{}))
	sch := goSender(ctx, ss, secret)
	res, err := rs.Run(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	so := <-sch
	if so.err != nil {
		t.Fatal(so.err)
	}
	texts = append(texts, fmt.Sprintf("%+v", res.Stats), fmt.Sprintf("%+v", so.res.Stats))

	rs, ss = openPair(t, limited, newEngine(t, Config{}))
	_ = ss.Close()
	_, err = rs.Run(ctx, secret)
	texts = append(texts, err.Error())

	for _, s := range texts {
		if strings.Contains(s, marker) {
			t.Errorf("leaks an item: %s", s)
		}
	}
}
