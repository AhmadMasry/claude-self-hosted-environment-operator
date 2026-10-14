#!/usr/bin/env python3
"""Render a transcript as an animated terminal SVG for the README.

Usage: hack/render-terminal-svg.py docs/images/user-flow.txt > docs/images/user-flow.svg

Lines appear one after another (SMIL animation, which GitHub renders inline).
Lines starting with "== " are step headings; "$ " is the prompt; a line
containing "passed" is the final result. The width follows the longest line.
"""
import sys
from xml.sax.saxutils import escape

lines = open(sys.argv[1], encoding="utf-8").read().rstrip("\n").split("\n")
line_h, pad_x, top = 22, 18, 52
width = max(980, int(max(len(l) for l in lines) * 7.5) + 2 * pad_x)  # Menlo at 13px
height = top + line_h * len(lines) + 24
fg, dim, green, blue, bg = "#cdd6f4", "#9399b2", "#a6e3a1", "#89b4fa", "#1e1e2e"

out = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" font-family="SFMono-Regular, Menlo, Consolas, monospace" font-size="12.5">',
       f'<rect width="{width}" height="{height}" rx="10" fill="{bg}"/>',
       '<circle cx="22" cy="20" r="6" fill="#f38ba8"/><circle cx="42" cy="20" r="6" fill="#f9e2af"/><circle cx="62" cy="20" r="6" fill="#a6e3a1"/>',
       f'<text x="{width/2}" y="25" text-anchor="middle" fill="{dim}" font-size="12">a session on the operator</text>']
t = 0.0
for i, line in enumerate(lines):
    if line.startswith("$ "):
        color, weight, step = blue, "bold", 0.9
    elif line.startswith("== "):
        color, weight, step = green, "bold", 0.8
    elif "passed" in line:
        color, weight, step = green, "bold", 0.6
    else:
        color, weight, step = fg, "normal", 0.45
    y = top + line_h * i
    out.append(f'<text x="{pad_x}" y="{y}" fill="{color}" font-weight="{weight}" opacity="0" xml:space="preserve">{escape(line)}'
               f'<animate attributeName="opacity" from="0" to="1" begin="{t:.2f}s" dur="0.05s" fill="freeze"/></text>')
    t += step
out.append("</svg>")
print("\n".join(out))
