package appconfig

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/updatecheck"
)

func env(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := kv[k]
		return v, ok
	}
}

func required() map[string]string {
	return map[string]string{
		"SPINNERET_DATABASE_URL": "postgres://spinneret:dbpass@db:5432/spinneret?sslmode=disable",
		"SPINNERET_REDIS_URL":    "redis://:redispass@redis:6379/0",
		"SPINNERET_KEKS":         "k1:c2VjcmV0LWtlay1tYXRlcmlhbC1zaG91bGQtbm90LWxlYWs=",
	}
}

func with(base map[string]string, kv ...string) map[string]string {
	out := make(map[string]string, len(base)+len(kv)/2)
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestLoadFromDefaults(t *testing.T) {
	t.Parallel()
	c, err := LoadFrom(env(required()))
	require.NoError(t, err)

	host, _ := os.Hostname()
	if host == "" {
		host = "spinneret"
	}
	require.Regexp(t, "^"+regexp.QuoteMeta(host)+"-[0-9a-f]{6}$", c.InstanceID)

	want := Config{
		HTTPAddr:             ":8080",
		Role:                 RoleAll,
		InstanceID:           c.InstanceID,
		DatabaseURL:          required()["SPINNERET_DATABASE_URL"],
		DatabaseMaxConns:     32,
		RedisURL:             required()["SPINNERET_REDIS_URL"],
		RedisPrefix:          "sp",
		ClickHouseTTLDays:    90,
		KEKs:                 required()["SPINNERET_KEKS"],
		ReportShards:         16,
		ReportDedupTTL:       time.Hour,
		LateReportWindow:     10 * time.Minute,
		StreamMaxLen:         1_000_000,
		AcquireFleetInflight: 64,
		PayloadCache:         true,
		PayloadCacheSize:     200_000,
		DEKCacheSize:         100_000,
		DEKCacheTTL:          10 * time.Minute,
		TokenCacheTTL:        30 * time.Second,
		SessionTTL:           12 * time.Hour,
		CookieSecure:         "auto",
		LogLevel:             "info",
		LogFormat:            "json",
		ProxyCheckURL:        "http://example.com/",
		ProxyCheckInterval:   60 * time.Second,
		ProxyCheckTimeout:    10 * time.Second,
		Retention:            Retention{RiskEvents: 720 * time.Hour, MinuteStats: 720 * time.Hour, HourStats: 4320 * time.Hour, StateEvents: 8760 * time.Hour, Audit: 8760 * time.Hour},
		RecordCooldownEvents: true,
		MaxWatchers:          20_000,
		UIEnabled:            true,
		UpdateCheckURL:       updatecheck.DefaultURL,
		ShutdownTimeout:      30 * time.Second,
		AdminMaxRequestBytes: 64 << 20,
	}
	// This fixture supplies only the three required variables, so those are the
	// only ones recorded — which is the distinction the settings page depends on.
	require.True(t, c.EnvSet("SPINNERET_DATABASE_URL"))
	require.False(t, c.EnvSet("SPINNERET_RETENTION_AUDIT"), "defaulted, not supplied")
	c.envSet = nil

	require.Equal(t, want, c)
	require.NoError(t, c.Validate())

	c2, err := LoadFrom(env(required()))
	require.NoError(t, err)
	require.NotEqual(t, c.InstanceID, c2.InstanceID, "default instance ids carry random suffixes")
}

func TestLoadFromOverrides(t *testing.T) {
	t.Parallel()
	vars := with(required(),
		"SPINNERET_HTTP_ADDR", "127.0.0.1:9000",
		"SPINNERET_ROLE", " Worker ",
		"SPINNERET_INSTANCE_ID", "node-a",
		"SPINNERET_DATABASE_MAX_CONNS", "64",
		"SPINNERET_ACQUIRE_FLEET_INFLIGHT", "128",
		"SPINNERET_ACQUIRE_MAX_INFLIGHT", "48",
		"SPINNERET_REDIS_URL", "",
		"SPINNERET_REDIS_ADDRS", "r1:6379, r2:6379,,",
		"SPINNERET_REDIS_PREFIX", "spx",
		"SPINNERET_CLICKHOUSE_URL", "clickhouse://u:chpass@ch:9000/default",
		"SPINNERET_CLICKHOUSE_TTL_DAYS", "30",
		"SPINNERET_KEK_FILE", "/etc/spinneret/kek",
		"SPINNERET_KEKS", "",
		"SPINNERET_KEK_CURRENT", "k2",
		"SPINNERET_REPORT_SHARDS", "255",
		"SPINNERET_REPORT_DEDUP_TTL", "2h",
		"SPINNERET_LATE_REPORT_WINDOW", "0",
		"SPINNERET_STREAM_MAXLEN", "5000",
		"SPINNERET_PAYLOAD_CACHE", "false",
		"SPINNERET_PAYLOAD_CACHE_SIZE", "0",
		"SPINNERET_DEK_CACHE_SIZE", "10",
		"SPINNERET_DEK_CACHE_TTL", "1m",
		"SPINNERET_TOKEN_CACHE_TTL", "5s",
		"SPINNERET_SESSION_TTL", "1d",
		"SPINNERET_COOKIE_SECURE", "TRUE",
		"SPINNERET_TLS_CERT_FILE", "/tls/cert.pem",
		"SPINNERET_TLS_KEY_FILE", "/tls/key.pem",
		"SPINNERET_TRUSTED_PROXIES", "10.0.0.0/8, 192.0.2.1",
		"SPINNERET_METRICS_ADDR", ":9090",
		"SPINNERET_LOG_LEVEL", "DEBUG",
		"SPINNERET_LOG_FORMAT", "Text",
		"SPINNERET_PROXY_CHECK_URL", "https://example.com/204",
		"SPINNERET_PROXY_CHECK_INTERVAL", "5m",
		"SPINNERET_PROXY_CHECK_TIMEOUT", "500ms",
		"SPINNERET_PROXY_EXIT_IP_URL", "https://ip.example.com",
		"SPINNERET_GEOIP_DB", "/geo.mmdb",
		"SPINNERET_RETENTION_RISK_EVENTS", "7d",
		"SPINNERET_RETENTION_MINUTE_STATS", "14d",
		"SPINNERET_RETENTION_HOUR_STATS", "90d",
		"SPINNERET_RETENTION_STATE_EVENTS", "365d",
		"SPINNERET_RETENTION_AUDIT", "24h",
		"SPINNERET_RECORD_COOLDOWN_EVENTS", "0",
		"SPINNERET_MAX_WATCHERS", "1",
		"SPINNERET_UI_ENABLED", "f",
		"SPINNERET_UPDATE_CHECK_URL", "https://releases.example.invalid/latest",
		"SPINNERET_ALLOWED_ORIGINS", "http://localhost:5173",
		"SPINNERET_SHUTDOWN_TIMEOUT", "5s",
		"SPINNERET_ADMIN_MAX_REQUEST_BYTES", "1048576",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "otel:4317",
		"SPINNERET_PPROF_ADDR", "127.0.0.1:6060",
	)
	c, err := LoadFrom(env(vars))
	require.NoError(t, err)
	want := Config{
		HTTPAddr: "127.0.0.1:9000", Role: RoleWorker, InstanceID: "node-a",
		DatabaseURL: vars["SPINNERET_DATABASE_URL"], DatabaseMaxConns: 64,
		RedisAddrs: []string{"r1:6379", "r2:6379"}, RedisPrefix: "spx",
		ClickHouseURL: "clickhouse://u:chpass@ch:9000/default", ClickHouseTTLDays: 30,
		KEKFile: "/etc/spinneret/kek", KEKCurrent: "k2",
		ReportShards: 255, ReportDedupTTL: 2 * time.Hour, LateReportWindow: 0, StreamMaxLen: 5000,
		AcquireFleetInflight: 128, AcquireMaxInflight: 48,
		PayloadCache: false, PayloadCacheSize: 0,
		DEKCacheSize: 10, DEKCacheTTL: time.Minute,
		TokenCacheTTL: 5 * time.Second, SessionTTL: 24 * time.Hour, CookieSecure: "true",
		TLSCertFile: "/tls/cert.pem", TLSKeyFile: "/tls/key.pem",
		TrustedProxies: []string{"10.0.0.0/8", "192.0.2.1"}, MetricsAddr: ":9090", PprofAddr: "127.0.0.1:6060",
		LogLevel: "debug", LogFormat: "text",
		ProxyCheckURL: "https://example.com/204", ProxyCheckInterval: 5 * time.Minute, ProxyCheckTimeout: 500 * time.Millisecond,
		ProxyExitIPURL: "https://ip.example.com", GeoIPDB: "/geo.mmdb",
		Retention: Retention{
			RiskEvents: 7 * 24 * time.Hour, MinuteStats: 14 * 24 * time.Hour, HourStats: 90 * 24 * time.Hour,
			StateEvents: 365 * 24 * time.Hour, Audit: 24 * time.Hour,
		},
		RecordCooldownEvents: false, MaxWatchers: 1, UIEnabled: false,
		UpdateCheckURL: "https://releases.example.invalid/latest",
		AllowedOrigins: []string{"http://localhost:5173"}, ShutdownTimeout: 5 * time.Second,
		AdminMaxRequestBytes: 1 << 20, OTLPEndpoint: "otel:4317",
	}
	// envSet is bookkeeping rather than configuration, and it holds one entry per
	// override this test sets, so comparing it inside the struct would restate the
	// override map. Assert what it is actually for — telling a supplied value from
	// a defaulted one — and then leave it out of the comparison.
	require.True(t, c.EnvSet("SPINNERET_RETENTION_AUDIT"), "this test sets it")
	require.False(t, c.EnvSet("SPINNERET_REDIS_URL"), "this test does not set it")
	require.False(t, c.EnvSet("SPINNERET_NOT_A_VARIABLE"))
	c.envSet = nil

	require.Equal(t, want, c)
	require.False(t, c.Role.ServesAPI())
	require.True(t, c.Role.RunsWorkers())
}

func TestLoadFromParseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{name: "invalid int", key: "SPINNERET_REPORT_SHARDS", value: "many", wantErr: `SPINNERET_REPORT_SHARDS: invalid integer "many"`},
		{name: "int32 overflow", key: "SPINNERET_DATABASE_MAX_CONNS", value: "4294967298", wantErr: `SPINNERET_DATABASE_MAX_CONNS: invalid 32-bit integer "4294967298"`},
		{name: "int32 not a number", key: "SPINNERET_DATABASE_MAX_CONNS", value: "lots", wantErr: "invalid 32-bit integer"},
		{name: "invalid bool", key: "SPINNERET_PAYLOAD_CACHE", value: "yes", wantErr: `SPINNERET_PAYLOAD_CACHE: invalid boolean "yes"`},
		{name: "invalid duration", key: "SPINNERET_SESSION_TTL", value: "forever", wantErr: `SPINNERET_SESSION_TTL: invalid duration "forever"`},
		{name: "permanent duration rejected", key: "SPINNERET_TOKEN_CACHE_TTL", value: "permanent", wantErr: "SPINNERET_TOKEN_CACHE_TTL: invalid duration"},
		{name: "negative duration", key: "SPINNERET_DEK_CACHE_TTL", value: "-1m", wantErr: "SPINNERET_DEK_CACHE_TTL: invalid duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadFrom(env(with(required(), tt.key, tt.value)))
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}

	_, err := LoadFrom(env(with(required(), "SPINNERET_REPORT_SHARDS", "x", "SPINNERET_UI_ENABLED", "maybe")))
	require.ErrorContains(t, err, "SPINNERET_REPORT_SHARDS")
	require.ErrorContains(t, err, "SPINNERET_UI_ENABLED", "all parse errors are reported together")

	_, err = LoadFrom(nil)
	require.ErrorContains(t, err, "nil lookup")
}

