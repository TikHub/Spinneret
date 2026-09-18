package proxy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
)

func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }

func TestParseLines(t *testing.T) {
	data := strings.Join([]string{
		"# comment",
		"",
		"http://u:p@1.1.1.1:80 kind=residential region=us city=nyc provider=acme tags=a,b max_concurrency=5",
		"  socks5://2.2.2.2:1080\r",
		"http://3.3.3.3:80 bogus",
		"http://4.4.4.4:80 color=red",
		"http://5.5.5.5:80 max_concurrency=many",
		"http://6.6.6.6:80 session_template=user-{username} url=x",
	}, "\n")
	failures := &failureList{}
	rows, err := parseImport(FormatLines, data, failures)
	require.NoError(t, err)
	require.Equal(t, []importRow{
		{
			Line: 3, URL: "http://u:p@1.1.1.1:80", Kind: strp("residential"), Region: strp("us"), City: strp("nyc"),
			Provider: strp("acme"), Tags: []string{"a", "b"}, TagsSet: true, MaxConcurrency: intp(5),
		},
		{Line: 4, URL: "socks5://2.2.2.2:1080"},
	}, rows)
	require.Equal(t, []ImportFailure{
		{Line: 5, Message: "field 2 must be key=value"},
		{Line: 6, Message: `unknown attribute "color"`},
		{Line: 7, Message: "max_concurrency must be an integer"},
		{Line: 8, Message: "url must be the first field"},
	}, failures.result())
}

func TestParseJSONL(t *testing.T) {
	data := strings.Join([]string{
		`{"url":"http://1.1.1.1:80","kind":"mobile","tags":["x"],"max_concurrency":3,"session_template":"s-{random}","region":"de","city":"berlin","provider":"p"}`,
		``,
		`{"kind":"mobile"}`,
		`{"url":"http://2.2.2.2:80","extra":1}`,
		`{"url":"http://3.3.3.3:80","max_concurrency":"x"}`,
		`{"url":"http://4.4.4.4:80"} {"url":"x"}`,
		`{"url":"http://secret:pw@5.5.5.5:80"`,
		`not json`,
		`{"url":"http://6.6.6.6:80","tags":[]}`,
	}, "\n")
	failures := &failureList{}
	rows, err := parseImport(FormatJSONL, data, failures)
	require.NoError(t, err)
	require.Equal(t, []importRow{
		{
			Line: 1, URL: "http://1.1.1.1:80", Kind: strp("mobile"), Region: strp("de"), City: strp("berlin"),
			Provider: strp("p"), Tags: []string{"x"}, TagsSet: true, MaxConcurrency: intp(3), SessionTemplate: strp("s-{random}"),
		},
		{Line: 9, URL: "http://6.6.6.6:80", Tags: []string{}, TagsSet: true},
	}, rows)
	got := failures.result()
	require.Len(t, got, 6)
	require.Equal(t, ImportFailure{Line: 3, Message: "url is required"}, got[0])
	require.Equal(t, 4, got[1].Line)
	require.Contains(t, got[1].Message, `unknown field "extra"`)
	require.Equal(t, ImportFailure{Line: 5, Message: `invalid JSON object: field "max_concurrency" has the wrong type`}, got[2])
	require.Equal(t, ImportFailure{Line: 6, Message: "invalid JSON object: unexpected data after the object"}, got[3])
	require.Equal(t, 7, got[4].Line)
	require.Equal(t, 8, got[5].Line)
	for _, f := range got {
		require.NotContains(t, f.Message, "pw", "failures never echo credentials")
	}
}

func TestParseCSV(t *testing.T) {
	data := "\uFEFFURL, kind ,tags,max_concurrency,region,city,provider,session_template\n" +
		"http://1.1.1.1:80,tunnel,a;b,10,us,,acme,{username}-x\n" +
		"\n" +
		"http://2.2.2.2:80,,,,,,,\n" +
		"http://3.3.3.3:80,tunnel\n" +
		",tunnel,,,,,,\n" +
		"http://4.4.4.4:80,,,NaN,,,,\n" +
		"\"http://5.5.5.5:80,,,,,,,\n"
	failures := &failureList{}
	rows, err := parseImport(FormatCSV, data, failures)
	require.NoError(t, err)
	require.Equal(t, []importRow{
		{
			Line: 2, URL: "http://1.1.1.1:80", Kind: strp("tunnel"), Tags: []string{"a", "b"}, TagsSet: true,
			MaxConcurrency: intp(10), Region: strp("us"), Provider: strp("acme"), SessionTemplate: strp("{username}-x"),
		},
		{Line: 3, URL: "http://2.2.2.2:80"},
	}, rows)
	got := failures.result()
	require.Equal(t, []ImportFailure{
		{Line: 4, Message: "expected 8 fields"},
		{Line: 5, Message: "url is required"},
		{Line: 6, Message: "max_concurrency must be an integer"},
		{Line: 7, Message: "malformed csv record"},
	}, got)
}

