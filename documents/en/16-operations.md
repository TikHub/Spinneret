# Operations runbook

**Day 2. Sizing the three databases, backing them up and proving the backup works, upgrading, scaling,
rebuilding the hot state, rotating the key everything depends on, controlling disk growth — and a numbered
playbook for each failure you will actually be paged for.**

[中文](../zh/16-operations.md)

---

## Contents

- [The shape of a healthy deployment](#the-shape-of-a-healthy-deployment)
- [The control script and the CLI](#the-control-script-and-the-cli)
- [Capacity planning](#capacity-planning)
- [Backups](#backups)
- [Restoring](#restoring)
- [Upgrades and migrations](#upgrades-and-migrations)
- [Scaling](#scaling)
- [Changing the shard count](#changing-the-shard-count)
- [Rebuilding the hot state](#rebuilding-the-hot-state)
- [Key rotation](#key-rotation)
- [Data retention and reclaiming disk](#data-retention-and-reclaiming-disk)
- [Accounts, passwords and tokens](#accounts-passwords-and-tokens)
- [Routine checks](#routine-checks)
- [Incident playbooks](#incident-playbooks)
- [Page or ticket](#page-or-ticket)
- [Next](#next)

---

## The shape of a healthy deployment

Two endpoints and four numbers tell you almost everything.

```bash
curl -s 127.0.0.1:8080/healthz   # {"status":"ok"} — the process is alive
curl -s 127.0.0.1:8080/readyz    # every dependency, by name
```

```json
{"checks":{"catalog":"ok","hotstate":"ok","postgres":"ok","redis":"ok"},"status":"ok"}
```

`/readyz` is what a load balancer probes; `/healthz` only says the process exists. A replica that is shutting
down answers `503 {"status":"draining"}` for `min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4)` *before* it closes its
listener — expected during every restart, and a sign of a misconfigured proxy if it lasts longer.

The four numbers, from `/metrics`:

| Metric | What it tells you | Normal |
| --- | --- | --- |
| `spinneret_stream_pending{shard}` | Reports waiting to be applied | Near zero, spiky. Steady growth means the workers are behind |
| `spinneret_report_lag_seconds` | How stale the state the scheduler decides on is | p99 tens of milliseconds |
| `spinneret_stream_owned_shards` | Shards this instance consumes | Sums across instances to `SPINNERET_REPORT_SHARDS` |
| `spinneret_acquire_peers` | How many live API instances each instance sees | Equal to your API-role replica count |

What each metric means, and the full list, is in [Observability and alerting](./12-observability.md). This page
uses them; it does not redefine them.

---

## The control script and the CLI

An install made by `install/install.sh` has `./spnrctl` in its directory. It is `docker compose` with the four
things that must be right every time already set: the project name, the override files in order, the working
directory that makes `./config`, `./secrets` and the build context resolve, and `COMPOSE_ENV_FILES`.

```bash
./spnrctl ps
./spnrctl logs -f spinneret
./spnrctl restart lb
./spnrctl up -d --wait
./spnrctl down              # stop, keep the data
./spnrctl down -v           # stop and delete the volumes. Irreversible.
```

The administration CLI lives in the image as `/usr/local/bin/spnr`. Run it through the one-shot `migrate`
service rather than through a replica: that service already carries the full environment and the key, has no
server entrypoint in the way, and depends only on PostgreSQL — so it works when no replica is healthy.

Every `spnr …` command on this page means that. Paste this once per shell session and the rest of the page is
copy-pasteable:

```bash
spnr() { ./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate "$@"; }

spnr migrate status
spnr kek status
spnr rebuild --help
```

Other ways to reach the CLI — inside a running replica, or on the host against your own URLs — are in
[CLI reference → Running spnr against a deployment](./15-cli.md#running-spnr-against-a-deployment).

`bash install.sh --manage` reopens the installer as a menu. Its top level is **1** status, **2** upgrade (or
move to another published tag, including an older one), **3** *Manage*, **4** stop or remove. Everything on
this page lives under **Manage**:

| Manage | | Manage | |
| --- | --- | --- | --- |
| 1 | Change an administrator password | 7 | Health check |
| 2 | Add an administrator | 8 | Logs |
| 3 | List accounts | 9 | Restart services |
| 4 | Create an API token | 10 | Back up now |
| 5 | Rebuild the hot state | 11 | Restore a backup |
| 6 | Show the configuration | 12 | Free disk space |

Every entry delegates to one of the commands below, so nothing in the menu can drift away from what it stands
for. This page names them as *Manage → 10* and so on.

---

## Capacity planning

Size the three stores separately. They grow on different terms, and only one of them grows with request rate
in a way that will surprise you.

### Redis / Valkey — sized by traffic, not by data

This is the one to get right. The dataset is small and predictable; the *traffic-proportional* state is
several times larger. Measured with `MEMORY USAGE` on a live 100,000-identity dataset:

| Structure | Per unit |
| --- | --- |
| Identity hash | **201 B** average, 296 B maximum, per identity |
| Ready queue member | **64.6 B** per (identity × endpoint group) — about 62 MiB per million |
| Health state entry | **≈ 71 B** per *warmed* (identity × endpoint group) |
| Report dedup marker | **80 B** per report, for `SPINNERET_REPORT_DEDUP_TTL` |
| Ended lease hash | **≈ 290 B** per lease, kept `max(SPINNERET_LATE_REPORT_WINDOW, SPINNERET_REPORT_DEDUP_TTL)` after it ends |
| Report stream entry | **≈ 440 B** per entry in the backlog |

```text
Redis working set ≈ identities                    × 201 B
                  + identities × endpoint_groups  × 64.6 B    (ready queues)
                  + warm(identity × group)        × 71 B      (health state, → all pairs eventually)
                  + reports/s  × dedup_TTL        × 80 B      (dedup markers)
                  + acquires/s × max(late, dedup) × 290 B     (ended lease hashes)
                  + backlog_entries               × 440 B     (report streams)
```

Three things follow from that shape:

1. **`SPINNERET_REPORT_DEDUP_TTL` is the single biggest memory lever.** Its default is `1h`, the minimum
   accepted value is `1m`, and nodes retry within seconds, not hours. 5–15 minutes is usually enough and cuts
   both traffic terms by 4–12×.
2. **Lowering it below `SPINNERET_LATE_REPORT_WINDOW` (default `10m`) stops helping the lease term**, because
   the ended lease hash is kept for the larger of the two. Lower both together, or stop at the late window.
3. **Endpoint groups multiply per-identity state.** Each (identity × group) pair costs ~65 B of ready queue
   plus ~71 B of health. Define groups by the policy they need, not by how many URLs you have.

Valkey runs with `--maxmemory-policy noeviction`: this state must never be evicted behind the server's back.
**Never enable eviction.** Plan roughly **twice** the computed working set: the AOF rewrite forks the process,
and a Redis that reaches its ceiling fails writes loudly rather than degrading.

### PostgreSQL — sized by catalog size and by retention

The hot path never touches PostgreSQL; it sees batched writes and stayed under 3 % CPU in every load scenario.
Its disk is what needs planning, and it is driven by retention, not by request rate:

| What grows | Rows | Bounded by |
| --- | --- | --- |
| `identities`, `identity_payloads`, `proxies`, `accounts` | One per object (payloads: the last **5** versions per identity) | Your catalog |
| `outcome_stats_minutely`, `acquire_stats_minutely` | One per (minute × site × endpoint group × proxy × outcome/result) | `SPINNERET_RETENTION_MINUTE_STATS`, 720 h |
| `node_stats_minutely` | One per (minute × namespace × node) | `SPINNERET_RETENTION_MINUTE_STATS` |
| `payload_access_minutely` | One per (minute × namespace × token × identity type) | `SPINNERET_RETENTION_MINUTE_STATS` |
| `identity_stats_hourly` | One per (hour × identity × endpoint group × outcome) — **the dominant term** | `SPINNERET_RETENTION_HOUR_STATS`, 4320 h |
| `risk_events` | One per risk event | `SPINNERET_RETENTION_RISK_EVENTS`, 720 h |
| `state_events` | One per identity/proxy state transition | `SPINNERET_RETENTION_STATE_EVENTS`, 8760 h |
| `audit_logs` | One per administrative action and per secret read | `SPINNERET_RETENTION_AUDIT`, 8760 h |
| `alert_events` | One per fired alert | A fixed **90 days**, not configurable |

`identity_stats_hourly` is the term to compute, because it is the only aggregate whose cardinality includes
your identity count:

```text
rows/hour = distinct (identity, endpoint group, outcome) combinations that saw traffic in that hour
          ≤ min( requests/hour , identities × endpoint_groups × distinct_outcomes )
rows/day  = rows/hour × 24
```

Its retention is 180 days by default and it is partitioned **monthly**, so six or seven partitions of that
size are on disk at all times. Every other aggregate is bounded by site, endpoint group, proxy, node, token
and outcome cardinality — not by your identity count — and is usually small enough to ignore.

Connections are the other PostgreSQL limit, and it is a hard one: keep
`replicas × SPINNERET_DATABASE_MAX_CONNS + headroom < max_connections`. The Compose stack sets
`max_connections=300` and `SPINNERET_DATABASE_MAX_CONNS` defaults to `32` (minimum `2`). Leave headroom for
`spnr`, for `psql`, and for the migration job's own lock connection.

### ClickHouse — sized by request rate and TTL

ClickHouse holds one row per report in `report_events` and one row per lease lifecycle event in
`lease_events`, both `MergeTree`, both partitioned by `toYYYYMMDD(event_time)` and both carrying
`TTL toDateTime(event_time) + INTERVAL <SPINNERET_CLICKHOUSE_TTL_DAYS> DAY` (default `90`, range 1–3650).

Rows are cheap — every low-cardinality column is `LowCardinality(String)` and `event_time` is
`CODEC(DoubleDelta, ZSTD(1))` — but the row *count* is your full request volume times the retention, so this
is the store that grows linearly with traffic. Measure it on your own data rather than trusting a per-row
estimate:

```bash
# spnrctl hands .env to Compose; it does not export it into your shell. Set the password
# once per session — every clickhouse-client command on this page needs it.
CLICKHOUSE_PASSWORD=$(grep -m1 '^CLICKHOUSE_PASSWORD=' deploy/compose/.env | cut -d= -f2-)

./spnrctl exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" -q "
  SELECT table, formatReadableSize(sum(bytes_on_disk)) AS disk, sum(rows) AS rows
  FROM system.parts WHERE active AND database = 'spinneret' GROUP BY table"
```

Divide disk by rows once, then multiply by `rate × 86400 × TTL_days`. Memory is a separate, sharper problem:
`deploy/compose/config/clickhouse-limits.xml` caps the caches, and `max_server_memory_usage` must stay
**above** the image's idle resident size (~1.2 GiB) — see the ClickHouse playbook below.

ClickHouse is optional. Without `SPINNERET_CLICKHOUSE_URL` the control plane works unchanged and only the
request explorer goes dark.

### Server instances

One instance sustains, with one Valkey behind it: **4,500 acquire→report cycles/s** with exclusive leases,
**5,500/s** with `max_concurrent_leases: 4`, about **20,000 reports/s** applied inside the 200 ms lag target,
and **10,000** concurrent `WatchConfig` long polls for 0.05 cores and ~504 MiB. At those rates the instance
itself uses 1.4–1.6 of the 16 cores of the measurement VM, so the v0.1 sizing target of 4 vCPU / 8 GiB per
instance is enough. All of it is measured in [Performance and tuning](./17-performance.md).

Plan offered load at about **80 % of the knee**. Past the knee this system does not degrade, it collapses —
see [Performance → Past the knee](./17-performance.md#past-the-knee-congestion-collapse).

### A worked example

One site, **200,000 identities**, **20 endpoint groups**, a steady **2,000 acquire→report cycles/s**, 90 days
of request history.

**Redis, with the defaults (`dedup_TTL = 1h`):**

| Term | Calculation | Size |
| --- | --- | ---: |
| Identity hashes | 200,000 × 201 B | 38 MiB |
| Ready queues | 200,000 × 20 × 64.6 B | 246 MiB |
| Health state, fully warm | 4,000,000 × 71 B | 271 MiB |
| Dedup markers | 2,000/s × 3,600 s × 80 B | 549 MiB |
| Ended lease hashes | 2,000/s × 3,600 s × 290 B | **1.94 GiB** |
| **Total working set** | | **≈ 3.0 GiB** |

Provision **6 GiB** of Redis. Now set `SPINNERET_REPORT_DEDUP_TTL=10m` (equal to the late-report window, so
both traffic terms shrink):

| Term | Calculation | Size |
| --- | --- | ---: |
| Dataset + health | as above | 555 MiB |
| Dedup markers | 2,000/s × 600 s × 80 B | 92 MiB |
| Ended lease hashes | 2,000/s × 600 s × 290 B | 332 MiB |
| **Total working set** | | **≈ 0.95 GiB** |

**2 GiB** of Redis now carries the same load. That one variable is the difference between a 6 GiB instance and
a 2 GiB one.

**PostgreSQL:** the catalog is ~200,000 identity rows plus up to 1,000,000 payload version rows. The
aggregates are bounded by cardinality: 20 groups × a handful of outcomes per minute is negligible, and
`identity_stats_hourly` is capped by traffic at 2,000/s × 3,600 = 7.2 M requests/hour spread over at most
200,000 × 20 × outcomes combinations — so in practice a few hundred thousand rows per hour, 180 days deep.
Start at **100 GiB** and watch the six monthly partitions of that one table.

**ClickHouse:** 2,000 reports/s × 86,400 × 90 days = **15.6 billion rows** in `report_events`, plus the lease
events. Measure your bytes-per-row before committing disk; this is the store that decides whether you keep 90
days or 14. Lowering `SPINNERET_CLICKHOUSE_TTL_DAYS` is the lever.

**Server:** 2,000 cycles/s is 44 % of one instance's exclusive-lease knee. Run **two** instances for
availability, not for throughput, and keep `SPINNERET_ACQUIRE_FLEET_INFLIGHT` at its default.

---

## Backups

A backup of this deployment is **three** things, and it is only a backup if it is all three.

| Artefact | Why |
| --- | --- |
| `postgres.dump` | `pg_dump -Fc`. The source of truth: catalog, sites, identities, proxies, policies, configs, secrets, users, tokens, audit |
| `secrets/kek.key` | Without it, every encrypted field in that dump is unrecoverable |
| `.env` | Its passwords are what the database volumes were built with |

What you lose if you have only some of them:

| You have | You can restore | You cannot |
| --- | --- | --- |
| Dump + KEK + `.env` | Everything | — |
| Dump + KEK | Everything, into a fresh stack with new passwords | Reattach the old data volumes |
| Dump, no KEK | Every path, name, description, tag, policy, user and permission | **Any identity payload, proxy URL, secret value or notification credential.** Permanently. The server will not even start |
| KEK + `.env`, no dump | Nothing | — |

Valkey is deliberately not on the list: the hot state is derived from PostgreSQL and `spnr rebuild`
regenerates it. ClickHouse is not either: it holds request-level history that expires on its own TTL. If that
history matters to you, back it up separately (`clickhouse-backup`, or a filesystem snapshot of the `chdata`
volume with the server stopped) and treat its loss as losing the request explorer, nothing else.

### The commands

Only what the images provide — no extra tooling:

```bash
umask 077
./spnrctl exec -T postgres pg_dump -U spinneret -Fc spinneret > postgres.dump
cp deploy/compose/secrets/kek.key  ./kek.key
cp deploy/compose/.env             ./env
```

*Manage → 10* does all three into `<install-dir>/backups/<UTC timestamp>/` — the dump written under
`umask 077`, both copies installed at `0600`, and `.env` saved as `env` so it is not hidden from a listing. The
*directory* is created under the ambient umask, normally `0755`, so tighten the tree yourself once:
`chmod 0700 <install-dir>/backups`. That path is *inside* the install directory, so a directory-level uninstall
takes it with everything else: **copy it off the host.**

Verify the dump and the key before you trust either:

```bash
# A pg_dump -Fc archive starts with the magic string PGDMP. A truncated dump does not.
head -c 5 postgres.dump   # PGDMP

# Every key line must decode to exactly 32 bytes. Check the first line: after a rotation
# the file holds several `id:base64` lines, and base64 -d would decode all of them at once.
head -1 kek.key | cut -d: -f2 | base64 -d | wc -c   # 32
```

Neither check proves the dump restores. Only a restore does, which is the next section.

### Where to keep them, and for how long

Store the key **separately** from the dumps — a secret manager, an offline password store, a sealed envelope.
Losing both at once is the only unrecoverable failure in this system.

Keep every KEK at least as long as the oldest backup taken under it. A KEK removed from the configuration
while an archived dump still needs it makes that dump unopenable, by anyone, permanently. This is why the
installer saves a replaced key as `kek.key.<UTC timestamp>.previous` rather than under a fixed name: a second
restore must not destroy the key the first one set aside.

**It is not a backup until it is off the machine and you have restored it once into an empty stack.** Nothing
else proves it works.

---

## Restoring

### The drill

Run this on purpose, on a spare host, before you need it.

```bash
# 1. A fresh checkout, then the backup's own .env and key. Compose reads .env from the
#    Compose file's own directory, so run everything from there.
git clone https://github.com/TikHub/Spinneret.git /srv/spinneret-drill
cd /srv/spinneret-drill/deploy/compose
cp /backups/20260918T031500Z/env      .env
mkdir -p secrets && cp /backups/20260918T031500Z/kek.key secrets/kek.key
chmod 0644 secrets/kek.key            # the container runs as a non-root user

# 2. Bring up the stores, build the image, apply the schema.
docker compose up -d --wait postgres valkey clickhouse
docker compose build migrate
docker compose run --rm migrate

# 3. Stop anything serving, then restore. --clean drops what it recreates, and a
#    DROP against a live connection fails — leaving a half-restored schema.
docker compose stop spinneret
docker compose exec -T postgres pg_restore -U spinneret -d spinneret --clean --if-exists \
  < /backups/20260918T031500Z/postgres.dump

# 4. Mandatory: the hot state describes the database that was there a moment ago.
docker compose run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild

# 5. Bring it up and check every dependency by name.
docker compose up -d --wait
curl -s 127.0.0.1:8080/readyz
```

Then prove it, because a stack that starts is not yet a stack that decrypted anything: sign in to the console,
open **Identities** and confirm the count, open one identity detail and confirm its payload fields are listed
rather than masked as undecryptable, open **Secrets** and reveal one value, and confirm **Audit** shows that
reveal. A restore that signs in but cannot decrypt is a restore with the wrong key.

Five things to know:

1. **Stop the replicas first.** `--clean` drops the objects it is about to recreate; a `DROP` on an object a
   live connection is using fails, which leaves a partly restored schema rather than a clean error.
2. **The rebuild is not optional.** Until it runs, every replica reports
   `hotstate: epoch missing (rebuild pending)` on `/readyz` and the load balancer serves nothing.
3. **Expect `pg_restore` to be noisy.** `--clean --if-exists` comments on objects that were not there. Read
   the output; a clean exit is not the same as a silent one.
4. **Leases did not survive.** Nodes holding pre-restore leases get `lease_unknown` on `Renew`, `Release` and
   `Report`, and should simply acquire again.
5. **Restored sites are cold.** Every identity comes back with the same ready-queue score in every endpoint
   group, spread over the following 60 s, and traffic is what decorrelates them. Expect a stretch of elevated
   `exhausted` — [Performance → warm-up](./17-performance.md#a-freshly-seeded-or-rebuilt-site-must-be-warmed).

*Manage → 11* does steps 3–5 against a backup directory under `<install-dir>/backups/`, requires the word
`restore`, and rolls the replicas one at a time at the end.

### Restoring against the wrong KEK

This is the failure you will see if you get it wrong, and it is worth recognising on sight. The server starts,
connects, and then exits:

```text
load dedupe_pepper system key (is the KEK the one used to initialize this database?)
```

Nothing but the right key fixes it. Not a rebuild, not a re-restore, not a migration. The system key that
message names is wrapped with the KEK that was current when the database was initialised, so a dump restored
under a different key cannot unwrap it — and every identity payload, proxy URL and secret version in that dump
is in the same position.

If the backup carries its own `kek.key` and it differs from the live one, *Manage → 11* says so, offers to
replace the live key behind a second confirmation that defaults to **no**, and saves the outgoing key as
`kek.key.<UTC timestamp>.previous` first. Replacing the live key is the one irreversible step in that script:
whatever was encrypted under the outgoing key stays encrypted under it, and only that timestamped copy can
open it again.

The safe alternative when you are unsure: restore into a **separate** stack with the backup's own key and
`.env`, confirm it opens, and only then decide what to do with the live one.

---

## Upgrades and migrations

**Finding out there is one.** **Settings → System** in the console shows the build you are running and,
when you press the button, the latest published release with a link to its notes. It asks only when
asked — nothing polls — and `SPINNERET_UPDATE_CHECK_URL=""` switches it off on a deployment that must
make no outbound connection; watch [Releases](https://github.com/TikHub/Spinneret/releases) instead.
`spnr version` reports the same string from the command line, and so does the login reply the console
seeds itself from.

Migrations are embedded in the binary and applied by `spnr migrate up`, serialized by a session-level
PostgreSQL advisory lock held on a dedicated connection — so several instances starting at once is safe, and a
single-connection pool does not deadlock against it. The Compose stack runs them in the one-shot `migrate`
service that `spinneret` depends on.

### The safe sequence

```bash
# 0. Back up first: Manage entry 10, or the commands under Backups.
spnr migrate status
# database schema version 6 (up to date)

# Published-image path
./spnrctl pull && ./spnrctl run --rm migrate && ./spnrctl up -d --wait

# Source path
git pull && ./spnrctl build && ./spnrctl run --rm migrate && ./spnrctl up -d --wait

spnr migrate status
./spnrctl ps
curl -s 127.0.0.1:8080/readyz
```

Pull or build **first**, migrate as its own step, then replace the replicas. The installer's top-level entry
**2** does exactly that, and pulls *before* it pins the new tag, so a failed pull leaves the running deployment
untouched rather than pointing at an image that does not exist.

### Verify before and after

| Before | After |
| --- | --- |
| `spnr migrate status` — note the version | `spnr migrate status` — up to date, and the number moved as expected |
| `spnr config check` — the configuration is still valid for the new binary | `./spnrctl ps` — every `spinneret` replica reaches `healthy`, not just `running` |
| A backup exists, off the host | `/readyz` on each replica returns `status: ok` with all four checks |
| `sum(spinneret_stream_pending)` is near zero | `spinneret_stream_pending` drains back down; `spinneret_stream_owned_shards` sums to `SPINNERET_REPORT_SHARDS` again |
| | `spinneret_acquire_peers` equals the API replica count again (immediately after a graceful stop; within ~60 s after a crash) |

### Rolling restarts

Compose recreates every replica of a scaled service together, which with two replicas is a five-to-ten-second
window where nothing new starts. The load balancer's retries cover it, but they do not have to be spent:
removing one container and running `up -d --no-recreate --no-deps spinneret` fills the empty slot from the new
spec and leaves the others alone.

```bash
for id in $(./spnrctl ps -q spinneret); do
  docker rm -f "$id"
  ./spnrctl up -d --no-recreate --wait --no-deps spinneret
done
./spnrctl up -d --wait
```

That is what the installer's upgrade does, one replica at a time, falling back to a plain recreate the moment
anything about it does not work. Give the orchestrator a stop grace period longer than
`SPINNERET_SHUTDOWN_TIMEOUT`; the Compose file sets `stop_grace_period: 40s` against the 30 s default.

### Rolling back

| Situation | What to do |
| --- | --- |
| The schema did not change | Move the image tag back and roll the replicas. Fast and safe — the installer's top-level entry 2 takes an older tag for exactly this |
| The schema changed and the new version is broken | Restore the backup. This is the safe path |
| Anything | **Never `spnr migrate down`.** A down migration drops tables and the data in them |

An old replica running next to new ones logs `database schema is newer than this binary`. That is expected
during a rolling upgrade and resolves when the last old replica is replaced; a *new* binary against an old
schema refuses to start instead.

### Migration rules

- Migrations are forward-only in practice. `down` exists, drops data, and is not a rollback strategy.
- The schema version is one number, applied atomically per migration under the advisory lock.
- **Never change `SPINNERET_REPORT_SHARDS` across an upgrade** — see
  [Changing the shard count](#changing-the-shard-count).
- Nodes do not have to be upgraded in lockstep: the node API and the SDKs ignore unknown JSON fields in both
  directions, and SDK clients retry `unavailable`, which is what an in-flight call to a stopping replica looks
  like.

---

## Scaling

The *mechanisms* — what is safe to scale, the role table, how shards are divided and how the acquire budget is
divided — are in [Installation → Scaling out](./02-installation.md#scaling-out). This section is the operator's
side of it: the commands, what to watch while a change converges, and how to take an instance out.

### Adding and removing replicas

Instances are stateless. Everything that must be coordinated is coordinated through Redis and PostgreSQL:
report shard ownership (Redis locks), leader election for jobs (PostgreSQL advisory locks), and catalog/config
invalidation (Redis pub/sub). So scaling is one variable:

```bash
SPINNERET_REPLICAS=4 ./spnrctl up -d --wait
```

Four things to get right around it:

- **Export `SPINNERET_REPLICAS` for every Compose call in the session**, or the next one scales the service
  back to the `.env` value. Write it into `.env` to make it permanent.
- **Raise `SPINNERET_DATABASE_MAX_CONNS` only as far as** `replicas × max_conns + headroom < max_connections`
  allows. The Compose stack is 300 and 32, so four replicas already use 128 of 300.
- **Every instance needs a distinct `SPINNERET_INSTANCE_ID`.** The default is the hostname plus a random
  suffix, so it is distinct everywhere. Only an id you pin yourself can collide — and two instances sharing one
  id collapse into a single registry member, each admitting the whole fleet acquire budget.
- **Removing replicas is the same command with a smaller number.** Shard ownership and the acquire budget both
  re-converge on their own, within about 10 seconds. Nothing has to be drained first, though draining is
  tidier — see below.

Scaling out adds server capacity: long-poll capacity, report processing, availability. It does **not** add
Redis capacity, and acquire throughput is bounded by Redis. Budget roughly one Redis primary per 4,500
acquire→report cycles/s with exclusive leases, or 5,500/s with `max_concurrent_leases: 4`, and use
`SPINNERET_REDIS_ADDRS` for Cluster mode beyond that.

**Those are per-instance ceilings, and they do not multiply by replica count.** Every published throughput
figure predates the acquire admission-control gate, and measured without it, two replicas behind the load
balancer reached **3,000 cycles/s in aggregate** — *less* than one instance reaches alone. That is the
bottleneck the gate was built to fix, and no gated figure is published yet. Plan against the per-instance
number, verify your own fleet before you assume it adds up, and read
[Performance → Admission control](./17-performance.md#admission-control) first.

### The api/worker split

`SPINNERET_ROLE` is `all` (default), `api` or `worker`. Two operational rules on top of the table in
[Installation](./02-installation.md#the-apiworker-role-split):

- Keep **at least two** worker-capable instances, so one failure does not stall the report pipeline — and
  never zero. With no worker, reports are accepted and queued but never applied:
  `sum(spinneret_stream_owned_shards)` falls to zero and `spinneret_stream_pending` grows without bound.
- A `worker` instance serves `/healthz`, `/readyz` and `/metrics` and **nothing else** — every other path is a
  404. It therefore passes a readiness probe while being useless to a node, so make sure your load balancer's
  upstream list names only the `api`-role instances.

### What to watch while a scaling change converges

Both registries beat every **2 s**, but they treat a missed beat differently, because they are
protecting different things. The **worker** registry treats a member as live for **10 s**: a worker
that stops beating should lose its shards quickly, and it safely can, because shard ownership is a
lock with its own TTL. The **acquire** registry treats a member as live for **60 s**: a beat needs
Redis, and the moment admission control matters is the moment Redis is saturated — so a short window
there lets a busy instance be pruned by its peers, who then divide the fleet budget by a smaller
number and *widen* the gate exactly when it should hold. A graceful shutdown deregisters the
instance immediately, so the long window only delays noticing a crash, and a crashed instance's
stale membership makes the survivors narrower, which is the safe direction.

So a scale-up converges in about ten seconds for shards and up to a minute for the acquire budget;
a graceful scale-down converges immediately for both. Watch these five series:

| Metric | What it should do |
| --- | --- |
| `spinneret_stream_owned_shards`, per instance | Settle at the target share, `ceil(SPINNERET_REPORT_SHARDS / live_workers)` |
| `sum(spinneret_stream_owned_shards)` | Return to exactly `SPINNERET_REPORT_SHARDS`. **Below it, reports are not being applied** |
| `spinneret_stream_pending{shard}` | A brief spike on adopted shards, then drain. The new owner first finishes what the old one claimed and never acknowledged — that spike is the mechanism working |
| `spinneret_acquire_peers` | Reach the real API instance count. Then `spinneret_acquire_inflight_limit` settles at `budget / peers`, clamped to `[4, 4096]` |
| `spinneret_acquire_peer_beat_age_seconds` | Stay at a few seconds |

A new instance briefly admits more than its fair share, because it starts by assuming it is alone and narrows
as it discovers peers. That is deliberate and harmless. A **frozen** peer count is not: with a stale count of
1 on a twenty-replica fleet, twenty instances each admit the whole budget at Redis. That is what
`spinneret_acquire_peer_beat_age_seconds` and `spinneret_acquire_peer_beat_failures_total` are for, and why the
limit change is logged once per event with the old and new peer counts — the one way to correlate "my
instance's acquire limit changed by itself" with a burst of sheds.

### Draining an instance for maintenance

A signalled shutdown drains in order, bounded by `SPINNERET_SHUTDOWN_TIMEOUT` (default `30s`):

1. `/readyz` flips to `503 {"status":"draining"}` while `/healthz` keeps answering `200`, so a liveness probe
   does not kill the process mid-drain.
2. Keep-alives stop; every response during the drain carries `Connection: close`.
3. It waits `min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4)` for load balancers to notice the failing readiness check.
4. In-flight requests finish; long polls and event streams are cancelled so they do not hold the window open.
5. Background loops stop tier by tier, so writers flush what the services produced.

```bash
./spnrctl stop --timeout 40 spinneret     # drain the whole service
docker stop --timeout 40 <container-id>   # drain one replica
```

Give the orchestrator a stop grace period **longer** than `SPINNERET_SHUTDOWN_TIMEOUT`, or it kills the process
in the middle of step 4; the Compose file sets `stop_grace_period: 40s` against the 30 s default. Shard locks
the instance still held expire within 10 s and the survivors claim them. A **second** signal terminates
immediately, so a shutdown that hangs can always be cut short.

---

## Changing the shard count

Lease ids encode the shard their reports must go to, so changing `SPINNERET_REPORT_SHARDS` **invalidates
in-flight leases**. It is a maintenance window, not a rolling change.

1. Stop node traffic — or accept that leases issued before the change report into a shard nobody owns.
2. Wait for the longest `lease_ttl` in your rotation policies so every outstanding lease has expired, and let
   the streams drain: `sum(spinneret_stream_pending)` at zero.
3. Change the value and restart **every** instance at once. Do not run a mixed fleet.

**Lowering it is worse than raising it.** A report whose lease id encodes a shard `>=` the new count is
rejected as `lease_unknown`, and those leases are stranded until they are reaped. Raising the count re-routes
new leases without rejecting in-flight ones — but treat the value as fixed for the life of a deployment either
way. The valid range is 1–255; the default 16 is right up to a few dozen worker instances.

---

## Rebuilding the hot state

Redis holds derived state: ready queues, availability, health scores, breaker windows, leases. If the volume
is recreated, flushed, or you have reason to believe it disagrees with PostgreSQL, rebuild — do not restore
Redis.

```bash
spnr rebuild                                                      # every site; deletes the epoch first
spnr rebuild --tenant default --namespace default --site example-site   # one site, no fleet-wide window
```

### What `spnr rebuild` does

Under the PostgreSQL advisory lock `spinneret:hotstate:rebuild`, so only one runs at a time, per site:

1. writes the site meta hash;
2. restores health state that is missing in Redis from the `hot_state_snapshots` table, which the leader's
   `hotstate_snapshot` job writes every **60 s** — so a rebuild is *not* a reset of what the system has
   learned, only of what happened in the last minute;
3. re-materializes proxies and identities authoritatively, with cold-start protection: ready scores that would
   land in the past are spread uniformly over the next 60 s;
4. prunes stale queue members and removes the hot state of sites whose rows no longer exist;
5. finally writes a new epoch key.

A fleet-wide rebuild **deletes the epoch key first**, so every replica answers `/readyz` with
`hotstate: epoch missing (rebuild pending)` and nodes get `503 rebuilding` until it finishes. That is a
deliberate outage. Naming one site with `--site` avoids it entirely by re-materializing that site alone, which
is the form to reach for during business hours.

### How long it takes

It is dominated by the number of identities times endpoint groups written into Redis, plus a paged read of
`hot_state_snapshots` at 5,000 rows a page.

One measured point, on the laptop-class VM of [Performance](./17-performance.md#the-environment): a site of
100,000 identities across 50 endpoint groups is 4.9 million `hot_state_snapshots` rows — 980 pages — and the
automatic startup rebuild after the Redis volume was discarded took **24.5 minutes** (the `duration`
field of the `hot-state rebuild finished` log line), with `/readyz` reporting `hotstate: building`
throughout and both replicas doing the work concurrently. Two things about that number are worth knowing before you plan a
maintenance window:

- It scales with identities × endpoint groups, not with traffic. Halve the endpoint groups and you halve
  the rebuild.
- **Every API replica rebuilds independently.** The work is idempotent, so the result is correct, but two
  replicas do the same paged read twice and contend for the same PostgreSQL and Redis while doing it. If you
  are recovering a large deployment against the clock, bring up **one** replica, let it finish, and start the
  rest afterwards.

So: on a small deployment a rebuild is a coffee break; at a hundred thousand identities it is a maintenance
window. Measure it once on a copy of your own data rather than trusting either adjective.

A rebuild you start with `spnr rebuild` reports its own elapsed time on its own stdout when it finishes —
`rebuilt hot state of every site in …`, or `rebuilt hot state of site <tenant>/<namespace>/<site> in …`. The two
log lines that bracket a rebuild, `hot-state rebuild started` with the site count and `hot-state rebuild
finished` with the site count and a `duration` field, are written by whichever process ran it: for the CLI that
is the one-shot `migrate` run, not the `spinneret` service. To watch the **automatic** startup rebuild instead:

```bash
./spnrctl logs spinneret | grep 'hot-state rebuild'
```

**Do not watch `DBSIZE` for progress.** A restore writes identity health with `HSETNX` into one hash
per endpoint group, so a site of 100,000 identities across 50 groups adds **50 keys**, not 5 million —
`DBSIZE` sits still for the whole rebuild and looks stuck. Watch the field count of one of those hashes
instead, which climbs towards the identity count:

```bash
./spnrctl exec valkey valkey-cli --scan --pattern 'sp:*:hs:*' --count 100 | head -1   # pick one
./spnrctl exec valkey valkey-cli hlen '<the key it printed>'
```

Measure it once on a copy of your own data so you know whether a full rebuild is a coffee break or a
maintenance window.

Instances also rebuild **automatically** at startup when the epoch key is missing. Losing Redis outright
therefore needs no intervention, only patience.

### What a Redis loss destroys and no rebuild brings back

| Lost | Consequence |
| --- | --- |
| In-flight leases | Nodes get `lease_unknown` for their reports and acquire again |
| Reports still queued in the stream shards | Those outcomes never reach the state machine |
| The current breaker evaluation windows | Breakers start from a clean window |
| Up to 60 s of health-score movement | Whatever happened since the last snapshot |
| Console sessions | Everyone signs in again |

A freshly rebuilt site is briefly more expensive to schedule than a warm one, because every identity starts
with the same ready-queue score in every endpoint group and traffic is what decorrelates them. Measured on a
100,000-identity dataset: a cold site collapsed at 2,500 cycles/s where the same site carried 4,500/s once
warm.

**Note.** `spnr rebuild --site` does not reset runtime counters such as the active-lease count, by design — a
rebuild must not drop live leases. If those counters are genuinely corrupt, the repair is to unlink the whole
site key prefix and then rebuild that site. Never unlink a live lease hash (`<prefix>:{s<siteKey>}:ls:*`, so
`sp:{s12}:ls:*` with the default prefix and site key 12) on its own:
the lease hash is what decrements the identity's active-lease counter when the lease ends, and deleting one
leaks that counter so the identity never becomes available again.

---

## Key rotation

Rotation is online: every configured key can unwrap, and only the current one wraps new data keys. It
re-encrypts the small wrapped-DEK column, never the data, so the job is cheap and safe under traffic.

```bash
# 1. Generate a new key and append it to the file. The last line is current by default.
spnr kek generate --id k2 >> deploy/compose/secrets/kek.key

# 2. Restart the instances so they load the new key list. The KEK set is read once at
#    startup and the file is a bind-mounted secret, so `up -d` recreates nothing when
#    only the file changed — restart the processes explicitly.
./spnrctl restart spinneret

# 3. Confirm both keys are loaded and see how much work is pending.
spnr kek status
# current kek: k2
#   k1               wrapped_records=18422
#   k2               wrapped_records=0 current
# rewrap: idle (0/0)

# 4. Re-wrap every stored data key onto the current KEK. Resumable; follows a job
#    already running on another instance rather than starting a second one.
spnr kek rewrap
# kek rewrap started
# progress: 5000/18422 records re-wrapped to k2
# ...
# kek rewrap finished

# 5. Only when k1 shows wrapped_records=0, remove its line and restart.
spnr kek status
```

### What rewrapping does

| Property | Value |
| --- | --- |
| Tables covered | `identity_payloads`, `secret_versions`, `proxies`, `notification_channels`, `system_keys` |
| Batch size | 500 rows |
| Concurrency | Exactly one instance at a time, via the advisory lock `spinneret:kek:rewrap` |
| Progress | Persisted in `system_settings` under `kek_rewrap_status`, so every instance and the console report it |
| Heartbeat | Every 2 s; a job whose instance stops beating for 30 s is reported as interrupted |
| Safety | Each update is a compare-and-swap on the old KEK id *and* the old wrapped DEK, so a record re-sealed concurrently is never overwritten |

The console does the same under **Secrets → Key encryption keys**, with the per-key wrapped-record counts and
a progress bar. Interrupting the CLI stops the job it started; running it again resumes from whatever is still
not on the current key.

### How to verify it

```bash
spnr kek status
# current kek: k2
#   k1               wrapped_records=0
#   k2               wrapped_records=18422 current
# rewrap: idle (18422/18422)
# last finished: 2026-09-18T03:41:07Z
```

Three things to read: `wrapped_records=0` for every retired key, no key marked `NOT-CONFIGURED` (that means
records still reference a key this process does not hold — those rows cannot be decrypted), and no
`last error:` line. Then confirm the data really opens: reveal a secret in the console and open one identity's
payload fields. Back the key file up again.

### How to fail back

Rotation is failure-tolerant because both keys stay configured throughout:

| Stage | To go back |
| --- | --- |
| After steps 1–2, before any rewrap | Set `SPINNERET_KEK_CURRENT=k1` (or move `k1` last) and restart. Nothing was wrapped with `k2` |
| During the rewrap | Same, then run `spnr kek rewrap` again to move the already-rewrapped records back onto `k1` |
| After step 5, `k1` removed | You cannot, and you do not need to: every record is on `k2` and `k2` is configured. The risk is the opposite one — a dump taken *before* the rotation still needs `k1` |

**Keep every retired key** until `spnr kek status` reports zero records wrapped with it *and* no backup you
still rely on was taken under it. See [Secret vault → Rotating the KEK](./10-secrets.md#rotating-the-kek) for
the envelope-encryption details behind all of this.

---

## Data retention and reclaiming disk

### Every retention setting

All of these except the stream cap are also editable at **Settings → System** in the console, where a
change applies on the next hourly pass without a restart. An environment variable that is set wins over
the console and pins the setting.

| Setting | Default | Prunes | Mechanism |
| --- | --- | --- | --- |
| `SPINNERET_RETENTION_RISK_EVENTS` | `720h` (30 d) | `risk_events` | Daily partitions dropped |
| `SPINNERET_RETENTION_MINUTE_STATS` | `720h` (30 d) | `outcome_stats_minutely`, `acquire_stats_minutely`, `node_stats_minutely`, `payload_access_minutely` | Daily partitions dropped |
| `SPINNERET_RETENTION_HOUR_STATS` | `4320h` (180 d) | `identity_stats_hourly` | Monthly partitions dropped |
| `SPINNERET_RETENTION_STATE_EVENTS` | `8760h` (365 d) | `state_events` | Monthly partitions dropped |
| `SPINNERET_RETENTION_AUDIT` | `8760h` (365 d) | `audit_logs` | Monthly partitions dropped |
| `SPINNERET_CLICKHOUSE_TTL_DAYS` | `90` | `report_events`, `lease_events` | ClickHouse `TTL`, enforced by ClickHouse itself |
| `SPINNERET_REPORT_DEDUP_TTL` | `1h` | Report dedup markers and ended lease hashes in Redis | Redis key expiry |
| `SPINNERET_LATE_REPORT_WINDOW` | `10m` | Ended lease hashes in Redis (floor) | Redis key expiry |
| `SPINNERET_STREAM_MAXLEN` | `1000000` | Report stream backlog, per shard | Shard owner trims to the consumer position |
| Compose `x-logging` | 20 MiB × 10 per container | Container stdout/stderr | Docker's `json-file` driver rotates |

Container logs are the one entry above that is not a Spinneret setting, and the only store in this stack
that has no bound of its own: Docker's `json-file` driver keeps everything unless it is told a size, and it
writes to the Docker data root — the same filesystem as the three volumes. The Compose files cap every
service at 20 MiB × 10 files. **If you do not deploy with these Compose files, set the equivalent
yourself**, either per container or as `log-opts` in `/etc/docker/daemon.json`; nothing inside Spinneret can
do it for you, and a deployment whose logs are uncapped will eventually fill the disk PostgreSQL is on.

Each PostgreSQL retention must be at least `24h`. `alert_events` is purged at a fixed **90 days** in batches of
5,000 and is not configurable. Identity payload versions keep the last **5** per identity.

### How partitions are dropped

The hourly `partition_manager` job runs on the **leader** instance with a 15-minute timeout and does three
things: creates missing partitions ahead of time, drops expired ones, and purges `alert_events`.

Partitions are named `<table>_p<YYYYMMDD>` for daily tables and `<table>_p<YYYYMM>` for monthly ones. Only
names matching that scheme are considered, and a partition is dropped only when its **upper bound** is not
after `now - retention` — so a partition is never dropped while it can still hold a row inside the retention
window. The first catalog scan runs without locks, so the usual "nothing expired" pass never blocks readers;
the parent table is locked `ACCESS EXCLUSIVE` only once something expired is found, and then only for the
drop.

Daily tables are kept covered from yesterday to seven days ahead; monthly tables from the start of last month
to three months ahead.

Raising a retention affects only data written from then on — the partitions already dropped are gone.
Lowering one drops partitions on the next hourly run.

If the job is failing, partitions stop being dropped and the disk fills quietly. That is what
`spinneret_job_runs_total{job="partition_manager",result="error"}` is for.

### Reclaiming disk in a hurry

In the order you should reach for them.

**1. Find out where it went.** The ClickHouse commands here and below need `CLICKHOUSE_PASSWORD` in your own
shell — set it as shown under [Capacity planning](#capacity-planning).

```bash
docker system df -v
./spnrctl exec -T postgres psql -U spinneret -c "
  SELECT relname, pg_size_pretty(pg_total_relation_size(c.oid)) AS size
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname='public' AND c.relkind IN ('r','p')
  ORDER BY pg_total_relation_size(c.oid) DESC LIMIT 20"
./spnrctl exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" -q "
  SELECT table, partition, formatReadableSize(sum(bytes_on_disk)) AS disk
  FROM system.parts WHERE active AND database='spinneret'
  GROUP BY table, partition ORDER BY sum(bytes_on_disk) DESC LIMIT 20"
```

**2. Lower a retention and let the next hourly job do the work.** The cleanest lever. Change the variable,
restart the instances, wait up to an hour — or force a pass by restarting the leader, since the job runs
almost immediately after leadership is acquired.

**3. Drop PostgreSQL partitions now**, with the same function the job uses, at a cutoff you choose:

```bash
./spnrctl exec -T postgres psql -U spinneret -c \
  "SELECT spinneret_drop_partitions_before('identity_stats_hourly','month', now() - interval '60 days')"
./spnrctl exec -T postgres psql -U spinneret -c \
  "SELECT spinneret_drop_partitions_before('outcome_stats_minutely','day', now() - interval '7 days')"
```

It returns the number of partitions dropped, takes the parent lock only when something expires, and refuses a
table that is not partitioned. Use the table's real granularity — `day` or `month` from the table above.

**4. Drop ClickHouse partitions now.** Partitions are one per day, named `YYYYMMDD`:

```bash
./spnrctl exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" -q \
  "ALTER TABLE spinneret.report_events DROP PARTITION '20260601'"
```

Lowering `SPINNERET_CLICKHOUSE_TTL_DAYS` also works — the server issues `ALTER TABLE … MODIFY TTL` at startup
when the retention differs from what the table carries — but ClickHouse then materializes the new TTL across
existing parts, which is a heavy background rewrite. Dropping whole partitions is instant; the TTL change is
the durable fix. Do the drop first if you are short of disk *now*.

**5. Old images and build cache.** *Manage → 12* lists images belonging to this Compose project that no
container references — running or stopped — and offers to remove them, defaulting to no. It can also clear
build cache, but BuildKit's cache is **per host, not per project**: clearing it makes the next build of
anything on that machine a cold one. This project never prunes a shared host on its own.

**What not to do:** do not delete the `chdata` volume to free space. It expires its own rows on a TTL, and
deleting it throws away all request history to solve a problem that a partition drop solves surgically. And
never trim a report stream that still holds unprocessed reports, or unlink live lease hashes — see the note
under [Rebuilding the hot state](#rebuilding-the-hot-state).

---

## Accounts, passwords and tokens

Three things exist only as console RPCs, not as CLI commands: changing a password, adding a user, and listing
users. The installer's management menu reaches them over the same endpoint the console uses, on loopback, with
the password read without echo and sent on stdin — never as an argument, because `argv` is readable by every
process on the host.

| Task | Where |
| --- | --- |
| Change an administrator password | Console, or *Manage → 1*. Every other session of that account ends. Offers to update `SPINNERET_ADMIN_PASSWORD` in `.env` so it does not go stale |
| Add an administrator | Console **Access → Users → New**, or *Manage → 2*. `admin` is tenant-wide, not platform-wide |
| Node tokens | `spnr token create --name … --scope …` (both flags are required), or *Manage → 4*. The plaintext is printed once and cannot be fetched again |
| Revoke a token | Console **Tokens**, or `RevokeToken`. Takes effect fleet-wide on the event bus; without the bus, within `SPINNERET_TOKEN_CACHE_TTL` (30 s) |
| Lost the administrator password | `spnr admin init` is idempotent and will **not** reset an existing one. Reset it from another administrator account, or from the console as a platform administrator |

Scopes, roles, bindings and what each one can reach: [Tenants, users and tokens](./11-access-control.md).

---

## Routine checks

### Daily, two minutes

| Look at | Normal |
| --- | --- |
| `/readyz` on every replica | `status: ok`, all four checks `ok` |
| `sum(spinneret_stream_pending)` | Near zero; spikes that drain |
| `spinneret_report_lag_seconds` p99 | Tens of milliseconds. 9.5 ms was measured at ~10,000 reports/s, 30.7 ms at ~20,000/s |
| `spinneret_acquire_duration_seconds` p99, per site | Low single-digit milliseconds. 1.86 ms was measured at 4,500 cycles/s |
| `spinneret_acquire_total{result="ok"}` share | Well above 95 %. Note `overloaded` separately: it is shedding, not failure |
| `spinneret_breaker_state` | `0` everywhere. Any `2` that is not a deliberate manual open wants a look |
| `spinneret_identities_available`, per endpoint group | Above zero on every group carrying traffic |
| Console **Overview**, per-site cards | No site with a risk share climbing against yesterday |

### Weekly, twenty minutes

| Look at | Normal |
| --- | --- |
| `spinneret_stream_owned_shards` summed | Exactly `SPINNERET_REPORT_SHARDS` |
| `spinneret_acquire_peers` and `spinneret_acquire_peer_beat_age_seconds` | Peers equal the API replica count; beat age a few seconds |
| `spinneret_job_runs_total{result="error"}` by job | Flat. Any slope on `partition_manager` means the disk is no longer being trimmed |
| `spinneret_db_write_batches_total{result="dropped"}`, `spinneret_state_writer_dropped_changes_total` | **Zero, always.** Any increase is lost data |
| `spinneret_report_total{outcome="unknown"}` share | Under 20 %. Higher means your signal rules are not classifying what nodes report |
| `spinneret_lease_reaped_total{kind="abandoned"}` vs `ok` acquires | Under 5 %. Higher means nodes are acquiring and not reporting |
| `spinneret_notify_deliveries_total{result!="ok"}` | Zero, or you will not hear about the next incident |
| Disk on all three volumes, and the partition list | Growing at the rate your retention implies, not faster |
| A backup exists, off the host, and its key decodes | Yes |
| `spnr kek status` | No `NOT-CONFIGURED` key, no records on a retired key |
| Console **Audit** | Nothing you cannot account for; every `secret.reveal` has a name next to it |

### Monthly or per release

- Run the restore drill on a spare host. A backup you have never restored is a hypothesis.
- Re-check capacity against the growth you actually saw, especially `identity_stats_hourly` and ClickHouse.
- Review token expiries and revoke what no node uses any more.
- Rotate the KEK if your policy says so, and after anyone with access to it leaves.

---

## Incident playbooks

Each one: how to confirm it, how to stop the bleeding, how to fix it, how to prevent it. Diagnosis from the
symptom end — including the complete error-reason table — is [Troubleshooting](./18-troubleshooting.md); these
are the *operator actions* for the situations that page you.

### 1. The pool is draining

Nodes get `429 no_identity_available` and the share of usable identities keeps falling.

**Confirm.** `spinneret_identities_available{site,group}` trending to zero while
`spinneret_acquire_total{result="exhausted"}` rises. `spinneret_identities{state=…}` shows where they went:
`banned`, `quarantined`, `expired` or `disabled`. The response header `Spinneret-Retry-After-Ms` says how long
the server thinks it will last. In the console, the **Heatmap** shows it directly: a whole column unavailable
is one endpoint group, a whole row is one identity, the whole grid is a rule.

**Stop the bleeding.** Decide first whether the pool is *genuinely* used up or a rule is eating it. If actions
are firing, put the action policy into `shadow` mode — it still evaluates and still records, but it stops
changing state — and publish. If the site is being actively hostile, pause it (`SetSitePaused`) rather than
letting every identity get banned.

**Fix.** If a rule misfired, roll the damage back: `RevertActions` with a `time_range` (start required), the
`policy_id` and `rule` that did it, and `dry_run: true` first to see the blast radius. Add `reset_failures`
and `reset_health` when the streaks themselves are wrong. If the pool is genuinely exhausted, the answer is
more identities, a longer `reuse_interval`, or fewer concurrent nodes — not a looser ban rule.

**Prevent.** Publish new action policies in `shadow` mode first and compare what they *would* have done
against what you want. Alert on `spinneret_identities_available` per group rather than on the total.

### 2. A site is being banned wholesale

Every identity used against one site comes back banned within minutes.

**Confirm.** `spinneret_report_total{site,outcome=~"banned|forbidden|captcha"}` dominating that site, and
`spinneret_actions_total{site,action="ban"}` climbing. In the console, **Risk Events** for the site shows the
rule that fired and the markers behind it.

**Stop the bleeding.** Pause the site. This is what the site switch is for: nodes get `503 site_paused`,
retry, and stop burning identities while you think. The alternative — letting it run — converts a target-side
change into a permanently damaged pool.

**Fix.** Read the reports before changing the policy. If the target genuinely started rejecting, no policy
change helps and the fix is at the node: different proxies, different identity type, lower rate. If the signal
rules are *misreading* a new response shape as a ban, fix the rules, publish, and `RevertActions` the bans
they caused over the window they ran in.

**Prevent.** Keep a signal rule for "unknown" narrow enough that an unrecognised response does not classify as
a ban. Alert on `spinneret_report_total{outcome="unknown"}` share so a changed response shape shows up as
*unclassified* before it shows up as a wholesale ban.

### 3. Breakers are flapping

An endpoint group opens, closes, opens again.

**Confirm.** `increase(spinneret_breaker_transitions_total{to="open"}[30m])` above 3 for one
`site`/`group`, with `spinneret_breaker_state` oscillating between `2` and `0`. Console **Breakers** lists the
transitions with their reasons.

**Stop the bleeding.** Open the breaker manually (`OpenBreaker`, with a duration) so the group stops
half-opening into a target that is not ready. A held-open breaker is a clean `503 circuit_open` for nodes;
flapping is a stream of intermittent failures that also churns identity state.

**Fix.** Flapping almost always means the breaker's recovery is too eager for the failure: the half-open probe
succeeds, full traffic returns, the failure rate crosses the threshold again. Four fields of the breaker
policy, in the order to try them: lengthen `open_duration` (and let `max_open_duration` back off repeat
opens), raise `half_open.close_min_samples` so more than a handful of probes must succeed before closing,
raise `half_open.close_success_ratio_gte`, and widen `window` or raise `min_requests` so one bad minute does
not trip it. All of them are in [Policies](./08-policies.md).

**Prevent.** Alert on the transition *rate*, not on the state — an open breaker for five minutes and a breaker
that opened six times in half an hour are different incidents.

### 4. Report lag is growing

State the scheduler decides on is getting stale.

**Confirm.** `spinneret_report_lag_seconds` p99 rising, `sum(spinneret_stream_pending)` growing rather than
oscillating. Then split the cause in one query:

| Check | If |
| --- | --- |
| `sum(spinneret_stream_owned_shards)` < `SPINNERET_REPORT_SHARDS` | Shards are unowned: not enough worker instances, or one is wedged |
| It equals the shard count, workers busy | The workers are the bottleneck — add worker instances |
| It equals the shard count, pending evenly spread, CPU idle | Too few shards for the instance count |
| `spinneret_report_process_duration_seconds` p99 up | The work per report got slower — usually PostgreSQL |
| `spinneret_db_write_batches_total{result="error"}` rising | PostgreSQL is the problem, not the worker |

**Stop the bleeding.** Add worker-capable instances. `SPINNERET_STREAM_MAXLEN` (1,000,000 per shard) caps how
much backlog is kept at all — beyond it entries are dropped silently, so a growing backlog has a deadline.

**Fix.** Keep 2–4 shards per worker instance. One instance applies about 20,000 reports/s inside the 200 ms
target; batching on the node side is free throughput, because a batch of 200 costs one `ingest.lua` call per
stream shard represented in the batch — at most `SPINNERET_REPORT_SHARDS`, pipelined — not 200 calls.

**Prevent.** Alert on `sum(spinneret_stream_pending)` and on `sum(spinneret_stream_owned_shards)` below the
shard count. The second one is the alert that catches "reports are accepted but nothing changes" before anyone
notices.

### 5. Acquire latency is high, or the server is shedding

Nodes see slow acquires, or `503 overloaded`.

**Confirm.** Read three series together, in this order:

| `spinneret_acquire_script_seconds` p99 | `spinneret_acquire_total` | Meaning |
| --- | --- | --- |
| Normal | `overloaded` rising, Redis CPU has headroom | The admission budget is narrower than what Redis can take |
| Spiked | `overloaded` rising | **Redis is stalled.** Raising the budget makes it worse |
| Any | `exhausted` rising *with* Redis CPU high | You are past the knee — congestion collapse |

**`overloaded` is not a fault.** It is admission control shedding load in the server, before any Redis
command, so that the fleet does not collapse. Treat a steady low rate of it as a capacity signal, not an
outage.

**Stop the bleeding.** If you are past the knee, offered load must drop below it for the loop to unwind —
once it starts, it does not recover on its own. Pause the busiest site, or throttle the nodes. The signature is
unmistakable: at collapse, one acquire issued **220 Redis commands instead of 49** and `EVALSHA` averaged
**157 µs instead of 27 µs**, with Valkey burning 3.5–3.7 cores to serve four times the commands the same load
needs when healthy.

**Fix.** By which row of the table you matched: raise `SPINNERET_ACQUIRE_FLEET_INFLIGHT`, or find the Redis
stall (an AOF rewrite, a fork, a noisy neighbour), or add Redis capacity and lower offered load. Check
`spinneret_acquire_peers` is right before you touch the budget — a frozen peer count makes every instance
admit a stale share. Full analysis: [Performance → Admission control](./17-performance.md#admission-control).

**Prevent.** Keep offered load under about 80 % of your measured knee. Leave admission control on. Give every
API instance a distinct `SPINNERET_INSTANCE_ID`. Alert on `overloaded` share *separately* from other acquire
failures.

### 6. Redis is out of memory

**Confirm.** Valkey refusing writes, or a restart loop with exit code 137 and
`docker inspect --format '{{.State.OOMKilled}}' <container>` returning `true`. *Manage → 7* checks
ClickHouse, Valkey and PostgreSQL for you.

```bash
./spnrctl exec -T valkey valkey-cli info memory | grep -E 'used_memory_human|maxmemory_human'
```

**Stop the bleeding.** Give it more memory. Eviction is **not** an option: the policy is `noeviction` on
purpose, and evicting this state corrupts the pool silently instead of failing loudly. If you cannot add
memory, reduce the traffic terms: lower `SPINNERET_REPORT_DEDUP_TTL` (minimum `1m`) and restart the
instances — existing markers still expire on their old TTL, so relief arrives over the following hour, not
instantly.

**Fix.** Compute the working set from the formula under [Capacity planning](#capacity-planning) and provision
for twice it. Check `SPINNERET_STREAM_MAXLEN` against the backlog you want to survive:
`rate × seconds / shards`.

**Prevent.** Alert on Valkey `used_memory` against its ceiling. Keep the bundled Valkey settings — `--save ""`
with `appendonly yes`, and the raised AOF rewrite thresholds — because AOF rewrites were the single largest
stability finding in this system: with the defaults a rewrite fired every ~52 s at 4,000 cycles/s, each fork a
multi-second I/O stall, and one stall in the metastable region below the knee is enough to start the
congestion loop.

### 7. PostgreSQL is out of connections or disk

**Confirm, connections.** `FATAL: sorry, too many clients already` in the logs, or:

```bash
./spnrctl exec -T postgres psql -U spinneret -c \
  "SELECT count(*), (SELECT setting FROM pg_settings WHERE name='max_connections') FROM pg_stat_activity"
```

**Stop the bleeding.** Lower `SPINNERET_DATABASE_MAX_CONNS` and restart the replicas, or raise
`max_connections` on the server. The arithmetic that must hold is
`replicas × max_conns + headroom < max_connections`; the Compose default is 300 and 32.

**Confirm, disk.** The table-size query under [Reclaiming disk in a hurry](#reclaiming-disk-in-a-hurry), plus
`spinneret_job_runs_total{job="partition_manager",result="error"}`.

**Stop the bleeding.** Drop expired partitions now with `spinneret_drop_partitions_before`. Set
`SPINNERET_RECORD_COOLDOWN_EVENTS=false` if cooldown rows dominate `state_events` and you do not need them.

**Fix.** Set retentions you can afford, and confirm `partition_manager` is running clean on the leader.

**Prevent.** Alert on `spinneret_job_runs_total{job="partition_manager",result="error"}` and on volume disk.
A failing partition manager is a silent disk leak: nothing breaks until the disk is full, and then everything
does.

### 8. ClickHouse is rejecting queries

**Confirm.** The request explorer errors or returns nothing while acquire, report, policy and config all keep
working — ClickHouse is not on the control-plane path. In the server log, `clickhouse batch insert failed`.
`MEMORY_LIMIT_EXCEEDED` is the usual cause. The container stays **healthy** throughout, because its health
check only asks `/ping`.

**Stop the bleeding.** If it is memory: `max_server_memory_usage` in
`deploy/compose/config/clickhouse-limits.xml` must stay **above** the process's idle resident size (~1.2 GiB
for the alpine image). Set below it, ClickHouse does not shrink to fit — every `INSERT` fails and the report
worker stalls on a store that is meant to be optional. Raise it, or give the host more memory, and restart the
service.

If it is disk, drop partitions (above). If it is a too-wide console query, narrow the window — the explorer
enforces its own limits and returns `query_too_large` or `query_timeout` rather than melting the server; see
[Observability → Query limits](./12-observability.md#query-limits-and-how-a-too-wide-query-fails).

**Fix.** Cap the caches rather than the total: the default mark cache alone is 5 GiB, sized for a dedicated
analytics machine. Size `SPINNERET_CLICKHOUSE_TTL_DAYS` to the disk you have.

**Prevent.** Alert on ClickHouse insert failures, not on its health check. Remember a ClickHouse outage costs
you history and nothing else — do not escalate it as a control-plane incident.

### 9. An instance is wedged

Running, not draining, not serving correctly.

**Confirm.** `/healthz` answers `200` but `/readyz` does not say `ok`, or it does and the instance still owns
no shards (`spinneret_stream_owned_shards` at zero on that instance while others carry the load), or its
`spinneret_acquire_peer_beat_age_seconds` is climbing past 30 s. `./spnrctl ps` shows it `running`, not
`healthy`.

**Stop the bleeding.** Take it out of rotation and replace it. It is stateless; nothing is lost.

```bash
docker rm -f <container-id>
./spnrctl up -d --no-recreate --wait --no-deps spinneret
```

Its shard locks expire within 10 s and the survivors pick them up. The acquire budget re-divides
immediately if the instance shut down gracefully, because it deregisters itself; if it crashed or is
wedged, the survivors keep dividing by the old count for up to 60 s, which leaves them narrower than
they need to be rather than wider.

**Fix.** Collect evidence *before* you remove it if you can: `./spnrctl logs --tail 200 spinneret`, the
`/readyz` body (it names the failing check), and — if profiling is enabled — a goroutine dump from
`SPINNERET_PPROF_ADDR`. A replica that is up but never healthy is almost always a dependency it cannot reach
or a hot state with no epoch.

**Prevent.** Alert on `up{job="spinneret"} == 0` *and* on readiness, because a wedged instance is scrapeable.
Make sure `/readyz` — not `/healthz` — is what your load balancer probes.

### 10. A bad policy or config was published

**Confirm.** The damage starts at a publish. Console **Policies** and **Config Center** both show version
history with an author and a timestamp; **Audit** shows the publish. Correlate the version's time against when
`spinneret_actions_total` or `spinneret_acquire_total{result!="ok"}` changed slope.

**Stop the bleeding.** Roll the version back. Both subsystems are versioned with rollback, and neither needs a
restart or a node redeploy: a policy rollback reaches the fleet on the event bus, and a config rollback is a
new version that watchers pick up — measured at **p99 46 ms** from publish to watcher wake-up on an *idle*
instance, which is not the state you are in during an incident.

**Fix.** Then undo the *consequences*, which the rollback does not: `RevertActions` scoped to the
`policy_id`/`rule` and the `time_range` the bad version was live, `dry_run: true` first.

**Prevent.** Use `shadow` mode for action policies and compare before enforcing. Use the rule debugger against
real reports before publishing. Keep the change window narrow so `time_range` is easy to get right, and keep
publish permissions off the accounts that do not need them.

### 11. A credential leaked

A token, a secret value, an identity payload or the KEK itself.

**An API token.** Revoke it (console **Tokens**, or `RevokeToken`). A `token.revoked` event drops it from every
instance's verification cache immediately; without the event bus, within `SPINNERET_TOKEN_CACHE_TTL` (30 s).
Nodes see their next call fail with `token_revoked` and no grace period, so have the replacement minted first.
Then read `payload_access_minutely` and the audit log for that token id to see what it fetched.

**A secret value.** Rotate at the target first, store a new version, let readers converge, then revoke the old
one at the target. Individual versions cannot be deleted — if a leaked version must become unreadable, delete
the secret and re-create it, then repoint or republish everything that referenced it. The order and the
convergence times are in [Secret vault → Rotating a secret](./10-secrets.md#rotating-a-secret).

**An identity payload.** Disable or retire the identity and replace it. `payload_access_minutely` records
which token fetched credentials of which identity type per minute, and the request explorer shows what was
actually done with the identity.

**The KEK.** Rotate it: [Key rotation](#key-rotation). Rewrapping re-seals every data key under the new KEK,
so the leaked key can no longer unwrap anything current — but it can still unwrap **every backup taken before
the rotation**. Re-take your backups after the rewrap finishes, and treat the old dumps as compromised.

**In every case.** Read the audit log — it is the only record of who revealed which secret, and it is retained
for `SPINNERET_RETENTION_AUDIT` (365 days by default). Then find out how it leaked: a token in a repository, a
`.env` in an image, a reveal by an account that should not have had the permission. Hardening checklist:
[Security](./19-security.md).

---

## Page or ticket

The full starter rule file, with real PromQL, is
[Observability → Starter alert rules](./12-observability.md#starter-alert-rules). What follows is the triage
question — who gets woken up — and one warning.

### Page

| Alert | Why it is a page |
| --- | --- |
| `up{job="spinneret"} == 0` for 2 m | An instance is gone |
| `sum(spinneret_stream_owned_shards)` below `SPINNERET_REPORT_SHARDS` for 5 m (write the count as a literal in the rule) | Reports are being accepted and never applied. Silent, and it gets worse |
| `sum(spinneret_stream_pending) > 50000` for 5 m | The backlog has a deadline: `SPINNERET_STREAM_MAXLEN` drops entries past the cap silently |
| `max(spinneret_breaker_state) by (site,group) == 2` for 5 m | A group has been circuit-broken for five minutes |
| `increase(spinneret_db_write_batches_total{result="dropped"}[15m]) > 0` or `increase(spinneret_state_writer_dropped_changes_total[15m]) > 0` | Writes were discarded. This is data loss |
| `spinneret_identities_available` at `0` for an endpoint group carrying traffic | That group cannot work at all |
| Disk above your threshold on any of the three volumes **or on the Docker data root** | Recovery from a full disk is much worse than prevention. The data root is where container logs and image layers live, and it is easy to watch only the volumes |

### Ticket

| Alert | Why it is not a page |
| --- | --- |
| Acquire failure share above 5 % (excluding `overloaded`) | Wants a policy or pool decision, not a night |
| Acquire **shedding** share above 5 % | A capacity signal. See the warning below |
| Acquire p99 above your budget for 10 m | Degraded, not down |
| Report lag p99 above 5 s for 10 m | Decisions are stale, not wrong |
| More than 3 breaker opens in 30 m for one group | Flapping. Needs a policy change with a clear head |
| Risk-outcome share above 10 % for 15 m | A trend to investigate |
| `unknown` outcome share above 20 % | Signal rules are not classifying. Urgent for correctness, not for availability |
| Abandoned leases above 5 % of `ok` acquires | A node-side bug |
| `spinneret_acquire_peer_beat_age_seconds > 30` | The budget division is frozen. Page it only if sheds follow |
| `spinneret_notify_deliveries_total{result!="ok"}` | Your alerting is broken — which is why it must not be your *only* alerting path |
| `spinneret_job_runs_total{result="error"}` by job | Except `partition_manager`, which is a slow disk leak |

### The warning: shedding is not failing

`spinneret_acquire_total{result="overloaded"}` and `spinneret_acquire_admission_total{result=~"shed_.*"}` mean
admission control did its job. Fold them into a generic "acquires are failing" alert and you will send someone
to add identities for what is a capacity problem — while the actual failure modes, `exhausted` and
`circuit_open`, get diluted below the threshold. Alert on them **separately**, and put the diagnostic order in
the alert's own description: check `spinneret_acquire_script_seconds` p99 first, because if it spiked, raising
the budget makes things worse.

The mirror-image mistake is treating `exhausted` as a capacity problem. It means the identity pool is
genuinely empty for that group — a supply or policy question, not a hardware one.

---

## Next

- [Observability and alerting](./12-observability.md) — every metric on this page, and the alert rules in full.
- [Troubleshooting](./18-troubleshooting.md) — the same failures from the symptom end, plus the complete
  error-reason table.
- [Performance and tuning](./17-performance.md) — the measured ceilings behind every number here.
- [Configuration reference](./03-configuration.md) — every variable these procedures change.
- [CLI reference](./15-cli.md) — every `spnr` command and flag, and the server's signals.
- [Installation and deployment](./02-installation.md) — the stack these procedures operate on.
- [Security](./19-security.md) — the hardening checklist and what to review in the audit log.
