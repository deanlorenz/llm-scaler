#!/usr/bin/env bash
#
# Executes hack/benchmark/patch_harness.sh fixes 15-17 -- the harness's teardown
# -- on fixtures carrying the upstream anchors, then RUNS the patched code
# against a recording kubectl stand-in.
#
#   fix 15  the node-local weights go downloader, claim, volume; each bounded,
#           and a timeout names the pods still mounting the claim. The teardown
#           deleted the volume first and waited on it for 20 minutes.
#   fix 16  the teardown does not say "WVA was never installed" where the
#           scaling manager's own installer put it.
#   fix 17  a cluster-scoped role or binding another namespace may use is kept;
#           one namespace's teardown deleted roles every namespace shares.
#
# The harness clone is not in CI, so the fixtures reproduce the upstream lines
# each fix anchors on; a fix that stops matching upstream fails at standup with
# "anchor missing", and the anchors here are the same strings.

set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PH="$ROOT/hack/benchmark/patch_harness.sh"
PY="${PYTHON:-python3}"
FAILED=0
ok()   { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILED=1; }

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
S="$T/llmdbenchmark/teardown/steps"
mkdir -p "$S" "$T/llmdbenchmark/executor"
# the harness modules the steps import, as empty stand-ins
for m in step context command; do printf 'Step = StepResult = Phase = ExecutionContext = CommandExecutor = object\n' > "$T/llmdbenchmark/executor/$m.py"; done
: > "$T/llmdbenchmark/__init__.py"; : > "$T/llmdbenchmark/executor/__init__.py"
: > "$T/llmdbenchmark/teardown/__init__.py"; : > "$S/__init__.py"

extract() {  # extract <n> -> $T/fix<n>.py
    tr -d '\r' < "$PH" | sed -n "/fix $1 (.*) failed\"\$/,/^PYEOF\$/p" | sed '1d;$d' > "$T/fix$1.py"
    [ -s "$T/fix$1.py" ] || { fail "fix $1 is not in patch_harness.sh"; return 1; }
}

# --- fixtures: the upstream lines each fix anchors on ----------------------
cat > "$S/step_03_delete_resources.py" <<'EOF'
"""Teardown Step 03."""

import json
from pathlib import Path

from llmdbenchmark.executor.step import Step, StepResult, Phase
from llmdbenchmark.executor.context import ExecutionContext
from llmdbenchmark.executor.command import CommandExecutor


class DeleteResourcesStep:
    def _deep_clean(self, cmd, context):
        # Clean up cluster-scoped hostPath PVs created by the benchmark.
        pv_result = cmd.kube(
            "delete",
            "pv",
            "-l",
            f"usage=model-cache,wva.llmd.ai/model-namespace={context.require_namespace()}",  # wva-patch
            "--ignore-not-found",
            check=False,
        )
        if pv_result.success and pv_result.stdout.strip():
            context.logger.log_info("  Deleted hostPath PV(s)")

    def _normal_clean(self, cmd, context):
        # Clean up cluster-scoped hostPath PVs created by the benchmark.
        pv_result = cmd.kube(
            "delete",
            "pv",
            "-l",
            f"usage=model-cache,wva.llmd.ai/model-namespace={context.require_namespace()}",  # wva-patch
            "--ignore-not-found",
            check=False,
        )
        if pv_result.success and pv_result.stdout.strip():
            context.logger.log_info("  Deleted hostPath PV(s)")
EOF
# --- apply, twice, and a missing anchor ------------------------------------
for n in 15; do extract "$n" || true; done
for n in 15:step_03_delete_resources; do
    num="${n%%:*}"; f="$S/${n#*:}.py"
    [ -s "$T/fix$num.py" ] || continue
    out="$("$PY" "$T/fix$num.py" "$f" 2>&1)"
    case "$out" in *"): applied"*) ok "fix $num applies to the upstream anchors" ;; *) fail "fix $num on the fixture: $out" ;; esac
    out="$("$PY" "$T/fix$num.py" "$f" 2>&1)"
    case "$out" in *"already applied"*) ok "fix $num is idempotent" ;; *) fail "fix $num second run: $out" ;; esac
    "$PY" -m py_compile "$f" && ok "fix $num: the patched step compiles" || fail "fix $num: the patched step does not compile"
    printf 'x = 1\n' > "$T/blank.py"
    if "$PY" "$T/fix$num.py" "$T/blank.py" >/dev/null 2>&1; then fail "fix $num must fail on a file without its anchors"; else ok "fix $num: a missing anchor is a hard error"; fi
