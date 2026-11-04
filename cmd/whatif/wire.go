package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/analyzers/learnreplay"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/signals/whatif"
)

// The -o json wire format. The library carries no json tags
// (internal/signals/whatif); these types are the one place the planner's
// answers are given names on the wire, and schemaVersion says which names.
// A change to any field here is a change of schema: bump schemaVersion and
// the pinned key list in testdata/schema-v<N>.txt (TestJSONSchemaIsPinned).

// schemaVersion is the version of the JSON output's schema.
const schemaVersion = 1

// wireOutput is one command's whole JSON output.
type wireOutput struct {
	SchemaVersion int         `json:"schemaVersion"`
	Question      string      `json:"question"`
	Source        *wireSource `json:"source,omitempty"`
	Bands         *wireBands  `json:"bands,omitempty"`
	Roles         []wireRole  `json:"roles"`
	ExitCode      int         `json:"exitCode"`
	Note          string      `json:"note,omitempty"`
	// Error is a usage or internal error met before any answer, with -o
	// json: the output then holds only it, the question and the exit code.
	Error *wireError `json:"error,omitempty"`
}

// wireError is an error met before any answer.
type wireError struct {
	Message string `json:"message"`
}

// wireSource is what the answer was read from.
type wireSource struct {
	Recording  string `json:"recording"`
	Namespace  string `json:"namespace"`
	Model      string `json:"model"`
	FirstCycle int    `json:"firstCycle"`
	LastCycle  int    `json:"lastCycle"`
	// TotalCycles is how many cycles of the model the recording holds;
	// LastCycle is the one the answer was read at.
	TotalCycles int       `json:"totalCycles"`
	FirstAt     time.Time `json:"firstAt"`
	LastAt      time.Time `json:"lastAt"`
}

// wireBands is how the bands were drawn.
type wireBands struct {
	Method      string  `json:"method"`
	BlockCycles int     `json:"blockCycles"`
	Resamples   int     `json:"resamples"`
	Seed        uint64  `json:"seed"`
	LoPercent   float64 `json:"loPercent"`
	HiPercent   float64 `json:"hiPercent"`
}

// wireRole is one role's answer: for decode, one variant's.
type wireRole struct {
	Role    string      `json:"role"`
	Variant string      `json:"variant"`
	Inputs  *wireInputs `json:"inputs,omitempty"`
	Results wireResults `json:"results"`
}

// wireInputs is what a variant was priced from.
type wireInputs struct {
	Line           wireLine     `json:"line"`
	FillBand       wireInterval `json:"fillBand"`
	CapacityTokens float64      `json:"capacityTokens"`
	KVPerRequest   float64      `json:"kvPerRequest"`
	Shape          wireShape    `json:"shape"`
	BatchCap       int64        `json:"batchCap"`
	BatchCapSource string       `json:"batchCapSource"`
	Ranks          int          `json:"ranks"`
	Replicas       int          `json:"replicas"`
	ReplicasFrom   string       `json:"replicasFrom"`
	Headroom       *float64     `json:"headroom,omitempty"`
	Rate           *float64     `json:"rate,omitempty"`
	TPOT           *wireTPOT    `json:"tpot,omitempty"`
}

// wireShape is the traffic shape a variant was priced at: each length
// measured (the request-rate-weighted mean of the variant's last cycles with
// traffic, FromCycle to ToCycle) or assumed by a flag.
type wireShape struct {
	InputTokens   float64 `json:"inputTokens"`
	InputSource   string  `json:"inputSource"`
	OutputTokens  float64 `json:"outputTokens"`
	OutputSource  string  `json:"outputSource"`
	PrefixHitRate float64 `json:"prefixHitRate"`
	FromCycle     int     `json:"fromCycle"`
	ToCycle       int     `json:"toCycle"`
	Cycles        int     `json:"cycles"`
}

// The sources a shape's length comes from.
const (
	shapeMeasured = "measured"
	shapeAssumed  = "assumed"
)

// wireLine is the ITL line and its readings.
type wireLine struct {
	A            float64 `json:"a"`
	B            float64 `json:"b"`
	Tier         string  `json:"tier"`
	BorrowedFrom string  `json:"borrowedFrom,omitempty"`
	Readings     int     `json:"readings"`
	// ObservedFill is the range of fill the readings were taken at, absent
	// with no readings; FromCycle and ToCycle are the cycles they span.
	ObservedFill *wireInterval `json:"observedFill,omitempty"`
	FromCycle    int           `json:"fromCycle"`
	ToCycle      int           `json:"toCycle"`
	// Resamples is how many bootstrap resamples were drawn, and
	// TierChanges how many of them the tracker's choice of fit put at
	// another tier (TierUnstable above a tenth).
	Resamples   int `json:"resamples"`
	TierChanges int `json:"tierChanges"`
}

// wireTPOT is the TPOT question: the target (question 1 only), the
// percentile and R_q.
type wireTPOT struct {
	TargetSeconds float64 `json:"targetSeconds,omitempty"`
	Percentile    float64 `json:"percentile,omitempty"`
	Ratio         float64 `json:"ratio,omitempty"`
	RatioSource   string  `json:"ratioSource,omitempty"`
	Windows       int     `json:"windows"`
}

// wireResults is a role's results by name; a question fills its own.
type wireResults struct {
	Knee           *wireResult `json:"knee,omitempty"`
	OperatingRate  *wireResult `json:"operatingRate,omitempty"`
	MeanTPOT       *wireResult `json:"meanTPOT,omitempty"`
	PercentileTPOT *wireResult `json:"percentileTPOT,omitempty"`
	Rate           *wireResult `json:"rate,omitempty"`
}

