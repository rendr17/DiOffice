"""Bounded, opt-in real HTTP/PostgreSQL event E2E; only a Go-owned fixture.

Run via TestEventBrowserPersistedHistoryAndSSE, not against a user runtime.
The durable event journeys never fulfill/mock routes. A separate caller-owned
UI QA context labels two browser-only GET-failure scenarios explicitly.
One real employee GET is paused until browser
offline mode to reproduce a queued-refresh network failure deterministically.
The EventSource observer delegates every byte,
retry, and close to the native implementation. Fixture SQL is explicitly marked
and never masquerades as a product task-update API or agent execution.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import re
import time
import traceback
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from playwright.sync_api import sync_playwright

CHECKS = [
    "initial-history-real-api",
    "persisted-history-after-reload",
    "live-http-append-and-snapshot-invalidation",
    "browser-form-create-through-real-http",
    "idempotent-http-create-no-extra-fact",
    "offline-http-write-and-native-replay",
    "authoritative-task-snapshot-not-event-payload",
    "project-switch-disposes-old-scope",
    "switched-project-live-events-only",
    "switch-back-reloads-missed-project-history",
    "cursor-expiry-real-410-and-snapshot-recovery",
    "logout-closes-activity-and-revokes-session",
    "no-page-errors-and-no-runtime-start",
]

OBSERVE_NATIVE_EVENTS = r"""(() => {
  const Native = window.EventSource;
  const evidence = {sources: [], facts: []};
  Object.defineProperty(window, '__eventBrowserEvidence', {value: evidence});
  window.EventSource = class extends Native {
    constructor(url, options) {
      super(url, options);
      const parsed = new URL(url, location.href);
      const projectId = parsed.pathname.split('/')[4];
      this.record = {id: evidence.sources.length, projectId,
        after: parsed.searchParams.get('after'), closed: false, opens: 0, errors: 0};
      evidence.sources.push(this.record);
      this.addEventListener('open', () => this.record.opens++);
      this.addEventListener('error', () => this.record.errors++);
      this.addEventListener('message', (message) => {
        try {
          const event = JSON.parse(message.data);
          evidence.facts.push({sourceId: this.record.id, eventId: event.eventId,
            projectId: event.projectId, sequence: event.streamSequence,
            lastEventId: message.lastEventId});
        } catch { evidence.facts.push({invalidJSON: true}); }
      });
    }
    close() { this.record.closed = true; super.close(); }
  };
})();"""

TIMELINE = """() => Array.from(document.querySelectorAll('.event-timeline li')).map(el =>
  ({eventId: el.dataset.eventId, streamSequence: Number(el.dataset.sequence)}))"""
TASK_CARDS = """() => Array.from(document.querySelectorAll('.task-card')).map(el => ({
  title: el.querySelector('.task-title-button').textContent.trim(),
  description: el.querySelector('.task-description').textContent.trim(),
  status: el.querySelector('.state-badge').textContent.trim(),
  priority: el.querySelector('.priority-label').textContent.trim()
}))"""


class CheckFailure(Exception):
    def __init__(self, reason: str, details: dict | None = None):
        super().__init__(reason)
        self.reason, self.details = reason, details or {}


def require(condition: bool, reason: str, **details):
    if not condition:
        raise CheckFailure(reason, details)


def loopback_url(value: str) -> str:
    parsed = urlparse(value)
    require(parsed.scheme == "http" and parsed.hostname == "127.0.0.1"
            and parsed.port is not None and parsed.username is None
            and parsed.password is None and parsed.path in ("", "/")
            and not parsed.query and not parsed.fragment,
            "Only an ephemeral loopback fixture origin is allowed")
    return value.rstrip("/")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixture-url", required=True)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--channel", default="msedge")
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    report_path = output / "dioffice-events-browser.json"
    report = {"passed": False, "checks": [], "skipped": [], "screenshots": [],
              "pageResponses": [], "fixtureOnlyMutations": [], "cleanup": {},
              "runtimeStarted": False, "mockedResponses": False, "mockedResponsesScope": "durable-event-journeys"}
    deadline = time.monotonic() + 145
    start = time.monotonic()
    phase_start = start
    stage = CHECKS[0]
    page = context = browser = fixture_request = api_request = None
    page_errors = []
    runtime_requests = []
    native_evidence = {}

    def timeout(maximum=9000):
        remaining = int((deadline - time.monotonic()) * 1000)
        require(remaining > 0, "Browser test exceeded its 145-second bound")
        return max(1, min(maximum, remaining))

    def begin(name):
        nonlocal stage, phase_start
        stage, phase_start = name, time.monotonic()

    def passed(**details):
        report["checks"].append({"name": stage, "passed": True,
                                 "durationMs": round((time.monotonic() - phase_start) * 1000),
                                 **details})

    def fixture_get(path="/fixture/state"):
        response = fixture_request.get(path, timeout=timeout(4000))
        require(response.status == 200, "Fixture read failed", path=path, status=response.status)
        return response.json()

    def fixture_post(path, body=None):
        response = fixture_request.post(path, data=body or {}, timeout=timeout(5000))
        require(response.status == 200, "Test-only fixture mutation failed", path=path, status=response.status)
        report["fixtureOnlyMutations"].append(path)
        return response.json()

    def api_get(path, status=200):
        response = api_request.get(path, timeout=timeout(4000))
        require(response.status == status, "Real product API read returned unexpected status",
                path=path, status=response.status, expectedStatus=status)
        return response.json()

    def task_body(title):
        return {"assigneeEmployeeId": fixture["employeeId"], "title": title,
                "description": "Created through the real HTTP API, not a fixture route.",
                "acceptanceCriteria": [], "requiredChecks": [], "taskType": "feature", "priority": "NORMAL"}

    def create(project, title, key, body=None, status=201):
        response = api_request.post(f"/api/v1/projects/{project}/tasks", data=body or task_body(title),
                                    headers={"Idempotency-Key": key}, timeout=timeout(5000))
        require(response.status == status, "Real product HTTP create returned unexpected status",
                status=response.status, expectedStatus=status, title=title)
        # Only this non-secret boolean response header is inspected.
        replayed = response.headers.get("idempotency-replayed") == "true"
        return response.json(), replayed

    def recent(project):
        return api_get(f"/api/v1/projects/{project}/events")

    def tasks(project):
        return api_get(f"/api/v1/projects/{project}/tasks")["items"]

    def db_state(project):
        return fixture_get()["projects"][project]

    def projection(events):
        return [{"eventId": event["eventId"], "streamSequence": event["streamSequence"]} for event in events]

    def timeline_matches(project):
        persisted = db_state(project)
        want = list(reversed(projection(persisted["events"][-100:])))
        page.wait_for_function("want => JSON.stringify((" + TIMELINE + ")()) === JSON.stringify(want)",
                               arg=want, timeout=timeout())
        rows = page.evaluate(TIMELINE)
        require(len(rows) == len({row["eventId"] for row in rows}), "Activity duplicated a persisted event ID")
        require([row["streamSequence"] for row in rows] == sorted(
            [row["streamSequence"] for row in rows], reverse=True), "Activity is not newest-sequence-first")
        return rows

    def snapshot_matches(project, keep_open=False):
        # Read the real visible quest journal through the new HUD journey.
        # Never replace authoritative cards with a hidden test-only mirror.
        had_activity = page.locator(".workstation-activity").is_visible()
        if had_activity:
            close_activity()
        if not page.locator(".tasks-panel").is_visible():
            page.get_by_role("navigation", name="Workspace navigation").get_by_role(
                "button", name="Task board", exact=True).click(timeout=timeout())
        page.locator(".tasks-panel").wait_for(timeout=timeout())
        snapshot = tasks(project)
        want = sorted([{"title": row["title"], "description": row["description"].strip(),
                        "status": row["status"].replace("_", " "), "priority": row["priority"]}
                       for row in snapshot], key=lambda row: row["title"])
        page.wait_for_function("want => JSON.stringify((" + TASK_CARDS +
                               ")().sort((a,b) => a.title.localeCompare(b.title))) === JSON.stringify(want)",
                               arg=want, timeout=timeout())
        if not keep_open:
            page.get_by_role("dialog").get_by_role("button", name="Tutup panel", exact=True).click(timeout=timeout())
            page.get_by_role("dialog").wait_for(state="hidden", timeout=timeout())
            if had_activity:
                open_activity()
                timeline_matches(project)
        return snapshot

    def live():
        page.locator(".event-connection[data-connection='live']").first.wait_for(timeout=timeout(12000))

    def open_activity():
        page.locator(".scene-workstation").click(timeout=timeout())
        page.get_by_role("dialog").wait_for(timeout=timeout())
        live()

    def close_activity():
        page.get_by_role("dialog").get_by_role("button", name="Tutup panel", exact=True).click(timeout=timeout())
        page.get_by_role("dialog").wait_for(state="hidden", timeout=timeout())

    def evidence():
        return page.evaluate("window.__eventBrowserEvidence")

    def active_source(project):
        sources = [source for source in evidence()["sources"]
                   if source["projectId"] == project and not source["closed"] and source["opens"] > 0]
        require(len(sources) == 1, "Expected exactly one native open EventSource for selected scope",
                openSourceCount=len(sources))
        return sources[0]

    def wait_state(predicate, reason):
        end = time.monotonic() + timeout() / 1000
        while time.monotonic() < end:
            value = fixture_get()
            if predicate(value):
                return value
            page.wait_for_timeout(min(100, timeout()))
        raise CheckFailure(reason)

    def stable(predicate, reason, milliseconds=800):
        # A bounded negative observation window, not a blind readiness sleep.
        end = time.monotonic() + min(milliseconds, timeout()) / 1000
        while time.monotonic() < end:
            require(predicate(), reason)
            page.wait_for_timeout(min(100, timeout()))

    def reads(project):
        return sum(row["projectId"] == project and row["resource"] == "tasks"
                   and row["status"] == 200 for row in report["pageResponses"])

    def screenshot(name):
        target = output / (name + ".png")
        page.screenshot(path=str(target), full_page=True, timeout=timeout())
        report["screenshots"].append(str(target))

    def response_seen(response):
        parsed = urlparse(response.url)
        match = re.fullmatch(r"/api/v1/projects/([0-9a-f-]+)/(?P<resource>tasks|events|events/stream)", parsed.path)
        if match and response.request.method == "GET":
            report["pageResponses"].append({"projectId": match.group(1), "resource": match.group("resource"),
                                            "after": parse_qs(parsed.query).get("after", [None])[0],
                                            "status": response.status})

    try:
        fixture_origin = loopback_url(args.fixture_url)
        with sync_playwright() as playwright:
            try:
                fixture_request = playwright.request.new_context(base_url=fixture_origin, timeout=4000)
                fixture = fixture_get("/fixture")
                require(fixture.get("protocol") == "dioffice-event-browser-v1"
                        and fixture.get("isolated") is True
                        and re.fullmatch(r"dioffice_http_test_[0-9a-f]{16}", fixture.get("schema", "")),
                        "The runner requires the Go disposable-schema fixture")
                base = loopback_url(fixture["webOrigin"])
                loopback_url(fixture["apiOrigin"])
                report["fixture"] = {key: fixture[key] for key in
                                     ["schema", "projectA", "projectB", "webOrigin", "apiOrigin"]}
                a, b = fixture["projectA"], fixture["projectB"]
                browser = playwright.chromium.launch(channel=args.channel, headless=True, timeout=timeout(20000))
                qa_spec = importlib.util.spec_from_file_location("dioffice_studio_qa", Path(__file__).with_name("check-office.py"))
                require(qa_spec is not None and qa_spec.loader is not None, "Read-only UI QA module could not be loaded")
                qa_module = importlib.util.module_from_spec(qa_spec)
                qa_spec.loader.exec_module(qa_module)
                before_ui = fixture_get()["projects"]
                qa_context = browser.new_context(viewport={"width": 1440, "height": 1000}, device_scale_factor=1)
                report["uiQA"] = {"passed": False, "checks": [], "isolatedContextClosed": False,
                                  "serverMutation": False, "faultInjection": "browser-only GET failures"}
                try:
                    qa_page = qa_context.new_page()
                    ui_checks = qa_module.verify_studio_ui(qa_page, base, output)
                    require(len(ui_checks) == 13 and all(check.get("passed") is True for check in ui_checks),
                            "Read-only game UI QA did not pass all 13 checks")
                    require(fixture_get()["projects"] == before_ui, "Read-only UI QA changed task/event database state")
                    report["uiQA"].update(passed=True, checks=ui_checks,
                        fonts=qa_page.evaluate("""() => Object.fromEntries(['.game-ui', '.hud-nav button', '.world-owner'].map(selector =>
                            [selector, getComputedStyle(document.querySelector(selector)).fontFamily]))"""))
                finally:
                    qa_context.close()
                    report["uiQA"]["isolatedContextClosed"] = True
                context = browser.new_context(viewport={"width": 1440, "height": 1000}, device_scale_factor=1)
                context.add_init_script(OBSERVE_NATIVE_EVENTS)
                page = context.new_page()
                page.set_default_timeout(9000)
                page.on("pageerror", lambda error: page_errors.append(type(error).__name__))
                page.on("response", response_seen)
                page.on("request", lambda request: runtime_requests.append(urlparse(request.url).path)
                        if re.search(r"/(start|runtime|github|execute)(/|$)", urlparse(request.url).path) else None)
                wait_state(lambda state: not any(state["activeStreams"].values()), "UI QA context left an active server stream")
                page.goto(base, wait_until="domcontentloaded", timeout=timeout(20000))
                page.locator(".scene-workstation").wait_for(timeout=timeout(20000))
                require(page.locator(".studio-hud").count() == 1
                        and page.locator(".app-sidebar").count() == 0,
                        "Primary surface must be a game world with a lower HUD, not the old dashboard")
                page.wait_for_function("""() => ['.world-art', '.world-character', '.hud-portrait'].every(selector => {
                    const image = document.querySelector(selector);
                    return image && image.complete && image.naturalWidth > 0;
                })""", timeout=timeout())
                page.wait_for_function("id => document.querySelector('.project-picker select')?.value === id", arg=a, timeout=timeout())
                owner = page.locator(".world-owner")
                owner_before = float(owner.get_attribute("data-position"))
                owner.click(timeout=timeout())
                page.keyboard.press("ArrowRight")
                page.wait_for_function("""before => Number(document.querySelector('.world-owner').dataset.position) > before""",
                    arg=owner_before, timeout=timeout())
                require(float(owner.get_attribute("data-position")) > owner_before,
                        "Manual keyboard input did not move the human Owner avatar")
                require(page.locator(".employee-bubble .state-badge").inner_text().strip() == "IDLE",
                        "Moving Owner must never simulate employee work")
                screenshot("dioffice-studio-world-desktop")
                page.set_viewport_size({"width": 390, "height": 844})
                screenshot("dioffice-studio-world-mobile")
                page.set_viewport_size({"width": 1440, "height": 1000})
                open_activity()
                game_window = page.get_by_role("dialog")
                before_drag = game_window.bounding_box()
                titlebar = game_window.locator(":scope > .window-bar").bounding_box()
                start_x, start_y = titlebar["x"] + titlebar["width"] / 3, titlebar["y"] + titlebar["height"] / 2
                page.mouse.move(start_x, start_y)
                page.mouse.down()
                page.mouse.move(start_x + 60, start_y + 30, steps=8)
                page.mouse.up()
                after_drag = game_window.bounding_box()
                require(after_drag["x"] > before_drag["x"] + 20,
                        "Dragging game-window titlebar did not move the actual native dialog")
                require(after_drag["x"] >= 8 and after_drag["y"] >= 8
                        and after_drag["x"] + after_drag["width"] <= 1432
                        and after_drag["y"] + after_drag["height"] <= 992,
                        "Dragged game-window escaped the viewport")
                game_window.get_by_role("button", name="Reset posisi panel", exact=True).click(timeout=timeout())
                require(abs(game_window.bounding_box()["x"] - before_drag["x"]) < 1,
                        "Game-window reset did not restore its centered position")
                # Clone cookies only in memory into an independent APIRequestContext.
                # It is deliberately NOT context.request, so browser offline mode
                # cannot block the writer. Never serialize/log headers or cookies.
                cookies = context.cookies(base)
                csrf = next(cookie["value"] for cookie in cookies if cookie["name"] == "dioffice_csrf")
                cookie_header = "; ".join(cookie["name"] + "=" + cookie["value"] for cookie in cookies)
                api_request = playwright.request.new_context(base_url=base, timeout=5000,
                    extra_http_headers={"Cookie": cookie_header, "X-CSRF-Token": csrf, "Origin": base})
                del cookies, csrf, cookie_header
                history = recent(a)
                rows = timeline_matches(a)
                require(history["hasMore"] is False and history["nextCursor"] == "1"
                        and projection(history["items"]) == list(reversed(rows)) and len(rows) == 1,
                        "Initial Activity does not match the committed API/DB history")
                snapshot_matches(a)
                start_disabled = page.get_by_role("dialog").get_by_role("button", name="Start", exact=True).is_disabled()
                require(start_disabled, "Read-only workstation enabled runtime Start")
                passed(events=len(rows), cursor=history["nextCursor"])

                begin(CHECKS[1])
                first_rows = rows
                page.reload(wait_until="domcontentloaded", timeout=timeout(20000))
                page.locator(".scene-workstation").wait_for(timeout=timeout())
                open_activity()
                require(timeline_matches(a) == first_rows, "Reload changed or lost persisted event IDs/sequences")
                require(projection(recent(a)["items"]) == list(reversed(first_rows)), "Reload history differs from real API")
                passed(events=len(first_rows), sameEventIds=True)
                screenshot("dioffice-events-history-reload")

                begin(CHECKS[2])
                snapshot_matches(a)
                before_reads = reads(a)
                live_body = task_body("Browser separate HTTP draft")
                live_task, replayed = create(a, live_body["title"], "browser-live-http", live_body)
                require(not replayed and live_task["status"] == "DRAFT", "Live HTTP create was not a new durable draft")
                rows = timeline_matches(a)
                snapshot_matches(a)
                live_event = next(event for event in recent(a)["items"] if event["taskId"] == live_task["id"])
                require(any(fact.get("eventId") == live_event["eventId"]
                            and fact.get("lastEventId") == str(live_event["streamSequence"])
                            for fact in evidence()["facts"]), "Live event was not received by a real native SSE connection")
                require(reads(a) > before_reads, "SSE fact did not trigger an authoritative browser task GET")
                passed(taskId=live_task["id"], eventId=live_event["eventId"], sequence=live_event["streamSequence"],
                       writer="independent APIRequestContext", browserSnapshotReads=reads(a)-before_reads)

                begin(CHECKS[3])
                close_activity()
                page.get_by_role("navigation", name="Workspace navigation").get_by_role(
                    "button", name="Beri instruksi", exact=True).click(timeout=timeout())
                page.locator(".task-command-field textarea").fill("Browser form durable draft")
                with page.expect_response(lambda response: urlparse(response.url).path == f"/api/v1/projects/{a}/tasks"
                                          and response.request.method == "POST", timeout=timeout()) as created_response:
                    page.get_by_role("button", name="Save draft", exact=True).click(timeout=timeout())
                require(created_response.value.status == 201, "Browser task form did not receive HTTP 201")
                form_task = created_response.value.json()
                page.get_by_role("dialog").get_by_role("button", name="Tutup panel", exact=True).click(timeout=timeout())
                page.get_by_role("dialog").wait_for(state="hidden", timeout=timeout())
                snapshot_matches(a)
                open_activity()
                timeline_matches(a)
                form_event = next(event for event in recent(a)["items"] if event["taskId"] == form_task["id"])
                require(any(fact.get("eventId") == form_event["eventId"] for fact in evidence()["facts"]),
                        "Browser-created task did not arrive over native SSE")
                passed(taskId=form_task["id"], sequence=form_event["streamSequence"], httpStatus=201)
                screenshot("dioffice-events-live-append")

                begin(CHECKS[4])
                before_idempotent = db_state(a)
                facts_before = len(evidence()["facts"])
                same_task, replayed = create(a, live_body["title"], "browser-live-http", live_body)
                require(replayed and same_task["id"] == live_task["id"], "Idempotent HTTP retry did not replay the same task")
                conflict_body = {**live_body, "title": "Conflicting idempotency input"}
                conflict, _ = create(a, conflict_body["title"], "browser-live-http", conflict_body, status=409)
                require(conflict.get("error") == "idempotency_key_conflict", "Changed idempotency input did not conflict")
                require(db_state(a) == before_idempotent, "Idempotent retry/conflict changed task/event/outbox/audit/head rows")
                stable(lambda: len(evidence()["facts"]) == facts_before, "Idempotent retry emitted a duplicate SSE fact")
                timeline_matches(a)
                passed(replayedTaskId=same_task["id"], unchangedCountsAndSequence=True, conflictStatus=409)

                begin(CHECKS[5])
                cached_rows = timeline_matches(a)
                cached_cursor = db_state(a)["head"]
                native_before = active_source(a)
                context.set_offline(True)
                # Explicitly terminate only this fixture's SSE for deterministic
                # offline semantics across Chromium network-emulation versions.
                fixture_post("/fixture/disconnect", {"projectId": a})
                page.locator(".event-connection[data-connection='reconnecting']").first.wait_for(timeout=timeout())
                wait_state(lambda state: state["activeStreams"].get(a, 0) == 0, "Offline SSE did not terminate")
                require(page.evaluate(TIMELINE) == cached_rows, "Offline mode discarded already received Activity")
                offline_tasks = [create(a, f"Browser offline HTTP draft {index}", f"browser-offline-{index}")[0]
                                 for index in (1, 2)]
                require(page.evaluate(TIMELINE) == cached_rows, "An offline browser unexpectedly received new events")
                context.set_offline(False)
                live()
                rows = timeline_matches(a)
                snapshot_matches(a)
                native_after = active_source(a)
                require(native_after["id"] == native_before["id"] and native_after["opens"] > native_before["opens"],
                        "Reconnect replaced native EventSource instead of using its real retry/replay")
                streams = fixture_get()["streams"]
                require(any(stream["projectId"] == a and stream["lastEventId"] == str(cached_cursor)
                            and stream["after"] == native_before["after"] and stream["status"] == 200 for stream in streams),
                        "Reconnect did not send the native Last-Event-ID cursor to the real API")
                offline_events = [event for event in recent(a)["items"]
                                  if event["taskId"] in {task["id"] for task in offline_tasks}]
                received = [fact for fact in evidence()["facts"] if fact.get("eventId") in {event["eventId"] for event in offline_events}]
                require(len(received) == 2 and [fact["sequence"] for fact in received] == [event["streamSequence"] for event in offline_events],
                        "Offline events were not replayed exactly once in durable sequence order")
                passed(replayedEvents=len(received), lastEventId=str(cached_cursor),
                       sameNativeSource=True, offlineWriter="independent APIRequestContext", fixtureStreamTermination=True)
                screenshot("dioffice-events-offline-replay")

                begin(CHECKS[6])
                close_activity()
                snapshot_matches(a, keep_open=True)
                quiet_reads = reads(a)
                stable(lambda: reads(a) == quiet_reads, "Previous invalidations did not settle before snapshot fixture", 600)
                before_snapshot = db_state(a)
                mutation = fixture_post("/fixture/task-snapshot")
                require(mutation["emittedEvent"] is False and db_state(a) == before_snapshot,
                        "Snapshot-only fixture unexpectedly emitted or modified an event")
                require(page.get_by_role("button", name="Buka task Browser seed draft", exact=True).is_visible(),
                        "UI changed without an API snapshot invalidation")
                authoritative = next(task for task in tasks(a) if task["id"] == fixture["seedTaskId"])
                signal_task, _ = create(a, "Browser snapshot invalidation signal", "browser-snapshot-signal")
                snapshot_matches(a, keep_open=True)
                require(reads(a) > quiet_reads, "Live invalidation did not fetch a new task snapshot")
                page.get_by_role("button", name="Buka task " + authoritative["title"], exact=True).click(timeout=timeout())
                detail_dialog = page.get_by_role("dialog").filter(has=page.locator(".task-detail-title"))
                require(page.locator(".task-detail-description").inner_text() == authoritative["description"]
                        and page.locator(".task-detail-heading .state-badge").inner_text().strip() == "BLOCKED"
                        and fixture["seedTaskId"] in detail_dialog.inner_text(),
                        "Read-only task details do not reflect the authoritative DB/API snapshot")
                detail_dialog.get_by_role("button", name="Tutup panel", exact=True).click(timeout=timeout())
                close_activity()
                open_activity()
                timeline_matches(a)
                original_fact = next(event for event in recent(a)["items"] if event["taskId"] == fixture["seedTaskId"])
                require(original_fact["data"]["initialState"] == "DRAFT" and original_fact["data"]["title"] == "Browser seed draft",
                        "The persisted created fact was rewritten to fake snapshot state")
                passed(snapshotStatus=authoritative["status"], originalEventStatus=original_fact["data"]["initialState"],
                       signalTaskId=signal_task["id"], fixtureSQLOnly=True, browserSnapshotReads=reads(a)-quiet_reads)
                screenshot("dioffice-events-authoritative-snapshot")

                begin(CHECKS[7])
                old_source = active_source(a)
                old_facts = sum(fact.get("projectId") == a for fact in evidence()["facts"])
                close_activity()
                page.locator(".project-picker select").select_option(b)
                open_activity()
                wait_state(lambda state: state["activeStreams"].get(a, 0) == 0 and state["activeStreams"].get(b, 0) == 1,
                           "Project switch did not dispose the old real stream")
                require(next(source for source in evidence()["sources"] if source["id"] == old_source["id"])["closed"],
                        "Project switch did not close the old native EventSource")
                b_rows = timeline_matches(b)
                snapshot_matches(b)
                missed_task, _ = create(a, "Browser inactive-project HTTP draft", "browser-inactive-project")
                stable(lambda: page.evaluate(TIMELINE) == b_rows and
                       sum(fact.get("projectId") == a for fact in evidence()["facts"]) == old_facts,
                       "Disposed project scope still delivered or polluted active Activity")
                passed(oldProjectStreamClosed=True, inactiveProjectTaskId=missed_task["id"],
                       activeProjectEvents=len(b_rows), crossProjectDelivery=False)

                begin(CHECKS[8])
                b_task, _ = create(b, "Browser scoped B HTTP draft", "browser-scoped-b")
                b_rows = timeline_matches(b)
                snapshot_matches(b)
                b_event = next(event for event in recent(b)["items"] if event["taskId"] == b_task["id"])
                require(any(fact.get("eventId") == b_event["eventId"] and fact.get("projectId") == b
                            for fact in evidence()["facts"]), "Switched project did not receive its own real SSE event")
                require(not ({row["eventId"] for row in b_rows} & {event["eventId"] for event in db_state(a)["events"]}),
                        "Project A event IDs leaked into project B Activity")
                passed(projectBTaskId=b_task["id"], events=len(b_rows), mixedProjectIds=False)
                screenshot("dioffice-events-project-scope")

                begin(CHECKS[9])
                b_source = active_source(b)
                close_activity()
                page.locator(".project-picker select").select_option(a)
                open_activity()
                rows = timeline_matches(a)
                snapshot_matches(a)
                wait_state(lambda state: state["activeStreams"].get(b, 0) == 0, "Switch-back left project B stream active")
                require(next(source for source in evidence()["sources"] if source["id"] == b_source["id"])["closed"],
                        "Switch-back did not close project B native EventSource")
                missed_event = next(event for event in recent(a)["items"] if event["taskId"] == missed_task["id"])
                require(missed_event["eventId"] in {row["eventId"] for row in rows}, "Switch-back lost inactive-project persisted history")
                passed(events=len(rows), recoveredInactiveEventId=missed_event["eventId"], projectBStreamClosed=True)

                begin(CHECKS[10])
                pending_employee_reads = []

                def pause_employee_read(route):
                    pending_employee_reads.append(route)

                page.route("**/api/v1/employees", pause_employee_read)
                refresh_signal, _ = create(a, "Browser pending employee refresh", "browser-employee-refresh")
                timeline_matches(a)
                wait_for_read = time.monotonic() + timeout() / 1000
                while not pending_employee_reads and time.monotonic() < wait_for_read:
                    page.wait_for_timeout(min(50, timeout()))
                require(pending_employee_reads, "A real live event did not invalidate the employee snapshot")
                expired_cursor = db_state(a)["head"]
                expiry_source = active_source(a)
                cached_rows = page.evaluate(TIMELINE)
                with page.expect_event("requestfailed", predicate=lambda request:
                                       urlparse(request.url).path == "/api/v1/employees", timeout=timeout()):
                    context.set_offline(True)
                    for route in pending_employee_reads:
                        route.continue_()
                page.unroute("**/api/v1/employees", pause_employee_read)
                page.wait_for_function("() => document.querySelector('.workspace-unavailable') || document.querySelector('.employee-refresh-warning')", timeout=timeout())
                require(page.locator(".workspace-unavailable").count() == 0,
                        "Employee refresh failure disposed an already-loaded workspace")
                require(page.get_by_role("dialog").locator(".employee-refresh-warning").count() == 1,
                        "Failed employee refresh was not marked as an unconfirmed snapshot")
                require(not next(source for source in evidence()["sources"] if source["id"] == expiry_source["id"])["closed"],
                        "Transient employee refresh failure closed the project EventSource")
                require(page.evaluate(TIMELINE) == cached_rows, "Failed employee refresh discarded committed history")
                with page.expect_event("requestfailed", predicate=lambda request:
                                       urlparse(request.url).path == "/api/v1/employees", timeout=timeout()):
                    page.get_by_role("dialog").get_by_role("button", name="Refresh employee", exact=True).click(timeout=timeout())
                require(page.locator(".workspace-unavailable").count() == 0
                        and page.evaluate(TIMELINE) == cached_rows
                        and not next(source for source in evidence()["sources"] if source["id"] == expiry_source["id"])["closed"],
                        "Failed manual employee retry disposed the loaded project or its history")
                fixture_post("/fixture/disconnect", {"projectId": a})
                page.locator(".event-connection[data-connection='reconnecting']").first.wait_for(timeout=timeout())
                wait_state(lambda state: state["activeStreams"].get(a, 0) == 0, "Expiry fixture stream did not stop")
                for index in (1, 2):
                    create(a, f"Browser expiry committed draft {index}", f"browser-expiry-{index}")
                expiry = fixture_post("/fixture/expire-cursor", {"projectId": a, "after": expired_cursor})
                require(expired_cursor < expiry["retainedMinimum"] - 1, "Fixture did not create a real retained-history gap")
                expired = api_get(f"/api/v1/projects/{a}/events?after={expired_cursor}&limit=100", status=410)
                require(expired.get("error") == "event_cursor_expired"
                        and len(expired["snapshot"]["items"]) == 1
                        and expired["snapshot"]["items"][0]["projectId"] == a,
                        "Real cursor expiry did not return an authorized retained snapshot")
                require(page.evaluate(TIMELINE) == cached_rows, "Offline expiry fixture mutated browser history directly")
                context.set_offline(False)
                page.get_by_role("dialog").locator(".activity-warning").filter(has_text="Cursor lama tidak dapat direplay").wait_for(timeout=timeout(15000))
                live()
                page.get_by_role("dialog").locator(".employee-refresh-warning").wait_for(state="hidden", timeout=timeout())
                rows = timeline_matches(a)
                snapshot = snapshot_matches(a)
                require(len(rows) == 1 and len(snapshot) > len(rows), "Cursor recovery confused history retention with the task snapshot")
                require(any(row["projectId"] == a and row["resource"] == "events" and row["status"] == 410
                            for row in report["pageResponses"]), "Browser did not diagnose real HTTP 410 before resync")
                require(any(stream["projectId"] == a and stream["status"] == 410
                            for stream in fixture_get()["streams"]), "Native reconnect did not hit the real expired SSE cursor")
                require(next(source for source in evidence()["sources"] if source["id"] == expiry_source["id"])["closed"]
                        and active_source(a)["after"] == str(expiry["head"]),
                        "Expiry recovery did not replace the old source with the latest authorized cursor")
                passed(expiredCursor=str(expired_cursor), retainedMinimum=expiry["retainedMinimum"],
                       recoveredCursor=str(expiry["head"]), httpStatus=410, retainedEvents=len(rows), tasks=len(snapshot),
                       fixtureRetentionDeletion=True, queuedEmployeeReadFailedWhileOffline=True,
                       employeeSnapshotWarningRecovered=True, manualEmployeeRetryAttempted=True,
                       refreshSignalTaskId=refresh_signal["id"],
                       requestTimingControl="real employee GET paused until browser offline; no response fabricated")
                screenshot("dioffice-events-cursor-expiry")

                begin(CHECKS[11])
                current_source = active_source(a)
                before_logout = db_state(a)
                close_activity()
                with page.expect_response(lambda response: urlparse(response.url).path == "/api/v1/auth/logout", timeout=timeout()) as logout_response:
                    page.get_by_role("button", name="Sign out", exact=True).click(timeout=timeout())
                require(logout_response.value.status == 200, "Real HTTP logout failed")
                page.get_by_role("heading", name="Welcome back", exact=True).wait_for(timeout=timeout())
                wait_state(lambda state: not state["activeStreams"], "Logout left a real SSE subscription active")
                require(page.get_by_role("dialog").count() == 0 and page.locator(".event-timeline").count() == 0,
                        "Logout left Activity/dialog visible")
                require(next(source for source in evidence()["sources"] if source["id"] == current_source["id"])["closed"],
                        "Logout did not close the native EventSource")
                revoked = api_get(f"/api/v1/projects/{a}/events", status=401)
                rejected, _ = create(a, "Browser revoked-session write", "browser-revoked", status=401)
                require(revoked.get("error") == "unauthorized" and rejected.get("error") == "unauthorized"
                        and db_state(a) == before_logout, "Revoked session could still read or mutate persisted project data")
                stream_count = len(fixture_get()["streams"])
                stable(lambda: len(fixture_get()["streams"]) == stream_count,
                       "Signed-out Activity reopened a stream", 1300)
                passed(logoutStatus=200, revokedHistoryStatus=401, revokedCreateStatus=401,
                       activeStreams=0, activityClosed=True)
                screenshot("dioffice-events-logout")

                begin(CHECKS[12])
                require(not page_errors, "JavaScript page errors occurred", errorCount=len(page_errors), errorTypes=page_errors)
                require(not runtime_requests and start_disabled, "Unexpected runtime/Start/GitHub request occurred",
                        unexpectedPaths=runtime_requests)
                require(len(report["checks"]) == len(CHECKS) - 1, "Browser acceptance checklist was incomplete")
                passed(pageErrorCount=0, runtimeRequests=0, startDisabled=True, mockedResponses=False)
                report["passed"] = True
            except Exception as error:
                # Never print Playwright APIRequest error strings: they can contain
                # request headers/cookies. Only our own curated assertions expose details.
                location = next((frame for frame in reversed(traceback.extract_tb(error.__traceback__))
                                 if Path(frame.filename).resolve() == Path(__file__).resolve()), None)
                report["checks"].append({"name": stage, "passed": False,
                    "reason": error.reason if isinstance(error, CheckFailure) else f"{type(error).__name__} during {stage}",
                    "details": error.details if isinstance(error, CheckFailure) else {},
                    "runnerLine": location.lineno if location else None,
                    "durationMs": round((time.monotonic() - phase_start) * 1000)})
                if page is not None:
                    try:
                        report["failureEvidence"] = {"timeline": page.evaluate(TIMELINE),
                            "connections": page.locator(".event-connection").evaluate_all("els => els.map(el => el.dataset.connection)"),
                            "taskCards": page.evaluate(TASK_CARDS)}
                        screenshot("dioffice-events-failure")
                    except Exception:
                        report["failureEvidenceUnavailable"] = True
            finally:
                if page is not None:
                    try:
                        native_evidence = evidence()
                    except Exception:
                        pass
                if context is not None:
                    context.close()
                if api_request is not None:
                    api_request.dispose()
                if fixture_request is not None:
                    fixture_request.dispose()
                report["cleanup"]["requestContextsDisposed"] = True
                if browser is not None:
                    browser.close()
                    report["cleanup"]["browserClosed"] = not browser.is_connected()
    except Exception as error:
        report["startupOrCleanupFailure"] = type(error).__name__
        report["passed"] = False
    report["nativeEventSourceEvidence"] = native_evidence
    report["skipped"] = [name for name in CHECKS if name not in {check["name"] for check in report["checks"]}]
    report["durationMs"] = round((time.monotonic() - start) * 1000)
    report["checkCount"] = len(report["checks"])
    report["passedCheckCount"] = sum(check["passed"] for check in report["checks"])
    report["passed"] = report["passed"] and not report["skipped"] and report["passedCheckCount"] == len(CHECKS)
    report_path.write_text(json.dumps(report, indent=2), encoding="utf-8")
    ui_checks = report.get("uiQA", {}).get("checks", [])
    print(json.dumps({"passed": report["passed"], "passedChecks": report["passedCheckCount"],
                      "checks": report["checkCount"], "uiChecks": len(ui_checks),
                      "passedUiChecks": sum(check["passed"] for check in ui_checks),
                      "skipped": report["skipped"], "report": str(report_path)}))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
