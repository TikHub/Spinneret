package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
)

func TestNewKeysDefaultPrefix(t *testing.T) {
	require.Equal(t, "sp", NewKeys("").Prefix)
	require.Equal(t, "custom", NewKeys("custom").Prefix)
	// The zero value behaves like the default prefix.
	require.Equal(t, "sp:{s1}:meta", Keys{}.SiteMeta(1))
}

func TestKeysSchema(t *testing.T) {
	const lease = "lse_0192a3f4c1d27b8e9a01f2c3d4e5a6b7_1a_0f"
	sp := NewKeys("sp")
	cu := NewKeys("x1")

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"SiteTag", sp.SiteTag(12), "{s12}"},
		{"SiteBase", sp.SiteBase(12), "sp:{s12}:"},
		{"SiteMeta", sp.SiteMeta(12), "sp:{s12}:meta"},
		{"Ready", sp.Ready(12, 7), "sp:{s12}:rdy:7"},
		{"Health", sp.Health(12, 7), "sp:{s12}:hs:7"},
		{"Identity", sp.Identity(12, 9001), "sp:{s12}:id:9001"},
		{"Account", sp.Account(12, 3), "sp:{s12}:acc:3"},
		{"AccountMembers", sp.AccountMembers(12, 3), "sp:{s12}:accm:3"},
		{"Lease", sp.Lease(12, lease), "sp:{s12}:ls:" + lease},
		{"LeaseExpiry", sp.LeaseExpiry(12), "sp:{s12}:lsexp"},
		{"Sticky", sp.Sticky(12, 7, "sess-1"), "sp:{s12}:stk:7:sess-1"},
		{"Quota", sp.Quota(12, 7, 9001), "sp:{s12}:q:7:9001"},
		{"ProxySite", sp.ProxySite(12, 44), "sp:{s12}:px:44"},
		{"ProxyReady", sp.ProxyReady(12), "sp:{s12}:pxrdy"},
		{"Window", sp.Window(12, 7, 29300190199), "sp:{s12}:win:7:29300190199"},
		{"WindowHLL", sp.WindowHLL(12, 7, 29300190199), "sp:{s12}:winh:7:29300190199"},
		{"Breaker", sp.Breaker(12, 7), "sp:{s12}:brk:7"},
		{"CounterIdentity", sp.Counter(12, "i12", "captcha", 600000), "sp:{s12}:cnt:i12:captcha:600000"},
		{"CounterProxy", sp.Counter(12, "p9", "rate_limited", 60000), "sp:{s12}:cnt:p9:rate_limited:60000"},
		{"CrossProxy", sp.CrossProxy(12, 44), "sp:{s12}:xa:p:44"},
		{"CrossIdentity", sp.CrossIdentity(12, 9001), "sp:{s12}:xa:i:9001"},
		{"Bans", sp.Bans(12, 9001), "sp:{s12}:bans:9001"},
		{"RecentCooldowns", sp.RecentCooldowns(12, 7), "sp:{s12}:rcd:7"},
		{"RecentSiteCooldowns", sp.RecentSiteCooldowns(12), "sp:{s12}:rcds"},
		{"OpenBreakers", sp.OpenBreakers(12), "sp:{s12}:brko"},
		{"Checkpoint", sp.Checkpoint(12), "sp:{s12}:ckpt"},
		{"Dirty", sp.Dirty(12), "sp:{s12}:dirty"},
		{"RoundRobin", sp.RoundRobin(12, 7), "sp:{s12}:rr:7"},
		{"ActiveGroups", sp.ActiveGroups(12), "sp:{s12}:aeg"},
		{"SiteLockReap", sp.SiteLock(12, "reap"), "sp:{s12}:lock:reap"},
		{"SiteLockBreaker", sp.SiteLock(12, "brk:7"), "sp:{s12}:lock:brk:7"},
		{"ShardTag", sp.ShardTag(3), "{r3}"},
		{"ShardTagTwoDigits", sp.ShardTag(15), "{r15}"},
		{"Stream", sp.Stream(3), "sp:{r3}:stream"},
		{"Dedup", sp.Dedup(3, "rep-1"), "sp:{r3}:dd:rep-1"},
		{"ShardOwner", sp.ShardOwner(3), "sp:{r3}:owner"},
		{"Epoch", sp.Epoch(), "sp:meta:epoch"},
		{"Workers", sp.Workers(), "sp:workers"},
		{"RuntimeVersions", sp.RuntimeVersions("ns_abc"), "sp:rtv:ns_abc"},
		{"CatalogMarks", sp.CatalogMarks(), "sp:catv"},
		{"Session", sp.Session("deadbeef"), "sp:sess:deadbeef"},
		{"RateLimit", sp.RateLimit("login_user", "alice"), "sp:rl:login_user:alice"},
		{"AlertDedup", sp.AlertDedup("brk:eg_1"), "sp:alert:brk:eg_1"},
		{"Lock", sp.Lock("snapshot"), "sp:lock:snapshot"},
		{"Channel", sp.Channel("catalog"), "sp:ch:catalog"},
		{"ChannelNamespace", sp.Channel("ns:ns_1"), "sp:ch:ns:ns_1"},
		{"ChannelPattern", sp.ChannelPattern(), "sp:ch:*"},
		{"CustomPrefixSite", cu.Ready(1, 2), "x1:{s1}:rdy:2"},
		{"CustomPrefixShard", cu.Stream(0), "x1:{r0}:stream"},
		{"CustomPrefixGlobal", cu.Workers(), "x1:workers"},
		{"CustomPrefixChannel", cu.ChannelPattern(), "x1:ch:*"},
		{"ZeroValueChannelPattern", Keys{}.ChannelPattern(), "sp:ch:*"},
		{"GlobPrefixChannelPattern", NewKeys(`a*b?[c]\d`).ChannelPattern(), `a\*b\?\[c\]\\d:ch:*`},
		{"GlobPrefixChannel", NewKeys("a*b").Channel("catalog"), "a*b:ch:catalog"},
		{"NegativeKey", sp.Identity(-1, -2), "sp:{s-1}:id:-2"},
		{"LargeKeys", sp.Quota(9223372036854775807, 1, 2), "sp:{s9223372036854775807}:q:1:2"},
		{"LongSticky", sp.Sticky(1, 2, strings.Repeat("a", 64)), "sp:{s1}:stk:2:" + strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.got)
		})
	}
}

