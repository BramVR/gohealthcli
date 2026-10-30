#!/usr/bin/env python3
"""Check the cursor model and replay bounded schedules against the SQLite writer."""

import argparse
from dataclasses import dataclass
import hashlib
import json
import math
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[2]
SPEC = Path(__file__).resolve().parent
TOOLS = ROOT / "dist/formal-tools"
JAR = TOOLS / "tla2tools-1.7.4.jar"
JAR_SHA256 = "936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88"
JAR_URL = "https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar"
GO_ENV = {**os.environ, "GOTOOLCHAIN": "auto"}
SAFETY_TEST = "TestFormalSyncCursorSafety"
COMMIT_TEST = "TestFormalSyncCursorCommitFailure"
HIGHWATER_TEST = "TestFormalSyncCursorHighwater"
ATTR_KEY = "sync-cursor.case.v1"
NONCE_ENV = "GOHEALTHCLI_SYNC_PROOF_NONCE"
PACKAGE = "github.com/BramVR/gohealthcli/cmd/gohealthcli"


@dataclass(frozen=True)
class CaseSpec:
    owner: str
    allowed: frozenset[str]
    suites: frozenset[str]


CASES = (
    CaseSpec(SAFETY_TEST, frozenset({"FinalizeOutcome", "AtomicRun", "CompletedOnly"}), frozenset({"full", "safety"})),
    *(CaseSpec(COMMIT_TEST + "/" + case, frozenset({"TransactionReleased", "AtomicRun", "CompletedOnly"}),
               frozenset({"full", "safety"})) for case in ("absent_cursor", "existing_cursor")),
    CaseSpec(HIGHWATER_TEST, frozenset({"CursorHighwater"}), frozenset({"full"})),
)


@dataclass(frozen=True)
class Finding:
    owner: str
    invariant: str


@dataclass(frozen=True)
class CaseResult:
    owner: str
    invariant: str | None
    detail: str


@dataclass(frozen=True)
class CompletedProof:
    cases: tuple[CaseResult, ...]

    @property
    def findings(self) -> frozenset[Finding]:
        return frozenset(Finding(case.owner, case.invariant) for case in self.cases if case.invariant is not None)


class IncompleteProof(RuntimeError):
    pass


def strict_json(text):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError(f"duplicate JSON key {key!r}")
            result[key] = value
        return result

    def nonfinite(value):
        raise ValueError(f"non-finite JSON value {value!r}")

    def finite_float(value):
        parsed = float(value)
        if not math.isfinite(parsed):
            return nonfinite(value)
        return parsed

    return json.loads(text, object_pairs_hook=pairs, parse_constant=nonfinite, parse_float=finite_float)


