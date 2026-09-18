package durationx

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    Duration
		wantErr string
	}{
		{in: "", want: 0},
		{in: "0", want: 0},
		{in: "  0  ", want: 0},
		{in: "0s", want: 0},
		{in: "500ms", want: Duration(500 * time.Millisecond)},
		{in: "30s", want: Duration(30 * time.Second)},
		{in: "10m", want: Duration(10 * time.Minute)},
		{in: "24h", want: Duration(24 * time.Hour)},
		{in: "1h30m", want: Duration(90 * time.Minute)},
		{in: "1.5h", want: Duration(90 * time.Minute)},
		{in: "7d", want: Duration(7 * 24 * time.Hour)},
		{in: "0d", want: 0},
		{in: "7d12h", want: Duration(7*24*time.Hour + 12*time.Hour)},
		{in: "1d12h30m5s", want: Duration(36*time.Hour + 30*time.Minute + 5*time.Second)},
		{in: "2d500ms", want: Duration(48*time.Hour + 500*time.Millisecond)},
		{in: " 30s ", want: Duration(30 * time.Second)},
		{in: "250us", want: Duration(250 * time.Microsecond)},
		{in: "permanent", want: Permanent},
		{in: "PERMANENT", want: Permanent},
		{in: " Permanent ", want: Permanent},
		{in: "106751d", want: Duration(106751 * 24 * time.Hour)},
		{in: "-1s", wantErr: "negative duration"},
		{in: "-1", wantErr: "negative duration"},
		{in: "-permanent", wantErr: "negative duration"},
		{in: "7d-1h", wantErr: "negative duration"},
		{in: "abc", wantErr: "invalid duration"},
		{in: "10", wantErr: "invalid duration"},
		{in: "d", wantErr: "invalid duration"},
		{in: "7D", wantErr: "invalid duration"},
		{in: "1.5d", wantErr: "invalid duration"},
		{in: "1h2d", wantErr: "invalid duration"},
		{in: "7dd", wantErr: "invalid duration"},
		{in: "forever", wantErr: "invalid duration"},
		{in: "106752d", wantErr: "overflows"},
		{in: "99999999999999999999d", wantErr: "invalid duration"},
		{in: "106751d24h", wantErr: "overflows"},
		{in: "106751d23h47m16s854ms776us", wantErr: "overflows"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMustParse(t *testing.T) {
	t.Parallel()
	require.Equal(t, Duration(time.Minute), MustParse("1m"))
	require.Panics(t, func() { MustParse("nope") })
}

func TestString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   Duration
		want string
	}{
		{in: 0, want: "0s"},
		{in: Permanent, want: "permanent"},
		{in: Duration(time.Nanosecond), want: "1ns"},
		{in: Duration(250 * time.Microsecond), want: "250µs"},
		{in: Duration(500 * time.Millisecond), want: "500ms"},
		{in: Duration(1500 * time.Microsecond), want: "1.5ms"},
		{in: Duration(30 * time.Second), want: "30s"},
		{in: Duration(90 * time.Second), want: "1m30s"},
		{in: Duration(10 * time.Minute), want: "10m"},
		{in: Duration(90 * time.Minute), want: "1h30m"},
		{in: Duration(24 * time.Hour), want: "1d"},
		{in: Duration(7 * 24 * time.Hour), want: "7d"},
		{in: Duration(7*24*time.Hour + 12*time.Hour), want: "7d12h"},
		{in: Duration(36*time.Hour + 5*time.Second + 20*time.Millisecond), want: "1d12h5s20ms"},
		{in: Duration(-2 * time.Second), want: "-2s"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, tt.in.String(), int64(tt.in))
	}
}

