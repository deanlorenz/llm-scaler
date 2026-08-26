#!/usr/bin/env python3
"""Inspect v0.4.0 scaled_objects + pods series."""
import json
from pathlib import Path

base = Path('/home/dean/code/llm-d/dean-llmd-scaler-sandbox/worktrees/benchmark-extract/hack/benchmark/results')

for run in sorted(p for p in base.iterdir() if p.is_dir()):
    print(f'\n=== {run.name} ===')

    so_raw = json.loads((run / 'scaled_objects.json').read_text())
    sos = so_raw if isinstance(so_raw, list) else so_raw.get('scaled_objects', [])
    for so in sos:
        reps = so.get('replicas') or []
        print(f'  SO {so.get("so_id","?")} replicas={len(reps)}', end='')
        if reps:
            sources = set(r.get('source') for r in reps)
            print(f' sources={sources} first={reps[0]}', end='')
        print()

    pods_raw = json.loads((run / 'pods.json').read_text())
    pod_sos = pods_raw if isinstance(pods_raw, list) else pods_raw.get('scaled_objects', [])
    for so in pod_sos[:1]:
        for p in (so.get('pods') or [])[:1]:
            series = p.get('series') or []
            if series:
                s0 = series[0]
                hist_keys = [k for k in s0 if 'hist' in k]
                extra = [k for k in s0 if k not in ('t','run','wait','kv','ttft_ms')]
                print(f'  pod series extra keys: {extra}')
                if hist_keys:
                    h = s0[hist_keys[0]]
                    print(f'  {hist_keys[0]} sample: le[:4]={h["le"][:4]} n[:4]={h["n"][:4]}')
