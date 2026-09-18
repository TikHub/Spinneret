# Policies

**Policies are how you tell Spinneret to select identities, read request results and react to them. This page covers all four policy kinds, their complete YAML, the draft/publish lifecycle, the binding hierarchy, the rule debugger and shadow mode.**

[中文](../zh/08-policies.md)

---

## Contents

- [What a policy is](#what-a-policy-is)
- [The lifecycle: draft, publish, version, rollback](#the-lifecycle-draft-publish-version-rollback)
- [Bindings and resolution](#bindings-and-resolution)
- [Fields every policy has](#fields-every-policy-has)
- [Rotation policies](#rotation-policies)
- [Signal policies](#signal-policies)
- [Action policies](#action-policies)
- [Breaker policies](#breaker-policies)
- [The rule debugger](#the-rule-debugger)
- [Testing a policy safely: shadow mode](#testing-a-policy-safely-shadow-mode)
- [Troubleshooting: my policy does not seem to apply](#troubleshooting-my-policy-does-not-seem-to-apply)

---

## What a policy is

A policy is a named, versioned YAML document that belongs to one namespace and has one kind. It
is stored as a draft while you edit it, published as an immutable numbered version when you are
happy with it, and *bound* to a place in the namespace hierarchy so that it actually takes effect.

There are exactly four kinds, and every endpoint group has exactly one of each in effect at any
moment:

| Kind | Console name | What it controls |
| --- | --- | --- |
| `rotation` | Rotation | Which identity types are eligible, how one is selected, how long the lease lasts, quotas, concurrency, sticky sessions, and which proxy is paired with it. |
| `signal` | Signal | Ordered rules that turn the raw facts of a report (status, business code, error kind, markers, URI, method, latency, size) into one of twelve *outcomes* and a *blame*. |
| `action` | Action | What each outcome does: cooldowns, bans, expiry, quarantine, the health score, ban escalation and cross attribution. |
| `breaker` | Breaker | When an endpoint group trips open, how long it stays open, how it probes its way back and what happens to recent cooldowns. |

The three that run on every report do so in this order:

```text
Report  ──signal policy──▶  outcome + blame  ──action policy──▶  planned actions
                                  │
                                  └──▶ breaker window counters ──breaker policy──▶ open / half-open / closed
```

The rotation policy runs on the other side, at `Acquire` time, and consumes the state the action
and breaker policies produce. See [Concepts](./04-concepts.md) for the whole request flow.

Every new namespace is created with the four built-in defaults — `default-rotation`,
`default-signal`, `default-action`, `default-breaker` — published as version 1 and bound at
namespace level. A working system therefore exists before you write a single policy.

### The four built-in defaults

These are what a fresh namespace actually runs, and they are also the `builtin` level at the bottom
of the binding hierarchy. `default-rotation` is exactly the schema defaults in the
[rotation table](#rotation-policies) below, and `default-breaker` exactly those in the
[breaker table](#breaker-policies). The other two are worth reading before you write your own.

`default-signal` — eleven rules, first match wins, `trust_outcome_hint: false`:

| Rule | Condition | Outcome |
| --- | --- | --- |
| `proxy-error` | `error_kind: [proxy_auth, conn_refused]` | `proxy_error` |
| `network-error` | `error_kind: [timeout, conn_reset, tls, dns]` | `network_error` |
| `captcha` | `markers: [captcha_page]` | `captcha` |
| `login-redirect` | `markers: [login_redirect]` | `auth_invalid` |
| `rate-limited` | `http_status: [429]` | `rate_limited` |
| `target-error` | `http_status: { gte: 500 }` | `target_error` |
| `empty-list` | `http_status: [200]` and `markers: [empty_list]` | `empty` |
| `client-error` | `http_status: [400]` | `client_error` |
| `auth-invalid` | `http_status: [401]` | `auth_invalid` |
| `forbidden` | `http_status: [403]` | `forbidden` |
| `success` | `http_status: { gte: 200, lt: 300 }` | `success` |

Anything that matches none of them is `unknown` with blame `none`. The markers `captcha_page`,
`login_redirect` and `empty_list` are what the default policy expects your node to report — nothing
detects them for you.

`default-action` — seven rules, every match evaluated:

| Rule | Reacts to | Action | Scope | Timing |
| --- | --- | --- | --- | --- |
| `rate-limited-cooldown` | `rate_limited` | `cooldown` | `identity_endpoint` | `base: 60s`, `multiplier: 2`, `max: 30m` |
| `empty-cooldown` | `empty`, 3 within `10m` | `cooldown` | `identity_endpoint` | `base: 5m`, constant |
| `captcha-cooldown` | `captcha` | `cooldown` | `identity_site` | `base: 30m`, `multiplier: 2`, `max: 6h` |
| `captcha-ban` | `captcha`, 3 within `24h` | `ban` | `identity` | `12h` |
| `auth-invalid-expire` | `auth_invalid` | `expire` | `identity` | — |
| `banned-account` | `banned` | `ban` | `account` | `permanent` |
| `proxy-error-cooldown` | `proxy_error` | `cooldown` | `proxy_site` | `base: 2m`, `max: 30m`, constant |

plus the escalation ladder (2 bans within `7d` → `72h`, 3 bans within `30d` → `permanent`), the
health defaults listed under [Action policies](#action-policies), `ban_expiry_state: pending` and
`cross_attribution` enabled with both thresholds at 3.

Note what is *not* there: no rule reacts to `network_error`, `forbidden`, `target_error`,
`client_error` or `unknown`. `forbidden` still moves the health score (and `network_error` does when
the blame includes the identity), so those two can still reach an identity through
`health.endpoint_low` and `health.quarantine` — but nothing else happens until you write a rule.

![Policies](../images/policies.png)

Policies live in the console under **Scheduling → Policies**. The page has four views:
**Policies** (the editor), **Bindings**, **Resolve** and **Rule debugger**.

---

## The lifecycle: draft, publish, version, rollback

A policy has two bodies at once: the **draft** (mutable, your work in progress) and the
**current published version** (immutable, what the system actually runs).

```text
create ──▶ draft ──save draft──▶ draft ──publish──▶ v1 ──▶ draft ──publish──▶ v2
                                                     │
                                                     └──rollback to v1──▶ v3 (= the YAML of v1)
```

| Operation | Permission | What happens |
| --- | --- | --- |
| Create | `policy:write` (plus `policy:publish` when publishing straight away) | The YAML is parsed and validated; its `name`, `description` and optional `bind` block are taken from the document. Stored as the draft, or published as version 1. |
| Save draft | `policy:write` | The YAML must parse and validate as a policy of the same kind, and the `name` must not change. `extends` references and `bind` targets are *not* checked yet. |
| Publish | `policy:publish` | The draft is validated, the `extends` chain must resolve among **published** policies and compile, and the document becomes version `current + 1`. The draft is cleared. |
| Roll back | `policy:publish` | The YAML of an older version is published again as a *new* version. The draft is left alone. Rolling back to the version that is already current is refused. |
| Delete | `policy:write` **and** `policy:publish` | The policy, its draft and every version are deleted. Refused while published policies `extend` it, and refused for a `default-<kind>` policy that is still bound at namespace level. |

Other useful operations:

- **Validate** (`policy:read`) parses YAML without storing anything and returns either the list
  of problems (each as `path: message`, for example `rules[2].scope: ban does not support scope
  identity_endpoint (allowed: identity|account|proxy)`) or the **normalized YAML** — your
  document with every default filled in. This is the fastest way to see what a default actually
  is.
- **Diff** compares two sides. In the API, `from_version: 0` means the current published version
  and `to_version: 0` means the draft (or the current published version when there is no draft);
  the response carries both documents and a unified diff with three lines of context. In the
  console this is the **Versions** tab: pick two versions, or a version and the draft.
- **Optimistic concurrency**: publishing with `expected_version` set to a positive number fails
  with a conflict unless the policy really is at that version. Use it when two people edit the
  same policy.

Limits and rules that apply to every write:

| Limit | Value |
| --- | --- |
| YAML document size | 1 MiB |
| Publish comment | 1024 characters |
| Versions per policy | up to 2147483647 |
| Policy name | `^[a-z0-9][a-z0-9._-]{0,63}$`, unique per namespace and kind, **immutable after creation** |
| `extends` chain depth | 5 policies including the leaf |

Publishing is not lazy. The transaction commits, the namespace catalog snapshot is invalidated
locally and peer instances are notified (they reload after a 100 ms debounce), and a
`policy.published` event is pushed to the console over the event stream. A full safety-net reload
runs every 60 seconds regardless. In practice a published policy is in effect on every instance
within a second or two.

Every write is audited as `policy.create`, `policy.save_draft`, `policy.publish`,
`policy.rollback`, `policy.delete`, `policy.bind` or `policy.unbind` — see
[Tenants, users and tokens](./11-access-control.md).

---

## Bindings and resolution

A policy does nothing until it is bound. A **binding** attaches one policy to one *target* — the
namespace, a site, a site + client, or a site + client + endpoint group — and there can be at most
one binding per kind per target. So: one namespace-level binding per kind, one per site, one per
site + client, one per site + client + endpoint group. Two sites can each carry their own
site-level action binding; a level is not a single slot.

| Level | Binding names | Wins over |
| --- | --- | --- |
| `endpoint_group` | site + client + endpoint group | everything below |
| `client` | site + client | site, namespace, built-in |
| `site` | site | namespace, built-in |
| `namespace` | nothing | built-in |
| `builtin` | — | nothing; this is the fallback compiled into the server |

Resolution walks from the most specific to the least specific and takes the first level that has
a binding of that kind. It is per kind: an endpoint group can take its rotation policy from an
endpoint-group binding and its breaker policy from the namespace binding in the same breath.

Two rules catch people out:

- A binding row that names a client or an endpoint group but no site never matches anything.
- When several rows somehow share the winning level, the first one wins.

### A worked example

Namespace `crawlers` has site `example-site` with clients `web` and `mobile`, and `web` has the
endpoint groups `search` and `detail`. Suppose these action-policy bindings exist:

| Binding | Level | Policy |
| --- | --- | --- |
| (namespace) | `namespace` | `default-action` |
| `example-site` | `site` | `site-strict` |
| `example-site` + `web` | `client` | `web-lenient` |
| `example-site` + `web` + `search` | `endpoint_group` | `search-aggressive` |

The action policy that applies is then:

| Target | Winning level | Policy |
| --- | --- | --- |
| `example-site` / `web` / `search` | `endpoint_group` | `search-aggressive` |
| `example-site` / `web` / `detail` | `client` | `web-lenient` |
| `example-site` / `mobile` / anything | `site` | `site-strict` |
| another site in the namespace | `namespace` | `default-action` |

Delete the namespace binding as well and the last row falls through to `default-action` again —
this time as the *built-in* default rather than the stored policy of the same name.

### Seeing what actually applies

Three ways, in increasing order of authority:

1. **Console → Policies → Resolve.** Pick a site, client and endpoint group; the panel shows one
   row per kind with the policy name, its version, the winning level and, on demand, the
   *effective* YAML.
2. **The API**, `ResolvePolicies`, which is what that panel calls. It returns one `ResolvedPolicy`
   per kind — `kind`, `policy_id`, `name`, `version`, `level` and `yaml` — ordered rotation,
   signal, action, breaker.
3. **The rule debugger**, which not only resolves but runs the policies against a report.

For an endpoint group, `ResolvePolicies` reports the references from the catalog snapshot — the
policies actually in effect, not a fresh recomputation. For signal and action policies the
returned YAML is **flattened**: the `extends` chain is applied, rules are concatenated root first,
and a header comment names the chain.

**Note.** If a stored version no longer parses, or its `extends` chain no longer resolves, the
server falls back to the built-in default for that kind, logs a warning and reports the built-in
default in `ResolvePolicies`. That is the one case where the console shows level `builtin` even
though a binding exists.

### Binding from the console or from the YAML

You can bind in three places:

- **Policies → Bindings → New binding**, or the **Bindings** tab of a policy. Requires
  `policy:publish` on the target (the namespace, or the site for a site/client/endpoint-group
  binding) and `policy:read` on the policy. The policy must already have a published version.
- The optional **`bind` block inside the YAML** (see below). It is applied when the policy is
  first published and whenever it differs from the `bind` block of the current version.
  Republishing an unchanged `bind` block therefore never reverts a binding you changed by hand
  in the meantime.
- The API, `SetBinding` / `DeleteBinding`.

Deleting a binding makes the next less specific binding apply — or the built-in default, if that
was the last one.

---

## Fields every policy has

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `name` | string | — | Required. `^[a-z0-9][a-z0-9._-]{0,63}$`. Unique per namespace and kind. Cannot be changed after the policy is created. |
| `description` | string | `""` | Free text, at most 1024 characters. Shown in the policy list. |
| `bind` | object | none | Optional binding target applied on publish. `bind.site` is required when the block is present; `bind.client` requires a site; `bind.endpoint_group` requires a client. Each value is at most 128 characters. |
| `extends` | string | `""` | Signal and action policies only. The name of another policy of the same kind in the same namespace whose rules run **before** this policy's rules. |

The YAML parser is strict: unknown fields are rejected, a document must contain exactly one YAML
document, and an empty document is an error. Durations are written as `500ms`, `30s`, `10m`,
`24h`, `7d`, `1h30m`, `1d12h`, `0` or the keyword `permanent` where a permanent value is legal.

Anything you leave out gets a default. Fields whose zero value is a legal setting — `enabled:
false`, a trip threshold of `0`, `reuse_interval: 0s`, `max_rebinds_per_day: 0` — are only
defaulted when the field is absent, so writing the zero explicitly preserves it.

---

## Rotation policies

A rotation policy answers the question `Acquire` asks: *which identity, for how long, through
which proxy?*

### Schema

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `identity_types` | list of strings | `[]` | Identity types eligible for this endpoint group. Empty means every identity type of the site and client. Each name at most 64 characters. |
| `rotation.strategy` | enum | `weighted_random` | `weighted_random`, `least_recently_used`, `round_robin`, `best_health`. |
| `rotation.candidate_sample` | int | `32` | Candidates sampled from the ready set per acquire. 1–256. |
| `rotation.lease_ttl` | duration | `2m` | Lease lifetime granted by one `Acquire` or `Renew`. 5s–30m. |
| `rotation.max_lease_lifetime` | duration | `30m` | Renewals cannot extend a lease beyond this. Must be at least `lease_ttl`, at most 24h. |
| `rotation.max_concurrent_leases` | int | `1` | Concurrent leases of one identity in this endpoint group. `1` makes the identity exclusive. 1–10000. |
| `rotation.reuse_interval` | duration | `0s` | Minimum gap between two uses of the same identity. `0s` disables it. |
| `rotation.reuse_anchor` | enum | `released` | Whether the gap is measured from when the lease was `acquired` or when it was `released`. |
| `rotation.reuse_scope` | enum | `endpoint_group` | Whether the gap applies within this `endpoint_group` only or across the whole `site`. |
| `rotation.quota` | list of `{limit, window}` | `[]` | Requests per identity in this endpoint group per window, as a sliding-window estimate. `limit` ≥ 1, `window` ≥ 1s, windows must be distinct. |
| `rotation.sticky.enabled` | bool | `false` | Whether a `session_key` sent with `Acquire` pins one identity. |
| `rotation.sticky.ttl` | duration | `10m` | How long the session → identity mapping lives. At least 1s. |
| `rotation.warmup.duration` | duration | `0s` | How long an identity counts as freshly activated. `0s` disables warm-up. |
| `rotation.warmup.quota_factor` | float | `1` | Quota multiplier during warm-up. Greater than 0, at most 1. |
| `rotation.probe.weight_factor` | float | `0.1` | Under `weighted_random`, the multiplier applied to the weight of a `pending` identity **and** the probability with which such a candidate is accepted once picked. Greater than 0, at most 1. |
| `rotation.probe.max_leases` | int | `2` | Concurrent-lease cap for `pending` identities, applied instead of `max_concurrent_leases` when it is lower. At least 1. |
| `proxy.mode` | enum | `none` | `none`, `pool`, `bind_identity`, `region_match`. |
| `proxy.kinds` | list | `[]` | Filter: `datacenter`, `residential`, `mobile`, `tunnel`. Empty means no filter. |
| `proxy.tags` | list | `[]` | Filter on proxy tags. |
| `proxy.providers` | list | `[]` | Filter on proxy providers. |
| `proxy.regions` | list | `[]` | Filter on proxy regions. |
| `proxy.region_match` | bool | `false` | Require the proxy region to match the identity region. |
| `proxy.rebind_tolerance` | duration | `5m` | In `bind_identity` mode, how long to wait for a cooling bound proxy before rebinding the identity to another one. |
| `proxy.max_rebinds_per_day` | int | `3` | In `bind_identity` mode, how many times per day an identity may be rebound. `0` forbids rebinding. |

Proxy modes are described in full on [Proxies](./07-proxies.md).

The four selection strategies:

| Strategy | Picks | Reads the health score |
| --- | --- | --- |
| `weighted_random` | A random candidate, weighted by the **square** of its decayed health score clamped to 5–100, so a score of 100 is four times as likely as a score of 50 and 400 times as likely as a score of 5. A fading identity loses traffic gradually instead of being cut off. | yes |
| `best_health` | The candidate with the highest decayed score. Half-open breaker probes force this strategy whatever the policy says. | yes |
| `least_recently_used` | The candidate whose last use is oldest. | no |
| `round_robin` | The lowest identity id above the one this endpoint group picked last, wrapping round to the lowest id of the sample. The cursor is kept per endpoint group. | no |

### How the parameters interact at acquire time

- The breaker is checked first. An open breaker refuses the acquire; a half-open breaker allows
  at most `half_open.probe_leases_per_10s` probe leases per ten seconds, and those probes always
  use `best_health` and ignore sticky sessions.
- If sticky sessions are on, the request carries a session key and the call asks for exactly **one**
  lease, the pinned identity is tried first and is allowed to skip the reuse interval. A batch
  acquire that asks for more than one lease ignores the session key entirely.
- Otherwise the scheduler samples up to `candidate_sample` identities from the ready set and lets
  the strategy pick among them, as in the table above. Under `weighted_random`, a `pending`
  identity has its weight multiplied by `probe.weight_factor` *and* is only accepted with that
  probability once picked, so it takes roughly `weight_factor²` of the traffic it would otherwise
  get.
- A picked candidate is then filtered: identity state, cooldowns, the reuse interval, the
  concurrency cap (`probe.max_leases` for pending identities), the quota windows and, in
  `bind_identity` mode, the bound proxy. A candidate that fails is pushed forward in the ready set
  to the moment it could serve again and the strategy picks another one.
- Quota windows are scaled by `warmup.quota_factor` while the identity is within
  `warmup.duration` of its activation, with a floor of one request.

### Example: exclusive identities with an hourly quota

```yaml
name: search-rotation
description: One lease at a time per identity, 60 requests per hour, no proxy.
bind:
  site: example-site
  client: web
  endpoint_group: search
identity_types: [cookie]
rotation:
  strategy: weighted_random
  candidate_sample: 32
  lease_ttl: 90s
  max_lease_lifetime: 10m
  max_concurrent_leases: 1        # exclusive
  reuse_interval: 45s             # cool the identity between two uses
  reuse_anchor: released
  reuse_scope: site               # the gap applies across the whole site
  quota:
    - { limit: 60, window: 1h }
    - { limit: 600, window: 24h }
  warmup:
    duration: 24h                 # new identities get 10 % of the quota for a day
    quota_factor: 0.1
  probe:
    weight_factor: 0.05
    max_leases: 1
proxy:
  mode: none
```

### Example: sticky sessions over a residential proxy pool

```yaml
name: detail-sticky
description: Pin a session to one identity and one residential proxy region.
bind:
  site: example-site
  client: mobile
  endpoint_group: detail
identity_types: [device, cookie]
rotation:
  strategy: best_health
  candidate_sample: 64
  lease_ttl: 5m
  max_lease_lifetime: 30m
  max_concurrent_leases: 4        # four workers may share one identity
  reuse_interval: 0s
  sticky:
    enabled: true
    ttl: 30m                      # a session_key keeps its identity for half an hour
proxy:
  mode: bind_identity             # one identity always exits through the same proxy
  kinds: [residential]
  regions: [eu-west]
  region_match: true
  rebind_tolerance: 2m
  max_rebinds_per_day: 1
```

---

## Signal policies

A signal policy is an ordered list of rules. Each rule is a set of conditions and an outcome. The
**first matching rule wins**; its outcome and blame are the classification of the report.

### Schema

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `trust_outcome_hint` | bool | `false` | When no rule matches, use the node's own `outcome_hint` if it is a valid outcome. Taken from the leaf policy of an `extends` chain. |
| `rules` | list | `[]` | The rules, in evaluation order. |
| `rules[].name` | string | `""` | Optional. Same pattern as a policy name, unique within the policy. Unnamed rules are labelled `<policy>.rules[i]`. |
| `rules[].when` | object | `{}` | The conditions. **All present conditions must hold.** A rule with no conditions matches every report. |
| `rules[].outcome` | enum | — | Required. One of the twelve outcomes below. |
| `rules[].blame` | enum | — | Optional override of the outcome's default blame: `none`, `identity`, `proxy`, `both`. |

### The condition language

| Condition | Form | Matches when |
| --- | --- | --- |
| `http_status` | a single integer, a list of integers, or a range `{gte, gt, lte, lt}` | The list form matches the reported status exactly; `0` in a list matches a report that carried **no** status. The range form only ever matches a real status (a missing status never matches a range). Values 0–999. Decimal literals must not have leading zeros. |
| `business_code` | string or list of strings | The report's business code is non-empty and listed. Numbers are normalized to their decimal string form, so `[10001]` and `["10001"]` are the same. |
| `error_kind` | list | The node's transport error kind is listed: `timeout`, `conn_reset`, `conn_refused`, `proxy_auth`, `tls`, `dns`, `other`. |
| `markers` | list | **Any** marker the node reported is in this list. |
| `uri` | `{prefix: /path}` or `{regex: ...}` | Exactly one of the two. `prefix` must start with `/` and is a plain prefix match on the normalized request path. `regex` is RE2, matched anywhere in the path. Each at most 1024 characters. |
| `method` | list | The request method is listed, compared case-insensitively. Letters only, at most 16 characters per token. |
| `latency_ms` | range `{gte, gt, lte, lt}` | The reported latency in milliseconds is in the interval. |
| `response_bytes` | range `{gte, gt, lte, lt}` | The reported response body size is in the interval. |

Ranges need at least one bound; `gte`/`gt` and `lte`/`lt` are mutually exclusive, and an empty
interval is a validation error.

### The outcomes

There are exactly twelve. `blame` decides whether the identity, the proxy, both or neither is
held responsible — action rules are gated on it.

| Outcome | Default blame | Counts as risk | Advances the identity failure streak |
| --- | --- | --- | --- |
| `success` | `none` | no | no |
| `empty` | `identity` | no | yes |
| `rate_limited` | `both` | yes | yes |
| `captcha` | `identity` | yes | yes |
| `auth_invalid` | `identity` | no | yes |
| `forbidden` | `identity` | yes | yes |
| `banned` | `identity` | yes | yes |
| `proxy_error` | `proxy` | no | no |
| `network_error` | `proxy` | no | only when blamed on the identity |
| `target_error` | `none` | no | no |
| `client_error` | `none` | no | no |
| `unknown` | `none` | no | no |

*Risk* outcomes feed the breaker's risk ratio. The last column is the **identity** failure streak,
which drives exponential backoff for `identity_endpoint`, `identity_site`, `identity` and `account`
cooldowns.

**Note.** The **proxy** streak follows a different rule: whenever the blame includes the proxy, it
is advanced by every failure outcome *and* by `proxy_error` and `network_error`. A `proxy_site` or
`proxy` cooldown with a `multiplier` above 1 therefore does escalate on transport errors, even
though the table says they are not identity failures.

When no rule matches, the report is classified `unknown` with blame `none` — unless
`trust_outcome_hint` is on and the node sent a valid `outcome_hint`, in which case the hint is
used with its default blame.

### Extending a signal policy

`extends` concatenates rule lists: the parent's rules run first, then the child's. Because the
first match wins, a child can only *append* fallbacks, never override an earlier parent rule. The
chain resolves against published policies only, is at most five policies deep and may not contain
a cycle. `trust_outcome_hint` comes from the leaf.

### Example: a site that signals through business codes

```yaml
name: partner-api-signal
description: The API answers 200 with a business code; only markers reveal captchas.
bind:
  site: partner-api
  client: web
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
  - name: quota-exhausted
    when: { http_status: [200], business_code: ["10429"] }
    outcome: rate_limited
  - name: token-expired
    when: { http_status: [200], business_code: ["10401", "10403"] }
    outcome: auth_invalid
  - name: account-banned
    when: { http_status: [200], business_code: ["10900"] }
    outcome: banned
  - name: empty-page
    when: { http_status: [200], markers: [empty_list] }
    outcome: empty
  - name: target-error
    when: { http_status: { gte: 500 } }
    outcome: target_error
  - name: success
    when: { http_status: { gte: 200, lt: 300 } }
    outcome: success
```

### Example: a per-endpoint refinement on top of the default

```yaml
name: search-signal
description: The search endpoint returns 200 with an empty body under soft blocking.
extends: default-signal          # the 11 default rules run first
bind:
  site: example-site
  client: web
  endpoint_group: search
rules:
  - name: soft-block
    when:
      http_status: [200]
      uri: { prefix: /api/search }
      response_bytes: { lt: 512 }
    outcome: empty
    blame: identity
  - name: slow-degraded
    when:
      http_status: [200]
      latency_ms: { gte: 15000 }
    outcome: target_error
```

**Warning.** Because the parent's `success` rule (`http_status: {gte: 200, lt: 300}`) already
matches a 200 response, the two rules above will never be reached in this chain. Put refinements
in a parent and the broad fallbacks in the leaf, or write a standalone policy. The rule debugger
tells you which rule actually matched.

---

## Action policies

An action policy turns a classified report into *planned actions*. Unlike signal rules, **every**
matching action rule is evaluated; at the end at most one action survives per subject.

### Schema

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `mode` | enum | `enforce` | `enforce` applies the planned actions; `shadow` only records them. |
| `ban_expiry_state` | enum | `pending` | The identity state when a temporary ban ends: `pending` (must re-verify) or `active`. |
| `rules` | list | `[]` | The action rules. |
| `escalation` | list | `[]` | Ladder that lengthens repeated temporary bans. |
| `health.*` | object | see below | The health score and the actions it drives. |
| `cross_attribution.*` | object | see below | Re-attribution of risk between identities and proxies. |

#### `rules[]`

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `name` | string | `""` | Optional, unique within the policy. `lifecycle.activate`, `health.endpoint_low` and `health.quarantine` are reserved. |
| `when.outcome` | outcome or list | — | Required. The outcomes this rule reacts to; no duplicates. |
| `when.count.gte` | int | — | Optional. Require at least this many events of the outcome for the subject within the window. At least 1. |
| `when.count.within` | duration | — | The sliding window of the count condition. At least 1s. |
| `action` | enum | — | `cooldown`, `expire`, `quarantine`, `ban`. |
| `scope` | enum | — | `identity_endpoint`, `identity_site`, `identity`, `account`, `proxy_site`, `proxy`. |
| `base` | duration | — | **cooldown only.** Required, at least 1ms. The first cooldown of a streak. |
| `multiplier` | float | `1` | **cooldown only.** Finite, at least 1. |
| `max` | duration | `24h` | **cooldown only.** Cap on the computed cooldown; at least `base`. |
| `max_exponent` | int | `10` | **cooldown only.** Cap on the exponent, 0–64. Because `0` means "unset", a constant cooldown is written as `multiplier: 1`, not `max_exponent: 0`. |
| `failure_reset_after` | duration | `1h` | **cooldown only.** How long without a failure resets the streak. At least 1s. |
| `duration` | duration | — | **ban and quarantine only.** Required. At least 1s; `ban` also accepts `permanent`. Not allowed on `cooldown` or `expire`. |

#### What each action does

| Action | Effect | Allowed scopes |
| --- | --- | --- |
| `cooldown` | The subject is unavailable for that scope until the cooldown ends. Duration grows exponentially with the failure streak. | `identity_endpoint`, `identity_site`, `identity`, `account`, `proxy_site`, `proxy` |
| `quarantine` | The identity (or proxy) is taken out of rotation for a fixed duration; an identity becomes `pending` afterwards. | `identity`, `proxy` |
| `expire` | The identity is marked `expired` and stays out until its payload is updated. No duration. | `identity` |
| `ban` | The subject is banned for a duration or `permanent`. Temporary identity and account bans go through the escalation ladder. | `identity`, `account`, `proxy` |

Scopes are ordered from narrow to broad: `identity_endpoint` < `identity_site` < `identity`, and
`proxy_site` < `proxy`. A cooldown written with `scope: identity` is normalized to
`identity_site` when the policy is compiled — the identity-wide scope is reserved for the
harsher actions.

#### Evaluation, in order

1. A `pending` identity that reported `success` gets an `activate` action from the lifecycle
   (rule name `lifecycle.activate`).
2. Every rule whose `when.outcome` contains the classified outcome is considered. If it has a
   `count` condition, the counter for `(subject of the scope, outcome, window)` must reach `gte`.
3. **Blame gating.** Identity- and account-scoped rules only fire when the blame includes the
   identity. Proxy-scoped rules only fire when the blame includes the proxy *and* a proxy was
   actually used.
4. **Account degradation.** If the rule is account-scoped but the identity has no account, the
   action falls back to `identity_site` (for a cooldown) or `identity` (for the others).
5. **Health actions** are added when the blame includes the identity or the outcome is a failure
   outcome: a low-score endpoint cooldown (`health.endpoint_low`) and a quarantine
   (`health.quarantine`), under the thresholds below.
6. **One winner per subject.** The planned actions are reduced to at most one per subject
   (identity, account, proxy) by severity: `activate` 0 < `cooldown` 1 < `quarantine` 2 <
   `expire` 3 < temporary `ban` 4 < permanent `ban` 5. Ties go to the longer duration, then the
   broader scope, then the lower rule index. An activation is therefore always dropped when any
   other identity action exists.

#### Cooldown arithmetic

```text
duration = min(base × multiplier^min(streak − 1, max_exponent), max) × jitter
```

`streak` is the consecutive-failure counter of the matching scope: the identity × endpoint-group
streak for `identity_endpoint`, the proxy streak for `proxy_site` and `proxy`, and the
identity × site streak otherwise. `jitter` is drawn in `[0.8, 1.2]`. The result is rounded to
whole milliseconds and is never below 1 ms.

The streak moves in both directions: a failure increments it, a `success` **halves** it (integer
division, so 5 becomes 2), and it is reset to zero when the last failure is older than
`failure_reset_after`. The reset the hot path actually uses is the **longest**
`failure_reset_after` among the cooldown rules of the chain.

With `base: 60s, multiplier: 2, max: 30m` the ladder is 60s, 2m, 4m, 8m, 16m, 30m, 30m, …

#### Escalation

```yaml
escalation:
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
```

Evaluated whenever a **temporary identity or account ban** is planned (proxy bans are never
escalated, because ban history is tracked per identity). `bans` counts earlier bans of the
identity in the window, plus the one being applied. The matching step with the highest threshold
wins; ties go to the longer duration, and a step never *shortens* a ban. Steps must be ordered by
strictly increasing `gte` with non-decreasing durations, and `within` is at most 30 days. In an
`extends` chain, the escalation ladder comes from the leaf if it defines one, otherwise from the
nearest ancestor that does.

#### Health score

Each identity carries an exponentially weighted score per endpoint group and a global one, both
in 0–100.

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `health.alpha` | float | `0.1` | EWMA weight of the newest observation. Greater than 0, at most 1. |
| `health.baseline` | float | `70` | The score a new identity starts at and decays back towards. 0–100. |
| `health.tau` | duration | `6h` | Decay time constant: `score ← baseline + (score − baseline)·e^(−Δt/tau)`. At least 1s. |
| `health.observations` | map outcome → 0–100 | `{}` | Overrides of the observation value of an outcome. Built-in values: `success` 100, `empty` 60, `network_error` 50, `rate_limited` 30, `forbidden` 10, `captcha` 0. Other outcomes do not move the score. |
| `health.endpoint_low_score` | float | `15` | Below this endpoint score, plan a cooldown. 0–100. |
| `health.endpoint_low_min_samples` | int | `10` | Minimum endpoint samples before that applies. At least 1. |
| `health.endpoint_low_cooldown` | duration | `6h` | Duration of that cooldown. Not planned again while a cooldown at least this long is already running. At least 1s. |
| `health.quarantine_score` | float | `20` | Below this global score, quarantine the identity. 0–100. |
| `health.quarantine_min_samples` | int | `10` | Minimum global samples before that applies. At least 1. |
| `health.quarantine_duration` | duration | `24h` | Duration of the quarantine. At least 1s. |

A `success` always updates the identity score; other observed outcomes only do so when the blame
includes the identity. Proxies carry their own score per site with fixed observation values:
`success` 100, `proxy_error` 0, `network_error` 40, `rate_limited` 30, applied only when the
blame includes the proxy.

#### Cross attribution

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `cross_attribution.enabled` | bool | `true` | Whether to re-attribute risk between identity and proxy. |
| `cross_attribution.window` | duration | `10m` | Sliding window of the pairing history. At least 1s. |
| `cross_attribution.proxy_distinct_identities` | int | `3` | This many distinct identities failing on one proxy in the window blame the proxy. At least 1. |
| `cross_attribution.identity_distinct_proxies` | int | `3` | This many distinct proxies failing with one identity in the window blame the identity. At least 1. |

It runs only for a risk outcome that had both an identity and a proxy, and it rewrites the blame
before the action rules see it: if the proxy threshold is met and the identity threshold is not,
the blame becomes `proxy`; if the identity threshold is met, the identity is added to the blame
(so `proxy` becomes `both`).

### Example: a strict policy for an endpoint that bans quickly

```yaml
name: search-aggressive
description: Back off hard on rate limiting, retire an identity after three captchas a day.
bind:
  site: example-site
  client: web
  endpoint_group: search
mode: enforce
rules:
  - name: rate-limited-cooldown
    when: { outcome: rate_limited }
    action: cooldown
    scope: identity_endpoint
    base: 2m
    multiplier: 3
    max: 2h
    max_exponent: 5
    failure_reset_after: 2h
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
    when: { outcome: [proxy_error, network_error] }
    action: cooldown
    scope: proxy_site
    base: 2m
    max: 30m
escalation:
  - when: { bans: { gte: 2, within: 7d } }
    duration: 72h
  - when: { bans: { gte: 3, within: 30d } }
    duration: permanent
health:
  alpha: 0.2
  baseline: 70
  tau: 3h
  observations:
    empty: 40                     # an empty page is worse here than the default 60
  endpoint_low_score: 25
  endpoint_low_min_samples: 20
  endpoint_low_cooldown: 4h
  quarantine_score: 20
  quarantine_min_samples: 20
  quarantine_duration: 24h
ban_expiry_state: pending
cross_attribution:
  enabled: true
  window: 10m
  proxy_distinct_identities: 3
  identity_distinct_proxies: 3
```

### Example: a lenient policy layered on a shared base

```yaml
name: web-lenient
description: Softer cooldowns for browsing endpoints; inherits the shared base rules.
extends: shared-base-action
bind:
  site: example-site
  client: web
mode: enforce
rules:
  - name: empty-cooldown
    when: { outcome: empty, count: { gte: 5, within: 15m } }
    action: cooldown
    scope: identity_endpoint
    base: 90s
    multiplier: 1                 # constant, no backoff
  - name: forbidden-quarantine
    when: { outcome: forbidden, count: { gte: 2, within: 6h } }
    action: quarantine
    scope: identity
    duration: 12h
health:
  alpha: 0.05                     # react slowly
  baseline: 75
  tau: 12h
  endpoint_low_score: 10
  endpoint_low_min_samples: 30
  endpoint_low_cooldown: 2h
  quarantine_score: 15
  quarantine_min_samples: 30
  quarantine_duration: 12h
ban_expiry_state: active
```

**Note.** `mode`, `health`, `cross_attribution` and `ban_expiry_state` always come from the leaf
of an `extends` chain. Only `rules` are concatenated, and only `escalation` falls back to the
nearest ancestor.

---

## Breaker policies

A breaker protects a whole endpoint group. It watches a sliding window of reports; when the
window looks bad enough it *opens*, and every `Acquire` for that group is refused until it has
probed its way back.

![Breakers](../images/breakers.png)

### Schema

| Field | Type | Default | Meaning |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | A disabled breaker never trips. Automatic opens and the half-open state close on the next evaluation; a manual open is kept — a timed one until its duration expires, an indefinite one until you close it by hand. |
| `window` | duration | `60s` | The sliding window of reports. At least 1s. |
| `buckets` | int | `12` | How many buckets the window is split into. 1–60. Must divide the window into whole milliseconds, and each bucket must be at least 1s. |
| `min_requests` | int | `50` | Minimum reports in the window before any trip condition is considered. At least 1. |
| `trip.risk_ratio_gte` | float | `0.4` | Trip when `risk / total` reaches this. 0–1; `0` disables the condition. |
| `trip.distinct_captcha_identities_gte` | int | `10` | Trip when this many distinct identities hit a captcha in the window. At least 0; `0` disables the condition. |
| `trip.success_ratio_lte` | float | `0.2` | Trip when `success / total` falls to this or below. 0–1; `0` disables the condition. |
| `open_duration` | duration | `2m` | The first open period. At least 1s. |
| `max_open_duration` | duration | `1h` | Cap on the doubled open period. At least `open_duration`. |
| `reset_open_count_after` | duration | `30m` | Time closed after which the consecutive-open counter resets to 1. At least 1s. |
| `half_open.probe_leases_per_10s` | int | `5` | Leases issued per 10-second window while half-open. At least 1. |
| `half_open.close_min_samples` | int | `5` | Probe reports needed before the half-open decision is taken. At least 1. |
| `half_open.close_success_ratio_gte` | float | `0.8` | Probe success ratio needed to close. Greater than 0, at most 1. |
| `revert_recent_cooldowns` | enum | `endpoint` | `none`, `endpoint` or `all`. Also accepts booleans: `true` = `endpoint`, `false` = `none`. |

At least one trip condition must be non-zero while the breaker is enabled.

### The state machine

```text
            total ≥ min_requests and a trip condition holds
   closed ──────────────────────────────────────────────▶ open
     ▲                                                     │ the open period expires
     │ probe success ratio ≥ close_success_ratio_gte        ▼
     └───────────────────────── half_open ◀────────────────┘
                                   │ probe success ratio < close_success_ratio_gte
                                   └────────────────────────▶ open (period doubled)
```

- **Open period.** `min(open_duration × 2^(consecutive_opens − 1), max_open_duration)`. The
  consecutive-open counter goes back to 1 when the breaker has been closed for at least
  `reset_open_count_after`. With the defaults the ladder is 2m, 4m, 8m, 16m, 32m, 1h, 1h, …
- **Half-open.** The group issues at most `half_open.probe_leases_per_10s` leases per ten
  seconds. Probes always select with `best_health` and ignore sticky sessions, so a probe is
  never spent on a weak identity. Once `close_min_samples` probe reports have arrived, the
  breaker closes if their success ratio reaches `close_success_ratio_gte` and opens again (with
  the counter incremented and the period doubled) otherwise.
- **Window hygiene.** Buckets that started before the last close are ignored, so a breaker that
  just closed is not immediately re-tripped by the samples that tripped it.
- **Evaluation cadence.** A sweep runs every 5 seconds on every instance. Candidates are endpoint
  groups with acquire activity in the last 2 minutes plus every breaker that is not closed, and
  each group is guarded by a per-group lock so two instances never evaluate it at once.

Transitions carry a human-readable reason that names the condition and the numbers, for example
`risk_ratio 0.55 >= 0.40; captcha_identities 12 >= 10` or `probe success_ratio 0.60 < 0.80`. The
reason is stored with the breaker event, shown in the console and published on the event stream.

### Reverting cooldowns when a breaker opens

When a breaker opens automatically, the cooldowns that the site's own bad behaviour caused are
usually wrong — the identities were not at fault. `revert_recent_cooldowns` decides what to undo:

| Value | Effect |
| --- | --- |
| `none` | Nothing is reverted. |
| `endpoint` | Every identity × endpoint-group cooldown that **the system itself** applied to this endpoint group inside the window is reverted, whatever outcome caused it — an `empty` as much as a `captcha`. The identity's failure streak is put back where it was too. |
| `all` | Everything `endpoint` does, plus the identity × site cooldowns applied inside the window, so the identity becomes selectable again in every endpoint group of the site. Identity × endpoint-group cooldowns of *other* endpoint groups are not touched. |

Cooldowns you applied by hand are left alone; bans, expiry and quarantine are never touched; and a
**manual** open never reverts anything.

### Manual operation

Opening or closing a breaker by hand is not a policy change — it is an operation on the live
state, done from **Scheduling → Breakers** or the breaker admin API, and it needs
`breaker:operate` on the site.

- An open with a duration behaves like an automatic open and half-opens when the duration
  expires. The duration is at most 365 days.
- An open with no duration (or `permanent`) is indefinite and never leaves the open state on its
  own.
- Closing resumes leases immediately, without half-open probing.
- Every manual operation is written to the audit log with the reason you gave.

### Example: a fast, twitchy breaker for a fragile endpoint

```yaml
name: search-breaker
description: Short window, low sample floor, quick recovery.
bind:
  site: example-site
  client: web
  endpoint_group: search
enabled: true
window: 30s
buckets: 6                        # 5 s per bucket
min_requests: 20
trip:
  risk_ratio_gte: 0.25
  distinct_captcha_identities_gte: 5
  success_ratio_lte: 0.5
open_duration: 30s
max_open_duration: 10m
reset_open_count_after: 10m
half_open:
  probe_leases_per_10s: 3
  close_min_samples: 5
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: endpoint
```

### Example: a slow, tolerant breaker for a bulk endpoint

```yaml
name: bulk-breaker
description: Only trip on a sustained collapse; stay open long and revert nothing.
bind:
  site: partner-api
  client: partner
enabled: true
window: 10m
buckets: 20                       # 30 s per bucket
min_requests: 500
trip:
  risk_ratio_gte: 0.6
  distinct_captcha_identities_gte: 0   # this API has no captcha; disable the condition
  success_ratio_lte: 0.1
open_duration: 5m
max_open_duration: 2h
reset_open_count_after: 1h
half_open:
  probe_leases_per_10s: 1
  close_min_samples: 20
  close_success_ratio_gte: 0.9
revert_recent_cooldowns: none
```

---

## The rule debugger

The debugger replays one report against the policies that are really resolved for a site, client
and endpoint group. It touches nothing: no counters move, no cooldowns are written, no events are
published. It needs `policy:read` on the site.

Open it from **Policies → Rule debugger** (namespace-wide) or from the **Debugger** tab of a
policy.

### What you give it

| Input | Notes |
| --- | --- |
| Site and client | Required. |
| Endpoint group, or a URI to match | Choose the group directly, or give a URI and let the site's URI rules pick the group. With neither, the report's own `uri` is matched. |
| The report | `uri`, `method`, `http_status`, `business_code`, `error_kind`, `markers`, `outcome_hint`, `latency_ms`, `response_bytes`. The console can paste a real report as JSON, in the shape a node sends it. |
| Identity state | `pending`, `active`, `expired`, `banned`, `quarantined`, `disabled`, `retired`. Empty means `active`. |
| `has_account`, `has_proxy` | Whether the identity belongs to an account and whether a proxy was used — these drive the blame gating and account degradation. |
| Counters | Keyed `<subject>:<outcome>:<window>`, for example `identity:captcha:24h`, with the value **including** this report. Any counter a rule needs and you did not supply counts as 1. Up to 256 entries. |
| Ban counts | Keyed by window, for example `30d`, counting earlier bans of the identity, excluding the one being evaluated. Up to 32 entries. |
| Scores, samples and streaks | `endpoint_score`, `endpoint_samples`, `global_score`, `global_samples` (scores 0–100), `endpoint_streak`, `site_streak` (0 reuses the endpoint streak), `proxy_streak`. |
| Remaining endpoint cooldown | For example `5m`. A running cooldown suppresses a new low-score cooldown. |
| Draft policy | Optionally, the id of one signal **or** action policy whose draft (or published YAML when it has no draft) replaces the resolved policy of that kind. This is how you test an unpublished change. |

### What it gives back

| Output | Meaning |
| --- | --- |
| `outcome` | The classified outcome. |
| `blame` | `none`, `identity`, `proxy` or `both`. |
| `matched_rule_index` / `matched_rule_name` | The signal rule that won; index `-1` means no rule matched and the fallback was used. |
| `actions[]` | One entry per surviving action: `action`, `scope`, `duration` (`permanent` for a permanent ban, empty for an activation), `permanent`, `severity`, `rule_name`, `source` and `rule_index`. |
| `counters[]` | The counters the action rules read for this outcome — the exact keys you can fill in above. |
| `mode` | `enforce` or `shadow`, from the resolved action policy. |

`source` says where an action came from: `rule` (an action rule), `escalation` (an action rule
whose ban duration was raised by the ladder), `health` (`health.endpoint_low` or
`health.quarantine`) or `lifecycle` (`lifecycle.activate`).

### Reading a result

A typical loop:

1. Run the report as it came in. If the outcome is wrong, the signal policy is wrong — look at
   `matched_rule_name`, then at the rules *above* it that you expected to match.
2. If the outcome is right but nothing happens, check `blame` first. An identity-scoped rule
   simply does not fire on a report blamed on the proxy.
3. If a rule with a `count` condition does not fire, look at `counters[]` and set the counter it
   names to a realistic value.
4. If two rules both match but only one action comes back, that is the one-winner-per-subject
   reduction. The `severity` column shows why.

---

## Testing a policy safely: shadow mode

Set `mode: shadow` on an action policy and publish it. From then on the policy is evaluated
exactly as before — rules match, cooldowns are computed, escalation runs — but **nothing is
applied**: no cooldown is written, no identity is banned, no notification is sent.

What you still get:

- A state event per planned action with `shadow = true`, visible in the identity history and in
  the state-event data. Cooldown events are recorded only when the server's
  `SPINNERET_RECORD_COOLDOWN_EVENTS` is on, which it is by default.
- The `spinneret_actions_total` counter incremented with `mode="shadow"`, so you can graph
  "what would this policy have done" next to "what the live policy did".

The usual sequence for a risky change:

1. Copy the live policy to a new name, make the change, set `mode: shadow`, publish and bind it
   at the level you want to test.
2. Leave it for a representative period and compare `spinneret_actions_total{mode="shadow"}` with
   the enforcing policy's counters, and read the shadow state events for a handful of identities.
3. Flip `mode: enforce` and publish. If it goes wrong, roll back to the previous version — the
   old YAML is still there.

For a single report you do not need shadow mode at all: the rule debugger with `draft_policy_id`
set gives you the answer without publishing anything.

Shadow mode only exists for action policies. To test a signal policy safely, use the debugger; to
test a breaker policy safely, widen the thresholds rather than shadowing them.

---

## Troubleshooting: my policy does not seem to apply

Work down this list; the causes are roughly in order of how often they are the real one.

| Symptom | Cause | Fix |
| --- | --- | --- |
| Nothing changed after editing | The change is still a draft. | Publish it. The editor shows **Draft** next to the policy; the **Versions** tab shows what is actually published. |
| The policy is published but a different one runs | A more specific binding wins. | **Policies → Resolve** with the exact site, client and endpoint group. It names the winning policy, its version and its level. |
| Resolve shows `builtin` even though you bound a policy | The stored version no longer parses, or its `extends` chain no longer resolves; the server fell back to the built-in default and logged a warning. | Validate the YAML of that version, republish a good one, or fix the parent policy. |
| An action rule never fires | Blame gating. Identity-scoped rules need the blame to include the identity; proxy-scoped rules need it to include the proxy **and** a proxy to have been used. | Check `blame` in the debugger. Set `blame:` explicitly on the signal rule if the default attribution is wrong for that site. |
| An action rule with `count` never fires | The counter never reaches `gte` within `within`, or the counter is keyed on a different subject than you think — it is the subject of the rule's **declared** scope. | Read `counters[]` in the debugger; it lists the exact keys the rules read. |
| Two rules match, only one action appears | One winner per subject, by severity. | Expected. Give the weaker rule a different subject, or accept it. |
| A cooldown lands on `identity_site` although you wrote `scope: identity` | A cooldown with scope `identity` is normalized to `identity_site` at compile time. | Use `identity_endpoint` or `identity_site` explicitly; use `identity` for `ban`, `expire` and `quarantine`. |
| A signal rule is never reached | An earlier rule already matched — including rules inherited through `extends`, which run **first**. | The debugger reports `matched_rule_name`. Reorder, or narrow the earlier rule. |
| A `http_status` range never matches | A range only matches a report that carried a status. A missing status is matched with the list form `[0]`. | Use `http_status: [0]` for transport failures, or add an `error_kind` condition. |
| Actions are planned but nothing happens to identities | The action policy is in `mode: shadow`. | Check `mode` in the debugger result or the resolved YAML. Publish with `mode: enforce`. |
| A breaker never trips | `enabled: false`, all three trip conditions are `0`, or `min_requests` is never reached in the window. | Check the resolved YAML. Compare `min_requests` with the real traffic of that group over `window`. |
| A breaker trips constantly | `window` is too short, `min_requests` too low, or the thresholds too tight for a genuinely noisy endpoint. | Widen the window, raise `min_requests`, or raise `risk_ratio_gte`. |
| A breaker will not close | Half-open probes keep failing, or too few probes arrive to reach `close_min_samples`. | Look at the probe metrics on the Breakers page. Lower `close_min_samples`, or raise `probe_leases_per_10s` so a decision is reachable. |
| Changes are slow to take effect on one instance | Catalog invalidation is debounced by 100 ms across instances, with a full reload every 60 seconds as a safety net. | Wait a few seconds. If it persists beyond a minute, check the event bus and the server logs. |
| Publishing is refused with a conflict | Someone else published in the meantime and you sent `expected_version`. | Reload the policy, re-apply your change, publish again. |
| Deleting a policy is refused | Published policies still `extend` it, or it is a `default-<kind>` policy bound at namespace level. | Repoint or delete the children first; bind another policy at namespace level before deleting a default. |

---

## Next

- [Concepts](./04-concepts.md) — how a request flows from `Acquire` to `Report` and back.
- [Identities and accounts](./06-identities.md) — what a cooldown, ban, quarantine or expiry does to an identity.
- [Proxies](./07-proxies.md) — the proxy modes a rotation policy selects between.
- [Observability and alerting](./12-observability.md) — the breaker page, the cooldown heatmap, risk events and the metrics named here.
- [Node API reference](./13-node-api.md) — the `Report` fields the signal conditions match on.
- [Troubleshooting](./18-troubleshooting.md) — symptoms that are not policy problems.
