// Package service holds the business logic of a CC in relay mode. The handler (internal/handler)
// calls PSIService for each request of the orchestrator (ORC); PSIService prepares the data,
// runs the session through the port layer (internal/psi: Check, Open, Run, Close, Deliver) and
// reports to the ORC (orc_outbox.go). It never imports the PSI core. See
// internal/orcapi/README.md for the flows.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/XSpring/minipsi/internal/orcapi"
	"github.com/XSpring/minipsi/internal/psi"
)

// Defaults of Config.
const (
	defaultMaxSessions = 16
	defaultTTL         = 10 * time.Minute
	defaultHTTPTimeout = 30 * time.Second
	// reportTimeout bounds one status report (O2).
	reportTimeout = 30 * time.Second
)

// Catalog is the local dataset catalog of this CC.
type Catalog interface {
	// Load returns the raw values of dataset name. Its errors must not contain values.
	Load(ctx context.Context, name string) ([][]byte, error)
}

// Config configures a PSIService.
type Config struct {
	// CCID is the ID of this CC, and ORCURL the base URL of the ORC; see ORCFromEnv.
	CCID   string
	ORCURL string
	// Client sends the requests to the ORC; nil means a client with a 30 s timeout.
	Client *http.Client
	// Engine runs the PSI sessions.
	Engine psi.Engine
	// Catalog loads the datasets that the ORC names.
	Catalog Catalog
	// MaxSessions is the number of sessions kept at once, ended ones until the ORC deletes
	// them included; 0 means 16.
	MaxSessions int
	// TTL bounds how long the data may load, how long a session waits for run, and how long
	// an ended session waits to be deleted; 0 means 10 minutes.
	TTL time.Duration
	// Logger receives no items, values, payloads or results; nil discards.
	Logger *slog.Logger
}

// PSIService runs the sessions that the ORC starts on this CC. It implements the Service
// interface of internal/handler and is safe for concurrent use.
type PSIService struct {
	ccID      string
	orc       *orcClient
	engine    psi.Engine
	catalog   Catalog
	store     store
	ttl       time.Duration
	log       *slog.Logger
	base      context.Context // parent of all background work; never a request context
	afterFunc func(time.Duration, func()) stopper
}

