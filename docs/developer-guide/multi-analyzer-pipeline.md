# Multi-Analyzer Pipeline (developer reference)

llm-scaling-manager's scaling engine runs multiple **analyzers**
in series each cycle. Each analyzer consumes the same per-replica metrics
and produces an `*domain.AnalyzerResult` carrying per-variant capacity,
model-level totals, and (for P/D disaggregated models) per-role capacity.
The engine post-step calibrates `RequiredCapacity` / `SpareCapacity` at
every scope using a uniform threshold formula. The optimizer reads a
per-analyzer slice (`[]NamedAnalyzerResult`) and decides scaling actions
over it via shared free functions in `internal/engines/allocation/`.

---

## Architecture

### Data flow per optimize cycle

```text
┌──────────────────────────────────────────────────────────┐
│ Config (config.ScalingPolicy per model/namespace)        │
│   Priority, Analyzers[]:                                 │
│     name, enabled, Score,                                │
│     ScaleUpThreshold, ScaleDownBoundary                  │
└──────────────────────────┬───────────────────────────────┘
                           │ engine reads per cycle
                           ▼
┌──────────────────────────────────────────────────────────┐
│ Engine: per-model preparation                            │
│   • BuildVariantStates (GPUsPerReplica per variant       │
│     from ScaleTarget / VA labels)                        │
│   • CollectSchedulerQueueMetrics (shared across          │
│     analyzers)                                           │
│   • cfg.AnalyzerThresholds(name) per analyzer           │
│     (per-analyzer override over model-level globals)     │
└──────────────────────────┬───────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────┐
│ Engine: run analyzers, build per-analyzer slice          │
│ Saturation (always first), then each registered          │
│ non-saturation analyzer in registration order:           │
│   • skip if Enabled:false                                │
│   • Analyze(ctx, input) → *AnalyzerResult                │
│   • buildCapacities(nr, ...) → supply from capacities,   │
│     then applyUniversalThreshold(nr, scaleUp, scaleDown) │
│     → writes RC/SC on nr at model + each role scope      │
│   • append NamedAnalyzerResult{                          │
│       Name, Result,                                      │
│       Score     ← config.Analyzers[name].Score,          │
│       Remaining ← RC,   Spare ← SC,                      │
│     } to []NamedAnalyzerResult                           │
└──────────────────────────┬───────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────┐
│ Engine: build ModelScalingRequest                        │
│   AnalyzerResults  ← per-analyzer slice (above)          │
│   VariantStates    ← prepared above                      │
│   Priority         ← config.Priority                     │
│   Disaggregated    ← any variant has a non-"both" Role   │
└──────────────────────────┬───────────────────────────────┘
                           │
                           ▼
┌──────────────────────────────────────────────────────────┐
│ Optimizer (CostAware or GreedyByScore)                   │
│   • initRoleState → RolePairedState + RoleSpare          │
│   • Scale-up: allocateForModelPaired                     │
│       pick(role) → variant; joint Δ_util commit          │
│       applyAllocation → decrement Remaining              │
│   • Scale-down: scaleDownRoleIterated                    │
│       needsScaleDownForRole → saturation spare, if live  │
│       safeRemovalReplicasForRole → saturation only       │
│       applyDeallocationForRole → decrement RoleSpare     │
└──────────────────────────┬───────────────────────────────┘
                           │
                           ▼
                       VariantDecisions
```

### Key concepts

| Concept | Definition |
|---|---|
| **Analyzer** | Implementation of `domain.Analyzer`. Built in: saturation (kv-token capacity, package `saturation`), which sizes the fleet, and `slo` (package `slo`, [its page](slo-analyzer.md)), which observes. External: throughput (RPS/ITL-derived). |
| **`VariantCapacity`** | Per-variant primitives: `ReplicaCount`, `PendingReplicas`, `PerReplicaCapacity` (analyzer-specific units), `Role`, `TotalDemand`, and the warm-pool trio `WarmPoolReplicas` / `WarmPoolPerReplicaCapacity` / `WarmPoolCapacity`. `Cost` and `AcceleratorName` are **not** on this struct — the optimizer reads them from `VariantMetadata`. |
| **`AnalyzerResult`** | Per-(model, analyzer) output: the pure (D, P) signal — `VariantCapacities[]`, `TotalDemand`, `RoleDemand`. It has **no** supply or scaling-signal fields; those live on `NamedAnalyzerResult` and are the builder's. |
| **`RoleCapacity`** | Per-role aggregate within an `AnalyzerResult`: `TotalSupply`, `TotalDemand`, `TotalAnticipatedSupply`, `RequiredCapacity` / `SpareCapacity` (engine-written). Used for P/D disaggregated models only. |
| **`NamedAnalyzerResult`** | Optimizer-side wrapper: `{Name, Result, Score, Remaining, Spare, RoleSpare, Live}`. Working `Remaining`/`Spare`/`RoleSpare` are decremented by helpers during allocation; `Result` is never mutated. `Live` is set by the engine each cycle and gates scale-down participation (see "How results combine"). |
| **Linearity invariant** | Adding *n* replicas of variant *v* reduces analyzer *i*'s working `Remaining` by exactly *n × PRC_i[v]*. Holds at model scope (non-disaggregated) and at role scope (disaggregated). |

