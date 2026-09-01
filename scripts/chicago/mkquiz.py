#!/usr/bin/env python3
"""Build the guest-facing quiz page from curate.py's shortlist JSON.

    mkquiz.py docs/show/quiz/*.json --out viz/public/quiz.html

The page is what the guest sees on camera: four plausible upgrades per
scenario, no numbers, until the host reveals them. Numbers come from
curate.py --json, never from a second transcription — a figure that lives in
two files diverges the first time one of them is regenerated, and on this
material the divergence would be invisible until someone quoted it on air.

Output goes in viz/public/ so vite copies it into viz/dist/, which demosrv
already serves at / (its -viz flag). That means the quiz is reachable at
/quiz.html next to the map app at /app/ with no demosrv change.

Self-contained by construction: no CDN, no fetch, no build step. The page
has to work on a laptop with flaky conference wifi.
"""
import argparse
import base64
import html
import json
import os
import sys

# Per-scenario framing. Kept here rather than in the reports because it is
# presentation, not measurement: what the guest needs to know to make the
# choice make sense.
CONTEXT = {
    "merge-pod": ("The Merge",
                  "44 lanes · 13.8 lane-km · a fictitious two-lane freeway "
                  "with one heavy on-ramp — authored so the merge is the "
                  "only bottleneck in the network"),
    "bottleneck-town": ("Bottleneck Town",
                        "200 lanes · 24.6 lane-km · a fictitious four-signal "
                        "arterial through a small grid, authored so the "
                        "signals are the only bottleneck"),
    "chi-loop-urban": ("The Loop and the expressways",
                       "55,555 lanes · 2,204 lane-km · the downtown grid plus "
                       "the Kennedy, Dan Ryan, Eisenhower, Stevenson, Lake "
                       "Shore Drive and the Jane Byrne"),
    "chi-loop-cbd": ("The Loop CBD",
                     "11,851 lanes · 208 lane-km · the downtown grid alone — "
                     "1,057 signals, one about every 200 m"),
    "chi-kennedy": ("The Kennedy corridor",
                    "15,315 lanes · 961 lane-km · one expressway and its ramp "
                    "terminals"),
}

# Per-scenario limits, shown under the reveal. Same rationale as CONTEXT —
# presentation, not measurement — but these are the sentences that keep the
# page honest, so they are per-scenario rather than one global footnote. The
# Chicago caveats (fast expressways, uninterpretable widenings) are artifacts
# of a 55k-lane OSM import at a demand the driver could not fully serve; they
# are NOT true of the two authored pods, and printing them there would invent
# a weakness that scenario does not have. A scenario with no entry here gets
# no claim made on its behalf.
CAVEATS = {
    "merge-pod":
        "The jam here is real car-following: a backward-propagating "
        "shockwave at about 10 km/h, with every vehicle under driver control "
        "for the whole run. The diversion option is the exception — the "
        "engine has no route-choice model, so how many drivers would take a "
        "frontage road is an input we supplied, not a result. Read it as "
        "\"what a diversion this size buys\".",
    "bottleneck-town":
        "A fictitious network with no calibration target: it is not claimed "
        "to reproduce any real town. The claim is narrower — the congestion "
        "in it is signal queueing under full car-following control, and the "
        "options are compared against the same baseline on the same seeds.",
    "chi-loop-urban":
        "<b>These numbers are superseded and are being re-measured.</b> They "
        "were produced before the comparison tooling dropped horizon-partial "
        "intervals (ADR-0014 §3), so they average a truncated final interval "
        "into a window reported as 12,000 ticks; and the arms ran without "
        "<code>-drivers</code>, at about 1.5% uncontrolled coasting against a "
        "0.1% bar, so part of the fleet had no car-following control. The "
        "paired design means the RANKING is likely to survive both; the "
        "absolute speeds are not a measurement of the scenario as written. "
        "Lane widening is <b>inconclusive</b>, not disproven: added lanes "
        "carry only ~6.6% of their corridor's traffic in this model, so a "
        "widening result cannot separate \"it doesn't help\" from \"we "
        "didn't really widen it.\" Expressway mainlines run faster than the "
        "real thing; the downtown grid and Lake Shore Drive are realistic.",
    "chi-loop-cbd":
        "The downtown grid is the part of this model that matches reality "
        "best — signal density and block spacing are imported, not assumed. "
        "There is no transit, and downtown Chicago's real transit share is "
        "very high, so absolute car volumes are an overestimate. At the "
        "full 12,000-tick horizon the saturated grid refuses part of "
        "demand at injection (first expiry at tick 7,327), and the loss "
        "varies by arm — 93.6% of demand delivered in the baseline, 94.3% "
        "in the winning arm. Pairing absorbs the shared part; a "
        "load-dependent bias cannot be excluded.",
    "chi-kennedy":
        "Expressway mainlines run faster than the real thing (59-76 km/h "
        "against a real 25-45), so corridor speeds are optimistic. Lane "
        "widening is inconclusive here for the same reason as the full "
        "network: the router barely uses the added lanes. Every arm ran on "
        "one driver replica; at this fleet size (~1,700 vehicles) that "
        "stays under the 0.1% uncontrolled-coasting bar — the baseline "
        "recording of this same scenario in that configuration logs 0.08%.",
}

