#!/usr/bin/env bash
# Decide what a benchmark run may file as its own measurement, and file it.
#
# This lived in the Makefile recipe, where nothing could test it -- and two
# defects landed there while the scripts beside it were covered by an executable
# check: pod timings were gated on the SAMPLES file's failure flag, so a valid
# timings file died with a bad samples file; and a refused start left no flag at
# all, so a previous run's file at the same path would have been filed as this
# run's measurement.
#
# The rule is the same for every artefact and is about the ARTEFACT, not about an
# exit status. An earlier version gated on the stop's exit code, and every way
# this can go wrong exits 0 -- a finalise conceded to nobody, a terminator append
# that hit a read-only /tmp, a stop whose own count said "unreadable" -- so the
# recipe announced "filed" over a file that does not parse.
#
#   file it when   it exists, is non-empty, its own capture did not report a
#                  failure, and (for JSON) it parses
#   say why not    otherwise, naming the file and the reason
#
# Usage:
#   file_capture.sh <results-dir> <samples-json> <controller-log>
#
# A missing results dir is not an error: a run that produced no results directory
# has nothing to file into, and saying so once is enough.
set -u

case "${1:-}" in
    -h|--help)
        sed -n '2,/^[^#]/p' "$0" | sed 's/^# \{0,1\}//; $d'
        exit 0
        ;;
esac

# ${1-}, not ${1:?}: the recipe passes an EMPTY results dir whenever its glob
# matched nothing, and `:?` fires on null as well as unset -- which aborted the
# whole of benchmark-run (report, plots, the load-generator check) with make
# Error 2 on the case the recipe itself calls the common one. The header and the
# guard below both say a missing results dir is not an error; `:?` made that
# guard unreachable.
RESULTS="${1-}"
SAMPLES="${2:?samples path required}"
WVALOG="${3:?controller log path required}"

filed=0
skipped=0

_parses() {
    python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$1" 2>/dev/null
}

# _file <source> <destination> <kind> <flag-base>
#
# kind is "json" or "text". flag-base is the CAPTURE the artefact came from, which
# is not always the artefact itself: the pod timings are `<samples>.pod_timings.json`
# while their capture's flags are `<samples>.startfailed`. Deriving the flag names
# from the artefact meant the timings consulted a name nothing ever writes, so a
# capture that never started had its samples withheld and a PREVIOUS run's timings
# filed as this run's measurement.
_file() {
    local src="$1" dst="$2" kind="$3" flags="${4:-$1}" base
    base="$(basename "$src")"

    if [ -f "$flags.startfailed" ]; then
        echo "  not filing $base: its capture never started"
        skipped=$((skipped + 1))
        return 0
    fi
    if [ -f "$flags.stopfailed" ]; then
        echo "  not filing $base: its capture could not be stopped, so the file is incomplete"
        skipped=$((skipped + 1))
        return 0
    fi
    if [ ! -s "$src" ]; then
        # Nothing was captured. Not a failure worth a warning on its own -- an
        # empty capture reads as "not measured" downstream, which is honest.
        return 0
    fi
    if [ "$kind" = json ] && ! _parses "$src"; then
        echo "  not filing $base: it does not parse as JSON, so it is not a usable measurement"
        skipped=$((skipped + 1))
        return 0
    fi
    mkdir -p "$(dirname "$dst")" 2>/dev/null || true
    if cp "$src" "$dst" 2>/dev/null; then
        echo "  filed $base -> $dst"
        filed=$((filed + 1))
    else
        echo "  not filing $base: could not copy it to $dst"
        skipped=$((skipped + 1))
    fi
}

if [ -z "$RESULTS" ] || [ ! -d "$RESULTS" ]; then
    echo "  no results directory yet, so nothing is filed"
    exit 0
fi

# Each artefact is judged on its own CONTENTS -- a valid timings file must not die
# with a corrupt samples file -- but both consult the flags of the capture they came
# from, which is the sampler for both of these.
_file "$SAMPLES" "$RESULTS/metrics/processed/wva_replica_samples.json" json "$SAMPLES"
_file "$SAMPLES.pod_timings.json" "$RESULTS/metrics/processed/wva_pod_timings.json" json "$SAMPLES"
_file "$WVALOG" "$RESULTS/wva_controller.log" text "$WVALOG"

echo "  capture artefacts: $filed filed, $skipped withheld"
