#!/usr/bin/env bash
#
# Executes the installer's external-scaler TLS code (deploy/lib/scaler_tls.sh
# and wva_verify_scaler_tls in deploy/lib/verify.sh) against a STUBBED kubectl
# that keeps objects in a directory, with the real openssl.
#
# What it pins, each of which parses cleanly and installs cleanly when wrong:
#
#   the default            a new install stays plaintext; a re-install keeps TLS
#                          on; hand-set TLS flags and an unreadable Deployment
#                          are refusals, not "off"
#   openssl certificates   the serving certificate names both Service hosts and
#                          chains to the CA; the client one carries the pinned
#                          identity; a re-run changes nothing that is still
#                          good, and re-issues a leaf that is expiring, names
#                          the wrong hosts, or outlived its CA
#   ownership              a Secret, Issuer or Certificate the installer did not
#                          create is never overwritten; Forbidden is a refusal
#   cert-manager           the Issuer chain and Certificates it applies, and
#                          refusals when the webhook or issuance fails
#   OpenShift              the serving certificate is the service CA's
#                          (annotation on the Service), never minted here
#   the Deployment         flags, mounts and the client-CA digest present
#                          exactly when TLS is on, in every shipped overlay
#   verify                 the reported mode, expiry, names, and ScaledObjects
#                          that would still dial plaintext
#
# Every case asserts something that a missing function also fails, so a run
# against code without this feature fails rather than passes empty.

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAIL=0
PASS=0
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "  FAIL: $*"; FAIL=$((FAIL + 1)); }
pass() { PASS=$((PASS + 1)); }
ok() { if eval "$1"; then pass; else fail "$2"; fi; }

for tool in yq jq openssl; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "FAIL: $tool is required (not skipping: a skipped check reads as a pass)"
        exit 1
    fi
done

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

# Sourced through CR-stripped copies: a Windows checkout can write these as
# CRLF, and bash then stops at the first function definition. A file that does
# not exist yet (the code under test, on a tree without it) becomes an empty
# one, so every case fails on its missing function instead of the run aborting.
for f in constants common verify scaler_tls; do
    if [ -f "$ROOT/deploy/lib/$f.sh" ]; then
        tr -d '\r' <"$ROOT/deploy/lib/$f.sh" >"$WORK/$f.sh"
    else
        : >"$WORK/$f.sh"
    fi
done

NS=wva-check
STORE="$WORK/store"

forbidden() { echo 'Error from server (Forbidden): secrets is forbidden: User "tenant" cannot get resource' >&2; }
notfound() { echo 'Error from server (NotFound): not found' >&2; }

# store_get <resource> <name>: the stored object, or NotFound.
store_get() {
    if [ -f "$STORE/$1__$2.json" ]; then cat "$STORE/$1__$2.json"; else notfound; return 1; fi
}

# store_put_secret <file>: a Secret JSON, as a write.
store_put_secret() {
    local name
    name="$(jq -r .metadata.name "$1")"
    cp "$1" "$STORE/secret__$name.json"
    echo "secret/$name" >>"$WORK/writes"
}

# cm_issue <manifest>: what cert-manager would do with the applied manifest --
# store every Issuer and Certificate, and write each Certificate's Secret with
# the secretTemplate labels and a stand-in CA.
cm_issue() {
    local doc kind name secret labels
    yq -o=json -I=0 '.' "$1" | while IFS= read -r doc; do
        [ -n "$doc" ] && [ "$doc" != null ] && [ "$doc" != --- ] || continue
        kind="$(printf '%s' "$doc" | jq -r .kind)"
        name="$(printf '%s' "$doc" | jq -r .metadata.name)"
        case "$kind" in
            Issuer) printf '%s' "$doc" >"$STORE/issuers.cert-manager.io__$name.json" ;;
            Certificate)
                printf '%s' "$doc" >"$STORE/certificates.cert-manager.io__$name.json"
                secret="$(printf '%s' "$doc" | jq -r .spec.secretName)"
                labels="$(printf '%s' "$doc" | jq -c '.spec.secretTemplate.labels // {}')"
                jq -n --arg n "$secret" --arg c "$name" --argjson l "$labels" \
                    --arg ca "$(printf 'FAKE-CERT-MANAGER-CA\n' | base64 | tr -d '\n')" \
                    '{apiVersion: "v1", kind: "Secret",
                      metadata: {name: $n, labels: $l, annotations: {"cert-manager.io/certificate-name": $c}},
                      data: {"tls.crt": $ca, "tls.key": $ca, "ca.crt": $ca}}' >"$STORE/secret__$secret.json"
                echo "cm-secret/$secret" >>"$WORK/writes"
                ;;
        esac
    done
}

