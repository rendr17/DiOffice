from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
MODULE_DIR = REPO_ROOT / "tools" / "execution-spike"
sys.path.insert(0, str(MODULE_DIR))

from dioffice_spike import SpikeError, load_manifest

SCHEMA = REPO_ROOT / "docs" / "schemas" / "execution-manifest.schema.json"
EXAMPLE = REPO_ROOT / "examples" / "execution-manifest.react-pnpm.json"


class ManifestValidationTests(unittest.TestCase):
    def test_canonical_example_passes_schema_validation(self) -> None:
        manifest = load_manifest(EXAMPLE, SCHEMA)
        self.assertEqual(manifest["workerProfile"], "node-22-pnpm-10-playwright")

    def test_windows_path_traversal_is_rejected(self) -> None:
        data = json.loads(EXAMPLE.read_text(encoding="utf-8"))
        data["workingDirectory"] = r"..\..\outside"
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "manifest.json"
            path.write_text(json.dumps(data), encoding="utf-8")
            with self.assertRaisesRegex(SpikeError, "workingDirectory"):
                load_manifest(path, SCHEMA)

    def test_absolute_windows_path_is_rejected(self) -> None:
        data = json.loads(EXAMPLE.read_text(encoding="utf-8"))
        data["workingDirectory"] = r"C:\outside"
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "manifest.json"
            path.write_text(json.dumps(data), encoding="utf-8")
            with self.assertRaisesRegex(SpikeError, "workingDirectory"):
                load_manifest(path, SCHEMA)


if __name__ == "__main__":
    unittest.main()
