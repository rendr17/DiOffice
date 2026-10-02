from __future__ import annotations

import argparse
import asyncio
import json
import os
import re
import subprocess
import sys
import tempfile
import time
from pathlib import Path, PureWindowsPath
from typing import Any

from jsonschema import Draft202012Validator

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_SCHEMA = REPO_ROOT / "docs" / "schemas" / "execution-manifest.schema.json"


class SpikeError(Exception):
    """Expected validation or execution failure for the spike CLI."""


def _validate_working_directory(value: Any) -> None:
    if not isinstance(value, str) or not value or "\x00" in value:
        raise SpikeError("workingDirectory must be a non-empty relative path")
    if "\\" in value:
        raise SpikeError("workingDirectory must use portable '/' separators")
    windows_path = PureWindowsPath(value)
    parts = re.split(r"/+", value)
    if windows_path.is_absolute() or windows_path.drive or value.startswith("/"):
        raise SpikeError("workingDirectory must be relative")
    if any(part == ".." for part in parts):
        raise SpikeError("workingDirectory cannot traverse outside the repository")


def load_manifest(
    manifest_path: Path | str, schema_path: Path | str | None = None
) -> dict[str, Any]:
    manifest_path = Path(manifest_path)
    schema_path = Path(schema_path) if schema_path else DEFAULT_SCHEMA
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        schema = json.loads(schema_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SpikeError(f"cannot read manifest or schema: {exc}") from exc

    try:
        Draft202012Validator.check_schema(schema)
    except Exception as exc:
        raise SpikeError(f"execution manifest schema is invalid: {exc}") from exc

    errors = sorted(
        Draft202012Validator(schema).iter_errors(manifest),
        key=lambda error: (list(map(str, error.absolute_path)), error.message),
    )
    if errors:
        error = errors[0]
        location = ".".join(map(str, error.absolute_path)) or "$"
        raise SpikeError(f"manifest schema error at {location}: {error.message}")

    _validate_working_directory(manifest["workingDirectory"])
    return manifest


SAFE_ENVIRONMENT_NAMES = {"CI", "NODE_ENV"}


def collect_passthrough_environment(
    names: list[str], host_environment: dict[str, str] | None = None
) -> dict[str, str]:
    requested = set(names)
    unknown = requested - SAFE_ENVIRONMENT_NAMES
    if unknown:
        raise SpikeError(
            f"environment variable(s) not allowlisted: {', '.join(sorted(unknown))}"
        )
    source = host_environment if host_environment is not None else os.environ
    selected: dict[str, str] = {}
    for name in sorted(requested):
        if name not in source:
            continue
        value = source[name]
        if (
            not isinstance(value, str)
            or len(value) > 256
            or any(char in value for char in "\x00\r\n")
        ):
            raise SpikeError(
                f"invalid value for pass-through environment variable {name}"
            )
        selected[name] = value
    return selected


def build_docker_argv(
    *,
    image_digest: str,
    workspace: Path,
    working_directory: str,
    command: list[str],
    resources: dict[str, Any],
    cidfile: Path,
    network_profile: str,
    environment: dict[str, str] | None = None,
) -> list[str]:
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_digest):
        raise SpikeError("worker image must be pinned by a sha256 digest")
    if network_profile != "none":
        raise SpikeError(
            f"networkProfile={network_profile!r} is not supported; refusing execution without an egress allowlist"
        )
    if not command or any(not isinstance(part, str) or not part for part in command):
        raise SpikeError("worker command must be a non-empty argv array")
    selected_environment = collect_passthrough_environment(
        list((environment or {}).keys()), environment or {}
    )

    _validate_working_directory(working_directory)
    root = Path(workspace).resolve(strict=True)
    target = root.joinpath(
        *[part for part in working_directory.split("/") if part not in ("", ".")]
    )
    target = target.resolve(strict=True)
    if not target.is_relative_to(root) or not target.is_dir():
        raise SpikeError("workingDirectory resolves outside the isolated workspace")

    cpu_limit = float(resources["cpu"])
    memory_limit = int(resources["memoryMiB"])
    if not (0 < cpu_limit <= 4) or not (512 <= memory_limit <= 8192):
        raise SpikeError("resource limits are outside the execution-manifest bounds")

    argv = [
        "docker",
        "run",
        "--rm",
        "--pull=never",
        "--init",
        "--label=com.dioffice.execution-spike=true",
        "--network=none",
        "--read-only",
        "--cap-drop=ALL",
        "--security-opt=no-new-privileges",
        "--pids-limit=128",
        f"--memory={memory_limit}m",
        f"--cpus={cpu_limit:g}",
        "--tmpfs",
        "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777",
        "--user=1000:1000",
        "--cidfile",
        str(cidfile),
        "--mount",
        f"type=bind,source={root.as_posix()},target=/workspace",
        "--workdir",
        "/workspace"
        + (
            "/"
            + "/".join(
                part for part in working_directory.split("/") if part not in ("", ".")
            )
            if working_directory != "."
            else ""
        ),
        "--entrypoint=",
    ]
    for name, value in sorted(selected_environment.items()):
        argv.extend(["--env", f"{name}={value}"])
    argv.extend([image_digest, *command])
    return argv


