package apiutil

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
)

// requireAppErr asserts the code and reason of an apperr error and returns
// its client-facing message.
func requireAppErr(t *testing.T, err error, code connect.Code, reason apperr.Reason) string {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.As(err)
	require.True(t, ok, "expected an apperr error, got %T: %v", err, err)
	require.Equal(t, code, ae.Code, ae.Error())
	require.Equal(t, reason, ae.Reason, ae.Error())
	return ae.Message
}

func TestPageSize(t *testing.T) {
	tests := []struct {
		in   int32
		want int
	}{
		{math.MinInt32, DefaultPageSize},
		{-1, DefaultPageSize},
		{0, DefaultPageSize},
		{1, 1},
		{DefaultPageSize + 1, DefaultPageSize + 1},
		{MaxPageSize, MaxPageSize},
		{MaxPageSize + 1, MaxPageSize},
		{math.MaxInt32, MaxPageSize},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, PageSize(tc.in), "PageSize(%d)", tc.in)
	}
}

type testCursor struct {
	CreatedAt int64  `json:"t"`
	ID        string `json:"i"`
}

func TestCursorRoundTrip(t *testing.T) {
	in := testCursor{CreatedAt: 1_758_000_000_123_456, ID: "tok_01J"}
	token, err := EncodeCursor(in)
	require.NoError(t, err)
	require.NotContains(t, token, "=", "unpadded")
	require.NotContains(t, token, "+")
	require.NotContains(t, token, "/")

	var out testCursor
	ok, err := DecodeCursor(token, &out)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, in, out)
}

func TestEncodeCursorError(t *testing.T) {
	_, err := EncodeCursor(make(chan int))
	require.ErrorContains(t, err, "encode cursor")
}

func TestDecodeCursorEmpty(t *testing.T) {
	out := testCursor{ID: "keep"}
	ok, err := DecodeCursor("", &out)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, testCursor{ID: "keep"}, out)
}

func TestDecodeCursorInvalid(t *testing.T) {
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	// A syntactically valid cursor of exactly MaxCursorLength bytes.
	pad := strings.Repeat("a", (MaxCursorLength*3/4)-len(`{"t":1,"i":""}`))
	atLimit := encode(`{"t":1,"i":"` + pad + `"}`)
	require.Len(t, atLimit, MaxCursorLength)

	var out testCursor
	ok, err := DecodeCursor(atLimit, &out)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, pad, out.ID)

	tests := map[string]string{
		"one byte over the limit": atLimit + "A",
		"far over the limit":      strings.Repeat("A", 1<<20),
		"not base64":              "!!!",
		"padded base64":           base64.URLEncoding.EncodeToString([]byte(`{"t":1}`)),
		"standard alphabet":       base64.RawStdEncoding.EncodeToString([]byte(`{"i":"??>"}`)),
		"not json":                encode("not json"),
		"trailing data":           encode(`{"t":1} x`),
		"wrong field type":        encode(`{"t":"x"}`),
		"wrong shape":             encode(`[1,2]`),
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			var c testCursor
			ok, err := DecodeCursor(token, &c)
			require.False(t, ok)
			msg := requireAppErr(t, err, connect.CodeInvalidArgument, apperr.ReasonInvalidArgument)
			require.Equal(t, "invalid page_token", msg)
		})
	}
}

func FuzzDecodeCursor(f *testing.F) {
	valid, err := EncodeCursor(testCursor{CreatedAt: 1, ID: "x"})
	require.NoError(f, err)
	for _, seed := range []string{"", "!", valid, strings.Repeat("A", MaxCursorLength+1)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		var c testCursor
		ok, err := DecodeCursor(token, &c)
		if err != nil {
			require.False(t, ok)
			require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
			return
		}
		require.Equal(t, token != "", ok)
		require.LessOrEqual(t, len(token), MaxCursorLength)
	})
}

func TestPrincipal(t *testing.T) {
	_, err := Principal(context.Background())
	requireAppErr(t, err, connect.CodeUnauthenticated, apperr.ReasonSessionInvalid)

	p := &authz.Principal{Kind: authz.KindUser, ID: "usr_1"}
	got, err := Principal(authz.WithPrincipal(context.Background(), p))
	require.NoError(t, err)
	require.Same(t, p, got)
}

