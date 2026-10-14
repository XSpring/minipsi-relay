package protocol

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/XSpring/minipsi/internal/hashing"
	"github.com/XSpring/minipsi/internal/poly"
	"github.com/XSpring/minipsi/internal/version"
)

// Instance is one PSI instance: an ID (unique, sorted ascending across the instance list)
// and the party's items. Standalone mode is a single instance with ID 0.
type Instance struct {
	ID    uint32
	Items [][]byte
}

// Limits bound what a peer may ask this party to process.
type Limits struct {
	MaxInstanceSize int // items per instance, either side
	MaxTotalItems   int // items over all instances, per side
	MaxInstances    int
}

// Config holds per-party settings. The zero value is valid: Defaults() fills it in.
type Config struct {
	Hash       hashing.Alg // comparison hash (must match the peer); default SHA-256
	Lambda     int         // statistical security parameter (must match); default 40, ≥ 40
	Workers    int         // parallelism; <= 0 means GOMAXPROCS
	NTTBackend string      // "native" (default) or "lattigo"; local choice
	// PolyCacheBytes is the per-tree transform cache budget: 0 = default,
	// negative = disabled.
	PolyCacheBytes   int64
	MaxFrame         int           // max encoded frame size in bytes; default 4 MiB
	Limits           Limits        // defaults filled in by Defaults()
	HandshakeTimeout time.Duration // wait for the peer's Hello and sizes; default 30 s
	PhaseTimeout     time.Duration // max wait for any single peer frame; default 10 min
	WaveItems        int           // batched-mode pipelining granularity; default 2^14 items
	// Padding is the receiver's padding policy; nil = DefaultPadPolicy.
	// Receiver-local: the sender only sees the resulting point counts.
	Padding *PadPolicy
	// SenderPadding is the sender's output padding policy; nil = NoSenderPadding.
	// Sender-local: the receiver only sees the resulting output counts.
	SenderPadding *PadPolicy
	Rand          io.Reader    // randomness; nil = crypto/rand (tests may inject)
	Logger        *slog.Logger // nil = discard; never receives item data or secrets
}

