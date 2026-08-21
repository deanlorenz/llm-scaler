# Scaler-side issues to file (drafts, not yet posted)

Follow-through on `docs/plans/benchmark/observability-gaps.md` §4/§5/§6: that
doc recorded several findings about the scaler's *own* code (not this repo's
benchmark-tooling port) and explicitly deferred filing them. This doc turns
those findings into ready-to-post issue drafts.

**Status: drafts only. Nothing has been posted to GitHub.** Per instruction,
this session does not push branches or run `gh issue create` — that requires
explicit user sign-off, given separately, after the target repo below is
confirmed.

## Target repo

Confirmed by the user: the `upstream` remote —
`https://github.com/ev-shindin/llm-scaler` — is the reference repo, and its
default branch is now `main` (matches
[[reference_llm_scaler_repo_layout]]'s 2026-08-20 update: `feat/wva-external-scaler`
was promoted into `main` there).

**One open discrepancy to flag before filing draft D4 below**: item 4 is
about PRs `#1506`/`#1508`. Checked just now (read-only `gh pr view`) —
those PR numbers do **not** exist on `ev-shindin/llm-scaler` (its own PR
numbering only runs up to `#26`, all opened by `deanlorenz`). They exist on
a *different* repo, `llm-d/llm-d-workload-variant-autoscaler`
(`go.mod`'s module path, confirming this is the same codebase's upstream
home), both still `OPEN`, not merged:

- `#1506` — "fix: Inject trace_id/span_id into structured logs"
- `#1508` — "feat(observability): add OpenTelemetry tracing for the optimization pipeline"

So D4 as currently scoped can't cite those PR numbers meaningfully if filed
against `ev-shindin/llm-scaler` — worth a decision: file D4 against
`llm-d/llm-d-workload-variant-autoscaler` instead (where the PRs actually
live), or drop it from this batch and revisit once those PRs land and their
change reaches `ev-shindin/llm-scaler` some other way. Issues A/B/C don't
depend on this — they're about code that lives in this repo either way
(`internal/engines/...`, `deploy/lib/scaledobject.sh`).

## Draft A — `wva_desired_replicas`/`wva_current_replicas` gauges not cleared when a variant goes inactive; may be linked to a scrape-error spike