# Options whose result depends on a number WE supplied rather than one the
# engine derived. The frontage road is the whole category so far: the kernel
# has no route-choice model, so "15% of ramp traffic diverts" is an input
# written into the demand file (an ADR-0021 weighted destination on a second
# flow from the same portal), not a behaviour the simulation discovered.
# Surfaced on the card rather than only in the post-reveal caveat — a guest
# picking between four options should know which one is partly an assumption
# before they pick, not after.
ASSUMPTIONS = {
    ("merge-pod", "frontage-road"):
        "Assumes 15% of ramp traffic diverts. The engine has no route-choice "
        "model, so that share is an input we wrote into the demand, not a "
        "result — read this as \u201cwhat a diversion this size buys\u201d.",
    # Junction control is the other place an assumption hides, and it is
    # easier to miss than a demand split because it lives in the network
    # rather than in a number anyone quotes. Both new-road options declare
    # their control, not just the one that loses — otherwise the disclosure
    # itself would be a thumb on the scale. The two roads are deliberately
    # NOT treated alike, because they are not alike: connector-south cuts
    # four arterials at grade and is signalised there, bypass-north crosses
    # nothing and keeps priority at its two T-junctions.
    ("bottleneck-town", "connector-south"):
        "Signalised where it crosses the four cross streets — four new "
        "lights on the same 86 s fixed-time cycle the rest of the town runs, "
        "with an even two-phase split. It keeps priority only at the two "
        "T-junctions where it leaves and rejoins Main Street. An earlier "
        "build made the new road the priority leg at all six, so four "
        "existing arterials had to yield to it; that bought the connector "
        "time straight out of the cross streets, which is not what building "
        "this road would actually entail.",
    ("bottleneck-town", "bypass-north"):
        "Unsignalised. It crosses nothing — it leaves and rejoins Main "
        "Street at two T-junctions and is the priority leg at both, which is "
        "how a trunk route past a town is normally built.",
}

