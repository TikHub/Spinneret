# Concepts

**The mental model of Spinneret. Read this once and you will be able to predict what a request
will do before you send it: which identity it gets, through which proxy, what a failure costs,
and who decides.**

[中文](../zh/04-concepts.md)

---

## Contents

- [Control plane and node](#control-plane-and-node)
- [The isolation model: platform, tenant, namespace](#the-isolation-model-platform-tenant-namespace)
- [The addressing model: site, client, endpoint group](#the-addressing-model-site-client-endpoint-group)
- [Identities](#identities)
- [The identity state machine](#the-identity-state-machine)
- [Leases](#leases)
- [Reports](#reports)
- [Outcomes, blame and actions](#outcomes-blame-and-actions)
- [Health score](#health-score)
- [Circuit breakers](#circuit-breakers)
- [Proxies](#proxies)
- [Configuration and secrets](#configuration-and-secrets)
- [Policies and the binding hierarchy](#policies-and-the-binding-hierarchy)
- [One request, end to end](#one-request-end-to-end)

---

## Control plane and node

A control plane owns the *state* around requests and hands it out one request at a time: which
identity is usable right now, through which proxy, whether the endpoint is currently circuit-broken,
what the node's configuration and secrets are. The node owns the *request*: it builds it, signs it if
the target needs signing, sends it, parses the response, and reports what happened.

Spinneret never contacts a target site. It has no signing algorithms, no login flows and no captcha
solving. Everything it knows about a target comes from what nodes report back.

```text
node                                  Spinneret
 |  Acquire(site, client, uri)  ----->  pick identity + proxy, open a lease
 |  <----- identity + credential + proxy + lease_id
 |
 |  ... the node sends the request to the target, reads the response ...
 |
 |  Report(lease_id, status, markers, latency)  ----->  classify, cool down,
 |                                                      ban, score, trip breakers
```

A node needs exactly two configuration values of its own: the server URL and an API token.
Everything else is fetched at request time, so changing a cooldown rule, adding identities or
pausing a site takes effect within seconds and never requires redeploying a node.

---

## The isolation model: platform, tenant, namespace

Three levels. A **tenant** is the hard isolation boundary; a **namespace** is a partition inside a
tenant and is the practical "team" unit. Every piece of operational data belongs to exactly one
namespace.

```text
platform
└── tenant  "acme"                      hard isolation boundary
    ├── namespace  "prod"               the team / environment unit
    │   ├── sites
    │   │   └── site  "example-site"
    │   │       ├── clients        web, mobile, partner
    │   │       ├── endpoint groups (per client)   search, detail, _default
    │   │       │   └── URI rules
    │   │       ├── identity types (per client)    web_cookie, app_device
    │   │       ├── accounts
    │   │       └── identities
    │   ├── proxy pool                  shared by every site of the namespace
    │   ├── policies + policy bindings
    │   ├── config groups → config items
    │   └── secrets (vault paths)
    └── namespace  "staging"
        └── … completely separate data
```

What this buys you:

| Question | Answer |
| --- | --- |
| Can a user in namespace `prod` see the identities of `staging`? | No. Identities, credentials, proxies, accounts, policies, config items, secrets and audit rows are all namespace-scoped. |
| Can a user in tenant A see anything of tenant B? | No. A user's role bindings are per tenant; a user with no binding in a tenant cannot see the tenant at all. |
| Who sees everything? | A platform administrator (`users.is_platform_admin`). Platform administrators also hold `tenant:manage` and `kek:manage`, which nobody else can hold. |
| What is an API token scoped to? | Exactly one namespace, chosen when the token is created. A node can never reach another namespace, whatever it asks for. |

A user is granted access through **role bindings**. One binding carries a tenant, a role
(`viewer`, `operator`, `admin`, `owner`), optionally one namespace, optionally a list of site IDs,
and optionally a few extra permissions. A binding without a namespace applies to every namespace of
the tenant; a binding with `site_ids` restricts the site-scoped permissions to those sites.

Node traffic does not use roles at all. `lease:acquire`, `report:write` and `secret:read` are
node-only permissions and can only be granted through API token scopes.

See [Tenants, users and tokens](./11-access-control.md) for the full permission list.

---

## The addressing model: site, client, endpoint group

A node never says "give me identity X". It says *what it is about to do*, and Spinneret resolves
that to a configuration.

```text
site            example-site        one target, one namespace
 └── client     web | mobile | partner
      └── endpoint group   search | detail | _default
           └── URI rules   /api/search*  →  search
```

- A **site** is one target system inside a namespace. Its name is unique in the namespace.
- A **client** is a flavour of access to that site: `web`, `mobile`, `partner`. A site declares its
  clients (`web` by default). Identity types, endpoint groups and identities all belong to a
  specific site *and* client, because a mobile identity is not usable as a web identity.
- An **endpoint group** is a set of endpoints of one site+client that behave alike. Every site+client
  always has a group named `_default`.

### Why endpoint groups exist

Two endpoints of the same site are usually *not* interchangeable from a risk point of view. A list
endpoint may allow 60 requests an hour per identity before it starts returning captchas, while a
detail endpoint allows thousands. One of them may start failing at 09:00 while the other is fine.
If cooldowns, quotas, health scores and circuit breakers were kept per site, one hostile endpoint
would take the whole site down with it.

So the endpoint group is the unit at which Spinneret keeps:

- the ready queue of identities and their per-endpoint cooldowns,
- the per-endpoint health score and failure streak of each identity,
- the request quota windows,
- the sliding window and state of the circuit breaker,
- the `low_watermark` that raises an alert when too few identities remain available.

### How a URI becomes an endpoint group

`Acquire` takes either an explicit `endpoint_group` or a `uri`. The explicit name always wins. When
neither is given, `_default` is used. A `uri` is normalized to a path and matched against the URI
rules of that site **and client** — there is one matcher per site and client, so `/api/search` can
map to different endpoint groups for `web` and for `mobile`:

| Rule kind | Pattern example | Matches |
| --- | --- | --- |
| `exact` | `/api/search` | that path only |
| `template` | `/api/item/{id}` | one path segment per `{…}` placeholder |
| `prefix` | `/api/search` | any path starting with it |
| `regex` | `^/api/(search\|suggest)` | Go regular expression over the path |

Priority is fixed and does not depend on the order rules were created:

1. `exact`
2. `template` — more literal segments first, then fewer placeholders, then `position`
3. `prefix` — longest pattern first
4. `regex` — by `position`
5. `_default`

`position` is the tie-breaker you control; lower comes first.

---

## Identities

An **identity** is one usable persona at a site: a cookie jar, a device fingerprint, a token, a set
of signing parameters, or any combination. It is the thing that gets burnt, cooled down and banned.

### Identity type

An identity type is the schema. It declares, per site and client:

| Part | Meaning |
| --- | --- |
| `fields` | the payload fields, each with a `type`, and the `required` and `sensitive` flags |
| `unique_by` | which field paths deduplicate identities on import (up to 16 paths) |
| `activation` | `probe` (imported as `pending`, activated by the first success) or `immediate` (imported as `active`) |
| `deliver` | how a payload is rendered into a credential |

Field types are `string`, `number`, `bool`, `cookie_map`, `json` and `secret_ref`. A `secret_ref`
field holds a namespace-relative vault path instead of a value; it is resolved at delivery time, so
an API key lives in the vault and not in thousands of identity payloads. A type may declare up to
128 fields.

```yaml
name: web_cookie
site: example-site
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
  signature:  { type: string, sensitive: true }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
  values:
    signature: "{{ signature }}"
```

### Payload, payload version, credential

The **payload** is the identity's data. It is encrypted at rest with envelope encryption (a
per-payload DEK wrapped by the KEK) and is never returned in a listing. Every write of a payload
creates a new **payload version**; `identities.payload_version` points at the current one, and older
versions stay until they are pruned, which is what makes a bad refresh recoverable.

The **credential** is what a node actually receives. It is the payload rendered through the type's
`deliver` templates into six fixed segments:

| Segment | Type | Typical use |
| --- | --- | --- |
| `cookies` | map | cookie jar as name → value |
| `cookie_header` | string | the same jar pre-rendered as `k1=v1; k2=v2` |
| `headers` | map | request headers |
| `query` | map | query parameters |
| `json` | any JSON value | a request body fragment |
| `values` | struct | anything typed the node needs (device parameters, signing keys) |

Unused segments come back as empty maps, empty strings or null. Delivery templates only support
field placeholders of the form `{{ path }}`, with dotted sub-keys for `cookie_map` and `json`
fields (`{{ cookies.sessionid }}`). There are no expressions, pipes or function calls: that rules
out template injection and keeps rendering cheap enough for the acquire hot path.

### Accounts

An **account** groups identities that share one real login at a site (`accounts.external_ref` is
unique per site). The point is blast radius: when a report says the *account* is banned, banning one
cookie jar is pointless — every identity of that account is worthless. An account-scoped ban
therefore pushes every member identity out of scheduling, and member identities changed for that
reason carry the state reason `account_ban` so that unbanning the account releases exactly those.

Accounts have their own states: `active`, `banned` and `disabled`, plus a cooldown.

---

## The identity state machine

An identity has one **lifecycle state** in PostgreSQL and, independently, an **availability
condition** in the hot state. Confusing the two is the most common misreading of the model.

Lifecycle states: `pending`, `active`, `expired`, `banned`, `quarantined`, `disabled`, `retired`.

```text
                    import (activation: immediate)
                ┌──────────────────────────────────────┐
                │                                      v
   import ──► pending ──── first success ──────────► active ◄── operator: enable
 (activation:    ▲              (lifecycle rule)       │       │   (from disabled)
   probe)        │                                     │       └── ban expires with
                 │                                     │           ban_expiry_state: active
                 │                                     ├── outcome auth_invalid
                 │                                     │   (action: expire)       ──► expired
                 │                                     │
                 │                                     ├── global score < quarantine_score
                 │                                     │   with >= quarantine_min_samples
                 │                                     │   global samples         ──► quarantined
                 │                                     │
                 │                                     ├── captcha x N in a window
                 │                                     │   (action: ban)          ──► banned
                 │                                     │
                 │                                     └── operator: disable      ──► disabled
                 │
                 ├── operator: unban, or a ban expiring with
                 │   ban_expiry_state: pending (the default) ──────────────  from banned
                 ├── operator: unquarantine, or the quarantine expiring ───  from quarantined
                 ├── operator: restore ────────────────────────────────────  from retired
                 └── operator: activate ──────────────────────────────────►  active (skips the probe)

   any state ── operator: archive ──► retired      (hidden from listings, never scheduled)
```

| State | Console label | Scheduled? | What moves it here | What moves it out |
| --- | --- | --- | --- | --- |
| `pending` | Pending | Yes, at reduced weight | import with `activation: probe`; `unban`; `unquarantine`; ban expiry with `ban_expiry_state: pending`; quarantine expiry; `restore` | first `success` report activates it; failures can still ban or expire it |
| `active` | Active | Yes | `activate`, `enable`, first success of a pending identity, `immediate` import, ban expiry with `ban_expiry_state: active` | any action rule, or an operator |
| `expired` | Expired | No | the `auth_invalid` outcome (built-in `auth-invalid-expire` rule), or `expire` | a payload refresh, or `activate` |
| `banned` | Banned | No | a `ban` action at scope `identity`, or an account ban | the ban's end time (expiry job → `ban_expiry_state`), or `unban` (→ `pending`). A ban with no end time is permanent |
| `quarantined` | Quarantined | No | a `quarantine` action (typically health-score driven) | the quarantine end, or `unquarantine` — both → `pending` |
| `disabled` | Disabled | No | operator `disable`, or the identity's account being disabled | `enable` |
| `retired` | Retired | No | operator `archive` | `restore` (→ `pending`) |

On top of the state, an `active` identity can still be unusable *right now* because of a hot-state
condition. These are not states and never appear in the state column:

- a **cooldown** until a timestamp, at endpoint-group scope or site scope;
- **already leased** up to `max_concurrent_leases`;
- a spent **quota** window;
- a **reuse interval** that has not elapsed since it was last used;
- its **account** being cooled down, banned or disabled;
- the **proxy** it is bound to being unavailable.

The [cooldown heatmap](./12-observability.md) exists precisely to make this second layer visible.

---

## Leases

A **lease** is a time-boxed claim on one identity for one endpoint group. It is what makes
concurrency safe: the scheduler knows how many nodes hold a given identity right now, and it can
reclaim a claim from a node that died.

```text
AcquireResponse
  lease.lease_id        "lse_<32 hex>_<site>_<shard>"   send it with every report
  lease.identity_id     the leased identity
  lease.identity_type   the type name
  lease.endpoint_group  the group it was issued for
  lease.expires_at      when it expires unless renewed
  lease.sticky          true when reused through session_key
  lease.probe           true for a breaker probe or a pending identity
  credential            the rendered credential
  proxy                 null when the rotation policy assigns none
  hints.renew_before_ms a quarter of the lease TTL
```

### TTL, lifetime and renew

Two different limits:

| Limit | Rotation policy field | Default | Range | Meaning |
| --- | --- | --- | --- | --- |
| Lease TTL | `rotation.lease_ttl` | `2m` | 5s – 30m | how long one lease lives without a renew |
| Lease lifetime | `rotation.max_lease_lifetime` | `30m` | up to 24h | the hard cap a lease can ever reach through renewals |

`Renew(lease_id, extend_ms)` extends an active lease from *now*; `extend_ms: 0` means the policy
TTL, and the maximum accepted value is `1800000`. A renew never shortens a lease, and a lease
already at its lifetime cap fails with `lease_lifetime_exceeded`. Renew when
`hints.renew_before_ms` remain, not on a fixed schedule.

### Release

`Release(lease_id)` ends a lease early and is idempotent — it returns `released: false` when the
lease had already ended. You usually do not need it: set `release: true` on the last report of the
lease and the worker releases it after processing, which saves a round trip.

### Concurrency, reuse and stickiness

| Rotation field | Default | Effect |
| --- | --- | --- |
| `max_concurrent_leases` | `1` | `1` means an exclusive lease: nobody else gets this identity until it ends |
| `reuse_interval` | `0s` | minimum gap between two uses of the same identity |
| `reuse_anchor` | `released` | measure the gap from when the previous lease was `acquired` or `released` |
| `reuse_scope` | `endpoint_group` | apply the gap within one endpoint group, or across the whole `site` |
| `quota` | `[]` | e.g. `[{limit: 60, window: 1h}]` — a sliding-window request budget per identity per endpoint group |
| `warmup` | `0s`, factor `1` | scale the quota of freshly activated identities for a while |
| `sticky.enabled` / `sticky.ttl` | `false` / `10m` | when enabled, `Acquire` calls carrying the same `session_key` reuse the same identity while it stays usable |

Sticky sessions only apply when a single lease is requested, and half-open breaker probes ignore
stickiness entirely: a probe must go to the healthiest identity, otherwise a sick sticky identity
would keep the endpoint's breaker open on its own.

An exclusive lease also affects the *other* endpoint groups of the same client — an identity held
exclusively for `search` is not available for `detail` — and releasing or expiring it restores its
availability in those groups.

### When a node dies holding a lease

Nothing needs to happen on the node side. Every instance runs a **lease reaper** once per second.
It takes a per-site lock, reads the overdue leases of that site in batches of 200, and ends them as
`expired`. A lease that ended without any report ever being ingested or processed is marked
**abandoned**. That is not a request-explorer row — the explorer only holds processed reports. It
surfaces as the per-node **Abandoned** and **Unreported** columns of the overview and as
`spinneret_lease_reaped_total{kind="abandoned"}`, which is how you see a fleet of nodes crash.

The reaper also rescores the identity and restores the availability it pushed away while it was
leased, so a dead node costs you at most one lease TTL of that identity's capacity.

---

## Reports

A node reports **facts**, never decisions. All classification, blame and state change is
server-side, which is what lets you change the rules without redeploying nodes.

| Field | Meaning |
| --- | --- |
| `report_id` | client-generated idempotency key, 1–64 characters of `[A-Za-z0-9_.:-]`; a UUID is recommended |
| `lease_id` | the lease the request was made with |
| `uri`, `method` | the request path (optionally with query) and HTTP method |
| `http_status` | 0–999; `0` means no response was received |
| `business_code` | business status code read out of the response body, if any |
| `error_kind` | `""`, `timeout`, `conn_reset`, `conn_refused`, `proxy_auth`, `tls`, `dns`, `other` |
| `markers` | up to 32 response features the node recognized, e.g. `captcha_page`, `login_redirect`, `empty_list` |
| `outcome_hint` | the node's own guess; used only when the signal policy sets `trust_outcome_hint` |
| `latency_ms`, `response_bytes` | request latency and response size |
| `started_at`, `finished_at` | both required; `finished_at` must not precede `started_at` |
| `release` | release the lease after this report is processed |

Up to 500 reports go in one call. Reports are validated individually, so one bad report does not
reject the batch: the response returns `accepted`, `duplicated` and a `rejected` list whose reasons
are `invalid_argument`, `lease_unknown` or `scope_missing`.

**Markers are the extension point.** Spinneret knows nothing about your target's HTML. When your
node recognizes "this is the interstitial that means we are being challenged", it attaches a marker
such as `captcha_page`, and a signal rule turns that marker into an outcome. Adding a new failure
mode is a marker in the node plus one rule in a policy — not a code change in the control plane.

### Idempotency and delivery

`report_id` is remembered for the dedup window (`SPINNERET_REPORT_DEDUP_TTL`, default `1h`). A
retry with the same `report_id` inside that window is counted in `duplicated` and has no further
effect, so a node can retry a failed `Report` call safely.

Ingestion only validates, authorizes and appends the report to a stream shard derived from the
lease ID (`SPINNERET_REPORT_SHARDS`, default `16`). A worker consumes the shard and does the real
work. Each shard carries a checkpoint of the last applied entry, so a redelivered stream entry is
detected and skipped rather than applied twice.

A report that arrives more than `SPINNERET_LATE_REPORT_WINDOW` (default `10m`) after its lease ended
is **late**. It is still stored and still counts in the breaker window and in the quota, but it
updates no health score, no sample count, no failure streak and no outcome counter, and it plans no
action. Stale outcomes must not cool down an identity that has been healthy for ten minutes.

---

## Outcomes, blame and actions

This is the pipeline. Four stages, each owned by a different policy kind.

```text
report ──► signal policy ──► outcome ──► action policy ──► action ──► state change
           (classify)        (+ blame)   (rules + counts)  (+ scope)
```

### 1. Classification: the signal policy

An ordered list of rules; **the first matching rule wins**. A rule matches on any combination of
`http_status`, `business_code`, `error_kind`, `markers`, `uri` (prefix or regex), `method`,
`latency_ms` and `response_bytes`; a rule with no conditions matches everything. It produces an
`outcome` and may override the default `blame`.

The twelve outcomes:

| Outcome | Console label | Risk? | Always a failure? | Default blame |
| --- | --- | --- | --- | --- |
| `success` | Success | | | none |
| `empty` | Empty or truncated data | | yes | identity |
| `rate_limited` | Rate limited | yes | yes | both |
| `captcha` | Captcha | yes | yes | identity |
| `auth_invalid` | Login state invalid | | yes | identity |
| `forbidden` | Forbidden | yes | yes | identity |
| `banned` | Banned | yes | yes | identity |
| `proxy_error` | Proxy error | | | proxy |
| `network_error` | Network error | | only when blamed on the identity | proxy |
| `target_error` | Target site error | | | none |
| `client_error` | Client error | | | none |
| `unknown` | Unknown | | | none |

"Risk" outcomes are what circuit breakers count. "Failure" outcomes are what increments an
identity's failure streak.

**Blame** decides who pays: the identity, the proxy, both, or neither. It is what stops a bad proxy
from destroying a hundred good identities. Cross attribution refines it automatically: within a
10-minute window (default), if a risk outcome hits a proxy across at least 3 distinct identities
while that identity has seen fewer than 3 distinct proxies, the blame moves to the proxy; if one
identity fails across at least 3 distinct proxies, the identity is blamed as well.

The built-in default signal policy classifies, in order: proxy auth / connection refused →
`proxy_error`; timeout / reset / TLS / DNS → `network_error`; marker `captcha_page` → `captcha`;
marker `login_redirect` → `auth_invalid`; 429 → `rate_limited`; ≥500 → `target_error`;
200 with marker `empty_list` → `empty`; 400 → `client_error`; 401 → `auth_invalid`;
403 → `forbidden`; 2xx → `success`.

### 2. Disposition: the action policy

Every rule whose `when` matches is evaluated, and the most severe action per subject wins — at most
one action each for the identity, the account and the proxy. A rule's `when` takes a list of
outcomes and, optionally, a `count` condition (`gte` events `within` a sliding window) evaluated
against the counter of the subject named by the rule's scope.

The four action kinds and the scopes each may use:

| Action | Allowed scopes | What it does |
| --- | --- | --- |
| `cooldown` | `identity_endpoint`, `identity_site`, `identity`, `account`, `proxy_site`, `proxy` | make the subject unavailable until a timestamp; the state does not change |
| `quarantine` | `identity`, `proxy` | set aside for review; ends after `duration` (identity → `pending`) |
| `ban` | `identity`, `account`, `proxy` | remove from scheduling until the ban ends; `duration: permanent` never ends |
| `expire` | `identity` | mark the credential stale so a refresh job replaces it |

`activate` also exists but is planned only by the lifecycle (a `pending` identity reporting
`success`); it cannot be written in a rule.

| Scope | Console label | Blast radius |
| --- | --- | --- |
| `identity_endpoint` | Identity × endpoint group | one identity, one endpoint group |
| `identity_site` | Identity × site | one identity, every endpoint group of the site |
| `identity` | Identity | one identity, everywhere |
| `account` | Account | every identity of the account |
| `proxy_site` | Proxy × site | one proxy, one site |
| `proxy` | Proxy (all sites) | one proxy, everywhere |

A cooldown lasts `min(base × multiplier^min(streak − 1, max_exponent), max) × jitter`, where
`streak` is the subject's current failure streak and `jitter` is drawn from `[0.8, 1.2]`. The
exponent is `streak − 1`, so the first cooldown of a streak is `base` itself. The streak resets
after `failure_reset_after` (default `1h`) without a failure, and a success halves it.

Cooldowns are **constant** by default, because `multiplier` defaults to `1`. Set `multiplier > 1` to
make them exponential, bounded by `max_exponent` (default `10`) and `max` (default `24h`). The
built-in `default-action` policy opts in with `multiplier: 2` on its `rate_limited` and `captcha`
rules; the rest of its cooldown rules stay constant.

Temporary bans climb an **escalation** ladder based on how often the subject was banned recently.
The built-in default: 2 bans within 7 days → 72h, 3 bans within 30 days → permanent.

When a ban ends, the identity returns to the state named by `ban_expiry_state` — `pending` by
default, so it has to earn its way back with a successful probe, or `active` if you trust it. The
expiry job reads that field from the action policy resolved for the identity's site + client
`_default` endpoint group, not from the policy that issued the ban, so setting it on a
per-endpoint-group binding has no effect on ban expiry.

An action policy in `mode: shadow` plans everything and applies nothing: the state events are
recorded with `shadow: true` so you can see exactly what the policy *would* have done. Use it
before publishing a rule that bans things.

---

## Health score

Every identity carries two exponentially weighted scores in the hot state: one per endpoint group
and one global (per identity across the site). A score is a number in 0–100 with a sample count.

Each processed report updates the relevant score in two steps:

```text
decay:  score = baseline + (score - baseline) * exp(-(now - last_update) / tau)
update: score = alpha * observation + (1 - alpha) * score
```

Defaults: `alpha: 0.1`, `baseline: 70`, `tau: 6h`. Decay is what makes an old disaster stop mattering:
an untouched identity drifts back to the baseline rather than staying condemned forever.

The observation is chosen by the outcome. Identity observations (overridable per policy under
`health.observations`):

| Outcome | Observation | Applied when |
| --- | --- | --- |
| `success` | 100 | always |
| `empty` | 60 | the blame includes the identity |
| `network_error` | 50 | the blame includes the identity |
| `rate_limited` | 30 | the blame includes the identity |
| `forbidden` | 10 | the blame includes the identity |
| `captcha` | 0 | the blame includes the identity |

Proxies have their own, fixed table: `success` 100 (always), `proxy_error` 0, `network_error` 40 and
`rate_limited` 30, each applied only when the blame includes the proxy.

The score feeds rotation in two ways.

1. **Selection.** The `weighted_random` strategy (the default) weights each candidate by the square
   of its score, clamped to `[5, 100]`, so a score of 100 is four times as likely to be picked as a
   score of 50 and 400 times as likely as a score of 5. A dying identity fades out of traffic
   instead of being cut off. `best_health` picks the highest score outright and is what half-open
   breaker probes use; `least_recently_used` and `round_robin` ignore the score.
2. **Score-driven actions.** Two independent checks, and both run only when the report affected the
   identity — the blame includes it, or the outcome is a failure. The first reads the identity ×
   endpoint-group score: below `health.endpoint_low_score` (default `15`) with at least
   `endpoint_low_min_samples` (default `10`) samples **of that endpoint group**, the identity is
   cooled down on that endpoint group for `endpoint_low_cooldown` (default `6h`) — skipped when a
   longer cooldown is already running there. The second reads the identity's **global** score, not
   the endpoint group's: below `health.quarantine_score` (default `20`) with at least
   `quarantine_min_samples` (default `10`) **global** samples, the identity is quarantined for
   `quarantine_duration` (default `24h`).

The minimum sample counts are what keep two unlucky requests from quarantining a good identity.

---

## Circuit breakers

A cooldown protects the *identity*. A circuit breaker protects the *target* — and, through it, your
whole identity pool. When an endpoint group starts handing out captchas to everything, continuing to
send requests does not get you data; it burns every identity you own, one at a time. The breaker
stops the traffic instead.

A breaker belongs to one endpoint group and counts a sliding window of reports (default `60s` split
into `12` buckets of 5s).

```text
          trip condition met, >= min_requests in the window
 closed ─────────────────────────────────────────────────────► open
   ▲                                                            │
   │                                                            │ open_duration elapsed
   │  >= close_min_samples probes                               │ (next Acquire moves it)
   │  and probe success ratio >= close_success_ratio_gte        v
   └──────────────────────────────────  half_open  ◄────────────┘
                                            │
                                            │ >= close_min_samples probes
                                            │ and probe success ratio < close_success_ratio_gte
                                            └────────────────────► open (longer, up to max_open_duration)
```

| State | Console label | What `Acquire` does |
| --- | --- | --- |
| `closed` | Closed | normal service |
| `open` | Open | fails immediately with `circuit_open` and a `Spinneret-Retry-After-Ms` hint |
| `half_open` | Half-open | issues at most `probe_leases_per_10s` (default `5`) probe leases per 10-second window; everything else gets `circuit_open` |

Trip conditions — any one of them opens the breaker, and setting one to `0` disables it. None of
them apply until the window holds at least `min_requests` (default `50`) reports, and they are only
evaluated while the breaker is `closed`: a `half_open` breaker is decided by its probes alone.

| Condition | Default | Meaning |
| --- | --- | --- |
| `trip.risk_ratio_gte` | `0.4` | risk outcomes / total reports in the window |
| `trip.distinct_captcha_identities_gte` | `10` | how many distinct identities saw a captcha |
| `trip.success_ratio_lte` | `0.2` | success ratio below which the breaker opens |

An open breaker stays open for `open_duration` (default `2m`). The first `Acquire` after that moves
it to `half_open` and takes a probe. Repeated opens without a `reset_open_count_after` (default
`30m`) quiet period lengthen the open period, up to `max_open_duration` (default `1h`). A
**probe lease** is marked `probe: true` in the response; its report decides the transition, and it
is exempt from the suppression that otherwise stops actions while the breaker is open.

Because the breaker's verdict is "this was not the identities' fault", opening one also **reverts
the recent cooldowns** it caused: `revert_recent_cooldowns: endpoint` (the default) gives back the
identity × endpoint-group cooldowns of the group that opened, `all` additionally gives back those
identities' site-level cooldowns, `none` nothing. Only cooldowns are reverted — bans, quarantines
and expiry are kept.

Reports processed while a non-probe lease's breaker is effectively open are **suppressed**, and a
suppressed report is gated exactly like a late one: it is stored and counted in the breaker window
and the quota, but it updates no health score, no sample count, no failure streak and no outcome
counter, and it plans no action. The request explorer says as much on the row — "Health updates and
actions were suppressed because the breaker was open". Otherwise, the requests you sent before the
breaker tripped would keep punishing identities for a failure that was never theirs.

An evaluation sweep runs every 5 seconds on every instance and evaluates the groups with recent
activity plus every group whose breaker is not closed, so a breaker also closes without traffic
reaching it.

A **paused site** is the manual version of the same idea: `Acquire` fails with `site_paused` and a
30-second retry hint for every endpoint group of the site.

---

## Proxies

The **proxy pool** belongs to the namespace, not to a site, so one pool serves every site the team
runs. Each proxy carries a scheme (`http`, `https`, `socks5`), host, port, a kind
(`datacenter`, `residential`, `mobile`, `tunnel`), a region and city, a provider, tags, a
`max_concurrency` and an optional session template. The full URL, credentials included, is encrypted
at rest; only a credential-free display URL and a username hint are stored in clear.

The rotation policy decides how a proxy is chosen:

| `proxy.mode` | Behaviour |
| --- | --- |
| `none` | no proxy is assigned; the response's `proxy` field is null |
| `pool` | one proxy is picked from the pool for each lease, filtered by `kinds`, `providers`, `regions` and `tags` |
| `bind_identity` | each identity keeps one proxy across leases (stored in `proxy_bindings`), rebinding only when the bound proxy is unusable, bounded by `rebind_tolerance` (default `5m`) and `max_rebinds_per_day` (default `3`) |
| `region_match` | like `pool`, but the proxy's region must equal the identity's region. The `region_match: true` flag applies the same restriction to `pool` and `bind_identity` |

Pool selection decays each proxy's health score first and then weights the proxy by
`max(score, 5)²` — only the lower bound is clamped, unlike identity selection, which clamps to
`[5, 100]` — among proxies that are `active`, not cooling down, and below their `max_concurrency`. When no proxy can serve the lease, `Acquire` fails with `no_proxy_available`
rather than handing out a naked identity.

**A proxy shares the blame with the identity.** Both are named on the lease, so when a report comes
back, blame decides which of the two pays:

- `blame: proxy` → the proxy's health score drops and proxy-scoped rules can cool it down or ban it;
  the identity is untouched.
- `blame: identity` → the identity pays; the proxy is untouched.
- `blame: both` (the default for `rate_limited`) → both pay.
- Cross attribution can move the blame automatically, as described above.

Proxies have the same cooldown granularity as identities: `proxy_site` cools a proxy for one site
only (a proxy blocked by one target is usually still fine for another), while `proxy` cools or bans
it everywhere. A background health checker probes proxies on a schedule and maintains the same
kind of EWMA score, and repeated check failures mark a proxy `dead`.

---

## Configuration and secrets

A node fetches its configuration and its secrets from Spinneret at run time. The reason is the same
reason it fetches identities at run time: a value that is baked into an image can only be changed by
a deployment, and a deployment is minutes you do not have when a target changes behaviour.

**Config items** live in config groups inside a namespace and are `json`, `yaml` or `text`. Only the
published version is served to nodes. Every publish and rollback increases the version number, which
never repeats for a namespace/group/key. Nodes read one item with `GetConfig`, several with
`BatchGetConfig`, and subscribe with `WatchConfig`: a long poll that returns immediately with the
items whose version differs from the one the node holds, or waits up to `timeout_ms`
(default 30000, max 60000) and returns an empty list. A node that holds version 0 of an item gets it
as soon as it has a published version. That is how a config change reaches a fleet in seconds
without any node polling in a loop.

The reserved, read-only group `_runtime` exposes two documents Spinneret maintains itself:
`breakers` (the current breaker state of every endpoint group) and `site_switches` (which sites are
paused). A node can watch them to avoid sending requests it already knows will be refused.

**Secrets** live in a vault path inside the namespace and are protected by envelope encryption: each
secret version is sealed with its own DEK, and the DEK is wrapped by the KEK. A node reads one with
`GetSecret(path, version)` — version `0` means the current one — and needs a
`secret:read:<glob>` scope over `"<namespace>/<path>"`. Every read is written to the audit log.

The two systems compose: a config item may contain `${secret:...}` references, which are resolved
server-side before the item is served, and a config item whose content contained references comes
back with `has_secret_refs: true`. Nodes must not persist such content in plain text. Identity
payloads compose the same way through `secret_ref` fields.

---

## Policies and the binding hierarchy

Four policy kinds, each answering one question:

| Kind | Console label | Question it answers |
| --- | --- | --- |
| `rotation` | Rotation | which identity and which proxy does this request get? |
| `signal` | Signal | what did this report actually mean? |
| `action` | Action | what does that cost the identity, the account or the proxy? |
| `breaker` | Breaker | when do we stop sending requests to this endpoint group? |

Policies are written as YAML, versioned, published and rollback-able. Signal and action policies can
`extends` a parent policy (chains of at most 5, cycles rejected), so a team can keep one house
policy and override a handful of rules per site.

### Resolution

Each kind is resolved independently for the endpoint group of the request. The most specific binding
wins:

```text
endpoint group  >  client  >  site  >  namespace  >  built-in default
```

- an endpoint-group binding must name the endpoint group, and its site and client, when set, must
  match too;
- a client binding names a site and a client, no endpoint group;
- a site binding names a site only;
- a namespace binding names no site, client or endpoint group;
- a binding with a client or endpoint group but no site never matches anything;
- when several bindings share the winning level, the first one wins;
- when nothing matches, the built-in `default-rotation`, `default-signal`, `default-action` or
  `default-breaker` applies.

### Worked example

Bindings in namespace `prod`:

| # | Kind | Site | Client | Endpoint group | Policy |
| --- | --- | --- | --- | --- | --- |
| 1 | `action` | — | — | — | `house-action` |
| 2 | `action` | `example-site` | — | — | `site-action` |
| 3 | `action` | `example-site` | `web` | `search` | `search-action` |
| 4 | `rotation` | `example-site` | `web` | — | `web-rotation` |

A request `Acquire(site: example-site, client: web, uri: /api/search?q=x)` matching the `search`
endpoint group resolves:

- **action** → `search-action` at level `endpoint_group` (row 3 beats rows 2 and 1);
- **rotation** → `web-rotation` at level `client` (row 4);
- **signal** → no binding at all → built-in `default-signal` at level `builtin`;
- **breaker** → no binding → built-in `default-breaker` at level `builtin`.

The same call with `uri: /api/item/123`, matching the `detail` group, resolves the action policy to
`site-action` instead, because row 3 only binds `search`. The console shows the resolved policy and
its level on the endpoint group, so you never have to work this out by hand.

See [Policies](./08-policies.md) for the YAML of each kind, drafts, version compare and the rule
debugger.

---

## One request, end to end

This is the whole system in one trace. A node crawls `/api/search?q=shoes` on `example-site`,
client `web`.

1. **The node calls `Acquire`** with `site: example-site`, `client: web`, `uri: /api/search?q=shoes`,
   `wait_ms: 500`, carrying an API token. The API authenticates the token, resolves its namespace and
   checks `lease:acquire` on the site.
   *Changes:* nothing yet.

2. **The scheduler resolves the target.** The site is looked up in the namespace catalog and checked
   for `paused` (a paused site fails now with `site_paused` and a 30-second retry hint). The URI is
   normalized to `/api/search` and matched against the URI rules of that site and client
   (`example-site` + `web`), which select the `search` endpoint group. The rotation policy for that group is resolved through the binding hierarchy.
   *Changes:* nothing yet.

3. **The breaker gate.** The scheduler reads the `search` breaker. Closed → continue. Open and not
   yet due → return `circuit_open` with the remaining milliseconds. Open and due → move it to
   `half_open` and take this call as a probe. Half-open with the probe budget already spent → return
   `circuit_open`.
   *Changes:* possibly `closed`/`open` → `half_open`, and the probe counter for this 10-second window.

4. **Identity selection.** In one atomic Lua script, the scheduler samples up to `candidate_sample`
   (default 32) identities from the endpoint group's ready queue, whose score is the time at which
   each becomes available. A candidate is dropped or pushed forward if it is not `active`/`pending`,
   still cooling down, at `max_concurrent_leases`, inside its reuse interval, out of quota, bound to
   an unusable proxy, or owned by a banned, disabled or cooled-down account. The configured strategy
   picks among the survivors — `weighted_random` by score², `best_health` for probes. If a
   `session_key` was sent and stickiness is on, the previous identity of that session is tried first.
   *Changes:* filtered candidates are re-scored in the ready queue so the next call skips them cheaply.

5. **Proxy assignment.** Per `proxy.mode`, the script assigns no proxy, picks one from the pool
   (filtered by kind, provider, region and tags, weighted by score²), reuses the identity's bound
   proxy, or binds a new one. If nothing can serve the lease, the call either retries within
   `wait_ms` (50/100/150/200 ms backoff) or fails with `no_proxy_available`.
   *Changes:* the proxy's active-lease counter; in `bind_identity` mode possibly a new or rebound
   `proxy_bindings` row and its daily rebind counter.

6. **The lease is written.** A lease hash is created with the identity, proxy, endpoint group and
   expiry (`now + lease_ttl`), the lease ID is added to the site's expiry set, the identity's
   availability is pushed past the lease (and past the reuse interval), and the quota window
   advances. If a candidate could not be found within `wait_ms`, the call fails with
   `no_identity_available`.
   *Changes:* a new lease; the identity's last-used time and availability; quota counters.

7. **The credential is rendered and returned.** The identity payload is decrypted, any `secret_ref`
   fields are resolved from the vault, and the type's `deliver` templates produce the credential.
   The node receives `lease`, `credential`, `proxy` and `hints`.
   *Changes:* a `lease_events` row, the acquire statistics and the metrics. No request-explorer row
   yet — that one appears at step 14, when the report has been processed.

8. **The node sends the request** to the target through the given proxy with the given credential.
   Spinneret is not involved. If the work outlasts the TTL, the node calls `Renew` when
   `hints.renew_before_ms` remain.

9. **The node calls `Report`** with a fresh `report_id`, the `lease_id`, the URI and method, the HTTP
   status, any markers it recognized, the latency, the byte count, the start and finish timestamps,
   and `release: true`. Ingestion validates the report, checks `report:write` for the lease's site,
   drops it as a duplicate if that `report_id` was seen within the dedup window, and appends it to
   the report stream shard encoded in the lease ID. The call returns immediately.
   *Changes:* a stream entry; the dedup key.

10. **A worker picks up the entry.** It resolves the site, the lease and the endpoint group, then
    classifies the report with the resolved signal policy: the first matching rule gives the outcome
    and the blame. Say the node attached `captcha_page` — outcome `captcha`, blame `identity`.
    *Changes:* nothing yet.

11. **The hot state is updated atomically.** The shard checkpoint advances (so a redelivered entry is
    skipped). The breaker's sliding-window bucket counts the request as total and as risk, adds the
    identity to the window's distinct-captcha set, and the quota window advances. Then the report is
    classified: **suppressed** if the breaker was effectively open and this was not a probe, **late**
    if it arrived long after its lease ended, ordinary otherwise. Only an ordinary report goes on to
    the scoring — the identity's endpoint and global health scores are decayed and updated with
    observation 0, their sample counts increase, the failure streak increments, the proxy's score is
    updated if the blame includes it, and the outcome counters and cross-attribution sets advance.
    Either mark stops the report here, and stops step 12 with it.
    *Changes:* the breaker window and the quota always; health scores, streaks and counters only for
    an ordinary report.

12. **The action policy is evaluated** with the outcome, the blame, the identity's state and the
    counters just read. The built-in default plans a `cooldown` at scope `identity_site` with base
    `30m` and multiplier 2 for `captcha`, and, if this is the third captcha within 24 hours, a `ban`
    at scope `identity` for 12 hours. The most severe action per subject wins, so the ban replaces
    the cooldown for this identity. If the policy's ban escalation ladder matches, the 12 hours
    become 72 hours or permanent.
    *Changes:* nothing yet — this stage only plans.

13. **The executor applies the plan.** In `enforce` mode the identity is banned in the hot state
    immediately, the row in PostgreSQL follows asynchronously, and a state event is recorded
    (`from_state: active`, `to_state: banned`, `action: ban`, with the policy, version, rule,
    `report_id` and `lease_id` that caused it). In `shadow` mode nothing is applied and the event is
    recorded with `shadow: true`. The state event appears on the risk events page, the identity
    detail timeline and the notification pipeline.
    *Changes:* the identity's state and availability; a state event; alerts.

14. **The lease is released** because the report asked for it, and the breaker is notified that this
    endpoint group saw a risk outcome. The request is recorded for the request explorer and the
    per-site statistics.
    *Changes:* the lease ends; the identity's active-lease count and the proxy's drop by one.

15. **Within 5 seconds, the breaker sweep evaluates the `search` group.** If the window now holds at
    least 50 reports and the risk ratio has crossed 0.4, or 10 distinct identities have seen a
    captcha, the breaker opens for 2 minutes, reverts the cooldowns it caused on that endpoint group,
    and publishes a transition event. The ban from step 13 survives: only cooldowns are reverted.
    Every subsequent `Acquire` for `search` fails with
    `circuit_open` until the open period ends — and the node's next `Acquire` after that becomes the
    probe that decides whether the endpoint has recovered.

At no point did anyone deploy anything.

---

## Next

- [Quick start](./01-quickstart.md) — run this end to end on one host in about thirty minutes.
- [Identities and accounts](./06-identities.md) — identity types, importing, the state machine in
  operational detail, and the manual operations.
- [Policies](./08-policies.md) — the YAML of all four kinds, publishing, shadow mode and the rule
  debugger.
- [Node API reference](./13-node-api.md) — the exact request and response shapes of every call named
  on this page.
- [Observability and alerting](./12-observability.md) — where each of these state changes shows up.
- [FAQ and glossary](./21-faq.md) — every term on this page in both languages.
