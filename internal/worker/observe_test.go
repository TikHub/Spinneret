package worker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/signal"
)

// withOutcome returns an event mutated to classify as outcome under the
// built-in signal policy.
func withOutcome(ev signal.Event, outcome string) signal.Event {
	ev.HTTPStatus, ev.ErrorKind, ev.Markers = 200, "", []string{}
	switch outcome {
	case policy.OutcomeEmpty:
		ev.Markers = []string{"empty_list"}
	case policy.OutcomeRateLimited:
		ev.HTTPStatus = 429
	case policy.OutcomeCaptcha:
		ev.Markers = []string{"captcha_page"}
	case policy.OutcomeAuthInvalid:
		ev.HTTPStatus = 401
	case policy.OutcomeForbidden:
		ev.HTTPStatus = 403
	case policy.OutcomeProxyError:
		ev.HTTPStatus, ev.ErrorKind = 0, "proxy_auth"
	case policy.OutcomeNetworkError:
		ev.HTTPStatus, ev.ErrorKind = 0, "timeout"
	case policy.OutcomeTargetError:
		ev.HTTPStatus = 503
	case policy.OutcomeClientError:
		ev.HTTPStatus = 400
	case policy.OutcomeUnknown:
		ev.HTTPStatus = 302
	}
	return ev
}

