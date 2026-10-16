# Old plan review

The old project plan, called "handover", proposed a PSI engine that runs as a background process inside each Compute Cluster (CC). The plan kept the original C++ MiniPSI code unchanged and wrapped it through cgo behind a small C API. A CC app would drive the engine over an HTTP API (sessions, input upload, outbox and inbox, status, result, delete). Every protocol message would travel CC app -> Orchestrator (ORC) -> other CC app inside a committee-relay envelope. The work was split into 4 phases: (1) C++ core plus cgo, (2) the MiniPSI protocol in Go, (3) the HTTP engine and sessions, (4) Docker image and demo. The plan lives in three files that are not committed to this repository: `handover-plan.en.md` (English, 193 lines), `handover-plan.md` (Vietnamese, 338 lines, more detail) and `spec-orchestrator-compute-clusters.md` (the platform spec for ORC and CC communication, Vietnamese, updated 30/09/2026; its "Committee Relay Envelope" section is an empty heading).

The repository today is `minipsi` (module `github.com/XSpring/minipsi`), reviewed at commit `4aa7c9f`. It is a pure-Go implementation of the semi-honest polynomial-based Diffie-Hellman PSI over Curve25519 and Elligator2, built as a static `CGO_ENABLED=0` binary (README.md:3-5). It is a CLI with one-shot `receiver` and `sender` commands, a sender-only `serve` daemon and a `request` client. The two parties talk directly over one gRPC bidirectional stream with mutual TLS 1.3. `--mode relay` ("via the Orchestrator") is reserved and rejected (internal/config/config.go:252-259). The PSI core sits behind a narrow seam in `internal/protocol`: `RunReceiver`, `RunSender`, the `Conn` interface (Send and Recv of a `Frame`) and the `Frame` type (internal/protocol/conn.go:12-15, internal/protocol/messages.go:28-38). See [ENTRY_POINTS.md](ENTRY_POINTS.md#seam) for the seam and [CALL_GRAPHS.md](CALL_GRAPHS.md#cg-06) for the protocol session call graph.

**What changes for the team.** The cgo and C++ wrapping is no longer needed, because the repository already has a pure-Go PSI core. The next step is to design the interface signatures of a new API (an engine and session API, and relay through the Orchestrator) on top of that seam. This review checks every item of the old plan against the repository and rewrites the plan for that goal.

**How the review was done.** The plan was split into 6 areas. In each area one agent checked every plan statement against the code and tests, and a second agent verified those results adversarially: it re-ran the targeted tests, re-checked line numbers and changed statuses where needed. Statuses:

| Status | Meaning |
|---|---|
| done | The repository does what the plan says |
| done differently | The plan's goal is met, by a different design |
| partial | Part of the item exists |
| missing | Nothing in the repository implements it |
| obsolete | The item no longer applies (mostly the C++/cgo work) |
| conflict | The repository contradicts a plan decision |
| beyond plan | The repository has something the plan does not mention |

**Reference notation.** Repository references are `path:line` at commit `4aa7c9f`. Old plan references are plain text: in the area tables, "en 38" means handover-plan.en.md line 38, "vi 54" means handover-plan.md line 54, and "spec 42" means spec-orchestrator-compute-clusters.md line 42. Some plan lines are checked in more than one area, so the per-area counts overlap.

## Summary

| Area | Items | Done | Done differently | Partial | Missing | Obsolete | Conflict | Beyond plan |
|---|---|---|---|---|---|---|---|---|
| [core-crypto](#core-crypto) | 33 | 4 | 17 | 0 | 0 | 8 | 1 | 3 |
| [protocol-flow](#protocol-flow) | 29 | 8 | 10 | 0 | 2 | 1 | 2 | 6 |
| [io-data](#io-data) | 23 | 5 | 3 | 3 | 1 | 1 | 1 | 9 |
| [engine-session](#engine-session) | 34 | 2 | 5 | 9 | 10 | 1 | 1 | 6 |
| [spec-orc](#spec-orc) | 27 | 2 | 3 | 5 | 11 | 0 | 1 | 5 |
| [packaging-docs](#packaging-docs) | 26 | 2 | 6 | 3 | 3 | 3 | 0 | 9 |
| **Total** | **172** | **23** | **44** | **20** | **27** | **14** | **6** | **38** |

## Key conclusions

- **Phase 1 (C++ core plus cgo) is obsolete.** There is no cgo, no C or C++ source, no MiniPSI submodule and no NTL, GMP, gf2x or libsodium. Every primitive the C API would have exposed has a pure-Go replacement: `elligator.EncodeBatch`, `poly.Arith.Interpolate`, `poly.Evaluator`, `elligator.MapToPoint` and edwards25519 multiplication in `internal/group`. The build is static (Makefile:27, Dockerfile:13).
- **Phase 2 (protocol in Go) is done, but differently.** The math runs over GF(2^255-19) with one polynomial of 32-byte coefficients per instance, not two polynomials over a 128-bit field. There is no Rijndael-256 permutation Pi: the interpolated values are uniform Elligator2 preimages (internal/elligator/elligator.go:164-165). The wire has 7 frame kinds (Hello, SizesChunk, SenderKeys, PolyChunk, OutputChunk, Done, Abort) instead of the plan's 3 messages (gk, poly, masks) (api/minipsi/v1/psi.proto:14-24). The sender sends hashed outputs truncated to l_out bytes (about 8-11 B at lambda = 40), not 32-byte DH masks (internal/hashing/hashing.go:57-69). The only Phase 2 criterion not met is the golden test with MiniPSI's sample CSV files.
- **The main conflict is networking.** The spec allows no direct CC to CC path (spec-orchestrator-compute-clusters.md line 42), and the plan agreed (handover-plan.en.md line 13). The only implemented mode is a direct gRPC mTLS link between the two parties, including a standing `serve` listener. Relay mode is reserved and rejected (internal/config/config.go:252-259).
- **Phase 3 (HTTP engine and sessions) is mostly missing.** There is no HTTP server, session registry, inbox or outbox, status, result or delete endpoint, and no `/healthz`. There is no envelope header (`session_id`, `sender_cc_id`, `receiver_cc_id`, `envelope_seq`, `protocol_phase`, `payload_digest`), no sequence or digest check and no `committee.*` or `handover.*` code. Parts of Phase 3 exist in another form: chunking (MaxFrame, 4 MiB by default), handshake and phase timeouts, a one-session-at-a-time daemon, numeric `ERR_*` Abort codes, and a per-direction SHA-256 transcript checked at Done.
- **The seam for the new API already exists.** `RunReceiver` and `RunSender` take any `protocol.Conn` (internal/protocol/receiver.go:40, internal/protocol/sender.go:28). An inbox/outbox adapter or a relay client can be a new `Conn` without changing the protocol. The protocol is not a message-in/message-out state machine, so that adapter must deliver frames in order and without loss, and a session cannot resume after a restart.
- **Phase 4 (image and demo) is partly done.** The image is a 16.7 MB distroless nonroot static binary, built and smoke-tested in CI (Dockerfile:17-21, .github/workflows/ci.yml:92-99). There is no healthcheck, no `docker-compose.yml`, no sample data, no OpenAPI (there is no HTTP API to describe) and no CC-app integration guide. Configuration is flags plus YAML, not environment variables.
- **The design differences need a crypto review.** The plan's trust argument was "the paper's C++ runs unchanged". The repository runs a modified construction: no Pi, torsion-randomized uniform-preimage encoding, l_out-truncated hashes bound to the session ID and instance, receiver padding and per-instance keys. The repository justifies these only in code comments and statistical tests. The spec that internal/field/field.go:29 refers to ("the lp() of the spec") is not in the repository.
- **Input handling has one real conflict.** The plan trims leading and trailing whitespace. The repository compares raw bytes and never trims (internal/itemio/itemio.go:3, RUNBOOK.md:23). Default limits are also higher than the plan's 2^20 (2^22 per instance, 2^24 per side).

<a id="core-crypto"></a>
## Area: core-crypto

The C++, cgo, NTL, submodule and C API items are obsolete. The five C API functions map to pure-Go code. Rijndael-256 Pi is gone, and the only repository justification is a uniformity comment plus a statistical test, so this needs crypto review. The verifier re-ran the poly, elligator, group, field, hashing, rns and protocol tests with `go test -short`; all passed.

| Item | Plan says | Status | Evidence (repo file:line) | Adjustment |
|---|---|---|---|---|
| core-1 | vi 54, 188-192; en 15, 80: change no C++ line; Go does no math, it only drives the original C++ | obsolete | No `import "C"`, no C/C++ files, no .gitmodules or third_party/. Pure-Go packages listed at README.md:67-68; internal/field/field.go:1 builds on filippo.io/edwards25519/field | Replace with "pure-Go core, CGO_ENABLED=0". Make the loss of paper-code provenance a crypto-review decision |
| core-2 | en 38: receiver interpolates with polyFFT.cpp `iterative_interpolate_zp` | done differently | `poly.Arith.Interpolate` internal/poly/evaluator.go:57-61 -> `Tree.Interpolate` internal/poly/tree.go:250-272; RNS multiplication over 9 primes internal/rns/rns.go:1-38; called at internal/protocol/receiver.go:208 | Mark done: interpolation over GF(2^255-19) in internal/poly |
| core-3 | en 39: sender evaluates with `build_tree`/`evaluate` | done differently | `NewEvaluator`/`Prepare`/`Evaluate` internal/poly/evaluator.go:20-55; used at internal/protocol/sender.go:181-191 | Mark done (pure-Go remainder tree, goroutine-parallel) |
| core-4 | en 40, 54, 57: Rijndael-256 with a fixed key as ideal permutation Pi on both sides | obsolete | No Rijndael or Pi anywhere. The interpolated value is the Elligator2 preimage S, argued uniform at internal/elligator/elligator.go:164-165; the sender maps P(H(x)) directly with `MapToPoint` at internal/protocol/sender.go:275 | Drop Pi. Crypto review must confirm Pi is not needed. Locate and commit the spec cited at internal/field/field.go:29 |
| core-5 | en 41: Elligator2 `ScalarBaseMult2` and `RepresentativeToPublicKey2` | done differently | RFC 9380 map internal/elligator/elligator.go:53-74; 4 preimages internal/elligator/elligator.go:137-162; `EncodeBatch` adds a random 8-torsion point and picks a random preimage internal/elligator/elligator.go:191-196; `MapToPoint` internal/elligator/elligator.go:110-135 | Mark done; describe the RFC 9380 map with torsion and uniform preimage choice |
| core-6 | en 42: libsodium X25519 | done differently | edwards25519 with u-coordinate output internal/group/group.go:106-109; unclamped scalars internal/group/group.go:82-89; explicit cofactor clearing internal/group/group.go:130-141; K validated as non-identity prime-order point internal/group/group.go:143-166 | Mark done as X25519-equivalent edwards25519 multiplication |
| core-7 | en 44: link liblibPSI.a, libcryptoTools.a, NTL, GMP, gf2x, libsodium | obsolete | Makefile:27 and Dockerfile:13 build with CGO_ENABLED=0; distroless runtime Dockerfile:17-21; go.mod lists only Go modules | Drop; record "static pure-Go binary, distroless image" as done |
| core-8 | en 45: C++ test program run on 2026-10-13 | obsolete | No C++ harness. Go equivalents: `TestInterpolate` internal/poly/poly_test.go:257, `TestEncodeUniformRoundTrip` internal/elligator/elligator_test.go:209 | Drop |
| core-9 | en 46-47, 86: Go writes the control flow, item hashing and mask shuffle | done | Receiver internal/protocol/receiver.go:40-160; sender internal/protocol/sender.go:28-105; `HashToField` internal/field/field.go:87-98; ChaCha8 shuffle internal/protocol/sender.go:135-150 | Mark done. The shuffle permutes evaluation points, so outputs leave in permuted order |
| core-10 | en 17: hash each item to 128 bits | done differently | SHA-512 wide-reduced into GF(2^255-19) internal/field/field.go:87-98; collisions between distinct items -> `CodeInput` internal/protocol/receiver.go:204-206 | Rewrite as "domain-separated SHA-512 hash-to-field" |
| core-11 | en 48, 89: validate in Go because NTL calls abort() | done differently | No abort path. `ErrDuplicatePoint` internal/poly/tree.go:261-265 -> `CodeInput` internal/protocol/receiver.go:209-210; receiver dedup internal/protocol/padding.go:66-93; `CheckInput` internal/protocol/padding.go:95-113 | Rewrite: the core returns errors; dedup and the m >= 2 floor live in the protocol |
| core-12 | en 50: Go allocates every C buffer | obsolete | No cgo boundary | Drop |
| core-13 | en 54: `hc_recv_encode` returns Pi(representative(g^r)) and r | done differently | `elligator.EncodeBatch` internal/elligator/elligator.go:191-196, no Pi; called per batch of 256 at internal/protocol/receiver.go:184-194 | Mark done as `EncodeBatch`; remove Pi |
| core-14 | en 55: `hc_interpolate`, x and y of 16 B | done differently | `poly.Arith.Interpolate` on 32-byte elements internal/poly/evaluator.go:57-61; called at internal/protocol/receiver.go:208 | Mark done; sizes are 32 B |
| core-15 | en 56, 84: evaluate in groups of size(Y), pad last group, dummy for a 1-item R | done differently | Balanced chunks of at most max(n2, 64) points internal/poly/evaluator.go:20-36, used at internal/protocol/sender.go:188; m >= 2 floor internal/protocol/padding.go:44-53 | Mark done; no last-group padding is needed |
| core-16 | en 57: `hc_send_decode` applies Pi^-1 then decodes a point | done differently | `MapToPoint`, no Pi^-1: internal/elligator/elligator.go:110-135, internal/protocol/sender.go:275; no-image case gives random l_out bytes internal/protocol/sender.go:276-290 | Mark done as `MapToPoint` |
| core-17 | en 58: `hc_x25519` for g^k, (g^r)^k, (g^k)^r | done differently | g^k internal/protocol/sender.go:116-121; (g^r)^k internal/protocol/sender.go:280-283; (g^k)^r internal/protocol/receiver.go:343-351 | Mark done; one key k_b per instance |
| core-18 | en 71: 2 polynomials of 16-byte coefficients | done differently | One polynomial of 32-byte canonical coefficients api/minipsi/v1/psi.proto:65-69; strict decode internal/protocol/sender.go:340-349, internal/field/field.go:37-54 | Rewrite; wire size unchanged (about 32 MiB at 2^20) |
| core-19 | en 72, 76: 32-byte DH masks compared directly | done differently | l_out-byte hashed outputs, l_out = min(32, ceil((lambda + log2 pairs)/8)) internal/hashing/hashing.go:57-69; h_b keyed by SID and instance internal/hashing/hashing.go:126-148; SID internal/protocol/session.go:452-457; about 8-11 B at lambda 40 RUNBOOK.md:161-163 | Rewrite; add lambda and hash to the session parameters both sides must agree on |
| core-20 | en 85: remove duplicates before running | done | CLI dedup for both roles internal/itemio/itemio.go:187-205, logged at internal/cli/party.go:217-219; receiver dedup internal/protocol/padding.go:66-93 | Mark done |
| core-21 | en 88: parallelism from NTL threads and goroutines | done differently | Goroutines only: internal/poly/arith.go:43-61, internal/rns/multiplier.go:60, internal/rns/multiplier.go:69 | Rewrite as "goroutine pool (Workers)" |
| core-22 | en 14, 62, 80: keep exactly MiniPSI `-i`, math and crypto unchanged | conflict | Modified construction: no Pi, torsion encoding internal/elligator/elligator.go:164-165, truncated hashed outputs internal/hashing/hashing.go:57-69, padding internal/protocol/padding.go:8-31, per-instance keys internal/protocol/sender.go:107-121. No document explains the deviations | Team decision: a documented pure-Go variant (spec committed) plus crypto review, or a return to the paper's construction |
| p1-1 | en 156: MiniPSI as an unmodified git submodule | obsolete | No .gitmodules, no third_party/; the module is pure Go (go.mod) | Drop |
| p1-2 | en 157: write cpp/handover_core.cpp (5-function C API) | obsolete | No C/C++ sources; functions replaced as in core-13 to core-17 | Drop; keep the mapping for reviewers |
| p1-3 | en 158: Go `core` package (cgo) and a Dockerfile | done differently | Core split into internal/field, group, elligator, hashing, poly, rns, ntt, parallel (README.md:67-68); multi-stage Dockerfile:1-21 with CGO_ENABLED=0 | Mark done; list the packages |
| p1-4 | en 159: `go test ./core` runs in Docker | done differently | Tests run on the host or in CI (`make race`, .github/workflows/ci.yml:35-44); the Docker job builds and runs `version` (.github/workflows/ci.yml:92-99) | Replace with "go test ./... passes (make race in CI)" |
| p1-5 | en 160: interpolate then evaluate gives back y | done | internal/poly/tree.go:250-272, internal/poly/evaluator.go:46-61; `TestInterpolate` internal/poly/poly_test.go:257 | Mark done |
| p1-6 | en 161: Pi^-1(Pi(v)) = v | obsolete | No Pi; closest is the Elligator2 round trip internal/elligator/elligator.go:137-162 | Drop, or replace with the Elligator2 round-trip test after the crypto review |
| p1-7 | en 162: Elligator plus X25519 give the same key on both sides | done | Sender internal/protocol/sender.go:275-283; receiver internal/protocol/receiver.go:343-351 | Mark done |
| p1-8 | en 163: Go rejects duplicate x and n < 2 before calling C++ | done differently | Duplicates are deduplicated internal/protocol/padding.go:66-93; collisions -> input error internal/protocol/receiver.go:204-210; n = 0 allowed internal/poly/tree.go:257-259; k = 1 padded to m = 2 internal/protocol/padding.go:11-15 | Mark done. The sender accepts a peer size of 1 (internal/protocol/session.go:433-440), acceptable only for semi-honest peers |
| bp-1 | vi 173-174 (implicit 128-bit field) | beyond plan | GF(2^255-19) internal/field/field.go:1-21; RNS exact for products up to 2^23 internal/rns/rns.go:1-38; NTT backend native or Lattigo internal/rns/lattigo.go:11-22 | Record as implemented parameters |
| bp-2 | en 14, 18: one polynomial, no bins; size hiding out of scope | beyond plan | Receiver padding internal/protocol/padding.go:8-31; sender dummy outputs internal/protocol/padding.go:55-64; batched instances internal/itemio/itemio.go:28-30, internal/protocol/sender.go:107-121 | Record; team decides whether batched mode stays in scope |
| bp-3 | not in plan | beyond plan | Canonical decoding internal/field/field.go:37-54; prime-order key check internal/group/group.go:143-166; domain separation internal/hashing/hashing.go:51-55; experimental AES-MMO hash internal/hashing/hashing.go:19-22 | Record as hardening; decide whether the AES hash is allowed in production |

<a id="protocol-flow"></a>
## Area: protocol-flow

The repository replaces the plan's 3 messages with 7 frame kinds and an explicit handshake. All the section 5 control changes of the old plan are met by other means. Phase 2 is met except the MiniPSI sample-data criterion. The API is a blocking run over `Conn`, not a state machine. At 2^20 items with default settings the poly direction is about 32 MiB and the output direction about 11 MiB.

| Item | Plan says | Status | Evidence (repo file:line) | Adjustment |
|---|---|---|---|---|
| flow-1 | en 62-64: keep MiniPSI's 3 messages (gk, poly, masks); add none | conflict | 7 frame kinds internal/protocol/messages.go:28-38, api/minipsi/v1/psi.proto:14-24; Hello and SizesChunk both ways internal/protocol/session.go:362-387; Done both ways internal/protocol/receiver.go:134-144, internal/protocol/sender.go:95-104; Abort internal/protocol/errors.go:76-101 | Team decision. Recommended: accept the 7 kinds and rewrite the message table around them |
| flow-2 | en 68-72; spec 54: each message has a `protocol_phase` in the envelope header | missing | No `protocol_phase`. The frame kind is the protobuf oneof (api/minipsi/v1/psi.proto:14-24, internal/protocol/messages.go:97-106) | Relay adapter defines one phase value per frame kind |
| flow-3 | en 70: gk, S -> R, 32 B | done differently | Uniform k_b per instance, K_b as a 32-byte Edwards encoding in SenderKeys internal/protocol/sender.go:107-128; checked by `group.DecodeKey` internal/protocol/receiver.go:445-461 | Rewrite as "SenderKeys: one 32-byte key per instance, after the handshake" |
| flow-4 | en 71: poly, R -> S, 2 polynomials of 16 B, values Pi(Elligator(g^r)) | done differently | One polynomial per instance over GF(2^255-19) internal/protocol/receiver.go:172-230; dummy points internal/protocol/receiver.go:264-300; PolyParts with offset internal/protocol/receiver.go:232-253; sender requires exactly m_b coefficients internal/protocol/sender.go:340-342 | Rewrite (m_b x 32 B, about 32 MiB at 2^20); crypto review for the dropped Pi |
| flow-5 | en 72: masks, S -> R, 32 B per sender item, shuffled | done differently | Output = h_b(u(...)) truncated to l_out; no-image values give random bytes internal/protocol/sender.go:261-295; l_out internal/hashing/hashing.go:57-69; permuted order internal/protocol/sender.go:198-259 | Rewrite as OutputChunk; about 11 MiB at 2^20 (l_out = 11 B) |
| flow-6 | vi 173-174: two polynomials because 32 B does not fit a 16-byte coefficient | obsolete | 32-byte field elements internal/field/field.go:17-21; one polynomial per instance internal/protocol/receiver.go:200-218 | Drop |
| flow-7 | en 74: messages are one-way; gk and poly may be sent at the same time | done | Receiver streams P without waiting for keys internal/protocol/receiver.go:104-121; sender sends keys without waiting for P internal/protocol/sender.go:84-94 | Mark done; add the handshake and final Done to the flow |
| flow-8 | en 75: set sizes are not sent separately | done differently | SizesChunk both ways, checked against limits internal/protocol/session.go:376-442; receiver announces padded m_b internal/protocol/receiver.go:52-56 | Rewrite: padded sizes are announced in the handshake |
| flow-9 | en 76: R compares with the masks and returns the original values | done differently | Compare values computed early internal/protocol/receiver.go:310-376; matching after the Done exchange internal/protocol/receiver.go:131-147; result is indices internal/protocol/receiver.go:29-34; CLI writes original bytes internal/itemio/itemio.go:228-261 | Rewrite the step |
| flow-10 | en 17: hash items to 128 bits | done differently | SHA-512 into GF(2^255-19) internal/field/field.go:87-98; collisions -> ERR_INPUT internal/protocol/receiver.go:204-210 | Rewrite (same as core-10) |
| ctrl-1 | en 62, 80: Go re-implements no math | conflict | All math is pure Go: internal/field/field.go:1-5, internal/poly/doc.go:1-10, internal/elligator/elligator.go:1-9, internal/group/group.go:1-4; static build README.md:15 | Team decision: accept the pure-Go core with a crypto review |
| ctrl-2 | en 84: evaluate in groups of size(Y), pad the last group | done differently | `NewEvaluator(z, n2)` internal/protocol/sender.go:181-193; balanced chunks internal/poly/evaluator.go:10-36 | Rewrite; drop the padding step |
| ctrl-3 | en 84: add a dummy when R has 1 item | done | m >= 2 floor internal/protocol/padding.go:8-15; one random point by default internal/protocol/padding.go:22-24 | Mark done (reason in repo: m = 1 makes the polynomial constant) |
| ctrl-4 | en 85: remove duplicate items | done | itemio internal/itemio/itemio.go:187-212; receiver internal/protocol/padding.go:66-93 | Mark done. `RunSender` does not dedup, so non-CLI callers must dedup sender input |
| ctrl-5 | en 86: S shuffles the masks | done | ChaCha8 shuffle of the evaluation order internal/protocol/sender.go:130-150; schema states permuted order api/minipsi/v1/psi.proto:77 | Mark done |
| ctrl-6 | en 87, 118: chunks of at most 1 MiB | done differently | Packer fills MaxFrame minus 1024 bytes internal/protocol/receiver.go:496-560; MaxFrame 4 MiB default, 64 KiB minimum internal/protocol/session.go:70-72; `frameReserve` internal/protocol/messages.go:197-202; OutputParts carry no offset api/minipsi/v1/psi.proto:65-82 | Set max_frame to 1 MiB for relay or adopt 4 MiB; require in-order delivery |
| ctrl-7 | en 88: one message stream per session | done | One ordered `Conn` internal/protocol/conn.go:9-15 over one gRPC bidi stream api/minipsi/v1/psi.proto:10-12; worker goroutines internal/parallel/parallel.go:18-87 | Mark done; the stream is direct today (see spec-orc net-1) |
| ctrl-8 | en 89: Go validates input before C++ | done differently | `CheckInput` internal/protocol/padding.go:95-114, called at internal/cli/party.go:230; peer frames validated internal/protocol/session.go:393-442, internal/protocol/sender.go:328-352, internal/protocol/receiver.go:445-482; `parallel.For` workers have no recover internal/parallel/parallel.go:61-71 | Rewrite; add panic recovery before running many sessions in one process |
| p2-1 | en 168: Sender and Receiver as message-in/message-out state machines | done differently | Blocking `RunReceiver` internal/protocol/receiver.go:36-71 and `RunSender` internal/protocol/sender.go:26-49 over `Conn`; `PipeConn` internal/protocol/conn.go:17-84; gRPC `streamConn` internal/transport/transport.go:139-263 | Replace with a relay or inbox/outbox `Conn` adapter. Rewrite as a state machine only if sessions must survive restarts |
| p2-2 | en 169: dedup, grouped evaluation, mask shuffle | done | internal/protocol/padding.go:66-93; internal/poly/evaluator.go:20-36; internal/protocol/sender.go:137-150 | Mark done |
| p2-3 | en 170: no networking in Phase 2 | done | internal/protocol imports no net or gRPC package; in-memory pair internal/protocol/conn.go:17-84; `bench` internal/cli/tools.go:78-121 | Mark done |
| p2-4 | en 172: MiniPSI sample CSVs give the 100-item expected intersection | missing | No data/, testdata/ or *.csv in the repository | Add the files with a golden test, or replace the criterion with the generated-data tests |
| p2-5 | en 173: correct for unequal, 1-item and empty sets | done | internal/protocol/sender.go:181-193; internal/protocol/padding.go:43-53; internal/protocol/receiver.go:424-438; internal/itemio/itemio.go:215-218 | Mark done |
| beyond-1 | not in plan (vi 145, 180-182) | beyond plan | Hello and SizesChunk internal/protocol/messages.go:40-57; version, suite, role, hash, lambda, nonce and limit checks internal/protocol/session.go:362-464; SID internal/hashing/hashing.go:71-80 | Add the handshake to the plan; decide whether to bind the ORC `session_id` |
| beyond-2 | not in plan | beyond plan | Done with frame count and SHA-256 transcript internal/protocol/session.go:269-293, internal/protocol/messages.go:108-169; Abort with ERR_* codes internal/protocol/errors.go:10-30 | Add Done and Abort; map ERR_* to the platform's abort signals and error names |
| beyond-3 | not in plan (vi 56) | beyond plan | Instances internal/protocol/session.go:21-26; waves internal/protocol/session.go:509-524; instance column internal/itemio/itemio.go:27-30; RUNBOOK.md:73-75 | Decide whether the API exposes batched mode |
| beyond-4 | en 18: size hiding out of scope | beyond plan | Receiver and sender padding policies internal/protocol/padding.go:8-64; flags internal/cli/party.go:90-99; RUNBOOK.md:77-82 | Decide the default for platform runs (default receiver padding reveals k = m-1) |
| beyond-5 | not in plan | beyond plan | Comparison hash SHA-256 or experimental AES internal/hashing/hashing.go:16-43; lambda default 40 internal/protocol/session.go:64-66; negotiated in Hello internal/protocol/session.go:404-407 | Add hash and lambda as session parameters; decide on AES |
| beyond-6 | not in plan (vi 108-110, NTL) | beyond plan | NTT backend chosen locally, not negotiated internal/protocol/session.go:174-183 | Mention as a local engine setting |

<a id="io-data"></a>
## Area: io-data

Every input and output decision has a counterpart, usually by a different design. The default `lines` format covers the plan's 1-column file; a real CSV reader and batched input go further. Normalisation is a real conflict. Limits are higher and finer-grained than 2^20 and are set by flags or YAML only. Two gaps were confirmed: a batched CSV cannot declare an empty instance, and standalone output is not CSV-quoted.

| Item | Plan says | Status | Evidence (repo file:line) | Adjustment |
|---|---|---|---|---|
| io-1 | en 17: 1-column CSV, no header, one item per line | done | Default `lines` format internal/itemio/itemio.go:73-98; default config internal/config/config.go:116; flags internal/cli/party.go:82-83 | Mark done; call the format "lines" |
| io-2 | vi 56: only a 1-column file | beyond plan | CSV reader with column and header internal/itemio/itemio.go:100-178; flags internal/cli/party.go:84-86 | Record as an input option the API could reuse |
| io-3 | vi 53: one set per side | beyond plan | Batched mode internal/itemio/itemio.go:136-176; a row with an empty item is skipped before its instance registers internal/itemio/itemio.go:161-164, although RUNBOOK.md:75 tells users to use empty instances | Record; fix the empty-instance gap or the runbook |
| io-4 | en 96: `PUT /v1/sessions/{id}/input` with a text/csv body | missing | No `net/http` import; input is a path or stdin internal/itemio/itemio.go:47-57 | Engine-API work; parse the body with `itemio.Read` (internal/itemio/itemio.go:60) |
| io-5 | vi 42: the CC app pushes its data | beyond plan | stdin `-` internal/itemio/itemio.go:47-49; serve datasets internal/config/serve.go:132-139; daemon rejects `-` internal/config/serve.go:186-187; datasets checked at startup internal/cli/serve.go:110-116 | Describe stdin and datasets; add a stdin test |
| io-6 | en 17, 47: 128-bit item hash written in Go | done differently | DST internal/field/field.go:26-27; `HashToField` internal/field/field.go:87-97 | Rewrite as SHA-512 hash-to-field |
| io-7 | vi 57: users do not hash their data | done | Items are raw bytes internal/itemio/itemio.go:3; hashing only in the protocol internal/protocol/sender.go:159 | Mark done |
| io-8 | en 48: catch collisions before the core | beyond plan | Collision check internal/protocol/receiver.go:264-275 -> `CodeInput` internal/protocol/receiver.go:204-206; RUNBOOK.md:151 | Record (exit 2) |
| io-9 | vi 57: no domain separation mentioned | beyond plan | DSTs internal/hashing/hashing.go:51-55; suite id internal/version/version.go:15 | Record as a fixed suite parameter |
| io-10 | en 17, 76: results are original values | done | `WriteResult` internal/itemio/itemio.go:231-261; matches are indices internal/protocol/receiver.go:379-415 | Mark done |
| io-11 | en 100: `GET .../result` as a 1-column CSV | partial | Standalone output is raw lines and an item with CR or LF fails internal/itemio/itemio.go:251-252; batched output is `instance_id,item` rows internal/itemio/itemio.go:228-261; no HTTP | Document the real formats; decide on a CSV-quoted standalone mode; GET stays API work |
| io-12 | vi 215-216: delete session data | beyond plan | Temp file, mode 0600, fsync, rename internal/itemio/itemio.go:263-297; RUNBOOK.md:170 | Carry over as a requirement for any API result storage |
| io-13 | en 17: trim leading and trailing whitespace only | conflict | No normalisation beyond the line terminator internal/itemio/itemio.go:3, internal/itemio/itemio.go:80-81; "compares raw bytes" RUNBOOK.md:23 | Team decision: raw bytes, or an opt-in trim both CCs set the same way |
| io-14 | vi 58: no other normalisation; both sides agree on conventions | done | internal/itemio/itemio.go:3; RUNBOOK.md:171 | Mark done; keep the note in the integration guide |
| io-15 | vi 56-58: no rule for empty lines or line endings | beyond plan | Empty lines skipped and CRLF tolerated internal/itemio/itemio.go:78-88; report counters internal/report/report.go:30-32 | Document the input rules for CC teams |
| io-16 | en 85: remove duplicates | done | internal/itemio/itemio.go:187-212; internal/protocol/padding.go:66-93 | Mark done |
| io-17 | en 48, 84, 89, 163: block n < 2 and duplicates before C++ | obsolete | No C++; m >= 2 floor internal/protocol/padding.go:43-53; dummy points internal/protocol/receiver.go:264-300 | Replace with "padding guarantees m >= 2; empty sets allowed" |
| io-18 | en 17: at most 2^20 items per side, configurable | done differently | max_instance_size 2^22, max_total_items 2^24, max_instances 2^24 internal/config/config.go:90-96, internal/config/config.go:119; flags internal/cli/party.go:115-118 | Team decision on the default, weighed against about 1 GB per 2^20 items (RUNBOOK.md:20) |
| io-19 | en 75: the limit applies per side | beyond plan | Own input checked before the network internal/protocol/padding.go:95-114; peer sizes checked internal/protocol/session.go:433-439 -> `CodeLimit` | Add to the plan; add a test for the peer max_total_items limit |
| io-20 | en 87, 118: 1 MiB chunks | done differently | MaxFrame 4 MiB internal/protocol/session.go:70-71; gRPC limit MaxFrame + 64 KiB internal/transport/transport.go:118 | Set max_frame to 1 MiB in a relay profile |
| io-21 | vi 59: limits avoid excess memory | beyond plan | memory_limit 2 GiB internal/config/config.go:122; `debug.SetMemoryLimit` internal/cli/party.go:472-485 | Add to the configuration list |
| io-22 | en 190: max set size via env vars | partial | Only GOMEMLIMIT internal/cli/party.go:475, MINIPSI_DEV_PKI internal/devcerts/devpki.go:32-35 and the insecure acknowledgement internal/transport/transport.go:94 | Add env overrides, or adopt mounted YAML for containers |
| io-23 | en 171-173: MiniPSI sample files; unequal, 1-item, empty sets | partial | No sample data; generated tests internal/protocol/protocol_test.go:104-125; scripts/e2e.sh:21-30 | Edge cases done; the MiniPSI sample check is optional |

<a id="engine-session"></a>
## Area: engine-session

None of the old plan's section 6 engine API exists. The repository runs one process per session, or a sender-only `serve` daemon called by `request`. Input and results are files; status is exit codes, logs and JSON reports. The reusable seam is `protocol.Conn`. The verifier re-ran the named protocol and transport tests; all passed.

| Item | Plan says | Status | Evidence (repo file:line) | Adjustment |
|---|---|---|---|---|
| api-1 | en 91: engine is driven over HTTP inside the CC | missing | No HTTP server; control is the cobra CLI and YAML internal/cli/root.go:18-26; peer link is gRPC api/minipsi/v1/psi.proto:10-12 | Team decision (see Phase A) |
| api-2 | en 95: `POST /v1/sessions` with session_id, psi_role, my_cc_id, peer_cc_id | partial | Role by subcommand internal/cli/party.go:41-47, enforced in Hello internal/protocol/session.go:402-403; peer by SAN pinning internal/transport/tls.go:75-95; SID derived internal/protocol/session.go:452-456 | Role and peer done; ORC session_id and CC ids remain |
| api-3 | en 96: `PUT .../input` | done differently | `--input`/`--format` internal/cli/party.go:82-87; validated before the network internal/cli/party.go:230; daemon datasets internal/cli/serve.go:111-116 | Mark done as file or dataset; an API must carry the format options |
| api-4 | en 97: `GET .../outbox` long-poll | missing | Internal writer feeds `conn.Send` internal/protocol/session.go:269-293 | Remaining: `Conn` adapter, Send -> outbox |
| api-5 | en 98: `POST .../inbox` | missing | `streamConn.Recv` internal/transport/transport.go:223-239; `session.recv` internal/protocol/session.go:306-331 | Remaining: `Conn` adapter, inbox -> Recv |
| api-6 | en 99: `GET /v1/sessions/{id}` status | missing | Exit codes internal/cli/root.go:38-69; logs internal/cli/serve.go:202-211; report internal/report/report.go:14-39 | Map states to these signals |
| api-7 | en 100: `GET .../result`, receiver only | done differently | `--output` written on success internal/cli/party.go:301-314, internal/itemio/itemio.go:228-297; the sender learns nothing internal/protocol/sender.go:19-24 | Content done; delivery is API work |
| api-8 | en 101: `DELETE` aborts and deletes all session data | partial | Signal -> Abort(ERR_CANCELLED) internal/protocol/session.go:243-249; scalar erasure only on success internal/protocol/receiver.go:149-151, internal/protocol/sender.go:101-103; reports stay internal/cli/serve.go:213-222 | Per-session abort handle; erase on abort paths too |
| api-9 | en 102: `GET /healthz` | missing | No HTTP server or gRPC health service; Dockerfile:19 and Dockerfile:21 set only USER and ENTRYPOINT | Choose `/healthz` or grpc.health.v1 |
| env-1 | en 104-116; spec 54: JSON message with the 6 header fields and a base64 payload | missing | `pb.Frame` oneof without header api/minipsi/v1/psi.proto:14-24; only a comment mentions an envelope internal/protocol/messages.go:197-198 | Envelope wraps a serialized `pb.Frame`, built by the adapter |
| env-2 | vi 165-171, 235-236: protocol_phase is gk, poly or masks | obsolete | 7 frame kinds internal/protocol/messages.go:28-38; `frameKind` internal/protocol/session.go:342-358 | Derive the phase from the frame kind |
| env-3 | en 87, 118: chunks with their own envelope_seq; index and count in the payload | done differently | Packer internal/protocol/receiver.go:496-560; MaxFrame limits internal/config/config.go:216-217; PolyPart has an offset, OutputPart only an instance id api/minipsi/v1/psi.proto:60-78 | Chunking done; the per-envelope size cap is a config decision |
| env-4 | en 124; spec 77: a seq gap -> `committee.envelope_sequence_gap` | missing | No seq field. Related: offset check internal/protocol/sender.go:337-339; key index internal/protocol/receiver.go:447-449; Done count internal/protocol/session.go:333-339 | Per-direction counter in the adapter |
| env-5 | en 125; spec 78: repeat or regression -> `committee.envelope_out_of_order` | missing | Caught only indirectly; a reordered same-size OutputChunk passes until Done | Monotonic check in the adapter |
| env-6 | en 126, 179: payload_digest checked; HTTP 400 `handover.*` | partial | Per-direction SHA-256 transcript internal/protocol/messages.go:108-169, checked at Done internal/protocol/session.go:333-339, before matching internal/protocol/receiver.go:136-147 | Add a per-envelope digest; keep the transcript as defence in depth |
| err-1 | en 120-126: spec error names | partial | Numeric ERR_* internal/protocol/errors.go:10-30 sent in Abort api/minipsi/v1/psi.proto:89-92; exit codes internal/cli/root.go:38-69, README.md:56-57 | Table ERR_* -> `handover.*`; `committee.*` only from the adapter |
| walk-1 | en 132-136: create, upload, get result, delete | done differently | Start receiver/sender or `request` against `serve` README.md:26-43, RUNBOOK.md:38-51; daemon loop internal/cli/serve.go:155-165 | Rewrite the walk-through, or keep it as the API target |
| walk-2 | en 134; spec 42-43: CC app carries messages through the ORC; no direct path | conflict | Direct gRPC mTLS 1.3 internal/transport/transport.go:356-389, internal/transport/transport.go:488-563, README.md:3-5; relay rejected internal/config/config.go:256-257, RUNBOOK.md:143 | Relay transport is the main remaining work |
| walk-3 | en 138: the CC app moves opaque messages until done | missing | The transport adapter moves frames itself internal/transport/transport.go:139-239 | Tied to the relay decision |
| walk-4 | en 88: one stream per session | done | One gRPC bidi stream per session internal/transport/transport.go:394-454; MaxConcurrentStreams(2) internal/transport/transport.go:381 | Mark done; a relay `Conn` keeps one ordered stream |
| p3-1 | en 178: build the section 6 API with inbox/outbox; 1 MiB chunks | partial | Chunking exists (env-3); no session package or endpoints | Split: chunking done; endpoints per the API decision |
| p3-2 | en 179: check envelope_seq and digest | partial | Ordered stream and transcript internal/protocol/session.go:333-339; no seq api/minipsi/v1/psi.proto:14-24 | Adapter work (Phase C) |
| p3-3 | en 180: timeouts | done | HandshakeTimeout 30 s, PhaseTimeout 10 min internal/protocol/session.go:82-87; `--timeout` internal/cli/party.go:278-282; daemon timeouts internal/config/serve.go:50-54 | Mark done; relay needs an idle timeout |
| p3-4 | en 180: limit on concurrent sessions | done differently | Fixed at 1, others refused as busy internal/transport/transport.go:418-426; sequential loop internal/cli/serve.go:155-165; client retry internal/cli/party.go:129-139 | Rewrite; `max_sessions` is a team decision |
| p3-5 | en 181: delete data when a session ends or aborts | partial | Buffers released internal/protocol/receiver.go:363; no partial output internal/itemio/itemio.go:263-297; reports stay internal/cli/serve.go:213-222; RUNBOOK.md:169 | Retention policy; erasure on abort |
| p3-6 | en 183: e2e with 2 engines and a fake CC app | partial | Direct mTLS e2e test/e2e/e2e_test.go:151, scripts/e2e.sh:21-106; in-memory pairs internal/protocol/protocol_test.go:62; scripted fake peer internal/protocol/protocol_test.go:405-498; no fake CC app or relay | Retarget: adapter `Conn`s through an in-memory fake ORC |
| p3-7 | en 184: a wrong seq returns the right code | missing | Closest: offset-gap and key-index tests internal/protocol/protocol_test.go:513, internal/protocol/protocol_test.go:553 | Adapter tests (Phase C) |
| p3-8 | en 185: abort midway cleans up both sides | partial | SIGTERM -> both exit 4, no output test/e2e/e2e_test.go:189-210; killed peer -> exit 3 test/e2e/e2e_test.go:213-227 | Per-session abort; assert retention |
| beyond-1 | not in plan (vi 84, 303-305) | beyond plan | `protocol.Conn` internal/protocol/conn.go:9-15; runs take any `Conn` internal/protocol/sender.go:28, internal/protocol/receiver.go:40 | Implement relay as a new `Conn` selected by `--mode relay` |
| beyond-2 | not in plan (vi 219-232) | beyond plan | Frame oneof api/minipsi/v1/psi.proto:14-24; unexported `toPB`/`fromPB` internal/transport/convert.go:10-85 | Payload = serialized Frame; export or move the conversion |
| beyond-3 | not in plan (vi 240-242) | beyond plan | `frameReserve` = 1024 internal/protocol/messages.go:197-202; used internal/protocol/session.go:376 | Envelope overhead must fit the reserve, or raise it |
| beyond-4 | not in plan (vi 210, 234) | beyond plan | SID from both nonces internal/hashing/hashing.go:71-80; in reports internal/report/report.go:74-76 | Decide how the ORC session_id relates to the SID |
| beyond-5 | not in plan (vi 210) | beyond plan | `Admit`/`Rejection` internal/transport/transport.go:45-84; `mode` field internal/config/config.go:252-260 | Reuse admission for ORC directives; wire `--mode relay` into every party command |
| beyond-6 | not in plan (vi 214-216) | beyond plan | Exit codes internal/cli/root.go:18-26; JSON report internal/report/report.go:1-39 | Use the report as the status and telemetry source |

<a id="spec-orc"></a>
## Area: spec-orc

The only working mode is a direct gRPC/mTLS 1.3 link, which both the spec and the plan forbid. None of these exist: ORC client or relay stream, envelope header, envelope crypto (AEAD, X25519, Ed25519, HKDF), `committee.*` rejection codes, control plane, cryptoshredding, the placeholder ORC API or an envelope-level fake relay. Most of these were deferred by the plan's PoC scope. The integration seam exists, and several platform requirements are already met (TLS 1.3 mutual auth, SID in reports, item-free logs, padding, timeouts).

| Item | Plan says | Status | Evidence (repo file:line) | Adjustment |
|---|---|---|---|---|
| net-1 | en 13; spec 26-28, 42: never connect directly; no CC to CC path | conflict | `validMode` accepts only direct internal/config/config.go:252-260; parties listen or dial each other internal/cli/party.go:427-448; `serve` keeps a listener internal/cli/serve.go:143-165; RUNBOOK.md:9-11 | Team decision: keep direct mode only for labs (not spec-compliant) or remove it; relay is the main work |
| net-2 | en 88: one stream per session; no network link to the other side | partial | One gRPC bidi stream internal/transport/transport.go:394-454; local goroutines internal/protocol/session.go:526-567; the stream goes straight to the peer internal/transport/transport.go:488-563 | Single stream done; the no-direct-link part moves to relay work |
| arch-1 | en 3-6, 12, 16, 138: a CC app drives the engine over HTTP | missing | No CC app, no HTTP; the CLI process moves frames over gRPC itself internal/transport/transport.go:143-239, internal/cli/party.go:283-301 | Decide who owns the ORC connection: the engine (relay `Conn`) or a CC app |
| seam-1 | en 27, 168-170: protocol has no networking; state machines | done differently | `Conn` with Send and Recv internal/protocol/conn.go:9-15; runs at internal/protocol/sender.go:28, internal/protocol/receiver.go:40; `PipeConn` internal/protocol/conn.go:17-84 | Mark done; build the outbox design as a relay `Conn` that adds seq and digest |
| mode-1 | not in plan (vi 52, 267-278) | beyond plan | `mode` direct or relay, relay rejected internal/config/config.go:252-260; flags internal/cli/party.go:77, internal/cli/serve.go:45; README.md:51, RUNBOOK.md:143 | Implement `mode: relay`; add ORC address, CC ids, ORC session_id |
| relay-1 | spec 39-44: one long-lived gRPC stream per CC to the ORC; ORC-opaque | missing | Proto defines only the peer `PSI.Session` api/minipsi/v1/psi.proto:10-12 | Specify a CC -> ORC stream that multiplexes sessions, plus a relay `Conn` |
| hdr-1 | en 104-116; spec 50-56: 6 header fields with the spec's names | missing | Frames have no header api/minipsi/v1/psi.proto:14-24; SID derived internal/protocol/session.go:452-456; identities from cert SANs internal/transport/transport.go:282-316 | Header mapping in Phase B (one phase per frame kind) |
| hdr-2 | en 87, 118: 1 MiB chunks, own envelope_seq, index inside the payload | done differently | Position data inside the payload api/minipsi/v1/psi.proto:60-82; packing to MaxFrame minus 1 KiB internal/protocol/messages.go:197-201 | One frame = one envelope; size the reserve once the format is fixed |
| seq-1 | en 120-126, 179, 184: seq checks with `committee.*` codes | missing | No envelope_seq or `committee.*` codes; protocol aborts with ERR_PROTOCOL internal/protocol/errors.go:13-30 | Relay `Conn`; map `committee.*` to Abort and exit codes |
| aead-1 | spec 55-56, 63-67; en 18: AEAD payload under k_committee_session, Ed25519 signature; deferred | missing | No envelope crypto; confidentiality is hop-by-hop TLS 1.3 only internal/transport/tls.go:51-73 | Team decision: may a PoC relay run without AEAD? It would not be ORC-opaque |
| order-1 | spec 69-80: signature -> sequence -> AEAD -> deliver, 5 rejection codes | missing | Only the end-of-session transcript check internal/protocol/session.go:333-339 | Per-envelope verify pipeline in the relay `Conn` before Recv returns |
| key-1 | spec 66; en 101, 181, 185: key per session process, cryptoshred; abort wipes data | partial | No committee key; daemon drops session state when `serveSession` returns internal/cli/serve.go:168-222; no explicit wiping | Abort cleanup done; wiping and cryptoshredding remain; decide per-session process |
| ctl-1 | spec 31-37: ORC directives (contract, peer, role, abort) | missing | No control plane; nearest are dataset and identity metadata internal/config/serve.go:56-60 and role check internal/protocol/session.go:402-403 | Control-plane client, or leave it to a CC app |
| ctl-2 | spec 36: CC sends status, telemetry, audit, result metadata | partial | Data exists locally: report internal/report/report.go:13-39; logs internal/cli/party.go:462-470 | Forward the report over the control plane |
| ctl-3 | spec 36; en 18: result body to the receiving EN; deferred | missing | Local `--output` file internal/cli/party.go:311 | Keep deferred |
| ids-1 | en 95, 132: session_id, role, my_cc_id, peer_cc_id | partial | Role by subcommand (serve is sender internal/config/serve.go:131); peer by `--peer-name` internal/cli/party.go:103; no CC id, SID derived | Add CC ids and ORC session_id to relay config |
| orc-1 | en 140-147: placeholder ORC API over HTTP | missing | No ORC code, no relay service in the proto | Prefer a small gRPC relay placeholder (spec line 43 asks for a stream) |
| orc-2 | en 149: in-memory fake relay in tests | partial | `protocol.Pipe` internal/protocol/conn.go:17-84; scripted fake peer internal/protocol/protocol_test.go:405 | Fake relay on the Pipe pattern that can drop, reorder, replay and alter |
| orc-3 | spec 67: ORC must not import the AEAD package (lint-inv02) | missing | No ORC code; `make check` is lint, race, vuln Makefile:51 | Add an import check only if a placeholder ORC lives in this repo |
| audit-1 | spec 82-84, 91: ORC audit-first, three-way envelope count | beyond plan | Frame and byte counters internal/protocol/session.go:237-238; report internal/report/report.go:35-36 | Define one frame = one envelope and whether Done counts |
| stall-1 | spec 80: a stalled relay only aborts the session | beyond plan | Bounded receives internal/protocol/session.go:306-316; timeout -> Abort internal/protocol/session.go:243-267 | Already aligned; tune timeouts for relay |
| align-1 | spec 9: ORC and CC channels run over mTLS | beyond plan | TLS 1.3 minimum internal/transport/transport.go:86-109; client certs required internal/transport/tls.go:51-73 | Reuse for the CC -> ORC link |
| align-2 | spec 35, 65: peer identity, pinned Ed25519 identities | done differently | Optional SAN pinning internal/transport/tls.go:75-95; `allowed_peers` internal/config/serve.go:113-124 | Make pinning mandatory in relay; add Ed25519 pinning for envelope signatures |
| align-3 | spec 54, 84: consistent session records | beyond plan | Shared 16-byte SID internal/hashing/hashing.go:71-80; logged internal/cli/party.go:298; exit codes internal/cli/root.go:19-26 | Add the ORC session_id to reports and logs |
| align-4 | en 18; spec 82-84: metadata exposure accepted | done | Sizes sent in SizesChunk internal/protocol/session.go:360-387; padding opt-in RUNBOOK.md:77-82; no items in reports internal/report/report.go:1-2 | Mark done; padding also limits what envelope sizes reveal |
| scope-1 | en 18: PoC defers envelope crypto, real ORC, EN exchange, malicious security, size hiding | done | None of these exist; relay rejected internal/config/config.go:256-257 | Keep the list; add "direct mode exists and is not spec-compliant" |
| spec-gap-1 | spec 96: the "Committee Relay Envelope" section | missing | The spec ends at the heading; the repository has only the protobuf Frame api/minipsi/v1/psi.proto:14-24 | Get the envelope wire format from the platform owners before building the relay `Conn` |

<a id="packaging-docs"></a>
## Area: packaging-docs

Phase 4 is partly met. The runtime image is done and more hardened than planned. Missing: healthcheck, `docker-compose.yml`, sample data, OpenAPI and a CC-app integration guide. The "clean machine with only Docker" criterion is not met. Several doc defects would break a demo. The plan's layout is replaced by a pure-Go layout, and the project is named `minipsi`.

| Item | Plan says | Status | Evidence (repo file:line) | Adjustment |
|---|---|---|---|---|
| pkg-1 | en 190: small runtime image, one binary | done | Multi-stage build Dockerfile:6-15; distroless nonroot pinned by digest Dockerfile:17-21; 16.7 MB, uid 65532 | Mark done; document port 7443 (RUNBOOK.md:42), optionally EXPOSE it |
| pkg-2 | en 190: configure via env vars (port, sessions, timeout, max set size) | partial | Flags plus strict YAML internal/config/config.go:128-146; only env vars: insecure ack internal/transport/transport.go:94, MINIPSI_DEV_PKI internal/devcerts/devpki.go:32-35, GOMEMLIMIT internal/cli/party.go:475; serve limits and timeouts only in YAML internal/config/serve.go:25-26 | Flags plus YAML done; env overrides or a mounted config; serve flags for limits |
| pkg-3 | en 190, 102: healthcheck (`/healthz`) | missing | No health service; the gRPC server registers only PSI internal/transport/transport.go:377-386 | grpc.health.v1 or a `minipsi healthcheck` exec subcommand (distroless has no shell) |
| pkg-4 | en 191: docker-compose.yml for the demo | missing | No compose, demo or testdata; the quickstart makes data with printf README.md:31-32 | Compose with certs-init, one `serve`, one `request`, sample data |
| pkg-5 | en 192: OpenAPI docs | missing | No HTTP API; only the peer wire schema api/minipsi/v1/psi.proto:10-12, linted by buf (buf.yaml:4-14) | Depends on the API transport decision (OpenAPI if HTTP, proto docs if gRPC) |
| pkg-6 | en 192: README for CC-app integration | partial | README.md:1-71 and RUNBOOK.md:1-171 are operator docs for direct mode; relay "reserved and rejected" RUNBOOK.md:143 | Write the integration guide after Phase B |
| pkg-7 | en 193: demo runs on a clean machine with only Docker | partial | Image builds (.github/workflows/ci.yml:92-99); no compose or sample data; RUNBOOK.md:86-90 container example needs `--user` with `certs dev` keys | Define as "`docker compose up` prints the expected intersection" and add a CI job |
| pkg-8 | en 158-159: Dockerfile builds C++ and Go; tests in Docker | done differently | Pure-Go build Dockerfile:6-15; tests in CI .github/workflows/ci.yml:35-44 | Dockerfile done; drop "tests in Docker" |
| layout-1 | en 23, 156: project named `handover` | done differently | Binary and image are `minipsi` (Makefile:27, Dockerfile:15, internal/cli/root.go:74); module `github.com/XSpring/minipsi` | Recommended: adopt `minipsi` in the plan |
| layout-2 | en 24: cmd/handover opens the HTTP port | done differently | internal/cli/root.go:88 `Run`, called from cmd/minipsi/main.go:11-13; gRPC listener internal/transport/transport.go:377-386 | Rewrite as cmd/minipsi, internal/cli, internal/config |
| layout-3 | en 25: api/ holds the HTTP endpoints | done differently | api/minipsi/v1/psi.proto is the peer wire schema (api/minipsi/v1/psi.proto:10-12) | api/ = wire schema; the CC API is open work |
| layout-4 | en 26: session/ for lifecycle, inbox/outbox, seq, chunks, timeouts | done differently | No session package; daemon sessions internal/cli/serve.go:104-223; protocol session internal/protocol/session.go; timeouts internal/config/config.go:98-104 | Rewrite to the real packages; inbox/outbox/seq is relay work |
| layout-5 | en 27: protocol/ with no networking | done | `Conn` only internal/protocol/conn.go:12-15; in-memory Pipe internal/protocol/conn.go:17-32 | Mark done (path internal/protocol) |
| layout-6 | en 28: core/ calls C++ through cgo | obsolete | No core/ and no cgo; CGO_ENABLED=0 Makefile:27, Dockerfile:13 | Drop; record the pure-Go decision |
| layout-7 | en 29: cpp/handover_core.cpp | obsolete | No C/C++ files | Drop |
| layout-8 | en 30: third_party/MiniPSI submodule | obsolete | No .gitmodules, no third_party/, no MiniPSI sample data | Drop; vendor the sample CSVs as testdata if wanted |
| layout-9 | en 31: docker/ with a multi-stage Dockerfile | done differently | Root Dockerfile:1-21 | Mark done; put compose files in a deploy/ or demo/ directory |
| bp-ci | not in plan | beyond plan | CI jobs lint, race, govulncheck, proto-check, e2e, fuzz, docker .github/workflows/ci.yml:1-99 | Mark done; add a compose-demo job later |
| bp-make | not in plan | beyond plan | Makefile:1-85 with pinned tools; staticcheck under go1.26.0 Makefile:18; README says Go 1.26+ README.md:11 | Raise the documented minimum Go patch so `make vuln` passes (govulncheck fails on go1.26.0) |
| bp-e2e | not in plan for direct mode (en 183) | beyond plan | scripts/e2e.sh:21-106 six scenarios; Go suite behind build tag test/e2e/e2e_test.go:1 | Mark done; fix two timing-racy e2e tests; reuse scenarios for the demo |
| bp-devpki | not in plan | beyond plan | `certs dev` internal/cli/tools.go:70-73; loopback-only `--dev` internal/config/config.go:268-274 | Mark done; compose needs `certs dev --host <service names>` |
| doc-container-uid | en 193 | beyond plan | Container example RUNBOOK.md:86-90; image uid 65532 Dockerfile:19 | Add `--user` or chown guidance |
| doc-systemd | vi 333-338 | beyond plan | RUNBOOK.md:100 `ExecStart` has no subcommand and exits 1 | Commit the fix (`minipsi sender --config ...`) |
| doc-quickstart-race | en 193 | beyond plan | One-shot quickstart has no `--connect-retry` README.md:41-42; default 0 internal/config/config.go:120 | Add `--connect-retry 10s`; maybe a non-zero default |
| doc-server-name | en 193 | beyond plan | TLS server name defaults to the dial host internal/cli/party.go:357-366; `--server-name` flag internal/cli/party.go:104 is undocumented in README and RUNBOOK | Document `--server-name` and use it in the demo |
| bp-opsdocs | en 192 (only a CC-app README) | beyond plan | Operator runbook RUNBOOK.md:1-171; exit-code contract internal/cli/root.go:19-26 | Mark done; the CC-app guide stays separate |

## Adjusted plan

The old plan had four phases: C++/cgo core, protocol in Go, HTTP engine, image and demo. The first two are behind us: the pure-Go core and the protocol exist and are tested. The work now is (A) decide and review what was built differently, (B) design the API interfaces on the protocol seam, (C) build the relay transport and envelope security the spec requires, and (D) package and document it.

```text
Phase A  decisions + crypto review         (no code; unblocks B and C)
Phase B  API interface design on the seam  (signatures, adapters, mapping tables)
Phase C  relay transport + envelope security per the spec
Phase D  packaging, demo, docs
```

### Phase A: decisions and crypto review

**Goal.** Record the decisions the old plan assumed and the repository changed, so Phase B designs against a fixed baseline.

**Work items.**

1. Adopt the pure-Go core (CGO_ENABLED=0) as the baseline and drop the "no C++ line changed" rule (core-1, ctrl-1, core-22).
2. Crypto review of the repository's construction against Rosulek-Trieu semi-honest PSI: no Pi, uniform Elligator2 preimages with a torsion component in GF(2^255-19), one polynomial per instance, l_out-truncated hashes bound to SID and instance, lambda = 40 default, receiver padding, per-instance keys, the experimental AES-MMO hash. Find and commit the spec that internal/field/field.go:29 refers to (core-4, flow-4, bp-3).
3. Accept the 7 frame kinds as wire protocol version 1 and as the relay payload units (flow-1, env-2).
4. Choose who owns the ORC connection: the engine (a relay `Conn` over one long-lived gRPC stream per CC, as the spec describes) or a CC app driving an inbox/outbox API (as the old plan describes). This also decides HTTP or gRPC for the CC-facing API and whether OpenAPI applies (api-1, arch-1, pkg-5).
5. Decide the fate of direct mode: lab-only and documented as not spec-compliant, or removed (net-1).
6. Get the committee relay envelope wire format from the platform owners, since the spec section is empty (spec-gap-1).
7. Fix product parameters: the name (`minipsi`), input normalisation (raw bytes or opt-in trim), default limits (2^20 or 2^22/2^24), result format, batched mode in scope or not, default padding (layout-1, io-13, io-18, io-11, bp-2, beyond-4).

**Done when.** Each decision above is written down with an owner and a date. The crypto review has a written result (accepted, accepted with changes, or revert). The envelope format source is known.

**Replaces.** Old Phase 1 entirely (handover-plan.en.md lines 153-163) and the "Decisions made" table (handover-plan.en.md lines 8-18).

### Phase B: API interface design on the protocol seam

**Goal.** Interface signatures for the new API, built on `RunReceiver`, `RunSender`, `Conn` and `Frame` without changing the protocol. See [ENTRY_POINTS.md](ENTRY_POINTS.md#seam) for the seam contract and [CALL_GRAPHS.md](CALL_GRAPHS.md#cg-06) for how a session uses `Conn`.

**Work items.**

1. **Session / engine API.** Operations that map onto the old plan's endpoints: create a session (ORC `session_id`, role, own and peer CC ids, hash, lambda, limits, padding), provide input (reuse `itemio.Read` options: format, column, header, batched), query status (states mapped from existing signals: running, done = exit 0, failed = exit 1-5), fetch the result (receiver only; indices from `Result` -> original values), abort and delete (context cancel plus erasure). Decide where the API lives: `internal/protocol` can only be imported inside the module.
2. **`Conn` adapter.** One type that implements `protocol.Conn` (internal/protocol/conn.go:12-15): Send -> outbox or relay stream, Recv <- inbox in `envelope_seq` order. It must meet the contract in [ENTRY_POINTS.md](ENTRY_POINTS.md#seam): ordered and lossless delivery (OutputParts carry no offset), Send and Recv concurrent with each other but not with themselves, prompt return on context cancel, io.EOF after peer close, a final best-effort Abort after the writer exits. Add an idle timeout for undelivered envelopes.
3. **Envelope header mapping.** One table, proposed:

   | Header field | Source |
   |---|---|
   | `session_id` | ORC directive; Phase A decides whether it is also bound into Hello or the SID |
   | `sender_cc_id`, `receiver_cc_id` | Relay configuration (own and peer CC id; relation to certificate SANs to be decided) |
   | `envelope_seq` | Per-direction counter in the adapter, starting at 1 |
   | `protocol_phase` | Frame kind: `hello`, `sizes`, `keys` (was gk), `poly`, `output` (was masks), `done`, `abort` |
   | `payload_digest` | SHA-256 of the payload as sent (serialized `Frame`, or ciphertext once AEAD exists) |

4. **Payload encoding.** Payload = serialized `minipsi.v1.Frame`. Export or move `toPB`/`fromPB` (internal/transport/convert.go:10-85) so the adapter can share them. Decide binary protobuf or base64 JSON.
5. **Size budget.** `MaxFrame` against the relay's envelope cap (1 MiB in the old plan, 4 MiB default today), the 1024-byte `frameReserve` (internal/protocol/messages.go:197-202), signature and AEAD overhead, and base64 growth if JSON.
6. **Error mapping table.** `ERR_*` Abort codes (internal/protocol/errors.go:10-30) and exit codes (internal/cli/root.go:19-26) -> `handover.*` names; `committee.*` codes emitted only by the adapter.
7. **Multi-session safety.** Panic recovery in `parallel.For` (internal/parallel/parallel.go:61-71), sender-side dedup for non-CLI callers, the concurrency model (`max_sessions` or one at a time).

**Done when.** The interface signatures, header mapping and error mapping are reviewed and agreed. A test runs `RunReceiver` and `RunSender` over two adapter `Conn`s joined by an in-memory mover (the `PipeConn` and fake-peer pattern, internal/protocol/protocol_test.go:405-498) and gets the same intersection as direct mode.

**Replaces.** Old Phase 2's state-machine item (handover-plan.en.md line 168), old Phase 3's API and inbox/outbox item (handover-plan.en.md line 178), the `session/` package in the layout (handover-plan.en.md line 26), and the envelope JSON example (handover-plan.en.md lines 104-118).

### Phase C: relay transport and envelope security

**Goal.** Make `--mode relay` work per the spec: no direct CC to CC path, the ORC routes envelopes it cannot read or forge.

**Work items.**

1. Relay client: one long-lived gRPC stream per CC to the ORC (spec-orchestrator-compute-clusters.md line 43), multiplexing sessions, reusing the mTLS 1.3 setup (internal/transport/tls.go:51-73). Replace the rejection in `validMode` (internal/config/config.go:252-259) and wire `--mode relay` into every party command. Config: ORC address, CC ids, session_id.
2. Per-envelope checks in the spec's order: signature -> sequence -> AEAD verify with the header as associated data -> deliver. Rejection codes: `committee.envelope_signature_invalid`, `committee.envelope_sequence_gap`, `committee.envelope_out_of_order`, `committee.envelope_aead_invalid`, `committee.envelope_aead_ad_mismatch`. Done and Abort frames are outside the transcript, so the envelope layer must authenticate them.
3. Committee key: each CC signs an ephemeral X25519 key with its pinned Ed25519 identity; both derive `k_committee_session` with HKDF-SHA256; the key is cryptoshredded at tear-down. Decide whether relay sessions in `serve` run in a per-session process. Extend erasure to abort and timeout paths.
4. Test relay: an in-memory fake ORC on the Pipe pattern that can drop, reorder, replay, alter and inject envelopes. Optionally a small gRPC placeholder ORC (one stream per CC, routed by `receiver_cc_id`) with an import check that it never imports the envelope-crypto package.
5. Audit counts: one frame = one envelope; define whether Done and Abort count, so reports feed the three-way envelope count.
6. Timeouts for relay latency (HandshakeTimeout 30 s, PhaseTimeout 10 min, internal/protocol/session.go:82-87), given that two CC apps may start minutes apart.
7. Control plane mapping (directives -> role, peer, dataset; report -> status and telemetry), or a documented hand-off to the CC app.

**Done when.** Two engines in relay mode through the fake ORC give the right intersection. Each tampering case (drop, reorder, replay, altered header, injected envelope, bad signature) returns the right `committee.*` code. Aborting midway cleans up both sides, with retention assertions. A test shows the ORC side sees only header fields and ciphertext.

**Replaces.** Old Phase 3's seq and digest checks and done-when criteria (handover-plan.en.md lines 179-185), the placeholder ORC API (handover-plan.en.md lines 140-149), and the PoC deferral of envelope crypto (handover-plan.en.md line 18), if Phase A brings it into scope.

### Phase D: packaging, demo, docs

**Goal.** A demo that runs on a clean machine with only Docker, and docs that match the code.

**Work items.**

1. Healthcheck for `serve`: grpc.health.v1, or a `minipsi healthcheck` exec subcommand for a distroless HEALTHCHECK. Optionally EXPOSE 7443.
2. `docker-compose.yml` in a deploy/ or demo/ directory: a certs-init job (`certs dev --host <service names>`), one `serve` (sender with a sample dataset), one `request` (receiver), sample data. Add the fake ORC once Phase C lands. A CI job runs it.
3. Doc fixes: `--user` in the container example (RUNBOOK.md:86-90), `--connect-retry` in the quickstart (README.md:41-42), document `--server-name`, the systemd `ExecStart` subcommand (RUNBOOK.md:100), the minimum Go patch version for govulncheck.
4. Configuration for containers: `MINIPSI_*` environment overrides or a documented mounted-YAML pattern; serve flags for limits and timeouts.
5. CC-app integration guide and API reference (OpenAPI if HTTP, proto reference if gRPC), written from the Phase B interfaces.
6. Test hygiene: fix the two timing-racy e2e tests; optionally add the MiniPSI sample CSVs as a golden test.

**Done when.** `docker compose up` on a machine with only Docker prints the expected intersection, a CI job proves it, and README, RUNBOOK and the integration guide match the code.

**Replaces.** Old Phase 4 (handover-plan.en.md lines 187-193). The image itself is already done.

### Already done, no further work

The static distroless image, the pure-Go core and its tests, deduplication, the mask shuffle, unequal, 1-item and empty sets, chunking, timeouts, the one-stream-per-session model, TLS 1.3 mutual authentication, JSON reports and exit codes, CI and the Makefile, and the dev PKI.

## Open decisions for the team

The open questions from the 6 areas, merged and deduplicated. The area items each one comes from are in brackets.

1. **Pure-Go baseline and crypto review.** Accept the repository's variant as the new baseline: no Pi, torsion-randomized uniform-preimage encoding in GF(2^255-19), one polynomial, l_out-truncated session-bound hashes, padding. Or require a return to the paper's construction. Is an external review or a cross-check against MiniPSI test vectors required before production? [core-4, core-22, ctrl-1, flow-4]
2. **Missing spec.** internal/field/field.go:29 refers to "the lp() of the spec", which is not in the repository. Where is it, and should it be committed as the reference? [core-4, io-6]
3. **lambda and output length.** Is l_out truncation with lambda = 40 (about 8-11 B outputs) acceptable, or should lambda be fixed higher or outputs be 32 bytes as in MiniPSI? [core-19, beyond-5]
4. **Scope of extras.** Do batched multi-instance mode and the experimental AES-MMO hash stay in scope given the "exactly -i, one polynomial, no bins" decision? Should the engine API expose batched instances, and accept the CSV options (column, header, instance column) on input? [bp-2, bp-3, beyond-3, io-2]
5. **Peer size 1.** Should the sender reject a receiver that announces m = 1 (a constant polynomial, so every sender item matches)? This matters only beyond the semi-honest model. [p1-8]
6. **Frame kinds as relay units.** Adopt the 7 frame kinds with one `protocol_phase` per kind, or insist on 3 messages (a wire protocol change)? [flow-1, flow-2, env-2, hdr-1]
7. **Who owns the ORC connection.** The engine (relay `Conn` over one gRPC stream per CC) or a CC app driving an HTTP inbox/outbox? This also settles HTTP vs gRPC for the CC-facing API and whether OpenAPI is written. [api-1, arch-1, walk-2, walk-3, orc-1, pkg-5]
8. **Envelope wire format.** The spec section is empty. Who defines it (protobuf or JSON, signature coverage, associated-data encoding), and is the payload binary `Frame` or base64 JSON? [spec-gap-1, env-1, beyond-2]
9. **Envelope size.** Relay at 1 MiB (about 52 envelopes at 2^20) or 4 MiB (about 19)? Do header, Ed25519 signature, AEAD tag and base64 growth fit, and is the 1 KiB `frameReserve` meant for this envelope? [ctrl-6, env-3, hdr-2, io-20, beyond-3]
10. **Who runs the envelope checks.** The engine's adapter or the CC app (seq, digest, later signature and AEAD)? Done and Abort are outside the transcript, so the envelope layer must authenticate them. [env-4, env-5, env-6, order-1]
11. **PoC without envelope crypto.** May a PoC relay run without AEAD and signatures, although the ORC could then read sizes, coefficients and outputs (not ORC-opaque)? [aead-1, scope-1]
12. **Session identity.** Bind the ORC `session_id` into Hello or the SID (needs crypto review), or carry it only in the header, logs and reports? How do CC ids relate to certificate SANs? [beyond-1, beyond-4, ids-1, align-3]
13. **Error namespace.** How do ERR_* codes and exit codes map to `handover.*`, which layer emits `committee.*`, and which exit codes do they map to? Should Abort map to the platform's abort and tear-down signals? [err-1, seq-1, beyond-2]
14. **Direct mode.** Keep direct mode (one-shot and the `serve` listener) as a lab or bilateral mode documented as not spec-compliant, or remove it? [net-1, scope-1]
15. **Roles and directives.** `serve` is always the sender and `request` always the receiver. Must an ORC directive assign either role to either CC (a receiver-side daemon)? How do directives relate to datasets, `allowed_peers` and requester-started sessions? [api-2, ctl-1]
16. **Concurrency.** One session at a time per daemon with busy-retry, or a configurable `max_sessions`? If many sessions share a process, `parallel.For` must recover panics. [p3-4, pkg-2, ctrl-8]
17. **Data retention and erasure.** May reports and result files stay on disk? Should scalar erasure also run on abort and timeout paths? Must relay sessions run in a per-session process so the committee key can be cryptoshredded? [api-8, p3-5, key-1]
18. **Health and status.** HTTP `/healthz`, the gRPC health service, or an exec healthcheck subcommand? Is a queryable per-session status needed beyond logs and reports? [api-6, api-9, pkg-3]
19. **Timeouts under relay.** What should HandshakeTimeout (30 s) and PhaseTimeout (10 min per frame) be when the two CC apps may start minutes apart? [stall-1, p3-3]
20. **Size hiding defaults.** Default receiver padding reveals k = m-1, sender padding is off, sizes are sent before the peer's Hello is validated, and the ORC sees envelope counts and sizes. What should platform runs use? [beyond-4, align-4]
21. **Envelope count rule.** Does the three-way envelope count include Done and Abort, which the frame counters exclude today? [audit-1]
22. **Input normalisation.** Keep raw-byte comparison, or add the plan's whitespace trim as an option both CCs must set the same way? [io-13]
23. **Limits and container config.** Default limits 2^20 (plan) or 2^22/2^24 (repo)? Environment variable overrides, or mounted YAML as the container interface? [io-18, io-22, pkg-2]
24. **Result format.** Add a CSV-quoted standalone output (items with line breaks fail today after the run, with a misleading error), or document lines-only? [io-11]
25. **Empty batched instances.** Should a CSV row with an instance id and an empty item register an empty instance, as RUNBOOK.md:75 implies, or should the runbook change? [io-3]
26. **Sender dedup.** Should `RunSender` deduplicate its own input? Today only itemio does, and duplicate sender items produce identical outputs the receiver can see. [ctrl-4]
27. **MiniPSI golden data.** Obtain MiniPSI's data/*.csv and add a golden test, or replace the 100-item criterion with the generated-data tests? [p2-4, io-23]
28. **Name.** Rename the plan to `minipsi` (recommended) or the code to `handover`? [layout-1]
29. **Demo content.** Should the Phase D demo show direct mode (serve/request over mTLS between two containers) or relay mode through a fake ORC? [pkg-7]
30. **Connect retry default.** Give `receiver` and `sender` a non-zero default `--connect-retry` so the quickstart stops racing? [doc-quickstart-race]
