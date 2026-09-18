package textdiff

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnifiedIdentical(t *testing.T) {
	require.Empty(t, Unified("a", "b", "x\ny\n", "x\ny\n", 3))
}

func TestUnifiedChange(t *testing.T) {
	from := "a\nb\nc\nd\ne\nf\ng\n"
	to := "a\nb\nc\nD\ne\nf\ng\n"
	got := Unified("v1", "v2", from, to, 1)
	want := "--- v1\n+++ v2\n@@ -3,3 +3,3 @@\n c\n-d\n+D\n e\n"
	require.Equal(t, want, got)
}

func TestUnifiedInsertAndDelete(t *testing.T) {
	got := Unified("old", "new", "", "x\ny\n", 3)
	require.Equal(t, "--- old\n+++ new\n@@ -0,0 +1,2 @@\n+x\n+y\n", got)

	got = Unified("old", "new", "x\ny\n", "", 3)
	require.Equal(t, "--- old\n+++ new\n@@ -1,2 +0,0 @@\n-x\n-y\n", got)
}

func TestUnifiedSeparateHunks(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = string(rune('a' + i%26))
	}
	from := strings.Join(lines, "\n") + "\n"
	changed := append([]string(nil), lines...)
	changed[1] = "X"
	changed[25] = "Y"
	to := strings.Join(changed, "\n") + "\n"
	got := Unified("a", "b", from, to, 2)
	require.Equal(t, 2, strings.Count(got, "@@ -"))
	require.Contains(t, got, "-b\n+X\n")
	require.Contains(t, got, "-z\n+Y\n")
}

func TestUnifiedMergesCloseHunks(t *testing.T) {
	from := "1\n2\n3\n4\n5\n6\n"
	to := "1\nX\n3\n4\nY\n6\n"
	got := Unified("a", "b", from, to, 1)
	require.Equal(t, 1, strings.Count(got, "@@ -"))
}

func TestUnifiedNegativeContextAndNoTrailingNewline(t *testing.T) {
	got := Unified("a", "b", "x\ny", "x\nz", -5)
	require.Equal(t, "--- a\n+++ b\n@@ -2,1 +2,1 @@\n-y\n+z\n", got)
}

func TestLCSFallbackForHugeInputs(t *testing.T) {
	a := make([]string, 6000)
	b := make([]string, 6000)
	for i := range a {
		a[i] = "a" + strings.Repeat("x", i%7)
		b[i] = "b" + strings.Repeat("y", i%5)
	}
	ops := lcsOps(a, b)
	require.Len(t, ops, 12000)
}
