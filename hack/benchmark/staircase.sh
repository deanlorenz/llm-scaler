#!/usr/bin/env bash
#
# staircase.sh -- the TTFT staircase, end to end, on a fixed P/D fleet.
#
#   staircase.sh run <image>   stand up, warm up, wait for the freeze, run every phase
#   staircase.sh freeze        the prediction and the ladder, from the warm-up's recording
#   staircase.sh analyze       one analysis.jsonl per finished phase, then report.md
#   staircase.sh teardown      free everything, from any state
#   staircase.sh status        what the run has done, and what holds GPUs
#   staircase.sh abort         stop a run: its own exit path tears down
#
# Set BENCHMARK_NAMESPACE (a fresh one: run refuses one that exists), STAIRCASE_RUN
# (the run's directory: run refuses one a run has used) and BENCHMARK_PROMETHEUS_URL.
# Optional: STAIRCASE_MODEL, STAIRCASE_ACCELERATOR_KEY and _VALUE (a node label the engines
# select), STAIRCASE_DECODE_CPU and _MEMORY, STAIRCASE_MAX_MODEL_LEN, STAIRCASE_LOAD_IMAGE,
# STAIRCASE_POD_HEADER, STAIRCASE_SLO_TTFT.
#
# THE SAFETY THIS CARRIES, each paid for on a shared GPU cluster:
#   - An in-cluster Job deletes the workloads when the lease lapses, so a dead runner
#     frees the GPUs within minutes (staircase_deadline.py). Armed before the standup.
#   - One EXIT trap tears down on every exit path. It runs from a private copy of this
#     directory, so an edit or a branch switch under a long run changes nothing.
#   - A saved capture is replaced only by a larger one (keep_larger).
#   - No step runs unless the ladder and the frozen prediction match the hashes taken
#     when they were written (verify_frozen).
#   - Engine errors are read from the logs: a wrapper shell outlives a dead engine, and
#     the pod stays Running.

set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${STAIRCASE_ROOT:-$(cd "$HERE/../.." && pwd)}"
PY="$(command -v python3 || command -v python)" || { echo "python3 not found" >&2; exit 1; }

NS="${BENCHMARK_NAMESPACE:-}"
R="${STAIRCASE_RUN:-}"
MODEL="${STAIRCASE_MODEL:-Qwen/Qwen3-0.6B}"
SPEC="${STAIRCASE_SPEC:-$HERE/staircase_spec.yaml}"
LOAD_IMAGE="${STAIRCASE_LOAD_IMAGE:-ghcr.io/llm-d/llm-d-benchmark:v0.7.8}"
PROM="${BENCHMARK_PROMETHEUS_URL:-}"
SLO_TTFT="${STAIRCASE_SLO_TTFT:-200ms}"
SCENARIO="${BENCHMARK_SPEC:-guides/pd-disaggregation}"
# The longest held-out prompt, its spread and the answer must fit.
MAX_MODEL_LEN="${STAIRCASE_MAX_MODEL_LEN:-40960}"
WHATIF="${STAIRCASE_WHATIF:-$ROOT/bin/whatif}"
# Seconds: the hard cap on a whole run, the lease a beat of the runner extends, the
# wait for a freeze, a fleet change, a poll.
HARD_CAP="${STAIRCASE_HARD_CAP:-345600}"
LEASE="${STAIRCASE_LEASE:-2400}"
FREEZE_WAIT="${STAIRCASE_FREEZE_WAIT:-9000}"
READY_WAIT="${STAIRCASE_READY_WAIT:-1200}"
POLL="${STAIRCASE_POLL:-20}"
ENGERR='hit an exception|Engine core initialization failed|ZeroDivisionError|Traceback'
RECORDINGS=/var/lib/wva/learner-recordings

