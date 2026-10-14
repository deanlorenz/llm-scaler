#!/usr/bin/env bash
#
# Executes the installer's default-choosing helpers against fixtures.
#
# These decide what a plain `make deploy-wva` ships: which image is pulled and
# how, and whether the operator is told when the result is not reproducible.
# Every defect here parses cleanly and installs cleanly -- a registry port read
# as a tag, a digest treated as a moving tag, a floating tag deployed in
# silence -- so `bash -n` and a green install both miss it.
#
# WRITING A CASE HERE: an expected-EMPTY case proves nothing on its own -- a
# function that does not exist returns empty too. The guard below refuses to run
# unless every function is defined, and EXPECT_CASES refuses to pass a run that
# ended early.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

command -v make >/dev/null 2>&1 || {
    printf 'FATAL: make is required to run these checks.\n' >&2
    exit 1
}
command -v yq >/dev/null 2>&1 || {
    printf 'FATAL: yq is required to run these checks.\n' >&2
    exit 1
}

# Sourced through CR-stripped copies: a Windows checkout with core.autocrlf
# makes bash stop at the first function definition, which looks exactly like
# every function being missing.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
for lib in common.sh cli.sh infra_wva.sh scaledobject.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$WORK/$lib"
done
BLUE='' GREEN='' YELLOW='' RED='' NC=''
# shellcheck source=/dev/null
source "$WORK/common.sh"
# shellcheck source=/dev/null
source "$WORK/cli.sh"
# shellcheck source=/dev/null
source "$WORK/infra_wva.sh"
# shellcheck source=/dev/null
source "$WORK/scaledobject.sh"

for fn in wva_split_image_ref wva_image_ref_fields wva_image_pull_policy wva_warn_floating_image \
          wva_warn_prometheus_tls wva_prometheus_skip_verify wva_prometheus_scraper_sa wva_prometheus_url_namespace \
          wva_append_metrics_reader_decision render_default_scaledobject; do
    declare -F "$fn" >/dev/null || {
        printf 'FATAL: %s is not defined after sourcing the library.\n' "$fn" >&2
        exit 1
    }
done

EXPECT_CASES=53
RAN=0
FAILED=0

# check <name> <want> <got>
check() {
    if [ "$2" = "$3" ]; then
        RAN=$((RAN + 1))
        printf 'ok    %s\n' "$1"
    else
        FAILED=$((FAILED + 1))
        printf 'FAIL  %s\n        want [%s]\n        got  [%s]\n' "$1" "$2" "$3" >&2
    fi
}

# ----------------------------------------------------------------------------
# Image reference: IMG -> repo, tag, digest
# ----------------------------------------------------------------------------
split() {
    WVA_IMAGE_REPO='' WVA_IMAGE_TAG='' WVA_IMAGE_DIGEST=''
    wva_split_image_ref "$1" || { printf 'rejected'; return 0; }
    printf '%s|%s|%s' "$WVA_IMAGE_REPO" "$WVA_IMAGE_TAG" "$WVA_IMAGE_DIGEST"
}

check 'image: repo:tag' \
    'ghcr.io/o/llm-scaling-manager|main|' \
    "$(split ghcr.io/o/llm-scaling-manager:main)"

# Splitting on the first colon read the port as the tag.
check 'image: a registry port is not a tag' \
    'localhost:5000/wva|dev|' \
    "$(split localhost:5000/wva:dev)"

check 'image: a digest is carried as a digest' \
    'ghcr.io/o/wva||sha256:abc123' \
    "$(split ghcr.io/o/wva@sha256:abc123)"

check 'image: repo:tag@digest keeps both' \
    'localhost:5000/wva|v1.2.3|sha256:abc123' \
    "$(split localhost:5000/wva:v1.2.3@sha256:abc123)"

check 'image: a bare repo is rejected' \
    'rejected' \
    "$(split localhost:5000/wva)"