func TestObserveEffectsPerOutcome(t *testing.T) {
	tests := []struct {
		outcome        string
		identityScore  float64 // 0 = no hs entry written
		nfail          int64
		globalScore    string
		proxyScore     string
		proxyNfail     string
		windowSuccess  string
		windowRisk     string
		dirtyIdentity  bool
		dirtyProxy     bool
		expectedBlame  string
		expectedSample int64
	}{
		{policy.OutcomeSuccess, 73, 0, "73.00", "73.00", "", "1", "", true, true, "none", 1},
		{policy.OutcomeEmpty, 69, 1, "69.00", "", "", "", "", true, false, "identity", 1},
		{policy.OutcomeRateLimited, 66, 1, "66.00", "66.00", "1", "", "1", true, true, "both", 1},
		{policy.OutcomeCaptcha, 63, 1, "63.00", "", "", "", "1", true, false, "identity", 1},
		{policy.OutcomeAuthInvalid, 70, 1, "", "", "", "", "", true, false, "identity", 0},
		{policy.OutcomeForbidden, 64, 1, "64.00", "", "", "", "1", true, false, "identity", 1},
		{policy.OutcomeProxyError, 0, 0, "", "63.00", "1", "", "", false, true, "proxy", 0},
		{policy.OutcomeNetworkError, 0, 0, "", "67.00", "1", "", "", false, true, "proxy", 0},
		{policy.OutcomeTargetError, 0, 0, "", "", "", "", "", false, false, "none", 0},
		{policy.OutcomeClientError, 0, 0, "", "", "", "", "", false, false, "none", 0},
		{policy.OutcomeUnknown, 0, 0, "", "", "", "", "", false, false, "none", 0},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			f := newFixture(t)
			f.w.exec = nil
			f.identity(t, 1)
			f.proxy(t, 9)
			lease := f.lease(t, 1, 9)
			f.process(t, withOutcome(f.event(lease), tt.outcome))

			rec := f.lastRecord(t)
			require.Equal(t, tt.outcome, rec.Outcome)
			require.Equal(t, tt.expectedBlame, rec.Blame)
			require.Equal(t, "web_cookie", rec.IdentityType)
			require.Equal(t, "eg_search", rec.EndpointGroupID)

			score, sts, samples, nfail, lastfail := f.hs(t, 1)
			require.InDelta(t, tt.identityScore, score, 0.001)
			require.Equal(t, tt.nfail, nfail)
			require.Equal(t, tt.expectedSample, samples)
			if tt.identityScore != 0 && tt.expectedSample > 0 {
				require.Equal(t, f.now.UnixMilli(), sts)
			}
			if tt.nfail > 0 {
				require.Equal(t, f.now.UnixMilli(), lastfail)
				require.Equal(t, "1", f.hget(t, f.keys.Identity(testSiteKey, 1), "gnf"))
			}
			require.Equal(t, tt.globalScore, f.hget(t, f.keys.Identity(testSiteKey, 1), "gs"))
			require.Equal(t, tt.proxyScore, f.hget(t, f.keys.ProxySite(testSiteKey, 9), "sc"))
			require.Equal(t, tt.proxyNfail, f.hget(t, f.keys.ProxySite(testSiteKey, 9), "nf"))

			bucket := f.now.UnixMilli() / 5000
			win := f.keys.Window(testSiteKey, testGroupKey, bucket)
			require.Equal(t, "1", f.hget(t, win, "t"))
			require.Equal(t, tt.windowSuccess, f.hget(t, win, "s"))
			require.Equal(t, tt.windowRisk, f.hget(t, win, "r"))

			dirty := f.smembers(t, f.keys.Dirty(testSiteKey))
			require.Equal(t, tt.dirtyIdentity, contains(dirty, "e4201:1"), dirty)
			require.Equal(t, tt.dirtyProxy, contains(dirty, "p9"), dirty)
			require.Equal(t, "1", f.hget(t, f.keys.Lease(testSiteKey, lease), "rc"))
		})
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestObserveStreakHalvingAndReset(t *testing.T) {
	f := newFixture(t)
	f.w.exec = nil
	f.identity(t, 1)
	f.proxy(t, 9)
	lease := f.lease(t, 1, 9)
	for range 4 {
		f.process(t, withOutcome(f.event(lease), policy.OutcomeRateLimited))
		f.now = f.now.Add(time.Second)
	}
	_, _, _, nfail, _ := f.hs(t, 1)
	require.EqualValues(t, 4, nfail)
	require.Equal(t, "4", f.hget(t, f.keys.Identity(testSiteKey, 1), "gnf"))
	require.Equal(t, "4", f.hget(t, f.keys.ProxySite(testSiteKey, 9), "nf"))

	f.process(t, withOutcome(f.event(lease), policy.OutcomeSuccess))
	_, _, _, nfail, _ = f.hs(t, 1)
	require.EqualValues(t, 2, nfail)
	require.Equal(t, "2", f.hget(t, f.keys.Identity(testSiteKey, 1), "gnf"))
	require.Equal(t, "2", f.hget(t, f.keys.ProxySite(testSiteKey, 9), "nf"))

	// failure_reset_after of the built-in cooldown rules is 1h.
	f.now = f.now.Add(61 * time.Minute)
	f.process(t, withOutcome(f.event(lease), policy.OutcomeRateLimited))
	_, _, _, nfail, lastfail := f.hs(t, 1)
	require.EqualValues(t, 1, nfail)
	require.Equal(t, f.now.UnixMilli(), lastfail)
	require.Equal(t, "1", f.hget(t, f.keys.Identity(testSiteKey, 1), "gnf"))
	require.Equal(t, strconv.FormatInt(f.now.UnixMilli(), 10), f.hget(t, f.keys.Identity(testSiteKey, 1), "glf"))
	require.Equal(t, "1", f.hget(t, f.keys.ProxySite(testSiteKey, 9), "nf"))
}