# stub_kubectl answers from $STORE and STUB_* variables:
#   STUB_CM            present | absent | anything else (discovery error)
#   STUB_DEPLOY        FORBIDDEN makes the controller Deployment unreadable
#   STUB_SECRETS       FORBIDDEN makes every Secret unreadable
#   STUB_DRYRUN        fail makes the server-side dry run fail (webhook down)
#   STUB_WAIT          fail makes the Certificate wait time out
#   STUB_SO            FORBIDDEN makes ScaledObjects unreadable; else $WORK/so.json
stub_kubectl() {
    printf '%s\n' "$*" >>"$WORK/kubectl.log"
    local a="$*" tmp name
    case "$a" in
        "get --raw /apis/cert-manager.io")
            case "${STUB_CM:-absent}" in
                present) echo '{"kind":"APIGroup","name":"cert-manager.io"}' ;;
                absent) echo 'Error from server (NotFound): the server could not find the requested resource' >&2; return 1 ;;
                *) echo 'Unable to connect to the server: i/o timeout' >&2; return 1 ;;
            esac
            ;;
        "get deployment wva-controller-manager -n $NS -o json")
            [ "${STUB_DEPLOY:-}" != FORBIDDEN ] || { forbidden; return 1; }
            store_get deployment wva-controller-manager
            ;;
        "get scaledobjects.keda.sh "*)
            [ "${STUB_SO:-}" != FORBIDDEN ] || { forbidden; return 1; }
            if [ -f "$WORK/so.json" ]; then cat "$WORK/so.json"; else echo '{"items":[]}'; fi
            ;;
        "get secret "*" -n $NS -o jsonpath="*)
            [ "${STUB_SECRETS:-}" != FORBIDDEN ] || { forbidden; return 1; }
            set -- $a
            store_get secret "$3" | jq -r '.data["tls.crt"] // ""'
            ;;
        "get "*" -n $NS -o json")
            set -- $a
            if [ "$2" = secret ] && [ "${STUB_SECRETS:-}" = FORBIDDEN ]; then forbidden; return 1; fi
            store_get "$2" "$3"
            ;;
        "apply --server-side "*)
            tmp="$WORK/apply.json"
            cat >"$tmp"
            store_put_secret "$tmp"
            ;;
        "apply --dry-run=server -f "*)
            [ "${STUB_DRYRUN:-ok}" = ok ] || { echo 'Error from server (InternalError): failed calling webhook "webhook.cert-manager.io": connection refused' >&2; return 1; }
            ;;
        "apply -f "*)
            cp "${a#apply -f }" "$WORK/cm-applied.yaml"
            cm_issue "$WORK/cm-applied.yaml"
            ;;
        "wait "*) [ "${STUB_WAIT:-ok}" = ok ] || return 1 ;;
        "delete secret "*" -n $NS --ignore-not-found")
            set -- $a
            name="$3"
            rm -f "$STORE/secret__$name.json"
            ;;
        *) return 0 ;;
    esac
}

# run <function call> [VAR=value...]: runs it in a subshell with the libraries
# sourced and kubectl stubbed. Refusal text lands in $WORK/refusal, warnings in
# $WORK/warnings, info and success lines in $WORK/info; the variables the code
# sets are read back from $WORK/vars. Sets RC.
run() {
    local call="$1"
    shift
    : >"$WORK/kubectl.log"
    : >"$WORK/warnings"
    : >"$WORK/info"
    : >"$WORK/refusal"
    : >"$WORK/writes"
    : >"$WORK/vars"
    : >"$WORK/stdout"
    : >"$WORK/stderr"
    rm -f "$WORK/cm-applied.yaml"
    (
        # shellcheck disable=SC1091
        . "$WORK/constants.sh"
        # shellcheck disable=SC1091
        . "$WORK/common.sh"
        # shellcheck disable=SC1091
        . "$WORK/verify.sh"
        # shellcheck disable=SC1091
        . "$WORK/scaler_tls.sh"
        log_info() { echo "$1" >>"$WORK/info"; }
        log_success() { echo "$1" >>"$WORK/info"; }
        log_warning() { echo "$1" >>"$WORK/warnings"; }
        log_error() { echo "$1" >"$WORK/refusal"; exit 1; }
        kubectl() { stub_kubectl "$@"; }
        WVA_NS=$NS
        ENVIRONMENT=kubernetes
        WVA_SCOPE=namespace
        local kv
        for kv in "$@"; do eval "$kv"; done
        declare -F "${call%% *}" >/dev/null || { echo "${call%% *} is not defined" >"$WORK/refusal"; exit 2; }
        eval "$call" >"$WORK/stdout" 2>>"$WORK/stderr"
        local st=$?
        # Plain assignments, not `declare -p`: sourced inside run, a declare
        # would make them local to it.
        printf 'WVA_SCALER_TLS_ON=%q\nWVA_SCALER_TLS_PROVIDER_RESOLVED=%q\nWVA_SCALER_CLIENT_CA_HASH=%q\n' \
            "${WVA_SCALER_TLS_ON:-}" "${WVA_SCALER_TLS_PROVIDER_RESOLVED:-}" "${WVA_SCALER_CLIENT_CA_HASH:-}" >"$WORK/vars"
        exit "$st"
    )
    RC=$?
    WVA_SCALER_TLS_ON="" WVA_SCALER_TLS_PROVIDER_RESOLVED="" WVA_SCALER_CLIENT_CA_HASH=""
    # shellcheck disable=SC1091
    . "$WORK/vars" 2>/dev/null || true
}

reset_store() { rm -rf "$STORE"; mkdir -p "$STORE"; rm -f "$WORK/so.json"; }

