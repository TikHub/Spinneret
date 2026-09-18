package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/pkg/durationx"
)

type validationCase struct {
	name string
	src  string
	want []string // substrings of Problem.String(), all must be present
}

func runValidationCases(t *testing.T, kind Kind, cases []validationCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseYAML(kind, []byte(tc.src))
			if len(tc.want) == 0 {
				require.NoError(t, err)
				require.NotNil(t, spec)
				return
			}
			require.Nil(t, spec)
			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			problems := make([]string, 0, len(ve.Problems))
			for _, p := range ve.Problems {
				problems = append(problems, p.String())
			}
			joined := strings.Join(problems, "\n")
			for _, w := range tc.want {
				require.Contains(t, joined, w)
			}
			require.Len(t, ve.Problems, len(tc.want), "unexpected extra problems:\n%s", joined)
		})
	}
}

func TestValidateCommon(t *testing.T) {
	runValidationCases(t, KindBreaker, []validationCase{
		{"valid minimal", "name: a", nil},
		{"valid with bind", "name: a.b_c-1\nbind: {site: shop, client: web, endpoint_group: search}", nil},
		{"missing name", "window: 60s", []string{"name: is required"}},
		{"uppercase name", "name: Abc", []string{`name: must match ^[a-z0-9][a-z0-9._-]{0,63}$ (got "Abc")`}},
		{"leading dash", "name: -abc", []string{"name: must match"}},
		{"too long name", "name: " + strings.Repeat("a", 65), []string{"name: must match"}},
		{"long description", "name: a\ndescription: " + strings.Repeat("x", 1025), []string{"description: must be at most 1024 characters"}},
		{"bind without site", "name: a\nbind: {client: web}", []string{"bind.site: is required when bind is set"}},
		{"bind fields too long", "name: a\nbind: {site: " + strings.Repeat("s", 129) + ", client: " + strings.Repeat("c", 129) + ", endpoint_group: " + strings.Repeat("e", 129) + "}",
			[]string{"bind.site: must be at most 128", "bind.client: must be at most 128", "bind.endpoint_group: must be at most 128"}},
	})
	require.True(t, ValidName(strings.Repeat("a", 64)))
	require.False(t, ValidName(""))
	require.False(t, ValidName("a b"))
}

