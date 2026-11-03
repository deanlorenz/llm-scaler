package saturation

import (
	"math"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/shape"
)

// The KV a resident request holds, when some of its prompt was a prefix-cache
// hit (issue #277).
//
// shape.New discounts the prompt by the hit rate, ILeff = IL*(1 - hit), as if
// every hit saved KV. A hit saves compute, but it saves KV only when the block
// it hits is also held by another running request: a hit on a block a
// finished request left behind makes that block the new request's own, so it
// is resident as if it had been computed. With multi-turn reuse most hits are
// of that kind, and pricing the resident request at ILeff + D*OL/2 then
// over-counts the sequences a cache holds, up to about 4x. The derived mu
// (deriveMuAtCap) sizes its sequence count by that figure, so an over-counted
// count over-states mu and under-orders replicas.
//
// The engine says how many requests it holds (vllm:num_requests_running,
// sglang:num_running_reqs; domain.ReplicaMetrics.RunningRequests), and its
// cache says how many tokens they hold, so the KV per resident request is
// measured, not modelled: the tokens in use over the running requests, over
// the fleet's Ready generating replicas (fleetResidentKVPerRequest).
//
// The measurement only says how much of the hit discount is real. It is
// bounded below by the shape's KVreq, every hit saving KV, and above by the
// same shape with no hit at all, IL + D*OL/2, no hit saving any:
//
//   - a reading below the shape's KVreq is a request part way through its
//     prefill (resident, its prompt not yet all written), or a spread of
//     output lengths D under-states, and is not taken as more sharing than
//     the hit rate allows;
//   - a reading above IL + D*OL/2 is a prompt length that lags the workload,
//     which the shape tracker answers, not this.
//
// Where there is no reading -- fewer than minResidentSequences running across
// the fleet, a missing running-requests or KV series, no live KV capacity, or
// a shape change outstanding (the requests resident then belong to both
// shapes) -- the request is priced at IL + D*OL/2, no hit saving KV. That is
// the safer of the two choices the issue leaves open: the other, keeping
// ILeff, is the over-statement the issue is about. So with the switch on the
// derived mu is never above the one before it: a reading can take the hit
// discount back, never add to it. The cost is an over-order, by up to the hit
// discount, on a caching fleet that holds fewer than minResidentSequences
// requests, where the arrival rate the mu divides is small.
//
// It moves the derived mu and the GPS check that predicts a token rate from
// the same KV per request (noteLineMismatch). It does not move the prompt
// length prefill's throughput key is discounted by (throughputKeyInput): a hit
// does save prefill compute. Nor the derived k2 or the zero-replica estimate,
// which price the full prompt already (estimateCapacityAtCapSpread).

// SwitchKVPerRequestHitsResident is the replay switch for prefix-cache hits
// that stay resident (issue #277). On, the derived mu and the GPS check price
// a resident request at the KV the fleet's running requests are measured to
// hold, between ILeff + D*OL/2 and IL + D*OL/2, and at IL + D*OL/2 with no
// reading. Off, as before, at ILeff + D*OL/2.
const SwitchKVPerRequestHitsResident = "kv-per-request-hits-resident"

// minResidentSequences is the fewest running requests, over the fleet, the
// KV per resident request is measured from. Each resident request is caught
// at its own age, so one request reads anywhere from its prompt to its prompt
// and its whole generation. Over n requests the mean's spread falls as
// 1/sqrt(n): at IL = OL, about 7% of the KV per request at 8.
const minResidentSequences = 8

// fleetResidentKVPerRequest is the KV, in tokens, the fleet's running
// requests hold each: the tokens in use over the running requests, summed
// over the Ready generating replicas that read both now. ok is false with
// fewer than minResidentSequences running among them.
//
// Both halves are instant readings of the same scrape (KvUsageInstant,
// RunningRequests), so they describe the same requests. A replica lent by the
// warm pool is not counted, as the ITL line counts none (noteITL), and nor is
// one whose live capacity is unread or the collector's overflow sentinel
// (capacityTokensFor): its tokens in use are unknown.
func fleetResidentKVPerRequest(replicas []domain.ReplicaMetrics, rolesByVariant map[string]string) (float64, bool) {
	var tokens float64
	var running int
	for _, rm := range replicas {
		if !rm.Ready || rm.FromWarmPool || !generatesOutput(rm, rolesByVariant) {
			continue
		}
		if !rm.RunningRequestsKnown || !rm.KvUsageInstantKnown || rm.RunningRequests <= 0 {
			continue
		}
		if rm.TotalKvCapacityTokens <= 0 || rm.TotalKvCapacityTokens == math.MaxInt64 {
			continue
		}
		if !(rm.KvUsageInstant >= 0 && rm.KvUsageInstant <= 1) {
			continue
		}
		tokens += rm.KvUsageInstant * float64(rm.TotalKvCapacityTokens)
		running += rm.RunningRequests
	}
	if running < minResidentSequences {
		return 0, false
	}
	return tokens / float64(running), true
}

// withResidentKV is s with its KVreq set to the KV a resident request is
// measured to hold, bounded by s's own KVreq and the KVreq of s with no hit
// (see the file comment): measured where ok, and the bound with no hit
// otherwise. Only KVreq moves; ILeff stays the hit-discounted prompt the
// shape's other readers take.
func withResidentKV(s shape.Shape, measured float64, ok bool) shape.Shape {
	if !(s.KVreq > 0) {
		return s
	}
	// A literal's OutputDispersion of 0 reads as 1 (WithOutputDispersion).
	noHit := shape.New(s.AvgInputTokens, s.AvgOutputTokens, 0).WithOutputDispersion(s.OutputDispersion)
	kv := noHit.KVreq
	if ok && measured > 0 {
		kv = min(max(measured, s.KVreq), noHit.KVreq)
	}
	s.KVreq = kv
	return s
}
