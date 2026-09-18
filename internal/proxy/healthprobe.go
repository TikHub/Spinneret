package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
	xproxy "golang.org/x/net/proxy"
)

// Probe limits.
const (
	maxCheckBodyBytes  = 64 << 10
	maxExitIPBodyBytes = 4 << 10
	maxHeaderBytes     = 64 << 10
	maxErrorLength     = 256
	checkUserAgent     = "spinneret-proxy-check/1"
)

// probe performs the check request (and the optional exit IP lookup) through u.
func (h *HealthChecker) probe(ctx context.Context, u ParsedURL) CheckResult {
	transport, err := h.transport(u)
	if err != nil {
		return CheckResult{Error: redact(err.Error(), u)}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	res := CheckResult{}
	start := time.Now()
	status, _, err := h.fetch(ctx, client, h.cfg.CheckURL, maxCheckBodyBytes)
	if err != nil {
		res.Error = describeError(err, u)
		return res
	}
	if status >= http.StatusBadRequest {
		res.Error = fmt.Sprintf("check request returned HTTP %d", status)
		return res
	}
	res.OK = true
	res.LatencyMs = int(time.Since(start).Milliseconds())

	if h.cfg.ExitIPURL == "" {
		return res
	}
	status, body, err := h.fetch(ctx, client, h.cfg.ExitIPURL, maxExitIPBodyBytes)
	if err != nil || status >= http.StatusBadRequest {
		h.logger.Debug("proxy exit ip lookup failed", slog.Int("status", status), slog.String("error", describeErrorOrEmpty(err, u)))
		return res
	}
	if ip, ok := parseExitIP(body); ok {
		res.ExitIP = ip.String()
		geo := h.lookupRegion(ip)
		res.Region, res.city = geo.region, geo.city
	}
	return res
}

// fetch performs a GET with the check timeout and returns the status and at
// most limit bytes of the body.
func (h *HealthChecker) fetch(ctx context.Context, client *http.Client, target string, limit int64) (int, []byte, error) {
	rctx, cancel := context.WithTimeout(ctx, h.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, errors.New("invalid check url")
	}
	req.Header.Set("User-Agent", checkUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, body, nil
}

// transport builds a single-use transport that routes through u.
func (h *HealthChecker) transport(u ParsedURL) (*http.Transport, error) {
	dialer := &net.Dialer{Timeout: h.cfg.Timeout}
	t := &http.Transport{
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    h.cfg.Timeout,
		ResponseHeaderTimeout:  h.cfg.Timeout,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: maxHeaderBytes,
	}
	if h.tlsConfig != nil {
		t.TLSClientConfig = h.tlsConfig.Clone()
	}
	switch u.Scheme {
	case SchemeHTTP, SchemeHTTPS:
		t.Proxy = http.ProxyURL(u.URL())
	case SchemeSOCKS5:
		var auth *xproxy.Auth
		if u.HasUser {
			auth = &xproxy.Auth{User: u.Username, Password: u.Password}
		}
		d, err := xproxy.SOCKS5("tcp", u.HostPort(), auth, dialer)
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		cd, ok := d.(xproxy.ContextDialer)
		if !ok {
			return nil, errors.New("socks5 dialer does not support contexts")
		}
		t.DialContext = cd.DialContext
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	return t, nil
}

// parseExitIP extracts an IP from a JSON {"ip": "..."} or plain-text body.
func parseExitIP(body []byte) (netip.Addr, bool) {
	text := strings.TrimSpace(string(body))
	var obj struct {
		IP string `json:"ip"`
	}
	if strings.HasPrefix(text, "{") && json.Unmarshal([]byte(text), &obj) == nil {
		text = strings.TrimSpace(obj.IP)
	}
	ip, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// geoInfo is the location resolved from an exit IP.
type geoInfo struct {
	region string
	city   string
}

// geoRecord is the subset of GeoIP2/GeoLite2 City and Country records used.
type geoRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
}

// lookupRegion resolves the country code and English city name of ip.
func (h *HealthChecker) lookupRegion(ip netip.Addr) geoInfo {
	if h.cfg.GeoIPDB == "" {
		return geoInfo{}
	}
	h.geoOnce.Do(func() {
		r, err := maxminddb.Open(h.cfg.GeoIPDB)
		if err != nil {
			h.logger.Error("open geoip database failed", slog.Any("error", err))
			return
		}
		h.geoMu.Lock()
		h.geo = r
		h.geoMu.Unlock()
	})
	h.geoMu.RLock()
	defer h.geoMu.RUnlock()
	if h.geo == nil {
		return geoInfo{}
	}
	var rec geoRecord
	if err := h.geo.Lookup(ip).Decode(&rec); err != nil {
		h.logger.Debug("geoip lookup failed", slog.Any("error", err))
		return geoInfo{}
	}
	return geoInfo{region: rec.Country.ISOCode, city: rec.City.Names["en"]}
}

// describeError turns a request error into a short message without the
// request URL or credentials.
func describeError(err error, u ParsedURL) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		msg = "timeout: " + msg
	}
	return truncate(redact(msg, u), maxErrorLength)
}

func describeErrorOrEmpty(err error, u ParsedURL) string {
	if err == nil {
		return ""
	}
	return describeError(err, u)
}

// minRedactLength avoids mangling messages by redacting very short credentials.
const minRedactLength = 3

// redact replaces the credentials of u (raw and percent-encoded) in msg.
func redact(msg string, u ParsedURL) string {
	if !u.HasUser {
		return msg
	}
	for _, secret := range []string{u.Password, u.Username} {
		if len(secret) < minRedactLength {
			continue
		}
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if len(form) >= minRedactLength {
				msg = strings.ReplaceAll(msg, form, "***")
			}
		}
	}
	return msg
}
