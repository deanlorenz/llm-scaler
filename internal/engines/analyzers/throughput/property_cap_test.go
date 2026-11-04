package throughput

import (
	"math"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/internal/learnertest"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/itl"
)

// Property cases for the line's cap admission (analyzer-learner.md, PR 4b-3;
// SwitchITLAdmitsAtSequenceCap), each through Analyze: the edges the
// enumeration names for the ITL learner (the sequence cap, and
// itlBand.capMaxPreemptionRate at its default and at a value an operator may
// set), and known truth for a variant whose only replica in reach is at its
// cap. The mutant is the switch off (TestThroughputCapPropertyMutants).

// propCap is the stated cap per rank the cases run at.
const propCap = 64

// capCases is the cap admission's property cases, on the analyzer newA builds.
func capCases(newA propAnalyzer) []learnertest.Case {
	c := func(class learnertest.Class, name string, seeds int, check func(uint64) error) learnertest.Case {
		return learnertest.Case{Class: class, Learner: propLearner, Name: name, Family: learnertest.FamilyDirect,
			Tuning: "default", Seeds: seeds, Check: check}
	}
	return []learnertest.Case{
		c(learnertest.ClassEdges, "sequence-cap", 1, checkSequenceCap(newA)),
		c(learnertest.ClassEdges, "sequence-cap-edges", 1, checkSequenceCapEdges(newA)),
		c(learnertest.ClassEdges, "cap-preemption-default", 1, checkCapPreemption(newA, nil, 0)),
		c(learnertest.ClassEdges, "cap-preemption-non-default", 1, checkCapPreemption(newA, capPreemptionPolicy(0.5), 0.5)),
		c(learnertest.ClassKnownTruth, "at-cap-with-siblings-below-the-band", learnertest.CheapSeeds, checkAtCapKnownTruth(newA)),
	}
}

// capPreemptionPolicy is a policy whose itlBand.capMaxPreemptionRate is rate.
func capPreemptionPolicy(rate float64) *config.ScalingPolicy {
	return &config.ScalingPolicy{AnalyzerTuning: config.AnalyzerTuning{
		ITLBand: config.ITLBandTuning{CapMaxPreemptionRate: &rate}}}
}

// declaredCapStore is a capacity store whose v1 states its cap by the
// llm-d.ai/max-num-seqs annotation only (propCap), as the probe's does.
func declaredCapStore() *capacity.Store {
	store := capacity.NewStore()
	store.Update("ns", "m", "v1", capacity.Record{EngineParams: &capacity.EngineParams{Engine: "vllm", DeclaredBatchCap: propCap}},
		learnertest.Start)
	return store
}

// cappedRow is pod-a above the band at k, on the true line, its batch at
// propCap on its one rank, requests waiting, no preemption, read.
func cappedRow(k float64) domain.ReplicaMetrics {
	row := lineRow("pod-a", k, propITL(k), learnertest.Start.Add(-time.Hour))
	row.RunningRequests, row.RunningRequestsKnown, row.QueueLength = propCap, true, 5
	row.PreemptionRate, row.PreemptionRateKnown = 0, true
	return row
}

// admittedAbove reports whether one cycle of row, on a fresh analyzer newA
// builds with v1's cap stated in store (nil for none) under policy cfg (nil
// for the defaults), put a reading in the band's window.
func admittedAbove(newA propAnalyzer, store *capacity.Store, cfg domain.AnalyzerConfig, row domain.ReplicaMetrics) (bool, error) {
	a := newA()
	if store != nil {
		a = a.WithCapacityStore(store)
	}
	r := newPropRun(a)
	r.cfg = cfg
	if _, _, err := r.step([]domain.ReplicaMetrics{row}); err != nil {
		return false, err
	}
	return r.windowLen() == 1, nil
}

// Edges, the sequence cap: a replica above the band at its stated cap
// (llm-d.ai/max-num-seqs 64, 64 running, requests waiting, no preemption)
// is a reading (#287, #293). Before learner PR 4b-3 the line refused it, and
// the ledger listed the case.
func checkSequenceCap(newA propAnalyzer) func(uint64) error {
	return func(uint64) error {
		ok, err := admittedAbove(newA, declaredCapStore(), nil, cappedRow(0.90))
		if err != nil {
			return err
		}
		return learnertest.Holds(ok, learnertest.KindNotLearned,
			"a replica at its sequence cap above the band was refused")
	}
}

