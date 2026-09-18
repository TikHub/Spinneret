package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/proxy/proxydb"
)

func TestPlanOperation(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	banUntil := now.Add(time.Hour)
	row := func(state string) proxydb.Proxy {
		return proxydb.Proxy{ID: "pxy_1", State: state, StateReason: "old", ConsecutiveCheckFailures: 4, BanUntil: &banUntil, NextCheckAt: now.Add(time.Hour)}
	}
	hour := durationx.Duration(time.Hour)
	tests := []struct {
		name      string
		state     string
		req       OperationRequest
		site      bool
		to        string
		write     bool
		event     bool
		errPart   string
		until     *time.Time
		permanent bool
		check     func(t *testing.T, p opPlan)
	}{
		{name: "disable active", state: StateActive, req: OperationRequest{Operation: OpDisable, Reason: "r"}, to: StateDisabled, write: true, event: true},
		{name: "disable disabled is noop", state: StateDisabled, req: OperationRequest{Operation: OpDisable}, to: StateDisabled},
		{name: "disable retired", state: StateRetired, req: OperationRequest{Operation: OpDisable}, errPart: "retired"},
		{name: "enable dead resets failures", state: StateDead, req: OperationRequest{Operation: OpEnable}, to: StateActive, write: true, event: true,
			check: func(t *testing.T, p opPlan) {
				require.Zero(t, p.params.ConsecutiveCheckFailures)
				require.Equal(t, now, p.params.NextCheckAt)
			}},
		{name: "enable active is noop", state: StateActive, req: OperationRequest{Operation: OpEnable}, to: StateActive},
		{name: "enable banned", state: StateBanned, req: OperationRequest{Operation: OpEnable}, errPart: "use unban"},
		{name: "ban temporary", state: StateActive, req: OperationRequest{Operation: OpBan, Duration: hour}, to: StateBanned, write: true, event: true, until: &banUntil},
		{name: "ban permanent", state: StateQuarantined, req: OperationRequest{Operation: OpBan, Duration: durationx.Permanent}, to: StateBanned, write: true, event: true, permanent: true,
			check: func(t *testing.T, p opPlan) { require.Nil(t, p.params.BanUntil) }},
		{name: "ban retired", state: StateRetired, req: OperationRequest{Operation: OpBan, Duration: hour}, errPart: "retired"},
		{name: "unban", state: StateBanned, req: OperationRequest{Operation: OpUnban}, to: StateActive, write: true, event: true,
			check: func(t *testing.T, p opPlan) { require.Nil(t, p.params.BanUntil) }},
		{name: "unban active", state: StateActive, req: OperationRequest{Operation: OpUnban}, errPart: "not banned"},
		{name: "global cooldown", state: StateDead, req: OperationRequest{Operation: OpCooldown, Duration: hour}, to: StateDead, write: true, event: true, until: &banUntil,
			check: func(t *testing.T, p opPlan) { require.Equal(t, banUntil, *p.params.CooldownUntil) }},
		{name: "site cooldown", state: StateActive, site: true, req: OperationRequest{Operation: OpCooldown, Duration: hour}, to: StateActive, event: true, until: &banUntil,
			check: func(t *testing.T, p opPlan) { require.Nil(t, p.params.CooldownUntil) }},
		{name: "cooldown retired", state: StateRetired, req: OperationRequest{Operation: OpCooldown, Duration: hour}, errPart: "retired"},
		{name: "quarantine default duration", state: StateDisabled, req: OperationRequest{Operation: OpQuarantine}, to: StateQuarantined, write: true, event: true,
			check: func(t *testing.T, p opPlan) {
				require.Equal(t, now.Add(DefaultQuarantineDuration), *p.until)
				require.Equal(t, now.Add(DefaultQuarantineDuration), *p.params.BanUntil, "ban_until holds the quarantine end")
			}},
		{name: "quarantine again replaces the end", state: StateQuarantined, req: OperationRequest{Operation: OpQuarantine, Duration: hour}, to: StateQuarantined,
			write: true, event: true, until: &banUntil,
			check: func(t *testing.T, p opPlan) { require.Equal(t, banUntil, *p.params.BanUntil) }},
		{name: "disable clears a stale ban end", state: StateActive, req: OperationRequest{Operation: OpDisable}, to: StateDisabled, write: true, event: true,
			check: func(t *testing.T, p opPlan) { require.Nil(t, p.params.BanUntil) }},
		{name: "quarantine banned", state: StateBanned, req: OperationRequest{Operation: OpQuarantine}, errPart: "cannot be quarantined"},
		{name: "unquarantine", state: StateQuarantined, req: OperationRequest{Operation: OpUnquarantine}, to: StateActive, write: true, event: true,
			check: func(t *testing.T, p opPlan) { require.Nil(t, p.params.BanUntil) }},
		{name: "cooldown keeps the ban end", state: StateBanned, req: OperationRequest{Operation: OpCooldown, Duration: hour}, to: StateBanned, write: true, event: true,
			check: func(t *testing.T, p opPlan) { require.Equal(t, banUntil, *p.params.BanUntil) }},
		{name: "unquarantine active", state: StateActive, req: OperationRequest{Operation: OpUnquarantine}, errPart: "not quarantined"},
		{name: "archive banned clears the ban end", state: StateBanned, req: OperationRequest{Operation: OpArchive}, to: StateRetired, write: true, event: true,
			check: func(t *testing.T, p opPlan) { require.Nil(t, p.params.BanUntil) }},
		{name: "archive retired is noop", state: StateRetired, req: OperationRequest{Operation: OpArchive}, to: StateRetired},
		{name: "restore", state: StateRetired, req: OperationRequest{Operation: OpRestore}, to: StateActive, write: true, event: true,
			check: func(t *testing.T, p opPlan) {
				require.Nil(t, p.params.BanUntil)
				require.Nil(t, p.params.CooldownUntil)
				require.Zero(t, p.params.ConsecutiveCheckFailures)
			}},
		{name: "restore active", state: StateActive, req: OperationRequest{Operation: OpRestore}, errPart: "not retired"},
		{name: "reset stats global", state: StateDead, req: OperationRequest{Operation: OpResetStats}, to: StateDead, write: true, event: true,
			check: func(t *testing.T, p opPlan) { require.Zero(t, p.params.ConsecutiveCheckFailures) }},
		{name: "reset stats site", state: StateDead, site: true, req: OperationRequest{Operation: OpResetStats}, to: StateDead, event: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, msg := planOperation(row(tt.state), tt.req, tt.site, now)
			if tt.errPart != "" {
				require.Contains(t, msg, tt.errPart)
				return
			}
			require.Empty(t, msg)
			require.Equal(t, tt.state, plan.from)
			require.Equal(t, tt.to, plan.to)
			require.Equal(t, tt.to, plan.params.State)
			require.Equal(t, tt.write, plan.write)
			require.Equal(t, tt.event, plan.event)
			require.Equal(t, tt.permanent, plan.permanent)
			if tt.until != nil {
				require.Equal(t, *tt.until, *plan.until)
			}
			if tt.to != tt.state {
				require.Equal(t, now, plan.params.StateChangedAt)
				require.Equal(t, tt.req.Reason, plan.params.StateReason)
			} else {
				require.Equal(t, "old", plan.params.StateReason)
			}
			if tt.check != nil {
				tt.check(t, plan)
			}
		})
	}
}

