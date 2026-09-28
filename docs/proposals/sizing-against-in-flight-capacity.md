# Sizing the fleet against where it will be, not only where it is

Run T (2026-09-28) served the 1k/6000 → 8k/1000 shape swap on
`feat/mu-from-itl`. Phase 2 — the half the branch was built for — held a median
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
itself was asking for **5.9 replicas' worth of work** — at the 0.85 scale-up
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
derived `mu` read 1.013 against the measured 1.058 — four per cent — which moved
utilisation from one side of 0.622 to the other. Main was not more correct; it
was luckier, and it also never sat at nine long enough to be tested.

### Why the ramp was slow

	07:46:13  1 -> 2      07:46:58  4 -> 5
	07:46:28  2 -> 3      07:47:13  5 -> 6
	07:46:43  3 -> 4      07:47:28  6 -> 9   atMax

Load started ~07:45:20. The first order came 53 s later — metric lag, `rate[1m]`
plus scrape — and then the fleet crept one replica per 15-second cycle for a
full minute before asking for the number the backlog actually justified.

The creep is `floor.Estimate`'s ordered-behind-queue path, which caps the order
at `scaleUpThreshold x (anticipated + smallestP)` — one replica beyond what is
already anticipated. Its comment is honest about why: a thin window can
under-read `mu` by half, and the floor is `rate x P/mu`, so a halved `mu`
doubles the order in one shot.

But the cap is paid every cycle of every ramp, including the ones where `mu` is
well measured. Each replica needs 67–82 s. Ordering nine at 07:46:13 rather
than creeping would have had the fleet ready about **75–90 s earlier** —
precisely the window in which TTFT p95 reached 68 s.

## The treatment

### 1. Release against a target utilisation inside the band

Replace `demand/scaleDownBoundary` in the spare calculation with
`demand/targetUtilisation`, where `targetUtilisation` sits strictly inside
[scaleDownBoundary, scaleUpThreshold] — the midpoint, 0.775 on the defaults.

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

### 2. Credit in-flight capacity against the backlog, then let the order stand

The `+1` cap is a proxy for two different doubts, and they need separating.

**Doubt A: is `mu` trustworthy?** Already answered elsewhere — the floor knows
whether a reading is its own, borrowed, thin or derived, and `mayOrder` is
exactly that judgement. Where `mayOrder` is true the cap is redundant.

**Doubt B: have I already ordered this?** Not answered anywhere. `anticipated`
subtracts in-flight replicas from *supply*, but the backlog term
`rate = lambda + backlog/drainSeconds` is untouched by them. So a 191-request
backlog is priced in full on every cycle until it drains — five times over, at
15-second cycles against a 70-second start. Remove the cap without fixing this
and the ramp overshoots harder, which is what put run T at nine in the first
place.

So credit the backlog with the capacity already on its way:

	inFlight_i        = replicas ordered but not yet Ready
	remaining_i       = max(0, expectedStartSeconds - age_i)
	creditedDrain     = sum_i mu_i x max(0, drainSeconds - remaining_i)
	effectiveBacklog  = max(0, backlog - creditedDrain)
	rate              = lambda + effectiveBacklog / drainSeconds

A replica that will be ready in 10 s of a 60-second drain window contributes
`mu x 50 s` of drain; one ordered a second ago contributes almost nothing. As
in-flight replicas approach readiness the credited drain rises and the residual
backlog falls, so successive cycles stop re-ordering the same queue. `mu_i` is
the same figure the floor already prices with, and `expectedStartSeconds` is
measurable — 67–82 s here — so it should be *learned* per variant from observed
`created -> Ready` intervals rather than configured, with a conservative default
until a variant has started a replica under observation.

With the backlog credited, the `+1` cap can be dropped **where `mayOrder` is
true**, and kept where it is not. The ramp then orders the deficit once,
waits out the dead time, and does not ratchet.

### 3. What this does NOT fix, and should not pretend to

The 53-second delay before the first order is metric lag: `rate(...[1m])` plus
scrape interval. No amount of control logic recovers a signal that has not
arrived. It is worth measuring separately against a shorter rate window, but it
is a collector change and does not belong in this proposal.

And phase-1's TTFT spike cannot be removed by faster ordering alone — with a
67–82 s start time and 6 req/s arriving, several hundred requests queue before
any new replica can serve, whatever the controller does. Ordering the deficit at
once removes roughly the 75–90 s of *self-inflicted* delay. Removing the rest
needs the replica to start faster or to be already running; that is the warm
pool's problem, not the optimizer's.

## Verification

Both changes alter replica counts, so a unit test is necessary but not
sufficient. The gate is:

1. Specs on the arithmetic: the release lands at 8 on run T's exact figures and
   stops; the credited backlog falls to zero as in-flight replicas approach
   readiness; a thin window still cannot order beyond `+1`.
2. A negative control against the parent commit for each — run T's numbers make
   this easy: `RoleSpare = 684,115` releases 0 today and must release 1 after.
3. A cluster re-run of the same shape swap, judged on phase-2 fleet and latency
   (unchanged is the requirement — this proposal must not undo that result) and
   on phase-1 GPU-minutes and the ramp's TTFT spike.

Phase 1 is noise for *latency* comparisons on a shared cluster — identical work
has produced 28 s, 59 s and 256 s medians — so the phase-1 claim to test is
GPU-minutes and the time to reach the target fleet, not the TTFT p95.
