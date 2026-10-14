#!/usr/bin/env bash
# End-to-end smoke test: two minipsi processes on localhost over mutual TLS.
# Usage: scripts/e2e.sh [path/to/minipsi]   (default: bin/minipsi)
# Exits non-zero on any failure. Uses a temporary directory that is removed afterwards.
set -euo pipefail

BIN=$(realpath "${1:-bin/minipsi}")
WORK=$(mktemp -d)
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$WORK"' EXIT
cd "$WORK"

PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])' 2>/dev/null || echo 17443)
ADDR="127.0.0.1:$PORT"

"$BIN" certs dev --out certs >/dev/null
TLS_R=(--cert certs/receiver.crt --key certs/receiver.key --ca certs/ca.crt --peer-name sender)
TLS_S=(--cert certs/sender.crt --key certs/sender.key --ca certs/ca.crt --peer-name receiver)

fail() { echo "FAIL: $*" >&2; exit 1; }

# 1. Standalone, sender listens, receiver dials; SHA-256.
seq 1 3000 | sed 's/^/user-/' > y.txt
seq 2001 6000 | sed 's/^/user-/' > x.txt
"$BIN" sender --listen "$ADDR" --input x.txt "${TLS_S[@]}" --report s1.json 2> s1.log &
SPID=$!
"$BIN" receiver --connect "$ADDR" --connect-retry 20s --server-name sender --input y.txt \
  --output out1.txt --report r1.json "${TLS_R[@]}" 2> r1.log || fail "receiver 1 (see r1.log)"
wait $SPID || fail "sender 1 (see s1.log)"
[ "$(sort out1.txt | md5sum)" = "$(seq 2001 3000 | sed 's/^/user-/' | sort | md5sum)" ] || fail "intersection 1"
echo "ok   standalone sha256 (receiver dials): $(wc -l < out1.txt) items"

# 2. Batched CSV, receiver listens, sender dials; AES hash, block padding on both sides.
{ echo "inst,item"; for b in 1 2 3; do seq 1 200 | sed "s/^/$b,u$b-/"; done; } > y.csv
{ echo "inst,item"; for b in 1 2 3; do seq 150 400 | sed "s/^/$b,u$b-/"; done; } > x.csv
"$BIN" receiver --listen "$ADDR" --input y.csv --format csv --header --batched --instance-column inst \
  --column item --output out2.csv --hash aes --pad-multiple 16 "${TLS_R[@]}" 2> r2.log &
RPID=$!
"$BIN" sender --connect "$ADDR" --connect-retry 20s --server-name receiver --input x.csv --format csv \
  --header --batched --instance-column inst --column item --hash aes --pad-multiple 32 "${TLS_S[@]}" 2> s2.log || fail "sender 2"
wait $RPID || fail "receiver 2 (see r2.log)"
[ "$(wc -l < out2.csv)" -eq 153 ] || fail "batched intersection size $(wc -l < out2.csv) != 153"
echo "ok   batched csv aes (receiver listens): $(wc -l < out2.csv) rows"

# 3. Parameter mismatch → both exit 4.
set +e
"$BIN" sender --listen "$ADDR" --input x.txt --lambda 64 "${TLS_S[@]}" 2> s3.log &
SPID=$!
"$BIN" receiver --connect "$ADDR" --connect-retry 20s --server-name sender --input y.txt --output out3.txt \
  "${TLS_R[@]}" 2> r3.log
RC=$?
wait $SPID; SC=$?
set -e
[ "$RC" -eq 4 ] && [ "$SC" -eq 4 ] || fail "mismatch exit codes $RC/$SC (want 4/4)"
[ ! -e out3.txt ] || fail "output written on failure"
echo "ok   parameter mismatch: both exit 4, no output"

# 4. Wrong CA → receiver exits 3.
"$BIN" certs dev --out other >/dev/null
set +e
"$BIN" sender --listen "$ADDR" --input x.txt "${TLS_S[@]}" --timeout 10s 2> s4.log &
SPID=$!
"$BIN" receiver --connect "$ADDR" --connect-retry 3s --server-name sender --input y.txt --output out4.txt \
  --cert other/receiver.crt --key other/receiver.key --ca other/ca.crt 2> r4.log
RC=$?
kill $SPID 2>/dev/null; wait $SPID 2>/dev/null
set -e
[ "$RC" -eq 3 ] || fail "untrusted CA exit code $RC (want 3)"
echo "ok   untrusted certificate rejected (exit 3)"

# 5. Daemons: both machines serve, each requests from the other; real certs.
PORT2=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])' 2>/dev/null || echo 17444)
ADDR2="127.0.0.1:$PORT2"
"$BIN" serve --listen "$ADDR" --dataset customers=x.txt --state-dir sstate \
  --cert certs/sender.crt --key certs/sender.key --ca certs/ca.crt 2> serve_s.log &
SSPID=$!
"$BIN" serve --listen "$ADDR2" --dataset users=y.txt \
  --cert certs/receiver.crt --key certs/receiver.key --ca certs/ca.crt 2> serve_r.log &
SRPID=$!
"$BIN" request --peer "$ADDR" --server-name sender --dataset customers --input y.txt --output out5.txt \
  "${TLS_R[@]}" 2> q5.log || fail "request to sender daemon (see q5.log)"
"$BIN" request --peer "$ADDR2" --server-name receiver --dataset users --input x.txt --output out6.txt \
  "${TLS_S[@]}" 2> q6.log || fail "request to receiver daemon (see q6.log)"
[ "$(sort out5.txt | md5sum)" = "$(sort out1.txt | md5sum)" ] || fail "daemon intersection 5"
[ "$(sort out6.txt | md5sum)" = "$(sort out1.txt | md5sum)" ] || fail "daemon intersection 6"
set +e
"$BIN" request --peer "$ADDR" --server-name sender --dataset nope --input y.txt --output out7.txt "${TLS_R[@]}" 2> q7.log
RC=$?
set -e
[ "$RC" -eq 4 ] || fail "unknown dataset exit $RC (want 4)"
"$BIN" request --peer "$ADDR" --server-name sender --dataset customers --input y.txt --output out8.txt \
  "${TLS_R[@]}" 2> q8.log || fail "daemon did not survive a rejected request"
kill -TERM $SSPID $SRPID
wait $SSPID || fail "sender daemon exit"
wait $SRPID || fail "receiver daemon exit"
[ "$(ls sstate/reports | wc -l)" -eq 2 ] || fail "daemon reports"
echo "ok   daemons both ways (serve/request), unknown dataset exit 4, clean shutdown"

# 6. --dev: shared dev PKI, no TLS flags.
export MINIPSI_DEV_PKI="$WORK/devpki"
"$BIN" serve --dev --name b --listen "$ADDR" --dataset customers=x.txt 2> dev_s.log &
DPID=$!
"$BIN" request --dev --name a --peer "$ADDR" --dataset customers --input y.txt --output out9.txt 2> dev_r.log \
  || fail "dev request (see dev_r.log)"
kill -TERM $DPID; wait $DPID || fail "dev daemon exit"
[ "$(sort out9.txt | md5sum)" = "$(sort out1.txt | md5sum)" ] || fail "dev intersection"
echo "ok   --dev serve/request"

echo "e2e: all passed"
