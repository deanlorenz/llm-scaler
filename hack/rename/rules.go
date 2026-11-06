package main

import (
	"regexp"
	"strings"
)

// Targets are the switches of a run.
type Targets struct {
	// Module is the Go module path. This one value is the module switch: it is
	// the only place the target is written, and the old paths are matched
	// whatever it says, so the tool can move main from the first path to this
	// one, or a later main from any earlier target to a new one.
	Module string
	// CodeMoved turns the held rows on. See HoldUntilMoved.
	CodeMoved bool
}

// DefaultTargets is what a run does without flags.
var DefaultTargets = Targets{
	Module:    "github.com/llm-d-incubation/llm-d-scaling-manager",
	CodeMoved: false,
}

const (
	newRepoSlug = "llm-d-incubation/llm-d-scaling-manager"
	newImage    = "ghcr.io/llm-d-incubation/llm-d-scaling-manager"
	newName     = "llm-d-scaling-manager"
	kubeName    = "scaling-manager"
	metricPre   = "llmsm_"
	envPre      = "LLMSM_"

	// residualPattern is every spelling of an old name. Whatever it finds
	// outside a rule's claim is unclassified, and --apply refuses it.
	residualPattern = `wva|Wva|WVA|(?i:workload[-_ ]?variant)|llm-scaling-manager|ev-shindin|llm-d/llm-d-workload`
)

// Path classes for the report.
const (
	ClassTool      = "tool"
	ClassGo        = "go"
	ClassGoTest    = "go-test"
	ClassGoE2E     = "go-e2e-support"
	ClassTestdata  = "testdata"
	ClassHistory   = "docs-history"
	ClassDocs      = "docs"
	ClassManifest  = "manifest"
	ClassDashboard = "dashboard"
	ClassShell     = "shell"
	ClassPython    = "python"
	ClassCI        = "ci"
	ClassBuild     = "build"
	ClassOther     = "other"
)

// classify gives a path its class.
func classify(p string) string {
	switch {
	case strings.HasPrefix(p, "hack/rename/"):
		return ClassTool
	case strings.Contains(p, "/testdata/"):
		return ClassTestdata
	case strings.HasSuffix(p, "_test.go"):
		return ClassGoTest
	case strings.HasPrefix(p, "test/") && strings.HasSuffix(p, ".go"):
		return ClassGoE2E
	case strings.HasSuffix(p, ".go"):
		return ClassGo
	case matchAny(historyPaths, p):
		return ClassHistory
	case strings.HasSuffix(p, ".md"):
		return ClassDocs
	case strings.HasPrefix(p, ".github/"):
		return ClassCI
	case strings.HasSuffix(p, ".json") && strings.Contains(p, "dashboard"):
		return ClassDashboard
	case strings.HasPrefix(p, "config/") || strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".j2"):
		return ClassManifest
	case strings.HasSuffix(p, ".sh"):
		return ClassShell
	case strings.HasSuffix(p, ".py"):
		return ClassPython
	case p == "Makefile" || strings.HasPrefix(p, "Dockerfile") || p == "go.mod" || p == "PROJECT":
		return ClassBuild
	}
	return ClassOther
}

// binaryExtensions are never edited, whatever their bytes look like.
var binaryExtensions = []string{
	".gz", ".tgz", ".zip", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".pdf",
	".docx", ".pptx", ".xlsx", ".so", ".a", ".bin", ".zst", ".xz",
}

// historyPaths are records of what was decided and when. They keep the names of
// their time; only a link to a file the tool moved is updated.
var historyPaths = []string{
	"docs/plans/**",
	"docs/proposals/**",
	"comparison/**",
}

// scalerTLS mints the external scaler's certificates.
var scalerTLS = []string{"deploy/lib/scaler_tls.sh"}

// upstreamGuide is the guide for moving from the other product.
var upstreamGuide = []string{"docs/guides/from-upstream-wva/**"}

