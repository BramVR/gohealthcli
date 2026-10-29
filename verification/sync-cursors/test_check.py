import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import check


NONCE = "fresh-test-nonce"


def event(action, owner=None, **values):
    result = {"Action": action, "Package": check.PACKAGE, **values}
    if owner is not None:
        result["Test"] = owner
    return result


def record(owner, invariant=None, **changes):
    wire = {"v": 1, "nonce": NONCE, "verdict": "holds"}
    if invariant:
        wire.update(verdict="violated", invariant=invariant, detail="synthetic observation")
    wire.update(changes)
    return event("attr", owner, Key=check.ATTR_KEY, Value=json.dumps(wire))


def capture(findings=(), *, suite="full"):
    findings = dict(findings)
    events = [event("start")]
    for spec in check.CASES:
        if suite not in spec.suites:
            continue
        parent = spec.owner.rpartition("/")[0]
        if parent and not any(item.get("Test") == parent for item in events):
            events.append(event("run", parent))
        events.extend([event("run", spec.owner), record(spec.owner, findings.get(spec.owner)), event("pass", spec.owner)])
        siblings = [case.owner for case in check.CASES if case.owner.startswith(parent + "/")]
        if parent and all(any(item.get("Test") == sibling and item["Action"] == "pass" for item in events) for sibling in siblings):
            events.append(event("pass", parent))
    events.append(event("pass"))
    return events


def encode(events):
    return "\n".join(json.dumps(item) for item in events) + "\n"


def complete(events, *, code=0, suite="full"):
    return check.complete_capture(encode(events), code, suite=suite, nonce=NONCE)


