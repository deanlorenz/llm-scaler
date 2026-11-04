package saturation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// V_P's busy minutes through Analyze (SwitchPrefillBusyMinutes): a prefill
// row whose queue minimum over the minute was read and above zero is a busy
// minute, and V_P is the median computed-token rate of such minutes.

// busyMinute is one prefill minute: the computed-token rate, and the queue
// minimum with its presence bit. The queue's peak is 3 in every minute.
type busyMinute struct {
	rate  float64
	qmin  float64
	known bool
}

// busyVP is the V_P the busy minutes below are taken at: a work-conserving
// pod that never ran out of work computes at V_P, read with the noise of a
// one-minute rate either side.
const busyVP = 24000.0

// busyMinutes is five minutes in which the pod's queue never emptied, at
// rates whose median is busyVP. Their peak under a queue, the least of the
// best three in a row, is 23500: what P1 read with no queue minimum.
func busyMinutes() []busyMinute {
	rates := []float64{23000, 25000, 24000, 23500, 24500}
	out := make([]busyMinute, 0, len(rates))
	for _, r := range rates {
		out = append(out, busyMinute{rate: r, qmin: 2, known: true})
	}
	return out
}

// busyWarmup is how many cycles the settle tracker holds a new replica out
// for (prefill.SteadyFor at a minute a cycle): they are run first, with no
// queue minimum, so that every minute of a test is admitted.
const busyWarmup = 2

// cycleWith runs one cycle of the harness's P/D model with the prefill row
// m, and returns its observed line.
func (h *observeHarness) cycleWith(m busyMinute) string {
	h.t.Helper()
	states := []domain.VariantReplicaState{
		{VariantName: "decode", Role: domain.RoleDecode, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
		{VariantName: "prefill", Role: domain.RolePrefill, AcceleratorName: "H100", CurrentReplicas: 1, GPUsPerReplica: 1},
	}
	dec := makeReplicaMetrics("decode-0", "decode", 5000, 16000, 0, 4000, 500)
	pre := makeReplicaMetrics("prefill-0", "prefill", 0, 16000, 3, 4000, 1)
	pre.PrefillComputedTokenRate, pre.PrefillComputedTokenRateKnown = m.rate, true
	pre.RequestRate, pre.RequestRateKnown = m.rate/4000, true
	pre.QueueMin, pre.QueueMinKnown = m.qmin, m.known
	h.lines = nil
	_, err := h.a.Analyze(h.ctx, makeAnalyzerInput([]domain.ReplicaMetrics{dec, pre}, states))
	require.NoError(h.t, err)
	h.clock = h.clock.Add(time.Minute)
	obs := observedLines(h.lines)
	require.Len(h.t, obs, 1)
	return obs[0]
}

// busyRun drives a harness through the warm-up and then minutes, with
// SwitchPrefillBusyMinutes set to on, and returns the last observed line.
func busyRun(t *testing.T, on bool, minutes []busyMinute) string {
	t.Helper()
	h := newObserveHarness(t, &capacity.EngineParams{Engine: inferenceengine.EngineVLLM,
		MaxNumBatchedTokens: 8192, EffectiveMaxBatchedTokens: 8192})
	require.NoError(t, h.a.SetSwitchesForReplay(map[string]bool{SwitchPrefillBusyMinutes: on}))
	for range busyWarmup {
		h.cycleWith(busyMinute{rate: 1000})
	}
	var last string
	for _, m := range minutes {
		last = h.cycleWith(m)
	}
	require.Contains(t, last, `"downstreamSaturated"=false`, "decode must not back-pressure prefill here")
	require.Contains(t, last, `"admitted"=1`)
	return last
}

// Known truth: five busy minutes read V_P exactly, the median of their
// rates, and not as a lower bound. With the switch off, the same minutes are
// only a peak under a queue, as P1 shipped: 23500, below V_P.
func TestPrefillBusy_BusyMinutesRecoverVP(t *testing.T) {
	on := busyRun(t, true, busyMinutes())
	assert.Contains(t, on, `"vpSource"="busy-minutes"`)
	assert.Contains(t, on, fmt.Sprintf(`"vp"=%v`, busyVP))
	assert.Contains(t, on, `"vpLowerBound"=false`)
	assert.Contains(t, on, `"busyMinutes"=5`)
	assert.Contains(t, on, `"vpSamples"=5`)
	assert.Contains(t, on, `"queueMinPods"=1`)

	off := busyRun(t, false, busyMinutes())
	assert.Contains(t, off, `"vpSource"="peak-under-queue"`)
	assert.Contains(t, off, `"vp"=23500`)
	assert.Contains(t, off, `"busyMinutes"=0`)
	assert.Contains(t, off, `"queueMinPods"=0`)
}

// A minute whose queue emptied (a queue minimum of zero), or whose queue
// minimum was not read (a value with QueueMinKnown false, which a reader
// must not take for one), is not busy, however fast it ran: decoy minutes of
// each kind at 40000 tokens/s, a queue on their peak, interleaved with the
// five busy ones, leave five busy minutes and V_P at their median. Counted
// as busy, the decoys would raise it to 25000 or above.
func TestPrefillBusy_AnEmptiedOrUnreadMinuteIsNotBusy(t *testing.T) {
	for name, decoy := range map[string]busyMinute{
		"emptied": {rate: 40000, qmin: 0, known: true},
		"unread":  {rate: 40000, qmin: 2, known: false},
	} {
		t.Run(name, func(t *testing.T) {
			busy := busyMinutes()
			minutes := make([]busyMinute, 0, 2*len(busy))
			for _, m := range busy {
				minutes = append(minutes, m, decoy)
			}
			last := busyRun(t, true, minutes)
			assert.Contains(t, last, `"busyMinutes"=5`)
			assert.Contains(t, last, `"vpSource"="busy-minutes"`)
			assert.Contains(t, last, fmt.Sprintf(`"vp"=%v`, busyVP))
		})
	}
}

// Four busy minutes are not enough (prefill.MinBusyMinutes): with the fifth
// replaced by an emptied minute V_P falls back to the peak under a queue.
func TestPrefillBusy_FourBusyMinutesAreNotASource(t *testing.T) {
	minutes := busyMinutes()
	minutes[4].qmin = 0
	last := busyRun(t, true, minutes)
	assert.Contains(t, last, `"busyMinutes"=4`)
	assert.Contains(t, last, `"vpSource"="peak-under-queue"`)
}
