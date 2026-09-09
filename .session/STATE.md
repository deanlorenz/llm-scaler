# composite-analyzer

## Orientation

- **Conventions:** `worktrees/session-tracking/CONVENTIONS.md`
  *(read this first, before any other file)*
- **What / goal / mission:** The **composite aggregation calculation** — reduce the
  `[]NamedAnalyzerResult` from `runAnalyzersAndScore` into the single `CompositeSignal` the
  optimizer consumes, so every enabled analyzer's demand influences it, not just saturation's.
  Aggregation at both model and role level. Normalization into **saturation's units** (token
  capacity) for now; request-based normalization ("100 requests waiting in EPP queue") is the
  eventual target but is deferred. This is `single-analyzer`'s **CT7**, lifted into its own
  mission, with two user-directed departures: the composite gets a **new name**, and **all
  analyzers' Scores** affect the composition.
- **Worktree:** `worktrees/composite-analyzer` (branch `composite-analyzer`)
- **Role / scope:** mission owner — owns STATE, the plan, the `composite-analyzer` branch, and
  integration decisions for this mission only.
- **Ledger / log:** active `.session/2026-09-08-composite-analyzer-1.md`; captured retired
  `.session/ledger/<slug>.md`
  ⚠ DO NOT READ — not yours; new session creates its own ledger

## Task

- **Plan / spec:** `.session/spec.md` — mission spec, **v8, APPROVED [USER, 2026-09-08]**.
  Implementation explicitly held back pending a separate go-ahead — see Next step.
  *(do not read upfront — pull on demand only)*
- **Survey:** `.session/survey-zero-signal.md` — what breaks on a zero/absent composite signal;
  also inventories the existing `wva_model_scaling_blocked` gates. *(pull on demand)*
- **Context:** (for implementation, once the spec is approved)
  - `internal/engines/steadystate/engine_v2.go` — `runAnalyzersAndScore` (:102),
    `collectV2ModelRequest` `CompositeSignal:` assignment (:797), `hasSaturationResult` (:722),
    `buildCapacities` (:850)
  - `internal/engines/allocation/optimizer_interfaces.go` — `NamedAnalyzerResult`,
    `ModelScalingRequest.CompositeSignal`