die() { echo "staircase: $*" >&2; exit 1; }
need() {
    [ -n "$NS" ] && [ -n "$R" ] || die "set BENCHMARK_NAMESPACE and STAIRCASE_RUN"
    case "$R" in /*) ;; *) die "STAIRCASE_RUN must be absolute: $R" ;; esac
}
t() { echo "$(date -u +%FT%TZ) $*" >> "$R/timeline.txt"; }

# Every get is capped: an API stall fails with a reason, not a hang.
kube() { command kubectl --request-timeout=90s -n "$NS" "$@"; }

# keep_larger <new> <saved> : a capture may never shrink the stored copy. A dead pod's
# NotFound once overwrote 400 lines of failure context.
keep_larger() {
    local new old
    new=$(wc -c < "$1" 2>/dev/null || echo 0)
    old=$(wc -c < "$2" 2>/dev/null || echo 0)
    if [ "$new" -gt "$old" ]; then mv "$1" "$2"; else rm -f "$1"; fi
}

# running_pods [regex] : the Running pods, none being deleted.
running_pods() {
    kube get pods -o json 2>/dev/null | jq -r '.items[] | select(.metadata.deletionTimestamp == null)
        | select(.status.phase=="Running") | .metadata.name' | grep -E -- "${1:-.}"
}

names() { [ -s "$R/names.sh" ] && . "$R/names.sh"; }

# --- the deadline, before anything else exists ---------------------------------------
arm_deadline() {
    local now hard lease
    now=$(date +%s); hard=$((now + HARD_CAP)); lease=$((now + 5400))
    echo "$hard" > "$R/deadline-epoch.txt"
    kube create configmap staircase-deadline --from-file=staircase_deadline.py="$HERE/staircase_deadline.py" || return 1
    kube create configmap staircase-lease --from-literal=until="$lease" || return 1
    sed -e "s/__DEADLINE_EPOCH__/$hard/" -e "s/__NS__/$NS/" -e "s#__IMAGE__#$LOAD_IMAGE#" \
        "$HERE/staircase_deadline.yaml" > "$R/deadline.yaml"
    if grep -q '__[A-Z_]*__' "$R/deadline.yaml"; then t "deadline.yaml has an unfilled placeholder"; return 1; fi
    kube apply -f "$R/deadline.yaml" || return 1
    local i
    for i in $(seq 1 40); do
        kube logs job/staircase-deadline > "$R/deadline-start.log" 2>/dev/null
        if grep -q 'deadline at' "$R/deadline-start.log"; then t "deadline armed: $(cat "$R/deadline-start.log")"; return 0; fi
        sleep "${STAIRCASE_ARM_POLL:-6}"
    done
    t "deadline job not running"
    return 1
}

renew_lease() {
    kube patch cm staircase-lease --type merge -p "{\"data\":{\"until\":\"$(($(date +%s) + LEASE))\"}}" >/dev/null 2>"$R/lease.err" \
        || echo "$(date -u +%FT%TZ) lease patch failed: $(head -1 "$R/lease.err")" >> "$R/lease-failures.log"
}

# --- standup ---------------------------------------------------------------------------
standup() {
    local set="prefill.replicas=2,decode.replicas=2,model.maxModelLen=$MAX_MODEL_LEN"
    if [ -n "${STAIRCASE_ACCELERATOR_KEY:-}" ]; then
        set="$set,decode.acceleratorType.labelKey=$STAIRCASE_ACCELERATOR_KEY,decode.acceleratorType.labelValue=${STAIRCASE_ACCELERATOR_VALUE:-}"
        set="$set,prefill.acceleratorType.labelKey=$STAIRCASE_ACCELERATOR_KEY,prefill.acceleratorType.labelValue=${STAIRCASE_ACCELERATOR_VALUE:-}"
    fi
    t "standup begins"
    # In the background, and exec: a trapped signal waits for a foreground child, and
    # CHILD must be what kill reaches. timeout passes the TERM on to make.
    (cd "$ROOT" && LLMDBENCH_SET="$set" exec timeout --signal=TERM 2700 make --no-print-directory benchmark-standup \
        BENCHMARK_NAMESPACE="$NS" BENCHMARK_SPEC="$SCENARIO" MODEL_ID="$MODEL" IMG="$1" \
        BENCHMARK_PROMETHEUS_URL="$PROM" BENCHMARK_DECODE_REPLICAS=2 \
        BENCHMARK_DECODE_CPU="${STAIRCASE_DECODE_CPU:-8}" BENCHMARK_DECODE_MEMORY="${STAIRCASE_DECODE_MEMORY:-32Gi}" \
        BENCHMARK_KEDA_MIN_REPLICAS=2 BENCHMARK_KEDA_MAX_REPLICAS=2) > "$R/standup.log" 2>&1 &
    CHILD=$!
    wait "$CHILD"
}

# prepare : read the names, keep the originals, list slo, flow control off.
prepare() {
    local dec pre epp cm router
    dec=$(kube get deploy -o name | sed 's#.*/##' | grep -- '-decode$')
    pre=$(kube get deploy -o name | sed 's#.*/##' | grep -- '-prefill$')
    epp=$(kube get deploy -o name | sed 's#.*/##' | grep -- '-epp$')
    if [ "$(echo "$dec" | grep -c .)" != 1 ] || [ "$(echo "$pre" | grep -c .)" != 1 ] || [ "$(echo "$epp" | grep -c .)" != 1 ]; then
        t "names: decode='$dec' prefill='$pre' epp='$epp'; want one each"; return 1
    fi
    cm=$(kube get cm -o json | jq -r '.items[] | select(.data["pd-config.yaml"] != null) | .metadata.name')
    [ "$(echo "$cm" | grep -c .)" = 1 ] || { t "EPP ConfigMap with pd-config.yaml: '$cm'"; return 1; }
    router=$(kube get svc -o json | jq -r --arg e "$epp" '.items[] | select(.metadata.name == $e) | select([.spec.ports[] | .port] | index(80)) | .metadata.name')
    [ -n "$router" ] || { t "no Service $epp with port 80"; return 1; }
    {
        echo "DEC=$dec"; echo "PRE=$pre"; echo "EPPD=$epp"; echo "EPPCM=$cm"
        echo "ROUTER=http://$router.$NS.svc:80"
    } > "$R/names.sh"
    t "names: $(tr '\n' ' ' < "$R/names.sh")"
    local d len
    for d in "$dec" "$pre"; do
        len=$(kube get deploy "$d" -o json | jq -r '.spec.template.spec.containers[] | select(.name=="vllm") | .env[] | select(.name=="VLLM_MAX_MODEL_LEN") | .value')
        [ "$len" = "$MAX_MODEL_LEN" ] || { t "$d VLLM_MAX_MODEL_LEN is '$len', not $MAX_MODEL_LEN"; return 1; }
    done
    kube get cm "$cm" -o json | jq -r '.data["pd-config.yaml"]' > "$R/pd-config-on.yaml"
    "$PY" "$HERE/staircase_config.py" fc-off "$R/pd-config-on.yaml" "$R/pd-config-off.yaml" || { t "fc-off refused"; return 1; }
    kube get configmap wva-scaling-policy-config -o json | jq -r '.data.default' > "$R/policy-orig.yaml"
    "$PY" "$HERE/staircase_config.py" list-slo "$R/policy-orig.yaml" "$R/policy.yaml" "$SLO_TTFT" || { t "list-slo refused"; return 1; }
    jq -n --rawfile d "$R/policy.yaml" '{data: {default: $d}}' > "$R/policy-patch.json"
    kube patch configmap wva-scaling-policy-config --type merge --patch-file "$R/policy-patch.json" >/dev/null || { t "policy patch failed"; return 1; }
    t "slo listed, TTFT target $SLO_TTFT, shadow"
}

