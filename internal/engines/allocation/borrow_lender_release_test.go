package allocation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/kueue"
)

// PR-C: a waiting lender Kueue does not reclaim for is handed back what it
// waits for, when the entry opts in (releaseToNonReclaimingLenders). Every
// replica here is a whole 8-GPU node unless a case says otherwise.

// waitingLender is a lender on Never waiting for pending GPUs, lending unused.
func waitingLender(pending, unused int) kueue.LenderState {
	return kueue.LenderState{Queue: "lender", Waiting: true, Reclaim: kueue.ReclaimNever, Unused: unused, PendingGPUs: pending}
}

// relWorld is namespace "b" at guaranteed g holding used GPUs on H100, in
// cohort "c", whose pool reads l and Kueue's borrowed figure borrowed;
// release marks the pool as opted in.
type relWorld struct {
	g, used, borrowed int
	l                 kueue.LenderState
	release           bool
	readAt            time.Time
	classPriority     map[string]int32
}

func (w relWorld) constraints() []*ResourceConstraints {
	cs := govWorld{g: w.g, used: w.used, lenders: []kueue.LenderState{w.l}, readAt: w.readAt}.constraints()
	ch := &cs[0].SharedPools[0]
	ch.ReleaseToNonReclaiming = w.release
	ch.Borrowed = w.borrowed
	if w.borrowed == 0 {
		ch.Borrowed = max(0, w.used-w.g)
	}
	ch.ClassPriority = w.classPriority
	return cs
}

// cycleOf runs one cycle: Observe, then Releases on no-change decisions.
func cycleOf(g *BorrowGovernor, at time.Time, w relWorld, reqs ...ModelScalingRequest) ([]BorrowRelease, []domain.VariantDecision) {
	if w.readAt.IsZero() {
		w.readAt = at
	}
	c := BorrowCycle{Now: at, Constraints: w.constraints(), Requests: reqs}
	g.Observe(c)
	ds := decisionsOf(reqs)
	return g.Releases(c, ds), ds
}

// lowered is how many replicas the decisions take off each variant.
func lowered(ds []domain.VariantDecision) map[string]int {
	out := map[string]int{}
	for _, d := range ds {
		if n := d.CurrentReplicas - d.TargetReplicas; n > 0 {
			out[d.VariantName] = n
		}
	}
	return out
}

func withClass(r ModelScalingRequest, class string) ModelScalingRequest {
	for i := range r.VariantStates {
		r.VariantStates[i].PriorityClass = class
	}
	return r
}

func withMin(r ModelScalingRequest, n int) ModelScalingRequest {
	for i := range r.VariantStates {
		r.VariantStates[i].MinReplicas = &n
	}
	return r
}

func TestLenderReleaseOffChangesNothing(t *testing.T) {
	t0 := time.Unix(50000, 0)
	g := NewBorrowGovernor(DefaultBorrowSettings())
	w := relWorld{g: 16, used: 48, l: waitingLender(24, 32)}
	for i := range 4 {
		rel, ds := cycleOf(g, t0.Add(time.Duration(i)*time.Minute), w, govModel("x", 6, 8))
		if len(rel) != 0 || len(lowered(ds)) != 0 {
			t.Fatalf("cycle %d: released %+v (lowered %v) with releaseToNonReclaimingLenders off", i, rel, lowered(ds))
		}
	}
	raw, err := g.MarshalLedger()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "version") || strings.Contains(raw, "lenderReleases") {
		t.Errorf("ledger %s carries release fields with the option off; it must stay what it always was", raw)
	}
}

