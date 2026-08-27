# CT1b implementation report — engine-side guard: saturation producing no result

**Status.** DONE.

## What changed

`internal/engines/steadystate/engine_v2.go` — inside `runAnalyzersAndScore`,
immediately after the existing `if err != nil { return nil, err }` check on
`runV2AnalysisOnly`'s result, added:

```go
if baseResult == nil {
    return nil, fmt.Errorf("saturation analyzer produced no result for model %s", modelID)
}
```

No import changes needed (`fmt` was already imported). This makes the engine
explicitly reject a `(nil, nil)` return from the saturation analyzer instead
of letting a nil `Result` flow into `buildNamedResult` →
`composeAnalyzerResults` → the optimizer. The error return is caught by the
existing abstain machinery two levels up in `engine.go` (`collectV2ModelRequest`
propagates it; the `StartOptimizeLoop` caller logs it, records the
`recordOptimizationFailedEvent`, emits safety-net metrics, and `continue`s to
the next model) — no new logging/event/metric code was needed in
`engine_v2.go`, per the task's mechanism note.

`internal/engines/steadystate/engine_v2_test.go` — added a new
`Describe("runAnalyzersAndScore saturation-nil guard", ...)` block, placed
between the existing `"runAnalyzersAndScore call ordering"` and
`"runAnalyzersAndScore disabled-analyzer gate"` blocks (same file, same
construction pattern). It builds an `Engine` whose `saturationV2Analyzer` is
a `fakeAnalyzerWithResult` with `result: nil` — since that fake's `Analyze`
returns `(f.result, nil)` unconditionally, this reuses the existing
test-double pattern (from `engine_v2_population_test.go`) to force exactly
the `(nil, nil)` violation of `SaturationAnalyzer`'s real contract that the
new guard defends against. The spec asserts:

- `err` is non-nil and its message contains
  `"saturation analyzer produced no result for model m"`.
- `results` is `nil`.
- A companion `spyAnalyzer` registered as a second, enabled analyzer has
  `callCount == 0` — proving the function returns before reaching the
  non-saturation analyzer loop (and therefore before
  `composeAnalyzerResults`/`buildNamedResult` for that entry).

No other files were touched. The pre-existing uncommitted T1 work in this
package (`composeAnalyzerResults`, the `t.Skip()`-ed tests) was left exactly
as found — not staged, not reverted, not rewritten.

## Commit

- Hash: `71c401c2`
- Message: `fix(steadystate): guard runAnalyzersAndScore against a nil saturation result`
- Files: `internal/engines/steadystate/engine_v2.go`,
  `internal/engines/steadystate/engine_v2_test.go`
- Diffstat: 2 files changed, 3 insertions(+) in the source file; +34 lines of
  new test in the test file.

## Verification

- `gofmt -l` on both changed files: no output (clean).
- `go vet ./internal/engines/steadystate/...`: clean.
- `go build ./...`: clean, no errors.
- `go test ./internal/...`: every package passes (`ok` for every package with
  tests; no package failed to build; `[no test files]` only for packages that
  never had tests).
- `go test ./internal/engines/steadystate/... -v` (ginkgo default reporter):
  **130 of 130 specs passed, 0 failed, 0 pending, 0 skipped** — up from **129**
  specs on the pre-change baseline (verified by `git stash`-ing this change and
  re-running the same command, which reported `129 of 129`, all passing). This
  confirms the new spec is the only addition and every previously-passing
  spec's outcome is unchanged, consistent with the task's reachability note
  that this branch cannot fire via any real analyzer call today.

## Deviations / ambiguity

None of substance.

1. The spec's error-wording example (`fmt.Errorf("saturation analyzer
   produced no result for model %s", modelID)`) was used verbatim.
2. Test placement: the spec said to check `engine_v2_test.go` and related
   files for the existing pattern and use it. `engine_v2_test.go` already had
   two `Describe` blocks scoped to `runAnalyzersAndScore` itself (`"call
   ordering"` and `"disabled-analyzer gate"`), each building an `*Engine`
   literal directly with a `fakeAnalyzerWithResult` for `saturationV2Analyzer`
   (a pattern originally defined in `engine_v2_population_test.go`). Followed
   that same construction pattern and inserted the new block between the two,
   keeping all `runAnalyzersAndScore`-focused blocks contiguous, rather than
   creating a new file.
3. Used `fakeAnalyzerWithResult{result: nil}` rather than writing a new
   test-double type — its `Analyze` method returns `(f.result, nil)`
   unconditionally, so `result: nil` already produces exactly the `(nil, nil)`
   case the task asked to exercise, with zero new test-support code.

## Worktree note (not part of CT1b itself)

The task spec file `docs/plans/analyzers/task-ct1b-2026-08-26.md` was not
present in this worktree's checked-out tree at task start (this worktree's
`HEAD` did not have the docs commit that added it, even though the commit
object itself — `a7cba3f2`, "docs(analyzers): CT1a task spec + implementation
report, CT1b task spec" — was reachable in the shared object database from a
sibling worktree, `worktrees/single-analyzer`). Restored the file into this
worktree via `git show a7cba3f2:docs/plans/analyzers/task-ct1b-2026-08-26.md`
before starting, so it exists here as the task instructions expect. Its
content was verified identical to the spec text reproduced in the task
prompt. This is now a new untracked file in this worktree, included in the
same commit as the code/test change below.
