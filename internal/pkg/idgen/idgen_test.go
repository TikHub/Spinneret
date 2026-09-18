package idgen

import (
	"errors"
	"math"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var idPattern = regexp.MustCompile(`^[a-z]+_[0-9a-f]{32}$`)

func TestNewAndValid(t *testing.T) {
	t.Parallel()
	prefixes := []string{
		Tenant, Namespace, Site, EndpointGroup, URIRule, IdentityType, Identity, Account, Proxy, Policy,
		PolicyBinding, ConfigItem, Secret, Token, User, RoleBinding, StateEvent, Audit, BreakerEvent,
		Channel, Alert, RiskEvent, Lease,
	}
	seenPrefix := map[string]bool{}
	for _, p := range prefixes {
		require.False(t, seenPrefix[p], "duplicate prefix %s", p)
		seenPrefix[p] = true

		id := New(p)
		require.Regexp(t, idPattern, id)
		require.True(t, strings.HasPrefix(id, p+"_"))
		require.True(t, Valid(id, p), id)
		// The UUID must be version 7.
		require.Equal(t, byte('7'), id[len(p)+1+12], id)
	}
}

func TestNewIsUniqueAndOrdered(t *testing.T) {
	t.Parallel()
	const n = 5000
	seen := make(map[string]struct{}, n)
	prev := ""
	for range n {
		id := New(Identity)
		_, dup := seen[id]
		require.False(t, dup, id)
		seen[id] = struct{}{}
		require.Greater(t, id, prev, "UUIDv7 ids must be monotonically increasing")
		prev = id
	}
}

func TestValid(t *testing.T) {
	t.Parallel()
	good := "sit_0192a3f4c1d27b8e9a01f2c3d4e5a6b7"
	tests := []struct {
		name   string
		id     string
		prefix string
		want   bool
	}{
		{name: "good", id: good, prefix: Site, want: true},
		{name: "wrong prefix", id: good, prefix: Identity, want: false},
		{name: "prefix is substring", id: "site_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", prefix: Site, want: false},
		{name: "uppercase hex", id: "sit_0192A3F4C1D27B8E9A01F2C3D4E5A6B7", prefix: Site, want: false},
		{name: "short", id: "sit_0192a3f4", prefix: Site, want: false},
		{name: "long", id: good + "0", prefix: Site, want: false},
		{name: "non hex", id: "sit_0192a3f4c1d27b8e9a01f2c3d4e5a6bz", prefix: Site, want: false},
		{name: "empty", id: "", prefix: Site, want: false},
		{name: "prefix only", id: "sit_", prefix: Site, want: false},
		{name: "missing underscore", id: "sit0192a3f4c1d27b8e9a01f2c3d4e5a6b7", prefix: Site, want: false},
		{name: "dashed uuid", id: "sit_0192a3f4-c1d2-7b8e-9a01-f2c3d4e5a6b7", prefix: Site, want: false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, Valid(tt.id, tt.prefix), tt.name)
	}
}

func TestTime(t *testing.T) {
	t.Parallel()
	before := time.Now().Add(-time.Millisecond)
	id := New(Audit)
	after := time.Now().Add(time.Millisecond)
	ts, ok := Time(id)
	require.True(t, ok)
	require.Equal(t, time.UTC, ts.Location())
	require.False(t, ts.Before(before.Truncate(time.Millisecond)), "%s before %s", ts, before)
	require.False(t, ts.After(after), "%s after %s", ts, after)

	// Known value: 0x0192a3f4c1d2 ms since epoch.
	known, ok := Time("evt_0192a3f4c1d27b8e9a01f2c3d4e5a6b7")
	require.True(t, ok)
	require.Equal(t, time.UnixMilli(0x0192a3f4c1d2).UTC(), known)

	leaseTS, ok := Time(LeasePrefix(42) + "0f")
	require.True(t, ok)
	require.WithinDuration(t, time.Now(), leaseTS, 5*time.Second)

	for _, bad := range []string{"", "evt", "evt_", "evt_0192a3f4c1d2", "evt_0192A3F4C1D27B8E9A01F2C3D4E5A6B7", "evt_zz92a3f4c1d27b8e9a01f2c3d4e5a6b7"} {
		_, ok := Time(bad)
		require.False(t, ok, bad)
	}
}

func TestFormatShard(t *testing.T) {
	t.Parallel()
	require.Equal(t, "00", FormatShard(0))
	require.Equal(t, "0f", FormatShard(15))
	require.Equal(t, "10", FormatShard(16))
	require.Equal(t, "ff", FormatShard(255))
}

func TestLeaseIDRoundTrip(t *testing.T) {
	t.Parallel()
	siteKeys := []int64{
		1, 2, 9, 10, 35, 36, 37, 1295, 1296, 46655, 46656,
		math.MaxInt32 - 1, math.MaxInt32, math.MaxInt32 + 1, 1 << 32,
		1<<40 - 1, 1 << 40, math.MaxInt64,
	}
	rng := rand.New(rand.NewPCG(7, 11))
	for range 500 {
		siteKeys = append(siteKeys, 1+rng.Int64N(1<<40))
	}
	for i, siteKey := range siteKeys {
		prefix := LeasePrefix(siteKey)
		require.True(t, strings.HasPrefix(prefix, "lse_"), prefix)
		require.True(t, strings.HasSuffix(prefix, "_"+strconv.FormatInt(siteKey, 36)+"_"), prefix)
		shards := []int{0, 1, 15, 16, 127, 254, 255}
		if i < 4 {
			shards = shards[:0]
			for s := range 256 {
				shards = append(shards, s)
			}
		}
		for _, shard := range shards {
			id := prefix + FormatShard(shard)
			ref, err := ParseLeaseID(id)
			require.NoError(t, err, id)
			require.Equal(t, LeaseRef{SiteKey: siteKey, Shard: shard}, ref, id)
		}
	}
	require.NotEqual(t, LeasePrefix(5), LeasePrefix(5), "each prefix carries a fresh uuid")
}

func TestParseLeaseIDExample(t *testing.T) {
	t.Parallel()
	ref, err := ParseLeaseID("lse_0192a3f4c1d27b8e9a01f2c3d4e5a6b7_1a_0f")
	require.NoError(t, err)
	require.Equal(t, LeaseRef{SiteKey: 46, Shard: 15}, ref)
}

func TestParseLeaseIDMalformed(t *testing.T) {
	t.Parallel()
	const hex = "0192a3f4c1d27b8e9a01f2c3d4e5a6b7"
	tests := []struct {
		name string
		id   string
	}{
		{name: "empty", id: ""},
		{name: "prefix only", id: "lse_"},
		{name: "wrong prefix", id: "lsx_" + hex + "_1a_0f"},
		{name: "other entity id", id: "idt_" + hex + "_1a_0f"},
		{name: "uppercase prefix", id: "LSE_" + hex + "_1a_0f"},
		{name: "missing shard", id: "lse_" + hex + "_1a_"},
		{name: "missing shard and separator", id: "lse_" + hex + "_1a"},
		{name: "missing site", id: "lse_" + hex + "__0f"},
		{name: "short hex", id: "lse_" + hex[:31] + "_1a_0f"},
		{name: "long hex", id: "lse_" + hex + "0_1a_0f"},
		{name: "uppercase hex", id: "lse_" + strings.ToUpper(hex) + "_1a_0f"},
		{name: "non hex uuid", id: "lse_" + hex[:31] + "g_1a_0f"},
		{name: "site zero", id: "lse_" + hex + "_0_0f"},
		{name: "site negative", id: "lse_" + hex + "_-1_0f"},
		{name: "site plus sign", id: "lse_" + hex + "_+1_0f"},
		{name: "site leading zero", id: "lse_" + hex + "_01a_0f"},
		{name: "site uppercase", id: "lse_" + hex + "_1A_0f"},
		{name: "site invalid char", id: "lse_" + hex + "_1-a_0f"},
		{name: "site too long", id: "lse_" + hex + "_10000000000000_0f"},
		{name: "site overflow", id: "lse_" + hex + "_1y2p0ij32e8e8_0f"},
		{name: "shard one digit", id: "lse_" + hex + "_1a_f"},
		{name: "shard three digits", id: "lse_" + hex + "_1a_100"},
		{name: "shard uppercase", id: "lse_" + hex + "_1a_0F"},
		{name: "shard non hex", id: "lse_" + hex + "_1a_zz"},
		{name: "extra segment", id: "lse_" + hex + "_1a_0f_00"},
		{name: "trailing space", id: "lse_" + hex + "_1a_0f "},
		{name: "prefix without shard", id: LeasePrefix(10)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref, err := ParseLeaseID(tt.id)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrInvalidLeaseID))
			require.Equal(t, LeaseRef{}, ref)
		})
	}
}

func TestValidReportID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id   string
		want bool
	}{
		{id: "a", want: true},
		{id: "0192a3f4-c1d2-7b8e-9a01-f2c3d4e5a6b7", want: true},
		{id: "node-1:batch.42_x", want: true},
		{id: "ABCxyz019", want: true},
		{id: strings.Repeat("a", 64), want: true},
		{id: "", want: false},
		{id: strings.Repeat("a", 65), want: false},
		{id: "has space", want: false},
		{id: "slash/id", want: false},
		{id: "semi;colon", want: false},
		{id: "quote'", want: false},
		{id: "unicode-é", want: false},
		{id: "newline\n", want: false},
		{id: "null\x00", want: false},
		{id: "star*", want: false},
		{id: "curly{r1}", want: false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, ValidReportID(tt.id), "%q", tt.id)
	}
}