MAX_OUTPUT_BYTES = 64 * 1024


def _run_git(
    repo: Path, args: list[str], env: dict[str, str], timeout: int = 30
) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo), *args],
        stdin=subprocess.DEVNULL,
        capture_output=True,
        text=True,
        timeout=timeout,
        env=env,
        check=False,
    )
    if result.returncode:
        message = result.stderr.strip() or "git command failed"
        raise SpikeError(message)
    return result.stdout.strip()


def _git_environment(run_root: Path) -> tuple[dict[str, str], Path]:
    empty_config = run_root / "empty.gitconfig"
    empty_config.write_text("", encoding="utf-8")
    hooks_dir = run_root / "empty-hooks"
    hooks_dir.mkdir()
    env = {
        "PATH": os.environ.get("PATH", ""),
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_CONFIG_GLOBAL": str(empty_config),
        "GIT_TERMINAL_PROMPT": "0",
        "GIT_CONFIG_COUNT": "3",
        "GIT_CONFIG_KEY_0": "core.hooksPath",
        "GIT_CONFIG_VALUE_0": str(hooks_dir),
        "GIT_CONFIG_KEY_1": "core.fsmonitor",
        "GIT_CONFIG_VALUE_1": "false",
        "GIT_CONFIG_KEY_2": "core.autocrlf",
        "GIT_CONFIG_VALUE_2": "false",
    }
    for name in ("SYSTEMROOT", "SystemRoot", "TEMP", "TMP"):
        if name in os.environ:
            env[name] = os.environ[name]
    return env, hooks_dir


def _source_repository(path: Path, git_env: dict[str, str]) -> tuple[Path, str]:
    source = Path(path).resolve(strict=True)
    if not source.is_dir():
        raise SpikeError("--repo must point to a Git repository directory")
    top = Path(_run_git(source, ["rev-parse", "--show-toplevel"], git_env)).resolve(
        strict=True
    )
    base_sha = _run_git(top, ["rev-parse", "HEAD"], git_env)
    if not re.fullmatch(r"[0-9a-f]{40,64}", base_sha):
        raise SpikeError("could not resolve a valid source commit SHA")
    return top, base_sha