func TestLenderReleaseUnits(t *testing.T) {
	t0 := time.Unix(50000, 0)
	settings := DefaultBorrowSettings()
	on := func(g, used, pending, unused int) relWorld {
		return relWorld{g: g, used: used, l: waitingLender(pending, unused), release: true}
	}

	t.Run("a waiting lender gets ceil(pending/unit) units in one pass", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		rel, ds := cycleOf(gov, t0, on(16, 48, 20, 32), govModel("x", 6, 8))
		if got := lowered(ds)["x-v"]; got != 3 {
			t.Fatalf("lowered x by %d, want ceil(20/8) = 3 at once", got)
		}
		if len(rel) != 1 || rel[0].Cause != ReleaseLenderWaiting || rel[0].From != 6 || rel[0].To != 3 || rel[0].Lender.Lender != "lender" {
			t.Errorf("releases = %+v, want one x 6->3, cause lender-waiting, to lender", rel)
		}
	})

	t.Run("bounded by min(PendingGPUs, LentOut)", func(t *testing.T) {
		for _, tc := range []struct {
			name                      string
			pending, unused, borrowed int
			want                      int
		}{
			{"pending past what it lent: what it lent", 40, 16, 32, 2},
			{"Kueue's borrowed figure the smaller", 40, 32, 8, 1},
			{"a lender pending nothing gets nothing", 0, 32, 32, 0},
		} {
			gov := NewBorrowGovernor(settings)
			w := on(16, 48, tc.pending, tc.unused)
			w.borrowed = tc.borrowed
			_, ds := cycleOf(gov, t0, w, govModel("x", 6, 8))
			if got := lowered(ds)["x-v"]; got != tc.want {
				t.Errorf("%s: lowered x by %d, want %d", tc.name, got, tc.want)
			}
		}
	})

	t.Run("never below the guaranteed figure", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		_, ds := cycleOf(gov, t0, on(40, 48, 24, 32), govModel("x", 6, 8))
		if got := lowered(ds)["x-v"]; got != 1 {
			t.Errorf("lowered x by %d, want 1: only 8 GPUs lie past guaranteed 40", got)
		}
	})

	t.Run("never below minReplicas", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		_, ds := cycleOf(gov, t0, on(16, 48, 24, 32), withMin(govModel("x", 6, 8), 5))
		if got := lowered(ds)["x-v"]; got != 1 {
			t.Errorf("lowered x by %d, want 1: minReplicas 5", got)
		}
	})

	t.Run("lowest priority model first, a class not read last", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		w := on(0, 72, 16, 72)
		w.classPriority = map[string]int32{"serving": 1000, "batch": 10}
		_, ds := cycleOf(gov, t0, w, withClass(govModel("x", 3, 8), "serving"),
			withClass(govModel("y", 3, 8), "batch"), withClass(govModel("z", 3, 8), "unread"))
		if got := lowered(ds); got["y-v"] != 2 || got["x-v"] != 0 || got["z-v"] != 0 {
			t.Errorf("lowered %v, want y (batch, 10) by 2 and nothing else", got)
		}
		gov = NewBorrowGovernor(settings)
		w.l = waitingLender(56, 72)
		_, ds = cycleOf(gov, t0, w, withClass(govModel("x", 3, 8), "serving"),
			withClass(govModel("y", 3, 8), "batch"), withClass(govModel("z", 3, 8), "unread"))
		if got := lowered(ds); got["y-v"] != 3 || got["x-v"] != 3 || got["z-v"] != 1 {
			t.Errorf("lowered %v, want y 3, then x 3, then z (priority not read) 1", got)
		}
	})

	t.Run("a P/D model gives whole pairs at its role ratio", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		req := pdFleet(pdRole{"p", "prefill", 2}, pdRole{"d", "decode", 4})
		c := BorrowCycle{Now: t0, Constraints: on(0, 48, 8, 48).constraints(), Requests: []ModelScalingRequest{req}}
		c.Constraints[0].SharedPools[0].ReadAt = t0
		gov.Observe(c)
		ds := decisionsOf(c.Requests)
		for i := range ds {
			ds[i].Role = req.VariantStates[i].Role
		}
		gov.Releases(c, ds)
		if got := lowered(ds); got["p"] != 1 || got["d"] != 2 {
			t.Errorf("lowered %v, want one pair at 1P:2D for 8 GPUs pending", got)
		}
	})

	t.Run("a 16-GPU gang goes whole for an 8-GPU need, never below guaranteed", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		_, ds := cycleOf(gov, t0, on(16, 48, 8, 32), govModel("x", 3, 16))
		if got := lowered(ds)["x-v"]; got != 1 {
			t.Errorf("lowered x by %d groups, want 1: a unit larger than the need still goes", got)
		}
		gov = NewBorrowGovernor(settings)
		_, ds = cycleOf(gov, t0, on(40, 48, 8, 32), govModel("x", 3, 16))
		if got := lowered(ds)["x-v"]; got != 0 {
			t.Errorf("lowered x by %d groups, want 0: a 16-GPU group past only 8 borrowed GPUs takes guaranteed", got)
		}
	})

	t.Run("Kueue's own reclaim is left to Kueue", func(t *testing.T) {
		any := waitingLender(16, 32)
		any.Reclaim = kueue.ReclaimAny
		p := func(n int32) *int32 { return &n }
		outranks := waitingLender(16, 32)
		outranks.Reclaim, outranks.Priority = kueue.ReclaimLowerPriority, p(500)
		below := outranks
		below.Priority = p(5)
		for _, tc := range []struct {
			name string
			l    kueue.LenderState
			want int
		}{{"a lender on Any", any, 0}, {"a LowerPriority lender above the model", outranks, 0},
			{"a LowerPriority lender below the model", below, 2}} {
			gov := NewBorrowGovernor(settings)
			w := relWorld{g: 16, used: 48, l: tc.l, release: true, classPriority: map[string]int32{"batch": 10}}
			_, ds := cycleOf(gov, t0, w, withClass(govModel("x", 6, 8), "batch"))
			if got := lowered(ds)["x-v"]; got != tc.want {
				t.Errorf("%s: lowered x by %d, want %d", tc.name, got, tc.want)
			}
		}
	})

	t.Run("a stale read hands nothing back", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		w := on(16, 48, 16, 32)
		c := BorrowCycle{Now: t0, Constraints: w.constraints(), Requests: []ModelScalingRequest{govModel("x", 6, 8)}}
		c.Constraints[0].SharedPools[0].Stale = true
		gov.Observe(c)
		ds := decisionsOf(c.Requests)
		if rel := gov.Releases(c, ds); len(rel) != 0 {
			t.Errorf("released %+v on a stale read", rel)
		}
	})
}