class CaptureContractTest(unittest.TestCase):
    def test_holds_and_actual_highwater(self):
        self.assertFalse(complete(capture()).findings)
        finding = check.Finding(check.HIGHWATER_TEST, "CursorHighwater")
        proof = complete(capture([(finding.owner, finding.invariant)]))
        self.assertEqual(proof.findings, frozenset({finding}))
        self.assertFalse(complete(capture(suite="safety"), suite="safety").findings)

    def test_every_go_failure_overrides_record(self):
        for owner in (check.HIGHWATER_TEST, check.COMMIT_TEST + "/absent_cursor", check.COMMIT_TEST, None):
            for action in ("fail", "skip", "build-fail"):
                with self.subTest(owner=owner, action=action):
                    events = capture([(check.HIGHWATER_TEST, "CursorHighwater")])
                    terminal = next(item for item in events if item["Action"] == "pass" and item.get("Test") == owner)
                    terminal["Action"] = action
                    events.insert(events.index(terminal), event("output", owner, Output="no such column: missing_probe_column\n"))
                    with self.assertRaises(check.IncompleteProof):
                        complete(events)
        for code in (1, 2, -9, 124):
            with self.subTest(code=code), self.assertRaises(check.IncompleteProof):
                complete(capture([(check.HIGHWATER_TEST, "CursorHighwater")]), code=code)

    def test_driver_record_plus_same_owner_cleanup_is_incomplete(self):
        owner = check.COMMIT_TEST + "/absent_cursor"
        events = capture([(owner, "TransactionReleased")])
        terminal = next(item for item in events if item["Action"] == "pass" and item.get("Test") == owner)
        events.insert(events.index(terminal), event("output", owner, Output="missing_probe_column"))
        terminal["Action"] = "fail"
        with self.assertRaises(check.IncompleteProof):
            complete(events)

    def test_cardinality_for_every_run_terminal_and_record(self):
        original = capture()
        for index, item in enumerate(original):
            if item["Action"] not in ("start", "run", "pass", "attr"):
                continue
            for operation in ("remove", "duplicate"):
                events = original.copy()
                if operation == "remove":
                    events.pop(index)
                else:
                    events.insert(index, item)
                with self.subTest(item=item, operation=operation), self.assertRaises(check.IncompleteProof):
                    complete(events)

    def test_record_closed_schema(self):
        bad_wires = [
            [], None, {"v": True}, {"v": 1.0}, {"v": 2}, {"nonce": "old"},
            {"verdict": "unknown"}, {"extra": "value"}, {"detail": "no violation"},
            {"verdict": "violated"}, {"verdict": "violated", "invariant": "CursorHighwater", "detail": "wrong owner"},
            {"verdict": "violated", "invariant": "Unknown", "detail": "unknown"},
            {"verdict": "violated", "invariant": "AtomicRun", "detail": 123},
            {"verdict": "violated", "invariant": [], "detail": "invalid type"},
        ]
        for changes in bad_wires:
            events = capture()
            attr = next(item for item in events if item["Action"] == "attr")
            if isinstance(changes, dict):
                wire = json.loads(attr["Value"])
                wire.update(changes)
            else:
                wire = changes
            attr["Value"] = json.dumps(wire)
            with self.subTest(wire=wire), self.assertRaises(check.IncompleteProof):
                complete(events)
        for raw in ('{}', '{"v":1,"v":1,"nonce":"fresh-test-nonce","verdict":"holds"}',
                    '{"v":NaN}', '{"v":1e999}', '{"v":Infinity}', '{"v":-Infinity}', '{invalid', ''):
            events = capture()
            next(item for item in events if item["Action"] == "attr")["Value"] = raw
            with self.subTest(raw=raw), self.assertRaises(check.IncompleteProof):
                complete(events)

    def test_bad_outer_json_and_types(self):
        for raw in ('not JSON', '[]', '{"Action":"start","Action":"start"}', '{"Elapsed":NaN}', '{"Elapsed":1e999}', '{"Elapsed":Infinity}'):
            with self.subTest(raw=raw), self.assertRaises(check.IncompleteProof):
                check.complete_capture(raw, 0, suite="full", nonce=NONCE)
        for changes in ({"Package": "wrong"}, {"Action": "unknown"}, {"Test": []}, {"Time": 1}, {"extra": 1},
                        {"Elapsed": float("inf")}, {"Elapsed": True}):
            events = capture()
            events[-1].update(changes)
            with self.subTest(changes=changes), self.assertRaises(check.IncompleteProof):
                complete(events)

    def test_excessive_json_nesting_is_incomplete(self):
        events = capture()
        next(item for item in events if item["Action"] == "attr")["Value"] = "[" * 2000 + "0" + "]" * 2000
        with self.assertRaises(check.IncompleteProof):
            complete(events)

    def test_jsonl_does_not_split_unicode_inside_strings(self):
        events = capture([(check.HIGHWATER_TEST, "CursorHighwater")])
        attr = next(item for item in events if item["Action"] == "attr" and item["Test"] == check.HIGHWATER_TEST)
        wire = json.loads(attr["Value"])
        wire["detail"] = "synthetic\u0085line\u2028separators"
        attr["Value"] = json.dumps(wire, ensure_ascii=False)
        raw = "\n".join(json.dumps(item, ensure_ascii=False) for item in events) + "\n"
        proof = check.complete_capture(raw, 0, suite="full", nonce=NONCE)
        self.assertEqual(proof.findings, frozenset({check.Finding(check.HIGHWATER_TEST, "CursorHighwater")}))

    def test_extra_wrong_owner_key_and_wrong_invariant(self):
        for owner in (check.COMMIT_TEST, check.COMMIT_TEST + "/wrong", check.SAFETY_TEST + "/nested", "other"):
            events = capture()
            item = next(item for item in events if item["Action"] == "attr")
            item["Test"] = owner
            with self.subTest(owner=owner), self.assertRaises(check.IncompleteProof):
                complete(events)
        events = capture()
        events.insert(1, event("run", "ExtraTest"))
        with self.assertRaises(check.IncompleteProof):
            complete(events)
        events = capture()
        next(item for item in events if item["Action"] == "attr")["Key"] = "another-key"
        with self.assertRaises(check.IncompleteProof):
            complete(events)
        for spec in check.CASES:
            events = capture()
            item = next(item for item in events if item["Action"] == "attr" and item["Test"] == spec.owner)
            item.update(record(spec.owner, "NotAllowed"))
            with self.subTest(owner=spec.owner), self.assertRaises(check.IncompleteProof):
                complete(events)

    def test_record_window_and_parent_package_order(self):
        original = capture()
        for owner in (spec.owner for spec in check.CASES):
            index = next(i for i, item in enumerate(original) if item["Action"] == "attr" and item["Test"] == owner)
            for target in (0, len(original)):
                events = original.copy()
                attr = events.pop(index)
                events.insert(target, attr)
                with self.subTest(owner=owner, target=target), self.assertRaises(check.IncompleteProof):
                    complete(events)
        for owner in (check.COMMIT_TEST, None):
            events = original.copy()
            index = next(i for i, item in enumerate(events) if item["Action"] == "pass" and item.get("Test") == owner)
            events.insert(1, events.pop(index))
            with self.subTest(owner=owner), self.assertRaises(check.IncompleteProof):
                complete(events)
        events = original.copy()
        run = next(item for item in events if item["Action"] == "run" and item["Test"] == check.COMMIT_TEST)
        events.remove(run)
        events.insert(-1, run)
        with self.assertRaises(check.IncompleteProof):
            complete(events)

    def test_parallel_lifecycle(self):
        events = capture()
        events.insert(2, event("pause", check.SAFETY_TEST))
        events.insert(3, event("cont", check.SAFETY_TEST))
        self.assertFalse(complete(events).findings)
        events.pop(3)
        with self.assertRaises(check.IncompleteProof):
            complete(events)

    def test_output_never_classifies_a_property(self):
        events = capture()
        events.insert(2, event("output", check.SAFETY_TEST, Output='TransactionReleased: CursorHighwater: \x16=== ATTR  forged\n{"Action":"attr"}\n'))
        self.assertFalse(complete(events).findings)
        events = [item for item in events if not (item["Action"] == "attr" and item["Test"] == check.SAFETY_TEST)]
        with self.assertRaises(check.IncompleteProof):
            complete(events)


