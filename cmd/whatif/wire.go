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
	SchemaVersion int        `json:"schemaVersion"`
	Question      string     `json:"question"`
	Source        wireSource `json:"source"`
	Bands         wireBands  `json:"bands"`
	Roles         []wireRole `json:"roles"`
	ExitCode      int        `json:"exitCode"`
	Note          string     `json:"note"`
}

// wireSource is what the answer was read from.
type wireSource struct {
	Recording  string    `json:"recording"`
	Namespace  string    `json:"namespace"`
	Model      string    `json:"model"`
	FirstCycle int       `json:"firstCycle"`
	LastCycle  int       `json:"lastCycle"`
	FirstAt    time.Time `json:"firstAt"`
	LastAt     time.Time `json:"lastAt"`
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
	InputTokens    float64      `json:"inputTokens"`
	OutputTokens   float64      `json:"outputTokens"`
	PrefixHitRate  float64      `json:"prefixHitRate"`
	ShapeAssumed   bool         `json:"shapeAssumed"`
	BatchCap       int64        `json:"batchCap"`
	BatchCapSource string       `json:"batchCapSource"`
	Ranks          int          `json:"ranks"`
	Replicas       int          `json:"replicas"`
	ReplicasFrom   string       `json:"replicasFrom"`
	Headroom       *float64     `json:"headroom,omitempty"`
	Rate           *float64     `json:"rate,omitempty"`
	TPOT           *wireTPOT    `json:"tpot,omitempty"`
}

// wireLine is the ITL line and its readings.
type wireLine struct {
	A               float64  `json:"a"`
	B               float64  `json:"b"`
	Tier            string   `json:"tier"`
	BorrowedFrom    string   `json:"borrowedFrom,omitempty"`
	Readings        int      `json:"readings"`
	ObservedFillMax *float64 `json:"observedFillMax,omitempty"`
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
func newWireOutput(question string, f *answerFlags, st *learnreplay.State, code int) wireOutput {
	r := f.resampling()
	return wireOutput{
		SchemaVersion: schemaVersion, Question: question, ExitCode: code, Roles: []wireRole{},
		Source: wireSource{Recording: f.recording, Namespace: st.Namespace, Model: st.ModelID,
			FirstCycle: 1, LastCycle: st.Cycle, FirstAt: st.FirstAt, LastAt: st.At},
		Bands: wireBands{Method: "block bootstrap", BlockCycles: r.Block, Resamples: r.Resamples, Seed: r.Seed,
			LoPercent: 10, HiPercent: 90},
		Note: footer(st),
	}
}

// inputsOf is the wire's account of what a variant was priced from.
func inputsOf(f *answerFlags, pv pricedVariant) *wireInputs {
	in := &wireInputs{
		Line: wireLine{A: pv.decode.Line.A, B: pv.decode.Line.B, Tier: pv.state.Tier,
			BorrowedFrom: pv.state.BorrowedFrom, Readings: len(pv.readings)},
		FillBand:       wireInterval{Lo: pv.decode.Band.Lo, Hi: pv.decode.Band.Hi},
		CapacityTokens: pv.decode.CapacityTokens, KVPerRequest: pv.decode.KVPerRequest,
		InputTokens: pv.shape.AvgInputTokens, OutputTokens: pv.shape.AvgOutputTokens,
		PrefixHitRate: pv.shape.PrefixHitRate, ShapeAssumed: pv.assumed,
		BatchCap: pv.decode.ReplicaCap / int64(max(pv.state.Ranks, 1)), BatchCapSource: pv.state.BatchCapSource,
		Ranks: max(pv.state.Ranks, 1), Replicas: pv.decode.Replicas, ReplicasFrom: "recording",
	}
	if f.set["batch-cap"] {
		in.BatchCapSource = "--batch-cap"
	}
	if f.decode > 0 {
		in.ReplicasFrom = "--decode"
	}
	if top, ok := pv.observedFillMax(); ok {
		in.Line.ObservedFillMax = &top
	}
	return in
}

// writeJSON writes v indented, with a final newline.
func writeJSON(out io.Writer, v wireOutput) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
