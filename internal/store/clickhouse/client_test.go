package clickhouse

import (
	"context"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/require"
)

func TestOptions(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
		check   func(t *testing.T, opt *clickhouse.Options)
	}{
		{name: "empty", url: " ", wantErr: "url is empty"},
		{name: "http protocol rejected", url: "http://localhost:8123/default", wantErr: "only the native protocol"},
		{name: "invalid dial timeout", url: "clickhouse://localhost:9000/db?dial_timeout=abc", wantErr: "dial timeout"},
		{name: "unparsable url hides password", url: "clickhouse://user:hunter2@local host:9000/db", wantErr: "parse url"},
		{
			name: "defaults",
			url:  "clickhouse://user:secret@ch1:9000/events",
			check: func(t *testing.T, opt *clickhouse.Options) {
				require.Equal(t, clickhouse.Native, opt.Protocol)
				require.Equal(t, []string{"ch1:9000"}, opt.Addr)
				require.Equal(t, "events", opt.Auth.Database)
				require.Equal(t, "user", opt.Auth.Username)
				require.NotNil(t, opt.Compression)
				require.Equal(t, clickhouse.CompressionLZ4, opt.Compression.Method)
				require.Equal(t, defaultDialTimeout, opt.DialTimeout)
				require.Equal(t, productName, opt.ClientInfo.Products[len(opt.ClientInfo.Products)-1].Name)
			},
		},
		{
			name: "url overrides",
			url:  "tcp://ch1:9000,ch2:9000/db?dial_timeout=2s&compress=zstd",
			check: func(t *testing.T, opt *clickhouse.Options) {
				require.Equal(t, []string{"ch1:9000", "ch2:9000"}, opt.Addr)
				require.Equal(t, 2*time.Second, opt.DialTimeout)
				require.Equal(t, clickhouse.CompressionZSTD, opt.Compression.Method)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt, err := options(tt.url)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), "hunter2")
				return
			}
			require.NoError(t, err)
			tt.check(t, opt)
		})
	}
}

func TestOpen(t *testing.T) {
	url := testClickHouseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := Open(ctx, url)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	var one uint8
	require.NoError(t, conn.QueryRow(ctx, "SELECT 1").Scan(&one))
	require.Equal(t, uint8(1), one)

	_, err = Open(ctx, "")
	require.ErrorIs(t, err, ErrNoURL)
}

func TestOpenUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("dials the network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Open(ctx, "clickhouse://127.0.0.1:1/default?dial_timeout=500ms")
	require.ErrorContains(t, err, "ping")
}
