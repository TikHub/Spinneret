# One-command install / 一键部署

**English below · 中文见末尾**

Two scripts, same behaviour, different language. Both install with **Docker
only** — running the stack without Docker means putting PostgreSQL, Valkey,
ClickHouse, Go and Node on the host yourself, which is a document rather than a
script: [Installation](../documents/en/02-installation.md).

| | Script | Docs |
|---|---|---|
| English | [`install.sh`](./install.sh) | [Quickstart](../documents/en/01-quickstart.md) · [Installation](../documents/en/02-installation.md) |
| 中文 | [`install.zh.sh`](./install.zh.sh) | [快速开始](../documents/zh/01-quickstart.md) · [安装与部署](../documents/zh/02-installation.md) |

---

## Read it before you run it

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh -o install.sh
less install.sh          # 2200 lines, every decision commented
bash install.sh
```

That is the recommended order, and it is not ceremony. Anything you pipe into a
shell runs as you, and on the Docker step the script offers to run something as
root. It is written to be read: it says what it is about to do before each step
and asks before every change.

The single line, if you would rather:

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh | bash
```

Piping still works, questions included — answers are read from your terminal
(`/dev/tty`) rather than from stdin, because with `curl | bash` stdin *is* the
script. If there is no terminal at all, the script says so and stops rather than
quietly taking defaults for a question like "which address do I publish on".
For a genuinely unattended run, say so explicitly:

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh | bash -s -- --yes
```

## The one thing that is different about this deployment

`deploy/compose/secrets/kek.key` is the key-encryption key. Every stored
credential, every proxy URL and every vault secret in this deployment is
encrypted with it. **There is no recovery path without it** — a database dump
restored against the wrong key fails with `is the KEK the one used to initialize
this database?`, and nothing but the right key fixes that.

The script generates it once, **never overwrites an existing one**, and tells you
to back it up before it tells you anything else. Copy it off the machine before
you put any data into the deployment. Back it up together with `.env`, whose
passwords are what the database volumes were built with.

## What it asks

Seven questions, each with a default you can accept with enter:

1. **Where to install.** `/opt/spinneret` as root, `~/spinneret` otherwise.
2. **Keep it on `127.0.0.1`?** Yes by default. Saying no publishes on `0.0.0.0`
   and warns you: the console is an admin surface and `/metrics` is on the same
   listener with no authentication, so that wants a TLS reverse proxy in front
   and `SPINNERET_COOKIE_SECURE=true` in `.env`.
3. **Which port.** `8080`.
4. **Administrator username.** `admin`. Validated against the server's own rule
   (`^[a-z0-9][a-z0-9._-]{2,63}$`) so a rejected name costs a keystroke rather
   than a failed bootstrap.
5. **How many server replicas.** Default is one per two cores, clamped to 1–4,
   and forced to 1 under 4 GiB of RAM. With one replica an upgrade has a short
   window where nothing serves; with two there is none, at roughly 150–500 MiB
   more RAM.
6. **The observability profile.** Off by default. On, it adds Prometheus
   scraping the server's `/metrics`, published on `127.0.0.1` only — always,
   whatever the console is bound to, because it has no authentication.
7. **Published image or build from source.** It checks whether
   `ghcr.io/tikhub/spinneret:latest` can actually be fetched *before* asking, so
   the question knows which way it is about to go. Pulling takes about a minute;
   building takes 5–15 minutes and about 2 GB of build cache on a first build.

If there is already a checkout in the directory it asks one more: whether to
update it to the latest `main` (`fetch` + `merge --ff-only`, never `reset
--hard`).

## What it does

- Detects the distribution and offers the right way to install Docker. Debian,
  Ubuntu and derivatives; RHEL, CentOS, Rocky, AlmaLinux, Fedora, Oracle,
  Amazon Linux; openSUSE and SLES; Arch and Manjaro; Alpine; NixOS, Void,
  Gentoo. macOS points at Docker Desktop or OrbStack; WSL points at the Windows
  side. Where Docker's own convenience script does not support the distribution
  (Arch, Alpine and friends) it prints the one package-manager command instead
  of offering to pipe a script that would refuse you.
- Refuses to guess on anything that is not `x86_64` or `arm64`, and warns under
  4 GiB of RAM or on a single core.
- Clones the repository, shallow, `--depth 1 --branch main`. The whole
  repository, because the build-from-source path needs it: the compose build
  context is the repository root.
- Generates the three passwords and the key-encryption key **on this machine**.
  Nothing is taken from the environment and nothing is sent anywhere.
- Writes `.env` from the shipped `deploy/compose/.env.example` with `awk` rather
  than from a block of `printf`s, so every comment in the example — and every
  variable added to it later — survives into the file you will read.
- **Scales to this host.** Under about 7.6 GiB of RAM it writes memory ceilings
  into `compose.host.yml`, so an unlucky moment takes the OOM killer to whatever
  was actually growing rather than to PostgreSQL.
- Writes a `spnrctl` wrapper carrying the four things that have to be right
  every single time and whose absence produces confusing behaviour rather than
  an error: the project name, the compose files in the right order, the working
  directory that makes `./config`, `./secrets` and `../..` resolve, and
  `COMPOSE_ENV_FILES`.
- Pulls or builds, applies migrations as an explicit step, starts everything,
  polls `/readyz` until all four dependencies report `ok` (up to 300 s — a cold
  host also has to initialise PostgreSQL, let ClickHouse create its schema and
  let the first replica build its hot state), creates the first administrator,
  and prints the console URL with the username and password.

## What it will not do

It writes only inside the directory you choose, plus Docker's own named volumes.
The one exception is the path leading to it: if the parent directories of the
directory you name do not exist they are created — named first, with `sudo`, and
with the command printed. Nothing else out there is touched: it never edits a
file outside that directory, never adds a cron job, never opens a firewall port,
and never installs anything without asking first.

It uses `sudo` for exactly three things, each printed before it runs: installing
Docker if you say yes, starting the Docker service, and creating the install
directory when that directory needs root. It will **not** add you to the
`docker` group — on most machines that is the same as handing out root, so it
prints the command and lets you decide.

It refuses to install into `/`, `/usr`, `/etc`, `/var`, `/bin`, `/sbin`, `/lib`,
`/boot`, `/home`, `/root` or `/opt` itself, into a git checkout of some other
project — decided by whether `deploy/compose/docker-compose.yml` and
`deploy/compose/.env.example` are actually in it, not by what a remote URL says —
or into a directory that already has something in it that is not a Spinneret
install.

It does not overwrite an existing `.env` (its passwords are what the database
volumes were built with; a new file would not open them) and it does not
overwrite an existing `kek.key`, ever.

## Run it again to manage the instance

The script works out which job it is doing. If there is already an install —
found by asking Docker which directory the `spinneret` Compose project was
started from, so it does not matter where you put it — it opens a menu instead
of installing:

```
==> An install is already here
    ✓ /opt/spinneret
    ✓ Running v0.1.0
    ✓ Image ghcr.io/tikhub/spinneret:latest

      1  Status — versions, containers, schema, disk
      2  Move to another image tag (re-pull, migrate, restart)
      3  Manage — accounts, tokens, backups, health, disk
      4  Stop or remove this install
      q  Quit
