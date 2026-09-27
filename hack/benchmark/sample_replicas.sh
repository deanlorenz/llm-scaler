#!/usr/bin/env bash
# Sample serving replica counts for the duration of a benchmark run.
#
# The harness already writes metrics/processed/replica_status_timeseries.json,
# and on every FMA run measured it came back with snapshots but no controllers:
# collect_metrics.sh filters them with
#
#     model_filter="${LLMDBENCH_HARNESS_STACK_NAME:-}"
#     if model_filter and model != model_filter: continue
#
# comparing a STACK name against the llm-d.ai/model LABEL. Ours are
# "inference-scheduling-wva" and "qwen-qwe-...", which never match, so every
# controller is dropped. The variable cannot be overridden from here either:
# run_only.sh writes it into the harness pod spec from endpoint_stack_name,
# which is also used as --stack, so it cannot simply be set to the model.
#
# Rather than depend on that being fixed upstream, sample it ourselves. The
# result is the same shape the harness produces, so postprocess reads it with
# the same code.
#
# Usage:
#   sample_replicas.sh [--context <ctx>] [--force] start <namespace> <outfile>
#   sample_replicas.sh stop <outfile> [<namespace>]
#
# The namespace on stop is optional and only used to warn when it does not
# match the one the capture was started with.
set -u
# --help prints this file's header comment -- the documentation the script
# already carries, so it cannot drift from what the script does. Placed before
# any argument handling because several of these take a namespace as $1, and
# without it `--help` was consumed as one.
case "${1:-}" in
    -h|--help)
        sed -n '2,/^[^#]/p' "$0" | sed 's/^# \{0,1\}//; $d'
        exit 0
        ;;
esac

# Ownership, argument parsing and the kubectl wrapper are shared with the other
# capture helper. They used to be duplicated per script and had begun to drift.
# shellcheck source=hack/benchmark/capture_lib.sh
. "$(dirname "$0")/capture_lib.sh"

capture_parse_args "$@" || exit 2
set -- ${CAPTURE_POSITIONAL[@]+"${CAPTURE_POSITIONAL[@]}"}

CMD="${1:?usage: $0 [--context <ctx>] [--force] start <namespace> <outfile> | stop <outfile> [<namespace>]}"
INTERVAL="${REPLICA_SAMPLE_INTERVAL:-10}"

