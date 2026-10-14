// Package scaler implements KEDA's ExternalScaler gRPC service for WVA.
//
// WVA computes the desired replica count with its capacity model; this scaler
// delivers that decision to KEDA/HPA over the external-scaler contract, so KEDA
// actuates (no Prometheus round-trip, no prometheus-adapter). It resolves the
// scale target from the KEDA ScaledObject (namespace/name -> scaleTargetRef) and
// reads WVA's latest decision from the in-memory store the actuator feeds
// (internal/decision).
package scaler

import (
	"context"
	"time"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	pb "github.com/kedacore/keda/v2/pkg/scalers/externalscaler"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/decision"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/registry"
)

// metricTargetSize is the target KEDA is told to divide by. One is the
// whole design: WVA publishes the replica count it wants, so a target of
// 1 makes KEDA's arithmetic the identity and the HPA carries WVA's figure
// rather than a ratio. Named because it appears twice -- in the response
// and in the log line that reports the response -- and two literals for
// one value is how a status message comes to disagree with the thing it
// describes.
const metricTargetSize = 1

// MetricName is the external metric WVA exposes to KEDA/HPA. It is advertised
// with a target of 1, so HPA computes ceil(metricValue / 1) = metricValue and
// scales the target to exactly WVA's desired replica count.
const MetricName = "wva-desired-replicas"

// activationTTL bounds how long a "keep this target awake" decision is honoured
// without being refreshed. See Handler.honoursDecision.
const activationTTL = 60 * time.Second

// Handler implements pb.ExternalScalerServer.
type Handler struct {
	pb.UnimplementedExternalScalerServer
	// client MUST be an uncached reader (manager.GetAPIReader). A cached Get of a
	// ScaledObject lazily starts a cluster-wide LIST+WATCH informer for the kind
	// on first use — the watch this design exists to remove — and it would appear
	// the moment KEDA made its first call, with nothing in the code saying "watch".
	client client.Reader
	store  *decision.Store
	// registry is the set of workloads WVA manages. Every RPC registers its ref
	// there — see observe.
	registry *registry.Registry
	// now is the clock used for decision freshness; overridden in tests.
	now func() time.Time
	// elected is closed once this replica holds the leader lease; nil means it
	// always does. Only the leader runs the engines that fill the store, so
	// before it closes the store's silence means "not the leader", not "no
	// decision yet" -- see leader.
	elected <-chan struct{}
}

// NewHandler builds a Handler. A nil store falls back to decision.Default, and a
// nil reg to registry.Default.
//
// c must be uncached — see the client field.
func NewHandler(c client.Reader, store *decision.Store, reg *registry.Registry) *Handler {
	if store == nil {
		store = decision.Default
	}
	if reg == nil {
		reg = registry.Default
	}
	return &Handler{client: c, store: store, registry: reg, now: time.Now}
}

// WithElected makes the handler refuse decision-bearing calls until elected is
// closed (manager.Elected()). Nil leaves it answering as the leader, which is
// what a manager without leader election is. Returns h for chaining.
func (h *Handler) WithElected(elected <-chan struct{}) *Handler {
	h.elected = elected
	return h
}

// errNotLeader is what a replica that does not hold the lease answers with
// for anything that needs a decision. Unavailable is the retriable code: KEDA
// logs it, marks the trigger as erroring and takes no scaling action, and
// asks again on its next poll -- by which time this replica is normally the
// leader, since the only supported reason to be here is the window between
// a pod becoming Ready and the outgoing pod's lease being released.
var errNotLeader = status.Error(codes.Unavailable,
	"this WVA replica does not hold the leader lease yet; decisions are made by the leader")

// leader reports whether this replica holds the lease. An empty decision store
// on a replica that does not is not "no decision yet" -- the leader may well
// have one -- so the calls that read the store check this first and refuse
// rather than answer 0.
func (h *Handler) leader() bool {
	if h.elected == nil {
		return true
	}
	select {
	case <-h.elected:
		return true
	default:
		return false
	}
}

