package config

// saturationAnalyzer is the saturation analyzer's type, whose band decides.
const saturationAnalyzer = "saturation"

// InertAnalyzerThresholdsMessage starts the warning logged for each analyzer
// InertAnalyzerThresholds returns.
const InertAnalyzerThresholdsMessage = "WARNING: an analyzer other than saturation sets scaleUpThreshold or " +
	"scaleDownBoundary; the combined decision is judged against saturation's band, so the setting " +
	"changes only that analyzer's own published result, not what is scaled"

// InertAnalyzerThresholds returns, in list order, the type of each analyzer
// entry other than saturation that sets scaleUpThreshold or
// scaleDownBoundary.
//
// The composite decides from every listed analyzer's replica need, in
// saturation's units, and judges that decision against saturation's band
// (its own override, else the model-level one). Another analyzer's band
// shapes only its own result, which the composite reads as a need, not as a
// band, so such a setting does not change what is scaled.
func (c ScalingPolicy) InertAnalyzerThresholds() []string {
	var out []string
	for _, a := range c.Analyzers {
		if a.EffectiveType() == saturationAnalyzer {
			continue
		}
		if a.ScaleUpThreshold != nil || a.ScaleDownBoundary != nil {
			out = append(out, a.EffectiveType())
		}
	}
	return out
}
