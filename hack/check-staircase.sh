#!/usr/bin/env bash
# Executes the TTFT staircase against a stub kubectl, make and whatif, and runs its
# arithmetic self-test.
#
# A run holds GPUs on a shared cluster for days, and what protects them is exit paths,
# which `bash -n` does not see. So every way out is taken here: the teardown must run on
# each, the deadline must be armed before anything holds a GPU, and no step may run on a
# prediction that moved. The count is asserted at the end.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
B="$ROOT/hack/benchmark"
PY="$(command -v python3 || command -v python)" || { echo "FAIL: python3 not found"; exit 1; }
EXPECTED_CHECKS=32

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
checks=0
ok() { checks=$((checks + 1)); echo "  ok: $1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); echo "  FAIL: $1" >&2; }
expect() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }

echo "arithmetic..."
if "$PY" "$B/staircase_selftest.py"; then ok "self-test"; else bad "self-test"; fi

# A level no window can judge would read a NaN p90, and NaN never trips the frozen rule.
if "$PY" "$B/staircase_load.py" --url u --model m --out "$WORK/o" --schedule 6:10:50:3:1 --runaway 1 --dry-run > /dev/null 2>&1; then
    bad "a stop rule on a level with nothing to judge must refuse"
else
    ok "a stop rule on a level with nothing to judge refuses"
fi
if "$PY" "$B/staircase_load.py" --url u --model m --out "$WORK/o" --schedule 600:10:50:3:4 --runaway 1 --dry-run | grep -q 'Ca2 [34]\.'; then
    ok "the driver prints the Ca2 it will send"
else
    bad "the driver prints the Ca2 it will send"
fi

mkdir -p "$WORK/bin"
STUB="$WORK/bin"

cat > "$STUB/make" <<'STUB'
#!/usr/bin/env bash
echo "make $*" >> "$LOG"
case "$*" in *benchmark-standup*)
    if [ -n "${MAKE_SLEEP:-}" ]; then
        sleep "$MAKE_SLEEP" &
        echo $! > "$STATE/standup-sleep"
        wait
    fi
    [ -n "${MAKE_FAIL:-}" ] && exit 2 ;;
esac
exit 0
STUB

cat > "$STUB/whatif" <<'STUB'
#!/usr/bin/env bash
# whatif freeze --recording R --spec S -o OUT
while [ $# -gt 0 ]; do
    case $1 in -o) out=$2; shift ;; --spec) spec=$2; shift ;; esac
    shift
done
[ -e "$out" ] && exit 2
sum=$(sha256sum "$spec" | cut -d' ' -f1)
sed "s/__SPEC__/$sum/" "$FROZEN_FIXTURE" > "$out"
STUB

cat > "$WORK/frozen.json" <<'JSON'
{"specSha256":"__SPEC__","gate":{"vp":true,"a":true,"decodeLine":true,"sloListed":true},"buckets":[
{"name":"B","prompt":8000,"output":200,"ca2":1,"prefill":2,"decode":2,"vp":288288,"imbalance":1.19,"decodeKnee":44,"knee":44,
"onsets":{"Prefill":54.6},"curve":[{"rate":17.6}],"targets":[{"percentile":90,"seconds":0.2,"predictedOff":37,"predictedOn":37}]}]}
JSON

cat > "$WORK/spec.yaml" <<'YAML'
promptCV: 0.0577
holdSeconds: 600
discardSeconds: 180
maxHoldSeconds: 1500
runawayFactor: 2
maxInflight: 4000
repeats: 1
seedBase: 3000
phaseOverheadSeconds: 240
warmup:
  order: [sweep]
  sweep: {driver: open, schedule: "1:1:1000:32:1", seed: 11}
buckets:
- {name: B, fc: ["off", "on"], gate: true}
YAML

