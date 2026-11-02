package saturation

import (
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// ITL per token, not per step (issue #274).
//
// vLLM observes inter_token_latency once per request per engine step, however
// many tokens the step emitted. With speculative decoding or MTP a step emits
// 1 + the accepted draft tokens, so the ITL mean is the step time, and a line
// fitted from it prices a replica's token rate low by that factor -- 1.5-3x
// on typical acceptance -- and the floor orders that many replicas too many.
//
// The ITL line is fitted from the time per token instead: each of the
// variant's rows is read through domain.ReplicaMetrics.ITLPerToken before the
// window takes it, with the accepted tokens per step the collector read over
// the ITL's own window (registration.QuerySpecAcceptedPerStep). A replica
// whose flags speculate and whose counter was not read offers no reading,
// rather than a step time taken for a token time. SGLang's ITL is per token
// already and is read as it is.
//
// The window, the baseline, the held slope and the shared line are then in
// time per token. On a vLLM engine that speculates, that differs from what a
// build without the correction learned, so the learned-state snapshot records
// the unit of each variant's ITL entries and each line (ITLPerToken in
// LearnedState), and a speculating variant's entries in the other unit are
// neither restored nor lent.
//
// The throughput and slo analyzers still read the step time; they keep their
// own lines.

// SwitchITLPerToken is the replay switch for the correction (#274): on from
// the constructor; a replay turns it off (SetSwitchesForReplay), and the ITL
// line is fitted from the step time, as before.
const SwitchITLPerToken = "itl-per-accepted-token"

// itlRows is rows with variant's read per token (domain.ReplicaMetrics.
// ITLPerToken) under params, the engine flags the variant runs. Other
// variants' rows are left as they are; the window reads only variant's. With
// the switch off, or when no row changes, rows is returned itself; otherwise a
// copy, since the input is shared with the rest of the cycle.
func (a *SaturationAnalyzer) itlRows(rows []domain.ReplicaMetrics, variant string,
	params *capacity.EngineParams) []domain.ReplicaMetrics {
	if !a.itlPerToken {
		return rows
	}
	speculates := params.SpeculatesPerStep()
	var out []domain.ReplicaMetrics
	for i, rm := range rows {
		if rm.VariantName != variant {
			continue
		}
		read := rm.ITLPerToken(speculates)
		if read.AvgITL == rm.AvgITL && read.AvgITLKnown == rm.AvgITLKnown {
			continue
		}
		if out == nil {
			out = make([]domain.ReplicaMetrics, len(rows))
			copy(out, rows)
		}
		out[i] = read
	}
	if out == nil {
		return rows
	}
	return out
}

// itlUnitsDiffer says whether ITL state learned in the unit perToken names
// (itl_per_token.go) is in another unit than this analyzer reads for an
// engine run under params: only on vLLM with speculative decoding, where a
// step is not one token.
func (a *SaturationAnalyzer) itlUnitsDiffer(perToken bool, params *capacity.EngineParams) bool {
	return params.SpeculatesPerStep() && perToken != a.itlPerToken
}
