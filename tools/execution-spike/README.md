# DiOffice execution spike

Local prototype for validating the canonical execution manifest and running `install` plus `checks[]` in a disposable Docker clone. It is a sandbox smoke test, not a production worker.

## What it enforces

- Validates against `docs/schemas/execution-manifest.schema.json`; rejects traversal and Windows-style paths.
- Uses only committed `HEAD`; uncommitted and untracked worktree changes are excluded. It creates a separate clone without hard-linked Git objects, with system/global Git config, hooks, and fsmonitor disabled for the operation.
- Requires an operator-owned profile map outside the repository; every image is selected by a local `sha256:` image ID and is never pulled implicitly.
- Runs argv directly (no host shell) in a non-root container with `--network=none`, read-only root, dropped capabilities, `no-new-privileges`, CPU/memory/PID limits, a 64 KiB log cap, and a disposable clone mount only.
- Forwards only `CI` and `NODE_ENV` when explicitly listed in the manifest. It does not mount host credentials or the Docker socket.
- Stops timed-out workers and removes the temporary clone; enforces `diskGiB` as a periodic workspace-size guard, not a hard filesystem quota.

`networkProfile: package-registries` is intentionally rejected until a real egress allowlist exists. The `start` command and preview configuration are validated but not run; there is no OpenCode invocation, screenshot capture, commit, or PR flow yet.

## Run

```bash
uv run --project tools/execution-spike --python 3.14 python tools/execution-spike/dioffice_spike.py validate --manifest examples/execution-manifest.react-pnpm.json
```

For `run`, create a JSON profile map **outside the source repo**, mapping the canonical profile name to a trusted, locally available image ID:

```json
{"node-22-pnpm-10-playwright":"sha256:<replace-with-image-id>"}
```

Then run:

```bash
uv run --project tools/execution-spike --python 3.14 python tools/execution-spike/dioffice_spike.py run --repo <clean-git-repo> --manifest <execution.json> --profile-map <external-profile-map.json> --work-root <scratch-dir>
```

The map is an operator trust decision. Do not map the production profile to the plain Node image used by the test below.

## Tests

Unit tests:

```bash
uv run --project tools/execution-spike --python 3.14 python -m unittest discover -s tools/execution-spike/tests -v
```

Docker integration tests require a local image ID and a scratch path:

```bash
DIOFFICE_SPIKE_TEST_IMAGE=<local-sha256-image-id> DIOFFICE_SPIKE_TEST_ROOT=<scratch-dir> uv run --project tools/execution-spike --python 3.14 python -m unittest discover -s tools/execution-spike/tests -v
```

The integration fixture uses a plain `node:22-bookworm-slim` image solely to exercise container isolation; it does not provide pnpm or Playwright.
