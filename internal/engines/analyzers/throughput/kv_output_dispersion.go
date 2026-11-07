package throughput

import (
	"math"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/fleet"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// The KV a resident request holds, with output lengths that vary (issue #433,
// split from #276; the same correction as saturation's, kv_output_dispersion.go
// there, and the slo analyzer's, output_dispersion.go there).
//
// A long request stays resident longer, so a KV snapshot finds it in
// proportion to how long it stays, and the average resident request holds
// ILeff + D*OL/2, D = E[O^2]/E[O]^2 (shape.WithOutputDispersion). With D taken
// as 1 this analyzer read too many sequences from a fill, by 7-44% at IL = 2
// OL: its supply (computeVariantSupply) read high, and it ordered too few
// replicas where saturation, on the same variant, now prices the spread.
//
// Here KVreq sets how many sequences a fill holds, in the supply at k_sat, the
// local demand at k* (computeLocalDemand) and the GPS check, so a larger D
// lowers all three by the same factor.
//
// D is read as saturation reads it: from each replica's lower bound on E[O^2]
// (domain.SquaredMeanLowerBound), request-weighted over the replicas the
// variant's shape averages (averageShapeMetrics), and only when every one of
// them read the bound; otherwise 1, no spread. Unlike saturation it is not
// held at 1 across a change of shape: across one the [5m] histogram holds both
// shapes and reads high, and here a higher D only lowers the supply, so the
// side it errs on is more replicas.

// SwitchKVPerRequestOutputDispersion is the replay switch for the spread of
// output lengths in the KV per request on the throughput analyzer (issue
// #433). On, a resident request is priced at ILeff + D*OL/2, D the spread
// read from the output-length histogram. Off, as before, at ILeff + OL/2.
const SwitchKVPerRequestOutputDispersion = "throughput-kv-per-request-output-dispersion"

// variantOutputDispersion is the spread of a variant's output lengths, D =
// E[O^2]/E[O]^2, over the replicas averageShapeMetrics averages and with its
// weights, so the two are one reading of one variant. 1 unless every one of
// them read a positive, finite bound on E[O^2].
func variantOutputDispersion(rows []domain.ReplicaMetrics) float64 {
	counted := shapeEligible
	// A NaN bound fails the > 0 below; +Inf is refused by name.
	if !slices.ContainsFunc(rows, counted) ||
		slices.ContainsFunc(rows, func(m domain.ReplicaMetrics) bool {
			return counted(m) && !(m.AvgOutputTokensSquaredKnown && m.AvgOutputTokensSquared > 0 &&
				!math.IsInf(m.AvgOutputTokensSquared, 0))
		}) {
		return 1
	}
	mean := fleet.Mean(rows, func(m domain.ReplicaMetrics) float64 { return m.AvgOutputTokens }, counted)
	squared := fleet.Mean(rows, func(m domain.ReplicaMetrics) float64 { return m.AvgOutputTokensSquared }, counted)
	return shape.Dispersion(mean, squared)
}
