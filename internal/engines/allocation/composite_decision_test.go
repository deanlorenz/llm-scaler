package allocation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/aggregation"
)

// makeAnalyzer builds an eligible (Live, informative) NamedAnalyzerResult for
// name whose only variant is "v" with the given demand and PRC, so its
// TotalReplicas(v) = demand/prc when prc > 0.
func makeAnalyzer(name string, demand, prc float64) NamedAnalyzerResult {
	reason := "measured"
	if prc <= 0 {
		reason = ReasonNoData
	}
	return NamedAnalyzerResult{
		Name: name,
		Live: true,
		Result: &domain.AnalyzerResult{
			TotalDemand: demand,
			VariantCapacities: []domain.VariantCapacity{
				{VariantName: "v", PerReplicaCapacity: prc, Reason: reason},
			},
		},
	}
}

// testDecision mirrors ResolveSO's old return shape, so the ported test
// bodies below stay close to their pre-rewrite originals.
type testDecision struct {
	n            float64
	ok           bool
	path         DecisionPath
	contributors []string
}

// resolveSOForTest runs composite.go step 5's inline contributor-collection
// and decision switch in isolation from buildComposite, for testing that
// logic without standing up a full Engine/collectV2ModelRequest. This is the
// "small test-only helper" composite-signal-redesign.md's coder task calls
// for now that ResolveSO/SODecision are deleted and their logic lives inline
// in steadystate.buildComposite (not exported, not callable directly from
// this package's tests).
//
// entries plays eligibleAnalyzers's role (may or may not include sat, mirrors
// ResolveSO's old "entries" parameter where sat's own presence/eligibility in
// the list was what determined whether it could be an ordinary contributor);
// sat is buildComposite's separate, always-present identity/fallback source
// (found from namedResults regardless of eligibility, exactly as
// findSaturation does). variant is the SO being resolved.
func resolveSOForTest(entries []NamedAnalyzerResult, sat *NamedAnalyzerResult, variant string) testDecision {
	type contribution struct {
		name string
		n    float64
	}
	var contributors []contribution
	for _, e := range entries {
		if !Eligible(e) {
			continue
		}
		evc, present := findVariantCapacityForTest(e.Result, variant)
		if !present || evc.Reason == ReasonNoData || evc.Reason == ReasonError {
			continue
		}
		n, ok := TotalReplicas(e.Result, evc)
		if !ok {
			continue
		}
		contributors = append(contributors, contribution{name: e.Name, n: n})
	}

	switch {
	case len(contributors) == 0:
		// A sat with no real result (nil, erroring, no-data, or stale) must
		// never produce a fallback value -- mirrors composite.go step 5's
		// Eligible(sat) guard, added after the first version of this switch
		// let a stale sat leak a fallback value (caught by make test).
		if sat == nil || sat.Result == nil || !Eligible(*sat) {
			return testDecision{path: DecisionNoSignal}
		}
		satVC, present := findVariantCapacityForTest(sat.Result, variant)
		if satN, ok := TotalReplicas(sat.Result, satVC); present && ok {
			return testDecision{n: satN, ok: true, path: DecisionSatFallback, contributors: []string{sat.Name}}
		}
		return testDecision{path: DecisionNoSignal}
	case len(contributors) == 1:
		return testDecision{n: contributors[0].n, ok: true, path: DecisionSingle, contributors: []string{contributors[0].name}}
	default:
		var names []string
		var best float64
		for _, c := range contributors {
			names = append(names, c.name)
			if c.n > best {
				best = c.n
			}
		}
		return testDecision{n: best, ok: true, path: DecisionAgree, contributors: names}
	}
}

// findVariantCapacityForTest mirrors composite.go's private findVariantCapacity,
// duplicated here since that helper lives in the steadystate package.
func findVariantCapacityForTest(result *domain.AnalyzerResult, variant string) (domain.VariantCapacity, bool) {
	if result == nil {
		return domain.VariantCapacity{}, false
	}
	for _, vc := range result.VariantCapacities {
		if vc.VariantName == variant {
			return vc, true
		}
	}
	return domain.VariantCapacity{}, false
}

