#!/usr/bin/env bash
#
# Certificates for the KEDA external scaler (:9090), and the controller flags
# that serve them.
#
# The controller serves the scaler over mutual TLS: a serving certificate for
# the scaler Service, a client certificate for KEDA, and the controller pinned to
# that client's identity. KEDA presents its certificate through a
# ClusterTriggerAuthentication the admin phase writes, and every ScaledObject the
# installer writes points at it (see "KEDA's side" below).
#
# WVA_SCALER_TLS unset is the default: TLS once KEDA's side is in place and every
# ScaledObject naming the scaler can follow it; plaintext, with a warning, when
# not. WVA_SCALER_TLS=true makes either of those a refusal. WVA_SCALER_TLS=false
# stays on plaintext. WVA_SCALER_TLS_MIGRATE=true moves existing ScaledObjects
# with the scaler.
#
# Where the certificates come from:
#
#   cert-manager present   an Issuer chain (self-signed root -> CA Issuer) and
#                          three Certificates. cert-manager renews them.
#   no cert-manager        a private CA and two leaves minted here with openssl,
#                          stored as Secrets. A re-install re-issues a leaf that
#                          is missing, expires within WVA_SCALER_RENEW_DAYS, names
#                          other hosts, or no longer chains to the CA. The CA is
#                          kept until it is missing or itself expiring.
#   OpenShift              the SERVING certificate comes from the service CA
#                          (an annotation on the scaler Service); the service CA
#                          issues serving certificates only, so KEDA's client
#                          certificate still comes from cert-manager or openssl.
#
# Never overwrites a Secret, Issuer or Certificate it did not create: everything
# it writes carries the label in WVA_SCALER_TLS_LABEL, and anything else under
# one of its names is a refusal. A Secret it cannot read is a refusal too --
# Forbidden is "unknown", never "absent".
#
# Rotation and the running controller: the serving certificate is watched
# (controller-runtime certwatcher) and reloaded without a restart. The client CA
# is read once at start, so the Deployment's pod template carries its digest:
# a changed CA changes the template and rolls the controller, an unchanged one
# does not.
#
# Requires vars: WVA_NS, ENVIRONMENT. Requires funcs: log_info, log_warning,
# log_error, and kc_capture (common.sh) for the KEDA side; kubectl and jq on PATH
# (openssl for the no-cert-manager path).

WVA_SCALER_TLS_SECRET=wva-external-scaler-tls
WVA_SCALER_CLIENT_SECRET=wva-external-scaler-client-tls
WVA_SCALER_CA_SECRET=wva-external-scaler-ca
WVA_SCALER_TLS_LABEL=wva.llmd.ai/scaler-tls
WVA_SCALER_TLS_ANNOTATION=wva.llmd.ai/scaler-tls
WVA_SCALER_CA_HASH_ANNOTATION=wva.llmd.ai/scaler-client-ca-sha256
WVA_SCALER_CERT_DIR=/etc/wva/scaler-tls
WVA_SCALER_CLIENT_CA_DIR=/etc/wva/scaler-client-ca
WVA_SCALER_CLIENT_SAN_DEFAULT=wva-keda-client
WVA_SCALER_LEAF_DAYS=365
WVA_SCALER_CA_DAYS=3650
# KEDA reads its client certificate when it first dials a scaler address and
# keeps that connection until its operator restarts, so the certificate lives
# as long as the CA: a renewed one would not reach KEDA before a restart (see
# "KEDA's side").
WVA_SCALER_CLIENT_DAYS=$WVA_SCALER_CA_DAYS
WVA_SCALER_RENEW_DAYS=30

# wva_scaler_dns_names echoes the names the serving certificate must carry: the
# Service by its short in-cluster name, and by the fully qualified one the
# installer writes into every ScaledObject's scalerAddress.
wva_scaler_dns_names() {
    printf '%s %s\n' "wva-external-scaler.${WVA_NS}.svc" "wva-external-scaler.${WVA_NS}.svc.cluster.local"
}

# wva_scaler_client_san echoes the identity KEDA's client certificate carries
# and the controller accepts (--external-scaler-client-san).
wva_scaler_client_san() {
    printf '%s\n' "${WVA_SCALER_CLIENT_SAN:-$WVA_SCALER_CLIENT_SAN_DEFAULT}"
}

# wva_scaler_check_client_san refuses a WVA_SCALER_CLIENT_SAN that is not one
# lowercase DNS name. The value goes into an openssl config, a YAML manifest and
# a controller flag; a comma, a newline or a space would add names or keys there.
# Each label is 1 to 63 characters and starts and ends with a letter or digit,
# and the whole name is at most 253, as RFC 1123 has it.
wva_scaler_check_client_san() {
    local san="${WVA_SCALER_CLIENT_SAN:-}" label='[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?'
    local re="^${label}(\.${label})*\$"
    [ -n "$san" ] || return 0
    [ "${#san}" -le 253 ] && [[ "$san" =~ $re ]] && return 0
    log_error "WVA_SCALER_CLIENT_SAN must be one lowercase DNS name: labels of letters, digits and hyphens, each starting and ending with a letter or digit and at most 63 long, joined by single dots, 253 in all (got: ${san})."
}

# wva_scaler_not_found succeeds when kubectl error text $1 is the API server
# saying the object or resource does not exist. Anchored on kubectl's own
# "(NotFound)", never on the bare word: a namespace or object named notfound-prod
# appears in a Forbidden message too, and Forbidden is "unknown", never "absent".
wva_scaler_not_found() {
    printf '%s' "$1" | grep -qF '(NotFound)'
}

# wva_scaler_tls_read_live sets WVA_SCALER_TLS_LIVE from the running controller
# Deployment:
#   absent      no Deployment yet (a new install)
#   unreadable  the read failed for another reason (WVA_SCALER_TLS_LIVE_ERR)
#   plaintext   no TLS flags
#   manual      TLS flags this installer did not set
#   <provider>  installer-provisioned TLS: cert-manager or openssl
wva_scaler_tls_read_live() {
    local out rc=0 provider has_flag
    WVA_SCALER_TLS_LIVE=absent
    WVA_SCALER_TLS_LIVE_ERR=""
    WVA_SCALER_TLS_LIVE_FLAGS=""
    out="$(kubectl get deployment wva-controller-manager -n "$WVA_NS" -o json 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        if wva_scaler_not_found "$out"; then
            return 0
        fi
        WVA_SCALER_TLS_LIVE=unreadable
        WVA_SCALER_TLS_LIVE_ERR="$(printf '%s\n' "$out" | head -1)"
        return 0
    fi
    provider="$(printf '%s' "$out" | jq -r --arg k "$WVA_SCALER_TLS_ANNOTATION" \
        '.spec.template.metadata.annotations[$k] // ""' 2>/dev/null)" || provider=""
    has_flag="$(printf '%s' "$out" | jq -r \
        '[.spec.template.spec.containers[]? | (.args // [])[] | select(startswith("--external-scaler-cert-path"))] | length' \
        2>/dev/null)" || has_flag=0
    WVA_SCALER_TLS_LIVE_FLAGS="$(printf '%s' "$out" | jq -r \
        '[.spec.template.spec.containers[]? | (.args // [])[] | select(startswith("--external-scaler-"))] | .[]' \
        2>/dev/null)" || WVA_SCALER_TLS_LIVE_FLAGS=""
    if [ -n "$provider" ]; then
        WVA_SCALER_TLS_LIVE="$provider"
    elif [ "${has_flag:-0}" != 0 ]; then
        WVA_SCALER_TLS_LIVE=manual
    else
        WVA_SCALER_TLS_LIVE=plaintext
    fi
}

# wva_cert_manager_api sets WVA_CERT_MANAGER_API to present, absent or unknown.
# The discovery document is readable by every authenticated user, so a tenant
# gets the same answer an admin does; any error but NotFound is unknown.
wva_cert_manager_api() {
    local out rc=0
    out="$(kubectl get --raw /apis/cert-manager.io 2>&1)" || rc=$?
    if [ "$rc" = 0 ]; then
        WVA_CERT_MANAGER_API=present
    elif wva_scaler_not_found "$out"; then
        WVA_CERT_MANAGER_API=absent
    else
        WVA_CERT_MANAGER_API=unknown
        WVA_CERT_MANAGER_API_ERR="$(printf '%s\n' "$out" | head -1)"
    fi
}

# wva_scaler_object_state reads object $1 (a resource, e.g. secret) named $2 in
# WVA_NS and sets WVA_OBJ_STATE to absent, ours, foreign or unreadable, with the
# object in WVA_OBJ_JSON. "ours" means it carries WVA_SCALER_TLS_LABEL.
wva_scaler_object_state() {
    local resource="$1" name="$2" out rc=0 owner
    WVA_OBJ_JSON=""
    WVA_OBJ_STATE=absent
    WVA_OBJ_ERR=""
    out="$(kubectl get "$resource" "$name" -n "$WVA_NS" -o json 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        if wva_scaler_not_found "$out"; then
            return 0
        fi
        WVA_OBJ_STATE=unreadable
        WVA_OBJ_ERR="$(printf '%s\n' "$out" | head -1)"
        return 0
    fi
    WVA_OBJ_JSON="$out"
    owner="$(printf '%s' "$out" | jq -r --arg k "$WVA_SCALER_TLS_LABEL" '.metadata.labels[$k] // ""' 2>/dev/null)" || owner=""
    if [ -n "$owner" ]; then
        WVA_OBJ_STATE=ours
    else
        WVA_OBJ_STATE=foreign
    fi
}

# wva_scaler_require_writable refuses unless object $1/$2 is absent or ours.
wva_scaler_require_writable() {
    local resource="$1" name="$2"
    wva_scaler_object_state "$resource" "$name"
    case "$WVA_OBJ_STATE" in
        absent|ours) return 0 ;;
        unreadable)
            wva_scaler_drop_tmp
            log_error "Cannot read $resource/$name in $WVA_NS ($WVA_OBJ_ERR), so whether this installer created it is unknown. Grant get on $resource there, or set WVA_SCALER_TLS=false." ;;
        *)
            wva_scaler_drop_tmp
            log_error "$resource/$name in $WVA_NS was not created by this installer (no $WVA_SCALER_TLS_LABEL label); refusing to overwrite it. Remove or rename it, or set WVA_SCALER_TLS=false." ;;
    esac
}

# wva_scaler_secret_field writes data key $1 of WVA_OBJ_JSON, decoded, to file $2
# (empty when the key is absent).
wva_scaler_secret_field() {
    local key="$1" file="$2" b64
    b64="$(printf '%s' "$WVA_OBJ_JSON" | jq -r --arg k "$key" '.data[$k] // ""' 2>/dev/null)" || b64=""
    if [ -n "$b64" ]; then
        printf '%s' "$b64" | base64 -d > "$file" 2>/dev/null || : > "$file"
    else
        : > "$file"
    fi
}

# wva_scaler_cert_sans echoes the DNS names certificate $1 carries, sorted and
# space-separated.
wva_scaler_cert_sans() {
    openssl x509 -in "$1" -noout -text 2>/dev/null \
        | awk '/Subject Alternative Name/ { getline; print }' \
        | tr ',' '\n' | sed -n 's/^ *DNS://p' | sort -u | tr '\n' ' '
}

# wva_scaler_sorted_names echoes its arguments sorted, unique, space-separated,
# in the form wva_scaler_cert_sans prints.
wva_scaler_sorted_names() {
    printf '%s\n' "$@" | sort -u | tr '\n' ' '
}

# wva_scaler_cert_ok succeeds when certificate $1 chains to CA $2, stays valid
# for WVA_SCALER_RENEW_DAYS more days, and names exactly the hosts in $3.
wva_scaler_cert_ok() {
    local cert="$1" ca="$2" want="$3"
    [ -s "$cert" ] && [ -s "$ca" ] || return 1
    openssl verify -CAfile "$ca" "$cert" >/dev/null 2>&1 || return 1
    openssl x509 -in "$cert" -noout -checkend $((WVA_SCALER_RENEW_DAYS * 86400)) >/dev/null 2>&1 || return 1
    # shellcheck disable=SC2086 # $want is a space-separated list on purpose
    [ "$(wva_scaler_cert_sans "$cert")" = "$(wva_scaler_sorted_names $want)" ]
}

# wva_scaler_new_ca mints a CA into $1/ca.crt and $1/ca.key.
wva_scaler_new_ca() {
    local d="$1"
    printf '[req]\ndistinguished_name=dn\nx509_extensions=v3\nprompt=no\n[dn]\nCN=wva-external-scaler-ca.%s\n[v3]\nbasicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,cRLSign\nsubjectKeyIdentifier=hash\n' \
        "$WVA_NS" > "$d/ca.cnf"
    openssl req -x509 -new -newkey rsa:2048 -nodes -sha256 -days "$WVA_SCALER_CA_DAYS" \
        -keyout "$d/ca.key" -out "$d/ca.crt" -config "$d/ca.cnf" >/dev/null 2>&1
}

# wva_scaler_new_leaf mints $1/$2.crt and $1/$2.key, signed by $1/ca.*, with
# common name $3, extended key usage $4 and the DNS names that follow.
wva_scaler_new_leaf() {
    local d="$1" name="$2" cn="$3" eku="$4" san="" n
    shift 4
    for n in "$@"; do san="${san:+$san,}DNS:$n"; done
    printf '[req]\ndistinguished_name=dn\nprompt=no\n[dn]\nCN=%s\n' "$cn" > "$d/$name.cnf"
    printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=%s\nsubjectAltName=%s\n' \
        "$eku" "$san" > "$d/$name.ext"
    openssl req -new -newkey rsa:2048 -nodes -sha256 -keyout "$d/$name.key" -out "$d/$name.csr" \
        -config "$d/$name.cnf" >/dev/null 2>&1 || return 1
    openssl x509 -req -sha256 -in "$d/$name.csr" -CA "$d/ca.crt" -CAkey "$d/ca.key" \
        -set_serial "0x$(openssl rand -hex 16)" -days "$WVA_SCALER_LEAF_DAYS" \
        -extfile "$d/$name.ext" -out "$d/$name.crt" >/dev/null 2>&1
}

# wva_scaler_write_secret writes kubernetes.io/tls Secret $1 (role $2) from the
# files $3 (tls.crt), $4 (tls.key) and $5 (ca.crt). Server-side apply, so no
# last-applied annotation carries a second copy of the private key; the
# ownership check before it already established the Secret is ours.
wva_scaler_write_secret() {
    local name="$1" role="$2" crt="$3" key="$4" ca="$5"
    jq -n --arg n "$name" --arg ns "$WVA_NS" --arg l "$WVA_SCALER_TLS_LABEL" --arg r "$role" \
        --rawfile crt "$crt" --rawfile key "$key" --rawfile ca "$ca" \
        '{apiVersion: "v1", kind: "Secret", type: "kubernetes.io/tls",
          metadata: {name: $n, namespace: $ns,
                     labels: {"app.kubernetes.io/managed-by": "llm-scaling-manager", ($l): $r}},
          data: {"tls.crt": ($crt | @base64), "tls.key": ($key | @base64), "ca.crt": ($ca | @base64)}}' \
        | kubectl apply --server-side --force-conflicts --field-manager=llm-scaling-manager -f - >/dev/null \
        || { wva_scaler_drop_tmp; log_error "Could not write Secret $name in $WVA_NS."; }
}

