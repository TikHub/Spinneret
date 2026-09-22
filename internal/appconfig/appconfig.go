// Package appconfig loads the server configuration from SPINNERET_*
// environment variables, applying the defaults documented in section 12 of
// the implementation spec.
package appconfig

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/pkg/netx"
	"github.com/TikHub/Spinneret/internal/updatecheck"
)

// Role selects which subsystems an instance runs.
type Role string

// Supported roles.
const (
	RoleAll    Role = "all"
	RoleAPI    Role = "api"
	RoleWorker Role = "worker"
)

// ServesAPI reports whether the instance serves HTTP APIs.
func (r Role) ServesAPI() bool { return r == RoleAll || r == RoleAPI }

// RunsWorkers reports whether the instance runs background workers.
func (r Role) RunsWorkers() bool { return r == RoleAll || r == RoleWorker }

// EnvSet reports whether the named environment variable supplied a value, as
// opposed to the configuration falling back to its default. Only the settings
// that the console can also change need to ask.
func (c Config) EnvSet(key string) bool { return c.envSet[key] }

// Retention holds data retention periods for partitioned tables.
type Retention struct {
	RiskEvents  time.Duration
	MinuteStats time.Duration
	HourStats   time.Duration
	StateEvents time.Duration
	Audit       time.Duration
}