// New returns a PSIService for cfg.
func New(cfg Config) (*PSIService, error) {
	var errs []error
	if !orcapi.ValidCCID(cfg.CCID) {
		errs = append(errs, errors.New("service: invalid CC ID"))
	}
	if cfg.Engine == nil {
		errs = append(errs, errors.New("service: no engine"))
	}
	if cfg.Catalog == nil {
		errs = append(errs, errors.New("service: no catalog"))
	}
	if cfg.MaxSessions < 0 || cfg.TTL < 0 {
		errs = append(errs, errors.New("service: max sessions and TTL must not be negative"))
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	orc, err := newORCClient(cfg.ORCURL, client, cfg.CCID)
	if err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	s := &PSIService{
		ccID: cfg.CCID, orc: orc, engine: cfg.Engine, catalog: cfg.Catalog,
		store: newMemStore(orDefault(cfg.MaxSessions, defaultMaxSessions)), ttl: cfg.TTL, log: cfg.Logger,
		base: context.Background(), afterFunc: realAfterFunc,
	}
	if s.ttl == 0 {
		s.ttl = defaultTTL
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s, nil
}

// orDefault returns v, or def when v is 0.
func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// StartSession serves I1. It registers the session and returns; loading the data, the checks
// and opening the session run in the background and end with a status report: ready or
// rejected.
func (s *PSIService) StartSession(_ context.Context, req *orcapi.StartSessionRequest) error {
	if err := req.Validate(); err != nil {
		return fmt.Errorf("service: %w: %w", psi.ErrMalformed, err)
	}
	if req.CCID != s.ccID {
		return fmt.Errorf("service: session %s: %w", req.SessionID, ErrMisrouted)
	}
	ctx, cancel := context.WithTimeout(s.base, s.ttl)
	r := &record{id: req.SessionID, peerCCID: req.PeerCCID, role: req.Role, cancel: cancel}
	if err := s.store.add(r); err != nil {
		cancel()
		return err
	}
	go s.load(ctx, cancel, r, req.Dataset, slices.Clone(req.Prep))
	return nil
}

// load prepares the data of r and opens its session, then reports ready or rejected, unless
// the ORC aborted the session meanwhile.
func (s *PSIService) load(ctx context.Context, cancel context.CancelFunc, r *record, dataset string, steps []orcapi.PrepStep) {
	defer cancel()
	sess, items, info := s.open(ctx, r, dataset, steps)
	r.mu.Lock()
	if r.state != stateStarting || r.deleted {
		r.mu.Unlock()
		if sess != nil {
			_ = sess.Close()
		}
		return
	}
	r.cancel = nil
	rep := &orcapi.StatusReport{State: orcapi.StateReady}
	if info != nil {
		s.setState(r, stateRejected)
		rep = &orcapi.StatusReport{State: orcapi.StateRejected, Error: info}
	} else {
		r.sess, r.items = sess, items
		s.setState(r, stateReady)
	}
	r.mu.Unlock()
	s.report(r.id, rep)
}

// open loads and prepares dataset, checks the items and opens the session. On failure it
// returns why, for the rejected report.
func (s *PSIService) open(ctx context.Context, r *record, dataset string, steps []orcapi.PrepStep) (session, [][]byte, *orcapi.ErrorInfo) {
	role := psi.RoleReceiver
	if r.role == orcapi.RoleSender {
		role = psi.RoleSender
	}
	values, err := s.catalog.Load(ctx, dataset)
	if cerr := ctx.Err(); cerr != nil {
		err = cerr // the deadline or an abort, whatever error the catalog returned
	}
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return nil, nil, &orcapi.ErrorInfo{Kind: orcapi.ErrorTimeout}
		case errors.Is(err, context.Canceled): // aborted: nothing is reported
			return nil, nil, &orcapi.ErrorInfo{Kind: orcapi.ErrorCanceled}
		}
		s.log.Warn("dataset not loaded", "session_id", r.id, "dataset", dataset, "error", err)
		return nil, nil, &orcapi.ErrorInfo{Kind: orcapi.ErrorInput}
	}
	items, err := prepare(values, steps)
	if err != nil {
		return nil, nil, &orcapi.ErrorInfo{Kind: orcapi.ErrorInput}
	}
	if err := s.engine.Check(role, items); err != nil {
		return nil, nil, errorInfo(err)
	}
	out := &orcOutbox{c: s.orc, sessionID: r.id, peerCCID: r.peerCCID}
	var sess session
	if role == psi.RoleReceiver {
		rs, err := s.engine.OpenReceiver(r.id, out)
		if err != nil {
			return nil, nil, &orcapi.ErrorInfo{Kind: orcapi.ErrorInternal}
		}
		sess = receiverRun{rs}
	} else {
		ss, err := s.engine.OpenSender(r.id, out)
		if err != nil {
			return nil, nil, &orcapi.ErrorInfo{Kind: orcapi.ErrorInternal}
		}
		sess = senderRun{ss}
	}
	return sess, items, nil
}

// RunSession serves I2. It starts Run in the background; Run ends with a status report: done
// or failed.
func (s *PSIService) RunSession(_ context.Context, sessionID string) error {
	r, err := s.lockRecord(sessionID)
	if err != nil {
		return err
	}
	if r.state != stateReady {
		r.mu.Unlock()
		return fmt.Errorf("service: session %s is %s: %w", sessionID, r.state, ErrInvalidState)
	}
	ctx, cancel := context.WithCancel(s.base)
	sess, items := r.sess, r.items
	r.cancel, r.items = cancel, nil
	s.setState(r, stateRunning)
	r.mu.Unlock()
	go s.run(ctx, cancel, r, sess, items)
	return nil
}

// run runs the session, records the outcome, then reports it.
func (s *PSIService) run(ctx context.Context, cancel context.CancelFunc, r *record, sess session, items [][]byte) {
	defer cancel()
	matches, st, err := sess.run(ctx, items)
	_ = sess.Close()
	r.mu.Lock()
	r.sess, r.cancel = nil, nil
	var rep *orcapi.StatusReport
	if err != nil {
		s.setState(r, stateFailed)
		rep = &orcapi.StatusReport{State: orcapi.StateFailed, Error: errorInfo(err)}
	} else {
		r.matches = matches
		s.setState(r, stateDone)
		rep = &orcapi.StatusReport{State: orcapi.StateDone, Stats: &orcapi.SessionStats{
			ProtocolSID: st.ProtocolSID,
			Messages:    orcapi.MessageCounts{Sent: st.MessagesSent, Recv: st.MessagesReceived},
			DurationMS:  st.Duration.Milliseconds(),
		}}
	}
	r.mu.Unlock()
	s.report(r.id, rep)
}

