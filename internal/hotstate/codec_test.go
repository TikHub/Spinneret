package hotstate

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

func TestHealthCodec(t *testing.T) {
	tests := []struct {
		name   string
		packed string
		want   healthEntry
	}{
		{"full", "55.25|1000|3|2|900|5000|6000|700", healthEntry{55.25, 1000, 3, 2, 900, 5000, 6000, 700}},
		{"missing", "", healthEntry{Score: 70, ScoreTS: 42}},
		{"short", "12.5|7", healthEntry{Score: 12.5, ScoreTS: 7}},
		{"malformed", "x|y|z|1.9|NaN|Inf|-|8", healthEntry{Score: 70, ScoreTS: 42, NFail: 1, LastUsed: 8}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, parseHealth(tc.packed, 70, 42))
		})
	}
	h := healthEntry{Score: 55.256, ScoreTS: 1, Samples: 2, NFail: 3, LastFail: 4, Cooldown: 5, Reuse: 6, LastUsed: 7}
	require.Equal(t, "55.26|1|2|3|4|5|6|7", packHealth(h))
	require.Equal(t, "0.00|0|0|0|0|0|0|0", packHealth(healthEntry{Score: -0.001}))
	require.Equal(t, "0.00", formatScore(-0.001))
	require.Equal(t, "12.50", formatScore(12.5))
}

func TestDecayScore(t *testing.T) {
	tau := time.Hour
	require.Equal(t, 30.0, decayScore(30, 1000, 1000, 70, tau))
	require.Equal(t, 30.0, decayScore(30, 2000, 1000, 70, tau), "no decay backwards in time")
	require.Equal(t, 30.0, decayScore(30, 0, 1000, 70, 0), "no decay without tau")
	require.InDelta(t, 70+(30-70)*math.Exp(-1), decayScore(30, 0, tau.Milliseconds(), 70, tau), 1e-9)
}

func TestParsers(t *testing.T) {
	require.Equal(t, int64(5), parseInt("", 5))
	require.Equal(t, int64(12), parseInt("12", 0))
	require.Equal(t, int64(12), parseInt("12.9", 0))
	require.Equal(t, int64(3), parseInt("abc", 3))
	require.Equal(t, int64(3), parseInt("NaN", 3))
	require.Equal(t, 1.5, parseFloat("1.5", 0))
	require.Equal(t, 2.0, parseFloat("", 2))
	require.Equal(t, 2.0, parseFloat("Inf", 2))
	require.Equal(t, []int64{1, 3}, parseCSVInt64("1,x,3"))
	require.Nil(t, parseCSVInt64(""))
	require.Equal(t, "1,2,3", csvInt64([]int64{1, 2, 3}))
	require.Equal(t, []int64{1, 2, 5}, sortedInt64([]int64{5, 1, 2, 5, 1}))
	require.Equal(t, []string{"a", "b"}, dedupeStrings([]string{"a", "", "b", "a"}))
	require.Equal(t, [][]string{{"a", "b"}, {"c"}}, chunkStrings([]string{"a", "b", "c"}, 2))
	require.Nil(t, chunkStrings(nil, 2))
	require.Equal(t, [][]int64{{1}, {2}}, chunkInt64([]int64{1, 2}, 1))
	require.Nil(t, chunkInt64(nil, 2))
	require.Equal(t, ",a,b,", tagList([]string{" a ", "", "b"}))
	require.Equal(t, "", tagList(nil))
	require.True(t, msToTime(0).IsZero())
	require.Nil(t, msToTimePtr(-1))
	require.Equal(t, int64(1234), msToTimePtr(1234).UnixMilli())
	require.Equal(t, int64(0), timeMs(nil))
	k, ok := parseKey("17")
	require.True(t, ok)
	require.Equal(t, int64(17), k)
	for _, v := range []string{"", "0", "-3", "x"} {
		_, ok := parseKey(v)
		require.False(t, ok, v)
	}
	one := "1"
	require.Equal(t, int64(1), parseOptInt(&one, 9))
	require.Equal(t, int64(9), parseOptInt(nil, 9))
}

func TestStateEncodings(t *testing.T) {
	until := time.UnixMilli(5000)
	require.Equal(t, int64(-1), banUntilMs(stateBanned, nil))
	require.Equal(t, int64(5000), banUntilMs(stateBanned, &until))
	require.Equal(t, int64(0), banUntilMs(stateActive, &until))
	require.Equal(t, int64(5000), quarantineUntilMs(stateQuar, &until))
	require.Equal(t, int64(0), quarantineUntilMs(stateQuar, nil))
	require.Equal(t, int64(0), quarantineUntilMs(stateActive, &until))
	require.Equal(t, "1", boolArg(true))
	require.Equal(t, "0", boolArg(false))
}

