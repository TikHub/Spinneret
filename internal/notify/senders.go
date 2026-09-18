package notify

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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/version"
)

// HTTP delivery limits.
const (
	// DefaultHTTPTimeout bounds one delivery request.
	DefaultHTTPTimeout = 10 * time.Second
	maxResponseBytes   = 64 << 10
	maxErrorBytes      = 256
)

// deliveryError is a delivery failure whose message is safe to store and log:
// it never contains URLs, tokens, secrets or payloads.
type deliveryError struct {
	msg string
	// permanent failures are not retried.
	permanent bool
}

func (e *deliveryError) Error() string { return e.msg }

func failure(permanent bool, format string, args ...any) error {
	return &deliveryError{msg: truncateBytes(fmt.Sprintf(format, args...), maxErrorBytes), permanent: permanent}
}

// isPermanent reports whether a delivery error must not be retried.
func isPermanent(err error) bool {
	var de *deliveryError
	return errors.As(err, &de) && de.permanent
}

// errorSummary returns the storable message of a delivery error.
func errorSummary(err error) string {
	if err == nil {
		return ""
	}
	var de *deliveryError
	if errors.As(err, &de) {
		return de.msg
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "delivery canceled or timed out"
	}
	return "delivery failed"
}

// newHTTPClient returns the shared delivery client: bounded timeout, no
// redirects (a redirect response is reported as a failure) and no cookie jar.
func newHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultHTTPTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 4
	transport.ResponseHeaderTimeout = timeout
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// sender delivers messages over HTTP.
type sender struct {
	client *http.Client
	now    func() time.Time
}

// send delivers msg through a channel of the given kind.
func (s *sender) send(ctx context.Context, kind string, cfg channelConfig, msg message) error {
	switch kind {
	case ChannelWebhook:
		return s.sendWebhook(ctx, cfg, msg)
	case ChannelFeishu:
		return s.sendFeishu(ctx, cfg, msg)
	case ChannelDingTalk:
		return s.sendDingTalk(ctx, cfg, msg)
	case ChannelWeCom:
		return s.sendWeCom(ctx, cfg, msg)
	case ChannelTelegram:
		return s.sendTelegram(ctx, cfg, msg)
	default:
		return failure(true, "unsupported channel kind %q", kind)
	}
}

func (s *sender) sendWebhook(ctx context.Context, cfg channelConfig, msg message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return failure(true, "encode payload: %v", err)
	}
	headers := make(map[string]string, len(cfg.Headers)+2)
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	if cfg.Secret != "" {
		ts := strconv.FormatInt(s.now().Unix(), 10)
		headers[HeaderTimestamp] = ts
		headers[HeaderSignature] = WebhookSignature(cfg.Secret, ts, body)
	}
	_, err = s.post(ctx, cfg.URL, headers, body)
	return err
}

