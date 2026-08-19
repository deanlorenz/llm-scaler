#!/usr/bin/env python3
"""
preflight_shared_cluster.py — read-only shared-cluster invariant checks.

Ported from llm-d-workload-variant-autoscaler's benchmark branch. That version
also checked for specific guard functions inside a FORKED llm-d-benchmark clone
(presence-gates on cluster-scoped operations upstream's standup would otherwise
perform). This repo stands up the UPSTREAM public llm-d-benchmark directly, not
a fork, so those source-level checks do not apply here and are dropped.

What is kept: the generic, cluster-state invariants that matter regardless of
which standup code runs, because they describe hazards this repo's own
Makefile already works around (see benchmark-standup's inline handling of the
prometheus-adapter-resource-reader ClusterRole, Makefile ~757-778) rather than
gates in a separate binary.

It is read-only. It performs no writes of any kind.

Namespace discipline: every kubectl call carries an explicit -n <namespace>,
including calls on cluster-scoped resources where kubectl accepts and ignores
it -- a net against this script misclassifying something as namespaced, not a
scope restriction (kubectl ignores -n for genuinely cluster-scoped writes).

Usage
-----
  python3 preflight_shared_cluster.py -n dhl-la-1708
  python3 preflight_shared_cluster.py -n dhl-la-1708 --report-only   # always exit 0

Exit status: 0 if every gating check passed, 1 otherwise.
"""
import argparse
import json
import subprocess
import sys

EXTERNAL_METRICS_APISERVICE = "v1beta1.external.metrics.k8s.io"
EXPECTED_METRICS_OWNER = "openshift-keda/keda-metrics-apiserver"

PROMETHEUS_ADAPTER_CLUSTERROLE = "prometheus-adapter-resource-reader"

# SCCs an OpenShift model-serving standup may grant to a workload ServiceAccount.
SCCS_TO_CHECK = ["anyuid", "privileged"]


class Report:
    def __init__(self):
        self.rows = []
        self.failed = 0

    def add(self, gating, ok, check, detail):
        if ok:
            status = "PASS"
        elif gating:
            status = "FAIL"
            self.failed += 1
        else:
            status = "WARN"
        self.rows.append((status, check, detail))

    def render(self):
        width = max((len(c) for _, c, _ in self.rows), default=0)
        for status, check, detail in self.rows:
            marker = {"PASS": "  ok  ", "FAIL": " FAIL ", "WARN": " warn "}[status]
            print(f"[{marker}] {check.ljust(width)}  {detail}")


def kubectl(namespace, *args):
    cmd = ["kubectl", *args, "-n", namespace]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    return proc.returncode == 0, proc.stdout.strip()


def get_json(namespace, *args):
    ok, out = kubectl(namespace, *args, "-o", "json")
    if not ok or not out:
        return None
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        return None


def check_namespace_exists(rep, ns):
    ok, out = kubectl(ns, "get", "namespace", ns, "--ignore-not-found", "-o", "name")
    rep.add(True, bool(ok and out), f"target namespace {ns} exists",
            out or "NOT FOUND -- wrong cluster, or the namespace was removed")


def check_prometheus_adapter_stub(rep, ns):
    """Precondition benchmark-standup's own inline logic depends on (Makefile
    ~757-778): it stubs/reuses this ClusterRole rather than force-installing
    prometheus-adapter, but only when the ClusterRole's helm-ownership
    annotations are readable. Surface the current state before standup runs."""
    obj = get_json(ns, "get", "clusterrole", PROMETHEUS_ADAPTER_CLUSTERROLE, "--ignore-not-found")
    if obj is None:
        rep.add(False, True, "prometheus-adapter-resource-reader",
                "not present -- benchmark-standup will create+annotate its own stub")
        return
    annotations = (obj.get("metadata", {}) or {}).get("annotations", {}) or {}
    release = annotations.get("meta.helm.sh/release-name")
    release_ns = annotations.get("meta.helm.sh/release-namespace")
    if release and release_ns:
        rep.add(True, True, "prometheus-adapter-resource-reader",
                f"owned by release {release!r} in {release_ns!r} -- "
                f"benchmark-standup will not touch it if that ns != this namespace")
    else:
        rep.add(True, False, "prometheus-adapter-resource-reader",
                "present but unannotated -- ownership cannot be determined; "
                "investigate before running benchmark-standup")


