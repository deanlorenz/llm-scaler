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

EXPECTED_CHECKS=57

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


# ---- only one of two concurrent stops finalises --------------------------
# Both used to pass capture_owned, both saw the capture already dead, and both
# appended a JSON terminator: 15 of 15 trials produced `...]}]}` while both
# invocations exited 0 reporting "unreadable".
O11="$WORK/concstop.json"
bash "$SR" start cs-ns "$O11" >/dev/null 2>&1 && note_pid "$(pid_of "$O11")"
sleep 1
bash "$SR" stop "$O11" cs-ns >/dev/null 2>&1 &
bash "$SR" stop "$O11" cs-ns >/dev/null 2>&1 &
wait
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$O11" 2>/dev/null \
    && ok "two concurrent stops leave valid JSON" \
    || bad "concurrent stops corrupted the file: $(tail -c 12 "$O11")"

# ---- a namespace pair is not an output path ----------------------------
# The lone-argument guard was gated on the argument COUNT, so two namespaces
# skipped it: exit 0, "nothing to stop", capture still running.
O12="$WORK/twoarg.json"
bash "$SR" start ta-ns "$O12" >/dev/null 2>&1 && note_pid "$(pid_of "$O12")"
sleep 1
P12="$(pid_of "$O12")"
if bash "$SR" stop ta-ns other-ns >/dev/null 2>"$WORK/e"; then
    bad "two namespaces were accepted as a stop target"
else
    ok "two arguments that own no record are refused"
fi
if [ -n "$P12" ] && kill -0 "$P12" 2>/dev/null; then
    ok "and that capture is left running rather than reported stopped"
else
    bad "the refused two-argument stop killed the capture"
fi
bash "$SR" stop "$O12" ta-ns >/dev/null 2>&1 || true

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




# ---- a stop that cannot terminate the file does not report success ------
# A read-only output FILE no longer defeats a stop: the artefact is assembled to a
# temp file and renamed, and a rename needs no write permission on its target. A
# read-only DIRECTORY does still prevent it, and that must be reported rather than
# reported as success.
RODIR="$WORK/rodir"
mkdir -p "$RODIR"
O15="$RODIR/rofile.json"
bash "$SR" start ro-ns "$O15" >/dev/null 2>&1 && note_pid "$(pid_of "$O15")"
sleep 1
chmod 500 "$RODIR"
if bash "$SR" stop "$O15" ro-ns >/dev/null 2>"$WORK/e"; then
    bad "stop reported success when it could not assemble the artefact"
else
    grep -q "could not assemble" "$WORK/e" \
        && ok "a stop that cannot assemble its artefact says so and fails" \
        || bad "the assembly failure was not reported: $(head -1 "$WORK/e")"
fi
chmod 700 "$RODIR" 2>/dev/null || true

# ---- an output path containing a pipe --------------------------------
# The resolver used to return "<out>|<ns>" in one string, so such a path was split
# in the middle: stop reported success against a truncated name and the capture
# kept running.
O16="$WORK/pipe|name.json"
bash "$SR" start pp-ns "$O16" >/dev/null 2>&1 && note_pid "$(pid_of "$O16")"
sleep 1
P16="$(pid_of "$O16")"
bash "$SR" stop "$O16" pp-ns >/dev/null 2>&1 || true
if [ -n "$P16" ] && kill -0 "$P16" 2>/dev/null; then
    bad "a path containing a pipe left the capture running"
else
    ok "an output path containing a pipe is handled whole"
fi

# ---- the tail: concurrent stops, and the dedup temp name -------------
LC="$WORK/conc.log"
bash "$TL" --context tc-ctx start tc-ns "$LC" >/dev/null 2>&1 && note_pid "$(pid_of "$LC")"
sleep 1
printf 'dup\ndup\nuniq\n' >> "$LC"
bash "$TL" stop tc-ns "$LC" >"$WORK/o1" 2>"$WORK/e1" &
bash "$TL" stop tc-ns "$LC" >"$WORK/o2" 2>"$WORK/e2" &
wait
grep -q "cannot stat" "$WORK/e1" "$WORK/e2" 2>/dev/null \
    && bad "two tail stops raced on one dedup temp name" \
    || ok "two concurrent tail stops do not race on a temp name"
