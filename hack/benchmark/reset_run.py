#!/usr/bin/env python3
"""
reset_run.py — return the namespace to a clean pre-run state, and nothing more.

Ported from llm-d-workload-variant-autoscaler's benchmark branch. Dropped: the
workload-PVC / data-access-pod reclaim logic -- this repo's benchmark pipeline
(the upstream llmdbenchmark CLI) lands per-run results directly on the runner
host's own workspace, there is no separate results PVC to harvest from here.

Why the rest still exists
-------------------------
Two runs on the same standing stack must not inherit each other's state:

  * Analyzer memory. The controller accumulates per-analyzer history
    (throughput OLS samples, saturation history). Run 2 starting with run 1's
    accumulators is not a clean run 2.
  * vLLM prefix cache. Reusing the same prompt seed would serve run 2 partly
    from a cache run 1 warmed, and report better TTFT for no reason
    attributable to the controller.
  * Leftover harness objects. The upstream harness cleans its own pod/
    ConfigMaps on a normal exit, but not after a failed or interrupted run.

Scope: this is the LOWEST rung. It does not rebuild the stack, does not touch
the namespace's shape, and cannot touch anything cluster-scoped.

Two non-actions worth stating, because both look like omissions:

  * KEDA pause state is reported, never changed. A ScaledObject left paused
    means the next run traces flat at whatever replica count it is pinned to,
    which looks like an autoscaling result and is not one. Un-pausing is a
    decision about starting a run, and belongs to whoever starts it.
  * The model server's own Deployment/Service/InferencePool/gateway stay --
    those are the stack's shape, not per-run state.

Dry run is the default. Nothing is written without --apply.

Usage
-----
  python3 reset_run.py -n dhl-la-1708                 # show what would reset
  python3 reset_run.py -n dhl-la-1708 --apply          # do it
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys

# Label the upstream llm-d-benchmark harness pod carries.
HARNESS_POD_LABEL = "app=llmdbench-harness-launcher"
HARNESS_SCRIPTS_CONFIGMAP = "llmdbench-harness-scripts"

# This repo's controller Deployment carries this label regardless of overlay.
CONTROLLER_LABEL = "app.kubernetes.io/name=workload-variant-autoscaler"


def kubectl(namespace: str, *args: str) -> subprocess.CompletedProcess:
    return subprocess.run(["kubectl", "-n", namespace, *args],
                          capture_output=True, text=True, check=False)


def get_json(namespace: str, *args: str):
    proc = kubectl(namespace, *args, "-o", "json")
    if proc.returncode != 0:
        return None
    try:
        return json.loads(proc.stdout)
    except json.JSONDecodeError:
        return None


class Reset:
    def __init__(self, namespace: str, apply: bool):
        self.namespace = namespace
        self.apply = apply
        self.planned: list[str] = []
        self.skipped: list[str] = []
        self.failed: list[str] = []

    def plan(self, description: str) -> None:
        self.planned.append(description)
        print(f"  {'DO  ' if self.apply else 'WOULD'} {description}")

    def skip(self, description: str) -> None:
        self.skipped.append(description)
        print(f"  skip  {description}")

    def run(self, description: str, *args: str) -> bool:
        self.plan(description)
        if not self.apply:
            return True
        proc = kubectl(self.namespace, *args)
        if proc.returncode != 0:
            err = (proc.stderr or proc.stdout).strip().splitlines()
            print(f"        FAILED: {err[0] if err else 'unknown error'}")
            self.failed.append(description)
            return False
        return True


def find_controller_deployment(namespace: str) -> str | None:
    data = get_json(namespace, "get", "deploy", "-l", CONTROLLER_LABEL)
    items = (data or {}).get("items") or []
    return items[0]["metadata"]["name"] if items else None


def find_scaled_deployments(namespace: str) -> list[tuple[str, str, str | None]]:
    """(scaledobject, deployment, paused-replicas annotation or None) for every
    ScaledObject in the namespace, discovered rather than guessed by name."""
    data = get_json(namespace, "get", "scaledobject")
    out = []
    for item in (data or {}).get("items") or []:
        target = (item.get("spec") or {}).get("scaleTargetRef") or {}
        name = target.get("name")
        if not name:
            continue
        paused = (item["metadata"].get("annotations") or {}).get(
            "autoscaling.keda.sh/paused-replicas")
        out.append((item["metadata"]["name"], name, paused))
    return out


def find_harness_pods(namespace: str) -> list[str]:
    data = get_json(namespace, "get", "pods", "-l", HARNESS_POD_LABEL)
    return [i["metadata"]["name"] for i in (data or {}).get("items") or []]


def find_harness_configmaps(namespace: str) -> list[str]:
    data = get_json(namespace, "get", "configmap")
    found = []
    for item in (data or {}).get("items") or []:
        name = item["metadata"]["name"]
        if name == HARNESS_SCRIPTS_CONFIGMAP or name.endswith("-profiles"):
            found.append(name)
    return found


def reset_harness_objects(r: Reset) -> None:
    print("\nLeftover harness objects (the upstream harness normally removes these on exit)")
    pods = find_harness_pods(r.namespace)
    if pods:
        for pod in pods:
            r.run(f"delete pod/{pod}", "delete", "pod", pod, "--ignore-not-found")
    else:
        r.skip("no harness pod present")

    cms = find_harness_configmaps(r.namespace)
    if cms:
        for cm in cms:
            r.run(f"delete configmap/{cm}", "delete", "configmap", cm, "--ignore-not-found")
    else:
        r.skip("no per-run ConfigMaps present")


def reset_controller(r: Reset, timeout: str) -> None:
    print("\nWVA controller (flush analyzer in-memory state)")
    deploy = find_controller_deployment(r.namespace)
    if not deploy:
        r.skip("no WVA controller deployment found in this namespace")
        return
    if not r.run(f"rollout restart deploy/{deploy}", "rollout", "restart", f"deploy/{deploy}"):
        return
    if r.apply:
        proc = kubectl(r.namespace, "rollout", "status", f"deploy/{deploy}", f"--timeout={timeout}")
        print(f"        {proc.stdout.strip() or proc.stderr.strip()}")
        if proc.returncode != 0:
            r.failed.append(f"deploy/{deploy} did not become ready within {timeout}")


def reset_decode(r: Reset, timeout: str) -> None:
    print("\nDecode pods (flush the vLLM prefix cache)")
    targets = find_scaled_deployments(r.namespace)
    if not targets:
        r.skip("no ScaledObject in this namespace -- nothing identified as decode")
        return

    for so, deploy, paused in targets:
        if paused is not None:
            r.skip(f"deploy/{deploy} -- scaledobject/{so} is paused at "
                   f"{paused} replica(s); nothing running to restart")
            continue
        if not r.run(f"rollout restart deploy/{deploy} (via scaledobject/{so})",
                     "rollout", "restart", f"deploy/{deploy}"):
            continue
        if r.apply:
            proc = kubectl(r.namespace, "rollout", "status", f"deploy/{deploy}",
                           f"--timeout={timeout}")
            print(f"        {proc.stdout.strip() or proc.stderr.strip()}")
            if proc.returncode != 0:
                r.failed.append(f"deploy/{deploy} did not become ready within {timeout}")


def report_untouched(r: Reset) -> None:
    print("\nNot touched (raise the scope deliberately if you need any of these)")
    for line in [
        "ScaledObjects / KEDA-generated HPAs -- including their pause state",
        "Deployments, Services, InferencePool, gateway -- the stack's shape",
        "anything cluster-scoped -- out of reach at this scope by design",
    ]:
        print(f"  keep  {line}")

    for so, deploy, paused in find_scaled_deployments(r.namespace):
        if paused is not None:
            print(
                f"\nNOTE: scaledobject/{so} is PAUSED at {paused} replica(s).\n"
                f"      deploy/{deploy} will not scale, and a run started now would\n"
                f"      trace flat -- which reads as an autoscaling result.\n"
                f"      Un-pause with:\n"
                f"        kubectl annotate scaledobject/{so} -n {r.namespace} \\\n"
                f"          autoscaling.keda.sh/paused-replicas-"
            )


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Reset per-run state in one namespace. Dry run unless --apply.")
    parser.add_argument("-n", "--namespace", required=True)
    parser.add_argument("--apply", action="store_true",
                        help="actually perform the reset (default: report only)")
    parser.add_argument("--skip-restart", action="store_true", help="do not restart any pods")
    parser.add_argument("--controller-timeout", default="120s")
    parser.add_argument("--decode-timeout", default="600s")
    args = parser.parse_args()

    probe = kubectl(args.namespace, "get", "namespace", args.namespace, "-o", "name")
    if probe.returncode != 0:
        print(f"ERROR: namespace {args.namespace} not reachable: {(probe.stderr or '').strip()}",
              file=sys.stderr)
        sys.exit(1)

    mode = "APPLY" if args.apply else "DRY RUN -- nothing will be changed"
    print(f"Reset run state in namespace {args.namespace}   [{mode}]")

    r = Reset(args.namespace, args.apply)
    reset_harness_objects(r)
    if args.skip_restart:
        print("\nPod restarts")
        r.skip("--skip-restart")
    else:
        reset_controller(r, args.controller_timeout)
        reset_decode(r, args.decode_timeout)
    report_untouched(r)

    print(f"\n{len(r.planned)} action(s) {'performed' if args.apply else 'planned'}, "
          f"{len(r.skipped)} skipped, {len(r.failed)} failed")
    if not args.apply and r.planned:
        print("Re-run with --apply to perform them.")
    if r.failed:
        for f in r.failed:
            print(f"  FAILED: {f}")
        sys.exit(1)


if __name__ == "__main__":
    main()
