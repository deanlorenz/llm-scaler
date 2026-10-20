#!/usr/bin/env bash
#
# Executes the installer's handling of WVA_WATCH_NS as a LIST (deploy/lib)
# against a stubbed kubectl: one namespace-scoped controller, in a namespace of
# its own, managing several llm-d namespaces.
#
# Pinned here:
#   - `auto` resolves to every namespace with llm-d model servers or an
#     InferencePool, never the controller's own; a list is trimmed and
#     de-duplicated; a bad name, an empty `auto`, a list on a cluster-scoped
#     install and a list holding WVA_NS are each refused.
#   - the per-namespace checks run once per listed namespace, never against the
#     controller's, and a listed namespace with no llm-d fails.
#   - the prereqs phase writes the manager Role and RoleBinding in each listed
#     namespace and nowhere else, and the monitors go to each.
#   - the Kueue and policy-namespace reads are narrowed to exactly the listed
#     namespaces (plus wva-policy and the controller's own), never every Namespace.
#   - the ScaledObject plan scans each listed namespace.
#   - a namespace-scoped quota over a list is keyed `default` (one budget each);
#     WVA_QUOTA_SCOPE=cluster gives one budget for all of them.
#   - the uninstall removes the Role and RoleBinding in each namespace the live
#     Deployment names.
#   - a single WVA_WATCH_NS behaves as before.

set -u

# CHECK_ROOT runs the check against another tree -- how its negative control is
# taken: point it at an extracted older deploy/ and the cases must fail.
ROOT="${CHECK_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
FAIL=0
PASS=0
# Every case reports ok or FAIL exactly once; a run that stops early must not
# pass having checked less. Change this deliberately with the cases.
EXPECT_CASES=16

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh scaledobject.sh infra_monitoring.sh infra_wva.sh limiter_policy.sh cleanup.sh install_core.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done

for tool in jq yq; do
    command -v "$tool" >/dev/null 2>&1 || { printf 'FATAL: %s is required.\n' "$tool" >&2; exit 1; }
done

ok()   { PASS=$((PASS + 1)); echo "ok   $1"; }
fail() { FAIL=$((FAIL + 1)); echo "FAIL $1"; [ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/       /'; }

# Each case runs in a subshell that sources the libraries afresh, so one case's
# globals cannot leak into the next.
CALLS="$WORK/calls.log"
STUB="$WORK/stub.sh"
cat > "$STUB" <<'STUBEOF'
BLUE='' GREEN='' YELLOW='' RED='' NC=''
for lib in common.sh scaledobject.sh infra_monitoring.sh infra_wva.sh limiter_policy.sh cleanup.sh; do
    # shellcheck source=/dev/null
    . "$WORK/$lib"
done
#   MS_NS      namespaces whose Deployments carry llm-d.ai/role=decode
#   POOL_NS    namespaces with an InferencePool
#   LIVE_WATCH the live Deployment's WVA_WATCH_NAMESPACE
kubectl() {
    echo "kubectl $*" >> "$CALLS"
    case "$*" in
        "auth can-i list deployments -A") echo yes; return 0 ;;
        "auth can-i "*) echo yes; return 0 ;;
        "get deployments -A -o json")
            local ns items=""
            for ns in ${MS_NS:-}; do
                items="${items:+$items,}{\"metadata\":{\"name\":\"m\",\"namespace\":\"$ns\"},\"spec\":{\"template\":{\"metadata\":{\"labels\":{\"llm-d.ai/role\":\"decode\"}}}}}"
            done
            echo "{\"items\":[${items}]}"; return 0 ;;
        "get leaderworkersets -A -o json")
            echo 'error: the server does not have a resource type "leaderworkersets"' >&2; return 1 ;;
        "get inferencepools.inference.networking.k8s.io -A -o jsonpath="*)
            local ns
            for ns in ${POOL_NS:-}; do echo "$ns"; done; return 0 ;;
        "get inferencepools.inference.networking.x-k8s.io -A -o jsonpath="*)
            echo 'error: the server does not have a resource type' >&2; return 1 ;;
        "get namespace "*) return 0 ;;
        "kustomize "*)
            printf '%s\n' 'apiVersion: rbac.authorization.k8s.io/v1' 'kind: Role' 'metadata:' \
                '  name: wva-manager-role' '  namespace: wva-ctl' 'rules: []'
            return 0 ;;
        "apply -f -") cat >> "$WORK/applied.yaml"; echo "---" >> "$WORK/applied.yaml"; return 0 ;;
        "get deploy -n "*"-l app.kubernetes.io/name=workload-variant-autoscaler -o jsonpath="*)
            printf '%s' "${LIVE_WATCH:-}"; return 0 ;;
        "get deploy wva-controller-manager -n "*" -o jsonpath="*)
            printf '%s' "${LIVE_WATCH:-}"; return 0 ;;
    esac
    return 0
}
STUBEOF
export WORK CALLS STUB

# run <snippet>: sources the stub and the libs, then runs the snippet. Leaves
# out (stdout+stderr) and rc.
run() {
    : > "$CALLS"; : > "$WORK/applied.yaml"
    out="$(bash -c ". \"\$STUB\"; $1" 2>&1)"; rc=$?
}

