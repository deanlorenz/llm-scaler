package saturation

// SwitchITLTierHysteresis is the replay switch for the latch on the ITL
// line's tier (issue #495, itl.Admission.TierHysteresis). Off, each cycle
// chooses its tier alone, as before.
const SwitchITLTierHysteresis = "saturation-itl-tier-hysteresis"

// tierHysteresisOn reports SwitchITLTierHysteresis.
func (a *SaturationAnalyzer) tierHysteresisOn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.itlTierHysteresis
}
