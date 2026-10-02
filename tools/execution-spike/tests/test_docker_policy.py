from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "tools" / "execution-spike"))

from dioffice_spike import (
    SpikeError,
    build_docker_argv,
    collect_passthrough_environment,
)


class DockerPolicyTests(unittest.TestCase):
    def test_worker_command_is_argv_with_hardened_container_flags(self) -> None:
        command = ["node", "-e", "process.stdout.write('ok')"]
        with tempfile.TemporaryDirectory() as temp_dir:
            argv = build_docker_argv(
                image_digest="sha256:" + "a" * 64,
                workspace=Path(temp_dir),
                working_directory=".",
                command=command,
                resources={"cpu": 2, "memoryMiB": 1024},
                cidfile=Path(temp_dir) / "container.cid",
                network_profile="none",
            )

        for required_flag in (
            "--network=none",
            "--read-only",
            "--cap-drop=ALL",
            "--security-opt=no-new-privileges",
            "--pids-limit=128",
            "--memory=1024m",
            "--cpus=2",
            "--pull=never",
            "--label=com.dioffice.execution-spike=true",
        ):
            self.assertIn(required_flag, argv)
        self.assertEqual(argv[-len(command) :], command)
        self.assertNotIn("sh", argv[-len(command) :])
        self.assertNotIn("/var/run/docker.sock", argv)

    def test_package_registry_egress_fails_closed(self) -> None:
        with self.assertRaisesRegex(SpikeError, "package-registries.*not supported"):
            build_docker_argv(
                image_digest="sha256:" + "a" * 64,
                workspace=Path("C:/scratch/worktree"),
                working_directory=".",
                command=["node", "--version"],
                resources={"cpu": 1, "memoryMiB": 512},
                cidfile=Path("C:/scratch/container.cid"),
                network_profile="package-registries",
            )

    def test_only_non_secret_allowlisted_environment_is_forwarded(self) -> None:
        result = collect_passthrough_environment(
            ["CI", "NODE_ENV"],
            {"CI": "true", "NODE_ENV": "test", "GITHUB_TOKEN": "must-not-pass"},
        )
        self.assertEqual({"CI": "true", "NODE_ENV": "test"}, result)
        with self.assertRaisesRegex(SpikeError, "not allowlisted"):
            collect_passthrough_environment(["API_KEY"], {"API_KEY": "secret"})

    def test_mutable_image_tags_are_rejected(self) -> None:
        with self.assertRaisesRegex(SpikeError, "sha256 digest"):
            build_docker_argv(
                image_digest="node:22-bookworm-slim",
                workspace=Path("C:/scratch/worktree"),
                working_directory=".",
                command=["node", "--version"],
                resources={"cpu": 1, "memoryMiB": 512},
                cidfile=Path("C:/scratch/container.cid"),
                network_profile="none",
            )


if __name__ == "__main__":
    unittest.main()