func TestValidateRotation(t *testing.T) {
	runValidationCases(t, KindRotation, []validationCase{
		{"design doc example", `
name: web-search-rotation
bind: { site: shop, client: web, endpoint_group: search }
identity_types: [web_cookie]
rotation:
  strategy: weighted_random
  candidate_sample: 32
  lease_ttl: 120s
  max_concurrent_leases: 1
  reuse_interval: 30s
  reuse_anchor: released
  reuse_scope: endpoint_group
  quota:
    - { limit: 60, window: 1h }
    - { limit: 500, window: 24h }
  sticky: { enabled: true, ttl: 10m }
  warmup: { duration: 24h, quota_factor: 0.3 }
  probe: { weight_factor: 0.1, max_leases: 2 }
proxy:
  mode: bind_identity
  kinds: [residential]
`, nil},
		{"enums", "name: a\nrotation: {strategy: random, reuse_anchor: now, reuse_scope: global}\nproxy: {mode: magic, kinds: [cave]}", []string{
			`rotation.strategy: must be one of weighted_random|least_recently_used|round_robin|best_health (got "random")`,
			`rotation.reuse_anchor: must be one of acquired|released (got "now")`,
			`rotation.reuse_scope: must be one of endpoint_group|site (got "global")`,
			`proxy.mode: must be one of none|pool|bind_identity|region_match (got "magic")`,
			`proxy.kinds[0]: must be one of datacenter|residential|mobile|tunnel (got "cave")`,
		}},
		{"candidate sample high", "name: a\nrotation: {candidate_sample: 257}", []string{"rotation.candidate_sample: must be between 1 and 256 (got 257)"}},
		{"candidate sample negative", "name: a\nrotation: {candidate_sample: -1}", []string{"rotation.candidate_sample: must be between 1 and 256 (got -1)"}},
		{"lease ttl too short", "name: a\nrotation: {lease_ttl: 4s}", []string{"rotation.lease_ttl: must be at least 5s (got 4s)"}},
		{"lease ttl too long", "name: a\nrotation: {lease_ttl: 31m, max_lease_lifetime: 1h}", []string{"rotation.lease_ttl: must be at most 30m (got 31m)"}},
		{"lease ttl permanent", "name: a\nrotation: {lease_ttl: permanent}", []string{"rotation.lease_ttl: must not be permanent"}},
		{"lifetime below ttl", "name: a\nrotation: {lease_ttl: 10m, max_lease_lifetime: 5m}", []string{"rotation.max_lease_lifetime: must be at least lease_ttl (10m)"}},
		{"lifetime too long", "name: a\nrotation: {max_lease_lifetime: 25h}", []string{"rotation.max_lease_lifetime: must be at most 1d (got 1d1h)"}},
		{"lifetime permanent", "name: a\nrotation: {max_lease_lifetime: permanent}", []string{"rotation.max_lease_lifetime: must not be permanent"}},
		{"concurrency", "name: a\nrotation: {max_concurrent_leases: 10001}", []string{"rotation.max_concurrent_leases: must be between 1 and 10000"}},
		{"reuse interval permanent", "name: a\nrotation: {reuse_interval: permanent}", []string{"rotation.reuse_interval: must not be permanent"}},
		{"quota", "name: a\nrotation: {quota: [{limit: 0, window: 500ms}, {limit: 5, window: 1h}, {limit: 6, window: 60m}]}", []string{
			"rotation.quota[0].limit: must be at least 1 (got 0)",
			"rotation.quota[0].window: must be at least 1s (got 500ms)",
			"rotation.quota[2].window: duplicates the window of quota[1] (1h)",
		}},
		{"sticky ttl", "name: a\nrotation: {sticky: {enabled: true, ttl: 500ms}}", []string{"rotation.sticky.ttl: must be at least 1s"}},
		{"warmup", "name: a\nrotation: {warmup: {duration: permanent, quota_factor: 1.5}}", []string{
			"rotation.warmup.duration: must not be permanent",
			"rotation.warmup.quota_factor: must be greater than 0 and at most 1 (got 1.5)",
		}},
		{"warmup negative factor", "name: a\nrotation: {warmup: {quota_factor: -0.5}}", []string{"rotation.warmup.quota_factor: must be greater than 0"}},
		{"probe", "name: a\nrotation: {probe: {weight_factor: 2, max_leases: -1}}", []string{
			"rotation.probe.weight_factor: must be greater than 0 and at most 1 (got 2)",
			"rotation.probe.max_leases: must be at least 1 (got -1)",
		}},
		{"proxy numbers", "name: a\nproxy: {rebind_tolerance: permanent, max_rebinds_per_day: -1}", []string{
			"proxy.rebind_tolerance: must not be permanent",
			"proxy.max_rebinds_per_day: must be at least 0 (got -1)",
		}},
		{"proxy lists", "name: a\nproxy: {tags: [a, a, \"\"], providers: [], regions: [" + strings.Repeat("r", 257) + "]}", []string{
			`proxy.tags[1]: duplicate value "a"`,
			"proxy.tags[2]: must not be empty",
			"proxy.regions[0]: must be at most 256 characters",
		}},
		{"identity types", "name: a\nidentity_types: [" + strings.Repeat("t", 65) + ", \"\"]", []string{
			"identity_types[0]: must be at most 64 characters",
			"identity_types[1]: must not be empty",
		}},
	})

	// Empty optional lists are normalized to nil (providers: [] above is accepted).
	spec, err := ParseYAML(KindRotation, []byte("name: a\nproxy: {providers: []}\nidentity_types: []"))
	require.NoError(t, err)
	require.Nil(t, spec.(*RotationSpec).Proxy.Providers)
	require.Nil(t, spec.(*RotationSpec).IdentityTypes)
}

