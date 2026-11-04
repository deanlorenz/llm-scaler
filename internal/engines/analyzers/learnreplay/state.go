package learnreplay

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learn"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// The what-if planner's replay (docs/proposals/slo-capacity-planner.md,
// "Data per mode", Recording): replay a recording through the real
// analyzers, stop after the cycle asked for, and read what they had learned
// then. The answer is what the controller knew at that cycle.

// StateQuery names the point of a recording a what-if question is asked at.
type StateQuery struct {
	// Namespace and ModelID name the model. An empty ModelID takes the
	// recording's only model; an empty Namespace matches any, and a model
	// found in two namespaces is an error.
	Namespace, ModelID string
	// Cycle is the model's cycle to stop after, counting its saturation
	// Analyze calls from 1; 0 is its last.
	Cycle int
}

// State is what the analyzers had learned of one model when the replay
// stopped.
type State struct {
	Namespace, ModelID string
	// Cycle is the model's cycle the replay stopped after, and Cycles how
	// many of its cycles the replay read (Cycle, unless the query asked for
	// the last).
	Cycle int
	// FirstAt is the clock of the model's first cycle and At that of Cycle.
	FirstAt, At time.Time
	// Policy is the scaling policy the cycle was analyzed under; nil when
	// the recorded config was no policy.
	Policy *config.ScalingPolicy
	// Variants is each variant the cycle priced, by name.
	Variants []VariantState
	// TPOTWindows is the router's per-request TPOT histogram of every cycle
	// up to Cycle that recorded one, oldest first, as bucket rates: the
	// input to whatif.TPOTRatioFromWindows. Empty in a recording made before
	// the recorder carried it.
	TPOTWindows []whatif.TPOTWindow
}

// VariantState is one variant's learned state at the cycle: everything the
// decode planner prices from.
type VariantState struct {
	Variant, Role string
	// Replicas is the variant's replica count as the cycle priced it.
	Replicas int
	// Line is the ITL line the variant was priced on, zero with none; Tier
	// is where it came from (whatif.TierOLS and the rest, "" with no line)
	// and BorrowedFrom the donor of a borrowed one (namespace/variant).
	Line         itl.Model
	Tier         string
	BorrowedFrom string
	// PricingKey is the ITL key the line was fitted under, "" for a line
	// the variant did not fit; Readings and SteadyReadings are that key's
	// window, which bands the line (whatif.BootstrapLines).
	PricingKey               string
	Readings, SteadyReadings []itl.Observation
	// CapacityTokens is C, the replica's whole KV capacity
	// (whatif.CapacityTokens).
	CapacityTokens float64
	// BatchCap is the cap per engine rank the analyzer prices the variant
	// at, BatchCapSource where it came from (flags, declared, learned,
	// default; "" with none), and Ranks the engine ranks per replica.
	BatchCap       int64
	BatchCapSource string
	Ranks          int
	// InputTokens, OutputTokens and PrefixHitRate are the shape the
	// variant's rows reported, averaged over its rows that report one.
	InputTokens, OutputTokens, PrefixHitRate float64
}

