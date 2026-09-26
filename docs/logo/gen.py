"""Generates the mote logo (docs/logo/mote-{dark,light}.svg).

The wordmark is DejaVu Sans Mono Bold converted to outlines so it renders the
same everywhere. Needs fontTools (`pip install fonttools`) and the DejaVu fonts.
Run: python3 docs/logo/gen.py
"""
import os
from fontTools.ttLib import TTFont
from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen

FONT = TTFont("/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf")
OUT = os.path.dirname(os.path.abspath(__file__))

THEMES = {
    "dark": {"fg": "#e6e6f0", "stops": ("#7dd3fc", "#a78bfa", "#e879f9"), "mote": "#fbbf24"},
    "light": {"fg": "#1f2330", "stops": ("#0284c7", "#7c3aed", "#c026d3"), "mote": "#d97706"},
}


def outline(text, size, x, y):
    """Returns the SVG path, the advance width and the ink bounds."""
    gs, cmap = FONT.getGlyphSet(), FONT.getBestCmap()
    s = size / FONT["head"].unitsPerEm
    pen, bounds, cx = SVGPathPen(gs), BoundsPen(gs), x
    for ch in text:
        g = cmap[ord(ch)]
        gs[g].draw(TransformPen(pen, (s, 0, 0, -s, cx, y)))
        gs[g].draw(TransformPen(bounds, (s, 0, 0, -s, cx, y)))
        cx += gs[g].width * s
    return pen.getCommands(), cx - x, bounds.bounds


# Motes leave the top of the cursor, rise and drift right while fading.
# (start x offset, rise, drift, radius, duration s, delay s)
MOTES = [(6, 58, 26, 5.8, 2.4, 0.0), (18, 70, 44, 4.2, 2.9, 0.6), (12, 50, 12, 3.4, 2.2, 1.2),
         (24, 64, 58, 2.9, 3.1, 1.7), (8, 76, 34, 2.4, 2.7, 2.3), (20, 46, 50, 4.6, 2.5, 0.3)]


def logo(theme):
    t = THEMES[theme]
    a, b, c = t["stops"]
    size, base = 88, 0
    # Lay out at the origin, then centre the ink (chevron to cursor) in the
    # canvas with equal margins that are large enough for the motes.
    chev, w1, cb = outline("›", size, 0, base)
    word, w2, wb = outline("mote", size, w1 + 14, base)
    cx = w1 + 14 + w2 + 12
    cw, ch = 30, 66
    top = base - ch
    left, right = cb[0], cx + cw
    ink_top, ink_bottom = min(cb[1], wb[1], top), max(cb[3], wb[3], base)
    reach_x = max(dx + drift + r for dx, _, drift, r, _, _ in MOTES) - cw
    reach_y = max(rise + r - 6 for _, rise, _, r, _, _ in MOTES) - (top - ink_top)
    pad_x, pad_y = max(reach_x, 16) + 4, max(reach_y, 12) + 4
    width = round(right - left + 2 * pad_x)
    height = round(ink_bottom - ink_top + 2 * pad_y)
    tx, ty = pad_x - left, pad_y - ink_top
    motes = []
    for dx, rise, drift, r, dur, delay in MOTES:
        x0, y0 = cx + dx, top + 6
        motes.append(
            f'<circle cx="{x0:.1f}" cy="{y0}" r="{r}" fill="{t["mote"]}" opacity="0">'
            f'<animate attributeName="cy" values="{y0};{y0 - rise}" dur="{dur}s" begin="{delay}s" repeatCount="indefinite"/>'
            f'<animate attributeName="cx" values="{x0:.1f};{x0 + drift:.1f}" dur="{dur}s" begin="{delay}s" repeatCount="indefinite"/>'
            f'<animate attributeName="opacity" values="0;1;0.8;0" keyTimes="0;0.15;0.6;1" dur="{dur}s" begin="{delay}s" repeatCount="indefinite"/>'
            f'<animate attributeName="r" values="{r};{r * 0.45:.1f}" dur="{dur}s" begin="{delay}s" repeatCount="indefinite"/>'
            f"</circle>")
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {width} {height}" width="{width}" height="{height}" role="img" aria-label="mote">'
        f'<defs>'
        f'<linearGradient id="word" gradientUnits="userSpaceOnUse" x1="{left:.1f}" y1="0" x2="{right:.1f}" y2="0">'
        f'<stop offset="0" stop-color="{a}"/><stop offset="0.5" stop-color="{b}"/><stop offset="1" stop-color="{c}"/></linearGradient>'
        f'<linearGradient id="cursor" x1="0" y1="0" x2="0.35" y2="1">'
        f'<stop offset="0" stop-color="{a}"/><stop offset="0.5" stop-color="{b}"/><stop offset="1" stop-color="{c}"/></linearGradient>'
        f'</defs>'
        f'<g transform="translate({tx:.1f} {ty:.1f})">'
        f'<path d="{chev}" fill="url(#word)"/>'
        f'<path d="{word}" fill="{t["fg"]}"/>'
        f'<rect x="{cx:.1f}" y="{top}" width="{cw}" height="{ch}" rx="3" fill="url(#cursor)">'
        f'<animate attributeName="opacity" values="1;0" dur="1.06s" calcMode="discrete" repeatCount="indefinite"/></rect>'
        + "".join(motes) + "</g></svg>\n")


for theme in THEMES:
    with open(os.path.join(OUT, f"mote-{theme}.svg"), "w") as f:
        f.write(logo(theme))