func TestParseDirty(t *testing.T) {
	tests := []struct {
		in   string
		want dirtyEntry
		ok   bool
	}{
		{"e12:34", dirtyEntry{kind: 'e', group: 12, key: 34}, true},
		{"g7", dirtyEntry{kind: 'g', key: 7}, true},
		{"p9", dirtyEntry{kind: 'p', key: 9}, true},
		{"e12", dirtyEntry{}, false},
		{"e1:x", dirtyEntry{}, false},
		{"gx", dirtyEntry{}, false},
		{"z1", dirtyEntry{}, false},
		{"g", dirtyEntry{}, false},
	}
	for _, tc := range tests {
		got, ok := parseDirty(tc.in)
		require.Equal(t, tc.ok, ok, tc.in)
		require.Equal(t, tc.want, got, tc.in)
	}
}

func TestKeyHelpers(t *testing.T) {
	require.Equal(t, `a\*b\?c\[d\]e\\f`, escapeGlob(`a*b?c[d]e\f`))
	require.Equal(t, "plain", escapeGlob("plain"))

	site, rest, ok := siteKeyOf("sp", "sp:{s12}:px:5")
	require.True(t, ok)
	require.Equal(t, int64(12), site)
	require.Equal(t, "px:5", rest)
	for _, k := range []string{"other:{s1}:x", "sp:{sx}:x", "sp:{s1", "sp:{s}:x"} {
		_, _, ok := siteKeyOf("sp", k)
		require.False(t, ok, k)
	}

	s := &Syncer{keys: redis.Keys{}}
	require.Equal(t, redis.DefaultPrefix, s.keyPrefix())
	s.keys = redis.NewKeys("custom")
	require.Equal(t, "custom", s.keyPrefix())
}

func TestPlanAndHealthSelection(t *testing.T) {
	ns := catalogtest.NewNamespace("ten_1", "ns_1", "prod")
	site := catalogtest.AddSite(ns, "sit_1", "demo", 7, "web")
	typ := catalogtest.MustCompileType("ity_1", site.ID, 1, fmt.Sprintf(typeYAML, "cookie", "demo", "web"))
	catalogtest.AddIdentityType(site, typ)
	search := catalogtest.AddGroup(site, "eg_search", "web", "search", 71)
	search.IdentityTypeIDs = []string{"ity_1", "ity_missing"}

	plan := newGroupPlan(site)
	idx := plan.profileIndex("web", "ity_1")
	require.Equal(t, idx, plan.profileIndex("web", "ity_1"), "profiles are cached")
	require.True(t, plan.eligibleFor(idx, 71))
	require.False(t, plan.eligibleFor(idx, 999))
	require.False(t, plan.eligibleFor(-1, 71))
	require.False(t, plan.eligibleFor(100, 71))
	require.Equal(t, "cookie", plan.eligibleTypeNames(search))
	require.Equal(t, 0, plan.list[plan.removal].size()-len(plan.all))

	custom := catalogtest.DefaultPolicies()
	spec := policy.Default(policy.KindAction).(*policy.ActionSpec)
	spec.Health.Baseline = 50
	compiled, err := policy.CompileAction([]*policy.ActionSpec{spec})
	require.NoError(t, err)
	custom.Action = compiled
	catalogtest.SetPolicies(search, custom)

	groups := plan.groupsOfClient("web")
	require.Equal(t, policy.DefaultHealthBaseline, globalHealth(groups).Baseline, "the _default group wins")
	require.Equal(t, 50.0, globalHealth([]*catalog.EndpointGroup{search}).Baseline)
	require.Equal(t, policy.DefaultHealthBaseline, globalHealth(nil).Baseline)
	require.Equal(t, policy.DefaultHealthBaseline, healthOf(&catalog.EndpointGroup{}).Baseline)
	require.Equal(t, policy.DefaultHealthBaseline, defaultHealth().Baseline)
}

func TestSnapshotRunTrim(t *testing.T) {
	run := newSnapshotRun()
	for i := 0; i <= snapshotCacheLimit; i++ {
		run.identities[int64(i)] = "x"
	}
	run.trim()
	require.Empty(t, run.identities)
	run.proxies[1] = "p"
	run.trim()
	require.Len(t, run.proxies, 1)
}
