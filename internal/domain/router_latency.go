package domain

// LatencyBucket is one cumulative histogram bucket: the per-second rate of
// requests whose latency was at most UpperBound seconds. The +Inf bucket has
// UpperBound = math.Inf(1) and carries the total.
type LatencyBucket struct {
	UpperBound float64
	Rate       float64
}

// RouterLatency is one model's request latency as the router (the llm-d EPP)
// measured it, over WindowSeconds.
//
// The router's time to first token starts when the request reaches the router,
// BEFORE flow-control admission, so it is the one server-side TTFT that
// includes the router's queue -- the number a client's TTFT SLO is judged on.
// Only streaming requests are counted: for a non-streaming response the router
// records the whole request duration as its TTFT.
type RouterLatency struct {
	// WindowSeconds is the rate window the figures below were taken over.
	WindowSeconds float64

	// TTFTBuckets is the cumulative TTFT histogram, as per-second rates, in no
	// particular order. Empty when the router exports no TTFT series for the
	// model.
	TTFTBuckets []LatencyBucket

	// TPOTSumRate and TPOTCountRate are the rates of the streaming
	// time-per-output-token histogram's _sum and _count. Their ratio is the
	// mean TPOT over the window, request-weighted. Both zero when absent.
	TPOTSumRate   float64
	TPOTCountRate float64
}