func TestLenderReleaseInFlight(t *testing.T) {
	t0 := time.Unix(50000, 0)
	settings := DefaultBorrowSettings()
	w := relWorld{g: 16, used: 48, l: waitingLender(16, 32), release: true}

	t.Run("one in flight per lender until it lands, then a fresh read", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		// x (6) and y (2), 64 GPUs at guaranteed 16: 48 borrowed.
		w := w
		w.used = 64
		if _, ds := cycleOf(gov, t0, w, govModel("x", 6, 8), govModel("y", 2, 8)); lowered(ds)["x-v"] != 2 || lowered(ds)["y-v"] != 0 {
			t.Fatalf("first release lowered %v, want x by 2", lowered(ds))
		}
		// Still waiting, nothing landed: no second release -- not from y
		// either -- and the pin holds over a decision the demand floor raised
		// again.
		rel, ds := cycleOf(gov, t0.Add(time.Minute), w, govModel("x", 6, 8), govModel("y", 2, 8))
		if len(rel) != 0 || lowered(ds)["x-v"] != 2 || lowered(ds)["y-v"] != 0 {
			t.Fatalf("second cycle released %+v, lowered %v; want nothing new and the pin at 4", rel, lowered(ds))
		}
		if !gov.VariantStatus("b", "x-v").LenderRelease {
			t.Error("VariantStatus does not report the release in flight")
		}
		// Landed: x holds 4. The read in this cycle predates the landing.
		landed := t0.Add(2 * time.Minute)
		w4 := w
		w4.used = 48
		w4.readAt = landed.Add(-time.Second)
		if rel, _ := cycleOf(gov, landed, w4, govModel("x", 4, 8), govModel("y", 2, 8)); len(rel) != 0 {
			t.Fatalf("released %+v in the cycle it landed, before a fresh read", rel)
		}
		// A fresh read after the landing, the lender still waiting: the next.
		w4.readAt = landed.Add(20 * time.Second)
		rel, ds = cycleOf(gov, landed.Add(30*time.Second), w4, govModel("x", 4, 8), govModel("y", 2, 8))
		if len(rel) != 1 || lowered(ds)["x-v"] != 2 {
			t.Errorf("after landing and a fresh read: released %+v lowered %v, want x 4->2", rel, lowered(ds))
		}
	})

	t.Run("one that never lands times out", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		cycleOf(gov, t0, w, govModel("x", 6, 8))
		late := t0.Add(gov.lenderTimeout() + time.Minute)
		rel, _ := cycleOf(gov, late, w, govModel("x", 6, 8))
		if len(rel) != 1 {
			t.Errorf("past the release timeout released %+v, want a new release", rel)
		}
	})

	t.Run("a lender gone from a fresh read takes its pins with it", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		cycleOf(gov, t0, w, govModel("x", 6, 8))
		gone := w
		gone.l = kueue.LenderState{Queue: "other", Reclaim: kueue.ReclaimAny}
		_, ds := cycleOf(gov, t0.Add(time.Minute), gone, govModel("x", 6, 8))
		if lowered(ds)["x-v"] != 0 {
			t.Errorf("lowered %v after the lender left the cohort", lowered(ds))
		}
	})
}