**Source**: `observability-gaps.md` §1 ("A real, live-observed metric-lifecycle
gap") and §4 items 1 and 3.

**Body draft**:

> `wva_desired_replicas`/`wva_current_replicas` (and likely other per-variant
> gauges) kept reporting `1`/`1` for the
> `optimized-baseline-nvidia-gpu-vllm-decode-wva` variant well after its
> `ScaledObject` was paused and the underlying Deployment was actually at `0`
> replicas — confirmed live, on a real cluster. The gauge is never cleared
> when a variant becomes inactive; the last-known value persists
> indefinitely.
>
> Separately, on the same cluster, `wva_errors_total{error_type="Failed to
> scrape pod"}` read **11738** (single scrape, not a rate) during a ~15-minute
> idle window — unexplained at the time.
>
> Filing these together on a hypothesis, not a confirmed root cause: a
> variant with no live pods (paused/scaled-to-zero) would produce exactly
> this pair of symptoms if the collector keeps trying to scrape/hold state
> for a target that no longer exists — repeated failed-scrape errors, and a
> gauge that never gets an update (so never gets cleared) because the thing
> that would clear it is itself gated on a successful scrape. If that's not
> the actual mechanism, please split this back into two issues — this is
> filed as one because the two symptoms line up suspiciously well, not
> because the collector code has been read to confirm it.
>
> Reported from a benchmark-tooling port (dashboard/analysis only); not
> something that repo can or should patch.

**Labels (suggested)**: `bug`, `observability`

---

## Draft B — no decision-log-equivalent structured line for scale-from-zero activation

**Source**: `observability-gaps.md` §1 and §4 item 2.

**Body draft**:

> The steadystate optimizer path emits two structured, decision-relevant log
> lines (`analyzer-result`, `scaling-decision`, via PR #1318) that a
> downstream consumer can parse to reconstruct *why* a scaling decision was
> made. The scale-from-zero path (`internal/engines/scalefromzero/engine.go`,
> triggered by KEDA activation through `internal/scaler`'s
> `StreamIsActive`/`IsActive`, not the periodic reconcile loop) has no
> equivalent: it logs `"Published scale-from-zero activation for Target
> Workload"` and `"Scale-from-zero decision written to cache"`, but neither
> carries the same kind of structured decision payload (demand/supply/reason)
> the steadystate lines do.
>
> Effect: cold-start / 0→1 activation behavior is invisible to any tooling
> built against the steadystate decision-log schema — there's no way to see
> *why* a scale-from-zero fired (or didn't) through the same pipeline used
> for steady-state scaling.
>
> Reported from a benchmark-tooling port; this is a gap in the scaler's own
> instrumentation, not something that port can add from outside.

**Labels (suggested)**: `enhancement`, `observability`

---

## Draft C — `ScaledObject.modelID` trigger can silently go stale after a manual model change, with no warning

**Source**: `observability-gaps.md` §4 item 5 and §5 (full incident writeup).

**Body draft**:

> Real incident on `dhl-la-1708` (2026-08-19, resolved 2026-08-20 — see below):
> a Deployment was hand-patched to serve a different model
> (`Qwen/Qwen3-32B` → `Qwen/Qwen3-0.6B`) without updating either the pod
> template's `llm-d.ai/model` label or the Deployment's `ScaledObject`
> (written once, at creation time, by `deploy/lib/scaledobject.sh`'s
> `llm-d.ai/created-by` annotation flow). Result: the WVA controller kept
> processing the *old* `modelID` for the entire run, matched it against
> nothing the deployment actually scrapes, and produced zero
> `analyzer-result`/`scaling-decision` lines (`"decisionsApplied": 0`)
> throughout a run with a real, visible load ramp that should have driven
> scaling. No warning, no error — just silent zero decisions.
>
> This specific instance is already resolved operationally (re-derived and
> adopted the correct `modelID` via `make scaledobjects-plan`/`-apply`,
> verified with `verify_wva_scaledobjects.sh`) — filing this issue for the
> underlying design gap, not the incident itself. Two independent things
> worth deciding, either or both:
>
> 1. Should `deploy/lib/scaledobject.sh` (or whatever standup flow re-points a
>    Deployment at a different model) re-derive `modelID` from the live
>    container instead of writing it once and never revisiting it?
> 2. Should the controller warn (metric, event, or log line) when a
>    `ScaledObject`'s `modelID` trigger has never matched any scraped metric
>    for some window — i.e. detect "this trigger is provably dead" instead of
>    computing zero decisions forever with no signal?
>
> Reported from a benchmark-tooling port that hit this while extracting a
> real run; the actual fix belongs with whoever owns the Deployment↔ScaledObject
> sync path.

**Labels (suggested)**: `bug`, `deploy`

---

## Draft D3 — `waitingQueueDemand`'s per-request KV charge: is `I + O` (vs. `I + 0.5*O`) the right planning size?

**Source**: `observability-gaps.md` §4 item 6 and §6 (full writeup).

**Body draft**:

> Not a bug report — a design question for whoever owns
> `internal/engines/analyzers/saturation_v2/analyzer.go`, recorded after a
> real run turned up a large, now-understood-but-unconfirmed-as-optimal gap.
>
> On a `decode_heavy` benchmark run, WVA's `demand` signal peaked at ≈3400
> request-equivalents while the run's own measured in-system concurrency
> peaked at only ≈1600 at roughly the same time — over 2x. Traced directly to
> `waitingQueueDemand`'s doc comment, which names three deliberate,
> over-provisioning-biased choices (not a bug — the comment states the
> reasoning: under-provisioning causes preemption/recompute thrash, which
> costs more than a spare replica):
>
> 1. Each request's KV footprint is charged at its *last* decode step — full
>    `I + O` (prompt + complete generation) — a peak/no-preemption planning
>    size, not the request's footprint at any real instant. Confirmed
>    numerically at this run's peak (t≈221s): 1549 requests actually active,
>    mean `in_tok=1000`, mean `out_tok`-so-far `=2438` of a 4000-token target
>    — real footprint ≈3438/request, charged as if every one would reach the
>    full 5000.
> 2. This term and a separate "resident" term are each independently
>    `max_over_time` over a 1-minute window, then summed — even though the
>    two maxima need not have occurred at the same real instant.
> 3. A queued (not-yet-running) request is priced into the total ahead of
>    time.
>
> The question, specifically about (1): would `I + 0.5*O` — roughly the
> request's *mean* footprint over its lifetime, rather than its peak — be a
> better planning size? This is recorded as an open question, not a proposed
> change: the benchmark port that found this has no ground truth to judge it
> against (no token-by-token KV occupancy trace exists for any captured run,
> only each request's final `in_tok`/`out_tok`). Whoever chose `I + O`
> originally is in a much better position to know why, and whether `0.5*O`
> trades away real headroom that's needed for a reason not visible from the
> outside.

**Labels (suggested)**: `question`, `saturation-v2`

---

## Not drafted as a standalone issue — deferred

**Item 4** (`observability-gaps.md` §4 item 4 / §1 last bullet): `#1506`/`#1508`
adding `trace_id`/`span_id` to every structured log line. Not filing this as
an issue in this batch — see the "Target repo" discrepancy above (the PRs
live on `llm-d/llm-d-workload-variant-autoscaler`, not `ev-shindin/llm-scaler`).
Once that's resolved one way or the other, and/or once those PRs actually
merge, this becomes a one-line "worth revisiting the decision table for
trace cross-referencing" issue or comment — not worth filing as a tracking
issue on its own yet.

## Next steps

1. User resolves the target-repo discrepancy for item 4 above (file D4 against
   `llm-d/llm-d-workload-variant-autoscaler` instead, or drop it from this
   batch — Drafts A/B/C are unaffected either way).
2. User reviews Drafts A/B/C (and D3, if kept) for accuracy and tone.
3. Only on explicit go-ahead: run `gh issue create --repo ev-shindin/llm-scaler
   --title "..." --body "..."` (or the other repo, for D4) for each approved
   draft.
4. Once filed, come back to this doc and to `observability-gaps.md` §4/§5/§6
   to link the issue numbers, then commit.
