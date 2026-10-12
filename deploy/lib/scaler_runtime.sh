#!/usr/bin/env bash
#
# Scaler backend deployment/runtime helpers for deploy/install.sh.
# Requires vars: MONITORING_NAMESPACE, KEDA_NAMESPACE, KEDA_CHART_VERSION.
# Requires funcs: log_info/log_warning/log_success/log_error,
# should_skip_helm_repo_update(), version_at_least() (prereqs.sh), wva_any_pod_ready()/wva_pods_exist() (common.sh).
#

# KEDA_FALLBACK_MIN_VERSION is the oldest KEDA whose ScaledObject has
# `spec.fallback.behavior` (currentReplicasIfHigher), added in KEDA v2.17.0
# ("Add Fallback option `behavior` for dynamic fallback calculation", kedacore
# #6450). It is NOT a minimum for the install: `external-push` is far older
# (2.0), and on an older KEDA the ScaledObjects are written with no fallback
# block at all (so_keda_fallback_probe, scaledobject.sh). The preflight says so
# up front, as a warning, rather than leaving it to the first plan.
KEDA_FALLBACK_MIN_VERSION="${KEDA_FALLBACK_MIN_VERSION:-2.17}"

# wva_keda_installs_itself reports whether THIS run installs KEDA (so its absence
# now is expected, not a fault): kind-emulator always goes the Helm path, and
# Kubernetes does when KEDA_HELM_INSTALL=true.
wva_keda_installs_itself() {
    [ "${ENVIRONMENT:-kubernetes}" = "kind-emulator" ] && return 0
    [ "${ENVIRONMENT:-kubernetes}" = "kubernetes" ] && [ "${KEDA_HELM_INSTALL:-false}" = "true" ]
}

# wva_keda_absent_advice echoes what to do about a cluster with no KEDA, per
# platform. One copy, used by the preflight and by deploy_keda.
wva_keda_absent_advice() {
    if [ "${ENVIRONMENT:-kubernetes}" = "openshift" ]; then
        printf '%s\n' "On OpenShift this installer does not install KEDA: it uses the platform's." \
            "Install the Custom Metrics Autoscaler operator (OperatorHub) and create its" \
            "KedaController (namespace openshift-keda), then re-run this command."
    else
        printf '%s\n' "Install KEDA on the cluster, or let this installer do it:" \
            "    KEDA_HELM_INSTALL=true   (installs chart kedacore/keda ${KEDA_CHART_VERSION:-<pinned>} into ${KEDA_NAMESPACE:-keda-system})"
    fi
}

# wva_keda_old_warning warns that this KEDA predates fallback.behavior, in the
# terms so_keda_fallback_probe uses when the plan finds the same thing: older
# KEDA is supported, its ScaledObjects just get no fallback.
wva_keda_old_warning() {
    local what="$1"
    if [ "${WVA_SO_FALLBACK:-true}" = "false" ]; then
        log_info "KEDA: ${what}; WVA_SO_FALLBACK=false, so nothing here needs fallback.behavior."
        return 0
    fi
    log_warning "KEDA: ${what} -- fallback.behavior arrived in KEDA ${KEDA_FALLBACK_MIN_VERSION}, so the ScaledObjects this install writes get NO fallback. An older KEDA would prune the field and apply a static fallback that forces minReplicaCount. Upgrade KEDA to ${KEDA_FALLBACK_MIN_VERSION} or later to get one."
}