func TestValidateOperation(t *testing.T) {
	tests := []struct {
		req     OperationRequest
		errPart string
	}{
		{req: OperationRequest{Operation: OpDisable}},
		{req: OperationRequest{Operation: "explode"}, errPart: "unknown operation"},
		{req: OperationRequest{Operation: OpDisable, Site: "alpha"}, errPart: "site is only allowed"},
		{req: OperationRequest{Operation: OpCooldown}, errPart: "duration is required for cooldown"},
		{req: OperationRequest{Operation: OpCooldown, Duration: durationx.Permanent}, errPart: "duration is required for cooldown"},
		{req: OperationRequest{Operation: OpCooldown, Site: "alpha", Duration: durationx.MustParse("1m")}},
		{req: OperationRequest{Operation: OpBan}, errPart: "duration is required for ban"},
		{req: OperationRequest{Operation: OpBan, Duration: durationx.Permanent}},
		{req: OperationRequest{Operation: OpQuarantine, Duration: durationx.Permanent}, errPart: "quarantine duration"},
		{req: OperationRequest{Operation: OpQuarantine}},
		{req: OperationRequest{Operation: OpResetStats, Reason: string(make([]byte, maxReasonLength+1))}, errPart: "reason"},
	}
	for _, tt := range tests {
		err := validateOperation(tt.req)
		if tt.errPart == "" {
			require.NoError(t, err)
			continue
		}
		require.ErrorContains(t, err, tt.errPart)
	}
	_, err := validateIDs(nil)
	require.ErrorContains(t, err, "must not be empty")
	_, err = validateIDs(make([]string, MaxBulkIDs+1))
	require.ErrorContains(t, err, "at most")
	_, err = validateIDs([]string{""})
	require.ErrorContains(t, err, "empty values")
	ids, err := validateIDs([]string{"a", "b", "a"})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, ids)
}