func TestValidateSignal(t *testing.T) {
	runValidationCases(t, KindSignal, []validationCase{
		{"design doc example with business code", `
name: web-signals
bind: { site: shop, client: web }
trust_outcome_hint: false
rules:
  - when: { error_kind: [proxy_auth, conn_refused] }
    outcome: proxy_error
  - when: { http_status: [200], business_code: [10001, 10002] }
    outcome: forbidden
  - when: { uri: {regex: "^/api/v[0-9]+/"}, method: [GET, post], latency_ms: {gte: 0, lt: 100}, response_bytes: {lte: 10} }
    outcome: empty
    blame: none
  - when: {}
    outcome: unknown
`, nil},
		{"outcome and blame", "name: a\nrules:\n  - when: {}\n  - when: {}\n    outcome: bogus\n    blame: someone", []string{
			"rules[0].outcome: is required",
			`rules[1].outcome: must be one of success|empty|rate_limited|captcha|auth_invalid|forbidden|banned|proxy_error|network_error|target_error|client_error|unknown (got "bogus")`,
			`rules[1].blame: must be one of none|identity|proxy|both (got "someone")`,
		}},
		{"rule names", "name: a\nrules:\n  - {name: dup, outcome: success}\n  - {name: dup, outcome: success}\n  - {name: Bad, outcome: success}", []string{
			`rules[1].name: duplicates the name of rules[0] ("dup")`,
			`rules[2].name: must match`,
		}},
		{"extends", "name: a\nextends: a", []string{"extends: a policy cannot extend itself"}},
		{"extends pattern", "name: a\nextends: B", []string{`extends: must match ^[a-z0-9][a-z0-9._-]{0,63}$ (got "B")`}},
		{"http status list", "name: a\nrules:\n  - when: {http_status: [1000, -1]}\n    outcome: success\n  - when: {http_status: []}\n    outcome: success", []string{
			"rules[0].when.http_status[0]: must be between 0 and 999 (got 1000)",
			"rules[0].when.http_status[1]: must be between 0 and 999 (got -1)",
			"rules[1].when.http_status: must not be empty",
		}},
		{"http status ranges", `name: a
rules:
  - when: {http_status: {}}
    outcome: success
  - when: {http_status: {gte: 1, gt: 2, lte: 5, lt: 6}}
    outcome: success
  - when: {http_status: {gte: 1000}}
    outcome: success
  - when: {http_status: {gt: 500, lt: 501}}
    outcome: success
  - when: {http_status: {lte: 0}}
    outcome: success
  - when: {http_status: {gte: 0, lt: 1}}
    outcome: success
  - when: {http_status: {gte: 0, lte: 1}}
    outcome: success
`, []string{
			"rules[0].when.http_status: must set at least one of gte, gt, lte, lt",
			"rules[1].when.http_status: gte and gt are mutually exclusive",
			"rules[1].when.http_status: lte and lt are mutually exclusive",
			"rules[2].when.http_status.gte: must be between 0 and 999 (got 1000)",
			"rules[3].when.http_status: describes an empty range",
			"rules[4].when.http_status: never matches: a range only matches reported statuses",
			"rules[5].when.http_status: never matches",
		}},
		{"string lists", `name: a
rules:
  - when: {business_code: [], error_kind: [boom, timeout, timeout, other], markers: [""], method: ["GE T"]}
    outcome: success
`, []string{
			"rules[0].when.business_code: must not be empty",
			`rules[0].when.error_kind[0]: must be one of timeout|conn_reset|conn_refused|proxy_auth|tls|dns|other (got "boom")`,
			`rules[0].when.error_kind[2]: duplicate value "timeout"`,
			"rules[0].when.markers[0]: must not be empty",
			`rules[0].when.method[0]: must be an HTTP method token (got "GE T")`,
		}},
		{"uri", `name: a
rules:
  - when: {uri: {}}
    outcome: success
  - when: {uri: {prefix: /a, regex: b}}
    outcome: success
  - when: {uri: {prefix: api}}
    outcome: success
  - when: {uri: {regex: "(unclosed"}}
    outcome: success
  - when: {uri: {regex: "` + strings.Repeat("a", 1025) + `"}}
    outcome: success
  - when: {uri: {prefix: "/` + strings.Repeat("a", 1024) + `"}}
    outcome: success
`, []string{
			"rules[0].when.uri: must set prefix or regex",
			"rules[1].when.uri: prefix and regex are mutually exclusive",
			"rules[2].when.uri.prefix: must start with /",
			"rules[3].when.uri.regex: invalid regular expression",
			"rules[4].when.uri.regex: must be at most 1024 characters",
			"rules[5].when.uri.prefix: must be at most 1024 characters",
		}},
		{"numeric ranges", "name: a\nrules:\n  - when: {latency_ms: {gte: -1}, response_bytes: {lt: 0}}\n    outcome: success\n  - when: {latency_ms: {lte: 4611686018427387905}}\n    outcome: success", []string{
			"rules[0].when.latency_ms.gte: must be at least 0 (got -1)",
			"rules[0].when.response_bytes: describes an empty range",
			"rules[1].when.latency_ms.lte: must be at most 2^62 (got 4611686018427387905)",
		}},
	})
}