// wireResult is an answer or a refusal, never both.
type wireResult struct {
	Answer  *wireAnswer  `json:"answer,omitempty"`
	Refused *wireRefusal `json:"refused,omitempty"`
}

// wireAnswer is whatif.Answer on the wire.
type wireAnswer struct {
	Value float64       `json:"value"`
	Unit  string        `json:"unit"`
	Band  *wireInterval `json:"band,omitempty"`
	Basis wireBasis     `json:"basis"`
	Flags []string      `json:"flags"`
}

// wireBasis is whatif.Basis on the wire; a field that does not apply is 0.
type wireBasis struct {
	Fill     float64  `json:"fill"`
	KneeRate float64  `json:"kneeRate"`
	Floor    float64  `json:"floor"`
	Uncapped float64  `json:"uncapped"`
	BoundBy  []string `json:"boundBy"`
	Tier     string   `json:"tier"`
}

// wireRefusal is whatif.Unanswerable on the wire.
type wireRefusal struct {
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// wireInterval is a range, Lo to Hi.
type wireInterval struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
}

// The units a result's value is in.
const (
	unitRate    = "r/s"
	unitSeconds = "s"
)

// toWire is a Result on the wire, in unit; nil for a question not asked.
func toWire(r whatif.Result, unit string) *wireResult {
	switch v := r.(type) {
	case whatif.Answer:
		a := &wireAnswer{Value: v.Value, Unit: unit, Flags: []string{}, Basis: wireBasis{
			Fill: v.Basis.Fill, KneeRate: v.Basis.KneeRate, Floor: v.Basis.Floor, Uncapped: v.Basis.Uncapped,
			BoundBy: []string{}, Tier: v.Basis.Tier,
		}}
		if v.Band != nil {
			a.Band = &wireInterval{Lo: v.Band.Lo, Hi: v.Band.Hi}
		}
		for _, b := range v.Basis.BoundBy {
			a.Basis.BoundBy = append(a.Basis.BoundBy, string(b))
		}
		for _, f := range v.Flags {
			a.Flags = append(a.Flags, string(f))
		}
		return &wireResult{Answer: a}
	case whatif.Unanswerable:
		return &wireResult{Refused: &wireRefusal{Reason: string(v.Reason), Detail: v.Detail}}
	}
	return nil
}

// newWireOutput starts a command's JSON output from the state it read.
func newWireOutput(question string, f *answerFlags, st *learnreplay.State, variants []pricedVariant, code int) wireOutput {
	r := f.resampling()
	return wireOutput{
		SchemaVersion: schemaVersion, Question: question, ExitCode: code, Roles: []wireRole{},
		Source: &wireSource{Recording: f.recording, Namespace: st.Namespace, Model: st.ModelID,
			FirstCycle: 1, LastCycle: st.Cycle, TotalCycles: st.Cycles, FirstAt: st.FirstAt, LastAt: st.At},
		Bands: &wireBands{Method: "block bootstrap", BlockCycles: r.Block, Resamples: r.Resamples, Seed: r.Seed,
			LoPercent: 10, HiPercent: 90},
		Note: footer(st, variants),
	}
}

// inputsOf is the wire's account of what a variant was priced from.
func inputsOf(f *answerFlags, pv pricedVariant) *wireInputs {
	in := &wireInputs{
		Line: wireLine{A: pv.decode.Line.A, B: pv.decode.Line.B, Tier: pv.state.Tier,
			BorrowedFrom: pv.state.BorrowedFrom, Readings: len(pv.state.LineReadings),
			Resamples: pv.lines.Drawn, TierChanges: pv.lines.TierChanges(),
			FromCycle: pv.state.ReadingsFrom, ToCycle: pv.state.ReadingsTo},
		FillBand:       wireInterval{Lo: pv.decode.Band.Lo, Hi: pv.decode.Band.Hi},
		CapacityTokens: pv.decode.CapacityTokens, KVPerRequest: pv.decode.KVPerRequest,
		Shape: wireShape{InputTokens: pv.shape.AvgInputTokens, InputSource: shapeMeasured,
			OutputTokens: pv.shape.AvgOutputTokens, OutputSource: shapeMeasured, PrefixHitRate: pv.shape.PrefixHitRate,
			FromCycle: pv.state.Shape.FromCycle, ToCycle: pv.state.Shape.ToCycle, Cycles: pv.state.Shape.Cycles},
		BatchCap: pv.perRank, BatchCapSource: pv.state.BatchCapSource,
		Ranks: max(pv.state.Ranks, 1), Replicas: pv.decode.Replicas, ReplicasFrom: "recording",
	}
	if pv.assumedIL {
		in.Shape.InputSource = shapeAssumed
	}
	if pv.assumedOL {
		in.Shape.OutputSource = shapeAssumed
	}
	if f.batchCap > 0 {
		in.BatchCapSource = "--batch-cap"
	}
	if f.decode > 0 {
		in.ReplicasFrom = "--decode"
	}
	if lo, hi, ok := pv.observedFill(); ok {
		in.Line.ObservedFill = &wireInterval{Lo: lo, Hi: hi}
	}
	return in
}

// writeJSON writes v indented, with a final newline.
func writeJSON(out io.Writer, v wireOutput) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
