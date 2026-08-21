#!/usr/bin/env bash

# Forked from llm-d/llm-d-benchmark's existing_stack/run_only.sh, pinned ref
# v0.7.8 (https://github.com/llm-d/llm-d-benchmark/blob/v0.7.8/existing_stack/run_only.sh),
# adapted 2026-08-21. NOT a verbatim import -- see
# docs/plans/benchmark/run-only-metrics-gap.md for why: upstream's run_only.sh
# never runs collect_metrics.sh, so a run against it produces real per-request
# output but no pod-level vLLM/EPP metrics at all (the data panels 3/4/5 of
# this port's rendered trace depend on). This fork closes that gap by running
# an in-pod collector (run_only_collect_metrics.sh, a trimmed fork of
# collect_metrics.sh -- vLLM/EPP scrape only, see that file's own header)
# through the harness pod's own ServiceAccount (already created below with
# tightly namespace-scoped RBAC) -- no kubeconfig injection, unlike the full
# llmdbenchmark CLI's own pod spec.
#
# Diffed against the pinned upstream ref before every commit that touches
# this file, to keep the adaptation visible and minimal.

if uname -s | grep -qi darwin; then
  alias sed=gsed
fi

# Constants
HARNESS_EXECUTABLE=llm-d-benchmark.sh
CURL_TIMEOUT=10

HARNESS_POD_LABEL="llmdbench-harness-launcher"
HARNESS_EXECUTABLE="llm-d-benchmark.sh"
HARNESS_CPU_NR=16
HARNESS_CPU_MEM=32Gi
RESULTS_DIR_PREFIX=/requests
KUBECTL_TIMEOUT=180
DATASET_DIR=/workspace

# Our collector, mounted alongside the harness's own profile ConfigMap.
COLLECTOR_SCRIPT="$(dirname "$(realpath -- "${BASH_SOURCE[0]}")")/run_only_collect_metrics.sh"
COLLECTOR_MOUNT_DIR=/workspace/collector
COLLECTOR_INTERVAL="${LLMDBENCH_METRICS_COLLECTION_INTERVAL:-15}"


function show_usage {
  cat <<USAGE
Usage: ${_script_name} -c <config-file> [options]

  Runs llm-d-benchmark harness against an existing LLM deployment stack.

  Options:
    -c/--config path to configuration file
    -o/--output destination for the results. (e.g. local/folder, gs://my-bucket, s3://my-bucket)
    -R/--repeat number of times to repeat the experiment (default: 1). Results are aggregated with mean/std dev.
    --pre-workload bash script to run on the harness pod before each workload (overrides config hooks.pre_workload)
    --post-workload bash script to run on the harness pod after each workload (overrides config hooks.post_workload)
    -v/--verbose print the command being executed, and result
    -d/--debug execute harness in "debug-mode"
    -n/--dry-run do not execute commands, just print what would be executed
    -h/--help show this help
USAGE
}

if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
  echo "This script should be executed not sourced" >&2
  show_usage
  return 1
fi

# Log announcement function
function announce {
    local message="${1}"
    local logfile=${2:-none}

    case ${logfile} in
        none|""|"1")
            echo
            echo "===> $(date) - ${0}" #:${LINENO}
            echo -e "$message"
            echo -ne "\033[01;33m";   # br yellow
            echo "------------------------------------------------------------"
            echo -ne "\033[0m"
            ;;
        silent|"0")
            ;;
        *)
            echo -e "==> $(date) - ${0} - $message" >> ${logfile}
            ;;
    esac

}

# Sanitize pod name to conform to Kubernetes naming conventions
function sanitize_pod_name {
  tr [:upper:] [:lower:] <<<"$1" | sed -e 's/[^0-9a-z-][^0-9a-z-]*/-/g' | sed -e 's/^-*//' | sed -e 's/-*$//'
}

# Sanitize directory name to conform to filesystem naming conventions
function sanitize_dir_name {
  sed -e 's/[^0-9A-Za-z_-][^0-9A-Za-z_-]*/_/g' <<<"$1"
}