ls "$LC".dedup* >/dev/null 2>&1 \
    && bad "a dedup temp file was left behind" \
    || ok "no dedup temp file survives a tail stop"

# ---- the tail refuses a namespace pair too --------------------------
# The sampler grew this guard in round 4; the tail never did, which is the
# asymmetry the shared library exists to prevent.
LT="$WORK/twoarg.log"
bash "$TL" start tt-ns "$LT" >/dev/null 2>&1 && note_pid "$(pid_of "$LT")"
sleep 1
PT="$(pid_of "$LT")"
if bash "$TL" stop tt-ns other-ns >/dev/null 2>"$WORK/e"; then
    bad "the tail accepted two namespaces as a stop target"
else
    ok "the tail refuses two arguments that own no record"
fi
if [ -n "$PT" ] && kill -0 "$PT" 2>/dev/null; then
    ok "and leaves that tail running rather than reporting it stopped"
else
    bad "the refused two-argument tail stop killed the capture"
fi
bash "$TL" stop tt-ns "$LT" >/dev/null 2>&1 || true



# ---- the filing decision ---------------------------------------------
FC="$ROOT/hack/benchmark/file_capture.sh"
mkres() { rm -rf "$WORK/res"; mkdir -p "$WORK/res"; }

# a good set: everything is filed
mkres
printf '{"snapshots":[{"a":1}]}' > "$WORK/f.json"
printf '{"pods":[{"name":"p"}]}' > "$WORK/f.json.pod_timings.json"
printf 'log line\n' > "$WORK/f.log"
out="$(bash "$FC" "$WORK/res" "$WORK/f.json" "$WORK/f.log" 2>&1)"
[ -f "$WORK/res/metrics/processed/wva_replica_samples.json" ] \
    && [ -f "$WORK/res/metrics/processed/wva_pod_timings.json" ] \
    && [ -f "$WORK/res/wva_controller.log" ] \
    && ok "a complete capture files all three artefacts" \
    || bad "a complete capture did not file everything: $out"

# a corrupt samples file must not take the others down with it
mkres
printf '{"snapshots":[{"a":1}' > "$WORK/f.json"
out="$(bash "$FC" "$WORK/res" "$WORK/f.json" "$WORK/f.log" 2>&1)"
[ ! -f "$WORK/res/metrics/processed/wva_replica_samples.json" ] \
    && ok "an unparseable samples file is withheld" \
    || bad "an unparseable samples file was filed"
[ -f "$WORK/res/metrics/processed/wva_pod_timings.json" ] \
    && ok "and valid pod timings are still filed beside it" \
    || bad "valid pod timings died with the samples file: $out"
[ -f "$WORK/res/wva_controller.log" ] \
    && ok "and so is the controller log" \
    || bad "the controller log died with the samples file"

# a capture that never started must not file whatever is at the path
mkres
printf '{"snapshots":[{"PREVIOUS":"RUN"}]}' > "$WORK/f.json"
touch "$WORK/f.json.startfailed"
out="$(bash "$FC" "$WORK/res" "$WORK/f.json" "$WORK/f.log" 2>&1)"
[ ! -f "$WORK/res/metrics/processed/wva_replica_samples.json" ] \
    && ok "a capture that never started files nothing from its path" \
    || bad "a previous run's file was filed as this run's measurement"
case "$out" in
    *"never started"*) ok "and says that is why" ;;
    *) bad "the reason was not reported: $out" ;;
esac
rm -f "$WORK/f.json.startfailed"

# a capture that could not be stopped
mkres
printf '{"snapshots":[{"a":1}]}' > "$WORK/f.json"
touch "$WORK/f.json.stopfailed"
out="$(bash "$FC" "$WORK/res" "$WORK/f.json" "$WORK/f.log" 2>&1)"
[ ! -f "$WORK/res/metrics/processed/wva_replica_samples.json" ] \
    && ok "a capture that could not be stopped is withheld" \
    || bad "an incomplete capture was filed"
rm -f "$WORK/f.json.stopfailed"