func TestLoadFromBlankValuesUseDefaults(t *testing.T) {
	t.Parallel()
	c, err := LoadFrom(env(with(required(), "SPINNERET_HTTP_ADDR", "   ", "SPINNERET_REPORT_SHARDS", "")))
	require.NoError(t, err)
	require.Equal(t, ":8080", c.HTTPAddr)
	require.Equal(t, 16, c.ReportShards)
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		vars    map[string]string
		wantErr string
	}{
		{name: "missing everything", vars: map[string]string{}, wantErr: "SPINNERET_DATABASE_URL is required"},
		{name: "missing redis", vars: with(required(), "SPINNERET_REDIS_URL", ""), wantErr: "SPINNERET_REDIS_URL or SPINNERET_REDIS_ADDRS is required"},
		{name: "missing kek", vars: with(required(), "SPINNERET_KEKS", ""), wantErr: "SPINNERET_KEK_FILE or SPINNERET_KEKS is required"},
		{name: "bad role", vars: with(required(), "SPINNERET_ROLE", "scheduler"), wantErr: `SPINNERET_ROLE must be one of all, api, worker (got "scheduler")`},
		{name: "prefix with colon", vars: with(required(), "SPINNERET_REDIS_PREFIX", "sp:x"), wantErr: "SPINNERET_REDIS_PREFIX must be non-empty"},
		{name: "prefix with brace", vars: with(required(), "SPINNERET_REDIS_PREFIX", "{sp}"), wantErr: "SPINNERET_REDIS_PREFIX"},
		{name: "max conns", vars: with(required(), "SPINNERET_DATABASE_MAX_CONNS", "1"), wantErr: "SPINNERET_DATABASE_MAX_CONNS must be >= 2"},
		{name: "max conns negative wraparound", vars: with(required(), "SPINNERET_DATABASE_MAX_CONNS", "-5"), wantErr: "SPINNERET_DATABASE_MAX_CONNS must be >= 2"},
		{name: "shards zero", vars: with(required(), "SPINNERET_REPORT_SHARDS", "0"), wantErr: "SPINNERET_REPORT_SHARDS must be between 1 and 255"},
		{name: "shards too many", vars: with(required(), "SPINNERET_REPORT_SHARDS", "256"), wantErr: "SPINNERET_REPORT_SHARDS must be between 1 and 255"},
		{name: "dedup ttl", vars: with(required(), "SPINNERET_REPORT_DEDUP_TTL", "59s"), wantErr: "SPINNERET_REPORT_DEDUP_TTL must be >= 1m"},
		{name: "stream maxlen", vars: with(required(), "SPINNERET_STREAM_MAXLEN", "999"), wantErr: "SPINNERET_STREAM_MAXLEN must be >= 1000"},
		{name: "negative payload cache", vars: with(required(), "SPINNERET_PAYLOAD_CACHE_SIZE", "-1"), wantErr: "cache sizes must be positive"},
		{name: "dek cache zero", vars: with(required(), "SPINNERET_DEK_CACHE_SIZE", "0"), wantErr: "cache sizes must be positive"},
		{name: "payload cache enabled with zero size", vars: with(required(), "SPINNERET_PAYLOAD_CACHE_SIZE", "0"), wantErr: "SPINNERET_PAYLOAD_CACHE_SIZE must be >= 1"},
		{name: "cookie secure", vars: with(required(), "SPINNERET_COOKIE_SECURE", "sometimes"), wantErr: "SPINNERET_COOKIE_SECURE must be auto, true or false"},
		{name: "tls cert without key", vars: with(required(), "SPINNERET_TLS_CERT_FILE", "/c.pem"), wantErr: "must be set together"},
		{name: "tls key without cert", vars: with(required(), "SPINNERET_TLS_KEY_FILE", "/k.pem"), wantErr: "must be set together"},
		{name: "log level", vars: with(required(), "SPINNERET_LOG_LEVEL", "trace"), wantErr: "SPINNERET_LOG_LEVEL must be debug, info, warn or error"},
		{name: "log format", vars: with(required(), "SPINNERET_LOG_FORMAT", "xml"), wantErr: "SPINNERET_LOG_FORMAT must be json or text"},
		{name: "proxy check interval", vars: with(required(), "SPINNERET_PROXY_CHECK_INTERVAL", "500ms"), wantErr: "proxy check interval/timeout too small"},
		{name: "proxy check timeout", vars: with(required(), "SPINNERET_PROXY_CHECK_TIMEOUT", "50ms"), wantErr: "proxy check interval/timeout too small"},
		{name: "clickhouse ttl", vars: with(required(), "SPINNERET_CLICKHOUSE_TTL_DAYS", "0"), wantErr: "SPINNERET_CLICKHOUSE_TTL_DAYS must be between 1 and 3650"},
		{name: "max watchers", vars: with(required(), "SPINNERET_MAX_WATCHERS", "0"), wantErr: "SPINNERET_MAX_WATCHERS must be >= 1"},
		{name: "admin request bytes", vars: with(required(), "SPINNERET_ADMIN_MAX_REQUEST_BYTES", "1048575"), wantErr: "SPINNERET_ADMIN_MAX_REQUEST_BYTES must be >= 1MiB"},
		{name: "session ttl", vars: with(required(), "SPINNERET_SESSION_TTL", "0"), wantErr: "SPINNERET_SESSION_TTL must be >= 1m"},
		{name: "trusted proxies", vars: with(required(), "SPINNERET_TRUSTED_PROXIES", "10.0.0.0/8,not-an-ip"), wantErr: "SPINNERET_TRUSTED_PROXIES: netx: entry 1"},
		{name: "retention risk", vars: with(required(), "SPINNERET_RETENTION_RISK_EVENTS", "0"), wantErr: "SPINNERET_RETENTION_RISK_EVENTS must be >= 24h"},
		{name: "retention minute", vars: with(required(), "SPINNERET_RETENTION_MINUTE_STATS", "1h"), wantErr: "SPINNERET_RETENTION_MINUTE_STATS must be >= 24h"},
		{name: "retention hour", vars: with(required(), "SPINNERET_RETENTION_HOUR_STATS", "23h"), wantErr: "SPINNERET_RETENTION_HOUR_STATS must be >= 24h"},
		{name: "retention state", vars: with(required(), "SPINNERET_RETENTION_STATE_EVENTS", "0"), wantErr: "SPINNERET_RETENTION_STATE_EVENTS must be >= 24h"},
		{name: "retention audit", vars: with(required(), "SPINNERET_RETENTION_AUDIT", "0"), wantErr: "SPINNERET_RETENTION_AUDIT must be >= 24h"},
		{
			name:    "pprof on the api address",
			vars:    with(required(), "SPINNERET_HTTP_ADDR", ":8080", "SPINNERET_PPROF_ADDR", ":8080"),
			wantErr: "SPINNERET_PPROF_ADDR must not be the API address",
		},
		{
			name:    "pprof on the metrics address",
			vars:    with(required(), "SPINNERET_METRICS_ADDR", ":9090", "SPINNERET_PPROF_ADDR", ":9090"),
			wantErr: "SPINNERET_PPROF_ADDR must not be the metrics address",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := LoadFrom(env(tt.vars))
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
			require.Equal(t, Config{}, c, "no partial config on error")
		})
	}

	// Validate reports every problem at once.
	err := Config{}.Validate()
	require.Error(t, err)
	for _, want := range []string{"SPINNERET_ROLE", "SPINNERET_DATABASE_URL", "SPINNERET_REDIS_URL", "SPINNERET_KEK_FILE", "SPINNERET_LOG_LEVEL"} {
		require.Contains(t, err.Error(), want)
	}
}