// keepWholeFiles keep every old name except the module path, which has to
// change for the file to compile. These files implement the fall-back to the
// old names and test it, so the old names in them are the point.
var keepWholeFiles = []string{
	"internal/metrics/legacy_names.go",
	"internal/metrics/legacy_names_test.go",
	"internal/config/legacy_env.go",
	"internal/config/legacy_env_test.go",
	"hack/check-legacy-env.sh",
	"docs/developer-guide/renaming.md",
	"pr-body-rname.md",
}

// standalone matches a name standing alone: not part of a longer kebab-case name.
func standalone(name string) string {
	return `(?:^|[^A-Za-z0-9_-])(?P<m>` + regexp.QuoteMeta(name) + `)(?:$|[^A-Za-z0-9_-])`
}

// RulesFor builds the table. Order is meaning: a row claims its text before
// the rows below it run, so what must stay comes first and the broad rows come
// last. metrics are the metric names read from internal/constants, so a metric
// is counted as a metric and not as a word.
func RulesFor(t Targets, metrics []string) []Rule {
	kept := func(name, class, pattern, why string, notPaths ...string) Rule {
		return Rule{Name: name, Class: class, Pattern: pattern, Action: Keep, Why: why, NotPaths: notPaths}
	}
	held := func(name, pattern, replace, why string) Rule {
		return Rule{Name: name, Class: "repo-link", Pattern: pattern, Replace: replace, Action: HoldUntilMoved, Why: why}
	}
	pathToo := func(r Rule) Rule {
		r.Contexts = []string{CtxText, CtxComment, CtxString, CtxPath}
		return r
	}

	rules := []Rule{
		// One line, in a file the tool otherwise rewrites, that must stay.
		{Name: "keep-marked-line", Class: "legacy-alias", Pattern: `(?m)^.*rename:keep.*$`, Action: Keep,
			Contexts: []string{CtxText}, Why: "marked by hand: the fall-back needs the old name on this line"},

		// Links and paths into other repositories. They name someone else's
		// content, so renaming them breaks them.
		kept("keep-upstream-fork", "external", `github\.com/lionelvillard/[^\s)\]"'`+"`"+`>]*`, "another person's fork"),
		kept("keep-upstream-url", "external", `https?://github\.com/llm-d/llm-d-workload-variant-autoscaler[^\s)\]"'`+"`"+`>]*`, "the upstream repository: issue and pull links must keep resolving"),
		// The Go module path: the one switch. It runs before every other
		// keep, because a kept file still has to import the renamed module.
		{Name: "module-path", Class: "module", Pattern: `(?:^|[^/.\w-])(?P<m>github\.com/(?:llm-d/llm-d-workload-variant-autoscaler|ev-shindin/llm-scaling-manager)(?:/internal|/api|/cmd|/test|/pkg)?)(?:$|[^\w.-])`,
			Fn: moduleSwap(t.Module), NotPaths: append([]string{"README.md", "CONTRIBUTING.md", "docs/**", ".github/**", "Dockerfile.warmpool-proxy", "OWNERS"}, historyPaths...),
			Contexts: []string{CtxText, CtxComment, CtxString, CtxImport}},
		{Name: "module-path-docs", Class: "module", Pattern: `github\.com/llm-d/llm-d-workload-variant-autoscaler/(?:internal|api|cmd|test|pkg)\b`,
			Fn: moduleSwap(t.Module), Contexts: []string{CtxText}, NotPaths: historyPaths},

		kept("keep-upstream-slug", "external", `llm-d/llm-d-workload-variant-autoscaler`, "names the repository this project forked from"),
		kept("keep-ig-wva", "external", `(?:github\.com/llm-d-incubation/)?ig-wva`, "a separate repository"),
		kept("keep-llmd-docs", "external", `llm-d\.ai/docs/architecture/Components/workload-variant-autoscaler|README\.wva\.md`, "a page in another project's docs"),
		kept("keep-upstream-prose", "external", `(?i:llm-d's) (?:workload-variant-autoscaler|WVA)(?: \(WVA\))?|workload-variant-autoscaler \(WVA\)|still named WVA`, "names the other product"),

		// What an older release wrote, kept as it wrote it: a test reads these
		// to prove the new code still understands them.
		{Name: "keep-recorded-fixture", Class: "testdata", Pattern: residualPattern, Action: Keep, Contexts: []string{CtxText},
			Paths: []string{"internal/engines/steadystate/testdata/learned-state/**", "internal/engines/allocation/testdata/main-era-composed-policy.yaml"},
			Why:   "written by an older release: the test is that this still loads"},

		// Two tests whose fixture value is load-bearing. The first asserts a sorted
		// result, and the new name sorts before team-a where the old one sorted
		// after it. The second must match the writer inside a recorded document.
		{Name: "keep-sort-order-fixture", Class: "testdata", Pattern: `wva-system`, Action: Keep, Contexts: []string{CtxString},
			Paths: []string{"cmd/cache_namespaces_test.go"}, Why: "the expected order depends on this string's sort position"},
		{Name: "keep-sort-order-shell-fixture", Class: "testdata", Pattern: `wva-ctl`, Action: Keep,
			Paths: []string{"hack/check-watch-namespace-list.sh"}, Why: "the expected list is sorted, and the new name sorts before team-a"},
		{Name: "keep-recorded-writer", Class: "testdata", Pattern: `wva-system|wva-controller-[a-z0-9-]*`, Action: Keep, Contexts: []string{CtxString},
			Paths: []string{"internal/engines/steadystate/learned_state_backcompat_test.go"}, Why: "must equal the writer in the recorded document"},

		// Files that keep their names, so what links to them keeps the name too.
		{Name: "keep-history-file-names", Class: "file-ref", Pattern: `proposals/wva-[a-z-]+\.md|wva-vs-keda-scaling-study|wva-external-scaler-(?:proposal|alternative)|exp[0-9a-z]+_wva\.png`, Action: Keep,
			Contexts: []string{CtxText, CtxComment, CtxString}, Why: "plans, proposals and studies keep their file names"},

		// History keeps its names; only links to moved files change, below.
		{Name: "history-file-link", Class: "file-ref", Pattern: `two-variant-wva-benchmark|infra_wva\.sh|build-wva-image-local\.sh|two-variant-wva\.yaml|tail_wva_logs|dump_wva_(?:full|target)_timeseries`,
			Fn: renameFileWord, Paths: historyPaths},
		{Name: "keep-history", Class: "history", Pattern: residualPattern, Action: Keep, Paths: historyPaths,
			Contexts: []string{CtxText, CtxPath}, Why: "plans, proposals and studies are records of their time"},

		{Name: "keep-whole-file", Class: "legacy-alias", Pattern: residualPattern, Action: Keep, Paths: keepWholeFiles,
			Contexts: []string{CtxText, CtxComment, CtxString, CtxTag, CtxPath}, Why: "the fall-back to the old names: the old names are the point"},

		// Images and people that are not this repository.
		kept("keep-warmpool-proxy", "image", `ghcr\.io/ev-shindin/(?:llm-scaler/)?warmpool-proxy`, "pinned by digest; no image exists under the new owner (see the report)"),
		kept("keep-fma-image", "image", `ghcr\.io/ev-shindin/(?:dual-pods-controller|fma-launcher)|ev-shindin/llm-d-fast-model-actuation`, "another project's image or fork"),
		kept("keep-old-repo-name", "repo-link", `github\.com/ev-shindin/llm-scaler/[^\s)\]"'`+"`"+`>]*`, "a numbered link on the repository's earlier name"),
		{Name: "keep-rules-screenshots", Class: "image", Pattern: `wva\.rules(?:-firing)?\.png`, Action: Keep,
			Contexts: []string{CtxText, CtxComment, CtxString, CtxPath}, Why: "a screenshot: the picture still shows the old group name"},
		{Name: "keep-other-product-paths", Class: "external", Pattern: `from-upstream-wva|autoscaling-nvidia-mooncake-sglang-vs-wva`, Action: Keep,
			Contexts: []string{CtxText, CtxComment, CtxString, CtxPath}, Why: "these files are about the other product, so their names stay"},

		// The upstream guide talks about two installs at once: upstream's names
		// on upstream's objects stay, and this project's names move.
		{Name: "keep-guide-upstream-line", Class: "external", Pattern: `(?m)^.*<upstream-namespace>.*$`, Action: Keep,
			Paths: upstreamGuide, Why: "a command against the upstream install"},
		{Name: "keep-guide-upstream-names", Class: "external", Pattern: `workload-variant-autoscaler|wva-(?:manager|metrics|epp-metrics)[a-z-]*|\bWVA\b|\bwva_`, Action: Keep,
			Paths: upstreamGuide, Why: "names of the upstream install, or the other product"},
		{Name: "keep-owners", Class: "person", Pattern: `ev-shindin`, Action: Keep, Paths: []string{"OWNERS"}, Why: "a GitHub login"},

		// Links that record something on the old repository: the new one does
		// not have these numbers.
		kept("keep-numbered-links", "repo-link", `github\.com/ev-shindin/llm-scaling-manager/(?:pull|issues)/[0-9]+[^\s)\]"'`+"`"+`>]*|github\.com/ev-shindin/llm-scaling-manager/commit/[0-9a-f]+|github\.com/ev-shindin/llm-scaling-manager/releases/tag/v[0-9][^\s)\]"'`+"`"+`>]*`,
			"pull requests, issues, commits and tags stay on the old repository: their numbers do not carry over"),

		// Held until the code, the issues and the images are in the new home.
		held("hold-image", `ghcr\.io/ev-shindin/llm-scaling-manager`, newImage, "the image does not exist under the new name until CI publishes it there"),
		held("hold-image-base", `(?:^|[^\w/.-])(?P<m>ghcr\.io/ev-shindin)(?:$|[^\w/-])`, "ghcr.io/llm-d-incubation", "the Makefile's image base"),
		held("hold-image-suffix", `\$\(IMAGE_TAG_BASE\)/llm-scaling-manager`, "$$(IMAGE_TAG_BASE)/"+newName, "the Makefile's image name"),
		held("hold-repo-url", `github\.com(?P<sep>[/:])ev-shindin/llm-scaling-manager`, "github.com${sep}"+newRepoSlug, "a link or a remote: resolves only after the move"),
		held("hold-packages", `github\.com/users/ev-shindin/packages\?repo_name=llm-scaling-manager`, "github.com/orgs/llm-d-incubation/packages?repo_name="+newName, "the package page"),
		held("hold-repo-slug", `ev-shindin/llm-scaling-manager`, newRepoSlug, "a repository slug: resolves only after the move"),
		held("hold-fork-clone", `/llm-scaling-manager\.git`, "/"+newName+".git", "a fork's clone URL: a fork of the new repository does not exist yet"),
		held("hold-clone-dir", `(?:^|[^\w/-])(?P<m>cd llm-scaling-manager)(?:$|[^\w/-])`, "cd "+newName, "the directory a clone makes"),
		held("hold-upstream-remote", `(?:^|[\s\x60])(?P<m>ev-shindin)(?:$|[\s\x60])`, "llm-d-incubation", "an owner named in text"),

		// llm-d-benchmark is another repository. Its scenario schema has a wva
		// block, and patch_harness.sh anchors on the text of its source, so these
		// name its code, not ours. No test here reads them: a test needs its clone.
		kept("keep-harness-schema", "external", `\.wva\.(?:enabled|hpa|prometheus|image|namespace|repoRef)\b|chartVersions\.wva|has\("wva"\)|\bwva\.(?:enabled|hpa|prometheus|image|namespace|repoRef)\b|harness,wva\b|wva-config/platform`,
			"llm-d-benchmark's scenario schema"),
		{Name: "keep-harness-scenario-key", Class: "external", Pattern: `(?:^|[\s\x60])(?P<m>wva):`, Action: Keep,
			Paths: []string{"hack/benchmark/scenarios/**"}, Why: "the wva block of llm-d-benchmark's scenario schema"},
		{Name: "keep-harness-anchor", Class: "external", Pattern: `WVA teardown skipped|WVA was never installed`, Action: Keep,
			Paths: []string{"hack/benchmark/patch_harness.sh", "hack/check-harness-teardown.sh"}, Why: "text in llm-d-benchmark's source that the patcher anchors on"},

		// Persisted or in-cluster names that stay. Each is a decision in the
		// report; change Action to Rename here to take the other side.
		kept("keep-annotation-domain", "persisted", `wva\.llm-?d\.ai`, "label and annotation keys sit on users' objects; a new key silently drops namespace exclusions and policy grants"),
		kept("keep-policy-namespace", "persisted", standalone("wva-policy"), "a fixed namespace admins created and granted; the name is the security boundary"),
		kept("keep-learned-state", "persisted", standalone("wva-learned-state"), "holds learned state: a new name starts the fleet from nothing"),
		kept("keep-share-history", "persisted", standalone("wva-utilization-share-history"), "holds the utilization-share ledger history"),
		kept("keep-legacy-policy-cm", "persisted", standalone("wva-saturation-scaling-config"), "already a legacy name, read as a fall-back"),
		kept("keep-applied-by", "persisted", `wva-epp-metrics-auth|applied-by[=:] ?(?:\\?")?wva-setup-prereqs|(?:^|[^A-Za-z0-9_-])wva-setup-prereqs(?:$|[^A-Za-z0-9_-])`,
			"a mark on applied objects that undeploy selects on; the EPP role and its per-namespace bindings carry the same word and are matched by name"),
		kept("keep-metric-wire-name", "persisted", standalone("wva-desired-replicas"), "the external metric name in every KEDA-built HPA"),
		kept("keep-node-dir", "persisted", `/mnt/local/wva`, "a directory on GPU nodes that holds downloaded weights"),
		kept("keep-field-manager", "persisted", `--field-manager=llm-scaling-manager`, "server-side-apply owner of existing fields"),

		// Sentences that state the old naming as fact. The keep row is what makes
		// the second run a no-op: the new sentence names the old prefix.
		{Name: "keep-claim-result", Class: "prose-claim", Pattern: "the old `wva_` names are published", Action: Keep, Why: "this tool's own sentence"},
		{Name: "claim-keeps-names", Class: "prose-claim", Pattern: "Kubernetes objects, make targets and metrics keep their `wva-` / `wva_` names(?:\\r?\\nfrom upstream, which is the only naming to change in dashboards)?",
			Replace: "Metrics use the `llmsm_` prefix; the old `wva_` names are published as well for one release"},

		// An X.509 common name holds 64 characters, and the longer service name
		// leaves room for a namespace of only 28, which the old default of 34
		// does not fit. Verification reads the SANs, so the common name can be
		// shorter than the Service: these keep every namespace up to 38 working.
		{Name: "tls-common-name-ca", Class: "tls", Pattern: `(?P<k>CN=|commonName: )wva-external-scaler-ca\.`, Replace: "${k}llmsm-scaler-ca.", Paths: scalerTLS},
		{Name: "tls-common-name-leaf", Class: "tls", Pattern: `serving "wva-external-scaler\.\$\{WVA_NS\}\.svc"`, Replace: `serving "llmsm-external-scaler.${LLMSM_NS}.svc"`, Paths: scalerTLS},
		{Name: "tls-common-name-cert-manager", Class: "tls", Pattern: `commonName: \$\{dns1\}`, Replace: "commonName: llmsm-external-scaler.${LLMSM_NS}.svc", Paths: scalerTLS},

		// Metrics and alert names.
		{Name: "metric-name", Class: "metric", Pattern: metricPattern(metrics), Replace: metricPre + "${s}"},
		{Name: "alert-anchor", Class: "alert", Pattern: `#wva([a-z]{4,})`, Replace: "#llmsm$1", Contexts: []string{CtxText, CtxComment, CtxString}},
		{Name: "alert-name", Class: "alert", Pattern: `\bWVA([A-Z]\w*)`, Replace: "LLMSM$1"},
		{Name: "camel-embedded", Class: "word", Pattern: `([a-z])WVA([A-Z]\w*|\b)`, Replace: "${1}ScalingManager$2"},

		// Environment variables.
		{Name: "env-prefix", Class: "env", Pattern: `\bWVA_`, Replace: envPre},
		{Name: "env-after-escape", Class: "env", Pattern: `\\[a-z"](?P<m>WVA_)`, Replace: envPre},
		{Name: "env-inner", Class: "env", Pattern: `_WVA_`, Replace: "_LLMSM_"},
		{Name: "env-suffix", Class: "env", Pattern: `_WVA\b`, Replace: "_SCALING_MANAGER"},

		// The project's names.
		{Name: "project-name-title", Class: "project", Pattern: `Workload[- ]Variant[- ]Autoscaler(?: \(WVA\))?`, Replace: newName},
		{Name: "project-name-bare", Class: "project", Pattern: `llm-scaling-manager`, Replace: newName},
		pathToo(Rule{Name: "kube-long-name", Class: "k8s-object", Pattern: `workload-variant-autoscaler`, Replace: kubeName}),
		{Name: "go-style-word", Class: "word", Pattern: `\bwva([A-Z])`, Replace: "scalingManager$1"},
		{Name: "pascal-word", Class: "word", Pattern: `\bWva([A-Z])`, Replace: "ScalingManager$1"},
		kept("keep-test-word", "fixture", `\bwvax\b`, "a fixture word with no meaning"),
		{Name: "e2e-word", Class: "word", Pattern: `e2ewva`, Replace: "e2e-scaling-manager"},
		pathToo(Rule{Name: "underscore-word-file", Class: "file-ref", Pattern: `infra_wva|dump_wva_|tail_wva_|wva_threshold|wva_saturation_v2_config`, Fn: renameFileWord}),
		{Name: "underscore-prefix", Class: "word", Pattern: `\bwva_`, Replace: metricPre},
		{Name: "underscore-then-object", Class: "k8s-object", Pattern: `_wva-`, Replace: "_" + kubeName + "-"},
		{Name: "underscore-inner", Class: "word", Pattern: `_wva_`, Replace: "_scaling_manager_"},
		{Name: "underscore-suffix", Class: "word", Pattern: `_wva\b`, Replace: "_scaling_manager"},
		pathToo(Rule{Name: "kube-prefix", Class: "k8s-object", Pattern: `\bwva-`, Replace: kubeName + "-"}),
		pathToo(Rule{Name: "kube-suffix", Class: "k8s-object", Pattern: `-wva\b`, Replace: "-" + kubeName}),
		{Name: "dir-word", Class: "k8s-object", Pattern: `/wva\b`, Replace: "/" + kubeName},
		{Name: "dot-word", Class: "word", Pattern: `\bwva\.`, Replace: kubeName + "."},
		{Name: "bare-word", Class: "word", Pattern: `\bwva\b`, Replace: kubeName},

		// Prose. The first rows see the word before WVA so an article is not
		// doubled, and the last row decides on a capital from what stands
		// before it on the line.
		{Name: "prose-py-ident", Class: "word", Pattern: `\bWVA(?P<x>\s*=\s*re\.|\.(?:match|search|sub)\()`, Replace: "LLMSM${x}", Paths: []string{"**/*.py"}},
		{Name: "prose-dashboard-title", Class: "prose", Pattern: `\bWVA Operational Dashboard`, Replace: "Scaling Manager Operational Dashboard"},
		{Name: "prose-hyphen", Class: "prose", Pattern: `\bWVA-`, Replace: "scaling-manager-"},
		{Name: "prose-the", Class: "prose", Pattern: `\b([Tt])he WVA\b`, Replace: "${1}he scaling manager"},
		{Name: "prose-a", Class: "prose", Pattern: `\b([Aa])n? WVA\b`, Replace: "${1} scaling manager"},
		{Name: "prose-determiner", Class: "prose", Pattern: `\b([Ee]very|[Ee]ach|[Aa]ny|[Tt]his|[Tt]hat|[Oo]ur|[Yy]our|[Nn]o|[Oo]ne|[Aa]nother|[Pp]er|[Mm]issing|[Ss]econdary|[Nn]ew|[Oo]ld|(?i:other)|[Ss]econd) WVA\b`, Replace: "${1} scaling manager"},
		{Name: "prose-plural", Class: "prose", Pattern: `\bWVAs\b`, Replace: "scaling managers"},
		{Name: "prose-hyphen-after", Class: "prose", Pattern: `-WVA\b`, Replace: "-scaling-manager"},
		{Name: "prose-word", Class: "prose", Pattern: `\bWVA\b`, Fn: proseWVA},
		{Name: "prose-title", Class: "prose", Pattern: `\bWva\b`, Replace: "Scaling manager"},
	}
	return rules
}

