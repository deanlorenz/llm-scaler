"""Build a self-contained comparison report (out/index.html) from the rendered
figures + summary table. Read-only over the sim: it just references the PNGs
already in out/ and parses out/summary.md.

Run:  ./.venv/bin/python report.py     (after run.py has produced the figures)

No server needed — open out/index.html directly (file://). Pure vanilla HTML/CSS/JS.
"""

import json
import os
import re

OUT = "out"

# All figures render at figsize=(11,15) dpi=120 -> 1320x1800 px. The Compare
# fit<->full slider interpolates pane image width between "fit to pane" and this.
FULL_W = 1320

# Scenario metadata mirrors run.py's outputs + the per-test framing. Strings are
# recalibrated to the sat_frac=0.85 results (see design-doc §6): all three complete
# 100%, so the story is the waiting-quality mix, NOT completion or peak-queue.
# Do NOT reintroduce the old "recovers completion / survives lag / overshoots"
# answers — they are false under this calibration.
SCENARIOS = [
    {"key": "ideal", "label": "Ideal",
     "stem": "01-ideal",
     "setup": "setup=0 · size to CENTERED demand rate (DR) × headroom (clairvoyant)",
     "answers": "what does good look like? → 100% served ≤2s; never queues on a smooth bump"},
    {"key": "static", "label": "No scaling",
     "stem": "07-static",
     "setup": "fixed fleet pinned at the shape's maxReplicaCount for the whole run · no autoscaler, pre-warmed (setup=0)",
     "answers": "what if you just provision for max and never scale? → 100% prompt (never queues on this "
                "bump), but the most expensive fleet (6001 rep·s ≈ 3.5× ideal) at the lowest utilisation "
                "(0.17) — promptness bought by paying for peak capacity through every valley"},
    {"key": "setup-lag", "label": "Setup lag",
     "stem": "02-setup-lag",
     "setup": "setup=90 · the SAME demand-tracking commands as ideal, landing 90s late",
     "answers": "does a correct policy survive 90s boot lag? → still completes 100%, but only ~36% served promptly "
                "(≤2s) and a 32s p90 wait. ⚠ confound: setup-lag→queue-aware changes TWO things at once (foresight "
                "lost, centered→trailing window, AND a backlog-drain term added) — not a clean A/B on the backlog "
                "term alone"},
    {"key": "queue-aware", "label": "Queue-aware",
     "stem": "03-queue-aware",
     "setup": "setup=90, drain_time=20 (the level-field backlog-drain deadline, shared with Qexp) · "
              "demand-tracking + backlog-drain (reactive, TRAILING)",
     "answers": "can a reactive backlog term rescue quality? → barely on promptness — 33% served ≤2s, roughly "
                "flat vs setup-lag's 36% — though it does lift the ≤15s share (55%→72%); and it worsens the p90 "
                "tail (32s→40s), chasing the backlog only after it has piled up during the boot. Reacting isn't "
                "enough — this is what motivates anticipation, see Qexp"},
    {"key": "qexp", "label": "Qexp (anticipatory)",
     "stem": "08-queue-aware-exp",
     "setup": "setup=90, drain_time=20, proj_setup=120 · anticipatory: a PERIODIC control loop that sizes to the "
              "backlog PEAK projected over the committed boot schedule (up now + pending at their estimated "
              "land-times), assuming a 120s boot lead (over-anticipates the true 90s). Reads only the observable "
              "queue LEVEL — no foresight of arrivals",
     "answers": "does anticipating the boot-window pile-up help? → decisively. Qexp serves 78% promptly (≤2s) vs "
                "reactive queue-aware's 33%, at essentially the same fleet cost (1920 vs 1872 prov·s, +3%), with a "
                "far shorter tail (p90 17.6s vs 40.2s) and a lower queue peak (428 vs 607). It orders sooner and "
                "HOLDS through the boot instead of chasing the queue after the fact — anticipation, not extra "
                "capacity, is what buys the quality. Still no foresight: it only projects the CURRENT queue forward "
                "(axis-2 dead-time compensation, not axis-1)"},
    {"key": "hpa-queue", "label": "HPA queue",
     "stem": "04-hpa-queue",
     "setup": "KEDA queue-depth · AverageValue target=1/replica → desired=ceil(Q) · setup=90, clamped to the shape's cap",
     "answers": "naive queue-depth scaling (target 1)? → 64% prompt with a real slow tail (6.6% failed, p90 52.6s); "
                "pins at the maxReplicaCount=10 cap and still burns ~1.8× the ideal fleet (3159 vs 1714 rep·s) — "
                "the cold-start backlog dominates the tail"},
    {"key": "hpa-concurrency", "label": "HPA concurrency",
     "stem": "05-hpa-concurrency",
     "setup": "KEDA running-count · AverageValue target c≈58/replica → desired=ceil(R/c) · setup=90, clamped to the shape's cap",
     "answers": "concurrency-only scaling? → catastrophic: the running-count signal is capacity-capped (R ≤ n·usable_C), "
                "so it is BLIND to the 2004-deep queue behind it, stalls at 4 replicas, 74% wait >60s. Concurrency alone "
                "cannot outrun boot lag"},
    {"key": "hpa-combined", "label": "HPA combined",
     "stem": "06-hpa-combined",
     "setup": "KEDA both triggers · desired=max(queue, concurrency) · up on either, down on both · setup=90, clamped to the shape's cap",
     "answers": "combining the two triggers (native KEDA max)? → the queue trigger rescues the concurrency blind spot; "
                "now the best-served fleet-heavy option (77% ≤2s, 95% ≤15s, p90 4.5s) at ~1.9× the ideal fleet "
                "(3242 rep·s), slightly beating queue-depth alone — the well-lit path's saturation+running pairing"},
]


# --------------------------------------------------------------------------
# Demand shapes (mirrors run.py's DEMO_SHAPES). `bump` first (reference), `spike`
# last (teaching-only). Short labels feed the picker buttons + gallery headings;
# SHAPE_NOTES carries the per-shape banner prose (light markdown; _md_inline'd for
# HTML, used verbatim in REPORT.md). Every scenario is rendered for every shape.
# --------------------------------------------------------------------------
SHAPES = [
    ("bump",      "Bump"),
    ("trapezoid", "Trapezoid"),
    ("stepup",    "Step up"),
    ("stepdown",  "Step down"),
    ("spike",     "Spike"),
]

# Per-shape banner. bump = the calibration reference (all narrative numbers are
# its); the three sustained shapes get a one-line "what this stresses" descriptor;
# spike is the teaching banner (autoscaling is the wrong tool for a 6s burst).
# NOTE: spike's concrete numbers are tuned to the actual capped run in verification
# — the prose here is number-free on purpose so it can't drift from the figures.
SHAPE_NOTES = {
    "bump": "**Bump** — a smooth triangular rise-and-fall (0 → peak → 0), the "
            "**calibration/reference** shape. Every constant (`drain_time=20`, "
            "`proj_setup=120`, `headroom=1.3`) is tuned here and the fleet fully "
            "drains at both ends. All narrative numbers in the Compare/Table prose "
            "are this shape's reference values.",
    "trapezoid": "**Trapezoid** — ramp up to a *sustained plateau* at peak, then "
                 "ramp down, over a low floor (≈ peak/3). Stresses the sizers in "
                 "long steady-state, not just a transient — the anticipation vs "
                 "reaction gap shows on both the up-ramp and the hold.",
    "stepup": "**Step up** — an abrupt jump from a low floor to a *sustained high "
              "plateau that never recedes*. Stresses how fast each policy closes "
              "the gap after a step and where it settles.",
    "stepdown": "**Step down** — starts high, drops to a *sustained low floor*. "
                "Stresses scale-**down** discipline: how much fleet-time each "
                "policy wastes before releasing capacity it no longer needs "
                "(the uncapped WVA desired peaks are highest on this shape — the "
                "reason the actuation cap matters most here).",
    "spike": "**Spike — a teaching case, NOT a calibration shape.** A ~6-second "
             "burst to 3× peak, far shorter than the 90 s replica boot. The "
             "bottleneck here is **boot lag, not the sizing algorithm**: by the time "
             "an ordered replica finishes booting, the burst is long over. So every "
             "*achievable* policy that must spin up capacity — reactive, "
             "anticipatory, and both KEDA baselines — drops **between 7% and 57%** "
             "of requests. The clairvoyant **ideal** line *does* survive (0% failed), "
             "but only because it boots instantly — a fiction no real cluster gets. "
             "The one real policy that absorbs the burst cleanly is **No scaling** "
             "pinned at the max: 0% failed, because the replicas are already warm — "
             "paid for with ~5× the steady-state resource-seconds and ~14% "
             "utilisation the rest of the time. The lesson: for a burst shorter than "
             "your boot time, autoscaling is the *wrong tool* — only standing "
             "pre-provisioned headroom absorbs it, and that headroom is exactly what "
             "you pay to be spike-proof. Exact numbers are in the per-shape Table.",
}


def fig_path(scen: dict, shape: str, kind: str = "main") -> str:
    """Compose a scenario's figure filename for a shape. kind ∈ {main, latency}.
    Mirrors run.py's `{stem}-{shape}.png` / `{stem}-{shape}-latency.png` output."""
    stem = scen["stem"]
    return f"{stem}-{shape}-latency.png" if kind == "latency" else f"{stem}-{shape}.png"