// AbortSession serves I3. While the data loads, the loading stops; a ready session is closed,
// which tells the peer to stop; neither is reported. A running session is canceled, and Run
// then reports failed.
func (s *PSIService) AbortSession(_ context.Context, sessionID string, req *orcapi.AbortSessionRequest) error {
	r, err := s.lockRecord(sessionID)
	if err != nil {
		return err
	}
	switch r.state {
	case stateStarting:
		r.cancel()
		r.cancel = nil
		s.setState(r, stateAborted)
	case stateReady:
		sess := r.sess
		r.sess, r.items = nil, nil
		s.setState(r, stateAborted)
		r.mu.Unlock()
		go func() { _ = sess.Close() }()
		s.log.Info("session aborted", "session_id", sessionID, "reason", req.Reason)
		return nil
	case stateRunning:
		r.cancel()
	default:
		r.mu.Unlock()
		return fmt.Errorf("service: session %s is %s: %w", sessionID, r.state, ErrInvalidState)
	}
	r.mu.Unlock()
	s.log.Info("session aborted", "session_id", sessionID, "reason", req.Reason)
	return nil
}

// DeleteSession serves I4: it removes an ended session with its items and result.
func (s *PSIService) DeleteSession(_ context.Context, sessionID string) error {
	r, err := s.lockRecord(sessionID)
	if err != nil {
		return err
	}
	if !r.state.ended() {
		r.mu.Unlock()
		return fmt.Errorf("service: session %s is %s: %w", sessionID, r.state, ErrInvalidState)
	}
	r.deleted = true
	r.matches, r.items = nil, nil
	r.gen++
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.mu.Unlock()
	s.store.remove(r)
	return nil
}

// Deliver serves I5: it passes the message inside env to the engine.
func (s *PSIService) Deliver(ctx context.Context, env *orcapi.Envelope) error {
	if env.ReceiverCCID != s.ccID {
		return fmt.Errorf("service: session %s: %w", env.SessionID, ErrMisrouted)
	}
	return s.engine.Deliver(ctx, toMessage(env))
}

// lockRecord returns the record of sessionID with its lock held, or an error that wraps
// psi.ErrUnknownSession.
func (s *PSIService) lockRecord(sessionID string) (*record, error) {
	r := s.store.get(sessionID)
	if r != nil {
		r.mu.Lock()
		if !r.deleted {
			return r, nil
		}
		r.mu.Unlock()
	}
	return nil, fmt.Errorf("service: session %s: %w", sessionID, psi.ErrUnknownSession)
}

// report sends a status report with its own context, so that it is sent even when the work it
// reports on was canceled. A failed report is logged and not retried.
func (s *PSIService) report(sessionID string, rep *orcapi.StatusReport) {
	ctx, cancel := context.WithTimeout(s.base, reportTimeout)
	defer cancel()
	rep.CCID = s.ccID
	if err := s.orc.reportStatus(ctx, sessionID, rep); err != nil {
		s.log.Warn("status report failed", "session_id", sessionID, "state", rep.State, "error", err)
	}
}

// errorKinds maps the session failure kinds of the port to those of the ORC API.
var errorKinds = []struct {
	err  error
	kind orcapi.ErrorKind
}{
	{psi.ErrInput, orcapi.ErrorInput},
	{psi.ErrMismatch, orcapi.ErrorMismatch},
	{psi.ErrProtocol, orcapi.ErrorProtocol},
	{psi.ErrPeerAborted, orcapi.ErrorPeerAborted},
	{psi.ErrTimeout, orcapi.ErrorTimeout},
	{psi.ErrDelivery, orcapi.ErrorDelivery},
	{psi.ErrCanceled, orcapi.ErrorCanceled},
}

// errorInfo describes a failure of Check or Run for a status report: its kind, and the abort
// code and side when err is a *psi.Error. Anything else is internal.
func errorInfo(err error) *orcapi.ErrorInfo {
	info := &orcapi.ErrorInfo{Kind: orcapi.ErrorInternal}
	for _, k := range errorKinds {
		if errors.Is(err, k.err) {
			info.Kind = k.kind
			break
		}
	}
	var pe *psi.Error
	if errors.As(err, &pe) {
		info.Code, info.Remote = pe.Code, pe.Remote
	}
	return info
}
