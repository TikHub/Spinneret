# Secret vault

**The encrypted store for API keys, passwords, certificates and any other value a node or a config item needs but must never be committed to a repository. This page covers the encryption design, the key-encryption key, creating and versioning secrets, how a node reads one, and the audit trail.**

[中文](../zh/10-secrets.md)

---

## Contents

- [What the vault is](#what-the-vault-is)
- [Envelope encryption](#envelope-encryption)
- [The key-encryption key](#the-key-encryption-key)
- [Secrets: paths, values and versions](#secrets-paths-values-and-versions)
- [Managing secrets in the console](#managing-secrets-in-the-console)
- [How a node reads a secret](#how-a-node-reads-a-secret)
- [Permissions and scopes](#permissions-and-scopes)
- [Revealing a plaintext](#revealing-a-plaintext)
- [Rotating a secret](#rotating-a-secret)
- [Rotating the KEK](#rotating-the-kek)
- [What is audited and what is not](#what-is-audited-and-what-is-not)
- [Expiry and alerting](#expiry-and-alerting)
- [Limits](#limits)
- [Next](#next)

---

## What the vault is

The vault is a per-namespace, path-addressed store of encrypted values. A secret has a path
(`signing/api_key`), a description, tags, an optional expiry, and an ordered list of immutable
versions. Only the current version is served by default; older versions stay readable until the
secret is deleted.

Values are **write-only from the outside**. Once stored, the console shows a mask (`••••abcd`) and
nothing else. There are exactly three ways a plaintext leaves the server:

1. A node calls `SecretService.GetSecret` with an API token whose `secret:read` glob covers the
   path. This is the intended mechanism, not a leak.
2. A node reads a config item that contains a `${secret:...}` reference; the server resolves the
   reference and returns the substituted content — again only if the token's `secret:read` glob
   covers the referenced path.
3. A console user with `secret:reveal` calls `SecretAdminService.RevealSecret`. This is audited and
   the console hides the value again after 60 seconds.

A fourth, internal path exists: an identity payload field of type `secret_ref` is resolved while
the credential is rendered for a lease. That resolution is not separately authorized and not
separately audited, because the authorization happened when the reference was *written* into the
payload — see [Indirectly, through an identity payload](#indirectly-through-an-identity-payload)
and [What is audited and what is not](#what-is-audited-and-what-is-not).

The same envelope machinery encrypts more than vault secrets. Identity payloads, proxy URLs,
notification channel configuration and internal system keys use the same KEK and the same
per-record data keys, which is why KEK rotation is a single operation covering all of them.

---

## Envelope encryption

Every encrypted value is sealed with a fresh random 32-byte **data-encryption key (DEK)**, and that
DEK is itself encrypted ("wrapped") with the **key-encryption key (KEK)** the server holds in
memory. Both layers are AES-256-GCM with a random 12-byte nonce prepended to the ciphertext.

```text
ciphertext  = nonce(12) || AES-256-GCM(DEK, plaintext, AAD)
wrapped DEK = nonce(12) || AES-256-GCM(KEK, DEK, "spinneret-dek:" + <kek id>)
```

Three things are stored per record: `ciphertext`, `wrapped_dek` and `kek_id`. The KEK itself is
never in the database.

### Additional authenticated data

Both layers are bound to their context with AES-GCM additional authenticated data (AAD):

| Layer | AAD | Effect |
| --- | --- | --- |
| Value | `<record id>` + `\x00` + `<field>` — for a secret version, `<secret id>\x00v<version>` | A ciphertext copied into another row, another column or another version number fails to decrypt |
| Wrapped DEK | `spinneret-dek:<kek id>` | A wrapped DEK relabelled with a different KEK id fails to unwrap, even if two ids hold the same key material |

### What is stored where

| Table | Encrypted column | Wrapped DEK | KEK id |
| --- | --- | --- | --- |
| `secret_versions` | `ciphertext` | `wrapped_dek` | `kek_id` |
| `identity_payloads` | payload | `wrapped_dek` | `kek_id` |
| `proxies` | proxy URL | `url_wrapped_dek` | `url_kek_id` |
| `notification_channels` | channel configuration | `config_wrapped_dek` | `config_kek_id` |
| `system_keys` | internal keys (for example the dedupe pepper) | `wrapped_dek` | `kek_id` |

Secret *metadata* lives unencrypted in the `secrets` table: `path`, `description`, `tags`,
`current_version`, `expires_at`, `last_accessed_at`, `created_by` and the timestamps.

### What an attacker with the database gets

A copy of PostgreSQL without the KEK yields:

- every secret **path**, description, tag, version count, creation and last-access time;
- every ciphertext and every wrapped DEK, neither of which can be opened;
- the KEK **id** each record was wrapped with — an identifier, not key material.

It does not yield a single plaintext, and it does not allow moving a ciphertext from one record to
another, because the AAD binds each ciphertext to its record and field.

**Treat the path as public.** Do not encode the value, the account or the customer into the path.

### The DEK cache

Unwrapping a DEK on every read would make the KEK a hot path. Instead, the AES-GCM instance derived
from each unwrapped DEK is kept in an LRU cache keyed by SHA-256 over the KEK id and the wrapped
DEK; raw DEK bytes are zeroed as soon as the key schedule has been derived. The cache is sized by
`SPINNERET_DEK_CACHE_SIZE` (default `100000`) and expires entries after `SPINNERET_DEK_CACHE_TTL`
(default `10m`). See [Configuration reference](./03-configuration.md).

---

## The key-encryption key

### Format

A KEK is 32 random bytes (AES-256), base64-encoded. A KEK id matches `^[a-zA-Z0-9_-]{1,32}$`.

The server loads keys from two settings and merges them:

| Variable | Meaning |
| --- | --- |
| `SPINNERET_KEK_FILE` | Path to a file of at most 1 MiB holding the keys |
| `SPINNERET_KEKS` | Comma-separated inline list: `id:base64,id2:base64` |
| `SPINNERET_KEK_CURRENT` | Id of the KEK used to wrap new DEKs; defaults to the last key listed (file entries first, then `SPINNERET_KEKS` entries) |

At least one of `SPINNERET_KEK_FILE` and `SPINNERET_KEKS` must be set, or the server refuses to
start with `SPINNERET_KEK_FILE or SPINNERET_KEKS is required`.

The KEK file is line-based:

```text
# Comments and blank lines are ignored; a leading BOM is skipped.
k1:zvR7t0nQ2mS8k3F1xYbA5cD9eG4hJ6lN8pQ0rT2uV4w=
k2:8pQ0rT2uV4wzvR7t0nQ2mS8k3F1xYbA5cD9eG4hJ6lN=
```

A file that holds a single bare base64 line with no `id:` prefix is accepted and the key gets the
id `k1`. That shorthand is only valid when it is the file's only key. Every key must decode to
exactly 32 bytes; standard and URL-safe base64, padded or not, are all accepted. Listing the same
id twice is allowed only when the key material is identical.

Key material never appears in error messages or logs. A malformed id is reported without echoing
it, because a malformed id is usually misplaced key material.

### Generating a KEK

```bash
# Print a ready-to-paste KEK file line.
spnr kek generate --id k1

# Or, with no binary at hand:
printf 'k1:%s\n' "$(openssl rand -base64 32)"
```

`spnr` is the administration CLI; it ships in the same container image as the server, and `make
build` puts it in `bin/`. From a source checkout `go run ./cmd/spnr …` works identically. For
running it against a Compose deployment see [CLI reference](./15-cli.md).

The Compose stack does this for you: `scripts/compose-init.sh` writes
`deploy/compose/secrets/kek.key` as a single `k1:<base64>` line with mode `0644` (the container
runs as a non-root user and must be able to read the bind-mounted secret), and the services get
`SPINNERET_KEK_FILE: /run/secrets/kek`. See
[Installation and deployment](./02-installation.md).

### Backing it up

**Warning.** Data encrypted with a KEK is unrecoverable without that KEK. A database backup without
the KEK restores every path, description and tag and not one value. Back the key up separately from
the database, before the first secret is written, and verify the backup by decoding it:

```bash
# Must print 32.
cut -d: -f2 deploy/compose/secrets/kek.key | base64 -d | wc -c
```

Store the backup where the database backup is not — an offline password manager, a hardware token,
a sealed envelope. Losing both at once is the only unrecoverable failure mode in this system.

Never delete a retired KEK from the configuration while records still reference it. The server
reports such records as undecryptable: list views show `••••` instead of a mask with a suffix, reads
fail with an internal error, and `spnr kek status` marks the key `NOT-CONFIGURED`.

---

## Secrets: paths, values and versions

### Paths

A path is relative to the namespace and must match `^[a-z0-9][a-z0-9_./-]{0,255}$` with no empty,
`.` or `..` segments and no trailing `/`. The canonical form matters: token scope globs are matched
against `<namespace name>/<path>`, and a non-canonical path is rejected outright so that a glob can
never be satisfied through path aliasing.

`/` is a plain character in the path, but the console groups paths into a folder tree by it, so a
consistent prefix convention pays off:

```text
signing/api_key
signing/api_secret
database/read_replica_password
partner-api/token
```

`(namespace, path)` is unique. A path deleted and re-created is a new secret with a new ID
(`sec_<32 hex>`) and version numbering restarts at 1.

### Versions

Every stored value is an immutable version, numbered from 1. Updating a secret with a new value
appends version *n+1* and moves `current_version`; the older versions stay decryptable and readable
by id and version number. Updating only the description, tags or expiry does not create a version.

Each version records the KEK id that wrapped its data key and the actor that stored it
(`user:<id>` or `token:<id>`).

Deleting a secret deletes **all** of its versions permanently, immediately. Config items that
reference it stop resolving on nodes.

### Values

A value is 1 byte to 64 KiB of valid UTF-8. Binary material must be encoded (base64, PEM) before
it is stored.

---

## Managing secrets in the console

**Secrets** sits under **Configuration** in the navigation. The page requires `secret:list`. For a
platform administrator it has two tabs, **Secrets** and **Key encryption keys**; everyone else sees
the secret list with no tab strip at all. The list shows one row per secret with the masked value, current version,
expiry, last access and tags, a folder tree built from the paths (the first 500 paths only, with a
notice when truncated), and filters by search term and tags.

Selecting a row opens a detail panel with three tabs:

| Tab | Content |
| --- | --- |
| Overview | Metadata, masked value, expiry, last access, created by, timestamps |
| Versions | Version number, KEK id, actor and creation time, newest first |
| Access logs | Audited reads, reveals and changes, newest first: time, actor, action, version, result, IP address |

The masking rule: values of eight characters or fewer show as `••••`, longer values show `••••`
followed by their last four characters. A value that cannot be decrypted (its KEK is not
configured) degrades to `••••` and the server logs one warning per operation, not one per secret.

Creating and editing require `secret:write`; the **Reveal** action requires `secret:reveal`. The
console disables the actions the signed-in user lacks the permission for and names the missing
permission in a tooltip.

---

## How a node reads a secret

### Directly, with GetSecret

`SecretService.GetSecret` takes a path relative to the token's namespace and a version (`0` reads
the current version).

```bash
curl -s https://spinneret.example.internal/spinneret.v1.SecretService/GetSecret \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -d '{"path":"signing/api_key","version":0}'
```

```json
{
  "path": "signing/api_key",
  "version": 3,
  "value": "…",
  "expires_at": null
}
```

With the SDKs:

```python
value = client.get_secret("signing/api_key").value
pinned = client.get_secret("signing/api_key", version=3).value
```

```go
resp, err := client.GetSecret(ctx, &spinneret.GetSecretRequest{Path: "signing/api_key"})
```

The namespace is never sent: it is the namespace the token is bound to. The handler rejects any
principal that is not an API token with `permission_denied` and the message *"SecretService requires
an API token; use SecretAdminService.RevealSecret"* — a console session cannot read a secret through
the node API.

### Indirectly, through a config reference

Config item content may contain `${secret:<path>}` or `${secret:<path>#<version>}`. The server
substitutes the plaintext when it serves the item to a node, so the reference — not the value —
is what lives in the config version history.

```yaml
# config group "crawler", key "http"
upstream:
  token: ${secret:partner-api/token}
  signing_key: ${secret:signing/api_key#3}
```

Rules that apply:

- The path must be canonical, exactly as for a stored secret; a pinned version must be a positive
  integer.
- There is no escape syntax. Every `${secret:` occurrence must form a valid reference, or the item
  fails validation.
- At most **100 distinct** references per config item.
- For `json` content the substituted value is JSON-string escaped; `yaml` and `text` get the raw
  value.
- Publishing validates that every referenced path — and every pinned version — exists in the
  namespace. It fails with `failed_precondition` and *"referenced secrets do not exist in the
  namespace: …"* otherwise.
- `GetConfig`, `BatchGetConfig` and `WatchConfig` deliveries carry the substituted content and a
  flag that the item had references. See [Configuration center](./09-config-center.md).

Resolution happens per request: each distinct reference in one response is read once, and each read
writes a `secret.read` audit entry. There is no server-side cache of resolved config secrets.

Two failures are worth recognising:

| Situation | Error |
| --- | --- |
| The token holds `config:read` for the group but no matching `secret:read` glob | `permission_denied` — *"reading config item `<group>/<key>` requires read access to secret `<path>`"* |
| The referenced secret was deleted after the item was published | `failed_precondition` — *"config item `<group>/<key>` references secret `<path>` which does not exist"* |

### Indirectly, through an identity payload

An identity type may declare a field of type `secret_ref`. Its value is a namespace-relative secret
path, and the vault resolves it while the credential is rendered for a lease — so a shared API key
can live in the vault once instead of being copied into every identity payload. See
[Identities and accounts](./06-identities.md).

This path behaves differently from the two above, deliberately:

- It performs **no** permission check of its own at render time. The check happens at *write* time
  instead: storing a payload whose `secret_ref` field introduces a path the stored payload's same
  field does not already carry requires the permission to read that secret — a `secret:read` scope
  matching `<namespace name>/<path>` for a token, `secret:reveal` on the namespace for a user — and
  the secret must exist in the identity's namespace. Otherwise the write is rejected with
  `permission_denied` (*"the payload references secret `<path>`, which requires secret:read"* —
  `secret:reveal` for a user) or `invalid_argument` (*"… which does not exist in namespace
  `<ns>`"*). References left unchanged in the same field are not re-authorized. Storing a reference
  is therefore equivalent to reading the secret, and that is the security control for this path.
- It writes **no** per-read audit entry.
- Two caches sit in front of it. The rendered credential is cached for up to **60 seconds**
  (`SecretCredentialTTL`) whenever it resolved a secret, and the reload behind that can be served by
  the vault's own resolve cache, which holds a resolved value for **30 seconds** per namespace and
  path (LRU of 4096 entries, about 16 MiB); concurrent misses for the same secret share one database
  read. A change is dropped from the resolve cache immediately on the instance that made it. In the
  worst case an identity `secret_ref` can therefore still deliver the old value for about
  **90 seconds** after the rotation.

---

## Permissions and scopes

| Permission | Held by | Grants |
| --- | --- | --- |
| `secret:list` | viewer, operator, admin, owner | List secrets and their masked values, read metadata, list versions, read access logs |
| `secret:write` | admin, owner | Create, update (including storing a new version), delete |
| `secret:reveal` | admin, owner; also grantable to a single role binding as an extra permission | Reveal a plaintext in the console; also what a non-token principal needs to read a secret through config resolution |
| `secret:read` | **tokens only**, through the `secret:read` scope | Node reads via `GetSecret` and via `${secret:...}` resolution |
| `kek:manage` | platform administrators only | Read KEK status, start a rewrap |

`secret:read` is node-only: no console role includes it, and it cannot be granted to a user.
Symmetrically, `secret:list`, `secret:write` and `secret:reveal` are what a token gets only through
the `admin` scope.

### The secret:read scope

The scope is `secret:read:<glob>` and the argument is **required**. The glob is matched against
`<namespace name>/<path>`:

- `*` matches any sequence of characters, including `/`.
- `?` matches exactly one character.
- Everything else, including `[`, `]` and `\`, is a literal.

Because a token is bound to exactly one namespace, the namespace half of the glob must be that
namespace's name (or a wildcard that covers it).

| Scope | `prod/signing/api_key` | `prod/db/password` | Token in namespace `staging` |
| --- | --- | --- | --- |
| `secret:read:prod/*` | allowed | allowed | denied |
| `secret:read:prod/signing/*` | allowed | denied | denied |
| `secret:read:prod/signing/api_key` | allowed | denied | denied |
| `secret:read:prod/db/?` | denied | denied (`password` is longer than one character) | denied |
| `secret:read:*` | allowed | allowed | allowed, within `staging` only |
| `secret:read:db/*` (no namespace part) | denied | denied | denied |

Prefix tricks do not work: a token in namespace `prod-evil` is not matched by `prod/*`, and a
non-canonical request path such as `public/../private/key` or `db//pw` is rejected before the glob
is evaluated.

Grant the narrowest glob that works:

```bash
spnr token create \
  --tenant default --namespace prod --name crawler-hk \
  --scope lease:acquire --scope report:write \
  --scope config:read:crawler \
  --scope secret:read:prod/signing/* \
  --expires 720h
```

A node that reads config items containing `${secret:...}` needs **both** the `config:read` scope for
the group and a `secret:read` glob covering every referenced path. See
[Tenants, users and tokens](./11-access-control.md).

---

## Revealing a plaintext

In the console, **Reveal** on a secret row or in the detail panel opens a confirmation that names
the version and states plainly that the reveal is recorded in the audit log with the account, IP
address, version and time. Confirming calls `SecretAdminService.RevealSecret`.

The revealed value is held in component state only — never in the query cache — and is hidden again
after **60 seconds**, or immediately on **Hide now** or when the dialog is closed. A **Copy value**
action is offered so the value does not need to be re-revealed.

Authorization has two steps, and the difference matters:

- Visibility is decided by `secret:list` alone. A principal without `secret:list` on the secret gets
  `not_found`, so secret IDs of other tenants and namespaces cannot be probed — holding
  `secret:reveal` does not make an otherwise invisible secret visible. **No audit entry is written**,
  because as far as the system is concerned nothing was addressed.
- A principal that can see the secret but lacks `secret:reveal` gets `permission_denied`, and the
  attempt is audited as `secret.reveal` with result `denied`.

Revealing a version other than the current one is allowed by passing the version number; `0` means
the current version.

---

## Rotating a secret

Rotating a value is an `UpdateSecret` with a new value. It appends a version and moves the current
pointer; it never rewrites an existing version.

Running nodes do not break, provided you rotate in the right order:

1. **Add the new credential at the target** first, so both old and new are valid there.
2. **Store the new value** as a new version (console: *Edit* → *New value*; API: `UpdateSecret` with
   `value` set).
3. **Wait for the readers to pick it up.**
   - Nodes calling `GetSecret` see the new value on their next call — there is no server-side cache
     on this path.
   - Nodes watching a config item that carries an unpinned `${secret:<path>}` reference do **not**
     get a new delivery: the config version did not change, and `WatchConfig` is driven by config
     versions. They pick the new value up on their next `GetConfig` or the next time the item is
     republished. If a secret rotation must reach watchers immediately, republish the referencing
     config item.
   - Identity `secret_ref` fields converge within about **90 seconds** across instances: a rendered
     credential that resolved a secret is cached for up to 60 seconds, and its reload can be served
     from the vault's 30-second resolve cache.
4. **Revoke the old credential at the target** only after every reader has moved — for
   `secret_ref` identities that means waiting out the full ~90 seconds, not 30.

Pinned references (`${secret:<path>#3}`) keep serving version 3 until the config item is edited.
That is the tool for a change that must be rolled out together with a config change rather than on
its own schedule.

**Note.** Deleting old versions is not possible individually — only the whole secret can be deleted.
If a leaked version must become unreadable, delete the secret and re-create it, then repoint or
republish everything that referenced it.

---

## Rotating the KEK

Rotation re-wraps every data key with a new KEK. It does **not** re-encrypt the data: only the
small wrapped-DEK column changes, so the job is cheap and safe to run while the system serves
traffic.

The complete procedure:

```bash
# 1. Generate the new key.
spnr kek generate --id k2

# 2. Add the line to the KEK file (keep k1!) and make k2 current.
#    Either list k2 last in the file, or set SPINNERET_KEK_CURRENT=k2.
cat deploy/compose/secrets/kek.key
# k1:…
# k2:…

# 3. Restart every instance so they all hold both keys. The KEK set is read
#    once at startup and there is no reload signal, and the key file is a
#    bind-mounted Compose secret — so `up -d` recreates nothing when only the
#    file changed. Restart the processes explicitly.
docker compose -f deploy/compose/docker-compose.yml restart spinneret

# 4. Confirm both keys are loaded and see how much work is pending.
spnr kek status
# current kek: k2
#   k1               wrapped_records=18422
#   k2               wrapped_records=0 current
# rewrap: idle (0/0)

# 5. Re-wrap everything onto k2 and wait.
spnr kek rewrap
# kek rewrap started
# progress: 5000/18422 records re-wrapped to k2
# ...
# kek rewrap finished

# 6. Only when status shows wrapped_records=0 for k1, remove k1 from the
#    configuration and restart. Keep the retired key in cold backup.
```

Both CLI commands need `SPINNERET_DATABASE_URL` and the KEK settings.

`spnr kek status` prints one line per key with its wrapped-record count, ` current` for the wrapping
key and ` NOT-CONFIGURED` for a key that records still reference but this process does not hold. The
console shows the same information under **Secrets → Key encryption keys**, including a progress bar
and a **Start rewrap** button; both require `kek:manage`, which only platform administrators hold.

How the job works:

| Property | Value |
| --- | --- |
| Tables covered | `identity_payloads`, `secret_versions`, `proxies`, `notification_channels`, `system_keys` |
| Batch size | 500 rows |
| Concurrency | Exactly one instance at a time, via the PostgreSQL advisory lock `spinneret:kek:rewrap` |
| Progress | Persisted in `system_settings` under the key `kek_rewrap_status`, so every instance and the console can report it |
| Heartbeat | Every 2 s; a job whose instance stops heartbeating for 30 s is reported as interrupted |
| Safety | Each update is a compare-and-swap on the old KEK id and the old wrapped DEK, so a record re-sealed concurrently is never overwritten |

`spnr kek rewrap` follows a job already running on another instance instead of starting a second
one, and exits non-zero if the job reported errors. Interrupting the CLI stops the job it started;
starting it again resumes from what is still not on the current KEK.

Rotate the KEK when it may have been exposed, when someone with access to it leaves, or on whatever
schedule your policy sets. See [Operations runbook](./16-operations.md).

---

## What is audited and what is not

Audit entries go to `audit_logs` with the actor (`user`, `token` or `system`), actor id and name,
tenant, namespace, resource kind `secret`, the secret id and path, the result, the client IP, the
user agent and a JSON details object. They are readable in the secret's **Access logs** tab and in
the tenant audit log ([Tenants, users and tokens](./11-access-control.md)).

### Audited

| Action | Written when | Results | Details |
| --- | --- | --- | --- |
| `secret.create` | A secret is created | `ok` | `version` (always 1) |
| `secret.update` | Metadata changed or a new version stored | `ok` | `version`, `value_changed`, `description_changed`, `tags_changed`, `expiry_changed` |
| `secret.delete` | A secret and its versions are deleted | `ok` | `version` (the last current version) |
| `secret.reveal` | A console reveal is attempted by a principal that can see the secret | `ok`, `denied`, `error` | `version` |
| `secret.read` | A node reads a secret — `GetSecret`, or a `${secret:...}` reference being resolved | `ok`, `denied`, `error` | `version`, `purpose`, and `error` on failure (`not_found`, `unavailable`, `decrypt`) |
| `kek.rewrap_start` | A rewrap is requested (resource kind `kek`) | `ok`, `error` | `started` |

The `purpose` distinguishes why a read happened: `api` for a direct `GetSecret`, and
`config:<group>/<key>` for a reference resolved while serving a config item. It is sanitised to at
most 64 bytes with whitespace and control characters removed.

A denied or failed read is audited too, and the server looks the secret id up so the attempt appears
in that secret's access log even though nothing was returned. When a config read is denied because
of a missing `secret:read` glob, two entries are written: the `secret.read` denial and a `config.read`
denial carrying `secret_path` and `secret_version`.

### Not audited

| Operation | Why |
| --- | --- |
| `ListSecrets`, `GetSecret` (metadata), `ListSecretVersions`, `ListSecretAccessLogs` | Metadata reads; they never expose a value. They still require `secret:list` |
| `GetKEKStatus` | Read-only status; requires `kek:manage` |
| Identity `secret_ref` resolution during credential rendering | Authorized at lease acquisition; a per-read entry per lease would drown the audit log. The lease and the report are the record |
| Reads served from the 30-second identity resolve cache | No read happened |
| A **reveal** attempt by a principal that cannot see the secret at all (no `secret:list` on it) | Returns `not_found`; nothing was addressed. A denied **read** is different — it is always audited (see above) |

### Last access

`last_accessed_at` on the secret is maintained separately from the audit log: reads buffer the
timestamp in memory and a background flush writes them in batches every 10 seconds (and once more
at shutdown). The buffer holds 100,000 secrets; beyond that, accesses to secrets not already
buffered are dropped. The column is informational — use the access log, not this timestamp, for
anything that must be exact.

---

## Expiry and alerting

`expires_at` is optional and advisory: an expired secret is **still returned** to nodes, and the
server logs a warning (`expired secret was read`) with the namespace, secret id and expiry. Nothing
stops working on its own — expiry is a reminder, not an enforcement mechanism.

The alert rule fires once per day per secret for secrets expiring within the next 7 days, and also
covers secrets that expired in the last 7 days, as event kind `secret_expiring`. Route it to a
channel in **Notifications**; see [Observability and alerting](./12-observability.md).

The console highlights the same window: **Expires within 7 days** and **Expired** badges in the list.

---

## Limits

| Thing | Limit |
| --- | --- |
| Secret path | 1–256 bytes, `^[a-z0-9][a-z0-9_./-]{0,255}$`, canonical segments |
| Secret value | 1 byte – 64 KiB, valid UTF-8 |
| Description | 1024 characters |
| Tags | 64 per secret, 64 characters each |
| Tag filter on list | 32 tags |
| Search term | 256 characters |
| Page size | default 50, maximum 500 |
| Versions per secret | 2 147 483 647 (`failed_precondition` beyond that) |
| Distinct `${secret:...}` references per config item | 100 |
| KEK | exactly 32 bytes, id `^[a-zA-Z0-9_-]{1,32}$` |
| KEK file | 1 MiB |
| Reveal visibility in the console | 60 seconds |

---

## Next

- [Configuration center](./09-config-center.md) — config items, versions and the `${secret:...}` references this page resolves.
- [Tenants, users and tokens](./11-access-control.md) — issuing a token with the right `secret:read` glob.
- [Node API reference](./13-node-api.md) — the exact `GetSecret` request and response, and the error reasons.
- [Identities and accounts](./06-identities.md) — `secret_ref` payload fields.
- [Operations runbook](./16-operations.md) — KEK backup, rotation schedule and restore drills.
- [Security](./19-security.md) — the threat model this design answers, and what stays your responsibility.