// observe records that KEDA has asked about this ref. It is the discovery event:
// WVA does not look for the workloads it manages, it manages the ones it is
// called about (docs/plans/engine/keda-driven-discovery.md).
//
// Called at the top of every RPC, including ones whose answer does not depend on
// the ref, because registration is the point and the answer is incidental. It
// never fails and never rejects: metadata that does not parse is still a
// registration — the engine reports the bad trigger once per cycle with the
// object's name attached, which is a far better diagnostic than a workload that
// silently never appears.
func (h *Handler) observe(ref *pb.ScaledObjectRef) {
	if ref == nil || h.registry == nil {
		return
	}
	h.registry.Observe(ref.GetNamespace(), ref.GetName(), ref.GetScalerMetadata())
}

// honoursDecision reports whether d still speaks for its target, or has gone
// stale and should be treated as "no opinion".
//
// The store is a latch: Set overwrites and nothing is ever removed, so without
// this a decision written once outlives whatever produced it. That is not
// hypothetical — a variant briefly running long enough for the saturation engine
// to publish "2" keeps isActive true forever after the target is scaled to zero,
// so the target is woken the instant KEDA asks, is never observed inactive, and
// NO engine can ever detect pending demand on it. Scale-from-zero is dead for
// that target until the process restarts.
//
// The rule is asymmetric, and the asymmetry is what makes it safe:
//
//   - "Keep this awake" (> 0) is a claim that must be kept current. Its
//     publishers normally re-publish well inside activationTTL — the
//     scale-from-zero engine every 100ms while a queue is pending, the saturation
//     engine every optimize interval (GLOBAL_OPT_INTERVAL, default 15s) — so an
//     unrefreshed positive decision means nobody is making the claim any more,
//     and the honest answer is to look at the target instead (see
//     currentlyRunning). GLOBAL_OPT_INTERVAL has only a lower bound, so a
//     deployment configuring it at or above activationTTL expires decisions
//     between publishes as a matter of course — and this repo ships one:
//     config/components/openshift/configmap-patch.yaml sets 60s, exactly the TTL.
//     The effect is benign, because the fallback then answers from the target's
//     replica count, which for a running target is still "active"; but it is the
//     normal case there, not an edge case.
//
//   - "This should be asleep" (0) never expires. It is the resting state, and
//     expiring it would flip a target that is still draining back to active,
//     flapping against the scale-down that is already in flight.
//
// Note that expiry cannot strand a cold start: as soon as KEDA acts on an
// activation the target has replicas, so the currentlyRunning fallback answers
// "active" on its own. The activation decision only has to outlive KEDA's
// reaction, not the pod's startup.
//
// Scope: this governs the 0<->1 gate only. GetMetrics deliberately has no
// freshness check (it drives HPA's scaling at >=1, where a value collapsing to 0
// between publishes would be worse), so a stale decision on a RUNNING target
// still reports its last replica count there. That predates this expiry and is
// unchanged by it — expiring the activation bit does not, on its own, let a
// target with an abandoned decision scale back down.
func (h *Handler) honoursDecision(d decision.Decision) bool {
	if d.DesiredReplicas <= 0 {
		return true
	}
	now := h.now
	if now == nil { // zero-value Handler (tests); NewHandler always sets it.
		now = time.Now
	}
	return now().Sub(d.UpdatedAt) <= activationTTL
}

