package main

import (
	"bytes"
	"flag"
	"testing"
)

// captureSubcommandFlagSet drives the named registry entry's real Run
// adapter with `--help` and returns the fully registered FlagSet the
// subcommand built, captured via the observeSubcommandFlagSet seam.
// `--help` short-circuits at parse time, so the invocation never touches
// the filesystem, the archive, or the provider — but the FlagSet we
// observe is exactly the one production dispatch would parse.
func captureSubcommandFlagSet(t *testing.T, cmd commandDef) *flag.FlagSet {
	t.Helper()
	var captured *flag.FlagSet
	observeSubcommandFlagSet = func(fs *flag.FlagSet) {
		if fs.Name() == cmd.Name && captured == nil {
			captured = fs
		}
	}
	t.Cleanup(func() { observeSubcommandFlagSet = nil })

	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	if code := cmd.Run([]string{"--help"}, CommonFlagValues{}, stdout, stderr, runtimeAdapters{}); code != 0 {
		t.Fatalf("`%s --help` exit code = %d, want 0\nstderr: %s", cmd.Name, code, stderr.String())
	}
	if captured == nil {
		t.Fatalf("command %q never surfaced its FlagSet to the drift-test observer; is its parse path wired through ParseCommon (or notifySubcommandFlagSetObserver)?", cmd.Name)
	}
	return captured
}

// runtimeFlagType maps a runtime flag.Flag to the registry's flagSpec
// Type vocabulary ("string" | "bool" | "int"). flag.UnquoteUsage is the
// stdlib's own type-name derivation: it reports "" for bool flags (they
// take no value) and already collapses int/int64 to "int" — the same
// width-agnostic vocabulary the published schema uses, where an integer
// flag's bit width is an implementation detail.
func runtimeFlagType(f *flag.Flag) string {
	name, _ := flag.UnquoteUsage(f)
	if name == "" {
		return "bool"
	}
	return name
}

// TestRegisterCommonBindsCommonFlagsSpec pins the single-source-of-truth
// contract of issue #76: a FlagSet bound via RegisterCommon must report
// (via flag.VisitAll) exactly the flags that commonFlagsSpec declares —
// same names, same types, same usage strings. Because the registry's
// withCommon* helpers project the same spec into every commandDef.Flags
// slice, rewording a shared flag in commonFlagsSpec moves the runtime
// --help output and the published schema together; hand-editing either
// side alone is no longer possible.
func TestRegisterCommonBindsCommonFlagsSpec(t *testing.T) {
	if len(commonFlagsSpec) != 5 {
		t.Fatalf("commonFlagsSpec declares %d flags, want the 5 shared flags", len(commonFlagsSpec))
	}

	fs := flag.NewFlagSet("spec-probe", flag.ContinueOnError)
	RegisterCommon(fs, AllCommonFlagsSpec(), CommonFlagValues{})

	got := make(map[string]flagSpec)
	fs.VisitAll(func(f *flag.Flag) {
		got[f.Name] = flagSpec{Name: f.Name, Type: runtimeFlagType(f), Usage: f.Usage}
	})
	if len(got) != len(commonFlagsSpec) {
		t.Errorf("RegisterCommon bound %d flags, want %d (one per commonFlagsSpec entry)", len(got), len(commonFlagsSpec))
	}
	for _, spec := range commonFlagsSpec {
		g, ok := got[spec.Name]
		if !ok {
			t.Errorf("commonFlagsSpec declares --%s but RegisterCommon did not bind it", spec.Name)
			continue
		}
		if g.Type != spec.Type {
			t.Errorf("--%s type: bound %q, spec %q", spec.Name, g.Type, spec.Type)
		}
		if g.Usage != spec.Usage {
			t.Errorf("--%s usage: bound %q, spec %q", spec.Name, g.Usage, spec.Usage)
		}
	}
}

// TestRegisterCommonAppliesUsageOverrides pins the override seam: a
// subcommand whose shared-flag semantics diverge from the generic
// wording (export's `--json` is a --format synonym, for example)
// declares the divergence ONCE in a map that both its registry entry
// (via withCommonOverrides) and its runtime spec consume. This test
// covers the runtime half: an override in CommonFlagSpec.UsageOverrides
// must reach the bound FlagSet, while non-overridden flags keep the
// canonical commonFlagsSpec wording.
func TestRegisterCommonAppliesUsageOverrides(t *testing.T) {
	fs := flag.NewFlagSet("override-probe", flag.ContinueOnError)
	spec := AllCommonFlagsSpec()
	spec.UsageOverrides = map[string]string{"json": "synonym for --format jsonl"}
	RegisterCommon(fs, spec, CommonFlagValues{})

	if got := fs.Lookup("json").Usage; got != "synonym for --format jsonl" {
		t.Errorf("--json usage = %q, want the override", got)
	}
	want := ""
	for _, f := range commonFlagsSpec {
		if f.Name == "plain" {
			want = f.Usage
		}
	}
	if got := fs.Lookup("plain").Usage; got != want {
		t.Errorf("--plain usage = %q, want canonical spec wording %q", got, want)
	}
}

// TestEveryCommandFlagSetMatchesRegistryFlags is the issue #76 drift
// guard: for every registry entry — visible or hidden — the runtime
// FlagSet its Run adapter actually builds (walked via flag.VisitAll)
// must advertise the same flags, with the same types and usage strings,
// as the commandDef.Flags slice that `schema --json` and the generated
// command-reference pages publish. A hand-typed usage string drifting in
// either direction fails here instead of shipping a binary whose --help
// disagrees with the Project Site.
func TestEveryCommandFlagSetMatchesRegistryFlags(t *testing.T) {
	for _, cmd := range commands {
		t.Run(cmd.Name, func(t *testing.T) {
			fs := captureSubcommandFlagSet(t, cmd)

			got := make(map[string]flagSpec)
			fs.VisitAll(func(f *flag.Flag) {
				got[f.Name] = flagSpec{Name: f.Name, Type: runtimeFlagType(f), Usage: f.Usage}
			})

			want := make(map[string]flagSpec, len(cmd.Flags))
			for _, spec := range cmd.Flags {
				if _, dup := want[spec.Name]; dup {
					t.Errorf("registry advertises --%s twice for %s", spec.Name, cmd.Name)
				}
				want[spec.Name] = spec
			}

			for name, w := range want {
				g, ok := got[name]
				if !ok {
					t.Errorf("registry advertises --%s but the %s FlagSet does not define it", name, cmd.Name)
					continue
				}
				if g.Type != w.Type {
					t.Errorf("--%s type drift: runtime %q, registry %q", name, g.Type, w.Type)
				}
				if g.Usage != w.Usage {
					t.Errorf("--%s usage drift:\n  runtime:  %q\n  registry: %q", name, g.Usage, w.Usage)
				}
			}
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("the %s FlagSet defines --%s but the registry does not advertise it", cmd.Name, name)
				}
			}
		})
	}
}
