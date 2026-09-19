# Installation and deployment

**The deployment reference: which install path to take, what the host needs, every service in the
Compose stack and what breaks without it, the files a deployment owns, ports, reverse proxies and
TLS, scaling out, upgrading, uninstalling, and running with no Docker at all.**

[中文](../zh/02-installation.md)

---

## Contents

- [Which path](#which-path)
- [Requirements](#requirements)
- [The guided installer](#the-guided-installer)
  - [What it asks](#what-it-asks)
  - [Flags and environment overrides](#flags-and-environment-overrides)
  - [What it writes to disk](#what-it-writes-to-disk)
  - [spnrctl, the control script](#spnrctl-the-control-script)
  - [Running it again: the management menu](#running-it-again-the-management-menu)
- [Manual Docker Compose](#manual-docker-compose)
- [Published image or build from source](#published-image-or-build-from-source)
- [The Compose stack, service by service](#the-compose-stack-service-by-service)
  - [postgres](#postgres)
  - [valkey](#valkey)
  - [clickhouse](#clickhouse)
  - [migrate](#migrate)
  - [spinneret](#spinneret)
  - [lb](#lb)
  - [The profile-gated services](#the-profile-gated-services)
  - [Enabling a profile](#enabling-a-profile)
- [The files a deployment owns](#the-files-a-deployment-owns)
- [The key-encryption key](#the-key-encryption-key)
- [Compose override files](#compose-override-files)
- [Ports and networking](#ports-and-networking)
- [Behind a reverse proxy, and TLS](#behind-a-reverse-proxy-and-tls)
- [Scaling out](#scaling-out)
- [Several stacks on one host](#several-stacks-on-one-host)
- [Multi-host and orchestrators](#multi-host-and-orchestrators)
- [Upgrading](#upgrading)
- [Uninstalling](#uninstalling)
- [Running without Docker](#running-without-docker)
- [When the install goes wrong](#when-the-install-goes-wrong)
- [Next](#next)

---

## Which path

Spinneret ships as one stateless binary (`spinneret-server`) plus an administration CLI (`spnr`), both
in a single distroless image. Three ways to get there, in descending order of how much the project does
for you.

| Path | What you get | What it costs |
| --- | --- | --- |
| **The guided installer** — `install/install.sh` | A complete deployment on one host: Docker checked or installed, the repository cloned, passwords and the vault key generated, the Compose overrides written for *this* machine, migrations applied, the stack started and waited for, the first administrator created. Plus a control script and a management menu for everything afterwards | You accept its layout: one directory, Docker named volumes, Caddy as the in-network load balancer. A script you should read before running |
| **The Compose files in the repository** — `deploy/compose/docker-compose.yml` | The same stack, assembled by hand, step by step, nothing hidden. This is what the installer drives | Four things have to be right on every command and none of them errors when it is wrong: the project name, the override files in order, the working directory, and where `.env` is read from. The installer's `spnrctl` exists precisely because that is easy to get wrong |
| **No Docker** | The binary on a host, PostgreSQL and Valkey wherever you like, systemd or your own supervisor | Everything above becomes yours: installing and tuning three databases, building the console toolchain (Node and pnpm) alongside Go, delivering the key-encryption key, the reverse proxy, health checks, backups. There is no script and there will not be one. It works — see [Running without Docker](#running-without-docker) — but budget a day, not an hour |

Be honest with yourself about the third one. Nothing in the binary needs a container: it opens one
listener, talks to PostgreSQL and Redis, reads a key file and writes nothing to local disk. What Docker
is doing for you here is the *other* four processes and their tuning — the Valkey persistence settings,
the ClickHouse cache caps and the PostgreSQL parameters in this repository are all the result of load
testing, and on a hand-built host you inherit the job of reproducing them.

If you are reading this before your first install, go to [Quick start](./01-quickstart.md) instead and
come back here when you need a detail.

---

## Requirements

| Component | Version | Notes |
| --- | --- | --- |
| Docker Engine | Any version that ships the Compose v2 plugin at 2.24 or newer — in practice Engine 24 or newer | The real requirement is Compose: `docker compose version` must report **2.24 or newer**, the first release with the `!reset` and `!override` merge tags that the override files this project writes both use. Nothing checks the Engine version; the installer gates on Compose alone. The old standalone `docker-compose` (v1) cannot run this stack and is detected and rejected |
| Architecture | `x86_64` or `arm64` | The published image is built for those two. Anything else builds from source, which works |
| PostgreSQL | 17 | Source of truth. The stack runs `postgres:17-alpine` |
| Redis or Valkey | Valkey 8 / Redis 7 or newer | Hot state, leases, report streams, sessions. Must be persistent and must **never** evict keys |
| ClickHouse | 25.8, optional | Raw request events behind the request explorer. Leave `SPINNERET_CLICKHOUSE_URL` empty to run without it |

Host tools, if you use the installer: `bash`, `git`, `curl`, `awk`, `base64`, the coreutils it actually
calls (`install`, `cmp`, `mktemp`, `find`, `head`, `sort`), and either a readable `/dev/urandom` or
`openssl`. Alpine ships neither bash nor git by default: `apk add bash git curl coreutils`.

| Size | vCPU | RAM | Disk |
| --- | --- | --- | --- |
| Evaluation, ClickHouse disabled | 2 | 2 GiB | 10 GiB |
| The stack as shipped | 2 | **4 GiB** | 20 GiB |
| Comfortable | 4 | 8 GiB | 40 GiB SSD |
| One server instance at the design load (about 5,000 acquires/s) | 4 per instance | 8 GiB per instance | fast disks for PostgreSQL and Valkey |

Those numbers are not a guess; every term comes from the container configuration in
`deploy/compose/docker-compose.yml` and `deploy/compose/config/`:

| Term | Where it comes from | Size |
| --- | --- | --- |
| PostgreSQL shared buffers | `-c shared_buffers=512MB` on the `postgres` command line | 512 MiB, allocated at start |
| PostgreSQL connection slots | `-c max_connections=300`; each server replica opens up to `SPINNERET_DATABASE_MAX_CONNS` (32) | 2 replicas × 32 = 64 of 300 |
| ClickHouse resident set | The image idles near 1.2 GiB whatever its caches are set to (jemalloc arenas); `config/clickhouse-limits.xml` caps `max_server_memory_usage` at 2.5 GiB and the mark cache at 64 MiB, down from ClickHouse's own 5 GiB default | 1.2 GiB idle, 2.5 GiB ceiling |
| Valkey working set | `--maxmemory-policy noeviction` with **no** `maxmemory`: it grows with the hot state, and the dominant term is report dedup markers plus pinned ended-lease hashes over `SPINNERET_REPORT_DEDUP_TTL` (default `1h`) | grows with traffic; leave headroom for the AOF-rewrite fork |
| Server replicas | `deploy.replicas: ${SPINNERET_REPLICAS:-2}` | roughly 150–500 MiB each |
| Load balancer | `caddy:2-alpine` | tens of MiB |

Add those up and 4 GiB is a floor rather than a recommendation. The installer knows it: under about
7.6 GiB it writes `mem_limit` ceilings into `compose.host.yml` — ClickHouse two fifths of RAM,
PostgreSQL a quarter, Valkey a fifth, the server an eighth — so that an unlucky moment takes the OOM
killer to whatever was actually growing rather than to PostgreSQL.

**What being short of each one costs:**

| Short of | What happens |
| --- | --- |
| RAM | The OOM killer picks a container. ClickHouse is the usual victim, and if `SPINNERET_CLICKHOUSE_URL` is set it is a **hard startup dependency** — the server fails to start with `connect clickhouse: …` rather than degrading. Valkey being killed loses the whole hot state, which needs `spnr rebuild`. `docker inspect --format '{{.State.OOMKilled}}' <container>` confirms which |
| RAM, less severely | ClickHouse below its idle RSS does not shrink: every `INSERT` fails with `MEMORY_LIMIT_EXCEEDED` and the report worker stalls. That is why `max_server_memory_usage` is set *above* the idle figure, not below it |
| CPU | One core still starts, slowly. Two is the real floor: the installer warns below that, and clamps the replica default to one |
| Disk | `chdata` grows fastest and expires on `SPINNERET_CLICKHOUSE_TTL_DAYS` (default 90). `docker system df -v` shows where the space went |
| PostgreSQL connections | Past `replicas × SPINNERET_DATABASE_MAX_CONNS + headroom > 300`, `/readyz` reports `postgres: unreachable` under load. Raise `max_connections` on the `postgres` command line, or lower the pool |
| File descriptors | ClickHouse asks for 262,144 soft and hard (`ulimits.nofile`). If the host's hard limit is lower the container refuses to start and the log says `nofile`. Check `ulimit -Hn` |
| Compose version | The override files fail in ways that read like a bug in this project. Update Docker |

Every variable in the table, with its range and validation: [Configuration](./03-configuration.md).

---

## The guided installer

`install/install.sh` (English) and `install/install.zh.sh` (Chinese) are the same script with different
messages — the same options, the same environment variables, the same menu, the same safety boundaries.
Docker only. They install, and then they manage what they installed.

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh -o install.sh
less install.sh          # 2,500 lines, every decision commented; read it before you run it
bash install.sh
```

That order is recommended and it is not ceremony: anything you pipe into a shell runs as you, and on the
Docker step the script offers to run something as root.

Piping works, questions included — answers are read from `/dev/tty` rather than from stdin, because with
`curl | bash` stdin *is* the script:

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh | bash
```

With no terminal at all (a CI runner, a `docker exec` without `-t`) it says so and stops, rather than
quietly taking defaults for a question like "which address do I publish on". For a genuinely unattended
run, say so: `| bash -s -- --yes`.

**What it will not do.** It writes only inside the directory you choose, plus Docker's own named volumes.
The single exception is the path leading to that directory: missing parents are created — named first,
with `sudo`, and with the command printed. It uses `sudo` for exactly three things, each printed before it
runs: installing Docker if you say yes, starting the Docker service, and creating the install directory
when that directory needs root. It will **not** add you to the `docker` group — on most machines that is
the same as handing out root, so it prints the command and lets you decide. It never edits a file outside
the install directory, never adds a cron job, never opens a firewall port, never installs anything without
asking, never overwrites an existing `.env` (its passwords are what the database volumes were built with),
and never overwrites an existing `kek.key`.

It refuses to install into `/`, `/usr`, `/etc`, `/var`, `/bin`, `/sbin`, `/lib`, `/boot`, `/home`, `/root`
or `/opt` itself; into a git checkout of some other project — decided by whether
`deploy/compose/docker-compose.yml` and `deploy/compose/.env.example` are actually there, not by what a
remote URL says; or into a directory that already has something in it that is not a Spinneret install.

`install/README.md` is the full reference, in both languages.

### What it asks

Seven questions, each with a default you can take with enter.

| | Question | Default | Notes |
| --- | --- | --- | --- |
| 1 | Install directory | `/opt/spinneret` as root, `~/spinneret` otherwise | The checkout, `.env`, the vault key and the control script live here. The databases do not |
| 2 | Keep the console on `127.0.0.1`? | yes | Saying no publishes on `0.0.0.0` and warns you: the console is an admin surface and `/metrics` is on the same listener with no authentication |
| 3 | Which port | `8080` | Validated as a port |
| 4 | Administrator username | `admin` | Validated against the server's own rule, `^[a-z0-9][a-z0-9._-]{2,63}$`, so a rejected name costs a keystroke rather than a failed bootstrap |
| 5 | How many server replicas | one per two cores, clamped to 1–4, forced to 1 under 4 GiB | One replica means an upgrade has a short window with nothing serving; two means none, at roughly 150–500 MiB more RAM |
| 6 | The observability profile | off | On, it adds Prometheus scraping the server's `/metrics`, published on `127.0.0.1` only — always, whatever the console is bound to, because it has no authentication |
| 7 | Published image or build from source | published, if it can be fetched | It runs `docker manifest inspect` on the tag **before** asking, so the question already knows which way it is about to go |

If there is already a checkout in the directory it asks one more: whether to update it to the latest
`main` (`fetch` plus `merge --ff-only`, never `reset --hard`).

Then it clones the repository shallow (`--depth 1 --branch main`, the whole repository because the
source-build path uses the repository root as the build context), generates the two database passwords,
the administrator password and the key-encryption key **on the machine**, writes `.env` from the shipped
`.env.example` with `awk` so every comment in the example survives into the file you will read, writes the
overrides for this host, writes `spnrctl`, pulls or builds, applies migrations as an explicit step, brings
the stack up with `--wait`, polls `http://127.0.0.1:<port>/readyz` for up to 300 s until all four
dependencies report `ok`, creates the first administrator, and prints the console URL with the credentials.

### Flags and environment overrides

```
--yes, -y   Take the default answer to every question.
--check     Detect the system and print what would happen, then stop.
--manage    Go straight to the menu for an install that already exists.
--help, -h  The option list.
```

`--check` is the one to run first on an unfamiliar host: it reports the distribution, architecture, cores
and RAM; whether Docker is present, running and new enough; whether `git` and `curl` are there, with the
package-manager command for each that is missing; and whether the published image can actually be fetched
from this network. It changes nothing, writes nothing and asks nothing.

Every answer can be preset from the environment, which is what makes `--yes` a complete unattended install
rather than just "all defaults".

| Variable | Default | What it presets |
| --- | --- | --- |
| `SPINNERET_PROJECT` | `spinneret` | The Compose project name, also how an existing install is found. Letters, digits, dash and underscore only |
| `SPINNERET_INSTALL_DIR` | `/opt/spinneret` as root, `~/spinneret` otherwise | Where to install, or where to find the install under `--manage` |
| `SPINNERET_BIND_HOST` | `127.0.0.1` | `127.0.0.1` or `0.0.0.0`. Written to `.env`, read by `compose.host.yml` |
| `SPINNERET_PORT` | `8080` | The published port |
| `SPINNERET_ADMIN_USERNAME` | `admin` | The first administrator |
| `SPINNERET_REPLICAS` | derived from CPU and RAM | Server replicas |
| `SPINNERET_ENABLE_OBSERVABILITY` | `0` | `1` adds the Prometheus profile |
| `SPINNERET_USE_PUBLISHED` | `1` | `1` pulls the published image, `0` builds from the checkout |
| `SPINNERET_IMAGE` | `tikhubio/spinneret` | The image repository — `ghcr.io/tikhub/spinneret` for the same build from GitHub Packages (which needs a login), a private mirror, or a fork, without editing the script |
| `SPINNERET_IMAGE_TAG` | `latest` | The image tag. Pin an exact one for a production install |
| `NO_COLOR` | unset | Set to anything to turn colour off |

```bash
SPINNERET_INSTALL_DIR=/srv/spinneret SPINNERET_PORT=9000 \
SPINNERET_REPLICAS=2 SPINNERET_IMAGE_TAG=v0.1.0 \
  bash install.sh --yes
```

The administrator password and the two database passwords are always generated on the machine and never
taken from the environment.

### What it writes to disk

```text
<INSTALL_DIR>/                            # /opt/spinneret or ~/spinneret
├── .git/                                 # shallow clone of TikHub/Spinneret
├── deploy/compose/
│   ├── docker-compose.yml                # from the repository, never edited
│   ├── compose.image.yml                 # written here — the published-image override
│   ├── compose.build.yml                 # written here — only under a non-default project name
│   ├── compose.host.yml                  # written here — bind address, memory ceilings
│   ├── .env                              # written here, mode 0600
│   ├── config/{Caddyfile,clickhouse-*.xml,prometheus.yml}
│   └── secrets/
│       └── kek.key                       # written here, mode 0644 in a 0700 directory
├── backups/<UTC timestamp>/              # written by Manage → 10
│   ├── postgres.dump
│   ├── kek.key
│   └── env
└── spnrctl                               # written here, mode 0755
```

The databases themselves live in Docker's named volumes — `spinneret_pgdata`, `spinneret_valkeydata`,
`spinneret_chdata` — not in this directory.

### spnrctl, the control script

`spnrctl` is the single entry point. It carries the four things that have to be right on every command and
whose absence produces confusing behaviour rather than an error: the Compose project name, the override
files in the right order, the working directory that makes `./config`, `./secrets` and the `../..` build
context resolve, and `COMPOSE_ENV_FILES`. Everything after the name is passed straight to `docker compose`.

```bash
./spnrctl ps                        # what is running
./spnrctl logs -f spinneret         # follow the server log
./spnrctl restart lb                # restart one service
./spnrctl up -d --wait              # start, wait for healthy
./spnrctl down                      # stop, keep the data
./spnrctl down -v                   # stop and delete the volumes. Irreversible.
```

The administration CLI lives in the image as `/usr/local/bin/spnr` and needs only PostgreSQL, so run it
through the one-shot `migrate` service rather than through a replica that may not be healthy:

```bash
./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status
./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate \
    token create --name node-1 \
    --scope lease:acquire --scope report:write --scope config:read
```

The images are distroless, so `./spnrctl exec spinneret sh` does not work — there is no shell in them.

### Running it again: the management menu

Run the script again and it works out which job it is doing. An existing install is found by asking Docker
which directory the Compose project was started from, so it does not matter where you put it, and it is
looked for on **every** run.

```
==> An install is already here
    ✓ /opt/spinneret
    ✓ Running v0.1.0
    ✓ Image tikhubio/spinneret:latest

      1  Status — versions, containers, schema, disk
      2  Move to another image tag (re-pull, migrate, restart)
      3  Manage — accounts, tokens, backups, health, disk
      4  Stop or remove this install
      q  Quit
```

`--manage` goes straight there. Every setting is re-read from the install's own files first, so an install
created with one replica and no observability profile is never upgraded as though it had two and the profile
on. Entry 2 compares the running version against the latest release, and a pre-release suffix counts as
older, so `v1.2.0-rc1` is behind `v1.2.0`.

`--yes` over a deployment that already exists prints where it is and changes nothing. "Take the default
answer to every question" is right for a fresh host and wrong for a live one, where it would mean switching
a source-built install to the published `latest`, applying that image's migrations to the production
database, turning the observability profile off, and rewriting `compose.host.yml` over whatever you had
edited into it.

**Manage** (entry 3) covers the things people otherwise ask how to do:

| | | |
| --- | --- | --- |
| 1 | Change an administrator password | Signs in as the account and changes its own password, exactly as the console does. Every other session of that account ends. Offers to update `SPINNERET_ADMIN_PASSWORD` in `.env` |
| 2 | Add an administrator | Creates a user with the tenant-wide `admin` role. There is no rename: accounts are created, and passwords are reset |
| 3 | List accounts | Username, id, and whether the account is a platform administrator or disabled |
| 4 | Create an API token | `spnr token create`. Only the plaintext is printed, once |
| 5 | Rebuild the hot state | `spnr rebuild`. Warns that a fleet-wide rebuild deletes the epoch key first, so every replica reports not-ready until it finishes; naming one site with `--site` avoids that |
| 6 | Show the configuration | `spnr config check` — the whole `SPINNERET_*` environment, validated, with every credential redacted |
| 7 | Health check | `/healthz` and `/readyz`, container status, and whether ClickHouse, Valkey or PostgreSQL has been OOM-killed |
| 8 | Logs | `spnrctl logs` |
| 9 | Restart services | Offers to roll the replicas one at a time |
| 10 | Back up now | `pg_dump -Fc`, plus `kek.key` and `env`, into `<install-dir>/backups/<UTC timestamp>/`. The dump is written under `umask 077` and both copies are installed at `0600`; the directory itself inherits the host's umask, so tighten it yourself if the host is shared |
| 11 | Restore a backup | Requires typing `restore`, compares the backup's key against the live one, stops the replicas, `pg_restore --clean --if-exists`, rebuilds the hot state, brings them back |
| 12 | Free disk space | Images of **this** Compose project that no container references, and optionally the host's build cache — which is shared with every other build on the machine, and the prompt says so |

Three of those — change a password, add a user, list users — do not exist in the `spnr` CLI at all; they
are console RPCs, reached over the same endpoint the console itself uses, on loopback. Passwords are read
without echo and travel to `curl` on stdin, never as a command-line argument, because `argv` is readable by
every process on the host. Everything else delegates to `spnrctl` or to `spnr` inside the image, so a menu
entry cannot drift away from the command it stands for.

Backups, restores and key rotation in full: [Operations](./16-operations.md#backups).

---

## Manual Docker Compose

Everything is in `deploy/compose/docker-compose.yml`, Compose project name `spinneret`.

```bash
git clone https://github.com/TikHub/Spinneret.git
cd Spinneret
./scripts/compose-init.sh
```

`compose-init.sh` creates, only if missing, `deploy/compose/.env` from `.env.example` with random
`PG_PASSWORD`, `CLICKHOUSE_PASSWORD` and `SPINNERET_ADMIN_PASSWORD`, and
`deploy/compose/secrets/kek.key` holding one fresh 32-byte key as `k1:<base64>` at mode `0644`. It is safe
to re-run: existing files are kept. Both are git-ignored and excluded from the Docker build context. It
needs `openssl` on `PATH`.

```bash
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

`--wait` returns when every container with a healthcheck reports healthy. `init-admin` creates the platform
administrator (`SPINNERET_ADMIN_USERNAME`, default `admin`), the tenant `default`, the namespace `default`
and its default policies; it is idempotent and prints `already initialized` on a second run.

Verify:

```bash
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate migrate status
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate config check
```

**A note that saves an hour.** Compose resolves `.env` and every relative path in the stack (`./config`,
`./secrets`, the `../..` build context) against **the Compose file's directory**, not against your current
one. That is exactly why the `-f deploy/compose/docker-compose.yml` form above works from the repository
root: `.env` is still found next to the file, not next to you. `cd deploy/compose` and plain
`docker compose` works the same way, and the `spnrctl` the installer writes carries the whole invocation
for you. `required variable PG_PASSWORD is missing a value` therefore does not mean you are in the wrong
directory — it means `deploy/compose/.env` has not been created yet (run `./scripts/compose-init.sh`), or
an explicit `--env-file` / `COMPOSE_ENV_FILES` is pointing somewhere else.

`make up` and `make down` are the same two commands with the `-f` already in place.

---

## Published image or build from source

The published multi-arch image is `tikhubio/spinneret` on Docker Hub, which is the copy that pulls
without credentials. The same build is pushed to `ghcr.io/tikhub/spinneret` on GitHub Packages at the
same digest; that one needs a login, so point `SPINNERET_IMAGE` at it only if you have one. Both are
built for `linux/amd64` and `linux/arm64` and tagged on every version tag (`v` followed by a digit) with `vX.Y.Z`, `X.Y.Z`, `X.Y` and `latest` —
`latest` only for tags with no pre-release suffix, so `v1.2.0-rc1` never becomes `latest`. The tag is stamped into the binary, so
`spnr version` and the console report exactly which build is running. Pulling takes about a minute.
Building takes 5–15 minutes and about 2 GB of build cache on a first build, and needs to reach the Go and
Node package registries.

The installer writes `compose.image.yml` for the published path:

```yaml
services:
  migrate:
    image: ${SPINNERET_IMAGE:-tikhubio/spinneret}:${SPINNERET_IMAGE_TAG:-latest}
    build: !reset null
  spinneret:
    image: ${SPINNERET_IMAGE:-tikhubio/spinneret}:${SPINNERET_IMAGE_TAG:-latest}
    build: !reset null
  init-admin:
    image: ${SPINNERET_IMAGE:-tikhubio/spinneret}:${SPINNERET_IMAGE_TAG:-latest}
    build: !reset null
```

All three services that run the server binary — `migrate`, `spinneret`, `init-admin` — share one image, so
overriding only one leaves the other two trying to build. `build: !reset null` drops the build section
entirely, which means a stray `docker compose build` cannot quietly rebuild over the image that was pulled,
and the repository root does not have to exist for Compose to resolve a context that will not be used.
Delete the file to go back to building from source.

By hand, the same thing is `SPINNERET_IMAGE` / `SPINNERET_IMAGE_TAG` in `.env` plus that override file, or
simply `docker compose pull` with the image pinned. **Pin an exact tag for a production install** rather
than following a moving one: then you can say which build is running, and put it back.

---

## The Compose stack, service by service

Eleven services. Six are the stack; five are gated behind a profile and do nothing unless you ask for them.

| Service | Image | Profile | Publishes | Volumes |
| --- | --- | --- | --- | --- |
| `postgres` | `postgres:17-alpine` | — | nothing | `pgdata` |
| `valkey` | `valkey/valkey:8-alpine` | — | nothing | `valkeydata` |
| `clickhouse` | `clickhouse/clickhouse-server:25.8-alpine` | — | nothing | `chdata`, two read-only config mounts |
| `migrate` | `spinneret:local` or the published image | — | nothing | the `kek` secret |
| `spinneret` | the same image | — | nothing | the `kek` secret |
| `lb` | `caddy:2-alpine` | — | `${SPINNERET_PORT:-8080}` → 8080 | `config/Caddyfile` read-only |
| `init-admin` | the same image | `init` | nothing | the `kek` secret |
| `prometheus` | `prom/prometheus:v2.54.1` | `observability` | `${PROMETHEUS_PORT:-9090}` → 9090 | `config/prometheus.yml` read-only |
| `mocktarget` | `spinneret-mocktarget:local` | `test`, `loadtest`, `example` | `${MOCK_TARGET_PORT:-19090}` → 9090, `${MOCK_PROXY_PORT:-19091}` → 9091 | none |
| `example-crawler` | `spinneret-example-crawler:local` | `example` | `${EXAMPLE_PORT:-18000}` → 8000 | none |
| `k6` | `grafana/k6:latest` | `loadtest` | nothing | `test/load` read-only |

The shared environment block (`x-spinneret-env`) is applied to `migrate`, `spinneret` and `init-admin`
alike, which is why the CLI inside `migrate` sees exactly the configuration the server sees.

### postgres

The source of truth: the catalog, sites, identities, proxies, policies, config items, secret versions,
users, tokens and the audit log. Started with four non-default parameters —
`max_connections=300`, `shared_buffers=512MB`, `effective_cache_size=1GB`, `wal_compression=on` — and its
data in the named volume `pgdata`. Healthcheck: `pg_isready -U spinneret -d spinneret` every 5 s, 30
retries, which is what `migrate` waits for.

Without it nothing starts: `migrate` has `depends_on: postgres: service_healthy`, and every other service
is downstream of `migrate`. It is also the one component whose loss is not recoverable from anywhere else
in the deployment — [back it up](./16-operations.md#backups).

### valkey

The hot state: lease hashes, availability sets, cooldown and ban markers, breaker state, report streams,
console sessions, the peer and worker registries. `valkeydata` holds its AOF.

Five settings that are not defaults, each of them load-tested:

| Setting | Value | Why |
| --- | --- | --- |
| `--appendonly yes --appendfsync everysec` | on | Durability is the AOF. Not traded away |
| `--save ""` | RDB off | The RDB save points forked a second time for the same data — at 4,000 acquire→report cycles/s the default fired a background save every minute *on top of* an AOF rewrite every 40 s |
| `--auto-aof-rewrite-percentage 300 --auto-aof-rewrite-min-size 1gb` | 300 % / 1 GiB | The hot state is mostly short-lived keys, so the AOF grows far faster than the dataset it describes; with the defaults a rewrite fired every ~52 s under load, each one forking a ~1 GiB process |
| `--io-threads ${VALKEY_IO_THREADS:-4}` | 4 | Little throughput, a great deal of tail latency. Set `VALKEY_IO_THREADS=1` for the single-threaded behaviour |
| `--maxmemory-policy noeviction` | noeviction | **Load-bearing.** The hot state is derived data, but silently *losing* keys changes scheduling decisions rather than failing loudly. Never enable eviction |

`--maxclients 20000` covers the long-poll and streaming connection counts. Healthcheck: `valkey-cli ping`.

Without it the server does not become ready: `/readyz` reports `redis: unreachable`. If the volume is lost
or flushed, `/readyz` reports `hotstate: epoch missing (rebuild pending)` and `spnr rebuild` regenerates
everything from PostgreSQL. The measurements and the sizing method are in
[Performance](./17-performance.md#valkey-settings).

### clickhouse

Raw request events, behind the request explorer and the analytics queries. Database `spinneret`, user
`spinneret`, volume `chdata`, two read-only config mounts — `config/clickhouse-listen.xml` (listen on IPv4
only, because containers usually lack IPv6) and `config/clickhouse-limits.xml` (the cache caps). Schema and
TTL are created by the server at startup from `SPINNERET_CLICKHOUSE_TTL_DAYS`. `ulimits.nofile` is raised
to 262,144 soft and hard. Healthcheck: `wget -qO- http://127.0.0.1:8123/ping`.

**Optional, but not optional halfway.** If `SPINNERET_CLICKHOUSE_URL` is set and ClickHouse cannot be
reached, the server fails to start rather than degrading. To run without it, empty that variable — the
request explorer and raw events go away, and about 1.2 GiB of RAM comes back. Everything else works:
identities, leases, policies, breakers, the per-minute and per-hour statistics all live in PostgreSQL and
Redis.

### migrate

A one-shot container that runs `/usr/local/bin/spnr migrate up` and exits. `restart: "no"`, healthcheck
disabled, `depends_on: postgres: service_healthy`. Migrations are embedded in the binary and serialized by
a PostgreSQL advisory lock, so several instances applying them at once is safe.

`spinneret` waits on `service_completed_successfully`, which is what guarantees no replica ever serves
against a schema it does not understand. It is also the container to run any `spnr` command in: it needs
only PostgreSQL, and it is never the one serving traffic.

Without it the server does not start at all. Note that Compose can consider a one-shot already satisfied
when its container spec has not changed, which is why the installer runs `run --rm migrate` as an explicit
step rather than relying on the dependency chain.

### spinneret

The server. `SPINNERET_REPLICAS` replicas (default 2) of the same image, no published ports — the load
balancer reaches them by the service name on the Compose network. The `kek` secret is mounted at
`/run/secrets/kek`. The image's own `HEALTHCHECK` runs
`spnr healthcheck --url http://127.0.0.1:8080/readyz` every 10 s with a 20 s start period and 3 retries,
which is how `--wait` and `lb`'s `depends_on` know a replica is usable.

`stop_grace_period: 40s` is deliberate and larger than the server's own
`SPINNERET_SHUTDOWN_TIMEOUT` (default 30 s): a replica that is asked to stop reports `draining` first,
then finishes in-flight requests, then stops its background loops tier by tier so writers flush what they
produced. Docker must not `SIGKILL` it in the middle of that.

`depends_on` is `migrate: service_completed_successfully`, `valkey: service_healthy`,
`clickhouse: service_healthy`.

### lb

Caddy, the in-network load balancer, and the only service that publishes a host port. It is started with
`--watch`, so editing `config/Caddyfile` reloads it in place without a restart. Its own healthcheck probes
`/healthz` through itself.

The Caddyfile is worth reading before you put your own proxy in front of it; every setting in it has a
comment saying which measurement produced it. The parts that matter to anyone replacing it:

| Setting | Value | Why |
| --- | --- | --- |
| `dynamic a spinneret 8080` with `refresh 5s` | DNS-based upstreams | Replicas are discovered by Compose DNS, so scaling needs no reconfiguration |
| `lb_policy least_conn` | least connections | Keeps long polls and event streams spread evenly |
| `health_uri /readyz`, `health_interval 2s` | readiness, not liveness | A draining replica is taken out of the pool before it closes its listener |
| `fail_duration 2s`, `max_fails 1` | one failed dial parks an instance for 2 s | With `least_conn` a dead replica otherwise keeps being picked — it has the fewest active connections. The window is short on purpose: a longer one turns a connection burst into `503 no upstreams available` |
| `lb_retries 20`, `lb_try_duration 10s`, `lb_try_interval 250ms` | retry budget | Covers a replica that is gone, and the window in which *every* upstream is parked |
| `request_buffers 128KiB` | buffered POST bodies | Every RPC is a POST; a proxy can only replay one onto another upstream if it has the body. Larger bodies (identity and proxy imports) stream and are not retried |
| `flush_interval -1` | no buffering | Long polls (`WatchConfig`, up to 60 s) and the console's event stream must not be buffered or cut |
| `dial_timeout 5s`, `read_timeout 90s`, `write_timeout 90s`, `keepalive 90s` | transport | A dial that times out counts as a failure and parks a replica that is merely busy, so the dial timeout is well above the worst-case accept latency of a burst |

Without `lb`, nothing is published to the host. You can replace it with your own proxy — see
[Behind a reverse proxy, and TLS](#behind-a-reverse-proxy-and-tls) — but then those eight rows become
your configuration to get right.

### The profile-gated services

| Service | What it is | What needs it |
| --- | --- | --- |
| `init-admin` (`init`) | One shot: `spnr admin init --username ${SPINNERET_ADMIN_USERNAME:-admin} --password-env SPINNERET_ADMIN_PASSWORD`. Creates the first platform administrator, the tenant `default`, the namespace `default` and its default policies. Idempotent. Requires `SPINNERET_ADMIN_PASSWORD` in `.env` or Compose refuses to start it | Without it you have a running stack and no way to sign in |
| `prometheus` (`observability`) | `prom/prometheus:v2.54.1` with `config/prometheus.yml`: DNS service discovery on the name `spinneret`, port 8080, `/metrics`, every 15 s — so it scrapes each replica directly, not through the load balancer | Nothing. Metrics are on the server's own listener either way; this is a place to keep them. It has **no authentication** and the series describe every namespace in the deployment, so keep it on loopback |
| `mocktarget` (`test`, `loadtest`, `example`) | A scriptable fake target site on 9090 with an admin API, plus an authenticating HTTP forward proxy on 9091. Its rules are set at runtime: `curl -X PUT 127.0.0.1:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'` | The e2e suite, the load suite, and the example walkthrough. Nothing in production |
| `example-crawler` (`example`) | The FastAPI example node from `examples/fastapi-crawler`, pointed at `http://lb:8080` and `http://mocktarget:9090`. Needs `EXAMPLE_TOKEN`, which `scripts/example-quickstart.sh` writes into `.env`; it refuses to start without one | The example walkthrough |
| `k6` (`loadtest`) | `grafana/k6:latest` running `test/load/${K6_SCRIPT:-acquire_report.js}` against `http://lb:8080` with `LOADTEST_TOKEN` | The load suite. `spnr seed` prints a suitable token |

None of the five has a healthcheck, so `--wait` never waits for one of them. `init-admin` is the one-shot
of the group: `restart: "no"`, `healthcheck: disable: true`, and
`depends_on: migrate: service_completed_successfully`, so it runs against a migrated schema and exits.
`prometheus`, `mocktarget` and `example-crawler` are `restart: unless-stopped`; `k6` sets no restart policy
at all, so it runs its script once and stays exited.

### Enabling a profile

A profile adds containers; it changes nothing about the core stack.

```bash
cd deploy/compose

# one-shot: bootstrap the first administrator
docker compose --profile init run --rm init-admin

# long-running: add Prometheus and keep it
docker compose --profile observability up -d

# the same, through the installer's control script
./spnrctl --profile observability up -d
```

The installer bakes `--profile observability` into the generated `spnrctl` when you answer yes to question
6, so you do not have to remember it. `COMPOSE_PROFILES=observability` in the environment does the same for
plain `docker compose`. `make e2e`, `make load` and `make example` pass the profiles they need themselves.

---

## The files a deployment owns

Four things, and it is worth knowing which of them you can lose.

| | What it is | Mode | Replaceable? |
| --- | --- | --- | --- |
| `deploy/compose/.env` | The two database passwords, the first administrator's password, the port, the replica count, and anything else you set. Written from `.env.example`, which carries a comment for every variable | `0600` | **No, in practice.** The database passwords are what the `pgdata` and `chdata` volumes were built with; a new file will not open them. Changing one later needs an `ALTER ROLE` inside the container as well |
| `deploy/compose/secrets/kek.key` | The key-encryption key — see below | `0644` in a `0700` directory | **Never.** There is no recovery path |
| `deploy/compose/config/` | `Caddyfile`, `clickhouse-listen.xml`, `clickhouse-limits.xml`, `prometheus.yml` | from the repository | Yes, they come from the checkout. Your edits do not — `Caddyfile` reloads on save, the others need a restart of their container |
| The volumes `pgdata`, `valkeydata`, `chdata` | PostgreSQL's data directory, Valkey's AOF, ClickHouse's storage | Docker's | `pgdata` **only from a backup**. `valkeydata` is derived: `spnr rebuild` regenerates it from PostgreSQL. `chdata` is analytics that expire on their own TTL |

`.env` and `secrets/` are both git-ignored and both excluded from the Docker build context, so neither can
travel into an image by accident.

A backup of this deployment is three things and it is only a backup if it is all three: `postgres.dump`,
`kek.key` and `env`. Valkey and ClickHouse are deliberately not in it.
[Operations → Backups](./16-operations.md#backups) has the commands and the restore drill.

---

## The key-encryption key

`deploy/compose/secrets/kek.key` holds one or more lines of `id:base64`, mounted into the containers as
the Docker secret `kek` at `/run/secrets/kek` (`SPINNERET_KEK_FILE`). It is the root of the vault:
identity payloads, proxy URLs, secret versions and notification credentials are each encrypted with a
per-record data key, and that data key is wrapped with this one. Only wrapped keys and ciphertext are
stored; the KEK never touches the database.

**Back it up before you put data into the deployment, and again after every rotation.** A database dump
without it is unrecoverable for every encrypted field, and a dump restored against the wrong key fails
with `is the KEK the one used to initialize this database?`. Nothing but the right key fixes that.

```bash
cp deploy/compose/secrets/kek.key ~/somewhere-safe/spinneret-kek-$(date -u +%Y%m%d).key
```

The file is mode `0644` **on purpose**, and that is not a mistake to fix. Compose bind-mounts that exact
file into the container, which runs as the distroless `nonroot` user (uid 65532) and matches no host user;
a `0600` file is unreadable there and the server exits with
`vault: read kek file "/run/secrets/kek": … permission denied`. The protection belongs on the directory,
which is `0700`.

Generating, rotating, re-wrapping and retiring keys: [Operations → Key rotation](./16-operations.md#key-rotation)
and `spnr kek --help`. The variables: [Configuration → Keys](./03-configuration.md#keys). The threat model:
[Secret vault](./10-secrets.md).

---

## Compose override files

Compose merges `-f` files in order, and the installer's `spnrctl` passes them in the right one:
`docker-compose.yml`, then `compose.image.yml` or `compose.build.yml`, then `compose.host.yml`. Two merge
tags matter and both need Compose 2.24:

- `ports: !override` **replaces** the published-port list instead of appending to it. A plain merge would
  leave two mappings for the same container port and the second would fail to bind.
- `build: !reset null` removes the build section, so the repository root does not have to exist for
  Compose to resolve a context that will not be used.

`compose.host.yml` is yours to edit — its own header says so. It carries the bind address, because the
shipped file publishes `"${SPINNERET_PORT:-8080}:8080"` with no host part and putting `127.0.0.1:8080` into
`SPINNERET_PORT` would break every other consumer of that variable:

```yaml
services:
  lb:
    ports: !override
      - "${SPINNERET_BIND_HOST:-127.0.0.1}:${SPINNERET_PORT:-8080}:8080"
  prometheus:
    ports: !override
      - "127.0.0.1:${PROMETHEUS_PORT:-9090}:9090"
```

On a host under about 7.6 GiB it also carries `mem_limit` for `clickhouse`, `postgres`, `valkey` and
`spinneret`. Those are ceilings, not reservations.

Anything else you want to change about the stack belongs in a file of your own, passed after these — not
in edits to `docker-compose.yml`, which an upgrade will move under you.

```bash
docker compose -f docker-compose.yml -f compose.image.yml -f compose.host.yml -f compose.mine.yml up -d
```

---

## Ports and networking

| Port | Service | Variable | Publish it? |
| --- | --- | --- | --- |
| 8080 | Console and node API, through `lb` | `SPINNERET_PORT`, address from `SPINNERET_BIND_HOST` | Yes — this is the deployment. Behind TLS if anything but this host reaches it |
| 9090 | Prometheus, profile `observability` | `PROMETHEUS_PORT` | **Loopback only.** No authentication, and its series describe every namespace |
| 19090 / 19091 | Mock target site / its forward proxy, profiles `test`, `loadtest`, `example` | `MOCK_TARGET_PORT`, `MOCK_PROXY_PORT` | Never outside a test host |
| 18000 | Example crawler, profile `example` | `EXAMPLE_PORT` | Never outside a test host |

**Not published, and nothing in the design wants them to be:** PostgreSQL 5432, Valkey 6379, ClickHouse
9000 and 8123, and the server replicas' own 8080. They are reachable only on the Compose network.
(`make infra-up` does publish 45432, 46379, 49000 and 48123 — that is
`deploy/compose/docker-compose.infra.yml`, a separate Compose project called `spinneret-infra` for running
the test suite, with `fsync=off` and no persistence. Never point a deployment at it.)

Two more listeners exist and are off by default:

| Variable | What it opens | Rule |
| --- | --- | --- |
| `SPINNERET_METRICS_ADDR` | Moves `/metrics` to its own listener. Empty (the default) leaves it on the API listener, **unauthenticated** | Set it to a loopback or private address if the API listener is reachable by anyone who should not read your metrics |
| `SPINNERET_PPROF_ADDR` | The `net/http/pprof` debug endpoints | Unauthenticated, and they expose heap contents and goroutine stacks. Validation refuses to put it on the API or metrics address. Keep it unreachable from untrusted networks |

Outbound, the server needs: PostgreSQL, Redis, optionally ClickHouse, whatever
`SPINNERET_PROXY_CHECK_URL` points at (the proxy health checker fetches it *through* every proxy — on a
host with no internet access, point it somewhere reachable or every proxy is marked dead), your
notification channel endpoints, and nothing else. It never contacts a target site itself.

---

## Behind a reverse proxy, and TLS

The server can terminate TLS itself with `SPINNERET_TLS_CERT_FILE` and `SPINNERET_TLS_KEY_FILE` (set
together or neither; the key pair is loaded at startup so a broken certificate fails the start rather than
the first request, and the minimum version is TLS 1.2). The usual setup is a reverse proxy: the stack ships
Caddy as the in-network load balancer, and you put Caddy, nginx or a cloud load balancer in front of it —
or replace it.

### Get the client IP right first

This is the one that bites. Three things depend on knowing which address a request actually came from:

- **Token IP allowlists.** An API token can be restricted to CIDRs; with the wrong client IP either every
  request is rejected or the restriction is meaningless.
- **Rate limits**, including login throttling.
- **The audit log**, which records the address every administrative action came from.

`SPINNERET_TRUSTED_PROXIES` is the list of CIDRs and bare addresses whose `X-Forwarded-For` and
`X-Real-Ip` headers are believed. It is **empty by default**, which means the direct peer address is used
and forwarding headers are ignored — safe, and wrong the moment there is a proxy. The Compose stack sets
`172.16.0.0/12,10.0.0.0/8,192.168.0.0/16`, which covers Docker's own networks; add the address of your
edge proxy if it is somewhere else.

```bash
SPINNERET_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12,192.168.0.0/16
```

Entries are CIDRs (`10.0.0.0/8`, `2001:db8::/32`) or bare addresses (`192.0.2.1`, `::1`). Addresses are
normalised before comparison, so `10.0.0.0/8` matches a peer reported as `[::ffff:10.1.2.3]`. Set it too
wide and any client can claim any address; set it too narrow and every client looks like your proxy.

### What the proxy in front must do

| Requirement | Caddy | nginx |
| --- | --- | --- |
| Pass `X-Forwarded-For` and `X-Forwarded-Proto` | default | `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` and `X-Forwarded-Proto $scheme;` |
| **Do not buffer** long polls or event streams. `WatchConfig` blocks up to 60 s (default 30 s) and the console's `/api/v1/events/stream` is endless | `flush_interval -1` | `proxy_buffering off;` with `proxy_read_timeout 120s;` |
| Health-check **`/readyz`**, about every 2 s — not `/healthz`, which only says the process is alive | `health_uri /readyz`, `health_interval 2s` | `health_check uri=/readyz interval=2s` (nginx Plus), or an external check |
| Buffer request bodies if you want POST retries. Every RPC is a POST | `request_buffers 128KiB` | `proxy_request_buffering on;` (the default) |
| Speak HTTP/2 end to end if clients use gRPC | default | `grpc_pass` with `http2` |
| Idle and read timeouts above the long-poll ceiling | `read_timeout 90s` | `proxy_read_timeout 120s;` |

```nginx
location / {
    proxy_pass         http://spinneret_upstream;
    proxy_http_version 1.1;
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
    proxy_buffering    off;            # long polls and the event stream
    proxy_read_timeout 120s;
    client_max_body_size 64m;          # identity and proxy imports
}
```

The server's own HTTP timeouts, for reference when tuning a proxy around it: 10 s to read request headers,
6 minutes to read a request, 120 s idle, no write timeout (long polls and streams outlive any fixed
bound — unary calls are bounded by a deadline interceptor instead). Node requests are capped at 8 MiB and
administrative ones at `SPINNERET_ADMIN_MAX_REQUEST_BYTES` (64 MiB).

### Why `/readyz` and not `/healthz`

A replica that is asked to stop does four things in order: it marks itself draining, so `/readyz` answers
`503 {"status":"draining"}`; it disables HTTP keep-alives, so every response in the drain window carries
`Connection: close` and no peer sends a new request on a pooled connection that is about to close; it waits
`min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4)` — five seconds with the default 30 s — *before* closing its
listener; and only then does it shut the listener down and stop its background loops.

That first window is the whole reason a rolling restart can be invisible to nodes: a proxy that probes
`/readyz` every 2 s has taken the replica out of its pool before it stops accepting. A proxy that probes
`/healthz`, or that probes nothing, will cut requests off instead. `503 {"status":"draining"}` for more
than about five seconds per replica means whatever is in front is not honouring `/readyz`.

### HTTP/2, h2c and websockets

There are no websockets. The console's live updates are Server-Sent Events on `/api/v1/events/stream`, and
node config watching is a long-poll `WatchConfig` call — both are ordinary HTTP responses that must not be
buffered. What *does* need attention is HTTP/2: on a plain listener the server enables unencrypted HTTP/2
(h2c) as well as HTTP/1.1, and on a TLS listener HTTP/1.1 and HTTP/2. Connect-over-JSON works over
HTTP/1.1 and needs nothing special; the Go SDK's gRPC transport needs h2c or HTTP/2 over TLS all the way
through.

### Cookies and origins

With TLS at the edge, leave `SPINNERET_COOKIE_SECURE=auto` — the default, which marks the session cookie
Secure when the request arrived over TLS, **or** when a **trusted** proxy sent
`X-Forwarded-Proto: https`. Trusted means the direct peer matches `SPINNERET_TRUSTED_PROXIES`, and that
list is empty by default: behind a proxy you have not listed there, `auto` silently resolves to
not-Secure. Set it to `true` to force it, and do that on any console published beyond loopback. `false` is
for a plain-HTTP intranet and nothing else.

If the console is served from a different origin than the API, list that origin in
`SPINNERET_ALLOWED_ORIGINS`. In the shipped layout they are the same origin and it stays empty.

---

## Scaling out

Server instances are stateless. They hold no local data, and everything that has to be coordinated is
coordinated through Redis and PostgreSQL: report stream shard ownership, leader election for periodic jobs
(PostgreSQL advisory locks), catalog and config invalidation (Redis pub/sub), and the two instance
registries.

```bash
SPINNERET_REPLICAS=4 ./spnrctl up -d --wait
```

### What is safe to scale, and what is not

| | |
| --- | --- |
| **Server replicas** | Safe, and the intended way to add capacity. The PostgreSQL pool is **per instance**, so adding replicas multiplies connection demand: raise PostgreSQL's `max_connections`, or lower `SPINNERET_DATABASE_MAX_CONNS`, so that `replicas × SPINNERET_DATABASE_MAX_CONNS + headroom < max_connections` still holds (300 in this stack) |
| **PostgreSQL** | Vertically, and with read replicas of your own arrangement. The server writes to one primary |
| **Redis** | One instance carries the design targets and is the ceiling for acquire throughput. `SPINNERET_REDIS_ADDRS` enables Cluster mode. **Never enable key eviction** |
| **ClickHouse** | Independently; it is only written in batches and read by console queries |
| **`SPINNERET_REPORT_SHARDS`** | **Not safe to change on a running deployment.** Lease IDs encode the shard their reports must go to, so changing the count strands every in-flight lease. It is a maintenance window: [Operations → Changing the shard count](./16-operations.md#changing-the-shard-count) |

### The api/worker role split

`SPINNERET_ROLE` (or `spinneret-server --role`) takes `all`, `api` or `worker`. `all` is the default and
does both.

| Role | Serves | Runs |
| --- | --- | --- |
| `api` | The node API, the console, `/healthz`, `/readyz`, `/metrics` | No background loops |
| `worker` | `/healthz`, `/readyz`, `/metrics` **only** | The report pipeline, breaker evaluation, lease reaping, proxy health checks, alert evaluation, hot-state snapshots, partition maintenance |
| `all` | everything | everything |

Split them when serving and processing have different scaling curves — a fleet that grows because of
long-poll count does not need more report workers. Keep **at least two workers** so one failure does not
stall the pipeline, and remember that a `worker` instance answers `/readyz`, so do not let your load
balancer put it in the traffic pool.

### How report shards are divided

Every worker instance registers a heartbeat in Redis every 2 s; a heartbeat is live for 10 s. From the
sorted list of live members each instance computes a target of `ceil(shards / live)` shards, starts probing
at `position × shards / live` so two instances do not fight over the same shard, and takes a per-shard lock
with a 10 s TTL refreshed every 3 s. Shards above the target are released. Each owned shard runs one
sequential consumer in the consumer group `workers`, which is what makes report processing per-identity
ordered.

The consequences for sizing: **one shard has exactly one owner**, so the shard count is the hard ceiling on
report parallelism. Keep 2–4 shards per worker instance. More worker instances than shards leaves some
instances idle. Reading the two signals together: growing `spinneret_stream_pending` with busy workers
means the workers are the bottleneck; evenly spread pending entries with idle CPU means too few shards.

### How the acquire admission budget is divided

This is the part that changes how you think about replica count, and it is new — read it before you scale.

An `Acquire` that fails costs about five times one that succeeds, so a fleet that queues every request at
Redis degrades by collapsing rather than by slowing down. Admission control caps how many `acquire` scripts
each instance has in flight at Redis and sheds the excess **in the server**, before any Redis command is
issued.

**The throughput figures in this page were measured without this gate** — including the ~5,000 acquires/s
per instance the sizing table is built on — and none of them has been re-measured with it on. Use them as
the shape of the load, not as a fleet plan to the decimal — the
[Performance page](./17-performance.md#admission-control) has the A/B recipe for measuring your own.

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | `64` (0–65536) | Concurrent acquire scripts the **whole fleet** may have in flight. Each API instance admits this divided by the number of live API instances it sees, clamped to `[4, 4096]`. `0` turns admission control off |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | `0` = derive (0–4096) | Pins this instance's limit instead of dividing the fleet budget, and stops the heartbeat that counts peers |

The division uses the same kind of registry as the shards, with one deliberate difference: every API
instance heartbeats every 2 s, a heartbeat is live for **60 s**, and each instance re-divides on every beat.
Thirty beats of slack, where the worker shard registry allows five, because a beat needs Redis and the
moment this gate matters is the moment Redis is saturated. Measured with a 10 s window, a busy instance
missed enough beats to be pruned by its peer, which then divided the fleet budget by one and admitted all
of it — widening the gate exactly when it should have held, which feeds the collapse the gate exists to
prevent. The cost of the longer window is that a crashed instance keeps its share for up to a minute. With the stack's default of two
replicas and a budget of 64, each admits 32. Scale to four and each admits 16 — the fleet total does not
move. An attempt waits up to 50 ms for a permit, never longer than its own remaining `wait_ms`; beyond that
it is shed as `unavailable` with reason `overloaded` and a retry hint jittered into 100–200 ms. A batch
permit is weighted by `count`.

So: **extra replicas add server capacity — long-poll capacity, report processing, availability — without
adding Redis concurrency.** That is deliberate, and it is why adding replicas to a fleet that is already at
the Redis knee does not help.

Three operational consequences:

1. **Every API instance needs a distinct `SPINNERET_INSTANCE_ID`.** The id is the registry member name the
   budget is divided by, so N instances sharing one id collapse into a single member, each reports
   `spinneret_acquire_peers = 1`, and each admits the whole budget — N times the intended concurrency at
   Redis. The default is the hostname plus a random suffix, which is distinct on Compose and on Kubernetes
   but *not* in a deployment that templates the hostname and then also pins the id.
2. **Pin `SPINNERET_ACQUIRE_MAX_INFLIGHT` on every API instance or on none.** A pinned instance does not
   register as an acquirer; the ones that derive then divide by too small a count, and the fleet total
   exceeds the budget by the pinned instances' whole allocation. Pin it when Redis capacity scales with the
   number of primaries (set the per-primary figure), when you measured your own ceiling, or for reproducible
   load tests.
3. **Raise the fleet budget rather than the replica count** when `overloaded` and
   `spinneret_acquire_queued` rise *while Redis CPU is below its ceiling*. If `exhausted` rises *with* Redis
   CPU, you are past the knee and the answer is less offered load or more Redis.

The measurements behind the default, and the congestion behaviour it prevents:
[Performance → Admission control](./17-performance.md#admission-control).

---

## Several stacks on one host

The Compose project name is what separates two deployments: volumes, networks and container names are all
derived from it. The installer takes `SPINNERET_PROJECT` (default `spinneret`) and writes it into the
generated `spnrctl`, so every later command lands on the right stack.

```bash
SPINNERET_PROJECT=staging SPINNERET_INSTALL_DIR=/srv/spinneret-staging \
SPINNERET_PORT=8081 bash install.sh
```

Two things to know. The shipped file tags what it builds `spinneret:local`, a name that is the same for
every project on the host; under any project name other than `spinneret` the installer writes
`compose.build.yml` to prefix it, so a build in one stack cannot overwrite the tag another stack's
containers were created from. And if two deployments ever share one Redis they need different
`SPINNERET_REDIS_PREFIX` values (default `sp`) — but the Compose stack gives each project its own Valkey,
so that only comes up in hand-written deployments.

---

## Multi-host and orchestrators

There is no Helm chart and no Kubernetes manifest in this repository. There does not need to be: the
container is an ordinary stateless HTTP service, and what an orchestrator has to know about it is short.

**What the container needs:**

| | |
| --- | --- |
| Environment | `SPINNERET_DATABASE_URL`, `SPINNERET_REDIS_URL` (or `SPINNERET_REDIS_ADDRS`), and one of `SPINNERET_KEK_FILE` / `SPINNERET_KEKS`. Everything else has a default — [Configuration](./03-configuration.md) |
| A distinct `SPINNERET_INSTANCE_ID` per instance | The default (hostname plus a random suffix) is already distinct; do not template it into something shared |
| The key-encryption key | As a mounted file (`SPINNERET_KEK_FILE`, which is how the Compose stack does it, via a Docker secret) or as `SPINNERET_KEKS` in the environment. A mounted file is preferable: environment variables are visible in more places |
| One port | 8080 by default, `SPINNERET_HTTP_ADDR` |
| A readiness probe | `GET /readyz`, every 2 s. The image's own healthcheck is `spnr healthcheck --url http://127.0.0.1:8080/readyz` |
| A termination grace period **above 40 s** | The Compose stack uses `stop_grace_period: 40s` against a 30 s `SPINNERET_SHUTDOWN_TIMEOUT`. Set the orchestrator's equivalent no lower |
| `SIGTERM` to stop | A second signal terminates a shutdown that is taking too long, so a supervisor that escalates is fine |

**What is stateless:** the whole container. It writes nothing to local disk, needs no volume, and runs
happily with a read-only root filesystem as the distroless `nonroot` user (uid 65532). There is no shell in
the image, so debugging is `kubectl logs`, `/metrics` and `/readyz` rather than `exec sh`.

**What has to be shared between hosts:** PostgreSQL, Redis, the key-encryption key, and ClickHouse if you
use it. All four are the same objects for every instance — not per-host copies. Two instances against
different Redis instances are two deployments that happen to share a database, and they will make
contradictory scheduling decisions.

**What an operator has to provide themselves:** the three databases and their tuning (start from the
settings in [The Compose stack](#the-compose-stack-service-by-service) — they are load-tested), TLS
termination, a load balancer that probes `/readyz` and does not buffer streams, backups of PostgreSQL and
the KEK, delivery of the KEK as a secret, metrics scraping, and the migration step. Run migrations as a
one-shot job before rolling the new version — `spnr migrate up`, or `spinneret-server --migrate` on one
instance — never as an init container on every replica; the advisory lock makes that safe but not useful.

---

## Upgrading

Migrations are embedded in the binary. The safe sequence with traffic running, which is what the
installer's menu entry 2 automates:

1. **Back it up.** Manage → 10, or the commands in [Operations → Backups](./16-operations.md#backups).
2. `spnr migrate status` — it prints the applied and the embedded schema version. Do it before and after.
3. **Pull or build first, pin second.** Writing a new `SPINNERET_IMAGE_TAG` into `.env` and *then* failing
   to fetch it leaves the deployment pointing at an image that does not exist.
4. **Migrate as its own step**, not as a side effect of `up`. `migrate` is a one-shot service that Compose
   may consider already satisfied when its container spec has not changed, and "the schema is at whatever it
   was" is not a thing to leave to inference. If migrations fail here, the old containers are still up and
   nothing was swapped.
5. **Then replace the replicas**, rolling if you can.
6. Watch `/readyz` on each replica and `spinneret_stream_pending` while the workers restart.

```bash
# published-image path
./spnrctl pull && ./spnrctl run --rm migrate && ./spnrctl up -d --wait

# source path
git -C . pull && ./spnrctl build && ./spnrctl run --rm migrate && ./spnrctl up -d --wait
```

**Rolling the replicas.** Compose recreates every replica of a scaled service together, which with two
replicas is a five-to-ten-second window where nothing new starts. The load balancer's retries cover it, but
they do not have to be spent. Removing one container and running
`up -d --no-recreate --no-deps spinneret` fills the empty slot from the new spec and leaves the others
alone:

```bash
# one replica at a time; --wait blocks until the new container is healthy
for id in $(./spnrctl ps -q spinneret); do
  docker rm -f "$id"
  ./spnrctl up -d --no-recreate --wait --no-deps spinneret
done
./spnrctl up -d --wait        # the load balancer and anything else, in one pass at the end
```

Note that the container is removed with plain `docker rm -f` on its id, not with `docker compose rm`:
Compose addresses a *service*, and one replica of a scaled service is not a service.

The 40 s stop grace period is what makes each step clean: the container being removed drains for five
seconds with `/readyz` answering `draining`, finishes in-flight requests, and flushes its background loops
before Docker gives up on it. The installer does exactly this, one replica at a time, and falls back to a
plain recreate the moment anything about it does not work — a slower upgrade is better than a clever one
that half-finishes.

**Rolling back.** Move `SPINNERET_IMAGE_TAG` to the previous tag and repeat the sequence. That is the fast
path, and it works **when the schema did not change**. It does not work backwards across a migration:
**never roll back with `spnr migrate down`** — a down migration drops tables and the data in them.
Restoring the backup is the safe path.

**What a schema migration does and does not allow.** Forward migrations are applied under a PostgreSQL
advisory lock, so several instances starting at once is safe, and an old replica and a new one can serve
side by side for the length of a rolling restart. What they cannot do is make an old binary understand a
new schema indefinitely — keep the mixed window to a restart, not a week. Two things must never change
across an upgrade: `SPINNERET_REPORT_SHARDS` (lease IDs encode the shard) and the key-encryption key that
the database was initialised with.

Nodes do not have to be upgraded in lockstep: the node API and the SDKs ignore unknown JSON fields in both
directions, and SDK clients retry `unavailable`, which is what an in-flight POST to a stopping replica
looks like.

The full checklist: [Operations → Upgrades and migrations](./16-operations.md#upgrades-and-migrations).

---

## Uninstalling

Three levels, in this order. The installer's menu entry 4 offers exactly these and requires you to type
`delete` — not a y/n — for levels 2 and 3.

```bash
# 1. Stop the stack, keep every byte of data.
./spnrctl down --remove-orphans

# 2. Also delete the data volumes: pgdata, valkeydata, chdata.
./spnrctl down -v --remove-orphans

# 3. Also remove the install directory, including .env and secrets/kek.key.
cat deploy/compose/secrets/kek.key      # copy it somewhere first, if you may ever need the dumps
rm -rf /opt/spinneret
```

What each level costs:

| After | Recoverable |
| --- | --- |
| Level 1 | Everything. `./spnrctl up -d --wait` brings it back |
| Level 2 | Every identity, proxy, account, policy, config item, secret, user, token and audit record in the deployment is gone. The key-encryption key survives in the install directory, so **a dump taken with it can still be restored into a fresh stack** |
| Level 3 | Nothing, unless `kek.key` and `.env` are already off this machine. Without that key no backup of this deployment can ever be opened again, by anyone |

Docker images are left alone at every level: nothing here prunes a shared host. `docker image prune` and
`docker builder prune` are yours to run, and note that BuildKit's cache is per host, not per project —
clearing it makes the next build of anything on that machine a cold one.

The installer's level 3 additionally refuses any path that does not contain
`deploy/compose/docker-compose.yml`, and refuses `/`, `/usr`, `/etc`, `/var`, `/home`, `/root`, `/opt`,
`/bin`, `/sbin`, `/lib` and `/boot` outright.

---

## Running without Docker

Supported in the sense that the binary has no container dependencies. Not supported in the sense that there
is no script, and the tuning the Compose stack does for you becomes yours. Here is the honest version.

### What you must install first

| | Version | Why |
| --- | --- | --- |
| PostgreSQL | 17 | The store. Start from the stack's parameters: `max_connections` above `instances × SPINNERET_DATABASE_MAX_CONNS` plus headroom, `shared_buffers` 512 MiB or more, `wal_compression=on` |
| Valkey or Redis | Valkey 8 / Redis 7+ | Persistent (`appendonly yes`, `appendfsync everysec`), RDB save points off, `auto-aof-rewrite-percentage 300`, `auto-aof-rewrite-min-size 1gb`, **`maxmemory-policy noeviction`** and no `maxmemory`. Those five are not optional preferences — see [valkey](#valkey) |
| ClickHouse | 25.8, optional | Only if you want raw request events. Cap its caches as `deploy/compose/config/clickhouse-limits.xml` does |
| Go | 1.27.1 or newer | To build |
| Node | 22.13 or newer | To build the console |
| pnpm | 10.27.0 | `corepack enable` installs the pinned version from `web/package.json` |

### Build

The console is compiled into the binary with `go:embed`, so **the web build must happen before the Go
build**. Without it only `web/dist/.keep` is embedded and the server serves no UI files — which looks like a
broken deployment rather than a missing step.

```bash
git clone https://github.com/TikHub/Spinneret.git
cd Spinneret

make web-install      # cd web && pnpm install --frozen-lockfile
make web              # cd web && pnpm build       -> web/dist
make build            # -> bin/spinneret-server and bin/spnr
```

`make build` is two `go build` invocations with `CGO_ENABLED=0 -trimpath` and the version stamped in via
`-ldflags`; the result is two static binaries you can copy to a host that has neither Go nor Node. That is
also what the container does, in the same order — `deploy/docker/Dockerfile` is a three-stage build (Node,
then Go, then distroless) and it copies `web/dist` from the first stage into the second.

Copy `bin/spinneret-server` and `bin/spnr` to `/usr/local/bin/`.

### Configure

Everything is `SPINNERET_*` environment variables; there is no configuration file. At minimum:

```bash
# /etc/spinneret/spinneret.env  — mode 0600, owned by root
SPINNERET_HTTP_ADDR=127.0.0.1:8080
SPINNERET_DATABASE_URL=postgres://spinneret:…@db.internal:5432/spinneret?sslmode=require
SPINNERET_REDIS_URL=redis://cache.internal:6379/0
SPINNERET_KEK_FILE=/etc/spinneret/kek.key
SPINNERET_TRUSTED_PROXIES=10.0.0.0/8
SPINNERET_COOKIE_SECURE=true
SPINNERET_LOG_FORMAT=json
```

Generate the key file once, with `spnr kek generate`, and back it up before you put any data in:

```bash
spnr kek generate > /etc/spinneret/kek.key    # one line: k1:<base64 of 32 random bytes>
chmod 0600 /etc/spinneret/kek.key             # 0600 is correct here — no container user to satisfy
chown spinneret: /etc/spinneret/kek.key
```

Validate the whole environment before starting anything. `spnr config check` prints it back, validated,
with every credential redacted — it catches a bad duration, an out-of-range shard count or a missing
required variable in a second rather than in a crash loop:

```bash
set -a; . /etc/spinneret/spinneret.env; set +a
spnr config check
```

### Migrate and bootstrap

```bash
spnr migrate up                 # embedded migrations, serialized by an advisory lock
spnr migrate status             # applied version and embedded version
printf '%s\n' "$ADMIN_PASSWORD" | spnr admin init --username admin --password-stdin
```

`spnr admin init` is idempotent: with a platform administrator already present it prints
`already initialized` and exits 0.

### A systemd unit

```ini
# /etc/systemd/system/spinneret.service
[Unit]
Description=Spinneret control plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=spinneret
Group=spinneret
EnvironmentFile=/etc/spinneret/spinneret.env
ExecStart=/usr/local/bin/spinneret-server
Restart=always
RestartSec=2
# SIGTERM starts the drain; a second signal aborts it. Keep this above
# SPINNERET_SHUTDOWN_TIMEOUT (default 30s) so systemd does not kill a draining instance.
KillSignal=SIGTERM
TimeoutStopSec=45
# The process writes nothing to local disk.
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
NoNewPrivileges=yes
ReadOnlyPaths=/etc/spinneret

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now spinneret
curl -s 127.0.0.1:8080/readyz
```

Add `--role api` or `--role worker` to `ExecStart` for a split fleet, and run the units on separate hosts
or as separate unit instances. Keep at least two workers.

### What is still yours to do

- A reverse proxy in front, configured as in [Behind a reverse proxy, and TLS](#behind-a-reverse-proxy-and-tls),
  or `SPINNERET_TLS_CERT_FILE` / `SPINNERET_TLS_KEY_FILE` on the server itself.
- **A distinct `SPINNERET_INSTANCE_ID` per instance.** The default is the hostname plus a random suffix,
  which is already distinct — the trap is setting it to the same value from a template. Getting it wrong
  multiplies the fleet's Redis concurrency by the number of instances; see
  [Scaling out](#how-the-acquire-admission-budget-is-divided).
- Backups of PostgreSQL **and** the key file, and a restore drill — [Operations](./16-operations.md#backups).
- Scraping `/metrics`, and moving it to `SPINNERET_METRICS_ADDR` if the API listener is exposed.
- Upgrades: build, `spnr migrate up`, then restart the units one at a time.

Everything else — configuration, policies, keys, scaling, retention — is identical to the Compose
deployment, because none of it is container-specific.

---

## When the install goes wrong

| What you see | What it means |
| --- | --- |
| `Compose is 2.20.x; this stack needs 2.24 or newer` | Update Docker. The override files use merge tags that do not exist before 2.24 |
| `No terminal to ask questions on` | The script was piped with no `/dev/tty` available. Download and run it, or pass `--yes` |
| `required variable PG_PASSWORD is missing a value` | Compose found no `.env` to read. Either `deploy/compose/.env` does not exist yet — run `./scripts/compose-init.sh` — or `--env-file` / `COMPOSE_ENV_FILES` points somewhere else. Compose looks for it next to the Compose file, not in your current directory |
| `Docker is running, but your user cannot reach it` | Your user is not in the `docker` group. That group is root on most machines, which is why nothing here adds you to it for you |
| `tikhubio/spinneret:latest cannot be fetched from here` | No such tag published yet, or this host cannot reach the registry. Nothing is wrong with the checkout; build from source instead |
| `vault: read kek file … permission denied` | `kek.key` is not readable by the container's user. It must be `-rw-r--r--`; do not "harden" it to `0600` |
| `vault: kek "k1" must be 32 bytes, got N` | Truncated or corrupt key file |
| `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` | The wrong key for this database — the restore-with-the-wrong-key signature. Nothing but the right key fixes it |
| `bind: address already in use` | Something else has the port. `ss -ltnp 'sport = :8080'` names it; change `SPINNERET_PORT` in `.env` and bring the stack up again |
| `connect clickhouse: …` at startup | `SPINNERET_CLICKHOUSE_URL` is set and ClickHouse is not reachable — often OOM-killed on a small host. Raise the RAM, or empty the variable to run without analytics |
| ClickHouse will not start, log mentions `nofile` | The container asks for 262,144 descriptors soft and hard. Check `ulimit -Hn` on the host |
| `/readyz` never turns `ok` | The body names the dependency: `postgres`, `redis`, `catalog` or `hotstate`. `hotstate: building` is normal on a first start; `hotstate: epoch missing (rebuild pending)` needs `spnr rebuild` |
| `429 no_proxy_available` from every node | Usually `SPINNERET_PROXY_CHECK_URL`: it defaults to a URL on the public internet, and a host that cannot reach it marks every proxy dead. Point it at something reachable |
| The console loads but shows no UI files | A binary built without the web build. `make web` before `make build` |
| The source build fails | Build the one service on its own so the error is not buried in a progress stream: `./spnrctl build spinneret` |

Every one of these, plus the ones that happen after the install, with the log lines to grep for:
[Troubleshooting](./18-troubleshooting.md).

---

## Next

- [Configuration reference](./03-configuration.md) — every `SPINNERET_*` variable, its default, its valid
  range and when you would change it.
- [Operations runbook](./16-operations.md) — backups and restore drills, key rotation, upgrades, scaling,
  hot-state rebuilds, retention, incident playbooks.
- [Security](./19-security.md) — the hardening checklist to work through before this is reachable by anyone
  else.
- [Observability and alerting](./12-observability.md) — the metrics to scrape and the signals worth
  alerting on.
- [Performance and tuning](./17-performance.md) — the measured numbers, the congestion behaviour, and the
  tuning levers in the order they pay.
- [Troubleshooting](./18-troubleshooting.md) — by the error text you are looking at.
- [CLI reference](./15-cli.md) — every `spnr` command and every `spinneret-server` flag.