func TestValidateAction(t *testing.T) {
	runValidationCases(t, KindAction, []validationCase{
		{"design doc example", `
name: web-search-actions
bind: { site: shop, client: web, endpoint_group: search }
extends: web-default
mode: shadow
rules:
  - when: { outcome: [rate_limited, captcha] }
    action: cooldown
    scope: identity
    base: 60s
    multiplier: 2
    max: 30m
    max_exponent: 0
  - when: { outcome: forbidden }
    action: quarantine
    scope: proxy
    duration: 1h
  - when: { outcome: forbidden }
    action: cooldown
    scope: account
    base: 1s
    max: 1s
escalation:
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
health: {observations: {success: 90, auth_invalid: 0}}
ban_expiry_state: active
`, nil},
		{"top level enums", "name: a\nmode: loud\nban_expiry_state: gone", []string{
			`mode: must be one of enforce|shadow (got "loud")`,
			`ban_expiry_state: must be one of pending|active (got "gone")`,
		}},
		{"outcomes", "name: a\nrules:\n  - {action: expire, scope: identity}\n  - {when: {outcome: [captcha, nope, captcha]}, action: expire, scope: identity}", []string{
			"rules[0].when.outcome: is required",
			`rules[1].when.outcome[1]: must be one of success|`,
			`rules[1].when.outcome[2]: duplicate outcome "captcha"`,
		}},
		{"count", "name: a\nrules:\n  - {when: {outcome: captcha, count: {gte: 0, within: 0s}}, action: expire, scope: identity}", []string{
			"rules[0].when.count.gte: must be at least 1 (got 0)",
			"rules[0].when.count.within: must be at least 1s (got 0s)",
		}},
		{"count permanent window", "name: a\nrules:\n  - {when: {outcome: captcha, count: {gte: 1, within: permanent}}, action: expire, scope: identity}", []string{
			"rules[0].when.count.within: must not be permanent",
		}},
		{"action and scope enums", "name: a\nrules:\n  - {when: {outcome: captcha}, action: nuke, scope: identity}\n  - {when: {outcome: captcha}, action: expire, scope: planet}\n  - {when: {outcome: captcha}, action: activate, scope: identity}", []string{
			`rules[0].action: must be one of cooldown|expire|quarantine|ban (got "nuke")`,
			`rules[1].scope: must be one of identity_endpoint|identity_site|identity|account|proxy_site|proxy (got "planet")`,
			`rules[2].action: must be one of cooldown|expire|quarantine|ban (got "activate")`,
		}},
		{"scope compatibility", `name: a
rules:
  - {when: {outcome: captcha}, action: ban, scope: identity_endpoint, duration: 1h}
  - {when: {outcome: captcha}, action: ban, scope: proxy_site, duration: 1h}
  - {when: {outcome: captcha}, action: expire, scope: account}
  - {when: {outcome: captcha}, action: expire, scope: proxy}
  - {when: {outcome: captcha}, action: quarantine, scope: account, duration: 1h}
  - {when: {outcome: captcha}, action: quarantine, scope: identity_site, duration: 1h}
`, []string{
			"rules[0].scope: ban does not support scope identity_endpoint (allowed: identity|account|proxy)",
			"rules[1].scope: ban does not support scope proxy_site (allowed: identity|account|proxy)",
			"rules[2].scope: expire does not support scope account (allowed: identity)",
			"rules[3].scope: expire does not support scope proxy (allowed: identity)",
			"rules[4].scope: quarantine does not support scope account (allowed: identity|proxy)",
			"rules[5].scope: quarantine does not support scope identity_site (allowed: identity|proxy)",
		}},
		{"cooldown max permanent", "name: a\nrules:\n  - {when: {outcome: captcha}, action: cooldown, scope: proxy, base: 1m, max: permanent}", []string{
			"rules[0].max: must not be permanent",
		}},
		{"cooldown multiplier not finite", "name: a\nrules:\n  - {when: {outcome: captcha}, action: cooldown, scope: proxy, base: 1m, multiplier: .inf}\n  - {when: {outcome: captcha}, action: cooldown, scope: proxy, base: 1m, multiplier: .nan}", []string{
			"rules[0].multiplier: must be a finite number of at least 1 (got +Inf)",
			"rules[1].multiplier: must be a finite number of at least 1 (got NaN)",
		}},
		{"reserved rule names", `name: a
rules:
  - {name: health.quarantine, when: {outcome: captcha}, action: expire, scope: identity}
  - {name: health.endpoint_low, when: {outcome: captcha}, action: expire, scope: identity}
  - {name: lifecycle.activate, when: {outcome: captcha}, action: expire, scope: identity}
  - {name: health.other, when: {outcome: captcha}, action: expire, scope: identity}
`, []string{
			`rules[0].name: "health.quarantine" is reserved`,
			`rules[1].name: "health.endpoint_low" is reserved`,
			`rules[2].name: "lifecycle.activate" is reserved`,
		}},
		{"cooldown parameters", `name: a
rules:
  - {when: {outcome: captcha}, action: cooldown, scope: identity_site}
  - {when: {outcome: captcha}, action: cooldown, scope: identity_site, base: 10m, max: 5m, multiplier: 0.5, max_exponent: 65, duration: 1h}
  - {when: {outcome: captcha}, action: cooldown, scope: identity_site, base: permanent, failure_reset_after: 500ms}
`, []string{
			"rules[0].base: is required for cooldown",
			"rules[1].multiplier: must be a finite number of at least 1 (got 0.5)",
			"rules[1].max: must be at least base (10m)",
			"rules[1].max_exponent: must be between 0 and 64 (got 65)",
			"rules[1].duration: is not supported for cooldown",
			"rules[2].base: must not be permanent",
			"rules[2].failure_reset_after: must be at least 1s (got 500ms)",
		}},
		{"ban quarantine expire durations", `name: a
rules:
  - {when: {outcome: banned}, action: ban, scope: identity}
  - {when: {outcome: banned}, action: ban, scope: identity, duration: 500ms, base: 1m, multiplier: 2, max: 1h, max_exponent: 3, failure_reset_after: 1h}
  - {when: {outcome: banned}, action: quarantine, scope: identity}
  - {when: {outcome: banned}, action: quarantine, scope: identity, duration: permanent}
  - {when: {outcome: banned}, action: expire, scope: identity, duration: 1h}
`, []string{
			"rules[0].duration: is required for ban (a duration or permanent)",
			"rules[1].base: is only supported for cooldown",
			"rules[1].multiplier: is only supported for cooldown",
			"rules[1].max: is only supported for cooldown",
			"rules[1].max_exponent: is only supported for cooldown",
			"rules[1].failure_reset_after: is only supported for cooldown",
			"rules[1].duration: must be at least 1s (got 500ms)",
			"rules[2].duration: is required for quarantine",
			"rules[3].duration: must not be permanent",
			"rules[4].duration: is not supported for expire",
		}},
		{"escalation", `name: a
escalation:
  - when: {bans: {gte: 0, within: 31d}}
  - when: {bans: {gte: 3, within: 7d}}
    duration: permanent
  - when: {bans: {gte: 3, within: 7d}}
    duration: 72h
  - when: {bans: {gte: 4, within: 0s}}
    duration: 1h
  - when: {bans: {gte: 5, within: 1d}}
    duration: 500ms
`, []string{
			"escalation[0].when.bans.gte: must be at least 1 (got 0)",
			"escalation[0].when.bans.within: must be at most 30d (got 31d)",
			"escalation[0].duration: is required (a duration or permanent)",
			"escalation[2].when.bans.gte: must be greater than escalation[1].when.bans.gte (3)",
			"escalation[2].duration: must not be shorter than escalation[1].duration (permanent)",
			"escalation[3].when.bans.within: must be at least 1s (got 0s)",
			"escalation[3].duration: must not be shorter than escalation[2].duration (3d)",
			"escalation[4].duration: must be at least 1s (got 500ms)",
			"escalation[4].duration: must not be shorter than escalation[3].duration (1h)",
		}},
		{"health", `name: a
health:
  alpha: 1.5
  baseline: 101
  tau: permanent
  observations: {success: 101, nonsense: 5, captcha: -1}
  endpoint_low_score: -1
  endpoint_low_min_samples: -1
  endpoint_low_cooldown: permanent
  quarantine_score: 100.5
  quarantine_min_samples: -2
  quarantine_duration: 500ms
`, []string{
			"health.alpha: must be greater than 0 and at most 1 (got 1.5)",
			"health.baseline: must be between 0 and 100 (got 101)",
			"health.tau: must not be permanent",
			"health.observations.captcha: must be between 0 and 100 (got -1)",
			`health.observations.nonsense: unknown outcome "nonsense"`,
			"health.observations.success: must be between 0 and 100 (got 101)",
			"health.endpoint_low_score: must be between 0 and 100 (got -1)",
			"health.endpoint_low_min_samples: must be at least 1 (got -1)",
			"health.endpoint_low_cooldown: must not be permanent",
			"health.quarantine_score: must be between 0 and 100 (got 100.5)",
			"health.quarantine_min_samples: must be at least 1 (got -2)",
			"health.quarantine_duration: must be at least 1s (got 500ms)",
		}},
		{"health nan", "name: a\nhealth: {alpha: .nan}", []string{"health.alpha: must be greater than 0 and at most 1 (got NaN)"}},
		{"cross attribution", "name: a\ncross_attribution: {window: 10ms, proxy_distinct_identities: -1, identity_distinct_proxies: -3}", []string{
			"cross_attribution.window: must be at least 1s (got 10ms)",
			"cross_attribution.proxy_distinct_identities: must be at least 1 (got -1)",
			"cross_attribution.identity_distinct_proxies: must be at least 1 (got -3)",
		}},
	})
}

