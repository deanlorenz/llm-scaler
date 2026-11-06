package metrics

import (
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// LegacyMetricNamesEnvVar turns the old metric names off. They are on unless
// this reads "false": a ScaledObject, alert or dashboard written against an old
// name has to keep working through the upgrade without anyone setting anything.
const LegacyMetricNamesEnvVar = "LEGACY_METRIC_NAMES"

// The metric prefixes. hack/rename leaves this file alone, so these two stay
// as they are written here while the constants in internal/constants move from
// the old prefix to the new one.
//
// Until that rename no metric has the new prefix, so this whole file publishes
// nothing. It starts to work when the rename lands and stops being needed one
// release later: delete this file and the withLegacyNames call in InitMetrics.
const (
	currentMetricPrefix = "llmsm_"
	legacyMetricPrefix  = "wva_"
)

// withLegacyNames returns a Registerer that registers with r and also publishes
// everything named with the current prefix under the legacy prefix: same type,
// same labels, same values, read at the same scrape. setting is the value of
// LegacyMetricNamesEnvVar.
//
// It works at scrape time rather than by registering a second collector per
// metric so that no call site changes, the two names cannot disagree, and a
// metric added tomorrow is covered. The second registry holds the same
// collectors and exists only to be gathered: the Registerer interface cannot
// read back what r holds, and gathering r itself from inside its own collection
// would recurse.
func withLegacyNames(r prometheus.Registerer, setting string) prometheus.Registerer {
	if setting == "false" {
		return r
	}
	mirror := prometheus.NewRegistry()
	r.MustRegister(legacyCollector{source: mirror})
	return mirroredRegisterer{Registerer: r, mirror: mirror}
}

// mirroredRegisterer registers every collector with the real registry and with
// the mirror.
type mirroredRegisterer struct {
	prometheus.Registerer
	mirror *prometheus.Registry
}

// Register registers c with both. An error from the real registry stops there,
// so callers see the errors they always saw.
func (m mirroredRegisterer) Register(c prometheus.Collector) error {
	if err := m.Registerer.Register(c); err != nil {
		return err
	}
	return m.mirror.Register(c)
}

// MustRegister registers each collector with both and panics on an error.
func (m mirroredRegisterer) MustRegister(cs ...prometheus.Collector) {
	for _, c := range cs {
		if err := m.Register(c); err != nil {
			panic(err)
		}
	}
}

// Unregister removes c from both.
func (m mirroredRegisterer) Unregister(c prometheus.Collector) bool {
	m.mirror.Unregister(c)
	return m.Registerer.Unregister(c)
}

// legacyCollector publishes what source gathers under the legacy names.
//
// Describe sends nothing, which makes it an unchecked collector: its set of
// metrics depends on what is registered and only a scrape can know it.
type legacyCollector struct {
	source prometheus.Gatherer
}

// Describe implements prometheus.Collector.
func (legacyCollector) Describe(chan<- *prometheus.Desc) {}

// Collect implements prometheus.Collector. A gather error is dropped here on
// purpose: the same collectors are gathered by the real registry in the same
// scrape, which reports it, and the old names must not be able to fail a scrape
// the new names pass. What was gathered is still published.
func (l legacyCollector) Collect(ch chan<- prometheus.Metric) {
	families, _ := l.source.Gather()
	for _, family := range families {
		suffix, ok := strings.CutPrefix(family.GetName(), currentMetricPrefix)
		if !ok {
			continue
		}
		help := fmt.Sprintf("Deprecated: use %s. %s", family.GetName(), family.GetHelp())
		desc := prometheus.NewDesc(legacyMetricPrefix+suffix, help, nil, nil)
		for _, m := range family.GetMetric() {
			ch <- legacyMetric{desc: desc, source: m}
		}
	}
}

// legacyMetric is one gathered series under another name.
type legacyMetric struct {
	desc   *prometheus.Desc
	source *dto.Metric
}

// Desc implements prometheus.Metric.
func (m legacyMetric) Desc() *prometheus.Desc { return m.desc }

// Write implements prometheus.Metric. The gathered series already holds its
// labels and its value, so it is handed over field by field: a protobuf message
// must not be copied by value.
func (m legacyMetric) Write(out *dto.Metric) error {
	out.Label = m.source.Label
	out.Gauge = m.source.Gauge
	out.Counter = m.source.Counter
	out.Summary = m.source.Summary
	out.Untyped = m.source.Untyped
	out.Histogram = m.source.Histogram
	out.TimestampMs = m.source.TimestampMs
	return nil
}
