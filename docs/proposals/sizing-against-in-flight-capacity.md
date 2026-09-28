# Sizing the fleet against where it will be, not only where it is

Run T (2026-09-28) served the 1k/6000 → 8k/1000 shape swap on
`feat/mu-from-itl`. Phase 2 -- the half the branch was built for -- held a median
of 4 replicas against main's 6 at the same TTFT. Phase 1 went the other way: it
held **8.64 replicas against main's 7.18 for an identical median TTFT of
0.022 s**, and its cold-start TTFT spike reached 68 s.

Both are the same defect. The controller reasons only about the fleet it has
right now, and on both edges that costs it:

- **Scaling down**, it asks whether the CURRENT fleet minus one replica would
  still sit below the scale-down boundary. Nine replicas at a utilisation of
  0.643 could not release one, because releasing would have put it at 0.723 and
  the rule demands ≤ 0.70.
- **Scaling up**, it orders one replica per cycle while a queue builds, because
  it has no way to distinguish "my estimate is thin" from "nine replicas are
  already on their way". With a 15-second cycle and a **67–82 s** replica start
  time, ordering the deficit outright would re-order the same backlog five times
  before the first replica served anything.

The `+1` cap and the conservative release rule are both defences against the
same missing quantity: **the capacity already in flight, and when it will
arrive.**

## What the numbers were

Phase-1 steady state, run T, from the controller's own `analyzer-result` and
`throughput-demand-floor-total` lines:

	organic demand (before floor)      ~600-830k tokens
	demand after the floor              5,383,320 tokens
	decode supply at 9 replicas         8,374,572 tokens   (9 x 930,508)
	utilisation                         0.643
	RoleSpare[decode]                     684,115 tokens
	one replica (prc)                     930,508 tokens

The floor is `lambda x P / mu` = `6 x 930,508 / 1.013` = 5.51M, so the floor
itself was asking for **5.9 replicas' worth of work** -- at the 0.85 scale-up
threshold, seven replicas. The demand estimate was right. The fleet sat at nine
anyway.

### Why it could not come down

`safeRemovalReplicasForRole` (`internal/engines/allocation/analyzer_helpers.go:248`)
is `floor(RoleSpare[role] / prc)`, and `applyUniversalThreshold`
(`internal/engines/steadystate/engine_v2.go:565`) sets
`RoleSpare = supply - demand/scaleDownBoundary`. Releasing one replica of `n`
therefore requires

	utilisation <= scaleDownBoundary x (n-1)/n

At nine replicas that is `0.70 x 8/9 = 0.622`. Run T was at **0.643** and missed
by 0.02. The band [0.70, 0.85] is deliberate hysteresis and is not the problem;
the problem is that the release test is taken against the *bottom* of the band,
so the fleet must be a full replica below the boundary before it may shed one.
The larger the fleet, the tighter that gets in absolute terms while the
per-replica step stays the same size.

This is also why a 4 % difference in `mu` mattered out of proportion. The
derived `mu` read 1.013 against the measured 1.058 -- four per cent -- which moved
utilisation from one side of 0.622 to the other. Main was not more correct; it
was luckier, and it also never sat at nine long enough to be tested.

### Why the ramp was slow -- and it is NOT a cap

	07:46:13  1 -> 2      07:46:58  4 -> 5
	07:46:28  2 -> 3      07:47:13  5 -> 6
	07:46:43  3 -> 4      07:47:28  6 -> 9   atMax

The first reading of this was that `floor.Estimate`s ordered-behind-queue path
capped the order at one replica beyond anticipated supply. That is wrong: the cap
only applies on the branch where `mayOrder` is false and a standing queue
releases it, and run T had derived readings, so `mayOrder` was true and the
floor was never capped at all.

What the log actually shows is a fleet chasing its own measurement:

	time      demand      anticipated   rc          target
	07:46:28  2,940,867   ~2 replicas   738,443     3
	07:46:43  3,807,628   ~4 replicas   768,180     4
	07:46:58  4,598,560   ~5 replicas   768,180     5
	07:47:13  5,389,492   ~6 replicas   768,180     6
	07:47:28  9,902,755   ~6 replicas   5,147,393   9  atMax

`rc` is `demand/scaleUpThreshold - anticipatedSupply`, and it sat at **768,180
for three consecutive cycles** -- just under one replica (930,508). Each cycle
demand grew by about 790k, anticipated grew by one replica, and the difference
stayed pinned a hair below the one-replica granularity. So the optimizer added
exactly one replica per cycle, not because anything capped it, but because
**RC never exceeded one replica's worth.**

The reason it never did is that both inputs are trailing measurements:

- **Demand is observed from the fleet's own occupancy and queues**, so each new
  replica takes load, which raises measured demand, which justifies the next
  replica. The signal grows *with* the fleet it is meant to size.
- **lambda is `rate(...[1m])`**. At 07:46:13 the floor was 1,408,616 tokens,
  which at a cost of about 930k per replica-second is an arrival rate near
  1.5 req/s -- against a true 6. A minute-trailing mean under-reads a ramp by
  the ratio of ramp length to window.

