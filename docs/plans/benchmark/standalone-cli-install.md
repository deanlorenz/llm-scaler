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

## Update 2026-08-20: the pip-install premise is disproved; a better path exists, with one real open gap

Revisited this plan in a later session (Dean: "I prefer (4)"). Two rounds of
research, read-only against the actual `llm-d-benchmark` clone already
checked out in this worktree (pinned `v0.7.8`), changed the shape of this
plan substantially.

**The original "standalone pip install of the full CLI" premise is disproved.**
`ExecutionContext.base_dir` (`llmdbenchmark/executor/context.py`) is never
wired up from `--base-dir` in any of the CLI's four dispatch call sites
(`llmdbenchmark/cli.py`, the `ExecutionContext(...)` constructions for
standup/smoketest/teardown/run) — it is always `None`. Every `run`-mode step
that needs an asset tree therefore always falls back to
`Path(__file__).resolve().parents[3])`, which only "works" today because an
*editable* install's `__file__` happens to sit inside the git checkout. None
of `workload/harnesses/`, `workload/profiles/<harness>/`, or
`config/templates/jinja/20_harness_pod.yaml.j2` are declared as package data
in `pyproject.toml` (`[tool.setuptools.packages.find]` only discovers the
`llmdbenchmark` Python package; `[tool.setuptools.package-data]` ships only
`llmdbenchmark/analysis/scripts/*` and `llmdbenchmark/agent/*.yaml`) — a
non-editable install silently drops the other two trees, breaking
ConfigMap-building (step_06) and harness-pod deployment (step_07). `run` also
always needs a full `template_dir`/`values_file`/`scenario_file` spec triple
(no minimal-spec code path exists), and the shared CLI dispatch calls
`helmfile template` even for `run` unless the scenario's `config.yaml`
disables `modelservice`. None of this is fixable from our side with flags
alone — `--base-dir` is dead code upstream.

**A much better-fitting path exists and was previously unknown to this port:**
`llm-d-benchmark/existing_stack/run_only.sh` + a sibling
`config_template.yaml` (Dean's own recollection: "the old run_only.sh scripts
just used the harness directly" — correct, and still present at that path).
Pure bash, no Python, no CLI, no venv, no template tree: reads one YAML
config via `yq` (confirmed `mikefarah/yq` v4.53.2 already installed
system-wide — the exact flavor its `-o shell`/`explode()` syntax needs),
creates its own namespace-scoped ServiceAccount/Role/RoleBinding, verifies
the HF secret and endpoint reachability, builds a ConfigMap directly from
the config's own inline `workload.<name>` block (same schema as this repo's
`test/benchmark/scenarios/*.yaml.in` files, no translation needed), launches
a bare Pod (no helm/helmfile/jinja) running `harness.image`, `exec`s
`llm-d-benchmark.sh --harness=... --workload=...` inside it, and
reports/copies results. `dhl-la-1708` already has a PVC literally named
`workload-pvc` — the config template's own default — no customization
needed there either.

