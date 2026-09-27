#!/usr/bin/env bash
# Continuously capture the WVA controller's own logs for the duration of a
# benchmark run, into a plain file this process controls end to end.
#
# The controller pod's log buffer is bounded by kubelet's fixed per-container
# rotation size (a small, fixed cap unrelated to how much disk the node
# actually has), not by how long the run takes. Any run longer than a few
# minutes -- worse now that k1/k2 decision logging adds real per-cycle
# volume on top of KEDA's own polling chatter -- rotates the very lines
# dump_k2_decisions.py needs, out from under it, often before the run even
# finishes. Tailing continuously from the start and writing to a plain file
# on local disk sidesteps that: once a line has left the pod, the
# container's own rotation no longer matters.
#
# Usage:
#   tail_wva_logs.sh [--context <ctx>] [--force] start <namespace> <outfile>
#   tail_wva_logs.sh [--context <ctx>] stop <namespace> <outfile>
#
# `kubectl logs -f` does not survive the run: the apiserver's load balancer
# resets long-lived streaming connections on an idle-ish timeout regardless of
# how much log volume is flowing (measured on pokprod001: "connection reset by
# peer" at ~15 minutes into an 83-minute run, mid-stream, pod itself never
# restarted). A bare `-f` call that drops silently at that point undoes the
# whole reason this script exists -- 69 minutes of the run's k1/k2 decisions
# were never captured anywhere, kubelet's own rotation having already reclaimed
# the pod's in-container copy. So `start` runs the tail in a reconnect loop:
# on any exit, resume from the last line's own timestamp via --since-time
# rather than from "now", so a drop loses zero lines (just re-fetches the
# handful spanning the reconnect, deduplicated by `stop`).
set -u
# --help prints this file's header comment -- the documentation the script
# already carries, so it cannot drift from what the script does. Placed before
# any argument handling because the commands take a namespace as $1, and without
# it `--help` would be consumed as one.
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

CMD="${1:?usage: $0 [--context <ctx>] [--force] start <namespace> <outfile> | stop <namespace> <outfile>}"

case "$CMD" in
  start)
    NS="${2:?namespace required}"; OUT="${3:?outfile required}"
    shift 3 || true
    # A stray argument is almost always a flag in the wrong place, and a dropped
    # --context means watching the wrong cluster while believing otherwise.
    [ "$#" -eq 0 ] || { echo "unexpected argument: $1" >&2; exit 2; }
    # Ownership first: "another process owns this" is both more specific and
    # more actionable than "there are bytes here", and for the log tail the
    # output is legitimately empty for the first seconds of every capture, so
    # byte count cannot be what protects it. A refusal after the claim has to
    # give the claim back, or refusing would leave a lock nobody holds.
    capture_claim "$OUT" "$NS" || exit 1
    capture_guard_data "$OUT" || { capture_release "$OUT"; exit 1; }
    capture_verify_writable "$OUT" || { capture_release "$OUT"; exit 1; }
    : > "$OUT"
    rm -f "$OUT.stop"
    # No --prefix: dump_k2_decisions.py's LOG_LINE regex expects the
    # timestamp at column 0. A single-replica controller means this is
    # unambiguous without one; the rare exception (a brief moment where two
    # pods match during a rollout) isn't worth breaking the parser for.
    (
      while [ ! -f "$OUT.stop" ]; do
        since_ts="$(tail -n1 "$OUT" 2>/dev/null | cut -f1)"
        if [ -n "$since_ts" ]; then
          capture_kube logs -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler \
            -f --since-time="$since_ts" --tail=-1 --max-log-requests=10 \
            >> "$OUT" 2>> "$OUT.stderr"
        else
          capture_kube logs -n "$NS" -l app.kubernetes.io/name=workload-variant-autoscaler \
            -f --since=1s --tail=-1 --max-log-requests=10 \
            >> "$OUT" 2>> "$OUT.stderr"
        fi
        sleep 1
      done
    ) &
    tail_pid=$!
    if ! echo "$tail_pid" > "$OUT.pid" || ! capture_record_owner "$OUT" "$NS" "$tail_pid"; then
        echo "cannot record the capture at $OUT -- stopping it rather than running unowned" >&2
        kill "$tail_pid" 2>/dev/null || true
        capture_release "$OUT"
        exit 1
    fi
    echo "WVA log tail started (pid $tail_pid, context ${CAPTURE_CTX:-inherited}," \
         "namespace $NS) -> $OUT"
    ;;
  stop)
    # The two helpers took their stop arguments in opposite orders, and getting
    # them backwards used to exit 0 while leaving the capture running. Recognise
    # the path instead of trusting the position.
    resolved="$(capture_resolve_outfile "${3:-}" "${2:?namespace required}")"
    OUT="${resolved%%|*}"; NS="${resolved#*|}"
    if ! capture_owned "$OUT"; then
      echo "no capture owns $OUT -- nothing to stop" >&2
      exit 0
    fi
    capture_check_owner "$OUT" "$NS" "$CAPTURE_CTX"
    touch "$OUT.stop"
    # The reconnect loop re-execs kubectl, so the children matter as much as
    # the recorded pid -- but they are OUR children, found by parent, never by
    # matching a command line. The pattern sweep this replaces killed any other
    # session capturing the same namespace, including one on another cluster.
    capture_kill_tree "$OUT.pid"
    rm -f "$OUT.stop"
    # --since-time reconnects overlap by design (see start); collapse the
    # handful of re-fetched duplicate lines per reconnect back to one each.
    if [ -f "$OUT" ]; then
      awk '!seen[$0]++' "$OUT" > "$OUT.dedup" && mv "$OUT.dedup" "$OUT"
    fi
    n=$(wc -l < "$OUT" 2>/dev/null || echo 0)
    capture_release "$OUT"
    echo "WVA log tail stopped: $n line(s) -> $OUT"
    ;;
  *)
    echo "unknown command: $CMD" >&2; exit 2 ;;
esac
