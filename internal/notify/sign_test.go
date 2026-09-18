package notify

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// The expected values were computed independently with Python's hmac module.
func TestSignatures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "webhook",
			got:  WebhookSignature("whsec_test", "1758011411", []byte(`{"id":"alt_1"}`)),
			want: "sha256=d6301cc4c95e202e904fcc35567814699a695b2226f5060c6206d3e691c83ca4",
		},
		{
			name: "feishu",
			got:  feishuSign("feishu-secret", 1599360473),
			want: "GnmU6lMJMtXPkxW7kXxgtXI0SJxjk0TQFnFuz93Dt+0=",
		},
		{
			name: "dingtalk",
			got:  dingTalkSign("SECabc123", 1577262236757),
			want: "sP2tUmwYDrLqnFjDuYnBJi0Od0TLi424Z2c6qlTmCao=",
		},
		{
			name: "dingtalk url encoded",
			got:  url.QueryEscape(dingTalkSign("SECabc123", 1577262236757)),
			want: "sP2tUmwYDrLqnFjDuYnBJi0Od0TLi424Z2c6qlTmCao%3D",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, tt.got)
		})
	}
}
