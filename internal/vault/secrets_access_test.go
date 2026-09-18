package vault

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccessTracker(t *testing.T) {
	t.Parallel()
	t0 := time.Unix(1_700_000_000, 0)
	tr := newAccessTracker(2)

	tr.touch("", t0)
	require.Zero(t, tr.len(), "empty IDs are ignored")
	tr.touch("a", t0)
	tr.touch("a", t0.Add(-time.Second))
	require.Equal(t, map[string]time.Time{"a": t0}, tr.drain(), "an older access never replaces a newer one")
	require.Nil(t, tr.drain())

	// Entries restored after a failed flush merge with newer accesses and
	// respect the buffer limit.
	tr.touch("a", t0.Add(time.Minute))
	tr.touch("b", t0)
	tr.restore(map[string]time.Time{"a": t0, "b": t0.Add(time.Hour), "c": t0})
	require.Equal(t, 2, tr.len())
	require.EqualValues(t, 1, tr.dropped.Load(), "c does not fit")
	got := tr.drain()
	require.Equal(t, t0.Add(time.Minute), got["a"])
	require.Equal(t, t0.Add(time.Hour), got["b"])

	tr.touch("x", t0)
	tr.touch("y", t0)
	tr.touch("z", t0)
	require.Equal(t, 2, tr.len())
	require.EqualValues(t, 2, tr.dropped.Load())
}
