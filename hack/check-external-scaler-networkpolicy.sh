#!/usr/bin/env bash
#
# Renders every shipped overlay and asserts the external-scaler NetworkPolicy
# admits the KEDA external scaler port (:9090) ONLY from KEDA's namespace, while
# every other port the controller serves stays reachable.
#
# The scaler has no authentication of its own by default, and every call
# registers the workload it names with the caller's trigger metadata, so a Pod
# that reaches :9090 can rewrite another tenant's scaler metadata and read any
# decision. The policy is the boundary, and each way of getting it wrong parses
# and applies cleanly:
#
#   a podSelector that matches no controller Pod   policy selects nothing: open
#   a `from:` that is empty, {} or a bare podSelector   open to a namespace or more
#   a controller port with no rule                  metrics or probes go dark
#   the wrong KEDA namespace                        KEDA refused, nothing scales
#
# Then it executes the installer's wva_append_scaler_networkpolicy_patch against
# a STUBBED kubectl, renders the result, and asserts which namespace it chose.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAIL=0
PASS=0
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "  FAIL: $*"; FAIL=$((FAIL + 1)); }
pass() { PASS=$((PASS + 1)); }

if ! command -v yq >/dev/null 2>&1; then
    echo "FAIL: yq is required (not skipping: a skipped check reads as a pass)"
    exit 1
fi

# render <dir> <out>: kustomize build with whatever is available.
render() {
    if [ -n "${KUSTOMIZE:-}" ] && [ -x "${KUSTOMIZE}" ]; then
        "$KUSTOMIZE" build "$1" >"$2"
    elif command -v kustomize >/dev/null 2>&1; then
        command kustomize build "$1" >"$2"
    elif command -v kubectl >/dev/null 2>&1; then
        command kubectl kustomize "$1" >"$2"
    else
        echo "FAIL: neither kustomize nor kubectl is available to render overlays"
        exit 1
    fi
}

POLICY='select(.kind == "NetworkPolicy" and .metadata.name == "wva-external-scaler")'
SCALER_PORT=9090