// targetName resolves the scale-target name for a ScaledObjectRef, cheapest
// source first.
//
//  1. the registry, if the enricher has already resolved this entry — the steady
//     state, and the reason this path costs no API traffic at all; and only then
//  2. an uncached read of the ScaledObject, for the window between a workload's
//     first call and its first enrichment pass.
//
// Step 1 matters because step 2 is uncached by necessity: making it cached would
// serve it from a cluster-wide informer, so without the registry hop every KEDA
// poll of every workload would be a real API request.
//
// The scale target is never taken from trigger metadata. ScaledObject.spec
// .scaleTargetRef is the authority on what a ScaledObject scales, and a second
// hand-written copy of that name can disagree with it -- silently attributing a
// variant's metrics to another workload. That is not hypothetical: cloning a
// ScaledObject to add a variant used to carry the field across and point the new
// entry at the original's Deployment.
func (h *Handler) targetName(ctx context.Context, ref *pb.ScaledObjectRef) (string, error) {
	if ref == nil {
		return "", status.Error(codes.InvalidArgument, "scaledObjectRef is required")
	}
	if h.registry != nil {
		if e, ok := h.registry.Get(ref.GetNamespace(), ref.GetName()); ok && e.Target.Name != "" {
			return e.Target.Name, nil
		}
	}
	var so kedav1alpha1.ScaledObject
	nn := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	if err := h.client.Get(ctx, nn, &so); err != nil {
		return "", status.Errorf(readErrorCode(err), "getting ScaledObject %s: %v", nn, err)
	}
	if so.Spec.ScaleTargetRef == nil || so.Spec.ScaleTargetRef.Name == "" {
		return "", status.Errorf(codes.FailedPrecondition, "ScaledObject %s has no scaleTargetRef.name", nn)
	}
	return so.Spec.ScaleTargetRef.Name, nil
}

// readErrorCode maps a failed API read to the gRPC code that names its cause.
//
// KEDA shows the code on the trigger error, so it is the operator's first clue:
// NotFound means the object is gone; PermissionDenied means WVA's own RBAC (or
// its credential) is the problem, which is never "not found"; anything else --
// a timeout, throttling, a refused connection -- is transient, and Unavailable
// is the retriable code KEDA simply asks again on.
func readErrorCode(err error) codes.Code {
	switch {
	case apierrors.IsNotFound(err):
		return codes.NotFound
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return codes.PermissionDenied
	default:
		return codes.Unavailable
	}
}

// decisionFor returns WVA's latest decision for the ref and whether one exists
// yet.
func (h *Handler) decisionFor(ctx context.Context, ref *pb.ScaledObjectRef) (decision.Decision, bool, error) {
	name, err := h.targetName(ctx, ref)
	if err != nil {
		return decision.Decision{}, false, err
	}
	d, ok := h.store.Get(ref.Namespace, name)
	return d, ok, nil
}

// desired returns WVA's latest desired replicas for the ref and whether a
// decision exists yet.
func (h *Handler) desired(ctx context.Context, ref *pb.ScaledObjectRef) (int32, bool, error) {
	d, ok, err := h.decisionFor(ctx, ref)
	if err != nil || !ok {
		return 0, false, err
	}
	return d.DesiredReplicas, true, nil
}

// GetMetricSpec advertises the WVA metric with a target of 1 so HPA scales the
// target to exactly the value GetMetrics returns.
//
// The response does not depend on the ref, but the call still registers it: KEDA
// asks for the spec when it starts managing a ScaledObject, which makes this the
// earliest notice WVA gets that a workload exists.
//
// Answered whether or not this replica is the leader. It is the one call whose
// answer is static, and the answer KEDA gets when it reconciles a ScaledObject
// decides what metric the HPA carries -- refusing it there leaves the HPA on
// the Kubernetes CPU default until the next reconcile, which nothing may ever
// trigger (see Server.NeedLeaderElection). KEDA asks again on every poll --
// GetMetricSpecForScaling is a live gRPC call with no caching anywhere above
// it, driven by a pollingInterval timer and by the external-metrics path
// independently -- and those calls register the workload like any other.
func (h *Handler) GetMetricSpec(ctx context.Context, ref *pb.ScaledObjectRef) (*pb.GetMetricSpecResponse, error) {
	h.observe(ref)
	// KEDA builds the HPA's spec.metrics from this response. If the call never
	// arrives, the HPA is created with an empty metrics list, Kubernetes
	// defaults it to Resource/cpu, and the workload scales on a CPU metric a
	// cluster with no metrics-server cannot even evaluate -- while the
	// ScaledObject reports Ready=True and WVA goes on publishing the correct
	// replica count every cycle. Nothing in WVA's output distinguishes that
	// from working.
	//
	// Until this line, it was not distinguishable from WVA's side at all:
	// IsActive logged every poll and this logged nothing, so the
	// inconsequential call was observable and the decisive one was not. Two CI
	// failures were investigated end to end without being able to answer "did
	// KEDA ever ask?", and the answer had to be reconstructed from the KEDA
	// operator's own log.
	//
	// V(1), beside IsActive, and NOT unconditionally: KEDA asks on every poll
	// (GetMetricSpecForScaling is uncached), and the samples here ship
	// pollingInterval: 5, so an unconditional line is one per ScaledObject per
	// poll forever at a level an operator cannot turn down. V(1) still reaches
	// them: the deployment passes no -v, so it runs at logging.DEFAULT, which
	// is 2.
	log.FromContext(ctx).V(1).Info("external scaler GetMetricSpec",
		"namespace", ref.GetNamespace(), "scaledObject", ref.GetName(),
		"metricName", MetricName, "targetSize", metricTargetSize)
	return &pb.GetMetricSpecResponse{
		MetricSpecs: []*pb.MetricSpec{{MetricName: MetricName, TargetSize: metricTargetSize}},
	}, nil
}

