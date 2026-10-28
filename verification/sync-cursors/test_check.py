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
            {"Action": "pass", "Test": COMMIT + "/absent_cursor"},
            {"Action": "output", "Test": COMMIT + "/existing_cursor", "Output": "unrelated fixture failure\n"},
            {"Action": "fail", "Test": COMMIT + "/existing_cursor"},
            {"Action": "fail", "Test": COMMIT},
        ]
        with self.assertRaisesRegex(RuntimeError, COMMIT + "/existing_cursor failed outside a named invariant"):
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
            {"Action": "pass", "Test": COMMIT + "/absent_cursor"},
            {"Action": "pass", "Test": COMMIT + "/existing_cursor"},
            {"Action": "pass", "Test": COMMIT},
            {"Action": "output", "Test": HIGHWATER, "Output": "CursorHighwater: backward cursor\n"},
            {"Action": "fail", "Test": HIGHWATER},
        ]
        self.assertEqual(self.go_check(events).returncode, 1)

    def test_transaction_marker_belongs_to_its_subtest(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "pass", "Test": COMMIT + "/absent_cursor"},
            {"Action": "output", "Test": COMMIT + "/existing_cursor", "Output": "TransactionReleased: active transaction\n"},
            {"Action": "fail", "Test": COMMIT + "/existing_cursor"},
            {"Action": "fail", "Test": COMMIT},
        ]
        self.assertEqual(self.go_check(events, safety_only=True).returncode, 1)

    def test_skipped_proof_cannot_pass(self):
        events = [{"Action": "pass", "Test": SAFETY}, {"Action": "skip", "Test": COMMIT}]
        with self.assertRaisesRegex(RuntimeError, "skipped proof: " + COMMIT):
            self.go_check(events, code=0, safety_only=True)

    def test_sibling_failure_cannot_borrow_transaction_marker(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "output", "Test": COMMIT + "/absent_cursor", "Output": "TransactionReleased: active transaction\n"},
            {"Action": "fail", "Test": COMMIT + "/absent_cursor"},
            {"Action": "output", "Test": COMMIT + "/existing_cursor", "Output": "no such column: missing_probe_column\n"},
            {"Action": "fail", "Test": COMMIT + "/existing_cursor"},
            {"Action": "output", "Test": COMMIT, "Output": "--- FAIL: " + COMMIT + " (0.01s)\n"},
            {"Action": "fail", "Test": COMMIT},
        ]
        with self.assertRaisesRegex(RuntimeError, COMMIT + "/existing_cursor failed outside a named invariant"):
            self.go_check(events, safety_only=True)

    def test_parent_failure_cannot_borrow_child_marker(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "pass", "Test": COMMIT + "/existing_cursor"},
            {"Action": "output", "Test": COMMIT + "/absent_cursor", "Output": "TransactionReleased: active transaction\n"},
            {"Action": "fail", "Test": COMMIT + "/absent_cursor"},
            {"Action": "output", "Test": COMMIT, "Output": "unrelated parent cleanup failure\n"},
            {"Action": "fail", "Test": COMMIT},
        ]
        with self.assertRaisesRegex(RuntimeError, COMMIT + " failed outside a named invariant"):
            self.go_check(events, safety_only=True)

    def test_child_failure_cannot_borrow_parent_marker(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "pass", "Test": COMMIT + "/absent_cursor"},
            {"Action": "output", "Test": COMMIT, "Output": "TransactionReleased: active transaction\n"},
            {"Action": "output", "Test": COMMIT + "/existing_cursor", "Output": "unrelated child failure\n"},
            {"Action": "fail", "Test": COMMIT + "/existing_cursor"},
            {"Action": "fail", "Test": COMMIT},
        ]
        with self.assertRaisesRegex(RuntimeError, COMMIT + "/existing_cursor failed outside a named invariant"):
            self.go_check(events, safety_only=True)

    def test_marked_sibling_failures_propagate_to_parent(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "output", "Test": COMMIT + "/absent_cursor", "Output": "TransactionReleased: active transaction\n"},
            {"Action": "fail", "Test": COMMIT + "/absent_cursor"},
            {"Action": "output", "Test": COMMIT + "/existing_cursor", "Output": "TransactionReleased: active transaction\n"},
            {"Action": "fail", "Test": COMMIT + "/existing_cursor"},
            {"Action": "output", "Test": COMMIT, "Output": "--- FAIL: " + COMMIT + " (0.01s)\n"},
            {"Action": "fail", "Test": COMMIT},
        ]
        self.assertEqual(self.go_check(events, safety_only=True).returncode, 1)

    def test_skipped_cursor_case_is_incomplete(self):
        for case in ("absent_cursor", "existing_cursor"):
            with self.subTest(case=case):
                events = [
                    {"Action": "pass", "Test": SAFETY},
                    {"Action": "pass", "Test": COMMIT},
                    *[{"Action": "skip" if name == case else "pass", "Test": COMMIT + "/" + name}
                      for name in ("absent_cursor", "existing_cursor")],
                ]
                with self.assertRaisesRegex(RuntimeError, "skipped proof: " + COMMIT + "/" + case):
                    self.go_check(events, code=0, safety_only=True)

    def test_missing_cursor_case_is_incomplete(self):
        for case in ("absent_cursor", "existing_cursor"):
            with self.subTest(case=case):
                events = [
                    {"Action": "pass", "Test": SAFETY},
                    {"Action": "pass", "Test": COMMIT},
                    *[{"Action": "pass", "Test": COMMIT + "/" + name}
                      for name in ("absent_cursor", "existing_cursor") if name != case],
                ]
                with self.assertRaisesRegex(RuntimeError, "missing named result for " + COMMIT + "/" + case):
                    self.go_check(events, code=0, safety_only=True)

    def test_highwater_failure_cannot_hide_all_cursor_cases_skipping(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "pass", "Test": COMMIT},
            {"Action": "skip", "Test": COMMIT + "/absent_cursor"},
            {"Action": "skip", "Test": COMMIT + "/existing_cursor"},
            {"Action": "output", "Test": HIGHWATER, "Output": "CursorHighwater: backward cursor\n"},
            {"Action": "fail", "Test": HIGHWATER},
        ]
        with self.assertRaisesRegex(RuntimeError, "skipped proof:"):
            self.go_check(events)

    def test_nested_skip_is_incomplete(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "pass", "Test": COMMIT},
            {"Action": "pass", "Test": COMMIT + "/absent_cursor"},
            {"Action": "pass", "Test": COMMIT + "/existing_cursor"},
            {"Action": "skip", "Test": COMMIT + "/existing_cursor/nested"},
        ]
        with self.assertRaisesRegex(RuntimeError, "skipped proof: " + COMMIT + "/existing_cursor/nested"):
            self.go_check(events, code=0, safety_only=True)

    def test_unexpected_test_is_not_required_proof(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "pass", "Test": COMMIT},
            {"Action": "pass", "Test": COMMIT + "/absent_cursor"},
            {"Action": "pass", "Test": COMMIT + "/existing_cursor"},
            {"Action": "pass", "Test": COMMIT + "/wrong_owner"},
        ]
        with self.assertRaisesRegex(RuntimeError, "unexpected named result:"):
            self.go_check(events, code=0, safety_only=True)

    def test_wrong_invariant_cannot_complete_failed_cursor_case(self):
        events = [
            {"Action": "pass", "Test": SAFETY},
            {"Action": "pass", "Test": COMMIT + "/absent_cursor"},
            {"Action": "output", "Test": COMMIT + "/existing_cursor", "Output": "CursorHighwater: unrelated invariant\n"},
            {"Action": "fail", "Test": COMMIT + "/existing_cursor"},
            {"Action": "fail", "Test": COMMIT},
        ]
        with self.assertRaisesRegex(RuntimeError, COMMIT + "/existing_cursor failed outside a named invariant"):
            self.go_check(events, safety_only=True)


