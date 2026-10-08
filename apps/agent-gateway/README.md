# Agent gateway (local development)

The gateway binds to `127.0.0.1:4100` by default. It exposes `/healthz` for liveness and `/readyz` for the OpenCode preflight. It does not create sessions or execute tasks.

## Local project-folder bridge

The authenticated API calls the internal bridge routes; browsers must call the API, never the gateway directly:

- `GET /internal/projects/{projectUUID}/folder` reports only whether a configured folder exists.
- `POST /internal/projects/{projectUUID}/open-folder` accepts only `{"editor":"explorer"}` or `{"editor":"vscode"}`.

Set `AGENT_GATEWAY_INTERNAL_TOKEN` to the same random value (minimum 32 characters) in the API and gateway environments. Configure `AGENT_GATEWAY_PROJECT_FOLDERS` only for the local gateway as a JSON object mapping project UUIDs to absolute directory paths. Example shape: `{"<project-uuid>":"C:/absolute/path/to/repository"}`. Do not commit real paths or tokens.

The gateway rejects calls without the internal bearer token, resolves only an explicit project mapping, confirms the path is a directory, and does not return the path. It starts a fixed platform file manager or the `code` CLI using argument arrays with `shell: false`; no path or executable name is accepted from the request. `code` must be on `PATH` for the VS Code option. This local-only action opens a folder; it does not read files or run a task.

Run the gateway tests with `pnpm --filter @dioffice/agent-gateway test` and typecheck with `pnpm --filter @dioffice/agent-gateway typecheck`.