- **Refs:** *(do not read unless needed — all cross-worktree reads, authorized by user)*
  - `worktrees/single-analyzer/.session/compose-logic-plan.md` — the p3 "initial plan"; its
    RC-max premise does **not** hold on this base (see spec §2.3)
  - `worktrees/single-analyzer/.session/spec.md` — §CT7 and §"Semantic framework"
  - `worktrees/single-analyzer/.session/pr-spec-34-composite-signal.md` (merged PR #34),
    `pr-spec-next-coverage-units.md` (CT6 + its unfixed correctness bug)
  - `internal/engines/allocation/multi_backup/analyzer_helpers_multi.go` — pre-single-analyzer
    multi-entry aggregations to port (`//go:build ignore`, cannot compile as-is)
- **Expected output:** this phase — an approved mission spec. Mission overall — the composite
  aggregation implemented, with the test plan in spec §9.
- **Done / completion criteria:** spec approved by the user, with the 7 open items in spec §10
  decided.
- **Limits:**
  - **Until the spec is approved, ALL work stays in `.session/`.** No code, no writes outside
    this worktree (user instruction 2026-09-08). Cross-worktree **reads** are authorized.
  - Do **not** modify `internal/engines/allocation/multi_backup/` — upstream-tracked,
    `//go:build ignore`. Port its arithmetic into new code; leave the files alone.
  - **Normalization is deferred.** Do not build on CT6 / `normalizeToCompositeUnits`
    (absent from this base, and its parent-branch implementation has an unfixed correctness
    bug — spec §2.3). Composite stays in saturation's token units.
  - Branch base is `upstream/main` @ `c013012e` (rebased again 2026-09-08 with user approval —
    "rebase first" — after the prior base `4db060e2` went stale within hours; upstream is
    actively moving). Do not rebase again without user approval. The pre-rebase tip `006486b9`
    is preserved in the reflog at `composite-analyzer@{1}` (the tip before that, `b4549217`, is
    further back in the same reflog). When checking "have I changed anything", use
    `git rev-list --left-right --count upstream/main...composite-analyzer` or compare against the
    recorded base SHA — a bare `git diff upstream/main..` conflates "I changed things" with
    "upstream advanced", and a tree-to-tree diff across the rebase looks alarming for the same
    reason; compare the two commits' specific blobs instead.
  - Preserve the **sat-only fast path** exactly: one analyzer ⇒ composite numerically identical
    to today. Non-negotiable.
  - **Do not invoke the upstream `pr-review` skill.** The user does not want it used on this
    mission (decision 2026-09-08). It ships tracked on `upstream/main` and was left in place
    unmodified — no deletion, no settings override. It already carries
    `disable-model-invocation: true`, so it is never model-invoked; treat it as off-limits even
    so. It runs only if the user explicitly types `/pr-review`.
- **Extra rules / rule refs:** `conventions/mission-owner.md`, `conventions/feature-worktree-setup.md`

## Execution

### Steps / subtasks
- [x] Create mission worktree off `upstream/main`
- [x] Set up `.session/`, skill symlinks, verify clean `git status`
- [x] Create initial `STATE.md`
- [x] Create `session-tracking/missions/composite-analyzer/` symlinks (uncommitted, by design)
- [x] Pin session into the worktree (`EnterWorktree`)
- [x] Commit `.session/` to the mission branch
- [x] Rebase onto current `upstream/main` (`4db060e2`, then again onto `c013012e`)
- [x] Define mission goal and scope with the user
- [x] Read the parent mission's specs (p3 plan, CT7, semantic framework, PR #34, CT6)
- [x] Draft the mission spec → `.session/spec.md` (v1)
- [x] User review #1 → **v1's core conversion rejected**; spec rewritten as v2
- [x] User review #2 → six factual corrections, all re-verified against upstream source; **v3**
- [x] User review #3 → five real errors (see below); **v4**
- [x] User review #4 → three of my own misreadings corrected (no design change); **v5**
- [x] User decided **D2** (composite decision-path field; no-signal gate; scale-from-zero PRC
      fallbacks); **v6**
- [x] User **reversed D1** — Score deferred entirely, `Agg_N` is a pure `max`; and **scoped D3** —
      the target is repeated PRC/demand/bounds/`ceil()` derivations, not variant selection; **v7**
- [x] **Zero/absent-signal survey done** → `.session/survey-zero-signal.md`
- [x] **Answered the 2 remaining §10 questions** — D2: yes, policy-owned reason; D3: first two
      derivation categories only (ceil()/rounding + PRC/demand lookup)
- [x] **Veto pass on §10/D4's 12 confirmations** — all 12 confirmed, no vetoes
- [x] **Get explicit user approval of spec v8 as a whole** — approved 2026-09-08
- [x] **Get separate explicit go-ahead to begin implementation** — user: "go ahead. implement and
      review" (2026-09-09)
- [x] Confirm coder isolation setup with user — **same-worktree**, async/background
- [x] Re-read `coder-orchestration.md` and `worktree-delegation.md` in full (this session,
      2026-09-09, immediately before dispatch — not stale)
- [x] Write task file `.session/task-coder-agg1.md` — 12-item checklist, one commit per item,
      decomposing spec v8 §4–§9 in dependency order (accessor → eligibility → per-SO N/Agg_N →
      fallback/decision-path → gate repair → derivation chain → cross-role coverage → composite
      construction/wiring → D3-scoped query API → observability audit → full test-plan sweep)
- [x] Launch coder (`coder-agg1`) in this worktree, same-worktree/async
- [x] Set up reviewer (`reviewer-agg1`) to read commits as they land
- [x] **Coder reports all 12 checklist items complete** — commits `4ac16404..f98a566f` on
      `composite-analyzer`, self-reported `make test`/`make lint` clean (2026-09-09)
- [x] **Independently verified the coder's completion claim** (mission owner, not taking the
      report on faith — convention rule 10): `go build ./...` clean; `go test $(go list ./... |
      grep -v /e2e | grep -v /benchmark)` (exact `make test` scope) all green including
      `aggregation`/`allocation`/`steadystate`; Ginkgo suite in `steadystate` — 165/167 specs
      passed, 0 failed, 2 skipped (pre-existing, unrelated). Both non-negotiable regression guards
      exist and pass: test 1 (`composite_test.go:36-38`, sat-only numerical identity) and test 13
      (`composite_test.go:213-216`, Score has no effect). IDE diagnostics flagging
      `undefined: DemandForRole` and unused lowercase `demandForRole` were stale/mid-edit-cache
      artifacts — not real; `go vet ./...` is clean.
- [ ] **Reviewer's full Phase 1 + Phase 2 pass** — re-invoked 2026-09-09 now that all 12 commits
      landed; was previously only standing by on an empty range. In progress.