// Config is the complete server configuration.
type Config struct {
	HTTPAddr   string
	Role       Role
	InstanceID string

	DatabaseURL      string
	DatabaseMaxConns int32

	RedisURL    string
	RedisAddrs  []string
	RedisPrefix string

	ClickHouseURL     string
	ClickHouseTTLDays int

	KEKFile    string
	KEKs       string
	KEKCurrent string

	ReportShards     int
	ReportDedupTTL   time.Duration
	LateReportWindow time.Duration
	StreamMaxLen     int64

	// NotifyAllowPrivateTargets permits notification delivery to private,
	// loopback, link-local (including instance metadata) and multicast
	// addresses. Default false: such targets are refused to prevent SSRF from
	// channel URLs. Enable only for trusted deployments that deliberately send
	// notifications to internal hosts.
	NotifyAllowPrivateTargets bool

	// AcquireFleetInflight is the fleet-wide number of concurrent acquire
	// scripts admission control allows; 0 turns admission control off.
	AcquireFleetInflight int
	// AcquireMaxInflight pins this instance's acquire limit instead of
	// dividing the fleet budget by the live instance count.
	AcquireMaxInflight int

	PayloadCache     bool
	PayloadCacheSize int

	DEKCacheSize int
	DEKCacheTTL  time.Duration

	TokenCacheTTL time.Duration
	SessionTTL    time.Duration
	CookieSecure  string

	TLSCertFile    string
	TLSKeyFile     string
	TrustedProxies []string
	MetricsAddr    string
	// PprofAddr enables the net/http/pprof debug listener on its own address
	// (SPINNERET_PPROF_ADDR). It is empty, and pprof therefore off, by default:
	// the endpoints are unauthenticated and expose memory contents, so the
	// address must never be reachable from outside the deployment.
	PprofAddr string

	LogLevel  string
	LogFormat string

	ProxyCheckURL      string
	ProxyCheckInterval time.Duration
	ProxyCheckTimeout  time.Duration
	ProxyExitIPURL     string
	GeoIPDB            string

	Retention            Retention
	RecordCooldownEvents bool
	MaxWatchers          int
	UIEnabled            bool
	// UpdateCheckURL is the release feed the console's update check reads. The
	// empty string disables the check, so a deployment that must make no
	// outbound call at all can say so.
	UpdateCheckURL       string
	AllowedOrigins       []string
	ShutdownTimeout      time.Duration
	AdminMaxRequestBytes int64
	OTLPEndpoint         string

	// envSet names the variables the environment supplied, filled by Load. It is
	// unexported because it is an implementation detail of EnvSet.
	envSet map[string]bool
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom reads the configuration using lookup, applies defaults and validates it.
func LoadFrom(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("appconfig: nil lookup function")
	}
	l := loader{lookup: lookup}
	c := Config{
		HTTPAddr:         l.str("SPINNERET_HTTP_ADDR", ":8080"),
		Role:             Role(strings.ToLower(l.str("SPINNERET_ROLE", string(RoleAll)))),
		InstanceID:       l.str("SPINNERET_INSTANCE_ID", ""),
		DatabaseURL:      l.str("SPINNERET_DATABASE_URL", ""),
		DatabaseMaxConns: l.int32("SPINNERET_DATABASE_MAX_CONNS", 32),

		RedisURL:    l.str("SPINNERET_REDIS_URL", ""),
		RedisAddrs:  l.list("SPINNERET_REDIS_ADDRS"),
		RedisPrefix: l.str("SPINNERET_REDIS_PREFIX", "sp"),

		ClickHouseURL:     l.str("SPINNERET_CLICKHOUSE_URL", ""),
		ClickHouseTTLDays: l.int("SPINNERET_CLICKHOUSE_TTL_DAYS", 90),

		KEKFile:    l.str("SPINNERET_KEK_FILE", ""),
		KEKs:       l.str("SPINNERET_KEKS", ""),
		KEKCurrent: l.str("SPINNERET_KEK_CURRENT", ""),

		ReportShards:     l.int("SPINNERET_REPORT_SHARDS", 16),
		ReportDedupTTL:   l.dur("SPINNERET_REPORT_DEDUP_TTL", time.Hour),
		LateReportWindow: l.dur("SPINNERET_LATE_REPORT_WINDOW", 10*time.Minute),
		StreamMaxLen:     int64(l.int("SPINNERET_STREAM_MAXLEN", 1_000_000)),

		NotifyAllowPrivateTargets: l.bool("SPINNERET_NOTIFY_ALLOW_PRIVATE_TARGETS", false),

		AcquireFleetInflight: l.int("SPINNERET_ACQUIRE_FLEET_INFLIGHT", 64),
		AcquireMaxInflight:   l.int("SPINNERET_ACQUIRE_MAX_INFLIGHT", 0),

		PayloadCache:     l.bool("SPINNERET_PAYLOAD_CACHE", true),
		PayloadCacheSize: l.int("SPINNERET_PAYLOAD_CACHE_SIZE", 200_000),

		DEKCacheSize: l.int("SPINNERET_DEK_CACHE_SIZE", 100_000),
		DEKCacheTTL:  l.dur("SPINNERET_DEK_CACHE_TTL", 10*time.Minute),

		TokenCacheTTL: l.dur("SPINNERET_TOKEN_CACHE_TTL", 30*time.Second),
		SessionTTL:    l.dur("SPINNERET_SESSION_TTL", 12*time.Hour),
		CookieSecure:  strings.ToLower(l.str("SPINNERET_COOKIE_SECURE", "auto")),

		TLSCertFile:    l.str("SPINNERET_TLS_CERT_FILE", ""),
		TLSKeyFile:     l.str("SPINNERET_TLS_KEY_FILE", ""),
		TrustedProxies: l.list("SPINNERET_TRUSTED_PROXIES"),
		MetricsAddr:    l.str("SPINNERET_METRICS_ADDR", ""),
		PprofAddr:      l.str("SPINNERET_PPROF_ADDR", ""),

		LogLevel:  strings.ToLower(l.str("SPINNERET_LOG_LEVEL", "info")),
		LogFormat: strings.ToLower(l.str("SPINNERET_LOG_FORMAT", "json")),

		ProxyCheckURL:      l.str("SPINNERET_PROXY_CHECK_URL", "http://example.com/"),
		ProxyCheckInterval: l.dur("SPINNERET_PROXY_CHECK_INTERVAL", 60*time.Second),
		ProxyCheckTimeout:  l.dur("SPINNERET_PROXY_CHECK_TIMEOUT", 10*time.Second),
		ProxyExitIPURL:     l.str("SPINNERET_PROXY_EXIT_IP_URL", ""),
		GeoIPDB:            l.str("SPINNERET_GEOIP_DB", ""),

		Retention: Retention{
			RiskEvents:  l.dur("SPINNERET_RETENTION_RISK_EVENTS", 720*time.Hour),
			MinuteStats: l.dur("SPINNERET_RETENTION_MINUTE_STATS", 720*time.Hour),
			HourStats:   l.dur("SPINNERET_RETENTION_HOUR_STATS", 4320*time.Hour),
			StateEvents: l.dur("SPINNERET_RETENTION_STATE_EVENTS", 8760*time.Hour),
			Audit:       l.dur("SPINNERET_RETENTION_AUDIT", 8760*time.Hour),
		},
		RecordCooldownEvents: l.bool("SPINNERET_RECORD_COOLDOWN_EVENTS", true),
		MaxWatchers:          l.int("SPINNERET_MAX_WATCHERS", 20_000),
		UIEnabled:            l.bool("SPINNERET_UI_ENABLED", true),
		UpdateCheckURL:       l.str("SPINNERET_UPDATE_CHECK_URL", updatecheck.DefaultURL),
		AllowedOrigins:       l.list("SPINNERET_ALLOWED_ORIGINS"),
		ShutdownTimeout:      l.dur("SPINNERET_SHUTDOWN_TIMEOUT", 30*time.Second),
		AdminMaxRequestBytes: int64(l.int("SPINNERET_ADMIN_MAX_REQUEST_BYTES", 64<<20)),
		OTLPEndpoint:         l.str("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
	}
	if c.InstanceID == "" {
		c.InstanceID = defaultInstanceID()
	}
	c.envSet = l.seen
	if err := errors.Join(l.errs...); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks required settings and value ranges.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	switch c.Role {
	case RoleAll, RoleAPI, RoleWorker:
	default:
		add("SPINNERET_ROLE must be one of all, api, worker (got %q)", c.Role)
	}
	if c.DatabaseURL == "" {
		add("SPINNERET_DATABASE_URL is required")
	}
	if c.RedisURL == "" && len(c.RedisAddrs) == 0 {
		add("SPINNERET_REDIS_URL or SPINNERET_REDIS_ADDRS is required")
	}
	if c.KEKFile == "" && c.KEKs == "" {
		add("SPINNERET_KEK_FILE or SPINNERET_KEKS is required")
	}
	if c.RedisPrefix == "" || strings.ContainsAny(c.RedisPrefix, "{}: ") {
		add("SPINNERET_REDIS_PREFIX must be non-empty and must not contain '{', '}', ':' or spaces")
	}
	if c.DatabaseMaxConns < 2 {
		add("SPINNERET_DATABASE_MAX_CONNS must be >= 2")
	}
	if c.ReportShards < 1 || c.ReportShards > 255 {
		add("SPINNERET_REPORT_SHARDS must be between 1 and 255")
	}
	if c.ReportDedupTTL < time.Minute {
		add("SPINNERET_REPORT_DEDUP_TTL must be >= 1m")
	}
	if c.LateReportWindow < 0 {
		add("SPINNERET_LATE_REPORT_WINDOW must be >= 0")
	}
	if c.StreamMaxLen < 1000 {
		add("SPINNERET_STREAM_MAXLEN must be >= 1000")
	}
	if c.AcquireFleetInflight < 0 || c.AcquireFleetInflight > 65536 {
		add("SPINNERET_ACQUIRE_FLEET_INFLIGHT must be between 0 (admission control off) and 65536")
	}
	if c.AcquireMaxInflight < 0 || c.AcquireMaxInflight > 4096 {
		add("SPINNERET_ACQUIRE_MAX_INFLIGHT must be between 0 (derive from the fleet budget) and 4096")
	}
	if c.PayloadCacheSize < 0 || c.DEKCacheSize < 1 {
		add("cache sizes must be positive")
	}
	if c.PayloadCache && c.PayloadCacheSize < 1 {
		add("SPINNERET_PAYLOAD_CACHE_SIZE must be >= 1 when SPINNERET_PAYLOAD_CACHE is enabled")
	}
	if c.SessionTTL < time.Minute {
		add("SPINNERET_SESSION_TTL must be >= 1m")
	}
	if _, err := netx.ParsePrefixes(c.TrustedProxies); err != nil {
		add("SPINNERET_TRUSTED_PROXIES: %v", err)
	}
	for _, r := range []struct {
		name string
		v    time.Duration
	}{
		{"SPINNERET_RETENTION_RISK_EVENTS", c.Retention.RiskEvents},
		{"SPINNERET_RETENTION_MINUTE_STATS", c.Retention.MinuteStats},
		{"SPINNERET_RETENTION_HOUR_STATS", c.Retention.HourStats},
		{"SPINNERET_RETENTION_STATE_EVENTS", c.Retention.StateEvents},
		{"SPINNERET_RETENTION_AUDIT", c.Retention.Audit},
	} {
		if r.v < 24*time.Hour {
			add("%s must be >= 24h", r.name)
		}
	}
	switch c.CookieSecure {
	case "auto", "true", "false":
	default:
		add("SPINNERET_COOKIE_SECURE must be auto, true or false")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		add("SPINNERET_TLS_CERT_FILE and SPINNERET_TLS_KEY_FILE must be set together")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "warning", "error":
	default:
		add("SPINNERET_LOG_LEVEL must be debug, info, warn or error")
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		add("SPINNERET_LOG_FORMAT must be json or text")
	}
	if c.ProxyCheckInterval < time.Second || c.ProxyCheckTimeout < 100*time.Millisecond {
		add("proxy check interval/timeout too small")
	}
	if c.ClickHouseTTLDays < 1 || c.ClickHouseTTLDays > 3650 {
		add("SPINNERET_CLICKHOUSE_TTL_DAYS must be between 1 and 3650")
	}
	if c.MaxWatchers < 1 {
		add("SPINNERET_MAX_WATCHERS must be >= 1")
	}
	if c.AdminMaxRequestBytes < 1<<20 {
		add("SPINNERET_ADMIN_MAX_REQUEST_BYTES must be >= 1MiB")
	}
	if sameFixedAddr(c.PprofAddr, c.HTTPAddr) {
		add("SPINNERET_PPROF_ADDR must not be the API address: the profiling endpoints are unauthenticated")
	}
	if sameFixedAddr(c.PprofAddr, c.MetricsAddr) {
		add("SPINNERET_PPROF_ADDR must not be the metrics address")
	}
	return errors.Join(errs...)
}

// sameFixedAddr reports whether two listen addresses are the same fixed
// address. Port 0 (pick a free port, used by tests) never collides.
func sameFixedAddr(a, b string) bool {
	if a == "" || a != b {
		return false
	}
	_, port, err := net.SplitHostPort(a)
	return err != nil || port != "0"
}

// Redacted returns settings suitable for logging with credentials masked.
func (c Config) Redacted() map[string]string {
	return map[string]string{
		"http_addr":        c.HTTPAddr,
		"role":             string(c.Role),
		"instance_id":      c.InstanceID,
		"database_url":     redactURL(c.DatabaseURL),
		"redis_url":        redactURL(c.RedisURL),
		"redis_addrs":      strings.Join(c.RedisAddrs, ","),
		"redis_prefix":     c.RedisPrefix,
		"clickhouse_url":   redactURL(c.ClickHouseURL),
		"kek_file":         c.KEKFile,
		"keks":             maskPresence(c.KEKs),
		"kek_current":      c.KEKCurrent,
		"report_shards":    strconv.Itoa(c.ReportShards),
		"payload_cache":    strconv.FormatBool(c.PayloadCache),
		"tls":              strconv.FormatBool(c.TLSCertFile != ""),
		"metrics_addr":     c.MetricsAddr,
		"pprof_addr":       c.PprofAddr,
		"log_level":        c.LogLevel,
		"ui_enabled":       strconv.FormatBool(c.UIEnabled),
		"otlp_endpoint":    redactURL(c.OTLPEndpoint),
		"late_window":      c.LateReportWindow.String(),
		"report_dedup_ttl": c.ReportDedupTTL.String(),

		"acquire_fleet_inflight": strconv.Itoa(c.AcquireFleetInflight),
		"acquire_max_inflight":   strconv.Itoa(c.AcquireMaxInflight),

		"notify_allow_private_targets": strconv.FormatBool(c.NotifyAllowPrivateTargets),
	}
}

// redactedSecret replaces credentials in redacted URLs and DSNs.
const redactedSecret = "xxxxx"

// sensitiveParams are query parameter / DSN keys whose values are masked.
var sensitiveParams = map[string]struct{}{
	"password": {}, "passwd": {}, "pass": {}, "pwd": {}, "secret": {}, "token": {},
	"sslpassword": {}, "access_token": {}, "api_key": {}, "apikey": {},
}

// kvSecretPattern matches key=value DSN pairs (libpq style) with a sensitive
// key; the value may be single-quoted with backslash escapes.
var kvSecretPattern = regexp.MustCompile(`(?i)\b(password|passwd|pass|pwd|secret|token|sslpassword|access_token|api_key|apikey)(\s*=\s*)('(?:[^'\\]|\\.)*'|[^\s]*)`)

// redactURL masks credentials in URLs ("scheme://user:password@host?password=")
// and in key=value DSNs ("host=db password=secret").
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		return kvSecretPattern.ReplaceAllString(raw, "${1}${2}"+redactedSecret)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid url>"
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), redactedSecret)
		}
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "<invalid url>"
		}
		for k := range q {
			if _, ok := sensitiveParams[strings.ToLower(k)]; ok {
				q[k] = []string{redactedSecret}
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func maskPresence(s string) string {
	if s == "" {
		return ""
	}
	return "<set>"
}

func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "spinneret"
	}
	var b [3]byte
	_, _ = rand.Read(b[:])
	return host + "-" + hex.EncodeToString(b[:])
}

