package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type credentialStoreConfig struct {
	kind    string
	service string
	path    string
}

func defaultCredentialStoreConfig() credentialStoreConfig {
	return credentialStoreConfig{kind: "os_native", service: "gohealthcli"}
}

func validateCredentialStoreConfig(store credentialStoreConfig) error {
	switch store.kind {
	case "os_native":
		if store.service == "" {
			return errors.New("missing Credential Store service name")
		}
	case "file":
		if store.path == "" {
			return errors.New("missing Credential Store file path")
		}
		parent := filepath.Dir(store.path)
		if _, err := os.Stat(parent); err == nil {
			if err := validateOwnerOnlyDir(parent); err != nil {
				return fmt.Errorf("Credential Store file parent: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if info, err := os.Stat(store.path); err == nil {
			if info.IsDir() {
				return fmt.Errorf("%s is a directory", store.path)
			}
			if usesPOSIXPermissions() && info.Mode().Perm() != 0o600 {
				return fmt.Errorf("%s is not owner-only: mode %04o, want 0600", store.path, info.Mode().Perm())
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	case "":
		return errors.New("missing Credential Store configuration")
	default:
		return errors.New("unsupported Credential Store type")
	}
	return nil
}

func validateCredentialStoreRuntimeWithRuntime(store credentialStoreConfig, protectedPaths []string, runtime runtimeAdapters) error {
	runtime = runtime.withDefaults()
	switch store.kind {
	case "file":
		storePath, err := canonicalCredentialPath(store.path)
		if err != nil {
			return err
		}
		for _, protectedPath := range protectedPaths {
			if protectedPath == "" {
				continue
			}
			checkedPath, err := canonicalCredentialPath(protectedPath)
			if err != nil {
				return err
			}
			if storePath == checkedPath {
				return errors.New("Credential Store file path must not match config, archive, or OAuth client files")
			}
		}
	case "os_native":
		switch runtime.currentOS {
		case "darwin":
			if _, err := runtime.findExecutable("security"); err != nil {
				return errors.New("OS-native Credential Store requires the security command; configure credential_store type \"file\"")
			}
		case "linux":
			if _, err := runtime.findExecutable("secret-tool"); err != nil {
				return errors.New("OS-native Credential Store requires secret-tool; install libsecret tooling or configure credential_store type \"file\"")
			}
		case "windows":
			if _, err := runtime.findExecutable("powershell"); err != nil {
				if _, err := runtime.findExecutable("powershell.exe"); err != nil {
					return errors.New("OS-native Credential Store requires PowerShell; configure credential_store type \"file\"")
				}
			}
		}
	}
	return nil
}

func canonicalCredentialPath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolvedPath, err := filepath.EvalSymlinks(absolutePath); err == nil {
		return filepath.Clean(resolvedPath), nil
	}
	parent := filepath.Dir(absolutePath)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		resolvedParent = parent
	}
	return filepath.Clean(filepath.Join(resolvedParent, filepath.Base(absolutePath))), nil
}

type credentialStore interface {
	Store(key string, tokenMaterial map[string]any) error
	Load(key string) (map[string]any, error)
}

// errCredentialStoreTokenMaterialNotFound is the sentinel every
// Credential Store backend returns when no token material exists for
// the Connection key — missing store file, missing key, or an absent
// OS-native secret. Callers (doctor's token_missing classification)
// branch on it via errors.Is; matching on the message text is
// forbidden (issue #272).
var errCredentialStoreTokenMaterialNotFound = errors.New("Credential Store token material not found; run `gohealthcli connect` first")

func newCredentialStoreWithRuntime(config credentialStoreConfig, runtime runtimeAdapters) (credentialStore, error) {
	runtime = runtime.withDefaults()
	switch config.kind {
	case "file":
		return fileCredentialStore{path: config.path}, nil
	case "os_native":
		if runtime.currentOS != "darwin" && runtime.currentOS != "linux" && runtime.currentOS != "windows" {
			return nil, errors.New("OS-native Credential Store is not available on this platform; configure credential_store type \"file\"")
		}
		return osNativeCredentialStore{service: config.service, runtime: runtime}, nil
	default:
		return nil, errors.New("unsupported Credential Store type")
	}
}

type fileCredentialStore struct {
	path string
}

func (store fileCredentialStore) Store(key string, tokenMaterial map[string]any) error {
	if err := ensureOwnerOnlyDir(filepath.Dir(store.path)); err != nil {
		return err
	}
	existing := map[string]any{}
	if content, err := os.ReadFile(store.path); err == nil && len(content) > 0 {
		if err := json.Unmarshal(content, &existing); err != nil {
			return errors.New("Credential Store file is not valid JSON")
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing[key] = tokenMaterial
	content, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := os.WriteFile(store.path, content, 0o600); err != nil {
		return err
	}
	if !usesPOSIXPermissions() {
		return nil
	}
	return os.Chmod(store.path, 0o600)
}

func (store fileCredentialStore) Load(key string) (map[string]any, error) {
	content, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errCredentialStoreTokenMaterialNotFound
		}
		return nil, err
	}
	var existing map[string]json.RawMessage
	if err := json.Unmarshal(content, &existing); err != nil {
		return nil, errors.New("Credential Store file is not valid JSON")
	}
	raw, ok := existing[key]
	if !ok {
		return nil, errCredentialStoreTokenMaterialNotFound
	}
	var tokenMaterial map[string]any
	if err := json.Unmarshal(raw, &tokenMaterial); err != nil {
		return nil, errors.New("Credential Store token material is not valid JSON")
	}
	return tokenMaterial, nil
}

type osNativeCredentialStore struct {
	service string
	runtime runtimeAdapters
}

func (store osNativeCredentialStore) Store(key string, tokenMaterial map[string]any) error {
	content, err := json.Marshal(tokenMaterial)
	if err != nil {
		return err
	}
	runtime := store.runtime.withDefaults()
	// context.Background(): Credential Store access is a synchronous
	// local subprocess with no cancellation path today; the context
	// keeps the exec invocations on the Context API (#305). A future
	// producer threads through the runtime adapters seam.
	ctx := context.Background()
	switch runtime.currentOS {
	case "darwin":
		return runtime.runSecurityAddGenericPassword(ctx, store.service, key, content)
	case "linux":
		return runtime.runSecretToolStore(ctx, store.service, key, content)
	case "windows":
		return runtime.runWindowsCredentialWrite(ctx, store.service, key, content)
	default:
		return errors.New("OS-native Credential Store is not available on this platform; configure credential_store type \"file\"")
	}
}

func (store osNativeCredentialStore) Load(key string) (map[string]any, error) {
	var content []byte
	var err error
	runtime := store.runtime.withDefaults()
	// context.Background(): same rationale as Store above (#305).
	ctx := context.Background()
	switch runtime.currentOS {
	case "darwin":
		content, err = runtime.runSecurityFindGenericPassword(ctx, store.service, key)
	case "linux":
		content, err = runtime.runSecretToolLookup(ctx, store.service, key)
	case "windows":
		content, err = runtime.runWindowsCredentialRead(ctx, store.service, key)
	default:
		return nil, errors.New("OS-native Credential Store is not available on this platform; configure credential_store type \"file\"")
	}
	if err != nil {
		return nil, err
	}
	var tokenMaterial map[string]any
	if err := json.Unmarshal(content, &tokenMaterial); err != nil {
		return nil, errors.New("Credential Store token material is not valid JSON")
	}
	return tokenMaterial, nil
}

func runSecurityAddGenericPasswordCommand(ctx context.Context, service, key string, content []byte) error {
	cmd := exec.CommandContext(ctx, "security", "add-generic-password", "-U", "-s", service, "-a", key, "-w")
	password := string(content)
	cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
	return cmd.Run()
}

func runSecurityFindGenericPasswordCommand(ctx context.Context, service, key string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", key, "-w")
	output, err := cmd.Output()
	if err != nil {
		return nil, errCredentialStoreTokenMaterialNotFound
	}
	return []byte(strings.TrimSpace(string(output))), nil
}

func runSecretToolStoreCommand(ctx context.Context, service, key string, content []byte) error {
	cmd := exec.CommandContext(ctx, "secret-tool", "store", "--label", service, "service", service, "account", key)
	cmd.Stdin = strings.NewReader(string(content))
	return cmd.Run()
}

func runSecretToolLookupCommand(ctx context.Context, service, key string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "secret-tool", "lookup", "service", service, "account", key)
	output, err := cmd.Output()
	if err != nil {
		return nil, errCredentialStoreTokenMaterialNotFound
	}
	return []byte(strings.TrimSpace(string(output))), nil
}

func runWindowsCredentialWriteCommand(ctx context.Context, service, key string, content []byte) error {
	target := service + ":" + key
	script := `
$secret = [Console]::In.ReadToEnd()
$code = @"
using System;
using System.Runtime.InteropServices;
public static class NativeCredential {
  [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
  public struct CREDENTIAL {
    public UInt32 Flags;
    public UInt32 Type;
    public string TargetName;
    public string Comment;
    public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
    public UInt32 CredentialBlobSize;
    public IntPtr CredentialBlob;
    public UInt32 Persist;
    public UInt32 AttributeCount;
    public IntPtr Attributes;
    public string TargetAlias;
    public string UserName;
  }
  [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
  public static extern bool CredWrite(ref CREDENTIAL credential, UInt32 flags);
}
"@
Add-Type $code
$bytes = [Text.Encoding]::Unicode.GetBytes($secret)
$blob = [Runtime.InteropServices.Marshal]::AllocHGlobal($bytes.Length)
try {
  [Runtime.InteropServices.Marshal]::Copy($bytes, 0, $blob, $bytes.Length)
  $credential = New-Object NativeCredential+CREDENTIAL
  $credential.Type = 1
  $credential.TargetName = $env:GOHEALTHCLI_CREDENTIAL_TARGET
  $credential.UserName = $env:GOHEALTHCLI_CREDENTIAL_ACCOUNT
  $credential.CredentialBlobSize = $bytes.Length
  $credential.CredentialBlob = $blob
  $credential.Persist = 2
  if (-not [NativeCredential]::CredWrite([ref]$credential, 0)) {
    throw [ComponentModel.Win32Exception][Runtime.InteropServices.Marshal]::GetLastWin32Error()
  }
} finally {
  [Runtime.InteropServices.Marshal]::FreeHGlobal($blob)
}
`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "GOHEALTHCLI_CREDENTIAL_TARGET="+target, "GOHEALTHCLI_CREDENTIAL_ACCOUNT="+key)
	cmd.Stdin = strings.NewReader(string(content))
	return cmd.Run()
}

