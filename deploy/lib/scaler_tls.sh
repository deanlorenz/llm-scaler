#!/usr/bin/env bash
#
# Certificates for the KEDA external scaler (:9090), and the controller flags
# that serve them.
#
# WVA_SCALER_TLS=true makes the controller serve the scaler over mutual TLS:
# a serving certificate for the scaler Service, a client certificate for KEDA,
# and the controller pinned to that client's identity. The default is false
# until the installer also wires a TriggerAuthentication into every ScaledObject
# it writes -- KEDA dials plaintext without one, and a TLS server refuses it.
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
# log_error; kubectl and jq on PATH (openssl for the no-cert-manager path).

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
    out="$(kubectl get deployment wva-controller-manager -n "$WVA_NS" -o json 2>&1)" || rc=$?
    if [ "$rc" != 0 ]; then
        if printf '%s' "$out" | grep -qi 'notfound'; then
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
    elif printf '%s' "$out" | grep -qi 'notfound\|could not find the requested resource'; then
        WVA_CERT_MANAGER_API=absent
    else
        WVA_CERT_MANAGER_API=unknown
        WVA_CERT_MANAGER_API_ERR="$(printf '%s\n' "$out" | head -1)"
    fi
}

# wva_resolve_scaler_tls decides whether this install serves the scaler over
# TLS (WVA_SCALER_TLS_ON) and who issues the certificates
# (WVA_SCALER_TLS_PROVIDER_RESOLVED: cert-manager or openssl).
#
# Unset, WVA_SCALER_TLS keeps what the running controller has: an upgrade must
# never quietly take an install back to plaintext. A controller whose TLS flags
# this installer did not set is a refusal rather than a guess, since the apply
# would drop them. Sets variables rather than echoing, because log_error is an
# exit and an exit inside $( ) ends only the subshell.
wva_resolve_scaler_tls() {
    WVA_SCALER_TLS_ON=false
    WVA_SCALER_TLS_KEPT=false
    WVA_SCALER_TLS_PROVIDER_RESOLVED=""
    wva_scaler_tls_read_live
    case "${WVA_SCALER_TLS:-}" in
        true) WVA_SCALER_TLS_ON=true ;;
        false) ;;
        "")
            case "$WVA_SCALER_TLS_LIVE" in
                absent|plaintext) ;;
                unreadable)
                    log_error "Could not read Deployment wva-controller-manager in $WVA_NS ($WVA_SCALER_TLS_LIVE_ERR), so whether its external scaler serves TLS is unknown and cannot be kept. Re-run with WVA_SCALER_TLS=true or WVA_SCALER_TLS=false." ;;
                manual)
                    log_error "The running controller in $WVA_NS serves the external scaler over TLS with flags this installer did not set, and this apply would drop them. Re-run with WVA_SCALER_TLS=true to have the installer provision and keep TLS, or WVA_SCALER_TLS=false to accept plaintext." ;;
                *)
                    WVA_SCALER_TLS_ON=true
                    WVA_SCALER_TLS_KEPT=true ;;
            esac
            ;;
        *) log_error "WVA_SCALER_TLS must be true or false (got '${WVA_SCALER_TLS}')" ;;
    esac
    [ "$WVA_SCALER_TLS_ON" = true ] || return 0

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
                *) log_error "Could not tell whether cert-manager is installed (${WVA_CERT_MANAGER_API_ERR:-discovery failed}). Re-run with WVA_SCALER_TLS_PROVIDER=cert-manager or WVA_SCALER_TLS_PROVIDER=openssl." ;;
            esac
            ;;
        *) log_error "WVA_SCALER_TLS_PROVIDER must be auto, cert-manager or openssl (got '$want')" ;;
    esac
    WVA_SCALER_TLS_PROVIDER_RESOLVED="$want"
    if [ "$WVA_SCALER_TLS_KEPT" = true ]; then
        log_info "External scaler TLS stays on (the running controller has it; WVA_SCALER_TLS=false turns it off), certificates from $want"
    else
        log_info "External scaler TLS: mutual TLS, certificates from $want"
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
        if printf '%s' "$out" | grep -qi 'notfound'; then
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
            log_error "Cannot read $resource/$name in $WVA_NS ($WVA_OBJ_ERR), so whether this installer created it is unknown. Grant get on $resource there, or set WVA_SCALER_TLS=false." ;;
        *)
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
        || log_error "Could not write Secret $name in $WVA_NS."
}

# wva_scaler_sha256 echoes the SHA-256 of file $1.
wva_scaler_sha256() {
    openssl dgst -sha256 "$1" 2>/dev/null | awk '{ print $NF }'
}