# Mirrors the results-dir formula build/llm-d-benchmark.sh computes INSIDE the
# pod (LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR_PREFIX/<harness>_<experiment_id>_<stack>)
# -- computed here, in the outer shell, so our collector can be told the exact
# path before llm-d-benchmark.sh re-derives (and overwrites, identically) it
# itself. Any drift between this and that script's own formula would point the
# collector at the wrong directory silently, so keep this in lockstep with it.
function experiment_results_dir {
  local harness_name="$1" experiment_id="$2" stack_name="$3"
  sanitize_dir_name "${RESULTS_DIR_PREFIX}/${harness_name}_${experiment_id}_${stack_name}"
}

# Best-effort discovery of the EPP metrics-reader token secret: installs vary
# in what they name it (this repo's own WVA deploy code produces something
# like <release>-epp-metrics-token, bound to a ServiceAccount whose name
# contains "epp" and "metrics"; llm-d-benchmark's own default assumes
# inference-gateway-sa-metrics-reader-secret from a different install path).
# Pattern-matched rather than hardcoded, same reasoning as wait_serving.sh's
# router/EPP service lookup. Empty output is not an error: the collector
# degrades to unauthenticated EPP scrapes (already handled by
# run_only_collect_metrics.sh's own auth-retry logic).
function detect_epp_metrics_secret {
  local namespace="$1"
  ${control_kubectl} --namespace "${namespace}" get secrets -o name 2>/dev/null \
    | sed 's|^secret/||' \
    | grep -iE 'epp.*metrics|metrics.*epp' \
    | head -1
}

function upload_results {
  local pod_name=$1
  local storage_type=$2
  local destination=$3

  local local_results_dir=$(mktemp -d)
  if [[ "${storage_type}" == "local" ]]; then
    local_results_dir="${destination}/${_uid}"
    local provider="local"
  else
    local provider=$(echo "${destination}" | cut -d: -f1)
  fi
  mkdir -p ${local_results_dir}
  announce "📂 Copying results from pod ${pod_name} to directory '${destination}'"
  $control_kubectl cp "${pod_name}:${RESULTS_DIR_PREFIX}/." "${local_results_dir}" -n "${harness_namespace}"

  case ${provider} in
    gs)
      announce "☁️ Uploading results to GCS bucket ${destination}"
      gcloud storage cp --recursive "${local_results_dir}/" "${destination}/${_uid}/"
      ;;
    s3)
      announce "☁️ Uploading results to S3 bucket ${destination}"
      aws s3 cp --recursive "${local_results_dir}/" "${destination}/${_uid}/"
      ;;
    local)
      announce "ℹ️ Results saved to local folder."
      ;;
    *)
      announce "❌ ERROR: unknown or unsupported storage provider \"${provider}\"."
      exit 1
      ;;
  esac
}

# Retrieve list of available harnesses
function get_harness_list {
  ls ${LLMDBENCH_MAIN_DIR}/workload/harnesses | $LLMDBENCH_CONTROL_SCMD -e 's^inference-perf^inference_perf^' -e 's^vllm-benchmark^vllm_benchmark^' | cut -d '-' -f 1 | $LLMDBENCH_CONTROL_SCMD -n -e 's^inference_perf^inference-perf^' -e 's^vllm_benchmark^vllm-benchmark^' -e 'H;${x;s/\n/,/g;s/^,//;p;}'
}

