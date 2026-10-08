# Bundled studio art

- `2dpig-office.png` is an unmodified copy of `assets/2dpig/PixelOfficeAssets.png`.
- Creator: 2dPig. Source: https://2dpig.itch.io/pixel-office
- License: CC0-1.0, as recorded in `assets/manifest.json` and `licenses/CC0_AND_SOURCES.md`.
- Source canvas: 256 × 160 px. Sprite rectangles are defined in `src/office-ui.tsx` and rendered at integer scales with `image-rendering: pixelated`.
- The static room backdrop and 16 px UI icons in `src/office-ui.tsx` are original DiOffice compositions, not MapleStory assets.
- The employee sprite is temporary starter art. Names, roles and statuses come from persisted API records; the sprite does not imply live agent execution.

No proprietary MapleStory characters, maps, sprites, or UI are redistributed.

## Original woodland studio art

`studio-world.png` (640 × 360 logical pixels, RGB), `studio-engineer-idle.png` (40 × 56, transparent RGBA, front idle), and `studio-owner-idle.png` (40 × 56, transparent RGBA, three-quarter-left idle with glasses) are original DiOffice compositions authored with Hermes using the deterministic Pillow drawing script `tools/art/generate-studio-art.py` (seed `61427`). All silhouettes, pixel clusters, textures, architecture, flora, faces and clothing were drawn on the logical pixel grid from code; no external images, traced references, proprietary sprites, fonts or image-model output were used. These original assets are separate from the CC0 2dPig starter atlas above; no third-party license is asserted for them. The woodland world has a cyan sky, layered green hills/forest, large textured trees, tiled timber cottage, warm windows, fixed scenic lamps, grass and stone/clay foreground, with no baked labels, counts, application status or branding. The clear center porch uses foot plane `y=277` (about 77% of height), with the HQ centered near `x=352`, workstation overlay near `x=320`, and employee near `x=365`; both sprites use foot anchor `(20, 55)`. They are single, static idle frames and do not imply agent execution. Display at integer nearest-neighbor scale (`image-rendering: pixelated`). Regenerate from the repository root with `uv run --no-project --with "pillow>=12,<13" python tools/art/generate-studio-art.py`; each PNG also embeds source/rendering provenance, and the script verifies decoded pixels, dimensions and binary sprite alpha before printing SHA-256 checksums.

The adjacent Garden and Workshop scenes and Owner animation sheets are also original, deterministic pixel-grid drawings from `tools/art/generate-studio-world-expansion.py` (seed `738214`). The two 640 × 360 RGB scenes use the same `y=277` walk plane. `studio-owner-walk.png` is a 160 × 56 RGBA strip with four distinct horizontal 40 × 56 frames; `studio-owner-states.png` is a 160 × 168 RGBA sheet with four-frame idle, rising/falling, and landing/recovery rows. All sprite frames use binary alpha; grounded rows share a foot baseline. No MapleStory/Nexon assets, reference tracing, external fonts or model-generated imagery are used. Regenerate and verify from the repository root with `uv run --no-project --with "pillow>=12,<13" python tools/art/generate-studio-world-expansion.py`.

| Asset | SHA-256 |
|---|---|
| `studio-world-garden.png` | `2f7438c657e0bb02de7039e2c48659e5ec19320118c9c675ffc27169f6e1c1dd` |
| `studio-world-workshop.png` | `3690fafbd22092835a7eff90a513f9981df0c675a015b7058807c306edd01625` |
| `studio-owner-walk.png` | `74a917ca6dac88720e11f0c76cb105abd2197b15d1d620784723c47c6bbc7457` |
| `studio-owner-states.png` | `97f32b7c506972af181ba8975ffcc5a27f9ff10bb4b88c4faa40066b8d1669df` |
