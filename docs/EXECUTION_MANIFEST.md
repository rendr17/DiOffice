# DiOffice v0.1 — Project Execution Manifest

- **Canonical file in a connected repository:** `.dioffice/execution.json`
- **Schema:** [`schemas/execution-manifest.schema.json`](schemas/execution-manifest.schema.json) (v1.0.0)
- **Example:** [`../examples/execution-manifest.react-pnpm.json`](../examples/execution-manifest.react-pnpm.json)

The execution manifest declares how a supported repository is installed, started, checked, and previewed. It is untrusted project input: validate it, review changes to it, and execute it only inside the platform worker policy. It is not a security policy and cannot grant additional privileges.

## 1. v0.1 contract

Required fields: `manifestVersion`, `workerProfile`, `workingDirectory`, `networkProfile`, `environment`, `commands`, `preview`, and `resources`.

- `manifestVersion` is currently `1`.
- `workerProfile` must be in the operator allowlist. The example profile is `node-22-pnpm-10-playwright`; platform configuration resolves it to an immutable image digest. A repository manifest cannot choose an arbitrary image/tag.
- `workingDirectory` and preview routes are relative to the checked-out repository/workspace. Reject absolute paths, `..`, symlinks escaping the workspace, and paths that resolve outside the task mount.
- Each command is an argv array, executed directly without interpolated shell text. No command string, shell expansion, inline credential, or user-supplied Docker flag is accepted.
- `install` uses the repository lockfile in frozen/immutable mode. Lockfile/dependency changes require Owner review/approval. The initial dependency install is still sandboxed and network-restricted.
- `checks` identify stable check IDs, argv, timeout, and whether each check blocks review. At least one check must be required. Every check result is bound to the exact commit SHA that it tested.
- `preview` starts one local app, checks readiness, then captures only same-worker localhost routes at the listed viewports. v0.1 output is static screenshots/artifacts, not an interactive browser session. Do not follow redirects to external/internal control-plane addresses.
- `environment.passThrough` lists names only. Values are resolved from Owner-controlled project config and policy; no value or secret is stored in this manifest. Deny sensitive names (TOKEN, KEY, SECRET, PASSWORD, CREDENTIAL, PRIVATE) and never pass control-plane credentials.
- `networkProfile` is a request from a fixed enum (`none`, `package-registries`), not an arbitrary host allowlist. The operator maps it to an egress policy; the worker cannot change that policy.
- Reject `environment.passThrough` names matching sensitive patterns or disallowed by Owner-controlled policy, even if the value is not present; the JSON Schema enforces identifier syntax, and policy enforcement must apply before resolving values.
- `resources` are requested ceilings and may only be lowered by project policy. Platform hard caps are 4 vCPU, 8192 MiB memory, 20 GiB writable disk, and 3600 seconds per attempt. Initial MVP defaults are 2 vCPU, 4096 MiB, 10 GiB, and 1800 seconds.

## 2. Security enforcement outside the manifest

The worker/orchestrator must enforce non-root UID, no privileged mode, no Docker socket, dropped Linux capabilities, seccomp/AppArmor or platform equivalent, read-only root filesystem, task-only writable mount, no host paths, no production secrets, operation-scoped short-lived credentials, CPU/memory/PID/disk/time limits, and controlled egress. A manifest cannot override any of these controls. Browser access is restricted to the assigned local preview endpoint; block metadata services, control plane, database, Temporal, object-store admin, and unrelated private networks.

Protected operations are intercepted before execution. Dependency/lockfile changes, destructive actions, permission expansion, and merge use the action-scoped approval state machine; denial/expiry means the operation does not run.

## 3. Example profile

The checked-in JSON example is a template for a React/Vite repository using Node 22, pnpm, and Playwright. Adapt argv to repository scripts; do not treat the example as a built-in worker image or as proof that this repository currently runs.

## 4. Change and validation rules

- Validate syntax, schema, profile allowlist, resolved directories, command policy, resource ceilings, and network profile before enabling Start.
- Manifest edits are shown in the task draft/review and recorded with a content digest. A change while a task is running cannot silently alter that task's execution contract; it applies only to a new, explicitly reviewed attempt.
- Invalid/unsupported manifests disable Start with actionable diagnostics.
- Manifest fields are versioned. Unknown fields are rejected; breaking behavior requires a schema version change and compatibility plan.

## 5. Retention and cleanup

- Stop/remove the attempt container on completion, failure, or cancellation; retry cleanup durably if Docker is unavailable.
- Retain the task worktree/branch and review evidence while the PR is open or a review/revision is pending.
- After PR is merged/closed and task is Done/Canceled, retain the worktree for 7 days by default, then queue cleanup. Failed/blocked tasks retain the workspace for 14 days after the last activity unless the Owner deletes sooner.
- Retain screenshots and sanitized logs for 30 days by default; audit events and task state history follow the organization retention policy and are not removed by workspace cleanup.
- Cleanup records success/failure and removed-resource IDs; cleanup failure remains visible and retryable. Never delete a remote branch or PR as an implicit cleanup action.

## 6. Example validation expectations

A valid manifest has one required check, bounded timeouts/resources, nonempty argv for install/start/checks, a preview port/readiness path, and at least one relative route/viewport. Invalid manifests include shell-string commands, unknown worker profile, absolute/traversal path, secret environment variable name, unrestricted network host, no required check, or resource request over hard cap.