def _load_worker_image(
    profile_map_path: Path, profile_name: str, source_repo: Path
) -> str:
    path = Path(profile_map_path).resolve(strict=True)
    if path.is_relative_to(source_repo):
        raise SpikeError(
            "--profile-map must be outside the untrusted source repository"
        )
    try:
        profiles = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise SpikeError(f"cannot read worker profile map: {exc}") from exc
    if not isinstance(profiles, dict):
        raise SpikeError("worker profile map must be a JSON object")
    image = profiles.get(profile_name)
    if not isinstance(image, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", image):
        raise SpikeError(
            f"worker profile {profile_name!r} must map to a sha256 image ID"
        )
    inspected = subprocess.run(
        ["docker", "image", "inspect", "--format", "{{.Id}}", image],
        stdin=subprocess.DEVNULL,
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    if inspected.returncode or inspected.stdout.strip() != image:
        raise SpikeError(
            "pinned worker image is not present locally; pull/build it explicitly before running"
        )
    return image


def _docker_ready() -> None:
    result = subprocess.run(
        ["docker", "info", "--format", "{{.ServerVersion}}"],
        stdin=subprocess.DEVNULL,
        capture_output=True,
        text=True,
        timeout=20,
        check=False,
    )
    if result.returncode:
        raise SpikeError("Docker daemon is unavailable; start Docker Desktop and retry")


def _workspace_size_bytes(root: Path) -> int:
    total = 0
    pending = [root]
    while pending:
        current = pending.pop()
        try:
            with os.scandir(current) as entries:
                for entry in entries:
                    try:
                        if entry.is_dir(follow_symlinks=False):
                            pending.append(Path(entry.path))
                        elif entry.is_file(follow_symlinks=False):
                            total += entry.stat(follow_symlinks=False).st_size
                    except OSError:
                        continue
        except OSError as exc:
            raise SpikeError("unable to enforce workspace disk limit") from exc
    return total


async def _stop_container(cidfile: Path) -> None:
    try:
        container_id = cidfile.read_text(encoding="ascii").strip()
    except (OSError, UnicodeError):
        return
    if not re.fullmatch(r"[0-9a-f]{12,64}", container_id):
        return
    for args in (
        ["docker", "stop", "--time", "2", container_id],
        ["docker", "rm", "--force", container_id],
    ):
        try:
            process = await asyncio.create_subprocess_exec(
                *args,
                stdin=asyncio.subprocess.DEVNULL,
                stdout=asyncio.subprocess.DEVNULL,
                stderr=asyncio.subprocess.DEVNULL,
            )
            await asyncio.wait_for(process.wait(), timeout=5)
        except (OSError, asyncio.TimeoutError):
            continue


async def _capture_worker(
    argv: list[str],
    cidfile: Path,
    workspace: Path,
    timeout_seconds: float,
    disk_limit_bytes: int,
) -> tuple[int, str]:
    process = await asyncio.create_subprocess_exec(
        *argv,
        stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.STDOUT,
    )
    captured = bytearray()

    async def read_output() -> None:
        assert process.stdout is not None
        while True:
            chunk = await process.stdout.read(4096)
            if not chunk:
                return
            if len(captured) + len(chunk) > MAX_OUTPUT_BYTES:
                raise SpikeError("worker output exceeded the 64 KiB cap")
            captured.extend(chunk)

    async def monitor_disk() -> None:
        while process.returncode is None:
            size = await asyncio.to_thread(_workspace_size_bytes, workspace)
            if size > disk_limit_bytes:
                raise SpikeError("workspace exceeded the manifest diskGiB limit")
            await asyncio.sleep(0.5)

    wait_task = asyncio.create_task(process.wait())
    output_task = asyncio.create_task(read_output())
    disk_task = asyncio.create_task(monitor_disk())
    try:
        await asyncio.wait_for(
            asyncio.gather(wait_task, output_task, disk_task), timeout=timeout_seconds
        )
    except (asyncio.TimeoutError, SpikeError):
        if process.returncode is None:
            process.kill()
            await process.wait()
        await _stop_container(cidfile)
        raise
    except BaseException:
        if process.returncode is None:
            process.kill()
            await process.wait()
        await _stop_container(cidfile)
        raise
    return process.returncode or 0, captured.decode("utf-8", errors="replace")


def _run_worker(
    argv: list[str],
    cidfile: Path,
    workspace: Path,
    timeout: float,
    disk_limit_bytes: int,
) -> tuple[int, str]:
    try:
        return asyncio.run(
            _capture_worker(argv, cidfile, workspace, timeout, disk_limit_bytes)
        )
    except TimeoutError as exc:
        raise SpikeError("worker command timed out; container stop requested") from exc
    except SpikeError:
        raise
    except (OSError, RuntimeError) as exc:
        raise SpikeError(f"could not start Docker worker: {exc}") from exc


def run_execution(
    repo_path: Path | str,
    manifest_path: Path | str,
    profile_map_path: Path | str,
    *,
    schema_path: Path | str | None = None,
    work_root: Path | str | None = None,
) -> dict[str, Any]:
    manifest = load_manifest(manifest_path, schema_path)
    _docker_ready()

    selected_work_root = Path(work_root or tempfile.gettempdir()).resolve()
    selected_work_root.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(
        prefix="dioffice-spike-", dir=selected_work_root
    ) as temp_dir:
        run_root = Path(temp_dir)
        git_env, hooks_dir = _git_environment(run_root)
        source_repo, base_sha = _source_repository(Path(repo_path), git_env)
        image = _load_worker_image(
            Path(profile_map_path), manifest["workerProfile"], source_repo
        )
        clone = run_root / "workspace"
        clone_result = subprocess.run(
            [
                "git",
                "-c",
                f"core.hooksPath={hooks_dir}",
                "clone",
                "--no-local",
                "--no-hardlinks",
                "--quiet",
                "--",
                str(source_repo),
                str(clone),
            ],
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            timeout=120,
            env=git_env,
            check=False,
        )
        if clone_result.returncode:
            raise SpikeError(
                clone_result.stderr.strip()
                or "could not create isolated repository clone"
            )

        workdir = clone
        for part in manifest["workingDirectory"].split("/"):
            if part not in ("", "."):
                workdir = workdir / part
        resolved_workdir = workdir.resolve(strict=True)
        if (
            not resolved_workdir.is_relative_to(clone.resolve(strict=True))
            or not resolved_workdir.is_dir()
        ):
            raise SpikeError(
                "workingDirectory does not resolve to a directory inside the clone"
            )

        pass_environment = collect_passthrough_environment(
            manifest["environment"]["passThrough"]
        )
        deadline = time.monotonic() + manifest["resources"]["attemptTimeoutSeconds"]
        commands: list[dict[str, Any]] = [
            {
                "id": "install",
                "name": "Install",
                "required": True,
                **manifest["commands"]["install"],
            },
            *manifest["commands"]["checks"],
        ]
        results: list[dict[str, Any]] = []
        disk_limit_bytes = int(manifest["resources"]["diskGiB"]) * 1024**3
        for command in commands:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise SpikeError("execution attempt exceeded attemptTimeoutSeconds")
            timeout_seconds = min(float(command["timeoutSeconds"]), remaining)
            cidfile = run_root / f"{command['id']}.cid"
            argv = build_docker_argv(
                image_digest=image,
                workspace=clone,
                working_directory=manifest["workingDirectory"],
                command=command["argv"],
                resources=manifest["resources"],
                cidfile=cidfile,
                network_profile=manifest["networkProfile"],
                environment=pass_environment,
            )
            return_code, output = _run_worker(
                argv, cidfile, clone, timeout_seconds, disk_limit_bytes
            )
            try:
                cidfile.unlink(missing_ok=True)
            except OSError:
                pass
            status = "passed" if return_code == 0 else "failed"
            results.append(
                {
                    "id": command["id"],
                    "name": command["name"],
                    "required": command["required"],
                    "status": status,
                    "exitCode": return_code,
                    "output": output,
                }
            )
            if command["id"] == "install" and return_code != 0:
                break

        required_failed = any(
            item["required"] and item["status"] != "passed" for item in results
        )
        return {
            "status": "checks-failed" if required_failed else "checks-passed",
            "baseSha": base_sha,
            "workerProfile": manifest["workerProfile"],
            "workerImage": image,
            "install": results[0]
            if results and results[0]["id"] == "install"
            else None,
            "checks": [item for item in results if item["id"] != "install"],
            "preview": "not-run",
        }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="dioffice-spike")
    subparsers = parser.add_subparsers(dest="operation", required=True)
    validate_parser = subparsers.add_parser(
        "validate", help="validate an execution manifest"
    )
    validate_parser.add_argument("--manifest", required=True, type=Path)
    validate_parser.add_argument("--schema", type=Path)
    run_parser = subparsers.add_parser(
        "run", help="run install and checks in a disposable Docker clone"
    )
    run_parser.add_argument("--repo", required=True, type=Path)
    run_parser.add_argument("--manifest", required=True, type=Path)
    run_parser.add_argument("--profile-map", required=True, type=Path)
    run_parser.add_argument("--schema", type=Path)
    run_parser.add_argument("--work-root", type=Path)
    args = parser.parse_args(argv)
    try:
        if args.operation == "validate":
            manifest = load_manifest(args.manifest, args.schema)
            result = {
                "status": "valid",
                "workerProfile": manifest["workerProfile"],
                "networkProfile": manifest["networkProfile"],
                "checkCount": len(manifest["commands"]["checks"]),
            }
        else:
            result = run_execution(
                args.repo,
                args.manifest,
                args.profile_map,
                schema_path=args.schema,
                work_root=args.work_root,
            )
        print(json.dumps(result, indent=2))
        return 0 if result.get("status") in ("valid", "checks-passed") else 1
    except SpikeError as exc:
        print(f"dioffice-spike: {exc}", file=sys.stderr)
        return 2
    except (OSError, subprocess.SubprocessError, TimeoutError):
        print(
            "dioffice-spike: local process or filesystem operation failed",
            file=sys.stderr,
        )
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
