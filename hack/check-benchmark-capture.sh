#!/usr/bin/env bash
# Execute the benchmark capture helpers against a stub kubectl and assert the
# properties that cost real data when they were missing.
#
# `bash -n` sees none of this. A capture that replaces the run already on disk,
# that finalises a file it does not own, or that watches a cluster nobody named,
# all parse perfectly.
#
# Every case below corresponds to a defect that was measured on this code, not to
# a hypothetical. The count is asserted at the end: a block that stops running
# would otherwise read as green.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SR="$ROOT/hack/benchmark/sample_replicas.sh"
TL="$ROOT/hack/benchmark/tail_wva_logs.sh"

EXPECTED_CHECKS=25

WORK="$(mktemp -d)"
STARTED_PIDS=""

# Kill what we started, on any exit path. An EXIT-only trap leaves strays when a
# CI job is cancelled, and killing the wrapper alone reparents its `sleep`, which
# is how earlier runs left two hour-long sleeps per invocation on a developer box.
cleanup() {
    for p in $STARTED_PIDS; do
        pkill -P "$p" 2>/dev/null || true
        kill "$p" 2>/dev/null || true
    done
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

fails=0
checks=0
ok() { checks=$((checks + 1)); echo "  ok: $1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); echo "  FAIL: $1" >&2; }

note_pid() { STARTED_PIDS="$STARTED_PIDS $1"; }
pid_of() { cat "$1.pid" 2>/dev/null || true; }

# A kubectl that records how it was called and returns an empty item list, so
# the samplers' python filter has valid input.
cat > "$WORK/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "$@" >> "${STUB_LOG:?}"
echo '{"items":[]}'
STUB
chmod +x "$WORK/kubectl"
export KUBECTL_CMD="$WORK/kubectl"
export STUB_LOG="$WORK/calls.log"
: > "$STUB_LOG"
# Short, so nothing long-lived is left behind even if a kill is missed.
export REPLICA_SAMPLE_INTERVAL=1

# ---- a populated output is not replaced ---------------------------------
OUT="$WORK/samples.json"
printf '{"snapshots":[{"a":1}]}' > "$OUT"
BEFORE="$(cat "$OUT")"
if bash "$SR" start demo-ns "$OUT" >"$WORK/o" 2>"$WORK/e"; then
    bad "start overwrote a populated output (exit 0)"
else
    grep -q "refusing to start" "$WORK/e" \
        && ok "start refuses a populated output" \
        || bad "start failed without a refusal: $(head -1 "$WORK/e")"
fi
[ "$(cat "$OUT")" = "$BEFORE" ] \
    && ok "the populated output is byte-for-byte untouched" \
    || bad "the populated output was modified: $(head -c 40 "$OUT")"

# ---- and a stop after a refused start must not corrupt it ---------------
# The guard promised the bytes on disk were safe; stop used to append a JSON
# terminator to whatever was at the path two calls later.
bash "$SR" stop "$OUT" demo-ns >/dev/null 2>&1 || true
[ "$(cat "$OUT")" = "$BEFORE" ] \
    && ok "stop after a refused start leaves the file alone" \
    || bad "stop corrupted a file it never owned: $(head -c 48 "$OUT")"

# ---- --force replaces it; --context reaches kubectl and the claim -------
: > "$STUB_LOG"
if bash "$SR" --context my-cluster --force start demo-ns "$OUT" >"$WORK/o" 2>&1; then
    ok "start with --force succeeds on a populated output"
    note_pid "$(pid_of "$OUT")"
else
    bad "start --force failed: $(head -2 "$WORK/o")"
fi
sleep 1
grep -q -- "--context my-cluster" "$STUB_LOG" \
    && ok "--context reaches the kubectl calls" \
    || bad "--context never reached kubectl: $(head -2 "$STUB_LOG")"

owner="$(cat "$OUT.lock/owner" 2>/dev/null || true)"
case "$owner" in
    *"namespace=demo-ns"*) ok "the claim records the namespace" ;;
    *) bad "claim has no namespace: $owner" ;;
esac
case "$owner" in
    *"context=my-cluster"*) ok "the claim records the context" ;;
    *) bad "claim has no context: $owner" ;;