var _ = Describe("TotalReplicas", func() {

	It("computes D/PRC for a non-disaggregated result (demand in TotalDemand)", func() {
		result := &domain.AnalyzerResult{
			TotalDemand:       1000,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}},
		}
		vc, _ := findVariantCapacityForTest(result, "v")
		n, ok := TotalReplicas(result, vc)
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(5.0))
	})

	It("computes D/PRC for a disaggregated result, using the variant's own role", func() {
		result := &domain.AnalyzerResult{
			RoleDemand: map[string]float64{"prefill": 400, "decode": 900},
			VariantCapacities: []domain.VariantCapacity{
				{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100},
				{VariantName: "d1", Role: "decode", PerReplicaCapacity: 300},
			},
		}
		pVC, _ := findVariantCapacityForTest(result, "p1")
		n, ok := TotalReplicas(result, pVC)
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(4.0))

		dVC, _ := findVariantCapacityForTest(result, "d1")
		n, ok = TotalReplicas(result, dVC)
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(3.0))
	})

	// spec test 21: PRC > 0 with demand(role(SO)) == 0 is explicitly legal.
	It("is defined and zero when demand is a real zero (test 21)", func() {
		result := &domain.AnalyzerResult{
			TotalDemand:       0,
			VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200}},
		}
		vc, _ := findVariantCapacityForTest(result, "v")
		n, ok := TotalReplicas(result, vc)
		Expect(ok).To(BeTrue())
		Expect(n).To(BeZero())
	})

	// spec test 18: PRC == 0, demand > 0 -> undefined, no division by zero.
	It("is undefined when PRC is zero (test 18)", func() {
		vc := domain.VariantCapacity{VariantName: "v", PerReplicaCapacity: 0}
		result := &domain.AnalyzerResult{TotalDemand: 500, VariantCapacities: []domain.VariantCapacity{vc}}
		_, ok := TotalReplicas(result, vc)
		Expect(ok).To(BeFalse())
	})

	It("is undefined when PRC is negative", func() {
		vc := domain.VariantCapacity{VariantName: "v", PerReplicaCapacity: -10}
		result := &domain.AnalyzerResult{TotalDemand: 500, VariantCapacities: []domain.VariantCapacity{vc}}
		_, ok := TotalReplicas(result, vc)
		Expect(ok).To(BeFalse())
	})

	// spec test 19: both zero -> no panic, no NaN/Inf, just undefined.
	It("is undefined, not NaN, when both PRC and demand are zero (test 19)", func() {
		vc := domain.VariantCapacity{VariantName: "v", PerReplicaCapacity: 0}
		result := &domain.AnalyzerResult{TotalDemand: 0, VariantCapacities: []domain.VariantCapacity{vc}}
		n, ok := TotalReplicas(result, vc)
		Expect(ok).To(BeFalse())
		Expect(n).To(BeZero()) // zero value, not NaN — ok is the authority, not n
	})

	// spec §5.4/A4: an analyzer silent about a role does not participate in it.
	It("is undefined when the analyzer never attributed demand to the variant's role (A4)", func() {
		vc := domain.VariantCapacity{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100}
		result := &domain.AnalyzerResult{
			RoleDemand:        map[string]float64{"decode": 900}, // says nothing about prefill
			VariantCapacities: []domain.VariantCapacity{vc},
		}
		_, ok := TotalReplicas(result, vc)
		Expect(ok).To(BeFalse())
	})

	It("is undefined for a nil result", func() {
		_, ok := TotalReplicas(nil, domain.VariantCapacity{VariantName: "v", PerReplicaCapacity: 200})
		Expect(ok).To(BeFalse())
	})

	// spec test 9: unit independence — scaling both D and PRC by the same
	// constant k must leave TotalReplicas unchanged.
	It("is unit-independent: scaling demand and PRC by the same constant leaves N unchanged (test 9)", func() {
		baseVC := domain.VariantCapacity{VariantName: "v", PerReplicaCapacity: 200}
		base := &domain.AnalyzerResult{TotalDemand: 1000, VariantCapacities: []domain.VariantCapacity{baseVC}}
		scaledVC := domain.VariantCapacity{VariantName: "v", PerReplicaCapacity: 200 * 37}
		scaled := &domain.AnalyzerResult{TotalDemand: 1000 * 37, VariantCapacities: []domain.VariantCapacity{scaledVC}}

		nBase, okBase := TotalReplicas(base, baseVC)
		nScaled, okScaled := TotalReplicas(scaled, scaledVC)
		Expect(okBase).To(BeTrue())
		Expect(okScaled).To(BeTrue())
		Expect(nScaled).To(Equal(nBase))
	})

	// AggN's max-over-contributors behavior (formerly a separate function,
	// now step 5's inline switch in buildComposite) reduces to a single
	// TotalReplicas call when there is exactly one contributor -- ported here
	// since AggN itself is deleted.
	It("reduces to the single contributor's N when called on its own result (formerly AggN's single-contributor case)", func() {
		vc := domain.VariantCapacity{VariantName: "v", PerReplicaCapacity: 200}
		result := &domain.AnalyzerResult{TotalDemand: 1000, VariantCapacities: []domain.VariantCapacity{vc}}
		n, ok := TotalReplicas(result, vc)
		Expect(ok).To(BeTrue())
		Expect(n).To(Equal(5.0))
	})
})