func TestValidateBreaker(t *testing.T) {
	runValidationCases(t, KindBreaker, []validationCase{
		{"design doc example", `
name: search-breaker
bind: { site: shop, endpoint_group: search }
window: 60s
min_requests: 50
trip:
  risk_ratio_gte: 0.4
  distinct_captcha_identities_gte: 10
  success_ratio_lte: 0.2
open_duration: 2m
max_open_duration: 1h
half_open:
  probe_leases_per_10s: 5
  close_min_samples: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: false
`, nil},
		{"all revert modes", "name: a\nrevert_recent_cooldowns: all", nil},
		{"disabled breaker without trips", "name: a\nenabled: false\ntrip: {risk_ratio_gte: 0, distinct_captcha_identities_gte: 0, success_ratio_lte: 0}", nil},
		{"enabled breaker without trips", "name: a\ntrip: {risk_ratio_gte: 0, distinct_captcha_identities_gte: 0, success_ratio_lte: 0}", []string{
			"trip: at least one condition must be non-zero while the breaker is enabled",
		}},
		{"window and buckets", "name: a\nwindow: 500ms\nbuckets: 61", []string{
			"window: must be at least 1s (got 500ms)",
			"buckets: must be between 1 and 60 (got 61)",
		}},
		{"buckets too small", "name: a\nwindow: 10s\nbuckets: 20", []string{"buckets: must divide window (10s) into buckets of at least 1s"}},
		{"buckets not dividing", "name: a\nwindow: 10001ms\nbuckets: 7", []string{"buckets: must divide window (10s1ms) into whole milliseconds"}},
		{"window permanent", "name: a\nwindow: permanent", []string{"window: must not be permanent"}},
		{"ratios", "name: a\nmin_requests: -1\ntrip: {risk_ratio_gte: 1.1, distinct_captcha_identities_gte: -1, success_ratio_lte: -0.1}\nhalf_open: {probe_leases_per_10s: -1, close_min_samples: -1, close_success_ratio_gte: 2}", []string{
			"min_requests: must be at least 1 (got -1)",
			"trip.risk_ratio_gte: must be between 0 and 1 (got 1.1)",
			"trip.distinct_captcha_identities_gte: must be at least 0 (got -1)",
			"trip.success_ratio_lte: must be between 0 and 1 (got -0.1)",
			"half_open.probe_leases_per_10s: must be at least 1 (got -1)",
			"half_open.close_min_samples: must be at least 1 (got -1)",
			"half_open.close_success_ratio_gte: must be greater than 0 and at most 1 (got 2)",
		}},
		{"open durations", "name: a\nopen_duration: 10m\nmax_open_duration: 5m\nreset_open_count_after: permanent", []string{
			"max_open_duration: must be at least open_duration (10m)",
			"reset_open_count_after: must not be permanent",
		}},
		{"open duration permanent", "name: a\nopen_duration: permanent\nmax_open_duration: 500ms", []string{
			"open_duration: must not be permanent",
			"max_open_duration: must be at least 1s (got 500ms)",
		}},
		{"revert mode", "name: a\nrevert_recent_cooldowns: sometimes", []string{
			`revert_recent_cooldowns: must be one of none|endpoint|all or a boolean (got "sometimes")`,
		}},
	})
}