function start_harness_pod {
  local pod_name=$1
  local storage_type=$2 # "pvc", "local" or "cloud"

  if [ "${harness_dataset_url:=none}" == "none" ]; then
    local is_dataset_url="# "
  else
    local is_dataset_url=""
  fi

  local volume_def="(.spec.volumes[] | select(.name == \"results\"))"
  if [[ "$storage_type" == "pvc" ]]; then
    volume_def="${volume_def}.persistentVolumeClaim.claimName = \"${harness_results_pvc}\"";
  elif [[ "$storage_type" == "local" ]] || [[ "$storage_type" == "cloud" ]]; then
    volume_def="${volume_def}.emptyDir = {}";
  else
    announce "❌ Error: Unsupport storage type '${storage_type}'."
    exit 1
  fi

  ${control_kubectl} --namespace ${harness_namespace} delete pod ${pod_name} --ignore-not-found

  cat <<EOF | yq "${volume_def}" | yq '.spec.containers[0].env = load("'${_config_file}'").env + .spec.containers[0].env' | ${control_kubectl} apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
  namespace: ${harness_namespace}
  labels:
    app: ${HARNESS_POD_LABEL}
spec:
  serviceAccountName: llmdbench-harness-sa
  containers:
  - name: harness
    image: ${harness_image}
    imagePullPolicy: Always
    securityContext:
      runAsUser: 0
    command: ["sh", "-c"]
    args:
    - "sleep 1000000"
    resources:
      limits:
        cpu: "${HARNESS_CPU_NR}"
        memory: ${HARNESS_CPU_MEM}
      requests:
        cpu: "${HARNESS_CPU_NR}"
        memory: ${HARNESS_CPU_MEM}
    env:
    - name: HF_TOKEN
      valueFrom:
        secretKeyRef:
          name: "${endpoint_hf_token_secret}"
          key: HF_TOKEN
    - name: LLMDBENCH_RUN_WORKSPACE_DIR
      value: "/workspace"
    - name: LLMDBENCH_MAGIC_ENVAR
      value: "harness_pod"
    - name: LLMDBENCH_HARNESS_NAME
      value: "${harness_name}"
    - name: LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR_PREFIX
      value: "${RESULTS_DIR_PREFIX}"
    - name: LLMDBENCH_RUN_DATASET_DIR
      value: "${DATASET_DIR}"
    ${is_dataset_url}- name: LLMDBENCH_RUN_DATASET_URL
    ${is_dataset_url}  value: "${harness_dataset_url}"
    - name: LLMDBENCH_HARNESS_STACK_NAME
      value: "${endpoint_stack_name}"
    - name: LLMDBENCH_VLLM_COMMON_NAMESPACE
      value: "${endpoint_namespace}"
    - name: LLMDBENCH_EPP_METRICS_SECRET
      value: "${_epp_metrics_secret}"
    - name: LLMDBENCH_DESCRIPTION_TEXT
      value: "${_description_text}"
    - name: LLMDBENCH_DESCRIPTION_KEYWORDS
      value: "${_description_keywords}"
    volumeMounts:
    - name: results
      mountPath: ${RESULTS_DIR_PREFIX}
    - name: "${harness_name}-profiles"
      mountPath: /workspace/profiles/${harness_name}
    - name: run-only-collector
      mountPath: ${COLLECTOR_MOUNT_DIR}
  volumes:
  - name: results
  - name: ${harness_name}-profiles
    configMap:
      name: ${harness_name}-profiles
  - name: run-only-collector
    configMap:
      name: run-only-collector
      defaultMode: 0755
  restartPolicy: Never
EOF
  ${control_kubectl} wait --for=condition=Ready=True pod ${pod_name} -n ${harness_namespace} --timeout="${KUBECTL_TIMEOUT}s"
  if [[ $? != 0 ]]; then
    announce "❌ Timeout waiting for pod ${pod_name} to get ready"
    exit 1
  fi
  announce "ℹ️ Harness pod ${pod_name} started"
  ${control_kubectl} describe pod ${pod_name} -n ${harness_namespace}
}

set -euo pipefail
cd "$(dirname "$(realpath -- $0)")" > /dev/null 2>&1
_script_name="${0##*/}"
_control_dir=$(realpath $(pwd)/)
_root_dir=$(realpath "${_control_dir}/../")
_uid=$(date +%s)
_repeat=${LLMDBENCH_HARNESS_REPEAT:-1}

#Parse command line arguments
# ========================================================
while [[ $# -gt 0 ]]; do
    key="$1"

    case $key in
        -c=*|--config=*)
        _config_file=$(echo $key | cut -d '=' -f 2)
        ;;
        -c|--config)
        _config_file="$2"
        shift
        ;;
        -o=*|--output=*)
        _output_destination=$(echo $key | cut -d '=' -f 2)
        ;;
        -o|--output)
        _output_destination="$2"
        shift
        ;;
        -R=*|--repeat=*)
        _repeat=$(echo $key | cut -d '=' -f 2)
        ;;
        -R|--repeat)
        _repeat="$2"
        shift
        ;;
        --pre-workload=*)
        _cli_pre_workload=$(echo $key | cut -d '=' -f 2-)
        ;;
        --pre-workload)
        _cli_pre_workload="$2"
        shift
        ;;
        --post-workload=*)
        _cli_post_workload=$(echo $key | cut -d '=' -f 2-)
        ;;
        --post-workload)
        _cli_post_workload="$2"
        shift
        ;;
        -n|--dry-run)
        export $kubectl=1
        ;;
        -d|--debug)
        export LLMDBENCH_HARNESS_DEBUG=1
        ;;
        -v|--verbose)
        export LLMDBENCH_VERBOSE=1
        ;;
        -h|--help)
        show_usage
        exit 0
        ;;
        *)
        announce "❌ ERROR: unknown option \"$key\""
        show_usage
        exit 1
        ;;
        esac
        shift
