"""Draw original, deterministic DiOffice woodland pixel art with Pillow.

Run from any directory with:
    uv run --no-project --with "pillow>=12,<13" python tools/art/generate-studio-art.py

All drawing is on the logical pixel grid: no external images, fonts, tracing,
antialiasing, gradients, filters, or resampling. The only deliverables are one
640x360 world and two single-frame, transparent 40x56 idle sprites. The bottom
center porch is intentionally quiet for separately rendered interactive props.
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
SEED = 61427
Point = tuple[int, int]

# Deliberately bounded, warm/cool ramp palette rather than RGB noise.
P = {
    "sky": "#bae6e5", "sky_high": "#c7eeeb", "sky_low": "#d4f0e5",
    "cloud_shadow": "#a2d3d5", "cloud_edge": "#d9eee5", "cloud": "#edf5df",
    "cloud_light": "#fff8e5", "far": "#99cbbb", "far_light": "#b0d8c0",
    "hill": "#83baa2", "hill_light": "#9dcbad", "forest_far": "#69a790",
    "forest_mid": "#5b987c", "forest_light": "#76ad88", "forest_dark": "#498269",
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
    "metal": "#38545a", "metal_light": "#6e8983", "metal_dark": "#2b4149",
    "stone_dark": "#5f6b64", "stone": "#98a297", "stone_light": "#c1c4aa",
    "stone_edge": "#778379", "grass_dark": "#476944", "grass": "#64934e",
    "grass_mid": "#85b357", "grass_light": "#accc6a", "grass_sun": "#cee58b",
    "soil_dark": "#645243", "soil": "#86664a", "soil_mid": "#a58458",
    "soil_light": "#c7a874", "soil_sand": "#d8c48a", "flower_pink": "#dd9b9b",
    "flower_light": "#f4c1aa", "flower_blue": "#a8cfcb", "flower_blue_dark": "#6ba3b2",
    "flower_yellow": "#f5da8f", "terracotta": "#b87957", "terracotta_dark": "#875b47",
}


def rgb(value: str) -> tuple[int, int, int]:
    """Convert a hex palette entry into an RGB tuple."""
    return tuple(int(value[i:i + 2], 16) for i in (1, 3, 5))


def polygon(draw: ImageDraw.ImageDraw, points: Iterable[Point], color: str) -> None:
    draw.polygon(list(points), fill=color)


def organic_points(
    cx: int, cy: int, rx: int, ry: int, rng: random.Random, lobes: int = 9,
) -> list[Point]:
    """Make a pixel-stepped, asymmetric leafy silhouette, not a circle."""
    phase = rng.random() * 6.28
    result = []
    for i in range(90):
        angle = math.tau * i / 90
        radius = 1 + .075 * math.sin(lobes * angle + phase)
        radius += .035 * math.sin(17 * angle + phase * 2)
        radius += rng.uniform(-.025, .025)
        result.append((round(cx + math.cos(angle) * rx * radius),
                       round(cy + math.sin(angle) * ry * radius)))
    return result


def cloud(draw: ImageDraw.ImageDraw, x: int, y: int, width: int, seed: int) -> None:
    rng = random.Random(seed)
    height = max(10, width // 4)
    draw.rectangle((x + 4, y + height - 1, x + width - 4, y + height + 3), P["cloud_shadow"])
    lobes = [(0.15, .70, .19), (.34, .38, .22), (.54, .48, .20), (.74, .75, .20)]
    for fx, fy, fr in lobes:
        r = round(width * fr)
        xx, yy = round(x + width * fx), round(y + height * fy)
        pts = organic_points(xx, yy, r, max(4, round(r * .53)), rng, 5)
        polygon(draw, pts, P["cloud_edge"])
        polygon(draw, [(px, py - 2) for px, py in pts], P["cloud"])
    draw.rectangle((x + 6, y + height - 4, x + width - 4, y + height), P["cloud"])
    draw.line((x + 9, y + height + 1, x + width - 6, y + height + 1), fill=P["cloud_edge"])
    draw.line((x + width // 4, y + 1, x + width // 3, y + 1), fill=P["cloud_light"])
    draw.rectangle((x + width // 4 - 2, y + 3, x + width // 4 + 4, y + 4), P["cloud_light"])


def hills(im: Image.Image, draw: ImageDraw.ImageDraw) -> None:
    # Keep the sky one quiet cyan field; horizontal color bands read as panels.
    for args in [(34, 40, 81, 1), (177, 75, 62, 2), (291, 28, 76, 3),
                 (449, 55, 92, 4), (597, 107, 51, 5)]:
        cloud(draw, *args)
    ridge = [(x, round(158 + 17 * math.sin(x / 65) + 9 * math.cos(x / 43)))
             for x in range(-4, W + 5, 4)]
    polygon(draw, ridge + [(W, 242), (0, 242)], P["far"])
    polygon(draw, [(x, y + 5) for x, y in ridge] + [(W, 196), (0, 200)], P["far_light"])
    ridge2 = [(x, round(199 + 17 * math.sin(x / 73 + 1.7) + 8 * math.sin(x / 36)))
              for x in range(-4, W + 5, 4)]
    polygon(draw, ridge2 + [(W, 276), (0, 276)], P["hill"])
    polygon(draw, [(x, y + 3) for x, y in ridge2] + [(W, 230), (0, 239)], P["hill_light"])
    # A low, atmospheric band of irregular broadleaf crowns.
    rng = random.Random(SEED + 1)
    for x in range(-12, W + 25, 19):
        yy = rng.randint(182, 210)
        rr = rng.randint(15, 25)
        polygon(draw, organic_points(x, yy, rr, rng.randint(21, 37), rng, 7), P["forest_far"])
        draw.line((x, yy + 14, x + 1, 256), fill=P["forest_mid"], width=2)
        if rng.random() > .5:
            polygon(draw, organic_points(x - 5, yy - 5, rr - 7, 14, rng, 6), P["forest_light"])
    # Midground forest has a darker lower edge, intermittent trunks and vines.
    for x in range(-10, W + 20, 28):
        yy = rng.randint(221, 239)
        polygon(draw, organic_points(x, yy, rng.randint(21, 29), 25, rng), P["forest_mid"])
        draw.line((x - 3, yy + 9, x - 2, 271), fill=P["forest_dark"], width=3)
        draw.line((x - 3, yy + 15, x - 13, yy + 6), fill=P["forest_dark"], width=2)
    draw.rectangle((0, 256, W, 285), P["forest_dark"])
    for _ in range(290):
        x, y = rng.randrange(W), rng.randrange(241, 278)
        if im.getpixel((x, y)) in (rgb(P["forest_mid"]), rgb(P["forest_dark"])):
            draw.rectangle((x, y, x + rng.randint(1, 3), y + 1), P["forest_light"])


def leaf_cluster(draw: ImageDraw.ImageDraw, x: int, y: int, color: str, size: int = 1) -> None:
    """Small stepped, three-part leaf motif with intentional negative space."""
    draw.rectangle((x, y, x + size + 1, y), color)
    draw.rectangle((x - 1, y + 1, x + size, y + 1), color)
    if size > 1:
        draw.rectangle((x + 1, y + 2, x + size, y + 2), color)


def crown(
    im: Image.Image, cx: int, cy: int, rx: int, ry: int, seed: int,
    shadowed: bool = False,
) -> None:
    """Layer an organic outline, shaded foliage volumes and sparse leaf glints."""
    rng = random.Random(seed)
    draw = ImageDraw.Draw(im)
    pts = organic_points(cx, cy, rx, ry, rng, rng.randint(7, 11))
    polygon(draw, [(x, y + 2) for x, y in pts], P["tree_outline"])
    polygon(draw, pts, P["leaf_dark"] if shadowed else P["leaf_shadow"])
    inner = organic_points(cx - rx // 12, cy - ry // 7, max(4, rx - 3), max(4, ry - 4), rng)
    polygon(draw, inner, P["leaf_shadow"] if shadowed else P["leaf"])
    lit = organic_points(cx - rx // 5, cy - ry // 4, max(3, int(rx * .75)), max(3, int(ry * .66)), rng)
    polygon(draw, lit, P["leaf"] if shadowed else P["leaf_mid"])
    # Broken patches avoid a flat geometric ellipse and give canopy depth.
    for _ in range(max(2, rx // 12)):
        lx = rng.randint(cx - rx // 2, cx + rx // 3)
        ly = rng.randint(cy - ry // 2, cy + ry // 4)
        patch = organic_points(lx, ly, rng.randint(5, 12), rng.randint(4, 8), rng, 5)
        polygon(draw, patch, P["leaf_mid"] if shadowed else P["leaf_light"])
    allowed = {rgb(P[k]) for k in ("leaf_dark", "leaf_shadow", "leaf", "leaf_mid", "leaf_light")}
    for _ in range(max(20, rx * ry // 6)):
        x = rng.randint(max(0, cx - rx + 2), min(W - 5, cx + rx - 2))
        y = rng.randint(max(0, cy - ry + 2), min(H - 4, cy + ry - 2))
        if im.getpixel((x, y)) not in allowed:
            continue
        light = y < cy + rng.randint(-6, 4)
        color = rng.choice([P["leaf_light"], P["leaf_glint"], P["leaf_mid"]]) if light else rng.choice([P["leaf_shadow"], P["leaf_dark"], P["leaf"]])
        if shadowed and color == P["leaf_glint"]:
            color = P["leaf_light"]
        leaf_cluster(draw, x, y, color, rng.choice([1, 1, 2, 2, 3]))
    # Tiny two-tone tips on the sun-facing upper contour.
    for x, y in pts[45:82:5]:
        if 0 <= x < W - 2 and 0 <= y < H - 2:
            draw.rectangle((x, y + 2, x + 2, y + 2), P["leaf_light"])


def tree_trunk(draw: ImageDraw.ImageDraw, x: int, top: int, bottom: int, width: int, seed: int) -> None:
    rng = random.Random(seed)
    left, right = x - width // 2, x + width // 2
    polygon(draw, [(left - 8, bottom), (left + 1, bottom - 12), (left + 4, top + 15),
                   (x, top), (right - 3, top + 13), (right + 2, bottom - 14),
                   (right + 10, bottom), (x + 8, bottom + 3), (x - 10, bottom + 3)], P["bark_dark"])
    polygon(draw, [(left - 3, bottom - 2), (left + 5, bottom - 17), (left + 7, top + 20),
                   (x + 1, top + 9), (x + 6, bottom - 13), (x + 11, bottom - 1)], P["bark"])
    polygon(draw, [(left + 4, bottom - 8), (left + 7, top + 23), (x, top + 19),
                   (x + 1, bottom - 16)], P["bark_mid"])
    # Forks are drawn beneath crowns; not floating ornamental sticks.
    draw.line([(x + 3, top + 63), (x - 25, top + 31), (x - 39, top + 20)], fill=P["bark_dark"], width=9)
    draw.line([(x + 1, top + 59), (x - 24, top + 28), (x - 38, top + 17)], fill=P["bark"], width=5)
    draw.line([(x, top + 75), (x + 29, top + 37), (x + 45, top + 29)], fill=P["bark_dark"], width=8)
    draw.line([(x + 1, top + 72), (x + 30, top + 35), (x + 44, top + 26)], fill=P["bark_mid"], width=4)
    for _ in range(28):
        yy = rng.randint(top + 49, bottom - 8)
        xx = rng.randint(left + 5, right - 4)
        draw.line((xx, yy, xx - 1, yy + rng.randint(3, 9)), fill=rng.choice([P["bark_dark"], P["bark_mid"], P["bark_light"]]))
    draw.ellipse((x - 4, bottom - 54, x + 3, bottom - 46), fill=P["bark_dark"])
    draw.arc((x - 3, bottom - 53, x + 3, bottom - 46), 100, 310, fill=P["bark_light"])
    # Ivy follows the trunk with opposing single leaves.
    for yy in range(bottom - 77, bottom - 10, 6):
        xx = x + round(3 * math.sin(yy / 11))
        draw.line((xx, yy, xx + 1, yy + 6), fill=P["leaf_shadow"])
        leaf_cluster(draw, xx - 4, yy + 1, P["leaf_mid"], 2)
        leaf_cluster(draw, xx + 2, yy + 4, P["leaf_light"], 1)


def grass_tuft(draw: ImageDraw.ImageDraw, x: int, y: int, size: int, seed: int) -> None:
    rng = random.Random(seed)
    polygon(draw, [(x - size - 1, y), (x - size, y - 4), (x - 2, y - 2),
                   (x - 2, y - size - 5), (x + 1, y - 4), (x + size - 1, y - size - 3),
                   (x + size - 1, y - 2), (x + size + 3, y - 4), (x + size + 1, y)], P["grass_dark"])
    for off in [-size + 1, -2, 1, size - 1]:
        h = rng.randint(3, size + 3)
        draw.line((x + off, y - 1, x + off - 1, y - h), fill=rng.choice([P["grass_mid"], P["grass_light"], P["grass"]]))


def ground(im: Image.Image) -> None:
    draw = ImageDraw.Draw(im)
    rng = random.Random(SEED + 3)
    # Three readable strata: turf/root ledge, clay/stone wall, shaded foreground.
    polygon(draw, [(0, 277), (96, 277), (140, 280), (244, 278), (281, 277),
                   (429, 277), (484, 280), (565, 278), (640, 277), (640, 360), (0, 360)], P["soil"])
    draw.rectangle((0, 301, W, H), P["soil_dark"])
    draw.rectangle((0, 304, W, 311), P["soil_mid"])
    for _ in range(830):
        x, y = rng.randrange(W), rng.randint(284, 358)
        color = rng.choice([P["soil"], P["soil_mid"], P["soil_light"]]) if y < 322 else rng.choice([P["soil"], P["soil_dark"]])
        draw.rectangle((x, y, x + rng.choice([1, 2, 3, 5]), y + rng.choice([0, 1, 2])), color)
    # Embedded angular stones; staggered, varied, and chipped, never uniform tiles.
    for row, yy in enumerate([291, 311, 334, 354]):
        x = -20 + (row % 2) * 12
        while x < W:
            width, height = rng.randint(11, 32), rng.randint(7, 15)
            stone = [(x + 3, yy - height // 2), (x + width - 6, yy - height // 2 - 1),
                     (x + width, yy - 1), (x + width - 4, yy + height // 2),
                     (x + 3, yy + height // 2 + 1), (x - 1, yy + 2)]
            polygon(draw, [(px, py + 1) for px, py in stone], P["soil_dark"])
            color = P["stone_edge"] if yy > 330 else rng.choice([P["stone"], P["stone_edge"]])
            polygon(draw, stone, color)
            draw.line(stone[:3], fill=P["stone_light"] if yy < 330 else P["stone"], width=1)
            draw.line((x + width // 3, yy - 2, x + width // 3 - 2, yy + 3), fill=P["stone_dark"])
            if width > 24:
                draw.rectangle((x + width - 9, yy + 3, x + width - 7, yy + 3), P["soil_mid"])
            x += width + rng.randint(5, 16)
    # Curving exposed roots hanging off the turf edge.
    for x in [41, 113, 184, 458, 513, 596]:
        draw.line([(x, 282), (x + 3, 291), (x - 3, 301), (x + 1, 306)], fill=P["bark_dark"], width=2)
        draw.line([(x + 1, 283), (x + 4, 291), (x - 2, 299)], fill=P["bark_mid"])
    for x in range(0, W, 3):
        quiet = 277 <= x <= 416
        top = 277 if quiet else 276 + round(2 * math.sin(x / 13))
        depth = rng.randint(6, 9) if quiet else rng.randint(8, 12)
        draw.rectangle((x, top, x + 3, top + depth), P["grass_dark"])
        draw.rectangle((x, top, x + 3, top + 2), P["grass_mid"])
        draw.line((x, top, x + 1, top), fill=P["grass_light"])
        if not quiet:
            draw.line((x + 1, top - rng.randint(1, 4), x + 1, top + 1), fill=P["grass_light"])
            if rng.random() < .2:
                draw.rectangle((x, top + depth - 1, x + 2, top + depth + 2), P["grass"])
    # Central porch/path has a straight 277px foot plane, not scenery obstacles.
    polygon(draw, [(246, 270), (430, 270), (441, 277), (436, 284), (262, 284), (241, 278)], P["stone_dark"])
    polygon(draw, [(248, 270), (428, 270), (437, 276), (433, 279), (259, 279), (246, 276)], P["stone"])
    for x in range(251, 429, 23):
        draw.line((x, 271, x + 3, 277), fill=P["stone_edge"])
        draw.line((x + 4, 270, x + 19, 270), fill=P["stone_light"])
    draw.line((259, 278, 433, 278), fill=P["stone_light"])
    # Sparse path grit stays below the reserved foot plane.
    for _ in range(55):
        xx, yy = rng.randint(256, 431), rng.randint(280, 294)
        draw.rectangle((xx, yy, xx + 1, yy), rng.choice([P["soil_sand"], P["grass_mid"], P["soil_light"]]))


def roof(im: Image.Image, points: list[Point], seed: int, tile_w: int = 12, tile_h: int = 7) -> None:
    draw = ImageDraw.Draw(im)
    rng = random.Random(seed)
    mask = Image.new("1", (W, H), 0)
    ImageDraw.Draw(mask).polygon(points, fill=1)
    layer = Image.new("RGB", (W, H), P["roof"])
    ld = ImageDraw.Draw(layer)
    x0, y0 = min(p[0] for p in points), min(p[1] for p in points)
    x1, y1 = max(p[0] for p in points), max(p[1] for p in points)
    for row, y in enumerate(range(y0, y1 + tile_h, tile_h)):
        shift = tile_w // 2 if row % 2 else 0
        for x in range(x0 - tile_w + shift, x1 + tile_w, tile_w):
            color = rng.choice([P["roof"], P["roof"], P["roof_light"], P["roof_shadow"]])
            ld.rectangle((x + 1, y, x + tile_w - 1, y + tile_h - 1), color)
            ld.line((x + 2, y + 1, x + tile_w - 3, y + 1), fill=P["roof_glint"])
            ld.line((x + 2, y + tile_h - 1, x + tile_w - 2, y + tile_h - 1), fill=P["roof_shadow"])
            ld.point((x + tile_w - 1, y + tile_h - 2), fill=P["roof_dark"])
            if rng.random() < .12:
                ld.line((x + 4, y + 3, x + 3, y + 5), fill=P["roof_shadow"])
            if rng.random() < .05:
                ld.rectangle((x + 4, y + 3, x + 7, y + 4), P["roof_moss"])
    im.paste(layer, (0, 0), mask)
    draw.line(points + [points[0]], fill=P["roof_dark"], width=2)


def window(draw: ImageDraw.ImageDraw, x: int, y: int, width: int = 30, height: int = 29) -> None:
    draw.rectangle((x - 3, y - 3, x + width + 3, y + height + 3), P["wood_dark"])
    draw.rectangle((x - 2, y - 2, x + width + 2, y + height + 2), P["wood_light"])
    draw.rectangle((x, y, x + width, y + height), P["glass_dark"])
    draw.rectangle((x + 2, y + 2, x + width - 2, y + height - 2), P["glass_mid"])
    draw.rectangle((x + 2, y + 2, x + width - 2, y + 7), P["glass_light"])
    draw.rectangle((x + 2, y + height - 7, x + width - 2, y + height - 2), P["glass"])
    # Two stepped glints, a hint of curtain, and visible wood crossbars.
    draw.line((x + 4, y + 3, x + 4, y + 10), fill=P["glass_glint"], width=2)
    draw.line((x + 7, y + 3, x + 7, y + 6), fill=P["glass_glint"])
    polygon(draw, [(x + width - 8, y + 2), (x + width - 2, y + 2),
                   (x + width - 2, y + 19), (x + width - 6, y + 14)], P["glass_light"])
    draw.rectangle((x + width // 2 - 1, y, x + width // 2 + 1, y + height), P["wood_edge"])
    draw.rectangle((x, y + height // 2, x + width, y + height // 2 + 1), P["wood_edge"])
    draw.line((x + width // 2 - 1, y + 1, x + width // 2 - 1, y + height - 1), fill=P["wood_sun"])
    draw.rectangle((x - 5, y + height + 2, x + width + 5, y + height + 5), P["wood_edge"])
    draw.line((x - 5, y + height + 2, x + width + 5, y + height + 2), fill=P["wood_sun"])


def planter(draw: ImageDraw.ImageDraw, x: int, y: int, width: int, seed: int) -> None:
    rng = random.Random(seed)
    polygon(draw, [(x - 1, y), (x + width + 1, y), (x + width - 1, y + 8), (x + 1, y + 8)], P["wood_dark"])
    draw.rectangle((x, y + 1, x + width, y + 5), P["wood"])
    draw.line((x + 1, y + 2, x + width - 1, y + 2), fill=P["wood_light"])
    for xx in range(x + 2, x + width, 4):
        draw.line((xx, y - rng.randint(2, 6), xx, y), fill=P["leaf_dark"])
        leaf_cluster(draw, xx - 1, y - 3, P["leaf_mid"], 2)
        yy = y - rng.randint(5, 8)
        color = rng.choice([P["flower_pink"], P["flower_yellow"], P["flower_light"]])
        draw.rectangle((xx, yy - 1, xx + 1, yy + 1), color)
        draw.line((xx - 1, yy, xx + 2, yy), fill=color)
        draw.point((xx, yy), fill=P["glass_glint"])


def cottage(im: Image.Image) -> None:
    draw = ImageDraw.Draw(im)
    rng = random.Random(SEED + 4)
    # Small rear wing, varied material courses, and one warm window.
    draw.rectangle((417, 202, 474, 272), P["wood_dark"])
    draw.rectangle((420, 208, 470, 268), P["wood"])
    for yy in range(210, 267, 8):
        draw.line((421, yy, 469, yy), fill=P["wood_edge"])
        draw.line((421, yy + 1, 469, yy + 1), fill=P["wood_light"])
    draw.rectangle((419, 263, 471, 269), P["stone_edge"])
    for x in range(422, 470, 11):
        draw.line((x, 264, x + 1, 268), fill=P["stone_dark"])
    window(draw, 438, 226, 21, 25)
    roof(im, [(414, 178), (464, 184), (483, 209), (412, 209)], SEED + 41, 10, 6)
    draw.line((414, 210, 483, 210), fill=P["wood_dark"], width=3)
    draw.line((416, 208, 481, 208), fill=P["wood_sun"])
    # Chimney behind the primary roof, uneven sandstone courses and moss cap.
    draw.rectangle((394, 113, 413, 161), P["stone_dark"])
    draw.rectangle((397, 115, 411, 158), P["stone"])
    for yy in range(118, 155, 6):
        draw.line((397, yy, 411, yy), fill=P["stone_edge"])
        draw.line((402 if yy % 12 else 408, yy, 402 if yy % 12 else 408, yy + 5), fill=P["stone_edge"])
    draw.rectangle((391, 110, 415, 115), P["stone_dark"])
    draw.line((392, 110, 414, 110), fill=P["stone_light"])
    draw.line((393, 108, 402, 108), fill=P["leaf_mid"], width=2)
    # A few discrete chimney puffs, not a gradient/blur.
    for xx, yy, rr in [(404, 102, 6), (408, 89, 7), (416, 77, 8)]:
        pts = organic_points(xx, yy, rr, max(3, rr - 2), rng, 5)
        polygon(draw, pts, P["cloud_edge"])
        draw.line((xx - 2, yy - 2, xx + 2, yy - 2), fill=P["cloud"])
    # Main lodge sits at 55% horizontal. Lower wall stays quiet for two overlays.
    main = [(241, 188), (347, 138), (443, 188), (443, 272), (241, 272)]
    polygon(draw, main, P["wood_dark"])
    polygon(draw, [(246, 191), (347, 145), (438, 191), (438, 269), (246, 269)], P["plaster"])
    polygon(draw, [(350, 160), (438, 198), (438, 269), (414, 269), (412, 192)], P["plaster_dark"])
    # Fine plaster chips only: no speckle covering the clear central area.
    for _ in range(140):
        x, y = rng.randint(248, 435), rng.randint(195, 267)
        if 303 < x < 387 and y > 223:
            continue
        if im.getpixel((x, y)) in (rgb(P["plaster"]), rgb(P["plaster_dark"])):
            draw.rectangle((x, y, x + 1, y), rng.choice([P["plaster_light"], P["plaster_dark"]]))
    # Timber beams, sunlit seams, wooden siding at the base.
    for xx in [246, 338, 432]:
        draw.rectangle((xx, 194, xx + 5, 270), P["wood_edge"])
        draw.line((xx + 1, 197, xx + 1, 269), fill=P["wood_light"])
    draw.rectangle((251, 201, 437, 205), P["wood_edge"])
    draw.line((252, 202, 431, 202), fill=P["wood_light"])
    draw.rectangle((251, 257, 432, 269), P["wood"])
    draw.line((251, 259, 432, 259), fill=P["wood_edge"])
    draw.line((251, 262, 432, 262), fill=P["wood_light"])
    draw.line((251, 266, 432, 266), fill=P["wood_edge"])
    for xx in range(257, 433, 14):
        draw.line((xx, 260, xx, 269), fill=P["wood_edge"])
        draw.point((xx + 3, 264), fill=P["wood_dark"])
    # Two windows with shutters: warm, legible architecture, no signage/text.
    window(draw, 269, 217, 29, 29)
    window(draw, 397, 216, 25, 28)
    for sx, yy in [(261, 218), (301, 218), (389, 217), (425, 217)]:
        draw.rectangle((sx, yy, sx + 5, yy + 27), P["wood_edge"])
        draw.line((sx + 1, yy + 1, sx + 1, yy + 25), fill=P["wood_light"])
        for sy in range(yy + 4, yy + 25, 5):
            draw.line((sx + 1, sy, sx + 4, sy), fill=P["wood_dark"])
    planter(draw, 270, 251, 28, SEED + 42)
    planter(draw, 396, 249, 27, SEED + 43)
    # Arched central door; empty porch in front is reserved for Deni/workstation.
    draw.rectangle((352, 218, 381, 269), P["wood_dark"])
    draw.pieslice((352, 204, 381, 232), 180, 360, fill=P["wood_dark"])
    draw.rectangle((355, 219, 378, 268), P["wood_edge"])
    draw.pieslice((355, 207, 378, 230), 180, 360, fill=P["wood_edge"])
    draw.rectangle((357, 221, 376, 267), P["wood"])
    for xx in [361, 367, 373]:
        draw.line((xx, 224, xx, 267), fill=P["wood_edge"])
        draw.line((xx + 1, 225, xx + 1, 266), fill=P["wood_light"])
    draw.ellipse((359, 211, 374, 226), fill=P["glass_dark"])
    draw.ellipse((361, 212, 372, 223), fill=P["glass_mid"])
    draw.line((366, 212, 366, 223), fill=P["wood_edge"])
    draw.line((361, 217, 372, 217), fill=P["wood_edge"])
    draw.rectangle((374, 247, 375, 249), P["glass_light"])
    # Main roof is a deep tiled silhouette, not a plain triangle.
    roof(im, [(221, 195), (255, 167), (341, 123), (354, 123), (452, 176), (463, 197)], SEED + 44)
    # Stepped pale bargeboards outline the gable and a layered eave.
    draw.line([(224, 195), (255, 171), (343, 128), (352, 128), (448, 179), (460, 196)], fill=P["wood_edge"], width=5)
    draw.line([(225, 192), (256, 168), (343, 125), (352, 125), (448, 176), (460, 193)], fill=P["wood_sun"], width=2)
    draw.rectangle((228, 195, 459, 199), P["wood_dark"])
    draw.line((230, 195, 457, 195), fill=P["wood_light"])
    for xx in range(237, 458, 19):
        draw.rectangle((xx, 199, xx + 3, 202), P["wood_edge"])
    # Pegged diagonal braces make the front elevation read as timber joinery.
    for xx, direction in [(251, 1), (432, -1)]:
        draw.line((xx, 220, xx + 15 * direction, 205), fill=P["wood_edge"], width=3)
        draw.line((xx + direction, 218, xx + 14 * direction, 205), fill=P["wood_light"])
        draw.point((xx + 3 * direction, 216), fill=P["wood_dark"])
    # Eave ivy stays at the cool outer edge, outside the clear interaction stage.
    for yy in range(202, 235, 5):
        xx = 438 + round(2 * math.sin(yy / 7))
        draw.line((xx, yy, xx, yy + 4), fill=P["leaf_dark"])
        leaf_cluster(draw, xx - 2, yy, P["leaf_mid"], 2)
        leaf_cluster(draw, xx + 1, yy + 3, P["leaf_light"], 1)
    # Round dormer: tiny upper studio loft window, framed in timber.
    draw.ellipse((328, 151, 365, 184), fill=P["roof_dark"])
    draw.ellipse((330, 152, 363, 181), fill=P["wood_light"])
    draw.ellipse((334, 155, 359, 178), fill=P["wood_dark"])
    draw.ellipse((336, 157, 357, 176), fill=P["glass_mid"])
    draw.pieslice((336, 157, 357, 176), 180, 360, fill=P["glass_light"])
    draw.line((347, 157, 347, 176), fill=P["wood_edge"], width=2)
    draw.line((337, 166, 357, 166), fill=P["wood_edge"], width=2)
    draw.rectangle((332, 181, 362, 184), P["wood_edge"])
    draw.line((332, 181, 362, 181), fill=P["wood_sun"])
    # Roof moss, growing under the ridge and at the cool right eave.
    for xx, yy in [(275, 159), (281, 157), (287, 155), (412, 181), (418, 184), (424, 187)]:
        leaf_cluster(draw, xx, yy, P["roof_moss"], 3)
        draw.point((xx + 1, yy - 1), fill=P["leaf_light"])
    # One fixed lantern at the cottage entrance, not an application state.
    draw.line([(385, 213), (390, 213), (390, 218)], fill=P["metal"], width=2)
    draw.rectangle((386, 218, 394, 229), P["metal_dark"])
    draw.rectangle((388, 220, 392, 226), P["glass_light"])
    draw.line((390, 220, 390, 226), fill=P["glass_mid"])
    draw.line((386, 218, 394, 218), fill=P["metal_light"])


def lamp(draw: ImageDraw.ImageDraw, x: int, foot: int = 277) -> None:
    draw.rectangle((x - 4, foot - 2, x + 4, foot + 1), P["stone_dark"])
    draw.rectangle((x - 2, foot - 44, x + 1, foot - 3), P["metal_dark"])
    draw.line((x - 1, foot - 43, x - 1, foot - 5), fill=P["metal_light"])
    draw.rectangle((x - 4, foot - 45, x + 3, foot - 43), P["metal"])
    draw.line([(x - 1, foot - 45), (x - 1, foot - 57), (x + 3, foot - 60), (x + 12, foot - 60), (x + 15, foot - 56)], fill=P["metal_dark"], width=2)
    polygon(draw, [(x + 8, foot - 55), (x + 11, foot - 59), (x + 19, foot - 59), (x + 22, foot - 55)], P["metal"])
    draw.rectangle((x + 9, foot - 55, x + 21, foot - 42), P["metal_dark"])
    draw.rectangle((x + 11, foot - 53, x + 19, foot - 45), P["glass_mid"])
    draw.rectangle((x + 12, foot - 53, x + 16, foot - 47), P["glass_light"])
    draw.line((x + 15, foot - 53, x + 15, foot - 45), fill=P["wood"])
    draw.line((x + 8, foot - 42, x + 22, foot - 42), fill=P["metal_light"])
    draw.rectangle((x + 10, foot - 41, x + 20, foot - 40), P["metal_dark"])
    draw.point((x + 15, foot - 61), fill=P["metal_light"])


def mushroom(draw: ImageDraw.ImageDraw, x: int, y: int, small: bool = False) -> None:
    r = 3 if small else 5
    draw.rectangle((x - 1, y - 4, x + 1, y), P["plaster_dark"])
    draw.line((x - 1, y - 3, x - 1, y), fill=P["plaster_light"])
    polygon(draw, [(x - r - 1, y - 4), (x - r + 1, y - 7), (x - 1, y - 9),
                   (x + 2, y - 9), (x + r, y - 6), (x + r + 1, y - 4)], P["terracotta_dark"])
    polygon(draw, [(x - r, y - 5), (x - r + 2, y - 7), (x - 1, y - 8),
                   (x + 2, y - 8), (x + r, y - 5)], P["flower_pink"])
    draw.rectangle((x - 2, y - 7, x - 1, y - 6), P["flower_light"])
    draw.point((x + 3, y - 5), fill=P["plaster_light"])


def fern(draw: ImageDraw.ImageDraw, x: int, y: int, height: int, seed: int) -> None:
    rng = random.Random(seed)
    for off in [-7, -3, 1, 6]:
        end = x + off * 2
        top = y - height + rng.randint(0, 5)
        draw.line([(x, y), (x + off, y - height // 2), (end, top)], fill=P["leaf_dark"])
        for i in range(2, 8):
            t = i / 9
            xx = round(x + (end - x) * t)
            yy = round(y + (top - y) * t)
            ln = max(1, round(4 * (1 - t)))
            draw.line((xx - ln, yy - 1, xx + ln, yy + 1), fill=P["leaf_mid"])
            draw.point((xx - ln, yy - 1), fill=P["leaf_light"])


def flora(im: Image.Image) -> None:
    draw = ImageDraw.Draw(im)
    rng = random.Random(SEED + 5)
    # Wildflower tufts keep the central interaction silhouette uncluttered.
    for x in [12, 32, 57, 101, 145, 188, 208, 224, 467, 492, 515, 544, 584, 615, 637]:
        y = rng.randint(274, 279)
        grass_tuft(draw, x, y, rng.randint(4, 7), x + SEED)
        for _ in range(rng.randint(1, 3)):
            xx, hh = x + rng.randint(-6, 6), rng.randint(6, 11)
            draw.line((xx, y, xx - 1, y - hh), fill=P["leaf_shadow"])
            draw.line((xx - 1, y - 3, xx - 4, y - 5), fill=P["leaf_mid"])
            color = rng.choice([P["flower_pink"], P["flower_blue"], P["flower_yellow"]])
            draw.line((xx - 2, y - hh, xx + 1, y - hh), fill=color)
            draw.line((xx - 1, y - hh - 1, xx - 1, y - hh + 1), fill=color)
            draw.point((xx - 1, y - hh), fill=P["glass_glint"])
    for x, y, small in [(78, 277, False), (86, 278, True), (177, 278, True),
                         (527, 279, False), (534, 279, True), (607, 277, True)]:
        mushroom(draw, x, y, small)
    fern(draw, 120, 277, 20, 101)
    fern(draw, 575, 277, 22, 102)
    fern(draw, 23, 277, 18, 103)
    # Cottage-edge potted herb, to the right, outside the reserved center.
    polygon(draw, [(448, 266), (462, 266), (460, 276), (450, 276)], P["terracotta_dark"])
    draw.rectangle((449, 266, 461, 269), P["terracotta"])
    draw.line((450, 268, 460, 268), fill=P["roof_glint"])
    draw.line((455, 251, 455, 265), fill=P["leaf_dark"])
    for yy in [253, 257, 261]:
        leaf_cluster(draw, 450, yy, P["leaf_mid"], 3)
        leaf_cluster(draw, 456, yy - 1, P["leaf_light"], 2)
    # Periphery rocks with pale lichens, grass in cracks.
    for x, y, wide in [(149, 276, 20), (201, 279, 11), (501, 277, 18), (624, 277, 16)]:
        polygon(draw, [(x - wide // 2, y), (x - wide // 2 + 2, y - 5), (x - 2, y - 9),
                       (x + wide // 2 - 3, y - 7), (x + wide // 2, y - 1)], P["stone_dark"])
        polygon(draw, [(x - wide // 2 + 2, y - 1), (x - wide // 2 + 3, y - 5),
                       (x - 2, y - 8), (x + wide // 2 - 3, y - 6), (x + wide // 2 - 1, y - 1)], P["stone"])
        draw.line((x - wide // 2 + 3, y - 5, x - 2, y - 8), fill=P["stone_light"])
        draw.rectangle((x + 1, y - 5, x + 3, y - 4), P["leaf_olive"])
    # Fallen little branch away from the character silhouettes.
    draw.line([(157, 278), (163, 273), (175, 274)], fill=P["bark_dark"], width=3)
    draw.line([(158, 277), (163, 272), (174, 273)], fill=P["bark_light"])
    draw.line((166, 273, 170, 269), fill=P["bark"])
    # Discrete falling leaf accents are scenery, not animation or state.
    for xx, yy in [(185, 128), (208, 187), (488, 161), (515, 214), (132, 213)]:
        leaf_cluster(draw, xx, yy, P["leaf_light"], 2)
        draw.point((xx + 2, yy + 2), fill=P["leaf_shadow"])


def world() -> Image.Image:
    im = Image.new("RGB", (W, H), P["sky"])
    draw = ImageDraw.Draw(im)
    hills(im, draw)
    # Farther smaller trees establish depth before the two framing hero trees.
    tree_trunk(draw, 179, 159, 278, 12, 211)
    for args in [(157, 168, 31, 24, 2101), (188, 148, 34, 31, 2102),
                 (205, 174, 30, 24, 2103)]:
        crown(im, *args, shadowed=True)
    tree_trunk(draw, 504, 147, 279, 14, 212)
    for args in [(483, 166, 32, 25, 2201), (514, 142, 37, 31, 2202),
                 (534, 174, 30, 23, 2203)]:
        crown(im, *args, shadowed=True)
    ground(im)
    cottage(im)
    # Big asymmetric trunks and overlapping lobes frame, never obscure, HQ.
    tree_trunk(draw, 74, 101, 278, 29, 301)
    for args in [(17, 132, 53, 34, 3101), (51, 113, 52, 40, 3102),
                 (113, 121, 47, 35, 3103), (30, 82, 52, 41, 3104),
                 (83, 67, 59, 43, 3105), (126, 85, 41, 36, 3106),
                 (50, 44, 41, 31, 3107), (89, 41, 44, 30, 3108)]:
        crown(im, *args)
    tree_trunk(draw, 584, 110, 279, 30, 302)
    for args in [(538, 139, 40, 32, 3201), (589, 134, 52, 39, 3202),
                 (632, 119, 47, 38, 3203), (536, 98, 43, 35, 3204),
                 (577, 74, 53, 39, 3205), (623, 67, 51, 37, 3206),
                 (571, 47, 37, 30, 3207), (619, 39, 44, 32, 3208)]:
        crown(im, *args)
    # Two small bushes at frame edges are layered over, not on, the porch.
    for args in [(5, 259, 37, 20, 3301), (103, 265, 20, 12, 3302),
                 (552, 263, 21, 14, 3303), (638, 255, 34, 25, 3304)]:
        crown(im, *args, shadowed=True)
    lamp(draw, 215)
    lamp(draw, 479)
    flora(im)
    return im


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


def engineer() -> Image.Image:
    """Front-facing relaxed idle engineer, original big head and blue overshirt."""
    im = Image.new("RGBA", (40, 56), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    # The pose is grounded: no tools, workstation, effects, or work animation.
    d.rectangle((12, 43, 19, 51), S["outline"])
    d.rectangle((21, 43, 28, 51), S["outline"])
    d.rectangle((13, 44, 18, 50), S["trouser"])
    d.rectangle((22, 44, 27, 50), S["trouser"])
    d.line((13, 44, 13, 49), fill=S["trouser_light"])
    d.line((22, 45, 22, 49), fill=S["trouser_light"])
    polygon(d, [(11, 50), (18, 50), (19, 52), (19, 54), (10, 54), (10, 52)], S["shoe"])
    polygon(d, [(22, 50), (28, 50), (30, 52), (30, 54), (21, 54), (21, 52)], S["shoe"])
    d.line((11, 54, 18, 54), fill=S["sole"])
    d.line((22, 54, 29, 54), fill=S["sole"])
    d.rectangle((15, 29, 24, 34), S["skin_shadow"])
    d.rectangle((16, 29, 23, 32), S["skin"])
    polygon(d, [(11, 32), (15, 31), (17, 33), (22, 33), (25, 31), (29, 33),
                (31, 43), (28, 46), (12, 46), (9, 43)], S["outline"])
    polygon(d, [(12, 33), (15, 32), (18, 35), (22, 35), (25, 32), (28, 34),
                (29, 43), (27, 45), (13, 45), (11, 42)], S["jacket"])
    d.rectangle((17, 34, 22, 43), S["shirt"])
    d.rectangle((21, 37, 22, 43), S["shirt_shadow"])
    polygon(d, [(14, 32), (17, 34), (17, 38), (13, 35)], S["jacket_light"])
    polygon(d, [(24, 32), (23, 38), (26, 35)], S["jacket_mid"])
    d.line((14, 36, 14, 43), fill=S["jacket_mid"])
    d.line((24, 37, 24, 44), fill=S["jacket_dark"])
    d.rectangle((11, 35, 13, 41), S["jacket_mid"])
    d.rectangle((27, 35, 29, 41), S["jacket_dark"])
    d.rectangle((9, 41, 12, 45), S["outline"])
    d.rectangle((10, 42, 12, 44), S["skin"])
    d.point((10, 42), fill=S["skin_light"])
    d.rectangle((28, 41, 31, 45), S["outline"])
    d.rectangle((29, 42, 30, 44), S["skin"])
    d.rectangle((25, 38, 27, 40), S["jacket_dark"])
    d.line((25, 38, 27, 38), fill=S["jacket_light"])
    d.point((15, 40), fill=S["badge"])
    # Broad head: a 28-pixel organic stepped silhouette, not an emoji circle.
    head = [(12, 4), (27, 4), (27, 5), (31, 5), (31, 7), (33, 7), (33, 12),
            (35, 12), (35, 23), (33, 23), (33, 27), (30, 27), (30, 30),
            (26, 30), (26, 32), (14, 32), (14, 30), (10, 30), (10, 27),
            (7, 27), (7, 23), (5, 23), (5, 13), (7, 13), (7, 8), (10, 8), (10, 5), (12, 5)]
    polygon(d, head, S["outline"])
    polygon(d, [(9, 14), (30, 13), (32, 17), (32, 26), (28, 29), (24, 31),
                (15, 31), (10, 28), (8, 24), (8, 18)], S["skin_shadow"])
    polygon(d, [(10, 14), (29, 14), (31, 18), (31, 25), (28, 28), (24, 30),
                (15, 30), (11, 27), (9, 23), (9, 18)], S["skin"])
    polygon(d, [(11, 16), (27, 15), (29, 18), (28, 24), (22, 28), (14, 27), (11, 23)], S["skin_light"])
    d.rectangle((6, 21, 8, 24), S["skin"])
    d.rectangle((32, 21, 34, 24), S["skin"])
    d.point((7, 22), fill=S["skin_shadow"])
    d.point((33, 22), fill=S["skin_shadow"])
    # Two expressive black eyes with white catches, not copied sprite geometry.
    d.rectangle((12, 20, 15, 24), S["eye"])
    d.rectangle((24, 20, 27, 24), S["eye"])
    d.rectangle((13, 20, 14, 21), S["eye_white"])
    d.rectangle((25, 20, 26, 21), S["eye_white"])
    d.line((12, 19, 15, 19), fill=S["hair"])
    d.line((24, 19, 27, 19), fill=S["hair"])
    d.rectangle((10, 25, 12, 25), S["cheek"])
    d.rectangle((28, 25, 30, 25), S["cheek"])
    d.point((20, 25), fill=S["skin_shadow"])
    d.line((18, 28, 21, 28), fill=S["skin_shadow"])
    # Asymmetric dark hair with separated clumps and two narrow blue glints.
    hair = [(8, 13), (9, 9), (12, 9), (12, 6), (17, 6), (17, 4), (24, 4),
            (24, 5), (29, 5), (29, 7), (32, 7), (33, 13), (32, 18), (30, 21),
            (29, 16), (26, 16), (26, 13), (23, 16), (19, 17), (20, 12),
            (16, 17), (13, 18), (14, 13), (11, 16), (9, 22), (7, 19)]
    polygon(d, hair, S["hair_dark"])
    polygon(d, [(10, 10), (14, 7), (20, 6), (26, 7), (29, 10), (24, 10),
                (21, 13), (21, 9), (16, 14), (16, 10), (12, 14)], S["hair"])
    d.line((13, 8, 17, 7), fill=S["hair_mid"])
    d.line((19, 7, 24, 7), fill=S["hair_mid"])
    d.line((25, 8, 28, 9), fill=S["hair_light"])
    d.line((8, 16, 8, 19), fill=S["hair"])
    return im


def owner() -> Image.Image:
    """Three-quarter left idle owner in the same 40x56 chibi scale/palette."""
    im = Image.new("RGBA", (40, 56), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    d.rectangle((13, 43, 20, 51), S["outline"])
    d.rectangle((22, 43, 28, 51), S["outline"])
    d.rectangle((14, 44, 19, 50), S["trouser"])
    d.rectangle((23, 44, 27, 50), S["trouser"])
    d.line((14, 45, 14, 50), fill=S["trouser_light"])
    polygon(d, [(12, 50), (19, 50), (20, 52), (20, 54), (10, 54), (10, 52)], S["shoe"])
    polygon(d, [(22, 50), (28, 50), (29, 52), (29, 54), (21, 54), (21, 52)], S["shoe"])
    d.line((11, 54, 19, 54), fill=S["sole"])
    d.line((22, 54, 28, 54), fill=S["sole"])
    d.rectangle((16, 29, 24, 34), S["skin_shadow"])
    d.rectangle((17, 30, 22, 32), S["skin"])
    polygon(d, [(12, 32), (16, 31), (20, 33), (24, 31), (28, 33), (30, 42),
                (28, 46), (12, 46), (10, 41)], S["outline"])
    polygon(d, [(13, 33), (16, 32), (19, 35), (23, 33), (27, 34), (29, 42),
                (27, 45), (13, 45), (11, 41)], S["jacket_mid"])
    d.rectangle((15, 35, 19, 44), S["shirt"])
    d.rectangle((18, 36, 19, 44), S["shirt_shadow"])
    polygon(d, [(15, 32), (18, 35), (17, 39), (12, 35)], S["jacket_light"])
    polygon(d, [(22, 33), (23, 38), (25, 35)], S["jacket_light"])
    d.rectangle((23, 36, 28, 42), S["jacket"])
    d.line((26, 36, 26, 43), fill=S["jacket_light"])
    d.line((21, 39, 21, 44), fill=S["jacket_dark"])
    d.rectangle((11, 40, 14, 44), S["outline"])
    d.rectangle((12, 41, 13, 43), S["skin"])
    d.rectangle((26, 41, 30, 46), S["outline"])
    d.rectangle((27, 42, 29, 45), S["skin"])
    d.point((27, 42), fill=S["skin_light"])
    d.rectangle((21, 40, 24, 42), S["jacket_dark"])
    d.line((21, 40, 24, 40), fill=S["jacket_light"])
    d.point((13, 38), fill=S["badge"])
    head = [(13, 4), (27, 4), (27, 6), (31, 6), (31, 8), (33, 8), (33, 12),
            (34, 12), (34, 24), (32, 24), (32, 28), (28, 28), (28, 30),
            (24, 30), (24, 32), (14, 32), (14, 30), (10, 30), (10, 27),
            (7, 27), (7, 24), (5, 24), (5, 20), (7, 18), (7, 12), (9, 12), (9, 8), (13, 8)]
    polygon(d, head, S["outline"])
    polygon(d, [(10, 15), (28, 13), (31, 18), (31, 26), (27, 29), (23, 31),
                (14, 31), (10, 28), (8, 24), (6, 23), (8, 20)], S["skin_shadow"])
    polygon(d, [(11, 15), (26, 14), (29, 18), (29, 25), (26, 28), (22, 30),
                (14, 30), (10, 27), (8, 23), (7, 22), (9, 20)], S["skin"])
    polygon(d, [(11, 17), (23, 16), (25, 19), (24, 26), (20, 28), (13, 27), (10, 23)], S["skin_light"])
    d.rectangle((29, 21, 32, 24), S["skin"])
    d.point((31, 22), fill=S["skin_shadow"])
    d.rectangle((10, 20, 12, 23), S["eye"])
    d.rectangle((21, 20, 24, 24), S["eye"])
    d.point((11, 20), fill=S["eye_white"])
    d.rectangle((22, 20, 23, 21), S["eye_white"])
    # Small rounded-square glasses differentiate the owner without a logo.
    d.rectangle((8, 19, 15, 25), outline=S["glass"])
    d.rectangle((19, 19, 27, 25), outline=S["glass"])
    d.line((15, 21, 19, 21), fill=S["glass"])
    d.line((27, 20, 30, 19), fill=S["glass"])
    d.point((9, 19), fill=S["jacket_light"])
    d.point((20, 19), fill=S["jacket_light"])
    d.point((7, 24), fill=S["skin_glint"])
    d.rectangle((10, 26, 12, 26), S["cheek"])
    d.rectangle((25, 26, 27, 26), S["cheek"])
    d.line((14, 28, 17, 28), fill=S["skin_shadow"])
    hair = [(9, 13), (10, 9), (14, 9), (14, 6), (20, 5), (20, 4), (27, 5),
            (31, 8), (33, 12), (33, 21), (31, 24), (29, 20), (28, 15),
            (23, 15), (23, 12), (19, 15), (15, 16), (16, 12), (11, 18), (8, 19)]
    polygon(d, hair, S["hair_dark"])
    polygon(d, [(12, 10), (16, 7), (23, 6), (28, 8), (29, 11), (25, 10),
                (20, 13), (20, 9), (16, 13), (16, 10), (11, 15)], S["hair"])
    d.line((17, 7, 22, 7), fill=S["hair_mid"])
    d.line((24, 7, 28, 9), fill=S["hair_light"])
    d.line((31, 13, 31, 18), fill=S["hair_mid"])
    return im


def save(name: str, image: Image.Image, description: str) -> dict[str, object]:
    OUT.mkdir(parents=True, exist_ok=True)
    path = OUT / name
    metadata = PngImagePlugin.PngInfo()
    metadata.add_text("Title", name.removesuffix(".png"))
    metadata.add_text("Author", "Original DiOffice art, authored with Hermes")
    metadata.add_text("Description", description)
    metadata.add_text("Source", "Original deterministic Pillow drawing; no external images, fonts or sprites")
    metadata.add_text("Generator", "tools/art/generate-studio-art.py; seed=61427")
    metadata.add_text("Rendering", "Logical pixels; nearest-neighbor integer display scale; no antialiasing")
    image.save(path, format="PNG", optimize=True, pnginfo=metadata)
    # Reopen the exact delivered artifact and check integrity, dimensions and alpha.
    with Image.open(path) as reopened:
        reopened.verify()
    with Image.open(path) as reopened:
        assert reopened.size == image.size and reopened.mode == image.mode
        assert reopened.tobytes() == image.tobytes()
        if reopened.mode == "RGBA":
            assert set(reopened.getchannel("A").tobytes()) == {0, 255}
            assert reopened.getbbox() is not None
        return {
            "path": str(path), "logical_size": list(reopened.size), "mode": reopened.mode,
            "bytes": path.stat().st_size, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            "unique_colors": len(reopened.getcolors(maxcolors=W * H) or []),
            "anchor": [20, 55] if reopened.mode == "RGBA" else None,
            "verified": True,
        }


def main() -> None:
    images = [
        ("studio-world.png", world(), "640x360 original woodland studio side view; HQ x~55%; clear porch x282..405; foot plane y277; no UI/text/NPCs"),
        ("studio-engineer-idle.png", engineer(), "40x56 original front-facing dark-haired chibi engineer in blue jacket; one static idle frame; foot anchor 20,55"),
        ("studio-owner-idle.png", owner(), "40x56 original three-quarter-left dark-haired chibi owner in blue jacket and glasses; one static idle frame; foot anchor 20,55"),
    ]
    records = [save(name, im, description) for name, im, description in images]
    assert len(records) == 3
    print(json.dumps({"asset_count": len(records), "assets": records}, indent=2))


if __name__ == "__main__":
    main()
