#!/usr/bin/env bash
# Execute the benchmark capture helpers against a stub kubectl and assert the
# properties that cost real data when they were missing.
#
# `bash -n` sees none of this. A capture that replaces the run already on disk,
# that finalises a file it does not own, that watches a cluster nobody named, or
# that kills an unrelated process, all parse perfectly.
#
# Every case corresponds to a defect measured on this code, not to a
# hypothetical. The count is asserted at the end, because a block that stops
# running would otherwise read as green.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SR="$ROOT/hack/benchmark/sample_replicas.sh"
TL="$ROOT/hack/benchmark/tail_wva_logs.sh"

EXPECTED_CHECKS=37

WORK="$(mktemp -d)"
STARTED_PIDS=""

# Kill what we started, on any exit path, by process group -- the captures lead
# their own groups. An EXIT-only trap leaves strays when CI is cancelled, and
# killing a wrapper alone reparents its children, which is how an earlier version
# left two hour-long sleeps per run on a developer box.
cleanup() {
    for p in $STARTED_PIDS; do
        kill -TERM "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null || true
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
owner_of() { cat "$1.owner" 2>/dev/null || true; }

cat > "$WORK/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "$@" >> "${STUB_LOG:?}"
echo '{"items":[]}'
STUB
chmod +x "$WORK/kubectl"
export KUBECTL_CMD="$WORK/kubectl"
export STUB_LOG="$WORK/calls.log"
: > "$STUB_LOG"
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

# ---- a refused start leaves no ownership record behind ------------------
# A refusal that keeps the claim would block the path for good.
[ ! -f "$OUT.owner" ] \
    && ok "a refused start leaves no ownership record" \
    || bad "a refused start left a record: $(owner_of "$OUT")"

# ---- and a stop after a refused start must not corrupt it --------------
bash "$SR" stop "$OUT" demo-ns >/dev/null 2>&1 || true
[ "$(cat "$OUT")" = "$BEFORE" ] \
    && ok "stop after a refused start leaves the file alone" \
    || bad "stop corrupted a file it never owned: $(head -c 48 "$OUT")"

# ---- --force replaces it; --context reaches kubectl and the record ----
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

owner="$(owner_of "$OUT")"
case "$owner" in
    *"namespace=demo-ns"*) ok "the record names the namespace" ;;
    *) bad "record has no namespace: $owner" ;;
esac
case "$owner" in
    *"context=my-cluster"*) ok "the record names the context" ;;
    *) bad "record has no context: $owner" ;;
esac
case "$owner" in
    *starttime=[0-9]*) ok "the record carries the owner start time, so a reused pid is not mistaken for it" ;;
    *) bad "record has no starttime: $owner" ;;
esac

# ---- a LIVE capture is not stealable, whatever its output weighs -------
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
    && ok "the first capture still runs and still owns the pidfile" \
    || bad "the first capture was displaced"

# ---- the whole capture tree dies on stop ------------------------------
# The sampler runs kubectl and python inside a command substitution, so they are
# grandchildren; `pkill -P` reached children only and they outlived the stop,
# printing BrokenPipeError after the success line.
bash "$SR" --context my-cluster stop "$OUT" demo-ns >"$WORK/o" 2>"$WORK/e" || true
# Polled, not slept: a fixed sleep was the only timing assumption in this file.
for _ in $(seq 1 50); do
    kill -0 "$FIRST_PID" 2>/dev/null || break
    sleep 0.1
done
if kill -0 "$FIRST_PID" 2>/dev/null; then
    bad "the capture survived its own stop"
else
    ok "stop ends the capture"
fi
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$OUT" 2>/dev/null \
    && ok "the finalised samples file is valid JSON" \
    || bad "the finalised samples file does not parse"
[ ! -f "$OUT.owner" ] \
    && ok "stop drops the ownership record" \
    || bad "stop left a record behind: $(owner_of "$OUT")"

# ---- stop is idempotent ----------------------------------------------
bash "$SR" stop "$OUT" demo-ns >/dev/null 2>&1 || true
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$OUT" 2>/dev/null \
    && ok "a second stop does not corrupt the file" \
    || bad "a second stop corrupted the file: $(tail -c 12 "$OUT")"

