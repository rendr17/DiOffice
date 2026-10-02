# DiOffice

DiOffice adalah virtual software company tempat Owner mengelola AI employees permanen. Identitas dan riwayat employee tetap; runtime adalah tool yang dapat diganti. v0.1 membuktikan satu workflow dengan Deni (Frontend Engineer) dan OpenCode sebagai runtime pertama.

## Target v0.1

Buktikan satu flow end-to-end nyata:

1. Owner membuat task `DRAFT` untuk Deni; belum ada worker atau runtime.
2. Owner melengkapi kriteria penerimaan dan menyimpan task sebagai `BACKLOG` atau `READY`.
3. Owner menekan Start secara eksplisit; DiOffice membuat workspace terisolasi dan memulai OpenCode.
4. Deni mengerjakan repository nyata; aktivitas tersimpan sebagai event yang dinormalisasi.
5. Checks dan preview dijalankan; evidence terikat pada commit SHA yang diuji.
6. Deni membuat atau memperbarui satu PR; Owner meninjau diff, checks, preview, dan PR.
7. Approve menandai task `DONE` (diterima, belum merge); Merge adalah aksi Owner terpisah.
8. Employee/office menampilkan state backend yang persisten, bukan progress simulasi.

## Dokumen authoritative

- `docs/PRD.md` — canonical product requirements.
- `docs/STATE_MACHINES.md` — canonical states and transitions.
- `docs/EVENT_SCHEMA.md` + `docs/schemas/event-envelope.schema.json` — event contract.
- `docs/EXECUTION_MANIFEST.md` + `docs/schemas/execution-manifest.schema.json` — execution contract.
- `docs/USER_FLOW.md` — journey summary.
- `docs/MVP_SCOPE.md` — P0/P1/P2.
- `docs/DESIGN_SYSTEM.md` — visual system.
- `docs/SYSTEM_ARCHITECTURE.md` — service boundaries.
- `docs/DATABASE_SCHEMA.md` — logical data model draft.
- `docs/AGENT_RUNTIME_SPEC.md` — runtime adapter summary.
- `docs/ASSETS.md` — asset guidance.

`README_PRD.md` and `docs/DiOffice_PRD_MVP_v0.1.md` are superseded historical copies; do not implement from them.

## Stack v0.1

- App: React + TypeScript + Vite
- Pixel office: Phaser
- UI: Tailwind CSS + shadcn/ui
- Backend: Go
- Database: PostgreSQL
- Agent gateway: Node.js + TypeScript
- Runtime pertama: OpenCode
- Browser automation: Playwright
- Execution isolation: Docker + git worktree
- Realtime: REST commands + project-scoped SSE with durable replay cursor
- Workflow durability: Temporal
- Object storage: S3-compatible / MinIO untuk local development

## Prinsip utama

DiOffice tidak boleh mensimulasikan pekerjaan yang sebenarnya tidak terjadi. Animasi karakter, status, monitor, diff, test, dan progress harus berasal dari state/event nyata dari task dan runtime.