// Edges, the sequence cap from both sides: the batch exactly at the cap on
// every rank is admitted, and one sequence either side of it, an empty
// queue, an unread preemption rate, a cap nobody states (the parser's
// default, at which the batch sits), and a two-rank replica at one rank's
// cap are each refused.
func checkSequenceCapEdges(newA propAnalyzer) func(uint64) error {
	type edge struct {
		name  string
		store *capacity.Store
		row   func(*domain.ReplicaMetrics)
		want  bool
	}
	// The parser's default: max_num_seqs read as 256 with no flag setting it.
	defaulted := capacity.EngineParams{Engine: "vllm", MaxNumSeqs: 256}
	defaultCap := defaulted.BatchCap()
	unstated := capacity.NewStore()
	unstated.Update("ns", "m", "v1", capacity.Record{EngineParams: &defaulted}, learnertest.Start)
	edges := []edge{
		{"at the cap", nil, func(*domain.ReplicaMetrics) {}, true},
		{"one under the cap", nil, func(m *domain.ReplicaMetrics) { m.RunningRequests = propCap - 1 }, false},
		{"one over the cap", nil, func(m *domain.ReplicaMetrics) { m.RunningRequests = propCap + 1 }, false},
		{"no queue", nil, func(m *domain.ReplicaMetrics) { m.QueueLength = 0 }, false},
		{"preemption unread", nil, func(m *domain.ReplicaMetrics) { m.PreemptionRateKnown = false }, false},
		{"preemption NaN", nil, func(m *domain.ReplicaMetrics) { m.PreemptionRate = math.NaN() }, false},
		{"only the default cap", unstated, func(m *domain.ReplicaMetrics) { m.RunningRequests = int(defaultCap) }, false},
		{"two ranks, one rank's cap", nil, func(m *domain.ReplicaMetrics) { m.EngineRanks = 2 }, false},
		{"two ranks, both at the cap", nil, func(m *domain.ReplicaMetrics) {
			m.EngineRanks, m.RunningRequests = 2, 2*propCap
		}, true},
	}
	return func(uint64) error {
		if defaultCap <= 0 || defaulted.StatedBatchCap() != 0 {
			return learnertest.Violate(learnertest.KindNotLearned, "the default edge tests nothing: default cap %d, stated %d",
				defaultCap, defaulted.StatedBatchCap())
		}
		for _, e := range edges {
			store := e.store
			if store == nil {
				store = declaredCapStore()
			}
			row := cappedRow(0.90)
			e.row(&row)
			got, err := admittedAbove(newA, store, nil, row)
			if err != nil {
				return err
			}
			if got != e.want {
				kind := learnertest.KindWrongSide
				if e.want {
					kind = learnertest.KindNotLearned
				}
				return learnertest.Violate(kind, "%s: admitted %t, want %t", e.name, got, e.want)
			}
		}
		return nil
	}
}

// Edges, itlBand.capMaxPreemptionRate: a replica at its cap preempting at
// exactly the policy's rate is admitted, and one preempting just over it is
// refused; at the default (0) the first preemption refuses.
func checkCapPreemption(newA propAnalyzer, cfg *config.ScalingPolicy, rate float64) func(uint64) error {
	return func(uint64) error {
		var policy domain.AnalyzerConfig
		if cfg != nil {
			policy = cfg
		}
		for _, side := range []struct {
			preempt float64
			want    bool
		}{{rate, true}, {rate + 0.001, false}} {
			row := cappedRow(0.90)
			row.PreemptionRate = side.preempt
			got, err := admittedAbove(newA, declaredCapStore(), policy, row)
			if err != nil {
				return err
			}
			if got != side.want {
				kind := learnertest.KindWrongSide
				if side.want {
					kind = learnertest.KindNotLearned
				}
				return learnertest.Violate(kind, "preempting %.3f/s against capMaxPreemptionRate %.3f: admitted %t, want %t",
					side.preempt, rate, got, side.want)
			}
		}
		return nil
	}
}

