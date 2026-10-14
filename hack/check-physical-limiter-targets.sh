#!/usr/bin/env bash
#
# Executes enable_physical_limiter (deploy/lib/physical_limiter.sh) against a
# stubbed kubectl, and asserts WHO it grants cluster-wide node read to.
#
# It exists because the answer used to be "whatever any labelled Deployment
# names". The command found controllers by the label
# app.kubernetes.io/name=workload-variant-autoscaler, which anyone who can create
# a Deployment can set, and bound the ServiceAccount that Deployment named --
# `default`, say, which every pod in that namespace runs as. A cluster-admin
# command then handed a tenant-chosen account node read across the cluster, and
# reported it as "granted".
#
# What is asserted is the bindings the command would APPLY, read back from the
# manifests the stub receives -- not the source, and not the log line.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAIL=0
CASES=0

for tool in yq jq; do
    command -v "$tool" >/dev/null 2>&1 || {
        echo "FATAL: $tool is required by this check, and is missing. Nothing below would be about the code."
        exit 2
    }
done

# CR-stripped copies, for the reason hack/check-limiter-declaration.sh gives.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh limiter_policy.sh physical_limiter.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done
BLUE=''; GREEN=''; YELLOW=''; RED=''; NC=''
unset WVA_QUOTAS WVA_QUOTA_SCOPE WVA_QUOTA_KUEUE WVA_SCOPE WVA_WATCH_NS WVA_LIMITER WVA_LIMITER_TYPE \
    WVA_LIMITER_TARGETS WVA_CONTROLLER_SA WVA_CONTROLLER_IMAGE_REPOS WVA_IMAGE_REPO
WVA_NS="wva-system"
WVA_POLICY_NS="wva-policy"
# shellcheck disable=SC1090
. "$WORK/common.sh"
# shellcheck disable=SC1090
. "$WORK/limiter_policy.sh"
# shellcheck disable=SC1090
. "$WORK/physical_limiter.sh"

case_begin() { CASES=$((CASES + 1)); CASE_FAIL_AT="$FAIL"; }
fail() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }
ok()   { [ "$FAIL" -eq "${CASE_FAIL_AT:-0}" ] || return 0; echo "ok   $*"; }

# ---------------------------------------------------------------------------
# The stub. Deployments come from $KDEPLOYS/<namespace>.json; a namespace named
# in $KFAIL_NS cannot be read. Every applied manifest is reduced to one GRANT
# line per binding: `<binding kind> <binding name> <namespace>/<account>`.
# ---------------------------------------------------------------------------
STUB="$WORK/stub"; mkdir -p "$STUB"
cat > "$STUB/kubectl" <<'STUBEOF'
#!/usr/bin/env bash
printf 'CALL[%s]\n' "$*" >> "$KCALLS"
# Parsed, not glob-matched: `-o json` is a prefix of `-o jsonpath=...`, and a
# pattern on the joined string answered every jsonpath read with the whole list.
ns=""; out=""; prev=""; name=""; pos=0
for a in "$@"; do
  case "$prev" in
    -n) ns="$a" ;;
    -o) out="$a" ;;
    -l) : ;;
    *) case "$a" in
         -*) : ;;
         *) pos=$((pos + 1)); [ "$pos" -eq 3 ] && name="$a" ;;   # get deploy NAME
       esac ;;
  esac
  case "$a" in -o*=*) out="${a#-o}" ;; esac
  prev="$a"
