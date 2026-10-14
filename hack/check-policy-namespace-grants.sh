#!/usr/bin/env bash
#
# Asserts that every namespace-scoped controller is granted what it needs to
# FIND and READ a published cluster policy, by executing the grant functions
# against a stubbed kubectl.
#
# Two bugs, both silent:
#
#   A. A namespace-scoped controller holds only a Role, which cannot reach a
#      cluster-scoped Namespace. Its lookup of the well-known `wva-policy`
#      namespace was Forbidden, the controller read that as "absent", and ran on
#      its own ConfigMap -- while `make enable-physical-limiter` reported the
#      cluster bounded. Fixed by a Namespace `get` grant: the
#      policy-namespace-reader component (prereqs phase) and
#      pl_grant_namespace_read (enable-physical-limiter), plus a restart for a
#      controller that could not see the namespace when it started.
#   B. A controller installed AFTER the policy was published resolved to it but
#      had no RoleBinding there, and refused to start. Fixed by the prereqs phase
#      granting the read whenever the policy namespace already exists.
#
# What is asserted is what the functions would APPLY, not what their source
# says.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAIL=0
CASES=0

# Sourced through CR-stripped copies, as hack/check-limiter-declaration.sh does:
# a Windows checkout can write these as CRLF, and bash then stops at the first
# function definition.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh limiter_policy.sh physical_limiter.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done
BLUE=''; GREEN=''; YELLOW=''; RED=''; NC=''
unset WVA_QUOTAS WVA_QUOTA_SCOPE WVA_QUOTA_KUEUE WVA_SCOPE WVA_WATCH_NS WVA_LIMITER WVA_LIMITER_TYPE WVA_LIMITER_TARGETS
WVA_NS="wva-system"
WVA_POLICY_NS="wva-policy"
# shellcheck disable=SC1090
. "$WORK/common.sh"
# shellcheck disable=SC1090
. "$WORK/limiter_policy.sh"
# shellcheck disable=SC1090
. "$WORK/physical_limiter.sh"

for tool in yq jq; do
    command -v "$tool" >/dev/null 2>&1 || {
        echo "FATAL: $tool is required by this check, and is missing. Nothing below would be about the code."
        exit 2
    }
done

case_begin() { CASES=$((CASES + 1)); CASE_FAIL_AT="$FAIL"; }
fail() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }
ok()   { [ "$FAIL" -eq "${CASE_FAIL_AT:-0}" ] || return 0; echo "ok   $*"; }

# ---------------------------------------------------------------------------
# The stub. Every invocation is recorded as CALL[...]; every `apply -f -` also
# records the manifest it was given between APPLY<< and >>APPLY.
# ---------------------------------------------------------------------------
STUB="$WORK/stub"; mkdir -p "$STUB"
cat > "$STUB/kubectl" <<'STUBEOF'
#!/usr/bin/env bash
printf 'CALL[%s]\n' "$*" >> "$KLOG"
case "$*" in
  "auth can-i create clusterrolebindings"*) echo yes; exit 0 ;;
  "auth can-i get namespaces/"*)
      if [ "${CANI_NS:-no}" = yes ]; then echo yes; exit 0; fi
      echo no; exit 1 ;;
  "apply -f -"*)
      { echo "APPLY<<"; cat; echo ">>APPLY"; } >> "$KLOG"; exit 0 ;;
  "get namespace "*)
      name="$3"
      case "$*" in
        *'policy-namespace'*)
            if [ -n "${MANAGED_GET_ERR:-}" ]; then echo "$MANAGED_GET_ERR" >&2; exit 1; fi
            printf '%s' "${NS_LABEL:-}"; exit 0 ;;
        *'creationTimestamp'*) printf '2026-01-01T00:00:00Z'; exit 0 ;;
      esac
      case " ${EXISTING_NS:-} " in
        *" $name "*) echo "namespace/$name"; exit 0 ;;
      esac
      if [ -n "${NS_GET_ERR:-}" ]; then echo "$NS_GET_ERR" >&2; exit 1; fi
      echo "Error from server (NotFound): namespaces \"$name\" not found" >&2; exit 1 ;;
  "get deploy -A"*) printf '%s\n' "${DEPLOY_NS_LIST:-}"; exit 0 ;;
  # By label, and (since enable-physical-limiter verifies its targets) by the
  # verified Deployment's name.
  "get deploy -n "*|"get deploy wva-controller-manager -n "*)
      case "$*" in
        # The verification read of enable-physical-limiter: one genuine controller,
        # the controller account and image, so the grants below are reached.
        *' -o json')   # exactly -o json: '-o jsonpath=' reads go to the cases below
            printf '%s' '{"items":[{"metadata":{"name":"wva-controller-manager"},"spec":{"template":{"spec":{"serviceAccountName":"wva-controller-manager","containers":[{"name":"manager","image":"ghcr.io/ev-shindin/llm-scaling-manager:main"}]}}}}]}' ;;
        *'.args}'*) printf '%s' '["--watch-namespace=$(WVA_WATCH_NAMESPACE)"]' ;;
        *'WVA_WATCH_NAMESPACE'*) printf '%s' '$(POD_NAMESPACE)' ;;
        *'serviceAccountName'*) printf 'wva-controller-manager' ;;
      esac
      exit 0 ;;
  "get pods -n "*) printf '2026-02-01T00:00:00Z'; exit 0 ;;
  "get configmap"*)
      echo 'Error from server (NotFound): configmaps "wva-scaling-policy-config" not found' >&2; exit 1 ;;
  "create "*"--dry-run=client"*)
      printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: stub\n'; exit 0 ;;
  "label --local"*) cat; exit 0 ;;
