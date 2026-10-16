# ADR-0001: One symmetric bidirectional gRPC stream, roles in Hello

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

## Status

Accepted (recorded retroactively from repository evidence; original date and alternatives
considered are unknown).

## Context

A PSI run has two roles, receiver and sender, that exchange several message kinds in both
directions: Hello and sizes from both sides, keys and outputs from the sender, polynomial
coefficients from the receiver, and Done or Abort from either side. Deployments differ in which
host can accept inbound connections, and the daemon mode (`serve`) needs the sender to listen
while the requester dials in.

## Decision

- The wire API is one service `PSI` with one RPC `Session(stream Frame) returns (stream Frame)`.
  Every message is a `Frame` with a `oneof` body, in both directions.
- The role is a field of `Hello`, not a property of the RPC. Either role may be the gRPC client
  or the gRPC server: `--listen` or `--connect` is chosen independently of the role.
- Because there is one message type in both directions, the buf lint rules about request and
  response naming and the service suffix are disabled.
- The transport package exposes the stream as a `protocol.Conn`, so the protocol code does not
  depend on gRPC.

## Consequences

- Operators pick the direction of the TCP connection to fit firewalls, independent of who learns
  the intersection.
- Ordering and legality of frames are enforced by the protocol state machines, not by the gRPC
  schema; an empty or unknown body is converted to an empty frame and rejected there.
- Shutdown must work in both directions. For example the receiver closes the session before writing
  its result, because a sender acting as the gRPC client waits for that close.
- Adding a message kind means a new `oneof` field plus converter, digest and state-machine
  changes (see [ADR-0006](0006-transcript-digest-in-done.md)).

## Evidence

- `api/minipsi/v1/psi.proto:8-11` (`PSI`): comment "symmetric bidirectional frame stream; the roles are announced in Hello".
- `api/minipsi/v1/psi.proto:38-41` (`Hello`): `role` field of Hello.
- `buf.yaml:7-14` (`SERVICE_SUFFIX`): lint exceptions and their reason.
- `internal/transport/transport.go:1-5` (`transport`): package doc, one bidi stream exposed as `protocol.Conn`.
- `internal/cli/party.go:356-368` (`ServerConfig`): listen or dial chosen from `Listen`, not from the role.
- `internal/transport/convert.go:47-49` (`fromPB`): empty or unknown body becomes an empty frame.
- `internal/cli/party.go:302-304` (`closeAll`): receiver closes first when the sender is the gRPC client.

Related: [INTEGRATIONS.md](../INTEGRATIONS.md#peer-to-peer-grpc),
[CALL_GRAPHS.md](../CALL_GRAPHS.md#cg-05-transport-listen-accept-dial-stream-and-tls).
