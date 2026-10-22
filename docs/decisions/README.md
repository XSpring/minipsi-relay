# Architecture decision records

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

These records describe design decisions that the repository makes visible: code comments,
schema comments, configuration validation, build files and the CI workflow. They were written
after the fact. The repository has no design history, so each record says only what the code
and its comments show. Where a rationale is not written down, the record says so instead of
guessing, and inferred points are labelled as inferred.

## Index

| ADR | Title | Status | Area |
|---|---|---|---|
| [0001](0001-symmetric-bidi-grpc-stream.md) | One symmetric bidirectional gRPC stream, roles in Hello | Accepted (retroactive) | transport, wire schema |
| [0002](0002-mandatory-mutual-tls-13.md) | Mutual TLS 1.3 is mandatory; plaintext and dev PKI are fenced off | Accepted (retroactive) | security, transport |
| [0003](0003-daemon-one-session-at-a-time.md) | The daemon serves one session at a time and hides dataset existence | Accepted (retroactive) | `serve`, `request` |
| [0004](0004-rns-ntt-pluggable-backend.md) | RNS NTT polynomial arithmetic with a pluggable local backend | Accepted (retroactive) | math core |
| [0005](0005-size-and-timing-leak-mitigations.md) | Size and timing leak mitigations in the protocol | Accepted (retroactive) | protocol, privacy |
| [0006](0006-transcript-digest-in-done.md) | Transcript digest and frame count in Done frames | Accepted (retroactive) | protocol |
| [0007](0007-strict-configuration-loading.md) | Strict configuration: unknown keys fail, explicit flags override | Accepted (retroactive) | configuration |
| [0008](0008-static-binary-pinned-supply-chain.md) | Static binary, distroless image and pinned tools and actions | Accepted (retroactive) | build, packaging, CI |
| [0009](0009-psi-port-layer.md) | A port layer between business services and the PSI core | Proposed | `internal/psi`, services |

## Format

Each record is a file `NNNN-short-title.md` (four-digit number, lower-case words joined by
hyphens) with these sections:

1. **Title** as `# ADR-NNNN: <decision in a few words>`, followed by the commit note.
2. **Status**: one of Proposed, Accepted, Superseded by ADR-NNNN, Deprecated. Records written
   from repository evidence use "Accepted (recorded retroactively from repository evidence;
   original date and alternatives considered are unknown)".
3. **Context**: the forces and constraints that the code or its comments state.
4. **Decision**: what the code does, in the present tense.
5. **Consequences**: effects on operators, peers and maintainers, good and bad.
6. **Evidence**: `path:LINE` references with the symbol in backticks on the same line.

Rules for new records:

- Number records in sequence and never reuse a number. Supersede a record instead of
  rewriting its decision, and link both ways.
- Cite only what the repository shows. Mark anything else as inferred or unknown.
- Keep source references in the form `path:LINE` so `check_docs.py` can verify them.

## Related documents

- [INTEGRATIONS.md](../INTEGRATIONS.md): gRPC, TLS/PKI, filesystem, libraries, packaging.
- [ARCHITECTURE.md](../ARCHITECTURE.md), [DOMAIN.md](../DOMAIN.md),
  [CONFIGURATION.md](../CONFIGURATION.md), [DEVELOPMENT.md](../DEVELOPMENT.md).
- [ENTRY_POINTS.md](../ENTRY_POINTS.md), [CALL_GRAPHS.md](../CALL_GRAPHS.md),
  [BUSINESS_FLOWS.md](../BUSINESS_FLOWS.md).
- [README.md](../../README.md), [RUNBOOK.md](../../RUNBOOK.md).
