# ADR-0007: Strict configuration: unknown keys fail, explicit flags override

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

## Status

Accepted (recorded retroactively from repository evidence; original date and alternatives
considered are unknown).

## Context

Both parties must agree on some settings (hash, lambda) and each has role-specific ones
(padding policies, output). A misspelled YAML key or a setting meant for the other role would
otherwise be silently ignored and could weaken privacy or produce wrong results.

## Decision

- YAML files are decoded with `KnownFields(true)`: unknown keys are errors, for party configs and
  for the daemon config.
- Precedence is defaults, then the YAML file, then flags; a flag overrides the file only when it
  was set explicitly on the command line (`Changed`).
- `memory_limit` remembers whether it was set explicitly, so an explicit value beats
  `GOMEMLIMIT`, which beats the default.
- Validation collects all errors and reports them together. A config file for another role, a
  receiver setting on the sender (or the reverse), and request settings outside `request` are
  rejected. All of these exit with code 1.

## Consequences

- Typos fail fast, before any input is read or any connection is made.
- Configuration files cannot carry unrelated keys or comments as keys; old keys must be removed
  when they are renamed.
- Default values on flags do not mask values from the file.

## Evidence

- `internal/config/config.go:128-135` (`KnownFields`): party config, unknown keys are errors.
- `internal/config/serve.go:77-90` (`KnownFields`): daemon config.
- `internal/cli/party.go:154` (`config`): "flags override it".
- `internal/cli/party.go:163-171` (`Changed`): role check and explicit-flag overlay.
- `internal/cli/serve.go:64-68` (`Changed`): explicit-flag overlay for `serve`.
- `internal/config/config.go:148-153` (`MemoryLimitExplicit`): explicit memory limit vs `GOMEMLIMIT`.
- `internal/config/config.go:209-213` (`sender_padding`): role-local padding.
- `internal/cli/party.go:175-176` (`request`): request settings only for `request`.
- `RUNBOOK.md:56` (`unknown keys are errors`): operator documentation.

Related: [CONFIGURATION.md](../CONFIGURATION.md),
[BUSINESS_FLOWS.md](../BUSINESS_FLOWS.md#workflow-configuration-resolution-and-validation).
