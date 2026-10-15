package alerts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kueueAlert is the named alert's rule, failing if it is absent.
type kueueAlert struct {
	Expr, For, Severity, Summary, Description, Runbook string
}

func findAlert(t *testing.T, name string) kueueAlert {
	t.Helper()
	for _, group := range loadRule(t).Spec.Groups {
		for _, r := range group.Rules {
			if r.Alert == name {
				return kueueAlert{Expr: r.Expr, For: r.For, Severity: r.Labels["severity"],
					Summary: r.Annotations["summary"], Description: r.Annotations["description"],
					Runbook: r.Annotations["runbook_url"]}
			}
		}
	}
	require.Failf(t, "alert not found", "no alert %q in the PrometheusRule", name)
	return kueueAlert{}
}

// R8, R11: the stale alert fires per namespace and says which, because a
// single namespace's failing ResourceQuota read freezes that namespace alone;
// a summary that blamed the Kueue reader sent on-call to the wrong read.
func TestKueueReaderStaleNamesTheNamespace(t *testing.T) {
	a := findAlert(t, "WVAKueueReaderStale")
	assert.Equal(t, []string{"wva_quota_borrow_withheld"}, ExtractMetricNames(a.Expr))
	assert.Contains(t, a.Expr, `{reason="stale"}`)
	assert.Contains(t, a.Expr, "max by (limiter_name, exported_namespace)")
	assert.Regexp(t, `==\s*1\s*$`, a.Expr)
	assert.Equal(t, "5m", a.For)
	assert.Equal(t, "warning", a.Severity)
	assert.Contains(t, a.Summary, "$labels.exported_namespace")
	assert.Contains(t, a.Description, "ResourceQuota", "the description names the per-namespace cause too")
	assert.True(t, strings.HasSuffix(a.Runbook, "#wvakueuereaderstale"))
}

// R3: Kueue reachable but its controller not running froze borrowing with no
// alert at all -- the version gauge reads 1 under assumeTestedVersion.
func TestKueueControllerDownHasItsOwnAlert(t *testing.T) {
	a := findAlert(t, "WVAKueueControllerDown")
	assert.Equal(t, []string{"wva_quota_borrow_withheld"}, ExtractMetricNames(a.Expr))
	assert.Contains(t, a.Expr, `{reason="kueue-controller-down"}`)
	assert.Equal(t, "warning", a.Severity)
	assert.Contains(t, a.Description, "KUEUE_NAMESPACE=", "how a namespace-scoped install gets the Role")
	assert.Contains(t, a.Description, "leader-election ID", "a custom Kueue leader-election ID reads as down (S5)")
	assert.True(t, strings.HasSuffix(a.Runbook, "#wvakueuecontrollerdown"))
}

// R3: an untested Kueue release withholds borrowing everywhere; it is said,
// at info, not left silent.
func TestKueueVersionUntestedIsInformational(t *testing.T) {
	a := findAlert(t, "WVAKueueVersionUntested")
	assert.Equal(t, []string{"wva_quota_borrow_withheld"}, ExtractMetricNames(a.Expr))
	assert.Contains(t, a.Expr, `{reason="kueue-version-untested"}`)
	assert.Equal(t, "info", a.Severity)
	assert.True(t, strings.HasSuffix(a.Runbook, "#wvakueueversionuntested"))
}

// R2: aggregated per controller instance, as one controller's view; a bare
// max() hid which one could not read the version.
func TestKueueVersionUnknownIsPerControllerInstance(t *testing.T) {
	a := findAlert(t, "WVAKueueVersionUnknown")
	assert.Contains(t, a.Expr, "max by (controller_instance) (wva_kueue_controller_version_known)")
	assert.Contains(t, a.Description, "assumeTestedVersion: true", "the documented fix")
}

// R6: the drift alert compares Kueue's own figures with the scaling manager's
// reading of them; a second scaling manager cannot cause it.
func TestKueueBorrowDriftNamesNoImpossibleCause(t *testing.T) {
	a := findAlert(t, "WVAKueueBorrowDrift")
	assert.NotContains(t, a.Description, "second scaling manager")
}

// R11: the unplaced alert is per variant, names it, and waits past the
// placement timeout before paging.
func TestKueuePodsUnplacedAlert(t *testing.T) {
	a := findAlert(t, "WVAKueuePodsUnplaced")
	assert.Equal(t, []string{"wva_kueue_unplaced_pods"}, ExtractMetricNames(a.Expr))
	assert.Contains(t, a.Expr, "max by (exported_namespace, variant_name)")
	assert.Regexp(t, `>\s*0\s*$`, a.Expr)
	assert.Equal(t, "5m", a.For)
	assert.Equal(t, "warning", a.Severity)
	assert.Contains(t, a.Summary, "$labels.exported_namespace")
	assert.Contains(t, a.Summary, "$labels.variant_name")
	assert.True(t, strings.HasSuffix(a.Runbook, "#replicas-dropped-under-kueue-borrowing"))
}