# 1. auto finds every llm-d namespace, not the controller's own.
run 'MS_NS="team-a wva-ctl" POOL_NS="team-b"; WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS=auto; wva_resolve_watch_namespaces; echo "WATCH=$WVA_WATCH_NS SRC=$WVA_WATCH_NS_SOURCE"'
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -qx 'WATCH=team-a,team-b SRC=discovered'; then
    ok "auto resolves to team-a (model servers) and team-b (InferencePool), not the controller's namespace"
else
    fail "auto must resolve to team-a,team-b (rc=$rc)" "$out"
fi

# 2. A list is trimmed and de-duplicated.
run 'WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS=" team-a, team-b,team-a"; wva_resolve_watch_namespaces; echo "WATCH=$WVA_WATCH_NS"'
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -qx 'WATCH=team-a,team-b'; then
    ok "a list is trimmed and de-duplicated, in order"
else
    fail "a list must come out as team-a,team-b (rc=$rc)" "$out"
fi

# 3-6. Refusals.
run 'WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS="team-a,Team_B"; wva_resolve_watch_namespaces'
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q "'Team_B' is not a namespace name"; then
    ok "a name that is not a namespace name is refused"
else
    fail "Team_B must be refused (rc=$rc)" "$out"
fi
run 'MS_NS="" POOL_NS=""; WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS=auto; wva_resolve_watch_namespaces'
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'found no namespace with llm-d'; then
    ok "auto that finds nothing is refused, not installed managing nothing"
else
    fail "an empty auto must be refused (rc=$rc)" "$out"
fi
run 'WVA_NS=wva-ctl WVA_NS_EXPLICIT=wva-ctl WVA_SCOPE=cluster WVA_WATCH_NS="team-a,team-b"; wva_resolve_watch_namespaces'
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'only a namespace-scoped install takes'; then
    ok "a list on a cluster-scoped install is refused"
else
    fail "a list with WVA_SCOPE=cluster must be refused (rc=$rc)" "$out"
fi
run 'WVA_NS=team-a WVA_NS_EXPLICIT=team-a WVA_SCOPE=namespace WVA_WATCH_NS="team-a,team-b"; wva_resolve_watch_namespaces'
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'runs in a namespace of its own'; then
    ok "a list that holds the controller's own namespace is refused"
else
    fail "WVA_NS inside the list must be refused (rc=$rc)" "$out"
fi

# 7. The per-namespace checks run once per listed namespace, never the controller's.
CHECK_SNIPPET='MS_NS="team-a team-b"; WVA_NS=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS="team-a,team-b"
wva_serving_workload_count() { case " $MS_NS " in *" $1 "*) echo 1 ;; *) echo 0 ;; esac; }
wva_epp_services() { :; }
wva_report_modelserver_metrics() { echo "WATCH ms ${WVA_WATCH_NS}" >> "$CALLS"; return 0; }
wva_report_epp_flowcontrol() { echo "WATCH epp ${WVA_WATCH_NS}" >> "$CALLS"; return 0; }
wva_check_managed_namespaces'
run "$CHECK_SNIPPET"
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'Checking the 2 namespace(s) this controller manages' \
    && [ "$(grep -c '^WATCH ' "$CALLS")" -eq 4 ] && grep -qx 'WATCH epp team-b' "$CALLS" \
    && ! grep -q 'WATCH .* wva-ctl$' "$CALLS" && printf '%s' "$out" | grep -q 'Namespaces: all 2 passed'; then
    ok "both checks run once in each listed namespace, never the controller's, and both pass"
else
    fail "the checks must run per listed namespace (rc=$rc)" "$out
$(cat "$CALLS")"
fi

# 8. A listed namespace with no llm-d fails.
run "${CHECK_SNIPPET/MS_NS=\"team-a team-b\"/MS_NS=\"team-a\"}"
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'No llm-d found in team-b' \
    && printf '%s' "$out" | grep -q 'team-b: failed (no llm-d model servers)'; then
    ok "a listed namespace with no llm-d fails, with the namespace named"
else
    fail "team-b with no llm-d must fail (rc=$rc)" "$out"
fi

# 9. The Role and RoleBinding go to each listed namespace, and nowhere else.
run 'WVA_NS=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS="team-a,team-b"; wva_apply_watch_namespace_rbac "$WORK"'
applied="$(yq -r 'select(.kind != null) | .kind + " " + .metadata.namespace' "$WORK/applied.yaml" 2>/dev/null | grep -v '^---$' | sort | paste -sd, -)"
if [ "$rc" -eq 0 ] && [ "$applied" = "Role team-a,Role team-b,RoleBinding team-a,RoleBinding team-b" ]; then
    ok "the manager Role and RoleBinding are written in team-a and team-b only"
else
    fail "Role and RoleBinding must be written in each listed namespace only (rc=$rc, applied=$applied)" "$out"
fi

# 10. The monitors are looked after in each listed namespace.
run 'WVA_NS=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS="team-a,team-b"; wva_monitor_namespaces | paste -sd: -'
if [ "$rc" -eq 0 ] && [ "$(printf '%s' "$out" | tail -1)" = "team-a:team-b" ]; then
    ok "the monitors go to each listed namespace"
