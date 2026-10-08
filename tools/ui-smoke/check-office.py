"""Read-only game-first DiOffice QA; never saves a draft, starts runtime, or logs out.

Successful API responses always come from the real server. Only GET failure cases
inject browser-only 503s. Ordinary local development-session bootstrap is left to
the app/fixture; the smoke itself only calls GET APIs.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import traceback
from pathlib import Path
from typing import TYPE_CHECKING
from urllib.parse import quote, urlparse

if TYPE_CHECKING:
    from playwright.sync_api import Locator, Page, Route

ROOT = Path(__file__).resolve().parents[2]
ART = ROOT / "apps" / "web" / "public" / "art" / "2dpig-office.png"
TIMEOUT = 20000
VIEWPORTS = ((1440, 1000), (768, 1024), (390, 844), (320, 740))
# Original check identifiers/count stay compatible with the previous report.
EXPECTED_CHECKS = (
    ("real-session-and-employee-state", None),
    ("bundled-CC0-pixel-art", None),
    ("native-dialog-focus-escape-keyboard-tabs-disabled-runtime", None),
    ("private-reference-image-local-preview-remove", None),
    ("API-task-board-filter-read-only-detail", None),
    ("real-team-and-compose-navigation", None),
    *(("responsive-no-horizontal-overflow", width) for width, _ in VIEWPORTS),
    ("API-error-notice-and-no-stale-task-cards", None),
    ("directory-error-not-an-empty-office-and-retry-recovery", None),
    ("no-JavaScript-page-errors", None),
)
ORIGINAL_IMAGES = (
    ('.studio-world-area[data-area="main-office"] .world-art', "/art/studio-world.png", 640, 360),
    ('.studio-world-area[data-area="garden"] .world-art', "/art/studio-world-garden.png", 640, 360),
    ('.studio-world-area[data-area="workshop"] .world-art', "/art/studio-world-workshop.png", 640, 360),
    (".scene-employee .world-character", "/art/studio-engineer-idle.png", 40, 56),
    (".hud-portrait", "/art/studio-owner-idle.png", 40, 56),
    (".world-owner-state-source", "/art/studio-owner-states.png", 160, 168),
    (".world-owner-walk-source", "/art/studio-owner-walk.png", 160, 56),
)


def safe_error_description(error: Exception) -> str:
    # Playwright transport errors may embed Cookie/Authorization headers.
    # Layout assertion details are ours; other failures retain only the type.
    if not isinstance(error, AssertionError):
        return type(error).__name__
    description = str(error)
    if description:
        return description
    frames = traceback.extract_tb(error.__traceback__) if error.__traceback__ else []
    if frames:
        source = frames[-1]
        return f"AssertionError at {Path(source.filename).name}:{source.lineno}"
    return "AssertionError"


def verify_studio_ui(page: Page, base_url: str, output_dir: str | Path) -> list[dict]:
    """Verify 13 journeys using an isolated, caller-owned sync Playwright page.

    Write screenshots/JSON and return results; raise on failure (also saving a
    partial report). Never launch/close the page, context, or browser. Navigate
    and temporarily resize the page, restoring its viewport and removing only
    our own routes/listener in finally. Require a seeded project and employee.
    Browser-only GET faults do not mutate or test server/database availability.
    """
    from playwright.sync_api import expect

    base = base_url.rstrip("/")
    parsed = urlparse(base)
    assert parsed.scheme in ("http", "https") and parsed.netloc, "Pass the fixture's webOrigin"
    assert not parsed.username and not parsed.password, "No credentials in base_url"
    out = Path(output_dir)
    out.mkdir(parents=True, exist_ok=True)
    assert ART.is_file(), f"Missing bundled reference image: {ART}"
    original_viewport = page.viewport_size
    results: list[dict] = []
    errors: list[str] = []
    blocked_writes: list[dict] = []
    screenshots: list[str] = []
    current: dict = {}
    tasks_fault, directory_fault = "**/api/v1/projects/*/tasks", "**/api/v1/employees"
    fault_hits = {"tasks": 0, "employees": 0}
    nav = page.get_by_role("navigation", name="Workspace navigation")

    def begin(name: str, **metadata) -> None:
        nonlocal current
        current = {"check": name, **metadata}

    def passed(**metadata) -> None:
        results.append({**current, "passed": True, **metadata})

    def report() -> None:
        (out / "dioffice-ui-qa.json").write_text(json.dumps(results, indent=2), encoding="utf-8")

    def on_page_error(error) -> None:
        errors.append(str(error))

    def readonly_guard(route: Route) -> None:
        request = route.request
        path = urlparse(request.url).path
        # Do not replace the app's normal local auth bootstrap. Block all other
        # writes, including accidental task submit, runtime calls, and logout.
        if request.method not in ("GET", "HEAD", "OPTIONS") and path != "/api/v1/auth/dev-session":
            blocked_writes.append({"method": request.method, "path": path})
            route.abort("blockedbyclient")
        else:
            route.fallback()

    def unavailable_tasks(route: Route) -> None:
        if route.request.method == "GET":
            fault_hits["tasks"] += 1
            route.fulfill(status=503, content_type="application/json", body='{"error":"service_unavailable"}')
        else:
            readonly_guard(route)

    def unavailable_directory(route: Route) -> None:
        if route.request.method == "GET":
            fault_hits["employees"] += 1
            route.fulfill(status=503, content_type="application/json", body='{"error":"service_unavailable"}')
        else:
            readonly_guard(route)

    def get_json(path: str) -> dict:
        response = page.request.get(base + path, timeout=TIMEOUT)
        try:
            assert response.status == 200, (path, response.status)
            return response.json()
        finally:
            response.dispose()

    def window(content: str) -> Locator:
        # Scope by content: a task detail dialog stacks over the journal, and
        # lower native modals can be inert in the accessibility tree.
        return page.locator("dialog.inspector-dialog").filter(has=page.locator(content))

    def assert_native(dialog: Locator) -> None:
        expect(dialog).to_be_visible(timeout=TIMEOUT)
        # Team -> compose focuses in requestAnimationFrame; cleanup also restores
        # parent focus after a child unmount. Wait for focus instead of racing it.
        handle = dialog.element_handle(timeout=TIMEOUT)
        assert handle is not None
        try:
            page.wait_for_function("""el => el instanceof HTMLDialogElement && el.open
                && el.matches(':modal') && el.contains(document.activeElement)""",
                arg=handle, timeout=TIMEOUT)
        finally:
            handle.dispose()

    def open_pane(label: str, content: str) -> Locator:
        expect(page.locator("dialog[open]")).to_have_count(0, timeout=TIMEOUT)
        nav.get_by_role("button", name=label, exact=True).click(timeout=TIMEOUT)
        dialog = window(content)
        assert_native(dialog)
        return dialog

    def escape_window(dialog: Locator) -> None:
        page.keyboard.press("Escape")
        expect(dialog).to_have_count(0, timeout=TIMEOUT)

    def clean_world() -> None:
        expect(page.locator("dialog[open]")).to_have_count(0, timeout=TIMEOUT)
        expect(page.locator(".scene-workstation")).to_be_visible(timeout=TIMEOUT)
        expect(page.locator(".studio-hud")).to_be_visible(timeout=TIMEOUT)
        expect(page.locator(".hud-snapshot span").nth(0)).to_have_text(
            re.compile(r"^\d+ task tersimpan$"), timeout=TIMEOUT)
        page.wait_for_function("""specs => specs.every(([selector]) => {
            const image = document.querySelector(selector);
            return image && image.complete && image.naturalWidth > 0;
        })""", arg=ORIGINAL_IMAGES, timeout=TIMEOUT)
        page.evaluate("document.fonts.ready.then(() => true)")
        page.wait_for_function("""() => {
            const world = document.querySelector('.pixel-stage').getBoundingClientRect();
            const hud = document.querySelector('.studio-hud').getBoundingClientRect();
            return Math.abs(world.height - (innerHeight - Math.ceil(hud.height) - 16)) <= 1;
        }""", timeout=TIMEOUT)

    def screenshot(name: str, *, world: bool = False) -> None:
        if world:
            clean_world()
        page.screenshot(path=str(out / name), full_page=True, timeout=TIMEOUT)
        screenshots.append(name)

    def assert_cards(dialog: Locator, snapshot: list[dict]) -> None:
        expected = [{"title": row["title"], "description": row["description"].strip(),
                     "status": row["status"], "priority": row["priority"]} for row in snapshot]
        page.wait_for_function("""want => {
            const cards = [...document.querySelectorAll('.tasks-panel .task-card')].map(card => ({
                title: card.querySelector('.task-title-button').textContent.trim(),
                description: card.querySelector('.task-description').textContent.trim(),
                status: card.querySelector('.state-badge').dataset.state,
                priority: card.querySelector('.priority-label').textContent.trim()
            }));
            return JSON.stringify(cards) === JSON.stringify(want);
        }""", arg=expected, timeout=TIMEOUT)
        expect(dialog.locator(".task-count")).to_have_text(
            f"{len(snapshot)} {'task' if len(snapshot) == 1 else 'tasks'}", timeout=TIMEOUT)

    def geometry() -> dict:
        return page.evaluate("""() => {
            const rect = selector => {
                const r = document.querySelector(selector).getBoundingClientRect();
                return {x:r.x,y:r.y,width:r.width,height:r.height,right:r.right,bottom:r.bottom};
            };
            return {viewport:{width:innerWidth,height:innerHeight},
                page:{width:document.documentElement.scrollWidth,height:document.documentElement.scrollHeight},
                world:rect('.pixel-stage'),hud:rect('.studio-hud'),workstation:rect('.scene-workstation'),
                computerScreen:rect('.world-computer'),locationSign:rect('.world-location-sign'),
                nameplates:{owner:rect('.owner-nameplate'),computer:rect('.object-nameplate'),
                    employee:rect('.scene-employee .employee-nameplate')},
                ownerText:document.querySelector('.owner-nameplate').textContent,
                ownerOverflow:{scrollWidth:document.querySelector('.owner-nameplate').scrollWidth,
                    clientWidth:document.querySelector('.owner-nameplate').clientWidth},
                worldAreas:document.querySelectorAll('.studio-world-area').length,
                jump:rect('.world-jump')};
        }""")

    def assert_window_geometry(dialog: Locator) -> dict:
        value = dialog.evaluate("""el => {
            const r = el.getBoundingClientRect(), body = el.querySelector('.inspector-body');
            return {title:el.querySelector('.window-label').textContent.trim(),
                x:r.x,y:r.y,right:r.right,bottom:r.bottom,width:r.width,height:r.height,
                bodyWidth:body.clientWidth,bodyScrollWidth:body.scrollWidth,
                pageWidth:document.documentElement.scrollWidth,viewportWidth:innerWidth,viewportHeight:innerHeight};
        }""")
        assert value["pageWidth"] <= value["viewportWidth"], value
        assert value["x"] >= 0 and value["y"] >= 0, value
        assert value["right"] <= value["viewportWidth"] + 1 and value["bottom"] <= value["viewportHeight"] + 1, value
        assert value["bodyScrollWidth"] <= value["bodyWidth"] + 1, value
        return value

    def mobile_chrome() -> list[dict]:
        controls = page.locator(
            ".studio-hud button, .studio-hud input, .studio-map-toolbar select, "
            ".account-actions button, .studio-quest-tracker button, "
            "dialog[open] > .window-bar button, dialog[open] .tasks-panel > .window-bar button, "
            "dialog[open] .workstation-tabs button, dialog[open] .task-filters button, "
            "dialog[open] .text-button"
        ).evaluate_all("""elements => elements.filter(el => {
            const s = getComputedStyle(el);
            return el.getClientRects().length && s.visibility !== 'hidden' && s.display !== 'none';
        }).map(el => {
            const r = el.getBoundingClientRect();
            return {label:el.getAttribute('aria-label') || el.textContent.trim() || el.id || el.tagName,
                width:r.width,height:r.height};
        })""")
        assert controls, "No mobile chrome controls found"
        undersized = [row for row in controls if row["width"] < 44 or row["height"] < 44]
        assert not undersized, {"minimumTouchTarget": 44, "undersizedChrome": undersized}
        return controls

    page.on("pageerror", on_page_error)
    page.route("**/api/v1/**", readonly_guard)
    try:
        begin("real-session-and-employee-state")
        page.set_viewport_size({"width": 1440, "height": 1000})
        # Long-lived SSE prevents networkidle; use DOM/image/snapshot readiness.
        page.goto(base, wait_until="domcontentloaded", timeout=TIMEOUT)
        clean_world()
        user = get_json("/api/v1/auth/session")["user"]
        projects = get_json("/api/v1/projects")["items"]
        employees = get_json("/api/v1/employees")["items"]
        assert projects and employees, "Smoke requires a real project and persisted employee"
        owner_name = user["displayName"]
        owner_plate = page.locator(".owner-nameplate")
        assert owner_name in owner_plate.inner_text(timeout=TIMEOUT)
        assert owner_plate.get_attribute("title", timeout=TIMEOUT) == owner_name
        expect(page.locator(".studio-world-map")).to_have_attribute("data-world-width", "1920", timeout=TIMEOUT)
        assert page.locator(".studio-world-area").count() == 3
        assert "Tahoma" not in page.locator(".map-window-title h1").evaluate("el => getComputedStyle(el).fontFamily")
        project_id = page.locator(".project-picker select").input_value(timeout=TIMEOUT)
        assert project_id in {row["id"] for row in projects}
        composer = open_pane("Beri instruksi", ".create-panel")
        selected_id = composer.locator("select[required]").input_value(timeout=TIMEOUT)
        employee = next(row for row in employees if row["id"] == selected_id)
        expect(composer.get_by_role("button", name="Save draft", exact=True)).to_be_disabled(timeout=TIMEOUT)
        escape_window(composer)
        expect(page.locator(".hud-owner strong")).to_have_text(user["displayName"], timeout=TIMEOUT)
        expect(page.locator(".hud-state-row .state-badge")).to_have_text(employee["status"].replace("_", " "), timeout=TIMEOUT)
        expect(page.locator(".scene-employee .state-badge")).to_have_attribute("data-state", employee["status"], timeout=TIMEOUT)
        assert page.locator(".scene-employee .employee-nameplate").inner_text().startswith(employee["name"])
        expect(page.locator(".hud-snapshot span").nth(1)).to_have_text(f"{len(employees)} employee", timeout=TIMEOUT)
        owner = page.locator(".world-owner")
        page.wait_for_function("""() => {
            const avatar = document.querySelector('.world-owner');
            return avatar.dataset.animation === 'idle'
                && getComputedStyle(avatar.querySelector('.world-owner-state-sprite')).animationName === 'owner-idle-cycle';
        }""", timeout=TIMEOUT)
        idle_frames = []
        for _ in range(7):
            idle_frames.append(owner.locator(".world-owner-state-sprite").evaluate(
                "el => `${getComputedStyle(el).backgroundPositionX}:${getComputedStyle(el).backgroundPositionY}`"))
            page.wait_for_timeout(90)
        assert len(set(idle_frames)) >= 2, {"ownerIdleCycleDidNotAdvanceFrames": idle_frames}
        owner.focus(timeout=TIMEOUT)
        start_position = int(owner.get_attribute("data-position", timeout=TIMEOUT))
        owner.press("ArrowRight", timeout=TIMEOUT)
        page.wait_for_function("""old => {
            const avatar = document.querySelector('.world-owner');
            return Number(avatar.dataset.position) !== old && avatar.dataset.animation === 'walk'
                && getComputedStyle(avatar.querySelector('.world-owner-walk-sprite')).animationName === 'owner-walk-cycle';
        }""", arg=start_position, timeout=TIMEOUT)
        walk_frames = []
        for _ in range(12):
            walk_frames.append(owner.locator(".world-owner-walk-sprite").evaluate(
                """el => {
                    const style = getComputedStyle(el);
                    const value = style.backgroundPositionX;
                    const offset = Number.parseFloat(value);
                    return value.endsWith('%')
                        ? offset * (el.clientWidth - Number.parseFloat(style.backgroundSize)) / 100
                        : offset;
                }"""))
            if owner.get_attribute("data-walking", timeout=TIMEOUT) != "true":
                break
            page.wait_for_timeout(35)
        assert len(set(walk_frames)) >= 2, {"walkCycleDidNotAdvanceFrames": walk_frames}
        page.wait_for_function("""() => document.querySelector('.world-owner').dataset.animation === 'idle'""", timeout=TIMEOUT)
        owner.press("ArrowLeft", timeout=TIMEOUT)
        page.wait_for_function("""old => Number(document.querySelector('.world-owner').dataset.position) < old""",
            arg=start_position + 1, timeout=TIMEOUT)
        page.wait_for_function("""() => {
            const avatar = document.querySelector('.world-owner');
            return avatar.dataset.animation === 'walk' && avatar.dataset.facing === 'left';
        }""", timeout=TIMEOUT)
        assert owner.locator(".world-owner-walk-sprite").evaluate(
            "el => getComputedStyle(el).transform.startsWith('matrix(-1')"), "left-facing walk must mirror the local sprite"
        page.wait_for_function("""() => document.querySelector('.world-owner').dataset.animation === 'idle'""", timeout=TIMEOUT)
        page.get_by_role("button", name="Lompat avatar Owner").click(timeout=TIMEOUT)
        page.wait_for_function("""() => {
            const avatar = document.querySelector('.world-owner');
            return avatar.dataset.animation === 'jump'
                && getComputedStyle(avatar.querySelector('.world-owner-state-sprite')).animationName === 'owner-jump-cycle';
        }""", timeout=TIMEOUT)
        page.wait_for_function("""() => {
            const avatar = document.querySelector('.world-owner');
            return avatar.dataset.animation === 'jump'
                && getComputedStyle(avatar.querySelector('.world-owner-state-sprite')).backgroundPositionX === '-40px';
        }""", timeout=TIMEOUT)
        page.wait_for_function("""() => {
            const avatar = document.querySelector('.world-owner');
            return avatar.dataset.animation === 'fall'
                && getComputedStyle(avatar.querySelector('.world-owner-state-sprite')).animationName === 'owner-fall-cycle';
        }""", timeout=TIMEOUT)
        page.wait_for_function("""() => {
            const avatar = document.querySelector('.world-owner');
            return avatar.dataset.animation === 'fall'
                && getComputedStyle(avatar.querySelector('.world-owner-state-sprite')).backgroundPositionX === '-120px';
        }""", timeout=TIMEOUT)
        page.wait_for_function("""() => {
            const avatar = document.querySelector('.world-owner');
            return avatar.dataset.animation === 'land'
                && getComputedStyle(avatar.querySelector('.world-owner-state-sprite')).animationName === 'owner-land-cycle';
        }""", timeout=TIMEOUT)
        page.wait_for_function("""() => {
            const avatar = document.querySelector('.world-owner');
            return avatar.dataset.animation === 'land'
                && getComputedStyle(avatar.querySelector('.world-owner-state-sprite')).backgroundPositionX === '-120px';
        }""", timeout=TIMEOUT)
        page.wait_for_function("""() => document.querySelector('.world-owner').dataset.animation === 'idle'""", timeout=TIMEOUT)
        page.emulate_media(reduced_motion="reduce")
        assert owner.locator(".world-owner-state-sprite").evaluate(
            "el => getComputedStyle(el).animationName === 'none'"), "reduced motion must suppress the idle cycle"
        page.emulate_media(reduced_motion="no-preference")
        owner.focus(timeout=TIMEOUT)
        for _ in range(11):
            previous_position = int(owner.get_attribute("data-position", timeout=TIMEOUT))
            owner.press("ArrowRight", timeout=TIMEOUT)
            page.wait_for_function("""old => {
                const avatar = document.querySelector('.world-owner');
                return Number(avatar.dataset.position) !== old;
            }""", arg=previous_position, timeout=TIMEOUT)
            page.wait_for_function("""() => document.querySelector('.world-owner').dataset.walking === 'false'""", timeout=TIMEOUT)
        page.wait_for_function("""() => document.querySelector('.studio-world-map').dataset.currentArea === 'garden'
            && Number(document.querySelector('.studio-world-map').dataset.cameraX) > 0""", timeout=TIMEOUT)
        expect(page.locator(".studio-mini-map")).to_have_attribute("aria-label", re.compile("Forest Garden"), timeout=TIMEOUT)
        for _ in range(11):
            previous_position = int(owner.get_attribute("data-position", timeout=TIMEOUT))
            owner.press("ArrowLeft", timeout=TIMEOUT)
            page.wait_for_function("""old => Number(document.querySelector('.world-owner').dataset.position) !== old""",
                arg=previous_position, timeout=TIMEOUT)
            page.wait_for_function("""() => document.querySelector('.world-owner').dataset.walking === 'false'""", timeout=TIMEOUT)
        page.wait_for_function("""() => document.querySelector('.studio-world-map').dataset.currentArea === 'main-office'
            && Number(document.querySelector('.studio-world-map').dataset.cameraX) === 0""", timeout=TIMEOUT)
        expect(page.locator(".studio-mini-map")).to_have_attribute("aria-label", re.compile("Main Office"), timeout=TIMEOUT)
        passed(employeeCount=len(employees), projectCount=len(projects), localAvatarWalkAndJump=True,
               animationStates=["idle", "walk", "jump", "fall", "land"],
               observedIdleFramePositions=sorted(set(idle_frames)), observedWalkFramePositions=sorted(set(walk_frames)),
               scrollableAreas=True, miniMapTracksOwner=True,
               employeeStateUnchanged=True)

        begin("bundled-CC0-pixel-art")
        for path in dict.fromkeys([spec[1] for spec in ORIGINAL_IMAGES] + ["/art/2dpig-office.png"]):
            response = page.request.get(base + path, timeout=TIMEOUT)
            try:
                assert response.status == 200, (path, response.status)
            finally:
                response.dispose()
        images = []
        for selector, path, width, height in ORIGINAL_IMAGES:
            image = page.locator(selector).evaluate("""el => ({path:new URL(el.currentSrc).pathname,
                complete:el.complete,width:el.naturalWidth,height:el.naturalHeight,
                rendering:getComputedStyle(el).imageRendering})""")
            assert image == {"path": path, "complete": True, "width": width, "height": height, "rendering": "pixelated"}, image
            images.append(image)
        passed(originalStudioAssets=images, CC0ReferenceAtlas="/art/2dpig-office.png")
        screenshot("dioffice-office-desktop.png", world=True)

        begin("native-dialog-focus-escape-keyboard-tabs-disabled-runtime")
        workstation = page.get_by_role("button", name="Buka workstation " + employee["name"], exact=True)
        workstation.click(timeout=TIMEOUT)
        inspector = window(".workstation-identity")
        assert_native(inspector)
        expect(inspector.locator(".workstation-identity h2")).to_have_text(employee["name"], timeout=TIMEOUT)
        expect(inspector.locator(".workstation-identity .state-badge")).to_have_attribute("data-state", employee["status"], timeout=TIMEOUT)
        expect(inspector.get_by_role("button", name="Start", exact=True)).to_be_disabled(timeout=TIMEOUT)
        assert "Belum ada sesi runtime" in inspector.inner_text()
        assert inspector.locator(".pixel-sprite").first.evaluate("el => getComputedStyle(el).imageRendering") == "pixelated"
        screenshot("dioffice-workstation-desktop.png")
        activity = inspector.get_by_role("tab", name="Activity", exact=True)
        activity.focus(timeout=TIMEOUT)
        activity.press("ArrowRight", timeout=TIMEOUT)
        diff = inspector.get_by_role("tab", name="Diff", exact=True)
        expect(diff).to_have_attribute("aria-selected", "true", timeout=TIMEOUT)
        expect(diff).to_be_focused(timeout=TIMEOUT)
        assert "Diff belum tersedia" in inspector.get_by_role("tabpanel").inner_text()
        diff.press("End", timeout=TIMEOUT)
        approvals = inspector.get_by_role("tab", name="Approvals", exact=True)
        expect(approvals).to_have_attribute("aria-selected", "true", timeout=TIMEOUT)
        expect(approvals).to_be_focused(timeout=TIMEOUT)
        approvals.press("Home", timeout=TIMEOUT)
        expect(activity).to_have_attribute("aria-selected", "true", timeout=TIMEOUT)
        escape_window(inspector)
        expect(workstation).to_be_focused(timeout=TIMEOUT)
        passed(nativeDialog=True, runtimeStartDisabled=True)

        begin("private-reference-image-local-preview-remove")
        instruction = "UI smoke: local preview only; do not save this draft."
        quick = page.get_by_role("textbox", name="Quick instruction", exact=True)
        quick.fill(instruction, timeout=TIMEOUT)
        quick.press("Enter", timeout=TIMEOUT)  # HUD opens review only; never Save draft.
        composer = window(".create-panel")
        assert_native(composer)
        textarea = composer.locator("textarea")
        expect(textarea).to_have_value(instruction, timeout=TIMEOUT)
        composer.locator("input[type=file]").set_input_files(str(ART), timeout=TIMEOUT)
        page.wait_for_function("""() => {
            const image = document.querySelector('.attachment-preview');
            return image && image.complete && image.naturalWidth === 256 && image.naturalHeight === 160
                && image.src.startsWith('blob:');
        }""", timeout=TIMEOUT)
        screenshot("dioffice-composer-desktop.png")
        composer.get_by_role("button", name="Remove " + ART.name, exact=True).click(timeout=TIMEOUT)
        expect(composer.locator(".attachment-preview")).to_have_count(0, timeout=TIMEOUT)
        textarea.fill("", timeout=TIMEOUT)
        expect(composer.get_by_role("button", name="Save draft", exact=True)).to_be_disabled(timeout=TIMEOUT)
        escape_window(composer)
        passed(submitted=False, serverMutation=False, previewDimensions={"width": 256, "height": 160}, hudReviewOnly=True)

        begin("API-task-board-filter-read-only-detail")
        board = open_pane("Task board", ".tasks-panel")
        expect(page.locator(".create-panel")).to_have_count(0, timeout=TIMEOUT)
        task_path = "/api/v1/projects/" + quote(project_id, safe="") + "/tasks"
        tasks = get_json(task_path)["items"]
        assert_cards(board, tasks)
        filters = board.get_by_role("group", name="Filter tasks by status")
        filters.get_by_role("button", name="Done", exact=True).click(timeout=TIMEOUT)
        expect(board.locator(".task-card")).to_have_count(sum(row["status"] == "DONE" for row in tasks), timeout=TIMEOUT)
        filters.get_by_role("button", name="All", exact=True).click(timeout=TIMEOUT)
        assert_cards(board, tasks)
        if tasks:
            title_button = board.locator(".task-title-button").first
            title_button.click(timeout=TIMEOUT)
            detail = window(".task-detail-title")
            assert_native(detail)
            expect(page.locator("dialog[open]")).to_have_count(2, timeout=TIMEOUT)
            expect(detail.locator(".task-detail-title")).to_have_text(tasks[0]["title"], timeout=TIMEOUT)
            expect(detail.locator(".task-detail-meta code")).to_have_text(tasks[0]["id"], timeout=TIMEOUT)
            expect(detail.get_by_role("button", name="Start", exact=True)).to_be_disabled(timeout=TIMEOUT)
            assert "Read-only detail" in detail.inner_text()
            escape_window(detail)  # Close child first, leaving the journal intact.
            expect(page.locator("dialog[open]")).to_have_count(1, timeout=TIMEOUT)
            assert_native(board)
            expect(title_button).to_be_focused(timeout=TIMEOUT)
        screenshot("dioffice-board-desktop.png")
        escape_window(board)
        expect(nav.get_by_role("button", name="Task board", exact=True)).to_be_focused(timeout=TIMEOUT)
        passed(loadedTasks=len(tasks), detailChecked=bool(tasks), nestedDetailChecked=bool(tasks), serverMutation=False)

        begin("real-team-and-compose-navigation")
        team = open_pane("Team", ".team-grid")
        expect(team.locator(".team-card")).to_have_count(len(employees), timeout=TIMEOUT)
        for index, row in enumerate(employees):
            card = team.locator(".team-card").nth(index)
            expect(card.locator("h2")).to_have_text(row["name"], timeout=TIMEOUT)
            expect(card.locator(".state-badge")).to_have_attribute("data-state", row["status"], timeout=TIMEOUT)
        screenshot("dioffice-team-desktop.png")
        team.locator(".team-card").first.get_by_role("button", name="Beri instruksi", exact=False).click(timeout=TIMEOUT)
        composer = window(".create-panel")
        assert_native(composer)
        expect(composer.locator("textarea")).to_be_focused(timeout=TIMEOUT)
        expect(composer.locator("select[required]")).to_have_value(employees[0]["id"], timeout=TIMEOUT)
        expect(page.locator(".team-grid")).to_have_count(0, timeout=TIMEOUT)
        escape_window(composer)
        passed(employeeCount=len(employees), composerFocused=True, serverMutation=False)

        for width, height in VIEWPORTS:
            begin("responsive-no-horizontal-overflow", width=width)
            page.set_viewport_size({"width": width, "height": height})
            page.evaluate("window.scrollTo(0, 0)")
            clean_world()
            value = geometry()
            assert value["page"]["width"] <= width, value
            assert value["page"]["height"] <= height + 1, value
            # The world owns the available playfield, with reserved bottom HUD
            # space. Requiring 95% of the *whole* viewport puts actors under HUD.
            assert value["world"]["width"] >= width * 0.95 and value["world"]["height"] >= height * 0.65, value
            assert value["world"]["height"] >= height - value["hud"]["height"] - 24, value
            assert value["world"]["bottom"] <= value["hud"]["y"] + 16, value
            plates = value["nameplates"]
            sign, screen = value["locationSign"], value["computerScreen"]
            sign_overlap_x = min(sign["right"], screen["right"]) - max(sign["x"], screen["x"])
            sign_overlap_y = min(sign["bottom"], screen["bottom"]) - max(sign["y"], screen["y"])
            assert sign_overlap_x <= 1 or sign_overlap_y <= 1, {
                "workstationScreenCoversStudioSign": {"sign": sign, "screen": screen}}
            assert owner_name in value["ownerText"], value
            assert value["ownerOverflow"]["scrollWidth"] <= value["ownerOverflow"]["clientWidth"] + 1, value
            assert value["worldAreas"] == 3, value
            assert 0 <= value["jump"]["x"] and value["jump"]["right"] <= width + 1, value
            assert value["jump"]["width"] >= 44 and value["jump"]["height"] >= 44, value
            for actor in ("owner", "employee"):
                a, b = plates[actor], plates["computer"]
                overlap_x = min(a["right"], b["right"]) - max(a["x"], b["x"])
                overlap_y = min(a["bottom"], b["bottom"]) - max(a["y"], b["y"])
                assert overlap_x <= 1 or overlap_y <= 1, {"overlappingNameplates": [actor, "computer"], "rectangles": plates}
            assert plates["owner"]["x"] >= 0 and plates["owner"]["right"] <= width, plates
            assert value["hud"]["height"] < height * 0.4 and value["hud"]["bottom"] <= height, value
            assert value["workstation"]["x"] >= 0 and value["workstation"]["right"] <= width, value
            assert value["workstation"]["y"] >= 0 and value["workstation"]["bottom"] <= value["hud"]["y"], value
            expect(page.locator(".app-sidebar, .workspace-hero, .office-hero, .employee-panel")).to_have_count(0, timeout=TIMEOUT)
            assert page.locator(".pixel-stage").evaluate("el => getComputedStyle(el).position") == "absolute"
            touch_targets = mobile_chrome() if width <= 390 else []
            if width == 390:
                screenshot("dioffice-office-mobile.png", world=True)
            workstation.click(timeout=TIMEOUT)
            inspector = window(".workstation-identity")
            assert_native(inspector)
            window_sizes = {"workstation": assert_window_geometry(inspector)}
            expect(inspector.get_by_role("button", name="Start", exact=True)).to_be_disabled(timeout=TIMEOUT)
            if width <= 390:
                touch_targets.extend(mobile_chrome())
            escape_window(inspector)
            for label, selector, key in (("Task board", ".tasks-panel", "board"),
                                         ("Beri instruksi", ".create-panel", "composer"),
                                         ("Team", ".team-grid", "team")):
                dialog = open_pane(label, selector)
                window_sizes[key] = assert_window_geometry(dialog)
                if width <= 390:
                    touch_targets.extend(mobile_chrome())
                escape_window(dialog)
            passed(geometry=value, windows=window_sizes, worldDominatesViewport=True,
                   minimumMobileTouchTarget=44 if width <= 390 else None, mobileChrome=touch_targets)

        begin("API-error-notice-and-no-stale-task-cards", faultInjection="browser-only", serverMutation=False)
        page.set_viewport_size({"width": 1440, "height": 1000})
        board = open_pane("Task board", ".tasks-panel")
        assert_cards(board, get_json(task_path)["items"])
        page.route(tasks_fault, unavailable_tasks)
        board.get_by_role("button", name="Refresh tasks", exact=True).click(timeout=TIMEOUT)
        expect(board.get_by_role("heading", name="Task belum bisa dimuat", exact=True)).to_be_visible(timeout=TIMEOUT)
        # World notices are inert behind a modal; use DOM, not a role locator.
        expect(page.locator(".studio-notices [role=alert]")).to_be_visible(timeout=TIMEOUT)
        expect(board.locator(".task-card")).to_have_count(0, timeout=TIMEOUT)
        assert board.locator(".task-count").inner_text().startswith("–")
        expect(page.locator(".hud-snapshot span").nth(0)).to_have_text("Task belum dikonfirmasi", timeout=TIMEOUT)
        assert "Snapshot belum dikonfirmasi" in page.locator(".studio-quest-tracker").inner_text()
        expect(page.locator(".studio-quest-tracker li")).to_have_count(0, timeout=TIMEOUT)
        assert fault_hits["tasks"] > 0
        page.unroute(tasks_fault, unavailable_tasks)
        board.get_by_role("button", name="Refresh tasks", exact=True).click(timeout=TIMEOUT)
        assert_cards(board, get_json(task_path)["items"])
        expect(page.locator(".studio-notices [role=alert]")).to_have_count(0, timeout=TIMEOUT)
        escape_window(board)
        passed(injectedReadFailures=fault_hits["tasks"], recoveredFromRealAPI=True)

        begin("directory-error-not-an-empty-office-and-retry-recovery", faultInjection="browser-only", serverMutation=False)
        page.route(directory_fault, unavailable_directory)
        page.reload(wait_until="domcontentloaded", timeout=TIMEOUT)
        expect(page.locator(".workspace-unavailable")).to_be_visible(timeout=TIMEOUT)
        expect(page.get_by_role("alert")).to_be_visible(timeout=TIMEOUT)
        assert "Workspace belum bisa dimuat" in page.locator(".workspace-unavailable").inner_text()
        expect(page.locator(".scene-employee, .scene-workstation, .create-panel")).to_have_count(0, timeout=TIMEOUT)
        expect(page.locator(".project-picker select")).to_be_disabled(timeout=TIMEOUT)
        expect(nav.get_by_role("button", name="Beri instruksi", exact=True)).to_be_disabled(timeout=TIMEOUT)
        expect(page.locator(".hud-snapshot span").nth(0)).to_have_text("Task belum dikonfirmasi", timeout=TIMEOUT)
        expect(page.locator(".hud-snapshot span").nth(1)).to_have_text("Team belum dikonfirmasi", timeout=TIMEOUT)
        assert fault_hits["employees"] > 0
        page.unroute(directory_fault, unavailable_directory)
        page.get_by_role("button", name="Muat ulang workspace", exact=True).click(timeout=TIMEOUT)
        clean_world()
        expect(page.get_by_role("alert")).to_have_count(0, timeout=TIMEOUT)
        recovered_employees = get_json("/api/v1/employees")["items"]
        recovered_project = page.locator(".project-picker select").input_value(timeout=TIMEOUT)
        recovered_tasks = get_json("/api/v1/projects/" + quote(recovered_project, safe="") + "/tasks")["items"]
        expect(page.locator(".hud-snapshot span").nth(0)).to_have_text(f"{len(recovered_tasks)} task tersimpan", timeout=TIMEOUT)
        expect(page.locator(".hud-snapshot span").nth(1)).to_have_text(f"{len(recovered_employees)} employee", timeout=TIMEOUT)
        expect(page.locator(".workspace-unavailable")).to_have_count(0, timeout=TIMEOUT)
        passed(injectedReadFailures=fault_hits["employees"], recoveredFromRealAPI=True)

        begin("no-JavaScript-page-errors")
        clean_world()
        assert not errors, errors
        assert not blocked_writes, {"unexpectedWriteRequestsBlocked": blocked_writes}
        passed(pageErrors=errors, unexpectedWriteRequests=blocked_writes, screenshots=screenshots)
        actual = tuple((row["check"], row.get("width")) for row in results)
        assert actual == EXPECTED_CHECKS, {"expected": EXPECTED_CHECKS, "actual": actual}
        report()
        return results
    except Exception as error:
        results.append({**current, "passed": False, "error": safe_error_description(error)})
        report()
        raise
    finally:
        page.unroute(tasks_fault, unavailable_tasks)
        page.unroute(directory_fault, unavailable_directory)
        page.unroute("**/api/v1/**", readonly_guard)
        page.remove_listener("pageerror", on_page_error)
        if original_viewport is not None and not page.is_closed():
            page.set_viewport_size(original_viewport)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:5174")
    default_output = Path(os.environ.get("TMPDIR") or (
        str(Path(os.environ["LOCALAPPDATA"]) / "hermes" / "cache" / "scratch")
        if os.name == "nt" and "LOCALAPPDATA" in os.environ else
        str(Path(os.environ.get("HERMES_HOME", str(Path.home() / ".hermes"))) / "cache" / "scratch")))
    parser.add_argument("--output", type=Path, default=default_output)
    parser.add_argument("--channel", default="msedge", help="Installed Chromium channel, e.g. msedge or chrome")
    args = parser.parse_args()
    from playwright.sync_api import sync_playwright

    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(channel=args.channel, headless=True)
        try:
            context = browser.new_context(viewport={"width": 1440, "height": 1000}, device_scale_factor=1)
            try:
                results = verify_studio_ui(context.new_page(), args.base_url, args.output)
                print(json.dumps(results, indent=2))
                print("Screenshots and QA report:", args.output)
            finally:
                context.close()
        finally:
            browser.close()


if __name__ == "__main__":
    main()
