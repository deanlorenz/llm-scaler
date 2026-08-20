#!/usr/bin/env bash
# Capture the WVA controller's own log for the duration of a run.
#
# Why this exists
# ----------------
# extract_real_trace.py's read_controller_log() is what turns analyzer-result/
# scaling-decision/k1-k2 lines into the decision table and panel 6 -- but
# nothing wrote controller.log automatically. Found the hard way: a run
# against dhl-la-1708 completed cleanly and reported zero decisions, and it
# took a manual `oc logs --since=20m` fetch after the fact (which only worked
# because the controller pod had not restarted or rotated its log since the
# run) to discover the run actually HAD real decisions the whole time. See
# docs/plans/benchmark/observability-gaps.md #2, which flagged this exact gap
# and deferred it.
#
# What this is NOT: an in-cluster follower. That doc's own analysis is that a
# client-side `oc logs -f` is "fine for a short, attended smoke test; it is
# the wrong tool for anything unattended or longer than a few minutes" --
# no reconnect on a dropped connection, no durability independent of this
# machine. This closes the "nothing captures it at all" gap now, at that
# tier; the in-cluster follower (gateway-log-follower.sh-style, watermarked,
# survives a restart) stays open for anything long-running or unattended.
#
# Usage:
#   capture_wva_controller_log.sh start <namespace> <outfile> [deployment]
#   capture_wva_controller_log.sh stop  <outfile>
#
# Writes plain text to <outfile>, matching read_controller_log()'s input
# format exactly (the same lines `oc logs` prints), and <outfile>.stderr for
# the capture process's own errors (connection drops, RBAC, etc.) -- kept
# separate so a stderr line can never be mistaken for a controller log line.
set -u

CMD="${1:?usage: $0 start <namespace> <outfile> [deployment] | stop <outfile>}"
KUBECTL="${KUBECTL_CMD:-kubectl}"

case "$CMD" in
  start)
    NS="${2:?namespace required}"; OUT="${3:?outfile required}"
    DEPLOY="${4:-wva-controller-manager}"
    mkdir -p "$(dirname "$OUT")"

    if ! $KUBECTL --namespace "$NS" get deploy "$DEPLOY" >/dev/null 2>&1; then
        echo "capture_wva_controller_log: deploy/$DEPLOY not found in $NS -- pass it as arg 4" >&2
        exit 1
    fi

    # --since=1s: only lines from here on. The controller has been running for
    # days by the time a benchmark starts; without this the capture opens with
    # its entire history and read_controller_log() would parse cycles that
    # have nothing to do with this run.
    $KUBECTL --namespace "$NS" logs "deploy/$DEPLOY" -f --since=1s \
        >"$OUT" 2>"$OUT.stderr" &
    echo $! > "$OUT.pid"
    echo "wva controller-log capture started (pid $(cat "$OUT.pid")) -> $OUT"
    ;;
  stop)
    OUT="${2:?outfile required}"
    if [ -f "$OUT.pid" ]; then
        kill "$(cat "$OUT.pid")" 2>/dev/null || true
        rm -f "$OUT.pid"
    fi
    n=0
    [ -f "$OUT" ] && n=$(wc -l < "$OUT")
    echo "wva controller-log capture stopped: $n line(s) -> $OUT"
    ;;
  *)
    echo "unknown command: $CMD" >&2; exit 2 ;;
esac