// errNoDecision is what GetMetrics answers for a target WVA has not decided
// yet. Unavailable, exactly like errNotLeader and for the same reason: a 0
// would be an answer, and HPA acts on answers. With a target of 1 it computes
// zero replicas and clamps to minReplicas, so the decision store being empty
// -- which it is on every restart and every lease hand-off, until the new
// leader's first cycle publishes -- would cut every running fleet to its floor.
// An error is a trigger failure instead: HPA keeps the current count, and KEDA
// applies the ScaledObject's fallback, if it has one, after failureThreshold.
var errNoDecision = status.Error(codes.Unavailable,
	"WVA has not published a decision for this target yet; holding the current replica count")

// GetMetrics returns WVA's desired replica count as the metric value. Before the
// first optimization decision exists for the target it refuses with Unavailable
// (see errNoDecision), so HPA holds the current count rather than acting on a 0
// nobody decided.
func (h *Handler) GetMetrics(ctx context.Context, req *pb.GetMetricsRequest) (*pb.GetMetricsResponse, error) {
	ref := req.GetScaledObjectRef()
	h.observe(ref)
	logger := log.FromContext(ctx)
	if !h.leader() {
		// At V(1), beside IsActive's line, because this is the poll path and
		// says the same thing every 5 s while a lease changes hands. It is
		// logged at all because WVA used to refuse here in total silence: KEDA
		// recorded the refusal and WVA did not, so the one component an
		// operator reads first had no trace of its own answer.
		logger.V(1).Info("external scaler GetMetrics refused: not the leader",
			"namespace", ref.GetNamespace(), "scaledObject", ref.GetName())
		return nil, errNotLeader
	}
	d, ok, err := h.desired(ctx, ref)
	if err != nil {
		// The error goes back to KEDA, which logs it; this records that WVA is
		// where it came from.
		logger.V(1).Info("external scaler GetMetrics failed",
			"namespace", ref.GetNamespace(), "scaledObject", ref.GetName(), "err", err)
		return nil, err
	}
	if !ok {
		// Logged for the reason the not-leader refusal is: KEDA records the
		// trigger error, and without this line WVA's own log has no trace of
		// why the HPA stopped moving.
		logger.V(1).Info("external scaler GetMetrics refused: no decision yet",
			"namespace", ref.GetNamespace(), "scaledObject", ref.GetName())
		return nil, errNoDecision
	}
	value := int64(d)
	// The VALUE KEDA actuates on. Only a decided target reaches this line; an
	// undecided one is refused above, so a 0 here is WVA's answer. That is why
	// the line no longer carries a `decided` field: it could only ever be true.
	logger.V(1).Info("external scaler GetMetrics",
		"namespace", ref.GetNamespace(), "scaledObject", ref.GetName(),
		"metricName", MetricName, "value", value)
	return &pb.GetMetricsResponse{
		MetricValues: []*pb.MetricValue{{MetricName: MetricName, MetricValue: value}},
	}, nil
}

