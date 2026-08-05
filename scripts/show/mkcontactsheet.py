#!/usr/bin/env python3
"""One page showing every diagram the quiz can render, at card width.

    mkcontactsheet.py docs/show/diag

The quiz page shows one scenario at a time and hides the answers, which is
right for a guest and wrong for reviewing the artwork: checking that the
bypass and the relief road are actually distinguishable means seeing them
side by side, at the width they will be shown at, without clicking through
tabs. The setup slide sorts first in each group so the review order matches
the presentation order.

Self-contained like the quiz for the same reason — the SVGs are inlined, so
this file opens from disk with no server.
"""
import argparse
import glob
import html
import os

CSS = """body{margin:0;background:#0d1117;color:#e6edf3;
font:15px/1.5 ui-sans-serif,system-ui,-apple-system,sans-serif}
.w{max-width:1100px;margin:0 auto;padding:2rem 1.25rem 4rem}
h1{font-size:1.4rem;margin:0 0 .25rem}
h2{font-size:1rem;color:#8b949e;margin:2rem 0 .75rem;font-weight:600}
p{color:#8b949e}
/* Two columns at the same width the quiz uses, so a diagram that is
   illegible here is illegible there. */
.g{display:grid;grid-template-columns:repeat(2,1fr);gap:1rem}
@media (max-width:720px){.g{grid-template-columns:1fr}}
.c{background:#161b22;border:1px solid #30363d;border-radius:12px;
padding:1rem 1.2rem}
.c .n{color:#8b949e;font-size:.78rem;letter-spacing:.06em;
text-transform:uppercase;margin-bottom:.5rem}
.c svg{display:block;width:100%;height:auto}"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("dir", help="directory of <pod>__<option>.svg files")
    ap.add_argument("--out", default="")
    args = ap.parse_args()
    out = args.out or os.path.join(args.dir, "index.html")

    by = {}
    for path in sorted(glob.glob(os.path.join(args.dir, "*.svg"))):
        name = os.path.basename(path)[:-4]
        if "__" not in name:
            continue
        pod, opt = name.split("__", 1)
        with open(path) as f:
            by.setdefault(pod, []).append((opt, f.read().strip()))

    doc = ['<!doctype html><meta charset="utf-8">',
           "<title>option diagrams — contact sheet</title>",
           f"<style>{CSS}</style>",
           '<div class="w"><h1>Option diagrams — contact sheet</h1>',
           "<p>Every diagram the quiz page can show, at card width. "
           "Regenerate with <code>scripts/show/build-quiz.sh</code>.</p>"]
    for pod in sorted(by):
        doc.append(f'<h2>{html.escape(pod)}</h2><div class="g">')
        for opt, svg in sorted(by[pod], key=lambda kv: (kv[0] != "setup", kv[0])):
            doc.append(f'<div class="c"><div class="n">{html.escape(opt)}'
                       f"</div>{svg}</div>")
        doc.append("</div>")
    doc.append("</div>")

    with open(out, "w") as f:
        f.write("\n".join(doc))
    print(f"[sheet] {sum(len(v) for v in by.values())} diagrams -> {out}")


if __name__ == "__main__":
    main()
