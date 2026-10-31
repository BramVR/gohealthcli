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
const VALID_COMMAND_NAME = /^[a-z][a-z0-9-]*$/;

// PRESERVED_DOC_FILES lists hand-written pages under docs/commands/ that the
// regenerator must NOT delete. They document discoverability surfaces that
// live outside the Command Registry: `help` is a top-level dispatch verb (no
// registry entry), and `version` is a top-level flag (no registry entry).
// Both pages are referenced from README.md and docs/quickstart.md; deleting
// them on every `make docs-commands` would break those links and silently
// drop the surface from the Project Site sidebar. Exported so the drift
// check (check-command-reference.mjs) skips the same files when comparing
// committed pages against a fresh regeneration.
export const PRESERVED_DOC_FILES = new Set(["help.md", "version.md"]);

export function renderIndex(commands, binary) {
  const visible = commands.filter((c) => !c.hidden);
  const lines = [];
  lines.push("---");
  lines.push(`title: ${yamlString("Command reference")}`);
  lines.push(`description: ${yamlString(`Every ${binary} subcommand at a stable URL.`)}`);
  lines.push("---");
  lines.push("");
  lines.push(`Every user-facing subcommand exposed by \`${binary}\`. Pages are regenerated from the binary by \`make docs-commands\`; the committed copies must match a fresh regeneration.`);
  lines.push("");
  lines.push("## Subcommands");
  lines.push("");
  for (const cmd of visible) {
    const short = cmd.short ? ` — ${cmd.short}` : "";
    lines.push(`- [\`${binary} ${cmd.name}\`](commands/${cmd.name}.html)${short}`);
  }
  lines.push("");
  // Discoverability surfaces that live outside the Command Registry. The
  // pages themselves are hand-written and preserved by the generator (see
  // PRESERVED_DOC_FILES); linking them here keeps the sidebar and reference
  // index honest about every flag and verb the binary actually exposes.
  lines.push("## Discoverability");
  lines.push("");
  lines.push(`- [\`${binary} help\`](commands/help.html) — discoverability verb (\`help\`, \`help <command>\`, did-you-mean).`);
  lines.push(`- [\`${binary} --version\`](commands/version.html) — build-stamped version, commit, and built identifiers (plain and JSON shapes).`);
  lines.push("");
  return lines.join("\n");
}

export function renderCommand(cmd, binary) {
  const lines = [];
  lines.push("---");
  lines.push(`title: ${yamlString(`${binary} ${cmd.name}`)}`);
  if (cmd.short) lines.push(`description: ${yamlString(cmd.short)}`);
  lines.push("---");
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

// JSON string literals are a valid subset of YAML double-quoted flow scalars,
// so JSON.stringify gives us correct escaping for quotes, newlines, and other
// metacharacters without bespoke logic.
export function yamlString(text) {
  return JSON.stringify(text ?? "");
}

function escapeCell(text) {
  if (text == null) return "";
  return text.replace(/\|/g, "\\|").replace(/\r?\n/g, " ");
}

// Skip the CLI work when imported (e.g. by tests). Detect direct invocation
// by comparing the resolved script URL to the entry-point argv.
const invokedDirectly = import.meta.url === `file://${process.argv[1]}`;
if (invokedDirectly) {
  await main();
}

async function main() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  if (!chunks.length) {
    console.error("gen-command-reference: no schema JSON on stdin");
    process.exit(1);
  }

  let doc;
  try {
    doc = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch (err) {
    console.error(`gen-command-reference: stdin is not valid JSON (${err.message})`);
    process.exit(1);
  }

  if (doc.version !== EXPECTED_VERSION) {
    console.error(`gen-command-reference: schema version ${doc.version} not supported (expected ${EXPECTED_VERSION})`);
    process.exit(1);
  }
  if (!Array.isArray(doc.commands)) {
    console.error("gen-command-reference: schema document is missing a commands array");
    process.exit(1);
  }

  const root = process.cwd();
  const docsDir = path.join(root, "docs");
  const outDir = path.join(docsDir, "commands");

  // Clear the directory before writing so a removed command does not leave a
  // stale page behind. Anything outside docs/commands/ is never touched.
  // PRESERVED_DOC_FILES (e.g. help.md, version.md) survive the wipe: they
  // document discoverability surfaces that live outside the Command Registry
  // and are not regenerated from schema --json.
  if (fs.existsSync(outDir)) {
    for (const entry of fs.readdirSync(outDir, { withFileTypes: true })) {
      if (entry.isFile() && PRESERVED_DOC_FILES.has(entry.name)) continue;
      fs.rmSync(path.join(outDir, entry.name), { recursive: true, force: true });
    }
  } else {
    fs.mkdirSync(outDir, { recursive: true });
  }

  let written = 0;
  for (const cmd of doc.commands) {
    if (cmd.hidden) continue;
    if (!VALID_COMMAND_NAME.test(cmd.name)) {
      console.error(`gen-command-reference: invalid command name ${JSON.stringify(cmd.name)}`);
      process.exit(1);
    }
    const filePath = path.join(outDir, `${cmd.name}.md`);
    fs.writeFileSync(filePath, renderCommand(cmd, doc.binary), "utf8");
    written += 1;
    console.log(`wrote ${path.relative(root, filePath)}`);
  }

  const indexPath = path.join(docsDir, "commands.md");
  fs.writeFileSync(indexPath, renderIndex(doc.commands, doc.binary), "utf8");
  console.log(`wrote ${path.relative(root, indexPath)}`);

  console.log(`generated ${written} command-reference page${written === 1 ? "" : "s"} + index`);
}