done

# Validate repeat count
if ! [[ "$_repeat" =~ ^[1-9][0-9]*$ ]]; then
  announce "❌ ERROR: --repeat must be a positive integer, got \"$_repeat\""
  exit 1
fi

# Read configuration file
# ========================================================
announce "📄 Reading configuration file $_config_file"
if ! [[ -f $_config_file  ]]; then
  announce "❌ ERROR: could not find config file \"$_config_file\""
  exit 1
fi
eval $( yq -o shell '. | del(.workload)| del (.env) | del(.description)' "$_config_file")

# Extract optional description metadata
_description_text=$(yq '.description.text // ""' "$_config_file")
_description_keywords=$(yq '.description.keywords // [] | join(",")' "$_config_file")

# Resolve workload hooks (CLI flags override config file values)
# ========================================================
_pre_workload="${_cli_pre_workload:-${hooks_pre_workload:-}}"
_post_workload="${_cli_post_workload:-${hooks_post_workload:-}}"
if [[ -n "$_pre_workload" || -n "$_post_workload" ]]; then
  announce "ℹ️ Workload hooks configured:
  pre_workload: ${_pre_workload:-(none)}
  post_workload: ${_post_workload:-(none)}"
fi

# Verify output destination
# ========================================================
announce "🔎 Verifying output destination"
if [[ -z "${_output_destination:-}" ]]; then
  _storage_type="pvc"
  # PVC mode check
  announce "ℹ️ Verifying results PVC ${harness_results_pvc}"
  if ! $control_kubectl --namespace=${harness_namespace} describe pvc ${harness_results_pvc} &> /dev/null; then
    announce "❌ Error: results PVC '${harness_results_pvc}' not found in namespace '${harness_namespace}'. Please ensure it exists."
    exit 1
  fi
else
  if [[ "${_output_destination}" == *"://"* ]]; then
    _storage_type="cloud"
    _scheme=$(echo "${_output_destination}" | cut -d: -f1)
    _bucket=$(echo "${_output_destination}" | cut -d ':' -f 2 | sed -e 's^//^^g' -e 's:/*$::')
    case "${_scheme}" in
      gs)
        announce "ℹ️ Verifying GCS output destination..."
        if ! command -v gcloud &> /dev/null; then
          announce "❌ 'gcloud' command not found, but is required for 'gs://' output."
          exit 1
        else
          is_bucket=$(gcloud storage buckets describe "gs://${_bucket}" 2>/dev/null && echo "exists" || true)
        fi
        ;;
      s3)
        announce "ℹ️ Verifying S3 output destination..."
        if ! command -v aws &> /dev/null; then
          announce "❌ 'aws' command not found, but is required for 's3://' output."
          exit 1
        else
          is_bucket=$(aws s3 ls "s3://${_bucket}/" 2>/dev/null && echo "exists" || true)
        fi
        ;;
      *)
        announce "❌ ERROR: Unsupported cloud provider scheme '${_scheme}' for destination '${_output_destination}'."
        exit 1
        ;;
    esac

    if [[ -z $is_bucket ]]; then
      announce "❌ ERROR: Bucket \"${_bucket}\" ('${_output_destination}') not found or not accessible."
      exit 1
    else
      announce "✅ Output destination checked"
    fi

  else
    _storage_type="local"
    announce "ℹ️ Verifying local output destination '${_output_destination}'"
    parent_dir=$(dirname "${_output_destination}")
    mkdir -p "${parent_dir}"
    if [[ ! -w "${parent_dir}" ]]; then
      announce "❌ ERROR: Output directory '${parent_dir}' is not writable."
      exit 1
    fi
  fi
fi

if [[ "$harness_parallelism" != "1" ]]; then
    announce "❌ ERROR: harness_parallelism is set to '$harness_parallelism'. Only parallelism=1 is supported."
    exit 1