# pin_fleet <prefill> <decode> : ScaledObjects min = max, then wait for Ready, capped,
# reading the engines' logs: a crashed engine presents as a pod that never gets Ready.
pin_fleet() {
    local np=$1 nd=$2 so target want a b end p
    names
    for so in $(kube get scaledobject -o json | jq -r '.items[] | "\(.metadata.name)=\(.spec.scaleTargetRef.name)"'); do
        target=${so#*=}; so=${so%=*}
        case "$target" in
            "$DEC") want=$nd ;;
            "$PRE") want=$np ;;
            *) continue ;;
        esac
        kube patch scaledobject "$so" --type merge -p "{\"spec\":{\"minReplicaCount\":$want,\"maxReplicaCount\":$want}}" >/dev/null \
            || { t "ScaledObject $so patch failed"; return 1; }
    done
    end=$(($(date +%s) + READY_WAIT))
    while [ "$(date +%s)" -lt "$end" ]; do
        a=$(kube get deploy "$DEC" -o jsonpath='{.status.readyReplicas}')
        b=$(kube get deploy "$PRE" -o jsonpath='{.status.readyReplicas}')
        if [ "${a:-0}" = "$nd" ] && [ "${b:-0}" = "$np" ]; then t "fleet prefill=$b decode=$a"; return 0; fi
        for p in $(running_pods "^($DEC|$PRE)-"); do
            kube logs "$p" -c vllm --tail=300 2>/dev/null | grep -m1 -E "$ENGERR" | sed "s#^#$p: #" >> "$R/engine-errors.txt"
        done
        [ -s "$R/engine-errors.txt" ] && { t "engine error: $(head -3 "$R/engine-errors.txt" | tr '\n' ' ')"; return 1; }
        sleep "$POLL"
    done
    t "fleet not Ready in ${READY_WAIT}s: prefill=${b:-0} decode=${a:-0}"
    return 1
}

# flow_control on|off : set the router's config and restart it; refuse any other state.
flow_control() {
    local f="$R/pd-config-$1.yaml" got
    names
    [ -s "$f" ] || { t "flow_control: $f missing"; return 1; }
    jq -n --rawfile d "$f" '{data: {"pd-config.yaml": $d}}' > "$R/epp-patch-$1.json"
    kube patch cm "$EPPCM" --type merge --patch-file "$R/epp-patch-$1.json" >/dev/null || { t "EPP config patch failed"; return 1; }
    got=$(kube get cm "$EPPCM" -o json | jq -r '.data["pd-config.yaml"]' | grep -cE '^(featureGates|flowControl):')
    if { [ "$1" = on ] && [ "$got" != 2 ]; } || { [ "$1" = off ] && [ "$got" != 0 ]; }; then
        t "flow control $1: the config has $got flow-control keys"; return 1
    fi
    kube rollout restart "deploy/$EPPD" >/dev/null && command kubectl -n "$NS" rollout status "deploy/$EPPD" --timeout=300s >/dev/null \
        || { t "EPP restart failed"; return 1; }
    sleep "${STAIRCASE_SETTLE:-20}"
    t "flow control $1"
}

