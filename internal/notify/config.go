package notify

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/identity"
)

// Channel config limits.
const (
	maxConfigBytes      = 16 << 10
	maxURLBytes         = 2048
	maxSecretBytes      = 1024
	maxHeaders          = 20
	maxHeaderNameBytes  = 128
	maxHeaderValueBytes = 4096
	maxChatIDBytes      = 128
	maxBotTokenBytes    = 256
	// DefaultTelegramAPIBase is used when a telegram channel sets no api_base.
	DefaultTelegramAPIBase = "https://api.telegram.org"
)

// Config field names.
const (
	fieldURL        = "url"
	fieldSecret     = "secret"
	fieldHeaders    = "headers"
	fieldWebhookURL = "webhook_url"
	fieldBotToken   = "bot_token"
	fieldChatID     = "chat_id"
	fieldAPIBase    = "api_base"
)

var (
	allowedConfigFields = map[string][]string{
		ChannelWebhook:  {fieldURL, fieldSecret, fieldHeaders},
		ChannelFeishu:   {fieldWebhookURL, fieldSecret},
		ChannelDingTalk: {fieldWebhookURL, fieldSecret},
		ChannelWeCom:    {fieldWebhookURL},
		ChannelTelegram: {fieldBotToken, fieldChatID, fieldAPIBase},
	}
	headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
	botTokenPattern   = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{1,200}$`)
	// reservedHeaders cannot be set by channel configs.
	reservedHeaders = map[string]bool{
		"Host": true, "Content-Length": true, "Content-Type": true, "Transfer-Encoding": true,
		"Connection": true, "Te": true, "Upgrade": true, "Trailer": true,
		"X-Spinneret-Timestamp": true, "X-Spinneret-Signature": true,
	}
	// sensitiveHeaderWords mark headers whose values are masked on read.
	sensitiveHeaderWords = []string{"authorization", "token", "key", "secret", "password", "cookie", "signature"}
)

// channelConfig is the validated, kind-specific channel configuration. Only
// the fields allowed for the kind are set. It is stored sealed as JSON.
type channelConfig struct {
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	WebhookURL string            `json:"webhook_url,omitempty"`
	Secret     string            `json:"secret,omitempty"`
	BotToken   string            `json:"bot_token,omitempty"`
	ChatID     string            `json:"chat_id,omitempty"`
	APIBase    string            `json:"api_base,omitempty"`
}

// configError builds an invalid-argument error for a config field. Messages
// never contain the offending value.
func configError(format string, args ...any) error {
	return apperr.InvalidArgument("", "config: "+format, args...)
}

// maskedError reports a masked value that cannot be restored: it does not
// match the stored value, or it is a credential whose destination changed.
func maskedError(field string) error {
	return configError("%s is masked but does not match the stored value (credentials must be sent in full when the destination changes); send the full value", field)
}

// parseConfig validates raw settings of a channel of the given kind.
func parseConfig(kind string, raw map[string]any) (channelConfig, error) {
	allowed, ok := allowedConfigFields[kind]
	if !ok {
		return channelConfig{}, apperr.InvalidArgument("", "unsupported channel kind %q", kind)
	}
	if raw == nil {
		return channelConfig{}, configError("settings are required")
	}
	if b, err := json.Marshal(raw); err != nil || len(b) > maxConfigBytes {
		return channelConfig{}, configError("settings must be a JSON object of at most %d bytes", maxConfigBytes)
	}
	for name := range raw {
		if !contains(allowed, name) {
			return channelConfig{}, configError("unknown field %q for %s channels (allowed: %s)", truncateBytes(name, 64), kind, strings.Join(allowed, ", "))
		}
	}
	var (
		cfg channelConfig
		err error
	)
	switch kind {
	case ChannelWebhook:
		if cfg.URL, err = urlField(raw, fieldURL, true); err != nil {
			return channelConfig{}, err
		}
		if cfg.Secret, err = secretField(raw, fieldSecret); err != nil {
			return channelConfig{}, err
		}
		if cfg.Headers, err = headersField(raw); err != nil {
			return channelConfig{}, err
		}
	case ChannelFeishu, ChannelDingTalk, ChannelWeCom:
		if cfg.WebhookURL, err = urlField(raw, fieldWebhookURL, true); err != nil {
			return channelConfig{}, err
		}
		if kind != ChannelWeCom {
			if cfg.Secret, err = secretField(raw, fieldSecret); err != nil {
				return channelConfig{}, err
			}
		}
	case ChannelTelegram:
		if cfg, err = parseTelegramConfig(raw); err != nil {
			return channelConfig{}, err
		}
	}
	return cfg, nil
}

func parseTelegramConfig(raw map[string]any) (channelConfig, error) {
	var cfg channelConfig
	token, err := stringField(raw, fieldBotToken, maxBotTokenBytes)
	if err != nil {
		return cfg, err
	}
	if token == "" {
		return cfg, configError("%s is required", fieldBotToken)
	}
	if strings.HasPrefix(token, identity.MaskPrefix) {
		return cfg, maskedError(fieldBotToken)
	}
	if !botTokenPattern.MatchString(token) {
		return cfg, configError("%s must look like <bot id>:<secret>", fieldBotToken)
	}
	cfg.BotToken = token
	if cfg.ChatID, err = chatIDField(raw); err != nil {
		return cfg, err
	}
	if cfg.APIBase, err = urlField(raw, fieldAPIBase, false); err != nil {
		return cfg, err
	}
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	return cfg, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// stringField returns an optional string field ("" when absent or null).
func stringField(raw map[string]any, name string, maxBytes int) (string, error) {
	v, ok := raw[name]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", configError("%s must be a string", name)
	}
	s = strings.TrimSpace(s)
	if len(s) > maxBytes {
		return "", configError("%s must be at most %d bytes", name, maxBytes)
	}
	if !utf8.ValidString(s) || strings.ContainsAny(s, "\r\n\x00") {
		return "", configError("%s contains invalid characters", name)
	}
	return s, nil
}

func secretField(raw map[string]any, name string) (string, error) {
	s, err := stringField(raw, name, maxSecretBytes)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(s, identity.MaskPrefix) {
		return "", maskedError(name)
	}
	return s, nil
}

// urlField validates an absolute http(s) URL field.
func urlField(raw map[string]any, name string, required bool) (string, error) {
	s, err := stringField(raw, name, maxURLBytes)
	if err != nil {
		return "", err
	}
	if s == "" {
		if required {
			return "", configError("%s is required", name)
		}
		return "", nil
	}
	if strings.Contains(s, identity.MaskPrefix) {
		return "", maskedError(name)
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return "", configError("%s must be an absolute http or https URL", name)
	}
	return s, nil
}

func chatIDField(raw map[string]any) (string, error) {
	v, ok := raw[fieldChatID]
	if !ok || v == nil {
		return "", configError("%s is required", fieldChatID)
	}
	var s string
	switch t := v.(type) {
	case string:
		s = strings.TrimSpace(t)
	case float64:
		if t != math.Trunc(t) || math.Abs(t) > 1<<53 {
			return "", configError("%s must be an integer or a string", fieldChatID)
		}
		s = strconv.FormatInt(int64(t), 10)
	case json.Number:
		s = t.String()
	default:
		return "", configError("%s must be an integer or a string", fieldChatID)
	}
	if s == "" || len(s) > maxChatIDBytes || strings.ContainsAny(s, "\r\n\x00") {
		return "", configError("%s must be 1..%d bytes", fieldChatID, maxChatIDBytes)
	}
	return s, nil
}

func headersField(raw map[string]any) (map[string]string, error) {
	v, ok := raw[fieldHeaders]
	if !ok || v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, configError("%s must be an object of string values", fieldHeaders)
	}
	if len(m) > maxHeaders {
		return nil, configError("at most %d headers are allowed", maxHeaders)
	}
	out := make(map[string]string, len(m))
	seen := make(map[string]bool, len(m))
	for name, val := range m {
		if len(name) > maxHeaderNameBytes || !headerNamePattern.MatchString(name) {
			return nil, configError("header name %q is invalid", truncateBytes(name, maxHeaderNameBytes))
		}
		canonical := http.CanonicalHeaderKey(name)
		if reservedHeaders[canonical] {
			return nil, configError("header %q cannot be overridden", canonical)
		}
		if seen[canonical] {
			return nil, configError("header %q is duplicated", canonical)
		}
		seen[canonical] = true
		s, ok := val.(string)
		if !ok {
			return nil, configError("header %q must be a string", canonical)
		}
		if len(s) > maxHeaderValueBytes || !utf8.ValidString(s) || strings.ContainsAny(s, "\r\n\x00") {
			return nil, configError("header %q has an invalid value", canonical)
		}
		if isSensitiveHeader(canonical) && strings.HasPrefix(s, identity.MaskPrefix) {
			return nil, maskedError(fmt.Sprintf("header %q", canonical))
		}
		out[canonical] = s
	}
	return out, nil
}

// isSensitiveHeader reports whether a header value is a credential.
func isSensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range sensitiveHeaderWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// maskURL keeps the scheme and host of a URL and masks the rest, which may
// carry access tokens (for example ".../robot/send?access_token=…").
func maskURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return identity.MaskString(s)
	}
	origin := u.Scheme + "://" + u.Host
	rest := strings.TrimPrefix(strings.TrimPrefix(s, origin), "/")
	if rest == "" {
		return origin + "/"
	}
	return origin + "/" + identity.MaskString(rest)
}

// masked returns the display form of the config.
func (c channelConfig) masked(kind string) map[string]any {
	out := map[string]any{}
	switch kind {
	case ChannelWebhook:
		out[fieldURL] = maskURL(c.URL)
		if c.Secret != "" {
			out[fieldSecret] = identity.MaskString(c.Secret)
		}
		if len(c.Headers) > 0 {
			h := make(map[string]any, len(c.Headers))
			for name, v := range c.Headers {
				if isSensitiveHeader(name) {
					v = identity.MaskString(v)
				}
				h[name] = v
			}
			out[fieldHeaders] = h
		}
	case ChannelFeishu, ChannelDingTalk, ChannelWeCom:
		out[fieldWebhookURL] = maskURL(c.WebhookURL)
		if c.Secret != "" {
			out[fieldSecret] = identity.MaskString(c.Secret)
		}
	case ChannelTelegram:
		out[fieldBotToken] = identity.MaskString(c.BotToken)
		out[fieldChatID] = c.ChatID
		if c.APIBase != "" {
			out[fieldAPIBase] = displayAPIBase(c.APIBase)
		}
	}
	return out
}

// mergeMasked returns a copy of raw in which every masked value equal to the
// masked form of the stored value is replaced by the stored value, so a config
// read from the API and sent back unchanged keeps its secrets.
//
// Credentials that are transmitted verbatim to the destination (credential
// headers of webhooks, Telegram bot tokens) are only restored while the
// destination (url, api_base) is unchanged: otherwise a caller who can edit a
// channel but cannot see its secrets could redirect them to a server of their
// choice. Such values must then be sent again in full.
func mergeMasked(kind string, stored channelConfig, raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	keep := func(field, storedValue, maskedValue string) {
		if s, ok := out[field].(string); ok && storedValue != "" && s == maskedValue {
			out[field] = storedValue
		}
	}
	switch kind {
	case ChannelWebhook:
		keep(fieldURL, stored.URL, maskURL(stored.URL))
		keep(fieldSecret, stored.Secret, identity.MaskString(stored.Secret))
		sameDestination := stored.URL != "" && trimmedString(out[fieldURL]) == stored.URL
		if h, ok := out[fieldHeaders].(map[string]any); ok {
			merged := make(map[string]any, len(h))
			for name, v := range h {
				merged[name] = v
				s, isString := v.(string)
				old, found := stored.Headers[http.CanonicalHeaderKey(name)]
				if sameDestination && isString && found && isSensitiveHeader(name) && s == identity.MaskString(old) {
					merged[name] = old
				}
			}
			out[fieldHeaders] = merged
		}
	case ChannelFeishu, ChannelDingTalk, ChannelWeCom:
		keep(fieldWebhookURL, stored.WebhookURL, maskURL(stored.WebhookURL))
		keep(fieldSecret, stored.Secret, identity.MaskString(stored.Secret))
	case ChannelTelegram:
		keep(fieldAPIBase, stored.APIBase, displayAPIBase(stored.APIBase))
		if telegramBase(trimmedString(out[fieldAPIBase])) == telegramBase(stored.APIBase) {
			keep(fieldBotToken, stored.BotToken, identity.MaskString(stored.BotToken))
		}
	}
	return out
}

// displayAPIBase returns the display form of a Telegram API base: an URL
// embedding credentials (user info) is masked like webhook URLs.
func displayAPIBase(s string) string {
	if u, err := url.Parse(s); err == nil && u.User != nil {
		return maskURL(s)
	}
	return s
}

// trimmedString returns v as a trimmed string ("" for non-strings).
func trimmedString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// telegramBase normalizes a Telegram API base for comparison.
func telegramBase(s string) string {
	s = strings.TrimRight(s, "/")
	if s == "" {
		return DefaultTelegramAPIBase
	}
	return s
}

// encode serializes the config for sealing.
func (c channelConfig) encode() ([]byte, error) {
	b, err := json.Marshal(c) //nolint:gosec // G117: the encoded config is sealed with the vault cipher before it is stored and never returned unmasked.
	if err != nil {
		return nil, fmt.Errorf("encode channel config: %w", err)
	}
	return b, nil
}

// decodeConfig parses a decrypted config.
func decodeConfig(b []byte) (channelConfig, error) {
	var c channelConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return channelConfig{}, fmt.Errorf("decode channel config: %w", err)
	}
	return c, nil
}

// normalizeEventTypes validates and de-duplicates subscribed alert kinds,
// preserving order.
func normalizeEventTypes(kinds []string) ([]string, error) {
	if len(kinds) == 0 {
		return nil, apperr.InvalidArgument("", "event_types must list at least one alert kind")
	}
	out := make([]string, 0, len(kinds))
	seen := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		if !ValidKind(k) {
			return nil, apperr.InvalidArgument("", "event_types: unknown alert kind %q", truncateBytes(k, 64))
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// normalizeSiteIDs de-duplicates and sorts site IDs.
func normalizeSiteIDs(ids []string) ([]string, error) {
	if len(ids) > MaxChannelSites {
		return nil, apperr.InvalidArgument("", "at most %d sites are allowed", MaxChannelSites)
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, apperr.InvalidArgument("", "site ids must not be empty")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// truncateBytes shortens s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