// Defaults returns c with zero fields replaced by their defaults.
func (c Config) Defaults() Config {
	if c.Hash == 0 {
		c.Hash = hashing.SHA256
	}
	if c.Lambda == 0 {
		c.Lambda = 40
	}
	if c.NTTBackend == "" {
		c.NTTBackend = "native"
	}
	if c.MaxFrame == 0 {
		c.MaxFrame = 4 << 20
	}
	if c.Limits.MaxInstanceSize == 0 {
		c.Limits.MaxInstanceSize = 1 << 22
	}
	if c.Limits.MaxTotalItems == 0 {
		c.Limits.MaxTotalItems = 1 << 24
	}
	if c.Limits.MaxInstances == 0 {
		c.Limits.MaxInstances = 1 << 24
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = 30 * time.Second
	}
	if c.PhaseTimeout == 0 {
		c.PhaseTimeout = 10 * time.Minute
	}
	if c.WaveItems == 0 {
		c.WaveItems = 1 << 14
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.Padding == nil {
		pp := DefaultPadPolicy
		c.Padding = &pp
	}
	if c.SenderPadding == nil {
		pp := NoSenderPadding
		c.SenderPadding = &pp
	}
	return c
}

func (c *Config) validate() error {
	if _, err := hashing.NewComparer(c.Hash, [hashing.SIDSize]byte{}, 1); err != nil {
		return fmt.Errorf("protocol: hash: %w", err)
	}
	if c.Lambda < 40 || c.Lambda > 256 {
		return fmt.Errorf("protocol: lambda %d out of range [40, 256]", c.Lambda)
	}
	if c.MaxFrame < 64<<10 {
		return fmt.Errorf("protocol: max frame %d below 64 KiB", c.MaxFrame)
	}
	if err := c.Padding.validate(); err != nil {
		return err
	}
	return c.SenderPadding.validate()
}

// Stats summarises a run (for reports; contains no secrets).
type Stats struct {
	Phases         []Phase
	FramesSent     uint64
	FramesReceived uint64
	BytesSent      int64 // approximate payload bytes
	BytesReceived  int64
}

// Phase is a named wall-clock interval.
type Phase struct {
	Name     string
	Duration time.Duration
}

// params are the negotiated session parameters.
type params struct {
	ids    []uint32 // instance IDs, ascending
	n1, n2 []int    // sender / receiver set sizes per instance
	sid    [hashing.SIDSize]byte
	outLen int
	cmp    hashing.Comparer
}

// transcript accumulates the digest of frames in one direction (Done/Abort excluded).
type transcript struct {
	h      hash.Hash
	frames uint64
	bytes  int64
}

// session is the role-independent machinery: writer goroutine, receive helper,
// handshake, transcript digests, Done exchange and abort handling.
type session struct {
	cfg   Config
	role  Role
	conn  Conn
	log   *slog.Logger
	rnd   io.Reader
	arith *poly.Arith
	out   chan *Frame

	mu        sync.Mutex // guards stats.Phases
	stats     Stats
	sent, rcv transcript
	start     time.Time
}

func newSession(cfg Config, role Role, conn Conn) (*session, error) {
	cfg = cfg.Defaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	ar, err := poly.NewArith(cfg.NTTBackend, cfg.Workers)
	if err != nil {
		return nil, err
	}
	switch {
	case cfg.PolyCacheBytes < 0:
		ar.CacheBytes = 0
	case cfg.PolyCacheBytes > 0:
		ar.CacheBytes = cfg.PolyCacheBytes
	}
	rnd := cfg.Rand
	if rnd == nil {
		rnd = rand.Reader
	} else {
		rnd = &lockedReader{r: rnd}
	}
	return &session{
		cfg: cfg, role: role, conn: conn, rnd: rnd, arith: ar,
		log:  cfg.Logger.With("role", role.String()),
		out:  make(chan *Frame, 8),
		sent: transcript{h: sha256.New()}, rcv: transcript{h: sha256.New()},
		start: time.Now(),
	}, nil
}

// lockedReader makes an injected io.Reader safe for the concurrent encoders.
type lockedReader struct {
	mu sync.Mutex
	r  io.Reader
}

func (l *lockedReader) Read(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.Read(p)
}

// phase records the duration of a named step since start.
func (s *session) phase(name string, start time.Time) {
	d := time.Since(start)
	s.mu.Lock()
	s.stats.Phases = append(s.stats.Phases, Phase{name, d})
	s.mu.Unlock()
	s.log.Debug("phase done", "phase", name, "duration", d)
}

// run executes body (role logic) together with the writer goroutine, and turns failures
// into Abort frames and *Error values.
func (s *session) run(parent context.Context, body func(ctx context.Context, g *taskGroup) error) error {
	g := newGroup(parent)
	writerDone := make(chan struct{})
	g.Go(func(ctx context.Context) error {
		defer close(writerDone)
		return s.writer(ctx)
	})
	g.Go(func(ctx context.Context) error {
		err := body(ctx, g)
		if err == nil {
			close(s.out) // writer flushes everything (incl. Done) and exits
		}
		return err
	})
	err := g.Wait()
	s.stats.FramesSent, s.stats.BytesSent = s.sent.frames, s.sent.bytes
	s.stats.FramesReceived, s.stats.BytesReceived = s.rcv.frames, s.rcv.bytes
	s.phase("total", s.start)
	if err == nil {
		return nil
	}
	err = s.classify(parent, err)
	if ab := abortFor(err); ab != nil {
		// The writer has exited; send the Abort directly (best effort, bounded wait).
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		_ = s.conn.Send(ctx, &Frame{Abort: ab})
		cancel()
	}
	s.log.Warn("session failed", "error", err)
	return err
}

// classify maps context errors to protocol errors.
func (s *session) classify(parent context.Context, err error) error {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pe *Error
	if errors.As(err, &pe) {
		return err
	}
	if errors.Is(parent.Err(), context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Msg: "session deadline exceeded", Err: err}
	}
	return &Error{Code: CodeCancelled, Msg: "cancelled", Err: err}
}

