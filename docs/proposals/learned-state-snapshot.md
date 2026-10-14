# Learned state across a restart: a snapshot

**Status:** proposed, and being built in the same PR. Supersedes Part 3 and
Appendix A of the restart proposal in PR #116
(`docs/proposals/learned-state-across-restarts.md` on that branch), which
chose to rebuild the latency line from the engines' Prometheus series and
rejected a stored snapshot. Its Parts 1 and 2 (the configuration fingerprint
and sharing a fitted line) stand; they are built in PR #126.

## The problem

Everything the saturation analyzer learns lives in the controller's memory.
A restart -- an upgrade, a node drain, a leader change, an OOM kill -- starts
every model cold:

| learned figure | what a cold start uses instead | direction of the error |
| --- | --- | --- |
| k2, the KV a replica holds at saturation (`P2-hist`) | the figure derived from the engine's arguments (`P3-k2`), or k1 | **higher** capacity, so **fewer** replicas |
| saturated throughput, what the demand floor divides arrivals by | none: the floor does not bind until the role saturates again | a floor that does not hold the fleet up |
| the ITL baseline, the zero-contention decode step | a default constant | the derived throughput is priced from a guess |
| the replica start estimate | none until a Pod finishes starting | backlog projected over no start time |

The first row is the one that matters. A higher k2 orders fewer replicas, and
an under-sized fleet does not saturate, so it never produces the reading that
would correct it: the tail latency of that episode does not recover. A
controller restart therefore re-opens, once per restart, exactly the hazard
the k2 history exists to close.

## Why #116's refit does not answer it

#116 proposed rebuilding the ITL line at startup from a range query over the
engines' own series, and rejected a ConfigMap on the grounds that the
observations already exist in Prometheus. Two things have changed since.

1. **Learned state is per variant now (PR #132), and the engines' series do
   not name the variant.** vLLM and SGLang emit no variant label. The
   controller attributes a Pod's readings to a variant by walking its
   owner references, which works only while the Pod exists. A range query
   over the last hours returns readings from Pods that have since been
   scaled away, and those cannot be attributed. A refit could rebuild a
   per-model figure, not a per-variant one.
2. **The refit never covered k2 or throughput.** #116 said so itself: k2
   depends on the controller's own judgement that a replica was saturated,
   not on a raw series. Those are the figures whose loss is dangerous.

And one of #116's costs has since been paid: the utilization-share optimizer
stores its need history in a ConfigMap, with its own Role, a
compare-and-swap write and deletion as a flush. A second store of the same
shape adds no new kind of object, permission or failure mode.

#116's case against reading WVA's **own metrics** back -- no writer
identity, no honest age, no trust boundary, silently inert -- still holds,
and is why this is a ConfigMap and not a metric.

## The design

**One ConfigMap**, `wva-learned-state`, in the controller's namespace, holding
one JSON document:

```json
{
  "version": 1,
  "writtenAt": "2026-10-14T18:00:00Z",
  "k2":         {"<key>": {"values": [4000, 4100], "lastUsed": "...", "lastWritten": "..."}},
  "throughput": {"<key>": {"values": [3.2], "lastUsed": "...", "lastWritten": "...", "sampledAt": "...", "lastRead": 3.2}},
  "itlBaseline":  {"<key>": 0.0118},
  "startSeconds": {"<key>": 71.5},
  "variantSeenAt": {"<key>": "..."},
  "accelerators": {"<ns>/<variant>": {"name": "H100", "lastUsed": "..."}}
}
```

The keys are the analyzer's own map keys, stored verbatim. The store does not
interpret them.

**What is not stored:** the raw `(k, ITL)` window samples, which age out in 30
minutes and are refilled within that; the per-Pod "start already counted"
records, which name Pods; and the per-model shape and decode-saturation memos,
which are seconds-to-minutes state. A borrowed k2's relaxation clock (#132)
joins the snapshot when #132 lands, so a restart does not restart a borrow at
full strength.

### Rules

1. **Every entry keeps its own clocks, and a restore applies the horizons to
   them.** A k2 window last read eight days ago is past `BucketRetention` and
   is not restored. A variant last seen two days ago is past `VariantTimeout`
   and its per-variant figures are not restored. A restore never makes a
   figure younger than it was.
2. **Restore once, on the first cycle the controller acts.** Only the leader
   optimizes, so a standby never restores; on a leader change, the new
   leader restores what the old one last wrote.
3. **Only the leader writes, at most once per minute,** in one
   read-modify-write with the ConfigMap's `resourceVersion` as the
   concurrency check, and only when the state changed.
4. **Deleting the ConfigMap flushes.** A controller that has read the
   ConfigMap and then finds it gone forgets what it restored rather than
   writing it straight back. The next write creates it from what has been
   learned since.
5. **A read that fails is not an empty store.** NotFound means nothing was
   stored. Forbidden, or any other error, is logged as an error and restores
   nothing, and the controller keeps retrying rather than overwriting what
   it could not read.
6. **Bounded.** The document stays under 900 KiB, below the API server's 1 MiB
   object limit. When it would exceed that, the least recently used entries
   are dropped first and the log names how many.
7. **Versioned.** A document of another version is ignored, not guessed at.
8. **Off by default** (`LEARNED_STATE_SNAPSHOT=true` turns it on), until a
   cluster run has shown a restart keeping k2 and the replica count.

### Keys that change shape

A key format change -- per-variant keys in #132, the configuration
fingerprint after it -- leaves stored entries under keys nothing looks up. They
are not migrated: inventing a variant for a pooled entry would invent the
provenance the per-variant keys exist to keep. They are restored, never read,
and dropped by the sweep at their horizon, like any other unused entry.

### What it costs

- A Role in the controller's namespace granting `get` and `update` on the one
  named ConfigMap, and `create` on ConfigMaps (Kubernetes cannot restrict
  `create` by name), as the share history's Role does.
- One read per restart and at most one write per minute.
- A restart no longer clears a poisoned figure. A k2 window that learned a
  wrong value keeps it across the restart, as it already did across every
  cycle before it. Deleting the ConfigMap is the flush.

## Related, not in scope

Two steps of #116 are not about restarts and are carried here so that
closing it does not lose them:

- **Publish the learned figures** (#116's step 6), as `wva_learned_*`
  families: the ITL line's slope, intercept and samples, k2 and the saturated
  throughput per bucket. Worth doing on its own: it makes a decision after a
  restart explicable on a dashboard, and it needs no trust from anyone because
  nothing reads it back. #116 left three questions open that still apply: its
  names collide with the `wva_itl_line_*` families in
  [signals-as-metrics](signals-as-metrics.md); a `source` label would make a
  restored figure a different series rather than an annotation on one; and
  per-variant keys (#132) change the label set it proposed.
- **Raise `DefaultWindowMaxSize`** (#116's step 7), the ITL window's sample
  count, from about 20 toward 240, so the window reaches its 30-minute age
  bound instead of stopping at a fraction of it. Measured in #116: **52% of
  244 cycles** could not fit a line for want of `k` spread. Gate it on a
  measurement that a wider window actually spans the 0.30 the fit needs; if it
  does not, it buys only memory.

## Open questions

1. Should a restored k2 count as an own reading for the throughput floor's
   "orders on its own data" rule, or as one sample less? It is an own reading
   of this variant, measured, but it may be days old.
2. Is the minute write interval right for a controller that may be killed
   without warning? The cost of a longer one is at most that much learning
   lost per restart.