# Per-row "what it means" annotations for the Table tab. Keyed by the row's first
# cell (metric label, already stripped in summary.md). Percentile/family rows are
# matched by prefix so we don't enumerate every pNN.
ROW_MEANING = {
    # "offered" is the denominator for every quality %: dividing by it (not by
    # COMPLETED) is what guards against survivorship bias — a policy that strands
    # its slowest requests can't look good by counting only the survivors.
    "offered": "every request that arrived — the denominator (guards against survivorship bias)",
    "completed": "requests that finished within the run",
    "completed %": "completion rate — the 'did it finish at all' number",
    "unfinished": "still in system at trace end (permanently stranded)",
    # Cumulative "≤Ns %" rows are matched by the "≤" prefix in row_meaning()
    # below (labels/edges are dynamic, so we don't enumerate them here).
    "replica·seconds": "∫ READY replicas dt (accepting fleet only) — the usable-cost proxy",
    "provisioned·seconds": "∫ ALL billed replicas dt (booting + accepting + draining) — total fleet-time you pay for",
    "boot-lag waste·s": "provisioned − ready replica·seconds: capacity billed while booting or draining, never serving",
    "utilization": "delivered work ÷ usable capacity paid for; <1 idle fleet, ~1+ packed (packed can still fail latency — read next to the % bands)",
}


def row_meaning(label: str) -> str:
    if label in ROW_MEANING:
        return ROW_MEANING[label]
    if label.startswith("≤"):
        return ("cumulative — share of ALL offered served within this wait "
                "(the wait CDF sampled at this bound; rows climb toward 100%)")
    if label.startswith("failed"):
        return ("finished but slower than the last band edge — the slow tail on "
                "the OFFERED denominator (completed % − last ≤Ns %); unfinished "
                "requests are counted separately in the row above")
    if label.startswith("wait "):
        return "pre-service wait (dispatch − arrival), completed requests only"
    if label.startswith("time/work "):
        return ("time-in-system ÷ size — a slowdown proxy; informational only, NOT "
                "the scored signal (the bands score absolute wait, so short and long "
                "requests are held to the same 'promptly served' bar)")
    if label.startswith("replicas "):
        return "ready replica count over the run"
    return ""


# Glossary: the parameter/term definitions distilled from design-doc §2.6/§3.
GLOSSARY = [
    ("range vs interval",
     "A <b>range</b> is a lookback span (how far back a windowed average reaches, "
     "PromQL <code>metric[5m]</code>); an <b>interval</b> is a cadence (how often "
     "something recomputes/samples). Independent: average over 60s, decide every 15s."),
    ("the three meanings of “rate”",
     "<b>service_rate</b> = tokens/s one in-service request advances at (a backend "
     "property, fixed). <b>DR</b> (demand rate) = arrival_rate × E[size], tokens/s — "
     "a demand ESTIMATE, not a measurement. <b>measured throughput</b> = observed "
     "arrival/departure counts per second. Three different quantities the word "
     "“rate” gets loosely attached to; only measured throughput is one you actually "
     "observe directly."),
    ("DR — demand rate (was OWR)",
     "DR(t) = arrival_rate(t) × E[size], in <b>tokens/s</b> — the offered <i>work</i> "
     "rate, not requests/s (each request's work/size varies, so demand is measured in "
     "tokens). An <b>estimate</b>, not a measurement: arrival count is observable but a "
     "request's work (size) is not known at arrival. Valid as a proxy only under the "
     "<b>stationary-shape assumption</b> — arrival rate varies over time, the size "
     "distribution does not. (Named <code>owr</code> in the code / trace files.)"),
    ("C / sat_frac / usable ceiling",
     "<b>C</b> = raw per-backend concurrency limit (100 here). <b>sat_frac</b> = "
     "usable fraction (0.85); a backend saturates at the <b>usable ceiling</b> "
     "⌊sat_frac·C⌋ = 85 concurrent, a flat stand-in for the way real serving "
     "(vLLM) stops gaining goodput as concurrency climbs. Usable per-backend "
     "throughput = ⌊sat_frac·C⌋ × service_rate."),
    ("headroom",
     "Scale-up utilization target. headroom=1.3 sizes for ~1/1.3 ≈ 77% utilization, "
     "leaving slack for noise. Raw-hardware utilization ≈ sat_frac/headroom ≈ "
     "0.85/1.3 ≈ 65%."),
    ("sizing_range / decision_interval / drain_time",
     "<b>sizing_range</b> (60s) = the lookback the sizer averages DR over. "
     "<b>decision_interval</b> (15s) = how often it recomputes the desired count. "
     "<b>drain_time</b> = the deadline over which the backlog term aims to clear "
     "the current queue; used by both backlog-drain sizers at the same <b>20s</b> — "
     "a deliberate level-field rule (2026-08-05) so queue-aware and Qexp compare on "
     "identical drain aggression and only the reactive-vs-anticipatory difference shows."),
    ("setup / drain",
     "<b>setup</b> = boot lag, start→up (dead time; 90s for the lagged scenarios). "
     "<b>drain</b> = drain time, stop→down."),
    ("foresight — seeing future arrivals (axis 1)",
     "Whether a sizer can see arrivals that haven't happened yet. A <b>centered</b> "
     "window [t−r/2, t+r/2] averages future arrivals into the estimate; a "
     "<b>trailing</b> window [t−r, t] sees only the past. This is real foresight, "
     "and <b>only the clairvoyant ideal sizer has it</b> — no deployable controller "
     "can see the future. This is the one axis that separates the ideal from every "
     "real strategy."),
    ("setup / dead-time compensation (axis 2 — NOT foresight)",
     "Whether a sizer acts early enough to cover boot lag: it must aim at the "
     "demand it will face at t+setup and credit the replicas already booting, so it "
     "doesn't re-order the same backlog every interval (integral windup). A "
     "<b>real</b> controller does this WITHOUT foresight — by projecting the current "
     "queue/backlog trend forward, not by peeking at future arrivals. <b>Qexp</b> "
     "(the anticipatory scenario, built) is exactly this: no axis-1 foresight, only "
     "axis-2 dead-time compensation. Orthogonal to axis 1 — a sizer can have either, "
     "both, or neither."),
    ("Qexp — the anticipatory queue-aware sizer",
     "A <b>periodic control loop</b> (the <code>08-queue-aware-exp</code> scenario). "
     "Each tick it re-reads the observable state — backlog level, up capacity, and the "
     "replicas already booting with their estimated land-times — and rolls the backlog "
     "forward under that committed boot schedule. It sizes to the <b>PEAK</b> of that "
     "projected backlog (not the backlog measured now, and not its eventual residual), "
     "so it orders enough to cover the pile-up that WILL accumulate during the boot and "
     "then HOLDS through the boot instead of chasing the queue after the fact. Same "
     "backlog-drain idea as reactive queue-aware; the difference is projecting forward "
     "vs measuring now. No axis-1 foresight — it never sees future arrivals."),
    ("observability wall",
     "The real system exposes only the queue <b>LEVEL</b> (depth right now), never "
     "per-request departures or per-batch drain rates. So a sizer cannot track "
     "individual cohorts through the queue — it can only read the current level and "
     "react. Qexp respects this: it projects the CURRENT level forward and drives "
     "scale-down off the OBSERVED backlog dropping, not off a modelled departure "
     "schedule. This is what keeps it deployable rather than a paper policy."),
    ("proj_setup — the conservatism dial",
     "The boot lead the projection ASSUMES (distinct from <code>setup</code>, the boot "
     "lag the sim actually applies). Under-predict (proj_setup &lt; setup) → the loop "
     "anticipates less and drifts toward reactive; over-predict (&gt; setup) → it orders "
     "earlier and trades a little cost for a shorter tail. Crucially the loop is "
     "<b>self-correcting</b>: because it re-observes the true level every tick, it stays "
     "stable across the whole range and never DEPENDS on the assumption being right — "
     "proj_setup just tunes how conservative it is. In the sweep at headroom=1.3, "
     "<b>good% climbs as you over-predict</b> (70.7% at the honest 90 → 78% on a broad "
     "plateau around 120–135) and only <b>collapses if you over-predict too far</b> "
     "(35% at 180 — the projection orders so early it flaps); tail p90 improves across "
     "the same plateau. So it is a promptness-vs-tail-vs-cost knob with a wide safe "
     "band (the demo runs proj_setup=120), not a correctness knob."),
    ("quality bands",
     "Requests are scored by ABSOLUTE pre-service wait (not slowdown ratio): "
     "good ≤2s / almost ≤15s / mediocre ≤30s / meh ≤45s / bad ≤60s / failed >60s "
     "(good and failed pinned; the 2–60s middle is an even ramp). "
     "Percentages use the OFFERED denominator so bands + unfinished% sum to 100. "
     "The Table's <b>“≤Ns %” rows</b> and the <b>wait-CDF</b> figure show the same "
     "data <b>cumulatively</b> (share served <i>within</i> each bound, so each row "
     "climbs toward 100%); the stacked panel-1a figure shows the <b>exclusive</b> "
     "per-band shares. failed% = 100 − (≤60s %) − unfinished%."),
    ("goodput",
     "Throughput that actually meets the latency bar. Real serving throughput can "
     "keep rising while goodput collapses past a concurrency knee — which is why "
     "sat_frac caps the USABLE ceiling below raw C."),
    ("HPA/KEDA AverageValue formula",
     "The <code>04/05/06-hpa-*</code> scenarios are the well-lit KEDA path. Each "
     "trigger uses <b>metricType: AverageValue</b> (a per-replica target), so "
     "<code>desired = ceil(total_metric / per_replica_target)</code> and the current "
     "replica count <b>cancels out</b> — the sizer is stateless in n. Queue-depth "
     "target 1 → <code>ceil(Q)</code>; running-count target c → <code>ceil(R/c)</code>. "
     "These are <b>closed-loop</b>: they read the ACTUAL simulated queue/running "
     "signal, trailing-averaged over a 60s window (<code>avg_over_time</code>), decided "
     "every 15s — no foresight, purely reactive. Empty signal → <b>hold</b> at current n "
     "(cold start → 1); clamped to <b>[minReplicaCount, maxReplicaCount]</b> = [1, 10]."),
    ("KEDA multi-trigger combine (max)",
     "With multiple triggers KEDA takes the <b>max</b> of each trigger's desired count: "
     "scale <b>up on either</b>, <b>down only when both</b> agree lower. This is native "
     "behaviour, not custom logic — the <code>06-hpa-combined</code> scenario is exactly "
     "<code>max(ceil(Q), ceil(R/c))</code>. It is why the well-lit path pairs a "
     "saturation/queue trigger with a running-count trigger: the queue trigger covers "
     "the running-count signal's capacity-capped blind spot (see 05)."),
]