# secret_file <name> <key> <out>: a stored Secret key, decoded.
secret_file() {
    jq -r --arg k "$2" '.data[$k] // ""' "$STORE/secret__$1.json" 2>/dev/null | base64 -d >"$3" 2>/dev/null || : >"$3"
}
sans_of() { openssl x509 -in "$1" -noout -text 2>/dev/null | awk '/Subject Alternative Name/ { getline; print }' | tr ',' '\n' | sed -n 's/^ *DNS://p' | sort -u | tr '\n' ' '; }
label_of() { jq -r '.metadata.labels["wva.llmd.ai/scaler-tls"] // ""' "$STORE/secret__$1.json" 2>/dev/null; }
# yqs is yq over a multi-document file, without the --- it prints between results.
yqs() { yq "$@" | grep -vx -- ---; }
sha_of() { openssl dgst -sha256 "$1" | awk '{ print $NF }'; }

# put_deploy <provider|plaintext|manual>: a controller Deployment in the store.
put_deploy() {
    local args='["--leader-elect=true"]' ann='{}'
    case "$1" in
        plaintext) ;;
        manual) args='["--leader-elect=true","--external-scaler-cert-path=/mine"]' ;;
        *) args='["--leader-elect=true","--external-scaler-cert-path=/etc/wva/scaler-tls"]'
           ann="{\"wva.llmd.ai/scaler-tls\":\"$1\"}" ;;
    esac
    jq -n --argjson args "$args" --argjson ann "$ann" \
        '{kind: "Deployment", metadata: {name: "wva-controller-manager"},
          spec: {template: {metadata: {annotations: $ann},
                            spec: {containers: [{name: "manager", args: $args}]}}}}' \
        >"$STORE/deployment__wva-controller-manager.json"
}

# put_foreign_secret <name> [annotations-json]: a Secret this installer did not write.
put_foreign_secret() {
    local ann="${2:-}"
    [ -n "$ann" ] || ann='{}'
    jq -n --arg n "$1" --argjson a "$ann" \
        '{kind: "Secret", metadata: {name: $n, annotations: $a}, data: {"tls.crt": "Zm9yZWlnbgo="}}' \
        >"$STORE/secret__$1.json"
}

SERVING=wva-external-scaler-tls
CLIENT=wva-external-scaler-client-tls
CA=wva-external-scaler-ca
WANT_SANS="wva-external-scaler.$NS.svc wva-external-scaler.$NS.svc.cluster.local "

# ---------------------------------------------------------------- the default
echo "== resolve: a new install stays plaintext"
reset_store
run wva_resolve_scaler_tls
ok '[ "$RC" = 0 ] && [ "$WVA_SCALER_TLS_ON" = false ]' "new install: exit $RC, on=$WVA_SCALER_TLS_ON (want 0, false): $(cat "$WORK/refusal")"

echo "== resolve: a re-install keeps TLS on, and its provider"
reset_store
put_deploy openssl
run wva_resolve_scaler_tls STUB_CM=present
ok '[ "$RC" = 0 ] && [ "$WVA_SCALER_TLS_ON" = true ]' "re-install: on=$WVA_SCALER_TLS_ON, want true: $(cat "$WORK/refusal")"
ok '[ "$WVA_SCALER_TLS_PROVIDER_RESOLVED" = openssl ]' "re-install: provider $WVA_SCALER_TLS_PROVIDER_RESOLVED, want the live openssl even with cert-manager now present"

echo "== resolve: WVA_SCALER_TLS=false turns a live TLS install off"
run wva_resolve_scaler_tls WVA_SCALER_TLS=false
ok '[ "$RC" = 0 ] && [ "$WVA_SCALER_TLS_ON" = false ]' "explicit false: on=$WVA_SCALER_TLS_ON"

echo "== resolve: an unreadable Deployment is a refusal, not plaintext"
run wva_resolve_scaler_tls STUB_DEPLOY=FORBIDDEN
ok '[ "$RC" = 1 ] && grep -q "WVA_SCALER_TLS=true or WVA_SCALER_TLS=false" "$WORK/refusal"' "Forbidden Deployment: exit $RC, $(cat "$WORK/refusal")"

echo "== resolve: TLS flags set by hand are a refusal, not dropped"
reset_store
put_deploy manual
run wva_resolve_scaler_tls
ok '[ "$RC" = 1 ] && grep -q "did not set" "$WORK/refusal"' "hand-set flags: exit $RC, $(cat "$WORK/refusal")"

echo "== resolve: a bogus value refuses"
reset_store
run wva_resolve_scaler_tls WVA_SCALER_TLS=yes
ok '[ "$RC" = 1 ]' "WVA_SCALER_TLS=yes: exit $RC"

echo "== resolve: auto picks cert-manager when installed, openssl when not, refuses when unknown"
run wva_resolve_scaler_tls WVA_SCALER_TLS=true STUB_CM=present
ok '[ "$WVA_SCALER_TLS_PROVIDER_RESOLVED" = cert-manager ]' "cert-manager present: got '$WVA_SCALER_TLS_PROVIDER_RESOLVED'"
run wva_resolve_scaler_tls WVA_SCALER_TLS=true STUB_CM=absent
ok '[ "$WVA_SCALER_TLS_PROVIDER_RESOLVED" = openssl ]' "cert-manager absent: got '$WVA_SCALER_TLS_PROVIDER_RESOLVED'"
run wva_resolve_scaler_tls WVA_SCALER_TLS=true STUB_CM=error
ok '[ "$RC" = 1 ] && grep -q WVA_SCALER_TLS_PROVIDER "$WORK/refusal"' "discovery error: exit $RC (want a refusal naming WVA_SCALER_TLS_PROVIDER)"