func TestNilSpecs(t *testing.T) {
	specs := []Spec{(*RotationSpec)(nil), (*SignalSpec)(nil), (*ActionSpec)(nil), (*BreakerSpec)(nil)}
	for _, s := range specs {
		require.ErrorContains(t, s.Validate(), "spec is nil")
		require.Empty(t, s.PolicyName())
		require.Nil(t, s.PolicyBinding())
		require.NotPanics(t, s.ApplyDefaults)
		require.True(t, ValidKind(s.Kind()))
	}
	require.Zero(t, (*BreakerSpec)(nil).BucketDuration())
	require.Zero(t, (&BreakerSpec{}).BucketDuration())
}

func TestPolicyBindingIsCopied(t *testing.T) {
	specs := []Spec{
		&RotationSpec{Bind: &Binding{Site: "s"}},
		&SignalSpec{Bind: &Binding{Site: "s"}},
		&ActionSpec{Bind: &Binding{Site: "s"}},
		&BreakerSpec{Bind: &Binding{Site: "s"}},
	}
	for _, s := range specs {
		b := s.PolicyBinding()
		require.Equal(t, &Binding{Site: "s"}, b)
		b.Site = "changed"
		require.Equal(t, "s", s.PolicyBinding().Site)
	}
	require.Nil(t, (&RotationSpec{}).PolicyBinding())
}

