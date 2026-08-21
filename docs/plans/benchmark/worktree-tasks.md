# Sibling worktree tasks (2026-08-20)

Four sibling worktrees were created off `worktree-benchmark` at commit `7e7b3fa9`, each on its own
branch, each with a `TASK.md` at its root, to divide follow-up work from this benchmark-tooling
session so it could run in parallel across separate sessions:

| Directory (under `.claude/worktrees/`) | Branch | Task |
|---|---|---|
| `anchor-offset` | `worktree-anchor-offset` | Improve the time-anchor cross-correlation fit |
| `run-only-gap` | `worktree-run-only-gap` | Resolve the `collect_metrics.sh` gap, finish the standalone run-only plan |
| `scaler-issues` | `worktree-scaler-issues` | Draft (and, with sign-off, file) the accumulated scaler-side GitHub issues |
| `multi-variant` | `worktree-multi-variant` | Design and run a real multi-variant scenario |

This doc is the durable copy of each `TASK.md`'s content — the originals are uncommitted local
files in worktrees this session cannot run `git commit` against (worktree isolation blocks git
operations targeting another worktree, even via `-C`), so this copy, committed here, is what
survives if any of those local files are ever lost. If a sibling worktree's own `TASK.md` and this
copy ever disagree, the sibling's own file is authoritative for whatever work it already started
against — update this doc to match, don't silently prefer this copy.

---

## Task: anchor-offset — improve the time-anchor cross-correlation fit in extract_real_trace.py

### Context

This worktree branched off `worktree-benchmark` (all of that session's work — extraction
fixes, panel rendering fixes, the WVA supply/demand signal, the ScaledObject-drift gate — is
already here). Read `docs/plans/benchmark/observability-gaps.md` in full first; it's the running
record of what's been found and fixed in this benchmark port.

### The problem

`hack/benchmark/extract_real_trace.py` anchors `inference-perf`'s per-request timestamps (which
arrive on a *monotonic* clock, not epoch) onto real wall-clock time via a cross-correlation fit —
look for the function that builds `meta['time_anchor']` (fields: `offset`, `corr`, `method`,
`guess`, `shift_from_guess_s`, `signal`, `n_scrapes`, `over_l_samples`, `over_l_frac`,
`over_l_worst_rel`, `trustworthy`).

On the `quick_smoke` run captured this session
(`hack/benchmark/results/20260820-real-decisions/bundle.json`), this fit came back weak:
`corr=0.92`, a `9.0s` shift from its own initial guess, `trustworthy: False`. That weakness has a
real, visible consequence: when the panel-3/5 rendering fixes (already done, this session) put the
request-derived "in system" line on the same x-axis as the pod-metric-derived "running" bars, the
two visibly disagree at some points (e.g. at `t=163s`, pod gauge shows `run=19.0` while the
anchored request-derived `in_system=7` at the same tick — running should never exceed in-system).
Confirmed this isn't a rendering bug — it's the anchor's own imprecision showing through once the
two series are finally compared point-for-point instead of on unrelated timelines.

For contrast: `hack/benchmark/results/20260820-decode-heavy/bundle.json` (a `guidellm` run) needs
no anchor correction at all (`time_anchor: {offset: 0.0, method: 'not-needed'}` — guidellm reports
epoch time directly) and shows far fewer of these run-vs-in_system disagreements (3 out of 55
ticks, vs quick_smoke's much higher rate) — so there's likely a smaller, structural
measurement-cadence mismatch even with a perfect anchor, on top of a larger anchor-driven
component specific to weak-anchor runs like quick_smoke.

### What to do

1. Find and read the anchor-fitting function in full. Understand exactly what signal it
   correlates against what (the code comment mentions `signal: 'run+wait'`).
2. Figure out why quick_smoke's fit came back weak — is the search range too narrow, the signal
   choice suboptimal, insufficient scrape samples (`n_scrapes` — check its value), or something
   else? Use the real bundle data above to investigate, not synthetic data. The raw run inputs
   needed to re-extract (per-request JSON, metrics/raw scrapes, controller.log) were copied
   directly into this worktree's root, at the same relative paths they had in `benchmark`:
   `dean-20260820-143619-338/results/inference-perf-1787225821-3dl4q4_1/` (quick_smoke) and
   `dean-20260820-152419-492/results/guidellm-1787228725-a32uh3_1/` (decode_heavy), plus
   `wva-controller-run3.log` at the worktree root for quick_smoke's controller log (that run
   predates the automated capture, so it needs `--controller-log` explicitly).
3. If you find a concrete improvement, implement it, re-run extraction against both bundles above
   (`python3 hack/benchmark/extract_real_trace.py --run <run-dir> --controller-log <log>`), and
   confirm `corr` improves and the run-vs-in_system disagreement rate drops.
