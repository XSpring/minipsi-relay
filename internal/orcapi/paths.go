package orcapi

// Endpoint patterns for http.ServeMux. The CC serves the first five; the ORC serves
// PatternReportStatus and PatternMessages.
const (
	PatternStartSession  = "POST /v1/sessions"
	PatternRunSession    = "POST /v1/sessions/{session_id}/run"
	PatternAbortSession  = "POST /v1/sessions/{session_id}/abort"
	PatternDeleteSession = "DELETE /v1/sessions/{session_id}"
	PatternMessages      = "POST /v1/messages"
	PatternReportStatus  = "POST /v1/sessions/{session_id}/status"
)

// Body size limits. MaxEnvelopeBytes leaves 4 KiB for the CC IDs on top of the largest PSI
// message (8 MiB, psi.MaxMessageBytes).
const (
	MaxControlBytes  = 64 << 10
	MaxEnvelopeBytes = 8<<20 + 4<<10
)
