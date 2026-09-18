// Package version exposes build metadata injected at link time.
package version

import "runtime/debug"

// Version is overridden with -ldflags "-X .../internal/version.Version=v0.1.0".
var Version = DevVersion

// DevVersion is the version reported by development builds without metadata.
const DevVersion = "dev"

// revisionLen is the number of VCS revision characters shown in dev versions.
const revisionLen = 12

// String returns the effective build version: the link-time Version when it
// was injected, otherwise the module version or VCS revision recorded by the
// Go toolchain ("dev-<revision>[-dirty]"), and "dev" when nothing is known.
func String() string {
	return Resolve(Version, debug.ReadBuildInfo)
}

// Resolve implements String with injectable inputs (used by tests).
func Resolve(injected string, read func() (*debug.BuildInfo, bool)) string {
	if injected != "" && injected != DevVersion {
		return injected
	}
	info, ok := read()
	if !ok || info == nil {
		return DevVersion
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return DevVersion
	}
	if len(revision) > revisionLen {
		revision = revision[:revisionLen]
	}
	v := DevVersion + "-" + revision
	if modified {
		v += "-dirty"
	}
	return v
}
