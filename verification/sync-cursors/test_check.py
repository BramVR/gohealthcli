import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import check


SAFETY = "TestFormalSyncCursorSafety"
COMMIT = "TestFormalSyncCursorCommitFailure"
HIGHWATER = "TestFormalSyncCursorHighwater"


class GoResultsTest(unittest.TestCase):
    def go_check(self, events, *, code=1, safety_only=False):
        result = subprocess.CompletedProcess([], code, "\n".join(json.dumps(event) for event in events))
        with tempfile.TemporaryDirectory() as directory, patch.object(check, "run", return_value=result), contextlib.redirect_stdout(io.StringIO()):
            return check.go_check(Path(directory), "test", safety_only=safety_only)

    def test_parallel_failure_cannot_borrow_highwater_marker(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "run", "Test": COMMIT},
            {"Action": "pause", "Test": COMMIT},
            {"Action": "output", "Test": HIGHWATER, "Output": "CursorHighwater: backward cursor\n"},
            {"Action": "fail", "Test": HIGHWATER},
            {"Action": "cont", "Test": COMMIT},
            {"Action": "output", "Test": COMMIT + "/existing_cursor", "Output": "unrelated fixture failure\n"},
            {"Action": "fail", "Test": COMMIT + "/existing_cursor"},
            {"Action": "fail", "Test": COMMIT},
        ]
        with self.assertRaisesRegex(RuntimeError, COMMIT + " failed outside a named invariant"):
            self.go_check(events)

    def test_known_failure_still_requires_every_test(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "output", "Test": HIGHWATER, "Output": "CursorHighwater: backward cursor\n"},
            {"Action": "fail", "Test": HIGHWATER},
        ]
        with self.assertRaisesRegex(RuntimeError, "missing named result for " + COMMIT):
            self.go_check(events)

    def test_highwater_finding_remains_a_property_failure(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "pass", "Test": COMMIT},
            {"Action": "output", "Test": HIGHWATER, "Output": "CursorHighwater: backward cursor\n"},
            {"Action": "fail", "Test": HIGHWATER},
        ]
        self.assertEqual(self.go_check(events).returncode, 1)

    def test_transaction_marker_belongs_to_its_subtest(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "output", "Test": COMMIT + "/existing_cursor", "Output": "TransactionReleased: active transaction\n"},
            {"Action": "fail", "Test": COMMIT + "/existing_cursor"},
            {"Action": "fail", "Test": COMMIT},
        ]
        self.assertEqual(self.go_check(events, safety_only=True).returncode, 1)

    def test_skipped_proof_cannot_pass(self):
        events = [{"Action": "pass", "Test": SAFETY}, {"Action": "skip", "Test": COMMIT}]
        with self.assertRaisesRegex(RuntimeError, "missing named result for " + COMMIT):
            self.go_check(events, code=0, safety_only=True)


if __name__ == "__main__":
    unittest.main()
