// Package transport carries protocol sessions over gRPC (one bidi stream) with
// mutual TLS 1.3. A Server accepts exactly one session or, with
// Options.Repeat, one session at a time for as long as it runs (daemon); a client
// dials with optional retry and request metadata. Both expose the session as a
// protocol.Conn.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/XSpring/minipsi/internal/gen/minipsiv1"
	"github.com/XSpring/minipsi/internal/protocol"
)

// acceptedHeader is sent by the server when it takes the stream as the session.
const acceptedHeader = "minipsi-session"

// InsecureEnv must be set to "1" for Options.Insecure to be accepted.
const InsecureEnv = "MINIPSI_I_UNDERSTAND_INSECURE"

// ErrInsecureNotAcknowledged is returned when plaintext mode is requested without InsecureEnv.
var ErrInsecureNotAcknowledged = errors.New("transport: plaintext mode requires " + InsecureEnv + "=1")

// Options configure a server or client.
type Options struct {
	TLS          *tls.Config   // mTLS config (ServerConfig / ClientConfig); required unless Insecure
	Insecure     bool          // plaintext h2c, for development only (needs InsecureEnv=1)
	MaxFrame     int           // max frame size in bytes (default 4 MiB); gRPC limit is MaxFrame + 64 KiB
	ConnectRetry time.Duration // client: keep retrying until this long (0: single attempt)
	Logger       *slog.Logger
	// Dialer overrides the client's network dialer (tests use bufconn).
	Dialer func(ctx context.Context, addr string) (net.Conn, error)

	// Metadata (client) is sent with the session request (e.g. dataset, identity).
	Metadata map[string]string
	// Repeat (server) accepts one session at a time instead of exactly one; requests that
	// arrive while a session runs are refused as busy (the client retries).
	Repeat bool
	// Admit (server), if set, vets each request before it is accepted; a *Rejection makes
	// the client fail without retrying, any other error is reported as unavailable.
	Admit func(Request) error
}

// Request describes an incoming session request for Options.Admit.
type Request struct {
	Metadata map[string]string // request metadata (lower-case keys, first value)
	PeerSANs []string          // DNS and URI SANs of the verified client certificate
	Peer     string            // peer description for logs
}

// Rejection refuses a session request; Msg is shown to the requester.
type Rejection struct{ Msg string }

func (r *Rejection) Error() string { return "request rejected: " + r.Msg }

// ErrRejected is returned (wrapped) by Dial when the server refused the request.
var ErrRejected = errors.New("transport: request rejected by peer")

// ErrBusy is returned (wrapped) by Dial when the server stayed busy until the retry
// window ended.
var ErrBusy = errors.New("transport: peer busy with another session")

const busyMsg = "minipsi: busy with another session"

