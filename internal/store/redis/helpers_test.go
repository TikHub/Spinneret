package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

const testRedisImage = "valkey/valkey:8-alpine"

var (
	containerOnce sync.Once
	containerURL  string
	containerErr  error
)

// testRedisURL returns the Redis URL for integration tests, skipping in short mode.
func testRedisURL(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test requires Redis; skipped in -short mode")
	}
	if u := os.Getenv("SPINNERET_TEST_REDIS_URL"); u != "" {
		return u
	}
	containerOnce.Do(func() {
		ctx := context.Background()
		c, err := tcredis.Run(ctx, testRedisImage)
		if err != nil {
			containerErr = err
			return
		}
		containerURL, containerErr = c.ConnectionString(ctx)
	})
	require.NoError(t, containerErr, "start valkey container")
	return containerURL
}

// newTestClient opens a client and a unique key prefix whose keys are removed on cleanup.
func newTestClient(t testing.TB) (rueidis.Client, Keys) {
	t.Helper()
	url := testRedisURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Open(ctx, url, nil)
	require.NoError(t, err)

	var b [5]byte
	_, err = rand.Read(b[:])
	require.NoError(t, err)
	keys := NewKeys("t" + hex.EncodeToString(b[:]))

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		var cursor uint64
		for {
			entry, err := client.Do(cctx, client.B().Scan().Cursor(cursor).Match(keys.Prefix+":*").Count(1000).Build()).AsScanEntry()
			if err != nil {
				t.Logf("cleanup scan: %v", err)
				break
			}
			if len(entry.Elements) > 0 {
				if err := client.Do(cctx, client.B().Unlink().Key(entry.Elements...).Build()).Error(); err != nil {
					t.Logf("cleanup unlink: %v", err)
				}
			}
			cursor = entry.Cursor
			if cursor == 0 {
				break
			}
		}
		client.Close()
	})
	return client, keys
}