# ---- a reused pid is neither honoured nor killed ---------------------
# A pid alone is not an identity. An unrelated process whose pid landed in the
# record used to block start forever AND be killed by stop.
sleep 600 &
VICTIM=$!
note_pid "$VICTIM"
O6="$WORK/recycled.json"
: > "$O6"
printf 'namespace=old context=old started=then pid=%s starttime=1 cmd=x\n' "$VICTIM" > "$O6.owner"
echo "$VICTIM" > "$O6.pid"
if bash "$SR" start fresh-ns "$O6" >"$WORK/o" 2>"$WORK/e"; then
    note_pid "$(pid_of "$O6")"
    ok "a record whose start time does not match is not treated as live"
else
    bad "a reused pid blocked a new capture: $(head -1 "$WORK/e")"
fi
bash "$SR" stop "$O6" fresh-ns >/dev/null 2>&1 || true
kill -0 "$VICTIM" 2>/dev/null \
    && ok "stop did not kill the unrelated process holding that pid" \
    || bad "stop killed an unrelated process"
kill "$VICTIM" 2>/dev/null || true

# ---- mismatches are reported ----------------------------------------
O7="$WORK/mismatch.json"
bash "$SR" --context c-one start ns-one "$O7" >/dev/null 2>&1 && note_pid "$(pid_of "$O7")"
sleep 1
warn="$(bash "$SR" --context c-two stop "$O7" ns-two 2>&1 >/dev/null || true)"
case "$warn" in
    *"namespace=ns-two"*) ok "stop warns on a namespace mismatch" ;;
    *) bad "no namespace warning: $(echo "$warn" | head -1)" ;;
esac
case "$warn" in
    *"context=c-two"*) ok "stop warns on a context mismatch" ;;
    *) bad "no context warning: $(echo "$warn" | head -1)" ;;
esac

# ---- no namespace on stop is not a mismatch -------------------------
O2="$WORK/quiet.json"
bash "$SR" --context c1 start quiet-ns "$O2" >/dev/null 2>&1 && note_pid "$(pid_of "$O2")"
sleep 1
quiet="$(bash "$SR" stop "$O2" 2>&1 >/dev/null || true)"
[ -z "$quiet" ] \
    && ok "stop without a namespace says nothing" \
    || bad "stop without a namespace warned anyway: $(echo "$quiet" | head -1)"

# ---- the stop arguments may be given in either order ----------------
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

# ---- flags are honoured wherever they appear -----------------------
: > "$STUB_LOG"
O4="$WORK/after.json"
bash "$SR" start after-ns "$O4" --context trailing-ctx >/dev/null 2>&1 && note_pid "$(pid_of "$O4")"
sleep 1
grep -q -- "--context trailing-ctx" "$STUB_LOG" \
    && ok "a flag after the subcommand is still honoured" \
    || bad "a flag after the subcommand was dropped: $(head -1 "$STUB_LOG")"
bash "$SR" stop "$O4" after-ns >/dev/null 2>&1 || true

# ---- a stray positional is an error -------------------------------
if bash "$SR" start a-ns "$WORK/x.json" extra-junk >/dev/null 2>"$WORK/e"; then
    bad "a stray positional was accepted"
else
    grep -q "unexpected argument" "$WORK/e" \
        && ok "a stray positional is refused" \
        || bad "stray positional failed for the wrong reason: $(head -1 "$WORK/e")"
fi

# ---- an empty --context= is an error ------------------------------
if bash "$SR" --context= start a-ns "$WORK/y.json" >/dev/null 2>"$WORK/e"; then
    bad "--context= was accepted and silently meant inherited"
else
    ok "an empty --context= is refused"
fi

# ---- a path that cannot be written fails loudly -------------------
RO="$WORK/ro"
mkdir -p "$RO"
chmod 500 "$RO"
if bash "$SR" --force start demo-ns "$RO/z.json" >"$WORK/o" 2>"$WORK/e"; then
    bad "start reported success for a path it cannot write: $(head -1 "$WORK/o")"
else
    grep -qE "cannot write|cannot create" "$WORK/e" \
        && ok "start refuses a path it cannot write, and says which" \
        || bad "unwritable path failed for the wrong reason: $(head -1 "$WORK/e")"
fi
chmod 700 "$RO"


# ---- an empty record is a claim in progress, not a capture to finalise -----
# start must refuse, and stop must NOT append a terminator to whatever is there.
# This is the one refusal path that leaves an empty record behind, and it used to
# corrupt a previous run's file through it.
O8="$WORK/emptyrec.json"
printf '{"snapshots":[{"prev":"run"}]}' > "$O8"
B8="$(cat "$O8")"
: > "$O8.owner"
if bash "$SR" start e-ns "$O8" >/dev/null 2>"$WORK/e"; then
    bad "start proceeded against a record being written by someone else"
