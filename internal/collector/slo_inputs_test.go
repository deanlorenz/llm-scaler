package collector

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/registration"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/collector/source"
)

// TestReplicaCollectionFetchesSLOInputsOnlyWhereTheAnalyzerRuns pins that a
// fleet with no slo analyzer pays nothing for the queries only it reads, and
// that a namespace where it ran gets them.
func TestReplicaCollectionFetchesSLOInputsOnlyWhereTheAnalyzerRuns(t *testing.T) {
	var asked []string
	src := &mockMetricsSource{refreshFunc: func(_ context.Context, spec source.RefreshSpec) (map[string]*source.MetricResult, error) {
		asked = append(asked, spec.Queries...)
		return map[string]*source.MetricResult{}, nil
	}}
	c := NewReplicaMetricsCollector(src, nil, nil, nil, nil)

	_, _, err := c.queryReplicaSeries(context.Background(), "m", "plain", nil)
	require.NoError(t, err)
	require.Contains(t, asked, registration.QueryPrefillComputedTokenRate,
		"the control: the other replica queries are still fetched")
	for _, q := range sloInputQueries {
		assert.NotContains(t, asked, q, "no model here runs the slo analyzer")
	}

	c.AddSLOInputNamespace("slo")
	asked = nil
	_, _, err = c.queryReplicaSeries(context.Background(), "m", "slo", nil)
	require.NoError(t, err)
	for _, q := range sloInputQueries {
		assert.Contains(t, asked, q)
	}
	asked = nil
	_, _, err = c.queryReplicaSeries(context.Background(), "m", "plain", nil)
	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(asked, func(q string) bool { return slices.Contains(sloInputQueries, q) }),
		"only the namespaces named")
	assert.Contains(t, agnosticReplicaQueries, registration.QueryAvgPrefillTime,
		"the shared list itself is left whole")

	c.ResetSLOInputNamespaces()
	assert.False(t, c.CollectsSLOInputs("slo"), "each cycle starts with no namespace marked")
}