# ---------------------------------------------------------------- openssl
echo "== openssl: a fresh install writes the CA, serving and client Secrets"
reset_store
mkdir -p "$WORK/k"
printf 'namespace: %s\nresources:\n- ./base\npatches:\n' "$NS" >"$WORK/k/kustomization.yaml"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=absent
ok '[ "$RC" = 0 ]' "fresh: exit $RC: $(cat "$WORK/refusal")"
ok '[ "$(label_of $CA)/$(label_of $SERVING)/$(label_of $CLIENT)" = ca/serving/client ]' "fresh: Secret labels $(label_of $CA)/$(label_of $SERVING)/$(label_of $CLIENT), want ca/serving/client"
secret_file "$CA" tls.crt "$WORK/ca.pem"
secret_file "$SERVING" tls.crt "$WORK/serving.pem"
secret_file "$CLIENT" tls.crt "$WORK/client.pem"
secret_file "$CLIENT" ca.crt "$WORK/client-ca.pem"
ok '[ "$(sans_of "$WORK/serving.pem")" = "$WANT_SANS" ]' "serving SANs '$(sans_of "$WORK/serving.pem")', want '$WANT_SANS'"
ok 'openssl verify -CAfile "$WORK/ca.pem" "$WORK/serving.pem" >/dev/null 2>&1' "serving does not chain to the CA"
ok 'openssl x509 -in "$WORK/serving.pem" -noout -text | grep -q "TLS Web Server Authentication"' "serving lacks the serverAuth usage"
ok '[ "$(sans_of "$WORK/client.pem")" = "wva-keda-client " ]' "client SANs '$(sans_of "$WORK/client.pem")', want 'wva-keda-client'"
ok 'openssl x509 -in "$WORK/client.pem" -noout -text | grep -q "TLS Web Client Authentication"' "client lacks the clientAuth usage"
ok 'openssl verify -CAfile "$WORK/ca.pem" "$WORK/client.pem" >/dev/null 2>&1' "client does not chain to the CA"
ok 'cmp -s "$WORK/client-ca.pem" "$WORK/ca.pem"' "the client Secret's ca.crt (the controller's client CA) is not the CA"
ok '[ -n "$WVA_SCALER_CLIENT_CA_HASH" ] && [ "$WVA_SCALER_CLIENT_CA_HASH" = "$(sha_of "$WORK/ca.pem")" ]' "client CA digest '$WVA_SCALER_CLIENT_CA_HASH' is not the CA's"
ok 'grep -q -- "--server-side" "$WORK/kubectl.log" && ! grep -q "^apply -f -" "$WORK/kubectl.log"' "Secrets not written server-side (a client-side apply copies the key into an annotation)"
cp "$WORK/k/kustomization.yaml" "$WORK/kustomization.tls"
FIRST_HASH="$WVA_SCALER_CLIENT_CA_HASH"
cp "$WORK/ca.pem" "$WORK/ca.first"

echo "== openssl: a re-run with nothing to renew writes nothing"
printf 'namespace: %s\nresources:\n- ./base\npatches:\n' "$NS" >"$WORK/k/kustomization.yaml"
put_deploy openssl
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' STUB_CM=absent
ok '[ "$RC" = 0 ] && [ ! -s "$WORK/writes" ]' "unchanged re-run: exit $RC, wrote: $(tr '\n' ' ' <"$WORK/writes")"
ok '[ "$WVA_SCALER_CLIENT_CA_HASH" = "$FIRST_HASH" ]' "unchanged re-run moved the client CA digest (the controller would restart for nothing)"
ok 'grep -q -- "--external-scaler-cert-path" "$WORK/k/kustomization.yaml"' "kept-on re-run did not render the TLS flags"

# reissue_case <label> <mutation>: mutates the stored serving cert, re-runs,
# and asserts the serving leaf alone was re-issued, CA and digest kept.
reissue_case() {
    echo "== openssl: re-issues a serving certificate that $1"
    eval "$2"
    printf 'namespace: %s\nresources:\n- ./base\npatches:\n' "$NS" >"$WORK/k/kustomization.yaml"
    run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' STUB_CM=absent
    secret_file "$SERVING" tls.crt "$WORK/serving.new"
    ok '[ "$RC" = 0 ] && [ "$(tr "\n" " " <"$WORK/writes")" = "secret/$SERVING " ]' "$1: exit $RC, wrote '$(tr '\n' ' ' <"$WORK/writes")', want the serving Secret alone"
    ok '[ "$(sans_of "$WORK/serving.new")" = "$WANT_SANS" ] && openssl x509 -in "$WORK/serving.new" -noout -checkend $((300 * 86400)) >/dev/null 2>&1' "$1: the new serving certificate is not a full-length one naming both hosts"
    ok '[ "$WVA_SCALER_CLIENT_CA_HASH" = "$FIRST_HASH" ]' "$1: the client CA changed, which restarts the controller for a leaf it hot-reloads"
}