# Through parse_args, the path IMG actually takes: a tagless IMG keeps the
# defaults rather than installing half of it.
via_img() {
    (
        WVA_IMAGE_REPO=default/repo WVA_IMAGE_TAG=deftag WVA_IMAGE_DIGEST=''
        IMG="$1"
        parse_args 2>/dev/null
        printf '%s|%s|%s' "$WVA_IMAGE_REPO" "$WVA_IMAGE_TAG" "$WVA_IMAGE_DIGEST"
    )
}

check 'IMG: registry port with tag' \
    'host:5000/team/wva|v1|' \
    "$(via_img host:5000/team/wva:v1)"

check 'IMG: digest pins by digest' \
    'ghcr.io/o/wva||sha256:0123abcd' \
    "$(via_img ghcr.io/o/wva@sha256:0123abcd)"

check 'IMG: no tag keeps the default image' \
    'default/repo|deftag|' \
    "$(via_img host:5000/team/wva)"

check 'IMG: plain repo:tag' \
    'ghcr.io/o/wva|main|' \
    "$(via_img ghcr.io/o/wva:main)"

# The fields the Makefile reads (deploy-e2e-infra, WVA_CONTROLLER_IMAGE_REPOS).
# It split IMG inline on the last colon, so a digest became repo
# "ghcr.io/o/w@sha256" with the hex as its tag.
DIGEST="sha256:$(printf '0123456789abcdef%.0s' 1 2 3 4)"
check 'fields: registry port with tag' 'localhost:5000/w|dev|' "$(wva_image_ref_fields localhost:5000/w:dev)"
check 'fields: ref-<sha> tag' 'ghcr.io/o/w|ref-abc|' "$(wva_image_ref_fields ghcr.io/o/w:ref-abc)"
check 'fields: digest' "ghcr.io/o/w||$DIGEST" "$(wva_image_ref_fields "ghcr.io/o/w@$DIGEST")"
check 'fields: bare repo pulls latest' 'ghcr.io/o/w|latest|' "$(wva_image_ref_fields ghcr.io/o/w)"

# The Makefile itself, evaluated by make: the recipe and the repository list run
# exactly these expressions.
make_eval() {
    make -s --no-print-directory -C "$ROOT" --eval "b133-print: ; @echo '$1'" b133-print "${@:2}" 2>/dev/null
}
check 'Makefile: the e2e IMG split is the tested helper (digest)' "ghcr.io/o/w||$DIGEST" \
    "$(make_eval "\$(shell \$(WVA_IMAGE_REF_FIELDS) 'ghcr.io/o/w@$DIGEST')")"
