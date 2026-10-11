package steadystate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/config"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/engines/allocation"
	"github.com/llm-d/llm-d-workload-variant-autoscaler/internal/metrics"
)

// The utilization-share need history survives a restart in one ConfigMap in
// the controller's namespace, one data key per group. Without it a restarted
// ledger knows no model's peak, and the planner takes every peak at its
// ceiling -- the headroom cap on -- for a whole swing window. Metrics cannot
// be the read-back store: they carry no writer and no honest age.
//
// Only the acting (leader) path writes it, at most once per
// shareHistoryInterval per group and only when the history changed, with the
// ConfigMap's resourceVersion as the concurrency check. Deleting the
// ConfigMap flushes the history: the next ledger built starts empty.
const (
	// shareHistoryConfigMap is the ConfigMap's name. The controller's Role
	// (config/base/rbac/utilization-share-history-role.yaml) grants get and
	// update on this name only.
	shareHistoryConfigMap = "wva-utilization-share-history"
	// shareHistoryVersion is the stored format's version. An entry of any
	// other version is ignored, not guessed at.
	shareHistoryVersion = 1
	// shareHistoryInterval is the least time between two writes of a group's
	// history, and between two reads retried after a failed one.
	shareHistoryInterval = time.Minute
	// shareHistoryMaxRoles is the most roles one group's entry stores.
	shareHistoryMaxRoles = 128
	// shareHistoryMaxBytes bounds the whole ConfigMap's data, below the API
	// server's 1 MiB object limit with room for its metadata.
	shareHistoryMaxBytes = 900 << 10
)

// errShareHistoryTooLarge is a write refused because the ConfigMap would
// outgrow shareHistoryMaxBytes.
var errShareHistoryTooLarge = errors.New("the utilization-share history ConfigMap would exceed its size bound")

// errShareHistoryDeleted is a write that found the ConfigMap deleted since it
// was read: an operator flushed the history.
var errShareHistoryDeleted = errors.New("the utilization-share history ConfigMap was deleted")

// shareHistoryEntry is one group's stored need history.
type shareHistoryEntry struct {
	Version int `json:"version"`
	// Group is the unsanitized group key: two groups whose keys sanitize
	// alike never read each other's history.
	Group     string    `json:"group"`
	WrittenAt time.Time `json:"writtenAt"`
	// SwingWindow is the window the history was kept over when written. An
	// entry is expired, and removed by the next write, once all of it lies
	// further back than this.
	SwingWindow        metav1.Duration             `json:"swingWindow"`
	ControllerInstance string                      `json:"controllerInstance,omitempty"`
	Roles              map[string]shareHistoryRole `json:"roles"`
}

// shareHistoryRole is a role's stored history: since when its need has been
// recorded, and its samples as [unix seconds, need].
type shareHistoryRole struct {
	Since   time.Time    `json:"since"`
	Samples [][2]float64 `json:"samples"`
}

// shareHistoryState is the store's memory between cycles. It is not reset
// with the ledgers: the ConfigMap outlives them.
type shareHistoryState struct {
	// cm is the ConfigMap as last read or written; nil when it must be read
	// before the next write (never read, absent, or a write conflicted).
	cm *corev1.ConfigMap
	// unread are the groups whose history could not be read when their
	// ledger was built, by when the read was last tried. Their history is
	// neither restored nor written until a read succeeds: a write would
	// replace the history it could not read.
	unread map[string]time.Time
	// lastWrite is when each group's history was last written (or tried),
	// and written what it was, so an unchanged history is not written again.
	lastWrite map[string]time.Time
	written   map[string]string
	// errs is each operation's last error text, so a standing failure is
	// logged once per change.
	errs map[string]string
	// truncated is how many roles each group's last write left out, so a
	// truncation is logged once per change.
	truncated map[string]int
}

