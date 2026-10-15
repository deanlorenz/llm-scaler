package constants

// The SLO analyzer's metrics. See docs/proposals/slo-tiers.md, "Observability".
const (
	// WVASLOTargetSeconds is the latency target a model's policy declares.
	// Labels: namespace, model_name, objective, statistic.
	WVASLOTargetSeconds = "wva_slo_target_seconds"
	// WVASLOObservedSeconds is the latency the router observed over its window:
	// the mean for TPOT, the configured percentile for TTFT. Absent when the
	// router exported too few requests to judge.
	// Labels: namespace, model_name, objective, statistic.
	WVASLOObservedSeconds = "wva_slo_observed_seconds"
	// WVASLOAttainmentRatio is the share of requests whose TTFT met the target.
	// Absent below the sample minimum, never 0 or 1 by default.
	// Labels: namespace, model_name, objective, statistic.
	WVASLOAttainmentRatio = "wva_slo_attainment_ratio"
	// WVASLOFill is the KV fill, as a fraction of physical KV, at which a
	// variant's mean inter-token latency meets the TPOT target -- the fill its
	// SLO capacity is priced at. Absent when the variant could not be priced.
	// Labels: namespace, model_name, variant_name, objective.
	WVASLOFill = "wva_slo_fill"
	// WVASLOPredictedITLSeconds is the mean inter-token latency the variant's
	// line predicts at the fill the fleet would actually run at once the
	// scale-up headroom is applied: the margin an operator really has.
	// Labels: namespace, model_name, variant_name.
	WVASLOPredictedITLSeconds = "wva_slo_predicted_itl_seconds"
	// WVASLOSource is 1 for the source a variant's SLO capacity came from and 0
	// for every other, like wva_replica_capacity_source.
	// Labels: namespace, model_name, variant_name, source.
	WVASLOSource = "wva_slo_source"

	// WVASLOWindowRequests is how many requests the router recorded for the
	// objective over its window. Present whenever the router reported anything,
	// so an absent wva_slo_observed_seconds beside it means "too few requests to
	// judge", and both absent means the router reported nothing.
	// Labels: namespace, model_name, objective, statistic.
	WVASLOWindowRequests = "wva_slo_window_requests"

	// WVASLOExcluded is 1 for the reason a model's router window is not being
	// judged this cycle and 0 for the others; all 0 while it is judged. A
	// window is excluded while it covers a wake from zero or a replica
	// starting, and for a grace period after, because those requests waited out
	// capacity no steady-state sizing could have had ready; and after cycles the
	// analyzer did not see (a skipped model, a controller restart). Per
	// objective: a prefill replica starting excludes TTFT and not TPOT.
	// Labels: namespace, model_name, objective, reason.
	WVASLOExcluded = "wva_slo_excluded"

	// WVASLOPrefillSeconds, WVASLOEngineQueueSeconds, WVASLOPrefillUtilization
	// and WVASLOPrefillCeiling are the TTFT curve's inputs for a variant that
	// prefills: its mean prefill time (first scheduled to first token), its mean
	// engine queue wait, its prefill token rate per replica over its learned
	// ceiling, and the ceiling itself in tokens per second. Observed and not
	// priced: the curve is priced only once these show it can be learned.
	// Labels: namespace, model_name, variant_name.
	WVASLOPrefillSeconds     = "wva_slo_prefill_seconds"
	WVASLOEngineQueueSeconds = "wva_slo_engine_queue_seconds"
	WVASLOPrefillUtilization = "wva_slo_prefill_utilization"
	WVASLOPrefillCeiling     = "wva_slo_prefill_ceiling_tokens_per_second"

	// LabelObjective names an SLO objective: "ttft" or "tpot".
	LabelObjective = "objective"
	// LabelStatistic names the statistic an objective is judged on: "mean", or
	// "p" followed by the percentile ("p90").
	LabelStatistic = "statistic"
)

// SLO objective label values.
const (
	SLOObjectiveTTFT = "ttft"
	SLOObjectiveTPOT = "tpot"
)

// SLO source label values: where a variant's SLO capacity came from, or why
// there is none.
const (
	// SLOSourceOwn: the variant's own ordinary least-squares ITL fit.
	SLOSourceOwn = "own"
	// SLOSourcePinned: a fit with the intercept pinned, because the fleet's
	// readings did not span enough of k to fit both coefficients. A balanced
	// fleet often sits here indefinitely.
	SLOSourcePinned = "pinned"
	// SLOSourcePinnedDefault: a pinned fit whose intercept is the built-in
	// floor, not one this variant was ever measured at. Not a measurement of
	// the hardware: on a large accelerator the real intercept is several times
	// the floor, and the slope fitted against it is too steep.
	SLOSourcePinnedDefault = "pinned-default"
	// SLOSourceNoOutputLength: no replica has reported an output length, so
	// neither the demand nor the per-request KV footprint is known.
	SLOSourceNoOutputLength = "no-output-length"
	// SLOSourceNoLine: too few readings to fit any line.
	SLOSourceNoLine = "no-line"
	// SLOSourceInfeasible: no fill in the line's range meets the target.
	SLOSourceInfeasible = "infeasible"
	// SLOSourceInputInvalid: an input was missing, non-finite or non-positive.
	SLOSourceInputInvalid = "input-invalid"
)

// Reasons a model's router window is excluded from judgement.
const (
	SLOExcludedZeroReplicas = "zero-replicas"
	SLOExcludedStarting     = "starting"
	// SLOExcludedUnobserved: the analyzer did not see the model's previous
	// cycles -- it was skipped, as a waking model whose Pods have no metrics
	// yet is, or the controller restarted -- so the window may hold a
	// transient nothing here saw.
	SLOExcludedUnobserved = "unobserved"
)

// SLOExcludedReasons is every exclusion reason, in the order they are published.
var SLOExcludedReasons = []string{SLOExcludedZeroReplicas, SLOExcludedStarting, SLOExcludedUnobserved}

// SLOSources is every SLO source value, in the order they are published.
var SLOSources = []string{
	SLOSourceOwn, SLOSourcePinned, SLOSourcePinnedDefault, SLOSourceNoLine, SLOSourceNoOutputLength,
	SLOSourceInfeasible, SLOSourceInputInvalid,
}
