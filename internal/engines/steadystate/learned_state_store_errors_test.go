package steadystate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A store that can be read and not written -- the learned-state Role missing,
// the manager Role still reading ConfigMaps -- reports the write failure once
// and its recovery once. With one error slot for both operations, each
// minute's successful read cleared the write's error: "works again" and the
// same Forbidden were logged every minute, a store that looked like it kept
// recovering.
func TestAStoreThatCannotBeWrittenSaysSoOnce(t *testing.T) {
	var logged []string
	ctx := ctrl.LoggerInto(context.Background(),
		funcr.New(func(_, args string) { logged = append(logged, args) }, funcr.Options{}))
	count := func(msg string) int {
		n := 0
		for _, l := range logged {
			if strings.Contains(l, msg) {
				n++
			}
		}
		return n
	}
	denied := true
	forbidden := func(name string) error {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, name, nil)
	}
	c := interceptor.NewClient(fake.NewClientBuilder().Build(), interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if denied {
				return forbidden(obj.GetName())
			}
			return cl.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if denied {
				return forbidden(obj.GetName())
			}
			return cl.Update(ctx, obj, opts...)
		},
	})
	now := time.Now()
	e := snapshotEngine(t, c, &stateSpy{state: sampleState(4000)})
	e.restoreLearnedState(ctx, now)
	for i := range 4 {
		e.writeLearnedState(ctx, now.Add(time.Duration(i)*learnedStateInterval))
	}
	assert.Equal(t, 1, count("could not write the snapshot"), "the write failure is logged once, not every interval")
	assert.Zero(t, count("again"), "a read that worked reported the store as recovered while it still could not be written")

	denied = false
	e.writeLearnedState(ctx, now.Add(4*learnedStateInterval))
	assert.Equal(t, 1, count("the snapshot can be written again"), "the recovery of the write is logged once")
	assert.Zero(t, count("the snapshot can be read again"), "the read never failed")
}
