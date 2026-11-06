package whatif

import (
	"fmt"
	"math"
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// The staircase run's frozen ladder (docs/proposals/slo-capacity-planner-ttft.md,
// "As built: the decode term"): P1's estimate and the decode line as the run's
// warm-up recording left them, on two pods a role, flow control off. The
// recording is not committed here, so its figures are literals.
//
// ladderTerm is what the same recording teaches: D from
// prefill.DecodeTermWindow over its rows, R the warm-up's fit of the router's
// _sum and _count (which no learner recording holds, so it was read from the
// run's Prometheus). Both come from the warm-up alone; no staircase level went
// into them.
var ladderTerm = DecodeTerm{
	Engine:  PromptLine{Base: 0.0003864652858474246, PerToken: 5.084782327291034e-06},
	PerStep: 6.394844679228212,
	Hop:     PromptLine{Base: 0.00461, PerToken: 0.511e-6},
}

// ladderBucket is one shape of the staircase, with the Ca^2 it was sent at and
// the recorded transfer the old sum read.
type ladderBucket struct {
	prompt, output, kvPerRequest, ca2, transfer float64
}

var ladderBuckets = map[string]ladderBucket{
	"A": {prompt: 1000, output: 1000, kvPerRequest: 1500, transfer: 0.0080 / 1000},
	"B": {prompt: 8000, output: 200, kvPerRequest: 8100, transfer: 0.0314 / 8500},
	"C": {prompt: 8000, output: 200, kvPerRequest: 8100, ca2: 4, transfer: 0.0314 / 8500},
}

// ladderFleet is the fleet of one bucket on the old sum, which the term
// replaces.
func ladderFleet(b ladderBucket) TTFTFleet {
	return TTFTFleet{
		Prefill: Prefill{
			VP: 288287.9, FixedLatency: 0.01205735519529711, VPPromptTokens: 8001.162709910401,
			CurveSlope: 4.61284350108191e-07, CurveCurvature: 2.2578832669308503e-10, VPMargin: DefaultVPMargin,
			Imbalance: 1.192798939189922, Replicas: 2, PromptTokens: b.prompt,
			PromptCV: 0.2 / math.Sqrt(12), ArrivalCV2: b.ca2, TransferSecondsPerToken: b.transfer,
		},
		Decode: &Decode{
			Line: itl.Model{A: 0.03002838165320376, B: 0.0020791987727279727}, Band: DefaultBand(), Tier: "pinned-B",
			CapacityTokens: 1162240, KVPerRequest: b.kvPerRequest, OutputTokens: b.output, ReplicaCap: 256, Replicas: 2,
		},
		FlowControl:     FlowControl{State: FlowControlOff},
		LegacyDecodeSum: true,
	}
}

// The ladder before and after the term. Before is the frozen run of main at
// 59772fce2, to its 5 decimals: the old sum must reproduce it. After pins the
// term. Every figure rises, the floors with them.
func TestLadder_BeforeAndAfterTheTerm(t *testing.T) {
	const tol = 6e-6 // half the last decimal printed, and a little
	for _, l := range ladderFigures {
		t.Run(fmt.Sprintf("%s at %g q %g", l.bucket, l.rate, l.q), func(t *testing.T) {
			fl := ladderFleet(ladderBuckets[l.bucket])
			old := mustTTFT(t, ttftAt(t, fl, l.rate, l.q))
			if math.Abs(old.Seconds-l.oldSeconds) > tol || math.Abs(old.Floor-l.oldFloor) > tol {
				t.Errorf("before: %.5f floor %.5f, frozen %.5f floor %.5f", old.Seconds, old.Floor, l.oldSeconds, l.oldFloor)
			}
			fl.LegacyDecodeSum = false
			term := ladderTerm
			fl.DecodeTerm = &term
			now := mustTTFT(t, ttftAt(t, fl, l.rate, l.q))
			if math.Abs(now.Seconds-l.newSeconds) > tol || math.Abs(now.Floor-l.newFloor) > tol {
				t.Errorf("after: %.5f floor %.5f, pinned %.5f floor %.5f", now.Seconds, now.Floor, l.newSeconds, l.newFloor)
			}
			if !(now.Seconds > old.Seconds) || !(now.Floor > old.Floor) {
				t.Errorf("fell: %.5f -> %.5f, floor %.5f -> %.5f", old.Seconds, now.Seconds, old.Floor, now.Floor)
			}
		})
	}
}

// On the staircase's own fleet a target between the two floors refuses, where
// the old sum answered: bucket A's p90 floor is 31 ms by the old sum and 40 ms
// by the term.
func TestLadder_ATargetUnderTheCorrectedFloorRefuses(t *testing.T) {
	fl := ladderFleet(ladderBuckets["A"])
	target := TTFTTarget{Seconds: 0.035, Percentile: 0.9}
	if a := mustAnswer(t, rateForTTFT(t, fl, target)); !(a.Value > 0) {
		t.Fatalf("the old sum gave %g", a.Value)
	}
	fl.LegacyDecodeSum = false
	term := ladderTerm
	fl.DecodeTerm = &term
	u, ok := rateForTTFT(t, fl, target).(Unanswerable)
	if !ok || u.Reason != TargetBelowFloor {
		t.Errorf("want TargetBelowFloor, got %#v", rateForTTFT(t, fl, target))
	}
}

// ladderFigures is every level of the staircase, at the mean, p90 and p95, in
// seconds.
var ladderFigures = []struct {
	bucket                                     string
	rate, q                                    float64
	oldSeconds, oldFloor, newSeconds, newFloor float64
}{
	{"A", 17.066, 0, 0.03017, 0.03011, 0.04605, 0.03941},
	{"A", 17.066, 0.9, 0.03112, 0.03097, 0.04678, 0.04009},
	{"A", 17.066, 0.95, 0.03157, 0.03122, 0.04715, 0.04029},
	{"A", 23.466, 0, 0.03020, 0.03011, 0.05059, 0.03941},
	{"A", 23.466, 0.9, 0.03119, 0.03097, 0.05136, 0.04009},
	{"A", 23.466, 0.95, 0.03189, 0.03122, 0.05205, 0.04029},
	{"A", 29.865, 0, 0.03022, 0.03011, 0.05779, 0.03941},
	{"A", 29.865, 0.9, 0.03128, 0.03097, 0.05861, 0.04009},
	{"A", 29.865, 0.95, 0.03237, 0.03122, 0.05999, 0.04029},
	{"B", 17.581, 0, 0.08166, 0.07594, 0.11632, 0.10287},
	{"B", 17.581, 0.9, 0.10672, 0.08022, 0.14164, 0.10828},
	{"B", 17.581, 0.95, 0.11589, 0.08149, 0.15081, 0.10989},
	{"B", 24.174, 0, 0.08523, 0.07594, 0.12576, 0.10287},
	{"B", 24.174, 0.9, 0.11586, 0.08022, 0.15659, 0.10828},
	{"B", 24.174, 0.95, 0.12781, 0.08149, 0.16854, 0.10989},
	{"B", 30.767, 0, 0.09072, 0.07594, 0.14168, 0.10287},
	{"B", 30.767, 0.9, 0.12916, 0.08022, 0.18027, 0.10828},
	{"B", 30.767, 0.95, 0.14509, 0.08149, 0.19620, 0.10989},
	{"B", 33.404, 0, 0.09581, 0.07594, 0.15329, 0.10287},
	{"B", 33.404, 0.9, 0.14040, 0.08022, 0.19801, 0.10828},
	{"B", 33.404, 0.95, 0.15944, 0.08149, 0.21705, 0.10989},
	{"B", 36.042, 0, 0.10379, 0.07594, 0.16965, 0.10287},
	{"B", 36.042, 0.9, 0.15667, 0.08022, 0.22263, 0.10828},
	{"B", 36.042, 0.95, 0.17996, 0.08149, 0.24592, 0.10989},
	{"B", 38.679, 0, 0.11221, 0.07594, 0.19129, 0.10287},
	{"B", 38.679, 0.9, 0.17282, 0.08022, 0.25198, 0.10828},
	{"B", 38.679, 0.95, 0.20037, 0.08149, 0.27954, 0.10989},
	{"B", 41.316, 0, 0.15888, 0.07594, 0.26196, 0.10287},
	{"B", 41.316, 0.9, 0.22333, 0.08022, 0.32649, 0.10828},
	{"B", 41.316, 0.95, 0.34792, 0.08149, 0.45108, 0.10989},
	{"C", 17.581, 0, 0.09874, 0.07594, 0.13340, 0.10287},
	{"C", 17.581, 0.9, 0.15969, 0.08022, 0.19439, 0.10828},
	{"C", 17.581, 0.95, 0.21399, 0.08149, 0.24869, 0.10989},
	{"C", 24.174, 0, 0.11299, 0.07594, 0.15352, 0.10287},
	{"C", 24.174, 0.9, 0.20438, 0.08022, 0.24495, 0.10828},
	{"C", 24.174, 0.95, 0.26856, 0.08149, 0.30913, 0.10989},
	{"C", 30.767, 0, 0.13451, 0.07594, 0.18548, 0.10287},
	{"C", 30.767, 0.9, 0.26260, 0.08022, 0.31359, 0.10828},
	{"C", 30.767, 0.95, 0.34179, 0.08149, 0.39278, 0.10989},
	{"C", 33.404, 0, 0.15199, 0.07594, 0.20947, 0.10287},
	{"C", 33.404, 0.9, 0.30634, 0.08022, 0.36385, 0.10828},
	{"C", 33.404, 0.95, 0.39785, 0.08149, 0.45536, 0.10989},
	{"C", 36.042, 0, 0.17722, 0.07594, 0.24308, 0.10287},
	{"C", 36.042, 0.9, 0.36624, 0.08022, 0.43212, 0.10828},
	{"C", 36.042, 0.95, 0.47462, 0.08149, 0.54050, 0.10989},
	{"C", 38.679, 0, 0.20273, 0.07594, 0.28181, 0.10287},
	{"C", 38.679, 0.9, 0.42473, 0.08022, 0.50382, 0.10828},
	{"C", 38.679, 0.95, 0.54995, 0.08149, 0.62904, 0.10989},
	{"C", 41.316, 0, 0.27142, 0.07594, 0.37450, 0.10287},
	{"C", 41.316, 0.9, 0.62747, 0.08022, 0.73057, 0.10828},
	{"C", 41.316, 0.95, 1.08740, 0.08149, 1.19050, 0.10989},
}
