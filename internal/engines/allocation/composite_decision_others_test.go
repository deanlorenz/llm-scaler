package allocation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// TestResolveSOSeparatesSaturationFromTheOthers: Others is the largest need
// among the analyzers other than saturation, also when saturation's is larger
// than all of them; the composite prices saturation's need as its own demand
// and the others' at saturation's PRC.
func TestResolveSOSeparatesSaturationFromTheOthers(t *testing.T) {
	sat := makeAnalyzer(domain.SaturationAnalyzerName, 800, 100) // 8
	a := makeAnalyzer("a", 50, 10)                               // 5
	b := makeAnalyzer("b", 60, 10)                               // 6

	d := ResolveSO([]NamedAnalyzerResult{a, sat, b}, &sat, "v")
	assert.Equal(t, 8.0, d.N)
	assert.True(t, d.Saturation)
	assert.True(t, d.OthersOK)
	assert.Equal(t, 6.0, d.Others)

	d = ResolveSO([]NamedAnalyzerResult{b, a}, &sat, "v")
	assert.Equal(t, 6.0, d.N)
	assert.False(t, d.Saturation, "saturation unlisted")
	assert.Equal(t, 6.0, d.Others)

	d = ResolveSO([]NamedAnalyzerResult{sat}, &sat, "v")
	assert.True(t, d.Saturation)
	assert.False(t, d.OthersOK)
}
