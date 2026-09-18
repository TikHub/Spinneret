package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type secretValuer struct{ v string }

func (s secretValuer) LogValue() slog.Value { return slog.StringValue(s.v) }

func TestParseLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{in: "", want: slog.LevelInfo},
		{in: "debug", want: slog.LevelDebug},
		{in: "DEBUG", want: slog.LevelDebug},
		{in: " info ", want: slog.LevelInfo},
		{in: "warn", want: slog.LevelWarn},
		{in: "Warning", want: slog.LevelWarn},
		{in: "error", want: slog.LevelError},
		{in: "trace", wantErr: true},
		{in: "info+2", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseLevel(tt.in)
		if tt.wantErr {
			require.Error(t, err, tt.in)
			require.Contains(t, err.Error(), "unknown log level")
			continue
		}
		require.NoError(t, err, tt.in)
		require.Equal(t, tt.want, got, tt.in)
	}
}

func TestNewLoggerErrors(t *testing.T) {
	t.Parallel()
	_, err := NewLogger("verbose", "json", &bytes.Buffer{})
	require.ErrorContains(t, err, "unknown log level")
	_, err = NewLogger("info", "xml", &bytes.Buffer{})
	require.ErrorContains(t, err, `unknown log format "xml"`)
}

func TestNewLoggerNilWriterDefaultsToStderr(t *testing.T) {
	t.Parallel()
	l, err := NewLogger("error", "text", nil)
	require.NoError(t, err)
	require.NotNil(t, l)
	require.False(t, l.Enabled(t.Context(), slog.LevelInfo))
}

func decodeJSONLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	require.NotEmpty(t, line)
	require.NotContains(t, line, "\n", "expected exactly one log line")
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &m))
	return m
}

func TestNewLoggerJSONRedaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		log   func(l *slog.Logger)
		check func(t *testing.T, m map[string]any)
	}{
		{
			name: "every sensitive key",
			log: func(l *slog.Logger) {
				l.Info("msg",
					"password", "p@ss", "token", "spn_abc", "secret", "s3cr3t",
					"authorization", "Bearer spn_abc", "cookie", "a=b", "payload", map[string]string{"sid": "x"},
					"url_credentials", "user:pw")
			},
			check: func(t *testing.T, m map[string]any) {
				for _, k := range []string{"password", "token", "secret", "authorization", "cookie", "payload", "url_credentials"} {
					require.Equal(t, RedactedValue, m[k], k)
				}
			},
		},
		{
			name: "case insensitive keys",
			log:  func(l *slog.Logger) { l.Info("msg", "Password", "p", "AUTHORIZATION", "Bearer x") },
			check: func(t *testing.T, m map[string]any) {
				require.Equal(t, RedactedValue, m["Password"])
				require.Equal(t, RedactedValue, m["AUTHORIZATION"])
			},
		},
		{
			name: "similar keys are kept",
			log:  func(l *slog.Logger) { l.Info("msg", "token_id", "tok_1", "secret_path", "db/pw", "site", "shop") },
			check: func(t *testing.T, m map[string]any) {
				require.Equal(t, "tok_1", m["token_id"])
				require.Equal(t, "db/pw", m["secret_path"])
				require.Equal(t, "shop", m["site"])
				require.Equal(t, "msg", m["msg"])
				require.Equal(t, "INFO", m["level"])
				require.NotContains(t, m, "source")
			},
		},
		{
			name: "attribute inside sensitive group",
			log: func(l *slog.Logger) {
				l.Info("msg", slog.Group("payload", slog.String("sid", "abc"), slog.Int("n", 1)))
			},
			check: func(t *testing.T, m map[string]any) {
				g, ok := m["payload"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, RedactedValue, g["sid"])
				require.Equal(t, RedactedValue, g["n"])
			},
		},
		{
			name: "sensitive key inside normal group",
			log: func(l *slog.Logger) {
				l.Info("msg", slog.Group("req", slog.String("cookie", "a=b"), slog.String("path", "/x")))
			},
			check: func(t *testing.T, m map[string]any) {
				g, ok := m["req"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, RedactedValue, g["cookie"])
				require.Equal(t, "/x", g["path"])
			},
		},
		{
			name: "WithGroup sensitive",
			log:  func(l *slog.Logger) { l.WithGroup("secret").Info("msg", "value", "plain") },
			check: func(t *testing.T, m map[string]any) {
				g, ok := m["secret"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, RedactedValue, g["value"])
			},
		},
		{
			name: "With attrs redacted",
			log:  func(l *slog.Logger) { l.With("token", "spn_x").Info("msg") },
			check: func(t *testing.T, m map[string]any) {
				require.Equal(t, RedactedValue, m["token"])
			},
		},
		{
			name: "LogValuer resolved then redacted",
			log:  func(l *slog.Logger) { l.Info("msg", "password", secretValuer{v: "hunter2"}) },
			check: func(t *testing.T, m map[string]any) {
				require.Equal(t, RedactedValue, m["password"])
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			l, err := NewLogger("debug", "json", &buf)
			require.NoError(t, err)
			tt.log(l)
			require.NotContains(t, buf.String(), "spn_")
			require.NotContains(t, buf.String(), "hunter2")
			tt.check(t, decodeJSONLine(t, &buf))
		})
	}
}

func TestNewLoggerLevelFiltering(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l, err := NewLogger("warn", "", &buf)
	require.NoError(t, err)
	l.Info("dropped")
	require.Empty(t, buf.String())
	l.Warn("kept")
	m := decodeJSONLine(t, &buf)
	require.Equal(t, "kept", m["msg"])
}

func TestNewLoggerText(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l, err := NewLogger("info", "TEXT", &buf)
	require.NoError(t, err)
	l.Info("hello", "password", "hunter2", "site", "shop")
	out := buf.String()
	require.Contains(t, out, "msg=hello")
	require.Contains(t, out, "password="+RedactedValue)
	require.Contains(t, out, "site=shop")
	require.NotContains(t, out, "hunter2")
}

func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()
	require.True(t, IsSensitiveKey("Cookie"))
	require.False(t, IsSensitiveKey("cookies_count"))
	require.False(t, IsSensitiveKey(""))
}
