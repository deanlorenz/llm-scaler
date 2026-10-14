package steadystate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/capacity"
)

// stateSpy is an analyzer double for the store alone: it exports a given
// state and records what it was asked to restore or forget.
type stateSpy struct {
	plainAnalyzer
	state    saturation.LearnedState
	restored []saturation.LearnedState
	forgot   int
}

func (s *stateSpy) ExportLearnedState() saturation.LearnedState { return s.state }
func (s *stateSpy) RestoreLearnedState(st saturation.LearnedState) saturation.RestoreReport {
	s.restored = append(s.restored, st)
	return saturation.RestoreReport{}
}
func (s *stateSpy) ForgetLearnedState() { s.forgot++ }

func sampleState(k2 float64) saturation.LearnedState {
	now := time.Now()
	return saturation.LearnedState{
		K2: map[string]capacity.RollingAverageSnapshot{
			"ns|v|m|H100|1|both|short|q5": {Values: []float64{k2}, LastUsed: now, LastWritten: now},
		},
		Config: map[string]string{"ns/v": "abc123"},
	}
}

func snapshotEngine(t *testing.T, c client.Client, spy *stateSpy) *Engine {
	t.Helper()
	cfg := config.NewTestConfig()
	config.SetLearnedStateSnapshotForTest(cfg, true)
	return &Engine{Config: cfg, client: c, saturationV2Analyzer: spy}
}

func storedDoc(t *testing.T, c client.Client) (learnedStateDoc, *corev1.ConfigMap) {
	t.Helper()
	var cm corev1.ConfigMap
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Namespace: config.SystemNamespace(), Name: learnedStateConfigMap}, &cm))
	var doc learnedStateDoc
	require.NoError(t, json.Unmarshal([]byte(cm.Data[learnedStateKey()]), &doc))
	return doc, &cm
}

func TestLearnedStateSnapshotIsOffByDefault(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	e := &Engine{Config: config.NewTestConfig(), client: c, saturationV2Analyzer: &stateSpy{state: sampleState(4000)}}
	now := time.Now()
	e.restoreLearnedState(context.Background(), now)
	e.writeLearnedState(context.Background(), now)

	var cm corev1.ConfigMap
	err := c.Get(context.Background(), client.ObjectKey{Namespace: config.SystemNamespace(), Name: learnedStateConfigMap}, &cm)
	assert.True(t, apierrors.IsNotFound(err), "a ConfigMap was written with LEARNED_STATE_SNAPSHOT unset")
}

// A restart and a leader hand-off are the same transfer: one process writes,
// another, with nothing in memory, reads what it wrote.
func TestLearnedStateSurvivesARestartOrAHandOff(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	ctx := context.Background()
	now := time.Now()

	old := snapshotEngine(t, c, &stateSpy{state: sampleState(4000)})
	old.restoreLearnedState(ctx, now)
	old.writeLearnedState(ctx, now)
	doc, _ := storedDoc(t, c)
	assert.Equal(t, learnedStateVersion, doc.Version)
	assert.Equal(t, config.SystemNamespace(), doc.Writer.Namespace)
	assert.NotEmpty(t, doc.Writer.Identity, "the document names the controller that wrote it")

	next := &stateSpy{}
	snapshotEngine(t, c, next).restoreLearnedState(ctx, now.Add(time.Minute))
	require.Len(t, next.restored, 1, "the new leader did not restore what the old one wrote")
	assert.Equal(t, []float64{4000}, next.restored[0].K2["ns|v|m|H100|1|both|short|q5"].Values)
	assert.Equal(t, "abc123", next.restored[0].Config["ns/v"], "the configuration identity travels with the state")
}

// Deleting the ConfigMap flushes: the controller forgets rather than writing
// what it knows straight back, which would make the deletion a no-op.
func TestDeletingTheSnapshotFlushes(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	ctx := context.Background()
	now := time.Now()
	spy := &stateSpy{state: sampleState(4000)}
	e := snapshotEngine(t, c, spy)
	e.restoreLearnedState(ctx, now)
	e.writeLearnedState(ctx, now)
	_, cm := storedDoc(t, c)
	require.NoError(t, c.Delete(ctx, cm))

	spy.state = sampleState(5000)
	e.writeLearnedState(ctx, now.Add(2*time.Minute))
	assert.Equal(t, 1, spy.forgot, "the deleted snapshot was not taken as a flush")
	var again corev1.ConfigMap
	err := c.Get(ctx, client.ObjectKey{Namespace: config.SystemNamespace(), Name: learnedStateConfigMap}, &again)
	assert.True(t, apierrors.IsNotFound(err), "the state was written straight back over the deletion")
}

// A read that fails is not an empty store: nothing is restored, and nothing is
// written over what could not be read.
func TestAForbiddenReadIsNotAnEmptyStore(t *testing.T) {
	base := fake.NewClientBuilder().Build()
	ctx := context.Background()
	now := time.Now()
	old := snapshotEngine(t, base, &stateSpy{state: sampleState(4000)})
	old.restoreLearnedState(ctx, now)
	old.writeLearnedState(ctx, now)
	before, _ := storedDoc(t, base)

	forbidden := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == learnedStateConfigMap {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, nil)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
	spy := &stateSpy{state: sampleState(9000)}
	e := snapshotEngine(t, forbidden, spy)
	e.restoreLearnedState(ctx, now)
	e.writeLearnedState(ctx, now.Add(2*time.Minute))
	assert.Empty(t, spy.restored)

	after, _ := storedDoc(t, base)
	assert.Equal(t, before.State.K2, after.State.K2, "a controller that could not read the snapshot overwrote it")
}