// providerResult is the union of the error fields of chat provider responses.
type providerResult struct {
	Code          *int64 `json:"code"`
	Msg           string `json:"msg"`
	StatusCode    *int64 `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
	ErrCode       *int64 `json:"errcode"`
	ErrMsg        string `json:"errmsg"`
	OK            *bool  `json:"ok"`
	Description   string `json:"description"`
	ErrorCode     *int64 `json:"error_code"`
}

func decodeProviderResult(provider string, body []byte) (providerResult, error) {
	var r providerResult
	if len(bytes.TrimSpace(body)) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, failure(false, "%s: unexpected response body", provider)
	}
	return r, nil
}

func (s *sender) sendFeishu(ctx context.Context, cfg channelConfig, msg message) error {
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": renderText(msg)},
	}
	if cfg.Secret != "" {
		ts := s.now().Unix()
		payload["timestamp"] = strconv.FormatInt(ts, 10)
		payload["sign"] = feishuSign(cfg.Secret, ts)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return failure(true, "encode payload: %v", err)
	}
	resp, err := s.post(ctx, cfg.WebhookURL, nil, body)
	if err != nil {
		return err
	}
	r, err := decodeProviderResult("feishu", resp)
	if err != nil {
		return err
	}
	if r.Code != nil && *r.Code != 0 {
		return failure(false, "feishu: code %d: %s", *r.Code, providerText(r.Msg, cfg))
	}
	if r.StatusCode != nil && *r.StatusCode != 0 {
		return failure(false, "feishu: status code %d: %s", *r.StatusCode, providerText(r.StatusMessage, cfg))
	}
	return nil
}

func (s *sender) sendDingTalk(ctx context.Context, cfg channelConfig, msg message) error {
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"title": ellipsis(msg.headline(), 128),
			"text":  renderMarkdown(msg),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return failure(true, "encode payload: %v", err)
	}
	target, err := dingTalkTarget(cfg.WebhookURL, cfg.Secret, s.now().UnixMilli())
	if err != nil {
		return err
	}
	resp, err := s.post(ctx, target, nil, body)
	if err != nil {
		return err
	}
	return errCodeResult("dingtalk", resp, cfg)
}

// dingTalkTarget appends the signature parameters of a signed DingTalk robot
// to its webhook URL (keeping existing query parameters such as access_token).
func dingTalkTarget(webhookURL, secret string, timestampMs int64) (string, error) {
	if secret == "" {
		return webhookURL, nil
	}
	u, err := url.Parse(webhookURL)
	if err != nil {
		return "", failure(true, "build request: invalid target URL")
	}
	signature := "timestamp=" + strconv.FormatInt(timestampMs, 10) + "&sign=" + url.QueryEscape(dingTalkSign(secret, timestampMs))
	if u.RawQuery == "" {
		u.RawQuery = signature
	} else {
		u.RawQuery += "&" + signature
	}
	return u.String(), nil
}

func (s *sender) sendWeCom(ctx context.Context, cfg channelConfig, msg message) error {
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": renderMarkdown(msg)},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return failure(true, "encode payload: %v", err)
	}
	resp, err := s.post(ctx, cfg.WebhookURL, nil, body)
	if err != nil {
		return err
	}
	return errCodeResult("wecom", resp, cfg)
}

// errCodeResult checks DingTalk/WeCom style {"errcode":0,"errmsg":"ok"} responses.
func errCodeResult(provider string, resp []byte, cfg channelConfig) error {
	r, err := decodeProviderResult(provider, resp)
	if err != nil {
		return err
	}
	if r.ErrCode != nil && *r.ErrCode != 0 {
		return failure(false, "%s: errcode %d: %s", provider, *r.ErrCode, providerText(r.ErrMsg, cfg))
	}
	return nil
}

func (s *sender) sendTelegram(ctx context.Context, cfg channelConfig, msg message) error {
	base := cfg.APIBase
	if base == "" {
		base = DefaultTelegramAPIBase
	}
	payload := map[string]any{
		"chat_id":                  cfg.ChatID,
		"text":                     renderTelegramHTML(msg),
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return failure(true, "encode payload: %v", err)
	}
	status, resp, err := s.postRaw(ctx, base+"/bot"+cfg.BotToken+"/sendMessage", nil, body)
	if err != nil {
		return err
	}
	r, decodeErr := decodeProviderResult("telegram", resp)
	if r.OK != nil && !*r.OK {
		code := int64(status)
		if r.ErrorCode != nil {
			code = *r.ErrorCode
		}
		return failure(code >= 400 && code < 500 && code != 429, "telegram: error %d: %s",
			code, providerText(r.Description, cfg))
	}
	if status < 200 || status > 299 {
		return statusFailure("telegram", status)
	}
	if decodeErr != nil {
		return decodeErr
	}
	if r.OK == nil {
		return failure(false, "telegram: unexpected response body")
	}
	return nil
}

// post sends a JSON POST and treats every non-2xx status as a failure.
func (s *sender) post(ctx context.Context, target string, headers map[string]string, body []byte) ([]byte, error) {
	status, resp, err := s.postRaw(ctx, target, headers, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, statusFailure("http", status)
	}
	return resp, nil
}

// postRaw sends a JSON POST and returns the status and a bounded body. Errors
// never include the target URL, which may embed credentials.
func (s *sender) postRaw(ctx context.Context, target string, headers map[string]string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, failure(true, "build request: invalid target URL")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "Spinneret-Notify/"+version.String())
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, transportFailure(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, nil, failure(false, "read response: %s", sanitizeNetError(err))
	}
	// Drain a bounded remainder so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	return resp.StatusCode, data, nil
}

func statusFailure(provider string, status int) error {
	permanent := status >= 300 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
	return failure(permanent, "%s: unexpected status %d", provider, status)
}

// transportFailure converts a client error into a safe delivery error.
func transportFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return failure(false, "request failed: %s", sanitizeNetError(err))
}

// sanitizeNetError describes a transport error without the request URL.
func sanitizeNetError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns lookup failed"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "connection failed (" + opErr.Op + ")"
	}
	return "transport error"
}

// providerText bounds and flattens a provider supplied error message and
// removes the channel's credentials from it: delivery errors are stored and
// shown to callers who may not see the channel settings.
func providerText(s string, cfg channelConfig) string {
	s = strings.TrimSpace(validUTF8(s))
	for _, credential := range credentialsOf(cfg) {
		s = strings.ReplaceAll(s, credential, identity.MaskPrefix)
	}
	s = oneLine(s)
	if s == "" {
		return "no message"
	}
	return ellipsis(s, 160)
}

// Minimum lengths of values treated as credentials by credentialsOf.
const (
	minCredentialBytes  = 6
	minPathSegmentBytes = 16
)

// credentialsOf lists the values of a channel config that may grant access
// and must never appear in stored errors: secrets, bot tokens, credential
// header values, and the passwords, query parameter values and long path
// segments of the destination URLs (webhook URLs commonly embed access
// tokens). Longer values come first so that overlapping values are replaced
// completely.
func credentialsOf(cfg channelConfig) []string {
	var out []string
	add := func(v string, minBytes int) {
		if len(v) >= minBytes {
			out = append(out, v)
		}
	}
	add(cfg.Secret, minCredentialBytes)
	add(cfg.BotToken, minCredentialBytes)
	for name, v := range cfg.Headers {
		if isSensitiveHeader(name) {
			add(v, minCredentialBytes)
		}
	}
	for _, raw := range []string{cfg.URL, cfg.WebhookURL, cfg.APIBase} {
		u, err := url.Parse(raw)
		if raw == "" || err != nil {
			continue
		}
		if password, ok := u.User.Password(); ok {
			add(password, minCredentialBytes)
		}
		for _, values := range u.Query() {
			for _, v := range values {
				add(v, minCredentialBytes)
			}
		}
		for _, segment := range strings.Split(u.Path, "/") {
			add(segment, minPathSegmentBytes)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}
