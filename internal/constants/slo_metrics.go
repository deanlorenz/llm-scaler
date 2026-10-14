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
	// Labels: namespace, model_name, objective.
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
	// SLOSourceNoLine: too few readings to fit any line.
	SLOSourceNoLine = "no-line"
	// SLOSourceInfeasible: no fill in the line's range meets the target.
	SLOSourceInfeasible = "infeasible"
	// SLOSourceInputInvalid: an input was missing, non-finite or non-positive.
	SLOSourceInputInvalid = "input-invalid"
)

// SLOSources is every SLO source value, in the order they are published.
var SLOSources = []string{
	SLOSourceOwn, SLOSourcePinned, SLOSourceNoLine, SLOSourceInfeasible, SLOSourceInputInvalid,
}