// Known truth, a variant at its cap: one replica above the band at its
// stated cap, its k wandering over 0.86-0.94, and two siblings below the
// band. The band's line prices the variant from the capped replica's
// readings -- not the above-band fallback -- from its tenth reading, and
// never under the truth at itl.DefaultKSat by more than propTruthTol (the
// line pins B at the default, 6 ms against a true 10 ms, so it prices a
// little under the truth there).
func checkAtCapKnownTruth(newA propAnalyzer) func(uint64) error {
	return func(seed uint64) error {
		const cycles = 24
		f := newPropFleet(learnertest.Rand(seed), cycles, 0.86, 0.90, 0.94)
		a := newA().WithCapacityStore(declaredCapStore())
		early := learnertest.Start.Add(-time.Hour)
		_, first, lines, err := lineOf(a, cycles, func(c int) []domain.ReplicaMetrics {
			k := f.ks[c%len(f.ks)]
			row := cappedRow(k)
			row.AvgITL = propITL(k) * (1 + f.noise[c%len(f.ks)][c])
			return []domain.ReplicaMetrics{row,
				lineRow("pod-b", 0.08, propITL(0.08), early), lineRow("pod-c", 0.08, propITL(0.08), early)}
		})
		if err != nil {
			return err
		}
		if first < 0 || len(lines) == 0 {
			return learnertest.Violate(learnertest.KindNotLearned, "no line in %d cycles with a replica at its cap", cycles)
		}
		if n := a.PathCounts()[PathITLAboveBand]; n > 0 {
			return learnertest.Violate(learnertest.KindNotLearned, "%d cycles priced from the above-band fallback, not the band's line", n)
		}
		if len(lines) != cycles-itl.DefaultMinSamples+1 {
			return learnertest.Violate(learnertest.KindNotLearned, "priced on %d cycles, want every cycle from the tenth reading (%d)",
				len(lines), cycles-itl.DefaultMinSamples+1)
		}
		truth := propITL(itl.DefaultKSat)
		for c, m := range lines {
			if p := propPriced(m); p < truth/(1+propTruthTol) {
				return learnertest.Violate(learnertest.KindOverPriced, "cycle %d: priced %.4f s, under the truth %.4f s by %.2f %%",
					c, p, truth, 100*(1-p/truth))
			}
		}
		return nil
	}
}

// capMutants is each cap case that must fail with the cap admission off:
// every one that admits a reading.
var capMutants = []string{
	"edges/throughput-itl/sequence-cap",
	"edges/throughput-itl/sequence-cap-edges",
	"edges/throughput-itl/cap-preemption-default",
	"edges/throughput-itl/cap-preemption-non-default",
	"known-truth/throughput-itl/at-cap-with-siblings-below-the-band",
}

// TestThroughputCapPropertyMutants runs the cap cases with
// SwitchITLAdmitsAtSequenceCap off, the line 4b-2 built: each must fail
// there, and its first failure is the case's negative control.
func TestThroughputCapPropertyMutants(t *testing.T) {
	cases := capCases(withSwitchOff(SwitchITLAdmitsAtSequenceCap))
	if len(cases) != len(capMutants) {
		t.Fatalf("%d cap cases, %d named mutants", len(cases), len(capMutants))
	}
	for i, c := range cases {
		if c.ID() != capMutants[i] {
			t.Fatalf("case %d is %s, want %s", i, c.ID(), capMutants[i])
		}
		seeds, _, err := learnertest.SeedsFor(c.Seeds)
		if err != nil {
			t.Fatal(err)
		}
		run, err := learnertest.RunCase(c.Check, seeds)
		if err != nil {
			t.Fatalf("%s: %v", c.ID(), err)
		}
		if len(run.Failures) == 0 {
			t.Errorf("%s passes with the cap admission off: it tests nothing of it", c.ID())
			continue
		}
		t.Logf("cap admission off, %s %s", c.ID(), run)
	}
}