```

`--manage` goes straight there. Every setting is re-read from the install's own
files first, so an install created with one replica and no observability profile
is never upgraded as though it had two and the profile on.

An existing install is looked for on **every** run, including with `--yes` or
with `SPINNERET_INSTALL_DIR` set. `--yes` over a deployment that is already here
prints where it is and changes nothing: "take the default answer to every
question" is right for a fresh host and wrong for a live one, where it would mean
switching a build-from-source install to the published `latest`, applying that
image's migrations to the production database, turning the observability profile
off, and rewriting `compose.host.yml` over whatever you had edited into it. Use
`--manage` — or `spnrctl` — to work on an install that already exists.

**Manage** covers the things people otherwise ask how to do:

| | |
|---|---|
| Change an administrator password | Signs in as the account and changes its own password, exactly as the console does. Every other session of that account ends. Offers to update `SPINNERET_ADMIN_PASSWORD` in `.env` so it does not go stale. |
| Add an administrator | `AccessAdminService/CreateUser` with the tenant-wide `admin` role. There is no rename: accounts are created and their passwords reset. |
| List accounts | Username, id, and whether the account is a platform admin or disabled. |
| Create an API token | `spnr token create`. Only the plaintext is printed, once — it cannot be fetched again. |
| Rebuild the hot state | `spnr rebuild`. Warns that a fleet-wide rebuild deletes the epoch key first, so every replica reports not-ready until it finishes; naming one site avoids that. |
| Show the configuration | `spnr config check` — the whole `SPINNERET_*` environment, validated, with every credential replaced. The pre-flight for a hand-edited `.env`. |
| Health check | `/healthz` and `/readyz`, container status, and whether ClickHouse, Valkey or PostgreSQL has been OOM-killed. |
| Logs, restart | Restart offers to roll the replicas one at a time. |
| Back up now / Restore a backup | See below. |
| Free disk space | Images of **this Compose project** that no container references, and optionally the host's build cache — which is shared with every other build on the machine, and the prompt says so. |

Three of those — change a password, add a user, list users — do not exist in the
`spnr` CLI at all; they are console RPCs. The script reaches them over the same
Connect-over-JSON endpoint the console itself uses, on loopback. Passwords are
read without echo and travel to `curl` on stdin, never as a command-line
argument, because `argv` is readable by every process on the host.

Everything else delegates to `spnrctl` or to the `spnr` CLI inside the image, so
a menu entry cannot drift away from the command it stands for.

## Upgrading

Menu entry 2. It compares the running version against the latest GitHub release
— a pre-release suffix counts as older, so `v1.2.0-rc1` is behind `v1.2.0`.

On the published-image path it **pulls before it pins**: writing the tag into
`.env` and then failing to fetch it would leave the deployment pointing at an
image that does not exist. Once the pull succeeds, `SPINNERET_IMAGE_TAG` is
pinned to that exact tag rather than following a moving one, so you can say
which build is running and put it back. The same field takes an older tag, which
is how a rollback is done.

On the build-from-source path it fetches, `merge --ff-only`, and rebuilds.

Then migrations run as their own step, and only then are the replicas replaced.
If migrations fail, the old containers are still up and nothing was swapped.

**The restart is rolling when it can be.** Compose recreates every replica of a
scaled service together, which with two replicas is a five-to-ten-second window
where nothing new starts; the load balancer's retries cover it, but they do not
have to be spent. So with two or more replicas the script removes one container
and runs `up -d --no-recreate --no-deps spinneret` to fill the empty slot from
the new spec, one at a time, leaving the others alone. It falls back to a plain
recreate the moment anything about that does not work: a slower upgrade is
better than a clever one that half-finishes.

Your data is untouched — the named volumes survive. Two things to know anyway:

- **Back up first.** Menu entry 10 does it in one step.
- **Never roll back with `spnr migrate down`.** A down migration drops tables
  and the data in them. Restoring the backup is the safe path.
- **`SPINNERET_REPORT_SHARDS` must never change** across an upgrade. Lease ids
  encode their shard; changing it strands in-flight leases. The installer never
  offers it.

## Back up and restore

A backup of this deployment is three things, and it is only a backup if it is
all three:

| | Why |
|---|---|
| `postgres.dump` | `pg_dump -Fc`. The source of truth: catalog, sites, identities, proxies, policies, configs, secrets, users, tokens, audit. |
| `kek.key` | Copied at mode `0600`. The live copy is `0644` because a container reads it (see below); the backup copy is read by nothing, and a restore re-installs it with an explicit `install -m 0644` whatever the source mode was. Without it the dump cannot be opened, by you or by anyone. |
| `env` | Its passwords are what the database volumes were built with. |

Valkey is not in there on purpose: the hot state is derived from PostgreSQL and
`spnr rebuild` regenerates it. ClickHouse is not either: it holds analytics that
expire on their own TTL.

Menu entry 10 writes all three into `<install-dir>/backups/<UTC timestamp>/`.
Both `backups/` and the timestamped directory are `0700`, the dump is written
under `umask 077`, and the two copies are placed with explicit modes (`0600`
each) rather than inherited ones — `install -m` ignores the umask, so the mode
has to be said out loud. That is inside the install directory so a
directory-level uninstall takes it with everything else — **copy it off the
host**.

Menu entry 11 restores one: it lists what is there, requires you to type
`restore`, and compares the backup's key against the one the deployment is
using. If they differ it offers to replace the live key — defaulting to **no**
and requiring the word `replace-key`, because that is the one irreversible step
in this script — and keeps the current key as
`kek.key.<UTC timestamp>.previous` rather than under a fixed name, so a second
restore cannot destroy the key the first one saved.

Then it stops the server replicas (they hold open connections to the objects
`pg_restore --clean` is about to drop, and a `DROP` on an in-use object fails),
runs `pg_restore --clean --if-exists`, **rebuilds the hot state**, brings the
replicas back, and runs the health check. That rebuild is mandatory, not
optional: until the epoch key exists every replica reports `hotstate: epoch
missing (rebuild pending)` and the load balancer serves nothing.

It is not a backup until it is off this machine and you have restored it once
into an empty stack. Nothing else proves it works.

## Uninstalling

Menu entry 4, three levels:

1. **Stop the stack, keep every byte of data.** `down --remove-orphans`.
2. **Stop and delete the data volumes.** Every identity, proxy, policy, config,
   secret, user and token in this deployment. The key-encryption key is kept, so
   a dump taken with it can still be restored into a fresh stack.
3. **That, plus remove the install directory** — including `.env` and
   `secrets/kek.key`. Without that key no backup of this deployment can ever be
   opened again, by anyone, so it offers to print the key first so you can copy
   it somewhere.

Levels 2 and 3 require typing `delete`, not a y/n. Level 3 additionally refuses
any path that does not contain `deploy/compose/docker-compose.yml`. Docker
images are left alone either way: this script does not prune a shared host.

## Options

```
--yes, -y   Take the default answer to every question. Publishes on
            127.0.0.1:8080, one administrator called admin, replicas scaled to
            this host, observability off.