// identRules map Go identifiers. First match wins.
var identRules = []IdentRule{
	{Name: "ident-import-alias", Class: "go-ident", Pattern: `^wvav1alpha1$`, Replace: "llmdv1alpha1"},
	{Name: "ident-leading-upper", Class: "go-ident", Pattern: `^WVA`, Replace: "LLMSM"},
	{Name: "ident-leading-lower", Class: "go-ident", Pattern: `^wva`, Replace: "scalingManager"},
	{Name: "ident-embedded-upper", Class: "go-ident", Pattern: `WVA`, Replace: "ScalingManager"},
	{Name: "ident-embedded-pascal", Class: "go-ident", Pattern: `Wva`, Replace: "ScalingManager"},
}

// metricPattern is an alternation of the metric suffixes, longest first so a
// name is not cut at a shorter one, which also takes _bucket and friends.
func metricPattern(metrics []string) string {
	seen := map[string]bool{}
	var alts []string
	for _, m := range metrics {
		s := strings.TrimPrefix(strings.TrimPrefix(m, "wva_"), "llmsm_")
		if !seen[s] {
			seen[s] = true
			alts = append(alts, regexp.QuoteMeta(s))
		}
	}
	if len(alts) == 0 {
		return `\bwva_(?P<s>\A\z)` // a pattern that cannot match
	}
	return `\bwva_(?P<s>(?:` + strings.Join(alts, "|") + `)\w*)`
}

