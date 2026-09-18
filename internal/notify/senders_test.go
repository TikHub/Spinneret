package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

func testSender() *sender {
	return &sender{client: newHTTPClient(2 * time.Second), now: func() time.Time { return fixedNow }}
}

func TestSendWebhook(t *testing.T) {
	t.Parallel()
	p := newProvider(t, providerResponse{status: 204})
	s := testSender()
	cfg := channelConfig{URL: p.URL + "/hook?x=1", Secret: "whsec", Headers: map[string]string{"X-Team": "ops"}}
	require.NoError(t, s.send(context.Background(), ChannelWebhook, cfg, sampleMessage()))

	reqs := p.received()
	require.Len(t, reqs, 1)
	r := reqs[0]
	require.Equal(t, "/hook", r.Path)
	require.Equal(t, "x=1", r.Query)
	require.Equal(t, "ops", r.Headers.Get("X-Team"))
	require.Equal(t, "application/json; charset=utf-8", r.Headers.Get("Content-Type"))
	ts := r.Headers.Get(HeaderTimestamp)
	require.Equal(t, strconv.FormatInt(fixedNow.Unix(), 10), ts)
	require.Equal(t, WebhookSignature("whsec", ts, r.Body), r.Headers.Get(HeaderSignature))

	body := decodeJSON(t, r.Body)
	for _, field := range []string{"id", "kind", "severity", "title", "message", "tenant", "namespace", "site", "details", "created_at"} {
		require.Contains(t, body, field)
	}
	require.Equal(t, "alt_1", body["id"])
	require.Equal(t, "shop", body["site"])
	require.Equal(t, "2026-09-17T10:00:00Z", body["created_at"])

	// Without a secret no signature headers are sent.
	require.NoError(t, s.send(context.Background(), ChannelWebhook, channelConfig{URL: p.URL}, sampleMessage()))
	require.Empty(t, p.received()[1].Headers.Get(HeaderSignature))
}

func TestSendWebhookFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    int
		permanent bool
	}{
		{name: "server error", status: 500},
		{name: "rate limited", status: 429},
		{name: "timeout status", status: 408},
		{name: "not found", status: 404, permanent: true},
		{name: "redirect is not followed", status: 302, permanent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProvider(t, providerResponse{status: tt.status, body: `{"error":"x"}`})
			err := testSender().send(context.Background(), ChannelWebhook, channelConfig{URL: p.URL + "/secret-path"}, sampleMessage())
			require.Error(t, err)
			require.Equal(t, tt.permanent, isPermanent(err))
			require.Contains(t, errorSummary(err), strconv.Itoa(tt.status))
			require.NotContains(t, errorSummary(err), "secret-path")
			require.Len(t, p.received(), 1)
		})
	}
}

func TestSendTransportErrorsHideURL(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	target := srv.URL + "/robot/send?access_token=topsecret"
	srv.Close()
	err := testSender().send(context.Background(), ChannelDingTalk, channelConfig{WebhookURL: target}, sampleMessage())
	require.Error(t, err)
	require.False(t, isPermanent(err))
	require.NotContains(t, err.Error(), "topsecret")
	require.NotContains(t, err.Error(), srv.URL)

	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Reading the body lets the server notice the client hanging up.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	s := &sender{client: newHTTPClient(50 * time.Millisecond), now: time.Now}
	err = s.send(context.Background(), ChannelWebhook, channelConfig{URL: slow.URL + "/?token=topsecret"}, sampleMessage())
	require.Error(t, err)
	require.Equal(t, "request failed: timeout", errorSummary(err))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = testSender().send(ctx, ChannelWebhook, channelConfig{URL: slow.URL}, sampleMessage())
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "delivery canceled or timed out", errorSummary(err))

	err = testSender().send(context.Background(), ChannelWebhook, channelConfig{URL: "http://[::1"}, sampleMessage())
	require.True(t, isPermanent(err))
	err = testSender().send(context.Background(), "pager", channelConfig{}, sampleMessage())
	require.True(t, isPermanent(err))
	require.Equal(t, "delivery failed", errorSummary(errors.New("boom")))
	require.Empty(t, errorSummary(nil))
}

