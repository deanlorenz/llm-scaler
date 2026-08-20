#!/usr/bin/env bash
# verify_wva_scaledobjects.sh -- read-only: does every model server's
# ScaledObject still point WVA at the model it actually serves?
#
# Built after a real incident on dhl-la-1708: a Deployment's serving model was
# changed by hand (see dhl-la-1708.env's OOM workaround) without also updating
# the ScaledObject already scaling it. Nothing re-syncs that automatically --
# deploy/lib/scaledobject.sh only ever writes it once, at creation. WVA kept
# evaluating decisions for the model it was told about, never matched any
# scraped metric, and applied zero scaling decisions for an entire benchmark
# run -- silently, with real load and a real queue buildup the whole time. See
# docs/plans/benchmark/observability-gaps.md #5 for the full trace.
#
# This is read-only and reuses deploy/lib/scaledobject.sh's own discovery
# (install_default_scaledobjects in plan mode, so_plan_rows) rather than
# reimplementing model detection: that is the code that derives modelID from a
# live container's actual `vllm serve` args, and it is the same code any real
# fix runs through (make scaledobjects-apply ... apply: adopt). This script
# never calls so_apply_plan and never mutates anything.
#
# Usage:
#   verify_wva_scaledobjects.sh <namespace> [--report-only]
#
# Exit status: 0 if every registered model server's ScaledObject modelID
# matches what its container actually serves and every discovered model
# server is registered, 1 otherwise. --report-only always exits 0.
set -euo pipefail

NS="${1:?usage: $0 <namespace> [--report-only]}"
REPORT_ONLY=0
[ "${2:-}" = "--report-only" ] && REPORT_ONLY=1

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# common.sh's log_* helpers reference these unconditionally (deploy/install.sh
# normally defines them before sourcing it); under `set -u` here they must
# exist first.
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'
# shellcheck source=../../deploy/lib/common.sh
source "$REPO_ROOT/deploy/lib/common.sh"
# shellcheck source=../../deploy/lib/scaledobject.sh
source "$REPO_ROOT/deploy/lib/scaledobject.sh"

PLAN_FILE=$(mktemp -t wva-verify-plan.XXXXXX.yaml)
trap 'rm -f "$PLAN_FILE"' EXIT

export WVA_NS="$NS" NAMESPACE="$NS" WVA_SCOPE=namespace WVA_DEFAULT_SO_NS="$NS" \
       WVA_DEFAULT_SO=plan WVA_DEFAULT_SO_PLAN="$PLAN_FILE"
wva_bootstrap_env
install_default_scaledobjects

echo
echo "=== modelID drift check ==="
printf '%-55s %-20s %-30s %s\n' "WORKLOAD" "SERVES" "SCALEDOBJECT SAYS" "STATUS"

drift=0 unregistered=0 unresolved=0 ok=0
while IFS=$'\037' read -r apply p_ns kind name model minr maxr cost policy; do
    [ -n "$name" ] || continue
    if [ -z "$model" ]; then
        printf '%-55s %-20s %-30s %s\n' "$p_ns/$name" "(unreadable)" "-" "SKIP"
        unresolved=$((unresolved + 1))
        continue
    fi
    existing=$(so_existing_name "$p_ns" "$name")
    if [ -z "$existing" ]; then
        printf '%-55s %-20s %-30s %s\n' "$p_ns/$name" "$model" "(none)" "UNREGISTERED"
        unregistered=$((unregistered + 1))
        continue
    fi
    configured=$(kubectl get scaledobject -n "$p_ns" "$existing" -o json 2>/dev/null \
        | jq -r '[.spec.triggers[]? | select(.type | startswith("external")) | .metadata.modelID // empty] | first // empty')
    if [ "$configured" = "$model" ]; then
        printf '%-55s %-20s %-30s %s\n' "$p_ns/$name" "$model" "$existing: $configured" "OK"
        ok=$((ok + 1))
    else
        printf '%-55s %-20s %-30s %s\n' "$p_ns/$name" "$model" "$existing: ${configured:-(empty)}" "DRIFT"
        drift=$((drift + 1))
    fi
done < <(so_plan_rows "$PLAN_FILE")

echo
echo "verify-wva-scaledobjects: $ok ok, $drift drift, $unregistered unregistered, $unresolved unresolved"
if [ "$drift" -gt 0 ]; then
    echo "  DRIFT: a ScaledObject's modelID no longer matches what its container serves."
    echo "  WVA silently applies zero decisions for that workload -- it never matches a"
    echo "  scraped metric. Fix through the code that owns this config, not by hand-patching:"
    echo "    make scaledobjects-plan WVA_DEFAULT_SO_NS=$NS   # edit that entry's apply: to adopt"
    echo "    make scaledobjects-apply WVA_DEFAULT_SO_PLAN=<edited file>"
fi
if [ "$unregistered" -gt 0 ]; then
    echo "  UNREGISTERED: a discovered model server has no ScaledObject at all --"
    echo "  it is never autoscaled by WVA. Review with:"
    echo "    make scaledobjects-plan WVA_DEFAULT_SO_NS=$NS"
fi

[ "$REPORT_ONLY" = "1" ] && exit 0
[ "$drift" -eq 0 ] && [ "$unregistered" -eq 0 ]