done
deploys() {
  case " ${KFAIL_NS:-} " in *" $ns "*) exit 1 ;; esac
  if [ -f "$KDEPLOYS/$ns.json" ]; then cat "$KDEPLOYS/$ns.json"; else printf '{"items":[]}'; fi
}
case "$1" in
  auth) exit 0 ;;
  apply)
    doc="$(cat)"
    printf '%s\n' "$doc" | yq -N e 'select(.kind == "ClusterRoleBinding" or .kind == "RoleBinding") | .kind + " " + .metadata.name + " " + .subjects[0].namespace + "/" + .subjects[0].name' - \
      | sed 's/^/GRANT /' >> "$KGRANTS"
    exit 0 ;;
  get)
    case "$2" in
      deploy)
        if [ "$*" != "${*#*-A}" ]; then printf '%s\n' "${KDEPLOY_NS_LIST:-}"; exit 0; fi
        case "$out" in
          json) deploys ;;
          jsonpath=*)
            # A plain field path is evaluated against the fixture, so the code
            # under test reads what a cluster would answer. Filter expressions
            # (the WVA_WATCH_NAMESPACE env lookup) answer empty: no fixture
            # sets one.
            path="${out#jsonpath=}"; path="${path#\{}"; path="${path%\}}"
            case "$path" in *"[?("*) exit 0 ;; esac
            if [ -n "$name" ]; then
              deploys | jq -r --arg n "$name" ".items[] | select(.metadata.name == \$n) | ${path} // empty"
            else
              deploys | jq -r "${path} // empty"
            fi ;;
        esac ;;
      configmap) printf 'scaleUpThreshold: 0.85' ;;
      *) : ;;
    esac ;;
  *) : ;;
esac
exit 0
STUBEOF
chmod +x "$STUB/kubectl"

DEPLOYS="$WORK/deploys"; mkdir -p "$DEPLOYS"

# deploy_json <name> <serviceaccount|-> <manager image|-> [<other container name>]
# One Deployment object. `-` omits the field, so an absent ServiceAccount and an
# absent manager container are both expressible.
deploy_json() {
    local name="$1" sa="$2" image="$3" cname="${4:-manager}"
    jq -n --arg name "$name" --arg sa "$sa" --arg image "$image" --arg cname "$cname" '
        {metadata: {name: $name, labels: {"app.kubernetes.io/name": "workload-variant-autoscaler"}},
         spec: {template: {spec: (
            (if $sa == "-" then {} else {serviceAccountName: $sa} end)
            + {containers: [{name: $cname, image: (if $image == "-" then "busybox" else $image end)}]}
         )}}}'
}
# put_ns <namespace> <deployment json>... -- the namespace fixture.
put_ns() {
    local ns="$1"; shift
    printf '%s\n' "$@" | jq -s '{apiVersion: "v1", kind: "List", items: .}' > "$DEPLOYS/$ns.json"
}

IMG="ghcr.io/ev-shindin/llm-scaling-manager:main"
put_ns tenant-a "$(deploy_json wva-controller-manager wva-controller-manager "$IMG")"
# The attack: the label, the real image, the namespace default account.
put_ns tenant-b "$(deploy_json impostor default "$IMG")"
# The controller account, but someone else's image.
put_ns tenant-c "$(deploy_json lookalike wva-controller-manager "quay.io/someone/anything:v1")"
# The controller account and image, but no `manager` container.
put_ns tenant-d "$(deploy_json sidecar wva-controller-manager "$IMG" app)"
# A genuine controller and an impostor side by side, impostor first: the old
# code read `.items[0]` and would have bound the impostor account.
put_ns tenant-e "$(deploy_json aaa-impostor tenant-admin "$IMG")" \
                "$(deploy_json wva-controller-manager wva-controller-manager "$IMG")"
# A pinned digest and a registry port must still be recognised as the repo.
put_ns tenant-f "$(deploy_json wva-controller-manager wva-controller-manager "ghcr.io/ev-shindin/llm-scaling-manager:v1.2@sha256:abc")"

# run_enable [VAR=value ...] -- enable_physical_limiter in a subshell, the stub
# first on PATH. Output in $WORK/out, grants in $GRANTS, status in $RC.
run_enable() {
    GRANTS="$WORK/grants"; : > "$GRANTS"
    CALLS="$WORK/calls"; : > "$CALLS"
    (
        PATH="$STUB:$PATH"; export PATH
        KGRANTS="$GRANTS" KCALLS="$CALLS" KDEPLOYS="$DEPLOYS"
        export KGRANTS KCALLS KDEPLOYS
        KDEPLOY_NS_LIST="tenant-a tenant-b tenant-c tenant-d tenant-e tenant-f"
        export KDEPLOY_NS_LIST
        KDEPLOY_NS_LIST="$(printf '%s\n' $KDEPLOY_NS_LIST)"
        for kv in "$@"; do export "${kv?}"; done
        enable_physical_limiter
    ) >"$WORK/out" 2>&1
    RC=$?
}
granted_to() { grep -q "^GRANT ClusterRoleBinding wva-node-reader-rolebinding-[^ ]* $1\$" "$GRANTS"; }
any_grant_in() { grep -q "^GRANT [A-Za-z]* [^ ]* $1/" "$GRANTS"; }