func TestNamespace(t *testing.T) {
	prod := catalogtest.NewNamespace("ten_a", "ns_prod", "prod")
	staging := catalogtest.NewNamespace("ten_a", "ns_staging", "staging")
	otherTenant := catalogtest.NewNamespace("ten_b", "ns_b_prod", "prod")
	cat := catalogtest.New(prod, staging, otherTenant)
	as := func(p *authz.Principal) context.Context { return authz.WithPrincipal(context.Background(), p) }

	user := &authz.Principal{Kind: authz.KindUser, ID: "usr_1", TenantID: "ten_a"}
	token := &authz.Principal{Kind: authz.KindToken, ID: "tok_1", TenantID: "ten_a", NamespaceID: "ns_staging", NamespaceName: "staging"}
	orphan := &authz.Principal{Kind: authz.KindToken, ID: "tok_2", TenantID: "ten_a", NamespaceID: "ns_gone", NamespaceName: "gone"}

	t.Run("user resolves by name in the active tenant", func(t *testing.T) {
		p, ns, err := Namespace(as(user), cat, "prod")
		require.NoError(t, err)
		require.Same(t, user, p)
		require.Same(t, prod, ns)
	})
	t.Run("user of another tenant", func(t *testing.T) {
		other := &authz.Principal{Kind: authz.KindUser, ID: "usr_2", TenantID: "ten_b"}
		_, ns, err := Namespace(as(other), cat, "prod")
		require.NoError(t, err)
		require.Same(t, otherTenant, ns)
	})
	t.Run("user unknown namespace", func(t *testing.T) {
		_, _, err := Namespace(as(user), cat, "missing")
		requireAppErr(t, err, connect.CodeNotFound, apperr.ReasonNotFound)
	})
	t.Run("user without namespace name", func(t *testing.T) {
		_, _, err := Namespace(as(user), cat, "")
		requireAppErr(t, err, connect.CodeInvalidArgument, apperr.ReasonInvalidArgument)
	})
	t.Run("user without active tenant", func(t *testing.T) {
		admin := &authz.Principal{Kind: authz.KindUser, ID: "usr_root", IsPlatformAdmin: true}
		_, _, err := Namespace(as(admin), cat, "prod")
		msg := requireAppErr(t, err, connect.CodeInvalidArgument, apperr.ReasonInvalidArgument)
		require.Contains(t, msg, "active tenant")
	})
	t.Run("token resolves to its own namespace", func(t *testing.T) {
		for _, name := range []string{"", "staging"} {
			p, ns, err := Namespace(as(token), cat, name)
			require.NoError(t, err)
			require.Same(t, token, p)
			require.Same(t, staging, ns)
		}
	})
	t.Run("token for another namespace", func(t *testing.T) {
		_, _, err := Namespace(as(token), cat, "prod")
		requireAppErr(t, err, connect.CodePermissionDenied, apperr.ReasonScopeMissing)
	})
	t.Run("token namespace missing from the catalog", func(t *testing.T) {
		_, _, err := Namespace(as(orphan), cat, "")
		requireAppErr(t, err, connect.CodeNotFound, apperr.ReasonNotFound)
	})
	t.Run("unauthenticated", func(t *testing.T) {
		_, _, err := Namespace(context.Background(), cat, "prod")
		requireAppErr(t, err, connect.CodeUnauthenticated, apperr.ReasonSessionInvalid)
	})
}

func TestSiteAndResources(t *testing.T) {
	ns := catalogtest.NewNamespace("ten_a", "ns_prod", "prod")
	shop := catalogtest.AddSite(ns, "sit_shop", "shop", 7)

	got, err := Site(ns, "shop")
	require.NoError(t, err)
	require.Same(t, shop, got)
	_, err = Site(ns, "")
	requireAppErr(t, err, connect.CodeInvalidArgument, apperr.ReasonSiteUnknown)
	_, err = Site(ns, "market")
	msg := requireAppErr(t, err, connect.CodeInvalidArgument, apperr.ReasonSiteUnknown)
	require.Contains(t, msg, `"market"`)

	require.Equal(t, authz.Resource{TenantID: "ten_a", NamespaceID: "ns_prod", NamespaceName: "prod"}, NamespaceResource(ns))
	require.Equal(t, authz.Resource{TenantID: "ten_a", NamespaceID: "ns_prod", NamespaceName: "prod", SiteID: "sit_shop", SiteName: "shop"},
		SiteResource(ns, shop))
}

func TestTimestamps(t *testing.T) {
	at := time.Date(2031, 5, 1, 12, 30, 45, 123_456_789, time.FixedZone("UTC+8", 8*3600))

	require.Nil(t, Timestamp(time.Time{}))
	require.True(t, Timestamp(at).AsTime().Equal(at))

	require.Nil(t, TimestampPtr(nil))
	zero := time.Time{}
	require.Nil(t, TimestampPtr(&zero))
	require.True(t, TimestampPtr(&at).AsTime().Equal(at))

	require.Nil(t, MillisTimestamp(0))
	require.Nil(t, MillisTimestamp(-1))
	require.Equal(t, int64(1_758_000_000_123), MillisTimestamp(1_758_000_000_123).AsTime().UnixMilli())

	require.Nil(t, Time(nil))
	back := Time(timestamppb.New(at))
	require.NotNil(t, back)
	require.True(t, back.Equal(at))
	require.Equal(t, time.UTC, back.Location())
}

