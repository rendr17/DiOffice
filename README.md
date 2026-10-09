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

Monorepo status saat ini:

- `apps/api` — Go API dengan PostgreSQL readiness, bootstrap Owner, login/session/CSRF, directory, draft/private reference-image endpoints, explicit DRAFT → BACKLOG transition, serta persisted project history/SSE replay.
- `apps/web` — React + TypeScript + Vite pixel studio; task desk dapat menyimpan DRAFT ke BACKLOG secara eksplisit, sementara task/workstation Activity membaca API/event nyata.
- `apps/agent-gateway` — Node.js + TypeScript liveness/preflight OpenCode serta local folder opener berbasis project UUID allowlist; belum membuat sesi atau menjalankan task.
- `db/migrations` — executable identity/project, Owner-password, task/event/outbox, dan execution-layer migrations (workspaces, attempts, sessions, approvals, pull requests, artifacts, checks verification).
- `tools/execution-spike` — eksperimen Docker lokal, belum worker production.

### Local development

Prerequisites: Go 1.24+, Node.js 22.12+, pnpm 10.34.6, dan Docker Compose.

```bash
pnpm install
cp .env.example .env          # PowerShell: Copy-Item .env.example .env
cp apps/web/.env.example apps/web/.env # optional browser API URL override
docker compose up -d postgres temporal s3mock
```

Jalankan migration, buat Owner, lalu start API dari `apps/api`. Password bootstrap dibaca via prompt tersembunyi; jangan masukkan password sebagai flag atau commit `.env`.

```bash
cd apps/api
set -a
source ../../.env
set +a
go run ./cmd/migrate
go run ./cmd/bootstrap-owner --organization "Local DiOffice" --project "Manual test" --email "owner@example.invalid"
export COOKIE_SECURE=false     # only for local HTTP development
go run ./cmd/api               # API :8080, bind ke localhost
```

Di terminal terpisah, load `.env` lalu jalankan `pnpm dev`: `set -a; source .env; set +a; pnpm dev`. Buka `http://127.0.0.1:5173` (gunakan alamat IP yang dicetak Vite). Sign in memakai organization ID dari output bootstrap, email Owner, dan password yang tadi dimasukkan. Browser dev memakai same-origin `/api` melalui proxy Vite; target default `http://127.0.0.1:8080` dan dapat diganti lewat `DIOFFICE_API_PROXY_TARGET`. `VITE_API_BASE_URL` hanya diperlukan bila ingin memanggil API secara langsung. `WEB_ORIGIN` harus sama persis dengan origin browser. `COOKIE_SECURE=false` hanya untuk HTTP loopback local; deployed environments must use HTTPS and secure cookies. Compose menjalankan PostgreSQL di `127.0.0.1:15432`, Temporal di `127.0.0.1:7233`, dan S3Mock di `127.0.0.1:9090`; sesuaikan `POSTGRES_HOST_PORT`/`S3_HOST_PORT` jika port bentrok. S3Mock hanya test double, bukan storage production. Environment Compose hanya development; jangan pakai default-nya di production. Semua port infra bind ke loopback.

#### Open project folder (local only)

Untuk mengaktifkan tombol **Explorer / VS Code**, isi `.env` lokal dengan token acak minimal 32 karakter (misalnya hasil `openssl rand -hex 32`) pada `AGENT_GATEWAY_INTERNAL_TOKEN`, lalu petakan project UUID ke absolute folder path pada `AGENT_GATEWAY_PROJECT_FOLDERS`, misalnya `'{"<project-uuid>":"C:/absolute/path/to/repository"}'`. UUID bisa diambil dari bootstrap/API. API dan gateway harus menerima token yang sama; API serta Vite/gateway dijalankan pada mesin yang sama. Folder path hanya dibaca gateway lokal, tidak disimpan di database atau dikirim ke browser. Jangan commit `.env` atau mapping path. Tanpa token/mapping yang valid, tombol tampil nonaktif. VS Code memerlukan CLI `code` di `PATH`.

