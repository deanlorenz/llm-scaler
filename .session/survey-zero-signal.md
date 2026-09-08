# Survey — what breaks on a zero or absent composite signal

**Requested by [USER]** ("need to check which calculations break"), run 2026-09-08 on branch
`composite-analyzer` @ `4db060e2` (= `upstream/main`). Read from source; nothing inherited.

Two distinct failure modes, deliberately separated because they behave differently:
- **Absent** — `CompositeSignal.Result == nil` (no analysis this cycle).
- **Zero** — `Result != nil` but a value inside it is `0` (demand 0, PRC 0, RC/SC 0).

---

## 1. `Result == nil` — every consumer already guards it

| Site | Guard | Behavior |
|---|---|---|
| `variant_records.go:79` `recordsForRequest` | `if nr.Result == nil { return nil }` | returns nil; doc comment: *"callers treat as skip this model"* |
| `rescale.go:344` | `if satNamed.Result == nil { return nil }` | no decisions for the model |
| `rescale.go:528` `rescaleInputsForGroup` | `if satNamed.Result == nil { continue }` | model excluded from water-filling |
| `allocation/analyzer_helpers.go:141` `initRoleState` | `if e.Result == nil { return nil, pickerState }` | no roles ⇒ `anyRoleNeedsScaleUp` false ⇒ no scale-up; empty `RoleSpare` ⇒ no scale-down |
| `cost_aware_optimizer.go:59` | via `initRoleState` + `records == nil` | falls through to no decisions |
| `greedy_score_optimizer.go:117,156` | `if records == nil { continue }` | model skipped |
| `engine_v2.go:722` `hasSaturationResult` | `... && req.CompositeSignal.Result != nil` | quota not charged |

**Finding 1 — the absent case is uniformly safe today, and it degrades to "do nothing for this
model".** No panic, no division, no partial decision. This is the behavior **[USER]** wants preserved
("probably should not do any autoscaling").

**Finding 2 — but it is enforced by SEVEN independent nil checks, not one gate.** There is no single
"is there a signal" predicate; each consumer re-derives it. `hasSaturationResult` additionally tests
saturation's **name**, so §8's rename breaks *that* one while leaving the other six intact — i.e. the
rename would produce a *partially* gated system, which is worse than either extreme. This is the
strongest argument for A11' (repair the gate to test for a usable signal) **plus** exposing one shared
predicate the others can use.

---

## 2. Zero values inside a non-nil `Result`

### 2.1 `TotalDemand == 0` — safe, and load-bearing
- `rescale.go:94` — water-fill weight is `w := m.Priority * m.Demand`. A zero-demand model gets
  **weight 0**, so it receives no share of the budget beyond its floor. Correct: it wants nothing.
  The struct comment confirms the intent — *"used only inside the weight ratio, so its unit cancels"*.
- **Risk if a phantom demand were substituted:** a model with genuinely zero demand would acquire
  positive weight and take GPUs from models that actually need them. This is exactly the bug the
  deferred normalization branch hit (`77f21355`, forcing demand to `1.0`), and it is why §2.5's
  "demand 0 stays 0" rule matters here specifically.
- `roleDemandGPUs` (`rescale.go:585`) — `ceil(0 / best_PRC) = 0` replicas ⇒ 0 GPUs. Safe.

### 2.2 `PerReplicaCapacity == 0` — guarded, and it *disables* the variant
- `roleDemandGPUs`: `if vc.PerReplicaCapacity <= 0 { continue }`, then `if best <= 0 { return 0 }`.
- `multi_backup`'s `prcForVariant`/`roleBottleneckReplicas` use the same `prc <= 0 { continue }` shape.
- Per the parent mission's own 11-site survey, `PerReplicaCapacity <= 0` is *"a designed eligibility
  gate, not a division-safety guard"* — corroborated here by the `Reason` sentinel field existing
  alongside it.