- [ ] Mission owner reviews the reviewer's verdict, confirms/pushes back, integrates
- [ ] Implement: `Agg_N` + derivation chain in/beside `internal/engines/aggregation/`, query API,
      composite naming + quota-guard repair, observability audit, 30-case test plan — **DONE per
      coder + independent build/test verification; pending reviewer's spec-conformance verdict**

**Last completed:** independent verification of `coder-agg1`'s self-reported completion (build,
`make test` scope, both regression guards). Reviewer re-engaged for its full pass; not yet landed.

**Errors review #3 caught, for context on how much to trust the current draft:** (1) `N` and
coverage are the same quantity (`cov = 1/N`) — v3 computed both and called it a cross-check;
(2) the composite's construction was inverted — demand is unchanged, `N` carries the signal;
(3) saturation is a fallback, **not a floor** — the CT7 "floor invariant" was carried unexamined
through three drafts; (4) there is no single-model request-shape assumption — safety is structural;
(5) aggregator names encoded the operation (`max…`) instead of the quantity (`Agg_N`).

**Next step / resume point — as of 2026-09-09:**

- **Still working on:** `coder-agg1` reports all 12 checklist items complete
  (`4ac16404..f98a566f`); mission owner independently verified build/test/regression-guard claims
  (see checklist). `reviewer-agg1` has been re-invoked for its full Phase 1+2 pass over all 12
  commits — this was pending as of this STATE update; check `.session/review-coder-agg1.md` and
  `mission.composite-analyzer.reviewer-agg1.out` for its verdict before treating this mission as
  done.
