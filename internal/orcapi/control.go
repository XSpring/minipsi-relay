package orcapi

// Role is the part a CC plays in a session.
type Role string

// Roles. The receiver learns the intersection; the sender learns nothing about it.
const (
	RoleReceiver Role = "receiver"
	RoleSender   Role = "sender"
)

// PrepStep is one step that turns a raw value of the dataset into a PSI item. Both CCs of a
// session must apply the same steps in the same order, because items match only when their
// bytes are equal.
type PrepStep string

// Prep steps.
const (
	// PrepTrim removes leading and trailing white space.
	PrepTrim PrepStep = "trim"
	// PrepNFC converts to Unicode normalization form C.
	PrepNFC PrepStep = "nfc"
	// PrepLower converts to lower case.
	PrepLower PrepStep = "lower"
)

// StartSessionRequest starts a session on a CC (ORC to CC, POST /v1/sessions). The ORC sends one
// to each CC of the session, with opposite roles and the same Prep.
type StartSessionRequest struct {
	// SessionID is assigned by the ORC.
	SessionID string `json:"session_id"`
	// CCID is the CC that receives this request; PeerCCID is the other CC of the session.
	CCID     string `json:"cc_id"`
	PeerCCID string `json:"peer_cc_id"`
	Role     Role   `json:"role"`
	// ContractRef identifies the contract between the data owners; it is opaque to the CC.
	ContractRef string `json:"contract_ref"`
	// Dataset names the data this CC uses: an entry of its local dataset catalog.
	Dataset string `json:"dataset"`
	// Prep lists the steps applied to every value, in order; an empty list keeps the values
	// as they are.
	Prep []PrepStep `json:"prep"`
}

// Validate checks the rules of the request.
func (r *StartSessionRequest) Validate() error {
	var p problems
	if !ValidSessionID(r.SessionID) {
		p.add("session_id", "invalid session ID")
	}
	if !ValidCCID(r.CCID) {
		p.add("cc_id", "invalid CC ID")
	}
	if !ValidCCID(r.PeerCCID) {
		p.add("peer_cc_id", "invalid CC ID")
	}
	if r.Role != RoleReceiver && r.Role != RoleSender {
		p.add("role", "must be receiver or sender")
	}
	if r.ContractRef == "" {
		p.add("contract_ref", "required")
	}
	if !ValidDatasetName(r.Dataset) {
		p.add("dataset", "invalid dataset name")
	}
	if r.Prep == nil {
		p.add("prep", "required (use [] for no steps)")
	}
	for _, s := range r.Prep {
		if s != PrepTrim && s != PrepNFC && s != PrepLower {
			p.add("prep", "unknown step")
		}
	}
	return p.err()
}

// AbortReason says why the ORC aborts a session.
type AbortReason string

// Abort reasons.
const (
	AbortPeerRejected AbortReason = "peer_rejected"
	AbortPeerFailed   AbortReason = "peer_failed"
	AbortDeadline     AbortReason = "deadline"
	AbortOperator     AbortReason = "operator"
)

// AbortSessionRequest aborts a session on a CC (ORC to CC,
// POST /v1/sessions/{session_id}/abort).
type AbortSessionRequest struct {
	Reason AbortReason `json:"reason"`
}

// Validate checks the rules of the request.
func (r *AbortSessionRequest) Validate() error {
	var p problems
	switch r.Reason {
	case AbortPeerRejected, AbortPeerFailed, AbortDeadline, AbortOperator:
	default:
		p.add("reason", "unknown reason")
	}
	return p.err()
}
