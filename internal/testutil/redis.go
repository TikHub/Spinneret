package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// RedisURLEnv names the environment variable holding the Redis/Valkey URL used
// by integration tests.
const RedisURLEnv = "SPINNERET_TEST_REDIS_URL"

const (
	redisImage          = "valkey/valkey:8-alpine"
	redisSetupTimeout   = 2 * time.Minute
	redisCleanupTimeout = 30 * time.Second
	redisScanCount      = 1000
)

// redisFixture lazily starts one Valkey container per test binary when no URL is configured.
type redisFixture struct {
	containerOnce sync.Once
	containerURL  string
	containerErr  error
}

var sharedRedis = &redisFixture{}

// Redis returns a client connected to the test Redis/Valkey server and a key
// builder with a unique prefix ("t" + 10 hex characters), so tests sharing a
// server never see each other's keys. When the test ends every key under the
// prefix is deleted (SCAN + UNLINK, never FLUSHDB) and the client is closed.
// Skips in -short mode.
func Redis(t testing.TB) (rueidis.Client, redis.Keys) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test requires Redis; skipped in -short mode")
	}
	return sharedRedis.open(t)
}

func (f *redisFixture) open(t testing.TB) (rueidis.Client, redis.Keys) {
	t.Helper()
	base, err := f.baseURL()
	if err != nil {
		t.Fatalf("testutil: start redis container (set %s to use a running server): %v", RedisURLEnv, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisSetupTimeout)
	defer cancel()
	client, err := redis.Open(ctx, base, nil)
	if err != nil {
		t.Fatalf("testutil: connect test redis: %v", err)
	}
	keys := redis.NewKeys(randomRedisPrefix())
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), redisCleanupTimeout)
		defer ccancel()
		if _, err := deleteRedisPrefix(cctx, client, keys.Prefix); err != nil {
			t.Errorf("testutil: delete test keys with prefix %s: %v", keys.Prefix, err)
		}
		client.Close()
	})
	return client, keys
}

// baseURL returns the configured URL or starts the shared container once.
func (f *redisFixture) baseURL() (string, error) {
	if v := strings.TrimSpace(os.Getenv(RedisURLEnv)); v != "" {
		return v, nil
	}
	f.containerOnce.Do(func() {
		f.containerURL, f.containerErr = startRedisContainer()
	})
	return f.containerURL, f.containerErr
}

// startRedisContainer starts a Valkey container; Ryuk reaps it when the test binary exits.
func startRedisContainer() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), redisSetupTimeout)
	defer cancel()
	c, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		return "", fmt.Errorf("run %s: %w", redisImage, err)
	}
	url, err := c.ConnectionString(ctx)
	if err != nil {
		return "", fmt.Errorf("redis container connection string: %w", err)
	}
	return url, nil
}

// deleteRedisPrefix unlinks every key matching "<prefix>:*" on every node and
// returns the number of keys removed.
func deleteRedisPrefix(ctx context.Context, client rueidis.Client, prefix string) (int, error) {
	if prefix == "" || strings.ContainsAny(prefix, "*?[]\\") {
		return 0, fmt.Errorf("refusing to delete keys for unsafe prefix %q", prefix)
	}
	pattern := prefix + ":*"
	deleted := 0
	for _, node := range client.Nodes() {
		var cursor uint64
		for {
			entry, err := node.Do(ctx, node.B().Scan().Cursor(cursor).Match(pattern).Count(redisScanCount).Build()).AsScanEntry()
			if err != nil {
				return deleted, fmt.Errorf("scan: %w", err)
			}
			if len(entry.Elements) > 0 {
				// Per-key UNLINK routed through the main client works in cluster mode too.
				cmds := make(rueidis.Commands, len(entry.Elements))
				for i, key := range entry.Elements {
					cmds[i] = client.B().Unlink().Key(key).Build()
				}
				for _, res := range client.DoMulti(ctx, cmds...) {
					n, err := res.AsInt64()
					if err != nil {
						return deleted, fmt.Errorf("unlink: %w", err)
					}
					deleted += int(n)
				}
			}
			cursor = entry.Cursor
			if cursor == 0 {
				break
			}
		}
	}
	return deleted, nil
}

// randomRedisPrefix returns "t" followed by 10 random hex characters.
func randomRedisPrefix() string {
	var b [5]byte
	// crypto/rand.Read never returns an error (it aborts the process instead).
	_, _ = rand.Read(b[:])
	return "t" + hex.EncodeToString(b[:])
}