# wva_scaler_tmp_begin makes the private directory in WVA_SCALER_TMPD that holds
# private keys while certificates are minted or copied, and arms INT, TERM and
# HUP traps that remove it, so a Ctrl-C or a closed terminal does not leave a
# key on disk.
#
# Those three only, never EXIT: deploy_wva_controller owns the EXIT trap (its
# throw-away overlay), and replacing it would leak that directory instead. The
# handlers exit, which still runs that EXIT trap. The INT, TERM and HUP traps in
# force before are saved and put back by wva_scaler_drop_tmp.
#
# Every command between this and wva_scaler_drop_tmp is guarded (|| ...): the
# installer runs under set -e and pipefail, and an unguarded failure would exit
# there with the key still in the directory.
wva_scaler_tmp_begin() {
    WVA_SCALER_TMPD="$(mktemp -d)" || log_error "Could not create a temporary directory for the scaler certificates."
    chmod 700 "$WVA_SCALER_TMPD" || true
    WVA_SCALER_SAVED_TRAPS="$(trap -p INT TERM HUP)"
    trap 'wva_scaler_drop_tmp; exit 130' INT
    trap 'wva_scaler_drop_tmp; exit 143' TERM
    trap 'wva_scaler_drop_tmp; exit 129' HUP
}

# wva_scaler_drop_tmp removes the directory in WVA_SCALER_TMPD, which holds
# private keys while certificates are minted, before a refusal exits, and puts
# back the INT, TERM and HUP traps wva_scaler_tmp_begin replaced.
wva_scaler_drop_tmp() {
    [ -z "${WVA_SCALER_TMPD:-}" ] || rm -rf "$WVA_SCALER_TMPD"
    WVA_SCALER_TMPD=""
    if [ -n "${WVA_SCALER_SAVED_TRAPS+set}" ]; then
        trap - INT TERM HUP
        eval "$WVA_SCALER_SAVED_TRAPS"
        unset WVA_SCALER_SAVED_TRAPS
    fi
}

# wva_scaler_sha256 echoes the SHA-256 of file $1.
wva_scaler_sha256() {
    openssl dgst -sha256 "$1" 2>/dev/null | awk '{ print $NF }'
}

# wva_scaler_check_openshift_serving makes room for the service CA to write the
# serving Secret: absent or already the service CA's is fine, one this installer
# wrote earlier is removed, anything else is a refusal. Every refusal drops the
# temporary key directory first, in case a caller already made one.
wva_scaler_check_openshift_serving() {
    local svc
    wva_scaler_object_state secret "$WVA_SCALER_TLS_SECRET"
    case "$WVA_OBJ_STATE" in
        absent) return 0 ;;
        unreadable)
            wva_scaler_drop_tmp
            log_error "Cannot read Secret $WVA_SCALER_TLS_SECRET in $WVA_NS ($WVA_OBJ_ERR), so whether the service CA owns it is unknown. Grant get on secrets there, or set WVA_SCALER_TLS=false." ;;
        ours)
            kubectl delete secret "$WVA_SCALER_TLS_SECRET" -n "$WVA_NS" --ignore-not-found >/dev/null 2>&1 \
                || { wva_scaler_drop_tmp; log_error "Could not remove Secret $WVA_SCALER_TLS_SECRET in $WVA_NS so the service CA can issue it."; }
            return 0 ;;
    esac
    svc="$(printf '%s' "$WVA_OBJ_JSON" | jq -r '.metadata.annotations["service.beta.openshift.io/originating-service-name"] // ""' 2>/dev/null)" || svc=""
    [ "$svc" = wva-external-scaler ] && return 0
    wva_scaler_drop_tmp
    log_error "Secret $WVA_SCALER_TLS_SECRET in $WVA_NS was not issued by the OpenShift service CA for wva-external-scaler, nor by this installer; refusing to take it over. Remove or rename it, or set WVA_SCALER_TLS=false."
}

# wva_scaler_tls_provision_openssl mints or refreshes the CA and leaves with
# openssl and sets WVA_SCALER_CLIENT_CA_HASH.
wva_scaler_tls_provision_openssl() {
    local d ca_new=false issued="" kept="" serving=true dns san ca_json client_json serving_json=""
    [ "${ENVIRONMENT:-}" = openshift ] && serving=false

    # Every refusal first, before the temporary directory exists, so none can
    # leave a private key (or an empty directory) behind.
    wva_scaler_require_writable secret "$WVA_SCALER_CA_SECRET"
    ca_json="$WVA_OBJ_JSON"
    wva_scaler_require_writable secret "$WVA_SCALER_CLIENT_SECRET"
    client_json="$WVA_OBJ_JSON"
    if [ "$serving" = true ]; then
        wva_scaler_require_writable secret "$WVA_SCALER_TLS_SECRET"
        serving_json="$WVA_OBJ_JSON"
    else
        wva_scaler_check_openshift_serving
    fi

    wva_scaler_tmp_begin
    d="$WVA_SCALER_TMPD"
    WVA_OBJ_JSON="$ca_json"
    wva_scaler_secret_field tls.crt "$d/ca.crt"
    wva_scaler_secret_field tls.key "$d/ca.key"
    WVA_OBJ_JSON="$client_json"
    wva_scaler_secret_field tls.crt "$d/client.old"
    wva_scaler_secret_field ca.crt "$d/client-ca.old"
    WVA_OBJ_JSON="$serving_json"
    wva_scaler_secret_field tls.crt "$d/serving.old"

    # A provider switch: Certificates this installer created would keep
    # rewriting the Secrets below.
    wva_cert_manager_api
    if [ "$WVA_CERT_MANAGER_API" = present ]; then
        kubectl delete certificates.cert-manager.io,issuers.cert-manager.io -n "$WVA_NS" \
            -l "$WVA_SCALER_TLS_LABEL" --ignore-not-found >/dev/null 2>&1 || true
    fi

    if [ ! -s "$d/ca.crt" ] || [ ! -s "$d/ca.key" ] \
        || ! openssl x509 -in "$d/ca.crt" -noout -checkend $((WVA_SCALER_RENEW_DAYS * 86400)) >/dev/null 2>&1; then
        wva_scaler_new_ca "$d" || { wva_scaler_drop_tmp; log_error "openssl could not mint the scaler CA."; }
        wva_scaler_write_secret "$WVA_SCALER_CA_SECRET" ca "$d/ca.crt" "$d/ca.key" "$d/ca.crt"
        ca_new=true
        issued="$issued CA"
    else
        kept="$kept CA"
    fi

    if [ "$serving" = true ]; then
        dns="$(wva_scaler_dns_names)"
        if [ "$ca_new" = true ] || ! wva_scaler_cert_ok "$d/serving.old" "$d/ca.crt" "$dns"; then
            # shellcheck disable=SC2086 # two names, split on purpose
            wva_scaler_new_leaf "$d" serving "wva-external-scaler.${WVA_NS}.svc" serverAuth $dns \
                || { wva_scaler_drop_tmp; log_error "openssl could not mint the scaler serving certificate."; }
            wva_scaler_write_secret "$WVA_SCALER_TLS_SECRET" serving "$d/serving.crt" "$d/serving.key" "$d/ca.crt"
            issued="$issued serving"
        else
            kept="$kept serving"
        fi
    fi

    san="$(wva_scaler_client_san)"
    if [ "$ca_new" = true ] || ! wva_scaler_cert_ok "$d/client.old" "$d/ca.crt" "$san" \
        || ! cmp -s "$d/client-ca.old" "$d/ca.crt"; then
        WVA_SCALER_LEAF_DAYS="$WVA_SCALER_CLIENT_DAYS" wva_scaler_new_leaf "$d" client "$san" clientAuth "$san" \
            || { wva_scaler_drop_tmp; log_error "openssl could not mint the KEDA client certificate."; }
        wva_scaler_write_secret "$WVA_SCALER_CLIENT_SECRET" client "$d/client.crt" "$d/client.key" "$d/ca.crt"
        issued="$issued client"
    else
        kept="$kept client"
    fi

    WVA_SCALER_CLIENT_CA_HASH="$(wva_scaler_sha256 "$d/ca.crt")" || WVA_SCALER_CLIENT_CA_HASH=""
    wva_scaler_drop_tmp
    [ -n "$WVA_SCALER_CLIENT_CA_HASH" ] || log_error "openssl could not digest the scaler CA."
    [ -z "$issued" ] || log_info "  issued:${issued} (openssl; the serving certificate is valid ${WVA_SCALER_LEAF_DAYS} days, KEDA's client certificate ${WVA_SCALER_CLIENT_DAYS} days, the CA ${WVA_SCALER_CA_DAYS} days; a re-install re-issues one within ${WVA_SCALER_RENEW_DAYS} days of its expiry)"
    [ -z "$kept" ] || log_info "  kept, still valid:${kept}"
    if [ "$serving" = false ]; then
        log_info "  serving certificate: issued by the OpenShift service CA into Secret $WVA_SCALER_TLS_SECRET"
    fi
}

# wva_scaler_cert_manager_manifest prints the Issuers and Certificates.
wva_scaler_cert_manager_manifest() {
    local labels nested san dns1 dns2
    san="$(wva_scaler_client_san)"
    read -r dns1 dns2 <<< "$(wva_scaler_dns_names)"
    labels="  labels:
    app.kubernetes.io/managed-by: llm-scaling-manager
    ${WVA_SCALER_TLS_LABEL}: cert-manager"
    # The same labels one level deeper, under secretTemplate, so cert-manager
    # stamps them on the Secrets it writes and they read as this installer's.
    nested="$(printf '%s\n' "$labels" | sed 's/^/  /')"
    cat <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: wva-external-scaler-selfsigned
  namespace: ${WVA_NS}
${labels}
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wva-external-scaler-ca
  namespace: ${WVA_NS}
${labels}
spec:
  isCA: true
  commonName: wva-external-scaler-ca.${WVA_NS}
  secretName: ${WVA_SCALER_CA_SECRET}
  duration: $((WVA_SCALER_CA_DAYS * 24))h
  renewBefore: $((WVA_SCALER_RENEW_DAYS * 24))h
  privateKey:
    algorithm: RSA
    size: 2048
    rotationPolicy: Never
  issuerRef:
    name: wva-external-scaler-selfsigned
    kind: Issuer
  secretTemplate:
${nested}
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: wva-external-scaler-ca
  namespace: ${WVA_NS}
${labels}
spec:
  ca:
    secretName: ${WVA_SCALER_CA_SECRET}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wva-external-scaler-client
  namespace: ${WVA_NS}
${labels}
spec:
  commonName: ${san}
  dnsNames:
  - ${san}
  secretName: ${WVA_SCALER_CLIENT_SECRET}
  duration: $((WVA_SCALER_CLIENT_DAYS * 24))h
  renewBefore: $((WVA_SCALER_RENEW_DAYS * 24))h
  usages:
  - client auth
  - digital signature
  - key encipherment
  issuerRef:
    name: wva-external-scaler-ca
    kind: Issuer
  secretTemplate:
${nested}
EOF
    [ "${ENVIRONMENT:-}" = openshift ] && return 0
    cat <<EOF
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wva-external-scaler
  namespace: ${WVA_NS}
${labels}
spec:
  commonName: ${dns1}
  dnsNames:
  - ${dns1}
  - ${dns2}
  secretName: ${WVA_SCALER_TLS_SECRET}
  duration: $((WVA_SCALER_LEAF_DAYS * 24))h
  renewBefore: $((WVA_SCALER_RENEW_DAYS * 24))h
  usages:
  - server auth
  - digital signature
  - key encipherment
  issuerRef:
    name: wva-external-scaler-ca
    kind: Issuer
  secretTemplate:
${nested}
EOF
}

# wva_scaler_tls_provision_cert_manager applies the Issuer chain and the
# Certificates, waits for them, and sets WVA_SCALER_CLIENT_CA_HASH. When
# cert-manager does not accept or issue them, the default gives up on TLS for
# this run (WVA_SCALER_TLS_WANTED=false) and WVA_SCALER_TLS=true refuses.
wva_scaler_tls_provision_cert_manager() {
    local d out rc=0 cert certs="wva-external-scaler-ca wva-external-scaler-client" secret
    [ "${ENVIRONMENT:-}" = openshift ] || certs="$certs wva-external-scaler"
    for cert in wva-external-scaler-selfsigned wva-external-scaler-ca; do
        wva_scaler_require_writable issuers.cert-manager.io "$cert"
    done
    for cert in $certs; do
        wva_scaler_require_writable certificates.cert-manager.io "$cert"
    done
    for secret in "$WVA_SCALER_CA_SECRET" "$WVA_SCALER_CLIENT_SECRET"; do
        wva_scaler_require_writable secret "$secret"
    done
    if [ "${ENVIRONMENT:-}" = openshift ]; then
        wva_scaler_check_openshift_serving
    else
        wva_scaler_require_writable secret "$WVA_SCALER_TLS_SECRET"
    fi

    d="$(mktemp -d)"
    wva_scaler_cert_manager_manifest > "$d/certs.yaml"
    # A dry run on the server goes through cert-manager's webhook: CRDs that are
    # installed with no webhook answering fail here, before anything is written.
    out="$(kubectl apply --dry-run=server -f "$d/certs.yaml" 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        rm -rf "$d"
        wva_scaler_tls_give_up "cert-manager is installed but would not accept the scaler's Issuers and Certificates in $WVA_NS:
$(printf '%s\n' "$out" | head -3)
Fix cert-manager (or the permission), or re-run with WVA_SCALER_TLS_PROVIDER=openssl." \
            "cert-manager would not accept the scaler's Issuers and Certificates in $WVA_NS ($(printf '%s\n' "$out" | head -1)); fix cert-manager, or re-run with WVA_SCALER_TLS_PROVIDER=openssl"
        return 0
    fi
    if ! kubectl apply -f "$d/certs.yaml" >/dev/null; then
        rm -rf "$d"
        wva_scaler_tls_give_up "Could not apply the scaler Issuers and Certificates in $WVA_NS." \
            "the scaler Issuers and Certificates could not be applied in $WVA_NS"
        return 0
    fi
    for cert in $certs; do
        if ! kubectl wait --for=condition=Ready "certificates.cert-manager.io/$cert" -n "$WVA_NS" --timeout=120s >/dev/null 2>&1; then
            rm -rf "$d"
            wva_scaler_tls_give_up "Certificate $cert in $WVA_NS is not Ready after 120s. See: kubectl describe certificate $cert -n $WVA_NS" \
                "Certificate $cert in $WVA_NS is not Ready after 120s (kubectl describe certificate $cert -n $WVA_NS)"
            return 0
        fi
    done

    wva_scaler_object_state secret "$WVA_SCALER_CLIENT_SECRET"
    wva_scaler_secret_field ca.crt "$d/ca.crt"
    if [ ! -s "$d/ca.crt" ]; then
        rm -rf "$d"
        wva_scaler_tls_give_up "Secret $WVA_SCALER_CLIENT_SECRET in $WVA_NS has no ca.crt after issuance (${WVA_OBJ_ERR:-empty}), so the controller would have no client CA." \
            "Secret $WVA_SCALER_CLIENT_SECRET in $WVA_NS has no ca.crt after issuance (${WVA_OBJ_ERR:-empty})"
        return 0
    fi
    WVA_SCALER_CLIENT_CA_HASH="$(wva_scaler_sha256 "$d/ca.crt")" || WVA_SCALER_CLIENT_CA_HASH=""
    rm -rf "$d"
    [ -n "$WVA_SCALER_CLIENT_CA_HASH" ] || log_error "openssl could not digest the scaler client CA."
    log_info "  cert-manager Certificates Ready: $certs (cert-manager renews them; the serving certificate reloads without a restart)"
    if [ "${ENVIRONMENT:-}" = openshift ]; then
        log_info "  serving certificate: issued by the OpenShift service CA into Secret $WVA_SCALER_TLS_SECRET"
    fi
}