// The following port composite_decision_test.go's original ResolveSO/
// SODecision cases onto resolveSOForTest, which exercises the same
// contributor-collection + decision-switch logic now inlined in
// steadystate.buildComposite (composite-signal-redesign.md §2.1(b)-(d)).
var _ = Describe("buildComposite's contributor/decision logic (via resolveSOForTest)", func() {

	Describe("C0-agree / C1-single", func() {
		It("records C1-single with exactly one contributor", func() {
			other := makeAnalyzer("throughput", 600, 200) // N=3
			d := resolveSOForTest([]NamedAnalyzerResult{other}, nil, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSingle))
			Expect(d.n).To(Equal(3.0))
			Expect(d.contributors).To(ConsistOf("throughput"))
		})

		It("records C0-agree with more than one contributor, taking the max (test 2)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5
			other := makeAnalyzer("throughput", 2400, 200)                // N=12
			d := resolveSOForTest([]NamedAnalyzerResult{sat, other}, &sat, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionAgree))
			Expect(d.n).To(Equal(12.0))
			Expect(d.contributors).To(ConsistOf(domain.SaturationAnalyzerName, "throughput"))
		})

		// spec test 3: sat is a fallback, not a floor — a lower-N contributor
		// can pull the composite below sat's own value when sat also
		// qualifies as an ordinary contributor.
		It("allows the aggregate to fall below saturation's own N when both are ordinary contributors (test 3)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 2400, 200) // N=12
			other := makeAnalyzer("throughput", 1000, 200)                // N=5
			d := resolveSOForTest([]NamedAnalyzerResult{sat, other}, &sat, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionAgree))
			Expect(d.n).To(Equal(12.0)) // max(12,5) = 12, but not a floor -- see below
		})
	})

	Describe("C2-sat-fallback", func() {
		// spec test 8: no other contributor for this SO, saturation used as
		// fallback.
		It("falls back to saturation when no other analyzer contributes for this SO", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5
			otherNoData := NamedAnalyzerResult{
				Name: "throughput",
				Live: true,
				Result: &domain.AnalyzerResult{
					VariantCapacities: []domain.VariantCapacity{{VariantName: "v", Reason: "T1-ols"}}, // informative but no PRC for v -> undefined N
				},
			}
			// throughput is NOT an eligibleAnalyzers member with a defined
			// contribution here (its own per-SO Reason check and TotalReplicas
			// both fail it), and sat itself is not in "entries" (excluded
			// upstream, mirroring sat being disabled) -- so the fallback fires.
			d := resolveSOForTest([]NamedAnalyzerResult{otherNoData}, &sat, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(5.0))
			Expect(d.contributors).To(ConsistOf(domain.SaturationAnalyzerName))
		})

		// spec test 8a: idle SO, own store record -- PRC from P0-store, N
		// defined, composite scales up via the fallback path.
		It("scales up from an idle SO's P0-store PRC via the fallback path (test 8a)", func() {
			sat := NamedAnalyzerResult{
				Name: domain.SaturationAnalyzerName,
				Live: true,
				Result: &domain.AnalyzerResult{
					TotalDemand: 500,
					VariantCapacities: []domain.VariantCapacity{
						{VariantName: "v", PerReplicaCapacity: 100, Reason: "P0-store"},
					},
				},
			}
			d := resolveSOForTest(nil, &sat, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(5.0))
		})

		// spec test 8b: never-seen-before SO -- borrowed EffectiveCapacity
		// (also P0-store) contributes normally, not discounted or clamped.
		It("does not discount a borrowed P0-store PRC for a never-seen-before SO (test 8b)", func() {
			satBorrowed := NamedAnalyzerResult{
				Name: domain.SaturationAnalyzerName,
				Live: true,
				Result: &domain.AnalyzerResult{
					TotalDemand: 500,
					VariantCapacities: []domain.VariantCapacity{
						{VariantName: "v", PerReplicaCapacity: 250, Reason: "P0-store"}, // borrowed, possibly an over-estimate
					},
				},
			}
			d := resolveSOForTest(nil, &satBorrowed, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(2.0)) // 500/250, exactly as reported -- no clamp/discount
		})
	})

	Describe("C4-no-signal", func() {
		// spec test 8c: no-data (PRC=0) -- N undefined, contribution
		// skipped, decision path records it, no division/panic.
		It("records no-signal when saturation is present but has no-data for this SO (test 8c)", func() {
			sat := NamedAnalyzerResult{
				Name: domain.SaturationAnalyzerName,
				Live: true,
				Result: &domain.AnalyzerResult{
					VariantCapacities: []domain.VariantCapacity{
						{VariantName: "v", PerReplicaCapacity: 0, Reason: ReasonNoData},
					},
				},
			}
			d := resolveSOForTest(nil, &sat, "v")
			Expect(d.ok).To(BeFalse())
			Expect(d.path).To(Equal(DecisionNoSignal))
		})

		It("records no-signal when there are no entries and no sat at all", func() {
			d := resolveSOForTest(nil, nil, "v")
			Expect(d.ok).To(BeFalse())
			Expect(d.path).To(Equal(DecisionNoSignal))
		})

		It("records no-signal when saturation itself is not eligible and nothing else contributes", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200)
			sat.Live = false // not eligible
			d := resolveSOForTest(nil, &sat, "v")
			Expect(d.ok).To(BeFalse())
			Expect(d.path).To(Equal(DecisionNoSignal))
		})

		// Regression guard (spec §2.9, corrected 2026-09-14): sat disabled
		// (excluded from eligibleAnalyzers/"entries" upstream) AND
		// !Eligible(sat) (here: stale) with no other contributor must record
		// DecisionNoSignal, never DecisionSatFallback. A sat with no real
		// result -- nil, erroring, no-data, or stale -- must never
		// participate in scaling decisions, fallback or otherwise, ever.
		// This is the exact gap make test caught in the first version of
		// step 5's switch, which was missing the Eligible(sat) guard.
		It("never falls back to a disabled-and-stale sat, even with no other contributor (regression guard, spec §2.9)", func() {
			disabledAndStaleSat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5, would satisfy TotalReplicas if reached
			disabledAndStaleSat.Live = false                                              // stale -- fails Eligible()
			// sat excluded from "entries" (mirrors being excluded from
			// eligibleAnalyzers upstream for being disabled) AND not itself
			// Eligible -- both conditions spec §2.9 requires for the
			// regression guard.
			d := resolveSOForTest(nil, &disabledAndStaleSat, "v")
			Expect(d.path).ToNot(Equal(DecisionSatFallback),
				"a sat with no real result must never produce a fallback value")
			Expect(d.path).To(Equal(DecisionNoSignal))
			Expect(d.ok).To(BeFalse())
			Expect(d.contributors).To(BeEmpty())
		})
	})

	Describe("eligibility gating (test 6, 7)", func() {
		// spec test 6: non-live analyzer -> not a contributor.
		It("excludes a non-live analyzer from contributing (test 6)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5, fallback-eligible
			other := makeAnalyzer("throughput", 2400, 200)                // N=12, but non-live
			other.Live = false
			d := resolveSOForTest([]NamedAnalyzerResult{other}, &sat, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(5.0))
			Expect(d.contributors).To(ConsistOf(domain.SaturationAnalyzerName))
		})

		// spec test 7: non-informative analyzer (Reason=no-data/error) -> not
		// a contributor.
		It("excludes a non-informative analyzer from contributing (test 7)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5
			other := NamedAnalyzerResult{
				Name: "throughput",
				Live: true,
				Result: &domain.AnalyzerResult{
					TotalDemand:       2400,
					VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200, Reason: ReasonNoData}},
				},
			}
			d := resolveSOForTest([]NamedAnalyzerResult{other}, &sat, "v")
			Expect(d.ok).To(BeTrue())
			Expect(d.path).To(Equal(DecisionSatFallback))
			Expect(d.n).To(Equal(5.0))
		})

		// Regression guard (spec §2.9): a stale analyzer must never appear in
		// contributors, regardless of what its per-SO Reason says -- this is
		// the gap this mission's spec-correction round closed (Eligible() as
		// the loop's first, unconditional check).
		It("excludes a stale (!Live) analyzer even though its per-SO Reason looks perfectly usable (regression guard, spec §2.9)", func() {
			sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5, fallback-eligible
			staleButOtherwiseUsable := NamedAnalyzerResult{
				Name: "throughput",
				Live: false, // stale -- must be excluded by Eligible(), never reaching the per-SO Reason check
				Result: &domain.AnalyzerResult{
					TotalDemand:       2400,
					VariantCapacities: []domain.VariantCapacity{{VariantName: "v", PerReplicaCapacity: 200, Reason: "measured"}}, // N=12, looks perfectly fine per-SO
				},
			}
			d := resolveSOForTest([]NamedAnalyzerResult{staleButOtherwiseUsable}, &sat, "v")
			Expect(d.contributors).ToNot(ContainElement("throughput"),
				"a stale analyzer must never contribute, regardless of its per-SO Reason")
			Expect(d.path).To(Equal(DecisionSatFallback),
				"with the stale analyzer excluded, only sat's fallback contribution remains")
			Expect(d.n).To(Equal(5.0))
		})
	})

	// spec test 9: unit independence at the decision level too.
	It("is unit-independent: scaling demand and PRC by the same constant leaves the decision unchanged (test 9)", func() {
		sat := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 200) // N=5
		scaledSat := makeAnalyzer(domain.SaturationAnalyzerName, 1000*13, 200*13)
		d1 := resolveSOForTest(nil, &sat, "v")
		d2 := resolveSOForTest(nil, &scaledSat, "v")
		Expect(d1.ok).To(BeTrue())
		Expect(d2.ok).To(BeTrue())
		Expect(d2.n).To(Equal(d1.n))
		Expect(d2.path).To(Equal(d1.path))
	})

	// spec test 10: perturbing PRC_sat must not change another analyzer's
	// contribution to the composite.
	It("PRC_sat independence: perturbing saturation's own PRC does not change another analyzer's N (test 10)", func() {
		other := makeAnalyzer("throughput", 2400, 200) // N=12, independent of sat entirely

		satLowPRC := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 50)   // N=20
		satHighPRC := makeAnalyzer(domain.SaturationAnalyzerName, 1000, 500) // N=2

		dLow := resolveSOForTest([]NamedAnalyzerResult{satLowPRC, other}, &satLowPRC, "v")
		dHigh := resolveSOForTest([]NamedAnalyzerResult{satHighPRC, other}, &satHighPRC, "v")

		// other's own contribution (12) is identical in both cases; only the
		// winner of the max (and therefore the aggregate) may change based
		// on sat's PRC, but other's computed N_i is untouched by it.
		otherVC, _ := findVariantCapacityForTest(other.Result, "v")
		otherN, ok := TotalReplicas(other.Result, otherVC)
		Expect(ok).To(BeTrue())
		Expect(otherN).To(Equal(12.0))

		Expect(dLow.ok).To(BeTrue())
		Expect(dLow.n).To(Equal(20.0)) // sat's low-PRC N wins
		Expect(dHigh.ok).To(BeTrue())
		Expect(dHigh.n).To(Equal(12.0)) // other's N wins once sat's own N drops to 2
	})

	// spec test 11: analyzer with no RoleDemand for a role -> not a
	// contributor for that role (A4).
	It("excludes an analyzer that never attributed demand to this SO's role (test 11)", func() {
		sat := NamedAnalyzerResult{
			Name: domain.SaturationAnalyzerName,
			Live: true,
			Result: &domain.AnalyzerResult{
				RoleDemand: map[string]float64{"decode": 900},
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100, Reason: "measured"},
				},
			},
		}
		d := resolveSOForTest(nil, &sat, "p1")
		Expect(d.ok).To(BeFalse())
		Expect(d.path).To(Equal(DecisionNoSignal))
	})

	// spec test 23: a role carrying demand but currently served by no SO at
	// all -- no crash, and the decision reflects a genuinely uncovered role
	// (C4-no-signal) rather than a spurious full coverage. Distinct from
	// test 11: here the role's demand IS attributed (RoleDemand has a real
	// entry), but nothing in VariantCapacities serves it, so there is no SO
	// to even ask the question of.
	It("does not crash and records no-signal for a role with demand but no serving SO (test 23)", func() {
		sat := NamedAnalyzerResult{
			Name: domain.SaturationAnalyzerName,
			Live: true,
			Result: &domain.AnalyzerResult{
				RoleDemand: map[string]float64{"decode": 900}, // decode has real demand...
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100, Reason: "measured"},
					// ...but no variant serves decode at all.
				},
			},
		}
		d := resolveSOForTest(nil, &sat, "decode-variant-that-does-not-exist")
		Expect(d.ok).To(BeFalse())
		Expect(d.path).To(Equal(DecisionNoSignal))
		Expect(d.contributors).To(BeEmpty())
	})

	// spec test 4 (ported from prc_com_test.go, deleted with PRCCom): disaggregated,
	// per-role aggregation where another analyzer is higher for ONE role only --
	// the other role must be unaffected.
	It("aggregates independently per role: a higher contributor for prefill only raises the decision there, not for decode (test 4)", func() {
		sat := NamedAnalyzerResult{
			Name: domain.SaturationAnalyzerName,
			Live: true,
			Result: &domain.AnalyzerResult{
				RoleDemand: map[string]float64{"prefill": 400, "decode": 900},
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100, Reason: "measured"}, // N=4
					{VariantName: "d1", Role: "decode", PerReplicaCapacity: 300, Reason: "measured"},  // N=3
				},
			},
		}
		other := NamedAnalyzerResult{
			Name: "throughput",
			Live: true,
			Result: &domain.AnalyzerResult{
				RoleDemand: map[string]float64{"prefill": 800},
				VariantCapacities: []domain.VariantCapacity{
					{VariantName: "p1", Role: "prefill", PerReplicaCapacity: 100, Reason: "measured"}, // N=8, higher than sat's 4
					// other says nothing about decode/d1 at all.
				},
			},
		}

		dPrefill := resolveSOForTest([]NamedAnalyzerResult{sat, other}, &sat, "p1")
		Expect(dPrefill.ok).To(BeTrue())
		Expect(dPrefill.n).To(Equal(8.0), "other's higher N must win for prefill")

		dDecode := resolveSOForTest([]NamedAnalyzerResult{sat, other}, &sat, "d1")
		Expect(dDecode.ok).To(BeTrue())
		Expect(dDecode.n).To(Equal(3.0), "decode must be untouched by other's prefill-only disagreement")

		// The read half of old prc_com_test.go's assertion: PRC_com computed
		// from these N's, using aggregation.DemandForRole directly (the
		// PRC line itself is now inlined in buildComposite, not a separate
		// function to call here).
		prefillDemand, ok := aggregation.DemandForRole(sat.Result, "prefill")
		Expect(ok).To(BeTrue())
		Expect(prefillDemand/dPrefill.n).To(Equal(50.0), "400/8 -- lower than sat's own PRC of 100")

		decodeDemand, ok := aggregation.DemandForRole(sat.Result, "decode")
		Expect(ok).To(BeTrue())
		Expect(decodeDemand/dDecode.n).To(Equal(300.0), "decode's PRC is unaffected, since nothing disagreed there")
	})
})
