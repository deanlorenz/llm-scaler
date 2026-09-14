# task-coder-composite-redesign

## Orientation

- **What:** rewrite `buildComposite` and its helpers to the design in
  `.session/composite-signal-redesign.md` §2 (the ONLY doc section you need — do not read the
  rest of that doc, and do not read `spec.md`; both are addressed to the mission owner, not you).
- **Worktree:** this worktree (`worktrees/composite-analyzer`, branch `composite-analyzer`).
  Startup check: confirm `git rev-parse --show-toplevel` resolves here and
  `git branch --show-current` reports `composite-analyzer`. Stop and report if not.
- **Role / scope:** coder. Implement exactly the checklist below, in order. Every name, every
  signature, every file destination is decided in this task file — if you find yourself
  choosing between two options, stop and ask **the mission owner** (see "If something in this
  task file is wrong or ambiguous" below for exactly how); do not pick one yourself, and do not
  escalate a coding/design question to the human user directly (no `agentbus_ask_user`, no
  `user.in` question) — the mission owner is the one who decides whether a question needs the
  user at all.
- **Do not:** touch `internal/engines/allocation/multi_backup/`; push or open a PR; change any
  formula (§2.7); read `spec.md` or `composite-signal-redesign.md` beyond its §2.
- **Agentbus channels** (per `conventions/agentbus.md` — subscribe to `In:` before starting any
  work, and stay subscribed until you exit):
  - **In:** `composite-analyzer.coder-redesign.in`
  - **Out:** `composite-analyzer.coder-redesign`
  - **Announce:** `mission.composite-analyzer`
  - Subscribe with:
    `agentbus_subscribe(topic="composite-analyzer.coder-redesign.in", session_id="coder-redesign")`
  - Announce presence on startup:
    `agentbus_publish(topic="mission.composite-analyzer", from_session="coder-redesign", kind="announce", body="session=coder-redesign role=coder online. in=composite-analyzer.coder-redesign.in out=composite-analyzer.coder-redesign")`
  - Publish status, findings, questions, and your final result to `Out:`. If the mission owner
    asks anything on `In:`, answer on `Out:` before continuing. Publish the final result to
    `Out:` before exiting.
- **Progress reporting to the user, in addition to your normal `Out:` channel:** after
  completing each numbered step below, publish a short status note directly to the human user:
  `agentbus_publish(topic="user.in", from_session="<your slug>", kind="note", body="step N done: <one line>")`.
  This is non-blocking, additive, and does not replace anything you already report on `Out:` —
  per `conventions/agentbus.md`'s "Status & Progress Notifications (user.in)". Also publish one
  on genuinely blocked/stuck, and one final note on completion or on stopping to ask a question.

## Files this task touches

| File | What happens to it |
|---|---|
| `internal/engines/steadystate/composite.go` | `buildComposite` rewritten. Gains the inlined PRC computation, `AggN`'s replacement, the new logging (step 9), the Ready-vs-serving comment (step 10). Loses its private `roleOfVC`/`RoleOfVC` (moved to `domain`, step 4). |
| `internal/engines/allocation/composite_decision.go` | `ResolveSO` and `SODecision` **deleted** (step 3) — step 5's inline loop in `composite.go` fully absorbs their logic; there is no rewritten replacement in this file. `TotalReplicas` added (step 3). Decision-path constants become a typed enum (step 7). |
| `internal/engines/allocation/composite_eligibility.go` (+ its test file) | `eligible` exported as `Eligible` — pure rename, no behavior change (step 5). |
| `internal/domain/role.go` (new file, or an existing small `domain` file — your choice) | `RoleOfVC` added — the one shared role-canonicalization function, moved here (not `steadystate`, to avoid an import cycle — see step 4). |
| `internal/engines/aggregation/replicas_needed.go` | Deleted. Its logic moves into `composite_decision.go` (see step 3). |
| `internal/engines/aggregation/prc_com.go` | Deleted. Its logic is inlined into `composite.go` (see step 5). |
| `internal/engines/allocation/composite_signal_gate.go` | `HasUsableCompositeSignal` replaced by two functions (see step 8). |
| `internal/engines/steadystate/engine_v2.go` | One new line before the `buildComposite` call (step 1); one new log call (step 9); one call site updated (step 8). |
| `internal/engines/allocation/engine.go` | One call site updated (step 8). |

## Step 1 — resolve `eligibleAnalyzers` before compose; use policy default thresholds

In `engine_v2.go`, the existing code (currently `:817-818`) is:
```go
satUp, satDown := config.AnalyzerThresholds(domain.SaturationAnalyzerName)
composite := buildComposite(ctx, namedResults, satUp, satDown)
```
This names `domain.SaturationAnalyzerName` at the call site, outside `buildComposite` — the
composite must use the policy's own default thresholds, never an analyzer-specific override,
and never name any analyzer at this call site (**correction, 2026-09-14**: an earlier version
of this task file kept the `satUp`/`satDown` line as-is; that was wrong — no sat-specific code
belongs outside `buildComposite`). Delete the `AnalyzerThresholds` line and its "Saturation's
own thresholds are the composite's too" comment above it entirely (check first that `satUp`/
`satDown` have no other use in this function — if they do, keep the line for that other use but
still stop passing them into `buildComposite`). Replace with:

