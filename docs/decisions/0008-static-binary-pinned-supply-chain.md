# ADR-0008: Static binary, distroless image and pinned tools and actions

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

## Status

Accepted (recorded retroactively from repository evidence; original date and alternatives
considered are unknown).

## Context

minipsi handles private data sets and private keys. It is deployed as a single binary or a
container on hosts run by different organisations, and builds should be reproducible and resistant
to supply-chain changes.

## Decision

- The binary is built with `CGO_ENABLED=0` and `-trimpath`, with version and commit stamped by
  ldflags, both in the Makefile and in the Dockerfile.
- The container image copies the binary into `gcr.io/distroless/static-debian12:nonroot` and runs
  as `nonroot:nonroot`. Both base images are pinned by digest, to be updated deliberately.
- Development tools (staticcheck, govulncheck, buf, protoc-gen-go, protoc-gen-go-grpc) are pinned
  to exact versions in the Makefile.
- GitHub Actions are pinned by commit SHA and bumped deliberately to releases at least two weeks
  old; the workflow has read-only permissions. The Makefile applies the same two-week rule to
  staticcheck.

## Consequences

- The image has no shell or package manager; debugging happens with logs and reports, and
  mounted directories must be readable and writable by uid 65532.
- Updating a base image, tool or action is a deliberate change with a visible diff.
- Pinned tools can lag the Go toolchain: staticcheck runs under a Go 1.26 toolchain until a
  release supporting Go 1.27 is old enough.

## Evidence

- `Dockerfile:2-6` (`distroless`): static binary on distroless, base images pinned by digest.
- `Dockerfile:13-15` (`CGO_ENABLED`): static build with ldflags.
- `Dockerfile:17-21` (`nonroot`): distroless nonroot runtime.
- `Makefile:10-18` (`STATICCHECK_VERSION`): pinned tools and the two-week rule.
- `Makefile:27` (`CGO_ENABLED`): local static build.
- `.github/workflows/ci.yml:3` (`pinned`): actions pinned by SHA, releases at least two weeks old.
- `.github/workflows/ci.yml:12-13` (`permissions`): read-only token.
- `.github/workflows/ci.yml:28-29` (`checkout`): SHA-pinned actions.

Related: [INTEGRATIONS.md](../INTEGRATIONS.md#packaging-and-ci), [DEVELOPMENT.md](../DEVELOPMENT.md),
[RUNBOOK.md](../../RUNBOOK.md#container).