# check_keda_prerequisite answers "is there a KEDA to act on the ScaledObjects
# WVA needs?" BEFORE anything is created.
#
# It used to be answered only by deploy_keda, which ran after the controller had
# been applied -- so a cluster with no KEDA passed `make check-prereqs`, then got
# namespaces, RBAC and a running controller, and only then exit 1.
#
# Three readings, never two:
#   present     the ScaledObject CRD exists and (when the operator can be seen)
#               the operator is Ready. Older than KEDA 2.17 is still present, with
#               a warning: its ScaledObjects get no fallback.
#   absent      NotFound: an error, with what to do on this platform
#   unknown     Forbidden or unreadable: a warning. FORBIDDEN IS NOT ABSENT -- a
#               namespace admin cannot read CRDs, and refusing them for a KEDA that
#               is there all along sent them to an admin for nothing.
#
# Sets WVA_KEDA_STATE (present|absent-installing|unknown|skipped) for the summary,
# so that line reports what was measured rather than what was assumed.
check_keda_prerequisite() {
    WVA_KEDA_STATE=unknown
    if [ "${SCALER_BACKEND:-keda}" != "keda" ]; then
        WVA_KEDA_STATE=skipped
        log_info "KEDA: not checked (SCALER_BACKEND=${SCALER_BACKEND:-}). WVA needs KEDA to actuate; something else must provide it."
        return 0
    fi

    local probe rc=0
    # || rc=$?, never ; rc=$? -- under set -e the plain form exits on the very
    # NotFound this exists to explain.
    probe=$(kubectl get crd scaledobjects.keda.sh -o jsonpath='{.spec.versions[*].schema.openAPIV3Schema.properties.spec.properties.fallback.properties.behavior.type}' 2>&1) || rc=$?
    if [ $rc -ne 0 ]; then
        if printf '%s' "$probe" | grep -qi 'forbidden'; then
            log_warning "KEDA: cannot verify (reading CustomResourceDefinitions is not permitted for you), so this install assumes KEDA is present."
            log_warning "  If it is not, ScaledObjects stay unready and nothing scales. A cluster admin can confirm: kubectl get crd scaledobjects.keda.sh"
            return 0
        fi
        if printf '%s' "$probe" | grep -qiE 'notfound|not found'; then
            if wva_keda_installs_itself; then
                WVA_KEDA_STATE=absent-installing
                log_info "KEDA: not on the cluster yet; this run installs it (chart kedacore/keda ${KEDA_CHART_VERSION:-})."
                return 0
            fi
            WVA_KEDA_STATE=absent
            log_error "KEDA is not installed (no scaledobjects.keda.sh CRD). WVA cannot actuate without it, and nothing has been applied yet.

$(wva_keda_absent_advice)"
        fi
        log_error "KEDA: could not check for the scaledobjects.keda.sh CRD:
    ${probe}
Nothing has been applied. Fix cluster access, or skip the check with SKIP_CHECKS=true."
    fi

    # The CRD decides whether fallback.behavior is admitted or pruned. Missing,
    # it is an older KEDA: a warning, never a refusal (see KEDA_FALLBACK_MIN_VERSION).
    local old=""
    if [ -z "$(printf '%s' "$probe" | tr -d '[:space:]')" ]; then
        old="its ScaledObject CRD has no spec.fallback.behavior"
    fi

    # The operator. Its pod labels differ between distributions (OpenShift's
    # Custom Metrics Autoscaler is one), so only a definite answer is fatal:
    # operator pods that exist and are none of them Ready.
    local ready_rc=0 image version=""
    wva_any_pod_ready -A "$KEDA_OPERATOR_LABEL_SELECTOR" || ready_rc=$?
    case "$ready_rc" in
        0)
            image="$(kubectl get pods -A -l "$KEDA_OPERATOR_LABEL_SELECTOR" -o jsonpath='{.items[0].spec.containers[0].image}' 2>/dev/null || true)"
            case "$image" in
                *@*) : ;;                                   # digest: no version in it
                *:*) version="${image##*:}"; version="${version#v}" ;;
            esac
            if [ -z "$old" ] && [ -n "$version" ] && [[ "$version" =~ ^[0-9]+\.[0-9]+ ]] \
                && ! version_at_least "$version" "$KEDA_FALLBACK_MIN_VERSION"; then
                old="the operator is ${version}"
            fi
            WVA_KEDA_STATE=present
            log_success "KEDA: ScaledObject CRD present, operator Ready${version:+ (version $version)}"
            ;;
        1)
            if wva_pods_exist -A "$KEDA_OPERATOR_LABEL_SELECTOR"; then
                if wva_keda_installs_itself; then
                    WVA_KEDA_STATE=absent-installing
                    log_warning "KEDA: operator pods exist and none is Ready; this run's Helm install will try to repair it."
                    return 0
                fi
                WVA_KEDA_STATE=absent
                log_error "KEDA: the operator pods ($KEDA_OPERATOR_LABEL_SELECTOR) exist and none is Ready, so no ScaledObject would be reconciled. Nothing has been applied.
    kubectl get pods -A -l $KEDA_OPERATOR_LABEL_SELECTOR"
            fi
            WVA_KEDA_STATE=present
            log_warning "KEDA: the CRD is present, but no operator pod labelled $KEDA_OPERATOR_LABEL_SELECTOR was found to confirm it is running (other distributions label it differently)."
            ;;
        *)
            WVA_KEDA_STATE=present
            log_info "KEDA: the CRD is present; whether its operator is Ready cannot be checked (listing pods cluster-wide is not permitted for you)."
            ;;
    esac
    if [ -n "$old" ]; then
        wva_keda_old_warning "$old"
    fi
    return 0
}