# wva_append_scaler_tls_patch points the controller at the certificates: the
# three external-scaler flags, the two Secret mounts, and the pod-template
# annotations that record the provider and the client CA digest. On OpenShift it
# also asks the service CA for the serving certificate. Appends to the
# `patches:` list of kustomization $1, which must already exist.
wva_append_scaler_tls_patch() {
    local kustomization="$1" san
    san="$(wva_scaler_client_san)"
    cat >> "$kustomization" <<PATCH
- patch: |-
    - op: test
      path: /spec/template/spec/containers/0/name
      value: manager
    - op: add
      path: /spec/template/spec/containers/0/args/-
      value: --external-scaler-cert-path=${WVA_SCALER_CERT_DIR}
    - op: add
      path: /spec/template/spec/containers/0/args/-
      value: --external-scaler-client-ca=${WVA_SCALER_CLIENT_CA_DIR}/ca.crt
    - op: add
      path: /spec/template/spec/containers/0/args/-
      value: --external-scaler-client-san=${san}
    - op: add
      path: /spec/template/spec/containers/0/volumeMounts/-
      value: {name: scaler-tls, mountPath: ${WVA_SCALER_CERT_DIR}, readOnly: true}
    - op: add
      path: /spec/template/spec/containers/0/volumeMounts/-
      value: {name: scaler-client-ca, mountPath: ${WVA_SCALER_CLIENT_CA_DIR}, readOnly: true}
    - op: add
      path: /spec/template/spec/volumes/-
      value: {name: scaler-tls, secret: {secretName: ${WVA_SCALER_TLS_SECRET}, items: [{key: tls.crt, path: tls.crt}, {key: tls.key, path: tls.key}]}}
    - op: add
      path: /spec/template/spec/volumes/-
      value: {name: scaler-client-ca, secret: {secretName: ${WVA_SCALER_CLIENT_SECRET}, items: [{key: ca.crt, path: ca.crt}]}}
    - op: add
      path: /spec/template/metadata/annotations/wva.llmd.ai~1scaler-tls
      value: ${WVA_SCALER_TLS_PROVIDER_RESOLVED}
    - op: add
      path: /spec/template/metadata/annotations/wva.llmd.ai~1scaler-client-ca-sha256
      value: "${WVA_SCALER_CLIENT_CA_HASH}"
  target:
    kind: Deployment
    name: wva-controller-manager
PATCH
    [ "${ENVIRONMENT:-}" = openshift ] || return 0
    cat >> "$kustomization" <<PATCH
- patch: |-
    apiVersion: v1
    kind: Service
    metadata:
      name: wva-external-scaler
      annotations:
        service.beta.openshift.io/serving-cert-secret-name: ${WVA_SCALER_TLS_SECRET}
  target:
    kind: Service
    name: wva-external-scaler
PATCH
}

# --- KEDA's side -------------------------------------------------------------
#
# KEDA presents its client certificate through a ClusterTriggerAuthentication,
# and a ClusterTriggerAuthentication reads Secrets only in KEDA's own
# namespace (KEDA_CLUSTER_OBJECT_NAMESPACE on the operator). So the ADMIN phase
# copies the client Secret there and writes the ClusterTriggerAuthentication.
# The copy never goes to a model namespace: whoever can read it can pose as KEDA.
# A ConfigMap in WVA_NS (WVA_SCALER_KEDA_MARKER) records what was written, so the
# controller phase, which cannot read KEDA's namespace, can tell KEDA is ready.
#
# One KEDA fact decides the scalerAddress (pkg/scalers/external_scaler.go,
# v2.18): KEDA keeps ONE gRPC connection per scalerAddress string, built with the
# TLS settings of the first ScaledObject that dials it, and never closes it while
# KEDA runs. A ScaledObject's TLS settings therefore do nothing on an address KEDA
# already dialled in plaintext. TLS ScaledObjects use the Service's short name,
# wva-external-scaler.<ns>.svc:9090; plaintext ones keep
# wva-external-scaler.<ns>.svc.cluster.local:9090. Same Service, a different
# pool key: a fresh connection, and no KEDA restart. The serving certificate
# names both hosts.

WVA_SCALER_KEDA_MARKER=wva-scaler-keda-auth

# wva_scaler_mixed_risk echoes, for a message, what a ScaledObject whose
# trigger does not match the scaler's mode risks. It is not simply "broken":
# on a real cluster, one on the TLS address without the authenticationRef was
# Ready and scaled, because it rode the connection an authenticated one had
# opened. Which one dials first after KEDA starts decides it.
wva_scaler_mixed_risk() {
    printf '%s' "This is unsupported, and depends on order. KEDA keeps ONE connection per scaler address, opened with the settings of whichever ScaledObject dials first after KEDA starts. These may work now only because another ScaledObject opened that connection. After a KEDA operator restart, if one of them dials first, the connection is opened wrong and EVERY ScaledObject on that address can stop scaling, until they are fixed and KEDA is restarted again."
}

# wva_scaler_keda_auth_name echoes the name of the ClusterTriggerAuthentication,
# and of the Secret copy it reads, for the controller in namespace $1 (WVA_NS).
wva_scaler_keda_auth_name() {
    printf 'wva-external-scaler-%s\n' "${1:-$WVA_NS}"
}

# wva_scaler_address echoes the scalerAddress for the scaler in namespace $1:
# the TLS form when $2 is tls, the plaintext form otherwise.
wva_scaler_address() {
    if [ "${2:-plaintext}" = tls ]; then
        printf 'wva-external-scaler.%s.svc:9090\n' "$1"
    else
        printf 'wva-external-scaler.%s.svc.cluster.local:9090\n' "$1"
    fi
}

# wva_scaler_mode_of echoes how the controller in namespace $1 serves the scaler:
#   tls        mutual TLS this installer provisioned
#   manual     TLS flags set by hand (no KEDA side from this installer)
#   plaintext  no TLS, or no controller there
#   unknown    its Deployment could not be read
wva_scaler_mode_of() {
    local out rc=0
    out="$(kubectl get deployment wva-controller-manager -n "$1" -o json 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        if wva_scaler_not_found "$out"; then echo plaintext; else echo unknown; fi
        return 0
    fi
    printf '%s' "$out" | jq -r --arg a "$WVA_SCALER_TLS_ANNOTATION" '
        ([.spec.template.spec.containers[]? | select(.name == "manager") | (.args // [])[]
          | select(startswith("--external-scaler-cert-path"))] | length) as $f
        | (.spec.template.metadata.annotations[$a] // "") as $p
        | if $f == 0 then "plaintext" elif $p != "" then "tls" else "manual" end' 2>/dev/null || echo unknown
}

# wva_scaler_address_mode echoes the mode a ScaledObject for the scaler in
# namespace $1 is written in: tls or plaintext, as wva_scaler_mode_of reads it
# (hand-set TLS is plaintext here: whoever set it owns KEDA's side). When the
# Deployment cannot be read, an explicit WVA_SCALER_TLS=true or false says
# which; with neither it echoes unknown, and the caller warns.
wva_scaler_address_mode() {
    local mode
    mode="$(wva_scaler_mode_of "$1")"
    if [ "$mode" = unknown ]; then
        case "${WVA_SCALER_TLS:-}" in
            true) mode=tls ;;
            false) mode=plaintext ;;
        esac
    fi
    case "$mode" in
        tls|unknown) printf '%s\n' "$mode" ;;
        *) echo plaintext ;;
    esac
}

# wva_scaler_trigger_auth_json echoes the authenticationRef a trigger naming the
# TLS scaler in namespace $1 carries.
wva_scaler_trigger_auth_json() {
    jq -nc --arg n "$(wva_scaler_keda_auth_name "$1")" '{name: $n, kind: "ClusterTriggerAuthentication"}'
}

# wva_scaler_install_puts_keda succeeds when this install itself installs KEDA,
# which deploy_scaler_backend does before the controller: on kind, and on
# Kubernetes with KEDA_HELM_INSTALL=true.
wva_scaler_install_puts_keda() {
    [ "${SCALER_BACKEND:-keda}" = keda ] || return 1
    case "${ENVIRONMENT:-}" in
        kind-emulator) return 0 ;;
        kubernetes) [ "${KEDA_HELM_INSTALL:-false}" = true ] ;;
        *) return 1 ;;
    esac
}

# wva_scaler_keda_run_ns sets WVA_SCALER_KEDA_RUN_NS to the namespace KEDA's
# operator runs in, or to empty when it is not known. Only sources a tenant
# cannot choose count: KEDA_NAMESPACE set by the caller (install.sh defaults it
# to keda-system, so only when KEDA_NAMESPACE_EXPLICIT=true), the namespace
# serving the external-metrics APIService, or the namespace this install puts
# KEDA in. Never the NetworkPolicy in WVA_NS, which that namespace's admin can
# edit.
wva_scaler_keda_run_ns() {
    local kns=""
    WVA_SCALER_KEDA_RUN_NS=""
    if [ "${KEDA_NAMESPACE_EXPLICIT:-false}" = true ]; then
        kns="${KEDA_NAMESPACE:-}"
    elif declare -F wva_keda_apiservice_ns >/dev/null && kns="$(wva_keda_apiservice_ns 2>/dev/null)"; then
        :
    elif wva_scaler_install_puts_keda; then
        kns="${KEDA_NAMESPACE:-}"
    else
        kns=""
    fi
    WVA_SCALER_KEDA_RUN_NS="$kns"
}

# wva_scaler_keda_object_ns sets WVA_SCALER_KEDA_OBJ_NS to the namespace KEDA
# reads a ClusterTriggerAuthentication's Secrets from, or to empty when it is
# not known (WVA_SCALER_KEDA_OBJ_WHY says why). The private key is copied there,
# so it starts from wva_scaler_keda_run_ns. KEDA's operator may move it with
# KEDA_CLUSTER_OBJECT_NAMESPACE; when the Deployments there cannot be read, that
# is unknown, not unset, unless KEDA_NAMESPACE was given explicitly.
wva_scaler_keda_object_ns() {
    local kns rc=0 env=""
    WVA_SCALER_KEDA_OBJ_NS=""
    WVA_SCALER_KEDA_OBJ_WHY=""
    wva_scaler_keda_run_ns
    kns="$WVA_SCALER_KEDA_RUN_NS"
    if [ -z "$kns" ]; then
        WVA_SCALER_KEDA_OBJ_WHY="where KEDA runs is not known. Set KEDA_NAMESPACE=<the namespace KEDA runs in>"
        return 0
    fi
    kc_capture get deployments -n "$kns" -o json || rc=$?
    if [ "$rc" != 0 ]; then
        if [ "${KEDA_NAMESPACE_EXPLICIT:-false}" = true ]; then
            WVA_SCALER_KEDA_OBJ_NS="$kns"
            return 0
        fi
        WVA_SCALER_KEDA_OBJ_WHY="the Deployments in $kns, where KEDA runs, cannot be read ($(printf '%s\n' "$KC_ERR" | head -1)), so whether KEDA_CLUSTER_OBJECT_NAMESPACE moves its Secrets elsewhere is unknown. Grant list on deployments there, or set KEDA_NAMESPACE=$kns"
        return 0
    fi
    # A value from a fieldRef is the operator's own namespace, which is kns.
    env="$(printf '%s' "$KC_OUT" | jq -r '[.items[]?.spec.template.spec.containers[]?.env[]?
        | select(.name == "KEDA_CLUSTER_OBJECT_NAMESPACE") | .value // empty] | first // ""' 2>/dev/null)" || env=""
    WVA_SCALER_KEDA_OBJ_NS="${env:-$kns}"
}

# wva_scaler_keda_api echoes present when the ClusterTriggerAuthentication kind
# is served, absent when it is not, unknown when discovery failed.
wva_scaler_keda_api() {
    local out rc=0
    out="$(kubectl get --raw /apis/keda.sh/v1alpha1 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        if wva_scaler_not_found "$out"; then echo absent; else echo unknown; fi
        return 0
    fi
    if printf '%s' "$out" | jq -e '[.resources[]?.name] | index("clustertriggerauthentications")' >/dev/null 2>&1; then
        echo present
    else
        echo absent
    fi
}

# wva_scaler_keda_auth_check is everything wva_scaler_keda_auth_apply checks
# before it writes, so the install can run it before it writes anything at all:
# KEDA's API, where KEDA reads its Secrets, the rights to write there, and that
# the copy, the ClusterTriggerAuthentication and the marker are absent or this
# installer's. A foreign or unreadable one is a refusal. Anything else that
# keeps KEDA's side from being written goes through wva_scaler_keda_not_ready
# (a refusal when WVA_SCALER_TLS=true, a warning otherwise) and returns 1.
# Sets WVA_SCALER_KEDA_OBJ_NS.
wva_scaler_keda_auth_check() {
    local name api out rc=0
    name="$(wva_scaler_keda_auth_name)"
    api="$(wva_scaler_keda_api)"
    if [ "$api" != present ]; then
        wva_scaler_keda_not_ready "KEDA's ClusterTriggerAuthentication kind is $api on this cluster (is KEDA installed?)"
        return 1
    fi
    wva_scaler_keda_object_ns
    if [ -z "$WVA_SCALER_KEDA_OBJ_NS" ]; then
        wva_scaler_keda_not_ready "$WVA_SCALER_KEDA_OBJ_WHY"
        return 1
    fi
    if ! kubectl auth can-i create clustertriggerauthentications.keda.sh >/dev/null 2>&1 \
        || ! kubectl auth can-i create secrets -n "$WVA_SCALER_KEDA_OBJ_NS" >/dev/null 2>&1; then
        wva_scaler_keda_not_ready "you may not create ClusterTriggerAuthentications, or Secrets in $WVA_SCALER_KEDA_OBJ_NS"
        return 1
    fi
    # The copy and the ClusterTriggerAuthentication: absent or ours only.
    WVA_NS="$WVA_SCALER_KEDA_OBJ_NS" wva_scaler_object_state secret "$name"
    case "$WVA_OBJ_STATE" in
        absent|ours) ;;
        unreadable) log_error "Cannot read Secret $name in $WVA_SCALER_KEDA_OBJ_NS ($WVA_OBJ_ERR). Grant get on secrets there, or set WVA_SCALER_TLS=false." ;;
        *) log_error "Secret $name in $WVA_SCALER_KEDA_OBJ_NS was not created by this installer (no $WVA_SCALER_TLS_LABEL label); refusing to overwrite it. Remove or rename it, or set WVA_SCALER_TLS=false." ;;
    esac
    out="$(kubectl get clustertriggerauthentications.keda.sh "$name" -o json 2>&1)" || rc=$?
    if [ "$rc" = 0 ]; then
        printf '%s' "$out" | jq -e --arg k "$WVA_SCALER_TLS_LABEL" '.metadata.labels[$k] // empty' >/dev/null 2>&1 \
            || log_error "ClusterTriggerAuthentication $name was not created by this installer (no $WVA_SCALER_TLS_LABEL label); refusing to overwrite it. Remove or rename it, or set WVA_SCALER_TLS=false."
    elif ! wva_scaler_not_found "$out"; then
        log_error "Cannot read ClusterTriggerAuthentication $name ($(printf '%s\n' "$out" | head -1)). Grant get on clustertriggerauthentications.keda.sh, or set WVA_SCALER_TLS=false."
    fi
    # The marker: the controller phase trusts what it says, so a same-named
    # ConfigMap someone else wrote is never taken over.
    wva_scaler_object_state configmap "$WVA_SCALER_KEDA_MARKER"
    case "$WVA_OBJ_STATE" in
        absent|ours) ;;
        unreadable) log_error "Cannot read ConfigMap $WVA_SCALER_KEDA_MARKER in $WVA_NS ($WVA_OBJ_ERR). Grant get on configmaps there, or set WVA_SCALER_TLS=false." ;;
        *) log_error "ConfigMap $WVA_SCALER_KEDA_MARKER in $WVA_NS was not created by this installer (no $WVA_SCALER_TLS_LABEL label); refusing to overwrite it. Remove or rename it, or set WVA_SCALER_TLS=false." ;;
    esac
    return 0
}

