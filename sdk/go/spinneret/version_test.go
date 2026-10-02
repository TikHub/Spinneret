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

// TestLockstepFailureNamesTheFix pins the advice the release test gives, on
// made-up versions rather than on the repository's files.
func TestLockstepFailureNamesTheFix(t *testing.T) {
	goConst := func(v string) versionedFile {
		return versionedFile{path: "sdk/go/spinneret/types.go", field: "const Version", version: v}
	}
	python := func(v string) versionedFile {
		return versionedFile{path: "sdk/python/src/spinneret/_version.py", field: "__version__", version: v}
	}
	console := func(v string) versionedFile {
		return versionedFile{path: "web/package.json", field: `"version"`, version: v}
	}
	cases := []struct {
		name  string
		files []versionedFile
		fixes []string // the lines under "to fix it:"; none when the files agree
	}{
		{
			name:  "in step",
			files: []versionedFile{goConst("0.1.6"), python("0.1.6"), console("0.1.6")},
		},
		{
			name:  "Go const forgotten",
			files: []versionedFile{goConst("0.1.5"), python("0.1.6"), console("0.1.6")},
			fixes: []string{"bump const Version in sdk/go/spinneret/types.go from 0.1.5 to 0.1.6"},
		},
		{
			name:  "console forgotten",
			files: []versionedFile{goConst("0.1.6"), python("0.1.6"), console("0.1.5")},
			fixes: []string{`bump "version" in web/package.json from 0.1.5 to 0.1.6`},
		},
		{
			name:  "only the console bumped",
			files: []versionedFile{goConst("0.1.5"), python("0.1.5"), console("0.1.6")},
			fixes: []string{
				"bump const Version in sdk/go/spinneret/types.go from 0.1.5 to 0.1.6",
				"bump __version__ in sdk/python/src/spinneret/_version.py from 0.1.5 to 0.1.6",
			},
		},
		{
			name:  "Go const written as the tag",
			files: []versionedFile{goConst("v0.1.6"), python("0.1.6"), console("0.1.6")},
			fixes: []string{"write const Version in sdk/go/spinneret/types.go as 0.1.6, without the tag's v"},
		},
		{
			name:  "Python written with a trailing .0",
			files: []versionedFile{goConst("0.1.6"), python("0.1.6.0"), console("0.1.6")},
			fixes: []string{"write __version__ in sdk/python/src/spinneret/_version.py as 0.1.6"},
		},
		{
			name:  "Go const written as the tag and Python forgotten",
			files: []versionedFile{goConst("v0.1.6"), python("0.1.5"), console("0.1.6")},
			fixes: []string{
				"write const Version in sdk/go/spinneret/types.go as 0.1.6, without the tag's v",
				"bump __version__ in sdk/python/src/spinneret/_version.py from 0.1.5 to 0.1.6",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := lockstepFailure(tc.files)
			if tc.fixes == nil {
				require.Empty(t, msg)
				return
			}
			_, block, found := strings.Cut(msg, "\nto fix it:\n")
			require.True(t, found, "no fixes in:\n%s", msg)
			lines := strings.Split(block, "\n")
			for i := range lines {
				lines[i] = strings.TrimSpace(lines[i])
			}
			require.Equal(t, tc.fixes, lines, "message:\n%s", msg)
		})
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
// so each file that differs from the newest version found is named. A file that
// names the newest release in another spelling, usually the tag's "v", is told
// to respell it rather than bump it, and the bare spelling wins a tie because
// files carry the version without the "v".
func lockstepFailure(files []versionedFile) string {
	newest := files[0].version
	for _, f := range files[1:] {
		c := updatecheck.Compare(f.version, newest)
		if c > 0 || (c == 0 && tagSpelled(newest) && !tagSpelled(f.version)) {
			newest = f.version
		}
	}
	var table, fixes strings.Builder
	for _, f := range files {
		fmt.Fprintf(&table, "\n  %-38s %-14s %s", f.path, f.field, f.version)
		switch {
		case f.version == newest:
		case updatecheck.Compare(f.version, newest) != 0:
			fmt.Fprintf(&fixes, "\n  bump %s in %s from %s to %s", f.field, f.path, f.version, newest)
		case tagSpelled(f.version):
			fmt.Fprintf(&fixes, "\n  write %s in %s as %s, without the tag's v", f.field, f.path, newest)
		default:
			fmt.Fprintf(&fixes, "\n  write %s in %s as %s", f.field, f.path, newest)
		}
	}
	if fixes.Len() == 0 {
		return ""
	}
	return "a release bumps the Go SDK, the Python SDK and the console to the same version, " +
		"but they disagree:" + table.String() + "\nto fix it:" + fixes.String()
}

// tagSpelled reports whether a version is written the way the git tag writes
// it, with a leading "v" that the files a release bumps leave out.
func tagSpelled(version string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(version)), "v")
}
