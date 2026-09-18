# `test/perf` — Redis-side micro-benchmarks of the hot path

A reproducible harness that measures **where the Valkey CPU of one
acquire → report cycle goes**, script by script and primitive by primitive, so
an optimization can be proved rather than argued. It is the companion of
[`docs/benchmarks.md`](../../docs/benchmarks.md), which measures the same system
end to end through the compose stack; this harness isolates the Redis side.

Everything is behind the `perf` build tag, so `go build ./...`,
`go test ./internal/...` and `make e2e` never compile it.

## Requirements

* A reachable Valkey ≥ 7 (Valkey 8.1 for the `FUNCTION` benchmark), named by
  `SPINNERET_TEST_REDIS_URL` or `-perf.url`. The `spinneret-infra` stack's
  Valkey on port 46379 is the intended target.
* `docker` on `PATH`, for `BenchmarkIOThreads` only.

The harness **writes only under its own random key prefix** (`pf<8 hex>:…`),
drops it when the process exits, and saves and restores every `CONFIG`
parameter it changes. It never reconfigures the stack Valkey and never starts a
container except the disposable one in `BenchmarkIOThreads` (name
`spinneret-perf-valkey`, port 46999), which it removes again.

## Running it

```bash
export SPINNERET_TEST_REDIS_URL='redis://localhost:46379/0'

# Everything, with the SLOWLOG per-call percentiles (default).
go test -tags perf -timeout 60m ./test/perf/ -run XXX -bench . -benchtime 1x

# Everything, SLOWLOG off. These are the numbers to quote: "slowlog-log-slower-
# than 0" itself costs about 5 us per acquire call (BenchmarkSlowlogOverhead).
go test -tags perf -timeout 60m ./test/perf/ -run XXX -bench . -benchtime 1x \
  -perf.ops 3000 -perf.slowlog=false

# One group only.
go test -tags perf -timeout 30m ./test/perf/ -run XXX -bench BenchmarkAcquire \
  -benchtime 1x -perf.ops 3000 -perf.slowlog=false

# The §18.4 dataset (100k identities x 50 endpoint groups, ~2.5 s to seed).
go test -tags perf -timeout 60m ./test/perf/ -run XXX -bench BenchmarkAcquire \
  -benchtime 1x -perf.big -perf.ops 3000 -perf.slowlog=false

# io-threads 1 vs 4 and appendonly on/off, on a disposable container.
go test -tags perf -timeout 30m ./test/perf/ -run XXX -bench BenchmarkIOThreads \
  -benchtime 1x -perf.iothreads
```

`-run XXX` selects no `Test*` function, so only the benchmarks run.
`-benchtime 1x` is deliberate: every benchmark does its own fixed-size
measurement (`-perf.ops`) and reports through `b.ReportMetric`, so `b.N` only
controls how many times the whole block repeats.

