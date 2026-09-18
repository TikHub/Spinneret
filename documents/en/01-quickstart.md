# Quick start

**From an empty host to a node that has leased an identity, sent a request and reported the result —
in about half an hour, understanding what you did. Docker only; everything else runs in containers.**

[中文](../zh/01-quickstart.md)

---

## Contents

- [What you are about to build](#what-you-are-about-to-build)
- [Before you start](#before-you-start)
- [Step 1 — Get the stack running](#step-1--get-the-stack-running)
  - [Route A — the guided installer](#route-a--the-guided-installer)
  - [Route B — git clone and Compose by hand](#route-b--git-clone-and-compose-by-hand)
- [Step 2 — Check that it is healthy](#step-2--check-that-it-is-healthy)
- [Step 3 — Sign in, and back up the key](#step-3--sign-in-and-back-up-the-key)
- [Step 4 — A tour of the Overview page](#step-4--a-tour-of-the-overview-page)
- [Step 5 — Create a site, a client and two endpoint groups](#step-5--create-a-site-a-client-and-two-endpoint-groups)
- [Step 6 — Create an identity type and import two identities](#step-6--create-an-identity-type-and-import-two-identities)
- [Step 7 — Create an API token for the node](#step-7--create-an-api-token-for-the-node)
- [Step 8 — Lease an identity](#step-8--lease-an-identity)
- [Step 9 — Report a success, then a failure](#step-9--report-a-success-then-a-failure)
- [Step 10 — See what happened](#step-10--see-what-happened)
- [Step 11 — The complete example node](#step-11--the-complete-example-node)
- [Tearing it down](#tearing-it-down)
- [If something went wrong](#if-something-went-wrong)
- [Next](#next)

---

## What you are about to build

Spinneret is a control plane. A crawler node gets two things from you — a server URL and an API
token — and everything else at request time: which identity to use, through which proxy, whether
the endpoint is currently circuit-broken, what its configuration is. In return the node reports what
happened, and the server turns those reports into cooldowns, bans, health scores and circuit
breaking.

```text
Acquire(site, client, uri)          ->  identity + credential + proxy + lease
  ... the node sends the request to the target ...
Report(lease_id, status, markers)   ->  the server classifies, cools down, bans, trips breakers
```

By the end of this page you will have run that loop by hand with `curl`, and watched the server
cool an identity down because of it. Nothing was deployed to make that happen — that is the point of
the design.

Three quantities to keep in mind while you read: the stack is **one binary** (`spinneret-server`)
plus an administration CLI (`spnr`) in one image, in front of PostgreSQL, Valkey and (optionally)
ClickHouse; the console is compiled **into** the binary, so there is no separate web service; and
everything you do in the console is a call to the same API a node calls.

---

## Before you start

| | |
| --- | --- |
| Docker Engine | 24 or newer with the Compose plugin — `docker compose version` must report **2.24 or newer** |
| Architecture | `x86_64` or `arm64` for the published images; any architecture if you build from source |
| RAM | 4 GiB for the stack as shipped; 2 GiB works with ClickHouse disabled |
| Disk | 20 GiB |
| Ports | one free host port, `8080` by default |
| Host tools (Route A) | `bash`, `git`, `curl`, `awk`, `base64`, the usual coreutils, and either `/dev/urandom` or `openssl` |

Nothing is installed on the host itself: PostgreSQL, Valkey, ClickHouse, the server replicas and the
load balancer all run as containers and their data lives in Docker named volumes.

4 GiB is a hard floor for the stack as shipped, not a soft one: ClickHouse idles near 1.2 GiB
whatever its caches are set to, and PostgreSQL allocates 512 MiB of shared buffers at start.

The complete requirement list, every knob, several stacks on one host and the non-Docker path are in
[Installation and deployment](./02-installation.md).

---

## Step 1 — Get the stack running

Two routes to the same stack. Route A is three commands and a handful of questions; Route B is the
same thing by hand, for when you want to see every step. Pick one and carry on to
[Step 2](#step-2--check-that-it-is-healthy).

### Route A — the guided installer

`install/install.sh` detects the host, offers the right way to install Docker if it is missing,
clones the repository, generates the passwords and the key-encryption key **on this machine**, writes
the Compose overrides for this host, pulls or builds the image, applies migrations as an explicit
step, starts everything, polls `/readyz` until every dependency reports `ok`, creates the first
administrator and prints the console URL with the credentials.

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh -o install.sh
less install.sh          # read it first; you are about to run it
bash install.sh
```

That order is not ceremony. Anything you pipe into a shell runs as you, and on the Docker step the
script offers to run something as root. It is written to be read: it says what it is about to do
before each step and asks before every change.

`install/install.zh.sh` is the same script with Chinese prompts — same options, same environment
variables, same management menu, same boundaries.

Piping works too, questions included, because answers are read from `/dev/tty` rather than from
stdin — with `curl | bash`, stdin *is* the script:

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh | bash
```

With no terminal at all the script says so and stops, rather than quietly taking a default for a
question like "which address do I publish on". For a genuinely unattended run, say so explicitly:

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh | bash -s -- --yes
```

#### The seven questions

Each has a default you can accept with enter.

| | Question | Default |
| --- | --- | --- |
| 1 | Where to install | `/opt/spinneret` as root, `~/spinneret` otherwise |
| 2 | Keep the console on `127.0.0.1`? | yes |
| 3 | Which port | `8080` |
| 4 | Administrator username | `admin`, validated against the server's own rule (`^[a-z0-9][a-z0-9._-]{2,63}$`) so a rejected name costs a keystroke rather than a failed bootstrap |
| 5 | How many server replicas | one per two cores, clamped to 1–4, forced to 1 under 4 GiB |
| 6 | The observability profile | off |
| 7 | Published image or build from source | published, and only asked **after** checking whether `ghcr.io/tikhub/spinneret:latest` can actually be fetched from this host |

Saying no to question 2 publishes on `0.0.0.0`, and the script warns you: the console is an admin
surface and `/metrics` is on the same listener with no authentication, so that wants a TLS reverse
proxy in front and `SPINNERET_COOKIE_SECURE=true` in `.env`. Question 5 matters for upgrades — with
one replica an upgrade has a short window where nothing serves; with two there is none, at roughly
150–500 MiB more RAM. Pulling the image takes about a minute; building takes 5–15 minutes and about
2 GB of build cache on a first build.

Every answer can be preset from the environment, which is what makes `--yes` a complete unattended
install rather than just "all defaults": `SPINNERET_PROJECT`, `SPINNERET_INSTALL_DIR`,
`SPINNERET_BIND_HOST`, `SPINNERET_PORT`, `SPINNERET_ADMIN_USERNAME`, `SPINNERET_REPLICAS`,
`SPINNERET_ENABLE_OBSERVABILITY`, `SPINNERET_USE_PUBLISHED`, `SPINNERET_IMAGE`,
`SPINNERET_IMAGE_TAG`, `NO_COLOR`.

```bash
SPINNERET_INSTALL_DIR=/srv/spinneret SPINNERET_PORT=9000 \
SPINNERET_REPLICAS=2 SPINNERET_IMAGE_TAG=v0.1.0 \
  bash install.sh --yes
```

The administrator password and the two database passwords are **always** generated on the machine
and never taken from the environment.

#### The four flags

| Flag | |
| --- | --- |
| `--yes`, `-y` | Take the default answer to every question. Over an install that already exists it changes **nothing** and says so |
| `--check` | Detect the host and print what would happen, then stop. Changes nothing, writes nothing, asks nothing |
| `--manage` | Go straight to the menu for an install that already exists |
| `--help`, `-h` | The option list |

`--check` is the one to run first on an unfamiliar host: it reports the distribution, architecture,
cores and RAM, whether Docker is present, running and new enough, whether `git` and `curl` are there
with the package-manager command for each that is missing, and whether the published image can
actually be fetched from this network.

#### What it prints when it is done

```text
==> Done

    Console     http://127.0.0.1:8080
    Username    admin
    Password    <24 random alphanumerics, shown here>
    Change it after the first sign-in. Until you do, it also sits in .env in clear.

    Directory   /opt/spinneret
    Control     /opt/spinneret/spnrctl ps | logs -f spinneret | restart lb | down
    Menu        bash install.sh --manage
    Environment /opt/spinneret/deploy/compose/.env  (0600 — the database passwords)
    Vault key   /opt/spinneret/deploy/compose/secrets/kek.key  (back this up; nothing decrypts without it)

    Next, from this machine:
      # a token for one crawler node
      /opt/spinneret/spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate \
          token create --name node-1 --scope lease:acquire --scope report:write --scope config:read

      # then point the node at the server
      export SPINNERET_URL=http://127.0.0.1:8080
      export SPINNERET_TOKEN=<the token printed above>

    Quickstart  https://github.com/TikHub/Spinneret/blob/main/documents/en/01-quickstart.md
    快速开始    https://github.com/TikHub/Spinneret/blob/main/documents/zh/01-quickstart.md
```

Two files matter from now on: `deploy/compose/.env` (mode `0600`, the database passwords and the
administrator password) and `deploy/compose/secrets/kek.key` (the key-encryption key). See
[Step 3](#step-3--sign-in-and-back-up-the-key).

`spnrctl` is the single entry point for this deployment. It is `docker compose` carrying the four
things that have to be right every single time and whose absence produces confusing behaviour rather
than an error: the project name, the compose files in the right order, the working directory that
makes `./config`, `./secrets` and `../..` resolve, and `COMPOSE_ENV_FILES`. Everything after the name
goes straight to Compose.

```bash
cd /opt/spinneret
./spnrctl ps                        # what is running
./spnrctl logs -f spinneret         # follow the server log
./spnrctl restart lb                # restart one service
```

Run `bash install.sh` again later and it works out which job it is doing: an existing install (found
by asking Docker which directory the Compose project was started from) opens a management menu
instead — status, upgrade, accounts, tokens, hot-state rebuild, configuration, health, logs,
restart, backup, restore, disk, uninstall. `install/README.md` documents every question, every flag
and every file it writes, in both languages.

Now go to [Step 2](#step-2--check-that-it-is-healthy).

### Route B — git clone and Compose by hand

The same stack, three commands, nothing generated for you that you cannot read first.

```bash
git clone https://github.com/TikHub/Spinneret.git
cd Spinneret
```

**1. Write the environment and the key.** `compose-init.sh` creates, only if missing,
`deploy/compose/.env` from the shipped `.env.example` with random `PG_PASSWORD`,
`CLICKHOUSE_PASSWORD` and `SPINNERET_ADMIN_PASSWORD`, and `deploy/compose/secrets/kek.key` with a
fresh 32-byte key in the form `k1:<base64>`. It is safe to re-run — existing files are kept — and it
needs `openssl` on `PATH`. Both files are git-ignored and excluded from the Docker build context.

```bash
./scripts/compose-init.sh
chmod 0700 deploy/compose/secrets      # the script does not do this; the installer does
```

```text
created /path/to/Spinneret/deploy/compose/.env
created /path/to/Spinneret/deploy/compose/secrets/kek.key (back it up: data encrypted with it is unrecoverable without it)
```

The `chmod` is the one thing `compose-init.sh` leaves to you: it writes `kek.key` as `0644` on
purpose (see [Step 3](#step-3--sign-in-and-back-up-the-key)), and the protection belongs on the
directory, which it creates world-readable.

**2. Build and start.** PostgreSQL, Valkey, ClickHouse, the one-shot migration job, two server
replicas and the Caddy load balancer.

```bash
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
```

`--build` takes 5–15 minutes on a first build and needs to reach the Go and Node package
registries; to pull the published image instead, see
[Installation → Published image or build from source](./02-installation.md#published-image-or-build-from-source).
`--wait` returns when every container reports healthy. Migrations are serialized by a PostgreSQL
advisory lock, so several instances starting at once is safe.

**3. Create the first administrator.** The one-shot `init-admin` service creates the platform
administrator (`SPINNERET_ADMIN_USERNAME`, default `admin`), the tenant `default`, the namespace
`default` and its four built-in default policies. It is idempotent:

```bash
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

```text
already initialized
```

is what a second run prints, and it exits 0.

**One note that saves an hour.** Compose reads `.env` from **the Compose file's directory**, not from
your current one, and the stack's relative paths (`./config`, `./secrets`, the `../..` build context)
resolve against that same directory. Either `cd deploy/compose` and use plain `docker compose`, or
use the `spnrctl` the installer writes. `required variable PG_PASSWORD is missing a value` means
that `.env` is not there at all — run `./scripts/compose-init.sh`. Your current directory does not
affect it: `-f deploy/compose/docker-compose.yml` works from anywhere, because Compose takes the
project directory from the Compose file. What the current directory *does* affect is a bare
`-f docker-compose.yml`, which only resolves from `deploy/compose`.

---

## Step 2 — Check that it is healthy

```bash
cd deploy/compose
docker compose ps -a --format 'table {{.Service}}\t{{.Status}}'
```

```text
SERVICE           STATUS
clickhouse        Up 10 hours (healthy)
lb                Up 20 hours (healthy)
migrate           Exited (0) 20 seconds ago
postgres          Up 28 hours (healthy)
spinneret         Up 6 minutes (healthy)
spinneret         Up 6 minutes (healthy)
valkey            Up 11 hours (healthy)
```

`migrate` having exited `0` is correct — it is a one-shot job the server depends on completing. Two
`spinneret` rows is the default replica count.

Then ask the server itself. `/healthz` says only that the process is alive; `/readyz` names every
dependency:

```bash
curl -s 127.0.0.1:8080/healthz
curl -s 127.0.0.1:8080/readyz
```

```json
{"status":"ok"}
```

```json
{"checks":{"catalog":"ok","hotstate":"ok","postgres":"ok","redis":"ok"},"status":"ok"}
```

Those four names are the whole readiness contract, and when one of them is not `ok` its value is the
diagnosis:

| Check | `ok` means | A value other than `ok` |
| --- | --- | --- |
| `postgres` | the connection pool answered a ping | `unreachable` — the password does not match the volume, or the pool is exhausted |
| `redis` | Valkey/Redis answered a ping | `unreachable` — Valkey is down, often OOM-killed on a small host |
| `catalog` | the namespace snapshot has been loaded | `not loaded` — wait; if it persists, check PostgreSQL |
| `hotstate` | the epoch key exists and, on an instance that runs workers, the hot state has finished building | `building` on a first start; `epoch missing (rebuild pending)` after Valkey was flushed or its volume recreated — run `spnr rebuild` |

When any check fails the top-level `status` is `unavailable` and the response code is `503`.

**`/readyz` is what a load balancer must probe, not `/healthz`.** A replica that is shutting down
answers `503 {"status":"draining"}` for the first quarter of `SPINNERET_SHUTDOWN_TIMEOUT` (at most
5 s) *before* it closes its listener, which is what makes a rolling restart invisible to nodes.

Two more checks worth running once, both inside a server container:

```bash
docker compose exec spinneret spnr migrate status
```

```text
database schema version 6 (up to date)
```

(The number is whatever the schema version of your release is; `up to date` is the part that
matters.)

```bash
docker compose exec spinneret spnr config check
```

```json
{
  "acquire_fleet_inflight": "64",
  "acquire_max_inflight": "0",
  "clickhouse_url": "clickhouse://spinneret:xxxxx@clickhouse:9000/spinneret",
  "database_url": "postgres://spinneret:xxxxx@postgres:5432/spinneret?sslmode=disable",
  "http_addr": ":8080",
  "instance_id": "899a20dbb6ef-ac8841",
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

That is the whole `SPINNERET_*` environment as the server actually parsed it, with every credential
replaced by `xxxxx`. It is the pre-flight for a hand-edited `.env`: if a variable you set is not in
there with the value you expect, the server is not using it. Every field is documented in
[Configuration reference](./03-configuration.md).

---

## Step 3 — Sign in, and back up the key

### Where the administrator password comes from

| Route | Where it is |
| --- | --- |
| A — the installer | Printed once at the end, under **Password**. It is also written to `.env` as `SPINNERET_ADMIN_PASSWORD` |
| B — by hand | `grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env` |

```bash
grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env
```

It is 24 random alphanumerics (20 from `compose-init.sh`), generated on the machine. Only the
one-shot `init-admin` service reads it; the running server never does.

Open <http://127.0.0.1:8080> and sign in as `admin` with that password.

**Change it after the first sign-in.** Until you do it also sits in `.env` in clear. The account menu
in the top-right corner leads to **Profile**, where changing your password signs out all your *other*
sessions and keeps the current one. The installer's management menu can do it too, and offers to
update `SPINNERET_ADMIN_PASSWORD` in `.env` so the file does not go stale.

A few facts about the session that save confusion later: it is a server-side session in
Redis/Valkey behind the `spinneret_session` cookie, its lifetime is `SPINNERET_SESSION_TTL`
(default `12h`) and it slides — it is extended on use. Sign-in attempts are throttled at 5 failures
per username and 20 per client IP address, both over 15 minutes; exceeding either answers
`login_throttled` and the form tells you how long to wait. The console follows your browser language
and the switch is in the header, next to the theme menu.

### Back up the key before you put data in

`deploy/compose/secrets/kek.key` is the key-encryption key. Every identity payload, every proxy URL,
every vault secret and every notification credential in this deployment is encrypted with a
per-record data key, and every one of those data keys is wrapped with this one. Only wrapped keys and
ciphertext are ever stored — the key itself never touches the database.

**There is no recovery path without it.** A database dump restored against the wrong key fails with
`load dedupe_pepper system key (is the KEK the one used to initialize this database?)`, and nothing
but the right key fixes that.

```bash
cp deploy/compose/secrets/kek.key ~/somewhere-safe/spinneret-kek-$(date -u +%Y%m%d).key
cp deploy/compose/.env            ~/somewhere-safe/spinneret-env-$(date -u +%Y%m%d)
```

Copy both off the machine now, while there is nothing to lose. `.env` belongs in the same backup
because its passwords are what the database volumes were built with; a new file would not open them.

**Note.** `kek.key` is mode `0644` **on purpose**, and that is not a mistake to fix — the protection
belongs on the directory, which the installer sets to `0700` and which you set yourself on Route B
(`chmod 0700 deploy/compose/secrets`). Compose bind-mounts that exact file into a container that runs as the distroless
`nonroot` user (uid 65532), which matches no host user; a `0600` file is unreadable there and the
server exits with `vault: read kek file "/run/secrets/kek": … permission denied`.

What a complete backup is, and how to prove it works by restoring it into an empty stack, is in
[Operations → Backups](./16-operations.md#backups).

---

## Step 4 — A tour of the Overview page

You land on **Overview**. It is the live health of one namespace, it refreshes itself every 10
seconds, and the window selector in its header (`1m`, `5m`, `15m`, `1h`) decides whether a number is
a spike or a trend.

![The overview page of the console](../images/overview.png)

Top to bottom:

| Section | What it tells you |
| --- | --- |
| Six stat tiles | **Available identities**, **Acquire QPS** (with the acquire failure ratio under it), **Report QPS** (with pending reports), **Success ratio** (with the unknown ratio), **Risk ratio**, **Open breakers** (with the half-open count) |
| Two charts | Acquire rate over the last hour, and success and risk ratio over the last hour |
| Sites | One card per site: identities broken down by state, available identities, proxies by state, acquire and report rates, the success/risk/unknown/client-error ratios, open and half-open breakers, and the endpoint groups below their low watermark |
| Open breakers | Every open or half-open breaker in the namespace, with the endpoint group and how long it stays open |
| Low watermark warnings | Endpoint groups whose available identities fell below the threshold you set on them |
| Nodes (last hour) | Per node: acquires, reports, leases that were never reported, rejected calls and the unreported ratio |

Three of those tiles are the ones you will end up watching. **Success ratio** turns amber below 80 %
(and stays neutral while there is no report traffic at all), **Risk ratio** turns red at 10 % or
above, and **Open breakers** turns red the moment one is open — the colour is never the only carrier
of the meaning, the number is right there. A site card is the answer to "*which* site is unhappy",
which is why the tour of a real incident usually goes Overview → the unhappy site card → Identities
or Breakers.

Right now the page is empty of numbers, because there is no site yet. The next four steps fix that.
The rest of the console — the sidebar's six groups, the tenant and namespace switchers, the page
introduction cards, what a read-only user sees — is in
[Console overview](./05-console-overview.md).

---

## Step 5 — Create a site, a client and two endpoint groups

This is the structure everything else hangs from, so it is worth understanding rather than copying.

| Term | What it is |
| --- | --- |
| **site** | One target, inside one namespace. Cooldowns, bans, breakers and policy bindings are all scoped to it |
| **client** | A kind of caller of that site — `web`, `mobile`, `partner`. Identities belong to one client |
| **endpoint group** | A set of URIs with similar risk behaviour. **The unit of rotation, cooldown and circuit breaking** |
| **URI rule** | How a request path is mapped to an endpoint group: `exact`, `template`, `prefix` or `regex` |

We will build the site `example-site` with one client `web` and two endpoint groups: `search` for
everything under `/search`, and `detail` for `/detail/{id}`.

### In the console

1. **Sites** → **New site**. Name `example-site` (`^[a-z0-9_][a-z0-9_.-]{0,63}$` — lower-case
   letters, digits, `_`, `.` and `-`, up to 64 characters, starting with a letter, digit or `_`,
   and it cannot be changed later), display name *Example site*, clients `web`
   — type it and press enter. Create.
2. The site appears with **one** endpoint group already: `_default`. Every client gets one, and it
   receives every request whose path matches no rule. You cannot delete it.
3. **New endpoint group** → client `web`, name `search`. Leave the low watermark at `0` (it is an
   alert threshold on available identities; `0` turns the alert off). Create.
4. On the new group, **Edit URI rules** → **Add rule** → kind `prefix`, pattern `/search`. Save.
5. Repeat for `detail`, with a `template` rule `/detail/{id}`.

Rules are matched in a fixed priority — exact, then template (more literal segments first), then
prefix (longest first), then regex (in list order), then `_default` — so the list order only matters
for regex rules and template ties.

### Over the API

Everything the console does is a call to the same API. These calls need a token with the `admin`
scope (see [Step 7](#step-7--create-an-api-token-for-the-node)) or a console session; the examples
use a token.

```bash
export SPINNERET_URL=http://127.0.0.1:8080
export SPINNERET_ADMIN_TOKEN=spn_…          # a token with the admin scope

curl -sS -X POST "$SPINNERET_URL/spinneret.v1.SiteAdminService/CreateSite" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","name":"example-site","display_name":"Example site","clients":["web"]}'
```

```json
{
  "site": {
    "id": "sit_01a0b5bb20427b289c8fe36bfdd6afae",
    "namespace": "default",
    "name": "example-site",
    "display_name": "Example site",
    "description": "",
    "clients": ["web"],
    "paused": false,
    "paused_reason": "",
    "paused_at": null,
    "endpoint_group_count": 1,
    "identity_count": 0,
    "created_at": "2026-09-18T18:15:34.722764Z",
    "updated_at": "2026-09-18T18:15:34.722764Z"
  }
}
```

`endpoint_group_count: 1` is the `_default` group the site was created with.

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.SiteAdminService/CreateEndpointGroup" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site","client":"web","name":"search",
       "rules":[{"kind":"prefix","pattern":"/search"}]}'

curl -sS -X POST "$SPINNERET_URL/spinneret.v1.SiteAdminService/CreateEndpointGroup" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site","client":"web","name":"detail",
       "rules":[{"kind":"template","pattern":"/detail/{id}"}]}'
```

```json
{
  "endpoint_group": {
    "id": "eg_01a0b5bb205d7df6b44e3b610d32f878",
    "site": "example-site",
    "site_id": "sit_01a0b5bb20427b289c8fe36bfdd6afae",
    "client": "web",
    "name": "search",
    "description": "",
    "low_watermark": 0,
    "rules": [
      {"id": "uri_01a0b5bb205e7ef19655a7f90724c795", "kind": "prefix", "pattern": "/search", "position": 0}
    ],
    "available_identities": 0,
    "breaker_state": "closed",
    "created_at": "2026-09-18T18:15:34.749940Z",
    "updated_at": "2026-09-18T18:15:34.749940Z"
  }
}
```

**You do not need to create a policy.** Creating the namespace installed four built-in default
policies — `default-rotation`, `default-signal`, `default-action` and `default-breaker` — published
as version 1 and bound at namespace level. Everything in the next six steps is decided by them. The
numbers that matter here are in the defaults:

| Default | Value | Where it shows up |
| --- | --- | --- |
| rotation `lease_ttl` | `2m` | how long a lease lives without a renewal |
| rotation `max_concurrent_leases` | `1` | one lease per identity at a time — exclusive |
| rotation `reuse_interval` | `0s` | no minimum gap between two uses of the same identity — a released identity is immediately a candidate again |
| rotation `proxy.mode` | `none` | no proxy is assigned, so you need no proxy pool to finish this page |
| signal rule `rate-limited` | `http_status: [429]` → outcome `rate_limited` | Step 9 |
| action rule `rate-limited-cooldown` | cooldown, scope `identity_endpoint`, base `60s`, multiplier `2`, max `30m` | Step 9 |
| health | baseline `70`, `alpha 0.1`, success observation `100`, `rate_limited` observation `30` | the health score moves |
| breaker | window `60s`, `min_requests: 50`, opens at `risk_ratio ≥ 0.4` or `success_ratio ≤ 0.2`, `open_duration: 2m` | Step 11 |

Reading and changing them: [Policies](./08-policies.md).

---

## Step 6 — Create an identity type and import two identities

An **identity** is one credential the scheduler leases out. An **identity type** is the schema of a
family of them: which fields a payload has, which of them are sensitive, which of them identify the
identity (so two imports of the same credential are the same identity), how a new one is activated,
and how the payload is rendered into the **credential** a node receives.

### In the console

**Identity Types** → **New type**. Pick site `example-site`, write the YAML below (the **Insert
example** menu offers a cookie type and a device type to start from), check the **Delivery preview**
tab to see exactly what a node will get, then **Create**.

```yaml
name: web_cookie
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
```

Four lines of that are worth pausing on:

- `cookie_map` accepts an object of strings, a `Cookie` header string **or** an array of
  `{name, value}` objects, which is what makes a browser export usable directly.
- `sensitive: true` masks the value everywhere it is displayed, keeping the last four characters
  (`••••b2c3`). It changes display only — the whole payload is always encrypted either way.
- `unique_by: [cookies.sessionid]` is the deduplication key. Spinneret stores an HMAC-SHA256 of the
  canonical value list, keyed with a server-side pepper; the plaintext key is never stored. Two rows
  with the same session id are the same identity, and a second import updates it instead of
  inserting a duplicate.
- `activation: probe` (the default) means a new identity starts in state `pending` and only reaches
  `active` when a node reports a **success** on a lease of it. Pending identities are still leased,
  at a reduced weight and at most two at a time, and their leases are flagged `probe: true`. Use
  `immediate` when the credential was already validated somewhere else.

Templates support exactly one construct, `{{ path }}` — no pipes, no function calls, no
conditionals. That rules out template injection and keeps rendering cheap enough for the `Acquire`
hot path.

Over the API it is one call:

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.IdentityAdminService/CreateIdentityType" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site",
       "spec_yaml":"name: web_cookie\nclient: web\nfields:\n  cookies: { type: cookie_map, required: true, sensitive: true }\n  user_agent: { type: string }\nunique_by: [cookies.sessionid]\nactivation: probe\ndeliver:\n  cookies: \"{{ cookies }}\"\n  headers:\n    User-Agent: \"{{ user_agent }}\"\n"}'
```

The response echoes the stored spec with `site: example-site` written into it, the generated JSON
Schema (draft 2020-12, `additionalProperties: false`) that an external importer can validate rows
against, and `version: 1`.

### Import two identities

Import takes JSON Lines or CSV, one site and one identity type at a time. Two identities are enough
to see rotation happen.

**Identities** → **Import**, or paste these two lines:

```json
{"cookies": "sessionid=a1b2c3; csrf_token=xyz", "user_agent": "example-crawler/1.0"}
{"cookies": "sessionid=d4e5f6; csrf_token=uvw", "user_agent": "example-crawler/1.0"}
```

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.IdentityAdminService/ImportIdentities" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site","type":"web_cookie","format":"jsonl",
       "data":"{\"cookies\": \"sessionid=a1b2c3; csrf_token=xyz\", \"user_agent\": \"example-crawler/1.0\"}\n{\"cookies\": \"sessionid=d4e5f6; csrf_token=uvw\", \"user_agent\": \"example-crawler/1.0\"}\n"}'
```

```json
{"created": 2, "updated": 0, "unchanged": 0, "failed": []}
```

Run the same import again and it answers `{"created": 0, "updated": 0, "unchanged": 2, "failed": []}`
— that is the deduplication key doing its job, and it is why a credential-refresh service can simply
re-post its whole file.

On the **Identities** page both rows now show state **pending**, type `web_cookie`, and a global
score of 70 — the health baseline. The cookie values are masked; revealing one needs the
`identity:reveal` permission and is audited.

![The identities list](../images/identities.png)

The complete story — every field kind, delivery segments, the state machine, CSV, importing at
scale, accounts, the cooldown heatmap — is in [Identities and accounts](./06-identities.md).

---

## Step 7 — Create an API token for the node

A node needs exactly two things: the server URL and a token. The token carries the tenant and the
namespace, which is why a node never sends either.

### In the console

**Tokens** → **Create token**. Name it after the node (`node-1`), and in the scope builder take the
**Crawler node** preset — `lease:acquire`, `report:write`, `config:read` — then narrow the first two
to this site. Leave the IP allowlist and the rate limit empty for now. Set **Expires in** to `30d`.

The plaintext is shown **once**, in a dialog that makes you confirm you have saved it. The server
stores only its SHA-256 digest; there is no call that returns it again. If you lose it, revoke the
token and create another.

### On the command line

`spnr` lives in the image as `/usr/local/bin/spnr` and needs only PostgreSQL, so run it through the
one-shot `migrate` service rather than through a replica that may not be healthy:

```bash
cd deploy/compose
docker compose run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  token create --tenant default --namespace default --name node-1 \
    --scope lease:acquire:example-site \
    --scope report:write:example-site \
    --scope config:read \
    --expires 720h
```

```text
spn_EN36ISldyttiSq4ksD1xCs1ShzBOpkGevq8mpNk6AyN
```

The only thing on stdout is the token, which makes it safe to capture in a variable. On an install
made by the installer the same command is
`./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate token create …`, or
*Manage* → 4 in the installer's menu.
`--expires` takes `720h`, `30d`, or `0`/`never` for a token that does not expire; the default is
`720h`.

### Why those three scopes

| Scope | What it unlocks | Argument |
| --- | --- | --- |
| `lease:acquire:example-site` | `Acquire`, `AcquireBatch`, `Renew`, `Release` on that one site | an **exact** site name; omit it to cover every site of the namespace |
| `report:write:example-site` | `Report` for leases of that site | an exact site name. The batch is admitted if the token has `report:write` anywhere in the namespace, then each report is checked against the site of its lease and rejected individually with `scope_missing` |
| `config:read` | `GetConfig`, `BatchGetConfig`, `WatchConfig` | an optional **glob** over the config group (`config:read:crawler*`); omitted means every group |

What the node does **not** get is the point. It cannot create identities (`identity:write`), publish
configuration (`config:publish`), read the vault (`secret:read`, which is the one scope that *must*
carry an argument), or touch anything administrative (`admin`). Give every node its own token with
only the scopes its job needs; a leaked token is then bounded by what that node was allowed to do.
Roles, permissions, IP allowlists, rate limits and rotation are in
[Tenants, users and tokens](./11-access-control.md).

```bash
export SPINNERET_TOKEN=spn_EN36ISldyttiSq4ksD1xCs1ShzBOpkGevq8mpNk6AyN
```

---

## Step 8 — Lease an identity

Everything a node calls is a unary `POST` to `<base-url>/spinneret.v1.<Service>/<Method>` with a
JSON body. There is no `/api` prefix and no path versioning — the version is in the package name —
and no client library is needed.

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Acquire" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: node-1' \
  -d '{"site":"example-site","client":"web","uri":"/search?q=shoes","wait_ms":500}'
```

```json
{
  "lease": {
    "lease_id": "lse_01a0b5b5707d7defa2af06b7ed755f0e_6v_0c",
    "identity_id": "idt_01a0b5b4f9797382ae09590b2b49bd9c",
    "identity_type": "web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-18T18:11:22.045Z",
    "sticky": false,
    "probe": true
  },
  "credential": {
    "cookies": {"csrf_token": "uvw", "sessionid": "d4e5f6"},
    "cookie_header": "",
    "headers": {"User-Agent": "example-crawler/1.0"},
    "query": {},
    "json": null,
    "values": {}
  },
  "proxy": null,
  "hints": {"renew_before_ms": 30000}
}
```

Read it field by field, because this one response is most of the product:

- `endpoint_group: "search"` — the server matched `/search?q=shoes` against the URI rules of
  `example-site` + `web`. You did not tell it which group; you told it which URI.
- `probe: true` — the identity is still `pending`, so this lease is a probe. Its report decides a
  state transition, which is why a node must report it accurately and never drop it.
- `expires_at` is two minutes out (the default `lease_ttl`) and `renew_before_ms` is 30000, a
  quarter of it: renew when less than that remains, or let the lease expire if the work is done.
- `credential` is the delivery rendering of the payload. `cookies` and `headers` are filled because
  the type's `deliver` map asked for them; `cookie_header`, `query`, `json` and `values` are empty
  because it did not. A node merges these into its HTTP client and never parses the identity itself.
- `proxy: null` — the default rotation policy assigns none. With a proxy pool and
  `proxy.mode: pool` or `bind_identity` this carries a full URL **including credentials**, which is
  why it must never be logged.

The three optional request fields worth knowing now: `endpoint_group` names a group explicitly and
takes precedence over `uri`; `session_key` reuses the same identity for the same key while the
rotation policy's sticky sessions are on; `wait_ms` (0–5000) is how long the server may wait for an
identity to free up before failing.

### What a failure looks like

`max_concurrent_leases` is `1` in the default rotation policy, so with two identities the third
concurrent Acquire has nothing to give. Run the call above twice more, without reporting anything in
between — this time with `-i`, because the interesting part is in the headers:

```bash
curl -i -sS -X POST "$SPINNERET_URL/spinneret.v1.LeaseService/Acquire" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: node-1' \
  -d '{"site":"example-site","client":"web","uri":"/search?q=shoes","wait_ms":500}'
```

```text
HTTP/1.1 429 Too Many Requests
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 20894
```

```json
{"code":"resource_exhausted","message":"no identity available for example-site/web/search"}
```

Two response headers carry the machine-readable part: `Spinneret-Reason` is the stable reason string
you branch on, and `Spinneret-Retry-After-Ms` is how long to wait (clamped to 50 ms – 60 s). Here it
is about 21 seconds, which is when the first identity's lease expires. The reasons a node has to
handle — `no_identity_available`, `no_proxy_available`, `circuit_open`, `site_paused`, `overloaded`,
`scope_missing`, `rate_limited` and the token errors — each have a row in
[Node API reference → The error model](./13-node-api.md#the-error-model), with whether retrying is
safe.

**Acquire is not idempotent**: a retried Acquire issues a second lease. Retry it only when the
failure provably happened before the request was sent, or when the server itself answered
`unavailable`.

---

## Step 9 — Report a success, then a failure

Reporting is the other half of the hot path, and the rule is one sentence: **nodes report facts, not
decisions.** The node says what it observed — status code, latency, size, transport error, page
markers. The server's signal policy classifies that into an outcome, and the action policy decides
what to do about it. That division is what lets you change how a `429` is treated without
redeploying a single node.

### A success

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ReportService/Report" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: node-1' \
  -d '{"reports":[{
        "report_id": "3f1a8c02-5b6d-4e11-9a77-0c2d4e6f8a10",
        "lease_id": "lse_01a0b5b5707d7defa2af06b7ed755f0e_6v_0c",
        "uri": "/search",
        "method": "GET",
        "http_status": 200,
        "latency_ms": 412,
        "response_bytes": 48213,
        "started_at": "2026-09-18T18:09:22.100Z",
        "finished_at": "2026-09-18T18:09:22.512Z",
        "release": true
      }]}'
```

```json
{"accepted": 1, "duplicated": 0, "rejected": []}
```

`report_id` is your idempotency key — a UUID is the obvious choice. Re-sending the same batch comes
back as `{"accepted": 0, "duplicated": 1, "rejected": []}` rather than double-counting, which is what
makes retrying a whole batch safe. `started_at` and `finished_at` are **required**. `release: true`
returns the lease in the same call, which is one round trip instead of two. One call carries 1 to 500
reports, each validated individually: a bad one is listed in `rejected` with a reason while the rest
of the batch is accepted.

Now look at the identity in the console — **Identities**, then click through to the row. Or ask the
API:

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.IdentityAdminService/GetIdentity" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"id":"idt_01a0b5b4f9797382ae09590b2b49bd9c"}'
```

The response also carries `hot_state` and `recent_events`; abridged to the two fields that
changed:

```json
{
  "identity": {
    "id": "idt_01a0b5b4f9797382ae09590b2b49bd9c",
    "site": "example-site",
    "client": "web",
    "type": "web_cookie",
    "state": "active",
    "state_reason": "lifecycle.activate",
    "state_changed_at": "2026-09-18T18:09:29.917769Z",
    "activated_at": "2026-09-18T18:09:29.917769Z",
    "global_score": 72.99756793080434,
    "global_samples": 1,
    "active_leases": 0
  },
  "payload": {
    "cookies": {"csrf_token": "••••", "sessionid": "••••e5f6"},
    "user_agent": "example-crawler/1.0"
  },
  "revealed": false
}
```

Two things changed and neither was your decision: the state went `pending` → `active` with the
reason `lifecycle.activate` (the probe worked, so the credential joined the pool at full weight),
and the health score moved from the baseline 70 towards the success observation of 100 by
`alpha` = 0.1 — about 73. The payload comes back masked, because you did not ask to reveal it.

### A failure

Acquire again. The default strategy is `weighted_random` and `reuse_interval` is `0s`, so **either**
identity can come back — including the one you just activated, which now has the higher score and
therefore the higher weight. Read `identity_id` and `lease_id` out of the response and report a `429`
on that lease:

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.ReportService/Report" \
  -H 'Content-Type: application/json' \
  -H 'Connect-Protocol-Version: 1' \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'X-Spinneret-Node: node-1' \
  -d '{"reports":[{
        "report_id": "7c4e91b6-2d38-4a55-8f0b-1e9c3d5a7b20",
        "lease_id": "lse_01a0b5b5fbdf7e56a5940987aad4c882_6v_0b",
        "uri": "/search",
        "method": "GET",
        "http_status": 429,
        "latency_ms": 120,
        "response_bytes": 512,
        "started_at": "2026-09-18T18:10:00.000Z",
        "finished_at": "2026-09-18T18:10:00.120Z",
        "release": true
      }]}'
```

```json
{"accepted": 1, "duplicated": 0, "rejected": []}
```

The same response — accepting a report says nothing about how it was classified. The classification
is on the identity, a second later. In the console, the identity's row now shows a countdown; the
detail page shows exactly what happened, and so does `GetIdentity` — this time abridged to
`hot_state` and the newest of `recent_events`, for the identity that was still `pending`:

```json
{
  "hot_state": {
    "state": "pending",
    "global_score": 66.00138309415952,
    "global_samples": 1,
    "groups": [
      {"endpoint_group": "search", "score": 66.00138309415952, "samples": 1,
       "consecutive_failures": 1, "cooldown_until": "2026-09-18T18:11:01.472Z",
       "available_at": "2026-09-18T18:11:01.472Z", "in_ready_queue": true},
      {"endpoint_group": "detail", "score": 70, "samples": 0,
       "consecutive_failures": 0, "cooldown_until": null, "available_at": null},
      {"endpoint_group": "_default", "score": 70, "samples": 0,
       "consecutive_failures": 0, "cooldown_until": null, "available_at": null}
    ]
  },
  "recent_events": [
    {
      "created_at": "2026-09-18T18:09:57.784856Z",
      "site": "example-site",
      "subject_kind": "identity",
      "endpoint_group": "search",
      "from_state": "pending",
      "to_state": "pending",
      "action": "cooldown",
      "scope": "identity_endpoint",
      "until": "2026-09-18T18:11:01.472Z",
      "permanent": false,
      "outcome": "rate_limited",
      "policy_id": "pol_01a0afa70af67cdca274e9e5f564ea3c",
      "policy_version": 1,
      "rule": "rate-limited-cooldown",
      "report_id": "7c4e91b6-2d38-4a55-8f0b-1e9c3d5a7b20",
      "lease_id": "lse_01a0b5b5fbdf7e56a5940987aad4c882_6v_0b",
      "actor": "system",
      "reason": "rate-limited-cooldown",
      "shadow": false
    }
  ]
}
```

That one event is the whole chain, written down:

| Field | Meaning |
| --- | --- |
| `outcome: rate_limited` | the signal policy's rule `rate-limited` matched `http_status: [429]` |
| `rule: rate-limited-cooldown`, `policy_version: 1` | the action policy rule that fired, and which version of it |
| `action: cooldown`, `scope: identity_endpoint` | cool this identity down **on this endpoint group only** — `detail` is untouched, and so is the other identity |
| `until` | 60 seconds out: the rule's `base`. A second consecutive one would be 120 s (`multiplier: 2`), capped at `30m` |
| `from_state: pending`, `to_state: pending` | a failed probe does **not** activate the identity; it stays pending |
| `actor: system` | nobody did this by hand |
| `shadow: false` | the policy is in `enforce` mode. In `shadow` mode the event would be recorded and the cooldown not applied |

The score moved the other way too: `rate_limited` has a health observation of 30, so 70 drifted to
about 66. The per-group score is what the rotation strategy weights candidates by, which is how a
credential that keeps failing on one endpoint group quietly stops being chosen for it.

If the Acquire handed you back the identity you activated a moment ago, everything about the
cooldown, the rule and the action is identical — only the two state fields and the arithmetic
differ: `from_state` and `to_state` read `active`, and the score falls from about 73 towards 30 by
`alpha` = 0.1, so about 68.7 rather than 66.

Watch the countdown in the console and the identity comes back on its own after a minute. You did
not write any of this logic; you imported two rows and reported two facts.

---

## Step 10 — See what happened

Two places record it, and they answer different questions.

### The request explorer

**Requests** in the sidebar is every processed report, stored in ClickHouse. Filter it to
`site=example-site` and you see the two reports you sent:

```bash
curl -sS -X POST "$SPINNERET_URL/spinneret.v1.DashboardService/QueryRequestEvents" \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $SPINNERET_ADMIN_TOKEN" \
  -d '{"namespace":"default","site":"example-site","page_size":5}'
```

A request event has 30 fields; the first one below is complete, the second is abridged to the ones
that differ:

```json
{
  "events": [
    {
      "event_time": "2026-09-18T18:10:00.120Z",
      "received_at": "2026-09-18T18:09:57.784Z",
      "site": "example-site",
      "client": "web",
      "endpoint_group": "search",
      "identity_id": "idt_01a0b5b4f979735cbaf25a48ee1fe9f7",
      "identity_type": "web_cookie",
      "proxy_id": "",
      "lease_id": "lse_01a0b5b5fbdf7e56a5940987aad4c882_6v_0b",
      "report_id": "7c4e91b6-2d38-4a55-8f0b-1e9c3d5a7b20",
      "node": "node-1",
      "token_id": "tok_01a0b5b4f99e70558cbb2823bd228a4d",
      "uri": "/search",
      "method": "GET",
      "http_status": 429,
      "error_kind": "",
      "markers": [],
      "outcome": "rate_limited",
      "blame": "both",
      "rule": "rate-limited",
      "latency_ms": 120,
      "response_bytes": "512",
      "suppressed": false,
      "late": false,
      "probe": false
    },
    {
      "event_time": "2026-09-18T18:09:22.512Z",
      "site": "example-site",
      "endpoint_group": "search",
      "identity_id": "idt_01a0b5b4f9797382ae09590b2b49bd9c",
      "uri": "/search",
      "http_status": 200,
      "outcome": "success",
      "blame": "none",
      "rule": "success",
      "latency_ms": 412,
      "response_bytes": "48213"
    }
  ],
  "next_page_token": "",
  "summary": null
}
```

Every row carries what the node sent **and** what the server decided: `outcome`, the `rule` that
produced it, and `blame` (`none`, `identity`, `proxy` or `both`) — which is what stops a bad proxy
from getting a good identity banned. `node` and `token_id` are how you find the one node that
is misbehaving.

`probe: false` here is not a contradiction of the `probe: true` you saw on the lease in
[Step 8](#step-8--lease-an-identity): a request event's `probe` marks **half-open breaker probes
only**, while a lease's `probe` is also true for any lease of a `pending` identity.

The console's view of the same data has filters in the URL, so any view is a link you
can paste into an incident channel:
`/requests?site=example-site&outcomes=captcha,banned&range=6h`.

If you left `SPINNERET_CLICKHOUSE_URL` empty, this page is not available. **Risk Events** is the
PostgreSQL-backed fallback: non-success reports only, but always there.

### The identity detail page

Click an identity anywhere and you get its whole story on one page: the state timeline (the events
you just read), the payload versions, the per-endpoint-group scheduler state (score, samples,
consecutive failures, remaining cooldown, whether it is in the ready queue) and its recent risk
events. Alongside it are the manual operations — cooldown, ban, unban, quarantine, unquarantine,
expire, disable, enable, archive, restore, activate, reset statistics — and they are all audited.

![The identity detail page](../images/identity-detail.png)

Both pages in depth: [Observability and alerting](./12-observability.md) and
[Identities and accounts](./06-identities.md).

---

## Step 11 — The complete example node

You have now driven the loop by hand. The repository also ships a complete node — a FastAPI service
using the Python SDK — and a scriptable mock target site with an authenticating proxy, so you can
watch identities cool down and a breaker trip without pointing anything at a real target.

```bash
./scripts/example-quickstart.sh        # or: make example
```

It is idempotent, takes about fifteen seconds on a warm stack, and tells you what it did:

```text
[example-quickstart] stack ready (5.3s)
[example-quickstart] seeded: {"namespace": "default", "site": "example", "site_created": false, "groups_created": 0, "identities": {"created": 0, "updated": 0, "unchanged": 20}, "proxies": {"created": 0, "updated": 0, "unchanged": 2}, "policies": {"rotation": "unchanged", "signal": "unchanged"}, "config": "unchanged", "token_reused": true, "seconds": 0.205} (0.4s)
[example-quickstart] example-crawler running on http://localhost:18000 (8.5s)
[example-quickstart] GET /healthz -> {"ok":true,"config_watcher_running":true}
[example-quickstart] GET /crawl/search?q=quickstart -> {"ok":true,"status":200,"identity_id":"idt_01a0b0a4dc4774759bba124ee0e6be8f","proxy_id":"pxy_01a0b0a4dc4b7c5984d858b85e92f447","endpoint_group":"search","markers":[],"business_code":"","data":{"ok":true,"path":"/site/search","identity":"example-17","proxy":"expx1","items":[{"id":"/site/search#1","title":"item 1"},{"id":"/site/search#2","title":"item 2"},{"id":"/site/search#3","title":"item 3"}]}}
[example-quickstart] GET /crawl/item/42 -> {"ok":true,"status":200,"identity_id":"idt_01a0b0a4dc4774268ccf781b336894c3","proxy_id":"pxy_01a0b0a4dc4b7c5984d858b85e92f447","endpoint_group":"detail","markers":[],"business_code":"","data":{"ok":true,"path":"/site/item/42","identity":"example-06","proxy":"expx1","items":[{"id":"/site/item/42#1","title":"item 1"},{"id":"/site/item/42#2","title":"item 2"},{"id":"/site/item/42#3","title":"item 3"}]}}
[example-quickstart] GET /config -> {"ok":true,"group":"crawler","key":"example.json","version":3,"format":"json","from_snapshot":false,"content":{"search_page_size":10,"item_fields":["id","title"],"greeting":"hello from Spinneret"}}
[example-quickstart] chain verified (0.2s)
[example-quickstart] done in 14.5s: console http://localhost:8080 (site example), crawler http://localhost:18000
```

What it created, in namespace `default`, alongside what you built by hand:

| | |
| --- | --- |
| Site `example` | client `web`, endpoint groups `search` (prefix `/site/search`) and `detail` (template `/site/item/{id}`) |
| Identity type `example_web_cookie` | and 20 identities, `activation: immediate` |
| Two proxies | pointing at the mock target's authenticating forward proxy |
| Two published policies | `example-rotation` (`proxy.mode: bind_identity`, `reuse_interval: 1s`) and `example-signal` |
| Config item `crawler/example.json` | which the node watches by long polling |
| A node token | scopes `lease:acquire:example`, `report:write:example`, `config:read:crawler`, written to `deploy/compose/.env` as `EXAMPLE_TOKEN`; an existing valid one is reused |
| Service `example-crawler` | on <http://127.0.0.1:18000>, plus `mocktarget` on `19090` (site) and `19091` (proxy) |

Call it yourself:

```bash
curl 'http://127.0.0.1:18000/crawl/search?q=shoes'
curl  http://127.0.0.1:18000/crawl/item/42
curl  http://127.0.0.1:18000/config
```

Each crawl does the three steps you did by hand: `lease` an identity and a proxy for the URI it is
about to fetch, request the target with `httpx.AsyncClient(**lease.httpx_kwargs())` so the credential
and the proxy are merged into the client, and `report_response(...)` the facts it observed. In Python
the whole loop is:

```python
import httpx, spinneret

async with spinneret.AsyncClient() as client:          # reads SPINNERET_URL / SPINNERET_TOKEN
    async with client.lease(site="example-site", client="web", uri="/search?q=shoes") as lease:
        async with httpx.AsyncClient(**lease.httpx_kwargs()) as http:
            response = await http.get("https://target.invalid/search", params={"q": "shoes"})
        lease.report_response(response)                # facts only; the server decides the outcome
```

### Make the target misbehave

The mock target is scriptable per path prefix. Turn `/site/search` into a rate limiter, send some
traffic, and watch the console:

```bash
curl -X PUT 127.0.0.1:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
```

```json
[{"prefix":"/site/search","mode":"rate_limit","status":429,"probability":1,"item_count":3}]
```

```bash
for i in $(seq 1 60); do curl -s -o /dev/null 'http://127.0.0.1:18000/crawl/search?q=x'; done
```

The first requests still answer `200`, with the observed failure inside the body — the node reports
what it saw and keeps going:

```json
{"ok":false,"status":429,"identity_id":"idt_01a0b0a4dc4774078234a278cde1ef49","proxy_id":"pxy_…","endpoint_group":"search","markers":[],"business_code":"","data":{"ok":false,"error":"rate_limited"}}
```

Identities pick up 60-second cooldowns on `search` and their scores fall. Then, once the breaker's
window has at least 50 requests, it opens, and every `Acquire` for that endpoint group fails until
it closes. Call the node once more with `-i` to see it:

```bash
curl -i 'http://127.0.0.1:18000/crawl/search?q=x'
```

```text
HTTP/1.1 503 Service Unavailable
retry-after: 117
```

```json
{"ok":false,"error":"unavailable","reason":"circuit_open"}
```

**Breakers** in the console shows why, in the breaker's own words — abridged here to the decision
(the full status also carries `namespace`, `site_id`, `endpoint_group_id`, `last_opened_at`,
`last_closed_at`, `site_paused` and `policy_id`):

```json
{
  "site": "example",
  "client": "web",
  "endpoint_group": "search",
  "state": "open",
  "open_until": "2026-09-18T18:14:05.109Z",
  "consecutive_opens": 1,
  "manual": false,
  "reason": "risk_ratio 0.98 >= 0.40; success_ratio 0.02 <= 0.20",
  "window": {"total": 58, "success": 1, "risk": 57, "captcha_identities": 0,
             "risk_ratio": 0.9828, "success_ratio": 0.0172},
  "probe": {"samples": 0, "successes": 0, "issued": 0},
  "policy_name": "default-breaker"
}
```

Two minutes later (`open_duration`) it goes half-open, issues a handful of probe leases, and closes
again if enough of them succeed — 5 samples at an 80 % success ratio, in the default policy. Put the
target back and it recovers by itself:

```bash
curl -X DELETE 127.0.0.1:19090/_admin/rules
```

Nothing was deployed, restarted or edited to make any of that happen. `./scripts/example-quickstart.sh --reset`
deletes the example site, its token, proxies, policies and config item first, so you can start over.

The node endpoint by endpoint, its environment variables and its production notes are in
[examples/fastapi-crawler/README.md](../../examples/fastapi-crawler/README.md); the SDKs are in
[SDKs and examples](./14-sdks.md).

---

## Tearing it down

```bash
cd deploy/compose
docker compose stop                 # stop the containers, keep everything
docker compose up -d --wait         # start again
docker compose down                 # remove the containers, keep the volumes
docker compose down -v              # also delete pgdata, valkeydata, chdata. Irreversible.
```

`down -v` deletes every identity, proxy, policy, config item, secret, user and token in the
deployment. The key-encryption key survives in `deploy/compose/secrets/kek.key`, so a dump taken with
it can still be restored into a fresh stack — which is exactly why you backed it up in
[Step 3](#step-3--sign-in-and-back-up-the-key).

On an install made by the installer, use `./spnrctl` for all of that — it carries the project name,
the override files, the working directory and the env file that all have to be right every time:

```bash
./spnrctl stop
./spnrctl logs -f spinneret
./spnrctl down -v
```

Or `bash install.sh --manage` → menu entry 4, which offers three levels: stop and keep every byte;
stop and delete the data volumes; that plus remove the install directory including `.env` and
`kek.key` — and it offers to print the key first, because without it no backup of this deployment can
ever be opened again. Anything that deletes data requires typing `delete`, not a y/n. Docker images
are left alone either way: nothing here prunes a shared host.

---

## If something went wrong

| What you saw | Where to look |
| --- | --- |
| `Compose is 2.20.x; this stack needs 2.24 or newer` | Update Docker. The override files use merge tags that do not exist before 2.24 |
| `required variable PG_PASSWORD is missing a value` | There is no `.env` — run `./scripts/compose-init.sh`. Compose reads it from the **Compose file's** directory, not from yours, so a full `-f deploy/compose/docker-compose.yml` works from anywhere |
| `bind: address already in use` | Something else has port 8080. `lsof -iTCP:8080 -sTCP:LISTEN` names it; change `SPINNERET_PORT` in `.env` |
| `vault: read kek file … permission denied` | `kek.key` must be `-rw-r--r--`. Do not "harden" it to `0600` — see [Step 3](#step-3--sign-in-and-back-up-the-key) |
| `vault: kek "k1" must be 32 bytes, got N` | Truncated or corrupt key file |
| `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` | Wrong key for this database — the restore-with-the-wrong-key signature |
| `/readyz` never turns `ok` | The body names the dependency. See the table in [Step 2](#step-2--check-that-it-is-healthy) |
| `503 {"status":"draining"}` | Expected for up to five seconds per replica during a restart. Longer means whatever is in front is not honouring `/readyz` |
| ClickHouse keeps restarting | Usually the OOM killer on a small host. Raise the RAM, or empty `SPINNERET_CLICKHOUSE_URL` to run without analytics |
| `429 no_identity_available` on a real deployment | Not enough usable identities, or a rotation policy too narrow. Start at [Troubleshooting](./18-troubleshooting.md) |
| `429 no_proxy_available` | Most often `SPINNERET_PROXY_CHECK_URL`: it defaults to a URL on the public internet, and a host that cannot reach it marks every proxy dead |
| `503 circuit_open` | The endpoint group's breaker is open. It closes when the target recovers; **Breakers** says why it opened |
| `403 scope_missing` | The token lacks a scope for that call or that site. See [Step 7](#step-7--create-an-api-token-for-the-node) |

[Troubleshooting](./18-troubleshooting.md) is organized by symptom and carries the complete
error-reason table, plus how to read the logs and how to collect a diagnostic bundle.

---

## Next

Three pages, in this order:

- [Concepts](./04-concepts.md) — the mental model behind everything you just did: tenants,
  namespaces, sites, endpoint groups, identities, leases, reports, signals, actions, policies,
  breakers. Read it once and you can predict what a request will do before you send it.
- [Console overview](./05-console-overview.md) — the whole navigation map, what each page is for,
  which document covers it, and what a read-only or site-scoped user sees.
- [Node API reference](./13-node-api.md) — every RPC a node calls, with request and response fields,
  the error-reason table, the retry rules and the recommended client loop.

And when you are ready to run this for real:

- [Installation and deployment](./02-installation.md) — reverse proxies and TLS, scaling out,
  several stacks on one host, upgrades, uninstalling, deploying without Docker.
- [Security](./19-security.md) — the hardening checklist to work through before this is reachable by
  anyone else.
- [Operations runbook](./16-operations.md) — backups and restore drills, key rotation, hot-state
  rebuilds, retention, incident playbooks.
