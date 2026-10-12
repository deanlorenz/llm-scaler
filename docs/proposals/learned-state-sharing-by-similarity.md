# Learned-state sharing by similarity, not by identity

**Status:** proposed, not built. Supersedes part of the sharing key on
`feat/engine-config-identity`; see [What this changes](#what-this-changes).

A variant with no fitted inter-token-latency line of its own cannot price its
first decisions from a measurement. It can price them from a *formula*, which
is what the controller does today, or it can start from a line another variant
already fitted. The second is better when the two variants really are running
the same engine, and worse when they are not.

The question this proposal answers is **what "the same engine" has to mean**.

## The problem with proving identity

The current design answers it with a digest. `EngineParams.Fingerprint()` hashes
the launch flags that drive latency; `engineFingerprint`
(`internal/engines/analyzers/saturation/analyzer.go`) refuses to produce a key
at all unless `Complete()` is true, and `itlPhysicsKey` falls back to the
unshared per-variant key when there is no fingerprint. A borrowed line is
therefore an assertion: *these two variants are running the same engine.*

That assertion has to be exactly right, because a false equality licenses a
variant to price itself from a measurement taken on a different engine. And it
cannot be made exactly right. Five adversarial review rounds found seven ways
to produce a false equality or to destroy a true one, every one of them
measured as a digest collision rather than argued:

| break | effect |
| --- | --- |
| a boolean flag swallows the next token, and an unresolvable `$VAR` there is dropped unrecorded | `--enforce-eager $EXTRA_ARGS` hashes as no flag at all |
| single-dash aliases are not read | `-q fp8` hashes as unquantized: an FP8 and a BF16 serving on one line |
| a valueless flag swallows a following alias | `--enforce-eager -q fp8` hashes as `--enforce-eager` alone |
| a glued single-character alias | `-qfp8`, `-O3`, `-O.level=3` hash as no flag at all |
| long flags neither parser maps | `--no-enable-prefix-caching`, which two of this repo's own benchmark scenarios set, hashes as prefix caching ON |
| the engine image is not hashed | `vllm-openai:v0.8.5` and `:v0.11.0` with identical flags collide — and a canary rollout is the likeliest way to get two same-flag variants, where the new cold one is the borrower |
| `EngineParams` is frozen on the first live record | the digest describes a configuration that is no longer running |

Two attempts to close the first four made things worse rather than better. The
second attempt denied sharing to ordinary deployments — `grep -q`,
`pip install -q`, `curl -O` and `python3 -O` in a shell preamble each cost a
fleet its key, because `splitShellString` flattens the whole `/bin/sh -c`
payload and a preamble's flags are indistinguishable from the engine's — while
`-qfp8` kept colliding. Both attempts were reverted.

The pattern is the finding. **A parser cannot soundly answer "did I read
everything" from a flattened shell command line**, and an identity claim needs
exactly that answer. Hashing the image plus the verbatim argument list would be
sound, but it fails the other way: two variants differing only in
`--served-model-name` would never share, which is the denial problem again,
arrived at on purpose.

## The reframe

A borrowed line does not have to be an identity claim. It can be a **prior**.

Under that reading a slightly-wrong match is not a correctness failure, it is a
worse starting point — and the failure mode changes from *false equality*,
which is unbounded, to *imperfect ranking*, which degrades gracefully. That is
the whole of this proposal. Everything below follows from it.

It is also what the codebase already does for the neighbouring quantity.
`EngineParams.IsCapacityCompatible` compares twelve latency-relevant fields and
**ignores every other field** — it is a similarity predicate, not a byte
identity — and `Store.FindCompatible` deliberately does not gate on
`Complete()`. `internal/signals/capacity/store.go` argues that at length,
because gating it once cost a 30x capacity over-estimate. So graded matching is
already in the tree, already reviewed, and already defended for capacity. This
extends it to the latency line.

## Tiers

Pick the best available source for a variant's latency line, preferring in
order:

| tier | source | predicate |
| --- | --- | --- |
| 0 | the variant's own fitted line | its own window has a usable fit |
| 1 | a line from a variant with the **same resolved flags** | identical `Fingerprint()`, both `Complete()` |
| 2 | a line from a variant **compatible on what matters** | same model, accelerator and GPU count, and `IsCapacityCompatible` |
| 3 | no line | fall back to the derived figure, as today |

Tier 2 is where the unreadable-configuration cases land. An unresolved flag
stops being a **veto** and becomes a **distance penalty**: a variant whose
`--block-size` could not be read is not refused a prior, it is matched on the
eleven fields that were read and marked as a weaker match. This is precisely why
the seven breaks stop being breaks — every one of them was "the gate was asked a
question it could not answer". Remove the gate and the question disappears.

Tier 1 keeps the digest, so `Fingerprint()` and `wva_engine_config` stay as they
are. What changes is that an absent or incomplete fingerprint demotes a variant
to tier 2 instead of dropping it to tier 3.

## Authority decreases with distance

A tier must decide **how much a prior is trusted**, not merely whether one is
used. Two rules, one existing and one new:

1. **Cap, never order — absolute, at every tier.** A borrowed figure already
   bounds what a variant may claim and can never drive it: `borrowedReading`
   routes the reading so `mayOrder` is never set, `thinOwnWindow` excludes it
   from the queue release, and `fleetHasMeasuredItself` refuses it. This rule
   does **not** relax for a tier-1 match. It is what makes a wrong prior
   survivable, and it is the reason this proposal is safe at all.
2. **Own data displaces a prior, and sooner at a greater distance.** The
   narrowing already un-sets the borrowed flag once a variant has its own
   window. A tier-2 prior should additionally be displaced by *fewer* own
   observations than a tier-1 prior, since there is less reason to believe it.

## The risk this proposal does not remove

**"Learn on the way" may never complete.** A fit needs k-spread of at least
`DefaultMinKSpread` (0.30), and this project has measured the autoscaler
defeating its own learning: unpinned, per-replica KV occupancy fell to a median
of 0.049 against the 0.15 admissibility floor, so 13% of readings were
admissible and the window stalled at 4 of 10 samples. A cold variant can borrow
indefinitely and never graduate.

Two consequences, both deliberate:

- The tier is **permanent state, not a transient**. It must be visible for as
  long as it applies — in the `itl-line-borrowed` and `derived-mu` lines, in
  `itl-gps-mismatch` (which now names the lender), and ideally as a label or a
  `reason` value so it is reachable from Prometheus rather than only from logs.
- Rule 1 above cannot be relaxed on the grounds that a prior has "settled".
  It has not settled; it has persisted.

**And the knowledge problem does not vanish**, it softens. Ranking similarity
still needs to know which fields drive latency. The difference is that getting
the ranking slightly wrong now costs accuracy, where getting an identity claim
slightly wrong cost correctness. `IsCapacityCompatible`'s twelve fields are an
already-reviewed answer to the same question.

## What this changes

**Nothing in the scaler.** PR #125 (`fix/scaler-handshake-observability`)
touches only `internal/scaler/externalscaler.go` and its test. It is unaffected.

**On `feat/engine-config-identity` (PR #126), this supersedes the key and keeps
everything else.**

Superseded:

- `engineFingerprint`'s `if !rec.EngineParams.Complete() { return "" }` — becomes
  a tier-2 demotion rather than a refusal.
- `itlPhysicsKey`'s fall-through to the per-variant key on an empty
  fingerprint — becomes a tier-2 lookup before giving up.
- The documentation claim that an unreadable configuration is "already excluded
  from sharing", which is already corrected on that branch but for the ITL line
  only.

Kept, unchanged, and sound as reviewed:

- `Fingerprint()`, `FingerprintVersion`, the field-classification drift test,
  and `wva_engine_config` — tier 1 needs all of it.
- The sharing plumbing: `itlLines`, `publishLine`, `borrowLine`, the sweep on
  `bucketRetention`, and the narrowing.
- The floor's cap-not-order routing, `mayOrder`/`borrowedOnly`, and the
  `heldWhy` composition.
- `disableLearnedStateReuse`, which gates tiers 1 and 2 together.

Three of the seven breaks are **not** addressed by this proposal and want their
own change either way, because they are about the digest describing the wrong
thing rather than about who may share: the engine image is not hashed,
`EngineParams` is frozen on the first live record, and the ITL window is not
re-keyed when a variant's configuration changes.

## Open questions

1. **How many own observations should displace a tier-2 prior?** "Fewer than
   tier 1" is the rule; the numbers want a measurement, not a guess.
2. **Should tier 2 require a *minimum* similarity**, or is "same model,
   accelerator, GPU count" enough? A variant differing in `MaxModelLen` by 100x
   is compatible by the twelve-field predicate but probably should not lend.
3. **Is tier 2 worth it at all**, or is tier 1 plus a correct image hash enough
   in practice? Worth measuring on a real fleet before building the tiering:
   if nearly every real pair that should share is already tier 1, the tiering
   is complexity for no gain.
4. **Where does the tier surface in Prometheus?** This project's convention is
   a `reason` on an existing series rather than a new gauge.

Question 3 is the one to answer first, and it is answerable from
`wva_engine_config` on a real fleet without writing any of this.