type loader struct {
	lookup func(string) (string, bool)
	errs   []error
	// seen records the keys the environment actually supplied a value for, as
	// opposed to the ones that fell back to a default. Settings that can also be
	// changed from the console need the difference: a variable that is set pins
	// the setting, so that a deployment managed from a file keeps the guarantee
	// that the file is what runs.
	seen map[string]bool
}

func (l *loader) raw(key string) (string, bool) {
	v, ok := l.lookup(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	l.seen[key] = true
	return v, true
}

func (l *loader) str(key, def string) string {
	if v, ok := l.raw(key); ok {
		return v
	}
	return def
}

func (l *loader) int(key string, def int) int {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid integer %q", key, v))
		return def
	}
	return n
}

func (l *loader) int32(key string, def int32) int32 {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < math.MinInt32 || n > math.MaxInt32 {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid 32-bit integer %q", key, v))
		return def
	}
	return int32(n)
}

func (l *loader) bool(key string, def bool) bool {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid boolean %q", key, v))
		return def
	}
	return b
}

func (l *loader) dur(key string, def time.Duration) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	d, err := durationx.Parse(v)
	if err != nil || d.IsPermanent() {
		l.errs = append(l.errs, fmt.Errorf("%s: invalid duration %q", key, v))
		return def
	}
	return d.Std()
}

func (l *loader) list(key string) []string {
	v, ok := l.raw(key)
	if !ok {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
