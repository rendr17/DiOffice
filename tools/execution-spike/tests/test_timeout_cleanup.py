from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "tools" / "execution-spike"))

from dioffice_spike import SpikeError, run_execution


@unittest.skipUnless(
    os.environ.get("DIOFFICE_SPIKE_TEST_IMAGE"), "requires local Docker test image"
)
class DockerTimeoutCleanupTests(unittest.TestCase):
    def test_timed_out_worker_is_stopped_and_ephemeral_files_are_removed(self) -> None:
        scratch_root = os.environ.get("DIOFFICE_SPIKE_TEST_ROOT")
        with tempfile.TemporaryDirectory(dir=scratch_root) as temp_dir:
            root = Path(temp_dir)
            repo = root / "fixture-repo"
            repo.mkdir()
            (repo / "input.txt").write_text("fixture\n", encoding="utf-8")
            self._git(repo, "init", "--initial-branch=main")
            self._git(repo, "config", "core.autocrlf", "false")
            self._git(repo, "add", "input.txt")
            self._git(
                repo,
                "-c",
                "user.name=DiOffice Spike",
                "-c",
                "user.email=spike@example.invalid",
                "commit",
                "-m",
                "fixture",
            )

            manifest = {
                "manifestVersion": 1,
                "workerProfile": "node-22-pnpm-10-playwright",
                "workingDirectory": ".",
                "networkProfile": "none",
                "environment": {"passThrough": []},
                "commands": {
                    "install": {"argv": ["node", "--version"], "timeoutSeconds": 10},
                    "start": {"argv": ["node", "--version"], "timeoutSeconds": 10},
                    "checks": [
                        {
                            "id": "timeout",
                            "name": "Timeout cleanup",
                            "argv": ["node", "-e", "setTimeout(()=>{},30000)"],
                            "required": True,
                            "timeoutSeconds": 3,
                        }
                    ],
                },
                "preview": {
                    "port": 3000,
                    "healthPath": "/",
                    "readinessTimeoutSeconds": 10,
                    "routes": ["/"],
                    "viewports": [{"name": "desktop", "width": 1280, "height": 800}],
                },
                "resources": {
                    "cpu": 1,
                    "memoryMiB": 1024,
                    "diskGiB": 1,
                    "attemptTimeoutSeconds": 60,
                },
            }
            manifest_path = root / "manifest.json"
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            profile_map = root / "worker-profiles.json"
            profile_map.write_text(
                json.dumps(
                    {
                        "node-22-pnpm-10-playwright": os.environ[
                            "DIOFFICE_SPIKE_TEST_IMAGE"
                        ]
                    }
                ),
                encoding="utf-8",
            )
            work_root = root / "runs"
            work_root.mkdir()

            with self.assertRaisesRegex(SpikeError, "timed out"):
                run_execution(repo, manifest_path, profile_map, work_root=work_root)

            self.assertEqual(list(work_root.iterdir()), [])
            manifest["commands"]["checks"][0]["argv"] = [
                "node",
                "-e",
                "process.exit(7)",
            ]
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            failed = run_execution(
                repo, manifest_path, profile_map, work_root=work_root
            )
            self.assertEqual(failed["status"], "checks-failed")
            self.assertEqual(failed["checks"][0]["exitCode"], 7)
            self.assertEqual(list(work_root.iterdir()), [])
            manifest["commands"]["checks"][0]["argv"] = ["node", "--version"]
            manifest["commands"]["checks"][0]["timeoutSeconds"] = 10
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            restarted = run_execution(
                repo, manifest_path, profile_map, work_root=work_root
            )
            self.assertEqual(restarted["status"], "checks-passed")
            self.assertEqual(list(work_root.iterdir()), [])
            manifest["commands"]["checks"][0]["argv"] = [
                "node",
                "-e",
                "process.stdout.write('x'.repeat(100000))",
            ]
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaisesRegex(SpikeError, "64 KiB cap"):
                run_execution(repo, manifest_path, profile_map, work_root=work_root)
            self.assertEqual(list(work_root.iterdir()), [])
            self.assertEqual(self._git(repo, "status", "--porcelain"), "")
            leftovers = subprocess.run(
                [
                    "docker",
                    "ps",
                    "-a",
                    "--filter",
                    "label=com.dioffice.execution-spike=true",
                    "--format",
                    "{{.ID}}",
                ],
                check=True,
                capture_output=True,
                text=True,
            ).stdout.strip()
            self.assertEqual(leftovers, "")

    @staticmethod
    def _git(repo: Path, *args: str) -> str:
        result = subprocess.run(
            ["git", "-C", str(repo), *args],
            check=True,
            capture_output=True,
            text=True,
        )
        return result.stdout.strip()


if __name__ == "__main__":
    unittest.main()
