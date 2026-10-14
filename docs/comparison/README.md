# Comparisons

How the scaling manager's autoscaling compares with the other systems in this space. Written to
be checkable: each claim is about a named mechanism, not a marketing position,
and where a competitor's decision logic is closed the comparison says so rather
than guessing.

- **[NVIDIA Dynamo, Mooncake and SGLang](autoscaling-nvidia-mooncake-sglang-vs-wva.md)**
  — the detailed comparison. Dynamo's Planner is the only true peer autoscaler
  of the three, and the only one with forecasting; Mooncake is a load-shedding
  scheduler rather than an autoscaler; SGLang ships no native autoscaler and is
  driven by external HPA/KEDA rules.
- **[Dynamo, Fireworks and Together, on one page](autoscaling-one-pager-dynamo-fireworks-together.md)**
  — the same ground against the hosted platforms, compressed: which signals each
  one scales on, and how many of them you are allowed to use at once.

Neither page benchmarks anything, and nothing here measures the scaling manager
against **plain KEDA or HPA thresholds** on the same engine: that head-to-head
has not been run. The case against a threshold on one metric is argued, not
measured, in [Why llm-scaling-manager](../why.md#against-the-alternatives).

The scaling manager is an advanced fork of llm-d's
[workload-variant-autoscaler](https://github.com/llm-d/llm-d-workload-variant-autoscaler),
developed independently; these pages compare mechanisms, not the two projects.