# check_policy <label> <rendered.yaml> <expected KEDA namespace>
check_policy() {
    local label="$1" rendered="$2" want_ns="$3" np="$WORK/np.yaml"
    echo "== $label"
    local n
    n="$(yq "$POLICY | .metadata.name" "$rendered" | grep -c . || true)"
    if [ "$n" != "1" ]; then
        fail "$label: expected exactly one NetworkPolicy wva-external-scaler, rendered $n"
        return
    fi
    pass
    yq "$POLICY" "$rendered" >"$np"

    # The policy must select the controller Pod, or it restricts nothing.
    local dep_labels sel kv
    dep_labels="$(yq 'select(.kind == "Deployment" and .metadata.name == "wva-controller-manager") | .spec.template.metadata.labels | to_entries | .[] | .key + "=" + .value' "$rendered")"
    sel="$(yq '.spec.podSelector.matchLabels // {} | to_entries | .[] | .key + "=" + .value' "$np")"
    if [ -z "$sel" ] || [ -z "$dep_labels" ]; then
        fail "$label: podSelector or controller Pod labels are empty (selector: '$sel')"
    else
        local ok=1
        for kv in $sel; do
            printf '%s\n' "$dep_labels" | grep -qxF "$kv" || { fail "$label: podSelector $kv is not on the controller Pod"; ok=0; }
        done
        [ "$ok" = 1 ] && pass
    fi
    if yq '.spec.policyTypes[]' "$np" | grep -qx Ingress; then pass; else fail "$label: policyTypes lacks Ingress"; fi

    # Every port the controller serves: 9090 only from KEDA, the rest from anywhere.
    local ports port rules i open restricted
    ports="$(yq 'select(.kind == "Deployment" and .metadata.name == "wva-controller-manager") | .spec.template.spec.containers[].ports[].containerPort' "$rendered" | sort -u)"
    if ! printf '%s\n' "$ports" | grep -qx "$SCALER_PORT"; then
        fail "$label: the controller no longer serves :$SCALER_PORT -- update this check"
    fi
    rules="$(yq '.spec.ingress | length' "$np")"
    for port in $ports; do
        open=0
        restricted=0
        for ((i = 0; i < rules; i++)); do
            yq ".spec.ingress[$i].ports[].port" "$np" | grep -qx "$port" || continue
            if [ "$(yq ".spec.ingress[$i].from // [] | length" "$np")" = "0" ]; then
                open=1
            else
                restricted=1
                if [ "$port" = "$SCALER_PORT" ]; then
                    check_scaler_peers "$label" "$np" "$i" "$want_ns"
                fi
            fi
        done
        if [ "$port" = "$SCALER_PORT" ]; then
            if [ "$open" = 1 ]; then
                fail "$label: :$port is admitted by a rule with no from: (every source)"
            elif [ "$restricted" = 0 ]; then
                fail "$label: no rule admits :$port, so KEDA cannot call the scaler"
            else
                pass
            fi
        elif [ "$open" = 1 ]; then
            pass
        else
            fail "$label: :$port has no rule admitting every source (metrics and probes must stay reachable)"
        fi
    done
}

# check_scaler_peers: every peer of rule <i> must be EXACTLY a namespaceSelector
# naming <want_ns>. A bare podSelector means "this namespace", {} means "every
# namespace", and an ipBlock means whatever the CIDR says -- none is KEDA.
check_scaler_peers() {
    local label="$1" np="$2" i="$3" want_ns="$4" peers j keys labels
    peers="$(yq ".spec.ingress[$i].from | length" "$np")"
    for ((j = 0; j < peers; j++)); do
        keys="$(yq ".spec.ingress[$i].from[$j] | keys | join(\",\")" "$np")"
        labels="$(yq ".spec.ingress[$i].from[$j].namespaceSelector.matchLabels // {} | to_entries | map(.key + \"=\" + .value) | join(\",\")" "$np")"
        if [ "$keys" != "namespaceSelector" ] || [ "$labels" != "kubernetes.io/metadata.name=$want_ns" ]; then
            fail "$label: :$SCALER_PORT peer $j is {$keys} [$labels], want only namespaceSelector kubernetes.io/metadata.name=$want_ns"
        else
            pass
        fi
    done
}

for overlay in "$ROOT"/config/overlays/*/*/; do
    name="${overlay%/}"
    name="${name#"$ROOT"/}"
    want=keda-system
    case "$name" in */openshift) want=openshift-keda ;; esac
    if ! render "$overlay" "$WORK/rendered.yaml" 2>"$WORK/err"; then
        fail "$name: does not render: $(head -3 "$WORK/err")"
        continue
    fi
    check_policy "$name" "$WORK/rendered.yaml" "$want"
done

# --- The installer's patch, against a stubbed kubectl.
#
# Sourced through a CR-stripped copy: a Windows checkout can write these files
# as CRLF, and bash then stops at the first function definition.
tr -d '\r' <"$ROOT/deploy/lib/constants.sh" >"$WORK/constants.sh"
tr -d '\r' <"$ROOT/deploy/lib/infra_wva.sh" >"$WORK/infra_wva.sh"