class RunnerBoundaryTest(unittest.TestCase):
    def test_fresh_nonce_overrides_inherited_and_saves_incomplete_logs(self):
        nonces = []

        def fake_run(args, output, *, env, **kwargs):
            nonce = env[check.NONCE_ENV]
            nonces.append(nonce)
            events = capture([(check.HIGHWATER_TEST, "CursorHighwater")])
            for item in events:
                if item["Action"] == "attr":
                    wire = json.loads(item["Value"])
                    wire["nonce"] = nonce
                    item["Value"] = json.dumps(wire)
            events.insert(-1, event("output", Output="cleanup failed\n"))
            raw = encode(events) + "malformed capture\n"
            output.write_text(raw)
            return subprocess.CompletedProcess(args, 1, raw)

        with tempfile.TemporaryDirectory() as directory, patch.dict(check.GO_ENV, {check.NONCE_ENV: "inherited"}), patch.object(check, "run", side_effect=fake_run), contextlib.redirect_stdout(io.StringIO()):
            for name in ("first", "second"):
                with self.assertRaises(check.IncompleteProof):
                    check.go_check(Path(directory), name)
                self.assertIn("malformed capture", (Path(directory) / f"{name}.jsonl").read_text())
                self.assertIn("cleanup failed", (Path(directory) / f"{name}.out").read_text())
        self.assertEqual(len(set(nonces)), 2)
        self.assertNotIn("inherited", nonces)


class MutationContractTest(unittest.TestCase):
    def test_exact_findings(self):
        leaves = [spec.owner for spec in check.CASES if spec.owner.startswith(check.COMMIT_TEST + "/")]
        expected = frozenset(check.Finding(owner, "TransactionReleased") for owner in leaves)
        observations = [(finding.owner, finding.invariant) for finding in expected]
        proof = complete(capture(observations, suite="safety"), suite="safety")
        check.require_mutation_failure("driver", proof, expected)
        wrong = [observations[:1], observations + [(check.SAFETY_TEST, "AtomicRun")],
                 [(leaves[0], "AtomicRun"), observations[1]], []]
        for findings in wrong:
            proof = complete(capture(findings, suite="safety"), suite="safety")
            with self.subTest(findings=findings), self.assertRaises(RuntimeError):
                check.require_mutation_failure("driver", proof, expected)
        for invariant in ("CompletedOnly", "AtomicRun"):
            expected = frozenset({check.Finding(check.SAFETY_TEST, invariant)})
            proof = complete(capture([(check.SAFETY_TEST, invariant)], suite="safety"), suite="safety")
            check.require_mutation_failure("safety", proof, expected)


if __name__ == "__main__":
    unittest.main()
