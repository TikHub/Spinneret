package buildinfo

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveVersion(t *testing.T) {
	info := func(main string, settings ...string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			bi := &debug.BuildInfo{Main: debug.Module{Path: "github.com/TikHub/Spinneret", Version: main}}
			for i := 0; i+1 < len(settings); i += 2 {
				bi.Settings = append(bi.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
			}
			return bi, true
		}
	}
	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }
	tests := []struct {
		name     string
		injected string
		read     func() (*debug.BuildInfo, bool)
		want     string
	}{
		{name: "ldflags win", injected: "v0.1.0", read: info("v9.9.9"), want: "v0.1.0"},
		{name: "module version", injected: DevVersion, read: info("v0.1.1"), want: "v0.1.1"},
		{name: "pseudo version with vcs stamp", injected: DevVersion,
			read: info("v0.0.0-20260917091500-b674631abcde+dirty"), want: "v0.0.0-20260917091500-b674631abcde+dirty"},
		{name: "devel with vcs revision", injected: DevVersion,
			read: info("(devel)", "vcs.revision", "b674631abcdef0123456789", "vcs.modified", "true"), want: "dev-b674631abcde-dirty"},
		{name: "devel with clean short revision", injected: DevVersion,
			read: info("(devel)", "vcs.revision", "b674631", "vcs.modified", "false"), want: "dev-b674631"},
		{name: "devel without vcs", injected: DevVersion, read: info("(devel)"), want: DevVersion},
		{name: "no build info", injected: DevVersion, read: noInfo, want: DevVersion},
		{name: "empty injected value", injected: "", read: info("v0.2.0"), want: "v0.2.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolve(tc.injected, tc.read))
		})
	}
	require.NotEmpty(t, Version())
}