// renameFileWord is the new form of an old word inside a file name.
func renameFileWord(claimed, _ string) string {
	r := strings.NewReplacer("infra_wva", "infra_scaling_manager", "wva_threshold", "scaling_manager_threshold",
		"wva_saturation_v2_config", "saturation_v2_config", "dump_wva_", "dump_scaling_manager_",
		"tail_wva_", "tail_scaling_manager_", "wva", kubeName)
	return r.Replace(claimed)
}

// moduleSwap keeps what follows the module path and swaps the path itself.
func moduleSwap(target string) func(string, string) string {
	return func(claimed, _ string) string {
		for _, old := range []string{"github.com/llm-d/llm-d-workload-variant-autoscaler", "github.com/ev-shindin/llm-scaling-manager"} {
			if rest, ok := strings.CutPrefix(claimed, old); ok {
				return target + rest
			}
		}
		return claimed
	}
}

// proseWVA is "the scaling manager", capitalised where a sentence starts.
func proseWVA(_, before string) string {
	trimmed := strings.TrimRight(before, " \t*_`\"'>([")
	line := strings.TrimLeft(before, " \t")
	switch {
	case strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#!") && strings.TrimLeft(line, "# ") == "":
		return "Scaling manager"
	case strings.HasSuffix(trimmed, "//") || strings.HasSuffix(trimmed, "#") || strings.HasSuffix(trimmed, "|"),
		strings.HasSuffix(trimmed, "-"), trimmed == "":
		return "The scaling manager"
	case strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, "!") || strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, ":"):
		return "The scaling manager"
	}
	return "the scaling manager"
}
