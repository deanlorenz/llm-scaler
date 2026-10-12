package allocation

import (
	"testing"
	"time"
)

// mixedStepRun is a quota of 16 split between D, holding 12 in 4-GPU steps but
// giving from an 8-GPU variant, and R, holding 4 and growing by 8: both
// targets are 8, so D is above its target and R below. One of D's replicas
// is twice its target's step.
func mixedStepRun() SharePlanInput {
	return SharePlanInput{
		Roles: []ShareRole{
			{Key: "D", Weight: 1, Need: 3.5, Floor: 4, Ceiling: 64, ReplicaGPUs: 4},
			{Key: "R", Weight: 1, Need: 6, Floor: 4, Ceiling: 64, ReplicaGPUs: 4},
		},
		Held:       map[string]int{"D": 12, "R": 4},
		Thresholds: map[string]float64{"D": 0.8, "R": 0.8},
		Budget:     16,
		Tolerance:  0.15,
		Give:       map[string]ShareVariant{"D": {Name: "d", GPUs: 8, PodGPUs: []int{8}}},
		Grow:       map[string]ShareVariant{"R": {Name: "r", GPUs: 8, PodGPUs: []int{8}}},
	}
}

// A donor never gives below its whole-replica target: D's 8-GPU replica would
// leave it 4 of its 8, though D, nearly idle, would still read in its
// continuous band. Taken, D is short and R above its own target, and the
// next plan moves the GPUs back.
func TestShareDonorKeepsItsTargetWhenAReplicaIsTwoSteps(t *testing.T) {
	l := NewShareLedger()
	in := mixedStepRun()
	for i := range 6 {
		p := PlanShareTransfers(l, in, time.Unix(int64(1000+15*i), 0), simTimings())
		for _, tr := range p.Started {
			if tr.Donor == "D" {
				t.Fatalf("cycle %d: D gave %d of its 12 against a target of 8: %+v", i, tr.DonorGPUs, tr)
			}
		}
	}
}

// Moves never reverse while demand holds, by the targets alone: every move
// goes from a role above its target to one below its own and leaves the donor
// at or above its target, so a receiver is never above its target while its
// donor is below. Each cycle lands its moves at once and starts a fresh
// ledger, so no hold, confirm cycle or release in flight can be what stops a
// reversal. On the mixed-step quota both roles give and grow by 8 GPUs and
// need little: one replica would swap which of the two is below its target,
// and, unbounded, the next would swap it back.
func TestShareMovesDoNotReverseUnderSteadyDemand(t *testing.T) {
	in := mixedStepRun()
	in.Roles[1].Need = 4 // R below its target of 8, D above it, neither short of its need
	vs := map[string]ShareVariant{"D": {Name: "d", GPUs: 8, PodGPUs: []int{8}}, "R": {Name: "r", GPUs: 8, PodGPUs: []int{8}}}
	in.Give, in.Grow = vs, vs
	in.ConfirmCycles = 1
	in.Headroom = ShareHeadroom{Capped: true, Replicas: 1}
	gave := map[string]string{}
	for i := range 10 {
		p := PlanShareTransfers(NewShareLedger(), in, time.Unix(int64(1000+15*i), 0), simTimings())
		for _, tr := range p.Started {
			if tr.Receiver == "" {
				continue
			}
			if gave[tr.Receiver] == tr.Donor {
				t.Fatalf("cycle %d: %s gives back to %s, which gave to it (held %v)", i, tr.Donor, tr.Receiver, in.Held)
			}
			gave[tr.Donor] = tr.Receiver
			in.Held = map[string]int{tr.Donor: in.Held[tr.Donor] - tr.DonorGPUs, tr.Receiver: in.Held[tr.Receiver] + tr.GPUs}
		}
	}
}