# recorder_on : the recorder, as the last change before load.
recorder_on() {
    kube patch deployment wva-controller-manager --type strategic \
        --patch-file "$ROOT/config/components/learner-recorder/learner-recorder-deployment.yaml" >/dev/null \
        || { t "recorder patch failed"; return 1; }
    command kubectl -n "$NS" rollout status deployment/wva-controller-manager --timeout=300s >/dev/null || { t "recorder rollout failed"; return 1; }
    controller_pod > "$R/pod1.txt" || return 1
    sleep "${STAIRCASE_SETTLE:-20}"
    kube exec "$(cat "$R/pod1.txt")" -c recordings -- sh -c "head -1 $RECORDINGS/learnrec-*.jsonl" > "$R/start-line.json" 2>&1
    [ "$(jq -r .Kind "$R/start-line.json" 2>/dev/null)" = start ] || { t "no recording start line: $(head -c 200 "$R/start-line.json")"; return 1; }
    t "recorder on, commit $(jq -r .Data.Commit "$R/start-line.json")"
}

# controller_pod : the one Running controller, waiting out a rollout, capped.
controller_pod() {
    local end p
    end=$(($(date +%s) + 180))
    while :; do
        p=$(kube get pod -l control-plane=controller-manager -o json | jq -r '[.items[] | select(.metadata.deletionTimestamp == null) | select(.status.phase=="Running")] | .[].metadata.name')
        [ "$(echo "$p" | grep -c .)" = 1 ] && { echo "$p"; return 0; }
        [ "$(date +%s)" -ge "$end" ] && { t "controller pods after 180 s: '$p'"; return 1; }
        sleep "$POLL"
    done
}

