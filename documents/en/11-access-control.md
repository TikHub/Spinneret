# Tenants, users and tokens

**The complete access-control model of Spinneret: what tenants and namespaces isolate, how users get
permissions, what an API token may do, how sessions work, and what the audit log records.**

[中文](../zh/11-access-control.md)

---

## Contents

- [The two boundaries](#the-two-boundaries)
- [A worked example: two teams on one server](#a-worked-example-two-teams-on-one-server)
- [Platform administrators](#platform-administrators)
- [Users and role bindings](#users-and-role-bindings)
- [Roles](#roles)
- [Site-scoped bindings](#site-scoped-bindings)
- [The permission list](#the-permission-list)
- [API tokens](#api-tokens)
- [Token scopes](#token-scopes)
- [IP allowlists, rate limits and expiry](#ip-allowlists-rate-limits-and-expiry)
- [Revocation](#revocation)
- [Tokens that create tokens](#tokens-that-create-tokens)
- [Sessions, cookies and CSRF](#sessions-cookies-and-csrf)
- [The audit log](#the-audit-log)
- [Hardening checklist](#hardening-checklist)

---

## The two boundaries

Spinneret has exactly two containers, and they nest:

- A **tenant** is the hard isolation boundary — a company, a customer or an organization. Nothing is
  shared between tenants except the server process, the database and the key-encryption keys the
  operator manages.
- A **namespace** is a partition inside one tenant — an environment (`prod`, `staging`) or a business
  line. Namespaces are the working unit: almost every object belongs to exactly one.

Which container owns what:

| Object | Belongs to | Notes |
| --- | --- | --- |
| Namespace | tenant | `namespaces (tenant_id, name)` is unique |
| Site | namespace | endpoint groups, URI rules and identity types hang off the site |
| Identity, account, payload | site | therefore namespace, therefore tenant |
| Proxy | namespace | never shared between namespaces |
| Policy and policy binding | namespace | bindings may narrow to a site or endpoint group |
| Config item and version | namespace | `(namespace_id, group_name, key)` is unique |
| Secret and secret version | namespace | `(namespace_id, path)` is unique |
| API token | namespace | one token, one namespace, forever |
| User account | global | a login, not an authorization |
| Role binding | tenant | optionally narrowed to one namespace and some of its sites |
| Notification channel | tenant, optionally namespace | a channel with no namespace covers the tenant |
| Audit entry | tenant, usually namespace | tenant-level operations carry an empty namespace |
| KEK, system settings | platform | shared by every tenant; only platform admins touch them |

Two consequences worth stating plainly:

1. **A user account is not access.** Creating a user grants nothing. The role binding grants
   everything. The same account can be an `owner` in one tenant and have no access at all in another.
2. **A token never crosses a namespace.** Its tenant and namespace are fixed at creation and cannot
   be changed. To give a node access to two namespaces, give it two tokens.

See [Concepts](./04-concepts.md) for how these objects relate at request time.

---

## A worked example: two teams on one server

One tenant, `acme`, with two namespaces: `prod` and `staging`. Two users:

```text
tenant acme
├── namespace prod       sites: example-site, partner-api
└── namespace staging    sites: example-site

user  mei    binding: owner    on tenant acme   (no namespace)     → both namespaces
user  dana   binding: operator on namespace staging                → staging only
```

What `dana` — an operator pinned to `staging` — can and cannot do with `prod`:

| Thing in `prod` | Can `dana`? | Why |
| --- | --- | --- |
| See `prod` in the namespace switcher | No | `namespace:read` is granted only on `staging` |
| List sites, endpoint groups, identity types | No | the binding's namespace does not match |
| List or open identities, accounts | No | site resources inherit the namespace check |
| Reveal an identity payload | No | `identity:reveal` is not in `operator` and not on `prod` |
| List proxies | No | proxies are namespace-level objects of `prod` |
| Read config items, read or list secrets | No | `config:read` and `secret:list` are checked on `prod` |
| List or create API tokens | No | `token:read` / `token:write` are checked on `prod` |
| Read `prod` audit entries | No | the audit query is restricted to the readable namespaces |
| See request/lease statistics of `prod` | No | `dashboard:read` is checked per namespace |
| Change the `acme` tenant, add users, grant roles | No | those need a tenant-wide binding |

And inside `staging`, `dana` can read everything a viewer reads plus operate identities, proxies,
breakers, and edit policy and config drafts — but not publish them, not write or reveal secrets, and
not manage tokens: those are `admin` rights. See [Roles](#roles).

Now add a second tenant, `partner-co`. A user with bindings only in `acme` cannot address
`partner-co` at all: selecting it as the active tenant fails with `permission_denied` before any
handler runs, and `partner-co` never appears in the tenant switcher. Nothing in the tenant —
identities, payloads, proxies, config, secrets, tokens, requests, audit — is reachable.

**Note.** Isolation is enforced in the server, not in the console. Every RPC re-checks the
principal's permissions against the resource; the console only decides what to draw.

---

## Platform administrators

A platform administrator is a user with `is_platform_admin` set. They:

- pass **every** permission check in **every** tenant, including `tenant:manage` and `kek:manage`,
  which nobody else can hold;
- see every tenant in `GetMe`, and may select any existing tenant as the active tenant;
- may list every user (`all_users`), manage users who hold bindings in tenants they are not bound to,
  and administer other platform administrators;
- may create a user that already exists and just add a binding to it, where a tenant owner gets
  `already_exists`.

The active tenant of a platform administrator is a UI selection, not a security boundary. A platform
administrator with **no** active tenant selected is still useful: they can manage tenants and read
the platform-level audit entries (sign-ins and KEK operations, which carry an empty tenant).

The **first** platform administrator is created by the CLI, together with a tenant, a namespace and
the built-in default policies, in one transaction:

```bash
SPINNERET_ADMIN_PASSWORD='…' spnr admin init \
  --username admin \
  --password-env SPINNERET_ADMIN_PASSWORD \
  --tenant default \
  --namespace default
```

The command is idempotent: when a platform administrator already exists it prints
`already initialized` and exits 0. There is **no API, RPC or console action that promotes a user to
platform administrator**, and `spnr admin init` will not create a second one — it just reports
`already initialized`. Promoting a second one is a deliberate database operation, and should be
treated as such. See [CLI reference](./15-cli.md).

---

## Users and role bindings

A user is a global console account:

| Field | Meaning |
| --- | --- |
| `id` | `usr_…` |
| `username` | unique, lower-case, 3–64 characters of `a-z 0-9 . _ -`, starting with a letter or digit |
| `display_name` | free text, at most 128 characters |
| `email` | optional, at most 254 characters |
| `locale` | preferred console language (`en`, `zh-CN`); empty follows the browser |
| `is_platform_admin` | see above |
| `disabled` | cannot sign in; disabling ends every session immediately |
| `last_login_at`, `created_at` | timestamps |

Profile fields are global: editing a display name on the **Users** page changes it in every tenant.

A **role binding** is what grants access. It has four parts:

```text
binding = user + tenant + role [+ namespace] [+ sites] [+ extra permissions]
```

| Part | Effect |
| --- | --- |
| `role` | `owner`, `admin`, `operator` or `viewer` — the permission set |
| `namespace` | empty = every namespace of the tenant and the tenant-level resources; set = that namespace only |
| `sites` | empty = every site; set = only those sites of that namespace (at most 500, and they require a namespace) |
| `extra_permissions` | single sensitive permissions added on top of the role |

A user may hold several bindings, in one tenant or across tenants; they are additive, and a check
passes when **any** binding allows it. A user with no binding in a tenant cannot select that tenant.

Only four permissions may be added as extras — `config:publish`, `secret:reveal`, `identity:reveal`
and `policy:publish` — which lets you give an operator exactly one admin-grade right without making
them an admin. Anything else is rejected with `invalid_argument`.

### Managing users and bindings

Users, bindings and tenant-level notification channels are **tenant-level resources**: they can only
be managed through a binding that covers the whole tenant (no namespace, no site list). A
namespace-pinned owner runs their namespace but cannot add a single user.

Further rules the server enforces:

- Granting or removing an `owner` binding requires a **tenant-wide owner** binding (or a platform
  administrator).
- The **last tenant-wide owner binding of a tenant cannot be deleted** (`failed_precondition`).
- An identical binding (same role, namespace, sites and extras) is rejected with `already_exists`.
- A tenant admin cannot manage a user who holds bindings in tenants they cannot manage, and cannot
  manage a platform administrator at all.
- You cannot disable your own account.
- Disabling a user or resetting their password ends all of their sessions.

RPCs: `AccessAdminService.ListUsers`, `CreateUser`, `UpdateUser`, `ResetPassword`,
`ListRoleBindings`, `CreateRoleBinding`, `DeleteRoleBinding`. Tenants and namespaces are managed by
`TenantAdminService`.

---

## Roles

Roles nest: `viewer ⊂ operator ⊂ admin ⊂ owner`.

| Role | Adds to the role below | In one sentence |
| --- | --- | --- |
| `viewer` | — | Reads scheduling state, config, dashboards, notifications and the audit log; lists secrets without their values. |
| `operator` | `identity:write`, `identity:operate`, `proxy:write`, `proxy:operate`, `policy:write`, `config:write`, `breaker:operate` | Day-to-day operations: import and operate identities and proxies, edit policy and config drafts, open and close breakers. |
| `admin` | `site:write`, `policy:publish`, `config:publish`, `secret:write`, `secret:reveal`, `identity:reveal`, `token:read`, `token:write`, `notify:write` | Changes what is published and holds the credentials: sites and identity types, publishing, secrets, reveals, API tokens, notification channels. |
| `owner` | `namespace:write`, `user:read`, `user:write` | Runs the tenant: creates and deletes namespaces, manages users and role bindings. |

`viewer` in full: `namespace:read`, `site:read`, `identity:read`, `proxy:read`, `policy:read`,
`breaker:read`, `config:read`, `secret:list`, `dashboard:read`, `audit:read`, `notify:read`.

Two permissions are held by nobody but platform administrators (`tenant:manage`, `kek:manage`), and
three are granted **only** through token scopes and never through a role (`lease:acquire`,
`report:write`, `secret:read`).

---

## Site-scoped bindings

Adding sites to a binding narrows it to those sites. The intent is a contractor or a squad that owns
two sites of a shared namespace.

A site-scoped binding grants its role's permissions on the **listed sites** only. On namespace-level
objects — proxies, config items, secrets, API tokens, the namespace itself — it grants nothing, with
two deliberate exceptions:

| Exception | Why |
| --- | --- |
| `namespace:read` | without it the namespace would not appear in the switcher and the user could not reach their own sites |
| `proxy:read` | identities are acquired through proxies; a site operator has to see which proxy an identity used |

Both exceptions apply **only when the binding is pinned to a namespace**. A tenant-wide binding with
a site list gets neither, because the server cannot tie a site list to a namespace without extra I/O.

So a site-scoped operator on `example-site` in `staging` can operate that site's identities and read
the proxy pool, but cannot read a config item, list a secret, see a token or touch `partner-api`.

**Note.** Listing endpoints filter by site where they can. Where a filter cannot express the grant —
a token whose scope names a site, for example — the server falls back to checking each candidate
individually, so the result is the same, just computed differently.

---

## The permission list

Every permission is a `<resource>:<verb>` string. Checks fail closed: an unknown permission, an
unknown role, a malformed resource or a missing principal is denied.

| Permission | Allows | Console page |
| --- | --- | --- |
| `tenant:manage` | Create, update and delete tenants | Tenants |
| `kek:manage` | Read KEK status and start a rewrap | Secrets (KEK panel) |
| `namespace:read` | See a namespace and select it in the scope switcher | all pages |
| `namespace:write` | Create, update and delete namespaces | Tenants |
| `site:read` | List and open sites, endpoint groups, URI rules, identity types | Sites |
| `site:write` | Create, edit and delete sites, endpoint groups, URI rules and identity types | Sites, Identity Types |
| `identity:read` | List and open identities and accounts | Identities, Accounts, Identity Types |
| `identity:write` | Import, edit and update identities and their payloads, and create or update accounts | Identities, Accounts |
| `identity:operate` | Ban, cool down, quarantine, release, revert, bulk-operate identities and accounts | Identities, Identity detail |
| `identity:reveal` | See the decrypted payload of an identity | Identity detail |
| `proxy:read` | List and open proxies | Proxies |
| `proxy:write` | Import, edit and delete proxies | Proxies |
| `proxy:operate` | Enable, disable, cool down and health-check proxies | Proxies |
| `policy:read` | List and open policies, versions and bindings | Policies |
| `policy:write` | Create policies and save drafts | Policies |
| `policy:publish` | Publish, roll back, delete, bind and unbind policies | Policies |
| `breaker:read` | See breaker state and history | Breakers, Overview |
| `breaker:operate` | Open and close breakers, pause and resume a site | Breakers |
| `config:read` | Read config groups, items and versions | Config Center |
| `config:write` | Create and edit config items and save drafts | Config Center |
| `config:publish` | Publish, roll back and delete config items and versions | Config Center |
| `secret:list` | List secret paths and metadata, never values | Secrets |
| `secret:write` | Create, update and delete secrets and versions | Secrets |
| `secret:reveal` | Read a secret value in the console | Secrets |
| `token:read` | List API tokens (never their secrets) | Tokens |
| `token:write` | Create and revoke API tokens | Tokens |
| `user:read` | List users and role bindings of the tenant | Users |
| `user:write` | Create and update users, reset passwords, grant and remove bindings | Users |
| `audit:read` | Query the audit log | Audit |
| `notify:read` | List notification channels and alert events | Notifications |
| `notify:write` | Create, edit, delete and test notification channels | Notifications |
| `dashboard:read` | Read dashboards, the cooldown heatmap, the request explorer and risk events | Overview, Heatmap, Requests, Risk Events |
| `lease:acquire` | `Acquire`, `AcquireBatch`, `Renew`, `Release` | node only — no console page |
| `report:write` | `Report`, `ReportBatch` | node only — no console page |
| `secret:read` | `GetSecret` and `${secret:…}` resolution inside config | node only — no console page |

A console navigation entry is shown when its permission is granted anywhere in the active namespace;
**Users** is the exception and needs a tenant-wide grant. See
[Console overview](./05-console-overview.md).

---

## API tokens

A token is what a crawler node authenticates with. It is bound to one tenant and one namespace, it
carries scopes, and it may do what its scopes allow and nothing else.

### Creating one in the console

**Access › Tokens**, with `token:write` on the namespace. The form takes a name (unique among the
namespace's non-revoked tokens, at most 64 characters), a description, the scopes, an optional IP
allowlist, an optional rate limit and an expiry (the form defaults to `90d`).

The response contains the plaintext **once**:

```text
spn_3Qw9…                       (47 characters: "spn_" + 43 base62 characters)
```

Spinneret stores only the SHA-256 digest of the token and its first 12 characters (`token_prefix`,
including `spn_`) for identification. There is no way to display it again. If it is lost, revoke the
token and create a new one.

### Creating one from the command line

Useful for bootstrapping a node before anyone has signed in to the console. It needs only
`SPINNERET_DATABASE_URL` and performs no permission check — running it means having the database:

```bash
spnr token create \
  --tenant default \
  --namespace default \
  --name crawler-node-01 \
  --scope lease:acquire \
  --scope report:write \
  --scope config:read \
  --expires 720h
```

Only the plaintext token is printed on stdout. `--expires` accepts durations such as `720h` or `30d`
and defaults to `720h`; `0` or `never` creates a token that does not expire. Tokens created this way
are recorded with `created_by = system:cli`.

### Using one

```bash
curl -sS https://spinneret.example.com/spinneret.v1.NodeService/Acquire \
  -H 'Authorization: Bearer spn_3Qw9…' \
  -H 'Content-Type: application/json' \
  -H 'X-Spinneret-Node: crawler-node-01' \
  -d '{"site":"example-site","client":"web","uri":"/search"}'
```

The `X-Spinneret-Node` header is optional and names the calling instance in the request explorer and
the per-node statistics (leases and reports). It is not recorded in the audit log. See
[Node API reference](./13-node-api.md).

### Token fields

| Field | Meaning |
| --- | --- |
| `id` | `tok_…` |
| `namespace` | the namespace the token is bound to; fixed |
| `name` | unique among non-revoked tokens of the namespace — revoking frees the name for reuse |
| `token_prefix` | first 12 characters, for identification in listings and audit details |
| `scopes` | see below; at least one, at most 64 |
| `ip_allowlist` | IPs or CIDRs; empty means any address; at most 256 entries |
| `rate_limit_rps` | per server instance; `0` means unlimited; at most 1 000 000 |
| `expires_at` | must be in the future when set; null means no expiry |
| `revoked_at` | null while usable |
| `last_used_at`, `last_used_ip` | flushed to the database every 30 seconds, so they lag slightly |
| `created_by` | `user:<id>`, `token:<id>` or `system:cli` |

---

## Token scopes

A scope is a name, optionally followed by `:` and one argument.

| Scope | Argument | Grants |
| --- | --- | --- |
| `lease:acquire[:<site>]` | exact site name, optional | `lease:acquire` |
| `report:write[:<site>]` | exact site name, optional | `report:write` |
| `config:read[:<group glob>]` | glob over the config group, optional | `config:read` |
| `config:publish[:<group glob>]` | glob over the config group, optional | `config:read`, `config:write`, `config:publish` |
| `secret:read:<namespace>/<path glob>` | **required** | `secret:read` |
| `identity:write[:<site>]` | exact site name, optional | `identity:read`, `identity:write`, `identity:operate` |
| `proxy:write` | none | `proxy:read`, `proxy:write`, `proxy:operate` |
| `admin` | none | every permission of the `admin` role, inside the token's namespace |

Rules the parser enforces:

- Omitting an optional argument widens the scope to **every** site or **every** group.
- Site arguments are compared **exactly** and must not contain `*` or `?`; omit the argument to allow
  all sites. They must also match the site-name pattern `^[a-z0-9_][a-z0-9_.-]{0,63}$`.
- `secret:read` **must** carry an argument, and its glob is matched against
  `"<namespace name>/<secret path>"`. The namespace part must be the token's own namespace.
- Scopes must not contain whitespace, control characters or invalid UTF-8; duplicates are rejected.
- At most 64 scopes per token; the API accepts up to 256 characters per scope (the parser's hard
  limits are 512 bytes per scope and 256 bytes per argument).

### Glob syntax

Globs have exactly two metacharacters:

| Token | Matches |
| --- | --- |
| `*` | any sequence of characters, including the empty sequence and `/` |
| `?` | exactly one character |

Everything else — including `[`, `]` and `\` — is a literal. There are no character classes and no
escaping. Matching never backtracks exponentially — the worst case is O(pattern × input) with no
allocation — so a hostile pattern cannot blow up the server.

```text
config:read:crawler*            matches groups crawler, crawler.hk, crawlerx
secret:read:prod/signing/*      matches prod/signing/api_key, prod/signing/a/b
secret:read:prod/*              matches every secret of namespace prod
lease:acquire:example-site      matches only the site named example-site
lease:acquire                   matches every site of the namespace
```

Secret paths are canonicalised before matching: an empty path, a leading or trailing `/`, and `.` or
`..` segments are rejected, so a glob can never be satisfied through path aliasing.

### Presets in the console

The scope builder offers three starting points:

| Preset | Scopes |
| --- | --- |
| Crawler node | `lease:acquire`, `report:write`, `config:read` |
| Cookie refresher | `identity:write` |
| CI publisher | `config:publish` |

### Choosing scopes

Give every node its own token and only the scopes its job needs. A token that only fetches leases and
reports outcomes does not need `config:publish`, and almost never needs `admin`. Grant `secret:read`
only to nodes that actually read secrets — directly through `GetSecret`, or because their config
items carry `${secret:…}` references the server resolves on their behalf; paths the glob does not
cover are refused. See [Secret vault](./10-secrets.md) and
[Configuration center](./09-config-center.md).

Calls outside the scopes fail with `permission_denied` and reason `scope_missing`. The message names
the permission only, never the resource, so it cannot be used to probe what exists.

---

## IP allowlists, rate limits and expiry

**IP allowlist.** Entries are bare addresses (`203.0.113.7`, `::1`, which become single-address
prefixes) or CIDR ranges (`10.0.0.0/8`, `2001:db8::/32`). An empty allowlist allows any address. A
request from outside the list is rejected with `permission_denied` / `ip_not_allowed`.

The client address is the TCP peer address, unless the peer is inside `SPINNERET_TRUSTED_PROXIES`, in
which case `X-Forwarded-For` (walked from the right, skipping trusted hops) and then `X-Real-IP` are
honoured. **If you run behind a reverse proxy and do not set `SPINNERET_TRUSTED_PROXIES`, every
allowlist will see the proxy's address, not the client's.** See
[Configuration reference](./03-configuration.md).

**Rate limit.** `rate_limit_rps` is a token bucket **per server instance**, with one second of burst.
With three replicas behind a load balancer, a limit of 100 allows up to 300 req/s in the worst case.
Exceeding it returns `resource_exhausted` with reason `rate_limited` and a
`Spinneret-Retry-After-Ms` header. `0` disables the limit.

**Expiry.** `expires_at` must be in the future. An expired token fails with `unauthenticated` /
`token_expired`. Expiry is the cheapest way to bound the damage of a leaked token; prefer a finite
expiry and a rotation routine over a permanent token.

**Rotation.** There is no RPC that deletes a token. Rotating one means: create the new token, roll it
out, then revoke the old one. Because the uniqueness constraint only covers non-revoked tokens, the
new token can reuse the old name once the old one is revoked.

---

## Revocation

Revoking a token is immediate and irreversible:

1. `revoked_at` is set in the database.
2. A `token.revoked` event is published on the Redis/Valkey event bus.
3. Every server instance drops the token from its verification cache when it receives the event.
4. Requests carrying it fail with `unauthenticated` and reason `token_revoked`.

A running node sees its next call fail; it does not get a grace period, and retrying does not help.
Nodes should treat `token_revoked`, `token_expired` and `token_invalid` as fatal and stop, rather
than retry — see [Node API reference](./13-node-api.md).

If the event bus is unavailable, revocation still takes effect on every instance once its cached
verification expires: `SPINNERET_TOKEN_CACHE_TTL`, 30 seconds by default. Unknown token hashes are
negatively cached for 5 seconds. Revoking an already revoked token is a no-op.

---

## Tokens that create tokens

The `admin` scope includes `token:write`, so a token holding it can mint further tokens. To stop that
from becoming a privilege escalation, a token created **by a token** can never be broader than its
parent. The parent row is re-read from the database at creation time, so the current limits apply and
a token revoked or expired since it authenticated cannot create anything.

| Rule | Failure |
| --- | --- |
| The `admin` scope cannot be granted to the child | `permission_denied` |
| The parent is revoked or expired | `token_revoked` / `token_expired` |
| The parent expires, so the child must too, no later than the parent | `permission_denied`, naming the parent's expiry |
| The parent has an IP allowlist, so the child needs a non-empty one, and every child prefix must lie inside a parent prefix | `permission_denied`, naming the offending entry |
| The parent is rate limited, so the child must be, at no more than the parent's rate | `permission_denied`, naming the parent's limit |

An empty parent allowlist allows every address, so any child allowlist is accepted. Prefixes of
different address families never contain each other, so an IPv6 child under an IPv4-only parent is
rejected.

Users and the CLI are not restricted this way: a console `admin` can create any token the namespace
allows.

---

## Sessions, cookies and CSRF

The console signs in with `AuthService.Login`, the only RPC that does not require authentication.

**Passwords.** At least 10 characters, at most 1024. Hashed with Argon2id — 64 MiB of memory, 3
iterations, parallelism 2, a 16-byte salt and a 32-byte key. Verification runs under a concurrency
limit so that a burst of sign-ins cannot exhaust memory, and an unknown username is verified against
a dummy hash so that timing does not reveal whether an account exists. Every failure returns the same
message.

**Login throttling.** Counted in Redis/Valkey over a 15-minute window: 5 failures per username and 20
per client IP. Exceeding either returns `resource_exhausted` / `login_throttled` with the retry delay.
A successful sign-in clears the username counter and releases the IP slot; an administrative password
reset clears the username counter as well.

**The session cookie.**

| Property | Value |
| --- | --- |
| Name | `spinneret_session` |
| Value | 43 characters, base64url of 32 random bytes |
| Attributes | `HttpOnly`, `SameSite=Strict`, `Path=/`, `Max-Age` = session TTL |
| `Secure` | per `SPINNERET_COOKIE_SECURE`: `auto` (default — set when the request arrived over TLS, directly or through a trusted proxy sending `X-Forwarded-Proto: https`), `true`, `false` |
| Lifetime | `SPINNERET_SESSION_TTL`, 12 hours by default, minimum 1 minute |
| Storage | server-side in Redis/Valkey, keyed by the SHA-256 of the cookie value — the cookie itself is only a random string |

The expiry **slides**: when less than half the TTL remains, the server extends the session and sends
the cookie again on the same response.

**CSRF.** Every cookie-authenticated request with an unsafe method (anything but `GET`, `HEAD`,
`OPTIONS`) must carry `X-Spinneret-CSRF: 1`. Missing it is rejected with `permission_denied` and
reason `csrf_missing`. A cross-site form cannot set a custom header, and `SameSite=Strict` already
keeps the cookie off cross-site requests; the header is the second lock. API tokens authenticate with
`Authorization: Bearer` and are not subject to CSRF.

**The active tenant.** Console requests select their tenant with `X-Spinneret-Tenant: <tenant id>`.
`GET` requests that cannot set headers (the SSE event stream) use the `tenant` query parameter
instead. Selecting a tenant the user has no binding in fails with `permission_denied`.

**What ends a session.**

| Event | Effect |
| --- | --- |
| Sign out | that session only |
| Change own password | every **other** session of the user; the current one is kept |
| Administrative password reset | every session of the user |
| Disabling the account | every session of the user |
| TTL expiry | that session |

Password changes are enforced twice: the session index is cleared, and each session records the
password generation it was created with, so a session that escaped the sweep is still rejected.

---

## The audit log

Every security-relevant operation is recorded: administrative mutations, secret reads and reveals,
sign-ins and sign-outs, and denied attempts at sensitive operations. Writes are buffered and flushed
in batches, so a handler never blocks on the audit table.

### Fields

| Field | Meaning |
| --- | --- |
| `id` | `aud_…` |
| `created_at` | when the operation happened |
| `tenant_id` / `namespace` | the tenant, and the namespace when the operation had one; empty namespace means a tenant-level operation |
| `actor_kind` | `user`, `token` or `system` (a background job) |
| `actor_id` | `usr_…`, `tok_…`, or empty for `system` |
| `actor_name` | username, token name or job name |
| `action` | e.g. `secret.read`, `identity.bulk_operate`, `config.publish` |
| `resource_kind` / `resource_id` / `resource_name` | what was acted on |
| `result` | `ok`, `denied` or `error` |
| `ip` | client address, resolved through the trusted-proxy rules |
| `user_agent` | client user agent |
| `details` | operation-specific JSON — never contains a secret value or a payload |

### Actions

| Area | Actions |
| --- | --- |
| Authentication | `auth.login`, `auth.logout`, `user.change_password` |
| Users and access | `user.create`, `user.update`, `user.reset_password`, `role_binding.create`, `role_binding.delete`, `token.create`, `token.revoke` |
| Tenancy | `tenant.create`, `tenant.update`, `tenant.delete`, `namespace.create`, `namespace.update`, `namespace.delete` |
| Sites | `site.create`, `site.update`, `site.delete`, `site.pause`, `site.resume`, `endpoint_group.create`, `endpoint_group.update`, `endpoint_group.delete`, `uri_rules.replace` |
| Identities | `identity_type.create`, `identity_type.update`, `identity_type.delete`, `identity.import`, `identity.update`, `identity.update_payload`, `identity.reveal`, `identity.bulk_operate`, `identity.revert_actions`, `account.upsert` |
| Proxies | `proxy.import`, `proxy.update`, `proxy.delete`, `proxy.check`, `proxy.<operation>` |
| Policies | `policy.create`, `policy.save_draft`, `policy.publish`, `policy.rollback`, `policy.delete`, `policy.bind`, `policy.unbind` |
| Config | `config.create`, `config.draft.save`, `config.publish`, `config.rollback`, `config.delete`, `config.read` (denials only) |
| Secrets | `secret.create`, `secret.update`, `secret.delete`, `secret.reveal`, `secret.read` |
| Breakers | `breaker.open`, `breaker.close` |
| Notifications | `notify.channel.create`, `notify.channel.update`, `notify.channel.delete`, `notify.channel.test` |
| Platform | `kek.rewrap_start` |

Every secret read and reveal is recorded with its version and client IP, whether it succeeded or was
denied, and a node read also records the purpose the caller declared. That is the record you will want
after an incident.

### Reading it

**Access › Audit**, or `AccessAdminService.ListAuditLogs`. Filters — namespace, actor, action,
resource kind, resource ID, result — are **exact matches**, not substrings; `actor` matches either
the actor ID or the actor name. The API bounds each filter: namespace and resource kind at most 64
characters, actor, action and resource ID at most 128, and `result` must be `ok`, `denied` or `error`.
The time range defaults to the last 24 hours, entries come newest first, the page size defaults to 50
and is capped at 500.

Who sees what:

| Principal | Sees |
| --- | --- |
| A namespace filter is given | requires `audit:read` on that namespace |
| Tenant-wide `audit:read` | every entry of the active tenant, tenant-level entries included |
| `audit:read` only in some namespaces | the entries of those namespaces; `permission_denied` when there are none |
| Platform administrator with no active tenant | the platform-level entries whose tenant is empty (sign-ins, KEK operations) |

### Durability and retention

Entries go through a bounded in-memory buffer (50 000 entries) flushed every second in batches of
500. If the buffer fills — a flood of activity while the database is slow — entries are **dropped**
rather than taking the API down, and the drop is logged and counted. A batch the database rejects is
split until the offending entry is isolated, so one bad entry only loses itself.

`audit_logs` is partitioned by month. The `partition_manager` leader job runs hourly, creates the
upcoming partitions and drops the expired ones. Retention is `SPINNERET_RETENTION_AUDIT`, 8760h (one
year) by default. If you need the log beyond that, export it before the partition is dropped — see
[Operations runbook](./16-operations.md).

---

## Hardening checklist

**Who should hold what**

- [ ] Platform administrator: as few people as possible, ideally two. They bypass every tenant
      boundary and hold `kek:manage`.
- [ ] Tenant-wide `owner`: the people responsible for the tenant's access. `owner` is the only role
      that can grant `owner`, and the last one cannot be removed.
- [ ] `admin`: people who publish and who hold credentials. `secret:reveal` and `identity:reveal`
      come with it — that is the point of the role, and the reason not to hand it out widely.
- [ ] `operator`: the default for people doing day-to-day work. When one of them occasionally needs
      to publish config, add `config:publish` as an extra permission instead of promoting them.
- [ ] `viewer`: everyone else, including dashboards-only stakeholders.
- [ ] Contractors and squads: a `viewer` or `operator` binding pinned to one namespace and a site
      list. Confirm they cannot read config or secrets — site-scoped bindings grant only
      `namespace:read` and `proxy:read` at namespace level.

**Tokens**

- [ ] One token per node or per job — never a shared "fleet token".
- [ ] Scopes narrowed to the sites, config groups and secret paths actually used.
- [ ] `admin` scope: not used, unless a tool genuinely administers the namespace.
- [ ] `secret:read` only where secrets are actually read, with the tightest glob that works.
- [ ] An expiry on every token, and a rotation routine that beats it.
- [ ] An IP allowlist wherever node addresses are stable, with `SPINNERET_TRUSTED_PROXIES` correct.
- [ ] A rate limit on every token, remembering it is per instance.

**Sessions and operations**

- [ ] `SPINNERET_COOKIE_SECURE=true` (or `auto` behind TLS) in any deployment reachable over a
      network.
- [ ] `SPINNERET_SESSION_TTL` shortened from 12 hours if the console is exposed beyond a trusted
      network.
- [ ] Departures: disable the account (which ends its sessions) and revoke the tokens it created.
- [ ] A recurring review of `secret.read`, `secret.reveal` and `identity.reveal` audit entries.
- [ ] Audit retention long enough for your incident process, with an export if the year default is
      not enough.

See [Security](./19-security.md) for the threat model behind these choices.

---

## Next

- [Security](./19-security.md) — the threat model, what the software protects and what you are
  responsible for.
- [Secret vault](./10-secrets.md) — envelope encryption, KEK rotation and what `secret:reveal` costs.
- [Node API reference](./13-node-api.md) — how a node authenticates and what each error reason means.
- [CLI reference](./15-cli.md) — `spnr admin init`, `spnr token create` and the rest.
- [Console overview](./05-console-overview.md) — the scope switcher and which pages appear for which
  permission.
