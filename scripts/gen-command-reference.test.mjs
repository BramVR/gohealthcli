import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { renderCommand, yamlString } from "./gen-command-reference.mjs";

describe("yamlString", () => {
  it("wraps a plain string in double quotes", () => {
    assert.equal(yamlString("hello"), '"hello"');
  });
  it("escapes embedded double quotes", () => {
    assert.equal(yamlString('say "hi"'), '"say \\"hi\\""');
  });
  it("escapes newlines", () => {
    assert.equal(yamlString("first\nsecond"), '"first\\nsecond"');
  });
  it("handles null and undefined", () => {
    assert.equal(yamlString(null), '""');
    assert.equal(yamlString(undefined), '""');
  });
});

describe("renderCommand", () => {
  const sample = {
    name: "doctor",
    short: "Validate local setup.",
    long: "Run a diagnostic check.\n\nWith --online, refresh tokens.",
    hidden: false,
    flags: [
      { name: "config", type: "string", default: "", usage: "config file path" },
      { name: "online", type: "bool", default: "false", usage: "refresh tokens" },
    ],
  };

  it("emits valid frontmatter with quoted title and description", () => {
    const out = renderCommand(sample, "gohealthcli");
    assert.match(out, /^---\ntitle: "gohealthcli doctor"\ndescription: "Validate local setup\."\n---/);
  });

  it("includes the auto-gen warning comment", () => {
    const out = renderCommand(sample, "gohealthcli");
    assert.match(out, /<!-- Auto-generated from `gohealthcli schema --json`\. Do not edit by hand\. -->/);
  });

  it("includes the long description verbatim", () => {
    const out = renderCommand(sample, "gohealthcli");
    assert.ok(out.includes("Run a diagnostic check.\n\nWith --online, refresh tokens."));
  });

  it("renders flags as a markdown table with em-dash for empty defaults", () => {
    const out = renderCommand(sample, "gohealthcli");
    assert.match(out, /\| `--config` \| string \| — \| config file path \|/);
    assert.match(out, /\| `--online` \| bool \| `false` \| refresh tokens \|/);
  });

  it("escapes pipe characters in flag descriptions", () => {
    const cmd = { ...sample, flags: [{ name: "x", type: "bool", default: "false", usage: "a | b" }] };
    const out = renderCommand(cmd, "gohealthcli");
    assert.ok(out.includes("a \\| b"));
  });

  it("emits a Usage block when positional_args is present", () => {
    const cmd = { ...sample, positional_args: "<SQL>" };
    const out = renderCommand(cmd, "gohealthcli");
    assert.match(out, /## Usage\n\n```\ngohealthcli doctor <SQL>\n```/);
  });

  it("omits the description frontmatter line when short is empty", () => {
    const cmd = { ...sample, short: "" };
    const out = renderCommand(cmd, "gohealthcli");
    assert.ok(!out.match(/^description:/m));
  });

  it("falls back to short when long is empty", () => {
    const cmd = { ...sample, long: "" };
    const out = renderCommand(cmd, "gohealthcli");
    assert.ok(out.includes("Validate local setup."));
  });
});
