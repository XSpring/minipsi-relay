package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	mrand "math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/psi"
)

// fakeORC is an in-memory orchestrator for one direction of a session. Post hands the message
// to the peer's deliver after a JSON round trip, as the HTTP relay does. It checks that Post
// is never called concurrently and that seq counts up from 1.
type fakeORC struct {
	t     testing.TB
	to    *relayConn
	busy  atomic.Bool
	last  uint64 // last accepted seq; only used while busy
	posts atomic.Int64
}

func (o *fakeORC) Post(ctx context.Context, m *psi.Message) error {
	if !o.busy.CompareAndSwap(false, true) {
		o.t.Error("Post called concurrently")
		return errors.New("concurrent post")
	}
	defer o.busy.Store(false)
	o.posts.Add(1)
	if m.Seq != o.last+1 {
		o.t.Errorf("Post seq %d after %d", m.Seq, o.last)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(body) > psi.MaxMessageBytes {
		o.t.Errorf("message of %d bytes exceeds MaxMessageBytes", len(body))
	}
	var in psi.Message
	if err := json.Unmarshal(body, &in); err != nil {
		return err
	}
	if err := o.to.deliver(ctx, &in); err != nil {
		return err
	}
	o.last = m.Seq
	return nil
}

// recordOutbox keeps the posted messages, or fails every Post with err.
type recordOutbox struct {
	mu   sync.Mutex
	msgs []*psi.Message
	err  error
}

func (r *recordOutbox) Post(_ context.Context, m *psi.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recordOutbox) setErr(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

func (r *recordOutbox) posted() []*psi.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.msgs)
}

// relayPair returns the connections of both parties of one session, linked by fake
// orchestrators.
func relayPair(t testing.TB) (a, b *relayConn, ab, ba *fakeORC) {
	ab, ba = &fakeORC{t: t}, &fakeORC{t: t}
	a, b = newRelayConn("s-1", ab), newRelayConn("s-1", ba)
	ab.to, ba.to = b, a
	t.Cleanup(func() { a.close(); b.close() })
	return a, b, ab, ba
}

// testSets returns receiver and sender items with common shared items, and the ascending
// receiver indices of the shared ones.
func testSets(nr, ns, common int, rng *mrand.Rand) (recv, send [][]byte, want []int) {
	item := func() []byte {
		b := make([]byte, 16)
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		return b
	}
	recv = make([][]byte, nr)
	for i := range recv {
		recv[i] = item()
	}
	send = make([][]byte, ns)
	for i := range send {
		send[i] = item()
	}
	want = rng.Perm(nr)[:common]
	for k, p := range rng.Perm(ns)[:common] {
		send[p] = slices.Clone(recv[want[k]])
	}
	slices.Sort(want)
	return recv, send, want
}

type relayResult struct {
	res        *protocol.Result
	sres       *protocol.SenderResult
	rErr, sErr error
}

// runRelay runs the receiver over a and the sender over b; the sender starts after delay.
func runRelay(cfg protocol.Config, a, b *relayConn, recv, send [][]byte, delay time.Duration) relayResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out relayResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		out.res, out.rErr = protocol.RunReceiver(ctx, cfg, a, []protocol.Instance{{Items: recv}})
	}()
	go func() {
		defer wg.Done()
		time.Sleep(delay)
		out.sres, out.sErr = protocol.RunSender(ctx, cfg, b, []protocol.Instance{{Items: send}})
	}()
	wg.Wait()
	return out
}

// frameMsg returns f as message seq of session id.
func frameMsg(t testing.TB, id string, seq uint64, f *protocol.Frame) *psi.Message {
	t.Helper()
	kind, payload, err := encodeFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	return &psi.Message{SessionID: id, Seq: seq, Kind: kind, Payload: payload}
}

func doneFrame() *protocol.Frame {
	return &protocol.Frame{Done: &protocol.Done{FramesSent: 1, Digest: make([]byte, 32)}}
}