class MutationContractTest(unittest.TestCase):
    def test_both_cursor_cases_must_fail_on_transaction_release(self):
        owners = (COMMIT + "/absent_cursor", COMMIT + "/existing_cursor")
        cases = {SAFETY: "pass", COMMIT: "fail", **{owner: "fail" for owner in owners}}
        witnesses = {owner: ("TransactionReleased:",) for owner in owners}
        proof = check.GoProof(1, "", cases, witnesses)
        check.require_mutation_failure("driver", proof, owners, "TransactionReleased:")
        for case in owners:
            with self.subTest(case=case):
                for outcome in ("pass", "skip", None):
                    changed = dict(cases)
                    if outcome is None:
                        del changed[case]
                    else:
                        changed[case] = outcome
                    with self.assertRaisesRegex(RuntimeError, "intended cases and invariant"):
                        check.require_mutation_failure("driver", check.GoProof(1, "", changed, witnesses), owners, "TransactionReleased:")

    def test_marker_from_wrong_owner_cannot_count(self):
        cases = {SAFETY: "fail", COMMIT: "pass", COMMIT + "/absent_cursor": "pass", COMMIT + "/existing_cursor": "pass"}
        proof = check.GoProof(1, "TransactionReleased:", cases, {SAFETY: ("atomic run invariant",)})
        with self.assertRaisesRegex(RuntimeError, "intended cases and invariant"):
            check.require_mutation_failure("driver", proof, (COMMIT + "/absent_cursor", COMMIT + "/existing_cursor"), "TransactionReleased:")

    def test_unexpected_error_is_not_an_invariant_witness(self):
        owners = (COMMIT + "/absent_cursor", COMMIT + "/existing_cursor")
        cases = {SAFETY: "pass", COMMIT: "fail", **{owner: "fail" for owner in owners}}
        proof = check.GoProof(1, "TransactionReleased:", cases, {owners[0]: ("TransactionReleased:",)})
        with self.assertRaisesRegex(RuntimeError, "intended cases and invariant"):
            check.require_mutation_failure("driver", proof, owners, "TransactionReleased:")

    def test_highwater_finding_cannot_count_as_mutation_witness(self):
        cases = {SAFETY: "pass", COMMIT: "pass", COMMIT + "/absent_cursor": "pass", COMMIT + "/existing_cursor": "pass", HIGHWATER: "fail"}
        proof = check.GoProof(1, "TransactionReleased:", cases, {HIGHWATER: ("CursorHighwater:",)})
        with self.assertRaisesRegex(RuntimeError, "intended cases and invariant"):
            check.require_mutation_failure("driver", proof, (COMMIT + "/absent_cursor", COMMIT + "/existing_cursor"), "TransactionReleased:")


if __name__ == "__main__":
    unittest.main()