check 'Makefile: deploy-e2e-infra splits IMG with the helper and passes the digest' '2' \
    "$(sed -n '/^deploy-e2e-infra:/,/^[^[:space:]#].*:/p' "$ROOT/Makefile" | tr -d '\r' \
        | grep -c -e 'IMG_FIELDS="\$\$(\$(WVA_IMAGE_REF_FIELDS)' -e 'WVA_IMAGE_DIGEST=\$\$IMAGE_DIGEST')"
check 'Makefile: controller repos, default IMG' 'ghcr.io/ev-shindin/llm-scaling-manager' \
    "$(make_eval '$(WVA_CONTROLLER_IMAGE_REPOS)')"
check 'Makefile: controller repos, a custom IMG is appended' 'ghcr.io/ev-shindin/llm-scaling-manager localhost:5000/w' \
    "$(make_eval '$(WVA_CONTROLLER_IMAGE_REPOS)' IMG=localhost:5000/w:dev)"
check 'Makefile: controller repos, a digest of the published repo adds nothing' 'ghcr.io/ev-shindin/llm-scaling-manager' \
    "$(make_eval '$(WVA_CONTROLLER_IMAGE_REPOS)' "IMG=ghcr.io/ev-shindin/llm-scaling-manager@$DIGEST")"
check 'Makefile: controller repos stay overridable' 'registry.local/wva' \
    "$(make_eval '$(WVA_CONTROLLER_IMAGE_REPOS)' IMG=localhost:5000/w:dev WVA_CONTROLLER_IMAGE_REPOS=registry.local/wva)"

# ----------------------------------------------------------------------------
# Pull policy: IfNotPresent only for what cannot move
# ----------------------------------------------------------------------------
check 'pull policy: main is Always'          'Always'       "$(wva_image_pull_policy main '')"
check 'pull policy: a digest is IfNotPresent' 'IfNotPresent' "$(wva_image_pull_policy main sha256:abc)"
check 'pull policy: a release tag is IfNotPresent' 'IfNotPresent' "$(wva_image_pull_policy v0.6.0 '')"
# ref-<sha> is re-pushed after a merge in the OpenShift e2e; unknown means Always.
check 'pull policy: an unclassified tag is Always' 'Always' "$(wva_image_pull_policy ref-0123abcd '')"

# ----------------------------------------------------------------------------
# Floating-tag warning
# ----------------------------------------------------------------------------
warned() { wva_warn_floating_image "$@" 2>&1 | grep -c 'FLOATING tag' || true; }

check 'warning: main is announced'      '1' "$(warned ghcr.io/o/wva main '')"
check 'warning: latest is announced'    '1' "$(warned ghcr.io/o/wva latest '')"
check 'warning: a release tag is quiet' '0' "$(warned ghcr.io/o/wva v0.6.0 '')"
check 'warning: a digest is quiet'      '0' "$(warned ghcr.io/o/wva main sha256:abc)"

# ----------------------------------------------------------------------------
# Prometheus TLS: an unverified HTTPS connection is announced
# ----------------------------------------------------------------------------
tls_warned() { wva_warn_prometheus_tls "$1" 2>&1 | grep -c 'will NOT verify' || true; }

# The shipped Kubernetes config, read from the file rather than restated here.
SHIPPED_CFG="$(tr -d '\r' < "$ROOT/config/base/manager/manager-configmap.yaml" | yq '.data."config.yaml"')"
[ -n "$SHIPPED_CFG" ] || { printf 'FATAL: could not read the shipped manager config.\n' >&2; exit 1; }
# The shipped config verifies: a manifest applied without the installer sends
# its bearer token only to a Prometheus it has verified.
check 'tls: the shipped config verifies' 'false' \
    "$(printf '%s\n' "$SHIPPED_CFG" | yq -r '.PROMETHEUS_TLS_INSECURE_SKIP_VERIFY')"
# What the installer writes over it, and that its choice is announced.
skip_for() { ( ENVIRONMENT="$1" PROMETHEUS_URL="$2" PROMETHEUS_TLS_INSECURE_SKIP_VERIFY="$3" wva_prometheus_skip_verify ); }
k8s_skip="$(skip_for kubernetes https://p:9090 '')"
check 'tls: Kubernetes install, no CA to give: verification off' 'true' "$k8s_skip"
check 'tls: OpenShift keeps the shipped verification (service CA)' '' "$(skip_for openshift https://t:9091 '')"
check 'tls: an explicit choice wins' 'false' "$(skip_for kubernetes https://p:9090 false)"
INSTALLED_CFG="$(printf '%s\n' "$SHIPPED_CFG" | yq ".PROMETHEUS_TLS_INSECURE_SKIP_VERIFY = \"$k8s_skip\"")"
check 'tls: the installer choice on Kubernetes is warned about' '1' "$(tls_warned "$INSTALLED_CFG")"
check 'tls: a CA path silences it' '0' \
    "$(tls_warned "$(printf '%s\n' "$INSTALLED_CFG" | yq '.PROMETHEUS_CA_CERT_PATH = "/etc/ca/ca.crt"')")"
check 'tls: verification on is quiet' '0' \
    "$(tls_warned "$(printf '%s\n' "$SHIPPED_CFG" | yq '.PROMETHEUS_TLS_INSECURE_SKIP_VERIFY = "false"')")"
check 'tls: plain HTTP is not this warning' '0' \
    "$(tls_warned "$(printf 'PROMETHEUS_BASE_URL: "http://p:9090"\nPROMETHEUS_TLS_INSECURE_SKIP_VERIFY: "true"\n')")"

