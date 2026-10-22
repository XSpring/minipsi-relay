// Package orcapi defines the HTTP API between a compute cluster (CC) and the orchestrator (ORC):
// the messages each side sends, their JSON form, the endpoints and the validation rules. It is
// the data contract of relay mode, where the two CCs never connect and every message goes
// through the ORC. The decision is recorded in ADR-0010.
//
// The package imports only the standard library, so the handler and the service can share it.
//
// # Endpoints
//
// On the CC, called by the ORC:
//
//	POST   /v1/sessions                       StartSessionRequest   202
//	POST   /v1/sessions/{session_id}/run      (no body)             202
//	POST   /v1/sessions/{session_id}/abort    AbortSessionRequest   202
//	DELETE /v1/sessions/{session_id}          (no body)             204
//	POST   /v1/messages                       Envelope              202
//
// On the ORC, called by the CC:
//
//	POST   /v1/sessions/{session_id}/status   StatusReport          204
//	POST   /v1/messages                       Envelope              202
//
// Errors are a Problem (RFC 9457).
//
// # Session flow
//
//  1. The ORC sends StartSessionRequest to both CCs, with opposite roles.
//  2. Each CC loads its dataset, applies the prep steps, checks and opens the session, and
//     reports StateReady, or StateRejected (the session is not opened).
//  3. When both CCs are ready, the ORC sends run to both.
//  4. The PSI frames travel as Envelope values: CC to ORC, then ORC to the other CC.
//  5. Each CC reports StateDone or StateFailed. The ORC then deletes the session on both CCs.
//
// # Rules
//
// Field names are snake_case. Decode refuses invalid UTF-8, null, unknown fields, wrong types,
// invalid base64, trailing data and any value that breaks the rules of the type. Every error
// wraps ErrInvalid and never contains message values. Reports never carry items, match counts,
// item counts or free text.
package orcapi