### The three layers

Everything below follows from one split, and getting it wrong is the most common
mistake in this pipeline — logic written inside one analyzer is silently absent
from the other two.

| Layer | Owns | Where |
|---|---|---|
| **Collector** | Collecting metrics: which rows exist at all. Variant identity (the owner walk to the managed scaler), the Pod-state gate, collapsing a Pod's engine instances into one replica row, and the per-row flags `Ready` and `FromWarmPool`. | `internal/collector` |
| **Analyzers** | Analysis: the **measured** signal, and nothing else. Demand `D` (model-level and per role) and per-replica capacity `P`, per variant, in analyzer-specific units. | `internal/engines/analyzers/*` |
| **Builder** | Building demands and capacities: everything **derived**. Supply totals, utilization, per-role capacities, `RequiredCapacity`/`SpareCapacity`, and `WarmPoolCapacity`. | `buildCapacities`, `internal/engines/steadystate/engine_v2.go` |

`AnalyzerResult` has no supply fields at all, so an analyzer *cannot* write a
supply that contradicts its own (D, P) — the linearity invariant holds by
construction rather than by convention.

### Warm pool bridges: demand yes, supply no

A **bridge** is a warm pool Pod lent to a variant while it is short. It reports
metrics under the variant it serves, and the collector flags those rows
`FromWarmPool`.

- Its **demand counts**. The traffic is the variant's. Leave it out and demand
  reads lowest exactly while a bridge covers the shortfall, then reappears from
  nowhere when the Pod goes back.
- Its **capacity is never supply**. The Pod is borrowed; counting it would tell
  the optimizer the fleet is already big enough and suppress the scale-up the
  bridge exists to cover — after which the pool holds the Pod indefinitely,
  because the replicas that would release it were never created.
- **`P` is measured over the variant's own replicas only.** `ReplicaCount` is
  clamped to the scale target, which a warm pool Pod is not part of, so a `P`
  blended over a bridge would price the counted replicas at a figure none of them
  delivers. The readings genuinely differ: the pool runs its engines at a lower
  `--gpu-memory-utilization` than the workload (measured on an OpenShift cluster 2026-08-31:
  pool 0.90, workload 0.95), so less of the GPU is KV cache.

An analyzer that reads replica rows must therefore exclude `FromWarmPool` rows
from its per-replica maths. `saturation` splits them and reports the bridge's
own reading; `throughput` filters them via `ownReplicasOnly`; `external` needs
nothing, because its `P` is a constant target from config and it never reads
replica rows.

**Invariant: a variant is warmed by at most one pool.** `FromWarmPool` is a bool
rather than a pool name, and `decision.WarmPoolSupply` is keyed by variant alone,
both of which are correct only under that assumption — one pool means one
`--gpu-memory-utilization`, so a variant's bridges are comparable and a single
median over them means something.

Different variants of one model *may* sit in different pools, prefill and decode
included: a variant is named by its ScaledObject, role and all, and every figure
above is per variant, so they aggregate independently. What the invariant rules
out is one variant bridged by two pools at once — their bridges would carry
genuinely different capacities and the median would describe neither.

**The pool layer enforces it structurally**, so the accounting above is not
relying on operator discipline. A variant names at most one pool through the
`warmPool` trigger metadata key, and `warmpool.VariantsFor` hands a variant only
to the pool it names — or to the single pool, when the namespace has exactly one
and there is nothing to disambiguate. With several pools declared, a variant that
names none is *Unassignable*: it gets no warm copy and is reported, rather than
being warmed by whichever pool saw it first. Several pools per namespace is an
anticipated configuration, not a hazard — a warm copy is only reusable on the
accelerator it was loaded on, so a cluster with two GPU types needs one pool per
type.