The table rows are printed to **stderr** as well as to the benchmark log, so
they are readable without `-v`.

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-perf.url` | `$SPINNERET_TEST_REDIS_URL` | Valkey to measure |
| `-perf.ops` | 2000 | measured calls per block |
| `-perf.chunk` | 250 | calls between two dataset-restoring passes |
| `-perf.big` | false | 100k identities × 50 groups instead of 20k × 5 |
| `-perf.slowlog` | true | `slowlog-log-slower-than 0` for per-call percentiles |
| `-perf.keep` | false | keep the seeded keys for inspection |
| `-perf.iothreads` | false | run the disposable-container comparison |

## How a number is produced

Three instruments, because no single one is trustworthy on its own:

1. **`INFO commandstats` deltas** around the measured window. `cmdstat_evalsha`
   `usec / calls` is the server-side cost of one script call as Valkey measures
   it, and it is the authoritative "µs/call" figure. The same deltas give the
   **exact count** of the nested `redis.call()` commands per script call.
   Their `usec`, however, is truncated to whole microseconds per call, so a
   0.2 µs `HGET` is recorded as 0 about 80 % of the time: the `nested=` column is
   a lower bound and is never used for attribution.
2. **A differential calibration** (`BenchmarkPrimitives`, `BenchmarkLuaHelpers`,
   `BenchmarkNumberParsing`). Each primitive runs `loopIters` times inside one
   `EVALSHA`, and the empty-loop cost is subtracted. That measures the real
   sub-microsecond cost of a primitive *as the interpreter pays for it*, and
   these numbers are what the command counts get multiplied with.
3. **`SLOWLOG` with `slowlog-log-slower-than 0`**, for per-call percentiles
   rather than a mean. Nested `redis.call()` commands are logged too, so entries
   are filtered by command name. It costs about 5 µs per acquire call — quantified
   by `BenchmarkSlowlogOverhead` — so headline numbers use `-perf.slowlog=false`.

Wall-clock percentiles are also reported, but on Docker Desktop for macOS a
round trip to a published container port is 110–130 µs, which swamps the
server-side cost. Wall clock is useful for *changes* in the same environment,
not as an absolute.

### Keeping the dataset comparable

An acquire leases an identity, which pushes it out of the ready range; over a
few thousand calls the pool would drain and later calls would measure a
different system. Every block therefore declares a `chunk` and a `maintain`
function: after each chunk the harness restores the ready ZSETs, the identity
lease counters and the lease keys the chunk wrote. `maintain` runs **outside**
the timed window and outside the `commandstats` window, and uses plain commands
only (never `EVAL`/`EVALSHA`/`FCALL`), so it cannot be mistaken for the script
under test.

Blocks whose writes would change how acquire behaves — `apply.lua` writing an
identity × endpoint cooldown, `breaker_eval.lua` tripping a breaker — run
against `dataset.egIdle()`, an endpoint group no acquire block samples, and
delete their state afterwards. `BenchmarkAcquirePhases` fails if the full-script
phase ever returns anything but `OK`, which is the tripwire for this class of
contamination.

## The benchmarks

| Benchmark | What it answers |
| --- | --- |
| `BenchmarkBaseline` | `PING`, `EVALSHA return 0`, `EVALSHA common.lua + return 0`, the same with a production-sized `ARGV`, and `EVAL` of the cached source (the `NOSCRIPT` fallback). The second and third rows isolate the per-call cost of re-creating the 17 `common.lua` closures. |
| `BenchmarkSlowlogOverhead` | What `slowlog-log-slower-than 0` adds, so a SLOWLOG run can be corrected. |
| `BenchmarkPrimitives` | Calibrated µs/call of every Redis primitive the hot path uses, at this dataset's sizes. |
| `BenchmarkLuaHelpers` | Calibrated µs/call of the pure-Lua helpers (`sp_hs_unpack`, `sp_hs_pack`, the pool arrays, the `hs` parsers). No Redis command: whatever they cost is interpreter time. |
| `BenchmarkNumberParsing` | `tonumber` by input shape, plus `cjson` and `struct` alternatives. |
| `BenchmarkAcquire` | `acquire.lua` at candidate_sample 1/4/8/32, on a clean pool and on a pool where 50 % of candidates are filtered, plus `count=10`, one quota window, `max_concurrent_leases=4` and `strategy=best_health`. |
| `BenchmarkAcquireBreakdown` | The exact command mix of one acquire call, clean and filtered. |
| `BenchmarkAcquirePhases` | The assembled script truncated after each of its six parts: how much is dispatch, prelude, `ARGV` decoding, closure definitions, and the real work. |
| `BenchmarkAcquireArgv` | The `ARGV`-decoding section alone, with and without the 20 random floats. |
| `BenchmarkAcquireFloor` | A script issuing the identical 12 Redis commands with the identical payloads and **no decision logic**: the hard lower bound, and therefore the budget an optimization can work in. |
| `BenchmarkAcquireLocalGlobals` | Whether binding `KEYS`/`ARGV`/stdlib to chunk-level locals helps (interleaved rounds, best of three, because run-to-run drift is ±8 %). |
| `BenchmarkLeaseEnd` | `release.lua` with and without the `xg` exclusive-push marker, `renew.lua`, `reap.lua` at batch 100. |
| `BenchmarkObserve` | `observe.lua` for `success` and `rate_limited`, with and without quota windows, count-condition counters and ban-escalation windows. |
| `BenchmarkSignal` | `ingest.lua` at batch 1 and 100, `lease_retain.lua` at 1 and 100 leases per call. |
| `BenchmarkApplyBreaker` | `apply.lua` (identity × endpoint cooldown) and `breaker_eval.lua` in `read` and `eval` mode over a 12-bucket window. |
| `BenchmarkFunction` | The same `common.lua` + `acquire.lua` sources loaded as a Valkey `FUNCTION` library and called with `FCALL`, against `EVALSHA`. |
| `BenchmarkOptimizationCandidates` | Rewritten helpers measured next to the shipped ones on identical input. |
| `BenchmarkIOThreads` | Closed-loop acquire throughput on a disposable Valkey at `io-threads` 1 and 4, `appendonly` no and yes, at 1/8/32 concurrent callers. |
| `BenchmarkABAcquire` | The shipped `acquire.lua` against the frozen pre-optimization sources under `testdata/baseline`, interleaved. |
| `BenchmarkABLeaseEnd` | The same for `release.lua`, `renew.lua` and `reap.lua`. |
| `BenchmarkABPrelude` | `observe.lua` and `lease_retain.lua` — bodies the scheduler track did not touch — in front of the frozen `common.lua` and in front of the shipped one, which is what the shared prelude change is worth to the rest of the system. |
| `BenchmarkObservePhases` | `observe.lua` truncated after each of its blocks: dispatch and `ARGV` transport, checkpoint, `ARGV` decode, lease/quota, window buckets, breaker read, cross attribution, identity read, the `hs` update, the global update, counters, ban counts, reply. |
| `BenchmarkApplyPhases` | `apply.lua` truncated after its header, its nine shared helper closures, its eight handler closures and its two dispatch tables: what the script pays before any operation runs. |
| `BenchmarkBreakerParts` | The twelve `HMGET`s of a breaker window against the single `PFCOUNT` over the twelve captcha HyperLogLogs. |
| `BenchmarkABWorker` | The shipped `observe.lua`, `lease_retain.lua`, `apply.lua` and `breaker_eval.lua` against the frozen pre-worker-track sources under `testdata/worker_baseline`, interleaved. |

## Before and after

`testdata/baseline/*.lua` are frozen copies of the scheduler scripts and of
`common.lua` as they were before the acquire-path optimization, assembled the way
the loader assembled them then (the whole `common.lua` in front of every body).
The `BenchmarkAB*` blocks run both variants in one process, alternating blocks,
and keep the best block of each — a sequential "before run, after run" cannot
decide anything at ±8 % drift. Each side gets the wire forms its own code reads:
the expanded endpoint group layout and decimal fractions for the baseline, the
packed layout and parts-per-million integers for the shipped scripts.

```bash
export SPINNERET_TEST_REDIS_URL='redis://localhost:46379/0'
go test -tags perf -timeout 60m ./test/perf/ -run XXX -bench BenchmarkAB \
  -benchtime 1x -perf.ops 3000 -perf.slowlog=false            # 20k x 5
go test -tags perf -timeout 60m ./test/perf/ -run XXX -bench BenchmarkAB \
  -benchtime 1x -perf.ops 3000 -perf.big -perf.slowlog=false  # 100k x 50
```

Server-side µs per call, Valkey 8.1.10 on Docker Desktop / M4 Max, best of three
interleaved rounds of 3 000 calls:

| block | 20k x 5 before | after | 100k x 50 before | after |
| --- | ---: | ---: | ---: | ---: |
| `acquire.lua` candidate_sample 32, clean | 85.4 | **57.4** (−32.8 %) | 85.1 | **59.3** (−30.3 %) |
| `acquire.lua` candidate_sample 32, 50 % filtered | 102.4 | **69.7** (−32.0 %) | 102.7 | **72.2** (−29.7 %) |
| `acquire.lua` candidate_sample 8, clean | 73.8 | **56.3** (−23.7 %) | 73.3 | **57.7** (−21.2 %) |
| `acquire.lua` candidate_sample 1, clean | 70.5 | **54.6** (−22.5 %) | 67.6 | **55.6** (−17.8 %) |
| `release.lua`, `xg` unset | 39.4 | **29.1** (−26.2 %) | 53.6 | **29.2** (−45.6 %) |
| `release.lua`, `xg` set by one other group | 44.5 | **38.2** (−14.1 %) | 96.3 | **60.1** (−37.6 %) |
| `renew.lua` | 17.9 | **15.9** (−11.4 %) | 17.8 | **15.6** (−12.4 %) |
| `reap.lua`, per expired lease at batch 100 | 20.2 | **13.0** (−35.6 %) | 20.7 | **14.1** (−31.8 %) |
| `observe.lua` (prelude only) | 48.8 | **47.2** (−3.4 %) | 46.7 | **44.9** (−4.0 %) |
| `lease_retain.lua` (prelude only) | 10.4 | **8.9** (−14.9 %) | 10.6 | **9.0** (−14.7 %) |

One acquire and one release, the pair the hot path runs per request, cost
138.7 µs before and 88.4 µs after on the 100k x 50 dataset: **−36 %**.

## Before and after — the worker path

`testdata/worker_baseline/*.lua` freeze the report-path scripts and `common.lua`
as they stood **after** the scheduler-path optimization and before the
worker-path one, so `BenchmarkABWorker` isolates what the second track changed.
The baseline bodies are assembled with the same helper selection the loader
applies (`selectPrelude` in `worker_ab_test.go`), so neither side pays for
closures the other does not create.

```bash
export SPINNERET_TEST_REDIS_URL='redis://localhost:46379/0'
go test -tags perf -timeout 40m ./test/perf/ -run XXX -bench BenchmarkABWorker \
  -benchtime 1x -perf.ops 3000 -perf.slowlog=false
```

Server-side µs per call, best of three interleaved rounds of 3 000 calls,
Valkey 8.1.10 on Docker Desktop / M4 Max:

| block | before | after |
| --- | ---: | ---: |
| `observe.lua` success, no counters | 46.9 | **31.0** (−33.9 %) |
| `observe.lua` rate_limited, 2 counters + 2 ban windows | 61.8 | **41.6** (−32.6 %) |
| `lease_retain.lua`, a request with one lease | 9.7 | **9.4** (−2.7 %) |
| `lease_retain.lua` per lease, 100 leases of one site | 9.2 | **2.0** (−78.2 %) |
| `apply.lua`, automatic identity × endpoint cooldown | 47.3 | **34.4** (−27.2 %) |
| `breaker_eval.lua` mode `read`, 12-bucket window | 47.5 | **37.9** (−20.3 %) |
| `breaker_eval.lua` mode `eval`, 12-bucket window | 54.3 | **44.1** (−18.8 %) |

One report of a 100-report batch — `ingest.lua` 2.6 + `lease_retain.lua` 9.2 +
`observe.lua` 46.9 = **58.7 µs** before, 2.6 + 2.0 + 31.0 = **35.6 µs** after:
**−39 %** of the Valkey CPU of the report path.

`BenchmarkApplyBreaker`'s `apply` block must raise the cooldown end on every
call (`applyCooldownArgs` takes `until`): an operation whose end is not later
than the one already on the entry returns `already_cooling` without writing, and
the block would measure the early return.

## Proving an optimization

1. Record a baseline of the blocks the change touches, with `-perf.slowlog=false`
   and at least `-perf.ops 3000`:

   ```bash
   go test -tags perf ./test/perf/ -run XXX -bench 'BenchmarkAcquire|BenchmarkLeaseEnd|BenchmarkObserve|BenchmarkSignal' \
     -benchtime 1x -perf.ops 3000 -perf.slowlog=false 2>&1 | tee before.txt
   ```

2. Make the change, re-run the identical command into `after.txt`, and compare
   the `server=` column. **Run the pair back to back on an idle machine**: the
   run-to-run drift of the same code on the same data is ±8 %, so anything
   smaller than that needs interleaved repetitions (see
   `BenchmarkAcquireLocalGlobals` for the pattern).

3. A change that alters the command *mix* must show it: compare the
   `over N cmds` figure and the `reportNested` table, not only the total.

4. The harness reads the `.lua` sources from disk at run time
   (`scripts_test.go:repoRoot`), so editing a script under
   `internal/**/lua/*.lua` is picked up without touching the harness.

5. Correctness is not this harness's job. `go test -race ./internal/...` and
   `make e2e` remain the gate; the harness only asserts that a measured call
   returned the status it expected, so a broken script cannot look fast.