func TestParseCSVHeaderErrors(t *testing.T) {
	tests := []struct {
		name, data, errPart string
	}{
		{name: "empty", data: "", errPart: "empty"},
		{name: "no url column", data: "kind,region\n", errPart: "url column"},
		{name: "unknown column", data: "url,colour\n", errPart: `unknown column "colour"`},
		{name: "duplicate column", data: "url,kind,KIND\n", errPart: "duplicate column"},
		{name: "bad header", data: "\"url\n", errPart: "header is invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseImport(FormatCSV, tt.data, &failureList{})
			require.ErrorContains(t, err, tt.errPart)
			require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
		})
	}
}

func TestParseImportLimits(t *testing.T) {
	_, err := parseImport("xml", "x", &failureList{})
	require.ErrorContains(t, err, "format must be")

	_, err = parseImport(FormatLines, strings.Repeat("a", MaxImportBytes+1), &failureList{})
	require.ErrorContains(t, err, "exceeds")

	var b strings.Builder
	for i := 0; i <= MaxImportRows; i++ {
		fmt.Fprintf(&b, "http://10.0.%d.%d:80\n", i/250, i%250)
	}
	for _, format := range []string{FormatLines, FormatJSONL, FormatCSV} {
		data := b.String()
		switch format {
		case FormatJSONL:
			data = strings.ReplaceAll(strings.TrimSpace(data), "\n", "\"}\n{\"url\":\"")
			data = `{"url":"` + data + `"}`
		case FormatCSV:
			data = "url\n" + data
		}
		_, err := parseImport(format, data, &failureList{})
		require.ErrorContains(t, err, "exceeds 100000 rows", format)
	}
}

func TestFailureListCap(t *testing.T) {
	f := &failureList{}
	require.Equal(t, []ImportFailure{}, f.result())
	for i := 0; i < maxImportFailures+5; i++ {
		f.add(i+1, "row %d", i)
	}
	require.Equal(t, maxImportFailures+5, f.count())
	res := f.result()
	require.Len(t, res, maxImportFailures+1)
	require.Equal(t, ImportFailure{Line: 0, Message: "5 more rows failed"}, res[len(res)-1])
}

func TestAttributesValidate(t *testing.T) {
	base := Attributes{Kind: KindDatacenter, MaxConcurrency: 1}
	tests := []struct {
		name    string
		mut     func(a *Attributes)
		errPart string
		tags    []string
	}{
		{name: "ok", mut: func(*Attributes) {}},
		{name: "kind", mut: func(a *Attributes) { a.Kind = "satellite" }, errPart: "kind"},
		{name: "region", mut: func(a *Attributes) { a.Region = strings.Repeat("r", MaxRegionLength+1) }, errPart: "region"},
		{name: "city control", mut: func(a *Attributes) { a.City = "a\tb" }, errPart: "city"},
		{name: "provider", mut: func(a *Attributes) { a.Provider = strings.Repeat("p", MaxProviderLength+1) }, errPart: "provider"},
		{name: "concurrency low", mut: func(a *Attributes) { a.MaxConcurrency = 0 }, errPart: "max_concurrency"},
		{name: "concurrency high", mut: func(a *Attributes) { a.MaxConcurrency = MaxMaxConcurrency + 1 }, errPart: "max_concurrency"},
		{name: "empty tag", mut: func(a *Attributes) { a.Tags = []string{" "} }, errPart: "empty"},
		{name: "tag comma", mut: func(a *Attributes) { a.Tags = []string{"a,b"} }, errPart: "commas"},
		{name: "tag long", mut: func(a *Attributes) { a.Tags = []string{strings.Repeat("t", MaxTagLength+1)} }, errPart: "at most"},
		{name: "too many tags", mut: func(a *Attributes) {
			for i := 0; i <= MaxTags; i++ {
				a.Tags = append(a.Tags, fmt.Sprintf("t%d", i))
			}
		}, errPart: "at most 64 tags"},
		{name: "tags normalized", mut: func(a *Attributes) { a.Tags = []string{" x ", "y", "x"} }, tags: []string{"x", "y"}},
		{name: "template", mut: func(a *Attributes) { a.SessionTemplate = "{nope}" }, errPart: "unknown placeholder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := base
			tt.mut(&a)
			err := a.validate()
			if tt.errPart != "" {
				require.ErrorContains(t, err, tt.errPart)
				return
			}
			require.NoError(t, err)
			if tt.tags != nil {
				require.Equal(t, tt.tags, a.Tags)
			}
		})
	}
	require.True(t, ValidState(StateDead))
	require.False(t, ValidState("zombie"))
	require.Equal(t, []string{"a", "b", "c"}, mergeTags([]string{"a", "b"}, []string{"b", "c"}))
	require.Equal(t, []string{"a", "b"}, splitTags(" a ;; b,"))
}
