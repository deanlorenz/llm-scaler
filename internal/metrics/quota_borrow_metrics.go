package metrics

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
)

// Kueue cohort borrowing series
// (the Kueue borrowing proposal, PR #161, Part C). Every
// other borrowing condition is a reason on wva_model_scaling_blocked.
var (
	quotaLimitGPUs      *prometheus.GaugeVec
	quotaBorrowWithheld *prometheus.GaugeVec
	kueueUnplacedPods   *prometheus.GaugeVec
	kueuePreemptedPods  *prometheus.CounterVec
	kueueLenderWaiting  *prometheus.GaugeVec
	borrowReleases      *prometheus.CounterVec
	kueueBorrowDrift    *prometheus.GaugeVec
	kueueNeverRead      *prometheus.GaugeVec
	kueueVersionKnown   *prometheus.GaugeVec
	kueueCooldown       *prometheus.GaugeVec
	kueueBorrowedGPUs   *prometheus.GaugeVec
)

// registerQuotaBorrowMetrics creates and registers the borrowing series.
// Called from InitMetrics, after controllerInstance is set.
func registerQuotaBorrowMetrics(registry prometheus.Registerer) error {
	withInstance := func(labels ...string) []string {
		if controllerInstance != "" {
			labels = append(labels, constants.LabelControllerInstance)
		}
		return labels
	}
	quotaLimitGPUs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAQuotaLimitGPUs,
		Help: "A namespace-scope quota entry's GPU cap for one namespace and accelerator, by tier. " +
			"guaranteed: the cap without borrowing. borrowable: what the namespace may draw past it now, " +
			"were no other namespace to draw first -- min(its borrow cap, the free GPUs of its Kueue " +
			"cohorts' shared pools serving the accelerator); 0 where borrowing is not counted. " +
			"An unlimited guaranteed figure publishes no series.",
	}, withInstance(constants.LabelLimiterName, constants.LabelNamespace, constants.LabelAccelerator, constants.LabelTier))
	quotaBorrowWithheld = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAQuotaBorrowWithheld,
		Help: "1 for the reason a namespace's borrowable GPUs are 0, for a quota entry that reads Kueue " +
			"borrowing. Present only while the reason holds.",
	}, withInstance(constants.LabelLimiterName, constants.LabelNamespace, constants.LabelAccelerator, constants.LabelReason))
	kueueUnplacedPods = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAKueueUnplacedPods,
		Help: "Pods of a variant, in a namespace that counts Kueue borrowing, that have not placed for longer " +
			"than the placement timeout (Unschedulable, or held at Kueue's admission gate). Absent otherwise.",
	}, withInstance(constants.LabelNamespace, constants.LabelVariantName))
	kueuePreemptedPods = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: constants.WVAKueuePreemptedPodsTotal,
		Help: "Preempted Workloads of a model (a Workload evicted, reason Preempted) in a namespace that counts " +
			"borrowing: one per Workload, so a LeaderWorkerSet or pod group counts once.",
	}, withInstance(constants.LabelNamespace, constants.LabelModelName))
	kueueLenderWaiting = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAKueueLenderWaitingSeconds,
		Help: "Seconds a Kueue lender ClusterQueue has waited for capacity it lent, published only while a " +
			"namespace this controller governs still borrows in its cohort. Above the borrow cooldown, " +
			"Kueue has not taken back what it lent.",
	}, withInstance(constants.LabelCohort, constants.LabelClusterQueue))
	borrowReleases = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: constants.WVABorrowReleasesTotal,
		Help: "Variant targets lowered to give borrowed Kueue capacity back, one per release record, by cause: " +
			"placement-timeout (a borrowed replica did not place in time).",
	}, withInstance(constants.LabelNamespace, constants.LabelModelName, constants.LabelCause))
	kueueBorrowDrift = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAKueueBorrowDriftGPUs,
		Help: "Per Kueue ClusterQueue in a cohort, the GPUs by which the borrowed figure the scaling manager " +
			"computes (usage past nominal) differs from Kueue's status.flavorsReservation borrowed figure. " +
			"0 while they agree; above 0, Kueue's accounting is not what borrowing assumes.",
	}, withInstance(constants.LabelClusterQueue))
	kueueNeverRead = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAKueueNeverRead,
		Help: "1 for a quota entry that reads Kueue, leaves some accelerator type unlimited (-1) in its static " +
			"caps, and has never read Kueue successfully: nothing bounds that type. Absent otherwise.",
	}, withInstance(constants.LabelLimiterName))
	kueueVersionKnown = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAKueueControllerVersionKnown,
		Help: "1 when the Kueue controller's Deployment was read and its image names a version, 0 when not " +
			"(a digest-only image, or a Deployment that cannot be read). While 0, Kueue borrowing is withheld " +
			"(kueue-version-unknown) or frozen (kueue-controller-down).",
	}, withInstance())
	kueueCooldown = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAKueueBorrowCooldownSeconds,
		Help: "The borrowCooldown in force, in seconds, while a quota entry counts Kueue borrowing. " +
			"The lender-waiting alert compares wva_kueue_lender_waiting_seconds against it.",
	}, withInstance())
	kueueBorrowedGPUs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: constants.WVAKueueBorrowedGPUs,
		Help: "GPUs the namespaces this controller governs hold past their guaranteed figures, per Kueue " +
			"cohort and flavor, as the last cycle counted them. Absent where nothing is borrowed.",
	}, withInstance(constants.LabelCohort, constants.LabelFlavor))
	for name, c := range map[string]prometheus.Collector{
		constants.WVAKueueControllerVersionKnown: kueueVersionKnown,
		constants.WVAKueueBorrowCooldownSeconds:  kueueCooldown,
		constants.WVAKueueBorrowedGPUs:           kueueBorrowedGPUs,
		constants.WVAKueueBorrowDriftGPUs:        kueueBorrowDrift,
		constants.WVAKueueNeverRead:              kueueNeverRead,
		constants.WVAQuotaLimitGPUs:              quotaLimitGPUs,
		constants.WVAQuotaBorrowWithheld:         quotaBorrowWithheld,
		constants.WVAKueueUnplacedPods:           kueueUnplacedPods,
		constants.WVAKueuePreemptedPodsTotal:     kueuePreemptedPods,
		constants.WVAKueueLenderWaitingSeconds:   kueueLenderWaiting,
		constants.WVABorrowReleasesTotal:         borrowReleases,
	} {
		if err := registry.Register(c); err != nil {
			return fmt.Errorf("failed to register %s: %w", name, err)
		}
	}
	return nil
}

