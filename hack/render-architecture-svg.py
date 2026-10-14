#!/usr/bin/env python3
"""Render the README architecture diagram in a light and a dark variant.

Usage: hack/render-architecture-svg.py docs/images
Writes architecture-light.svg and architecture-dark.svg into the directory;
the README picks one with a <picture> element.
"""
import sys
from xml.sax.saxutils import escape

W, H = 1180, 600
FONT = "-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif"

PALETTES = {
    "light": dict(bg="#ffffff", text="#1f2328", muted="#59636e", line="#59636e",
                  box="#f6f8fa", border="#d1d9e0", accent="#0969da", accent_bg="#ddf4ff",
                  good="#1a7f37", good_bg="#dafbe1", warn="#9a6700", warn_bg="#fff8c5",
                  cluster="#f6f8fa", cluster_border="#8c959f"),
    "dark": dict(bg="#0d1117", text="#e6edf3", muted="#9198a1", line="#9198a1",
                 box="#161b22", border="#3d444d", accent="#58a6ff", accent_bg="#121d2f",
                 good="#3fb950", good_bg="#12261e", warn="#d29922", warn_bg="#272115",
                 cluster="#0d1117", cluster_border="#6e7681"),
}


class Canvas:
    def __init__(self, p):
        self.p = p
        self.out = []

    def rect(self, x, y, w, h, fill, stroke, rx=8, dash=None, sw=1.2):
        d = f' stroke-dasharray="{dash}"' if dash else ""
        self.out.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" '
                        f'stroke="{stroke}" stroke-width="{sw}"{d}/>')

    def text(self, x, y, s, size=13, color=None, weight="normal", anchor="start", family=None):
        color = color or self.p["text"]
        fam = f' font-family="{family}"' if family else ""
        self.out.append(f'<text x="{x}" y="{y}" font-size="{size}" fill="{color}" font-weight="{weight}" '
                        f'text-anchor="{anchor}"{fam}>{escape(s)}</text>')

    def lines(self, x, y, items, size=12, color=None, lh=17, anchor="start"):
        for i, s in enumerate(items):
            self.text(x, y + i * lh, s, size=size, color=color or self.p["muted"], anchor=anchor)

    def box(self, x, y, w, h, title, body=(), kind="box", mono=False):
        p = self.p
        fill, stroke, tcolor = {
            "box": (p["box"], p["border"], p["text"]),
            "accent": (p["accent_bg"], p["accent"], p["accent"]),
            "good": (p["good_bg"], p["good"], p["good"]),
            "warn": (p["warn_bg"], p["warn"], p["warn"]),
        }[kind]
        self.rect(x, y, w, h, fill, stroke)
        self.text(x + 12, y + 22, title, size=13, color=tcolor, weight="600",
                  family="SFMono-Regular, Menlo, Consolas, monospace" if mono else None)
        self.lines(x + 12, y + 42, body)

    def arrow(self, x1, y1, x2, y2, label=None, dash=None, both=False, label_dy=-6):
        p = self.p
        d = f' stroke-dasharray="{dash}"' if dash else ""
        ms = ' marker-start="url(#tail)"' if both else ""
        self.out.append(f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{p["line"]}" stroke-width="1.4"'
                        f'{d} marker-end="url(#head)"{ms}/>')
        if label:
            self.text((x1 + x2) / 2, (y1 + y2) / 2 + label_dy, label, size=11, color=p["muted"], anchor="middle")

    def render(self):
        p = self.p
        head = (f'<defs><marker id="head" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">'
                f'<path d="M0,0 L8,4 L0,8 z" fill="{p["line"]}"/></marker>'
                f'<marker id="tail" markerWidth="8" markerHeight="8" refX="1" refY="4" orient="auto">'
                f'<path d="M8,0 L0,4 L8,8 z" fill="{p["line"]}"/></marker></defs>')
        return (f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
                f'font-family="{FONT}">{head}<rect width="{W}" height="{H}" fill="{p["bg"]}"/>'
                + "\n".join(self.out) + "</svg>\n")


