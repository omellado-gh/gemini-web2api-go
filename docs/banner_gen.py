"""Genera el banner del README (docs/banner.svg).

Uso: python docs/banner_gen.py

Enfoque de diseño: prioridad al logotipo tipográfico, sin conceptos rebuscados. En toda
la imagen solo hay un elemento con color: la línea degradada bajo el logotipo, que usa
la misma paleta de marca que el panel de administración (#4285F4 → #9B72CB → #D96570),
y en el panel también aparece en un único lugar (la pista roja de bloqueo).

Dos restricciones vienen de cómo renderiza GitHub (el SVG en el README se renderiza como
imagen):
- Sin filtros/sombras: un resplandor difuminado solo se ve sucio a tamaños pequeños
- La fuente usa la pila monoespaciada del sistema, sin fuentes externas (el sandbox de
  <img> bloquea enlaces externos)
"""
import io
import os

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(REPO, "docs", "banner.svg")

W, H = 1280, 260
MONO = "ui-monospace,SFMono-Regular,SF Mono,Menlo,Consolas,Liberation Mono,monospace"
CHIPS = ["no account", "no API key", "single Go binary", "admin panel"]

cx = W / 2
p = [
    f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" '
    f'viewBox="0 0 {W} {H}" role="img" '
    f'aria-label="gemini-web2api-go — the Gemini web protocol, spoken as OpenAI">',
    '<defs><linearGradient id="sweep" x1="0" y1="0" x2="1" y2="0">'
    '<stop offset="0" stop-color="#4285F4"/>'
    '<stop offset="0.55" stop-color="#9B72CB"/>'
    '<stop offset="1" stop-color="#D96570"/></linearGradient></defs>',
    f'<rect width="{W}" height="{H}" fill="#0D1117"/>',
    f'<text x="{cx}" y="112" font-family="{MONO}" font-size="46" font-weight="600" '
    f'letter-spacing="-1.2" fill="#E9EEF6" text-anchor="middle">gemini-web2api-go</text>',
    f'<rect x="{cx - 190}" y="136" width="380" height="2.5" rx="1.25" fill="url(#sweep)"/>',
    f'<text x="{cx}" y="174" font-family="{MONO}" font-size="15" fill="#8695AE" '
    f'text-anchor="middle">gemini.google.com &#8594; OpenAI-compatible /v1</text>',
]

# Una fila de pastillas, centrada a mano (el SVG no tiene flex)
FW, PAD, GAP = 7.1, 15, 11        # FW = ancho aproximado de un carácter monoespaciado de 12px
widths = [len(c) * FW + PAD * 2 for c in CHIPS]
x = cx - (sum(widths) + GAP * (len(CHIPS) - 1)) / 2
for c, w in zip(CHIPS, widths):
    p.append(f'<rect x="{x:.1f}" y="200" width="{w:.1f}" height="27" rx="13.5" '
             f'fill="#161C26" stroke="#242C3A"/>')
    p.append(f'<text x="{x + w / 2:.1f}" y="218" font-family="{MONO}" font-size="12" '
             f'fill="#7D8CA6" text-anchor="middle">{c}</text>')
    x += w + GAP

p.append("</svg>")
io.open(OUT, "w", encoding="utf-8").write("\n".join(p))
print(f"Escrito {OUT}  ({os.path.getsize(OUT)} bytes)")