# wva_scaler_keda_auth_apply is the admin step: copy the client Secret to KEDA's
# namespace, write the ClusterTriggerAuthentication, and record both in the
# marker ConfigMap. Runs when TLS is wanted (WVA_SCALER_TLS_WANTED), after KEDA
# is installed. A refusal when TLS was asked for explicitly; a warning and no
# change when it is only the default, so the controller stays plaintext.
wva_scaler_keda_auth_apply() {
    [ "${WVA_SCALER_TLS_WANTED:-false}" = true ] || return 0
    local name d obj_state sha not_after ca_src
    name="$(wva_scaler_keda_auth_name)"
    wva_scaler_keda_auth_check || return 0

    wva_scaler_tmp_begin
    d="$WVA_SCALER_TMPD"
    wva_scaler_object_state secret "$WVA_SCALER_CLIENT_SECRET"
    if [ "$WVA_OBJ_STATE" != ours ]; then
        wva_scaler_drop_tmp
        wva_scaler_keda_not_ready "KEDA's client certificate (Secret $WVA_SCALER_CLIENT_SECRET in $WVA_NS) is $WVA_OBJ_STATE${WVA_OBJ_ERR:+: $WVA_OBJ_ERR}"
        return 0
    fi
    wva_scaler_secret_field tls.crt "$d/tls.crt"
    wva_scaler_secret_field tls.key "$d/tls.key"
    wva_scaler_secret_field ca.crt "$d/ca.crt"
    ca_src="the installer's CA"
    if [ "${ENVIRONMENT:-}" = openshift ]; then
        # The serving certificate is the service CA's, so KEDA must trust that.
        kubectl get configmap openshift-service-ca.crt -n "$WVA_NS" -o jsonpath='{.data.service-ca\.crt}' > "$d/ca.crt" 2>/dev/null || : > "$d/ca.crt"
        ca_src="the OpenShift service CA"
    fi
    if [ ! -s "$d/tls.crt" ] || [ ! -s "$d/tls.key" ] || [ ! -s "$d/ca.crt" ]; then
        wva_scaler_drop_tmp
        wva_scaler_keda_not_ready "KEDA's client certificate, key or ${ca_src} could not be read in $WVA_NS"
        return 0
    fi

    # The copy, read again: whether it changed decides the KEDA restart notice.
    # wva_scaler_keda_auth_check settled the ClusterTriggerAuthentication and
    # the marker.
    WVA_NS="$WVA_SCALER_KEDA_OBJ_NS" wva_scaler_object_state secret "$name"
    obj_state="$WVA_OBJ_STATE"
    local changed=false
    if [ "$obj_state" = ours ]; then
        # KEDA builds a scaler connection's TLS once and keeps it until KEDA
        # restarts, so a changed copy does not reach a connection already open.
        wva_scaler_secret_field tls.crt "$d/old.crt"
        wva_scaler_secret_field ca.crt "$d/old.ca"
        cmp -s "$d/old.crt" "$d/tls.crt" && cmp -s "$d/old.ca" "$d/ca.crt" || changed=true
    fi
    case "$obj_state" in
        absent|ours) ;;
        unreadable) wva_scaler_drop_tmp; log_error "Cannot read Secret $name in $WVA_SCALER_KEDA_OBJ_NS ($WVA_OBJ_ERR). Grant get on secrets there, or set WVA_SCALER_TLS=false." ;;
        *) wva_scaler_drop_tmp; log_error "Secret $name in $WVA_SCALER_KEDA_OBJ_NS was not created by this installer (no $WVA_SCALER_TLS_LABEL label); refusing to overwrite it. Remove or rename it, or set WVA_SCALER_TLS=false." ;;
    esac

    jq -n --arg n "$name" --arg ns "$WVA_SCALER_KEDA_OBJ_NS" --arg l "$WVA_SCALER_TLS_LABEL" --arg src "$WVA_NS" \
        --rawfile crt "$d/tls.crt" --rawfile key "$d/tls.key" --rawfile ca "$d/ca.crt" \
        '{apiVersion: "v1", kind: "Secret", type: "Opaque",
          metadata: {name: $n, namespace: $ns,
                     labels: {"app.kubernetes.io/managed-by": "llm-scaling-manager", ($l): "keda-client"},
                     annotations: {"wva.llmd.ai/copied-from": ($src + "/wva-external-scaler-client-tls")}},
          data: {"tls.crt": ($crt | @base64), "tls.key": ($key | @base64), "ca.crt": ($ca | @base64)}}' \
        | kubectl apply --server-side --force-conflicts --field-manager=llm-scaling-manager -f - >/dev/null \
        || { wva_scaler_drop_tmp; log_error "Could not write Secret $name in $WVA_SCALER_KEDA_OBJ_NS."; }
    jq -n --arg n "$name" --arg l "$WVA_SCALER_TLS_LABEL" --arg s "$name" \
        '{apiVersion: "keda.sh/v1alpha1", kind: "ClusterTriggerAuthentication",
          metadata: {name: $n, labels: {"app.kubernetes.io/managed-by": "llm-scaling-manager", ($l): "keda-client"}},
          spec: {secretTargetRef: [
            {parameter: "caCert", name: $s, key: "ca.crt"},
            {parameter: "tlsClientCert", name: $s, key: "tls.crt"},
            {parameter: "tlsClientKey", name: $s, key: "tls.key"}]}}' \
        | kubectl apply --server-side --force-conflicts --field-manager=llm-scaling-manager -f - >/dev/null \
        || { wva_scaler_drop_tmp; log_error "Could not write ClusterTriggerAuthentication $name."; }

    sha="$(wva_scaler_sha256 "$d/tls.crt")" || sha=""
    not_after="$(openssl x509 -in "$d/tls.crt" -noout -enddate 2>/dev/null | sed 's/^notAfter=//')" || not_after=""
    # The certificate itself (public), so the controller phase checks its
    # expiry with openssl rather than parsing a date. clientSan is the identity
    # it carries: the controller phase compares it with the one it pins.
    jq -n --arg ns "$WVA_NS" --arg l "$WVA_SCALER_TLS_LABEL" --arg m "$WVA_SCALER_KEDA_MARKER" \
        --arg cta "$name" --arg on "$WVA_SCALER_KEDA_OBJ_NS" --arg sha "$sha" --rawfile crt "$d/tls.crt" \
        --arg cas "$(wva_scaler_sha256 "$d/ca.crt")" --arg san "$(wva_scaler_cert_sans "$d/tls.crt" | sed 's/ *$//')" \
        '{apiVersion: "v1", kind: "ConfigMap",
          metadata: {name: $m, namespace: $ns, labels: {"app.kubernetes.io/managed-by": "llm-scaling-manager", ($l): "keda-client"}},
          data: {clusterTriggerAuthentication: $cta, secretNamespace: $on, clientCertSha256: $sha, clientCert: $crt, caSha256: $cas, clientSan: $san}}' \
        | kubectl apply --server-side --force-conflicts --field-manager=llm-scaling-manager -f - >/dev/null \
        || { wva_scaler_drop_tmp; log_error "Could not write ConfigMap $WVA_SCALER_KEDA_MARKER in $WVA_NS."; }
    wva_scaler_drop_tmp
    WVA_SCALER_KEDA_READY=true
    log_success "KEDA's side of the scaler TLS: ClusterTriggerAuthentication $name, reading Secret $name in $WVA_SCALER_KEDA_OBJ_NS (client certificate expires $not_after; KEDA trusts ${ca_src})"
    if [ "$changed" = true ]; then
        log_warning "KEDA's client certificate or the CA it trusts changed. KEDA keeps the TLS of a connection it already has until it restarts, so restart KEDA's operator now:  $(wva_scaler_keda_restart_cmd)"
    elif [ "$obj_state" = absent ] && wva_scaler_keda_operator_may_run; then
        # A first write while KEDA runs: after an uninstall and reinstall, KEDA
        # still holds the connection it built with the old certificate.
        # A first install and a reinstall look the same here: an uninstall
        # removes everything it wrote, the marker included, and KEDA does not
        # show which connections it holds. So both get the warning, and the
        # controller's log tells them apart after the fact
        # (wva_scaler_keda_conn_state, run by verify-deployment and after a
        # ScaledObject move).
        local tls_rows tls_rc=0
        tls_rows="$(wva_scaler_plaintext_scaledobjects plaintext)" || tls_rc=$?
        if [ "$tls_rc" != 0 ] || [ -n "$tls_rows" ]; then
            log_warning "KEDA's side was written for the first time while KEDA's operator is running, and ScaledObjects already use the scaler's TLS address (or they cannot be listed). If this scaler was installed here before (an uninstall and reinstall), KEDA still holds the old certificate and every handshake fails until it restarts. Then restart KEDA's operator:  $(wva_scaler_keda_restart_cmd)"
        else
            log_warning "KEDA's side was written for the first time while KEDA's operator is running. The installer cannot tell a first install from a reinstall. If this namespace had the scaler before, KEDA keeps the old client certificate until its operator restarts, and without a restart the HPAs stay unwired (on the CPU default). On a first install, nothing to do. After the install,  $(wva_make_cmd verify-deployment)  tells which. To be safe now, restart KEDA's operator:  $(wva_scaler_keda_restart_cmd)"
        fi
    fi
}

# wva_scaler_keda_operator_ns echoes the namespace KEDA's operator runs in,
# for a message: wva_scaler_keda_run_ns, else the namespace KEDA's Secrets are
# in (from this run, or from the marker), else a placeholder.
wva_scaler_keda_operator_ns() {
    [ -n "${WVA_SCALER_KEDA_RUN_NS:-}" ] || wva_scaler_keda_run_ns
    printf '%s\n' "${WVA_SCALER_KEDA_RUN_NS:-${WVA_SCALER_KEDA_OBJ_NS:-${WVA_SCALER_KEDA_MARKER_NS:-<KEDA namespace>}}}"
}

# wva_scaler_keda_restart_cmd echoes the command that restarts KEDA's operator,
# the Deployment KEDA's own manifests and Helm chart name keda-operator.
wva_scaler_keda_restart_cmd() {
    printf 'kubectl rollout restart deployment/keda-operator -n %s\n' "$(wva_scaler_keda_operator_ns)"
}

# wva_scaler_keda_restart_after_undeploy restarts KEDA's operator when the
# uninstall removed KEDA's side of the scaler TLS while ScaledObjects used it
# (WVA_SCALER_KEDA_RESTART_WANTED). Call it after the ScaledObjects are gone.
# KEDA_RESTART=false leaves it to the reader, with the command.
wva_scaler_keda_restart_after_undeploy() {
    [ "${WVA_SCALER_KEDA_RESTART_WANTED:-false}" = true ] || return 0
    local ns rows rc=0
    ns="$(wva_scaler_keda_operator_ns)"
    # Only with no ScaledObject left on the TLS address. One that stays (adopted,
    # or made by someone else) would have KEDA rebuild the pooled address on
    # restart with the ClusterTriggerAuthentication gone: empty auth, plaintext.
    # Then the restart belongs after the reinstall, as it always did.
    rows="$(wva_scaler_plaintext_scaledobjects plaintext)" || rc=$?
    if [ "$rc" != 0 ] || [ -n "$rows" ]; then
        log_warning "ScaledObjects still use the scaler's TLS address$([ "$rc" != 0 ] && printf ' (or they cannot be listed)'), so KEDA's operator is not restarted now. If you install this scaler in $WVA_NS again, restart it after the install:  $(wva_scaler_keda_restart_cmd)"
        return 0
    fi
    if [ "${KEDA_RESTART:-true}" != true ]; then
        log_warning "KEDA_RESTART=false: KEDA's operator still holds a connection with the old client certificate. Before installing this scaler in $WVA_NS again, restart it:  $(wva_scaler_keda_restart_cmd)"
        return 0
    fi
    if kubectl rollout restart deployment/keda-operator -n "$ns" >/dev/null 2>&1; then
        log_success "Restarted KEDA's operator in $ns, so a reinstall here gets a new connection to the scaler."
    else
        log_warning "Could not restart KEDA's operator in $ns. Before installing this scaler in $WVA_NS again, restart it:  $(wva_scaler_keda_restart_cmd)"
    fi
}

# wva_scaler_keda_operator_may_run succeeds unless KEDA's operator Deployment is
# known to be absent: a Deployment that cannot be read may be running.
wva_scaler_keda_operator_may_run() {
    local ns rc=0
    ns="$(wva_scaler_keda_operator_ns)"
    kc_capture get deployment keda-operator -n "$ns" -o name || rc=$?
    [ "$rc" = 0 ] && return 0
    ! wva_scaler_not_found "$KC_ERR"
}

# wva_scaler_keda_not_ready reports that KEDA's side could not be written: a
# refusal when WVA_SCALER_TLS=true was asked for, a warning otherwise.
wva_scaler_keda_not_ready() {
    WVA_SCALER_KEDA_READY=false
    if [ "${WVA_SCALER_TLS:-}" = true ]; then
        log_error "WVA_SCALER_TLS=true, but KEDA's side cannot be written: $1."
    fi
    log_warning "The external scaler stays on plaintext gRPC: KEDA's side of TLS cannot be written ($1)."
}