def check_metrics_apiservice_owner(rep, ns):
    obj = get_json(ns, "get", "apiservice", EXTERNAL_METRICS_APISERVICE, "--ignore-not-found")
    if obj is None:
        rep.add(True, False, "shared invariant: external metrics owner",
                f"apiservice/{EXTERNAL_METRICS_APISERVICE} NOT FOUND -- expected "
                f"{EXPECTED_METRICS_OWNER}; do not run a standup until this is understood")
        return
    svc = obj.get("spec", {}).get("service", {}) or {}
    owner = f"{svc.get('namespace')}/{svc.get('name')}"
    ok = owner == EXPECTED_METRICS_OWNER
    rep.add(True, ok, "shared invariant: external metrics owner",
            f"{EXTERNAL_METRICS_APISERVICE} -> {owner} (as expected)" if ok else
            f"{EXTERNAL_METRICS_APISERVICE} -> {owner}, expected {EXPECTED_METRICS_OWNER} "
            "-- ownership already moved; a standup could compound the damage")


def check_scc_users_clean(rep, ns):
    """Assert prior runs used the namespace-scoped SCC grant form (RoleBinding
    to system:openshift:scc:<scc>), not a cluster-wide `oc adm policy
    add-scc-to-user` that mutates the shared SCC object's .users list."""
    prefix = f"system:serviceaccount:{ns}:"
    for scc in SCCS_TO_CHECK:
        obj = get_json(ns, "get", "scc", scc, "--ignore-not-found")
        if obj is None:
            rep.add(False, False, f"shared invariant: scc/{scc} .users",
                    "could not read the SCC (insufficient rights, or not OpenShift) -- not gating")
            continue
        leaked = [u for u in (obj.get("users") or []) if u.startswith(prefix)]
        rep.add(True, not leaked, f"shared invariant: scc/{scc} .users",
                f"{ns} absent -- grants are namespace-scoped RoleBindings" if not leaked
                else f"LEAKED into cluster-global SCC: {leaked}")


def check_wva_clusterscope_rbac(rep, ns):
    """The install-methods.md hazard: a namespace-scoped install still creates
    ClusterRoleBindings (named wva-<base>-<hash> by this repo's kustomize
    overlay), and undeploying the wrong one can affect another install on the
    same cluster. There is no reliable label to select on -- this cluster has
    other teams' WVA installs from other repos/eras with no consistent
    labelling (confirmed live: asmalvan-, moabdi-, braulio-, lionel- prefixed
    CRBs coexist here) -- so this checks EVERY ClusterRoleBinding whose name
    starts with "wva-" and reports which namespace(s) they actually bind."""
    data = get_json(ns, "get", "clusterrolebinding")
    if data is None:
        rep.add(False, True, "shared invariant: WVA ClusterRoleBindings",
                "could not list (insufficient rights) -- not gating")
        return
    items = [i for i in (data.get("items") or [])
             if i["metadata"]["name"].startswith("wva-")]
    others = []
    for i in items:
        subs = i.get("subjects") or []
        sub_ns = {s.get("namespace") for s in subs if s.get("namespace")}
        if sub_ns and sub_ns != {ns}:
            others.append(f"{i['metadata']['name']} -> {sorted(sub_ns)}")
    # This cluster alone has 100+ other teams' wva-* CRBs (confirmed live) --
    # print a bounded sample, not the whole list, or this becomes unreadable.
    shown = others[:5]
    more = f" (+{len(others) - 5} more)" if len(others) > 5 else ""
    rep.add(False, not others, "shared invariant: WVA ClusterRoleBindings",
            f"{len(items)} wva-* found, all bind only {ns}" if not others else
            f"{len(others)} wva-* binding(s) bind a DIFFERENT namespace (other "
            f"teams' installs -- expected on this cluster; just don't undeploy "
            f"blindly): {shown}{more}")


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                  formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-n", "--namespace", required=True,
                    help="the ONLY namespace this run may touch")
    ap.add_argument("--report-only", action="store_true",
                    help="print the report but always exit 0")
    args = ap.parse_args()

    ns = args.namespace
    print(f"Shared-cluster pre-flight -- namespace {ns} (read-only, no writes)\n")

    rep = Report()
    check_namespace_exists(rep, ns)
    check_prometheus_adapter_stub(rep, ns)
    check_metrics_apiservice_owner(rep, ns)
    check_scc_users_clean(rep, ns)
    check_wva_clusterscope_rbac(rep, ns)
    rep.render()

    if rep.failed:
        print(f"\n{rep.failed} gating check(s) FAILED.")
        if args.report_only:
            print("(--report-only: exiting 0 anyway)")
            return 0
        return 1

    print("\nAll gating checks passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