func (o *Options) normalize() error {
	if o.MaxFrame == 0 {
		o.MaxFrame = 4 << 20
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Insecure {
		if os.Getenv(InsecureEnv) != "1" {
			return ErrInsecureNotAcknowledged
		}
		if o.TLS != nil {
			return errors.New("transport: both TLS and insecure mode configured")
		}
		return nil
	}
	if o.TLS == nil {
		return errors.New("transport: TLS configuration required")
	}
	if o.TLS.MinVersion < tls.VersionTLS13 {
		return errors.New("transport: TLS 1.3 required")
	}
	return nil
}

func (o *Options) creds() credentials.TransportCredentials {
	if o.Insecure {
		return insecure.NewCredentials()
	}
	return credentials.NewTLS(o.TLS)
}

func (o *Options) msgLimit() int { return o.MaxFrame + 64<<10 }

// --- stream adapter ----------------------------------------------------------------------

type grpcStream interface {
	Send(*pb.Frame) error
	Recv() (*pb.Frame, error)
	Context() context.Context
}

type recvResult struct {
	f   *protocol.Frame
	err error
}

type sendReq struct {
	f         *protocol.Frame
	closeSend bool // client: half-close, serialised with sends (gRPC forbids them concurrently)
	err       chan error
}

// streamConn adapts a gRPC stream to protocol.Conn. gRPC Send/Recv do not take a context,
// so a pump goroutine receives ahead (one frame of buffering) and a send loop serialises
// sends; Send/Recv then honour their contexts without tearing down the stream, which keeps
// the stream usable for a final Abort after a cancelled operation.
type streamConn struct {
	st      grpcStream
	recvCh  chan recvResult
	sendCh  chan sendReq
	done    chan struct{}
	once    sync.Once
	recvErr error // sticky, owned by Recv callers
	peer    string
}

func newStreamConn(st grpcStream) *streamConn {
	c := &streamConn{
		st:     st,
		recvCh: make(chan recvResult, 1),
		sendCh: make(chan sendReq),
		done:   make(chan struct{}),
		peer:   peerIdentity(st.Context()),
	}
	go c.pump()
	go c.sendLoop()
	return c
}

func (c *streamConn) pump() {
	for {
		m, err := c.st.Recv()
		var r recvResult
		if err != nil {
			r.err = mapErr(err)
		} else {
			r.f = fromPB(m)
		}
		select {
		case c.recvCh <- r:
		case <-c.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (c *streamConn) sendLoop() {
	for {
		select {
		case req := <-c.sendCh:
			if req.closeSend {
				var err error
				if cs, ok := c.st.(interface{ CloseSend() error }); ok {
					err = cs.CloseSend()
				}
				req.err <- err
				continue
			}
			req.err <- mapErr(c.st.Send(toPB(req.f)))
		case <-c.done:
			return
		}
	}
}

// Send implements protocol.Conn.
func (c *streamConn) Send(ctx context.Context, f *protocol.Frame) error {
	req := sendReq{f: f, err: make(chan error, 1)}
	select {
	case c.sendCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return io.ErrClosedPipe
	}
	select {
	case err := <-req.err:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Recv implements protocol.Conn.
func (c *streamConn) Recv(ctx context.Context) (*protocol.Frame, error) {
	if c.recvErr != nil {
		return nil, c.recvErr
	}
	select {
	case r := <-c.recvCh:
		if r.err != nil {
			c.recvErr = r.err
		}
		return r.f, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, io.ErrClosedPipe
	}
}

// closeSend half-closes a client stream through the send loop, waiting at most timeout
// (a send blocked on flow control is then abandoned; the caller tears the stream down).
func (c *streamConn) closeSend(timeout time.Duration) {
	req := sendReq{closeSend: true, err: make(chan error, 1)}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case c.sendCh <- req:
	case <-t.C:
		return
	case <-c.done:
		return
	}
	select {
	case <-req.err:
	case <-t.C:
	}
}

// PeerIdentity describes the authenticated peer (certificate subject and SANs) for logs.
func (c *streamConn) PeerIdentity() string { return c.peer }

func (c *streamConn) stop() { c.once.Do(func() { close(c.done) }) }

// mapErr turns stream termination into io.EOF and keeps other errors descriptive.
func mapErr(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.Canceled:
			return fmt.Errorf("stream cancelled by peer: %w", io.EOF)
		case codes.ResourceExhausted:
			return fmt.Errorf("frame exceeds size limit: %s", s.Message())
		}
		return fmt.Errorf("%s: %s", s.Code(), s.Message())
	}
	return err
}

// peerSANs returns the DNS and URI SANs of the verified client certificate.
func peerSANs(ctx context.Context) []string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.PeerCertificates) == 0 {
		return nil
	}
	leaf := ti.State.PeerCertificates[0]
	sans := append([]string(nil), leaf.DNSNames...)
	for _, u := range leaf.URIs {
		sans = append(sans, u.String())
	}
	return sans
}

func peerIdentity(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "unknown"
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.PeerCertificates) == 0 {
		return p.Addr.String() + " (unauthenticated)"
	}
	leaf := ti.State.PeerCertificates[0]
	var sans []string
	sans = append(sans, leaf.DNSNames...)
	for _, u := range leaf.URIs {
		sans = append(sans, u.String())
	}
	return fmt.Sprintf("%s CN=%q SAN=[%s]", p.Addr, leaf.Subject.CommonName, strings.Join(sans, ","))
}

// --- server ------------------------------------------------------------------------------

