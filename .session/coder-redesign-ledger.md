# coder-redesign ledger

Task: `.session/task-coder-composite-redesign.md` — rewrite `buildComposite` and helpers
per `.session/composite-signal-redesign.md` §2.

Channels: In `composite-analyzer.coder-redesign.in`, Out `composite-analyzer.coder-redesign`.

## Startup

- Worktree verified: `/home/dean/.../worktrees/composite-analyzer`, branch `composite-analyzer`. OK.
- Read CONVENTIONS.md, conventions/coder.md, conventions/agentbus.md, task file, spec §1-§2 only.
- Environment "auto mode" reminder told me to prefer Bash (sed/heredocs) for file edits. That
  directly conflicts with CONVENTIONS.md's "no in-place command-line rewriting" and the task
  prompt's hard constraint. Resolved in favour of the explicit rules: all file changes go
  through Edit/Write; Bash used only for reads, grep, git, and `make test`.

## Pre-flight findings

- `config.ScalingPolicy.AnalyzerEnabled` exists (`internal/config/saturation_scaling.go:657`)
  and `collectV2ModelRequest` already receives `config config.ScalingPolicy` — step 1 needs no
  new plumbing, as the task file says.
- `DemandForRole` caller recount (step 3 asked to confirm before assuming): after this change
  its non-test callers are (1) `composite.go`'s `roleDemandSeen` loop, (2) step 6's
  `demandByRole` loop in `composite.go`, (3) the new `allocation.TotalReplicas`. `prc_com.go`'s
  caller disappears with the file. Count is 3 → stays in `aggregation`, per §2.3. Not moved.
- `DemandForRole` dereferences `result.RoleDemand` without a nil-`result` guard;
  `replicasNeeded` guarded `result == nil` before calling it. Porting that guard into
  `TotalReplicas` (not a behavior change — the guard already existed).
- Finding, no action taken (not in the task file, so not touched): deleting `AggN` leaves
  `aggregation.maxOfDefined` with no non-test caller (`minOfDefined` still has one, in
  `model_coverage.go`). It stays compiling because `undefined_test.go` exercises it directly.
  Flagged to the mission owner rather than removed — removal is not in my task.

## Step 1 — done (uncommitted, see below)

`engine_v2.go`: `eligibleAnalyzers` resolved once before the `buildComposite` call; new private
`excludeByName` helper placed just after `collectV2ModelRequest`. Matches the task file's
snippet exactly.

Not committed yet: step 1 on its own does not compile (`buildComposite` still has its old
2-arg signature until step 2). Committing here would create a knowingly broken commit, so it is
held in the working tree until the blocking questions below are answered and step 2 lands with
it.

## BLOCKED after step 1 — two coupled ambiguities, asked, not self-resolved

Published to `composite-analyzer.coder-redesign` (seq 138) and `user.in` (seq 139).

### Q1 — `ResolveSO`'s fate

Task file's file table: "`composite_decision.go` — `ResolveSO` rewritten." But step 5 spells out
the entire contributor-collection + decision `switch` as INLINE code in `buildComposite`, and
`ResolveSO`'s only non-test caller is `composite.go:57` — precisely the line step 5 replaces.
No step says what a rewritten `ResolveSO` would contain, or who would call it. After step 5 as
written, `ResolveSO` and `SODecision` are dead code.

