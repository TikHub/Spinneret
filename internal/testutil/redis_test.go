package testutil

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/TikHub/Spinneret/internal/store/redis"
)

// fixtureFatalCapture captures Fatalf calls of a fixture without failing the
// enclosing test; like testing.T it stops the calling goroutine.
type fixtureFatalCapture struct {
	*testing.T
	mu      sync.Mutex
	message string
}

func (c *fixtureFatalCapture) Fatalf(format string, args ...any) {
	c.mu.Lock()
	c.message = fmt.Sprintf(format, args...)
	c.mu.Unlock()
	runtime.Goexit()
}

func (c *fixtureFatalCapture) run(fn func(tb testing.TB)) string {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(c)
	}()
	<-done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.message
}

func requireRedisBase(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	base, err := sharedRedis.baseURL()
	require.NoError(t, err)
	return base
}

func countRedisKeys(ctx context.Context, t *testing.T, c rueidis.Client, prefix string) int {
	t.Helper()
	n := 0
	var cursor uint64
	for {
		entry, err := c.Do(ctx, c.B().Scan().Cursor(cursor).Match(prefix+":*").Count(1000).Build()).AsScanEntry()
		require.NoError(t, err)
		n += len(entry.Elements)
		cursor = entry.Cursor
		if cursor == 0 {
			return n
		}
	}
}

func TestRedisFixture(t *testing.T) {
	base := requireRedisBase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	observer, err := redis.Open(ctx, base, nil)
	require.NoError(t, err)
	defer observer.Close()

	var prefixA, prefixB string
	t.Run("isolated prefixes", func(t *testing.T) {
		a, keysA := Redis(t)
		b, keysB := Redis(t)
		prefixA, prefixB = keysA.Prefix, keysB.Prefix
		require.Regexp(t, regexp.MustCompile(`^t[0-9a-f]{10}$`), keysA.Prefix)
		require.NotEqual(t, keysA.Prefix, keysB.Prefix)

		require.NoError(t, a.Do(ctx, a.B().Set().Key(keysA.Lock("x")).Value("a").Build()).Error())
		require.NoError(t, a.Do(ctx, a.B().Hset().Key(keysA.Identity(1, 2)).FieldValue().FieldValue("st", "active").Build()).Error())
		require.NoError(t, b.Do(ctx, b.B().Set().Key(keysB.Lock("x")).Value("b").Build()).Error())

		got, err := b.Do(ctx, b.B().Get().Key(keysA.Lock("x")).Build()).ToString()
		require.NoError(t, err)
		require.Equal(t, "a", got)
		require.Equal(t, 2, countRedisKeys(ctx, t, observer, keysA.Prefix))
		require.Equal(t, 1, countRedisKeys(ctx, t, observer, keysB.Prefix))
	})
	require.NotEmpty(t, prefixA)
	require.Zero(t, countRedisKeys(ctx, t, observer, prefixA), "keys removed on cleanup")
	require.Zero(t, countRedisKeys(ctx, t, observer, prefixB), "keys removed on cleanup")
}

func TestDeleteRedisPrefix(t *testing.T) {
	client, keys := Redis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// More keys than one SCAN page, spread over several sites (hash tags).
	const total = 2500
	cmds := make(rueidis.Commands, 0, total)
	for i := range total {
		cmds = append(cmds, client.B().Set().Key(keys.Identity(int64(i%7), int64(i))).Value(strconv.Itoa(i)).Build())
	}
	for _, res := range client.DoMulti(ctx, cmds...) {
		require.NoError(t, res.Error())
	}
	neighbour := keys.Prefix + "x:not-mine"
	require.NoError(t, client.Do(ctx, client.B().Set().Key(neighbour).Value("1").Build()).Error())
	t.Cleanup(func() {
		_ = client.Do(context.Background(), client.B().Del().Key(neighbour).Build()).Error()
	})

	n, err := deleteRedisPrefix(ctx, client, keys.Prefix)
	require.NoError(t, err)
	require.Equal(t, total, n)
	require.Zero(t, countRedisKeys(ctx, t, client, keys.Prefix))
	exists, err := client.Do(ctx, client.B().Exists().Key(neighbour).Build()).AsInt64()
	require.NoError(t, err)
	require.Equal(t, int64(1), exists, "keys of a longer prefix are untouched")

	n, err = deleteRedisPrefix(ctx, client, keys.Prefix)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestDeleteRedisPrefixErrors(t *testing.T) {
	client, _ := Redis(t)
	for _, prefix := range []string{"", "t*", "t?", "t[a]", `t\`} {
		_, err := deleteRedisPrefix(context.Background(), client, prefix)
		require.ErrorContains(t, err, "unsafe prefix", prefix)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := deleteRedisPrefix(canceled, client, "tabc")
	require.ErrorContains(t, err, "scan")
}

func TestRandomRedisPrefix(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		p := randomRedisPrefix()
		require.Regexp(t, `^t[0-9a-f]{10}$`, p)
		require.False(t, seen[p])
		seen[p] = true
	}
}

func TestRedisFixtureFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Run("container start failure", func(t *testing.T) {
		t.Setenv(RedisURLEnv, "")
		f := &redisFixture{}
		f.containerOnce.Do(func() { f.containerErr = errors.New("docker unavailable") })
		msg := (&fixtureFatalCapture{T: t}).run(func(tb testing.TB) { f.open(tb) })
		require.Contains(t, msg, "docker unavailable")
		require.Contains(t, msg, RedisURLEnv)
	})

	t.Run("container url is used when the variable is unset", func(t *testing.T) {
		t.Setenv(RedisURLEnv, " ")
		f := &redisFixture{}
		f.containerOnce.Do(func() { f.containerURL = "redis://container:6379" })
		got, err := f.baseURL()
		require.NoError(t, err)
		require.Equal(t, "redis://container:6379", got)
	})

	t.Run("unreachable server", func(t *testing.T) {
		t.Setenv(RedisURLEnv, "redis://:secret@127.0.0.1:1/0?dial_timeout=500ms")
		f := &redisFixture{}
		msg := (&fixtureFatalCapture{T: t}).run(func(tb testing.TB) { f.open(tb) })
		require.Contains(t, msg, "connect test redis")
		require.NotContains(t, msg, "secret")
	})
}

func TestStartRedisContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), redisSetupTimeout)
	defer cancel()

	// The container is reaped by Ryuk when the test binary exits.
	url, err := startRedisContainer()
	require.NoError(t, err)
	client, err := redis.Open(ctx, url, nil)
	require.NoError(t, err)
	defer client.Close()
	info, err := client.Do(ctx, client.B().Info().Section("server").Build()).ToString()
	require.NoError(t, err)
	require.Contains(t, info, "valkey_version:8.")
}