func TestSendFeishu(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		resp    providerResponse
		wantErr string
	}{
		{name: "ok", resp: providerResponse{status: 200, body: `{"code":0,"msg":"success","data":{}}`}},
		{name: "legacy ok", resp: providerResponse{status: 200, body: `{"StatusCode":0,"StatusMessage":"success"}`}},
		{name: "sign mismatch", resp: providerResponse{status: 200, body: `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`},
			wantErr: "feishu: code 19021: sign match fail"},
		{name: "legacy error", resp: providerResponse{status: 200, body: `{"StatusCode":9499,"StatusMessage":"Bad Request"}`},
			wantErr: "feishu: status code 9499"},
		{name: "invalid body", resp: providerResponse{status: 200, body: `<html>`}, wantErr: "unexpected response body"},
		{name: "http error", resp: providerResponse{status: 502, body: ``}, wantErr: "unexpected status 502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProvider(t, tt.resp)
			err := testSender().send(context.Background(), ChannelFeishu,
				channelConfig{WebhookURL: p.URL + "/open-apis/bot/v2/hook/abc", Secret: "feishu-secret"}, sampleMessage())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			body := decodeJSON(t, p.received()[0].Body)
			require.Equal(t, "text", body["msg_type"])
			require.Contains(t, body["content"].(map[string]any)["text"], "[CRITICAL] Breaker opened")
			require.Equal(t, strconv.FormatInt(fixedNow.Unix(), 10), body["timestamp"])
			require.Equal(t, feishuSign("feishu-secret", fixedNow.Unix()), body["sign"])
		})
	}
	t.Run("no secret", func(t *testing.T) {
		t.Parallel()
		p := newProvider(t, providerResponse{status: 200, body: `{"code":0}`})
		require.NoError(t, testSender().send(context.Background(), ChannelFeishu, channelConfig{WebhookURL: p.URL}, sampleMessage()))
		body := decodeJSON(t, p.received()[0].Body)
		require.NotContains(t, body, "sign")
		require.NotContains(t, body, "timestamp")
	})
}

func TestSendDingTalk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		resp    providerResponse
		wantErr string
	}{
		{name: "ok", resp: providerResponse{status: 200, body: `{"errcode":0,"errmsg":"ok"}`}},
		{name: "keyword mismatch", resp: providerResponse{status: 200, body: `{"errcode":310000,"errmsg":"keywords not in content"}`},
			wantErr: "dingtalk: errcode 310000: keywords not in content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProvider(t, tt.resp)
			err := testSender().send(context.Background(), ChannelDingTalk,
				channelConfig{WebhookURL: p.URL + "/robot/send?access_token=tok", Secret: "SECabc"}, sampleMessage())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			r := p.received()[0]
			q, err := url.ParseQuery(r.Query)
			require.NoError(t, err)
			ts := strconv.FormatInt(fixedNow.UnixMilli(), 10)
			require.Equal(t, "tok", q.Get("access_token"))
			require.Equal(t, ts, q.Get("timestamp"))
			require.Equal(t, dingTalkSign("SECabc", fixedNow.UnixMilli()), q.Get("sign"))
			body := decodeJSON(t, r.Body)
			require.Equal(t, "markdown", body["msgtype"])
			md := body["markdown"].(map[string]any)
			require.Equal(t, "[CRITICAL] Breaker opened: shop/web/search", md["title"])
			require.Contains(t, md["text"], "### [CRITICAL]")
		})
	}
	t.Run("no secret and no query", func(t *testing.T) {
		t.Parallel()
		p := newProvider(t, providerResponse{status: 200, body: `{"errcode":0}`})
		require.NoError(t, testSender().send(context.Background(), ChannelDingTalk, channelConfig{WebhookURL: p.URL + "/send"}, sampleMessage()))
		require.Empty(t, p.received()[0].Query)
	})
	t.Run("secret without query", func(t *testing.T) {
		t.Parallel()
		p := newProvider(t, providerResponse{status: 200, body: `{"errcode":0}`})
		require.NoError(t, testSender().send(context.Background(), ChannelDingTalk, channelConfig{WebhookURL: p.URL + "/send", Secret: "s"}, sampleMessage()))
		require.True(t, strings.HasPrefix(p.received()[0].Query, "timestamp="))
	})
}

