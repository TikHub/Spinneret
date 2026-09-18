# Proxies

**The outbound proxy pool: what a proxy record holds, how proxies get into Spinneret, how the scheduler picks one for a lease, how their health is measured, and what to do when a node gets `no_proxy_available`.**

[中文](../zh/07-proxies.md)

---

## Contents

- [The pool](#the-pool)
- [What a proxy record holds](#what-a-proxy-record-holds)
- [Proxy states](#proxy-states)
- [Importing proxies](#importing-proxies)
- [Assignment modes](#assignment-modes)
- [How the scheduler picks a proxy](#how-the-scheduler-picks-a-proxy)
- [Session templates and rotating gateways](#session-templates-and-rotating-gateways)
- [Identity–proxy binding](#identityproxy-binding)
- [Health checking](#health-checking)
- [Health scores, cooldowns and cross attribution](#health-scores-cooldowns-and-cross-attribution)
- [Manual operations](#manual-operations)
- [Editing and deleting proxies](#editing-and-deleting-proxies)
- [Provider statistics](#provider-statistics)
- [Permissions and API](#permissions-and-api)
- [Operating the pool](#operating-the-pool)
- [Next](#next)

---

## The pool

A proxy pool belongs to a **namespace**. Every proxy in the namespace is materialised into the hot
state of **every site** of that namespace, so one pool serves all sites; per-site differences
(health score, active leases, cooldown) live in the hot state, not in separate records.

Spinneret never routes traffic itself. It hands a node a proxy URL together with the lease, and the
node makes the request through it:

```text
Acquire(site, client, uri)  ->  identity + credential + proxy { proxy_id, url, kind, region } + lease
Report(lease_id, outcome, …) ->  the proxy's health, cooldowns and state are updated
```

Whether a lease carries a proxy at all, and which proxy, is decided by the **rotation policy** bound
to the endpoint group — see [Assignment modes](#assignment-modes) and
[Policies](./08-policies.md).

The console page is **Scheduling → Proxies** (`/proxies`). It has two tabs, *Proxies* and
*Providers*.

### Where the state lives

| Layer | Holds | Notes |
| --- | --- | --- |
| PostgreSQL, table `proxies` | The record: encrypted URL, attributes, lifecycle state, check results | Source of truth |
| PostgreSQL, table `proxy_bindings` | Identity → proxy bindings (`bind_identity` mode) | Written asynchronously; Redis stays authoritative for routing |
| Redis/Valkey, hash `px:<proxy hkey>` per site | Live per-site state: score, samples, active leases, cooldowns, failure streak | Rebuildable from PostgreSQL |
| Redis/Valkey, sorted set `pxrdy` per site | The ready queue: active proxies scored by "available at" | Only `active` proxies are members |

The full proxy URL **including credentials** is sealed with the vault cipher (AAD `<proxy id>` +
`\x00` + `url`, for example `pxy_…\x00url`). Only `display_url` (no credentials) and `username_hint` (the first four
characters of the user name followed by `***`) are stored in clear text, and the API never returns
the credentials. See [Secret vault](./10-secrets.md).

---

## What a proxy record holds

| Field | Type | Meaning |
| --- | --- | --- |
| `id` | `pxy_…` | Proxy ID |
| `display_url` | text | `scheme://host:port`, never credentials |
| `scheme` | `http` \| `https` \| `socks5` | Transport to the proxy |
| `host`, `port` | text, 1–65535 | Normalised host (lower-case DNS name or canonical IP) and port |
| `username_hint` | text | First four characters of the user name plus `***`; empty without credentials |
| `kind` | `datacenter` \| `residential` \| `mobile` \| `tunnel` | Used by the rotation policy filter `proxy.kinds` |
| `region` | ≤ 64 bytes | Exit region, typically an ISO country code; used by region matching and the filter `proxy.regions` |
| `city` | ≤ 128 bytes | Exit city, informational |
| `provider` | ≤ 128 bytes | Free-form provider name; groups the *Providers* tab and the filter `proxy.providers` |
| `max_concurrency` | 1…100000 | Maximum simultaneous leases through this proxy |
| `tags` | ≤ 64 tags, each ≤ 64 bytes | Free-form labels; the rotation filter `proxy.tags` requires **all** listed tags |
| `session_template` | ≤ 512 bytes | User-name template for rotating gateways (see below) |
| `state` | see [Proxy states](#proxy-states) | Lifecycle state |
| `state_reason`, `state_changed_at` | text, timestamp | Why and when the state last changed |
| `ban_until` | timestamp | End of a ban **or** of a quarantine; `NULL` while banned means permanent |
| `cooldown_until` | timestamp | End of the global cooldown |
| `url_version` | int | Incremented whenever the URL is replaced |
| `last_check_at`, `last_check_ok`, `last_latency_ms`, `exit_ip`, `consecutive_check_failures`, `next_check_at` | — | Health-check results |
| `bound_identities` | int | Number of identities bound to this proxy (read-only, computed) |

Tags must not contain commas, whitespace or control characters — they are stored in the hot state as
`,tag1,tag2,` and matched by substring.

**There is no weight field.** The selection weight of a proxy is derived from its live health score
(see [How the scheduler picks a proxy](#how-the-scheduler-picks-a-proxy)); `max_concurrency` is the
only capacity knob you set by hand.

### Per-site state

`GetProxy` and the console detail sheet also return, for every site the caller can read:

| Field | Meaning |
| --- | --- |
| `state` | State of the proxy as the site's scheduler sees it |
| `score` | Proxy × site health score, 0…100, decayed to the moment of reading |
| `samples` | Number of observations behind the score |
| `active_leases` | Leases currently held through this proxy on that site |
| `cooldown_until` | End of the **site-scoped** cooldown |

---

## Proxy states

| State | In the pool? | How you get in | How you get out |
| --- | --- | --- | --- |
| `active` | yes | Default on import; `enable`, `unban`, `unquarantine`, `restore`; a successful check on a dead proxy | any operation below |
| `disabled` | no | `disable` | `enable` |
| `dead` | no | 3 consecutive failed health checks | a successful health check, or `enable` |
| `banned` | no | `ban`, or an action policy rule with `action: ban, scope: proxy` | `unban`, or the ban expires |
| `quarantined` | no | `quarantine`, or an action policy rule with `action: quarantine, scope: proxy` | `unquarantine`, or the quarantine expires |
| `retired` | no | `archive` | `restore` |

Listing proxies without a state filter hides `retired` proxies; every other state is shown.

Cooldowns are **not** states. A cooling proxy stays `active` and stays in the ready queue, but its
availability time is in the future, so the scheduler skips it until then. There are two cooldowns:
a site-scoped one (`cd`) and a global one (`gcd`, mirrored from `proxies.cooldown_until`); the proxy
is available at `max(cd, gcd)`.

`proxies` has no `quarantine_until` column: `ban_until` carries the end of a ban **or** of a
quarantine, and the expiry job releases both back to `active`.

---

## Importing proxies

`ImportProxies` (permission `proxy:write`, console *Proxies → Import*) is the only way to create
proxies. It accepts three formats and deduplicates by URL.

| Limit | Value |
| --- | --- |
| Data size | 32 MiB |
| Data rows | 100 000 |
| Reported per-row failures | 1000, then one summary row with line `0`: `N more rows failed` |

### Format `lines`

One URL per line, optionally followed by whitespace-separated `key=value` pairs. Blank lines and
lines starting with `#` are ignored. Accepted keys: `kind`, `region`, `city`, `provider`, `tags`,
`max_concurrency`, `session_template`. `tags` is split on `,` or `;`. Because fields are split on
whitespace, a value cannot contain spaces. Putting `url=` in a pair is rejected: the URL must be the
first field.

```text
# datacenter pool, one region
http://user:pass@203.0.113.10:8000 kind=datacenter region=US provider=provider-a tags=dc,primary
http://user:pass@203.0.113.11:8000 kind=datacenter region=US provider=provider-a tags=dc,primary
socks5://user:pass@203.0.113.12:1080 kind=residential region=DE provider=provider-b max_concurrency=2
```

### Format `jsonl`

One JSON object per line with `url`, `kind`, `region`, `city`, `provider`, `tags` (array),
`max_concurrency` and `session_template`. Unknown fields are rejected; `url` is required.

```json
{"url": "http://user:pass@203.0.113.10:8000", "kind": "datacenter", "region": "US", "tags": ["dc"]}
{"url": "socks5://user:pass@203.0.113.12:1080", "kind": "residential", "region": "DE", "max_concurrency": 2}
```

### Format `csv`

A header row naming the columns, then one proxy per row. Allowed columns: `url` (required),
`kind`, `region`, `city`, `provider`, `tags`, `max_concurrency`, `session_template`. An unknown or
duplicated column rejects the whole import. Empty cells mean "not set". Separate tags with `;` — a
comma would split the CSV field. Record numbers are line numbers, with the header counting as
line 1.

```text
url,kind,region,provider,tags,max_concurrency
http://user:pass@203.0.113.10:8000,datacenter,US,provider-a,dc;primary,4
http://user:pass@203.0.113.13:8000,mobile,DE,provider-b,mobile,1
```

### URL rules

`scheme://[user[:password]@]host:port` with scheme `http`, `https` or `socks5`, at most 2048 bytes,
and an **explicit port**. No path, query or fragment. IPv6 hosts go in brackets. Credentials are
percent-decoded and limited to 255 bytes each. Parse errors never echo the input, so a bad
credential never reaches a log or an error message.

### Defaults, deduplication and merging

`defaults` supplies values for rows that do not set them: `kind` (empty means `datacenter`),
`region`, `city`, `provider`, `tags` (added to every row, unioned with the row's own tags),
`max_concurrency` (0 means 1) and `session_template`.

Proxies are deduplicated per namespace by `HMAC-SHA256(pepper, normalised URL)`. A row whose URL
already exists **updates** the existing proxy: only the attributes the row or a default explicitly
sets are applied; everything else keeps its value. If nothing changes, the row counts as
*unchanged*. A duplicate within the same import is rejected with `duplicate of line N`.

The response counts `created`, `updated`, `unchanged` and lists `failed` rows by line number with a
message that never contains credentials.

**Always dry-run first.** With `dry_run: true` nothing is stored and the response reports what
*would* be created and updated. The console requires a dry run with the current inputs before it
enables the Import button.

Import publishes no state events, so the resolver keeps a decrypted URL, region, kind and session
template for up to 30 seconds. The attributes the scheduler filters on — `kind`, `region`,
`provider`, `tags` and `max_concurrency` — are written to the hot state by the import itself and
apply to the next acquire.

---

## Assignment modes

The `proxy` section of the **rotation policy** bound to an endpoint group decides everything about
proxy assignment:

```yaml
proxy:
  mode: pool                 # none | pool | bind_identity | region_match
  kinds: [datacenter]        # any of datacenter | residential | mobile | tunnel
  tags: [primary]            # the proxy must carry ALL of these tags
  providers: [provider-a]    # any of
  regions: [US, DE]          # any of
  region_match: false        # also require proxy.region == identity.region
  rebind_tolerance: 5m       # bind_identity only
  max_rebinds_per_day: 3     # bind_identity only
```

| Mode | What it does | Use it when |
| --- | --- | --- |
| `none` (default) | No proxy is assigned; `AcquireResponse.proxy` is absent | The node has its own egress, or the target needs none |
| `pool` | A proxy is chosen from the site's ready queue for every lease | Datacenter or shared pools where any exit is as good as another |
| `bind_identity` | The identity keeps one proxy across leases; a new proxy is chosen only when the bound one is unusable | Accounts whose sessions are tied to an exit IP |
| `region_match` | Like `pool`, but the proxy's `region` must equal the **identity's** region | Geo-pinned accounts, where the exit country must follow the account |

`region_match: true` as a flag adds the same region constraint to `pool` and `bind_identity`, so
`mode: region_match` is equivalent to `mode: pool` plus `region_match: true`.

**Note.** Region matching is exact string equality. An identity with an empty region therefore only
matches proxies whose `region` is also empty. Set the region on identities and proxies together, or
leave region matching off.

`kinds`, `providers` and `regions` are OR-sets: a proxy passes when it matches any listed value. An
empty list means "no constraint". `tags` is an AND-set: the proxy must carry every listed tag.

---

## How the scheduler picks a proxy

Proxy selection happens inside the atomic `Acquire` script, after the identity has been chosen.

Each site keeps a sorted set `pxrdy` whose members are the `active` proxies of the namespace, scored
by the millisecond at which the proxy becomes available. Selection works on the **due range** of
that set, `score <= now`.

1. `ZCOUNT pxrdy -inf now`. If nothing is due, no proxy can be assigned to this candidate; when no
   lease at all could be issued the call ends as `no_proxy_available` (see below).
2. Candidates are sampled in **windows of 16**, for at most **3 rounds**. When more than 48 proxies
   are due, the window offset is random; otherwise the windows walk the head of the set.
3. Each sampled proxy is read once (`st`, `kd`, `rg`, `pv`, `tg`, `mc`, `al`, `sc`, `sts`, `cd`,
   `gcd`, `pid`). A proxy that cannot serve a lease right now **leaves the due range** so that later
   samples in the same call and the next calls do not keep hitting it:
   - not `active` → removed from `pxrdy`;
   - cooling (`max(cd, gcd) > now`) → pushed to the end of the cooldown;
   - saturated (`al >= max_concurrency`) → pushed by `min(lease_ttl, 5 s)`.
4. Policy filters are applied: `kinds`, `providers`, `regions`, all of `tags`, and region equality
   when region matching is on.
5. Survivors are weighted by `max(score, 5)²`, where `score` is the proxy × site health score decayed
   to now, and one is drawn by roulette. Squaring the score makes a healthy proxy strongly preferred
   without ever excluding a weak one.
6. Sampling stops at the first round that produced at least one candidate.
7. On success the proxy's `al` is incremented. If it reaches `max_concurrency` the proxy is pushed in
   `pxrdy` to the lease expiry (when `max_concurrency` is 1) or by `min(lease_ttl, 5 s)`. Releasing,
   renewing out or reaping the lease brings it back.

When no candidate survives, `Acquire` retries within its `wait_ms` budget (0–5000 ms, server cap
5 s) and then fails with `no_proxy_available` and a `retry_after_ms` computed from the earliest availability time in `pxrdy`
(clamped to 50 ms…60 s, 1 s when the head is already due). See
[Node API reference](./13-node-api.md).

The proxy ID and URL are then resolved outside the script: the resolver decrypts the URL, renders the
session template if there is one, and returns `{proxy_id, url, kind, region}`. Decrypted URLs are
cached per proxy for 30 seconds (LRU, 100 000 entries) and dropped immediately on any proxy state
event, so an edited URL or template takes effect at once.

---

## Session templates and rotating gateways

Many providers expose a single gateway host and select the exit through the **user name**:
`user-<account>-session-<id>@gateway:8000`. `session_template` renders that user name per lease.

| Placeholder | Renders to |
| --- | --- |
| `{username}` | The user name stored in the proxy URL |
| `{password}` | The password stored in the proxy URL |
| `{identity_id}` | ID of the leased identity |
| `{identity_hash}` | First 12 hex characters of `sha256(identity_id)` — stable per identity |
| `{lease_id}` | ID of the lease |
| `{random}` | 8 random hex characters, new for every lease |

Rules: the template is at most 512 bytes, braces cannot be escaped, an unknown placeholder or an
unterminated `{` is a validation error. The rendered value replaces **only the user name**; the
password from the stored URL is kept. A template that renders to an empty user name leaves the
stored URL untouched.

```text
# Sticky exit per identity: the same identity always gets the same session id.
http://user:pass@tunnel.provider-a.example:9000 kind=tunnel session_template=user-{username}-session-{identity_hash}

# A fresh exit for every lease.
http://user:pass@tunnel.provider-a.example:9000 kind=tunnel session_template=user-{username}-session-{random}
```

Use `{identity_hash}` (not `{identity_id}`) when the gateway limits the length of the user name:
it is short and stable. Use `{random}` or `{lease_id}` for a rotating gateway that should give a new
IP per request. For a rotating gateway you usually also want `kind: tunnel` and a
`max_concurrency` that matches the plan you bought, because Spinneret cannot see how many exits sit
behind the one host.

---

## Identity–proxy binding

In `bind_identity` mode each identity carries the hot-state field `px`, the proxy it is bound to,
plus `rbd` (the UTC day of the last rebind, `YYYYMMDD`) and `rbn` (rebinds on that day).

On every acquire the bound proxy is checked before the identity is even considered a candidate:

| Situation | What happens |
| --- | --- |
| No binding yet | A proxy is selected from the pool and the identity is bound to it (`binding = b`) |
| Bound proxy `active`, not cooling, has capacity | The bound proxy is used; no selection happens |
| Bound proxy `active`, not cooling, saturated | The **identity** is pushed by `min(lease_ttl, 5 s)` and another identity is tried |
| Bound proxy `active` but cooling, and the cooldown ends within `rebind_tolerance` | The identity is pushed to the end of that cooldown — it waits rather than moving |
| Bound proxy cooling longer than `rebind_tolerance`, or not `active` | Rebind: a new proxy is selected, excluding the old one (`binding = r`) |
| Rebind needed but `rbn` for today already reached `max_rebinds_per_day` | The identity is pushed by 10 minutes and skipped |

Defaults: `rebind_tolerance: 5m`, `max_rebinds_per_day: 3`. `max_rebinds_per_day: 0` disables
rebinding completely — an identity whose proxy goes bad simply waits. The counter resets at
00:00 UTC.

In a batch acquire, a bound proxy that has already served a lease inside the same call is
re-checked against `max_concurrency` before it is handed out again.

Bindings are persisted into `proxy_bindings` (`identity_id` primary key, `proxy_id`, `bound_at`,
`rebind_day`, `rebinds_today`) by an asynchronous writer: a 50 000-entry queue, flushed every second
in batches of up to 1000, three attempts per batch. The write never blocks `Acquire`; if the queue
overflows the binding is dropped and counted, because Redis remains authoritative for routing and
the next acquire or hot-state snapshot writes it again.

Deleting a proxy removes its bindings through the foreign-key cascade, and the bound identities pick
a new proxy on their next acquire.

---

## Health checking

An independent checker fetches a URL **through** every proxy on a schedule. It is the only thing
that moves a proxy between `active` and `dead` without an operator or a report.

| Setting | Variable | Default |
| --- | --- | --- |
| Check URL | `SPINNERET_PROXY_CHECK_URL` | `http://example.com/` |
| Interval between checks of one proxy | `SPINNERET_PROXY_CHECK_INTERVAL` | `60s` (minimum `1s`) |
| Timeout of one check request | `SPINNERET_PROXY_CHECK_TIMEOUT` | `10s` (minimum `100ms`) |
| Exit-IP endpoint (optional) | `SPINNERET_PROXY_EXIT_IP_URL` | empty (disabled) |
| GeoIP database (optional) | `SPINNERET_GEOIP_DB` | empty (disabled) |

Concurrency inside one run is fixed at 64 checks. See
[Configuration reference](./03-configuration.md).

### How a run works

The job `proxy_health_check` runs on **every** instance at the check interval. Proxies are sharded
across live workers by `xxhash64(proxy_id) % workers`, so each proxy is checked by exactly one
instance. A run selects proxies in state `active` or `dead` whose `next_check_at` has passed, in
pages of 500.

One check is an HTTP `GET` of the check URL through the proxy, with
`User-Agent: spinneret-proxy-check/1`, keep-alives disabled and redirects not followed. Dial, TLS
handshake, response headers and the request as a whole all use the check timeout. A transport error
or a status of 400 or above is a failure; anything else is a success and the round-trip time becomes
`last_latency_ms`. `socks5://` proxies are dialled through a SOCKS5 dialer; `http://` and `https://`
through the proxy URL.

### Exit IP and GeoIP

If `SPINNERET_PROXY_EXIT_IP_URL` is set, a successful check makes a second request to that URL
through the same proxy and parses the body — either `{"ip": "…"}` or a bare address — into
`exit_ip`. A failure of this second request is logged at debug level and does **not** fail the
check.

If `SPINNERET_GEOIP_DB` points at a GeoIP2/GeoLite2 City or Country database in `.mmdb` format, the exit IP is
resolved to a country ISO code and an English city name. These are written **only to fill gaps**:
the region is set only when the stored region is empty, and the city only when both were empty.
Values you set by hand or by import are never overwritten.

### What a failure does

| Consecutive failures | Effect |
| --- | --- |
| 1, 2 | `consecutive_check_failures` increments; the proxy stays `active` and stays in the pool |
| 3 | `active → dead`, `state_reason` becomes `health check failed 3 times: <error>`, the proxy leaves `pxrdy` |
| 4, 5, … | Checks back off: `next_check_at = now + min(interval × 2^(failures − 3), 1h)` |

The first successful check of a `dead` proxy sets it back to `active` with
`state_reason = health check succeeded`, clears the failure counter and returns it to the pool.
No other state is changed automatically: a `disabled`, `banned`, `quarantined` or `retired` proxy is
never checked and never revived by the checker.

Every check also records one observation into the proxy × site health score of **every** site of the
namespace: 100 on success, 0 on failure, EWMA with alpha 0.1 towards baseline 70, decay constant
6 hours, with the failure streak reset after an hour without failures. Only sites that already have
a hash for the proxy are touched.

State transitions write a `state_events` row and publish a `proxy.state` event with action
`health_check`; a check that merely filled in region/city publishes action `update`. Both are
visible in the console immediately.

Error messages recorded on a proxy never contain the proxy credentials — user name and password are
redacted, in raw and percent-encoded form, before anything is stored or logged.

### Checking one proxy now

`CheckProxy` (permission `proxy:operate`, console *Check now* on the row or in the detail sheet) runs
the same probe immediately and records it exactly like a scheduled check — including the failure
counter and a possible `active → dead` transition. It returns `{ok, latency_ms, exit_ip, region,
error}`.

If two instances would record a check for the same proxy at the same time, the later one is dropped
(the locked row is no longer due), so failures are never counted twice.

### Metrics

`spinneret_proxies{site,state}` is a gauge of the proxy count per site and state, refreshed after
each check run. See [Observability and alerting](./12-observability.md).

---

## Health scores, cooldowns and cross attribution

The health checker tells you whether a proxy *works*. Reports tell you whether it *works for the
target*. Both feed the same proxy × site score.

### The proxy × site score

Every report with a proxy updates `sc` (score), `sts` (timestamp), `sn` (samples) and the failure
streak `nf`/`lf` on the proxy's hash for that site, with the alpha, baseline and tau of the action
policy's `health` section (defaults 0.1 / 70 / 6h). The observation value is fixed:

| Outcome | Proxy observation | Counted when |
| --- | --- | --- |
| `success` | 100 | always |
| `proxy_error` | 0 | the blame includes the proxy |
| `network_error` | 40 | the blame includes the proxy |
| `rate_limited` | 30 | the blame includes the proxy |
| everything else | — | never affects the proxy score |

Between observations the score decays back towards the baseline, so a proxy that is simply not used
drifts to 70 and neither gains nor keeps an advantage.

### Blame

Every report is classified into an outcome and a **blame**, which decides whether the identity, the
proxy, both or neither is held responsible:

| Outcome | Default blame |
| --- | --- |
| `empty`, `captcha`, `auth_invalid`, `forbidden`, `banned` | identity |
| `rate_limited` | both |
| `proxy_error`, `network_error` | proxy |
| everything else | none |

A signal rule can override this with an explicit `blame: none \| identity \| proxy \| both`. Action
rules whose scope is a proxy are skipped unless the blame includes the proxy, and rules whose scope
is an identity or account are skipped unless the blame includes the identity.

### Cross attribution

Default blame is a guess. Cross attribution corrects it from what actually happened, using a sliding
window of which identities used which proxies:

```yaml
cross_attribution:
  enabled: true
  window: 10m
  proxy_distinct_identities: 3
  identity_distinct_proxies: 3
```

For a **risk outcome** (`rate_limited`, `captcha`, `forbidden`, `banned`) reported on time and
carrying both an identity and a proxy, two sets are kept per site for the length of the window: the
distinct identities that hit trouble on this proxy, and the distinct proxies on which this identity
hit trouble. Then:

- ≥ `proxy_distinct_identities` identities on this proxy **and** < `identity_distinct_proxies`
  proxies for this identity → blame becomes `proxy`. Three different accounts getting captchas
  through the same exit is the exit's fault, not the accounts'.
- ≥ `identity_distinct_proxies` proxies for this identity → the identity is added to the blame
  (`identity`, or `both` if the proxy was already blamed). An account that fails through three
  different exits is failing on its own.

The corrected blame is what the action rules and the health observations see. This is why an
identity can survive a bad proxy, and why a bad proxy is cooled down even though the symptom was a
captcha.

### Automatic actions on proxies

Action policy rules can target proxies:

| `scope` | Effect | Written where |
| --- | --- | --- |
| `proxy_site` (cooldown) | Cools the proxy on the reporting site only | `cd` of that site's hash |
| `proxy` (cooldown) | Cools the proxy on every site of the namespace | `gcd` on every site, persisted to `proxies.cooldown_until` |
| `proxy` (ban) | Bans the proxy everywhere | Lifecycle state on every site, persisted |
| `proxy` (quarantine) | Quarantines the proxy everywhere | Lifecycle state on every site, persisted |

The built-in default action policy contains one such rule:

```yaml
- name: proxy-error-cooldown
  when: { outcome: proxy_error }
  action: cooldown
  scope: proxy_site
  base: 2m
  max: 30m
```

Cooldowns escalate with the failure streak up to `max`. See [Policies](./08-policies.md).

---

## Manual operations

`OperateProxies` (permission `proxy:operate`) applies one operation to up to **1000** proxy IDs. In
the console: select rows, then use the bulk bar, or use the row action menu.

| Operation | Duration | Site-scoped | Effect |
| --- | --- | --- | --- |
| `disable` | — | no | → `disabled`. Not used for new leases until enabled. Fails on `retired` (restore it first) |
| `enable` | — | no | `disabled` or `dead` → `active`, failure counter cleared, checked again immediately. Fails on `banned`, `quarantined`, `retired` |
| `ban` | required, `permanent` allowed | no | → `banned` until the end; `permanent` never expires on its own. Fails on `retired` (restore it first) |
| `unban` | — | no | `banned` → `active`. Fails otherwise |
| `cooldown` | required, > 0 | yes | Skipped by the scheduler until the end. With a site: only that site (`cd`). Without: every site (`gcd`) and `proxies.cooldown_until`. Fails on `retired` (restore it first) |
| `quarantine` | optional (default 24h) | no | → `quarantined` until the end. Re-quarantining a quarantined proxy replaces the end. Fails on `banned` and on `retired` (restore it first) |
| `unquarantine` | — | no | `quarantined` → `active`. Fails otherwise |
| `archive` | — | no | → `retired`. Never used, hidden unless you filter for retired |
| `restore` | — | no | `retired` → `active`, clearing ban, cooldown and failure counter. Fails otherwise |
| `reset_stats` | — | yes | Clears score, samples and failure streak (`sc`, `sts`, `sn`, `nf`, `lf`). Without a site also clears `consecutive_check_failures` |

Durations use the form `10m`, `7d`, `500ms` with units `ms|s|m|h|d`, or `permanent` for a ban. A
`reason` of up to 512 bytes is recorded in the state event and the audit log.

Every operation is a bulk operation and answers with a result, not an error:

```json
{
  "result": {
    "matched": 12,
    "succeeded": 10,
    "failed": [
      {"id": "pxy_…", "reason": "not_found",           "message": "proxy not found"},
      {"id": "pxy_…", "reason": "failed_precondition", "message": "proxy is retired; restore it first"}
    ]
  }
}
```

`reason` is one of `not_found` (unknown, or invisible to you), `failed_precondition` (the state does
not allow the operation), `site_unknown` (the named site does not exist in that namespace) or
`internal`. IDs from several namespaces may be mixed in one call; each namespace is processed in its
own transaction.

Every operation that actually changes something writes a `state_events` row and a `proxy.state`
event on the namespace channel (the action of `unquarantine` is `activate`), so the console and any
SSE consumer see it within milliseconds. The audit entry `proxy.<operation>` is written even for a
no-op — `disable` on an already disabled proxy, `archive` on a retired one.

---

## Editing and deleting proxies

### Updating attributes

`UpdateProxy` (permission `proxy:write`) changes `kind`, `region`, `city`, `provider`,
`max_concurrency`, `tags` (send `set_tags: true`, an empty list clears them) and `session_template`.
Unset fields are left alone. A request that changes nothing writes no audit entry and no event, and
the console answers "Nothing changed"; the response always carries the full proxy.

### Replacing the URL

Sending `url` replaces the proxy URL **including credentials**. The new URL is re-sealed, `url_hash`
recomputed, `display_url`, `host`, `port` and `username_hint` updated, and `url_version`
incremented. The stored URL is never shown, so the console asks you to type the new URL twice.
New leases get the new URL immediately (the resolver cache is dropped by the state event); leases
already in flight keep the URL they were given.

### Deleting versus archiving

`DeleteProxies` (permission `proxy:write`, up to 1000 IDs) removes the proxies permanently, together
with their identity bindings, their hot state on every site and their hot-state snapshots. It cannot
be undone and the history goes with it.

**Archive instead of delete** when you may want the record back or want to keep the trail: `archive`
retires the proxy, keeps everything, and `restore` brings it back.

The console warns before deleting when identities are bound to the selected proxies. Deletion is
done per namespace in one transaction that removes the hot state first; if anything fails, that
namespace is left untouched, its hot state is restored best-effort and the affected IDs come back as
failures with reason `internal`, so a retry is safe.

---

## Provider statistics

The *Providers* tab (`GetProviderStats`, permission `proxy:read`) aggregates the pool by the
`provider` attribute over the last hour, 24 hours or 7 days — or any explicit range:

| Column | Meaning |
| --- | --- |
| `proxies` | Proxies with this provider (retired excluded) |
| `active` / `dead` | Of those, how many are in each state |
| `requests` | Reports through this provider's proxies in the range |
| `success_ratio` | Share of reports with outcome `success` |
| `risk_ratio` | Share with outcome `rate_limited`, `captcha`, `forbidden` or `banned` |
| `avg_latency_ms` | Average reported latency |

`GetProviderStats` also takes `site`; an empty `site` aggregates every site you can read, which is
what the console sends.

Rows are ordered by request count. Proxies without a provider are grouped under an empty name. Only
sites you can read are counted, so two users may legitimately see different numbers.

This is the table that answers "which provider is burning my identities". A provider with a high
risk ratio and a healthy check pass rate is being detected, not broken; a provider with a low check
pass rate is simply down.

---

## Permissions and API

| Permission | Grants |
| --- | --- |
| `proxy:read` | `ListProxies`, `GetProxy`, `GetProviderStats` |
| `proxy:write` | `ImportProxies`, `UpdateProxy`, `DeleteProxies` |
| `proxy:operate` | `OperateProxies`, `CheckProxy` |

Roles: `viewer` has `proxy:read`; `operator` and above add `proxy:write` and `proxy:operate`. The API
token scope `proxy:write` grants all three permissions. A site-scoped binding holds only
`proxy:read` (and `namespace:read`) at namespace level: it can list proxies and see the per-site
state of its own sites, but it cannot import, edit, delete or operate proxies at all. The `site`
argument of `cooldown` and `reset_stats` narrows the effect of the operation, not the permission it
needs. A proxy you cannot read answers `not_found` rather than
`permission_denied`, so proxy IDs of other tenants are not disclosed. See
[Tenants, users and tokens](./11-access-control.md).

All eight RPCs live on `ProxyAdminService` and are reachable over Connect, gRPC and HTTP + JSON at
`/spinneret.v1.ProxyAdminService/<Method>`:

```bash
curl -sS https://spinneret.example.com/spinneret.v1.ProxyAdminService/ListProxies \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"namespace":"default","states":["active"],"kinds":["residential"],"page_size":50}'
```

```bash
curl -sS https://spinneret.example.com/spinneret.v1.ProxyAdminService/ImportProxies \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"namespace":"default","format":"lines","dry_run":true,
       "defaults":{"kind":"datacenter","provider":"provider-a"},
       "data":"http://user:pass@203.0.113.10:8000 region=US\n"}'
```

Listing supports filters on `states`, `kinds`, `providers`, `regions`, `tags` (all required) and a
`search` string matching a substring of the display URL, host or exit IP, or an ID prefix. Pages are
50 by default, 500 at most, and continue with the opaque `next_page_token`.

For quick load-test pools, `spnr seed --proxies N --proxy-url 'http://user-{i}:secret@host:9091'`
imports N generated proxies and publishes a rotation policy with `mode: pool`. See
[CLI reference](./15-cli.md).

---

## Operating the pool

### Sizing

Start from concurrency, not from a proxy count. The pool must supply

```text
peak concurrent leases  ≈  requests per second × average request duration (s)
```

and each proxy supplies `max_concurrency` of that. Leave headroom: at any moment some proxies are
cooling, some are dead, and `max_concurrency` is an upper bound the scheduler actively avoids —
a saturated proxy is pushed out of the due range, so a pool sized exactly to peak will produce
`no_proxy_available` at peak.

In `bind_identity` mode the pool must instead be large enough that the identities that are
schedulable at the same time can each hold their own binding. Roughly: `proxies × max_concurrency ≥
identities you want live at once`.

`max_concurrency` should reflect what the provider allows, not what the machine can push. For
rotating gateways the value is a policy choice: the gateway will happily accept more, and the
resulting parallel exits are what gets you detected.

### Mixing providers

- Set `provider` on every import. Without it the *Providers* tab is empty and you cannot tell two
  vendors apart when one degrades.
- Use `tags` for anything you want to route on that is not kind, region or provider — `primary`,
  `backup`, `cheap`, a contract name. The rotation filter requires all listed tags, so tags compose.
- Keep a second provider imported but tagged out of the bound policies. Switching then means editing
  one `proxy.tags` list in a rotation policy, not importing a pool under pressure.
- Do not mix kinds in one endpoint group unless you mean to. A `residential` exit and a
  `datacenter` exit will not behave the same, and the weighted pick will quietly prefer whichever
  currently scores better.
- Give residential and mobile proxies a lower `max_concurrency` than datacenter proxies. They are
  usually a single household or handset line.

### "no proxy available"

A node receiving `no_proxy_available` means the site's ready queue had nothing usable at that
moment. Work through this in order:

1. **Is any proxy in the pool active?** Console → Proxies, filter state `active`. `0` means the pool
   is empty, disabled or dead; `spinneret_proxies{state="dead"}` climbing points at the provider or
   the check URL.
2. **Do the policy filters match anything?** Open the bound rotation policy and compare
   `proxy.kinds`, `proxy.providers`, `proxy.regions` and `proxy.tags` with the filter bar on the
   Proxies page. A tag typo excludes the whole pool silently — filters are ANDed and there is no
   warning when the intersection is empty.
3. **Is region matching biting?** With `mode: region_match` or `region_match: true`, an identity with
   no region only matches proxies with no region. Compare the region of the identities being
   scheduled with the regions in the pool.
4. **Is everything cooling?** The detail sheet shows the per-site cooldown, and the global cooldown
   is on the record. A storm of `proxy_error` reports plus the default `proxy-error-cooldown` rule
   cools proxies for up to 30 minutes. Fix the cause, then `cooldown` is the one thing you can undo
   with a shorter manual cooldown — a manual cooldown overwrites an automatic one, in both
   directions.
5. **Is everything saturated?** Per-site `active_leases` at `max_concurrency` across the pool means
   you need more proxies, a higher `max_concurrency`, or fewer concurrent leases. Look at the
   retry-after the node received: it is the time until the earliest proxy becomes available, so a
   value near the lease TTL means capacity, while 60 s means nothing is scheduled to come back soon.
6. **In `bind_identity` mode:** identities whose bound proxy is bad and whose daily rebind budget is
   spent are pushed for 10 minutes and look like an identity shortage. Raise
   `max_rebinds_per_day`, or fix the proxies.

`no_proxy_available` is a `RESOURCE_EXHAUSTED` with a retry-after; the node should back off and
retry, not fail the job. See [Troubleshooting](./18-troubleshooting.md).

### Other symptoms

| Symptom | Likely cause |
| --- | --- |
| Proxies flip between `active` and `dead` | Check timeout too tight for the provider's latency, or the check URL is rate-limiting the checker. Raise `SPINNERET_PROXY_CHECK_TIMEOUT`, or point `SPINNERET_PROXY_CHECK_URL` at a host you control |
| Checks pass but every request fails | The check URL is reachable through the proxy and the target is not. Check with a real target URL |
| All proxies show score 70, 0 samples | No reports carry a proxy — the endpoint group's rotation policy is probably still `mode: none` |
| One provider's risk ratio far above the others | That provider's exits are being detected. Cool them down or tag them out before they burn identities |
| Import says `unchanged` for everything | The URLs already exist and the row sets no attribute that differs. Set the attributes you want to change explicitly, or via `defaults` |
| Exit IP always empty | `SPINNERET_PROXY_EXIT_IP_URL` is not set, or the endpoint is not reachable through the proxy |

### Routine maintenance

- Re-import the provider's current list on a schedule. Import is idempotent: existing proxies are
  updated, not duplicated, and new ones are created.
- `archive` proxies you stop paying for instead of deleting them; you keep the history and the
  provider statistics stay comparable over time.
- After a provider incident, `reset_stats` on the affected proxies clears the depressed scores so
  they compete fairly again instead of waiting out the 6-hour decay.
- Back up the KEK. Without it the stored proxy URLs cannot be decrypted. See
  [Operations runbook](./16-operations.md).

---

## Next

- [Policies](./08-policies.md) — the rotation policy that chooses the assignment mode and filters,
  and the action policy that cools down and bans proxies.
- [Identities and accounts](./06-identities.md) — the other half of a lease, and the identity region
  that region matching compares against.
- [Node API reference](./13-node-api.md) — what the node receives in `AcquireResponse.proxy` and how
  to report a proxy failure.
- [Observability and alerting](./12-observability.md) — `spinneret_proxies`, risk events and the
  request explorer.
- [Configuration reference](./03-configuration.md) — the `SPINNERET_PROXY_*` and `SPINNERET_GEOIP_DB`
  variables.
- [Troubleshooting](./18-troubleshooting.md) — the complete error-reason table.
