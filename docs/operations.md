# Operations runbook

[中文文档](operations.zh-CN.md) · [Deployment guide](deployment.md) · [API reference](api.md)

Day-2 operations: how to shape a deployment after it is running, what each knob does, and what to do when
something misbehaves. Everything here is available in the console; the equivalent RPCs are named so you can
script it.

- [Tenants, namespaces, users and roles](#tenants-namespaces-users-and-roles)
- [API tokens and scopes](#api-tokens-and-scopes)
- [Sites, endpoint groups and URI rules](#sites-endpoint-groups-and-uri-rules)
- [Identity types and imports](#identity-types-and-imports)
- [Policies](#policies)
- [Breakers and site switches](#breakers-and-site-switches)
- [Manual operations and rollback](#manual-operations-and-rollback)
- [Proxies](#proxies)
- [Config center](#config-center)
- [Secrets](#secrets)
- [Notifications and webhooks](#notifications-and-webhooks)
- [Monitoring](#monitoring)
- [Hot-state rebuild](#hot-state-rebuild)
- [KEK rotation](#kek-rotation)
- [Retention](#retention)

---

## Tenants, namespaces, users and roles

```
tenant                     isolation boundary; users are bound to it by role bindings
└── namespace              owns sites, proxies, configs, secrets, tokens, policies
    └── site               owns endpoint groups, identity types, identities, accounts
```

`spnr admin init` creates the first platform administrator plus tenant `default`, namespace `default` and
its four default policies. Everything after that happens in the console (**Admin → Tenants**,
**Access → Users**) or through `TenantAdminService` / `AccessAdminService`.

**Tenants** separate teams: a tenant that owns the sites of platform A cannot see platform B at all. Use
namespaces inside a tenant to separate environments (`prod`, `staging`) or projects that share operators.
Console requests carry the active tenant in the `X-Spinneret-Tenant` header and address namespaces by
name.

**Roles** are cumulative:

| Role | Permissions |
| --- | --- |
| `viewer` | every `*:read`, plus `secret:list`, `dashboard:read`, `audit:read`, `notify:read` |
| `operator` | viewer + `identity:write` `identity:operate` `proxy:write` `proxy:operate` `policy:write` `config:write` `breaker:operate` |
| `admin` | operator + `site:write` `policy:publish` `config:publish` `secret:write` `secret:reveal` `identity:reveal` `token:read` `token:write` `notify:write` `namespace:read` |
| `owner` | admin + `namespace:write` `user:read` `user:write` |

A role binding pins a user to a tenant and optionally to **one namespace** and **a set of sites**. A
site-restricted binding cannot touch namespace-level resources (proxies, configs, secrets, tokens,
channels) — with the deliberate exception of `proxy:read`, so an operator can still see which proxy a lease
used. `extra_permissions` adds individual permissions (`config:publish`, `secret:reveal`,
`identity:reveal`, `policy:publish`) to any role without promoting it.

Platform administrators (`is_platform_admin`, created only by `spnr admin init`) pass every check and are
the only ones who can manage tenants and the KEK. Create as few as you can.

Practical setup: give crawler engineers `operator` bound to their sites, give a team lead `admin` on the
namespace, keep `owner` for whoever manages accounts. There is no delete-user RPC; disable a user instead
(**Access → Users → Disable**) — their existing sessions stop working on the next request
(`session_invalid`). Resetting a password ends every session of that user too.

---

## API tokens and scopes

Nodes authenticate with `Authorization: Bearer spn_…`. A token belongs to exactly one namespace, so node
requests never name one.

```bash
docker compose -f deploy/compose/docker-compose.yml exec spinneret \
  spnr token create --tenant default --namespace default --name crawler-hk \
    --scope lease:acquire:shop --scope report:write:shop \
    --scope config:read:crawler --scope 'secret:read:default/signing/*' \
    --expires 720h
```

The plaintext is printed once and never stored — only a SHA-256 hash and the 12-character prefix are.
The console shows it once as well (**Access → Tokens**).

| Scope | Grants |
| --- | --- |
| `lease:acquire[:<site>]` | `Acquire`, `AcquireBatch`, `Renew`, `Release` |
| `report:write[:<site>]` | `Report` |
| `config:read[:<group glob>]` | `GetConfig`, `BatchGetConfig`, `WatchConfig`, and admin config reads |
| `config:publish[:<group glob>]` | config read + write + publish |
| `secret:read:<glob over "<namespace>/<path>">` | `GetSecret`, and resolving `${secret:…}` / `secret_ref` fields |
| `identity:write[:<site>]` | identity read, write and operate (for refresher services) |
| `proxy:write` | proxy read, write and operate |
| `admin` | role `admin` inside the token's namespace |

Without the `:<site>` / `:<glob>` suffix a scope covers the whole namespace. Prefer narrow scopes: a node
that only crawls one site should not be able to lease identities of another.

Tokens can additionally carry an **IP allow-list** (CIDRs) and a **per-instance rate limit** (requests per
second), both set in the console. Verification results are cached for `SPINNERET_TOKEN_CACHE_TTL` (30 s),
but revoking publishes an event that drops the cache immediately.

Rotation: create a new token under a temporary name, roll it out, then revoke the old one
(**Access → Tokens → Revoke**). Token names are unique per namespace among *usable* tokens only, so once the
old token is revoked its name is free again and the replacement can be renamed (or re-created) under it.
Revoked tokens stay listed under their original name as an audit trail.

---

## Sites, endpoint groups and URI rules

A **site** is a target platform (`shop`) with one or more **clients** (`web`, `app`). A site can be
paused, which makes every `Acquire` fail with `site_paused`.

An **endpoint group** is the unit of rotation, cooldown and circuit breaking. Every site+client
automatically has `_default`; create more when different endpoints deserve different treatment — a search
endpoint that rate-limits aggressively should not drag down a feed endpoint.

**URI rules** map request paths to an endpoint group. Match priority is fixed:

| Kind | Pattern | Example |
| --- | --- | --- |
| `exact` | a full path | `/api/v1/feed` |
| `template` | path with `{param}` segments | `/api/v1/item/{id}` |
| `prefix` | path prefix, longest wins | `/api/v1/search` |
| `regex` | RE2, evaluated by position | `^/api/v[0-9]+/user/[0-9]+$` |

`exact` > `template` > `prefix` (longest) > `regex` (by position) > `_default`. Only the **path** is
matched: the query string is ignored (it often carries signed values). Nodes may send a full URL, a path,
or skip matching entirely by passing `endpoint_group` explicitly.

Use **Sites → URI tester** (`SiteAdminService/TestURI`) before relying on a rule set: it shows which rule
matched, which group was chosen and the four policies that resolve for it.

Set a **low watermark** per endpoint group to get an `identity_low_watermark` alert when the number of
available identities drops below it.

> Cooldowns are per endpoint group. When one specific URI needs its own cooldown, give it its own endpoint
> group with an `exact` rule — that is the intended granularity escape hatch.

---

## Identity types and imports

An **identity type** declares the payload fields of an identity and how they are delivered to nodes. It
belongs to one site and client.

```yaml
name: web_cookie
site: shop
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
  signature:  { type: string, sensitive: true }
unique_by: [cookies.sessionid]     # default: all required fields
activation: probe                  # probe | immediate
deliver:
  cookies: "{{ cookies }}"
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
  values:
    signature: "{{ signature }}"
```

```yaml
name: app_device
site: shop
client: app
fields:
  device_id:  { type: string, required: true }
  install_id: { type: string, required: true }
  cookies:    { type: cookie_map, sensitive: true }
  extra:      { type: json }
unique_by: [device_id]
activation: immediate
deliver:
  query:
    device_id: "{{ device_id }}"
    install_id: "{{ install_id }}"
  cookie_header: "{{ cookies }}"
  json: "{{ extra }}"
```

Field types: `string`, `number`, `bool`, `cookie_map`, `json`, `secret_ref`. `sensitive: true` fields are
encrypted at rest, masked in the console (unless the caller has `identity:reveal`) and never logged.
A `secret_ref` field stores a **vault path** that is resolved into the credential at lease time — writing
such a payload requires read access to that secret.

Delivery templates are placeholders only, no expressions. A value that is exactly `{{ path }}` yields the
typed value; anything else interpolates as a string. A `cookie_map` rendered into `cookie_header` becomes
`k1=v1; k2=v2` with sorted keys. The resulting credential always has the same six segments — `cookies`,
`cookie_header`, `headers`, `query`, `json`, `values` — so nodes never need to know the type.

Use **Identity types → Delivery preview** (`PreviewDelivery`) to see the rendered credential for a sample
payload before importing anything.

### Importing identities

Console: **Identities → Import**. API: `IdentityAdminService/ImportIdentities`. Up to 50 000 rows or
32 MiB per call, always available as a **dry run** first.

**JSON Lines** — one object per line. With a `payload` key the rest are attributes; without it the whole
object is the payload minus the reserved keys:

```jsonl
{"payload":{"cookies":{"sessionid":"abc","csrftoken":"x1"},"user_agent":"Mozilla/5.0"},"account":"acct-1","region":"HK","tags":["batch-09"],"labels":{"vendor":"a"}}
{"cookies":{"sessionid":"def"},"user_agent":"Mozilla/5.0","_account":"acct-2","_region":"SG","_tags":["batch-09"]}
```

**CSV** — the header row is the field names; `_account`, `_region` and `_tags` (`;`-separated) are reserved
columns. `cookie_map` cells accept a `Cookie` header string, `json` cells accept JSON:

```csv
cookies,user_agent,_account,_region,_tags
"sessionid=abc; csrftoken=x1",Mozilla/5.0,acct-1,HK,batch-09;web
```

`cookie_map` also accepts a browser export array of `{"name":…,"value":…}` objects.

Semantics: rows are validated against a JSON Schema generated from the field definitions; failures are
returned per row with a line number. Deduplication uses `unique_by`. A row matching an existing identity
with a **changed** payload creates a new payload version (last 5 kept), resets health to the baseline and
moves `active`/`pending`/`quarantined`/`expired` identities back to `pending` (or `active` with
`activation: immediate`); `banned`, `disabled` and `retired` identities keep their state. An unchanged
payload counts as `unchanged`.

### Refreshing expired identities

When an identity expires, Spinneret emits an `identity_expired` alert. A refresher service can subscribe to
the webhook or poll `ListIdentities(state=expired)`, obtain fresh credentials and call
`UpdateIdentityPayload` — the identity returns to `pending` and is re-validated by probe leases.

### Accounts

Several identities can share an `account_id` ("these five cookies are the same account"). An account-scoped
ban applies to all of its identities, and action rules can propagate a ban from an identity to its account.

---

## Policies

Four policy kinds decide everything the server does automatically. Each is YAML, versioned, with a draft
you edit and a published version that takes effect. Policies live in a namespace and are **bound** to
scopes.

```
binding on (site, client, endpoint_group)   most specific
      > (site, client)
      > (site)
      > namespace default (no site)
      > built-in default                     least specific
```

Resolution happens per endpoint group, per kind: a site can use the namespace rotation policy while one
endpoint group overrides only the breaker policy. **Policies → Resolve** shows the effective four for any
group. `signal` and `action` policies also support `extends: <name>` (parent rules first, child rules
appended; depth ≤ 5, cycles rejected).

Workflow: edit the draft (`policy:write`) → **Validate** → **Publish** (`policy:publish`, creates a
version) → optionally **Rollback** (re-publishes an old version's YAML as a new version). Publishing
invalidates the catalog, so changes are live within about a second. Every new namespace starts with
`default-rotation`, `default-signal`, `default-action` and `default-breaker` bound at namespace level.

Durations accept `ms`, `s`, `m`, `h`, `d` and `permanent`. Unknown fields are rejected — a typo fails
validation instead of being silently ignored.

### Rotation

Who gets leased, for how long, how often, and with which proxy.

```yaml
name: web-search-rotation
identity_types: []                # empty = every type of the site + client
rotation:
  strategy: weighted_random       # weighted_random | least_recently_used | round_robin | best_health
  candidate_sample: 32            # candidates sampled per acquire (1..256)
  lease_ttl: 2m                   # 5s..30m
  max_lease_lifetime: 30m         # renewals can never exceed this
  max_concurrent_leases: 1        # 1 = exclusive use of an identity
  reuse_interval: 30s             # minimum gap between two uses of one identity
  reuse_anchor: released          # acquired | released
  reuse_scope: endpoint_group     # endpoint_group | site
  quota:
    - { limit: 60, window: 1h }   # per identity, per endpoint group
  sticky:
    enabled: true                 # same session_key reuses the same identity
    ttl: 10m
  warmup:
    duration: 24h                 # new identities ramp up slowly
    quota_factor: 0.2
  probe:
    weight_factor: 0.1            # how often pending/half-open probes are chosen
    max_leases: 2
proxy:
  mode: bind_identity             # none | pool | bind_identity | region_match
  kinds: [residential]            # datacenter | residential | mobile | tunnel
  regions: [HK, SG]
  region_match: false             # require the proxy region to match the identity region
  rebind_tolerance: 5m            # how long a bound proxy may be unavailable before rebinding
  max_rebinds_per_day: 3
```

`reuse_interval` is the single most effective anti-detection knob: with `reuse_anchor: released` the clock
starts when the lease ends, so a slow request does not shorten the gap. `reuse_scope: site` makes the gap
apply across all endpoint groups of the site.

### Signal

Turns the facts a node reported into one of twelve **outcomes**: `success`, `empty`, `rate_limited`,
`captcha`, `auth_invalid`, `forbidden`, `banned`, `proxy_error`, `network_error`, `target_error`,
`client_error`, `unknown`. First matching rule wins; no match means `unknown`.

```yaml
name: web-signals
trust_outcome_hint: false         # true lets nodes propose the outcome
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
  - name: business-code-risk
    when: { business_code: ["2154", "8"] }
    outcome: captcha
  - name: rate-limited
    when: { http_status: [429] }
    outcome: rate_limited
  - name: search-empty
    when: { http_status: [200], markers: [empty_list], uri: { prefix: /api/v1/search } }
    outcome: empty
    blame: identity               # optional: identity | proxy | none
  - name: target-error
    when: { http_status: { gte: 500 } }
    outcome: target_error
  - name: success
    when: { http_status: { gte: 200, lt: 300 } }
    outcome: success
```

`when` keys: `http_status` (list, or `{gte,gt,lte,lt}`; `0` means "no response"), `business_code` (list),
`error_kind` (list), `markers` (matches if any is present), `uri` (`{prefix}` or `{regex}`, RE2), `method`
(list), `latency_ms` and `response_bytes` (ranges). All keys in one rule must match.

Test rules against a real report with **Policies → Rule debugger** (`PolicyAdminService/DebugReport`)
before publishing.

### Action

Turns outcomes into cooldowns, bans, expiry and quarantine, and maintains health scores.

```yaml
name: web-search-actions
mode: enforce                     # enforce | shadow (evaluate and record, change nothing)
rules:
  - name: rate-limited-cooldown
    when: { outcome: rate_limited }
    action: cooldown
    scope: identity_endpoint
    base: 60s
    multiplier: 2                 # exponential on consecutive failures
    max: 30m
    max_exponent: 10
    failure_reset_after: 1h
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
escalation:                       # applied when a temporary ban is issued
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
health:
  alpha: 0.1                      # EWMA weight of each observation
  baseline: 70
  tau: 6h                         # decay back towards the baseline
  observations: {}                # overrides: success 100, empty 60, network_error 50,
                                  # rate_limited 30, forbidden 10, captcha 0
  endpoint_low_score: 15
  endpoint_low_min_samples: 10
  endpoint_low_cooldown: 6h
  quarantine_score: 20
  quarantine_min_samples: 10
  quarantine_duration: 24h
ban_expiry_state: pending         # state an identity returns to when a ban expires
cross_attribution:
  enabled: true
  window: 10m
  proxy_distinct_identities: 3    # 3 identities failing on one proxy blames the proxy
  identity_distinct_proxies: 3    # one identity failing on 3 proxies blames the identity
```

Valid action/scope pairs: `cooldown` → `identity_endpoint`, `identity_site`, `account`, `proxy_site`,
`proxy`; `ban` → `identity`, `account`, `proxy`; `expire` → `identity`; `quarantine` → `identity`, `proxy`.
Every matching rule is evaluated and the most severe action per subject wins.

**Start in `mode: shadow`** when you introduce or change a rule set. Shadow mode evaluates everything and
records what it *would* have done (visible in risk events and `spinneret_actions_total{mode="shadow"}`)
without cooling down or banning anything.

### Breaker

Per endpoint group, sliding window, three states.

```yaml
name: search-breaker
enabled: true
window: 60s
buckets: 12                       # 5-second resolution
min_requests: 50                  # below this the window is not evaluated
trip:                             # any condition opens the breaker; 0 disables a condition
  risk_ratio_gte: 0.4
  distinct_captcha_identities_gte: 10
  success_ratio_lte: 0.2
open_duration: 2m                 # doubles on repeated opens
max_open_duration: 1h
reset_open_count_after: 30m
half_open:
  probe_leases_per_10s: 5
  close_min_samples: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: endpoint # none | endpoint | all
```

`revert_recent_cooldowns` matters: when a whole endpoint breaks, the identities that hit it were not
necessarily burnt. `endpoint` (the default) undoes the identity×endpoint cooldowns applied during the
tripping window, `all` also undoes identity×site cooldowns, `none` keeps them.

---

## Breakers and site switches

**Breakers page** (`BreakerAdminService`). Each endpoint group shows its state (`closed` / `open` /
`half_open`), the current window counters and the transition history.

- `closed → open` happens automatically when a trip condition is met, or manually
  (**Open** with a duration and a reason, `OpenBreaker`).
- `open → half_open` happens automatically after `open_duration`. Only probe leases are issued
  (`probe_leases_per_10s`); their reports decide the next transition.
- `half_open → closed` when `close_min_samples` probes reach `close_success_ratio_gte`; back to `open`
  otherwise, with a longer `open_duration`.
- **Close** (`CloseBreaker`) forces it closed immediately — use it after you fixed the real cause.

While a breaker is open, `Acquire` for that group fails with `503` / `circuit_open` and a retry hint.
Other endpoint groups of the same site are unaffected.

**Site switches** (`SetSitePaused`, also on the Breakers page) stop all acquisition for a site
(`503` / `site_paused`). Use them for maintenance, for a platform-wide incident, or to stop a runaway job
without touching the nodes. Reports for existing leases are still accepted while a site is paused.

Both are visible live: the console subscribes to `breaker.transition` events over SSE, and every transition
can raise a notification (`breaker_opened`, `breaker_reopened`, `breaker_closed`).

---

## Manual operations and rollback

**Single and bulk operations** (`OperateIdentities`, `BulkOperateIdentities`, `OperateAccount`,
`OperateProxies`): cooldown with a scope and duration, ban (fixed duration or permanent), unban,
quarantine, expire, disable, retire, enable. Every operation takes a **reason**, is written to the audit
log and to the identity's state events, and shows up in the console's identity timeline.

**`RevertActions`** is the antidote to a misfiring rule. It reverts the automatic actions recorded in a
time range:

| Field | Meaning |
| --- | --- |
| `time_range` | Required; `start` must be set so a revert can never silently cover all history |
| `site`, `policy_id`, `rule` | Narrow to one site, the policy that produced the actions, or a single rule name |
| `actions` | Which to revert; empty means `ban`, `quarantine`, `expire` and `cooldown` |
| `reset_failures` | Also clear consecutive-failure streaks (so backoff restarts at `base`) |
| `reset_health` | Also reset health scores to the baseline |
| `dry_run` | List the affected identities and change nothing |

Typical incident: a signal rule misclassified a target-side 500 storm as `captcha`, which banned a few
hundred identities.

1. Fix the signal policy and publish it.
2. `RevertActions` with `dry_run: true`, the incident's time range and `rule: captcha-ban` — check the
   count and the identity list.
3. Re-run with `dry_run: false`, `reset_failures: true` and `reset_health: true`.
4. Watch the Identities page: reverted identities return to their pre-action state.

---

## Proxies

Proxies belong to a namespace and are offered to sites through the rotation policy's `proxy` section.
Attributes: URL (encrypted at rest), `kind` (`datacenter` / `residential` / `mobile` / `tunnel`), `region`,
`city`, `provider`, `tags`, `max_concurrency` and an optional `session_template` for rotating-session
gateways.

**Assignment modes** (rotation policy): `none` (no proxy), `pool` (one from the filtered pool per lease),
`bind_identity` (an identity keeps the same proxy, rebinding only when it is unavailable longer than
`rebind_tolerance`, at most `max_rebinds_per_day` times), `region_match` (the proxy region must match the
identity's).

### Importing

**Proxies → Import** (`ImportProxies`), three formats, dry run available, deduplicated by URL:

```text
# lines: one URL per line, optional space-separated key=value pairs
http://user:pass@1.2.3.4:8080 kind=residential region=HK tags=pool-a,fast
socks5://user:pass@1.2.3.5:1080 kind=datacenter max_concurrency=4
```

```jsonl
{"url":"http://user:pass@1.2.3.4:8080","kind":"residential","region":"HK","provider":"acme","tags":["pool-a"],"max_concurrency":2}
```

```csv
url,kind,region,city,provider,tags,max_concurrency,session_template
http://user:pass@1.2.3.4:8080,residential,HK,Hong Kong,acme,pool-a;fast,2,
```

Blank lines and `#` comments are ignored in `lines`. `ProxyDefaults` in the request fills attributes a row
does not set.

### Health checks

Every `SPINNERET_PROXY_CHECK_INTERVAL` (60 s) each proxy is used to fetch `SPINNERET_PROXY_CHECK_URL`
(`http://example.com/`) with `SPINNERET_PROXY_CHECK_TIMEOUT` (10 s). Failures move a proxy
to `dead`; successes revive it. With `SPINNERET_PROXY_EXIT_IP_URL` set, the check also records the exit IP,
and with `SPINNERET_GEOIP_DB` it fills empty regions from that IP.

**Check now** on a proxy row (`CheckProxy`) runs the probe immediately and shows the result — the first
thing to try when nodes report `no_proxy_available`. On a host without internet access, point
`SPINNERET_PROXY_CHECK_URL` at something reachable or the whole pool will go `dead`.

Proxies also get cooldowns from action rules (`proxy_site`, `proxy` scopes) and appear with per-provider
success statistics under **Proxies → Providers** (`GetProviderStats`).

---

## Config center

Versioned configuration delivered to nodes, so crawler settings change without a redeploy.

An item is addressed by `group` + `key` inside a namespace (`crawler` / `search.json`), has a format
(`json`, `yaml`, `text`), a draft and published versions. Version numbers never repeat for a
namespace/group/key, including after a delete and re-create.

Workflow (**Config** page or `ConfigAdminService`): edit the draft → **Publish** (`config:publish`) →
compare versions with the diff view → **Rollback** re-publishes an old version as a new one.

Nodes read it with `GetConfig` / `BatchGetConfig` and follow it with `WatchConfig`, a long poll that
returns as soon as a watched version changes (default wait 30 s, max 60 s). Both SDKs wrap this in a
`ConfigWatcher` with change callbacks and an atomic local snapshot, so a node still starts when the control
plane is briefly unreachable.

**Secret references.** Content may contain `${secret:<path>}` or `${secret:<path>#<version>}`; the server
resolves them when serving the item, which requires the caller's token to hold a matching
`secret:read:<namespace>/<path>` scope. Resolved items are flagged `has_secret_refs: true` and the SDKs
refuse to write them to a plaintext snapshot.

**The `_runtime` group** is reserved and read-only. It exposes the current state of `breakers` and
`site_switches`, so a node can watch it and back off locally before its next `Acquire` even fails.

---

## Secrets

The vault stores shared secrets (signing keys, API keys, account passwords) with AES-256-GCM envelope
encryption. A secret has a path inside its namespace (`signing/api_key`, lower-case, `[a-z0-9_./-]`),
versions, and an optional expiry.

| Action | Permission | Notes |
| --- | --- | --- |
| List (metadata only) | `secret:list` | Values are never in list responses |
| Create / update (new version) | `secret:write` | |
| Reveal a value in the console | `secret:reveal` | Typed confirmation in the UI; audited as `secret.read` |
| Read from a node | token scope `secret:read:<ns>/<path glob>` | Audited as `secret.read` |

Three ways to consume a secret without pasting it anywhere:

1. `${secret:path}` in a config item,
2. a `secret_ref` field in an identity type, resolved into the credential at lease time,
3. `SecretService/GetSecret` from a node.

Expiry raises a `secret_expiring` alert 7 days ahead. **Secrets → Access logs** lists every read with who,
when and from where.

Writing an identity payload that references a secret requires read access to that secret — otherwise an
operator could exfiltrate any secret by pointing an identity field at it and leasing the identity.

---

## Notifications and webhooks

**Channels** (`NotificationAdminService`, **Notifications** page) belong to a tenant and can be restricted
to specific sites. Kinds: `webhook`, `feishu`, `dingtalk`, `wecom`, `telegram`. Each subscribes to a set of
event types and can be tested (**Test alert**) and disabled without deleting.

Alert kinds: `breaker_opened`, `breaker_reopened`, `breaker_closed`, `identity_low_watermark`,
`proxy_low_watermark`, `ban_spike`, `report_backlog`, `unknown_ratio_high`, `client_error_spike`,
`identity_expired`, `secret_expiring`, `test`. Severities are `info`, `warning`, `critical`. Identical
alerts are de-duplicated for 10 minutes by default.

### Webhook payload

`POST` with `Content-Type: application/json`:

```json
{
  "id": "alt_01a0b0ff449e78a49c2d8cc240515316",
  "kind": "breaker_opened",
  "severity": "critical",
  "title": "Breaker opened: shop/search",
  "message": "risk_ratio 0.62 over 60s (min_requests 50)",
  "tenant": "default",
  "namespace": "default",
  "site": "shop",
  "details": { "endpoint_group": "search", "risk_ratio": 0.62, "open_duration": "2m" },
  "created_at": "2026-09-17T20:11:54.243Z"
}
```

### Verifying the signature

When the channel has a secret, deliveries carry two headers:

```http
X-Spinneret-Timestamp: 1758140314
X-Spinneret-Signature: sha256=6f1c…
```

The signature is `"sha256=" + hex(HMAC_SHA256(secret, timestamp + "." + raw_body))`. Verify over the **raw**
body, before JSON parsing, and reject timestamps that are too old:

```python
import hashlib, hmac, time

def verify(secret: str, timestamp: str, body: bytes, signature: str, tolerance: int = 300) -> bool:
    if abs(time.time() - int(timestamp)) > tolerance:
        return False
    expected = "sha256=" + hmac.new(
        secret.encode(), timestamp.encode() + b"." + body, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(expected, signature)
```

```go
func verify(secret, timestamp string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}
```

Feishu and DingTalk use their own provider signatures, computed from the channel secret automatically.
Custom headers can be added per channel, except the two signature headers.

Delivery results (`ok`, error, attempts) are stored with each alert; **Notifications → Alert history**
shows them.

---

## Monitoring

`/healthz` (liveness), `/readyz` (PostgreSQL, Redis, hot state, catalog; `draining` while shutting down)
and `/metrics` (Prometheus). The Compose stack has a `prometheus` profile that scrapes both replicas.

| Metric | Labels | What it tells you |
| --- | --- | --- |
| `spinneret_acquire_total` | `site`, `group`, `result` | Lease attempts and why they failed |
| `spinneret_acquire_duration_seconds` | `site` | Server-side acquire latency (target p99 < 5 ms) |
| `spinneret_report_ingest_total` | `result` (accepted/duplicated/rejected) | Ingest health |
| `spinneret_report_total` | `site`, `group`, `outcome` | The outcome mix — your primary risk signal |
| `spinneret_report_lag_seconds` | — | Receipt → processing delay |
| `spinneret_report_process_duration_seconds` | — | Worker processing time per report |
| `spinneret_identities` | `site`, `type`, `state` | Lifecycle distribution |
| `spinneret_identities_available` | `site`, `group` | Capacity per endpoint group |
| `spinneret_actions_total` | `site`, `action`, `scope`, `mode` | Cooldowns/bans applied (`mode=shadow` for dry runs) |
| `spinneret_breaker_state` | `site`, `group` | 0 closed, 1 half-open, 2 open |
| `spinneret_breaker_transitions_total` | `site`, `group`, `to` | Flapping detection |
| `spinneret_proxies` | `site`, `state` | Proxy pool health |
| `spinneret_stream_pending` | `shard` | Unacknowledged report entries per shard |
| `spinneret_stream_owned_shards` | — | Shards owned by this instance (sum = `SPINNERET_REPORT_SHARDS`) |
| `spinneret_lease_reaped_total` | `site`, `kind` (expired/abandoned) | Nodes that never report |
| `spinneret_config_watchers` | — | Active long polls on this instance |
| `spinneret_http_requests_total` / `_duration_seconds` | `procedure`, `code` | Per-RPC traffic and latency |
| `spinneret_notify_deliveries_total` | `kind`, `result` | Alert delivery failures |
| `spinneret_db_write_batches_total` | `writer`, `result` | Batched persistence failures |
| `spinneret_job_runs_total` / `_duration_seconds` | job | Background job health |
| `spinneret_state_writer_pending_changes` / `_spilled_changes_total` / `_dropped_changes_total` | — | State persistence backpressure (spilled = written synchronously, dropped = lost) |

Suggested alerts:

| Alert | Expression sketch | Why |
| --- | --- | --- |
| Instance down | `up{job="spinneret"} == 0 for 1m` | |
| Not ready | `/readyz` non-2xx for 2m | Dependency loss or a stuck rebuild |
| Acquire failures | `rate(spinneret_acquire_total{result!="ok"}[5m]) / rate(spinneret_acquire_total[5m]) > 0.05 for 10m` | Capacity or breaker problem |
| Acquire latency | `histogram_quantile(0.99, rate(spinneret_acquire_duration_seconds_bucket[5m])) > 0.02 for 10m` | Redis or CPU saturation |
| Report backlog | `sum(spinneret_stream_pending) > 50000 for 10m`, or `report_lag_seconds` p99 > 60 s | Workers cannot keep up |
| Unowned shards | `sum(spinneret_stream_owned_shards) < SPINNERET_REPORT_SHARDS for 5m` | A shard has no consumer |
| Breaker open | `max(spinneret_breaker_state) by (site,group) == 2 for 5m` | Target-side incident |
| Breaker flapping | `increase(spinneret_breaker_transitions_total{to="open"}[1h]) > 5` | Thresholds too tight |
| Capacity low | `spinneret_identities_available < <low watermark> for 10m` | Refresh identities |
| Risk spike | `rate(spinneret_report_total{outcome=~"captcha|banned|rate_limited"}[5m])` above baseline | Detection kicked in |
| Unknown outcomes | `rate(spinneret_report_total{outcome="unknown"}[15m]) / rate(spinneret_report_total[15m]) > 0.1` | Signal rules no longer match reality |
| Abandoned leases | `rate(spinneret_lease_reaped_total{kind="abandoned"}[15m])` rising | Nodes crash before reporting |
| Alert delivery | `rate(spinneret_notify_deliveries_total{result!="ok"}[15m]) > 0` | You are blind to alerts |

Spinneret raises several of these itself as notification alerts (`report_backlog`, `identity_low_watermark`,
`proxy_low_watermark`, `ban_spike`, `unknown_ratio_high`, `client_error_spike`) — use both: Prometheus for
infrastructure, Spinneret alerts for domain events.

In the console, **Overview** gives per-site QPS, outcome mix and breaker counts, **Heatmap** shows identity
× endpoint-group availability and scores, **Requests** is a per-report explorer (needs ClickHouse) and
**Risk events** lists every automatic action with its rule and blame.

---

## Hot-state rebuild

Redis holds only derived state; PostgreSQL is the source of truth. Rebuild when Redis lost data, after a
restore, or when hot state and database disagree.

```bash
# everything: deletes the hot-state epoch, re-materializes every site
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr rebuild

# one site only, no fleet-wide downtime
docker compose -f deploy/compose/docker-compose.yml exec spinneret \
  spnr rebuild --tenant default --namespace default --site shop
```

A full rebuild takes a PostgreSQL advisory lock (only one runs at a time), writes site metadata, restores
health scores from the periodic `hot_state_snapshots`, re-materializes proxies and identities, spreads
ready times over the next 60 s to avoid a thundering herd, prunes stale queue members and finally publishes
a new epoch. API instances answer `503` / `rebuilding` until it completes, and nodes retry.

Instances also rebuild automatically at startup when the epoch key is missing — losing Redis does not
require manual intervention, only patience.

---

## KEK rotation

See [deployment.md → KEK management](deployment.md#kek-management) for the full procedure. In short:

```bash
spnr kek generate --id k2 >> deploy/compose/secrets/kek.key   # add and make current
docker compose ... up -d --wait spinneret                     # restart to load it
spnr kek rewrap                                               # re-wrap every DEK, follows progress
spnr kek status                                               # 0 records on the old key → remove it
```

The console does the same under **Secrets → KEK** (platform administrators only): it shows configured
KEKs, how many records each wraps and the progress of a running rewrap. Rotation is online — every
configured key can still unwrap, so requests keep working throughout.

---

## Retention

| Data | Variable | Default | Where |
| --- | --- | --- | --- |
| Risk events | `SPINNERET_RETENTION_RISK_EVENTS` | 30 d | PostgreSQL, daily partitions |
| Minute statistics | `SPINNERET_RETENTION_MINUTE_STATS` | 30 d | PostgreSQL |
| Hour statistics | `SPINNERET_RETENTION_HOUR_STATS` | 180 d | PostgreSQL |
| State events | `SPINNERET_RETENTION_STATE_EVENTS` | 365 d | PostgreSQL |
| Audit log | `SPINNERET_RETENTION_AUDIT` | 365 d | PostgreSQL |
| Raw request events | `SPINNERET_CLICKHOUSE_TTL_DAYS` | 90 d | ClickHouse |

The hourly `partition_manager` job (leader only) creates partitions ahead of time and drops those entirely
older than the retention period; ClickHouse enforces its own TTL. Other bounded data: identity payload
versions (last 5 kept), ban history (30 days), report de-duplication keys
(`SPINNERET_REPORT_DEDUP_TTL`, 1 h) and report stream entries (each shard owner trims its stream to the
consumer group position every few seconds, so only the real backlog is kept, capped at
`SPINNERET_STREAM_MAXLEN` per shard).

Raising a retention period only affects data written from then on; lowering one drops partitions on the
next job run. Keep the audit retention at or above whatever your compliance requires — it is the only
record of who revealed which secret.