# mint_serving <days> <names...>: a serving cert signed by the stored CA.
mint_serving() {
    local days="$1" san="" n
    shift
    for n in "$@"; do san="${san:+$san,}DNS:$n"; done
    secret_file "$CA" tls.crt "$WORK/m-ca.crt"
    secret_file "$CA" tls.key "$WORK/m-ca.key"
    printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\n' "$san" >"$WORK/m.ext"
    openssl req -new -newkey rsa:2048 -nodes -subj /CN=x -keyout "$WORK/m.key" -out "$WORK/m.csr" >/dev/null 2>&1
    openssl x509 -req -in "$WORK/m.csr" -CA "$WORK/m-ca.crt" -CAkey "$WORK/m-ca.key" -set_serial 7 \
        -days "$days" -extfile "$WORK/m.ext" -out "$WORK/m.crt" >/dev/null 2>&1
    jq --arg c "$(base64 <"$WORK/m.crt" | tr -d '\n')" '.data["tls.crt"] = $c' "$STORE/secret__$SERVING.json" >"$WORK/m.json"
    mv "$WORK/m.json" "$STORE/secret__$SERVING.json"
}
reissue_case "expires within 30 days" "mint_serving 10 wva-external-scaler.$NS.svc wva-external-scaler.$NS.svc.cluster.local"
reissue_case "does not name svc.cluster.local" "mint_serving 365 wva-external-scaler.$NS.svc"

echo "== openssl: a missing CA re-issues everything, and moves the digest"
rm -f "$STORE/secret__$CA.json"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' STUB_CM=absent
secret_file "$CA" tls.crt "$WORK/ca.new"
secret_file "$SERVING" tls.crt "$WORK/serving.new"
ok '[ "$RC" = 0 ] && [ "$(sort "$WORK/writes" | tr "\n" " ")" = "secret/$CA secret/$CLIENT secret/$SERVING " ]' "new CA: wrote '$(sort "$WORK/writes" | tr '\n' ' ')', want all three"
ok 'openssl verify -CAfile "$WORK/ca.new" "$WORK/serving.new" >/dev/null 2>&1' "new CA: serving not re-signed by it"
ok '[ "$WVA_SCALER_CLIENT_CA_HASH" = "$(sha_of "$WORK/ca.new")" ] && [ "$WVA_SCALER_CLIENT_CA_HASH" != "$FIRST_HASH" ]' "new CA: the digest did not move, so the controller keeps the old client CA"

echo "== openssl: a Secret this installer did not write is never overwritten"
reset_store
put_foreign_secret "$SERVING"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=absent
ok '[ "$RC" = 1 ] && grep -q "not created by this installer" "$WORK/refusal"' "foreign Secret: exit $RC, $(cat "$WORK/refusal")"
ok '[ ! -s "$WORK/writes" ] && [ "$(jq -r .data[\"tls.crt\"] "$STORE/secret__$SERVING.json")" = Zm9yZWlnbgo= ]' "foreign Secret: something was written ($(tr '\n' ' ' <"$WORK/writes"))"

echo "== openssl: an unreadable Secret is a refusal, not absent"
reset_store
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=absent STUB_SECRETS=FORBIDDEN
ok '[ "$RC" = 1 ] && grep -q "Cannot read" "$WORK/refusal" && [ ! -s "$WORK/writes" ]' "Forbidden Secret: exit $RC, $(cat "$WORK/refusal")"

echo "== openssl: refuses clearly when openssl is missing"
reset_store
mkdir -p "$WORK/noopenssl"
for t in jq grep head sed tr awk cat; do ln -sf "$(command -v "$t")" "$WORK/noopenssl/$t"; done
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=absent "PATH=$WORK/noopenssl"
ok '[ "$RC" = 1 ] && grep -q "openssl" "$WORK/refusal" && [ ! -s "$WORK/writes" ]' "no openssl: exit $RC, $(cat "$WORK/refusal")"

# ---------------------------------------------------------------- OpenShift
echo "== OpenShift: the service CA issues the serving certificate; the installer does not"
reset_store
printf 'namespace: %s\nresources:\n- ./base\npatches:\n' "$NS" >"$WORK/k/kustomization.yaml"
put_foreign_secret "$SERVING" '{"service.beta.openshift.io/originating-service-name":"wva-external-scaler"}'
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=absent ENVIRONMENT=openshift
ok '[ "$RC" = 0 ] && [ "$(sort "$WORK/writes" | tr "\n" " ")" = "secret/$CA secret/$CLIENT " ]' "OpenShift: exit $RC, wrote '$(sort "$WORK/writes" | tr '\n' ' ')', want the CA and client only: $(cat "$WORK/refusal")"
ok 'grep -q "service.beta.openshift.io/serving-cert-secret-name: $SERVING" "$WORK/k/kustomization.yaml"' "OpenShift: no serving-cert annotation on the scaler Service"
cp "$WORK/k/kustomization.yaml" "$WORK/kustomization.openshift"

echo "== OpenShift: a serving Secret neither the service CA nor the installer wrote is refused"
reset_store
put_foreign_secret "$SERVING"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=absent ENVIRONMENT=openshift
ok '[ "$RC" = 1 ] && grep -q "service CA" "$WORK/refusal" && [ ! -s "$WORK/writes" ]' "OpenShift foreign Secret: exit $RC, $(cat "$WORK/refusal")"

