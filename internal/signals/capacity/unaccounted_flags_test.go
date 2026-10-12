package capacity

import (
	"testing"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/inferenceengine"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
)

// The two breaks an adversarial review measured, as tests.
//
// Both had the same shape: Complete() came back true over a configuration the
// parser had not read, so the digest was a false equality and the gate that
// consults it waved the variant through. The property these pin is not "the
// flag is parsed" -- it is "a flag that was NOT read is recorded, so no
// sharing key is produced". Withholding a key is always the safe direction;
// producing one for two engines that merely failed to parse alike is not.
//
// Each test asserts the collision is gone AND that the honest case still
// shares, because a fix that simply stopped producing keys would pass the
// first half on its own.

// F1. parseArgsWith gives a valueless flag the next non-`--` token as its
// value, so a boolean CAN have a value that fails to resolve. When that
// happened the flag was neither applied nor recorded.
//
// Measured before the fix: `--enforce-eager $EXTRA_ARGS` produced
// EnforceEager=false, Unresolved=[], Complete()=true and the SAME digest as a
// deployment with no flag at all -- so one running with CUDA graphs off shared
// an inter-token-latency line with one running them on. EnforceEager is in the
// digest precisely because it changes that latency.
func TestABooleanWhoseSwallowedValueCannotResolveIsRecorded(t *testing.T) {
	// EXTRA_ARGS is not in the container's own env: in practice it arrives
	// through envFrom, which this package deliberately cannot read.
	swallowed := parseShell("vllm serve m --max-model-len 16384 --enforce-eager $EXTRA_ARGS", nil)
	if swallowed.Complete() {
		t.Errorf("Complete() is true over a boolean whose value could not be read, so "+
			"the digest claims to describe a CUDA-graph setting the parser never saw. "+
			"Unresolved=%v", swallowed.Unresolved)
	}

	none := parseShell("vllm serve m --max-model-len 16384", nil)
	if !none.Complete() {
		t.Fatalf("precondition: the no-flag case must still read as complete, "+
			"Unresolved=%v", none.Unresolved)
	}
	if swallowed.Fingerprint() == none.Fingerprint() {
		t.Error("an unreadable --enforce-eager still hashes the same as no flag at all, " +
			"so eager-on and eager-off would share one latency line")
	}

	// The honest case is untouched: a literal boolean is read and shares.
	literal := parseShell("vllm serve m --max-model-len 16384 --enforce-eager", nil)
	if !literal.Complete() {
		t.Errorf("a literal --enforce-eager must read as complete, Unresolved=%v",
			literal.Unresolved)
	}
	if literal.Fingerprint() == none.Fingerprint() {
		t.Error("eager on and eager off must not hash the same")
	}
}

// F2. The arg loop reads only `--` flags, so an engine's single-dash alias was
// neither applied nor recorded.
//
// Measured before the fix: `-q fp8` produced Quantization="", Unresolved=[],
// Complete()=true and the SAME digest as no flag -- pooling an FP8 and a BF16
// serving of one model onto one latency line, which EngineParams' own comment
// names as the thing a latency key must never do.
func TestAnEngineShortAliasIsRecorded(t *testing.T) {
	short := parseShell("vllm serve m --max-model-len 16384 -q fp8", nil)
	if short.Complete() {
		t.Errorf("Complete() is true over -q fp8, so an FP8 serving hashes as an "+
			"unquantized one. Unresolved=%v", short.Unresolved)
	}
	none := parseShell("vllm serve m --max-model-len 16384", nil)
	if short.Fingerprint() == none.Fingerprint() {
		t.Error("-q fp8 still hashes the same as no quantization flag")
	}

	// -tp too, which decides the parallel size the key is partly built from.
	tp := parseShell("vllm serve m --max-model-len 16384 -tp 2", nil)
	if tp.Complete() {
		t.Errorf("Complete() is true over -tp 2, Unresolved=%v", tp.Unresolved)
	}
}

// THE FALSE POSITIVE the first version of the F2 fix introduced, kept as a
// test because it cost nothing to find and would cost a fleet its sharing.
//
// Recording every single-dash token made `python3 -m sglang.launch_server`
// report `-m` unresolved -- the INTERPRETER's flag, not the engine's. The
// command line this parser is handed includes both and cannot tell them apart
// by shape, which is why the fix is a named set of engine aliases instead.
func TestTheInterpretersOwnFlagsAreNotTheEnginesFlags(t *testing.T) {
	d := shellDeployment("python3 -m "+sglangLauncher+" --model-path m "+
		"--mem-fraction-static 0.8 --page-size 64 --context-length 16384", nil)
	d.Spec.Template.Spec.Containers[0].Name = sglangContainer
	p := ParseEngineArgs(inferenceengine.EngineSGLang, scaletarget.NewDeploymentAccessor(d))
	for _, got := range p.Unresolved {
		if got == "m" {
			t.Errorf("`-m` from `python3 -m` was recorded as an unread engine flag, so "+
				"every SGLang deployment launched through the interpreter loses its "+
				"sharing key. Unresolved=%v", p.Unresolved)
		}
	}
}

// And the residue, asserted so it is a known limit rather than an assumption:
// a short alias that is NOT in the named set still passes unrecorded. Stated
// here so the next person to widen the set knows what it buys.
func TestAnUnknownShortAliasStillPassesUnrecorded(t *testing.T) {
	p := parseShell("vllm serve m --max-model-len 16384 -zz something", nil)
	if !p.Complete() {
		t.Skip("a wider policy now records unknown short flags; update this test's " +
			"premise, it recorded the old limit rather than a requirement")
	}
	if len(p.Unresolved) != 0 {
		t.Errorf("expected the unknown short alias to be unrecorded, got %v", p.Unresolved)
	}
}
