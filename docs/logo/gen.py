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


# The cursor blinks on a steady, clock-like beat; motes ride the same clock so
# they only exist during the invisible half and are always gone before it
# reappears.
CYCLE = 1.06  # full blink period, seconds (matches a regular cursor blink)
ON_FRAC = 0.5  # fraction of CYCLE the cursor is visible

# Motes leave the top of the cursor, rise and drift right while fading, each
# living entirely inside the cursor's off-phase (t1..t3, as a fraction of CYCLE).
# Kept sparse so the mark stays calm.
# (start x offset, rise, drift, radius, t1, t3)
MOTES = [(4, 42, 14, 3.8, 0.52, 0.80), (10, 46, 20, 4.6, 0.54, 0.86), (20, 38, 34, 3.4, 0.62, 0.93)]


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
    for dx, rise, drift, r, t1, t3 in MOTES:
        x0, y0 = cx + dx, top + 6
        t2 = t1 + 0.22 * (t3 - t1)
        f2 = (t2 - t1) / (t3 - t1)
        key = f"0;{t1:.3f};{t2:.3f};{t3:.3f};1"
        motes.append(
            f'<circle cx="{x0:.1f}" cy="{y0}" r="{r}" fill="{t["mote"]}" opacity="0">'
            f'<animate attributeName="cy" values="{y0};{y0};{y0 - rise * f2:.2f};{y0 - rise:.2f};{y0 - rise:.2f}" keyTimes="{key}" dur="{CYCLE}s" repeatCount="indefinite"/>'
            f'<animate attributeName="cx" values="{x0:.1f};{x0:.1f};{x0 + drift * f2:.2f};{x0 + drift:.1f};{x0 + drift:.1f}" keyTimes="{key}" dur="{CYCLE}s" repeatCount="indefinite"/>'
            f'<animate attributeName="opacity" values="0;0;1;0.3;0" keyTimes="{key}" dur="{CYCLE}s" repeatCount="indefinite"/>'
            f'<animate attributeName="r" values="{r};{r};{r - r * 0.6 * f2:.2f};{r * 0.4:.2f};{r * 0.4:.2f}" keyTimes="{key}" dur="{CYCLE}s" repeatCount="indefinite"/>'
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
        f'<animate attributeName="opacity" values="1;1;0;0" keyTimes="0;{ON_FRAC};{ON_FRAC};1" '
        f'dur="{CYCLE}s" calcMode="discrete" repeatCount="indefinite"/></rect>'
        + "".join(motes) + "</g></svg>\n")


for theme in THEMES:
    with open(os.path.join(OUT, f"mote-{theme}.svg"), "w") as f:
        f.write(logo(theme))
