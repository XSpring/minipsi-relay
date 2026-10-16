# ADR-0004: RNS NTT polynomial arithmetic with a pluggable local backend

Source references are at commit `4aa7c9f`; if lines drift, search for the symbol.

## Status

Accepted (recorded retroactively from repository evidence; original date and alternatives
considered are unknown).

## Context

The receiver interpolates a polynomial through its points and the sender evaluates that
polynomial at all of its items, both over F = GF(2^255-19). For large sets this needs fast
polynomial multiplication inside subproduct and remainder trees.

## Decision

- Polynomial multiplication dispatches between schoolbook, Karatsuba and an RNS NTT. The RNS
  path lifts coefficients to residues modulo K word-sized NTT primes, convolves them with a
  transform backend and recombines the result with Garner's CRT. The package doc states the
  bound that makes the CRT result exact.
- The transform is behind a `Backend` interface with two implementations: `native` (in-house
  cyclic NTT, the default) and `lattigo` (Lattigo v6 negacyclic RNS NTT).
- The backend is a local choice: it is not sent on the wire, and peers may use different
  backends. It is selected with `--ntt-backend` / `ntt_backend`.

## Consequences

- No CGO or external math library is required by default; Lattigo is linked in but used only
  when selected.
- Both backends must produce the same linear convolution for products within the transform
  length; tests run a receiver and a sender with different backends.
- Unknown backend names are rejected by configuration validation and again by `NewBackend`.
- Why two backends exist (for example benchmarking or cross-checking) is not stated in the
  repository.

## Evidence

- `internal/poly/doc.go:1-4` (`Karatsuba`): dispatch between schoolbook, Karatsuba and RNS-NTT.
- `internal/rns/rns.go:1-6` (`Garner`): multi-modular NTT and the exactness bound.
- `internal/rns/rns.go:262-267` (`Backend`): pluggable backend interface.
- `internal/rns/rns.go:281-290` (`NewBackend`): `native` and `lattigo`.
- `internal/rns/lattigo.go:11-14` (`lattigoBackend`): Lattigo negacyclic NTT.
- `internal/protocol/session.go:40` (`NTTBackend`): "local choice".
- `internal/protocol/protocol_test.go:137` (`NTTBackend`): "backend is a local choice; mix them".
- `internal/config/config.go:228-231` (`ntt_backend`): validation.
- `go.mod:27` (`lattigo`): Lattigo v6.2.0 dependency.

Related: [INTEGRATIONS.md](../INTEGRATIONS.md#go-libraries), [ARCHITECTURE.md](../ARCHITECTURE.md).
