# DiOffice System Architecture v0.1

**Authority:** Product requirements are in `PRD.md`; lifecycle, event, and execution contracts are in `STATE_MACHINES.md`, `EVENT_SCHEMA.md`, and `EXECUTION_MANIFEST.md`. This file describes service boundaries only.

## Architecture principles

1. Employee identity is separate from runtime provider.
2. Business state is separate from Phaser rendering.
3. Each task executes in an isolated workspace.
4. Runtime-specific events are normalized before reaching the UI.
5. Long-running workflows must survive process restarts.
6. Human approval boundaries are explicit.

## Stack

### Web application
- React
- TypeScript
- Vite
- Tailwind CSS
- shadcn/ui
- Zustand
- TanStack Query
- Phaser
- Monaco Editor
- xterm.js
- dnd-kit

### Backend core
- Go
- net/http + chi
- PostgreSQL
- pgx + sqlc
- Temporal
- REST commands + project-scoped SSE with durable replay cursor

### Agent gateway
- Node.js
- TypeScript
- Runtime adapters

### Runtime v0.1
- OpenCode

### Execution
- Docker
- git worktree
- Playwright

### Storage
- PostgreSQL for structured state
- S3-compatible storage / MinIO for screenshots and artifacts

## High-level topology

User Browser
  -> React App
  -> Go API
  -> PostgreSQL
  -> Temporal
  -> Agent Gateway
  -> OpenCode
  -> Worker Container
  -> Git Worktree
  -> Repository / GitHub

Worker/agent events flow back:

Runtime -> Agent Gateway -> Go event ingestion -> PostgreSQL state/event/outbox transaction -> SSE replay stream -> React + Phaser

## Service responsibilities

### apps/web
- Application shell
- Board
- Employee/task/workstation UI
- Phaser office integration
- Realtime subscriptions

### apps/api
- Authentication/authorization boundary
- Employee and task state
- Project/repository state
- Approvals
- Runtime adapter (OpenCode only in v0.1)
- Event persistence
- Workflow commands

### apps/agent-gateway
- Runtime adapters
- Session lifecycle
- Runtime event parsing
- Event normalization
- Runtime-specific errors

### worker
- Workspace provisioning
- git worktree
- container lifecycle
- local app execution
- Playwright
- resource limits

## Core domain objects

- Organization
- User
- Employee
- Project
- Repository
- Task
- ExecutionAttempt
- Workspace
- AgentSession
- AgentEvent + transactional Outbox
- Approval
- PullRequest
- Artifact

## Canonical state and event contracts

State values and allowed transitions are defined only in `STATE_MACHINES.md`; normalized event names and payloads are defined in `EVENT_SCHEMA.md` and `schemas/event-envelope.schema.json`. This architecture summary does not define parallel enumerations. Phaser receives backend projections only.

## Realtime strategy

Use REST for authenticated Owner commands and project-scoped SSE for one-way persisted activity. Commit event/state/outbox before publishing; resume with `Last-Event-ID` using project `streamSequence`; deduplicate at-least-once delivery; require an authorized snapshot if a cursor expires. WebSocket is not required in v0.1.

Do not expose raw runtime event formats directly to frontend components.

## Workflow orchestration

`STATE_MACHINES.md` defines the canonical transition flow. At a high level: Owner drafts/confirms task → explicit Start → provision isolated workspace and OpenCode attempt → execute/check/preview → push branch and create/update PR → Owner requests changes or approves exact SHA → optional separate Merge. Never treat approval as merge or runtime completion as task completion.

## Repository isolation

One task = one branch + one worktree + one execution workspace.

Example:

- `worktrees/task-001-deni`
- `feature/task-001-settings`

No two concurrent employees should modify the same local checkout.

## Phaser boundary

Phaser is a renderer/interaction layer for office simulation.

Phaser must not own:
- task truth
- runtime session truth
- Git truth
- permissions
- approval state

It receives normalized employee state and emits user interaction events.
