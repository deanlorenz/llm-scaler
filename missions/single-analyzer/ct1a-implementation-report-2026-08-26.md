# CT1a implementation report — nil-guard `rescaleModelDecisions`

**Status.** DONE.

## What changed

`internal/engines/allocation/rescale.go` — added an early nil-guard in
`rescaleModelDecisions`, immediately after the `saturationNamedEntry` call
(now lines 344-347), mirroring `rescaleInputsForGroup`'s existing pattern
exactly:

```go
satNamed := saturationNamedEntry(req.AnalyzerResults)
if satNamed == nil || satNamed.Result == nil {
    return nil
}
records := buildVariantRecords(req, satNamed.Result)
```

`internal/engines/allocation/rescale_test.go` — added a new `Describe("rescaleModelDecisions", ...)`
block with one `It` that builds a `ModelScalingRequest` whose `AnalyzerResults`
has no `domain.SaturationAnalyzerName` entry (left nil), calls
`o.rescaleModelDecisions(...)` via a `NewGreedyByScoreOptimizer()` receiver
(matching the construction pattern already used throughout
`rescale_optimize_test.go`), and asserts it returns an empty/nil slice with no
panic. Added the `"context"` import needed for `context.Background()`.

No other files were touched. The pre-existing uncommitted T1 changes in
`internal/engines/steadystate/` were left exactly as found (not staged, not
committed, not reverted).

## Commit

- Hash: `8906ef7b`
- Message: `fix(allocation): nil-guard rescaleModelDecisions against a missing saturation entry`
- Files: `internal/engines/allocation/rescale.go`, `internal/engines/allocation/rescale_test.go`
- Diffstat: 2 files changed, 30 insertions(+), 0 deletions(-)

## Verification

- `gofmt -l` on both changed files: no output (clean).
- `go build ./...`: clean, no errors.
- `go test ./internal/...`: all packages pass (`ok` for every package with
  tests; no `[no test files]` package failed to build).
- `go test ./internal/engines/allocation/... -run TestScaleToZero -v` (ginkgo
  default reporter): **255 of 255 specs passed, 0 failed, 0 pending, 0
  skipped** (up from 254 before this change — the one new spec). No existing
  spec's behavior changed, consistent with the spec's expectation that this
  path was never previously exercised with a nil `satNamed`.

## Deviations / ambiguity

None of substance. Two minor judgment calls, both within the spec's stated
latitude:

1. The spec's Todo says "Add a new test ... (or the existing test file
   covering `rescaleModelDecisions`, if a more specific one already exists —
   check first)." I checked: no existing test calls `rescaleModelDecisions`
   directly (only indirectly through `Optimize`/`applyRescale` in
   `rescale_optimize_test.go`). Placed the new test in `rescale_test.go`
   (the file the spec's own Refs section names as the default target),
   directly next to the other `Describe` blocks for this file's internal
   helpers.
2. The spec's expected-outcome section says "asserts it returns `nil` (or an
   empty slice)". Used Gomega's `BeEmpty()`, which accepts both, rather than
   pinning to exactly `nil` — this matches the spec's own stated flexibility.
