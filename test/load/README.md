# Load tests (k6)

Scenarios for the v0.1 performance targets of the design document (§18.4), run against the
Connect JSON API of the `deploy/compose` stack. The measured results, the analysis and the
tuning recommendations live in [`docs/benchmarks.md`](../../docs/benchmarks.md)
([中文](../../docs/benchmarks.zh-CN.md)).

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
| `acquire_report.js` | `ACQUIRE_RATE`, `DURATION`, `REPORT_MODE` (`release`, `keep`, `none`), `PRE_VUS`, `MAX_VUS` |
| `report_ingest.js` | `BATCH`, `REPORT_RATE`, `DURATION`, `LEASES`, `PRE_VUS`, `MAX_VUS` |
| `watch_config.js` | `WATCHERS`, `POLLS_PER_VU`, `DURATION`, `WATCH_TIMEOUT_MS`, `RAMP_S`, `CONFIG_GROUP`, `CONFIG_KEY` |
| `run.sh` | `LOADTEST_TOKEN` (required), `OUT_DIR`, `MID_AFTER` (seconds before the mid-run snapshot), `MID_STATS` (`0` to skip the `docker stats` part of it) |

Each scenario prints a readable summary plus a machine-readable block between
`---K6SUMMARY---` markers, which `run.sh` extracts into `k6.json`.

## Getting numbers that mean something

Three things make the difference between a measurement and a story. All three are documented
with the evidence in [benchmarks.md](../../docs/benchmarks.md).

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
  [benchmarks.md → Housekeeping](../../docs/benchmarks.md#housekeeping).
