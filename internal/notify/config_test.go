package notify

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
)

func TestParseConfig(t *testing.T) {
	t.Parallel()
	manyHeaders := map[string]any{}
	for i := range maxHeaders + 1 {
		manyHeaders["X-H"+strings.Repeat("a", i)] = "v"
	}
	tests := []struct {
		name    string
		kind    string
		raw     map[string]any
		want    channelConfig
		wantErr string
	}{
		{
			name: "webhook full",
			kind: ChannelWebhook,
			raw: map[string]any{"url": " https://hooks.example.com/a?b=c ", "secret": "s3cr3t",
				"headers": map[string]any{"x-api-key": "k", "X-Team": "ops"}},
			want: channelConfig{URL: "https://hooks.example.com/a?b=c", Secret: "s3cr3t",
				Headers: map[string]string{"X-Api-Key": "k", "X-Team": "ops"}},
		},
		{name: "webhook minimal", kind: ChannelWebhook, raw: map[string]any{"url": "http://10.0.0.1:8080/hook"},
			want: channelConfig{URL: "http://10.0.0.1:8080/hook"}},
		{name: "webhook missing url", kind: ChannelWebhook, raw: map[string]any{}, wantErr: "url is required"},
		{name: "webhook bad scheme", kind: ChannelWebhook, raw: map[string]any{"url": "ftp://x/y"}, wantErr: "absolute http or https"},
		{name: "webhook relative url", kind: ChannelWebhook, raw: map[string]any{"url": "/hook"}, wantErr: "absolute http or https"},
		{name: "webhook url not string", kind: ChannelWebhook, raw: map[string]any{"url": 3.0}, wantErr: "must be a string"},
		{name: "webhook masked url", kind: ChannelWebhook, raw: map[string]any{"url": "https://x/••••abcd"}, wantErr: "masked"},
		{name: "webhook masked secret", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "secret": "••••abcd"}, wantErr: "masked"},
		{name: "webhook too many headers", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": manyHeaders}, wantErr: "at most 20 headers"},
		{name: "webhook reserved header", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": map[string]any{"content-type": "x"}}, wantErr: "cannot be overridden"},
		{name: "webhook signature header", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": map[string]any{"X-Spinneret-Signature": "x"}}, wantErr: "cannot be overridden"},
		{name: "webhook duplicate header", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": map[string]any{"x-a": "1", "X-A": "2"}}, wantErr: "duplicated"},
		{name: "webhook invalid header name", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": map[string]any{"bad name": "1"}}, wantErr: "invalid"},
		{name: "webhook header crlf", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": map[string]any{"X-A": "a\r\nb"}}, wantErr: "invalid value"},
		{name: "webhook header not string", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": map[string]any{"X-A": 1.0}}, wantErr: "must be a string"},
		{name: "webhook headers not object", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": "x"}, wantErr: "object"},
		{name: "webhook masked auth header", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "headers": map[string]any{"Authorization": "••••abcd"}}, wantErr: "masked"},
		{name: "unknown field", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "webhook_url": "https://x"}, wantErr: "unknown field"},
		{name: "nil config", kind: ChannelWebhook, raw: nil, wantErr: "required"},
		{name: "unsupported kind", kind: "pager", raw: map[string]any{}, wantErr: "unsupported channel kind"},
		{name: "oversized", kind: ChannelWebhook, raw: map[string]any{"url": "https://x", "secret": strings.Repeat("a", maxConfigBytes)}, wantErr: "at most"},
		{name: "feishu", kind: ChannelFeishu, raw: map[string]any{"webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/abc", "secret": "sec"},
			want: channelConfig{WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc", Secret: "sec"}},
		{name: "dingtalk no secret", kind: ChannelDingTalk, raw: map[string]any{"webhook_url": "https://oapi.dingtalk.com/robot/send?access_token=x", "secret": nil},
			want: channelConfig{WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=x"}},
		{name: "wecom rejects secret", kind: ChannelWeCom, raw: map[string]any{"webhook_url": "https://qyapi.weixin.qq.com/x", "secret": "s"}, wantErr: "unknown field"},
		{name: "wecom", kind: ChannelWeCom, raw: map[string]any{"webhook_url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k"},
			want: channelConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k"}},
		{name: "secret with newline", kind: ChannelFeishu, raw: map[string]any{"webhook_url": "https://x", "secret": "a\nb"}, wantErr: "invalid characters"},
		{name: "telegram numeric chat", kind: ChannelTelegram, raw: map[string]any{"bot_token": "123456:ABC-def_1", "chat_id": -1001234567890.0},
			want: channelConfig{BotToken: "123456:ABC-def_1", ChatID: "-1001234567890"}},
		{name: "telegram api base", kind: ChannelTelegram, raw: map[string]any{"bot_token": "1:a", "chat_id": "@alerts", "api_base": "https://tg.example.com/"},
			want: channelConfig{BotToken: "1:a", ChatID: "@alerts", APIBase: "https://tg.example.com"}},
		{name: "telegram fractional chat", kind: ChannelTelegram, raw: map[string]any{"bot_token": "1:a", "chat_id": 1.5}, wantErr: "integer or a string"},
		{name: "telegram bool chat", kind: ChannelTelegram, raw: map[string]any{"bot_token": "1:a", "chat_id": true}, wantErr: "integer or a string"},
		{name: "telegram empty chat", kind: ChannelTelegram, raw: map[string]any{"bot_token": "1:a", "chat_id": " "}, wantErr: "1..128 bytes"},
		{name: "telegram missing chat", kind: ChannelTelegram, raw: map[string]any{"bot_token": "1:a"}, wantErr: "chat_id is required"},
		{name: "telegram missing token", kind: ChannelTelegram, raw: map[string]any{"chat_id": "1"}, wantErr: "bot_token is required"},
		{name: "telegram bad token", kind: ChannelTelegram, raw: map[string]any{"bot_token": "abc/def", "chat_id": "1"}, wantErr: "must look like"},
		{name: "telegram bad api base", kind: ChannelTelegram, raw: map[string]any{"bot_token": "1:a", "chat_id": "1", "api_base": "tg"}, wantErr: "absolute http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseConfig(tt.kind, tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestConfigErrorsDoNotLeakValues(t *testing.T) {
	t.Parallel()
	_, err := parseConfig(ChannelWebhook, map[string]any{"url": "ftp://user:hunter2@example.com"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2")
}

func TestMaskedConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind string
		cfg  channelConfig
		want map[string]any
	}{
		{
			name: "webhook",
			kind: ChannelWebhook,
			cfg: channelConfig{URL: "https://hooks.example.com/services/T0/B0/XYZ12345", Secret: "supersecret",
				Headers: map[string]string{"Authorization": "Bearer abcdefgh", "X-Api-Key": "key-9876", "X-Team": "ops"}},
			want: map[string]any{
				"url":    "https://hooks.example.com/••••2345",
				"secret": "••••cret",
				"headers": map[string]any{
					"Authorization": "••••efgh", "X-Api-Key": "••••9876", "X-Team": "ops",
				},
			},
		},
		{
			name: "webhook host only",
			kind: ChannelWebhook,
			cfg:  channelConfig{URL: "https://hooks.example.com"},
			want: map[string]any{"url": "https://hooks.example.com/"},
		},
		{
			name: "dingtalk",
			kind: ChannelDingTalk,
			cfg:  channelConfig{WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=abcdef123456", Secret: "SEC1"},
			want: map[string]any{"webhook_url": "https://oapi.dingtalk.com/••••3456", "secret": "••••"},
		},
		{
			name: "wecom",
			kind: ChannelWeCom,
			cfg:  channelConfig{WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=k-1234"},
			want: map[string]any{"webhook_url": "https://qyapi.weixin.qq.com/••••1234"},
		},
		{
			name: "telegram",
			kind: ChannelTelegram,
			cfg:  channelConfig{BotToken: "123456:ABCDEFG", ChatID: "-100", APIBase: "https://tg.example.com"},
			want: map[string]any{"bot_token": "••••DEFG", "chat_id": "-100", "api_base": "https://tg.example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.cfg.masked(tt.kind))
		})
	}
	require.Equal(t, "••••", maskURL("abc"))
}

func TestMergeMasked(t *testing.T) {
	t.Parallel()
	stored := channelConfig{
		URL: "https://hooks.example.com/services/T0/B0/XYZ12345", Secret: "supersecret",
		Headers: map[string]string{"Authorization": "Bearer abcdefgh", "X-Team": "ops"},
	}
	masked := stored.masked(ChannelWebhook)

	t.Run("round trip keeps secrets", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig(ChannelWebhook, mergeMasked(ChannelWebhook, stored, masked))
		require.NoError(t, err)
		require.Equal(t, stored, cfg)
	})
	t.Run("changed values replace secrets and removed fields disappear", func(t *testing.T) {
		t.Parallel()
		raw := map[string]any{
			"url":     masked["url"],
			"headers": map[string]any{"authorization": "Bearer new-token", "X-Team": "sre"},
		}
		cfg, err := parseConfig(ChannelWebhook, mergeMasked(ChannelWebhook, stored, raw))
		require.NoError(t, err)
		require.Equal(t, channelConfig{
			URL:     stored.URL,
			Headers: map[string]string{"Authorization": "Bearer new-token", "X-Team": "sre"},
		}, cfg)
	})
	t.Run("mismatched mask is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := parseConfig(ChannelWebhook, mergeMasked(ChannelWebhook, stored, map[string]any{
			"url": stored.URL, "secret": "••••zzzz",
		}))
		require.ErrorContains(t, err, "masked")
	})
	t.Run("im kinds", func(t *testing.T) {
		t.Parallel()
		im := channelConfig{WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/0123456789", Secret: "feishu-secret"}
		cfg, err := parseConfig(ChannelFeishu, mergeMasked(ChannelFeishu, im, im.masked(ChannelFeishu)))
		require.NoError(t, err)
		require.Equal(t, im, cfg)
	})
	t.Run("telegram", func(t *testing.T) {
		t.Parallel()
		tg := channelConfig{BotToken: "123456:ABCDEFG", ChatID: "42"}
		cfg, err := parseConfig(ChannelTelegram, mergeMasked(ChannelTelegram, tg, tg.masked(ChannelTelegram)))
		require.NoError(t, err)
		require.Equal(t, tg, cfg)
	})
	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, mergeMasked(ChannelWebhook, stored, nil))
	})
}

// Credentials sent verbatim to the destination must not follow a changed
// destination, otherwise a caller who cannot see them could redirect them.
func TestMergeMaskedDestinationChange(t *testing.T) {
	t.Parallel()
	hook := channelConfig{
		URL: "https://hooks.example.com/services/T0/B0/XYZ12345", Secret: "supersecret",
		Headers: map[string]string{"Authorization": "Bearer abcdefgh", "X-Team": "ops"},
	}
	tg := channelConfig{BotToken: "123456:ABCDEFG", ChatID: "42"}
	tgBase := channelConfig{BotToken: "123456:ABCDEFG", ChatID: "42", APIBase: "https://user:p4ssw0rd@tg.example.com"}
	tests := []struct {
		name    string
		kind    string
		stored  channelConfig
		raw     func(masked map[string]any) map[string]any
		want    channelConfig
		wantErr string
	}{
		{
			name: "webhook new url drops masked credential headers", kind: ChannelWebhook, stored: hook,
			raw: func(m map[string]any) map[string]any {
				return map[string]any{"url": "https://attacker.example.net/collect", "secret": m["secret"], "headers": m["headers"]}
			},
			wantErr: `header "Authorization" is masked`,
		},
		{
			name: "webhook new url keeps the signing secret and plain headers", kind: ChannelWebhook, stored: hook,
			raw: func(m map[string]any) map[string]any {
				return map[string]any{"url": "https://hooks.example.com/other", "secret": m["secret"],
					"headers": map[string]any{"Authorization": "Bearer new", "X-Team": "ops"}}
			},
			want: channelConfig{URL: "https://hooks.example.com/other", Secret: "supersecret",
				Headers: map[string]string{"Authorization": "Bearer new", "X-Team": "ops"}},
		},
		{
			name: "webhook same url with spaces keeps credential headers", kind: ChannelWebhook, stored: hook,
			raw: func(m map[string]any) map[string]any {
				return map[string]any{"url": " " + hook.URL + " ", "headers": m["headers"]}
			},
			want: channelConfig{URL: hook.URL, Headers: hook.Headers},
		},
		{
			name: "telegram new api base drops masked bot token", kind: ChannelTelegram, stored: tg,
			raw: func(m map[string]any) map[string]any {
				return map[string]any{"bot_token": m["bot_token"], "chat_id": "42", "api_base": "https://attacker.example.net"}
			},
			wantErr: "bot_token is masked",
		},
		{
			name: "telegram explicit default api base keeps bot token", kind: ChannelTelegram, stored: tg,
			raw: func(m map[string]any) map[string]any {
				return map[string]any{"bot_token": m["bot_token"], "chat_id": "42", "api_base": DefaultTelegramAPIBase + "/"}
			},
			want: channelConfig{BotToken: tg.BotToken, ChatID: "42", APIBase: DefaultTelegramAPIBase},
		},
		{
			name: "telegram api base with credentials round trips masked", kind: ChannelTelegram, stored: tgBase,
			raw:  func(m map[string]any) map[string]any { return m },
			want: tgBase,
		},
		{
			name: "telegram other masked api base is rejected", kind: ChannelTelegram, stored: tgBase,
			raw: func(m map[string]any) map[string]any {
				return map[string]any{"bot_token": "123456:NEWTOKEN", "chat_id": "42", "api_base": "https://tg.example.com/••••zzzz"}
			},
			wantErr: "api_base is masked",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseConfig(tt.kind, mergeMasked(tt.kind, tt.stored, tt.raw(tt.stored.masked(tt.kind))))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	masked := tgBase.masked(ChannelTelegram)
	require.NotContains(t, masked["api_base"], "p4ssw0rd")
	require.NotContains(t, masked["api_base"], "user")
	require.Equal(t, "https://tg.example.com", displayAPIBase("https://tg.example.com"))
}

func TestNormalizeFilters(t *testing.T) {
	t.Parallel()
	kinds, sites, sev, err := normalizeFilters("ns_1", []string{KindBanSpike, KindTest, KindBanSpike}, []string{"b", "a", "b"}, "")
	require.NoError(t, err)
	require.Equal(t, []string{KindBanSpike, KindTest}, kinds)
	require.Equal(t, []string{"a", "b"}, sites)
	require.Equal(t, SeverityWarning, sev)

	_, _, _, err = normalizeFilters("ns_1", nil, nil, "")
	require.ErrorContains(t, err, "at least one")
	_, _, _, err = normalizeFilters("ns_1", []string{"nope"}, nil, "")
	require.ErrorContains(t, err, "unknown alert kind")
	_, _, _, err = normalizeFilters("", []string{KindTest}, []string{"a"}, "")
	require.ErrorContains(t, err, "namespace-bound")
	_, _, _, err = normalizeFilters("ns", []string{KindTest}, []string{""}, "")
	require.ErrorContains(t, err, "must not be empty")
	_, _, _, err = normalizeFilters("ns", []string{KindTest}, make([]string, MaxChannelSites+1), "")
	require.ErrorContains(t, err, "at most")
	_, _, _, err = normalizeFilters("ns", []string{KindTest}, nil, "fatal")
	require.ErrorContains(t, err, "min_severity")
}

func TestValidators(t *testing.T) {
	t.Parallel()
	require.Len(t, Kinds(), 12)
	require.True(t, ValidKind(KindReportBacklog))
	require.False(t, ValidKind("x"))
	require.True(t, ValidSeverity(SeverityCritical))
	require.False(t, ValidSeverity(""))
	require.True(t, ValidChannelKind(ChannelTelegram))
	require.False(t, ValidChannelKind("pager"))
	require.NoError(t, validateChannelName("ops alerts"))
	for _, bad := range []string{"", " x", "x ", "a\nb", strings.Repeat("x", MaxChannelNameLen+1)} {
		require.Error(t, validateChannelName(bad), bad)
	}
	require.Equal(t, "ab", truncateBytes("ab€", 4))
	require.Equal(t, 50, clampLimit(0))
	require.Equal(t, 500, clampLimit(1000))
	require.Equal(t, 7, clampLimit(7))
}
