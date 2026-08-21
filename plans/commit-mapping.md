# Benchmark Commit Mapping — Agent Briefing

This file tells each benchmark worktree agent exactly which commits to cherry-pick
onto their branch, and in what order. All commits originate from the old shared
benchmark branches (`worktree-benchmark`, `worktree-anchor-offset`,
`worktree-multi-variant`, `worktree-run-only-gap`, `worktree-scaler-issues`).

All target branches start from `upstream/main`. Use `git cherry-pick <hash>` in
order. Where a commit is marked **SPLIT**, see the note — only take the specified
hunks, not the whole commit.

---

## bench-prepare (worktrees/benchmark-init)

**Purpose:** Everything needed BEFORE a run. Cluster env contract, variant
management, preflight checks, ScaledObject verification, GPU reservation.
Must obey shared-cluster safety rules.

| # | Hash | Message | Note |
|---|------|---------|------|
| 1 | `6f4cbf1d` | port shared-cluster safety scaffolding (env_guard, env_wizard, preflight, reset_run) | Core safety/env contract |
| 2 | `ffa87255` | port GPU reservation/coupler tooling | Pre-run cluster prep |
| 3 | `52851b63` | fix gitignore llmdbenchmark workspace-dir pattern | Housekeeping |
| 4 | `ebbcdd50` | rescan/verify ScaledObject modelID before every benchmark run | Preflight gate |
| 5 | `88a9d75b` | add_variant.py support for optimized-baseline topology | Variant setup = cluster prep |

---

## benchmark-runtools (worktrees/benchmark-runtools)

**Purpose:** Run execution — harness setup/patching, endpoint wiring, client-side
monitoring (replica sampling, metrics scraping, controller log capture), workload
scenarios, results collection. Must obey shared-cluster safety rules.

| # | Hash | Message | Note |
|---|------|---------|------|
| 1 | `db198b50` | add BENCHMARK_ENDPOINT_URL passthrough to benchmark-run | Harness wiring |
| 2 | `eaccd3bb` | recognize .yaml.in in benchmark-run local-file check | Harness fix |
| 3 | `5eb585f8` | loop over suffixes instead of hardcoding | Harness fix |
| 4 | `65a922fa` | add scrape_wva_metrics.sh | Client-side monitor during run |
| 5 | `49135192` | wire scrape_wva_metrics.sh into benchmark-run | ⚠️ SPLIT: take Makefile hunks only; skip extract_real_trace.py hunks (those go to benchmark-extract) |
| 6 | `2c787a73` | recognize llm-d.ai/model label in sample_replicas.sh | Run-time replica sampler fix |
| 7 | `9de1fbd2` | move dhl-la-1708 model to small one | Env config |
| 8 | `a6129e28` | fix apostrophes in sample_replicas.sh | Run-time sampler fix |
| 9 | `77ea1b03` | persist BENCHMARK_ENDPOINT_URL for dhl-la-1708 | Env config |
| 10 | `14bf01de` | wire automated WVA controller-log capture into benchmark-run | Client-side monitor during run |
| 11 | `5fbf9fdb` | staged burst scenarios for scale-down/scale-up cycling | Workload scenarios |
| 12 | `04b70602` | add benchmark-run-only, closing run_only.sh metrics gap | Run execution variant |

---

## benchmark-extract (worktrees/benchmark-extract)

**Purpose:** Raw metrics → usable data. Operates on already-collected data only.
Parses controller logs, decision tables, k1/k2 trail, timeseries, WVA metrics
scrapes. Does NOT start any monitors or touch the cluster.

| # | Hash | Message | Note |
|---|------|---------|------|
| 1 | `777af8fe` | port real-trace extraction/rendering pipeline | Core extraction pipeline |
| 2 | `ad2be821` | add dump_wva_decision_table.py | Raw decisions → structured table |
| 3 | `3762d846` | extract saturation_v2 k1/k2 decision trail | k1/k2 extraction |
| 4 | `ed233f90` | capture applied_reason field | Field parsing fix |
| 5 | `49135192` | add scan_wva_metrics()/parse_wva_scrape() to extract_real_trace.py | ⚠️ SPLIT: take extract_real_trace.py hunks only; skip Makefile hunks (those go to benchmark-runtools) |
| 6 | `d6c4415b` | fix read_inference_perf() renamed field, out_tok always None | Extraction fix |
| 7 | `41ab5ab4` | investigate quick_smoke weak time anchor; fix extract_real_trace.py | Extraction fix |
| 8 | `d14d91c4` | fall back to Thanos when controller logs rotated away | Extraction resilience |
| 9 | `bf88a741` | fix dump_wva_full_timeseries.py scrape filename mismatch | Extraction fix |
| 10 | `08a7100b` | build analyze_wva_decisions.py, validated against real data | Decisions analysis tool |