# ----------------------------------------------------------------------------
# Who may read the controller's /metrics: the Prometheus actually here
# ----------------------------------------------------------------------------
# A stub kubectl answering the Prometheus CR listing with $PROM_CRS
# ("<namespace> <serviceAccountName>" per line). It ignores -n on purpose: the
# function must itself keep only the namespace PROMETHEUS_URL addresses.
PROM_CRS=''
kubectl() { [ -z "$PROM_CRS" ] || printf '%s\n' "$PROM_CRS"; }
# scraper <CRs> <URL> <DEPLOY_PROMETHEUS> [PROMETHEUS_SA] [explicit URL]: the
# fifth argument is PROMETHEUS_URL_EXPLICIT, and defaults to the URL itself --
# the caller passed it. Pass '' for a URL the installer DETECTED.
scraper() {
    (
        PROM_CRS="$1" PROMETHEUS_URL="$2" DEPLOY_PROMETHEUS="$3" PROMETHEUS_SA="${4:-}"
        PROMETHEUS_URL_EXPLICIT="${5-$2}"
        MONITORING_NAMESPACE=our-mon
        wva_prometheus_scraper_sa 2>/dev/null
    )
}
OBS_URL='https://prometheus-operated.obs.svc.cluster.local:9090'
MON_URL='http://prom.mon.svc:9090'

check 'scraper: PROMETHEUS_SA wins' 'ns1 sa1' \
    "$(scraper 'mon prom' "$MON_URL" true ns1/sa1)"
check 'scraper: a malformed PROMETHEUS_SA grants nobody, not a detected CR' '' \
    "$(scraper 'mon prom' "$MON_URL" true just-a-name)"
check 'scraper: the CR in the namespace the URL names' 'obs obs-prom' \
    "$(scraper "$(printf 'mon prom\nobs obs-prom')" "$OBS_URL" false)"
# Was "otherwise the first CR": on a shared cluster, someone else's Prometheus.
check 'scraper: no URL namespace -> nobody, never the first CR' '' \
    "$(scraper "$(printf 'mon prom\nobs obs-prom')" 'https://prom.example:9090' false)"
check 'scraper: a CR only in another namespace is not chosen' '' \
    "$(scraper 'mon prom' "$OBS_URL" false)"
# Was granted `default`: every Pod in that namespace without its own account.
check 'scraper: a CR with no serviceAccountName (default) is not granted' '' \
    "$(scraper 'mon ' "$MON_URL" false)"
check 'scraper: CRs in that namespace disagreeing -> nobody' '' \
    "$(scraper "$(printf 'obs a\nobs b')" "$OBS_URL" false)"
check 'scraper: two CRs agreeing on one account' 'obs a' \
    "$(scraper "$(printf 'obs a\nobs a')" "$OBS_URL" false)"
# A DETECTED URL is whichever Prometheus the API listed first -- on a shared
# cluster someone else's -- so its CR never names the subject.
check 'scraper: a detected URL never decides the subject' '' \
    "$(scraper "$(printf 'mon prom\nobs obs-prom')" "$OBS_URL" false '' '')"
check 'scraper: no CR yet, our stack in MONITORING_NAMESPACE -> its account' \
    'our-mon kube-prometheus-stack-prometheus' \
    "$(scraper '' 'https://kube-prometheus-stack-prometheus.our-mon.svc.cluster.local:9090' true)"
check 'scraper: no CR, DEPLOY_PROMETHEUS, URL elsewhere -> nobody' '' \
    "$(scraper '' "$OBS_URL" true)"
check 'scraper: no CR and none deployed -> nobody' '' \
    "$(scraper '' 'https://prom.example:9090' false)"
check 'url namespace: service.namespace.svc' 'obs' "$(wva_prometheus_url_namespace "$OBS_URL")"
check 'url namespace: an external host has none' '' "$(wva_prometheus_url_namespace 'https://prom.example.com:9090')"