func (s *session) writer(ctx context.Context) error {
	for {
		select {
		case f, ok := <-s.out:
			if !ok {
				return nil
			}
			if f.Done != nil {
				f = &Frame{Done: &Done{FramesSent: s.sent.frames, Digest: s.sent.h.Sum(nil)}}
			} else {
				digestFrame(s.sent.h, f)
				s.sent.frames++
			}
			s.sent.bytes += int64(payloadSize(f))
			if err := s.conn.Send(ctx, f); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return &TransportError{Op: "send", Err: err}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// send queues f for the writer. Done frames are completed (count, digest) by the writer.
func (s *session) send(ctx context.Context, f *Frame) error {
	select {
	case s.out <- f:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// recv returns the next frame, failing on timeout, transport errors and peer aborts.
func (s *session) recv(ctx context.Context, timeout time.Duration) (*Frame, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	f, err := s.conn.Recv(rctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if rctx.Err() != nil {
			return nil, errorf(CodeTimeout, "no frame from peer within %v", timeout)
		}
		return nil, &TransportError{Op: "recv", Err: err}
	}
	if f == nil {
		return nil, errorf(CodeProtocol, "empty frame")
	}
	if f.Abort != nil {
		return nil, &Error{Code: Code(f.Abort.Code), Msg: f.Abort.Message, Remote: true}
	}
	s.rcv.bytes += int64(payloadSize(f))
	if f.Done == nil {
		digestFrame(s.rcv.h, f)
		s.rcv.frames++
	}
	return f, nil
}

// checkPeerDone verifies the peer's Done against what was received.
func (s *session) checkPeerDone(d *Done) error {
	if d.FramesSent != s.rcv.frames || !bytes.Equal(d.Digest, s.rcv.h.Sum(nil)) {
		return errorf(CodeProtocol, "transcript mismatch: peer sent %d frames, received %d", d.FramesSent, s.rcv.frames)
	}
	return nil
}

// frameKind names the frame type (for error messages).
func frameKind(f *Frame) string {
	switch {
	case f.Hello != nil:
		return "Hello"
	case f.Sizes != nil:
		return "SizesChunk"
	case f.Keys != nil:
		return "SenderKeys"
	case f.Poly != nil:
		return "PolyChunk"
	case f.Output != nil:
		return "OutputChunk"
	case f.Done != nil:
		return "Done"
	}
	return "empty"
}

// handshake exchanges Hello and size lists and derives the session parameters. sizes are
// the set sizes this party announces (the receiver announces padded point counts).
func (s *session) handshake(ctx context.Context, ids []uint32, sizes []int) (*params, error) {
	start := time.Now()
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(s.rnd, nonce); err != nil {
		return nil, &Error{Code: CodeInternal, Msg: "randomness", Err: err}
	}
	hello := &Hello{
		ProtocolVersion: version.ProtocolVersion, Suite: version.Suite, Role: s.role,
		Hash: s.cfg.Hash, Lambda: uint32(s.cfg.Lambda), Nonce: nonce,
		InstanceCount: uint32(len(ids)), SoftwareVersion: version.Version,
	}
	if err := s.send(ctx, &Frame{Hello: hello}); err != nil {
		return nil, err
	}
	per := (s.cfg.MaxFrame - frameReserve) / 10
	for lo := 0; lo < len(ids); lo += per {
		hi := min(lo+per, len(ids))
		c := &SizesChunk{IDs: make([]uint32, 0, hi-lo), Sizes: make([]uint32, 0, hi-lo)}
		for b := lo; b < hi; b++ {
			c.IDs = append(c.IDs, ids[b])
			c.Sizes = append(c.Sizes, uint32(sizes[b]))
		}
		if err := s.send(ctx, &Frame{Sizes: c}); err != nil {
			return nil, err
		}
	}

	f, err := s.recv(ctx, s.cfg.HandshakeTimeout)
	if err != nil {
		return nil, err
	}
	peer := f.Hello
	if peer == nil {
		return nil, errorf(CodeProtocol, "expected Hello, got %s", frameKind(f))
	}
	switch {
	case peer.ProtocolVersion != version.ProtocolVersion:
		return nil, errorf(CodeParamMismatch, "protocol version %d, want %d", peer.ProtocolVersion, version.ProtocolVersion)
	case peer.Suite != version.Suite:
		return nil, errorf(CodeParamMismatch, "suite %q, want %q", peer.Suite, version.Suite)
	case peer.Role != RoleSender && peer.Role != RoleReceiver || peer.Role == s.role:
		return nil, errorf(CodeParamMismatch, "peer role %s, want %s", peer.Role, other(s.role))
	case peer.Hash != s.cfg.Hash:
		return nil, errorf(CodeParamMismatch, "hash %s, want %s", peer.Hash, s.cfg.Hash)
	case int(peer.Lambda) != s.cfg.Lambda:
		return nil, errorf(CodeParamMismatch, "lambda %d, want %d", peer.Lambda, s.cfg.Lambda)
	case len(peer.Nonce) != 32:
		return nil, errorf(CodeProtocol, "nonce length %d", len(peer.Nonce))
	case int(peer.InstanceCount) != len(ids):
		return nil, errorf(CodeParamMismatch, "peer has %d instances, we have %d", peer.InstanceCount, len(ids))
	}
	// Peer size list: same IDs, sizes within limits.
	peerSizes := make([]int, 0, len(ids))
	total := 0
	for len(peerSizes) < len(ids) {
		f, err := s.recv(ctx, s.cfg.HandshakeTimeout)
		if err != nil {
			return nil, err
		}
		c := f.Sizes
		if c == nil {
			return nil, errorf(CodeProtocol, "expected SizesChunk, got %s", frameKind(f))
		}
		if len(c.IDs) != len(c.Sizes) || len(c.IDs) == 0 || len(peerSizes)+len(c.IDs) > len(ids) {
			return nil, errorf(CodeProtocol, "malformed SizesChunk")
		}
		for i, id := range c.IDs {
			b := len(peerSizes)
			if id != ids[b] {
				return nil, errorf(CodeParamMismatch, "instance #%d: peer ID %d, ours %d", b, id, ids[b])
			}
			n := int(c.Sizes[i])
			if n > s.cfg.Limits.MaxInstanceSize {
				return nil, errorf(CodeLimit, "instance %d: peer size %d exceeds limit %d", id, n, s.cfg.Limits.MaxInstanceSize)
			}
			if total += n; total > s.cfg.Limits.MaxTotalItems {
				return nil, errorf(CodeLimit, "peer total items exceed limit %d", s.cfg.Limits.MaxTotalItems)
			}
			peerSizes = append(peerSizes, n)
		}
	}

	p := &params{ids: ids, n1: peerSizes, n2: sizes}
	var pairs uint64
	if s.role == RoleSender {
		p.n1, p.n2 = sizes, peerSizes
	}
	for b := range ids {
		pairs += uint64(p.n1[b]) * uint64(p.n2[b])
	}
	nR, nS := nonce, peer.Nonce
	if s.role == RoleSender {
		nR, nS = peer.Nonce, nonce
	}
	p.sid = hashing.SessionID(nR, nS)
	p.outLen = hashing.OutputLen(s.cfg.Lambda, pairs)
	if p.cmp, err = hashing.NewComparer(s.cfg.Hash, p.sid, p.outLen); err != nil {
		return nil, &Error{Code: CodeInternal, Msg: "comparer", Err: err}
	}
	s.phase("handshake", start)
	s.log.Info("session parameters", "instances", len(ids), "out_len", p.outLen, "hash", s.cfg.Hash.String())
	return p, nil
}

func other(r Role) Role {
	if r == RoleSender {
		return RoleReceiver
	}
	return RoleSender
}

// checkOwn validates the caller's instance list (and the sizes it will announce, if
// different from the item counts) against the local limits.
func checkOwn(cfg Config, own []Instance, announced []int) error {
	cfg = cfg.Defaults()
	if len(own) > cfg.Limits.MaxInstances {
		return errorf(CodeLimit, "%d instances exceed limit %d", len(own), cfg.Limits.MaxInstances)
	}
	total := 0
	for i, in := range own {
		if i > 0 && in.ID <= own[i-1].ID {
			return errorf(CodeInput, "instance IDs must be strictly increasing (%d after %d)", in.ID, own[i-1].ID)
		}
		n := len(in.Items)
		if announced != nil {
			n = max(n, announced[i])
		}
		if n > cfg.Limits.MaxInstanceSize {
			return errorf(CodeLimit, "instance %d: %d items (padded) exceed limit %d", in.ID, n, cfg.Limits.MaxInstanceSize)
		}
		total += n
	}
	if total > cfg.Limits.MaxTotalItems {
		return errorf(CodeLimit, "%d items exceed limit %d", total, cfg.Limits.MaxTotalItems)
	}
	return nil
}

// offsets returns prefix sums: off[b] is the flat index of instance b's first item.
func offsets(sizes []int) []int {
	off := make([]int, len(sizes)+1)
	for b, n := range sizes {
		off[b+1] = off[b] + n
	}
	return off
}

// waves splits instance indices into consecutive ranges of about target items each.
func waves(sizes []int, target int) [][2]int {
	var out [][2]int
	lo, acc := 0, 0
	for b, n := range sizes {
		acc += n
		if acc >= target {
			out = append(out, [2]int{lo, b + 1})
			lo, acc = b+1, 0
		}
	}
	if lo < len(sizes) {
		out = append(out, [2]int{lo, len(sizes)})
	}
	return out
}

// taskGroup runs goroutines, cancelling all on the first error (a minimal errgroup).
type taskGroup struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	err    error
}

func newGroup(parent context.Context) *taskGroup {
	ctx, cancel := context.WithCancel(parent)
	return &taskGroup{ctx: ctx, cancel: cancel}
}

// Go runs fn in a goroutine; a panic is converted into an internal error.
func (g *taskGroup) Go(fn func(ctx context.Context) error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				g.fail(&Error{Code: CodeInternal, Msg: "internal error", Err: fmt.Errorf("panic: %v", r)})
			}
		}()
		if err := fn(g.ctx); err != nil {
			g.fail(err)
		}
	}()
}

func (g *taskGroup) fail(err error) {
	g.once.Do(func() {
		g.err = err
		g.cancel()
	})
}

func (g *taskGroup) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}