esac
exit 0
STUBEOF
chmod +x "$STUB/kubectl"

# run <function and args...> -- in a SUBSHELL (log_error exits), stub first on
# PATH. Leaves the call log in $LOG, stderr+stdout in $OUT, status in $RC.
run() {
    LOG="$WORK/log"; : > "$LOG"
    (
        PATH="$STUB:$PATH"; export PATH
        KLOG="$LOG"; export KLOG
        export CANI_NS NS_LABEL EXISTING_NS NS_GET_ERR MANAGED_GET_ERR DEPLOY_NS_LIST
        "$@"
    ) >"$WORK/out" 2>&1
    RC=$?
    OUT="$(cat "$WORK/out")"
}

# applied_doc <kind> <name> -- the applied manifest of that kind and name, as
# JSON, or nothing.
applied_doc() {
    awk '/^APPLY<<$/{on=1; print "---"; next} /^>>APPLY$/{on=0; next} on' "$LOG" \
        | yq -o=json -I=0 "select(.kind == \"$1\" and .metadata.name == \"$2\")" 2>/dev/null
}

SUFFIX_A="$(wva_ns_suffix tenant-a)"

# ---------------------------------------------------------------------------
# The component every namespace-scoped install applies in the prereqs phase.
# ---------------------------------------------------------------------------
case_begin
role_file="$ROOT/config/components/policy-namespace-reader/policy-namespace-reader-clusterrole.yaml"
if [ ! -f "$role_file" ]; then
    fail "config/components/policy-namespace-reader is missing: no namespace-scoped controller can read the Namespace that holds cluster policy"
else
    rules="$(tr -d '\r' < "$role_file" | yq -o=json -I=0 '.rules')"
    [ "$(printf '%s' "$rules" | jq -r '[.[] | select(.resources | index("namespaces")) | .verbs[]] | index("get") != null')" = "true" ] \
        || fail "the policy-namespace-reader ClusterRole does not grant get on namespaces: $rules"
    [ "$(printf '%s' "$rules" | jq -r '[.[].resources[]] | unique | join(",")')" = "namespaces" ] \
        || fail "the policy-namespace-reader ClusterRole grants more than namespaces: $rules"
    ok "the policy-namespace-reader ClusterRole grants get on namespaces, and nothing else"
fi

for platform in kubernetes openshift; do
    case_begin
    if tr -d '\r' < "$ROOT/config/overlays/namespace-scoped/$platform/kustomization.yaml" \
        | yq -e '.components[] | select(. == "../../../components/policy-namespace-reader/")' >/dev/null 2>&1; then
        ok "the namespace-scoped $platform overlay includes the policy-namespace-reader component"
    else
        fail "the namespace-scoped $platform overlay does not include components/policy-namespace-reader"
    fi
done

# Renamed per install, or two installs share one binding and the second apply
# replaces the first one's subject.
case_begin
printf '%s\n' "${WVA_SHARED_CLUSTER_ROLE_BINDINGS[@]}" | grep -qx 'wva-policy-namespace-reader-rolebinding' \
    || fail "wva-policy-namespace-reader-rolebinding is not renamed per install (WVA_SHARED_CLUSTER_ROLE_BINDINGS)"
