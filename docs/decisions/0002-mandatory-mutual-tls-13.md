# ADR-0002: Mutual TLS 1.3 is mandatory; plaintext and dev PKI are fenced off

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

## Status

Accepted (recorded retroactively from repository evidence; original date and alternatives
considered are unknown).

## Context

The protocol is semi-honest: it protects item privacy against a peer that follows the protocol,
but it does not authenticate the peer by itself. Both parties need to know who they are talking
to, and the daemon needs an identity to check against `allowed_peers`. Developers also need a
quick way to run two processes on one machine.

## Decision

- The transport refuses to start without a TLS configuration whose minimum version is TLS 1.3.
- The server requires and verifies a client certificate against the configured CA; the client
  verifies the server certificate and server name. `--peer-name` additionally pins a DNS or URI
  SAN on either side.
- Plaintext (h2c) needs both `--insecure-dev` and the environment variable
  `MINIPSI_I_UNDERSTAND_INSECURE=1`, cannot be combined with TLS files, and logs a warning.
- `--dev` uses a shared, automatically created development PKI. Validation allows it only with
  loopback addresses and never together with TLS files or plaintext; it also logs a warning.
  `certs dev` creates throw-away certificates and never overwrites existing files.

## Consequences

- Production runs need a CA and a certificate per party; certificate SANs double as identities
  for pinning and for the daemon's allow-list.
- A misconfigured TLS setup fails during connection setup (exit 3 after the retry window), not in
  the middle of a run.
- Running in plaintext takes two deliberate steps, which keeps it out of scripts by accident.
- The dev PKI keys sit unencrypted in a local state directory; this is acceptable only because
  dev mode is restricted to loopback.

## Evidence

- `internal/transport/transport.go:86-109` (`normalize`): TLS required, TLS 1.3 minimum, insecure gate.
- `internal/transport/transport.go:39-43` (`InsecureEnv`): environment acknowledgement for plaintext.
- `internal/transport/tls.go:48-60` (`RequireAndVerifyClientCert`): server config.
- `internal/transport/tls.go:62-73` (`ClientConfig`): client config with server name and pinning.
- `internal/transport/tls.go:75-95` (`pinPeer`): SAN pinning.
- `internal/config/config.go:263-286` (`validTLS`): dev only on loopback, no mixing of modes.
- `internal/cli/party.go:349` (`PLAINTEXT`): plaintext warning.
- `internal/cli/party.go:400` (`DEV`): dev-mode warning.
- `internal/devcerts/devpki.go:45-50` (`EnsureDev`): "Not for production use."
- `internal/devcerts/devcerts.go:125-127` (`WriteFiles`): existing files are not overwritten.

Related: [INTEGRATIONS.md](../INTEGRATIONS.md#tls-and-pki),
[RUNBOOK.md](../../RUNBOOK.md#3-certificates).