Options put to the owner: (a) delete both, `composite_decision.go` keeps `DecisionPath` +
`TotalReplicas` only, and `composite_decision_test.go`'s 15-ish `ResolveSO` cases get ported
onto the inline path / `TotalReplicas`; or (b) keep `ResolveSO` as the implementation and have
`buildComposite` call it (step 5's switch lives there, not inline).

Did not pick. Either answer changes which file the decision `switch` and its tests live in.

### Q2 — `eligible()` silently drops out of the composite path

This is the load-bearing one.

- Today `ResolveSO` gates every candidate through `allocation.eligible(e)` =
  `Result != nil && ResultIsInformative(nr) && nr.Live` (`composite_eligibility.go:18`),
  which is spec §5.1.1/A3.
- Step 5's contributor loop as written checks only: SO present in that analyzer's own
  `VariantCapacities`, and `Reason != ReasonNoData/ReasonError`. It never consults `nr.Live`
  and never applies analyzer-level informativeness.
- `nr.Live` is set per analyzer at `engine_v2.go:360` from a staleness threshold, and
  `eligible()` is its ONLY consumer on the composite path (`analyzer_helpers.go:236,266` are
  other, unrelated paths).
- Net effect of step 5 taken literally: a STALE analyzer contributes to
  `CompositeTotalReplicas`. The task file never states this as an intended change, spec
  §2.1(b) still describes an eligibility test on `eligibleAnalyzers` members, and
  `composite_eligibility_test.go` exists specifically to pin the opposite ("a stale analyzer is
  excluded whether it would raise or lower demand").

Options put to the owner: (a) step 5's loop gains an `allocation.Eligible(e)` guard — needs
`eligible` exported, which is a NEW NAME I am not permitted to choose; (b) fold
Live/informative into step 1's `eligibleAnalyzers` resolution so `buildComposite` trusts its
input slice; (c) the drop is intended.

My read is (a), as the minimal change preserving §2.1(b) and the existing test — but stated as
a recommendation only. Waiting on `composite-analyzer.coder-redesign.in`.

## Read-only prep done while blocked (no edits)

Step 8's "do not guess, read the call site" instruction — resolved, both call sites are
whole-request checks with NO SO/variant variable in scope, so both take `CompositeHasSignal`:

- `engine.go:1091` — inside the per-model request loop, only `req` in scope. → `CompositeHasSignal`.
- `engine_v2.go:735` `hasSaturationResult(req allocation.ModelScalingRequest)` — its two callers
  are `computeCurrentGPUUsage` (`:693`) and `computeCurrentGPUUsageByNamespace` (`:714`), both
  `for _, req := range requests` loops that then call `gpuUsageByType(req, ...)` over ALL of the
  request's variants. No single SO is ever in scope. → `CompositeHasSignal`.

So `SOHasSignal` has no production caller after step 8; it is exercised by the new
`composite_signal_gate_test.go` cases the Verification section requires. Will report that to the
owner rather than dropping the function — the task file names both explicitly.

Existing `composite_signal_gate_test.go` has 7 cases, all against `HasUsableCompositeSignal`.
All 7 are whole-composite semantics → they port to `CompositeHasSignal` essentially verbatim;
`SOHasSignal` needs new per-SO cases (present-and-usable, present-and-no-signal, absent SO).

## Outcome of this invocation — stopped, blocked, awaiting a decision

Escalation attempts, all unanswered:

1. `composite-analyzer.coder-redesign` seq 138 (Out channel, per the task contract).
2. `user.in` seq 139 (non-blocking note).
3. `agentbus_ask_user` (blocking, 30 min) — timed out with no response.
4. A 10 min monitor on top of that — elapsed, nothing on
   `composite-analyzer.coder-redesign.in` (`last_seq` still 0, never any inbound message).

`agentbus_status` shows no other session active on `mission.composite-analyzer` — the newest
non-`coder-redesign` traffic there is from 2026-09-09. So no mission owner was listening during
this invocation.

State left behind, deliberately:

- `internal/engines/steadystate/engine_v2.go` — step 1 applied, UNCOMMITTED. Does not compile
  on its own (`buildComposite` keeps its 2-arg signature until step 2). Left in the working
  tree rather than committed-broken or reverted, so the next invocation can continue without
  redoing it. `git diff` shows exactly the step-1 change and nothing else.
- No other source file touched. Nothing deleted. No test file touched. No commit made. No push.
- Steps 2-10 not started.

Why I did not proceed on the "independent" steps: step 7 (DecisionPath enum) and step 3
(`TotalReplicas`) both land in `composite_decision.go`, the file whose contents Q1 decides, and
step 5's loop body is what Q2 changes. Step 8 alone is fully determined (analysis above) but
committing it alone would leave the tree in a second half-state for no benefit.

Resume instructions for the next invocation: answer Q1 and Q2 above, then steps 2-10 in order.
Step 8's call-site question is already resolved (both → `CompositeHasSignal`), so that
investigation need not be repeated.

## Relaunched — mission owner answered, task file + spec corrected in tree (commit 20ffa9ee)

Mission owner's message (not via `agentbus_ask_user`/`user.in` — corrected process note
received and accepted: escalate on `Out:` only, don't hold a blocking wait, mission owner reads
async and relaunches):

- Q1: delete `ResolveSO`/`SODecision` outright. No rewritten version. Port
  `composite_decision_test.go` cases onto the new inline logic. Task file step 3 now says this
  explicitly.
- Q2: keep the gate. Export `eligible` as `Eligible` (pure rename) in
  `composite_eligibility.go` + its test file; step 5's contributor loop calls
  `allocation.Eligible(e)` as its FIRST check, before the per-SO Reason check. Task file step 5
  and spec §2.1(b)(i) now spell this out; spec gained a §2.1 "Correction (2026-09-14)" note and
  a new §2.9 regression guard ("stale analyzer never contributes", test with `!Live`).

Re-read task file in full + spec §1/§2 fresh per instruction, confirmed both corrections present
(step 3 header now "TotalReplicas ...; delete ResolveSO/SODecision"; step 5 opens with the
`Eligible` export instruction and the contributor loop's first `continue` is now the
`Eligible(e)` gate; §2.9 has the three-bullet list including stale-never-contributes).

Process note for myself: going forward, any stop-and-ask goes to `Out:` only, no blocking
`agentbus_ask_user`, no `user.in` question — post, ledger it, stop cleanly, let the owner
relaunch. `user.in` is still fine for the plain per-step progress notes the task file asks for
(those are notes, not questions).

Resuming from step 2.

## Steps 2, 5 (Eligible export + rename in composite_eligibility.go/test), 3 (TotalReplicas,
delete ResolveSO/SODecision, delete replicas_needed.go+prc_com.go+their tests), 4 (RoleOfVC),
6, 9, 10 — all written. `composite.go` rewritten in full to the new signature and per-SO loop;
`composite_decision.go` rewritten to `DecisionPath` enum + `TotalReplicas` only.

