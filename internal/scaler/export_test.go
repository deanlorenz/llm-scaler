package scaler

import "time"

// SetPassiveZeroRecheckForTest shortens how often StreamIsActive re-reads a
// held variant it reports inactive, and returns a func that restores it.
func SetPassiveZeroRecheckForTest(d time.Duration) func() {
	old := passiveZeroRecheck
	passiveZeroRecheck = d
	return func() { passiveZeroRecheck = old }
}
