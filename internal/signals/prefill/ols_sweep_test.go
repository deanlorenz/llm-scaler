package prefill

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/settle/settlerow"
)

// olsSweepRecording is the ols-sweep learner golden's recording: one decode
// and one prefill pod, P/D on vLLM, swept from 1 to 30 requests/s and from
// 1000- to 6000-token prompts. The prefill pod never queued.
var olsSweepRecording = filepath.Join("..", "..", "engines", "analyzers", "learnreplay",
	"testdata", "goldens", "ols-sweep", "recording.jsonl.gz")

// replayOLSSweep feeds every Analyze input of the recording to an observer,
// with the settle tracker observed from the same rows as the engine observes
// it, and returns the prefill variant's last estimate and how many cycles
// it saw.
func replayOLSSweep(t *testing.T) (Estimate, int) {
	t.Helper()
	gz, err := os.ReadFile(olsSweepRecording)
	require.NoError(t, err)
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	require.NoError(t, err)
	raw, err := io.ReadAll(zr)
	require.NoError(t, err)
	rec, err := learnrec.Read(raw)
	require.NoError(t, err)

	tr := settle.NewTracker()
	o := NewObserver()
	var est Estimate
	cycles := 0
	for _, ev := range rec.Events {
		switch ev.Kind {
		case "settle.Observe":
			var d struct {
				Namespace, ModelID string
				Interval           time.Duration
			}
			require.NoError(t, ev.DecodeData(&d))
			tr.SetInterval(d.Interval)
			continue
		case "saturation.Analyze":
		default:
			continue
		}
		// The Analyze event as saturation records it (AnalyzeEvent); only
		// its input is read.
		var d struct {
			Input           learnrec.Input
			Records, Result learnrec.Raw
			Error           string
		}
		require.NoError(t, ev.DecodeData(&d))
		roles := map[string]string{}
		for _, vs := range d.Input.VariantStates {
			roles[vs.VariantName] = vs.Role
		}
		model := d.Input.Namespace + "/" + d.Input.ModelID
		tr.Observe(model, settlerow.Of(d.Input.ReplicaMetrics, roles), ev.Now)
		var rows []domain.ReplicaMetrics
		for _, rm := range d.Input.ReplicaMetrics {
			if domain.CanonicalRole(roles[rm.VariantName]) == domain.RolePrefill {
				rows = append(rows, rm)
			}
		}
		if len(rows) == 0 {
			continue
		}
		est = o.Observe(Cycle{
			Key: "prefill", Engine: inferenceengine.EngineVLLM, Disaggregated: true, Rows: rows,
			Settle: &Settle{View: tr, Model: model}, Now: ev.Now,
		})
		cycles++
	}
	return est, cycles
}

// On ols-sweep, P1 fits a and refuses V_P and f
// (docs/proposals/slo-capacity-planner.md, the PR plan; the TTFT page's
// "What is derivable today": the one prefill pod never queued, so it
// supports a and the low-load slope, not V_P or f).
func TestOLSSweep_FitsAAndRefusesVPAndF(t *testing.T) {
	est, cycles := replayOLSSweep(t)
	require.Equal(t, 226, cycles, "the recording's Analyze cycles")

	// a: the page measured 10.1 ms at low load; the overhead
	// AvgTTFT - AvgServiceTime is never below 2.5 ms with no queue.
	require.Empty(t, est.ARefused)
	assert.GreaterOrEqual(t, est.ASamples, MinFixedSamples)
	assert.InDelta(t, 0.0101, est.A, 0.002, "a")
	assert.True(t, est.AOverheadKnown)
	assert.GreaterOrEqual(t, est.AOverhead, 0.0025)
	assert.GreaterOrEqual(t, est.APromptMax, MinPromptSpread*est.APromptMin)
	// The slope bound is a validation signal, far above the 30k tok/s the
	// pod was offered at its peak.
	assert.Greater(t, est.SlopeBound, 30000.0)

	// V_P: no busy minute (none can be recognised without the queue's
	// minimum), no queue ever, no back-pressure bound.
	assert.Equal(t, NoPrefillCapacity, est.VPRefused)
	assert.Zero(t, est.VP)
	assert.Empty(t, est.VPSource)

	// f: one prefill pod.
	assert.Equal(t, OnePod, est.FRefused)
	assert.Zero(t, est.F)
	t.Logf("ols-sweep: a %.4f s (intercept %.4f, overhead %.4f) over %d minutes, prompts %.0f..%.0f; slope bound %.0f tok/s; V_P %s; f %s",
		est.A, est.AIntercept, est.AOverhead, est.ASamples, est.APromptMin, est.APromptMax, est.SlopeBound, est.VPRefused, est.FRefused)
}