```go
eligibleAnalyzers := namedResults
if !config.AnalyzerEnabled(domain.SaturationAnalyzerName) {
    eligibleAnalyzers = excludeByName(namedResults, domain.SaturationAnalyzerName)
}
composite := buildComposite(ctx, namedResults, eligibleAnalyzers, config.ScaleUpThreshold, config.ScaleDownBoundary)
```

`config` is already a parameter of the enclosing function (`collectV2ModelRequest`,
`config config.ScalingPolicy`, `engine_v2.go:781`) — no new parameter needed on that function.

Write `excludeByName(results []allocation.NamedAnalyzerResult, name string) []allocation.NamedAnalyzerResult` as a small private helper in `engine_v2.go`, next to `buildComposite`'s call site. It returns a new slice omitting the entry whose `Name == name`; if no such entry exists, returns the input unchanged.

## Step 2 — `buildComposite`'s new signature

Change `buildComposite`'s signature in `composite.go` from:
```go
func buildComposite(ctx context.Context, namedResults []allocation.NamedAnalyzerResult, scaleUp, scaleDown float64) allocation.NamedAnalyzerResult
```
to:
```go
func buildComposite(ctx context.Context, namedResults, eligibleAnalyzers []allocation.NamedAnalyzerResult, scaleUp, scaleDown float64) allocation.NamedAnalyzerResult
```
`namedResults` is still used to find sat (identity source, step 4) and for `maxScore` (unchanged). `eligibleAnalyzers` is the new parameter, used only in step 5's contributor collection.

## Step 3 — `TotalReplicas`, in `composite_decision.go`; delete `ResolveSO`/`SODecision`

Delete `internal/engines/aggregation/replicas_needed.go` entirely (`AggN`, `replicasNeeded`, `variantCapacity`, `roleOf` — all four functions, no trace kept).

Also delete `ResolveSO` and `SODecision` from `composite_decision.go`. Their only non-test
caller today (`composite.go:57`) is the exact call site step 5's inline contributor loop
replaces; step 5's `switch` fully absorbs `ResolveSO`'s decision logic. There is no rewritten
version of either — do not keep them as a parallel/unused implementation. Port
`composite_decision_test.go`'s existing `ResolveSO`/`SODecision` cases to exercise the new
inline logic instead (via `TotalReplicas` plus a small test-only helper that runs step 5's
`switch` logic if you need to test the switch in isolation from `buildComposite`) — do not
drop coverage silently.

Add to `composite_decision.go`:

```go
// TotalReplicas returns D_i[role]/PRC_i(SO) — analyzer i's own measured demand
// for this SO's role, divided by analyzer i's own measured PRC for this SO.
// Both values are analyzer i's own data; never sat's, never the composite's.
// ok is false when vc.PerReplicaCapacity <= 0 or the analyzer has no demand
// entry for the role at all.
func TotalReplicas(result *domain.AnalyzerResult, vc domain.VariantCapacity) (n float64, ok bool)
```

