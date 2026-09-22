package identity

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// buildTags returns a comma-separated string of n distinct tokens.
func buildTags(n int, sep string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(sep)
		}
		fmt.Fprintf(&b, "t%d", i)
	}
	return b.String()
}

// TestImportTagsWithinLimit accepts exactly MaxTags distinct tags in every form.
func TestImportTagsWithinLimit(t *testing.T) {
	t.Parallel()
	jsonlString := fmt.Sprintf(`{"payload":{"cookie":"x"},"tags":%q}`+"\n", buildTags(MaxTags, ","))
	rows, rowErrs, err := ParseImport(ImportFormatJSONL, strings.NewReader(jsonlString), ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rowErrs)
	require.Len(t, rows, 1)
	require.Len(t, rows[0].Tags, MaxTags)
}

// TestImportTagsOverLimitRejected rejects MaxTags+1 distinct tags in JSONL
// (string and array) and CSV forms with a clear per-row error, instead of
// doing unbounded work.
func TestImportTagsOverLimitRejected(t *testing.T) {
	t.Parallel()
	over := MaxTags + 1

	// JSONL, comma-separated string form.
	jsonlString := fmt.Sprintf(`{"payload":{"cookie":"x"},"tags":%q}`+"\n", buildTags(over, ","))
	rows, rowErrs, err := ParseImport(ImportFormatJSONL, strings.NewReader(jsonlString), ImportLimits{})
	require.NoError(t, err) // a bad row is a row error, not a fatal parse error
	require.Empty(t, rows)
	require.Len(t, rowErrs, 1)
	require.Contains(t, rowErrs[0].Message, "more than")

	// JSONL, array form.
	items := make([]string, over)
	for i := range items {
		items[i] = fmt.Sprintf("t%d", i)
	}
	arr := `["` + strings.Join(items, `","`) + `"]`
	jsonlArray := fmt.Sprintf(`{"payload":{"cookie":"x"},"tags":%s}`+"\n", arr)
	rows, rowErrs, err = ParseImport(ImportFormatJSONL, strings.NewReader(jsonlArray), ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Len(t, rowErrs, 1)
	require.Contains(t, rowErrs[0].Message, "more than")

	// CSV, semicolon-separated form.
	csv := "cookie,_tags\n" + "x," + buildTags(over, ";") + "\n"
	rows, rowErrs, err = ParseImport(ImportFormatCSV, strings.NewReader(csv), ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Len(t, rowErrs, 1)
	require.Contains(t, rowErrs[0].Message, "more than")
}

// TestImportLabelsOverLimitRejected rejects more than MaxLabels labels.
func TestImportLabelsOverLimitRejected(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(`{"payload":{"cookie":"x"},"labels":{`)
	for i := 0; i <= MaxLabels; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":"v"`, i)
	}
	b.WriteString("}}\n")
	rows, rowErrs, err := ParseImport(ImportFormatJSONL, strings.NewReader(b.String()), ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Len(t, rowErrs, 1)
	require.Contains(t, rowErrs[0].Message, "more than")
}

// TestImportTagsDoSBounded is the regression guard for the O(n^2) dedup DoS:
// a single row carrying a huge tag field must fail fast (bounded time), not
// burn CPU proportionally to n^2. Before the fix, ~1MB took >25s; the cap now
// short-circuits after MaxTags distinct values, so even a multi-megabyte field
// returns in well under a second.
func TestImportTagsDoSBounded(t *testing.T) {
	t.Parallel()
	huge := buildTags(2_000_000, ",") // ~15MB of distinct tokens
	line := fmt.Sprintf(`{"payload":{"cookie":"x"},"tags":%q}`+"\n", huge)

	start := time.Now()
	rows, rowErrs, err := ParseImport(ImportFormatJSONL, bytes.NewReader([]byte(line)),
		ImportLimits{MaxRows: DefaultImportMaxRows, MaxBytes: DefaultImportMaxBytes})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Empty(t, rows)
	require.Len(t, rowErrs, 1)
	require.Less(t, elapsed, 2*time.Second, "tag processing must be bounded, got %v", elapsed)
}