func TestStringParseRoundTrip(t *testing.T) {
	t.Parallel()
	values := []Duration{
		0, Permanent, 1, Duration(time.Microsecond), Duration(time.Millisecond), Duration(999 * time.Millisecond),
		Duration(time.Second), Duration(59 * time.Minute), Duration(23*time.Hour + 59*time.Minute + 59*time.Second),
		Duration(365 * 24 * time.Hour), Duration(math.MaxInt64),
		Duration(3*24*time.Hour + 4*time.Hour + 5*time.Minute + 6*time.Second + 7*time.Millisecond + 8*time.Microsecond + 9),
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		values = append(values, Duration(rng.Int64N(math.MaxInt64)))
		values = append(values, Duration(rng.Int64N(int64(90*24*time.Hour))/int64(time.Millisecond)*int64(time.Millisecond)))
	}
	for _, v := range values {
		s := v.String()
		got, err := Parse(s)
		require.NoError(t, err, s)
		require.Equal(t, v, got, s)
	}
}

func TestAccessors(t *testing.T) {
	t.Parallel()
	d := Duration(1500 * time.Millisecond)
	require.Equal(t, 1500*time.Millisecond, d.Std())
	require.Equal(t, int64(1500), d.Milliseconds())
	require.False(t, d.IsPermanent())
	require.False(t, d.IsZero())

	require.Equal(t, time.Duration(-1), Permanent.Std())
	require.Equal(t, int64(-1), Permanent.Milliseconds())
	require.True(t, Permanent.IsPermanent())
	require.False(t, Permanent.IsZero())

	var zero Duration
	require.True(t, zero.IsZero())
	require.Equal(t, int64(0), zero.Milliseconds())
}

type jsonHolder struct {
	D Duration `json:"d"`
}

func TestJSON(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in   Duration
		want string
	}{
		{in: 0, want: `{"d":"0s"}`},
		{in: Permanent, want: `{"d":"permanent"}`},
		{in: Duration(7*24*time.Hour + 12*time.Hour), want: `{"d":"7d12h"}`},
		{in: Duration(250 * time.Millisecond), want: `{"d":"250ms"}`},
	} {
		b, err := json.Marshal(jsonHolder{D: tt.in})
		require.NoError(t, err)
		require.Equal(t, tt.want, string(b))
		var back jsonHolder
		require.NoError(t, json.Unmarshal(b, &back))
		require.Equal(t, tt.in, back.D)
	}

	tests := []struct {
		in      string
		want    Duration
		wantErr string
	}{
		{in: `{"d":"30s"}`, want: Duration(30 * time.Second)},
		{in: `{"d":"7d"}`, want: Duration(7 * 24 * time.Hour)},
		{in: `{"d":"permanent"}`, want: Permanent},
		{in: `{"d":""}`, want: 0},
		{in: `{"d":1500}`, want: Duration(1500 * time.Millisecond)},
		{in: `{"d":0}`, want: 0},
		{in: `{"d":-1}`, want: Permanent},
		{in: `{"d":null}`, want: 0},
		{in: `{"d":9223372036854}`, want: Duration(9223372036854 * time.Millisecond)},
		{in: `{"d":-2}`, wantErr: "negative duration"},
		{in: `{"d":9223372036855}`, wantErr: "overflows"},
		{in: `{"d":1.5}`, wantErr: "string or integer milliseconds"},
		{in: `{"d":true}`, wantErr: "string or integer milliseconds"},
		{in: `{"d":{}}`, wantErr: "string or integer milliseconds"},
		{in: `{"d":"soon"}`, wantErr: "invalid duration"},
		{in: `{"d":"-5s"}`, wantErr: "negative duration"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var h jsonHolder
			err := json.Unmarshal([]byte(tt.in), &h)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, h.D)
		})
	}

	var d Duration
	require.Error(t, d.UnmarshalJSON([]byte(`"unterminated`)))
	d = Duration(time.Hour)
	require.NoError(t, d.UnmarshalJSON([]byte("null")))
	require.Equal(t, Duration(0), d)
}

type yamlHolder struct {
	D Duration `yaml:"d"`
}

