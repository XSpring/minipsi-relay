# ADR-0009: A port layer between business services and the PSI core

Source references are at the commit that adds `internal/psi`; if lines drift, search for the
symbol.

## Status

Proposed (issue #9). The interfaces are added first; the adapter over the PSI core follows in
later changes.

## Context

- Business services must run PSI sessions. Their controllers will use HTTP first and gRPC
  later, so the service code must not depend on a wire protocol.
- The only entry points today are `protocol.RunReceiver`, `protocol.RunSender` and
  `protocol.Conn`, plus the gRPC transport. They expose core types (frames, `hashing.Alg`,
  `protocol.Config`, instance indices) and assume a direct connection between the parties.
- The parties (CC1 and CC2) must not connect directly. Their messages travel over plain HTTP
  through the orchestrator (ORC): the ORC pushes inbound messages to a CC, and the CC posts
  outbound messages to the ORC. No message crypto is used at this stage.
- The ORC assigns the session ID. A session holds one set per party, and the receiver needs
  the matching values, not indices.
- The core constrains any message carrier: frames in each direction must arrive in order with
  no loss and no duplicates (ADR-0006), one goroutine sends and one receives, an Abort can
  follow a cancellation, the handshake sends Hello and Sizes before it reads, frames are
  chunked to `MaxFrame - 1024`, and errors before the session starts send no Abort.

## Decision

- Package `internal/psi` is the port layer. Services import only this package, and it imports
  only the standard library (checked by a test), so services cannot reach core types.
- `Engine` has four methods: `Check` validates items before a session is requested;
  `OpenReceiver` and `OpenSender` register the ORC's session ID so that the peer's first
  messages are buffered before `Run`; `Deliver` takes one inbound message pushed by the ORC.
- `ReceiverSession` and `SenderSession` have `Run` (called once, blocking, cancelled through
  the context) and `Close` (idempotent; tells the peer to stop when `Run` never ran).
- The service implements `Outbox.Post` to send one message to the ORC. The layer calls it in
  sequence order, one call at a time, and retries the same message after a failure.
- A `Message` is a struct with JSON tags: `session_id` (checked by `ValidSessionID`: 1 to 128
  ASCII letters, digits, `-`, `_`, `.` or `:`), `seq` (per sender, from 1, no gaps), `kind`
  and an opaque `payload`. Services read only the routing fields and forward the payload byte
  for byte. The payload format belongs to the adapter.
- The layer keeps each direction in order. While a session is open, an exact duplicate is
  accepted again, a conflicting one is refused and ends the session, and a message ahead of
  its sequence is refused with a retryable error. After the session has ended, every message
  for it is refused as closed. There is no reorder buffer.
- `Check` and `Run` drop exact duplicate items first, for both roles, and apply the limits to
  the distinct items. On a local failure, including one before the first message, `Run` tells
  the peer to stop (best effort), so the peer does not wait for its handshake timeout.
- Failures are `*psi.Error` values whose `Kind` is one of eight sentinel errors, a finer split
  of the CLI exit code classes (`exitCode` at `internal/cli/root.go:38`). `Unwrap` returns
  only `Kind`, so services cannot depend on the adapter's error types. Six more sentinel errors
  describe registry and message refusals and tell the caller whether to retry.
- `ReceiverResult.Matches` holds the distinct matching input values; `Stats` holds no items,
  field elements, scalars or protocol outputs.

## Consequences

- Services and their tests depend on a small, stable set of interfaces, and the core can be
  replaced or wrapped differently without changing them.
- The orchestrator must retry a message that `Deliver` refused with a retryable error for at
  least the handshake timeout, keep each direction of a session in order, forward payloads
  byte for byte without re-encoding them, assign valid session IDs, and accept bodies of up to
  `MaxMessageBytes` (8 MiB).
- Without message crypto, the orchestrator can read and rewrite every message; the transcript
  digest has no key and does not prevent consistent rewriting. A separate security review is
  needed before production use.
- Both parties send Done before checking the peer's Done, so an Abort can still arrive after
  the receiver returned its result.
- The handshake and phase timeouts must cover two orchestrator hops, the peer's computation,
  and the time between the peer's `Open` and its `Run`, because a party sends its first
  message only when `Run` starts.
- A process restart loses its sessions; there is no resume.

## Evidence

- `internal/psi/psi.go:18` (`ValidSessionID`) and `internal/psi/psi.go:67` (`Message`):
  routing fields and opaque payload.
- `internal/psi/psi.go:82` (`Outbox`): implemented by the service.
- `internal/psi/psi.go:87` (`Engine`): `Check`, `OpenReceiver`, `OpenSender`, `Deliver`.
- `internal/psi/psi.go:123` (`ReceiverSession`) and `internal/psi/psi.go:140` (`SenderSession`).
- `internal/psi/errors.go:10` (session failure kinds) and `internal/psi/errors.go:37` (registry
  and message errors); `internal/psi/errors.go:55` (`Error`).
- `internal/protocol/conn.go:12` (`Conn`): the ordered frame stream the adapter implements.
- `internal/protocol/messages.go:201` (`frameReserve`): room kept in every frame.
- `internal/protocol/session.go:362` (`handshake`): Hello and Sizes are sent before reading.
- `internal/protocol/padding.go:99` (`CheckInput`) and `internal/protocol/errors.go:97`
  (`AbortConn`): pre-session checks and aborts that the CLI uses today.

Related: [ENTRY_POINTS.md](../ENTRY_POINTS.md#seam), [ANALYSIS_GAPS.md](../ANALYSIS_GAPS.md),
[ADR-0006](0006-transcript-digest-in-done.md).
