# Task: resolve the collect_metrics.sh gap in the paused standalone-install plan

## Context

This worktree branched off `worktree-benchmark`. Read `docs/plans/benchmark/standalone-cli-install.md`
in full, especially the "Update 2026-08-20" section at the end — that is the authoritative,
already-verified record of everything found so far. Do not re-derive it; it's the result of two
rounds of research this session, read directly against the `llm-d-benchmark` clone.

## Where things stand

- The original goal was a standalone (non-editable pip install) `llmdbenchmark` CLI, decoupled
  from the persistent clone this repo keeps for `benchmark-standup`. That premise is **disproved**:
  `ExecutionContext.base_dir` is never wired up from `--base-dir` in any of `cli.py`'s dispatch
  sites, so `run` mode always falls back to a `__file__`-relative path that only survives an
  *editable* install by coincidence. Do not revisit this path.
- A much better fit exists: `llm-d-benchmark/existing_stack/run_only.sh` + a sibling
  `config_template.yaml` — pure bash, no Python CLI, no venv, no template tree. Read both files in
  full (they're short, in the clone at that path if you re-clone `llm-d-benchmark` at `v0.7.8`, or
  just read the doc's own long quotes from them). `dhl-la-1708` already has a PVC literally named
  `workload-pvc` (the template's own default) and `yq` (mikefarah flavor, v4.53.2) is already
  installed system-wide — both already confirmed, no need to re-check.
- The one real, unresolved gap: `run_only.sh`'s bare pod spec never runs `collect_metrics.sh`
  (`workload/harnesses/collect_metrics.sh` in the clone), the in-pod, kubeconfig-injected script
  the full CLI path uses to scrape vLLM/EPP `/metrics` into `metrics/raw/*_metrics.log` — the files
  `hack/benchmark/extract_real_trace.py` depends on for panel 3 (running/waiting bars), panel 4
  (KV% heatmap), and most of panel 5. Without it, a `run_only.sh`-driven run produces real
  per-request output but no pod-level metrics at all.

## What to do

1. Decide between the two candidate fixes the doc sketches (neither implemented, neither evaluated
   in depth — this is your first real task):
   - (a) Carry `collect_metrics.sh` + kubeconfig injection into our own copy of the pod spec —
     stops this being a verbatim, unmodified import of `run_only.sh`, and means deciding how
     comfortable this is: injecting a kubeconfig into a namespace-scoped pod.
   - (b) Write a client-side vLLM/EPP scraper, extending the exact pattern
     `hack/benchmark/scrape_wva_metrics.sh` already uses for the WVA controller's own authenticated
     `/metrics` (port-forward + scrape from the machine driving the run, not from inside the
     harness pod) — no kubeconfig injection into the cluster at all. The doc's own read: this fits
     this port's established pattern better and avoids putting a kubeconfig inside a pod. Start
     here unless you find a concrete reason (b) doesn't work.
2. Once the metrics-collection gap has a real plan, implement the rest of the design already laid
   out in the doc: bring `run_only.sh` into this repo verbatim at `hack/benchmark/run_only.sh`
   (diff against the clone before committing, header comment recording provenance and pinned ref),
   author a per-namespace run-only config template rendering our existing
   `test/benchmark/scenarios/*.yaml.in` scenarios into its `workload.<name>:` block, and add
   `benchmark-run-only-check` / `benchmark-run-only` Makefile targets matching house `##`
   conventions (see any existing `benchmark-*` target for style).
3. Verify end-to-end against `dhl-la-1708` with the `quick_smoke` scenario: pod comes up, workload
   runs, metrics/raw scrapes land, `extract_real_trace.py` reads the result cleanly (or surfaces a
   concrete, specific shape mismatch to fix, not silent wrong output).

## Cluster safety — read before running anything real

This task **does** touch the shared `dhl-la-1708` cluster once you get to step 3. Standing rules
from this session, all still in force:
- Never patch the llm-d Deployment or hand-patch a ScaledObject's trigger — those belong to
  standup/scaler code, not benchmark tooling. See `deploy/lib/scaledobject.sh` for the sanctioned
  path if you ever find a config drift.
- Run `make benchmark-verify-scaledobjects BENCHMARK_NAMESPACE=dhl-la-1708 BENCHMARK_REPORT_ONLY=true`
  before any real run.
- GPUs are shared. Park the namespace (`make so-park SO=<name> ...` or the equivalent) when you're
  done testing, the same way every other run this session ended.
- **Coordinate with the user before starting a real run** if another worktree/session might also
  be driving load against the same namespace at the same time — this is the one part of "divide
  work across worktrees" that still needs serializing.

## When done

Commit on this branch (`worktree-run-only-gap`), update `standalone-cli-install.md` with the
outcome (resolved gap, what shipped, what's still open), and let the user know.
