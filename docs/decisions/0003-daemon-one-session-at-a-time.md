# ADR-0003: The daemon serves one session at a time and hides dataset existence

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

## Status

Accepted (recorded retroactively from repository evidence; original date and alternatives
considered are unknown).

## Context

`minipsi serve` keeps running and answers `minipsi request` sessions as sender for named
datasets. A session is CPU- and memory-heavy. Requesters must not learn which datasets exist
unless they are allowed to use them, and one failing requester must not take the service down.

## Decision

- The transport server in `Repeat` mode accepts one session at a time. A request that arrives
  while a session runs gets `Unavailable` with a fixed "busy" message, and the client retries.
  `request` therefore defaults to a 2-minute connect-retry window.
- An `Admit` hook vets each request before it takes the session slot. An unknown dataset and a
  peer outside `allowed_peers` get the same `FailedPrecondition` answer, "dataset not available",
  which the client does not retry. A dataset whose file is missing is reported as unavailable,
  with the detail logged only locally.
- All datasets are loaded and checked at start-up (fail fast), and each session reads its
  dataset again, so file updates take effect without a restart. A load failure during a session
  sends the peer only a generic input abort.
- A failed session is logged and reported but never stops the daemon; only cancellation (SIGINT
  or SIGTERM) or a listener failure ends it.

## Consequences

- Throughput is one session at a time per daemon; concurrent requesters queue by retrying.
- A requester cannot tell "no such dataset" from "not allowed".
- Per-session reports in `state_dir/reports/` are the audit trail, including failed sessions.
- Each session pays the cost of reading the dataset again.

## Evidence

- `internal/cli/serve.go:25-26` (`newServeCmd`): one session at a time.
- `internal/transport/transport.go:57-62` (`Repeat`): busy refusal and the `Admit` contract.
- `internal/transport/transport.go:418-423` (`busyMsg`): session slot and busy answer.
- `internal/cli/party.go:129-131` (`requestConnectRetry`): 2-minute retry default.
- `internal/cli/serve.go:131-142` (`Rejection`): "Same answer for unknown and not-allowed".
- `internal/cli/serve.go:110-116` (`loadInput`): fail fast at start-up.
- `internal/cli/serve.go:180-185` (`AbortConn`): per-session reload and generic abort.
- `internal/cli/serve.go:102-103` (`runServe`): failed sessions never stop the daemon.

Related: [BUSINESS_FLOWS.md](../BUSINESS_FLOWS.md#workflow-serving-datasets-and-answering-a-session-request),
[RUNBOOK.md](../../RUNBOOK.md#4b-daemon-mode-serve-and-request).