# wva_scaler_check_openshift_serving makes room for the service CA to write the
# serving Secret: absent or already the service CA's is fine, one this installer
# wrote earlier is removed, anything else is a refusal.
wva_scaler_check_openshift_serving() {
    local svc
    wva_scaler_object_state secret "$WVA_SCALER_TLS_SECRET"
    case "$WVA_OBJ_STATE" in
        absent) return 0 ;;
        unreadable)
            log_error "Cannot read Secret $WVA_SCALER_TLS_SECRET in $WVA_NS ($WVA_OBJ_ERR), so whether the service CA owns it is unknown. Grant get on secrets there, or set WVA_SCALER_TLS=false." ;;
        ours)
            kubectl delete secret "$WVA_SCALER_TLS_SECRET" -n "$WVA_NS" --ignore-not-found >/dev/null 2>&1 \
                || log_error "Could not remove Secret $WVA_SCALER_TLS_SECRET in $WVA_NS so the service CA can issue it."
            return 0 ;;
    esac
    svc="$(printf '%s' "$WVA_OBJ_JSON" | jq -r '.metadata.annotations["service.beta.openshift.io/originating-service-name"] // ""' 2>/dev/null)" || svc=""
    [ "$svc" = wva-external-scaler ] && return 0
    log_error "Secret $WVA_SCALER_TLS_SECRET in $WVA_NS was not issued by the OpenShift service CA for wva-external-scaler, nor by this installer; refusing to take it over. Remove or rename it, or set WVA_SCALER_TLS=false."
}

