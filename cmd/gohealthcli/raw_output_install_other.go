//go:build !darwin && !linux && !windows

package main

import "fmt"

func publishStagedRawOutput(stagedPath, targetPath string) error {
	return fmt.Errorf("raw --output is unsupported on this platform because atomic no-replace rename is unavailable")
}
