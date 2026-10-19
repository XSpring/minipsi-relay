# internal/psi: the PSI port layer

This folder is the only part of the PSI code that business services should import. It describes
*what* a service can ask for (run a PSI session, get the result) without saying *how* the PSI is
computed. The decision behind it is recorded in
[ADR-0009](../../docs/decisions/0009-psi-port-layer.md).

Status: this package contains interfaces and types only. The first implementation, the adapter
`internal/psi/core` over the existing PSI core (`internal/protocol`), is planned and not written
yet.

## In one minute

- `internal/psi` is the **port**: a set of Go interfaces and types, the contract between services
  and any PSI implementation.
- `internal/psi/core` will be the **adapter**: it implements those interfaces by calling the
  existing PSI core in `internal/protocol`.
- Services depend only on the port. To use another PSI implementation later, write another
  adapter; the services do not change.

## The layers

```mermaid
flowchart TD
    C["Controller (HTTP now, gRPC later)"] --> S["Service, for example psi_service.go"]
    S -->|"calls Engine, ReceiverSession, SenderSession"| P["internal/psi (port: interfaces and types)"]
    A["internal/psi/core (adapter, planned)"] -.->|implements| P
    S -.->|"implements Outbox (HTTP client to the orchestrator)"| P
    A -->|calls| K["internal/protocol (existing PSI core)"]
```

Read the arrows like this:

- The service calls the interfaces of the port. It never sees the adapter or the core.
- The adapter implements those interfaces and calls the core.
- The service also implements one interface of the port, `Outbox`, because the PSI needs a way
  to send messages out and only the service knows how to reach the orchestrator.

The same idea in a more familiar shape:

| Usual app | PSI |
|---|---|
| `UserService` | your service, for example `psi_service.go` |
| `UserRepository` (interface) | `internal/psi` (port) |
| `PostgresUserRepository` (implementation) | `internal/psi/core` (adapter) |

## Files in this folder

| File | What it holds |
|---|---|
| `psi.go` | The interfaces `Engine`, `ReceiverSession`, `SenderSession` and `Outbox`; the `Message` struct; `Role`; `ValidSessionID` and the size limits |
| `errors.go` | The error values a service checks with `errors.Is`, and the `Error` type |
| `result.go` | What `Run` returns: `ReceiverResult`, `SenderResult`, `Stats` |
| `doc.go` | The package documentation (`go doc ./internal/psi`) |
| `psi_test.go` | Tests for the types, and a test that this package imports only the standard library |

Go imports a package, not a file: `import "github.com/XSpring/minipsi/internal/psi"` gives a
service everything listed above.

## What a service uses

| Name | Kind | Who implements it | What it is for |
|---|---|---|---|
| `Engine` | interface | the adapter | `Check` the items, `OpenReceiver` or `OpenSender` a session, `Deliver` an inbound message |
| `ReceiverSession`, `SenderSession` | interface | the adapter | `Run` the session once and get the result; `Close` it |
| `Outbox` | interface | the service | `Post` one outbound message to the orchestrator |
| `Message` | struct | - | one protocol message: `session_id`, `seq`, `kind`, `payload` (JSON) |
| `ReceiverResult`, `SenderResult`, `Stats` | struct | - | the result of `Run` |
| `ErrTimeout`, `ErrBusy`, ... and `Error` | errors | - | what went wrong |

## How a session runs

The two parties (CC1 and CC2) never talk to each other directly. Every message goes through the
orchestrator (ORC) over plain HTTP: the ORC POSTs inbound messages to a CC, and the CC POSTs its
outbound messages to the ORC.

```mermaid
sequenceDiagram
    participant R as CC1 service (receiver)
    participant O as Orchestrator
    participant S as CC2 service (sender)
    R->>R: Engine.Check(RoleReceiver, items)
    O-->>R: session_id
    O-->>S: session_id
    R->>R: Engine.OpenReceiver(session_id, outbox)
    S->>S: Engine.OpenSender(session_id, outbox)
    R->>R: session.Run(ctx, items)
    S->>S: session.Run(ctx, items)
    loop every protocol message
        R->>O: Outbox.Post(message)
        O->>S: POST, then Engine.Deliver(message)
        S->>O: Outbox.Post(message)
        O->>R: POST, then Engine.Deliver(message)
    end
    R->>R: Run returns the matching items
    S->>S: Run returns stats only
```

Steps for one party:

1. `Engine.Check(role, items)`: reject bad input before asking the ORC for a session.
2. Get a `session_id` from the ORC.
3. Create a new `Outbox` for this session, then call `Engine.OpenReceiver(id, outbox)` or
   `Engine.OpenSender(id, outbox)` as soon as the ID is known. From now on, messages from the
   peer are kept until `Run` starts.
4. `session.Run(ctx, items)`: blocks until the session ends. Call it promptly, because the
   peer is already waiting. Cancel `ctx` to stop it.
5. `session.Close()`: always, for example with `defer`. If `Run` was never called, `Close` also
   tells the peer to stop waiting.

Meanwhile, the HTTP handler that receives the ORC's POSTs passes each message unchanged to
`Engine.Deliver`.

Example:

