# Load tests (k6)

Scenarios for the v0.1 performance targets, run against the
Connect JSON API of the `deploy/compose` stack. The measured results, the analysis and the
tuning recommendations live in [`documents/en/17-performance.md`](../../documents/en/17-performance.md)
([中文](../../documents/zh/17-performance.md)).

| File | Scenario | Design target |
| --- | --- | --- |
| `acquire_report.js` | Acquire → Report(release) at a constant arrival rate | Acquire p99 < 5 ms server-side, ≥ 5,000/s per instance |
| `report_ingest.js` | Batched report ingest against long leases | ≥ 20,000 reports/s per instance, report → state update p99 < 200 ms |
| `watch_config.js` | Concurrent WatchConfig long polls | ≥ 10,000 watchers per instance |
| `config_awareness.py` | Publish → watcher wake-up latency | Config change awareness < 1 s |
| `run.sh` | Runs one scenario between server-side metric snapshots | — |
| `metrics.py` | `/metrics` + Valkey snapshots, deltas, histogram quantiles, per-command CPU | — |
| `tune.py` | Republishes the seeded rotation policy with different values | — |

k6 latencies include the Docker network and the Caddy load balancer; the authoritative
latency is the server-side histogram (`spinneret_acquire_duration_seconds`,
`spinneret_report_lag_seconds`), which `run.sh` reads from each replica.

## Running

```bash
# 1. Seed the test dataset (100k identities x 50 endpoint groups) and print a node token.
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate \
  seed --site loadtest --identities 100000 --groups 50
export LOADTEST_TOKEN=spn_...

# 2. Run a scenario with metric snapshots around it (results in .loadtest/<name>/).
test/load/run.sh acq-3000 acquire_report.js ACQUIRE_RATE=3000 DURATION=60s

# ... or run k6 directly, without the snapshots.
LOADTEST_TOKEN=$LOADTEST_TOKEN K6_SCRIPT=acquire_report.js \
  docker compose -f deploy/compose/docker-compose.yml --profile loadtest run --rm \
  -e ACQUIRE_RATE=3000 -e DURATION=60s k6
```

`run.sh` writes `before.json`, `mid.json` (sampled under load, with `docker stats`),
`after.json`, `delta.json`, `gauges.json`, `k6.txt` and `k6.json` into
`${OUT_DIR:-.loadtest}/<name>/`, and prints a one-line summary of each.

For per-instance figures, export `SPINNERET_REPLICAS=1` for **every** compose command of the
session (including `run.sh`, which calls compose itself) and re-run `up -d --wait`; otherwise
the next compose command scales the service back to the value in `deploy/compose/.env`.

## Variables

Common: `SPINNERET_URL` (default `http://lb:8080`; set it to `http://spinneret:8080` to bypass
the load balancer), `SPINNERET_TOKEN`, `SITE` (`loadtest`), `CLIENT` (`web`), `GROUPS` (`50`).

| Script | Variables |
| --- | --- |
| `acquire_report.js` | `ACQUIRE_RATE`, `DURATION`, `REPORT_MODE` (`release`, `keep`, `none`), `PRE_VUS`, `MAX_VUS`, `ADMISSION` (`on`, `off`), `SHED_MAX` |
| `report_ingest.js` | `BATCH`, `REPORT_RATE`, `DURATION`, `LEASES`, `PRE_VUS`, `MAX_VUS` |
| `watch_config.js` | `WATCHERS`, `POLLS_PER_VU`, `DURATION`, `WATCH_TIMEOUT_MS`, `RAMP_S`, `CONFIG_GROUP`, `CONFIG_KEY` |
| `run.sh` | `LOADTEST_TOKEN` (required), `OUT_DIR`, `MID_AFTER` (seconds before the mid-run snapshot), `MID_STATS` (`0` to skip the `docker stats` part of it), `ADMISSION`, `ACQUIRE_FLEET_INFLIGHT`, `ADMISSION_SETTLE` |

Each scenario prints a readable summary plus a machine-readable block between
`---K6SUMMARY---` markers, which `run.sh` extracts into `k6.json`.

## Acquire admission control

The server bounds how many `acquire.lua` calls one instance may have in flight at Redis and
sheds the rest with `unavailable` / `Spinneret-Reason: overloaded` before they reach Redis.
Both halves of the measurement need to be visible, so `acquire_report.js` reads the reason
header (`connectReason` in `lib.js`) and splits what used to be one `acquire_unavailable`
bucket:

| Counter | Connect code | `Spinneret-Reason` | Meaning |
| --- | --- | --- | --- |
| `acquire_ok` | — | — | Lease granted. |
| `acquire_shed` | `unavailable` | `overloaded` | Admission control refused the request; **no Redis command was issued**. A saturation signal, not an error. |
| `acquire_breaker_open` | `unavailable` | `circuit_open` | The endpoint group's breaker is open. |
| `acquire_site_paused` | `unavailable` | `site_paused` | The site is paused. |
| `acquire_unavailable` | `unavailable` | anything else | Unclassified `unavailable` — including one from the load balancer, which sends no reason header. |
| `acquire_exhausted` | `resource_exhausted` | `no_identity_available` | The identity pool is genuinely empty. |
| `acquire_no_proxy` | `resource_exhausted` | `no_proxy_available` | No usable proxy for the chosen identity. |
| `acquire_failed` | anything else | — | A real failure; the `acquire_failed: ['count<1']` threshold still fails the run. |

A shed is deliberately **not** counted as `acquire_failed`: it is the correct answer to
offered load above the knee, and it carries `Spinneret-Retry-After-Ms`.

