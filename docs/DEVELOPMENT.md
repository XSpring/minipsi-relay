# Development guide

How to build, test, regenerate and debug minipsi (module `github.com/XSpring/minipsi`) on a
development machine, and what CI checks on every change.

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

Related documents: [README.md](../README.md#build-and-test) (short build/test summary and
quickstart), [RUNBOOK.md](../RUNBOOK.md) (deployment and exit-code troubleshooting),
[CONFIGURATION.md](CONFIGURATION.md) (every config key, flag and environment variable),
[ARCHITECTURE.md](ARCHITECTURE.md), [DOMAIN.md](DOMAIN.md), [INTEGRATIONS.md](INTEGRATIONS.md),
[decisions/README.md](decisions/README.md), [ENTRY_POINTS.md](ENTRY_POINTS.md),
[BUSINESS_FLOWS.md](BUSINESS_FLOWS.md) and [CALL_GRAPHS.md](CALL_GRAPHS.md).

## 1. Prerequisites

| Tool | Version | Needed for | Notes |
|---|---|---|---|
| Go | go.mod minimum `go 1.26.0`, no `toolchain` line | everything | With the default `GOTOOLCHAIN=auto`, an older installed `go` switches to go1.26.0 inside the repo (`go version` there prints `go1.26.0`). CI and the Docker build use 1.27.2, see [section 11](#11-pitfalls). |
| GNU make | any recent | all `make` targets | The recipes use POSIX `sh` syntax. |
| git | any | `make build` version stamping | `VERSION` and `COMMIT` come from `git describe` / `git rev-parse` (`Makefile:4-5`). |
| C compiler + cgo | system `gcc` or `clang` | `make race` | The race detector needs cgo; `make build` itself uses `CGO_ENABLED=0`. |
| bash, coreutils | bash 4+, `realpath`, `mktemp`, `seq`, `md5sum` | `scripts/e2e.sh` | The script runs with `set -euo pipefail` (`scripts/e2e.sh:5`). |
| `python3` | optional | `scripts/e2e.sh` free-port pick | Without it the script falls back to fixed ports 17443 (`scripts/e2e.sh:12`) and 17444 (`scripts/e2e.sh:71`). |
| Docker | optional | `docker build` | Base images are pinned by digest and need network access. |

The Go minimum is the go directive at `go.mod:3`.

`protoc` itself is not needed: `buf` compiles the `.proto` files and calls the two Go plugins.

## 2. First-time setup

```bash
go mod download   # fetch module dependencies (the Dockerfile does the same)
make tools        # install the pinned dev tools (network required)
```

`make tools` runs `go install` for five pinned tools (`Makefile:72-77`). They land in the go
command's install directory (`$GOBIN` if set, else `$(go env GOPATH)/bin`), and the Makefile
invokes them from `$(GOBIN)`, which defaults to `$(go env GOPATH)/bin` (`Makefile:2`).

| Tool | Pinned version | Make variable | Used by |
|---|---|---|---|
| staticcheck | v0.8.1 | `STATICCHECK_VERSION` (`Makefile:11`) | `make lint` |
| govulncheck | v1.8.0 | `GOVULNCHECK_VERSION` (`Makefile:12`) | `make vuln` |
| buf | v1.73.0 | `BUF_VERSION` (`Makefile:13`) | `make proto` |
| protoc-gen-go | v1.36.12 | `PROTOC_GEN_GO` (`Makefile:14`) | `make proto` |
| protoc-gen-go-grpc | v1.6.2 | `PROTOC_GEN_GO_GRPC` (`Makefile:15`) | `make proto` |

The module of buf v1.73.0 declares `go 1.26.7`, so on Go 1.26.0 to 1.26.6 its `go install` has
to download a newer toolchain (`GOTOOLCHAIN=auto` and network access).

`make test`, `make race`, `make build`, `make bench`, `make fuzz-smoke` and `make e2e` need none
of these tools. `make`, `make all`, `make check`, `make lint`, `make vuln`, `make proto` and
`make proto-check` fail until `make tools` has run.

## 3. Make targets

The default goal is `all: check build` (`Makefile:24`). Every invocation also runs `git describe`,
`git rev-parse` and `date`, because `LDFLAGS` is expanded at parse time (`Makefile:7-8`).

| Target | What it runs | Needs `make tools`? | In CI? |
|---|---|---|---|
| `all` | `check`, then `build` (`Makefile:24`) | yes | no (CI runs the parts as separate jobs) |
| `build` | `CGO_ENABLED=0 go build -trimpath -ldflags ... -o bin/minipsi ./cmd/minipsi` (`Makefile:26-27`) | no | yes, as the prerequisite of `e2e` |
| `test` | `go test ./...`, full sizes (`Makefile:29-30`) | no | no |
| `race` | `go test -race -short ./...` (`Makefile:34-35`) | no | yes (job `test`) |
| `cover` | `go test -coverprofile=coverage.out ./...`, prints the total line (`Makefile:37-38`) | no | no |
| `fmt-check` | `gofmt -l cmd internal`, fails on any listed file (`Makefile:40-42`) | no | yes, via `lint` |
| `lint` | `fmt-check`, `go vet ./...`, staticcheck under `GOTOOLCHAIN=go1.26.0` (`Makefile:44-46`) | yes | yes (job `lint`) |
| `vuln` | `govulncheck ./...` (`Makefile:48-49`); needs network for the vulnerability database | yes | yes (job `vuln`) |
| `check` | `lint`, `race`, `vuln` in that order (`Makefile:51`) | yes | split across jobs |
| `fuzz-smoke` | every `Fuzz*` target for `$(FUZZTIME)` each, default 10s (`Makefile:54-59`) | no | yes (job `fuzz`) |
| `bench` | `go test -run '^$' -bench . -benchmem ./...` (`Makefile:61-62`) | no | no |
| `proto` | `buf lint`, then `buf generate` (`Makefile:64-66`) | yes | yes, via `proto-check` |
| `proto-check` | `proto`, then `git diff --exit-code -- internal/gen` (`Makefile:69-70`) | yes | yes (job `proto`) |
| `tools` | `go install` of the five pinned tools (`Makefile:72-77`) | n/a | yes, in `lint`, `vuln`, `proto` |
| `e2e` | `build`, `./scripts/e2e.sh`, then `go test -tags e2e -count 1 ./test/e2e` (`Makefile:80-82`) | no | yes (job `e2e`) |
| `clean` | `rm -rf bin coverage.out` (`Makefile:84-85`) | no | no |

Variables you can override on the command line: `GO`, `GOBIN`, `VERSION`, `COMMIT`, `DATE`,
`STATICCHECK_GOTOOLCHAIN` and `FUZZTIME` (all `?=`, `Makefile:1-20`).

## 4. Running locally

The fastest end-to-end check of a change is the one-machine quickstart in
[README.md](../README.md#quickstart-on-one-machine). It uses `bin/minipsi` from `make build`.

Without building a binary, the in-process benchmark runs both parties over an in-memory
connection (no network, no certificates):

```bash
go run ./cmd/minipsi bench --n1 4096 --n2 4096
```

It prints the intersection size, per-phase timings of both sides and the bytes sent each way.
Other flags: `--instances`, `--overlap`, `--hash`, `--ntt-backend`, `--workers`
(`minipsi bench --help`).

`--dev` creates a shared development PKI on first use and allows loopback addresses only. To keep
it out of your home directory, point it at a temporary directory, as `scripts/e2e.sh` does with
`MINIPSI_DEV_PKI` (`scripts/e2e.sh:99`):

```bash
export MINIPSI_DEV_PKI="$(mktemp -d)"
bin/minipsi serve   --dev --name b --listen 127.0.0.1:7443 --dataset customers=b.txt &
bin/minipsi request --dev --name a --peer 127.0.0.1:7443 --dataset customers \
  --input a.txt --output out.txt
```

Without the variable the PKI lives in `$XDG_STATE_HOME/minipsi/dev-pki`, else
`~/.local/state/minipsi/dev-pki` (`internal/devcerts/devpki.go:31-43`, `DefaultDevDir`).

## 5. Tests

### Unit tests

Unit tests sit beside the code as in-package `*_test.go` files (no external `_test` packages,
no `t.Parallel`). `go test ./...` passes in about 9 seconds on a developer machine.
`internal/cli` tests run the CLI in-process; real processes and OS signals are covered only by
the end-to-end suites below.

`-short` (used by `make race`) shrinks the largest sizes but skips no test, because race
instrumentation makes the field and NTT code about 10 times slower (`Makefile:32-33`). It also
drops the 4096-item case of `TestStandalone` (`internal/protocol/protocol_test.go:104-113`). CI runs
only `make race`, so run `make test` locally to exercise the full sizes.

```bash
go test ./internal/config/            # one package
go test -run TestServeValidate -v ./internal/config/
```

### End-to-end

`make e2e` builds `bin/minipsi` and runs two suites:

1. `scripts/e2e.sh [path/to/minipsi]` (default `bin/minipsi`, `scripts/e2e.sh:7`) runs six
   scenarios in a temporary directory: standalone SHA-256 (receiver dials), batched CSV with AES
   and block padding (receiver listens), parameter mismatch (both exit 4, no output), untrusted CA
   (exit 3), two `serve` daemons answering `request` both ways (unknown dataset exits 4, clean
   SIGTERM shutdown, two report files), and `--dev` serve/request.
2. `go test -tags e2e -count 1 ./test/e2e` (`test/e2e/e2e_test.go:1` holds the only build tag).
   `TestMain` builds its own unstamped binary (`test/e2e/e2e_test.go:27-37`, `TestMain`), then
   `TestMatrix` (both listeners x both hashes, mixed NTT backends), `TestSigtermMidRun`
   (interrupted party exits 4, peer sees `ERR_CANCELLED`) and `TestPeerKilled` (survivor exits 3).

`go test ./...` and the lint targets skip `test/e2e`, because of the `e2e` build tag.

### Fuzzing

`go test -list '^Fuzz' ./...` lists seven targets:

| Target | File |
|---|---|
| `FuzzMapPreimages` | `internal/elligator/elligator_test.go:481` |
| `FuzzDecode` | `internal/field/field_test.go:203` |
| `FuzzDecodeKey` | `internal/group/group_test.go:278` |
| `FuzzRead` | `internal/itemio/itemio_test.go:142` |
| `FuzzSenderFrames` | `internal/protocol/fuzz_test.go:90` |
| `FuzzReceiverFrames` | `internal/protocol/fuzz_test.go:98` |
| `FuzzDecodeDTO` | `internal/orcapi/orcapi_test.go:283` |

```bash
make fuzz-smoke FUZZTIME=30s     # every target, 30s each (CI uses 10s)
go test -run '^$' -fuzz '^FuzzRead$' -fuzztime 1m ./internal/itemio
```

Plain `go test` runs each target's seed corpus as ordinary tests. A fuzz failure writes the
crashing input to `<package>/testdata/fuzz/<Target>/` in the working tree; commit it as a
regression seed once fixed. No `testdata` directory exists at `4aa7c9f`.

### Benchmarks

`make bench` runs all 48 benchmarks once with `-benchmem`, including 2^20-point NTT/RNS cases,
so it is slow. Narrow it with plain `go test`, for example
`go test -run '^$' -bench Standalone2e14 -benchmem ./internal/protocol`.

### Large-scale test (environment-gated)

`TestLargeScale` in `internal/poly` is skipped unless `MINIPSI_LARGE` is set
(`internal/poly/poly_test.go:530-532`, `TestLargeScale`). These variables are read only by this test:

| Variable | Meaning |
|---|---|
| `MINIPSI_LARGE` | log2 of the point count, for example `20` |
| `MINIPSI_BACKEND` | `native` (default) or `lattigo` |
| `MINIPSI_CACHE_MB` | transform cache size in MiB; `0` disables it (default 256 MiB) |

```bash
MINIPSI_LARGE=20 MINIPSI_BACKEND=lattigo go test -run TestLargeScale -v ./internal/poly
```

It logs per-phase timings and peak RSS. It does not compile on Windows (`syscall.Getrusage`).

## 6. Protocol buffers

The wire schema is `api/minipsi/v1/psi.proto`; generated code is in `internal/gen/minipsiv1/`.
Never edit `internal/gen` by hand: change the `.proto` file and run `make proto`, then commit
both. There are no `//go:generate` directives.

- `buf.yaml` lints with `STANDARD` minus `SERVICE_SUFFIX`, `RPC_REQUEST_STANDARD_NAME`,
  `RPC_RESPONSE_STANDARD_NAME` and `RPC_REQUEST_RESPONSE_UNIQUE`, because the service is a single
  symmetric bidirectional stream of one `Frame` type (`buf.yaml:10-14`, `SERVICE_SUFFIX`).
- `buf.gen.yaml` runs the two `local:` plugins with `out: internal/gen`; `make proto` prefixes
  `PATH` with `$(GOBIN)` so buf finds them (`Makefile:65-66`).
- `make proto-check` (CI job `proto`) regenerates in place and fails on any diff under
  `internal/gen` (`Makefile:69-70`). After a failure the regenerated files stay in your working
  tree. Plugin versions are pinned, so the generated headers must keep reading
  `protoc-gen-go v1.36.12` and `protoc-gen-go-grpc v1.6.2`.

## 7. Docker and version stamping

```bash
docker build -t minipsi .
docker run --rm minipsi version
```

The image is a static binary on `gcr.io/distroless/static-debian12:nonroot`, running as
`nonroot:nonroot` in `/work` with `ENTRYPOINT ["/usr/local/bin/minipsi"]` (`Dockerfile:17-21`).
Mounting certificates and data is covered in [RUNBOOK.md](../RUNBOOK.md#container).

`minipsi version` prints `internal/version` variables that are set with `-X` at link time
(`internal/version/version.go:5-9`, `Version`):

| Build path | `Version` | `Commit` | `Date` |
|---|---|---|---|
| `make build` (`Makefile:7-8`, `LDFLAGS`) | `git describe --tags --always --dirty`, else `dev` | `git rev-parse --short HEAD` | `date -u` at build time |
| `docker build` (`Dockerfile:11-14`, `VERSION`) | build arg `VERSION`, default `dev` | build arg `COMMIT`, default `unknown` | never set: prints `built: unknown` |
| `go build` / `go run` / Go e2e suite | `dev` | `unknown` | `unknown` |

Pass the build args to stamp an image, as the Dockerfile comment shows:
`docker build --build-arg VERSION=$(git describe --tags --always) --build-arg COMMIT=$(git rev-parse --short HEAD) .`
The CI `docker` job passes none, so its image reports `dev`. The stamped `Version` is also sent
to the peer in the handshake and written to run reports.

## 8. Continuous integration

`.github/workflows/ci.yml` runs on pushes to `main`, on every pull request and on manual
`workflow_dispatch`.
A newer run on the same ref cancels the older one (`.github/workflows/ci.yml:15-17`, `cancel-in-progress`). All seven jobs run in parallel on `ubuntu-24.04` with Go `1.27.2`
(`.github/workflows/ci.yml:20`, `GO_VERSION`). Actions are pinned by commit SHA.

| Job | Steps | Timeout | Local equivalent |
|---|---|---|---|
| `lint` | `make tools`, `make lint` | 15 min | `make lint` |
| `test` | `make race` | 30 min | `make race` |
| `vuln` | `make tools`, `make vuln` | 15 min | `make vuln` |
| `proto` | `make tools`, `make proto-check` | 15 min | `make proto-check` |
| `e2e` | `make e2e` | 20 min | `make e2e` |
| `fuzz` | `make fuzz-smoke FUZZTIME=10s` | 20 min | `make fuzz-smoke` |
| `docker` | `docker build -t minipsi:ci .`, `docker run --rm minipsi:ci version` | 20 min | same commands |

Before pushing, `make check proto-check e2e` reproduces everything except the fuzz and docker
jobs. CI never runs `make test`, `make cover` or `make bench`.

## 9. Debugging

- `--log-level debug` (any party command or `serve`) adds debug lines, for example how the
  memory limit was chosen (`internal/cli/party.go:474-484`, `applyMemoryLimit`). Logs go to stderr;
  they are designed never to contain set items, and tests check this.
- `--log-format json` emits one JSON object per log line, convenient for `jq`.
- `--report FILE` writes a JSON run report (exit code, timings, byte counts, session ID; the
  receiver also gets `result_count`); `--report -` writes it to stdout. A daemon writes one
  report per accepted session under `<state_dir>/reports/` (`internal/cli/serve.go:213-218`,
  `serveSession`).
- `minipsi bench` isolates compute cost from the network and TLS.
- Exit codes are 0 ok, 1 usage/config, 2 input, 3 network/timeout, 4 protocol/peer
  abort/interrupted, 5 internal (`internal/cli/root.go:19-26`, `ExitUsage`). Use the
  [RUNBOOK troubleshooting table](../RUNBOOK.md#5-exit-codes-and-troubleshooting) to map them
  to causes.
- `scripts/e2e.sh` deletes its work directory on exit, including the `*.log` files its `FAIL`
  messages point to (`scripts/e2e.sh:9`, `trap`). To keep them, run the failing scenario by hand.

## 10. Data stores and migrations

None. minipsi has no database, schema or migration step. Persistent state is limited to files:
input sets, the receiver's result file, JSON reports, the daemon's `state_dir/reports/` and the
`--dev` PKI directory.

## 11. Pitfalls

- **staticcheck runs under go1.26.0.** `STATICCHECK_GOTOOLCHAIN ?= go1.26.0` (`Makefile:18`)
  because staticcheck v0.8.1 "cannot read Go 1.27 export data"; the pin stays until a release
  supporting Go 1.27 is at least two weeks old (v0.8.2 was released 2026-10-12, `Makefile:16-17`).
  The lint job therefore downloads go1.26.0 at run time. Raising `go` in go.mod to 1.27 or later
  breaks `make lint` until the pin and `STATICCHECK_VERSION` are updated together.
- **CI Go differs from go.mod.** CI and the Docker build use 1.27.2; go.mod only requires
  1.26.0, so a local `make build` uses go1.26.0 unless you set `GOTOOLCHAIN=go1.27.2`. govulncheck
  results depend on the toolchain's standard library and can differ between the two.
- **Generated-code drift fails CI.** Any hand edit under `internal/gen`, or a different plugin
  version, makes the `proto` job fail. Regenerate with the pinned tools.
- **`fmt-check` covers `cmd` and `internal` only** (`Makefile:41`, `gofmt`), not `test/`, and it
  uses whatever `gofmt` is first on `PATH`, which `GOTOOLCHAIN` does not switch. Run
  `gofmt -l test` yourself after editing the e2e suite.
- **`GOMEMLIMIT` vs `--memory-limit`.** An explicit `--memory-limit` or `memory_limit` key wins;
  otherwise a set `GOMEMLIMIT` wins over the 2 GiB default; `0` disables the limit
  (`internal/cli/party.go:474-484`, `applyMemoryLimit`). In-process CLI tests share one runtime,
  so a memory limit set by one test persists for the rest of the test binary.
- **`--insecure-dev` needs `MINIPSI_I_UNDERSTAND_INSECURE=1`**, otherwise the run exits 1
  (`internal/transport/transport.go:93-95`, `InsecureEnv`). `request --insecure-dev` sends no
  dataset metadata, so a daemon always answers "dataset not available"; use `--dev` instead.
- **Dev PKI location.** `MINIPSI_DEV_PKI`, else `$XDG_STATE_HOME/minipsi/dev-pki`, else
  `~/.local/state/minipsi/dev-pki`. All `--dev` processes sharing that directory trust each other;
  delete it to start over.
- **Port races.** `scripts/e2e.sh` and the Go tests pick a free port and then release it before
  use. Without python3 the script uses fixed ports 17443 and 17444, which can collide.
- **Exit codes are a contract.** `scripts/e2e.sh` and `test/e2e` assert exact exit codes and the
  `peer connected` log line (`internal/cli/party.go:289`, `log`). Changing them breaks the
  end-to-end suites.