func TestYAML(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in   Duration
		want string
	}{
		{in: 0, want: "d: 0s\n"},
		{in: Permanent, want: "d: permanent\n"},
		{in: Duration(36 * time.Hour), want: "d: 1d12h\n"},
		{in: Duration(10 * time.Minute), want: "d: 10m\n"},
	} {
		b, err := yaml.Marshal(yamlHolder{D: tt.in})
		require.NoError(t, err)
		require.Equal(t, tt.want, string(b))
		var back yamlHolder
		require.NoError(t, yaml.Unmarshal(b, &back))
		require.Equal(t, tt.in, back.D)
	}

	tests := []struct {
		name    string
		in      string
		want    Duration
		wantErr string
	}{
		{name: "string", in: "d: 30s", want: Duration(30 * time.Second)},
		{name: "quoted string", in: `d: "7d12h"`, want: Duration(7*24*time.Hour + 12*time.Hour)},
		{name: "permanent", in: "d: permanent", want: Permanent},
		{name: "integer ms", in: "d: 1500", want: Duration(1500 * time.Millisecond)},
		{name: "zero int", in: "d: 0", want: 0},
		{name: "minus one", in: "d: -1", want: Permanent},
		{name: "missing field", in: "other: 1", want: 0},
		{name: "null", in: "d: null", want: 0},
		{name: "negative int", in: "d: -2", wantErr: "negative duration"},
		{name: "overflow int", in: "d: 9223372036855", wantErr: "overflows"},
		{name: "int out of range", in: "d: 99999999999999999999", wantErr: "invalid duration"},
		{name: "hex int", in: "d: 0x10", wantErr: "invalid duration"},
		{name: "float", in: "d: 1.5", wantErr: "invalid duration"},
		{name: "bad string", in: "d: soon", wantErr: "line 1"},
		{name: "quoted number without unit", in: `d: "500"`, wantErr: "invalid duration"},
		{name: "sequence", in: "d: [1, 2]", wantErr: "must be a scalar"},
		{name: "mapping", in: "d: {a: 1}", wantErr: "must be a scalar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h yamlHolder
			err := yaml.Unmarshal([]byte(tt.in), &h)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, h.D)
		})
	}
}

func TestYAMLHandBuiltNodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		node    yaml.Node
		want    Duration
		wantErr string
	}{
		{name: "untagged integer is milliseconds", node: yaml.Node{Kind: yaml.ScalarNode, Value: "1500"}, want: Duration(1500 * time.Millisecond)},
		{name: "untagged minus one is permanent", node: yaml.Node{Kind: yaml.ScalarNode, Value: "-1"}, want: Permanent},
		{name: "untagged string", node: yaml.Node{Kind: yaml.ScalarNode, Value: "7d"}, want: Duration(7 * 24 * time.Hour)},
		{name: "explicit string tag keeps unit requirement", node: yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "1500"}, wantErr: "invalid duration"},
		{name: "untagged negative integer", node: yaml.Node{Kind: yaml.ScalarNode, Value: "-5"}, wantErr: "negative duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var d Duration
			err := tt.node.Decode(&d)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, d)
		})
	}
}

// FuzzParseRoundTrip checks that every accepted input is non-negative (or
// Permanent) and survives String, JSON and YAML round trips unchanged.
func FuzzParseRoundTrip(f *testing.F) {
	for _, s := range []string{"", "0", "7d12h", "permanent", "1.5h", "106751d23h", "+1d", "250us", "1d500ms", "abc"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := Parse(s)
		if err != nil {
			return
		}
		if d < 0 && !d.IsPermanent() {
			t.Fatalf("Parse(%q) returned negative %d", s, int64(d))
		}
		back, err := Parse(d.String())
		if err != nil || back != d {
			t.Fatalf("String round trip of %q: %q -> %d, %v", s, d.String(), int64(back), err)
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("json marshal %q: %v", s, err)
		}
		var j Duration
		if err := json.Unmarshal(b, &j); err != nil || j != d {
			t.Fatalf("json round trip of %q: %s -> %d, %v", s, b, int64(j), err)
		}
		y, err := yaml.Marshal(d)
		if err != nil {
			t.Fatalf("yaml marshal %q: %v", s, err)
		}
		var yd Duration
		if err := yaml.Unmarshal(y, &yd); err != nil || yd != d {
			t.Fatalf("yaml round trip of %q: %s -> %d, %v", s, y, int64(yd), err)
		}
	})
}

func TestParseErrorDoesNotPanicOnLongInput(t *testing.T) {
	t.Parallel()
	_, err := Parse(strings.Repeat("9", 10000) + "d")
	require.Error(t, err)
}
