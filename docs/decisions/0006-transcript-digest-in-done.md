# ADR-0006: Transcript digest and frame count in Done frames

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

## Status

Accepted (recorded retroactively from repository evidence; original date and alternatives
considered are unknown).

## Context

Each session is a stream of frames in both directions, and either side ends its part with a Done
frame. The code does not state the purpose of the digest. Inferred: it lets both parties confirm
that they saw the same frame sequence, including both Hellos and their nonces.

## Decision

- Each side keeps one SHA-256 hash per direction over a canonical encoding of every frame it
  sends and receives: a tag byte equal to the protobuf `oneof` field number, then the fields in
  schema order with length prefixes. Done and Abort frames are not part of the transcript.
- The sender of Done fills in `frames_sent` and `transcript_digest` for its direction.
- On receiving the peer's Done, a side compares the count and digest with what it received and
  fails with a protocol error ("transcript mismatch") if either differs.

## Consequences

- A mismatch ends the run with a protocol error even after all data has been exchanged.
- The digest is independent of the protobuf serialisation, so it survives changes to generated
  code, but every new frame kind or field needs a matching canonical encoding.
- The digest uses SHA-256 regardless of the comparison hash chosen with `--hash`.

## Evidence

- `api/minipsi/v1/psi.proto:84-87` (`Done`): `frames_sent` and `transcript_digest`.
- `internal/protocol/messages.go:97-111` (`digestFrame`): tags and canonical encoding.
- `internal/protocol/session.go:194` (`transcript`): one SHA-256 per direction.
- `internal/protocol/session.go:276-280` (`digestFrame`): Done filled in; other frames hashed.
- `internal/protocol/session.go:333-339` (`checkPeerDone`): comparison and error.

Related: [BUSINESS_FLOWS.md](../BUSINESS_FLOWS.md#workflow-session-establishment-parameter-agreement-and-abort),
[ADR-0001](0001-symmetric-bidi-grpc-stream.md).