type label string

func TestStruct(t *testing.T) {
	s, err := Struct(nil)
	require.NoError(t, err)
	require.Nil(t, s)

	at := time.Date(2031, 5, 1, 12, 30, 45, 500_000_000, time.FixedZone("UTC+8", 8*3600))
	var nilTime *time.Time
	var nilMap map[string]int64
	var nilSlice []string
	n := int64(9)
	in := map[string]any{
		"nil":          nil,
		"bool":         true,
		"string":       "s",
		"int":          1,
		"int32":        int32(2),
		"int64":        int64(3),
		"uint16":       uint16(4),
		"float32":      float32(0.5),
		"float64":      1.25,
		"json_number":  json.Number("12"),
		"bytes":        []byte("hi"),
		"time":         at,
		"zero_time":    time.Time{},
		"time_ptr":     &at,
		"nil_time_ptr": nilTime,
		"raw":          json.RawMessage(`{"a":[1,"b"]}`),
		"strings_map":  map[string]string{"k": "v"},
		"int_map":      map[string]int{"a": 1},
		"int64_map":    map[string]int64{"b": 2},
		"float_map":    map[string]float64{"c": 0.25},
		"bool_map":     map[string]bool{"d": true},
		"nil_map":      nilMap,
		"strings":      []string{"x", "y"},
		"nil_slice":    nilSlice,
		"ints":         []int{1, 2},
		"int64s":       []int64{3, 4},
		"floats":       []float64{0.5},
		"maps":         []map[string]any{{"id": "a", "at": at, "n": []int64{1}}, nil},
		"any_slice":    []any{map[string]int64{"e": 5}, at},
		"nested":       map[string]any{"inner": map[string][]string{"tags": {"t1"}}},
		"named":        []label{"l1"},
		"array":        [2]int32{7, 8},
		"int_ptr":      &n,
	}
	s, err = Struct(in)
	require.NoError(t, err)
	raw, err := s.MarshalJSON()
	require.NoError(t, err)
	require.JSONEq(t, `{
		"nil": null, "bool": true, "string": "s",
		"int": 1, "int32": 2, "int64": 3, "uint16": 4, "float32": 0.5, "float64": 1.25, "json_number": 12,
		"bytes": "aGk=",
		"time": "2031-05-01T04:30:45.5Z", "zero_time": null, "time_ptr": "2031-05-01T04:30:45.5Z", "nil_time_ptr": null,
		"raw": {"a": [1, "b"]},
		"strings_map": {"k": "v"}, "int_map": {"a": 1}, "int64_map": {"b": 2}, "float_map": {"c": 0.25},
		"bool_map": {"d": true}, "nil_map": null,
		"strings": ["x", "y"], "nil_slice": null, "ints": [1, 2], "int64s": [3, 4], "floats": [0.5],
		"maps": [{"id": "a", "at": "2031-05-01T04:30:45.5Z", "n": [1]}, null],
		"any_slice": [{"e": 5}, "2031-05-01T04:30:45.5Z"],
		"nested": {"inner": {"tags": ["t1"]}},
		"named": ["l1"], "array": [7, 8], "int_ptr": 9
	}`, string(raw))

	// The input is not modified.
	require.Equal(t, map[string]int64{"b": 2}, in["int64_map"])
	require.Equal(t, at, in["time"])
}

func TestStructUnsupported(t *testing.T) {
	tests := map[string]any{
		"struct":          struct{ A int }{A: 1},
		"channel":         make(chan int),
		"non-string keys": map[int]string{1: "a"},
		"invalid raw":     json.RawMessage(`{`),
		"invalid utf8":    "\xff",
		"nested struct":   []any{struct{}{}},
	}
	for name, v := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Struct(map[string]any{"v": v})
			requireAppErr(t, err, connect.CodeInternal, apperr.ReasonInternal)
		})
	}
}

func TestDuration(t *testing.T) {
	d, err := Duration("ttl", "1d12h", false)
	require.NoError(t, err)
	require.Equal(t, 36*time.Hour, d.Std())

	d, err = Duration("ttl", "", false)
	require.NoError(t, err)
	require.True(t, d.IsZero())

	d, err = Duration("ban", "permanent", true)
	require.NoError(t, err)
	require.Equal(t, durationx.Permanent, d)

	_, err = Duration("ttl", "permanent", false)
	msg := requireAppErr(t, err, connect.CodeInvalidArgument, apperr.ReasonInvalidArgument)
	require.Equal(t, "ttl: permanent is not allowed", msg)

	_, err = Duration("ttl", "soon", true)
	msg = requireAppErr(t, err, connect.CodeInvalidArgument, apperr.ReasonInvalidArgument)
	require.True(t, strings.HasPrefix(msg, "ttl: "), msg)
}
