package capacity

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/utils/scaletarget"
)

// ExplicitMaxNumSeqs is the engine's cap only when its flag stated it. With
// no flag the parser assumes 256, and vLLM V1 picks 1024 on large
// accelerators: the ITL fit must not read a replica at 256 as capped there.
func TestExplicitMaxNumSeqs(t *testing.T) {
	for name, tc := range map[string]struct {
		params EngineParams
		want   int64
	}{
		"no flag":         {ParseVLLMArgs(scaletarget.NewDeploymentAccessor(makeTestDeployment("--gpu-memory-utilization=0.9"))), 0},
		"vLLM flag":       {ParseVLLMArgs(scaletarget.NewDeploymentAccessor(makeTestDeployment("--max-num-seqs=128"))), 128},
		"singular flag":   {ParseVLLMArgs(scaletarget.NewDeploymentAccessor(makeTestDeployment("--max-num-seq", "64"))), 64},
		"unreadable flag": {ParseVLLMArgs(scaletarget.NewDeploymentAccessor(makeTestDeployment("--max-num-seqs", "$UNSET_SEQS"))), 0},
		"a readable and an unreadable flag": {ParseVLLMArgs(scaletarget.NewDeploymentAccessor(makeTestDeployment(
			"--max-num-seqs", "512", "--max-num-seqs", "$UNSET_SEQS"))), 0},
		"through the env": {ParseVLLMArgs(scaletarget.NewDeploymentAccessor(makeDeploymentWithEnvAndArgs(
			[]corev1.EnvVar{{Name: "SEQS", Value: "96"}}, "--max-num-seqs", "$SEQS"))), 96},
	} {
		if got := tc.params.ExplicitMaxNumSeqs(); got != tc.want {
			t.Errorf("%s: ExplicitMaxNumSeqs = %d, want %d (MaxNumSeqs %d)", name, got, tc.want, tc.params.MaxNumSeqs)
		}
	}
	if got := (*EngineParams)(nil).ExplicitMaxNumSeqs(); got != 0 {
		t.Errorf("nil params: %d, want 0", got)
	}
}

// --max-num-active-seqs (vLLM v0.31+) stops the running batch below
// max_num_seqs, so the batch cap is the lower of the two (issue #308). Read in
// every form max_num_seqs is.
func TestMaxNumActiveSeqsIsTheBatchCap(t *testing.T) {
	parse := func(args ...string) EngineParams {
		return ParseVLLMArgs(scaletarget.NewDeploymentAccessor(makeTestDeployment(args...)))
	}
	shell := makeTestDeployment()
	shell.Spec.Template.Spec.Containers[0].Command = []string{
		"/bin/sh", "-c", "vllm serve m --max-num-seqs 512 --max-num-active-seqs 128"}
	for name, tc := range map[string]struct {
		params     EngineParams
		wantActive int64
		wantCap    int64
	}{
		"--x=v alone":                {parse("--max-num-active-seqs=64"), 64, 64},
		"--x v alone":                {parse("--max-num-active-seqs", "64"), 64, 64},
		"underscore spelling":        {parse("--max_num_active_seqs=48"), 48, 48},
		"below max_num_seqs":         {parse("--max-num-seqs=256", "--max-num-active-seqs=64"), 64, 64},
		"flag order does not matter": {parse("--max-num-active-seqs", "64", "--max-num-seqs", "256"), 64, 64},
		"the lower of the two":       {parse("--max-num-seqs=32", "--max-num-active-seqs=64"), 64, 32},
		"through the env": {ParseVLLMArgs(scaletarget.NewDeploymentAccessor(makeDeploymentWithEnvAndArgs(
			[]corev1.EnvVar{{Name: "ACTIVE", Value: "40"}}, "--max-num-active-seqs", "$ACTIVE"))), 40, 40},
		"in a shell command": {ParseVLLMArgs(scaletarget.NewDeploymentAccessor(shell)), 128, 128},
		// An active cap nothing could read may be below any max_num_seqs that
		// was: the cap is unknown.
		"unreadable":                    {parse("--max-num-seqs=256", "--max-num-active-seqs", "$UNSET_ACTIVE"), 0, 0},
		"zero is not a value vLLM runs": {parse("--max-num-seqs=256", "--max-num-active-seqs=0"), 0, 0},
		"not a number":                  {parse("--max-num-active-seqs=many"), 0, 0},
		"absent: max_num_seqs alone":    {parse("--max-num-seqs=256"), 0, 256},
	} {
		if got := tc.params.MaxNumActiveSeqs; got != tc.wantActive {
			t.Errorf("%s: MaxNumActiveSeqs = %d, want %d", name, got, tc.wantActive)
		}
		if got := tc.params.ExplicitMaxNumSeqs(); got != tc.wantCap {
			t.Errorf("%s: ExplicitMaxNumSeqs = %d, want %d", name, got, tc.wantCap)
		}
	}

	// An unreadable value is recorded, as one for max_num_seqs is, so the read
	// is not reported complete.
	if p := parse("--max-num-active-seqs", "$UNSET_ACTIVE"); p.Complete() {
		t.Errorf("an unreadable --max-num-active-seqs reads as complete: Unresolved=%v", p.Unresolved)
	}
	if p := parse("--max-num-active-seqs=64"); !p.Complete() {
		t.Errorf("a readable --max-num-active-seqs reads as incomplete: Unresolved=%v", p.Unresolved)
	}
}
