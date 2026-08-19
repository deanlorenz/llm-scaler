#!/usr/bin/env bash
# gpu-reservation-coupler.sh — ported from llm-d-workload-variant-autoscaler's
# benchmark branch. See gpu-reservation.yaml's header for the pokprod-specific
# caution (idle-GPU sweep agent) before scaling this above 0 replicas here.
#
# Couples the gpu-reservation deployment to the decode deployment so a constant
# GPU footprint (decode_replicas * GPUS_PER_DECODE_REPLICA + reservation
# replicas == HOLD_TOTAL) is always held. When KEDA scales decode UP, the
# reservation is scaled DOWN by the matching GPU count within one poll --
# freeing pre-reserved GPU(s) for the new decode pod instead of racing other
# tenants. When decode scales back DOWN, the reservation is scaled back UP.
#
# Adapted from the original (which assumed 1 GPU/decode-replica): this
# namespace's decode deployment runs --tensor-parallel-size=2, i.e. 2
# GPUs/replica, so the coupled math is decode_gpus = dd * GPUS_PER_DECODE_REPLICA.
#
# Scoped strictly to deploy/gpu-reservation in dhl-la-1708. Touches nothing else.
# Stop early:  touch /tmp/stop-gpu-coupler   (or TaskStop the background task)
set -uo pipefail
NS=dhl-la-1708
DECODE=optimized-baseline-nvidia-gpu-vllm-decode
RES=gpu-reservation
# NS and DECODE stay hardcoded on purpose: they are the blast-radius guard, not
# a knob. Only the timing/footprint knobs are overridable:
#   HOLD_TOTAL=4 MAX_ITERS=900 ./gpu-reservation-coupler.sh
GPUS_PER_DECODE_REPLICA=${GPUS_PER_DECODE_REPLICA:-2}
HOLD_TOTAL=${HOLD_TOTAL:-2}
STOP=/tmp/stop-gpu-coupler
MAX_ITERS=${MAX_ITERS:-560}   # 560 x 5s ~= 47 min safety cap
POLL=${POLL:-5}

log(){ echo "$(date -u +%H:%M:%S) coupler | $*"; }
log "start: HOLD_TOTAL=$HOLD_TOTAL gpus/replica=$GPUS_PER_DECODE_REPLICA decode=$DECODE res=$RES poll=${POLL}s"
rm -f "$STOP"
for i in $(seq 1 "$MAX_ITERS"); do
  if [ -f "$STOP" ]; then log "stop sentinel present; exiting"; break; fi
  dd=$(kubectl get deploy "$DECODE" -n "$NS" -o jsonpath='{.spec.replicas}' 2>/dev/null)
  cur=$(kubectl get deploy "$RES" -n "$NS" -o jsonpath='{.spec.replicas}' 2>/dev/null)
  if [ -z "$dd" ]; then log "WARN decode replicas unreadable; skip"; sleep "$POLL"; continue; fi
  if [ -z "$cur" ]; then log "WARN reservation deploy missing; skip"; sleep "$POLL"; continue; fi
  decode_gpus=$(( dd * GPUS_PER_DECODE_REPLICA ))
  target=$(( HOLD_TOTAL - decode_gpus )); [ "$target" -lt 0 ] && target=0
  if [ "$cur" != "$target" ]; then
    log "decode=$dd (${decode_gpus} GPUs)  reservation $cur -> $target  (scaling)"
    if kubectl scale deploy/"$RES" -n "$NS" --replicas="$target" >/dev/null 2>&1; then
      log "reservation scaled to $target"
    else
      log "ERR: scale failed"
    fi
  fi
  sleep "$POLL"
done
log "exit"