// TestObserveKeepsCooldownReuseAndLastUse pins the splice of the packed hs
// entry: observe.lua rewrites the score, the timestamp, the sample count and
// the failure streak and carries "cd", "ru" and "lu" back byte for byte, and
// the reply reports the cooldown it found.
func TestObserveKeepsCooldownReuseAndLastUse(t *testing.T) {
	f := newFixture(t)
	f.w.exec = nil
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	cd := f.now.UnixMilli() + 300_000
	ru := f.now.UnixMilli() + 60_000
	lu := f.now.UnixMilli() - 1_000
	seeded := fmt.Sprintf("63.25|%d|7|3|%d|%d|%d|%d", f.now.UnixMilli()-5_000, f.now.UnixMilli()-5_000, cd, ru, lu)
	f.hset(t, f.keys.Health(testSiteKey, testGroupKey), strconv.FormatInt(1, 10), seeded)

	f.process(t, withOutcome(f.event(lease), policy.OutcomeSuccess))

	stored := f.hget(t, f.keys.Health(testSiteKey, testGroupKey), "1")
	fields := strings.Split(stored, "|")
	require.Len(t, fields, 8, stored)
	require.Equal(t, strconv.FormatInt(f.now.UnixMilli(), 10), fields[1], "sts is now")
	require.Equal(t, "8", fields[2], "samples counted")
	require.Equal(t, "1", fields[3], "streak halved")
	require.Equal(t, strconv.FormatInt(f.now.UnixMilli()-5_000, 10), fields[4], "lastfail untouched")
	require.Equal(t, strconv.FormatInt(cd, 10), fields[5], "cd untouched")
	require.Equal(t, strconv.FormatInt(ru, 10), fields[6], "ru untouched")
	require.Equal(t, strconv.FormatInt(lu, 10), fields[7], "lu untouched")
	score, err := strconv.ParseFloat(fields[0], 64)
	require.NoError(t, err)
	require.InDelta(t, 0.1*100+0.9*63.25, score, 0.02)

	// The reply carries the cooldown it found, which the action policy uses as
	// EndpointCooldownRemaining.
	obs, err := f.w.observe(context.Background(), testSiteKey, observeParams{
		shard: 0, streamID: f.streamID(), now: f.now, leaseID: lease,
		group: f.site.GroupsByKey[int64(testGroupKey)], action: f.w.fallbackAction,
		identityKey: 1, outcome: policy.OutcomeSuccess, blame: policy.BlameNone,
	})
	require.NoError(t, err)
	require.Equal(t, cd, obs.endpointCooldownUntil)
}

func TestObserveScoreDecay(t *testing.T) {
	f := newFixture(t)
	f.w.exec = nil
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	f.process(t, withOutcome(f.event(lease), policy.OutcomeCaptcha)) // 63
	// After one tau (6h) the distance to the baseline shrinks by e.
	f.now = f.now.Add(6 * time.Hour)
	f.process(t, withOutcome(f.event(lease), policy.OutcomeSuccess))
	decayed := 70 + (63-70)*0.36787944117
	score, _, samples, _, _ := f.hs(t, 1)
	require.InDelta(t, 0.1*100+0.9*decayed, score, 0.01)
	require.EqualValues(t, 2, samples)
	require.Equal(t, "2", f.hget(t, f.keys.Identity(testSiteKey, 1), "gn"))
}

func TestObserveQuotaAndReportCount(t *testing.T) {
	f := newFixture(t)
	f.w.exec = nil
	rot := *f.group.Rotation
	rot.Rotation.Quota = []policy.QuotaSpec{{Limit: 10, Window: durationx.MustParse("1m")}}
	f.group.Rotation = &rot
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	qkey := f.keys.Quota(testSiteKey, testGroupKey, 1)
	idx := f.now.UnixMilli() / 60000
	f.hset(t, qkey, "60000:c", strconv.FormatInt(idx, 10), "60000:n", "1") // counted at acquire

	f.process(t, f.event(lease)) // rc 1: first request already counted
	require.Equal(t, "1", f.hget(t, qkey, "60000:n"))
	f.process(t, f.event(lease)) // rc 2
	require.Equal(t, "2", f.hget(t, qkey, "60000:n"))
	require.Equal(t, "2", f.hget(t, f.keys.Lease(testSiteKey, lease), "rc"))

	f.now = f.now.Add(time.Minute)
	f.process(t, f.event(lease))
	require.Equal(t, strconv.FormatInt(idx+1, 10), f.hget(t, qkey, "60000:c"))
	require.Equal(t, "2", f.hget(t, qkey, "60000:p"))
	require.Equal(t, "1", f.hget(t, qkey, "60000:n"))

	f.now = f.now.Add(5 * time.Minute)
	f.process(t, f.event(lease))
	require.Equal(t, "0", f.hget(t, qkey, "60000:p"))
	require.Equal(t, "1", f.hget(t, qkey, "60000:n"))
	ttl, err := f.rdb.Do(context.Background(), f.rdb.B().Pttl().Key(qkey).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, ttl, int64(0))
	require.LessOrEqual(t, ttl, int64(120000))
}

