#!/usr/bin/env python3
"""bx Windows 图标生成器(真相源)。heater 盾牌 + 白色 "b"。
用法: python3 winres/gen-icons.py   # 从仓库根跑
产物: winres/icon.png(256) winres/icon16.png(32)
      internal/tray/icons/{protected,warning,failed,off}.ico(各含 16/20/24/32)
改完重生成 .syso: go generate ./...
"""
import glob, math, os
from PIL import Image, ImageDraw, ImageFont

SS = 8  # 超采样
WHITE = (255, 255, 255, 255)
COLORS = {
    "green": (34, 197, 94, 255),
    "amber": (245, 180, 20, 255),
    "red":   (239, 68, 68, 255),
    "grey":  (148, 156, 164, 255),
}
# 字体决定那个 "b" 的形状,也就是决定这个标长什么样。
# **找不到就必须响亮失败** —— 原来的 load_default() 回落会画出一个位图字体的
# 小 "b",在 1024 上糊成一团,而脚本照样退出 0、图标照样被打进安装包。
# 一个静默产出坏结果的回落,比报错糟得多。
FONT_CANDIDATES = [
    "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
    "/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf",
]
# macOS 上没有 DejaVu,但装了 matplotlib 的话它自带一份 —— 用同一个字体
# 才能保证两个平台上的标是同一个标。
FONT_GLOBS = [
    os.path.expanduser("~/.local/share/uv/tools/*/lib/python*/site-packages/matplotlib/mpl-data/fonts/ttf/DejaVuSans-Bold.ttf"),
    "/usr/local/lib/python*/site-packages/matplotlib/mpl-data/fonts/ttf/DejaVuSans-Bold.ttf",
    os.path.expanduser("~/**/site-packages/matplotlib/mpl-data/fonts/ttf/DejaVuSans-Bold.ttf"),
]

def _font(px):
    for p in FONT_CANDIDATES:
        if os.path.exists(p):
            return ImageFont.truetype(p, px)
    for pattern in FONT_GLOBS:
        found = sorted(glob.glob(pattern, recursive=True))
        if found:
            return ImageFont.truetype(found[0], px)
    raise SystemExit(
        "找不到 DejaVuSans-Bold.ttf —— 拒绝用回落字体画图标。\n"
        "字体决定这个标长什么样;换一个字体等于悄悄换掉品牌标识。\n"
        "装一个:pip install matplotlib(它自带这份字体),或把路径加进 FONT_CANDIDATES。")

def _heater_shield(dr, box, fill):
    x0, y0, x1, y1 = box
    w, h = x1 - x0, y1 - y0
    top = y0 + h * 0.06
    r = w * 0.10
    tlx, trx = x0 + w * 0.10, x1 - w * 0.10
    def lerp(a, b, t): return a + (b - a) * t
    steps, curve = 40, []
    for i in range(steps + 1):
        t = i / steps
        if t < 0.45:
            px, py = x1, lerp(top + r, y0 + h * 0.55, t / 0.45)
        else:
            tt = (t - 0.45) / 0.55
            px = lerp(x1, x0 + w * 0.5, tt)
            py = lerp(y0 + h * 0.55, y1, math.sin(tt * math.pi / 2))
        curve.append((px, py))
    left = [(x1 - (px - x0), py) for px, py in reversed(curve)]
    dr.polygon([(trx, top)] + curve + left + [(tlx, top)], fill=fill)
    dr.pieslice([tlx - r, top, tlx + r, top + 2 * r], 180, 270, fill=fill)
    dr.pieslice([trx - r, top, trx + r, top + 2 * r], 270, 360, fill=fill)
    dr.rectangle([tlx, top, trx, top + r], fill=fill)

def render(color, size):
    big = size * SS
    im = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    pad = big * 0.08
    _heater_shield(d, (pad, pad, big - pad, big - pad), COLORS[color])
    f = _font(int(big * 0.46))
    tb = d.textbbox((0, 0), "b", font=f)
    tw, th = tb[2] - tb[0], tb[3] - tb[1]
    d.text((big / 2 - tw / 2 - tb[0], big * 0.45 - th / 2 - tb[1]), "b", font=f, fill=WHITE)
    return im.resize((size, size), Image.LANCZOS)

def main():
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    render("green", 256).save(os.path.join(root, "winres", "icon.png"))
    # 1024 供 macOS 的 .icns(Retina 下 Dock 会用到 512@2x)。
    render("green", 1024).save(os.path.join(root, "winres", "icon1024.png"))
    render("green", 32).save(os.path.join(root, "winres", "icon16.png"))
    ico_dir = os.path.join(root, "internal", "tray", "icons")
    sizes = [16, 20, 24, 32]
    for name, color in [("protected", "green"), ("warning", "amber"),
                        ("failed", "red"), ("off", "grey")]:
        base = render(color, 32)
        base.save(os.path.join(ico_dir, name + ".ico"),
                  sizes=[(s, s) for s in sizes])
    print("icons generated")

if __name__ == "__main__":
    main()
