//go:build windows

package backup

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const windowsHardenPrivatePathScript = `$ErrorActionPreference = 'Stop'
$path = $args[0]
$isDirectory = [System.Convert]::ToBoolean($args[1])
$current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
if ($isDirectory) {
  $acl = New-Object System.Security.AccessControl.DirectorySecurity
  $inheritance = [System.Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
} else {
  $acl = New-Object System.Security.AccessControl.FileSecurity
  $inheritance = [System.Security.AccessControl.InheritanceFlags]::None
}
$acl.SetOwner($current)
$acl.SetAccessRuleProtection($true, $false)
$propagation = [System.Security.AccessControl.PropagationFlags]::None
$rights = [System.Security.AccessControl.FileSystemRights]::FullControl
$allow = [System.Security.AccessControl.AccessControlType]::Allow
$system = New-Object System.Security.Principal.SecurityIdentifier -ArgumentList 'S-1-5-18'
$administrators = New-Object System.Security.Principal.SecurityIdentifier -ArgumentList 'S-1-5-32-544'
foreach ($sid in @($current, $system, $administrators)) {
  $acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($sid, $rights, $inheritance, $propagation, $allow)))
}
if ($isDirectory) {
  [System.IO.Directory]::SetAccessControl($path, $acl)
} else {
  [System.IO.File]::SetAccessControl($path, $acl)
}
`

const windowsValidatePrivatePathScript = `$ErrorActionPreference = 'Stop'
$path = $args[0]
$isDirectory = [System.Convert]::ToBoolean($args[1])
$current = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$allowed = @($current, 'S-1-5-18', 'S-1-5-32-544')
if ($isDirectory) {
  $acl = [System.IO.Directory]::GetAccessControl($path)
} else {
  $acl = [System.IO.File]::GetAccessControl($path)
}
if (-not $acl.AreAccessRulesProtected) { throw 'ACL inheritance is enabled' }
$owner = ([System.Security.Principal.NTAccount]$acl.Owner).Translate([System.Security.Principal.SecurityIdentifier]).Value
if ($allowed -notcontains $owner) { throw 'owner is not the current user, SYSTEM, or Administrators' }
$bad = @($acl.Access | Where-Object {
  $_.AccessControlType -eq [System.Security.AccessControl.AccessControlType]::Allow -and
  $allowed -notcontains $_.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
})
if ($bad.Count -ne 0) { throw 'ACL grants access to another identity' }
`

func hardenPrivatePath(path string, isDirectory bool) error {
	if err := runWindowsACLScript(windowsHardenPrivatePathScript, path, isDirectory); err != nil {
		return fmt.Errorf("enforce owner-only Windows ACL for %s: %w", path, err)
	}
	return validatePlatformPrivatePath(path, isDirectory)
}

func validatePlatformPrivatePath(path string, isDirectory bool) error {
	if err := runWindowsACLScript(windowsValidatePrivatePathScript, path, isDirectory); err != nil {
		return fmt.Errorf("%s is not owner-only: %w", path, err)
	}
	return nil
}

func runWindowsACLScript(script, path string, isDirectory bool) error {
	command := exec.CommandContext(context.Background(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script, path, fmt.Sprint(isDirectory))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
