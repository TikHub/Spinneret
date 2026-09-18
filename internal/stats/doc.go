// Package stats aggregates lease and report statistics in memory and flushes
// them to PostgreSQL (and raw events to ClickHouse).
//
// The hot paths (scheduler acquire/lease end, worker report processing, report
// ingest rejections) call the Record* methods of an Aggregator. They never
// block on I/O: every call merges one record into sharded, mutex-protected
// maps keyed by the primary key of each aggregate table, appends non-success
// reports to a bounded risk-event buffer and queues ClickHouse rows on the
// non-blocking clickhouse.Writer.
//
// Aggregator.Run flushes every 10 seconds (and once more on shutdown):
//
//   - acquire_stats_minutely  (bucket, namespace_id, site_id, endpoint_group_id, result) → count, duration_us_sum
//   - payload_access_minutely (bucket, namespace_id, token_id, identity_type_id) → count of successful acquires
//   - node_stats_minutely     (bucket, namespace_id, node) → acquires (ok), reports (all), abandoned, rejected
//   - outcome_stats_minutely  (bucket, namespace_id, site_id, endpoint_group_id, proxy_id, outcome) → count, latency_ms_sum, response_bytes_sum
//   - identity_stats_hourly   (bucket, identity_id, endpoint_group_id, outcome) → count (site_id kept from the first record)
//   - risk_events             one row per non-success report (COPY)
//
// Aggregates are written with one INSERT … SELECT unnest(…) ON CONFLICT DO
// UPDATE statement per chunk (at most 5000 rows) that adds the counters to the
// stored row, so several instances can flush the same buckets. Rows are sorted
// by primary key so that concurrent writers lock rows in the same order.
//
// Failure handling per flush:
//
//   - retryable errors (connection loss, timeouts, deadlocks, lock timeouts,
//     insufficient resources, …) are retried up to three times with
//     exponential backoff; afterwards the rows stay pending and are merged
//     with the next flush. When the database as a whole is unavailable
//     (client-side failures, SQLSTATE classes 08, 53, 57 except 57014, 58)
//     the remaining writes of the whole flush are skipped; after any other
//     server error only the remaining writes of that table are skipped, so
//     one failing table does not starve the others;
//   - permanent server errors (undefined table, insufficient privilege, …)
//     are not retried; the rows stay pending and the table's remaining
//     writes of the flush are skipped;
//   - a missing partition (SQLSTATE 23514 without constraint name, "no
//     partition of relation … found for row") triggers one
//     postgres.EnsurePartitions call per flush and a retry; rows whose bucket
//     still has no partition (far outside the managed window) are dropped per
//     partition period;
//   - data errors (SQLSTATE classes 22 and 23, 54000) are isolated by
//     bisecting the chunk; only the offending rows are dropped.
//
// Memory is bounded: each aggregate table accepts at most 200 000 distinct keys
// per flush interval (further records are dropped and counted), the pending
// set kept after failed flushes is capped at the same size (rows of the oldest
// buckets are dropped first) and the risk-event buffer and its pending set
// each hold at most 50 000 rows and about 64 MiB.
// Drop counters are exposed by Aggregator.Counters and logged at most once per
// flush.
//
// Normalizations applied at record time: bucket times are UTC; an empty node
// name is stored as "_" in node_stats_minutely (matching the GetNodeStats
// contract); an empty report outcome is stored as "unknown"; text values are
// made valid UTF-8 without NUL bytes and truncated to bounded lengths; a
// report's event time is FinishedAt, or ReceivedAt when FinishedAt is zero or
// implausible (more than 5 minutes after ReceivedAt or more than 24 hours
// before it).
package stats
