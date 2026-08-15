//go:build windows

package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsPrivatePathHardeningRemovesEveryoneAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const grantEveryone = `$ErrorActionPreference = 'Stop'
$acl = [System.IO.Directory]::GetAccessControl($args[0])
$everyone = New-Object System.Security.Principal.SecurityIdentifier -ArgumentList 'S-1-1-0'
$rule = New-Object System.Security.AccessControl.FileSystemAccessRule($everyone, 'Read', 'ContainerInherit, ObjectInherit', 'None', 'Allow')
$acl.AddAccessRule($rule)
[System.IO.Directory]::SetAccessControl($args[0], $acl)
`
	command := exec.CommandContext(context.Background(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", grantEveryone, dir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("seed broad ACL: %v: %s", err, output)
	}
	if err := hardenPrivatePath(dir, true); err != nil {
		t.Fatalf("harden directory: %v", err)
	}
	if err := validatePlatformPrivatePath(dir, true); err != nil {
		t.Fatalf("validate hardened directory: %v", err)
	}

	identity := filepath.Join(dir, "identity.txt")
	if err := os.WriteFile(identity, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := hardenPrivatePath(identity, false); err != nil {
		t.Fatalf("harden file: %v", err)
	}
	if err := validatePlatformPrivatePath(identity, false); err != nil {
		t.Fatalf("validate hardened file: %v", err)
	}
}
