package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/psi"
)

// inboundQueue is the number of peer frames a session holds before deliver waits. A session
// has one instance, so before it reads, the peer sends only Hello and one SizesChunk; the
// queue keeps them until Run starts.
const inboundQueue = 8

// messageOverhead bounds the JSON bytes of a Message besides its session ID, kind and
// payload: the field names, quotes, separators and up to 20 digits of seq.
const messageOverhead = 128

var (
	// errDelivery marks a failed Outbox.Post.
	errDelivery = errors.New("core: message delivery failed")
	// errRefused ends a session after deliver refused a peer message.
	errRefused = errors.New("core: peer message refused")
	// errClosed ends a session that was closed.
	errClosed = errors.New("core: session closed")
	// errEnded is what deliver returns once the session has ended.
	errEnded = errors.New("core: session ended")
)

// relayConn is the protocol.Conn of one session in relay mode. Send encodes a frame, numbers
// it with the next seq and posts it through the session's Outbox. deliver checks a message
// that the orchestrator pushed (session ID, seq, size, payload) and queues its frame for Recv.
//
// Relay mode has no end of stream, so Recv never returns io.EOF; the protocol ends with Done
// and does not read after it. A message that deliver refuses ends the inbound side: Recv
// fails from then on, but Send keeps working until close, so that an Abort can still be sent.
type relayConn struct {
	id  string
	out psi.Outbox

	sendMu  sync.Mutex // serializes Post, so messages leave in seq order
	nextOut uint64     // seq of the next outbound message; guarded by sendMu

	deliverMu sync.Mutex // serializes deliver, so frames are queued in seq order
	nextIn    uint64     // seq of the next inbound message; guarded by deliverMu
	in        chan *protocol.Frame

	endOnce   sync.Once
	err       error         // why the inbound side ended; set once, before ended is closed
	ended     chan struct{} // closed when the inbound side ends
	closeOnce sync.Once
	closed    chan struct{} // closed by close

	sent, recv           atomic.Uint64
	bytesSent, bytesRecv atomic.Int64
}

var _ protocol.Conn = (*relayConn)(nil)

// newRelayConn returns the connection of session id, whose messages go out through out.
func newRelayConn(id string, out psi.Outbox) *relayConn {
	return &relayConn{
		id: id, out: out, nextOut: 1, nextIn: 1,
		in:     make(chan *protocol.Frame, inboundQueue),
		ended:  make(chan struct{}),
		closed: make(chan struct{}),
	}
}

// Send implements protocol.Conn. It posts f as the next message of the session and returns
// after the orchestrator accepted it. A failed Post returns an error that wraps errDelivery
// and leaves seq unchanged.
func (c *relayConn) Send(ctx context.Context, f *protocol.Frame) error {
	kind, payload, err := encodeFrame(f)
	if err != nil {
		return err
	}
	if len(c.id)+len(kind)+len(payload)+messageOverhead > psi.MaxMessageBytes {
		return fmt.Errorf("core: %s message of %d payload bytes exceeds the size limit", kind, len(payload))
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	select {
	case <-c.closed:
		return errClosed
	default:
	}
	m := &psi.Message{SessionID: c.id, Seq: c.nextOut, Kind: kind, Payload: payload}
	if err := c.out.Post(ctx, m); err != nil {
		return fmt.Errorf("%w: %s message %d: %w", errDelivery, kind, m.Seq, err)
	}
	c.nextOut++
	c.sent.Add(1)
	c.bytesSent.Add(int64(len(payload)))
	return nil
}

// Recv implements protocol.Conn. It returns the queued peer frames in seq order. Once the
// inbound side has ended, it returns errRefused or errClosed, even if frames are queued.
func (c *relayConn) Recv(ctx context.Context) (*protocol.Frame, error) {
	select {
	case <-c.ended:
		return nil, c.err
	default:
	}
	select {
	case f := <-c.in:
		return f, nil
	case <-c.ended:
		return nil, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// deliver checks m and queues its frame for Recv. It returns nil once the frame is queued,
// waiting while the queue is full. Otherwise it returns:
//   - an error that wraps psi.ErrMalformed for a message with another session ID, an
//     unexpected seq, a payload above psi.MaxMessageBytes or a payload that does not decode;
//     this also ends the inbound side;
//   - errEnded once the inbound side has ended;
//   - ctx.Err() if ctx is done before the frame is queued; seq is then unchanged, so the
//     message is not counted as received.
//
// Errors never contain the payload.
func (c *relayConn) deliver(ctx context.Context, m *psi.Message) error {
	c.deliverMu.Lock()
	defer c.deliverMu.Unlock()
	select {
	case <-c.ended:
		return errEnded
	default:
	}
	f, err := c.check(m)
	if err != nil {
		c.end(errRefused)
		return err
	}
	select {
	case c.in <- f:
	case <-c.ended:
		return errEnded
	case <-ctx.Done():
		return ctx.Err()
	}
	c.nextIn++
	c.recv.Add(1)
	c.bytesRecv.Add(int64(len(m.Payload)))
	return nil
}

// check validates m against the session and decodes its frame. The caller holds deliverMu.
func (c *relayConn) check(m *psi.Message) (*protocol.Frame, error) {
	switch {
	case m == nil:
		return nil, fmt.Errorf("%w: nil message", psi.ErrMalformed)
	case m.SessionID != c.id:
		return nil, fmt.Errorf("%w: message for another session", psi.ErrMalformed)
	case m.Seq != c.nextIn:
		return nil, fmt.Errorf("%w: seq %d, want %d", psi.ErrMalformed, m.Seq, c.nextIn)
	case len(m.Payload) > psi.MaxMessageBytes:
		return nil, fmt.Errorf("%w: payload of %d bytes exceeds the size limit", psi.ErrMalformed, len(m.Payload))
	}
	f, err := decodeFrame(m.Kind, m.Payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", psi.ErrMalformed, err)
	}
	return f, nil
}

// end ends the inbound side with err, unless it has already ended.
func (c *relayConn) end(err error) {
	c.endOnce.Do(func() {
		c.err = err
		close(c.ended)
	})
}

// close ends the session: Send, Recv and deliver fail from then on, and calls that wait
// return. It is idempotent.
func (c *relayConn) close() {
	c.closeOnce.Do(func() { close(c.closed) })
	c.end(errClosed)
}

// connStats counts the messages that the orchestrator accepted (sent) and that deliver
// accepted (received), and their payload bytes.
type connStats struct {
	Sent, Received           uint64
	BytesSent, BytesReceived int64
}

func (c *relayConn) stats() connStats {
	return connStats{
		Sent: c.sent.Load(), Received: c.recv.Load(),
		BytesSent: c.bytesSent.Load(), BytesReceived: c.bytesRecv.Load(),
	}
}
