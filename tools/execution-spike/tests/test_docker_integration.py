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


@unittest.skipUnless(
    os.environ.get("DIOFFICE_SPIKE_TEST_IMAGE"), "requires local Docker test image"
)
class DockerExecutionIntegrationTests(unittest.TestCase):
    def test_checks_run_in_ephemeral_isolated_clone(self) -> None:
        scratch_root = os.environ.get("DIOFFICE_SPIKE_TEST_ROOT")
        with tempfile.TemporaryDirectory(dir=scratch_root) as temp_dir:
            root = Path(temp_dir)
            repo = root / "fixture-repo"
            repo.mkdir()
            (repo / "input.txt").write_text("fixture\n", encoding="utf-8")
            (repo / ".gitattributes").write_text(
                "*.txt filter=repo-local\n", encoding="utf-8"
            )
            self._git(repo, "init", "--initial-branch=main")
            self._git(repo, "config", "core.autocrlf", "false")
            self._git(repo, "config", "filter.repo-local.clean", "cat")
            self._git(
                repo, "config", "filter.repo-local.smudge", "printf injected-content"
            )
            self._git(repo, "add", "input.txt", ".gitattributes")
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
            (repo / "input.txt").write_text("uncommitted\n", encoding="utf-8")
            (repo / "host-only.txt").write_text("host-only\n", encoding="utf-8")

            manifest = {
                "manifestVersion": 1,
                "workerProfile": "node-22-pnpm-10-playwright",
                "workingDirectory": ".",
                "networkProfile": "none",
                "environment": {"passThrough": []},
                "commands": {
                    "install": {
                        "argv": [
                            "node",
                            "-e",
                            "require('fs').writeFileSync('install.marker','present')",
                        ],
                        "timeoutSeconds": 15,
                    },
                    "start": {"argv": ["node", "--version"], "timeoutSeconds": 15},
                    "checks": [
                        {
                            "id": "isolation-smoke",
                            "name": "Container isolation smoke test",
                            "argv": [
                                "node",
                                "-e",
                                "const fs=require('fs'),net=require('net');if(process.getuid()===0)process.exit(41);if(fs.existsSync('/var/run/docker.sock'))process.exit(42);if(process.env.GITHUB_TOKEN)process.exit(43);if(fs.readFileSync('install.marker','utf8')!=='present')process.exit(44);if(!fs.readFileSync('input.txt','utf8').startsWith('fixture'))process.exit(46);if(fs.existsSync('host-only.txt'))process.exit(47);const s=net.createConnection({host:'1.1.1.1',port:80});s.setTimeout(1500);s.on('connect',()=>process.exit(45));s.on('error',()=>{console.log('isolated-ok');process.exit(0)});s.on('timeout',()=>{s.destroy();console.log('isolated-ok');process.exit(0)});",
                            ],
                            "required": True,
                            "timeoutSeconds": 10,
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

            original_secret = os.environ.get("GITHUB_TOKEN")
            os.environ["GITHUB_TOKEN"] = "test-secret-must-not-enter-container"
            try:
                completed = subprocess.run(
                    [
                        sys.executable,
                        str(
                            REPO_ROOT
                            / "tools"
                            / "execution-spike"
                            / "dioffice_spike.py"
                        ),
                        "run",
                        "--repo",
                        str(repo),
                        "--manifest",
                        str(manifest_path),
                        "--profile-map",
                        str(profile_map),
                        "--work-root",
                        str(work_root),
                    ],
                    check=False,
                    capture_output=True,
                    text=True,
                )
                self.assertEqual(
                    completed.returncode, 0, completed.stderr + completed.stdout
                )
                result = json.loads(completed.stdout)
            finally:
                if original_secret is None:
                    os.environ.pop("GITHUB_TOKEN", None)
                else:
                    os.environ["GITHUB_TOKEN"] = original_secret

            self.assertEqual(result["status"], "checks-passed")
            self.assertEqual(result["checks"][0]["status"], "passed")
            self.assertIn("isolated-ok", result["checks"][0]["output"])
            self.assertFalse((repo / "install.marker").exists())
            self.assertEqual(
                (repo / "input.txt").read_text(encoding="utf-8"), "uncommitted\n"
            )
            self.assertEqual(
                (repo / "host-only.txt").read_text(encoding="utf-8"), "host-only\n"
            )
            self.assertNotEqual(self._git(repo, "status", "--porcelain"), "")
            self.assertEqual(list(work_root.iterdir()), [])

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