# ---------------------------------------------------------------- cert-manager
echo "== cert-manager: Issuer chain and Certificates, labelled, waited for"
reset_store
printf 'namespace: %s\nresources:\n- ./base\npatches:\n' "$NS" >"$WORK/k/kustomization.yaml"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=present
ok '[ "$RC" = 0 ] && [ -f "$WORK/cm-applied.yaml" ]' "cert-manager: exit $RC, nothing applied: $(cat "$WORK/refusal")"
CMA="$WORK/cm-applied.yaml"
ok '[ "$(yqs "select(.kind == \"Issuer\") | .metadata.name" "$CMA" | sort | tr "\n" " ")" = "wva-external-scaler-ca wva-external-scaler-selfsigned " ]' "cert-manager: Issuers $(yqs 'select(.kind == "Issuer") | .metadata.name' "$CMA" 2>/dev/null | tr '\n' ' ')"
ok '[ "$(yqs "select(.kind == \"Certificate\") | .metadata.name" "$CMA" | sort | tr "\n" " ")" = "wva-external-scaler wva-external-scaler-ca wva-external-scaler-client " ]' "cert-manager: Certificates $(yqs 'select(.kind == "Certificate") | .metadata.name' "$CMA" 2>/dev/null | tr '\n' ' ')"
ok '[ "$(yqs "select(.metadata.labels[\"wva.llmd.ai/scaler-tls\"] == \"cert-manager\") | .metadata.name" "$CMA" | grep -c .)" = 5 ]' "cert-manager: not every object carries the ownership label"
ok '[ "$(yqs "select(.kind == \"Certificate\" and .spec.secretTemplate.labels[\"wva.llmd.ai/scaler-tls\"] == \"cert-manager\") | .metadata.name" "$CMA" | grep -c .)" = 3 ]' "cert-manager: a Certificate does not stamp the label on its Secret"
ok '[ "$(yqs "select(.metadata.name == \"wva-external-scaler\") | .spec.dnsNames | sort | join(\" \")" "$CMA") " = "$WANT_SANS" ]' "cert-manager: serving dnsNames are not both Service hosts"
ok 'yqs "select(.metadata.name == \"wva-external-scaler-client\") | .spec.usages[]" "$CMA" | grep -qx "client auth"' "cert-manager: the client Certificate lacks client auth"
ok 'yqs "select(.metadata.name == \"wva-external-scaler-ca\") | .spec.isCA" "$CMA" | grep -qx true' "cert-manager: the CA Certificate is not isCA"
ok '[ "$(grep -c "^wait --for=condition=Ready certificates.cert-manager.io/" "$WORK/kubectl.log")" = 3 ]' "cert-manager: did not wait for each of the 3 Certificates"
printf 'FAKE-CERT-MANAGER-CA\n' >"$WORK/cmca"
ok '[ "$WVA_SCALER_CLIENT_CA_HASH" = "$(sha_of "$WORK/cmca")" ]' "cert-manager: the digest is not the issued client CA's"
ok '[ "$(grep -c "^apply --server-side" "$WORK/kubectl.log")" = 0 ]' "cert-manager: the installer wrote Secrets itself"
ok 'grep -q "value: cert-manager" "$WORK/k/kustomization.yaml"' "cert-manager: the provider is not recorded on the pod template"

echo "== cert-manager: a re-run accepts its own objects"
printf 'namespace: %s\nresources:\n- ./base\npatches:\n' "$NS" >"$WORK/k/kustomization.yaml"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=present
ok '[ "$RC" = 0 ]' "cert-manager re-run: exit $RC, $(cat "$WORK/refusal")"

echo "== cert-manager: a Certificate it did not create is refused"
reset_store
jq -n '{kind: "Certificate", metadata: {name: "wva-external-scaler"}}' >"$STORE/certificates.cert-manager.io__wva-external-scaler.json"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=present
ok '[ "$RC" = 1 ] && grep -q "not created by this installer" "$WORK/refusal" && [ ! -f "$WORK/cm-applied.yaml" ]' "foreign Certificate: exit $RC, $(cat "$WORK/refusal")"

echo "== cert-manager: a webhook that does not answer is a refusal before anything is written"
reset_store
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=present STUB_DRYRUN=fail
ok '[ "$RC" = 1 ] && grep -q "WVA_SCALER_TLS_PROVIDER=openssl" "$WORK/refusal" && [ ! -f "$WORK/cm-applied.yaml" ]' "dry-run failure: exit $RC, $(cat "$WORK/refusal")"

echo "== cert-manager: a Certificate that never becomes Ready is a refusal"
reset_store
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=present STUB_WAIT=fail
ok '[ "$RC" = 1 ] && grep -q "not Ready" "$WORK/refusal"' "wait failure: exit $RC, $(cat "$WORK/refusal")"

echo "== cert-manager on OpenShift: no serving Certificate"
reset_store
printf 'namespace: %s\nresources:\n- ./base\npatches:\n' "$NS" >"$WORK/k/kustomization.yaml"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=present ENVIRONMENT=openshift
ok '[ "$RC" = 0 ] && ! yqs "select(.kind == \"Certificate\") | .metadata.name" "$WORK/cm-applied.yaml" | grep -qx wva-external-scaler' "cert-manager on OpenShift: exit $RC, or a serving Certificate competes with the service CA"