func TestRole(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role    Role
		api     bool
		workers bool
	}{
		{role: RoleAll, api: true, workers: true},
		{role: RoleAPI, api: true, workers: false},
		{role: RoleWorker, api: false, workers: true},
		{role: "bogus", api: false, workers: false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.api, tt.role.ServesAPI(), tt.role)
		require.Equal(t, tt.workers, tt.role.RunsWorkers(), tt.role)
	}
}

func TestRedacted(t *testing.T) {
	t.Parallel()
	vars := with(required(),
		"SPINNERET_CLICKHOUSE_URL", "clickhouse://default:chpass@ch:9000/default?dial_timeout=1s&password=chquerypass",
		"SPINNERET_REDIS_ADDRS", "r1:6379,r2:6379",
		"SPINNERET_KEK_FILE", "/run/secrets/kek",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "https://user:otelpass@otel:4317",
	)
	c, err := LoadFrom(env(vars))
	require.NoError(t, err)
	r := c.Redacted()

	require.Equal(t, "postgres://spinneret:xxxxx@db:5432/spinneret?sslmode=disable", r["database_url"])
	require.Equal(t, "redis://:xxxxx@redis:6379/0", r["redis_url"])
	require.Equal(t, "clickhouse://default:xxxxx@ch:9000/default?dial_timeout=1s&password=xxxxx", r["clickhouse_url"])
	require.Equal(t, "<set>", r["keks"])
	require.Equal(t, "https://user:xxxxx@otel:4317", r["otlp_endpoint"])
	require.Equal(t, "r1:6379,r2:6379", r["redis_addrs"])
	require.Equal(t, "/run/secrets/kek", r["kek_file"])
	require.Equal(t, "all", r["role"])
	require.Equal(t, "16", r["report_shards"])
	require.Equal(t, "true", r["payload_cache"])
	require.Equal(t, "false", r["tls"])
	require.Equal(t, "10m0s", r["late_window"])
	require.Equal(t, "1h0m0s", r["report_dedup_ttl"])

	all := strings.Join(mapValues(r), " ")
	for _, secret := range []string{"dbpass", "redispass", "chpass", "chquerypass", "otelpass", "c2VjcmV0"} {
		require.NotContains(t, all, secret)
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "no credentials", in: "redis://redis:6379/0", want: "redis://redis:6379/0"},
		{name: "username only", in: "postgres://spinneret@db/app", want: "postgres://spinneret@db/app"},
		{name: "user and password", in: "postgres://u:p@db/app", want: "postgres://u:xxxxx@db/app"},
		{name: "query password", in: "postgres://db/app?user=u&password=secret", want: "postgres://db/app?password=xxxxx&user=u"},
		{name: "query case insensitive", in: "postgres://db/app?PassWord=secret&sslpassword=k", want: "postgres://db/app?PassWord=xxxxx&sslpassword=xxxxx"},
		{name: "query token", in: "https://h/x?token=abc&api_key=def", want: "https://h/x?api_key=xxxxx&token=xxxxx"},
		{name: "invalid query escape", in: "postgres://db/app?password=%zz", want: "<invalid url>"},
		{name: "invalid url", in: "postgres://u:p@[::1/app", want: "<invalid url>"},
		{name: "kv dsn", in: "host=db user=u password=secret dbname=app", want: "host=db user=u password=xxxxx dbname=app"},
		{name: "kv dsn quoted", in: `host=db password='se cr\'et' dbname=app`, want: "host=db password=xxxxx dbname=app"},
		{name: "kv dsn spaces around equals", in: "host=db password = secret", want: "host=db password = xxxxx"},
		{name: "kv dsn uppercase", in: "HOST=db PASSWORD=secret", want: "HOST=db PASSWORD=xxxxx"},
		{name: "host port untouched", in: "otel:4317", want: "otel:4317"},
		{name: "similar key untouched", in: "passage=1", want: "passage=1"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, redactURL(tt.in), tt.name)
	}
}

