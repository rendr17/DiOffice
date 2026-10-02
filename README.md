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
- Execution isolation: Docker + git worktree (`tools/execution-spike/README.md` documents an experimental command-check smoke test; not production-ready)
- Realtime: REST commands + project-scoped SSE with durable replay cursor
- Workflow durability: Temporal
- Object storage: S3-compatible; S3Mock hanya untuk local integration test (bukan production storage)

## Project foundation

Monorepo scaffold saat ini:

- `apps/api` — Go HTTP API, liveness endpoint, dan test dasar.
- `apps/web` — React + TypeScript + Vite shell; belum menampilkan task atau aktivitas palsu.
- `apps/agent-gateway` — Node.js + TypeScript health endpoint; belum memanggil OpenCode.
- `db/migrations` — tempat migration SQL; model database saat ini masih logical, belum executable DDL.
- `tools/execution-spike` — eksperimen Docker lokal, belum worker production.

### Local development

Prerequisites: Go 1.24+, Node.js 22.12+, pnpm 10.34.6, dan Docker Compose.

```bash
pnpm install
cp .env.example .env          # PowerShell: Copy-Item .env.example .env
docker compose up -d postgres temporal s3mock
pnpm dev                       # Web :5173 + Agent Gateway :4100
```

Jalankan API di terminal lain:

```bash
cd apps/api
go run ./cmd/api               # API :8080, bind ke localhost
```

Health endpoints: `http://127.0.0.1:8080/healthz` dan `http://127.0.0.1:4100/healthz`.
Compose menjalankan PostgreSQL di `127.0.0.1:15432`, Temporal di `127.0.0.1:7233`, dan S3Mock di `127.0.0.1:9090`; sesuaikan `POSTGRES_HOST_PORT`/`S3_HOST_PORT` jika port bentrok. S3Mock hanya mengimplementasikan subset S3 untuk test lokal, bukan storage production. Environment Compose hanya untuk development; jangan pakai default-nya di production. Semua port infra bind ke loopback. Belum ada auth, persistence domain, task execution, atau integrasi OpenCode di scaffold ini.

### Local checks

```bash
pnpm test
pnpm typecheck
pnpm build
cd apps/api && go test ./...
```

`.github/workflows/ci.yml` menjalankan check Go, JavaScript workspace, contract validation, dan config Compose.

## Prinsip utama

DiOffice tidak boleh mensimulasikan pekerjaan yang sebenarnya tidak terjadi. Animasi karakter, status, monitor, diff, test, dan progress harus berasal dari state/event nyata dari task dan runtime.
