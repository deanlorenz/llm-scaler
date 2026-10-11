package allocation

import (
	"math"
	"slices"
	"time"
)

const (
	// ShareHistoryMaxSamples is the most need samples a role's stored history
	// keeps. Samples are bucketed (NeedHistory) so that a swing window fits:
	// one per minute up to this many minutes of window, coarser beyond.
	ShareHistoryMaxSamples = 180
	// ShareHistoryMaxGap is the longest unobserved stretch a restored history
	// may end with and still count as covering its window. A restart's own gap
	// -- the pod's restart, the write interval -- is well inside it; a
	// controller down for longer missed whatever peaked meanwhile, so its
	// restored peaks are kept but not taken as the whole window's.
	ShareHistoryMaxGap = 10 * time.Minute
	// shareHistorySkew is how far in the future a stored timestamp may lie
	// and still be read: clocks across controller replicas differ a little.
	shareHistorySkew = time.Minute
)

// ShareNeedSample is one stored need reading.
type ShareNeedSample struct {
	At   time.Time
	Need float64
}

// ShareRoleNeedHistory is one role's stored need history: since when its need
// has been recorded, and its samples over the last swing window, oldest first.
type ShareRoleNeedHistory struct {
	Since   time.Time
	Samples []ShareNeedSample
}

// ShareNeedHistory is a ledger's need history by role, as it is stored across
// controller restarts (NeedHistory, RestoreNeedHistory). It is what PeakNeed
// and PeakKnown read: without it a restarted ledger knows no role's peak, and
// the planner takes every peak at its ceiling for a swing window.
type ShareNeedHistory map[string]ShareRoleNeedHistory

// shareHistoryBucket is the width one stored sample stands for: a minute, or
// coarser when a swing window holds more minutes than ShareHistoryMaxSamples.
func shareHistoryBucket(window time.Duration) time.Duration {
	b := time.Minute
	if per := (window + ShareHistoryMaxSamples - 1) / ShareHistoryMaxSamples; per > b {
		b = per
	}
	return b
}

// NeedHistory is the ledger's need history over the last swing window, for
// storage. Each bucket (a minute, or coarser for a long window) keeps only its
// highest sample, which is all PeakNeed reads, so at most
// ShareHistoryMaxSamples are kept per role.
func (l *ShareLedger) NeedHistory(now time.Time, tm ShareTimings) ShareNeedHistory {
	bucket := shareHistoryBucket(tm.SwingWindow)
	out := make(ShareNeedHistory, len(l.needsSince))
	for role, since := range l.needsSince {
		var samples []ShareNeedSample
		for _, s := range l.needs[role] {
			if now.Sub(s.at) > tm.SwingWindow || !shareNeedValid(s.need) {
				continue
			}
			if n := len(samples); n > 0 && samples[n-1].At.Truncate(bucket).Equal(s.at.Truncate(bucket)) {
				if s.need > samples[n-1].Need {
					samples[n-1] = ShareNeedSample{At: s.at, Need: s.need}
				}
				continue
			}
			samples = append(samples, ShareNeedSample{At: s.at, Need: s.need})
		}
		if len(samples) > ShareHistoryMaxSamples {
			samples = samples[len(samples)-ShareHistoryMaxSamples:]
		}
		out[role] = ShareRoleNeedHistory{Since: since, Samples: samples}
	}
	return out
}

// RestoreNeedHistory merges a stored need history into the ledger, as read back
// after a restart. A sample that is not a finite, non-negative need, that is
// older than the swing window, or that lies in the future is dropped, and at
// most ShareHistoryMaxSamples of the newest are taken per role. A role's Since
// is restored -- making its peak known once a window has passed since it --
// only when its history runs up to within ShareHistoryMaxGap of now: a longer
// gap was not observed, and a peak in it would be missed. A role not in the
// group is restored too; Retain forgets it if it does not return.
//
// It returns how many roles had their Since restored.
func (l *ShareLedger) RestoreNeedHistory(h ShareNeedHistory, now time.Time, tm ShareTimings) int {
	known := 0
	for role, rh := range h {
		samples := make([]shareNeedSample, 0, len(rh.Samples))
		for _, s := range rh.Samples {
			if !shareNeedValid(s.Need) || s.At.IsZero() || now.Sub(s.At) > tm.SwingWindow || s.At.Sub(now) > shareHistorySkew {
				continue
			}
			samples = append(samples, shareNeedSample{at: s.At, need: s.Need})
		}
		if len(samples) == 0 {
			continue
		}
		slices.SortFunc(samples, func(a, b shareNeedSample) int { return a.at.Compare(b.at) })
		if len(samples) > ShareHistoryMaxSamples {
			samples = samples[len(samples)-ShareHistoryMaxSamples:]
		}
		l.needs[role] = append(l.needs[role], samples...)
		slices.SortStableFunc(l.needs[role], func(a, b shareNeedSample) int { return a.at.Compare(b.at) })

		last := samples[len(samples)-1].at
		since := rh.Since
		if since.IsZero() || since.Sub(now) > shareHistorySkew || since.After(samples[0].at) && since.Sub(samples[0].at) > shareHistorySkew {
			// No honest start: the samples say the need was recorded at
			// least from the first of them.
			since = samples[0].at
		}
		if now.Sub(last) > ShareHistoryMaxGap {
			continue
		}
		if cur, ok := l.needsSince[role]; !ok || since.Before(cur) {
			l.needsSince[role] = since
		}
		known++
	}
	return known
}

// ForgetNeedHistory drops every role's need history, as when the stored
// history is flushed: each role's peak is unknown again until it has been
// recorded for a swing window.
func (l *ShareLedger) ForgetNeedHistory() {
	clear(l.needs)
	clear(l.needsSince)
}

// shareNeedValid reports whether need is a reading a peak may be taken from.
func shareNeedValid(need float64) bool {
	return !math.IsNaN(need) && !math.IsInf(need, 0) && need >= 0
}
