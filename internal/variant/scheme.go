package variant

import "k8s.io/apimachinery/pkg/runtime"

// A Variant is an event target, and an event target is a runtime.Object.
var (
	_ runtime.Object = (*VariantAutoscaling)(nil)
	_ runtime.Object = (*VariantAutoscalingList)(nil)
)