esac

# ---- a LIVE capture is not stealable, whatever its output weighs --------
# Byte count used to decide this, which is wrong in both directions: a live
# capture that has written nothing read as free, and the refusal for one that had
# written something blamed the bytes rather than the owner.
if bash "$SR" --context other-cluster start demo-ns "$OUT" >"$WORK/o" 2>"$WORK/e"; then
    bad "a second start stole a live capture (exit 0)"
    note_pid "$(pid_of "$OUT")"
else
    grep -q "a capture already owns" "$WORK/e" \
        && ok "a live capture is not stealable" \
        || bad "second start failed for the wrong reason: $(head -1 "$WORK/e")"
fi
FIRST_PID="$(pid_of "$OUT")"
kill -0 "$FIRST_PID" 2>/dev/null \
    && ok "the first capture is still running and still owns the pidfile" \
    || bad "the first capture was displaced"

# ---- stopping names both fields, and a mismatch is reported -------------
warn="$(bash "$SR" --context wrong-cluster stop "$OUT" wrong-ns 2>&1 >/dev/null || true)"
case "$warn" in
    *"namespace=wrong-ns"*) ok "stop warns on a namespace mismatch" ;;
    *) bad "no namespace warning: $(echo "$warn" | head -1)" ;;
esac
case "$warn" in
    *"context=wrong-cluster"*) ok "stop warns on a context mismatch" ;;
    *) bad "no context warning: $(echo "$warn" | head -1)" ;;
esac
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$OUT" 2>/dev/null \
    && ok "the finalised samples file is valid JSON" \
    || bad "the finalised samples file does not parse"

# ---- stop is idempotent -------------------------------------------------
bash "$SR" stop "$OUT" demo-ns >/dev/null 2>&1 || true
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$OUT" 2>/dev/null \
    && ok "a second stop does not corrupt the file" \
    || bad "a second stop corrupted the file: $(tail -c 12 "$OUT")"

# ---- no namespace on stop is not a mismatch ----------------------------
O2="$WORK/quiet.json"
bash "$SR" --context c1 start quiet-ns "$O2" >/dev/null 2>&1 && note_pid "$(pid_of "$O2")"
sleep 1
quiet="$(bash "$SR" stop "$O2" 2>&1 >/dev/null || true)"
[ -z "$quiet" ] \
    && ok "stop without a namespace says nothing" \
    || bad "stop without a namespace warned anyway: $(echo "$quiet" | head -1)"

# ---- the stop arguments may be given in either order -------------------
# The two helpers took them in opposite orders; getting it backwards used to
# exit 0, print "stopped", and leave the capture running.
O3="$WORK/order.json"
bash "$SR" start order-ns "$O3" >/dev/null 2>&1 && note_pid "$(pid_of "$O3")"
sleep 1
P3="$(pid_of "$O3")"
bash "$SR" stop order-ns "$O3" >/dev/null 2>&1 || true
if [ -n "$P3" ] && kill -0 "$P3" 2>/dev/null; then
    bad "the reversed stop order left the capture running (pid $P3)"
else
    ok "stop accepts its arguments in either order"
fi

# ---- flags are honoured wherever they appear ---------------------------
: > "$STUB_LOG"
O4="$WORK/after.json"
bash "$SR" start after-ns "$O4" --context trailing-ctx >/dev/null 2>&1 && note_pid "$(pid_of "$O4")"
sleep 1
grep -q -- "--context trailing-ctx" "$STUB_LOG" \
    && ok "a flag after the subcommand is still honoured" \
    || bad "a flag after the subcommand was dropped: $(head -1 "$STUB_LOG")"
bash "$SR" stop "$O4" after-ns >/dev/null 2>&1 || true

# ---- a stray positional is an error, not a silent drop -----------------
if bash "$SR" start a-ns "$WORK/x.json" extra-junk >/dev/null 2>"$WORK/e"; then
    bad "a stray positional was accepted"
else
    grep -q "unexpected argument" "$WORK/e" \
        && ok "a stray positional is refused" \
        || bad "stray positional failed for the wrong reason: $(head -1 "$WORK/e")"
fi