4. If no real fix exists (e.g. the underlying data genuinely doesn't support a tighter fit), say so
   plainly and record why in `observability-gaps.md` rather than forcing a change that doesn't
   actually help.

### Out of scope here

- Don't touch `render_real_trace.py`'s panel code — that's already fixed this session (panel
  2/3/4/5/6 axis and alignment fixes, all committed). This task is extraction-side only.
- Don't touch the cluster. This is pure offline analysis against already-captured bundles.

### When done

Commit on this branch (`worktree-anchor-offset`), update `observability-gaps.md` with the
finding, and let the user know so it can be merged back into `worktree-benchmark` (or pushed to
`origin` directly — `origin` is the user's own fork, `deanlorenz/llm-scaler`; never push to
`upstream`, which is push-disabled anyway).

---

## Task: run-only-gap — resolve the collect_metrics.sh gap in the paused standalone-install plan

### Context

This worktree branched off `worktree-benchmark`. Read `docs/plans/benchmark/standalone-cli-install.md`
in full, especially the "Update 2026-08-20" section at the end — that is the authoritative,
already-verified record of everything found so far. Do not re-derive it; it's the result of two
rounds of research this session, read directly against the `llm-d-benchmark` clone.

### Where things stand

- The original goal was a standalone (non-editable pip install) `llmdbenchmark` CLI, decoupled
  from the persistent clone this repo keeps for `benchmark-standup`. That premise is **disproved**:
  `ExecutionContext.base_dir` is never wired up from `--base-dir` in any of `cli.py`'s dispatch
  sites, so `run` mode always falls back to a `__file__`-relative path that only survives an
  *editable* install by coincidence. Do not revisit this path.
- A much better fit exists: `llm-d-benchmark/existing_stack/run_only.sh` + a sibling
  `config_template.yaml` — pure bash, no Python CLI, no venv, no template tree. Read both files in
  full (they're short — either re-clone `llm-d-benchmark` at `v0.7.8`, or read the doc's own long
  quotes from them). `dhl-la-1708` already has a PVC literally named `workload-pvc` (the
  template's own default) and `yq` (mikefarah flavor, v4.53.2) is already installed system-wide —
  both already confirmed, no need to re-check.
- The one real, unresolved gap: `run_only.sh`'s bare pod spec never runs `collect_metrics.sh`
  (`workload/harnesses/collect_metrics.sh` in the clone), the in-pod, kubeconfig-injected script
  the full CLI path uses to scrape vLLM/EPP `/metrics` into `metrics/raw/*_metrics.log` — the files
  `hack/benchmark/extract_real_trace.py` depends on for panel 3 (running/waiting bars), panel 4
  (KV% heatmap), and most of panel 5. Without it, a `run_only.sh`-driven run produces real
  per-request output but no pod-level metrics at all.

### What to do

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

### Cluster safety — read before running anything real

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

### When done

Commit on this branch (`worktree-run-only-gap`), update `standalone-cli-install.md` with the
outcome (resolved gap, what shipped, what's still open), and let the user know.

---

## Task: scaler-issues — file the accumulated scaler-side findings as real issues

### Context

This worktree branched off `worktree-benchmark`. Over the course of this benchmark-tooling
session, several real findings about the scaler's own code (not this repo's benchmark tooling)
were recorded in `docs/plans/benchmark/observability-gaps.md` instead of being fixed here — that
doc's own stated scope is "instrumenting the scaler's own code... is out of scope for this port;
that's recorded... as a gap for a future issue, not patched from here." This task is that
follow-through: turn the recorded findings into real, filed issues, so they're visible to whoever
owns that code instead of sitting in a local markdown file.

### The findings to file (read each in full in the doc before drafting an issue — don't paraphrase
from this summary alone, it's compressed)

From `observability-gaps.md` §4 ("Summary: what to open issues for"):
1. `wva_desired_replicas`/`wva_current_replicas` (and likely other per-variant gauges) are not
   cleared when a variant becomes inactive — stale last-known values persist indefinitely.
   Confirmed live.
2. No decision-log-equivalent structured line for scale-from-zero activation (only for the
   steadystate optimizer path) — makes cold-start/0→1 behavior unobservable through the same
   pipeline as steady-state scaling.
3. `wva_errors_total{error_type="Failed to scrape pod"}` was very high (11738) during an idle
   window on a real cluster — unexplained.
4. (Heads-up, not a bug) PR #1506/#1508 will add `trace_id`/`span_id` to every structured log line
   once merged — worth revisiting the decision table to include them once that lands. Check
   whether those PRs have merged since this was written before filing anything for this one.
5. A manual model change on a Deployment leaves the ScaledObject's `modelID` trigger stale, and
   WVA silently computes zero decisions forever with no warning — full incident writeup in §5.
   Whoever owns `deploy/lib/scaledobject.sh` should decide whether it should re-derive `modelID`
   from the live Deployment instead of being handed a value once at creation time, and/or whether
   the controller should warn when a trigger never matches any scraped metric.
6. `waitingQueueDemand`'s per-request KV charge uses the full `I + O` (prompt + complete
   generation) as a "last decode step" planning size — full writeup in §6, including Dean's own
   follow-up question (`I + 0.5*O` might be a better planning size, but this port has no ground
   truth to judge that against).

