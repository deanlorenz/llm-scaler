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
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/logging"
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
	// line it emitted.
	//
	// shippedVerbosity is logging.DEFAULT, not a literal: an earlier revision
	// of this file hardcoded 1 and called it "what the shipped deployment
	// prints", which was wrong by one -- cmd/main.go defaults -v to
	// logging.DEFAULT and that is 2. The difference mattered. It made the
	// control for "this line would vanish from production at V(2)" pass for
	// the wrong reason: at the real default a V(2) line prints. Anchored to
	// the constant, the way analyzer_log_test.go does, the threshold tracks
	// the deployment if the constant ever moves.
	captured := func(verbosity int, fn func(ctx context.Context)) []string {
		var lines []string
		lg := funcr.NewJSON(func(obj string) { lines = append(lines, obj) },
			funcr.Options{Verbosity: verbosity})
		fn(ctrllog.IntoContext(context.Background(), lg))
		return lines
	}

	// logged requires the message AND every field to be on the SAME line.
	// Matching them anywhere in the captured output let a field assertion be
	// satisfied by a different log line than the one named, which is exactly
	// the kind of pass this file exists to refuse.
	logged := func(lines []string, msg string, fields ...string) bool {
	line:
		for _, l := range lines {
			if !strings.Contains(l, msg) {
				continue
			}
			for _, f := range fields {
				if !strings.Contains(l, f) {
					continue line
				}
			}
			return true
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
		lines := captured(logging.DEFAULT, func(ctx context.Context) {
			_, err := h.GetMetricSpec(ctx, ref("chat-so", nil))
			Expect(err).NotTo(HaveOccurred())
		})

		Expect(logged(lines, "external scaler GetMetricSpec")).To(BeTrue(),
			"without this line, 'KEDA never asked' and 'KEDA asked and we answered' are "+
				"indistinguishable from WVA's side -- and that is the difference between "+
				"an HPA wired to WVA and one silently scaling on CPU")
		Expect(logged(lines, "external scaler GetMetricSpec", "chat-so", scaler.MetricName)).
			To(BeTrue(), "on that same line it must name the ScaledObject it answered for "+
				"and the metric it advertised")
	})

	It("records it at a level the shipped deployment prints, and not louder", func() {
		// Pinned from BOTH sides, because both directions are regressions.
		//
		// Too quiet and the diagnostic nobody can read is the problem it was
		// written to solve. Too loud -- an unconditional Info, which this line
		// was at first -- and it fires on every poll for every ScaledObject at
		// a level an operator cannot turn down: KEDA's GetMetricSpecForScaling
		// is a live gRPC call with no caching above it, and the samples this
		// repo ships set pollingInterval: 5.
		h := newHandler("chat-so")

		atShipped := captured(logging.DEFAULT, func(ctx context.Context) {
			_, err := h.GetMetricSpec(ctx, ref("chat-so", nil))
			Expect(err).NotTo(HaveOccurred())
		})
		Expect(logged(atShipped, "external scaler GetMetricSpec")).To(BeTrue(),
			"the deployment runs at -v=logging.DEFAULT, so a line deeper than that "+
				"never reaches an operator and the two CI investigations this exists "+
				"for would go the same way again")

		silent := captured(0, func(ctx context.Context) {
			_, err := h.GetMetricSpec(ctx, ref("chat-so", nil))
			Expect(err).NotTo(HaveOccurred())
		})
		Expect(logged(silent, "external scaler GetMetricSpec")).To(BeFalse(),
			"it must stay V()-gated. Unconditional, this is one line per ScaledObject "+
				"per poll forever -- roughly 240 a minute across 20 ScaledObjects at the "+
				"shipped pollingInterval of 5 -- which is the spam that made the first "+
				"version of this change wrong")
	})

	It("records the value it handed KEDA, and whether a decision existed", func() {
		h := newHandler("chat-so")
		// Keyed on the resolved SCALE TARGET, not the ScaledObject: decisionFor
		// goes through targetName first. Seeded under the ScaledObject's own
		// name the lookup misses, GetMetrics reports decided=false, and the
		// spec below is what caught that.
		store.Set(testNamespace, "chat-so-target", 3)

		lines := captured(logging.DEFAULT, func(ctx context.Context) {
			_, err := h.GetMetrics(ctx, &pb.GetMetricsRequest{
				ScaledObjectRef: ref("chat-so", nil), MetricName: scaler.MetricName,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		Expect(logged(lines, "external scaler GetMetrics", `"value":3`, `"decided":true`)).To(BeTrue(),
			"and 0 from 'no decision yet' must be distinguishable from a decided 0: "+
				"they are the same number on the wire and mean opposite things")
	})

	It("records its own refusal when it does not hold the lease", func() {
		// WVA used to refuse here in silence: KEDA recorded the refusal and WVA
		// did not, so the component an operator reads first had no trace of its
		// own answer -- and a refusal at the wrong moment is what leaves an HPA
		// unwired for the life of the ScaledObject.
		h := notLeader("chat-so")

		lines := captured(logging.DEFAULT, func(ctx context.Context) {
			_, err := h.GetMetrics(ctx, &pb.GetMetricsRequest{
				ScaledObjectRef: ref("chat-so", nil), MetricName: scaler.MetricName,
			})
			Expect(err).To(HaveOccurred())
		})

		// Named, not just "not the leader": both handlers refuse with that
		// phrase, so matching it alone does not pin which one spoke.
		Expect(logged(lines, "external scaler GetMetrics refused", "chat-so")).To(BeTrue(),
			"a refusal that leaves no trace is how this failure mode stayed invisible")
	})

	It("says when NO decision existed, which is the whole reason for the flag", func() {
		// The untested half. 0 because WVA decided 0, and 0 because WVA has not
		// decided yet and KEDA should hold at minReplicaCount, are the same
		// number on the wire and opposite in meaning -- which is what `decided`
		// is for. Nothing seeded here, so the lookup misses.
		//
		// Without this spec a regression that hardcoded `"decided", true` would
		// pass every other assertion in this file.
		h := newHandler("chat-so")

		lines := captured(logging.DEFAULT, func(ctx context.Context) {
			_, err := h.GetMetrics(ctx, &pb.GetMetricsRequest{
				ScaledObjectRef: ref("chat-so", nil), MetricName: scaler.MetricName,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		Expect(logged(lines, "external scaler GetMetrics", `"value":0`, `"decided":false`)).
			To(BeTrue(), "a 0 with no decision behind it must be distinguishable from a "+
				"decided 0, on the same line, or the field says nothing")
	})

	It("records its own failure when the lookup errors", func() {
		// No ScaledObject at all, so targetName cannot resolve one and desired()
		// returns an error. The error reaches KEDA either way; this line is what
		// says WVA is where it came from.
		s := runtime.NewScheme()
		Expect(kedav1alpha1.AddToScheme(s)).To(Succeed())
		h := scaler.NewHandler(fake.NewClientBuilder().WithScheme(s).Build(),
			store, registry.New(0))

		lines := captured(logging.DEFAULT, func(ctx context.Context) {
			_, err := h.GetMetrics(ctx, &pb.GetMetricsRequest{
				ScaledObjectRef: ref("missing-so", nil), MetricName: scaler.MetricName,
			})
			Expect(err).To(HaveOccurred())
		})

		Expect(logged(lines, "external scaler GetMetrics failed", `"err"`)).To(BeTrue(),
			"a failure that leaves no trace in WVA's log sends the next investigation "+
				"to KEDA's log to find out what WVA answered")
	})

	It("records IsActive's refusal too, not just GetMetrics'", func() {
		// The same blind spot, one handler over. Logging GetMetrics' refusal and
		// not IsActive's just moved it: both refuse identically, and IsActive is
		// the call KEDA makes most often.
		h := notLeader("chat-so")

		lines := captured(logging.DEFAULT, func(ctx context.Context) {
			_, err := h.IsActive(ctx, ref("chat-so", nil))
			Expect(err).To(HaveOccurred())
		})

		Expect(logged(lines, "external scaler IsActive refused", "chat-so")).To(BeTrue(),
			"a standby replica that refuses in silence looks identical to one that "+
				"was never asked")
	})

	It("records IsActive's own failure when the lookup errors", func() {
		// The gap the commit before this one left: it added this log line and
		// no spec, so deleting the whole statement left the suite green. Its
		// own message said "logging one sibling and not the other just moved
		// the blind spot" -- and then did exactly that with the specs.
		s := runtime.NewScheme()
		Expect(kedav1alpha1.AddToScheme(s)).To(Succeed())
		h := scaler.NewHandler(fake.NewClientBuilder().WithScheme(s).Build(),
			store, registry.New(0))

		lines := captured(logging.DEFAULT, func(ctx context.Context) {
			_, err := h.IsActive(ctx, ref("missing-so", nil))
			Expect(err).To(HaveOccurred())
		})

		Expect(logged(lines, "external scaler IsActive failed", `"err"`)).To(BeTrue(),
			"the error reaches KEDA either way; this line is what says WVA is where "+
				"it came from")
	})

	// EVERY new line, gated. The previous revision guarded only GetMetricSpec,
	// so three of the others could be made unconditional Info and stay green --
	// on a PR that exists because that exact regression shipped once. A table,
	// so a line added later without a level is a failure rather than an
	// omission nobody notices.
	DescribeTable("each handshake line is visible at the shipped verbosity and gated below it",
		func(drive func(h *scaler.Handler, ctx context.Context), msg string) {
			h := notLeader("chat-so")
			if !strings.Contains(msg, "refused") {
				h = newHandler("chat-so")
			}
			atShipped := captured(logging.DEFAULT, func(ctx context.Context) { drive(h, ctx) })
			Expect(logged(atShipped, msg)).To(BeTrue(),
				"a diagnostic the shipped deployment does not print is the problem "+
					"this file exists to solve")

			silent := captured(0, func(ctx context.Context) { drive(h, ctx) })
			Expect(logged(silent, msg)).To(BeFalse(),
				"and it must stay V()-gated: every one of these fires per poll, per "+
					"ScaledObject, so unconditional is spam at a level an operator "+
					"cannot turn down")
		},
		Entry("GetMetricSpec", func(h *scaler.Handler, ctx context.Context) {
			_, _ = h.GetMetricSpec(ctx, ref("chat-so", nil))
		}, "external scaler GetMetricSpec"),
		Entry("GetMetrics' value", func(h *scaler.Handler, ctx context.Context) {
			_, _ = h.GetMetrics(ctx, &pb.GetMetricsRequest{
				ScaledObjectRef: ref("chat-so", nil), MetricName: scaler.MetricName,
			})
		}, "external scaler GetMetrics"),
		Entry("GetMetrics' refusal", func(h *scaler.Handler, ctx context.Context) {
			_, _ = h.GetMetrics(ctx, &pb.GetMetricsRequest{
				ScaledObjectRef: ref("chat-so", nil), MetricName: scaler.MetricName,
			})
		}, "external scaler GetMetrics refused"),
		Entry("IsActive", func(h *scaler.Handler, ctx context.Context) {
			_, _ = h.IsActive(ctx, ref("chat-so", nil))
		}, "external scaler IsActive"),
		Entry("IsActive's refusal", func(h *scaler.Handler, ctx context.Context) {
			_, _ = h.IsActive(ctx, ref("chat-so", nil))
		}, "external scaler IsActive refused"),
	)

	It("still answers, and does not panic, for a nil ref", func() {
		h := newHandler("chat-so")
		captured(logging.DEFAULT, func(ctx context.Context) {
			Expect(func() { _, _ = h.GetMetricSpec(ctx, nil) }).NotTo(Panic())
		})
	})
})