func TestLenderReleaseLedger(t *testing.T) {
	t0 := time.Unix(50000, 0)
	settings := DefaultBorrowSettings()
	w := relWorld{g: 16, used: 48, l: waitingLender(16, 32), release: true}

	t.Run("a restart neither releases again nor drops the pin", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		cycleOf(gov, t0, w, govModel("x", 6, 8))
		raw, err := gov.MarshalLedger()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(raw, `"version":1`) || !strings.Contains(raw, `"lenderReleases"`) {
			t.Fatalf("ledger %s carries no versioned release", raw)
		}
		l, err := UnmarshalBorrowLedger(raw)
		if err != nil {
			t.Fatal(err)
		}
		next := NewBorrowGovernor(settings)
		if dropped := next.Restore(l); dropped != 0 {
			t.Fatalf("dropped %d of what this controller wrote", dropped)
		}
		rel, ds := cycleOf(next, t0.Add(time.Minute), w, govModel("x", 6, 8))
		if len(rel) != 0 || lowered(ds)["x-v"] != 2 {
			t.Errorf("after the restart released %+v, lowered %v; want nothing new, x pinned at 4", rel, lowered(ds))
		}
	})

	t.Run("nothing is released while the ledger is unread", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		c := BorrowCycle{Now: t0, Constraints: w.constraints(), Requests: []ModelScalingRequest{govModel("x", 6, 8)}, LedgerUnread: true}
		c.Constraints[0].SharedPools[0].ReadAt = t0
		gov.Observe(c)
		if rel := gov.Releases(c, decisionsOf(c.Requests)); len(rel) != 0 {
			t.Errorf("released %+v before the persisted ledger was read", rel)
		}
	})

	t.Run("a newer version is refused; corrupt entries are dropped", func(t *testing.T) {
		if _, err := UnmarshalBorrowLedger(`{"version":2,"lenderReleases":[]}`); !errors.Is(err, ErrBorrowLedgerVersion) {
			t.Errorf("version 2: err %v, want ErrBorrowLedgerVersion", err)
		}
		if l, err := UnmarshalBorrowLedger(`{"timeoutHeld":{"b/x-v":3}}`); err != nil || l.TimeoutHeld["b/x-v"] != 3 {
			t.Errorf("a ledger written before the version field: %+v %v, want read as version 1", l, err)
		}
		good := LenderRelease{LenderKey: LenderKey{Cohort: "c", Lender: "l", Flavor: "h100", Resource: "nvidia.com/gpu"},
			Units: []ReleasedUnit{{Namespace: "b", Variant: "x-v", To: 4}}, GPUs: 16, ReleasedAt: t0}
		noLender, negative, noUnits := good, good, good
		noLender.Lender = "other-but-empty-cohort"
		noLender.Cohort = ""
		negative.Lender = "l2"
		negative.Units = []ReleasedUnit{{Namespace: "b", Variant: "x-v", To: -1}}
		noUnits.Lender = "l3"
		noUnits.Units = nil
		gov := NewBorrowGovernor(settings)
		if dropped := gov.Restore(BorrowLedger{LenderReleases: []LenderRelease{good, noLender, negative, noUnits}}); dropped != 3 {
			t.Errorf("dropped %d, want the 3 entries this controller cannot have written", dropped)
		}
		if got := gov.Ledger().LenderReleases; len(got) != 1 || got[0].Lender != "l" {
			t.Errorf("kept %+v, want the good entry alone", got)
		}
		b, _ := json.Marshal(gov.Ledger())
		if !strings.Contains(string(b), `"version":1`) {
			t.Errorf("restored ledger %s is not versioned", b)
		}
	})
}

