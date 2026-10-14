# Deployment Runbook

How to run minipsi between two organisations' hosts. All flags: `minipsi <command> --help`.

## 1. Roles and topology

- **Receiver** (host A) learns the intersection; **sender** (host B) learns nothing beyond
  the receiver's padded set size.
- Either role may listen; the other connects. Only the listener's TCP port must be
  reachable (one port, TLS 1.3, gRPC/HTTP2). One run = one process = one session
  (for long-lived listeners see §4b).
- Choose the listener = the party that can expose a port; the dialer uses
  `--connect-retry` so start order doesn't matter.

## 2. Prerequisites

| Item | Requirement |
|---|---|
| Binary | `make build` (static, linux/amd64 or arm64) or the container image (`docker build .`) |
| CPU / RAM | ≥ 2 vCPU (dedicated, not shared-core); RAM: ~1 GB per 2^20 items plus headroom. Default `--memory-limit 2GiB`; raise it on bigger hosts, keep it ≤ ~75 % of the host/container limit. |
| Time sync | not required (no time-based protocol logic); certificate validity needs a sane clock |
| PKI | One CA trusted by both parties (or each trusts the other's CA via `--ca` bundle). Leaf certificates must carry a DNS or URI SAN and both TLS server and client EKUs (either side may be server or client). |
| Agreement | Both parties agree on: `--hash` (sha256 default), `--lambda` (40), instance IDs and input normalisation (minipsi compares raw bytes). |

## 3. Certificates

Production: issue from your PKI, e.g. SAN `DNS:psi-a.example.org` for the receiver and
`DNS:psi-b.example.org` for the sender, EKU serverAuth+clientAuth. Pin the peer:
`--peer-name psi-b.example.org` on host A and `--peer-name psi-a.example.org` on host B.

Testing only: `minipsi certs dev --out certs --name psi-a.example.org,psi-b.example.org`
(creates a throw-away CA; distribute `ca.crt` and each host's `.crt/.key`; keys are 0600).

Single machine, several parties: add `--dev [--name N]` to any command instead of the three
TLS flags. A shared dev CA and one certificate per name are created on first use
in `~/.local/state/minipsi/dev-pki` (override: `MINIPSI_DEV_PKI`); loopback addresses only.

## 4. Running

```bash
# Host B (sender, listens)
minipsi sender --listen 0.0.0.0:7443 --input /data/x.txt \
  --cert /etc/minipsi/b.crt --key /etc/minipsi/b.key --ca /etc/minipsi/ca.crt \
  --peer-name psi-a.example.org --report /var/log/minipsi/run-$(date +%F).json --log-format json

# Host A (receiver, dials)
minipsi receiver --connect psi-b.example.org:7443 --connect-retry 10m --input /data/y.txt \
  --output /data/intersection.txt \
  --cert /etc/minipsi/a.crt --key /etc/minipsi/a.key --ca /etc/minipsi/ca.crt \
  --peer-name psi-b.example.org --report /var/log/minipsi/run-$(date +%F).json --log-format json
```

Or put everything in a YAML file (`--config`); flags override the file.

```yaml
# receiver.yaml — run with: minipsi receiver --config receiver.yaml (unknown keys are errors)
connect: "psi-b.example.org:7443"            # or listen: "0.0.0.0:7443"
input: { path: y.txt, format: lines }       # csv: { format: csv, column: email, header: true }
output: out.txt
protocol: { hash: sha256, lambda: 40, padding: { extra: 1, min: 0, multiple: 1 } }
# sender files use sender_padding instead of padding (default off: { extra: 0, min: 0, multiple: 1 })
tls: { cert: a.crt, key: a.key, ca: ca.crt, peer_name: psi-b.example.org }
limits: { max_frame: 4MiB, max_instance_size: 4194304, max_total_items: 16777216, max_instances: 16777216 }
timeouts: { handshake: 30s, phase: 10m, total: 0s, connect_retry: 10m }
workers: 0                                   # 0 = all CPUs
ntt_backend: native                          # native | lattigo
memory_limit: 2GiB
poly_cache: 256MiB
report: run.json
log: { format: json, level: info }
```

Batched mode (many small instances): CSV with an instance-ID column on both sides, e.g.
`--format csv --header --batched --instance-column inst --column item`; the instance-ID sets
must be identical on both sides (use empty instances where a party has no items).

Hiding set sizes: `--pad-min N` (fixed minimum) and/or `--pad-multiple N` (round up, e.g.
1024). On the receiver they add dummy interpolation points (32 bytes plus interpolation work
each); on the sender they add dummy outputs (ℓ_out bytes plus the full per-item
evaluation and EC cost each, so timing does not reveal the real size). Sender
padding is off by default. In YAML: `protocol.padding` (receiver) / `protocol.sender_padding`
(sender).

### Container

```bash
docker run --rm -v /srv/minipsi:/work -p 7443:7443 minipsi:<version> sender \
  --listen 0.0.0.0:7443 --input /work/x.txt --cert /work/b.crt --key /work/b.key \
  --ca /work/ca.crt --peer-name psi-a.example.org --memory-limit 1500MiB
```

The image runs as `nonroot` (uid 65532): the mounted directory must be readable (inputs,
certs) and writable (output, report) by that uid. Set `--memory-limit` below the container
memory limit.

### Scheduling

- cron / systemd timer / Kubernetes Job: start the listener first or give the dialer a
  generous `--connect-retry`; use `--timeout` to bound the whole run.
- Example systemd unit (oneshot): `ExecStart=/usr/local/bin/minipsi --config /etc/minipsi/sender.yaml`,
  `Type=oneshot`, `User=minipsi`, `LimitCORE=0`, `ProtectSystem=strict`,
  `ReadWritePaths=/data /var/log/minipsi`, `MemoryMax=` ≥ memory-limit × 1.3.

## 4b. Daemon mode: `serve` and `request`

Each machine runs one long-lived `serve` for the datasets it offers; whoever wants a result
runs `request` against the other machine. Sessions run one at a time per daemon.

```yaml
# /etc/minipsi/serve.yaml on host B
listen: 0.0.0.0:7443
tls: { cert: /etc/minipsi/b.crt, key: /etc/minipsi/b.key, ca: /etc/minipsi/ca.crt }
state_dir: /var/lib/minipsi            # per-session JSON reports in reports/
datasets:
  customers:
    input: { path: /data/customers.txt }
    protocol: { hash: sha256, sender_padding: { multiple: 1024 } }
allowed_peers: []                      # empty = any peer with a certificate from ca.crt
```

```bash
# Host B: start the daemon (systemd: Type=simple, Restart=on-failure, same hardening as §4)
minipsi serve --config /etc/minipsi/serve.yaml --log-format json

# Host A: request a session; the result is written locally on A
minipsi request --peer psi-b.example.org:7443 --dataset customers \
  --input /data/y.txt --output /data/intersection.txt \
  --cert /etc/minipsi/a.crt --key /etc/minipsi/a.key --ca /etc/minipsi/ca.crt \
  --peer-name psi-b.example.org --report /var/log/minipsi/req-$(date +%F).json
```

- The requester must use the dataset's hash and λ (mismatch → exit 4 naming the parameter).
- Busy daemon: `request` retries for `--connect-retry` (default 2m), then exit 3.
- Unknown or not-allowed dataset: exit 4, "dataset not available".
- The daemon re-reads the dataset file for every session (update it atomically, e.g. write
  a temp file and rename). A failed session never stops the daemon.
- **Trust boundary:** with `allowed_peers: []` every holder of a certificate from `ca.crt`
  can query every dataset, repeatedly. Use a CA dedicated to the PSI partnership, or list
  the allowed peers' SANs.
- Local test on one machine:
  `minipsi serve --dev --name b --listen 127.0.0.1:7443 --dataset customers=b.txt` and
  `minipsi request --dev --name a --peer 127.0.0.1:7443 --dataset customers --input a.txt --output out.txt`.
- `mode: relay` (via the Orchestrator) is reserved and rejected until implemented.

## 5. Exit codes and troubleshooting

| Exit | Meaning | Typical causes → action |
|---|---|---|
| 0 | success | — |
| 1 | usage / configuration | bad flag/YAML key, missing cert files, `--insecure-dev` without acknowledgement → fix config |
| 2 | input data | unreadable input, own input over `--max-*` limits, H2F collision (practically impossible) → check input/limits |
| 3 | network / timeout | peer not reachable, TLS failure (wrong CA, expired cert, name mismatch, `--peer-name` mismatch), peer vanished, daemon busy past `--connect-retry` or dataset file missing on the daemon, `--phase-timeout`/`--timeout` → check firewall, certificates (`openssl x509 -text`), clocks, timeouts |
| 4 | protocol / peer abort / interrupted | parameter mismatch (hash, λ, instance IDs), unknown/not-allowed dataset or "sender dataset unavailable" (daemon), peer limits exceeded, peer aborted or was interrupted, SIGINT/SIGTERM → compare both sides' logs (the peer's error code/message is logged) |
| 5 | internal error | bug → keep logs + report, file an issue |

The JSON report (`--report`) is written on success and failure: phase timings, bytes,
SID (correlates both sides' logs), peer certificate identity, error.

## 6. Capacity planning

`minipsi bench --n1 N --n2 N [--instances B]` runs both parties in one process (so on one
host it measures the *sum* of both parties' work). Bandwidth ≈ 32 B × receiver points (R→S) +
ℓ_out (≈ 8–11 B) × sender items (S→R).

## 7. Operational security

- Use organisational certificates and `--peer-name` on both sides; never `--insecure-dev`
  outside a lab.
- Disable core dumps and use encrypted swap on both hosts (secrets live in memory).
- Logs and reports contain no items, but outputs do: protect `--output` (written 0600).
- Both parties must normalise items identically before the run; minipsi does not.