func TestRelayRunsPSI(t *testing.T) {
	cases := []struct {
		name           string
		nr, ns, common int
		maxFrame       int
		delay          time.Duration
		minR, minS     uint64 // messages sent by the receiver and by the sender, at least
	}{
		{name: "small", nr: 50, ns: 40, common: 10, minR: 4, minS: 4},
		{name: "empty", minR: 4, minS: 4},
		// Several poly chunks from the receiver and output chunks from the sender.
		{name: "many frames", nr: 10000, ns: 10000, common: 2500, maxFrame: 64 << 10, minR: 6, minS: 6},
		{name: "late sender", nr: 20, ns: 30, common: 5, delay: 100 * time.Millisecond, minR: 4, minS: 4},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recv, send, want := testSets(tc.nr, tc.ns, tc.common, mrand.New(mrand.NewPCG(uint64(i), 7)))
			a, b, _, _ := relayPair(t)
			out := runRelay(protocol.Config{MaxFrame: tc.maxFrame}, a, b, recv, send, tc.delay)
			if out.rErr != nil || out.sErr != nil {
				t.Fatalf("receiver: %v, sender: %v", out.rErr, out.sErr)
			}
			got := out.res.Instances[0].Matches
			if want == nil {
				want = []int{}
			}
			if got == nil {
				got = []int{}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("matches %v, want %v", got, want)
			}
			sa, sb := a.stats(), b.stats()
			if sa.Sent != sb.Received || sb.Sent != sa.Received ||
				sa.BytesSent != sb.BytesReceived || sb.BytesSent != sa.BytesReceived {
				t.Errorf("stats do not match: receiver %+v, sender %+v", sa, sb)
			}
			// The protocol counts every frame but Done.
			if sa.Sent != out.res.Stats.FramesSent+1 || sb.Sent != out.sres.Stats.FramesSent+1 {
				t.Errorf("messages sent %d and %d, protocol frames %d and %d",
					sa.Sent, sb.Sent, out.res.Stats.FramesSent, out.sres.Stats.FramesSent)
			}
			if sa.Sent < tc.minR || sb.Sent < tc.minS {
				t.Errorf("messages sent %d and %d, want at least %d and %d", sa.Sent, sb.Sent, tc.minR, tc.minS)
			}
		})
	}
}

func TestDeliverRefuses(t *testing.T) {
	const marker = "SECRETMARKER"
	cases := map[string]func(t *testing.T) *psi.Message{
		"nil message":   func(*testing.T) *psi.Message { return nil },
		"other session": func(t *testing.T) *psi.Message { return frameMsg(t, "s-2", 2, doneFrame()) },
		"seq gap":       func(t *testing.T) *psi.Message { return frameMsg(t, "s-1", 3, doneFrame()) },
		"seq repeated":  func(t *testing.T) *psi.Message { return frameMsg(t, "s-1", 1, doneFrame()) },
		"seq zero":      func(t *testing.T) *psi.Message { return frameMsg(t, "s-1", 0, doneFrame()) },
		"unknown kind": func(*testing.T) *psi.Message {
			return &psi.Message{SessionID: "s-1", Seq: 2, Kind: "kind" + marker, Payload: []byte(`{}`)}
		},
		"bad payload": func(*testing.T) *psi.Message {
			return &psi.Message{SessionID: "s-1", Seq: 2, Kind: kindAbort, Payload: []byte(`{"message":1,"` + marker + `":2}`)}
		},
		"too large": func(*testing.T) *psi.Message {
			p := append([]byte(`{"message":"`+marker), bytes.Repeat([]byte("a"), psi.MaxMessageBytes)...)
			return &psi.Message{SessionID: "s-1", Seq: 2, Kind: kindAbort, Payload: append(p, `"}`...)}
		},
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			out := &recordOutbox{}
			c := newRelayConn("s-1", out)
			defer c.close()
			ctx := context.Background()
			if err := c.deliver(ctx, frameMsg(t, "s-1", 1, doneFrame())); err != nil {
				t.Fatal(err)
			}
			err := c.deliver(ctx, msg(t))
			if !errors.Is(err, psi.ErrMalformed) {
				t.Fatalf("deliver: %v, want ErrMalformed", err)
			}
			if strings.Contains(err.Error(), marker) {
				t.Errorf("error leaks the payload: %v", err)
			}
			// The inbound side has ended, even with a frame still queued.
			if _, err := c.Recv(ctx); !errors.Is(err, errRefused) {
				t.Errorf("Recv: %v, want errRefused", err)
			}
			if err := c.deliver(ctx, frameMsg(t, "s-1", 2, doneFrame())); !errors.Is(err, errEnded) {
				t.Errorf("deliver after refusal: %v, want errEnded", err)
			}
			if got := c.stats().Received; got != 1 {
				t.Errorf("received %d, want 1", got)
			}
			// Send still works, so that an Abort can tell the peer.
			if err := c.Send(ctx, &protocol.Frame{Abort: &protocol.Abort{Code: uint32(protocol.CodeProtocol)}}); err != nil {
				t.Errorf("Send after refusal: %v", err)
			}
			if len(out.posted()) != 1 {
				t.Errorf("posted %d messages, want 1", len(out.posted()))
			}
		})
	}
}