printf '%s\n' "${WVA_OWNED_CLUSTER_ROLES[@]}" | grep -qx 'wva-policy-namespace-reader-role:wva-policy-namespace-reader-rolebinding' \
    || fail "wva-policy-namespace-reader-role is not renamed per install (WVA_OWNED_CLUSTER_ROLES)"
ok "the policy-namespace-reader role and binding are renamed per install"

# ---------------------------------------------------------------------------
# Bug B: the prereqs phase grants the read of a policy published earlier.
# ---------------------------------------------------------------------------
if ! declare -F pl_grant_published_policy_read >/dev/null; then
    case_begin
    fail "pl_grant_published_policy_read is not defined: a controller installed after the policy was published gets no read of it, and refuses to start"
else
    case_begin
    CANI_NS=no NS_LABEL='' EXISTING_NS='wva-policy' NS_GET_ERR='' MANAGED_GET_ERR='' \
        run pl_grant_published_policy_read tenant-a tenant-a
    rb="$(applied_doc RoleBinding "wva-policy-reader-$SUFFIX_A")"
    if [ "$RC" -ne 0 ]; then
        fail "pl_grant_published_policy_read failed: $OUT"
    elif [ -z "$rb" ]; then
        fail "a self-managed controller installed after wva-policy exists got no RoleBinding there: $(cat "$LOG")"
    else
        [ "$(printf '%s' "$rb" | jq -r '.metadata.namespace')" = "wva-policy" ] \
            || fail "the policy-read RoleBinding is not in wva-policy: $rb"
        [ "$(printf '%s' "$rb" | jq -r '.subjects[0].namespace + "/" + .subjects[0].name')" = "tenant-a/wva-controller-manager" ] \
            || fail "the policy-read RoleBinding binds the wrong subject: $rb"
        [ -n "$(applied_doc Role wva-policy-reader)" ] \
            || fail "the wva-policy-reader Role the binding references was not applied"
        ok "a controller installed after wva-policy exists is granted its read"
    fi

    case_begin
    CANI_NS=no NS_LABEL='' EXISTING_NS='' NS_GET_ERR='' MANAGED_GET_ERR='' \
        run pl_grant_published_policy_read tenant-a tenant-a
    if [ "$RC" -ne 0 ]; then
        fail "pl_grant_published_policy_read failed when no policy namespace exists: $OUT"
    elif grep -q '^APPLY<<' "$LOG"; then
        fail "a RoleBinding was applied into a policy namespace that does not exist: $(cat "$LOG")"
    else
        ok "no policy namespace yet: nothing is granted, and the install goes on"
    fi

    case_begin
    CANI_NS=no NS_LABEL='platform-policy' EXISTING_NS='platform-policy wva-policy' NS_GET_ERR='' MANAGED_GET_ERR='' \
        run pl_grant_published_policy_read tenant-a tenant-a
    rb="$(applied_doc RoleBinding "wva-policy-reader-$SUFFIX_A")"
    if [ "$(printf '%s' "$rb" | jq -r '.metadata.namespace // ""')" = "platform-policy" ]; then
        ok "the wva.llmd.ai/policy-namespace label wins over wva-policy, as it does in the controller"
    else
        fail "the label on the managed namespace was not followed: $(cat "$LOG")"
    fi

    # An admin-owned controller keeps its own ConfigMap: nothing to grant, and
    # nothing to ask the cluster.
    case_begin
    CANI_NS=no NS_LABEL='' EXISTING_NS='wva-policy' NS_GET_ERR='' MANAGED_GET_ERR='' \
        run pl_grant_published_policy_read wva-system team-b
    if grep -q '^CALL' "$LOG"; then
        fail "an admin-owned controller (manages team-b, runs in wva-system) was granted policy reads: $(cat "$LOG")"
    else
        ok "an admin-owned controller is left alone"
    fi

    # Forbidden is not absent: no grant, but a warning that says so.
    case_begin
    CANI_NS=no NS_LABEL='' EXISTING_NS='' MANAGED_GET_ERR='' \
        NS_GET_ERR='Error from server (Forbidden): namespaces "wva-policy" is forbidden' \
        run pl_grant_published_policy_read tenant-a tenant-a
    if [ "$RC" -ne 0 ]; then
        fail "a Forbidden existence check aborted the install: $OUT"
    elif grep -q '^APPLY<<' "$LOG"; then
        fail "a grant was applied although the namespace's existence was never established"
    elif ! printf '%s' "$OUT" | grep -q 'Could not tell whether'; then
        fail "a Forbidden existence check was reported as an absent policy namespace: $OUT"
    else
        ok "a Forbidden existence check is reported, not read as absent"
    fi
