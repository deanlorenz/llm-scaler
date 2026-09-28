# SGLang Backend Support

## Overview

llm-scaling-manager supports both [vLLM](https://github.com/vllm-project/vllm)
and [SGLang](https://github.com/sgl-project/sglang) inference engines. The scaling manager reads
each engine's Prometheus metrics to estimate load and capacity, then drives the
scale subresource through HPA/KEDA exactly as it does for vLLM.

The inference engine is **auto-detected per variant** — you do not configure it.
A deployment with no SGLang signal is treated as vLLM, so existing vLLM
deployments are unaffected.

## How detection works

The scaling manager inspects the variant's scale-target pod template (the Deployment/LeaderWorkerSet
the ScaledObject points at) and classifies it as SGLang when **either**:

- a container image reference contains `sglang` (e.g. `lmsysorg/sglang:...`), or
- a container command/args invoke `sglang.launch_server` / `sglang serve`
  (including inside a `/bin/sh -c "..."` wrapper).

Otherwise the variant is treated as **vLLM** (the default).

There is nothing engine-specific to declare. A workload is registered by
creating a KEDA ScaledObject for it, exactly as for vLLM, and the same
ScaledObject works for either engine: the engine is read off the scale
target's pod template, not off any field you set. See
[workload preparation](workload-preparation.md) for the registration itself.

## Metrics

The scaling manager requires the same autoscaling signals from either engine. SGLang exposes them
under `sglang:*` metric names (labeled with `model_name`, which the scaling manager filters on).
The mapping:

| Signal | vLLM | SGLang |
|--------|------|--------|
| KV-cache utilization (0–1) | `vllm:kv_cache_usage_perc` | `sglang:token_usage` |
| Queue depth | `vllm:num_requests_waiting` | `sglang:num_queue_reqs` |
| Total KV-cache token capacity | derived from `vllm:cache_config_info` | `sglang:max_total_num_tokens` |
| Time to first token | `vllm:time_to_first_token_seconds` | `sglang:time_to_first_token_seconds` |
| Inter-token latency | `vllm:inter_token_latency_seconds` | `sglang:inter_token_latency_seconds` |
| Prompt tokens / request | `vllm:request_prompt_tokens_*` | `sglang:prompt_tokens_histogram_*` |
| Generation tokens / request | `vllm:request_generation_tokens_*` | `sglang:generation_tokens_histogram_*` |
| Prefix-cache hit rate | `vllm:prefix_cache_hits / _queries` | `sglang:cached_tokens_total / sglang:prompt_tokens_total` |
| Request count (scale-to-zero) | `vllm:request_success_total` | `sglang:num_requests_total` *(not yet enabled — see Caveats)* |

> Make sure your Prometheus (or VictoriaMetrics) scrapes the SGLang pods' `/metrics`
> endpoint, the same way you scrape vLLM. The scaling manager only consumes metrics that are
> actually present in your monitoring backend.

## Serving-flag mapping

When a variant has no live metrics yet (e.g. a freshly created or scaled-to-zero
variant), the scaling manager estimates capacity from the deployment's serving flags. SGLang flags
map onto the same internal parameters as their vLLM counterparts:

| Parameter | vLLM flag | SGLang flag |
|-----------|-----------|-------------|
| GPU memory fraction | `--gpu-memory-utilization` | `--mem-fraction-static` |
| Max concurrent requests | `--max-num-seqs` | `--max-running-requests` |
| KV page/block size | `--block-size` | `--page-size` |
| Tensor parallelism | `--tensor-parallel-size` | `--tp-size` / `--tp` |
| Total KV token capacity | (n/a) | `--max-total-tokens` |
| Context length | `--max-model-len` | `--context-length` |
| Eager / no CUDA graph | `--enforce-eager` | `--disable-cuda-graph` |

## Caveats

- **vLLM remains the default.** Detection is conservative: anything without a clear
  SGLang signal is treated as vLLM.
- **A variant runs a single engine.** Mixing vLLM and SGLang containers within one
  variant's pod is not supported.
- **Metric availability.** SGLang must be started with metrics enabled so the
  `sglang:*` series are exposed and scraped.
- **Scale-to-zero reads the SGLang counter.** Idleness is measured from
  `sglang:num_requests_total` rather than the vLLM counter: the detected engine is
  passed to the enforcer, which selects the series for it
  (`CollectModelRequestCountForEngine`). Earlier releases asked for
  `vllm:request_success_total` whatever the engine, so an SGLang model had no such
  series, read as permanently idle, and was refused parking outright to keep it
  safe. That gate is gone; `test/e2e/scale_to_zero_sglang_test.go` covers the
  round trip.

## See also

- Design proposal: [`docs/proposals/sglang-backend.md`](../proposals/sglang-backend.md)
- SGLang server arguments: <https://docs.sglang.io/advanced_features/server_arguments.html>