else
    fail "wva_monitor_namespaces must list team-a and team-b, one per line (rc=$rc)" "$out"
fi

# 11. The Kueue and policy-namespace reads are narrowed to the listed names.
run 'WVA_NS=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS="team-a,team-b"; k="$WORK/k.yaml"; : > "$k"
wva_append_kueue_reader_scope_patch "$k" wva-ctl $(wva_watch_words)
wva_append_policy_namespace_reader_scope_patch "$k" wva-ctl $(wva_watch_words) ""
cat "$k"'
kueue_names="$(printf '%s\n' "$out" | awk '/wva-kueue-reader-role/{exit} /^      - /{print $2}' | tr -d '"' | paste -sd, -)"
policy_names="$(printf '%s\n' "$out" | awk '/name: wva-kueue-reader-role/{f=1;next} f && /^      - /{print $2}' | tr -d '"' | sort | paste -sd, -)"
if [ "$rc" -eq 0 ] && [ "$kueue_names" = "team-a,team-b" ] && [ "$policy_names" = "team-a,team-b,wva-ctl,wva-policy" ]; then
    ok "the Namespace reads are narrowed to the listed namespaces (and wva-policy and its own), not every Namespace"
else
    fail "the reads must name exactly the listed namespaces (kueue=$kueue_names policy=$policy_names)" "$out"
fi

# 12. The ScaledObject plan scans each listed namespace.
run 'WVA_NS=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS="team-a,team-b"; so_target_namespaces | paste -sd: -'
if [ "$rc" -eq 0 ] && [ "$(printf '%s' "$out" | tail -1)" = "team-a:team-b" ]; then
    ok "the ScaledObject plan scans team-a and team-b"
else
    fail "so_target_namespaces must give team-a and team-b, one per line (rc=$rc)" "$out"
fi

# 13-14. The quota over a list: one budget each by default, one for all with scope cluster.
run 'WVA_NS=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS="team-a,team-b" WVA_QUOTAS="H100=4"; limiter_entry_yaml quota'
if [ "$rc" -eq 0 ] && printf '%s\n' "$out" | grep -qx '    default:' && ! printf '%s' "$out" | grep -q 'team-a,team-b'; then
    ok "a namespace-scoped quota over a list is keyed default: one budget per namespace"
else
    fail "the namespace quota must be keyed default, never on the list (rc=$rc)" "$out"
fi
run 'WVA_NS=wva-ctl WVA_SCOPE=namespace WVA_WATCH_NS="team-a,team-b" WVA_QUOTAS="H100=4" WVA_QUOTA_SCOPE=cluster; limiter_entry_yaml quota'
if [ "$rc" -eq 0 ] && printf '%s\n' "$out" | grep -qx '  scope: cluster' && printf '%s\n' "$out" | grep -qx '    H100: 4'; then
    ok "WVA_QUOTA_SCOPE=cluster gives one budget for every listed namespace"
else
    fail "the cluster quota must carry H100: 4 under scope: cluster (rc=$rc)" "$out"
fi

# 15. The uninstall removes the grant in each namespace the live Deployment names.
run 'LIVE_WATCH="team-a,team-b"; WVA_NS=wva-ctl WVA_SCOPE=namespace; wva_undeploy_watch_namespace_rbac; echo "WATCH=$WVA_WATCH_NS"'
dels="$(grep -E '^kubectl delete (role|rolebinding) ' "$CALLS" | awk '{print $3" "$6}' | sort | paste -sd, -)"
if [ "$rc" -eq 0 ] && [ "$dels" = "role team-a,role team-b,rolebinding team-a,rolebinding team-b" ] \
    && printf '%s' "$out" | grep -qx 'WATCH=team-a,team-b'; then
    ok "the uninstall removes the Role and RoleBinding in each namespace the controller managed"
else
    fail "the uninstall must delete in team-a and team-b (rc=$rc, deletes=$dels)" "$out
$(cat "$CALLS")"
fi

# 16. A single WVA_WATCH_NS is unchanged: not a list, keyed on itself.
run 'WVA_NS=wva-team-a WVA_SCOPE=namespace WVA_WATCH_NS=team-a WVA_QUOTAS="H100=4"; wva_resolve_watch_namespaces; wva_watch_is_multi && echo MULTI; limiter_entry_yaml quota'
if [ "$rc" -eq 0 ] && ! printf '%s' "$out" | grep -qx MULTI && printf '%s\n' "$out" | grep -qx '    team-a:'; then
    ok "a single WVA_WATCH_NS is not a list, and its quota is keyed on it as before"
else
    fail "a single WVA_WATCH_NS must behave as before (rc=$rc)" "$out"
fi

total=$((PASS + FAIL))
echo ""
echo "$PASS passed, $FAIL failed ($total of $EXPECT_CASES cases)"
if [ "$total" -ne "$EXPECT_CASES" ]; then
    echo "FAIL expected $EXPECT_CASES cases, ran $total"
    exit 1
fi
[ "$FAIL" -eq 0 ]
