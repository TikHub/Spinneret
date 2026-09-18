package spinneret

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"connectrpc.com/connect"
)

// Default timeouts.
const (
	// DefaultTimeout bounds ordinary unary calls.
	DefaultTimeout = 10 * time.Second
	// WatchGrace is added to the server-side wait of WatchConfig long polls.
	WatchGrace = 5 * time.Second
	// DefaultDialTimeout bounds TCP connection setup of the default HTTP client.
	DefaultDialTimeout = 3 * time.Second
)

// Options configures a [Client]. Empty BaseURL, Token and Node fall back to
// the SPINNERET_URL, SPINNERET_TOKEN and SPINNERET_NODE environment variables.
type Options struct {
	// BaseURL is the server URL, e.g. "https://spinneret.internal" (a path
	// prefix is allowed; query, fragment and credentials are not).
	BaseURL string
	// Token is the node API token ("spn_..."). It is only sent in the
	// Authorization header and never logged.
	Token string
	// Node is sent as X-Spinneret-Node; defaults to the host name. Characters
	// outside [A-Za-z0-9._:@-] are replaced by "-" and the value is cut to
	// 128 characters.
	Node string
	// HTTPClient sends the requests. The default client uses a 3s dial
	// timeout, HTTP/2 over TLS, the proxy environment variables, and
	// unencrypted HTTP/2 (h2c) for http:// URLs when UseGRPC is set. A custom
	// client must not set a total timeout shorter than the WatchConfig wait.
	HTTPClient connect.HTTPClient
	// UseGRPC selects the gRPC protocol (binary protobuf, HTTP/2 required).
	// The default is the Connect protocol with JSON.
	UseGRPC bool
	// Timeout bounds each attempt of an ordinary unary call (default 10s).
	// Acquire adds wait_ms; WatchConfig uses timeout_ms plus 5s instead.
	Timeout time.Duration
	// Retry is the retry policy of unary calls; nil uses DefaultRetryPolicy
	// and NoRetry() disables retries.
	Retry *RetryPolicy
	// Reporter tunes the background reporter returned by [Client.Reporter].
	Reporter ReporterOptions
	// Logger receives SDK logs (never tokens, credentials, proxy URLs, config
	// contents or secret values); nil uses slog.Default().
	Logger *slog.Logger
	// UserAgent is prepended to the SDK user agent.
	UserAgent string
}

// settings are validated, defaulted options.
type settings struct {
	baseURL    string
	host       string
	token      string
	node       string
	httpClient connect.HTTPClient
	transport  *http.Transport // default client transport, closed by Client.Close
	useGRPC    bool
	timeout    time.Duration
	retry      RetryPolicy
	reporter   ReporterOptions
	logger     *slog.Logger
	userAgent  string
}

func (o Options) resolve(getenv func(string) string) (settings, error) {
	s := settings{useGRPC: o.UseGRPC, httpClient: o.HTTPClient}
	baseURL := firstNonEmpty(o.BaseURL, getenv(EnvURL))
	if baseURL == "" {
		return s, errInvalidOptions("server URL is not configured (set Options.BaseURL or %s)", EnvURL)
	}
	normalized, host, err := normalizeBaseURL(baseURL)
	if err != nil {
		return s, err
	}
	s.baseURL, s.host = normalized, host
	token := strings.TrimSpace(firstNonEmpty(o.Token, getenv(EnvToken)))
	if token == "" {
		return s, errInvalidOptions("token is not configured (set Options.Token or %s)", EnvToken)
	}
	if strings.ContainsFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return s, errInvalidOptions("token must not contain whitespace or control characters")
	}
	s.token = token
	node := firstNonEmpty(o.Node, getenv(EnvNode))
	if node == "" {
		node = DefaultNodeName()
	}
	s.node = SanitizeNodeName(node)
	if o.Timeout < 0 {
		return s, errInvalidOptions("timeout must not be negative")
	}
	s.timeout = o.Timeout
	if s.timeout == 0 {
		s.timeout = DefaultTimeout
	}
	s.retry = DefaultRetryPolicy()
	if o.Retry != nil {
		if s.retry, err = o.Retry.normalized(); err != nil {
			return s, err
		}
	}
	if s.reporter, err = o.Reporter.normalized(); err != nil {
		return s, err
	}
	s.logger = o.Logger
	if s.logger == nil {
		s.logger = slog.Default()
	}
	s.userAgent = strings.TrimSpace(o.UserAgent + " spinneret-go/" + Version)
	if s.httpClient == nil {
		s.transport = newTransport(o.UseGRPC)
		s.httpClient = &http.Client{Transport: s.transport}
	}
	return s, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// normalizeBaseURL validates a server URL and returns it without trailing
// slashes, together with its host[:port].
func normalizeBaseURL(raw string) (string, string, error) {
	candidate := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(candidate)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", errInvalidOptions("invalid server URL: expected http(s)://host[:port][/prefix]")
	}
	if u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(candidate, "?#") {
		return "", "", errInvalidOptions("invalid server URL: query and fragment are not allowed")
	}
	if u.User != nil {
		return "", "", errInvalidOptions("invalid server URL: credentials must not be embedded")
	}
	return candidate, u.Host, nil
}

// SanitizeNodeName normalizes a node name to a header-safe value of at most
// 128 characters: characters outside [A-Za-z0-9._:@-] become "-", leading
// and trailing "-" are removed, and an empty result becomes "unknown".
func SanitizeNodeName(name string) string {
	var b strings.Builder
	inInvalidRun := false
	for _, r := range strings.TrimSpace(name) {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("._:@-", r)
		if valid {
			b.WriteRune(r)
			inInvalidRun = false
			continue
		}
		if !inInvalidRun {
			b.WriteByte('-')
			inInvalidRun = true
		}
	}
	cleaned := strings.Trim(b.String(), "-")
	if len(cleaned) > MaxNodeNameLength {
		cleaned = cleaned[:MaxNodeNameLength]
	}
	if cleaned == "" {
		return "unknown"
	}
	return cleaned
}

// DefaultNodeName returns the sanitized host name, or "unknown".
func DefaultNodeName() string {
	host, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return SanitizeNodeName(host)
}

// newTransport builds the transport of the default HTTP client.
func newTransport(useGRPC bool) *http.Transport {
	dialer := &net.Dialer{Timeout: DefaultDialTimeout, KeepAlive: 30 * time.Second}
	protocols := new(http.Protocols)
	if useGRPC {
		// gRPC needs HTTP/2: negotiated with ALPN over TLS, prior knowledge (h2c) otherwise.
		protocols.SetHTTP2(true)
		protocols.SetUnencryptedHTTP2(true)
	} else {
		protocols.SetHTTP1(true)
		protocols.SetHTTP2(true)
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		Protocols:             protocols,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}