// The other half of the same rule: once the read works again -- a Role applied
// late, an API server that was briefly unreachable -- the controller restores
// FIRST. Counting the failed read as a restore would let the first write after
// it replace the stored state with whatever this process learned since it
// started.
func TestAFailedReadIsRetriedBeforeAnythingIsWritten(t *testing.T) {
	base := fake.NewClientBuilder().Build()
	ctx := context.Background()
	now := time.Now()
	old := snapshotEngine(t, base, &stateSpy{state: sampleState(4000)})
	old.restoreLearnedState(ctx, now)
	old.writeLearnedState(ctx, now)

	denied := true
	flaky := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if denied && key.Name == learnedStateConfigMap {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, nil)
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
	spy := &stateSpy{state: sampleState(9000)}
	e := snapshotEngine(t, flaky, spy)
	e.restoreLearnedState(ctx, now) // denied
	denied = false

	e.writeLearnedState(ctx, now.Add(2*time.Minute)) // must not write: nothing restored yet
	after, _ := storedDoc(t, base)
	assert.Equal(t, []float64{4000}, after.State.K2["ns|v|m|H100|1|both|short|q5"].Values,
		"the first write after a failed read replaced the stored state without restoring it")

	e.restoreLearnedState(ctx, now.Add(2*time.Minute))
	require.Len(t, spy.restored, 1, "the read was not retried")
}

// A document of another format version stays where it is, so a controller
// rolled back to that version still finds its own.
func TestAnotherVersionsDocumentIsLeftAlone(t *testing.T) {
	foreign := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: learnedStateConfigMap, Namespace: config.SystemNamespace()},
		Data:       map[string]string{"v0": `{"version":0}`},
	}
	c := fake.NewClientBuilder().WithObjects(foreign).Build()
	ctx := context.Background()
	now := time.Now()
	spy := &stateSpy{state: sampleState(4000)}
	e := snapshotEngine(t, c, spy)
	e.restoreLearnedState(ctx, now)
	assert.Empty(t, spy.restored, "another version's document was read as this one's")
	e.writeLearnedState(ctx, now)

	_, cm := storedDoc(t, c)
	assert.Equal(t, `{"version":0}`, cm.Data["v0"])
}

func TestASnapshotFromAnotherNamespaceIsNotRestored(t *testing.T) {
	raw, err := json.Marshal(learnedStateDoc{
		Version: learnedStateVersion, WrittenAt: time.Now(),
		Writer: learnedStateWriter{Namespace: "somewhere-else", Identity: "pod-x"},
		State:  sampleState(4000),
	})
	require.NoError(t, err)
	copied := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: learnedStateConfigMap, Namespace: config.SystemNamespace()},
		Data:       map[string]string{learnedStateKey(): string(raw)},
	}
	spy := &stateSpy{}
	snapshotEngine(t, fake.NewClientBuilder().WithObjects(copied).Build(), spy).
		restoreLearnedState(context.Background(), time.Now())
	assert.Empty(t, spy.restored, "a document written by another namespace's controller was restored")
}

func TestAnUnchangedStateIsNotRewritten(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	ctx := context.Background()
	now := time.Now()
	e := snapshotEngine(t, c, &stateSpy{state: sampleState(4000)})
	e.restoreLearnedState(ctx, now)
	e.writeLearnedState(ctx, now)
	_, first := storedDoc(t, c)
	e.writeLearnedState(ctx, now.Add(5*time.Minute))
	_, second := storedDoc(t, c)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion)
}

func TestTheSizeBoundLeavesOutTheLeastRecentlyUsedFirst(t *testing.T) {
	now := time.Now()
	s := saturation.LearnedState{K2: map[string]capacity.RollingAverageSnapshot{}}
	for i := range 200 {
		at := now.Add(-time.Duration(i) * time.Minute)
		s.K2[string(rune('a'+i%26))+string(rune('A'+i/26))] = capacity.RollingAverageSnapshot{
			Values: []float64{4000, 4100, 4200}, LastUsed: at, LastWritten: at,
		}
	}
	full, _, err := encodeLearnedState(s, now, 1<<30)
	require.NoError(t, err)
	raw, dropped, err := encodeLearnedState(s, now, len(full)/2)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(raw), len(full)/2)
	assert.Greater(t, dropped, 0)

	var doc learnedStateDoc
	require.NoError(t, json.Unmarshal([]byte(raw), &doc))
	oldestKept := now
	for _, w := range doc.State.K2 {
		if w.LastUsed.Before(oldestKept) {
			oldestKept = w.LastUsed
		}
	}
	left := 0
	for k, w := range s.K2 {
		if _, kept := doc.State.K2[k]; kept {
			continue
		}
		left++
		assert.False(t, w.LastUsed.After(oldestKept),
			"%s, used more recently than an entry that was kept, was left out", k)
	}
	assert.Equal(t, dropped, left)
}
