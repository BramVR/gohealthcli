//go:build !darwin && !linux && !windows

package main

import "fmt"

func rawOutputPlatformSupported() error {
	return fmt.Errorf("raw --output is unsupported on this platform because atomic no-replace rename is unavailable")
}

func openPlatformRawOutput(string) (rawOutputDestination, error) {
	return nil, rawOutputPlatformSupported()
}