CSS = """
:root{--bg:#0d1117;--card:#161b22;--edge:#30363d;--ink:#e6edf3;--dim:#8b949e;
--win:#3fb950;--bad:#f85149;--meh:#8b949e;--accent:#58a6ff}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);
font:16px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
.wrap{max-width:1100px;margin:0 auto;padding:2rem 1.25rem 4rem}
h1{font-size:1.6rem;margin:0 0 .25rem;letter-spacing:-.02em}
.sub{color:var(--dim);margin:0 0 1.5rem;font-size:.95rem}
nav{display:flex;gap:.5rem;flex-wrap:wrap;margin-bottom:1.5rem}
nav button{background:var(--card);color:var(--dim);border:1px solid var(--edge);
border-radius:999px;padding:.5rem 1rem;font:inherit;font-size:.9rem;cursor:pointer}
nav button[aria-selected=true]{background:var(--accent);color:#04121f;
border-color:var(--accent);font-weight:600}
.ctx{color:var(--dim);font-size:.9rem;margin:0 0 1.25rem}
/* The setup slide sits at option-card width rather than full bleed, so the
   network is the same size here as on the cards below it — a network that
   changes size between the setup and the options reads as a different one. */
#setup{background:var(--card);border:1px solid var(--edge);border-radius:12px;
padding:1rem 1.2rem;margin:0 0 1rem;max-width:calc(50% - .5rem)}
#setup svg,#setup img{display:block;width:100%;height:auto}
.lgnd{display:flex;flex-wrap:wrap;gap:.5rem .9rem;align-items:center;
margin-top:.6rem;font-size:.78rem;color:var(--dim)}
.lgnd .sw{display:inline-flex;align-items:center;gap:.3rem}
.lgnd .sw i{width:.85rem;height:.85rem;border-radius:2px;display:inline-block}
.lgnd-note{flex-basis:100%;color:var(--dim)}
@media (max-width:720px){#setup{max-width:none}}
/* Exactly two columns: four options auto-fitted at 1100px wrap 3+1, which
   reads as a ranking rather than a set of equals. */
.grid{display:grid;grid-template-columns:repeat(2,1fr);gap:1rem}
@media (max-width:720px){.grid{grid-template-columns:1fr}}
.opt{background:var(--card);border:1px solid var(--edge);border-radius:12px;
padding:1.1rem 1.2rem;cursor:pointer;text-align:left;font:inherit;color:inherit;
transition:border-color .15s,transform .15s;
/* A button centres its content, and the grid stretches every card to the
   tallest in the row — so an option carrying a timing strip pushes its
   neighbour's text into the middle of an empty card. Top-align instead. */
display:flex;flex-direction:column;align-items:stretch;justify-content:flex-start}
.opt:hover{border-color:var(--accent)}
.opt[aria-pressed=true]{border-color:var(--accent);box-shadow:0 0 0 1px var(--accent)}
.opt .n{color:var(--dim);font-size:.75rem;letter-spacing:.08em;text-transform:uppercase}
.opt .l{font-size:1.05rem;font-weight:600;margin-top:.35rem;line-height:1.35}
/* The diagrams are generated against one shared bounding box per scenario,
   so they only stay comparable if the cards render them at one shared
   width — hence a fixed block here rather than intrinsic SVG sizing. */
.opt .dg{margin:.5rem 0 .1rem}
.opt .dg svg{display:block;width:100%;height:auto}
.res{margin-top:.9rem;padding-top:.9rem;border-top:1px solid var(--edge);display:none}
.revealed .res{display:block}
.revealed .opt.win{border-color:var(--win);box-shadow:0 0 0 1px var(--win)}
.revealed .opt.bad{border-color:var(--bad)}
.d{font-size:1.7rem;font-weight:700;letter-spacing:-.02em}
.d.win{color:var(--win)}.d.bad{color:var(--bad)}.d.meh{color:var(--meh)}
.v{font-size:.85rem;color:var(--dim);margin-top:.2rem}
.note{font-size:.8rem;color:#d29922;margin-top:.5rem;line-height:1.4}
.bar{display:flex;gap:.75rem;align-items:center;margin:1.5rem 0 0;flex-wrap:wrap}
.bar button{background:var(--accent);color:#04121f;border:0;border-radius:8px;
padding:.6rem 1.2rem;font:inherit;font-weight:600;cursor:pointer}
.bar button.ghost{background:transparent;color:var(--dim);border:1px solid var(--edge)}
.hint{color:var(--dim);font-size:.8rem}
/* The setup slide is the whole of page 1, so it gets the full column rather
   than the half-width it had when it sat above the option grid. */
#p0 #setup{max-width:none}
.watch{margin:1rem 0 0}
.watch a{color:var(--accent);font-weight:600;text-decoration:none;
border-bottom:1px solid rgba(88,166,255,.4)}
.watch a:hover{border-bottom-color:var(--accent)}
.watch .hint{display:block;margin-top:.35rem}
/* An option whose result rests on a number we supplied rather than one the
   engine derived. Shown on the card, not only under the reveal: a guest
   choosing between four options deserves to know which one is partly an
   assumption BEFORE picking, not after. */
.opt .asm{font-size:.78rem;color:#d29922;margin-top:.5rem;line-height:1.4}
footer{margin-top:3rem;padding-top:1.5rem;border-top:1px solid var(--edge);
color:var(--dim);font-size:.82rem;line-height:1.6}
footer b{color:var(--ink);font-weight:600}
@media print{body{background:#fff;color:#000}}
"""

