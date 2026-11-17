# Legenda automática + LUT (100% no navegador)

Editor mínimo para vídeo vertical (9:16): gera legendas automaticamente, permite editá-las e aplica uma LUT de cor. Nada é enviado para servidor.

## Como rodar

Qualquer servidor estático serve (precisa de `http://`, não `file://`):

```bash
cd legendas-lut
python3 -m http.server 8000   # abra http://localhost:8000
```

Também funciona publicado em GitHub Pages, Netlify, Cloudflare Pages etc.

## Como funciona

| Etapa | Tecnologia |
|---|---|
| Transcrição | Whisper (`transformers.js`) em Web Worker, com timestamps por palavra. O modelo é baixado do Hugging Face na primeira vez e fica em cache. |
| LUT | Arquivos `.cube` 3D + presets embutidos, aplicados em shader WebGL2 (textura 3D). |
| Legenda | Desenhada em canvas (fonte, contorno, destaque palavra por palavra). |
| Exportação | Canvas 1080×1920 + `MediaRecorder` (MP4 no Chrome, WebM no Firefox). Roda em tempo real. |

## Limitações

- A exportação leva o tempo do vídeo (grava em tempo real) e exige a aba visível.
- Só aceita LUT 3D `.cube` (1D não é suportada).
- A transcrição usa o áudio inteiro em memória; vídeos muito longos (>15 min) podem ficar pesados.
