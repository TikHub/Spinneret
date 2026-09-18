-- StatsUpsertAcquireMinutely adds acquire result counters per minute bucket.
-- Rows are parallel arrays of equal length (select-list unnest expands them in
-- lockstep); counters add to existing rows because several instances flush the
-- same buckets. A statement must not repeat a primary key, and rows should be
-- ordered by primary key so concurrent writers lock rows in the same order.
-- The same conventions apply to every upsert in this file.
-- name: StatsUpsertAcquireMinutely :exec
INSERT INTO acquire_stats_minutely AS t (
    bucket, namespace_id, site_id, endpoint_group_id, result, count, duration_us_sum
)
SELECT unnest(sqlc.arg(buckets)::timestamptz[]),
       unnest(sqlc.arg(namespace_ids)::text[]),
       unnest(sqlc.arg(site_ids)::text[]),
       unnest(sqlc.arg(endpoint_group_ids)::text[]),
       unnest(sqlc.arg(results)::text[]),
       unnest(sqlc.arg(counts)::bigint[]),
       unnest(sqlc.arg(duration_us_sums)::bigint[])
ON CONFLICT (bucket, namespace_id, site_id, endpoint_group_id, result) DO UPDATE
SET count = t.count + EXCLUDED.count,
    duration_us_sum = t.duration_us_sum + EXCLUDED.duration_us_sum;

-- StatsUpsertPayloadAccessMinutely adds credential delivery counters per token and identity type.
-- name: StatsUpsertPayloadAccessMinutely :exec
INSERT INTO payload_access_minutely AS t (
    bucket, namespace_id, token_id, identity_type_id, count
)
SELECT unnest(sqlc.arg(buckets)::timestamptz[]),
       unnest(sqlc.arg(namespace_ids)::text[]),
       unnest(sqlc.arg(token_ids)::text[]),
       unnest(sqlc.arg(identity_type_ids)::text[]),
       unnest(sqlc.arg(counts)::bigint[])
ON CONFLICT (bucket, namespace_id, token_id, identity_type_id) DO UPDATE
SET count = t.count + EXCLUDED.count;

-- StatsUpsertNodeMinutely adds per-node lease and report counters.
-- name: StatsUpsertNodeMinutely :exec
INSERT INTO node_stats_minutely AS t (
    bucket, namespace_id, node, acquires, reports, abandoned, rejected
)
SELECT unnest(sqlc.arg(buckets)::timestamptz[]),
       unnest(sqlc.arg(namespace_ids)::text[]),
       unnest(sqlc.arg(nodes)::text[]),
       unnest(sqlc.arg(acquires)::bigint[]),
       unnest(sqlc.arg(reports)::bigint[]),
       unnest(sqlc.arg(abandoned)::bigint[]),
       unnest(sqlc.arg(rejected)::bigint[])
ON CONFLICT (bucket, namespace_id, node) DO UPDATE
SET acquires = t.acquires + EXCLUDED.acquires,
    reports = t.reports + EXCLUDED.reports,
    abandoned = t.abandoned + EXCLUDED.abandoned,
    rejected = t.rejected + EXCLUDED.rejected;

-- StatsUpsertOutcomeMinutely adds report outcome counters and latency/size sums.
-- name: StatsUpsertOutcomeMinutely :exec
INSERT INTO outcome_stats_minutely AS t (
    bucket, namespace_id, site_id, endpoint_group_id, proxy_id, outcome, count, latency_ms_sum, response_bytes_sum
)
SELECT unnest(sqlc.arg(buckets)::timestamptz[]),
       unnest(sqlc.arg(namespace_ids)::text[]),
       unnest(sqlc.arg(site_ids)::text[]),
       unnest(sqlc.arg(endpoint_group_ids)::text[]),
       unnest(sqlc.arg(proxy_ids)::text[]),
       unnest(sqlc.arg(outcomes)::text[]),
       unnest(sqlc.arg(counts)::bigint[]),
       unnest(sqlc.arg(latency_ms_sums)::bigint[]),
       unnest(sqlc.arg(response_bytes_sums)::bigint[])
ON CONFLICT (bucket, namespace_id, site_id, endpoint_group_id, proxy_id, outcome) DO UPDATE
SET count = t.count + EXCLUDED.count,
    latency_ms_sum = t.latency_ms_sum + EXCLUDED.latency_ms_sum,
    response_bytes_sum = t.response_bytes_sum + EXCLUDED.response_bytes_sum;

-- StatsUpsertIdentityHourly adds per-identity outcome counters per hour bucket.
-- name: StatsUpsertIdentityHourly :exec
INSERT INTO identity_stats_hourly AS t (
    bucket, site_id, identity_id, endpoint_group_id, outcome, count
)
SELECT unnest(sqlc.arg(buckets)::timestamptz[]),
       unnest(sqlc.arg(site_ids)::text[]),
       unnest(sqlc.arg(identity_ids)::text[]),
       unnest(sqlc.arg(endpoint_group_ids)::text[]),
       unnest(sqlc.arg(outcomes)::text[]),
       unnest(sqlc.arg(counts)::bigint[])
ON CONFLICT (bucket, identity_id, endpoint_group_id, outcome) DO UPDATE
SET count = t.count + EXCLUDED.count;
