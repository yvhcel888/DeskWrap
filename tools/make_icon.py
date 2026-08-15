# -*- coding: utf-8 -*-
"""Generate DeskWrap app icon (PNG + ICO)."""
from PIL import Image, ImageDraw

SIZE = 512
img = Image.new('RGBA', (SIZE, SIZE), (0, 0, 0, 0))
d = ImageDraw.Draw(img)

# rounded-square background with blue->purple gradient
grad = Image.new('RGBA', (SIZE, SIZE))
gd = ImageDraw.Draw(grad)
for y in range(SIZE):
    t = y / SIZE
    r = int(79 + (124 - 79) * t)
    g = int(140 + (92 - 140) * t)
    b = int(255 + (255 - 92) * t)
    gd.line([(0, y), (SIZE, y)], fill=(r, g, b, 255))
mask = Image.new('L', (SIZE, SIZE), 0)
md = ImageDraw.Draw(mask)
md.rounded_rectangle([0, 0, SIZE - 1, SIZE - 1], radius=110, fill=255)
img.paste(grad, (0, 0), mask)

# letter "D" - a thick arc + vertical bar
d = ImageDraw.Draw(img)
bar_w = 58
gap = 96
top = 108
bot = SIZE - top
# vertical bar
d.rectangle([gap, top, gap + bar_w, bot], fill=(255, 255, 255, 255))
# arc: draw as thick polyline approximation
import math
cx = SIZE / 2 + 30
cy = SIZE / 2
r1 = (SIZE - 2 * gap) / 2
r2 = r1 - bar_w
pts_in = []
pts_out = []
for a in range(-90, 91, 2):
    rad = math.radians(a)
    pts_in.append((cx + r2 * math.cos(rad), cy + r2 * math.sin(rad)))
    pts_out.append((cx + r1 * math.cos(rad), cy + r1 * math.sin(rad)))
arc_pts = pts_out + pts_in[::-1]
d.polygon(arc_pts, fill=(255, 255, 255, 255))

import os
import sys
# Run from the repo root; outputs next to the assets/ folder by default,
# or wherever --out points.
out = sys.argv[sys.argv.index("--out") + 1] if "--out" in sys.argv else "assets"
os.makedirs(out, exist_ok=True)
img.save(os.path.join(out, "icon.png"))
img.resize((256, 256), Image.LANCZOS).save(os.path.join(out, "icon.ico"))
print(f"图标已生成: {out}/icon.png + icon.ico")
