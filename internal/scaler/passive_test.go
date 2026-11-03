package scaler_test

import (
	"context"
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	pb "github.com/kedacore/keda/v2/pkg/scalers/externalscaler"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/scaler"
)

// passiveModels marks the listed models passive.
type passiveModels map[string]bool

func (p passiveModels) Model(_, modelID string) (bool, string) { return p[modelID], "test" }

const passiveModel = "acme/chat"

// ourTrigger is this controller's trigger, named so a formula can refer to it.
func ourTrigger() kedav1alpha1.ScaleTriggers {
	return kedav1alpha1.ScaleTriggers{Type: "external-push", Name: "wva",
		Metadata: map[string]string{registry.ModelIDKey: passiveModel}}
}

// promTrigger is a KEDA trigger of someone else's.
func promTrigger() kedav1alpha1.ScaleTriggers {
	return kedav1alpha1.ScaleTriggers{Type: "prometheus", Name: "prom",
		Metadata: map[string]string{"query": "sum(rate(x[1m]))", "threshold": "5"}}
}

var _ = Describe("Passive mode through the external scaler", func() {
	var (
		ctx   context.Context
		store *decision.Store
		c     client.Client
		reg   *registry.Registry
	)
	meta := map[string]string{registry.ModelIDKey: passiveModel}

	so := func(min, max *int32, formula string, triggers ...kedav1alpha1.ScaleTriggers) *kedav1alpha1.ScaledObject {
		o := &kedav1alpha1.ScaledObject{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "chat-decode"},
			Spec: kedav1alpha1.ScaledObjectSpec{
				ScaleTargetRef:  &kedav1alpha1.ScaleTarget{Name: "chat-decode-deploy"},
				MinReplicaCount: min, MaxReplicaCount: max,
				Triggers: triggers,
			},
		}
		if formula != "" {
			o.Spec.Advanced = &kedav1alpha1.AdvancedConfig{ScalingModifiers: kedav1alpha1.ScalingModifiers{Formula: formula, Target: "1"}}
		}
		return o
	}

	newHandler := func(passive scaler.PassiveModels, objs ...client.Object) *scaler.Handler {
		s := runtime.NewScheme()
		Expect(kedav1alpha1.AddToScheme(s)).To(Succeed())
		Expect(appsv1.AddToScheme(s)).To(Succeed())
		c = fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
		reg = registry.New(0)
		return scaler.NewHandler(c, store, reg).WithPassive(passive)
	}

	getMetrics := func(h *scaler.Handler) (int64, error) {
		resp, err := h.GetMetrics(ctx, &pb.GetMetricsRequest{ScaledObjectRef: ref("chat-decode", meta), MetricName: scaler.MetricName})
		if err != nil {
			return 0, err
		}
		return resp.MetricValues[0].MetricValue, nil
	}
	isActive := func(h *scaler.Handler) bool {
		resp, err := h.IsActive(ctx, ref("chat-decode", meta))
		Expect(err).NotTo(HaveOccurred())
		return resp.Result
	}
	setReplicas := func(n int32) {
		var d appsv1.Deployment
		Expect(c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "chat-decode-deploy"}, &d)).To(Succeed())
		d.Spec.Replicas = &n
		Expect(c.Update(ctx, &d)).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		store = decision.NewStore()
	})

	Context("hold: our trigger alone", func() {
		It("answers the live spec.replicas whatever the decision, and follows a hand scale", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(nil, nil, "", ourTrigger()), deployment("chat-decode-deploy", ptr(3)))
			for _, d := range []int32{9, 0, 1} {
				store.Set(testNamespace, "chat-decode-deploy", d)
				Expect(getMetrics(h)).To(Equal(int64(3)), "decision %d", d)
				Expect(isActive(h)).To(BeTrue(), "decision %d", d)
			}
			setReplicas(5) // kubectl scale
			Expect(getMetrics(h)).To(Equal(int64(5)))
		})

		It("clamps the held count to the ScaledObject's bounds", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(ptr(2), ptr(4), "", ourTrigger()), deployment("chat-decode-deploy", ptr(7)))
			Expect(getMetrics(h)).To(Equal(int64(4)))
			setReplicas(1)
			Expect(getMetrics(h)).To(Equal(int64(2)))
		})

		It("keeps a target at zero asleep, even with a decision to wake it", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(ptr(0), nil, "", ourTrigger()), deployment("chat-decode-deploy", ptr(0)))
			store.Set(testNamespace, "chat-decode-deploy", 4)
			Expect(getMetrics(h)).To(Equal(int64(0)))
			Expect(isActive(h)).To(BeFalse())
		})

		It("never pushes an activation change on StreamIsActive when the decision changes", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(ptr(0), nil, "", ourTrigger()), deployment("chat-decode-deploy", ptr(0)))
			sctx, cancel := context.WithCancel(ctx)
			defer cancel()
			stream := newFakeStream(sctx)
			done := make(chan error, 1)
			go func() { done <- h.StreamIsActive(ref("chat-decode", meta), stream) }()
			Expect(stream.nextPush()).To(BeFalse())
			store.Set(testNamespace, "chat-decode-deploy", 3) // a scale-from-zero wake
			stream.expectNoPush()
			cancel()
			Eventually(done).Should(Receive(BeNil()))
		})

		It("answers the decision for an active model (control)", func() {
			h := newHandler(passiveModels{}, so(nil, nil, "", ourTrigger()), deployment("chat-decode-deploy", ptr(3)))
			store.Set(testNamespace, "chat-decode-deploy", 9)
			Expect(getMetrics(h)).To(Equal(int64(9)))
		})
	})

	Context("observe: our trigger beside another", func() {
		It("answers 0 and inactive whatever the decision", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(nil, nil, "", ourTrigger(), promTrigger()), deployment("chat-decode-deploy", ptr(3)))
			for _, d := range []int32{9, 0} {
				store.Set(testNamespace, "chat-decode-deploy", d)
				Expect(getMetrics(h)).To(Equal(int64(0)), "decision %d", d)
				Expect(isActive(h)).To(BeFalse(), "decision %d", d)
			}
		})

		It("never errors, even with the scale target unreadable", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(nil, nil, "", ourTrigger(), promTrigger()))
			Expect(getMetrics(h)).To(Equal(int64(0)))
			Expect(isActive(h)).To(BeFalse())
		})

		It("answers 0 before the leader lease is held, where an active model is refused", func() {
			never := make(chan struct{})
			h := newHandler(passiveModels{passiveModel: true}, so(nil, nil, "", ourTrigger(), promTrigger()), deployment("chat-decode-deploy", ptr(3)))
			h.WithElected(never)
			Expect(getMetrics(h)).To(Equal(int64(0)))
			Expect(isActive(h)).To(BeFalse())

			active := newHandler(passiveModels{}, so(nil, nil, "", ourTrigger(), promTrigger()), deployment("chat-decode-deploy", ptr(3)))
			active.WithElected(never)
			_, err := getMetrics(active)
			Expect(err).To(HaveOccurred())
		})

		It("answers 0 too where a scalingModifiers formula names our trigger (observe-unsafe)", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(nil, nil, "max(prom, wva)", ourTrigger(), promTrigger()), deployment("chat-decode-deploy", ptr(3)))
			store.Set(testNamespace, "chat-decode-deploy", 9)
			Expect(getMetrics(h)).To(Equal(int64(0)))
		})
	})

	// The KEDA/HPA side, applied to the scaler's answers: the HPA's
	// AverageValue rule with target 1 (inside a 10% tolerance it keeps the
	// current count, else ceil(value)), the highest proposal over metrics,
	// and KEDA's activation (inactive at minReplicaCount 0 parks the target,
	// active at zero wakes it). Status replicas are taken equal to spec.
	Describe("an HPA and KEDA driven by the scaler's answers", func() {
		hpaStep := func(h *scaler.Handler, foreign int64, min int32) int32 {
			var d appsv1.Deployment
			Expect(c.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: "chat-decode-deploy"}, &d)).To(Succeed())
			cur := *d.Spec.Replicas
			value, err := getMetrics(h)
			Expect(err).NotTo(HaveOccurred(), "an erroring metric holds every scale-down")
			active := isActive(h) || foreign > 0
			next := cur
			switch {
			case cur == 0 && active:
				next = max(min, 1)
			case cur > 0 && !active && min == 0:
				next = 0
			case cur > 0:
				ours := int64(cur)
				if math.Abs(float64(value)/float64(cur)-1) > 0.1 {
					ours = value
				}
				next = int32(max(ours, foreign, int64(min), 1))
			}
			setReplicas(next)
			return next
		}
		decisions := []int32{0, 1, 12, 3, 0, 7, 20, 2, 0, 5}

		It("hold: the replica count never moves while the decision swings", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(ptr(0), nil, "", ourTrigger()), deployment("chat-decode-deploy", ptr(4)))
			for i := 0; i < 40; i++ {
				store.Set(testNamespace, "chat-decode-deploy", decisions[i%len(decisions)])
				Expect(hpaStep(h, 0, 0)).To(Equal(int32(4)), "step %d", i)
			}
		})

		It("hold: a target at zero stays at zero while the decision asks to wake it", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(ptr(0), nil, "", ourTrigger()), deployment("chat-decode-deploy", ptr(0)))
			for i := 0; i < 20; i++ {
				store.Set(testNamespace, "chat-decode-deploy", decisions[i%len(decisions)]+1)
				Expect(hpaStep(h, 0, 0)).To(Equal(int32(0)), "step %d", i)
			}
		})

		It("observe: the count follows the other trigger only", func() {
			h := newHandler(passiveModels{passiveModel: true}, so(ptr(0), nil, "", ourTrigger(), promTrigger()), deployment("chat-decode-deploy", ptr(4)))
			foreign := []int64{4, 6, 6, 2, 9, 3}
			for i := 0; i < 30; i++ {
				store.Set(testNamespace, "chat-decode-deploy", decisions[i%len(decisions)])
				f := foreign[i%len(foreign)]
				Expect(hpaStep(h, f, 0)).To(Equal(int32(f)), "step %d", i)
			}
		})

		It("active (control): the count follows the decision", func() {
			h := newHandler(passiveModels{}, so(ptr(0), nil, "", ourTrigger()), deployment("chat-decode-deploy", ptr(4)))
			store.Set(testNamespace, "chat-decode-deploy", 12)
			Expect(hpaStep(h, 0, 0)).To(Equal(int32(12)))
		})
	})
})
