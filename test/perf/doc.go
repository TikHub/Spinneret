// Package perf holds the Redis-side micro-benchmark harness of the Spinneret
// hot path.
//
// Every measuring file carries the "perf" build tag, so the harness is
// compiled only by "go test -tags perf ./test/perf/...". It measures where the
// Valkey CPU of one acquire -> report cycle goes, by running the production Lua
// scripts against a seeded site and attributing the cost with three
// independent instruments:
//
//   - INFO commandstats deltas: EVALSHA calls and microseconds as the server
//     measures them, plus the exact number of nested redis.call() commands per
//     script call (their microseconds are truncated to whole units and are not
//     usable, which is why the primitives are calibrated separately);
//   - SLOWLOG with slowlog-log-slower-than 0: per-call server-side durations,
//     so percentiles are available and not only the mean;
//   - wall-clock timings in Go: what a caller sees, round trip included.
//
// Everything is scoped to the harness: it writes only under its own random key
// prefix and restores every CONFIG value it changes. See README.md for the
// commands and for what each benchmark measures.
package perf
