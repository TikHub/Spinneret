package identity

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
)

func parseImportString(t *testing.T, format, data string, limits ImportLimits) ([]ImportRow, []RowError, error) {
	t.Helper()
	return ParseImport(format, strings.NewReader(data), limits)
}

func TestParseImportJSONL(t *testing.T) {
	data := "\xef\xbb\xbf" + `{"cookies": "sessionid=a; csrf_token=b", "user_agent": "UA", "_account": " acc-1 ", "_region": "US", "_tags": "a, b,,a", "_labels": {"team": "x"}}` + "\r\n" +
		"\n" +
		"   \t\n" +
		`{"payload": {"device_id": "d1", "install_id": "i1", "extra": {"aid": 1001}}, "account": "acc-2", "region": "SG", "tags": ["x", " y ", "x", ""], "labels": {"k": "v"}}` + "\n" +
		`{"payload": {"device_id": "d2"}, "tags": null, "labels": {}}` + "\n" +
		`{"device_id": "d3", "_tags": ["t"], "_labels": null}`
	rows, rowErrs, err := parseImportString(t, "JSONL", data, ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rowErrs)
	require.Equal(t, []ImportRow{
		{
			Line:    1,
			Payload: map[string]any{"cookies": "sessionid=a; csrf_token=b", "user_agent": "UA"},
			Account: "acc-1", Region: "US", Tags: []string{"a", "b"}, Labels: map[string]string{"team": "x"},
		},
		{
			Line:    4,
			Payload: map[string]any{"device_id": "d1", "install_id": "i1", "extra": map[string]any{"aid": json.Number("1001")}},
			Account: "acc-2", Region: "SG", Tags: []string{"x", "y"}, Labels: map[string]string{"k": "v"},
		},
		{Line: 5, Payload: map[string]any{"device_id": "d2"}},
		{Line: 6, Payload: map[string]any{"device_id": "d3"}, Tags: []string{"t"}},
	}, rows)
}

func TestParseImportJSONLRowErrors(t *testing.T) {
	lines := []struct {
		line string
		msg  string
	}{
		{`{"device_id": `, "invalid JSON"},
		{`[1, 2]`, "expected a JSON object"},
		{`null`, "expected a JSON object"},
		{`{"a": 1} {"b": 2}`, "unexpected data after the object"},
		{`{"payload": {"a": 1}, "acount": "x"}`, `unknown envelope key "acount"`},
		{`{"payload": "a=1"}`, "payload must be a JSON object"},
		{`{"payload": {}}`, "payload is empty"},
		{`{"_account": "x"}`, "payload is empty"},
		{`{"payload": {"a": 1}, "account": 5}`, "account must be a string"},
		{`{"a": 1, "_region": true}`, "_region must be a string"},
		{`{"payload": {"a": 1}, "tags": 5}`, "tags must be an array of strings or a comma-separated string"},
		{`{"payload": {"a": 1}, "tags": [1]}`, "tags must be an array of strings"},
		{`{"a": 1, "_labels": "k=v"}`, "_labels must be an object of strings"},
		{`{"payload": {"a": 1}, "labels": {"k": 1}}`, "labels.k must be a string"},
		{`{"payload": {"a": 1}, "labels": {" ": "v"}}`, "labels has an empty key"},
	}
	var b strings.Builder
	b.WriteString(`{"ok": true}` + "\n")
	for _, l := range lines {
		b.WriteString(l.line + "\n")
	}
	rows, rowErrs, err := parseImportString(t, "jsonl", b.String(), ImportLimits{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].Line)
	require.Len(t, rowErrs, len(lines))
	for i, l := range lines {
		require.Equal(t, i+2, rowErrs[i].Line, l.line)
		require.Contains(t, rowErrs[i].Message, l.msg, l.line)
	}
}

func TestParseImportCSV(t *testing.T) {
	data := "\xef\xbb\xbf device_id ,install_id,cookies,extra,_account,_region,_tags,_labels\r\n" +
		`d1,i1,"sessionid=a; csrf_token=1%7Cabc","{""aid"": 1001}",acc-1,US,a; b ;a,team=x; env = prod` + "\r\n" +
		"\r\n" +
		",,,,,,,\n" +
		"   \n" +
		`d2,i2,,"[1, 2]",,,,` + "\n" +
		`d3,,"[{""name"":""sid"",""value"":""v""}]",{not json},,,,` + "\n" +
		`"d4` + "\n" + `multi",  ,,  ,,,,` + "\n"
	rows, rowErrs, err := parseImportString(t, "csv", data, ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rowErrs)
	require.Equal(t, []ImportRow{
		{
			Line: 2,
			Payload: map[string]any{
				"device_id": "d1", "install_id": "i1", "cookies": "sessionid=a; csrf_token=1%7Cabc",
				"extra": map[string]any{"aid": json.Number("1001")},
			},
			Account: "acc-1", Region: "US", Tags: []string{"a", "b"}, Labels: map[string]string{"team": "x", "env": "prod"},
		},
		{Line: 6, Payload: map[string]any{"device_id": "d2", "install_id": "i2", "extra": []any{json.Number("1"), json.Number("2")}}},
		{
			Line:    7,
			Payload: map[string]any{"device_id": "d3", "cookies": []any{map[string]any{"name": "sid", "value": "v"}}, "extra": "{not json}"},
		},
		{Line: 8, Payload: map[string]any{"device_id": "d4\nmulti"}},
	}, rows)
}