Signature note: takes `vc domain.VariantCapacity` (the already-resolved value), not
`(result, variantName string)` — the caller (step 5) already has `vc` in hand from iterating
sat's own list; do not make this function search for it again.

Body: port `replicasNeeded`'s existing logic from the deleted file (division, the
`PerReplicaCapacity <= 0` guard, the "role's demand not present at all" guard), adapted to take
`vc` directly instead of looking it up via `variantCapacity(result, variant)`. Use
`aggregation.DemandForRole(result, domain.RoleOfVC(vc))` for the demand lookup (see step 4 for
where `RoleOfVC` now lives) — `DemandForRole` stays in the `aggregation` package (still 3
callers after this change: `prc_com.go`'s callers plus this one — recount to confirm before
assuming, but do not move it without confirming first).

## Step 4 — one shared role-canonicalization function, in `domain`

Today there are three copies of the same logic: `aggregation.roleOf` (private,
`replicas_needed.go` — being deleted in step 3), `steadystate.roleOfVC` (private,
`composite.go:248`), and an inline duplicate inside `aggregation.AggregateByRole`
(`aggregation.go:117-119`).

**Do NOT place the unified function in `steadystate`** — `steadystate` already imports
`allocation` (three files: `composite.go`, `engine_v2.go`, `engine.go`), and step 3's
`TotalReplicas` lives in `allocation` and needs to call this function too. `allocation`
importing `steadystate` would be a compile-time import cycle.

