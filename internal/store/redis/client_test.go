package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientOption(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		addrs   []string
		wantErr string
		check   func(t *testing.T, addrs []string, db int, tls bool, shuffle bool)
	}{
		{
			name:    "nothing configured",
			url:     "  ",
			addrs:   []string{" ", ""},
			wantErr: "url or cluster addresses required",
		},
		{
			name:    "invalid scheme",
			url:     "http://localhost:6379",
			wantErr: "invalid URL scheme",
		},
		{
			name:    "invalid database",
			url:     "redis://localhost:6379/abc",
			wantErr: "invalid database number",
		},
		{
			name:    "database in cluster mode",
			url:     "redis://localhost:6379/2",
			addrs:   []string{"10.0.0.1:6379"},
			wantErr: "cannot be selected in cluster mode",
		},
		{
			name: "standalone url",
			url:  "redis://user:secret@cache.internal:6380/3",
			check: func(t *testing.T, addrs []string, db int, tls bool, shuffle bool) {
				require.Equal(t, []string{"cache.internal:6380"}, addrs)
				require.Equal(t, 3, db)
				require.False(t, tls)
				require.False(t, shuffle)
			},
		},
		{
			name:  "cluster addresses with tls url",
			url:   "rediss://:secret@seed.internal:6379",
			addrs: []string{" 10.0.0.1:6379 ", "", "10.0.0.2:6379"},
			check: func(t *testing.T, addrs []string, db int, tls bool, shuffle bool) {
				require.Equal(t, []string{"10.0.0.1:6379", "10.0.0.2:6379"}, addrs)
				require.Zero(t, db)
				require.True(t, tls)
				require.True(t, shuffle)
			},
		},
		{
			name:  "cluster addresses without url",
			addrs: []string{"10.0.0.1:6379"},
			check: func(t *testing.T, addrs []string, db int, tls bool, shuffle bool) {
				require.Equal(t, []string{"10.0.0.1:6379"}, addrs)
				require.False(t, tls)
				require.True(t, shuffle)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt, err := clientOption(tt.url, tt.addrs)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), "secret")
				return
			}
			require.NoError(t, err)
			require.True(t, opt.DisableCache)
			require.Equal(t, ClientName, opt.ClientName)
			require.Equal(t, defaultDialTimeout, opt.Dialer.Timeout)
			require.Equal(t, defaultConnWriteTimeout, opt.ConnWriteTimeout)
			tt.check(t, opt.InitAddress, opt.SelectDB, opt.TLSConfig != nil, opt.ShuffleInit)
		})
	}
}

func TestClientOptionKeepsURLOverrides(t *testing.T) {
	opt, err := clientOption("redis://localhost:6379?client_name=custom&dial_timeout=2s&write_timeout=3s", nil)
	require.NoError(t, err)
	require.Equal(t, "custom", opt.ClientName)
	require.Equal(t, 2*time.Second, opt.Dialer.Timeout)
	require.Equal(t, 3*time.Second, opt.ConnWriteTimeout)
}

func TestClientOptionDoesNotLeakPassword(t *testing.T) {
	_, err := clientOption("redis://user:hunter2@local host:6379", nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2")
}

func TestOpen(t *testing.T) {
	url := testRedisURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Open(ctx, url, nil)
	require.NoError(t, err)
	defer client.Close()
	name, err := client.Do(ctx, client.B().ClientGetname().Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, ClientName, name)

	_, err = Open(ctx, "", nil)
	require.Error(t, err)
}

func TestOpenUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("dials the network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Open(ctx, "redis://127.0.0.1:1?dial_timeout=500ms", nil)
	require.Error(t, err)
}

// loadingErr is what a Redis or Valkey instance replies to every command while
// it reads its dataset back from disk.
var loadingErr = errors.New("LOADING Valkey is loading the dataset in memory")

func TestWaitReadyRetriesWhileLoading(t *testing.T) {
	t.Parallel()
	calls := 0
	err := waitReady(context.Background(), func(context.Context) error {
		calls++
		if calls < 4 {
			return loadingErr
		}
		return nil
	}, time.Minute, time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, 4, calls, "should have pinged until the server finished loading")
}

func TestWaitReadyFailsFastOnOtherErrors(t *testing.T) {
	t.Parallel()
	// A wrong password does not become right by waiting, so it must not be
	// retried: doing so would turn a misconfiguration into a slow startup.
	calls := 0
	authErr := errors.New("WRONGPASS invalid username-password pair")
	err := waitReady(context.Background(), func(context.Context) error {
		calls++
		return authErr
	}, time.Minute, time.Millisecond)
	require.ErrorIs(t, err, authErr)
	require.Equal(t, 1, calls)
}

func TestWaitReadyGivesUpAfterTheBound(t *testing.T) {
	t.Parallel()
	calls := 0
	start := time.Now()
	err := waitReady(context.Background(), func(context.Context) error {
		calls++
		return loadingErr
	}, 30*time.Millisecond, time.Millisecond)
	require.ErrorIs(t, err, loadingErr)
	require.Contains(t, err.Error(), "still loading its dataset")
	require.Greater(t, calls, 1, "should have retried before giving up")
	require.Less(t, time.Since(start), 5*time.Second, "must not wait past the bound")
}

func TestWaitReadyStopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitReady(ctx, func(context.Context) error { return loadingErr }, time.Minute, time.Millisecond)
	require.ErrorIs(t, err, context.Canceled)
}

func TestIsLoading(t *testing.T) {
	t.Parallel()
	require.True(t, isLoading(loadingErr))
	require.False(t, isLoading(errors.New("ERR unknown command")))
	require.False(t, isLoading(errors.New("WRONGPASS invalid username-password pair")))
	// Not a prefix match: only the server's own reply counts.
	require.False(t, isLoading(errors.New("dial tcp: LOADING")))
}
