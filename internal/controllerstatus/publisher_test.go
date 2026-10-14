package controllerstatus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/constants"
)

const testNamespace = "wva-system"

// writes counts the Create and Update calls that reached the fake client, and
// lets a test fail the next few of either kind.
type writes struct {
	creates, updates int
	failCreate       []error
	failUpdate       []error
}

func newTestPublisher(t *testing.T, w *writes, objs ...client.Object) (*Publisher, client.Client) {
	t.Helper()
	t.Setenv("POD_NAME", "wva-controller-manager-abc")
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			w.creates++
			if len(w.failCreate) > 0 {
				err := w.failCreate[0]
				w.failCreate = w.failCreate[1:]
				return err
			}
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			w.updates++
			if len(w.failUpdate) > 0 {
				err := w.failUpdate[0]
				w.failUpdate = w.failUpdate[1:]
				return err
			}
			return cl.Update(ctx, obj, opts...)
		},
	})
	return New(c, c, testNamespace), base
}

func stored(t *testing.T, c client.Client) *corev1.ConfigMap {
	t.Helper()
	var cm corev1.ConfigMap
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: ConfigMapName}, &cm); err != nil {
		t.Fatalf("reading %s: %v", ConfigMapName, err)
	}
	return &cm
}

var configMapsResource = schema.GroupResource{Resource: "configmaps"}

func TestFlushCreatesOnceAndSkipsUnchanged(t *testing.T) {
	w := &writes{}
	p, c := newTestPublisher(t, w)
	ctx := context.Background()

	if err := p.flush(ctx); err != nil {
		t.Fatalf("first flush: %v", err)
	}
	cm := stored(t, c)
	if cm.Labels[labelName] != labelNameValue || cm.Labels[labelComponent] != componentValue {
		t.Errorf("labels = %v", cm.Labels)
	}
	if got := cm.Data[KeyFormatVersion]; got != FormatVersion {
		t.Errorf("formatVersion = %q", got)
	}
	if got := cm.Data[KeyLeader]; got != "wva-controller-manager-abc" {
		t.Errorf("leader = %q, want POD_NAME", got)
	}
	for _, k := range []string{KeyPolicyNamespace, KeyPolicyConfigMap, KeyPolicyResourceVersion, KeyPolicyAccepted,
		KeyPolicyError, KeyLimiter, KeyUtilizationShare, KeyUtilizationShareError, KeyControllerInstance} {
		if _, ok := cm.Data[k]; !ok {
			t.Errorf("key %q missing", k)
		}
	}

	// Unchanged content: no write at all, however often it is flushed or set.
	p.SetUtilizationShare(constants.UtilizationShareModeOff, nil)
	for range 3 {
		if err := p.flush(ctx); err != nil {
			t.Fatalf("repeat flush: %v", err)
		}
	}
	if w.creates != 1 || w.updates != 0 {
		t.Errorf("writes = %d creates, %d updates; want 1, 0", w.creates, w.updates)
	}
}

func TestFlushUpdatesOnChange(t *testing.T) {
	w := &writes{}
	p, c := newTestPublisher(t, w)
	ctx := context.Background()
	if err := p.flush(ctx); err != nil {
		t.Fatal(err)
	}
	p.SetUtilizationShare(constants.UtilizationShareModeInvalid, errors.New("optimizer.type: unknown"))
	if err := p.flush(ctx); err != nil {
		t.Fatal(err)
	}
	cm := stored(t, c)
	if cm.Data[KeyUtilizationShare] != constants.UtilizationShareModeInvalid ||
		cm.Data[KeyUtilizationShareError] != "optimizer.type: unknown" {
		t.Errorf("data = %v", cm.Data)
	}
	if w.creates != 1 || w.updates != 1 {
		t.Errorf("writes = %d creates, %d updates; want 1, 1", w.creates, w.updates)
	}
}

func TestFlushUpdatesExistingConfigMap(t *testing.T) {
	existing := &corev1.ConfigMap{}
	existing.Name, existing.Namespace = ConfigMapName, testNamespace
	existing.Data = map[string]string{KeyFormatVersion: "0", "stale": "x"}
	w := &writes{}
	p, c := newTestPublisher(t, w, existing)
	if err := p.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	cm := stored(t, c)
	if _, ok := cm.Data["stale"]; ok || cm.Data[KeyFormatVersion] != FormatVersion {
		t.Errorf("data = %v, want the desired content only", cm.Data)
	}
	if cm.Labels[labelComponent] != componentValue {
		t.Errorf("labels = %v", cm.Labels)
	}
	if w.creates != 0 || w.updates != 1 {
		t.Errorf("writes = %d creates, %d updates; want 0, 1", w.creates, w.updates)
	}
}