func TestOperateProxies(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	env.svc.now = fixedClock(now)
	proxies := env.importLines(t, env.ns, "http://10.5.0.1:80", "http://10.5.0.2:80", "http://10.5.0.3:80")
	sideProxy := env.importLines(t, env.other, "http://10.5.1.1:80")[0]
	env.hot.reset()

	t.Run("ban with state events, sync, events and audit", func(t *testing.T) {
		res, err := env.svc.OperateProxies(ctx, adminUser(), []string{proxies[0].ID, sideProxy.ID, "pxy_missing"},
			OperationRequest{Operation: OpBan, Duration: durationx.MustParse("2h"), Reason: "abuse"})
		require.NoError(t, err)
		require.Equal(t, 2, res.Matched)
		require.Equal(t, 2, res.Succeeded)
		require.Equal(t, []BulkFailure{{ID: "pxy_missing", Reason: "not_found", Message: "proxy not found"}}, res.Failed)

		row := env.row(t, proxies[0].ID)
		require.Equal(t, StateBanned, row.State)
		require.Equal(t, "abuse", row.StateReason)
		require.True(t, now.Add(2*time.Hour).Equal(*row.BanUntil))

		evs := env.stateEvents(t, proxies[0].ID)
		require.Len(t, evs, 1)
		require.Equal(t, seRow{SubjectID: proxies[0].ID, From: StateActive, To: StateBanned, Action: "ban", Scope: "proxy",
			Actor: "user:usr_admin", Reason: "abuse", Until: evs[0].Until}, evs[0])
		require.True(t, now.Add(2*time.Hour).Equal(*evs[0].Until))
		require.ElementsMatch(t, []string{proxies[0].ID, sideProxy.ID}, env.hot.syncedIDs())
		require.Contains(t, env.audit.actions(), "proxy.ban")

		var data StateEventData
		found := false
		for _, ev := range env.events.snapshot() {
			if ev.Type == events.TypeProxyState && ev.NamespaceID == env.ns.ID {
				require.NoError(t, json.Unmarshal(ev.Data, &data))
				if data.SubjectID == proxies[0].ID {
					found = true
				}
			}
		}
		require.True(t, found)
		require.Equal(t, "proxy", data.SubjectKind)
		require.Equal(t, "ban", data.Action)
		require.Equal(t, StateActive, data.From)
		require.Equal(t, StateBanned, data.To)
	})

	t.Run("invalid transitions are per-item failures", func(t *testing.T) {
		res, err := env.svc.OperateProxies(ctx, adminUser(), []string{proxies[0].ID, proxies[1].ID}, OperationRequest{Operation: OpUnban})
		require.NoError(t, err)
		require.Equal(t, 2, res.Matched)
		require.Equal(t, 1, res.Succeeded)
		require.Equal(t, []BulkFailure{{ID: proxies[1].ID, Reason: "failed_precondition", Message: "proxy is not banned"}}, res.Failed)
		require.Equal(t, StateActive, env.row(t, proxies[0].ID).State)
		require.Nil(t, env.row(t, proxies[0].ID).BanUntil)
	})

	t.Run("global cooldown writes gcd on every site", func(t *testing.T) {
		p := env.row(t, proxies[1].ID)
		env.materialize(t, env.alpha, p)
		env.materialize(t, env.beta, p)
		require.NoError(t, env.rdb.Do(ctx, env.rdb.B().Zadd().Key(env.keys.ProxyReady(env.alpha.Key)).ScoreMember().
			ScoreMember(float64(now.UnixMilli()), strconv.FormatInt(p.Key, 10)).Build()).Error())

		res, err := env.svc.OperateProxies(ctx, adminUser(), []string{p.ID}, OperationRequest{Operation: OpCooldown, Duration: durationx.MustParse("10m")})
		require.NoError(t, err)
		require.Equal(t, 1, res.Succeeded)
		until := now.Add(10 * time.Minute)
		require.True(t, until.Equal(*env.row(t, p.ID).CooldownUntil))
		ms := strconv.FormatInt(until.UnixMilli(), 10)
		for _, site := range []*struct{ key int64 }{{env.alpha.Key}, {env.beta.Key}} {
			require.Equal(t, ms, env.hget(t, env.keys.ProxySite(site.key, p.Key), "gcd"))
			require.Empty(t, env.hget(t, env.keys.ProxySite(site.key, p.Key), "cd"))
		}
		score, err := env.rdb.Do(ctx, env.rdb.B().Zscore().Key(env.keys.ProxyReady(env.alpha.Key)).Member(strconv.FormatInt(p.Key, 10)).Build()).AsFloat64()
		require.NoError(t, err)
		require.Equal(t, float64(until.UnixMilli()), score)
		require.Contains(t, env.dirty(t, env.alpha), "p"+strconv.FormatInt(p.Key, 10))
		require.Equal(t, "proxy", env.stateEvents(t, p.ID)[0].Scope)
	})

	t.Run("site cooldown writes cd on that site only", func(t *testing.T) {
		p := env.row(t, proxies[2].ID)
		env.hot.reset()
		res, err := env.svc.OperateProxies(ctx, adminUser(), []string{p.ID}, OperationRequest{Operation: OpCooldown, Site: "beta", Duration: durationx.MustParse("5m"), Reason: "429"})
		require.NoError(t, err)
		require.Equal(t, 1, res.Succeeded)
		until := strconv.FormatInt(now.Add(5*time.Minute).UnixMilli(), 10)
		require.Equal(t, until, env.hget(t, env.keys.ProxySite(env.beta.Key, p.Key), "cd"))
		require.Equal(t, p.ID, env.hget(t, env.keys.ProxySite(env.beta.Key, p.Key), "pid"))
		require.Empty(t, env.hget(t, env.keys.ProxySite(env.alpha.Key, p.Key), "cd"))
		require.Nil(t, env.row(t, p.ID).CooldownUntil, "site cooldowns are not stored in PostgreSQL")
		require.Empty(t, env.hot.syncedIDs())
		evs := env.stateEvents(t, p.ID)
		require.Len(t, evs, 1)
		require.Equal(t, env.beta.ID, evs[0].SiteID)
		require.Equal(t, "proxy_site", evs[0].Scope)
		require.Equal(t, "cooldown", evs[0].Action)
	})

	t.Run("unknown site fails items", func(t *testing.T) {
		res, err := env.svc.OperateProxies(ctx, adminUser(), []string{proxies[2].ID}, OperationRequest{Operation: OpCooldown, Site: "nope", Duration: durationx.MustParse("5m")})
		require.NoError(t, err)
		require.Equal(t, []BulkFailure{{ID: proxies[2].ID, Reason: "site_unknown", Message: "site not found"}}, res.Failed)
	})

	t.Run("reset stats clears health fields", func(t *testing.T) {
		p := env.row(t, proxies[1].ID)
		env.materialize(t, env.alpha, p, "sc", "12.50", "sts", "1", "sn", "9", "nf", "3", "lf", "5", "al", "1")
		_, err := env.pool.Exec(ctx, `UPDATE proxies SET consecutive_check_failures = 5 WHERE id = $1`, p.ID)
		require.NoError(t, err)
		res, err := env.svc.OperateProxies(ctx, adminUser(), []string{p.ID}, OperationRequest{Operation: OpResetStats})
		require.NoError(t, err)
		require.Equal(t, 1, res.Succeeded)
		key := env.keys.ProxySite(env.alpha.Key, p.Key)
		for _, f := range []string{"sc", "sts", "sn", "nf", "lf"} {
			require.Empty(t, env.hget(t, key, f), f)
		}
		require.Equal(t, "1", env.hget(t, key, "al"), "other fields are kept")
		require.Zero(t, env.row(t, p.ID).ConsecutiveCheckFailures)
	})

	t.Run("lifecycle round trip", func(t *testing.T) {
		id := proxies[2].ID
		for _, step := range []struct {
			op, want string
		}{
			{OpDisable, StateDisabled}, {OpEnable, StateActive}, {OpQuarantine, StateQuarantined},
			{OpUnquarantine, StateActive}, {OpArchive, StateRetired}, {OpRestore, StateActive},
		} {
			res, err := env.svc.OperateProxies(ctx, adminUser(), []string{id}, OperationRequest{Operation: step.op})
			require.NoError(t, err, step.op)
			require.Empty(t, res.Failed, step.op)
			require.Equal(t, step.want, env.row(t, id).State, step.op)
		}
		evs := env.stateEvents(t, id)
		require.Equal(t, "activate", evs[len(evs)-3].Action, "unquarantine is recorded as activate")
	})

	t.Run("permissions", func(t *testing.T) {
		_, err := env.svc.OperateProxies(ctx, viewerUser(), []string{proxies[0].ID}, OperationRequest{Operation: OpDisable})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.svc.OperateProxies(ctx, siteOperator(env.ns.ID, env.alpha.ID), []string{proxies[0].ID}, OperationRequest{Operation: OpDisable})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.svc.OperateProxies(ctx, tokenPrincipal(t, "lease:acquire"), []string{proxies[0].ID}, OperationRequest{Operation: OpDisable})
		requireReason(t, err, apperr.ReasonScopeMissing)
		_, err = env.svc.OperateProxies(ctx, nil, []string{proxies[0].ID}, OperationRequest{Operation: OpDisable})
		requireReason(t, err, apperr.ReasonSessionInvalid)

		res, err := env.svc.OperateProxies(ctx, foreignUser(), []string{proxies[0].ID}, OperationRequest{Operation: OpDisable})
		require.NoError(t, err)
		require.Zero(t, res.Matched)
		require.Equal(t, "not_found", res.Failed[0].Reason)
		require.Equal(t, StateActive, env.row(t, proxies[0].ID).State)

		res, err = env.svc.OperateProxies(ctx, tokenPrincipal(t, "proxy:write"), []string{proxies[0].ID, sideProxy.ID}, OperationRequest{Operation: OpDisable})
		require.NoError(t, err)
		require.Equal(t, 1, res.Succeeded, "tokens only see their namespace")
		require.Equal(t, []BulkFailure{{ID: sideProxy.ID, Reason: "not_found", Message: "proxy not found"}}, res.Failed)

		_, err = env.svc.OperateProxies(ctx, adminUser(), nil, OperationRequest{Operation: OpDisable})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = env.svc.OperateProxies(ctx, adminUser(), []string{proxies[0].ID}, OperationRequest{Operation: "boom"})
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	t.Run("hot sync failure returns internal after commit", func(t *testing.T) {
		env.hot.err = errors.New("down")
		defer env.hot.reset()
		res, err := env.svc.OperateProxies(ctx, adminUser(), []string{proxies[0].ID}, OperationRequest{Operation: OpEnable})
		requireReason(t, err, apperr.ReasonInternal)
		require.Equal(t, 1, res.Succeeded)
		require.Equal(t, StateActive, env.row(t, proxies[0].ID).State)
	})
}

func TestUpdateProxy(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	proxies := env.importLines(t, env.ns, "http://old:pw@10.6.0.1:80 region=us tags=a", "http://10.6.0.2:80")
	p := proxies[0]
	env.hot.reset()

	t.Run("url replace reseals and bumps version", func(t *testing.T) {
		url := "socks5://new%40user:n3w@10.6.0.9:1080"
		region := "  de "
		tpl := "{username}-{lease_id}"
		mc := 7
		got, err := env.svc.UpdateProxy(ctx, adminUser(), p.ID, UpdateRequest{
			URL: &url, Region: &region, SessionTemplate: &tpl, MaxConcurrency: &mc, SetTags: true, Tags: nil,
		})
		require.NoError(t, err)
		require.Equal(t, 2, got.URLVersion)
		require.Equal(t, "socks5://10.6.0.9:1080", got.DisplayURL)
		require.Equal(t, "new@***", got.UsernameHint)
		require.Equal(t, "de", got.Attributes.Region)
		require.Equal(t, []string{}, got.Attributes.Tags)
		require.Equal(t, 7, got.Attributes.MaxConcurrency)
		raw := env.rawRow(t, p.ID)
		u, err := OpenURL(env.cipher, p.ID, sealedURL(raw))
		require.NoError(t, err)
		require.Equal(t, "n3w", u.Password)
		require.Equal(t, []string{p.ID}, env.hot.syncedIDs())
		require.Contains(t, env.audit.actions(), "proxy.update")
	})

	t.Run("same url is a no-op", func(t *testing.T) {
		env.hot.reset()
		url := "socks5://new%40user:n3w@10.6.0.9:1080/"
		got, err := env.svc.UpdateProxy(ctx, adminUser(), p.ID, UpdateRequest{URL: &url})
		require.NoError(t, err)
		require.Equal(t, 2, got.URLVersion)
		require.Empty(t, env.hot.syncedIDs())
	})

	t.Run("errors", func(t *testing.T) {
		dup := "http://10.6.0.2:80"
		_, err := env.svc.UpdateProxy(ctx, adminUser(), p.ID, UpdateRequest{URL: &dup})
		requireReason(t, err, apperr.ReasonAlreadyExists)
		bad := "http://10.6.0.2"
		_, err = env.svc.UpdateProxy(ctx, adminUser(), p.ID, UpdateRequest{URL: &bad})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		kind := "rocket"
		_, err = env.svc.UpdateProxy(ctx, adminUser(), p.ID, UpdateRequest{Kind: &kind})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		tpl := "{password"
		_, err = env.svc.UpdateProxy(ctx, adminUser(), p.ID, UpdateRequest{SessionTemplate: &tpl})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = env.svc.UpdateProxy(ctx, viewerUser(), p.ID, UpdateRequest{Kind: &kind})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.svc.UpdateProxy(ctx, foreignUser(), p.ID, UpdateRequest{})
		requireReason(t, err, apperr.ReasonNotFound)
		_, err = env.svc.UpdateProxy(ctx, adminUser(), "pxy_none", UpdateRequest{})
		requireReason(t, err, apperr.ReasonNotFound)
	})
}