# no results directory is not an error
out="$(bash "$FC" "$WORK/nosuchdir" "$WORK/f.json" "$WORK/f.log" 2>&1)"; rc=$?
[ "$rc" -eq 0 ] \
    && ok "a run with no results directory files nothing and does not fail" \
    || bad "a missing results directory was treated as an error"

# ---- assembling the artefact is idempotent ------------------------------
# This is the property that replaced seven rounds of finalise machinery. The
# capture appends one JSON object per line and stop assembles the array from those
# lines, so the answer cannot depend on who ran it, how many times, or whether a
# previous attempt was killed part-way.
OI="$WORK/idem.json"
bash "$SR" start idem-ns "$OI" >/dev/null 2>&1 && note_pid "$(pid_of "$OI")"
sleep 2
bash "$SR" stop "$OI" idem-ns >/dev/null 2>&1 || true
first="$(cat "$OI" 2>/dev/null)"
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$OI" 2>/dev/null \
    && ok "a stop assembles a valid artefact" \
    || bad "the assembled artefact does not parse: $(tail -c 20 "$OI")"

# ---- two concurrent stops produce identical bytes ----------------------
OC="$WORK/conc2.json"
bash "$SR" start c2-ns "$OC" >/dev/null 2>&1 && note_pid "$(pid_of "$OC")"
sleep 2
bash "$SR" stop "$OC" c2-ns >/dev/null 2>&1 &
bash "$SR" stop "$OC" c2-ns >/dev/null 2>&1 &
wait
python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$OC" 2>/dev/null \
    && ok "two concurrent stops leave a valid artefact" \
    || bad "concurrent stops corrupted the artefact: $(tail -c 20 "$OC")"

# ---- and a stop repeated on the assembled file changes nothing ---------
# Where the old design appended a second terminator, re-running is now a no-op:
# the record is gone, so stop declines, and even if it did not the assembly is a
# function of the lines.
before="$(cat "$OC" 2>/dev/null)"
bash "$SR" stop "$OC" c2-ns >/dev/null 2>&1 || true
[ "$(cat "$OC" 2>/dev/null)" = "$before" ] \
    && ok "repeating a stop leaves the artefact byte-identical" \
    || bad "a repeated stop changed the artefact: $(tail -c 20 "$OC")"

# ---- a torn line costs its own snapshot, not the measurement -----------
# A kill mid-write can leave a partial line. The snapshots before it are still a
# measurement, so assembly drops the torn line rather than failing.
OT="$WORK/torn.json"
bash "$SR" start torn-ns "$OT" >/dev/null 2>&1 && note_pid "$(pid_of "$OT")"
sleep 2
printf '{"timestamp": "partial' >> "$OT.snapshots.jsonl"
bash "$SR" stop "$OT" torn-ns >/dev/null 2>&1 || true
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if d["snapshots"] else 1)' "$OT" 2>/dev/null \
    && ok "a torn line is dropped and the earlier snapshots survive" \
    || bad "a torn line cost the whole measurement: $(head -c 40 "$OT")"

# ---- no intermediate files survive a stop -----------------------------
ls "$OT".snapshots.jsonl "$OT".assembling.* >/dev/null 2>&1 \
    && bad "an intermediate file survived the stop" \
    || ok "no intermediate files survive a stop"

# ---- a capture that never started files NEITHER of its artefacts --------
# Both come from the sampler, so both consult the sampler's flags. Deriving the
# flag name from the artefact made the timings check a name nothing writes, and a
# previous run's timings were filed as this run's measurement.
mkres
printf '{"snapshots":[{"PREVIOUS":"RUN"}]}' > "$WORK/f.json"
printf '{"pods":[{"name":"FROM-A-PREVIOUS-RUN"}]}' > "$WORK/f.json.pod_timings.json"
touch "$WORK/f.json.startfailed"
out="$(bash "$FC" "$WORK/res" "$WORK/f.json" "$WORK/f.log" 2>&1)"
[ ! -f "$WORK/res/metrics/processed/wva_pod_timings.json" ] \
    && ok "a capture that never started files neither of its artefacts" \
    || bad "a previous run's pod timings were filed: $out"
rm -f "$WORK/f.json.startfailed" "$WORK/f.json.pod_timings.json"

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
