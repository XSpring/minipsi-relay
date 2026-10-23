package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/XSpring/minipsi/internal/orcapi"
	"github.com/XSpring/minipsi/internal/psi"
)

// state is the state of a session on this CC.
type state int

const (
	stateStarting state = iota // loading and checking the data, opening the session
	stateReady                 // open, waiting for run
	stateRunning               // Run in progress
	stateRejected              // never opened
	stateDone                  // Run succeeded
	stateFailed                // Run failed, or the session expired before run
	stateAborted               // aborted by the ORC before run
)

// ended reports a final state: only then may the ORC delete the session.
func (s state) ended() bool { return s >= stateRejected }

func (s state) String() string {
	switch s {
	case stateStarting:
		return "starting"
	case stateReady:
		return "ready"
	case stateRunning:
		return "running"
	case stateRejected:
		return "rejected"
	case stateDone:
		return "done"
	case stateFailed:
		return "failed"
	case stateAborted:
		return "aborted"
	}
	return fmt.Sprintf("state(%d)", int(s))
}

// session is what the service needs of psi.ReceiverSession and psi.SenderSession.
type session interface {
	run(ctx context.Context, items [][]byte) (matches [][]byte, st psi.Stats, err error)
	Close() error
}

type receiverRun struct{ psi.ReceiverSession }

func (r receiverRun) run(ctx context.Context, items [][]byte) ([][]byte, psi.Stats, error) {
	res, err := r.Run(ctx, items)
	if err != nil {
		return nil, psi.Stats{}, err
	}
	return res.Matches, res.Stats, nil
}

type senderRun struct{ psi.SenderSession }

func (s senderRun) run(ctx context.Context, items [][]byte) ([][]byte, psi.Stats, error) {
	res, err := s.Run(ctx, items)
	if err != nil {
		return nil, psi.Stats{}, err
	}
	return nil, res.Stats, nil
}

// stopper stops a timer; *time.Timer is one.
type stopper interface{ Stop() bool }

// record is one session of this CC. Its fields after mu are guarded by mu. Never hold mu
// while calling the engine, the catalog or the ORC, and never take the store's lock while
// holding mu.
type record struct {
	id       string
	peerCCID string
	role     orcapi.Role

	mu      sync.Mutex
	state   state
	gen     uint64             // counts state changes; a timer armed for an older one does nothing
	deleted bool               // removed by I4 or by its TTL
	timer   stopper            // TTL timer of the current state, or nil
	cancel  context.CancelFunc // cancels the loading (starting) or Run (running)
	sess    session            // the open session (ready, running)
	items   [][]byte           // the prepared items (ready)
	matches [][]byte           // the receiver's result (done), kept until the record is removed
}

// store keeps the sessions of this CC by session ID. memStore keeps them in memory; another
// implementation may replace it later.
type store interface {
	// add stores r. It returns an error that wraps psi.ErrSessionExists or psi.ErrBusy.
	add(r *record) error
	// get returns the record of id, or nil.
	get(id string) *record
	// remove removes r, unless another record has taken its ID since.
	remove(r *record)
}

// memStore is a store in memory that holds at most max records, ended ones included.
type memStore struct {
	mu  sync.Mutex
	m   map[string]*record
	max int
}

func newMemStore(max int) *memStore {
	return &memStore{m: map[string]*record{}, max: max}
}

func (s *memStore) add(r *record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[r.id]; ok {
		return fmt.Errorf("service: session %s: %w", r.id, psi.ErrSessionExists)
	}
	if len(s.m) >= s.max {
		return fmt.Errorf("service: %d sessions kept: %w", len(s.m), psi.ErrBusy)
	}
	s.m[r.id] = r
	return nil
}

func (s *memStore) get(id string) *record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

func (s *memStore) remove(r *record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[r.id] == r {
		delete(s.m, r.id)
	}
}

// setState moves r to st and arms the TTL timer of the new state: ready and ended states
// expire after s.ttl, starting and running do not (the loading context and the protocol
// timeouts bound them). The caller holds r.mu.
func (s *PSIService) setState(r *record, st state) {
	r.state = st
	r.gen++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if st == stateReady || st.ended() {
		gen := r.gen
		r.timer = s.afterFunc(s.ttl, func() { s.expire(r, gen) })
	}
}

// expire runs when the TTL of state gen of r has passed. A session still ready is closed
// (the peer is told to stop) and becomes failed; there is no status report for this, as the
// ORC API has none. An ended session is removed.
func (s *PSIService) expire(r *record, gen uint64) {
	r.mu.Lock()
	if r.gen != gen || r.deleted {
		r.mu.Unlock()
		return
	}
	switch {
	case r.state == stateReady:
		sess := r.sess
		r.sess, r.items = nil, nil
		s.setState(r, stateFailed)
		r.mu.Unlock()
		s.log.Info("session expired before run", "session_id", r.id)
		_ = sess.Close()
	case r.state.ended():
		r.deleted = true
		r.matches, r.items = nil, nil
		r.gen++
		r.timer = nil
		r.mu.Unlock()
		s.store.remove(r)
	default:
		r.mu.Unlock()
	}
}

// realAfterFunc is the TTL timer outside tests.
func realAfterFunc(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }
