// Package slo is the SLO analyzer: it prices each variant's capacity at the
// fill where the variant meets the latency targets its scaling policy declares,
// and judges whether the model meets them as the router measured it.
//
// Phase 0 of docs/proposals/slo-tiers.md. On this release the engine runs the
// analyzer for every model whose policy lists it, publishes its (D, P) as
// wva_analyzer_demand / wva_analyzer_target{analyzer_name="slo"} and its own
// wva_slo_* series, and sizes from the saturation analyzer alone: nothing this
// package returns changes a replica count.
package slo

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// backlogDrainSeconds is how quickly the analyzer asks the fleet to clear the
// requests waiting in the router's queue: each costs O output tokens, spread
// over this many seconds of demand. A queue under a TPOT SLO is already a
// latency miss for someone, so the drain is short.
const backlogDrainSeconds = 60.0

// windowTimeout is how long a variant's ITL window outlives the last cycle
// that saw the variant.
const windowTimeout = 24 * time.Hour

// RouterLatencySource reads a model's request latency as the router measured
// it. The collector implements it.
type RouterLatencySource interface {
	CollectRouterLatency(ctx context.Context, modelID, namespace string) *domain.RouterLatency
}

// Analyzer is the SLO analyzer. Safe for concurrent use.
type Analyzer struct {
	router RouterLatencySource
	store  *capacity.Store
	now    func() time.Time

	mu sync.Mutex
	// windows holds each generating variant's (k, ITL) readings. The analyzer
	// fits its own lines rather than reading the saturation analyzer's, because
	// saturation fits only the decode role: an aggregated replica (role "both"),
	// the common deployment, never gets a line there.
	windows map[string]*itl.Window
	// baseline is the intercept B of each window's last full fit: what a
	// pinned fit holds B to once the fleet stops spanning k.
	baseline map[string]float64
	seen     map[string]time.Time
	// warned records a misconfiguration already logged for a model, so a
	// half-configured policy says so once and not every cycle.
	warned map[string]string
}

// New returns an SLO analyzer. router may be nil, in which case the SLO is
// priced but never judged; store may be nil, in which case no engine batch cap
// applies and capacity comes from live readings only.
func New(router RouterLatencySource, store *capacity.Store) *Analyzer {
	return &Analyzer{
		router:   router,
		store:    store,
		now:      time.Now,
		windows:  make(map[string]*itl.Window),
		baseline: make(map[string]float64),
		seen:     make(map[string]time.Time),
		warned:   make(map[string]string),
	}
}

// Name implements domain.Analyzer.
func (a *Analyzer) Name() string { return domain.SLOAnalyzerName }

// Analyze implements domain.Analyzer.
func (a *Analyzer) Analyze(ctx context.Context, input domain.AnalyzerInput) (*domain.AnalyzerResult, error) {
	logger := ctrl.LoggerFrom(ctx)
	now := a.now()
	cfg, _ := input.Config.(*config.ScalingPolicy)
	if cfg == nil {
		return nil, fmt.Errorf("slo analyzer: config is %T, want *config.ScalingPolicy", input.Config)
	}
	a.warnOnce(logger, input, cfg.SLOMisconfiguration())

	result := &domain.AnalyzerResult{
		AnalyzerName: domain.SLOAnalyzerName,
		ModelID:      input.ModelID,
		Namespace:    input.Namespace,
		AnalyzedAt:   now,
	}
	snap := metrics.SLOModelSnapshot{Namespace: input.Namespace, ModelName: input.ModelID}

	fleet := readFleet(input)
	demand := fleet.demand(input)
	result.TotalDemand = demand
	if fleet.disaggregated {
		result.RoleDemand = map[string]float64{domain.RoleDecode: demand}
	}

	var tpotTarget float64
	if cfg.SLO != nil && cfg.SLO.TPOT != nil {
		tpotTarget = cfg.SLO.TPOT.Target.Seconds()
	}
	for _, vs := range fleet.variants {
		vc, reading := a.priceVariant(input, vs, fleet, tpotTarget, cfg.ScaleUpThreshold, now)
		if fleet.generatingReplicas > 0 && isGenerating(vs.Role) {
			vc.TotalDemand = demand * float64(fleet.ready[vs.VariantName]) / float64(fleet.generatingReplicas)
		}
		if vc.PerReplicaCapacity > 0 && vc.ReplicaCount > 0 {
			vc.Utilization = vc.TotalDemand / (float64(vc.ReplicaCount) * vc.PerReplicaCapacity)
		}
		result.VariantCapacities = append(result.VariantCapacities, vc)
		if reading != nil {
			snap.Variants = append(snap.Variants, *reading)
			logger.V(logging.DEFAULT).Info("slo-variant",
				"modelID", input.ModelID, "namespace", input.Namespace,
				"variant", vs.VariantName, "role", vs.Role, "source", reading.Source,
				"fill", reading.Fill, "predictedITL", reading.PredictedITL,
				"perReplicaTokensPerSec", vc.PerReplicaCapacity, "reason", vc.Reason)
		}
	}
	a.sweep(now)

	snap.Objectives = a.judge(ctx, input, cfg.SLO)
	for _, o := range snap.Objectives {
		logger.V(logging.DEFAULT).Info("slo-model",
			"modelID", input.ModelID, "namespace", input.Namespace,
			"objective", o.Objective, "statistic", o.Statistic,
			"target", o.TargetSeconds, "observed", o.Observed, "attainment", o.Attainment,
			"demandTokensPerSec", demand, "outputTokens", fleet.shape.AvgOutputTokens)
	}
	metrics.PublishSLO(snap, now)
	return result, nil
}

// priceVariant fits the variant's line and prices one replica of it at the
// TPOT target. The reading is nil for a variant the TPOT objective does not
// describe: a prefill role, or a policy with no TPOT target.
func (a *Analyzer) priceVariant(input domain.AnalyzerInput, vs domain.VariantReplicaState,
	fleet fleetReading, tpotTarget, scaleUp float64, now time.Time) (domain.VariantCapacity, *metrics.SLOVariantReading) {
	vc := domain.VariantCapacity{
		VariantName:      vs.VariantName,
		Role:             vs.Role,
		ReplicaCount:     vs.CurrentReplicas,
		PendingReplicas:  vs.PendingReplicas,
		StuckReplicas:    vs.StuckReplicas,
		PendingAges:      vs.PendingAges,
		ObservedReplicas: fleet.ready[vs.VariantName],
		Reason:           domain.ReasonNoData,
	}
	// Prefill is not modelled in phase 0: its TPOT is meaningless (the P/D
	// sidecar asks it for one token), and TTFT is not priced yet. Left as
	// no-data, so a combiner treats it as undefined rather than as zero.
	if !isGenerating(vs.Role) {
		return vc, nil
	}
	key := windowKey(input.Namespace, input.ModelID, vs)
	line, source := a.fitLine(key, input.ReplicaMetrics, vs.VariantName, now)
	if !(tpotTarget > 0) {
		return vc, nil
	}
	reading := &metrics.SLOVariantReading{
		Variant: vs.VariantName, Source: source, Fill: math.NaN(), PredictedITL: math.NaN(),
	}
	if source == constants.SLOSourceNoLine {
		return vc, reading
	}
	p := priceTPOT(line, tpotTarget)
	switch {
	case !p.ok:
		reading.Source = constants.SLOSourceInputInvalid
		vc.Reason = domain.ReasonError
		return vc, reading
	case !p.feasible:
		// Abstain, never cap: holding an infeasible variant at its count would
		// freeze a single-variant fleet as load rises.
		reading.Source = constants.SLOSourceInfeasible
		return vc, reading
	}
	params := a.engineParams(input.Namespace, input.ModelID, vs.VariantName)
	var maxSeqs int64
	if params != nil {
		maxSeqs = params.MaxNumSeqs
	}
	capTokens := fleet.capacityTokens(vs.VariantName, params)
	rate := replicaTokenRate(line, p.fill, capTokens, fleet.shape.KVreq, maxSeqs)
	if !(rate > 0) {
		reading.Source = constants.SLOSourceInputInvalid
		vc.Reason = domain.ReasonError
		return vc, reading
	}
	vc.PerReplicaCapacity = rate
	vc.Reason = "slo-" + source
	reading.Fill = p.fill
	if k := realisedFill(line, p.fill, scaleUp, capTokens, fleet.shape.KVreq, maxSeqs); finite(k) {
		reading.PredictedITL = line.ITLAt(k)
	}
	return vc, reading
}