// A release never gives back what another release of the cycle, or the
// reclaim yield for a wake, already took.
func TestLenderReleaseNoDoubleRelease(t *testing.T) {
	t0 := time.Unix(50000, 0)
	settings := DefaultBorrowSettings()

	t.Run("a replica the wake's reclaim yield gave back is not given again", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		w := relWorld{g: 40, used: 48, l: waitingLender(16, 32), release: true, readAt: t0}
		c := BorrowCycle{Now: t0, Constraints: w.constraints(), Requests: []ModelScalingRequest{govModel("x", 6, 8)}}
		gov.Observe(c)
		ds := decisionsOf(c.Requests)
		ds[0].TargetReplicas = 5 // the pass yielded one replica to the wake's reclaim
		if rel := gov.Releases(c, ds); len(rel) != 0 || ds[0].TargetReplicas != 5 {
			t.Errorf("released %+v (target %d) after the yield took the only borrowed replica; want nothing more",
				rel, ds[0].TargetReplicas)
		}
	})

	t.Run("a model with a kueue-gate or placement-timeout pin is not released to a lender", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		gov.Restore(BorrowLedger{TimeoutHeld: map[string]int{"b/x-v": 5}})
		w := relWorld{g: 0, used: 96, l: waitingLender(8, 96), release: true}
		_, ds := cycleOf(gov, t0, w, govModel("x", 6, 8), govModel("y", 6, 8))
		if got := lowered(ds); got["x-v"] != 1 || got["y-v"] != 1 {
			t.Errorf("lowered %v, want x by its pin alone (1) and the lender's 8 GPUs from y (1)", got)
		}
	})

	t.Run("a model released to a lender is not timed out in the same flight", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		w := relWorld{g: 16, used: 48, l: waitingLender(8, 32), release: true}
		cycleOf(gov, t0, w, govModel("x", 6, 8))
		stuck := unplacedVariant(govModel("x", 6, 8), "x-v", t0)
		rel, ds := cycleOf(gov, t0.Add(settings.PlacementTimeout+time.Minute), w, stuck)
		if len(rel) != 0 || lowered(ds)["x-v"] != 1 {
			t.Errorf("released %+v, lowered %v; want only the lender's pin (1), no placement-timeout release on top", rel, lowered(ds))
		}
	})

	t.Run("a placement-timeout release in the same cycle counts against the lender's", func(t *testing.T) {
		gov := NewBorrowGovernor(settings)
		w := relWorld{g: 40, used: 56, l: waitingLender(8, 32), release: true}
		stuck := unplacedVariant(govModel("x", 7, 8), "x-v", t0)
		seenPast(gov, BorrowCycle{Constraints: w.constraints(), Requests: []ModelScalingRequest{stuck}}, t0)
		rel, ds := cycleOf(gov, t0.Add(settings.PlacementTimeout+time.Minute), w, stuck, govModel("y", 1, 8))
		causes := map[ReleaseCause]int{}
		for _, r := range rel {
			causes[r.Cause]++
		}
		if causes[ReleasePlacementTimeout] != 1 || lowered(ds)["x-v"] != 1 {
			t.Fatalf("releases %+v lowered %v, want x released once for the placement timeout", rel, lowered(ds))
		}
		if lowered(ds)["y-v"] != 1 || causes[ReleaseLenderWaiting] != 1 {
			t.Errorf("lowered %v, want the lender's 8 GPUs from y, the 8 left past guaranteed", lowered(ds))
		}
	})
}
