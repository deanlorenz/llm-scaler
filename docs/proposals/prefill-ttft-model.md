# Prefill capacity from a fitted TTFT model

## Why

Prefill is sized in the wrong units and has no way to know its own capacity
without first being overloaded. Both are measurable, and both were measured on
2026-09-29 (run PK, the `1k/6000 -> 30k/250` trace).

**Adding replicas did not add throughput for three minutes.** Per-replica
figures from the harness's scrapes, prefill role only:

    t+min  pods   inTok/s   reqs/s  running  waiting   TTFT
    5.7      1    138,875    4.75      3       109     13.8
    7.3      2    138,750    4.62      3       152     69.2
    9.7     10    135,000    4.50      4       220    114.1
    10.6    10    815,294   27.18     18       702     31.6
    11.6    10    933,750   31.12     23       650     12.9

Two facts in that table.

**`reqs/s` is not a capacity.** It reads ~4.5 at one replica, at two, and at
ten. It is the rate the fleet is being served at, which under overload equals
one replica's rate until traffic actually reaches the new replicas. Any sizing
that divides by it is dividing by a constant.

**`inTok/s` is the capacity.** 138,750 on one replica, ~933,750 on ten. Prefill
time is set by how many prompt tokens a replica has to compute -- bounded in
practice by `--max-num-batched-tokens` -- not by a request count. Demand on
this trace was 12 req/s x 30,000 = **360,000 input tok/s**, so the fleet needed
about three prefill replicas from the first cycle of phase 2 and took 5.2
minutes to get there, during which TTFT rose from 13.8 s to 146.7 s.

**Why it took 5.2 minutes: decode has a capacity model and prefill does not.**
In the same run:

    decode   574 capacity decisions with bucket "derived"  (ITL model)
              66 with "huge"                              (measured)
    prefill  469 decisions, ALL measured, never "derived"

`SaturatedThroughputDerived` makes `mayOrder` true immediately
(`internal/signals/floor/floor.go:255`), so decode orders its full size in one
cycle. Prefill has no derived figure, so it must first observe itself saturated,
is held meanwhile, and grows one replica per cycle: 1 -> 2 took two minutes.

## The model

Mirror `internal/signals/itl`, which is the same idea for decode:
`ITL(k) = A*k + B`, an OLS fit over a window, with a validity rule.

For prefill, fit time-to-first-token against the prompt tokens resident in the
replica:

    TTFT(T) = A*T + B

`B` is the fixed per-request cost (scheduling, the KV handoff to decode, the
first forward pass's constant part). `A` is the marginal cost per prompt token.
The useful consequence:

    tokens per second = T / (A*T + B)  ->  1/A  as T grows

So **`1/A` is the replica's prefill token-throughput ceiling**, a single fitted
coefficient, obtainable from ordinary traffic without ever driving the replica
into saturation. That is exactly what decode gets from ITL and prefill is
missing, and it is what removes the held creep: with `1/A` known, the floor can
order `ceil(lambda_in / (k_sat * (1/A)))` on the first cycle of a shape.

Demand is already available: `lambda_in = arrivalRate * ILeff`, where
`arrivalRate` is the EPP figure the floor already uses
(`QueryModelArrivalRate`) and `ILeff` is `shape.New(...).ILeff`, the
prefix-discounted prompt length -- a prefix the cache already holds is not
prefill work.

## The signal, and what is missing

Both engines expose TTFT, and the names are ALREADY in
`internal/constants/metrics.go`:

    vllm:time_to_first_token_seconds_sum / _count
    sglang:time_to_first_token_seconds_sum / _count

Nothing collects them. There is no registered query in
`internal/collector/registration/`, and no field on
`domain.ReplicaMetrics`. So the work is:

1. **Collect it.** One registered query per engine, differenced sum/count for a
   per-interval mean. A cumulative histogram since pod start is useless here --
   a replica that served phase 1 would carry it into phase 2 forever -- so the
   window must difference consecutive scrapes, as the analysis script for run
   PK does.
2. **Carry the regressor.** `T`, the prompt tokens resident in the replica.
   `num_requests_running * avgInputTokens` is available today;
   `vllm:num_requests_running` and the prompt-token histogram are both already
   collected. If a batched-token gauge is exposed it is the better regressor.
3. **Fit it.** A package beside `internal/signals/itl`, reusing that package's
   shape: a window of observations, an OLS fit, and a `ValidModel` rule.
   Reject `A <= epsilon` (a flat fit means the regressor is not explaining
   anything) and require `TTFT(T) > 0`.
4. **Use it.** Set `SaturatedThroughputDerived` for prefill from the fitted
   `1/A`, converted to the req/s the floor currently divides by: `1/A` tokens
   per second over `ILeff` tokens per request. That is a unit conversion at the
   boundary, not a change to the floor.

## What this does not need

No change to `k1`/`k2`. Prefill's KV capacity is real but never the binding
constraint -- run PK's plot shows prefill holding a 730-deep queue at **25% KV
utilisation** while decode spikes to 95%. That is why the occupancy path cannot
size prefill and everything has to come through the demand floor.

## Validation

The claim is specific enough to test: with `1/A` fitted, prefill should reach
its implied replica count within one or two cycles of a shape change rather
than five minutes, and phase-2 prefill TTFT should fall below the 37.4 s
measured in run PK (down from 113.1 s before the two ordering fixes).

Needs a trace with genuine prefix reuse to exercise `ILeff`; the current fleet
runs `--no-enable-prefix-caching`, so the hit rate is 0 and the discount is
inert.