# wva_scaler_keda_marker_state sets WVA_SCALER_KEDA_READY from the marker the
# admin phase leaves in WVA_NS, for the controller phase: true when it names
# this install's ClusterTriggerAuthentication and its client certificate has not
# expired. WVA_SCALER_KEDA_WHY says why not, WVA_SCALER_KEDA_STALE that KEDA
# holds an older (still valid) client certificate than the one in WVA_NS, and
# WVA_SCALER_KEDA_NOT_AFTER when the one KEDA holds expires, and
# WVA_SCALER_KEDA_EXPIRING whether that is within WVA_SCALER_RENEW_DAYS.
# WVA_SCALER_KEDA_CA_STALE is true when KEDA trusts another CA than the one
# that signs the serving certificate now, and unknown when the marker does not
# record it (an older installer wrote it), unread when the current CA cannot be
# read, and false when the two match. WVA_SCALER_KEDA_MARKER_NS is where
# the marker says KEDA's copy went.
#
# WVA_SCALER_KEDA_SAN_STALE is true when the identity KEDA's certificate carries
# is not $1, the one the controller pins (default wva_scaler_client_san): the
# controller then rejects every handshake, so KEDA's side is not ready.
wva_scaler_keda_marker_state() {
    local out rc=0 cta sha cur d pinned="${1:-}" held
    [ -n "$pinned" ] || pinned="$(wva_scaler_client_san)"
    WVA_SCALER_KEDA_READY=false
    WVA_SCALER_KEDA_WHY=""
    WVA_SCALER_KEDA_SAN_STALE=false
    WVA_SCALER_KEDA_STALE=false
    WVA_SCALER_KEDA_NOT_AFTER=""
    WVA_SCALER_KEDA_EXPIRING=false
    WVA_SCALER_KEDA_CA_STALE=false
    WVA_SCALER_KEDA_MARKER_NS=""
    out="$(kubectl get configmap "$WVA_SCALER_KEDA_MARKER" -n "$WVA_NS" -o json 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        if wva_scaler_not_found "$out"; then
            WVA_SCALER_KEDA_WHY="the cluster admin has not written KEDA's side yet"
        else
            WVA_SCALER_KEDA_WHY="ConfigMap $WVA_SCALER_KEDA_MARKER in $WVA_NS could not be read"
        fi
        return 0
    fi
    cta="$(printf '%s' "$out" | jq -r '.data.clusterTriggerAuthentication // ""')"
    sha="$(printf '%s' "$out" | jq -r '.data.clientCertSha256 // ""')"
    WVA_SCALER_KEDA_MARKER_NS="$(printf '%s' "$out" | jq -r '.data.secretNamespace // ""')"
    if [ "$cta" != "$(wva_scaler_keda_auth_name)" ]; then
        WVA_SCALER_KEDA_WHY="ConfigMap $WVA_SCALER_KEDA_MARKER names ClusterTriggerAuthentication '${cta}', not $(wva_scaler_keda_auth_name)"
        return 0
    fi
    d="$(mktemp -d)"
    printf '%s' "$out" | jq -r '.data.clientCert // ""' > "$d/keda.crt"
    WVA_SCALER_KEDA_NOT_AFTER="$(openssl x509 -in "$d/keda.crt" -noout -enddate 2>/dev/null | sed 's/^notAfter=//')"
    if ! openssl x509 -in "$d/keda.crt" -noout -checkend 0 >/dev/null 2>&1; then
        rm -rf "$d"
        WVA_SCALER_KEDA_WHY="the client certificate KEDA holds has expired or cannot be read (${WVA_SCALER_KEDA_NOT_AFTER:-no date})"
        return 0
    fi
    # The identity KEDA presents: recorded by the admin phase, or read off the
    # certificate when an older installer wrote the marker.
    held="$(printf '%s' "$out" | jq -r '.data.clientSan // ""')"
    [ -n "$held" ] || held="$(wva_scaler_cert_sans "$d/keda.crt" | sed 's/ *$//')"
    if [ "$held" != "$pinned" ]; then
        rm -rf "$d"
        WVA_SCALER_KEDA_SAN_STALE=true
        WVA_SCALER_KEDA_WHY="KEDA's client certificate carries the identity '${held}', but the controller accepts only '${pinned}', so it rejects every handshake. A cluster admin runs:  $(wva_make_cmd setup-prereqs "WVA_SCALER_CLIENT_SAN=${pinned}")  and then restarts KEDA's operator:  $(wva_scaler_keda_restart_cmd)"
        return 0
    fi
    WVA_SCALER_KEDA_READY=true
    openssl x509 -in "$d/keda.crt" -noout -checkend $((WVA_SCALER_RENEW_DAYS * 86400)) >/dev/null 2>&1 \
        || WVA_SCALER_KEDA_EXPIRING=true
    wva_scaler_object_state secret "$WVA_SCALER_CLIENT_SECRET"
    wva_scaler_secret_field tls.crt "$d/tls.crt"
    cur=""
    [ -s "$d/tls.crt" ] && cur="$(wva_scaler_sha256 "$d/tls.crt")"
    rm -rf "$d"
    [ -z "$cur" ] || [ "$cur" = "$sha" ] || WVA_SCALER_KEDA_STALE=true
    # The CA KEDA trusts against the one that signs the serving certificate
    # now. The OpenShift service CA rotates on its own.
    local cas want
    cas="$(printf '%s' "$out" | jq -r '.data.caSha256 // ""')"
    d="$(mktemp -d)"
    if [ "${ENVIRONMENT:-}" = openshift ]; then
        kubectl get configmap openshift-service-ca.crt -n "$WVA_NS" -o jsonpath='{.data.service-ca\.crt}' > "$d/ca" 2>/dev/null || : > "$d/ca"
    else
        wva_scaler_secret_field ca.crt "$d/ca"
    fi
    want=""
    [ -s "$d/ca" ] && want="$(wva_scaler_sha256 "$d/ca")"
    rm -rf "$d"
    # A marker from an installer that did not record the CA: cannot tell.
    # The current CA unreadable: not "the same", which verify would print.
    if [ -z "$cas" ]; then
        WVA_SCALER_KEDA_CA_STALE=unknown
    elif [ -z "$want" ]; then
        WVA_SCALER_KEDA_CA_STALE=unread
    elif [ "$cas" != "$want" ]; then
        WVA_SCALER_KEDA_CA_STALE=true
    fi
}

# wva_scaler_plaintext_scaledobjects echoes <namespace>/<name> of every
# ScaledObject, in the namespaces this install watches, with a trigger naming
# this install's scaler that would NOT reach it over this installer's TLS: the
# plaintext address, or no authenticationRef to the ClusterTriggerAuthentication.
# With $1 = plaintext, the reverse: triggers still on the TLS address or
# reference. With $1 = any, every one naming this scaler. Exits 2 when the
# ScaledObjects cannot be read: unknown, not none.
wva_scaler_plaintext_scaledobjects() {
    local want="${1:-tls}" out rc=0 err
    err="$(mktemp)"
    if [ "$(wva_install_scope)" = "namespace" ]; then
        out="$(kubectl get scaledobjects.keda.sh -n "${WVA_WATCH_NS:-$WVA_NS}" -o json 2>"$err")" || rc=$?
    else
        out="$(kubectl get scaledobjects.keda.sh -A -o json 2>"$err")" || rc=$?
    fi
    if [ "$rc" != 0 ]; then
        # KEDA not installed yet: there can be no ScaledObjects.
        if grep -qi "doesn.t have a resource type" "$err"; then rm -f "$err"; return 0; fi
        rm -f "$err"
        return 2
    fi
    rm -f "$err"
    printf '%s' "$out" | jq -r --arg p "wva-external-scaler.${WVA_NS}." \
        --arg a "$(wva_scaler_address "$WVA_NS" tls)" --arg n "$(wva_scaler_keda_auth_name)" --arg w "$want" '
        [.items[]? | . as $so | (.spec.triggers // [])[]
         | select((.metadata.scalerAddress // "") | startswith($p))
         | ((.metadata.scalerAddress == $a)
            and ((.authenticationRef.name // "") == $n)
            and ((.authenticationRef.kind // "") == "ClusterTriggerAuthentication")) as $tls
         | select(if $w == "any" then true elif $w == "tls" then ($tls | not) else ((.metadata.scalerAddress == $a) or (.authenticationRef != null)) end)
         | ($so.metadata.namespace + "/" + $so.metadata.name)] | unique | .[]' 2>/dev/null || return 2
}

# wva_scaler_migrate_scaledobjects rewrites the triggers naming this scaler on
# every ScaledObject in $1 (<namespace>/<name> lines) to mode $2: tls (the TLS
# address and the authenticationRef) or plaintext (neither). Only those
# triggers change; the list is rewritten in place. WVA_SCALER_MIGRATE_FAILED
# lists the ones that could not be patched.
wva_scaler_migrate_scaledobjects() {
    local rows="$1" mode="$2" ns name triggers ok=0 failed=""
    local p="wva-external-scaler.${WVA_NS}." a ref
    a="$(wva_scaler_address "$WVA_NS" "$mode")"
    ref="$(wva_scaler_trigger_auth_json "$WVA_NS")"
    while IFS=/ read -r ns name; do
        [ -n "$name" ] || continue
        triggers="$(kubectl get scaledobject "$name" -n "$ns" -o json 2>/dev/null \
            | jq -c --arg p "$p" --arg a "$a" --argjson r "$ref" --arg m "$mode" '[ .spec.triggers[]?
                | if ((.metadata.scalerAddress // "") | startswith($p))
                  then (.metadata.scalerAddress = $a)
                       | (if $m == "tls" then .authenticationRef = $r else del(.authenticationRef) end)
                  else . end ]' 2>/dev/null)" || triggers=""
        if [ -n "$triggers" ] && [ "$triggers" != null ] \
            && kubectl patch scaledobject "$name" -n "$ns" --type merge \
                -p "{\"spec\":{\"triggers\":$triggers}}" >/dev/null 2>&1; then
            ok=$((ok + 1))
        else
            failed="${failed}${ns}/${name}"$'\n'
        fi
    done <<< "$rows"
    WVA_SCALER_MIGRATE_FAILED="$failed"
    log_info "Moved $ok ScaledObject(s) to the $mode scaler address$([ "$mode" = tls ] && printf ' with authenticationRef %s' "$(wva_scaler_keda_auth_name)")."
}

# wva_resolve_scaler_tls decides whether this install WANTS the scaler on TLS
# (WVA_SCALER_TLS_WANTED) and who issues the certificates
# (WVA_SCALER_TLS_PROVIDER_RESOLVED: cert-manager or openssl). Whether TLS is
# actually turned on is wva_scaler_tls_prepare's call: KEDA must be ready for it.
#
# WVA_SCALER_TLS unset means the default: TLS, on a new install and on an
# upgrade from plaintext, once KEDA is ready for it (WVA_SCALER_TLS_DEFAULTED).
# An install whose controller already has TLS keeps it: an upgrade must never
# quietly go back to plaintext. A controller whose TLS flags this installer did
# not set is a refusal rather than a guess, since the apply would drop them.
# Sets variables rather than echoing, because log_error is an exit and an exit
# inside $( ) ends only the subshell.
wva_resolve_scaler_tls() {
    WVA_SCALER_TLS_ON=false
    WVA_SCALER_TLS_KEPT=false
    WVA_SCALER_TLS_WANTED=false
    WVA_SCALER_TLS_DEFAULTED=false
    WVA_SCALER_TLS_PROVIDER_RESOLVED=""
    wva_scaler_check_client_san
    wva_scaler_tls_read_live
    case "${WVA_SCALER_TLS:-}" in
        true)
            WVA_SCALER_TLS_WANTED=true
            if [ "$WVA_SCALER_TLS_LIVE" = manual ]; then
                log_warning "WVA_SCALER_TLS=true takes over the external scaler TLS flags set by hand on the running controller in $WVA_NS. These are replaced by the installer's certificates:"
                printf '%s\n' "$WVA_SCALER_TLS_LIVE_FLAGS" | sed 's/^/    /' >&2
            fi
            ;;
        false) ;;
        "")
            case "$WVA_SCALER_TLS_LIVE" in
                absent|plaintext)
                    WVA_SCALER_TLS_WANTED=true
                    WVA_SCALER_TLS_DEFAULTED=true ;;
                unreadable)
                    log_error "Could not read Deployment wva-controller-manager in $WVA_NS ($WVA_SCALER_TLS_LIVE_ERR), so whether its external scaler serves TLS is unknown and cannot be kept. Re-run with WVA_SCALER_TLS=true or WVA_SCALER_TLS=false." ;;
                manual)
                    log_error "The running controller in $WVA_NS serves the external scaler over TLS with flags this installer did not set, and this apply would drop them. Re-run with WVA_SCALER_TLS=true to have the installer provision and keep TLS, or WVA_SCALER_TLS=false to accept plaintext." ;;
                *)
                    WVA_SCALER_TLS_WANTED=true
                    WVA_SCALER_TLS_KEPT=true ;;
            esac
            ;;
        *) log_error "WVA_SCALER_TLS must be true or false (got '${WVA_SCALER_TLS}')" ;;
    esac
    [ "$WVA_SCALER_TLS_WANTED" = true ] || return 0

    local want="${WVA_SCALER_TLS_PROVIDER:-}"
    if [ -z "$want" ]; then
        case "$WVA_SCALER_TLS_LIVE" in
            cert-manager|openssl) want="$WVA_SCALER_TLS_LIVE" ;;
            *) want=auto ;;
        esac
    fi
    case "$want" in
        cert-manager|openssl) ;;
        auto)
            wva_cert_manager_api
            case "$WVA_CERT_MANAGER_API" in
                present) want=cert-manager ;;
                absent) want=openssl ;;
                *)
                    wva_scaler_tls_skip "could not tell whether cert-manager is installed (${WVA_CERT_MANAGER_API_ERR:-discovery failed}); set WVA_SCALER_TLS_PROVIDER=cert-manager or WVA_SCALER_TLS_PROVIDER=openssl"
                    return 0 ;;
            esac
            ;;
        *) log_error "WVA_SCALER_TLS_PROVIDER must be auto, cert-manager or openssl (got '$want')" ;;
    esac
    WVA_SCALER_TLS_PROVIDER_RESOLVED="$want"
}

# wva_scaler_tls_skip gives up on TLS for this run: a refusal when
# WVA_SCALER_TLS=true asked for it, a warning and plaintext when it was only the
# default. A controller that already has TLS is never sent back by a default.
wva_scaler_tls_skip() {
    if [ "${WVA_SCALER_TLS:-}" = true ] || [ "${WVA_SCALER_TLS_KEPT:-false}" = true ]; then
        log_error "External scaler TLS cannot be set up: $1."
    fi
    log_warning "The external scaler stays on plaintext gRPC (TLS is the default, but $1)."
    WVA_SCALER_TLS_WANTED=false
}

# wva_scaler_tls_give_up is wva_scaler_tls_skip with its own refusal text: $1
# when WVA_SCALER_TLS=true asked for TLS or the controller already has it, else
# a warning giving reason $2, and plaintext (WVA_SCALER_TLS_WANTED=false). The
# default never aborts an install the docs say stays on plaintext.
wva_scaler_tls_give_up() {
    if [ "${WVA_SCALER_TLS:-}" = true ] || [ "${WVA_SCALER_TLS_KEPT:-false}" = true ]; then
        log_error "$1"
    fi
    wva_scaler_tls_skip "$2"
}

# wva_scaler_tls_migrate_cmd echoes the command that moves the ScaledObjects
# with the install, for a message. It names INSTALL_PHASE only when the caller
# set it on this run, and then in this run's phase: all (admin and controller
# in one pass) or wva (the controller alone, after setup-prereqs). Otherwise
# the installer works the phase out again, as it did this time, and the
# command matches the one the docs give.
wva_scaler_tls_migrate_cmd() {
    if [ -z "${INSTALL_PHASE_EXPLICIT:-}" ]; then
        wva_make_cmd deploy-wva "WVA_SCALER_TLS=$1" WVA_SCALER_TLS_MIGRATE=true
        return 0
    fi
    local phase=all
    [ "${WVA_APPLY_SCOPE:-all}" = all ] || phase=wva
    wva_make_cmd deploy-wva "WVA_SCALER_TLS=$1" WVA_SCALER_TLS_MIGRATE=true "INSTALL_PHASE=$phase"
}

# wva_scaler_tls_check_scaledobjects decides what happens to the ScaledObjects
# that name this scaler when its mode changes to $1 (tls or plaintext). KEDA
# keeps one connection per scalerAddress, so ScaledObjects on the old address
# cannot share the new mode: each must move with it. With
# WVA_SCALER_TLS_MIGRATE=true the install moves them after the controller rolls
# (WVA_SCALER_TLS_MIGRATE_ROWS). Without it, a change asked for explicitly is a
# refusal that lists them; a change that is only the default is skipped. Returns
# 1 when the change must not happen.
wva_scaler_tls_check_scaledobjects() {
    local mode="$1" rows rc=0 asked=false
    WVA_SCALER_TLS_MIGRATE_ROWS=""
    [ -n "${WVA_SCALER_TLS:-}" ] && asked=true
    rows="$(wva_scaler_plaintext_scaledobjects "$mode")" || rc=$?
    if [ "$rc" != 0 ]; then
        [ "$asked" = true ] && log_error "Cannot list the ScaledObjects that name this scaler, so whether they can follow it to $mode is unknown. Grant list on scaledobjects.keda.sh, or leave WVA_SCALER_TLS unset."
        log_warning "The external scaler stays on plaintext gRPC: the ScaledObjects that name it cannot be listed, so whether they would follow it to TLS is unknown."
        return 1
    fi
    [ -n "$rows" ] || return 0
    if [ "${WVA_SCALER_TLS_MIGRATE:-false}" = true ]; then
        WVA_SCALER_TLS_MIGRATE_ROWS="$rows"
        WVA_SCALER_TLS_MIGRATE_MODE="$mode"
        log_info "These ScaledObjects move to the $mode scaler address once the controller is applied (WVA_SCALER_TLS_MIGRATE=true):"
        printf '%s\n' "$rows" | head -20 | sed 's/^/    /' >&2
        return 0
    fi
    local what="TLS" verb="carry no authenticationRef to ClusterTriggerAuthentication $(wva_scaler_keda_auth_name) on the TLS address"
    [ "$mode" = plaintext ] && { what="plaintext"; verb="still use the TLS address or an authenticationRef"; }
    if [ "$asked" = true ]; then
        log_error "These ScaledObjects name this scaler and $verb, so moving it to $what would leave them on the other mode:
$(printf '%s\n' "$rows" | head -20 | sed 's/^/    /')
$(wva_scaler_mixed_risk)
Move them with the install:  $(wva_scaler_tls_migrate_cmd "$( [ "$mode" = tls ] && echo true || echo false)")"
    fi
    log_warning "The external scaler stays on plaintext gRPC. TLS is the default, but these ScaledObjects name it and would need to move with it:"
    printf '%s\n' "$rows" | head -20 | sed 's/^/    /' >&2
    log_warning "  Move them with the install:  $(wva_scaler_tls_migrate_cmd true)"
    return 1
}

