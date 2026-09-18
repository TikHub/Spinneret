package configcenter

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
)

func TestContentCacheLRU(t *testing.T) {
	t.Parallel()
	c := newContentCache(100, 60)
	body := func(n int) *content { return newContent(FormatText, strings.Repeat("x", n), time.Time{}) }
	k := func(i int32) contentKey { return contentKey{itemID: "cfg_a", version: i} }

	c.put(k(1), body(40))
	c.put(k(2), body(40))
	_, ok := c.get(k(1)) // 1 becomes most recently used
	require.True(t, ok)
	c.put(k(3), body(40)) // evicts 2
	_, ok = c.get(k(2))
	require.False(t, ok)
	_, ok = c.get(k(1))
	require.True(t, ok)
	_, ok = c.get(k(3))
	require.True(t, ok)

	c.put(k(3), body(40)) // re-put is a no-op
	c.put(k(4), body(61)) // larger than the entry limit: not cached
	_, ok = c.get(k(4))
	require.False(t, ok)
	require.LessOrEqual(t, c.used, int64(100))
}

func TestNewContentScansReferences(t *testing.T) {
	t.Parallel()
	c := newContent(FormatText, "${secret:a} ${secret:a#2}", time.Time{})
	require.NoError(t, c.refsErr)
	require.Len(t, c.spans, 2)
	require.Equal(t, []SecretRef{{Path: "a"}, {Path: "a", Version: 2}}, c.refs)
	require.Greater(t, c.memSize(), int64(len(c.body)))

	bad := newContent(FormatText, "${secret:A}", time.Time{})
	require.Error(t, bad.refsErr)
}

func TestRuntimeHelpers(t *testing.T) {
	t.Parallel()
	require.Equal(t, int32(1), apiRuntimeVersion(0))
	require.Equal(t, int32(1), apiRuntimeVersion(-5))
	require.Equal(t, int32(43), apiRuntimeVersion(42))
	require.Equal(t, int32(math.MaxInt32), apiRuntimeVersion(math.MaxInt64))
	require.NotEmpty(t, runtimeDescription(RuntimeBreakers))
	require.Empty(t, runtimeDescription("other"))

	rc := newRuntimeCache()
	for i := range maxRuntimeCacheEntries + 1 {
		rc.put(itemKey{ns: strings.Repeat("n", i%7) + string(rune(i)), group: RuntimeGroup, key: RuntimeBreakers}, runtimeContent{version: 1})
	}
	rc.mu.Lock()
	require.LessOrEqual(t, len(rc.items), maxRuntimeCacheEntries)
	rc.mu.Unlock()
}

func TestConfigDefaults(t *testing.T) {
	t.Parallel()
	c := Config{}.withDefaults()
	require.Equal(t, DefaultMaxWatchers, c.MaxWatchers)
	require.Equal(t, DefaultMaxWatchTimeout, c.MaxWatchTimeout)
	require.Equal(t, DefaultWatchTimeout, c.DefaultWatchTimeout)
	require.Equal(t, DefaultResyncInterval, c.ResyncInterval)
	require.Equal(t, DefaultMaxCachedVersions, c.MaxCachedVersions)
	require.EqualValues(t, DefaultContentCacheBytes, c.ContentCacheBytes)
	require.EqualValues(t, DefaultMaxResponseBytes, c.MaxResponseBytes)
	require.Equal(t, DefaultOperationTimeout, c.OperationTimeout)

	c = Config{MaxWatchTimeout: time.Second, DefaultWatchTimeout: time.Minute}.withDefaults()
	require.Equal(t, time.Second, c.DefaultWatchTimeout)
}

func TestWrapErr(t *testing.T) {
	t.Parallel()
	require.NoError(t, wrapErr(nil, "x"))
	appErr := apperr.NotFound("gone")
	require.Same(t, appErr, wrapErr(appErr, "x"))
	plain := errors.New("boom")
	wrapped := wrapErr(plain, "load %s", "thing")
	require.ErrorIs(t, wrapped, plain)
	require.EqualError(t, wrapped, "load thing: boom")
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	require.Equal(t, DefaultPageSize, pageSize(0))
	require.Equal(t, MaxPageSize, pageSize(10_000))
	require.Equal(t, 7, pageSize(7))
	require.Equal(t, `a\%b\_c\\d`, escapeLike(`a%b_c\d`))
	next, err := nextVersion(math.MaxInt32)
	require.Error(t, err)
	require.Zero(t, next)
	require.Equal(t, "v12", versionName(12))
	require.Equal(t, `a\"b`, jsonStringBody(`a"b`))
}

func TestNilNamespaceRejected(t *testing.T) {
	t.Parallel()
	svc := New(Config{}, nil, catalogtest.New(), nil, nil, nil, nil, nil, nil)
	ctx := context.Background()
	p := authz.System("test")
	_, err := svc.ListItems(ctx, p, nil, ListOptions{})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = svc.GetItemByLocator(ctx, p, nil, "g", "k")
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = svc.CreateItem(ctx, p, nil, CreateRequest{})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = svc.GetConfig(ctx, p, nil, Ref{Group: "g", Key: "k"})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = svc.WatchConfig(ctx, p, nil, []WatchRef{{Group: "g", Key: "k"}}, time.Second)
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
}
