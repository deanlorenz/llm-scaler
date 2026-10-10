package scaler_test

import (
	"context"
	"strings"

	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	pb "github.com/kedacore/keda/v2/pkg/scalers/externalscaler"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/scaler"
)

// What the KEDA handshake leaves behind in WVA's own log.
//
// These exist because the opposite was true, and it cost two CI
// investigations. GetMetricSpec decides what metric KEDA puts on the HPA: get
// no answer and the HPA is built with an empty metrics list, which Kubernetes
// defaults to Resource/cpu, so the workload scales on CPU while the
// ScaledObject reports Ready=True and WVA goes on publishing the correct
// replica count every cycle. Nothing in WVA's output separated that from
// working, because GetMetricSpec logged NOTHING while IsActive logged every
// five seconds -- the cheap poll was observable and the decisive handshake was
// not.
//
// Asserting on log output is unusual, and deliberate: here the log line IS the
// feature. A spec that checked only the returned MetricSpecs would have passed
// throughout the period the gap existed.
var _ = Describe("the KEDA handshake is visible in WVA's log", func() {
	var store *decision.Store

	BeforeEach(func() { store = decision.NewStore() })

	// captured runs fn with a logger at the given verbosity and returns every
	// line it emitted. Verbosity 1 is what the shipped deployment prints: it
	// passes no -v, so the effective level is logging.DEFAULT.
	captured := func(verbosity int, fn func(ctx context.Context)) []string {
		var lines []string
		lg := funcr.NewJSON(func(obj string) { lines = append(lines, obj) },
			funcr.Options{Verbosity: verbosity})
		fn(ctrllog.IntoContext(context.Background(), lg))
		return lines
	}

	logged := func(lines []string, substr string) bool {
		for _, l := range lines {
			if strings.Contains(l, substr) {
				return true
			}
		}
		return false
	}

	// so builds a ScaledObject the handler can resolve. Local rather than
	// shared with externalscaler_test.go, whose builder is a closure inside its
	// own Describe.
	so := func(name string) *kedav1alpha1.ScaledObject {
		return &kedav1alpha1.ScaledObject{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: name},
			Spec: kedav1alpha1.ScaledObjectSpec{
				ScaleTargetRef: &kedav1alpha1.ScaleTarget{Name: name + "-target"},
			},
		}
	}

	newHandler := func(name string) *scaler.Handler {
		s := runtime.NewScheme()
		Expect(kedav1alpha1.AddToScheme(s)).To(Succeed())
		c := fake.NewClientBuilder().WithScheme(s).WithObjects(so(name)).Build()
		// A nil elected channel reads as always-elected (see leadership_test).
		return scaler.NewHandler(c, store, registry.New(0))
	}

	// notLeader is a handler whose lease channel is open, i.e. never closed, so
	// every call that needs the leader refuses.
	notLeader := func(name string) *scaler.Handler {
		s := runtime.NewScheme()
		Expect(kedav1alpha1.AddToScheme(s)).To(Succeed())
		c := fake.NewClientBuilder().WithScheme(s).WithObjects(so(name)).Build()
		return scaler.NewHandler(c, store, registry.New(0)).WithElected(make(chan struct{}))
	}

	It("records GetMetricSpec, the call that decides what metric the HPA carries", func() {
		h := newHandler("chat-so")
		lines := captured(1, func(ctx context.Context) {
			_, err := h.GetMetricSpec(ctx, ref("chat-so", nil))
			Expect(err).NotTo(HaveOccurred())
		})

		Expect(logged(lines, "external scaler GetMetricSpec")).To(BeTrue(),
			"without this line, 'KEDA never asked' and 'KEDA asked and we answered' are "+
				"indistinguishable from WVA's side -- and that is the difference between "+
				"an HPA wired to WVA and one silently scaling on CPU")
		Expect(logged(lines, "chat-so")).To(BeTrue(), "it must name the ScaledObject it answered for")
		Expect(logged(lines, scaler.MetricName)).To(BeTrue(), "and the metric it advertised")
	})

	It("records it at a level the shipped deployment actually prints", func() {
		// A diagnostic nobody can read is the problem it was written to solve.
		// The deployment passes no -v, so anything deeper than V(1) never
		// reaches an operator -- the same argument the analyzer's
		// replica-capacity-decision line already makes.
		h := newHandler("chat-so")
		lines := captured(0, func(ctx context.Context) {
			_, err := h.GetMetricSpec(ctx, ref("chat-so", nil))
			Expect(err).NotTo(HaveOccurred())
		})

		Expect(logged(lines, "external scaler GetMetricSpec")).To(BeTrue(),
			"GetMetricSpec logs at DEFAULT, not behind a -v nobody sets. It is one line "+
				"per ScaledObject per KEDA reconcile, not per poll, so the volume argument "+
				"that justifies V(1) for IsActive does not apply to it")
	})

	It("records the value it handed KEDA, and whether a decision existed", func() {
		h := newHandler("chat-so")
		// Keyed on the resolved SCALE TARGET, not the ScaledObject: decisionFor
		// goes through targetName first. Seeded under the ScaledObject's own
		// name the lookup misses, GetMetrics reports decided=false, and the
		// spec below is what caught that.
		store.Set(testNamespace, "chat-so-target", 3)

		lines := captured(1, func(ctx context.Context) {
			_, err := h.GetMetrics(ctx, &pb.GetMetricsRequest{
				ScaledObjectRef: ref("chat-so", nil), MetricName: scaler.MetricName,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		Expect(logged(lines, "external scaler GetMetrics")).To(BeTrue(),
			"the value KEDA actuates on must be recoverable from WVA's log")
		Expect(logged(lines, `"decided":true`)).To(BeTrue(),
			"and 0 from 'no decision yet' must be distinguishable from a decided 0: "+
				"they are the same number on the wire and mean opposite things")
	})

	It("records its own refusal when it does not hold the lease", func() {
		// WVA used to refuse here in silence: KEDA recorded the refusal and WVA
		// did not, so the component an operator reads first had no trace of its
		// own answer -- and a refusal at the wrong moment is what leaves an HPA
		// unwired for the life of the ScaledObject.
		h := notLeader("chat-so")

		lines := captured(1, func(ctx context.Context) {
			_, err := h.GetMetrics(ctx, &pb.GetMetricsRequest{
				ScaledObjectRef: ref("chat-so", nil), MetricName: scaler.MetricName,
			})
			Expect(err).To(HaveOccurred())
		})

		Expect(logged(lines, "not the leader")).To(BeTrue(),
			"a refusal that leaves no trace is how this failure mode stayed invisible")
	})

	It("still answers, and does not panic, for a nil ref", func() {
		h := newHandler("chat-so")
		captured(1, func(ctx context.Context) {
			Expect(func() { _, _ = h.GetMetricSpec(ctx, nil) }).NotTo(Panic())
		})
	})
})
