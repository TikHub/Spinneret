# Security

**What Spinneret protects, how it protects it, and what stays your responsibility. Read this before you put real credentials into the system, and again before you expose the console to anything wider than a private network.**

[中文](../zh/19-security.md)

---

## Contents

- [Threat model](#threat-model)
- [Trust boundaries](#trust-boundaries)
- [Encryption at rest](#encryption-at-rest)
- [The KEK is your responsibility](#the-kek-is-your-responsibility)
- [Console authentication and sessions](#console-authentication-and-sessions)
- [API token security](#api-token-security)
- [Operations that hand out a plaintext](#operations-that-hand-out-a-plaintext)
- [The audit trail](#the-audit-trail)
- [Transport security and reverse proxies](#transport-security-and-reverse-proxies)
- [Multi-tenant isolation](#multi-tenant-isolation)
- [Input validation and request limits](#input-validation-and-request-limits)
- [Dependencies and supply chain](#dependencies-and-supply-chain)
- [Hardening checklist](#hardening-checklist)
- [Reporting a vulnerability](#reporting-a-vulnerability)

---

## Threat model

Spinneret is a credential store with a scheduler attached. It holds cookies, device parameters,
account passwords, proxy URLs with embedded credentials, signing keys and API keys, and it hands
them out to machines over the network hundreds of times per second. That is the asset. Everything
in this page exists to keep that asset from leaving through a door you did not intend.

### Who the attackers are

| Attacker | Capability assumed | What the software does about it |
| --- | --- | --- |
| Network attacker | Can observe or modify traffic between a node and the server, or between a browser and the server | Nothing on its own — Spinneret speaks plain HTTP unless you give it a certificate or put it behind a TLS terminator. See [Transport security](#transport-security-and-reverse-proxies). |
| Stolen node token | Holds one plaintext API token taken from a compromised crawler node | The token is bound to one tenant and one namespace, limited to its scopes, optionally to an IP allowlist and a rate limit, and can be revoked within seconds on every instance |
| Curious or malicious console user | Has a valid console login with a low-privilege role | Permission checks fail closed on every service call; every reveal is audited; a viewer cannot read a secret plaintext or an identity payload |
| Database reader | Has a copy of the PostgreSQL data (a dump, a stolen disk, a replica) without the KEK | Identity payloads, proxy URLs, secret values and notification channel configurations are AES-256-GCM ciphertext and are not recoverable |
| Database reader with the KEK | Has both the data and the key file | Everything is recoverable. The KEK file is the single point of failure and must be protected as such |
| Browser-side attacker | Tries XSS, clickjacking or cross-site requests against a logged-in console user | `HttpOnly` + `SameSite=Strict` session cookie, a required CSRF header on unsafe methods, a strict Content-Security-Policy with no `unsafe-inline` scripts, `frame-ancestors 'none'` |
| Credential-stuffing attacker | Tries passwords against the console login | Argon2id hashing, per-username and per-IP throttling, one indistinguishable error for every failure |

### What Spinneret explicitly does not defend against

State these plainly to whoever approves your deployment:

- **A compromised host.** Anyone with root on the server, or with the ability to read process
  memory, can read the KEK, every unwrapped data key in the cache, and the plaintexts in flight.
- **A compromised node.** A node legitimately receives cookies, account credentials and proxy
  URLs in plaintext. Nothing stops a node that has been taken over from keeping them. Scope the
  token, allowlist the node's addresses, and treat a node compromise as a credential compromise.
- **An operator with the KEK.** There is no key escrow, no HSM integration and no split
  knowledge. Whoever can read `SPINNERET_KEK_FILE` can decrypt everything in the database.
- **A platform administrator.** Platform admins hold every permission in every tenant, including
  `kek:manage`. There is no configuration that restricts them.
- **Denial of service from authenticated callers.** Per-token rate limits and request-size caps
  exist, but there is no global admission control that keeps one busy tenant from crowding out
  another on a shared instance.
- **Anything about the target sites.** Spinneret performs no signing, no login flows and no
  captcha solving. What your nodes do with the credentials they receive is outside its model.

---

## Trust boundaries

| Boundary | Crossed by | Authenticated with | Notes |
| --- | --- | --- | --- |
| Node → server | `LeaseService`, `ReportService`, `ConfigService`, `SecretService` | `Authorization: Bearer spn_…` | Fixed tenant and namespace; scopes decide the rest |
| Browser → server | The console services (`*AdminService`, `AuthService`, `DashboardService`) | `spinneret_session` cookie + `X-Spinneret-CSRF: 1` | Active tenant selected by `X-Spinneret-Tenant`, checked against the user's role bindings |
| Browser → server (SSE) | `GET /api/v1/events/stream` | `spinneret_session` cookie only — `GET` is a safe method, so no CSRF header is required | Active tenant comes from the `?tenant=` query parameter, because an `EventSource` cannot set headers. It is checked the same way the header is |
| CLI → database | `spnr admin init`, `spnr token create`, `spnr kek …`, `spnr migrate`, `spnr seed`, `spnr rebuild` | Direct PostgreSQL connection (`SPINNERET_DATABASE_URL`) | **Bypasses the API and its permission checks entirely.** Whoever can run `spnr` against the database is a platform administrator |
| Server → PostgreSQL / Valkey / ClickHouse | Every request | Whatever the connection URL carries | Spinneret does not encrypt these connections itself; use `sslmode=verify-full` and network isolation |
| Server → KEK file | Process start only | Filesystem permissions | Read once into memory as AES-GCM instances; unwraps use those instances and never touch the filesystem again. Raw key bytes are zeroed after the key schedule is derived |
| Unauthenticated surface | `GET /healthz`, `GET /readyz`, `GET /metrics`, the console's static assets | None | `/metrics` exposes per-procedure counters and internal state; keep it on `SPINNERET_METRICS_ADDR` and off the public listener |

**Note.** `Principal.TenantID` for a console user is a UI selection, not a security boundary. The
role bindings are. Setting `X-Spinneret-Tenant` to a tenant you have no binding in returns
`permission_denied` — unless you are a platform administrator, who may select any tenant that
exists — and every permission check re-reads the bindings regardless of the header.

---

## Encryption at rest

Every sensitive value is sealed with **envelope encryption** (`internal/vault`):

```text
ciphertext  = nonce(12) || AES-256-GCM(DEK, nonce, plaintext, AAD)
wrapped DEK = nonce(12) || AES-256-GCM(KEK, nonce, DEK, "spinneret-dek:" + kekID)
```

- A fresh random 32-byte **DEK** is generated for every sealed value. It is never stored in
  plaintext; only its wrapped form is, next to the ciphertext and the id of the KEK that wrapped it.
- The data **AAD** is `<record id> || 0x00 || <field>` — for example `idt_…\x00payload:v3`. A
  ciphertext copied into another row or another column fails to authenticate.
- The DEK AAD binds a wrapped key to the KEK id, so relabelling a wrapped DEK with another id
  fails even when two ids happen to share key material.
- Every decryption failure wraps the same sentinel, `vault: decryption failed`, and reaches an API
  client only as a generic `internal` error. Server-side the messages do distinguish a ciphertext
  that fails to authenticate from a wrapped DEK that fails to unwrap, which is what you need to
  diagnose the cause; a KEK id the process does not hold is a separate error
  (`vault: unknown kek id`).

### What is encrypted

| Table | Encrypted columns | What it holds |
| --- | --- | --- |
| `identity_payloads` | `ciphertext`, `wrapped_dek`, `kek_id` | Cookies, tokens, device parameters, account passwords |
| `proxies` | `url_ciphertext`, `url_wrapped_dek`, `url_kek_id` | The full proxy URL including user and password |
| `secret_versions` | `ciphertext`, `wrapped_dek`, `kek_id` | Every secret value, every version |
| `notification_channels` | `config_ciphertext`, `config_wrapped_dek`, `config_kek_id` | Webhook URLs and channel credentials |
| `system_keys` | `ciphertext`, `wrapped_dek`, `kek_id` | Internal keys generated by the server (for example the report dedup pepper) |

### What is not encrypted

Assume anyone with database access can read all of this:

- **Metadata everywhere**: identity ids, states, health scores, site and client names, tags,
  cooldown and ban timestamps, the proxy's `display_url` (host and port, credentials stripped),
  secret paths, descriptions and expiry dates.
- **Config items.** Configuration values are stored in plaintext. Put anything sensitive in the
  vault and reference it with `${secret:...}` — see [Configuration center](./09-config-center.md).
- **The audit log**, including the IP addresses, user agents and `details` of every recorded
  operation.
- **Analytics in ClickHouse**: request outcomes, latencies, status codes, markers.
- **Passwords and API tokens** are not encrypted — they are hashed, which is the correct choice
  and means they cannot be recovered at all.

---

## The KEK is your responsibility

The key-encryption key is loaded at process start from `SPINNERET_KEK_FILE` (a file) or
`SPINNERET_KEKS` (an inline `id:base64,id2:base64` list), with `SPINNERET_KEK_CURRENT` selecting
which key wraps new data. A KEK is exactly 32 bytes, base64-encoded; an id must match
`^[a-zA-Z0-9_-]{1,32}$`.

```bash
# Generate a key line to put in the KEK file.
spnr kek generate --id k1
```

What the software guarantees:

- Key material never appears in an error message or a log line. A malformed id is not echoed back,
  because a malformed id is usually misplaced key material.
- The startup banner reports `keks` as a presence marker, never a value.
- The KEK file read is bounded at 1 MiB, so a misconfigured path fails fast.
- Raw DEK bytes are zeroed as soon as the AES key schedule is derived. Only derived AEAD instances
  live in the DEK cache (`SPINNERET_DEK_CACHE_SIZE`, default `100000`;
  `SPINNERET_DEK_CACHE_TTL`, default `10m`), and a positive TTL runs a purge goroutine so key
  material does not outlive it.

What you must do:

1. **Back the key up, off the machine that holds the database.** Data encrypted with a lost KEK is
   unrecoverable. There is no recovery path.
2. **Keep the backup and the database backup separate.** A backup archive containing both is
   equivalent to an unencrypted database.
3. **Restrict the file.** `scripts/compose-init.sh` creates `deploy/compose/secrets/kek.key` with
   mode `0644` so the distroless `nonroot` container user can read the bind mount. On a real
   deployment, tighten it to the uid the server runs as, or move it to your platform's secret
   store and mount it read-only.
4. **Rotate deliberately.** Add the new key, make it current, restart, then run
   `spnr kek rewrap`, and only remove the old key when `spnr kek status` shows no records still
   wrapped with it. Removing a KEK that still wraps data makes that data unreadable
   (`vault: unknown kek id`). The procedure is in [Operations runbook](./16-operations.md) and
   [Secret vault](./10-secrets.md).

---

## Console authentication and sessions

### Passwords

| Property | Value | Source |
| --- | --- | --- |
| Algorithm | Argon2id | `internal/auth/password.go` |
| Parameters | m = 64 MiB, t = 3, p = 2, 16-byte salt, 32-byte key | `DefaultArgon2Params` |
| Stored form | PHC string `$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>` in `users.password_hash` | |
| Comparison | Constant time (`crypto/subtle`) | |
| Minimum length | 10 characters | `MinPasswordLength` |
| Maximum length | 1024 characters | `MaxPasswordLength` |
| Character rules | Valid UTF-8; no composition rules | `ValidatePassword` |

There is no complexity requirement by design — length is the control. Concurrent Argon2id
computations are bounded by a semaphore (each one wants 64 MiB), so a burst of logins cannot
exhaust the machine's memory. A sign-in attempt for a username that does not exist still performs
a hash comparison against a dummy hash, so timing does not disclose account existence, and every
failure returns the same message: `invalid username or password`.

### Login throttling

| Dimension | Limit | Window | Error |
| --- | --- | --- | --- |
| Per username | 5 failures | 15 minutes | `login_throttled` (`resource_exhausted`) with a retry hint |
| Per client IP | 20 failures | 15 minutes | same |

Counters live in Redis and are **reserved before the password is verified**, so concurrent guesses
cannot exceed the limit. A successful sign-in clears the username counter and returns the IP slot.
An administrative password reset clears the username counter. `ChangePassword` goes through the
same throttle, so the change-password form cannot be used to brute-force the current password.

### Sessions

| Property | Value |
| --- | --- |
| Cookie name | `spinneret_session` |
| Value | 32 random bytes, base64url without padding (43 characters) |
| Stored as | Redis hash keyed by the hex SHA-256 of the cookie value — the raw value is never stored |
| Lifetime | `SPINNERET_SESSION_TTL`, default `12h`, sliding |
| Refresh | Extended (and the cookie re-sent) once less than half the TTL remains |
| Attributes | `HttpOnly`, `SameSite=Strict`, `Path=/`, `Max-Age = SessionTTL` |
| `Secure` | `SPINNERET_COOKIE_SECURE`: `auto` (default — set when the request arrived over TLS, or through a trusted proxy with `X-Forwarded-Proto: https`), `true`, `false` |

Each session records the user id, creation and expiry times, the client IP, a truncated user agent,
and a **password generation** (`cred`) — the user's `password_changed_at` in microseconds. A
session whose generation does not match the user's current one is rejected, even if it escaped
deletion. That is the mechanism behind the next table.

### What ends a session

| Event | Effect on sessions | Audit action |
| --- | --- | --- |
| Sign out | Ends that one session | `auth.logout` |
| Change own password | Keeps the session that made the change; **ends every other session of the user** | `user.change_password` |
| Administrative password reset | **Ends every session of the user**, and clears the login throttle for that username | `user.reset_password` |
| Account disabled | Authentication fails immediately (`session_invalid`, "account is disabled") | `user.update` |
| Session TTL expiry | Redis drops the hash | — |

Revocation is immediate on the instance that performed it, and a `user.changed` event propagates
to peers so they drop their cached copy at once; without the event bus, peers converge within the
5-second user cache TTL.

### CSRF

Cookie-authenticated requests with a method other than `GET`, `HEAD` or `OPTIONS` must carry:

```text
X-Spinneret-CSRF: 1
```

A missing header is rejected with `csrf_missing` (`permission_denied`). The header cannot be set
by a cross-site form or image request, and `SameSite=Strict` already prevents the cookie from being
sent cross-site; the header is the second lock. Bearer-token requests are exempt — they do not use
ambient credentials.

---

## API token security

### Format and storage

| Property | Value |
| --- | --- |
| Shape | `spn_` + 43 base62 characters (47 total), from 32 random bytes |
| Stored | SHA-256 digest in `api_tokens.token_hash` (unique), plus the first 12 characters in `token_prefix` for identification |
| Plaintext | Returned exactly once, by the call that created it. It is never stored and never shown again |
| Malformed credentials | Rejected by shape before any cache or database lookup |

The console says this in the creation dialog, and it is true: if a token is lost, revoke it and
create a new one. There is no recovery.

### Scopes

A token's authority is the union of its scopes. Every scope is checked against the resource of the
call, and unknown or structurally invalid scopes grant nothing.

| Scope | Argument | Grants |
| --- | --- | --- |
| `lease:acquire` | optional exact site name | `lease:acquire` |
| `report:write` | optional exact site name | `report:write` |
| `config:read` | optional config-group glob | `config:read` |
| `config:publish` | optional config-group glob | `config:read`, `config:write`, `config:publish` |
| `secret:read` | **required** glob over `<namespace name>/<path>` | `secret:read` |
| `identity:write` | optional exact site name | `identity:read`, `identity:write`, `identity:operate` |
| `proxy:write` | none | `proxy:read`, `proxy:write`, `proxy:operate` |
| `admin` | none | every permission of the `admin` role |

Rules the parser enforces: a scope is at most 512 bytes and its argument at most 256, with no
whitespace, control characters or invalid UTF-8; site-name arguments must not contain `*` or `?`
(omit the argument to allow every site); `secret:read` without an argument is rejected; duplicates
are rejected; at most 64 scopes per token. Secret path globs are matched only against canonical
relative paths — a path with an empty, `.` or `..` segment never matches, so a glob cannot be
satisfied through path aliasing.

`admin` is the one to be careful with. It grants the whole `admin` role inside the token's
namespace, including `secret:reveal` and `token:write`. Nodes never need it.

### IP allowlists, rate limits and expiry

| Control | Behaviour | Error |
| --- | --- | --- |
| IP allowlist | Up to 256 IP addresses or CIDR prefixes. Empty means any address. Checked against the *resolved client IP* — see [trusted proxies](#transport-security-and-reverse-proxies) | `ip_not_allowed` (`permission_denied`) |
| Rate limit | `rate_limit_rps`, 0 (unlimited) to 1,000,000. A token bucket with one second of burst, **per server instance** — a 3-replica deployment allows roughly 3 × the configured rate in total | `rate_limited` (`resource_exhausted`) with a retry hint |
| Expiry | Optional `expires_at`, must be in the future when set | `token_expired` (`unauthenticated`) |
| Revocation | Sets `revoked_at`; a `token.revoked` event drops the token from every instance's cache immediately | `token_revoked` (`unauthenticated`) |

Verification results are cached for `SPINNERET_TOKEN_CACHE_TTL` (default `30s`), and unknown token
hashes for 5 seconds. Without a working event bus, a revocation therefore takes effect on other
instances within the cache TTL rather than instantly. Last-used timestamps and IPs are buffered in
memory and flushed every 30 seconds, so a read of "last used" can lag by that much.

### Tokens cannot escalate

When an API token creates another token (`token:write` via the `admin` scope), the child is checked
against the parent's *current* database row — a parent revoked or expired since authentication
cannot create anything:

- The `admin` scope cannot be granted to the child.
- If the parent expires, the child must set `expires_at` and it must not be later than the parent's.
- If the parent has an IP allowlist, the child must have a non-empty one and every child prefix
  must lie inside a parent prefix.
- If the parent is rate limited, the child must be rate limited to at most the parent's rate.

All four failures are `permission_denied`.

---

## Operations that hand out a plaintext

This is the list to audit-review. Everything else returns metadata or masked values.

| Operation | Who can perform it | Audited as | Notes |
| --- | --- | --- | --- |
| `SecretAdminService.RevealSecret` | `secret:reveal` (role `admin` and above, or as an extra permission on a binding) | `secret.reveal` — ok, denied and error | The console shows the value for 60 seconds and warns that the reveal is recorded |
| `SecretService.GetSecret` | An **API token** with a matching `secret:read:<ns>/<glob>` scope. A user session is refused outright | `secret.read` — with version, purpose, result and client IP | Node-only path |
| `IdentityAdminService.GetIdentity` with `reveal` | `identity:reveal` (role `admin`, or as an extra permission) | `identity.reveal` — with site and payload version | Without it, sensitive fields come back as `••••` plus the last four characters; undeclared fields are fully masked |
| `LeaseService.Acquire` / `AcquireBatch` | Token scope `lease:acquire` | **Not audited** (hot path — leases are visible in the request explorer and lease state instead) | Returns the rendered credential and the full proxy URL *including* its credentials |
| `AccessAdminService.CreateToken` | `token:write` (role `admin` and above) | `token.create` — with scopes, allowlist, rate limit, prefix, expiry, never the plaintext | Plaintext returned once |
| `AccessAdminService.ResetPassword` | `user:write` (role `owner`), and platform admins for platform admins | `user.reset_password` | Caller chooses the new password; it is not mailed or generated |
| `spnr token create` | Anyone who can reach the database with `SPINNERET_DATABASE_URL` | **Not audited** — the actor is recorded as `system:cli` on the row, but no audit entry is written | Prints the plaintext on stdout |
| `spnr admin init` | Same | **Not audited**; objects are created with actor `system:bootstrap` | Creates the first platform administrator. Fails once one exists |

Two consequences worth stating to your reviewers: **the CLI is an unaudited administrative
path**, and **a node with `lease:acquire` receives real credentials by design**.

Proxy URLs are never returned to the console with their credentials — `Proxy.display_url` carries
host and port only — and proxy health-check error messages have the credentials redacted before
they are stored.

---

## The audit trail

Audit entries go into the partitioned `audit_logs` table and are readable in the console under
**Audit** (`audit:read`, held from role `viewer` upward, scoped to the namespaces the reader can
see).

### What an entry contains

`created_at`, `tenant_id`, `namespace_id`, `actor_kind` (`user` / `token` / `system`), `actor_id`,
`actor_name`, `action`, `resource_kind`, `resource_id`, `resource_name`, `result`
(`ok` / `denied` / `error`), `ip`, `user_agent`, and a JSON `details` object.

Actions recorded by the access and vault layers include `auth.login`, `auth.logout`,
`user.change_password`, `user.create`, `user.update`, `user.reset_password`,
`role_binding.create`, `role_binding.delete`, `token.create`, `token.revoke`, `secret.create`,
`secret.update`, `secret.delete`, `secret.reveal`, `secret.read` and `identity.reveal`. Other
domains add their own (identity, proxy, policy, config, site, tenant operations).

### What it guarantees

- **Denials are recorded, not just successes.** A refused reveal is written with
  `result = "denied"`, so a probing user leaves a trail.
- Entries are sanitised on the way in: invalid UTF-8 and NUL characters become U+FFFD, in text
  fields and inside `details`, so a hostile string cannot break the writer.
- A batch the database rejects is split until the offending entries are isolated; one bad entry
  loses only itself.
- The table has no application code that updates or deletes a row other than partition-level
  retention.

### What it does not guarantee

- **Writes are buffered and best-effort.** Entries go into a 50,000-slot in-process buffer flushed
  every second in batches of 500. If the buffer overflows, or the database stays unavailable past
  the retry budget, entries are **dropped and counted** rather than blocking the API. Audit
  logging must never take the control plane down — that is a deliberate trade, and it means the
  log is not a tamper-evident ledger.
- **It is not append-only at the database level.** Anyone with write access to PostgreSQL can
  change it. If you need tamper evidence, ship the entries to a write-once sink.
- **Retention is finite.** `SPINNERET_RETENTION_AUDIT` defaults to `8760h` (365 days); old
  partitions are dropped.

---

## Transport security and reverse proxies

### TLS

Spinneret serves **plain HTTP by default**. Two supported shapes:

1. **Terminate TLS in front** (recommended). The Compose stack does this with Caddy; any reverse
   proxy works. Set `SPINNERET_TRUSTED_PROXIES` so the server believes the forwarded headers.
2. **Terminate in the server.** Set `SPINNERET_TLS_CERT_FILE` and `SPINNERET_TLS_KEY_FILE`
   together (setting only one is a configuration error). The key pair is loaded at startup, so a
   broken certificate fails the boot instead of the first request. Minimum version is TLS 1.2, and
   HTTP/2 is enabled over TLS. Without a certificate the listener speaks HTTP/1.1 and cleartext
   HTTP/2.

### Getting the client IP right

Two things depend on the resolved client IP being correct: **token IP allowlists** and the
**per-IP login throttle**. Both are defeated by a misconfiguration here, in both directions.

`SPINNERET_TRUSTED_PROXIES` is a comma-separated list of CIDR prefixes. The rule:

- If the TCP peer is **not** inside a trusted prefix, forwarding headers are ignored entirely and
  the peer address is used. An attacker connecting directly cannot spoof `X-Forwarded-For`.
- If the peer **is** trusted, the `X-Forwarded-For` hops are walked from the right, skipping hops
  that are themselves trusted, and the first untrusted hop is used. If every hop is trusted, the
  leftmost is used. If `X-Forwarded-For` yields nothing usable, a single `X-Real-IP` is tried, and
  finally the peer address.

**Warning.** Leaving `SPINNERET_TRUSTED_PROXIES` empty behind a reverse proxy makes every request
appear to come from the proxy. IP allowlists then match the proxy, not the node, and the per-IP
login throttle throttles your whole deployment as one address. Setting it too widely — for example
trusting `0.0.0.0/0` — lets any client spoof its address. Trust exactly the proxy subnet.

`X-Forwarded-Proto: https` from a trusted peer also marks the request as secure, which is what
makes `SPINNERET_COOKIE_SECURE=auto` set the `Secure` attribute behind a terminating proxy. If your
proxy does not send it, set `SPINNERET_COOKIE_SECURE=true` explicitly.

The Compose stack ships `SPINNERET_TRUSTED_PROXIES: 172.16.0.0/12,10.0.0.0/8,192.168.0.0/16` —
correct for a private Docker network, and too wide the moment the server is reachable from other
private networks. Narrow it for a real deployment.

### Security headers and CSP

The console's static handler sets, on the HTML and asset responses:

| Header | Value |
| --- | --- |
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `DENY` |
| `Referrer-Policy` | `same-origin` |
| `Content-Security-Policy` | see below |

```text
default-src 'self'; script-src 'self' <sha256 of each inline script>;
style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:;
connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'self';
form-action 'self'
```

The hashes are computed from the built `index.html` at startup, so the policy needs no
`unsafe-inline` for scripts. `style-src` does allow `'unsafe-inline'`, which the UI toolkit
requires. `frame-ancestors 'none'` and `X-Frame-Options: DENY` together block framing.

These headers are set by the console handler only. API responses do not carry them — they are JSON
and are not rendered as documents. If you front the deployment with a proxy, adding
`Strict-Transport-Security` there is worthwhile; Spinneret does not set it.

### CORS

`SPINNERET_ALLOWED_ORIGINS` is empty by default, and with no origin configured the CORS middleware
is not installed at all: no cross-origin request is answered with permissive headers. When you do
configure it (the console dev server needs it), listed origins get
`Access-Control-Allow-Credentials: true`; the wildcard `*` allows any origin **without**
credentials. Do not list a production origin you do not control.

### Other listeners

| Endpoint | Authentication | Recommendation |
| --- | --- | --- |
| `GET /healthz`, `GET /readyz` | None | Fine to expose to your load balancer; they reveal dependency names and readiness only |
| `GET /metrics` | None | Bind it to `SPINNERET_METRICS_ADDR` on an internal interface. Otherwise it is served on the main listener |
| `SPINNERET_PPROF_ADDR` | **None** | Off by default. The endpoints expose heap contents and goroutine stacks — that includes plaintexts. Enable only on a loopback or private address, and the server logs a warning when you do |

---

## Multi-tenant isolation

Isolation is enforced in one pure, I/O-free package (`internal/authz`) that every service calls.
It fails closed: unknown permissions, unknown roles, unknown scopes, unknown principal kinds, a nil
principal and malformed resources are all denied.

- **Tenant** is the hard boundary. A user needs a role binding in a tenant to see anything in it.
  A token is pinned to one tenant *and* one namespace at creation and can never address another.
- **Namespace** partitions a tenant. A binding may be pinned to one namespace; a token always is.
- **Sites** narrow a binding further (up to 500 site ids). A site-restricted binding also gets
  `proxy:read` and `namespace:read` at namespace level, and only when the binding is pinned to that
  namespace — nothing else.
- **Not-found beats permission-denied** for cross-tenant lookups: asking for a token or a user in
  another tenant returns `not_found`, so ids are not confirmed to exist. Permission errors name the
  permission, never the resource.
- **Extra permissions** on a binding are restricted to a fixed set: `config:publish`,
  `secret:reveal`, `identity:reveal`, `policy:publish`. Nothing else can be added to a role.
- **Platform-only permissions** (`tenant:manage`, `kek:manage`) are held by platform admins and the
  internal system principal, and by nobody else — no role, no scope, no extra permission grants
  them.

### Known limits

- **Isolation is logical, not physical.** All tenants share one PostgreSQL database, one Redis
  keyspace and one ClickHouse database. There is no row-level security in the database; a SQL bug
  or direct database access crosses every boundary at once.
- **Resources are shared.** The per-token rate limit is the only quota. One tenant's load affects
  another's latency on the same instance. If tenants must not interfere, run separate deployments.
- **Platform admins see everything.** Including every tenant's secrets, through `secret:reveal`.
- **The KEK is global.** There is no per-tenant key. A KEK compromise is a compromise of every
  tenant.
- **The event bus and hot state are shared**, partitioned by key prefix (`SPINNERET_REDIS_PREFIX`)
  rather than by credentials.

For the full permission and role tables, see
[Tenants, users and tokens](./11-access-control.md).

---

## Input validation and request limits

Validation happens at the boundary, before a handler runs: `protovalidate` enforces the
constraints written into `proto/spinneret/v1/*.proto`, and the services validate again in Go.
Invalid input is `invalid_argument`; nothing is coerced silently.

| Limit | Value | Where |
| --- | --- | --- |
| Node request body | 8 MiB | `nodeMaxRequestBytes` |
| Admin request body | `SPINNERET_ADMIN_MAX_REQUEST_BYTES`, default 64 MiB | imports need the headroom |
| HTTP header bytes | 1 MiB | |
| Header read timeout | 10 s | |
| Body read timeout | 6 min | long polls and SSE are exempt from a write timeout |
| Idle connection timeout | 120 s | |
| Unary handler deadline | 60 s, or 5 min for imports and bulk operations; a shorter `Connect-Timeout-Ms` from the client always wins | `deadlineInterceptor` |
| `WatchConfig` long poll | client timeout capped at 60 s, plus 10 s slack | |
| Secret value | 64 KiB | `MaxSecretValueBytes` |
| Scopes per token | 64; scope ≤ 512 bytes, argument ≤ 256 bytes | |
| IP allowlist entries | 256 | |
| Sites per role binding | 500 | |
| Username | 3–64 characters, `^[a-z0-9][a-z0-9._-]{2,63}$` | |
| Tenant / namespace name | 2–63 characters, `^[a-z0-9][a-z0-9-]{1,62}$` | |
| `X-Spinneret-Node` | sanitised to `[A-Za-z0-9._:@/-]`, 128 bytes | anything else becomes `_` |
| User agent stored | truncated to 512 bytes (256 in a session record) | |
| Active tenant header | 64 bytes | |
| SSE watchers | `SPINNERET_MAX_WATCHERS`, default 20000 | |

**Query cost.** Analytics queries run against ClickHouse with an explicit `max_execution_time`.
When ClickHouse refuses a query because it exceeds its memory or row/byte quotas, the server
returns `query_too_large` (`resource_exhausted`); a timeout returns `query_timeout`
(`deadline_exceeded`). Both messages tell the operator to narrow the range or add filters instead
of surfacing an internal error. Set the ClickHouse-side limits to something your hardware can
absorb — the Compose stack ships a `clickhouse-limits.xml` for this.

**Error hygiene.** Internal errors are converted to a generic `internal` message before they reach
a client; the cause is logged server-side with the actor. SQL, driver and stack details never
cross the API boundary. Handler panics are recovered, logged with a stack trace and returned as
`internal`.

---

## Dependencies and supply chain

What the repository actually does, in `.github/workflows/ci.yml` and `.golangci.yml`:

| Check | Tool | Notes |
| --- | --- | --- |
| Generated code matches its sources | `buf generate`, `sqlc generate`, then `git diff --exit-code` | A tampered or stale generated file fails CI |
| Static analysis | `go vet` | |
| Security linting | `golangci-lint` with `gosec` enabled | Also `errorlint`, `bodyclose`, `noctx`, `nilerr`, `rowserrcheck`, `sqlclosecheck` |
| Tests | `go test -race -count=1 -skip 'TestStart.*Container' ./...` against real PostgreSQL, Valkey and ClickHouse service containers | The skipped tests are the ones that start containers themselves, which CI already provides |
| Web console | `pnpm typecheck`, `pnpm lint`, `pnpm test`, `pnpm build`, with `--frozen-lockfile` | |
| Python SDK | `pytest`, `ruff check`, `ruff format --check` on 3.9 / 3.12 / 3.13; `mypy src` on 3.12 only | |
| Container build | `docker/build-push-action` builds `deploy/docker/Dockerfile` (no push) | |
| Workflow permissions | `permissions: contents: read` | The workflow cannot write to the repository |

Properties of the build itself:

- **Partly pinned toolchain.** Go version from `go.mod`; `buf` v1.73.0 and `sqlc` v1.31.1 pinned by
  version in the workflow. `protoc-gen-go` and `protoc-gen-connect-go` are installed at `@latest`
  in the same step, so the generated-code diff check runs against a floating toolchain — pin them
  if you need that check to be reproducible.
- **Locked dependencies.** `go.sum` for Go, `pnpm-lock.yaml` with `--frozen-lockfile` for the
  console.
- **Minimal runtime image.** `gcr.io/distroless/static-debian12:nonroot` — no shell, no package
  manager — running as `nonroot:nonroot` with `CGO_ENABLED=0` static binaries. The console is
  embedded in the binary, so there is no web root to write to.
- **No network egress at runtime** except to PostgreSQL, Redis/Valkey, ClickHouse, your proxies,
  the proxy health-check URL, and your notification channels.

What is **not** in CI today, and is therefore yours to add if you need it: dependency
vulnerability scanning (`govulncheck`, Dependabot), container image scanning, SBOM generation and
artefact signing. The repository root does carry a `SECURITY.md` — the policy summarised in
[Reporting a vulnerability](#reporting-a-vulnerability) — and an Apache-2.0 `LICENSE`.

---

## Hardening checklist

Work through this before the first production deployment.

**Keys and credentials**

- [ ] `SPINNERET_KEK_FILE` is readable only by the uid the server runs as, or comes from a platform
      secret store.
- [ ] The KEK is backed up **separately from the database backup**, and a restore has been
      rehearsed.
- [ ] `scripts/compose-init.sh` was run (or equivalent): no default passwords for PostgreSQL,
      ClickHouse or the bootstrap administrator remain.
- [ ] The bootstrap administrator password was supplied via `--password-env` or `--password-stdin`,
      never as a command-line flag, and was changed after first sign-in.
- [ ] Database, Redis and ClickHouse credentials are unique to this deployment and the connection
      strings are not in version control.

**Network**

- [ ] TLS terminates in front of, or inside, the server; no plaintext hop crosses an untrusted
      network.
- [ ] `SPINNERET_TRUSTED_PROXIES` names exactly the reverse-proxy subnet.
- [ ] `SPINNERET_COOKIE_SECURE` is `true`, or `auto` with a proxy that sends
      `X-Forwarded-Proto: https`.
- [ ] `SPINNERET_ALLOWED_ORIGINS` is empty in production, or lists only origins you control.
- [ ] `SPINNERET_METRICS_ADDR` binds to an internal interface; `/metrics` is not publicly
      reachable.
- [ ] `SPINNERET_PPROF_ADDR` is empty.
- [ ] PostgreSQL, Redis/Valkey and ClickHouse are reachable only from the application network, and
      PostgreSQL uses TLS (`sslmode=verify-full`) if it is not on the same host.

**Access**

- [ ] Each human has their own account. No shared console logins.
- [ ] Roles are assigned by need: `viewer` for read-only, `operator` for day-to-day work,
      `admin` only where `secret:reveal` and `token:write` are genuinely needed, `owner` for the
      few who manage users.
- [ ] `secret:reveal` and `identity:reveal` are granted as extra permissions on specific bindings
      rather than by promoting someone to `admin`.
- [ ] Platform administrators are counted on one hand and reviewed.
- [ ] Node tokens carry the narrowest scopes that work — `lease:acquire:<site>` and
      `report:write:<site>` rather than the unargumented forms, never `admin`.
- [ ] Node tokens have an `expires_at` and an IP allowlist wherever the node's egress address is
      stable.
- [ ] Node tokens have a `rate_limit_rps` set, sized per instance.
- [ ] Token inventory is reviewed on a schedule; tokens with no recent "last used" are revoked.
- [ ] Access to the machines and to `SPINNERET_DATABASE_URL` is restricted — `spnr` against the
      database is an unaudited administrative path.

**Operations**

- [ ] Backups of PostgreSQL are tested by restoring them, not just taken.
- [ ] `SPINNERET_RETENTION_AUDIT` matches your compliance requirement.
- [ ] Audit entries are shipped to an external sink if you need tamper evidence; the `Dropped()`
      counter of the audit writer is not silently ignored.
- [ ] Alerts exist for repeated `permission_denied`, `login_throttled` and `secret.reveal` events —
      see [Observability and alerting](./12-observability.md).
- [ ] A KEK rotation has been rehearsed end to end (`spnr kek rewrap`, `spnr kek status`).
- [ ] The upgrade path and its rollback are written down —
      [Operations runbook](./16-operations.md).

---

## Reporting a vulnerability

Spinneret is maintained and open-sourced by **TikHub** at <https://github.com/TikHub/Spinneret>.

**Do not open a public issue for a security problem.** Use GitHub's private vulnerability
reporting on the repository — the *Security* tab → *Report a vulnerability* — which opens a private
advisory visible only to the maintainers. If that is unavailable to you, contact the maintainers
privately through <https://github.com/TikHub> rather than posting details anywhere public.

Include, as far as you can:

- The version or commit you tested (`spnr version`, or the image tag).
- The component: server, console, CLI, one of the SDKs, or the deployment assets.
- What an attacker gains, and what they need beforehand (an account? a token? network position?).
- Reproduction steps, or a minimal proof of concept.
- Any configuration that makes the issue reachable or unreachable.

Please give the maintainers a reasonable window to ship a fix before disclosing publicly. If you
report something that turns out to be a documented limitation of this page — a compromised node
keeping the credentials it was given, for example — you will get an answer saying so, and the page
will be made clearer where it was not.

For a problem you have found in *your* deployment rather than in the software: rotate first, then
investigate. Revoke the affected tokens, reset the affected passwords, rotate the KEK if key
material may have been exposed, and read the audit log for the window in question.

---

## Next

- [Tenants, users and tokens](./11-access-control.md) — the full permission, role and scope tables.
- [Secret vault](./10-secrets.md) — creating, versioning, reading and rotating secrets.
- [Installation and deployment](./02-installation.md) — the reverse proxy and TLS setup in context.
- [Configuration reference](./03-configuration.md) — every variable named on this page.
- [Operations runbook](./16-operations.md) — KEK rotation, backups and incident playbooks.