func TestFlushRetriesConflict(t *testing.T) {
	w := &writes{}
	p, c := newTestPublisher(t, w)
	ctx := context.Background()
	if err := p.flush(ctx); err != nil {
		t.Fatal(err)
	}
	w.failUpdate = []error{apierrors.NewConflict(configMapsResource, ConfigMapName, errors.New("modified"))}
	p.SetUtilizationShare(constants.UtilizationShareModeShadow, nil)
	if err := p.flush(ctx); err != nil {
		t.Fatalf("flush after one conflict: %v", err)
	}
	if got := stored(t, c).Data[KeyUtilizationShare]; got != constants.UtilizationShareModeShadow {
		t.Errorf("utilizationShare = %q", got)
	}
	if w.updates != 2 {
		t.Errorf("updates = %d, want 2 (conflict, then success)", w.updates)
	}

	// A create that lost the race to another writer reads again and updates.
	w2 := &writes{failCreate: []error{apierrors.NewAlreadyExists(configMapsResource, ConfigMapName)}}
	p2, c2 := newTestPublisher(t, w2)
	if err := p2.flush(ctx); err != nil {
		t.Fatalf("flush after AlreadyExists: %v", err)
	}
	if w2.creates != 2 {
		t.Errorf("creates = %d, want 2", w2.creates)
	}
	stored(t, c2)
}

func TestFlushKeepsStateWhenForbidden(t *testing.T) {
	forbidden := apierrors.NewForbidden(configMapsResource, ConfigMapName, errors.New("no Role"))
	w := &writes{failCreate: []error{forbidden}}
	p, c := newTestPublisher(t, w)
	ctx := context.Background()
	p.SetUtilizationShare(constants.UtilizationShareModeActive, nil)

	err := p.flush(ctx)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("flush error = %v, want the Forbidden returned, not swallowed", err)
	}
	if p.written != nil {
		t.Errorf("written = %v after a failed write; want nothing recorded", p.written)
	}
	if got := p.Data()[KeyUtilizationShare]; got != constants.UtilizationShareModeActive {
		t.Errorf("desired utilizationShare = %q, want it kept", got)
	}

	// Once permitted, the next attempt writes what was kept.
	if err := p.flush(ctx); err != nil {
		t.Fatalf("flush after the Role appeared: %v", err)
	}
	if got := stored(t, c).Data[KeyUtilizationShare]; got != constants.UtilizationShareModeActive {
		t.Errorf("utilizationShare = %q", got)
	}
}

func TestRecordPolicy(t *testing.T) {
	w := &writes{}
	p, _ := newTestPublisher(t, w)
	p.RecordPolicy(nil, "policy-ns", "wva-scaling-policy-config", "42", errors.New("bad threshold"))
	d := p.Data()
	if d[KeyPolicyNamespace] != "policy-ns" || d[KeyPolicyConfigMap] != "wva-scaling-policy-config" ||
		d[KeyPolicyResourceVersion] != "42" || d[KeyPolicyAccepted] != "false" || d[KeyPolicyError] != "bad threshold" {
		t.Errorf("data = %v", d)
	}
	p.RecordPolicy(nil, "policy-ns", "wva-scaling-policy-config", "43", nil)
	d = p.Data()
	if d[KeyPolicyResourceVersion] != "43" || d[KeyPolicyAccepted] != "true" || d[KeyPolicyError] != "" {
		t.Errorf("data = %v", d)
	}
}

func TestNilPublisherIsSafe(t *testing.T) {
	var p *Publisher
	p.RecordPolicy(nil, "a", "b", "1", nil)
	p.ObserveConfig(nil)
	p.SetUtilizationShare(constants.UtilizationShareModeOff, nil)
	if p.Data() != nil {
		t.Error("nil publisher returned data")
	}
}