# ---------------------------------------------------------------------------
case_begin
run_enable
if [ "$RC" -ne 0 ]; then
    fail "enable_physical_limiter failed on a cluster where everything answers: $(tail -15 "$WORK/out")"
elif ! granted_to tenant-a/wva-controller-manager; then
    fail "the genuine controller in tenant-a was not granted node read: $(cat "$GRANTS")"
else
    ok "a genuine controller is granted node read"
fi

case_begin
if any_grant_in tenant-b; then
    fail "a labelled Deployment running as tenant-b/default was GRANTED -- a tenant-chosen account now reads every node: $(grep tenant-b "$GRANTS")"
elif ! grep -q 'SKIPPED tenant-b/impostor' "$WORK/out"; then
    fail "the impostor in tenant-b was not granted but not named either; an admin cannot tell it was skipped: $(tail -15 "$WORK/out")"
else
    ok "a labelled impostor with another ServiceAccount is skipped, by name, and granted nothing"
fi

case_begin
if any_grant_in tenant-c; then
    fail "a Deployment running a foreign image was granted: $(grep tenant-c "$GRANTS")"
elif ! grep -q 'SKIPPED tenant-c/lookalike' "$WORK/out"; then
    fail "the foreign-image Deployment was not named as skipped: $(tail -15 "$WORK/out")"
else
    ok "the controller account on a foreign image is skipped"
fi

case_begin
if any_grant_in tenant-d; then
    fail "a Deployment with no manager container was granted: $(grep tenant-d "$GRANTS")"
elif ! grep -q 'SKIPPED tenant-d/sidecar' "$WORK/out"; then
    fail "the Deployment with no manager container was not named as skipped: $(tail -15 "$WORK/out")"
else
    ok "a Deployment with no manager container is skipped"
fi

case_begin
if grep -q 'tenant-e/tenant-admin' "$GRANTS"; then
    fail "the impostor listed first in tenant-e had its account granted: $(grep tenant-e "$GRANTS")"
elif ! granted_to tenant-e/wva-controller-manager; then
    fail "the genuine controller beside an impostor was not granted: $(cat "$GRANTS")"
else
    ok "beside an impostor, only the genuine controller is granted"
fi

case_begin
if ! granted_to tenant-f/wva-controller-manager; then
    fail "a controller image pinned by tag and digest was not recognised: $(tail -15 "$WORK/out")"
else
    ok "an image pinned by tag and digest is recognised by its repository"
fi

case_begin
listed_at="$(grep -n 'Controllers to be granted' "$WORK/out" | head -1 | cut -d: -f1)"
first_grant="$(grep -n 'CALL\[apply' "$CALLS" | head -1 | cut -d: -f1)"
if [ -z "$listed_at" ]; then
    fail "the command did not print the controllers it is about to grant: $(tail -15 "$WORK/out")"
elif ! sed -n "${listed_at},\$p" "$WORK/out" | grep -q 'tenant-a/wva-controller-manager as ServiceAccount wva-controller-manager'; then
    fail "the printed list does not name the genuine controller: $(tail -15 "$WORK/out")"
elif sed -n "${listed_at},/Publishing/p" "$WORK/out" | grep -q 'impostor as'; then
    fail "the printed list names the impostor as one to be granted: $(tail -15 "$WORK/out")"
elif [ -z "$first_grant" ]; then
    fail "no apply reached the stub at all; this case can no longer tell before from after"
else
    ok "the final list is printed, and holds only verified controllers"