### Responsibility table

| Field | Written by | Read by |
|---|---|---|
| Per-variant `ReplicaCount`, `PendingReplicas`, `PerReplicaCapacity` (own replicas only), `Role`, `TotalDemand` | Analyzer | Builder, then optimizer (picker + scaling math) |
| Per-variant `WarmPoolReplicas`, `WarmPoolPerReplicaCapacity` | Analyzer (measurements) | Builder |
| Per-variant `WarmPoolCapacity` | **Builder** — `WarmPoolReplicas × WarmPoolPerReplicaCapacity`; in no supply total. Analyzer-written values are overwritten | Retained-pool switching decision |
| Per-variant `Role`, and the `ReplicaCount` clamp to the scale target | **Builder** (from discovery) | Optimizer |
| Model-level `TotalSupply`, `TotalAnticipatedSupply` | **Builder** — not fields on `AnalyzerResult` at all | Engine post-step, optimizer |
| Per-role `RoleCapacities[role].Total*` | **Builder** (`buildRoleCapacities`), pairing analyzer `RoleDemand` with supply it groups by role | Engine post-step, optimizer |
| `RequiredCapacity`, `SpareCapacity` (model + role scope) | **Engine post-step only** — analyzer-written values are overwritten | Optimizer |
| `NamedAnalyzerResult.Remaining`, `Spare`, `RoleSpare` | Optimizer helpers (`applyAllocation`, `applyDeallocationForRole`) | Optimizer allocation loop |
| `NamedAnalyzerResult.Live` | Engine (`runAnalyzersAndScore`, each cycle) | Scale-down gate on saturation's entry (`needsScaleDownForRole`, `safeRemovalReplicasForRole`); other analyzers' liveness is recorded, not forwarded |

---

## Components

- **Registration** — `internal/engines/steadystate/engine.go`:
  `RegisterAnalyzer(name, analyzer) error`. `cmd/main.go` registers external
  analyzers (e.g., throughput) before `StartOptimizeLoop`. The saturation analyzer is
  pre-registered at slot 0, and the slo analyzer after it; an external analyzer
  named `slo` is not run. The registry is snapshotted at `StartOptimizeLoop`;
  late registration returns an error.
- **Engine post-step** — `internal/engines/steadystate/engine_v2.go`:
  `applyUniversalThreshold(*allocation.NamedAnalyzerResult, scaleUp, scaleDown)`,
  called from `buildCapacities`, applies the formula
  `RC = max(0, TotalDemand/scaleUp − TotalAnticipatedSupply)` /
  `SC = max(0, TotalSupply − TotalDemand/scaleDown)` at model scope and
  each role in `RoleCapacities`, writing them on the `NamedAnalyzerResult`.
- **Capacity-build step** — `internal/engines/steadystate/engine_v2.go`:
  `buildCapacities(ctx, *NamedAnalyzerResult, metaByVariant, scaleUp, scaleDown)`
  runs between the analyzers and the optimizer. It joins discovery identity,
  clamps `ReplicaCount` to the scale target, derives each variant's
  `WarmPoolCapacity`, assembles model and per-role supply, and applies the
  universal threshold. Every derived capacity in the pipeline is written here.
- **Aggregation helpers** — `internal/engines/aggregation/`:
  `SumTotalSupply`, `SumTotalAnticipatedSupply`, `SumTotalDemand`,
  `AggregateByRole` over `[]VariantCapacity`. Used by the **builder** to assemble
  per-scope totals without reimplementing the math; analyzers do not call them to
  publish supply, because they have nowhere to publish it to.
- **Optimizer slice flow** — `internal/engines/allocation/`:
  `NamedAnalyzerResult` slice carries each analyzer's calibrated result plus
  working scratch state for the allocation loop. `CostAwareOptimizer` and
  `GreedyByScoreOptimizer` consume the slice via shared free functions
  (single-variant, paired P/D, and role-iterated helpers).

---

## User configuration

Analyzers are configured via `config.ScalingPolicy.Analyzers` (YAML key
`analyzers`). Each entry is an `AnalyzerScoreConfig` struct
(`internal/config/saturation_scaling.go`):

