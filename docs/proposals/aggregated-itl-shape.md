# The ITL line of an aggregated pod, by request shape

**Status:** proposal for issue #275, 2026-11-03. Builds on #432, which fits a
line for an aggregated variant (prefill and decode in one pod, no role label)
and reports the mu it derives without pricing it.

## The problem

An aggregated pod runs the prompts it admits as chunks inside its decode
steps. So its step time, the ITL, carries prefill work, and how much depends
on the request shape. A line fitted at one shape mis-prices another, in
either direction: at 1000 in / 500 out about 40% of the ITL is prefill.
#432 therefore reports the figure and sizes nothing from it.

Two more errors sit in the derived mu today:

- A sequence being prefilled holds KV and emits no token. The mu over-states
  by about `TTFT / (OL * ITL)`.
- A line kept past a shape change (a held slope, a restored line, a borrowed
  `|both` line) prices the old shape for as long as it is kept.

## The model

One step of an aggregated pod takes

```
ITL(k, x) = B + A*k + c*x
```

- `k` is the KV fill, as for decode.
- `x` is the prefill tokens computed per step: `x = n * IL_eff / (OL + s_pf)`.
  `n` is the resident sequences, `IL_eff` the prompt after prefix-cache
  hits, `OL` the output length, and `s_pf` the steps a request spends in
  prefill. With a short prefill this is the `n * IL_eff / OL` of #432.
- `B` and `A` are the engine's, as for decode. `c` is the cost of one
  prefill token in a step.

At a fixed shape `n` is proportional to `k` (`n = k * C / KVreq`), so
`x = g * k` with

```
g = (C / KVreq) * IL_eff / (OL + s_pf)
```

and the line the tracker already fits is `B + (A + c*g)*k`. Its slope at
shape `g` is `A_g = A + c*g`. So:

- the existing fit is kept unchanged, and it measures `A_g` at one shape;
- `c` is read off the slopes of **two or more shapes**, never from one.

`g` is computed from the same shape the derived mu is priced at (`IL_eff`,
`KVreq`, `OL` of the fleet shape) and the variant's capacity `C`. The
measured `x / k` (prefill token rate times ITL, over k) is logged beside it
as a check, not used.

`s_pf` comes from Little's law on readings already collected: a replica
holds `n = RunningRequests` sequences and completes `RequestRate`, so its
residence is `n / RequestRate`, and `s_pf = n / (RequestRate * ITL) - OL`.
It is learned as `sigma = s_pf / IL_eff` (steps per prompt token), the median
over the steady readings the full fit uses, and priced as
`s_pf = sigma * IL_eff`. A negative per-replica figure is a bad reading and
is dropped. No new metric is needed, so the recorded runs replay it.

## What is fitted, from which readings

**The line.** As #432: the same tracker, band, settle and cap admission. One
change: every line carries the shape it was fitted at, `gFit`, and `sigma`.
That holds for a fit made this cycle, a held slope, a restored line and a
borrowed `|both` line.

**A shape point for `c`.** After a fit, the variant may record a point
`(gFit, A_g)` for its line key. It is admitted only when all hold:

- the variant's own line, not held (`SlopeHeld`) and not borrowed;
- tier `ols`, or `pinned-B` on a learned intercept (`BaselineLearned`). A
  slope pinned to `DefaultBaselineSec` is not a measurement of `A_g`;
- every reading in the window was taken at this shape (see the shape change
  below), outside the 5-minute shape hold;
- `sigma` is known.

A point within 20% of an existing point's `g` replaces it (the newest wins),
so a flat shape refreshes one point and never builds a spread. At most six
points are kept per key.

**Identifying `c`.** With two or more points whose largest `g` is at least
1.5 times the smallest, an ordinary least-squares fit of `A_g` on `g` gives
`A` and `c`. It is refused, and `c` stays unidentified, unless `c >= 0` and
`A > 0`: a prefill token cannot make a step faster, and a negative decode
slope is not this physics. The fit is redone when a point is added or
replaced.

**When the shape is flat.** One point, or points too close: `c` is not
identified, and no default is pinned. A `c` identified earlier is held:
it describes the engine, not the shape, so it is kept through a flat period
for as long as the line key lives (same retention, same engine arguments).

## How the derived mu is computed

For the shape priced now, `g*`, and the line's `(A_g, gFit)`:

1. `g*` within 20% of `gFit` (the shape tracker's tolerance): price `A_g` as
   it is. A fleet that never changes shape is priced like a decode variant.
2. Otherwise, with `c` identified: price `A_g + c * (g* - gFit)`. Anchored on
   the line, so `c` only moves the difference.
3. Otherwise: no derived figure (`why=shape-unpriced`); the variant is sized
   as it is today without a line. A line without a `gFit` (from a #432
   build) is case 3 until it is refitted.

Then, with `n*` the sequences at the pricing `k` (after the batch cap, as
now) and `s_pf* = sigma * IL_eff`:

```
x*  = n* * IL_eff / (OL + s_pf*)
ITL = B + A_priced * kPrice      (A_priced from case 1 or 2; it already holds c*g*)
mu  = n* / ((OL + s_pf*) * ITL)
```

`g*` is `x* / kPrice`, so a batch cap that holds `n*` below the KV's
sequences also lowers the prefill term it prices.

The `s_pf*` term removes the prefill-occupancy over-statement: by Little's
law a resident sequence completes once per `(OL + s_pf) * ITL` seconds, not
once per `OL * ITL`. With `sigma` unknown there is no figure.

## When report-only lifts

Per variant, in memory. The variant's figure is priced once, at the current
shape and outside the shape hold:

- it has a figure (case 1 or 2, `sigma` known); and
- the GPS check (`noteLineMismatch`), now with the shape term and the
  occupancy correction, agrees within 15% (`DefaultGPSMismatchThresholdPct`)
  on the median comparable replica (`k >= 0.30`) on **3 consecutive
  comparable cycles**.

It goes back to report-only on 3 consecutive disagreeing cycles, when the
shape leaves cases 1 and 2, on a change of engine arguments, and on a
controller restart (the lift is not persisted: it is earned again within a
few cycles from the restored line). While report-only, sizing is exactly
#432's. The `derived-mu` line logs `reportOnly` and the reason; the metric
keeps `source="derived-report-only"` until the lift and `source="derived"`
after it.

## A shape change

When `g*` moves more than 20% from the `gFit` of the variant's window, the
window's readings are dropped, as `itlUnderArgs` drops them on a change of
engine arguments. Kept: the intercept `B` (it does not depend on shape), the
held slope with its `gFit` and `sigma`, and the shape points. So:

- the next fit is taken at the new shape only, and the pinned fit gives a new
  `A_g` within a few settled readings;
- meanwhile the held or restored or borrowed line prices the new shape only
  through `c` (case 2), or not at all (case 3). This is the stale line of
  #432, priced after the shape hold: it can no longer happen;
- a lifted variant drops to report-only for a change without `c`, and
  earns the lift back at the new shape.

## Persistence and rollback

The learned-state document stays version 1. Every field is additive and
`omitempty`:

- `itlLines[*]` and `itlSlopes[*]` gain `g` and `sigma`;
- a new `itlShapeTerms`, keyed like `itlLines` (or the window key with no
  fingerprint): `c`, `a`, the points `{g, a, fittedAt}`, and `args`.

A document from #432 has none of them: its lines restore with no `gFit` and
price nothing (case 3) until refitted at the running shape. A build before
this one ignores the new fields: #432 keeps reporting only, and a build
before #432 fits no aggregated line at all. The forget annotation drops a
variant's shape term with its line; a change of engine arguments drops it
with the window. Decode lines and their keys do not change.

## Replay switches

Off, each does what #432 does.

- `itl-aggregated-shape-term`: the `gFit` and `sigma` tags, the window drop
  on a shape change, the shape points and `c`, and the corrected figure.
  Still reported only, so it moves the reported rate of the aggregated
  variants and nothing priced.
- `itl-aggregated-sizes`: the lift. It needs the tags, so alone it moves
  nothing; with both on it may move sizing of the three aggregated variants
  in the acceptance and engine fixtures. That interaction is named in
  `switch_runs_test.go`.

The fixtures run one shape, so `c` is never identified in them; the shape
points, `c`, cases 2 and 3 and the lift are held by unit tests and the
closed-loop probe ([golden rule](analyzer-learner.md#25-replay-and-the-golden-rule)).

## Limits

- One shape per fleet: `g*` is the fleet's shape, so a variant serving a
  shape of its own is priced at the average (as #432).
- `sigma` is measured at the current load and priced at `kPrice`. At a
  higher fill decode takes more of the step's token budget and prefill
  stretches, so mu is a little high there. The GPS check reads at the
  replica's own `k` and does not see it.
- A per-sequence cost (a term in `n`, not `k`) moves with `KVreq` across
  shapes. `c` absorbs part of it.
- The slo analyzer's line has the same shape dependence. It can reuse `c`
  later; it is out of scope here.