**But it has a real gap, found while sanity-checking the plan (Dean: "Not
sure the run_only script does all the post benchmark processing the regular
run does" — correct, it doesn't):** `llmdbenchmark/run/steps/step_07_deploy_harness.py`
has this comment on its full-CLI pod spec: `# Inject base64-encoded
kubeconfig so kubectl works inside the pod (needed by collect_metrics.sh and
llm-d-benchmark.sh vLLM scraping)`. `collect_metrics.sh`
(`workload/harnesses/collect_metrics.sh`) runs *inside* the harness pod,
using that injected kubeconfig, to scrape vLLM/EPP `/metrics` into the
`metrics/raw/*_metrics.log` files this port's own `extract_real_trace.py`
depends on for panel 3 (running/waiting bars), panel 4 (KV% heatmap), and
most of panel 5. `run_only.sh`'s bare pod spec injects no kubeconfig and
mounts no `llmdbench-harness-scripts` ConfigMap (only `${harness_name}-profiles`)
— it has neither the script nor the credentials to do this. A `run_only.sh`-
driven run would produce real per-request output but no pod-level metrics
scrapes at all, gutting the part of the pipeline this port exists for.

**Status: paused pending a decision on the gap, not ready to implement.**
Two ways to close it, neither evaluated yet:
1. Carry `collect_metrics.sh` + kubeconfig injection into our own copy of the
   pod spec (stops this being a verbatim, unmodified import of `run_only.sh`;
   also means deciding how comfortable we are injecting a kubeconfig into a
   namespace-scoped pod, versus scraping from outside it).
2. Write our own client-side vLLM/EPP scraper, extending the exact pattern
   `hack/benchmark/scrape_wva_metrics.sh` already uses for the WVA
   controller's own authenticated `/metrics` (port-forward + scrape from the
   machine driving the run, not from inside the harness pod) — no kubeconfig
   injection into the cluster at all, everything stays client-side like the
   rest of this port's collection scripts.
(2) fits this port's own established pattern more closely and avoids putting
a kubeconfig inside a pod; not yet attempted or verified.

## Update 2026-08-21: gap closed with a third option, better than either (1) or (2); implemented, live verification found one real bug fixed and one still open

Full detail in `docs/plans/benchmark/run-only-metrics-gap.md` — this is the
summary for this doc's own record, per its own "what to open issues for"
convention.

**The gap is closed with neither (1) nor (2) as originally scoped, but a
better third option Dean steered toward**: run the vLLM/EPP scraper *inside*
the harness pod (like (1)), but authenticate via the pod's own
ServiceAccount token (in-cluster kubectl config, automatic once a
ServiceAccount is attached) instead of injecting a kubeconfig at all. This
works because `run_only.sh` already creates its own tightly-scoped
ServiceAccount + namespaced Role/RoleBinding (`pods`/`pods/log` get/list) —
extended here with one more `secrets` rule, scoped by `resourceNames` to
exactly the EPP metrics-reader token, auto-detected by name pattern rather
than hardcoded (this repo's own WVA deploy names it differently than
`llm-d-benchmark`'s own default assumes). Net: real in-pod collection with a
*smaller* credential footprint than the full CLI's own approach, not a
tradeoff.

**Shipped** (all in `hack/benchmark/`, none a verbatim import — Dean: "you
can write your own version of run_only. It all stays here"):
`run_only.sh` (our fork), `run_only_collect_metrics.sh` (trimmed
vLLM/EPP-only fork of `collect_metrics.sh`), `render_run_only_config.sh`
(scenario → run_only config, including `REPLACE_ENV_LLMDBENCH_*` token
substitution the full CLI normally does client-side and `run_only.sh` never
does at all), `resolve_router_endpoint.sh` (reuses `wait_serving.sh`'s own
service-detection logic), plus `benchmark-run-only`/`benchmark-run-only-check`
Makefile targets and a `dhl-e2e-231.env` for `benchmark-guard`.

**Live verification against `dhl-e2e-231`, `quick_smoke`**: RBAC, both
ConfigMaps, pod creation, and model verification all passed. Found and fixed
one real bug — `run_only.sh` (the pinned upstream script too) references
`harness_results_pvc` unconditionally in a status message regardless of
storage mode, which crashed under our local-output rendering (fixed by
always populating the field). The workload run itself then hit a second,
**not yet root-caused** failure — `inference-perf`'s own multiprocess
metrics collector threw `RuntimeError: can't start new thread` with ~222
stray `inference-perf` processes observed in the pod's process table. Not
confirmed whether this is caused by our collector's background processes or
a pre-existing harness-image issue independent of this work — aborted per
standing risk tolerance, namespace confirmed back at baseline (decode pod
terminated, ScaledObject re-parked). See the plan doc's own status section
for exact next steps.