// fitLine adds this cycle's (k, ITL) readings for one variant to its window and
// fits ITL(k) = A*k + B over it, with the same admission rules and the same two
// tiers as the saturation analyzer's decode fit: a full least-squares fit when
// the window spans enough of k, otherwise B pinned to the last full fit's
// intercept (or the default floor) and only A fitted.
func (a *Analyzer) fitLine(key string, rows []domain.ReplicaMetrics, variant string, now time.Time) (itl.Model, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen[key] = now
	w, ok := a.windows[key]
	if !ok {
		w = itl.NewWindow(itl.DefaultWindowMaxSize, itl.DefaultObservationMaxAge, itl.DefaultMinSamples,
			itl.DefaultMinKSpread, itl.DefaultMinObservableK, itl.DefaultMaxObservableK)
		a.windows[key] = w
	}
	for _, rm := range rows {
		// A Pod not yet Ready, or one lent by the warm pool, is not a reading of
		// what one of this variant's replicas does.
		if rm.VariantName != variant || !rm.Ready || rm.FromWarmPool {
			continue
		}
		if !(rm.AvgITL > 0) || !finite(rm.AvgITL) || !(rm.KvUsageInstant > 0) || rm.KvUsageInstant > itl.DefaultMaxObservableK {
			continue
		}
		w.Add(rm.KvUsageInstant, rm.AvgITL, now)
	}
	w.Prune(now)
	obs := w.Observations()
	if w.Ready() {
		if m, ok := itl.Fit(obs); ok {
			if m.B > 0 {
				a.baseline[key] = m.B
			}
			return m, constants.SLOSourceOwn
		}
	}
	if len(obs) < itl.DefaultMinSamples {
		return itl.Model{}, constants.SLOSourceNoLine
	}
	b, learned := a.baseline[key]
	if !learned || !(b > 0) {
		b = itl.DefaultBaselineSec
	}
	if m, ok := itl.FitPinnedB(obs, b); ok {
		return m, constants.SLOSourcePinned
	}
	return itl.Model{}, constants.SLOSourceNoLine
}

// judge reads the router's latency for the model and reports each objective:
// the TPOT mean, and the TTFT percentile with its attainment. A figure from too
// few requests is NaN, which is not published.
func (a *Analyzer) judge(ctx context.Context, input domain.AnalyzerInput, slo *config.SLOPolicy) []metrics.SLOObjectiveReading {
	if slo.Empty() {
		return nil
	}
	var rl *domain.RouterLatency
	if a.router != nil {
		rl = a.router.CollectRouterLatency(ctx, input.ModelID, input.Namespace)
	}
	var out []metrics.SLOObjectiveReading
	if slo.TPOT != nil {
		r := metrics.SLOObjectiveReading{
			Objective: constants.SLOObjectiveTPOT, Statistic: "mean",
			TargetSeconds: slo.TPOT.Target.Seconds(), Observed: math.NaN(), Attainment: math.NaN(),
		}
		if rl != nil && rl.TPOTCountRate*rl.WindowSeconds >= minSamples(0) {
			r.Observed = rl.TPOTSumRate / rl.TPOTCountRate
		}
		out = append(out, r)
	}
	if slo.TTFT != nil {
		p := slo.TTFTPercentile()
		r := metrics.SLOObjectiveReading{
			Objective: constants.SLOObjectiveTTFT, Statistic: "p" + strconv.Itoa(p),
			TargetSeconds: slo.TTFT.Target.Seconds(), Observed: math.NaN(), Attainment: math.NaN(),
		}
		if rl != nil {
			if h, ok := newHistogram(rl.TTFTBuckets); ok && h.total*rl.WindowSeconds >= minSamples(p) {
				r.Observed = h.quantile(float64(p) / 100)
				r.Attainment, _ = h.fractionWithin(r.TargetSeconds)
			}
		}
		out = append(out, r)
	}
	return out
}

func (a *Analyzer) engineParams(namespace, modelID, variant string) *capacity.EngineParams {
	if a.store == nil {
		return nil
	}
	if rec := a.store.Get(namespace, modelID, variant); rec != nil {
		return rec.EngineParams
	}
	return nil
}

// sweep drops the windows of variants not seen for windowTimeout.
func (a *Analyzer) sweep(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, at := range a.seen {
		if now.Sub(at) > windowTimeout {
			delete(a.seen, key)
			delete(a.windows, key)
			delete(a.baseline, key)
		}
	}
}

func (a *Analyzer) warnOnce(logger logr.Logger, input domain.AnalyzerInput, problem string) {
	key := input.Namespace + "/" + input.ModelID
	a.mu.Lock()
	already := a.warned[key] == problem
	if problem == "" {
		delete(a.warned, key)
	} else {
		a.warned[key] = problem
	}
	a.mu.Unlock()
	if problem != "" && !already {
		logger.Info("SLO policy is half-configured", "modelID", input.ModelID,
			"namespace", input.Namespace, "problem", problem)
	}
}

