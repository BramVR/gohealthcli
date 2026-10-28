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


ROOT = Path(__file__).resolve().parents[2]
SPEC = Path(__file__).resolve().parent
TOOLS = ROOT / "dist/formal-tools"
JAR = TOOLS / "tla2tools-1.7.4.jar"
JAR_SHA256 = "936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88"
JAR_URL = "https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar"
GO_ENV = {**os.environ, "GOTOOLCHAIN": "auto"}


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
    names = ["TestFormalSyncCursor" + suffix for suffix in
             ["Safety", "CommitFailure"] + ([] if safety_only else ["Highwater"])]
    for test in names:
        if terminal.get(test) not in ("pass", "fail"):
            raise RuntimeError(f"{name}: missing named result for {test}")
    failures = {test for test, action in terminal.items() if action == "fail"}
    if result.returncode:
        if result.returncode != 1 or not failures:
            raise RuntimeError(f"{name}: Go checks did not reach a verification assertion; read {name}.out")
        markers = {
            "TestFormalSyncCursorSafety": ("atomic run invariant", "completed-only/key-isolation invariant", "finalize outcome invariant"),
            "TestFormalSyncCursorCommitFailure": ("TransactionReleased:",),
            "TestFormalSyncCursorHighwater": ("CursorHighwater:",),
        }
        for test in sorted(failures):
            output = "".join(event.get("Output", "") for event in events
                             if event.get("Test") == test)
            diagnostics = "".join(line for line in output.splitlines(keepends=True)
                                  if not line.lstrip().startswith(("=== ", "--- ")))
            if any(marker in diagnostics for marker in markers.get(test.split("/", 1)[0], ())):
                continue
            if not diagnostics.strip() and any(child.startswith(test + "/") for child in failures):
                continue
            raise RuntimeError(f"{test} failed outside a named invariant; read {name}.out")
    elif any(terminal[test] != "pass" for test in names) or failures:
        raise RuntimeError(f"{name}: missing named PASS result; read {name}.out")
    return result


def mutations(out):
    module = subprocess.run(["go", "list", "-m", "-f", "{{.Dir}}", "modernc.org/sqlite"],
                            cwd=ROOT, env=GO_ENV, text=True, capture_output=True, check=True).stdout.strip()
    checks = [
        ("completed-only", ROOT / "cmd/gohealthcli/sync_cursor.go", "return outcome == syncRunOutcomeCompleted", "return true",
         "completed-only/key-isolation invariant"),
        ("atomic-run", ROOT / "cmd/gohealthcli/health_archive_writer.go",
         "func (archive *sqliteHealthArchiveWriter) finalizeSyncRunAttempt(ctx context.Context, finalize syncRunFinalize) error {",
         "func (archive *sqliteHealthArchiveWriter) finalizeSyncRunAttempt(ctx context.Context, finalize syncRunFinalize) error {\n"
         "\tif err := finishSyncRun(ctx, archive.db, finalize.runFinish()); err != nil { return err }",
         "atomic run invariant"),
        ("key-isolation", ROOT / "cmd/gohealthcli/sync_cursor.go",
         "\t\tkey.sourceFamilyFilter,\n\t\tstring(key.rollupKind),\n\t\tto,",
         '\t\t"",\n\t\tstring(key.rollupKind),\n\t\tto,',
         "completed-only/key-isolation invariant"),
        ("transaction-released", Path(module) / "tx.go",
         "if t.c.db != 0 && sqlite3.Xsqlite3_get_autocommit(t.c.tls, t.c.db) == 0 {",
         "if false && t.c.db != 0 && sqlite3.Xsqlite3_get_autocommit(t.c.tls, t.c.db) == 0 {",
         "TransactionReleased:"),
    ]
    if go_check(out, "mutation-baseline", safety_only=True).returncode:
        raise RuntimeError("Safety baseline failed; do not attribute failures to mutations")
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
                if result.returncode or go_check(out, "local-driver-baseline", safety_only=True, modfile=modfile).returncode:
                    raise RuntimeError("Unmodified local SQLite module baseline failed")
                overlay_source = module_copy / "tx.go"
            replacement = scratch / (source.name + ".overlay")
            replacement.write_text(original.replace(old, new))
            overlay = scratch / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(overlay_source): str(replacement)}}))
            result = go_check(out, name, safety_only=True, overlay=overlay, modfile=modfile)
        if result.returncode != 1 or expected not in result.stdout:
            raise RuntimeError(f"{name}: did not fail on its intended invariant")
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
