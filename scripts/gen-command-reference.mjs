#!/usr/bin/env node
// Read a `gohealthcli schema --json` document from stdin and write one
// markdown reference page per non-hidden subcommand under docs/commands/.
//
// Invoked by `make docs-commands`; the generated files are committed so the
// Project Site build does not need a Go toolchain at deploy time. The drift
// check (slice #74) compares the committed files against a fresh regeneration
// and fails CI if they diverge.
import fs from "node:fs";
import path from "node:path";

const EXPECTED_VERSION = 1;
const root = process.cwd();
const outDir = path.join(root, "docs", "commands");

const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
if (!chunks.length) {
  console.error("gen-command-reference: no schema JSON on stdin");
  process.exit(1);
}
const doc = JSON.parse(Buffer.concat(chunks).toString("utf8"));
if (doc.version !== EXPECTED_VERSION) {
  console.error(`gen-command-reference: schema version ${doc.version} not supported (expected ${EXPECTED_VERSION})`);
  process.exit(1);
}
if (!Array.isArray(doc.commands)) {
  console.error("gen-command-reference: schema document is missing a commands array");
  process.exit(1);
}

fs.mkdirSync(outDir, { recursive: true });

let written = 0;
for (const cmd of doc.commands) {
  if (cmd.hidden) continue;
  const filePath = path.join(outDir, `${cmd.name}.md`);
  fs.writeFileSync(filePath, renderCommand(cmd, doc.binary), "utf8");
  written += 1;
  console.log(`wrote ${path.relative(root, filePath)}`);
}
console.log(`generated ${written} command-reference page${written === 1 ? "" : "s"}`);

function renderCommand(cmd, binary) {
  const lines = [];
  lines.push("---");
  lines.push(`title: \`${binary} ${cmd.name}\``);
  lines.push(`description: ${escapeFrontmatter(cmd.short)}`);
  lines.push("---");
  lines.push("");
  lines.push("<!-- Auto-generated from `gohealthcli schema --json`. Do not edit by hand. -->");
  lines.push("");
  if (cmd.long) {
    lines.push(cmd.long.trim());
  } else if (cmd.short) {
    lines.push(cmd.short);
  }

  if (cmd.positional_args) {
    lines.push("");
    lines.push("## Usage");
    lines.push("");
    lines.push("```");
    lines.push(`${binary} ${cmd.name} ${cmd.positional_args}`);
    lines.push("```");
  }

  if (Array.isArray(cmd.flags) && cmd.flags.length) {
    lines.push("");
    lines.push("## Flags");
    lines.push("");
    lines.push("| Flag | Type | Default | Description |");
    lines.push("| ---- | ---- | ------- | ----------- |");
    for (const f of cmd.flags) {
      const def = f.default === "" ? "—" : "`" + f.default + "`";
      lines.push(`| \`--${f.name}\` | ${f.type} | ${def} | ${escapeCell(f.usage)} |`);
    }
  }

  lines.push("");
  return lines.join("\n");
}

function escapeFrontmatter(text) {
  if (text == null) return "";
  return text.replace(/"/g, '\\"');
}

function escapeCell(text) {
  if (text == null) return "";
  return text.replace(/\|/g, "\\|").replace(/\r?\n/g, " ");
}
