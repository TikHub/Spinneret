package spinneret

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotComponent(t *testing.T) {
	// Expectations match Python's urllib.parse.quote(name, safe="") plus the
	// leading-dot rule of the Python SDK, so both SDKs share snapshot files.
	tests := map[string]string{
		"search.json":      "search.json",
		"a/b":              "a%2Fb",
		".hidden":          "%2Ehidden",
		"..":               "%2E.",
		"":                 "%00",
		"spinneret:8080":   "spinneret%3A8080",
		"键":                "%E9%94%AE",
		"sp ace~_-":        "sp%20ace~_-",
		"_token_namespace": "_token_namespace",
		"../../etc/passwd": "%2E.%2F..%2Fetc%2Fpasswd",
	}
	for in, want := range tests {
		require.Equal(t, want, snapshotComponent(in), "input %q", in)
	}
}

func TestSnapshotRoundTripAndLayout(t *testing.T) {
	root := t.TempDir()
	store := newSnapshotStore(filepath.Join(root, "cache"), "spinneret.internal:443", "", nil, discardLogger())
	item := testConfigItem("crawler", "search/web.json", 12, `{"k":"v"}`)
	written, err := store.save(item)
	require.NoError(t, err)
	require.True(t, written)

	path := filepath.Join(root, "cache", "spinneret.internal%3A443", "_token_namespace", "crawler", "search%2Fweb.json.json")
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		dirInfo, err := os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"format":1`)
	require.Contains(t, string(raw), `"has_secret_refs":false`)

	loaded, err := store.load("crawler", "search/web.json")
	require.NoError(t, err)
	require.Equal(t, int32(12), loaded.GetVersion())
	require.Equal(t, `{"k":"v"}`, loaded.GetContent())

	missing, err := store.load("crawler", "missing.json")
	require.NoError(t, err)
	require.Nil(t, missing)

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary files left behind")
}

func TestSnapshotLoadRejectsBadFiles(t *testing.T) {
	root := t.TempDir()
	store := newSnapshotStore(root, "host", "ns", nil, discardLogger())
	write := func(key, content string) {
		p := filepath.Join(root, store.relPath("g", key))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}

	write("corrupt", "{not json")
	_, err := store.load("g", "corrupt")
	require.Error(t, err)

	write("format", `{"format":2,"item":{}}`)
	_, err = store.load("g", "format")
	require.ErrorContains(t, err, "format")

	write("encrypted", `{"format":1,"encrypted":true,"envelope":{"v":1}}`)
	item, err := store.load("g", "encrypted")
	require.NoError(t, err)
	require.Nil(t, item, "encrypted Python snapshots are skipped")

	write("mismatch", `{"format":1,"item":{"group":"g","key":"other"}}`)
	_, err = store.load("g", "mismatch")
	require.ErrorContains(t, err, "another item")

	write("baditem", `{"format":1,"item":{"version":"x"}}`)
	_, err = store.load("g", "baditem")
	require.Error(t, err)

	write("secret", `{"format":1,"item":{"group":"g","key":"secret","content":"x","has_secret_refs":true}}`)
	item, err = store.load("g", "secret")
	require.NoError(t, err)
	require.Nil(t, item, "plain-text secret snapshots are never used")

	write("big", `{"format":1,"item":{"content":"`+strings.Repeat("a", maxSnapshotBytes)+`"}}`)
	_, err = store.load("g", "big")
	require.ErrorContains(t, err, "too large")

	// A snapshot written by the Python SDK (microsecond timestamps, extra fields) loads.
	write("python.json", `{"format": 1, "saved_at": "2026-09-16T08:30:11.962000Z", "encrypted": false,
		"item": {"namespace": "default", "group": "g", "key": "python.json", "format": "json", "version": 3,
		"content": "{}", "updated_at": "2026-09-16T08:30:11.962000Z", "has_secret_refs": false}}`)
	item, err = store.load("g", "python.json")
	require.NoError(t, err)
	require.Equal(t, int32(3), item.GetVersion())
	require.Equal(t, int64(1789547411), item.GetUpdatedAt().GetSeconds())
}

func TestSnapshotRootErrors(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	store := newSnapshotStore(filepath.Join(file, "sub"), "host", "", nil, discardLogger())
	_, err := store.save(testConfigItem("g", "k", 1, "{}"))
	require.Error(t, err)
	_, err = store.load("g", "k")
	require.Error(t, err)

	// A directory where the snapshot file should be makes the write fail cleanly.
	ok := newSnapshotStore(root, "host", "", nil, discardLogger())
	require.NoError(t, os.MkdirAll(filepath.Join(root, ok.relPath("g", "k")), 0o700))
	_, err = ok.save(testConfigItem("g", "k", 1, "{}"))
	require.Error(t, err)
	_, err = ok.load("g", "k")
	require.Error(t, err)
}
