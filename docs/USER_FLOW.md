# DiOffice v0.1 User Flow

**Authority:** The task transition and employee state machine in `STATE_MACHINES.md` is canonical. This journey is only a user-facing summary; it cannot define alternate states, transitions, or events.

## Current game-first UI journey

This section describes the implemented UI. The end-to-end runtime flow below is the target specification: Start, execution, evidence, review/approval and merge integrations are **not** enabled merely by rendering their windows/tabs.

1. Session and authorized project/employee snapshots load from the API. Show unavailable/retry or unknown counts when reads fail; do not treat failed reads as an empty office.
2. Enter the side-view studio. Explore Main Office, Forest Garden and Workshop Annex across one scrollable local world. The minimap tracks the Owner and current area; the project picker separately switches authenticated project scope. When the local gateway has an explicit folder mapping, use the current-project controls to open it in Explorer or VS Code; the absolute path is not shown in the browser. Employee name/role/status are persisted records, not scripted game statistics.
3. Click the human Owner avatar/world to focus it; left/right arrows walk, up/Space or the Jump control jumps, and clicking/tapping empty terrain sets a local walk target. The camera follows within world bounds; platforms allow top landings and solid props block horizontal movement. This is simple navigation, not full platformer physics, and has no backend mutations or simulated employee work.
4. Click the employee or computer to inspect the same native workstation window. Activity shows committed project events scoped to the employee. Other evidence tabs explicitly show unavailable; Start is disabled.
5. Open **Task board** from the bottom HUD or `J`. Filter the loaded API snapshot; click a task for detail. A `DRAFT` may be explicitly saved to `BACKLOG`; this persists a version-guarded state event but does not start an agent. `BACKLOG` is not yet executable. Escape returns from detail to journal, then from journal to world. The compact quest tracker opens the same task detail/journal, not a second task model.
6. Open **Team** or `T` for alternate employee/workstation/instruction access. No critical task or employee information requires pointer interaction with pixel art.
7. Use **Beri instruksi** or `C`. The HUD quick-instruction Enter/send action opens the composer for review, with the selected employee and editable fields. Only explicit **Save draft** submits the existing REST/multipart task contract; successful HTTP creation stores `DRAFT` and its durable event, not a running agent. The Owner may then use the task detail to save the draft to `BACKLOG`; Start stays disabled.
8. Drag a game window by its title bar to reposition it; **Reset posisi panel** or resizing recenters it. Native dialogs trap focus and restore it on Escape/close. Keyboard shortcuts are inactive while editing fields or using an open dialog.
9. Offline/reconnect retains committed Activity and uses native SSE replay. A failed employee refresh labels the cached snapshot as stale; project switch/logout disposes old streams and tenant facts. No raw credential, private object key or fabricated terminal/test output is shown.

## Primary end-to-end flow

### 1. Owner opens DiOffice

Office state:
- Project loaded
- Deni visible
- Deni status: Idle
- Command bar available

### 2. Owner gives command

Example:

> Deni, buat halaman Settings untuk project ini.

System actions:
- Resolve explicit employee target: Deni
- Create task in `DRAFT`; capture/edit title, description, acceptance criteria, repository, manifest, and required checks
- Save as `BACKLOG` or Owner-confirm as `READY`
- Do not create a branch, workspace, container, session, or runtime cost until the Owner explicitly presses Start

### 3. Owner explicitly starts execution

Task transitions:

```text
READY → PROVISIONING → IN_PROGRESS (only after runtime confirms session start)
```

System creates:
- Task branch
- Git worktree
- Docker workspace
- Agent session

### 4. Deni begins work

Office behavior:
- Deni walks to desk
- Deni sits
- Status becomes Planning/Researching/Coding based on normalized events

Workstation begins streaming:
- Activity
- Terminal
- Files touched
- Diff
- Browser state

### 5. Agent reads and edits repository

Example events:
- file.activity (read/create/modify/delete)
- command.started
- command.output
- command.completed

Office maps these into visual states.

### 6. Frontend preview

Deni starts local app.

Playwright/browser worker:
- Opens localhost
- Captures current screen
- Reports navigation/error state

Workstation Browser tab shows latest preview.

### 7. Tests

Deni runs required checks.

Task can only continue to Review when configured required checks pass or the owner explicitly overrides.

### 8. Git result

Deni creates a commit; required check evidence is tied to that exact commit SHA; then pushes the task branch and idempotently creates/updates one PR.

Task becomes `IN_REVIEW` only after checks pass (or an audited Owner override is recorded) and the PR head matches the checked candidate SHA. Employee state becomes `WAITING_REVIEW`.

### 9. Owner reviews

Owner opens task/workstation and sees:
- Summary
- Diff
- Test result
- Browser preview
- PR

Owner actions:
- Approve
- Request changes
- Send steering message

### 10A. Request changes

Owner message is appended to the active task context.

Task remains/re-enters In Progress.

Deni continues in the same workspace/session where possible.

### 10B. Approve

Owner approves the exact displayed PR head SHA and evidence. Task becomes `DONE` (accepted work; not merged). Merge is a separate explicit Owner action. A later PR head change invalidates approval and returns the task to `IN_REVIEW`.

Deni returns to Idle.

## Employee state mapping

- IDLE -> standing/sitting casually
- PLANNING -> thinking indicator
- RESEARCHING -> browser-focused state
- CODING -> typing at desk
- TESTING -> testing/gear state
- BLOCKED -> attention indicator
- WAITING_APPROVAL -> owner attention indicator
- PREVIEWING -> preview/screenshot state
- WAITING_REVIEW -> review-ready/available state; Owner performs review
- ERROR -> visible failure state until Owner retry/cancel
- DONE -> success state then IDLE

## Error flow

When an attempt fails, task state becomes `FAILED`; the Owner may retry as a new attempt or cancel. A recoverable external/policy/configuration blocker becomes `BLOCKED` and requires Owner/system resolution before retry. Error details remain visible; do not label every runtime failure `BLOCKED`.
