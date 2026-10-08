"""Generate original, deterministic DiOffice pixel-art expansion assets.

Run from any directory with:
    uv run --no-project --with "pillow>=12,<13" python tools/art/generate-studio-world-expansion.py

All artwork is drawn directly on the logical pixel grid with Pillow. No external
images, fonts, sprites, tracing, antialiasing, filters, or resampling are used.
The two 640x360 RGB worlds share the y=277 walk plane and palette language of
the existing studio scene. Owner walk is four 40x56 RGBA frames; the state sheet
has 4-frame idle, airborne, and landing rows on the same logical frame grid.
"""

from __future__ import annotations

import hashlib
import json
import math
import random
from pathlib import Path
from typing import Iterable

from PIL import Image, ImageDraw, PngImagePlugin

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "apps" / "web" / "public" / "art"
W, H = 640, 360
GROUND_Y = 277
SEED = 738214
Point = tuple[int, int]

# Original bounded palette: the cyan/leaf/earth ramps echo the woodland studio,
# while greenhouse glass and forge light distinguish the adjacent zones.
P = {
    "sky": "#bae6e5", "sky_hi": "#c9eeeb", "sky_low": "#d8f0df",
    "cloud": "#edf5df", "cloud_edge": "#d9eee5", "cloud_shade": "#a2d3d5",
    "far": "#99cbbb", "far_light": "#b0d8c0", "hill": "#83baa2",
    "hill_light": "#9dcbad", "forest_far": "#69a790", "forest_mid": "#5b987c",
    "forest_light": "#76ad88", "forest_dark": "#498269",
    "tree_outline": "#294f48", "leaf_dark": "#315e4e", "leaf_shadow": "#427857",
    "leaf": "#568b55", "leaf_mid": "#6ba45d", "leaf_light": "#8dbc6c",
    "leaf_glint": "#b2d582", "leaf_sun": "#d2e9a2", "leaf_olive": "#789655",
    "bark_dark": "#5c4940", "bark": "#82604a", "bark_mid": "#a27953",
    "bark_light": "#bf9663", "bark_moss": "#718754",
    "wood_dark": "#655249", "wood_edge": "#785a47", "wood": "#b68a61",
    "wood_light": "#d2ab76", "wood_sun": "#e8c58e", "plaster": "#e7d5a2",
    "plaster_dark": "#c7b98b", "plaster_light": "#f4e6b9",
    "roof_dark": "#654e49", "roof_shadow": "#925e4a", "roof": "#b47a55",
    "roof_light": "#ce9969", "roof_glint": "#e4b57b", "roof_moss": "#849466",
    "glass_dark": "#535d56", "glass": "#c99a61", "glass_mid": "#e9bf76",
    "glass_light": "#ffe5a0", "glass_glint": "#fff1c5",
    "glass_blue": "#92c8c5", "glass_blue_hi": "#c8e8d9",
    "metal": "#38545a", "metal_light": "#6e8983", "metal_dark": "#2b4149",
    "stone_dark": "#5f6b64", "stone": "#98a297", "stone_light": "#c1c4aa",
    "stone_edge": "#778379", "grass_dark": "#476944", "grass": "#64934e",
    "grass_mid": "#85b357", "grass_light": "#accc6a", "grass_sun": "#cee58b",
    "soil_dark": "#645243", "soil": "#86664a", "soil_mid": "#a58458",
    "soil_light": "#c7a874", "soil_sand": "#d8c48a",
    "flower_pink": "#dd9b9b", "flower_light": "#f4c1aa",
    "flower_blue": "#a8cfcb", "flower_blue_dark": "#6ba3b2",
    "flower_yellow": "#f5da8f", "terracotta": "#b87957",
    "terracotta_dark": "#875b47", "forge_dark": "#754a3e",
    "forge": "#c66f47", "forge_light": "#f1a45d", "forge_hot": "#ffe08a",
    "plank": "#9a704f", "plank_light": "#c29362", "plank_dark": "#604a3c",
}


def rgb(value: str) -> tuple[int, int, int]:
    return tuple(int(value[i:i + 2], 16) for i in (1, 3, 5))


def polygon(draw: ImageDraw.ImageDraw, points: Iterable[Point], color: str) -> None:
    draw.polygon(list(points), fill=color)


def organic_points(
    cx: int, cy: int, rx: int, ry: int, rng: random.Random, lobes: int = 9,
) -> list[Point]:
    """Return an irregular stepped contour for foliage/cloud silhouettes."""
    phase = rng.random() * math.tau
    result: list[Point] = []
    for i in range(90):
        angle = math.tau * i / 90
        radius = 1 + .075 * math.sin(lobes * angle + phase)
        radius += .035 * math.sin(17 * angle + phase * 2)
        radius += rng.uniform(-.025, .025)
        result.append((round(cx + math.cos(angle) * rx * radius),
                       round(cy + math.sin(angle) * ry * radius)))
    return result


def leaf_cluster(draw: ImageDraw.ImageDraw, x: int, y: int, color: str, size: int = 1) -> None:
    draw.rectangle((x, y, x + size + 1, y), fill=color)
    draw.rectangle((x - 1, y + 1, x + size, y + 1), fill=color)
    if size > 1:
        draw.rectangle((x + 1, y + 2, x + size, y + 2), fill=color)