func TestSendWeCom(t *testing.T) {
	t.Parallel()
	p := newProvider(t, providerResponse{status: 200, body: `{"errcode":0,"errmsg":"ok"}`})
	require.NoError(t, testSender().send(context.Background(), ChannelWeCom, channelConfig{WebhookURL: p.URL + "/send?key=k"}, sampleMessage()))
	body := decodeJSON(t, p.received()[0].Body)
	require.Equal(t, "markdown", body["msgtype"])
	require.Contains(t, body["markdown"].(map[string]any)["content"], "### [CRITICAL]")

	bad := newProvider(t, providerResponse{status: 200, body: `{"errcode":93000,"errmsg":"invalid webhook url"}`})
	err := testSender().send(context.Background(), ChannelWeCom, channelConfig{WebhookURL: bad.URL}, sampleMessage())
	require.ErrorContains(t, err, "wecom: errcode 93000: invalid webhook url")
	require.False(t, isPermanent(err))

	empty := newProvider(t, providerResponse{status: 200, body: ``})
	require.NoError(t, testSender().send(context.Background(), ChannelWeCom, channelConfig{WebhookURL: empty.URL}, sampleMessage()))
}

func TestSendTelegram(t *testing.T) {
	t.Parallel()
	const token = "123456:ABC-secret"
	tests := []struct {
		name      string
		resp      providerResponse
		wantErr   string
		permanent bool
	}{
		{name: "ok", resp: providerResponse{status: 200, body: `{"ok":true,"result":{"message_id":1}}`}},
		{name: "chat not found", resp: providerResponse{status: 400, body: `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`},
			wantErr: "telegram: error 400: Bad Request: chat not found", permanent: true},
		{name: "flood", resp: providerResponse{status: 429, body: `{"ok":false,"error_code":429,"description":"Too Many Requests"}`},
			wantErr: "telegram: error 429"},
		{name: "ok false with 200", resp: providerResponse{status: 200, body: `{"ok":false,"description":"token ` + token + ` rejected"}`},
			wantErr: "telegram: error 200"},
		{name: "gateway error", resp: providerResponse{status: 502, body: `<html>`}, wantErr: "telegram: unexpected status 502"},
		{name: "invalid body", resp: providerResponse{status: 200, body: `<html>`}, wantErr: "unexpected response body"},
		{name: "missing ok", resp: providerResponse{status: 200, body: `{}`}, wantErr: "unexpected response body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProvider(t, tt.resp)
			err := testSender().send(context.Background(), ChannelTelegram,
				channelConfig{BotToken: token, ChatID: "-100", APIBase: p.URL}, sampleMessage())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), "ABC-secret")
				require.Equal(t, tt.permanent, isPermanent(err))
			} else {
				require.NoError(t, err)
			}
			r := p.received()[0]
			require.Equal(t, "/bot"+token+"/sendMessage", r.Path)
			var body struct {
				ChatID    string `json:"chat_id"`
				Text      string `json:"text"`
				ParseMode string `json:"parse_mode"`
			}
			require.NoError(t, json.Unmarshal(r.Body, &body))
			require.Equal(t, "-100", body.ChatID)
			require.Equal(t, "HTML", body.ParseMode)
			require.Contains(t, body.Text, "&lt;fast&gt; &amp; loud")
		})
	}
}