At 07:47:28 the backlog term finally caught the 191-267 deep queue, demand
almost doubled in one cycle, and the fleet jumped straight to its ceiling. The
ramp was slow for two minutes and then overshot in one cycle -- both symptoms of
sizing from signals that describe the past.

## The treatment

### 1. Release against a target utilisation inside the band

Replace `demand/scaleDownBoundary` in the spare calculation with
`demand/targetUtilisation`, where `targetUtilisation` sits strictly inside
[scaleDownBoundary, scaleUpThreshold] -- the midpoint, 0.775 on the defaults.

Releasing then leaves the fleet inside the dead band rather than at its floor,
which is what makes it safe: the fleet lands somewhere it will not immediately
want to scale up from. On run T's numbers:

	at 9: spare = 8,374,572 - 5,383,320/0.775 = 1,427,708  -> release 1
	at 8: spare = 7,444,064 - 5,383,320/0.775 =   497,200  -> release 0, stop

Eight replicas, utilisation 0.723, inside the band and stable. **Not** seven:
seven would be utilisation 0.826, which is stable today but only 0.024 from the
scale-up threshold, and releasing to there is how a fleet flaps. Main's seven
was on the right side of that line by luck, and this proposal deliberately does
not chase it. The honest gain is 9 → 8, about 11 % of phase-1 GPU-minutes.

`targetUtilisation` is derived, not a new knob: the midpoint of two thresholds
that already exist and are already validated against each other
(`scaleUpThreshold > scaleDownBoundary` is enforced in config).

### 2. Size against the backlog that will exist, with the start time it will take

There is no cap to remove. The ramp was slow because RC is computed from
signals that describe the past -- observed demand, which grows with the fleet,
and a minute-trailing lambda -- so the fleet discovers its own size one replica
at a time and then overshoots when the backlog term finally lands.

Sizing has to use a LEADING signal instead, and the queue is one: unserved
arrivals are exactly what makes a queue grow. Two changes, in order of how much
they matter:

**The arrival rate a ramp needs is not the one being served.** lambda is
`rate(...[1m])`, which under-reads a ramp by roughly the ratio of ramp length to
window -- 1.5 req/s against a true 6 at 07:46:13. But the queue's own growth
reveals the shortfall directly, from a signal already collected:

	lambdaEff = lambdaMeasured + max(0, (backlog - backlogPrev) / cycleSeconds)

A growing queue means arrivals exceed service by exactly its growth rate, and
that term responds within one cycle instead of trailing a minute. It vanishes in
steady state, where the queue is flat, so it changes nothing outside a ramp.

The fix is to price the backlog **as it will be when the capacity lands**, not
as it is now. Two terms, and the first is the one the earlier draft of this
proposal missed:

- **Arrivals keep coming during the dead time.** From the instant a replica is
  ordered until it is Ready, work arrives at `lambda` and only the replicas
  already serving drain it. That is the dominant term on a ramp: at 6 req/s over
  a 70-second start, 420 requests arrive before the new replica serves one.
- **Capacity already ordered will help, partly.** A replica that becomes Ready
  with `t` seconds left in the drain window contributes `mu x t`, not `mu x
  drainSeconds`.

So, per role, with `T_next` the effective start time of the next replica this
role would order (§2.2):

	inFlight       = replicas ordered, not yet Ready        (PendingReplicas)
	remaining_i    = max(0, startFor(i) - age_i)
	servedDuringT  = mu x [ N_ready x T_next
	                        + sum_i max(0, T_next - remaining_i) ]
	arrivedDuringT = lambda x T_next
	B_landing      = max(0, backlog + arrivedDuringT - servedDuringT)
	rate           = lambda + B_landing / drainSeconds
	needed         = rate/mu - (N_ready + inFlight)

`needed` subtracts the in-flight replicas explicitly, which is what makes the
cap unnecessary: the same backlog cannot be ordered twice, because the second
cycle sees the first cycle's replicas both in `inFlight` and in
`servedDuringT`. With that, the `+1` bound is dropped **where `mayOrder` is
true** and kept where it is not.

`B_landing` is larger than today's `backlog` on a ramp and smaller than it once
capacity is in flight -- which is the whole point. It is also why this must land
together with §1: sizing up faster without being able to size back down is how
run T reached nine and stayed.

#### 2.1 Where the start time comes from

`T` is not a constant and must not be guessed in code.

**Seeded from the ScaledObject.** A `startSeconds` entry in the ScaledObject's
WVA metadata (the same metadata `registry.ParsePoolMeta` already reads for
`sleepMinSize`) gives the operator's estimate for a model that has never started
a replica under observation. It is a seed, not a setting: it is used until the
first measurement replaces it, and a run says which it used.

