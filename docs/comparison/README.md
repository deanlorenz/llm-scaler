# Comparisons

How the scaling manager's autoscaling compares with the other systems in this space. Written to
be checkable: each claim is about a named mechanism, not a marketing position,
and where a competitor's decision logic is closed the comparison says so rather
than guessing.

- **[KEDA alone](keda-alone.md)** — the one most operators weigh first: llm-d's
  own KEDA recipes against llm-scaling-manager, need by need, with a July 2026
  measured study and what has changed since.
- **[NVIDIA Dynamo, Mooncake and SGLang](autoscaling-nvidia-mooncake-sglang-vs-wva.md)**
  — the detailed comparison. Dynamo's Planner is the only true peer autoscaler
  of the three, and the only one with forecasting; Mooncake is a load-shedding
  scheduler rather than an autoscaler; SGLang ships no native autoscaler and is
  driven by external HPA/KEDA rules.
- **[Dynamo, Fireworks and Together, on one page](autoscaling-one-pager-dynamo-fireworks-together.md)**
  — the same ground against the hosted platforms, compressed: which signals each
  one scales on, and how many of them you are allowed to use at once.

The Dynamo, Mooncake and SGLang pages benchmark nothing. The only measured
head-to-head is against KEDA thresholds, from July 2026, on an older controller
and recipe; [KEDA alone](keda-alone.md#measured-against-keda-alone) says what
still holds.

The scaling manager is an advanced fork of llm-d's
workload-variant-autoscaler (now [llm-d-autoscaling](https://github.com/llm-d/llm-d-autoscaling), where it is deprecated in favour of KEDA recipes),
developed independently; these pages compare mechanisms, not the two projects.
