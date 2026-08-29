#!/usr/bin/env bash
# bench_preflight.sh — pre-run stack readiness check.
#
# Reads bench-meta.json (written by bench_init.sh) and reports the state of
# every stack managed by WVA in the target namespace. Focused on the endpoint
# URL the harness will drive: which SOs are wired to it, their KEDA/WVA state,
# replica readiness, and whether WVA's scaler service is reachable.
#
# Always exits 0 — issues are warnings, never blocking. The caller decides
# whether to proceed.
#
# Usage:
#   bench_preflight.sh <bench-meta.json>
#
# Environment:
#   KUBECTL_CMD   kubectl binary (default: kubectl)
set -euo pipefail

META_FILE="${1:?usage: $0 <bench-meta.json>}"
KUBECTL="${KUBECTL_CMD:-kubectl}"

[ -f "$META_FILE" ] || { echo "bench_preflight: ERROR: $META_FILE not found" >&2; exit 1; }

_info()  { echo "bench_preflight: $*"; }
_warn()  { echo "bench_preflight: WARNING: $*"; }
_ok()    { echo "bench_preflight:   verdict:       OK"; }

# ---------------------------------------------------------------------------
# Parse bench-meta.json
# ---------------------------------------------------------------------------
python3 - "$META_FILE" "$KUBECTL" <<'PYEOF'
import json, subprocess, sys

meta_path = sys.argv[1]
kubectl   = sys.argv[2]

meta   = json.load(open(meta_path))
ns     = meta["identity"]["namespace"]
stacks = meta.get("stacks", [])
wva    = meta.get("wva", {})
prom   = meta.get("prometheus", {})

def krun(*args):
    r = subprocess.run([kubectl] + list(args), capture_output=True, text=True)
    return r.stdout.strip(), r.returncode

def section(msg):
    print(f"\nbench_preflight: {msg}")

def field(k, v):
    print(f"bench_preflight:   {k:<22} {v}")

# ---------------------------------------------------------------------------
# Header
# ---------------------------------------------------------------------------
section(f"=== Pre-run check: namespace={ns} ===")
field("endpoint (run target):", stacks[0].get("endpoint_url", "?") if stacks else "?")
field("stacks discovered:", str(len(stacks)))
field("prometheus:", f"{prom.get('type','?')}  {prom.get('url','')}")

# ---------------------------------------------------------------------------
# Per-stack report
# ---------------------------------------------------------------------------
n_ok   = 0
n_warn = 0

for i, s in enumerate(stacks):
    section(f"Stack {i+1}/{len(stacks)}: {s['name']}")
    field("scaledobject:",  s.get("scaledobject", "?"))
    field("deployment:",    s.get("deployment",   "?"))
    field("model_id:",      s.get("model_id",     "?"))
    field("wva_trigger:",   s.get("wva_trigger_address", "?"))

    paused      = s.get("so_paused",      False)
    keda_active = s.get("so_keda_active", False)
    ready       = s.get("ready_replicas", 0)
    min_r       = s.get("min_replicas",   1)
    max_r       = s.get("max_replicas",   10)

    field("so_paused:",     str(paused).lower() + ("  ← KEDA will not actuate" if paused else ""))
    field("so_keda_active:", str(keda_active).lower())
    field("replicas:",      f"{ready} ready  (min={min_r} max={max_r})")

    # Verdict
    warnings = []
    if paused:
        warnings.append("SO paused — KEDA will not scale this stack")
    if paused and ready == 0:
        warnings.append("0 ready replicas — stack is dark, will not serve traffic")
    if not paused and ready == 0:
        warnings.append("0 ready replicas but SO is not paused — may be scaling up or stalled")
    if not keda_active and not paused:
        warnings.append("KEDA Active condition is False — scaler may not be receiving metrics")

    if warnings:
        for w in warnings:
            print(f"bench_preflight:   WARNING: {w}")
        n_warn += 1
    else:
        print(f"bench_preflight:   verdict:       OK")
        n_ok += 1

# ---------------------------------------------------------------------------
# WVA controller check
# ---------------------------------------------------------------------------
section("WVA controller")
wva_deploy = wva.get("controller_deployment", "wva-controller-manager")
out, rc = krun("get", "deploy", wva_deploy, "-n", ns,
               "-o", "jsonpath={.status.readyReplicas}/{.status.replicas}")
if rc == 0 and out:
    field("controller deploy:", f"{wva_deploy}  {out} ready")
    if not out.startswith("0/") and "/" in out:
        r, t = out.split("/", 1)
        if r == t and r != "0":
            print(f"bench_preflight:   verdict:       OK")
        else:
            print(f"bench_preflight:   WARNING: controller not fully ready ({out})")
    else:
        print(f"bench_preflight:   WARNING: controller has 0 ready replicas")
else:
    print(f"bench_preflight:   WARNING: could not read deploy/{wva_deploy}")

# WVA scaler service (the gRPC endpoint KEDA calls)
wva_scaler_svc = ""
# Derive from trigger address: "wva-external-scaler.<ns>.svc.cluster.local:9090" → "wva-external-scaler"
for s in stacks:
    addr = s.get("wva_trigger_address", "")
    if addr:
        wva_scaler_svc = addr.split(".")[0]
        break

if wva_scaler_svc:
    out, rc = krun("get", "svc", wva_scaler_svc, "-n", ns,
                   "-o", "jsonpath={.spec.clusterIP}:{.spec.ports[0].port}")
    if rc == 0 and out:
        field("scaler service:",  f"{wva_scaler_svc}  {out}")
        print(f"bench_preflight:   verdict:       OK")
    else:
        print(f"bench_preflight:   WARNING: svc/{wva_scaler_svc} not found in {ns}")
else:
    print(f"bench_preflight:   WARNING: no wva_trigger_address found in any stack — cannot verify scaler service")

# WVA metrics service
wva_metrics_svc = wva.get("metrics_service", "")
if wva_metrics_svc:
    out, rc = krun("get", "svc", wva_metrics_svc, "-n", ns,
                   "-o", "jsonpath={.spec.clusterIP}:{.spec.ports[0].port}")
    field("metrics service:",  f"{wva_metrics_svc}  {out if rc==0 else '?'}")
    if rc != 0:
        print(f"bench_preflight:   WARNING: svc/{wva_metrics_svc} not found in {ns}")

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
section("=== Summary ===")
field("stacks OK:",      str(n_ok))
field("stacks WARNING:", str(n_warn))
if stacks:
    run_stack = stacks[0]
    field("run target:",     f"stacks[0] — {run_stack['name']}")
    if run_stack.get("so_paused"):
        print(f"bench_preflight:   WARNING: run target is paused — load will be sent but WVA cannot scale")
    elif run_stack.get("ready_replicas", 0) == 0:
        print(f"bench_preflight:   WARNING: run target has 0 ready replicas")
    else:
        print(f"bench_preflight:   proceed:       YES")
PYEOF