# The URL is resolved BEFORE the subject is chosen. Asked first, it read the
# namespace of the shipped default rather than of the Prometheus detected. And
# neither decides: the default's account is trusted only when this install
# deploys that stack (DEPLOY_PROMETHEUS=true), and a detected one never.
decision() {
    (
        PROM_CRS="$(printf 'obs obs-prom\nworkload-variant-autoscaler-monitoring kube-prometheus-stack-prometheus')"
        PROMETHEUS_URL='https://kube-prometheus-stack-prometheus.workload-variant-autoscaler-monitoring.svc.cluster.local:9090'
        PROMETHEUS_URL_EXPLICIT='' WVA_PROMETHEUS_URL_RESOLVED='' PROMETHEUS_SA=''
        DEPLOY_PROMETHEUS=false
        wva_detect_prometheus_url() { printf '%s\n' "$OBS_URL"; }
        log_info() { :; }
        k="$(mktemp "$WORK/kust.XXXXXX")"
        wva_append_metrics_reader_decision "$k" wns >/dev/null 2>&1
        printf '%s' "$WVA_METRICS_READER_SCRAPER"
    )
}
check 'scraper: neither the detected URL nor the shipped default decides' '' "$(decision)"
# Where the order still matters: the stack this install deploys is recognised
# by the RESOLVED URL addressing MONITORING_NAMESPACE, not the shipped default's.
decision_ours() {
    (
        PROM_CRS=''
        PROMETHEUS_URL='https://kube-prometheus-stack-prometheus.workload-variant-autoscaler-monitoring.svc.cluster.local:9090'
        PROMETHEUS_URL_EXPLICIT='' WVA_PROMETHEUS_URL_RESOLVED='' PROMETHEUS_SA=''
        DEPLOY_PROMETHEUS=true MONITORING_NAMESPACE=obs
        wva_detect_prometheus_url() { printf '%s\n' "$OBS_URL"; }
        log_info() { :; }
        k="$(mktemp "$WORK/kust.XXXXXX")"
        wva_append_metrics_reader_decision "$k" wns >/dev/null 2>&1
        printf '%s' "$WVA_METRICS_READER_SCRAPER"
    )
}
check 'scraper: the resolved URL, not the shipped default, finds the stack this install deploys' \
    'obs kube-prometheus-stack-prometheus' "$(decision_ours)"
unset -f kubectl

# ----------------------------------------------------------------------------
# The shipped sample ScaledObject scales like the ones the installer writes
# ----------------------------------------------------------------------------
# The sample is what people copy. It had a 0s scale-down window at 100%/30s,
# so a copied sample shed replicas on a single low reading. (Before WVA's first
# decision the scaler now answers the current replica count, so it no longer
# answers 0 there either.)
gen_behavior="$(
    unset WVA_SO_SCALE_UP_STABILIZATION WVA_SO_SCALE_UP_PERIOD \
          WVA_SO_SCALE_DOWN_STABILIZATION WVA_SO_SCALE_DOWN_PERIOD
    render_default_scaledobject ns Deployment t m addr:9090 1 10 '' '' \
        | yq -o=json -I=0 '.spec.advanced.horizontalPodAutoscalerConfig.behavior | sort_keys(..)'
)"
sample_behavior="$(tr -d '\r' < "$ROOT/config/samples/keda/external-scaler/scaledobject.yaml" \
    | yq -o=json -I=0 '.spec.advanced.horizontalPodAutoscalerConfig.behavior | sort_keys(..)')"
[ -n "$gen_behavior" ] && [ "$gen_behavior" != null ] || {
    printf 'FATAL: the generator rendered no behavior block.\n' >&2; exit 1; }
check 'sample ScaledObject: behavior equals the generator default' "$gen_behavior" "$sample_behavior"
# Named outright as well, so a generator that changed its own default to 0
# does not pass by agreement.
check 'sample ScaledObject: scale-down waits 300s' '300' \
    "$(printf '%s' "$sample_behavior" | yq -p=json '.scaleDown.stabilizationWindowSeconds')"

# ----------------------------------------------------------------------------

if [ "$FAILED" -gt 0 ]; then
    printf '\n%d install-default assertion(s) failed.\n' "$FAILED" >&2
    exit 1
fi
if [ "$RAN" -ne "$EXPECT_CASES" ]; then
    printf '\n%d case(s) reported ok, %d expected -- the run did not finish.\n' \
        "$RAN" "$EXPECT_CASES" >&2
    exit 1
fi
printf '\nInstall default checks passed (%d cases).\n' "$RAN"