func TestObserveCountersOverWindows(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	empty := func() {
		f.process(t, withOutcome(f.event(lease), policy.OutcomeEmpty))
	}
	counter := f.keys.Counter(testSiteKey, "i1", policy.OutcomeEmpty, (10 * time.Minute).Milliseconds())

	empty()
	f.now = f.now.Add(4 * time.Minute)
	empty()
	require.Empty(t, f.exec.snapshot(), "two empties do not reach the count condition")
	f.now = f.now.Add(4 * time.Minute)
	empty()
	calls := f.exec.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, policy.ActionCooldown, calls[0].Planned[0].Action)
	require.Equal(t, policy.ScopeIdentityEndpoint, calls[0].Planned[0].Scope)
	require.Equal(t, 5*time.Minute, calls[0].Planned[0].Duration)

	// 11 minutes after the first report only the last two remain in the window.
	f.now = f.now.Add(3 * time.Minute)
	res := f.observeDirect(t, lease, policy.OutcomeEmpty, policy.BlameIdentity)
	require.Equal(t, []int64{3}, res.counts)
	hlen, err := f.rdb.Do(context.Background(), f.rdb.B().Hlen().Key(counter).Build()).AsInt64()
	require.NoError(t, err)
	require.EqualValues(t, 3, hlen, "stale buckets are pruned")
	ttl, err := f.rdb.Do(context.Background(), f.rdb.B().Pttl().Key(counter).Build()).AsInt64()
	require.NoError(t, err)
	require.LessOrEqual(t, ttl, (10*time.Minute + 10*time.Second).Milliseconds())
}

// observeDirect runs observe.lua with the search group policies.
func (f *fixture) observeDirect(t *testing.T, lease, outcome string, blame policy.Blame) observeResult {
	t.Helper()
	res, err := f.w.observe(context.Background(), testSiteKey, f.params(lease, outcome, blame))
	require.NoError(t, err)
	return res
}

func (f *fixture) params(lease, outcome string, blame policy.Blame) observeParams {
	info, _ := f.w.loadLease(context.Background(), testSiteKey, lease)
	p := observeParams{
		shard: 0, streamID: f.streamID(), now: f.now, leaseID: lease, group: f.group, action: f.group.Action,
		outcome: outcome, blame: blame,
		counters: f.group.Action.CounterRequests(outcome), banWindows: f.group.Action.EscalationWindows(),
	}
	if info != nil {
		p.identityKey, p.proxyKey = info.identityKey, info.proxyKey
	}
	return p
}

func TestObserveCounterSubjects(t *testing.T) {
	f := newFixture(t)
	f.setPolicies(t, `
name: subjects
rules:
  - when: { outcome: captcha, count: { gte: 5, within: 1h } }
    action: ban
    scope: account
    duration: 1h
  - when: { outcome: captcha, count: { gte: 5, within: 1h } }
    action: ban
    scope: identity
    duration: 1h
  - when: { outcome: captcha, count: { gte: 5, within: 30s } }
    action: cooldown
    scope: proxy_site
    base: 1m
escalation:
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
`)
	f.identity(t, 1)
	f.identity(t, 2, "acc", "77")
	f.proxy(t, 9)
	noAccount := f.lease(t, 1, 9)
	withAccount := f.lease(t, 2, 0)
	bans := f.keys.Bans(testSiteKey, 1)
	for _, ago := range []time.Duration{24 * time.Hour, 8 * 24 * time.Hour, 40 * 24 * time.Hour} {
		ms := f.now.Add(-ago).UnixMilli()
		require.NoError(t, f.rdb.Do(context.Background(), f.rdb.B().Zadd().Key(bans).ScoreMember().
			ScoreMember(float64(ms), strconv.FormatInt(ms, 10)).Build()).Error())
	}

	res := f.observeDirect(t, noAccount, policy.OutcomeCaptcha, policy.BlameIdentity)
	// Counter requests: account 1h (falls back to the identity key and shares
	// its counter with the identity rule), identity 1h, proxy 30s.
	require.Equal(t, []int64{1, 1, 1}, res.counts)
	require.Equal(t, []int64{1, 2}, res.banCounts)
	require.Zero(t, res.accountKey)

	res = f.observeDirect(t, withAccount, policy.OutcomeCaptcha, policy.BlameIdentity)
	require.Equal(t, []int64{1, 1, 0}, res.counts, "no proxy: proxy counter is 0")
	require.EqualValues(t, 77, res.accountKey)
	for _, key := range []string{
		f.keys.Counter(testSiteKey, "i1", policy.OutcomeCaptcha, time.Hour.Milliseconds()),
		f.keys.Counter(testSiteKey, "p9", policy.OutcomeCaptcha, (30 * time.Second).Milliseconds()),
		f.keys.Counter(testSiteKey, "a77", policy.OutcomeCaptcha, time.Hour.Milliseconds()),
		f.keys.Counter(testSiteKey, "i2", policy.OutcomeCaptcha, time.Hour.Milliseconds()),
	} {
		n, err := f.rdb.Do(context.Background(), f.rdb.B().Exists().Key(key).Build()).AsInt64()
		require.NoError(t, err)
		require.EqualValues(t, 1, n, key)
	}
}

