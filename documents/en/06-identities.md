# Identities and accounts

**Everything about the identity subsystem: what an identity is, how you describe one with an identity type, how identities get into Spinneret, what the scheduler does with them, and every operation you can run on them from the console or the API.**

[中文](../zh/06-identities.md)

---

## Contents

- [What an identity is](#what-an-identity-is)
- [Identity types](#identity-types)
- [Delivery: from payload to credential](#delivery-from-payload-to-credential)
- [A worked example](#a-worked-example)
- [Creating and changing a type](#creating-and-changing-a-type)
- [Importing identities](#importing-identities)
- [The identity list](#the-identity-list)
- [The identity detail page](#the-identity-detail-page)
- [The state machine](#the-state-machine)
- [Health score](#health-score)
- [Manual operations](#manual-operations)
- [Reverting automatic actions](#reverting-automatic-actions)
- [Accounts](#accounts)
- [The cooldown heatmap](#the-cooldown-heatmap)
- [Running a pool](#running-a-pool)
- [Permissions](#permissions)
- [Limits](#limits)
- [Next](#next)

---

## What an identity is

An identity is **one reusable credential that a node borrows to make a request**: a cookie set, a
registered device, an API key, a signed token — whatever a target checks to decide who is calling.
Spinneret stores it, encrypts it, decides who may use it and when, and hands it to a node at
`Acquire` time as a rendered **credential**.

An identity is:

- **Typed.** Its contents are declared by an **identity type**, which also says how they are
  delivered to a node.
- **Encrypted at rest.** The payload is sealed with envelope encryption (see
  [Secret vault](./10-secrets.md)); the API returns it masked unless the caller holds
  `identity:reveal`.
- **Stateful.** It has a lifecycle state, a health score per endpoint group and a global one, and
  cooldowns at several levels. That state is what the scheduler reads.
- **Scoped to one site and one client.** It inherits both from its type. An identity of site
  `example-site`, client `web` is never leased for another site or another client.
- **Deduplicated.** Two imports of the same credential produce one identity, keyed by the type's
  `unique_by` paths.

An identity is **not**:

- a login flow — Spinneret never logs in, solves a captcha or signs a request. It manages the state
  around requests; the node still makes them;
- a proxy — proxies are a separate pool with their own state (see [Proxies](./07-proxies.md)),
  bound to identities only when a rotation policy says so;
- a node — a node is a process holding an API token; identities are what the node borrows.

IDs are prefixed: `idt_…` for an identity, `ity_…` for an identity type, `acc_…` for an account,
`evt_…` for a state event.

![Identities](../images/identities.png)

---

## Identity types

An identity type declares, in YAML:

- the **fields** a payload carries and their kinds,
- which fields are **sensitive** (masked in the console, masked in the API),
- the **unique_by** paths identities are deduplicated by,
- the **activation** mode of newly imported identities,
- the **deliver** mapping that turns a payload into the credential a node receives.

```yaml
# Web crawler: cookie identity
name: web_cookie
site: example-site
client: web
description: Logged-in web cookies
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

The YAML is decoded strictly: an unknown key is an error, not a warning, and the document must be a
single mapping.

### Top-level keys

| Key | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Type name, unique within the site. Must match `^[a-z0-9][a-z0-9_.-]{0,63}$`. |
| `site` | yes | Site name. The API request's `site` always wins and is written back into the stored YAML, so you may omit it in the console. |
| `client` | yes | Client of the site, `^[a-z0-9_-]{1,32}$`. Must be a declared client of the site. |
| `description` | no | Free text, at most 2048 bytes. |
| `fields` | yes | Map of field name → field spec. At least one, at most 128. |
| `unique_by` | no | Field paths that identify an identity. Default: every required field, sorted. At most 16. |
| `activation` | no | `probe` (default) or `immediate`. |
| `deliver` | no | Map of segment → template. Omitting it means a node receives an empty credential. |

Field names must match `^[a-zA-Z_][a-zA-Z0-9_]{0,63}$` and must not be one of the reserved import
keys `payload`, `_account`, `_region`, `_tags`, `_labels`.

### Field kinds

| `type` | Accepted on import | Stored as | Notes |
| --- | --- | --- | --- |
| `string` | a JSON string | string | Must be valid UTF-8. |
| `number` | a number or a numeric string (`"123"`, `"1.5e3"`) | float64 | Must be finite. |
| `bool` | a boolean or a string `strconv.ParseBool` accepts (`true`, `false`, `1`, `0`, `T`, `f`, …) | bool | |
| `cookie_map` | an object of strings, a `Cookie` header string, or an array of `{name, value}` objects | map of name → value | At most 1024 cookies, name ≤ 1024 bytes, value ≤ 16 KiB. |
| `json` | any JSON value | the value, numbers as float64 | Addressable with dotted sub keys, including array indices. |
| `secret_ref` | a namespace-relative secret path matching `^[a-z0-9][a-z0-9_./-]{0,255}$` | string | The payload stores the path; the secret's plaintext is resolved from the vault at delivery time and never stored. |

Each field spec accepts `type`, `required`, `sensitive` and `description` — nothing else.

A `cookie_map` accepts all three shapes, which is what makes browser exports usable directly:

```json
{"sessionid": "a1b2c3", "csrf_token": "xyz"}
"sessionid=a1b2c3; csrf_token=xyz"
[{"name": "sessionid", "value": "a1b2c3", "domain": ".example", "path": "/"}]
```

A leading `Cookie:` prefix is stripped, pairs are trimmed, `=` inside a value is kept, empty pairs
are skipped and the last occurrence of a name wins. Cookie names may not contain control
characters, whitespace, `=`, `;`, `,` or `"`; values may not contain control characters (tab
excepted) or `;`.

### Sensitive fields

`sensitive: true` means the value is masked everywhere it is displayed. Masking keeps the last four
characters: `••••b2c3`. A value of four characters or fewer is masked entirely (`••••`). A sensitive
`cookie_map` is masked cookie by cookie; a sensitive `json` field becomes `••••` as a whole. Fields
that are not declared in the type — which can only happen for a payload stored before the type
changed — are masked entirely, because their sensitivity is unknown.

Marking a field sensitive changes display only. It does not change storage: the whole payload is
always encrypted.

### unique_by and deduplication

`unique_by` is a list of field paths. A path is a field name, optionally followed by dotted sub keys
for `cookie_map` and `json` fields:

```yaml
unique_by: [cookies.sessionid]      # one cookie of a cookie_map
unique_by: [device_id]              # a whole string field
unique_by: [extra.device.id]        # a nested key of a json field
```

For a `cookie_map` the sub key is the cookie name (which may itself contain dots). For a `json`
field each dotted segment is an object key or a non-negative array index.

Spinneret computes the canonical JSON array of those values and stores an HMAC-SHA256 of it, keyed
with a server-side pepper (a 32-byte system key kept in the vault, encrypted with the KEK). The
plaintext unique key is never stored. Two rows with the same key are the same identity; an import
updates instead of inserting. Coercion happens before hashing, so `"123"` and `123` produce the same
key for a `number` field.

A path whose value is missing, null or empty (empty string, empty map, empty array) is an error:
the row is rejected with `unique_by path "…" is missing from the payload`.

If `unique_by` is omitted it defaults to the sorted list of required fields. If nothing is required
and nothing is declared, the type is rejected.

### Activation

| `activation` | New identities start in | Reach `active` when |
| --- | --- | --- |
| `probe` (default) | `pending` | a node reports `success` on a lease of that identity |
| `immediate` | `active` | immediately, at import |

`probe` is the safe default: freshly imported credentials are leased at a reduced weight (the
rotation policy's `probe.weight_factor`, default `0.1`, with at most `probe.max_leases`, default
`2`, concurrent probe leases), and only credentials that actually worked join the pool at full
weight. Use `immediate` when the credential was already validated elsewhere, or for synthetic
identities.

---

## Delivery: from payload to credential

A credential has exactly six segments. The `deliver` map fills the ones you need; the rest stay
empty.

| Segment | Type in `deliver` | What the node gets | Console description |
| --- | --- | --- | --- |
| `cookies` | one `{{ cookie_map_field }}` placeholder, or a map of string templates | `cookies`: name → value | Cookies for the HTTP client |
| `cookie_header` | a string template | `cookie_header`: `"k1=v1; k2=v2"`, names sorted | Cookie request header |
| `headers` | a map of string templates | `headers`: name → value | Merged into request headers |
| `query` | a map of string templates | `query`: name → value | Merged into query parameters |
| `json` | any JSON structure whose strings are templates | `json`: a JSON value | Merged into the request body |
| `values` | a map of string templates | `values`: name → typed value | Custom values, e.g. signing tokens |

Any other segment name is rejected. A delivery map may hold at most 256 entries and one template
string at most 8 KiB.

### Templates

Templates support exactly one construct: `{{ path }}`, where `path` is a field name optionally
followed by dotted sub keys. Spaces and tabs inside the braces are allowed. There are no pipes, no
function calls, no expressions and no conditionals — a template containing `|`, `(`, `)`, a space
inside the expression or any of `"'`` , + * / \ $ ! = < > &` is rejected at save time. This is
deliberate: it rules out template injection and keeps rendering cheap enough for the `Acquire` hot
path.

Rules that follow from that:

- **A template that is exactly one placeholder keeps the value's type.** `values: {retries: "{{ n }}"}`
  with a `number` field delivers the number `12`, not the string `"12"`. A template with any
  surrounding text renders as a string.
- **A whole `cookie_map` stringifies to a `Cookie` header value** (`"a=1; b=2"`). That is why
  `cookie_header: "{{ cookies }}"` works, and why a whole cookie map is rejected as the value of a
  single cookie in the `cookies` map — use `{{ cookies.name }}` there.
- **Missing optional fields render as empty.** An empty header, query, cookie or value entry is
  omitted from the credential rather than delivered empty. A missing `json` placeholder renders
  `null`.
- **Header and cookie syntax is checked** both at save time (for the literal parts) and at render
  time (for the result). A rendered value with characters illegal in a header value fails the
  render.
- **`secret_ref` fields resolve at render time.** The delivered value is the secret's plaintext, not
  the path. Each distinct path is resolved at most once per credential, and a credential that
  resolved a secret is cached for at most 60 seconds. The vault's own resolve cache sits behind
  that, so after a rotation a node can still receive the old value for about 90 seconds — the full
  window is in [Secret vault](./10-secrets.md).

### Payload validation

Every payload — on import, on `UpdateIdentityPayload` and in the preview — goes through the same
two steps:

1. **Normalization.** Unknown fields are rejected, JSON `null` values are treated as absent, cookie
   maps are parsed, numbers and booleans are coerced, `json` values are deep-copied, strings are
   checked for valid UTF-8, and required fields must be present.
2. **JSON Schema validation.** The type generates a JSON Schema (draft 2020-12) from its fields —
   `additionalProperties: false`, required fields listed, sensitive fields annotated
   `writeOnly: true` — and the normalized payload is validated against it. The schema is returned by
   the API as `json_schema`, so an external importer can validate rows before sending them.

Every problem found is reported in one `invalid_argument` error, prefixed with the field name, and
never quotes a payload value.

---

## A worked example

Type `web_cookie` above. This row is imported:

```json
{"cookies": "sessionid=a1b2c3; csrf_token=xyz", "user_agent": "example-crawler/1.0", "signature": "s3cr3t-token"}
```

Normalization turns the cookie header string into a map:

```json
{
  "cookies": {"csrf_token": "xyz", "sessionid": "a1b2c3"},
  "signature": "s3cr3t-token",
  "user_agent": "example-crawler/1.0"
}
```

The unique key is `["a1b2c3"]` (the `cookies.sessionid` path); its HMAC is what deduplicates the
identity.

A node that acquires this identity receives:

```json
{
  "cookies": {"csrf_token": "xyz", "sessionid": "a1b2c3"},
  "cookie_header": "csrf_token=xyz; sessionid=a1b2c3",
  "headers": {"User-Agent": "example-crawler/1.0"},
  "query": {},
  "json": null,
  "values": {"signature": "s3cr3t-token"}
}
```

The same identity in the console, for an operator without `identity:reveal`:

```json
{
  "cookies": {"csrf_token": "••••", "sessionid": "••••b2c3"},
  "signature": "••••oken",
  "user_agent": "example-crawler/1.0"
}
```

`user_agent` is not sensitive, so it is shown as is. The value of `csrf_token` is three characters,
so it is masked completely.

### Delivery preview

`PreviewDelivery` renders a sample payload against an existing type (`type_id`) or a draft spec
(`spec_yaml`) and returns the credential, the normalized payload and any errors. Nothing is stored.
In the console it is the **Delivery preview** tab of the identity type editor, and the **Delivery
preview** row action on the list.

`secret_ref` values are **never** resolved in a preview: they render as the literal placeholder
`<secret:PATH>`. The preview cannot be used to read a secret.

---

## Creating and changing a type

In the console: **Identity Types** → **New type**. Pick the site, write the YAML (the **Insert
example** menu offers a cookie type and a device type), check the **Delivery preview** tab, then
**Create**. Opening an existing type shows the same editor; **Save new version** stores the next
version.

Over the API:

```bash
curl -sS https://spinneret.example.com/spinneret.v1.IdentityAdminService/CreateIdentityType \
  -H 'Authorization: Bearer spn_…' \
  -H 'Content-Type: application/json' \
  -d '{
        "namespace": "default",
        "site": "example-site",
        "spec_yaml": "name: web_cookie\nclient: web\nfields:\n  cookies: { type: cookie_map, required: true, sensitive: true }\nactivation: probe\ndeliver:\n  cookie_header: \"{{ cookies }}\"\n"
      }'
```

What can and cannot change:

| | Create | Update |
| --- | --- | --- |
| `site` | from the request (overrides the YAML) | fixed; naming a different site in the YAML is an error |
| `name` | from the YAML | fixed |
| `client` | from the YAML, must be a client of the site | fixed |
| fields, `unique_by`, `activation`, `deliver`, `description` | from the YAML | replaced |
| version | `1` | incremented on every update |

**Changing `unique_by` rehashes the type.** The update recomputes the unique key of every identity
of the type in the same transaction, which means decrypting every current payload. It fails with
`failed_precondition` if an identity has no value for the new paths or if two identities would
collide. On a large pool this is a slow, locking operation — plan it like a migration.

Deleting a type requires that it has no identities at all, retired ones included; otherwise the call
fails with `failed_precondition`.

Concurrency is handled with an advisory lock on the type plus a version check: an import or a
payload update that races a spec change fails with `conflict` and asks you to retry, rather than
writing a payload normalized with an outdated spec.

Creating, updating and deleting a type needs `site:write`; reading and previewing needs
`identity:read` or `site:read`.

---

## Importing identities

Import is how identities normally enter Spinneret: from the console (**Identities** → **Import**),
or from a credential-refresh service calling `ImportIdentities` with an API token carrying the
`identity:write[:<site>]` scope.

An import always targets **one site and one identity type**.

### JSON Lines

One JSON object per non-empty line. Two shapes are accepted.

**Flat** — the object is the payload, minus the reserved keys:

```json
{"cookies": "sessionid=a1b2c3; csrf=x", "user_agent": "example-crawler/1.0", "_account": "user-1", "_region": "US", "_tags": "pool-a,warm", "_labels": {"batch": "2026-03-01"}}
```

**Envelope** — the object has a `payload` key:

```json
{"payload": {"cookies": {"sessionid": "a1b2c3"}}, "account": "user-1", "region": "US", "tags": ["pool-a", "warm"], "labels": {"batch": "2026-03-01"}}
```

An envelope accepts only `payload`, `account`, `region`, `tags` and `labels`; any other key rejects
the row. `tags` may be an array of strings or a comma-separated string. Blank lines are skipped and
a leading UTF-8 byte order mark is ignored.

### CSV

A header row of field names is required. Reserved columns carry metadata:

| Column | Meaning |
| --- | --- |
| `_account` | account external reference |
| `_region` | region |
| `_tags` | tags, separated by `;` |
| `_labels` | labels as `k=v;k2=v2` |

Any other column starting with `_` is rejected in the header. Duplicate or empty header columns are
rejected. Empty cells are omitted, and a cell starting with `{` or `[` is decoded as JSON when it
parses; everything else stays a string, which `Normalize` then coerces.

```csv
cookies,user_agent,_account,_region,_tags
"sessionid=a1b2c3; csrf=x",example-crawler/1.0,user-1,US,pool-a;warm
"{""sessionid"":""d4e5f6""}",example-crawler/1.0,user-2,DE,pool-b
```

### Modes, dry run and what happens to each row

| Field | Values | Effect |
| --- | --- | --- |
| `mode` | `upsert` (default), `create_only` | `create_only` leaves existing identities untouched |
| `dry_run` | `true` / `false` | validates and counts; nothing is stored |

Per row, keyed by the unique hash:

| Situation | `upsert` | `create_only` | Counter |
| --- | --- | --- | --- |
| Unique key not seen before | identity created, payload stored as version 1, state from `activation` | same | `created` |
| Exists, payload differs | new payload version, lifecycle re-validated, health and failure streaks reset | skipped | `updated` / `unchanged` |
| Exists, payload identical | attributes the row sets are applied | skipped | `unchanged` |
| Same unique key twice in one file | second row rejected: `duplicate unique key (same identity as line N)` | same | `failed` |
| Row unparseable, payload invalid, or a rejected secret reference | rejected with its line number | same | `failed` |

Attribute updates are partial: an empty `_account`/`_region` and absent `_tags`/`_labels` leave the
stored values alone. A non-empty `_account` attaches the account with that reference on the site,
creating it if it does not exist.

The state change of a payload update follows the same rule as `UpdateIdentityPayload` (see
[The state machine](#the-state-machine)) and is recorded as a `payload_update` state event.

**Secret references.** If a payload references a secret through a `secret_ref` field, storing it is
equivalent to reading that secret — every lease of the identity resolves it. So the importer must be
allowed to read every newly referenced path (`secret:reveal` on the namespace for users, a
`secret:read` scope matching `<namespace>/<path>` for tokens) **and** the secret must exist.
References that the same field of the stored payload already carried are not re-authorized.
A row that fails this check is rejected individually — with its line number, counted in `failed` —
whether the identity is new or already exists; every other row of the import still commits.
`UpdateIdentityPayload`, which touches a single identity, fails the whole call instead, with
`permission_denied` or `invalid_argument`.

### Results and limits

```json
{"created": 812, "updated": 44, "unchanged": 9102, "failed": [{"line": 37, "message": "cookies: required field is missing"}]}
```

At most 1000 failures are reported individually; the rest are summarized in one extra entry with
`line: 0` and the message `N more rows failed`.

| Limit | Value |
| --- | --- |
| Rows per import | 50 000 |
| Bytes per import | 32 MiB |
| Rows per transaction | 500 |
| Failures reported | 1000 + one summary entry |
| Payload versions kept per identity | 5 |

### Importing at scale

An import is written in chunks of 500 rows, each in its own transaction with a two-minute timeout.
That means an infrastructure failure mid-import can leave earlier chunks committed — they are
synchronized to the hot state and audited normally. Re-running the same file is safe: unchanged rows
count as `unchanged`.

For pools larger than 50 000, split the file and loop. A sensible pattern for a refresh service:

1. `dry_run: true` once on a sample to confirm the shape parses;
2. import in files of 10 000–50 000 rows;
3. read `created`/`updated`/`unchanged`/`failed` from each response and alert on `failed`.

Each import writes one `identity.import` audit entry with the site, type, format, mode and counters.

For load testing, `spnr seed --site loadtest --identities 100000` generates synthetic identities of a
generated cookie type directly — see the [CLI reference](./15-cli.md).

---

## The identity list

**Identities** in the console (`/identities`) lists every identity of the selected namespace, on the
sites you may read. The list refreshes every 5 seconds while no dialog is open.

### Filters

| Filter | Field | Notes |
| --- | --- | --- |
| Site | `filter.site` | empty matches every accessible site |
| Identity type | `filter.type` | type name |
| States | `filter.states` | multi-select; empty matches every state except `retired` |
| Search | `filter.search` | identity ID **prefix**, or an exact label value |
| Tags | `filter.tags` | the identity must carry **all** of them |
| Account | `filter.account_ref` | exact external reference |
| Region | `filter.region` | exact |
| Min / max score | `filter.min_score`, `filter.max_score` | 0–100, min must not exceed max |
| Include retired | `filter.include_retired` | only has an effect while no state is selected |

Tags, account, region, the score range and "include retired" sit behind **More filters**; the badge
on the button counts how many of them are active.

### Columns

| Column | Content |
| --- | --- |
| ID | `idt_…`, links to the detail page |
| Site / Client / Type | from the identity type |
| State | state badge, with the reason and the end of a ban or quarantine |
| Account | external reference, empty when the identity has no account |
| Region, Tags | free-form attributes |
| Global score | score bar plus the sample count |
| Active leases | leases currently held (see the note below) |
| Payload | `v<n>`, the current payload version |
| Last used | time of the last lease |
| State changed | time of the last state change |

Sort by **Created**, **Updated**, **State changed**, **Last used** or **Global score**, ascending or
descending; paging is keyset-based, 50 rows per page by default and at most 500.

**Note.** The score in the list comes from the hot-state snapshot that Spinneret writes to
PostgreSQL every 60 seconds, so it can lag the live value by up to a minute. The detail page reads
Redis directly. Identities that have never been observed show the baseline (70). The live lease
count is likewise only read from Redis by `GetIdentity`: `ListIdentities` leaves `active_leases`
at 0, so trust that column on the detail page, not in the list.

Selecting rows enables the operations menu; a selection is limited to 1000 identities per call.
**Operate on matching** applies the same operation to everything the current filter matches instead
(see [Manual operations](#manual-operations)).

---

## The identity detail page

`/identities/<id>` (**Identities** → click an ID).

![Identity detail](../images/identity-detail.png)

**Attributes.** Site, client, type, account, region, tags, labels, created / activated / updated
times. **Edit** changes region, tags, labels and the account reference — only what you changed is
sent, and the payload is untouched. Setting an account reference that does not exist creates the
account; clearing it detaches the identity.

**Live state.** The site-level hot state read straight from Redis: the scheduler's view of the
state (flagged when it differs from the stored state), the site cooldown, the site reuse interval,
the exclusive-lease window, the account cooldown and the bound proxy. If the identity is not
materialized in the hot state at all, the card says so — it is then not schedulable until the next
rebuild or state change.

**Endpoint groups.** One row per endpoint group of the identity's client, not only the ones it has
state in — a group with no hot-state entry shows the baseline score and 0 samples. The columns are
health score, samples, consecutive failures, cooldown, reuse interval, last used, the earliest time
it can be leased, and whether it is in the ready queue. The status column resolves to, in order of
precedence: *Cooling down* →
*Reuse interval* → *Waiting* → *Ready* (queued) or *Eligible* (not queued).

**Payload.** The current payload, masked. **Reveal** requires `identity:reveal` on the site, is
recorded in the audit log as `identity.reveal`, and the clear text is hidden again after 60 seconds.
**New version** opens a JSON editor validated against the type's schema; masked values (`••••`)
cannot be saved, so reveal first or replace them. Storing a new version bumps the payload version,
resets health scores and failure streaks, and applies the payload-update transition. Spinneret keeps
the last five payload versions of an identity; older ones are deleted when a new one is written.

**State timeline.** The identity's state events, newest first, with the action, the from/to states,
the scope and endpoint group, the end of a temporary disposition, the policy and rule that produced
it, the report and lease it came from, the actor and the reason. Events produced by a policy in
shadow mode are marked **Shadow**: they were recorded but nothing was applied. `GetIdentity` returns
the 20 most recent events inline; the timeline pages further back through `ListStateEvents`.

**Recent risk events.** Non-success reports of this identity in the last 24 hours — time, endpoint
group, outcome, blame, HTTP status, rule, proxy, node and latency. See
[Observability](./12-observability.md).

The **Operations** menu on this page runs the same operations as the list, on this one identity.

---

## The state machine

| State | Meaning | Scheduled? |
| --- | --- | --- |
| `pending` | imported or re-validated, not yet proven | yes, at probe weight |
| `active` | proven working | yes |
| `expired` | the credential no longer authenticates; a refresh service should renew it | no |
| `banned` | removed from scheduling until `ban_until`, or permanently | no |
| `quarantined` | set aside for review until `quarantine_until` | no |
| `disabled` | switched off by an operator or by its account | no |
| `retired` | archived; hidden from lists unless you ask for it | no |

Only `pending` and `active` take part in scheduling.

### Manual transitions

Each operation accepts only certain source states; an identity in another state fails with
`invalid_transition` and the message `cannot <operation> an identity in state <state>`.

| Operation | From | To |
| --- | --- | --- |
| `ban` | `active`, `pending`, `quarantined`, `expired`, `disabled` | `banned` |
| `unban` | `banned` | `pending` |
| `quarantine` | `active`, `pending` | `quarantined` |
| `unquarantine` | `quarantined` | `pending` |
| `expire` | `active`, `pending`, `quarantined` | `expired` |
| `disable` | `pending`, `active`, `expired`, `banned`, `quarantined` | `disabled` |
| `enable` | `disabled` | `active` |
| `archive` | every state except `retired` | `retired` |
| `restore` | `retired` | `pending` |
| `activate` | `pending` | `active` |

`cooldown` and `reset_stats` are not lifecycle transitions: they only change the hot state. A
cooldown is refused only for `retired` identities.

### Automatic transitions

| Trigger | Effect | Recorded as |
| --- | --- | --- |
| A `pending` identity gets a `success` report | → `active` | rule `lifecycle.activate` |
| An action rule matches a report with `action: expire` | → `expired` | the rule's name |
| An action rule matches with `action: ban` | → `banned`, duration from the rule, possibly raised by the escalation ladder | the rule's name |
| An action rule matches with `action: quarantine` | → `quarantined` | the rule's name |
| Global score below `health.quarantine_score` (default 20) with at least `health.quarantine_min_samples` (default 10) samples | → `quarantined` for `health.quarantine_duration` (default 24h) | rule `health.quarantine` |
| Endpoint score below `health.endpoint_low_score` (default 15) with at least `health.endpoint_low_min_samples` (default 10) samples | endpoint cooldown of `health.endpoint_low_cooldown` (default 6h) | rule `health.endpoint_low` |
| A ban reaches `ban_until` | → the action policy's `ban_expiry_state`: `pending` (default) or `active` | reason `ban_expired` |
| A quarantine reaches `quarantine_until` | → `pending` | reason `quarantine_expired` |
| An account is banned or disabled | member identities follow | reason `account_ban` / `account_disabled` |
| A payload is replaced | see below | action `payload_update` |

Bans and quarantines end through a leader job that runs every 10 seconds. It refuses to release
anything while Redis does not answer, so a release is never committed to PostgreSQL while it is
invisible to the scheduler.

Which rules exist, what they match and how bans escalate is the subject of
[Policies](./08-policies.md). A policy in **shadow mode** records the state events it would have
produced without touching the identity.

### Payload updates

Replacing a payload — by import or by `UpdateIdentityPayload` — re-validates the identity:

| State before | State after, `activation: probe` | State after, `activation: immediate` |
| --- | --- | --- |
| `active`, `pending`, `quarantined`, `expired` | `pending` | `active` |
| `banned`, `disabled`, `retired` | unchanged | unchanged |

A quarantine is cleared when the identity leaves `quarantined` this way. Health scores and failure
streaks are reset in the hot state. A payload identical to the stored one creates no version and
changes no state.

That is the credential-refresh loop: a rule marks a dead cookie `expired`, an external service sees
`state: expired` through `ListIdentities`, logs in again and imports the fresh cookie, the identity
returns to `pending`, and the first successful probe promotes it to `active`.

---

## Health score

Every identity carries a health score between 0 and 100, per endpoint group **and** globally. It is
an exponentially weighted moving average with time decay toward a baseline.

On each report that affects the identity:

```text
decayed = baseline + (score - baseline) * exp(-Δt / tau)
score   = alpha * observation + (1 - alpha) * decayed
samples = samples + 1
```

| Parameter | Default | Meaning |
| --- | --- | --- |
| `health.baseline` | 70 | the score an identity relaxes toward when it is not used, and the score of an identity with no history |
| `health.alpha` | 0.1 | weight of one observation |
| `health.tau` | 6h | decay time constant; after `tau` the distance to the baseline has shrunk by 1/e |
| `health.observations` | see below | the value one outcome contributes |

Default observation values, overridable per outcome in the action policy:

| Outcome | Observation |
| --- | --- |
| `success` | 100 |
| `empty` | 60 |
| `network_error` | 50 |
| `rate_limited` | 30 |
| `forbidden` | 10 |
| `captcha` | 0 |

A `success` always affects the identity. Any other outcome affects it only when the blame includes
the identity — a `network_error` blamed on the proxy leaves the identity's score alone. Outcomes not
in the table (`auth_invalid`, `banned`, `proxy_error`, `target_error`, `client_error`, `unknown`) do
not move the score at all; they are handled by action rules instead.

Alongside the score, each level keeps a **consecutive failure streak**: incremented by each failure
(`empty`, `rate_limited`, `captcha`, `auth_invalid`, `forbidden`, `banned`, and `network_error` when
blamed on the identity), **halved** on each success, and restarted at 1 by the next failure that
arrives more than `failure_reset_after` after the previous one. The default is 1h; the value
actually used is the longest `failure_reset_after` among the cooldown rules of the action policy
that evaluates the report. Nothing clears the streak in the background, so the **Consecutive
failures** column keeps its value while the identity sits idle. The streak drives exponential
cooldown backoff.

How to read a score:

- **≥ 60** — healthy (the console draws it green).
- **30–59** — degraded.
- **< 30** — unhealthy; below `quarantine_score` (20) with enough samples the identity is
  quarantined automatically.
- **Exactly 70 with 0 samples** — no history at all. Either the identity has never been used, or it
  is not materialized in the hot state.

A score only means something with samples behind it: the console always shows the sample count next
to the bar, and the automatic actions have minimum-sample thresholds for exactly that reason.

`reset_stats` puts the score back to the baseline and clears samples, streaks and counters. Which
levels it touches depends on its `scope` — see [Scopes](#scopes).

---

## Manual operations

From the list (on a selection), from the detail page, or through `OperateIdentities` (explicit IDs)
and `BulkOperateIdentities` (everything matching a filter). All of them need `identity:operate` on
the site of each identity.

| Operation | Duration | Scope | What it does |
| --- | --- | --- | --- |
| `cooldown` | required, > 0, no `permanent` | yes | Pauses leasing for a while |
| `ban` | required, > 0 or `permanent` | – | Removes the identities from scheduling until the ban ends |
| `unban` | – | – | Lifts the ban; identities go to `pending` |
| `quarantine` | optional (policy default when empty), no `permanent` | – | Sets identities aside for review |
| `unquarantine` | – | – | Ends the quarantine early |
| `expire` | – | – | Marks the credential expired so a refresh service renews it |
| `disable` | – | – | Stops scheduling until enabled again |
| `enable` | – | – | Makes disabled identities schedulable |
| `archive` | – | – | Retires the identities |
| `restore` | – | – | Brings retired identities back to `pending` |
| `activate` | – | – | Promotes `pending` identities without waiting for a probe |
| `reset_stats` | – | yes | Resets health scores, failure streaks and counters |

Durations are written as `30m`, `2h`, `7d`, `1d12h` — units `ms`, `s`, `m`, `h`, `d` — or the word
`permanent`, which only `ban` accepts. The console offers presets `30s`, `5m`, `10m`, `30m`, `1h`,
`6h`, `1d`, `7d`, `30d` and defaults to `30m` for a cooldown and `7d` for a ban.

### Scopes

`cooldown` and `reset_stats` take a level:

| `scope` | Console label | `cooldown` applies to | `reset_stats` applies to |
| --- | --- | --- | --- |
| `identity_endpoint` | One endpoint group | one endpoint group; `endpoint_group_id` is required and must belong to the identity's site and client | the same one endpoint group |
| `identity_site` | Whole site | the site level: every endpoint group of the identity's client | the global score, the global failure streak and the outcome counters — **not** the per-endpoint-group entries |
| `identity` | — | same as `identity_site` | same as `identity_site` |
| *(empty)* | All levels | same as `identity_site` | the global score, the global failure streak, the outcome counters **and** every endpoint group of the identity's client |

The other operations ignore the scope: they change the lifecycle state, which is site-wide by
definition.

### Reset flags

`unban`, `unquarantine`, `enable`, `restore` and `activate` accept two extra switches:

- `reset_failures` — also clear consecutive failure streaks,
- `reset_health` — also reset health scores to the baseline.

Use them when the identity was punished for something that was not its fault, so it does not start
its second life one bad report away from being banned again. Other operations ignore both flags.

### Reason

Every operation takes a free-text `reason` (at most 512 bytes). It is stored on the state event and
in the audit log. Write one — six weeks later it is the only thing that explains a 30 000-identity
ban.

### Explicit IDs

```bash
curl -sS https://spinneret.example.com/spinneret.v1.IdentityAdminService/OperateIdentities \
  -H 'Authorization: Bearer spn_…' -H 'Content-Type: application/json' \
  -d '{"ids": ["idt_01hx…", "idt_01hy…"],
       "operation": "cooldown", "scope": "identity_site",
       "duration": "30m", "reason": "target-side maintenance window"}'
```

At most 1000 IDs per call, unique and non-empty. The response is a `BulkResult`:

```json
{"result": {"matched": 2, "succeeded": 2, "failed": []}}
```

`matched` is the number of IDs that actually reached the operator: an ID rejected before it, as
`not_found` or `endpoint_group_unknown`, is reported in `failed` but not counted in `matched`. So
`matched` equals `succeeded` plus the failures the operator itself reported.

Failure reasons you can get per identity:

| Reason | Meaning |
| --- | --- |
| `not_found` | unknown ID, or an identity in a namespace you have no relation to |
| `endpoint_group_unknown` | the endpoint group does not belong to that identity's site |
| `invalid_transition` | the identity's current state does not allow the operation |
| `invalid_argument` | the endpoint group does not match the identity's site and client |
| `not_in_hot_state` | a cooldown or reset needs the identity materialized in Redis, and it is not |
| `state_changed` | the identity changed concurrently; retry |
| `internal` | the operation failed and can be retried |

An identity on a site you may not operate fails the **whole request** with `permission_denied`;
unknown ones only fail their own row.

### By filter

`BulkOperateIdentities` takes the same `IdentityFilter` as the list, plus `dry_run` and `limit`
(default and maximum 100 000). In the console it is **Operate on matching**, which forces you to
**Preview count** before the confirm dialog appears.

```bash
curl -sS https://spinneret.example.com/spinneret.v1.IdentityAdminService/BulkOperateIdentities \
  -H 'Authorization: Bearer spn_…' -H 'Content-Type: application/json' \
  -d '{"namespace": "default",
       "filter": {"site": "example-site", "type": "web_cookie", "states": ["banned"], "tags": ["pool-a"]},
       "operation": "unban", "reset_failures": true, "reset_health": true,
       "reason": "rule misfire on 2026-03-01", "dry_run": true}'
```

A dry run returns `matched` only and changes nothing. A real run resolves the matching IDs in pages
and applies the operation in chunks of 1000, so `matched` and `succeeded` can differ if identities
change state while the operation runs. Every bulk run writes an `identity.bulk_operate` audit entry.

**Warning.** An empty filter matches every non-retired identity in the namespace. The console says
so explicitly before you confirm; over the API nothing stops you.

---

## Reverting automatic actions

`RevertActions` is the undo button for a rule that misfired. It looks at the state events written by
the system (not shadow events, not manual ones) in a time range and rolls back the ones whose effect
is still current.

| Field | Meaning |
| --- | --- |
| `namespace` | required |
| `site` | empty covers every site you may operate |
| `policy_id` | empty matches any policy |
| `rule` | rule name; empty matches any rule |
| `actions` | any of `ban`, `quarantine`, `expire`, `cooldown`; empty means all four |
| `time_range.start` | **required** — a revert never silently covers the whole history |
| `time_range.end` | empty means now |
| `reset_failures`, `reset_health` | applied to the identities that come back |
| `dry_run` | list the affected identities without changing anything |

What it does:

- an identity still in the state the event produced, and not changed since, goes back to `pending`
  with the reason `reverted`;
- an identity that has moved on since is left alone (it is not even counted);
- accounts banned automatically in the range are unbanned when `ban` is reverted;
- identity cooldowns still in effect are cleared.

The response is a `BulkResult` plus up to 1000 affected identity IDs; at most 50 000 candidate
events are considered in one call. Each reverted identity gets a `revert` state event recording the
event it undid. The console dialog (**Revert actions** on the identity list) forces a dry run first
and offers quick ranges.

Reverting does not change the policy. Fix the rule too, or the next report will apply it again — see
[Policies](./08-policies.md).

---

## Accounts

An **account** groups identities that belong to the same upstream user account on one site. It is
identified by the site plus an **external reference** (a user name, a UID, anything stable and
unique within the site).

Accounts exist so that one decision can cover several credentials. If one upstream account is
flagged, every cookie set derived from it is worthless at the same instant — and often the target
enforces limits per account rather than per session.

| Account state | Meaning |
| --- | --- |
| `active` | normal |
| `banned` | banned until `ban_until`, or permanently |
| `disabled` | switched off by an operator |

Accounts also carry a **cooldown** (`cooldown_until`), a region, tags and operator notes.

### Attaching identities

- On import, the `_account` column or the `account` envelope key. The account is created if missing.
- On the detail page, **Edit** → the account field. Same rule; clearing it detaches.
- `UpsertAccount` creates or updates the account itself (region, tags, notes) by site and external
  reference.

An identity has at most one account.

### Account operations

`OperateAccount` needs `identity:operate` and takes `ban`, `unban`, `cooldown`, `disable` or
`enable`. `ban` and `cooldown` need a duration; only `ban` accepts `permanent`.

| Operation | Account | Member identities |
| --- | --- | --- |
| `ban` | `active`, `disabled`, `banned` → `banned` | members in `active`, `pending`, `quarantined`, `expired`, members disabled *because of this account*, and members whose own ban ends earlier → `banned`, reason `account_ban` |
| `unban` | `banned` → `active` | members banned with reason `account_ban` → `pending` |
| `disable` | `active` → `disabled` | members in `active` or `pending` → `disabled`, reason `account_disabled` |
| `enable` | `disabled` → `active` | members disabled with reason `account_disabled` → `active` |
| `cooldown` | state unchanged | no state change; an account-level cooldown blocks leasing of every member identity until it ends |

The reason marker is what makes this reversible without collateral damage: unbanning an account only
releases the identities the account ban took down, not the ones an operator banned individually.

The response reports the account plus a `BulkResult` for the identities, where `matched` is the
number of member identities and `succeeded` the number actually changed. `reset_failures` and
`reset_health` apply to members that become schedulable again.

An account cooldown shows up on the identity detail page as **Account cooldown**, and is included in
the remaining cooldown the heatmap paints.

The console page is **Accounts** (`/accounts`): filter by site, state or external reference, add an
account, run an operation, or jump to **View identities** — which opens the identity list filtered by
that account.

---

## The cooldown heatmap

**Heatmap** (`/heatmap`) draws the identities of **one site and one client** as rows and its endpoint
groups as columns. It answers one question fast: *is a whole endpoint group in trouble, or just a
few identities?* It is dashboard data rather than identity data, so it needs `dashboard:read` on the
site, not `identity:read`.

![Cooldown heatmap](../images/heatmap.png)

Controls: site, client, metric, state filter, rows per page (100, 250 or 500) and chart/table view.
The page refreshes every 10 seconds.

| Metric | Cell colour |
| --- | --- |
| **Cooldown remaining** | how long the identity is still blocked in that group, counting the endpoint, site and account cooldowns and any ban; a permanent ban is its own bucket |
| **Score** | the decayed health score of that identity in that group |

Both values are always present in the response, so switching metric does not refetch.

Reading it:

- **The state filter starts at `active` + `pending`.** Widen it to see banned, quarantined, expired
  or disabled identities. Even "all states" excludes `retired`.
- **A dark column** — one endpoint group is cooling down across the whole pool. That is a target-side
  or rule-side problem, not a credential problem. Check the breaker and the rules for that group
  ([Policies](./08-policies.md), [Observability](./12-observability.md)).
- **Dark rows** — individual identities are burnt. Look at the accounts and the proxies they share.
- **A pale grid with no cells** — the identities have no hot state in those groups: they were never
  leased there, or the hot state has not been rebuilt. Cells with no hot state show the baseline
  score and follow the identity's lifecycle state for availability.
- **Click a cell** to open that identity. The **Table** view shows the same data as text, which is
  also the accessible view.

Rows are paged; the pager shows `Identities from–to of total`.

---

## Running a pool

### Sizing

The pool size you need is driven by how often one identity may be used, not by request volume alone.
For one endpoint group:

```text
sustainable RPS  ≈  usable identities / max(reuse_interval, average request duration)
usable identities = active + pending, minus those cooling down, banned or quarantined right now
```

`reuse_interval`, `max_concurrent_leases` (default 1 — exclusive) and `quota` come from the rotation
policy bound to that endpoint group; see [Policies](./08-policies.md). With
`max_concurrent_leases: 1` and a `reuse_interval` of 60 s, 600 identities give you at most 10 RPS,
and only while none of them is cooling down.

So size for the **bad** day: take the throughput you need, divide by the per-identity rate the
policy allows, and add headroom for the fraction of the pool that is typically unavailable. The
heatmap tells you what that fraction is.

If nodes get `no_identity_available` (a `resource_exhausted` error carrying `retry_after_ms`), the
pool is too small, too cold or too burnt for the current load — see
[Troubleshooting](./18-troubleshooting.md).

### Spotting a burnt pool

Symptoms, in the order they usually appear:

1. The **Global score** distribution on the identity list drifts down; more and more rows are amber
   or red.
2. The heatmap turns dark in **Cooldown remaining** across many rows at once.
3. The `banned` and `quarantined` counts grow on the overview dashboard.
4. Nodes start getting `no_identity_available`.

Diagnose before you act:

- Filter the identity list by **State = banned, quarantined** and sort by **State changed**
  descending. If a large number of identities changed state within the same few minutes, a rule
  fired on a wave — not a slow decay.
- Open one of them and read the **State timeline**: the rule name and the report tell you which
  policy decided it and on which outcome.
- Check the **Account** column. If the burnt identities share a handful of accounts, the accounts
  were flagged upstream, not the cookies.
- Check the bound proxies on the detail page. If they share a proxy or a subnet, it is a proxy
  problem; look at [Proxies](./07-proxies.md).

Then act:

- rule misfire → **Revert actions** for that rule and time range, with `reset_failures` and
  `reset_health`, then fix the rule;
- genuine upstream bans → leave the identities banned and import replacements;
- an account-level problem → ban the account, not the identities one by one;
- a proxy-level problem → operate on the proxy pool instead.

### Warm-up

A freshly imported credential is the most fragile thing in the pool. Two mechanisms protect it, both
configured in the rotation policy:

- **Probe.** With `activation: probe`, new identities stay `pending` and are selected at
  `probe.weight_factor` (default 0.1) of a normal identity's weight, with at most
  `probe.max_leases` (default 2) concurrent probe leases. One successful report promotes the
  identity to `active`.
- **Warm-up quota.** `warmup.duration` (default `0s`, i.e. off) and `warmup.quota_factor`
  (default 1) scale the quota of an identity for a period after it became active. Setting
  `warmup: {duration: 24h, quota_factor: 0.25}` gives a new identity a quarter of the normal request
  budget for its first day.

Practical warm-up procedure for a new batch:

1. Import with `activation: probe` and a tag such as `batch-2026-03-01`, so you can filter and
   operate on the batch later.
2. Let it run. Watch the batch on the identity list filtered by that tag, sorted by **Global score**.
3. After a day, compare the batch's score distribution with the rest of the pool. A batch that is
   uniformly worse than the pool is a bad source, not bad luck.
4. If a batch turns out to be bad, `archive` the whole batch by filter rather than deleting it —
   archived identities keep their history and can be restored.

Do not import a large batch and switch it to `active` with `activate` to "save time". You lose the
probe, and the first thing the target sees is several thousand unproven credentials at full rate.

### Retiring

There is no delete. `archive` moves identities to `retired`: they disappear from lists (unless you
tick **Include retired**), are removed from the hot state and are never scheduled. `restore` brings
them back to `pending`. Retired identities still count for deleting an identity type, and still hold
their encrypted payload — retention is covered in the [Operations runbook](./16-operations.md).

---

## Permissions

| Operation | Permission |
| --- | --- |
| List / get identities, types, accounts, state events, hot state | `identity:read` (types and previews also accept `site:read`) |
| Import, update a payload, update attributes, upsert an account | `identity:write` |
| Every manual operation, bulk operation, revert, account operation | `identity:operate` |
| Reveal a payload in clear text | `identity:reveal` (audited) |
| Open the cooldown heatmap | `dashboard:read` |
| Create, update, delete an identity type | `site:write` |
| Store a payload referencing a new secret | `secret:reveal` (users) or a matching `secret:read` scope (tokens) |

All of them are checked per site. An API token with the scope `identity:write[:<site>]` carries
`identity:read`, `identity:write` and `identity:operate` on that site, which is exactly what a
credential-refresh service needs. See [Tenants, users and tokens](./11-access-control.md).

---

## Limits

| Thing | Limit |
| --- | --- |
| Fields per identity type | 128 |
| `unique_by` paths | 16 |
| Entries per delivery segment | 256 |
| One delivery template | 8 KiB |
| Type YAML | 1 MiB |
| Preview sample payload | 1 MiB |
| Cookies per `cookie_map` | 1024 (name ≤ 1 KiB, value ≤ 16 KiB) |
| Tags per identity | 32 (each ≤ 64 bytes, `^[a-zA-Z0-9_.:-]{1,64}$`) |
| Labels per identity | 32 (key ≤ 64 bytes, value ≤ 256 bytes) |
| Region | 64 bytes |
| Account external reference | 256 bytes |
| Account notes | 4096 bytes |
| Operation reason | 512 bytes |
| IDs per `OperateIdentities` | 1000 |
| Identities per `BulkOperateIdentities` | 100 000 |
| Affected IDs returned by a revert | 1000 |
| Rows / bytes per import | 50 000 / 32 MiB |
| Payload versions kept | 5 |
| Page size | 50 by default, 500 maximum |

---

## Next

- [Policies](./08-policies.md) — the rules that decide the cooldowns, bans and quarantines this page
  shows you how to undo.
- [Proxies](./07-proxies.md) — the other half of a lease, and the usual suspect when a whole batch
  of identities goes bad at once.
- [Node API reference](./13-node-api.md) — how a node acquires an identity and reports the result.
- [Secret vault](./10-secrets.md) — what a `secret_ref` field points at.
- [Observability and alerting](./12-observability.md) — the dashboards, risk events and alerts that
  tell you a pool is degrading before the nodes do.
- [Operations runbook](./16-operations.md) — hot-state rebuild, retention and the day-2 procedures
  behind the operations here.