| Field | Type | Default | Purpose |
|---|---|---|---|
| `name` | string | required | Must match the name returned by `Analyzer.Name()` |
| `enabled` | bool | true (when the entry is present) | Set false to disable without removing the analyzer |
| `score` | float64 | 1.0 | Weight in the fair-share priority formula |
| `scaleUpThreshold` | float64 | global | Overrides the model-level `scaleUpThreshold` for this analyzer |
| `scaleDownBoundary` | float64 | global | Overrides the model-level `scaleDownBoundary` for this analyzer |

Minimal YAML example:

```yaml
analyzers:
  - name: saturation
    score: 1.0
    scaleUpThreshold: 0.85
    scaleDownBoundary: 0.70
  - name: throughput
    enabled: false   # disable without removing
    score: 2.0
```

When `enabled` is false the analyzer is neither called nor included in the
result slice: it is not run at all.

**Participation is opt-in.** An analyzer registered in code
(`Engine.RegisterAnalyzer`) participates in a cycle only when it has an
explicit entry in `analyzers` with `enabled` `true` or unset. An analyzer with
no entry at all does not run and is not included in the result slice,
exactly as if `enabled: false` had been set. This prevents a
registered-but-unconfigured analyzer out of the cycle. Only saturation's
result reaches the optimizer today (see [How results combine](#how-results-combine)),
so no other analyzer can block scale-down either way. Saturation is exempt from this gate —
it is always run, independent of `analyzers` config, because the engine
identifies it by name before this check applies.

---

## Analyzer implementor guide

Implement `domain.Analyzer` (`internal/domain/analyzer.go`):

```go
type Analyzer interface {
    Name() string
    Analyze(ctx context.Context, input AnalyzerInput) (*AnalyzerResult, error)
}
```

### Input

Key `AnalyzerInput` fields:

| Field | Type | Description |
|---|---|---|
| `ModelID` | string | Model being analyzed |
| `Namespace` | string | Kubernetes namespace |
| `ReplicaMetrics` | `[]ReplicaMetrics` | Per-replica metric snapshots |
| `VariantStates` | `[]VariantReplicaState` | Current/desired/pending replica counts per variant |
| `Config` | `AnalyzerConfig` | Resolved config (cast to your config type as needed) |
| `SchedulerQueue` | `*SchedulerQueueMetrics` | Scheduler queue metrics; nil when flow control is off |
| `ArrivalRate` | float64 | Model-level request arrival rate (req/s), no per-pod labels; zero when EPP absent or no traffic yet |

### Output invariants

Emit the measured (D, P) signal and stop there. Concretely, fill in:

```go
result.VariantCapacities = []domain.VariantCapacity{ /* ReplicaCount, PendingReplicas,
    PerReplicaCapacity, Role, TotalDemand, and the WarmPool* measurements */ }
result.TotalDemand = /* model-level demand: yours to attribute */
result.RoleDemand  = /* per role, or nil when not disaggregated */
```

**There is nothing else to populate.** `AnalyzerResult` has no `TotalSupply`,
`TotalAnticipatedSupply`, `RoleCapacities`, `RequiredCapacity` or
`SpareCapacity` fields — the builder computes all of them on
`NamedAnalyzerResult`. This is what makes the **linearity invariant**
(`supply = Σ_v ReplicaCount × PerReplicaCapacity`, at model and role scope) hold
by construction: an analyzer has no way to state a supply that disagrees with its
own (D, P).

Two rules that follow from that invariant, and are easy to get wrong:

- **`P` must be measured over the same population `ReplicaCount` counts** — the
  variant's own replicas. The builder clamps that count to the scale target, so
  any row that is not part of the scale target (a warm pool bridge) must be kept
  out of the per-replica maths. See "Warm pool bridges" above.
- **`ReplicaCount` is what actually reported this cycle**, in scale-target units
  (pods, or LWS groups). The builder only ever lowers it.

Aggregation helpers (`aggregation.SumTotalSupply`, `SumTotalAnticipatedSupply`,
`AggregateByRole`) still exist, but they are the **builder's** tools, not the
analyzer's; an analyzer normally has no reason to call them.

---

## Pipeline flow

1. `cmd/main.go` calls `engine.RegisterAnalyzer(name, a)` for each external
   analyzer before `StartOptimizeLoop`. The saturation analyzer is pre-registered at
   slot 0.
2. `StartOptimizeLoop` snapshots the registry into `analyzersSnapshot`
   (frozen, race-safe). The snapshot is the ordered set of analyzers that
   every optimize cycle iterates.
3. Per cycle, for each model: `runAnalyzersAndScore` runs the saturation
   analyzer unconditionally (it drives variant metadata), then iterates
   `analyzersSnapshot` in registration order for non-saturation analyzers.
4. Analyzers with `Enabled: false` are skipped entirely — neither called nor
   appended to the result slice.
5. For each analyzer that runs, `applyUniversalThreshold` is applied to its
   result using resolved thresholds (per-analyzer override beats global):
   `RC = max(0, TotalDemand/scaleUp − TotalAnticipatedSupply)`,
   `SC = max(0, TotalSupply − TotalDemand/scaleDown)`.
6. Each result is wrapped in a `NamedAnalyzerResult{Name, Result, Score,
   Remaining, Spare}` and appended to the `[]NamedAnalyzerResult` slice.
   `Remaining = RC` and `Spare = SC` after the post-step.
7. Saturation is always first. Its `VariantCapacities` entries carry `Cost`,
   `AcceleratorName`, and `Role` used downstream by the optimizer and
   enforcer.

---

## How results combine

**The composite** (`buildComposite`, `steadystate/composite.go`): the
optimizer receives one result, built in `collectV2ModelRequest` from every
analyzer that ran. It starts as a copy of saturation's `AnalyzerResult` (every
field, maps and slices cloned) and changes the need, the demand that carries
it, the PRC of each variant another analyzer decided, and the holds:

- **Need.** `allocation.ResolveSO` takes, for each eligible analyzer
  (`allocation.Eligible`: live, informative, non-nil) that has the variant
  without `ReasonNoData`/`ReasonError`, `N = D[role] / PRC` from the
  analyzer's own demand and capacity (`allocation.TotalReplicas`), and keeps
  the largest finite, non-negative one. Saturation is eligible only when the
  policy lists it (`ScalingPolicy.SaturationListed`: listed and enabled, or no
  `analyzers` section at all), and `slo` never is: it only observes. `Reason`
  records the path: `C0-agree` (several), `C1-single`, `C4-no-signal` (none).
- **Pricing** (`priceRole`, per role). The role is priced in one analyzer's
  frame (`roleFrames`: saturation's, and one per other analyzer from
  `Decision.Needs`), whichever needs more replicas for the fleet the role runs:
  the lowest coverage, the sum over its variants of replicas / N (1 per variant
  when it runs none), saturation's on a tie. A variant an analyzer does not
  size is read at saturation's ratio to the variants it does. Mixing frames per
  variant asked for more than any analyzer, and taking the largest need among
  the others per variant let one analyzer's zero need veto another's. A
  VERBOSE `composite-pricing` line names the frame and every frame's coverage
  and demand.
  - *Saturation's frame:* its demand and PRCs stand. `HeldRoleDemand` is
    raised to the others' need in saturation's units,
    `D_sat x coverage_sat / coverage_others`, and dropped when that reaches
    `D_sat`: the hold no longer sets the demand, and kept, the share optimizer
    would give nothing from the role.
  - *The others' frame:* the role's demand is the largest `N x PRC_sat` over
    the variants they size, defined when `D_sat[role]` is zero, so another
    analyzer can grow a role saturation sees as idle. Each variant they size
    gets `PRC = D / N`, so it needs exactly `N`, and variants on different
    accelerators keep their view of the relative worth. A variant they do not
    size keeps saturation's PRC. The model's `TotalDemand` moves with the role
    (or is the role's demand, aggregated), the role's variants share it in
    saturation's proportions (evenly when it attributed none), their
    utilization follows, and `HeldRoleDemand` is dropped.
  - *Scale-down* is bounded by every frame (`boundSpare`): shedding X of the
    chosen frame's capacity sheds at most `X x r` of frame f's, `r` the largest
    `PRC_f / PRC_chosen` over the running variants, so the role's spare (the
    model's on an aggregated model) is capped at `spare_f / r` for each f.
    *Scale-up* follows the chosen frame alone: a variant it grows may leave
    another frame short for one cycle, until the new replicas are read.
  - With one variant per role every frame gives saturation's PRC. A need
    whose demand at saturation's PRC overflows is no reading. A need on a
    variant saturation has no PRC for is carried only in the others' frame;
    in saturation's it is logged as dropped.
- **Holds** (`compositeHolds`, `allocation.RoleHold`, per role). With
  saturation unlisted, a role none of whose variants has a need is held both
  ways, and on P/D every other role is held against growth too, as the share
  optimizer already froze such a model whole. A listed analyzer other than saturation that is live but not eligible
  -- inside its staleness grace with no reading, or returned nothing this
  cycle (`listedInGrace` adds an entry with no `Result`) -- blocks scale-down
  of the roles it reports variants for, or of every role when it reports
  none. `applyHolds` zeroes the forbidden `RequiredCapacity`/`SpareCapacity`
  (per role, and model-level when every role is held that way); the share
  optimizer reads `Holds` itself, freezing the model on `NoScaleUp` and
  raising the role's floor to what it has on `NoScaleDown`. Greedy's rescale
  pass (`rescaleModelDecisions`) also reads them: a role held both ways freezes
  the model there, as in the share optimizer, and `rescaleInputsForGroup` gives
  such a model exactly its current GPUs in the water-fill, so the rest of the
  budget goes to the others; no role is reclaimed or filled in a direction it
  is held against.
- **Warm-pool pressure** (`publishVariantPressure`) is published from the
  composite, after it is built, with saturation's scale-up threshold; a role
  held against growth never reports `NeedsScaleUp`. Warm-pool supply is still
  published from saturation's entry: the composite keeps saturation's PRC, so
  the two are the same.

The composite is judged against saturation's thresholds
(`AnalyzerThresholds(saturation)`), and recorded in the same
`recordAnalyzerMetrics` call as the analyzers, once a cycle, as analyzer
`CompositeSignal`. Its `Score` is the largest score among the analyzers that
contributed a need, saturation's when none did; the cross-model fair share
reads it. It is live when any analyzer contributed a need, and as live as
saturation otherwise. `allocation.CompositeHasSignal` is false when every variant
is `C4-no-signal`; the engine then sets the `no-composite-signal` blocked
reason (`reportSignalReason`), and clears it on every cycle that builds no
composite.

**Scale-down gate** (`needsScaleDownForRole`): a role scales down only when
the composite is live and its `RoleSpare` for the role is above zero;
`safeRemovalReplicasForRole` reads the same entry. Because the composite's
need is the largest analyzer need, scale-down requires every contributing
analyzer to agree. A version in which every live analyzer votes separately is
kept in `internal/engines/allocation/multi_backup/` behind `//go:build ignore`;
the "vote" and "veto" wording below describes that design.

**Liveness.** An analyzer is live for the current cycle iff it produced a
non-error, capacity-bearing result within the staleness window (a fixed
multiple of the optimization interval, `analyzerLivenessStaleCycles` in
`internal/engines/steadystate/engine_v2.go`). The resolved interval falls back
to a 30s default whenever `Config` is absent **or** reports a non-positive
value, so a misconfigured interval can never zero the staleness window and
latch every analyzer non-live. An informative result with a zero-valued
`AnalyzedAt` is treated as current (recorded as "now") rather than
instantly-stale, so a forgotten timestamp on a future analyzer cannot
silently disarm the veto. A non-live analyzer — one that
has never produced a usable result, is currently erroring, or whose last
usable result has aged past the staleness window — is excluded from the
scale-down vote entirely: it neither vetoes nor constrains the safe-removal
minimum. This prevents a registered-but-uninformative analyzer (no metrics
yet, an error state, or a stale result) from silently blocking scale-down
for every model it's registered against. Recovery is automatic: once the
analyzer produces a fresh capacity-bearing result, it becomes live again on
the next cycle. Liveness is tracked per model, not just per analyzer name,
so one model's freshness never masks another's staleness.

An analyzer reporting no usable capacity (`no-data`) does not become
non-live immediately — it becomes non-live only once its last informative
result ages out of the staleness window. This distinguishes three cases: an
analyzer that never had good data (e.g. a mislabelled metric at startup)
never sets its timestamp and is non-live from the start; a transient
no-data blip on an analyzer with a recent good result stays live and still
participates in the vote (the intended "uncertain, err toward not scaling
down" behavior); and an analyzer whose good data has aged past the window
becomes non-live. A mislabelled or broken metrics query is not treated as
an *error* — it still returns a well-formed result, just one with no usable
capacity — so this reason-based check, not an engine-level error signal, is
what actually detects a durably-broken analyzer.

Within the multi-analyzer engine path (`runAnalyzersAndScore`), this
liveness filter applies uniformly to every registered analyzer, including
saturation's own token-capacity signal — there is no name-based exemption
inside the scale-down gate. (Saturation's separate role as the shared
metrics-collection layer — cache size, replica cost, etc., feeding every
analyzer and the cost optimizer — is unaffected; that collection either
succeeds for everyone or, if it fails, every analyzer ends up non-live and
the safety floor below applies.)

Liveness reflects whether an analyzer has a current *capacity* (supply-side)
signal — it does not gate on the *demand* signal. A falsely-low demand value
only biases toward scale-down, never toward a spurious veto, so it never
affects the veto gate; demand robustness is handled upstream by other
mechanisms (metric sanity checks on calibration inputs, request-rate /
local-demand fallbacks).

**Demand-liveness telemetry (warn-only).** As an observability aid, the engine
separately watches for the throughput analyzer having a live capacity signal
while reporting no demand (`TotalDemand == 0`) for at least the staleness
window. This usually means the request-arrival query is misconfigured or EPP
is not reporting arrivals — supply is being measured but no load is observed,
so scale-up will never trigger. When detected, the engine logs a warning; it
never sets `Live`, never touches `RoleSpare`, and never gates any scaling
decision. The signal is a timestamp gap rather than a boolean so a cold-start
scrape lag (supply resolving a cycle or two before the first arrival scrape)
does not false-positive: the gap only reaches the staleness window after
demand has genuinely been absent for that long.

**Safety floor.** If every analyzer in the slice is non-live for a role,
`needsScaleDownForRole` returns false rather than falling through to "no
vetoes, so scale down" — with zero live analyzers there is no current basis
to scale down. This also makes leader failover safe: a freshly-elected
leader starts with no liveness history, so scale-down for every role is
withheld until at least one analyzer produces a fresh result (typically
within a cycle or two).

**Scale-up gate** (`anyRoleNeedsScaleUp`): ANY analyzer having `Remaining > 0`
triggers scale-up for the corresponding role. The liveness gate does not
apply to scale-up — a non-live analyzer contributes `Remaining == 0`
(from `RequiredCapacity == 0`), which is already harmless to the max-across-
analyzers formula.

The saturation entry in the slice is also the keeper of per-variant metadata
(`Cost`, `AcceleratorName`, `Role`) that the optimizer reads from
`VariantCapacities`. Future work will extract per-variant metadata collection
out of the saturation result so each analyzer owns only its own signals.

---

## Data model: AnalyzerResult → NamedAnalyzerResult

Understanding what transforms where prevents the most common mistake: treating
`Result.*` counters as live state during allocation.

**`domain.AnalyzerResult`** is the immutable record an analyzer returns.
The engine owns its calibration:

1. The analyzer populates `VariantCapacities[]`, `TotalDemand` and, for P/D
   models, `RoleDemand`. `AnalyzerResult` has no supply or capacity-signal
   fields: the analyzer cannot publish supply, `RequiredCapacity` or
   `SpareCapacity`.
2. The engine wraps the result in a `NamedAnalyzerResult`, and
   `buildCapacities` derives `TotalSupply`, `TotalAnticipatedSupply` and
   `RoleCapacities` on it from `VariantCapacities`, then calls
   `applyUniversalThreshold`, which writes `RequiredCapacity` / `SpareCapacity`
   on the `NamedAnalyzerResult` at model scope and on each
   `RoleCapacities[role]`.
3. `Result` is never mutated. `Result.*` values are stable read-only data for
   the rest of the cycle.

**`allocation.NamedAnalyzerResult`** is the working unit the optimizer operates on.
Its fields fall into three categories:

| Field | Category | Description |
|---|---|---|
| `Name`, `Score`, `Result` | Immutable | Set by engine; never written by optimizer |
| `Remaining`, `Spare` | Mutable scalars | Model-scope working counters; decremented by `applyAllocation` during scale-up |
| `RoleSpare` | Mutable per-role map | Populated by `initRoleState`; decremented by `applyDeallocationForRole` during scale-down |

`Remaining` and `Spare` are seeded from `Result.RequiredCapacity` and
`Result.SpareCapacity`. `RoleSpare` is seeded from
`Result.RoleCapacities[role].SpareCapacity`. None of this flows back into
`Result`.

**`RolePairedState`** (`[]map[string]float64`, indexed as
`[analyzer-index][role]`) is picker-local demand created per call to
`initRoleState`. It holds per-role required capacity for the scale-up loop and
is decremented by the joint-commit step inside `allocateForModelPaired`. It is
not stored on `NamedAnalyzerResult` and is discarded after each model's
allocation pass.

---

## Optimizer internals and helper composition

Both optimizers share the same allocation and scale-down primitives from
`internal/engines/allocation/analyzer_helpers.go` and
`internal/engines/allocation/cost_aware_optimizer.go`. The optimizers own the
*when* and *which model*; the helpers own the *how*.

### Scale-up path

All scale-up goes through `allocateForModelPaired`:

```text
initRoleState(s)               → roles, RolePairedState (per-role demand + RoleSpare)
anyRoleNeedsScaleUp(ps, roles) → loop gate: any role still has demand?
  pick(role, ...)              → (variant, capN): optimizer-specific variant selector
  roleBottleneckReplicas       → max_i ceil(state[i][role] / PRC_i[v]): cross-analyzer replica sizing
  roleAggRemaining             → max demand across analyzers for this role
  Δ_util = min_role util_role  → joint commit bound: trim to the least-served role
  applyAllocation(s, v, k)     → decrement Remaining on all NamedAnalyzerResults
```

`pick` is a `RolePickFn` — the only part that differs between optimizers:

- `costGreedyRolePick`: picks the cheapest cost-efficient variant; no GPU budget
  cap (no limiter declared).
- `fairShareRolePick`: picks the cheapest variant within available GPU budget;
  caps `capN` to the fair-share target (a limiter declared).

For non-disaggregated models, `initRoleState` synthesizes a single `"both"` role
from the model-level scalars, so `allocateForModelPaired` handles both the
disaggregated and non-disaggregated cases through the same loop.

### Scale-down path

Both optimizers call `scaleDownRoleIterated`, which handles both disaggregated
and non-disaggregated models through the same role loop (`"both"` is the
synthetic role for non-disaggregated):

```text
for each role (sorted for determinism):
  needsScaleDownForRole(s, role)           → gate: ALL live analyzers have RoleSpare > 0
                                              (no live analyzer → false; see "How results combine")
  sortVariantsForScaleDown(s, vcs)         → cost-desc; tie-break: Score-weighted PRC asc
  scaleDownVariantSet(...)
    safeRemovalReplicasForRole(s, v, role) → min over live i of floor(RoleSpare[i][role] / PRC_i[v])
    applyDeallocationForRole(s, v, role, n)→ decrement RoleSpare on all entries
```

`sortVariantsForScaleDown` uses a Score-weighted PRC tie-break. With a single
analyzer (Score=1) this reduces to plain cost-descending / PRC-ascending order.

### Fair-share iteration (GreedyByScoreOptimizer only)

`fairShareScaleUp` uses iterative mean equalization rather than fixed fractions:

1. Compute `mean` = average `remaining` (fair-share priority value) across active
   models.
2. Sort by `remaining` descending; take the highest.
3. Call `allocateForModel` with budget `target = remaining − mean`: allocates
   replicas via `allocateForModelPaired` until the model's priority value drops
   to or below `mean`.
4. Recompute `remaining = fairShareValue(priority, s, ps, roles)` from the
   post-allocation working state.
5. Repeat until no active models remain or no GPUs are left.

`fairShareValue = priority × Σᵢ Score_i × Σ_role pickerState[i][role]`.
A higher `Score` on a high-demand analyzer increases a model's priority value
and therefore how many GPUs it attracts in a constrained environment.

---

## Optimizer consumption

The `[]NamedAnalyzerResult` slice is passed to one of two optimizers depending
on whether the `config.ScalingPolicy` declares any `limiters:` entry:

- **`CostAwareOptimizer`** (no limiters declared): operates
  on the saturation entry's `VariantCapacities` for cost and role data; scales
  up the cheapest variant that covers the required capacity, scales down the
  most expensive variant with spare capacity.
- **`GreedyByScoreOptimizer`** (a limiter declared): respects
  `ResourceConstraints` (GPU budgets per accelerator type). Models are ordered
  by fair-share priority value:
  `fsv = Priority × Σᵢ Score_i × Σ_role pickerState[i][role]`,
  where the sum over `i` runs across all `NamedAnalyzerResult` entries and
  `pickerState` is seeded from each entry's `Remaining`. Higher `Score` on a
  high-demand analyzer increases a model's allocation priority in constrained
  environments.

Both optimizers are stateless and selected per-cycle from the engine's
`optimizer` field.

## Observability

The engine emits two structured INFO log lines per reconcile cycle per model —
one per analyzer (after the threshold post-step) and one after the optimizer
returns. See [cycle-log.md](../reference/cycle-log.md) for field schemas, grep patterns,
and an explanation of the `reason` values set by each analyzer.
