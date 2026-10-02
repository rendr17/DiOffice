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
