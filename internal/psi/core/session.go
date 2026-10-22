package core

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/psi"
)

// Fixed texts of the Aborts that the adapter sends itself; they reveal nothing local.
const (
	abortInput     = "input rejected"
	abortMalformed = "malformed message"
	abortClosed    = "session closed"
)

type runState int

const (
	stateIdle    runState = iota // opened, Run not called
	stateRunning                 // Run in progress
	stateRan                     // Run returned
	stateClosed                  // closed before Run
)

// session is the state shared by receiverSession and senderSession.
type session struct {
	e    *Engine
	id   string
	role psi.Role
	conn *relayConn

	mu      sync.Mutex
	state   runState
	cancel  context.CancelFunc // cancels a running Run
	runDone chan struct{}      // closed once Run has returned and cleaned up
}

func newSession(e *Engine, id string, role psi.Role, out psi.Outbox) *session {
	return &session{e: e, id: id, role: role, conn: newRelayConn(id, out), runDone: make(chan struct{})}
}

// runInfo is what the core returns besides the receiver's matches.
type runInfo struct {
	sid   [16]byte
	stats protocol.Stats
}

// run executes this party's side of the session over items and returns the distinct items
// the core ran on. core runs RunReceiver or RunSender. Around it, run allows one call only,
// checks the input, tells the peer to stop on failures that the core does not report to it,
// maps errors to *psi.Error, and ends the session.
func (s *session) run(ctx context.Context, items [][]byte,
	core func(context.Context, []protocol.Instance) (runInfo, error)) ([][]byte, psi.Stats, error) {
	start := time.Now()
	ctx, err := s.begin(ctx)
	if err != nil {
		return nil, psi.Stats{}, err
	}
	defer s.end()

	distinct, removed := dedupItems(items)
	inst := []protocol.Instance{{Items: distinct}}
	role, _ := protocolRole(s.role) // valid: set by Open
	if err := protocol.CheckInput(s.e.cfg, role, inst); err != nil {
		// No message was sent; tell the peer so that it does not wait for its timeout.
		_ = protocol.AbortConn(ctx, s.conn, protocol.CodeInput, abortInput)
		return nil, psi.Stats{}, &psi.Error{Kind: psi.ErrInput, SessionID: s.id,
			Code: protocol.CodeInput.String(), Cause: err}
	}
	info, err := core(ctx, inst)
	if err != nil {
		// The core sends no Abort when Recv fails; a refused message is a protocol
		// violation that the peer must hear about.
		if errors.Is(err, errRefused) {
			_ = protocol.AbortConn(ctx, s.conn, protocol.CodeProtocol, abortMalformed)
		}
		return nil, psi.Stats{}, toError(s.id, err)
	}
	cs := s.conn.stats()
	st := psi.Stats{
		ProtocolSID:       hex.EncodeToString(info.sid[:]),
		Items:             len(items),
		DuplicatesRemoved: removed,
		MessagesSent:      cs.Sent,
		MessagesReceived:  cs.Received,
		BytesSent:         cs.BytesSent,
		BytesReceived:     cs.BytesReceived,
		Duration:          time.Since(start),
	}
	for _, p := range info.stats.Phases {
		st.Phases = append(st.Phases, psi.Phase{Name: p.Name, Duration: p.Duration})
	}
	return distinct, st, nil
}

// begin moves the session to running and returns the context Run uses, which close cancels.
func (s *session) begin(ctx context.Context) (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case stateRunning, stateRan:
		return nil, &psi.Error{Kind: psi.ErrInternal, SessionID: s.id, Cause: errors.New("core: Run called twice")}
	case stateClosed:
		return nil, &psi.Error{Kind: psi.ErrCanceled, SessionID: s.id, Cause: errClosed}
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.state = stateRunning
	return ctx, nil
}

// end ends the session after Run: later messages get psi.ErrUnknownSession.
func (s *session) end() {
	s.mu.Lock()
	s.state = stateRan
	s.cancel()
	s.mu.Unlock()
	s.release()
	close(s.runDone)
}

func (s *session) release() {
	s.e.remove(s)
	s.conn.close()
}

// close implements Close for both roles.
func (s *session) close() error {
	s.mu.Lock()
	switch s.state {
	case stateIdle:
		s.state = stateClosed
		s.mu.Unlock()
		// The peer may already wait for this party's first message.
		_ = protocol.AbortConn(context.Background(), s.conn, protocol.CodeCancelled, abortClosed)
		s.release()
	case stateRunning:
		s.cancel() // the core sends the Abort; Run cleans up
		s.mu.Unlock()
		<-s.runDone
	default:
		s.mu.Unlock()
	}
	return nil
}

// receiverSession implements psi.ReceiverSession.
type receiverSession struct{ s *session }

// Run implements psi.ReceiverSession.
func (r *receiverSession) Run(ctx context.Context, items [][]byte) (*psi.ReceiverResult, error) {
	var res *protocol.Result
	distinct, st, err := r.s.run(ctx, items, func(ctx context.Context, inst []protocol.Instance) (runInfo, error) {
		var err error
		res, err = protocol.RunReceiver(ctx, r.s.e.cfg, r.s.conn, inst)
		if err != nil {
			return runInfo{}, err
		}
		return runInfo{sid: res.SID, stats: res.Stats}, nil
	})
	if err != nil {
		return nil, err
	}
	// The indices are ascending, so the values keep the order of their first occurrence.
	idx := res.Instances[0].Matches
	matches := make([][]byte, len(idx))
	for i, j := range idx {
		matches[i] = distinct[j]
	}
	return &psi.ReceiverResult{Matches: matches, Stats: st}, nil
}

// Close implements psi.ReceiverSession.
func (r *receiverSession) Close() error { return r.s.close() }

// senderSession implements psi.SenderSession.
type senderSession struct{ s *session }

// Run implements psi.SenderSession.
func (ss *senderSession) Run(ctx context.Context, items [][]byte) (*psi.SenderResult, error) {
	_, st, err := ss.s.run(ctx, items, func(ctx context.Context, inst []protocol.Instance) (runInfo, error) {
		res, err := protocol.RunSender(ctx, ss.s.e.cfg, ss.s.conn, inst)
		if err != nil {
			return runInfo{}, err
		}
		return runInfo{sid: res.SID, stats: res.Stats}, nil
	})
	if err != nil {
		return nil, err
	}
	return &psi.SenderResult{Stats: st}, nil
}

// Close implements psi.SenderSession.
func (ss *senderSession) Close() error { return ss.s.close() }