--check     Detect the system and print what would happen, then stop. Changes
            nothing, writes nothing, asks nothing.
--manage    Go straight to the menu for an install that already exists.
--help, -h  Everything above, shorter.
```

`--check` is the one to run first on an unfamiliar host. It reports the
distribution, architecture, cores and RAM; whether Docker is present, running
and new enough; whether `git` and `curl` are there, with the package-manager
command for each that is missing; and whether the published image can actually
be fetched from this network.

Note that `--yes` skips the existing-install detection. It is an installer flag,
not a management one; use `--manage` to reach the menu non-interactively-ish, or
point `SPINNERET_INSTALL_DIR` at the install.

## Environment overrides

Each one presets an answer, which is what makes `--yes` a real unattended
install rather than just "all defaults":

| Variable | Default | What it presets |
|---|---|---|
| `SPINNERET_PROJECT` | `spinneret` | The Compose project name, also used to find an existing install. Set it to run a second, completely separate stack on the same host. Letters, digits, dash and underscore only. |
| `SPINNERET_INSTALL_DIR` | `/opt/spinneret` as root, `~/spinneret` otherwise | Where to install, or where to find the install in `--manage`. |
| `SPINNERET_BIND_HOST` | `127.0.0.1` | `127.0.0.1` or `0.0.0.0`. Written to `.env` and read by `compose.host.yml`. |
| `SPINNERET_PORT` | `8080` | The published port. |
| `SPINNERET_ADMIN_USERNAME` | `admin` | The first administrator. |
| `SPINNERET_REPLICAS` | derived from CPU and RAM | Server replicas. |
| `SPINNERET_ENABLE_OBSERVABILITY` | `0` | `1` adds the Prometheus profile. |
| `SPINNERET_USE_PUBLISHED` | `1` | `1` pulls the published image, `0` builds from the checkout. |
| `SPINNERET_IMAGE` | `ghcr.io/tikhub/spinneret` | The image repository — Docker Hub (`tikhub/spinneret`, the same digest), a private mirror or a fork, without editing the script. |
| `SPINNERET_IMAGE_TAG` | `latest` | The image tag. Pin an exact one for a production install. |
| `NO_COLOR` | unset | Set to anything to turn off colour. |

The administrator password and the two database passwords are always generated
on the machine and never taken from the environment.

```bash
SPINNERET_INSTALL_DIR=/srv/spinneret SPINNERET_PORT=9000 \
SPINNERET_REPLICAS=2 SPINNERET_IMAGE_TAG=v0.1.0 \
  bash install.sh --yes