// Server is a gRPC server that hands out exactly one session, or one at a time
// with Options.Repeat. Other session attempts are refused with Unavailable.
type Server struct {
	pb.UnimplementedPSIServer
	gs       *grpc.Server
	lis      net.Listener
	opts     Options
	sessions chan *Session
	taken    atomic.Bool
	closed   chan struct{}
	closeMu  sync.Once
	serveErr chan error
}

// Session is an accepted or dialed session. Close it when the protocol run is over.
type Session struct {
	conn    *streamConn
	release func()
	meta    map[string]string
}

// Metadata returns the request metadata (server side; nil for dialed sessions).
func (s *Session) Metadata() map[string]string { return s.meta }

// Conn returns the session as a protocol.Conn.
func (s *Session) Conn() protocol.Conn { return s.conn }

// PeerIdentity describes the authenticated peer.
func (s *Session) PeerIdentity() string { return s.conn.PeerIdentity() }

// Close ends the session (server: the RPC returns; client: half-close, then tear down).
func (s *Session) Close() error {
	s.release()
	return nil
}

// Listen starts a server on addr ("host:port").
func Listen(addr string, o Options) (*Server, error) {
	if err := o.normalize(); err != nil {
		return nil, err
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: listen %s: %w", addr, err)
	}
	return Serve(lis, o)
}

// Serve starts a server on an existing listener (tests use bufconn).
func Serve(lis net.Listener, o Options) (*Server, error) {
	if err := o.normalize(); err != nil {
		return nil, err
	}
	s := &Server{
		lis: lis, opts: o, sessions: make(chan *Session),
		closed: make(chan struct{}), serveErr: make(chan error, 1),
	}
	s.gs = grpc.NewServer(
		grpc.Creds(o.creds()),
		grpc.MaxRecvMsgSize(o.msgLimit()),
		grpc.MaxSendMsgSize(o.msgLimit()),
		grpc.MaxConcurrentStreams(2),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 15 * time.Second}),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: time.Minute, Timeout: 20 * time.Second}),
	)
	pb.RegisterPSIServer(s.gs, s)
	go func() { s.serveErr <- s.gs.Serve(lis) }()
	o.Logger.Info("listening", "addr", lis.Addr().String(), "tls", !o.Insecure)
	return s, nil
}

// Addr returns the listening address.
func (s *Server) Addr() net.Addr { return s.lis.Addr() }

// Session implements the PSI service: the first stream becomes the session, the RPC
// stays open until the session is closed.
func (s *Server) Session(stream pb.PSI_SessionServer) error {
	ctx := stream.Context()
	meta := map[string]string{}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for k, v := range md {
			if strings.HasPrefix(k, "minipsi-") && len(v) > 0 {
				meta[k] = v[0]
			}
		}
	}
	if s.opts.Admit != nil {
		req := Request{Metadata: meta, PeerSANs: peerSANs(ctx), Peer: peerIdentity(ctx)}
		if err := s.opts.Admit(req); err != nil {
			var rej *Rejection
			if errors.As(err, &rej) {
				s.opts.Logger.Info("request rejected", "peer", req.Peer, "reason", rej.Msg)
				return status.Error(codes.FailedPrecondition, rej.Msg)
			}
			s.opts.Logger.Warn("request not admitted", "peer", req.Peer, "error", err)
			return status.Error(codes.Unavailable, "minipsi: request cannot be served now")
		}
	}
	if !s.taken.CompareAndSwap(false, true) {
		if s.opts.Repeat {
			return status.Error(codes.Unavailable, busyMsg)
		}
		return status.Error(codes.Unavailable, "minipsi: this process serves exactly one session")
	}
	if s.opts.Repeat {
		defer s.taken.Store(false)
	}
	// Headers now, so the client's Dial completes as soon as the stream exists.
	if err := stream.SendHeader(metadata.Pairs(acceptedHeader, "accepted")); err != nil {
		s.taken.Store(false)
		return err
	}
	conn := newStreamConn(stream)
	released := make(chan struct{})
	var once sync.Once
	sess := &Session{conn: conn, release: func() { once.Do(func() { close(released) }) }, meta: meta}
	select {
	case s.sessions <- sess:
	case <-stream.Context().Done():
		// Never handed out (e.g. a probe that disconnected): free the slot again.
		conn.stop()
		s.taken.Store(false)
		return stream.Context().Err()
	case <-s.closed:
		conn.stop()
		return status.Error(codes.Unavailable, "minipsi: server shutting down")
	}
	s.opts.Logger.Info("session accepted", "peer", conn.PeerIdentity())
	select {
	case <-released:
	case <-stream.Context().Done():
	}
	conn.stop()
	return nil
}