_snapshot() {
    local ns="$1"
    capture_kube --namespace "$ns" get deployments,statefulsets -o json 2>/dev/null \
      | python3 -c '
import json, sys
from datetime import datetime, timezone
try:
    data = json.load(sys.stdin)
except Exception:
    data = {"items": []}
snap = {"timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "controllers": []}
for item in data.get("items", []):
    tmpl = item.get("spec", {}).get("template", {}).get("metadata", {}).get("labels", {})
    # Same predicate as the harness, WITHOUT the model filter that empties it:
    # a serving pod template, or the FMA requester.
    if tmpl.get("llm-d.ai/inferenceServing") != "true" and tmpl.get("llm-d.ai/role") != "requester":
        continue
    st = item.get("status", {})
    snap["controllers"].append({
        "name": item.get("metadata", {}).get("name", ""),
        "kind": item.get("kind", "Deployment"),
        "desired_replicas": item.get("spec", {}).get("replicas", 0),
        "ready_replicas": st.get("readyReplicas", 0) or 0,
        "available_replicas": st.get("availableReplicas", 0) or 0,
    })
print(json.dumps(snap))
'
}

# Per-pod timings, so a run can say whether FMA WOKE a sleeping instance or
# rebuilt one. That distinction is invisible in replica counts -- both look like
# "a replica arrived" -- and it is the difference between 3s and ~50-80s. We
# only found it by reading controller logs by hand; measuring it makes it a
# number in the results table instead.
#
# Emitted as JSON lines and deduped at stop, because pods vanish on scale-down:
# a single collection at the end would miss exactly the replicas we care about.
_pods_snapshot() {
    local ns="$1"
    capture_kube --namespace "$ns" get pods -o json 2>/dev/null \
      | python3 -c '
import json, sys
try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

# Launcher creation times, so we can tell a pre-existing launcher (which COULD
# have been woken) from one built for this bind (which certainly was not).
launchers = {}
for p in data.get("items", []):
    lb = (p["metadata"].get("labels") or {})
    if lb.get("app.kubernetes.io/component") == "launcher":
        launchers[p["metadata"]["name"]] = p["metadata"].get("creationTimestamp")

for p in data.get("items", []):
    m = p["metadata"]
    lb = m.get("labels") or {}
    # Launchers also carry an inference-serving label, but they are not scale
    # -target replicas -- they are the pool a replica binds INTO. Counting them
    # here would mix the thing being measured with the thing it waits for.
    if lb.get("app.kubernetes.io/component") == "launcher":
        continue
    serving = lb.get("llm-d.ai/inference-serving") == "true" or lb.get("llm-d.ai/inferenceServing") == "true"
    requester = lb.get("llm-d.ai/role") == "requester" or lb.get("app") == "dp-app"
    if not (serving or requester):
        continue
    ready = None
    for c in p.get("status", {}).get("conditions", []):
        if c.get("type") == "Ready" and c.get("status") == "True":
            ready = c.get("lastTransitionTime")
    dual = lb.get("dual-pods.llm-d.ai/dual")
    print(json.dumps({
        "name": m.get("name"),
        "node": p.get("spec", {}).get("nodeName"),
        "created": m.get("creationTimestamp"),
        "ready_at": ready,
        "bound_launcher": dual,
        "launcher_created": launchers.get(dual) if dual else None,
        "is_requester": bool(requester),
    }))
'
}

case "$CMD" in
  start)
    NS="${2:?namespace required}"; OUT="${3:?outfile required}"
    shift 3 || true
    # A stray argument here is almost always a flag in the wrong place, and a
    # dropped --context means watching the wrong cluster while believing
    # otherwise. Refuse rather than proceed.
    [ "$#" -eq 0 ] || { echo "unexpected argument: $1" >&2; exit 2; }
    # Ownership first: "another process owns this" is both more specific and more
    # actionable than "there are bytes here", and the log tail's output is
    # legitimately empty for the first seconds of every capture, so byte count
    # cannot be what protects it. A refusal after the claim gives the claim back.
    capture_claim "$OUT" "$NS" || exit 1
    capture_guard_data "$OUT" || { capture_release "$OUT"; exit 1; }
    capture_verify_writable "$OUT" || { capture_release "$OUT"; exit 1; }
    printf '{"snapshots":[' > "$OUT"
    : > "$OUT.pods.jsonl"
    # A run that records no pod timings must not file the PREVIOUS run's: the
    # write at stop is best-effort, so a leftover would be picked up as this
    # run's measurement.
    rm -f "$OUT.pod_timings.json"
    # Job control, so the background capture leads its own process group and one
    # signal at stop reaches every descendant. Without it the sampler's kubectl
    # and python -- grandchildren, because they run inside a command substitution
    # -- outlived the stop and wrote BrokenPipeError into the log after the
    # success line.
    set -m
    (
      first=1
      while :; do
        snap=$(_snapshot "$NS")
        if [ -n "$snap" ]; then
          [ $first -eq 1 ] || printf ',' >> "$OUT"
          printf '%s' "$snap" >> "$OUT"
          first=0
        fi
        _pods_snapshot "$NS" >> "$OUT.pods.jsonl" 2>/dev/null || true
        sleep "$INTERVAL"
      done
    ) &
    sampler_pid=$!
    set +m
    if ! echo "$sampler_pid" > "$OUT.pid" || ! capture_adopt "$OUT" "$NS" "$sampler_pid"; then
        echo "cannot record the capture at $OUT -- stopping it rather than running unowned" >&2
        kill "$sampler_pid" 2>/dev/null || true
        capture_release "$OUT"
        exit 1
    fi
    echo "replica sampler started (pid $sampler_pid, every ${INTERVAL}s," \
         "context ${CAPTURE_CTX:-inherited}, namespace $NS) -> $OUT"
    ;;
  stop)
    # The two helpers took their stop arguments in opposite orders, and getting
    # them backwards used to exit 0 while leaving the capture running. Recognise
    # the path instead of trusting the position.
    : "${2:?outfile required}"
    # A LONE argument that owns no record and looks nothing like a path is almost
    # certainly a namespace given in the other script's order. Accepting it as the
    # outfile exited 0, said "nothing to stop", and left the sampler running.
    if [ "$#" -lt 3 ] && ! capture_owned "$2"; then
        case "$2" in
            */*) : ;;
            *) echo "no capture owns \"$2\", and it does not look like an output path" >&2
               echo "  usage: $0 stop <outfile> [<namespace>]" >&2
               exit 2 ;;
        esac
    fi
    resolved="$(capture_resolve_outfile "$2" "${3:-}")"
    OUT="${resolved%%|*}"; NS_ARG="${resolved#*|}"
    if ! capture_owned "$OUT"; then
      echo "no capture owns $OUT -- nothing to stop, and nothing written" >&2
      exit 0
    fi
    capture_check_owner "$OUT" "$NS_ARG" "$CAPTURE_CTX"
    # If the capture would not die, stop here. Writing the JSON terminator and
    # dropping the record would leave a live capture invisible to the tooling,
    # still appending to a file that already looks finished.
    capture_stop "$OUT" "$OUT.pid" || exit 1
    # Close the array even if no snapshot was written, so the file is always
    # valid JSON. An empty snapshots list reads as "not measured" downstream,
    # which is the honest answer -- unlike a zero replica count.
    #
    # Written once, because stop releases the claim below and a second stop
    # returns before reaching here. Sniffing the last two bytes instead does NOT
    # work: a snapshot ends with `]}` as well -- {"timestamp":...,
    # "controllers":[...]} -- so a closed array and an open one ending in a
    # snapshot are indistinguishable, and the terminator was never written at
    # all, leaving a file that did not parse.
    printf ']}' >> "$OUT"
    # Dedupe the pod observations into one record per pod. Keep the observation
    # that has a Ready time: a pod is seen several times, and only later samples
    # carry the transition we want.
    # Keyed to this output, not a shared name: two runs in different
    # namespaces both wrote /tmp/wva_pod_timings.json and the second won.
    TIMINGS="$OUT.pod_timings.json"
    python3 - "$OUT.pods.jsonl" "$TIMINGS" <<'PY' 2>/dev/null || true
import json, sys
src, dst = sys.argv[1], sys.argv[2]
best = {}
try:
    for line in open(src, encoding="utf-8"):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except Exception:
            continue
        n = r.get("name")
        if not n:
            continue
        prev = best.get(n)
        # Prefer a record that knows when the pod became Ready, and one that
        # knows which launcher it bound to -- both appear only after the fact.
        if (prev is None
                or (r.get("ready_at") and not prev.get("ready_at"))
                or (r.get("bound_launcher") and not prev.get("bound_launcher"))):
            best[n] = r
except FileNotFoundError:
    pass
json.dump({"pods": list(best.values())}, open(dst, "w", encoding="utf-8"))
print("  pod timings: %d pod(s) -> %s" % (len(best), dst))
PY
    rm -f "$OUT.pods.jsonl"
    n=$(python3 -c "
import json,sys
try:
    d=json.load(open('$OUT'))
    s=d.get('snapshots',[])
    c=sum(len(x.get('controllers',[])) for x in s)
    print(f'{len(s)} snapshot(s), {c} controller sample(s)')
except Exception as e:
    print('unreadable:', e)
" 2>/dev/null)
    capture_finish "$OUT"
    echo "replica sampler stopped: $n -> $OUT"
    ;;
  *)
    echo "unknown command: $CMD" >&2; exit 2 ;;
esac
