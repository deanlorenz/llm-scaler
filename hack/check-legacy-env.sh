#!/usr/bin/env bash
#
# Fixtures for the install guard and the read fall-back in deploy/lib/common.sh,
# and the Makefile guard next to them: an old install variable stops the run
# after the rename, an old ConfigMap is read on a re-deploy, and nothing happens
# before the rename.
#
# The re-deploy cases run on any tree. They prove something only after the rename,
# when the code asks for a new name first: before it the old name is the only one.
set -u

cd "$(dirname "$0")/.." || exit 1

fail=0
cases=0

check() {
    local name=$1 want_rc=$2 want_text=$3 rc out
    shift 3
    cases=$((cases + 1))
    rc=0
    out=$("$@" 2>&1) || rc=$?
    if [ "$rc" != "$want_rc" ] || { [ -n "$want_text" ] && ! printf '%s' "$out" | grep -qF -- "$want_text"; }; then
        echo "FAIL: $name: rc=$rc, want $want_rc; output: $out"
        fail=1
    fi
}

# The function alone: sourcing common.sh would run its own call first.
fn=$(sed -n '/^legacy_env_refuse() {$/,/^}$/p' deploy/lib/common.sh)
if [ -z "$fn" ]; then
    echo "FAIL: read no legacy_env_refuse from deploy/lib/common.sh"
    exit 1
fi

# env -i: a variable left in the shell that runs this must not decide a case.
guard() {
    local args=()
    while [ "$#" -gt 1 ]; do args+=("$1"); shift; done
    env -i PATH="$PATH" "${args[@]}" bash -c "$fn"$'\n'"legacy_env_refuse $1"
}

check "no old variable passes" 0 "" guard LLMSM_
check "an old variable stops the run" 1 "WVA_NS was renamed to LLMSM_NS" guard WVA_NS=x LLMSM_
check "every old variable is named" 1 "WVA_SCOPE was renamed to LLMSM_SCOPE" guard WVA_NS=x WVA_SCOPE=y LLMSM_
check "a suffix form stops the run" 1 "DEPLOY_WVA was renamed to DEPLOY_SCALING_MANAGER" guard DEPLOY_WVA=false LLMSM_
check "an inner form stops the run" 1 "BENCHMARK_WVA_TAG was renamed to BENCHMARK_LLMSM_TAG" guard BENCHMARK_WVA_TAG=x LLMSM_
check "another inner form stops the run" 1 "SFZ_WVA_NAMESPACE was renamed to SFZ_LLMSM_NAMESPACE" guard SFZ_WVA_NAMESPACE=x LLMSM_
check "the old phase value stops the run" 1 "INSTALL_PHASE=wva was renamed" guard INSTALL_PHASE=wva LLMSM_
check "a name that merely contains it passes" 0 "" guard MY_WVAX=1 LLMSM_
check "equal prefixes do nothing" 0 "" guard WVA_NS=x DEPLOY_WVA=false INSTALL_PHASE=wva WVA_

# common.sh has to call it, or none of the above protects anything.
if ! grep -qE '^legacy_env_refuse [A-Z]+_ \|\| exit 1' deploy/lib/common.sh; then
    echo "FAIL: deploy/lib/common.sh does not call legacy_env_refuse"
    fail=1
fi

# The Makefile guard, on the tree as it stands: it stops the run only once the
# prefix has moved.
current=$(sed -n 's/^ENV_PREFIX := //p' Makefile | tr -d '\r')
if [ -z "$current" ]; then
    echo "FAIL: read no ENV_PREFIX from the Makefile"
    fail=1
elif [ "$current" = WVA_ ]; then
    check "make before the rename" 0 "" env -i PATH="$PATH" WVA_NS=x DEPLOY_WVA=false INSTALL_PHASE=wva make -n --no-print-directory lint-config
else
    check "make, a prefix form" 2 "WVA_NS: renamed" env -i PATH="$PATH" WVA_NS=x make -n --no-print-directory lint-config
    check "make, a suffix form" 2 "DEPLOY_WVA: renamed" env -i PATH="$PATH" DEPLOY_WVA=false make -n --no-print-directory lint-config
    check "make, an inner form" 2 "SFZ_WVA_NAMESPACE: renamed" make -n --no-print-directory lint-config SFZ_WVA_NAMESPACE=x
    check "make, the old phase" 2 "INSTALL_PHASE=wva was renamed" env -i PATH="$PATH" INSTALL_PHASE=wva make -n --no-print-directory lint-config
fi

# A re-deploy over an install from before the rename: only the old ConfigMaps
# exist, with the old keys. The stub answers NotFound for any other name.
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir "$WORK/bin"
cat >"$WORK/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
# get configmap <name> -n <ns> -o <format>
name=$3
case "$name" in
    wva-manager-config)
        cfg=$'WVA_PASSIVE: "true"\nPROMETHEUS_BASE_URL: https://prom.example:9090\nPROMETHEUS_TLS_INSECURE_SKIP_VERIFY: "true"\n'
        case "$*" in
            *jsonpath*) printf '%s' "$cfg" ;;
            *) jq -n --arg c "$cfg" '{data: {"config.yaml": $c}}' ;;
        esac
        ;;
    wva-scaling-policy-config)
        def=$'passive: true\nlimiters:\n- type: quota\n'
        case "$*" in
            *jsonpath*) printf '%s' "$def" ;;
            *) jq -n --arg d "$def" '{data: {default: $d, batch: "x: 1"}}' ;;
        esac
        ;;
    *)
        echo "Error from server (NotFound): configmaps \"$name\" not found" >&2
        exit 1
        ;;
esac
STUB
chmod +x "$WORK/bin/kubectl"

for infra in deploy/lib/infra_wva.sh deploy/lib/infra_scaling_manager.sh; do
    [ -f "$infra" ] || continue
    lower=$(printf '%s' "$current" | tr '[:upper:]' '[:lower:]')
    run_redeploy() {
        env -i PATH="$WORK/bin:$PATH" HOME="$HOME" NC= BLUE= GREEN= YELLOW= RED= bash -c '
            . deploy/lib/common.sh
            . deploy/lib/limiter_policy.sh
            . "$1"
            eval "$2"' redeploy "$infra" "$1"
    }
    check "a passive controller stays passive on re-deploy" 0 "passive=true/true" \
        run_redeploy "${lower}live_passive ns; echo \"passive=\$${current}LIVE_PASSIVE/\$${current}LIVE_DEFAULT_PASSIVE\""
    check "the live Prometheus settings carry over" 0 "tls=read https://prom.example:9090" \
        run_redeploy "${lower}read_live_prometheus_tls ns; echo \"tls=\$${current}LIVE_PROM_TLS \$${current}LIVE_PROM_URL\""
    check "the live limiter is seen" 0 "limiters=quota" \
        run_redeploy "${lower}live_limiters ns; echo \"limiters=\$(printf %s \"\$${current}LIVE_LIMITERS\" | tr -d '[]{}\" ' | sed 's/type://')\""
done

want=16
[ "$current" = WVA_ ] && want=13
if [ "$cases" != "$want" ]; then
    echo "FAIL: ran $cases cases, want $want"
    fail=1
fi
[ "$fail" = 0 ] && echo "ok: legacy_env ($cases cases)"
exit "$fail"
