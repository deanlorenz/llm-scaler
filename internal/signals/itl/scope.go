package itl

import "strings"

// PriorScope is the physics a one-sided prior on B is read within (issue
// #467; BPrior): a learned B counts toward a variant's prior only when its
// key is the same model on the same accelerator and GPU count, under the
// same engine arguments (capacity.EngineParams.ArgsTag), in the same
// canonical role. Keys are the analyzers' window keys,
// namespace|model|variant|accelerator|gpus, so the variant may differ: a
// learned B is a property of the hardware and the engine, as the shared
// lines are. The role is known only for the variants of the model being
// analyzed (RoleOf), so a key of another namespace is out of scope: the
// rule errs toward the floor, never toward another engine's intercept.
type PriorScope struct {
	// Key is the variant's own window key.
	Key string
	// Args is the engine arguments its readings run under, "" when unknown;
	// with none, only the variant's own key is in scope.
	Args string
	// ArgsOf is the engine arguments a key was last fitted under, "" when
	// unknown.
	ArgsOf func(key string) string
	// RoleOf is a variant's canonical role by name, "" when unknown; a key
	// whose role is unknown, or differs, is out of scope.
	RoleOf func(variant string) string
}

// windowKeyFields splits a window key into its five fields; ok is false for
// any other shape.
func windowKeyFields(key string) (f [5]string, ok bool) {
	parts := strings.Split(key, "|")
	if len(parts) != 5 {
		return f, false
	}
	copy(f[:], parts)
	return f, true
}

// In reports whether key is in the scope of s: the variant's own key, or a
// key with the same model, accelerator and GPU count, the same known engine
// arguments, and the same known role.
func (s PriorScope) In(key string) bool {
	if key == s.Key {
		return true
	}
	if s.Args == "" || s.ArgsOf == nil || s.RoleOf == nil {
		return false
	}
	own, ok := windowKeyFields(s.Key)
	if !ok {
		return false
	}
	other, ok := windowKeyFields(key)
	if !ok || other[0] != own[0] || other[1] != own[1] || other[3] != own[3] || other[4] != own[4] {
		return false
	}
	role := s.RoleOf(own[2])
	return role != "" && s.RoleOf(other[2]) == role && s.ArgsOf(key) == s.Args
}

// ScopedBaselines is the learned intercepts of t's keys in scope s.
func (t *Tracker) ScopedBaselines(s PriorScope) []float64 {
	var out []float64
	for key, b := range t.Baselines() {
		if s.In(key) {
			out = append(out, b)
		}
	}
	return out
}