```

## Host requirements

`bash`, `git`, `curl`, `awk`, `base64`, the coreutils the script actually calls
(`install`, `cmp`, `mktemp`, `find`, `head`, `sort`), and either `/dev/urandom`
or `openssl`. Worth spelling out because Alpine ships a minimal userland and
neither bash nor git by default: `apk add bash git curl coreutils`.

Docker Engine with **Compose v2.24 or newer**. That is the first release with
the `!reset` and `!override` merge tags, which the override files this script
writes both use; anything older fails in ways that read like a problem with this
project. The old standalone `docker-compose` (v1) is detected and rejected with
that explanation.

Architecture `x86_64` or `arm64`. Images are published for those two.

| | vCPU | RAM | Disk |
|---|---|---|---|
| Evaluation, ClickHouse disabled | 2 | 2 GiB | 10 GiB |
| The stack as shipped | 2 | **4 GiB** | 20 GiB |
| Recommended | 4 | 8 GiB | 40 GiB SSD |

4 GiB is the floor for the stack as shipped because ClickHouse idles at about
1.2 GiB whatever its caches are set to, and PostgreSQL allocates 512 MiB of
shared buffers at start. Emptying `SPINNERET_CLICKHOUSE_URL` disables raw
request events and the request explorer and takes that 1.2 GiB back.

Building from source additionally needs a builder that can run a `node:22-alpine`
plus `golang:1.27-alpine` multi-stage build, reach the Go and Node package
registries, and spare about 2 GB for build cache.

## What ends up on disk

```
<INSTALL_DIR>/                            # /opt/spinneret or ~/spinneret
├── .git/                                 # shallow clone of TikHub/Spinneret
├── deploy/compose/
│   ├── docker-compose.yml                # from the repository, never edited
│   ├── compose.image.yml                 # written here — the published-image override
│   ├── compose.host.yml                  # written here — bind address, memory ceilings
│   ├── .env                              # written here, mode 0600
│   ├── config/{Caddyfile,clickhouse-*.xml,prometheus.yml}
│   └── secrets/
│       └── kek.key                       # written here, mode 0644 in a 0700 directory
├── backups/<UTC timestamp>/              # written by menu entry 10
│   ├── postgres.dump
│   ├── kek.key
│   └── env
└── spnrctl                               # written here, mode 0755
```

The databases themselves live in Docker's named volumes —
`spinneret_pgdata`, `spinneret_valkeydata`, `spinneret_chdata` — not in this
directory.

**`compose.image.yml`** pins all three services that run the server binary —
`migrate`, `spinneret` and `init-admin` — to the published image and drops their
`build:` section with `!reset null`. All three share one image; overriding only
`spinneret` would leave the other two trying to build. Dropping `build:`
entirely means a `compose build` cannot quietly rebuild over the image that was
pulled. Delete the file to go back to building from source.

**`compose.host.yml`** carries the published bind address, because the shipped
compose file publishes `"${SPINNERET_PORT:-8080}:8080"` with no host part and
there is no bind-address variable. It uses `ports: !override` rather than a plain
merge, which would leave two mappings for the same container port and fail to
bind the second. Edit it freely; delete it to go back to the shipped defaults.

**`spnrctl`** is the single entry point. Everything after the name is passed
straight to `docker compose`, so anything Compose can do, it can do — with this
deployment already selected:

```bash
./spnrctl ps                        # what is running
./spnrctl logs -f spinneret         # follow the server log
./spnrctl restart lb                # restart one service
./spnrctl up -d --wait              # start, wait for healthy
./spnrctl down                      # stop, keep the data
./spnrctl down -v                   # stop and delete the volumes. Irreversible.
```

The administration CLI lives in the image as `/usr/local/bin/spnr` and needs
only PostgreSQL, so run it through the one-shot `migrate` service rather than
through a replica that may not be healthy:

```bash
./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status
./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate \
    token create --name node-1 \
    --scope lease:acquire --scope report:write --scope config:read
