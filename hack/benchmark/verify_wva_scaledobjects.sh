#!/usr/bin/env bash
# verify_wva_scaledobjects.sh -- read-only: does every ScaledObject's modelID
# still match what its target Deployment's container actually serves?
#
# Self-contained: reads the cluster directly. No deploy/lib dependencies, no
# env files, no install-tooling. Works against any llm-d+WVA install regardless
# of how it was created.
#
# Built after a real incident on dhl-la-1708: a Deployment's serving model was
# changed by hand without also updating its ScaledObject. WVA kept evaluating
# decisions for the model it was told about, never matched any scraped metric,
# and applied zero scaling decisions for an entire benchmark run silently.
#
# Usage:
#   verify_wva_scaledobjects.sh <namespace> [--model <model-id>] [--report-only]
#
# Modes:
#   --model <model-id>   Targeted: verify this specific model is registered and
#                        its ScaledObject's modelID matches what its Deployment
#                        actually serves. Exit 1 on drift, unregistered, or not
#                        found. Use this from benchmark-run to gate a specific run.
#   (no --model)         Full scan: report OK/DRIFT/UNREGISTERED for every
#                        ScaledObject in the namespace. Use this for the standalone
#                        benchmark-verify-scaledobjects target.
#
# Exit status: 0 if every checked ScaledObject is OK, 1 otherwise.
# --report-only always exits 0.
#
# Prereqs: kubectl, python3 (stdlib only).
set -euo pipefail

NS="${1:?usage: $0 <namespace> [--model <model-id>] [--report-only]}"
REPORT_ONLY=0
MODEL_ID=""

shift
while [ $# -gt 0 ]; do
    case "$1" in
        --model)    MODEL_ID="${2:?--model requires a value}"; shift 2 ;;
        --report-only) REPORT_ONLY=1; shift ;;
        *) echo "verify-wva-scaledobjects: unknown argument: $1" >&2; exit 1 ;;
    esac
done

KUBECTL="${KUBECTL_CMD:-kubectl}"

if ! $KUBECTL get namespace "$NS" >/dev/null 2>&1; then
    echo "verify-wva-scaledobjects: namespace '$NS' not found" >&2
    exit 1
fi

# Fetch all ScaledObjects and all Deployments in one call each, then compare in
# Python so we make exactly two API calls regardless of how many workloads exist.
so_json=$($KUBECTL get scaledobject -n "$NS" -o json 2>/dev/null || echo '{"items":[]}')
deploy_json=$($KUBECTL get deploy -n "$NS" -o json 2>/dev/null || echo '{"items":[]}')