cat > "$STUB/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "kubectl $*" >> "$LOG"
pd='featureGates:\n- flowControl\nplugins:\n- type: a\nflowControl:\n  maxBytes: 1\nschedulingProfiles:\n- name: b\n'
[ -e "$STATE/fc-off" ] && pd='plugins:\n- type: a\nschedulingProfiles:\n- name: b\n'
args=" $* "
case "$args" in
    *" get namespace "*) [ -n "${NS_EXISTS:-}" ]; exit ;;
    *" logs job/staircase-deadline "*) echo "t deadline at x" ;;
    *" get deploy -o name "*) printf 'deployment.apps/m-decode\ndeployment.apps/m-prefill\ndeployment.apps/m-epp\n' ;;
    *" get cm -o json "*) printf '{"items":[{"metadata":{"name":"eppcm"},"data":{"pd-config.yaml":"x"}}]}\n' ;;
    *" get cm eppcm -o json "*) printf '{"data":{"pd-config.yaml":"%s"}}\n' "$pd" ;;
    *" patch cm eppcm "*) case "$args" in *epp-patch-off*) touch "$STATE/fc-off" ;; *) rm -f "$STATE/fc-off" ;; esac ;;
    *" get svc -o json "*) printf '{"items":[{"metadata":{"name":"m-epp"},"spec":{"ports":[{"port":80}]}}]}\n' ;;
    *" get configmap wva-scaling-policy-config "*) printf '{"data":{"default":"analyzers:\\n  - name: saturation\\n    score: 1\\n"}}\n' ;;
    *" get deploy m-decode -o json "*|*" get deploy m-prefill -o json "*)
        printf '{"spec":{"template":{"spec":{"containers":[{"name":"vllm","env":[{"name":"VLLM_MAX_MODEL_LEN","value":"40960"}]}]}}}}\n' ;;
    *" get scaledobject -o json "*) printf '{"items":[{"metadata":{"name":"so-d"},"spec":{"scaleTargetRef":{"name":"m-decode"}}},{"metadata":{"name":"so-p"},"spec":{"scaleTargetRef":{"name":"m-prefill"}}}]}\n' ;;
    *" get deploy m-"*"jsonpath="*) echo 2 ;;
    *" get pod -l control-plane=controller-manager -o json "*)
        printf '{"items":[{"metadata":{"name":"ctl"},"status":{"phase":"Running"}}]}\n' ;;
    *" get pods -o json "*) printf '{"items":[]}\n' ;;
    *" get pods --no-headers "*) : ;;
    *" get --raw "*) : ;;
    *" exec ctl "*) printf '{"Kind":"start","Data":{"Commit":"abc"}}\n' ;;
    *" cp "*) d="${*: -1}"; mkdir -p "$d"; echo recording-bytes > "$d/learnrec-1.jsonl" ;;
    *" exec "*"gzip"*) printf 'index\n' | gzip ;;
    *" logs lg-"*|*" logs w-"*) printf '2026-11-06T02:00:00Z LEVEL 1 rate=1 in=1 out=1 ca2=1 hold=1s\n2026-11-06T02:00:02Z SWEEP DONE ok=1 err=0 runaway=no\n' ;;
    *" apply -f "*"lg-"*|*" apply -f "*"load.yaml"*)
        case "$args" in *"/phases/"*)
            echo "step" >> "$STATE/steps"
            [ -n "${TAMPER:-}" ] && echo "tampered" >> "$RUN/ladder.txt" ;;
        esac ;;