```

## The generated secrets

| | What it is | How to back it up |
|---|---|---|
| `PG_PASSWORD` | 32 random alphanumerics. The PostgreSQL password, interpolated raw into `SPINNERET_DATABASE_URL`. Alphanumeric because anything needing percent-encoding would silently produce a wrong password. | In `.env`. Honoured only when the data directory is empty, so changing it later needs an `ALTER ROLE` inside the container. |
| `CLICKHOUSE_PASSWORD` | 32 random alphanumerics, same story. | In `.env`. |
| `SPINNERET_ADMIN_PASSWORD` | 24 random alphanumerics. The first administrator's password. Printed once at the end. | In `.env`, in clear, until you change it — and then only if you let the script update the file. Only the one-shot `init-admin` reads it. |
| `secrets/kek.key` | `k1:<base64 of 32 random bytes>`. The root of the secret vault. | **Off the machine, before you put any data in.** Again after every rotation. There is no recovery path. |

`.env` is mode `0600`, written to a temp file in the same directory and moved
into place so an interrupt cannot leave half a file where a whole one used to be.

`kek.key` is mode `0644` **on purpose**, and that is not a mistake to fix.
Compose bind-mounts that exact file into the container, which runs as the
distroless `nonroot` user (uid 65532) and matches no host user. A `0600` key
file is unreadable there and the server exits with
`vault: read kek file "/run/secrets/kek": … permission denied`. The protection
belongs on the directory, which the script sets to `0700`.

## Troubleshooting

**`Compose is 2.20.x; this stack needs 2.24 or newer.`** Update Docker. The
override files use merge tags that do not exist before 2.24.

**`No terminal to ask questions on.`** You piped the script with no `/dev/tty`
available (a CI runner, a `docker exec` without `-t`). Either download and run
it, or pass `--yes`.

**`Docker is running, but your user cannot reach it.`** The daemon is up and
your user is not in the `docker` group. The script prints the two commands and
deliberately does not run them: group membership is root access on that machine.

**`ghcr.io/tikhub/spinneret:latest cannot be fetched from here.`** Either no such
tag has been published yet, or this host cannot reach the registry. Nothing is
wrong with your checkout — the script falls back to building from source and
says so.

**The stack is up but `/readyz` never turns `ok`.** The last body the script
prints names the broken dependency:

| `checks` says | Meaning | Fix |
|---|---|---|
| `hotstate: building` | First start, or right after a rebuild. | Wait. |
| `hotstate: epoch missing (rebuild pending)` | Valkey was flushed or its volume recreated. | Menu entry 5, or `spnr rebuild`. |
| `postgres: unreachable` | Password does not match the volume, or the pool is exhausted. | `./spnrctl logs --tail 40 postgres`. Keep `replicas × SPINNERET_DATABASE_MAX_CONNS + headroom < 300`. |
| `redis: unreachable` | Valkey is down — often OOM-killed. | Menu entry 7 says whether it was. |
| `catalog: not loaded` | The namespace snapshot has not been read yet. | Wait; if it persists, check PostgreSQL. |

**`503 {"status":"draining"}`.** Expected for up to five seconds per replica
during a restart. Longer than that means whatever is in front is not honouring
`/readyz`.

**`vault: read kek file … permission denied`.** `ls -l
deploy/compose/secrets/kek.key` must show `-rw-r--r--`. See above — do not
"harden" it to `0600`.

**`load dedupe_pepper system key (is the KEK the one used to initialize this
database?)`.** Wrong key for this database. This is the restore-with-the-wrong-key
signature. Nothing but the right key fixes it.

**`vault: kek "k1" must be 32 bytes, got N`.** Truncated or corrupt key file.

**`required variable PG_PASSWORD is missing a value`.** Compose could not read
`.env`. It reads `.env` from the **compose file's directory**, not from your
current one — which is exactly why `spnrctl` exists and why you should use it
instead of raw `docker compose`.

**`bind: address already in use`.** Something else has the port.
`ss -ltnp 'sport = :8080'` or `lsof -iTCP:8080 -sTCP:LISTEN` names it; change
`SPINNERET_PORT` in `.env` and `./spnrctl up -d`.

**ClickHouse keeps restarting.** Usually the OOM killer on a small host; menu
entry 7 reports it, and `docker inspect --format '{{.State.OOMKilled}}'`
confirms it. Note that if `SPINNERET_CLICKHOUSE_URL` is set, ClickHouse is a
hard startup dependency — the server errors out rather than degrading. Raise the
RAM, or empty that variable to run without analytics.

**ClickHouse will not start and the log mentions `nofile`.** The container asks
for 262144 file descriptors soft and hard. Check `ulimit -Hn` on the host.

**Valkey grows until the host swaps or the OOM killer arrives.** No `maxmemory`
is set and the policy is `noeviction`, deliberately: losing hot-state keys would
silently change scheduling decisions. The dominant term is dedup markers and
pinned ended-lease hashes over `SPINNERET_REPORT_DEDUP_TTL` (default `1h`).
Lower it to the actual retry window of your nodes, and leave the host headroom
for the AOF-rewrite fork.

**Nodes get `429 no_proxy_available`.** The most common cause on a real host is
`SPINNERET_PROXY_CHECK_URL`: it defaults to a URL on the public internet, and a
host that cannot reach it marks every proxy dead. Point it at something
reachable.

**The source build fails.** Build the one service on its own so the error is not
buried in a progress stream: `./spnrctl build spinneret`.

**Disk fills.** `chdata` grows fastest and expires on a 90-day TTL. Menu entry
12 lists images belonging to this Compose project that no container references —
running or stopped — and offers to remove them, defaulting to no. It can also
clear build cache, but BuildKit's cache is **per host, not per project**: its
records carry no project label, so clearing it makes the next build of anything
on that machine a cold one. That option defaults to no and says as much where it
is offered. `docker system df -v` shows where the space went.

More in [Troubleshooting](../documents/en/18-troubleshooting.md).

---

# 一键部署

两个脚本，行为相同，语言不同。两者都**只支持 Docker** —— 不用 Docker 的部署方式
是一篇文档能讲清的事，不是一个脚本：[安装与部署](../documents/zh/02-installation.md)。

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.zh.sh -o install.zh.sh
less install.zh.sh       # 两千两百行，每个决定都有注释
bash install.zh.sh
```