fi

# ---------------------------------------------------------------------------
# Bug A: enable-physical-limiter grants every self-managed controller the
# Namespace get, and restarts the ones that could not see the policy namespace.
# ---------------------------------------------------------------------------
case_begin
CANI_NS=no NS_LABEL='' EXISTING_NS='wva-policy' NS_GET_ERR='' MANAGED_GET_ERR='' DEPLOY_NS_LIST='tenant-a' \
    run enable_physical_limiter
crole="$(applied_doc ClusterRole "wva-policy-namespace-reader-role-$SUFFIX_A")"
crb="$(applied_doc ClusterRoleBinding "wva-policy-namespace-reader-rolebinding-$SUFFIX_A")"
if [ "$RC" -ne 0 ]; then
    fail "enable_physical_limiter failed under the stub: $OUT"
elif [ -z "$crole" ] || [ -z "$crb" ]; then
    fail "enable_physical_limiter did not grant tenant-a get on namespaces, so on Kubernetes it cannot see wva-policy and the published limiter never applies to it"
else
    [ "$(printf '%s' "$crole" | jq -r '.rules[0].resources[0] + ":" + (.rules[0].verbs | join(","))')" = "namespaces:get" ] \
        || fail "the namespace-read ClusterRole does not grant exactly get on namespaces: $crole"
    [ "$(printf '%s' "$crb" | jq -r '.subjects[0].namespace + "/" + .subjects[0].name')" = "tenant-a/wva-controller-manager" ] \
        || fail "the namespace-read binding names the wrong subject: $crb"
    [ "$(printf '%s' "$crb" | jq -r '.roleRef.name')" = "wva-policy-namespace-reader-role-$SUFFIX_A" ] \
        || fail "the namespace-read binding points at a ClusterRole that was not created: $crb"
    ok "enable_physical_limiter grants a self-managed controller get on namespaces"
fi

case_begin
# By name: enable-physical-limiter restarts the Deployment it verified, never
# whatever else carries the controller label.
if grep -q '^CALL\[rollout restart deploy wva-controller-manager -n tenant-a' "$LOG"; then
    ok "a controller that could not see wva-policy is restarted, so it re-resolves its policy source"
else
    fail "a controller that could not read namespace wva-policy was granted it but not restarted; it keeps its own ConfigMap until something restarts it: $(grep '^CALL' "$LOG")"
fi

case_begin
CANI_NS=yes NS_LABEL='' EXISTING_NS='wva-policy' NS_GET_ERR='' MANAGED_GET_ERR='' DEPLOY_NS_LIST='tenant-a' \
    run enable_physical_limiter
if [ "$RC" -ne 0 ]; then
    fail "enable_physical_limiter failed under the stub: $OUT"
elif grep -q '^CALL\[rollout restart' "$LOG"; then
    fail "a controller that could already see wva-policy, and started after it existed, was restarted for nothing"
else
    ok "a controller that could already see wva-policy is not restarted"
fi

# ---------------------------------------------------------------------------
# The prereqs phase calls the grant. A function nothing calls fixes nothing.
# ---------------------------------------------------------------------------
case_begin
tr -d '\r' < "$ROOT/deploy/install.sh" | grep -qE '^source "\$DEPLOY_LIB_DIR/physical_limiter.sh"' \
    || fail "deploy/install.sh does not source physical_limiter.sh, so the prereqs phase cannot grant the policy read"
tr -d '\r' < "$ROOT/deploy/lib/install_core.sh" | grep -qE '^[[:space:]]+pl_grant_published_policy_read ' \
    || fail "install_core.sh never calls pl_grant_published_policy_read"
ok "the prereqs phase calls the policy-read grant"

CASES_EXPECTED=13
case_begin
if [ "$CASES" -ne "$((CASES_EXPECTED + 1))" ]; then
    fail "$((CASES - 1)) cases ran, not $CASES_EXPECTED. Update CASES_EXPECTED deliberately."
else
    ok "all $CASES_EXPECTED cases ran"
fi

if [ "$FAIL" -ne 0 ]; then
    echo "policy namespace grants check FAILED"
    exit 1
fi
echo "policy namespace grants OK"