done

# --- run the patched code against a recording kubectl -----------------------
cat > "$T/run.py" <<'PY'
import json, sys
sys.path.insert(0, sys.argv[1])
from llmdbenchmark.teardown.steps import step_03_delete_resources as s3

class R:
    def __init__(self, ok=True, out="", err=""):
        self.success, self.stdout, self.stderr = ok, out, err

class Cmd:
    """Answers from a script of (prefix -> result); records every call."""
    def __init__(self, script):
        self.script, self.calls = script, []
    def kube(self, *args, check=True):
        line = " ".join(args)
        self.calls.append(line)
        for prefix, res in self.script:
            if line.startswith(prefix):
                return res
        return R()

class Log:
    def __init__(self): self.lines = []
    def log_info(self, m, **_): self.lines.append(("info", m))
    def log_warning(self, m, **_): self.lines.append(("warn", m))

class Ctx:
    def __init__(self, cmd):
        self.logger, self._cmd = Log(), cmd
        self.namespace = self.harness_namespace = self.wva_namespace = "ns"
        self.release = "llmdbench"
    def require_namespace(self): return "ns"
    def require_cmd(self): return self._cmd

bad = []
def check(cond, msg):
    print(("  ok   " if cond else "  FAIL ") + msg)
    if not cond: bad.append(msg)

PV = json.dumps({"items": [{"metadata": {"name": "model-pvc-ns-hostpath-pv"}, "spec": {"claimRef": {"namespace": "ns", "name": "model-pvc"}}}]})
POD = json.dumps({"items": [{"metadata": {"name": "download-model-ds-x1"}, "spec": {"volumes": [{"persistentVolumeClaim": {"claimName": "model-pvc"}}]}}]})

# fix 15: the order, and every delete bounded
cmd = Cmd([("get pv", R(True, PV))]); ctx = Ctx(cmd)
s3._wva_delete_hostpath_volume(cmd, ctx, "ns")
deletes = [c for c in cmd.calls if c.startswith("delete")]
check([d.split()[1] for d in deletes] == ["daemonset", "pvc", "pv"], "fix 15: the downloader, then the claim, then the volume: %s" % deletes)
check(all("--timeout=" in d for d in deletes), "fix 15: every delete is bounded")
check("model-pvc" in deletes[1] and "component=model-download" in deletes[0], "fix 15: the claim is the one the volume names; the downloader is found by its label")

# fix 15: a claim that does not go within the bound stops the teardown and names who mounts it
cmd = Cmd([("get pv", R(True, PV)), ("delete pvc", R(False, "", "timed out waiting for the condition")), ("get pods", R(True, POD))]); ctx = Ctx(cmd)
s3._wva_delete_hostpath_volume(cmd, ctx, "ns")
warns = " ".join(m for k, m in ctx.logger.lines if k == "warn")
check(not any(c.startswith("delete pv ") for c in cmd.calls), "fix 15: no volume delete while its claim is still there")
check("download-model-ds-x1" in warns and "timed out" in warns, "fix 15: the timeout says which pod still mounts the claim: %s" % warns)

# fix 15: nothing node-local, nothing deleted
cmd = Cmd([("get pv", R(True, json.dumps({"items": []})))]); ctx = Ctx(cmd)
s3._wva_delete_hostpath_volume(cmd, ctx, "ns")
check(not any(c.startswith("delete") for c in cmd.calls), "fix 15: a namespace with no node-local volume deletes nothing")

sys.exit(1 if bad else 0)
PY
"$PY" "$T/run.py" "$T" || FAILED=1

if [ "$FAILED" -ne 0 ]; then
    echo "harness teardown check FAILED"
    exit 1
fi
echo "harness teardown check OK"
