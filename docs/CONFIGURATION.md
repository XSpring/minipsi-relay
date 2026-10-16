# Configuration reference

The authoritative list of every configuration key, command-line flag, default, validation rule and
environment variable of minipsi. The code lives in `internal/config` (types, defaults, YAML
loading, validation) and `internal/cli` (flag registration and precedence).

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

Related documents: [RUNBOOK.md](../RUNBOOK.md#4-running) (annotated YAML examples for a
receiver and a daemon), [BUSINESS_FLOWS.md](BUSINESS_FLOWS.md#workflow-configuration-resolution-and-validation)
(step-by-step trace of configuration resolution), [DEVELOPMENT.md](DEVELOPMENT.md) (test-only
environment variables), [INTEGRATIONS.md](INTEGRATIONS.md) (gRPC metadata and TLS details),
[ARCHITECTURE.md](ARCHITECTURE.md), [DOMAIN.md](DOMAIN.md) and [decisions/README.md](decisions/README.md).

Validation messages below are quoted from the source, except that the source's Unicode
greater-or-equal sign is written here as `>=`.

## 1. Two configuration types

| Type | Commands | Defaults | Loader | Validation |
|---|---|---|---|---|
| `Config` (party) | `receiver`, `sender`, `request` | `internal/config/config.go:113-126` (`Default`) | `Config.Load` | `Config.Validate` |
| `Serve` (daemon) | `serve` | `internal/config/serve.go:68-75` (`DefaultServe`) | `Serve.Load` | `Serve.Validate` |

`request` is a receiver that dials a `serve` daemon: it uses the party `Config` with role
`receiver` (`internal/cli/party.go:42-47`, `partyRole`). The `bench`, `certs dev` and `version`
commands take flags only and read no configuration file.

## 2. Precedence

```mermaid
flowchart LR
  D["Built-in defaults"] --> Y["YAML file from --config"]
  Y --> F["Flags set explicitly on the command line"]
  F --> V["Validate: all errors reported, exit 1"]
```

1. **Defaults.** `Default()` for parties, `DefaultServe()` for the daemon. `request` alone raises
   `timeouts.connect_retry` to `2m`, because a busy daemon refuses new sessions until the running
   one ends (`internal/cli/party.go:131-139`, `requestConnectRetry`).
2. **YAML file.** `--config FILE` is a flag of each subcommand, not of the root command:
   `minipsi sender --config sender.yaml` works, `minipsi --config sender.yaml` fails with
   `unknown flag: --config`. Only keys present in the file change the defaults; nested blocks
   merge key by key (`internal/config/config.go:129-146`, `Load`).
3. **Flags.** A flag overrides the file only if it was set on the command line, even when set to
   its default value (`internal/cli/party.go:167-171`, `Changed`; `internal/cli/serve.go:64-81`, `set`).
4. **Validation.** `Validate` checks the merged result and joins all problems into one error,
   one per line. Any configuration error exits with code 1.

Loading rules (`Load`, both types):

- Strict: an unknown key at any depth is an error (`KnownFields`), for example
  `config c.yaml: yaml: unmarshal errors:` followed by `line 1: field rolee not found in type config.Config`.
- An empty or comment-only file is valid and changes nothing.
- Only the first YAML document is read; anything after `---` is ignored.
- A `null` value (`key: ~` or `key:` with nothing after it) keeps the default.
- A party file may contain `role`. If it differs from the command, the run fails with
  `config file is for role %q, command is %q` (`internal/cli/party.go:163-164`, `role`).
  A file for `request` must say `role: receiver` or omit `role`.

### Memory limit precedence

The Go runtime soft memory limit is chosen once per process
(`internal/cli/party.go:474-484`, `applyMemoryLimit`):

1. `memory_limit` in the file or `--memory-limit` on the command line (explicit) wins;
2. otherwise, if the `GOMEMLIMIT` environment variable is set, the runtime keeps that value;
3. otherwise the default `2GiB` applies.

`0` disables the limit. `memory_limit: ~` does not count as explicit. Note that the RUNBOOK
example file sets `memory_limit: 2GiB`, which makes it explicit and so overrides `GOMEMLIMIT`.

## 3. Value syntax

**Size** (`max_frame`, `memory_limit`, `poly_cache`). A non-negative base-10 integer with an
optional case-sensitive unit: `KiB`, `MiB`, `GiB`, `TiB` (powers of 1024), `KB`, `MB`, `GB`, `TB`
(powers of 1000) or `B`. No unit means bytes. Spaces are allowed (`"4 MiB"`); fractions, hex,
underscores and lower-case units (`4mib`, `4kB`) are rejected (`internal/config/config.go:298-312`, `ParseSize`).
With a unit the value is capped at 2^62 bytes. In YAML a size may be an integer or a string.
Error: `invalid size %q (use e.g. 4MiB, 2GiB or a byte count)`. Help output prints sizes in the
largest exact binary unit, for example `(default 4MiB)`.

**Duration** (all `timeouts.*`). Go duration syntax: `1500ms`, `30s`, `10m`, `1h30m`. Units are
`ns`, `us`, `ms`, `s`, `m`, `h`; there is no day unit, and a bare integer other than `0` is
rejected (`600` must be `600s`). In YAML the error is `invalid duration %q`
(`internal/config/config.go:355-359`, `UnmarshalYAML`); on the command line pflag reports the Go
parser error. Negative values parse but fail validation.

Size and duration errors from a YAML file carry the file name but no line number or key name.

**Other YAML values.** yaml.v3 truncates a float written into an integer key (`lambda: 64.9`
becomes 64) and accepts YAML 1.1 booleans (`yes`, `no`, `on`). String values are case-sensitive
(`hash`, `input.format`, `ntt_backend`, `mode`, `log.format`), except `log.level`.

## 4. Party configuration: `receiver`, `sender`, `request`

Flags are registered in `partyFlags` (`internal/cli/party.go:54-127`, `partyFlags`). The last
column says which commands accept the flag: R = `receiver`, S = `sender`, Q = `request`. A key
without a flag for a command can still be set in that command's YAML file.

| YAML key | Flag | Type | Default | Validation message | Flag on |
|---|---|---|---|---|---|
| `role` | (the subcommand) | string | set by the command | `role must be receiver or sender, got %q` | - |
| `mode` | `--mode` | string | `direct` | `relay` gives `mode relay (via the Orchestrator) is not implemented yet; use direct`; others give `mode must be direct or relay, got %q` | Q |
| `listen` | `--listen` | host:port | empty | `exactly one of listen or connect is required` | R, S |
| `connect` | `--connect` (R, S), `--peer` (Q) | host:port | empty | same as `listen` | R, S, Q |
| `request.dataset` | `--dataset` | string | empty | `request.dataset needs the receiver role and a peer to connect to` | Q |
| `request.identity` | `--identity` | string | first SAN of `tls.cert` | `request.identity needs request.dataset` | Q |
| `input.path` | `--input` | path | `-` (stdin) | `input path is required` | R, S, Q |
| `input.format` | `--format` | `lines` or `csv` | `lines` | `input format must be lines or csv, got %q` | R, S, Q |
| `input.column` | `--column` | index or header name | `0` | `input column must not be empty` (ignored for `lines`) | R, S, Q |
| `input.instance_column` | `--instance-column` | index or header name | empty | `batched mode requires instance_column` (csv and batched) | R, S, Q |
| `input.header` | `--header` | bool | `false` | none | R, S, Q |
| `input.batched` | `--batched` | bool | `false` | `batched mode requires format csv` (lines and batched) | R, S, Q |
| `output` | `--output` | path | `-` (stdout) | `output is only valid for the receiver` (sender with any other value) | R, Q |
| `protocol.hash` | `--hash` | `sha256` or `aes` | `sha256` | `hash must be sha256 or aes, got %q`; must match the peer | R, S, Q |
| `protocol.lambda` | `--lambda` | int | `40` | `lambda must be in [40, 256], got %d`; must match the peer | R, S, Q |
| `protocol.padding.extra` / `.min` / `.multiple` | `--pad-extra` / `--pad-min` / `--pad-multiple` | int | `1` / `0` / `1` | `padding needs extra >= 0, min >= 0, multiple >= 1` | R, Q |
| `protocol.sender_padding.extra` / `.min` / `.multiple` | `--pad-extra` / `--pad-min` / `--pad-multiple` | int | `0` / `0` / `1` | `sender_padding needs extra >= 0, min >= 0, multiple >= 1` | S |
| `tls.cert`, `tls.key`, `tls.ca` | `--cert`, `--key`, `--ca` | PEM paths | empty | `tls cert, key and ca are required (or dev / insecure_dev for development)` | R, S, Q |
| `tls.peer_name` | `--peer-name` | string | empty (no pin) | none; pins a DNS or URI SAN of the peer certificate | R, S, Q |
| `tls.server_name` | `--server-name` | string | host part of the dial address | none; used only when dialing | R, S, Q |
| `tls.insecure_dev` | `--insecure-dev` | bool | `false` | `insecure_dev cannot be combined with TLS files`; needs `MINIPSI_I_UNDERSTAND_INSECURE=1` | R, S, Q |
| `tls.dev` | `--dev` | bool | `false` | `dev cannot be combined with TLS files or insecure_dev`; loopback only (see below) | R, S, Q |
| `tls.name` | `--name` | lower-case DNS label | role name (`receiver` or `sender`) | `tls name is only used with dev` | R, S, Q |
| `limits.max_frame` | `--max-frame` | Size | `4MiB` | `max_frame must be >= 64KiB` | R, S, Q |
| `limits.max_instance_size` | `--max-instance-size` | int | `4194304` | `limits must be positive` | R, S, Q |
| `limits.max_total_items` | `--max-total-items` | int | `16777216` | `limits must be positive` | R, S, Q |
| `limits.max_instances` | `--max-instances` | int | `16777216` | `limits must be positive` | R, S, Q |
| `timeouts.handshake` | `--handshake-timeout` | Duration | `30s` | must be > 0 (message below) | R, S, Q |
| `timeouts.phase` | `--phase-timeout` | Duration | `10m` | must be > 0 | R, S, Q |
| `timeouts.total` | `--timeout` | Duration | `0` (no deadline) | must be >= 0 | R, S, Q |
| `timeouts.connect_retry` | `--connect-retry` | Duration | `0` (R, S); `2m` (Q) | must be >= 0 | R, S, Q |
| `workers` | `--workers` | int | `0` (GOMAXPROCS) | `workers must be >= 0` | R, S, Q |
| `ntt_backend` | `--ntt-backend` | `native` or `lattigo` | `native` | `ntt_backend must be native or lattigo, got %q`; local choice, need not match the peer | R, S, Q |
| `memory_limit` | `--memory-limit` | Size | `2GiB` (`0` = off) | `memory_limit and poly_cache must be >= 0` | R, S, Q |
| `poly_cache` | `--poly-cache` | Size | `256MiB` (`0` = off) | same as `memory_limit` | R, S, Q |
| `report` | `--report` | path | empty (no report); `-` = stdout | none | R, S, Q |
| `log.format` | `--log-format` | `text` or `json` | `text` | `log format must be text or json` | R, S, Q |
| `log.level` | `--log-level` | `debug`, `info`, `warn`, `error` | `info` | `log level must be debug, info, warn or error` (case-insensitive) | R, S, Q |

The single timeout message is `timeouts: handshake and phase must be > 0, total and connect_retry >= 0`
(`internal/config/config.go:222-224`, `Timeouts`). All checks are in `Config.Validate`
(`internal/config/config.go:156-250`, `Validate`), which never touches the filesystem: missing
TLS files fail later with exit 1, an unreadable input with exit 2.

### Cross-field rules

- **Padding belongs to one role.** The receiver policy is `protocol.padding`, the sender policy
  is `protocol.sender_padding`. A sender whose `padding` differs from `{1, 0, 1}`, or a receiver
  whose `sender_padding` differs from `{0, 0, 1}`, is rejected, so a privacy setting is never
  silently ignored (`internal/config/config.go:208-214`, `SenderPadding`). The `--pad-*` flags
  always bind to the running role's own policy.
- **Request settings.** `request` requires `--dataset` (`request needs --dataset`); `receiver` or
  `sender` with `request.dataset` in the file fail with
  ``request settings are only valid for `minipsi request` `` (`internal/cli/party.go:172-177`, `Dataset`).
- **Default identity.** Without `request.identity`, `request` sends the first DNS SAN of
  `tls.cert`, else its first URI SAN, else its subject CN (`internal/cli/party.go:404-424`, `firstSAN`).
- **TLS modes are exclusive** (`internal/config/config.go:263-286`, `validTLS`): TLS files, `dev`
  and `insecure_dev` cannot be mixed, and without `dev` or `insecure_dev` all three files are required.
- **Dev mode is loopback only.** The listen or dial address must be `localhost` or a loopback IP
  with a port; otherwise `dev mode only allows loopback addresses (127.0.0.1, ::1, localhost), got %q`.
  Any `127.0.0.0/8` address and IPv6 `::1` are accepted (`internal/devcerts/devpki.go:182-191`, `IsLoopback`).
- **Dev party name.** `tls.name` must be a lower-case DNS label, checked when the dev PKI is
  opened: `devcerts: party name %q must be a lower-case DNS label` (`internal/devcerts/devpki.go:51-52`, `EnsureDev`).

## 5. Daemon configuration: `serve`

Flags are registered in `newServeCmd` (`internal/cli/serve.go:27-56`, `newServeCmd`). Keys with
"YAML only" have no flag. Shared blocks (`tls`, `limits`, `log`) have the same keys and defaults
as in section 4.

| YAML key | Flag | Default | Validation message / behaviour |
|---|---|---|---|
| `listen` | `--listen` | empty | `listen is required` |
| `mode` | `--mode` | `direct` | as for parties; `relay` is rejected |
| `tls.cert`, `tls.key`, `tls.ca` | `--cert`, `--key`, `--ca` | empty | as for parties, checked against `listen` |
| `tls.dev`, `tls.name`, `tls.insecure_dev` | `--dev`, `--name`, `--insecure-dev` | `false`, `sender`, `false` | as for parties |
| `tls.peer_name` | YAML only | empty | pins a SAN of every requesting client |
| `tls.server_name` | YAML only | empty | accepted but unused (the daemon only listens) |
| `state_dir` | `--state-dir` | empty (no report files) | creates `<state_dir>/reports/` (mode 0700) and writes one JSON report per accepted session |
| `datasets` | `--dataset NAME=PATH` (repeatable) | none | `at least one dataset is required` |
| `datasets.<NAME>.input.path` | PATH of `--dataset` | none | `dataset %s: input.path must name a file` (stdin is not allowed) |
| `datasets.<NAME>.input.format`, `.column`, `.instance_column`, `.header`, `.batched` | YAML only | `lines`, `0`, empty, `false`, `false` | party input rules, prefixed `dataset <NAME>: ` |
| `datasets.<NAME>.protocol.hash` | YAML only | `sha256` | requesters must use the same hash |
| `datasets.<NAME>.protocol.lambda` | YAML only | `40` | requesters must use the same lambda |
| `datasets.<NAME>.protocol.sender_padding.extra` / `.min` / `.multiple` | YAML only | `0` / `0` / `1` | the daemon's sender padding policy |
| `allowed_peers` | YAML only | empty (accept every authenticated peer) | `allowed_peers must not contain empty names` |
| `limits.*` | YAML only | as for parties | as for parties |
| `timeouts.handshake` | YAML only | `30s` | must be > 0 |
| `timeouts.phase` | YAML only | `10m` | must be > 0 |
| `timeouts.session` | YAML only | `0` (no deadline) | `timeouts.session must be >= 0` |
| `workers` | `--workers` | `0` | as for parties |
| `ntt_backend` | YAML only | `native` | as for parties |
| `memory_limit` | `--memory-limit` | `2GiB` | as for parties, same `GOMEMLIMIT` precedence |
| `poly_cache` | YAML only | `256MiB` | as for parties |
| `log.format`, `log.level` | `--log-format`, `--log-level` | `text`, `info` | as for parties |

Party-only keys are unknown in a daemon file and rejected: top-level `role`, `connect`,
`output`, `report`, `input`, `protocol`, `request`, plus `timeouts.total`,
`timeouts.connect_retry`, `datasets.<NAME>.protocol.padding` and `datasets.<NAME>.limits`.

### Datasets

- **Names** must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`: 1 to 64 characters, starting with a
  letter or digit (`internal/config/serve.go:62`, `datasetName`). Otherwise:
  `dataset name %q: use letters, digits, '.', '_' or '-' (max 64)`.
- **`--dataset NAME=PATH`** splits at the first `=`; an empty name or path fails with
  `--dataset %q: want NAME=PATH` (`internal/cli/serve.go:82-86`, `want`). The flag creates the
  dataset in `lines` format with default protocol settings, and **replaces** a YAML dataset of
  the same name entirely (`internal/cli/serve.go:90`, `Dataset`).
- **Startup check.** Every dataset is read once at startup; an unreadable or invalid file stops
  the daemon with the input exit code 2 (`internal/cli/serve.go:110-116`, `loadInput`). The file
  is read again for every session, so it can be replaced while the daemon runs.
- **Admission.** An unknown dataset and a peer not in `allowed_peers` get the same rejection,
  `dataset not available`, so a client cannot probe which datasets exist; the client exits 4
  (`internal/cli/serve.go:131-137`, `Allowed`).

### `allowed_peers`

An empty list accepts every peer that passed mutual TLS. Otherwise a peer is admitted only if one
of its certificate's DNS or URI SANs equals an entry exactly: case-sensitive, no trimming, subject
CN never used (`internal/config/serve.go:114-124`, `Allowed`). The self-declared
`minipsi-identity` plays no part in this decision. Under `insecure_dev` peers present no
certificate, so a non-empty list rejects everyone.

### How a dataset becomes a session configuration

For every accepted session the daemon builds a sender `Config` from the `Serve` settings and the
requested dataset, starting from `Default()` (`internal/config/serve.go:128-156`, `SessionConfig`):

| Session `Config` field | Value |
|---|---|
| `role`, `output`, `connect` | `sender`, empty, empty |
| `mode`, `listen`, `tls`, `limits`, `workers`, `ntt_backend`, `memory_limit`, `poly_cache`, `log` | copied from the daemon |
| `input.*` | the dataset's input; empty `format` becomes `lines`, empty `column` becomes `0` |
| `protocol.hash`, `protocol.lambda` | the dataset's value; empty or `0` becomes `sha256` / `40` |
| `protocol.sender_padding` | the dataset's value; `multiple: 0` becomes `1` |
| `protocol.padding` | party default `{1, 0, 1}` (receiver policy, unused by a sender) |
| `timeouts.handshake`, `timeouts.phase` | the daemon's values |
| `timeouts.total` | the daemon's `timeouts.session` |
| `timeouts.connect_retry` | `0` |

Because "unset" is detected by zero values, a dataset cannot ask for `lambda: 0`,
`multiple: 0` or an empty format or column; those always become the defaults.

### Daemon validation

`Serve.Validate` checks `listen`, `mode`, TLS, the dataset count, `timeouts.session` and
`allowed_peers`, then validates each dataset (in name order) as a session configuration and
prefixes its messages with `dataset <NAME>: ` (`internal/config/serve.go:159-198`, `Validate`).
Identical messages are reported once (`internal/config/serve.go:208-222`, `dedupErrs`). As a result:

- a bad shared setting such as `workers: -1` is reported once, under the first dataset's name,
  for example `dataset customers: workers must be >= 0`;
- the same mistake in several datasets is reported only for the first one;
- shared settings such as `workers`, `limits`, `ntt_backend`, `memory_limit` and `log` are
  validated only through datasets with a valid name and path; if no dataset passes those
  checks, these settings are not validated at all.

## 6. Environment variables

| Variable | Read by | Effect |
|---|---|---|
| `MINIPSI_DEV_PKI` | `--dev` / `tls.dev` | Directory of the shared development PKI (`internal/devcerts/devpki.go:31-34`, `DevPKIEnv`). |
| `XDG_STATE_HOME` | `--dev` / `tls.dev` | If `MINIPSI_DEV_PKI` is unset, the PKI lives in `$XDG_STATE_HOME/minipsi/dev-pki`, else in `~/.local/state/minipsi/dev-pki` (`internal/devcerts/devpki.go:35-42`, `XDG_STATE_HOME`). |
| `MINIPSI_I_UNDERSTAND_INSECURE` | `--insecure-dev` / `tls.insecure_dev` | Must be exactly `1`; otherwise `transport: plaintext mode requires MINIPSI_I_UNDERSTAND_INSECURE=1` and exit 1 (`internal/transport/transport.go:93-95`, `InsecureEnv`). |
| `GOMEMLIMIT` | Go runtime | Used only when `memory_limit` / `--memory-limit` is not set (section 2). |

`MINIPSI_LARGE`, `MINIPSI_BACKEND` and `MINIPSI_CACHE_MB` are read only by a test; see
[DEVELOPMENT.md](DEVELOPMENT.md#large-scale-test-environment-gated). No other environment
variable affects minipsi.

## 7. Request metadata

`request` tells the daemon which dataset it wants through two gRPC metadata headers sent with
the session stream (`internal/config/serve.go:57-60`, `MetaDataset`):

| Header | Value |
|---|---|
| `minipsi-dataset` | `request.dataset` / `--dataset` |
| `minipsi-identity` | `request.identity` / `--identity`, or the certificate's first SAN |

The daemon keeps only headers that start with `minipsi-` (`internal/transport/transport.go:398-404`, `HasPrefix`).
The identity is self-declared: it appears in the daemon log (`requester=`) and the session
report, but never in authorization. `request --insecure-dev` sends no headers, so the daemon
always answers `dataset not available`; use `--dev` for local daemon tests. See
[INTEGRATIONS.md](INTEGRATIONS.md) for the wire-level contract.
