package orcapi

// ProblemCode is the machine-readable reason of a Problem.
type ProblemCode string

// Problem codes, with the HTTP status they come with.
const (
	CodeMalformed      ProblemCode = "malformed"       // 400
	CodeUnknownSession ProblemCode = "unknown_session" // 404
	CodeInvalidState   ProblemCode = "invalid_state"   // 409
	CodeTooLarge       ProblemCode = "too_large"       // 413
	CodeMisrouted      ProblemCode = "misrouted"       // 422
	CodeInternal       ProblemCode = "internal"        // 500
	CodeUnavailable    ProblemCode = "unavailable"     // 503
)

// Problem is the body of every error response on both sides (RFC 9457,
// application/problem+json). Detail holds fixed text only, never message values.
type Problem struct {
	Title  string      `json:"title"`
	Status int         `json:"status"`
	Code   ProblemCode `json:"code"`
	Detail string      `json:"detail,omitempty"`
}
