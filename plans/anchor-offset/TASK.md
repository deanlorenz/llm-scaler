# Task: improve the time-anchor cross-correlation fit in extract_real_trace.py

## Context

This worktree branched off `worktree-benchmark` (all of that session's work — extraction
fixes, panel rendering fixes, the WVA supply/demand signal, the ScaledObject-drift gate — is
already here). Read `docs/plans/benchmark/observability-gaps.md` in full first; it's the running
record of what's been found and fixed in this benchmark port.

## The problem

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

## What to do

1. Find and read the anchor-fitting function in full. Understand exactly what signal it
   correlates against what (the code comment mentions `signal: 'run+wait'`).
2. Figure out why quick_smoke's fit came back weak — is the search range too narrow, the signal
   choice suboptimal, insufficient scrape samples (`n_scrapes` — check its value), or something
   else? Use the real bundle data above to investigate, not synthetic data.
3. If you find a concrete improvement, implement it, re-run extraction against both bundles above
   (`python3 hack/benchmark/extract_real_trace.py --run <run-dir> --controller-log <log>` — check
   `provenance.json` in each results dir for the exact source run directory used), and confirm
   `corr` improves and the run-vs-in_system disagreement rate drops.
4. If no real fix exists (e.g. the underlying data genuinely doesn't support a tighter fit), say so
   plainly and record why in `observability-gaps.md` rather than forcing a change that doesn't
   actually help.

## Out of scope here

- Don't touch `render_real_trace.py`'s panel code — that's already fixed this session (panel
  2/3/4/5/6 axis and alignment fixes, all committed). This task is extraction-side only.
- Don't touch the cluster. This is pure offline analysis against already-captured bundles.

## When done

Commit on this branch (`worktree-anchor-offset`), update `observability-gaps.md` with the
finding, and let the user know so it can be merged back into `worktree-benchmark` (or pushed to
`origin` directly — `origin` is the user's own fork, `deanlorenz/llm-scaler`; never push to
`upstream`, which is push-disabled anyway).
