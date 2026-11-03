package slo

import (
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learn"
)

// Forgetting one variant's learned state, on an operator's request (issue
// #431). The forget-learned-state annotation is read by the engine, which
// acts on a token once, through the saturation analyzer's mark
// (saturation.ForgottenToken), and then forgets the variant in both
// analyzers. This analyzer keeps no token of its own: it forgets when the
// engine asks.
//
// Before, the annotation reached the saturation analyzer alone. This
// analyzer fits its own ITL line with its own itl.Tracker, so after a forget
// its line still priced from the readings taken before it, and held the
// slope fitted from them for up to the slope hold.

// SwitchITLForgetReachesTheLine is the replay switch for the forget: on, the
// annotation forgets the variant's ITL line here too, under every key it was
// filed under -- its window, steady window, traces, intercept and held
// slope, and the engine arguments it was learned under; off, as before, it
// forgets nothing here.
const SwitchITLForgetReachesTheLine = "slo-itl-forget-reaches-the-line"

// forgetVariant forgets one variant's learned state in this analyzer: each
// learner on learn's list that it keeps, by name (forgetLearnerLocked). It
// returns how many entries it dropped. With SwitchITLForgetReachesTheLine
// off it forgets nothing.
func (a *Analyzer) forgetVariant(namespace, modelID, variant string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.itlForgetReachesLine {
		return 0
	}
	n := 0
	for _, learner := range learn.Learners() {
		dropped, _ := a.forgetLearnerLocked(learner, namespace, modelID, variant)
		n += dropped
	}
	return n
}

// forgetLearnerLocked forgets one learner's state for a variant, and returns
// how many entries it dropped and whether it has code for the learner. The
// learners on learn.Learners are the saturation analyzer's names; this
// analyzer keeps one of them, the ITL line, as an instance of its own, and
// holds none of the others' state. A name it does not know is a learner put
// on the list without a decision here: logged as an error, and nothing is
// forgotten. A test walks the list and holds every name to handled. Callers
// hold a.mu.
func (a *Analyzer) forgetLearnerLocked(name, namespace, modelID, variant string) (dropped int, handled bool) {
	switch name {
	case learn.ITL:
		return a.forgetLinesLocked(namespace, modelID, variant), true
	case learn.K2:
		// k2 is the saturation analyzer's alone: this analyzer reads the
		// capacity store, which that analyzer's forget clears.
		return 0, true
	}
	ctrl.Log.WithName("slo").Error(nil, "learner on learn.Learners has no forget decision in the slo analyzer; nothing forgotten",
		"learner", name, "namespace", namespace, "modelID", modelID, "variant", variant)
	return 0, false
}

// forgetLinesLocked forgets the variant's ITL line under every key: what the
// tracker holds of it (windows and intercepts are counted, as EvictStale
// counts them), and the engine arguments it was learned under
// (linesUnderArgs), so the next fit starts a new line rather than reading a
// change of arguments. The key's last-seen stamp goes too: the next Analyze
// sets it again. Callers hold a.mu; the tracker's lock is taken under it, as
// EvictStale takes it.
func (a *Analyzer) forgetLinesLocked(namespace, modelID, variant string) int {
	// The key shape of windowKey. The trailing separator keeps "v" from
	// matching "v2".
	prefix := namespace + "|" + modelID + "|" + variant + "|"
	match := func(k string) bool { return strings.HasPrefix(k, prefix) }
	windows, baselines := a.lines.Sweep(a.now(), match)
	for key := range a.args {
		if match(key) {
			delete(a.args, key)
		}
	}
	for key := range a.seen {
		if match(key) {
			delete(a.seen, key)
		}
	}
	return windows + baselines
}
