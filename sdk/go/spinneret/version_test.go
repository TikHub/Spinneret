package spinneret

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/updatecheck"
)

// repoRoot is the repository root seen from this package's directory, which is
// the working directory go test runs the package in.
const repoRoot = "../../.."

// pythonVersionAssignment reads the version out of the Python SDK's _version.py.
var pythonVersionAssignment = regexp.MustCompile(`(?m)^__version__\s*=\s*["']([^"']+)["']`)

// versionedFile is one of the places a release writes its version down.
type versionedFile struct {
	path    string // from the repository root
	field   string // what to change in it
	version string
}

// TestVersionMatchesTheRelease keeps Version in step with the two other files a
// release bumps. It went unbumped from 0.1.1 to 0.1.5, so every Go node of those
// releases sent User-Agent spinneret-go/0.1.0 while the Python SDK and the
// console carried the real version.
func TestVersionMatchesTheRelease(t *testing.T) {
	files := []versionedFile{
		{path: "sdk/go/spinneret/types.go", field: "const Version", version: Version},
		{path: "sdk/python/src/spinneret/_version.py", field: "__version__", version: pythonSDKVersion(t)},
		{path: "web/package.json", field: `"version"`, version: consoleVersion(t)},
	}
	if msg := lockstepFailure(files); msg != "" {
		t.Fatal(msg)
	}
}

func pythonSDKVersion(t *testing.T) string {
	t.Helper()
	const path = "sdk/python/src/spinneret/_version.py"
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
	require.NoError(t, err)
	m := pythonVersionAssignment.FindSubmatch(raw)
	require.NotNil(t, m, "%s assigns no __version__", path)
	return string(m[1])
}

func consoleVersion(t *testing.T) string {
	t.Helper()
	const path = "web/package.json"
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
	require.NoError(t, err)
	var pkg struct {
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(raw, &pkg), "parse %s", path)
	require.NotEmpty(t, pkg.Version, "%s has no version", path)
	return pkg.Version
}

// lockstepFailure returns "" when every file carries the same version, and
// otherwise a message for whoever cut the release: what each file says, and
// which ones to bump. A release that forgets a file leaves it behind the others,
// so each file that differs from the newest version found is named.
func lockstepFailure(files []versionedFile) string {
	newest := files[0].version
	for _, f := range files[1:] {
		if updatecheck.Compare(f.version, newest) > 0 {
			newest = f.version
		}
	}
	var table, fixes strings.Builder
	for _, f := range files {
		fmt.Fprintf(&table, "\n  %-38s %-14s %s", f.path, f.field, f.version)
		if f.version != newest {
			fmt.Fprintf(&fixes, "\n  bump %s in %s from %s to %s", f.field, f.path, f.version, newest)
		}
	}
	if fixes.Len() == 0 {
		return ""
	}
	return "a release bumps the Go SDK, the Python SDK and the console to the same version, " +
		"but they disagree:" + table.String() + "\nto fix it:" + fixes.String()
}