# Feed both JSON blobs via environment variables into a single Python process.
result=$(SO_JSON="$so_json" DEPLOY_JSON="$deploy_json" NS="$NS" MODEL_ID="$MODEL_ID" \
    python3 -c '
import json, os, re, sys

ns          = os.environ["NS"]
model_id    = os.environ.get("MODEL_ID", "")   # empty = full scan
so_data     = json.loads(os.environ["SO_JSON"])
deploy_data = json.loads(os.environ["DEPLOY_JSON"])

# Index deployments by name -> list of container args.
deploy_args = {}
for d in deploy_data.get("items", []):
    name = d["metadata"]["name"]
    containers = (d.get("spec",{}).get("template",{}).get("spec",{})
                   .get("containers") or [])
    args = []
    for c in containers:
        args.extend(c.get("args") or [])
    deploy_args[name] = args

def model_from_args(args):
    """Extract the model a vLLM/SGLang container actually serves from its args."""
    # vLLM: `vllm serve <model>` or `serve <model>` or positional first non-flag
    for i, a in enumerate(args):
        if a == "serve" and i + 1 < len(args) and not args[i+1].startswith("-"):
            return args[i + 1]
    # --served-model-name overrides the positional
    for a in args:
        m = re.match(r"--served-model-name[= ](.+)", a)
        if m:
            return m.group(1)
    # positional: first non-flag arg that looks like a model path (contains "/")
    for a in args:
        if "/" in a and not a.startswith("-"):
            return a
    return ""

def so_declares_model(so, expected):
    for t in (so.get("spec",{}).get("triggers") or []):
        if (t.get("metadata") or {}).get("modelID","") == expected:
            return True
    return False

drift = ok = unregistered = unresolved = 0
rows = []

# In targeted mode, filter to only SOs that declare this modelID or whose
# scale target actually serves it. SOs for other models are irrelevant to this run.
# In scan mode, iterate everything.
all_sos = so_data.get("items", [])
if model_id:
    filtered = []
    for so in all_sos:
        tgt = (so.get("spec",{}).get("scaleTargetRef",{}) or {}).get("name","")
        live = model_from_args(deploy_args.get(tgt, [])) if tgt else ""
        if so_declares_model(so, model_id) or live == model_id:
            filtered.append(so)
    if not filtered:
        print(f"verify-wva-scaledobjects: model {model_id!r} not found in namespace {ns!r}")
        print("  No ScaledObject declares this modelID and no Deployment serves it.")
        print(f"  Register it:  make scaledobjects-plan WVA_DEFAULT_SO_NS={ns}")
        sys.exit(1)
    all_sos = filtered

for so in all_sos:
    so_name  = so["metadata"]["name"]
    target   = (so.get("spec",{}).get("scaleTargetRef",{}) or {}).get("name","")

    # modelID from the first external trigger
    so_model = ""
    for t in (so.get("spec",{}).get("triggers") or []):
        mid = (t.get("metadata") or {}).get("modelID","")
        if mid:
            so_model = mid
            break

    if not target:
        rows.append(("SKIP", so_name, "(no scaleTargetRef)", so_model or "(empty)", "no scale target"))
        unresolved += 1
        continue

    if target not in deploy_args:
        rows.append(("SKIP", so_name, target, so_model or "(empty)", "target Deployment not found"))
        unresolved += 1
        continue

    live_model = model_from_args(deploy_args[target])
    if not live_model:
        rows.append(("SKIP", so_name, target, so_model or "(empty)", "cannot read model from container args"))
        unresolved += 1
        continue

    if not so_model:
        rows.append(("UNREGISTERED", so_name, target, "(empty)", f"serves {live_model!r} but ScaledObject has no modelID trigger"))
        unregistered += 1
        continue

    if so_model == live_model:
        rows.append(("OK", so_name, target, so_model, live_model))
        ok += 1
    else:
        rows.append(("DRIFT", so_name, target, so_model, live_model))
        drift += 1

# Print table
header = "{:<14} {:<45} {:<35} {:<30} {}".format(
    "STATUS", "SCALEDOBJECT", "TARGET", "SO modelID", "LIVE model")
print(header)
print("-" * 140)
for status, so_name, target, so_model, live in rows:
    print("{:<14} {:<45} {:<35} {:<30} {}".format(status, so_name, target, so_model, live))

print()
print(f"verify-wva-scaledobjects: {ok} ok, {drift} drift, {unregistered} unregistered, {unresolved} unresolved")

if drift:
    print()
    print("  DRIFT: a ScaledObject modelID no longer matches what its Deployment serves.")
    print("  WVA evaluates decisions for the wrong model and never matches a scraped metric.")
    print("  Fix through the code that owns this config (never hand-patch):")
    print(f"    make scaledobjects-plan WVA_DEFAULT_SO_NS={ns}")
    print( "    (edit the generated plan: set apply: adopt on the drifted entry)")
    print( "    make scaledobjects-apply WVA_DEFAULT_SO_PLAN=<edited file>")

if unregistered:
    print()
    print("  UNREGISTERED: a model server has no ScaledObject -- WVA never autoscales it.")
    print(f"    make scaledobjects-plan WVA_DEFAULT_SO_NS={ns}")

sys.exit(1 if (drift or unregistered) else 0)
')

rc=$?
echo "$result"
[ "$REPORT_ONLY" = "1" ] && exit 0
exit $rc
