package policysvc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvctest"
)

func debugIn(mut func(*policysvc.DebugInput)) policysvc.DebugInput {
	in := policysvc.DebugInput{
		Site:   "shop",
		Client: "web",
		Report: policysvc.DebugReport{URI: "/api/v1/search/item?keyword=x", Method: "GET"},
	}
	if mut != nil {
		mut(&in)
	}
	return in
}

func TestDebugReportDesignExamples(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	// A draft signal policy that recognizes account bans, used through draft_policy_id.
	banSignals := draftNew(t, f, policy.KindSignal,
		"name: ban-signals\nextends: default-signal\nrules:\n  - {name: account-banned, when: {markers: [account_banned]}, outcome: banned}\n")

	tests := []struct {
		name    string
		in      policysvc.DebugInput
		outcome string
		blame   string
		rule    string
		ruleIdx int
		actions []policysvc.PlannedAction
		counter []policysvc.CounterRequirement
	}{
		{
			name: "429 cools down identity x endpoint",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.HTTPStatus = 429
				in.EndpointStreak = 1
			}),
			outcome: "rate_limited", blame: "both", rule: "rate-limited", ruleIdx: 4,
			actions: []policysvc.PlannedAction{{
				Action: "cooldown", Scope: "identity_endpoint", Duration: "1m", Severity: 1,
				RuleName: "rate-limited-cooldown", Source: "rule", RuleIndex: 0,
			}},
			counter: []policysvc.CounterRequirement{},
		},
		{
			name: "429 escalates with the streak",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.HTTPStatus = 429
				in.EndpointStreak = 4
			}),
			outcome: "rate_limited", blame: "both", rule: "rate-limited", ruleIdx: 4,
			actions: []policysvc.PlannedAction{{
				Action: "cooldown", Scope: "identity_endpoint", Duration: "8m", Severity: 1,
				RuleName: "rate-limited-cooldown", Source: "rule", RuleIndex: 0,
			}},
			counter: []policysvc.CounterRequirement{},
		},
		{
			name: "first captcha cools down identity x site",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.HTTPStatus = 200
				in.Report.Markers = []string{"captcha_page"}
				in.EndpointStreak = 1
			}),
			outcome: "captcha", blame: "identity", rule: "captcha", ruleIdx: 2,
			actions: []policysvc.PlannedAction{{
				Action: "cooldown", Scope: "identity_site", Duration: "30m", Severity: 1,
				RuleName: "captcha-cooldown", Source: "rule", RuleIndex: 2,
			}},
			counter: []policysvc.CounterRequirement{{Subject: "identity", Outcome: "captcha", Window: "1d"}},
		},
		{
			name: "captcha thrice in 24h bans the identity for 12h",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.Markers = []string{"captcha_page"}
				in.Counts = map[string]int64{"identity:captcha:24h": 3}
				in.EndpointStreak = 3
			}),
			outcome: "captcha", blame: "identity", rule: "captcha", ruleIdx: 2,
			actions: []policysvc.PlannedAction{{
				Action: "ban", Scope: "identity", Duration: "12h", Severity: 4,
				RuleName: "captcha-ban", Source: "rule", RuleIndex: 3,
			}},
			counter: []policysvc.CounterRequirement{{Subject: "identity", Outcome: "captcha", Window: "1d"}},
		},
		{
			name: "captcha ban escalates with earlier bans",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.Markers = []string{"captcha_page"}
				in.Counts = map[string]int64{"identity:captcha:1d": 5}
				in.BanCounts = map[string]int64{"7d": 1, "30d": 1}
			}),
			outcome: "captcha", blame: "identity", rule: "captcha", ruleIdx: 2,
			actions: []policysvc.PlannedAction{{
				Action: "ban", Scope: "identity", Duration: "3d", Severity: 4,
				RuleName: "captcha-ban", Source: "escalation", RuleIndex: 3,
			}},
			counter: []policysvc.CounterRequirement{{Subject: "identity", Outcome: "captcha", Window: "1d"}},
		},
		{
			name: "banned without account degrades to a permanent identity ban",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.Markers = []string{"account_banned"}
				in.DraftPolicyID = banSignals.ID
			}),
			outcome: "banned", blame: "identity", rule: "account-banned", ruleIdx: 11,
			actions: []policysvc.PlannedAction{{
				Action: "ban", Scope: "identity", Duration: "permanent", Permanent: true, Severity: 5,
				RuleName: "banned-account", Source: "rule", RuleIndex: 5,
			}},
			counter: []policysvc.CounterRequirement{},
		},
		{
			name: "banned with account bans the account permanently",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.Markers = []string{"account_banned"}
				in.DraftPolicyID = banSignals.ID
				in.HasAccount = true
			}),
			outcome: "banned", blame: "identity", rule: "account-banned", ruleIdx: 11,
			actions: []policysvc.PlannedAction{{
				Action: "ban", Scope: "account", Duration: "permanent", Permanent: true, Severity: 5,
				RuleName: "banned-account", Source: "rule", RuleIndex: 5,
			}},
			counter: []policysvc.CounterRequirement{},
		},
		{
			name: "pending identity is activated by success",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.HTTPStatus = 200
				in.IdentityState = "pending"
			}),
			outcome: "success", blame: "none", rule: "success", ruleIdx: 10,
			actions: []policysvc.PlannedAction{{
				Action: "activate", Scope: "identity", Severity: 0, RuleName: policy.RuleNameActivate, Source: "lifecycle", RuleIndex: -1,
			}},
			counter: []policysvc.CounterRequirement{},
		},
		{
			name: "proxy error without proxy plans nothing",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.ErrorKind = "proxy_auth"
			}),
			outcome: "proxy_error", blame: "proxy", rule: "proxy-error", ruleIdx: 0,
			actions: []policysvc.PlannedAction{}, counter: []policysvc.CounterRequirement{},
		},
		{
			name: "proxy error with proxy cools the proxy down",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.ErrorKind = "proxy_auth"
				in.HasProxy = true
				in.ProxyStreak = 2
			}),
			outcome: "proxy_error", blame: "proxy", rule: "proxy-error", ruleIdx: 0,
			actions: []policysvc.PlannedAction{{
				Action: "cooldown", Scope: "proxy_site", Duration: "2m", Severity: 1,
				RuleName: "proxy-error-cooldown", Source: "rule", RuleIndex: 6,
			}},
			counter: []policysvc.CounterRequirement{},
		},
		{
			name: "low endpoint score adds the health cooldown",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.HTTPStatus = 403
				in.EndpointScore = 10
				in.EndpointSamples = 20
				in.GlobalScore = 50
				in.GlobalSamples = 20
			}),
			outcome: "forbidden", blame: "identity", rule: "forbidden", ruleIdx: 9,
			actions: []policysvc.PlannedAction{{
				Action: "cooldown", Scope: "identity_endpoint", Duration: "6h", Severity: 1,
				RuleName: policy.RuleNameEndpointLow, Source: "health", RuleIndex: -1,
			}},
			counter: []policysvc.CounterRequirement{},
		},
		{
			name: "unmatched report is unknown",
			in: debugIn(func(in *policysvc.DebugInput) {
				in.Report.HTTPStatus = 302
				in.EndpointGroup = "_default"
			}),
			outcome: "unknown", blame: "none", rule: "", ruleIdx: -1,
			actions: []policysvc.PlannedAction{}, counter: []policysvc.CounterRequirement{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := svc.DebugReport(ctx, f.Viewer, f.Namespace, tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.outcome, res.Outcome)
			require.Equal(t, tt.blame, res.Blame)
			require.Equal(t, tt.rule, res.MatchedRuleName)
			require.Equal(t, tt.ruleIdx, res.MatchedRuleIndex)
			require.Equal(t, tt.actions, res.Actions)
			require.Equal(t, tt.counter, res.Counters)
			require.Equal(t, policy.ModeEnforce, res.Mode)
		})
	}
}

