//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRawOutputRejectsMacOSParentACL(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "acl-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/bin/chmod", "+a", "everyone allow add_file,delete_child,file_inherit", parent).CombinedOutput(); err != nil {
		t.Fatalf("add parent ACL: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("/bin/chmod", "-N", parent).Run() })

	_, err := prepareRawOutputFile(filepath.Join(parent, "response.json"))
	var validationError *rawOutputValidationError
	if !errors.As(err, &validationError) || !strings.Contains(err.Error(), "extended ACL") {
		t.Fatalf("prepare raw output error = %v, want macOS ACL refusal", err)
	}
	stagingFiles, globErr := filepath.Glob(filepath.Join(parent, ".gohealthcli-raw-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(stagingFiles) != 0 {
		t.Fatalf("ACL refusal left staging files: %v", stagingFiles)
	}
}
