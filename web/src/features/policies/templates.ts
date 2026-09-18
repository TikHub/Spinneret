import { type PolicyKind } from './constants';

/*
 * Starting points for new policies: the rotation and breaker templates carry
 * every default (spec section 7), the signal and action templates are the
 * rules of design doc sections 7.3 and 8.4 as shipped in the built-in default
 * policies (internal/policy/defaults), plus a commented business-code example.
 * NAME is replaced by the chosen policy name.
 */

const ROTATION = `name: NAME
# Empty list = every identity type of the site + client.
identity_types: []
rotation:
  strategy: weighted_random       # weighted_random | least_recently_used | round_robin | best_health
  candidate_sample: 32            # candidates sampled per acquire (1..256)
  lease_ttl: 2m                   # 5s..30m
  max_lease_lifetime: 30m         # renewals cannot extend a lease beyond this
  max_concurrent_leases: 1        # 1 = exclusive
  reuse_interval: 0s              # minimum gap between two uses of an identity
  reuse_anchor: released          # acquired | released
  reuse_scope: endpoint_group     # endpoint_group | site
  quota: []                       # e.g. [{limit: 60, window: 1h}]
  sticky:
    enabled: false
    ttl: 10m
  warmup:
    duration: 0s
    quota_factor: 1
  probe:
    weight_factor: 0.1
    max_leases: 2
proxy:
  mode: none                      # none | pool | bind_identity | region_match
  kinds: []                       # datacenter | residential | mobile | tunnel
  tags: []
  providers: []
  regions: []
  region_match: false
  rebind_tolerance: 5m
  max_rebinds_per_day: 3
`;

const SIGNAL = `name: NAME
# Rules are evaluated top-down; the first match wins. Unmatched reports are
# classified as "unknown" (or the node's outcome_hint when trusted and valid).
trust_outcome_hint: false
rules:
  - name: proxy-error
    when: { error_kind: [proxy_auth, conn_refused] }
    outcome: proxy_error
  - name: network-error
    when: { error_kind: [timeout, conn_reset, tls, dns] }
    outcome: network_error
  - name: captcha
    when: { markers: [captcha_page] }
    outcome: captcha
  - name: login-redirect
    when: { markers: [login_redirect] }
    outcome: auth_invalid
  - name: rate-limited
    when: { http_status: [429] }
    outcome: rate_limited
  - name: target-error
    when: { http_status: { gte: 500 } }
    outcome: target_error
  # Example business codes; use the values of the target site.
  # - name: business-forbidden
  #   when: { http_status: [200], business_code: ["10001", "10002"] }
  #   outcome: forbidden
  - name: empty-list
    when: { http_status: [200], markers: [empty_list] }
    outcome: empty
  - name: client-error
    when: { http_status: [400] }
    outcome: client_error
  - name: auth-invalid
    when: { http_status: [401] }
    outcome: auth_invalid
  - name: forbidden
    when: { http_status: [403] }
    outcome: forbidden
  - name: success
    when: { http_status: { gte: 200, lt: 300 } }
    outcome: success
`;

const ACTION = `name: NAME
# Every matching rule is evaluated; the most severe action per subject wins.
mode: enforce                     # enforce | shadow
rules:
  - name: rate-limited-cooldown
    when: { outcome: rate_limited }
    action: cooldown
    scope: identity_endpoint
    base: 60s
    multiplier: 2
    max: 30m
  - name: empty-cooldown
    when: { outcome: empty, count: { gte: 3, within: 10m } }
    action: cooldown
    scope: identity_endpoint
    base: 5m
  - name: captcha-cooldown
    when: { outcome: captcha }
    action: cooldown
    scope: identity_site
    base: 30m
    multiplier: 2
    max: 6h
  - name: captcha-ban
    when: { outcome: captcha, count: { gte: 3, within: 24h } }
    action: ban
    scope: identity
    duration: 12h
  - name: auth-invalid-expire
    when: { outcome: auth_invalid }
    action: expire
    scope: identity
  - name: banned-account
    when: { outcome: banned }
    action: ban
    scope: account
    duration: permanent
  - name: proxy-error-cooldown
    when: { outcome: proxy_error }
    action: cooldown
    scope: proxy_site
    base: 2m
    max: 30m
escalation:                       # evaluated when a temporary ban is applied
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
ban_expiry_state: pending         # pending | active
`;

const BREAKER = `name: NAME
enabled: true
window: 60s
buckets: 12
min_requests: 50
trip:                             # any condition opens the breaker; 0 disables a condition
  risk_ratio_gte: 0.4
  distinct_captcha_identities_gte: 10
  success_ratio_lte: 0.2
open_duration: 2m
max_open_duration: 1h
reset_open_count_after: 30m
half_open:
  probe_leases_per_10s: 5
  close_min_samples: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: endpoint # none | endpoint | all
`;

const TEMPLATES: Readonly<Record<PolicyKind, string>> = {
  rotation: ROTATION,
  signal: SIGNAL,
  action: ACTION,
  breaker: BREAKER,
};

/** Template YAML of a kind with the given policy name. */
export function policyTemplate(kind: PolicyKind, name: string): string {
  return TEMPLATES[kind].replace(/^name: NAME$/m, `name: ${name}`);
}
