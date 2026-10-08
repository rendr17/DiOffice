# DiOffice Design System v0.1

## Goal

Create one visual language across the productivity application and the pixel office world.

## Design character

- Cozy software studio
- Cute chibi/pixel world
- Serious operational UI
- Dense enough for engineering work
- Playful without becoming toy-like

## Two visual layers

### Application UI
Used for board, task detail, meeting, settings, terminal, diff, browser, and approvals.

### Pixel world
Used for employees, rooms, desks, computer states, movement, and office status.

Both layers must use the same semantic status model.

## Pixel rules

- Logical grid: 16x16 px
- Integer scaling only for primary sprite rendering
- image-rendering: pixelated / nearest-neighbor
- Furniture coordinates snap to logical grid where practical
- Collision and navigation grid must not depend on final art dimensions

## Employee visual contract

Every employee sprite set should eventually support:
- idle
- walk-up
- walk-down
- walk-left
- walk-right
- seated
- typing
- thinking
- talking
- success

MVP may begin with fewer frames as long as the manifest remains compatible.

**Authority:** Use canonical states in `docs/STATE_MACHINES.md`; this design summary cannot define its own lifecycle.

## Semantic states

Use only states from the canonical employee machine: `IDLE`, `PLANNING`, `RESEARCHING`, `CODING`, `TESTING`, `PREVIEWING`, `WAITING_APPROVAL`, `BLOCKED`, `WAITING_REVIEW`, `ERROR`, and transient `DONE` presentation. `MEETING` is later scope. Do not use a separate `REVIEWING` employee state: Owner review is task `IN_REVIEW` and employee `WAITING_REVIEW`.

Color must never be the only status indicator. Pair with text, icon, shape, or animation.

## Core UI components

P0 components:
- Button
- IconButton
- Input
- Textarea
- Select
- Badge
- Tooltip
- Tabs
- Dialog
- Drawer
- Toast
- CommandBar
- TaskCard
- KanbanColumn
- EmployeeCard
- EmployeeStatus
- ActivityRow
- ComputerWindow
- TerminalPanel
- DiffPanel
- BrowserPanel
- ApprovalPanel

## Workstation pattern

Desktop-like window with:
- Employee identity
- Current task
- Runtime
- Status
- Activity tab
- Diff tab
- Terminal tab
- Browser tab
- Tests tab
- Git/PR tab
- Message employee input

## Office interaction pattern

Click employee -> Employee Detail

Click computer -> Workstation

Click status bubble -> related task/status detail

Task state changes should be visible both in React UI and pixel world.

## Design tokens

Create shared tokens for:
- color
- spacing
- radius
- typography
- elevation
- motion duration
- semantic statuses
- pixel scale

Recommended package:

`packages/design-tokens`

Both React and Phaser should consume compatible semantic values rather than duplicating state definitions.

## Accessibility

- Keyboard navigation for application UI
- Visible focus states
- Status communicated by text/icon, not color alone
- Terminal/code panels support readable contrast
- Pixel office is not the only way to access critical information

## Implemented studio shell

The primary signed-in surface is a **side-view RPG world**, not a productivity dashboard with pixel decoration. MapleStory references inform the world/HUD/window interaction language; this is original DiOffice art and UI, not a pixel-identical replica or a complete platform game.