JS = """
const D = DATA__, keys = Object.keys(D);
// page 0 = the baseline, page 1 = the options. Two pages per scenario so
// neither one needs scrolling on stage: the setup and the menu are separate
// beats in the telling, and a guest scrolling back up to remember where the
// traffic came from has already lost the thread.
let cur = keys[0], page = 0, picked = {}, shown = {};
const $ = s => document.querySelector(s);

function pct(x){ return (x>0?'+':'') + x.toFixed(1) + '%'; }
function cls(o, isWin){
  if (isWin) return 'win';
  if ((o.delta_pct||0) <= -1) return 'bad';
  return 'meh';
}
// The bake URL has to be ABSOLUTE — the manifest URL is the base every chunk
// URL resolves against, and a root-relative one throws "Invalid base URL".
// Composed from location.origin rather than baked in, so the page works on
// whatever host and port is serving it.
function bakeHref(b){
  // The map app lives at /app.html — / is the splash landing page.
  return location.origin + '/app.html?bake=' + location.origin + b.bake +
         '&center=' + b.center + '&zoom=' + b.zoom;
}
function render(){
  const d = D[cur];
  $('#ctx').textContent = d.context;
  $('#seeds').textContent = d.seeds + ' paired seeds · ' + d.ticks +
    ' ticks · primary metric ' + d.metric;
  // Per-scenario, because the limits of a 55k-lane OSM import are not the
  // limits of an authored pod. Hidden entirely when a scenario declares none
  // rather than falling back to another scenario's caveats.
  const cav = $('#caveat');
  cav.innerHTML = d.caveat ? '<b>What this one can\\'t tell you.</b> ' + d.caveat : '';
  cav.style.display = d.caveat ? '' : 'none';
  document.querySelectorAll('nav button').forEach(b =>
    b.setAttribute('aria-selected', String(b.dataset.k === cur)));

  // PAGE 0 — the baseline. Setup slide, and a link to watch the run it was
  // measured on. The link is deliberately the same baked replay the options
  // were scored against, not a prettier one.
  const on0 = page === 0;
  $('#p0').style.display = on0 ? '' : 'none';
  $('#p1').style.display = on0 ? 'none' : '';
  const st = $('#setup');
  st.innerHTML = d.setup || '';
  st.style.display = d.setup ? '' : 'none';
  const wb = $('#watch');
  if (d.baseline) {
    wb.href = bakeHref(d.baseline);
    $('#watchnote').textContent = d.baseline.window || '';
    wb.parentElement.style.display = '';
  } else {
    wb.parentElement.style.display = 'none';
  }
  $('#pagehint').textContent = on0
    ? 'Watch the baseline, then show the four options.'
    : 'Pick one, then reveal.';

  const g = $('#grid');
  g.innerHTML = '';
  d.options.forEach((o, i) => {
    const isWin = o.name === d.winner;
    const b = document.createElement('button');
    b.className = 'opt' + (shown[cur] ? ' ' + cls(o, isWin) : '');
    b.setAttribute('aria-pressed', String(picked[cur] === i));
    b.onclick = () => { picked[cur] = (picked[cur] === i ? -1 : i); render(); };
    let h = '<div class="n">Option ' + (i+1) + '</div>';
    // Diagram above the words: on camera the guest has a few seconds to
    // tell four options apart, and "bypass north" vs "relief road south"
    // is the same sentence twice. Absent for scenarios with no diagrams
    // generated rather than substituted from another scenario.
    if (o.diagram) h += '<div class="dg">' + o.diagram + '</div>';
    h += '<div class="l">' + o.label + '</div>';
    if (o.assumption) h += '<div class="asm">' + o.assumption + '</div>';
    h += '<div class="res"><div class="d ' + cls(o, isWin) + '">' +
         pct(o.delta_pct) + '</div><div class="v">' + o.verdict +
         ' · p=' + o.p.toFixed(4) + '</div>';
    if (!o.carries_traffic)
      h += '<div class="note">Moves significantly less traffic — the network ' +
           'is doing less work, not doing it better.</div>';
    h += '</div>';
    b.innerHTML = h;
    g.appendChild(b);
  });
  g.className = 'grid' + (shown[cur] ? ' revealed' : '');
  $('#reveal').textContent = shown[cur] ? 'Hide the answer' : 'Reveal the answer';
}
function reveal(){ if (page === 1) { shown[cur] = !shown[cur]; render(); } }
function go(p){ page = p; render(); window.scrollTo(0, 0); }
document.addEventListener('keydown', e => {
  // Scenario keys are 1..min(9,N) — there is no key '10', so a tenth
  // scenario is reachable by click only rather than silently unreachable.
  // Switching scenario always lands on the baseline: the options mean
  // nothing to a room that has not seen the network yet.
  if (e.key >= '1' && e.key <= String(Math.min(9, keys.length)) && !e.metaKey) {
    cur = keys[+e.key - 1]; go(0);
  } else if (e.key === 'ArrowRight') { go(1); }
  else if (e.key === 'ArrowLeft') { go(0); }
  else if (e.key === 'r' || e.key === 'R') { reveal(); }
  else if (e.key === 'Escape') { shown = {}; picked = {}; go(0); }
});
window.addEventListener('DOMContentLoaded', () => {
  const nav = $('nav');
  keys.forEach(k => {
    const b = document.createElement('button');
    b.dataset.k = k; b.textContent = D[k].short;
    b.onclick = () => { cur = k; go(0); };
    nav.appendChild(b);
  });
  $('#toopts').onclick = () => go(1);
  $('#back').onclick = () => go(0);
  $('#reveal').onclick = reveal;
  $('#reset').onclick = () => { shown = {}; picked = {}; render(); };
  render();
});
"""