func TestApplyDefaultsProgrammatic(t *testing.T) {
	a := &ActionSpec{
		Name: "prog",
		Rules: []ActionRule{
			{When: ActionWhen{Outcome: OutcomeList{OutcomeCaptcha}}, Action: ActionCooldown, Scope: ScopeIdentitySite, Base: durationx.Duration(time.Minute)},
			{When: ActionWhen{Outcome: OutcomeList{OutcomeBanned}}, Action: ActionBan, Scope: ScopeIdentity, Duration: durationx.Permanent},
		},
	}
	a.ApplyDefaults()
	require.NoError(t, a.Validate())
	require.Equal(t, DefaultCooldownMultiplier, a.Rules[0].Multiplier)
	require.Equal(t, DefaultCooldownMaxExponent, a.Rules[0].MaxExponent)
	require.Zero(t, a.Rules[1].Multiplier)
	require.Equal(t, ModeEnforce, a.Mode)
	require.NotNil(t, a.Health.Observations)
	require.NotNil(t, a.Escalation)
	// Zero-legal fields are left alone by ApplyDefaults.
	require.Zero(t, a.Health.Baseline)
	require.False(t, a.CrossAttribution.Enabled)

	r := &RotationSpec{Name: "prog", IdentityTypes: StringList{}}
	r.ApplyDefaults()
	require.NoError(t, r.Validate())
	require.Nil(t, r.IdentityTypes)
	require.NotNil(t, r.Rotation.Quota)

	b := &BreakerSpec{Name: "prog", Trip: TripSpec{RiskRatioGte: 0.5}}
	b.ApplyDefaults()
	require.NoError(t, b.Validate())
	require.Equal(t, RevertEndpoint, b.RevertRecentCooldowns)
}

