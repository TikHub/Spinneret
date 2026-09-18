# Benchmarks

[English](benchmarks.md) · [简体中文](benchmarks.zh-CN.md)

Measurements of the v0.1 performance targets of the design document (§18.4), taken with
`test/load/` against the Docker Compose stack in `deploy/compose/`. Everything here is
reproducible with the commands in [Reproducing](#reproducing); the raw snapshots of the run
that produced these numbers are written to `.loadtest/<run>/` (git-ignored).

Every table carries a **before** column: the same scenario measured on the same machine before
the Lua hot-path optimizations of the scheduler and worker tracks and before the Valkey tuning
of [Valkey settings](#valkey-settings). Before and after were measured months apart on a
laptop whose background load is not identical, so treat single-digit percentages as noise; the
changes below are 30–120 %.

## Summary

| Target (§18.4) | Before | After | Verdict |
| --- | --- | --- | --- |
| Acquire server-side latency p99 < 5 ms | 0.95 ms at 2,000 cycles/s per instance | **1.86 ms at 4,500 cycles/s** per instance; 4.32 ms at 4,993 Acquire/s | **met** |
| Acquire throughput ≥ 5,000/s per instance | 2,000 cycles/s sustained; 4,614/s peak for Acquire alone | **4,993 Acquire/s** sustained at p99 4.32 ms (peak 7,792/s). Full acquire→report cycle: **5,500/s** with `max_concurrent_leases: 4`, **4,500/s** with exclusive leases | **met, with conditions** — for Acquire itself, and for the full cycle with a non-exclusive rotation policy; 4,500/s with exclusive leases. See [Verdict per target](#verdict-per-target) |
| Report ingest ≥ 20,000/s per instance | 29,584 reports/s accepted | **44,437 reports/s** accepted, zero rejections | **met** |
| Report → state update p99 < 200 ms | 21.5 ms at 9,846 reports/s | **30.7 ms at 19,761 reports/s**, all 1,200,200 applied; 9.5 ms at 9,905/s | **met**, at twice the rate |
| Config change awareness < 1 s | p99 55.4 ms idle | **p99 46.1 ms** idle | **met** |
| Concurrent long polls ≥ 10,000 per instance | 10,001 held | 9,994 held at the sample instant, zero failures, 0.05 cores | **met** |

Per Valkey thread, one acquire→report cycle costs **168.3 µs** of Redis CPU, down from 271.2 µs
— **5,940 cycles/s per Redis thread instead of 3,690** (measured per script in
[Where the time goes](#where-the-time-goes)).

### Verdict per target

The per-instance Acquire target is **met**, with the conditions stated:

* **Acquire alone** (the endpoint §18.4 names): 4,993/s on one instance at server-side
  p99 4.32 ms, with Valkey at 0.75 of one core. Peak 7,792/s when latency is allowed to go.
* **The full acquire→report cycle** (Acquire + Report + the worker applying it and ending the
  lease — five Lua scripts): **5,500/s** on one instance with the rotation policy's
  `max_concurrent_leases: 4`, at acquire p99 4.49 ms and report lag p99 62 ms.
* **With exclusive leases** (`max_concurrent_leases: 1`, what `spnr seed` configures and the
  most expensive point in the configuration space): **4,500 cycles/s**, at acquire p99 1.86 ms
  and report lag p99 9.3 ms. This is the one number short of 5,000/s.
* **All of it on a single Valkey instance**, as the design's test condition requires. No Redis
  Cluster was used or needed to reach these figures.

Two instances behind the load balancer reach **3,000 cycles/s in aggregate**, which is *less*
than one instance reaches alone. That is now the main bottleneck and it is not Redis CPU —
see [Why two instances reach less than one](#why-two-instances-reach-less-than-one).

## Environment

Everything — the server replicas, PostgreSQL, Valkey, ClickHouse, the load balancer **and the
k6 load generator** — runs inside one Docker Desktop VM on a laptop. This is the honest
qualifier on every number below: the load generator competes with the system under test.

| Component | Value |
| --- | --- |
| Host | macOS (Darwin 25.6.0), Docker Desktop 29.4.0 |
| Docker VM | 16 CPUs, 7.75 GiB RAM, shared by every container and by k6 |
| Server | `spinneret:local` (this repo), distroless, `SPINNERET_REPORT_SHARDS=16`, `SPINNERET_DATABASE_MAX_CONNS=32` |
| Redis | `valkey/valkey:8-alpine` (8.1.10), single instance — see [Valkey settings](#valkey-settings) |
| PostgreSQL | `postgres:17-alpine`, `shared_buffers=512MB`, `max_connections=300` |
| ClickHouse | `clickhouse/clickhouse-server:25.8-alpine`, capped by `config/clickhouse-limits.xml` |
| Load balancer | `caddy:2-alpine`, `deploy/compose/config/Caddyfile` |
| Load generator | `grafana/k6:latest` (k6 2.2.0) in the same VM, compose profile `loadtest` |

The design document assumes "4 vCPU / 8 GB per service instance and a single Redis instance".
At the sustained rates one server instance uses 1.4–1.6 of 16 cores and Valkey 1.6–1.9 (of
which about one is the command-executing main thread), so neither is starved by the sizing the
design assumes.

### Valkey settings

Three settings changed since the "before" runs. All three are in
`deploy/compose/docker-compose.yml` and each is justified by a measurement below.

| Setting | Before | After | Why |
| --- | --- | --- | --- |
| `io-threads` | 1 | **4** | +3 % throughput but a large difference to the tail at the knee — acquire p99 18.9 → 4.1 ms at 6,000 cycles/s. See [Valkey io-threads](#valkey-io-threads) |
| `save` (RDB) | default `3600 1 300 100 60 10000` | **disabled** (`--save ""`) | The AOF is the durability mechanism. The RDB points forked a second time for the same data, once a minute under load |
| `auto-aof-rewrite-percentage` / `-min-size` | 100 / 64 MiB | **300 / 1 GiB** | With the defaults a rewrite fired every ~52 s at 4,000 cycles/s. See [AOF rewrites](#aof-rewrites-were-tipping-the-system-over) |

`appendonly yes` and `appendfsync everysec` are unchanged: durability is not traded away here.

ClickHouse also got `config/clickhouse-limits.xml`, which caps its caches (the mark cache
defaults to 5 GiB). Unconstrained, it grew past 1.1 GiB and the kernel OOM killer picked it
during a 5,000/s run, freezing the whole VM. Note that `max_server_memory_usage` is compared
against process RSS, which idles near 1.2 GiB for this image whatever the caches are set to;
setting it *below* that does not shrink ClickHouse, it makes every INSERT fail with
`MEMORY_LIMIT_EXCEEDED` and stalls the report worker. It is set to 1.5 GiB as a safety net.

## Dataset

The §18.4 test condition — one site with 100,000 identities and 50 endpoint groups:

```bash
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50
```

Creates, idempotently, site `loadtest` (client `web`), endpoint groups `g0…g49` matching
`/api/g<i>/`, identity type `loadtest_cookie` with 100,000 synthetic cookie identities, the
published rotation policy `loadtest-rotation` (`weighted_random`, `candidate_sample: 32`,
`lease_ttl: 60s`, `max_concurrent_leases: 1`, `reuse_interval: 0s`, no proxies), a breaker
policy that never trips, the config item `crawler/loadtest.json`, and a node token.

`max_concurrent_leases: 1` means every lease is *exclusive*: while an identity is leased it is
unavailable in all 50 groups. That is the expensive end of the configuration space and it is
what the seed uses by default.

### A freshly seeded site must be warmed before it is measured

A seed (and `spnr rebuild`) writes the **same ready-queue score for every identity in every
endpoint group**. All 50 groups therefore propose the same head of the queue, and with
exclusive leases they collide on it: a group that samples a leased identity pushes its score
out to the lease expiry, which is Redis work that a warmed site does not do. Measured on this
dataset, a cold site collapsed into `resource_exhausted` at 2,500 cycles/s where the same site
carried 4,500/s once warm.

Traffic decorrelates the 50 queues on its own. The ladder in [Reproducing](#reproducing) —
45 s each at 1,500, 2,500, 3,500 and 4,000 cycles/s, with a settle between the steps — is
enough; about a million acquires. **Every throughput number below was measured on a warmed
site**, which is also the steady state a real deployment runs in. Cold-start behaviour is a
real property worth knowing about (a rebuilt hot state is briefly more expensive), not the
number to quote for capacity planning.

## Method

`test/load/run.sh` wraps each scenario:

1. scrape `/metrics` of every replica (through the `lb` container, since the replicas publish
   no host port) plus `INFO` / `INFO commandstats` from Valkey → `before.json`;
2. run the k6 scenario in the compose `loadtest` profile;
3. take a second snapshot mid-run for gauges under load → `mid.json`;
4. snapshot again at the end → `after.json`, and diff → `delta.json`.

Latency comes from the **server-side** histograms (`spinneret_acquire_duration_seconds`,
`spinneret_report_lag_seconds`), quantiles interpolated inside the Prometheus buckets, so it
excludes the Docker network and Caddy. Throughput comes from k6's own counters over the
scenario window (the snapshot window is ~3 s longer and would understate it). Valkey CPU comes
from `used_cpu_user + used_cpu_sys` deltas over the snapshot window, and the per-command
attribution from `INFO commandstats` deltas, which include calls made from inside Lua scripts.

`spinneret_acquire_duration_seconds` tops out at a 0.25 s bucket; a p99 printed as `inf`
below means "above 250 ms", i.e. the run was in overload.

Two method changes since the "before" runs, both of which affect what the numbers mean:

* **`MID_STATS=0`.** The mid-run snapshot used to call `docker stats`, which walks every
  container of the machine. On Docker Desktop that is expensive enough to perturb the run it
  is measuring: at 4,000 cycles/s it stalled the system under test for ~5 s (19,000 late
  iterations) and turned an otherwise 1.9 ms p99 into 11.9 ms. Every throughput row below was
  measured with `MID_STATS=0`; the per-container CPU/memory gauges are sampled separately, in
  runs that are not near the knee.
* **Settle between runs.** A run that ends in overload leaves leases held for their 60 s TTL
  and ready-queue scores pushed up to 60 s into the future. Starting the next run before that
  drains measures the recovery, not the system. The recipe in
  [Reproducing](#reproducing) waits until the ready queues are due again.

## Scenario A — Acquire → Report

`test/load/acquire_report.js`: one `LeaseService/Acquire`, then one `ReportService/Report`
with `release: true`, at a constant arrival rate, endpoint group picked uniformly from the 50.
The release is asynchronous: the report goes onto the Redis stream and a worker ends the lease,
so one iteration exercises five Lua scripts (acquire, ingest, lease_retain, observe, release).

### One replica, seeded policy (`max_concurrent_leases: 1`) — the per-instance figures

| Offered | Achieved | Acquire p50 | Acquire p99 | Lag p50 | Lag p99 | Server CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,500/s | 1,500/s | 0.12 ms | **0.50 ms** | 2.50 ms | 4.96 ms | 0.52 | 0.42 |
| 2,500/s | 2,500/s | 0.18 ms | **0.94 ms** | 2.51 ms | 4.97 ms | 0.80 | 0.73 |
| 3,500/s | 3,500/s | 0.28 ms | **1.74 ms** | 2.53 ms | 6.72 ms | 1.09 | 1.17 |
| 4,000/s | 4,000/s | 0.31 ms | **1.98 ms** | 2.55 ms | 8.50 ms | 1.23 | 1.37 |
| **4,500/s (2 min)** | **4,500/s** | 0.32 ms | **1.86 ms** | 2.56 ms | 9.32 ms | 1.41 | 1.61 |
| 5,000/s | 1,746/s + 534/s exhausted | 189 ms | > 250 ms | > 10 s | > 10 s | 0.51 | **2.02** |

The 2-minute run at 4,500/s is the headline per-instance result: **540,000 acquires and 540,000
reports, zero errors, zero exhausted**, acquire p99 1.86 ms, report lag p99 9.3 ms.
Before the optimizations the same instance sustained **2,000/s** and collapsed at 3,000/s.

| | Before | After | Change |
| --- | ---: | ---: | ---: |
| Sustained acquire→report cycles/s, one instance, exclusive leases | 2,000/s | **4,500/s** | **+125 %** |
| Acquire p99 at the sustained rate | 0.95 ms | 1.86 ms | at 2.25× the rate |
| Valkey commands per cycle at the sustained rate | 51.5 (102,987 cmd/s at 2,000/s) | **49.3** (215,163 cmd/s at 4,500/s) | −4 % |
| `EVALSHA` average over the 5.1 scripts of a cycle | 43.7 µs | **24.5 µs** | **−44 %** |

### One replica, `max_concurrent_leases: 4`

| Offered | Achieved | Acquire p50 | Acquire p99 | Lag p50 | Lag p99 | Server CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 5,000/s (2 min) | 4,999/s | 0.36 ms | **4.35 ms** | 2.73 ms | 91.4 ms | 1.60 | 1.77 |
| **5,500/s (2 min)** | **5,500/s** | 0.40 ms | **4.49 ms** | 3.00 ms | 62.4 ms | 1.61 | 1.88 |
| 6,000/s (2 min) | 5,982/s | 0.68 ms | 18.0 ms | 4.93 ms | > 10 s | 1.58 | 1.94 |

5,500/s is the highest rate at which **both** §18.4 latency targets still hold (acquire
p99 < 5 ms, report → state p99 < 200 ms). At 6,000/s the throughput is still there — 5,982 of
6,000 offered — but the report worker no longer keeps up and the lag target fails first.
Before, the same configuration sustained 3,000/s.

### Two replicas through the load balancer

| Offered | Achieved | Acquire p99 (inst 1 / 2) | Lag p99 | Server CPU/inst | Valkey CPU | Valkey cmd/s |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,500/s | 1,500/s | 0.55 / 0.58 ms | 4.96 ms | 0.30 | 0.45 | 70,253 |
| 2,500/s | 2,500/s | 1.76 / 0.99 ms | 4.98 ms | 0.46 | 0.83 | 117,501 |
| **3,000/s (2 min)** | **3,000/s** | **1.36 / 1.61 ms** | 4.98 ms | 0.59 | 1.17 | 143,854 |
| 3,500/s (45 s) | 3,500/s | 1.91 / 1.36 ms | 6.1 ms | 0.61 | 1.33 | 160,039 |
| 3,500/s (2 min) | 1,288/s + 946/s exhausted | > 250 ms | > 10 s | 0.25 | **3.53** | 438,650 |
| 4,000/s | 1,743/s + 477/s exhausted | > 250 ms | > 10 s | 0.25 | **3.71** | 462,282 |
| 4,500/s | 2,117/s + 342/s exhausted | > 250 ms | > 10 s | 0.33 | **3.59** | 457,091 |

Two replicas sustain **3,000 cycles/s** — the same figure as before the optimizations, and
1,500 cycles/s *less* than one replica manages on its own. 3,500/s survives 45 s and not two
minutes. Latency at 3,000/s did improve (p99 1.72 → 1.36 ms, lag p99 6.75 → 4.98 ms) and
Valkey's main thread has far more headroom, but the ceiling did not move. See
[Why two instances reach less than one](#why-two-instances-reach-less-than-one).

### Overload behaviour (congestion collapse)

Past the knee the system still does not degrade gracefully, it collapses, and the loop is
unchanged from the first round of measurements:

1. Valkey saturates → the report worker falls behind → leases are not released;
2. identities stay leased, so acquires in the other 49 groups sample them, reject them and
   push their ready-queue score forward — one `HGET` plus one `ZADD` each;
3. a failing acquire walks many more candidates than a succeeding one: measured at the 4,000/s
   two-replica collapse, **220 Redis commands per acquire instead of 49**, and `EVALSHA`
   averaging **157 µs instead of 27 µs**;
4. that pushes Valkey further into saturation.

What did change is where the loop starts and what it costs once started. The per-command work
of a *failing* acquire is cheaper than before (97 µs versus the 212 µs measured previously),
but the feedback itself is intact, and it is what sets every knee in this document — not
Valkey's CPU ceiling. At the two-replica collapse Valkey is burning 3.5–3.7 cores across its
I/O threads to serve four times the commands the same offered load needs when healthy.

Operationally: keep the offered load under ~80 % of the measured knee, alert on
`spinneret_acquire_total{result="exhausted"}` and on `spinneret_report_lag_seconds`, and see
[Tuning](#tuning-recommendations) for the configuration that removes step 2 entirely.

## Scenario B — Acquire alone

`REPORT_MODE=none` skips the report, so only `acquire.lua` runs (one replica,
`max_concurrent_leases: 4` so leases do not make identities exclusive while they pile up).
This isolates the Acquire endpoint that §18.4 names, but it is not a realistic workload:
leases accumulate for their full 60 s TTL and pollute the ready queues.

| Offered | Before: achieved / p99 | After: achieved | After: p50 | After: p99 | Server CPU | Valkey CPU |
| ---: | --- | ---: | ---: | ---: | ---: | ---: |
| 3,000/s | 2,975/s / 9.22 ms | — | — | — | — | — |
| 4,000/s | 3,926/s / 209 ms | 3,994/s | 0.47 ms | **2.00 ms** | 0.43 | 0.58 |
| 5,000/s | 4,436/s / > 250 ms | **4,993/s** | 0.65 ms | **4.32 ms** | 0.54 | 0.75 |
| 6,000/s | — | 5,779/s | 0.91 ms | > 250 ms | 0.56 | 1.09 |
| 8,000/s | 4,614/s / > 250 ms | **7,792/s** | 4.42 ms | > 250 ms | 0.72 | 1.49 |

**Peak Acquire throughput on one instance and one Valkey: 7,792/s**, up from 4,614/s
(**+69 %**), and the §18.4 rate of 5,000/s is now reached *inside* the p99 < 5 ms target
(4,993/s at 4.32 ms) where before 5,000/s offered produced 4,436/s at p99 above 250 ms.

## Scenario C — Report ingest

`test/load/report_ingest.js`: batches of reports against a pool of 400 long-lived leases,
one replica. "Accepted" is the server's own `spinneret_report_ingest_total{result="accepted"}`;
"applied" is `spinneret_report_lag_seconds` count, i.e. reports the worker actually put into
the hot state.

| Batch × rate | Accepted | Rejected | Applied | Lag p50 | Lag p99 | Server CPU | Valkey CPU |
| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: |
| 100 × 100/s | **9,905/s** | 0 | 600,100 / 600,100 | 2.73 ms | **9.5 ms** | 0.53 | 0.93 |
| 200 × 100/s | **19,761/s** | 0 | 1,200,200 / 1,200,200 | 6.10 ms | **30.7 ms** | 0.88 | 1.53 |
| 200 × 150/s | **29,640/s** | 0 | 1,311,096 / 1,800,200 | 8.3 s | > 10 s | 0.94 | 1.71 |
| 300 × 150/s | **44,437/s** | 0 | 1,090,595 / 2,700,300 | > 10 s | > 10 s | 0.95 | 1.65 |

| | Before | After | Change |
| --- | ---: | ---: | ---: |
| Reception, zero rejections | 29,584/s | **44,437/s** | **+50 %** |
| Applied to hot state with lag p99 < 200 ms | ~9,846/s (p99 21.5 ms) | **19,761/s (p99 30.7 ms)** | **+101 %** |
| Lag p99 at ~10,000 reports/s | 21.5 ms | **9.5 ms** | **−56 %** |

The narrower of the two paths is still processing, but it doubled: one instance now applies
**~20,000 reports/s** to the hot state inside the 200 ms target, where before 19,702/s
accepted left 429,000 of 1.2M reports unapplied at the end of the run. Above that the stream
backlog grows and lag follows it.

## Scenario D — Concurrent config watchers

`test/load/watch_config.js`, one replica, direct to the instance (see the load-balancer note
below). A watcher must hold the *current* version, otherwise the server answers immediately;
the script reads the published version in `setup()`. `spinneret_config_watchers` on the
replica is the authority for the number held.

| Watchers | Held (server gauge) | Failures | Server CPU | Goroutines | Server RSS | Valkey cmd/s |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 10,000 (before) | 10,001 | 0 | 0.09 | 20,198 | 749 MiB | 52 |
| 10,000 (after) | **9,994** | **0** | **0.05** | 20,110 | 504 MiB | 206 |

The target is met: ~10,000 blocked `WatchConfig` calls on one instance, zero failures, 0.05
CPU cores, and a blocked watcher costs no Redis at all. (9,994 rather than 10,001 is the
sample instant, not a failure — six k6 pollers were between polls; `watch_polls` reports zero
errors for the run.)

The load-balancer hazard documented in the first round is unchanged and still worth reading:
10,000 connections arriving in the same millisecond overran Caddy's `dial_timeout 2s`, and
with `max_fails 1` a single failed dial ejected the only upstream and produced a total outage
while the server was at 0.8 % CPU. The shipped `Caddyfile` now uses `max_fails 3`; raise
`dial_timeout` above the worst-case accept latency of a connection burst on your own LB, and
stagger node restarts.

## Scenario E — Config change awareness

`test/load/config_awareness.py` publishes new versions of `crawler/loadtest.json` and measures
the wake-up latency of its own watchers. Publisher and watchers live in one process, so both
timestamps come from one clock. `total` is measured from just before the publish RPC (the
conservative figure); `from_commit` from the moment `PublishConfig` returned.

| Situation | Watchers woken | total p99 (worst round) | total max | from_commit p99 |
| --- | ---: | ---: | ---: | ---: |
| Idle instance, 200 probe watchers, 6 publishes — before | 1,200 / 1,200 | 55.4 ms | 57.2 ms | 44.0 ms |
| Idle instance, 200 probe watchers, 6 publishes — after | **1,200 / 1,200** | **46.1 ms** | **46.8 ms** | **38.4 ms** |

Zero poll errors, `woken_ratio` 1.0. Two orders of magnitude inside the 1 s target.

## Analysis

### Where the time goes

Every Acquire→Report cycle is five Lua scripts. Sampling every Valkey command for 0.3 s under
a ~1,000 cycles/s load (`SLOWLOG` with `slowlog-log-slower-than 0`, grouping the `EVALSHA`
entries by script SHA) attributes the cost. Same method, same dataset, same machine as the
"before" column:

| Script | Calls per cycle | Before (µs) | After (µs) | Change |
| --- | ---: | ---: | ---: | ---: |
| `acquire.lua` | 1 | 104.2 | **68.3** | **−34 %** |
| `observe.lua` (worker state update) | 1 per report | 71.7 | **43.5** | **−39 %** |
| `release.lua` (lease end) | 1 per released lease | 67.0 | **34.0** | **−49 %** |
| `ingest.lua` | 1 per batch | 16.3 | **12.2** | **−25 %** |
| `lease_retain.lua` | 1 per site per request | 12.0 | **10.3** | **−14 %** |
| **Total per cycle** | | **271.2** | **168.3** | **−38 %** |
| **Cycles/s per Redis thread** | | **3,690** | **5,940** | **+61 %** |

Two scripts outside the cycle were sampled in the same window: `apply.lua` at 49.8 µs (an
automatic cooldown action) and `breaker_eval.lua` at 47.2 µs (a per-group sweep, rate-limited
by `NotifyMinInterval`, not a per-report script).

The interleaved micro-benchmarks behind these changes — per script, per Lua helper, with the
baseline sources frozen under `test/perf/testdata/` — are in
[`test/perf/README.md`](../test/perf/README.md). Those run directly against Valkey on the
developer machine, where absolutes are about 1.4× cheaper than this VM; the table above is the
end-to-end confirmation in the VM.

The measured 5,940 cycles/s per Redis thread is the ceiling the *scripts* impose. The knees in
Scenario A are lower (4,500–5,500 on one instance, 3,000 on two) because the congestion loop
starts before the thread is full, not because the thread is full.

### Valkey io-threads

Valkey executes every command — Lua included — on the main thread, so extra I/O threads move
only socket read and write. The first round of profiling concluded from a closed-loop
micro-benchmark that they were not worth it. Measured end to end on this stack they buy very
little throughput and a great deal of tail latency:

| One instance, `max_concurrent_leases: 4`, 60 s | io-threads=1 | io-threads=4 |
| --- | ---: | ---: |
| 6,000/s offered → achieved | 5,955/s | **5,999/s** |
| 6,000/s → acquire p99 | 18.9 ms | **4.09 ms** |
| 6,000/s → report lag p99 | 4,656 ms | **41 ms** |
| 6,000/s → Valkey CPU | 0.91 cores | 1.92 cores |
| 7,000/s offered → achieved | 6,765/s | **6,976/s** |
| 7,000/s → acquire p50 / p99 | 40.0 ms / 237 ms | **0.87 ms / 9.88 ms** |
| 7,000/s → Valkey CPU | 0.95 cores | 1.91 cores |

`io-threads 4` is now the compose default. The trade is about one extra core of Valkey CPU for
a tail that stays inside the §18.4 targets 1,000–2,000 cycles/s higher up the curve; on a host
where Redis has cores to spare that is worth taking. `VALKEY_IO_THREADS=1` restores the old
behaviour. It does not raise the *sustained* rate much: at the knee the main thread is still
the wall, which is why the 5,500/s and 4,500/s figures above are only a little above what
io-threads=1 reached.

### AOF rewrites were tipping the system over

The single largest stability finding of this round. With the Valkey defaults
(`auto-aof-rewrite-percentage 100`, `auto-aof-rewrite-min-size 64mb`) plus the default RDB
`save` points, a load run at 4,000 cycles/s triggered **23 AOF rewrites in 20 minutes — one
every 52 s** — each forking a ~1 GiB process and writing ~290 MiB, with a background RDB save
on top. Spinneret's hot state is mostly short-lived keys (lease hashes, report dedup markers,
stream entries), so the AOF grows far faster than the dataset it describes and the rewrite
trigger fires constantly.

Each rewrite is a multi-second I/O stall in a laptop VM, and the region just below the knee is
**metastable**: a stall there is enough to start the congestion loop, which never recovers. The
symptom was runs at rates that had been clean an hour earlier collapsing from the first second.
After `--save ""`, `--auto-aof-rewrite-percentage 300` and `--auto-aof-rewrite-min-size 1gb`,
the identical ladder that had been collapsing at 2,500 cycles/s ran clean to 4,000/s and the
4,500/s two-minute run passed. Nothing else changed between those two runs.

Durability is unchanged: the AOF is still on with `appendfsync everysec`. What was removed is
duplicated work (RDB alongside AOF) and an over-eager rewrite trigger.

### Why two instances reach less than one

One instance sustains 4,500 cycles/s; two instances behind Caddy sustain 3,000. This is
reproducible and it is not Redis CPU: at the two-replica 3,500/s collapse Valkey's *healthy*
cost would be about 1.4 cores of a 4-core io-threads budget, and what is actually observed is
3.5 cores spent on four times as many commands — the congestion loop, started earlier.

What is different with two instances at the same offered rate:

* **Twice the in-flight concurrency against one Valkey.** Each instance has its own connection
  pool and there is no shared admission control, so the same arrival rate arrives as roughly
  twice as many concurrent requests. Valkey queues deeper, each acquire takes longer, exclusive
  leases are therefore held longer, contention rises, and step 2 of the collapse loop starts at
  a lower offered rate.
* **The report shards are split.** Each instance owns 8 of the 16 stream shards, so the lease
  releases that keep the pool full depend on *both* workers keeping up; the collapse begins
  when either one slips.
* **Caddy is not the cause.** Running the same two-replica load directly against the instances
  (`SPINNERET_URL=http://spinneret:8080`, Docker DNS round-robin) collapsed at 3,500/s in the
  same way, with Caddy at 0.5 % CPU.

The next levers, in the order they are likely to pay:

1. **Bound in-flight acquires per instance** (admission control on the Acquire path), so
   offered load above the knee queues in the server rather than multiplying concurrency at
   Redis. This is what turns the collapse into graceful degradation.
2. **Make a failing acquire cheap.** It is currently ~5× a succeeding one (228 vs 47 commands);
   a candidate that is rejected for exclusivity costs an `HGET` plus a `ZADD` every time any of
   the 50 groups samples it. Remembering the rejection for the lease's remaining lifetime, or
   sampling with a filter that already excludes leased identities, removes the amplifier.
3. **Shard Redis.** Every key is hash-tagged by site or report shard, so a Redis Cluster
   multiplies the 5,940 cycles/s per thread across primaries. This is the lever that raises the
   ceiling once the loop is fixed; on its own it only moves the collapse to a higher rate.

### The lease-end fix (history, before the "before" column)

Kept because it explains the baseline every table above compares against, and because the cost
shape it describes is the one the next lever has to attack.

`release.lua`/`reap.lua` used to restore the ready-queue score of an identity in **every
endpoint group of its client** on every lease end, because some other group might have pushed
the score to the exclusive lease's expiry while the lease was held. That was one `ZSCORE`
(plus an `HGET` when it matched) per group per lease end, whether or not any group had pushed
anything: exactly **50.00 `ZSCORE` per acquire** on the 50-group dataset (5.00 on an otherwise
identical 5-group site), and `release.lua` at 152.6 µs versus 51.3 µs on the 5-group site —
**+2.25 µs per endpoint group**, making it the most expensive script in the system.

The fix records the fact on the identity instead of rediscovering it: an acquire that pushes an
identity because of another group's exclusive lease sets `xg` on the identity hash, and the
lease end only walks the groups `xg` names. Measured at 1,000 cycles/s on the 50-group dataset:

| Metric at 1,000 cycles/s, 50 groups | Before the fix | After the fix |
| --- | ---: | ---: |
| Valkey commands/s | 95,793 | 49,491 |
| Valkey CPU (commandstats) | 0.34 cores | 0.26 cores |
| `EVALSHA` average | 54.9 µs | 43.7 µs |
| `ZSCORE` per acquire | 50.00 | 0 |
| Sustained acquire→report rate (2 instances) | 1,678/s | 3,000/s |

Regression test: `internal/scheduler/lease_test.go:TestExclusivePushMarkerScopesTheRestore`.
The worker track later narrowed `xg` further, from "all groups" to the list of groups that
actually pushed. The 43.7 µs `EVALSHA` average in the right-hand column is the "before" figure
of the table in [Scenario A](#one-replica-seeded-policy-max_concurrent_leases-1--the-per-instance-figures);
it is now 24.5 µs.

### Redis sizing

Measured with `MEMORY USAGE` on the live dataset (Valkey 8, 100,000 identities, 50 groups):

| Structure | Key | Measured | Per unit |
| --- | --- | ---: | --- |
| Ready queue | `P:T:rdy:<eg>` with 100,000 members | 6,457,568 B | **64.6 B per (identity × endpoint group)** → **≈ 62 MiB per 1M ready-queue entries** |
| Health state | `P:T:hs:<eg>` | 84,072 B for 1,177 entries | ≈ 71 B per warmed (identity × group) |
| Identity | `P:T:id:<i>` | 201 B average, 296 B max over 400 sampled | per identity (was 264 B) |
| Report dedup marker | `P:R:dd:<reportId>` | 80 B | per report, for `SPINNERET_REPORT_DEDUP_TTL` (1 h) |
| Ended lease | `P:T:ls:<leaseId>` | ≈ 290 B | per lease, kept `max(late window, dedup TTL)` after the lease ends |
| Report stream entry | `P:R:stream` (16 shards) | ≈ 440 B | per entry |

A freshly rebuilt §18.4 dataset measures **351 MiB** of Valkey memory in total. What dominates
in production is the *traffic-proportional* state, not the dataset:

```
Redis working set ≈ 350 MiB (dataset)
                  + up to 345 MiB           (health entries, once every identity is warm in every group)
                  + reports/s  × dedup_TTL × 80 B    (dedup markers)
                  + acquires/s × dedup_TTL × 290 B   (ended lease hashes)
                  + backlog_entries × 440 B          (report streams)
```

At the sustained 4,500 cycles/s measured above and the default 1 h dedup TTL that is 1.2 GiB of
dedup markers plus 4.2 GiB of ended lease hashes — far more than the dataset. Sizing
`SPINNERET_REPORT_DEDUP_TTL` to the retry behaviour of your nodes is the single biggest memory
lever; see [Tuning](#tuning-recommendations).

Load runs in this VM show it directly: Valkey goes from 351 MiB after a rebuild to ~950 MiB
after a two-minute run at 4,500 cycles/s, and every byte of that is dedup markers and ended
lease hashes waiting out their hour.

### What is an environment limit and what is not

| Observation | Environment or product? |
| --- | --- |
| One instance stops at 4,500–5,500 cycles/s | **Product**: the congestion loop starts before Valkey's main thread is full (the scripts allow 5,940 cycles/s per thread) |
| Two instances reach less than one | **Product**: unbounded per-instance concurrency against one Redis; see above |
| A failing acquire costs ~5× a succeeding one | **Product**: the amplifier of every collapse in this document |
| Server instances never exceeded 1.7 of 16 cores | Headroom |
| Collapses that appeared and disappeared between identical runs | **Environment**, with a **product-adjacent** trigger since fixed: AOF rewrite forks every 52 s in a laptop VM. Tuned in `deploy/compose` |
| ClickHouse OOM-killed, freezing the VM during a 5,000/s run | **Environment** (7.75 GiB VM shared with k6, three ClickHouse servers and an unrelated application stack), fixed by capping its caches |
| A cold (freshly seeded or rebuilt) site collapsing at 2,500 cycles/s | **Product**, mild: a uniform initial ready-queue score correlates all 50 groups. Traffic fixes it within about a million acquires |
| 10,000 watchers failing through Caddy | **Environment / LB configuration**, not the server |
| k6 and Caddy at ~1 core each during the high-rate runs | **Environment**: the load generator shares the VM with the system under test |

## Tuning recommendations

**Replicas.** Adding server instances does not add Acquire throughput while a single Redis is
the bottleneck — and, as measured above, two replicas reach *less* aggregate throughput than
one until per-instance admission control exists. Scale replicas for availability, for long-poll
capacity (10,000 watchers per instance) and for report *processing* (each instance owns a share
of the 16 stream shards); scale Redis, and keep the offered rate per instance under its knee,
for Acquire throughput.

**Redis.** Every key is hash-tagged by site (`{s<key>}`) or by report shard (`{r<n>}`), and
`SPINNERET_REDIS_ADDRS` puts the client into cluster mode, so the scaling path is a Redis
Cluster (or one Redis per group of sites). Budget roughly one Redis **primary** per
**4,500 acquire→report cycles/s** with exclusive leases, or **5,500/s** with
`max_concurrent_leases: 4` — the script cost allows 5,940 cycles/s per thread and the
difference is congestion headroom you want to keep. Memory: the formula in
[Redis sizing](#redis-sizing).

**Valkey settings.** Use the ones in `deploy/compose/docker-compose.yml`: `io-threads 4`
(tail latency), `--save ""` with `appendonly yes` (do not pay for two persistence mechanisms),
and `auto-aof-rewrite-percentage 300` / `auto-aof-rewrite-min-size 1gb` (a workload of
short-lived keys otherwise rewrites the AOF continuously). See
[Valkey settings](#valkey-settings).

**`SPINNERET_REPORT_DEDUP_TTL`.** The default 1 h costs 80 B per report *and* keeps each ended
lease hash (~290 B) alive for the same hour — at 4,500 cycles/s, 5.4 GiB of Redis. Nodes retry
within seconds, not hours: 5–15 minutes is usually enough, and cuts that by 4–12×. The compose
file exposes it as `SPINNERET_REPORT_DEDUP_TTL`.

**`SPINNERET_STREAM_MAXLEN`.** Shard owners trim their stream to the consumer group position,
so the default of 1,000,000 per shard is the cap of a backlog the workers cannot drain, not a
permanent buffer. Size it to the backlog you want to survive a worker outage:
`rate × seconds_of_backlog / shards`. At 20,000 reports/s and 60 s of tolerated backlog that is
75,000 per shard; lower it when Redis memory is tight, because entries past the cap are dropped
silently.

**`max_concurrent_leases`.** Still the biggest per-site lever, and now quantified on the
optimized code: `1` (exclusive) sustains 4,500 cycles/s per instance, `4` sustains 5,500 — and
`4` degrades far more gracefully, because a leased identity no longer has to be pushed out of
the ready queue of every other group of the client, which is the amplifier of the collapse.
Use `1` only where a site really requires one request per identity at a time.

**Endpoint groups.** Per-identity state is per endpoint group: ~65 B of ready queue and ~71 B
of health per identity × group. Model endpoint groups by *policy* need, not by URL count —
50 groups × 100,000 identities is already 5M ready-queue entries (≈ 310 MiB).

**`candidate_sample`.** Unchanged advice, for a new reason. The scheduler track made acquire
read candidate state on demand rather than bulk-loading the whole sample, which cut the
K-dependence of `acquire.lua` from ~13 µs between K=1 and K=32 to ~4 µs
(`test/perf/README.md`). Lowering it from 32 now buys almost nothing and costs extra rounds
under contention. Leave it at the default.

**Database pool.** `SPINNERET_DATABASE_MAX_CONNS=32` per instance was never a factor:
PostgreSQL stayed under 3 % CPU in every scenario, because the hot path is Redis-only and
PostgreSQL sees only batched writes.

**ClickHouse.** Cap its caches on a shared host (`config/clickhouse-limits.xml`); the default
5 GiB mark cache is sized for a dedicated analytics machine. Keep `max_server_memory_usage`
*above* the process's idle RSS (~1.2 GiB for the alpine image) — below it, inserts fail and the
report worker stalls.

**Load balancer.** See [Scenario D](#scenario-d--concurrent-config-watchers): with few
upstreams, `max_fails 1` + `fail_duration 5s` turns one slow dial into a total outage.

**Profiling.** Set `SPINNERET_PPROF_ADDR` (for example `127.0.0.1:6060`) to serve
`/debug/pprof/` on its own listener for a profiling session. It is off by default and never
mounted on the API or metrics listener, because the endpoints are unauthenticated and expose
heap contents and goroutine stacks — never publish the port.

## Reproducing

Requirements: the stack from `deploy/compose` running (`scripts/compose-init.sh`, then
`docker compose … up -d --wait`), and Python 3 on the host for the tooling.

```bash
cd <repo>
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"

# 1. Seed the §18.4 dataset and capture the node token (JSON on stdout).
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50 > /tmp/seed.json
export LOADTEST_TOKEN=$(python3 -c "import json;print(json.load(open('/tmp/seed.json'))['token'])")

# 2. Per-instance figures. SPINNERET_REPLICAS must be exported for *every* compose
#    call, otherwise the next one scales the service back to the .env value.
export SPINNERET_REPLICAS=1
$COMPOSE up -d --wait

# 3. Warm the site (see "A freshly seeded site must be warmed"). Settle between steps:
#    a run that ended in overload leaves 60 s leases and pushed ready scores behind it.
for r in 1500 2500 3500 4000; do
  MID_STATS=0 test/load/run.sh "warm-$r" acquire_report.js ACQUIRE_RATE=$r DURATION=45s
  sleep 70
done

# 4. The per-instance acquire->report knee (2 minutes, the headline run).
MID_AFTER=70 MID_STATS=0 test/load/run.sh acq1-4500 acquire_report.js ACQUIRE_RATE=4500 DURATION=2m

# 5. Acquire alone. `tune.py --max-concurrent-leases 4` first, because leases accumulate
#    for their full TTL when nothing releases them.
python3 test/load/tune.py --max-concurrent-leases 4
for r in 4000 5000 6000 8000; do
  MID_STATS=0 test/load/run.sh "acqonly-$r" acquire_report.js \
    ACQUIRE_RATE=$r DURATION=45s REPORT_MODE=none
  sleep 70
done
python3 test/load/tune.py --restore

# 6. Report ingest.
MID_AFTER=30 MID_STATS=0 test/load/run.sh ing1-20k report_ingest.js \
  BATCH=200 REPORT_RATE=100 DURATION=60s LEASES=400

# 7. Concurrent watchers (direct to the instance, see Scenario D).
MID_AFTER=45 test/load/run.sh watch1-10000 watch_config.js \
  WATCHERS=10000 DURATION=90s RAMP_S=25 SPINNERET_URL=http://spinneret:8080

# 8. Config change awareness (publisher and watchers in one process).
python3 test/load/config_awareness.py --watchers 200 --publishes 6 --interval 3

# 9. Per-script cost: sample every command for 0.3 s under a ~1,000 cycles/s load and
#    group the EVALSHA entries by script SHA (the table in "Where the time goes").
MID_STATS=0 test/load/run.sh slowlog acquire_report.js ACQUIRE_RATE=1000 DURATION=60s &
sleep 40 && $COMPOSE exec -T valkey valkey-cli config set slowlog-log-slower-than 0
# ... 0.3 s later: slowlog get, then restore slowlog-log-slower-than to 10000.

# 10. Back to the normal topology.
unset SPINNERET_REPLICAS
$COMPOSE up -d --wait
```

Each run leaves `before.json`, `mid.json`, `after.json`, `delta.json`, `gauges.json`, `k6.txt`
and `k6.json` under `.loadtest/<name>/`. `test/load/metrics.py diff a.json b.json` prints the
server-side deltas of any two snapshots, including the per-Valkey-command CPU attribution.

Extra tools:

* `test/load/tune.py --max-concurrent-leases 4` republishes the seeded rotation policy with a
  different value (and `--restore` puts the seeded one back).
* `MID_STATS=0` drops the `docker stats` part of the mid-run snapshot; use it for every run
  near the knee (see [Method](#method)).

### Housekeeping

Load tests leave traffic-proportional state behind (dedup markers, ended lease hashes, stream
entries) that expires on its own within `SPINNERET_REPORT_DEDUP_TTL`, plus ClickHouse rows that
do not. On a small machine it is worth reclaiming it between runs:

```bash
# Trim the report streams (test data only; drops any unprocessed backlog).
$COMPOSE exec -T valkey valkey-cli eval \
  "local n=0 for i=0,15 do n=n+redis.call('XTRIM','sp:{r'..i..'}:stream','MAXLEN',5000) end return n" 0
# Drop report dedup markers.
$COMPOSE exec -T valkey sh -c \
  "valkey-cli --scan --pattern 'sp:{r*}:dd:*' --count 5000 > /tmp/k; xargs -a /tmp/k -n 1000 valkey-cli unlink"
# Analytics rows from load runs: ~2 GiB after an afternoon of testing.
$COMPOSE exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" \
  -q "TRUNCATE TABLE spinneret.report_events; TRUNCATE TABLE spinneret.lease_events"
```

> **Never unlink `sp:{<site>}:ls:*`.** A lease hash is what decrements the identity's `al`
> (active leases) counter when the lease ends. Deleting live ones leaks that counter and the
> identity is never available again — doing it once here left 97,520 of 100,000 identities
> permanently leased, and every following run collapsed into `resource_exhausted`. The same
> applies to trimming a stream that still holds unprocessed `release: true` reports. If it
> happens, the repair is to unlink the whole site prefix and rebuild:
>
> ```bash
> $COMPOSE exec -T valkey sh -c \
>   "valkey-cli --scan --pattern 'sp:{<site-tag>}:*' --count 5000 > /tmp/k; xargs -a /tmp/k -n 1000 valkey-cli unlink"
> $COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild --site loadtest
> ```
>
> Note that `spnr rebuild --site` on its own does **not** reset runtime counters (by design —
> a rebuild must not drop live leases); the unlink is what clears them. And a rebuilt site is
> cold, so warm it again before measuring.

Skipping the housekeeping is what turned a repeat of a clean run into a collapse: accumulated
keys and Valkey memory in a 7.75 GiB VM, with Valkey being OOM-killed and restarting mid-run.