- **Must keep:**
  1. Spec v8 is final and approved — do not reopen §10 (D1–D4) or re-litigate any of the
     "Design core" bullets below without the user raising it first.
  2. Two suggestion-box entries are filed, uncommitted, awaiting `policy-writer`:
     `session-tracking/suggestion-box/2026-09-08-2300-composite-analyzer.md` (`.wip` protocol
     should not apply to a mission owner's own single-writer STATE.md) and
     `2026-09-08-2311-composite-analyzer.md` (coder-dispatch conventions need a stated default
     path instead of three cold options). Do not resubmit or duplicate these.
  3. Branch was rebased onto `upstream/main` @ `c013012e`; do not rebase again without asking
     first.
  4. **Concurrency (same-worktree setup):** the coder reported done, but per convention rule 10
     the task is not closed until reviewed. Do not launch a second coder into this worktree while
     any follow-up/fix work might still be needed from `coder-agg1` — it was told to hold open
     after reporting done and wait on its `In:` channel rather than terminate, so re-engage it
     (SendMessage) for any fix rather than launching a new coder.
  5. Never push or publish without a fresh per-operation authorization — this includes not
     opening a PR yet; the user has not asked for that.
- **Continue from:** await `reviewer-agg1`'s verdict on `.session/review-coder-agg1.md`. If
  Pass: report to the user, ask about next steps (PR? further missions?). If Request Changes:
  relay findings to `coder-agg1` via its `In:` channel, do not fix code directly in this
  worktree while a coder is nominally still assigned to it.

**Standing instruction from review #4:** **[USER]** "Always ask me if not sure." Do not infer intent
from examples or fill gaps with invented premises — ask.

### Status

- Environment: **ready** — worktree on branch `composite-analyzer`, rebased onto `upstream/main`
  @ `c013012e`; 0 commits behind upstream, 12 ahead (all `.session/`-only doc commits). Only diff
  vs `upstream/main` is `.session/`. `.session/` tracked and committed, not gitignored;
  `resume-mission` + `wind-down` symlinks verified resolving into
  `session-tracking/claude-skills/`; `git status` clean.
- Session is **not** pinned via `EnterWorktree` — it simply starts with this worktree as its
  working directory (corrected 2026-09-08; a prior version of this file wrongly claimed
  `EnterWorktree` pinning). Cross-worktree reads work via plain absolute paths, `cat`, `git -C`,
  or `git show <branch>:<path>` — whichever is convenient. Cross-worktree **writes** still require
  a specific exception per `conventions/working-outside-worktree.md`; that boundary is enforced by
  convention, not by tooling, so it must be self-enforced.
- Mission definition: **done** — see Orientation. Normalization deferred; sat units for now.
- **Implementation: AUTHORIZED [USER, 2026-09-09]** ("go ahead. implement and review").
  `coder-agg1` **reports all 12 checklist items complete**, commits `4ac16404..f98a566f` on
  `composite-analyzer`. Coder was told to hold open on its `In:` channel rather than terminate —
  it has not been released; re-engage it directly (SendMessage) for any fix rather than launching
  a new coder into this worktree. Task file: `.session/task-coder-agg1.md`. Coder ledger:
  `.session/coder-agg1-ledger.md` (created by the coder).
- **Independent verification (mission owner, 2026-09-09):** `go build ./...` clean; `make test`'s
  exact scope (`go test $(go list ./... | grep -v /e2e | grep -v /benchmark)`) all green; Ginkgo
  suite in `internal/engines/steadystate` — 165/167 specs passed, 2 skipped (pre-existing,
  unrelated), 0 failed. Test 1 (sat-only identity, `composite_test.go:36-38`) and test 13 (Score
  has no effect, `composite_test.go:213-216`) both exist and pass. `go vet ./...` clean. (IDE
  diagnostics that briefly showed `undefined: DemandForRole` were stale/mid-edit-cache noise, not
  real — reconfirmed via direct `go build`/`go vet`.)
- **`reviewer-agg1`: full Phase 1+2 pass re-invoked 2026-09-09**, now that all 12 commits have
  landed (its first pass, mid-coder-run, correctly found nothing yet to review). Verdict pending
  — check `.session/review-coder-agg1.md` before treating this mission as done. Do not report
  this mission complete to the user, open a PR, or push anything until that verdict lands.
- Spec: **v8, APPROVED [USER, 2026-09-08]**, `.session/spec.md` (~1207 lines). §10 fully resolved
  — D1–D4 all decided, D4's 12 confirmations all veto-passed. Survey delivered:
  `.session/survey-zero-signal.md`.
- **Design core (settled by the user, not mine to revisit):**
  - **PRC is per SO** (implies model, variant, role). **Demand is per (model, role)** — three
    values (`both`, `prefill`, `decode`) that do **not** depend on which SOs exist. SOs are added
    and removed; a role's demand does not change because of that. Converse also holds: an SO can
    have a real PRC while its role's demand is 0. True for every analyzer, saturation included.
  - **Storage layout has two shapes:** `AnalyzerResult.RoleDemand` is **nil** when not
    disaggregated, and the `both` demand then lives in **`TotalDemand`** — there is no `both` map
    key. Read demand only through one accessor that handles both.
  - **`N(SO)` is the single aggregated quantity** — replicas needed for one SO to cover its role's
    whole demand. **Coverage is just `1/N`**, not a second signal. Everything else derives from `N`.
  - **Demand is unchanged and `D_sat` is the definition of 100%:** `D_com[role] == D_sat[role]`,
    `PRC_com(SO) = D_sat[role(SO)]/N_com(SO)`. Sat units are a *definition*, not a conversion — so
    no analyzer's contribution passes through `PRC_sat`. Sat-only is then an identity.
  - **Saturation is a FALLBACK, not a floor.** The CT7 "floor invariant" is **retired**. Sat
    contributes only if eligible; it is the fallback when nothing else has a usable signal; the
    composite **may legitimately come out below sat alone**. Fallback kind must be marked.
    Note `D_sat` stays the *unit* even when sat does not *contribute* — separate roles.
  - **Aggregators are named for the quantity, never the operation** (`Agg_N`, not "max"), with the
    combination rule swappable in one place.
  - **Coverage/`N` is meaningless when PRC or demand is zero.** Every calculation guards it; an
    undefined contribution must never enter a `min`/`max` as `0` or `+Inf`. `demand == 0` flows
    through as `0`, never manufactured into `1.0`.
  - Cross-role rule `cov(M) = min(cov(prefill), cov(decode)) + cov(both)` is the *only* relation
    between role and model level.
  - **No single-model shape assumption.** Different models may have different request shapes in the
    same round. Safety is *structural*: PRC aggregates per SO, demand per model, so nothing crosses
    a model boundary. Shape matters across models in the *optimizer*, and in the analyzer's own PRC
    estimation — both outside this mission.
  - **`Score` vs `priority` are different axes:** `Score` weights different **analyzers'** opinions
    about one model (this mission); **priority** weights different **models'** demand (fair-share /
    `fairShareValue`). `fairShareValue` using Score is a bug in a known direction, not an ambiguity.
  - **`Score` is DEFERRED — `Agg_N` is a pure `max`** (user reversed the v6 confidence design;
    "leave it out for now"). Score must have **no** effect on the composite, and a test asserts that.
    The user's future direction is **outlier rejection + small bias** (`5,5,5,10` with a low-scored
    `10` → maybe `6`), i.e. a robust statistic — **not** a weighted average, which is a standing
    exclusion. Pure `max` is sound here because the signal is already normalized to replica count.
  - **Fallbacks must still yield a non-zero PRC** for an idle SO (partial scale-from-zero) and a
    never-seen SO — saturation's ladder already does this (own store record → compatible variant's
    `EffectiveCapacity`, both `P0-store`). The composite consumes those and must **not** discount an
    estimated PRC. **Over-estimation is acceptable**: worst case a replica is added, measured, and
    removed — whereas a zero PRC blocks the scale-up that would produce the measurement.
  - **The composite carries a decision-path field** mirroring `VariantCapacity.Reason`, per SO.
    **No signal at all ⇒ no autoscaling**, via the existing `hasSaturationResult` gate repaired to
    test for a usable signal rather than saturation's name. The survey found this is **seven
    independent `Result == nil` checks plus that one name check**, so the repair should expose **one
    shared "is there a usable signal" predicate** rather than fixing that single site — otherwise the
    rename leaves the system *partially* gated, which is worse than either extreme.
  - **A consistent query API** is part of the deliverable — coverage, missing coverage/capacity,
    replicas-or-GPUs-to-close-the-gap — so **each concept has one definition** shared by every
    optimization step, instead of each function inventing its own. Note: the mutation of
    `Remaining`/`Spare`/`RoleSpare` during allocation is **intentional tracking**, which is why the
    optimizer gets a deep copy — it is *not* a defect to design around. Answers are expected to
    change as allocation progresses; only the *meaning* of each question must be fixed.
  - **Full observability** reusing the *same* log/metric functions as any analyzer result.