def draw(p):
    c = Canvas(p)

    # Left: the person and Anthropic.
    c.box(30, 60, 200, 92, "You", [
        "claude.ai, the Claude Code CLI",
        "or a Claude Code session",
        "picks the self-hosted environment",
    ])
    c.box(30, 250, 200, 108, "Anthropic control plane", [
        "holds the environment and",
        "queues its sessions; sends",
        "a signed work order per",
        "session to the orchestrator",
    ])
    c.arrow(130, 152, 130, 250, "start a session")

    # The cluster.
    cx, cy, cw, ch = 300, 30, 850, 540
    c.rect(cx, cy, cw, ch, p["cluster"], p["cluster_border"], rx=14, dash="6 4", sw=1.4)
    c.text(cx + 18, cy + 26, "Your Kubernetes cluster", size=14, weight="600")
    c.text(cx + cw - 18, cy + 26, "no inbound ports: every pod dials out to Anthropic",
           size=11, color=p["muted"], anchor="end")

    # Operator and the CR.
    c.box(cx + 20, cy + 50, 210, 96, "Operator", [
        "watches ClaudeEnvironments,",
        "builds every pod hardened,",
        "rolls on Secret change",
    ])
    c.box(cx + 300, cy + 50, 250, 96, "ClaudeEnvironment", [
        "mode: fixed | onDemand",
        "runner image, environment key Secret,",
        "hooks, wrapper, init containers, egress",
    ], kind="accent", mono=True)
    c.box(cx + 620, cy + 50, 210, 96, "Secret", [
        "the environment key from",
        "claude.ai, mounted read-only",
        "as a file, never an argument",
    ])
    c.arrow(cx + 230, cy + 98, cx + 300, cy + 98, "reconciles", label_dy=-8)
    c.arrow(cx + 550, cy + 98, cx + 620, cy + 98, "refs", label_dy=-8)

    # Fixed lane.
    lx, ly, lw, lh = cx + 20, cy + 180, 395, 240
    c.rect(lx, ly, lw, lh, p["bg"], p["border"], rx=10)
    c.text(lx + 14, ly + 24, "Fixed fleet", size=13, weight="600")
    c.text(lx + 14, ly + 42, "spec.fixed.replicas, a Deployment of long-lived runners",
           size=11, color=p["muted"])
    for i in range(3):
        c.box(lx + 14 + i * 122, ly + 60, 112, 70, "runner pod", [
            "long-lived,",
            "polls for work",
        ], kind="good")
    c.text(lx + 14, ly + 160, "You size the capacity. The operator rolls the", size=11, color=p["muted"])
    c.text(lx + 14, ly + 177, "Deployment when the Secret or the spec changes,", size=11, color=p["muted"])
    c.text(lx + 14, ly + 194, "with a grace period for sessions in flight.", size=11, color=p["muted"])

    # On-demand lane.
    ox, oy, ow, oh = cx + 435, cy + 180, 395, 240
    c.rect(ox, oy, ow, oh, p["bg"], p["border"], rx=10)
    c.text(ox + 14, oy + 24, "On-demand", size=13, weight="600")
    c.text(ox + 14, oy + 42, "spec.onDemand, one hardened pod per session", size=11, color=p["muted"])
    c.box(ox + 14, oy + 60, 150, 70, "orchestrator pod", [
        "holds the key, runs",
        "the spawn hook",
    ], kind="good")
    c.box(ox + 230, oy + 60, 150, 70, "ClaudeRunner", [
        "one per session,",
        "Pending to Succeeded",
    ], kind="accent", mono=True)
    c.arrow(ox + 164, oy + 95, ox + 230, oy + 95, "spawn hook", label_dy=-8)
    c.box(ox + 230, oy + 150, 150, 70, "runner pod", [
        "work-order JWT only,",
        "garbage-collected after",
    ], kind="good")
    c.arrow(ox + 305, oy + 130, ox + 305, oy + 150)
    c.text(ox + 14, oy + 160, "The operator starts the pod only for", size=11, color=p["muted"])
    c.text(ox + 14, oy + 177, "its own environment; an admission", size=11, color=p["muted"])
    c.text(ox + 14, oy + 194, "policy confines the orchestrator to", size=11, color=p["muted"])
    c.text(ox + 14, oy + 211, "its own work orders.", size=11, color=p["muted"])

    # Outbound connections to Anthropic.
    c.arrow(230, 300, lx, 300, "outbound only", dash="4 3", both=True, label_dy=-8)

    # Hardening strip.
    hx, hy, hw, hh = cx + 20, cy + 440, 810, 90
    c.rect(hx, hy, hw, hh, p["warn_bg"], p["warn"], rx=10)
    c.text(hx + 14, hy + 24, "Every pod the operator builds", size=13, color=p["warn"], weight="600")
    c.lines(hx + 14, hy + 44, [
        "Restricted Pod Security Standard: non-root, no privilege escalation, all capabilities dropped, "
        "read-only root filesystem, seccomp RuntimeDefault.",
        "Optional default-deny egress NetworkPolicy with your allow-list. Prometheus metrics and "
        "OpenTelemetry traces built in.",
        "Operator image signed with cosign and shipped with an SBOM.",
    ], size=11, color=p["text"], lh=16)

    return c.render()


def main():
    out_dir = sys.argv[1]
    for name, palette in PALETTES.items():
        with open(f"{out_dir}/architecture-{name}.svg", "w", encoding="utf-8") as f:
            f.write(draw(palette))


if __name__ == "__main__":
    main()
