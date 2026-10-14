# Roadmap

Planned work that changes what an operator can rely on. Each item links the
GitHub issue that tracks it; the issue has the detail and the current state.
Gaps that are documented but not yet planned are listed in
[Known gaps](why.md#known-gaps).

## GPU budgets

- **Kueue borrowing.** The Kueue quota source bounds a namespace by its
  ClusterQueue's nominal quota and does not count capacity the queue could
  borrow from its cohort. In a cohort that lends quota, a model is held at its
  nominal share even when Kueue would admit more from borrowed capacity.
  Planned: count borrowable capacity, opt-in, and prefer releasing borrowed
  replicas first.
  [#136](https://github.com/ev-shindin/llm-scaling-manager/issues/136)
  · today: [Kueue quotas](well-lit-paths/kueue-bounded-quotas/)
