package learnreplay

import (
	"context"
	"fmt"
	"math"
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

// QueryError is a StateQuery the recording cannot answer: a model it does
// not hold or names ambiguously, or a cycle past its last. It is the
// caller's question that is wrong, not the replay: every other error of
// StateAt is a recording that did not replay.
type QueryError struct{ msg string }

func (e *QueryError) Error() string { return "learnreplay: " + e.msg }

func queryErrorf(format string, args ...any) error {
	return &QueryError{msg: fmt.Sprintf(format, args...)}
}

// State is what the analyzers had learned of one model when the replay
// stopped.
type State struct {
	Namespace, ModelID string
	// Cycle is the model's cycle the replay stopped after.
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
	// the variant did not fit, and Readings that key's windows.
	PricingKey string
	Readings   itl.KeyReadings
	// LineReadings is the readings the line was fitted from, found by
	// making the tracker's choice of fit again on Readings (itl.Refit, or
	// itl.RefitAboveBand for an above-band line); Refitted says the refit
	// gave the line back. A line it does not give back (one priced per
	// token or by shape, say) has none, and is not banded.
	LineReadings []itl.Observation
	Refitted     bool
	// Baseline is the intercept the tracker's pinned fit pins for the key,
	// and MinSettled the steady gate's MinSettledSamples: what a refit takes.
	Baseline   float64
	MinSettled int
	// CapacityTokens is C, the replica's whole KV capacity
	// (whatif.CapacityTokens).
	CapacityTokens float64
	// BatchCap is the cap per engine rank the analyzer prices the variant
	// at, BatchCapSource where it came from (flags, declared, learned,
	// default; "" with none), and Ranks the engine ranks per replica.
	BatchCap       int64
	BatchCapSource string
	Ranks          int
	// Shape is the traffic the variant served (shapeOf).
	Shape MeasuredShape
}

// MeasuredShape is the shape a variant served over the cycles it had
// traffic in.
type MeasuredShape struct {
	// InputTokens, OutputTokens and PrefixHitRate are the means.
	InputTokens, OutputTokens, PrefixHitRate float64
	// FromCycle and ToCycle are the first and the last cycle they were
	// taken over, and Cycles how many of the cycles between had traffic;
	// all 0 when no cycle up to the one asked had any.
	FromCycle, ToCycle, Cycles int
}

// shapeCycles is how many of a variant's latest cycles with traffic its
// shape is taken over: five minutes at the default 15 s cycle.
const shapeCycles = 20

// shapeSample is one cycle's shape of a variant, and its request rate.
type shapeSample struct {
	cycle            int
	il, ol, hit, req float64
}

// StateAt replays recording through the analyzers (in Normal mode, whatever
// opts.Mode says) and returns the state of the model q names after q's
// cycle. A *QueryError is a model the recording does not hold or names
// ambiguously, or a cycle past its last; any other error is a recording
// that does not replay.
func StateAt(ctx context.Context, recording []byte, opts Options, q StateQuery) (*State, error) {
	if q.Cycle < 0 {
		return nil, queryErrorf("cycle %d, want 1 or more, or 0 for the last", q.Cycle)
	}
	opts.Mode = Normal
	type seen struct {
		state   *State
		cycles  int
		firstAt time.Time
		windows []whatif.TPOTWindow
		shapes  map[string][]shapeSample
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
			m = &seen{firstAt: ev.Now, shapes: map[string][]shapeSample{}}
			models[key] = m
		}
		if q.Cycle != 0 && m.cycles >= q.Cycle {
			return false
		}
		m.cycles++
		if w, ok := tpotWindow(in.RouterLatency); ok {
			m.windows = append(m.windows, w)
		}
		noteShapes(m.shapes, in.ReplicaMetrics, m.cycles)
		if q.Cycle == 0 || m.cycles == q.Cycle {
			m.state = r.stateOf(in, m.cycles, ev.Now, m.shapes)
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
		return nil, queryErrorf("the recording holds no saturation cycle")
	case len(keys) == 0:
		return nil, queryErrorf("the recording holds no cycle of model %q", q.ModelID)
	case len(keys) > 1:
		return nil, queryErrorf("the recording holds %s; name one with its namespace and model", strings.Join(keys, ", "))
	}
	m := models[keys[0]]
	if m.state == nil {
		return nil, queryErrorf("cycle %d asked for, and the recording holds %d of %s", q.Cycle, m.cycles, keys[0])
	}
	m.state.FirstAt = m.firstAt
	m.state.TPOTWindows = m.windows
	return m.state, nil
}

// noteShapes keeps, per variant, the shape of each of its latest
// shapeCycles cycles with traffic: a row serving requests, or generating
// tokens, at the cycle.
func noteShapes(shapes map[string][]shapeSample, rows []domain.ReplicaMetrics, cycle int) {
	type acc struct{ n, il, ol, hit, req float64 }
	byVariant := map[string]*acc{}
	traffic := map[string]bool{}
	for _, rm := range rows {
		a := byVariant[rm.VariantName]
		if a == nil {
			a = &acc{}
			byVariant[rm.VariantName] = a
		}
		if rm.RunningRequests > 0 || rm.RequestRate > 0 || rm.GenerationTokenRate > 0 {
			traffic[rm.VariantName] = true
		}
		if rm.AvgOutputTokens > 0 {
			a.n++
			a.il += rm.AvgInputTokens
			a.ol += rm.AvgOutputTokens
			a.hit += rm.PrefixCacheHitRate
		}
		if rm.RequestRateKnown && rm.RequestRate > 0 {
			a.req += rm.RequestRate
		}
	}
	for v, a := range byVariant {
		if !traffic[v] || a.n == 0 {
			continue
		}
		s := shapes[v]
		s = append(s, shapeSample{cycle: cycle, il: a.il / a.n, ol: a.ol / a.n, hit: a.hit / a.n, req: a.req})
		if len(s) > shapeCycles {
			s = s[len(s)-shapeCycles:]
		}
		shapes[v] = s
	}
}

// shapeOf is a variant's measured shape: the mean of its latest shapeCycles
// cycles with traffic, each weighted by its request rate (equally when no
// cycle read one). The rows' own lengths are means over a window already;
// weighting by rate keeps a cycle of a light trickle from counting as much
// as one at load, and taking cycles with traffic only means a recording
// that ends idle is priced at the traffic it last served rather than at
// none.
func shapeOf(samples []shapeSample) MeasuredShape {
	if len(samples) == 0 {
		return MeasuredShape{}
	}
	var total float64
	for _, s := range samples {
		total += s.req
	}
	var out MeasuredShape
	for _, s := range samples {
		w := 1 / float64(len(samples))
		if total > 0 {
			w = s.req / total
		}
		out.InputTokens += w * s.il
		out.OutputTokens += w * s.ol
		out.PrefixHitRate += w * s.hit
	}
	out.FromCycle, out.ToCycle, out.Cycles = samples[0].cycle, samples[len(samples)-1].cycle, len(samples)
	return out
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
func (r *replayer) stateOf(in domain.AnalyzerInput, cycle int, now time.Time, shapes map[string][]shapeSample) *State {
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
	minSettled := st.Policy.ITLSteadiness().MinSettledSamples
	for _, vc := range res.VariantCapacities {
		vs := VariantState{Variant: vc.VariantName, Role: vc.Role, Replicas: vc.ReplicaCount, MinSettled: minSettled}
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
			vs.Readings = r.sat.ITLReadings(rep.PricingKey)
		}
		vs.refit()
		var params *capacity.EngineParams
		var live int64
		if rec := r.store.Get(in.Namespace, in.ModelID, vc.VariantName); rec != nil {
			if rec.EngineParams != nil {
				p := *rec.EngineParams
				params = &p
			}
			live = rec.TotalKvCapacityTokens
		}
		for _, rm := range in.ReplicaMetrics {
			if rm.VariantName == vc.VariantName {
				vs.Ranks = max(vs.Ranks, rm.EngineRanks)
			}
		}
		if vs.Ranks < 1 {
			vs.Ranks = 1
			if params != nil && params.DataParallelSize > 1 {
				vs.Ranks = params.DataParallelSize
			}
		}
		vs.Shape = shapeOf(shapes[vc.VariantName])
		vs.CapacityTokens = whatif.CapacityTokens(params, vs.Ranks, live)
		vs.BatchCap, vs.BatchCapSource = r.sat.PricingBatchCap(in.Namespace, in.ModelID, vc.VariantName, params, learning)
		st.Variants = append(st.Variants, vs)
	}
	slices.SortFunc(st.Variants, func(a, b VariantState) int { return strings.Compare(a.Variant, b.Variant) })
	return st
}

// sameLine reports two lines equal to a part in a billion: a refit of the
// readings a line was fitted from gives it back to the last bits.
func sameLine(a, b itl.Model) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) <= 1e-9*max(math.Abs(x), math.Abs(y)) }
	return near(a.A, b.A) && near(a.B, b.B)
}

// refit finds the readings the line was fitted from, by making the tracker's
// choice of fit again on the key's windows: in the band, at the line's B,
// and, when that does not give the line back, the above-band fallback, which
// marks the line TierAboveBand. A held or borrowed line was fitted from no
// readings of the variant's own here.
func (vs *VariantState) refit() {
	if vs.Tier != whatif.TierOLS && vs.Tier != whatif.TierPinnedB {
		return
	}
	vs.Baseline = vs.Line.B
	if got := itl.Refit(vs.Readings.All, vs.Readings.Steady, vs.Baseline, vs.MinSettled); got.Tier != "" && sameLine(got.Model, vs.Line) {
		vs.Tier, vs.LineReadings, vs.Refitted = got.Tier, got.Read, true
		return
	}
	if got := itl.RefitAboveBand(vs.Readings.Above, vs.Readings.AboveSteady, vs.MinSettled); got.Tier != "" && sameLine(got.Model, vs.Line) {
		vs.Tier, vs.LineReadings, vs.Refitted = whatif.TierAboveBand, got.Read, true
	}
}