Server side, `run.sh` now snapshots `spinneret_acquire_admission_total{result}` (`immediate`,
`queued`, `shed_queue_full`, `shed_timeout`, `shed_canceled`),
`spinneret_acquire_admission_wait_seconds`, `spinneret_acquire_script_seconds` (the Lua round
trip alone) and the gate gauges `spinneret_acquire_inflight`, `spinneret_acquire_queued`,
`spinneret_acquire_inflight_limit` and `spinneret_acquire_peers`. They appear per instance in
`delta.json` / `gauges.json` and in the `admission` line of the run summary. `queued` rising
while sheds are still zero is the earliest warning that the knee is near.

### A/B on one build

`ADMISSION` switches the gate before the run, so the before/after pair is two runs of the same
image differing in one server variable — no build and no machine drift:

```bash
# Baseline arm: SPINNERET_ACQUIRE_FLEET_INFLIGHT=0 constructs no gate at all.
ADMISSION=off test/load/run.sh acq-5000-off acquire_report.js ACQUIRE_RATE=5000 DURATION=2m
# Gated arm: the fleet-wide budget, divided by the live instance count.
ADMISSION=on  test/load/run.sh acq-5000-on  acquire_report.js ACQUIRE_RATE=5000 DURATION=2m
```

`run.sh` exports `SPINNERET_ACQUIRE_FLEET_INFLIGHT` (`ACQUIRE_FLEET_INFLIGHT`, default `64`,
for `on`; `0` for `off`), recreates the `spinneret` service with `--no-build`, waits
`ADMISSION_SETTLE` seconds (default `15`) for the load balancer to re-resolve the new replicas,
and forwards the arm to k6 as `ADMISSION`. Leaving `ADMISSION` unset does not touch the stack
and lets the scenario tolerate sheds, which is what every other run wants.

The export matters and is not a style choice. `compose run k6` resolves the whole compose file
to satisfy k6's `depends_on` chain (`k6` → `lb` → `spinneret`), and a server whose *resolved*
environment differs from its running container gets recreated — so passing the arm to only the
`up` call puts the stack back to the default halfway through the run, and the arm silently
measures the default. Every compose invocation in `run.sh` has to see the same value.

The arm changes one threshold. With `ADMISSION=off` the server cannot shed, so
`acquire_shed: ['count<1']` fails the run if it does — that catches an arm that did not
actually switch, and it is what caught exactly the recreate bug described above. With the gate on, sheds are counted and never fail the run unless `SHED_MAX`
is set: the "no shedding at this rate" runs use `SHED_MAX=0`.

```bash
# No single-replica regression: 5,000/s must be served with the gate on and shed nothing.
ADMISSION=on SHED_MAX=0 test/load/run.sh acq-5000-gated acquire_report.js \
  ACQUIRE_RATE=5000 DURATION=2m REPORT_MODE=none
```

Both arms need `spinneret` recreated, so export `SPINNERET_REPLICAS` for the whole session as
described above if you are measuring per-instance figures.

## Getting numbers that mean something

Three things make the difference between a measurement and a story. All three are documented
with the evidence in [Performance and tuning](../../documents/en/17-performance.md).

* **`MID_STATS=0` for any run near the knee.** The mid-run snapshot otherwise calls
  `docker stats`, which walks every container of the machine; on Docker Desktop that stalled
  the system under test for ~5 s at 4,000 cycles/s and turned a 1.9 ms p99 into 11.9 ms.
* **Warm the site first.** `spnr seed` and `spnr rebuild` give every identity the same
  ready-queue score in every endpoint group, so all 50 groups contend on the same head of the
  queue. A cold site collapsed at 2,500 cycles/s where the warmed one carried 4,500/s. Run the
  ladder — 45 s each at 1,500, 2,500, 3,500, 4,000 — before the run you intend to quote.
* **Settle between runs.** A run that ended in overload leaves leases held for their 60 s TTL
  and ready-queue scores pushed up to 60 s into the future. Wait until the ready queues are due
  again (`ZCOUNT sp:{<site>}:rdy:<eg> -inf <now>` back at the cardinality) and until
  `ZCARD sp:{<site>}:lsexp` is near zero; otherwise the next run measures the recovery.

## Notes

* A `watch_config.js` watcher must hold the *current* config version; the script reads it in
  `setup()`. Holding version 0 makes the server answer immediately and turns the scenario into
  a request flood instead of a long-poll test.
* **Never unlink `sp:{<site>}:ls:*`** while cleaning up, and never trim a report stream that
  still holds unprocessed `release: true` entries. A lease hash is what decrements the
  identity's active-lease counter when the lease ends; deleting live ones leaks that counter and
  the identity is never available again. Recovering means unlinking the whole site prefix and
  running `spnr rebuild --site <site>` (a rebuild alone does not reset runtime counters).
* `RAMP_S` spreads the first poll of each VU. Without it, 10,000 watchers open their
  connections in the same millisecond, which the load balancer — not the server — cannot take.
* `report_ingest.js` keeps its leases for the whole run. Reports arriving after a lease expired
  are still accepted (they are late reports within `SPINNERET_LATE_REPORT_WINDOW`), so keep
  `DURATION` to a few minutes at most.
* Load tests leave traffic-proportional Redis state behind (dedup markers, ended lease hashes,
  stream entries). On a small machine, reclaim it between runs — see
  [Performance and tuning → Housekeeping](../../documents/en/17-performance.md#housekeeping).