else
    ok "start refuses while a record is mid-claim"
fi
grep -q "remove" "$WORK/e" \
    && bad "the mid-claim refusal tells the operator to delete the winner's record" \
    || ok "the mid-claim refusal does not advise deleting a live record"
bash "$SR" stop "$O8" e-ns >/dev/null 2>&1 || true
[ "$(cat "$O8")" = "$B8" ] \
    && ok "stop leaves a file alone when the record says nothing" \
    || bad "stop corrupted a file behind an empty record: $(tail -c 16 "$O8")"
rm -f "$O8.owner"

# ---- a lone namespace is not an output path ------------------------------
# `stop <ns>` used to be accepted as `stop <outfile>`: exit 0, "nothing to stop",
# sampler still running. The tail guards this; the sampler did not, and it is the
# likelier mistake because the tail documents the opposite argument order.
O9="$WORK/lone.json"
bash "$SR" start lone-ns "$O9" >/dev/null 2>&1 && note_pid "$(pid_of "$O9")"
sleep 1
P9="$(pid_of "$O9")"
if bash "$SR" stop lone-ns >/dev/null 2>"$WORK/e"; then
    bad "the sampler accepted a lone namespace as its outfile"
else
    ok "a lone namespace on stop is refused"
fi
if [ -n "$P9" ] && kill -0 "$P9" 2>/dev/null; then
    ok "and the capture is left running rather than reported stopped"
else
    bad "the refused stop killed the capture anyway"
fi
bash "$SR" stop "$O9" lone-ns >/dev/null 2>&1 || true

# ---- a stop that does not stop must fail --------------------------------
# Reporting success and then dropping the record leaves a live capture invisible
# to the tooling, still appending to a file that already has its terminator.
# Simulated with a process that ignores SIGTERM, which is the same shape as a kill
# that cannot be delivered.
O10="$WORK/nokill.json"
bash -c 'trap "" TERM; sleep 300' &
STUBBORN=$!
note_pid "$STUBBORN"
printf '{"snapshots":[' > "$O10"
echo "$STUBBORN" > "$O10.pid"
ST10="$(sed -n 's/^[0-9][0-9]* (.*) //p' "/proc/$STUBBORN/stat" 2>/dev/null | awk '{print $20}')"
printf 'namespace=nk-ns context=nk-ctx started=now pid=%s starttime=%s cmd=x\n' \
    "$STUBBORN" "$ST10" > "$O10.owner"
if bash "$SR" stop "$O10" nk-ns >/dev/null 2>"$WORK/e"; then
    bad "stop reported success for a capture that is still running"
else
    grep -q "did not stop" "$WORK/e" \
        && ok "a stop that cannot end the capture fails and says so" \
        || bad "stop failed without explaining why: $(head -1 "$WORK/e")"
fi
[ -f "$O10.owner" ] \
    && ok "and it keeps the record rather than losing track of the capture" \
    || bad "a failed stop removed the record anyway"
kill -KILL "$STUBBORN" 2>/dev/null || true

# ---- the log tail carries the same guards -------------------------
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

# a live tail whose output is still empty must not be stealable
: > "$LOG"
if bash "$TL" --context third-cluster start demo-ns "$LOG" >/dev/null 2>"$WORK/e"; then
    bad "a second tail stole a live capture with an empty log"
    note_pid "$(pid_of "$LOG")"
else
    grep -q "a capture already owns" "$WORK/e" \
        && ok "a live tail with an empty log is not stealable" \
        || bad "second tail failed for the wrong reason: $(head -1 "$WORK/e")"
fi

# tail stop requires its outfile: making it optional turned `stop <ns>` into
# exit 0, "nothing to stop", and a capture left running.
if bash "$TL" stop demo-ns >/dev/null 2>"$WORK/e"; then
    bad "tail stop accepted a missing outfile and reported success"
else
    ok "tail stop requires its outfile"
fi
bash "$TL" --context other-cluster stop demo-ns "$LOG" >/dev/null 2>&1 || true

# ---- the count itself --------------------------------------------
# Asserted without ok()/bad(), which would change the number being asserted.
if [ "$checks" -eq "$EXPECTED_CHECKS" ]; then
    echo "  ok: all $EXPECTED_CHECKS checks ran"
else
    echo "  FAIL: expected $EXPECTED_CHECKS checks, ran $checks -- a block stopped running" >&2
    fails=$((fails + 1))
fi

echo "benchmark capture checks: $checks run, $fails failed"
[ "$fails" -eq 0 ] || exit 1