Endpoint contract ada di [`docs/API.md`](docs/API.md). Browser dapat login, memilih project dan employee, membuka folder project lokal bila allowlist tersedia, membuat `DRAFT`, memindahkan task ke `BACKLOG`/`READY` lewat command idempotent/version-guarded, menghubungkan repository GitHub (registrasi metadata saja), dan menekan Start eksplisit yang mencatat execution attempt + workspace `PROVISIONING` dalam satu transaksi—tanpa memulai worker. Proses provisioner terpisah (`cmd/provisioner`) meng-claim attempt `CREATED`, membuat mirror clone + branch `task/{taskId}` + git worktree terisolasi, memvalidasi `.dioffice/execution.json` beserta digest-nya, lalu menandai workspace `READY` atau memblokir task dengan reason code aman. Session runner (`cmd/session-runner`) lalu meng-claim workspace `READY`, memverifikasi ulang digest manifest, membuat session OpenCode yang di-scope ke worktree lewat `OPENCODE_SERVER_URL` (loopback), mengirim instruksi awal, dan memindahkan attempt `RUNNING` / workspace `IN_USE` / task `IN_PROGRESS`. Event ingester (`cmd/event-ingester`) membuka SSE `GET /event` per session `RUNNING`, menormalisasi aktivitas provider menjadi fakta durable (`agent.message`, `file.activity`, `command.*`, `session.completed`/`session.failed`) dengan dedupe key unik sehingga replay/restart tidak menduplikasi, dan memindahkan session/attempt/task ke terminal state pada kegagalan provider. Checks runner (`cmd/checks-runner`) lalu meng-claim attempt `RUNNING` yang sesinya `COMPLETED`, mem-verifikasi ulang digest manifest di worktree (agent bisa saja mengubahnya), meng-commit residual working tree agar evidence terikat ke SHA immutable, menjalankan semua `commands.checks` sebagai argv process (bukan shell) dengan env allowlist + output tail terbatas sebagai artifact `log_archive`, dan memindahkan attempt ke `SUCCEEDED` bila semua check required lulus untuk `candidate_sha` — atau `FAILED` + task `FAILED` bila ada yang gagal. Owner juga dapat Retry (`BLOCKED`/`FAILED` → `READY`) dan Cancel (state canonical lain → `CANCELED`) — keduanya mengquiesce execution layer (sesi/attempt `CANCELED`, workspace `CLEANUP_PENDING`, approval pending di-resolve) dalam satu transaksi, dengan abort provider best-effort pasca-commit. PR runner (`cmd/pr-runner`) lalu meng-claim attempt `SUCCEEDED`, me-push `candidate_sha` persis (bukan branch tip) ke `task/{taskId}` di GitHub dengan token lewat `http.extraheader` (tidak pernah di URL/log), membuka atau mengadopsi pull request, menyimpan row `pull_requests` dengan head/base SHA dari respons API, dan memindahkan task ke `IN_REVIEW` dalam satu transaksi — retry terbatas lalu `BLOCKED` bila publikasi gagal terus. Kredensial GitHub di-resolve lewat `internal/secrets` (env `GITHUB_TOKEN` untuk lokal; resolver secrets-manager untuk production). Owner lalu me-review evidence PR (pane inspector menampilkan nomor, URL, branch, head/base SHA persis dari row `pull_requests`) dan meng-approve SHA yang ditampilkan lewat `POST .../tasks/{id}/approve` — backend memverifikasi ulang head PR, `candidate_sha` attempt `SUCCEEDED`, dan repository linkage sebelum mencatat row `approvals` single-use (digest `review:{sha}`, policy version, attempt terikat) + memindahkan task ke `DONE` dalam satu transaksi; replay idempoten, head mismatch/state salah ditolak eksplisit. Owner dapat juga mengirim permintaan perubahan (`POST .../request-changes`, reason wajib) — task kembali `IN_PROGRESS`, attempt baru memakai worktree+branch yang sama, dan feedback masuk prompt Deni. PR reconciler (`cmd/pr-reconciler`) me-repoll PR tercatat via GitHub API: head berubah pasca-approval mengembalikan task ke `IN_REVIEW` (approval lama stale via digest binding) dan PR closed/merged/hilang memindahkan review ke `BLOCKED` — semua dalam transaksi dengan dedupe key sehingga scan berulang tak menduplikasi fakta. Merge adalah aksi Owner terpisah (`POST .../merge`): backend memverifikasi approval `review_approve` non-stale terikat head tercatat lalu memanggil `PUT /pulls/{n}/merge` dengan precondition SHA GitHub sendiri; sukses menandai PR `MERGED` + `merged_at`, mencatat approval `merge` (digest `merge:{sha}`), dan emit `pull_request.updated` — task tetap `DONE`. Preview screenshot dan outbox publisher belum diimplementasikan; webhook GitHub belum ada (reconciler polling menggantikan sementara). Provider runtime kini punya registry — Owner mengonfigurasi provider lewat pane Providers (HUD) atau `GET`/`PUT /api/v1/providers`; credential hanya menyimpan nama env var, bukan secret. Session runner me-resolve runtime attempt via `provider_configs` (`OPENCODE_SERVER_URL` tetap fallback dev); provider terdaftar tapi belum punya adapter (codex, claude) gagal eksplisit dengan `provider_not_implemented`. SSE membaca committed PostgreSQL events, bukan outbox publisher atau worker. Migrations tetap eksplisit—API tidak mengubah schema saat startup.