PAGE = """<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="icon" type="image/svg+xml" href="/favicon.svg" />
<link rel="alternate icon" type="image/png" href="/favicon-32.png" />
<title>Which upgrade actually works? — traffic simulation</title>
<style>%(css)s</style></head><body>
<div class="wrap">
<h1>Which upgrade actually works?</h1>
<p class="sub">Four plausible fixes. Which one helps most? Pick before the
reveal.</p>
<nav></nav>
<p class="ctx" id="ctx"></p>
<div id="p0">
  <div id="setup"></div>
  <p class="watch"><a id="watch" target="_blank" rel="noopener">Watch the
  baseline running &#8594;</a> <span class="hint" id="watchnote"></span></p>
  <div class="bar"><button id="toopts">Show the four options &#8594;</button></div>
</div>
<div id="p1">
  <div class="grid" id="grid"></div>
  <div class="bar">
    <button id="reveal">Reveal the answer</button>
    <button id="back" class="ghost">&#8592; Baseline</button>
    <button id="reset" class="ghost">Reset</button>
  </div>
</div>
<p class="hint" id="pagehint" style="margin-top:.75rem"></p>
<p class="hint" id="seeds" style="margin-top:.25rem"></p>
<p class="hint" style="margin-top:.25rem">keys: <b>%(navkeys)s</b> scenario ·
<b>&#8592; &#8594;</b> page · <b>R</b> reveal · <b>Esc</b> reset</p>
<footer>
<p><b>How this was measured.</b> Every option was simulated against the same
baseline on the same seeds, so the seed's own randomness cancels. The
headline number is network mean speed (Edie's definition over every
vehicle-second in the window); p-values are paired t-tests over the per-seed
differences. Each option is also checked for throughput — raising speed by
carrying less traffic is not an upgrade.</p>
<p id="caveat"></p>
<p>How many options help at all is a property of each menu, not of the city
or the network it is drawn on. Some menus here have exactly one upgrade and
three no-ops; others have several that help and the question is by how much.
Neither shape was arranged in advance — the measurement decided it.</p>
</footer>
</div>
<script>%(js)s</script>
</body></html>
"""