fi
#@TODO harness_parallelism=1 only is supported for now!!!
#@TODO: The 'upload_results' function currently handles only one pod.
#       To support parallelism, it must collect results from all harness pods.

_harness_pod_name=$(sanitize_pod_name "${HARNESS_POD_LABEL}")

announce "ℹ️ Using endpoint_stack_name=$endpoint_stack_name on endpoint_namespace=$endpoint_namespace running model=${endpoint_model} at endpoint_base_url=$endpoint_base_url"
announce "ℹ️ Using harness_name=$harness_name, with _harness_pod_name=$_harness_pod_name on harness_namespace=$harness_namespace"

# Ensure harness namespace is prepared
# ========================================================
announce "🔧 Ensuring harness namespace is prepared"
_control_dir=$(realpath $(pwd)/)

# Detect the EPP metrics-reader secret before creating RBAC, so the Role
# below can scope its one `secrets` rule to exactly that resource by name
# rather than granting `get` on every secret in the namespace.
_epp_metrics_secret=$(detect_epp_metrics_secret "${endpoint_namespace}")
if [[ -n "${_epp_metrics_secret}" ]]; then
  announce "ℹ️ Detected EPP metrics secret: ${_epp_metrics_secret}"
  _epp_secret_resource_names="\"${_epp_metrics_secret}\""
else
  announce "⚠️ No EPP metrics secret found by name pattern in ${endpoint_namespace} -- collector will fall back to unauthenticated EPP scrapes"
  _epp_secret_resource_names=""
fi

# Create ServiceAccount and RBAC for metrics collection
# ========================================================
announce "🔧 Creating ServiceAccount for metrics collection"
cat <<RBAC_EOF | $control_kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: llmdbench-harness-sa
  namespace: ${harness_namespace}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: llmdbench-metrics-reader
  namespace: ${harness_namespace}
rules:
- apiGroups: [""]
  resources: ["pods", "pods/log"]
  verbs: ["get", "list"]
$(if [[ -n "${_epp_secret_resource_names}" ]]; then cat <<SECRET_RULE
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get"]
  resourceNames: [${_epp_secret_resource_names}]
SECRET_RULE
fi)
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: llmdbench-harness-metrics
  namespace: ${harness_namespace}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: llmdbench-metrics-reader
subjects:
- kind: ServiceAccount
  name: llmdbench-harness-sa
  namespace: ${harness_namespace}
RBAC_EOF
announce "✅ ServiceAccount and RBAC created"

# Verify HF token secret exists
# ========================================================
announce "🔧 Verifying HF token secret ${endpoint_hf_token_secret} in namespace ${endpoint_namespace}"
if $control_kubectl --namespace "$endpoint_namespace" get secret "$endpoint_hf_token_secret" 2>&1 > /dev/null; then
  announce "ℹ️ Using HF token secret $endpoint_hf_token_secret"
else
  announce "❌ ERROR: could not fetch HF token secret $endpoint_hf_token_secret"
  exit 1
fi

# Verify model is deployed and endpoint is reachable
# ========================================================
_verify_model_pod_name=$(sanitize_pod_name "verify-model-${_uid}")
announce "🔍 Verifying model ${endpoint_model} on endpoint ${endpoint_base_url}/v1/completions using pod $_verify_model_pod_name"

set +e
$control_kubectl -n $endpoint_namespace run ${_verify_model_pod_name} \
    --request-timeout=${KUBECTL_TIMEOUT}s --pod-running-timeout=${KUBECTL_TIMEOUT}s \
    -q --rm -i --image=alpine/curl --restart=Never --command -- \
    curl -sS -m $CURL_TIMEOUT -i --fail-with-body "${endpoint_base_url}/v1/completions" \
    -H "Content-Type: application/json" \
    -d '{
        "model": "'${endpoint_model}'",
        "prompt": "Hello"
    }'
if [[ $? != 0 ]]; then
  announce "❌ Error while verifying model"
  exit 1
fi
set -e

# Prepare ConfigMap with workload profiles
# ========================================================
announce "🔧 Preparing ConfigMap with workload profiles"
$control_kubectl --namespace "${harness_namespace}" delete configmap ${harness_name}-profiles --ignore-not-found

