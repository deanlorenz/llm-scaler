package saturation

import (
	"testing"
	"time"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/domain"
)

// servedRow is one cycle's row as noteServed reads it.
type servedRow struct {
	ready         bool
	readyAt       time.Time
	served, idle  bool
	wantFromCycle int // index of the cycle the clock starts at, -1 for none
}

func TestNoteServed_TheClockStartsAtTheFirstServedRowAfterAnIdleOne(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ready := t0.Add(-time.Hour)
	other := t0.Add(2 * time.Minute)
	cases := []struct {
		name string
		rows []servedRow
	}{
		{"idle, then routed to: the clock starts at the first served row", []servedRow{
			{true, ready, false, true, -1},
			{true, ready, false, true, -1},
			{true, ready, true, false, 2},
			{true, ready, true, false, 2},
		}},
		{"already serving at first sight: nothing says when its load began", []servedRow{
			{true, ready, true, false, -1},
			{true, ready, false, true, -1},
			{true, ready, true, false, -1},
		}},
		{"an idle run and return after serving does not restart it", []servedRow{
			{true, ready, false, true, -1},
			{true, ready, true, false, 1},
			{true, ready, false, true, 1},
			{true, ready, true, false, 1},
		}},
		{"a row whose rate was not read is no evidence of idle", []servedRow{
			{true, ready, false, false, -1},
			{true, ready, true, false, -1},
		}},
		{"a new Ready time is a new sighting", []servedRow{
			{true, ready, false, true, -1},
			{true, ready, true, false, 1},
			{true, other, false, true, -1},
			{true, other, true, false, 3},
		}},
		{"a row that is not Ready ends the sighting", []servedRow{
			{true, ready, false, true, -1},
			{false, ready, false, true, -1},
			{true, ready, true, false, -1},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newSettleMemo(t0)
			for i, r := range tc.rows {
				now := t0.Add(time.Duration(i) * 15 * time.Second)
				got := m.noteServed("v/p0", r.ready, r.readyAt, r.served, r.idle, now)
				want := time.Time{}
				if r.wantFromCycle >= 0 {
					want = t0.Add(time.Duration(r.wantFromCycle) * 15 * time.Second)
				}
				if !got.Equal(want) {
					t.Fatalf("cycle %d: clock from %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestRowServed_ReadsCompletionsAndTokens(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		rm                   domain.ReplicaMetrics
		wantServed, wantIdle bool
	}{
		{"a completion", domain.ReplicaMetrics{RequestRate: 0.1, RequestRateKnown: true}, true, false},
		{"tokens with no completion yet", domain.ReplicaMetrics{GenerationTokenRate: 30, RequestRateKnown: true}, true, false},
		{"known idle", domain.ReplicaMetrics{RequestRateKnown: true}, false, true},
		{"unread", domain.ReplicaMetrics{}, false, false},
	} {
		served, idle := rowServed(tc.rm)
		if served != tc.wantServed || idle != tc.wantIdle {
			t.Errorf("%s: served %v idle %v, want %v %v", tc.name, served, idle, tc.wantServed, tc.wantIdle)
		}
	}
}