deploy_keda() {
    log_info "Deploying KEDA (scaler backend)..."
    # Deploy flow is non-interactive: missing/failed scaler backend is a hard failure.

    # OpenShift: KEDA is cluster-managed (OLM/operator); never Helm-install — avoids
    # ClusterRole/release conflicts with an existing platform KEDA.
    if [ "$ENVIRONMENT" = "openshift" ]; then
        log_info "OpenShift: assuming platform-managed KEDA — skipping Helm install"
        if kubectl get crd scaledobjects.keda.sh >/dev/null 2>&1; then
            log_success "KEDA ScaledObject CRD is available on the cluster"
        else
            log_error "OpenShift: scaledobjects.keda.sh CRD not found (SCALER_BACKEND=keda).

$(wva_keda_absent_advice)"
        fi
        return
    fi

    # Kubernetes (e.g. CKS, shared clusters): assume cluster-managed KEDA; never Helm unless opted in.
    if [ "$ENVIRONMENT" = "kubernetes" ] && [ "${KEDA_HELM_INSTALL:-false}" != "true" ]; then
        log_info "Kubernetes: assuming cluster-managed KEDA — skipping Helm (set KEDA_HELM_INSTALL=true to install via Helm)"
        # Distinguish "absent" from "not allowed to look". A namespace admin cannot
        # read cluster-scoped CRDs, and treating that Forbidden as NotFound aborted
        # their install — for a KEDA that was present all along, and only AFTER the
        # controller had been applied, leaving it half-installed.
        # || keda_rc=$?, not ; keda_rc=$?. The plain form aborts the whole
        # install at this assignment the moment the CRD is absent, which is the
        # ONE case the message below exists to explain -- so the operator got
        # a bare exit 2 and a half-applied prerequisite set instead of being
        # told to set KEDA_HELM_INSTALL=true.
        local keda_probe keda_rc=0
        keda_probe=$(kubectl get crd scaledobjects.keda.sh 2>&1) || keda_rc=$?
        if [ $keda_rc -eq 0 ]; then
            log_success "KEDA ScaledObject CRD is available on the cluster"
        elif printf '%s' "$keda_probe" | grep -qi 'forbidden'; then
            log_warning "Cannot check for the KEDA CRD (reading cluster-scoped CRDs is not permitted for you), so this install assumes KEDA is present."
            log_warning "  If it is not, ScaledObjects will stay unready and nothing will scale. Confirm with whoever administers the cluster."
        else
            log_error "Kubernetes: scaledobjects.keda.sh CRD not found.

$(wva_keda_absent_advice)"
        fi
        return
    fi

    # Skip install if KEDA is already fully operational on the cluster.
    # Check CRD + operator pods + external metrics APIService to avoid false positives
    # from stale CRDs left behind after a prior uninstall.
    if kubectl get crd scaledobjects.keda.sh >/dev/null 2>&1; then
        # Readiness, not `grep -q Running`. This decides whether to install KEDA
        # at all, so a "0/1 Running" operator answering yes here skips the install
        # that would have fixed it and leaves the cluster with a KEDA that never
        # reconciles anything.
        if wva_any_pod_ready -A "$KEDA_OPERATOR_LABEL_SELECTOR"; then
            if kubectl get apiservice "$EXTERNAL_METRICS_APISERVICE_NAME" >/dev/null 2>&1; then
                log_success "KEDA CRD, operator, and metrics APIService detected — skipping helm install"
                return
            fi
        fi
        # Shared clusters (e.g. CKS) often pre-install KEDA without the exact pod label / APIService
        # shape our probe expects, but ClusterRole keda-operator already exists without Helm metadata.
        # Helm install then fails with ownership errors — skip Helm when that pattern is present.
        if kubectl get clusterrole keda-operator >/dev/null 2>&1; then
            keda_cr_managed_by=$(kubectl get clusterrole keda-operator -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}' 2>/dev/null || true)
            if [ "$keda_cr_managed_by" != "Helm" ]; then
                log_info "KEDA CRD present and ClusterRole keda-operator is not Helm-managed — skipping Helm install (pre-installed KEDA)"
                return
            fi
        fi
        log_warning "KEDA ScaledObject CRD found but operator or metrics APIService not detected; proceeding with helm install"
    fi

    kubectl create namespace "$KEDA_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

    helm repo add kedacore https://kedacore.github.io/charts 2>/dev/null || true
    if [ "$(should_skip_helm_repo_update)" = "true" ]; then
        log_info "Skipping helm repo update for KEDA (SKIP_HELM_REPO_UPDATE=true)"
    else
        helm repo update
    fi

    if ! helm upgrade -i "$KEDA_RELEASE_NAME" kedacore/keda \
        --version "$KEDA_CHART_VERSION" \
        -n "$KEDA_NAMESPACE" \
        --set prometheus.metricServer.enabled=true \
        --set prometheus.operator.enabled=true \
        --wait \
        --timeout=5m; then
        log_error "KEDA Helm installation failed (SCALER_BACKEND=keda)"
    else
        WVA_KEDA_STATE=absent-installing
        log_success "KEDA deployed in $KEDA_NAMESPACE"
    fi
}

