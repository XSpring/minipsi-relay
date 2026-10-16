# ADR-0005: Size and timing leak mitigations in the protocol

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

## Status

Accepted (recorded retroactively from repository evidence; original date and alternatives
considered are unknown).

## Context

The receiver must learn only the intersection and the sender must learn nothing about it. Besides
message contents, set sizes and the timing of messages can reveal information: the receiver
announces its point count, the sender sends one output per item, and the receiver's matching
work grows with the intersection size.

## Decision

- **Receiver finishes the session before matching.** It sends Done and waits for the peer's Done
  before running the match, so the duration of matching is not observable by the sender. The CLI
  also closes the session before writing the result file.
- **Receiver padding.** The receiver interpolates its distinct items plus random dummy points,
  `m = roundUp(max(k + Extra, Min), Multiple)` with `m >= 2` when `k >= 1`, and announces `m`
  instead of `k`. The default adds one point.
- **Sender dummy outputs.** Optional sender padding adds dummy evaluation points drawn uniformly
  from F that go through the same evaluation and elliptic-curve pipeline as real items. The
  default adds none.
- **Shuffled outputs.** The sender shuffles real and dummy positions per instance, so outputs are
  sent in permuted order.
- **Role-local policies.** `padding` belongs to the receiver and `sender_padding` to the sender;
  configuration validation rejects the other role's policy when it differs from the default.

## Consequences

- With the default receiver policy the sender still learns `k = m - 1`; hiding sizes needs
  `--pad-min` or `--pad-multiple`, at the cost of more computation and bandwidth.
- With default sender padding the receiver learns the sender's raw item count.
- Padding is never negotiated: each side decides its own, and a misplaced setting fails fast
  instead of being silently ignored.
- Some costs still scale with the distinct item count before Done (inferred from the code, not
  stated in it); see [DOMAIN.md](../DOMAIN.md).

## Evidence

- `internal/protocol/receiver.go:134-138` (`Done`): "Finish the session before matching".
- `internal/cli/party.go:302-304` (`closeAll`): close before writing the result.
- `internal/protocol/padding.go:8-15` (`PadPolicy`): padding formula, announced `m`, floor of 2.
- `internal/protocol/padding.go:22-30` (`DefaultPadPolicy`): defaults for both roles.
- `internal/protocol/sender.go:130-134` (`prepare`): dummies through the same pipeline; the shuffle hides their position.
- `api/minipsi/v1/psi.proto:75-77` (`OutputPart`): values in permuted order.
- `internal/config/config.go:209-213` (`sender_padding`): cross-role rejection.

Related: [BUSINESS_FLOWS.md](../BUSINESS_FLOWS.md#workflow-receiver-computation),
[BUSINESS_FLOWS.md](../BUSINESS_FLOWS.md#workflow-sender-computation).
