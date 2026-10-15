package alerts

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// prometheusRule is the subset of the PrometheusRule this check reads.
type prometheusRule struct {
	Spec struct {
		Groups []struct {
			Name  string `json:"name"`
			Rules []struct {
				Alert       string            `json:"alert"`
				Expr        string            `json:"expr"`
				For         string            `json:"for"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"rules"`
		} `json:"groups"`
	} `json:"spec"`
}

func loadRule(t *testing.T) prometheusRule {
	t.Helper()
	path := filepath.Join(RepoRoot(), "config", "components", "prometheus-alerts", "prometheusrule.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "reading %s", path)

	var rule prometheusRule
	require.NoError(t, yaml.Unmarshal(raw, &rule))
	require.NotEmpty(t, rule.Spec.Groups, "PrometheusRule has no groups")
	return rule
}

// The check that used to run only against a live cluster. An alert naming a
// metric that does not exist matches nothing and fires never, which looks exactly
// like a healthy cluster -- so it is not the sort of mistake that surfaces on its
// own. wva_node_access_denied shipped that way.
func TestAlertsReferenceOnlyDefinedMetrics(t *testing.T) {
	known, err := WVAMetricNames()
	require.NoError(t, err)
	t.Logf("%d wva_ metric constants defined", len(known))

	rule := loadRule(t)
	var alerts int
	for _, group := range rule.Spec.Groups {
		for _, r := range group.Rules {
			if r.Alert == "" {
				continue
			}
			alerts++
			for _, name := range ExtractMetricNames(r.Expr) {
				assert.True(t, IsKnown(name, known),
					"alert %q references unknown metric %q\n  expr: %s\n"+
						"  If this is a new WVA metric, define it in internal/constants/metrics.go.\n"+
						"  If it is an external metric, it needs an explicit allowance here.",
					r.Alert, name, r.Expr)
			}
		}
	}
	assert.NotZero(t, alerts, "no alerts found -- the check would pass vacuously")
}

func TestAlertsHaveRequiredFields(t *testing.T) {
	rule := loadRule(t)
	for _, group := range rule.Spec.Groups {
		for _, r := range group.Rules {
			if r.Alert == "" {
				continue
			}
			assert.NotEmpty(t, r.Expr, "alert %q has no expression", r.Alert)
			assert.NotEmpty(t, r.Labels["severity"], "alert %q has no severity", r.Alert)
			assert.NotEmpty(t, r.Annotations["summary"], "alert %q has no summary", r.Alert)
			assert.NotEmpty(t, r.Annotations["description"],
				"alert %q has no description -- the description is where an operator "+
					"finds out what to DO about it", r.Alert)
		}
	}
}

// The stripping is what makes a label matcher safe to write. Without it the
// contents of reason=~"..." are scanned as identifiers, and four of them are not
// PromQL keywords, so a correct alert fails.
func TestExtractMetricNamesIgnoresLabelMatcherContents(t *testing.T) {
	for _, tc := range []struct {
		name string
		expr string
		want []string
	}{
		{
			name: "regex matcher values are not metric names",
			expr: `wva_model_scaling_blocked{reason=~"variant-floor|policy-forbids-zero"} == 1`,
			want: []string{"wva_model_scaling_blocked"},
		},
		{
			name: "equality matcher values are not metric names",
			expr: `wva_model_scaling_blocked{reason="no-wake-signal"} == 1`,
			want: []string{"wva_model_scaling_blocked"},
		},
		{
			name: "label_replace's string arguments are not metric names",
			expr: `label_replace(wva_current_replicas, "model", "$1", "variant_name", "(.*)-.*")`,
			want: []string{"wva_current_replicas"},
		},
		{
			name: "aggregation and rate survive",
			expr: `sum by (query_type, reason) (rate(wva_metrics_collection_errors_total[5m])) > 0.0833`,
			want: []string{"wva_metrics_collection_errors_total"},
		},
		{
			name: "a subquery's range and step are not metric names",
			expr: `count_over_time((wva_slo_observed_seconds > wva_slo_target_seconds)[30m:1m]) >= 10`,
			want: []string{"wva_slo_observed_seconds", "wva_slo_target_seconds"},
		},
		{
			name: "multiple metrics are all found",
			expr: `wva_current_replicas == 0 and on() wva_desired_replicas > 0`,
			want: []string{"wva_current_replicas", "wva_desired_replicas"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ExtractMetricNames(tc.expr))
		})
	}
}

// Deriving the list from the constants is the point: a metric added to
// internal/constants must not need a second edit somewhere else to be usable in
// an alert.
func TestWVAMetricNamesComesFromTheConstants(t *testing.T) {
	known, err := WVAMetricNames()
	require.NoError(t, err)

	assert.True(t, known["wva_model_scaling_blocked"])
	assert.True(t, known["wva_current_replicas"])
	assert.True(t, known["wva_node_access_denied"])
	// Reason slugs and label names live in the same file and are not metrics.
	assert.False(t, known["variant-floor"])
	assert.False(t, known["namespace"])
}

// runbookPrefix is where every runbook_url points: the docs on main.
const runbookPrefix = "https://github.com/ev-shindin/llm-scaling-manager/blob/main/"

// Every alert names a runbook, and the runbook resolves: the URL is under the
// repository's main branch, the file exists in this tree, and the anchor is a
// heading in it. An anchor left behind by a renamed heading sends the operator
// paged at night to the top of a long page, which is where nobody finds the fix.
func TestAlertsHaveResolvableRunbooks(t *testing.T) {
	rule := loadRule(t)
	var checked int
	for _, group := range rule.Spec.Groups {
		for _, r := range group.Rules {
			if r.Alert == "" {
				continue
			}
			url := r.Annotations["runbook_url"]
			if !assert.NotEmpty(t, url, "alert %q has no runbook_url", r.Alert) {
				continue
			}
			rest, ok := strings.CutPrefix(url, runbookPrefix)
			if !assert.True(t, ok, "alert %q: runbook_url %q is not under %s", r.Alert, url, runbookPrefix) {
				continue
			}
			file, anchor, _ := strings.Cut(rest, "#")
			if !assert.NotEmpty(t, anchor, "alert %q: runbook_url %q names no anchor", r.Alert, url) {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(RepoRoot(), filepath.FromSlash(file)))
			if !assert.NoError(t, err, "alert %q: runbook file %s", r.Alert, file) {
				continue
			}
			assert.Contains(t, headingAnchors(string(raw)), anchor,
				"alert %q: %s has no heading with anchor #%s", r.Alert, file, anchor)
			checked++
		}
	}
	assert.NotZero(t, checked, "no runbook was checked -- the test would pass vacuously")
}

// headingAnchors returns the anchors GitHub generates for the Markdown headings
// in md: lowercased, punctuation other than '-' and '_' dropped, spaces turned
// into hyphens, and a repeated heading suffixed -1, -2 and so on. Headings
// inside fenced code blocks are not headings.
func headingAnchors(md string) map[string]bool {
	anchors := make(map[string]bool)
	seen := make(map[string]int)
	inFence := false
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimSpace(strings.TrimLeft(line, "#"))
		var b strings.Builder
		for _, r := range strings.ToLower(text) {
			switch {
			case r == ' ':
				b.WriteRune('-')
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
				b.WriteRune(r)
			}
		}
		slug := b.String()
		if n := seen[slug]; n > 0 {
			anchors[slug+"-"+strconv.Itoa(n)] = true
		} else {
			anchors[slug] = true
		}
		seen[slug]++
	}
	return anchors
}

func TestHeadingAnchorsMatchGitHub(t *testing.T) {
	got := headingAnchors("# Title\n## The controller is not Ready or crash-looping\n" +
		"```\n# not a heading\n```\n### GPUs held, or quota spent\n## Title\n")
	for _, want := range []string{
		"title",
		"the-controller-is-not-ready-or-crash-looping",
		"gpus-held-or-quota-spent",
		"title-1",
	} {
		assert.True(t, got[want], "missing anchor %q in %v", want, got)
	}
	assert.False(t, got["not-a-heading"], "a line inside a code fence is not a heading")
}
