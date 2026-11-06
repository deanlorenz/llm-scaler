package whatif

// What the TTFT model's callers, the slo analyzer's S1 and the what-if CLI,
// pass for an input with no reading (docs/proposals/slo-tiers.md, "S1
// decisions"). The model itself reads none of them: it prices what it is given.
const (
	// AssumedPromptCV is the prompt spread with no computed-prompt histogram:
	// not measured, the top of the spread taken as plausible. No committed
	// recording carries that histogram.
	AssumedPromptCV = 2.0
	// PromptCVStop is the most spread a caller passes, and where the callers'
	// peak tables end. It is a stop, not the value that prices the longest
	// TTFT: at no load that is the peak, 2.22 at p90, and past 3 the p90 falls
	// fast, to 1.96 times the mean by cv 4. It is not maxPromptCV, the model's
	// own bound, past which the gamma says nothing and the call is refused.
	PromptCVStop = 3.0
	// ImbalanceFloor is the least f assumed on more than one pod. Not
	// measured: it is the TTFT model's own synthetic multi-pod case, near P1's
	// 2:1:1:1 example (1.6). The prefill-stress run at three pods read a raw
	// share of at most 1.33 over its loaded cycles, so it held there. At light
	// load the raw share is chance: the same run read 3.00 at 0.06 r/s.
	ImbalanceFloor = 1.5
	// RoutingBoundShare is the f / N_p at which one pod takes the work and
	// more pods would mostly sit idle behind the router. Not measured.
	RoutingBoundShare = 0.8
	// FlowControlQueueThreshold and FlowControlKVThreshold are the EPP
	// detector's upstream defaults, which this repository's install leaves
	// unchanged, assumed with the gate on: nothing recorded reads them.
	FlowControlQueueThreshold = 5.0
	FlowControlKVThreshold    = 0.8
)
