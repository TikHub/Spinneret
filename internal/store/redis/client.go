// Package redis provides the Redis/Valkey building blocks shared by every
// hot-state component: the rueidis client factory, the frozen key schema
// (Keys), the Lua script loader that prepends the common prelude
// (lua/common.lua), and small distributed lock helpers.
package redis

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/redis/rueidis"
)

const (
	// ClientName is reported to the server via CLIENT SETNAME on every connection.
	ClientName = "spinneret"

	defaultDialTimeout      = 5 * time.Second
	defaultConnWriteTimeout = 10 * time.Second
)

// LoadingWait bounds how long Open waits for a server that answers LOADING,
// which a Redis or Valkey instance does for every command while it reads its
// AOF or RDB back from disk after a restart.
//
// Treating that as fatal makes the process exit and be restarted, which does
// eventually succeed but wastes the wait: the supervisor's backoff grows while
// the server needs a fixed time to load, so recovery lags further behind the
// larger the dataset is. Waiting in place costs nothing and keeps the log in
// one piece. The bound only caps a single attempt — a supervisor still retries
// after it, so a dataset slower than this is not stuck, only slower to report.
const LoadingWait = 2 * time.Minute

// loadingPoll is how often Open re-pings a loading server.
const loadingPoll = 500 * time.Millisecond

// Open creates a rueidis client and verifies connectivity with PING, waiting
// out a server that is still loading its dataset (see LoadingWait).
//
// url is a redis:// or rediss:// URL (credentials, TLS and database number are
// taken from it). When clusterAddrs is non-empty the client connects to those
// seed nodes in cluster mode; the URL, if any, then only contributes
// credentials, TLS and connection options. Client-side caching is disabled.
func Open(ctx context.Context, url string, clusterAddrs []string) (rueidis.Client, error) {
	opt, err := clientOption(url, clusterAddrs)
	if err != nil {
		return nil, err
	}
	client, err := rueidis.NewClient(opt)
	if err != nil {
		return nil, fmt.Errorf("redis: connect: %w", err)
	}
	ping := func(ctx context.Context) error {
		return client.Do(ctx, client.B().Ping().Build()).Error()
	}
	if err := waitReady(ctx, ping, LoadingWait, loadingPoll); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// waitReady pings until the server answers, retrying only while it reports
// that it is still loading. Every other error is returned on the first try:
// a wrong password or an unreachable host does not become right by waiting.
func waitReady(ctx context.Context, ping func(context.Context) error, wait, poll time.Duration) error {
	started := time.Now()
	for {
		err := ping(ctx)
		if err == nil {
			return nil
		}
		if !isLoading(err) {
			return fmt.Errorf("redis: ping: %w", err)
		}
		if waited := time.Since(started); waited >= wait {
			return fmt.Errorf("redis: ping: still loading its dataset after %s: %w", waited.Round(time.Second), err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("redis: ping: %w", ctx.Err())
		case <-time.After(poll):
		}
	}
}

// isLoading reports whether err is the server's "loading the dataset" reply.
// rueidis defines that as the LOADING prefix of the error string and exposes
// the same string through Error(), so the fallback tests exactly what its own
// IsLoading does — spelled out because a *rueidis.RedisError has unexported
// fields and cannot be constructed by a test.
func isLoading(err error) bool {
	if redisErr, ok := rueidis.IsRedisErr(err); ok {
		return redisErr.IsLoading()
	}
	return strings.HasPrefix(err.Error(), "LOADING")
}

// clientOption builds the rueidis options for Open without dialing.
func clientOption(rawURL string, clusterAddrs []string) (rueidis.ClientOption, error) {
	addrs := cleanAddrs(clusterAddrs)
	if strings.TrimSpace(rawURL) == "" && len(addrs) == 0 {
		return rueidis.ClientOption{}, errors.New("redis: url or cluster addresses required")
	}

	var opt rueidis.ClientOption
	if strings.TrimSpace(rawURL) != "" {
		parsed, err := rueidis.ParseURL(strings.TrimSpace(rawURL))
		if err != nil {
			return rueidis.ClientOption{}, fmt.Errorf("redis: parse url: %w", sanitizeURLError(err))
		}
		opt = parsed
	}

	if len(addrs) > 0 {
		if opt.SelectDB != 0 {
			return rueidis.ClientOption{}, fmt.Errorf("redis: database %d cannot be selected in cluster mode", opt.SelectDB)
		}
		opt.InitAddress = addrs
		opt.ShuffleInit = true
		if opt.TLSConfig != nil {
			// The server name derived from the URL host does not apply to every seed node.
			opt.TLSConfig.ServerName = ""
		}
	}

	if opt.ClientName == "" {
		opt.ClientName = ClientName
	}
	if opt.Dialer.Timeout <= 0 {
		opt.Dialer.Timeout = defaultDialTimeout
	}
	if opt.ConnWriteTimeout <= 0 {
		opt.ConnWriteTimeout = defaultConnWriteTimeout
	}
	opt.DisableCache = true
	return opt, nil
}

// cleanAddrs trims the given addresses and removes empty entries.
func cleanAddrs(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// sanitizeURLError strips the raw URL (which may contain a password) from
// net/url parse errors.
func sanitizeURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("%s: %w", uerr.Op, uerr.Err)
	}
	return err
}
