package steadystate

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/controllerstatus"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
)

// inFlightStore is the in-flight ledger's persistence in the controller
// status (controllerstatus.KeyInFlightLedger).
type inFlightStore struct {
	// loaded is set once the persisted ledger was read back, or found
	// absent or unusable; errLogged once a failed read was logged.
	loaded    bool
	errLogged bool
	// last is the ledger last handed to the status publisher.
	last string
}

// restoreInFlight reads the persisted in-flight ledger back into
// allocation.DefaultInFlight, once (allocation.InFlightLedger.RestoreInFlight):
// the holds still within their TTL, a replica whose pods are Unschedulable
// and a LeaderWorkerSet group's unplaced workers among them, outlive the
// restart. A read that fails is tried again next cycle; until one succeeds
// nothing is persisted, so the ledger in the ConfigMap is not overwritten by
// this process's empty one. A ledger that cannot be parsed, or of another
// version, is ignored and logged.
func (e *Engine) restoreInFlight(ctx context.Context, now time.Time) {
	st := &e.inFlightStore
	if st.loaded {
		return
	}
	var reader client.Reader
	if e.apiReader != nil {
		reader = e.apiReader
	} else if e.client != nil {
		reader = e.client
	}
	if reader == nil {
		st.loaded = true
		return
	}
	logger := ctrl.LoggerFrom(ctx)
	raw, found, err := controllerstatus.ReadKey(ctx, reader, config.SystemNamespace(), controllerstatus.KeyInFlightLedger)
	if err != nil {
		if !st.errLogged {
			st.errLogged = true
			logger.Info("Cannot read the persisted in-flight node ledger yet; the node holes it carries are not held until it is read, and the read is tried again each cycle",
				"error", err.Error())
		}
		return
	}
	st.loaded = true
	if !found {
		return
	}
	p, err := allocation.UnmarshalInFlight(raw)
	if err != nil {
		logger.Error(err, "Ignoring an unreadable in-flight node ledger")
		return
	}
	restored, dropped := allocation.DefaultInFlight.RestoreInFlight(p, now)
	if dropped > 0 {
		logger.Info("Warning: dropped in-flight node ledger entries out of bounds; this controller did not write them",
			"dropped", dropped)
	}
	if restored > 0 {
		logger.Info("Restored in-flight node holds after a restart", "variants", restored)
	}
	// last stays unset: the next persist hands the restored ledger to the
	// status publisher.
}

// persistInFlight hands the in-flight ledger to the status publisher when it
// changed, once the persisted one was read back.
func (e *Engine) persistInFlight(ctx context.Context, now time.Time) {
	st := &e.inFlightStore
	if !st.loaded {
		return
	}
	raw, err := allocation.DefaultInFlight.MarshalInFlight(now)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Cannot persist the in-flight node ledger; the last persisted one stays")
		return
	}
	if raw == st.last {
		return
	}
	st.last = raw
	e.Status.SetInFlightLedger(raw)
}