fi

# ---------------------------------------------------------------------------
# WVA_LIMITER_TARGETS is an allowlist of namespaces, not of Deployments: naming
# the impostor namespace does not make its Deployment genuine.
case_begin
run_enable WVA_LIMITER_TARGETS=tenant-b
if [ "$RC" -ne 0 ]; then
    fail "enable with WVA_LIMITER_TARGETS=tenant-b failed: $(tail -15 "$WORK/out")"
elif any_grant_in tenant-b; then
    fail "naming tenant-b in WVA_LIMITER_TARGETS granted its impostor: $(cat "$GRANTS")"
elif any_grant_in tenant-a; then
    fail "WVA_LIMITER_TARGETS=tenant-b still granted tenant-a; it is no longer an allowlist: $(cat "$GRANTS")"
else
    ok "WVA_LIMITER_TARGETS does not turn an impostor into a controller, and limits the grant to the named namespaces"
fi

# A named namespace with no controller yet gets the controller account and
# nothing else -- the install about to happen there runs as it.
case_begin
run_enable WVA_LIMITER_TARGETS=tenant-new
if [ "$RC" -ne 0 ]; then
    fail "enable with a not-yet-installed target failed: $(tail -15 "$WORK/out")"
elif ! granted_to tenant-new/wva-controller-manager; then
    fail "a named namespace with no controller yet was not prepared for one: $(cat "$GRANTS")"
else
    ok "a named namespace with no controller yet is granted only the controller account"
fi

# A mirror is accepted when named, and the account can be renamed.
case_begin
run_enable WVA_CONTROLLER_IMAGE_REPOS=quay.io/someone/anything
if ! granted_to tenant-c/wva-controller-manager; then
    fail "WVA_CONTROLLER_IMAGE_REPOS did not admit the mirror it names: $(tail -15 "$WORK/out")"
elif any_grant_in tenant-a; then
    fail "WVA_CONTROLLER_IMAGE_REPOS did not REPLACE the default repository list: $(cat "$GRANTS")"
else
    ok "WVA_CONTROLLER_IMAGE_REPOS names the repositories a controller may run from"
fi

# ---------------------------------------------------------------------------
# A namespace that cannot be read is a refusal, not a skip: the command cannot
# know what it is leaving ungranted.
case_begin
run_enable KFAIL_NS=tenant-c
if [ "$RC" -eq 0 ]; then
    fail "enable exited 0 having failed to read a controller namespace: $(tail -15 "$WORK/out")"
elif grep -q 'in force' "$WORK/out"; then
    fail "enable announced the limiter after failing to read a controller namespace: $(tail -15 "$WORK/out")"
elif [ -s "$GRANTS" ]; then
    fail "enable granted before refusing: $(cat "$GRANTS")"
else
    ok "an unreadable controller namespace stops enable before it grants anything"
fi

# pl_image_repo, directly.
case_begin
for pair in \
    "ghcr.io/ev-shindin/llm-scaling-manager:main=ghcr.io/ev-shindin/llm-scaling-manager" \
    "ghcr.io/ev-shindin/llm-scaling-manager@sha256:abc=ghcr.io/ev-shindin/llm-scaling-manager" \
    "registry.local:5000/wva=registry.local:5000/wva" \
    "registry.local:5000/wva:dev=registry.local:5000/wva"; do
    in="${pair%=*}"; want="${pair##*=}"
    got="$(pl_image_repo "$in")"
    [ "$got" = "$want" ] || fail "pl_image_repo $in gave $got, want $want"
done
ok "pl_image_repo strips tags and digests, and keeps a registry port"

case_begin
CASES_EXPECTED=13
if [ "$CASES" -ne "$CASES_EXPECTED" ]; then
    fail "$CASES cases ran, not $CASES_EXPECTED. Update CASES_EXPECTED deliberately."
else
    ok "all $CASES cases ran"
fi

if [ "$FAIL" -ne 0 ]; then
    echo "physical limiter targets check FAILED"
    exit 1
fi
echo "physical limiter targets OK"
