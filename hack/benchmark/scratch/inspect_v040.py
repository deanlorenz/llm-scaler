#!/usr/bin/env python3
"""Inspect v0.4.0 bundles from benchmark-extract."""
import json
from pathlib import Path

base = Path('/home/dean/code/llm-d/dean-llmd-scaler-sandbox/worktrees/benchmark-extract/hack/benchmark/results')

for run in sorted(p for p in base.iterdir() if p.is_dir()):
    meta = json.loads((run / 'meta.json').read_text())
    req_raw = json.loads((run / 'requests.json').read_text())
    reqs = req_raw if isinstance(req_raw, list) else req_raw.get('requests', [])
    req0 = reqs[0] if reqs else {}

    print(f'\n=== {run.name} ===')
    print(f'  meta sentinel fields:')
    print(f'    load_end_t={meta.get("load_end_t")}')
    print(f'    last_departure_t={meta.get("last_departure_t")}')
    print(f'    last_scale_event_t={meta.get("last_scale_event_t")}')
    print(f'    replica_scrape_end_t={meta.get("replica_scrape_end_t")}')
    print(f'  requests[0] keys: {list(req0.keys())}')
    print(f'  requests count: {len(reqs)}')

    # pods
    pods_raw = json.loads((run / 'pods.json').read_text())
    sos = pods_raw if isinstance(pods_raw, list) else pods_raw.get('scaled_objects', [])
    for so in sos[:1]:
        pods = so.get('pods') or []
        for p in pods[:1]:
            series = p.get('series') or []
            if series:
                s0 = series[0]
                hist_keys = [k for k in s0 if 'hist' in k]
                print(f'  pod series[0] keys: {list(s0.keys())}')
                if hist_keys:
                    print(f'  histogram fields: {hist_keys}')
        reps = so.get('replicas') or []
        print(f'  replicas count: {len(reps)}')
        if reps:
            print(f'  replicas[0]: {reps[0]}')