// IsActive reports the target active unless WVA has decided it needs zero
// replicas; before the first decision it mirrors the target's current replica
// count (see isActive).
//
// This is the poll path, used by a `type: external` trigger. A `type:
// external-push` trigger uses StreamIsActive instead, which reports the same
// predicate without waiting for a poll interval.
func (h *Handler) IsActive(ctx context.Context, ref *pb.ScaledObjectRef) (*pb.IsActiveResponse, error) {
	h.observe(ref)
	logger := log.FromContext(ctx)
	if !h.leader() {
		// Logged for the same reason GetMetrics' refusal is, and it was missed
		// there first: a refusal KEDA records and WVA does not leaves the
		// component an operator reads first with no trace of its own answer.
		// Both handlers refuse identically, so logging one and not the other
		// just moved the blind spot.
		logger.V(1).Info("external scaler IsActive refused: not the leader",
			"namespace", ref.GetNamespace(), "scaledObject", ref.GetName())
		return nil, errNotLeader
	}
	active, err := h.isActive(ctx, ref)
	if err != nil {
		// The error goes back to KEDA, which logs it; this records that WVA is
		// where it came from.
		logger.V(1).Info("external scaler IsActive failed",
			"namespace", ref.GetNamespace(), "scaledObject", ref.GetName(), "err", err)
		return nil, err
	}
	logger.V(1).Info("external scaler IsActive",
		"namespace", ref.GetNamespace(), "scaledObject", ref.GetName(), "active", active)
	return &pb.IsActiveResponse{Result: active}, nil
}

// isActive is the shared activation predicate behind IsActive and
// StreamIsActive: active unless WVA has decided the target needs zero replicas.
//
// Before the first decision exists the predicate falls back to the target's
// CURRENT replica count, which is the only honest answer: reporting active
// unconditionally would wake every workload parked at zero the moment KEDA
// first asks — defeating scale-to-zero and pre-empting the scale-from-zero
// engine, which never sees the workload as inactive. Reporting inactive
// unconditionally would be worse, scaling a running workload to zero before WVA
// has looked at it. So: a target already at zero stays asleep until a decision
// (or the scale-from-zero engine) wakes it; a running target stays up.
//
// A decision that has gone stale takes the same fallback as no decision at all,
// so a target cannot be held awake by a value nobody is publishing any more —
// see honoursDecision.
func (h *Handler) isActive(ctx context.Context, ref *pb.ScaledObjectRef) (bool, error) {
	d, ok, err := h.decisionFor(ctx, ref)
	if err != nil {
		return false, err
	}
	if ok && h.honoursDecision(d) {
		return d.DesiredReplicas > 0, nil
	}
	return h.currentlyRunning(ctx, ref), nil
}

// currentlyRunning reports whether the scale target has replicas right now. It
// is only consulted before WVA's first decision. Any failure to read the target
// answers true — the safe direction, since a false negative would scale a
// running workload to zero on the strength of a failed lookup.
func (h *Handler) currentlyRunning(ctx context.Context, ref *pb.ScaledObjectRef) bool {
	logger := log.FromContext(ctx)

	var so kedav1alpha1.ScaledObject
	nn := types.NamespacedName{Namespace: ref.GetNamespace(), Name: ref.GetName()}
	if err := h.client.Get(ctx, nn, &so); err != nil || so.Spec.ScaleTargetRef == nil {
		logger.V(1).Info("no decision yet and ScaledObject unreadable; reporting active",
			"scaledObject", nn.String())
		return true
	}

	target := &unstructured.Unstructured{}
	apiVersion := so.Spec.ScaleTargetRef.APIVersion
	if apiVersion == "" {
		apiVersion = "apps/v1" // KEDA's own default for scaleTargetRef
	}
	kind := so.Spec.ScaleTargetRef.Kind
	if kind == "" {
		kind = "Deployment" // KEDA's own default for scaleTargetRef
	}
	target.SetAPIVersion(apiVersion)
	target.SetKind(kind)
	targetKey := types.NamespacedName{Namespace: ref.GetNamespace(), Name: so.Spec.ScaleTargetRef.Name}
	if err := h.client.Get(ctx, targetKey, target); err != nil {
		logger.V(1).Info("no decision yet and scale target unreadable; reporting active",
			"target", targetKey.String(), "kind", kind)
		return true
	}

	// An absent spec.replicas means the workload defaults to 1 (Deployment and
	// LeaderWorkerSet both do), so absent reads as running.
	replicas, found, err := unstructured.NestedInt64(target.Object, "spec", "replicas")
	if err != nil || !found {
		return true
	}
	return replicas > 0
}