# wva_scaler_tls_provision_openssl mints or refreshes the CA and leaves with
# openssl and sets WVA_SCALER_CLIENT_CA_HASH.
wva_scaler_tls_provision_openssl() {
    local d ca_new=false issued="" kept="" serving=true dns san
    [ "${ENVIRONMENT:-}" = openshift ] && serving=false
    d="$(mktemp -d)"
    chmod 700 "$d"

    # Every refusal first, so none leaves a private key behind in $d.
    wva_scaler_require_writable secret "$WVA_SCALER_CA_SECRET"
    wva_scaler_secret_field tls.crt "$d/ca.crt"
    wva_scaler_secret_field tls.key "$d/ca.key"
    wva_scaler_require_writable secret "$WVA_SCALER_CLIENT_SECRET"
    wva_scaler_secret_field tls.crt "$d/client.old"
    wva_scaler_secret_field ca.crt "$d/client-ca.old"
    if [ "$serving" = true ]; then
        wva_scaler_require_writable secret "$WVA_SCALER_TLS_SECRET"
        wva_scaler_secret_field tls.crt "$d/serving.old"
    else
        wva_scaler_check_openshift_serving
    fi

    # A provider switch: Certificates this installer created would keep
    # rewriting the Secrets below.
    wva_cert_manager_api
    if [ "$WVA_CERT_MANAGER_API" = present ]; then
        kubectl delete certificates.cert-manager.io,issuers.cert-manager.io -n "$WVA_NS" \
            -l "$WVA_SCALER_TLS_LABEL" --ignore-not-found >/dev/null 2>&1 || true
    fi

    if [ ! -s "$d/ca.crt" ] || [ ! -s "$d/ca.key" ] \
        || ! openssl x509 -in "$d/ca.crt" -noout -checkend $((WVA_SCALER_RENEW_DAYS * 86400)) >/dev/null 2>&1; then
        wva_scaler_new_ca "$d" || { rm -rf "$d"; log_error "openssl could not mint the scaler CA."; }
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
                || { rm -rf "$d"; log_error "openssl could not mint the scaler serving certificate."; }
            wva_scaler_write_secret "$WVA_SCALER_TLS_SECRET" serving "$d/serving.crt" "$d/serving.key" "$d/ca.crt"
            issued="$issued serving"
        else
            kept="$kept serving"
        fi
    fi

    san="$(wva_scaler_client_san)"
    if [ "$ca_new" = true ] || ! wva_scaler_cert_ok "$d/client.old" "$d/ca.crt" "$san" \
        || ! cmp -s "$d/client-ca.old" "$d/ca.crt"; then
        wva_scaler_new_leaf "$d" client "$san" clientAuth "$san" \
            || { rm -rf "$d"; log_error "openssl could not mint the KEDA client certificate."; }
        wva_scaler_write_secret "$WVA_SCALER_CLIENT_SECRET" client "$d/client.crt" "$d/client.key" "$d/ca.crt"
        issued="$issued client"
    else
        kept="$kept client"
    fi

    WVA_SCALER_CLIENT_CA_HASH="$(wva_scaler_sha256 "$d/ca.crt")"
    rm -rf "$d"
    [ -z "$issued" ] || log_info "  issued:${issued} (openssl; leaves valid ${WVA_SCALER_LEAF_DAYS} days, re-issued by a re-install within ${WVA_SCALER_RENEW_DAYS} days of expiry)"
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
  duration: $((WVA_SCALER_LEAF_DAYS * 24))h
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
# Certificates, waits for them, and sets WVA_SCALER_CLIENT_CA_HASH.
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
        log_error "cert-manager is installed but would not accept the scaler's Issuers and Certificates in $WVA_NS:
$(printf '%s\n' "$out" | head -3)
Fix cert-manager (or the permission), or re-run with WVA_SCALER_TLS_PROVIDER=openssl."
    fi
    kubectl apply -f "$d/certs.yaml" >/dev/null || { rm -rf "$d"; log_error "Could not apply the scaler Issuers and Certificates in $WVA_NS."; }
    for cert in $certs; do
        kubectl wait --for=condition=Ready "certificates.cert-manager.io/$cert" -n "$WVA_NS" --timeout=120s >/dev/null 2>&1 \
            || { rm -rf "$d"; log_error "Certificate $cert in $WVA_NS is not Ready after 120s. See: kubectl describe certificate $cert -n $WVA_NS"; }
    done

    wva_scaler_object_state secret "$WVA_SCALER_CLIENT_SECRET"
    wva_scaler_secret_field ca.crt "$d/ca.crt"
    if [ ! -s "$d/ca.crt" ]; then
        rm -rf "$d"
        log_error "Secret $WVA_SCALER_CLIENT_SECRET in $WVA_NS has no ca.crt after issuance (${WVA_OBJ_ERR:-empty}), so the controller would have no client CA."
    fi
    WVA_SCALER_CLIENT_CA_HASH="$(wva_scaler_sha256 "$d/ca.crt")"
    rm -rf "$d"
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

# wva_scaler_tls_prepare is the install step: decide, provision, and patch the
# throw-away overlay $1/kustomization.yaml. The admin phase does nothing here --
# the Deployment, the Service and these Secrets are all the controller
# phase's -- so a tenant re-running deploy-wva keeps TLS working on its own.
wva_scaler_tls_prepare() {
    local kustomization="$1" unauth rc
    case "${WVA_APPLY_SCOPE:-all}" in
        prereqs) return 0 ;;
    esac
    wva_resolve_scaler_tls
    if [ "$WVA_SCALER_TLS_ON" != true ]; then
        case "$WVA_SCALER_TLS_LIVE" in
            cert-manager|openssl)
                log_warning "WVA_SCALER_TLS=false: the external scaler goes back to PLAINTEXT gRPC. Its certificates stay in $WVA_NS; ScaledObjects whose TriggerAuthentication sets caCert keep dialling TLS and go not-Ready until it is removed." ;;
        esac
        return 0
    fi
    # openssl mints the certificates without cert-manager, and digests the
    # client CA with it: needed on both paths.
    command -v openssl >/dev/null 2>&1 \
        || log_error "WVA_SCALER_TLS needs openssl (it mints the scaler certificates when there is no cert-manager, and digests the client CA either way), and it is not on PATH. Install openssl, or set WVA_SCALER_TLS=false."
    if [ "$WVA_SCALER_TLS_PROVIDER_RESOLVED" = openssl ]; then
        wva_scaler_tls_provision_openssl
    else
        wva_scaler_tls_provision_cert_manager
    fi
    wva_append_scaler_tls_patch "$kustomization"

    if declare -F wva_scaler_unauthenticated_scaledobjects >/dev/null; then
        rc=0
        unauth="$(wva_scaler_unauthenticated_scaledobjects)" || rc=$?
        if [ "$rc" = 0 ] && [ -n "$unauth" ]; then
            log_warning "=================================================================="
            log_warning "Scaler TLS is on, and these ScaledObjects name this scaler with no"
            log_warning "  authenticationRef. KEDA dials them in plaintext, the handshake fails,"
            log_warning "  and they go not-Ready -- NOTHING they target scales:"
            printf '%s\n' "$unauth" | head -10 | sed 's/^/    /' >&2
            log_warning "  Give each a TriggerAuthentication with caCert, tlsClientCert and"
            log_warning "  tlsClientKey from Secret $WVA_SCALER_CLIENT_SECRET (see"
            log_warning "  docs/reference/configuration.md#advanced), or set WVA_SCALER_TLS=false."
            log_warning "=================================================================="
        fi
    fi
}

# wva_scaler_tls_permission_needs echoes the namespaced resources the
# controller phase creates for scaler TLS, when TLS would be on: Secrets, plus
# the cert-manager kinds when cert-manager would issue. Echoes only, never
# refuses, so the permission check can call it in a substitution.
wva_scaler_tls_permission_needs() {
    [ "${INSTALL_PHASE:-all}" != prereqs ] || return 0
    local on=false provider="${WVA_SCALER_TLS_PROVIDER:-}"
    wva_scaler_tls_read_live
    case "${WVA_SCALER_TLS:-}" in
        true) on=true ;;
        "") case "$WVA_SCALER_TLS_LIVE" in cert-manager|openssl) on=true ;; esac ;;
    esac
    [ "$on" = true ] || return 0
    echo secrets
    if [ -z "$provider" ]; then
        case "$WVA_SCALER_TLS_LIVE" in
            cert-manager|openssl) provider="$WVA_SCALER_TLS_LIVE" ;;
            *) wva_cert_manager_api
               [ "$WVA_CERT_MANAGER_API" = present ] && provider=cert-manager ;;
        esac
    fi
    if [ "$provider" = cert-manager ]; then
        echo certificates.cert-manager.io
        echo issuers.cert-manager.io
    fi
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
}