# copy_recordings <pod> <dir> : never shrink a saved recording.
copy_recordings() {
    local tmp="$2.tmp.$$" f
    mkdir -p "$2"; rm -rf "$tmp"
    command kubectl --request-timeout=300s cp -n "$NS" -c recordings "$1:$RECORDINGS" "$tmp" >> "$R/copyrec.log" 2>&1
    for f in "$tmp"/*; do
        [ -f "$f" ] && keep_larger "$f" "$2/$(basename "$f")"
    done
    rm -rf "$tmp"
    kube logs --timestamps "$1" -c manager > "$2.log.tmp" 2>/dev/null
    keep_larger "$2.log.tmp" "$2.log"
}

# --- witnesses -------------------------------------------------------------------------
# witness : a pod that reaches Prometheus, and how to read the EPP's metrics directly.
# Neither failing stops the run: the client is the mark's witness.
witness() {
    names
    kube create configmap staircase-witness --from-file=staircase_promq.py="$HERE/staircase_promq.py" >/dev/null || t "witness configmap failed"
    pod_yaml witness witness staircase-witness /w "sleep 86400" 100m 128Mi 512Mi > "$R/witness.yaml"
    kube apply -f "$R/witness.yaml" >> "$R/timeline.txt" 2>&1
    command kubectl -n "$NS" wait pod witness --for=condition=Ready --timeout=180s >> "$R/timeline.txt" 2>&1 || t "witness pod not Ready"
    local epod port
    epod=$(running_pods "^$EPPD-" | head -1)
    [ -n "$epod" ] || { t "no EPP pod"; return 0; }
    for port in $(kube get pod "$epod" -o json | jq -r '.spec.containers[].ports[]?.containerPort') 9090; do
        if command kubectl --request-timeout=60s get --raw "/api/v1/namespaces/$NS/pods/$epod:$port/proxy/metrics" 2>/dev/null | grep -q '^llm_d_epp_\|^# HELP llm_d_epp_'; then
            echo "$port" > "$R/epp-scrape.txt"; t "EPP metrics readable at port $port"; return 0
        fi
    done
    t "EPP metrics not readable through the API proxy: Prometheus and the client only"
}

# pod_yaml <name> <label> <configmap> <mount> <command> <cpu> <mem request> <mem limit>
pod_yaml() {
    printf '%s\n' "apiVersion: v1" "kind: Pod" "metadata:" "  name: $1" "  namespace: $NS" "  labels: {app: $2}" \
        "spec:" "  restartPolicy: Never" "  containers:" "  - name: main" "    image: $LOAD_IMAGE" \
        "    command: [sh, -c, '$5']" "    resources:" "      requests: {cpu: \"$6\", memory: $7}" \
        "      limits: {memory: $8}" "    volumeMounts:" "    - {mountPath: $4, name: code, readOnly: true}" \
        "    - {mountPath: /out, name: out}" "  volumes:" "  - name: out" "    emptyDir: {}" "  - name: code" \
        "    configMap: {name: $3}"
}

# capture_loop : poll until STOP: lease, replicas, engine metrics and errors, EPP metrics,
# controller log. Every file is append-only or replaced only by a larger copy.
capture_loop() {
    local ts p port epod now leased=0
    names
    while [ ! -e "$R/STOP" ]; do
        ts=$(date -u +%FT%TZ); now=$(date +%s)
        if [ $((now - leased)) -ge "${STAIRCASE_LEASE_EVERY:-120}" ]; then renew_lease; leased=$now; fi
        for p in "$DEC" "$PRE"; do
            echo "$ts $p $(kube get deploy "$p" -o jsonpath='{.spec.replicas} {.status.readyReplicas}' 2>/dev/null)" >> "$R/replicas.log"
        done
        for p in $(running_pods "^($DEC|$PRE)-"); do
            port=8200; case $p in "$PRE"-*) port=8000 ;; esac
            command kubectl --request-timeout=60s get --raw "/api/v1/namespaces/$NS/pods/$p:$port/proxy/metrics" 2>/dev/null \
                | grep -E '^vllm:(kv_cache_usage_perc|num_requests_waiting|num_requests_running|num_preemptions_total|prompt_tokens_total|generation_tokens_total|request_success_total|request_prefill_time_seconds_(sum|count)|request_queue_time_seconds_(sum|count)|time_to_first_token_seconds_(sum|count))' \
                | sed "s#^#$ts $p #" >> "$R/vllm-metrics.log"
            kube logs "$p" -c vllm --since=70s 2>/dev/null | grep -E "$ENGERR" | head -3 | sed "s#^#$ts $p #" >> "$R/engine-errors.txt"
        done
        if [ -s "$R/epp-scrape.txt" ]; then
            epod=$(running_pods "^$EPPD-" | head -1)
            [ -n "$epod" ] && command kubectl --request-timeout=60s get --raw "/api/v1/namespaces/$NS/pods/$epod:$(cat "$R/epp-scrape.txt")/proxy/metrics" 2>/dev/null \
                | grep -E '^llm_d_epp_(request_ttft_seconds|request_streaming_tpot_seconds|flow_control_)' \
                | sed "s#^#$ts $epod #" >> "$R/epp-metrics.log"
        fi
        sleep "$POLL"
    done
}

# --- load --------------------------------------------------------------------------------
# load <dir> <pod> <open|closed> <cap seconds> <driver args...> : one load pod to SWEEP DONE;
# log and per-request CSVs saved on every poll, so nothing depends on the pod living.
load() {
    local d=$1 p=$2 kind=$3 cap=$4 cmd end i rc=1 ph
    shift 4
    names
    mkdir -p "$d"
    case $kind in
        open)
            cmd="python3 -u /code/staircase_load.py --url $ROUTER --model $MODEL --out /out"
            [ -n "${STAIRCASE_POD_HEADER:-}" ] && cmd="$cmd --pod-header $STAIRCASE_POD_HEADER"
            ;;
        closed) cmd="python3 -u /code/closed_loop_sweep.py --url $ROUTER --model $MODEL --stream --max-inflight 1024" ;;
        *) t "load: kind $kind"; return 1 ;;
    esac
    pod_yaml "$p" loadgen staircase-loadgen /code \
        "python3 -c \"import aiohttp\" 2>/dev/null || pip install -q aiohttp; $cmd $*; sleep 7200" 8 8Gi 16Gi > "$d/load.yaml"
    kube apply -f "$d/load.yaml" >> "$R/timeline.txt" 2>&1 || { t "$p apply failed"; return 1; }
    for i in $(seq 1 60); do
        kube logs "$p" 2>/dev/null | grep -q ' LEVEL 1 ' && break
        sleep "$POLL"
    done
    if ! kube logs "$p" 2>/dev/null | grep -q ' LEVEL 1 '; then
        kube delete pod "$p" --ignore-not-found --grace-period=1 >/dev/null 2>&1
        t "$p did not start"; return 1
    fi
    end=$(($(date +%s) + cap))
    while [ "$(date +%s)" -lt "$end" ]; do
        kube logs "$p" > "$d/log.tmp" 2>/dev/null; keep_larger "$d/log.tmp" "$d/sweep.log"
        if grep -q 'SWEEP DONE' "$d/sweep.log" 2>/dev/null; then rc=0; break; fi
        ph=$(kube get pod "$p" -o jsonpath='{.status.phase}' 2>/dev/null)
        case "$ph" in Succeeded|Failed) t "$p phase $ph"; break ;; esac
        if [ -s "$R/engine-errors.txt" ]; then t "$p: engine errors"; break; fi
        if [ -e "$R/abort" ]; then t "$p: abort"; break; fi
        sleep "$POLL"
    done
    kube logs "$p" > "$d/log.tmp" 2>/dev/null; keep_larger "$d/log.tmp" "$d/sweep.log"
    if [ "$kind" = open ]; then
        command kubectl --request-timeout=120s exec -n "$NS" "$p" -- sh -c 'cat /out/w*.csv | gzip -1' > "$d/req.tmp" 2>/dev/null
        keep_larger "$d/req.tmp" "$d/requests.csv.gz"
    fi
    kube delete pod "$p" --ignore-not-found --grace-period=1 --timeout=60s >/dev/null 2>&1
    t "$p end rc=$rc: $(grep -E 'RUNAWAY|SWEEP DONE' "$d/sweep.log" 2>/dev/null | tail -2 | tr '\n' ' ' | cut -c1-240)"
    return $rc
}

# schedule_seconds <schedule> : the sum of its holds.
schedule_seconds() { echo "$1" | tr ',' '\n' | awk -F: '{s += $1} END {print s}'; }

# prom_range <from epoch> <to epoch> <out>
prom_range() {
    command kubectl --request-timeout=200s -n "$NS" exec witness -- python3 /w/staircase_promq.py "$PROM" "$NS" "$1" "$2" > "$3.tmp" 2>/dev/null
    keep_larger "$3.tmp" "$3"
}

# warmup : the recording the prediction is made from, flow control off, slo listed from
# its first cycle. The order and the load are the spec's: what each stage teaches, and why
# V_P comes last, is in the plan.
warmup() {
    local d="$R/warmup" pod from stage driver sched seed cap
    mkdir -p "$d"
    pod=$(cat "$R/pod1.txt"); from=$(date +%s)
    for stage in $(jq -r '.warmup.order[]' "$R/spec.json"); do
        driver=$(jq -r ".warmup.$stage.driver" "$R/spec.json")
        sched=$(jq -r ".warmup.$stage.schedule" "$R/spec.json")
        seed=$(jq -r ".warmup.$stage.seed // 11" "$R/spec.json")
        cap=$(( $(schedule_seconds "$sched") + 900 ))
        t "warmup $stage"
        if [ "$driver" = open ]; then
            load "$d/$stage" "w-$stage" open "$cap" --schedule "$sched" --seed "$seed" --procs 2 --request-timeout 120 || return 1
        else
            load "$d/$stage" "w-$stage" closed "$cap" --schedule "$sched" || return 1
        fi
        copy_recordings "$pod" "$R/recordings-warmup"
    done
    sleep "${STAIRCASE_SETTLE:-20}"
    copy_recordings "$pod" "$R/recordings-warmup"
    prom_range "$from" "$(date +%s)" "$d/prom.jsonl"
    t "warmup done: $(ls -l "$R/recordings-warmup" | awk 'NR>1 {print $NF"="$5}' | tr '\n' ' ')"
}

# --- the freeze ------------------------------------------------------------------------
# freeze : run once, after the warm-up and before any step. A file that exists is never
# replaced, a refused bucket or a failed gate stops here, and the hashes are taken last.
cmd_freeze() {
    need
    local rec
    [ -e "$R/ladder.ready" ] && die "already frozen: $R/ladder.ready"
    [ -d "$R/phases" ] && die "a phase has run: a prediction after a step is not a prediction"
    # shellcheck disable=SC2012 # the largest recording: the sizes hold no odd names
    rec=$(ls -S "$R"/recordings-warmup/*.jsonl 2>/dev/null | head -1)
    [ -n "$rec" ] || die "no warm-up recording under $R/recordings-warmup"
    [ -x "$WHATIF" ] || die "$WHATIF: build it with make whatif"
    "$WHATIF" freeze --recording "$rec" --spec "$R/spec.yaml" -o "$R/frozen.json" || die "whatif freeze refused"
    "$PY" "$HERE/staircase_freeze.py" ladder "$R/frozen.json" "$R/spec.json" "$R/ladder.txt" "$R/spec.yaml" || die "no ladder"
    "$PY" "$HERE/staircase_freeze.py" plan "$R/frozen.json" "$R/spec.json" | tee "$R/plan.md"
    (cd "$R" && sha256sum frozen.json ladder.txt spec.yaml > frozen.sha256) || die "hashing failed"
    touch "$R/ladder.ready"
    t "frozen: $(tr '\n' ' ' < "$R/frozen.sha256" | cut -c1-200)"
}

# verify_frozen : no step runs on a prediction that moved.
verify_frozen() {
    (cd "$R" && sha256sum --check --status frozen.sha256) || { t "the frozen files do not match their hashes"; return 1; }
}

# --- a phase -----------------------------------------------------------------------------
# phase <name> <fc> <schedule> <runaway> <seed> <prefill> <decode> : flow control and the
# fleet, a fresh controller so the phase has a recording of its own, the load, the captures.
phase() {
    local n=$1 fc=$2 sched=$3 run=$4 seed=$5 np=$6 nd=$7 d pod hold rc from to
    d="$R/phases/$n"; mkdir -p "$d"
    [ -e "$d/done" ] && { t "phase $n already done"; return 0; }
    verify_frozen || return 1
    t "phase $n start fc=$fc runaway=$run seed=$seed fleet=$np+$nd"
    pin_fleet "$np" "$nd" || return 1
    flow_control "$fc" || return 1
    kube rollout restart deployment/wva-controller-manager >/dev/null
    command kubectl -n "$NS" rollout status deployment/wva-controller-manager --timeout=300s >/dev/null || { t "controller rollout failed"; return 1; }
    pod=$(controller_pod) || return 1
    echo "$pod" > "$R/pod1.txt"
    sleep "${STAIRCASE_SETTLE:-20}"
    from=$(date +%s); echo "$from" > "$d/start.txt"
    hold=$(schedule_seconds "$sched")
    load "$d" "lg-$(echo "$n" | tr 'A-Z' 'a-z')" open $((hold + 1500)) --schedule "$sched" --seed "$seed" --procs 6 --runaway "$run" \
        --max-inflight "$(jq -r .maxInflight "$R/spec.json")" --discard "$(jq -r .discardSeconds "$R/spec.json")"
    rc=$?
    sleep "${STAIRCASE_SETTLE:-20}"
    to=$(date +%s); echo "$to" > "$d/end.txt"
    copy_recordings "$pod" "$d/rec"
    prom_range "$from" "$to" "$d/prom.jsonl"
    t "phase $n rc=$rc"
    [ "$rc" = 0 ] && touch "$d/done"
    return "$rc"
}

# --- the run --------------------------------------------------------------------------------
# finish : the one exit path. Tear down on anything that started.
finish() {
    local rc=$?
    trap - EXIT
    t "exit rc=$rc: teardown"
    touch "$R/STOP"
    [ -n "${CAP:-}" ] && kill "$CAP" 2>/dev/null
    [ -n "${CHILD:-}" ] && kill "$CHILD" 2>/dev/null
    if [ "${STARTED:-0}" = 1 ]; then
        bash "$HERE/staircase.sh" teardown > "$R/teardown.out" 2>&1
        t "teardown rc=$?: $(tail -2 "$R/teardown.out" | tr '\n' ' ' | cut -c1-240)"
    fi
    exit "$rc"
}

cmd_run() {
    need
    [ $# = 1 ] || die "usage: staircase.sh run <image>"
    [ -n "$PROM" ] || die "set BENCHMARK_PROMETHEUS_URL"
    if [ -z "${STAIRCASE_COPY:-}" ]; then
        [ -e "$R/timeline.txt" ] && die "$R/timeline.txt exists: a run has used this directory"
        command kubectl --request-timeout=60s get namespace "$NS" >/dev/null 2>&1 && die "namespace $NS exists: a fresh one is wanted"
        mkdir -p "$R/bin" || die "cannot write $R"
        cp -f "$HERE"/staircase* "$HERE/closed_loop_sweep.py" "$R/bin/" || die "cannot copy the tooling"
        cp -f "$SPEC" "$R/spec.yaml" || die "no spec at $SPEC"
        if command -v yq >/dev/null; then yq -o=json "$R/spec.yaml" > "$R/spec.json" || die "spec does not parse"
        else die "yq not found"; fi
        STAIRCASE_COPY=1 STAIRCASE_ROOT="$ROOT" STAIRCASE_WHATIF="$WHATIF" exec bash "$R/bin/staircase.sh" run "$1"
    fi
    STARTED=0; CAP=; CHILD=
    trap finish EXIT
    trap 'exit 143' INT TERM HUP
    t "run start image=$1"
    command kubectl --request-timeout=60s create namespace "$NS" >/dev/null || { t "namespace create failed"; exit 1; }
    STARTED=1
    arm_deadline || exit 1
    standup "$1" || { t "standup failed"; exit 1; }
    CHILD=
    prepare || exit 1
    pin_fleet 2 2 || exit 1
    flow_control off || exit 1
    kube create configmap staircase-loadgen --from-file=staircase_load.py="$HERE/staircase_load.py" \
        --from-file=closed_loop_sweep.py="$HERE/closed_loop_sweep.py" >/dev/null || { t "loadgen configmap failed"; exit 1; }
    recorder_on || exit 1
    witness
    capture_loop > "$R/capture.out" 2>&1 &
    CAP=$!
    warmup || exit 1
    t "waiting for the freeze (capped at ${FREEZE_WAIT}s)"
    local end=$(($(date +%s) + FREEZE_WAIT))
    while [ ! -e "$R/ladder.ready" ]; do
        [ "$(date +%s)" -ge "$end" ] && { t "no freeze in time"; exit 1; }
        [ -e "$R/abort" ] && { t "abort"; exit 1; }
        sleep "$POLL"
    done
    verify_frozen || exit 1
    local name fc sched run seed np nd
    while read -r name fc sched run seed np nd; do
        [ -n "$name" ] || continue
        [ -e "$R/abort" ] && { t "abort"; exit 1; }
        phase "$name" "$fc" "$sched" "$run" "$seed" "$np" "$nd" || t "phase $name failed (going on unless the engines did)"
        [ -s "$R/engine-errors.txt" ] && { t "engine errors: stopping"; exit 1; }
        verify_frozen || exit 1
    done < "$R/ladder.txt"
    t "all phases done"
}

# teardown : free everything, from any state; safe to re-run. Saves what is still only in
# the cluster first, then the harness, then whatever is left, and lists what remains.
cmd_teardown() {
    need
    local p gpus i n
    kube delete pod -l app=loadgen --ignore-not-found --grace-period=5 --timeout=120s 2>&1
    for p in $(kube get pod -l control-plane=controller-manager -o json 2>/dev/null | jq -r '.items[].metadata.name'); do
        if kube get pod "$p" -o jsonpath='{.spec.containers[*].name}' 2>/dev/null | grep -qw recordings; then
            copy_recordings "$p" "$R/recordings-$p"
        fi
    done
    (cd "$ROOT" && timeout 1200 make --no-print-directory benchmark-teardown BENCHMARK_NAMESPACE="$NS" BENCHMARK_SPEC="$SCENARIO") > "$R/benchmark-teardown.log" 2>&1
    echo "benchmark-teardown rc=$?"
    kube delete scaledobject --all 2>&1
    kube delete job,deploy,lws,daemonset,statefulset --all --wait=false 2>&1
    kube delete pod --all --grace-period=30 --timeout=300s 2>&1
    for i in $(seq 1 30); do
        n=$(kube get pods --no-headers 2>/dev/null | grep -c .)
        [ "$n" = 0 ] && break
        sleep "${STAIRCASE_TEARDOWN_POLL:-10}"
    done
    gpus=$(kube get pods -o custom-columns='GPU:.spec.containers[*].resources.limits.nvidia\.com/gpu' --no-headers 2>/dev/null | grep -cv '<none>')
    kube get pods > "$R/final-pods.txt" 2>&1
    echo "pods left: $(grep -c . "$R/final-pods.txt"), with a GPU: $gpus"
}

cmd_status() {
    need
    tail -12 "$R/timeline.txt" 2>/dev/null | cut -c1-200
    ls "$R/phases" 2>/dev/null | tr '\n' ' '; echo
    kube get pods -o custom-columns='NAME:.metadata.name,PHASE:.status.phase,GPU:.spec.containers[*].resources.limits.nvidia\.com/gpu' 2>&1 | head -20
}

cmd_abort() {
    need
    touch "$R/abort"
    echo "abort written: the run tears down at its next check; follow it with staircase.sh status"
}

cmd_analyze() {
    need
    local p
    for p in "$R"/phases/*/; do
        p=$(basename "$p")
        [ -s "$R/phases/$p/requests.csv.gz" ] || { echo "$p: no requests"; continue; }
        "$PY" "$HERE/staircase_analyze.py" steps "$R" "$p" --discard "$(jq -r .discardSeconds "$R/spec.json")" || die "$p: analysis failed"
    done
    "$PY" "$HERE/staircase_analyze.py" report "$R" "$R/frozen.json" "$R/spec.json" ${STAIRCASE_REDO:+--redo "$STAIRCASE_REDO"} || die "report failed"
    echo "$R/report.md"
}

# Sourced by hack/check-staircase.sh, for its functions.
if [ "${STAIRCASE_LIB:-}" = 1 ]; then return 0; fi

case "${1:-}" in
    run) shift; cmd_run "$@" ;;
    freeze) cmd_freeze ;;
    analyze) cmd_analyze ;;
    teardown) cmd_teardown ;;
    status) cmd_status ;;
    abort) cmd_abort ;;
    *) sed -n '3,12p' "${BASH_SOURCE[0]}" >&2; exit 2 ;;
esac
