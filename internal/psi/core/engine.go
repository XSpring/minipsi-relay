package core

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/psi"
)

// defaultMaxSessions is the number of sessions an Engine keeps open at once by default.
const defaultMaxSessions = 16

// maxRelayFrame is the largest Config.Protocol.MaxFrame. Base64 makes a frame about 4/3 as
// large, so a frame of this size still fits in psi.MaxMessageBytes.
const maxRelayFrame = 5 << 20

// Config configures an Engine.
type Config struct {
	// Protocol holds the settings of the PSI core: hash, lambda, limits, timeouts, padding.
	// Its MaxFrame may be at most 5 MiB, so that every message fits in psi.MaxMessageBytes.
	// The handshake and phase timeouts must cover two orchestrator hops and the time between
	// the peer's Open and its Run.
	Protocol protocol.Config
	// MaxSessions is the number of sessions open at once; 0 means 16.
	MaxSessions int
}

// Engine implements psi.Engine over the PSI core in internal/protocol. Each session runs on a
// relayConn: it posts its messages through the Outbox given to Open and receives the messages
// passed to Deliver. An Engine is safe for concurrent use.
type Engine struct {
	cfg         protocol.Config
	maxSessions int

	mu       sync.Mutex
	sessions map[string]*session // open sessions by session ID
}

var _ psi.Engine = (*Engine)(nil)

// New returns an Engine for cfg. It refuses an invalid protocol configuration, a MaxFrame
// above 5 MiB and a negative MaxSessions.
func New(cfg Config) (*Engine, error) {
	pc := cfg.Protocol.Defaults()
	if pc.MaxFrame > maxRelayFrame {
		return nil, fmt.Errorf("core: max frame %d above %d", pc.MaxFrame, maxRelayFrame)
	}
	if err := protocol.CheckInput(pc, protocol.RoleReceiver, nil); err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}
	n := cfg.MaxSessions
	switch {
	case n < 0:
		return nil, fmt.Errorf("core: max sessions %d below 0", n)
	case n == 0:
		n = defaultMaxSessions
	}
	return &Engine{cfg: pc, maxSessions: n, sessions: map[string]*session{}}, nil
}

// Check implements psi.Engine.
func (e *Engine) Check(role psi.Role, items [][]byte) error {
	pr, err := protocolRole(role)
	if err != nil {
		return &psi.Error{Kind: psi.ErrInput, Cause: err}
	}
	distinct, _ := dedupItems(items)
	if err := protocol.CheckInput(e.cfg, pr, []protocol.Instance{{Items: distinct}}); err != nil {
		return &psi.Error{Kind: psi.ErrInput, Cause: err}
	}
	return nil
}

// OpenReceiver implements psi.Engine.
func (e *Engine) OpenReceiver(sessionID string, out psi.Outbox) (psi.ReceiverSession, error) {
	s, err := e.open(sessionID, psi.RoleReceiver, out)
	if err != nil {
		return nil, err
	}
	return &receiverSession{s}, nil
}

// OpenSender implements psi.Engine.
func (e *Engine) OpenSender(sessionID string, out psi.Outbox) (psi.SenderSession, error) {
	s, err := e.open(sessionID, psi.RoleSender, out)
	if err != nil {
		return nil, err
	}
	return &senderSession{s}, nil
}

func (e *Engine) open(id string, role psi.Role, out psi.Outbox) (*session, error) {
	if !psi.ValidSessionID(id) {
		return nil, fmt.Errorf("core: %w: invalid session ID", psi.ErrMalformed)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.sessions[id]; ok {
		return nil, fmt.Errorf("core: session %s: %w", id, psi.ErrSessionExists)
	}
	if len(e.sessions) >= e.maxSessions {
		return nil, fmt.Errorf("core: %d sessions open: %w", len(e.sessions), psi.ErrBusy)
	}
	s := newSession(e, id, role, out)
	e.sessions[id] = s
	return s, nil
}

// Deliver implements psi.Engine.
func (e *Engine) Deliver(ctx context.Context, m *psi.Message) error {
	if m == nil || !psi.ValidSessionID(m.SessionID) {
		return fmt.Errorf("core: %w: invalid session ID", psi.ErrMalformed)
	}
	e.mu.Lock()
	s := e.sessions[m.SessionID]
	e.mu.Unlock()
	if s == nil {
		return fmt.Errorf("core: session %s: %w", m.SessionID, psi.ErrUnknownSession)
	}
	err := s.conn.deliver(ctx, m)
	if errors.Is(err, errEnded) {
		return fmt.Errorf("core: session %s: %w", m.SessionID, psi.ErrUnknownSession)
	}
	return err
}

// remove unregisters s, unless another session has taken its ID since.
func (e *Engine) remove(s *session) {
	e.mu.Lock()
	if e.sessions[s.id] == s {
		delete(e.sessions, s.id)
	}
	e.mu.Unlock()
}

// dedupItems returns the distinct items in the order of their first occurrence, and the
// number of exact duplicates dropped. The returned slices share memory with items.
func dedupItems(items [][]byte) ([][]byte, int) {
	seen := make(map[string]struct{}, len(items))
	distinct := make([][]byte, 0, len(items))
	for _, it := range items {
		if _, ok := seen[string(it)]; ok {
			continue
		}
		seen[string(it)] = struct{}{}
		distinct = append(distinct, it)
	}
	return distinct, len(items) - len(distinct)
}

func protocolRole(r psi.Role) (protocol.Role, error) {
	switch r {
	case psi.RoleReceiver:
		return protocol.RoleReceiver, nil
	case psi.RoleSender:
		return protocol.RoleSender, nil
	}
	return 0, fmt.Errorf("core: invalid role %s", r)
}