### Development auth bypass (local-only)

Untuk melewati form login selama development, opt in pada **dua** proses: `DEV_AUTH_BYPASS=true` di API dan `VITE_DEV_AUTH_BYPASS=true` di Vite (`pnpm dev` saja; production build tetap menyembunyikan bypass). Setelah halaman lokal dibuka tanpa sesi, sign-in dimulai otomatis; tombol retry hanya muncul jika startup gagal. API menolak flag ini jika bind/origin bukan literal IP loopback atau `COOKIE_SECURE` bukan `false`. Bypass membuat/menggunakan Local Development Owner passwordless, org, project, dan Deni; API tetap memakai session cookie server-side dan CSRF. Akun tersebut bukan untuk password login—untuk akun normal jalankan `bootstrap-owner`. Jangan expose port atau aktifkan bypass di deployment.

### Pixel studio UI

UI saat ini memakai dua layer sesuai `docs/DESIGN_SYSTEM.md`: office pixel-art/RPG dan application panels yang tetap mudah dibaca. Arah visual MapleStory-inspired menggunakan aset starter CC0 2dPig serta room/icon original DiOffice—bukan aset proprietary MapleStory.

- **Office:** employee terpilih, status backend, furniture, dan komputer yang bisa dibuka.
- **Task board:** snapshot task project, filter status, refresh, detail, dan aksi eksplisit DRAFT → BACKLOG → READY → PROVISIONING. READY mensyaratkan repository terhubung + digest manifest tercatat; Start mencatat attempt/workspace, provisioner mematerialisasikan worktree, session runner memulai session OpenCode; task menjadi `IN_PROGRESS`.
- **Team:** identitas employee persisten dan shortcut ke workstation/composer.
- **Composer:** satu instruksi chat, preview/remove lampiran lokal, dan Save draft melalui API existing. Save tidak memulai agent.
- **Workstation Activity:** history employee-scoped dari maksimal 100 event project terbaru, live SSE, replay/dedupe, dan status reconnect/error yang eksplisit. Event memicu refetch task/employee snapshots; bukan mengganti state backend dengan simulasi.
- Start hanya aktif untuk task `READY`; task detail menampilkan status repository, input digest manifest `.dioffice/execution.json`, dan diagnostik `task_incomplete`. Writer produk menghasilkan `task.created`, `task.updated`, `task.state_changed`, `execution_attempt.state_changed`, `workspace.state_changed`, `session.started`, dan `session.failed`—belum aktivitas file/check/preview dari runtime.
- Initial directory/task load gagal ditampilkan sebagai error dengan retry dan count unknown, bukan kantor/list kosong. Refresh employee yang gagal mempertahankan snapshot terakhir dengan warning belum terkonfirmasi dan retry terpisah; Activity/SSE tidak ditutup oleh error jaringan tersebut. Timeline tidak menampilkan raw payload instruksi/terminal; history yang direset atau dibatasi ditandai.

Browser smoke untuk server lokal yang sudah berjalan dengan development auth bypass dan minimal satu employee:

```bash
uv run --with playwright python tools/ui-smoke/check-office.py --base-url http://127.0.0.1:5174
```

Runner memakai browser **Edge yang terpasang**; gunakan `--channel chrome` jika memakai Chrome. Ini bukan dependency production. Report JSON/screenshot disimpan di Hermes scratch (atau `--output` yang diberikan). Runner tidak submit task, memulai agent, menyimpan cookie/credential, atau mengubah project; skenario error memakai interception read response di browser isolated. Dev-session login tetap membuat/menggunakan sesi development biasa. Source/provenance art ada di `apps/web/public/art/README.md`.

History/live SSE/reconnect memiliki E2E opt-in terpisah dengan real Go HTTP API, PostgreSQL schema disposable, serta Vite/Edge pada port ephemeral. Runner memverifikasi replay/dedupe, authoritative snapshot refresh, queued employee-read failure saat offline, project disposal, real cursor expiry 410, dan logout. Public row fingerprints dan seluruh cleanup runtime/schema diperiksa setelah setiap run. Command, prerequisites, dan batasan ada di [`tools/ui-smoke/README.md`](tools/ui-smoke/README.md); ini bukan tes runtime OpenCode atau Start.

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