cmd=($control_kubectl create cm ${harness_name}-profiles)
cmd+=(--namespace "${harness_namespace}")
for key in $(yq '.workload | keys | .[]' $_config_file); do
  cmd+=( --from-file=${key}.yaml='<(yq ".workload.'$key' | explode(.)" '$_config_file')')
done
eval ${cmd[@]}
announce "ℹ️ ConfigMap '${harness_name}-profiles' created"

# Prepare ConfigMap with our metrics collector script
# ========================================================
announce "🔧 Preparing ConfigMap with the run-only metrics collector"
$control_kubectl --namespace "${harness_namespace}" delete configmap run-only-collector --ignore-not-found
$control_kubectl --namespace "${harness_namespace}" create configmap run-only-collector \
  --from-file="collect_metrics.sh=${COLLECTOR_SCRIPT}"
announce "ℹ️ ConfigMap 'run-only-collector' created"


# Create harness pod
# ========================================================
_pod_name="${_harness_pod_name}"    # place holder for parallelism support
announce "ℹ️ Creating harness pod ${_pod_name}"

set +e
start_harness_pod ${_pod_name} ${_storage_type}
set -e

# Execute workloads
# ========================================================
set +e
announce "ℹ️
  Running benchmark with Experiment ID ${_uid} (repeat=${_repeat}).
  Results will be stored in PVC ${harness_results_pvc}.

  Note:
    Benchmark will continue to run even on time-out or connection failure.
    Can follow progress by checking the logs (${control_kubectl} logs -f ${_pod_name} -n ${harness_namespace}).
"
if [ "${harness_wait_timeout}" -eq 0 ]; then
  _timeout=""
else
  _timeout="timeout ${harness_wait_timeout}s"
fi
declare -a workloads
while IFS= read -r workload; do
  workloads+=("$workload")
done < <(yq '.workload | keys | .[]' "${_config_file}")
announce "Workloads in ${_config_file} are ${workloads[*]}"

for _run_idx in $(seq 1 $_repeat); do
  if [[ $_repeat -gt 1 ]]; then
    announce "ℹ️ Starting repeat ${_run_idx} of ${_repeat}"
  fi

  for workload in "${workloads[@]}"; do
    if [[ $_repeat -gt 1 ]]; then
      _run_experiment_id="${_uid}_${workload}_run${_run_idx}"
    else
      _run_experiment_id="${_uid}_${workload}"
    fi
    announce "ℹ️ Running benchmark with workload ${workload} (experiment_id=${_run_experiment_id})."

    # Run pre-workload hook
    if [[ -n "${_pre_workload}" ]]; then
      announce "🔧 Running pre-workload hook..."
      $control_kubectl exec -i ${_pod_name} -n ${harness_namespace} -- bash -c "${_pre_workload}"
      if [[ $? -ne 0 ]]; then
        announce "⚠️ Warning: pre-workload hook failed for workload ${workload}."
      fi
    fi

    # The results dir llm-d-benchmark.sh will independently (re)compute for
    # this workload -- see experiment_results_dir's own comment. Exported
    # into the exec session below so our collector and llm-d-benchmark.sh
    # agree on where metrics/raw ends up without any IPC between them.
    _results_dir=$(experiment_results_dir "${harness_name}" "${_run_experiment_id}" "${endpoint_stack_name}")

    run_workload=$(cat <<RUN_WORKLOAD
    # redirect to root fds so that kubectl logs can capture output
    exec 1> >(tee /proc/1/fd/1 >&1)
    exec 2> >(tee /proc/1/fd/2 >&2)

    export LLMDBENCH_RUN_EXPERIMENT_ID="${_run_experiment_id}"
    export LLMDBENCH_RUN_EXPERIMENT_RESULTS_DIR="${_results_dir}"
    mkdir -p "${_results_dir}"

    chmod +x ${COLLECTOR_MOUNT_DIR}/collect_metrics.sh
    ${COLLECTOR_MOUNT_DIR}/collect_metrics.sh start >> "${_results_dir}/metrics_collection.log" 2>&1 &
    _collector_pid=\$!

    ${HARNESS_EXECUTABLE} --harness="${harness_name}" --workload="${workload}"
    _harness_rc=\$?

    ${COLLECTOR_MOUNT_DIR}/collect_metrics.sh stop >> "${_results_dir}/metrics_collection.log" 2>&1 || true
    wait \$_collector_pid 2>/dev/null || true
    exit \$_harness_rc
RUN_WORKLOAD
    )
    : | ${_timeout} $control_kubectl exec -i ${_pod_name} -n ${harness_namespace} -- bash -c "$run_workload"
    res=$?

    # Run post-workload hook
    if [[ -n "${_post_workload}" ]]; then
      announce "🔧 Running post-workload hook..."
      $control_kubectl exec -i ${_pod_name} -n ${harness_namespace} -- bash -c "${_post_workload}"
      if [[ $? -ne 0 ]]; then
        announce "⚠️ Warning: post-workload hook failed for workload ${workload}."
      fi
    fi

    if [ $res -eq 0 ]; then
      announce "ℹ️ Benchmark workload ${workload} (repeat ${_run_idx}/${_repeat}) complete."
    elif [ $res -eq 124 ]; then
      announce "⚠️ Warning: workload ${workload} (repeat ${_run_idx}/${_repeat}) timed out after ${harness_wait_timeout}s."
    else
      announce "❌ ERROR: error happened while running workload ${workload} (repeat ${_run_idx}/${_repeat})."
    fi
  done