**Finding 3 — a zero PRC silently removes the variant from consideration.** No error, no metric, no
log. For the **partial scale-from-zero** and **never-seen-SO** cases **[USER]** raised, that is exactly
the failure to avoid: the SO becomes invisible rather than being scaled. Saturation's `P0-store` ladder
(§5.1.4) is what prevents it — and this survey confirms nothing downstream would catch the miss if the
ladder ever failed to produce a value. **Recommend the composite record `C3-default-prc` / `C4-no-signal`
and emit it, so an invisible SO is observable rather than merely absent.**

### 2.3 `RequiredCapacity == 0` / `SpareCapacity == 0` — the ordinary steady state
- `initRoleState` seeds `pickerState[role] = rc.RequiredCapacity` and `RoleSpare[role] = SC`.
- `anyRoleNeedsScaleUp` is `> 0`; `needsScaleDownForRole` requires every live analyzer's
  `RoleSpare[role] > 0`. Both zero ⇒ no action. **This is the correct, common case** (a model at target).

### 2.4 `TotalSupply == 0` — guarded at the definition
`Utilization = TotalDemand / TotalSupply`, documented as **"0 when TotalSupply == 0"**. Confirmed
guarded in `buildCapacities`.

---

## 3. Related gates found — answering [USER]'s "I think we had more gates"

**[USER]** remembered a comment about pausing scaling decisions. Found a **separate, richer
system** than `hasSaturationResult`:

`applyScaleToZeroEnforcement` (`engine.go:1300+`) publishes `wva_model_scaling_blocked` with typed
reasons (`internal/constants/metrics.go:505+`), split by owning engine so two producers do not delete
each other's series:

| Owner | Reasons |
|---|---|
| `ScalingBlockedReasonsPolicy` (steady-state) | `variant-floor`, `policy-forbids-zero`, `engine-unsupported`, `activation-retention` |
| `ScalingBlockedReasonsWake` (scale-from-zero) | `no-wake-signal` |

Notable detail in that code: the reasons are published **unconditionally, before** the
empty-decision early return, because *"this call is what CLEARS a reason that no longer holds, so any
path that skips it pins the last bad answer — and a cycle that produced no decisions at all is
precisely when a stale 'will never park' series is most misleading."*

**Finding 4 — there is an established pattern for "why is scaling not happening", with typed reasons,
per-owner clearing, and a metric.** The composite's decision-path field (§5.1.2) should follow it
rather than invent a parallel convention. Open question for the user: should a composite
`C4-no-signal` also surface on `wva_model_scaling_blocked` (as a new policy-owned reason), or stay in
the composite's own log/metric? It is genuinely a "scaling is blocked" condition, so surfacing it there
would make the existing dashboard answer the question — but it adds a reason to a set another engine
also writes.

**Also relevant:** `activation-retention` is a *deliberate* pause after a wake, and
`ScalingBlockedActivationRetention` is documented as *not* alerted on. So "no scaling this cycle" is
already an expected, typed state — the composite's no-signal case is one more member of that family,
not a new concept.

---

## 4. Conclusions

1. **The absent-signal path needs no new safety work** — it is uniformly guarded and already degrades
   to "do nothing". The work is to keep it that way through §8's rename.
2. **One shared predicate is warranted.** Seven independent nil checks plus one name check means the
   rename produces partial gating. Expose "is there a usable signal" once.
3. **Zero demand must stay zero** — `rescale.go:94`'s weight makes a phantom demand actively harmful,
   not merely inaccurate.
4. **Zero PRC is the real hazard**, because it silently *disables* an SO rather than failing loudly —
   precisely the scale-from-zero case. Saturation's ladder prevents it; the composite should make a
   fallback visible via its decision path.
5. **No new default signals appear to be required** to keep calculations from breaking. Every zero is
   either guarded or semantically correct. The needed defaults are the **PRC** ones **[USER]** already
   named, and they live in saturation, not the composite.
6. **Follow the existing blocked-reasons pattern** for observability rather than inventing one.

**One decision for the user:** conclusion 6's open question — should `C4-no-signal` join
`wva_model_scaling_blocked`?
