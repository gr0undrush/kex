"""Generates build/appicon.png (1024) and build/windows/icon.ico for KEX.
Concept: the letter K built from a terminal chevron, with a blinking-cursor underscore.
Requires Pillow. Run from the project root: python build/make_icon.py"""
from PIL import Image, ImageDraw, ImageFilter, ImageOps, ImageChops

OUT, K = 1024, 2
S = OUT * K
px = lambda v: int(round(v * K))

def hexc(h, a=255):
    h = h.lstrip('#')
    return (int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16), a)

# background: deep gradient rounded square with a soft glow
grad = Image.linear_gradient('L').resize((S, S))
bg = ImageOps.colorize(grad, black=hexc('#14172e')[:3], white=hexc('#06070f')[:3]).convert('RGBA')
glow = Image.new('RGBA', (S, S), (0, 0, 0, 0))
ImageDraw.Draw(glow).ellipse([px(120), px(170), px(780), px(830)], fill=hexc('#7c4dff', 110))
bg = Image.alpha_composite(bg, glow.filter(ImageFilter.GaussianBlur(px(130))))
corner = Image.new('RGBA', (S, S), (0, 0, 0, 0))
ImageDraw.Draw(corner).ellipse([px(520), px(60), px(1000), px(540)], fill=hexc('#18e0c8', 45))
bg = Image.alpha_composite(bg, corner.filter(ImageFilter.GaussianBlur(px(120))))

mask = Image.new('L', (S, S), 0)
ImageDraw.Draw(mask).rounded_rectangle([0, 0, S - 1, S - 1], radius=px(235), fill=255)

def thick_line(d, pts, w, fill):
    d.line([(px(x), px(y)) for x, y in pts], fill=fill, width=px(w), joint='curve')
    r = w / 2
    for x, y in (pts[0], pts[-1]):
        d.ellipse([px(x - r), px(y - r), px(x + r), px(y + r)], fill=fill)

# the K: stem + two arms meeting at the stem
kmask = Image.new('L', (S, S), 0)
kd = ImageDraw.Draw(kmask)
W = 104
thick_line(kd, [(300, 232), (300, 792)], W, 255)
thick_line(kd, [(340, 520), (640, 236)], W, 255)
thick_line(kd, [(340, 520), (650, 790)], W, 255)

# gradient fill (cyan -> violet, top to bottom) with a glow underneath
kgrad = ImageOps.colorize(Image.linear_gradient('L').resize((S, S)),
                          black=hexc('#2af5d6')[:3], white=hexc('#9b6bff')[:3]).convert('RGBA')
kglow = Image.new('RGBA', (S, S), hexc('#7c4dff'))
kglow.putalpha(kmask.filter(ImageFilter.GaussianBlur(px(36))).point(lambda v: int(v * 0.8)))
canvas = Image.alpha_composite(bg, kglow)

# soft drop shadow
shadow = Image.new('RGBA', (S, S), (0, 0, 0, 0))
shadow.putalpha(kmask.filter(ImageFilter.GaussianBlur(px(18))).point(lambda v: int(v * 0.6)))
canvas = Image.alpha_composite(canvas, ImageChops.offset(shadow, 0, px(16)))

kfill = kgrad.copy()
kfill.putalpha(kmask)
canvas = Image.alpha_composite(canvas, kfill)

# subtle highlight along the stem
hl = Image.new('L', (S, S), 0)
ImageDraw.Draw(hl).line([(px(278), px(250)), (px(278), px(770))], fill=90, width=px(14))
hl = ImageChops.multiply(hl.filter(ImageFilter.GaussianBlur(px(4))), kmask)
white = Image.new('RGBA', (S, S), (255, 255, 255, 255))
white.putalpha(hl)
canvas = Image.alpha_composite(canvas, white)

# cursor underscore (amber), like a terminal prompt
cur = Image.new('RGBA', (S, S), (0, 0, 0, 0))
cd = ImageDraw.Draw(cur)
cd.rounded_rectangle([px(748), px(745), px(898), px(805)], radius=px(14), fill=hexc('#ffb62e'))
cglow = Image.new('RGBA', (S, S), hexc('#ffb62e'))
cglow.putalpha(cur.getchannel('A').filter(ImageFilter.GaussianBlur(px(26))).point(lambda v: int(v * 0.7)))
canvas = Image.alpha_composite(canvas, cglow)
canvas = Image.alpha_composite(canvas, cur)

# thin inner border
border = Image.new('RGBA', (S, S), (0, 0, 0, 0))
ImageDraw.Draw(border).rounded_rectangle([px(10), px(10), px(1014), px(1014)], radius=px(226),
                                         outline=hexc('#ffffff', 28), width=px(5))
canvas = Image.alpha_composite(canvas, border)

canvas.putalpha(ImageChops.multiply(canvas.getchannel('A'), mask))
icon = canvas.resize((OUT, OUT), Image.LANCZOS)
icon.save('build/appicon.png')
icon.save('build/windows/icon.ico', sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)])
print('ok')
