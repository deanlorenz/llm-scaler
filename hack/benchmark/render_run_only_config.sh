#!/usr/bin/env bash
# Render one of test/benchmark/scenarios/*.yaml.in into a full run_only.sh
# config file (config_template.yaml's own shape), for `make benchmark-run-only`.
#
# Two substitution passes happen before the profile is spliced into the
# rendered config's workload.<name>: block -- see
# docs/plans/benchmark/run-only-metrics-gap.md for why both are needed:
#
#   1. __REQUEST_RATE__ / __MAX_DURATION__ -- this repo's own tokens,
#      already substituted the same way by benchmark-run's Makefile recipe.
#   2. REPLACE_ENV_LLMDBENCH_DEPLOY_CURRENT_MODEL /
#      REPLACE_ENV_LLMDBENCH_HARNESS_STACK_ENDPOINT_URL -- the full
#      llmdbenchmark CLI's own tokens, normally resolved client-side by
#      llmdbenchmark/utilities/profile_renderer.py before the CLI ever builds
#      a ConfigMap. run_only.sh performs NONE of this substitution (it takes
#      .workload.<key> verbatim into the ConfigMap via `yq ... | explode(.)`)
#      so it has to happen here instead, or the harness pod receives a
#      profile with literal, unresolved REPLACE_ENV_... strings as
#      model_name/base_url.
#
# Usage:
#   render_run_only_config.sh <scenario.yaml.in> <workload-name> <namespace> \
#     <model-id> <endpoint-url> <harness-name> <harness-image> \
#     <hf-token-secret> <request-rate> <max-duration>
#
# Writes the rendered config YAML to stdout.
set -euo pipefail

_scenario_file="${1:?usage: $0 <scenario.yaml.in> <workload-name> <namespace> <model-id> <endpoint-url> <harness-name> <harness-image> <hf-token-secret> <request-rate> <max-duration>}"
_workload_name="${2:?workload-name required}"
_namespace="${3:?namespace required}"
_model_id="${4:?model-id required}"
_endpoint_url="${5:?endpoint-url required}"
_harness_name="${6:?harness-name required}"
_harness_image="${7:?harness-image required}"
_hf_secret="${8:?hf-token-secret required}"
_request_rate="${9:-10}"
_max_duration="${10:-600}"

if [[ ! -f "${_scenario_file}" ]]; then
  echo "render_run_only_config: scenario file not found: ${_scenario_file}" >&2
  exit 1
fi

# Stack name: derived from the model id, sanitized to a k8s-safe token, same
# spirit as run_only.sh's own sanitize_dir_name -- used only as a results
# label, never as a live k8s object name, so this is deliberately looser than
# that function's own charset.
_stack_name="run-only-$(echo "${_model_id}" | tr '/[:upper:]' '-[:lower:]' | tr -c 'a-z0-9-' '-' | sed -e 's/^-*//' -e 's/-*$//')"

_tmp_profile=$(mktemp)
trap 'rm -f "${_tmp_profile}"' EXIT

sed \
  -e "s/__REQUEST_RATE__/${_request_rate}/g" \
  -e "s/__MAX_DURATION__/${_max_duration}/g" \
  -e "s|REPLACE_ENV_LLMDBENCH_DEPLOY_CURRENT_MODEL|${_model_id}|g" \
  -e "s|REPLACE_ENV_LLMDBENCH_HARNESS_STACK_ENDPOINT_URL|${_endpoint_url}|g" \
  "${_scenario_file}" > "${_tmp_profile}"

# Fail loudly on a leftover token rather than shipping a profile that would
# make the harness pod hit a literal, unresolved string as its model/URL --
# see this script's own header on why run_only.sh cannot catch this itself.
if grep -qE '__REQUEST_RATE__|__MAX_DURATION__|REPLACE_ENV_LLMDBENCH_' "${_tmp_profile}"; then
  echo "render_run_only_config: unresolved token(s) remain after substitution:" >&2
  grep -nE '__REQUEST_RATE__|__MAX_DURATION__|REPLACE_ENV_LLMDBENCH_' "${_tmp_profile}" >&2
  exit 1
fi

# mikefarah/yq has no jq-style --arg: variables cross in via env vars, read
# back with strenv(NAME) (string) / env(NAME). See yq's own docs -- confirmed
# empirically against the v4.53.2 installed here, --arg is not a real flag.
STACK_NAME="${_stack_name}" \
MODEL_ID="${_model_id}" \
NAMESPACE="${_namespace}" \
ENDPOINT_URL="${_endpoint_url}" \
HF_SECRET="${_hf_secret}" \
HARNESS_NAME="${_harness_name}" \
HARNESS_IMAGE="${_harness_image}" \
WORKLOAD_NAME="${_workload_name}" \
PROFILE_PATH="${_tmp_profile}" \
yq -n '{
    "endpoint": {
      "stack_name": strenv(STACK_NAME),
      "model": strenv(MODEL_ID),
      "namespace": strenv(NAMESPACE),
      "base_url": strenv(ENDPOINT_URL),
      "hf_token_secret": strenv(HF_SECRET)
    },
    "control": {
      "kubectl": "kubectl"
    },
    "harness": {
      "name": strenv(HARNESS_NAME),
      "namespace": strenv(NAMESPACE),
      "parallelism": 1,
      "wait_timeout": 600,
      "image": strenv(HARNESS_IMAGE),
      # Always populated even under local (-o) output, which never reads
      # this field -- run_only.sh (the upstream script too, confirmed
      # against the pinned v0.7.8 ref: existing_stack/run_only.sh:570)
      # unconditionally references harness_results_pvc in one status message
      # regardless of storage mode. Omitting it here reproduced that as a
      # set -u crash, found live against dhl-e2e-231 on 2026-08-21, rather
      # than exercising a code path run_only.sh actually guards.
      "results_pvc": "unused-local-output-mode"
    },
    "env": [],
    "workload": {
      (strenv(WORKLOAD_NAME)): load(strenv(PROFILE_PATH))
    }
  }'