func TestDeliverWaitsWhenQueueFull(t *testing.T) {
	c := newRelayConn("s-1", &recordOutbox{})
	defer c.close()
	for seq := uint64(1); seq <= inboundQueue; seq++ {
		if err := c.deliver(context.Background(), frameMsg(t, "s-1", seq, doneFrame())); err != nil {
			t.Fatalf("deliver %d: %v", seq, err)
		}
	}
	next := frameMsg(t, "s-1", inboundQueue+1, doneFrame())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.deliver(ctx, next); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deliver on a full queue: %v, want DeadlineExceeded", err)
	}
	if got := c.stats().Received; got != inboundQueue {
		t.Errorf("received %d, want %d", got, inboundQueue)
	}
	// The same seq is accepted once Recv has made room.
	done := make(chan error, 1)
	go func() { done <- c.deliver(context.Background(), next) }()
	if _, err := c.Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("deliver after Recv: %v", err)
	}
	if got := c.stats().Received; got != inboundQueue+1 {
		t.Errorf("received %d, want %d", got, inboundQueue+1)
	}
}

func TestRecvWaitsForContext(t *testing.T) {
	c := newRelayConn("s-1", &recordOutbox{})
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv: %v, want DeadlineExceeded", err)
	}
}

func TestCloseReleasesWaiters(t *testing.T) {
	out := &recordOutbox{}
	full := newRelayConn("s-1", out)
	for seq := uint64(1); seq <= inboundQueue; seq++ {
		if err := full.deliver(context.Background(), frameMsg(t, "s-1", seq, doneFrame())); err != nil {
			t.Fatal(err)
		}
	}
	empty := newRelayConn("s-1", out)
	delivered, received := make(chan error, 1), make(chan error, 1)
	go func() {
		delivered <- full.deliver(context.Background(), frameMsg(t, "s-1", inboundQueue+1, doneFrame()))
	}()
	go func() {
		_, err := empty.Recv(context.Background())
		received <- err
	}()
	time.Sleep(20 * time.Millisecond) // let both block
	full.close()
	empty.close()
	if err := <-delivered; !errors.Is(err, errEnded) {
		t.Errorf("waiting deliver: %v, want errEnded", err)
	}
	if err := <-received; !errors.Is(err, errClosed) {
		t.Errorf("waiting Recv: %v, want errClosed", err)
	}
	full.close() // idempotent
	if _, err := full.Recv(context.Background()); !errors.Is(err, errClosed) {
		t.Errorf("Recv after close: %v, want errClosed", err)
	}
	if err := full.Send(context.Background(), doneFrame()); !errors.Is(err, errClosed) {
		t.Errorf("Send after close: %v, want errClosed", err)
	}
	if n := len(out.posted()); n != 0 {
		t.Errorf("posted %d messages after close", n)
	}
}