def complete_capture(raw: str, process_code: int, *, suite: str, nonce: str) -> CompletedProof:
    if process_code != 0:
        raise IncompleteProof(f"Go process exited {process_code}")
    specs = {case.owner: case for case in CASES if suite in case.suites}
    if not specs or not nonce:
        raise IncompleteProof("unknown suite or missing invocation nonce")
    terminals = set(specs)
    for owner in specs:
        parts = owner.split("/")
        terminals.update("/".join(parts[:i]) for i in range(1, len(parts)))
    runs, passed, records, paused = set(), set(), {}, set()
    package_started = package_passed = False
    fields = {
        "start": set(), "run": {"Test"}, "pause": {"Test"}, "cont": {"Test"},
        "pass": {"Test", "Elapsed"}, "output": {"Test", "Output"},
        "attr": {"Test", "Key", "Value"},
    }
    try:
        for line in raw.removesuffix("\n").split("\n"):
            event = strict_json(line)
            if not isinstance(event, dict):
                raise ValueError("Go event must be an object")
            action, owner = event.get("Action"), event.get("Test")
            if not isinstance(action, str) or action not in fields:
                raise ValueError(f"nonpassing or unknown Go action {action!r}")
            if event.get("Package") != PACKAGE:
                raise ValueError("wrong Go package")
            if set(event) - ({"Time", "Action", "Package"} | fields[action]):
                raise ValueError("unexpected Go event fields")
            if "Time" in event and not isinstance(event["Time"], str):
                raise ValueError("invalid Go event time")
            if "Elapsed" in event and (type(event["Elapsed"]) not in (int, float) or not 0 <= event["Elapsed"] < float("inf")):
                raise ValueError("invalid Go elapsed time")
            if "Test" in event and (not isinstance(owner, str) or owner not in terminals):
                raise ValueError(f"unexpected Go owner {owner!r}")
            if package_passed:
                raise ValueError("event after package terminal")
            if action == "start":
                if package_started:
                    raise ValueError("duplicate package start")
                package_started = True
                continue
            if not package_started:
                raise ValueError("event before package start")
            if action == "output":
                if not isinstance(event.get("Output"), str):
                    raise ValueError("invalid Go output")
                continue
            if owner is None:
                if action != "pass" or passed != terminals:
                    raise ValueError("missing tests before package terminal")
                package_passed = True
                continue
            parent = owner.rpartition("/")[0]
            if action == "run":
                if owner in runs or (parent and (parent not in runs or parent in passed)):
                    raise ValueError(f"duplicate or unordered run {owner!r}")
                runs.add(owner)
                continue
            if owner not in runs or owner in passed:
                raise ValueError(f"event outside run-to-terminal window {owner!r}")
            if action == "pass":
                if owner in paused or any(test.startswith(owner + "/") and test not in passed for test in terminals):
                    raise ValueError(f"unordered test terminal {owner!r}")
                passed.add(owner)
            elif action == "pause":
                if owner in paused:
                    raise ValueError("duplicate pause")
                paused.add(owner)
            elif action == "cont":
                if owner not in paused:
                    raise ValueError("continue without pause")
                paused.remove(owner)
            elif action == "attr":
                if owner not in specs or owner in records or event.get("Key") != ATTR_KEY or not isinstance(event.get("Value"), str):
                    raise ValueError(f"extra, duplicate or unowned record {owner!r}")
                wire = strict_json(event["Value"])
                if not isinstance(wire, dict) or type(wire.get("v")) is not int or wire["v"] != 1 or wire.get("nonce") != nonce:
                    raise ValueError("invalid proof version or nonce")
                verdict = wire.get("verdict")
                if verdict == "holds" and set(wire) == {"v", "nonce", "verdict"}:
                    records[owner] = CaseResult(owner, None, "")
                elif (verdict == "violated" and set(wire) == {"v", "nonce", "verdict", "invariant", "detail"}
                      and isinstance(wire["invariant"], str) and wire["invariant"] in specs[owner].allowed
                      and isinstance(wire["detail"], str)):
                    records[owner] = CaseResult(owner, wire["invariant"], wire["detail"])
                else:
                    raise ValueError(f"invalid verdict for {owner!r}")
        if not package_passed or runs != terminals or passed != terminals or set(records) != set(specs):
            raise ValueError("missing package, test or proof record")
    except (ValueError, TypeError, RecursionError) as error:
        raise IncompleteProof(str(error)) from error
    return CompletedProof(tuple(records[owner] for owner in specs))


def run(args, output, *, cwd=ROOT, env=None, echo=True):
    print("Running", " ".join(map(str, args)), flush=True)
    try:
        result = subprocess.run(args, cwd=cwd, env=env, text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, timeout=180)
    except subprocess.TimeoutExpired as error:
        partial = error.stdout or b""
        output.write_text(partial.decode(errors="replace") if isinstance(partial, bytes) else partial)
        raise
    output.write_text(result.stdout)
    if echo:
        print(result.stdout, end="", flush=True)
    return result


def tlc_jar():
    TOOLS.mkdir(parents=True, exist_ok=True)
    if not JAR.exists():
        with tempfile.TemporaryDirectory(dir=TOOLS) as tmp:
            download = Path(tmp) / "tla2tools.jar"
            subprocess.run(["curl", "--fail", "--location", "--silent", "--show-error",
                            "--max-time", "120", JAR_URL, "--output", str(download)], check=True)
            if hashlib.sha256(download.read_bytes()).hexdigest() != JAR_SHA256:
                raise RuntimeError("TLC download checksum mismatch")
            download.rename(JAR)
    if hashlib.sha256(JAR.read_bytes()).hexdigest() != JAR_SHA256:
        raise RuntimeError(f"TLC checksum mismatch at {JAR}")
    return JAR


