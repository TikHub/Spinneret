package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Defaults of a Checker.
const (
	// DefaultURL is the release feed of this project. It is a plain public GET
	// that carries nothing about the deployment making it.
	DefaultURL = "https://api.github.com/repos/TikHub/Spinneret/releases/latest"
	// DefaultTimeout bounds one outbound call.
	DefaultTimeout = 10 * time.Second
	// DefaultCacheTTL is how long a successful answer is reused. The feed is
	// rate limited per source address (60 requests an hour, unauthenticated),
	// and a console with several operators clicking the button must not spend
	// that budget.
	DefaultCacheTTL = time.Hour
	// failureCacheTTL is how long a failure is remembered, so that a network
	// that cannot reach the feed is not retried on every click.
	failureCacheTTL = time.Minute
	// maxBodyBytes caps what is read from the feed.
	maxBodyBytes = 1 << 20
)

// Result is the answer to one check.
type Result struct {
	// Current is the running build.
	Current string
	// Latest is the newest published release, empty when it is not known.
	Latest string
	// ReleaseURL points at the release notes of Latest.
	ReleaseURL string
	// UpdateAvailable is true only when Latest is a strictly newer release than
	// a Current that names a release at all.
	UpdateAvailable bool
	// CheckedAt is when the feed was last read, zero when it never was.
	CheckedAt time.Time
	// Disabled is true when the operator turned the check off; the other
	// fields except Current are then empty.
	Disabled bool
}

// Config configures a Checker.
type Config struct {
	// URL of the release feed. Empty disables the check entirely, which is how
	// a deployment that must make no outbound call at all is configured.
	URL string
	// Current is the running build, normally version.String().
	Current string
	// Timeout bounds one call; zero means DefaultTimeout.
	Timeout time.Duration
	// CacheTTL is how long a successful answer is reused; zero means
	// DefaultCacheTTL.
	CacheTTL time.Duration
	// Client is replaced in tests; nil means a client with Timeout.
	Client *http.Client
	// Now is replaced in tests; nil means time.Now.
	Now func() time.Time
}

// Checker reads the release feed on demand and caches what it learns. It is
// safe for concurrent use.
type Checker struct {
	cfg    Config
	client *http.Client

	mu       sync.Mutex
	cached   Result
	cachedAt time.Time
	cachedOK bool
	lastErr  error
}

// New creates a Checker. A Checker with an empty Config.URL is disabled and
// makes no outbound call.
func New(cfg Config) *Checker {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Checker{cfg: cfg, client: client}
}

// Enabled reports whether a feed is configured.
func (c *Checker) Enabled() bool { return c != nil && strings.TrimSpace(c.cfg.URL) != "" }

// Check returns the newest release, from cache when a recent answer is held.
// A failure returns the error and a Result that still carries the current
// build, so a caller can show "we could not reach the feed" without losing the
// version the operator came to read.
func (c *Checker) Check(ctx context.Context) (Result, error) {
	if !c.Enabled() {
		return Result{Current: c.cfg.Current, Disabled: true}, nil
	}

	now := c.cfg.Now()
	c.mu.Lock()
	if c.cachedOK && now.Sub(c.cachedAt) < c.cfg.CacheTTL {
		out := c.cached
		c.mu.Unlock()
		return out, nil
	}
	if !c.cachedOK && c.lastErr != nil && now.Sub(c.cachedAt) < failureCacheTTL {
		err := c.lastErr
		c.mu.Unlock()
		return Result{Current: c.cfg.Current}, err
	}
	c.mu.Unlock()

	latest, url, err := c.fetch(ctx)
	now = c.cfg.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedAt = now
	if err != nil {
		c.cachedOK = false
		c.lastErr = err
		return Result{Current: c.cfg.Current}, err
	}
	c.lastErr = nil
	c.cachedOK = true
	c.cached = Result{
		Current:         c.cfg.Current,
		Latest:          latest,
		ReleaseURL:      url,
		UpdateAvailable: IsNewer(c.cfg.Current, latest),
		CheckedAt:       now,
	}
	return c.cached, nil
}

// release is the part of the feed's payload this package reads.
type release struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// fetch reads the feed once.
func (c *Checker) fetch(ctx context.Context) (tag, url string, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.URL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build update check request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// The feed rejects requests without a user agent. It names the product and
	// nothing about this deployment.
	req.Header.Set("User-Agent", "Spinneret")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("reach the release feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// A feed with nothing published answers 404, which is an answer rather than
	// a failure: there is no newer release because there is no release at all.
	if resp.StatusCode == http.StatusNotFound {
		return "", "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("release feed answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", "", fmt.Errorf("read the release feed: %w", err)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", "", fmt.Errorf("decode the release feed: %w", err)
	}
	if rel.Draft || rel.Prerelease {
		// The feed's "latest" excludes these, but a custom URL might not, and a
		// pre-release must never be offered as an upgrade.
		return "", rel.HTMLURL, nil
	}
	return strings.TrimSpace(rel.TagName), rel.HTMLURL, nil
}