func TestSiteKeysShareHashTag(t *testing.T) {
	k := NewKeys("sp")
	tag := k.SiteTag(42)
	for _, key := range []string{
		k.SiteMeta(42), k.Ready(42, 1), k.Health(42, 1), k.Identity(42, 1), k.Lease(42, "lse_x"),
		k.Quota(42, 1, 2), k.Window(42, 1, 2), k.Counter(42, "a1", "banned", 1000), k.SiteLock(42, "reap"),
	} {
		require.True(t, strings.HasPrefix(key, "sp:"+tag+":"), key)
		require.True(t, strings.HasPrefix(key, k.SiteBase(42)), key)
	}
	require.True(t, strings.HasSuffix(k.SiteMeta(42), "meta"))
	require.Equal(t, k.SiteBase(42)+"meta", k.SiteMeta(42))
}

func TestChannelPatternEscapesGlobPrefix(t *testing.T) {
	client, keys := newTestClient(t)

	// A prefix with glob metacharacters must only match its own channels.
	own := NewKeys(keys.Prefix + "*")
	sibling := NewKeys(keys.Prefix + "x")
	tests := []struct {
		name    string
		channel string
		want    bool
	}{
		{"own channel", own.Channel("catalog"), true},
		{"sibling prefix channel", sibling.Channel("catalog"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, globMatch(t, client, own.ChannelPattern(), tt.channel))
		})
	}
}

// globMatch reports whether pattern matches channel using the server's glob
// implementation (a PSUBSCRIBE round trip).
func globMatch(t *testing.T, client rueidis.Client, pattern, channel string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subscribed := make(chan struct{})
	var once sync.Once
	received := make(chan string, 1)
	finished := make(chan struct{})
	var receiveErr error
	subCtx, stop := context.WithCancel(rueidis.WithOnSubscriptionHook(ctx, func(s rueidis.PubSubSubscription) {
		if s.Kind == "psubscribe" {
			once.Do(func() { close(subscribed) })
		}
	}))
	defer func() {
		stop()
		<-finished
	}()
	go func() {
		defer close(finished)
		receiveErr = client.Receive(subCtx, client.B().Psubscribe().Pattern(pattern).Build(), func(m rueidis.PubSubMessage) {
			select {
			case received <- m.Channel:
			default:
			}
		})
	}()
	select {
	case <-subscribed:
	case <-finished:
		t.Fatalf("psubscribe: %v", receiveErr)
	}
	require.NoError(t, client.Do(ctx, client.B().Publish().Channel(channel).Message("x").Build()).Error())
	// A sentinel channel that always matches avoids waiting for a timeout on a non-match.
	sentinel := strings.TrimSuffix(strings.ReplaceAll(pattern, `\`, ""), "*") + "sentinel"
	require.NoError(t, client.Do(ctx, client.B().Publish().Channel(sentinel).Message("x").Build()).Error())
	select {
	case got := <-received:
		return got == channel
	case <-ctx.Done():
		t.Fatal("no pubsub message received")
		return false
	}
}

func TestNormalizeSessionKey(t *testing.T) {
	hashed := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])[:32]
	}
	long := strings.Repeat("a", 65)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "user-42", "user-42"},
		{"all allowed characters", "AZaz09_.:-", "AZaz09_.:-"},
		{"empty stays empty", "", ""},
		{"exactly 64 characters", strings.Repeat("b", 64), strings.Repeat("b", 64)},
		{"65 characters hashed", long, hashed(long)},
		{"space hashed", "user 42", hashed("user 42")},
		{"slash hashed", "a/b", hashed("a/b")},
		{"brace hashed", "{s1}", hashed("{s1}")},
		{"unicode hashed", "用户", hashed("用户")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeSessionKey(tt.in)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len(got), 64)
		})
	}
}

func BenchmarkKeysReady(b *testing.B) {
	k := NewKeys("sp")
	for b.Loop() {
		_ = k.Ready(123456, 7890)
	}
}