func TestObserveCrossAttribution(t *testing.T) {
	f := newFixture(t)
	f.proxy(t, 9)
	var leases []string
	for i := int64(1); i <= 3; i++ {
		f.identity(t, i)
		leases = append(leases, f.lease(t, i, 9))
	}
	for i, lease := range leases {
		f.process(t, withOutcome(f.event(lease), policy.OutcomeRateLimited))
		f.now = f.now.Add(time.Second)
		if i < 2 {
			require.Equal(t, "both", f.lastRecord(t).Blame)
		}
	}
	require.Equal(t, "proxy", f.lastRecord(t).Blame, "3 distinct identities on the proxy blame the proxy")
	require.Len(t, f.exec.snapshot(), 2, "identity cooldown is not planned once the proxy is blamed")
	_, _, _, nfail3, _ := f.hs(t, 3)
	require.EqualValues(t, 1, nfail3, "failure outcomes always count against the identity streak")
	score3, _, samples3, _, _ := f.hs(t, 3)
	require.InDelta(t, 70, score3, 0.001, "identity score is untouched when only the proxy is blamed")
	require.Zero(t, samples3)
	require.Equal(t, "3", f.hget(t, f.keys.ProxySite(testSiteKey, 9), "nf"))

	ttl, err := f.rdb.Do(context.Background(), f.rdb.B().Pttl().Key(f.keys.CrossProxy(testSiteKey, 9)).Build()).AsInt64()
	require.NoError(t, err)
	require.LessOrEqual(t, ttl, (10 * time.Minute).Milliseconds())

	// Entries older than the window are trimmed: the proxy is no longer blamed.
	f.now = f.now.Add(11 * time.Minute)
	f.identity(t, 4)
	lease4 := f.lease(t, 4, 9)
	f.process(t, withOutcome(f.event(lease4), policy.OutcomeRateLimited))
	require.Equal(t, "both", f.lastRecord(t).Blame)
}

func TestObserveCrossAttributionIdentityOnManyProxies(t *testing.T) {
	tests := []struct {
		name  string
		blame policy.Blame
		want  policy.Blame
	}{
		{name: "proxy becomes both", blame: policy.BlameProxy, want: policy.BlameBoth},
		{name: "none becomes identity", blame: policy.BlameNone, want: policy.BlameIdentity},
		{name: "identity stays", blame: policy.BlameIdentity, want: policy.BlameIdentity},
		{name: "both stays", blame: policy.BlameBoth, want: policy.BlameBoth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.identity(t, 1)
			var res observeResult
			for p := int64(11); p <= 13; p++ {
				f.proxy(t, p)
				res = f.observeDirect(t, f.lease(t, 1, p), policy.OutcomeRateLimited, tt.blame)
			}
			require.Equal(t, tt.want, res.blame)
		})
	}
}

