package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Before the first definite answer there is no verdict to keep: a probe whose
// first List times out must report not ready, and ask again on the next probe
// rather than wait out the TTL. It reported Ready, which put a controller that
// may not list nodes into service until the next answer.
func TestNodeReadProbeIsNotReadyBeforeTheFirstAnswer(t *testing.T) {
	resetNodeReadProbe()
	t.Cleanup(resetNodeReadProbe)

	r := &listReader{err: apierrors.NewTimeoutError("node list timed out", 1)}
	assert.Error(t, nodeReadProbe(context.Background(), r),
		"no answer yet: not ready")
	assert.Error(t, nodeReadProbe(context.Background(), r),
		"still no answer: still not ready")
	assert.Equal(t, 2, r.calls, "with no answer yet, every probe must ask again")

	r.err = nil
	assert.NoError(t, nodeReadProbe(context.Background(), r), "the first answer decides")
	r.err = apierrors.NewTimeoutError("blip", 1)
	nodeReadProbeMu.Lock()
	nodeReadProbeAt = nodeReadProbeAt.Add(-2 * nodeReadProbeTTL)
	nodeReadProbeMu.Unlock()
	assert.NoError(t, nodeReadProbe(context.Background(), r), "after an answer, a blip keeps it")
}

// blockingReader's List blocks until its context ends, like a List against an
// API server that never answers.
type blockingReader struct{ listReader }

func (r *blockingReader) List(ctx context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	r.calls++
	<-ctx.Done()
	return ctx.Err()
}

// The List is bounded by nodeReadProbeTimeout, under the readiness probe's own
// 3s timeoutSeconds: an API server that never answers yields a verdict here,
// the previous one, rather than a probe the kubelet abandons.
func TestNodeReadProbeBoundsAHangingList(t *testing.T) {
	resetNodeReadProbe()
	t.Cleanup(resetNodeReadProbe)

	assert.Error(t, nodeReadProbe(context.Background(), &listReader{err: nodesForbidden()}), "setup: Forbidden")
	nodeReadProbeMu.Lock()
	nodeReadProbeAt = nodeReadProbeAt.Add(-2 * nodeReadProbeTTL)
	nodeReadProbeMu.Unlock()

	start := time.Now()
	err := nodeReadProbe(context.Background(), &blockingReader{})
	took := time.Since(start)
	assert.True(t, apierrors.IsForbidden(err), "a hanging List must keep the previous verdict, got %v", err)
	assert.Less(t, took, 3*time.Second, "the List must be cut off before the probe's 3s timeout")
}