---

## benchmark-viz (worktrees/benchmark-viz)

**Purpose:** Visualizations and reports. Panel rendering, publishing convention,
results artifacts. Consumes output from benchmark-extract.

| # | Hash | Message | Note |
|---|------|---------|------|
| 1 | `9bd35c04` | adopt autoscaling-viz results/ publishing convention | Publishing convention |
| 2 | `82049884` | fix panel 2/6 axis artifacts and panel 3 bar-width overlap | Panel fix |
| 3 | `827aa687` | fix panel 3/5 in-system line, p2 offset, p6 minor ticks | Panel fix |
| 4 | `e59a9f7d` | polish: p1a time axis, panel 3 dual-axis, panel 4 colorbar | Panel polish |
| 5 | `9d76ae38` | feat: panel 5 WVA supply/demand expressed as requests | New panel feature |
| 6 | `553ca42c` | polish: panel 3 legend, panel 4 amber KV gradation, panel 2 offset | Panel polish |
| 7 | `0d665587` | polish: panel 2 gap, panel 4 colour ramp, panel 5 demand label | Panel polish |
| 8 | `29a8616c` | docs+fix: demand-charge question, panel 4 header spacing | ⚠️ mixed — panel fix belongs here; doc hunk belongs in benchmark-plan. Accept as-is or split. |
| 9 | `4f840bcb` | title metadata, stage-boundary markers, wider x-axis | Panel feature |
| 10 | `0600eae7` | publish two multi-variant burst_4k1000 runs (results/ artifacts) | Published viz results |

---

## benchmark-plan (worktrees/benchmark-plan)

**Purpose:** Shared docs and discussions only. This is a detached tree — only
`plans/` exists at the root, no source files. All commits here are docs-only.

⚠️ Path conflict warning: the original commits write to `docs/plans/benchmark/`
but this tree has `plans/` at root. When cherry-picking, resolve conflicts by
placing files under `plans/<worktree-name>/` as already structured.

| # | Hash | Message | Source worktree |
|---|------|---------|-----------------|
| 1 | `29af241d` | docs: observability audit, in-flight-capture analysis, gaps | shared base |
| 2 | `793303b4` | docs: dump_k2_decisions.py discovery and consolidation plan | shared base |
| 3 | `df6f7d76` | docs: correct the deferred-k1/k2 framing | shared base |
| 4 | `8691ac09` | docs: publish quick_smoke viz result, modelID-drift finding | shared base |
| 5 | `5cd7cad2` | docs: record the modelID drift fix on dhl-la-1708 | shared base |
| 6 | `7616047e` | docs: publish the post-fix quick_smoke run with real WVA decisions | shared base |
| 7 | `7e7b3fa9` | docs: record the run_only.sh finding and its metrics-collection gap | shared base |
| 8 | `54c33a6e` | docs: durable copy of four sibling worktrees' TASK.md content | worktree-benchmark |
| 9 | `1190cbaa` | docs: correct EPP-unauthorized scope in anchor-fit finding | worktree-anchor-offset |
| 10 | `28988254` | docs: draft the run_only.sh metrics-gap design | worktree-run-only-gap |
| 11 | `1738485e` | docs: scope run-only collector to Collector A only | worktree-run-only-gap |
| 12 | `3e2c670c` | docs: record live-verification findings and handoff | worktree-run-only-gap |
| 13 | `1eb18d48` | docs: sub-task 1 done — secondary variant live on dhl-la-1708 | worktree-multi-variant |
| 14 | `d362472f` | docs: burst_4k1000 results, critical paused-variant finding | worktree-multi-variant |
| 15 | `e30fb91c` | docs: reorder plan doc log into chronological order | worktree-multi-variant |
| 16 | `bb20550b` | docs: draft issues to file for observability-gaps §4/§5/§6 | worktree-scaler-issues |

---

## Status

| Worktree | Branch | Path | Status |
|----------|--------|------|--------|
| bench-prepare | `benchmark-init` | `worktrees/benchmark-init` | ⬜ pending |
| benchmark-runtools | `benchmark-runtools` | `worktrees/benchmark-runtools` | ⬜ pending |
| benchmark-extract | `benchmark-extract` | `worktrees/benchmark-extract` | ⬜ pending |
| benchmark-viz | `benchmark-viz` | `worktrees/benchmark-viz` | ⬜ pending |
| benchmark-plan | `benchmark-plan` | `worktrees/benchmark-plan` | ⬜ pending |
