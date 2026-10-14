package protocol

import (
	"context"
	"io"
	"sync"
)

// Conn is a bidirectional, ordered frame stream between the two parties. Send and Recv may
// be called concurrently with each other, but not with themselves. Both must return
// promptly once ctx is done. Recv returns io.EOF after the peer closed its side.
type Conn interface {
	Send(ctx context.Context, f *Frame) error
	Recv(ctx context.Context) (*Frame, error)
}

// PipeConn is one end of an in-memory Conn pair (tests, benchmarks, in-process runs).
type PipeConn struct {
	in, out   chan *Frame
	closeOnce sync.Once
	closed    chan struct{} // closed by Close on this end
	peer      *PipeConn
}

// Pipe returns two connected in-memory Conns. buffer frames may be in flight per direction
// before Send blocks (emulating transport back-pressure).
func Pipe(buffer int) (*PipeConn, *PipeConn) {
	ab, ba := make(chan *Frame, buffer), make(chan *Frame, buffer)
	a := &PipeConn{in: ba, out: ab, closed: make(chan struct{})}
	b := &PipeConn{in: ab, out: ba, closed: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

// Send implements Conn.
func (c *PipeConn) Send(ctx context.Context, f *Frame) error {
	select {
	case <-c.closed:
		return io.ErrClosedPipe
	case <-c.peer.closed:
		return io.ErrClosedPipe
	default:
	}
	select {
	case c.out <- f:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	case <-c.peer.closed:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Recv implements Conn. Frames already queued are delivered before io.EOF.
func (c *PipeConn) Recv(ctx context.Context) (*Frame, error) {
	select {
	case f := <-c.in:
		return f, nil
	default:
	}
	select {
	case f := <-c.in:
		return f, nil
	case <-c.peer.closed:
		select {
		case f := <-c.in:
			return f, nil
		default:
			return nil, io.EOF
		}
	case <-c.closed:
		return nil, io.ErrClosedPipe
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes this end; the peer's Recv returns io.EOF once drained.
func (c *PipeConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
