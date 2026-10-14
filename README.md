# minipsi

Production CLI for the **semi-honest polynomial-based Diffie–Hellman private set
intersection** (Rosulek–Trieu, CCS'21) over Curve25519 + Elligator2, running between two
hosts over gRPC with mutual TLS 1.3. Only the receiver learns the intersection.

How to deploy and operate it: [RUNBOOK.md](RUNBOOK.md). All flags: `minipsi <command> --help`.

## Build and test

Requires Go 1.26+.

```bash
make tools   # one-time: staticcheck, govulncheck, buf, protoc plugins (via go install)
make build   # → bin/minipsi (static, CGO_ENABLED=0)
make check   # gofmt, vet, staticcheck, race tests, govulncheck
make e2e     # real processes over mTLS: one-shot runs and two daemons (scripts/e2e.sh)
make fuzz-smoke
docker build -t minipsi .   # distroless image
```

CI (`.github/workflows/ci.yml`) runs these on every push to `main` and every pull request:
lint, race tests, govulncheck, generated-code check, end-to-end, fuzz smoke and the
container build.

## Quickstart on one machine

`--dev` creates throw-away test certificates on first use and only allows loopback addresses.

```bash
printf 'alice\nbob\ncarol\n' > a.txt
printf 'bob\ncarol\ndave\n'  > b.txt

# Daemon mode: "b" serves its dataset, "a" requests a session and gets the result locally.
bin/minipsi serve   --dev --name b --listen 127.0.0.1:7443 --dataset customers=b.txt &
bin/minipsi request --dev --name a --peer 127.0.0.1:7443 --dataset customers \
  --input a.txt --output out.txt
cat out.txt   # bob, carol

# One-shot mode: one process per party, one session each.
bin/minipsi sender   --dev --listen 127.0.0.1:7444 --input b.txt &
bin/minipsi receiver --dev --connect 127.0.0.1:7444 --input a.txt --output out2.txt
```

## Commands

| Command | Purpose |
|---|---|
| `receiver`, `sender` | one-shot party: run one session and exit |
| `serve` | long-lived daemon answering session requests as sender for named datasets |
| `request` | receiver that asks a peer's `serve` daemon for a session (`--mode direct`; `relay` reserved) |
| `certs dev` | create a throw-away CA and certificates for testing |
| `bench` | run both parties in one process for capacity planning |
| `version` | version, protocol and build information |

Exit codes: 0 ok, 1 usage/config, 2 input, 3 network/timeout, 4 protocol/peer
abort/interrupted, 5 internal.

## Layout

| Path | Contents |
|---|---|
| `cmd/minipsi` | main |
| `internal/cli`, `internal/config` | commands, YAML/flag configuration, daemon loop |
| `internal/protocol` | the PSI protocol (receiver, sender, messages, errors) |
| `internal/transport` | gRPC over mutual TLS 1.3 |
| `internal/field`, `ntt`, `rns`, `poly` | prime field, NTT/RNS multiplication, polynomial interpolation/evaluation |
| `internal/group`, `elligator`, `hashing` | Curve25519 group, Elligator2 encoding, hashing |
| `internal/itemio`, `report`, `devcerts` | input/output files, JSON reports, test certificates |
| `api/minipsi/v1` | protobuf wire schema (generated code in `internal/gen`) |
| `test/e2e`, `scripts/e2e.sh` | end-to-end tests |