func TestProviderErrorsRedactCredentials(t *testing.T) {
	t.Parallel()
	const hookToken = "0b5c8f7e-1d2a-4c3b-9e8f-7a6b5c4d3e2f"
	feishu := newProvider(t, providerResponse{status: 200,
		body: `{"code":19001,"msg":"param invalid: incoming webhook access token invalid ` + hookToken + ` secret feishu-secret"}`})
	err := testSender().send(context.Background(), ChannelFeishu,
		channelConfig{WebhookURL: feishu.URL + "/open-apis/bot/v2/hook/" + hookToken, Secret: "feishu-secret"}, sampleMessage())
	require.ErrorContains(t, err, "feishu: code 19001")
	require.NotContains(t, err.Error(), hookToken)
	require.NotContains(t, err.Error(), "feishu-secret")

	const accessToken = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	ding := newProvider(t, providerResponse{status: 200, body: `{"errcode":300001,"errmsg":"token ` + accessToken + ` is not exist"}`})
	err = testSender().send(context.Background(), ChannelDingTalk,
		channelConfig{WebhookURL: ding.URL + "/robot/send?access_token=" + accessToken}, sampleMessage())
	require.ErrorContains(t, err, "dingtalk: errcode 300001: token •••• is not exist")

	const password = "api-p4ssw0rd"
	tg := newProvider(t, providerResponse{status: 401, body: `{"ok":false,"error_code":401,"description":"Unauthorized ` + password + ` 123456:ABC-secret"}`})
	u, parseErr := url.Parse(tg.URL)
	require.NoError(t, parseErr)
	u.User = url.UserPassword("bot", password)
	err = testSender().send(context.Background(), ChannelTelegram,
		channelConfig{BotToken: "123456:ABC-secret", ChatID: "1", APIBase: u.String()}, sampleMessage())
	require.ErrorContains(t, err, "telegram: error 401: Unauthorized •••• ••••")
	require.True(t, isPermanent(err))

	require.Equal(t, []string{"Bearer abcdefgh", "tokenvalue", "hunter22"}, credentialsOf(channelConfig{
		URL:     "https://hooks.example.com/short?token=tokenvalue&x=1",
		Secret:  "hunter22",
		Headers: map[string]string{"Authorization": "Bearer abcdefgh", "X-Team": "operations"},
	}))
	require.Equal(t, "no message", providerText("  ", channelConfig{}))
}

func TestDingTalkTarget(t *testing.T) {
	t.Parallel()
	const ts = int64(1577262236757)
	sign := "timestamp=1577262236757&sign=" + url.QueryEscape(dingTalkSign("SEC", ts))
	tests := []struct {
		name, in, secret, want string
	}{
		{"no secret", "https://oapi.dingtalk.com/robot/send?access_token=x#frag", "", "https://oapi.dingtalk.com/robot/send?access_token=x#frag"},
		{"existing query", "https://oapi.dingtalk.com/robot/send?access_token=x", "SEC", "https://oapi.dingtalk.com/robot/send?access_token=x&" + sign},
		{"no query", "https://oapi.dingtalk.com/robot/send", "SEC", "https://oapi.dingtalk.com/robot/send?" + sign},
		{"fragment stays last", "https://oapi.dingtalk.com/robot/send?access_token=x#frag", "SEC", "https://oapi.dingtalk.com/robot/send?access_token=x&" + sign + "#frag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := dingTalkTarget(tt.in, tt.secret, ts)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	_, err := dingTalkTarget("http://[::1", "SEC", ts)
	require.True(t, isPermanent(err))
	err = testSender().send(context.Background(), ChannelDingTalk, channelConfig{WebhookURL: "http://[::1", Secret: "SEC"}, sampleMessage())
	require.True(t, isPermanent(err))
}