def go_check(out, name, *, safety_only=False, overlay=None, modfile=None):
    pattern = "^TestFormalSyncCursor(Safety|CommitFailure)$" if safety_only else "^TestFormalSyncCursor"
    command = ["go", "test", "-json", "-tags=formal", "-count=1", "-timeout=120s", "-v", "-run", pattern]
    if overlay:
        command += ["-overlay", str(overlay)]
    if modfile:
        command += ["-modfile", str(modfile)]
    nonce = secrets.token_hex(32)
    capture = out / f"{name}.jsonl"
    try:
        result = run(command + ["./cmd/gohealthcli"], capture,
                     env={**GO_ENV, NONCE_ENV: nonce}, echo=False)
    finally:
        diagnostics = []
        if capture.exists():
            for line in capture.read_text().removesuffix("\n").split("\n"):
                try:
                    event = strict_json(line)
                    if isinstance(event, dict) and isinstance(event.get("Output"), str):
                        diagnostics.append(event["Output"])
                except (ValueError, RecursionError):
                    continue
        decoded = "".join(diagnostics)
        (out / f"{name}.out").write_text(decoded)
        print(decoded, end="", flush=True)
    proof = complete_capture(result.stdout, result.returncode,
                             suite="safety" if safety_only else "full", nonce=nonce)
    (out / f"{name}.proof.json").write_text(json.dumps({
        "cases": [dict(owner=case.owner, invariant=case.invariant, detail=case.detail) for case in proof.cases],
    }, indent=2) + "\n")
    return proof


def require_mutation_failure(name: str, proof: CompletedProof, expected: frozenset[Finding]):
    if proof.findings != expected:
        raise RuntimeError(f"{name}: did not produce its exact intended findings: {proof.findings!r}")