func TestParseImportCSVRowErrors(t *testing.T) {
	data := "a,b,_labels\n" +
		"1,2,k=v\n" +
		"1,2\n" +
		"1,2,3,4\n" +
		"x\"y,2,\n" +
		"1,2,novalue\n" +
		"1,2,=v\n" +
		",,k=v\n" +
		"\"1\"x,2,\n" +
		"5,6,;;\n"
	rows, rowErrs, err := parseImportString(t, "csv", data, ImportLimits{})
	require.NoError(t, err)
	require.Equal(t, []ImportRow{
		{Line: 2, Payload: map[string]any{"a": "1", "b": "2"}, Labels: map[string]string{"k": "v"}},
		{Line: 10, Payload: map[string]any{"a": "5", "b": "6"}},
	}, rows)
	require.Equal(t, []RowError{
		{Line: 3, Message: "wrong number of fields"},
		{Line: 4, Message: "wrong number of fields"},
		{Line: 5, Message: `invalid csv at line 5, column 2: bare " in non-quoted-field`},
		{Line: 6, Message: "_labels must be k=v pairs separated by ';'"},
		{Line: 7, Message: "_labels must be k=v pairs separated by ';'"},
		{Line: 8, Message: "payload is empty"},
		{Line: 9, Message: `invalid csv at line 9, column 3: extraneous or missing " in quoted-field`},
	}, rowErrs)
}

func TestParseImportCSVHeaderErrors(t *testing.T) {
	tests := []struct {
		name string
		data string
		err  string
	}{
		{"empty input", "", "csv header row is missing"},
		{"blank input", "\n\n", "csv header row is missing"},
		{"bad header quote", "a\"b,c\n", "invalid csv header"},
		{"empty column", "a,,b\n", "csv header column 2 is empty"},
		{"duplicate column", "a, a\n", `csv header column "a" is duplicated`},
		{"unknown reserved column", "a,_note\n", `csv header column "_note" is not a reserved column`},
		{"duplicate reserved column", "a,_tags,_tags\n", `csv header column "_tags" is duplicated`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, rowErrs, err := parseImportString(t, "csv", tt.data, ImportLimits{})
			require.Nil(t, rows)
			require.Nil(t, rowErrs)
			requireInvalid(t, err, tt.err)
		})
	}
}

