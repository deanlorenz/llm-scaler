# Well-lit paths

A well-lit path is a recipe for one scenario that is documented, tested and
benchmarked — the term is llm-d's, and the bar is theirs. Not every path here
clears all three yet: the **Benchmarked** column says which have a measured
run, and each page names the end-to-end suites and the benchmark scenario that
back it, by filename, so you can check the claim rather than take it.

Start from the problem you have. Every path opens with a **Start here**
block: its status, what you need, the guide steps to follow in order, the one
check that proves it works, and how to undo it.

| If this is your problem | Take this path | You need | Start with | Status | Benchmarked |
| --- | --- | --- | --- | --- | --- |
| Replica counts are set by hand, or by a CPU threshold that has nothing to do with serving | [Scale a model on saturation](scale-on-saturation/) | model servers already serving; Prometheus; the EPP's flow-control gate; a cluster admin for KEDA and `make setup-prereqs` | [Install in a namespace](../guides/install-in-namespace/) (namespace owner, after the admin's step 1) | Stable | one historical single-run study against a KEDA threshold (July 2026, older controller); none on this engine |
| A model sits idle for hours holding accelerators | [Scale to zero, and get back](scale-to-zero/) | the saturation path; flow control live in the running EPP | [Scale to zero](../guides/scale-to-zero/) (namespace owner, in the default install) | Stable | no |
| Scale-ups are too slow: by the time a replica loads, the spike is over | [Bridge a scale-up with a warm pool](warm-pool-bridge/) | the saturation path; GPUs for the pool; a shared model cache | [Warm pool](../guides/warm-pool/) (namespace owner) | Stable | [one run per arm](warm-pool-bridge/measured.md) |
| Several models are too large to start on demand, and there are not GPUs for all of them | [Hold several large models on one set of GPUs](retained-pool/) | as the warm pool, sized for the largest model | [Warm pool: retained pools](../guides/warm-pool/multi-pool.md#retained-pools-which-model-holds-the-gpus) (namespace owner) | **Experimental** | scenario only |
| Autoscaling can ask for more GPUs than the cluster has | [Bound a fleet by real GPUs](bound-by-gpus/) | a cluster admin; a controller from the saturation path | [Bound every install by real GPUs](../guides/admin-gpu-bounding/) (cluster admin) | Stable | no |
| Several teams share accelerators and each has been promised a number | [Cap what each tenant may take](tenant-gpu-quotas/) | a cluster admin, for a budget the tenant cannot edit | [check accelerators resolve](tenant-gpu-quotas/#start-here) (its step 1) | Stable | no |
| The tenant numbers already live in Kueue, and you do not want a second copy | [Bound tenants by the quotas Kueue already holds](kueue-bounded-quotas/) | the tenant-quotas path; Kueue's CRDs and LocalQueues | no guide yet: [the path's steps](kueue-bounded-quotas/#start-here), starting with accelerators that resolve | **Experimental** | no |
| Several models share one quota and peak at different times, but the quota goes to whichever scaled first | [Share one GPU quota between models](utilization-share/) | two or more models under one limiter | [Utilization share](../guides/utilization-share/) (whoever owns the policy) | **Experimental** | [single runs per arm](utilization-share/measured.md) |
| Interactive and batch workloads need different urgency, without per-model config | [Give classes of workloads different scaling behaviour](workload-classes/) | the saturation path; edit rights on the policy ConfigMap | no guide: [the path's steps](workload-classes/#start-here) | Stable | no |
| One model, two accelerator types, and you want the cost-efficient one first | [Serve one model on two accelerator variants](accelerator-variants/) | the saturation path; both deployments behind one EPP | [Register the workloads](../guides/install-in-namespace/README.md#4-register-the-workloads) (namespace owner) | Stable | a scenario and a written-up run, varying tensor parallelism on one GPU type |
| Prefill and decode have different shapes and you want them scaled apart | [Scale a P/D-disaggregated model](pd-disaggregation/) | an llm-d P/D deployment with role labels | [Register the workloads](../guides/install-in-namespace/README.md#4-register-the-workloads) (namespace owner); no guide deploys P/D | **Experimental** | [run twice, a day apart](pd-disaggregation/#measured) |

Everything here except the saturation path assumes the scaling manager is
installed and a workload is registered. If it is not, start at
[Install the scaling manager in a namespace](../guides/install-in-namespace/) —
the other paths pick up after it.

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