# ---------------------------------------------------------------- the Deployment
# render_case <label> <overlay> <kustomization patches file or ""> <want on|off>
render_case() {
    local label="$1" overlay="$2" patches="$3" want="$4" dir="$WORK/render" out="$WORK/rendered.yaml" dep="$WORK/dep.yaml"
    echo "== render: $label"
    rm -rf "$dir"
    mkdir -p "$dir"
    ln -s "$ROOT/$overlay" "$dir/base"
    if [ -n "$patches" ]; then cp "$patches" "$dir/kustomization.yaml"; else printf 'namespace: %s\nresources:\n- ./base\n' "$NS" >"$dir/kustomization.yaml"; fi
    if ! render "$dir" "$out" 2>"$WORK/err"; then
        fail "$label: does not render: $(head -3 "$WORK/err")"
        return
    fi
    yq 'select(.kind == "Deployment" and .metadata.name == "wva-controller-manager")' "$out" >"$dep"
    local flags mounts vols ann
    flags="$(yq '.spec.template.spec.containers[] | select(.name == "manager") | .args[] | select(test("^--external-scaler-(cert-path|client-ca|client-san)="))' "$dep" | sort | tr '\n' ' ')"
    mounts="$(yq '.spec.template.spec.containers[] | select(.name == "manager") | .volumeMounts[] | select(.name == "scaler-tls" or .name == "scaler-client-ca") | .mountPath' "$dep" | sort | tr '\n' ' ')"
    vols="$(yq '.spec.template.spec.volumes[] | select(.name == "scaler-tls" or .name == "scaler-client-ca") | .secret.secretName' "$dep" | sort | tr '\n' ' ')"
    ann="$(yq '.spec.template.metadata.annotations["wva.llmd.ai/scaler-client-ca-sha256"] // ""' "$dep")"
    if [ "$want" = on ]; then
        ok '[ "$flags" = "--external-scaler-cert-path=/etc/wva/scaler-tls --external-scaler-client-ca=/etc/wva/scaler-client-ca/ca.crt --external-scaler-client-san=wva-keda-client " ]' "$label: flags '$flags'"
        ok '[ "$mounts" = "/etc/wva/scaler-client-ca /etc/wva/scaler-tls " ]' "$label: mounts '$mounts'"
        ok '[ "$vols" = "$CLIENT $SERVING " ]' "$label: volumes '$vols'"
        ok '[ "$(yq ".spec.template.spec.volumes[] | select(.name == \"scaler-client-ca\") | .secret.items[].key" "$dep" | tr "\n" " ")" = "ca.crt " ]' "$label: the controller mounts more of the client Secret than its CA (the client key belongs to KEDA)"
        ok '[ -n "$ann" ]' "$label: no client-CA digest on the pod template, so a new CA never restarts the controller"
    else
        ok '[ -z "$flags$mounts$vols$ann" ]' "$label: TLS is off but rendered: flags '$flags' mounts '$mounts' volumes '$vols' digest '$ann'"
    fi
    local svc_ann
    svc_ann="$(yq 'select(.kind == "Service" and .metadata.name == "wva-external-scaler") | .metadata.annotations["service.beta.openshift.io/serving-cert-secret-name"] // ""' "$out" \
        | grep -vx -e '' -e '---' || true)"
    # (yq 4.44 prints blank lines and --- for the documents a select drops.)
    case "$label" in
        *OpenShift*on*) ok '[ "$svc_ann" = "$SERVING" ]' "$label: scaler Service annotation '$svc_ann', want $SERVING" ;;
        *) ok '[ -z "$svc_ann" ]' "$label: the scaler Service asks the service CA for a certificate ('$svc_ann')" ;;
    esac
}
for overlay in config/overlays/cluster-scoped/kubernetes config/overlays/namespace-scoped/kubernetes \
    config/overlays/cluster-scoped/openshift config/overlays/namespace-scoped/openshift; do
    render_case "$overlay, TLS off" "$overlay" "" off
    case "$overlay" in
        */openshift) render_case "$overlay, OpenShift TLS on" "$overlay" "$WORK/kustomization.openshift" on ;;
        *) render_case "$overlay, TLS on" "$overlay" "$WORK/kustomization.tls" on ;;
    esac
done

# ---------------------------------------------------------------- verify
# Store the Deployment as the TLS render produced it, and the Secrets it names.
echo "== verify: plaintext is reported as plaintext"
reset_store
put_deploy plaintext
run wva_verify_scaler_tls
ok '[ "$RC" = 0 ] && grep -q "plaintext" "$WORK/info" && [ ! -s "$WORK/warnings" ]' "verify plaintext: exit $RC, info '$(head -2 "$WORK/info")'"

