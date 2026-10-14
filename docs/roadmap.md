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

## Resilience

- **Learned capacity survives a restart.** What the controller learns about a
  model's capacity lives in memory, so a restart or a leader hand-off re-learns
  it, and a request shape it had already priced is sized from occupancy again.
  Planned: the leader writes its fitted state to a ConfigMap keyed on the engine's
  configuration, and a new leader recalls only entries that match and are fresh.
  [#137](https://github.com/ev-shindin/llm-scaling-manager/issues/137)

## Security

- **Mutual TLS on the scaler port by default.** On this release the scaler port
  has no TLS and no authentication by default; restrict it with a NetworkPolicy
  ([network exposure](reference/security-model.md#network-exposure)). Planned: the installer provisions the certificates
  (cert-manager when present, otherwise its own CA), wires every ScaledObject it
  generates or adopts, and pins KEDA's client identity.
  [#138](https://github.com/ev-shindin/llm-scaling-manager/issues/138)