# ---- an empty --context= is an error, not "inherited" ------------------
if bash "$SR" --context= start a-ns "$WORK/y.json" >/dev/null 2>"$WORK/e"; then
    bad "--context= was accepted and silently meant inherited"
else
    ok "an empty --context= is refused"
fi

# ---- a path that cannot be written fails loudly -----------------------
RO="$WORK/ro"
mkdir -p "$RO"
chmod 500 "$RO"
if bash "$SR" --force start demo-ns "$RO/z.json" >"$WORK/o" 2>"$WORK/e"; then
    bad "start reported success for a path it cannot write: $(head -1 "$WORK/o")"
else
    ok "start refuses a path it cannot write"
fi
chmod 700 "$RO"

# ---- a stale claim is reclaimed, not honoured forever -----------------
O5="$WORK/stale.json"
mkdir -p "$O5.lock"
printf 'namespace=old-ns context=old-ctx started=then pid=999999 cmd=x\n' > "$O5.lock/owner"
if bash "$SR" start fresh-ns "$O5" >"$WORK/o" 2>"$WORK/e"; then
    note_pid "$(pid_of "$O5")"
    grep -q "reclaiming a stale capture" "$WORK/e" \
        && ok "a claim whose holder is gone is reclaimed with a note" \
        || bad "a stale claim was reused without saying so"
else
    bad "a stale claim blocked a new capture: $(head -1 "$WORK/e")"
fi
bash "$SR" stop "$O5" fresh-ns >/dev/null 2>&1 || true

# ---- the log tail carries the same guards ----------------------------
LOG="$WORK/controller.log"
printf 'EARLIER RUN\n' > "$LOG"
if bash "$TL" start demo-ns "$LOG" >/dev/null 2>"$WORK/e"; then
    bad "tail start overwrote a populated log"
else
    grep -q "refusing to start" "$WORK/e" \
        && ok "tail start refuses a populated log" \
        || bad "tail start failed without a refusal"
fi
[ "$(cat "$LOG")" = "EARLIER RUN" ] \
    && ok "the populated log is untouched" \
    || bad "the populated log was modified"

: > "$STUB_LOG"
bash "$TL" --context other-cluster --force start demo-ns "$LOG" >/dev/null 2>&1 \
    && note_pid "$(pid_of "$LOG")"
sleep 1
grep -q -- "--context other-cluster" "$STUB_LOG" \
    && ok "tail passes --context to kubectl" \
    || bad "tail did not pass --context: $(head -1 "$STUB_LOG")"

# a live tail whose output is still empty must not be stealable either
: > "$LOG"
if bash "$TL" --context third-cluster start demo-ns "$LOG" >/dev/null 2>"$WORK/e"; then
    bad "a second tail stole a live capture with an empty log"
    note_pid "$(pid_of "$LOG")"
else
    grep -q "a capture already owns" "$WORK/e" \
        && ok "a live tail with an empty log is not stealable" \
        || bad "second tail failed for the wrong reason: $(head -1 "$WORK/e")"
fi

# stopping one capture must not reach another one in the same namespace
OTHER="$WORK/other.log"
bash "$TL" --context cluster-a --force start demo-ns "$OTHER" >/dev/null 2>&1 \
    && note_pid "$(pid_of "$OTHER")"
sleep 1
OTHER_PID="$(pid_of "$OTHER")"
bash "$TL" --context other-cluster stop demo-ns "$LOG" >/dev/null 2>&1 || true
if [ -n "$OTHER_PID" ] && kill -0 "$OTHER_PID" 2>/dev/null; then
    ok "stopping one capture leaves another in the same namespace alone"
else
    bad "stopping one capture killed another in the same namespace"
fi
bash "$TL" --context cluster-a stop demo-ns "$OTHER" >/dev/null 2>&1 || true

# ---- the count itself ------------------------------------------------
if [ "$checks" -eq "$EXPECTED_CHECKS" ]; then
    ok "all $EXPECTED_CHECKS checks ran"
else
    bad "expected $EXPECTED_CHECKS checks, ran $checks -- a block stopped running"
fi

echo "benchmark capture checks: $checks run, $fails failed"
[ "$fails" -eq 0 ] || exit 1