# wva_scaler_tls_prepare is the install step, run by every phase.
#   admin phase (prereqs, all)  provision the certificates and KEDA's side
#   controller phase (controller, all)  turn TLS on (or off) on the Deployment:
#       patches the throw-away overlay $1/kustomization.yaml
# The controller phase turns TLS on only when KEDA is ready for it and every
# ScaledObject naming the scaler can follow (wva_scaler_tls_check_scaledobjects).
wva_scaler_tls_prepare() {
    local kustomization="$1" scope="${WVA_APPLY_SCOPE:-all}"
    WVA_SCALER_KEDA_DEFERRED=false
    WVA_SCALER_KEDA_READY=false
    WVA_SCALER_TLS_MIGRATE_ROWS=""
    wva_resolve_scaler_tls

    if [ "$WVA_SCALER_TLS_WANTED" != true ]; then
        case "$WVA_SCALER_TLS_LIVE" in
            cert-manager|openssl)
                [ "$scope" = prereqs ] && return 0
                wva_scaler_tls_check_scaledobjects plaintext || return 0
                log_warning "WVA_SCALER_TLS=false: the external scaler goes back to PLAINTEXT gRPC. Its certificates and KEDA's ClusterTriggerAuthentication stay; undeploy removes them." ;;
            *)
                # Plaintext stays plaintext (WVA_SCALER_TLS=false, or the
                # default gave up): ScaledObjects on the TLS address are stuck.
                wva_scaler_tls_stay_plaintext ;;
        esac
        return 0
    fi
    # openssl mints the certificates without cert-manager, and digests the
    # client CA with it: needed on both paths.
    if ! command -v openssl >/dev/null 2>&1; then
        wva_scaler_tls_skip "openssl is not on PATH (it mints the scaler certificates when there is no cert-manager, and digests the client CA either way)"
        wva_scaler_tls_stay_plaintext
        return 0
    fi
    # The default asks for no rights the permission check did not: without them
    # it stays on plaintext, rather than failing halfway through.
    if [ "$WVA_SCALER_TLS_DEFAULTED" = true ]; then
        local verb res denied=""
        if ! kubectl auth can-i create secrets -n "$WVA_NS" >/dev/null 2>&1; then
            denied="you may not create Secrets in $WVA_NS"
        else
            for res in secrets $([ "$WVA_SCALER_TLS_PROVIDER_RESOLVED" = cert-manager ] && echo issuers.cert-manager.io certificates.cert-manager.io); do
                for verb in get create; do
                    [ -z "$denied" ] || break 2
                    kubectl auth can-i "$verb" "$res" -n "$WVA_NS" >/dev/null 2>&1 || denied="you may not $verb $res in $WVA_NS"
                done
            done
        fi
        if [ -n "$denied" ]; then
            wva_scaler_tls_skip "$denied"
            wva_scaler_tls_stay_plaintext
            return 0
        fi
    fi

    # KEDA's side, checked before anything is written: a refusal there leaves
    # nothing behind. The admin phase writes it after the certificates. KEDA is
    # normally there by now: this install puts it in before the controller. If
    # its API is not served yet, it is written after the controller
    # (wva_scaler_tls_after_keda). The controller phase reads what the admin left.
    local keda_side=none
    case "$scope" in
        prereqs|all)
            if [ "$(wva_scaler_keda_api)" = present ]; then
                if wva_scaler_keda_auth_check; then keda_side=write; fi
            elif wva_scaler_install_puts_keda; then
                keda_side=deferred
            else
                wva_scaler_keda_not_ready "KEDA is not installed (its ClusterTriggerAuthentication kind is not served)"
            fi ;;
    esac

    if [ "$WVA_SCALER_TLS_PROVIDER_RESOLVED" = openssl ]; then
        wva_scaler_tls_provision_openssl
    else
        wva_scaler_tls_provision_cert_manager
    fi
    # The default gives up when cert-manager does not issue (wva_scaler_tls_give_up).
    if [ "$WVA_SCALER_TLS_WANTED" != true ]; then
        wva_scaler_tls_stay_plaintext
        return 0
    fi

    case "$scope" in
        prereqs|all)
            case "$keda_side" in
                write) wva_scaler_keda_auth_apply ;;
                deferred)
                    WVA_SCALER_KEDA_DEFERRED=true
                    WVA_SCALER_KEDA_READY=true ;;
                *) WVA_SCALER_KEDA_READY=false ;;
            esac ;;
        *)
            wva_scaler_keda_marker_state
            # KEDA trusting an old CA fails every handshake. A controller that
            # already has TLS keeps it and is warned (below); one that does
            # not yet is not turned on into that.
            if [ "$WVA_SCALER_KEDA_READY" = true ] && [ "$WVA_SCALER_KEDA_CA_STALE" = true ] \
                && [ "$WVA_SCALER_TLS_KEPT" != true ]; then
                WVA_SCALER_KEDA_READY=false
                WVA_SCALER_KEDA_WHY="KEDA trusts a different CA than the one that signs the scaler's certificate now, so every handshake would fail. A cluster admin runs:  $(wva_make_cmd setup-prereqs)  and then restarts KEDA's operator:  $(wva_scaler_keda_restart_cmd)"
                wva_scaler_keda_not_ready "$WVA_SCALER_KEDA_WHY"
            elif [ "$WVA_SCALER_KEDA_READY" != true ]; then
                # The identity mismatch carries its own fix, with the KEDA restart.
                local fix
                fix="$(wva_make_cmd setup-prereqs)"
                [ "$WVA_SCALER_KEDA_SAN_STALE" != true ] || fix=""
                # WVA_SCALER_TLS=true refuses whether TLS is on already or not.
                if [ "$WVA_SCALER_TLS_KEPT" = true ] && [ "${WVA_SCALER_TLS:-}" != true ]; then
                    log_warning "External scaler TLS stays on, but KEDA's side is not usable: $WVA_SCALER_KEDA_WHY.${fix:+ A cluster admin runs:  $fix}"
                else
                    wva_scaler_keda_not_ready "$WVA_SCALER_KEDA_WHY${fix:+; a cluster admin runs:  $fix}"
                fi
            else
                if [ "$WVA_SCALER_KEDA_STALE" = true ]; then
                    log_warning "KEDA holds an older client certificate than the one in $WVA_NS (it expires ${WVA_SCALER_KEDA_NOT_AFTER}). It still works until then; a cluster admin refreshes it with:  $(wva_make_cmd setup-prereqs)"
                fi
                wva_scaler_keda_ca_report log_warning
            fi ;;
    esac
    [ "$scope" != prereqs ] || return 0
    if [ "$WVA_SCALER_KEDA_READY" != true ] && [ "$WVA_SCALER_TLS_KEPT" != true ]; then
        wva_scaler_tls_warn_stranded
        return 0
    fi

    if [ "$WVA_SCALER_TLS_KEPT" = true ]; then
        # Already on, so nothing here can refuse: ScaledObjects still on the
        # plaintext address are reported, and moved when asked.
        local rows rc=0
        rows="$(wva_scaler_plaintext_scaledobjects tls)" || rc=$?
        if [ "$rc" = 0 ] && [ -n "$rows" ]; then
            if [ "${WVA_SCALER_TLS_MIGRATE:-false}" = true ]; then
                WVA_SCALER_TLS_MIGRATE_ROWS="$rows"
                WVA_SCALER_TLS_MIGRATE_MODE=tls
            else
                log_warning "These ScaledObjects name this scaler on the plaintext address or without the authenticationRef $(wva_scaler_keda_auth_name):"
                printf '%s\n' "$rows" | head -20 | sed 's/^/    /' >&2
                log_warning "  $(wva_scaler_mixed_risk)"
                log_warning "  Move them:  $(wva_scaler_tls_migrate_cmd true)"
            fi
        fi
    else
        wva_scaler_tls_check_scaledobjects tls || { wva_scaler_tls_warn_stranded; return 0; }
    fi

    WVA_SCALER_TLS_ON=true
    if [ "$WVA_SCALER_TLS_KEPT" = true ]; then
        log_info "External scaler TLS stays on (the running controller has it; WVA_SCALER_TLS=false turns it off), certificates from $WVA_SCALER_TLS_PROVIDER_RESOLVED"
    else
        log_info "External scaler TLS: mutual TLS, certificates from $WVA_SCALER_TLS_PROVIDER_RESOLVED; ScaledObjects reach it at $(wva_scaler_address "$WVA_NS" tls) with authenticationRef $(wva_scaler_keda_auth_name)"
    fi
    wva_append_scaler_tls_patch "$kustomization"
}

# wva_scaler_keda_ca_report reports, through log function $1, what
# wva_scaler_keda_marker_state found about the CA KEDA trusts: nothing when it
# is current, the fix when it moved on, and "cannot tell" when the marker does
# not record it.
wva_scaler_keda_ca_report() {
    local say="$1"
    case "${WVA_SCALER_KEDA_CA_STALE:-false}" in
        true)
            "$say" "KEDA trusts a different CA than the one that signs the scaler's certificate now (on OpenShift the service CA rotates by itself), so every handshake fails and nothing scales. A cluster admin runs:  $(wva_make_cmd setup-prereqs)  and then restarts KEDA's operator:  $(wva_scaler_keda_restart_cmd)" ;;
        unknown)
            "$say" "Cannot tell which CA KEDA trusts: ConfigMap $WVA_SCALER_KEDA_MARKER in $WVA_NS was written by an older installer and does not record it. A cluster admin re-runs:  $(wva_make_cmd setup-prereqs)" ;;
    esac
}

