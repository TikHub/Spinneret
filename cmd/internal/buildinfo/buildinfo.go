// Package buildinfo resolves the version printed and reported by the Spinneret
// commands.
package buildinfo

import (
	"runtime/debug"

	"github.com/Evil0ctal/Spinneret/internal/version"
)

// DevVersion is the version of a build without any version information.
const DevVersion = version.DevVersion

// Version returns the effective build version (see version.String).
func Version() string {
	return resolve(version.Version, debug.ReadBuildInfo)
}

func resolve(injected string, read func() (*debug.BuildInfo, bool)) string {
	return version.Resolve(injected, read)
}
