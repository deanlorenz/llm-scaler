# Task: file the accumulated scaler-side findings as real issues

## Context

This worktree branched off `worktree-benchmark`. Over the course of this benchmark-tooling
session, several real findings about the scaler's own code (not this repo's benchmark tooling)
were recorded in `docs/plans/benchmark/observability-gaps.md` instead of being fixed here — that
doc's own stated scope is "instrumenting the scaler's own code... is out of scope for this port;
that's recorded... as a gap for a future issue, not patched from here." This task is that
follow-through: turn the recorded findings into real, filed issues, so they're visible to whoever
owns that code instead of sitting in a local markdown file.

## The findings to file (read each in full in the doc before drafting an issue — don't paraphrase
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

## What to do

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

## Out of scope here

- No code changes in this repo. This is a documentation/communication task.
- Don't touch the cluster.

## When done

Once the user has signed off and the issues are filed, update `observability-gaps.md` §4/§5/§6 to
link the filed issue numbers, commit on this branch (`worktree-scaler-issues`), and let the user
know.
