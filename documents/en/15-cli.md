# CLI reference

**Complete reference for the two Spinneret binaries: `spnr`, the administration CLI, and `spinneret-server`, the control-plane instance. Every command, flag, default, exit code and signal.**

[中文](../zh/15-cli.md)

---

## Contents

- [The two binaries](#the-two-binaries)
- [spnr](#spnr)
  - [How spnr gets its configuration](#how-spnr-gets-its-configuration)
  - [Global flags](#global-flags)
  - [Exit codes](#exit-codes)
  - [Command overview](#command-overview)
- [spnr migrate](#spnr-migrate)
- [spnr admin](#spnr-admin)
- [spnr token](#spnr-token)
- [spnr config](#spnr-config)
- [spnr kek](#spnr-kek)
- [spnr rebuild](#spnr-rebuild)
- [spnr seed](#spnr-seed)
- [spnr healthcheck](#spnr-healthcheck)
- [spnr version](#spnr-version)
- [spnr completion](#spnr-completion)
- [Running spnr against a deployment](#running-spnr-against-a-deployment)
- [spinneret-server](#spinneret-server)
  - [Flags](#flags)
  - [Environment](#environment)
  - [Roles](#roles)
  - [Startup](#startup)
  - [Signals, shutdown and draining](#signals-shutdown-and-draining)
  - [Server exit codes](#server-exit-codes)
- [I want to…](#i-want-to)

---

## The two binaries

| Binary | What it does | Where it lives |
| --- | --- | --- |
| `spinneret-server` | Runs one control-plane instance: the node API, the console, the background workers. Long-running. | `/usr/local/bin/spinneret-server` in the container (the image `ENTRYPOINT`); `bin/spinneret-server` after `make build` |
| `spnr` | Administration CLI: database migrations, the first administrator, API tokens, hot-state rebuilds, key-encryption keys, load-test data, container health checks. One-shot. | `/usr/local/bin/spnr` in the same container image; `bin/spnr` after `make build` |

Both are built from the same repository and shipped in the same image, so any Spinneret container can run either. Both read the same `SPINNERET_*` environment variables.

```bash
# Build both binaries into ./bin
make build
```

---

## spnr

```text
spnr administers a Spinneret deployment: database migrations, the first administrator,
API tokens, hot-state rebuilds, key-encryption keys and load-test data.

It reads the same SPINNERET_* environment variables as spinneret-server.

Usage:
  spnr [command]

Available Commands:
  admin       Administer console users
  completion  Generate the autocompletion script for the specified shell
  config      Inspect the server configuration
  healthcheck Probe an HTTP health endpoint (exit 0 on 2xx)
  help        Help about any command
  kek         Manage key-encryption keys
  migrate     Manage the PostgreSQL schema
  rebuild     Rebuild the Redis hot state from PostgreSQL
  seed        Create load-test and end-to-end test data (idempotent)
  token       Manage API tokens
  version     Print the spnr version
```

### How spnr gets its configuration

`spnr` has no configuration file and no connection flags. It reads the `SPINNERET_*` environment variables of its own process — the same ones `spinneret-server` reads, documented in [Configuration reference](./03-configuration.md).

Commands differ in how much of that configuration they require. Most need only the database, so they can be run before the rest of the stack exists:

| Command | Required | Optional |
| --- | --- | --- |
| `migrate up` / `down` / `status` | `SPINNERET_DATABASE_URL` | — |
| `admin init` | `SPINNERET_DATABASE_URL` | `SPINNERET_REDIS_URL` or `SPINNERET_REDIS_ADDRS`, `SPINNERET_REDIS_PREFIX` (to notify running instances) |
| `token create` | `SPINNERET_DATABASE_URL` | — |
| `kek status` / `kek rewrap` | `SPINNERET_DATABASE_URL` and `SPINNERET_KEK_FILE` or `SPINNERET_KEKS` | `SPINNERET_KEK_CURRENT` |
| `rebuild` | `SPINNERET_DATABASE_URL` and `SPINNERET_REDIS_URL` or `SPINNERET_REDIS_ADDRS` | `SPINNERET_REDIS_PREFIX` |
| `config check` | the complete server configuration | — |
| `seed` | the complete server configuration (database, Redis and KEK settings) | — |
| `kek generate`, `healthcheck`, `version`, `completion` | nothing | — |

Two details of the relaxed loader used by the database-only commands:

- The connection pool of a database-only command opens **4** connections, not the server's 32. `SPINNERET_DATABASE_MAX_CONNS` overrides it for **every** database-only command — `migrate`, `admin init`, `token create`, `kek status`, `kek rewrap` and `rebuild` all open the pool through the same helper — and must be an integer `>= 2`.
- `SPINNERET_REDIS_PREFIX` defaults to `sp`, exactly as in the server. A `rebuild` run with the wrong prefix rebuilds a hot state nobody reads.

`config check` and `seed` load and validate the **full** configuration, so they fail on any invalid variable — which is what makes `config check` useful before a deployment.

### Global flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--log-level` | `warn` | Level of the diagnostic log written to **stderr**: `debug`, `info`, `warn`, `error`. The log is plain text; command results always go to **stdout**. |
| `-h`, `--help` | — | Help for the command; exits 0. |

`-v` / `--version` is **not** global: it exists on the root command only. `spnr --version` prints `spnr <version>` and exits 0, but `spnr migrate --version` fails with `spnr: unknown flag: --version` and exit 1. Use `spnr version` when you want the version from a script.

Because results go to stdout and diagnostics to stderr, a token or a JSON result can be captured safely:

```bash
TOKEN="$(spnr token create --name crawler-a --scope lease:acquire --scope report:write)"
```

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. Also `--help`, and `admin init` when an administrator already exists. |
| `1` | Any failure: invalid flags, an unknown command, missing environment, a connection failure, a failed `healthcheck` probe, a `kek rewrap` that finished with errors. |

Every failure prints one line to stderr prefixed with `spnr: `:

```text
spnr: SPINNERET_DATABASE_URL is required
```

```text
spnr: invalid configuration:
SPINNERET_DATABASE_URL is required
SPINNERET_REDIS_URL or SPINNERET_REDIS_ADDRS is required
SPINNERET_KEK_FILE or SPINNERET_KEKS is required
```

### Command overview

| Command | Purpose |
| --- | --- |
| `spnr migrate up` | Apply every pending migration. |
| `spnr migrate down [--to N]` | Roll back one migration, or down to version `N`. |
| `spnr migrate status` | Compare the applied schema version with the one embedded in the binary. |
| `spnr admin init` | Create the first platform administrator, a tenant and a namespace. |
| `spnr token create` | Create an API token and print its secret. |
| `spnr config check` | Validate the environment and print it with secrets redacted. |
| `spnr kek generate` | Print a new random KEK line. |
| `spnr kek status` | Show configured KEKs, how many records each wraps, and rewrap progress. |
| `spnr kek rewrap` | Re-wrap every data key with the current KEK. |
| `spnr rebuild` | Rebuild the Redis hot state from PostgreSQL. |
| `spnr seed` | Create a synthetic load-test site with identities, policies and a node token. |
| `spnr healthcheck` | Probe an HTTP health endpoint; exit 0 on 2xx. |
| `spnr version` | Print the build version. |
| `spnr completion <shell>` | Print a shell completion script. |

---

## spnr migrate

Applies, rolls back or inspects the database migrations **embedded in the binary**. There is no separate migration directory to ship. Concurrent runs are serialized with a PostgreSQL advisory lock, so several instances or several operators starting a migration at the same time is safe.

Only `SPINNERET_DATABASE_URL` is required.

The version numbers below are the ones of this release: the binary embeds **5** migrations, so a fully migrated database is at version 5. Every release that adds a migration raises that number; run `spnr migrate status` to see what your binary expects.

### `spnr migrate up`

Applies every pending migration and prints the resulting version.

```bash
spnr migrate up
```

```text
database schema is at version 5
```

Running it when nothing is pending is a no-op that prints the same line.

### `spnr migrate down`

| Flag | Default | Meaning |
| --- | --- | --- |
| `--to` | unset | Roll back until the schema version equals this number. `0` removes every migration. Must be `>= 0` and must not be above the current version. |

Without `--to`, exactly one migration — the latest applied — is rolled back.

```bash
spnr migrate down           # one step back
spnr migrate down --to 4    # back to version 4
```

```text
database schema is at version 4
```

When the target equals the current version, it prints `database schema is already at version N` and changes nothing. `--to` above the current version is an error:

```text
spnr: --to 9 is above the current schema version 5 (use migrate up)
```

**Warning.** Rolling back drops tables and the data in them. Take a backup first — see [Operations runbook](./16-operations.md).

### `spnr migrate status`

Prints one line describing the relationship between the applied schema and the binary:

```text
database schema version 5 (up to date)
database schema version 3, binary version 5 (2 pending: run spnr migrate up)
database schema version 6 is newer than this binary (5)
```

The third case happens during a rolling downgrade; the server logs a warning and keeps running in that situation, it does not refuse to start.

---

## spnr admin

The only subcommand is `init`.

### `spnr admin init`

Creates the **first** platform administrator, together with a tenant and a namespace that receive the default policies. This is the bootstrap step of a fresh deployment; every later user is created in the console ([Tenants, users and tokens](./11-access-control.md)).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--username` | — | Administrator user name. Required. Lower-cased and trimmed; must be 3–64 characters of `a-z`, `0-9`, `.`, `_` or `-` and start with a letter or digit. |
| `--password-env` | — | Name of the environment variable holding the password. |
| `--password-stdin` | `false` | Read the password from the first line of stdin. |
| `--tenant` | `default` | Tenant to create or reuse. 2–63 characters of `a-z`, `0-9` or `-` and must start with a letter or digit. |
| `--namespace` | `default` | Namespace to create or reuse. Same rules as the tenant. |

`--password-env` and `--password-stdin` are mutually exclusive, and exactly one of them is required. The password must be 10–1024 characters. There is no `--password` flag on purpose: a password on the command line ends up in the shell history and in `ps`.

**Idempotent.** When a platform administrator already exists the command prints `already initialized` and exits **0**. That makes it safe in an installer, a Compose one-shot service or a restart loop.

When `SPINNERET_REDIS_URL` (or `SPINNERET_REDIS_ADDRS`) is set, running instances are notified to reload the new namespace immediately. Without Redis the command still succeeds; instances pick the namespace up within a minute.

```bash
# From an environment variable
SPINNERET_ADMIN_PASSWORD='a-long-passphrase' \
  spnr admin init --username admin --password-env SPINNERET_ADMIN_PASSWORD

# From stdin
printf '%s\n' "$PASSWORD" | spnr admin init --username admin --password-stdin
```

```text
created platform administrator admin (018f2c1e-1c1c-7c9e-9a1e-9f1b2c3d4e5f) in tenant default, namespace default
```

---

## spnr token

The only subcommand is `create`. Tokens can also be created in the console; the CLI exists so that a node token can be minted from an installer or a CI job with nothing but a database URL.

### `spnr token create`

Creates an API token bound to one tenant and namespace and prints **only the plaintext token** on stdout. The plaintext is never stored and cannot be retrieved later — capture it or lose it.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--tenant` | `default` | Tenant name. |
| `--namespace` | `default` | Namespace name. |
| `--name` | — | Token name, unique among the usable tokens of the namespace. Required. |
| `--scope` | — | A scope granted to the token. Repeatable; at least one is required. Duplicates are rejected. |
| `--expires` | `720h` | Lifetime. Accepts Go durations (`720h`, `90m`) and a leading day component (`30d`, `30d12h`). `0`, `never`, `permanent` or an empty value means no expiry. |
| `--description` | empty | Free-form description, at most 512 bytes. |

Scopes:

| Scope | Argument | Grants |
| --- | --- | --- |
| `lease:acquire` | optional site name | `Acquire`, `Renew`, `Release` |
| `report:write` | optional site name | `Report`, `ReportBatch` |
| `config:read` | optional group glob | `GetConfig`, `WatchConfig` |
| `config:publish` | optional group glob | read, write and publish config items |
| `secret:read` | **required** `<namespace>/<path glob>` | `GetSecret` |
| `identity:write` | optional site name | read, write and operate identities |
| `proxy:write` | none | read, write and operate proxies |
| `admin` | none | the full administrator permission set |

Argument rules: an argument follows the scope name after a colon, must be non-empty, at most 256 bytes of valid UTF-8 with no whitespace or control characters. Site-name arguments must **not** contain the wildcards `*` or `?` — omit the argument to allow every site. A whole scope string is at most 512 bytes. See [Tenants, users and tokens](./11-access-control.md) for what each permission covers.

```bash
spnr token create --tenant default --namespace default --name crawler-hk \
  --scope lease:acquire --scope report:write --scope config:read --expires 720h
```

```text
spn_0Xk9mQ2pZ7wR4tL1vB8nH5sD3fG6jC0aY9eU2iO7qK4
```

A plaintext token is always `spn_` followed by 43 base62 characters — 47 characters in total. Only its first 12 characters and a SHA-256 digest are stored, which is what the console shows in the token list.

A site-scoped, non-expiring token for a node that only works on `example-site`:

```bash
spnr token create --name node-example-site \
  --scope lease:acquire:example-site \
  --scope report:write:example-site \
  --scope 'config:read:crawler/*' \
  --expires never \
  --description 'nodes in rack 4'
```

---

## spnr config

The only subcommand is `check`.

### `spnr config check`

Loads and validates the complete `SPINNERET_*` environment and prints it as JSON with credentials redacted. Exits 0 when the configuration is valid, 1 with the list of problems otherwise. Nothing is connected to — this is a pure configuration check, so it is the right first command on a host that is not up yet.

```bash
spnr config check
```

```json
{
  "clickhouse_url": "clickhouse://spinneret:xxxxx@clickhouse:9000/spinneret",
  "database_url": "postgres://spinneret:xxxxx@postgres:5432/spinneret?sslmode=disable",
  "http_addr": ":8080",
  "instance_id": "node-1-bba039",
  "kek_current": "",
  "kek_file": "/run/secrets/kek",
  "keks": "",
  "late_window": "10m0s",
  "log_level": "info",
  "metrics_addr": "",
  "otlp_endpoint": "",
  "payload_cache": "true",
  "pprof_addr": "",
  "redis_addrs": "",
  "redis_prefix": "sp",
  "redis_url": "redis://valkey:6379/0",
  "report_dedup_ttl": "1h0m0s",
  "report_shards": "16",
  "role": "all",
  "tls": "false",
  "ui_enabled": "true"
}
```

That sample is a run inside the Compose stack, which always sets `SPINNERET_CLICKHOUSE_URL`. On a host without ClickHouse configured, `clickhouse_url` is the empty string and the server logs `clickhouse disabled: raw report events are not stored` on startup.

Passwords inside URLs are replaced with `xxxxx`, and `keks` shows only whether inline key material is present, never the keys. The output is a summary of the settings an operator most often gets wrong, not every variable; the full list is in [Configuration reference](./03-configuration.md).

---

## spnr kek

Manages the key-encryption keys that wrap the data-encryption keys of the secret vault and of every encrypted identity payload. The concepts are in [Secret vault](./10-secrets.md); this section is the command surface.

### `spnr kek generate`

Prints one new random key line in the `id:base64` format the KEK file uses. 32 random bytes, standard base64. Requires no environment at all.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--id` | `k1` | Key id. Must match `^[a-zA-Z0-9_-]{1,32}$`. |

```bash
spnr kek generate --id k2 >> deploy/compose/secrets/kek.key
```

```text
k2:sS1aF7AmODDr3+J1nT5oZIuUU2AnQ+/P3c8RM0E680Y=
```

The KEK file holds one key per line; blank lines and lines starting with `#` are ignored. When no `SPINNERET_KEK_CURRENT` is set, the **last** key listed is the current one.

**Warning.** Data encrypted with a KEK is unrecoverable without it. Back the KEK file up separately from the database backup, and never into the same place.

### `spnr kek status`

Shows the configured KEKs, how many stored records each one still wraps, and the state of the rewrap job. Requires `SPINNERET_DATABASE_URL` and the KEK settings.

```bash
spnr kek status
```

```text
current kek: k2
  k1               wrapped_records=1043
  k2               wrapped_records=28711 current
rewrap: idle (28711/29754)
last finished: 2026-03-04T09:12:44Z
```

Reading it: `k2` is current and already wraps 28711 records; 1043 records are still wrapped with `k1`. The `rewrap:` line is the job state — `idle` or `running` — followed by `done/total` of the running or last job: `total` is how many records that job found pending when it started, `done` how many it has re-wrapped so far. Here the last job re-wrapped 28711 of the 29754 records it started with, which is why 1043 are left on `k1`; running `spnr kek rewrap` again finishes them.

A key marked `NOT-CONFIGURED` still wraps records in the database but is missing from `SPINNERET_KEK_FILE` / `SPINNERET_KEKS`. Those records cannot be decrypted until the key is put back.

### `spnr kek rewrap`

Starts the rewrap job — or follows the one already running on another instance — and prints progress once per second until it finishes.

```bash
spnr kek rewrap
```

```text
kek rewrap started
progress: 4096/29754 records re-wrapped to k2
progress: 12288/29754 records re-wrapped to k2
progress: 29754/29754 records re-wrapped to k2
kek rewrap finished
```

If another instance already runs it, the first line reads `kek rewrap already running on another instance; following its progress`.

Exits 1 with `kek rewrap finished with errors: <message>` when the job reported errors. Interrupting the command with Ctrl-C **stops the job** — it runs in this process — and prints `kek rewrap interrupted (the job stops with this process)`. Re-running resumes from where it stopped.

The full rotation procedure:

```bash
# 1. Add the new key and make it current
spnr kek generate --id k2 >> deploy/compose/secrets/kek.key
#    SPINNERET_KEK_CURRENT=k2, or leave it unset and keep k2 last in the file

# 2. Restart every instance so they all know k2

# 3. Re-wrap
spnr kek rewrap

# 4. Remove k1 from the file only once this shows wrapped_records=0 for k1
spnr kek status
```

---

## spnr rebuild

Rebuilds the Redis hot state — the working set the scheduler reads on every `Acquire` — from PostgreSQL, which is always the source of truth.

Requires `SPINNERET_DATABASE_URL` and `SPINNERET_REDIS_URL` (or `SPINNERET_REDIS_ADDRS`), with the `SPINNERET_REDIS_PREFIX` of the deployment.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--tenant` | `default` | Tenant of `--site`. |
| `--namespace` | `default` | Namespace of `--site`. |
| `--site` | empty | Rebuild only this site. |

Without `--site`, the hot-state epoch key is deleted and **every** site is rebuilt. While that runs, API instances report not-ready (`/readyz` → `hotstate: epoch missing (rebuild pending)`) and a load balancer takes them out of rotation. With `--site`, only that one site is re-materialized and the rest of the deployment keeps serving.

Passing `--tenant` or `--namespace` without `--site` is rejected — they only select the namespace of a site rebuild:

```text
spnr: --tenant and --namespace select the namespace of --site; add --site for a site rebuild
```

```bash
# Everything (maintenance window)
spnr rebuild
```

```text
rebuilt hot state of every site in 12.418s
```

```bash
# One site, online
spnr rebuild --tenant default --namespace default --site example-site
```

```text
rebuilt hot state of site default/default/example-site in 1.902s
```

When to use it: after restoring a Redis backup, after a Redis flush, after a prefix change, or when the hot state and the database have visibly diverged. The runbook is in [Operations runbook](./16-operations.md).

---

## spnr seed

Creates — or completes — a synthetic site for load tests and end-to-end tests. It is idempotent for everything except the node token, which is always re-created (the previous token of the same name is revoked).

Requires the **complete** server configuration, and a database whose schema is current. If it is behind, the command refuses:

```text
spnr: database schema is at version 3, binary expects 5: run spnr migrate up
```

| Flag | Default | Range | Meaning |
| --- | --- | --- | --- |
| `--tenant` | `default` | — | Tenant; created when missing. |
| `--namespace` | `default` | — | Namespace; created when missing. |
| `--site` | `loadtest` | — | Site name. |
| `--client` | `web` | — | Client type. |
| `--groups` | `50` | 0–1000 | Number of endpoint groups. |
| `--identities` | `100000` | 0–5000000 | Number of identities. |
| `--proxies` | `0` | 0–100000 | Number of proxies; `0` means none. |
| `--proxy-url` | `http://loadtest-{i}:loadtest@mocktarget:9091` | — | Proxy URL template; `{i}` is replaced by the proxy index. Must contain `{i}` when `--proxies > 1`, because proxies are deduplicated by URL. |
| `--token-name` | the site name | — | Name of the node token. |

That table lists every **visible** flag. One more flag is hidden because it only tunes throughput: `--chunk-size` (default `5000`, range 1–50000) is the number of identities per import call. Lower it on a small database, raise it on a fast one.

Names passed to `--tenant`, `--namespace`, `--site`, `--client` and `--token-name` must be non-empty and contain no spaces or slashes. `--proxy-url` must be non-empty whenever `--proxies > 0`, and must contain `{i}` whenever `--proxies > 1`.

What it creates:

| Object | Value |
| --- | --- |
| Endpoint groups | `g0` … `g{N-1}`, each with the URI prefix rule `/api/g<i>/` |
| Identity type | `loadtest_cookie` — fields `cookies` (cookie map, required, sensitive) and `user_agent`, unique by `cookies.sessionid`, immediate activation |
| Identities | Deterministic synthetic rows, imported in chunks, `create_only` |
| Rotation policy | `loadtest-rotation` — weighted random, sample 32, lease TTL 60s, 1 concurrent lease per identity; proxy mode `pool` when `--proxies > 0`, otherwise `none` |
| Breaker policy | `loadtest-breaker` — a minimum request count so high that it never opens during a load test |
| Config item | `crawler/loadtest.json`, published |
| Proxies | Imported from the template, kind `datacenter`, provider `loadtest`, tag `loadtest` |
| Node token | Named after `--token-name`, scopes `lease:acquire`, `report:write`, `config:read`, no expiry |

Progress goes to **stderr**; the result is JSON on **stdout**:

```bash
spnr seed --site loadtest --identities 100000 --groups 50
```

```json
{
  "token": "spn_0Xk9mQ2pZ7wR4tL1vB8nH5sD3fG6jC0aY9eU2iO7qK4",
  "site": "loadtest",
  "groups": 50,
  "identities": 100000
}
```

A fifth key, `proxies`, is present only when proxies were created — it is omitted when `--proxies` is `0`:

```bash
spnr seed --site loadtest --proxies 100 --proxy-url 'http://lt-{i}:secret@mocktarget:9091'
```

```json
{
  "token": "spn_0Xk9mQ2pZ7wR4tL1vB8nH5sD3fG6jC0aY9eU2iO7qK4",
  "site": "loadtest",
  "groups": 50,
  "identities": 100000,
  "proxies": 100
}
```

Capturing just the token for a load run:

```bash
LOADTEST_TOKEN="$(spnr seed --site loadtest 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')"
```

**Note.** `spnr seed` writes into a real namespace. Point it at a tenant and namespace you are willing to fill with synthetic data.

---

## spnr healthcheck

Sends one `GET` and exits 0 when the response status is 2xx, 1 otherwise. It exists because the runtime image is distroless and has no shell, no `curl` and no `wget` — a container `HEALTHCHECK` needs a binary.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--url` | `http://127.0.0.1:8080/readyz` | URL to probe. Must be an absolute `http` or `https` URL. |
| `--timeout` | `3s` | Request timeout. Must be positive. |

Redirects are not followed — health endpoints never redirect, and following one to another host would make the check meaningless.

```bash
spnr healthcheck --url http://127.0.0.1:8080/readyz
echo $?   # 0
```

Failures print the reason with any credentials in the URL redacted:

```text
spnr: probe http://127.0.0.1:8080/readyz: status 503
spnr: probe http://127.0.0.1:8080/readyz: Get "http://127.0.0.1:8080/readyz": dial tcp 127.0.0.1:8080: connect: connection refused
```

This is exactly what the image declares:

```dockerfile
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/spnr", "healthcheck", "--url", "http://127.0.0.1:8080/readyz"]
```

---

## spnr version

```bash
spnr version
spnr --version
spnr -v
```

```text
spnr v0.1.0
```

Release builds inject the version at link time. A build without it falls back to the module version recorded by the Go toolchain, or to `dev-<12 hex characters of the revision>[-dirty]`, or to `dev` when nothing is known. The same string is reported by `spinneret-server --version` and by the server on startup.

---

## spnr completion

Prints a shell completion script for `bash`, `zsh`, `fish` or `powershell`.

```bash
# zsh, for the current user
spnr completion zsh > "${fpath[1]}/_spnr"

# bash, for the current shell
source <(spnr completion bash)
```

`spnr completion <shell> --help` explains where each shell expects the file.

---

## Running spnr against a deployment

### Inside the Compose stack

Every service built from the Spinneret image contains both binaries. For a one-shot `spnr` command, run it on the **`migrate`** service: it shares the image, the `SPINNERET_*` environment and the KEK secret with the servers, but it depends only on PostgreSQL, so it does not start Valkey and ClickHouse for a command that needs neither. Its entrypoint is already cleared, but pass `--entrypoint` anyway so the service's own `spnr migrate up` command is replaced:

```bash
cd /path/to/Spinneret

# One-shot command in a fresh container with the stack's environment
docker compose -f deploy/compose/docker-compose.yml \
  run --rm --entrypoint /usr/local/bin/spnr migrate kek status

# In an already running replica (distroless: pass the absolute path, there is no shell)
docker compose -f deploy/compose/docker-compose.yml \
  exec --index 1 spinneret /usr/local/bin/spnr kek status
```

`--index` is needed because the `spinneret` service runs `SPINNERET_REPLICAS` replicas (2 by default).

The `spinneret` service works the same way — `run --rm --entrypoint /usr/local/bin/spnr spinneret <command>` — but it `depends_on` `migrate`, `valkey` and `clickhouse`, so Compose pulls the whole stack up first. Use it only when you want that.

Two commands are already wired as services, so they need no entrypoint override:

```bash
# spnr migrate up — also runs automatically before the servers start
docker compose -f deploy/compose/docker-compose.yml run --rm migrate

# spnr admin init with SPINNERET_ADMIN_USERNAME / SPINNERET_ADMIN_PASSWORD from .env
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

### On the host

Build the binary and give it the environment yourself. Against the Compose stack the databases are not published to the host by default, so the usual host workflow is to reach them over the mapped ports of your own deployment or to run the CLI in a container as above.

```bash
make build

export SPINNERET_DATABASE_URL='postgres://spinneret:<password>@127.0.0.1:5432/spinneret?sslmode=disable'
export SPINNERET_REDIS_URL='redis://127.0.0.1:6379/0'
export SPINNERET_KEK_FILE=/etc/spinneret/kek.key

./bin/spnr migrate status
```

For commands that need the full configuration, source the same `.env` the servers use:

```bash
set -a; . ./deploy/compose/.env; set +a
```

**Note.** `deploy/compose/.env` holds `PG_PASSWORD` and `CLICKHOUSE_PASSWORD`, not the assembled URLs — the Compose file builds `SPINNERET_DATABASE_URL` from them. On the host you assemble the URL yourself.

---

## spinneret-server

One long-running control-plane instance. It serves the node API, the admin API, the console and — depending on its role — the background workers.

### Flags

```text
Usage: spinneret-server [--role all|api|worker] [--migrate] [--version]

Runs a Spinneret instance configured through SPINNERET_* environment variables.

Flags:
  -migrate
    	apply pending database migrations at startup
  -role string
    	instance role: all, api or worker (overrides SPINNERET_ROLE)
  -version
    	print the version and exit
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--role` | empty | `all`, `api` or `worker`. When set, it overrides `SPINNERET_ROLE`. |
| `--migrate` | `false` | Apply pending migrations at startup instead of refusing to start. |
| `--version` | `false` | Print `spinneret-server <version>` and exit 0. |
| `-h`, `--help` | — | Print the usage above and exit 0. |

These are Go-style flags: `-role api` and `--role api` are both accepted, and so is `--role=api`. There are no positional arguments — passing any is an error.

**Note.** `--migrate` is convenient for a single-instance deployment and a trap for a multi-instance one: several instances starting together would each try to migrate. The migration is serialized by an advisory lock so it is safe, but the clean pattern — and the one the Compose stack uses — is a separate `spnr migrate up` step that must complete before the servers start.

### Environment

Everything else comes from `SPINNERET_*` environment variables. The required minimum is:

| Variable | Notes |
| --- | --- |
| `SPINNERET_DATABASE_URL` | PostgreSQL connection URL. |
| `SPINNERET_REDIS_URL` **or** `SPINNERET_REDIS_ADDRS` | Valkey/Redis. |
| `SPINNERET_KEK_FILE` **or** `SPINNERET_KEKS` | At least one key-encryption key. |

Everything has a default or is optional. `SPINNERET_HTTP_ADDR` defaults to `:8080`, `SPINNERET_ROLE` to `all`, `SPINNERET_LOG_LEVEL` to `info`, `SPINNERET_LOG_FORMAT` to `json`, `SPINNERET_SHUTDOWN_TIMEOUT` to `30s`. The complete table is [Configuration reference](./03-configuration.md); `spnr config check` validates it.

An invalid configuration is reported as a list before anything is connected:

```text
spinneret-server: invalid configuration:
SPINNERET_ROLE must be one of all, api, worker (got "leader")
SPINNERET_REPORT_SHARDS must be between 1 and 255
```

### Roles

| Role | Serves the API and the console | Runs the background workers |
| --- | --- | --- |
| `all` (default) | yes | yes |
| `api` | yes | no |
| `worker` | no | yes |

A `worker` instance still binds `SPINNERET_HTTP_ADDR` and serves `GET /healthz`, `GET /readyz` and — when `SPINNERET_METRICS_ADDR` is empty — `GET /metrics`. Nothing else: the node API, the admin API and the console are not mounted. So workers can be health-checked and scraped exactly like API instances.

The workers are the report consumers and the scheduled jobs. A deployment must have at least one instance whose role runs workers, otherwise reports pile up in the Redis streams and nothing is ever classified. Splitting the roles lets you scale the two independently — see [Operations runbook](./16-operations.md).

### Startup

1. Flags are parsed, then the environment is loaded and validated.
2. The PostgreSQL pool is opened, and immediately the schema version is compared with the migrations embedded in the binary:
   - equal — continue;
   - database newer — log a warning and continue (a rolling downgrade);
   - database older and `--migrate` not set — **fail to start** with `database schema is at version N, binary expects M: run spnr migrate up`;
   - database older and `--migrate` set — apply the migrations, then continue.

   The partitions of the current time window are then ensured. All of this happens **before** Redis or ClickHouse is contacted, so a schema mismatch fails fast and tells you nothing about the rest of the stack.
3. Redis is opened, then ClickHouse when `SPINNERET_CLICKHOUSE_URL` is set (otherwise the instance logs `clickhouse disabled: raw report events are not stored`), then the key-encryption keys are loaded.
4. On an instance that runs workers, the Redis hot state is ensured: if the epoch key is missing it is rebuilt from PostgreSQL first. Readiness stays negative until that finishes.
5. The listeners come up and the instance logs `spinneret started` with its role, address and version.

Two HTTP endpoints report the state, both unauthenticated and both served by every role:

| Endpoint | Behaviour |
| --- | --- |
| `GET /healthz` | Always `200 {"status":"ok"}` while the process can serve HTTP. Use it as the liveness probe. |
| `GET /readyz` | Runs the `postgres`, `redis`, `catalog` and `hotstate` checks concurrently with a 2 s budget. `200 {"status":"ok","checks":{…}}` when all pass; `503 {"status":"unavailable","checks":{…}}` with the failing reason per check otherwise; `503 {"status":"draining"}` during shutdown. Use it as the readiness probe and as the load balancer's health check. |

### Signals, shutdown and draining

`spinneret-server` handles **SIGINT** and **SIGTERM**. Both start the same graceful shutdown. A **second** signal terminates the process immediately — the default handler is restored after the first one, so a shutdown that hangs can always be cut short.

The shutdown sequence, bounded end to end by `SPINNERET_SHUTDOWN_TIMEOUT` (default `30s`):

1. **Readiness flips to draining.** `/readyz` answers `503 {"status":"draining"}`. `/healthz` keeps answering `200`, so a liveness probe does not kill the process mid-drain.
2. **Keep-alives stop.** Idle pooled connections are closed and every response during the drain carries `Connection: close`, so no load balancer or SDK client sends a request on a connection this instance is about to close.
3. **Drain delay.** The instance waits `min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4)` — 5 s at the default timeout — giving load balancers time to notice the failing readiness check and stop routing. This delay is skipped when the shutdown was caused by a listener failure rather than by a signal.
4. **HTTP shutdown.** In-flight requests finish. Long polls (`WatchConfig`) and event streams are canceled so they do not hold the window open. HTTP gets half of the remaining budget; anything still open when it expires is closed.
5. **Background loops stop tier by tier**, so writers flush what the services produced — queued proxy bindings, for example, get a final 10 s flush.
6. The metrics and pprof servers are closed, then `shutdown complete` is logged with the elapsed time.

Give the orchestrator more grace than `SPINNERET_SHUTDOWN_TIMEOUT`, or it will kill the process in the middle of step 4. The Compose stack sets `stop_grace_period: 40s` against the default 30 s timeout; whatever orchestrator you use, give it a grace period longer than `SPINNERET_SHUTDOWN_TIMEOUT`.

```bash
# Drain one instance by hand
docker compose -f deploy/compose/docker-compose.yml stop --timeout 40 spinneret
```

### Server exit codes

| Code | Meaning |
| --- | --- |
| `0` | Clean shutdown after a signal. Also `--help` and `--version`. |
| `1` | Invalid configuration, startup failure (unreachable dependency, schema behind the binary without `--migrate`), or a listener that failed while running. |
| `2` | Command-line error: an unknown flag or unexpected positional arguments. |

A `2` is a deployment bug — the process will never start until the command line is fixed, so do not let a supervisor retry it forever.

---

## I want to…

| I want to | Command |
| --- | --- |
| Create the database schema on a fresh deployment | `spnr migrate up` |
| Check whether the schema matches the binary before an upgrade | `spnr migrate status` |
| Roll the schema back one version | `spnr migrate down` |
| Create the first console administrator | `spnr admin init --username admin --password-env SPINNERET_ADMIN_PASSWORD` |
| Mint a token for a crawler node | `spnr token create --name <node> --scope lease:acquire --scope report:write --scope config:read` |
| Mint a token limited to one site, forever | `spnr token create --name <node> --scope lease:acquire:<site> --scope report:write:<site> --expires never` |
| Verify the environment of a host before starting the server | `spnr config check` |
| Create the very first key-encryption key | `spnr kek generate --id k1 > /etc/spinneret/kek.key` |
| See which KEK wraps how much data | `spnr kek status` |
| Finish a KEK rotation | `spnr kek rewrap` |
| Rebuild the hot state of one site without downtime | `spnr rebuild --site <site>` |
| Rebuild the entire hot state after a Redis loss | `spnr rebuild` |
| Create a synthetic site and a token for a load test | `spnr seed --site loadtest --identities 100000` |
| Check an instance's readiness from inside its container | `spnr healthcheck --url http://127.0.0.1:8080/readyz` |
| Know which version is deployed | `spnr version` / `spinneret-server --version` |
| Run a server that migrates itself on start | `spinneret-server --migrate` |
| Run an instance that only serves the API | `spinneret-server --role api` |
| Run an instance that only processes reports and jobs | `spinneret-server --role worker` |
| Drain and stop an instance gracefully | send `SIGTERM`, wait longer than `SPINNERET_SHUTDOWN_TIMEOUT` |
| Run any `spnr` command against the Compose stack | `docker compose -f deploy/compose/docker-compose.yml run --rm --entrypoint /usr/local/bin/spnr migrate <command>` |

---

## Next

- [Installation and deployment](./02-installation.md) — where these binaries run and how the stack is wired.
- [Configuration reference](./03-configuration.md) — every `SPINNERET_*` variable both binaries read.
- [Tenants, users and tokens](./11-access-control.md) — what the scopes of `spnr token create` actually grant.
- [Secret vault](./10-secrets.md) — the key hierarchy behind `spnr kek`.
- [Operations runbook](./16-operations.md) — upgrades, rebuilds, KEK rotation and draining as procedures.
- [Troubleshooting](./18-troubleshooting.md) — what to do when one of these commands fails.