func TestHelpers(t *testing.T) {
	require.Equal(t, "a.b", field("a", "b"))
	require.Equal(t, "b", field("", "b"))
	require.Equal(t, "rules[3]", item("rules", 3))
	require.Equal(t, []Kind{KindRotation, KindSignal, KindAction, KindBreaker}, Kinds())
	require.Equal(t, "", ParentName(nil))
	require.Equal(t, "", ParentName((*SignalSpec)(nil)))
	require.Equal(t, "", ParentName((*ActionSpec)(nil)))
	require.Equal(t, "p", ParentName(&ActionSpec{Extends: "p"}))
	require.Equal(t, "", ParentName(&RotationSpec{}))

	sigs, err := SignalChain([]Spec{&SignalSpec{Name: "a"}, &SignalSpec{Name: "b"}})
	require.NoError(t, err)
	require.Len(t, sigs, 2)
	_, err = SignalChain([]Spec{&ActionSpec{}})
	require.ErrorContains(t, err, "chain element 0 is not a signal policy")
	_, err = SignalChain([]Spec{(*SignalSpec)(nil)})
	require.Error(t, err)

	acts, err := ActionChain([]Spec{&ActionSpec{Name: "a"}})
	require.NoError(t, err)
	require.Len(t, acts, 1)
	_, err = ActionChain([]Spec{&ActionSpec{}, &SignalSpec{}})
	require.ErrorContains(t, err, "chain element 1 is not an action policy")

	require.True(t, ScopeAllowed(ActionCooldown, ScopeIdentity))
	require.False(t, ScopeAllowed(ActionCooldown, "moon"))
	require.False(t, ScopeAllowed(ActionActivate, ScopeIdentity))
	require.Empty(t, allowedScopes(ActionActivate))
	require.Equal(t, SubjectKind(""), ActionScope("moon").Subject())
}
