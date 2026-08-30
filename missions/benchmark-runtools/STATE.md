# Mission: benchmark-runtools

End-to-end benchmark run tooling for llm-d WVA.
Worktree: `worktrees/benchmark-runtools` (branch: `benchmark-runtools`).

## Goal

Fix and complete the benchmark run pipeline so that:
1. All 4 workloads run cleanly (smoke_2min, decode_heavy, prefill_heavy, symmetrical)
2. Prometheus/Thanos range query returns actual metric data (currently always empty)
3. Extracted results feed correctly into the viz pipeline

## Current status

Core run pipeline is working (harness, SO management, results copy, patches all fixed and
committed). The remaining gap is Prometheus scraping — both known bugs are still unpatched
on disk despite being documented as "externally fixed" in the bench-runtools3 handoff.

### Committed and working (branch: benchmark-runtools)
- `run_scenario.sh`: SO unpause/pause, HPA wait, results copy, harness field strip, `|| true`
- `run_session.sh`: EPP preflight, image pin by digest, patch delivery via kubectl cp
- `bench_run.sh`: pod stop after session
- `hack/benchmark/patches/fix1_epp_float_ts.py` and `fix2_conversion_non_fatal.py`
- 9 viz cherry-picks from benchmark-viz (render_real_trace.py, report.py, bundles)
- `extract.py`: SO config fallback, pod timings fallback, controller log path
- `docs/plans/benchmark/decisions.md`

### Open: Prometheus scraping — both bugs still present

**Bug 1 — hack/benchmark/bench_init.sh line 153**
Hardcodes `https://thanos-querier.openshift-monitoring.svc.cluster.local:9091`
(cluster-internal SVC URL, unreachable from host). Must use the external Thanos route.
External route: `thanos-querier-openshift-monitoring.apps.pokprod001.ete14.res.ibm.com`
(confirmed working). Prefer accepting it via `BENCHMARK_PROMETHEUS_URL` env var, or
use `oc get route thanos-querier -n openshift-monitoring` to discover dynamically.

**Bug 2 — hack/benchmark/scrape_prometheus_range.sh line 127**
`$KUBECTL whoami -t` — kubectl does not have `whoami -t`; only `oc` does.
AUTH_TOKEN always empty -> Thanos returns 401 -> all 21 queries return empty `[]`.
Fix: use `oc whoami -t 2>/dev/null` (or `${OC_CMD:-oc} whoami -t`) with empty fallback.

## Immediate next step

Fix both Prometheus bugs, commit them, then run a scenario and verify
`prometheus_range.json` contains non-empty metric data.

After that:
- Extract fresh run results with `extract.py` and run viz pipeline
- Port legacy workloads: bursty, sharegpt_inferenceperf, quick_smoke, burst_4k1000, burst_4k250

## Key files (branch: benchmark-runtools, worktrees/benchmark-runtools)

| File | Notes |
|------|-------|
| `hack/benchmark/bench_init.sh` | **BUG: SVC URL line 153** needs external Thanos route |
| `hack/benchmark/scrape_prometheus_range.sh` | **BUG: kubectl whoami -t line 127** needs oc |
| `hack/benchmark/run_scenario.sh` | Committed, working |
| `hack/benchmark/run_session.sh` | Committed, working |
| `hack/benchmark/bench_run.sh` | Committed, working |
| `hack/benchmark/extract.py` | Committed, working |
| `hack/benchmark/patches/fix1_epp_float_ts.py` | Committed |
| `hack/benchmark/patches/fix2_conversion_non_fatal.py` | Committed |
| `hack/benchmark/bench-sessions/dhl-la-1708.yaml` | 4 workloads |
| `hack/benchmark/render_real_trace.py` | Cherry-picked from benchmark-viz |
| `hack/benchmark/report.py` | Cherry-picked from benchmark-viz |
| `docs/plans/benchmark/decisions.md` | Session decisions log |

## Uncommitted out-of-scope files (do NOT stage/commit on this branch)

- `config/base/rbac/kustomization.yaml` — namePrefix replacement fix (different mission)
- `config/base/rbac/epp-metrics-token-secret.yaml` — comment added
- `AGENTS.md` — Mission Scope Rules
- `docs/plans/benchmark/issue-epp-metrics-token-nameprefix.md` — EPP token issue doc
- `hack/benchmark/bench-workloads/prefill_heavy.yaml` — untracked, needs `git add` when ready
- `hack/benchmark/bench-workloads/symmetrical.yaml` — untracked, needs `git add` when ready
- `hack/benchmark/bench-scratch/` — run scratch data, not committed

## Cluster state (dhl-la-1708 / pokprod001)

- `wva-epp-metrics-token` secret: token populated (1728 chars), annotation = `wva-epp-metrics-reader`
- `optimized-baseline-nvidia-gpu-vllm-decode` deployment: 0 replicas (post-run)
- `optimized-baseline-nvidia-gpu-vllm-decode-wva` SO: paused (post-run)
- `llmdbench-harness` pod: deleted (post-run)

## Session log

- 2026-09-01 session=bench-runtools1 status=retired ledger=missions/benchmark-runtools/ledgers/bench-runtools1.md
- 2026-09-01 session=bench-runtools2 status=retired ledger=missions/benchmark-runtools/ledgers/bench-runtools2.md
- 2026-09-01 session=bench-runtools3 status=retired ledger=missions/benchmark-runtools/ledgers/bench-runtools3.md
- 2026-09-01 session=bench-runtools4 status=retired ledger=missions/benchmark-runtools/ledgers/bench-runtools4.md
