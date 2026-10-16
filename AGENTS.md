# Project rules

Rules for everyone who changes this repository, people and AI agents alike. Claude Code loads
this file through CLAUDE.md; other agents read it directly.

## Project

minipsi is a Go CLI for semi-honest polynomial-based Diffie-Hellman private set intersection
between two hosts over gRPC with mutual TLS 1.3. Only the receiver learns the intersection.
Usage is in README.md, operations in RUNBOOK.md, and the technical documentation in docs/.

One static binary is built from `cmd/minipsi`; all code lives under `internal/` (no public Go
API). Module `github.com/XSpring/minipsi`, Go 1.26 in go.mod, CI and Docker build with Go 1.27.2.

| Path | Contents |
|---|---|
| `cmd/minipsi` | `main`, calls `cli.Run` |
| `internal/cli` | cobra commands (`receiver`, `sender`, `request`, `serve`, `certs dev`, `bench`, `version`), exit codes |
| `internal/config` | defaults, strict YAML loading, validation |
| `internal/protocol` | the PSI protocol: session, handshake, receiver, sender, frames, error codes |
| `internal/transport` | gRPC over mutual TLS 1.3, one bidi stream per session |
| `internal/field`, `hashing`, `group`, `elligator` | GF(2^255-19), hashes, edwards25519 group, Elligator2 |
| `internal/ntt`, `rns`, `poly`, `parallel` | NTT, RNS multiplication, polynomial interpolation and evaluation, worker loops |
| `internal/itemio`, `report`, `devcerts`, `version` | input/output files, JSON reports, test certificates, build info |
| `api/minipsi/v1` | protobuf wire schema; generated code in `internal/gen` (never edit by hand) |
| `test/e2e`, `scripts/e2e.sh` | end-to-end tests with real processes |

## Documentation

Read the relevant document before changing a major workflow, and update it in the same change
when behaviour, flags, configuration or architecture change.

| Topic | Document |
|---|---|
| Architecture, package boundaries, concurrency | docs/ARCHITECTURE.md |
| PSI concepts, rules, error and exit codes | docs/DOMAIN.md |
| End-to-end workflows | docs/BUSINESS_FLOWS.md |
| Entry points and call graphs | docs/ENTRY_POINTS.md, docs/CALL_GRAPHS.md, docs/SUMMARY.md |
| Flags, YAML keys, environment variables | docs/CONFIGURATION.md |
| Tooling, tests, CI, pitfalls | docs/DEVELOPMENT.md |
| gRPC, TLS, filesystem and other integrations | docs/INTEGRATIONS.md |
| Architecture decisions | docs/decisions/README.md |
| Deployment and operations | RUNBOOK.md |

## Build and test

| Task | Command |
|---|---|
| Build | `make build` (static binary in bin/minipsi) |
| Try it | `go run ./cmd/minipsi bench` |
| Unit tests | `make test` (full sizes) and `make race` (what CI runs) |
| Lint | `make tools` once, then `make lint` (gofmt, vet, staticcheck) |
| Vulnerabilities | `make vuln` (needs `make tools`) |
| End-to-end | `make e2e` |
| Fuzz smoke | `make fuzz-smoke` |
| Protobuf | `make proto`, then `make proto-check` |

Run the tests that cover your change before you push. CI must pass before a merge.

Definition of done for a code change:

- `go test ./...` passes; `make race` passes when concurrency is touched.
- `gofmt -l cmd internal` prints nothing and `go vet ./...` is clean.
- `make e2e` when `internal/cli`, `internal/config`, `internal/transport` or `internal/itemio` change.
- `make fuzz-smoke` when a parser or decoder changes (itemio, field, group, elligator, frame handling).
- `make proto-check` when `api/` changes.
- New behaviour gets a test next to the code (`*_test.go` in the same package).

## Code conventions

These are established by the existing code; follow them.

- Errors: protocol failures are `*protocol.Error` with a wire `Code` (sent to the peer as an
  Abort) or `*protocol.TransportError`; the CLI maps errors to exit codes 0-5 in
  `internal/cli/root.go`. A new failure path must map to the right exit code (see
  docs/DOMAIN.md).
- Configuration: defaults live in `config.Default` and `config.DefaultServe`; unknown YAML keys
  are errors; an explicitly set flag overrides the file; `Validate` collects all problems with
  `errors.Join` and does not touch the filesystem. A new option needs a default, validation,
  a flag binding and an entry in docs/CONFIGURATION.md.