func TestStartWritesAndCoalesces(t *testing.T) {
	w := &writes{}
	p, c := newTestPublisher(t, w)
	p.debounce = 50 * time.Millisecond
	if !p.NeedLeaderElection() {
		t.Fatal("the status must be written by the leader only")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	read := func() map[string]string {
		var cm corev1.ConfigMap
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: ConfigMapName}, &cm); err != nil {
			return nil
		}
		return cm.Data
	}
	waitFor("the first write", func() bool { return read() != nil })

	// Three changes inside one debounce window land as one update.
	p.SetUtilizationShare(constants.UtilizationShareModeShadow, nil)
	p.RecordPolicy(nil, testNamespace, "wva-scaling-policy-config", "7", nil)
	p.SetUtilizationShare(constants.UtilizationShareModeActive, nil)
	waitFor("the coalesced write", func() bool {
		d := read()
		return d[KeyPolicyResourceVersion] == "7" && d[KeyUtilizationShare] == constants.UtilizationShareModeActive
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned %v", err)
	}
	if w.creates != 1 || w.updates != 1 {
		t.Errorf("writes = %d creates, %d updates; want 1, 1", w.creates, w.updates)
	}
}

// TestFlushCreateLosesRaceToAnotherWriter: between this process's read (no
// ConfigMap) and its create, another writer -- a previous leader still
// finishing its term -- creates the ConfigMap with its own content. The create
// fails AlreadyExists; the flush must re-read and overwrite with its content,
// not give up or report the race as a failure.
func TestFlushCreateLosesRaceToAnotherWriter(t *testing.T) {
	w := &writes{}
	p, c := newTestPublisher(t, w)
	raced := false
	p.writer = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			w.creates++
			if !raced {
				raced = true
				other := &corev1.ConfigMap{}
				other.Name, other.Namespace = ConfigMapName, testNamespace
				other.Data = map[string]string{KeyLeader: "the-other-leader"}
				if err := cl.Create(ctx, other); err != nil {
					t.Fatalf("seeding the race winner: %v", err)
				}
				return apierrors.NewAlreadyExists(configMapsResource, ConfigMapName)
			}
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			w.updates++
			return cl.Update(ctx, obj, opts...)
		},
	})
	if err := p.flush(context.Background()); err != nil {
		t.Fatalf("flush after losing the create race: %v", err)
	}
	cm := stored(t, c)
	if got := cm.Data[KeyLeader]; got != "wva-controller-manager-abc" {
		t.Errorf("leader = %q, want this process's content written over the race winner's", got)
	}
	if cm.Labels[labelComponent] != componentValue {
		t.Errorf("labels = %v", cm.Labels)
	}
	if w.creates != 1 || w.updates != 1 {
		t.Errorf("writes = %d creates, %d updates; want 1 (lost), 1", w.creates, w.updates)
	}
	if p.written == nil {
		t.Error("written not recorded after the race was resolved")
	}
}

// TestReportErrThrottlesUnchangedError: an unchanged error is logged once per
// errorRelogEvery, a changed one at once, and recovery re-arms it.
func TestReportErrThrottlesUnchangedError(t *testing.T) {
	p, _ := newTestPublisher(t, &writes{})
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	var logged []string
	logger := funcr.New(func(_, args string) { logged = append(logged, args) }, funcr.Options{})
	forbidden := apierrors.NewForbidden(configMapsResource, ConfigMapName, errors.New("no Role"))

	p.reportErr(logger, forbidden)
	if len(logged) != 1 {
		t.Fatalf("first error: %d lines logged, want 1", len(logged))
	}
	if !strings.Contains(logged[0], "wva-controller-status-role") {
		t.Errorf("a Forbidden must carry the RBAC hint: %s", logged[0])
	}
	now = now.Add(errorRelogEvery - time.Second)
	p.reportErr(logger, forbidden)
	if len(logged) != 1 {
		t.Errorf("unchanged error inside %v: %d lines, want 1", errorRelogEvery, len(logged))
	}
	now = now.Add(time.Second)
	p.reportErr(logger, forbidden)
	if len(logged) != 2 {
		t.Errorf("unchanged error after %v: %d lines, want 2", errorRelogEvery, len(logged))
	}
	p.reportErr(logger, errors.New("connection refused"))
	if len(logged) != 3 {
		t.Errorf("changed error: %d lines, want 3 (logged at once)", len(logged))
	}

	p.reportRecovered(logger)
	if len(logged) != 4 {
		t.Errorf("recovery: %d lines, want 4", len(logged))
	}
	p.reportErr(logger, errors.New("connection refused"))
	if len(logged) != 5 {
		t.Errorf("the same error after a recovery: %d lines, want 5 (re-armed)", len(logged))
	}
}