func TestSendPostFailure(t *testing.T) {
	out := &recordOutbox{err: errors.New("orchestrator unavailable")}
	c := newRelayConn("s-1", out)
	defer c.close()
	err := c.Send(context.Background(), doneFrame())
	if !errors.Is(err, errDelivery) {
		t.Fatalf("Send: %v, want errDelivery", err)
	}
	if got := c.stats().Sent; got != 0 {
		t.Errorf("sent %d, want 0", got)
	}
	// A failed Post does not use up a seq.
	out.setErr(nil)
	if err := c.Send(context.Background(), doneFrame()); err != nil {
		t.Fatal(err)
	}
	if msgs := out.posted(); len(msgs) != 1 || msgs[0].Seq != 1 || msgs[0].SessionID != "s-1" || msgs[0].Kind != kindDone {
		t.Errorf("posted %+v, want one done message with seq 1", msgs)
	}
}

func TestRunEndsWhenPostFails(t *testing.T) {
	c := newRelayConn("s-1", &recordOutbox{err: errors.New("orchestrator unavailable")})
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := protocol.RunReceiver(ctx, protocol.Config{}, c, []protocol.Instance{{Items: [][]byte{[]byte("a")}}})
	var te *protocol.TransportError
	if !errors.As(err, &te) || !errors.Is(err, errDelivery) {
		t.Fatalf("RunReceiver: %v, want a TransportError wrapping errDelivery", err)
	}
}

func TestSendSizeLimit(t *testing.T) {
	const maxFrame = 4 << 20 // protocol.Config default
	big := make([]byte, maxFrame)
	frames := map[string]*protocol.Frame{
		kindPolyChunk:   {Poly: &protocol.PolyChunk{Parts: []protocol.PolyPart{{Coeffs: big}}}},
		kindOutputChunk: {Output: &protocol.OutputChunk{Parts: []protocol.OutputPart{{Values: big}}}},
		kindSenderKeys:  {Keys: &protocol.SenderKeys{Keys: big}},
	}
	for kind, f := range frames {
		out := &recordOutbox{}
		c := newRelayConn(strings.Repeat("s", psi.MaxSessionIDLen), out)
		if err := c.Send(context.Background(), f); err != nil {
			t.Fatalf("%s of %d bytes: %v", kind, maxFrame, err)
		}
		body, err := json.Marshal(out.posted()[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > psi.MaxMessageBytes {
			t.Errorf("%s: message of %d bytes exceeds MaxMessageBytes", kind, len(body))
		}
		c.close()
	}

	out := &recordOutbox{}
	c := newRelayConn("s-1", out)
	defer c.close()
	tooBig := &protocol.Frame{Poly: &protocol.PolyChunk{Parts: []protocol.PolyPart{{Coeffs: make([]byte, 7<<20)}}}}
	err := c.Send(context.Background(), tooBig)
	if err == nil || errors.Is(err, errDelivery) {
		t.Fatalf("Send of a too large frame: %v, want a size error", err)
	}
	if len(out.posted()) != 0 {
		t.Error("a too large frame was posted")
	}
}

func TestAbortReachesPeer(t *testing.T) {
	a, b, _, _ := relayPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := protocol.AbortConn(ctx, a, protocol.CodeInput, "input rejected"); err != nil {
		t.Fatal(err)
	}
	_, err := protocol.RunSender(ctx, protocol.Config{}, b, []protocol.Instance{{Items: [][]byte{[]byte("a")}}})
	var pe *protocol.Error
	if !errors.As(err, &pe) || !pe.Remote || pe.Code != protocol.CodeInput {
		t.Fatalf("RunSender: %v, want a remote ERR_INPUT abort", err)
	}
}