```go
type MatchService struct {
	engine psi.Engine // given by main; the service does not know which implementation it is
	// client is created once in main and shared by all sessions: it reuses connections and
	// is safe for concurrent use.
	client *http.Client
	orcURL string // where outbound messages are posted
	// newSession asks the orchestrator for a session ID (the service's own HTTP call).
	newSession func(ctx context.Context) (string, error)
}

func (s *MatchService) RunReceiver(ctx context.Context, items [][]byte) ([][]byte, error) {
	// 1. Check first: no session exists yet, so a failure needs no cleanup and the peer is
	// never involved.
	if err := s.engine.Check(psi.RoleReceiver, items); err != nil {
		return nil, err
	}
	// 2. Only now ask the orchestrator for a session.
	sessionID, err := s.newSession(ctx)
	if err != nil {
		return nil, err
	}
	// 3. Create an Outbox for this session and open at once, so that the peer's first
	// messages are kept until Run starts.
	out := &orcOutbox{client: s.client, url: s.orcURL}
	sess, err := s.engine.OpenReceiver(sessionID, out)
	if err != nil {
		return nil, err
	}
	// 5. Always close; if Run never ran, Close tells the peer to stop waiting.
	defer sess.Close()
	// 4. Run the session.
	res, err := sess.Run(ctx, items)
	if err != nil {
		return nil, err // for example errors.Is(err, psi.ErrTimeout)
	}
	return res.Matches, nil
}

// OnMessage is called by the HTTP handler for every message the orchestrator pushes.
func (s *MatchService) OnMessage(ctx context.Context, m *psi.Message) error {
	return s.engine.Deliver(ctx, m)
}

// orcOutbox posts the messages of one session to the orchestrator. Its fields never change
// after it is created, and Post keeps everything else in local variables, so it would also be
// safe if it were shared by several sessions.
type orcOutbox struct {
	client *http.Client
	url    string
}

func (o *orcOutbox) Post(ctx context.Context, m *psi.Message) error {
	body, err := json.Marshal(m) // a new body for every call
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return err // ends the session (no retries in this version)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("orchestrator: status %d", resp.StatusCode) // ends the session
	}
	return nil // accepted
}
```

## Handling errors

Errors come in two groups.

**Session failures**, returned by `Check` and `Run` as an `*psi.Error`. Check them with
`errors.Is`:

| Error | Meaning |
|---|---|
| `ErrInput` | the local items were rejected (too many, too large, a hash collision) |
| `ErrMismatch` | the two parties use different parameters |
| `ErrProtocol` | the peer broke the protocol |
| `ErrPeerAborted` | the peer stopped the session; `Error.Code` says why |
| `ErrTimeout` | a peer message did not arrive in time |
| `ErrDelivery` | messages could not be posted to the orchestrator |
| `ErrCanceled` | the context was canceled or the session was closed |
| `ErrInternal` | a local failure of the engine |

**Registry and message errors**, returned by `OpenReceiver`, `OpenSender` and `Deliver`:

| Error | Meaning |
|---|---|
| `ErrBusy` | too many sessions are open |
| `ErrSessionExists` | a session with this ID is already open |
| `ErrUnknownSession` | no session with this ID is open (not opened yet, or already ended) |
| `ErrMalformed` | the message or session ID is invalid, or the message is out of sequence; this also ends an open session |

This version has no retries: a failed `Post` or a message that `Deliver` refuses ends the
session. The orchestrator must therefore deliver every message exactly once and in order, and
only after the receiving CC has opened the session. Retries may be added in a later version.

## Design patterns used

| Pattern | Where | Why |
|---|---|---|
| **Ports and Adapters** (hexagonal architecture) | Port: `internal/psi`. Adapter: `internal/psi/core` (planned). `Outbox` is a port in the other direction: the core calls out, the service implements it. | Services depend on a small, stable contract; the PSI implementation can be replaced without touching them. |
| **Service Layer** | Your service, for example `psi_service.go`, between the controller and `psi.Engine` | Keeps HTTP or gRPC details in the controller and PSI details behind the port. |
| **Dependency Injection** (composition root) | `main` creates the adapter (`core.New`, planned) and passes it to the service as a `psi.Engine`; the service passes its `Outbox` to `OpenReceiver` and `OpenSender`. | Only `main` knows which implementation is used; services and tests can use any `Engine`. |
| **Abstract Factory** | `Engine.OpenReceiver` and `Engine.OpenSender` create `ReceiverSession` and `SenderSession`; the adapter's engine is the concrete factory. | The service never creates a session itself, so it does not depend on the session's concrete type. |
| **Adapter** | `internal/psi/core` (planned): its receiver and sender wrap `protocol.RunReceiver` and `protocol.RunSender`; its connection turns `Outbox.Post` and `Deliver` into the `protocol.Conn` the core expects. | The existing core stays unchanged; only the adapter knows its types. |
| **Facade** | `Engine` and `Run` | They hide the handshake, the core configuration, frame chunking and session bookkeeping behind a few simple calls. |

## Adding another PSI implementation

1. Create a new folder, for example `internal/psi/otherpsi`.
2. Implement `psi.Engine`, `psi.ReceiverSession` and `psi.SenderSession` there, following the
   contracts written in `psi.go`. The message payload format is yours to choose; services
   never read it.
3. In `main`, create the new engine instead of the old one.

The interfaces in `internal/psi` are the standard to follow. `internal/psi/core` is one
implementation of them and can serve as an example. No service code changes.

## Rules

- This package imports only the Go standard library. A test enforces it, so services cannot
  reach core types through it.
- Never log, report or put into error messages the items given to `Run`, the matching items in
  `ReceiverResult.Matches`, or message payloads. Errors and `Stats` from this package contain
  none of them.
- Forward `Message` values unchanged, and never read or modify `Payload`.
