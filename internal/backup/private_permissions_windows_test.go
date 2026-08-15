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
$path = $env:GOHEALTHCLI_TEST_PRIVATE_PATH
$acl = [System.IO.Directory]::GetAccessControl($path)
$everyone = New-Object System.Security.Principal.SecurityIdentifier -ArgumentList 'S-1-1-0'
$rule = New-Object System.Security.AccessControl.FileSystemAccessRule($everyone, 'Read', 'ContainerInherit, ObjectInherit', 'None', 'Allow')
$acl.AddAccessRule($rule)
[System.IO.Directory]::SetAccessControl($path, $acl)
`
	powershellPath, err := systemPowerShellPath()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), windowsACLCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, powershellPath, "-NoProfile", "-NonInteractive", "-Command", grantEveryone)
	command.Env = append(os.Environ(), "GOHEALTHCLI_TEST_PRIVATE_PATH="+dir)
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("seed broad ACL: %v", ctx.Err())
		}
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