### What to do

1. Read `docs/plans/benchmark/observability-gaps.md` §4, §5, and §6 in full.
2. Draft an issue for each (title + body, referencing exact file/line where relevant — e.g.
   `internal/engines/analyzers/saturation_v2/analyzer.go`'s `waitingQueueDemand` for item 6,
   `deploy/lib/scaledobject.sh` for item 5). Some may be worth combining (e.g. items 1 and 3 are
   both about the metrics/gauge collector) — use judgment, don't mechanically file six issues if
   two of them are really one finding.
3. **Confirm the target repo before filing anything.** This session's own memory
   (`reference_llm_scaler_repo_layout.md`) has the exact origin/upstream layout — check it, and
   confirm with the user which repo issues should land in (likely `ev-shindin/llm-scaler`, the
   `upstream` remote, since that's where the scaler code under discussion actually lives — but
   confirm, don't assume).
4. **Filing an issue on someone else's repo is a visible action — get explicit sign-off from the
   user before actually running `gh issue create`.** Preparing the drafts is fine to do freely;
   publishing them is not something to do unprompted.

### Out of scope here

- No code changes in this repo. This is a documentation/communication task.
- Don't touch the cluster.

### When done

Once the user has signed off and the issues are filed, update `observability-gaps.md` §4/§5/§6 to
link the filed issue numbers, commit on this branch (`worktree-scaler-issues`), and let the user
know.

---

## Task: multi-variant — a multi-variant run, to exercise the coverage checks single-variant runs can't

### Context

This worktree branched off `worktree-benchmark`. Every run captured so far this session
(`quick_smoke` via `inference-perf`, `decode_heavy` via `guidellm` — both published under
`hack/benchmark/results/`) is single-variant, against `dhl-la-1708`'s one existing decode
deployment. Several of `extract_real_trace.py`'s own coverage checks stay "not supported by this
run" on both — some because these particular scenarios never generated enough load (Calibrate A,
Exercise the 0.85 ceiling), some structurally because they need more than one variant to mean
anything (router imbalance, a real two-variant efficiency comparison).

### What to do

1. Read `docs/developer-guide/two-variant-wva-benchmark.md` — this repo already documents a
   two-variant efficiency-aware benchmark design. Understand what it's asking for before planning
   a run.
2. Read `docs/plans/benchmark/observability-gaps.md` §3 — `analyze_wva_decisions.py`'s design is
   sketched but not built, explicitly because "no multi-variant run exists yet to validate
   against." A real multi-variant run is what unblocks actually building that tool, not just
   exercising more coverage checks.
3. Check `dhl-la-1708` for whether a second variant/deployment can be stood up within this
   namespace's existing scope (namespace-scoped only — this account has cluster-admin power that
   must never be used; see `deploy/lib/scaledobject.sh`'s own namespace-scoping and this session's
   own standing rule against touching anything cluster-scoped).
4. Design and run a real multi-variant scenario. Extract, render, and publish it the same way
   `hack/benchmark/publish_viz_result.sh` already does for the two runs in `hack/benchmark/results/`.
5. Once real multi-variant data exists, revisit whether `analyze_wva_decisions.py`'s sketched
   design in §3 of the doc actually holds up against it, and consider building it.

### Cluster safety — read before running anything real

This task drives real load against the shared `dhl-la-1708` cluster and holds real GPUs. Standing
rules from this session, all still in force:
- Run `make benchmark-verify-scaledobjects BENCHMARK_NAMESPACE=dhl-la-1708 BENCHMARK_REPORT_ONLY=true`
  before any real run — it's also wired as a hard gate into `benchmark-run` automatically.
- Never patch the llm-d Deployment directly, never hand-patch a ScaledObject trigger — see
  `deploy/lib/scaledobject.sh` for the sanctioned path (`scaledobjects-plan`/`scaledobjects-apply`)
  if a config drift needs fixing.
- Be conservative with GPUs — there is a sweep agent on this cluster that reaps idle GPUs, and
  other users share it. Park the namespace (`make so-park SO=<name> ...`) as soon as you're done,
  the same way every other run this session ended, and verify the decode pod actually terminated
  before considering the run "closed out."
- **Coordinate with the user before starting** — this is explicitly the one task in this
  "divide work across worktrees" split that needs to run alone against the cluster; check that no
  other worktree/session (e.g. `worktree-run-only-gap`, if it's also mid-verification) is driving
  load against `dhl-la-1708` at the same time.

### When done

Commit on this branch (`worktree-multi-variant`), publish the run's viz result the same way prior
runs were published this session, update `observability-gaps.md` with what the multi-variant run
did or didn't reveal, and let the user know.