done
set -e

# Aggregate results across repeated runs
# ========================================================
if [[ $_repeat -gt 1 ]]; then
  announce "ℹ️ Aggregating results across ${_repeat} repeated runs..."
  _aggregate_script="${_root_dir}/analysis/aggregate_runs.py"
  if [[ -f "$_aggregate_script" ]]; then
    for workload in "${workloads[@]}"; do
      # Collect result directories for all runs of this workload
      _run_dirs=""
      for _run_idx in $(seq 1 $_repeat); do
        _run_dirs="${_run_dirs} ${_uid}_${workload}_run${_run_idx}"
      done

      if [[ "${_storage_type}" == "pvc" ]]; then
        # Run aggregation inside the harness pod
        aggregate_cmd=$(cat <<AGG_CMD
python3 /workspace/analysis/aggregate_runs.py \
  --results-prefix "${RESULTS_DIR_PREFIX}" \
  --harness "${harness_name}" \
  --stack "${endpoint_stack_name}" \
  --run-ids ${_run_dirs} \
  --output "${RESULTS_DIR_PREFIX}/${_uid}_${workload}_aggregated"
AGG_CMD
        )
        $control_kubectl exec -i ${_pod_name} -n ${harness_namespace} -- bash -c "$aggregate_cmd"
      else
        # Run aggregation locally
        python3 "$_aggregate_script" \
          --results-prefix "${_output_destination}/${_uid}" \
          --harness "${harness_name}" \
          --stack "${endpoint_stack_name}" \
          --run-ids ${_run_dirs} \
          --output "${_output_destination}/${_uid}/${_uid}_${workload}_aggregated"
      fi

      if [[ $? -eq 0 ]]; then
        announce "✅ Aggregated results for workload ${workload} written."
      else
        announce "⚠️ Warning: aggregation failed for workload ${workload}."
      fi
    done
  else
    announce "⚠️ Warning: aggregation script not found at ${_aggregate_script}. Skipping aggregation."
  fi
fi

# Finalization
# ========================================================
case "${_storage_type}" in
  pvc)
    final_msg=$(cat <<EOM
PVC ${harness_results_pvc}.
  Please use $control_kubectl  -n ${harness_namespace} exec -it ${_pod_name} -- ls -lrt "${RESULTS_DIR_PREFIX}" to list results folders.
  Then, use $control_kubectl cp ${harness_namespace}/${_pod_name}:${RESULTS_DIR_PREFIX}/<result dir> <local dir> to copy to local machine.
EOM
    )
    ;;
  local|cloud)
    upload_results "${_pod_name}" "${_storage_type}" "${_output_destination}"
    if [[ "${_storage_type}" == "local" ]]; then
        final_msg="Local Directory $(realpath "$_output_destination")."
    else
        final_msg="Storage Bucket ${_output_destination}/${_uid}."
    fi
    ;;
esac

announce "✅
  Experiment ID is ${_uid}.
  All workloads completed (${_repeat} run(s) per workload).
  Results should be available in ${final_msg}
"
