# ADR-0010: The HTTP API between a compute cluster and the orchestrator

Source references are at the commit that adds `internal/orcapi`; if lines drift, search for the
symbol.

## Status

Proposed. The DTOs and endpoints are defined first; the handler and the service that use them
follow in later changes.

## Context

- In relay mode the two compute clusters (CC) never connect: every message goes through the
  orchestrator (ORC) over plain HTTP, without message crypto and without retries for now.
- The ORC spec asks for directives from the ORC (contract reference, peer CC, role, abort,
  tear-down) and status from the CC.
- The PSI port (`internal/psi`) defines how one protocol message travels (`psi.Message`), but not
  how a session starts, which data a CC uses, how it prepares it, how a CC reports its state, or
  which CC sent a relayed message.

## Decision

- Package `internal/orcapi` holds every DTO, the endpoint patterns, the body limits and the
  validation rules. It imports only the standard library (checked by a test).
- ORC to CC: `StartSessionRequest` (session ID, own and peer CC IDs, role, contract reference,
  dataset name, prep steps), `AbortSessionRequest` (reason), and run and delete without a body.
- The dataset is a name in the CC's local catalog. The prep steps (`trim`, `nfc`, `lower`, in
  order) are part of the request, so both CCs receive the same rules.
- Relay: `Envelope` is `psi.Message` with the same JSON names plus the sender and receiver CC
  IDs, which the ORC needs to route messages.
- CC to ORC: `StatusReport` with the states ready, rejected, done and failed, and an error
  (kind, code, remote) or stats (protocol SID, message counts, duration).
- The ORC sends run only after both CCs reported ready, so both have opened the session before
  the first protocol message.
- Errors are RFC 9457 problems with a `code` member. `Decode` follows the strict rules of the
  frame codec, and every error wraps `ErrInvalid` without message values.

## Consequences

- Both teams have one written contract, with example messages in `internal/orcapi/testdata`.
- Strict decoding means a new field needs the receiving side upgraded first.
- The CC IDs are self-declared until the CCs authenticate to the ORC. A security review is
  needed before production use.
- Reports carry no items, match counts or item counts (ADR-0005).

## Open questions for the ORC team

- Keep `seq` and `kind`, or use the spec's `envelope_seq` and `protocol_phase`? Is
  `payload_digest` needed?
- Does the ORC check `seq` before forwarding? When are CCs authenticated (mTLS, token)?
- Who decides `prep`, who maps a contract to dataset names, and how does the result reach the
  data owner?

## Evidence

- `internal/orcapi/control.go:29` (`StartSessionRequest`) and `internal/orcapi/control.go:90`
  (`AbortSessionRequest`).
- `internal/orcapi/relay.go:13` (`Envelope`) and `internal/orcapi/status.go:39`
  (`StatusReport`).
- `internal/orcapi/problem.go:18` (`Problem`) and `internal/orcapi/decode.go:23` (`Decode`).
- `internal/orcapi/paths.go:5` (endpoint patterns).
- `internal/config/serve.go:65` (`ValidDatasetName`): the dataset name rule.

Related: [ADR-0005](0005-size-and-timing-leak-mitigations.md),
[internal/orcapi/README.md](../../internal/orcapi/README.md).