// windowKey names one variant's line. The accelerator and GPU count are part
// of it, because ITL(k) is a property of the hardware a replica runs on.
func windowKey(namespace, modelID string, vs domain.VariantReplicaState) string {
	return namespace + "|" + modelID + "|" + vs.VariantName + "|" + vs.AcceleratorName + "|" +
		strconv.Itoa(vs.GPUsPerReplica)
}

func isGenerating(role string) bool {
	return role != domain.RolePrefill
}

// fleetReading is what the analyzer reads off one model's replicas once per
// cycle.
type fleetReading struct {
	variants      []domain.VariantReplicaState
	disaggregated bool
	// ready counts each variant's Ready, non-pool replica rows.
	ready map[string]int
	// generatingReplicas is the total of ready over decode and aggregated
	// variants.
	generatingReplicas int
	// shape is the request shape the generating replicas are serving.
	shape shape.Shape
	// requestRate is the generating replicas' summed completion rate, the
	// fallback when the router reports no arrival rate.
	requestRate float64
	rows        []domain.ReplicaMetrics
}

func readFleet(input domain.AnalyzerInput) fleetReading {
	f := fleetReading{ready: make(map[string]int), rows: input.ReplicaMetrics}
	f.variants = append(f.variants, input.VariantStates...)
	sort.Slice(f.variants, func(i, j int) bool { return f.variants[i].VariantName < f.variants[j].VariantName })
	roles := make(map[string]string, len(f.variants))
	for _, vs := range f.variants {
		roles[vs.VariantName] = vs.Role
		if vs.Role == domain.RolePrefill || vs.Role == domain.RoleDecode {
			f.disaggregated = true
		}
	}
	// Request-rate weighted, so a replica serving most of the traffic decides
	// the shape; equal weights when no replica reports a rate.
	var in, out, hit, weight float64
	for _, rm := range input.ReplicaMetrics {
		if !rm.Ready || rm.FromWarmPool || !isGenerating(roles[rm.VariantName]) {
			continue
		}
		f.ready[rm.VariantName]++
		f.generatingReplicas++
		if finite(rm.RequestRate) && rm.RequestRate > 0 {
			f.requestRate += rm.RequestRate
		}
	}
	for _, rm := range input.ReplicaMetrics {
		if !rm.Ready || rm.FromWarmPool || !isGenerating(roles[rm.VariantName]) {
			continue
		}
		w := 1.0
		if f.requestRate > 0 {
			w = rm.RequestRate
		}
		if !(w > 0) || !finite(rm.AvgOutputTokens) || !(rm.AvgOutputTokens > 0) {
			continue
		}
		in += w * rm.AvgInputTokens
		out += w * rm.AvgOutputTokens
		hit += w * rm.PrefixCacheHitRate
		weight += w
	}
	if weight > 0 {
		f.shape = shape.New(in/weight, out/weight, hit/weight)
	}
	return f
}

// demand is the decode work arriving, in output tokens per second: the arrival
// rate times the output length, plus the router's backlog drained over
// backlogDrainSeconds. Zero when the output length is unknown.
func (f fleetReading) demand(input domain.AnalyzerInput) float64 {
	o := f.shape.AvgOutputTokens
	if !(o > 0) || !finite(o) {
		return 0
	}
	lambda := input.ArrivalRate
	if !(lambda > 0) || !finite(lambda) {
		lambda = f.requestRate
	}
	d := lambda * o
	if q := input.SchedulerQueue; q != nil && q.QueueSize > 0 {
		d += float64(q.QueueSize) * o / backlogDrainSeconds
	}
	return d
}

// capacityTokens is the mean KV capacity of the variant's ready replicas, in
// tokens: the live reading where there is one, the engine's configured
// override otherwise, the same order the saturation analyzer reads them in.
func (f fleetReading) capacityTokens(variant string, params *capacity.EngineParams) float64 {
	var sum float64
	var n int
	for _, rm := range f.rows {
		if rm.VariantName != variant || !rm.Ready || rm.FromWarmPool {
			continue
		}
		c := 0.0
		// math.MaxInt64 is the collector's overflow sentinel, not a capacity.
		if rm.TotalKvCapacityTokens > 0 && rm.TotalKvCapacityTokens < math.MaxInt64 {
			c = float64(rm.TotalKvCapacityTokens)
		} else if params != nil && params.TotalKvTokensOverride > 0 {
			c = float64(params.TotalKvTokensOverride)
		}
		if c > 0 {
			sum += c
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