// QuotaBudget is one namespace-scope quota entry's figures for one namespace
// and accelerator, as the cycle used them.
type QuotaBudget struct {
	Limiter, Namespace, Accelerator string
	// Guaranteed below 0 is unlimited and publishes no series.
	Guaranteed int
	Borrowable int
	// Reason is why Borrowable is 0, for an entry that reads borrowing.
	Reason string
}

// seriesSet is the series a Publish call set on some gauges, so the next call
// can delete only those that are gone. publish sets every current series
// before it deletes any, so a scrape never lands on an empty set.
type seriesSet struct {
	mu        sync.Mutex
	published map[*prometheus.GaugeVec]map[string]prometheus.Labels
}

// publish makes the series of the gauges in vecs exactly those fill sets.
func (s *seriesSet) publish(vecs []*prometheus.GaugeVec, fill func(set func(*prometheus.GaugeVec, prometheus.Labels, float64))) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := map[*prometheus.GaugeVec]map[string]prometheus.Labels{}
	fill(func(g *prometheus.GaugeVec, l prometheus.Labels, v float64) {
		if controllerInstance != "" {
			l[constants.LabelControllerInstance] = controllerInstance
		}
		g.With(l).Set(v)
		if current[g] == nil {
			current[g] = map[string]prometheus.Labels{}
		}
		current[g][seriesKey(l)] = l
	})
	for _, g := range vecs {
		for k, l := range s.published[g] {
			if _, ok := current[g][k]; !ok {
				g.Delete(l)
			}
		}
	}
	s.published = current
}

