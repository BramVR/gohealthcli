#!/usr/bin/env python3
"""Check the cursor model and replay bounded schedules against the SQLite writer."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from typing import NamedTuple


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
CURSOR_CASES = tuple(COMMIT_TEST + "/" + case for case in ("absent_cursor", "existing_cursor"))
SAFETY_CASES = (SAFETY_TEST, COMMIT_TEST, *CURSOR_CASES)


class GoProof(NamedTuple):
    returncode: int
    stdout: str
    cases: dict[str, str]
    invariants: dict[str, tuple[str, ...]]


def run(args, output, *, cwd=ROOT, env=None, echo=True):
    print("Running", " ".join(map(str, args)), flush=True)
    result = subprocess.run(args, cwd=cwd, env=env, text=True, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, timeout=180)
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
    result = run(command + ["./cmd/gohealthcli"], out / f"{name}.jsonl", env=GO_ENV, echo=False)
    try:
        events = [json.loads(line) for line in result.stdout.splitlines()]
    except json.JSONDecodeError as error:
        print(result.stdout, end="", flush=True)
        raise RuntimeError(f"{name}: invalid Go test events; read {name}.jsonl") from error
    result.stdout = "".join(event.get("Output", "") for event in events)
    (out / f"{name}.out").write_text(result.stdout)
    print(result.stdout, end="", flush=True)
    terminal = {event["Test"]: event["Action"] for event in events
                if "Test" in event and event["Action"] in ("pass", "fail", "skip")}
    skipped = sorted(test for test, action in terminal.items() if action == "skip")
    if skipped:
        raise RuntimeError(f"{name}: skipped proof: {', '.join(skipped)}")
    names = SAFETY_CASES + (() if safety_only else (HIGHWATER_TEST,))
    for test in names:
        if terminal.get(test) not in ("pass", "fail"):
            raise RuntimeError(f"{name}: missing named result for {test}")
    extra = sorted(set(terminal) - set(names))
    if extra:
        raise RuntimeError(f"{name}: unexpected named result: {', '.join(extra)}")
    failures = {test for test, action in terminal.items() if action == "fail"}
    invariants = {}
    if result.returncode:
        if result.returncode != 1 or not failures:
            raise RuntimeError(f"{name}: Go checks did not reach a verification assertion; read {name}.out")
        markers = {
            SAFETY_TEST: ("atomic run invariant", "completed-only/key-isolation invariant", "finalize outcome invariant"),
            COMMIT_TEST: ("TransactionReleased:",),
            HIGHWATER_TEST: ("CursorHighwater:",),
        }
        for test in sorted(failures):
            output = "".join(event.get("Output", "") for event in events
                             if event.get("Test") == test)
            diagnostics = "".join(line for line in output.splitlines(keepends=True)
                                  if not line.lstrip().startswith(("=== ", "--- ")))
            matched = tuple(marker for marker in markers.get(test.split("/", 1)[0], ()) if marker in diagnostics)
            if matched:
                invariants[test] = matched
                continue
            if not diagnostics.strip() and any(child.startswith(test + "/") for child in failures):
                continue
            raise RuntimeError(f"{test} failed outside a named invariant; read {name}.out")
    elif any(terminal[test] != "pass" for test in names) or failures:
        raise RuntimeError(f"{name}: missing named PASS result; read {name}.out")
    (out / f"{name}.proof.json").write_text(json.dumps({
        "cases": terminal, "invariants": invariants,
    }, indent=2) + "\n")
    return GoProof(result.returncode, result.stdout, terminal, invariants)


def require_mutation_failure(name, proof, owners, marker):
    expected = {case: "fail" if any(owner == case or owner.startswith(case + "/") for owner in owners) else "pass"
                for case in SAFETY_CASES}
    if (proof.returncode != 1 or proof.cases != expected or set(proof.invariants) != set(owners)
            or any(marker not in proof.invariants[owner] for owner in owners)):
        raise RuntimeError(f"{name}: did not fail on its intended cases and invariant")


def mutations(out):
    module = subprocess.run(["go", "list", "-m", "-f", "{{.Dir}}", "modernc.org/sqlite"],
                            cwd=ROOT, env=GO_ENV, text=True, capture_output=True, check=True).stdout.strip()
    checks = [
        ("completed-only", ROOT / "cmd/gohealthcli/sync_cursor.go", "return outcome == syncRunOutcomeCompleted", "return true",
         (SAFETY_TEST,), "completed-only/key-isolation invariant"),
        ("atomic-run", ROOT / "cmd/gohealthcli/health_archive_writer.go",
         "func (archive *sqliteHealthArchiveWriter) finalizeSyncRunAttempt(ctx context.Context, finalize syncRunFinalize) error {",
         "func (archive *sqliteHealthArchiveWriter) finalizeSyncRunAttempt(ctx context.Context, finalize syncRunFinalize) error {\n"
         "\tif err := finishSyncRun(ctx, archive.db, finalize.runFinish()); err != nil { return err }",
         (SAFETY_TEST,), "atomic run invariant"),
        ("key-isolation", ROOT / "cmd/gohealthcli/sync_cursor.go",
         "\t\tkey.sourceFamilyFilter,\n\t\tstring(key.rollupKind),\n\t\tto,",
         '\t\t"",\n\t\tstring(key.rollupKind),\n\t\tto,',
         (SAFETY_TEST,), "completed-only/key-isolation invariant"),
        ("transaction-released", Path(module) / "tx.go",
         "if t.c.db != 0 && sqlite3.Xsqlite3_get_autocommit(t.c.tls, t.c.db) == 0 {",
         "if false && t.c.db != 0 && sqlite3.Xsqlite3_get_autocommit(t.c.tls, t.c.db) == 0 {",
         CURSOR_CASES, "TransactionReleased:"),
    ]
    if go_check(out, "mutation-baseline", safety_only=True).returncode:
        raise RuntimeError("Safety baseline failed; do not attribute failures to mutations")
    for name, source, old, new, owners, marker in checks:
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
                if result.returncode or go_check(out, "local-driver-baseline", safety_only=True, modfile=modfile).returncode:
                    raise RuntimeError("Unmodified local SQLite module baseline failed")
                overlay_source = module_copy / "tx.go"
            replacement = scratch / (source.name + ".overlay")
            replacement.write_text(original.replace(old, new))
            overlay = scratch / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(overlay_source): str(replacement)}}))
            result = go_check(out, name, safety_only=True, overlay=overlay, modfile=modfile)
        require_mutation_failure(name, result, owners, marker)
        if source.read_text() != original:
            raise RuntimeError(f"{source} changed during mutation checking")
        print(f"DETECTED {name}", flush=True)
    if go_check(out, "mutation-restored", safety_only=True).returncode:
        raise RuntimeError("Safety baseline failed after removing overlays")


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
    results.append(sqlite.returncode)
    print(f"Evidence {out}", flush=True)
    return int(any(results))


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Verification could not complete: {error}", file=sys.stderr)
        sys.exit(2)