# --------------------------------------------------------------------------
# Shared narrative prose — the ONE source for the handcrafted framing text.
# Rendered into BOTH the HTML report (header + Table view) and REPORT.md so the
# two never drift. Written in light markdown (**bold**, `code`); _md_inline()
# converts to HTML, REPORT.md consumes it verbatim. Edit here → re-run report.py.
# --------------------------------------------------------------------------
INTRO = (
    "One request trace, several sizing approaches. **Every figure is the actual "
    "simulated execution** — a scaling *policy* only changes the supply trace; the "
    "graphs always reflect what really happened. Calibration is anchored to a real "
    "WVA decode-heavy benchmark: peak ~24 req/s, ~1000-token mean work, per-backend "
    "concurrency `C=100`, `service_rate ≈ 83` tokens/s (one backend clears ~8.3 "
    "req/s), usable ceiling `⌊0.85·C⌋ = 85` concurrent, and a **90 s replica boot** "
    "for the lagged scenarios."
)
STORY_NOTE = (
    "**Every scenario completes 100% of requests.** The story is *not* completion — "
    "it is the **waiting-time quality mix** (how prompt service was) and the **cost** "
    "(`replica·seconds` of fleet-time). A policy can \"finish everything\" and still "
    "be terrible, or be perfectly prompt and burn 3× the fleet."
)
READINGS = (
    "Readings: the **ideal** clairvoyant sizer is the only one that sees future "
    "arrivals — 100% prompt at the lowest real cost, the reference everything else is "
    "measured against. **No scaling** is also 100% prompt but pins at the max and "
    "burns ~3.5× the ideal fleet at the lowest utilisation (0.17) — promptness bought "
    "by paying for peak through every valley. **Setup-lag → queue-aware → Qexp** is the "
    "deployable-sizer progression under 90s boot: a correct policy landing 90s late is "
    "only ~36% prompt (≤2s); adding a **reactive** backlog term (queue-aware, "
    "drain_time=20) barely moves promptness — 33% ≤2s, roughly flat — lifting only the "
    "≤15s share (55%→72%) while worsening the p90 tail (32s→40s), because it chases the "
    "queue after the pile-up. **Qexp** — the same backlog-drain idea but **anticipatory**, "
    "sizing to the projected backlog peak — is the breakthrough: **78% prompt** (≤2s), "
    "89% within 15s, p90 17.6s, at essentially the same fleet cost as reactive "
    "queue-aware (1920 vs 1872 prov·s). Anticipation, not extra capacity, is what buys "
    "the quality. Among the fleet-heavy KEDA options, **hpa-combined** is prompt (77% "
    "≤2s, 95% within 15s) and **hpa-queue** middling (64% ≤2s, a 6.6% failed tail), both "
    "at ~1.8–1.9× the ideal fleet. **hpa-concurrency** is catastrophic — 74% wait over a "
    "minute — because its signal is capacity-capped and blind to the queue; the KEDA "
    "`max` in **hpa-combined** is what rescues that blind spot."
)
# Compact "story in one table" row subset (exact labels as they appear in
# summary.md). The quality rows are now the CUMULATIVE "served within Ns" CDF
# points (≤2s prompt … ≤60s within-a-minute); rows absent from summary.md are
# skipped. failed% is not a row anymore (= 100 − ≤60s − unfinished) but the
# ≤60s row and unfinished carry the same information.
STORY_ROWS = [
    "≤2s %", "≤15s %", "≤45s %", "≤60s %", "unfinished",
    "wait avg (s)", "wait p95 (s)", "replicas max", "replica·seconds",
    "provisioned·seconds", "utilization",
]


def parse_md_table(path):
    """Parse a GitHub-style pipe table into (headers, rows). Skips the --- rule."""
    if not os.path.exists(path):
        return [], []
    rows = []
    for line in open(path):
        line = line.strip()
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if all(set(c) <= set("-: ") for c in cells):   # separator rule
            continue
        rows.append(cells)
    if not rows:
        return [], []
    return rows[0], rows[1:]