var (
	quotaBudgetSeries      seriesSet
	kueueUnplacedSeries    seriesSet
	kueueLenderWaitSeries  seriesSet
	kueueBorrowDriftSeries seriesSet
	kueueNeverReadSeries   seriesSet
)

// PublishKueueBorrowDrift makes wva_kueue_borrow_drift_gpus exactly these
// ClusterQueues, keyed by name. Call it after each successful Kueue read.
func PublishKueueBorrowDrift(byQueue map[string]int) {
	if kueueBorrowDrift == nil {
		return
	}
	kueueBorrowDriftSeries.publish([]*prometheus.GaugeVec{kueueBorrowDrift},
		func(set func(*prometheus.GaugeVec, prometheus.Labels, float64)) {
			for queue, gpus := range byQueue {
				set(kueueBorrowDrift, prometheus.Labels{constants.LabelClusterQueue: queue}, float64(gpus))
			}
		})
}

var (
	kueueCooldownSeries     seriesSet
	kueueBorrowedGPUsSeries seriesSet
)

// PublishKueueVersionKnown sets wva_kueue_controller_version_known. Call it on
// each Kueue read that checks the controller.
func PublishKueueVersionKnown(known bool) {
	if kueueVersionKnown == nil {
		return
	}
	v := 0.0
	if known {
		v = 1
	}
	l := prometheus.Labels{}
	if controllerInstance != "" {
		l[constants.LabelControllerInstance] = controllerInstance
	}
	kueueVersionKnown.With(l).Set(v)
}

// PublishKueueBorrowCooldown sets wva_kueue_borrow_cooldown_seconds to the
// cooldown in force while borrowing is counted, and removes it when on is
// false.
func PublishKueueBorrowCooldown(seconds float64, on bool) {
	if kueueCooldown == nil {
		return
	}
	kueueCooldownSeries.publish([]*prometheus.GaugeVec{kueueCooldown},
		func(set func(*prometheus.GaugeVec, prometheus.Labels, float64)) {
			if on {
				set(kueueCooldown, prometheus.Labels{}, seconds)
			}
		})
}

// BorrowedGPUs is what this controller's namespaces hold past their guaranteed
// figures in one cohort and flavor.
type BorrowedGPUs struct {
	Cohort, Flavor string
	GPUs           int
}

// PublishKueueBorrowedGPUs makes wva_kueue_borrowed_gpus exactly these cohorts
// and flavors. Call it every cycle, with nil when nothing is borrowed.
func PublishKueueBorrowedGPUs(held []BorrowedGPUs) {
	if kueueBorrowedGPUs == nil {
		return
	}
	kueueBorrowedGPUsSeries.publish([]*prometheus.GaugeVec{kueueBorrowedGPUs},
		func(set func(*prometheus.GaugeVec, prometheus.Labels, float64)) {
			for _, b := range held {
				if b.GPUs <= 0 {
					continue
				}
				set(kueueBorrowedGPUs, prometheus.Labels{constants.LabelCohort: b.Cohort,
					constants.LabelFlavor: b.Flavor}, float64(b.GPUs))
			}
		})
}

// PublishKueueNeverRead makes wva_kueue_never_read exactly these limiter
// entries. Call it every cycle, with nil when none is never read.
func PublishKueueNeverRead(limiters []string) {
	if kueueNeverRead == nil {
		return
	}
	kueueNeverReadSeries.publish([]*prometheus.GaugeVec{kueueNeverRead},
		func(set func(*prometheus.GaugeVec, prometheus.Labels, float64)) {
			for _, name := range limiters {
				set(kueueNeverRead, prometheus.Labels{constants.LabelLimiterName: name}, 1)
			}
		})
}

