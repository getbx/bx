#!/usr/bin/env python3
"""bx Windows 托盘图标生成器(真相源)。

用法: python3 winres/gen-icons.py   # 从仓库根跑
产物: internal/tray/icons/{protected,warning,failed,off}.ico(各含 16/20/24/32)

**托盘图标表示保护状态,不是产品标**(所有者 2026-09-28 定,与 macOS 同一条规则):
四态用与 macOS 菜单栏同一个盾形轮廓、同一套形态编码 —— 状态编码在轮廓,不在颜色:
    protected  实心
    off        空心
    warning    虚线(macOS 上是过渡态的形态;Windows 没有过渡态,warning 借它)
    failed     沿中线裂开
轮廓与裂缝的坐标逐字来自 apps/macos/BxMenu/Sources/BxMenu/MenuIcon.swift
(shieldOutlinePoints / shieldCrackPoints,16×16 坐标系,y 向下)。单色白、透明底:
Windows 托盘默认深色,系统不像 macOS 那样替 template 图上色。

**exe 的图标不在这里出**:那是产品标(b+x),来自设计包,vendored 成 winres/bx-{16,32,48,256}.png,
winres.json 直接引用。
"""
import os
from PIL import Image, ImageDraw

SS = 8  # 超采样
WHITE = (255, 255, 255, 255)
CLEAR = (0, 0, 0, 0)

# 与 MenuIcon.swift 逐字相同(TestWindowsTrayShieldMatchesTheMacOSOutline 钉住)。
SHIELD_OUTLINE = [(8, 1.5), (14, 3.35), (14, 8), (11.6, 13.4), (8, 15.15), (4.4, 13.4), (2, 8), (2, 3.35)]
SHIELD_CRACK = [(8, 1.5), (6.85, 5.1), (8.95, 7.3), (7.05, 10.4), (8.45, 12.5), (8, 15.15)]


def _scaled(points, size):
    k = size * SS / 16.0
    return [(x * k, y * k) for x, y in points]


def _stroke_polygon(dr, pts, width, fill):
    n = len(pts)
    for i in range(n):
        dr.line([pts[i], pts[(i + 1) % n]], fill=fill, width=width, joint="curve")
    r = width / 2
    for x, y in pts:
        dr.ellipse([x - r, y - r, x + r, y + r], fill=fill)


def _dashed_polygon(dr, pts, width, fill, dash, gap):
    # 沿边逐段走,画 dash、跳 gap;跨顶点时把剩余长度带过去。
    import math
    n = len(pts)
    on, left = True, dash
    for i in range(n):
        (x0, y0), (x1, y1) = pts[i], pts[(i + 1) % n]
        seg = math.hypot(x1 - x0, y1 - y0)
        t = 0.0
        while t < seg:
            step = min(left, seg - t)
            if on:
                ax, ay = x0 + (x1 - x0) * t / seg, y0 + (y1 - y0) * t / seg
                bx, by = x0 + (x1 - x0) * (t + step) / seg, y0 + (y1 - y0) * (t + step) / seg
                dr.line([(ax, ay), (bx, by)], fill=fill, width=width)
            t += step
            left -= step
            if left <= 0:
                on, left = (not on), (gap if on else dash)


def render(form, size):
    big = size * SS
    im = Image.new("RGBA", (big, big), CLEAR)
    dr = ImageDraw.Draw(im)
    pts = _scaled(SHIELD_OUTLINE, size)
    width = max(1, int(round(1.6 * size * SS / 16)))
    if form == "filled":
        dr.polygon(pts, fill=WHITE)
    elif form == "hollow":
        _stroke_polygon(dr, pts, width, WHITE)
    elif form == "dashed":
        unit = size * SS / 16.0
        _dashed_polygon(dr, pts, width, WHITE, dash=2.2 * unit, gap=1.4 * unit)
    elif form == "cracked":
        dr.polygon(pts, fill=WHITE)
        # 裂缝:用透明把实心盾切开,宽度与描边同级,16pt 下仍看得出是一道口。
        crack = _scaled(SHIELD_CRACK, size)
        mask = Image.new("L", (big, big), 0)
        mdr = ImageDraw.Draw(mask)
        mdr.line(crack, fill=255, width=max(1, int(round(1.3 * size * SS / 16))), joint="curve")
        im.paste(CLEAR, (0, 0), mask)
    else:
        raise SystemExit(f"unknown form {form}")
    return im.resize((size, size), Image.LANCZOS)


def main():
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    ico_dir = os.path.join(root, "internal", "tray", "icons")
    sizes = [16, 20, 24, 32]
    for name, form in [("protected", "filled"), ("warning", "dashed"),
                       ("failed", "cracked"), ("off", "hollow")]:
        base = render(form, 32)
        base.save(os.path.join(ico_dir, name + ".ico"),
                  sizes=[(s, s) for s in sizes],
                  append_images=[render(form, s) for s in sizes if s != 32])
        print("wrote", os.path.join(ico_dir, name + ".ico"))


if __name__ == "__main__":
    main()