// streamKeepalive bounds how long StreamIsActive stays silent. Activation is
// driven by decision-store wake-ups, so this only re-asserts the current state
// periodically — cheap insurance against a wake-up lost to a dropped stream or
// a decision written before KEDA opened it.
const streamKeepalive = 30 * time.Second

// StreamIsActive pushes activation to KEDA for a `type: external-push` trigger.
// It sends the current state immediately, then again whenever the target's
// decision changes it — so a workload sitting at zero is activated as soon as
// the decision lands, rather than up to one poll interval later. This is the
// path scale-from-zero rides: the engine that spots pending requests writes a
// non-zero decision, and that write wakes this stream.
//
// The stream lives until KEDA closes it (ctx cancellation), which returns nil —
// a closed stream is normal, not a failure. Errors resolving the scale target
// are returned, so KEDA surfaces a misconfigured ScaledObject instead of
// silently never activating.
func (h *Handler) StreamIsActive(ref *pb.ScaledObjectRef, stream pb.ExternalScaler_StreamIsActiveServer) error {
	ctx := stream.Context()
	logger := log.FromContext(ctx).WithValues(
		"namespace", ref.GetNamespace(), "scaledObject", ref.GetName())

	// Registered for the life of the stream rather than by timestamp. On a push
	// trigger this is the ONLY call a workload parked at zero ever receives —
	// KEDA does not poll IsActive, and the HPA does not query metrics for a
	// workload it is not scaling — so a TTL keyed on the last call would evict
	// exactly the entries whose purpose is to be woken from zero.
	if h.registry != nil && ref != nil {
		release := h.registry.Hold(ref.GetNamespace(), ref.GetName(), ref.GetScalerMetadata())
		defer release()
	}

	// Ended, not held, before the lease: the standby server that answers here
	// is stopped on election, and a stream parked here would hold that stop
	// open. Ended with OK rather than refused, deliberately: KEDA re-opens a
	// stream that closed cleanly after a steady 2 s, but backs off a failed
	// one 2, 4, 8 ... 60 s, so a refusal could leave a zero-parked workload
	// without its activation stream for a minute after election. The
	// registration above still happens, so the workload is known to the
	// engines the moment they start.
	if !h.leader() {
		// V(3): KEDA reopens every 2 s per parked workload for the whole wait
		// on the lease, and the default verbosity ships V(1) and V(2).
		logger.V(3).Info("external scaler StreamIsActive ended before the leader lease is held; KEDA will reopen it")
		return nil
	}

	// Resolve the target once, up front: it is fixed for the life of the
	// ScaledObject, and subscribing needs the resolved name.
	name, err := h.targetName(ctx, ref)
	if err != nil {
		return err
	}
	updates, unsubscribe := h.store.Subscribe(ref.GetNamespace(), name)
	defer unsubscribe()

	// Send the opening state before waiting on anything, so KEDA is never left
	// holding a stream that has said nothing.
	active, err := h.isActive(ctx, ref)
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.IsActiveResponse{Result: active}); err != nil {
		return err
	}
	logger.V(1).Info("external scaler StreamIsActive opened", "active", active)

	ticker := time.NewTicker(streamKeepalive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.V(1).Info("external scaler StreamIsActive closed")
			return nil
		case <-updates:
			next, err := h.isActive(ctx, ref)
			if err != nil {
				return err
			}
			if next == active {
				continue // nothing for KEDA to act on
			}
			active = next
			if err := stream.Send(&pb.IsActiveResponse{Result: active}); err != nil {
				return err
			}
			logger.V(1).Info("external scaler pushed activation change", "active", active)
		case <-ticker.C:
			next, err := h.isActive(ctx, ref)
			if err != nil {
				return err
			}
			active = next
			if err := stream.Send(&pb.IsActiveResponse{Result: active}); err != nil {
				return err
			}
		}
	}
}
