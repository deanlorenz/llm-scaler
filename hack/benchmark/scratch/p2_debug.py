#!/usr/bin/env python3
"""Diagnostic: check why p2 step plot is empty."""
import sys, os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '../../..'))

from hack.benchmark.render_real_trace import load_bundle, _assemble
from pathlib import Path
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from matplotlib.ticker import MaxNLocator

bundle, _ = load_bundle(Path('hack/benchmark/results/guidellm-decode-heavy'))
d = _assemble(bundle)
reps = d['reps']
system = d['system']
req_buckets = d.get('req_buckets')
reqs = d['reqs']
pods = d['pods']

origins = [r['t'] for r in reps[:1]] + [s['t'] for s in system[:1]]
origins += [min(r['t_arr'] for r in reqs)] if reqs else []
origins += [min(b['t'] for b in req_buckets)] if req_buckets else []
for p in pods.values():
    if p.get('series'): origins.append(p['series'][0]['t'])
t0_raw = min(origins)
warmup = bundle.meta.get('warmup_offset_s') or bundle.meta.get('prewarm_s') or 0.0
t0 = t0_raw + warmup
last_dep = bundle.meta.get('last_departure_t')
t1 = float(last_dep) if last_dep else (max(b['t'] for b in req_buckets) if req_buckets else system[-1]['t'])
span = t1 - t0_raw

xs = [r['t'] - t0 for r in reps]
dz = [r.get('desired') for r in reps]
rz = [r.get('ready') for r in reps]
dz_off = [v + 0.05 if v is not None else None for v in dz]
rz_off = [v - 0.05 if v is not None else None for v in rz]

print('xs range:', xs[0], '..', xs[-1])
print('dz range:', min(dz), '..', max(dz))
print('span:', span, 'xlim:', -warmup, 'to', span - warmup)
print('None in dz_off:', any(v is None for v in dz_off))
print('None in rz_off:', any(v is None for v in rz_off))

fig, ax = plt.subplots(7, 1, figsize=(15, 19), sharex=True,
    gridspec_kw={'height_ratios': [3, 3, 2, 3, 2.5, 2.5, 2.2]})
c = ax[2]
c.step(xs, dz_off, where='post', color='blue', lw=2.2, label='desired')
c.step(xs, rz_off, where='post', color='purple', lw=2.2, label='ready')
c.set_ylim(bottom=0)
c.yaxis.set_major_locator(MaxNLocator(integer=True))
print('ylim BEFORE xlim loop:', c.get_ylim())
for axis in ax:
    axis.set_xlim(-warmup, span - warmup)
    axis.margins(x=0)
print('ylim AFTER  xlim loop:', c.get_ylim())
fig.savefig('hack/benchmark/scratch/p2_debug.png', dpi=80)
print('saved hack/benchmark/scratch/p2_debug.png')
