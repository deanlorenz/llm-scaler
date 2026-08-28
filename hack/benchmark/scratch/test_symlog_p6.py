"""Standalone p6 symlog demo — no bundle needed."""
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import matplotlib.ticker as ticker
import numpy as np

fig, ax = plt.subplots(figsize=(10, 3))
g = ax

# Simulate two analyzer lanes with replica-delta values
# throughput: ramps up (scale pressure +2,+4), then settles near 0
# saturation: mostly near 0, one spike to -3 on scale-down
t = np.linspace(0, 600, 200)

# throughput analyzer: big positive spike then decay
ys_tp = np.where(t < 100, 0,
         np.where(t < 150, (t - 100) / 50 * 8,       # ramp to +8
         np.where(t < 300, 8 * np.exp(-(t-150)/80),   # decay
         np.where(t < 400, 0.5 + 0.3*np.sin(t/20),   # noise near 0
         np.where(t < 450, -3 * np.ones_like(t),      # scale-down pressure
         0.1 * np.ones_like(t))))))                    # settled

# saturation analyzer: mostly flat, brief negative excursion
ys_sat = np.where(t < 200, 0,
          np.where(t < 220, -2 * np.ones_like(t),
          np.where(t < 400, 0.2 * np.sin(t/30),
          np.where(t < 460, -1 * np.ones_like(t),
          0.0 * np.ones_like(t)))))

g.plot(t, ys_tp,  color='#2563eb', lw=1.4, label='throughput', zorder=2.4)
g.plot(t, ys_sat, color='#dc2626', lw=1.4, label='saturation',  zorder=2.4)
g.axhline(0, color='#1f2328', lw=0.8, alpha=0.5, zorder=2.0)

g.set_yscale('symlog', linthresh=1, base=2)
# Show plain integers on ticks, not 2^n notation
g.yaxis.set_major_formatter(ticker.FuncFormatter(lambda v, _: f'{int(v):+d}' if v != 0 else '0'))
g.grid(which='major', axis='y', alpha=0.3, lw=0.6)

g.set_xlabel('seconds since run start', fontsize=9)
g.set_ylabel('replica-delta\n(rc−sc)/prc', fontsize=8)
g.set_title('6 · signed replica-delta per analyzer  (+ scale-up pressure / − scale-down pressure)', fontsize=9, loc='left')
g.legend(fontsize=8)

fig.tight_layout()
out = '/home/dean/code/llm-d/dean-llmd-scaler-sandbox/worktrees/benchmark-viz/hack/benchmark/scratch/p6-symlog-demo.png'
fig.savefig(out, dpi=130)
print(f'wrote {out}')
