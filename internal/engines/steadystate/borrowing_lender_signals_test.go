package steadystate

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
)

// PR-C, what on-call sees of a release to a waiting lender: the reason on the
// lowered model, a Normal Event naming the lender, the release counted by
// cause, and the latency the lender-waiting alert adds to the cooldown.
var _ = Describe("TestLenderReleaseSignals: what on-call sees of a release to a lender", func() {
	ctx := context.Background()
	now := time.Unix(50000, 0)
	var registry *prometheus.Registry

	BeforeEach(func() {
		registry = prometheus.NewRegistry()
		Expect(metrics.InitMetrics(registry)).To(Succeed())
	})

	gather := func(name string, labels ...string) map[string]float64 {
		mfs, err := registry.Gather()
		Expect(err).NotTo(HaveOccurred())
		out := map[string]float64{}
		for _, mf := range mfs {
			if mf.GetName() != name {
				continue
			}
			for _, m := range mf.GetMetric() {
				var vals []string
				for _, want := range labels {
					for _, l := range m.GetLabel() {
						if l.GetName() == want {
							vals = append(vals, l.GetValue())
						}
					}
				}
				v := m.GetGauge().GetValue()
				if m.GetCounter() != nil {
					v = m.GetCounter().GetValue()
				}
				out[strings.Join(vals, ",")] = v
			}
		}
		return out
	}

	releaseCfg := func(release bool) *config.Config {
		cfg := config.NewTestConfig()
		cfg.UpdateScalingPolicyConfig(map[string]config.ScalingPolicy{"default": {Limiters: []config.QuotaLimiterConfig{{
			Name: "kq", Type: "quota", Scope: config.QuotaScopeNamespace,
			NamespaceQuotas: map[string]map[string]int{"default": {"H100": -1}},
			Kueue:           &config.KueueQuotaSource{Enabled: true, ReleaseToNonReclaimingLenders: release},
		}}}})
		return cfg
	}

	It("reports the release on the model, as an Event, as a count, and its latency", func() {
		cfg := releaseCfg(true)
		Expect(cfg.KueueReleasesToNonReclaiming()).To(BeTrue(), "premise: the entry opts in")
		rec := record.NewFakeRecorder(10)
		e := &Engine{Config: cfg, Recorder: rec}
		cs, reqs := lenderReleaseWorld(now)
		targets := map[string]scaletarget.ScaleTargetAccessor{"b/x-v": scaletarget.NewDeploymentAccessor(
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "x-v", Namespace: "b"}})}
		e.borrowing.inFlight = allocation.NewBorrowInFlight()
		e.borrowing.governor = allocation.NewBorrowGovernor(allocation.DefaultBorrowSettings())
		e.borrowing.ledgerLoaded = true
		cs = e.governBorrowing(ctx, cs, reqs, targets, now)
		ds := lenderDecisions(reqs)
		e.releaseBorrowed(ctx, reqs, ds, targets, now)
		Expect(ds[0].TargetReplicas).To(Equal(4))
		e.publishBorrowSignals(cs, reqs, ds, now)

		Expect(gather(constants.WVAModelScalingBlocked, constants.LabelModelName, constants.LabelReason)).To(
			HaveKeyWithValue("x,"+constants.ScalingBlockedReleasedToLender, 1.0))
		Expect(gather(constants.WVABorrowReleasesTotal, constants.LabelModelName, constants.LabelCause)).To(
			HaveKeyWithValue("x,lender-waiting", 1.0))
		// No ScaledObject is known: the cluster's defaults, 300s window, 15s
		// HPA sync, 30s poll, 30s grace.
		Expect(gather(constants.WVAKueueBorrowReleaseLatencySeconds)).To(HaveKeyWithValue("", 375.0))
		var events []string
		for len(rec.Events) > 0 {
			events = append(events, <-rec.Events)
		}
		Expect(events).To(ContainElement(SatisfyAll(
			HavePrefix("Normal "+constants.K8SEventBorrowedCapacityReleased),
			ContainSubstring("to ClusterQueue lender"), ContainSubstring("cause lender-waiting"))))
	})

	It("publishes no release latency, and counts no lender-waiting series, with the option off", func() {
		e := &Engine{Config: releaseCfg(false), Recorder: record.NewFakeRecorder(10)}
		cs, reqs := lenderReleaseWorld(now)
		cs[0].SharedPools[0].ReleaseToNonReclaiming = false
		e.borrowing.inFlight = allocation.NewBorrowInFlight()
		e.borrowing.ledgerLoaded = true
		cs = e.governBorrowing(ctx, cs, reqs, nil, now)
		ds := lenderDecisions(reqs)
		e.releaseBorrowed(ctx, reqs, ds, nil, now)
		e.publishBorrowSignals(cs, reqs, ds, now)
		Expect(ds[0].TargetReplicas).To(Equal(6))
		Expect(gather(constants.WVAKueueBorrowReleaseLatencySeconds)).To(BeEmpty())
		Expect(gather(constants.WVABorrowReleasesTotal, constants.LabelModelName, constants.LabelCause)).NotTo(
			HaveKey("x,lender-waiting"))
		Expect(gather(constants.WVAModelScalingBlocked, constants.LabelModelName, constants.LabelReason)).NotTo(
			HaveKey("x," + constants.ScalingBlockedReleasedToLender))
	})
})
