# DiOffice v0.1 — Starter Asset Manifest

Starter pack ini sengaja kecil dan fokus. Semua file binary yang dibundel di sini diverifikasi sebagai PNG valid dan berasal dari asset yang sumber kanoniknya menyatakan **CC0 / public-domain dedication**.

## 1) 2dPig — Pixel Office Asset Pack

**Bundled file**
- `2dpig/PixelOfficeAssets.png` — 256×160 RGBA PNG

**Isi yang berguna untuk v0.1**
- 5 karakter manusia
- desks / furniture
- computers / monitors
- plants / decor
- server / office equipment

**Canonical source**
- https://2dpig.itch.io/pixel-office

**License**
- CC0 1.0 / Creative Commons Public Domain Dedication
- Attribution tidak diwajibkan oleh sumber, tetapi credit tetap disimpan sebagai courtesy.

**Retrieval provenance**
- Public GitHub mirror: `hissinger/small-village`
- Path: `public/assets/tilesets/source/PixelOfficeAssets.png`
- Git blob SHA: `88f1d4382d7c7851f94500afb05d49bea41a5bfc`

## 2) MSavioti — Office Stuff

**Bundled files**
- `office_stuff/meeting_office.png` — 48×48 RGBA PNG
- `office_stuff/woman_office.png` — 48×48 RGBA PNG

**Canonical source**
- https://opengameart.org/content/office-stuff

**License**
- CC0

**Retrieval provenance**
- Public GitHub mirror: `LangSage/LangSage.github.io`
- Paths:
  - `english_games/lost-shopping-list/assets/images/environment/meeting_office.png`
  - `english_games/lost-shopping-list/assets/images/environment/woman_office.png`
- Git blob SHA:
  - meeting: `333ed849b4f62957e6e02a5310c5b2ef84bebdef`
  - woman: `4500af3bdbf9a63f410e53011d35f910ee4f18c7`

## Scale recommendation

- Base office source grid: 16 px.
- Render with integer nearest-neighbor scaling: 2×, 3×, or 4×.
- Treat 48×48 Office Stuff images as supplementary decoration/reference until normalized to the DiOffice sprite spec.

## Assets intentionally NOT rebundled

Beberapa free packs yang ditemukan sebelumnya tidak dimasukkan ke ZIP karena halaman lisensinya tidak memberi izin redistribusi yang cukup jelas, atau memiliki aturan tambahan yang berpotensi bertentangan dengan re-bundling. Mereka tetap dapat dipertimbangkan nanti setelah license review manual.

Untuk produk final, asset CC0 ini disarankan sebagai prototype scaffolding. Karakter/furniture utama DiOffice sebaiknya diganti dengan original art setelah core loop tervalidasi.