// Accept waits for the session.
func (s *Server) Accept(ctx context.Context) (*Session, error) {
	select {
	case sess := <-s.sessions:
		return sess, nil
	case err := <-s.serveErr:
		return nil, fmt.Errorf("transport: server stopped: %w", err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close stops the server, waiting up to 5 s for the session RPC to finish.
func (s *Server) Close() error {
	s.closeMu.Do(func() { close(s.closed) })
	done := make(chan struct{})
	go func() {
		s.gs.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		s.gs.Stop()
	}
	return nil
}

// --- client ------------------------------------------------------------------------------

// Dial connects to addr and opens the session stream. With ConnectRetry > 0 it keeps
// retrying (peer not yet listening, transient failures) until that much time has passed.
func Dial(ctx context.Context, addr string, o Options) (*Session, error) {
	if err := o.normalize(); err != nil {
		return nil, err
	}
	dopts := []grpc.DialOption{
		grpc.WithTransportCredentials(o.creds()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(o.msgLimit()), grpc.MaxCallSendMsgSize(o.msgLimit())),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 20 * time.Second}),
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{
			BaseDelay: 100 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: time.Second,
		}, MinConnectTimeout: 5 * time.Second}),
	}
	if o.Dialer != nil {
		dopts = append(dopts, grpc.WithContextDialer(o.Dialer))
	}
	cc, err := grpc.NewClient(addr, dopts...)
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	client := pb.NewPSIClient(cc)
	deadline := time.Now().Add(o.ConnectRetry)
	for attempt := 1; ; attempt++ {
		callCtx, cancel := context.WithCancel(context.Background())
		if len(o.Metadata) > 0 {
			callCtx = metadata.NewOutgoingContext(callCtx, metadata.New(o.Metadata))
		}
		stop := context.AfterFunc(ctx, cancel) // dial cancellation aborts the attempt
		stream, err := client.Session(callCtx)
		if err == nil {
			// Establish the stream now (surfacing TLS errors) by waiting for the server's
			// headers. A refused session is a trailers-only response without our header;
			// its status is then obtained from Recv.
			var md metadata.MD
			if md, err = stream.Header(); err == nil && len(md.Get(acceptedHeader)) == 0 {
				if _, err = stream.Recv(); err == nil {
					err = errors.New("transport: server did not accept the session")
				}
			}
		}
		stop()
		if err == nil {
			conn := newStreamConn(stream)
			o.Logger.Info("session established", "peer", conn.PeerIdentity(), "attempts", attempt)
			var once sync.Once
			return &Session{conn: conn, release: func() {
				once.Do(func() {
					conn.closeSend(5 * time.Second)
					waitEOF(conn, 5*time.Second)
					conn.stop()
					cancel()
					_ = cc.Close()
				})
			}}, nil
		}
		cancel()
		if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
			_ = cc.Close()
			return nil, fmt.Errorf("%w: %s", ErrRejected, st.Message())
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			_ = cc.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable && st.Message() == busyMsg {
				return nil, fmt.Errorf("transport: connecting to %s: %w", addr, ErrBusy)
			}
			return nil, fmt.Errorf("transport: connecting to %s: %w", addr, mapErr(err))
		}
		o.Logger.Debug("connect attempt failed", "attempt", attempt, "error", mapErr(err))
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
		}
	}
}

// waitEOF drains the stream until the server ends it (or timeout), so that the server has
// observed our half-close before the connection is torn down.
func waitEOF(c *streamConn, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if _, err := c.Recv(ctx); err != nil {
			return
		}
	}
}