// shareHistoryKey is the ConfigMap data key of a group's history: the
// controller instance (CONTROLLER_INSTANCE) and the group key, with every
// character a data key may not hold replaced, and a hash of the original, so
// distinct groups -- or two instances sharing a namespace and a group -- never
// share a key.
func shareHistoryKey(group string) string {
	id := group
	if inst := metrics.GetControllerInstance(); inst != "" {
		id = inst + "/" + group
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 200 {
		s = s[:200]
	}
	sum := sha256.Sum256([]byte(id))
	return s + "." + hex.EncodeToString(sum[:4])
}

// shareHistoryReader reads the ConfigMap uncached: the manager's cache holds
// only labelled ConfigMaps, or only the watched namespace, and either may
// exclude the controller's own.
func (e *Engine) shareHistoryReader() client.Reader {
	if e.apiReader != nil {
		return e.apiReader
	}
	return e.client
}

// reportShareHistoryErr logs a store operation's failure at Error level once
// per change of its error, and its recovery once.
func (st *utilizationShareState) reportShareHistoryErr(logger logr.Logger, op string, err error, msg string, kv ...any) {
	h := &st.history
	if h.errs == nil {
		h.errs = map[string]string{}
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	if text == h.errs[op] {
		return
	}
	prev := h.errs[op]
	h.errs[op] = text
	kv = append(kv, "configMap", shareHistoryConfigMap, "namespace", config.SystemNamespace())
	if err != nil {
		logger.Error(err, msg, kv...)
		return
	}
	if prev != "" {
		logger.Info("Utilization share: the need history store works again", append(kv, "operation", op)...)
	}
}

// readShareHistory reads the ConfigMap. An absent ConfigMap is no history: it
// returns nil and no error. Any other failure -- Forbidden included -- is an
// error, never an empty history.
func (e *Engine) readShareHistory(ctx context.Context) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	err := e.shareHistoryReader().Get(ctx, client.ObjectKey{Namespace: config.SystemNamespace(), Name: shareHistoryConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cm, nil
}

// restoreShareHistory reads a group's stored need history into its new ledger,
// before its first plan. On a failed read nothing is restored -- the ledger
// starts as it did without a store, its peaks unknown for a window -- the
// failure is logged, and the read is retried (saveShareHistory) until it
// succeeds.
func (e *Engine) restoreShareHistory(ctx context.Context, logger logr.Logger, group string, l *allocation.ShareLedger,
	now time.Time, tm allocation.ShareTimings) {
	st := &e.utilizationShare
	if e.client == nil {
		return
	}
	if st.history.unread == nil {
		st.history.unread = map[string]time.Time{}
	}
	cm, err := e.readShareHistory(ctx)
	st.reportShareHistoryErr(logger, "read", err,
		"Utilization share: could not read the stored need history; the models' peaks are unknown for a swing window "+
			"and their headroom is capped meanwhile. Retrying every minute; nothing is written until a read succeeds")
	if err != nil {
		st.history.unread[group] = now
		return
	}
	delete(st.history.unread, group)
	st.history.cm = cm
	if cm == nil {
		return
	}
	raw, ok := cm.Data[shareHistoryKey(group)]
	if !ok {
		return
	}
	h, entry, why := decodeShareHistory(raw, group)
	if why != "" {
		logger.Info("Utilization share: stored need history not used", "reason", why, "group", group)
		return
	}
	known := l.RestoreNeedHistory(h, now, tm)
	logger.Info("Utilization share: need history restored", "group", group, "roles", len(h),
		"peaksKnownFrom", known, "writtenAt", entry.WrittenAt, "writtenBy", entry.ControllerInstance)
}

// decodeShareHistory parses a stored entry. why is non-empty when the entry
// is not used at all.
func decodeShareHistory(raw, group string) (h allocation.ShareNeedHistory, entry shareHistoryEntry, why string) {
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return nil, entry, "unparseable: " + err.Error()
	}
	if entry.Version != shareHistoryVersion {
		return nil, entry, fmt.Sprintf("unknown version %d", entry.Version)
	}
	if entry.Group != group || entry.ControllerInstance != metrics.GetControllerInstance() {
		return nil, entry, fmt.Sprintf("written for group %q of controller instance %q", entry.Group, entry.ControllerInstance)
	}
	roles := slices.Sorted(maps.Keys(entry.Roles))
	if len(roles) > shareHistoryMaxRoles {
		roles = roles[:shareHistoryMaxRoles]
	}
	h = make(allocation.ShareNeedHistory, len(roles))
	for _, r := range roles {
		stored := entry.Roles[r]
		if len(stored.Samples) > allocation.ShareHistoryMaxSamples {
			stored.Samples = stored.Samples[len(stored.Samples)-allocation.ShareHistoryMaxSamples:]
		}
		rh := allocation.ShareRoleNeedHistory{Since: stored.Since, Samples: make([]allocation.ShareNeedSample, 0, len(stored.Samples))}
		for _, s := range stored.Samples {
			rh.Samples = append(rh.Samples, allocation.ShareNeedSample{At: time.Unix(int64(s[0]), 0), Need: s[1]})
		}
		h[r] = rh
	}
	return h, entry, ""
}

// encodeShareHistory is a group's entry, and the roles it left out to stay
// within shareHistoryMaxRoles.
func encodeShareHistory(group string, h allocation.ShareNeedHistory, now time.Time, tm allocation.ShareTimings) (raw string, dropped int, err error) {
	entry := shareHistoryEntry{
		Version: shareHistoryVersion, Group: group, WrittenAt: now.UTC(),
		SwingWindow: metav1.Duration{Duration: tm.SwingWindow}, ControllerInstance: metrics.GetControllerInstance(),
		Roles: map[string]shareHistoryRole{},
	}
	roles := slices.Sorted(maps.Keys(h))
	if len(roles) > shareHistoryMaxRoles {
		dropped = len(roles) - shareHistoryMaxRoles
		roles = roles[:shareHistoryMaxRoles]
	}
	for _, r := range roles {
		if len(h[r].Samples) == 0 {
			continue
		}
		sr := shareHistoryRole{Since: h[r].Since.UTC(), Samples: make([][2]float64, 0, len(h[r].Samples))}
		for _, s := range h[r].Samples {
			sr.Samples = append(sr.Samples, [2]float64{float64(s.At.Unix()), s.Need})
		}
		entry.Roles[r] = sr
	}
	b, err := json.Marshal(entry)
	return string(b), dropped, err
}

// shareHistoryContent is what decides whether an entry changed: everything but
// when it was written.
func shareHistoryContent(raw string) string {
	var entry shareHistoryEntry
	if json.Unmarshal([]byte(raw), &entry) != nil {
		return raw
	}
	entry.WrittenAt = time.Time{}
	b, _ := json.Marshal(entry)
	return string(b)
}

// shareHistoryExpired reports whether a stored entry holds nothing a restore
// could use any more: written longer than its swing window ago.
func shareHistoryExpired(raw string, now time.Time) bool {
	var entry shareHistoryEntry
	if json.Unmarshal([]byte(raw), &entry) != nil || entry.Version != shareHistoryVersion {
		return false // not ours to judge
	}
	return now.Sub(entry.WrittenAt) > entry.SwingWindow.Duration+allocation.ShareHistoryMaxGap
}

// saveShareHistory writes a group's need history when it changed, at most
// once per shareHistoryInterval. It runs only on the acting path: shadow mode
// keeps no ledger and writes nothing. A group whose history could not be read
// retries the read instead, and writes nothing until it succeeds.
func (e *Engine) saveShareHistory(ctx context.Context, logger logr.Logger, group string, tm allocation.ShareTimings, now time.Time) {
	st := &e.utilizationShare
	l := st.ledgers[group]
	if e.client == nil || l == nil {
		return
	}
	if tried, unread := st.history.unread[group]; unread {
		if now.Sub(tried) >= shareHistoryInterval {
			e.restoreShareHistory(ctx, logger, group, l, now, tm)
		}
		return
	}
	if at, ok := st.history.lastWrite[group]; ok && now.Sub(at) < shareHistoryInterval {
		return
	}
	raw, dropped, err := encodeShareHistory(group, l.NeedHistory(now, tm), now, tm)
	if err != nil {
		st.reportShareHistoryErr(logger, "encode", err, "Utilization share: could not encode the need history", "group", group)
		return
	}
	content := shareHistoryContent(raw)
	if st.history.written[group] == content {
		return
	}
	if st.history.lastWrite == nil {
		st.history.lastWrite, st.history.written, st.history.truncated = map[string]time.Time{}, map[string]string{}, map[string]int{}
	}
	st.history.lastWrite[group] = now
	if dropped != st.history.truncated[group] && dropped > 0 {
		logger.Info("Utilization share: need history truncated; the roles past the bound are not stored",
			"group", group, "droppedRoles", dropped, "maxRoles", shareHistoryMaxRoles)
	}
	st.history.truncated[group] = dropped
	key := shareHistoryKey(group)
	err = e.updateShareHistory(ctx, func(data map[string]string) bool {
		for k, v := range data {
			if k != key && shareHistoryExpired(v, now) {
				delete(data, k)
			}
		}
		data[key] = raw
		return true
	})
	if errors.Is(err, errShareHistoryDeleted) {
		// The flush: deleting the ConfigMap forgets the history, in memory as
		// well as stored -- rewritten from memory, the delete would undo
		// nothing. Every peak is unknown again for a window, as after a
		// restart with no store; the next write creates the ConfigMap afresh.
		for _, gl := range st.ledgers {
			gl.ForgetNeedHistory()
		}
		clear(st.history.written)
		logger.Info("Utilization share: the need history ConfigMap was deleted; every model's need history is forgotten",
			"configMap", shareHistoryConfigMap, "namespace", config.SystemNamespace())
		st.reportShareHistoryErr(logger, "write", nil, "")
		return
	}
	st.reportShareHistoryErr(logger, "write", err,
		"Utilization share: could not store the need history; a restart now forgets the models' peaks", "group", group)
	if err == nil {
		st.history.written[group] = content
	}
}

// deleteShareHistory removes a vanished group's history.
func (e *Engine) deleteShareHistory(ctx context.Context, logger logr.Logger, group string) {
	st := &e.utilizationShare
	if e.client == nil {
		return
	}
	delete(st.history.unread, group)
	delete(st.history.lastWrite, group)
	delete(st.history.written, group)
	delete(st.history.truncated, group)
	key := shareHistoryKey(group)
	err := e.updateShareHistory(ctx, func(data map[string]string) bool {
		if _, ok := data[key]; !ok {
			return false
		}
		delete(data, key)
		return true
	})
	st.reportShareHistoryErr(logger, "delete", err,
		"Utilization share: could not remove a vanished group's need history", "group", group)
}

// updateShareHistory applies mutate to the ConfigMap's data and writes it,
// creating the ConfigMap when absent. The write carries the resourceVersion it
// was based on; on a conflict -- another writer, a deleted ConfigMap -- it
// reads again and retries once, never more.
func (e *Engine) updateShareHistory(ctx context.Context, mutate func(map[string]string) bool) error {
	st := &e.utilizationShare
	var err error
	for range 2 {
		cm := st.history.cm
		if cm == nil {
			if cm, err = e.readShareHistory(ctx); err != nil {
				return err
			}
		}
		var obj *corev1.ConfigMap
		if cm != nil {
			obj = cm.DeepCopy()
		} else {
			obj = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Name: shareHistoryConfigMap, Namespace: config.SystemNamespace(),
				Labels: map[string]string{"app.kubernetes.io/component": "utilization-share-history",
					"app.kubernetes.io/part-of": "workload-variant-autoscaler"},
			}}
		}
		if obj.Data == nil {
			obj.Data = map[string]string{}
		}
		if !mutate(obj.Data) {
			st.history.cm = cm
			return nil
		}
		size := 0
		for k, v := range obj.Data {
			size += len(k) + len(v)
		}
		if size > shareHistoryMaxBytes {
			return fmt.Errorf("%w of %d bytes", errShareHistoryTooLarge, shareHistoryMaxBytes)
		}
		if obj.ResourceVersion == "" {
			err = e.client.Create(ctx, obj)
		} else {
			err = e.client.Update(ctx, obj)
		}
		if err == nil {
			st.history.cm = obj
			return nil
		}
		st.history.cm = nil
		if apierrors.IsNotFound(err) && obj.ResourceVersion != "" {
			// Deleted since it was read: the flush (saveShareHistory).
			return errShareHistoryDeleted
		}
		if !apierrors.IsConflict(err) && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return err
}