**Measured from the first ordered replica.** The controller already sees pod
creation and readiness. On the first ordered replica reaching Ready, record
`Ready - created` per variant and use the measured figure from then on; keep it
as a short EWMA so a single slow start does not pin the estimate. Run T's
figures -- 67 s for four pods, 82 s for five -- show the spread is small enough
for a mean to be useful and large enough that a constant would be wrong.

The measurement belongs per **variant**, not per model: it is an image, a set of
engine flags and a node, and two variants of one model routinely differ.

#### 2.2 A warm pool makes the first few replicas cheap -- and only the first few

A pool Pod that is `Asleep` for this model is "resident and wakeable"
(`internal/warmpool/pool.State`), and waking it is sub-second where a cold start
is 67–82 s. So the effective start time is **per replica ordered, not per
variant**: if `k` pool Pods can wake for this model, the first `k` replicas of
an order cost `wakeSeconds` and the rest cost the cold `startSeconds`.

	startFor(j) = wakeSeconds   for j < k
	            = startSeconds  for j >= k

This changes the size of the order, not only its timing: with `k = 3`, three
replicas land almost at once, so `servedDuringT` for the fourth is much larger
and `B_landing` much smaller -- the fleet orders *fewer* cold replicas because
the warm ones have already absorbed the backlog. The current code cannot express
that, because it has one start time and no notion of `j`.

`k` counts only what can actually wake **now**:

- `State == Asleep` -- `Loading` is a cold load in progress and is not a credit,
  `Serving` and `Draining` are already committed elsewhere.
- the Pod is not already lent to another variant (`lent` in `autosize.go`).
- the membership is for **this model**. A Pod asleep with another model's weights
  resident is a model switch, which is fast but not free, and is deliberately
  out of scope here: counting it would make `k` a promise this proposal cannot
  keep.

#### 2.3 Several models want the same pool

`k` is an availability claim on a shared resource, and two models sizing
themselves in the same cycle will both claim it unless the pool is apportioned.
The constraint is **Pods**, not memberships: one Pod wakes for one model, so if
model A has three sleeping memberships and model B two across the same four
Pods, they cannot both have what they see.

So the pool is split before anyone sizes against it:

	freePods     = Pods in the pool that are wakeable and unlent
	claim_m      = |{Pods where m is Asleep}|         per requesting model m
	deficit_m    = the replicas m would order at k=0  (its unaided need)
	grant_m      = min(claim_m, apportion(freePods, deficit_m))

`apportion` divides `freePods` in proportion to `deficit_m` -- the model that
needs the capacity most gets the most of it -- floored to whole Pods, with the
remainder going by descending fractional part and then by model name so the
split is deterministic and two controllers reach the same answer. `grant_m` is
then the `k` of §2.2.

Two properties this has to have, both worth a spec:

- **`sum_m grant_m <= freePods`.** The split must never hand the same Pod to two
  models; that is the whole reason it exists.
- **A model that asks for nothing is granted nothing**, so it cannot hold pool
  capacity away from a model that is scaling.

Sizing against a grant is still a *claim*, not a reservation: between sizing and
waking, another actor may take the Pod. That is acceptable and must be
acknowledged rather than defended against -- the fallback is the ordinary cold
start, and the next cycle re-sizes with `k` reduced. What is NOT acceptable is
treating the claim as certain and therefore ordering too few cold replicas to
recover; so `grant_m` is used for the *effective start time*, never to reduce
the replica count below what the cold path would eventually order.

### 3. What this does NOT fix, and should not pretend to

The 53-second delay before the first order is metric lag: `rate(...[1m])` plus
scrape interval. No amount of control logic recovers a signal that has not
arrived. It is worth measuring separately against a shorter rate window, but it
is a collector change and does not belong in this proposal.

And phase-1's TTFT spike cannot be removed by faster ordering alone -- with a
67–82 s start time and 6 req/s arriving, several hundred requests queue before
any new replica can serve, whatever the controller does. Ordering the deficit at
once removes roughly the 75–90 s of *self-inflicted* delay. Removing the rest
needs the replica to start faster or to be already running; that is the warm
pool's problem, not the optimizer's.

## Verification

Both changes alter replica counts, so a unit test is necessary but not
sufficient. The gate is:

1. Specs on the arithmetic: the release lands at 8 on run T's exact figures and
   stops; B_landing rises with arrivals over the dead time and falls as
   in-flight replicas approach readiness; a warm grant of k makes the first k
   replicas cheap and the k+1th cold; the pool split never hands one Pod to two
   models; a thin window still cannot order beyond `+1`.
2. A negative control against the parent commit for each -- run T's numbers make
   this easy: `RoleSpare = 684,115` releases 0 today and must release 1 after.
3. A cluster re-run of the same shape swap, judged on phase-2 fleet and latency
   (unchanged is the requirement -- this proposal must not undo that result) and
   on phase-1 GPU-minutes and the ramp's TTFT spike.

Phase 1 is noise for *latency* comparisons on a shared cluster -- identical work
has produced 28 s, 59 s and 256 s medians -- so the phase-1 claim to test is
GPU-minutes and the time to reach the target fleet, not the TTFT p95.