- Composition: viewport-filling side-view world, upper-left route minimap/project picker, upper-right quest tracker, character/computer interactions, lower Owner console/status/command HUD. No permanent SaaS sidebar, marketing hero, or always-open form/card grid.
- World and bottom HUD have separate layout space. A ResizeObserver reserves the HUD's actual border-box height; its minimum height is a separate token to avoid a resize feedback loop. The 1,920×360 logical world contains three adjacent 640×360 areas: Main Office, Forest Garden and Workshop Annex. A camera follows the locally controlled Owner and clamps at world bounds; the minimap marks the Owner's position and current area. Platforms support top-side landings and explicit solid props block horizontal walking. This is simple side-view navigation, not full platformer physics.
- On narrow screens minimap/tracker become compact, and primary chrome/navigation targets stay at least 44×44 px. The Owner nameplate wraps and stays inside the viewport rather than replacing the displayed identity with ellipsis; the full name remains in its title and HUD. Other labels remain responsive and use the same API-backed employee identity.
- Chrome: original pixel-shaped icons, compact bitmap-style system font fallback for title bars/labels, silver beveled frames, amber selected tabs/actions and cyan utility controls. Readable prose uses a local sans-serif fallback rather than Tahoma. Game-specific overrides are in `apps/web/src/studio-game.css`, loaded after `pixel-office.css`; no remote font dependency is required.
- Art: original `studio-world.png`, `studio-world-garden.png` and `studio-world-workshop.png` (640×360 each), `studio-engineer-idle.png` and `studio-owner-idle.png` (40×56), and `studio-owner-walk.png` (four 40×56 frames in a 160×56 strip). See `apps/web/public/art/README.md` and both deterministic Pillow generators for provenance/checksums. Scenes share the `y=277` ground plane. Sprite rendering uses nearest-neighbor and may be fractional at responsive world scale.
- The human Owner avatar moves **only after local input**: focus the world/avatar and use left/right arrows, tap/click empty terrain to walk, and use up/Space or the Jump control to jump. Walking frames animate only while moving; reduced-motion disables the decorative cycle. Position/zone are local navigation only—no API-side position or effect on employee/task state. Employee status remains authoritative from the API.
- Office projects the selected persisted employee; Team is the alternate keyboard/application access path. Bootstrap Deni remains an API record, not a hardcoded character identity. A shared engineer starter sprite does not imply a new employee record.
- Employee/task identity and canonical status labels come from API records. Task filters operate on the loaded project snapshot (up to 100 records), not a claimed workspace-wide total.
- Quest tracker previews loaded task records. HUD displays persisted employee state, snapshot counts and real SSE connection state—not HP, MP, levels, XP, invented quest rewards or simulated agent progress.
- Task board (`J`), Team (`T`) and draft composer (`C`) open native game windows over the world. Shortcuts do not intercept editable fields, modified key combinations or an open modal. The HUD quick instruction opens draft review; it never saves or starts execution by pressing Enter alone.
- Workstation Activity lists persisted PostgreSQL project events filtered to the selected employee/owned tasks. Snapshot status remains a separate REST read; immutable event facts are not rewritten to mimic current task state. Diff, Terminal, Browser, Checks, Git/PR and Approvals remain unavailable. Start stays disabled; no fabricated execution/evidence is added.
- Native dialogs preserve focus trapping, Escape/back and focus restoration, including task detail stacked over the journal. Pointer dragging the title bar is viewport-bounded, uses animation-frame transform updates without rerendering the evidence tree, and has a keyboard-accessible reset button. Resize recenters the window. Evidence tabs support arrow/Home/End navigation; reduced motion disables decorative effects, not direct human manipulation.
- Failed reads show unknown counts and retry UI instead of a successful-empty state. Private reference-image previews are local object URLs; saved images remain behind authenticated API URLs.
- A transient employee refresh failure labels the last known snapshot as stale without closing Activity/SSE. Initial directory failure remains a distinct unavailable-workspace state. Event history stays scoped to the current authenticated project; changing project/logout disposes that scope.

### Reference and verification boundaries

- Public official references: [Quick Start](https://www.nexon.com/maplestory/game/quick-start) and [Explore MapleStory](https://www.nexon.com/maplestory/game/explore-maplestory). Research screenshots are not bundled into the application; no Nexon sprites, logo, UI atlas or maps are redistributed.
- Asset provenance/generation: `apps/web/public/art/README.md`, `tools/art/generate-studio-art.py` and `docs/ASSETS.md`. The older CC0 atlas license remains intact.
- Unit tests cover authoritative identity/HUD, protocol scope, original sprites, local walk/jump, camera/area bounds, platform landing, solid-obstacle collision and bounded window movement. Run the project tests, typecheck and build; a passing build alone does not establish visual similarity.
- Read-only visual/keyboard/responsive/error smoke: `tools/ui-smoke/check-office.py`, with reusable `verify_studio_ui(page, base_url, output_dir)`. Its existing 13 checks include walk-cycle, jump/landing, travel from Main Office to Garden and camera/minimap updates, full Owner-nameplate visibility, 44×44 px mobile controls and existing API-boundary checks. The real fixture runs those UI checks in a separate browser context before the 13 durable-event journeys, verifies unchanged task/event state and closes that context/its streams. Viewports are 1440×1000, 768×1024, 390×844 and 320×740. Browser-only failed-read interception is labeled separately from real API/SSE responses; it must not submit tasks to a user workspace.
- Durable history/offline/native replay/project switch/410/logout verification uses the disposable real HTTP/PostgreSQL fixture and `tools/ui-smoke/check-events.py`. It also checks loaded art, manual Owner input and real native-dialog drag/reset. See `tools/ui-smoke/README.md` for opt-in execution and cleanup guarantees.
