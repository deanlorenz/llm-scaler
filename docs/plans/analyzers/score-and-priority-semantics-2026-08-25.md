# Score and Priority semantics (2026-08-25)

## 1. `Score` / `AnalyzerScoreConfig` — what it is

`internal/config/saturation_scaling.go:215-223` defines `AnalyzerScoreConfig{Score float64
// yaml:"score,omitempty", default 1.0}`. Doc comment at :200-202: *"configures an individual
analyzer's weight in the composite scoring function."* `AnalyzerScore()` (:617-622) comment:
*"the per-analyzer weight used by GreedyByScoreOptimizer for fair-share priority ordering across
models."* `docs/developer-guide/multi-analyzer-pipeline.md:137`: *"Weight in the fair-share
priority formula."*

**Score = a per-ANALYZER weight (trust), applied within one model's multi-analyzer set, that
feeds into that model's fair-share value versus OTHER models. It is never a per-model field.**

## 2. `Priority` — what it is

`ModelScalingRequest.Priority` (`optimizer_interfaces.go:84`) traces to `config.ScalingPolicy.Priority`
(`internal/config/saturation_scaling.go:77-78`, doc: *"multiplier for this model's scaling
urgency... preferential GPU allocation in fair-share"*), set at `engine_v2.go:826`
(`Priority: config.Priority`). YAML field `priority`
(`docs/developer-guide/scaling-policy-config.md:236`: *"Multiplier for this model's scaling
urgency in fair-share GPU allocation"*). Used in `fairShareValue(req.Priority, ...)` as the outer
multiplier over the model's total analyzer-weighted demand, and separately in `rescale.go:94`
(`w := m.Priority * m.Demand`) for water-filling.

**Priority = the per-model/per-ScaledObject fairness weight across DIFFERENT models competing for
GPU budget — distinct from Score (per-analyzer weight within one model).**

## 3. Is `fsv` a sort key only, or a real quantity?

**Not sort-key-only — its magnitude is used arithmetically.** `fsv := fairShareValue(...)` →
`w.remaining` → (a) sort key, (b) averaged into `mean` (`computeMean`), (c)
`target := w.remaining - mean` is a **GPU-allocation budget in capacity units** that caps
`ps[i][role]` and feeds `fairShareCap := ceil(target / PerReplicaCapacity)`, which directly bounds
replica counts allocated that iteration. So `fsv`'s absolute value determines how many
GPUs/replicas a model gets relative to the mean, not merely its rank.

## 4. Does weighted-sum-across-analyzers feed actual per-variant allocation math today?

**No.** `composeAnalyzerResults` today just returns saturation's raw result unchanged (comment:
"until the real reduction/backfill semantics... are designed in a later task") — no averaging
occurs pre-optimizer. Where multiple `NamedAnalyzerResult` entries DO coexist downstream,
disagreement is resolved by **max/min, never weighted-sum-as-a-composite-quantity**:
- `roleBottleneckReplicas` = `max_i ceil(...)` (replica sizing)
- `roleAggRemaining` = `max_i state[i][role]` (remaining demand)
- `safeRemovalReplicasForRole` = `min_i` over live analyzers (scale-down, conservative)

The only two places `Score` acts as a genuine weight are `fairShareValue`'s
`Σ_i Score_i · Σ_role pickerState[i][role]` (a **per-model scalar** used for cross-model
GPU-budget sizing, NOT a per-variant replica count) and `sortVariantsForScaleDown`'s
`Σ_i Score_i·PRC_i[v]` tie-break (ordering only). **Neither feeds a weighted-average
replica/demand number into per-variant allocation math** — actual replica sizing always uses
max/min cross-analyzer, never Score-weighted blending.

## Conclusion — directly answering the user's worry

**Score-weighted composition is validated today only as a component of a cross-MODEL sort/budget
scalar (fair-share ranking between different ScaledObjects), never as a way to combine
disagreeing analyzers' per-model/per-variant replica-count estimates into one composite number.**
When analyzers disagree on replica count *within* a single model/SO, the existing code always
uses max (scale-up) or min (scale-down) — never a Score-weighted average. So: weighted sum is
confirmed sound for its actual, existing purpose (sorting/budgeting between models); it has never
been used, and should not be assumed sound, for combining disagreeing analyzer opinions into one
composite value within a model — that's a different problem with a different existing precedent
(max/min), not weighted averaging.