推荐这个顺序，而且这不是走过场。任何你管道进 shell 的东西都以你的身份运行，而在装
Docker 那一步，它还会问你要不要以 root 跑一个脚本。管道运行时提问依然正常 ——
脚本从你的终端读答案，而不是从标准输入读，因为 `curl | bash` 的时候标准输入就是脚本
本身。完全无人值守请显式加 `--yes`。

[`install.zh.sh`](./install.zh.sh) 与 [`install.sh`](./install.sh)
结构完全一致，只有提示语言不同：同样的选项（`--yes` / `--check` / `--manage` /
`--help`）、同样的环境变量、同样的管理菜单、同样的安全边界（只往你指定的目录里写
东西，只在三件事上用 `sudo` 且每次都先打印命令，不问过你就不装任何东西）。

有一件事和大多数部署不同，值得单独说：`deploy/compose/secrets/kek.key`
是整个密钥库的根。这套部署里存下的每一份凭据、每一个代理地址、每一条 vault
密钥都用它加密，**丢了就没有任何恢复途径**。脚本只生成一次，绝不覆盖已有的那一份，
并且在告诉你任何别的事情之前，先让你把它备份出去。备份时请和 `.env` 一起 ——
后者里的口令是数据卷当初建立时用的那一套。

完整说明见中文文档：

- [快速开始](../documents/zh/01-quickstart.md)
- [安装与部署](../documents/zh/02-installation.md)
- [故障排查](../documents/zh/18-troubleshooting.md)
- [安全加固](../documents/zh/19-security.md)
