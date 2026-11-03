package slo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learn"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils"
)

// The forget-learned-state annotation reaches the slo line (issue #431),
// behind SwitchITLForgetReachesTheLine. Off is the line before the fix: the
// negative control the switch must fail.

// forgetSibling is a second variant whose name agg is a prefix of, so a
// forget of agg that matched by name alone would take its line too.
var forgetSibling = domain.VariantReplicaState{VariantName: "agg2", Role: domain.RoleBoth,
	AcceleratorName: "H100", GPUsPerReplica: 1, CurrentReplicas: 3}

// forgetCycle observes agg's and agg2's rows as the engine does, prices
// both, moves the clock a cycle on, and returns what each line read.
func forgetCycle(r *settleRig) (agg, sibling itl.Reading) {
	r.t.Helper()
	rows := append(settleFleet(),
		row("agg2", "agg2-a", 0.30), row("agg2", "agg2-b", 0.45), row("agg2", "agg2-c", 0.60))
	in := domain.AnalyzerInput{Namespace: "ns", ModelID: "m", Config: policy(44*time.Millisecond, 0),
		VariantStates: []domain.VariantReplicaState{settleAgg, forgetSibling}, ReplicaMetrics: rows}
	r.tr.Observe(utils.GetNamespacedKey("ns", "m"),
		settlerow.Of(rows, map[string]string{"agg": domain.RoleBoth, "agg2": domain.RoleBoth}), r.now)
	fleet := readFleet(in)
	_, _, a := r.a.priceVariant(in, settleAgg, windowKey("ns", "m", settleAgg), fleet, 0.044, 0.85, r.now)
	_, _, s := r.a.priceVariant(in, forgetSibling, windowKey("ns", "m", forgetSibling), fleet, 0.044, 0.85, r.now)
	r.now = r.now.Add(settleCycle)
	return a.line, s.line
}

// A forget drops the variant's line -- its window and the intercept its
// full fit learned -- and nothing of a sibling's. Off, as before, the line
// keeps pricing from the readings taken before the forget.
func TestForget_TheAnnotationForgetsTheLine(t *testing.T) {
	for _, on := range []bool{true, false} {
		r := newSettleRig(t, map[string]bool{SwitchITLForgetReachesTheLine: on})
		var agg, sib itl.Reading
		for range settleCycles + 10 {
			agg, sib = forgetCycle(r)
		}
		replicas := len(settleFleet())
		require.Equal(t, itl.TierOLS, agg.Tier, "premise: agg's line is a full fit")
		require.True(t, agg.BaselineLearned, "premise: agg's line learned its intercept")
		require.Greater(t, agg.Held, replicas, "premise: agg's window holds more than one cycle")
		require.Greater(t, sib.Held, replicas, "premise: the sibling's window holds more than one cycle")

		dropped := r.a.ForgetVariant("ns", "m", "agg")
		after, sibAfter := forgetCycle(r)
		if !on {
			assert.Zero(t, dropped, "switch off: nothing is forgotten")
			assert.Greater(t, after.Held, replicas, "switch off: the window kept the readings from before the forget")
			assert.True(t, after.BaselineLearned, "switch off: the intercept stays")
			assert.Equal(t, itl.TierOLS, after.Tier, "switch off: the line still prices from the old readings")
			continue
		}
		assert.Equal(t, 2, dropped, "the window and the intercept")
		assert.Equal(t, replicas, after.Held, "the window holds this cycle's readings alone")
		assert.False(t, after.BaselineStored, "the intercept is gone")
		assert.NotEqual(t, itl.TierOLS, after.Tier, "no full fit from one cycle")
		assert.Greater(t, sibAfter.Held, replicas, "the sibling agg2 keeps its window")
		assert.True(t, sibAfter.BaselineLearned, "and its intercept")
	}
}

// A forget of a variant the analyzer holds nothing of, or of another model's
// variant of the same name, drops nothing.
func TestForget_OnlyTheNamedModelsVariant(t *testing.T) {
	r := newSettleRig(t, nil)
	for range settleCycles + 10 {
		forgetCycle(r)
	}
	assert.Zero(t, r.a.ForgetVariant("ns", "other-model", "agg"))
	assert.Zero(t, r.a.ForgetVariant("other-ns", "m", "agg"))
	assert.Zero(t, r.a.ForgetVariant("ns", "m", "ag"), "a name agg starts with")
	agg, _ := forgetCycle(r)
	assert.Equal(t, itl.TierOLS, agg.Tier, "agg's line is untouched")
	// Control: the same analyzer forgets agg when asked by its own name.
	assert.Equal(t, 2, r.a.ForgetVariant("ns", "m", "agg"))
}

// The forget also drops the engine arguments the line was learned under: a
// line made anew records them again, rather than reading the next
// arguments as a change and sweeping the new line.
func TestForget_DropsTheArgumentsTheLineWasLearnedUnder(t *testing.T) {
	a := New(nil, nil)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	key := windowKey("ns", "m", settleAgg)
	sib := windowKey("ns", "m", forgetSibling)
	a.linesUnderArgs(key, "args-1", now)
	a.linesUnderArgs(sib, "args-1", now)
	a.mu.Lock()
	a.seen[key], a.seen[sib] = now, now
	a.mu.Unlock()

	a.ForgetVariant("ns", "m", "agg")
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.NotContains(t, a.args, key)
	assert.NotContains(t, a.seen, key)
	assert.Contains(t, a.args, sib, "control: the sibling keeps its arguments")
	assert.Contains(t, a.seen, sib)
}

// Forget reaches every learner on learn's list, by name: each is either
// the line this analyzer keeps or a learner it holds nothing of. A name it
// does not know is not handled.
func TestForget_EveryLearnerOnTheListIsHandled(t *testing.T) {
	a := New(nil, nil)
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, name := range learn.Learners() {
		_, handled := a.forgetLearnerLocked(name, "ns", "m", "v")
		assert.True(t, handled, "the learner %q is on learn's list and the slo analyzer has no forget decision for it", name)
	}
	dropped, handled := a.forgetLearnerLocked("no-such-learner", "ns", "m", "v")
	assert.False(t, handled, "negative control: a name the analyzer does not know is not handled")
	assert.Zero(t, dropped)
}
