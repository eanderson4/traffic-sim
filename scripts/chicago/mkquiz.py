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
import html
import json
import os
import sys

# Per-scenario framing. Kept here rather than in the reports because it is
# presentation, not measurement: what the guest needs to know to make the
# choice make sense.
CONTEXT = {
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
/* Exactly two columns: four options auto-fitted at 1100px wrap 3+1, which
   reads as a ranking rather than a set of equals. */
.grid{display:grid;grid-template-columns:repeat(2,1fr);gap:1rem}
@media (max-width:720px){.grid{grid-template-columns:1fr}}
.opt{background:var(--card);border:1px solid var(--edge);border-radius:12px;
padding:1.1rem 1.2rem;cursor:pointer;text-align:left;font:inherit;color:inherit;
transition:border-color .15s,transform .15s}
.opt:hover{border-color:var(--accent)}
.opt[aria-pressed=true]{border-color:var(--accent);box-shadow:0 0 0 1px var(--accent)}
.opt .n{color:var(--dim);font-size:.75rem;letter-spacing:.08em;text-transform:uppercase}
.opt .l{font-size:1.05rem;font-weight:600;margin-top:.35rem;line-height:1.35}
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
footer{margin-top:3rem;padding-top:1.5rem;border-top:1px solid var(--edge);
color:var(--dim);font-size:.82rem;line-height:1.6}
footer b{color:var(--ink);font-weight:600}
@media print{body{background:#fff;color:#000}}
"""

JS = """
const D = DATA__, keys = Object.keys(D);
let cur = keys[0], picked = {}, shown = {};
const $ = s => document.querySelector(s);

function pct(x){ return (x>0?'+':'') + x.toFixed(1) + '%'; }
function cls(o, isWin){
  if (isWin) return 'win';
  if ((o.delta_pct||0) <= -1) return 'bad';
  return 'meh';
}
function render(){
  const d = D[cur];
  $('#ctx').textContent = d.context;
  $('#seeds').textContent = d.seeds + ' paired seeds · ' + d.ticks +
    ' ticks · primary metric ' + d.metric;
  document.querySelectorAll('nav button').forEach(b =>
    b.setAttribute('aria-selected', String(b.dataset.k === cur)));
  const g = $('#grid');
  g.innerHTML = '';
  d.options.forEach((o, i) => {
    const isWin = o.name === d.winner;
    const b = document.createElement('button');
    b.className = 'opt' + (shown[cur] ? ' ' + cls(o, isWin) : '');
    b.setAttribute('aria-pressed', String(picked[cur] === i));
    b.onclick = () => { picked[cur] = (picked[cur] === i ? -1 : i); render(); };
    let h = '<div class="n">Option ' + (i+1) + '</div><div class="l">' +
            o.label + '</div>';
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
function reveal(){ shown[cur] = !shown[cur]; render(); }
document.addEventListener('keydown', e => {
  if (e.key >= '1' && e.key <= String(keys.length) && !e.metaKey) {
    cur = keys[+e.key - 1]; render();
  } else if (e.key === 'r' || e.key === 'R') { reveal(); }
  else if (e.key === 'Escape') { shown = {}; picked = {}; render(); }
});
window.addEventListener('DOMContentLoaded', () => {
  const nav = $('nav');
  keys.forEach(k => {
    const b = document.createElement('button');
    b.dataset.k = k; b.textContent = D[k].short;
    b.onclick = () => { cur = k; render(); };
    nav.appendChild(b);
  });
  $('#reveal').onclick = reveal;
  $('#reset').onclick = () => { shown = {}; picked = {}; render(); };
  render();
});
"""

PAGE = """<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Which upgrade actually works? — Chicago traffic simulation</title>
<style>%(css)s</style></head><body>
<div class="wrap">
<h1>Which upgrade actually works?</h1>
<p class="sub">Four plausible fixes. One of them measurably helps. Pick before
the reveal.</p>
<nav></nav>
<p class="ctx" id="ctx"></p>
<div class="grid" id="grid"></div>
<div class="bar">
  <button id="reveal">Reveal the answer</button>
  <button id="reset" class="ghost">Reset</button>
  <span class="hint">keys: <b>1-3</b> scenario · <b>R</b> reveal · <b>Esc</b> reset</span>
</div>
<p class="hint" id="seeds" style="margin-top:.75rem"></p>
<footer>
<p><b>How this was measured.</b> Every option was simulated against the same
baseline on the same seeds, so the seed's own randomness cancels. The
headline number is network mean speed (Edie's definition over every
vehicle-second in the window); p-values are paired t-tests over the per-seed
differences. Each option is also checked for throughput — raising speed by
carrying less traffic is not an upgrade.</p>
<p><b>What it can't tell you.</b> Lane widening is <b>inconclusive</b>, not
disproven: added lanes carry only ~6.6%% of their corridor's traffic in this
model, so a widening result cannot separate "it doesn't help" from "we didn't
really widen it." Expressway mainlines run faster than the real thing
(59-76 km/h against a real 25-45); the downtown grid and Lake Shore Drive are
realistic. "Exactly one winner" is a property of this menu, not of Chicago.</p>
</footer>
</div>
<script>%(js)s</script>
</body></html>
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("shortlists", nargs="+",
                    help="curate.py --json outputs, in presentation order")
    ap.add_argument("--out", default="viz/public/quiz.html")
    args = ap.parse_args()

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
            } for o in d["options"]],
        }

    js = JS.replace("DATA__", json.dumps(data))
    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "w") as f:
        f.write(PAGE % {"css": CSS, "js": js})
    print(f"[mkquiz] wrote {args.out} ({len(data)} scenarios)", file=sys.stderr)


if __name__ == "__main__":
    main()
