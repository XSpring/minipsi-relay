package orcapi

// State is the state of a session on one CC.
type State string

// States. StateReady is the only state that is not final.
const (
	// StateReady: the data is prepared and checked, and the session is open; the CC waits for
	// run.
	StateReady State = "ready"
	// StateRejected: the session never opened.
	StateRejected State = "rejected"
	// StateDone: the session finished.
	StateDone State = "done"
	// StateFailed: the session opened and then failed or was aborted.
	StateFailed State = "failed"
)

// ErrorKind classifies a failure; the kinds are the session errors of the PSI port
// (internal/psi).
type ErrorKind string

// Error kinds.
const (
	ErrorInput       ErrorKind = "input"
	ErrorMismatch    ErrorKind = "mismatch"
	ErrorProtocol    ErrorKind = "protocol"
	ErrorPeerAborted ErrorKind = "peer_aborted"
	ErrorTimeout     ErrorKind = "timeout"
	ErrorDelivery    ErrorKind = "delivery"
	ErrorCanceled    ErrorKind = "canceled"
	ErrorInternal    ErrorKind = "internal"
)

// StatusReport tells the ORC the state of a session on one CC (CC to ORC,
// POST /v1/sessions/{session_id}/status). Error is set for StateRejected and StateFailed, Stats
// for StateDone, and neither for StateReady. A report never carries items, match counts, item
// counts or free text.
type StatusReport struct {
	CCID  string        `json:"cc_id"`
	State State         `json:"state"`
	Error *ErrorInfo    `json:"error,omitempty"`
	Stats *SessionStats `json:"stats,omitempty"`
}

// ErrorInfo describes why a session was rejected or failed.
type ErrorInfo struct {
	Kind ErrorKind `json:"kind"`
	// Code is the PSI abort code, such as "ERR_TIMEOUT", when there is one.
	Code string `json:"code,omitempty"`
	// Remote reports that the peer ended the session.
	Remote bool `json:"remote"`
}

// SessionStats describes a finished session.
type SessionStats struct {
	// ProtocolSID is the hex session ID derived in the PSI handshake; both CCs report the same
	// value. It is not the ORC's session ID.
	ProtocolSID string `json:"protocol_sid"`
	// Messages counts the PSI messages this CC sent and received.
	Messages   MessageCounts `json:"messages"`
	DurationMS int64         `json:"duration_ms"`
}

// MessageCounts counts messages in each direction.
type MessageCounts struct {
	Sent uint64 `json:"sent"`
	Recv uint64 `json:"recv"`
}

// Validate checks the rules of the report.
func (s *StatusReport) Validate() error {
	var p problems
	if !ValidCCID(s.CCID) {
		p.add("cc_id", "invalid CC ID")
	}
	switch s.State {
	case StateReady:
		if s.Error != nil {
			p.add("error", "not allowed for ready")
		}
		if s.Stats != nil {
			p.add("stats", "not allowed for ready")
		}
	case StateRejected, StateFailed:
		if s.Error == nil {
			p.add("error", "required for rejected and failed")
		} else if !validErrorKind(s.Error.Kind) {
			p.add("error.kind", "unknown kind")
		}
		if s.Stats != nil {
			p.add("stats", "only allowed for done")
		}
	case StateDone:
		if s.Error != nil {
			p.add("error", "not allowed for done")
		}
		if s.Stats == nil {
			p.add("stats", "required for done")
		} else if s.Stats.ProtocolSID == "" {
			p.add("stats.protocol_sid", "required")
		}
	default:
		p.add("state", "must be ready, rejected, done or failed")
	}
	return p.err()
}

func validErrorKind(k ErrorKind) bool {
	switch k {
	case ErrorInput, ErrorMismatch, ErrorProtocol, ErrorPeerAborted, ErrorTimeout, ErrorDelivery,
		ErrorCanceled, ErrorInternal:
		return true
	}
	return false
}
