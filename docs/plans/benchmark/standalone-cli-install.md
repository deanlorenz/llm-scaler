# Standalone `llmdbenchmark` CLI install — decoupled from any repo clone

Status: **plan only, not implemented**. Written after discovering (see
`observability-gaps.md`'s sibling investigation) that `benchmark-install`'s full
clone-into-this-repo approach carries far more than a run-only invocation
against an already-existing stack actually needs.

## Why

`make benchmark-install` clones `llm-d-benchmark` into `llm-d-benchmark/` inside
whichever repo worktree you're standing in, and reinstalls/rechecks it out on
every fresh checkout. Confirmed tonight, precisely (not inferred):

- The CLI has no PyPI package — `install.sh` only ever does `pip install -e .`
  (editable) against the local clone. There's no way to `pip install
  llm-d-benchmark` from a registry.
- But an **editable** install is what ties the CLI to that specific clone
  persisting on disk. A **regular** (non-editable) `pip install <path>` copies
  the package into the target environment instead — the source clone becomes
  disposable once that finishes.
- Confirmed by grep: **zero** references to `helm`/`helmfile` anywhere under
  `llmdbenchmark/run/` (all 6 references in the whole CLI live under
  `llmdbenchmark/standup/`). A run-only invocation (`-U`/`--endpoint-url`)
  against an existing stack needs neither.
- `--spec`/`--specification_file` is a hard-required top-level CLI arg
  regardless of subcommand, but it accepts **a full path**, not just a bare
  name resolved against `--base-dir`. Same for `-w`/`--workload` via
  `--workload-file-path`. Neither has to come from the clone's own
  `config/specification/` or `workload/profiles/` trees.

So today's `benchmark-install` gives you three things bundled together that a
run-only user doesn't need together: (1) the CLI code, (2) `helm`/`helmfile`
prerequisites [standup-only], (3) the upstream `config/specification/` +
`workload/profiles/` asset trees [replaceable by our own full-path files].

## Proposed design

A new Makefile target — name TBD, something like `benchmark-cli-install` or
`llmdbenchmark-install` — distinct from the existing `benchmark-install`
(which stays as-is for anyone who *does* want standup):

1. Clone `llm-d-benchmark` at the pinned ref into a **temp directory**
   (`mktemp -d`, or a fixed scratch path under `/tmp`), shallow (`--depth=1` —
   we don't need history for a build step).
2. `pip install <tmp-clone-path>` — **not** `-e`/editable — into either:
   - `pip install --user`, landing the entry-point script in the standard
     `~/.local/bin/llmdbenchmark` (simplest, no venv to manage), or
   - a small dedicated persistent venv (e.g. `~/.local/venvs/llmdbenchmark`)
     with a wrapper/symlink placed at `~/bin/llmdbenchmark` (more isolated
     from whatever else is on this machine's Python).

   Either way, the point is: after this step, the installed CLI does not
   reference the tmp clone anymore.
3. Delete the tmp clone.
4. Result: `llmdbenchmark` is on `PATH` (or at a known stable location),
   reusable across every worktree and every future session on this machine,
   without re-cloning or re-installing per repo checkout.

## What replaces the clone's asset trees

- **Spec**: a minimal spec file we author and keep **in this repo**
  (`hack/benchmark/scenarios/` — exact shape TBD), referenced by its full
  path so `--base-dir` resolution never comes into play. Open question: what
  is the *minimum* content such a file needs for the CLI's run-only path to
  accept it without erroring? Not yet investigated — likely needs some
  experimentation once we're implementing this, since `-m`/`-p` on the CLI
  invocation override whatever the spec itself says for model/namespace
  anyway.
- **Workload profiles**: the ones we already maintain, in
  `test/benchmark/scenarios/*.yaml.in`. Today these get token-substituted
  (`__REQUEST_RATE__`, `__MAX_DURATION__`) and copied into the *clone's*
  `workload/profiles/<harness>/` before invoking the CLI (see `benchmark-run`'s
  recipe). Under this plan, using `--workload-file-path` to point directly at
  our own file instead: still need our own substitution step first (unless the
  CLI does its own token substitution for a directly-supplied file path too —
  not yet checked), but no dependency on the clone's `workload/profiles/`
  directory existing at all.

## Confirmed direction (Dean)

Render the substituted workload profile into our own run/results area, not
into the clone — colocated with the results it produces, not a throwaway
scratch path — then invoke the CLI with that file's full path via
`--workload-file-path` (never a bare `-w <name>` resolved against
`--base-dir`). Same principle for `--spec`: a full path to a file we
maintain, never a bare name.

## Open questions to resolve when implementing (not now)

1. Minimum viable `--spec` content for run-only mode — experiment needed.
2. Does `--workload-file-path` perform the same `REPLACE_ENV_*`/our own
   `__TOKEN__` substitution the clone-copy step does today, or is that
   entirely on us either way?
3. Does the installed package (even non-editable) have any *runtime* code
   path that assumes repo-root-relative files beyond what `--base-dir`/
   `--spec`/`--workload-file-path` explicitly supply? Editable vs. regular
   install only changes whether the *Python package* tracks the source tree
   live — if some step reads a file via a path relative to the repo root
   rather than via one of the explicit CLI flags, a non-editable install
   could still break in a way editable-from-a-persistent-clone wouldn't.
   Needs testing against a real run, not assumed from reading alone.
4. Versioning/upgrade story: since the CLI becomes decoupled from any one
   repo's clone, how do we deliberately bump to a newer/different
   `BENCHMARK_REPO_REF` later? (Straightforward: re-run the install target
   with a different ref — but worth deciding whether the target auto-checks
   for drift or is purely manual/on-demand.)
5. Exact target name and install location (`~/.local/bin` via `pip install
   --user` vs. a dedicated venv symlinked into `~/bin`) — user preference,
   not yet decided.
6. `benchmark-patch`'s two real bug fixes (EPP log timestamp parsing,
   guidellm-report non-fatal fix) currently patch files *inside the clone*.
   With no persistent clone, these need to move to patching the *installed
   package* directly (site-packages), or be upstreamed, or the affected
   behavior needs a different fix location entirely. Not addressed by this
   plan yet — a real gap this design introduces that the current approach
   doesn't have.

## What this does NOT change

- `benchmark-standup` and friends keep using today's full clone-in-repo
  `benchmark-install` — this plan is additive, for the run-only-against-an-
  existing-stack path specifically, not a replacement.
- Nothing here is implemented. This is scoped as a future work item.