- **Implementation seam:** `internal/engines/aggregation/` already exists — pure helpers named for
  what they aggregate (`SumTotalDemand`, `DemandByRole`, `AggregateByRole`, `IsDisaggregated`, …),
  already used by both analyzers. **New aggregations extend that package.** It is absent from every
  parent-mission document, which is why the user's "upstream is the source of truth" rule matters.
- **Rejected approaches — do not reintroduce:**
  1. Converting each analyzer's demand into sat units before aggregating (`D_i/PRC_i × PRC_sat`) —
     circular (routes every analyzer through the `PRC_sat` estimate those analyzers exist to
     correct) and uses a different factor per SO, so the result is denominated in nothing coherent.
  2. Scaling the composite's demand by a coverage ratio (`D_com = D_sat × cov_sat/cov_com`) — this
     *kills the composite signal*: demand is unchanged and `N` carries the signal.
  3. Treating saturation as a floor / `max`-ing against it. Sat is a fallback (see above).
  4. Computing coverage *and* `N` as if they were independent — `cov = 1/N`.
  5. Naming aggregators after their operation (`maxReplicas…`) instead of their quantity (`Agg_N`).
- **Lessons banked from `single-analyzer-normalize`** (deferred branch; learn from, do not build
  on): **deep-copy is mandatory** — a plain value copy of `NamedAnalyzerResult` aliases `Result`/
  `RoleCapacities`/`RoleSpare` and silently mutates saturation's own entry (`da0e1ee8`);
  **`demand == 0` must flow through as `0`** (`77f21355`); `allocation.CompositeSignalName` and an
  extra `analyzer-result` log line already exist as precedent, though its `%` unit does not apply
  to us (ours is sat units).
- Key finding: the p3 "initial plan" (`compose-logic-plan.md`) justifies its `max RC` rule with
  post-CT6-normalization reasoning that does **not** hold on this base —
  `normalizeToCompositeUnits` is absent from upstream, and its parent-branch implementation has
  an unfixed `1/PRC` correctness bug. Spec §2.3 records this.
- `session-tracking` symlinks: **created** (`missions/composite-analyzer/{STATE.md,ledgers}`),
  verified resolving. Left **uncommitted** per user instruction — `policy-writer` commits
  `session-tracking`. No agentbus `pending-commits` note published yet (not authorized).

### Known issues

- none

## Session log
- 2026-09-08 session=2026-09-08-composite-analyzer-1 status=retired ledger=.session/ledger/2026-09-08-composite-analyzer-1.md
- 2026-09-08 session=2026-09-08-composite-analyzer-2 status=active ledger=.session/2026-09-08-composite-analyzer-2.md