def crown(
    im: Image.Image, cx: int, cy: int, rx: int, ry: int, seed: int,
    shaded: bool = False,
) -> None:
    rng = random.Random(seed)
    d = ImageDraw.Draw(im)
    pts = organic_points(cx, cy, rx, ry, rng, rng.randint(7, 11))
    polygon(d, [(x, y + 2) for x, y in pts], P["tree_outline"])
    polygon(d, pts, P["leaf_dark"] if shaded else P["leaf_shadow"])
    inner = organic_points(cx - rx // 12, cy - ry // 7, max(4, rx - 3), max(4, ry - 4), rng)
    polygon(d, inner, P["leaf_shadow"] if shaded else P["leaf"])
    lit = organic_points(cx - rx // 5, cy - ry // 4, max(3, int(rx * .75)), max(3, int(ry * .66)), rng)
    polygon(d, lit, P["leaf"] if shaded else P["leaf_mid"])
    for _ in range(max(3, rx // 11)):
        lx = rng.randint(cx - rx // 2, cx + rx // 3)
        ly = rng.randint(cy - ry // 2, cy + ry // 4)
        patch = organic_points(lx, ly, rng.randint(4, 10), rng.randint(3, 7), rng, 5)
        polygon(d, patch, P["leaf_mid"] if shaded else P["leaf_light"])
    allowed = {rgb(P[k]) for k in ("leaf_dark", "leaf_shadow", "leaf", "leaf_mid", "leaf_light")}
    for _ in range(max(18, rx * ry // 7)):
        x = rng.randint(max(0, cx - rx + 2), min(W - 4, cx + rx - 2))
        y = rng.randint(max(0, cy - ry + 2), min(H - 4, cy + ry - 2))
        if im.getpixel((x, y)) not in allowed:
            continue
        light = y < cy + rng.randint(-6, 4)
        color = rng.choice([P["leaf_light"], P["leaf_glint"], P["leaf_mid"]]) if light else rng.choice([P["leaf_shadow"], P["leaf_dark"], P["leaf"]])
        if shaded and color == P["leaf_glint"]:
            color = P["leaf_light"]
        leaf_cluster(d, x, y, color, rng.choice([1, 1, 2, 2, 3]))
    for x, y in pts[45:82:6]:
        if 0 <= x < W - 2 and 0 <= y < H - 2:
            d.rectangle((x, y + 2, x + 2, y + 2), fill=P["leaf_light"])


def cloud(draw: ImageDraw.ImageDraw, x: int, y: int, width: int) -> None:
    h = max(8, width // 5)
    draw.rectangle((x + 4, y + h - 2, x + width - 4, y + h + 2), fill=P["cloud_shade"])
    for fx, fy, fr in [(0.18, .68, .20), (.38, .41, .23), (.62, .49, .21), (.82, .72, .17)]:
        r = round(width * fr)
        xx, yy = round(x + width * fx), round(y + h * fy)
        pts = organic_points(xx, yy, r, max(3, round(r * .45)), random.Random(x * 31 + y * 13 + xx), 5)
        polygon(draw, pts, P["cloud_edge"])
        polygon(draw, [(px, py - 2) for px, py in pts], P["cloud"])
    draw.rectangle((x + 7, y + h - 3, x + width - 5, y + h), fill=P["cloud"])
    draw.line((x + 10, y + h + 1, x + width - 8, y + h + 1), fill=P["cloud_edge"])
    draw.line((x + width // 4, y + 1, x + width // 3, y + 1), fill=P["plaster_light"])


def backdrop(im: Image.Image, variant: str) -> None:
    d = ImageDraw.Draw(im)
    d.rectangle((0, 0, W - 1, H - 1), fill=P["sky"])
    for args in [(27, 36, 77), (190, 65, 58), (404, 28, 85), (553, 78, 64)]:
        cloud(d, *args)
    ridge = [(x, round(155 + 16 * math.sin(x / 67 + .6) + 8 * math.cos(x / 41))) for x in range(-4, W + 5, 4)]
    polygon(d, ridge + [(W, 239), (0, 239)], P["far"])
    polygon(d, [(x, y + 5) for x, y in ridge] + [(W, 207), (0, 212)], P["far_light"])
    ridge2 = [(x, round(199 + 15 * math.sin(x / 76 + 1.5) + 8 * math.sin(x / 36))) for x in range(-4, W + 5, 4)]
    polygon(d, ridge2 + [(W, 278), (0, 278)], P["hill"])
    polygon(d, [(x, y + 3) for x, y in ridge2] + [(W, 240), (0, 244)], P["hill_light"])
    rng = random.Random(SEED + (11 if variant == "garden" else 19))
    for x in range(-16, W + 18, 21):
        yy, rr = rng.randint(187, 213), rng.randint(14, 22)
        polygon(d, organic_points(x, yy, rr, rng.randint(19, 30), rng, 7), P["forest_far"])
        d.line((x, yy + 12, x + 1, 254), fill=P["forest_mid"], width=2)
    for x in range(-8, W + 12, 30):
        yy = rng.randint(218, 238)
        polygon(d, organic_points(x, yy, rng.randint(19, 27), 24, rng, 8), P["forest_mid"])
        d.line((x - 2, yy + 8, x - 2, 273), fill=P["forest_dark"], width=3)
    d.rectangle((0, 254, W, 278), fill=P["forest_dark"])
    for _ in range(200):
        x, y = rng.randrange(W), rng.randrange(243, 275)
        if im.getpixel((x, y)) in (rgb(P["forest_mid"]), rgb(P["forest_dark"])):
            d.rectangle((x, y, x + rng.randint(1, 3), y + 1), fill=P["forest_light"])


def framing_trees(im: Image.Image, seed: int) -> None:
    d = ImageDraw.Draw(im)
    for x, top, bottom, width in [(55, 93, 278, 24), (592, 105, 278, 27)]:
        left, right = x - width // 2, x + width // 2
        polygon(d, [(left - 5, bottom), (left + 2, bottom - 12), (left + 2, top + 20),
                    (x, top), (right, top + 18), (right + 3, bottom - 9),
                    (right + 10, bottom), (x + 6, bottom + 2), (x - 9, bottom + 1)], P["bark_dark"])
        polygon(d, [(left + 1, bottom - 2), (left + 5, top + 23), (x, top + 9),
                    (x + 5, bottom - 15), (x + 10, bottom - 1)], P["bark"])
        d.line((left + 5, bottom - 4, left + 4, top + 40), fill=P["bark_mid"], width=3)
    for args in [
        (18, 118, 55, 38, seed + 1), (59, 103, 54, 42, seed + 2),
        (112, 136, 43, 32, seed + 3), (29, 71, 49, 38, seed + 4),
        (82, 48, 50, 35, seed + 5), (127, 83, 37, 31, seed + 6),
        (523, 130, 47, 36, seed + 7), (581, 106, 55, 42, seed + 8),
        (630, 130, 40, 33, seed + 9), (536, 72, 46, 34, seed + 10),
        (587, 45, 50, 37, seed + 11), (628, 68, 43, 32, seed + 12),
    ]:
        crown(im, *args)


def walk_plane(im: Image.Image, variant: str) -> None:
    """Shared earth ledge: a straight, unobstructed y=277 walk plane."""
    d = ImageDraw.Draw(im)
    rng = random.Random(SEED + (31 if variant == "garden" else 47))
    polygon(d, [(0, GROUND_Y), (640, GROUND_Y), (640, H), (0, H)], P["soil"])
    d.rectangle((0, 302, W, H), fill=P["soil_dark"])
    d.rectangle((0, 304, W, 311), fill=P["soil_mid"])
    for _ in range(770):
        x, y = rng.randrange(W), rng.randint(284, 358)
        color = rng.choice([P["soil"], P["soil_mid"], P["soil_light"]]) if y < 322 else rng.choice([P["soil"], P["soil_dark"]])
        d.rectangle((x, y, x + rng.choice([1, 2, 3, 5]), y + rng.choice([0, 1, 2])), fill=color)
    for row, yy in enumerate([291, 311, 334, 354]):
        x = -20 + (row % 2) * 12
        while x < W:
            width, height = rng.randint(12, 31), rng.randint(7, 14)
            stone = [(x + 3, yy - height // 2), (x + width - 6, yy - height // 2 - 1),
                     (x + width, yy - 1), (x + width - 4, yy + height // 2),
                     (x + 3, yy + height // 2 + 1), (x - 1, yy + 2)]
            polygon(d, [(px, py + 1) for px, py in stone], P["soil_dark"])
            color = P["stone_edge"] if yy > 330 else rng.choice([P["stone"], P["stone_edge"]])
            polygon(d, stone, color)
            d.line(stone[:3], fill=P["stone_light"] if yy < 330 else P["stone"], width=1)
            d.line((x + width // 3, yy - 2, x + width // 3 - 2, yy + 3), fill=P["stone_dark"])
            if width > 24:
                d.rectangle((x + width - 9, yy + 3, x + width - 7, yy + 3), fill=P["soil_mid"])
            x += width + rng.randint(5, 15)
    # Grass fringe is kept out of the main actor lane. The top lip remains flat.
    for x in range(0, W, 4):
        if 246 <= x <= 394:
            d.rectangle((x, 277, x + 3, 279), fill=P["soil_mid"])
            d.line((x, 277, x + 1, 277), fill=P["soil_light"])
            continue
        depth = rng.randint(5, 9)
        d.rectangle((x, 277, x + 3, 277 + depth), fill=P["grass_dark"])
        d.rectangle((x, 277, x + 3, 279), fill=P["grass_mid"])
        d.line((x, 277, x + 1, 277), fill=P["grass_light"])
        if rng.random() < .55:
            d.line((x + 1, 274 - rng.randint(0, 3), x + 1, 277), fill=P["grass_light"])
    # A familiar stone top edge ties both zones back to the studio map.
    d.line((246, 277, 394, 277), fill=P["stone_light"])
    d.line((0, 281, 245, 281), fill=P["grass_light"])
    d.line((395, 281, 639, 281), fill=P["grass_light"])


def step_path(draw: ImageDraw.ImageDraw, x0: int, x1: int, color: str, seed: int) -> None:
    rng = random.Random(seed)
    draw.rectangle((x0, 268, x1, 277), fill=P["stone_dark"])
    draw.rectangle((x0 + 2, 267, x1 - 2, 275), fill=color)
    for x in range(x0 + 5, x1 - 3, 16):
        draw.line((x, 268, x + 3, 274), fill=P["stone_edge"])
        draw.line((x + 4, 268, min(x + 12, x1 - 3), 268), fill=P["stone_light"])
    for _ in range(10):
        x, y = rng.randint(x0 + 3, x1 - 3), rng.randint(273, 276)
        draw.point((x, y), fill=P["soil_sand"])


def trellis(draw: ImageDraw.ImageDraw, x: int, y_top: int, y_bottom: int, seed: int) -> None:
    rng = random.Random(seed)
    d = draw
    d.rectangle((x, y_top + 4, x + 5, y_bottom), fill=P["wood_dark"])
    d.rectangle((x + 1, y_top + 5, x + 3, y_bottom - 2), fill=P["wood_light"])
    d.rectangle((x + 43, y_top + 4, x + 48, y_bottom), fill=P["wood_dark"])
    d.rectangle((x + 44, y_top + 5, x + 46, y_bottom - 2), fill=P["wood_light"])
    polygon(d, [(x - 2, y_top + 9), (x + 6, y_top + 1), (x + 12, y_top - 5),
                (x + 36, y_top - 5), (x + 43, y_top + 1), (x + 50, y_top + 9),
                (x + 47, y_top + 12), (x + 39, y_top + 4), (x + 9, y_top + 4),
                (x + 1, y_top + 12)], P["wood_dark"])
    d.line((x + 6, y_top + 5, x + 14, y_top - 2, x + 34, y_top - 2, x + 42, y_top + 5), fill=P["wood_sun"], width=2)
    for offset in [12, 23, 34]:
        d.line((x + offset, y_top + 3, x + offset, y_bottom - 3), fill=P["wood_edge"])
    for yy in range(y_top + 18, y_bottom - 8, 14):
        d.line((x + 7, yy, x + 41, yy), fill=P["wood_edge"])
        d.line((x + 8, yy + 1, x + 40, yy + 1), fill=P["wood_light"])
    # Vine follows the arbor and drops in two uneven, leaf-lit strands.
    d.line([(x + 10, y_top + 6), (x + 19, y_top - 3), (x + 32, y_top + 2),
            (x + 39, y_top + 7), (x + 39, y_top + 21), (x + 35, y_top + 31)], fill=P["leaf_dark"], width=2)
    for xx, yy in [(x + 12, y_top + 3), (x + 19, y_top - 2), (x + 27, y_top - 1),
                   (x + 37, y_top + 7), (x + 39, y_top + 18), (x + 35, y_top + 29)]:
        leaf_cluster(d, xx, yy, rng.choice([P["leaf_mid"], P["leaf_light"], P["leaf_sun"]]), 2)


def flower(draw: ImageDraw.ImageDraw, x: int, y: int, color: str, size: int = 1) -> None:
    draw.line((x, y + 2, x - 1, y + 8), fill=P["leaf_dark"])
    draw.line((x - 1, y + 6, x - 4, y + 5), fill=P["leaf_mid"])
    draw.line((x, y + 7, x + 3, y + 6), fill=P["leaf_light"])
    draw.rectangle((x - size, y - 1, x + size, y + 1), fill=color)
    draw.line((x, y - 2, x, y + 2), fill=color)
    draw.point((x, y), fill=P["glass_glint"])


def fern(draw: ImageDraw.ImageDraw, x: int, base: int, height: int, seed: int) -> None:
    rng = random.Random(seed)
    for off in [-7, -3, 1, 6]:
        end, top = x + off * 2, base - height + rng.randint(0, 4)
        draw.line([(x, base), (x + off, base - height // 2), (end, top)], fill=P["leaf_dark"])
        for i in range(2, 8):
            t = i / 9
            xx, yy = round(x + (end - x) * t), round(base + (top - base) * t)
            ln = max(1, round(4 * (1 - t)))
            draw.line((xx - ln, yy - 1, xx + ln, yy + 1), fill=P["leaf_mid"])
            draw.point((xx - ln, yy - 1), fill=P["leaf_light"])


def greenhouse(im: Image.Image) -> None:
    d = ImageDraw.Draw(im)
    # Low glasshouse with timber ribs and a central open doorway.
    polygon(d, [(239, 187), (303, 144), (336, 144), (403, 187), (397, 263), (244, 263)], P["wood_dark"])
    polygon(d, [(246, 188), (306, 151), (334, 151), (396, 188), (391, 259), (250, 259)], P["glass_blue"])
    polygon(d, [(252, 187), (307, 157), (330, 157), (388, 187), (383, 253), (256, 253)], P["glass_blue_hi"])
    # Glass panels have cool shade along their lower halves.
    for x0, x1 in [(254, 285), (292, 307), (334, 349), (356, 385)]:
        d.rectangle((x0, 195, x1, 252), fill=P["glass_blue"])
        d.rectangle((x0 + 2, 198, x1 - 2, 216), fill=P["glass_blue_hi"])
    d.rectangle((242, 185, 400, 193), fill=P["wood_dark"])
    d.rectangle((245, 185, 397, 188), fill=P["wood_sun"])
    for x in [253, 289, 308, 333, 352, 388]:
        d.rectangle((x, 183, x + 4, 260), fill=P["wood_edge"])
        d.line((x + 1, 187, x + 1, 258), fill=P["wood_light"])
    for y in [204, 224, 244]:
        d.line((249, y, 393, y), fill=P["wood_edge"], width=2)
    # Pitched glazed roof: dark silhouette, terracotta edge, glass panes.
    polygon(d, [(229, 190), (303, 136), (337, 136), (411, 190), (402, 196), (336, 151),
                (306, 151), (238, 197)], P["wood_dark"])
    polygon(d, [(236, 187), (305, 142), (336, 142), (403, 188), (394, 190), (335, 149),
                (307, 149), (244, 191)], P["glass_blue_hi"])
    d.line((237, 187, 305, 141, 337, 141, 402, 187), fill=P["wood_sun"], width=3)
    for x in [270, 294, 317, 340, 364, 386]:
        # Roof ribs visually converge toward the ridge.
        if x < 306:
            d.line((x, 185, 307, 144), fill=P["wood_edge"], width=2)
        elif x > 336:
            d.line((x, 185, 336, 144), fill=P["wood_edge"], width=2)
        else:
            d.line((x, 145, x, 186), fill=P["wood_edge"], width=2)
    d.line((305, 143, 335, 143), fill=P["wood_light"], width=3)
    # Seedling shelves and silhouettes visible behind the glass.
    for y in [225, 244]:
        d.rectangle((260, y, 300, y + 2), fill=P["wood"])
        d.line((260, y, 299, y), fill=P["wood_light"])
    for x, y in [(264, 223), (275, 222), (286, 223), (262, 242), (276, 241), (290, 242),
                 (356, 222), (369, 222), (380, 223), (360, 241), (376, 242)]:
        d.line((x, y + 3, x, y - 5), fill=P["leaf_dark"])
        leaf_cluster(d, x - 2, y - 4, P["leaf_mid"], 2)
        d.point((x + 2, y - 3), fill=P["leaf_light"])
    # Central door and brass handle.
    d.rectangle((307, 205, 335, 261), fill=P["wood_dark"])
    d.rectangle((311, 208, 331, 259), fill=P["wood"])
    d.line((312, 210, 312, 257), fill=P["wood_sun"])
    d.line((321, 210, 321, 258), fill=P["wood_edge"])
    d.line((330, 210, 330, 258), fill=P["wood_light"])
    d.rectangle((327, 235, 329, 239), fill=P["glass_light"])
    # Lattice workbench and seed trays under the left eave.
    d.rectangle((210, 231, 235, 253), fill=P["wood_dark"])
    d.rectangle((213, 229, 239, 234), fill=P["wood_sun"])
    d.rectangle((215, 234, 237, 248), fill=P["wood"])
    for x in range(217, 237, 5):
        d.line((x, 234, x, 246), fill=P["wood_edge"])
    d.rectangle((217, 226, 233, 230), fill=P["terracotta"])
    for x in [220, 226, 231]:
        d.line((x, 226, x, 220), fill=P["leaf_dark"])
        leaf_cluster(d, x - 2, 220, P["leaf_mid"], 2)


def garden_scene() -> Image.Image:
    im = Image.new("RGB", (W, H), P["sky"])
    backdrop(im, "garden")
    framing_trees(im, SEED + 100)
    d = ImageDraw.Draw(im)
    # Irregular garden arbours frame the glasshouse without blocking its entrance.
    trellis(d, 120, 199, 274, SEED + 101)
    trellis(d, 469, 202, 274, SEED + 102)
    greenhouse(im)
    # Raised herb beds flank a centered, unobstructed route to the door.
    for x, width, seed in [(22, 137, 211), (481, 137, 212)]:
        d = ImageDraw.Draw(im)
        d.rectangle((x - 3, 252, x + width + 3, 272), fill=P["wood_dark"])
        d.rectangle((x, 250, x + width, 267), fill=P["wood"])
        d.line((x + 1, 251, x + width - 2, 251), fill=P["wood_sun"], width=2)
        for xx in range(x + 9, x + width - 5, 13):
            d.line((xx, 254, xx + 4, 264), fill=P["wood_edge"])
        rng = random.Random(SEED + seed)
        for xx in range(x + 8, x + width - 6, 10):
            plant_y = rng.randint(239, 249)
            d.line((xx, 253, xx - 1, plant_y + 4), fill=P["leaf_dark"])
            leaf_cluster(d, xx - 4, plant_y, rng.choice([P["leaf_mid"], P["leaf_light"]]), 2)
            if rng.random() < .6:
                flower(d, xx + rng.randint(-2, 2), plant_y - 1,
                       rng.choice([P["flower_pink"], P["flower_yellow"], P["flower_blue"]]))
        for xx in range(x + 7, x + width - 2, 30):
            d.line((xx, 266, xx, 272), fill=P["wood_edge"])
    # A small rain barrel and watering can are authored silhouettes, not props from an atlas.
    d = ImageDraw.Draw(im)
    d.rectangle((190, 245, 207, 269), fill=P["wood_dark"])
    d.rectangle((193, 248, 204, 266), fill=P["wood"])
    for y in [251, 257, 263]:
        d.line((193, y, 204, y), fill=P["wood_edge"])
    d.rectangle((188, 243, 209, 246), fill=P["metal_dark"])
    d.rectangle((426, 255, 441, 264), fill=P["metal_dark"])
    d.rectangle((430, 251, 436, 256), fill=P["metal_light"])
    d.line((440, 257, 447, 254), fill=P["metal_dark"], width=2)
    # Reapply the shared ledge over the bases, then add the stepping-stone threshold.
    walk_plane(im, "garden")
    d = ImageDraw.Draw(im)
    step_path(d, 280, 360, P["stone"], SEED + 103)
    # Foreground blooms/ferns stay to the side of the sprite lane.
    for x, y, height, seed in [(20, 276, 20, 31), (80, 276, 22, 32), (175, 276, 17, 33),
                               (464, 276, 19, 34), (558, 276, 24, 35), (621, 276, 18, 36)]:
        fern(d, x, y, height, SEED + seed)
    rng = random.Random(SEED + 104)
    for x in list(range(17, 246, 17)) + list(range(399, 630, 19)):
        yy = rng.randint(267, 276)
        flower(d, x, yy, rng.choice([P["flower_pink"], P["flower_yellow"], P["flower_blue"]]), rng.choice([1, 1, 2]))
    for x, y, color in [(46, 270, P["flower_blue"]), (150, 271, P["flower_yellow"]),
                        (501, 271, P["flower_pink"]), (592, 269, P["flower_yellow"])]:
        leaf_cluster(d, x, y, color, 2)
    # Short, broken highlights on the continuous top lip; center remains clear.
    d.line((0, 277, 245, 277), fill=P["grass_light"])
    d.line((395, 277, 639, 277), fill=P["grass_light"])
    return im


def roof_tiles(im: Image.Image, roof_shape: list[Point], seed: int) -> None:
    """Draw shingle pixels, clipped to the actual pitched roof silhouette."""
    rng = random.Random(seed)
    mask = Image.new("1", (W, H), 0)
    ImageDraw.Draw(mask).polygon(roof_shape, fill=1)
    layer = Image.new("RGB", (W, H), P["roof"])
    d = ImageDraw.Draw(layer)
    for row, y in enumerate(range(137, 199, 7)):
        shift = 6 if row % 2 else 0
        for x in range(178 + shift, 479, 12):
            d.rectangle((x + 1, y, x + 10, y + 5), fill=rng.choice([P["roof"], P["roof"], P["roof_light"], P["roof_shadow"]]))
            d.line((x + 2, y + 1, x + 8, y + 1), fill=P["roof_glint"])
            d.line((x + 2, y + 5, x + 9, y + 5), fill=P["roof_shadow"])
            if rng.random() < .16:
                d.rectangle((x + 5, y + 3, x + 7, y + 4), fill=P["roof_moss"])
    im.paste(layer, (0, 0), mask)


def gear(draw: ImageDraw.ImageDraw, cx: int, cy: int, r: int, color: str) -> None:
    # Pixel-stepped cog silhouette and a contrasting hub.
    points = []
    for i in range(16):
        a = math.tau * i / 16
        rr = r + (3 if i % 2 == 0 else 0)
        points.append((round(cx + math.cos(a) * rr), round(cy + math.sin(a) * rr)))
    polygon(draw, points, P["metal_dark"])
    draw.ellipse((cx - r + 2, cy - r + 2, cx + r - 2, cy + r - 2), fill=color)
    draw.ellipse((cx - 4, cy - 4, cx + 4, cy + 4), fill=P["metal_dark"])
    draw.rectangle((cx - 1, cy - 3, cx + 1, cy + 3), fill=P["metal_light"])
    draw.rectangle((cx - 3, cy - 1, cx + 3, cy + 1), fill=P["metal_light"])


def workshop_shed(im: Image.Image) -> None:
    d = ImageDraw.Draw(im)
    # Chimney/forge stack behind the roof, with small stepped smoke puffs.
    d.rectangle((233, 112, 254, 179), fill=P["stone_dark"])
    d.rectangle((237, 117, 250, 176), fill=P["stone"])
    for y in range(123, 175, 9):
        d.line((237, y, 250, y), fill=P["stone_edge"])
        d.line((242 if y % 18 else 247, y, 242 if y % 18 else 247, y + 7), fill=P["stone_edge"])
    d.rectangle((229, 109, 258, 116), fill=P["metal_dark"])
    d.line((231, 109, 256, 109), fill=P["metal_light"])
    for x, y, r in [(242, 99, 5), (248, 88, 6), (240, 76, 5)]:
        pts = organic_points(x, y, r, max(3, r - 1), random.Random(SEED + x + y), 5)
        polygon(d, pts, P["cloud_edge"])
        d.line((x - 2, y - 2, x + 1, y - 2), fill=P["cloud"])
    # Shed walls and deep stone footing.
    d.rectangle((196, 178, 462, 270), fill=P["wood_dark"])
    d.rectangle((202, 185, 456, 263), fill=P["plaster"])
    d.rectangle((202, 235, 456, 262), fill=P["wood"])
    d.rectangle((202, 258, 456, 271), fill=P["stone_dark"])
    for x in range(205, 455, 16):
        d.line((x, 239, x, 257), fill=P["wood_edge"])
        d.line((x + 1, 240, x + 1, 255), fill=P["wood_light"])
    for x in range(207, 454, 28):
        d.line((x, 260, x + 2, 269), fill=P["stone_edge"])
    # Long pitched roof with tiled surface and thick timber eaves.
    roof_outer = [(182, 194), (218, 164), (286, 132), (358, 132), (432, 164), (475, 194),
                  (465, 205), (191, 205)]
    roof_surface = [(188, 193), (220, 165), (287, 136), (357, 136), (430, 165), (468, 193),
                    (460, 199), (196, 199)]
    polygon(d, roof_outer, P["roof_dark"])
    polygon(d, roof_surface, P["roof"])
    roof_tiles(im, roof_surface, SEED + 201)
    d.line([(185, 193), (219, 165), (287, 134), (358, 134), (432, 165), (472, 193)], fill=P["wood_sun"], width=3)
    d.line((193, 201, 465, 201), fill=P["wood_dark"], width=5)
    d.line((197, 199, 461, 199), fill=P["wood_light"], width=2)
    for x in [211, 266, 395, 449]:
        d.rectangle((x, 195, x + 5, 234), fill=P["wood_edge"])
        d.line((x + 1, 198, x + 1, 232), fill=P["wood_sun"])
    # Central open double bay; glowing forge and benches sit inside the shadow.
    d.rectangle((285, 204, 371, 263), fill=P["metal_dark"])
    d.rectangle((290, 208, 366, 259), fill=P["wood_dark"])
    d.rectangle((297, 212, 359, 256), fill=P["plank_dark"])
    d.rectangle((300, 214, 356, 247), fill=P["forge_dark"])
    d.rectangle((303, 219, 352, 240), fill=P["forge"])
    d.rectangle((307, 223, 348, 235), fill=P["forge_light"])
    d.rectangle((313, 226, 344, 231), fill=P["forge_hot"])
    # Bay framing, threshold, and door slides.
    d.rectangle((282, 202, 290, 265), fill=P["wood_edge"])
    d.rectangle((367, 202, 375, 265), fill=P["wood_edge"])
    d.line((284, 204, 284, 263), fill=P["wood_sun"], width=2)
    d.line((370, 204, 370, 263), fill=P["wood_sun"], width=2)
    d.rectangle((288, 258, 370, 264), fill=P["wood_light"])
    d.line((289, 258, 368, 258), fill=P["stone_light"])
    # Tool board to the left: saw, wrench and mallet are distinct, icon-like forms.
    d.rectangle((224, 207, 274, 234), fill=P["plank_dark"])
    d.rectangle((227, 210, 270, 231), fill=P["plank"])
    for x, yy in [(233, 213), (246, 214), (260, 212)]:
        d.rectangle((x, yy, x + 3, yy + 7), fill=P["metal_light"])
        d.line((x + 1, yy + 7, x + 5, yy + 14), fill=P["metal_dark"], width=2)
        d.point((x + 1, yy + 1), fill=P["glass_glint"])
    d.line((233, 223, 242, 229), fill=P["metal_light"], width=2)
    d.line((234, 222, 230, 230), fill=P["metal_dark"], width=2)
    # Workbench, vise, and anvil just outside the open bay (above the walk line).
    d.rectangle((215, 242, 280, 249), fill=P["wood_dark"])
    d.rectangle((218, 239, 282, 244), fill=P["wood_sun"])
    d.line((220, 244, 220, 266), fill=P["wood_edge"], width=3)
    d.line((276, 244, 276, 266), fill=P["wood_edge"], width=3)
    d.rectangle((269, 232, 283, 238), fill=P["metal_dark"])
    d.rectangle((271, 229, 281, 233), fill=P["metal_light"])
    polygon(d, [(376, 252), (384, 246), (390, 248), (392, 252), (400, 254),
                (399, 258), (376, 258)], P["metal_dark"])
    d.line((379, 251, 388, 249, 392, 252), fill=P["metal_light"], width=2)
    # Brass gear window and hanging lantern mark the craft facade.
    d.rectangle((404, 208, 444, 233), fill=P["wood_dark"])
    d.rectangle((407, 211, 441, 230), fill=P["glass_dark"])
    d.rectangle((409, 213, 439, 228), fill=P["glass_mid"])
    gear(d, 424, 220, 7, P["roof_light"])
    d.rectangle((394, 205, 400, 233), fill=P["wood_edge"])
    d.rectangle((445, 205, 451, 233), fill=P["wood_edge"])
    d.line((410, 210, 438, 210), fill=P["wood_sun"])
    d.line((417, 185, 417, 195), fill=P["metal_dark"], width=2)
    d.rectangle((412, 189, 422, 199), fill=P["metal_dark"])
    d.rectangle((415, 191, 419, 197), fill=P["glass_light"])
    # Roof-side pulley wheel and timber braces.
    gear(d, 468, 215, 12, P["wood_light"])
    d.line((461, 203, 461, 235), fill=P["wood_dark"], width=3)
    d.line((454, 219, 477, 219), fill=P["wood_dark"], width=3)
    d.line((454, 218, 477, 218), fill=P["wood_sun"])
    # Roof ridge vent and bolts.
    d.rectangle((314, 126, 345, 136), fill=P["metal_dark"])
    d.rectangle((318, 122, 341, 130), fill=P["metal_light"])
    d.rectangle((321, 119, 338, 124), fill=P["metal_dark"])
    for x in [218, 267, 397, 447]:
        d.point((x, 201), fill=P["glass_light"])


def waterwheel(im: Image.Image) -> None:
    d = ImageDraw.Draw(im)
    cx, cy, r = 108, 229, 35
    d.ellipse((cx - r - 3, cy - r - 3, cx + r + 3, cy + r + 3), fill=P["wood_dark"])
    d.ellipse((cx - r, cy - r, cx + r, cy + r), fill=P["wood"])
    d.ellipse((cx - r + 5, cy - r + 5, cx + r - 5, cy + r - 5), fill=P["plank_dark"])
    for i in range(8):
        a = math.tau * i / 8
        x1, y1 = round(cx + math.cos(a) * 5), round(cy + math.sin(a) * 5)
        x2, y2 = round(cx + math.cos(a) * (r - 5)), round(cy + math.sin(a) * (r - 5))
        d.line((x1, y1, x2, y2), fill=P["wood_light"], width=4)
        d.line((x2, y2, round(cx + math.cos(a) * (r - 1)), round(cy + math.sin(a) * (r - 1))), fill=P["wood_dark"], width=2)
    d.ellipse((cx - 7, cy - 7, cx + 7, cy + 7), fill=P["metal_dark"])
    d.ellipse((cx - 3, cy - 3, cx + 3, cy + 3), fill=P["metal_light"])
    # Water trough and ripples are behind the foot plane at the map edge.
    d.rectangle((66, 264, 150, 272), fill=P["stone_dark"])
    d.rectangle((71, 265, 145, 269), fill=P["flower_blue_dark"])
    for x in [79, 97, 118, 134]:
        d.line((x, 266, x + 7, 266), fill=P["flower_blue"])


def workshop_scene() -> Image.Image:
    im = Image.new("RGB", (W, H), P["sky"])
    backdrop(im, "workshop")
    framing_trees(im, SEED + 200)
    workshop_shed(im)
    waterwheel(im)
    d = ImageDraw.Draw(im)
    # Scattered lumber stacks and crates occupy the margins, not the actor lane.
    for x, y, width in [(43, 246, 56), (501, 246, 70)]:
        for row in range(3):
            yy = y + row * 7
            d.rectangle((x + row * 2, yy, x + width - row * 2, yy + 5), fill=P["plank"])
            d.line((x + row * 2 + 2, yy + 1, x + width - row * 2 - 2, yy + 1), fill=P["plank_light"])
            d.line((x + width // 3, yy + 1, x + width // 3 - 2, yy + 4), fill=P["plank_dark"])
    d.rectangle((577, 245, 617, 273), fill=P["wood_dark"])
    d.rectangle((581, 249, 613, 269), fill=P["wood"])
    d.line((581, 258, 613, 258), fill=P["wood_edge"])
    d.line((595, 249, 595, 269), fill=P["wood_edge"])
    d.line((582, 250, 612, 250), fill=P["wood_sun"])
    # One stack of metal stock and a tool cart at the left side.
    d.rectangle((153, 258, 183, 270), fill=P["metal_dark"])
    for yy in [260, 264, 268]:
        d.line((155, yy, 181, yy), fill=P["metal_light"])
    d.rectangle((183, 247, 197, 266), fill=P["metal_dark"])
    d.rectangle((185, 250, 195, 262), fill=P["wood"])
    d.ellipse((184, 264, 189, 269), fill=P["metal_dark"])
    d.ellipse((193, 264, 198, 269), fill=P["metal_dark"])
    walk_plane(im, "workshop")
    d = ImageDraw.Draw(im)
    step_path(d, 272, 381, P["stone"], SEED + 202)
    # Sawdust flecks and cool metal edging stay below/away from the foot plane.
    rng = random.Random(SEED + 203)
    for _ in range(55):
        x, y = rng.choice(list(range(12, 238)) + list(range(405, 629))), rng.randint(268, 276)
        d.rectangle((x, y, x + rng.randint(1, 3), y), fill=rng.choice([P["soil_sand"], P["plank_light"], P["soil_light"]]))
    for x, y in [(38, 272), (210, 274), (441, 273), (610, 271)]:
        d.rectangle((x, y, x + 4, y + 1), fill=P["metal_light"])
    d.line((0, 277, 245, 277), fill=P["grass_light"])
    d.line((395, 277, 639, 277), fill=P["grass_light"])
    return im


# Owner sprite colors match the existing idle Owner's dark hair, round glasses,
# warm face, blue overshirt, cream shirt and charcoal trousers/shoes.
S = {
    "outline": "#263542", "hair_dark": "#222e39", "hair": "#344150",
    "hair_mid": "#4a5664", "hair_light": "#64707b", "skin_shadow": "#ce927b",
    "skin": "#efbb94", "skin_light": "#ffdab0", "skin_glint": "#ffe6bd",
    "cheek": "#df9a88", "eye": "#26343c", "eye_white": "#fff7df",
    "jacket_dark": "#294e6b", "jacket": "#386f91", "jacket_mid": "#4f91b0",
    "jacket_light": "#80bfd1", "shirt": "#f1deae", "shirt_shadow": "#c3bfa1",
    "trouser": "#425968", "trouser_light": "#657b84", "shoe": "#293d48",
    "sole": "#82969a", "badge": "#f0c779", "glass": "#345367",
}


def _box(d: ImageDraw.ImageDraw, x0: int, y0: int, x1: int, y1: int, fill: str, dy: int = 0) -> None:
    d.rectangle((x0, y0 + dy, x1, y1 + dy), fill=fill)


def _poly(d: ImageDraw.ImageDraw, pts: Iterable[Point], fill: str, dy: int = 0) -> None:
    polygon(d, [(x, y + dy) for x, y in pts], fill)


def owner_head(d: ImageDraw.ImageDraw, dy: int, blink: bool = False) -> None:
    head = [(13, 4), (27, 4), (27, 6), (31, 6), (31, 8), (33, 8), (33, 12),
            (34, 12), (34, 24), (32, 24), (32, 28), (28, 28), (28, 30),
            (24, 30), (24, 32), (14, 32), (14, 30), (10, 30), (10, 27),
            (7, 27), (7, 24), (5, 24), (5, 20), (7, 18), (7, 12), (9, 12), (9, 8), (13, 8)]
    _poly(d, head, S["outline"], dy)
    _poly(d, [(10, 15), (28, 13), (31, 18), (31, 26), (27, 29), (23, 31),
              (14, 31), (10, 28), (8, 24), (6, 23), (8, 20)], S["skin_shadow"], dy)
    _poly(d, [(11, 15), (26, 14), (29, 18), (29, 25), (26, 28), (22, 30),
              (14, 30), (10, 27), (8, 23), (7, 22), (9, 20)], S["skin"], dy)
    _poly(d, [(11, 17), (23, 16), (25, 19), (24, 26), (20, 28), (13, 27), (10, 23)], S["skin_light"], dy)
    _box(d, 29, 21, 32, 24, S["skin"], dy)
    _box(d, 31, 22, 31, 22, S["skin_shadow"], dy)
    if blink:
        d.line((10, 22 + dy, 12, 22 + dy), fill=S["eye"])
        d.line((21, 22 + dy, 24, 22 + dy), fill=S["eye"])
    else:
        _box(d, 10, 20, 12, 23, S["eye"], dy)
        _box(d, 21, 20, 24, 24, S["eye"], dy)
        _box(d, 11, 20, 11, 20, S["eye_white"], dy)
        _box(d, 22, 20, 23, 21, S["eye_white"], dy)
    # Small squared glasses and the bridge are signature, high-contrast pixels.
    d.rectangle((8, 19 + dy, 15, 25 + dy), outline=S["glass"])
    d.rectangle((19, 19 + dy, 27, 25 + dy), outline=S["glass"])
    d.line((15, 21 + dy, 19, 21 + dy), fill=S["glass"])
    d.line((27, 20 + dy, 30, 19 + dy), fill=S["glass"])
    _box(d, 9, 19, 9, 19, S["jacket_light"], dy)
    _box(d, 20, 19, 20, 19, S["jacket_light"], dy)
    _box(d, 7, 24, 7, 24, S["skin_glint"], dy)
    _box(d, 10, 26, 12, 26, S["cheek"], dy)
    _box(d, 25, 26, 27, 26, S["cheek"], dy)
    d.line((14, 28 + dy, 17, 28 + dy), fill=S["skin_shadow"])
    hair = [(9, 13), (10, 9), (14, 9), (14, 6), (20, 5), (20, 4), (27, 5),
            (31, 8), (33, 12), (33, 21), (31, 24), (29, 20), (28, 15),
            (23, 15), (23, 12), (19, 15), (15, 16), (16, 12), (11, 18), (8, 19)]
    _poly(d, hair, S["hair_dark"], dy)
    _poly(d, [(12, 10), (16, 7), (23, 6), (28, 8), (29, 11), (25, 10),
              (20, 13), (20, 9), (16, 13), (16, 10), (11, 15)], S["hair"], dy)
    d.line((17, 7 + dy, 22, 7 + dy), fill=S["hair_mid"])
    d.line((24, 7 + dy, 28, 9 + dy), fill=S["hair_light"])
    d.line((31, 13 + dy, 31, 18 + dy), fill=S["hair_mid"])


def owner_body(d: ImageDraw.ImageDraw, dy: int, swing: int, pose: str = "walk") -> None:
    # The same compact blue coat/cream shirt silhouette as the idle sprite.
    _box(d, 16, 29, 24, 34, S["skin_shadow"], dy)
    _box(d, 17, 30, 22, 32, S["skin"], dy)
    _poly(d, [(12, 32), (16, 31), (20, 33), (24, 31), (28, 33), (30, 42),
              (28, 46), (12, 46), (10, 41)], S["outline"], dy)
    _poly(d, [(13, 33), (16, 32), (19, 35), (23, 33), (27, 34), (29, 42),
              (27, 45), (13, 45), (11, 41)], S["jacket_mid"], dy)
    _box(d, 15, 35, 19, 44, S["shirt"], dy)
    _box(d, 18, 36, 19, 44, S["shirt_shadow"], dy)
    _poly(d, [(15, 32), (18, 35), (17, 39), (12, 35)], S["jacket_light"], dy)
    _poly(d, [(22, 33), (23, 38), (25, 35)], S["jacket_light"], dy)
    _box(d, 23, 36, 28, 42, S["jacket"], dy)
    d.line((26, 36 + dy, 26, 43 + dy), fill=S["jacket_light"])
    d.line((21, 39 + dy, 21, 44 + dy), fill=S["jacket_dark"])
    if pose == "jump":
        _poly(d, [(11, 35), (14, 35), (13, 31), (10, 26), (7, 27), (8, 32)], S["outline"], dy)
        _poly(d, [(11, 36), (13, 35), (11, 31), (9, 28), (8, 29), (9, 33)], S["jacket_mid"], dy)
        _box(d, 7, 25, 9, 27, S["skin"], dy)
        _poly(d, [(27, 35), (30, 35), (33, 31), (34, 26), (31, 24), (28, 28)], S["outline"], dy)
        _poly(d, [(28, 35), (30, 34), (32, 30), (33, 27), (31, 26), (29, 30)], S["jacket_mid"], dy)
        _box(d, 32, 23, 34, 26, S["skin"], dy)
    elif pose == "fall":
        _poly(d, [(11, 35), (14, 36), (12, 39), (8, 38), (4, 40), (1, 38), (2, 34), (7, 35)], S["outline"], dy)
        _poly(d, [(11, 36), (13, 37), (10, 38), (7, 37), (4, 39), (3, 37), (7, 36)], S["jacket_mid"], dy)
        _box(d, 1, 35, 3, 38, S["skin"], dy)
        _poly(d, [(27, 35), (30, 36), (32, 35), (37, 34), (39, 37), (37, 40), (32, 38), (28, 39)], S["outline"], dy)
        _poly(d, [(28, 36), (30, 37), (33, 36), (37, 35), (38, 37), (36, 38), (32, 37), (29, 38)], S["jacket_mid"], dy)
        _box(d, 37, 35, 39, 38, S["skin"], dy)
    elif pose in ("idle", "land"):
        # Resting hands stay close to the coat; alternate one elbow pixel for
        # the slow breathing loop without moving the shoes off their baseline.
        _poly(d, [(11, 35), (14, 36), (13, 42), (11, 45), (8, 43), (9, 37)], S["outline"], dy)
        _poly(d, [(11, 36), (13, 37), (12, 42), (10, 43), (9, 41), (10, 37)], S["jacket_mid"], dy)
        _box(d, 9, 42, 11, 44, S["skin"], dy)
        if swing < 0:
            _poly(d, [(27, 35), (30, 36), (31, 41), (29, 44), (26, 42), (26, 38)], S["outline"], dy)
            _poly(d, [(28, 36), (29, 37), (30, 41), (28, 42), (27, 40)], S["jacket_mid"], dy)
            _box(d, 27, 41, 29, 43, S["skin"], dy)
        else:
            _poly(d, [(27, 35), (30, 36), (31, 42), (29, 45), (26, 43), (26, 38)], S["outline"], dy)
            _poly(d, [(28, 36), (29, 37), (30, 41), (28, 43), (27, 40)], S["jacket_mid"], dy)
            _box(d, 27, 42, 29, 44, S["skin"], dy)
    # Alternating little arm swing; only limb pixels move, torso remains stable.
    elif swing < 0:
        _poly(d, [(11, 35), (14, 36), (13, 42), (10, 44), (8, 42), (9, 37)], S["outline"], dy)
        _box(d, 10, 38, 12, 42, S["skin"], dy)
        _poly(d, [(27, 35), (30, 37), (31, 43), (28, 46), (25, 43), (27, 39)], S["outline"], dy)
        _box(d, 27, 40, 29, 44, S["skin"], dy)
    else:
        _poly(d, [(11, 35), (14, 37), (15, 42), (12, 46), (9, 44), (9, 39)], S["outline"], dy)
        _box(d, 10, 41, 12, 44, S["skin"], dy)
        _poly(d, [(27, 35), (30, 36), (31, 41), (29, 45), (26, 43), (26, 38)], S["outline"], dy)
        _box(d, 28, 40, 30, 43, S["skin"], dy)
    _box(d, 21, 40, 24, 42, S["jacket_dark"], dy)
    d.line((21, 40 + dy, 24, 40 + dy), fill=S["jacket_light"])
    _box(d, 13, 38, 13, 38, S["badge"], dy)


LEG_POSES = [
    # Frontward left stride; trailing right shoe remains planted at y=54.
    ([[(21, 42), (28, 42), (29, 47), (27, 51), (21, 50), (20, 46)],
      [(12, 42), (19, 42), (18, 47), (15, 50), (10, 50), (11, 46)]],
     [[(23, 49), (29, 49), (32, 52), (32, 54), (22, 54), (21, 52)],
      [(8, 49), (15, 49), (18, 52), (18, 54), (7, 54), (7, 52)]]),
    # Passing phase: lead leg under the hip, other leg folds back.
    ([[(22, 42), (28, 42), (27, 47), (30, 50), (27, 52), (22, 49)],
      [(13, 42), (20, 42), (20, 49), (18, 52), (12, 51), (12, 46)]],
     [[(25, 49), (30, 50), (32, 52), (32, 54), (24, 54), (23, 52)],
      [(11, 50), (17, 50), (20, 52), (20, 54), (10, 54), (9, 52)]]),
    # Opposite contact stride.
    ([[(22, 42), (28, 42), (29, 47), (31, 50), (29, 52), (22, 50)],
      [(12, 42), (19, 42), (17, 47), (13, 51), (9, 50), (10, 46)]],
     [[(26, 49), (32, 49), (35, 52), (35, 54), (25, 54), (24, 52)],
      [(8, 49), (14, 49), (17, 52), (17, 54), (7, 54), (7, 52)]]),
    # Second passing phase with the trailing knee switched.
    ([[(21, 42), (27, 42), (28, 48), (25, 52), (20, 50), (21, 46)],
      [(12, 42), (19, 42), (18, 47), (20, 50), (17, 52), (12, 49)]],
     [[(20, 49), (26, 50), (28, 52), (28, 54), (19, 54), (18, 52)],
      [(15, 50), (21, 50), (23, 52), (23, 54), (13, 54), (12, 52)]]),
]


def owner_frame(index: int) -> Image.Image:
    im = Image.new("RGBA", (40, 56), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    # Small vertical head/body recovery is paired with clear leg and arm changes.
    bob = [0, 1, 0, 1][index]
    legs, shoes = LEG_POSES[index]
    # Far leg first, so near leg and jacket naturally overlap it.
    for i, pts in enumerate(legs):
        _poly(d, pts, S["outline"])
        inner = [(x + (1 if x < 20 else -1), y) for x, y in pts]
        _poly(d, inner, S["trouser"])
        if i == 0:
            d.line((25, 44, 25, 48), fill=S["trouser_light"])
        else:
            d.line((14, 44, 14, 48), fill=S["trouser_light"])
    for i, pts in enumerate(shoes):
        _poly(d, pts, S["outline"])
        # Inset fill leaves the dark outline intact and the sole on a fixed baseline.
        shoe_inner = [(x + (1 if x < 20 else -1), min(53, y)) for x, y in pts]
        _poly(d, shoe_inner, S["shoe"])
        ysole = max(y for _, y in pts)
        if ysole >= 54:
            xvals = [x for x, y in pts if y >= 54]
            if xvals:
                d.line((min(xvals) + 1, 54, max(xvals) - 1, 54), fill=S["sole"])
    owner_body(d, bob, -1 if index in (0, 3) else 1)
    owner_head(d, bob)
    return im


def owner_walk_sheet() -> Image.Image:
    sheet = Image.new("RGBA", (160, 56), (0, 0, 0, 0))
    for frame in range(4):
        sheet.alpha_composite(owner_frame(frame), (frame * 40, 0))
    return sheet


def draw_owner_legs(
    d: ImageDraw.ImageDraw,
    legs: list[list[Point]],
    shoes: list[list[Point]],
) -> None:
    for index, pts in enumerate(legs):
        _poly(d, pts, S["outline"])
        inner = [(x + (1 if x < 20 else -1), y) for x, y in pts]
        _poly(d, inner, S["trouser"])
        if index == 0:
            d.line((25, 44, 25, 48), fill=S["trouser_light"])
        else:
            d.line((14, 44, 14, 48), fill=S["trouser_light"])
    for pts in shoes:
        _poly(d, pts, S["outline"])
        shoe_inner = [(x + (1 if x < 20 else -1), min(53, y)) for x, y in pts]
        _poly(d, shoe_inner, S["shoe"])
        ysole = max(y for _, y in pts)
        if ysole >= 54:
            xvals = [x for x, y in pts if y >= 54]
            if xvals:
                d.line((min(xvals) + 1, 54, max(xvals) - 1, 54), fill=S["sole"])


IDLE_LEGS: list[list[Point]] = [
    [(14, 42), (19, 42), (19, 51), (17, 53), (13, 51), (13, 46)],
    [(21, 42), (26, 42), (27, 50), (25, 53), (21, 51), (20, 46)],
]
IDLE_SHOES: list[list[Point]] = [
    [(11, 50), (17, 50), (20, 52), (20, 54), (10, 54), (9, 52)],
    [(22, 50), (28, 50), (31, 52), (31, 54), (21, 54), (20, 52)],
]


def owner_idle_frame(index: int) -> Image.Image:
    bob, blink, arm_swing = [(0, False, 0), (1, False, 0), (0, False, -1), (0, True, 0)][index]
    im = Image.new("RGBA", (40, 56), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    draw_owner_legs(d, IDLE_LEGS, IDLE_SHOES)
    owner_body(d, bob, arm_swing, pose="idle")
    owner_head(d, bob, blink=blink)
    return im


def owner_air_frame(index: int) -> Image.Image:
    air_poses = [
        ("jump", -1, [[(19, 42), (25, 42), (25, 47), (22, 49), (18, 47)],
                       [(13, 42), (19, 42), (19, 47), (16, 49), (12, 47)]],
         [[(21, 47), (27, 48), (29, 50), (27, 52), (21, 51)],
          [(12, 47), (17, 48), (19, 50), (17, 52), (11, 51)]]),
        ("jump", -2, [[(20, 41), (26, 41), (25, 46), (22, 48), (19, 46)],
                       [(13, 41), (19, 41), (19, 46), (16, 48), (12, 46)]],
         [[(22, 46), (28, 47), (30, 49), (28, 51), (22, 50)],
          [(11, 46), (16, 47), (18, 49), (16, 51), (10, 50)]]),
        ("fall", -1, [[(20, 42), (26, 42), (27, 48), (25, 50), (21, 49)],
                       [(13, 42), (19, 42), (18, 48), (16, 50), (12, 49)]],
         [[(23, 48), (29, 49), (31, 51), (31, 53), (22, 52)],
          [(10, 48), (16, 49), (18, 51), (17, 53), (9, 52)]]),
        ("fall", 0, [[(20, 42), (26, 42), (27, 49), (25, 51), (21, 50)],
                      [(13, 42), (19, 42), (18, 49), (16, 51), (12, 50)]],
         [[(23, 49), (29, 50), (31, 52), (31, 54), (22, 53)],
          [(10, 49), (16, 50), (18, 52), (17, 54), (9, 53)]]),
    ][index]
    pose, bob, legs, shoes = air_poses
    im = Image.new("RGBA", (40, 56), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    draw_owner_legs(d, legs, shoes)
    owner_body(d, bob, index % 2, pose=pose)
    owner_head(d, bob)
    return im


def owner_land_frame(index: int) -> Image.Image:
    if index > 0:
        return owner_idle_frame((1, 2, 0)[index - 1])
    im = Image.new("RGBA", (40, 56), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    crouched_legs = [
        [(14, 43), (20, 43), (20, 49), (18, 52), (13, 51)],
        [(21, 43), (27, 43), (27, 49), (25, 52), (20, 51)],
    ]
    crouched_shoes = [
        [(11, 50), (18, 50), (21, 52), (21, 54), (10, 54), (9, 52)],
        [(22, 50), (29, 50), (31, 52), (31, 54), (21, 54), (20, 52)],
    ]
    draw_owner_legs(d, crouched_legs, crouched_shoes)
    owner_body(d, 1, 0, pose="land")
    owner_head(d, 1)
    return im


def owner_states_sheet() -> Image.Image:
    sheet = Image.new("RGBA", (160, 168), (0, 0, 0, 0))
    rows = ([owner_idle_frame(i) for i in range(4)],
            [owner_air_frame(i) for i in range(4)],
            [owner_land_frame(i) for i in range(4)])
    for row, frames in enumerate(rows):
        for column, frame in enumerate(frames):
            sheet.alpha_composite(frame, (column * 40, row * 56))
    return sheet


def save_asset(name: str, image: Image.Image, description: str) -> dict[str, object]:
    OUT.mkdir(parents=True, exist_ok=True)
    path = OUT / name
    metadata = PngImagePlugin.PngInfo()
    metadata.add_text("Title", name.removesuffix(".png"))
    metadata.add_text("Author", "Original DiOffice art, authored with Hermes")
    metadata.add_text("Description", description)
    metadata.add_text("Source", "Original deterministic Pillow drawing; no external images, fonts or sprites")
    metadata.add_text("Generator", "tools/art/generate-studio-world-expansion.py; seed=738214")
    metadata.add_text("Rendering", "Logical pixels; hard edges; nearest-neighbor integer display scale")
    image.save(path, format="PNG", optimize=True, pnginfo=metadata)
    with Image.open(path) as reopened:
        reopened.verify()
    with Image.open(path) as reopened:
        assert reopened.size == image.size, (name, reopened.size, image.size)
        assert reopened.mode == image.mode, (name, reopened.mode, image.mode)
        assert reopened.n_frames == 1, (name, reopened.n_frames)
        assert reopened.tobytes() == image.tobytes(), f"pixel round-trip mismatch: {name}"
        if reopened.mode == "RGBA":
            alpha = set(reopened.getchannel("A").tobytes())
            assert alpha == {0, 255}, (name, alpha)
            assert reopened.getchannel("A").getbbox() is not None
        return {
            "path": str(path), "dimensions": list(reopened.size), "mode": reopened.mode,
            "bytes": path.stat().st_size,
            "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            "unique_colors": len(reopened.getcolors(maxcolors=W * H) or []),
            "alpha_values": sorted(set(reopened.getchannel("A").tobytes())) if reopened.mode == "RGBA" else None,
            "verified": True,
        }


def verify_walk_frames(sheet: Image.Image) -> None:
    frames = [sheet.crop((i * 40, 0, (i + 1) * 40, 56)) for i in range(4)]
    assert len({frame.tobytes() for frame in frames}) == 4, "walk frames must be visibly distinct"
    assert all(frame.getchannel("A").getbbox() is not None for frame in frames)
    assert all(set(frame.getchannel("A").tobytes()) <= {0, 255} for frame in frames)
    # Each frame retains the head/glasses palette and an unchanged canvas/footline.
    for frame in frames:
        assert frame.getpixel((10, 20))[3] == 255
        assert frame.getpixel((20, 55))[3] == 0
        assert any(frame.getpixel((x, 54))[3] == 255 for x in range(40)), "feet must reach the common baseline"


def verify_owner_states(sheet: Image.Image) -> None:
    assert sheet.size == (160, 168) and sheet.mode == "RGBA"
    for row in range(3):
        frames = [sheet.crop((column * 40, row * 56, (column + 1) * 40, (row + 1) * 56))
                  for column in range(4)]
        assert len({frame.tobytes() for frame in frames}) == 4, f"owner state row {row} needs four distinct poses"
        for frame in frames:
            alpha = set(frame.getchannel("A").tobytes())
            assert frame.getchannel("A").getbbox() is not None
            assert alpha <= {0, 255}, (row, alpha)
            if row != 1:
                assert any(frame.getpixel((x, 54))[3] == 255 for x in range(40)), "grounded frames must share the foot baseline"


def main() -> None:
    garden = garden_scene()
    workshop = workshop_scene()
    walk = owner_walk_sheet()
    states = owner_states_sheet()
    assert garden.size == workshop.size == (640, 360)
    assert garden.mode == workshop.mode == "RGB"
    assert walk.size == (160, 56) and walk.mode == "RGBA"
    assert states.size == (160, 168) and states.mode == "RGBA"
    verify_walk_frames(walk)
    verify_owner_states(states)
    records = [
        save_asset("studio-world-garden.png", garden,
                   "640x360 original garden/glasshouse explorable zone; shared flat y=277 walk plane; original scenery only; no UI or text"),
        save_asset("studio-world-workshop.png", workshop,
                   "640x360 original timber workshop/forge explorable zone; shared flat y=277 walk plane; original scenery only; no UI or text"),
        save_asset("studio-owner-walk.png", walk,
                   "160x56 horizontal four-frame Owner walk cycle; each frame 40x56 RGBA with transparent 0/255 alpha; glasses and chibi palette preserved; feet grounded"),
        save_asset("studio-owner-states.png", states,
                   "160x168 original Owner state sheet; four 40x56 idle frames with breathing/blink, two rising and two falling frames, and four landing/recovery frames; binary transparency and grounded footline"),
    ]
    assert len(records) == 4
    print(json.dumps({"asset_count": len(records), "seed": SEED, "assets": records}, indent=2))


if __name__ == "__main__":
    main()