func TestLoadUsesProcessEnvironment(t *testing.T) {
	for k, v := range required() {
		t.Setenv(k, v)
	}
	t.Setenv("SPINNERET_INSTANCE_ID", "from-env")
	t.Setenv("SPINNERET_ROLE", "api")
	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, "from-env", c.InstanceID)
	require.Equal(t, RoleAPI, c.Role)
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestLoadAcquireAdmission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		vars      map[string]string
		wantFleet int
		wantMax   int
		wantErr   string
	}{
		{name: "defaults", vars: required(), wantFleet: 64},
		{
			name:      "configured",
			vars:      with(required(), "SPINNERET_ACQUIRE_FLEET_INFLIGHT", "128", "SPINNERET_ACQUIRE_MAX_INFLIGHT", "32"),
			wantFleet: 128, wantMax: 32,
		},
		{name: "off", vars: with(required(), "SPINNERET_ACQUIRE_FLEET_INFLIGHT", "0"), wantFleet: 0},
		{
			name:    "negative fleet budget",
			vars:    with(required(), "SPINNERET_ACQUIRE_FLEET_INFLIGHT", "-1"),
			wantErr: "SPINNERET_ACQUIRE_FLEET_INFLIGHT must be between 0 (admission control off) and 65536",
		},
		{
			name:    "fleet budget too large",
			vars:    with(required(), "SPINNERET_ACQUIRE_FLEET_INFLIGHT", "65537"),
			wantErr: "SPINNERET_ACQUIRE_FLEET_INFLIGHT must be between 0",
		},
		{
			name:    "negative pinned limit",
			vars:    with(required(), "SPINNERET_ACQUIRE_MAX_INFLIGHT", "-1"),
			wantErr: "SPINNERET_ACQUIRE_MAX_INFLIGHT must be between 0 (derive from the fleet budget) and 4096",
		},
		{
			name:    "pinned limit too large",
			vars:    with(required(), "SPINNERET_ACQUIRE_MAX_INFLIGHT", "4097"),
			wantErr: "SPINNERET_ACQUIRE_MAX_INFLIGHT must be between 0",
		},
		{
			name:    "unparsable",
			vars:    with(required(), "SPINNERET_ACQUIRE_FLEET_INFLIGHT", "abc"),
			wantErr: `SPINNERET_ACQUIRE_FLEET_INFLIGHT: invalid integer "abc"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := LoadFrom(env(tt.vars))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantFleet, c.AcquireFleetInflight)
			require.Equal(t, tt.wantMax, c.AcquireMaxInflight)
			require.Equal(t, strconv.Itoa(tt.wantFleet), c.Redacted()["acquire_fleet_inflight"])
			require.Equal(t, strconv.Itoa(tt.wantMax), c.Redacted()["acquire_max_inflight"])
		})
	}
}
