# Well-lit paths

A well-lit path is a recipe for one scenario that is documented, tested and
benchmarked — the term is llm-d's, and the bar is theirs. Not every path here
clears all three yet: the **Benchmarked** column says which have a measured
run, and each page names the end-to-end suites and the benchmark scenario that
back it, by filename, so you can check the claim rather than take it.

Start from the problem you have.

| If this is your problem | Take this path | Status | Benchmarked |
| --- | --- | --- | --- |
| Replica counts are set by hand, or by a CPU threshold that has nothing to do with serving | [Scale a model on saturation](scale-on-saturation/) | Stable | scenario only; no run on this engine, none against plain KEDA |
| A model sits idle for hours holding accelerators | [Scale to zero, and get back](scale-to-zero/) | Stable | no |
| Scale-ups are too slow: by the time a replica loads, the spike is over | [Bridge a scale-up with a warm pool](warm-pool-bridge/) | Stable | [one run per arm](warm-pool-bridge/measured.md) |
| Several models are too large to start on demand, and there are not GPUs for all of them | [Hold several large models on one set of GPUs](retained-pool/) | **Experimental** | scenario only |
| Autoscaling can ask for more GPUs than the cluster has | [Bound a fleet by real GPUs](bound-by-gpus/) | Stable | no |
| Several teams share accelerators and each has been promised a number | [Cap what each tenant may take](tenant-gpu-quotas/) | Stable | no |
| The tenant numbers already live in Kueue, and you do not want a second copy | [Bound tenants by the quotas Kueue already holds](kueue-bounded-quotas/) | **Experimental** | no |
| Several models share one quota and peak at different times, but the quota goes to whichever scaled first | [Share one GPU quota between models](utilization-share/) | **Experimental** | [single runs per arm](utilization-share/measured.md) |
| Interactive and batch workloads need different urgency, without per-model config | [Give classes of workloads different scaling behaviour](workload-classes/) | Stable | no |
| One model, two accelerator types, and you want the cost-efficient one first | [Serve one model on two accelerator variants](accelerator-variants/) | Stable | a scenario and a written-up run, varying tensor parallelism on one GPU type |
| Prefill and decode have different shapes and you want them scaled apart | [Scale a P/D-disaggregated model](pd-disaggregation/) | **Experimental** | [run twice, a day apart](pd-disaggregation/#measured) |

Everything here assumes the scaling manager is installed and a workload is registered. If it is
not, start at [Install the scaling manager in a namespace](../guides/install-in-namespace/) —
the paths pick up after it.

They also assume a new replica starts as fast as it can: seconds lost on the
way to Ready come back as over-ordered replicas. Read the start-path checklist
in [Preparing a workload](../reference/workload-preparation.md#the-rest-of-the-start-path)
(image pull, weights on the node's disk) before the path you take.

## How these differ from the guides

A **guide** ([guides/](../guides/)) is the steps: run these commands, in this
order, and check this at the end. A **path** is the decision above the steps —
what the scenario buys you, what it costs, when not to take it, and what
evidence exists that it works. Paths link down into guides; they do not repeat
them.

## What "experimental" means

The recipe may still change shape: the defaults are not settled, or the design
is still moving. Each experimental page says which leg is short.

**Stable** does not mean fully covered by CI. Every page's *How it is tested*
section names its suites and states what is **not** covered; read it before
depending on a path. The evidence behind each path, side by side:
[how mature each part is](../why.md#how-mature-each-part-is).
