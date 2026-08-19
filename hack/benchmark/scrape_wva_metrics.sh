#!/usr/bin/env bash
# Scrape the WVA controller's own /metrics endpoint for the duration of a run.
#
# Why this exists
# ----------------
# extract_real_trace.py's scan_raw() only reads metrics/raw/<pod>_<epoch>_metrics.log
# files, which the harness populates from the vLLM and EPP pods it already knows
# about -- never from the WVA controller itself. That means wva_desired_replicas,
# wva_current_replicas, wva_saturation_utilization, wva_kv_cache_tokens_used/capacity,
# wva_spare_capacity, wva_required_capacity, wva_errors_total, and everything else the
# controller emits about its OWN decisions was never collected at all. In particular,
# wva_desired_replicas/wva_current_replicas are the same replica-actuation signal that
# replica_status_timeseries.json is supposed to carry, straight from the controller's
# own gauge, on the same scrape cadence as every other raw file here -- collecting them
# closes the gap sample_replicas.sh's label predicate left (see that script's own fix).
#
# The controller's metrics port is authenticated HTTPS (controller-runtime's secure
# metrics server, --metrics-secure=true) -- it is not a plain scrape target, so it needs
# its own collector rather than reusing whatever pulls the vLLM/EPP pods.
#
# What this is NOT: this does not add or change anything the controller emits. It only
# adds an operator-side scrape of metrics that already exist. Changing what the
# controller instruments is out of scope for this port (see AGENTS.md and the mission
# framing this was written under) -- gaps in the metric set itself belong in an issue,
# not a patch to internal/metrics/metrics.go from here.
#
# Usage:
#   scrape_wva_metrics.sh start <namespace> <out-dir> [service] [local-port]
#   scrape_wva_metrics.sh stop  <out-dir>
#
# Writes metrics/raw/wva-controller_<epoch>_metrics.log files in the SAME naming
# convention scan_raw() already parses (<pod-or-label>_<epoch>_metrics.log), so
# extract_real_trace.py's WVA_RE recognizes them as the controller's own scrape
# rather than a vLLM/EPP pod's.
set -u

CMD="${1:?usage: $0 start <namespace> <out-dir> [service] [local-port] | stop <out-dir>}"
KUBECTL="${KUBECTL_CMD:-kubectl}"
INTERVAL="${WVA_SCRAPE_INTERVAL:-15}"

case "$CMD" in
  start)
    NS="${2:?namespace required}"; OUT_DIR="${3:?out-dir required}"
    SVC="${4:-wva-controller-manager-metrics-service}"
    LOCAL_PORT="${5:-18443}"
    mkdir -p "$OUT_DIR"

    # Fail fast, not 15s into a silent empty-output loop, if the service does not
    # exist under this name (namePrefix/overlay naming can vary by install method).
    if ! $KUBECTL --namespace "$NS" get svc "$SVC" >/dev/null 2>&1; then
        echo "scrape_wva_metrics: svc/$SVC not found in $NS -- pass it as arg 4" >&2
        exit 1
    fi

    $KUBECTL --namespace "$NS" port-forward "svc/$SVC" "$LOCAL_PORT:8443" \
        >"$OUT_DIR/.wva_scrape_portforward.log" 2>&1 &
    pf_pid=$!
    echo "$pf_pid" > "$OUT_DIR/.wva_scrape_portforward.pid"

    # Give the forward a moment before the first scrape, and confirm it came up --
    # a dead port-forward otherwise fails every single scrape silently for the
    # whole run and is only noticed afterward.
    for _ in 1 2 3 4 5; do
        grep -q "Forwarding from" "$OUT_DIR/.wva_scrape_portforward.log" 2>/dev/null && break
        sleep 1
    done
    if ! grep -q "Forwarding from" "$OUT_DIR/.wva_scrape_portforward.log" 2>/dev/null; then
        echo "scrape_wva_metrics: port-forward to svc/$SVC did not come up:" >&2
        cat "$OUT_DIR/.wva_scrape_portforward.log" >&2
        kill "$pf_pid" 2>/dev/null || true
        exit 1
    fi

    (
      while :; do
        # Re-read the token every iteration rather than once at start: a run can
        # outlive a short-lived token, and a stale one fails every scrape after
        # expiry with no visible symptom until this file is inspected.
        token=$($KUBECTL config view --raw --minify \
            -o jsonpath='{.users[0].user.token}' 2>/dev/null)
        if [ -z "$token" ]; then
            # oc/kube login token flows (SA token, oidc) don't always populate
            # .user.token directly -- fall back to the CLI's own token command.
            token=$($KUBECTL whoami -t 2>/dev/null || true)
        fi
        epoch=$(date +%s)
        if [ -n "$token" ]; then
            curl -ks --max-time 10 -H "Authorization: Bearer $token" \
                "https://127.0.0.1:$LOCAL_PORT/metrics" \
                -o "$OUT_DIR/wva-controller_${epoch}_metrics.log" \
                || echo "scrape failed at $epoch" >> "$OUT_DIR/.wva_scrape_errors.log"
        else
            echo "no token available at $epoch" >> "$OUT_DIR/.wva_scrape_errors.log"
        fi
        sleep "$INTERVAL"
      done
    ) &
    echo $! > "$OUT_DIR/.wva_scrape_loop.pid"
    echo "wva metrics scraper started (loop pid $(cat "$OUT_DIR/.wva_scrape_loop.pid"), port-forward pid $pf_pid, every ${INTERVAL}s) -> $OUT_DIR"
    ;;
  stop)
    OUT_DIR="${2:?out-dir required}"
    for pidfile in "$OUT_DIR/.wva_scrape_loop.pid" "$OUT_DIR/.wva_scrape_portforward.pid"; do
      if [ -f "$pidfile" ]; then
        kill "$(cat "$pidfile")" 2>/dev/null || true
        rm -f "$pidfile"
      fi
    done
    n=$(ls "$OUT_DIR"/wva-controller_*_metrics.log 2>/dev/null | wc -l | tr -d ' ')
    errs=0
    [ -f "$OUT_DIR/.wva_scrape_errors.log" ] && errs=$(wc -l < "$OUT_DIR/.wva_scrape_errors.log")
    echo "wva metrics scraper stopped: $n scrape(s) written, $errs error(s) -> $OUT_DIR"
    ;;
  *)
    echo "unknown command: $CMD" >&2; exit 2 ;;
esac
