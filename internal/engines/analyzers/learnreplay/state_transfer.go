package learnreplay

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/saturation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/learnrec"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/prefill"
)

// transferPromptBin is the width of a TransferReading's prompt bin, in tokens.
const transferPromptBin = 500

// TransferReading is the decode pods' mean KV transfer time at one prompt length,
// over the rows with nothing waiting.
type TransferReading struct {
	// PromptTokens is the bin's centre, and Rows how many rows it holds.
	PromptTokens float64
	Rows         int
	// Seconds is the rows' mean AvgKVTransferTime.
	Seconds float64
}

// QuietTransfers reads a recording's transfer times by prompt length, shortest
// first, from decode rows with an empty queue. State's TransferSeconds is the last
// cycles', which on a warm-up that ends on a saturated prompt reads ten times the
// light-load figure: on the M2 warm-up 0.34 s against 0.031 s at 8000 tokens. A
// freeze prices a shape the warm-up's end may not be. Read, never fed back.
func QuietTransfers(recording []byte) ([]TransferReading, error) {
	rec, err := learnrec.Read(recording)
	if err != nil {
		return nil, err
	}
	bins := map[int]*TransferReading{}
	for _, ev := range rec.Events {
		if ev.Kind != saturation.KindAnalyze {
			continue
		}
		var d saturation.AnalyzeEvent
		if err := ev.DecodeData(&d); err != nil {
			return nil, fmt.Errorf("cycle input: %w", err)
		}
		in, err := decodeInput(d.Input)
		if err != nil {
			return nil, err
		}
		roles := rolesOf(in)
		for _, rm := range in.ReplicaMetrics {
			if domain.CanonicalRole(roles[rm.VariantName]) != domain.RoleDecode || !prefill.Serving(rm) ||
				rm.QueueLength != 0 || !rm.AvgKVTransferTimeKnown || !positive(rm.AvgKVTransferTime) ||
				!positive(rm.AvgInputTokens) {
				continue
			}
			key := int(math.Round(rm.AvgInputTokens / transferPromptBin))
			b := bins[key]
			if b == nil {
				b = &TransferReading{PromptTokens: float64(key * transferPromptBin)}
				bins[key] = b
			}
			b.Rows++
			b.Seconds += rm.AvgKVTransferTime
		}
	}
	out := make([]TransferReading, 0, len(bins))
	for _, b := range bins {
		b.Seconds /= float64(b.Rows)
		out = append(out, *b)
	}
	slices.SortFunc(out, func(x, y TransferReading) int { return cmp.Compare(x.PromptTokens, y.PromptTokens) })
	return out, nil
}
