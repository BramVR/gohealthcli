package main

import (
	"context"
	"errors"
	osexec "os/exec"
	goruntime "runtime"
	"testing"
)

// TestCredentialStoreCommandsHonorCanceledContext pins the
// noctx-completion slice (#305) at the os/exec seam: the OS-native
// Credential Store subprocess invocations ride the caller's context via
// exec.CommandContext, so a canceled context aborts before the
// subprocess launches. Asserted against the current platform's write
// command (the one production would run); skipped when the platform
// binary is not installed, since LookPath failures surface before the
// context check in os/exec.
func TestCredentialStoreCommandsHonorCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var binary string
	var run func() error
	switch goruntime.GOOS {
	case "darwin":
		binary = "security"
		run = func() error {
			return runSecurityAddGenericPasswordCommand(ctx, "gohealthcli-test-noctx", "cancel-probe", []byte("never-written"))
		}
	case "linux":
		binary = "secret-tool"
		run = func() error {
			return runSecretToolStoreCommand(ctx, "gohealthcli-test-noctx", "cancel-probe", []byte("never-written"))
		}
	case "windows":
		binary = "powershell.exe"
		run = func() error {
			return runWindowsCredentialWriteCommand(ctx, "gohealthcli-test-noctx", "cancel-probe", []byte("never-written"))
		}
	default:
		t.Skipf("no OS-native Credential Store on %s", goruntime.GOOS)
	}
	if _, err := osexec.LookPath(binary); err != nil {
		t.Skipf("%s not installed: %v", binary, err)
	}
	if err := run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("credential store write with canceled context = %v, want context.Canceled (subprocess must not launch)", err)
	}
}

// TestOpenBrowserHonorsCanceledContext: the browser-open helper rides
// the caller's context the same way (#305) — a canceled context aborts
// before any browser process is spawned.
func TestOpenBrowserHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	var binary string
	switch goruntime.GOOS {
	case "darwin":
		binary = "open"
	case "windows":
		binary = "rundll32"
	default:
		binary = "xdg-open"
	}
	if _, err := osexec.LookPath(binary); err != nil {
		t.Skipf("%s not installed: %v", binary, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := openBrowser(ctx, "https://example.invalid/never-opened"); !errors.Is(err, context.Canceled) {
		t.Fatalf("openBrowser with canceled context = %v, want context.Canceled (no browser must be spawned)", err)
	}
}