def mutations(out):
    module = subprocess.run(["go", "list", "-m", "-f", "{{.Dir}}", "modernc.org/sqlite"],
                            cwd=ROOT, env=GO_ENV, text=True, capture_output=True, check=True).stdout.strip()
    checks = [
        ("completed-only", ROOT / "cmd/gohealthcli/sync_cursor.go", "return outcome == syncRunOutcomeCompleted", "return true",
         frozenset({Finding(SAFETY_TEST, "CompletedOnly")})),
        ("atomic-run", ROOT / "cmd/gohealthcli/health_archive_writer.go",
         "func (archive *sqliteHealthArchiveWriter) finalizeSyncRunAttempt(ctx context.Context, finalize syncRunFinalize) error {",
         "func (archive *sqliteHealthArchiveWriter) finalizeSyncRunAttempt(ctx context.Context, finalize syncRunFinalize) error {\n"
         "\tif err := finishSyncRun(ctx, archive.db, finalize.runFinish()); err != nil { return err }",
         frozenset({Finding(SAFETY_TEST, "AtomicRun")})),
        ("key-isolation", ROOT / "cmd/gohealthcli/sync_cursor.go",
         "\t\tkey.sourceFamilyFilter,\n\t\tstring(key.rollupKind),\n\t\tto,",
         '\t\t"",\n\t\tstring(key.rollupKind),\n\t\tto,',
         frozenset({Finding(SAFETY_TEST, "CompletedOnly")})),
        ("highwater", ROOT / "cmd/gohealthcli/sync_cursor.go",
         "if !incoming.After(existing) {", "if false && !incoming.After(existing) {",
         frozenset({Finding(SAFETY_TEST, "CompletedOnly"), Finding(HIGHWATER_TEST, "CursorHighwater")})),
        ("transaction-released", Path(module) / "tx.go",
         "if t.c.db != 0 && sqlite3.Xsqlite3_get_autocommit(t.c.tls, t.c.db) == 0 {",
         "if false && t.c.db != 0 && sqlite3.Xsqlite3_get_autocommit(t.c.tls, t.c.db) == 0 {",
         frozenset(Finding(case.owner, "TransactionReleased") for case in CASES if case.owner.startswith(COMMIT_TEST + "/"))),
    ]
    if go_check(out, "mutation-baseline").findings:
        raise RuntimeError("Full baseline failed; do not attribute failures to mutations")
    for name, source, old, new, expected in checks:
        original = source.read_text()
        if original.count(old) != 1:
            raise RuntimeError(f"{name}: source changed; update the mutation after reviewing coverage")
        scratch = out / name
        scratch.mkdir()
        with tempfile.TemporaryDirectory(prefix="gohealthcli-sqlite-mutation-") as temporary:
            modfile = None
            overlay_source = source
            if name == "transaction-released":
                module_copy = Path(temporary) / "sqlite"
                shutil.copytree(module, module_copy)
                modfile = scratch / "mutant.mod"
                shutil.copy2(ROOT / "go.mod", modfile)
                shutil.copy2(ROOT / "go.sum", scratch / "mutant.sum")
                result = run(["go", "mod", "edit", "-modfile", str(modfile),
                              "-replace", f"modernc.org/sqlite={module_copy}"],
                             scratch / "module-replacement.out", env=GO_ENV)
                if result.returncode or go_check(out, "local-driver-baseline", modfile=modfile).findings:
                    raise RuntimeError("Unmodified local SQLite module baseline failed")
                overlay_source = module_copy / "tx.go"
            replacement = scratch / (source.name + ".overlay")
            replacement.write_text(original.replace(old, new))
            overlay = scratch / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(overlay_source): str(replacement)}}))
            result = go_check(out, name, overlay=overlay, modfile=modfile)
        require_mutation_failure(name, result, expected)
        if source.read_text() != original:
            raise RuntimeError(f"{source} changed during mutation checking")
        print(f"DETECTED {name}", flush=True)
    if go_check(out, "mutation-restored").findings:
        raise RuntimeError("Full baseline failed after removing overlays")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mutations", action="store_true", help="prove safety sensitivity with Go overlays; never edit product files")
    args = parser.parse_args()
    parent = ROOT / "dist/formal-verification"
    parent.mkdir(parents=True, exist_ok=True)
    out = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
    print(f"Evidence {out}", flush=True)
    goflags = os.environ.get("GOFLAGS")
    (out / "inputs.json").write_text(json.dumps({
        "GOFLAGS_present": goflags is not None,
        "GOFLAGS_sha256": hashlib.sha256(goflags.encode()).hexdigest() if goflags is not None else None,
        "GOTOOLCHAIN": GO_ENV["GOTOOLCHAIN"],
    }, indent=2) + "\n")
    for name, command in [("head", ["git", "rev-parse", "HEAD"]),
                          ("go-version", ["go", "version"]),
                          ("sqlite-version", ["go", "list", "-m", "modernc.org/sqlite"])]:
        if run(command, out / f"{name}.out", env=GO_ENV).returncode:
            raise RuntimeError(f"Could not record {name}")
    if args.mutations:
        mutations(out)
        return 0
    local_java = TOOLS / "jdk-21.0.9+10-jre/Contents/Home/bin/java"
    java = os.environ.get("JAVA") or (str(local_java) if local_java.exists() else "java")
    jar = tlc_jar()
    tool_version = run([java, "-version"], out / "java-version.out")
    if tool_version.returncode:
        raise RuntimeError("Java 11+ required. Install a JRE or set JAVA to its executable")
    shutil.copy2(SPEC / "SyncCursors.tla", out)
    results = []
    for config in ["Safety.cfg", "SyncCursors.cfg"]:
        shutil.copy2(SPEC / config, out)
        result = run([java, "-XX:+UseParallelGC", "-Xmx512m", "-jar", str(jar),
                      "-workers", "1", "-config", config, "-metadir", f"states-{config}", "SyncCursors.tla"],
                     out / f"{config}.out", cwd=out)
        if result.returncode not in (0, 12) or (result.returncode == 12 and "Invariant" not in result.stdout):
            raise RuntimeError(f"TLC did not finish a valid invariant check; read {config}.out")
        results.append(result.returncode)
    sqlite = go_check(out, "sqlite")
    results.append(bool(sqlite.findings))
    print(f"Evidence {out}", flush=True)
    return int(any(results))


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Verification could not complete: {error}", file=sys.stderr)
        sys.exit(2)
