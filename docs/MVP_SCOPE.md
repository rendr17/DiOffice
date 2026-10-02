# DiOffice v0.1 MVP Scope

**Authority:** `PRD.md` is canonical; lifecycle, event, and execution contracts are defined in `STATE_MACHINES.md`, `EVENT_SCHEMA.md`, and `EXECUTION_MANIFEST.md`.

## Objective

Deliver one trustworthy end-to-end workflow with one AI employee before expanding the company simulation.

## P0 — Must ship

### Owner and project
- Single owner workspace
- One active project
- One connected Git repository

### Employee
- Deni as persistent Frontend Engineer
- Employee profile
- Employee current status
- Current task
- FIFO Ready queue (additional tasks do not auto-start)

### Task management
- Create/edit draft, assign to Deni, explicit Start/Retry/Cancel, Send Instruction, Request Changes, Approve, and separate Merge
- Priority, canonical status, Kanban board, and task detail

### Task states
Use the exact canonical task lifecycle in `STATE_MACHINES.md`: `DRAFT`, `BACKLOG`, `READY`, `PROVISIONING`, `IN_PROGRESS`, `WAITING_APPROVAL`, `BLOCKED`, `IN_REVIEW`, `DONE`, `FAILED`, `CANCELED`. Board columns are views/groupings and cannot create a second state taxonomy.

### Execution
- OpenCode runtime integration
- Per-task git branch
- Per-task git worktree
- Per-task Docker workspace
- Terminal execution
- File modification
- Test command execution
- Local application preview
- Git commit
- Pull request creation

### Observability
- Normalized agent events
- Activity timeline
- Terminal output
- File change list
- Git diff
- Browser screenshot/preview state
- Test result
- PR link/state

### Pixel office
- One office room
- Deni character
- Desk
- Computer
- Idle animation/state
- Walk-to-desk behavior
- Working/typing state
- Testing state
- Blocked/attention indicator
- Review-ready state

### Security baseline
- No production secrets in prompts or task workers
- Hardened non-root worker; no Docker socket/privileged mode; task-only mounts
- Task-level branch/worktree isolation and one worker per attempt
- Enforced command/resource/network policy; validate `.dioffice/execution.json`
- Operation-scoped Owner approvals; merge is a separate explicit action

## P1 — Build immediately after core loop is stable

- Codex runtime adapter
- Runtime selection UI
- Runtime escalation suggestion
- Browser research tool for frontend tasks
- Asset generation tool interface
- Better visual preview and screenshot comparison
- Cost/token/runtime usage view

## P2 — Next product milestone

- Nabil — Backend Engineer
- Raka — QA Engineer
- Task dependencies and handoff
- Meetings
- Roadmap
- Company memory
- Runtime router
- Devin adapter

## Explicitly out of scope

- Kubernetes
- Kafka
- Rust runner
- Multi-floor office
- Dozens of employees
- Performance scoring system
- Autonomous organizational hierarchy
- Production deployment automation

## Definition of MVP completeness

MVP is not complete because the office animation looks good. It is complete only when the full task lifecycle executes against a real repository and is visible from DiOffice.