func TestParseImportLimits(t *testing.T) {
	jsonl := strings.Repeat(`{"a": "1"}`+"\n", 3)
	csvData := "a\n" + strings.Repeat("1\n", 3)
	csvWithErrors := "a,b\n1,2\n1\n1\n"
	tests := []struct {
		name   string
		format string
		data   string
		limits ImportLimits
		err    string
	}{
		{"jsonl rows", "jsonl", jsonl, ImportLimits{MaxRows: 2}, "exceeds the limit of 2 rows"},
		{"csv rows", "csv", csvData, ImportLimits{MaxRows: 2}, "exceeds the limit of 2 rows"},
		{"csv rows counting errors", "csv", csvWithErrors, ImportLimits{MaxRows: 2}, "exceeds the limit of 2 rows"},
		{"jsonl bytes", "jsonl", jsonl, ImportLimits{MaxBytes: 20}, "exceeds the limit of 20 bytes"},
		{"csv bytes", "csv", csvData, ImportLimits{MaxBytes: 5}, "exceeds the limit of 5 bytes"},
		{"csv header bytes", "csv", "abcdefghij\n1\n", ImportLimits{MaxBytes: 4}, "exceeds the limit of 4 bytes"},
		{"csv quoted field bytes", "csv", "a\n\"" + strings.Repeat("x", 100) + "\"\n", ImportLimits{MaxBytes: 50}, "exceeds the limit of 50 bytes"},
		{"unsupported format", "xlsx", jsonl, ImportLimits{}, `unsupported format "xlsx"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, rowErrs, err := parseImportString(t, tt.format, tt.data, tt.limits)
			require.Nil(t, rows)
			require.Nil(t, rowErrs)
			requireInvalid(t, err, tt.err)
		})
	}

	// Exactly at the limits is fine.
	rows, _, err := parseImportString(t, "jsonl", jsonl, ImportLimits{MaxRows: 3, MaxBytes: int64(len(jsonl))})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	rows, _, err = parseImportString(t, "csv", csvData, ImportLimits{MaxRows: 3, MaxBytes: int64(len(csvData))})
	require.NoError(t, err)
	require.Len(t, rows, 3)
}

func TestParseImportReaderErrors(t *testing.T) {
	_, _, err := ParseImport("jsonl", nil, ImportLimits{})
	requireInvalid(t, err, "reader is nil")

	boom := errors.New("disk on fire")
	for _, format := range []string{"jsonl", "csv"} {
		_, _, err := ParseImport(format, iotest.ErrReader(boom), ImportLimits{})
		require.ErrorIs(t, err, boom, format)

		r := io.MultiReader(strings.NewReader("a\n1\n"), iotest.ErrReader(boom))
		_, _, err = ParseImport(format, r, ImportLimits{})
		require.ErrorIs(t, err, boom, format)
	}
	// I/O failures are not reported as invalid arguments.
	_, _, err = ParseImport("csv", iotest.ErrReader(boom), ImportLimits{})
	_, isAppErr := apperr.As(err)
	require.False(t, isAppErr)
	require.ErrorContains(t, err, "import: read: disk on fire")

	// A failure after the first bytes surfaces from the format parser.
	_, _, err = ParseImport("csv", io.MultiReader(strings.NewReader("abcd"), iotest.ErrReader(boom)), ImportLimits{})
	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "import: read csv header: disk on fire")
}

func TestParseImportByteOrderMark(t *testing.T) {
	// Spreadsheet exports prepend a BOM, possibly before a quoted header.
	rows, rowErrs, err := parseImportString(t, "csv", "\xef\xbb\xbf\"device_id\",\"_tags\"\n\"d1\",a;b\n", ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rowErrs)
	require.Equal(t, []ImportRow{{Line: 2, Payload: map[string]any{"device_id": "d1"}, Tags: []string{"a", "b"}}}, rows)

	// Only a leading BOM is removed.
	rows, _, err = parseImportString(t, "csv", "a\n\xef\xbb\xbfx\n", ImportLimits{})
	require.NoError(t, err)
	require.Equal(t, "\xef\xbb\xbfx", rows[0].Payload["a"])

	// Inputs shorter than a BOM.
	rows, rowErrs, err = parseImportString(t, "jsonl", "{}", ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Equal(t, []RowError{{Line: 1, Message: "payload is empty"}}, rowErrs)
	rows, rowErrs, err = parseImportString(t, "jsonl", "\xef\xbb\xbf", ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Empty(t, rowErrs)
	_, _, err = parseImportString(t, "csv", "\xef\xbb", ImportLimits{MaxBytes: 1})
	requireInvalid(t, err, "exceeds the limit of 1 bytes")
}

func TestLimitedReaderHugeLimit(t *testing.T) {
	lr := &limitedReader{r: strings.NewReader("abc"), max: math.MaxInt64}
	data, err := io.ReadAll(lr)
	require.NoError(t, err)
	require.Equal(t, "abc", string(data))
}

func TestLimitedReaderSticky(t *testing.T) {
	lr := &limitedReader{r: strings.NewReader("0123456789"), max: 4}
	buf := make([]byte, 3)
	n, err := lr.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	_, err = lr.Read(buf)
	require.ErrorIs(t, err, errImportTooLarge)
	_, err = lr.Read(buf)
	require.ErrorIs(t, err, errImportTooLarge)
}

func TestImportThenNormalize(t *testing.T) {
	web := compileTestType(t, "web_cookie.yaml")
	data := "cookies,user_agent,signature,_tags\n" +
		"\"sessionid=a1b2c3; csrf_token=1%7Cabc\",Mozilla/5.0,x9y8z7,web;hk\n" +
		"\"[{\"\"name\"\":\"\"sessionid\"\",\"\"value\"\":\"\"zz\"\"}]\",,,\n" +
		"broken,,,\n"
	rows, rowErrs, err := parseImportString(t, "csv", data, ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rowErrs)
	require.Len(t, rows, 3)

	first, err := web.Normalize(rows[0].Payload)
	require.NoError(t, err)
	key, err := web.UniqueKey(first)
	require.NoError(t, err)
	require.Equal(t, `["a1b2c3"]`, string(key))
	require.Equal(t, []string{"web", "hk"}, rows[0].Tags)

	second, err := web.Normalize(rows[1].Payload)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"sessionid": "zz"}, second["cookies"])

	_, err = web.Normalize(rows[2].Payload)
	requireInvalid(t, err, "cookies: cookie pair 1 has no '='")

	app := compileTestType(t, "app_device.yaml")
	jsonRows, rowErrs, err := parseImportString(t, "jsonl", `{"device_id": "d", "install_id": "i", "extra": {"aid": 1001, "big": 1e3}}`, ImportLimits{})
	require.NoError(t, err)
	require.Empty(t, rowErrs)
	normalized, err := app.Normalize(jsonRows[0].Payload)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"aid": 1001.0, "big": 1000.0}, normalized["extra"])
}
