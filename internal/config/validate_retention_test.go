package config

import "testing"

// An unusable scaleToZero.retentionPeriod must fail the entry at load, where the
// operator sees it once, rather than pass and be re-reported by every cycle for
// every model that resolves to it.
func TestValidateRejectsUnusableRetentionPeriod(t *testing.T) {
	for _, bad := range []string{"5 minutes", "10", "0s", "-1m"} {
		p := ScalingPolicy{ScaleToZero: &ScaleToZeroEnvelope{RetentionPeriod: bad}}
		p.ApplyEntryDefaults()
		if err := p.Validate(); err == nil {
			t.Errorf("retentionPeriod %q validated", bad)
		}
	}
	for _, good := range []string{"", "90s", "5m", "1h30m"} {
		p := ScalingPolicy{ScaleToZero: &ScaleToZeroEnvelope{RetentionPeriod: good}}
		p.ApplyEntryDefaults()
		if err := p.Validate(); err != nil {
			t.Errorf("retentionPeriod %q rejected: %v", good, err)
		}
	}
}
