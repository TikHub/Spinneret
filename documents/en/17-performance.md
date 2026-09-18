# Performance and tuning

**What this stack actually does, measured: per-instance throughput and latency for acquire, report and
config, where the time goes inside Redis, how it behaves past its knee, and the levers that matter — in
the order they pay.**

[中文](../zh/17-performance.md)

---

## Contents

- [Summary](#summary)
- [Verdict per target](#verdict-per-target)
- [The environment](#the-environment)
- [The method](#the-method)
- [What the numbers exclude](#what-the-numbers-exclude)
- [The dataset](#the-dataset)
- [A freshly seeded or rebuilt site must be warmed](#a-freshly-seeded-or-rebuilt-site-must-be-warmed)
- [Acquire → Report](#acquire--report)
- [Acquire alone](#acquire-alone)
- [Report ingest](#report-ingest)
- [Concurrent config watchers](#concurrent-config-watchers)
- [Config change awareness](#config-change-awareness)
- [Where the time goes](#where-the-time-goes)
- [Past the knee: congestion collapse](#past-the-knee-congestion-collapse)
- [Admission control](#admission-control)
- [Valkey settings](#valkey-settings)
- [Valkey io-threads](#valkey-io-threads)
- [Redis sizing](#redis-sizing)
- [Tuning, in the order it pays](#tuning-in-the-order-it-pays)
- [What is an environment limit and what is not](#what-is-an-environment-limit-and-what-is-not)
- [Reproducing](#reproducing)
- [Housekeeping](#housekeeping)
- [Next](#next)

---

## Summary

Measured against the v0.1 performance targets with the k6 scenarios in `test/load/`, against the Compose
stack in `deploy/compose/`. One server instance, one Valkey instance, no Redis Cluster.

| Target | Result | |
| --- | --- | --- |
| Acquire server-side p99 < 5 ms | **1.86 ms at 4,500 cycles/s**; 4.32 ms at 4,993 Acquire/s | met |
| Acquire throughput ≥ 5,000/s per instance | **4,993 Acquire/s** sustained at p99 4.32 ms, peak 7,792/s. Full acquire→report cycle: **5,500/s** with `max_concurrent_leases: 4`, **4,500/s** with exclusive leases | met, with conditions |
| Report ingest ≥ 20,000/s per instance | **44,437 reports/s** accepted, zero rejections | met |
| Report → state update p99 < 200 ms | **30.7 ms at 19,761 reports/s**, every report applied; 9.5 ms at 9,905/s | met, at twice the rate |
| Config change awareness < 1 s | **p99 46.1 ms** | met |
| Concurrent long polls ≥ 10,000 per instance | **~10,000 held**, zero failures, 0.05 cores | met |

One acquire→report cycle costs **168.3 µs** of Redis CPU, which is **5,940 cycles/s per Redis thread**.

Four qualifications, stated once and true of every number below:

- **Per instance, single Redis.** No Redis Cluster was used or needed to reach these figures.
- **The full cycle is five Lua scripts**, not one: `acquire`, `ingest`, `lease_retain`, `observe`,
  `release`. A "cycles/s" figure is complete round trips, including the worker applying the report and
  ending the lease.
- **The knee is a feedback loop, not a CPU ceiling.** The scripts allow 5,940 cycles/s per Redis thread;
  the knees are lower because failing acquires amplify. See
  [Past the knee](#past-the-knee-congestion-collapse).
- **The tables predate per-instance acquire admission control.** Every run in this page was measured
  before the acquire gate existed, which is why the two-replica numbers are as bad as they are. No
  published figure here was measured with the gate on. [Admission control](#admission-control) says what the
  change does and how to measure it yourself, and marks clearly which claims are implementation and
  which are measurement.

---

## Verdict per target

The per-instance Acquire target is met, with the conditions stated:

- **Acquire alone** (the endpoint the target names): 4,993/s on one instance at server-side p99 4.32 ms,
  with Valkey at 0.75 of one core. Peak 7,792/s when latency is allowed to go.
- **The full acquire→report cycle** (Acquire + Report + the worker applying it and ending the lease —
  five Lua scripts): **5,500/s** on one instance with the rotation policy's `max_concurrent_leases: 4`,
  at acquire p99 4.49 ms and report lag p99 62 ms.
- **With exclusive leases** (`max_concurrent_leases: 1`, what `spnr seed` configures and the most
  expensive point in the configuration space): **4,500 cycles/s**, at acquire p99 1.86 ms and report lag
  p99 9.3 ms. This is the one figure short of 5,000/s.
- **Report ingest** is two ceilings, not one. Reception reaches 44,437/s with zero rejections;
  *processing* — the worker applying reports to the hot state inside the 200 ms target — reaches about
  20,000/s. Processing is the narrower one and the one to plan with.
- **Long polls and change awareness** are met with two orders of magnitude of headroom, and a blocked
  watcher costs no Redis at all.

The one figure that was worse than a single instance is two instances behind the load balancer:
**3,000 cycles/s in aggregate**, less than one instance manages alone. That is the measurement
[Admission control](#admission-control) exists to answer, and no gated figure has been published to
replace it.

---

## The environment

Everything — the server, PostgreSQL, Valkey, ClickHouse, the load balancer **and the load generator** —
runs inside one Docker VM on a laptop. That is the honest qualifier on every number in this page: the
load generator competes with the system under test.

| | |
| --- | --- |
| Host | macOS (Darwin 25.6.0), Docker Desktop 29.4.0 |
| Docker VM | 16 CPUs, 7.75 GiB RAM, shared by every container and by the load generator |
| Server | this repository's image, distroless, `SPINNERET_REPORT_SHARDS=16`, `SPINNERET_DATABASE_MAX_CONNS=32`, the payload cache at its defaults (`SPINNERET_PAYLOAD_CACHE=true`, `SPINNERET_PAYLOAD_CACHE_SIZE=200000`, so the whole 100,000-identity dataset fits in it) |
| Redis | `valkey/valkey:8-alpine` (8.1.10), single instance, the settings in [Valkey settings](#valkey-settings) |
| PostgreSQL | `postgres:17-alpine`, `shared_buffers=512MB`, `max_connections=300` |
| ClickHouse | `clickhouse/clickhouse-server:25.8-alpine`, caches capped by `config/clickhouse-limits.xml` |
| Load balancer | `caddy:2-alpine`, `deploy/compose/config/Caddyfile` |
| Load generator | k6 in the same VM, Compose profile `loadtest` |

Spinneret v0.1 targets 4 vCPU and 8 GiB per service instance and a single Redis instance. At the sustained
rates one server instance uses 1.4–1.6 of the 16 cores and Valkey 1.6–1.9 (about one of which is the
command-executing main thread), so neither is starved by that sizing — and a server instance never
exceeded 1.7 cores in any scenario, so the server is not what limits these numbers.

This is laptop-class hardware. Treat the *shapes* — where the knee is relative to the script cost, what
the collapse looks like, which lever moves which number — as the product, and the absolute rates as a
floor you should be able to beat on a dedicated host.

---

## The method

`test/load/run.sh` wraps each scenario:

1. scrape `/metrics` of every replica (through the `lb` container, since the replicas publish no host
   port) plus `INFO` and `INFO commandstats` from Valkey → `before.json`;
2. run the k6 scenario in the Compose `loadtest` profile;
3. take a second snapshot mid-run, for gauges under load → `mid.json`;
4. snapshot again at the end → `after.json`, and diff → `delta.json`.

Where each number comes from:

| Quantity | Source |
| --- | --- |
| Acquire latency | the **server-side** histogram `spinneret_acquire_duration_seconds`, quantiles interpolated inside the Prometheus buckets |
| Report lag | `spinneret_report_lag_seconds` — receipt to worker processing |
| Throughput | k6's own counters over the scenario window (the snapshot window is ~3 s longer and would understate it) |
| Reports accepted / applied | `spinneret_report_ingest_total{result="accepted"}` and the count of `spinneret_report_lag_seconds` |
| Valkey CPU | `used_cpu_user + used_cpu_sys` deltas over the snapshot window |
| Per-command and per-script cost | `INFO commandstats` deltas, which include commands issued from inside Lua scripts |
| Watchers held | `spinneret_config_watchers` on the replica |

Two method rules matter enough to state on their own, because ignoring either produces a number that
means nothing:

- **`MID_STATS=0` for any run near the knee.** The mid-run snapshot otherwise calls `docker stats`,
  which walks every container on the machine. On Docker Desktop that is expensive enough to perturb the
  run it is measuring: at 4,000 cycles/s it stalled the system under test for about 5 s (19,000 late
  iterations) and turned an otherwise 1.9 ms p99 into 11.9 ms. Every throughput row in this page was
  measured with `MID_STATS=0`; the per-container CPU and memory gauges are sampled separately, in runs
  that are not near the knee.
- **Settle between runs.** A run that ends in overload leaves leases held for their full TTL and
  ready-queue scores pushed up to that TTL into the future. Starting the next run before that drains
  measures the recovery, not the system.

---

## What the numbers exclude

- **The Docker network and the load balancer.** Latency is server-side. k6's own latencies include both:
  in the clean 5,500 cycles/s run, k6's median acquire was about 0.3 ms above the server-side p50
  (0.70 ms against 0.40 ms). They are useful for *changes* in the same environment, not as absolutes.
- **Everything the node does.** No target site is contacted, no signing is computed, no HTML is parsed.
  A "cycle" is the control-plane work around a request, not the request.
- **Anything above the histogram's last bucket.** `spinneret_acquire_duration_seconds` tops out at a
  0.25 s bucket and `spinneret_report_lag_seconds` at 10 s, so a p99 shown as `> 250 ms` or `> 10 s`
  means "off the top of the histogram", which in practice means the run was in overload.

---

## The dataset

One site, 100,000 identities, 50 endpoint groups:

```bash
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"

$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50 > /tmp/seed.json
```

`migrate` here is the **Compose service** whose image carries `spnr`; `seed` is the command. The seed is
idempotent — existing objects are kept, the node token is re-created — and prints
`{"token": …, "site": …, "groups": N, "identities": N}` on stdout.

It creates site `loadtest` (client `web`), endpoint groups `g0…g49` matching `/api/g<i>/`, the identity
type `loadtest_cookie` with 100,000 synthetic cookie identities, the published rotation policy
`loadtest-rotation`, the relaxed breaker policy `loadtest-breaker` that never trips, the config item
`crawler/loadtest.json`, and a node token. No proxies unless `--proxies` is given.

The rotation policy is the load-bearing part of the dataset:

| Field | Value | Effect |
| --- | --- | --- |
| `strategy` | `weighted_random` | health-weighted random pick |
| `candidate_sample` | `32` | 32 candidates sampled per acquire |
| `lease_ttl` | `60s` | half the product default of `120s` |
| `max_concurrent_leases` | `1` | **exclusive leases** |
| `reuse_interval` | `0s` | no enforced gap between two uses of one identity |

`max_concurrent_leases: 1` makes every lease exclusive: while an identity is leased it is unavailable in
all 50 groups. That is the expensive end of the configuration space, and it is what the seed uses by
default. `test/load/tune.py --max-concurrent-leases 4` republishes the same policy with a different
value, and `--restore` puts the seeded one back.

---

## A freshly seeded or rebuilt site must be warmed

A seed — and `spnr rebuild` — writes the **same ready-queue score for every identity in every endpoint
group**. All 50 groups therefore propose the same head of the queue, and with exclusive leases they
collide on it: a group that samples a leased identity pushes its score out to the lease expiry, which is
Redis work a warmed site does not do.

Measured on this dataset: a cold site collapsed into `resource_exhausted` at **2,500 cycles/s** where
the same site carried **4,500/s** once warm.

Traffic decorrelates the 50 queues on its own — about a million acquires, or the ladder in
[Reproducing](#reproducing). **Every throughput figure in this page was measured on a warmed site**,
which is also the steady state a real deployment runs in. Cold-start behaviour is worth knowing about (a
rebuilt hot state is briefly more expensive, which matters after
[a hot-state rebuild](./16-operations.md#rebuilding-the-hot-state)) but it is not the number to plan
capacity with.

---

## Acquire → Report

`test/load/acquire_report.js`: one `Acquire`, then one `Report` with `release: true`, at a constant
arrival rate, endpoint group picked uniformly from the 50. The release is asynchronous — the report goes
onto a Redis stream and a worker ends the lease — so one iteration exercises five Lua scripts.

**One replica, exclusive leases (`max_concurrent_leases: 1`) — the per-instance figures:**

| Offered | Achieved | Acquire p50 | Acquire p99 | Lag p50 | Lag p99 | Server CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,500/s | 1,500/s | 0.12 ms | **0.50 ms** | 2.50 ms | 4.96 ms | 0.52 | 0.42 |
| 2,500/s | 2,500/s | 0.18 ms | **0.94 ms** | 2.51 ms | 4.97 ms | 0.80 | 0.73 |
| 3,500/s | 3,500/s | 0.28 ms | **1.74 ms** | 2.53 ms | 6.72 ms | 1.09 | 1.17 |
| 4,000/s | 4,000/s | 0.31 ms | **1.98 ms** | 2.55 ms | 8.50 ms | 1.23 | 1.37 |
| **4,500/s (2 min)** | **4,500/s** | 0.32 ms | **1.86 ms** | 2.56 ms | 9.32 ms | 1.41 | 1.61 |
| 5,000/s | 1,746/s + 534/s exhausted | 189 ms | > 250 ms | > 10 s | > 10 s | 0.51 | **2.02** |

The two-minute run at 4,500/s is the headline: 540,000 acquires and 540,000 reports, zero errors, zero
exhausted, acquire p99 1.86 ms, report lag p99 9.3 ms.

**One replica, `max_concurrent_leases: 4`:**

| Offered | Achieved | Acquire p50 | Acquire p99 | Lag p50 | Lag p99 | Server CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 5,000/s (2 min) | 4,999/s | 0.36 ms | **4.35 ms** | 2.73 ms | 91.4 ms | 1.60 | 1.77 |
| **5,500/s (2 min)** | **5,500/s** | 0.40 ms | **4.49 ms** | 3.00 ms | 62.4 ms | 1.61 | 1.88 |
| 6,000/s (2 min) | 5,982/s | 0.68 ms | 18.0 ms | 4.93 ms | > 10 s | 1.58 | 1.94 |

5,500/s is the highest rate at which **both** latency targets still hold. At 6,000/s the throughput is
still there — 5,982 of 6,000 offered — but the report worker no longer keeps up, and the lag target
fails first.

**Two replicas behind the load balancer, admission control off.** These runs predate the acquire gate,
and they are the reason it exists:

| Offered | Achieved | Acquire p99 (inst 1 / 2) | Lag p99 | Server CPU/inst | Valkey CPU | Valkey cmd/s |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,500/s | 1,500/s | 0.55 / 0.58 ms | 4.96 ms | 0.30 | 0.45 | 70,253 |
| 2,500/s | 2,500/s | 1.76 / 0.99 ms | 4.98 ms | 0.46 | 0.83 | 117,501 |
| **3,000/s (2 min)** | **3,000/s** | **1.36 / 1.61 ms** | 4.98 ms | 0.59 | 1.17 | 143,854 |
| 3,500/s (45 s) | 3,500/s | 1.91 / 1.36 ms | 6.1 ms | 0.61 | 1.33 | 160,039 |
| 3,500/s (2 min) | 1,288/s + 946/s exhausted | > 250 ms | > 10 s | 0.25 | **3.53** | 438,650 |
| 4,000/s | 1,743/s + 477/s exhausted | > 250 ms | > 10 s | 0.25 | **3.71** | 462,282 |
| 4,500/s | 2,117/s + 342/s exhausted | > 250 ms | > 10 s | 0.33 | **3.59** | 457,091 |

Two replicas sustained **3,000 cycles/s** — 1,500 less than one replica on its own — and 3,500/s
survived 45 seconds but not two minutes. Latency at 3,000/s was better than a single instance's, and
Valkey's main thread had more headroom, but the ceiling moved the wrong way. Three things were different
at the same offered rate, and only the first is the cause:

- **Twice the in-flight concurrency against one Valkey.** Each instance had its own connection pool and
  there was no shared bound, so the same arrival rate arrived as roughly twice as many concurrent
  requests. Valkey queued deeper, each acquire took longer, exclusive leases were therefore held longer,
  contention rose, and the collapse loop started at a lower offered rate.
- **The report shards are split.** Each instance owns 8 of the 16 stream shards, so the lease releases
  that keep the pool full depend on *both* workers keeping up; the collapse begins when either slips.
- **The load balancer is not the cause.** Running the same two-replica load directly against the
  instances (`SPINNERET_URL=http://spinneret:8080`, Docker DNS round-robin) collapsed at 3,500/s in the
  same way, with Caddy at 0.5 % CPU.

---

## Acquire alone

`REPORT_MODE=none` skips the report, so only `acquire.lua` runs (one replica, `max_concurrent_leases: 4`
so accumulating leases do not make identities exclusive). This isolates the endpoint, but it is not a
realistic workload: leases pile up for their full 60 s TTL and pollute the ready queues.

| Offered | Achieved | p50 | p99 | Server CPU | Valkey CPU |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 4,000/s | 3,994/s | 0.47 ms | **2.00 ms** | 0.43 | 0.58 |
| **5,000/s** | **4,993/s** | 0.65 ms | **4.32 ms** | 0.54 | 0.75 |
| 6,000/s | 5,779/s | 0.91 ms | > 250 ms | 0.56 | 1.09 |
| 8,000/s | **7,792/s** | 4.42 ms | > 250 ms | 0.72 | 1.49 |

Peak Acquire throughput on one instance and one Valkey: **7,792/s**, and the 5,000/s target is reached
*inside* the p99 < 5 ms budget.

---

## Report ingest

`test/load/report_ingest.js`: batches of reports against a pool of 400 long-lived leases, one replica.
"Accepted" is what the server received; "applied" is what the worker actually put into the hot state.

| Batch × rate | Accepted | Rejected | Applied | Lag p50 | Lag p99 | Server CPU | Valkey CPU |
| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: |
| 100 × 100/s | **9,905/s** | 0 | 600,100 / 600,100 | 2.73 ms | **9.5 ms** | 0.53 | 0.93 |
| 200 × 100/s | **19,761/s** | 0 | 1,200,200 / 1,200,200 | 6.10 ms | **30.7 ms** | 0.88 | 1.53 |
| 200 × 150/s | **29,640/s** | 0 | 1,311,096 / 1,800,200 | 8.3 s | > 10 s | 0.94 | 1.71 |
| 300 × 150/s | **44,437/s** | 0 | 1,090,595 / 2,700,300 | > 10 s | > 10 s | 0.95 | 1.65 |

Reception and processing are two different ceilings, and the narrower one is processing: one instance
applies about **20,000 reports/s** inside the 200 ms target. Above that the stream backlog grows and lag
follows it — which is exactly what `spinneret_stream_pending` and `spinneret_report_lag_seconds` are
for.

Batching is close to free throughput on the node side: a batch of 200 costs one `ingest.lua` call, not
200. What it does not make free is `observe.lua`, which runs once per report in the worker — which is
why reception scales so much further than processing.

---

## Concurrent config watchers

`test/load/watch_config.js`, one replica, direct to the instance. A watcher must hold the *current*
config version, otherwise the server answers immediately and the scenario becomes a request flood; the
script reads the published version in `setup()`.

| Watchers | Held (`spinneret_config_watchers`) | Failures | Server CPU | Goroutines | Server RSS | Valkey cmd/s |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 10,000 | **9,994** | **0** | **0.05** | 20,110 | 504 MiB | 206 |

The target is met: about 10,000 blocked `WatchConfig` calls on one instance, zero failures, 0.05 CPU
cores, and **a blocked watcher costs no Redis at all**. (9,994 rather than 10,000 is the sample instant,
not a failure — a few pollers were between polls; the scenario reports zero poll errors for the run.)

10,000 is what was measured, not the ceiling: `SPINNERET_MAX_WATCHERS` (default **20,000** per instance)
is the hard cap on blocked `WatchConfig` calls, and a watcher arriving past it is rejected with
`resource_exhausted` / reason `rate_limited` rather than queued.

**The hazard here is the load balancer, not the server.** 10,000 connections arriving in the same
millisecond overran Caddy's dial timeout, and the failed dials ejected the only upstream — a total
outage while the server sat at 0.8 % CPU. Three settings in the shipped `Caddyfile` are there because of
this run:

| Setting | Value | Why |
| --- | --- | --- |
| `dial_timeout` | `5s` | Above the worst-case accept latency of a connection burst. A dial that times out counts as a failure and parks a replica that is merely busy |
| `fail_duration` | `2s` | A replica is still ejected on the first failed dial (`max_fails 1`, which `lb_policy least_conn` needs, or it keeps picking the dead replica), but the parking window is short. With `fail_duration 5s` a load test measured 76,857 "no upstreams available" 503s |
| `lb_retries` / `lb_try_duration` / `lb_try_interval` | `20` / `10s` / `250ms` | The retry loop outlasts the parking window, so a passive ejection costs latency instead of errors |

On your own load balancer: raise the dial timeout above the accept latency of your worst connection
burst, keep the ejection window shorter than the retry budget, and stagger node restarts so 10,000
nodes do not reconnect in the same millisecond.

---

## Config change awareness

`test/load/config_awareness.py` publishes new versions of `crawler/loadtest.json` and measures the
wake-up latency of its own watchers. Publisher and watchers live in one process, so both timestamps come
from one clock. `total` is measured from just before the publish RPC — the conservative figure;
`from_commit` from the moment the publish returned.

| Situation | Watchers woken | total p99 (worst round) | total max | from_commit p99 |
| --- | ---: | ---: | ---: | ---: |
| Idle instance, 200 probe watchers, 6 publishes | **1,200 / 1,200** | **46.1 ms** | **46.8 ms** | **38.4 ms** |

Zero poll errors, every watcher woken. Two orders of magnitude inside the 1 s target. The protocol this
measures is in [Configuration center → the watch protocol](./09-config-center.md).

---

## Where the time goes

Every acquire→report cycle is five Lua scripts. Sampling every Valkey command for 0.3 s under a
~1,000 cycles/s load — `SLOWLOG` with `slowlog-log-slower-than 0`, grouping the `EVALSHA` entries by
script SHA — attributes the cost. The "before" column is the same scenario on the same machine before
the hot-path optimisations:

| Script | Calls per cycle | Before (µs) | Now (µs) | | |
| --- | ---: | ---: | ---: | ---: | --- |
| `acquire.lua` | 1 | 104.2 | **68.3** | −34 % | pick, filter, lease |
| `observe.lua` | 1 per report | 71.7 | **43.5** | −39 % | worker state update |
| `release.lua` | 1 per released lease | 67.0 | **34.0** | −49 % | lease end |
| `ingest.lua` | 1 per batch | 16.3 | **12.2** | −25 % | append to the stream shard |
| `lease_retain.lua` | 1 per site per request | 12.0 | **10.3** | −14 % | keep reported leases readable until the worker gets to them |
| **Total per cycle** | | **271.2** | **168.3** | **−38 %** | |
| **Cycles/s per Redis thread** | | **3,690** | **5,940** | **+61 %** | |

Two scripts outside the cycle, sampled in the same window: `apply.lua` at 49.8 µs (one automatic
cooldown action) and `breaker_eval.lua` at 47.2 µs (a per-group sweep, rate-limited — not one per
report).

The 5,940 cycles/s per Redis thread is the ceiling the *scripts* impose. Every knee in this page is
lower than that, because the congestion loop starts before the thread is full.

### The two optimisations behind these figures

Both were proved with the micro-benchmark harness in `test/perf/`, which measures scripts directly
against Valkey with `INFO commandstats` deltas, a differential calibration of each Lua primitive, and
`SLOWLOG` percentiles. Its A/B blocks run the shipped scripts and frozen copies of the previous ones
interleaved in one process, because the run-to-run drift of identical code on identical data is ±8 % and
a sequential "before run, after run" cannot decide anything smaller.

**The scheduler path** — `acquire.lua`, `release.lua`, `renew.lua`, `reap.lua` and the shared prelude.
Server-side µs per call, best of three interleaved rounds of 3,000 calls on the 100,000 × 50 dataset:

| Block | Before | After |
| --- | ---: | ---: |
| `acquire.lua`, `candidate_sample: 32`, clean pool | 85.1 | **59.3** (−30.3 %) |
| `acquire.lua`, `candidate_sample: 32`, 50 % of candidates filtered | 102.7 | **72.2** (−29.7 %) |
| `acquire.lua`, `candidate_sample: 1`, clean pool | 67.6 | **55.6** (−17.8 %) |
| `release.lua`, no exclusive-push marker | 53.6 | **29.2** (−45.6 %) |
| `release.lua`, marker set by one other group | 96.3 | **60.1** (−37.6 %) |
| `reap.lua`, per expired lease at batch 100 | 20.7 | **14.1** (−31.8 %) |

One acquire and one release — the pair the hot path runs per request — cost 138.7 µs before and
**88.4 µs** after: −36 %. Note how little `candidate_sample` costs: acquire reads candidate state on
demand instead of bulk-loading the whole sample, so the difference between K=1 and K=32 is about 4 µs.

**The worker path** — `observe.lua`, `lease_retain.lua`, `apply.lua`, `breaker_eval.lua`:

| Block | Before | After |
| --- | ---: | ---: |
| `observe.lua`, success, no counters | 46.9 | **31.0** (−33.9 %) |
| `observe.lua`, `rate_limited`, 2 counters + 2 ban windows | 61.8 | **41.6** (−32.6 %) |
| `lease_retain.lua`, per lease, 100 leases of one site | 9.2 | **2.0** (−78.2 %) |
| `apply.lua`, automatic identity × endpoint cooldown | 47.3 | **34.4** (−27.2 %) |
| `breaker_eval.lua`, mode `eval`, 12-bucket window | 54.3 | **44.1** (−18.8 %) |

One report of a 100-report batch went from 58.7 µs to **35.6 µs** of Valkey CPU: −39 %.

An earlier fix, before the "before" column, explains the shape of both. `release.lua` used to restore
the ready-queue score of an identity in **every endpoint group of its client** on every lease end,
because some other group might have pushed the score out to an exclusive lease's expiry while the lease
was held. That was exactly **50 `ZSCORE` per acquire** on this 50-group dataset, whether or not any
group had pushed anything, and it made `release.lua` the most expensive script in the system at
+2.25 µs per endpoint group. The fix records the fact on the identity instead of rediscovering it: an
acquire that pushes an identity because of another group's exclusive lease marks the identity, and the
lease end only walks the groups the marker names. That took Valkey from 95,793 to 49,491 commands/s at
1,000 cycles/s, and the sustained two-instance rate from 1,678/s to 3,000/s.

Absolutes from `test/perf/` are about 1.4× cheaper than the Compose VM's, because they run against
Valkey on the developer machine; the end-to-end table above is the confirmation in the VM. Both numbers
exist on purpose: one proves the change, the other proves it survived integration.

---

## Past the knee: congestion collapse

The system does not degrade gracefully past its knee; it collapses. Recognising the signature matters
more than any individual number.

**The signature.** Acquire p99 jumps by an order of magnitude, `spinneret_report_lag_seconds` goes from
tens of milliseconds to seconds, `spinneret_acquire_total{result="exhausted"}` climbs sharply, and Redis
CPU is high while *useful* throughput is falling.

**The loop**, step by step:

1. Redis saturates, so the report worker falls behind;
2. leases are therefore not released, so identities stay leased;
3. acquires in the other 49 groups sample those leased identities, reject them, and push their
   ready-queue score forward — one `HGET` plus one `ZADD` each;
4. a failing acquire walks far more candidates than a succeeding one: measured at the two-replica
   4,000/s collapse, **220 Redis commands per acquire instead of 49**, and `EVALSHA` averaging
   **157 µs instead of 27 µs**;
5. which pushes Redis further into saturation.

**What it costs.** At the collapse Valkey burned 3.5–3.7 cores across its I/O threads to serve roughly
four times the commands the same offered load needs when healthy — and delivered a third of the useful
throughput. The per-command work of a failing acquire is cheaper than it used to be (97 µs against
212 µs before the optimisations), but the feedback itself is intact, and it is what sets every knee in
this page, not Valkey's CPU ceiling.

**It is metastable just below the knee.** In the region a little under the knee the system is stable
until something stalls it once — an AOF rewrite fork, a `docker stats` walk, a noisy neighbour — and
then it never recovers on its own. Once the loop starts, offered load has to drop below the knee for it
to unwind. That is why the AOF settings in [Valkey settings](#valkey-settings) mattered as much as they
did.

**Operationally:** keep offered load under about **80 % of the measured knee**, alert on
`spinneret_acquire_total{result="exhausted"}` and `spinneret_report_lag_seconds`, and leave admission
control on. The same signature from the symptom end, with the full metric list, is in
[Troubleshooting → Latency is high or throughput collapses](./18-troubleshooting.md#latency-is-high-or-throughput-collapses).

---

## Admission control

Admission control is the mechanism that turns that loop into ordinary backpressure. Each instance caps
how many `acquire.lua` calls it has in flight at Redis and sheds the excess **in the server**, before
any Redis command is issued. The whole point is that offered load above the knee is shed cheaply and
quickly with a retryable error, instead of multiplying concurrency at Redis.

### What was measured, and what was not

These are the gated figures. They were taken on the environment and dataset described above (one
site, 100,000 identities, 50 endpoint groups, warm), with `SPINNERET_ACQUIRE_FLEET_INFLIGHT=64`, and
each arm is a two-minute run whose numbers come from the server-side histograms.

| Replicas | Offered | Gate | Achieved cycles/s | Acquire p99 | Report lag p99 | Shed | Valkey |
| ---: | ---: | --- | ---: | ---: | ---: | ---: | --- |
| 1 | 4,500/s | on (limit 64) | **4,499/s** | 1.97 ms | 10.9 ms | 0.4/s | 1.65 cores, 215k cmd/s |
| 2 | 4,000/s | off | **4,000/s** | 1.66 ms | 8.4 ms | — | 1.71 cores, 192k cmd/s |
| 2 | 4,000/s | on (32 each) | **3,995/s** | 2.99 ms | 15.8 ms | 4.3/s | 1.79 cores, 193k cmd/s |
| 2 | 4,500/s | on (32 each) | **2,418/s** | 89 ms | above the top bucket | 1,881/s | 2.37 cores, 335k cmd/s |

Read them as three statements:

1. **The gate does not cost a single replica anything.** At the headline single-instance rate it
   admitted 4,499 of 4,500 offered cycles/s and shed 0.4/s, with acquire p99 at 1.97 ms. The limit
   was 64 — the whole fleet budget, because it was the whole fleet.
2. **At capacity, two replicas behave the same gated or ungated.** Both served ~4,000 cycles/s. The
   gate cost some tail: acquire p99 2.99 ms against 1.66 ms and report lag p99 15.8 ms against
   8.4 ms. That is the price of the permit accounting on a path whose healthy service time is a
   fraction of a millisecond, and it is the honest cost side of the ledger.
3. **Past capacity, the gate converts overload into shedding.** At 4,500/s — above what two
   replicas sustain on one Valkey — it served 2,418 cycles/s, shed 1,881/s as
   `unavailable`/`overloaded`, and kept acquire p99 finite at 89 ms. Throughput fell; it did not
   collapse, and no client saw a timeout.

**The two-replica ceiling moved, and not because of the gate.** Earlier rounds of this document
recorded two replicas sustaining 3,000 cycles/s and collapsing at 3,500. That does not reproduce:
re-measured from a rebuilt and warmed pool, two replicas serve 4,000 cycles/s with or without the
gate. The Lua hot-path work is the likely reason the knee moved; whatever the cause, the "two
replicas reach less than one" result in earlier rounds was at least partly a measurement artefact,
and it would have been easy to publish the gate as its cure.

**Not measured: two replicas past capacity with the gate off.** Every attempt was invalidated, and
the invalidations are worth writing down because they are the traps of this harness:

- **A collapsed run poisons the next one.** A run that ends in overload leaves leases held for their
  full TTL and ready-queue scores pushed out to lease expiry. A hundred seconds of sleep is not
  enough; runs started that way showed an `exhausted` count an order of magnitude above normal,
  which is the tell. Every arm above was preceded by `spnr rebuild` and a fresh warm-up.
- **Valkey restarting looks exactly like congestion collapse.** In one series Valkey restarted
  mid-run five times. On each restart it reloaded a 3.2 GB / 9.8-million-key dataset, which took
  **153 seconds** during which it accepted no connections — so the run recorded acquire p50 in the
  hundreds of milliseconds, `deadline_exceeded` from the 2 s Redis timeout, and throughput near
  zero. The giveaway is a *negative* Valkey command or CPU delta in `delta.json`, because the
  counters reset. Check for it before believing a collapse.
- **The A/B arm can revert itself.** `compose run k6` resolves the whole compose file to satisfy
  k6's `depends_on` chain, and a server whose resolved environment differs from its running
  container is recreated — so passing the arm to only the `up` call silently restores the default
  halfway through the run. `run.sh` now exports it, and the `acquire_shed: ['count<1']` threshold on
  the `off` arm exists to catch exactly this.

So the claim this page makes for the gate is the three statements above, and no more. "It prevents
the ungated collapse" is a reasonable expectation from the mechanism and from `internal/pkg/admit/`,
but it is not something measured here, and the recipe below is how to measure it.

### The two variables

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | `64` | Concurrent acquire scripts the **whole fleet** may have in flight at Redis, 0–65536. Each instance admits this divided by the live API instances it sees, clamped to `[4, 4096]`. `0` builds no gate at all and restores the unbounded behaviour |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | `0` (derive) | Pins this instance's own limit instead of dividing the fleet budget, 0–4096. A positive value also stops the heartbeat that counts peers |

Both are in [Configuration → Acquire admission control](./03-configuration.md#acquire-admission-control).

### How one attempt is decided

- The permit is taken **before any CPU work**, so a shed costs no allocation and no Redis command.
- A permit is weighted by `count`, so an `AcquireBatch` is charged for roughly the Lua work it really
  does rather than as one call.
- If no permit is free, a caller with wait budget parks in a FIFO wait room whose depth is **4× the
  limit**, floored at 32. A fresh arrival never barges past parked callers.
- A parked caller waits at most **50 ms**, and never longer than its own remaining `wait_ms`. Because
  every SDK defaults to `wait_ms = 0`, the common case is never parked at all: it is admitted if a
  permit is free right now and shed otherwise, in one mutex round trip.
- A shed returns reason `overloaded` (code `unavailable`, HTTP 503) with a retry hint jittered uniformly
  into **100–200 ms**. The jitter matters: both SDKs retry `unavailable` automatically, and an
  unjittered hint would return the whole shed population in lockstep.
- A shed attempt consumes one rung of the acquire wait ladder and sleeps **outside** the gate, and the
  time it spent waiting for a permit is credited against that rung — so a given `wait_ms` buys the same
  number of attempts with the gate as without it.
- A shed only surfaces as `overloaded` when **no** attempt reached Redis. Once the script has answered
  `EXHAUSTED` or `NO_PROXY`, that reason survives: with the gate on, `exhausted` means pool exhaustion
  only, never overload. That separation is the main diagnostic gain.

### How the budget is divided

Every API instance records its own timestamp in a sorted set in Redis every **2 s**, prunes members
whose heartbeat is older than the **60 s** live window, and reads back how many are left. There is no
feedback signal and nothing to oscillate. Each instance then sets its limit to
`SPINNERET_ACQUIRE_FLEET_INFLIGHT / live`, clamped to `[4, 4096]` — integer truncation on purpose, so
the fleet total can never exceed the configured budget.

**The live window is 30 beats, and that is deliberate.** A beat needs Redis, and the moment admission
control matters is the moment Redis is saturated — so beats are exactly then at risk of timing out.
This was measured: under a two-replica overload with a 10 s window, one instance missed enough beats
to be pruned by the other, which then divided the fleet budget by 1 and admitted all of it. The gate
widened precisely when it should have held, which is a positive feedback loop into the collapse the
gate exists to prevent. A graceful shutdown deregisters the instance itself, so the long window only
delays noticing a *crash*, and a crashed instance's stale membership makes the survivors narrower —
the safe direction. `spinneret_acquire_peer_beat_age_seconds` is how you see a registry that has
stopped converging.

The registry is best-effort in the rest of its behaviour. A Redis failure keeps this instance's last
observed count, so its own limit can only stay put or narrow, and the failure is logged rather than
returned to the request path: a registry problem must not be able to take an instance down. On
shutdown an instance removes itself, so the survivors widen their share on their next beat instead of
after the live window. A limit change is logged once, on change only.

Three consequences worth planning for:

- **Every API instance needs a distinct `SPINNERET_INSTANCE_ID`**, or they collapse into one registry
  member and each admits the *whole* budget. The default is host-derived with a random suffix
  (`<hostname>-<3 random bytes>`), so it is distinct under Compose; the peer registry additionally
  substitutes a random id and logs a warning if it is ever handed an empty one.
- **Pin on every instance or on none.** A pinned instance does not register, so the deriving instances
  divide by too small a count *and* the pinned allocation is added on top.
- **The per-instance floor of 4 eventually wins.** Past 16 replicas at the default budget the division
  truncates to the floor, and the fleet total grows as 4 × replicas again. Watch
  `sum(spinneret_acquire_inflight_limit)` against the configured budget: at 16 replicas the sum is
  the budget — 64/16 = 4, so 16 × 4 = 64 — and at 17 replicas it is 17 × 4 = 68. Once
  `sum(spinneret_acquire_inflight_limit)` exceeds `SPINNERET_ACQUIRE_FLEET_INFLIGHT` the floor has
  overtaken the budget, and the answer is to shard Redis or lower the fleet budget.

Because the budget is divided among live instances, extra replicas add **server** capacity — long-poll
capacity, report processing, availability — without adding Redis concurrency. Redis capacity is what
adds acquire throughput.

### Measuring it yourself

`test/load/run.sh` has an A/B arm that switches the gate before the run, so the pair is two runs of the
same image differing in one server variable — no rebuild, no machine drift:

```bash
export LOADTEST_TOKEN=$(python3 -c "import json;print(json.load(open('/tmp/seed.json'))['token'])")

# Baseline arm: no gate is constructed at all.
ADMISSION=off test/load/run.sh acq-5000-off acquire_report.js ACQUIRE_RATE=5000 DURATION=2m
# Gated arm: the fleet-wide budget, divided by the live instance count.
ADMISSION=on  test/load/run.sh acq-5000-on  acquire_report.js ACQUIRE_RATE=5000 DURATION=2m
```

`ADMISSION=on` exports `SPINNERET_ACQUIRE_FLEET_INFLIGHT` (from `ACQUIRE_FLEET_INFLIGHT`, default `64`),
`off` exports `0`; both recreate the `spinneret` service with `--no-build` and wait `ADMISSION_SETTLE`
seconds (default 15) for the load balancer to re-resolve the replicas. The arm changes one threshold:
with `ADMISSION=off` a shed fails the run, which catches an arm that did not actually switch. With the
gate on, sheds never fail the run unless `SHED_MAX` is set — `SHED_MAX=0` is how you assert "this rate
must be served without shedding at all":

```bash
ADMISSION=on SHED_MAX=0 test/load/run.sh acq-5000-gated acquire_report.js \
  ACQUIRE_RATE=5000 DURATION=2m REPORT_MODE=none
```

The scenario reads the `Spinneret-Reason` header so a shed (`acquire_shed`) is counted separately from a
genuinely empty pool (`acquire_exhausted`), an open breaker and a real failure. Server side, `run.sh`
snapshots `spinneret_acquire_admission_total{result}`, `spinneret_acquire_admission_wait_seconds`,
`spinneret_acquire_script_seconds` and the gate gauges `spinneret_acquire_inflight`,
`spinneret_acquire_queued`, `spinneret_acquire_inflight_limit` and `spinneret_acquire_peers`.

Reading them: **`queued` rising while sheds are still zero is the earliest warning that the knee is
near** — but only for clients that pass `wait_ms > 0`, since a caller with no wait budget is never
parked. `overloaded` rising while Redis CPU is *below* its ceiling and `spinneret_acquire_script_seconds`
p99 is normal means the limit is narrower than what Redis can take: raise the fleet budget, or pin a
measured value. `overloaded` rising with Redis CPU low but the script p99 *spiked* means Redis is
stalled rather than narrow — fix Redis, and do not raise the budget, which would re-open the collapse
loop. The full decision table is in
[Troubleshooting](./18-troubleshooting.md#latency-is-high-or-throughput-collapses).

---

## Valkey settings

Five settings in `deploy/compose/docker-compose.yml`. The first three are each justified by a measurement
below; the last two are safety margins:

| Setting | Valkey default | Here | Why |
| --- | --- | --- | --- |
| `io-threads` | 1 | **4** (`VALKEY_IO_THREADS`) | Little throughput, a great deal of tail latency — see [below](#valkey-io-threads) |
| `save` (RDB) | `3600 1 300 100 60 10000` | **off** (`--save ""`) | The AOF is the durability mechanism. The RDB points forked a second time for the same data, once a minute under load |
| `auto-aof-rewrite-percentage` / `-min-size` | 100 / 64 MiB | **300 / 1 GiB** | With the defaults a rewrite fired every ~52 s at 4,000 cycles/s |
| `maxmemory-policy` | `noeviction` | **`noeviction`** | Kept explicit. Evicting a lease hash or a ready queue silently corrupts the hot state; Redis is a working set here, not a cache |
| `maxclients` | 10,000 | **20,000** | Headroom for the connection pools of several server instances plus a CLI session. Node long polls do not consume a Redis connection — a blocked watcher holds none — so this is margin, not a measured requirement |

`appendonly yes` and `appendfsync everysec` are unchanged: durability is not traded away here.

**AOF rewrites were the largest stability finding.** Spinneret's hot state is mostly short-lived keys —
lease hashes, dedup markers, stream entries — so the AOF grows far faster than the dataset it describes
and the rewrite trigger fires constantly: **23 rewrites in 20 minutes at 4,000 cycles/s**, one every
52 s, each forking a ~1 GiB process and writing ~290 MiB, with a background RDB save on top. Each
rewrite is a multi-second I/O stall in a laptop VM, and the region just below the knee is metastable:
one stall is enough to start the congestion loop, which never recovers. The symptom was runs at rates
that had been clean an hour earlier collapsing from the first second. With `--save ""`,
`--auto-aof-rewrite-percentage 300` and `--auto-aof-rewrite-min-size 1gb`, the identical ladder that had
been collapsing at 2,500 cycles/s ran clean to 4,000/s and the 4,500/s two-minute run passed. Nothing
else changed between those two runs.

**ClickHouse needs capping on a shared host.** Out of the box it sizes itself for the whole machine —
`max_server_memory_usage` at 90 % of RAM, the mark cache alone allowed 5 GiB. In this VM that made it
grow past 1.1 GiB and get picked by the kernel OOM killer during a 5,000/s run, which froze the whole
VM. `deploy/compose/config/clickhouse-limits.xml` sets:

| Setting | Value | Why |
| --- | --- | --- |
| `max_server_memory_usage` | 2.5 GiB | A safety net, not a diet. It is compared against process RSS, which idles near 1.2 GiB for this image whatever the caches are — set *below* that and ClickHouse does not shrink, every `INSERT` fails with `MEMORY_LIMIT_EXCEEDED` and the report worker stalls. 1.5 GiB was too tight: the request explorer over ~16 M rows failed with code 241 |
| `mark_cache_size` | 64 MiB | Default 5 GiB. The hot path is inserts; marks are re-read cheaply |
| `uncompressed_cache_size`, `mmap_cache_size`, `compiled_expression_cache_size` | 0 | They only pay off for repeated large scans this deployment does not run |
| `jemalloc_enable_background_threads` | true | Return freed arenas to the OS instead of holding them for reuse |

`background_pool_size` is deliberately left alone: lowering it to 4 makes ClickHouse refuse to start,
because MergeTree's sanity check requires `background_pool_size × background_merges_mutations_concurrency_ratio`
to exceed `number_of_free_entries_in_pool_to_execute_mutation`. The caches are where the memory is.

---

## Valkey io-threads

Valkey executes every command — Lua included — on the main thread, so extra I/O threads move only socket
read and write. Measured end to end, they buy very little throughput and a great deal of tail:

| One instance, `max_concurrent_leases: 4`, 60 s | io-threads=1 | io-threads=4 |
| --- | ---: | ---: |
| 6,000/s offered → achieved | 5,955/s | **5,999/s** |
| 6,000/s → acquire p99 | 18.9 ms | **4.09 ms** |
| 6,000/s → report lag p99 | 4,656 ms | **41 ms** |
| 6,000/s → Valkey CPU | 0.91 cores | 1.92 cores |
| 7,000/s offered → achieved | 6,765/s | **6,976/s** |
| 7,000/s → acquire p50 / p99 | 40.0 ms / 237 ms | **0.87 ms / 9.88 ms** |
| 7,000/s → Valkey CPU | 0.95 cores | 1.91 cores |

The trade is about one extra core of Valkey CPU for a tail that stays inside the targets 1,000–2,000
cycles/s further up the curve. On a host where Redis has cores to spare, take it; `VALKEY_IO_THREADS=1`
restores the single-threaded behaviour. It does not raise the *sustained* rate much: at the knee the
main thread is still the wall, which is why the 4,500/s and 5,500/s figures are only a little above what
`io-threads 1` reached.

A closed-loop micro-benchmark originally concluded that I/O threads were not worth it. It was measuring
throughput, which they barely change. The end-to-end run is what showed what they actually buy.

---

## Redis sizing

Measured with `MEMORY USAGE` on the live dataset (Valkey 8, 100,000 identities, 50 endpoint groups):

| Structure | Measured | Per unit |
| --- | ---: | --- |
| Ready queue, 100,000 members | 6,457,568 B | **64.6 B per (identity × endpoint group)** → ≈ 62 MiB per 1M entries |
| Health state | 84,072 B for 1,177 entries | ≈ 71 B per warmed (identity × endpoint group) |
| Identity | 201 B average, 296 B max over 400 sampled | per identity |
| Report dedup marker | 80 B | per report, for `SPINNERET_REPORT_DEDUP_TTL` |
| Ended lease | ≈ 290 B | per lease, kept `max(late report window, dedup TTL)` after it ends |
| Report stream entry | ≈ 440 B | per entry |

A freshly rebuilt dataset of that size measures **351 MiB** in total. What dominates in production is
not the dataset, it is the traffic-proportional state:

```text
working set ≈ 350 MiB                            (dataset)
            + up to 345 MiB                      (health entries, once every identity is warm everywhere)
            + reports/s  × dedup_TTL × 80 B      (dedup markers)
            + acquires/s × dedup_TTL × 290 B     (ended lease hashes)
            + backlog_entries × 440 B            (report streams)
```

At 4,500 cycles/s with the default 1 h dedup TTL that is 1.2 GiB of dedup markers plus 4.4 GiB of ended
lease hashes — far more than the dataset. Load runs show it directly: 351 MiB after a rebuild, ~950 MiB
after two minutes at 4,500 cycles/s, and every byte of that difference is waiting out its hour.

**Sizing `SPINNERET_REPORT_DEDUP_TTL` to your nodes' retry behaviour is the single biggest memory
lever.** Worked example, 2,000 cycles/s with a 15-minute dedup TTL and a 100,000 × 50 dataset:

```text
350 MiB  dataset
345 MiB  health entries at full warmth
137 MiB  dedup markers      (2,000 × 900 s × 80 B)
498 MiB  ended lease hashes (2,000 × 900 s × 290 B)
  ~0     streams, while the workers keep up
------
≈ 1.3 GiB, so provision 3 GiB and alert at 2
```

Provision at least twice the computed figure: a worker outage turns the stream term from nothing into
`backlog × 440 B`, and `noeviction` means Redis refuses writes rather than quietly dropping state.

---

## Tuning, in the order it pays

1. **`max_concurrent_leases`** — the biggest per-site lever. `1` (exclusive) sustains 4,500 cycles/s per
   instance; `4` sustains 5,500 *and* degrades far more gracefully, because a leased identity no longer
   has to be pushed out of the ready queue of every other group of the client — which is the amplifier
   of the collapse. **Costs:** up to four concurrent requests per identity, which some sites will not
   tolerate. **Verify:** the achieved rate at a fixed offered rate, and how far above it acquire p99
   stays inside 5 ms. Lives in the [rotation policy](./08-policies.md).
2. **`SPINNERET_ACQUIRE_FLEET_INFLIGHT`** — the fleet's acquire concurrency at Redis. **Buys:** offered
   load above the knee is shed in the server with a retryable `overloaded` instead of collapsing Redis,
   and `exhausted` goes back to meaning an empty pool. **Costs:** a budget set too low sheds work Redis
   could have done. **Verify:** `spinneret_acquire_admission_total{result=~"shed.*"}` against Redis CPU
   and `spinneret_acquire_script_seconds` p99 — sheds with Redis CPU low and the script p99 normal mean
   the budget is too small. Start at the default 64, raise it in steps while Redis CPU has headroom, and
   pin `SPINNERET_ACQUIRE_MAX_INFLIGHT` on **every** API instance once you have measured a value.
3. **`SPINNERET_REPORT_DEDUP_TTL`** — the default 1 h costs 80 B per report and keeps each ended lease
   hash (~290 B) alive for the same hour. Nodes retry within seconds, not hours. **Buys:** 4–12× less
   Redis memory at 5–15 minutes. **Costs:** a node that retries a report later than the TTL applies it
   twice. **Verify:** `INFO memory` before and after, and
   `spinneret_report_ingest_total{result="duplicated"}` staying non-zero — that counter is what proves
   the window still catches your nodes' retries.
4. **Redis capacity, not replicas, for acquire throughput.** Every key is hash-tagged by site or report
   shard, and `SPINNERET_REDIS_ADDRS` puts the client into cluster mode, so a Redis Cluster multiplies
   the 5,940 cycles/s per thread across primaries. This is the lever that raises the *ceiling*. Budget
   roughly one Redis **primary** per **4,500** acquire→report cycles/s with exclusive leases, or
   **5,500/s** with `max_concurrent_leases: 4` — the script cost allows 5,940, and the difference is
   congestion headroom you want to keep. **Costs:** operating a cluster. **Verify:** Valkey CPU per
   primary, and that the knee moved.
5. **The bundled Valkey settings.** `io-threads 4`, `--save ""` with `appendonly yes`, the AOF rewrite
   thresholds, `maxmemory-policy noeviction`. **Buys:** the difference between a ladder that collapses
   at 2,500 cycles/s and one that runs clean to 4,000. **Verify:** `INFO persistence` —
   `aof_rewrite_in_progress` and the rewrite count over an hour of load.
6. **`SPINNERET_REPORT_SHARDS`** — bounds report *processing* parallelism, because a shard has exactly
   one owner. Keep 2–4 shards per worker instance. **Costs:** it is **fixed for the life of a
   deployment** — lease ids encode their shard, so changing it strands in-flight leases and needs a
   maintenance window ([Operations](./16-operations.md#changing-the-shard-count)). **Verify:** growing
   `spinneret_stream_pending` with busy workers means the workers are the bottleneck; evenly spread
   pending entries with idle worker CPU means too few shards.
7. **`SPINNERET_STREAM_MAXLEN`** — shard owners trim to the consumer position, so the default 1,000,000
   per shard is the cap on a backlog the workers cannot drain, not a permanent buffer. Size it to the
   outage you want to survive: `rate × seconds / shards`. At 20,000 reports/s and 60 s that is 75,000
   per shard. **Costs:** entries past the cap are dropped silently, so a cap that is too low loses
   reports during exactly the incident you wanted them for. **Verify:** `spinneret_stream_pending`
   during a deliberate worker restart.
8. **Lease TTLs.** `lease_ttl` (rotation policy default `120s`, range `5s`–`30m`) decides how long a
   crashed node's identity stays unavailable, and how long an overloaded run keeps poisoning the ready
   queues. Shorter recovers faster and costs more `Renew` traffic and more reaper work; longer is
   cheaper and recovers slower. `max_lease_lifetime` (default 30 m) caps renewals.
   `SPINNERET_LATE_REPORT_WINDOW` (default `10m`) decides how long an ended lease hash — ~290 B — is
   kept so a late report can still be recorded. **Verify:** `spinneret_lease_reaped_total{kind}`; a
   large `expired` share means TTLs are longer than your nodes' real hold time.
9. **Endpoint groups by policy need, not by URL count.** Per-identity state is per endpoint group:
   ~65 B of ready queue plus ~71 B of health each. 50 groups × 100,000 identities is 5M ready-queue
   entries (≈ 310 MiB), and under exclusive leases every extra group is one more competitor for the same
   identity.
10. **Replicas for availability, long polls and report processing** — 10,000 measured watchers per
    instance (`SPINNERET_MAX_WATCHERS` caps it at 20,000), and each instance owns a share of the stream
    shards. `SPINNERET_ROLE` is the finer lever: `worker` instances add report-processing capacity and
    `api` instances add acquire and long-poll capacity, so you can grow one without the other
    ([Operations → the api/worker split](./16-operations.md#the-apiworker-split)). With admission control
    on, extra replicas do not add Redis concurrency; with it off, they subtract throughput.
11. **`candidate_sample`: leave it at 32.** Acquire reads candidate state on demand rather than
    bulk-loading the sample, so the difference between K=1 and K=32 is about 4 µs. Lowering it buys
    almost nothing and costs extra rounds under contention.
12. **The payload cache: leave it on.** `SPINNERET_PAYLOAD_CACHE` (default `true`) with
    `SPINNERET_PAYLOAD_CACHE_SIZE` (default `200000`) is an unstated condition of every per-acquire
    figure in this page: the measured dataset is 100,000 identities, so the whole working set fits in the
    default cache and no measured acquire paid for a decrypt. **Costs:** `false` keeps decrypted payloads
    out of process memory — a hardening choice — at one decrypt per acquire; a size below the working set
    pays the same cost on every miss. **Verify:** server CPU and acquire p50 at a fixed offered rate
    before and after ([Configuration reference](./03-configuration.md)).
13. **The database pool was never a factor.** PostgreSQL stayed under 3 % CPU in every scenario: the hot
    path is Redis-only and PostgreSQL sees batched writes. Raise `SPINNERET_DATABASE_MAX_CONNS` with the
    replica count anyway, keeping `replicas × max_conns + headroom < max_connections` (300 in the
    Compose stack).
14. **ClickHouse limits on a shared host** — `config/clickhouse-limits.xml`, and keep
    `max_server_memory_usage` above the image's idle RSS. It is not on the hot path, but an OOM-killed
    ClickHouse stalls the report worker.
15. **Load balancer health checks.** With few upstreams, ejecting on the first failure needs a parking
    window shorter than the retry budget, or one slow dial becomes an outage — see
    [Concurrent config watchers](#concurrent-config-watchers).

**Profiling.** `SPINNERET_PPROF_ADDR` (for example `127.0.0.1:6060`) serves `/debug/pprof/` on its own
listener. Off by default, never on the API or metrics listener, and the configuration is rejected if the
address equals either of those. The endpoints are unauthenticated and expose heap contents and goroutine
stacks: never publish the port.

---

## What is an environment limit and what is not

So that nobody mistakes the laptop for the software:

| Observation | Environment or product? |
| --- | --- |
| One instance stops at 4,500–5,500 cycles/s | **Product**: the congestion loop starts before Valkey's main thread is full (the scripts allow 5,940 cycles/s per thread) |
| Two instances reached less than one | **Product**: unbounded per-instance concurrency against one Redis. That is what [admission control](#admission-control) addresses |
| A failing acquire costs ~5× a succeeding one | **Product**: the amplifier of every collapse in this page |
| A cold (freshly seeded or rebuilt) site collapsing at 2,500 cycles/s | **Product**, mild: a uniform initial ready-queue score correlates all 50 groups. Traffic fixes it within about a million acquires |
| Collapses that appeared and disappeared between identical runs | **Environment**, with a **product-adjacent** trigger since fixed: AOF rewrite forks every 52 s in a laptop VM. Tuned in `deploy/compose` |
| ClickHouse OOM-killed, freezing the VM during a 5,000/s run | **Environment**: a 7.75 GiB VM shared with the load generator and an unrelated stack. Fixed by capping its caches |
| 10,000 watchers failing through the load balancer | **Environment / load-balancer configuration**, not the server |
| The load generator and the load balancer at ~1 core each during high-rate runs | **Environment**: the load generator shares the VM with the system under test |
| `docker stats` turning a 1.9 ms p99 into 11.9 ms | **Environment / measurement**: use `MID_STATS=0` |
| Server instances never exceeded 1.7 of 16 cores | Headroom |

---

## Reproducing

Requirements: the Compose stack running, and Python 3 on the host for the tooling. Paths are relative to
the repository root, and a load-test session uses the repo-root Compose invocation below throughout,
because `test/load/run.sh` builds exactly that one itself. The `./spnrctl` wrapper is not a substitute: it
exists only inside a deployment created by the installer and pins that deployment's own Compose project.

```bash
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"

# 1. Seed the dataset and capture the node token.
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50 > /tmp/seed.json
export LOADTEST_TOKEN=$(python3 -c "import json;print(json.load(open('/tmp/seed.json'))['token'])")

# 2. Per-instance figures need one replica. SPINNERET_REPLICAS must be exported for *every*
#    compose call of the session, or the next one scales the service back to the .env value (2).
export SPINNERET_REPLICAS=1
$COMPOSE up -d --wait

# 3. Warm the site. Settle between steps: a run that ended in overload leaves 60 s leases and
#    ready scores pushed up to 60 s into the future.
for r in 1500 2500 3500 4000; do
  MID_STATS=0 test/load/run.sh "warm-$r" acquire_report.js ACQUIRE_RATE=$r DURATION=45s
  sleep 70
done

# 4. The per-instance acquire->report knee: the headline two-minute run.
MID_AFTER=70 MID_STATS=0 test/load/run.sh acq1-4500 acquire_report.js ACQUIRE_RATE=4500 DURATION=2m

# 5. Acquire alone. Raise max_concurrent_leases first, because leases accumulate for their full
#    TTL when nothing releases them.
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

# 7. Concurrent watchers, direct to the instance (the load balancer is the bottleneck here).
MID_AFTER=45 test/load/run.sh watch1-10000 watch_config.js \
  WATCHERS=10000 DURATION=90s RAMP_S=25 SPINNERET_URL=http://spinneret:8080

# 8. Config change awareness: publisher and watchers in one process, so one clock.
python3 test/load/config_awareness.py --watchers 200 --publishes 6 --interval 3

# 9. Per-script cost: sample every command for 0.3 s under a ~1,000 cycles/s load and group the
#    EVALSHA entries by script SHA (the table in "Where the time goes").
MID_STATS=0 test/load/run.sh slowlog acquire_report.js ACQUIRE_RATE=1000 DURATION=60s &
sleep 40 && $COMPOSE exec -T valkey valkey-cli config set slowlog-log-slower-than 0
# ... 0.3 s later: `slowlog get`, then restore slowlog-log-slower-than to 10000.

# 10. Back to the normal topology.
unset SPINNERET_REPLICAS
$COMPOSE up -d --wait
```

Each run writes `before.json`, `mid.json`, `after.json`, `delta.json`, `gauges.json`, `k6.txt` and
`k6.json` under `.loadtest/<name>/` (git-ignored). `test/load/metrics.py diff a.json b.json` prints the
server-side deltas of any two snapshots, including the per-Valkey-command CPU attribution.
`test/load/README.md` documents every scenario and its variables; `test/perf/README.md` documents the
per-script micro-benchmarks and the frozen baselines they compare against.

The warm-then-measure recipe in three lines, because it is the part people skip:

1. **Warm** — the ladder in step 3, about a million acquires, until the 50 ready queues are decorrelated.
2. **Settle** — wait until the ready queues are due again
   (`ZCOUNT sp:{s<site key>}:rdy:<endpoint group key> -inf <now>` back at the cardinality) and
   `ZCARD sp:{s<site key>}:lsexp` is near zero. Both keys are built from the **numeric** site and
   endpoint-group keys, not their names;
   [Troubleshooting](./18-troubleshooting.md#the-pool-drains-and-never-recovers) shows where to read the
   numeric site key.
3. **Measure** — one two-minute run at the rate you intend to quote, with `MID_STATS=0`.

---

## Housekeeping

A load test leaves traffic-proportional Redis state behind — dedup markers, ended lease hashes, stream
entries — which expires on its own within `SPINNERET_REPORT_DEDUP_TTL`, plus ClickHouse rows that do
not: roughly 2 GiB after an afternoon of testing. On a small machine, reclaim it between runs. Skipping
this is what turned a repeat of a clean run into a collapse, with Valkey OOM-killed mid-run.

```bash
# Trim the report streams (test data only; drops any unprocessed backlog).
$COMPOSE exec -T valkey valkey-cli eval \
  "local n=0 for i=0,15 do n=n+redis.call('XTRIM','sp:{r'..i..'}:stream','MAXLEN',5000) end return n" 0

# Drop report dedup markers.
$COMPOSE exec -T valkey sh -c \
  "valkey-cli --scan --pattern 'sp:{r*}:dd:*' --count 5000 > /tmp/k; xargs -a /tmp/k -n 1000 valkey-cli unlink"

# Analytics rows from load runs.
$COMPOSE exec -T clickhouse clickhouse-client --user spinneret --password "$CLICKHOUSE_PASSWORD" \
  -q "TRUNCATE TABLE spinneret.report_events; TRUNCATE TABLE spinneret.lease_events"
```

**Warning. Never unlink the lease hashes** (`sp:{s<site key>}:ls:*`). A lease hash is what decrements the
identity's active-lease counter when the lease ends; deleting a live one leaks that counter and the
identity never becomes available again. Doing it once here left 97,520 of 100,000 identities permanently
leased, and every following run collapsed into `resource_exhausted`. The same applies to trimming a
stream that still holds unprocessed `release: true` reports.

The repair is to unlink the whole site prefix and rebuild — `spnr rebuild --site` on its own does
**not** reset runtime counters, by design, because a rebuild must not drop live leases:

```bash
$COMPOSE exec -T valkey sh -c \
  "valkey-cli --scan --pattern 'sp:{<site-tag>}:*' --count 5000 > /tmp/k; xargs -a /tmp/k -n 1000 valkey-cli unlink"
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild --site loadtest
```

A rebuilt site is cold, so warm it again before measuring.

---

## Next

- [Configuration reference](./03-configuration.md) — every variable named on this page, with its range.
- [Operations → Scaling](./16-operations.md#scaling) — replicas, roles, shards, and what to watch.
- [Observability and alerting](./12-observability.md) — the metrics, and the thresholds worth alerting
  on.
- [Troubleshooting → Latency is high or throughput collapses](./18-troubleshooting.md#latency-is-high-or-throughput-collapses)
  — the same signature, from the symptom end, with the full admission-control metric table.
- [Policies](./08-policies.md) — where `max_concurrent_leases`, `lease_ttl` and `candidate_sample` live.
- [Contributing](./20-contributing.md) — how to run `test/perf/` and prove an optimisation.
