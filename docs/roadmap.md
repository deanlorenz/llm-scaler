# Roadmap

Planned work that changes what an operator can rely on. Each item links the
GitHub issue or PR that tracks it, with the detail and the current state. The
gaps themselves are summarized in [known gaps](why.md#known-gaps).

## GPU budgets

- **Kueue borrowing follow-ups.** Borrowing is counted today, on a flat
  cohort, from lenders Kueue would reclaim for. Planned: nested cohorts, Kueue
  Fair Sharing, `effectiveQuotas`, new borrowing under the utilization-share
  optimizer, and an opt-in to count and hand back the capacity of lenders that
  do not reclaim.
  [#136](https://github.com/ev-shindin/llm-scaling-manager/issues/136)
  · today: [Kueue quotas](well-lit-paths/kueue-bounded-quotas/), [the guide](guides/kueue-borrowing/)

## Scaling

- **Scale to a latency target.** Tiers set how full a replica may run, not a
  TTFT or TPOT target; an operator with an SLO translates it into thresholds by
  hand. A tier can already declare a latency target and see it judged
  ([observe only](reference/scaling-policy.md#slo-observe-only)). Planned: the
  controller derives the fill that meets it from measured latency, keeping
  variants, P/D pairing and the GPU budget.
  [#141](https://github.com/ev-shindin/llm-scaling-manager/issues/141)

- **Price prefill from the router's prompt tokens.** The router knows the
  prompt tokens in flight before an engine sees them; today that signal feeds
  only shape-change holds. Planned: use it as a prefill demand input, priced by
  the throughput the controller already learns, so prefill capacity is ordered
  before engine queues fill.
  [#144](https://github.com/ev-shindin/llm-scaling-manager/issues/144)

## Resilience

- **Make surviving a restart the default.** By default what the
  controller learns about a model's capacity lives in memory, so a restart or a
  leader hand-off re-learns it, and a request shape it had already priced is
  sized from occupancy again. `LEARNED_STATE_SNAPSHOT=true` keeps it in the
  ConfigMap `wva-learned-state` and restores only figures learned under the
  same engine configuration; it is off by default
  ([learned state across a restart](reference/configuration.md#learned-state-across-a-restart)).
  Planned: decide whether it becomes the default, and what has to land first.
  [#227](https://github.com/ev-shindin/llm-scaling-manager/issues/227)

## Evidence

- **Prove variant choice, and measure more.** An end-to-end spec that the
  cheaper variant grows, a benchmark across two GPU types, repeated runs with
  their spread, larger models, and a head-to-head with llm-d's recipes on their
  own published setups.
  [#142](https://github.com/ev-shindin/llm-scaling-manager/issues/142)
- **Nightly end-to-end on real GPUs.** CI runs the suites on kind with a
  simulator; a scheduled run on a GPU cluster would catch real-engine
  regressions (metrics, sleep and wake, drain, P/D transfer).
  [#143](https://github.com/ev-shindin/llm-scaling-manager/issues/143)
