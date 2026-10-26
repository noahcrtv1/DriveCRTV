# Gera AppIcon.icns (chevrons em degradê CRTV sobre preto) sem depender de ferramentas do macOS.
import struct, io, sys
from PIL import Image, ImageDraw

def render(size):
    S = 1024
    im = Image.new('RGBA', (S, S), (0, 0, 0, 0))
    # quadrado arredondado no grid de ícones do macOS (824 px dentro de 1024)
    pad, r = 100, 185
    bg = Image.new('RGBA', (S, S))
    d = ImageDraw.Draw(bg)
    d.rectangle([0, 0, S, S], fill=(0, 0, 0, 255))
    mask = Image.new('L', (S, S), 0)
    ImageDraw.Draw(mask).rounded_rectangle([pad, pad, S - pad, S - pad], r, fill=255)
    im.paste(bg, (0, 0), mask)
    # chevrons desenhados como máscara e pintados com o degradê da família
    shape = Image.new('L', (S, S), 0)
    d = ImageDraw.Draw(shape)
    w = 74
    for i in range(2):
        y0 = 330 + i * 190
        pts = [(330, y0), (512, y0 + 182), (694, y0)]
        d.line(pts, fill=255, width=w, joint='curve')
        for p in (pts[0], pts[2]):
            d.ellipse([p[0] - w / 2, p[1] - w / 2, p[0] + w / 2, p[1] + w / 2], fill=255)
    stops = [(0.0, (255, 59, 64)), (0.33, (219, 46, 140)), (0.66, (115, 56, 217)), (1.0, (26, 191, 217))]
    grad = Image.new('RGBA', (S, S))
    gd = ImageDraw.Draw(grad)
    for x in range(S):
        t = x / (S - 1)
        for (a, ca), (b, cb) in zip(stops, stops[1:]):
            if a <= t <= b:
                k = (t - a) / (b - a)
                gd.line([(x, 0), (x, S)], fill=tuple(int(ca[j] + (cb[j] - ca[j]) * k) for j in range(3)) + (255,))
                break
    im.paste(grad, (0, 0), shape)
    return im.resize((size, size), Image.LANCZOS)

def png(size):
    b = io.BytesIO()
    render(size).save(b, 'PNG')
    return b.getvalue()

types = [(b'ic11', 32), (b'ic12', 64), (b'ic07', 128), (b'ic13', 256), (b'ic08', 256), (b'ic14', 512), (b'ic09', 512), (b'ic10', 1024)]
body = b''.join(t + struct.pack('>I', len(p) + 8) + p for t, p in ((t, png(s)) for t, s in types))
open(sys.argv[1], 'wb').write(b'icns' + struct.pack('>I', len(body) + 8) + body)
render(512).save(sys.argv[1].replace('.icns', '-preview.png'))
