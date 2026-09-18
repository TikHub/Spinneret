//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
)

// mockRule is a mocktarget site rule (PUT /_admin/rules).
type mockRule struct {
	Prefix       string   `json:"prefix"`
	Mode         string   `json:"mode"`
	Status       int      `json:"status,omitempty"`
	BusinessCode string   `json:"business_code,omitempty"`
	Identities   []string `json:"identities,omitempty"`
	Proxies      []string `json:"proxies,omitempty"`
}

// mockProxyRule is a mocktarget proxy behavior (PUT /_admin/proxies).
type mockProxyRule struct {
	ProxyID   string `json:"proxy_id"`
	Mode      string `json:"mode"`
	LatencyMs int    `json:"latency_ms,omitempty"`
}

// mockStats is the subset of GET /_admin/stats the scenarios read.
type mockStats struct {
	Total   int64 `json:"total"`
	Entries []struct {
		Path     string `json:"path"`
		Mode     string `json:"mode"`
		Identity string `json:"identity"`
		Proxy    string `json:"proxy"`
		Status   int    `json:"status"`
		Count    int64  `json:"count"`
	} `json:"entries"`
	ProxyListener struct {
		ByResult map[string]int64 `json:"by_result"`
	} `json:"proxy_listener"`
}

// mockAdmin drives the mock target admin API.
type mockAdmin struct {
	baseURL string
	http    *http.Client
}

func newMockAdmin(baseURL string) *mockAdmin {
	return &mockAdmin{baseURL: baseURL, http: &http.Client{Timeout: 10 * time.Second}}
}

func (m *mockAdmin) do(ctx context.Context, t *testing.T, method, path string, body any, out any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.baseURL+path, rd)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	require.NoErrorf(t, err, "mocktarget %s %s", method, path)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "mocktarget %s %s: %s", method, path, data)
	if out != nil {
		require.NoErrorf(t, json.Unmarshal(data, out), "decode mocktarget %s %s", method, path)
	}
}

func (m *mockAdmin) setRules(ctx context.Context, t *testing.T, rules ...mockRule) {
	t.Helper()
	if rules == nil {
		rules = []mockRule{}
	}
	m.do(ctx, t, http.MethodPut, "/_admin/rules", rules, nil)
}

func (m *mockAdmin) clearRules(ctx context.Context, t *testing.T) {
	t.Helper()
	m.do(ctx, t, http.MethodDelete, "/_admin/rules", nil, nil)
}

func (m *mockAdmin) setProxyRules(ctx context.Context, t *testing.T, rules ...mockProxyRule) {
	t.Helper()
	m.do(ctx, t, http.MethodPut, "/_admin/proxies", rules, nil)
}

func (m *mockAdmin) clearProxyRules(ctx context.Context, t *testing.T) {
	t.Helper()
	m.do(ctx, t, http.MethodDelete, "/_admin/proxies", nil, nil)
}

func (m *mockAdmin) stats(ctx context.Context, t *testing.T, query url.Values) mockStats {
	t.Helper()
	var s mockStats
	m.do(ctx, t, http.MethodGet, "/_admin/stats?"+query.Encode(), nil, &s)
	return s
}

// fetchResult is what a crawler observed for one request.
type fetchResult struct {
	Status       int
	Marker       string
	BusinessCode string
	ErrorKind    string
	Bytes        int64
	Started      time.Time
	Finished     time.Time
	// Identity and Proxy are the attribution echoed by the mock target (X-Mock-Identity / X-Mock-Proxy).
	Identity string
	Proxy    string
}

// crawler sends target requests with lease credentials through the leased proxy, like a node would.
type crawler struct {
	targetURL       string
	clientProxyHost string

	mu         sync.Mutex
	transports map[string]*http.Transport
}

func newCrawler(cfg config) *crawler {
	return &crawler{targetURL: cfg.TargetURL, clientProxyHost: cfg.ClientProxyHost, transports: map[string]*http.Transport{}}
}

// transport returns a keep-alive transport for the proxy URL (direct when empty).
func (c *crawler) transport(proxyURL string) (*http.Transport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tr, ok := c.transports[proxyURL]; ok {
		return tr, nil
	}
	tr := &http.Transport{
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy URL: %w", err)
		}
		if c.clientProxyHost != "" {
			u.Host = c.clientProxyHost
		}
		tr.Proxy = http.ProxyURL(u)
	}
	c.transports[proxyURL] = tr
	return tr, nil
}

// fetch performs GET <target><uri> with the credential of the lease and derives report facts from the
// mock target response headers.
func (c *crawler) fetch(ctx context.Context, lease *spinneretv1.AcquireResponse, uri string) fetchResult {
	res := fetchResult{Started: time.Now()}
	defer func() { res.Finished = time.Now() }()
	proxyURL := lease.GetProxy().GetUrl()
	tr, err := c.transport(proxyURL)
	if err != nil {
		res.ErrorKind = "other"
		res.Finished = time.Now()
		return res
	}
	target, err := url.Parse(c.targetURL + uri)
	if err != nil {
		res.ErrorKind = "other"
		res.Finished = time.Now()
		return res
	}
	cred := lease.GetCredential()
	if len(cred.GetQuery()) > 0 {
		q := target.Query()
		for k, v := range cred.GetQuery() {
			q.Set(k, v)
		}
		target.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		res.ErrorKind = "other"
		res.Finished = time.Now()
		return res
	}
	for k, v := range cred.GetHeaders() {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	if h := cred.GetCookieHeader(); h != "" {
		req.Header.Set("Cookie", h)
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   15 * time.Second,
		// Redirects (login_redirect) are observations to report, not to follow.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		res.ErrorKind = classifyError(err)
		res.Finished = time.Now()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	n, _ := io.Copy(io.Discard, resp.Body)
	res.Finished = time.Now()
	res.Status = resp.StatusCode
	res.Bytes = n
	res.Marker = resp.Header.Get("X-Mock-Marker")
	res.BusinessCode = resp.Header.Get("X-Mock-Business-Code")
	res.Identity = resp.Header.Get("X-Mock-Identity")
	res.Proxy = resp.Header.Get("X-Mock-Proxy")
	if resp.StatusCode == http.StatusProxyAuthRequired {
		res.ErrorKind = "proxy_auth"
	}
	return res
}

// classifyError maps a transport error to a report error_kind.
func classifyError(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "connection refused"):
		return "conn_refused"
	case strings.Contains(err.Error(), "connection reset"), strings.Contains(err.Error(), "EOF"):
		return "conn_reset"
	case strings.Contains(err.Error(), "no such host"):
		return "dns"
	default:
		return "other"
	}
}

// report builds the report of a fetch.
func (r fetchResult) report(leaseID, uri, reportID string, release bool) *spinneretv1.Report {
	var markers []string
	if r.Marker != "" {
		markers = []string{r.Marker}
	}
	path := uri
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	return &spinneretv1.Report{
		ReportId:      reportID,
		LeaseId:       leaseID,
		Uri:           path,
		Method:        http.MethodGet,
		HttpStatus:    int32(r.Status),
		BusinessCode:  r.BusinessCode,
		ErrorKind:     r.ErrorKind,
		Markers:       markers,
		LatencyMs:     int32(r.Finished.Sub(r.Started).Milliseconds()),
		ResponseBytes: r.Bytes,
		StartedAt:     timestamppb.New(r.Started),
		FinishedAt:    timestamppb.New(r.Finished),
		Release:       release,
	}
}
