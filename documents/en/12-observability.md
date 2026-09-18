# Observability and alerting

**How to see what Spinneret is doing: the console dashboards, the request explorer, the Prometheus metrics, the health endpoints, and the notification channels that tell you about a problem before a customer does.**

[中文](../zh/12-observability.md)

---

## Contents

- [Where the numbers come from](#where-the-numbers-come-from)
- [Overview](#overview)
- [Cooldown heatmap](#cooldown-heatmap)
- [Request explorer](#request-explorer)
- [Risk events](#risk-events)
- [Breakers](#breakers)
- [Notifications: channels, rules and history](#notifications-channels-rules-and-history)
- [Prometheus metrics](#prometheus-metrics)
- [Starter alert rules](#starter-alert-rules)
- [The console event stream](#the-console-event-stream)
- [Health endpoints](#health-endpoints)
- [What to look at first](#what-to-look-at-first)
- [Next](#next)

---

## Where the numbers come from

Spinneret keeps its history in three stores — Valkey/Redis, PostgreSQL and ClickHouse — and every
console page reads exactly one or two of the tables below. Knowing which is which explains why some
pages are instant and some have query limits.

| Store | What lives there | Read by | Retention |
| --- | --- | --- | --- |
| Valkey / Redis (hot state) | ready queues, health scores, cooldowns, bans, breaker state, report stream backlog | overview live counts, heatmap, breakers | working set only, rebuildable |
| PostgreSQL minute aggregates | `acquire_stats_minutely`, `outcome_stats_minutely`, `node_stats_minutely` | overview rates and ratios, trend charts, node table | `SPINNERET_RETENTION_MINUTE_STATS` (default 30 d) |
| PostgreSQL hour aggregates | `identity_stats_hourly` — per identity, endpoint group and outcome | nothing yet: the table is written but no console page or RPC reads it | `SPINNERET_RETENTION_HOUR_STATS` (default 180 d) |
| PostgreSQL `risk_events` | one row per non-success report | risk events page | `SPINNERET_RETENTION_RISK_EVENTS` (default 30 d) |
| ClickHouse `report_events` | one row per processed report, success included | request explorer | `SPINNERET_CLICKHOUSE_TTL_DAYS` (default 90) |
| ClickHouse `lease_events` | one row per lease lifecycle event | nothing yet: available for ad-hoc SQL | `SPINNERET_CLICKHOUSE_TTL_DAYS` (default 90) |

`identity_stats_hourly` and `lease_events` are honest write-only tables today: they are kept
current so that per-identity and per-lease history is there when a page needs it, but shortening
`SPINNERET_RETENTION_HOUR_STATS` costs the console nothing right now.

The console pages above are served by `DashboardService` (`dashboard:read`), except the breakers
page, which is `BreakerAdminService` (`breaker:read`), and notifications, which is
`NotificationAdminService` (`notify:read`). All three only ever return data for sites the caller can
read. ClickHouse is optional: without `SPINNERET_CLICKHOUSE_URL` the request explorer is disabled
and every other page keeps working.

Prometheus metrics are a separate, independent path: they describe the *server process*, not the
namespace, and they are the right place to alert from. See
[Configuration reference](./03-configuration.md) for all of the variables named above.

---

## Overview

Console path `/`, navigation entry **Overview**. Needs `dashboard:read`; the open-breaker card
additionally needs `breaker:read`. Every query on the page refreshes every 10 s.

![Overview](../images/overview.png)

### The window selector

The selector in the page header offers **1m**, **5m**, **15m** and **1h** and defaults to 5m. It
rescales only the rates and ratios of the tiles and the site cards. Rates cover the *complete*
minutes before now — the current, partially written minute is excluded, so a 1m window is always a
finished minute and never a half-empty one. The two trend charts and the node table always cover
the last hour regardless of the selector.

### The tiles

| Tile | Value | Hint under the value |
| --- | --- | --- |
| Available identities | identities leasable right now, summed over sites | — |
| Acquire QPS | `Acquire` calls per second over the window | acquire failure ratio |
| Report QPS | reports per second over the window | pending reports (the stream backlog) |
| Success ratio | share of reports classified `success` | unknown ratio |
| Risk ratio | share of reports with a risk outcome | — |
| Open breakers | endpoint groups whose breaker is open | number of half-open breakers |

Details that matter when you read them:

- **Available identities** is not "identities that exist". Per client, the server takes the largest
  ready count over that client's endpoint groups, then sums over clients. An identity that is
  cooling down in one endpoint group but free in another still counts once.
- **Risk ratio** counts the four risk outcomes: `rate_limited`, `captcha`, `forbidden`, `banned`.
  It deliberately excludes `network_error`, `proxy_error` and `target_error`, which usually mean
  transport or target trouble rather than detection.
- **Acquire failure ratio** counts the acquire results `exhausted`, `circuit_open`, `site_paused`
  and `no_proxy` against all acquire attempts. A result of `error` is not counted as a failure here.
- **Pending reports** is cluster-wide, not per namespace: it is the sum over all report stream
  shards of the worker consumer group's pending plus lag (or the stream length when no worker has
  consumed the shard yet).
- Tiles change colour: success ratio turns amber below 80 % (only when reports are flowing), risk
  ratio turns red at 10 % or more, open breakers turn red above zero.

### Trend charts

Two charts, both over the last hour, both from the minute aggregates:

- **Acquire rate (last hour)** — the `acquire_rate` metric, acquires per second.
- **Success and risk ratio (last hour)** — the `success_ratio` and `risk_ratio` metrics, 0–1.

The underlying `GetTimeSeries` RPC is more capable than the overview uses it. It accepts the metrics
`acquire_rate`, `acquire_results`, `outcomes`, `success_ratio`, `risk_ratio` and `latency_avg`;
the steps `1m`, `5m`, `15m`, `1h`, `6h`, `1d` or an empty step to pick one automatically; a range of
at most 31 days defaulting to the last hour; and optional `site`, `client` (requires a site) and
`endpoint_group_id` filters. A step that would produce more than 720 buckets is raised to the next
supported one; if even `1d` does not fit, the call fails with `invalid_argument`. Buckets with no
data are returned as 0, not omitted.

### Per-site cards

One card per readable site, ordered by site name. Each card carries:

- the display name with the site name underneath, plus badges for **Paused**, open breakers and
  half-open breakers;
- **Available** — leasable identities of this site — next to **Acquire** and **Report** rates;
- a four-cell strip: **Success**, **Risk**, **Unknown**, **Acquire failures**, with the same colour
  thresholds as the tiles (acquire failures turn amber from 5 %);
- a stacked bar of identities by lifecycle state — active, pending, expired, quarantined, banned,
  disabled, retired — with counts in the legend;
- a warning line when endpoint groups of the site are below their low watermark.

The state bar is the fastest way to see *which* site is degrading: a growing expired segment means
payloads need refreshing, a growing banned segment means the target is pushing back.

### The two cards below

**Open breakers** lists up to 20 breakers in state `open` or `half_open` with site, endpoint group,
state, "open until" and reason. **Low watermark warnings** lists every endpoint group whose
available identities are below its configured low watermark, with client, available count and
threshold. Both are empty-state cards when there is nothing to show.

### Nodes (last hour)

Per-node counters from `node_stats_minutely`, busiest first, at most 1000 nodes: **Acquires**,
**Reports**, **Abandoned** (leases that expired without any report), **Rejected** and
**Unreported** (`abandoned / acquires`). The node name comes from the `X-Spinneret-Node` header a
node sends; reports without that header are grouped under `_`.

A high unreported ratio for one node is the signal that that node crashes, is killed, or forgets to
call `Report`. It is namespace-wide data, so the page requires namespace-wide dashboard access.

---

## Cooldown heatmap

Console path `/heatmap`, navigation entry **Heatmap**. Needs `dashboard:read`. Refreshes every 10 s.

![Cooldown heatmap](../images/heatmap.png)

The heatmap is one site *and* one client at a time: rows are identities, columns are that client's
endpoint groups sorted by name, and each cell is the hot state of that identity in that group.

| Control | Values |
| --- | --- |
| Client | one client type of the site (required) |
| Metric | **Cooldown remaining** or **Score** — both values are always present in the data; the metric only chooses what colours the cell |
| States | any of `pending`, `active`, `expired`, `banned`, `quarantined`, `disabled`, `retired`. The console starts at active + pending; its "All except retired" is also what the server uses when the request names no state |
| Identities per page | 100, 250 or 500 (the server default is 100 and its cap is 500) |
| View | **Chart** or **Table** — the table view is the same data as text, for screen readers and for copying |

Each cell carries a decayed health score (0–100), the remaining cooldown in milliseconds, and
whether the identity can be leased in that group right now. The remaining cooldown is the maximum of
the endpoint-level cooldown, the identity-wide cooldown, the account cooldown and any ban; a
permanent ban is reported as the largest possible int64 and shown as **Banned permanently** rather
than as a duration.

The matrix is sparse. The server omits a cell when the identity has no health entry in the group, no
cooldown and no ban, *and* its availability matches what its lifecycle state implies (`pending` and
`active` leasable, everything else not). Omitted cells are drawn with the group's baseline score and
labelled **No hot state (baseline)** — that is a healthy identity nobody has touched yet, not
missing data.

Row labels are the account reference when the identity has one, otherwise the region, otherwise the
last 8 characters of the identity ID. Clicking a cell opens that identity's detail page. Paging is
by identity ID, and the total count of matching identities is shown above the grid.

The heatmap reads Redis in one pipelined round trip, so it stays fast with 500 rows — but it shows
*now*, never history. For history use the request explorer.

---

## Request explorer

Console path `/requests`, navigation entry **Requests**. Needs `dashboard:read`. The first page
refreshes every 5 s while auto refresh is on; it pauses while the detail sheet is open.

![Request explorer](../images/requests.png)

### What is stored

Every processed report — success included — is written to the ClickHouse table `report_events`, one
row per report, with these columns:

| Group | Columns |
| --- | --- |
| Times | `event_time` (the request finished), `received_at` (the server accepted the report), `started_at` |
| Scope | `tenant_id`, `namespace_id`, `site_id`, `site`, `client`, `endpoint_group` |
| Subjects | `identity_id`, `identity_type`, `proxy_id`, `lease_id`, `report_id`, `node`, `token_id` |
| Request | `uri`, `method`, `http_status` (0 = no response), `business_code`, `error_kind`, `markers` |
| Classification | `outcome`, `outcome_hint` (what the node suggested), `blame` (`none`/`identity`/`proxy`/`both`), `rule` (the signal rule that matched) |
| Cost | `latency_ms`, `response_bytes` |
| Flags | `suppressed`, `late`, `probe` |

The table is a MergeTree partitioned by day on `event_time`, ordered by
`(namespace_id, site, endpoint_group, event_time)`, with bloom-filter skipping indexes on
`identity_id`, `proxy_id`, `lease_id` and `report_id`. That ordering is why a query filtered by
site and endpoint group over a narrow window is cheap, and why a query filtered only by a node name
over seven days is not.

Rows expire by `TTL event_time + SPINNERET_CLICKHOUSE_TTL_DAYS days` (default 90) with
`ttl_only_drop_parts = 1`, so expiry drops whole day partitions instead of rewriting parts.

### The second ClickHouse table: lease_events

`report_events` is not the only managed table. Spinneret also creates and writes `lease_events`, one
row per lease lifecycle event, with the same scope and subject columns (`tenant_id`, `namespace_id`,
`site_id`, `site`, `client`, `endpoint_group`, `identity_id`, `proxy_id`, `lease_id`, `node`,
`token_id`) plus `event`, `result`, `duration_us`, `probe` and `sticky`. It is partitioned by day on
`event_time`, ordered by `(namespace_id, site, endpoint_group, event_time)`, carries bloom-filter
indexes on `identity_id` and `lease_id`, and expires under the same
`SPINNERET_CLICKHOUSE_TTL_DAYS`.

No console page and no RPC reads it yet. It matters for two reasons: it is part of your ClickHouse
disk budget, and it is the only place where lease durations and sticky/probe decisions are queryable
after the fact — with plain SQL against the ClickHouse you already run.

### Filters

All filters are mirrored in the URL, so a view is a shareable link.

| Filter | Notes |
| --- | --- |
| Time range | presets **Last 15 minutes**, **Last hour**, **Last 6 hours**, **Last 24 hours**, **Last 7 days**, or a custom range. Default: last hour. Maximum span: 7 days |
| Site | one site name |
| Endpoint group | requires a site; the group must belong to that site and client |
| Outcomes | any subset of the twelve outcomes, including `success` |
| Identity ID / Proxy ID / Node | exact match |
| HTTP status | exact; `0` matches "no response"; range 0–999 |
| Min latency (ms) | keeps events with at least this latency |

`Lease ID` and `Report ID` are also accepted by the RPC (`QueryRequestEventsRequest.lease_id`,
`report_id`) — useful when you have a lease from a node log and want the report it produced.

Paging is keyset on `(event_time, report_id, lease_id)` descending, 50 rows per page by default and
at most 500. A preset range is anchored to the moment the first page was requested, so every page of
one query covers the same window instead of sliding while you page.

### The summary strip

Setting `include_summary` — which the console does for the first page only — adds an aggregate over
**every matching event**, not just the page: the total, counts per outcome, the average latency and
the 50th, 95th and 99th latency percentiles (approximate quantiles). The console keeps that strip
visible while you page deeper, and shows the outcome distribution as a donut.

### The detail panel

Clicking a row opens a sheet with the full event: site and endpoint group, identity (with a link to
the identity page) and identity type, proxy, node, token, lease and report IDs, the URI and method,
HTTP status, business code, error kind, markers, outcome and outcome hint, blame, matching rule,
latency, response size, started/finished/received times, and the three flags:

- **Suppressed** — health updates and actions were skipped because the breaker was open;
- **Late** — the report arrived after the lease had already ended;
- **Probe** — the lease was a half-open breaker probe.

### Query limits, and how a too-wide query fails

One console query is deliberately boxed in so that it cannot starve the report writers. Every
explorer query runs with `max_execution_time` equal to the server's ClickHouse timeout (30 s),
`max_memory_usage` of 512 MiB, `max_threads = 2` and `optimize_read_in_order = 1`.

When a query hits one of those guards, the failure is translated into an actionable client error
instead of an internal one, carrying a `Spinneret-Reason` header:

| Situation | Code | Reason | Message |
| --- | --- | --- | --- |
| memory limit, too many rows or bytes | `resource_exhausted` | `query_too_large` | "the analytics query needs more resources than ClickHouse allows: narrow the time range or add filters" |
| ClickHouse or client timeout, cancelled query | `deadline_exceeded` | `query_timeout` | "the analytics query timed out: narrow the time range or add filters" |

Both mean the same thing in practice: add a site or endpoint-group filter, or shorten the range.
Any other ClickHouse error stays an internal error and is logged with its cause.

### When ClickHouse is not configured

`QueryRequestEvents` fails with `unavailable` and reason `failed_precondition`, and the console
replaces the table with an explanation, the variable to set —
`SPINNERET_CLICKHOUSE_URL=clickhouse://user:password@clickhouse:9000/spinneret` — and a link to the
risk events page, which still works.

---

## Risk events

Console path `/risk-events`, navigation entry **Risk Events**. Needs `dashboard:read`. The first
page refreshes every 5 s while auto refresh is on.

Risk events are the PostgreSQL half of the same story: every report whose outcome is *not*
`success` gets a row in `risk_events`, independent of ClickHouse. If you run without ClickHouse,
this is your request history.

| Property | Value |
| --- | --- |
| Default range | last 24 hours |
| Maximum range | 31 days |
| Page size | 50 by default, 500 maximum |
| Paging | keyset on `(created_at, id)` descending |
| Retention | `SPINNERET_RETENTION_RISK_EVENTS`, default 30 days, enforced by dropping day partitions |
| Filters | site, endpoint group (requires a site), one non-success outcome, identity ID, proxy ID, node, time range |

Columns: time, site, endpoint group, outcome, blame, rule, HTTP status, business code, error kind,
markers, latency, identity, proxy, node, lease ID, report ID. Expanding a row adds the request
timings, the response size, the client, the event ID (`rsk_…`), the token and the endpoint group ID.

Note the asymmetry: the outcome filter here rejects `success`, because a successful report is by
definition not a risk event.

---

## Breakers

Console path `/breakers`, navigation entry **Breakers**. Reading needs `breaker:read`. Refreshes
every 5 s.

![Breakers](../images/breakers.png)

Three tabs:

- **Breakers** — one row per endpoint group: state (`closed`, `half_open`, `open`), open until,
  consecutive opens, the **Window** column (`N reports · N ok · N risk`), probe progress
  (`N/N ok · N issued`), the reason, the last open/close times and the policy in effect
  (or **Built-in default**). Filters: site, client, states, endpoint group.
- **Site switches** — the per-site pause switch, with clients, endpoint groups, state and pause
  reason.
- **History** — breaker transitions with time, endpoint group, transition, trigger (`Automatic`,
  `Manual`, `Probe`, `Site switch`), reason, open until, actor and the metrics at transition time. Ranges:
  last hour, 6 hours, 24 hours, 7 days, 30 days, all time.

For observability purposes the useful reflexes are: a breaker in `open` means every `Acquire` for
that group fails with `circuit_open`, and reports for leases already handed out arrive with the
`suppressed` flag set; a breaker flapping between `half_open` and `open` shows up in the history tab
and as repeated `breaker_reopened` alerts. The mechanics, the policy fields and the manual
open/close operations are documented in [Policies](./08-policies.md).

---

## Notifications: channels, rules and history

Console path `/notifications`, navigation entry **Notifications**. Reading needs `notify:read`,
creating and editing `notify:write`. Refreshes every 5 s.

Channels belong to a **tenant**, optionally to one namespace, and optionally to a set of sites
inside that namespace.

### Creating a channel

**New channel** opens the **New notification channel** form. Creating or editing a channel needs
`notify:write`.

| Field | What to put there |
| --- | --- |
| **Scope** | tenant-wide, or one namespace. Fixed after creation — *the scope cannot be changed* |
| **Kind** | `webhook`, `feishu`, `dingtalk`, `wecom` or `telegram`. Fixed after creation too |
| **Name** | 1–64 characters, no leading or trailing spaces |
| the kind's settings | the fields of the table below; the form shows only the selected kind's |
| **Event types** | at least one alert kind — an empty list is rejected |
| **Sites** | optional, namespace-scoped channels only, at most 500 sites. Empty = every site |
| **Min severity** | `info`, `warning` or `critical`. Empty means `warning` |
| **Enabled** | a disabled channel receives nothing except a test alert |

Scope and kind are immutable because both decide how the stored settings are interpreted; to change
either, create a new channel and delete the old one. Everything else — name, settings, event types,
sites, minimum severity, enabled — can be edited later.

Press **Send test alert** on the new channel immediately: it is the only way to find out that the
URL, token and signature actually work.

### Channel kinds

Five kinds are implemented. Each accepts only its own settings fields; anything else is rejected.

| Kind | Settings | Delivery |
| --- | --- | --- |
| `webhook` | `url` (required), `secret`, `headers` | JSON `POST` of the alert |
| `feishu` | `webhook_url` (required), `secret` | text message; the secret adds `timestamp` + `sign` to the payload |
| `dingtalk` | `webhook_url` (required), `secret` | markdown message; the secret adds `timestamp` and `sign` query parameters |
| `wecom` | `webhook_url` (required) | markdown message |
| `telegram` | `bot_token` (required), `chat_id` (required), `api_base` | `sendMessage` with HTML text; `api_base` defaults to `https://api.telegram.org` |

Validation worth knowing: URLs must be absolute `http`/`https`; a bot token must match
`<digits>:<secret>`; at most 20 extra headers, each with a valid header name, and
`Host`, `Content-Length`, `Content-Type`, `Transfer-Encoding`, `Connection`, `Te`, `Upgrade`,
`Trailer`, `X-Spinneret-Timestamp` and `X-Spinneret-Signature` cannot be overridden.

Settings are sealed with the vault cipher before storage and are **always** returned masked: URLs
keep scheme and host and mask the rest (a webhook URL often carries a token in its query), and
secrets, bot tokens and credential-looking headers (anything whose name contains `authorization`,
`token`, `key`, `secret`, `password`, `cookie` or `signature`) come back as `••••` plus the last
four characters. Sending a masked value back unchanged keeps the stored secret — except for
credentials that are transmitted verbatim to the destination (webhook credential headers, the
Telegram bot token), which are only restored while the destination URL is unchanged. Change the URL
and you must supply those in full again; otherwise an editor who cannot read a secret could redirect
it to a server of their choosing.

### Webhook payload and signature

A `webhook` channel receives one `POST` per alert with
`Content-Type: application/json; charset=utf-8`, `User-Agent: Spinneret-Notify/<version>`, the
configured headers, and this body:

```json
{
  "id": "alt_...",
  "kind": "breaker_opened",
  "severity": "critical",
  "title": "...",
  "message": "...",
  "tenant": "acme",
  "namespace": "prod",
  "site": "example-site",
  "details": { "site": "example-site", "endpoint_group": "search" },
  "created_at": "2026-01-01T12:00:00Z"
}
```

When `secret` is set, two headers are added:

```text
X-Spinneret-Timestamp: 1767268800
X-Spinneret-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + raw body)>
```

Recompute the HMAC over the raw request body to authenticate the delivery. Any non-2xx response is
a failure; redirects are not followed and count as failures too. Statuses in 300–499 other than 408
and 429 are treated as permanent and are not retried.

### Routing: which channel gets which alert

A channel receives an alert of its tenant when **all** of these hold: the channel is enabled, it
subscribes to the alert's kind, the alert's severity reaches the channel's minimum severity
(`info` < `warning` < `critical`; the default minimum is `warning`), and its scope covers the alert:

- a tenant-wide channel receives every alert of the tenant;
- a namespace channel receives the alerts of its namespace **and** tenant-level alerts such as
  `report_backlog`;
- a site-restricted channel receives the alerts of its sites plus alerts that concern no site.

### Alert kinds and what triggers them

| Kind | Severity | Trigger | Scope |
| --- | --- | --- | --- |
| `breaker_opened` | critical | any transition into `open` that did not come from `half_open` | site |
| `breaker_reopened` | critical | `half_open` → `open` (probes failed) | site |
| `breaker_closed` | info | `open` or `half_open` → `closed` | site |
| `identity_low_watermark` | warning | an endpoint group's identities available now are below its low watermark | site |
| `proxy_low_watermark` | warning | a namespace has ≥ 5 proxies and fewer than 20 % of its active+dead proxies are active | namespace |
| `ban_spike` | warning | bans on a site in the last 5 min exceed `max(10, 3 × the average 5-minute count of the previous hour)` | site |
| `report_backlog` | critical | pending + undelivered report stream entries across all shards exceed 50 000 | tenant |
| `unknown_ratio_high` | warning | over the last 5 min, with ≥ 100 reports, more than 20 % of a site's reports were classified `unknown` | site |
| `client_error_spike` | warning | same window and minimum, more than 10 % were `client_error` | site |
| `identity_expired` | info | an identity transitioned to `expired` | site |
| `secret_expiring` | warning | a secret expires within 7 days (and for 7 days after it expired) | namespace |
| `test` | info | the **Send test alert** button | channel's scope |

The three breaker kinds and `identity_expired` are driven by the event bus, so they fire within
seconds. The rest come from the `notify_alert_evaluation` leader job, which runs every 30 s with a
25 s timeout: a failing rule does not stop the others, and each rule stores at most 500 alerts per
run, deferring the rest to later runs.

Two details of `identity_expired` are worth knowing: its bus queue is bounded, so the evaluation job
also scans `state_events` for transitions to `expired` and emits the alerts the queue dropped, using
the same de-duplication key within a one-hour window.

### De-duplication, delivery and retries

Every alert with a de-duplication key claims a Redis marker for its window before it is stored:
10 minutes by default, 24 hours for `secret_expiring`, 1 hour for `identity_expired`. A repeat inside
the window is silently suppressed — that is why a breaker that flaps does not produce a hundred chat
messages.

Delivery is asynchronous: 4 workers by default, a 10 000-entry queue, 3 attempts per delivery with
exponential backoff from 2 s, and a 10 s HTTP timeout per request. Two safety valves shape what you
see in the history: a channel whose last delivery failed within the last 5 minutes gets a single
attempt per delivery until one succeeds (so one dead endpoint cannot monopolise the workers), and a
delivery that does not fit in the queue is recorded immediately as failed with the error
`delivery queue full`.

Delivery results are stored twice: appended to the alert's `deliveries` array as
`{"channel_id","channel_name","ok","error","attempts","at"}`, and summarised on the channel as
`last_delivery_at` and `last_delivery_status` (`ok` or an error summary). Provider error messages are
stored with credentials redacted and never contain the target URL.

### The test button

**Send test alert** stores a real `test` alert in the channel's tenant and namespace and delivers it
synchronously through that channel with **one** attempt, ignoring the channel's event-type filter,
site restriction, minimum severity and even its enabled flag. The console reports success or the
error inline, the attempt is appended to the alert history like any other delivery, and the action is
written to the audit log. Use it to prove that a URL, token and signature work; it tells you nothing
about whether the channel's filters would have matched a real alert.

### Alert history

The **Alert history** tab lists fired alerts newest first, with time, severity, kind, title, scope,
and a `N/M delivered` badge. Expanding a row shows the message, the kind-specific details and one
line per delivery attempt with its result and error. Filters: namespace, site (requires a namespace),
kind, severity and a range of last hour / 24 hours / 7 days / 30 days / all time; 50 rows per page,
500 maximum.

Alert events are purged after **90 days** by the `partition_manager` leader job, in batches of 5000.
Deleting a channel does not delete its delivery history.

---

## Prometheus metrics

Every metric is prefixed `spinneret_`. They are exposed in the Prometheus text format at
`GET /metrics`, unauthenticated, on the main HTTP listener — unless `SPINNERET_METRICS_ADDR` is set,
in which case `/metrics` moves to that address only and is no longer served on the API listener.
Metrics are per instance, so sum across instances. Label values are normalised: an empty value
becomes `_`, invalid UTF-8 is replaced and values are truncated to 128 bytes.

Worker-only instances (`SPINNERET_ROLE=worker`) serve health and metrics and nothing else, which is
exactly what you want to scrape them for.

### The hot path

| Metric | Type | Labels | Alert on |
| --- | --- | --- | --- |
| `spinneret_acquire_total` | counter | `site`, `group`, `result` (`ok`, `exhausted`, `circuit_open`, `site_paused`, `no_proxy`, `overloaded`, `error`) | rising share of non-`ok` results — but read `overloaded` separately (see below) |
| `spinneret_acquire_duration_seconds` | histogram | `site` | p99 above a few milliseconds |
| `spinneret_acquire_script_seconds` | histogram | — | p99 above a few milliseconds: this is the `acquire.lua` round trip alone. It shares its buckets with `spinneret_acquire_duration_seconds`, so the gap between the two is the wait ladder plus rendering |
| `spinneret_acquire_admission_total` | counter | `result` (`immediate`, `queued`, `shed_no_wait`, `shed_queue_full`, `shed_timeout`, `shed_canceled`) | sustained `shed.*` — one sample per *attempt*, so the shed ratio is `rate(…{result=~"shed.*"}[1m]) / rate(…[1m])` over all labels |
| `spinneret_acquire_admission_wait_seconds` | histogram | — | p99 approaching 50 ms: attempts are spending their whole wait budget queueing for a permit |
| `spinneret_acquire_inflight` | gauge | — | `sum()` across replicas is the fleet-wide acquire concurrency at Redis |
| `spinneret_acquire_queued` | gauge | — | rising: callers that pass `wait_ms > 0` are being parked. It stays at zero for the SDK default of `wait_ms = 0`, which is never parked |
| `spinneret_acquire_inflight_limit` | gauge | — | `sum(spinneret_acquire_inflight_limit) > SPINNERET_ACQUIRE_FLEET_INFLIGHT` (64 by default): past 16 replicas the division truncates to the per-instance floor of 4, so the sum overtakes the fleet budget (16 × 4 = 64, then 17 × 4 = 68). Shard Redis or lower `SPINNERET_ACQUIRE_FLEET_INFLIGHT` |
| `spinneret_acquire_peers` | gauge | — | mismatch with the number of scraped API-role instances (`count(up{job="spinneret"})` in the bundled Compose stack, whose `spinneret` job discovers only the API replicas): that mismatch is the only signal that distinguishes "one replica" from "the division is broken" |
| `spinneret_acquire_peer_beat_age_seconds` | gauge | — | growing beyond one beat interval (2 s): the peer count is frozen |
| `spinneret_acquire_peer_beat_failures_total` | counter | — | any sustained rate: while beats fail the peer count is frozen, which narrows the limit but never widens it |
| `spinneret_report_ingest_total` | counter | `result` (`accepted`, `duplicated`, `rejected`) | rising `rejected` |
| `spinneret_report_total` | counter | `site`, `group`, `outcome` (the twelve outcomes) | risk and `unknown` share |
| `spinneret_report_lag_seconds` | histogram | — | p99 growing: workers are behind |
| `spinneret_report_process_duration_seconds` | histogram | — | p99 growing: per-report work got expensive |
| `spinneret_lease_reaped_total` | counter | `site`, `kind` (`expired`, `abandoned`) | `abandoned` rate: nodes not reporting |

**The five acquire admission series exist only when admission control is on.** With
`SPINNERET_ACQUIRE_FLEET_INFLIGHT=0` no gate is constructed, so `spinneret_acquire_inflight`,
`_queued`, `_inflight_limit`, `_peers` and the two `_peer_beat_*` series are absent and
`spinneret_acquire_admission_total` exports no samples. `spinneret_acquire_peers` is also absent when
`SPINNERET_ACQUIRE_MAX_INFLIGHT` pins the limit, because then there is nothing to divide: an absent
series reads as "not applicable", a hardcoded `1` would read as a broken registry.

**`exhausted` is no longer the overload signal.** Before admission control,
`spinneret_acquire_total{result="exhausted"}` was the practical collapse detector. It is not any
more: `exhausted` now means the identity pool is genuinely empty (add identities, widen the rotation
policy) and `overloaded` means this instance was at its acquire concurrency limit (add capacity, or
offer less load). Alert on the two separately — see
[Performance and tuning](./17-performance.md) and [Operations](./16-operations.md).

### State and supply

| Metric | Type | Labels | Alert on |
| --- | --- | --- | --- |
| `spinneret_breaker_state` | gauge | `site`, `group` | value 2 (`open`); 1 is `half_open`, 0 is `closed` |
| `spinneret_breaker_transitions_total` | counter | `site`, `group`, `to` | transition rate (flapping) |
| `spinneret_actions_total` | counter | `site`, `action` (`cooldown`, `expire`, `quarantine`, `ban`, `activate`), `scope` (`identity_endpoint`, `identity_site`, `identity`, `account`, `proxy_site`, `proxy`), `mode` (`enforce`, `shadow`) | `ban` rate; comparing `shadow` with `enforce` when testing a policy |
| `spinneret_proxies` | gauge | `site`, `state` (`active`, `disabled`, `dead`, `banned`, `quarantined`, `retired`) | `active` falling — aggregate with `max`, see the note below |
| `spinneret_identities` | gauge | `site`, `type`, `state` | reserved: nothing sets it yet, so it exports no series — use the overview API for identity counts |
| `spinneret_identities_available` | gauge | `site`, `group` | reserved, as above |

**Note.** Proxies are counted per *namespace*, and the count of a namespace is then written once for
every site of that namespace. So `sum by (state) (spinneret_proxies)` multiplies the real number of
proxies by the number of sites. Use `max by (state) (spinneret_proxies)`, or pin the query to a
single `site`.

### Plumbing

| Metric | Type | Labels | Alert on |
| --- | --- | --- | --- |
| `spinneret_stream_pending` | gauge | `shard` | sum over shards: the report backlog |
| `spinneret_stream_owned_shards` | gauge | — | sum across workers ≠ `SPINNERET_REPORT_SHARDS`: a shard is unowned |
| `spinneret_config_watchers` | gauge | — | approaching `SPINNERET_MAX_WATCHERS` |
| `spinneret_http_requests_total` | counter | `procedure` (e.g. `/spinneret.v1.LeaseService/Acquire`), `code` (`ok` or a Connect code such as `resource_exhausted`) | error rate per procedure |
| `spinneret_http_request_duration_seconds` | histogram | `procedure` | p99 per procedure |
| `spinneret_notify_deliveries_total` | counter | `kind`, `result` (`ok`, `error`, `dropped`) | any `dropped`; sustained `error` |
| `spinneret_db_write_batches_total` | counter | `writer` (`state_events`, `risk_events`, `proxy_bindings`, `acquire_stats_minutely`, `outcome_stats_minutely`, `node_stats_minutely`, `identity_stats_hourly`, `payload_access_minutely`), `result` (`ok`, `retry`, `error`, `dropped`) | any `dropped`; sustained `error` |
| `spinneret_state_writer_pending_changes` | gauge | — | growing queue |
| `spinneret_state_writer_dropped_changes_total` | counter | — | any increase: state changes were lost |
| `spinneret_state_writer_spilled_changes_total` | counter | — | any increase: non-lifecycle events discarded |
| `spinneret_job_runs_total` | counter | `job`, `result` (`ok`, `error`) | `error` rate per job |
| `spinneret_job_duration_seconds` | histogram | `job` | a job approaching its timeout |
| `spinneret_loop_restarts_total` | counter | `loop` | any increase |

The `job` label takes the values `lease_reaper`, `action.expiry`, `breaker_evaluate`,
`hotstate_snapshot`, `proxy_health_check`, `notify_alert_evaluation` and `partition_manager`.

The registry also carries the standard Go runtime and process collectors, so `go_goroutines`,
`go_memstats_*` and `process_resident_memory_bytes` are available without extra configuration.

### Scraping

The Compose stack ships a Prometheus under the `observability` profile:

```bash
docker compose -f deploy/compose/docker-compose.yml --profile observability up -d prometheus
# http://localhost:9090 (override with PROMETHEUS_PORT in .env)
```

Its configuration discovers every replica of the `spinneret` service through the Compose DNS record:

```yaml
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: spinneret
    metrics_path: /metrics
    dns_sd_configs:
      - names: ["spinneret"]
        type: A
        port: 8080
```

Outside Compose, point `static_configs` or your service discovery at each instance's HTTP address —
or at `SPINNERET_METRICS_ADDR` if you moved `/metrics` off the public listener, which is the
recommended shape when the API listener is reachable from outside the deployment.

**Note.** `SPINNERET_PPROF_ADDR` enables `net/http/pprof` on its own address. It is off by default,
is never mounted on the API or metrics listener, and is unauthenticated: it exposes heap contents and
goroutine stacks. Bind it to a loopback or private address only.

---

## Starter alert rules

Save as `deploy/compose/config/alerts.yml` and add `rule_files: ["alerts.yml"]` to `prometheus.yml`
(`rule_files` paths resolve relative to `/etc/prometheus`). The shipped Compose service bind-mounts
`prometheus.yml` alone, so the rules file needs a second mount on the `prometheus` service or the
container will not see it:

```yaml
  prometheus:
    volumes:
      - ./config/prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - ./config/alerts.yml:/etc/prometheus/alerts.yml:ro
```

Thresholds are starting points — tune them against the numbers your own deployment produces.

```yaml
groups:
  - name: spinneret
    rules:
      - alert: SpinneretInstanceDown
        expr: up{job="spinneret"} == 0
        for: 2m
        labels: { severity: critical }
        annotations:
          summary: "Spinneret instance {{ $labels.instance }} is not scrapeable"

      # `overloaded` is excluded on purpose: load shedding is a healthy response to
      # overload, and paging "over 5% of acquires are failing" would send the
      # operator to add identities for what is a capacity problem. It gets its own
      # saturation alert below.
      - alert: SpinneretAcquireFailures
        expr: |
          sum(rate(spinneret_acquire_total{result!="ok",result!="overloaded"}[5m])) by (site)
            / clamp_min(sum(rate(spinneret_acquire_total[5m])) by (site), 0.001) > 0.05
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "Over 5% of acquires on {{ $labels.site }} are failing"

      - alert: SpinneretAcquireShedding
        expr: |
          sum(rate(spinneret_acquire_total{result="overloaded"}[5m])) by (site)
            / clamp_min(sum(rate(spinneret_acquire_total[5m])) by (site), 0.001) > 0.05
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "Over 5% of acquires on {{ $labels.site }} are being shed (saturation, not failure)"
          description: >-
            Admission control is shedding. Check spinneret_acquire_script_seconds p99 first:
            if it spiked, Redis is stalled and raising SPINNERET_ACQUIRE_FLEET_INFLIGHT makes it
            worse. If it is normal and Redis CPU has headroom, the budget is too narrow.

      - alert: SpinneretAcquirePeerDivisionStuck
        expr: max(spinneret_acquire_peer_beat_age_seconds) by (instance) > 30
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "Acquire peer heartbeat of {{ $labels.instance }} is stale"
          description: >-
            The live API instance count is frozen, so the fleet acquire budget is no longer being
            divided by the real replica count. Cross-check spinneret_acquire_peers against
            count(up{job="spinneret"}) — or whatever counts your API-role instances.

      - alert: SpinneretAcquireSlow
        expr: |
          histogram_quantile(0.99,
            sum(rate(spinneret_acquire_duration_seconds_bucket[5m])) by (le, site)) > 0.05
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "Acquire p99 on {{ $labels.site }} is above 50 ms"

      - alert: SpinneretBreakerOpen
        expr: max(spinneret_breaker_state) by (site, group) == 2
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "Breaker {{ $labels.site }}/{{ $labels.group }} has been open for 5 minutes"

      - alert: SpinneretBreakerFlapping
        expr: sum(increase(spinneret_breaker_transitions_total{to="open"}[30m])) by (site, group) > 3
        labels: { severity: warning }
        annotations:
          summary: "Breaker {{ $labels.site }}/{{ $labels.group }} opened more than 3 times in 30 min"

      - alert: SpinneretReportBacklog
        expr: sum(spinneret_stream_pending) > 50000
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "More than 50k reports are waiting to be processed"

      - alert: SpinneretReportLagHigh
        expr: |
          histogram_quantile(0.99,
            sum(rate(spinneret_report_lag_seconds_bucket[5m])) by (le)) > 5
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "Report processing lag p99 is above 5 s"

      - alert: SpinneretShardsUnowned
        expr: sum(spinneret_stream_owned_shards) < 16
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "Fewer shards are owned than SPINNERET_REPORT_SHARDS: reports are not processed"

      - alert: SpinneretRiskRatioHigh
        expr: |
          sum(rate(spinneret_report_total{outcome=~"rate_limited|captcha|forbidden|banned"}[10m])) by (site)
            / clamp_min(sum(rate(spinneret_report_total[10m])) by (site), 0.001) > 0.1
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "Over 10% of reports on {{ $labels.site }} carry a risk outcome"

      - alert: SpinneretUnknownRatioHigh
        expr: |
          sum(rate(spinneret_report_total{outcome="unknown"}[10m])) by (site)
            / clamp_min(sum(rate(spinneret_report_total[10m])) by (site), 0.001) > 0.2
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "Signal rules are not classifying {{ $labels.site }}: over 20% unknown"

      - alert: SpinneretLeasesAbandoned
        expr: |
          sum(rate(spinneret_lease_reaped_total{kind="abandoned"}[15m])) by (site)
            / clamp_min(sum(rate(spinneret_acquire_total{result="ok"}[15m])) by (site), 0.001) > 0.05
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "Over 5% of leases on {{ $labels.site }} end without a report"

      - alert: SpinneretWritesLost
        expr: |
          increase(spinneret_db_write_batches_total{result="dropped"}[15m]) > 0
            or increase(spinneret_state_writer_dropped_changes_total[15m]) > 0
        labels: { severity: critical }
        annotations:
          summary: "Spinneret discarded database writes on {{ $labels.instance }}"

      - alert: SpinneretAlertDeliveryFailing
        expr: sum(rate(spinneret_notify_deliveries_total{result!="ok"}[15m])) by (kind) > 0
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "Alert deliveries through {{ $labels.kind }} channels are failing"

      - alert: SpinneretJobFailing
        expr: sum(rate(spinneret_job_runs_total{result="error"}[15m])) by (job) > 0
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "Background job {{ $labels.job }} keeps failing"
```

Adjust `SpinneretShardsUnowned` if you changed `SPINNERET_REPORT_SHARDS` from its default of 16.

---

## The console event stream

`GET /api/v1/events/stream?namespace=<name>` is a Server-Sent Events stream for console sessions. It
is authenticated like any console request, requires a tenant on the principal and a namespace the
caller may read, and it is a *push* channel for UI freshness — not an audit feed and not a webhook
substitute.

Six event types are published on the namespace channel, each gated by its own permission:

| Event | Permission | Payload |
| --- | --- | --- |
| `breaker.transition` | `breaker:read` | `site`, `site_id`, `client`, `endpoint_group`, `endpoint_group_id`, `from`, `to`, `trigger`, `reason`, `open_until`, `consecutive_opens`, `manual`, `actor`, `metrics` (the window and probe counters at transition time) |
| `identity.state` | `identity:read` | subject kind and ID, site, `from`, `to`, action, until, reason |
| `proxy.state` | `proxy:read` | proxy state change |
| `alert` | `notify:read` | `id`, `kind`, `severity`, `title`, `message`, `namespace_id`, `site_id`, `created_at` |
| `config.published` | `config:read` | `item_id`, `group`, `key`, `version`, `source_version` (rollbacks only), `actor` — the namespace is in the envelope as `namespace_id` |
| `policy.published` | `policy:read` | the published policy |

Every frame is `event: <type>` plus a JSON `data:` line with the event envelope
(`type`, `tenant_id`, `namespace_id`, `site_id`, `at`, `data`). An event whose type the caller has no
permission for is dropped silently; unknown types are never forwarded. The internal runtime-version
events that a breaker or policy change also publishes travel on a different channel and are not in
that table, so the console never sees them — it refetches the affected queries instead.

Operational shape: the server sends `retry: 5000` and a comment on connect, then a `: ping` comment
every 15 s to keep the connection alive through proxies; at most 5000 concurrent streams per
instance, after which new ones get `503 too many event streams`; each subscriber has a 256-event
queue and a slow consumer's events are dropped rather than blocking the bus. Delivery is best
effort. The stream is cancelled when the instance starts draining, and the console reconnects with
exponential backoff and jitter from 1 s up to 30 s, showing **Live updates reconnecting…** in the
shell while it does.

The console uses the stream to invalidate cached queries by domain and to raise toasts for breaker
transitions and alerts. If it is disconnected, pages fall back to their polling intervals (5 s or
10 s) — you lose immediacy, not correctness.

---

## Health endpoints

Two unauthenticated endpoints on the main HTTP listener, on every role:

`GET /healthz` — liveness. Always `200` while the process can serve HTTP:

```json
{"status": "ok"}
```

`GET /readyz` — readiness. Runs all dependency checks concurrently with a 2 s budget:

```json
{
  "status": "ok",
  "checks": {
    "postgres": "ok",
    "redis": "ok",
    "catalog": "ok",
    "hotstate": "ok"
  }
}
```

`200` with `"status": "ok"` when every check passed. `503` with `"status": "unavailable"` when any
failed; that check's value is a short reason instead of `ok` — `unreachable` for PostgreSQL or
Redis, `not loaded` for the catalog, and `building`, `unreachable` (the Redis epoch lookup itself
failed) or `epoch missing (rebuild pending)` for the hot state. The detailed cause is written to the
server log, not to the response.

During shutdown the endpoint short-circuits to `503` with:

```json
{"status": "draining"}
```

It stays that way for the whole shutdown window (`SPINNERET_SHUTDOWN_TIMEOUT`, default 30 s) while
in-flight requests finish, which is what lets a load balancer stop sending new traffic before the
process goes away. Both responses carry `Cache-Control: no-store`.

Put `/readyz` behind your load balancer and container health check, and `/healthz` behind your
restart policy: a readiness failure means "do not send traffic here", not "restart this".

The container image already does this — `HEALTHCHECK` runs every 10 s:

```bash
spnr healthcheck --url http://127.0.0.1:8080/readyz   # exit 0 on 2xx, 1 otherwise
```

`spnr healthcheck` takes `--url` (default the line above) and `--timeout` (default 3 s), follows no
redirects, and is meant for images without a shell. See [CLI reference](./15-cli.md).

---

## What to look at first

A short triage order for the three failures that actually happen.

**Nodes get fewer results, or none.** Open the overview. Read the acquire failure ratio tile first:

- non-zero **and** open breakers non-zero → a breaker is refusing leases; go to the breakers page,
  check the **Window** column and the history tab, and read the risk events of that endpoint group to
  see what tripped it.
- non-zero with breakers closed → supply. Look at the low watermark card and the site cards' state
  bars. A growing expired segment means payloads need refreshing
  ([Identities](./06-identities.md)); a growing banned segment means the target is pushing back.
- zero, but throughput is still down → the nodes are not asking. Check the node table for a node
  whose acquires dropped, and that node's own logs.

**Results are wrong or classification looks off.** Read the unknown ratio. Above ~20 % on one site
means the signal rules no longer match what the target returns: open the request explorer, filter
that site and `outcome = unknown`, and read the `business_code`, `error_kind` and `markers` of a few
events. That is exactly the input you need for a new signal rule
([Policies](./08-policies.md)).

**Everything is slow, or reports lag.** In order:

1. `sum(spinneret_stream_pending)` and the **Pending reports** hint on the overview — a growing
   backlog means workers cannot keep up.
2. `sum(spinneret_stream_owned_shards)` across workers against `SPINNERET_REPORT_SHARDS` — a missing
   shard means nobody is draining it.
3. `spinneret_report_lag_seconds` p99 versus `spinneret_report_process_duration_seconds` p99 — high
   lag with low processing time is a worker shortage; both high is per-report work, usually the
   database.
4. `spinneret_db_write_batches_total{result="dropped"}` and
   `spinneret_state_writer_dropped_changes_total` — any increase means data was discarded and
   PostgreSQL is the bottleneck.
5. `spinneret_acquire_duration_seconds` p99 and `spinneret_http_request_duration_seconds` by
   procedure — to tell a slow hot path from a slow admin API.

[Performance and tuning](./17-performance.md) explains what to change once you know which of those
it is; [Troubleshooting](./18-troubleshooting.md) has the symptom-to-fix table.

Two habits that pay for themselves: put `report_backlog`, `breaker_opened` and
`identity_low_watermark` on a channel that a human actually reads, and press **Send test alert** on
that channel the day you create it, not the day you need it.

---

## Next

- [Troubleshooting](./18-troubleshooting.md) — symptom → cause → fix, and the complete error-reason table.
- [Operations runbook](./16-operations.md) — backups, upgrades, retention and incident playbooks.
- [Performance and tuning](./17-performance.md) — what the numbers on these pages mean for capacity.
- [Policies](./08-policies.md) — the signal, action and breaker rules behind the outcomes you just read.
- [Configuration reference](./03-configuration.md) — every `SPINNERET_*` variable named on this page.
