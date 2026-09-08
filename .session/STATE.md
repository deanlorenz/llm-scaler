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

- **Plan / spec:** `.session/spec.md` — mission spec, DRAFT v7.
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
  - Branch base is `upstream/main` @ `4db060e2` (rebased 2026-09-08 with user approval; the
    original base `778a8893` went stale within hours — upstream is actively moving). Do not
    rebase again without user approval. The pre-rebase tip `b4549217` is preserved in the reflog at
    `composite-analyzer@{1}`. When checking "have I changed anything", use
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
- [x] Rebase onto current `upstream/main` (`4db060e2`)
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
- [ ] **Answer the 2 remaining questions in §10** + veto pass on the 12 confirmations
- [ ] Implement
- [ ] Implement (post-approval): `Agg_N` + derivation chain in/beside
      `internal/engines/aggregation/`, query API, composite naming + quota-guard repair,
      observability audit, 30-case test plan

**Last completed:** `.session/spec.md` **v7** — D1 reversed (Score deferred, `Agg_N` is a pure
`max`), D3 scoped to four derivation categories, and the zero/absent-signal survey delivered.

**Errors review #3 caught, for context on how much to trust the current draft:** (1) `N` and
coverage are the same quantity (`cov = 1/N`) — v3 computed both and called it a cross-check;
(2) the composite's construction was inverted — demand is unchanged, `N` carries the signal;
(3) saturation is a fallback, **not a floor** — the CT7 "floor invariant" was carried unexamined
through three drafts; (4) there is no single-model request-shape assumption — safety is structural;
(5) aggregator names encoded the operation (`max…`) instead of the quantity (`Agg_N`).

**Next step / resume point:** two questions, then a veto pass, then implementation.
1. **§10/D2** — should a composite `C4-no-signal` also surface on `wva_model_scaling_blocked` as a new
   policy-owned reason? It genuinely is a "scaling is blocked" condition and the existing dashboard
   would then answer it, but it adds a reason to a set another engine also writes (the per-owner split
   exists to stop two producers clearing each other's series). My inclination: yes, policy-owned.
2. **§10/D3** — are all **four** derivation categories in this mission (rounding/`ceil()`,
   demand→replicas→GPUs, PRC/demand lookup, bounds), or only the first two? Recommend a two-step
   delivery, first two first, since that is where a semantic inconsistency actually changes a replica
   count.
3. Veto pass on §10/D4's 12 confirmations.

Do not start implementation before approval.

**Standing instruction from review #4:** **[USER]** "Always ask me if not sure." Do not infer intent
from examples or fill gaps with invented premises — ask.

### Status

- Environment: **ready** — worktree on branch `composite-analyzer`, rebased onto `upstream/main`
  @ `4db060e2`; 0 commits behind upstream, 1 ahead (the `.session/` commit). Only diff vs
  `upstream/main` is `.session/`. `.session/` tracked and committed, not gitignored;
  `resume-mission` + `wind-down` symlinks verified resolving into
  `session-tracking/claude-skills/`; `git status` clean.
- Session is **pinned** into this worktree via `EnterWorktree` — cross-worktree reads must use
  `cat <full-path>` or `git show <branch>:<path>`; `git -C` and `cd` elsewhere are blocked.
- Mission definition: **done** — see Orientation. Normalization deferred; sat units for now.
- Spec: **DRAFT v7**, `.session/spec.md` (1194 lines). **D1, D2, D3 all decided.** Survey delivered:
  `.session/survey-zero-signal.md`. Remaining: **2 questions** (§10) + a veto pass on the 12
  confirmations. Then implementation can start.
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
