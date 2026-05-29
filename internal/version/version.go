// Package version exposes the gohealthcli build version as a stable string.
package version

// Version is the build version. It is overridable at build time with
// -ldflags "-X github.com/BramVR/gohealthcli/internal/version.Version=...".
var Version = "0.0.0-dev"

// String returns the stable version line printed by the --version flag.
func String() string {
	return "gohealthcli " + Version
}
