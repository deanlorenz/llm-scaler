#!/usr/bin/env bash
# The shipped PrometheusRule parses, and the SLO alerts fire when they should
# and stay quiet when they should not.
#
# test/alerts checks that every alert names a metric that exists; it cannot
# evaluate an expression. A rule that errors at evaluation (a many-to-many join
# after a controller restart) or never fires (a gap that resets its for:) loads
# cleanly and looks exactly like a healthy model. promtool evaluates them on
# synthetic series. It needs no cluster.
#
#   1. promtool check rules: every expression in the shipped rule parses;
#   2. promtool test rules on test/alerts/testdata/slo-alerts.test.yaml: a judged
#      miss fires, a frozen or parked model and too few miss-minutes do not,
#      overlapping series from two controller pods still join, and a model
#      never judged raises WVAModelSLOUnjudged.
#
# PROMTOOL is the promtool binary (make promtool installs a pinned one); YQ is
# mikefarah yq v4.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROMTOOL=${PROMTOOL:-promtool}
YQ=${YQ:-yq}
RULE=${RULE:-$ROOT/config/components/prometheus-alerts/prometheusrule.yaml}
TESTS="$ROOT/test/alerts/testdata/slo-alerts.test.yaml"

if ! command -v "$PROMTOOL" >/dev/null 2>&1; then
  echo "FAIL: promtool not found (PROMTOOL=$PROMTOOL); run make promtool" >&2
  exit 1
fi
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# promtool reads plain rule files, not the PrometheusRule wrapper.
"$YQ" '{"groups": .spec.groups}' "$RULE" > "$WORK/full.yaml" || exit 1
N=$("$YQ" '[.groups[].rules[]] | length' "$WORK/full.yaml")
case "$N" in '' | *[!0-9]*) N=0 ;; esac
if [ "$N" -lt 1 ]; then
  echo "FAIL: no rules extracted from $RULE" >&2
  exit 1
fi
if ! "$PROMTOOL" check rules "$WORK/full.yaml"; then
  echo "FAIL: the shipped PrometheusRule does not parse" >&2
  exit 1
fi

# The tests compare labels and firing, not the annotation prose.
"$YQ" 'del(.groups[].rules[].annotations)' "$WORK/full.yaml" > "$WORK/rules.yaml" || exit 1
cp "$TESTS" "$WORK/slo-alerts.test.yaml"
if ! (cd "$WORK" && "$PROMTOOL" test rules slo-alerts.test.yaml); then
  echo "FAIL: an SLO alert fired, or stayed quiet, against its test" >&2
  exit 1
fi
echo "ok: alert rules parse and the SLO alert tests pass"