# install_case <label> <overlay> <want: namespace | ABSENT | REFUSED> [VAR=value...]
install_case() {
    local label="$1" overlay="$2" want="$3" dir="$WORK/install"
    shift 3
    case "$want" in ABSENT | REFUSED) echo "== $label" ;; esac
    rm -rf "$dir"
    mkdir -p "$dir"
    ln -s "$ROOT/$overlay" "$dir/base"
    printf 'namespace: wva-check\nresources:\n- ./base\npatches:\n' >"$dir/kustomization.yaml"
    : >"$WORK/kubectl.log"
    (
        # shellcheck disable=SC1091
        . "$WORK/constants.sh"
        # shellcheck disable=SC1091
        . "$WORK/infra_wva.sh"
        log_info() { :; }
        log_warning() { :; }
        log_error() { echo "$1" >"$WORK/refusal"; exit 1; }
        kubectl() {
            echo "$*" >>"$WORK/kubectl.log"
            case "$*" in
                *"get pods -A -l $KEDA_OPERATOR_LABEL_SELECTOR "*) printf '%s' "${STUB_PODS_STD:-}" ;;
                *"get pods -A -l app=keda-operator "*) printf '%s' "${STUB_PODS_APP:-}" ;;
                *) return 0 ;;
            esac
        }
        WVA_NS=wva-check
        KEDA_NAMESPACE=keda-system
        KEDA_NAMESPACE_EXPLICIT=false
        KEDA_HELM_INSTALL=false
        ENVIRONMENT=kubernetes
        local kv
        for kv in "$@"; do eval "$kv"; done
        if ! declare -F wva_append_scaler_networkpolicy_patch >/dev/null; then
            echo "wva_append_scaler_networkpolicy_patch is not defined" >"$WORK/refusal"
            exit 2
        fi
        wva_append_scaler_networkpolicy_patch "$dir/kustomization.yaml"
    )
    local rc=$?
    if [ "$want" = REFUSED ]; then
        if [ "$rc" = 1 ]; then pass; else fail "$label: expected a refusal, got exit $rc"; fi
        return
    fi
    if [ "$rc" != 0 ]; then
        fail "$label: exit $rc: $(cat "$WORK/refusal" 2>/dev/null)"
        return
    fi
    if ! render "$dir" "$WORK/installed.yaml" 2>"$WORK/err"; then
        fail "$label: the patched overlay does not render: $(head -3 "$WORK/err")"
        return
    fi
    if [ "$want" = ABSENT ]; then
        if [ -z "$(yq "$POLICY | .metadata.name" "$WORK/installed.yaml")" ]; then pass; else fail "$label: the policy is still rendered"; fi
        if grep -q "delete networkpolicy wva-external-scaler -n wva-check" "$WORK/kubectl.log"; then pass; else fail "$label: an existing policy is not removed on opt-out"; fi
        return
    fi
    check_policy "$label" "$WORK/installed.yaml" "$want"
}

install_case "installer: explicit KEDA_NAMESPACE wins" config/overlays/cluster-scoped/kubernetes my-keda \
    KEDA_NAMESPACE=my-keda KEDA_NAMESPACE_EXPLICIT=true STUB_PODS_STD=elsewhere
install_case "installer: KEDA found where its operator runs" config/overlays/namespace-scoped/kubernetes found-keda \
    "STUB_PODS_STD='found-keda found-keda'"
install_case "installer: OpenShift CMA operator found by app label" config/overlays/namespace-scoped/openshift openshift-keda \
    STUB_PODS_APP=openshift-keda ENVIRONMENT=openshift
install_case "installer: nothing visible on OpenShift: platform default" config/overlays/cluster-scoped/openshift openshift-keda \
    ENVIRONMENT=openshift
install_case "installer: nothing visible on Kubernetes: KEDA_NAMESPACE default" config/overlays/cluster-scoped/kubernetes keda-system
install_case "installer: Helm-installed KEDA is not there yet" config/overlays/cluster-scoped/kubernetes helm-keda \
    KEDA_NAMESPACE=helm-keda KEDA_HELM_INSTALL=true
install_case "installer: two KEDA namespaces refuse" config/overlays/cluster-scoped/kubernetes REFUSED \
    STUB_PODS_STD=keda-a STUB_PODS_APP=keda-b
install_case "installer: WVA_SCALER_NETWORKPOLICY=false drops the policy" config/overlays/cluster-scoped/kubernetes ABSENT \
    WVA_SCALER_NETWORKPOLICY=false
install_case "installer: WVA_SCALER_NETWORKPOLICY=bogus refuses" config/overlays/cluster-scoped/kubernetes REFUSED \
    WVA_SCALER_NETWORKPOLICY=bogus

echo ""
if [ "$FAIL" -gt 0 ]; then
    echo "external-scaler NetworkPolicy: $FAIL failed, $PASS passed"
    exit 1
fi
if [ "$PASS" = 0 ]; then
    echo "FAIL: no assertion ran"
    exit 1
fi
echo "external-scaler NetworkPolicy OK ($PASS assertions)"
