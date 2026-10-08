# Browser verification

## Persisted history/SSE — isolated real E2E

`check-events.py` must run through `TestEventBrowserPersistedHistoryAndSSE`, never against a shared/public API. The Go test owns a disposable PostgreSQL schema, the real HTTP router, a separate test-only fixture controller, and a Vite process on ephemeral loopback ports. It does not use a user browser profile, stop user processes, or change dependency manifests.

Prerequisites:

- Go and installed workspace dependencies (`pnpm install`).
- `MIGRATION_TEST_DATABASE_URL` supplied securely in the process environment for a local/test PostgreSQL database. Do not put credentials in command arguments, artifacts, or source control. The database user must be able to create/drop disposable schemas.
- Node.js, `uv`, and an installed Microsoft Edge Chromium browser. The Go fixture invokes `uv run --with playwright python`; it does not install a production browser dependency. The current fixture uses the runner's `msedge` default.
- An artifact directory outside the repository. On this Windows/Git Bash environment, use a native forward-slash path such as the Hermes scratch directory; on other systems, use the corresponding absolute scratch/cache path.

From `apps/api`, with the database environment already provisioned:

```bash
DIOFFICE_BROWSER_TEST=true \
DIOFFICE_BROWSER_OUTPUT="$TMPDIR/dioffice-events-run1" \
go test ./internal/httpapi -count=1 -v \
  -run '^TestEventBrowserPersistedHistoryAndSSE$' -timeout=5m
```

The default suite skips this browser test unless explicitly opted in. Missing browser/database prerequisites after opting in fail the test rather than silently skipping it. Repeat the command with distinct output directories to retain each run's evidence. The fixture runs **13 read-only game UI checks plus 13 durable event checks** in separate owned browser contexts.

### Acceptance checks

1. Initial Activity equals committed history from the real API/database.
2. Reload preserves stored event history and IDs.
3. A separate real HTTP create appends an SSE fact and invalidates the task snapshot.
4. Form submission creates a real durable `DRAFT` without starting an agent.
5. An idempotent replay allocates no additional fact/sequence; changed input returns a conflict.
6. Independent HTTP writes while the browser is offline replay through the native EventSource with `Last-Event-ID`, without duplicates or replacing the source for an ordinary reconnect.
7. Task cards reflect the authoritative API snapshot, not reconstructed event payloads.
8. Project switching disposes the old subscription and prevents cross-project delivery.
9. The selected second project receives only its own live facts.
10. Switching back reloads history missed while that project was inactive.
11. A queued employee snapshot read failing offline keeps the loaded workspace, cached history, and event scope. The stale employee snapshot is labelled, manual retry is safe, and a real retained-history gap returns HTTP **410** before recovery/refetch clears the warning.
12. Logout closes Activity and the native source, revokes the server session, and prevents further reads/writes.
13. No JavaScript page errors or runtime/Start/GitHub requests occur; Start remains disabled.

No API/SSE response is fabricated **in these 13 durable event journeys**. A native EventSource observer records IDs, opens, errors, and explicit closes. One employee GET is paused then released under actual browser offline mode to reproduce the queued-refresh race deterministically. If Chromium keeps an existing TCP stream alive while offline, the controller disconnects only that test-owned stream; the report labels that intervention. The separate read-only UI QA context uses two explicitly labeled browser-only 503 fault scenarios; see below. `mockedResponsesScope` in the event report makes this boundary explicit.

Two other controller operations are deliberately **fixture-only**, not product APIs: update a disposable seed task without an event to prove snapshot authority, and remove retained event/outbox rows in that schema to exercise expiry. Real task creates still go through the product HTTP endpoint.

### Evidence and cleanup

The browser is bounded to 145 seconds, the fixture to 210 seconds, and the Go command to 5 minutes. Each run writes:

- `dioffice-events-browser.json`: named assertions, native source/fact evidence, safe response metadata, screenshots, and browser/request-context cleanup.
- `dioffice-events-fixture.json`: public row fingerprints unchanged across all public tables, schema absence after drop, closed test HTTP/Vite/controller endpoints, stopped owned children, and zero remaining streams.
- `dioffice-ui-qa.json`: the 13 read-only UI checks, geometry/44 px mobile chrome, default nameplate separation and explicit browser-only fault-injection metadata. UI task/event database state is checked unchanged, and its context/streams are closed before the durable event journeys start.
- PNG screenshots for successful stages or the failure state.

Go reads the browser report back and checks both 13-check groups and cleanup; an exit-zero runner alone is insufficient. Failure messages are curated to avoid printing transport exception headers/cookies. Cookies remain in memory only. Default development ports and existing user tasks are not the fixture target.

This verifies history/replay and snapshot refresh, **not** OpenCode execution, Start/task transitions, Temporal/outbox publishing, GitHub/PR checks, approval, or merge.

## Read-only game UI verification

`check-office.py` exports `verify_studio_ui(page, base_url, output_dir) -> list[dict]`. It reuses a caller-owned sync Playwright page without launching or closing the browser/context; routes/listeners and the original viewport are restored in `finally`. The disposable fixture invokes it on `fixture.webOrigin`, checks all 13 results, verifies unchanged task/event database state and closes its isolated context/streams.

Checks cover original loaded/pixelated three-zone art and advancing Owner idle/walk frames, left-facing mirroring, jump/fall/landing transitions, reduced-motion behavior, authoritative employee/HUD state, route travel with camera/minimap updates, full Owner name, native dialog focus/Escape and stacked task details, keyboard tabs and J/T/C navigation, draft review/local attachment preview without submission, board/team snapshots, empty filters and error/retry states. Four viewports (1440×1000, 768×1024, 390×844, 320×740) check world/HUD separation, workstation-screen/studio-sign clearance, actor/computer nameplate separation, full Owner nameplate bounds, window/page overflow and at least 44×44 px primary mobile chrome. Six screenshots capture world desktop/mobile, workstation, board, composer and Team. Pure navigation unit tests cover platform landing, obstacle collision and route bounds; the browser smoke does not claim full platformer physics.

The UI-only 503 scenarios are marked `browser-only` and `serverMutation=false`; they do not verify server/database outages or durable transport. Outside the fixture, the standalone CLI is only for a known development server. It does not submit business mutations, but ordinary development sign-in can create a local session/identity. Both runners use DOM readiness, not `networkidle`, because SSE stays connected. Credential-safe diagnostics can be checked with `python tools/ui-smoke/test-reporting.py` from the repository root.

## Companion regression checks

From the repository root:

```bash
pnpm run test
pnpm run typecheck
pnpm run build
```

From `apps/api`, with the test database securely provisioned:

```bash
go test ./... -count=1
go vet ./...
go test ./internal/httpapi -count=3 \
  -run '^TestEventStream(Backpressure|Revocation|Terminates|RequestCancellation|Reconnect|Replay|Delivers)' \
  -timeout=4m
```

Reference-image HTTP/storage integration also needs the existing `DIOFFICE_S3_TEST_ENDPOINT`/`DIOFFICE_S3_TEST_BUCKET` configuration. S3Mock is a local test double, not production private storage. Go `-race` requires CGO and a supported C compiler; repeated ordinary tests must not be reported as race-detector coverage. Parse complete JSON test logs, not a truncated terminal head/tail, when reporting totals.
