#!/usr/bin/env python3
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
from hack.benchmark.render_real_trace import load_bundle, _assemble
from pathlib import Path

bundle, report = load_bundle(Path('hack/benchmark/results/v040-guidellm-decode-heavy'))
print('requests:', bundle.requests is not None, len(bundle.requests) if bundle.requests else 0)
if bundle.requests:
    print('requests[0] keys:', list(bundle.requests[0].keys()))
    print('has arr_rate:', 'arr_rate' in bundle.requests[0])

d = _assemble(bundle)
print('reqs:', len(d['reqs']))
print('req_buckets:', len(d['req_buckets']) if d.get('req_buckets') else None)
