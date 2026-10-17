// Package psi is the port layer between business services and the PSI core.
//
// Services import only this package. It defines the interfaces and types for running one
// private set intersection session per Run call, independent of the core that computes it and
// of the HTTP or gRPC layer that exposes the service. An adapter implements Engine over the
// PSI core; the service implements Outbox with its HTTP client to the orchestrator.
//
// # Import rule
//
// This package imports only the standard library, so services never depend on
// internal/protocol, internal/transport or their types (frames, hashing.Alg, protocol.Config,
// instance indices). Adapters live in their own packages and are wired in by the composition
// root.
//
// # Messages
//
// The two parties (CC1 and CC2) never connect directly. Every protocol message of a session is
// a Message that travels through the orchestrator (ORC):
//
//   - outbound: the Engine passes each message to the session's Outbox, in Seq order and one at
//     a time; the service sends it to the ORC (for example with an HTTP POST);
//   - inbound: the ORC pushes each message to the CC, and the service passes it unchanged to
//     Engine.Deliver.
//
// Services read only the routing fields (SessionID, Seq, Kind) and forward Payload as it is.
// The layer keeps each direction in order, drops exact duplicates from retries and rejects
// conflicting ones, so the orchestrator may retry freely. It assumes that the orchestrator
// retries a message that Deliver refused with a retryable error (ErrUnknownSession,
// ErrOutOfOrder, ErrBusy) for at least the handshake timeout, keeps each direction of a session
// in order, forwards Payload byte for byte without re-encoding it, assigns session IDs that
// satisfy ValidSessionID, and accepts bodies of up to MaxMessageBytes.
//
// # Session lifecycle
//
// Call Engine.Check before asking the orchestrator for a session, then Engine.OpenReceiver or
// Engine.OpenSender as soon as the orchestrator has assigned the session ID, so that the peer's
// first messages are buffered until Run starts. Call Run promptly: a party sends its first
// message only when Run starts, and the peer's handshake timeout is already running. Run the
// session once with Run, and call Close when done. Closing a session whose Run was never called
// tells the peer to stop waiting.
//
// # Privacy
//
// Items passed to Run, ReceiverResult.Matches and Message payloads must never be logged,
// reported or put into error messages. Errors and Stats of this package contain none of them.
package psi
