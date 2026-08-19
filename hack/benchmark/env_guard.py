#!/usr/bin/env python3
"""
env_guard.py — assert that a benchmark run is reproducible from a named env file.

Ported from llm-d-workload-variant-autoscaler's benchmark branch (the original
carries the full "why" -- see planning/benchmark-env-guard-design.md there).
Adapted here: REQUIRED_KEYS matches this repo's single IMG variable (no split
WVA_IMAGE_REPO/TAG) and drops the fixed Thanos URL this repo does not assume --
PROMETHEUS_URL is optional and auto-detected by deploy/install.sh when unset.

What this guards -- and what it deliberately does not
-------------------------------------------------------
Runs before DESTRUCTIVE operations only: standup, teardown, a run (real GPUs on
a shared cluster), controller restart, per-run reset. Read-only/local-only
targets (benchmark-preflight, benchmark-report, benchmark-analyze,
benchmark-plot-*, benchmark-install) are not gated -- they cannot change the
cluster, so a wrong env file cannot do damage through them.

Named env files, not context-named files
-----------------------------------------
Selection is BENCHMARK_ENV=<name> -> hack/benchmark/<name>.env. The kube context
is declared *inside* the file as KUBE_CONTEXT and verified against the live one.
A filename cannot be verified; a declared value can.

UNSAFE has levels, because "bypass the guard" is not one decision
-------------------------------------------------------------------
  UNSAFE=confirm  (or `true`)  ask per bypassed guard, interactively
  UNSAFE=once                  ask once, covering every bypassed guard
  UNSAFE=silent                bypass with no prompt; still logged

Read-only: this script never writes to the cluster or to the env file.
"""
import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

UNSAFE_LEVELS = ("confirm", "once", "silent")

# Keys that must be present for a run to be attributable after the fact.
REQUIRED_KEYS = (
    "KUBE_CONTEXT",
    "BENCHMARK_NAMESPACE",
    "IMG",
    "BENCHMARK_MODEL_ID",
)

# The workload profile declares its own harness (test/benchmark/scenarios/
# <workload>.yaml.in is written for one harness); overriding one without the
# other is the inconsistent state worth catching. Not to be confused with
# BENCHMARK_SPEC, the llmdbenchmark standup spec path (e.g.
# guides/workload-autoscaling) -- a different axis, not linked here.
LINKED_PAIRS = (("BENCHMARK_WORKLOAD", "BENCHMARK_HARNESS"),)


class Findings:
    """Collects refusals and complaints so all are reported in one pass."""

    def __init__(self, unsafe, interactive=True):
        self.unsafe = unsafe
        self.interactive = interactive
        self.refusals = []
        self.complaints = []

    def refuse(self, msg):
        self.refusals.append(msg)

    def complain(self, msg):
        self.complaints.append(msg)

    def _ask(self, prompt):
        if not self.interactive or not sys.stdin.isatty():
            print("  (not a terminal -- cannot confirm; treating as NO)", file=sys.stderr)
            return False
        try:
            answer = input(f"{prompt} [y/N] ").strip().lower()
        except (EOFError, KeyboardInterrupt):
            print(file=sys.stderr)
            return False
        return answer in ("y", "yes")

    def _how_to_override(self):
        print(
            "\n  To proceed anyway, pick how much confirmation you want:\n"
            "    UNSAFE=confirm   ask me about each bypassed guard (safest)\n"
            "    UNSAFE=once      ask me once, for all of them\n"
            "    UNSAFE=silent    no prompt at all (for automation; riskiest)\n"
            "  e.g.  make <target> BENCHMARK_ENV=<name> UNSAFE=confirm\n"
            "  Prefer fixing the env file where you can -- an override means the\n"
            "  env file no longer records what actually ran.",
            file=sys.stderr,
        )

    def report(self):
        for c in self.complaints:
            print(f"  [override] {c}", file=sys.stderr)
        if not self.refusals:
            if self.complaints:
                print(
                    f"env-guard: {len(self.complaints)} override(s) in effect -- "
                    f"the env file is NOT a complete record of this run.",
                    file=sys.stderr,
                )
            return 0

        for r in self.refusals:
            print(f"  [REFUSE] {r}", file=sys.stderr)

        n = len(self.refusals)
        if self.unsafe is None:
            print(f"env-guard: refusing to proceed ({n} guard(s) failed).", file=sys.stderr)
            self._how_to_override()
            return 1

        if self.unsafe == "silent":
            print(f"env-guard: UNSAFE=silent -- {n} guard(s) BYPASSED with no "
                  f"confirmation. You own the consequences.", file=sys.stderr)
            return 0

        if self.unsafe == "once":
            print(f"\nenv-guard: UNSAFE=once -- about to bypass {n} guard(s) "
                  f"listed above.", file=sys.stderr)
            if not self._ask("Bypass all of them and proceed?"):
                print("env-guard: declined; nothing was done.", file=sys.stderr)
                return 1
            print(f"env-guard: {n} guard(s) BYPASSED by confirmation.", file=sys.stderr)
            return 0

        print(f"\nenv-guard: UNSAFE=confirm -- confirming {n} guard(s) individually.",
              file=sys.stderr)
        for i, r in enumerate(self.refusals, 1):
            first = r.splitlines()[0]
            if not self._ask(f"  ({i}/{n}) Bypass: {first}"):
                print("env-guard: declined; nothing was done.", file=sys.stderr)
                return 1
        print(f"env-guard: {n} guard(s) BYPASSED by confirmation.", file=sys.stderr)
        return 0