- Privacy: never log or report items, field elements, scalars or protocol outputs, and keep
  them out of error messages (`TestNoSecretsLogged`, `TestErrorsDoNotLeakItems`). Messages sent
  to a peer must not reveal local details.
- Wire format: `protocol.Frame` mirrors `api/minipsi/v1/psi.proto` and
  `internal/transport/convert.go` maps between them; the transcript encoding in
  `internal/protocol/messages.go` must follow any change. Both peers check
  `version.ProtocolVersion` and `version.Suite` in the handshake.
- Security: keep TLS 1.3 with required client certificates; keep `--insecure-dev` gated by
  `MINIPSI_I_UNDERSTAND_INSECURE=1` and `--dev` limited to loopback addresses.
- Concurrency: `protocol.Conn` methods take a context and must return when it is done; CPU work
  uses `parallel.For`; randomness comes from `crypto/rand` (tests inject `Config.Rand`).

## Scope and dependencies

- Change only what the task needs: no unrelated refactoring, renaming or reformatting.
- Do not add a dependency unless it is clearly needed; tool versions are pinned in the
  Makefile, CI actions by commit SHA and base images by digest. Bump them deliberately.
- Never commit build outputs, certificates, keys or reports (see .gitignore).

## Language and characters

- Write code comments, docs, commit messages, pull requests and issues in English.
- Use plain ASCII in branch names, commit messages, and pull request and issue titles and
  bodies: no emoji, no arrows or other symbols.

## Agent attribution

Commits and pull requests made with an AI agent follow these rules:

- End the commit message and the pull request body with this line, after a blank line:
  `Supported by minipsi-relay agent`
- Never add tool attribution such as `Generated with Claude Code`, or a `Co-Authored-By:`
  trailer for an AI model.

## Branches

Format: `<module>/<feature>`

- `<module>` is the area you change: a package under internal/ (for example cli, config,
  transport, protocol, poly, rns), or one of api, docs, ci, build, agents.
- `<feature>` is a short description in lower-case kebab-case.
- Examples: `transport/relay-conn`, `docs/entry-point-call-graph-analysis`,
  `agents/project-rules`.
- Branch from an up-to-date main. Never commit directly to main.

### Short-lived branches

- A branch goes from creation to an open pull request, and then to merge, within 24-48 hours.
  This is a maximum, not a target: open the pull request as soon as the task is done.
- One branch holds the smallest task that can be implemented, tested and reviewed on its own.
- If a task cannot be finished within 24-48 hours, split it into smaller tasks first, each with
  its own branch and pull request. Every step must leave main building with tests passing.
- Do not mix unrelated features, fixes or refactoring in one branch. Anything the task does not
  need goes into a separate branch.
- Finish the change, run the tests that cover it, push and open the pull request early. Do not
  leave a branch idle; merge it or close it.
- AI agents: before writing code, estimate the scope of the task. If it does not fit into one
  short-lived branch, propose a breakdown (tasks, order, branch names) and wait for agreement
  before implementing.

## Commits

Format: `<type>(<module>): <summary>`

- `<type>` is one of feat, fix, docs, refactor, test, perf, build, ci, chore.
- `<module>` is the same module as in the branch name.
- `<summary>` is imperative and lower case, with no trailing period, and the whole first line
  is at most 72 characters.
- Example: `feat(api): add session api`.
- Add a body after a blank line when the reason for the change is not obvious.
- Keep one logical change per commit.
- Never amend or force-push a commit that is already pushed. Make the fix in a new commit.

## Pull requests

- Title: the commit format, `<type>(<module>): <summary>`.
- Base branch: main.
- Body: the sections of .github/pull_request_template.md (Summary, Changes, Testing, Related
  issues).
- Keep each pull request focused; split unrelated changes into separate pull requests.
- Write `Closes #<number>` when the pull request resolves an issue.
- Merge only after CI passes and a reviewer approves.

## Issues

- Title: the commit format, for example `fix(transport): retry when the daemon is busy`.
- Use the templates in .github/ISSUE_TEMPLATE (bug report, feature request).
- Labels by type: fix uses bug, feat uses enhancement, docs uses documentation; add others
  when they apply.
- A bug report includes steps to reproduce, expected and actual behaviour, the output of
  `minipsi version`, and logs.

## Agent workflow

The skill in .claude/skills/git-workflow/SKILL.md walks an agent through branches, commits,
pull requests and issues with these rules.

## Open questions

- `mode: relay` (via an "Orchestrator") is reserved and rejected by validation; its design is
  not in this repository.
- Release and versioning process (tags, changelog) is not documented.
