package steadystate

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// composeAnalyzerResults must take saturation's raw result as-is for the
// sat-only case: today's default, and the only case exercised in production.
// Any behavior change here for that case would violate the refactor's
// regression invariant.
func TestComposeAnalyzerResults_SaturationOnlyIsPassthrough(t *testing.T) {
	sat := rawAnalyzerResult{
		name:      domain.SaturationAnalyzerName,
		result:    &domain.AnalyzerResult{AnalyzerName: domain.SaturationAnalyzerName, TotalDemand: 42},
		scaleUp:   0.8,
		scaleDown: 0.4,
	}

	composed := composeAnalyzerResults([]rawAnalyzerResult{sat})

	assert.Equal(t, sat, composed)
}

// Saturation is the fallback candidate even when it is not the first entry —
// composeAnalyzerResults must find it by name, not by position.
func TestComposeAnalyzerResults_FindsSaturationRegardlessOfPosition(t *testing.T) {
	other := rawAnalyzerResult{
		name:   "throughput",
		result: &domain.AnalyzerResult{AnalyzerName: "throughput", TotalDemand: 7},
	}
	sat := rawAnalyzerResult{
		name:   domain.SaturationAnalyzerName,
		result: &domain.AnalyzerResult{AnalyzerName: domain.SaturationAnalyzerName, TotalDemand: 42},
	}

	composed := composeAnalyzerResults([]rawAnalyzerResult{other, sat})

	assert.Equal(t, sat, composed)
}