# wva_scaler_keda_conn_state sets WVA_SCALER_KEDA_CONN from the controller's
# log of the last WVA_SCALER_REFUSAL_SINCE (default 5m):
#   stale    it refused handshakes from a Pod in KEDA's namespace because their
#            client certificate is signed by an unknown authority, while the
#            certificate KEDA was given (the marker's copy) is signed by the
#            current client CA. KEDA presents an older certificate than the one
#            it was given: it keeps one connection per scaler address, built
#            once, and never closes it. Seen after an uninstall and reinstall in
#            the same namespace, with KEDA's operator running throughout.
#   copy     the same refusals, but the certificate KEDA was given is not signed
#            by the current client CA either
#   none     no such refusal from a Pod in KEDA's namespace
#   unknown  the log, KEDA's Pods, the marker or the client CA cannot be read
#            (WVA_SCALER_KEDA_CONN_WHY says which)
# WVA_SCALER_KEDA_CONN_PODS names the refused KEDA Pods. The controller logs at
# most one refusal per remote host per minute, so a KEDA that keeps retrying
# shows up within the window.
wva_scaler_keda_conn_state() {
    local kns logs pods out rc=0 hosts d
    WVA_SCALER_KEDA_CONN=unknown
    WVA_SCALER_KEDA_CONN_WHY=""
    WVA_SCALER_KEDA_CONN_PODS=""
    [ -n "${WVA_SCALER_KEDA_RUN_NS:-}" ] || wva_scaler_keda_run_ns
    kns="$WVA_SCALER_KEDA_RUN_NS"
    if [ -z "$kns" ]; then
        WVA_SCALER_KEDA_CONN_WHY="where KEDA runs is not known (set KEDA_NAMESPACE)"
        return 0
    fi
    logs="$(kubectl logs -n "$WVA_NS" -l "$WVA_CONTROLLER_LABEL_SELECTOR" -c manager \
        --since="${WVA_SCALER_REFUSAL_SINCE:-5m}" --tail=-1 --max-log-requests=10 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        WVA_SCALER_KEDA_CONN_WHY="the controller's log in $WVA_NS cannot be read ($(printf '%s\n' "$logs" | head -1))"
        return 0
    fi
    # The remote host of each refusal for an unknown authority, port and IPv6
    # brackets dropped. Console and JSON log lines both quote the field.
    # Both directions of the same stale connection: the controller refusing
    # KEDA's old client certificate ("unknown authority"), and KEDA refusing the
    # controller's new serving certificate with the CA it cached, which the
    # controller logs as "remote error: tls: bad certificate". Seen on kind
    # after an uninstall and a reinstall.
    hosts="$(printf '%s\n' "$logs" | grep -F 'refused a TLS handshake' | grep -E 'unknown authority|bad certificate' \
        | sed -n 's/.*"remote": *"\([^"]*\)".*/\1/p' | sed 's/:[0-9]*$//; s/^\[//; s/\]$//' | sort -u)" || hosts=""
    if [ -z "$hosts" ]; then
        WVA_SCALER_KEDA_CONN=none
        return 0
    fi
    rc=0
    pods="$(kubectl get pods -n "$kns" -o json 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        WVA_SCALER_KEDA_CONN_WHY="the controller refused handshakes for an unknown authority, but the Pods in $kns, where KEDA runs, cannot be read ($(printf '%s\n' "$pods" | head -1)), so whether KEDA sent them is not known"
        return 0
    fi
    WVA_SCALER_KEDA_CONN_PODS="$(printf '%s' "$pods" | jq -r --arg h "$hosts" '
        ($h | split("\n")) as $hs
        | [.items[]? | select([(.status.podIPs // [])[]?.ip, (.status.podIP // empty)] | any(. as $ip | $hs | any(. == $ip)))
           | .metadata.name] | unique | join(" ")' 2>/dev/null)" || WVA_SCALER_KEDA_CONN_PODS=""
    if [ -z "$WVA_SCALER_KEDA_CONN_PODS" ]; then
        WVA_SCALER_KEDA_CONN=none
        return 0
    fi
    # The certificate KEDA was given, against the client CA the controller
    # trusts now.
    rc=0
    out="$(kubectl get configmap "$WVA_SCALER_KEDA_MARKER" -n "$WVA_NS" -o json 2>&1)" || rc=$?
    d="$(mktemp -d)"
    [ "$rc" = 0 ] && printf '%s' "$out" | jq -r '.data.clientCert // ""' > "$d/keda.crt" 2>/dev/null || : > "$d/keda.crt"
    wva_scaler_object_state secret "$WVA_SCALER_CLIENT_SECRET"
    wva_scaler_secret_field ca.crt "$d/ca.crt"
    if [ ! -s "$d/keda.crt" ] || [ ! -s "$d/ca.crt" ]; then
        rm -rf "$d"
        WVA_SCALER_KEDA_CONN_WHY="the controller refused KEDA's Pod(s) $WVA_SCALER_KEDA_CONN_PODS for a certificate signed by an unknown authority, but the certificate KEDA was given (ConfigMap $WVA_SCALER_KEDA_MARKER) or the client CA (Secret $WVA_SCALER_CLIENT_SECRET) in $WVA_NS cannot be read"
        return 0
    fi
    if openssl verify -CAfile "$d/ca.crt" "$d/keda.crt" >/dev/null 2>&1; then
        WVA_SCALER_KEDA_CONN=stale
    else
        WVA_SCALER_KEDA_CONN=copy
    fi
    rm -rf "$d"
}

# wva_scaler_keda_conn_report reports what wva_scaler_keda_conn_state found.
# Succeeds when the cause is on KEDA's side (stale or copy): changing a
# ScaledObject cannot fix that, so the caller must not offer the re-wire. For
# unknown it names the KEDA restart as the other possible fix and fails, so the
# caller still lists the re-wire. Prints nothing for none.
wva_scaler_keda_conn_report() {
    case "${WVA_SCALER_KEDA_CONN:-none}" in
        stale)
            log_warning "  Cause: KEDA's operator still presents an OLD client certificate. The controller refused handshakes from KEDA's Pod(s) $WVA_SCALER_KEDA_CONN_PODS: certificate signed by unknown authority. The certificate KEDA was given (ClusterTriggerAuthentication $(wva_scaler_keda_auth_name)) is signed by the current CA, so KEDA is still using a connection it opened before. KEDA keeps one connection per scaler address and never closes it, so this follows an uninstall and reinstall in the same namespace. Changing the ScaledObjects does not help. Restart KEDA's operator:  $(wva_scaler_keda_restart_cmd)"
            return 0 ;;
        copy)
            log_warning "  Cause: KEDA's client certificate is not signed by the scaler's current client CA. The controller refused handshakes from KEDA's Pod(s) $WVA_SCALER_KEDA_CONN_PODS: certificate signed by unknown authority, and the certificate KEDA was given (ClusterTriggerAuthentication $(wva_scaler_keda_auth_name)) does not chain to the client CA in Secret $WVA_SCALER_CLIENT_SECRET. A cluster admin runs:  $(wva_make_cmd setup-prereqs)  and then restarts KEDA's operator:  $(wva_scaler_keda_restart_cmd)"
            return 0 ;;
        unknown)
            log_warning "  Whether KEDA still presents an old client certificate is not checked: ${WVA_SCALER_KEDA_CONN_WHY}. One of two fixes applies. If this scaler was uninstalled and reinstalled in this namespace while KEDA's operator kept running (the controller log then shows \"refused a TLS handshake\" with \"certificate signed by unknown authority\"), restart KEDA's operator:  $(wva_scaler_keda_restart_cmd)  Otherwise change the ScaledObjects again (below)."
            return 1 ;;
    esac
    return 1
}

# wva_scaler_tls_stay_plaintext names the ScaledObjects a plaintext scaler
# strands (wva_scaler_tls_warn_stranded), from every phase but the admin's.
wva_scaler_tls_stay_plaintext() {
    [ "${WVA_APPLY_SCOPE:-all}" = prereqs ] || wva_scaler_tls_warn_stranded
}

# wva_scaler_tls_warn_stranded warns about ScaledObjects on the TLS address or
# reference while the scaler stays on plaintext (wva_scaler_mixed_risk). With
# WVA_SCALER_TLS_MIGRATE=true it moves them back to the plaintext address
# instead, once the controller is applied (WVA_SCALER_TLS_MIGRATE_ROWS).
wva_scaler_tls_warn_stranded() {
    local rows rc=0
    rows="$(wva_scaler_plaintext_scaledobjects plaintext)" || rc=$?
    [ "$rc" = 0 ] && [ -n "$rows" ] || return 0
    if [ "${WVA_SCALER_TLS_MIGRATE:-false}" = true ]; then
        WVA_SCALER_TLS_MIGRATE_ROWS="$rows"
        WVA_SCALER_TLS_MIGRATE_MODE=plaintext
        log_info "The external scaler stays on plaintext gRPC. These ScaledObjects move back to the plaintext scaler address once the controller is applied (WVA_SCALER_TLS_MIGRATE=true):"
        printf '%s\n' "$rows" | head -20 | sed 's/^/    /' >&2
        return 0
    fi
    log_warning "These ScaledObjects use the scaler's TLS address or authenticationRef, but it stays on plaintext:"
    printf '%s\n' "$rows" | head -20 | sed 's/^/    /' >&2
    log_warning "  $(wva_scaler_mixed_risk)"
    log_warning "  Fix what keeps TLS off (above), or move them back:  $(wva_scaler_tls_migrate_cmd false)"
}

# wva_scaler_tls_after_keda writes KEDA's side when this install put KEDA in
# but its API was not served yet when the certificates were written. A
# fallback: KEDA is installed before the controller, so this is rare. The
# controller was already given TLS on the promise, so this is a refusal
# whatever WVA_SCALER_TLS says.
wva_scaler_tls_after_keda() {
    [ "${WVA_SCALER_KEDA_DEFERRED:-false}" = true ] || return 0
    WVA_SCALER_KEDA_DEFERRED=false
    WVA_SCALER_TLS=true wva_scaler_keda_auth_apply
}

# wva_scaler_sole_server_state sets WVA_SCALER_SOLE_WHY to why the controller's
# current Pods are not yet the only thing serving the scaler, or to empty when
# they are: no controller Pod is terminating, every one left is Ready, the
# Service's EndpointSlices list exactly those Pods (all ready), and, when the
# controller elects a leader, one of them holds the lease.
#
# `kubectl rollout status` does not mean this. It returns once the new Pod is
# Ready and the old ReplicaSet is scaled to zero, while the old Pod is still
# terminating and can still be an endpoint. Measured on an OpenShift cluster:
# the old Pod sent its GOAWAY at 13:40:23, KEDA reconciled the moved
# ScaledObject at 13:40:27 and got a plaintext answer on the TLS address, and
# the new Pod took the lease at 13:40:37.
#
# Waiting is not enough on its own. On kind, with the old Pod already gone,
# KEDA still failed on the TLS address for about two minutes with "first record
# does not look like a TLS handshake": the error its pooled connection last got
# from the plaintext Pod, while it waits to reconnect. wva_scaler_ensure_wired
# covers that.
#
# An EndpointSlice or Lease the caller may not read is not waited on; the names
# of what was skipped are in WVA_SCALER_SOLE_SKIPPED.
wva_scaler_sole_server_state() {
    local out rc=0 live
    WVA_SCALER_SOLE_WHY=""
    WVA_SCALER_SOLE_SKIPPED=""
    out="$(kubectl get pods -n "$WVA_NS" -l "$WVA_CONTROLLER_LABEL_SELECTOR" -o json 2>/dev/null)" || rc=$?
    if [ "$rc" != 0 ] || [ -z "$out" ]; then
        WVA_SCALER_SOLE_WHY="the controller Pods in $WVA_NS cannot be read"
        return 0
    fi
    WVA_SCALER_SOLE_WHY="$(printf '%s' "$out" | jq -r '
        [.items[]? | select(.metadata.deletionTimestamp != null) | .metadata.name] as $old
        | [.items[]? | select(.metadata.deletionTimestamp == null)
           | select(([.status.conditions[]? | select(.type == "Ready" and .status == "True")] | length) == 0)
           | .metadata.name] as $unready
        | [.items[]? | select(.metadata.deletionTimestamp == null) | .metadata.name] as $live
        | if ($old | length) > 0 then "old controller Pod(s) still terminating: " + ($old | join(" "))
          elif ($live | length) == 0 then "no controller Pod is running"
          elif ($unready | length) > 0 then "controller Pod(s) not Ready: " + ($unready | join(" "))
          else "" end' 2>/dev/null)" || WVA_SCALER_SOLE_WHY="the controller Pods in $WVA_NS cannot be parsed"
    [ -z "$WVA_SCALER_SOLE_WHY" ] || return 0
    live="$(printf '%s' "$out" | jq -c '[.items[]? | select(.metadata.deletionTimestamp == null) | .metadata.name] | sort')"

    rc=0
    out="$(kubectl get endpointslices -n "$WVA_NS" -l kubernetes.io/service-name=wva-external-scaler -o json 2>/dev/null)" || rc=$?
    if [ "$rc" != 0 ] || [ -z "$out" ]; then
        WVA_SCALER_SOLE_SKIPPED="EndpointSlices"
    else
        WVA_SCALER_SOLE_WHY="$(printf '%s' "$out" | jq -r --argjson live "$live" '
            [.items[]? | .endpoints[]?] as $eps
            | ([$eps[] | .targetRef.name // ""] | unique) as $names
            | [$eps[] | select((.conditions.ready // true) != true) | .targetRef.name // "?"] as $notready
            | if $names != $live then "the scaler Service routes to " + (if ($names | length) == 0 then "no Pod" else ($names | join(" ")) end) + ", not only to " + ($live | join(" "))
              elif ($notready | length) > 0 then "scaler endpoint(s) not ready: " + ($notready | join(" "))
              else "" end' 2>/dev/null)" || WVA_SCALER_SOLE_WHY="the scaler EndpointSlices cannot be parsed"
        [ -z "$WVA_SCALER_SOLE_WHY" ] || return 0
    fi

    # The lease, when the controller elects one. Until it holds it, the new
    # Pod serves a standby that it rebinds on election.
    rc=0
    out="$(kubectl get deployment wva-controller-manager -n "$WVA_NS" -o json 2>/dev/null)" || rc=$?
    if [ "$rc" = 0 ] && printf '%s' "$out" | jq -e '[.spec.template.spec.containers[]? | select(.name == "manager")
            | (.args // [])[] | select(. == "--leader-elect" or . == "--leader-elect=true")] | length > 0' >/dev/null 2>&1; then
        rc=0
        out="$(kubectl get leases -n "$WVA_NS" -o json 2>/dev/null)" || rc=$?
        if [ "$rc" != 0 ] || [ -z "$out" ]; then
            WVA_SCALER_SOLE_SKIPPED="${WVA_SCALER_SOLE_SKIPPED:+$WVA_SCALER_SOLE_SKIPPED and }the leader Lease"
        elif ! printf '%s' "$out" | jq -e --argjson live "$live" '[.items[]? | .spec.holderIdentity // "" | split("_")[0]
                | select(. as $h | $live | index($h))] | length > 0' >/dev/null 2>&1; then
            WVA_SCALER_SOLE_WHY="no current controller Pod holds the leader lease yet"
        fi
    fi
}

# wva_scaler_wait_sole_server waits, for at most WVA_SCALER_SETTLE_TIMEOUT
# seconds (default 180), until wva_scaler_sole_server_state finds nothing in
# the way. Returns 1 on timeout, with the reason in WVA_SCALER_SOLE_WHY.
wva_scaler_wait_sole_server() {
    local end
    end=$(( $(date +%s) + ${WVA_SCALER_SETTLE_TIMEOUT:-180} ))
    while :; do
        wva_scaler_sole_server_state
        [ -n "$WVA_SCALER_SOLE_WHY" ] || return 0
        [ "$(date +%s)" -lt "$end" ] || return 1
        sleep "${WVA_SCALER_POLL_SECONDS:-3}"
    done
}

# wva_scaler_so_wiring echoes how KEDA wired ScaledObject $2 in namespace $1:
#   wired        the HPA carries the external metric of every trigger naming
#                this scaler, and status.externalMetricNames lists it
#   unwired I..  it does not, for the triggers at these indices
#   paused       KEDA is told not to scale it
#   unknown      the ScaledObject or its HPA could not be read
# KEDA names a trigger's metric s<index>-<name>. It asks the scaler for that
# name only when the ScaledObject's spec changes, and builds the HPA without the
# metric when the call fails: then it stays on the CPU default for good.
wva_scaler_so_wiring() {
    local ns="$1" name="$2" so hpa rc=0 hname
    so="$(kubectl get scaledobject "$name" -n "$ns" -o json 2>/dev/null)" || rc=$?
    if [ "$rc" != 0 ] || [ -z "$so" ]; then echo unknown; return 0; fi
    hname="$(printf '%s' "$so" | jq -r --arg n "$name" '.status.hpaName // ("keda-hpa-" + $n)' 2>/dev/null)" || hname="keda-hpa-$name"
    rc=0
    hpa="$(kubectl get hpa "$hname" -n "$ns" -o json 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        if wva_scaler_not_found "$hpa"; then hpa='{}'; else echo unknown; return 0; fi
    fi
    printf '%s' "$so" | jq -r --arg p "wva-external-scaler.${WVA_NS}." --argjson h "$hpa" '
        if ([.status.conditions[]? | select(.type == "Paused" and .status == "True")] | length) > 0 then "paused"
        else
          ([.spec.triggers // [] | to_entries[] | select((.value.metadata.scalerAddress // "") | startswith($p)) | .key]) as $idx
          | ([$h.spec.metrics[]? | select(.type == "External") | .external.metric.name // ""]) as $hm
          | (.status.externalMetricNames // []) as $em
          | [$idx[] | . as $i | ("s" + ($i | tostring) + "-") as $pre
             | select((([$hm[] | select(startswith($pre))] | length) == 0)
                      or (([$em[] | select(startswith($pre))] | length) == 0))
             | $i]
          | if length == 0 then "wired" else "unwired " + (map(tostring) | join(" ")) end
        end' 2>/dev/null || echo unknown
}

# wva_scaler_unwired_of checks the <namespace>/<name> rows in $1 and sets
# WVA_SCALER_UNWIRED to "<namespace>/<name> <index>..." for each one KEDA has
# not wired, and WVA_SCALER_WIRE_UNKNOWN to the rows it could not read.
wva_scaler_unwired_of() {
    local ns name st
    WVA_SCALER_UNWIRED=""
    WVA_SCALER_WIRE_UNKNOWN=""
    while IFS=/ read -r ns name; do
        [ -n "$name" ] || continue
        st="$(wva_scaler_so_wiring "$ns" "$name")"
        case "$st" in
            wired|paused) ;;
            unwired*) WVA_SCALER_UNWIRED="${WVA_SCALER_UNWIRED}${ns}/${name} ${st#unwired }"$'\n' ;;
            *) WVA_SCALER_WIRE_UNKNOWN="${WVA_SCALER_WIRE_UNKNOWN}${ns}/${name}"$'\n' ;;
        esac
    done <<< "$1"
}

# wva_scaler_wait_wired waits up to $2 seconds for every row in $1 to be wired
# (wva_scaler_unwired_of). Returns 1 while some are not.
wva_scaler_wait_wired() {
    local end
    end=$(( $(date +%s) + $2 ))
    while :; do
        wva_scaler_unwired_of "$1"
        [ -n "$WVA_SCALER_UNWIRED" ] || return 0
        [ "$(date +%s)" -lt "$end" ] || return 1
        sleep "${WVA_SCALER_POLL_SECONDS:-3}"
    done
}

# WVA_SCALER_REWIRE_KEY is the trigger metadata key a re-wire writes. The scaler
# ignores it. Changing it is a spec change, which is what makes KEDA ask for the
# metric spec again; an annotation is not.
WVA_SCALER_REWIRE_KEY=wvaRewire

# wva_scaler_rewire_patch echoes the JSON patch that sets the re-wire key on
# the trigger at index $1 to $2.
wva_scaler_rewire_patch() {
    jq -nc --arg i "$1" --arg k "$WVA_SCALER_REWIRE_KEY" --arg v "$2" \
        '[{op: "add", path: ("/spec/triggers/" + $i + "/metadata/" + $k), value: $v}]'
}

# wva_scaler_rewire_cmd echoes, for a message, the command that re-wires the
# unwired row $1 ("<namespace>/<name> <index>...").
wva_scaler_rewire_cmd() {
    local so idxs ns name i cmd=""
    so="${1%% *}"
    idxs="${1#* }"
    ns="${so%%/*}"
    name="${so#*/}"
    for i in $idxs; do
        cmd="${cmd:+$cmd; }kubectl patch scaledobject $name -n $ns --type json -p '$(wva_scaler_rewire_patch "$i" "$(date +%s)")'"
    done
    printf '%s\n' "$cmd"
}

# wva_scaler_rewire makes KEDA read the metric spec again for the unwired rows
# in $1 ("<namespace>/<name> <index>..."): it patches the re-wire key on each
# trigger that names this scaler. Only those triggers, only those ScaledObjects.
wva_scaler_rewire() {
    local row so idxs ns name i stamp
    stamp="$(date +%s)"
    while IFS= read -r row; do
        [ -n "$row" ] || continue
        so="${row%% *}"
        idxs="${row#* }"
        ns="${so%%/*}"
        name="${so#*/}"
        for i in $idxs; do
            kubectl patch scaledobject "$name" -n "$ns" --type json \
                -p "$(wva_scaler_rewire_patch "$i" "$stamp")" >/dev/null 2>&1 || true
        done
    done <<< "$1"
}

# wva_scaler_ensure_wired checks that KEDA wired the HPA of every ScaledObject
# just moved ($1, <namespace>/<name> rows), and re-wires the ones it did not.
#
# KEDA asks the scaler for the metric spec once per spec change. The move is
# one; a call that fails then (a Pod going away, or KEDA's connection for the
# new address still backing off from the old mode, up to two minutes) leaves
# the HPA on the CPU default for good. So: wait WVA_SCALER_REWIRE_WAIT seconds
# (default 30), then up to WVA_SCALER_REWIRE_TRIES times (default 6) change the
# re-wire key and wait again. Never deletes or recreates a ScaledObject:
# KEDA does the same single call for a new one as for a changed one.
wva_scaler_ensure_wired() {
    local rows="$1" wait="${WVA_SCALER_REWIRE_WAIT:-30}" tries="${WVA_SCALER_REWIRE_TRIES:-6}" t=0
    [ -n "$rows" ] || return 0
    while ! wva_scaler_wait_wired "$rows" "$wait"; do
        # A KEDA that still presents an old client certificate: no re-wire
        # helps, only a restart of its operator.
        wva_scaler_keda_conn_state
        if [ "$WVA_SCALER_KEDA_CONN" = stale ] || [ "$WVA_SCALER_KEDA_CONN" = copy ]; then
            log_warning "=================================================================="
            log_warning "KEDA has not wired the scaler's metric onto the HPA of these ScaledObjects, so they scale on the CPU default and WVA's decisions do not reach them:"
            printf '%s' "$WVA_SCALER_UNWIRED" | sed 's/ .*//; s/^/    /' >&2
            wva_scaler_keda_conn_report || true
            log_warning "=================================================================="
            return 1
        fi
        if [ "$t" -ge "$tries" ]; then
            log_warning "=================================================================="
            log_warning "KEDA has not wired the scaler's metric onto the HPA of these ScaledObjects, so they scale on the CPU default and WVA's decisions do not reach them:"
            printf '%s' "$WVA_SCALER_UNWIRED" | sed 's/ .*//; s/^/    /' >&2
            wva_scaler_keda_conn_report || true
            log_warning "  KEDA asks for the metric spec only when a ScaledObject's spec changes. Once the scaler answers (make verify-deployment), change it again:"
            printf '%s' "$WVA_SCALER_UNWIRED" | while IFS= read -r row; do
                [ -n "$row" ] && printf '    %s\n' "$(wva_scaler_rewire_cmd "$row")" >&2
            done
            log_warning "=================================================================="
            return 1
        fi
        t=$((t + 1))
        log_info "KEDA has not wired the scaler's metric for $(printf '%s' "$WVA_SCALER_UNWIRED" | sed 's/ .*//' | paste -sd' ' -); changing trigger metadata $WVA_SCALER_REWIRE_KEY so it asks again (try $t of $tries)."
        wva_scaler_rewire "$WVA_SCALER_UNWIRED"
    done
    if [ -n "$WVA_SCALER_WIRE_UNKNOWN" ]; then
        log_info "Could not read these ScaledObjects or their HPAs, so whether KEDA wired them is not checked: $(printf '%s' "$WVA_SCALER_WIRE_UNKNOWN" | paste -sd' ' -)"
    fi
    return 0
}

# wva_scaler_tls_finish moves the ScaledObjects that must follow the scaler to
# its new mode (WVA_SCALER_TLS_MIGRATE_ROWS), once the controller is applied.
# Only once its rollout finished (WVA_CONTROLLER_ROLLED_OUT, set by
# deploy_wva_controller) and the new Pods alone serve the scaler
# (wva_scaler_wait_sole_server): moved earlier, KEDA dials a mode an old Pod
# does not serve. Otherwise it names them and the command that moves them
# later. After the move, it checks KEDA wired each HPA (wva_scaler_ensure_wired).
wva_scaler_tls_finish() {
    [ -n "${WVA_SCALER_TLS_MIGRATE_ROWS:-}" ] || return 0
    local why="" rows
    if [ "${WVA_CONTROLLER_ROLLED_OUT:-true}" != true ]; then
        why="The controller rollout did not finish"
    elif ! wva_scaler_wait_sole_server; then
        why="The new controller Pods are not yet the only thing serving the scaler ($WVA_SCALER_SOLE_WHY)"
    fi
    if [ -n "$why" ]; then
        log_warning "$why, so these ScaledObjects were NOT moved to the $WVA_SCALER_TLS_MIGRATE_MODE scaler address:"
        printf '%s\n' "$WVA_SCALER_TLS_MIGRATE_ROWS" | head -20 | sed 's/^/    /' >&2
        log_warning "  Once  kubectl rollout status deployment/wva-controller-manager -n $WVA_NS  succeeds and the old Pods are gone, move them:  $(wva_scaler_tls_migrate_cmd "$([ "$WVA_SCALER_TLS_MIGRATE_MODE" = tls ] && echo true || echo false)")"
        WVA_SCALER_TLS_MIGRATE_ROWS=""
        return 0
    fi
    [ -z "${WVA_SCALER_SOLE_SKIPPED:-}" ] || log_info "Not waited on before the move: the $WVA_SCALER_SOLE_SKIPPED (not readable)."
    rows="$WVA_SCALER_TLS_MIGRATE_ROWS"
    wva_scaler_migrate_scaledobjects "$rows" "$WVA_SCALER_TLS_MIGRATE_MODE"
    WVA_SCALER_TLS_MIGRATE_ROWS=""
    if [ -n "$WVA_SCALER_MIGRATE_FAILED" ]; then
        log_warning "These ScaledObjects could not be moved to the $WVA_SCALER_TLS_MIGRATE_MODE scaler address. Until they are, they may stop scaling, and after a KEDA restart they can stop the others on their address too (GitOps may also put the old trigger back; change it at the source):"
        printf '%s' "$WVA_SCALER_MIGRATE_FAILED" | sed 's/^/    /' >&2
        rows="$(printf '%s\n' "$rows" | grep -vxF -f <(printf '%s' "$WVA_SCALER_MIGRATE_FAILED") || true)"
    fi
    wva_scaler_ensure_wired "$rows" || true
}

# wva_scaler_tls_permission_needs echoes "<resource> <namespace>" for each kind
# this phase creates for scaler TLS ("-" for a cluster-scoped kind), when TLS
# was asked for or is already on. The default asks for nothing: without the
# rights it stays on plaintext and says so. Echoes only, never refuses, so the
# permission check can call it in a substitution.
wva_scaler_tls_permission_needs() {
    local on=false provider="${WVA_SCALER_TLS_PROVIDER:-}" phase="${INSTALL_PHASE:-all}"
    wva_scaler_tls_read_live
    case "${WVA_SCALER_TLS:-}" in
        true) on=true ;;
        "") case "$WVA_SCALER_TLS_LIVE" in cert-manager|openssl) on=true ;; esac ;;
    esac
    [ "$on" = true ] || return 0
    echo "secrets $WVA_NS"
    if [ -z "$provider" ]; then
        case "$WVA_SCALER_TLS_LIVE" in
            cert-manager|openssl) provider="$WVA_SCALER_TLS_LIVE" ;;
            *) wva_cert_manager_api
               [ "$WVA_CERT_MANAGER_API" = present ] && provider=cert-manager ;;
        esac
    fi
    if [ "$provider" = cert-manager ]; then
        echo "certificates.cert-manager.io $WVA_NS"
        echo "issuers.cert-manager.io $WVA_NS"
    fi
    [ "$phase" != wva ] || return 0
    echo "configmaps $WVA_NS"
    echo "clustertriggerauthentications.keda.sh -"
    wva_scaler_keda_object_ns
    [ -z "$WVA_SCALER_KEDA_OBJ_NS" ] || echo "secrets $WVA_SCALER_KEDA_OBJ_NS"
}

# wva_scaler_tls_undeploy removes what wva_scaler_tls_prepare created in WVA_NS:
# only objects carrying its label. The OpenShift service CA's serving Secret is
# the platform's and goes with the Service.
wva_scaler_tls_undeploy() {
    wva_cert_manager_api
    if [ "$WVA_CERT_MANAGER_API" = present ]; then
        kubectl delete certificates.cert-manager.io,issuers.cert-manager.io -n "$WVA_NS" \
            -l "$WVA_SCALER_TLS_LABEL" --ignore-not-found >/dev/null 2>&1 || true
    fi
    kubectl delete secret -n "$WVA_NS" -l "$WVA_SCALER_TLS_LABEL" --ignore-not-found >/dev/null 2>&1 \
        || log_warning "Could not remove the external scaler certificate Secrets (label $WVA_SCALER_TLS_LABEL) in $WVA_NS."
    # KEDA's side, and the marker that records it, are the admin's: only an
    # uninstall that may write them removes them.
    [ "${WVA_APPLY_SCOPE:-all}" != controller ] || return 0
    # cta_gone and copy_gone: this installer's ClusterTriggerAuthentication and
    # copy are known to be gone (removed now, or not there). Until both are, the
    # marker stays: it is how a later uninstall finds the copy.
    local name marker_state copy_ns="" marker_ns="" out rc=0 removed=false cta_gone=false copy_gone=false
    name="$(wva_scaler_keda_auth_name)"
    # The marker records where the copy went. Read it before anything is
    # deleted, and delete it last: it is how a re-run finds the copy.
    wva_scaler_object_state configmap "$WVA_SCALER_KEDA_MARKER"
    marker_state="$WVA_OBJ_STATE"
    if [ "$marker_state" = ours ]; then
        marker_ns="$(printf '%s' "$WVA_OBJ_JSON" | jq -r '.data.secretNamespace // ""' 2>/dev/null)" || marker_ns=""
    fi

    out="$(kubectl get clustertriggerauthentications.keda.sh "$name" -o json 2>&1)" || rc=$?
    if [ "$rc" = 0 ]; then
        if ! printf '%s' "$out" | jq -e --arg k "$WVA_SCALER_TLS_LABEL" '.metadata.labels[$k] // empty' >/dev/null 2>&1; then
            log_warning "ClusterTriggerAuthentication $name was not created by this installer (no $WVA_SCALER_TLS_LABEL label); left in place."
            cta_gone=true
        elif kubectl delete clustertriggerauthentications.keda.sh "$name" --ignore-not-found >/dev/null 2>&1; then
            removed=true
            cta_gone=true
        else
            log_warning "Could not remove ClusterTriggerAuthentication $name."
        fi
    elif wva_scaler_not_found "$out"; then
        cta_gone=true
    else
        log_warning "Could not read ClusterTriggerAuthentication $name ($(printf '%s\n' "$out" | head -1)); left in place."
    fi

    # Where KEDA reads its Secrets, worked out from sources a tenant cannot
    # choose, comes first. The marker lives in the controller namespace, whose
    # admin can edit it, so it is used only when that is not known.
    wva_scaler_keda_object_ns
    copy_ns="$WVA_SCALER_KEDA_OBJ_NS"
    if [ -z "$copy_ns" ]; then
        copy_ns="$marker_ns"
    elif [ -n "$marker_ns" ] && [ "$marker_ns" != "$copy_ns" ]; then
        log_warning "ConfigMap $WVA_SCALER_KEDA_MARKER in $WVA_NS says KEDA's copy of the scaler client certificate went to $marker_ns, but KEDA reads its Secrets from $copy_ns. Only $copy_ns is cleaned up. Check $marker_ns by hand:  kubectl get secret $name -n $marker_ns --show-labels"
    fi
    if [ -n "$copy_ns" ]; then
        WVA_NS="$copy_ns" wva_scaler_object_state secret "$name"
        case "$WVA_OBJ_STATE" in
            absent) copy_gone=true ;;
            ours)
                if kubectl delete secret "$name" -n "$copy_ns" --ignore-not-found >/dev/null 2>&1; then
                    removed=true
                    copy_gone=true
                else
                    log_warning "Could not remove KEDA's copy of the scaler client certificate, Secret $name in $copy_ns."
                fi ;;
            foreign)
                log_warning "Secret $name in $copy_ns was not created by this installer (no $WVA_SCALER_TLS_LABEL label); left in place."
                copy_gone=true ;;
            unreadable) log_warning "Could not read Secret $name in $copy_ns ($WVA_OBJ_ERR); KEDA's copy of the scaler client key may still be there." ;;
        esac
    elif [ "$marker_state" != absent ]; then
        log_warning "Where KEDA's copy of the scaler client certificate went is not known (${WVA_SCALER_KEDA_OBJ_WHY:-the marker names no namespace}); Secret $name in KEDA's namespace may still be there."
    else
        # No marker and nowhere to look: nothing records a copy.
        copy_gone=true
    fi

    case "$marker_state" in
        ours)
            if [ "$cta_gone" = true ] && [ "$copy_gone" = true ]; then
                kubectl delete configmap "$WVA_SCALER_KEDA_MARKER" -n "$WVA_NS" --ignore-not-found >/dev/null 2>&1 || true
            else
                log_warning "ConfigMap $WVA_SCALER_KEDA_MARKER in $WVA_NS is kept: KEDA's side (ClusterTriggerAuthentication $name and its Secret in KEDA's namespace) was not removed, and the marker records where it is. A cluster admin removes both with:  $(wva_make_cmd undeploy-wva)"
            fi ;;
        foreign) log_warning "ConfigMap $WVA_SCALER_KEDA_MARKER in $WVA_NS was not created by this installer (no $WVA_SCALER_TLS_LABEL label); left in place." ;;
    esac
    # KEDA opens a connection for the TLS address only when a ScaledObject on
    # it dials, and then keeps it, with this client certificate, until its
    # operator restarts. So the advice applies only when one does now (or they
    # cannot be listed). One that moved off TLS earlier also left one behind;
    # the reinstall says so when it writes KEDA's side again.
    if [ "$removed" = true ]; then
        local tls_rows tls_rc=0
        tls_rows="$(wva_scaler_plaintext_scaledobjects plaintext)" || tls_rc=$?
        if [ "$tls_rc" != 0 ] || [ -n "$tls_rows" ]; then
            # The uninstall restarts it once those ScaledObjects are gone
            # (wva_scaler_keda_restart_after_undeploy): with nothing left to
            # dial the address, the restart drops the old connection, and a
            # reinstall then builds a new one. Left to the reader, a reinstall
            # here had every handshake refused and every HPA on the CPU default.
            WVA_SCALER_KEDA_RESTART_WANTED=true
            log_info "KEDA's side of the scaler TLS is removed, but ScaledObjects used the TLS address$([ "$tls_rc" != 0 ] && printf ' (or they cannot be listed)'), so KEDA's operator keeps a connection with the old client certificate until it restarts. This uninstall restarts it once the ScaledObjects are removed (KEDA_RESTART=false skips that)."
        fi
    fi
}