def esc(s):
    return (s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;"))


def section_of(label: str) -> str:
    """Group summary rows into labelled sections for the Table view."""
    if label in {"offered", "completed", "completed %", "unfinished"}:
        return "Volume & completion"
    if label.startswith("≤") or label.startswith("failed"):
        return "Waiting-time quality mix — cumulative % of offered served within"
    if label.startswith("wait "):
        return "Waiting time before service (s)"
    if label.startswith("time/work "):
        return "Time per work unit (s/unit) — informational, not scored"
    if (label.startswith("replicas ")
            or label in {"replica·seconds", "provisioned·seconds",
                         "boot-lag waste·s", "utilization"}):
        return "Fleet & cost"
    return ""


# cell shading — semi-transparent so the number stays legible on any theme.
_C_GOOD = "background:rgba(22,163,74,0.15)"     # green
_C_MEH = "background:rgba(245,158,11,0.16)"     # amber
_C_BAD = "background:rgba(239,68,68,0.15)"      # red


def _direction(label: str):
    """+1 = higher is better, −1 = lower is better, None = don't shade."""
    l = label.strip().lower()
    if l.startswith("≤"):                       # cumulative "served within Ns" — higher better
        return +1
    if l.startswith("failed"):                  # slow tail (>last edge) — lower better
        return -1
    if l.startswith(("wait ", "time/work ")):
        return -1
    if (l.startswith("replicas ")
            or l in {"unfinished", "replica·seconds", "provisioned·seconds",
                     "boot-lag waste·s"}):
        return -1
    if l in {"completed %", "completed"}:
        return +1
    return None                                 # offered, utilization, etc. — neutral


def _num(cell: str):
    try:
        return float(str(cell).replace(",", "").strip())
    except ValueError:
        return None


def _row_cell_styles(label, cells):
    """Direction-aware green/amber/red for one row's scenario cells. Best value
    → green, worst → red, linearly graded between; all-equal rows stay neutral."""
    d = _direction(label)
    vals = [_num(c) for c in cells]
    nums = [v for v in vals if v is not None]
    if d is None or len(nums) < 2 or max(nums) == min(nums):
        return ["" for _ in cells]
    lo, hi = min(nums), max(nums)
    out = []
    for v in vals:
        if v is None:
            out.append("")
            continue
        frac = (v - lo) / (hi - lo)             # 0..1
        score = frac if d > 0 else (1.0 - frac)  # 1 = best
        out.append(_C_GOOD if score >= 0.66 else
                   (_C_MEH if score >= 0.33 else _C_BAD))
    return out


def render_table_html(headers, rows):
    if not headers:
        return "<p>(no summary.md found — run run.py first)</p>"
    ncol = len(headers) + 1
    th = "".join(f"<th>{esc(h)}</th>" for h in headers)
    th += '<th class="mean">what it means</th>'
    body = []
    cur = None
    for r in rows:
        sec = section_of(r[0])
        if sec and sec != cur:
            body.append(f'<tr class="sec"><td colspan="{ncol}">{esc(sec)}</td></tr>')
            cur = sec
        styles = _row_cell_styles(r[0], r[1:])
        tds = f"<td>{esc(r[0])}</td>"
        for c, st in zip(r[1:], styles):
            style = f' style="{st}"' if st else ""
            tds += f"<td{style}>{esc(c)}</td>"
        tds += f'<td class="mean">{esc(row_meaning(r[0]))}</td>'
        # The slow-tail row (failed >60s) is styled dark-red + ruled off from the
        # cumulative served-within bands above it (item: Table failed-row emphasis).
        rcls = ' class="failrow"' if r[0].startswith("failed") else ""
        body.append(f"<tr{rcls}>{tds}</tr>")
    return (f'<table class="sum"><thead><tr>{th}</tr></thead>'
            f'<tbody>{"".join(body)}</tbody></table>')


def render_tables_by_shape_html(out_dir=OUT) -> str:
    """Table tab: a shape picker (JS-populated, class `shapepick`) plus one
    server-rendered summary table per shape, each wrapped in a `data-shape` div
    that the picker toggles. bump is visible by default; the rest are hidden until
    selected. A missing summary-<shape>.md shows a hint in that shape's div."""
    blocks = ['<div class="pick shapepick" data-shape-for="table"></div>']
    for key, label in SHAPES:
        headers, rows = parse_md_table(os.path.join(out_dir, f"summary-{key}.md"))
        if headers:
            inner = render_table_html(headers, rows)
        else:
            inner = (f'<p class="tnote">(no <code>summary-{esc(key)}.md</code> — run '
                     f'<code>python run.py</code> first)</p>')
        hide = "" if key == "bump" else ' style="display:none"'
        note = SHAPE_NOTES.get(key)
        banner = f'<p class="sw-note">{_md_inline(note)}</p>' if note else ""
        blocks.append(f'<div data-shape="{esc(key)}"{hide}>'
                      f'<h3 class="sw">{esc(label)}</h3>{banner}{inner}</div>')
    return "".join(blocks)


def render_glossary_html():
    items = "".join(
        f"<dt>{term}</dt><dd>{definition}</dd>" for term, definition in GLOSSARY)
    return f'<dl class="gloss">{items}</dl>'


# ---- shared-prose converters (light markdown <-> html, both directions) ----
def _md_inline(s: str) -> str:
    """Render the light-markdown narrative constants (**bold**, `code`, *em*) to
    HTML. Text is otherwise trusted (our own constants), so no escaping."""
    s = re.sub(r"\*\*(.+?)\*\*", r"<b>\1</b>", s)
    s = re.sub(r"`(.+?)`", r"<code>\1</code>", s)
    s = re.sub(r"(?<!\*)\*(?!\*)(.+?)(?<!\*)\*(?!\*)", r"<i>\1</i>", s)
    return s


def _html_to_md(s: str) -> str:
    """Render the HTML-authored GLOSSARY definitions down to markdown inline."""
    s = re.sub(r"</?b>", "**", s)
    s = re.sub(r"</?i>", "*", s)
    s = re.sub(r"<code>(.*?)</code>", r"`\1`", s)
    return s


def _md_table(headers, rows) -> str:
    out = ["| " + " | ".join(headers) + " |",
           "|" + "---|" * len(headers)]
    for r in rows:
        out.append("| " + " | ".join(r) + " |")
    return "\n".join(out)


def _subset(rows, labels):
    """Rows whose first cell is in `labels`, in `labels` order; missing skipped."""
    by = {r[0]: r for r in rows}
    return [by[l] for l in labels if l in by]


# Sweep heading (substring) → the line-plot PNG sweep.py renders for it. The
# figure is embedded right under its ### heading, above the numeric table.
SWEEP_FIGS = {
    "setup (boot lag) sweep": "11-sweep-setuplag.png",
    "drain_time aggression": "12-sweep-drain.png",
    "proj_setup dial": "13-sweep-qexp.png",
    "headroom — static per-replica margin": "14-sweep-headroom.png",
    "headroom × drain_time": "15-sweep-headroom-drain.png",
    "headroom × proj_setup": "16-sweep-headroom-proj.png",
}

# The cap sweep is rendered per shape behind a switcher (not stacked). Order +
# membership mirror sweep.py's CAP_SHAPES (the sustained shapes where the ceiling
# bites the work-rate sizers; bump/spike are cap-inert and omitted there).
CAP_SWEEP_SHAPES = ["trapezoid", "stepup", "stepdown"]

# Short jump-nav labels for the Sweeps tab, matched against each ### heading by
# substring (the headings themselves are long). Cap sweep is labelled separately.
_NAV_LABELS = [
    ("setup (boot lag)", "Setup-lag"),
    ("drain_time aggression", "Queue-aware"),
    ("proj_setup dial", "Qexp"),
    ("headroom — static", "Headroom"),
    ("headroom × drain_time", "Head × drain"),
    ("headroom × proj_setup", "Head × proj"),
    ("ρ note", "ρ note"),
]


def _slug(s: str) -> str:
    """Stable DOM id for a heading — 'sw-' + lowercased alnum runs joined by '-'."""
    return "sw-" + re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


def _nav_label(heading: str) -> str:
    for key, label in _NAV_LABELS:
        if key in heading:
            return label
    return heading[:16]


def render_sweeps_html(out_dir=OUT) -> str:
    """Parse out/sweep.md into HTML for the Sweeps tab. The six knob sweeps + the
    ρ note render generically (### heading + line-plot PNG + prose + pipe table);
    the cap sweep (everything from the `## Cap sweep` marker) is pulled out and
    rendered behind its own per-shape switcher. A jump-to-section nav bar is
    prepended. Guarded: if sweep.py hasn't run, show a hint."""
    path = os.path.join(out_dir, "sweep.md")
    if not os.path.exists(path):
        return ('<p class="tnote">(no <code>sweep.md</code> found — run '
                '<code>python sweep.py</code> first)</p>')
    lines = open(path).read().splitlines()
    cap_start = next((i for i, l in enumerate(lines)
                      if l.startswith("## Cap sweep")), len(lines))
    nav = []                                   # [(dom-id, short-label)] for the nav
    body = _render_regular_sweeps(lines[:cap_start], out_dir, nav)
    if cap_start < len(lines):
        body += _render_cap_sweep(lines[cap_start:], out_dir, nav)
    navbar = ""
    if nav:
        btns = "".join(f'<button onclick="jumpTo(\'{hid}\')">{esc(lbl)}</button>'
                       for hid, lbl in nav)
        navbar = f'<div class="swnav">{btns}</div>'
    return navbar + body


def _render_regular_sweeps(lines, out_dir, nav) -> str:
    """Block-parse the non-cap portion of sweep.md (state machine over headings /
    tables / prose), embedding each sweep's line-plot PNG and collecting nav ids."""
    html, i, n = [], 0, len(lines)
    intro_done = False
    while i < n:
        line = lines[i].rstrip()
        if line.startswith("# ") and not line.startswith("## "):   # top title
            i += 1
            continue
        if line.startswith("### "):
            heading = line[4:]
            hid = _slug(heading)
            nav.append((hid, _nav_label(heading)))
            html.append(f"<h3 class='sw' id='{hid}'>{esc(heading)}</h3>")
            fig = next((v for k, v in SWEEP_FIGS.items() if k in heading), None)
            if fig and os.path.exists(os.path.join(out_dir, fig)):
                html.append(f'<figure class="swfig"><img src="{fig}" alt="{esc(heading)}">'
                            f'</figure>')
            i += 1
            continue
        if line.startswith("|"):                         # a pipe table block
            tbl = []
            while i < n and lines[i].lstrip().startswith("|"):
                tbl.append(lines[i])
                i += 1
            html.append(_sweep_table_html(tbl))
            continue
        if line.strip():                                 # prose paragraph
            cls = "tnote" if not intro_done else "sw-note"
            intro_done = True
            html.append(f'<p class="{cls}">{_md_inline(line.strip())}</p>')
        i += 1
    return "".join(html)


def _render_cap_sweep(lines, out_dir, nav) -> str:
    """Render the cap-sweep portion (from the `## Cap sweep` heading to EOF) as a
    single section with a per-shape switcher: intro prose, a `.shapepick`
    (data-shape-for="cap"), then one hidden-by-default div per CAP_SWEEP_SHAPES
    holding that shape's figure + cost/quality sub-tables. Trailing prose (the
    bump/spike cap-inert note) is shown once below, outside the switched divs."""
    title = lines[0][3:].strip() if lines and lines[0].startswith("## ") else "Cap sweep"
    hid = _slug(title)
    nav.append((hid, "Cap sweep"))
    intro, subs, trailing = [], [], []
    cur, i, n = None, 1, len(lines)
    while i < n:
        line = lines[i].rstrip()
        if line.startswith("### "):
            heading = line[4:]
            m = re.search(r"cap sweep \((\w+)\)\s*[—-]+\s*(.*)", heading)
            cur = {"shape": m.group(1) if m else "",
                   "kind": (m.group(2) if m else heading).strip(),
                   "prose": [], "tbl": []}
            subs.append(cur)
            i += 1
            continue
        if line.startswith("|"):
            while i < n and lines[i].lstrip().startswith("|"):
                cur["tbl"].append(lines[i])
                i += 1
            continue
        if line.strip():
            if cur is None:                              # pre-first-heading intro
                intro.append(line.strip())
            elif cur["tbl"]:                             # prose after a table = trailing note
                trailing.append(line.strip())
            else:                                        # prose between heading and its table
                cur["prose"].append(line.strip())
        i += 1

    out = [f"<h3 class='sw' id='{hid}'>{esc(title)}</h3>"]
    for j, p in enumerate(intro):
        out.append(f'<p class="{"tnote" if j == 0 else "sw-note"}">{_md_inline(p)}</p>')
    out.append('<div class="pick shapepick" data-shape-for="cap"></div>')
    for shape in CAP_SWEEP_SHAPES:
        blocks = [s for s in subs if s["shape"] == shape]
        if not blocks:
            continue
        hide = "" if shape == CAP_SWEEP_SHAPES[0] else ' style="display:none"'
        inner = []
        fig = f"17-sweep-cap-{shape}.png"
        if os.path.exists(os.path.join(out_dir, fig)):
            inner.append(f'<figure class="swfig"><img src="{fig}" '
                         f'alt="cap sweep {esc(shape)}"></figure>')
        for b in blocks:
            inner.append(f"<h4 class='sw4'>{esc(b['kind'])}</h4>")
            for p in b["prose"]:
                inner.append(f'<p class="sw-note">{_md_inline(p)}</p>')
            if b["tbl"]:
                inner.append(_sweep_table_html(b["tbl"]))
        out.append(f'<div data-cap-shape="{esc(shape)}"{hide}>{"".join(inner)}</div>')
    for p in trailing:
        out.append(f'<p class="sw-note">{_md_inline(p)}</p>')
    return "".join(out)


def _sweep_table_html(tbl_lines) -> str:
    """Render one parsed sweep pipe-table (list of raw '| … |' lines) to HTML,
    shading the metric columns best→green/worst→red like the Table view. Param
    columns (leading non-metric cells) are left neutral; the `*` baseline row is
    highlighted."""
    rows = []
    for ln in tbl_lines:
        cells = [c.strip() for c in ln.strip().strip("|").split("|")]
        if all(set(c) <= set("-: ") for c in cells):     # separator rule
            continue
        rows.append(cells)
    if not rows:
        return ""
    headers, body = rows[0], rows[1:]
    # Metric columns are the trailing ones sweep.py emits (good%, failed%, …);
    # everything before the first of those is a swept parameter. Detect by name.
    metric_names = {"good%", "failed%", "wait_p90", "rep_max", "rep·s",
                    "prov·s", "util"}
    first_metric = next((j for j, h in enumerate(headers) if h in metric_names),
                        len(headers))

    def col_dir(h):
        return +1 if h == "good%" else -1               # all others: lower better

    # Per-metric-column best/worst shading (column-wise, unlike the row-wise Table).
    col_styles = {}
    for j in range(first_metric, len(headers)):
        nums = [_num(r[j]) for r in body]
        vals = [v for v in nums if v is not None]
        if len(vals) < 2 or max(vals) == min(vals):
            continue
        lo, hi, d = min(vals), max(vals), col_dir(headers[j])
        col_styles[j] = []
        for v in nums:
            if v is None:
                col_styles[j].append("")
                continue
            frac = (v - lo) / (hi - lo)
            score = frac if d > 0 else (1.0 - frac)
            col_styles[j].append(_C_GOOD if score >= 0.66 else
                                 (_C_MEH if score >= 0.33 else _C_BAD))
    th = "".join(f"<th>{esc(h)}</th>" for h in headers)
    out = [f'<table class="sum"><thead><tr>{th}</tr></thead><tbody>']
    for ri, r in enumerate(body):
        star = any("*" in c for c in r[:first_metric])   # baseline row marker
        tr = ' class="base"' if star else ""
        tds = ""
        for j, c in enumerate(r):
            st = col_styles.get(j, [""] * len(body))[ri] if j >= first_metric else ""
            style = f' style="{st}"' if st else ""
            tds += f"<td{style}>{esc(c)}</td>"
        out.append(f"<tr{tr}>{tds}</tr>")
    out.append("</tbody></table>")
    return "".join(out)


# Cross-policy tradeoff figures are now rendered PER SHAPE by run.py:
#   09-wait-cdf-<shape>.png   (waiting-time CDF overlay)
#   10-cost-quality-<shape>.png (cost-vs-quality Pareto frontier)
# The two "how to read" notes below are shared across all shapes and shown once
# at the top of the Tradeoffs tab; the detailed frontier readings reference the
# bump (calibration) shape, which is rendered first.
CDF_NOTE = (
    "Each curve is a policy's <b>wait CDF over the OFFERED denominator</b>: height "
    "at time <i>t</i> = share of all arrivals served within <i>t</i> s. Curves that "
    "asymptote <b>below 100%</b> stranded work (unfinished). Read left-to-right: the "
    "further up-and-left, the prompter. Legend carries each policy's billed "
    "fleet-cost, so promptness and cost read together. This is the same data as the "
    "Table's “≤Ns %” rows, shown continuously.")
PARETO_NOTE = (
    "x = billed fleet-time (provisioned·seconds, the cost); y = promptness (% of "
    "offered served within 15s). The dashed line is the frontier over the "
    "<b>deployable</b> policies — anything below-and-right of it is dominated "
    "(something is both cheaper AND prompter). <b>ideal</b> is drawn apart as the "
    "clairvoyant reference (not deployable). This is where “same cost, better "
    "quality” becomes literal — on the reference <b>bump</b>: <b>setup-lag → "
    "queue-aware(1.3) → Qexp(1.3)</b> trace the frontier's steep left wall — each "
    "Pareto-optimal, a little more fleet-time for a lot more promptness — with Qexp "
    "the standout (89% within 15s at essentially queue-aware's cost). The extra "
    "hollow points are the two Q sizers swept to <b>headroom 1.5 and 2.0</b>: "
    "queue-aware climbs but every one of its points stays <i>below</i> Qexp, and "
    "both sizers' high-headroom variants are dominated by Qexp(1.3) — buying static "
    "margin costs real fleet-time for little extra quality, whereas anticipation is "
    "near-free. The fleet-heavy KEDA points (hpa-queue/combined) sit far to the "
    "right at ~2.5–3× the cost. The sustained shapes below stress this differently — "
    "read each shape's own frontier.")


def _tradeoff_fig_html(png, out_dir, caption) -> str:
    """One cost-quality or wait-CDF figure (guarded) for the Tradeoffs tab."""
    if not os.path.exists(os.path.join(out_dir, png)):
        return (f'<p class="tnote">(no <code>{esc(png)}</code> — run '
                f'<code>python run.py</code> first)</p>')
    return (f'<figure class="tradefig"><figcaption class="figcap">{esc(caption)}</figcaption>'
            f'<img src="{png}" alt="{esc(caption)}">'
            f'<a class="zoom" href="{png}" target="_blank">open full size &#8599;</a>'
            f"</figure>")


def render_tradeoffs_html(out_dir=OUT) -> str:
    """Tradeoffs tab: the two how-to-read notes only. The side-by-side shape
    comparison (pick shape A / shape B → each shape's cost-quality + wait-CDF
    stacked in the L/R columns) is JS-rendered into the two panes the template
    declares, so switching a column's shape is instant. `out_dir` is unused now
    (the client resolves figures by filename) but kept for signature stability."""
    return (
        "<h3 class='sw'>How to read these two views</h3>"
        f'<p class="sw-note"><b>Cost vs quality (Pareto).</b> {PARETO_NOTE}</p>'
        f'<p class="sw-note"><b>Waiting-time CDF.</b> {CDF_NOTE}</p>'
    )


def render_markdown(out_dir=OUT) -> str:
    """Generate REPORT.md programmatically from the SAME sources the HTML uses
    (summary.md + SCENARIOS + GLOSSARY + shared prose), so REPORT.md has
    identical scope to index.html — every rendered scenario, every metric row."""
    headers, rows = parse_md_table(os.path.join(out_dir, "summary.md"))
    scen = [s for s in SCENARIOS
            if os.path.exists(os.path.join(out_dir, fig_path(s, "bump", "main")))]
    md = []
    md.append("# Autoscaling Behavioral Demo — comparison report\n")
    md.append(INTRO + "\n")
    md.append(
        "> This is the static, GitHub-renderable view. The interactive version\n"
        "> (tabbed compare / browse / table / glossary, with a zoom slider) lives at\n"
        "> [`out/index.html`](out/index.html) — open it locally; GitHub strips its JS/CSS.\n"
        "> Rebuild everything with `python run.py && python report.py`.\n")
    md.append(STORY_NOTE + "\n")
    md.append("---\n")
    md.append("## The story in one table\n")
    md.append("Quality rows are the **cumulative** share of offered requests served "
              "*within* each wait bound (the wait CDF sampled at 2 / 15 / 45 / 60 s); "
              "cost is fleet-time.\n")
    if headers:
        md.append(_md_table(headers, _subset(rows, STORY_ROWS)) + "\n")
    md.append(READINGS + "\n")
    md.append("<details><summary>Full metrics table (all rows)</summary>\n")
    if headers:
        md.append(_md_table(headers, rows) + "\n")
    md.append("</details>\n")
    md.append("---\n")
    # Cross-policy tradeoff figures for the BUMP reference (CDF overlay +
    # cost-quality frontier); the per-shape versions follow in "Demand shapes".
    bump_pareto, bump_cdf = "10-cost-quality-bump.png", "09-wait-cdf-bump.png"
    if any(os.path.exists(os.path.join(out_dir, p)) for p in (bump_pareto, bump_cdf)):
        md.append("## Cost & waiting-time tradeoffs (reference: bump)\n")
        md.append("Two cross-policy views on one axis — the full waiting-time CDF and "
                  "the cost-vs-quality frontier — on the calibration **bump** shape.\n")
        if os.path.exists(os.path.join(out_dir, bump_pareto)):
            md.append(f"**Cost vs quality — the Pareto frontier.** {_html_to_md(PARETO_NOTE)}\n")
            md.append(f"![cost vs quality — bump]({out_dir}/{bump_pareto})\n")
        if os.path.exists(os.path.join(out_dir, bump_cdf)):
            md.append(f"**Waiting-time CDF — all policies on one axis.** {_html_to_md(CDF_NOTE)}\n")
            md.append(f"![waiting-time CDF — bump]({out_dir}/{bump_cdf})\n")
        md.append("---\n")
    # Demand shapes — the headline cross-shape comparison: each shape's Pareto
    # frontier. Full per-shape galleries + tables live in the interactive HTML.
    if any(os.path.exists(os.path.join(out_dir, f"10-cost-quality-{k}.png"))
           for k, _ in SHAPES):
        md.append("## Demand shapes\n")
        md.append("The same eight policies over five demand shapes. **bump** is the "
                  "calibration reference; **trapezoid / step up / step down** stress "
                  "sustained load and scale-down; **spike** is a teaching case "
                  "(autoscaling is the wrong tool for a 6 s burst). Each panel is that "
                  "shape's cost-vs-quality frontier; open [`out/index.html`](out/index.html) "
                  "for the full per-shape galleries, waiting-time CDFs, and metric tables.\n")
        for key, label in SHAPES:
            png = f"10-cost-quality-{key}.png"
            if os.path.exists(os.path.join(out_dir, png)):
                # SHAPE_NOTES already leads with the bold shape name — no label prefix.
                md.append(f"{_html_to_md(SHAPE_NOTES.get(key, ''))}\n")
                md.append(f"![cost vs quality — {key}]({out_dir}/{png})\n")
        md.append("---\n")
    md.append("## Scenarios (reference: bump)\n")
    for i, s in enumerate(scen, 1):
        md.append(f"### {i} · {s['label']}\n")
        md.append(f"*{s['setup']}*\n")
        md.append(f"{s['answers']}\n")
        # REPORT.md sits one level above out/; the HTML lives inside out/ so it
        # references bare filenames. Prefix out_dir for the markdown links. The
        # static fallback shows the bump reference figures (per-shape in the HTML).
        main = fig_path(s, "bump", "main")
        md.append(f"![{s['key']}]({out_dir}/{main})\n")
        lat = fig_path(s, "bump", "latency")
        if os.path.exists(os.path.join(out_dir, lat)):
            md.append("<details><summary>latency</summary>\n")
            md.append(f"![{s['key']} latency]({out_dir}/{lat})\n")
            md.append("</details>\n")
    md.append("---\n")
    # Parameter-sweep line-plots (full numeric tables stay in out/sweep.md).
    sweep_figs = [("Setup-lag — quality collapse & cost vs boot time", "11-sweep-setuplag.png"),
                  ("Queue-aware — aggression vs quality & cost", "12-sweep-drain.png"),
                  ("Qexp — assumed boot lead vs quality & cost", "13-sweep-qexp.png"),
                  ("Headroom — static margin vs quality & cost (queue-aware, Qexp)",
                   "14-sweep-headroom.png"),
                  ("Headroom × drain — aggressive reaction vs static margin (queue-aware)",
                   "15-sweep-headroom-drain.png"),
                  ("Headroom × anticipation — look-ahead vs static margin (Qexp)",
                   "16-sweep-headroom-proj.png"),
                  ("Cap sweep (trapezoid) — actuation ceiling vs cost & quality",
                   "17-sweep-cap-trapezoid.png"),
                  ("Cap sweep (step up) — actuation ceiling vs cost & quality",
                   "17-sweep-cap-stepup.png"),
                  ("Cap sweep (step down) — actuation ceiling vs cost & quality",
                   "17-sweep-cap-stepdown.png")]
    if any(os.path.exists(os.path.join(out_dir, p)) for _, p in sweep_figs):
        md.append("## Parameter sweeps\n")
        md.append("Trend + calibration line-plots (full numeric tables in "
                  f"[`{out_dir}/sweep.md`]({out_dir}/sweep.md)). The six knob sweeps "
                  "all run on the **bump** reference shape (only the knob varies); the "
                  "**Cap sweep** figures below vary the demand shape and name it in each "
                  "title. Solid = good %, dashed = wait p90, dotted vertical = baseline.\n")
        for title, png in sweep_figs:
            if os.path.exists(os.path.join(out_dir, png)):
                md.append(f"![{title}]({out_dir}/{png})\n")
        md.append("---\n")
    md.append("## Glossary\n")
    for term, definition in GLOSSARY:
        md.append(f"**{term}.** {_html_to_md(definition)}\n")
    return "\n".join(md)


TEMPLATE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Autoscaling Behavioral Demo — Report</title>
<style>
:root{--fg:#1f2937;--muted:#6b7280;--line:#e5e7eb;--accent:#2563eb;--sel:#dbeafe;--bg:#fff;}
*{box-sizing:border-box;}
body{margin:0;font:15px/1.5 -apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:var(--fg);background:var(--bg);}
header{padding:18px 24px;border-bottom:1px solid var(--line);}
header h1{margin:0 0 4px;font-size:19px;}
header p{margin:0 0 6px;color:var(--muted);font-size:13px;max-width:1000px;}
header p:last-child{margin-bottom:0;}
header p b{color:var(--fg);}
.tnote{max-width:1000px;margin:0 0 16px;color:#374151;font-size:13.5px;line-height:1.55;}
.tnote b{color:var(--fg);}
.tnote code{background:#f3f4f6;padding:1px 5px;border-radius:4px;font-size:12.5px;}
.tabs{display:flex;gap:6px;padding:12px 24px 0;border-bottom:1px solid var(--line);}
.tab{padding:8px 16px;border:1px solid var(--line);border-bottom:none;border-radius:8px 8px 0 0;background:#f9fafb;cursor:pointer;font-weight:600;font-size:13px;}
.tab.active{background:var(--bg);color:var(--accent);}
main{padding:20px 24px 60px;}
.view{display:none;}.view.active{display:block;}
.pick{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:10px;}
.pick button{padding:6px 12px;border:1px solid var(--line);border-radius:6px;background:#f9fafb;cursor:pointer;font-size:13px;}
.pick button.sel{background:var(--sel);border-color:var(--accent);color:var(--accent);font-weight:600;}
/* Compare: two half-width panes side by side, each its own scroll box; JS keeps
   their scroll positions in lockstep so the shared time axis stays aligned. */
.cmp{display:flex;gap:18px;align-items:flex-start;}
.pane{flex:1 1 0;min-width:0;}
/* grow to the full figure height (no vertical scrollbar); only scroll
   horizontally when zoomed past fit. overflow-y:hidden with height:auto means
   the box just expands to the image height. */
.scroll{overflow-x:auto;overflow-y:hidden;border:1px solid var(--line);border-radius:6px;background:#fff;}
.meta{font-size:12px;color:var(--muted);margin:2px 0 8px;min-height:48px;}
.meta b{color:var(--fg);}
figure{margin:0;}
figure img{height:auto;display:block;background:#fff;}
/* slider row */
.controls{display:flex;align-items:center;gap:12px;margin-bottom:14px;font-size:13px;color:var(--muted);}
.controls input[type=range]{width:260px;}
.controls .lbl{font-weight:600;color:var(--fg);}
.hint{font-size:12px;color:var(--muted);margin:14px 0 6px;}
.zoom{font-size:12px;color:var(--accent);text-decoration:none;display:inline-block;margin-top:6px;}
table.sum{border-collapse:collapse;font-size:13px;}
table.sum th,table.sum td{border:1px solid var(--line);padding:5px 12px;text-align:right;}
table.sum th:first-child,table.sum td:first-child{text-align:left;}
table.sum th.mean,table.sum td.mean{text-align:left;color:var(--muted);font-size:12px;max-width:360px;white-space:normal;}
table.sum thead th{background:#f3f4f6;position:sticky;top:0;z-index:3;}
table.sum tr.sec td{background:#eef2ff;color:#3730a3;font-weight:700;text-align:left;border-top:2px solid #c7d2fe;}
dl.gloss{max-width:900px;}
dl.gloss dt{font-weight:700;margin:16px 0 3px;color:var(--fg);}
dl.gloss dd{margin:0;color:#374151;font-size:14px;}
dl.gloss code{background:#f3f4f6;padding:1px 5px;border-radius:4px;font-size:12.5px;}
h3.sw{margin:26px 0 6px;font-size:15px;color:#3730a3;}
p.sw-note{max-width:1000px;margin:0 0 10px;color:#374151;font-size:13px;line-height:1.5;}
p.sw-note code{background:#f3f4f6;padding:1px 5px;border-radius:4px;font-size:12px;}
table.sum tr.base td{font-weight:700;}
#view-sweeps table.sum{margin-bottom:8px;}
/* cross-policy tradeoff + sweep line-plot figures: cap to pane width, keep aspect */
figure.tradefig{margin:0 0 26px;max-width:1100px;}
figure.swfig{margin:2px 0 16px;max-width:1100px;}
figure.tradefig img,figure.swfig img{max-width:100%;height:auto;display:block;border:1px solid var(--line);border-radius:6px;background:#fff;}
figcaption.figcap{font-size:12.5px;color:var(--muted);margin:0 0 4px;font-weight:600;}
/* Browse gallery: one fit-to-width main figure per shape + a collapsed latency figure */
.browse-gallery figure.browsefig{margin:0 0 4px;max-width:1100px;}
.browse-gallery figure.browsefig img{max-width:100%;height:auto;display:block;border:1px solid var(--line);border-radius:6px;background:#fff;}
.browse-gallery details{margin:0 0 22px;max-width:1100px;}
.browse-gallery details summary{cursor:pointer;color:var(--accent);font-size:13px;margin:4px 0;}
/* shape switcher (Compare + Table) reuses .pick styling; banner note under it */
p.shapebanner{max-width:1000px;margin:0 0 12px;}
/* Table slow-tail row: dark-red, ruled off from the served-within bands above it.
   The "what it means" cell stays muted (override the red). */
table.sum tr.failrow td{color:#b91c1c;font-weight:700;border-top:2px solid #fca5a5;}
table.sum tr.failrow td.mean{color:var(--muted);font-weight:400;}
/* Sweeps jump-to-section nav (sticky above the sections) */
.swnav{display:flex;flex-wrap:wrap;gap:6px;margin:0 0 12px;position:sticky;top:0;background:var(--bg);padding:8px 0;z-index:4;border-bottom:1px solid var(--line);}
.swnav button{padding:5px 10px;border:1px solid var(--line);border-radius:6px;background:#f9fafb;cursor:pointer;font-size:12px;color:var(--accent);font-weight:600;}
.swnav button:hover{background:var(--sel);border-color:var(--accent);}
h4.sw4{margin:16px 0 4px;font-size:13.5px;color:#4b5563;font-weight:700;}
/* Tradeoffs side-by-side reuses .cmp/.pane; align columns to the top */
#view-tradeoffs .cmp{margin-top:10px;align-items:flex-start;}
</style>
</head>
<body>
<header>
  <h1>Autoscaling Behavioral Demo — comparison report</h1>
  <p>__INTRO__</p>
  <p>__STORY__</p>
</header>
<div class="tabs">
  <div class="tab active" data-tab="compare">Compare</div>
  <div class="tab" data-tab="browse">Browse</div>
  <div class="tab" data-tab="table">Table</div>
  <div class="tab" data-tab="tradeoffs">Tradeoffs</div>
  <div class="tab" data-tab="sweeps">Sweeps</div>
  <div class="tab" data-tab="glossary">Glossary</div>
</div>
<main>
  <section class="view active" id="view-compare">
    <div class="controls">
      <span class="lbl">Zoom</span>
      <span>fit</span>
      <input type="range" id="zoomer" min="0" max="100" value="0">
      <span>full detail</span>
      <span id="zoomval"></span>
    </div>
    <div class="pick shapepick" data-shape-for="compare"></div>
    <p class="tnote shapebanner" id="shapebanner-compare"></p>
    <div class="cmp">
      <div class="pane">
        <div class="pick" data-side="L"></div>
        <div class="meta" id="meta-L"></div>
        <div class="scroll" id="scroll-L"><figure><img id="img-L"></figure></div>
        <a class="zoom" id="zoom-L" target="_blank">open full size &#8599;</a>
      </div>
      <div class="pane">
        <div class="pick" data-side="R"></div>
        <div class="meta" id="meta-R"></div>
        <div class="scroll" id="scroll-R"><figure><img id="img-R"></figure></div>
        <a class="zoom" id="zoom-R" target="_blank">open full size &#8599;</a>
      </div>
    </div>
  </section>
  <section class="view" id="view-browse">
    <div class="pick" data-side="B"></div>
    <div class="pick shapepick" data-shape-for="browse"></div>
    <div class="meta" id="meta-B"></div>
    <p class="hint">The selected policy on the selected demand shape — main figure plus a
    collapsible latency figure. Numbers in the meta line above reference the bump shape;
    see the Table tab for each shape's exact metrics.</p>
    <div class="browse-gallery" id="browse-gallery"></div>
  </section>
  <section class="view" id="view-table">
    <p class="tnote">__READINGS__</p>
    __TABLE__
  </section>
  <section class="view" id="view-tradeoffs">
    <p class="tnote">Cross-policy tradeoffs — the two views that put every policy on
    <b>one shared axis</b>: the full waiting-time <b>CDF</b> (how prompt), and the
    <b>cost&nbsp;vs&nbsp;quality</b> frontier (what the promptness costs). Pick a shape
    for each column to compare two demand shapes side by side.</p>
    __TRADEOFFS__
    <div class="cmp">
      <div class="pane">
        <div class="pick shapepick" data-shape-for="tradeA"></div>
        <div id="trade-A"></div>
      </div>
      <div class="pane">
        <div class="pick shapepick" data-shape-for="tradeB"></div>
        <div id="trade-B"></div>
      </div>
    </div>
  </section>
  <section class="view" id="view-sweeps">
    <p class="tnote">Parameter sweeps — trend + calibration figures and tables (not the
    seven canonical scenario figures). <b>Demand shape:</b> every knob sweep here runs on
    the <b>bump</b> reference shape (only the knob varies, the demand does not); the final
    <b>Cap sweep</b> is the exception — it varies the demand shape (trapezoid / step-up /
    step-down) and its switcher labels each. Each point re-runs the sim across a knob grid;
    a point at the baseline knobs reproduces the matching scenario's summary row. In
    the plots, <b>solid</b> = good&nbsp;% (left axis), <b>dashed</b> = wait&nbsp;p90
    (right axis), and the dotted vertical is the baseline. In the tables <b>*</b> marks
    the canonical baseline; metric cells are shaded best&rarr;green / worst&rarr;red
    down each column.</p>
    __SWEEPS__
  </section>
  <section class="view" id="view-glossary">
    __GLOSSARY__
  </section>
</main>
<script>
const SCEN = __SCEN__;
const SHAPES = __SHAPES__;            // [[key, short-label], ...]
const SHAPE_NOTES = __SHAPENOTES__;   // {key: html banner}
const FULL_W = __FULLW__;
const POLICY_DEFAULTS = {L:"setup-lag", R:"queue-aware", B:"ideal"};
// Each tab owns its shape independently (no shared state.shape). Tradeoffs holds
// two (A/B) for the side-by-side; cap defaults to a shape where the ceiling bites.
const state = Object.assign(
  {shape:{compare:"bump", browse:"bump", table:"bump",
          tradeA:"bump", tradeB:"trapezoid", cap:"trapezoid"}},
  POLICY_DEFAULTS);
const byKey = k => SCEN.find(s => s.key === k) || SCEN[0];
// Compose a scenario's figure path for a shape — mirrors run.py + report.py fig_path.
function figPath(s, shape, kind){
  return kind === "latency" ? s.stem+"-"+shape+"-latency.png" : s.stem+"-"+shape+".png";
}
function fillMeta(el, s){ el.innerHTML = "<b>"+s.label+"</b> &mdash; "+s.setup+"<br>answers: "+s.answers; }
// Compare panes (L / R): shape-aware, driven by state.shape.compare.
function renderSide(side){
  const s = byKey(state[side]);
  const path = figPath(s, state.shape.compare, "main");
  const img = document.getElementById("img-"+side);
  img.src = path; img.alt = s.label;
  const zoom = document.getElementById("zoom-"+side);
  if (zoom) zoom.href = path;
  const meta = document.getElementById("meta-"+side);
  if (meta) fillMeta(meta, s);
  document.querySelectorAll('.pick[data-side="'+side+'"] button').forEach(b => {
    b.classList.toggle("sel", b.dataset.key === state[side]);
  });
  applyZoom();
}
// Level the two Compare panes: the misalignment is variable-height meta text (the
// answers line), NOT the fixed-size PNGs. Equalize both meta boxes to the taller
// so the figures start on the same baseline. No-op if a pane is display:none
// (offsetHeight 0) — re-run on tab-activate + resize.
function levelMetas(){
  const l = document.getElementById("meta-L"), r = document.getElementById("meta-R");
  if (!l || !r) return;
  l.style.height = r.style.height = "auto";
  const h = Math.max(l.offsetHeight, r.offsetHeight);
  if (h) l.style.height = r.style.height = h + "px";
}
// Re-render both Compare panes + banner for the current compare shape, then level.
function renderCompare(){
  const banner = document.getElementById("shapebanner-compare");
  if (banner) banner.innerHTML = SHAPE_NOTES[state.shape.compare] || "";
  renderSide("L"); renderSide("R"); levelMetas();
}
// Browse: ONE shape for the selected policy (state.B) — main figure plus a
// collapsible latency figure. Shape chosen by the browse switcher (state.shape.browse).
function renderBrowse(){
  const s = byKey(state.B);
  const meta = document.getElementById("meta-B");
  if (meta) fillMeta(meta, s);
  const key = state.shape.browse, label = (SHAPES.find(x => x[0] === key) || [key, key])[1];
  const main = figPath(s, key, "main"), lat = figPath(s, key, "latency");
  const note = SHAPE_NOTES[key] ? '<p class="sw-note">'+SHAPE_NOTES[key]+'</p>' : '';
  document.getElementById("browse-gallery").innerHTML =
    "<h3 class='sw'>"+label+"</h3>" + note +
    '<figure class="browsefig"><img loading="lazy" src="'+main+'" alt="'+label+'"></figure>' +
    '<a class="zoom" href="'+main+'" target="_blank">open full size &#8599;</a>' +
    '<details><summary>latency — per-request time in system (coloured by request size)</summary>' +
    '<figure class="browsefig"><img loading="lazy" src="'+lat+'" alt="'+label+' latency"></figure>' +
    '<a class="zoom" href="'+lat+'" target="_blank">open full size &#8599;</a></details>';
  document.querySelectorAll('.pick[data-side="B"] button').forEach(b => {
    b.classList.toggle("sel", b.dataset.key === state.B);
  });
}
// Table: show only the div for the table tab's own shape (state.shape.table).
// DIRECT children only — the picker buttons also carry data-shape, so a descendant
// selector would hide all but the selected shape's pill and gut the switcher.
function renderTableShape(){
  document.querySelectorAll('#view-table > [data-shape]').forEach(d => {
    d.style.display = (d.dataset.shape === state.shape.table) ? "" : "none";
  });
}
// Tradeoffs: one column (A|B) renders its shape's note + cost-quality + wait-CDF,
// stacked. Each column owns its shape (state.shape.tradeA / tradeB).
function tradeFig(png, alt){
  return '<figure class="tradefig"><img loading="lazy" src="'+png+'" alt="'+alt+'"></figure>' +
         '<a class="zoom" href="'+png+'" target="_blank">open full size &#8599;</a>';
}
function renderTradeoffSide(side){
  const scope = side === "A" ? "tradeA" : "tradeB";
  const key = state.shape[scope];
  const label = (SHAPES.find(x => x[0] === key) || [key, key])[1];
  // The banner carries an id so levelTradeNotes() can equalize the two columns.
  const note = '<p class="sw-note" id="tnote-'+side+'">'+(SHAPE_NOTES[key] || "")+'</p>';
  document.getElementById("trade-"+side).innerHTML =
    "<h3 class='sw'>"+label+"</h3>" + note +
    "<h4 class='sw4'>Cost vs quality</h4>" + tradeFig("10-cost-quality-"+key+".png", label+" cost vs quality") +
    "<h4 class='sw4'>Waiting-time CDF</h4>" + tradeFig("09-wait-cdf-"+key+".png", label+" waiting-time CDF");
}
// Level the two Tradeoffs columns, same trick as levelMetas() on Compare: the shape
// banners are variable-height prose, so without this the "Cost vs quality" heading —
// and every figure below it — starts at a different y in each column and the
// side-by-side comparison no longer lines up. Re-run after either column re-renders,
// on tab-activate (offsetHeight is 0 while display:none) and on resize (the banner
// reflows against p.sw-note's max-width).
function levelTradeNotes(){
  const a = document.getElementById("tnote-A"), b = document.getElementById("tnote-B");
  if (!a || !b) return;
  a.style.height = b.style.height = "auto";
  const h = Math.max(a.offsetHeight, b.offsetHeight);
  if (h) a.style.height = b.style.height = h + "px";
}
// Cap sweep (Sweeps tab): show only the div for the cap switcher's shape.
function renderCapShape(){
  document.querySelectorAll('#view-sweeps [data-cap-shape]').forEach(d => {
    d.style.display = (d.dataset.capShape === state.shape.cap) ? "" : "none";
  });
}
function buildPickers(){
  // policy pickers only (data-side); shape pickers are built by buildShapePickers.
  document.querySelectorAll('.pick[data-side]').forEach(p => {
    const side = p.dataset.side;
    SCEN.forEach(s => {
      const b = document.createElement("button");
      b.textContent = s.label; b.dataset.key = s.key;
      // Compare (L/R) re-renders both panes so heights re-level; Browse (B) redraws.
      b.onclick = () => { state[side] = s.key; (side === "B" ? renderBrowse() : renderCompare()); };
      p.appendChild(b);
    });
  });
}
// Each tab's shape switcher is a .shapepick tagged with data-shape-for="<scope>".
// SHAPE_RENDER maps a scope to the render call that shows its shape — so a switcher
// only ever redraws its own tab (no cross-tab coupling).
const SHAPE_RENDER = {
  compare: renderCompare,
  browse: renderBrowse,
  table: renderTableShape,
  tradeA: () => { renderTradeoffSide("A"); levelTradeNotes(); },
  tradeB: () => { renderTradeoffSide("B"); levelTradeNotes(); },
  cap: renderCapShape,
};
function markShapeSel(picker, shape){
  picker.querySelectorAll('button').forEach(b => {
    b.classList.toggle("sel", b.dataset.shape === shape);
  });
}
function setShapeFor(scope, shape){
  state.shape[scope] = shape;
  document.querySelectorAll('.shapepick[data-shape-for="'+scope+'"]').forEach(p => markShapeSel(p, shape));
  (SHAPE_RENDER[scope] || function(){})();
}
function buildShapePickers(){
  // The cap switcher only offers shapes the cap sweep actually rendered (where the
  // ceiling bites the Q sizers); read them from the DOM so the two stay in sync.
  const capShapes = Array.from(document.querySelectorAll('#view-sweeps [data-cap-shape]'))
    .map(d => d.dataset.capShape);
  document.querySelectorAll('.shapepick').forEach(p => {
    const scope = p.dataset.shapeFor;
    const shapes = scope === "cap"
      ? SHAPES.filter(sh => capShapes.indexOf(sh[0]) !== -1)
      : SHAPES;
    shapes.forEach(function(sh){
      const b = document.createElement("button");
      b.textContent = sh[1]; b.dataset.shape = sh[0];
      b.onclick = () => setShapeFor(scope, sh[0]);
      p.appendChild(b);
    });
    markShapeSel(p, state.shape[scope]);
  });
}
// Compare-pane zoom: interpolate each pane image width between "fit to pane"
// (frac 0 => 100%, no horizontal scroll) and natural full detail (frac 1 => FULL_W).
function applyZoom(){
  const frac = (+document.getElementById("zoomer").value) / 100;
  document.getElementById("zoomval").textContent =
      frac === 0 ? "(fit)" : Math.round(100 + frac*(FULL_W/ (paneW()||FULL_W) *100 -100)) + "%";
  ["L","R"].forEach(side => {
    const img = document.getElementById("img-"+side);
    const pane = img.closest(".scroll").clientWidth - 2;   // minus border
    const w = pane + frac * (FULL_W - pane);
    img.style.width = Math.max(pane, w) + "px";
  });
}
function paneW(){
  const s = document.getElementById("scroll-L");
  return s ? s.clientWidth - 2 : 0;
}
// Synchronized HORIZONTAL scroll only: the panes have no vertical scrollbar
// (they grow to full figure height), so mirror scrollLeft to keep the shared
// time axis aligned when zoomed in.
function initSync(){
  const L = document.getElementById("scroll-L");
  const R = document.getElementById("scroll-R");
  let lock = false;
  function mirror(from, to){
    if (lock) return; lock = true;
    to.scrollLeft = from.scrollLeft;
    lock = false;
  }
  L.addEventListener("scroll", () => mirror(L, R));
  R.addEventListener("scroll", () => mirror(R, L));
}
function initTabs(){
  document.querySelectorAll('.tab').forEach(t => {
    t.onclick = () => {
      document.querySelectorAll('.tab').forEach(x => x.classList.remove('active'));
      document.querySelectorAll('.view').forEach(x => x.classList.remove('active'));
      t.classList.add('active');
      document.getElementById('view-'+t.dataset.tab).classList.add('active');
      // Height-dependent layout must run once the view is visible (offsetHeight is
      // 0 while display:none), so re-level / re-zoom Compare on activation.
      if (t.dataset.tab === "compare"){ applyZoom(); levelMetas(); }
      if (t.dataset.tab === "tradeoffs"){ levelTradeNotes(); }
    };
  });
}
// Sweeps jump-to-section nav: smooth-scroll to a heading by id.
function jumpTo(id){
  const el = document.getElementById(id);
  if (el) el.scrollIntoView({behavior: "smooth", block: "start"});
}
buildPickers(); buildShapePickers(); initTabs(); initSync();
document.getElementById("zoomer").addEventListener("input", applyZoom);
window.addEventListener("resize", () => { applyZoom(); levelMetas(); levelTradeNotes(); });
// Initial paint: every tab renders its own default shape independently.
renderCompare();
renderBrowse();
renderTableShape();
renderTradeoffSide("A");
renderTradeoffSide("B");
levelTradeNotes();   // no-op while the tab is hidden; re-run on activate + resize
renderCapShape();
</script>
</body>
</html>
"""


def build(out_dir=OUT, out_html=None, md_path="REPORT.md"):
    out_html = out_html or os.path.join(out_dir, "index.html")
    # A scenario is shown if its bump reference figure exists (all shapes share a stem).
    scen = [s for s in SCENARIOS
            if os.path.exists(os.path.join(out_dir, fig_path(s, "bump", "main")))]
    shape_notes_html = {k: _md_inline(v) for k, v in SHAPE_NOTES.items()}
    html = (TEMPLATE
            .replace("__SCEN__", json.dumps(scen))
            .replace("__SHAPES__", json.dumps(SHAPES))
            .replace("__SHAPENOTES__", json.dumps(shape_notes_html))
            .replace("__FULLW__", str(FULL_W))
            .replace("__INTRO__", _md_inline(INTRO))
            .replace("__STORY__", _md_inline(STORY_NOTE))
            .replace("__READINGS__", _md_inline(READINGS))
            .replace("__TABLE__", render_tables_by_shape_html(out_dir))
            .replace("__TRADEOFFS__", render_tradeoffs_html(out_dir))
            .replace("__SWEEPS__", render_sweeps_html(out_dir))
            .replace("__GLOSSARY__", render_glossary_html()))
    with open(out_html, "w") as f:
        f.write(html)
    print(f"[wrote {out_html}]  scenarios={[s['key'] for s in scen]}  "
          f"shapes={[k for k, _ in SHAPES]}")
    # REPORT.md is generated from the same sources → identical scope, no drift.
    with open(md_path, "w") as f:
        f.write(render_markdown(out_dir))
    print(f"[wrote {md_path}]  scenarios={len(scen)}")
    return out_html


if __name__ == "__main__":
    build()
