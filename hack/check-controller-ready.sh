#!/usr/bin/env bash
#
# Executes the controller readiness check -- wva_controller_rollout_ready, and
# verify_deployment around it -- against a stub kubectl.
#
# `kubectl rollout status` says "successfully rolled out" at once for a
# Deployment scaled to 0, so an install over a controller at 0/0 printed "WVA
# controller is running and ready" and exited 0. That parses perfectly and
# passes on every cluster where the controller is up; only a run against a
# Deployment at 0 replicas shows it.
#
# The stub answers the Deployment lookup with one controller, `rollout status`
# with exit $STUB_ROLLOUT, and the replica read with "$STUB_COUNTS"
# ("<spec.replicas>/<status.readyReplicas>", fields omitted as the API omits
# them); $STUB_COUNTS=FAIL makes that read fail. Every other call succeeds
# with no output, which verify_deployment reads as nothing else to report.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FAILED=0
RAN=0
EXPECT_CASES=13

for lib in constants.sh common.sh infra_wva.sh infra_monitoring.sh verify.sh; do
    tr -d '\r' < "$ROOT/deploy/lib/$lib" > "$TMP/$lib"
done
BLUE='' GREEN='' YELLOW='' RED='' NC=''
for lib in constants.sh common.sh infra_wva.sh infra_monitoring.sh verify.sh; do
    # shellcheck source=/dev/null
    source "$TMP/$lib"
done
for fn in wva_controller_rollout_ready verify_deployment; do
    declare -F "$fn" >/dev/null || {
        printf 'FATAL: %s is not defined after sourcing the library.\n' "$fn" >&2
        exit 1
    }
done
# Outside the WVA section verify_deployment asks Prometheus and the EPP; both
# are answered here so the stub needs to know only the Deployment.
wva_prometheus_endpoint() { echo "https://prom.example:9090"; }
wva_report_epp_flowcontrol() { return 0; }

mkdir -p "$TMP/bin"
cat > "$TMP/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
    "rollout status"*)
        [ "$STUB_ROLLOUT" = 0 ] && echo 'deployment "wva-controller-manager" successfully rolled out'
        exit "$STUB_ROLLOUT"
        ;;
    "get deployment -n "*" -o name")
        echo "deployment.apps/wva-controller-manager"
        ;;
    "get deployment.apps/wva-controller-manager "*)
        [ "$STUB_COUNTS" = FAIL ] && exit 1
        printf '%s' "$STUB_COUNTS"
        ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/kubectl"
PATH="$TMP/bin:$PATH"

check() {
    if [ "$2" = "$3" ]; then
        RAN=$((RAN + 1))
        printf 'ok    %s\n' "$1"
    else
        FAILED=$((FAILED + 1))
        printf 'FAIL  %s\n        want [%s]\n        got  [%s]\n' "$1" "$2" "$3" >&2
    fi
}

# ready <rollout rc> <counts> echoes the helper's status and reason.
ready() {
    local rc=0
    STUB_ROLLOUT="$1" STUB_COUNTS="$2" \
        wva_controller_rollout_ready deployment.apps/wva-controller-manager wns 1s >/dev/null 2>&1 || rc=$?
    echo "$rc ${WVA_CONTROLLER_NOT_READY:-}"
}
export STUB_ROLLOUT STUB_COUNTS

check 'ready: one replica, one Ready'                     '0 '                   "$(ready 0 '1/1')"
check 'ready: two replicas, one Ready (a standby)'        '0 '                   "$(ready 0 '2/1')"
check 'ready: replicas unset (API default 1), one Ready'  '0 '                   "$(ready 0 '/1')"
check 'not ready: scaled to 0, rollout passes'            '1 scaled-to-zero'     "$(ready 0 '0/')"
check 'not ready: rollout passes with none Ready'         '1 no-ready-replica'   "$(ready 0 '1/')"
check 'not ready: readyReplicas 0 written out'            '1 no-ready-replica'   "$(ready 0 '1/0')"
check 'not ready: the rollout fails'                      '1 rollout'            "$(ready 1 '1/1')"
check 'not ready: the Deployment cannot be read after'    '1 unreadable'         "$(ready 0 FAIL)"

# verify <rollout rc> <counts> runs verify_deployment and echoes its status
# and whether it claimed the controller ready.
verify() {
    local out rc=0
    out="$(STUB_ROLLOUT="$1" STUB_COUNTS="$2" WVA_NS=wns WVA_SCOPE=cluster WVA_WATCH_NS='' \
        SCALER_BACKEND=none DEPLOY_PROMETHEUS=false DEPLOY_OPERATIONAL_DASHBOARD=false \
        WVA_VERIFY_TIMEOUT=1s verify_deployment 2>&1)" || rc=$?
    printf '%s %s %s\n' "$rc" \
        "$(grep -q 'WVA controller is running and ready' <<<"$out" && echo claimed-ready || echo not-claimed)" \
        "$(grep -q 'scaled to 0 replicas' <<<"$out" && echo says-zero || echo silent)"
}

check 'verify: a Ready controller passes'                  '0 claimed-ready silent'   "$(verify 0 '1/1')"
check 'verify: 0/0 replicas fails, and says it is at 0'    '1 not-claimed says-zero'  "$(verify 0 '0/')"
check 'verify: a rollout with none Ready fails'            '1 not-claimed silent'     "$(verify 0 '1/')"
check 'verify: a failed rollout fails'                     '1 not-claimed silent'     "$(verify 1 '1/1')"

# deploy_wva_controller waits with the same helper; a bare rollout status there
# reports a controller at 0 as rolled out.
check 'every controller wait uses the helper (no bare rollout status on $wva_deploy)' '0' \
    "$(grep -c 'kubectl rollout status "\$wva_deploy"' "$TMP/infra_wva.sh" "$TMP/verify.sh" | awk -F: '{s+=$2} END {print s}')"

if [ "$FAILED" -gt 0 ]; then
    printf '\n%d controller readiness assertion(s) failed.\n' "$FAILED" >&2
    exit 1
fi
if [ "$RAN" -ne "$EXPECT_CASES" ]; then
    printf '\n%d case(s) reported ok, %d expected -- the run did not finish.\n' "$RAN" "$EXPECT_CASES" >&2
    exit 1
fi
printf '\nController readiness checks passed (%d cases).\n' "$RAN"