// StateAt replays recording through the analyzers (in Normal mode, whatever
// opts.Mode says) and returns the state of the model q names after q's
// cycle. An error is a recording that does not replay, a model it does not
// hold or names ambiguously, or a cycle past its last.
func StateAt(ctx context.Context, recording []byte, opts Options, q StateQuery) (*State, error) {
	if q.Cycle < 0 {
		return nil, fmt.Errorf("learnreplay: cycle %d, want 1 or more, or 0 for the last", q.Cycle)
	}
	opts.Mode = Normal
	type seen struct {
		state   *State
		cycles  int
		firstAt time.Time
		windows []whatif.TPOTWindow
	}
	models := map[string]*seen{}
	var stateErr error
	opts.stop = func(r *replayer, ev learnrec.RawEvent) bool {
		if ev.Kind != saturation.KindAnalyze || stateErr != nil {
			return stateErr != nil
		}
		var d saturation.AnalyzeEvent
		if err := ev.DecodeData(&d); err != nil {
			stateErr = err
			return true
		}
		in, err := decodeInput(d.Input)
		if err != nil {
			stateErr = err
			return true
		}
		if q.ModelID != "" && in.ModelID != q.ModelID || q.Namespace != "" && in.Namespace != q.Namespace {
			return false
		}
		key := in.Namespace + "/" + in.ModelID
		m := models[key]
		if m == nil {
			m = &seen{firstAt: ev.Now}
			models[key] = m
		}
		if q.Cycle != 0 && m.cycles >= q.Cycle {
			return false
		}
		m.cycles++
		if w, ok := tpotWindow(in.RouterLatency); ok {
			m.windows = append(m.windows, w)
		}
		if q.Cycle == 0 || m.cycles == q.Cycle {
			m.state = r.stateOf(in, m.cycles, ev.Now)
		}
		// Stop once the one model asked for has reached its cycle.
		return q.Cycle != 0 && q.ModelID != "" && q.Namespace != "" && m.cycles == q.Cycle
	}
	if _, err := Replay(ctx, recording, opts); err != nil {
		return nil, err
	}
	if stateErr != nil {
		return nil, fmt.Errorf("learnreplay: %w", stateErr)
	}
	keys := make([]string, 0, len(models))
	for k := range models {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	switch {
	case len(keys) == 0 && q.ModelID == "":
		return nil, errors.New("learnreplay: the recording holds no saturation cycle")
	case len(keys) == 0:
		return nil, fmt.Errorf("learnreplay: the recording holds no cycle of model %q", q.ModelID)
	case len(keys) > 1:
		return nil, fmt.Errorf("learnreplay: the recording holds %s; name one with its namespace and model", strings.Join(keys, ", "))
	}
	m := models[keys[0]]
	if m.state == nil {
		return nil, fmt.Errorf("learnreplay: cycle %d asked for, and the recording holds %d of %s", q.Cycle, m.cycles, keys[0])
	}
	m.state.FirstAt = m.firstAt
	m.state.TPOTWindows = m.windows
	return m.state, nil
}

// tpotWindow is the router's per-request TPOT histogram of one cycle, as the
// what-if library reads it: bucket rates for counts, which give the same
// quantile and mean.
func tpotWindow(rl *domain.RouterLatency) (whatif.TPOTWindow, bool) {
	if rl == nil || len(rl.TPOTBuckets) == 0 {
		return whatif.TPOTWindow{}, false
	}
	w := whatif.TPOTWindow{Sum: rl.TPOTSumRate, Count: rl.TPOTCountRate,
		Buckets: make([]whatif.HistogramBucket, len(rl.TPOTBuckets))}
	for i, b := range rl.TPOTBuckets {
		w.Buckets[i] = whatif.HistogramBucket{UpperEdge: b.UpperBound, Count: b.Rate}
	}
	return w, true
}

// stateOf reads the analyzers' learned state of the model in after its
// saturation Analyze.
func (r *replayer) stateOf(in domain.AnalyzerInput, cycle int, now time.Time) *State {
	st := &State{Namespace: in.Namespace, ModelID: in.ModelID, Cycle: cycle, At: now}
	st.Policy, _ = in.Config.(*config.ScalingPolicy)
	res := r.results[resultKey(domain.SaturationAnalyzerName, in.Namespace, in.ModelID)]
	if res == nil {
		return st
	}
	reports := map[string]learn.Report{}
	for _, rep := range r.sat.LearnerReports()[in.Namespace+"|"+in.ModelID] {
		if rep.Learner == learn.ITL {
			reports[rep.Variant] = rep
		}
	}
	learning := st.Policy != nil && st.Policy.EnableBatchCapLearning
	for _, vc := range res.VariantCapacities {
		vs := VariantState{Variant: vc.VariantName, Role: vc.Role, Replicas: vc.ReplicaCount}
		if sr := vc.ServiceRate; sr != nil && sr.HasLine {
			vs.Line = itl.Model{A: sr.LineSlope, B: sr.LineIntercept}
		}
		for _, b := range vc.Borrowed {
			if b.Quantity == domain.BorrowedITLLine {
				vs.BorrowedFrom = b.DonorNamespace + "/" + b.Donor
			}
		}
		rep := reports[vc.VariantName]
		if !vs.Line.IsZero() {
			vs.Tier = saturation.ITLTier(rep.Source, vs.BorrowedFrom != "")
		}
		if rep.PricingKey != "" {
			vs.PricingKey = rep.PricingKey
			vs.Readings, vs.SteadyReadings = r.sat.ITLReadings(rep.PricingKey)
		}
		var params *capacity.EngineParams
		var live int64
		if rec := r.store.Get(in.Namespace, in.ModelID, vc.VariantName); rec != nil {
			if rec.EngineParams != nil {
				p := *rec.EngineParams
				params = &p
			}
			live = rec.TotalKvCapacityTokens
		}
		var n, il, ol, hit float64
		for _, rm := range in.ReplicaMetrics {
			if rm.VariantName != vc.VariantName {
				continue
			}
			vs.Ranks = max(vs.Ranks, rm.EngineRanks)
			if rm.AvgOutputTokens > 0 {
				n++
				il += rm.AvgInputTokens
				ol += rm.AvgOutputTokens
				hit += rm.PrefixCacheHitRate
			}
		}
		if n > 0 {
			vs.InputTokens, vs.OutputTokens, vs.PrefixHitRate = il/n, ol/n, hit/n
		}
		if vs.Ranks < 1 {
			vs.Ranks = 1
			if params != nil && params.DataParallelSize > 1 {
				vs.Ranks = params.DataParallelSize
			}
		}
		vs.CapacityTokens = whatif.CapacityTokens(params, vs.Ranks, live)
		vs.BatchCap, vs.BatchCapSource = r.sat.PricingBatchCap(in.Namespace, in.ModelID, vc.VariantName, params, learning)
		st.Variants = append(st.Variants, vs)
	}
	slices.SortFunc(st.Variants, func(a, b VariantState) int { return strings.Compare(a.Variant, b.Variant) })
	return st
}
