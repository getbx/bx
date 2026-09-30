#!/usr/bin/env python3
"""Build the iPhone app icon and the in-app brand mark from the bx design pack.

    python3 apps/ios/icon/gen-ios-icons.py ~/Downloads/bx_B_integrated_production_v3

The pack's app-icon masters are macOS tiles (a rounded tile drawn inside the square). iOS masks
the corners itself and rejects transparency, so the phone icon is composed here instead: the
pack's transparent mark on a full-bleed background sampled from the same tile — light, dark,
and a grayscale "tinted" variant for iOS 18 home screens. The in-app mark is copied as-is
(on_light for light mode, on_dark for dark mode — the pack's README forbids mixing them).
"""
import os
import sys

from PIL import Image

pack = sys.argv[1]
here = os.path.dirname(os.path.abspath(__file__))
assets = os.path.join(here, "..", "App", "Assets.xcassets")
SIZE = 1024
MARK_WIDTH = 0.60  # share of the canvas the mark's bounding box spans (Apple's glyph grid ~0.6)


def mark(name):
    return Image.open(os.path.join(pack, "ui_mark", name)).convert("RGBA")


def gradient(top, bottom):
    img = Image.new("RGB", (SIZE, SIZE))
    for y in range(SIZE):
        t = y / (SIZE - 1)
        img.paste(tuple(round(a + (b - a) * t) for a, b in zip(top, bottom)), (0, y, SIZE, y + 1))
    return img


def compose(background, glyph):
    box = glyph.getchannel("A").getbbox()
    cropped = glyph.crop(box)
    w = round(SIZE * MARK_WIDTH)
    h = round(cropped.height * w / cropped.width)
    cropped = cropped.resize((w, h), Image.LANCZOS)
    out = background.copy()
    out.paste(cropped, ((SIZE - w) // 2, (SIZE - h) // 2), cropped)
    return out.convert("RGB")  # no alpha channel: the App Store rejects icons that have one


icon_dir = os.path.join(assets, "AppIcon.appiconset")
os.makedirs(icon_dir, exist_ok=True)
# Colors sampled from the pack's own light and dark app tiles.
compose(gradient((251, 251, 252), (236, 240, 244)), mark("on_light/bx-mark-on-light-master-1024.png")).save(os.path.join(icon_dir, "AppIcon-light.png"))
compose(gradient((30, 34, 40), (22, 26, 31)), mark("on_dark/bx-mark-on-dark-master-1024.png")).save(os.path.join(icon_dir, "AppIcon-dark.png"))
compose(Image.new("RGB", (SIZE, SIZE), (0, 0, 0)), mark("mono_white/bx-mark-white-master-1024.png")).convert("L").save(os.path.join(icon_dir, "AppIcon-tinted.png"))

mark_dir = os.path.join(assets, "BrandMark.imageset")
os.makedirs(mark_dir, exist_ok=True)
for src, dst in (("on_light/bx-mark-on-light-512.png", "BrandMark-light.png"), ("on_dark/bx-mark-on-dark-512.png", "BrandMark-dark.png")):
    img = mark(src)
    img.crop(img.getchannel("A").getbbox()).save(os.path.join(mark_dir, dst))
print("wrote", icon_dir, "and", mark_dir)
