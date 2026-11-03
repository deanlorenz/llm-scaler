package saturation

import (
	"math"
	"slices"
	"time"

	"github.com/go-logr/logr"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// The ITL line of an aggregated variant, by request shape (issue #275,
// docs/proposals/aggregated-itl-shape.md).
//
// An aggregated pod runs the prompts it admits as chunks inside its decode
// steps, so a step takes about B + A*k + c*x, with x the prefill tokens a
// step computes, about n*IL_eff/OL for n resident sequences. At one shape n
// is k*C/KVreq, so x = g*k with
//
//	g = C * IL_eff / (KVreq * OL)
//
// and the line the tracker fits, B + A_g*k, has the slope A_g = A + c*g.
// The fit is left as it is; what this file adds:
//
//   - every line carries the g it was fitted at (lineShape.g), and the
//     variant's window is dropped when g moves past aggShapeTolerance, so a
//     fit is never made from two shapes' readings;
//   - c is read off the slopes of two or more shapes (lineShape.points,
//     fitShapeTerm), never from one;
//   - a line prices a shape at or below its own g as it is, since c >= 0
//     makes it conservative there, and a higher g only through c
//     (resolveAggregatedPrice);
//   - the derived mu is corrected for the steps a request spends in prefill,
//     which hold KV and emit no token (sigma, from Little's law);
//   - a variant's figure sizes once the GPS check has agreed with it for
//     aggLiftCycles cycles (noteAggregatedLift), and never above a measured
//     rate of the variant's own (derivedMu.capAtMeasured).

// SwitchITLAggregatedShapeTerm is the replay switch for the shape term of an
// aggregated variant's line (issue #275): the shape tags, the window dropped
// on a shape change, the shape points and c, and the corrected figure. Off,
// the figure is #432's. The figure stays reported only: sizing from it is
// SwitchITLAggregatedSizes.
const SwitchITLAggregatedShapeTerm = "itl-aggregated-shape-term"

// SwitchITLAggregatedSizes is the replay switch for the lift (issue #275):
// an aggregated variant's figure prices its replicas once the GPS check has
// agreed with it. It needs the shape term's tags, so with
// SwitchITLAggregatedShapeTerm off it lifts nothing.
const SwitchITLAggregatedSizes = "itl-aggregated-sizes"

// The thresholds of the shape term. None is measured: they are first
// choices, named so a measurement can move them. None is a policy key.
const (
	// aggShapeTolerance is how far g may move, as a fraction, and still be
	// the shape a line was fitted at: the shape tracker's tolerance on IL
	// and OL (shape.DefaultChangeTolerance). Unmeasured.
	aggShapeTolerance = shape.DefaultChangeTolerance
	// aggShapeMinSpread is how far apart, as a ratio, the largest and the
	// smallest g of a line's points must be before c is fitted from them.
	// Unmeasured.
	aggShapeMinSpread = 1.5
	// aggLiftCycles is how many consecutive comparable cycles the GPS check
	// must agree before an aggregated figure sizes, and disagree before it
	// stops. Unmeasured.
	aggLiftCycles = 3
	// aggMaxShapePoints is how many shape points a line keeps. Unmeasured.
	aggMaxShapePoints = 6
	// aggSigmaMinReadings is how many prefill readings sigma needs, and
	// aggSigmaMaxReadings how many it keeps. Unmeasured.
	aggSigmaMinReadings = 3
	aggSigmaMaxReadings = 20
)

// The pricing cases of an aggregated line (aggCycle.priceCase), logged as
// the derived figure's why when there is none.
const (
	// aggCaseCovered: the shape now is at or below the line's own g, within
	// aggShapeTolerance above it. The line prices it as it is.
	aggCaseCovered = "shape-covered"
	// aggCaseShapeTerm: the shape now is above the line's g and c is
	// identified. The line prices it as A_g + c*(g* - g).
	aggCaseShapeTerm = "shape-term"
	// aggCaseUnpriced: the shape now is above the line's g and c is not
	// identified. No figure.
	aggCaseUnpriced = "shape-unpriced"
	// aggCaseLineShapeUnknown: the line carries no g (fitted by a build
	// before the shape term, or restored from one). No figure.
	aggCaseLineShapeUnknown = "line-shape-unknown"
	// aggCaseShapeUnknown: no g could be computed for the shape now (no
	// capacity, prompt or output length). No figure.
	aggCaseShapeUnknown = "shape-unknown"
)

// shapePoint is one shape's slope: the g a line was fitted at and its A_g.
type shapePoint struct {
	g, a float64
	at   time.Time
}

// lineShape is what the shape term keeps with a shared line (learnedLine):
// the g and sigma of the fit it holds, and the points c is fitted from.
type lineShape struct {
	// g is the shape the line was fitted at; 0 is unknown.
	g float64
	// sigma is the prefill steps per prompt token measured beside the fit;
	// meaningful only with sigmaOK.
	sigma   float64
	sigmaOK bool
	points  []shapePoint
	// a0 and c are the decode slope and the prefill cost per token fitted
	// from points (fitShapeTerm); meaningful only with cOK.
	a0, c float64
	cOK   bool
}

// clone is s with its own copy of the points.
func (s lineShape) clone() lineShape {
	s.points = slices.Clone(s.points)
	return s
}

// withinShape reports whether g is within aggShapeTolerance of ref.
func withinShape(g, ref float64) bool {
	return math.Abs(g-ref) <= aggShapeTolerance*ref
}

// addPoint adds p, replacing any point within aggShapeTolerance of its g,
// keeps the newest aggMaxShapePoints, and refits c.
func (s *lineShape) addPoint(p shapePoint) {
	kept := make([]shapePoint, 0, len(s.points)+1)
	for _, q := range s.points {
		if !withinShape(p.g, q.g) {
			kept = append(kept, q)
		}
	}
	kept = append(kept, p)
	if len(kept) > aggMaxShapePoints {
		kept = kept[len(kept)-aggMaxShapePoints:]
	}
	s.points = kept
	s.a0, s.c, s.cOK = fitShapeTerm(s.points)
}

// fitShapeTerm fits A_g = a0 + c*g over the points by least squares. ok is
// false with fewer than two points, a largest g under aggShapeMinSpread times
// the smallest, or a fit that is not this physics: a prefill token cannot
// make a step faster (c < 0), and a decode slope is positive (a0 > 0).
func fitShapeTerm(points []shapePoint) (a0, c float64, ok bool) {
	if len(points) < 2 {
		return 0, 0, false
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	var sg, sa float64
	for _, p := range points {
		lo, hi = min(lo, p.g), max(hi, p.g)
		sg += p.g
		sa += p.a
	}
	if !(lo > 0) || hi < aggShapeMinSpread*lo {
		return 0, 0, false
	}
	n := float64(len(points))
	mg, ma := sg/n, sa/n
	var sxx, sxy float64
	for _, p := range points {
		sxx += (p.g - mg) * (p.g - mg)
		sxy += (p.g - mg) * (p.a - ma)
	}
	if !(sxx > 0) {
		return 0, 0, false
	}
	c = sxy / sxx
	a0 = ma - c*mg
	if math.IsNaN(c) || math.IsInf(c, 0) || math.IsNaN(a0) || math.IsInf(a0, 0) || c < 0 || !(a0 > 0) {
		return 0, 0, false
	}
	return a0, c, true
}

// aggState is one aggregated variant's shape state, per ITL window key
// (itlAggregated). Guarded by a.mu.
type aggState struct {
	// windowG is the shape the window's readings were taken at; 0 before
	// the first.
	windowG float64
	// lineG, lineSigma and lineSigmaOK are the shape of the variant's own
	// last fit, which a held slope keeps.
	lineG       float64
	lineSigma   float64
	lineSigmaOK bool
	// sigmas is the prefill readings taken at windowG, newest last.
	sigmas []float64
	// refused counts the prefill readings refused since the variant was
	// first seen: residence under the output's own steps.
	refused int64
	// agree and disagree count consecutive comparable cycles; lifted is the
	// figure sizing.
	agree, disagree int
	lifted          bool
	// noOccupancyLogged is set once the variant has been logged as having no
	// prefill reading at all.
	noOccupancyLogged bool
}

// sigma is the median of the prefill readings, false with fewer than
// aggSigmaMinReadings.
func (s *aggState) sigma() (float64, bool) {
	if len(s.sigmas) < aggSigmaMinReadings {
		return 0, false
	}
	return medianRate(s.sigmas)
}

// aggCycle is one aggregated variant's shape and price for one cycle,
// resolved in fitLines and read by priceReplicas.
type aggCycle struct {
	key string
	// g is the shape now, 0 when it could not be computed.
	g float64
	// lineG is the g of the line priced: the variant's own, or the lender's.
	lineG float64
	// sigma is the prefill steps per prompt token priced: the window's,
	// else the line's.
	sigma   float64
	sigmaOK bool
	// refused is how many prefill readings the variant has had refused
	// (aggState.refused).
	refused int64
	c       float64
	cOK     bool
	// priceCase is one of the aggCase constants; figure is whether it
	// gives a figure, and a the slope it prices.
	priceCase string
	figure    bool
	a         float64
	// lifted is the variant's lift as the cycle began.
	lifted bool
	// errs is the GPS check's error, per comparable replica, in percent.
	errs []float64
}

// aggShapeOn reports whether a variant of this role prices its line by shape.
func (a *SaturationAnalyzer) aggShapeOn(role string) bool {
	return a.itlFitsAggregated && a.itlAggShapeTerm && domain.CanonicalRole(role) == domain.RoleBoth
}

// shapeG is g = C*IL_eff/(KVreq*OL): the prefill tokens a step computes per
// unit of KV fill. 0 when any term is missing.
func shapeG(capacityTokens, ilEff, kvReq, ol float64) float64 {
	if !(capacityTokens > 0) || !(ilEff > 0) || !(kvReq > 0) || !(ol > 0) {
		return 0
	}
	g := capacityTokens * ilEff / (kvReq * ol)
	if math.IsInf(g, 0) || math.IsNaN(g) {
		return 0
	}
	return g
}

// aggregatedShapeNow is the shape an aggregated variant is priced at this
// cycle: g, from the shape mu is priced at (pricingShapes) at the variant's
// median hit rate and its median KV capacity.
func (c *cycle) aggregatedShapeNow(variant string, params *capacity.EngineParams) float64 {
	var hits, caps []float64
	for _, rm := range c.input.ReplicaMetrics {
		if rm.VariantName != variant || rm.FromWarmPool {
			continue
		}
		hits = append(hits, rm.PrefixCacheHitRate)
		if ct := capacityTokensFor(params, rm.Ranks(), rm.TotalKvCapacityTokens); ct > 0 {
			caps = append(caps, ct)
		}
	}
	hit, _ := medianRate(hits)
	capTokens, ok := medianRate(caps)
	if !ok {
		return 0
	}
	_, muShape, _, _ := c.pricingShapes(hit)
	return shapeG(capTokens, muShape.ILeff, muShape.KVreq, c.muDivisor)
}

// aggStateLocked is key's shape state, made on first use. Callers hold a.mu.
func (a *SaturationAnalyzer) aggStateLocked(key string) *aggState {
	st, ok := a.itlAggregated[key]
	if !ok {
		st = &aggState{}
		a.itlAggregated[key] = st
	}
	return st
}

// noteAggregatedShape files the shape now, g, against the window's: the
// first g a window sees becomes its shape, and a g past aggShapeTolerance of
// it drops the window's readings (itl.Tracker.ClearWindow) and its prefill
// readings, and ends a lift, so the next fit is made at the new shape only.
// The intercept and the held slope are kept: B does not depend on shape, and
// the held slope keeps the g it was fitted at.
func (a *SaturationAnalyzer) noteAggregatedShape(key, variant string, g float64, logger logr.Logger) {
	if !(g > 0) {
		return
	}
	a.mu.Lock()
	st := a.aggStateLocked(key)
	from := st.windowG
	if from > 0 && withinShape(g, from) {
		a.mu.Unlock()
		return
	}
	st.windowG = g
	st.sigmas = nil
	wasLifted := st.lifted
	st.agree, st.disagree, st.lifted = 0, 0, false
	a.mu.Unlock()
	if !(from > 0) {
		return
	}
	dropped := a.itlTracker.ClearWindow(key)
	logger.V(logging.DEFAULT).Info("itl-aggregated-shape-moved",
		"variant", variant, "key", key, "fromG", from, "toG", g, "tolerance", aggShapeTolerance,
		"windowDropped", dropped, "wasLifted", wasLifted,
		"reason", "the request shape moved: the window's readings were taken at another shape, so the next fit is made from new readings; the held slope keeps the shape it was fitted at")
}

// noteOccupancy takes one prefill reading per replica of variant: by
// Little's law a replica holding n sequences and completing lambda a second
// keeps each n/lambda seconds, n/(lambda*ITL) steps, of which OL are decode
// steps; the rest, per prompt token, is sigma. A residence under OL steps is
// two near-equal figures' noise and is refused (counted), never read as a
// negative prefill. Replicas inside the ITL band only, ready and the
// variant's own; none at all while the shape changes.
func (a *SaturationAnalyzer) noteOccupancy(key, variant string, rows []domain.ReplicaMetrics,
	minK, maxK, fleetIL, fleetOL, fleetHit float64, logger logr.Logger) {
	var taken []float64
	refused, readable, own := 0, 0, 0
	for _, rm := range rows {
		if rm.VariantName != variant || !rm.Ready || rm.FromWarmPool {
			continue
		}
		own++
		if rm.RunningRequestsKnown && rm.RequestRateKnown {
			readable++
		}
		k := rm.KvUsageForITL()
		if k < minK || k > maxK || !(rm.AvgITL > 0) || math.IsInf(rm.AvgITL, 0) ||
			!rm.RunningRequestsKnown || !rm.RequestRateKnown || !(rm.RequestRate > 0) || rm.RunningRequests <= 0 {
			continue
		}
		ol, il, hit := rm.AvgOutputTokens, rm.AvgInputTokens, rm.PrefixCacheHitRate
		if !(ol > 0) {
			ol = fleetOL
		}
		if !(il > 0) {
			il, hit = fleetIL, fleetHit
		}
		ilEff := shape.New(il, ol, hit).ILeff
		if !(ol > 0) || !(ilEff > 0) {
			continue
		}
		steps := float64(rm.RunningRequests) / (rm.RequestRate * rm.AvgITL)
		if math.IsNaN(steps) || math.IsInf(steps, 0) {
			continue
		}
		if steps < ol {
			refused++
			continue
		}
		taken = append(taken, max((steps-ol)/ilEff, 0))
	}
	a.mu.Lock()
	st := a.aggStateLocked(key)
	st.refused += int64(refused)
	st.sigmas = append(st.sigmas, taken...)
	if len(st.sigmas) > aggSigmaMaxReadings {
		st.sigmas = slices.Clone(st.sigmas[len(st.sigmas)-aggSigmaMaxReadings:])
	}
	logNone := readable == 0 && own > 0 && !st.noOccupancyLogged && len(st.sigmas) == 0
	if logNone {
		st.noOccupancyLogged = true
	}
	a.mu.Unlock()
	if logNone {
		logger.V(logging.DEFAULT).Info("itl-aggregated-no-occupancy",
			"variant", variant, "key", key,
			"reason", "no replica reports both the running requests and the completion rate (vllm:num_requests_running and vllm:request_generation_tokens_count; SGLang publishes no completion rate per pod), so the steps a request spends in prefill cannot be read: the variant's derived mu stays reported only")
	}
}

// noteAggregatedFit files the shape of the variant's own line after this
// cycle's fit: a fit made now (not held) is at the window's shape and takes
// the window's sigma; a held slope with no shape -- one restored across a
// restart -- takes the shape of the line it published, when that line is
// still its fit. It returns the line's shape, and whether the fit is a
// shape point (pointOK).
func (a *SaturationAnalyzer) noteAggregatedFit(key, physicsKey string, r itl.Reading, shapeChanging bool) (g, sigma float64, sigmaOK, pointOK bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.aggStateLocked(key)
	if !r.Model.IsZero() && !r.SlopeHeld {
		st.lineG = st.windowG
		st.lineSigma, st.lineSigmaOK = st.sigma()
		pointOK = st.lineG > 0 && st.lineSigmaOK && !shapeChanging &&
			(r.Tier == itl.TierOLS || (r.Tier == itl.TierPinnedB && r.BaselineLearned))
	} else if r.SlopeHeld && !(st.lineG > 0) {
		if l, ok := a.itlLines[physicsKey]; ok && l.fromVariant == key && l.model.A == r.Model.A && l.shape.g > 0 {
			st.lineG, st.lineSigma, st.lineSigmaOK = l.shape.g, l.shape.sigma, l.shape.sigmaOK
		}
	}
	return st.lineG, st.lineSigma, st.lineSigmaOK, pointOK
}

// shapePublishedLine stamps the line just published under physicsKey with
// its shape, and adds its slope as a shape point when the fit is one.
func (a *SaturationAnalyzer) shapePublishedLine(physicsKey string, g, sigma float64, sigmaOK, point bool,
	slope float64, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, ok := a.itlLines[physicsKey]
	if !ok {
		return
	}
	l.shape = l.shape.clone()
	l.shape.g, l.shape.sigma, l.shape.sigmaOK = g, sigma, sigmaOK
	if point {
		l.shape.addPoint(shapePoint{g: g, a: slope, at: now})
	}
	a.itlLines[physicsKey] = l
}

// lineShapeOf is the shape kept with the shared line under physicsKey.
func (a *SaturationAnalyzer) lineShapeOf(physicsKey string) lineShape {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.itlLines[physicsKey].shape.clone()
}

// resolveAggregated is one aggregated variant's price for this cycle: the
// line it is priced on, at the shape the line was fitted at (lineG), the
// shape now (g), the shape term kept with the line under physicsKey, and the
// prefill steps per prompt token -- the window's, measured at the shape now,
// else the line's.
func (a *SaturationAnalyzer) resolveAggregated(key, physicsKey string, line itl.Model, lineG, lineSigma float64,
	lineSigmaOK bool, g float64) *aggCycle {
	term := a.lineShapeOf(physicsKey)
	a.mu.Lock()
	st := a.aggStateLocked(key)
	sigma, ok := st.sigma()
	lifted, refused := st.lifted, st.refused
	a.mu.Unlock()
	if !ok {
		sigma, ok = lineSigma, lineSigmaOK
	}
	ac := &aggCycle{key: key, g: g, lineG: lineG, sigma: sigma, sigmaOK: ok, refused: refused,
		c: term.c, cOK: term.cOK, lifted: lifted}
	ac.a, ac.priceCase, ac.figure = resolveAggregatedPrice(line.A, lineG, g, term.c, term.cOK)
	return ac
}

// resolveAggregatedPrice is the slope an aggregated line prices the shape
// now at, and which case gave it.
//
// Never extrapolated toward a better price: c >= 0, so a line fitted at a
// higher g states the ITL of a lower one too high, and it prices a shape at
// or below its own g (within aggShapeTolerance above it) as it is. Only a
// higher g is moved, through c, which raises the ITL and lowers mu; an
// over-estimated c then over-provisions and never under-provisions. With no
// c, a higher g has no figure.
func resolveAggregatedPrice(lineA, lineG, g, c float64, cOK bool) (priceA float64, priceCase string, figure bool) {
	switch {
	case !(g > 0):
		return 0, aggCaseShapeUnknown, false
	case !(lineG > 0):
		return 0, aggCaseLineShapeUnknown, false
	case g <= lineG*(1+aggShapeTolerance):
		return lineA, aggCaseCovered, true
	case cOK:
		return lineA + c*(g-lineG), aggCaseShapeTerm, true
	default:
		return 0, aggCaseUnpriced, false
	}
}

// occupancyFactor is OL/(OL + sigma*IL_eff): the share of a resident
// sequence's steps that emit a token. A sequence being prefilled holds KV
// and emits none, so a rate priced from k and ITL(k) alone over-states by
// the inverse.
func occupancyFactor(ol, ilEff, sigma float64) float64 {
	if !(ol > 0) || !(sigma > 0) || !(ilEff > 0) {
		return 1
	}
	return ol / (ol + sigma*ilEff)
}

// gpsErrorPct is the GPS check of an aggregated line with its shape term and
// its occupancy correction, at one replica's own k: the generation tokens a
// second the priced slope predicts there against the replica's own. ok false
// when the replica gives no comparison (as itl.GPSErrorPct).
func gpsErrorPct(priceA, b float64, rm domain.ReplicaMetrics, capacityTokens, kvReq, ol, ilEff, sigma float64) (float64, bool) {
	k := rm.KvUsageForITL()
	if rm.FromWarmPool || !rm.GenerationTokenRateKnown || k < itl.DefaultGPSMinKForVerification {
		return 0, false
	}
	observed := rm.GenerationTokenRate
	pred := itl.TokenRate(itl.Model{A: priceA, B: b}, k, capacityTokens, kvReq) * occupancyFactor(ol, ilEff, sigma)
	if !(observed > 0) || !(pred > 0) || math.IsInf(observed, 0) {
		return 0, false
	}
	return math.Abs(pred-observed) / observed * 100, true
}

// noteAggregatedLift moves one variant's lift on this cycle's GPS check: its
// median comparable replica within itl.DefaultGPSMismatchThresholdPct on
// aggLiftCycles consecutive comparable cycles lifts it, outside on as many
// drops it. A cycle with no figure, no sigma, or a shape change outstanding
// ends the lift and the counts; a cycle with no comparable replica moves
// nothing.
func (a *SaturationAnalyzer) noteAggregatedLift(variant string, ac *aggCycle, shapeChanging bool, logger logr.Logger) {
	a.mu.Lock()
	st := a.aggStateLocked(ac.key)
	was := st.lifted
	var med float64
	switch {
	case !ac.figure || !ac.sigmaOK || shapeChanging:
		st.agree, st.disagree, st.lifted = 0, 0, false
	case len(ac.errs) == 0:
	default:
		med, _ = medianRate(ac.errs)
		if med <= itl.DefaultGPSMismatchThresholdPct {
			st.agree, st.disagree = st.agree+1, 0
			if st.agree >= aggLiftCycles {
				st.lifted = true
			}
		} else {
			st.agree, st.disagree = 0, st.disagree+1
			if st.disagree >= aggLiftCycles {
				st.lifted = false
			}
		}
	}
	now, agree, disagree := st.lifted, st.agree, st.disagree
	a.mu.Unlock()
	if now == was {
		return
	}
	reason := "the GPS check agreed with the figure on consecutive cycles: it sizes"
	if !now {
		reason = "the figure is reported only again: the GPS check disagreed on consecutive cycles, the shape moved, or the figure or its prefill reading is missing"
	}
	logger.V(logging.DEFAULT).Info("itl-aggregated-lift",
		"variant", variant, "key", ac.key, "lifted", now, "medianErrPct", med,
		"thresholdPct", itl.DefaultGPSMismatchThresholdPct, "agree", agree, "disagree", disagree,
		"cycles", aggLiftCycles, "priceCase", ac.priceCase, "sigmaOK", ac.sigmaOK, "shapeChanging", shapeChanging,
		"reason", reason)
}