echo "== verify: mutual TLS, expiry, names; a ScaledObject with no authenticationRef is named"
reset_store
printf 'namespace: %s\nresources:\n- ./base\npatches:\n' "$NS" >"$WORK/k/kustomization.yaml"
run 'wva_scaler_tls_prepare "$WORK/k/kustomization.yaml"' WVA_SCALER_TLS=true STUB_CM=absent
rm -rf "$WORK/render"; mkdir -p "$WORK/render"
ln -s "$ROOT/config/overlays/namespace-scoped/kubernetes" "$WORK/render/base"
cp "$WORK/k/kustomization.yaml" "$WORK/render/kustomization.yaml"
if render "$WORK/render" "$WORK/rendered.yaml" 2>"$WORK/err"; then
    yq -o=json 'select(.kind == "Deployment" and .metadata.name == "wva-controller-manager")' "$WORK/rendered.yaml" \
        >"$STORE/deployment__wva-controller-manager.json"
else
    fail "verify: the TLS overlay does not render: $(head -3 "$WORK/err")"
fi
jq -n --arg p "wva-external-scaler.$NS.svc.cluster.local:9090" '{items: [
    {metadata: {namespace: "wva-check", name: "plain"}, spec: {triggers: [{type: "external-push", metadata: {scalerAddress: $p}}]}},
    {metadata: {namespace: "wva-check", name: "authed"}, spec: {triggers: [{type: "external-push", metadata: {scalerAddress: $p}, authenticationRef: {name: "x"}}]}},
    {metadata: {namespace: "wva-check", name: "other"}, spec: {triggers: [{type: "external-push", metadata: {scalerAddress: "some-other-scaler:9090"}}]}}]}' >"$WORK/so.json"
run wva_verify_scaler_tls
ok '[ "$RC" = 0 ] && grep -q "mutual TLS, client identity wva-keda-client" "$WORK/info"' "verify mTLS: mode not reported: $(head -3 "$WORK/info")"
ok 'grep -q "expires" "$WORK/info" && grep -q "wva-external-scaler.$NS.svc.cluster.local" "$WORK/info"' "verify mTLS: expiry or names not reported: $(grep serving "$WORK/info")"
ok 'grep -q "no authenticationRef" "$WORK/warnings" && grep -q "wva-check/plain" "$WORK/stderr" && ! grep -q "wva-check/authed\|wva-check/other" "$WORK/stderr"' "verify mTLS: the unauthenticated ScaledObject is not named alone: $(cat "$WORK/warnings" "$WORK/stderr" | head -4)"

echo "== verify: every ScaledObject authenticated is quiet; unreadable ones are unknown"
jq '.items |= map(select(.metadata.name != "plain"))' "$WORK/so.json" >"$WORK/so2.json" && mv "$WORK/so2.json" "$WORK/so.json"
run wva_verify_scaler_tls
ok '[ ! -s "$WORK/warnings" ] && grep -q "carries an authenticationRef" "$WORK/info"' "verify, all authenticated: $(head -2 "$WORK/warnings")"
run wva_verify_scaler_tls STUB_SO=FORBIDDEN
ok '[ ! -s "$WORK/warnings" ] && grep -q "Could not list ScaledObjects" "$WORK/info"' "verify, ScaledObjects unreadable: reported as none or as a warning"

echo "== verify: a certificate missing the scalerAddress host is a warning"
mint_serving 365 "wva-external-scaler.$NS.svc"
run wva_verify_scaler_tls
ok 'grep -q "does not name wva-external-scaler.$NS.svc.cluster.local" "$WORK/warnings"' "verify: missing SAN not warned: $(head -2 "$WORK/warnings")"

# ---------------------------------------------------------------- permissions, undeploy
echo "== permissions: what the controller phase must be allowed to create"
reset_store
run 'wva_scaler_tls_permission_needs' WVA_SCALER_TLS=true STUB_CM=absent
ok '[ "$(tr "\n" " " <"$WORK/stdout")" = "secrets " ]' "permissions, openssl: '$(tr '\n' ' ' <"$WORK/stdout")'"
run 'wva_scaler_tls_permission_needs' WVA_SCALER_TLS=true STUB_CM=present
ok '[ "$(tr "\n" " " <"$WORK/stdout")" = "secrets certificates.cert-manager.io issuers.cert-manager.io " ]' "permissions, cert-manager: '$(tr '\n' ' ' <"$WORK/stdout")'"
run 'wva_scaler_tls_permission_needs'
ok '[ "$RC" = 0 ] && [ ! -s "$WORK/stdout" ]' "permissions, TLS off: asked for '$(tr '\n' ' ' <"$WORK/stdout")'"
put_deploy openssl
run 'wva_scaler_tls_permission_needs'
ok '[ "$(tr "\n" " " <"$WORK/stdout")" = "secrets " ]' "permissions, kept on: '$(tr '\n' ' ' <"$WORK/stdout")'"

echo "== undeploy: removes only what carries the installer label"
run wva_scaler_tls_undeploy STUB_CM=present
ok 'grep -q "^delete secret -n $NS -l wva.llmd.ai/scaler-tls" "$WORK/kubectl.log" && grep -q "^delete certificates.cert-manager.io,issuers.cert-manager.io -n $NS -l wva.llmd.ai/scaler-tls" "$WORK/kubectl.log"' "undeploy: $(tr '\n' ';' <"$WORK/kubectl.log")"

echo ""
if [ "$FAIL" -gt 0 ]; then
    echo "external-scaler TLS: $FAIL failed, $PASS passed"
    exit 1
fi
if [ "$PASS" = 0 ]; then
    echo "FAIL: no assertion ran"
    exit 1
fi
echo "external-scaler TLS OK ($PASS assertions)"
