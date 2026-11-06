# Renaming the project

`hack/rename` renames the project throughout the tree. It is written to run
once on a later `main`, after the open pull requests are in, and it is safe to
run again: a second run changes nothing.

## Run it

```bash
# Dry run: writes bin/rename-report.md and changes nothing.
go run ./hack/rename

# Rewrite the tree. Refused while anything is unclassified.
go run ./hack/rename --apply

# Regenerate what the rename left out of date.
LEARNREPLAY_WRITE_MESSAGE_WORDS=1 go test -p 1 -run TestMessageWords_AreCurrent ./internal/engines/analyzers/learnreplay/
make guides-render warmpool-render

# Fail if a run would still change anything.
go run ./hack/rename --expect-noop
```

The report lists, per rule and path class, the counts and samples; every kept
and held name with the reason; and every occurrence no rule covers. `--apply`
needs that last list to be empty, or `--allow-unclassified`.

## What it will not touch

- Binary and compressed files, whatever they contain.
- Symlinks, submodules and untracked files.
- `docs/plans/`, `docs/proposals/` and `comparison/`. They are records, so they
  keep their names. Only a link to a file the tool moved changes.
- Links and names of other repositories, and text about the other product.
- Files that record what an older release wrote: the learned-state fixtures and
  the old policy fixture under `testdata`.
- The rows marked as persisted, below. Change `Action` in `rules.go` to take the
  other side of a decision.

A line with `rename:keep` on it, in a file that is not Go, stays as it is.

## The two switches

- `Targets.Module` in `rules.go` (or `--module`) is the Go module path. It is
  written in one place and works before and after the code moves. A build inside
  the tree does not care that the path does not resolve yet. `go get` and
  `go install` from outside fail until the repository at that path has a
  `go.mod`.
- `--code-moved` turns the held rows on: repository links, image references and
  clone commands. Leave it off until the code and the images are in the new
  home, because those names resolve only then. Pull request and issue links
  stay on the old repository either way, because their numbers do not carry over.

## How Go is rewritten

Go goes through its syntax tree, never a pattern over the source. Regular
expressions over Go test fixtures cost this repository a reverted refactor.

- Identifiers map by whole name through `identRules`, so every use of a name
  lands on the same new name.
- Comments, strings, struct tags and import paths each go through the text rules
  in their own context. A struct tag is touched only by a rule that names it.
- Edits are spliced into the original bytes, and gofmt then realigns the columns
  and re-sorts the imports.
- A positional check then holds the Nth token of the result against the Nth
  token of the input. A token may only differ the way the table says, so a value
  in a fixture cannot drift and an expression cannot become a literal. It
  compares in document order on purpose: fixtures reuse names, and a check keyed
  by name would hide that.

Strings in test fixtures take the same rules as production strings. Most of them
are expected values, so they have to move with the code they check. What must
not move is listed: a name kept in production is kept in its tests by the same
row.

## Old names during the upgrade release

| Name | What happens | Remove |
|---|---|---|
| Metrics | `internal/metrics/legacy_names.go` publishes every `llmsm_` metric as `wva_` too, from the same scrape. `LEGACY_METRIC_NAMES=false` turns it off. | Next release: delete the file and its call in `InitMetrics`. |
| Controller settings | `internal/config/legacy_env.go` reads `WVA_*` as `LLMSM_*`, from the environment and the config file, and logs each one. The new name wins. | Next release: delete the file, the call in `main` and the two helpers' call sites. |
| Install variables | `make` and `deploy/lib/common.sh` stop on a `WVA_*` variable and name the new one. Read by nothing, it would install into the default namespace. | Next release: the `legacy_env_refuse` function, its call and the Makefile block. |

All three do nothing before the rename, because they key on the current prefix
differing from the old one, or on a metric having the new prefix.

## Names in the cluster

A name that changes is a new object, and the old one is left behind. This is
what each name does on upgrade. `upgrade` means an existing install re-deployed
from the renamed tree. None of this has been run on a cluster.

| Name | Where | Upgrade effect if renamed | Decision |
|---|---|---|---|
| `wva.llmd.ai/*` labels and annotations | users' Namespaces and workloads | Controller stops seeing them: namespace exclusions and policy grants drop silently | Stay |
| `wva-policy` | a namespace admins create and grant | Tenants fall out from under cluster policy | Stay |
| `wva-learned-state`, `wva-utilization-share-history` | ConfigMaps the controller writes | Fleet starts from nothing | Stay |
| `wva-saturation-scaling-config` | a ConfigMap read as a fall-back | Already legacy | Stay |
| `wva-setup-prereqs`, `wva-epp-metrics-auth` | `llm-d.ai/applied-by` marks on applied objects | `undeploy` stops finding what it applied | Stay |
| `wva-desired-replicas` | external metric in every KEDA-built HPA | Unproven: KEDA has to rebuild every HPA | Stay |
| `/mnt/local/wva` | directory on GPU nodes | Downloaded weights are orphaned on every node | Stay |
| `--field-manager=llm-scaling-manager` | owner of applied fields | Stale owner on existing objects | Stay |
| `API group llmd.ai`, kind `VariantAutoscaling` | no `wva` in either | Not renamed; the kind is deprecated separately | Stay |
| Lease `72dd1cf1.llm-d.ai` | leader election | No old name in it | Stay |
| `namePrefix: wva-`, so every prefixed object | Deployment, ServiceAccount, RBAC, Services, ConfigMaps | New objects next to the old: uninstall the old one first | `scaling-manager-` |
| `workload-variant-autoscaler-system`, `wva-system` | default namespaces | A default re-deploy lands in a new namespace with no learned state, beside the running controller | `scaling-manager-system` |
| `wva-external-scaler` | Service every ScaledObject addresses, its NetworkPolicy, TLS names | Existing ScaledObjects address a Service that no longer exists: KEDA calls fail and scaling stops until they are re-pointed | `scaling-manager-external-scaler` |
| `wva-scaling-policy-config` | the ConfigMap admins edit | The install writes a fresh one; tuned policy is not read | `scaling-manager-scaling-policy-config`; copy it across before deploying |
| `wva-manager-config` | rendered by the install | Re-rendered | `scaling-manager-manager-config` |
| `wva-controller-status` | rewritten at every start | Old one stays as an orphan | `scaling-manager-controller-status` |
| `app.kubernetes.io/name: workload-variant-autoscaler` | selector labels | Immutable on a Deployment, so it needs the new Deployment above | `scaling-manager` |
| Grafana dashboard ids, alert names | operators' routes | Routes on `WVA*` alerts stop matching | `LLMSM*` |

There are no finalizers and no webhooks with an old name.

## Length limits

The rendered overlays stay inside every limit: the longest Service name is 50
characters and the longest port name 10.

An X.509 common name holds 64 characters. The new Service name would leave room
for a namespace of 28, and the old default namespace is 34, so the tool shortens
the common name to `llmsm-external-scaler.<ns>.svc`, which fits up to 38.
Verification reads the SANs, not the common name.