func TestDebugReportTargetsAndErrors(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	// URI matching selects the search group; an explicit uri overrides report.uri.
	res, err := svc.DebugReport(ctx, f.Viewer, f.Namespace, debugIn(nil))
	require.NoError(t, err)
	require.Equal(t, "search", res.EndpointGroupName)
	require.Equal(t, f.Search.ID, res.EndpointGroupID)
	res, err = svc.DebugReport(ctx, f.Viewer, f.Namespace, debugIn(func(in *policysvc.DebugInput) { in.URI = "/other" }))
	require.NoError(t, err)
	require.Equal(t, "_default", res.EndpointGroupName)
	res, err = svc.DebugReport(ctx, f.Viewer, f.Namespace, debugIn(func(in *policysvc.DebugInput) {
		in.Report.URI = ""
		in.URI = "https://target.example.com/api/v1/search/x"
	}))
	require.NoError(t, err)
	require.Equal(t, "search", res.EndpointGroupName)

	// A shadow action draft replaces the resolved action policy.
	shadow := draftNew(t, f, policy.KindAction, "name: shadow-actions\nmode: shadow\nrules:\n  - {when: {outcome: rate_limited}, action: ban, scope: identity, duration: 1h}\n")
	res, err = svc.DebugReport(ctx, f.Viewer, f.Namespace, debugIn(func(in *policysvc.DebugInput) {
		in.Report.HTTPStatus = 429
		in.DraftPolicyID = shadow.ID
	}))
	require.NoError(t, err)
	require.Equal(t, policy.ModeShadow, res.Mode)
	require.Len(t, res.Actions, 1)
	require.Equal(t, "ban", res.Actions[0].Action)
	require.Equal(t, "1h", res.Actions[0].Duration)
	require.Equal(t, "shadow-actions.rules[0]", res.Actions[0].RuleName)

	// A published policy without a draft is used as published.
	pubSignals := publishNew(t, f, policy.KindSignal, "name: all-captcha\nrules:\n  - {when: {}, outcome: captcha}\n")
	res, err = svc.DebugReport(ctx, f.Viewer, f.Namespace, debugIn(func(in *policysvc.DebugInput) {
		in.Report.HTTPStatus = 200
		in.DraftPolicyID = pubSignals.ID
	}))
	require.NoError(t, err)
	require.Equal(t, "captcha", res.Outcome)

	rotation := draftNew(t, f, policy.KindRotation, "name: some-rotation\n")
	brokenDraft := draftNew(t, f, policy.KindSignal, "name: broken-signals\nextends: nowhere\n")

	errs := []struct {
		name   string
		p      bool // use the lease token instead of the viewer
		in     policysvc.DebugInput
		code   connect.Code
		reason apperr.Reason
	}{
		{name: "unknown site", in: debugIn(func(in *policysvc.DebugInput) { in.Site = "nope" }), reason: apperr.ReasonSiteUnknown},
		{name: "unknown client", in: debugIn(func(in *policysvc.DebugInput) { in.Client = "tv" }), reason: apperr.ReasonClientUnknown},
		{name: "unknown group", in: debugIn(func(in *policysvc.DebugInput) { in.EndpointGroup = "nope" }), reason: apperr.ReasonEndpointGroupUnknown},
		{name: "bad uri", in: debugIn(func(in *policysvc.DebugInput) { in.URI = "ftp://x" }), reason: apperr.ReasonURIInvalid},
		{name: "bad report uri", in: debugIn(func(in *policysvc.DebugInput) { in.Report.URI = "no-slash" }), reason: apperr.ReasonURIInvalid},
		{name: "missing uri", in: debugIn(func(in *policysvc.DebugInput) { in.Report.URI = "" }), reason: apperr.ReasonURIInvalid},
		{name: "bad status", in: debugIn(func(in *policysvc.DebugInput) { in.Report.HTTPStatus = 1000 }), code: connect.CodeInvalidArgument},
		{name: "bad error kind", in: debugIn(func(in *policysvc.DebugInput) { in.Report.ErrorKind = "boom" }), code: connect.CodeInvalidArgument},
		{name: "empty marker", in: debugIn(func(in *policysvc.DebugInput) { in.Report.Markers = []string{""} }), code: connect.CodeInvalidArgument},
		{name: "negative latency", in: debugIn(func(in *policysvc.DebugInput) { in.Report.LatencyMs = -1 }), code: connect.CodeInvalidArgument},
		{name: "bad state", in: debugIn(func(in *policysvc.DebugInput) { in.IdentityState = "zombie" }), code: connect.CodeInvalidArgument},
		{name: "bad score", in: debugIn(func(in *policysvc.DebugInput) { in.EndpointScore = 101 }), code: connect.CodeInvalidArgument},
		{name: "negative streak", in: debugIn(func(in *policysvc.DebugInput) { in.ProxyStreak = -1 }), code: connect.CodeInvalidArgument},
		{name: "huge streak", in: debugIn(func(in *policysvc.DebugInput) { in.SiteStreak = 2_000_000 }), code: connect.CodeInvalidArgument},
		{name: "bad count key", in: debugIn(func(in *policysvc.DebugInput) { in.Counts = map[string]int64{"identity:captcha": 1} }), code: connect.CodeInvalidArgument},
		{name: "bad count subject", in: debugIn(func(in *policysvc.DebugInput) { in.Counts = map[string]int64{"node:captcha:1h": 1} }), code: connect.CodeInvalidArgument},
		{name: "bad count outcome", in: debugIn(func(in *policysvc.DebugInput) { in.Counts = map[string]int64{"identity:meh:1h": 1} }), code: connect.CodeInvalidArgument},
		{name: "bad count window", in: debugIn(func(in *policysvc.DebugInput) { in.Counts = map[string]int64{"identity:captcha:permanent": 1} }), code: connect.CodeInvalidArgument},
		{name: "negative count", in: debugIn(func(in *policysvc.DebugInput) { in.Counts = map[string]int64{"identity:captcha:1h": -1} }), code: connect.CodeInvalidArgument},
		{name: "bad ban window", in: debugIn(func(in *policysvc.DebugInput) { in.BanCounts = map[string]int64{"x": 1} }), code: connect.CodeInvalidArgument},
		{name: "negative ban count", in: debugIn(func(in *policysvc.DebugInput) { in.BanCounts = map[string]int64{"7d": -1} }), code: connect.CodeInvalidArgument},
		{name: "bad cooldown remaining", in: debugIn(func(in *policysvc.DebugInput) { in.EndpointCooldownRemaining = "soon" }), code: connect.CodeInvalidArgument},
		{name: "rotation draft", in: debugIn(func(in *policysvc.DebugInput) { in.DraftPolicyID = rotation.ID }), code: connect.CodeInvalidArgument},
		{name: "broken draft chain", in: debugIn(func(in *policysvc.DebugInput) { in.DraftPolicyID = brokenDraft.ID }), code: connect.CodeInvalidArgument},
		{name: "missing draft policy", in: debugIn(func(in *policysvc.DebugInput) { in.DraftPolicyID = "pol_missing" }), code: connect.CodeNotFound},
		{name: "token without policy scope", p: true, in: debugIn(nil), reason: apperr.ReasonScopeMissing},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			p := f.Viewer
			if tt.p {
				p = f.LeaseToken
			}
			_, err := svc.DebugReport(ctx, p, f.Namespace, tt.in)
			require.Error(t, err)
			if tt.code != 0 {
				requireCode(t, err, tt.code)
			}
			if tt.reason != "" {
				requireReason(t, err, tt.reason)
			}
		})
	}

	// Site-restricted principals can debug their own sites only.
	_, err = svc.DebugReport(ctx, f.ShopAdmin, f.Namespace, debugIn(nil))
	require.NoError(t, err)
	_, err = svc.DebugReport(ctx, f.ShopAdmin, f.Namespace, debugIn(func(in *policysvc.DebugInput) { in.Site = "forum" }))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.DebugReport(ctx, nil, f.Namespace, debugIn(nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestValidatePolicy(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	svc := f.Service

	res, err := svc.ValidatePolicy(f.Viewer, policy.KindRotation, "name: r\nrotation: {lease_ttl: 1s, strategy: nope}\n")
	require.NoError(t, err)
	require.False(t, res.Valid)
	require.Len(t, res.Errors, 2)
	require.Empty(t, res.NormalizedYAML)

	res, err = svc.ValidatePolicy(f.AdminToken, policy.KindRotation, "name: r\nfoo: 1\n")
	require.NoError(t, err)
	require.False(t, res.Valid)
	require.Len(t, res.Errors, 1)

	res, err = svc.ValidatePolicy(f.ShopAdmin, policy.KindBreaker, "name: b\n")
	require.NoError(t, err)
	require.True(t, res.Valid)
	require.Empty(t, res.Errors)
	require.Contains(t, res.NormalizedYAML, "min_requests: 50")

	_, err = svc.ValidatePolicy(f.LeaseToken, policy.KindBreaker, "name: b\n")
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = svc.ValidatePolicy(f.OtherTenant, policy.KindBreaker, "name: b\n")
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.ValidatePolicy(nil, policy.KindBreaker, "name: b\n")
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = svc.ValidatePolicy(authz.System("test"), "bogus", "name: b\n")
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.ValidatePolicy(authz.System("test"), policy.KindBreaker, "")
	requireCode(t, err, connect.CodeInvalidArgument)
}