Keep exactly one: add `RoleOfVC(vc domain.VariantCapacity) string` to the `domain` package
(new file `internal/domain/role.go`, or add to an existing small file in `domain` if one fits
better — your choice, this is a same-package file-organization detail, not a placement
decision). Body: canonicalize an empty `vc.Role` to `domain.RoleBoth`, otherwise return
`vc.Role` unchanged (port the existing `roleOfVC`/`roleOf` logic verbatim — no behavior
change). Delete `aggregation.roleOf` (goes away with step 3's file deletion) and
`steadystate.roleOfVC`/`RoleOfVC` from `composite.go` (replaced by `domain.RoleOfVC` — update
`composite.go`'s call sites, step 5's `roleOfVC(vc)` becomes `domain.RoleOfVC(vc)`). Leave
`AggregateByRole`'s inline copy as-is — it is a 2-line loop body, not worth a cross-package
call for. Anywhere else that needs role canonicalization (step 3's `TotalReplicas`, step 5's
PRC line, step 6's `demandByRole` construction) calls `domain.RoleOfVC`.

## Step 5 — `CompositeTotalReplicas` and inlined PRC, in `composite.go`

**First, export `eligible` as `Eligible`** in `internal/engines/allocation/composite_eligibility.go`
(rename only — capitalize the func name, keep its body and doc comment unchanged) so
`steadystate.buildComposite` can call it as `allocation.Eligible(e)`. Update
`composite_eligibility_test.go`'s calls from `eligible(nr)` to `Eligible(nr)` (still same-package,
still exercises the same logic — this is a pure rename, not a behavior change). This gate is
NOT new: it is today's existing `ResolveSO`/`eligible()` pairing, carried forward unchanged so a
stale/uninformative/nil-Result analyzer still cannot contribute — see the contributor loop below.

Inside `buildComposite`'s per-SO loop (iterating `sat.Result.VariantCapacities` — this part is
unchanged from today's code):

```go
type contribution struct {
    name string
    n    float64
}
var contributors []contribution
for _, e := range eligibleAnalyzers {
    if !allocation.Eligible(e) {
        // Stale, uninformative, or nil-Result analyzers contribute to nothing —
        // unchanged from today's ResolveSO/eligible() pairing (redesign §2.1(b)(i)).
        // This is a per-analyzer gate, checked once per analyzer per SO here; it is
        // NOT the same thing as the per-SO Reason check below, which is per-contribution.
        continue
    }
    evc, present := findVariantCapacity(e.Result, vc.VariantName)  // new tiny helper, see below
    if !present || evc.Reason == allocation.ReasonNoData || evc.Reason == allocation.ReasonError {
        continue
    }
    n, ok := allocation.TotalReplicas(e.Result, evc)
    if !ok {
        continue
    }
    contributors = append(contributors, contribution{name: e.Name, n: n})
}

var compositeTotalReplicas float64
var decisionPath allocation.DecisionPath  // step 8's new type
var contributorNames []string
switch {
case len(contributors) == 0:
    // sat-fallback: only reachable when sat was excluded from eligibleAnalyzers
    // upstream (step 1) for being disabled, AND no other analyzer contributed.
    satVC, present := findVariantCapacity(sat.Result, vc.VariantName)
    if satN, ok := allocation.TotalReplicas(sat.Result, satVC); present && ok {
        compositeTotalReplicas, decisionPath, contributorNames = satN, allocation.DecisionSatFallback, []string{sat.Name}
    } else {
        decisionPath = allocation.DecisionNoSignal
    }
case len(contributors) == 1:
    compositeTotalReplicas, decisionPath, contributorNames = contributors[0].n, allocation.DecisionSingle, []string{contributors[0].name}
default:
    decisionPath = allocation.DecisionAgree
    for _, c := range contributors {
        contributorNames = append(contributorNames, c.name)
        if c.n > compositeTotalReplicas {
            compositeTotalReplicas = c.n
        }
    }
}
```

`findVariantCapacity(result *domain.AnalyzerResult, variantName string) (domain.VariantCapacity, bool)` — small private helper in `composite.go`, searches `result.VariantCapacities` by name. This is the one place a by-name search remains (looking up a NON-sat analyzer's view of the SO sat is currently iterating) — it is not the same case step 3 removed (that was searching for the SAME analyzer's own SO, redundantly, when the caller already had it).

Then, still inside the loop, PRC (do NOT write a separate `PRCCom`-equivalent function — this is the entire computation, inlined):

```go
if compositeTotalReplicas > 0 {
    vc.PerReplicaCapacity = demandByRole[domain.RoleOfVC(vc)] / compositeTotalReplicas
} else if sourceVC != nil {
    vc.PerReplicaCapacity = sourceVC.PerReplicaCapacity  // existing fallback, unchanged
}
```

`demandByRole` is computed ONCE, before the per-SO loop starts (this is step 6 — see below),
not recomputed per SO.

## Step 6 — PRC's per-role demand lookup, once

Before the per-SO loop in `buildComposite`, add:
```go
demandByRole := make(map[string]float64)
for _, role := range rolesPresent(sat.Result.VariantCapacities) {  // existing helper or write one
    d, _ := aggregation.DemandForRole(sat.Result, role)
    demandByRole[role] = d
}
```
Then step 5's PRC line reads `demandByRole[domain.RoleOfVC(vc)]` instead of calling
`aggregation.DemandForRole` fresh for every SO. If no existing helper enumerates the distinct
roles in a `[]VariantCapacity`, write `rolesPresent` as a small private helper in `composite.go`
(dedupe by `domain.RoleOfVC`).

## Step 7 — decision-path type

In `composite_decision.go`, replace the existing untyped `string` constants:

```go
// DecisionPath categorizes how one SO's CompositeTotalReplicas was reached.
type DecisionPath string

const (
    DecisionAgree       DecisionPath = "C0-agree"
    DecisionSingle      DecisionPath = "C1-single"
    DecisionSatFallback DecisionPath = "C2-sat-fallback"
    DecisionNoSignal    DecisionPath = "C4-no-signal"
)
```
(Mirrors `domain.DecisionReason`'s existing pattern, `saturation_analyzer.go:12` — same
`type X string` + `const` shape, nothing new invented.)

`domain.VariantCapacity.Reason` stays `string` (unrelated field, do not change).

## Step 8 — split `HasUsableCompositeSignal`

Delete `HasUsableCompositeSignal` from `composite_signal_gate.go`. Replace with two functions
in the same file:

```go
// SOHasSignal reports whether variant's own decision path is not DecisionNoSignal.
func SOHasSignal(composite NamedAnalyzerResult, variant string) bool

// CompositeHasSignal reports whether composite carries a Result with at least
// one SO whose decision path is not DecisionNoSignal.
func CompositeHasSignal(composite NamedAnalyzerResult) bool
```

Update call sites:
- `engine.go:1091` — currently `HasUsableCompositeSignal(req.CompositeSignal)`. This call has
  no specific SO in scope (it's a whole-request check) → use `CompositeHasSignal`.
- `engine_v2.go:735` (via `hasSaturationResult`) — read the surrounding code first: if this
  call site is checking "does this model have ANY usable signal at all" (no specific SO) →
  `CompositeHasSignal`; if it's checking one particular SO's signal → `SOHasSignal`. Do not
  guess; read the call site's actual variable scope to tell which.

## Step 9 — composition-level logging

In `composite.go`'s per-SO loop, after step 5's contributor collection, add one log call per
SO:

```go
ctrl.LoggerFrom(ctx).Info("composite-contributors",
    "modelID", sat.Result.ModelID, "namespace", sat.Result.Namespace,
    "variant", vc.VariantName, "decisionPath", decisionPath,
    "contributors", contributorNames,
    "totalReplicas", compositeTotalReplicas,
)
```
This is additive — it does not replace `engine_v2.go`'s existing `logAnalyzerResult` call for
the composite (unchanged, still fires separately).

## Step 10 — code comment on the Ready-vs-serving gap

In `composite.go`, at the line copying `vc.ReplicaCount = sourceVC.ReplicaCount` (step "copy
from sat, unconditional" — today's existing line), add:
```go
// ReplicaCount here is sat's raw k8s ready count, not a count of replicas
// verified to be usefully serving. Accepted for now; Supply/AnticipatedSupply
// (buildCapacities) are built from this value as-is.
```

## Verification

- `make test` passes.
- Existing tests updated to match (do not leave a test file testing a deleted function):
  `composite_test.go`, `composite_observability_test.go`, `composite_decision_test.go`,
  `composite_eligibility_test.go`, `composite_signal_gate_test.go` — this one needs new tests
  for both `SOHasSignal` and `CompositeHasSignal`, since it tested one function that no longer
  exists.
- `replicas_needed_test.go`, `prc_com_test.go` — delete (their functions are deleted); port any
  test case that exercises behavior not otherwise covered into `composite_decision_test.go`
  (for `TotalReplicas`) — do not just delete test coverage silently.
- **Regression guard, both cases required:** composite PRC for an SO equals sat's own PRC
  exactly when sat is sole contributor — test this under decision path `DecisionSingle` (sat
  enabled, no other contributor) AND under `DecisionSatFallback` (sat disabled, no other
  contributor). These are two different code paths after this rewrite (step 5's `switch`) and
  both must hold.
- `maxScore` untouched — confirm no test relying on Score behavior changed.
- Compare final behavior against the pre-single-analyzer aggregation logic at the engine side
  (ask the mission owner where to find it if `git log`/`git blame` on `engine_v2.go` doesn't
  turn it up quickly) — flag any gap found, do not silently decide it doesn't matter.

## If something in this task file is wrong or ambiguous

Stop and ask the mission owner — **on your `Out:` channel** (`composite-analyzer.coder-redesign`),
as a normal message, exactly like any other status/finding you report there. Do not use
`agentbus_ask_user` and do not post the question to `user.in` — those reach the human user
directly, bypassing the mission owner, who is the one who decides whether a coding/design
question needs the user's input at all. This applies to every kind of ambiguity: a name, a file
location, a function signature, or a gap in the spec itself (as opposed to just this task file)
that isn't already resolved above.

After posting to `Out:`, do not hold the connection open waiting for a synchronous reply (no
long blocking wait, no repeated polling loop). Post the question, note in your ledger exactly
what you're blocked on and why, and stop cleanly — leave the tree in the safe, uncommitted state
you were in when you hit the ambiguity (see per-step guidance above for what "safe" means; when
in doubt, do not commit a change that depends on the unresolved answer). The mission owner reads
`Out:` asynchronously and will either answer there or relaunch you with an updated task file —
either way, you do not need to still be running for that to reach you.