def parse_env_file(path):
    """Parse a shell-style KEY=VALUE env file -- must agree with `make -include`."""
    out = {}
    for raw in path.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        line = re.sub(r"^export\s+", "", line)
        if "=" not in line:
            continue
        key, _, val = line.partition("=")
        key = key.strip()
        val = val.split(" #", 1)[0].strip()
        if len(val) >= 2 and val[0] == val[-1] and val[0] in "\"'":
            val = val[1:-1]
        out[key] = val
    return out


def current_context():
    proc = subprocess.run(["kubectl", "config", "current-context"],
                          capture_output=True, text=True)
    return proc.stdout.strip() if proc.returncode == 0 else None


def check_prometheus(effective, f):
    """Verify the collection target when set -- this repo lets it default to
    cluster auto-detection (deploy/install.sh), so an unset value is not an
    override worth flagging, only a set-but-unresolvable one."""
    configured = effective.get("PROMETHEUS_URL", "").strip()
    if not configured:
        return
    m = re.match(r"https?://([^:/]+)", configured)
    if not m:
        f.complain(f"PROMETHEUS_URL is not a parseable URL: {configured}")
        return
    host = m.group(1)
    parts = host.split(".")
    svc = parts[0]
    ns = parts[1] if len(parts) > 1 else ""
    if not ns:
        return  # not a cluster-internal service name; cannot verify further
    proc = subprocess.run(
        ["kubectl", "get", "svc", "-n", ns, svc, "-o", "name"],
        capture_output=True, text=True,
    )
    if proc.returncode != 0:
        f.complain(
            f"PROMETHEUS_URL points at service {svc!r} in namespace {ns!r}, "
            f"which does not exist in this cluster. Metrics collection will "
            f"silently return nothing."
        )


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--env-name", default=os.environ.get("BENCHMARK_ENV", ""))
    ap.add_argument("--env-dir", default=None)
    ap.add_argument("--unsafe", default=os.environ.get("UNSAFE", ""))
    ap.add_argument("--no-input", action="store_true")
    ap.add_argument("--effective", default="",
                    help="KEY=VALUE pairs make will actually use, comma-separated")
    ap.add_argument("--skip-context-check", action="store_true")
    args = ap.parse_args()

    unsafe_raw = (args.unsafe or "").strip().lower()
    if unsafe_raw in ("", "false", "0", "no"):
        unsafe = None
    elif unsafe_raw in ("true", "1", "yes", "confirm"):
        unsafe = "confirm"
    elif unsafe_raw in UNSAFE_LEVELS:
        unsafe = unsafe_raw
    else:
        print(f"env-guard: UNSAFE={args.unsafe!r} is not a known level. "
              f"Use one of: {', '.join(UNSAFE_LEVELS)} (or true == confirm).",
              file=sys.stderr)
        return 1

    f = Findings(unsafe=unsafe, interactive=not args.no_input)
    env_dir = Path(args.env_dir) if args.env_dir else Path(__file__).resolve().parent

    def available():
        return sorted(p.stem for p in env_dir.glob("*.env")
                      if p.name not in (".env", ".env.sample"))

    def offer_wizard(reason):
        print(f"env-guard: {reason}", file=sys.stderr)
        found = available()
        if found:
            print("  Existing env files: " + ", ".join(found), file=sys.stderr)
        print(
            "  A run must name the env file that records it:\n"
            "    make <target> BENCHMARK_ENV=<name>   # -> hack/benchmark/<name>.env",
            file=sys.stderr,
        )
        wizard = Path(__file__).resolve().parent / "env_wizard.py"
        if not wizard.is_file():
            print(f"  To create one: cp {env_dir}/.env.sample {env_dir}/<name>.env "
                  f"and edit it.", file=sys.stderr)
            return 1
        print("\n  No env file for this context yet? The wizard can create one:"
              "\n    make benchmark-init BENCHMARK_ENV=<name>", file=sys.stderr)
        if args.no_input or not sys.stdin.isatty():
            return 1
        try:
            answer = input("  Run the wizard now? [y/N] ").strip().lower()
        except (EOFError, KeyboardInterrupt):
            print(file=sys.stderr)
            return 1
        if answer not in ("y", "yes"):
            return 1
        cmd = [sys.executable, str(wizard), "--env-dir", str(env_dir)]
        if args.env_name:
            cmd += ["--name", args.env_name]
        return subprocess.run(cmd).returncode or 1

    if not args.env_name:
        return offer_wizard("BENCHMARK_ENV is not set.")

    env_path = env_dir / f"{args.env_name}.env"
    if not env_path.is_file():
        return offer_wizard(f"{env_path} does not exist.")

    env = parse_env_file(env_path)
    effective = dict(env)
    for pair in args.effective.split(","):
        if "=" in pair:
            k, _, v = pair.partition("=")
            effective[k.strip()] = v.strip()

    print(f"env-guard: {env_path.name} (namespace {env.get('BENCHMARK_NAMESPACE', '?')})",
          file=sys.stderr)

    missing = [k for k in REQUIRED_KEYS if not env.get(k, "").strip()]
    if missing:
        f.refuse(
            "env file is missing required key(s): " + ", ".join(missing) +
            "\n      Without these a run cannot be attributed after the fact."
        )

    declared = env.get("KUBE_CONTEXT", "").strip()
    if declared and not args.skip_context_check:
        live = current_context()
        if live is None:
            f.refuse("cannot read the current kube context (is kubectl configured?)")
        elif live != declared:
            f.refuse(
                f"kube context mismatch -- this env file is for a different cluster.\n"
                f"      env file declares: {declared}\n"
                f"      current context:   {live}\n"
                f"      Switch context, or select the env file for this one."
            )

    def overridden(key):
        return key in env and effective.get(key, env[key]) != env[key]

    for key in sorted(env):
        if overridden(key):
            f.complain(
                f"{key} overridden on the command line.\n"
                f"      env file: {env[key] or '(empty)'}\n"
                f"      in use:   {effective.get(key) or '(empty)'}"
            )

    for a, b in LINKED_PAIRS:
        a_over, b_over = overridden(a), overridden(b)
        if a_over != b_over:
            changed, other = (a, b) if a_over else (b, a)
            f.complain(
                f"{changed} was overridden but {other} was not. These are linked -- "
                f"the scenario declares its own harness, so changing one without the "
                f"other can run a scenario under a harness it did not specify."
            )

    check_prometheus(effective, f)

    rc = f.report()
    if rc == 0 and not f.complaints and not f.refusals:
        print("env-guard: OK -- run is fully described by the env file.", file=sys.stderr)
    return rc


if __name__ == "__main__":
    sys.exit(main())
