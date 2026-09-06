# Engine-side analyzer reduce: design notes (2026-08-30)

**Context.** `composeAnalyzerResults` in `internal/engines/steadystate/engine_v2.go` is
currently a stub — it returns saturation's result unchanged. This doc plans the real reduce
for when more than one analyzer is enabled.

---

## The invariant

> Never produce a composite result with less coverage than saturation alone would give.

Saturation is the floor. A composite must be at least as aggressive on scale-up, and at
least as conservative on scale-down, as sat's result would have been alone.

---

## What each analyzer produces

Each analyzer returns a `*domain.AnalyzerResult` containing:

| Field | What it means |
|---|---|
| `TotalDemand` | Model-level demand, in **this analyzer's own units** |
| `RoleDemand` | Per-role demand, same units (nil for non-disaggregated models) |
| `VariantCapacities[]` | Per-variant: `PerReplicaCapacity` (PRC), `ReplicaCount`, `TotalDemand` |

**Units differ across analyzers.** Saturation works in tokens; a throughput analyzer in
tokens/sec; an SLO analyzer in latency-capacity. `TotalDemand` from two different analyzers
cannot be added or averaged — the numbers are not in the same currency.

---

## What the capacity builder needs

`buildNamedResult` (called once, after compose) needs a single `*domain.AnalyzerResult` to
work from. It reads:

1. `VariantCapacities[v].PerReplicaCapacity` — to compute supply and replica targets.
2. `TotalDemand` — to compute model-level `RequiredCapacity` and `SpareCapacity`.
3. `RoleDemand` — to compute per-role RC/SC for P/D models.

**Saturation always owns PRC.** It is the only analyzer that reliably produces a meaningful
PRC in pod-replica units. The comment at engine_v2.go:100 states this explicitly. A non-sat
analyzer's PRC is in incommensurable units and must not replace sat's.

---

## The correct reduce operation

Since units differ, the common currency is **implied replica count**:

```
implied_replicas(analyzer_i, variant_v) = ceil( demand_i[v] / PRC_i[v] )
```

This is unit-free — it is the number of replicas analyzer_i says are needed for variant v.

**For each variant**, the composite takes the maximum implied replica count across all
informative analyzers, then back-converts to saturation's demand units:

```
max_replicas[v] = max over informative analyzers of implied_replicas(i, v)
composite_demand[v] = max_replicas[v] × sat.PRC[v]
```

Because saturation is always included in the max, `composite_demand[v] >= sat_demand[v]`
always — the floor invariant holds by construction.

The model-level `TotalDemand` for the composite is then:

```
composite.TotalDemand = max over variants of composite_demand[v]
```

(Same logic as how the capacity builder currently derives model-level RC from per-variant
capacity — it takes the most demanding variant's signal.)

**For disaggregated (P/D) models**, the same logic applies per role using `RoleDemand`.

---

## Sat-only fast path

When saturation is the only entry — the current default and only production case — the
reduce returns sat's result unchanged. Zero behavioural change from today.

---

## Open questions

These need answers before any code is written.

### Q1 — `TotalDemand` consistency

The capacity builder uses `TotalDemand` at two levels:
- **Model level** (`AnalyzerResult.TotalDemand`): for model-level RC/SC.
- **Per-variant level** (`VariantCapacity.TotalDemand`): for per-variant utilization display.

If we recompute model-level `TotalDemand` as `max over variants`, the per-variant
`TotalDemand` values on the composite also need to be updated (set to
`max_replicas[v] × sat.PRC[v]`). Otherwise utilization figures will be inconsistent.

**Question:** is that the right approach, or should per-variant `TotalDemand` stay as sat's
(for display/diagnostic purposes) while only model-level `TotalDemand` is raised?

### Q2 — `RoleDemand` when non-sat doesn't emit it

For disaggregated models, sat emits `RoleDemand` keyed by role. A non-sat analyzer might
emit only a model-level `TotalDemand` with no `RoleDemand`. In that case:

- Does the non-sat analyzer's demand contribute to the per-role reduce at all?
- Or is it treated as "no per-role signal" and only affects the model-level path?

**Question:** if a non-sat analyzer has no `RoleDemand`, does it participate in the
per-role max, and if so how?

### Q3 — Name of the composite result

The composed entry's name propagates to `CompositeSignal.Name`. There is one downstream
consumer that checks this name: `isSaturationResult` at engine_v2.go:747, used to decide
whether to apply a specific rescale path.

If we name the composite `"composite"`, that check would fail when non-sat analyzers are
active, potentially bypassing the rescale path unintentionally.

**Question:** should the composite keep the name `"saturation"` when sat is the floor? Or
should `isSaturationResult` be updated to check something other than the name (e.g. whether
sat's result was the floor contributor)?

### Q4 — Score for the composite

`Score` (the fair-share weight) is set on `NamedAnalyzerResult` by `buildNamedResult` from
`AnalyzerScoreConfig`. With a composite, what score should it carry? Options:
- Saturation's score (simplest, preserves today's behaviour).
- Max score across contributing analyzers.
- A fixed value (e.g. 1.0) since the composite is already a unified signal.

**Question:** what should the composite's Score be?

---

## Not in scope here

- Changing PRC ownership (saturation owns PRC; that stays).
- Per-analyzer threshold policy (composite inherits sat's thresholds for now).
- The CT4 fairness-definition question — orthogonal to this.