def diagram(root, key, option, omit=()):
    """Inline artwork for one option, or "" when none was generated.

    `omit` names artwork to leave OUT of the page while leaving the file on
    disk. build-quiz.sh's CHI_SKIP_MAP path needs that: the Chicago map and
    its sidecar are tracked files, and deleting them to suppress the slide
    left a stageable deletion in the checkout — one `git add -A` from
    committing the removal of a shipped asset. Suppression is a property of
    this build, not a reason to destroy the artifact.

    SVG first, then PNG as a data URI. The two authored pods draw as plan
    views (mkoptiondiag.py), which are vector and tiny. The Chicago cut
    cannot be: 55,555 lanes is 10-20 MB of SVG, and the page inlines what it
    shows. Its setup slide is a rendered congestion map instead
    (mkcongestionmap.py), ~150 KB of PNG, which base64 inlines to ~200 KB —
    acceptable for a page that must open on conference wifi with no CDN and
    no fetch, which an SVG of the same map would not be.
    """
    if not root or f"{key}__{option}" in omit:
        return ""
    svg = os.path.join(root, f"{key}__{option}.svg")
    if os.path.exists(svg):
        with open(svg) as f:
            return f.read().strip()
    png = os.path.join(root, f"{key}__{option}.png")
    if os.path.exists(png):
        with open(png, "rb") as f:
            b64 = base64.b64encode(f.read()).decode("ascii")
        alt = (f"{key} network, every lane coloured by its measured speed as "
               f"a share of its posted limit")
        out = (f'<img src="data:image/png;base64,{b64}" '
               f'alt="{html.escape(alt)}">')
        # A colour map nobody can decode is decoration. The legend and the
        # provenance ride WITH the picture, from the sidecar the generator
        # writes — not retyped here, for the same reason no number is.
        # A PNG with no readable sidecar is a build FAILURE, not a quiet
        # downgrade to an uncaptioned image. Without it the page would show a
        # colour map whose bands, measurement window and source run are all
        # unknown — precisely the "drawing of an opinion" the generator's
        # banner disclaims, and indistinguishable on screen from a good one.
        try:
            with open(png + ".json") as f:
                prov = json.load(f)
        except (OSError, ValueError) as exc:
            sys.exit(f"mkquiz: {png} has no readable provenance sidecar "
                     f"({png}.json: {exc}). Regenerate it with "
                     f"scripts/show/mkcongestionmap.py, which writes the "
                     f"sidecar alongside the image.")
        if prov.get("lanes_with_traffic") == 0:
            sys.exit(f"mkquiz: {png} was drawn from a window in which NO lane "
                     f"carried traffic (sidecar lanes_with_traffic=0) — an "
                     f"all-empty map reads as a quiet network. Regenerate it "
                     f"against a window the run actually covers.")
        # The legend is what makes the picture readable: without it the map
        # is coloured shapes with no key, which is the same undecodable
        # artefact the missing-sidecar exit above refuses. Validate the shape
        # too — indexing rgb[0..2] on a hand-edited sidecar would otherwise
        # die with a bare IndexError while every other failure here explains
        # itself.
        def _rgb_ok(c):
            return (isinstance(c, (list, tuple)) and len(c) >= 3
                    and all(isinstance(x, int) for x in c[:3]))

        bands = prov.get("bands")
        if not bands or not all(
                isinstance(b, (list, tuple)) and len(b) == 2
                and isinstance(b[0], (int, float)) and _rgb_ok(b[1])
                for b in bands):
            sys.exit(f"mkquiz: {png}'s sidecar has no usable 'bands' — the "
                     f"map would render with no legend, which is a picture "
                     f"of colours nobody can read. Regenerate it with "
                     f"mkcongestionmap.py.")
        if not _rgb_ok(prov.get("empty_rgb")):
            sys.exit(f"mkquiz: {png}'s sidecar has no usable 'empty_rgb' — "
                     f"the 'no traffic' colour would be unlabelled, and an "
                     f"empty lane reads as a fast one. Regenerate it with "
                     f"mkcongestionmap.py.")
        swatches, lo = [], 0.0
        for hi, rgb in bands:
            # Non-overlapping and half-open [lo, hi). "under 40%" was
            # wrong in the other direction — it also contains the under-20%
            # band, so two swatches claimed the same lane.
            if hi > 1.0:
                label = f"{lo:.0%}+"
            elif lo == 0.0:
                label = f"under {hi:.0%}"
            else:
                label = f"{lo:.0%}\u2013{hi:.0%}"
            swatches.append(
                f'<span class="sw"><i style="background:rgb({rgb[0]},{rgb[1]},'
                f'{rgb[2]})"></i>{label}</span>')
            lo = hi
        e = prov["empty_rgb"]
        swatches.append(
            f'<span class="sw"><i style="background:rgb({e[0]},{e[1]},'
            f'{e[2]})"></i>no traffic</span>')
        n = prov.get("lanes_with_traffic")
        tot = prov.get("lanes_drawn")
        note = "share of each lane\u2019s own posted speed limit"
        # `is not None`: 0 is the case worth printing loudest, not the case
        # to hide — though the guard above means it cannot reach here.
        if n is not None and tot:
            note += f" \u00b7 {n:,} of {tot:,} lanes carried traffic"
        # `is not None`, not truthiness: warmup 0 is the case where saying
        # so matters most, because the fill-up transient is then included.
        eff = prov.get("first_interval_tick")
        if eff is None:
            eff = prov.get("warmup_tick")
        # BOTH ends, not just the start. Dropping partial intervals
        # (ADR-0014) can pull the effective end well short of the run's
        # horizon \u2014 the shipped Chicago map covers 4,000-10,000 of a
        # 12,000-tick run, because the last interval was cut short by the
        # horizon \u2014 and a caption that gives only the start reads as though
        # the window runs to the end of the run.
        end = prov.get("last_interval_end")
        if eff is not None and end is not None:
            note += f" \u00b7 measured over ticks {eff:,}\u2013{end:,}"
        elif eff is not None:
            note += f" \u00b7 measured from tick {eff:,}"
        if prov.get("run_label"):
            note += f" \u00b7 {prov['run_label']}"
        cav = prov.get("note")
        extra = (f'<span class="lgnd-note">{html.escape(cav)}</span>'
                 if cav else "")
        return (out + '<div class="lgnd">' + "".join(swatches) +
                f'<span class="lgnd-note">{html.escape(note)}</span>'
                + extra + '</div>')
    return ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("shortlists", nargs="+",
                    help="curate.py --json outputs, in presentation order")
    ap.add_argument("--out", default="viz/public/quiz.html")
    ap.add_argument("--baselines", default="",
                    help="JSON map of scenario -> baked baseline replay; each "
                         "referenced bake is verified to exist under "
                         "--baked-root so a re-bake breaks the build instead "
                         "of shipping a dead link")
    ap.add_argument("--baked-root", default="data/baked",
                    help="filesystem root the baselines' /baked/... paths "
                         "resolve against, for that existence check")
    ap.add_argument("--diagrams", default="",
                    help="directory of artwork named "
                         "<scenario>__<option>.svg (mkoptiondiag.py plan "
                         "views) or <scenario>__<option>.png "
                         "(mkcongestionmap.py rasters, inlined as data URIs "
                         "with the legend and provenance from the sidecar "
                         "<name>.png.json). SVG wins if both exist; missing "
                         "artwork is simply omitted so a scenario can ship "
                         "text-only")
    ap.add_argument("--omit-diagram", action="append", default=[],
                    metavar="SCENARIO__OPTION",
                    help="leave this artwork out of the page without "
                         "deleting it (repeatable). For suppressing a "
                         "tracked asset in one build — deleting it instead "
                         "leaves a stageable deletion behind")
    args = ap.parse_args()
    omit = set(args.omit_diagram)

    baselines = {}
    if args.baselines:
        with open(args.baselines) as f:
            baselines = {k: v for k, v in json.load(f).items()
                         if not k.startswith("_")}
        for k, b in baselines.items():
            # The bake path is served as /baked/<run>/<hash>/index.json and
            # the hash changes whenever the run is re-recorded, so a stale
            # entry here is a link that 404s live on stage. Fail the build.
            rel = b["bake"].split("/baked/", 1)[-1]
            probe = os.path.join(args.baked_root, rel)
            if not os.path.exists(probe):
                print(f"mkquiz: baseline for {k} not found at {probe} — "
                      f"re-bake changed the content hash, or the bake is not "
                      f"under --baked-root", file=sys.stderr)
                sys.exit(1)

    data = {}
    for path in args.shortlists:
        key = os.path.splitext(os.path.basename(path))[0]
        with open(path) as f:
            d = json.load(f)
        short, ctx = CONTEXT.get(key, (d["title"], ""))
        if d.get("winner") is None:
            print(f"mkquiz: {key} has no significant winner — the page would "
                  f"reveal four no-ops as a game with an answer", file=sys.stderr)
            sys.exit(1)
        data[key] = {
            "short": short, "context": ctx, "title": d["title"],
            "caveat": CAVEATS.get(key, ""),
            "baseline": baselines.get(key),
            # Same inlining rationale as the per-option diagrams below.
            "setup": diagram(args.diagrams, key, "setup", omit),
            "winner": d["winner"], "seeds": d["seeds"], "ticks": d["ticks"],
            "metric": d["metric"],
            "options": [{
                "name": o["name"],
                # The label is the only untrusted-ish string that reaches the
                # DOM via innerHTML; escape it rather than trusting the
                # labels file to stay markup-free.
                "label": html.escape(o["label"]),
                "delta_pct": o["delta_pct"], "p": o["p"],
                "verdict": html.escape(o["verdict"]),
                "carries_traffic": o["carries_traffic"],
                # Inlined, not linked: the page has to render with no
                # network. Not escaped, unlike the label — this is markup
                # this repo generated from the simulated network, and
                # escaping it would print the SVG source.
                "diagram": diagram(args.diagrams, key, o["name"], omit),
                "assumption": html.escape(
                    ASSUMPTIONS.get((key, o["name"]), "")),
            } for o in d["options"]],
        }
        have = sum(1 for o in data[key]["options"] if o["diagram"])
        if args.diagrams and have not in (0, len(d["options"])):
            # A part-illustrated menu is worse than a plain one: the options
            # that got a picture look like the considered ones, and that cue
            # has nothing to do with which answer is right.
            print(f"mkquiz: {key} has diagrams for {have} of "
                  f"{len(d['options'])} options — all or none",
                  file=sys.stderr)
            sys.exit(1)

    js = JS.replace("DATA__", json.dumps(data))
    navkeys = "1" if len(data) == 1 else "1-%d" % min(9, len(data))
    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "w") as f:
        f.write(PAGE % {"css": CSS, "js": js, "navkeys": navkeys})
    print(f"[mkquiz] wrote {args.out} ({len(data)} scenarios)", file=sys.stderr)


if __name__ == "__main__":
    main()
