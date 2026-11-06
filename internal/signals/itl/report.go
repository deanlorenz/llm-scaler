package itl

import "time"

// SweepKeepingTraces is Sweep, keeping the replicas' settle traces, for a
// caller that forgets a key's line and goes on watching its replicas
// (ForgetKeysKeepingTraces says why).
func (t *Tracker) SweepKeepingTraces(now time.Time, stale func(key string) bool) (windows, baselines int) {
	return t.sweep(now, stale, true)
}