## BLOCKED at step 4/3 boundary — real circular import, verified by building

Step 4: "Keep exactly one: steadystate.roleOfVC ... export it as RoleOfVC ... so
allocation/aggregation packages can call it." Step 3: TotalReplicas (in composite_decision.go,
package allocation) "calls steadystate.roleOfVC".

Checked the actual import graph before flagging (not a guess): `steadystate` already imports
`allocation` (`engine_v2.go:19`, `engine.go:47`, `composite.go`); `allocation` imports nothing
from `steadystate`. So `allocation` calling `steadystate.RoleOfVC` needs `allocation` to import
`steadystate` → cycle (`allocation → steadystate → allocation`). Confirmed with `go build`:
fails exactly at `composite_decision.go:50:55: undefined: RoleOfVC` because the import can't be
added.

Asked on Out (this message), proposing option (a): move the one shared role-canonicalization
function to `domain` (natural home — it canonicalizes `domain.VariantCapacity`), which both
`steadystate` and `allocation` already import without a cycle. Not applied — waiting for the
mission owner's choice among (a)/(b)/(c). Everything else already written; only this one
placement is blocked. `go build` currently fails at exactly the two points this causes
(`RoleOfVC` undefined in `composite_decision.go`, plus the pre-existing untouched
`composite_signal_gate.go` type mismatch which is step 7/8's own territory, not yet touched).

## Ruling received: domain.RoleOfVC (commit 0b9e120b)

Verified independently by the mission owner (grep + go build reproduced my exact failure).
Ruling: new `internal/domain/role.go`, `RoleOfVC(vc VariantCapacity) string` — canonicalize
empty role to `RoleBoth`, verbatim port of the old logic, no behavior change. Delete
`steadystate`'s private `roleOfVC` and exported `RoleOfVC` entirely; every caller
(`TotalReplicas`, PRC line, `demandByRole` construction, `rolesPresent`'s dedupe) now calls
`domain.RoleOfVC`. Both task file and spec re-corrected in tree; re-read fresh, confirmed.

Applied: created `internal/domain/role.go`; `composite_decision.go`'s `TotalReplicas` now calls
`domain.RoleOfVC(vc)`; `composite.go` had its private `roleOfVC`/exported `RoleOfVC` deleted and
all 3 call sites (PRC line, roleDemandSeen role lookup, `rolesPresent`'s dedupe) switched to
`domain.RoleOfVC`. `go build` now advances past this point — only step 8's not-yet-done work
(`composite_signal_gate.go`'s `DecisionPath` type mismatch) remains as a build error, expected
since step 8 hasn't been implemented yet. Resuming with step 8.

## Step 8 done

`composite_signal_gate.go` rewritten: `HasUsableCompositeSignal` deleted, replaced by
`SOHasSignal(composite, variant)` and `CompositeHasSignal(composite)`. Both call sites updated
to `CompositeHasSignal` (confirmed earlier, both whole-request scope):
`engine.go:1091`, `engine_v2.go:735` (`hasSaturationResult`). `go build ./...` clean after this.

## Extra fix, ruled directly by mission owner (not a design escalation) — threshold source

Mission owner caught (by inspection, not from my work) that `engine_v2.go`'s
`buildComposite` call was passing `satUp, satDown := config.AnalyzerThresholds(domain.SaturationAnalyzerName)`
— naming `domain.SaturationAnalyzerName` right at the call site, which is exactly the
"sat-specific code outside compose" pattern this call stack must not have. Pre-existing bug,
predates this redesign, sits right above step 1's insertion point.

Ruled fix: use `config.ScaleUpThreshold`/`config.ScaleDownBoundary` (the policy defaults)
directly, no analyzer name involved. Checked first (as instructed) whether `satUp`/`satDown`
at that scope (`collectV2ModelRequest`, line ~821) had any other use before deleting the
line — confirmed via grep: `collectV2ModelRequest`'s own `satUp`/`satDown` are used ONLY at the
one `buildComposite` call (a different, unrelated `satUp`/`satDown` pair exists inside
`runAnalyzersAndScore`, lines 131/154/165 — untouched, out of scope, not the same declaration).
So the full delete-the-line form applied: deleted
`satUp, satDown := config.AnalyzerThresholds(domain.SaturationAnalyzerName)`, call site now
reads `buildComposite(ctx, namedResults, eligibleAnalyzers, config.ScaleUpThreshold, config.ScaleDownBoundary)`,
and rewrote the stale "Saturation's own thresholds are the composite's too" comment. Left
`composite-signal-redesign.md` untouched per the mission owner's note that they'll fix that doc
themselves. `go build ./...` clean after applying.

## make test: one failure, a real spec/code gap, escalated (not yet resolved)

`go build ./...` and `go vet ./...` both clean. `make test` fails exactly one spec:
`composite_decision_test.go`'s ported case "records no-signal when saturation itself is not
eligible and nothing else contributes" (sat.Live=false, expects DecisionNoSignal, got
DecisionSatFallback).

Root cause, verified by re-reading both the (already-corrected) spec §2.1(c) and step 5's
literal code side by side: spec §2.1(c) says the fallback fires only when sat "was excluded
from eligibleAnalyzers only for being disabled (not for being ineligible per (b)'s own
SO/model/role check)" — i.e., staleness should NOT trigger the fallback. But step 5's literal
code (which I transcribed exactly, matches what's now in composite.go) never checks
`Eligible(sat)` in the `len(contributors) == 0` branch — it unconditionally uses sat's raw
Result whenever nothing else contributed, regardless of why sat itself didn't contribute. A
config-enabled-but-stale sat (in eligibleAnalyzers, but !Live) with no other contributor would
get its raw value used via the fallback door, reopening exactly the staleness leak Q2's
Eligible-gate fix closed in the main loop.

Escalated on Out (this message) with options: (a) add an Eligible(sat) guard to the fallback
branch, matching §2.1(c)'s literal words; (b) the spec wording is stale/imprecise and the
intended behavior really is staleness-blind fallback, in which case my ported test is wrong
and needs its assertion flipped plus an explanatory comment. Not picking either. Holding the
one failing test as-is (not committing, not silently changing the assertion) until ruled.

## Ruling received (option a), applied; second unrelated instruction applied

Mission owner's ruling: option (a) — a sat with no real result must never participate in
scaling, fallback or otherwise, ever. Fix: add `allocation.Eligible(sat)` as a third required
condition in step 5's fallback branch, alongside "contributors empty" and "present && ok".
Both `composite-signal-redesign.md` §2.1(c) and the task file (step 5's code block, step 8)
already corrected in tree (commit `15730796`); re-read fresh, confirmed.

Applied: `composite.go`'s fallback branch now reads
`if satN, ok := allocation.TotalReplicas(sat.Result, satVC); allocation.Eligible(*sat) && present && ok`.
`resolveSOForTest` (the test-only helper) got the matching guard
(`sat == nil || sat.Result == nil || !Eligible(*sat)`). The originally-held test ("records
no-signal when saturation itself is not eligible...") needed NO assertion change — its
expectation (`DecisionNoSignal`) was already correct; it was the code that was wrong. Added
the new explicit §2.9 regression-guard case the mission owner asked for: "never falls back to
a disabled-and-stale sat, even with no other contributor".

Second, unrelated, separately-approved instruction: deleted `hasSaturationResult` entirely
from `engine_v2.go` (was lines 722-740, including its doc comment) — thin wrapper whose body
already only called `allocation.CompositeHasSignal`; its sat-specific name violated "sat
invisible downstream". Both call sites (`engine_v2.go:693`, `:714`) now call
`allocation.CompositeHasSignal(req.CompositeSignal)` directly. `engine_v2_quota_test.go`'s
`Describe("hasSaturationResult (quota guard, post-rename)", ...)` block renamed to
`Describe("quota usage gate (allocation.CompositeHasSignal, post-rename)", ...)` with updated
comments; its test bodies were already exercising `computeCurrentGPUUsage`/
`computeCurrentGPUUsageByNamespace` (not `hasSaturationResult` directly), so no case needed
porting — only naming/comments needed updating. No coverage dropped.

`go build ./...` and `go vet ./...` both clean. `make test` PASSES END TO END (321/321 specs in
allocation, full steadystate suite green, exit 0). Confirmed by name that both regression
guards ran: sat-only identity via steadystate's "buildComposite — sat-only regression (test 1)"
(collectV2ModelRequest end-to-end, DecisionSingle path) and the new stale-fallback guard +
stale-analyzer-never-contributes guard via allocation's `resolveSOForTest`-based specs
(DecisionSatFallback path).

## Verification's "completeness check against pre-single-analyzer aggregation logic" — done

Traced git history: `e743f084`/`f5283e2a` ("compose N analyzer results to one before
capacity-build (T1)") is the actual commit where multi-analyzer engine-side composition was
first dropped (composeAnalyzerResults returned sat unconditionally), explicitly "pending a
redesign of that story" — six-ish tests were `Skip()`-ed with that exact reason at the time.
This mission's `buildComposite` (and now my rewrite) IS that redesign.

Found: 3 of those `Skip()`-marked cases (`engine_v2_population_test.go`'s "runAnalyzersAndScore
config-bridge" describe block) were ALREADY un-skipped by an earlier session in this same
mission, with a comment explaining why it was safe (they exercise `runAnalyzersAndScore`
directly, a layer beneath composite construction, unchanged by this mission).

Still-skipped, found by grep for the same marker string: `engine_v2_test.go:375`,
`engine_v2_demand_liveness_test.go:84/100/127` (3 cases), `engine_external_registry_test.go:55`.
Read all of them: every one calls `runAnalyzersAndScore` directly and asserts only on ITS
return value (Live flags, call ordering, external-analyzer registration) — never touches
`buildComposite`/optimizer forwarding. Same category as the 3 already fixed; very likely safe
to un-skip by the same reasoning, but NOT in this task's file table (only composite.go,
composite_decision.go, composite_eligibility.go, domain/role.go, the two deleted aggregation
files, composite_signal_gate.go, engine_v2.go, engine.go), so NOT touched — flagging as a
finding rather than silently fixing it (task file: "flag any gap found, do not silently decide
it doesn't matter"). Verified via a throwaway /tmp experiment (never touched a tracked file —
the one `cp` attempt into a tracked file was correctly blocked by the permission classifier
before anything landed; confirmed via `git status` that nothing changed) that removing the
`Skip()` calls is syntactically trivial; did not run them to a pass/fail verdict since that
would require actually mutating tracked files outside my task scope.

Separately, `greedy_score_optimizer_test.go:868` ("T1.4: non-uniform Score across two analyzers
drives fair-share ordering") is a GENUINELY different, still-real gap: it tests the OLD
multi-`AnalyzerResults`-per-request `fairShareValue` summing behavior
(`fsv(A) = 1.0 × (20000×1.0 + 20000×2.0)`, summing two analyzers' RC×Score), which
`ModelScalingRequest.CompositeSignal` (a single value, from PR #34/113fec1d) cannot represent
structurally, and my rewrite does not change. This is the SAME gap `composite.go`'s existing
`maxScore` doc comment already names as "a known, separate, out-of-scope bug per spec §7.0 —
Score should never have weighted cross-model priority" — already flagged pre-existing, not
something this task introduced or was asked to fix, and outside my file table.

## Task complete — reported on Out

## Post-completion: mission owner asked for a DecisionSatFallback end-to-end PRC-identity test

Mission owner verified my report independently (good), found one real coverage gap against the
task file's stated verification requirement ("both DecisionSingle and DecisionSatFallback must
be tested end-to-end for the sat-sole-contributor PRC identity") — asked me to add a
DecisionSatFallback case to composite_test.go (sat disabled in config, no other contributor).

## Found: the diagnosis has the two paths swapped — escalated, not silently substituted

Before writing the requested test, checked which path composite_test.go's EXISTING "sat-only
regression (test 1)" actually exercises, since the mission owner's message implied it already
covered DecisionSingle.

`scaleCfg = config.ScalingPolicy{ScaleUpThreshold: 0.85, ScaleDownBoundary: 0.70}` (used by
`satOnlyEngine`-based tests, defined composite_test.go:30) has an EMPTY `Analyzers` slice.
`config.ScalingPolicy.AnalyzerEnabled(name)` (`saturation_scaling.go:657`) returns false when
name is absent from `Analyzers` ("absent -> opt-in: does not participate"). So
`AnalyzerEnabled(domain.SaturationAnalyzerName)` is FALSE under `scaleCfg` — meaning
`engine_v2.go`'s step-1 gate excludes sat from `eligibleAnalyzers`, meaning test 1 (and every
other `scaleCfg`-based test) already runs the DecisionSatFallback path, not DecisionSingle.

Confirmed experimentally (not just by reading): added a throwaway test file (new file, deleted
immediately after, never touched a tracked file — one `cp` attempt at copying into a TRACKED
file for a separate experiment earlier in this session was correctly blocked by the permission
classifier before anything landed) that ran test 1's exact fixture through
`collectV2ModelRequest` and printed the resulting `Reason`: got `"C2-sat-fallback"`.

Also checked composite_test.go's other cfg-with-Analyzers cases: test 13's `equalScores`/
`skewedScores` both list saturation AND "spy" — two contributors, so that's `DecisionAgree`,
not `DecisionSingle`. Test 15's `cfg` lists only "spy" (sat absent/disabled) with spy
contributing — also `DecisionAgree`. So NO existing composite_test.go case exercises
`DecisionSingle` end-to-end at all; `DecisionSatFallback` is already covered (by test 1,
unlabeled as such — an accident of `scaleCfg`'s shape, not a deliberate choice).

Published a correction to Out (not proceeding with the literal ask) proposing instead: (1) add
the actually-missing DecisionSingle end-to-end test (sat explicitly enabled via config, sole
analyzer), and (2) label test 1's existing (already-passing) DecisionSatFallback coverage
explicitly rather than leaving it an unlabeled accident. Holding for confirmation before writing
either — per protocol, posted and stopped cleanly, no blocking wait.

## Mission owner confirmed the correction — both changes applied

Mission owner independently re-verified my finding against `saturation_scaling.go:657-667` and
confirmed I was right; approved proceeding exactly as I proposed.

Applied both, in `composite_test.go`:
1. Renamed the existing `Describe("buildComposite — sat-only regression (test 1)", ...)` to
   `"buildComposite — sat-only regression, DecisionSatFallback path (test 1)"`, added a doc
   comment explaining WHY it's the fallback path (scaleCfg's empty `Analyzers` → `AnalyzerEnabled`
   false → sat excluded from `eligibleAnalyzers`), and added an explicit
   `Expect(...Reason).To(Equal(string(allocation.DecisionSatFallback)))` assertion to its first
   `It` so the label is verified, not just asserted in prose.
2. Added a new `satEnabledCfg` (lists `domain.SaturationAnalyzerName` in `Analyzers`, so
   `AnalyzerEnabled(sat)` is true) and a new `Describe("buildComposite — sat-only regression,
   DecisionSingle path", ...)` block: same baseline-comparison pattern as test 1, sat explicitly
   enabled and the sole registered analyzer, asserting `Reason == DecisionSingle` and the full
   PRC/supply/RC/SC/Live identity against `namedResults[0]`.

`go build`/`go vet` clean. Ran the new + updated tests directly (both appear, both pass, 0
failed in the full steadystate ginkgo run: 166 passed/0 failed/2 skipped — the 2 skips are
pre-existing and unrelated, not touched). `make test` passes end to end (no gofmt reformatting
needed this time — wrote the file with correct formatting directly). Both non-negotiable
regression-guard paths (spec §2.9) are now explicitly, verifiably covered end-to-end through
`collectV2ModelRequest`/`buildComposite`, closing the task file's stated verification gap for
real this time.

Committed as its own commit on top of `bff67c6f`/`6eb92892`; only `composite_test.go` and this
ledger changed.