// PublishQuotaBudgets makes wva_quota_limit_gpus and wva_quota_borrow_withheld
// exactly this cycle's budgets: a namespace that left, a borrowable figure
// that is no longer 0 and a reason that changed all lose their old series.
// Call it every cycle, with nil when no limiter is in force.
func PublishQuotaBudgets(budgets []QuotaBudget) {
	if quotaLimitGPUs == nil {
		return
	}
	quotaBudgetSeries.publish([]*prometheus.GaugeVec{quotaLimitGPUs, quotaBorrowWithheld},
		func(set func(*prometheus.GaugeVec, prometheus.Labels, float64)) {
			for _, b := range budgets {
				if b.Guaranteed < 0 {
					continue
				}
				tier := func(t string) prometheus.Labels {
					return prometheus.Labels{constants.LabelLimiterName: b.Limiter, constants.LabelNamespace: b.Namespace,
						constants.LabelAccelerator: b.Accelerator, constants.LabelTier: t}
				}
				set(quotaLimitGPUs, tier(constants.QuotaTierGuaranteed), float64(b.Guaranteed))
				set(quotaLimitGPUs, tier(constants.QuotaTierBorrowable), float64(b.Borrowable))
				if b.Reason != "" && b.Borrowable == 0 {
					set(quotaBorrowWithheld, prometheus.Labels{constants.LabelLimiterName: b.Limiter,
						constants.LabelNamespace: b.Namespace, constants.LabelAccelerator: b.Accelerator,
						constants.LabelReason: b.Reason}, 1)
				}
			}
		})
}

// UnplacedVariant is one variant's pods unplaced past the placement timeout.
type UnplacedVariant struct {
	Namespace, Variant string
	Pods               int
}

// PublishKueueUnplacedPods makes wva_kueue_unplaced_pods exactly these
// variants. Call it every cycle, with nil when no borrowing is counted.
func PublishKueueUnplacedPods(variants []UnplacedVariant) {
	if kueueUnplacedPods == nil {
		return
	}
	kueueUnplacedSeries.publish([]*prometheus.GaugeVec{kueueUnplacedPods},
		func(set func(*prometheus.GaugeVec, prometheus.Labels, float64)) {
			for _, v := range variants {
				if v.Pods <= 0 {
					continue
				}
				set(kueueUnplacedPods, prometheus.Labels{constants.LabelNamespace: v.Namespace,
					constants.LabelVariantName: v.Variant}, float64(v.Pods))
			}
		})
}

// WaitingLender is one lender ClusterQueue waiting while its cohort is
// borrowed from.
type WaitingLender struct {
	Cohort, ClusterQueue string
	Seconds              float64
}

// PublishKueueLenderWaiting makes wva_kueue_lender_waiting_seconds exactly
// these lenders. Call it every cycle, with nil when no borrowing is counted.
func PublishKueueLenderWaiting(lenders []WaitingLender) {
	if kueueLenderWaiting == nil {
		return
	}
	kueueLenderWaitSeries.publish([]*prometheus.GaugeVec{kueueLenderWaiting},
		func(set func(*prometheus.GaugeVec, prometheus.Labels, float64)) {
			for _, l := range lenders {
				set(kueueLenderWaiting, prometheus.Labels{constants.LabelCohort: l.Cohort,
					constants.LabelClusterQueue: l.ClusterQueue}, l.Seconds)
			}
		})
}

// CountBorrowRelease counts one release record of borrowed capacity.
func CountBorrowRelease(namespace, modelID, cause string) {
	if borrowReleases == nil {
		return
	}
	l := prometheus.Labels{constants.LabelNamespace: namespace, constants.LabelModelName: modelID, constants.LabelCause: cause}
	if controllerInstance != "" {
		l[constants.LabelControllerInstance] = controllerInstance
	}
	borrowReleases.With(l).Inc()
}

// CountKueuePreemptedPods counts n preempted Workloads of a model.
func CountKueuePreemptedPods(namespace, modelName string, n int) {
	if kueuePreemptedPods == nil || n <= 0 {
		return
	}
	l := prometheus.Labels{constants.LabelNamespace: namespace, constants.LabelModelName: modelName}
	if controllerInstance != "" {
		l[constants.LabelControllerInstance] = controllerInstance
	}
	kueuePreemptedPods.With(l).Add(float64(n))
}
