package whatifsource

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// maxPointsPerQuery bounds one range query's steps: Prometheus refuses a
// query of more than 11,000 points per series, so a longer window is asked
// in parts and joined.
var maxPointsPerQuery = 10_000

// windowAPI is a Prometheus API that answers an instant query, at the step
// it is set to, from one range query over the whole window.
//
// The collector's Prometheus source issues instant queries (api.Query at
// the wall clock). A capture plays every step of a past window through that
// same source, so each PromQL expression is asked once, as a range query
// over the window at the capture's step, and every step's instant query is
// answered with the samples the range query evaluated at that step. A range
// query evaluates its expression at start + k x step exactly as an instant
// query at that time would: the answer is the one the collector would have
// read, had it run then.
//
// Only Query is answered. QueryRange is refused, since a range asked of
// the window would bypass its steps; every other method is the embedded
// API's, which is nil, so calling one panics. The collector calls none of
// them.
type windowAPI struct {
	promv1.API
	// prom is the real client: QueryRange is the only call made on it, and
	// a range query reads, never writes.
	prom  promv1.API
	start time.Time
	end   time.Time
	step  time.Duration

	mu   sync.Mutex
	at   time.Time
	memo map[string]*rangeAnswer
}

// rangeAnswer is one expression's range query, fetched once.
type rangeAnswer struct {
	once     sync.Once
	byTime   map[model.Time]model.Vector
	warnings promv1.Warnings
	err      error
}

func newWindowAPI(prom promv1.API, start, end time.Time, step time.Duration) *windowAPI {
	return &windowAPI{prom: prom, start: start, end: end, step: step, at: start,
		memo: map[string]*rangeAnswer{}}
}

// setStep sets the instant the next instant queries are answered at.
func (w *windowAPI) setStep(at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.at = at
}

// ranges is how many range queries were asked.
func (w *windowAPI) ranges() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.memo)
}

// Query answers an instant query at the step setStep set, whatever ts the
// caller passed (the collector passes the wall clock).
func (w *windowAPI) Query(ctx context.Context, query string, _ time.Time, _ ...promv1.Option) (model.Value, promv1.Warnings, error) {
	w.mu.Lock()
	a := w.memo[query]
	if a == nil {
		a = &rangeAnswer{}
		w.memo[query] = a
	}
	at := w.at
	w.mu.Unlock()
	a.once.Do(func() { a.byTime, a.warnings, a.err = w.fetch(ctx, query) })
	if a.err != nil {
		return nil, a.warnings, a.err
	}
	vec := a.byTime[model.TimeFromUnixNano(at.UnixNano())]
	if vec == nil {
		vec = model.Vector{}
	}
	return vec, a.warnings, nil
}

// fetch asks query over the window, in parts of at most maxPointsPerQuery
// steps, and indexes its samples by the instant they were evaluated at.
func (w *windowAPI) fetch(ctx context.Context, query string) (map[model.Time]model.Vector, promv1.Warnings, error) {
	out := map[model.Time]model.Vector{}
	var warnings promv1.Warnings
	chunk := time.Duration(maxPointsPerQuery-1) * w.step
	for from := w.start; !from.After(w.end); from = from.Add(chunk + w.step) {
		to := from.Add(chunk)
		if to.After(w.end) {
			to = w.end
		}
		val, warn, err := w.prom.QueryRange(ctx, query, promv1.Range{Start: from, End: to, Step: w.step})
		warnings = append(warnings, warn...)
		if err != nil {
			return nil, warnings, fmt.Errorf("range query %q: %w", query, err)
		}
		matrix, ok := val.(model.Matrix)
		if !ok {
			if val == nil {
				continue
			}
			return nil, warnings, fmt.Errorf("range query %q: a %s, not a matrix", query, val.Type())
		}
		for _, stream := range matrix {
			for _, p := range stream.Values {
				out[p.Timestamp] = append(out[p.Timestamp], &model.Sample{Metric: stream.Metric, Value: p.Value, Timestamp: p.Timestamp})
			}
		}
	}
	return out, warnings, nil
}

// errNotCaptured is a method of the Prometheus API a capture does not
// answer.
var errNotCaptured = errors.New("whatifsource: only instant queries are answered from a capture's window")

// QueryRange is refused: the collector asks instant queries only.
func (w *windowAPI) QueryRange(context.Context, string, promv1.Range, ...promv1.Option) (model.Value, promv1.Warnings, error) {
	return nil, nil, errNotCaptured
}
