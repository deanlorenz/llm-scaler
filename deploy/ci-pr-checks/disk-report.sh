#!/usr/bin/env bash
#
# Prints the disk left at the end of an e2e job and the lowest free space the
# job reached, from the samples deploy/ci-pr-checks/free-disk.sh records.
# Never fails the job.

set -u

df -h /
docker system df 2>/dev/null || true

samples=/tmp/free-disk-samples.txt
if [ ! -s "$samples" ]; then
    echo "disk-report: no free-space samples (free-disk.sh did not run, or its sampler died)"
    exit 0
fi
low="$(sort -k2,2n "$samples" | head -1)"
echo "disk-report: $(wc -l < "$samples") samples; lowest free space $(( ${low#* } / 1024 )) MiB at ${low%% *} UTC"
