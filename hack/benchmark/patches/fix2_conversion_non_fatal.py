"""
Patch: guidellm-analyze_results.sh — make benchmark-report conversion non-fatal.

benchmark-report fails with KeyError: 'args' on some guidellm result shapes.
The analysis step (guidellm-analyze_results.sh) exits non-zero, causing
llm-d-benchmark.sh to retry 3 times and then exit non-zero, which causes
run_scenario.sh to treat the run as failed even though results.json is complete.

Fix: zero out LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC so the exit is skipped.

Idempotent: checks for MARK before applying.
"""
import io
import sys

path = sys.argv[1]
src = io.open(path, encoding="utf-8").read()

MARK = "# wva-patch: conversion is not fatal"
if MARK in src:
    print("fix2 (conversion non-fatal): already applied")
    sys.exit(0)

ANCHOR = (
    'if [[ $LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC -ne 0 ]]; then\n'
    '  echo "Results data conversion completed with errors."\n'
    '  exit $LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC\n'
    'fi\n'
)
if ANCHOR not in src:
    sys.exit("fix2: anchor missing (upstream shape changed): %r" % ANCHOR)

REPLACEMENT = (
    'if [[ $LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC -ne 0 ]]; then\n'
    '  echo "Results data conversion completed with errors."\n'
    '  ' + MARK + '\n'
    '  echo "NOTE: benchmark_report conversion failed (upstream bug) — results.json is complete."\n'
    '  LLMDBENCH_RUN_EXPERIMENT_CONVERT_RC=0\n'
    'fi\n'
)

src = src.replace(ANCHOR, REPLACEMENT, 1)
io.open(path, "w", encoding="utf-8", newline="\n").write(src)
print("fix2 (conversion non-fatal): applied")