func TestObserveCrossAttributionDisabled(t *testing.T) {
	f := newFixture(t)
	f.setPolicies(t, "name: no-xa\ncross_attribution: { enabled: false }\n")
	f.proxy(t, 9)
	var res observeResult
	for i := int64(1); i <= 3; i++ {
		f.identity(t, i)
		res = f.observeDirect(t, f.lease(t, i, 9), policy.OutcomeCaptcha, policy.BlameIdentity)
	}
	require.Equal(t, policy.BlameIdentity, res.blame)
	n, err := f.rdb.Do(context.Background(), f.rdb.B().Exists().Key(f.keys.CrossProxy(testSiteKey, 9)).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestObserveBreakerWindowAndHLL(t *testing.T) {
	f := newFixture(t)
	f.w.exec = nil
	for i := int64(1); i <= 3; i++ {
		f.identity(t, i)
	}
	f.process(t, withOutcome(f.event(f.lease(t, 1, 0)), policy.OutcomeCaptcha))
	f.process(t, withOutcome(f.event(f.lease(t, 2, 0)), policy.OutcomeCaptcha))
	f.process(t, withOutcome(f.event(f.lease(t, 2, 0)), policy.OutcomeCaptcha))
	f.process(t, withOutcome(f.event(f.lease(t, 3, 0)), policy.OutcomeSuccess))

	ctx := context.Background()
	bucket := f.now.UnixMilli() / 5000
	win := f.keys.Window(testSiteKey, testGroupKey, bucket)
	require.Equal(t, "4", f.hget(t, win, "t"))
	require.Equal(t, "1", f.hget(t, win, "s"))
	require.Equal(t, "3", f.hget(t, win, "r"))
	ttl, err := f.rdb.Do(ctx, f.rdb.B().Pttl().Key(win).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, ttl, int64(60000))
	require.LessOrEqual(t, ttl, int64(120000))
	distinct, err := f.rdb.Do(ctx, f.rdb.B().Pfcount().Key(f.keys.WindowHLL(testSiteKey, testGroupKey, bucket)).Build()).AsInt64()
	require.NoError(t, err)
	require.EqualValues(t, 2, distinct)
	score, err := f.rdb.Do(ctx, f.rdb.B().Zscore().Key(f.keys.ActiveGroups(testSiteKey)).Member(strconv.Itoa(testGroupKey)).Build()).AsFloat64()
	require.NoError(t, err)
	require.InDelta(t, float64(f.now.UnixMilli()), score, 0)

	// A later bucket gets its own hash, and its own expiry: the TTL is written
	// when the bucket is created, not refreshed on every report.
	f.now = f.now.Add(5 * time.Second)
	f.process(t, withOutcome(f.event(f.lease(t, 3, 0)), policy.OutcomeSuccess))
	next := f.keys.Window(testSiteKey, testGroupKey, bucket+1)
	require.Equal(t, "1", f.hget(t, next, "t"))
	ttl, err = f.rdb.Do(ctx, f.rdb.B().Pttl().Key(next).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, ttl, int64(60000))
	require.LessOrEqual(t, ttl, int64(120000))
}

func TestObserveProbeStatsAndSuppression(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	brk := f.keys.Breaker(testSiteKey, testGroupKey)
	probe := f.lease(t, 1, 0, "pr", "1")
	regular := f.lease(t, 1, 0)

	f.hset(t, brk, "st", "half_open")
	f.process(t, withOutcome(f.event(probe), policy.OutcomeSuccess))
	f.process(t, withOutcome(f.event(probe), policy.OutcomeCaptcha))
	f.process(t, withOutcome(f.event(regular), policy.OutcomeSuccess))
	require.Equal(t, "2", f.hget(t, brk, "ps"))
	require.Equal(t, "1", f.hget(t, brk, "pk"))
	require.True(t, f.lastRecord(t).Probe == false)
	_, _, samples, _, _ := f.hs(t, 1)
	require.EqualValues(t, 3, samples, "half-open does not suppress")

	f.hset(t, brk, "st", "open", "ou", strconv.FormatInt(f.now.Add(time.Minute).UnixMilli(), 10))
	execBefore := len(f.exec.snapshot())
	f.process(t, withOutcome(f.event(regular), policy.OutcomeRateLimited))
	rec := f.lastRecord(t)
	require.True(t, rec.Suppressed)
	require.False(t, rec.Probe)
	require.Equal(t, policy.OutcomeRateLimited, rec.Outcome)
	_, _, samples, _, _ = f.hs(t, 1)
	require.EqualValues(t, 3, samples, "suppressed reports do not change health")
	require.Len(t, f.exec.snapshot(), execBefore, "no actions while suppressed")
	require.Len(t, f.brk.calls, 2, "risk outcomes still notify the breaker")
	bucket := f.now.UnixMilli() / 5000
	require.Equal(t, "4", f.hget(t, f.keys.Window(testSiteKey, testGroupKey, bucket), "t"), "window statistics still count")

	// A probe lease on an open breaker is not suppressed.
	f.process(t, withOutcome(f.event(probe), policy.OutcomeSuccess))
	rec = f.lastRecord(t)
	require.False(t, rec.Suppressed)
	require.True(t, rec.Probe)
	require.Equal(t, "2", f.hget(t, brk, "ps"), "probe stats are only counted while half-open")
}

func TestObserveIdempotentRedelivery(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	ev := withOutcome(f.event(lease), policy.OutcomeRateLimited)
	id := f.process(t, ev)
	f.processWithID(t, ev, id)
	f.processWithID(t, ev, "1-0") // older than the checkpoint

	_, _, samples, nfail, _ := f.hs(t, 1)
	require.EqualValues(t, 1, samples)
	require.EqualValues(t, 1, nfail)
	require.Len(t, f.rec.snapshot(), 1)
	require.Len(t, f.exec.snapshot(), 1)
	require.Equal(t, "1", f.hget(t, f.keys.Lease(testSiteKey, lease), "rc"))
	require.Equal(t, id, f.hget(t, f.keys.Checkpoint(testSiteKey), "1"), "identity 1 maps to shard 1")

	// Same millisecond, higher sequence is newer.
	ms, _, _ := cutStreamID(id)
	f.processWithID(t, withOutcome(f.event(lease), policy.OutcomeSuccess), ms+"-999999")
	_, _, samples, _, _ = f.hs(t, 1)
	require.EqualValues(t, 2, samples)
}

func cutStreamID(id string) (ms, seq string, ok bool) {
	for i := 0; i < len(id); i++ {
		if id[i] == '-' {
			return id[:i], id[i+1:], true
		}
	}
	return id, "", false
}

func TestObserveMissingIdentityAndProxy(t *testing.T) {
	f := newFixture(t)
	lease := f.lease(t, 5, 8) // neither identity 5 nor proxy 8 exist
	res := f.observeDirect(t, lease, policy.OutcomeSuccess, policy.BlameNone)
	require.Empty(t, res.identityState)
	require.Empty(t, f.hget(t, f.keys.Health(testSiteKey, testGroupKey), "5"))
	n, err := f.rdb.Do(context.Background(), f.rdb.B().Exists().Key(f.keys.ProxySite(testSiteKey, 8)).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n, "observe never creates a proxy hash")
	n, err = f.rdb.Do(context.Background(), f.rdb.B().Exists().Key(f.keys.Identity(testSiteKey, 5)).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n, "observe never creates an identity hash")
}

func TestObserveReplyParsing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// A checkpoint-only call on a fresh site reports not duplicated, then duplicated.
	dup, err := f.w.checkpoint(ctx, testSiteKey, 1, "100-1")
	require.NoError(t, err)
	require.False(t, dup)
	dup, err = f.w.checkpoint(ctx, testSiteKey, 1, "100-1")
	require.NoError(t, err)
	require.True(t, dup)
	dup, err = f.w.checkpoint(ctx, testSiteKey, 1, "99")
	require.NoError(t, err)
	require.True(t, dup)
	dup, err = f.w.checkpoint(ctx, testSiteKey, 2, "99")
	require.NoError(t, err)
	require.False(t, dup, "checkpoints are per shard")

	_, err = parseObserveReply(f.rdb.Do(ctx, f.rdb.B().Ping().Build()), 0, 0)
	require.Error(t, err)
}
