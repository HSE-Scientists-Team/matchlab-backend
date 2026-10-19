from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import tempfile
import unittest

from matchlab_ml.extract import save
from matchlab_ml.maintenance import artifact_candidates, freshness_errors


class MaintenanceTests(unittest.TestCase):
    def test_retention_protects_active_recent_unknown_and_symlinked_runs(self):
        now = datetime.now(timezone.utc)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name, days in [("old", 8), ("active", 8), ("recent", 1), ("unknown", 8)]:
                directory = root / name
                directory.mkdir()
                (directory / "snapshot.json").write_text("{}")
                if name != "unknown":
                    (directory / "manifest.json").write_text(json.dumps({"schema_version": 1,
                        "extracted_at": (now-timedelta(days=days)).isoformat()}))
            (root / "linked").symlink_to(root / "old", target_is_directory=True)
            self.assertEqual([p.name for p in artifact_candidates(root, {"active"}, now)], ["old"])
            with self.assertRaises(ValueError):
                artifact_candidates(root, set(), now, 0)

    def test_private_atomic_exports_preserve_previous_file_on_serialization_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'snapshot.json'
            save(output, {'valid': True})
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(ValueError):
                save(output, {'invalid': float('nan')})
            self.assertEqual(json.loads(output.read_text()), {'valid': True})
            self.assertEqual(list(Path(temporary).iterdir()), [output])

    def test_freshness_distinguishes_empty_source_stale_and_synthetic_data(self):
        now = datetime.now(timezone.utc)
        latest = ("run", now, "postgres")
        self.assertEqual(freshness_errors(latest, None, -2, None, now, has_users=False), [])
        self.assertTrue(freshness_errors(latest, None, -2, None, now))
        self.assertEqual(freshness_errors(latest, "run", 100, now.timestamp(), now), [])
        self.assertTrue(freshness_errors(("run", now-timedelta(hours=13), "postgres"), "old", 100,
                                          (now-timedelta(hours=13)).timestamp(), now))
        self.assertTrue(freshness_errors(("run", now, "synthetic"), None, -2, None, now, has_users=False))