func runWindowsCredentialReadCommand(ctx context.Context, service, key string) ([]byte, error) {
	target := service + ":" + key
	script := `
$code = @"
using System;
using System.Runtime.InteropServices;
public static class NativeCredential {
  [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
  public struct CREDENTIAL {
    public UInt32 Flags;
    public UInt32 Type;
    public string TargetName;
    public string Comment;
    public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
    public UInt32 CredentialBlobSize;
    public IntPtr CredentialBlob;
    public UInt32 Persist;
    public UInt32 AttributeCount;
    public IntPtr Attributes;
    public string TargetAlias;
    public string UserName;
  }
  [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
  public static extern bool CredRead(string target, UInt32 type, UInt32 reservedFlag, out IntPtr credentialPtr);
  [DllImport("advapi32.dll", SetLastError = true)]
  public static extern void CredFree(IntPtr buffer);
}
"@
Add-Type $code
$utf8 = [Text.UTF8Encoding]::new($false)
$credentialPtr = [IntPtr]::Zero
if (-not [NativeCredential]::CredRead($env:GOHEALTHCLI_CREDENTIAL_TARGET, 1, 0, [ref]$credentialPtr)) {
  throw [ComponentModel.Win32Exception][Runtime.InteropServices.Marshal]::GetLastWin32Error()
}
try {
  $credential = [Runtime.InteropServices.Marshal]::PtrToStructure($credentialPtr, [type][NativeCredential+CREDENTIAL])
  $bytes = New-Object byte[] $credential.CredentialBlobSize
  [Runtime.InteropServices.Marshal]::Copy($credential.CredentialBlob, $bytes, 0, $credential.CredentialBlobSize)
  $credentialJson = [Text.Encoding]::Unicode.GetString($bytes)
  $stdout = [Console]::OpenStandardOutput()
  $outputBytes = $utf8.GetBytes($credentialJson)
  $stdout.Write($outputBytes, 0, $outputBytes.Length)
} finally {
  [NativeCredential]::CredFree($credentialPtr)
}
`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "GOHEALTHCLI_CREDENTIAL_TARGET="+target)
	output, err := cmd.Output()
	if err != nil {
		return nil, errCredentialStoreTokenMaterialNotFound
	}
	return []byte(strings.TrimSpace(string(output))), nil
}