esac
exit 0
STUB
chmod +x "$STUB"/*

# fresh <name> : a run directory and a log of its own.
fresh() {
    export RUN="$WORK/$1" LOG="$WORK/$1.log" STATE="$WORK/$1.state"
    mkdir -p "$STATE"; : > "$LOG"
}

# base_env <command...> : run it under the stubs.
base_env() {
    exec_env=(env PATH="$STUB:$PATH" BENCHMARK_NAMESPACE=stair-ns STAIRCASE_RUN="$RUN" BENCHMARK_PROMETHEUS_URL=http://p:9090
        STAIRCASE_SPEC="$WORK/spec.yaml" STAIRCASE_ROOT="$WORK" STAIRCASE_WHATIF="$STUB/whatif" FROZEN_FIXTURE="$WORK/frozen.json"
        STAIRCASE_POLL=0.2 STAIRCASE_SETTLE=0 STAIRCASE_ARM_POLL=0 STAIRCASE_TEARDOWN_POLL=0 STAIRCASE_FREEZE_WAIT=30)
    ("${exec_env[@]}" "$@")
}

# wait_for <pattern> <file> : capped, so a broken run fails rather than hangs.
wait_for() {
    local i
    for i in $(seq 1 150); do
        grep -q "$1" "$2" 2>/dev/null && return 0
        sleep 0.2
    done
    return 1
}

# Whether a teardown reached the harness and the load, whatever happened before.
tore_down() { grep -q 'make --no-print-directory benchmark-teardown' "$LOG" && grep -q 'delete job,deploy,lws' "$LOG"; }

echo "a whole run..."
fresh ok
base_env bash "$B/staircase.sh" run img@sha256:0 > "$WORK/ok.out" 2>&1 &
pid=$!
if wait_for 'waiting for the freeze' "$RUN/timeline.txt"; then ok "reaches the freeze"; else bad "reaches the freeze"; cat "$WORK/ok.out"; fi
expect "the warm-up left a recording" '[ -s "$RUN/recordings-warmup/learnrec-1.jsonl" ]'
expect "nothing ran before the freeze" '[ ! -e "$STATE/steps" ]'
base_env bash "$B/staircase.sh" freeze > "$WORK/freeze.out" 2>&1
expect "freeze writes the hashes" '[ -s "$RUN/frozen.sha256" ] && [ -e "$RUN/ladder.ready" ]'
expect "the plan is printed" 'grep -q "GPU-hours" "$RUN/plan.md"'
wait "$pid"; rc=$?
expect "the run exits 0" '[ "$rc" = 0 ]'
expect "both phases ran" '[ "$(wc -l < "$STATE/steps")" = 2 ]'
expect "the deadline is armed before the standup" '[ "$(grep -n "apply -f .*deadline.yaml" "$LOG" | head -1 | cut -d: -f1)" -lt "$(grep -n benchmark-standup "$LOG" | head -1 | cut -d: -f1)" ]'
expect "slo is listed before the recorder" '[ "$(grep -n "patch configmap wva-scaling-policy-config" "$LOG" | head -1 | cut -d: -f1)" -lt "$(grep -n "patch deployment wva-controller-manager" "$LOG" | head -1 | cut -d: -f1)" ]'
expect "the lease was renewed" 'grep -q "patch cm staircase-lease" "$LOG"'
expect "the fleet was pinned" 'grep -q "\"minReplicaCount\":2,\"maxReplicaCount\":2" "$LOG"'
expect "teardown on success" 'tore_down'
expect "the private copy ran" '[ -s "$RUN/bin/staircase.sh" ] && grep -q "run start" "$RUN/timeline.txt"'
if base_env bash "$B/staircase.sh" freeze > /dev/null 2>&1; then bad "a second freeze must refuse"; else ok "a second freeze refuses"; fi
if base_env bash "$B/staircase.sh" run img > /dev/null 2>&1; then bad "a used directory must refuse"; else ok "a used directory refuses"; fi

echo "exit paths..."
fresh nofreeze
mkdir -p "$RUN/recordings-warmup" "$RUN/phases"
echo x > "$RUN/recordings-warmup/a.jsonl"
if base_env bash "$B/staircase.sh" freeze > /dev/null 2>&1; then bad "a freeze after a phase must refuse"; else ok "a freeze after a phase refuses"; fi

fresh exists
if base_env NS_EXISTS=1 bash "$B/staircase.sh" run img > /dev/null 2>&1; then bad "an existing namespace must refuse"; else ok "an existing namespace refuses"; fi
expect "and nothing was created" '! grep -q "create namespace" "$LOG"'
expect "and nothing is torn down there" '! tore_down'

fresh standupfail
if base_env MAKE_FAIL=1 bash "$B/staircase.sh" run img > /dev/null 2>&1; then bad "a failed standup must fail"; else ok "a failed standup fails"; fi
expect "teardown after a failed standup" 'tore_down'
expect "and no recorder" '! grep -q "patch deployment wva-controller-manager" "$LOG"'

fresh term
exec_env=(env PATH="$STUB:$PATH" BENCHMARK_NAMESPACE=stair-ns STAIRCASE_RUN="$RUN" BENCHMARK_PROMETHEUS_URL=http://p:9090
    STAIRCASE_SPEC="$WORK/spec.yaml" STAIRCASE_ROOT="$WORK" STAIRCASE_WHATIF="$STUB/whatif" FROZEN_FIXTURE="$WORK/frozen.json"
    STAIRCASE_POLL=0.2 STAIRCASE_SETTLE=0 STAIRCASE_ARM_POLL=0 STAIRCASE_TEARDOWN_POLL=0)
# A simple command, not the function: the job's pid must be the run's own.
"${exec_env[@]}" MAKE_SLEEP=60 bash "$B/staircase.sh" run img > /dev/null 2>&1 &
pid=$!
if wait_for 'make .*benchmark-standup' "$LOG" && wait_for . "$STATE/standup-sleep"; then
    start=$(date +%s)
    kill -TERM "$pid"
    wait "$pid"
    took=$(($(date +%s) - start))
    expect "TERM during the standup tears down" 'tore_down'
    expect "and at once, not after the standup: ${took}s" '[ "$took" -lt 30 ]'
    expect "and the standup itself is stopped" '! kill -0 "$(cat "$STATE/standup-sleep")" 2>/dev/null'
else
    bad "the standup never started"; bad "no TERM case"; bad "no TERM case"
fi

fresh tamper
base_env TAMPER=1 bash "$B/staircase.sh" run img > /dev/null 2>&1 &
pid=$!
wait_for 'waiting for the freeze' "$RUN/timeline.txt"
base_env bash "$B/staircase.sh" freeze > /dev/null 2>&1
wait "$pid"; rc=$?
expect "a prediction that moved stops the run" '[ "$rc" != 0 ] && [ "$(wc -l < "$STATE/steps")" = 1 ]'
expect "teardown after it" 'tore_down'

fresh abort
base_env STAIRCASE_FREEZE_WAIT=600 bash "$B/staircase.sh" run img > /dev/null 2>&1 &
pid=$!
wait_for 'waiting for the freeze' "$RUN/timeline.txt"
base_env bash "$B/staircase.sh" abort > /dev/null 2>&1
wait "$pid"
expect "abort tears down" 'tore_down'

echo "captures..."
(
    export STAIRCASE_LIB=1
    # shellcheck source=/dev/null
    . "$B/staircase.sh"
    cd "$WORK" || exit 1
    echo "the whole capture" > saved
    echo x > candidate
    keep_larger candidate saved
    [ "$(cat saved)" = "the whole capture" ] && [ ! -e candidate ] || exit 1
    echo "a longer capture than before" > candidate
    keep_larger candidate saved
    [ "$(cat saved)" = "a longer capture than before" ]
)
expect "a capture never shrinks the saved copy, and grows it" '[ $? = 0 ]'

echo "$checks checks, $fails failed"
if [ "$checks" != "$EXPECTED_CHECKS" ]; then
    echo "FAIL: ran $checks checks, expected $EXPECTED_CHECKS" >&2
    exit 1
fi
[ "$fails" = 0 ]
